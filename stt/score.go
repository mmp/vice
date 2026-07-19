// Package stt provides a local algorithmic speech-to-command parser
// for ATC transcripts, replacing the LLM-based approach with fast fuzzy matching.
package stt

import (
	"slices"
	"strings"
	"unicode"
)

// This file holds the package's single similarity primitive: WordScore, a
// graded [0,1] measure of how plausibly a transcript word is a whisper
// rendering of a vocabulary word. Every matching layer builds on it
// (FuzzyMatch is WordScore compared against a threshold), so similarity
// tuning happens in exactly one place.
//
// The constants below are the complete inventory of similarity scores.
// Matching thresholds must not be introduced elsewhere.
const (
	// scoreFuzzyCap caps every non-exact match so an exact token always
	// outscores any fuzzy interpretation of the same span.
	scoreFuzzyCap = 0.95
	// scorePhoneticExact is awarded when two words have identical
	// metaphone codes: near-certain phonetic identity, but below any
	// letter-exact match.
	scorePhoneticExact = 0.90
	// scorePhoneticPartial is awarded for partial metaphone agreement
	// (prefix/suffix/subsequence, with letter-similarity guards).
	scorePhoneticPartial = 0.85
	// scorePhoneticGraded scales Jaro-Winkler over metaphone codes for
	// weak phonetic similarity. It is always below scorePhoneticPartial,
	// so it discriminates between competing candidates without ever
	// crossing a boolean match threshold on its own.
	scorePhoneticGraded = 0.80
	// scoreTokenBaseline is the reference per-token score for ranking
	// competing template matches: each consumed token is credited by how
	// much its match quality exceeds this baseline, so longer matches
	// that explain tokens at better-than-noise quality outrank short
	// exact prefixes.
	scoreTokenBaseline = 0.55
	// scoreSayAgainCredit is the per-token rank credit of a SAYAGAIN
	// partial match: above skipping the tokens as noise (zero credit),
	// but well below any real parse (at least log(0.8/0.55) ≈ 0.37 per
	// token), so a complete interpretation always wins.
	scoreSayAgainCredit = 0.10
	// scoreSkipOverMatch is the penalty for skipping a token where a
	// template match was available. Near-prohibitive: ignoring an
	// available interpretation to reparse the following tokens must only
	// win when the downstream gain is large (several exact tokens), not
	// because an alternative explains one extra trailing word.
	scoreSkipOverMatch = 0.10
	// callsignJointMargin is how close (in callsign confidence) an
	// alternative callsign candidate must be for command-parsability to
	// override the callsign ranking. Switching requires the leader's
	// remainder to be unparseable while the alternative's parses — strong
	// evidence — so the margin is generous. Beyond it the callsign
	// evidence alone decides.
	callsignJointMargin = 0.10
	// scoreCallsignSwitchGain is how much better (in coverage-adjusted
	// parse score) an alternative callsign candidate's remainder must
	// parse before it displaces the leader — roughly two exact tokens'
	// worth of extra explained command content.
	scoreCallsignSwitchGain = 1.2
	// scoreAltSegmentation slightly handicaps the runner-up template at a
	// position when the decoder branches on competing segmentations: at
	// equal evidence the primary (priority-ranked) interpretation wins,
	// while an alternative that explains more tokens still prevails.
	scoreAltSegmentation = 0.90
)

// WordScore returns a graded similarity in [0,1] between a transcript word
// and a vocabulary target: 1.0 for an exact (case-insensitive) match,
// otherwise the best of letter similarity (Jaro-Winkler), phonetic
// similarity, and known whisper confusions, capped at scoreFuzzyCap.
func WordScore(word, target string) float64 {
	if strings.EqualFold(word, target) {
		return 1
	}
	w := strings.ToLower(word)
	t := strings.ToLower(target)

	// Known false positives: similar-looking words with entirely different
	// ATC meanings never match.
	if slices.Contains(fuzzyMatchBlocklist[w], t) {
		return 0
	}
	// Very short words fuzzy-match longer targets too easily ("i" vs "in").
	if len(w) < 3 && len(t) > len(w) {
		return 0
	}

	score := JaroWinkler(w, t)
	if p := phoneticScore(w, t); p > score {
		score = p
	}
	if c := confusionScore(w, t); c > score {
		score = c
	}
	return min(score, scoreFuzzyCap)
}

// FuzzyMatch reports whether word matches target at the given threshold.
func FuzzyMatch(word, target string, threshold float64) bool {
	return WordScore(word, target) >= threshold
}

// PhoneticMatch reports whether two words are phonetically similar enough
// to treat as the same word in isolation.
func PhoneticMatch(w1, w2 string) bool {
	return phoneticScore(w1, w2) >= scorePhoneticPartial
}

// phoneticScore returns a graded phonetic similarity in
// [0, scorePhoneticExact] based on metaphone code agreement.
func phoneticScore(w1, w2 string) float64 {
	p1, a1 := DoubleMetaphone(w1)
	p2, a2 := DoubleMetaphone(w2)

	// Exact metaphone match, considering alternate encodings.
	if p1 == p2 || p1 == a2 || a1 == p2 || (a1 != "" && a1 == a2) {
		return scorePhoneticExact
	}
	if phoneticPartialMatch(w1, w2, p1, p2) {
		return scorePhoneticPartial
	}
	return scorePhoneticGraded * JaroWinkler(p1, p2)
}

// phoneticPartialMatch reports partial agreement between two words'
// primary metaphone codes: prefix, suffix, or subsequence containment,
// guarded by letter-level similarity to filter false positives.
func phoneticPartialMatch(w1, w2, p1, p2 string) bool {
	minPLen := min(len(p1), len(p2))
	maxPLen := max(len(p1), len(p2))

	// Short-code prefix: handles STT errors that add or drop trailing
	// sounds (e.g., "rhea" → "R", "reebo" → "RP"). The longer code is at
	// most 2 chars and the words must have decent letter similarity.
	if maxPLen == 2 {
		if strings.HasPrefix(p1, p2) || strings.HasPrefix(p2, p1) {
			if JaroWinkler(w1, w2) >= 0.65 {
				return true
			}
		}
	}

	// Longer-code prefix (3-6 chars): handles transcriptions that add
	// extra syllables producing extra trailing consonants, e.g.
	// "klomanad" (KLMNT) vs "clomn" (KLMN). Requires a higher letter
	// similarity and at most 2 extra consonants.
	if minPLen >= 3 && maxPLen <= 6 && maxPLen-minPLen <= 2 {
		if strings.HasPrefix(p1, p2) || strings.HasPrefix(p2, p1) {
			if JaroWinkler(w1, w2) >= 0.70 {
				return true
			}
		}
	}

	// Suffix matching: handles STT errors that drop leading sounds
	// (e.g., "laser" for "localizer"). The shorter code must be at least
	// 3 chars to avoid false positives.
	if len(p1) >= 3 && len(p2) >= 3 {
		if strings.HasSuffix(p1, p2) || strings.HasSuffix(p2, p1) {
			return true
		}
		// Shared suffix of 4+ characters. (3-char suffixes like "TNK"
		// are too common and cause false positives like "decelerating"
		// matching "heading".)
		for suffixLen := minPLen; suffixLen >= 4; suffixLen-- {
			if p1[len(p1)-suffixLen:] == p2[len(p2)-suffixLen:] {
				return true
			}
		}
	}

	// Subsequence matching: handles STT errors that interleave extra
	// sounds throughout a word (e.g., "lampstand" LMPSTNT contains
	// "lobstah" LPST as a subsequence). Guards: shorter code >= 4 chars,
	// at least 2 extra chars in the longer code (to avoid near-identical
	// pairs like "claimed" KLMT / "climbed" KLMPT), same first char, and
	// decent letter similarity.
	if minPLen >= 4 && maxPLen-minPLen >= 2 {
		shorter, longer := p1, p2
		if len(p1) > len(p2) {
			shorter, longer = p2, p1
		}
		if shorter[0] == longer[0] && isSubsequence(shorter, longer) {
			if JaroWinkler(w1, w2) >= 0.70 {
				return true
			}
		}
	}

	return false
}

// confusionTable holds whisper-specific word confusions that neither letter
// nor phonetic similarity can recover (the transcription sounds nothing
// like what was said). Entries map a transcript word to the vocabulary
// words it may stand for, each with a score. This is the only place such
// confusion data lives: add pairs here, never special-case code elsewhere.
// Targets containing a space are two-word confusions: the transcript word
// stands for two adjacent template words and is matched by the fused-word
// matcher ("fighting" for "fly heading").
var confusionTable = map[string][]confusion{
	"rate":        {{"radar", 0.85}},            // "rate of contact" for "radar contact"
	"read":        {{"radar", 0.85}},            // "i read or contact" for "radar contact"
	"reader":      {{"radar", 0.85}},            // "reader contact" for "radar contact"
	"rare":        {{"radar", 0.85}},            // "rare contact" for "radar contact"
	"wait":        {{"radar", 0.85}},            // "wait our contact" for "radar contact"
	"fighting":    {{"fly heading", 0.85}},      // "fighting three three zero"
	"flighting":   {{"fly heading", 0.85}},      // "flighting zero two zero"
	"sediment":    {{"descend maintain", 0.85}}, // "sediment day four thousand"
	"centimeter":  {{"descend maintain", 0.85}},
	"decimate":    {{"descend maintain", 0.85}},
	"disassembly": {{"descend via", 0.85}},    // "disassembly star"
	"insight":     {{"in sight", 0.85}},       // "report traffic insight"
	"clementine":  {{"climb maintain", 0.85}}, // "clementine 10000"
	"send":        {{"descend", 0.85}},        // "to send me ten ..." for "descend to one zero ..."
	// Garbled digit words, consumed by the number decoder's digit
	// extension. Scored above the decoder's dropped-zero repairs: a
	// recognized garbled digit beats assuming the trailing word is noise.
	"fine": {{"five", 0.92}},
	"once": {{"one", 0.92}},
	// Garbled command keywords, migrated from the retired normalization
	// rewrite table.
	"decent":  {{"descend", 0.85}},
	"setup":   {{"descend", 0.85}},
	"climin":  {{"climb", 0.85}},
	"club":    {{"climb", 0.85}},
	"con":     {{"climb", 0.85}},
	"fight":   {{"flight", 0.85}},
	"space":   {{"speed", 0.85}},
	"ready":   {{"reduce", 0.85}, {"radar", 0.85}},
	"secured": {{"cleared", 0.85}},
	"extract": {{"expect", 0.85}},
	"isle":    {{"ils", 0.85}},
	"rls":     {{"ils", 0.85}},
	"arnav":   {{"rnav", 0.85}},
	"cansec":  {{"contact", 0.85}},
	"tarot":   {{"tower", 0.85}},
	"tarom":   {{"tower", 0.85}},
	// Advisory phrases: "the field is at your eleven o'clock" and
	// "caution wake turbulence".
	"feels":     {{"field", 0.85}},
	"weight":    {{"wake", 0.85}},
	"terminals": {{"turbulence", 0.85}},
	// "right [turn] direct ..." spoken quickly comes out as "route
	// direct": the phrase form is consumed only by the fused-word matcher
	// inside the right-direct template, so "route" alone still never
	// reads as a bare "right" turn (see the blocklist entry).
	"route": {{"right direct", 0.85}},
	// "rioted in two seven zero" for "right turn two seven zero".
	"rioted": {{"right", 0.85}},
}

type confusion struct {
	target string
	score  float64
}

// confusionScore returns the confusion-table score for reading word as
// target, or 0 if the pair is not listed.
func confusionScore(word, target string) float64 {
	for _, c := range confusionTable[word] {
		if c.target == target {
			return c.score
		}
	}
	return 0
}

// fuzzyMatchBlocklist contains pairs of words that should NOT fuzzy-match.
// These are known false positives where similar-looking words have completely
// different meanings in ATC context.
var fuzzyMatchBlocklist = map[string][]string{
	"intercept":    {"increase", "speed"},  // "intercept localizer" vs "increase/speed"
	"increase":     {"intercept", "cross"}, // "increase speed" vs "cross fix"
	"cross":        {"increase"},
	"see":          {"speed"},                         // "see ya" vs "speed"
	"degrees":      {"increase"},                      // garbled STT output
	"flight":       {"right"},                         // "flight 638" vs "turn right"
	"heading":      {"descending"},                    // "heading 180" vs "descend"
	"had":          {"heading"},                       // "just had to" vs "heading" command
	"descend":      {"present"},                       // "descend and maintain" vs "present heading"
	"present":      {"descend"},                       // "present heading" vs "descend"
	"maximum":      {"minimum"},                       // "maximum speed" vs "minimum speed"
	"minimum":      {"maximum"},                       // "minimum speed" vs "maximum speed"
	"stand":        {"ident"},                         // "stand on the sand" vs "squawk ident"
	"red":          {"right", "reduce"},               // garbled word in phrases like "Red or Collins"
	"rig":          {"right"},                         // garbage word vs turn direction
	"route":        {"right"},                         // "route romeo 3017" (garbled "altimeter 3017") is not "right"
	"departure":    {"depart"},                        // position ID ("NY departure") vs depart fix instruction
	"departures":   {"depart"},                        // position ID plural vs depart instruction
	"depart":       {"departure"},                     // depart-fix instruction vs position ID ("NY departure")
	"project":      {"direct", "proceed"},             // "project" in "miami project" is not "direct"
	"approach":     {"approved", "direct", "proceed"}, // "approach" in position ID is not direct
	"approved":     {"approach"},
	"pro":          {"direct", "proceed"}, // Garbled "pro" is not direct/proceed
	"redo":         {"right"},             // "redo speed" should not match "turn right"
	"san":          {"say"},               // "san juan" should not match "say"
	"star":         {"standby"},           // "Lone Star approach" should not match "standby"
	"setup":        {"set"},               // "setup" is a garbled "descend", never "set the localizer"
	"decent":       {"ident"},             // "decent" is a garbled "descend", never "ident"
	"radar":        {"departure"},         // metaphone suffix RTR/TPRTR; "radar contact" is not a position ID
	"quit":         {"good"},              // identical metaphone KT; "quit" is a garbled "cleared", not a sign-off
	"right":        {"night", "flight"},   // "turn right" / runway direction; never "good night" or "flight level"
	"intermittent": {"ident"},             // noise word should not match "ident" command
	"claimed":      {"climbed", "climb"},  // STT noise vs altitude command
	"maintained":   {"maintain"},          // STT echo after "maintain" should not re-match
	"hitting":      {"heading"},           // garbled word should not match heading command
	"information":  {"uniform"},           // "information X" is the ATIS keyword, not NATO letter U
	"atis":         {"at"},                // "ATIS information X" is not "at {fix}" — don't let the ATIS phrase be hijacked by at-fix handlers
}

// JaroWinkler computes the Jaro-Winkler similarity between two strings.
// Returns a value between 0.0 (no similarity) and 1.0 (identical).
// Jaro-Winkler gives higher weight to strings that match from the beginning.
func JaroWinkler(s1, s2 string) float64 {
	s1 = strings.ToLower(s1)
	s2 = strings.ToLower(s2)

	jaro := jaroSimilarity(s1, s2)

	// Find common prefix length (max 4 characters for Winkler adjustment)
	prefixLen := 0
	for i := 0; i < min(len(s1), len(s2), 4); i++ {
		if s1[i] == s2[i] {
			prefixLen++
		} else {
			break
		}
	}

	// Winkler adjustment: boost score for common prefix
	// Standard scaling factor is 0.1
	return jaro + float64(prefixLen)*0.1*(1-jaro)
}

// jaroSimilarity computes the Jaro similarity between two strings.
func jaroSimilarity(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	// Calculate match window
	matchWindow := max(len(s1), len(s2))/2 - 1
	if matchWindow < 0 {
		matchWindow = 0
	}

	s1Matches := make([]bool, len(s1))
	s2Matches := make([]bool, len(s2))

	matches := 0
	transpositions := 0

	// Find matching characters
	for i := 0; i < len(s1); i++ {
		start := max(0, i-matchWindow)
		end := min(len(s2), i+matchWindow+1)

		for j := start; j < end; j++ {
			if s2Matches[j] || s1[i] != s2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	// Count transpositions
	k := 0
	for i := 0; i < len(s1); i++ {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if s1[i] != s2[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	t := float64(transpositions) / 2

	return (m/float64(len(s1)) + m/float64(len(s2)) + (m-t)/m) / 3
}

// DoubleMetaphone generates phonetic encodings for a word.
// Returns primary and alternate encodings. The alternate may be empty.
// This is a simplified implementation covering common ATC vocabulary.
func DoubleMetaphone(word string) (primary, alternate string) {
	word = strings.ToUpper(word)
	if len(word) == 0 {
		return "", ""
	}

	var pri, alt strings.Builder
	i := 0

	// Skip silent letters at start
	if hasPrefix(word, "GN", "KN", "PN", "WR", "PS") {
		i = 1
	}

	for i < len(word) && pri.Len() < 8 {
		c := word[i]
		switch c {
		case 'A', 'E', 'I', 'O', 'U':
			if i == 0 {
				pri.WriteByte('A')
				alt.WriteByte('A')
			}
			i++

		case 'B':
			pri.WriteByte('P')
			alt.WriteByte('P')
			if i+1 < len(word) && word[i+1] == 'B' {
				i += 2
			} else {
				i++
			}

		case 'C':
			if i+1 < len(word) && word[i+1] == 'H' {
				pri.WriteByte('X')
				alt.WriteByte('X')
				i += 2
			} else if i+1 < len(word) && (word[i+1] == 'I' || word[i+1] == 'E' || word[i+1] == 'Y') {
				pri.WriteByte('S')
				alt.WriteByte('S')
				i++
			} else {
				pri.WriteByte('K')
				alt.WriteByte('K')
				i++
			}

		case 'D':
			if i+1 < len(word) && word[i+1] == 'G' {
				if i+2 < len(word) && (word[i+2] == 'I' || word[i+2] == 'E' || word[i+2] == 'Y') {
					pri.WriteByte('J')
					alt.WriteByte('J')
					i += 2
				} else {
					pri.WriteByte('T')
					pri.WriteByte('K')
					alt.WriteByte('T')
					alt.WriteByte('K')
					i += 2
				}
			} else {
				pri.WriteByte('T')
				alt.WriteByte('T')
				i++
			}

		case 'F':
			pri.WriteByte('F')
			alt.WriteByte('F')
			if i+1 < len(word) && word[i+1] == 'F' {
				i += 2
			} else {
				i++
			}

		case 'G':
			if i+1 < len(word) && word[i+1] == 'H' {
				if i > 0 && !isVowel(word[i-1]) {
					pri.WriteByte('K')
					alt.WriteByte('K')
				}
				i += 2
			} else if i+1 < len(word) && word[i+1] == 'N' {
				pri.WriteByte('N')
				alt.WriteByte('K')
				alt.WriteByte('N')
				i += 2
			} else if i+1 < len(word) && (word[i+1] == 'I' || word[i+1] == 'E' || word[i+1] == 'Y') {
				pri.WriteByte('J')
				alt.WriteByte('K')
				i++
			} else {
				pri.WriteByte('K')
				alt.WriteByte('K')
				i++
			}

		case 'H':
			if i == 0 || (i > 0 && isVowel(word[i-1])) {
				if i+1 < len(word) && isVowel(word[i+1]) {
					pri.WriteByte('H')
					alt.WriteByte('H')
				}
			}
			i++

		case 'J':
			pri.WriteByte('J')
			alt.WriteByte('J')
			i++

		case 'K':
			pri.WriteByte('K')
			alt.WriteByte('K')
			if i+1 < len(word) && word[i+1] == 'K' {
				i += 2
			} else {
				i++
			}

		case 'L':
			pri.WriteByte('L')
			alt.WriteByte('L')
			if i+1 < len(word) && word[i+1] == 'L' {
				i += 2
			} else {
				i++
			}

		case 'M':
			pri.WriteByte('M')
			alt.WriteByte('M')
			if i+1 < len(word) && word[i+1] == 'M' {
				i += 2
			} else {
				i++
			}

		case 'N':
			pri.WriteByte('N')
			alt.WriteByte('N')
			if i+1 < len(word) && word[i+1] == 'N' {
				i += 2
			} else {
				i++
			}

		case 'P':
			if i+1 < len(word) && word[i+1] == 'H' {
				pri.WriteByte('F')
				alt.WriteByte('F')
				i += 2
			} else {
				pri.WriteByte('P')
				alt.WriteByte('P')
				if i+1 < len(word) && word[i+1] == 'P' {
					i += 2
				} else {
					i++
				}
			}

		case 'Q':
			pri.WriteByte('K')
			alt.WriteByte('K')
			i++

		case 'R':
			pri.WriteByte('R')
			alt.WriteByte('R')
			if i+1 < len(word) && word[i+1] == 'R' {
				i += 2
			} else {
				i++
			}

		case 'S':
			if i+1 < len(word) && word[i+1] == 'H' {
				pri.WriteByte('X')
				alt.WriteByte('X')
				i += 2
			} else if i+2 < len(word) && word[i+1] == 'I' && (word[i+2] == 'O' || word[i+2] == 'A') {
				pri.WriteByte('X')
				alt.WriteByte('S')
				i += 3
			} else {
				pri.WriteByte('S')
				alt.WriteByte('S')
				if i+1 < len(word) && word[i+1] == 'S' {
					i += 2
				} else {
					i++
				}
			}

		case 'T':
			if i+1 < len(word) && word[i+1] == 'H' {
				pri.WriteByte('0') // Using 0 for TH sound
				alt.WriteByte('T')
				i += 2
			} else if i+2 < len(word) && word[i+1] == 'I' && (word[i+2] == 'O' || word[i+2] == 'A') {
				pri.WriteByte('X')
				alt.WriteByte('X')
				i += 3
			} else {
				pri.WriteByte('T')
				alt.WriteByte('T')
				if i+1 < len(word) && word[i+1] == 'T' {
					i += 2
				} else {
					i++
				}
			}

		case 'V':
			pri.WriteByte('F')
			alt.WriteByte('F')
			i++

		case 'W':
			if i+1 < len(word) && isVowel(word[i+1]) {
				pri.WriteByte('W')
				alt.WriteByte('W')
			}
			i++

		case 'X':
			pri.WriteByte('K')
			pri.WriteByte('S')
			alt.WriteByte('K')
			alt.WriteByte('S')
			i++

		case 'Y':
			if i+1 < len(word) && isVowel(word[i+1]) {
				pri.WriteByte('Y')
				alt.WriteByte('Y')
			}
			i++

		case 'Z':
			pri.WriteByte('S')
			alt.WriteByte('S')
			if i+1 < len(word) && word[i+1] == 'Z' {
				i += 2
			} else {
				i++
			}

		default:
			i++
		}
	}

	return pri.String(), alt.String()
}

// isSubsequence returns true if every character of short appears in long
// in order (not necessarily contiguously).
func isSubsequence(short, long string) bool {
	si := 0
	for li := 0; li < len(long) && si < len(short); li++ {
		if short[si] == long[li] {
			si++
		}
	}
	return si == len(short)
}

// Helper functions

func hasPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isVowel(c byte) bool {
	return c == 'A' || c == 'E' || c == 'I' || c == 'O' || c == 'U'
}

// normalizeVowels normalizes common vowel patterns for better fuzzy matching.
// Handles cases where speech recognition contracts syllables (e.g., "gayel" -> "gail").
func normalizeVowels(s string) string {
	s = strings.ToLower(s)
	// Common vowel-y-vowel patterns that get contracted in speech
	s = strings.ReplaceAll(s, "aye", "ai")
	s = strings.ReplaceAll(s, "eye", "i")
	s = strings.ReplaceAll(s, "oye", "oi")
	s = strings.ReplaceAll(s, "uye", "ui")
	s = strings.ReplaceAll(s, "er", "i")
	return s
}

// normalizeConsonantClusters normalizes consonant clusters that sound alike.
// Handles cases where STT uses a different spelling for the same sound
// (e.g., "sachs" vs "socks" — "ch" and "ck" both produce a /k/ sound).
func normalizeConsonantClusters(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ck", "k")
	s = strings.ReplaceAll(s, "ch", "k")
	return s
}

// normalizeCK normalizes C/K equivalence for phonetically identical sounds.
// STT may use K for a hard C sound or vice versa (e.g., "kelse" for "Celtic").
func normalizeCK(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "c", "k")
}

// hasInitialCKSwap returns true if one string starts with 'c' and the other
// starts with 'k' (case-insensitive), indicating an initial C/K substitution.
func hasInitialCKSwap(a, b string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	aFirst := strings.ToLower(a[:1])
	bFirst := strings.ToLower(b[:1])
	return (aFirst == "c" && bFirst == "k") || (aFirst == "k" && bFirst == "c")
}

// CleanWord removes non-alphanumeric characters from a word.
func CleanWord(w string) string {
	var sb strings.Builder
	for _, r := range w {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}

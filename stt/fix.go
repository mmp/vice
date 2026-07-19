package stt

import (
	"slices"
	"strings"
)

// This file matches transcript spans against the aircraft's fix
// vocabulary. Every candidate fix is scored against every 1-3 token span
// with one scoring function, and the ranked candidates decide the match —
// there is no strategy cascade. NATO spellings ("that's delta papa kilo")
// override fuzzy matches since they are more explicit.

// fixCandidate is one scored interpretation of the leading tokens as a fix.
type fixCandidate struct {
	fix      string
	score    float64
	consumed int
}

// fixMatchScore scores reading phrase as a rendering of a fix's spoken
// name or identifier. Exact = 1.0; the tiers below mirror the relative
// trust in each kind of evidence.
func fixMatchScore(phrase, spokenName, fixID string) float64 {
	spokenLower := strings.ToLower(spokenName)
	if strings.EqualFold(phrase, spokenName) {
		return 1.0
	}

	score := 0.0
	// Phonetic agreement with the spoken name. Checked without a length
	// guard since phonetic matching accounts for extra syllables (e.g.,
	// "klomanad" → "Clomn").
	if PhoneticMatch(phrase, spokenName) {
		score = 0.80
	}
	// Truncated rendering: whisper cut the word short ("OB" for "Opihi");
	// the phrase's metaphone code opens the spoken name's.
	if pp, _ := DoubleMetaphone(phrase); len(pp) >= 2 {
		if sp, _ := DoubleMetaphone(spokenName); strings.HasPrefix(sp, pp) {
			score = max(score, 0.78)
		}
	}

	// Letter-similarity strategies require comparable lengths (prevents
	// "pucky heading 180" matching "Pucky").
	if float64(len(phrase))/float64(len(spokenName)) <= 1.5 {
		if jw := JaroWinkler(phrase, spokenName); jw >= 0.78 {
			score = max(score, jw)
		}
		// Normalized comparisons at a slight penalty: vowel contractions
		// ("gail" ~ "gayel"), sound-alike consonant clusters ("sachs" ~
		// "socks"), and initial C/K substitution ("kelse" ~ "celtic").
		if norm := JaroWinkler(normalizeVowels(phrase), normalizeVowels(spokenLower)); norm >= 0.78 {
			score = max(score, norm*0.95)
		}
		if norm := JaroWinkler(normalizeConsonantClusters(phrase), normalizeConsonantClusters(spokenLower)); norm >= 0.78 {
			score = max(score, norm*0.95)
		}
		if hasInitialCKSwap(phrase, spokenName) {
			if norm := JaroWinkler(normalizeCK(phrase), normalizeCK(spokenLower)); norm >= 0.78 {
				score = max(score, norm*0.95)
			}
		}
		// Initial-consonant garble: whisper often mishears the first
		// consonant of a proper name while preserving the rest ("hokus" or
		// "bokus" for "Vocus"). Accept when the remainders after the first
		// letter agree strongly, with C/K normalized; graded by the tail
		// agreement so an exact tail outranks scrambled-letter similarity
		// to other candidates ("bokus" must prefer Vocus over KBOS).
		if len(phrase) >= 4 && len(spokenLower) >= 4 {
			if jw := JaroWinkler(normalizeCK(phrase[1:]), normalizeCK(spokenLower[1:])); jw >= 0.9 {
				score = max(score, jw*0.83)
			}
		}
		// Consonant-skeleton equality for vowel-heavy STT errors
		// ("zizou" ~ "zzooo").
		if len(phrase) >= 3 && len(spokenName) >= 3 {
			if cons := extractConsonants(phrase); len(cons) >= 2 && cons == extractConsonants(spokenName) {
				score = max(score, 0.78)
			}
		}
		// The fix identifier itself: STT may transcribe the identifier
		// pronunciation rather than the spoken form ("betel" for BAKEL).
		if fixIDLower := strings.ToLower(fixID); fixIDLower != spokenLower {
			if PhoneticMatch(phrase, fixIDLower) {
				score = max(score, 0.78)
			}
			if jw := JaroWinkler(phrase, fixIDLower); jw >= 0.75 {
				score = max(score, jw)
			}
		}
	}
	return score
}

// fixCandidates scores every fix against the 1-3 token spans at the start
// of tokens and returns the viable candidates best-first. An exact spoken
// match at the longest span wins outright.
func fixCandidates(tokens []Token, fixes map[string]string) []fixCandidate {
	var cands []fixCandidate
	for length := min(3, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := range length {
			parts = append(parts, tokens[i].Text)
		}
		phrase := strings.Join(parts, " ")

		for spokenName, fixID := range fixes {
			if score := fixMatchScore(phrase, spokenName, fixID); score > 0 {
				cands = append(cands, fixCandidate{fix: fixID, score: score, consumed: length})
			}
		}
	}

	slices.SortStableFunc(cands, func(a, b fixCandidate) int {
		if a.score != b.score {
			if a.score > b.score {
				return -1
			}
			return 1
		}
		// An exact match of more tokens is more specific; among fuzzy ties
		// the alphabetically earlier fix wins for determinism, keeping the
		// longer span for the same fix.
		if a.score == 1.0 && a.consumed != b.consumed {
			return b.consumed - a.consumed
		}
		if a.fix != b.fix {
			if a.fix < b.fix {
				return -1
			}
			return 1
		}
		return b.consumed - a.consumed
	})

	// Keep the best entry per fix.
	seen := make(map[string]bool)
	cands = slices.DeleteFunc(cands, func(c fixCandidate) bool {
		if seen[c.fix] {
			return true
		}
		seen[c.fix] = true
		return false
	})
	return cands
}

// extractFix extracts a fix name from tokens by matching against known fixes.
func extractFix(tokens []Token, fixes map[string]string) (string, float64, int) {
	if len(tokens) == 0 {
		logLocalStt("  extractFix: no tokens to match")
		return "", 0, 0
	}
	if len(fixes) == 0 {
		logLocalStt("  extractFix: no fixes in aircraft context, cannot match")
		return "", 0, 0
	}

	logLocalStt("  extractFix: trying to match tokens[0]=%q against %d fixes", tokens[0].Text, len(fixes))

	var bestFix string
	var bestScore float64
	var bestLength int
	if cands := fixCandidates(tokens, fixes); len(cands) > 0 {
		bestFix, bestScore, bestLength = cands[0].fix, cands[0].score, cands[0].consumed
	}

	// Look for spelling patterns that may follow the spoken name.
	// Controllers often spell out fix names: "direct Deer Park, that's
	// delta papa kilo".
	searchStart := bestLength
	if bestFix == "" {
		// No match yet - still check for spelling after the first word
		searchStart = 1
	}

	if searchStart > 0 && searchStart < len(tokens) {
		spelledFix, spellingConf, spellingConsumed := extractSpelledFix(tokens[searchStart:], fixes)
		if spelledFix != "" {
			totalConsumed := searchStart + spellingConsumed
			if bestFix == "" {
				// No initial match - use spelling as primary
				logLocalStt("  extractFix: no spoken match, using spelling %q", spelledFix)
				return spelledFix, spellingConf, totalConsumed
			}
			if spelledFix == bestFix {
				// Spelling confirms our match - boost confidence
				logLocalStt("  extractFix: spelling confirms match %q", bestFix)
				return bestFix, max(bestScore, 0.98), totalConsumed
			}
			// Spelling contradicts match - prefer spelling (more explicit)
			logLocalStt("  extractFix: spelling %q overrides spoken match %q", spelledFix, bestFix)
			return spelledFix, spellingConf, totalConsumed
		}
	}

	// Try NATO spelling from the very start of tokens. This handles cases
	// where the controller spells out the entire fix name and the first NATO
	// letter word coincidentally fuzzy-matches a different fix (e.g.,
	// "sierra sierra oscar xray sierra" = SSOXS, where the first "sierra"
	// might fuzzy-match fix "SEY").
	if bestScore < 1.0 {
		spelledFix, spellingConf, spellingConsumed := extractSpelledFix(tokens, fixes)
		if spelledFix != "" {
			logLocalStt("  extractFix: full NATO spelling from start %q overrides fuzzy match %q", spelledFix, bestFix)
			return spelledFix, spellingConf, spellingConsumed
		}
	}

	if bestFix != "" {
		logLocalStt("  extractFix: matched %q -> %q (fuzzy %.2f)", tokens[0].Text, bestFix, bestScore)
		return bestFix, bestScore, bestLength
	}
	logLocalStt("  extractFix: no match found for %q", tokens[0].Text)
	return "", 0, 0
}

// extractConsonants extracts only consonants from a string (for fuzzy matching).
func extractConsonants(s string) string {
	var result strings.Builder
	s = strings.ToUpper(s) // isVowel expects uppercase
	for _, c := range s {
		if c >= 'A' && c <= 'Z' && !isVowel(byte(c)) {
			result.WriteRune(c)
		}
	}
	return strings.ToLower(result.String())
}

// extractSpelledFix looks for a spelled-out fix name in tokens using NATO phonetic alphabet.
// Handles patterns like "that's delta papa kilo" or "charlie alpha mike romeo november".
// Returns the fix ID if spelling matches a fix, confidence, and tokens consumed.
func extractSpelledFix(tokens []Token, fixes map[string]string) (string, float64, int) {
	if len(tokens) == 0 || len(fixes) == 0 {
		return "", 0, 0
	}

	startIdx := 0

	// Check for trigger phrase ("that's", "spelled", etc.)
	if IsSpellingTrigger(tokens[0].Text) {
		startIdx = 1
		if startIdx >= len(tokens) {
			return "", 0, 0
		}
	}

	// Extract NATO letters from remaining tokens
	var words []string
	for i := startIdx; i < len(tokens); i++ {
		words = append(words, tokens[i].Text)
	}

	spelled, natoConsumed := ExtractNATOSpelling(words)
	if len(spelled) < 2 {
		// Need at least 2 letters for a valid fix spelling
		return "", 0, 0
	}

	logLocalStt("  extractSpelledFix: extracted spelling %q from %d NATO words", spelled, natoConsumed)

	// Check if spelled string matches any fix ID (by value)
	for _, fixID := range fixes {
		if strings.EqualFold(spelled, fixID) {
			totalConsumed := startIdx + natoConsumed
			logLocalStt("  extractSpelledFix: spelling %q matches fix %q", spelled, fixID)
			return fixID, 0.95, totalConsumed
		}
	}

	logLocalStt("  extractSpelledFix: spelling %q does not match any fix", spelled)
	return "", 0, 0
}

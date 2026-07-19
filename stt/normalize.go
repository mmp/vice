package stt

import (
	"slices"
	"strconv"
	"strings"
)

// Phonetic normalization maps for STT error correction.
// These handle common speech-to-text errors and ATC phonetic alphabet.

// digitWords maps spoken digit words to their numeric string representation.
var digitWords = map[string]string{
	// Standard
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
	"five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
	// ATC phonetic
	"niner": "9", "fower": "4", "fife": "5", "tree": "3",
	// Common homophones
	"won": "1", "wun": "1", "ones": "1",
	"too":  "2",             // "to" is intentionally excluded - it's a common word
	"fore": "4",             // "for" is intentionally excluded - it's a common word
	"ate":  "8", "ait": "8", // Homophone for "eight"
	"oh":   "0", // Common way to say zero
	"zeri": "0", // Whisper STT transcription of "zero"
	"fire": "5", // STT mishearing of "five" (/faɪr/ ≈ /faɪv/)
}

// numberWords maps multi-digit number words to values.
var numberWords = map[string]string{
	"ten":       "10",
	"eleven":    "11",
	"twelve":    "12",
	"thirteen":  "13",
	"fourteen":  "14",
	"fifteen":   "15",
	"sixteen":   "16",
	"seventeen": "17",
	"eighteen":  "18",
	"nineteen":  "19",
	"twenty":    "20",
	"toser":     "20",  // STT error: "twenty" or "two zero" transcribed as "toser"
	"twenzo":    "210", // STT error: "two one zero" transcribed as "twenzo"
	"thirty":    "30",
	"forty":     "40",
	"fifty":     "50",
	"sixty":     "60",
	"seventy":   "70",
	"eighty":    "80",
	"ninety":    "90",
	// Note: "hundred" is NOT in this map because parseAltitudePattern
	// expects the word "hundred" to remain as-is for patterns like
	// "two thousand five hundred" -> 2500 ft (encoded as 25).
}

// natoAlphabet maps NATO phonetic alphabet to letters.
var natoAlphabet = map[string]string{
	"alpha": "a", "alfa": "a",
	"bravo":   "b",
	"charlie": "c",
	"delta":   "d",
	"echo":    "e",
	"foxtrot": "f",
	"golf":    "g",
	"hotel":   "h",
	"india":   "i",
	"juliet":  "j", "juliett": "j", "juliette": "j", // Legitimate spelling variants
	"kilo":     "k",
	"lima":     "l",
	"mike":     "m",
	"november": "n",
	"oscar":    "o",
	"papa":     "p",
	"quebec":   "q",
	"romeo":    "r",
	"sierra":   "s",
	"tango":    "t",
	"uniform":  "u",
	"victor":   "v",
	"whiskey":  "w", "whisky": "w", // Legitimate spelling variants
	"xray": "x", "x-ray": "x",
	"yankee": "y",
	"zulu":   "z",
}

// ConvertNATOLetter converts a NATO phonetic word to its letter.
// Returns the letter and true if found, empty string and false otherwise.
func ConvertNATOLetter(word string) (string, bool) {
	letter, ok := natoAlphabet[strings.ToLower(word)]
	return letter, ok
}

// spellingTriggerWords are words that introduce a spelling of a previously spoken name.
// For example: "proceed direct Deer Park, that's delta papa kilo"
var spellingTriggerWords = map[string]bool{
	"thats":    true, // "that's" after punctuation removal
	"spelled":  true,
	"spelling": true,
}

// IsSpellingTrigger returns true if the word introduces a spelling correction.
func IsSpellingTrigger(word string) bool {
	return spellingTriggerWords[strings.ToLower(word)]
}

// ExtractNATOSpelling extracts consecutive NATO phonetic letters from words.
// Returns the spelled-out string (uppercase) and number of words consumed.
// Stops at the first non-NATO word.
func ExtractNATOSpelling(words []string) (string, int) {
	var result strings.Builder
	consumed := 0
	for _, word := range words {
		if letter, ok := ConvertNATOLetter(word); ok {
			result.WriteString(strings.ToUpper(letter))
			consumed++
		} else {
			break
		}
	}
	return result.String(), consumed
}

// multiTokenReplacements maps sequences of tokens (space-joined) to replacements.
var multiTokenReplacements = map[string][]string{
	"i l s":          {"ils"},
	"r nav":          {"rnav"},
	"air nav":        {"rnav"}, // STT error: "R-Nav" transcribed as "Air Nav"
	"fly level":      {"flight", "level"},
	"time riding":    {"turn", "right"},
	"seven e":        {"70"},
	"eight e":        {"80"},
	"nine e":         {"90"},
	"six e":          {"60"},
	"five e":         {"50"},
	"r on a":         {"runway"},
	"right a star":   {"via", "star"},
	"i dead":         {"ident"},
	"i file":         {"5", "mile"},   // STT error: "five mile" transcribed as "I file"
	"for left":       {"4", "left"},   // STT error: "four left" transcribed as "for left"
	"for right":      {"4", "right"},  // STT error: "four right" transcribed as "for right"
	"for center":     {"4", "center"}, // STT error: "four center" transcribed as "for center"
	"vector as":      {"vector"},      // STT error: "vector for/to the" transcribed as "vector as"
	"vectors as":     {"vectors"},
	"to recall":      {"direct"},                // STT error: "direct" (TRKT) transcribed as "to recall" (TRKL)
	"mark point":     {"mach", "point"},         // STT error: "mach" mistranscribed as "mark" before "point"
	"i s t f":        {"ils"},                   // STT error: ILS spelled out, badly garbled
	"descend me the": {"descend", "via", "the"}, // STT error: "via" mistranscribed as "me" in "descend via the {STAR}"
	"send me the":    {"descend", "via", "the"}, // Same, with "descend" also garbled to "send"
	// Original utterance is "radar contact"; restore so radar_contact_info handler can strip it.
	"reduce to contact": {"radar", "contact"},
	// Original utterance is "radar contact" mistranscribed as "rate of contact".
	"right of contact": {"radar", "contact"},
	// ("radar" heard as "route of").
	"route of contact": {"radar", "contact"},
	"to park":          {"depart"}, // STT error: "depart" (TPRT) mistranscribed as "to park" (TPRK)
	"at the set":       {"descend"},
}

// matchMultiToken tries to match tokens against multiTokenReplacements.
// Returns (matched, replacement, tokensConsumed).
func matchMultiToken(tokens []string) (bool, []string, int) {
	// Try longest matches first (4 tokens, then 3, then 2)
	for length := min(4, len(tokens)); length >= 2; length-- {
		key := strings.Join(tokens[:length], " ")
		if replacement, ok := multiTokenReplacements[key]; ok {
			return true, replacement, length
		}
	}
	return false, nil, 0
}

// fillerWords are words to ignore during parsing.
var fillerWords = map[string]bool{
	"and": true, "the": true, "a": true, "an": true,
	"uh": true, "um": true, "uhh": true, "umm": true, "ah": true,
	"please": true, "thanks": true, "thank": true, "you": true,
	"day": true, "morning": true, "afternoon": true, "evening": true,
	"sir": true, "ma'am": true,
	"roger": true, "wilco": true, "copy": true,
	"heavy": true, "super": true, // Callsign suffixes to ignore
	"continue": true, "your": true, // "continue your right turn" - modifiers, not commands
	"to":   true,               // Often appears in garbled number sequences ("10 to 1 3 0" for "130")
	"off":  true,               // STT noise in "turn off heading" → "turn heading"
	"wing": true,               // STT error: "left-wing" for "left heading" becomes "left wing" after hyphen removal
	"i":    true, "said": true, // Pilot interjections ("I said I maintained...")
	"is":      true, // Prevents "is" from fuzzy matching fix names like "ISLAY" (Jaro-Winkler 0.84)
	"having":  true, // Prevents "having" from fuzzy matching "heading" (Jaro-Winkler 0.86)
	"leaving": true, // Prevents "leaving" from fuzzy matching "heading" (Jaro-Winkler 0.81)
	"cetera":  true, // Prevents "cetera" from fuzzy matching "cleared" (Jaro-Winkler 0.80)
	// Note: "contact" and "radar" are NOT filler words - they're command keywords
}

// normalizeContext holds state for the word normalization pipeline.
type normalizeContext struct {
	words  []string // raw input words (may be mutated by processors, e.g. "or" → "thousand")
	result []string // accumulated output tokens
	i      int      // current index into words
}

// wordProcessor tries to handle a word during normalization.
// Returns (replacement tokens, extra words to skip, handled).
// Processors may read/write ctx.words and ctx.result for context-dependent decisions.
type wordProcessor func(w string, ctx *normalizeContext) ([]string, int, bool)

// wordProcessors are applied in order during normalization. First match wins.
// Order is critical — multi-token raw patterns must precede single-word lookups,
// and exact lookups must precede fuzzy/phonetic matching.
var wordProcessors = []wordProcessor{
	processMultiTokenRaw,
	processSplitTextNumber,
	processDigitWord,
	processNumberWord,
	processCommandVocabulary,
	processFlightLevelMissing,
	processLevelMissingFlight,
	processAndDigit,
}

// commandVocabulary is the spoken ATC command vocabulary: canonical command
// words and their real morphological variants. Words in this set pass
// through normalization untouched — they are meaningful as spoken and must
// not be reinterpreted by the garble-recovery processors below. Garbled
// renderings of these words are not repaired here; the scored template
// matching recognizes them via similarity and the confusion table.
var commandVocabulary = map[string]bool{
	// Altitude
	"descend": true, "descending": true, "descent": true,
	"climb": true, "climbing": true, "maintain": true, "maintained": true,
	"altitude": true, "thousand": true, "thousandth": true, "thousandths": true,
	"hundred": true, "flight": true, "level": true, "expedite": true,
	// Heading
	"heading": true, "turn": true, "turning": true, "turned": true,
	"left": true, "right": true, "degrees": true, "fly": true, "present": true,
	// Speed
	"speed": true, "reduce": true, "increase": true, "slow": true,
	"down": true, "up": true,
	"slowest": true, "minimum": true, "maximum": true, "forward": true,
	"knots": true, "normal": true, "mach": true,
	// Navigation
	"direct": true, "directed": true, "proceed": true, "cross": true,
	"across": true, "depart": true, "hold": true, "land": true,
	"short": true, "via": true, "sid": true,
	// Holds
	"radial": true, "bearing": true, "inbound": true, "legs": true,
	"minute": true, "turns": true, "published": true,
	// Compass directions
	"north": true, "south": true, "east": true, "west": true,
	"northeast": true, "northwest": true, "southeast": true, "southwest": true,
	// Approach
	"cleared": true, "expect": true, "expected": true, "vectors": true,
	"approach": true, "cancel": true, "localizer": true, "localize": true,
	"intercept": true, "intercepted": true, "clearance": true,
	"visual": true, "ils": true, "rnav": true, "vor": true, "runway": true,
	// Transponder
	"squawk": true, "transponder": true, "ident": true, "standby": true, "mode": true,
	// Handoff
	"contact": true, "tower": true, "frequency": true, "departure": true, "center": true,
	// VFR/misc
	"go": true, "ahead": true, "radar": true, "services": true,
	"terminated": true, "resume": true, "own": true, "navigation": true, "vfr": true,
	// Discourse
	"disregard": true, "negative": true, "further": true, "then": true,
}

// processCommandVocabulary passes known command words through untouched.
func processCommandVocabulary(w string, _ *normalizeContext) ([]string, int, bool) {
	if commandVocabulary[w] {
		return []string{w}, 0, true
	}
	return nil, 0, false
}

// processMultiTokenRaw checks for multi-token patterns on raw words BEFORE phonetic matching.
// This catches patterns like "time riding" → "turn right" before "riding" gets
// normalized to "heading" via phonetic match.
func processMultiTokenRaw(w string, ctx *normalizeContext) ([]string, int, bool) {
	if ctx.i+1 >= len(ctx.words) {
		return nil, 0, false
	}
	rawNext := CleanWord(ctx.words[ctx.i+1])
	key := w + " " + rawNext
	if replacement, ok := multiTokenReplacements[key]; ok {
		return replacement, 1, true
	}
	return nil, 0, false
}

// processSplitTextNumber splits concatenated callsigns like "alaska8383" → ["alaska", "8383"].
func processSplitTextNumber(w string, _ *normalizeContext) ([]string, int, bool) {
	if parts := splitTextNumber(w); len(parts) > 1 {
		var out []string
		for _, part := range parts {
			if part != "" {
				out = append(out, part)
			}
		}
		return out, 0, true
	}
	return nil, 0, false
}

// processDigitWord normalizes digit words: "zero" → "0", "niner" → "9", etc.
func processDigitWord(w string, _ *normalizeContext) ([]string, int, bool) {
	if digit, ok := digitWords[w]; ok {
		return []string{digit}, 0, true
	}
	return nil, 0, false
}

// processNumberWord normalizes number words: "twenty" → "20", "thirty" → "30", etc.
// When the previous token is a fix-context keyword (direct/proceed/via), leave
// the word unconverted so it can be phonetically matched against fix names
// downstream (e.g., "direct eleven" → fix "Livin"/LIVVN).
func processNumberWord(w string, ctx *normalizeContext) ([]string, int, bool) {
	if num, ok := numberWords[w]; ok {
		if n := len(ctx.result); n > 0 {
			switch ctx.result[n-1] {
			case "direct", "proceed", "via":
				return nil, 0, false
			}
		}
		return []string{num}, 0, true
	}
	return nil, 0, false
}

// NOTE: NATO alphabet conversion is NOT done here because words like
// "delta" could be airline names (Delta Air Lines). NATO conversion is
// deferred to scoreGACallsign() and scoreFlightNumberMatch() where the
// context makes it clear we're building a callsign from phonetics.

// altitudeCommandKeywords names command keywords whose presence in prior
// normalized output indicates we're parsing an altitude command, used to gate
// processFlightLevelMissing so airline callsigns like "Frontier flight 8555"
// are not affected.
var altitudeCommandKeywords = map[string]bool{
	"cross":    true,
	"descend":  true,
	"climb":    true,
	"maintain": true,
	"at":       true,
}

// processFlightLevelMissing handles transcripts where the spoken "level" word
// was dropped or mis-transcribed (e.g., "flight level 280" heard as "flight
// 280" or "flight zero 280"). When "flight" is followed by exactly 3 digit
// words in an altitude-command context, inserts "level" so tokenize.go can
// form a TokenAltitude. When followed by exactly 4 digit words, also drops
// the first — almost certainly a garbled "level".
func processFlightLevelMissing(w string, ctx *normalizeContext) ([]string, int, bool) {
	if w != "flight" || ctx.i+1 >= len(ctx.words) {
		return nil, 0, false
	}
	if CleanWord(ctx.words[ctx.i+1]) == "level" {
		return nil, 0, false
	}
	inAltContext := false
	for _, t := range ctx.result {
		if altitudeCommandKeywords[t] {
			inAltContext = true
			break
		}
	}
	if !inAltContext {
		return nil, 0, false
	}
	count := 0
	for j := ctx.i + 1; j < len(ctx.words); j++ {
		wj := CleanWord(ctx.words[j])
		if _, ok := digitWords[wj]; ok {
			count++
			continue
		}
		if IsNumber(wj) {
			count++
			continue
		}
		break
	}
	switch count {
	case 3:
		return []string{"flight", "level"}, 0, true
	case 4:
		return []string{"flight", "level"}, 1, true
	}
	return nil, 0, false
}

// processLevelMissingFlight handles transcripts where "level" survived but the
// preceding "flight" was dropped (e.g., "maintain level two eight zero"). When
// "level" is followed by exactly 3 digit words in an altitude-command context
// and the prior token isn't already "flight", emits ["flight", "level"] so
// tokenize.go can form a TokenAltitude.
func processLevelMissingFlight(w string, ctx *normalizeContext) ([]string, int, bool) {
	if w != "level" || ctx.i+1 >= len(ctx.words) {
		return nil, 0, false
	}
	if n := len(ctx.result); n > 0 && ctx.result[n-1] == "flight" {
		return nil, 0, false
	}
	inAltContext := false
	for _, t := range ctx.result {
		if altitudeCommandKeywords[t] {
			inAltContext = true
			break
		}
	}
	if !inAltContext {
		return nil, 0, false
	}
	count := 0
	for j := ctx.i + 1; j < len(ctx.words); j++ {
		wj := CleanWord(ctx.words[j])
		if _, ok := digitWords[wj]; ok {
			count++
			continue
		}
		if IsNumber(wj) {
			count++
			continue
		}
		break
	}
	if count == 3 {
		return []string{"flight", "level"}, 0, true
	}
	return nil, 0, false
}

// processAndDigit handles "and" between digits: STT mishears "one" as "and".
// e.g., "two and zero" → "two one zero" (210).
// But "two nine and zero" → "290" (and is filler, not replacing one).
// And when either side is already a multi-digit number (e.g., "8000 and 250"),
// "and" is a natural-language connector between distinct values, not a misheard
// "one"; drop it as filler.
func processAndDigit(w string, ctx *normalizeContext) ([]string, int, bool) {
	if w != "and" || len(ctx.result) == 0 || ctx.i+1 >= len(ctx.words) {
		return nil, 0, false
	}
	prev := ctx.result[len(ctx.result)-1]
	nextWord := CleanWord(ctx.words[ctx.i+1])

	prevIsDigit := IsNumber(prev)
	_, nextIsDigitWord := digitWords[nextWord]
	nextIsDigit := IsNumber(nextWord) || nextIsDigitWord
	if prevIsDigit && nextIsDigit {
		// In a multi-digit sequence (prev-prev is also a digit), skip "and" as filler.
		if len(ctx.result) >= 2 && IsNumber(ctx.result[len(ctx.result)-2]) {
			return nil, 0, true
		}
		// digitWords entries always map to a single-digit string; bare numeric
		// tokens carry their own length. The "and→1" mishearing only makes sense
		// in pure single-digit sequences (e.g., "two and zero" → "210"). When
		// either side is multi-digit, "and" is a connector — drop it as filler.
		nextLen := 1
		if IsNumber(nextWord) {
			nextLen = len(nextWord)
		}
		if len(prev) > 1 || nextLen > 1 {
			return nil, 0, true
		}
		return []string{"1"}, 0, true // "and" → "1"
	}
	return nil, 0, false
}

// NormalizeTranscript normalizes a raw STT transcript for parsing.
// Handles phonetic corrections and cleanup. Disregard handling is done
// at a higher level after callsign matching.
// collapseRepetitions collapses three or more consecutive occurrences of a
// one- or two-word group down to one ("x ray x ray x ray jet" → "x ray
// jet"): whisper loops on unclear audio. Two occurrences are meaningful
// (NATO spelling like "sierra sierra"), and repeated digits are real
// content ("seven seven seven" for a 777), so both are left alone.
func collapseRepetitions(words []string) []string {
	isDigits := func(w string) bool {
		for _, r := range w {
			if r < '0' || r > '9' {
				return false
			}
		}
		return len(w) > 0
	}
	for g := 2; g >= 1; g-- {
		var out []string
		for i := 0; i < len(words); {
			reps := 1
			for i+(reps+1)*g <= len(words) && slices.Equal(words[i:i+g], words[i+reps*g:i+(reps+1)*g]) {
				reps++
			}
			hasWord := slices.ContainsFunc(words[i:i+min(g, len(words)-i)], func(w string) bool { return !isDigits(w) })
			if reps >= 3 && hasWord {
				out = append(out, words[i:i+g]...)
				i += reps * g
			} else {
				out = append(out, words[i])
				i++
			}
		}
		words = out
	}
	return words
}

func NormalizeTranscript(transcript string) []string {
	// Convert to lowercase and split
	transcript = strings.ToLower(strings.TrimSpace(transcript))
	if transcript == "" {
		return nil
	}

	// Replace hyphens with spaces so "1-1-thousand" becomes "1 1 thousand"
	transcript = strings.ReplaceAll(transcript, "-", " ")
	// Replace "@" with "at" so "@ CAMRN" is treated as "at CAMRN"
	transcript = strings.ReplaceAll(transcript, "@", "at")

	words := strings.Fields(transcript)
	if len(words) == 0 {
		return nil
	}
	words = collapseRepetitions(words)

	// Normalize each word through the processor pipeline
	ctx := &normalizeContext{words: words}
	skipCount := 0
	for i := range len(words) {
		if skipCount > 0 {
			skipCount--
			continue
		}

		w := CleanWord(words[i])
		if w == "" {
			continue
		}

		ctx.i = i
		handled := false
		for _, proc := range wordProcessors {
			if replacement, skip, ok := proc(w, ctx); ok {
				ctx.result = append(ctx.result, replacement...)
				skipCount = skip
				handled = true
				break
			}
		}
		if !handled {
			ctx.result = append(ctx.result, w)
		}
	}

	// Post-process: combine tens+units (e.g., "30 2" → "32"), join letter sequences, fix multi-word errors
	result := combineTensAndUnits(ctx.result)
	result = postProcessNormalized(result)

	return result
}

// combineTensAndUnits combines "tens + units" patterns into proper two-digit numbers.
// For example: "30 2" → "32", "40 5" → "45", "20 1" → "21"
// This handles spoken numbers like "thirty two" which normalize to "30" + "2"
// but should be understood as the number 32 (e.g., flight 32, heading 320).
func combineTensAndUnits(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}

	result := make([]string, 0, len(tokens))
	i := 0

	for i < len(tokens) {
		// Check for tens + units pattern
		if i+1 < len(tokens) {
			curr := tokens[i]
			next := tokens[i+1]

			// Current token must be a "tens" value (20, 30, 40, 50, 60, 70, 80, 90)
			// Next token must be a single digit (1-9)
			if isTensValue(curr) && IsSingleDigit19(next) {
				// Before combining, check for aircraft type pattern:
				// If prev + tens forms an aircraft type AND next is followed by "thousand",
				// don't combine - the units digit belongs to the altitude, not the aircraft type.
				// Example: "a 3 20 4 thousand" = A320 at 4000 feet, not "a 3 24 thousand"
				skipCombine := false
				if i > 0 && i+2 < len(tokens) {
					prev := tokens[i-1]
					afterUnits := strings.ToLower(tokens[i+2])

					if IsNumber(prev) && (afterUnits == "thousand" || afterUnits == "thousandth") {
						prevNum := ParseNumber(prev)
						tens := ParseNumber(curr)
						// Aircraft type pattern: single digit + tens = 3-digit type
						// e.g., 3 + 20 = 320 (A320)
						if prevNum >= 1 && prevNum <= 9 {
							potentialType := prevNum*100 + tens
							if isNormalizerAircraftType(potentialType) {
								skipCombine = true
							}
						}
					}
				}

				if skipCombine {
					result = append(result, curr)
					i++
					continue
				}

				// Combine: "30" + "2" → "32"
				tens := ParseNumber(curr)
				units := ParseNumber(next)
				combined := strconv.Itoa(tens + units)
				result = append(result, combined)
				i += 2
				continue
			}
		}

		result = append(result, tokens[i])
		i++
	}

	return result
}

// isNormalizerAircraftType returns true if the number is a common aircraft type.
// Used during normalization to avoid combining tens+units when they're part of
// an aircraft type followed by an altitude (e.g., "A320 four thousand").
func isNormalizerAircraftType(n int) bool {
	switch n {
	// Airbus narrow-body
	case 319, 320, 321:
		return true
	// Airbus wide-body
	case 330, 340, 350, 380:
		return true
	}
	return false
}

// isTensValue returns true if the string is a "tens" value (20, 30, 40, 50, 60, 70, 80, 90).
func isTensValue(s string) bool {
	switch s {
	case "20", "30", "40", "50", "60", "70", "80", "90":
		return true
	}
	return false
}

// IsSingleDigit19 returns true if the string is a single digit 1-9.
func IsSingleDigit19(s string) bool {
	return len(s) == 1 && s[0] >= '1' && s[0] <= '9'
}

// postProcessor tries to handle a token during post-processing.
// Returns (replacement tokens, extra tokens to skip, handled).
type postProcessor func(tokens []string, i int) ([]string, int, bool)

// postProcessors are applied in order during post-processing. First match wins.
var postProcessors = []postProcessor{
	postProcessMultiToken,
	postProcessGarbledILS,
	postProcessTurnGarbled,
	postProcessRunwayDesig,
	postProcessDegreesTurn,
}

// postProcessMultiToken handles table-driven multi-token replacements (longest match first).
func postProcessMultiToken(tokens []string, i int) ([]string, int, bool) {
	if matched, replacement, consumed := matchMultiToken(tokens[i:]); matched {
		return replacement, consumed - 1, true
	}
	return nil, 0, false
}

// postProcessGarbledILS handles garbled ILS letter spelling: "i" "l" + garbled "s".
// STT sometimes garbles the letter "S" to "fs", "es", "ess", etc.
// The exact "i l s" case is already handled by postProcessMultiToken above.
func postProcessGarbledILS(tokens []string, i int) ([]string, int, bool) {
	if tokens[i] == "i" && i+2 < len(tokens) && tokens[i+1] == "l" {
		t2 := tokens[i+2]
		if t2 != "s" && len(t2) >= 2 && len(t2) <= 3 && strings.HasSuffix(t2, "s") {
			return []string{"ils"}, 2, true
		}
	}
	return nil, 0, false
}

// postProcessTurnGarbled handles "turn [word] to N" where the word is garbage
// and "to" is garbled "two". e.g., "turn navigation to 7 0" → "turn heading 270"
func postProcessTurnGarbled(tokens []string, i int) ([]string, int, bool) {
	if tokens[i] != "turn" || i+3 >= len(tokens) || tokens[i+2] != "to" {
		return nil, 0, false
	}
	garbageWord := tokens[i+1]
	if garbageWord == "left" || garbageWord == "right" || garbageWord == "heading" {
		return nil, 0, false
	}
	digitCount := 0
	for j := i + 3; j < len(tokens) && IsNumber(tokens[j]); j++ {
		digitCount++
	}
	if digitCount < 1 {
		return nil, 0, false
	}
	var digitStr string
	for j := i + 3; j < i+3+digitCount; j++ {
		digitStr += tokens[j]
	}
	nextNum := ParseNumber(digitStr)
	if nextNum >= 10 && nextNum <= 99 {
		combined := 200 + nextNum
		if combined <= 360 {
			return []string{"turn", "heading", strconv.Itoa(combined)}, 2 + digitCount, true
		}
	}
	return nil, 0, false
}

// postProcessRunwayDesig handles runway designators: "13l" → "13" "left", etc.
// This handles cases where Whisper transcribes "one three left" as "13L".
func postProcessRunwayDesig(tokens []string, i int) ([]string, int, bool) {
	tok := tokens[i]
	if len(tok) < 2 {
		return nil, 0, false
	}
	lastChar := tok[len(tok)-1]
	numPart := tok[:len(tok)-1]
	if !IsNumber(numPart) {
		return nil, 0, false
	}
	switch lastChar {
	case 'l':
		return []string{numPart, "left"}, 0, true
	case 'r':
		return []string{numPart, "right"}, 0, true
	case 'c':
		return []string{numPart, "center"}, 0, true
	}
	return nil, 0, false
}

// postProcessDegreesTurn handles "<number> degrees [to the] left/right" without "turn".
// e.g., "20 degrees to the right" → "turn" "20" "degrees to the right".
// Returns "turn" + the current token (number); does NOT consume extra tokens.
func postProcessDegreesTurn(tokens []string, i int) ([]string, int, bool) {
	if !IsNumber(tokens[i]) {
		return nil, 0, false
	}
	num := ParseNumber(tokens[i])
	if num < 1 || num > 45 || i+1 >= len(tokens) || tokens[i+1] != "degrees" {
		return nil, 0, false
	}
	// Look ahead for "left" or "right" within the next few tokens
	for j := i + 2; j < len(tokens) && j < i+6; j++ {
		if tokens[j] == "left" || tokens[j] == "right" {
			return []string{"turn", tokens[i]}, 0, true
		}
	}
	return nil, 0, false
}

// postProcessNormalized handles multi-word STT errors and letter joining.
func postProcessNormalized(tokens []string) []string {
	result := make([]string, 0, len(tokens))
	skip := 0

	for i := range len(tokens) {
		if skip > 0 {
			skip--
			continue
		}

		handled := false
		for _, proc := range postProcessors {
			if replacement, extraSkip, ok := proc(tokens, i); ok {
				result = append(result, replacement...)
				skip = extraSkip
				handled = true
				break
			}
		}
		if !handled {
			result = append(result, tokens[i])
		}
	}

	return result
}

// IsFillerWord returns true if the word should be ignored during parsing.
func IsFillerWord(w string) bool {
	return fillerWords[strings.ToLower(w)]
}

// commandBoundaryKeywords are words that indicate the start of a new command context.
// These keywords should stop the slack mechanism from searching past them.
var commandBoundaryKeywords = map[string]bool{
	// Speed-related
	"speed": true, "slow": true, "reduce": true, "increase": true, "mach": true,
	// Altitude-related
	"maintain": true, "descend": true, "climb": true, "altitude": true,
	// Heading-related
	"heading": true, "turn": true, "left": true, "right": true,
	// Navigation
	"direct": true, "proceed": true,
	// Approach
	"cleared": true, "expect": true, "vectors": true, "approach": true,
	"intercept": true,
	// Other commands
	"contact": true, "squawk": true, "ident": true,
}

// IsCommandKeyword returns true if the word is a command keyword that indicates
// the start of a new command context. Used by the slack mechanism to avoid
// searching past command boundaries.
func IsCommandKeyword(w string) bool {
	return commandBoundaryKeywords[strings.ToLower(w)]
}

// IsDigit returns true if the string is a single digit (0-9).
func IsDigit(s string) bool {
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}

// ParseDigit converts a digit string to int. Returns -1 on error.
func ParseDigit(s string) int {
	if IsDigit(s) {
		return int(s[0] - '0')
	}
	return -1
}

// IsNumber returns true if the string is a sequence of digits.
func IsNumber(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseNumber converts a digit sequence to int. Returns -1 on error.
func ParseNumber(s string) int {
	if !IsNumber(s) {
		return -1
	}
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// splitTextNumber splits a word that has text followed by digits (or vice versa).
// Examples: "alaska8383" → ["alaska", "8383"], "8383alaska" → ["8383", "alaska"]
// Returns nil if the word doesn't need splitting.
func splitTextNumber(w string) []string {
	if len(w) < 2 {
		return nil
	}

	// Find the transition point between text and digits
	var textPart, numPart strings.Builder
	inDigits := w[0] >= '0' && w[0] <= '9'

	for _, c := range w {
		isDigit := c >= '0' && c <= '9'
		if isDigit == inDigits {
			if inDigits {
				numPart.WriteRune(c)
			} else {
				textPart.WriteRune(c)
			}
		} else {
			// Transition found
			if inDigits {
				// Was digits, now text
				if textPart.Len() > 0 || numPart.Len() > 0 {
					// Already have content, this is a second transition - don't split
					return nil
				}
				numPart.WriteRune(c)
			} else {
				// Was text, now digits - start collecting digits
				numPart.WriteRune(c)
			}
			inDigits = isDigit
		}
	}

	// Only split if we have both parts and each is meaningful
	t, n := textPart.String(), numPart.String()
	if len(t) >= 2 && len(n) >= 1 {
		return []string{t, n}
	}
	return nil
}

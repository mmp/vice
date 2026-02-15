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
	"won": "1", "wun": "1",
	"too":  "2",             // "to" is intentionally excluded - it's a common word
	"fore": "4",             // "for" is intentionally excluded - it's a common word
	"ate":  "8", "ait": "8", // Homophone for "eight"
	"oh":   "0", // Common way to say zero
	"zeri": "0", // Whisper STT transcription of "zero"
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

// natoCanonical maps each letter to its canonical NATO phonetic name.
// Used for fuzzy matching merged NATO letters.
var natoCanonical = map[string]string{
	"a": "alpha", "b": "bravo", "c": "charlie", "d": "delta", "e": "echo",
	"f": "foxtrot", "g": "golf", "h": "hotel", "i": "india", "j": "juliet",
	"k": "kilo", "l": "lima", "m": "mike", "n": "november", "o": "oscar",
	"p": "papa", "q": "quebec", "r": "romeo", "s": "sierra", "t": "tango",
	"u": "uniform", "v": "victor", "w": "whiskey", "x": "xray", "y": "yankee",
	"z": "zulu",
}

// trySplitMergedNATO attempts to split a word into two NATO phonetic letters.
// STT sometimes merges "echo whiskey" into "echowiski". This function detects
// such patterns using fuzzy matching and returns the split words.
// Returns nil if the word doesn't appear to be merged NATO letters.
func trySplitMergedNATO(word string) []string {
	word = strings.ToLower(word)
	if len(word) < 8 { // Minimum: two short NATO words merged (e.g., "echogolf" = 8)
		return nil
	}

	// Don't split if the word itself is already a NATO letter
	if _, ok := natoAlphabet[word]; ok {
		return nil
	}

	// Try each NATO letter as a potential prefix
	for _, nato1 := range natoCanonical {
		// Try different split points based on the NATO word length
		// Allow some flexibility for STT errors
		minSplit := max(len(nato1)-1, 3)           // Require at least 3 chars for prefix
		maxSplit := min(len(nato1)+1, len(word)-3) // Require at least 3 chars for suffix

		for splitAt := minSplit; splitAt <= maxSplit; splitAt++ {
			prefix := word[:splitAt]
			suffix := word[splitAt:]

			// Check if prefix fuzzy-matches the NATO word (high threshold)
			if JaroWinkler(prefix, nato1) < 0.85 {
				continue
			}

			// Check if suffix fuzzy-matches any NATO word (high threshold, min length)
			if len(suffix) < 3 {
				continue
			}
			for _, nato2 := range natoCanonical {
				if JaroWinkler(suffix, nato2) >= 0.80 {
					// Found a valid split
					return []string{nato1, nato2}
				}
			}
		}
	}
	return nil
}

// mergedCommandPrefixes are command words that commonly appear first in merged transcriptions.
var mergedCommandPrefixes = []string{"turn", "climb", "descend", "cleared", "expect", "fly"}

// canonicalSuffixes are command words that can be matched phonetically after a merged prefix.
// These are the actual command words that might be merged with a prefix.
var canonicalSuffixes = []string{"left", "right", "maintain", "direct", "ils", "heading"}

// garbledSuffixMappings maps specific garbled forms to their canonical command words.
// These are exact matches only - they should NOT be matched phonetically to avoid false positives.
// For example, "tor" should not match "der" even though they share metaphone "TR".
var garbledSuffixMappings = map[string]string{
	"dered": "direct", // garbled "direct" in "cleardered"
	"dred":  "direct", // garbled "direct"
	"red":   "direct", // garbled "direct" in "clerder" -> "cler" + "der" split fails, try "cleared" + "red"
	"der":   "direct", // truncated "direct"
	"drick": "direct", // garbled "direct" in "cleardrick"
}

// prefixSuffixCompatibility defines valid suffix words for each prefix.
// If a prefix is listed here, only the specified suffixes are allowed.
// This prevents false positives like "cleared" + "right" (which is not valid ATC).
var prefixSuffixCompatibility = map[string][]string{
	"cleared": {"direct", "ils"},  // "cleared direct" or "cleared ILS" are valid, not "cleared right"
	"expect":  {"heading", "ils"}, // "expect heading" or "expect ILS", not "expect direct"
}

// trySplitMergedCommand attempts to split a word into two command words.
// STT sometimes merges "turn right" into "turnwright". This function detects
// such patterns using phonetic matching and returns the split words.
// Returns nil if the word doesn't appear to be merged command words.
func trySplitMergedCommand(word string) []string {
	word = strings.ToLower(word)
	if len(word) < 7 { // Minimum: "turnleft" = 8, but allow some flexibility
		return nil
	}

	// Don't split if it's already a known command keyword
	if _, ok := commandKeywords[word]; ok {
		return nil
	}

	// Find the best match across all prefixes and split points
	var bestPrefix, bestSuffix string
	var bestScore float64

	// Try each prefix command word
	for _, prefix := range mergedCommandPrefixes {
		// Try different split points based on prefix length (allow ±2 for STT errors)
		minSplit := max(len(prefix)-2, 3)
		maxSplit := min(len(prefix)+2, len(word)-1)

		for splitAt := minSplit; splitAt <= maxSplit; splitAt++ {
			wordPrefix := word[:splitAt]
			wordSuffix := word[splitAt:]

			// Check if prefix part matches the command word (phonetic or JW >= 0.85)
			prefixScore := 0.0
			if PhoneticMatch(wordPrefix, prefix) {
				prefixScore = 1.0
			} else {
				jw := JaroWinkler(wordPrefix, prefix)
				if jw >= 0.85 {
					prefixScore = jw
				}
			}
			if prefixScore == 0 {
				continue
			}

			// Check if suffix part matches any known suffix
			if len(wordSuffix) < 2 {
				continue
			}
			// Get allowed suffixes for this prefix (if restricted)
			allowedSuffixes := prefixSuffixCompatibility[prefix]

			// First, check for exact match against garbled suffix mappings
			if canonical, ok := garbledSuffixMappings[wordSuffix]; ok {
				// Skip if this prefix has restrictions and this suffix isn't allowed
				if len(allowedSuffixes) == 0 || slices.Contains(allowedSuffixes, canonical) {
					totalScore := prefixScore + 2.0 // High score for exact garbled match
					if totalScore > bestScore {
						bestScore = totalScore
						bestPrefix = prefix
						bestSuffix = canonical
					}
				}
			}

			// Then, check for phonetic/fuzzy match against canonical suffixes only
			for _, canonical := range canonicalSuffixes {
				// Skip if this prefix has restrictions and this suffix isn't allowed
				if len(allowedSuffixes) > 0 && !slices.Contains(allowedSuffixes, canonical) {
					continue
				}
				suffixScore := scoreSuffixMatch(wordSuffix, canonical)
				if suffixScore > 0 {
					totalScore := prefixScore + suffixScore
					if totalScore > bestScore {
						bestScore = totalScore
						bestPrefix = prefix
						bestSuffix = canonical
					}
				}
			}
		}
	}

	if bestScore > 0 {
		return []string{bestPrefix, bestSuffix}
	}
	return nil
}

// scoreSuffixMatch returns a match score for suffix matching.
// Higher scores indicate better matches. Returns 0 for no match.
func scoreSuffixMatch(wordSuffix, target string) float64 {
	// Minimum suffix length to avoid false positives like "r" -> "right"
	if len(wordSuffix) < 2 {
		return 0
	}

	// Strategy 1: Exact phonetic match - highest score
	if PhoneticMatch(wordSuffix, target) {
		// Bonus for longer target (prefer "right" over "red" when both match phonetically)
		// Also add small bonus for Jaro-Winkler similarity as tiebreaker
		return 1.0 + float64(len(target))/10.0 + JaroWinkler(wordSuffix, target)/100.0
	}

	// Strategy 2: High Jaro-Winkler similarity
	jw := JaroWinkler(wordSuffix, target)
	if jw >= 0.80 {
		return jw
	}

	// Strategy 3: Metaphone prefix match - handles truncated suffixes
	// e.g., "der" (TR) matches "direct" (TRKT) because TR is prefix of TRKT
	// Require suffix length >= 3 to avoid false positives from very short
	// suffixes (e.g., "al" matching "ils" via AL→ALS prefix).
	suffixMeta, _ := DoubleMetaphone(wordSuffix)
	targetMeta, _ := DoubleMetaphone(target)
	if len(wordSuffix) >= 3 && len(suffixMeta) >= 2 && strings.HasPrefix(targetMeta, suffixMeta) {
		// Score based on how much of target's metaphone is covered
		return float64(len(suffixMeta)) / float64(len(targetMeta))
	}

	return 0
}

// phoneticCommandKeywords are command keywords that should be matched phonetically
// when exact lookup fails. Only high-value keywords are included to avoid false positives.
var phoneticCommandKeywords = []string{
	"heading", "descend", "climb", "maintain", "turn", "left", "right",
	"direct", "cleared", "contact", "approach", "intercept", "localizer",
	"expedite", "speed", "altitude", "runway", "reduce",
}

// phoneticCommandBlocklist prevents specific words from matching certain keywords.
// Key is the input word, value is the list of keywords it should NOT match.
var phoneticCommandBlocklist = map[string][]string{
	"continue":  {"maintain"}, // "continue your turn" is not "maintain"
	"continued": {"maintain"},
	"flight":    {"right", "left"}, // "flight 123" is not "right 123"
	"red":       {"right"},         // "red" in garbled phrases (e.g., "Red or Collins") is not "right"
	"redu":      {"right"},         // "redu-speed" is "reduce speed", not "right speed"
	"redo":      {"right"},         // "redo speed" is "reduce speed", not "right speed"
	"roto":      {"right"},         // "roto" is garbled airline name (Chronos), not "right"
	"towards":   {"reduce"},        // "contact towards" is not "reduce"
	"had":       {"heading"},       // "just had to" is not "heading"
	// "buddy" as part of a callsign should not match "expedite"
	"buddy": {"expedite"},
	// Speed-related words should match "speed" not "intercept" (suffix match on SPT)
	"rotospeed": {"intercept"}, // STT garble of "reduce speed"
	"speedo":    {"intercept"}, // STT garble of "speed"
	// NATO phonetic letters should not match command keywords
	"tango":   {"heading"},          // NATO letter T, not "heading"
	"juliet":  {"left"},             // NATO letter J, not "left"
	"charlie": {"climb", "cleared"}, // NATO letter C, not command keywords
	// Facility/location names should not match command keywords
	"barracuda": {"direct"},            // Miami position name, not "direct"
	"veracosta": {"cleared", "direct"}, // Garbled position name
	"mayr":      {"maintain"},          // Garbled word
	"argentina": {"maintain"},          // Airline name, not command
}

// tryPhoneticCommandMatch attempts to match a word phonetically against
// high-value command keywords. Returns the canonical keyword if matched.
func tryPhoneticCommandMatch(word string) string {
	if len(word) < 3 {
		return ""
	}
	wordLower := strings.ToLower(word)
	blocked := phoneticCommandBlocklist[wordLower]
	// Also check fuzzyMatchBlocklist from similarity.go
	if globalBlocked, ok := fuzzyMatchBlocklist[wordLower]; ok {
		blocked = append(blocked, globalBlocked...)
	}
	for _, kw := range phoneticCommandKeywords {
		// Skip if this word is blocked from matching this keyword
		isBlocked := false
		for _, b := range blocked {
			if b == kw {
				isBlocked = true
				break
			}
		}
		if isBlocked {
			continue
		}
		if PhoneticMatch(word, kw) {
			return kw
		}
	}
	return ""
}

// commandKeywords maps spoken command words to normalized forms.
var commandKeywords = map[string]string{
	// Altitude
	"descend":    "descend",
	"descending": "descend",
	"setup":      "descend",
	"climb":      "climb",
	"climbing":   "climb",
	"climin":     "climb",
	"club":       "climb",
	"con":        "climb",
	"maintain":   "maintain",
	"maintained": "maintain",
	"altitude":   "altitude",
	"thousand":   "thousand",
	"hundred":    "hundred",
	"flight":     "flight",
	"fight":      "flight",
	"level":      "level",
	"expedite":   "expedite",

	// Heading
	"heading": "heading",
	"turn":    "turn",
	"turning": "turn",
	"turned":  "turn",
	"left":    "left",
	"right":   "right",
	"degrees": "degrees",
	"fly":     "fly",
	"present": "present",

	// Speed
	"speed":    "speed",
	"reduce":   "reduce",
	"increase": "increase",
	"slow":     "slow",
	"slowest":  "slowest",
	"minimum":  "minimum",
	"maximum":  "maximum",
	"forward":  "forward",
	"knots":    "knots",
	"normal":   "normal",

	// Navigation
	"direct":   "direct",
	"directed": "direct",
	"colonel":  "kernel", // English homophones: both pronounced /ˈkɜːrnəl/
	"proceed":  "proceed",
	"cross":    "cross",
	"across":   "cross",
	"depart":   "depart",
	"hold":     "hold",
	"land":     "land",
	"short":    "short", // For "hold short" LAHSO commands
	"via":      "via",
	"sid":      "sid",

	// Hold-related
	"radial":    "radial",
	"bearing":   "bearing",
	"inbound":   "inbound",
	"legs":      "legs",
	"minute":    "minute",
	"turns":     "turns",
	"published": "published",

	// Compass directions (for hold instructions)
	"north":     "north",
	"south":     "south",
	"east":      "east",
	"west":      "west",
	"northeast": "northeast",
	"northwest": "northwest",
	"southeast": "southeast",
	"southwest": "southwest",

	// Approach
	"cleared":     "cleared",
	"expect":      "expect",
	"vectors":     "vectors",
	"approach":    "approach",
	"cancel":      "cancel",
	"localizer":   "localizer",
	"localize":    "localizer", // STT drops trailing 'r'
	"intercept":   "intercept",
	"intercepted": "intercept",
	"clearance":   "clearance",
	"visual":      "visual",
	"ils":         "ils",
	"rnav":        "rnav",
	"vor":         "vor",
	"runway":      "runway",

	// Transponder
	"squawk":      "squawk",
	"transponder": "transponder",
	"ident":       "ident",
	"standby":     "standby",
	"mode":        "mode",

	// Handoff
	"contact":   "contact",
	"cansec":    "contact",
	"tower":     "tower",
	"tarot":     "tower",
	"frequency": "frequency",
	"departure": "departure",
	"center":    "center",

	// VFR/Misc
	"go":         "go",
	"ahead":      "ahead",
	"radar":      "radar",
	"services":   "services",
	"terminated": "terminated",
	"resume":     "resume",
	"own":        "own",
	"navigation": "navigation",
	"vfr":        "vfr",

	// Disregard
	"disregard": "disregard",
	"negative":  "negative",

	// Expected clearance (to be ignored in hold instructions)
	"further": "further",

	// Then sequencing
	"then": "then",
}

// phraseExpansions maps single STT words to multiple normalized words.
var phraseExpansions = map[string][]string{
	"flighting":  {"fly", "heading"},
	"centimeter": {"descend", "maintain"},
	"fl":         {"flight", "level"},
}

// multiTokenReplacements maps sequences of tokens (space-joined) to replacements.
var multiTokenReplacements = map[string][]string{
	"i l s":       {"ils"},
	"r nav":       {"rnav"},
	"fly level":   {"flight", "level"},
	"time riding": {"turn", "right"},
}

// matchMultiToken tries to match tokens against multiTokenReplacements.
// Returns (matched, replacement, tokensConsumed).
func matchMultiToken(tokens []string) (bool, []string, int) {
	// Try longest matches first (3 tokens, then 2)
	for length := min(3, len(tokens)); length >= 2; length-- {
		key := strings.Join(tokens[:length], " ")
		if replacement, ok := multiTokenReplacements[key]; ok {
			return true, replacement, length
		}
	}
	return false, nil, 0
}

// localizerPrefixes contains prefixes that indicate "intercept localizer" when
// combined with "lok" or "lawk" in the word.
var localizerPrefixes = []string{"zap", "zop", "za"}

// isLocalizerPattern checks if a word is a garbled "intercept localizer".
// STT often produces words like "zapulokwizer" or "zapulawkwizer".
func isLocalizerPattern(w string) bool {
	if !strings.Contains(w, "lok") && !strings.Contains(w, "lawk") {
		return false
	}
	for _, prefix := range localizerPrefixes {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// fillerWords are words to ignore during parsing.
var fillerWords = map[string]bool{
	"and": true, "the": true, "a": true, "an": true,
	"uh": true, "um": true, "uhh": true, "umm": true,
	"please": true, "thanks": true, "thank": true, "you": true,
	"good": true, "day": true, "morning": true, "afternoon": true, "evening": true,
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

// NormalizeTranscript normalizes a raw STT transcript for parsing.
// Handles phonetic corrections and cleanup. Disregard handling is done
// at a higher level after callsign matching.
func NormalizeTranscript(transcript string) []string {
	// Convert to lowercase and split
	transcript = strings.ToLower(strings.TrimSpace(transcript))
	if transcript == "" {
		return nil
	}

	// Replace hyphens with spaces so "1-1-thousand" becomes "1 1 thousand"
	transcript = strings.ReplaceAll(transcript, "-", " ")

	words := strings.Fields(transcript)
	if len(words) == 0 {
		return nil
	}

	// Normalize each word
	result := make([]string, 0, len(words))
	skipCount := 0
	for i := 0; i < len(words); i++ {
		if skipCount > 0 {
			skipCount--
			continue
		}

		w := CleanWord(words[i])
		if w == "" {
			continue
		}

		// Check for multi-token patterns on raw words BEFORE phonetic matching
		// This catches patterns like "time riding" → "turn right" before "riding" gets
		// normalized to "heading" via phonetic match
		if i+1 < len(words) {
			rawNext := CleanWord(words[i+1])
			key := w + " " + rawNext
			if replacement, ok := multiTokenReplacements[key]; ok {
				result = append(result, replacement...)
				skipCount = 1
				continue
			}
		}

		// Split concatenated callsigns like "alaska8383" → ["alaska", "8383"]
		// This handles STT transcriptions that omit the space between airline and flight number
		if parts := splitTextNumber(w); len(parts) > 1 {
			for _, part := range parts {
				if part != "" {
					result = append(result, part)
				}
			}
			continue
		}

		// Try digit word normalization
		if digit, ok := digitWords[w]; ok {
			result = append(result, digit)
			continue
		}

		// Try number word normalization (twenty, thirty, etc.)
		if num, ok := numberWords[w]; ok {
			result = append(result, num)
			continue
		}

		// NOTE: NATO alphabet conversion is NOT done here because words like
		// "delta" could be airline names (Delta Air Lines). NATO conversion is
		// deferred to scoreGACallsign() and scoreFlightNumberMatch() where the
		// context makes it clear we're building a callsign from phonetics.

		// Try command keyword normalization (single word → single word)
		if norm, ok := commandKeywords[w]; ok {
			result = append(result, norm)
			continue
		}

		// Try phrase expansions (single word → multiple words)
		// Note: Check this BEFORE phonetic matching so exact table matches take priority
		if expansion, ok := phraseExpansions[w]; ok {
			result = append(result, expansion...)
			continue
		}

		// Try to split merged NATO phonetic letters (e.g., "echowiski" → "echo whiskey")
		if natoSplit := trySplitMergedNATO(w); natoSplit != nil {
			result = append(result, natoSplit...)
			continue
		}

		// Try to split merged command words (e.g., "turnwright" → "turn right")
		// Note: Check BEFORE phonetic matching since long merged words may partially
		// match a single keyword but should actually be split into multiple words
		if cmdSplit := trySplitMergedCommand(w); cmdSplit != nil {
			result = append(result, cmdSplit...)
			continue
		}

		// Try phonetic matching for command keywords (e.g., "hitting" → "heading")
		if phoneticMatch := tryPhoneticCommandMatch(w); phoneticMatch != "" {
			result = append(result, phoneticMatch)
			continue
		}

		// Check for "intercept localizer" pattern: words containing "lok" or "lawk"
		// with certain prefixes (e.g., "zapulokwizer", "zapulawkwizer")
		if isLocalizerPattern(w) {
			result = append(result, "intercept", "localizer")
			continue
		}

		// Handle "or" as STT noise in various contexts.
		if w == "or" && len(result) > 0 && i+1 < len(words) {
			prev := result[len(result)-1]
			nextWord := CleanWord(words[i+1])

			// Skip "or" between digits (e.g., "two nine or zero" for "two niner zero")
			prevIsDigit := IsNumber(prev)
			_, nextIsDigitWord := digitWords[nextWord]
			nextIsDigit := IsNumber(nextWord) || nextIsDigitWord
			if prevIsDigit && nextIsDigit {
				// Special case: "9 or 1000" means "niner thousand" - convert 1000 to thousand
				if nextWord == "1000" {
					words[i+1] = "thousand"
				}
				continue // Skip "or" between digits
			}

			// Skip "or" between "turn" and "left"/"right" (STT transcribes pause as "or")
			if prev == "turn" && (nextWord == "left" || nextWord == "right" ||
				PhoneticMatch(nextWord, "left") || PhoneticMatch(nextWord, "right")) {
				continue // Skip "or" between turn and direction
			}
		}

		// Handle "and" between digits: STT mishears "one" as "and"
		// e.g., "two and zero" means "two one zero" (210)
		// But "two nine and zero" should be "290" (and is filler, not replacing one)
		if w == "and" && len(result) > 0 && i+1 < len(words) {
			prev := result[len(result)-1]
			nextWord := CleanWord(words[i+1])

			prevIsDigit := IsNumber(prev)
			_, nextIsDigitWord := digitWords[nextWord]
			nextIsDigit := IsNumber(nextWord) || nextIsDigitWord
			if prevIsDigit && nextIsDigit {
				// Check if we're in a multi-digit sequence (prev-prev is also a digit)
				// If so, skip "and" (it's filler). Otherwise convert to "1".
				if len(result) >= 2 && IsNumber(result[len(result)-2]) {
					continue // Skip "and" in multi-digit sequence like "two nine and zero"
				}
				result = append(result, "1") // "and" → "1" in "two and zero"
				continue
			}
		}

		// Keep as-is
		result = append(result, w)
	}

	// Post-process: combine tens+units (e.g., "30 2" → "32"), join letter sequences, fix multi-word errors
	result = combineTensAndUnits(result)
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

// postProcessNormalized handles multi-word STT errors and letter joining.
func postProcessNormalized(tokens []string) []string {
	result := make([]string, 0, len(tokens))
	skip := 0

	for i := range len(tokens) {
		if skip > 0 {
			skip--
			continue
		}

		// Try table-driven multi-token replacements (longest match first)
		if matched, replacement, consumed := matchMultiToken(tokens[i:]); matched {
			result = append(result, replacement...)
			skip = consumed - 1 // -1 because loop will advance by 1
			continue
		}

		// Handle "turn [word] to N" where the word is garbage and "to" is garbled "two".
		// e.g., "turn navigation to 7 0" → "turn heading 270"
		if tokens[i] == "turn" && i+3 < len(tokens) && tokens[i+2] == "to" {
			garbageWord := tokens[i+1]
			if garbageWord != "left" && garbageWord != "right" && garbageWord != "heading" {
				digitCount := 0
				for j := i + 3; j < len(tokens) && IsNumber(tokens[j]); j++ {
					digitCount++
				}
				if digitCount >= 1 {
					var digitStr string
					for j := i + 3; j < i+3+digitCount; j++ {
						digitStr += tokens[j]
					}
					nextNum := ParseNumber(digitStr)
					if nextNum >= 10 && nextNum <= 99 {
						combined := 200 + nextNum
						if combined <= 360 {
							result = append(result, "turn", "heading", strconv.Itoa(combined))
							skip = 2 + digitCount
							continue
						}
					}
				}
			}
		}

		// Handle runway designators: "13l" → "13" "left", "22r" → "22" "right", "9c" → "9" "center"
		// This handles cases where Whisper transcribes "one three left" as "13L"
		if len(tokens[i]) >= 2 {
			lastChar := tokens[i][len(tokens[i])-1]
			numPart := tokens[i][:len(tokens[i])-1]
			if IsNumber(numPart) && (lastChar == 'l' || lastChar == 'r' || lastChar == 'c') {
				result = append(result, numPart)
				switch lastChar {
				case 'l':
					result = append(result, "left")
				case 'r':
					result = append(result, "right")
				case 'c':
					result = append(result, "center")
				}
				continue
			}
		}

		// Handle "<number> degrees [to the] left/right" pattern without "turn" keyword.
		// e.g., "20 degrees to the right" → "turn 20 degrees to the right"
		// Only applies when number is a valid degree value (1-45) and followed by "degrees".
		if IsNumber(tokens[i]) {
			num := ParseNumber(tokens[i])
			if num >= 1 && num <= 45 && i+1 < len(tokens) && tokens[i+1] == "degrees" {
				// Look ahead for "left" or "right" within the next few tokens
				hasDirection := false
				for j := i + 2; j < len(tokens) && j < i+6; j++ {
					if tokens[j] == "left" || tokens[j] == "right" {
						hasDirection = true
						break
					}
				}
				if hasDirection {
					result = append(result, "turn")
					// Continue to add the number below
				}
			}
		}

		// Default: keep the token as-is
		result = append(result, tokens[i])
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
	"speed": true, "slow": true, "reduce": true, "increase": true,
	// Altitude-related
	"maintain": true, "descend": true, "climb": true, "altitude": true,
	// Heading-related
	"heading": true, "turn": true,
	// Navigation
	"direct": true, "proceed": true,
	// Approach
	"cleared": true, "expect": true, "vectors": true, "approach": true,
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

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
	// Common STT errors
	"won": "1", "want": "1", "wun": "1",
	"too": "2", "tu": "2", // "to" is intentionally excluded - it's a common word
	"free": "3", "tee": "3",
	"fore":  "4", // "for" is intentionally excluded - it's a common word
	"fiv":   "5",
	"sicks": "6", "seeks": "6", "sex": "6",
	"ate": "8", "ait": "8", "eat": "8", "ada": "8",
	"oh": "0",
	// Ordinals sometimes transcribed instead of cardinals
	"first": "1", "second": "2", "third": "3", "fourth": "4", "fifth": "5",
	"sixth": "6", "seventh": "7", "eighth": "8", "ninth": "9",
	// Plural digits (sometimes used in readbacks)
	"fives": "5", "nines": "9",
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
	"alpha": "a", "alfa": "a", "alfor": "a",
	"bravo": "b", "brahvo": "b",
	"charlie": "c", "charlee": "c",
	"delta": "d", "deltta": "d",
	"echo": "e", "eko": "e",
	"foxtrot": "f", "foxrot": "f",
	"golf": "g", "gulf": "g",
	"hotel": "h", "hotell": "h",
	"india": "i", "indea": "i",
	"juliet": "j", "juliett": "j", "juliette": "j",
	"kilo": "k", "keelo": "k",
	"lima": "l", "leema": "l",
	"mike": "m", "mic": "m",
	"november": "n", "novemba": "n",
	"oscar": "o", "oskar": "o",
	"papa": "p", "pahpah": "p",
	"quebec": "q", "kebeck": "q", "kebec": "q",
	"romeo": "r", "romio": "r",
	"sierra": "s", "seeara": "s", "seara": "s",
	"tango": "t", "tanggo": "t",
	"uniform": "u", "youniform": "u",
	"victor": "v", "vikter": "v",
	"whiskey": "w", "whisky": "w",
	"xray": "x", "x-ray": "x", "exray": "x",
	"yankee": "y", "yankey": "y",
	"zulu": "z", "zoolu": "z",
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
	suffixMeta, _ := DoubleMetaphone(wordSuffix)
	targetMeta, _ := DoubleMetaphone(target)
	if len(suffixMeta) >= 2 && strings.HasPrefix(targetMeta, suffixMeta) {
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
	"towards":   {"reduce"},        // "contact towards" is not "reduce"
	// NATO phonetic letters should not match command keywords
	"tango":   {"heading"},          // NATO letter T, not "heading"
	"juliet":  {"left"},             // NATO letter J, not "left"
	"charlie": {"climb", "cleared"}, // NATO letter C, not command keywords
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
	"doesnt":     "descend",
	"send":       "descend", // "send a maintain" = "descend and maintain"
	"climb":      "climb",
	"climin":     "climb",
	"klimin":     "climb",
	"clomman":    "climb",
	"clementine": "climb",
	"con":        "climb",
	"maintain":   "maintain",
	"maintained": "maintain",
	"altitude":   "altitude",
	"thousand":   "thousand",
	"month":      "thousand", // STT error: "thousand" misheard as "month"
	"hundred":    "hundred",
	"flight":     "flight",
	"fight":      "flight",
	"level":      "level",
	"expedite":   "expedite",

	// Heading
	"heading":  "heading",
	"eating":   "heading", // STT error: "heading" misheard as "eating" (phonetically similar)
	"atting":   "heading", // STT error: "heading" misheard as "atting" (phonetically similar)
	"turn":     "turn",
	"lefthand": "left", // STT error: "left" with extra "hand" suffix
	"turning":  "turn", // STT captures continuous tense
	"turned":   "turn", // Past tense variant
	"left":     "left",
	"right":    "right",
	"degrees":  "degrees",
	"fly":      "fly",
	"present":  "present",

	// Speed
	"speed":    "speed",
	"dsp":      "speed",
	"reduce":   "reduce",
	"root":     "reduce",
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
	"directed": "direct", // Past tense variant
	"rig":      "direct", // STT error: "direct" garbled as "rig"
	"drag":     "direct", // STT error: "direct" garbled as "drag"
	"colonel":  "kernel", // English homophones: both pronounced /ˈkɜːrnəl/
	"proceed":  "proceed",
	"cross":    "cross",
	"depart":   "depart",
	"hold":     "hold",
	"via":      "via",
	"by":       "via",
	"sid":      "sid",
	"cid":      "sid",

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
	"cleared":   "cleared",
	"cliud":     "cleared", // STT error: garbled "cleared"
	"expect":    "expect",
	"spectat":   "expect", // STT error: "expect" garbled
	"select":    "expect", // "select the ILS" = "expect the ILS"
	"vectors":   "vectors",
	"approach":  "approach",
	"cancel":    "cancel",
	"localizer": "localizer",
	"localize":  "localizer", // STT drops trailing 'r'
	"intercept": "intercept",
	"nusselt":   "intercept",
	"clearance": "clearance",
	"visual":    "visual",
	"ils":       "ils",
	"dallas":    "ils",
	"alice":     "ils",
	"als":       "ils",
	"les":       "ils", // STT error: dropped leading sound
	"rnav":      "rnav",
	"arnavie":   "rnav", // STT error: "rnav" garbled with extra syllables
	"vor":       "vor",
	"runway":    "runway",
	"romn":      "runway", // STT error: garbled "runway"
	"renoya":    "runway", // STT error: garbled "runway"

	// Transponder
	"squawk":      "squawk",
	"transponder": "transponder",
	"ident":       "ident",
	"standby":     "standby",
	"mode":        "mode",

	// Handoff
	"contact":   "contact",
	"tower":     "tower",
	"tar":       "tower",
	"terror":    "tower",
	"her":       "tower", // STT error: "tower" misheard as "her"
	"frequency": "frequency",
	"departure": "departure",
	"center":    "center",

	// VFR/Misc
	"go":         "go",
	"ahead":      "ahead",
	"radar":      "radar",
	"redder":     "radar", // STT error: "radar" garbled as "redder"
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
// These are common STT errors where words get merged together.
var phraseExpansions = map[string][]string{
	"flighting":        {"fly", "heading"},      // "fly heading" -> "flighting"
	"disundermaintain": {"descend", "maintain"}, // "descend and maintain" -> "disundermaintain"
	"climbington":      {"climb", "maintain"},   // "climb and maintain" -> "climbington"
	"thunbright":       {"turn", "right"},       // STT error: "turn right" merged together
	// Note: "cleared direct" merged forms are handled by trySplitMergedCommand
	"expectilis": {"expect", "ils"},   // STT error: "expect ILS" merged
	"fl":         {"flight", "level"}, // STT converts "flight level" to "FL"
}

// multiTokenReplacements maps sequences of tokens (space-joined) to replacements.
// These handle STT errors that span multiple tokens.
var multiTokenReplacements = map[string][]string{
	"december 18": {"descend", "maintain"}, // STT error: "descend and maintain"
	"i l s":       {"ils"},                 // Spelled out ILS
	"r nav":       {"rnav"},                // R-NAV after hyphen removal
	"fly level":   {"flight", "level"},     // STT error: "flight level" misheard as "fly level"
	"eddie had":   {"etihad"},              // STT error: "Etihad" misheard as "eddie had"
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
	"having": true, // Prevents "having" from fuzzy matching "heading" (Jaro-Winkler 0.86)
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
	for i := 0; i < len(words); i++ {
		w := CleanWord(words[i])
		if w == "" {
			continue
		}

		// Handle garbled "niner" transcriptions like "9r,000" -> "9r000" -> "9000"
		// Whisper sometimes transcribes "niner" as "9r" when followed by digits.
		w = fixGarbledNiner(w)

		// Handle numbers with trailing 's' like "4s" → "40"
		// STT sometimes transcribes "four zero" or "forty" as "4s"
		w = fixTrailingS(w)

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

		// Keep as-is
		result = append(result, w)
	}

	// Post-process: join letter sequences and fix common multi-word errors
	result = postProcessNormalized(result)

	return result
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

		// Handle "l s" → "ils" (2 letters, missing "i")
		// Context-dependent: only when followed by "runway" or a number
		if tokens[i] == "l" && i+1 < len(tokens) && tokens[i+1] == "s" {
			if i+2 < len(tokens) {
				next := tokens[i+2]
				if next == "runway" || IsNumber(next) {
					result = append(result, "ils")
					skip = 1 // Skip the "s"
					continue
				}
			}
		}

		// Handle "10XXX" headings where "10" is a garbled transcription of "heading"
		// e.g., "10140" → ["heading", "140"], "10270" → ["heading", "270"]
		if len(tokens[i]) == 5 && tokens[i][:2] == "10" && IsNumber(tokens[i]) {
			possibleHeading := tokens[i][2:]
			hdg := ParseNumber(possibleHeading)
			if hdg >= 1 && hdg <= 360 {
				result = append(result, "heading", possibleHeading)
				continue
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

		// Handle abnormally large numbers that are likely altitude STT errors.
		// e.g., "144000" → "14000" (doubled 4), "120000" → "12000" (extra zero)
		// These occur when STT doubles a digit or adds extra zeros.
		if correctedNum := fixLargeNumber(tokens[i]); correctedNum != "" {
			result = append(result, correctedNum)
			continue
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

		// Handle "for [a] thousand/hundred" → "4 thousand/hundred"
		// "for" sounds like "four" in altitude contexts
		if tokens[i] == "for" && i+1 < len(tokens) {
			nextIdx := i + 1
			// Skip optional "a" between "for" and "thousand/hundred"
			if tokens[nextIdx] == "a" && nextIdx+1 < len(tokens) {
				nextIdx++
			}
			if tokens[nextIdx] == "thousand" || tokens[nextIdx] == "hundred" {
				result = append(result, "4")
				continue
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

// fixLargeNumber corrects abnormally large numbers that are likely STT errors.
// e.g., "144000" → "14000" (doubled digit), "120000" → "12000" (extra zero)
// Returns the corrected number string, or empty string if no correction applies.
func fixLargeNumber(s string) string {
	if !IsNumber(s) {
		return ""
	}
	n := ParseNumber(s)
	// Numbers > 60000 are unlikely altitudes (max typical is FL600 = 60000 ft)
	if n < 100000 || n > 1000000 {
		return ""
	}

	// First try: remove spurious '0' after first digit (e.g., "104000" → "14000")
	// This handles STT errors where "fourteen" becomes "one oh four" or similar.
	// Check this first because it's more likely to be the intended altitude.
	if len(s) >= 3 && s[1] == '0' && s[2] != '0' {
		corrected := string(s[0]) + s[2:]
		if cn := ParseNumber(corrected); cn >= 1000 && cn <= 60000 {
			return corrected
		}
	}

	// Second try: remove a doubled digit (e.g., "144000" → "14000")
	for j := 1; j < len(s); j++ {
		if s[j] == s[j-1] {
			// Try removing the duplicate
			corrected := s[:j] + s[j+1:]
			if cn := ParseNumber(corrected); cn >= 1000 && cn <= 60000 {
				return corrected
			}
		}
	}

	// Third try: remove trailing zero (e.g., "120000" → "12000")
	if s[len(s)-1] == '0' {
		corrected := n / 10
		if corrected >= 1000 && corrected <= 60000 {
			return strconv.Itoa(corrected)
		}
	}

	return ""
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

// fixGarbledNiner fixes STT transcription errors where "niner" is garbled.
// This handles patterns like:
//   - "9r" -> "9" (just the garbled niner)
//   - "9r000" -> "9000" (niner followed by more digits)
//   - "99er" -> "9" (STT transcribed "niner" with doubled digit)
func fixGarbledNiner(w string) string {
	if len(w) < 2 {
		return w
	}

	// Pattern 1: "99er" -> "9" (doubled digit + "er" from "niner")
	// STT sometimes transcribes "niner" as "99er" where "nine" becomes "99"
	if len(w) == 4 && w[0] == w[1] && w[0] >= '0' && w[0] <= '9' && w[2:] == "er" {
		return string(w[0]) // "99er" -> "9"
	}

	// Pattern 2: single digit followed by 'r' followed by optional digits
	// e.g., "9r", "9r000"
	if w[0] >= '0' && w[0] <= '9' && w[1] == 'r' {
		// Remove the 'r' - keep the leading digit and any trailing digits
		if len(w) == 2 {
			return string(w[0]) // "9r" -> "9"
		}
		// Check that everything after 'r' is digits
		allDigits := true
		for j := 2; j < len(w); j++ {
			if w[j] < '0' || w[j] > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return string(w[0]) + w[2:] // "9r000" -> "9000"
		}
	}
	return w
}

// fixTrailingS handles STT transcription errors where numbers are followed by 's'.
// For single-digit numbers: "4s" likely means "40" (four zero / forty).
// For multi-digit numbers: "23s" → "23" (just strip the noise 's').
// This handles patterns like "2s" -> "20", "4s" -> "40", "23s" -> "23", etc.
func fixTrailingS(w string) string {
	if len(w) < 2 || w[len(w)-1] != 's' {
		return w
	}

	// Check if everything before 's' is digits
	base := w[:len(w)-1]
	allDigits := true
	for _, c := range base {
		if c < '0' || c > '9' {
			allDigits = false
			break
		}
	}
	if !allDigits {
		return w
	}

	// Single digit + 's': "4s" -> "40" (STT transcribes "forty" as "4s")
	if len(base) == 1 {
		return base + "0"
	}

	// Multi-digit + 's': "23s" -> "23" (just strip the noise 's')
	return base
}

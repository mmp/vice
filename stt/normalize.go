package stt

import (
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
	"ate": "8", "ait": "8", "eat": "8",
	"oh": "0",
	// Ordinals sometimes transcribed instead of cardinals
	"first": "1", "second": "2", "third": "3", "fourth": "4", "fifth": "5",
	"sixth": "6", "seventh": "7", "eighth": "8", "ninth": "9",
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
	"thirty":    "30",
	"forty":     "40",
	"fifty":     "50",
	"sixty":     "60",
	"seventy":   "70",
	"eighty":    "80",
	"ninety":    "90",
	"hundred":   "100",
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

// commandKeywords maps spoken command words to normalized forms.
var commandKeywords = map[string]string{
	// Altitude
	"descend":    "descend",
	"descended":  "descend",
	"descending": "descend",
	"descendant": "descend", // STT error
	"descent":    "descend", // STT error
	"climb":      "climb",
	"climbed":    "climb",
	"climbing":   "climb",
	"climin":     "climb", // STT error: "climb and" -> "climin"
	"clementine": "climb", // STT error: "climb and maintain" -> "clementine"
	"con":        "climb", // STT error: "climb" -> "con" (not "contact")
	"maintain":   "maintain",
	"altitude":   "altitude",
	"thousand":   "thousand",
	"hundred":    "hundred",
	"flight":     "flight",
	"fight":      "flight", // STT error: "flight" often transcribed as "fight"
	"level":      "level",
	"expedite":   "expedite",

	// Heading
	"heading":  "heading",
	"hitting":  "heading", // STT error
	"turn":     "turn",
	"left":     "left",
	"lefting":  "left", // STT error
	"right":    "right",
	"righting": "right", // STT error
	"degrees":  "degrees",
	"degree":   "degrees",
	"fly":      "fly",
	"present":  "present",

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

	// Navigation
	"direct":  "direct",
	"proceed": "proceed",
	"cross":   "cross",
	"depart":  "depart",
	"hold":    "hold",

	// Approach
	"cleared":   "cleared",
	"expect":    "expect",
	"vectors":   "vectors",
	"approach":  "approach",
	"localizer": "localizer",
	"intercept": "intercept",
	"cancel":    "cancel",
	"clearance": "clearance",
	"visual":    "visual",
	"ils":       "ils",
	"ios":       "ils", // STT error
	"outlast":   "ils", // STT error
	"eyeless":   "ils", // STT error
	"dalas":     "ils", // STT error
	"dallas":    "ils", // STT error
	"rnav":      "rnav",
	"vor":       "vor",
	"runway":    "runway",

	// Transponder
	"squawk":      "squawk",
	"transponder": "transponder",
	"ident":       "ident",
	"standby":     "standby",
	"mode":        "mode",

	// Handoff
	"contact":   "contact",
	"konnek":    "contact", // STT error
	"konek":     "contact", // STT error
	"kannak":    "contact", // STT error
	"connector": "contact", // STT error
	"tower":     "tower",
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
	"disregard":  "disregard",
	"correction": "disregard",
	"negative":   "negative",

	// Then sequencing
	"then": "then",
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

		// Try command keyword normalization
		if norm, ok := commandKeywords[w]; ok {
			result = append(result, norm)
			continue
		}

		// Special case: "flighting" is commonly transcribed instead of "fly heading"
		if w == "flighting" {
			result = append(result, "fly", "heading")
			continue
		}

		// Handle "or" as STT misrecognition of "niner" when between digits.
		// STT sometimes transcribes "niner" as "nine or", so "two nine or zero"
		// becomes "2 9 or 0" instead of "2 9 0". Skip "or" when it appears
		// between digit-like tokens.
		// Also handle "niner thousand" -> "9 or 1000" where "1000" is really "thousand".
		if w == "or" && len(result) > 0 && i+1 < len(words) {
			prevIsDigit := IsNumber(result[len(result)-1])
			nextWord := CleanWord(words[i+1])
			_, nextIsDigitWord := digitWords[nextWord]
			nextIsDigit := IsNumber(nextWord) || nextIsDigitWord
			if prevIsDigit && nextIsDigit {
				// Special case: "9 or 1000" means "niner thousand" - convert 1000 to thousand
				if nextWord == "1000" {
					words[i+1] = "thousand"
				}
				continue // Skip "or" between digits
			}
		}

		// Keep as-is
		result = append(result, w)
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

// fixGarbledNiner fixes STT transcription errors where "niner" is garbled as "9r".
// For example, "9r,000" -> "9r000" after CleanWord, which should become "9000".
// This handles patterns like:
//   - "9r" -> "9" (just the garbled niner)
//   - "9r000" -> "9000" (niner followed by more digits)
func fixGarbledNiner(w string) string {
	if len(w) < 2 {
		return w
	}

	// Look for pattern: single digit followed by 'r' followed by optional digits
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

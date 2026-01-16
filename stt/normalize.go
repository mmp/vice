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

// commandKeywords maps spoken command words to normalized forms.
var commandKeywords = map[string]string{
	// Altitude
	"descend":    "descend",
	"descended":  "descend",
	"descending": "descend",
	"climb":      "climb",
	"climbed":    "climb",
	"climbing":   "climb",
	"maintain":   "maintain",
	"altitude":   "altitude",
	"thousand":   "thousand",
	"hundred":    "hundred",
	"flight":     "flight",
	"level":      "level",
	"expedite":   "expedite",

	// Heading
	"heading": "heading",
	"turn":    "turn",
	"left":    "left",
	"right":   "right",
	"degrees": "degrees",
	"degree":  "degrees",
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

	// Navigation
	"direct":  "direct",
	"proceed": "proceed",
	"cross":   "cross",
	"depart":  "depart",

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

		// Try NATO alphabet
		if letter, ok := natoAlphabet[w]; ok {
			result = append(result, letter)
			continue
		}

		// Try command keyword normalization
		if norm, ok := commandKeywords[w]; ok {
			result = append(result, norm)
			continue
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

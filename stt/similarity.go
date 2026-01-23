// Package stt provides a local algorithmic speech-to-command parser
// for ATC transcripts, replacing the LLM-based approach with fast fuzzy matching.
package stt

import (
	"strings"
	"unicode"
)

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

// PhoneticMatch returns true if two words are phonetically similar.
func PhoneticMatch(w1, w2 string) bool {
	p1, a1 := DoubleMetaphone(w1)
	p2, a2 := DoubleMetaphone(w2)

	// Exact metaphone match
	if p1 == p2 || p1 == a2 || a1 == p2 || (a1 != "" && a1 == a2) {
		return true
	}

	// Conservative extension: check if one code is a suffix of the other
	// This handles STT errors that drop leading sounds (e.g., "laser" for "localizer")
	// Only apply when the shorter code is at least 3 chars (to avoid false positives)
	if len(p1) >= 3 && len(p2) >= 3 {
		if strings.HasSuffix(p1, p2) || strings.HasSuffix(p2, p1) {
			return true
		}
		// Also check if they share a common suffix of 3+ characters
		minLen := min(len(p1), len(p2))
		for suffixLen := minLen; suffixLen >= 3; suffixLen-- {
			if p1[len(p1)-suffixLen:] == p2[len(p2)-suffixLen:] {
				return true
			}
		}
	}

	return false
}

// fuzzyMatchBlocklist contains pairs of words that should NOT fuzzy-match.
// These are known false positives where similar-looking words have completely
// different meanings in ATC context.
var fuzzyMatchBlocklist = map[string][]string{
	"intercept": {"increase"},           // "intercept localizer" vs "increase speed"
	"increase":  {"intercept", "cross"}, // "increase speed" vs "cross fix"
	"cross":     {"increase"},
	"see":       {"speed"},      // "see ya" vs "speed"
	"degrees":   {"increase"},   // garbled STT output
	"flight":    {"right"},      // "flight 638" vs "turn right"
	"heading":   {"descending"}, // "heading 180" vs "descend"
	"stand":     {"ident"},      // "stand on the sand" vs "squawk ident"
}

// FuzzyMatch returns true if word matches target with Jaro-Winkler >= threshold
// or if they match phonetically.
func FuzzyMatch(word, target string, threshold float64) bool {
	if strings.EqualFold(word, target) {
		return true
	}
	// Check blocklist for known false positives
	wordLower := strings.ToLower(word)
	targetLower := strings.ToLower(target)
	if blocked, ok := fuzzyMatchBlocklist[wordLower]; ok {
		for _, b := range blocked {
			if b == targetLower {
				return false
			}
		}
	}
	// Prevent very short words from fuzzy-matching longer targets
	// (e.g., "i" shouldn't match "in" via JaroWinkler)
	if len(word) < 3 && len(target) > len(word) {
		return false
	}
	if JaroWinkler(word, target) >= threshold {
		return true
	}
	if PhoneticMatch(word, target) {
		return true
	}
	return false
}

// BestMatch finds the best matching string from candidates.
// Returns the match and its score, or empty string and 0 if none found above threshold.
func BestMatch(word string, candidates []string, threshold float64) (string, float64) {
	var best string
	var bestScore float64

	for _, c := range candidates {
		score := JaroWinkler(word, c)
		if PhoneticMatch(word, c) {
			score = max(score, 0.85) // Phonetic matches get at least 0.85
		}
		if score > bestScore && score >= threshold {
			best = c
			bestScore = score
		}
	}

	return best, bestScore
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
	return s
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

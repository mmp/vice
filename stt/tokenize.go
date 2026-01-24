package stt

import (
	"strconv"
	"strings"
)

// TokenType represents the type of a parsed token.
type TokenType int

const (
	TokenWord TokenType = iota
	TokenNumber
	TokenAltitude // Specifically identified as an altitude (e.g., "8 thousand")
	TokenFix      // Matched against aircraft fixes
	TokenApproach // Matched against candidate approaches
	TokenCallsign // Part of callsign
	TokenICAO     // Airline ICAO code
)

// Token represents a normalized piece of the transcript.
type Token struct {
	Original string    // Original text from STT (before normalization)
	Text     string    // Normalized text
	Type     TokenType // Token type
	Value    int       // Numeric value if applicable (-1 if not)
	Pos      int       // Position in original token sequence
}

// Tokenize converts normalized words into structured tokens.
// Handles number merging, altitude patterns, and type detection.
func Tokenize(words []string) []Token {
	if len(words) == 0 {
		return nil
	}

	tokens := make([]Token, 0, len(words))
	i := 0

	for i < len(words) {
		w := words[i]

		// Check for "flight level" pattern
		if w == "flight" && i+1 < len(words) && words[i+1] == "level" {
			fl, consumed := parseFlightLevel(words[i+2:])
			if consumed > 0 {
				tokens = append(tokens, Token{
					Text:  "FL" + strconv.Itoa(fl),
					Type:  TokenAltitude,
					Value: fl,
					Pos:   i,
				})
				i += 2 + consumed
				continue
			}
		}

		// Check for altitude patterns: "N thousand [M hundred]"
		if IsDigit(w) || IsNumber(w) {
			alt, consumed := parseAltitudePattern(words[i:])
			if consumed > 1 { // Must have consumed at least digit + "thousand"
				tokens = append(tokens, Token{
					Text:  strconv.Itoa(alt),
					Type:  TokenAltitude,
					Value: alt,
					Pos:   i,
				})
				i += consumed
				continue
			}
		}

		// Check for multi-digit numbers (callsign parts, squawk codes, etc.)
		// Type classification (heading, speed) is deferred to command parsing
		// where context determines meaning.
		if IsDigit(w) || IsNumber(w) {
			num, text, consumed := parseDigitSequence(words[i:])
			if consumed > 0 {
				tokens = append(tokens, Token{
					Text:  text, // Preserve original digit sequence (e.g., "020" vs "20")
					Type:  TokenNumber,
					Value: num,
					Pos:   i,
				})
				i += consumed
				continue
			}
		}

		// Check for ICAO airline code (3 uppercase letters)
		if len(w) == 3 && isUpperAlpha(w) {
			tokens = append(tokens, Token{
				Text: strings.ToUpper(w),
				Type: TokenICAO,
				Pos:  i,
			})
			i++
			continue
		}

		// Check for single number
		if num, err := strconv.Atoi(w); err == nil {
			tok := Token{
				Text:  w,
				Type:  TokenNumber,
				Value: num,
				Pos:   i,
			}
			// Large numbers might be malformed altitudes from STT
			if num >= 1000 && num%1000 == 0 {
				// "8000" spoken as number -> altitude 80
				encoded := num / 100
				// Flight levels (>=18,000 ft = 180 encoded) require explicit "flight level" speech
				if encoded < 180 {
					tok.Type = TokenAltitude
					tok.Value = encoded
				}
			} else if num >= 100000 && num < 1800000 {
				// STT error: "800,000" -> 8000 ft -> 80
				// Cap below 18,000 ft (180 encoded) - flight levels require explicit "flight level"
				tok.Type = TokenAltitude
				tok.Value = num / 10000
			}
			tokens = append(tokens, tok)
			i++
			continue
		}

		// Regular word token
		tokens = append(tokens, Token{
			Text:  w,
			Type:  TokenWord,
			Value: -1,
			Pos:   i,
		})
		i++
	}

	return tokens
}

// parseFlightLevel parses digits after "flight level".
// Returns the FL value and number of words consumed.
func parseFlightLevel(words []string) (int, int) {
	if len(words) == 0 {
		return 0, 0
	}

	// Try to parse digit sequence
	fl := 0
	consumed := 0
	for consumed < len(words) && consumed < 3 {
		if IsDigit(words[consumed]) {
			fl = fl*10 + ParseDigit(words[consumed])
			consumed++
		} else if IsNumber(words[consumed]) {
			// e.g., "350" as a single word
			n, _ := strconv.Atoi(words[consumed])
			return n, 1
		} else {
			break
		}
	}

	return fl, consumed
}

// parseAltitudePattern parses patterns like:
// - "8 thousand" -> 80
// - "8 thousand 5 hundred" -> 85
// - "11 thousand" -> 110
// Returns encoded altitude and words consumed.
func parseAltitudePattern(words []string) (int, int) {
	if len(words) < 2 {
		return 0, 0
	}

	// Get the first number
	var thousands int
	consumed := 0

	// Handle multi-digit thousands (e.g., "1 1" for 11, or "11" as word)
	for consumed < len(words) {
		if IsDigit(words[consumed]) {
			thousands = thousands*10 + ParseDigit(words[consumed])
			consumed++
		} else if n, err := strconv.Atoi(words[consumed]); err == nil && n < 100 {
			thousands = n
			consumed++
			break
		} else {
			break
		}
	}

	if consumed == 0 || consumed >= len(words) {
		return 0, 0
	}

	// Must have "thousand" next
	if words[consumed] != "thousand" {
		return 0, 0
	}
	consumed++

	// Encode: thousands * 10 (in hundreds)
	altitude := thousands * 10

	// Check for hundreds part
	if consumed < len(words) {
		var hundreds int
		hundredsStart := consumed

		// Get hundreds digit(s)
		for consumed < len(words) {
			if IsDigit(words[consumed]) {
				hundreds = hundreds*10 + ParseDigit(words[consumed])
				consumed++
			} else if n, err := strconv.Atoi(words[consumed]); err == nil && n < 10 {
				hundreds = n
				consumed++
				break
			} else {
				break
			}
		}

		// Must have "hundred" after
		if consumed < len(words) && words[consumed] == "hundred" {
			altitude += hundreds
			consumed++
		} else {
			// No "hundred" - revert
			consumed = hundredsStart
		}
	}

	return altitude, consumed
}

// parseDigitSequence parses consecutive number tokens into a number.
// Handles both single digits and multi-digit numbers from normalization.
// Returns the number, the text representation (preserving leading zeros), and words consumed.
func parseDigitSequence(words []string) (int, string, int) {
	num := 0
	text := ""
	consumed := 0
	lastWasMultiDigit := false

	for consumed < len(words) {
		w := words[consumed]
		if IsDigit(w) {
			// Single digit - check if merging would exceed reasonable limit
			candidate := num*10 + ParseDigit(w)
			if candidate > 100000 {
				break // Stop merging - result would be unreasonably large
			}
			// Look ahead: if we have a multi-digit number accumulated and the next
			// word after this digit is a large number (>=1000), don't merge this digit.
			// This allows "10 1 2000" to parse as "10" + "12000" instead of "101" + "2000".
			if lastWasMultiDigit && consumed+1 < len(words) {
				nextN := ParseNumber(words[consumed+1])
				if nextN >= 1000 {
					break // Let this digit start a new sequence with the large number
				}
			}
			num = candidate
			text += w
			consumed++
			lastWasMultiDigit = false
		} else if IsNumber(w) {
			// Multi-digit number from normalization (e.g., "20", "250")
			n := ParseNumber(w)
			if n < 0 {
				break
			}
			// Don't merge two consecutive multi-digit numbers
			// This prevents "210 210" from becoming "210210"
			if lastWasMultiDigit {
				break
			}
			// If we already have digits, combine appropriately
			if num > 0 {
				// Combine: 2 + 50 = 250, 2 + 5 + 0 = 250
				candidate := num*intPow10(len(w)) + n
				if candidate > 100000 {
					break // Stop merging - result would be unreasonably large
				}
				num = candidate
			} else {
				num = n
			}
			text += w
			consumed++
			lastWasMultiDigit = len(w) > 1
		} else {
			break
		}
	}

	return num, text, consumed
}

// intPow10 returns 10^n for small n.
func intPow10(n int) int {
	result := 1
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}

// isUpperAlpha checks if string is all uppercase letters.
func isUpperAlpha(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return len(s) > 0
}

// TokensToString converts tokens back to a space-separated string (for debugging).
func TokensToString(tokens []Token) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = t.Text
	}
	return strings.Join(parts, " ")
}

// FindTokenOfType finds the first token of the given type starting at index.
func FindTokenOfType(tokens []Token, start int, tokenType TokenType) int {
	for i := start; i < len(tokens); i++ {
		if tokens[i].Type == tokenType {
			return i
		}
	}
	return -1
}

// FindNumber finds the first numeric token starting at index.
func FindNumber(tokens []Token, start int) (int, int) {
	for i := start; i < len(tokens); i++ {
		if tokens[i].Type == TokenNumber || tokens[i].Type == TokenAltitude {
			return tokens[i].Value, i
		}
	}
	return -1, -1
}

// CountTokenType counts tokens of a given type.
func CountTokenType(tokens []Token, tokenType TokenType) int {
	count := 0
	for _, t := range tokens {
		if t.Type == tokenType {
			count++
		}
	}
	return count
}

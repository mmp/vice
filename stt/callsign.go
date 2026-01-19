package stt

import (
	"strconv"
	"strings"

	"github.com/mmp/vice/sim"
)

// CallsignMatch represents a matched callsign with confidence score.
type CallsignMatch struct {
	Callsign       string                     // The matched ICAO callsign (e.g., "AAL5936")
	SpokenKey      string                     // The key in the aircraft context map
	Confidence     float64                    // Match confidence (0.0-1.0)
	Consumed       int                        // Number of tokens consumed for the callsign
	AddressingForm sim.CallsignAddressingForm // How the callsign was addressed (full vs type+trailing3)
}

// MatchCallsign attempts to match tokens to an aircraft callsign.
// Tries starting at different positions to handle garbage words at the beginning.
// Returns the best match and remaining tokens after the callsign.
func MatchCallsign(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	logLocalStt("MatchCallsign: %d tokens, %d aircraft", len(tokens), len(aircraft))
	if len(tokens) == 0 || len(aircraft) == 0 {
		return CallsignMatch{}, tokens
	}

	// First, try exact match - if tokens exactly match a spoken name, use it immediately.
	// Try different phrase lengths (longest first for more specific matches).
	maxPhraseLen := min(len(tokens), 8) // Reasonable max for callsigns like GA N-numbers
	for length := maxPhraseLen; length >= 1; length-- {
		var parts []string
		for i := 0; i < length; i++ {
			parts = append(parts, tokens[i].Text)
		}
		phrase := strings.Join(parts, " ")

		for spokenName, ac := range aircraft {
			if strings.EqualFold(phrase, spokenName) {
				logLocalStt("  exact match: %q -> %q (consumed %d)", phrase, ac.Callsign, length)
				return CallsignMatch{
					Callsign:       string(ac.Callsign),
					SpokenKey:      spokenName,
					Confidence:     1.0,
					Consumed:       length,
					AddressingForm: ac.AddressingForm,
				}, tokens[length:]
			}
		}
	}

	var bestMatch CallsignMatch
	bestStartPos := 0

	// Try matching starting at different positions (to handle garbage at start)
	maxStartPos := min(3, len(tokens)-1) // Don't skip more than 3 tokens
	for startPos := 0; startPos <= maxStartPos; startPos++ {
		for spokenName, ac := range aircraft {
			score, consumed := scoreCallsignMatch(tokens[startPos:], spokenName, string(ac.Callsign))
			if startPos == 0 {
				logLocalStt("  candidate %q (ICAO=%q): score=%.2f consumed=%d",
					spokenName, ac.Callsign, score, consumed)
			}
			// Penalty for skipping tokens at the start - require higher base score
			adjustedScore := score
			if startPos > 0 && score > 0 {
				// Require higher base score when skipping (0.75 minimum)
				if score < 0.75 {
					continue
				}
				adjustedScore = score * (1.0 - 0.1*float64(startPos))
				logLocalStt("  candidate %q at pos %d: score=%.2f (adjusted=%.2f) consumed=%d",
					spokenName, startPos, score, adjustedScore, consumed)
			}
			// Prefer higher score, or more tokens consumed on tie (more specific match)
			if adjustedScore > bestMatch.Confidence ||
				(adjustedScore == bestMatch.Confidence && startPos+consumed > bestMatch.Consumed) {
				bestMatch = CallsignMatch{
					Callsign:       string(ac.Callsign),
					SpokenKey:      spokenName,
					Confidence:     adjustedScore,
					Consumed:       startPos + consumed, // Include skipped tokens
					AddressingForm: ac.AddressingForm,
				}
				bestStartPos = startPos
			}
		}
	}

	// Threshold for accepting a match
	if bestMatch.Confidence < 0.5 {
		logLocalStt("  best match conf=%.2f below threshold 0.5, trying flight-number-only fallback", bestMatch.Confidence)
		// Fallback: try to match just a flight number if it uniquely identifies an aircraft
		if match, remaining := tryFlightNumberOnlyMatch(tokens, aircraft); match.Callsign != "" {
			return match, remaining
		}
		return CallsignMatch{}, tokens
	}

	logLocalStt("  accepted: %q (conf=%.2f, consumed=%d, startPos=%d)",
		bestMatch.Callsign, bestMatch.Confidence, bestMatch.Consumed, bestStartPos)
	return bestMatch, tokens[bestMatch.Consumed:]
}

// scoreCallsignMatch computes how well tokens match a callsign.
func scoreCallsignMatch(tokens []Token, spokenName, callsign string) (float64, int) {
	// Parse the spoken name: "American 5936", "China Southern 940 heavy", "November 1 2 3 AB"
	spokenParts := strings.Fields(strings.ToLower(spokenName))
	if len(spokenParts) == 0 {
		return 0, 0
	}

	// Split callsign into prefix and number: "AAL5936" -> "AAL", "5936"
	prefix, number := splitCallsign(callsign)

	// Figure out where the airline name ends in spoken parts
	// The flight number starts at the first numeric part
	airlineEndIdx := 0
	for i, part := range spokenParts {
		if IsNumber(part) || (len(part) > 0 && part[0] >= '0' && part[0] <= '9') {
			airlineEndIdx = i
			break
		}
		if part == "heavy" || part == "super" {
			airlineEndIdx = i
			break
		}
		airlineEndIdx = i + 1
	}
	airlineParts := spokenParts[:airlineEndIdx]

	totalScore := 0.0
	consumed := 0
	matchCount := 0

	// Try to match airline/prefix part (may be multiple words)
	if len(tokens) > 0 && len(airlineParts) > 0 {
		firstToken := strings.ToLower(tokens[0].Text)

		// Check if first token is an ICAO code matching prefix
		if tokens[0].Type == TokenICAO && strings.EqualFold(tokens[0].Text, prefix) {
			totalScore += 1.0
			consumed++
			matchCount++
		} else {
			// Try to match tokens against airline name parts
			airlineScore, airlineConsumed := scoreMultiWordAirline(tokens, airlineParts)
			if airlineScore > 0.6 {
				totalScore += airlineScore
				consumed += airlineConsumed
				matchCount++
			} else {
				// Fallback: try single word match against first airline part
				singleScore := scoreAirlineMatch(firstToken, airlineParts[0], prefix)
				if singleScore > 0.6 {
					totalScore += singleScore
					consumed++
					matchCount++
				}
			}
		}
	}

	// Try to match flight number
	if number != "" {
		bestNumScore := 0.0
		bestNumConsumed := 0
		bestNumStart := consumed

		// Search from current position (after airline if matched) to a few positions later.
		// This handles cases where the airline didn't match but the flight number is in the tokens.
		searchStart := 0
		if matchCount > 0 {
			searchStart = consumed // If airline matched, start searching after it
		}
		searchEnd := min(len(tokens), searchStart+3) // Don't search too far

		for startIdx := searchStart; startIdx < searchEnd; startIdx++ {
			numScore, numConsumed := scoreFlightNumberMatch(tokens[startIdx:], number)
			if numScore > bestNumScore {
				bestNumScore = numScore
				bestNumConsumed = numConsumed
				bestNumStart = startIdx
			}
		}

		if bestNumScore > 0.5 {
			totalScore += bestNumScore
			consumed = bestNumStart + bestNumConsumed
			matchCount++
		}
	}

	// Handle GA callsigns (N-numbers)
	if consumed == 0 && strings.HasPrefix(callsign, "N") {
		gaScore, gaConsumed := scoreGACallsign(tokens, callsign)
		if gaScore > 0.5 {
			return gaScore, gaConsumed
		}
	}

	if matchCount == 0 {
		return 0, 0
	}

	// Consume callsign suffixes (heavy, super) if present
	if consumed < len(tokens) {
		suffix := strings.ToLower(tokens[consumed].Text)
		if suffix == "heavy" || suffix == "super" {
			consumed++
		}
	}

	// Average the scores
	avgScore := totalScore / float64(matchCount)

	// Bonus for matching both airline and number
	if matchCount >= 2 {
		avgScore = min(1.0, avgScore*1.1)
	}

	return avgScore, consumed
}

// scoreMultiWordAirline scores how well tokens match a multi-word airline name.
func scoreMultiWordAirline(tokens []Token, airlineParts []string) (float64, int) {
	if len(tokens) < len(airlineParts) {
		return 0, 0
	}

	totalScore := 0.0
	for i, part := range airlineParts {
		tokenText := strings.ToLower(tokens[i].Text)
		score := JaroWinkler(tokenText, part)
		if score < 0.75 && !PhoneticMatch(tokenText, part) {
			return 0, 0 // One word doesn't match well enough
		}
		totalScore += max(score, 0.75)
	}

	avgScore := totalScore / float64(len(airlineParts))
	return avgScore, len(airlineParts)
}

// scoreAirlineMatch scores how well a spoken word matches an airline.
func scoreAirlineMatch(spoken, expected, icao string) float64 {
	// Exact match
	if spoken == expected {
		return 1.0
	}

	// Check if spoken matches the ICAO code directly (lowercased)
	if strings.EqualFold(spoken, icao) {
		return 1.0
	}

	// Fuzzy match
	jwScore := JaroWinkler(spoken, expected)
	if jwScore > 0.85 {
		return jwScore
	}

	// Phonetic match
	if PhoneticMatch(spoken, expected) {
		return 0.85
	}

	return jwScore
}

// scoreFlightNumberMatch scores how well tokens match a flight number.
func scoreFlightNumberMatch(tokens []Token, expectedNum string) (float64, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	// Build the number from consecutive digit tokens or a number token
	var builtNum strings.Builder
	consumed := 0

	for consumed < len(tokens) {
		t := tokens[consumed]

		// Single digit token
		if IsDigit(t.Text) {
			builtNum.WriteString(t.Text)
			consumed++
			continue
		}

		// Multi-digit number token
		if (t.Type == TokenNumber || t.Type == TokenAltitude) && t.Value >= 0 {
			builtNum.WriteString(strconv.Itoa(t.Value))
			consumed++
			continue
		}

		// Letter in callsign (GA: N123AB)
		if len(t.Text) == 1 && isLetter(t.Text) {
			builtNum.WriteString(strings.ToUpper(t.Text))
			consumed++
			continue
		}

		// NATO phonetic letter (e.g., "whiskey" -> "W", "juliet" -> "J")
		if letter, ok := ConvertNATOLetter(t.Text); ok {
			builtNum.WriteString(strings.ToUpper(letter))
			consumed++
			continue
		}

		// Alphanumeric token (like "4wj") - common in callsigns
		if isAlphanumeric(t.Text) {
			builtNum.WriteString(strings.ToUpper(t.Text))
			consumed++
			continue
		}

		break
	}

	if consumed == 0 {
		return 0, 0
	}

	built := builtNum.String()

	// Exact match
	if built == expectedNum {
		return 1.0, consumed
	}

	// Check if built number is a suffix of expected (partial match)
	if strings.HasSuffix(expectedNum, built) {
		// Partial match - slightly lower confidence
		return 0.85, consumed
	}

	// Check if expected is a suffix of built (overshot)
	if strings.HasSuffix(built, expectedNum) {
		return 0.8, consumed
	}

	// Fuzzy match on the number
	jwScore := JaroWinkler(built, expectedNum)
	if jwScore > 0.7 {
		return jwScore, consumed
	}

	return 0, 0
}

// scoreGACallsign scores General Aviation N-number callsigns.
func scoreGACallsign(tokens []Token, callsign string) (float64, int) {
	// GA callsigns: N12345, N123AB
	// May be spoken as "November 1 2 3 4 5" or "three four five" (last 3)
	if len(tokens) == 0 {
		return 0, 0
	}

	// Check for "november" or "n" at start
	firstLower := strings.ToLower(tokens[0].Text)
	consumed := 0

	if firstLower == "november" || firstLower == "n" {
		consumed = 1
	}

	// Build the rest of the callsign from digits/letters
	var built strings.Builder
	built.WriteString("N")

	for consumed < len(tokens) {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		if IsDigit(text) || (len(text) == 1 && isLetter(text)) {
			built.WriteString(strings.ToUpper(text))
			consumed++
		} else if len(text) == 1 && text[0] >= 'a' && text[0] <= 'z' {
			built.WriteString(strings.ToUpper(text))
			consumed++
		} else if letter, ok := ConvertNATOLetter(text); ok {
			// Handle NATO phonetic letters: "alpha" -> "A", "bravo" -> "B", etc.
			built.WriteString(strings.ToUpper(letter))
			consumed++
		} else {
			break
		}
	}

	if consumed == 0 {
		return 0, 0
	}

	builtCS := built.String()

	// Exact match
	if builtCS == callsign {
		return 1.0, consumed
	}

	// Check for suffix match (last 3 characters)
	if len(builtCS) >= 3 && len(callsign) >= 3 {
		if builtCS[len(builtCS)-3:] == callsign[len(callsign)-3:] {
			return 0.85, consumed
		}
	}

	// Fuzzy match
	return JaroWinkler(builtCS, callsign), consumed
}

// tryFlightNumberOnlyMatch attempts to match a callsign using just the flight number.
// Used as fallback when airline name is garbled/missing. Only succeeds if the number
// uniquely identifies one aircraft.
func tryFlightNumberOnlyMatch(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	// Scan tokens for numbers
	for i, t := range tokens {
		var numStr string
		if t.Type == TokenNumber {
			numStr = strconv.Itoa(t.Value)
		} else if IsNumber(t.Text) {
			numStr = t.Text
		} else {
			continue
		}

		// Find aircraft whose flight number matches
		var matches []struct {
			spokenKey string
			ac        Aircraft
		}
		for spokenName, ac := range aircraft {
			_, flightNum := splitCallsign(string(ac.Callsign))
			if flightNum == numStr {
				matches = append(matches, struct {
					spokenKey string
					ac        Aircraft
				}{spokenName, ac})
			}
		}

		// Only use if exactly one aircraft matches
		if len(matches) == 1 {
			match := matches[0]
			logLocalStt("  flight-number-only fallback: %q matches %q (ICAO=%q)",
				numStr, match.spokenKey, match.ac.Callsign)
			return CallsignMatch{
				Callsign:   string(match.ac.Callsign),
				SpokenKey:  match.spokenKey,
				Confidence: 0.6, // Lower confidence for number-only match
				Consumed:   i + 1,
			}, tokens[i+1:]
		}
	}

	return CallsignMatch{}, nil
}

// splitCallsign splits an ICAO callsign into prefix and number.
// "AAL5936" -> "AAL", "5936"
// "N123AB" -> "N", "123AB"
func splitCallsign(callsign string) (string, string) {
	for i, c := range callsign {
		if c >= '0' && c <= '9' {
			return callsign[:i], callsign[i:]
		}
	}
	return callsign, ""
}

// isLetter checks if a single-character string is a letter.
func isLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isAlphanumeric checks if a string contains only letters and digits,
// with at least one of each (making it a mixed alphanumeric like "4WJ").
func isAlphanumeric(s string) bool {
	if len(s) < 2 {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
		} else if c >= '0' && c <= '9' {
			hasDigit = true
		} else {
			return false // non-alphanumeric character
		}
	}
	return hasLetter && hasDigit
}

// Aircraft holds context for a single aircraft for STT processing.
type Aircraft struct {
	Callsign            string
	AircraftType        string            // Aircraft type code (e.g., "C172", "BE36")
	Fixes               map[string]string // spoken name -> fix ID
	CandidateApproaches map[string]string // spoken name -> approach ID
	AssignedApproach    string
	Altitude            int                        // Current altitude in feet
	State               string                     // "departure", "arrival", "overflight", "on approach", "vfr flight following"
	ControllerFrequency string                     // Current controller position the aircraft is tuned to
	TrackingController  string                     // Controller tracking this aircraft (from flight plan)
	AddressingForm      sim.CallsignAddressingForm // How this aircraft was addressed (based on which key matched)
}

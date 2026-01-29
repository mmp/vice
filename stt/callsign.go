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

	// Check for "heavy" or "super" in early tokens (within callsign region).
	// If found, filter to only consider aircraft with matching weight class.
	weightClass := detectWeightClass(tokens)
	if weightClass != "" {
		filtered := filterByWeightClass(aircraft, weightClass)
		if len(filtered) > 0 {
			logLocalStt("  detected %q in callsign region, filtering to %d aircraft", weightClass, len(filtered))
			aircraft = filtered
		}
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
	tieCount := 0 // Count of candidates with same best score and consumed tokens

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
			totalConsumed := startPos + consumed

			// Track ties: candidates with same score and consumed tokens
			if adjustedScore == bestMatch.Confidence && totalConsumed == bestMatch.Consumed && adjustedScore > 0 {
				tieCount++
			}

			// Prefer higher score, or more tokens consumed on tie (more specific match),
			// or alphabetically earlier callsign as final tie-breaker for determinism.
			isBetter := adjustedScore > bestMatch.Confidence ||
				(adjustedScore == bestMatch.Confidence && totalConsumed > bestMatch.Consumed) ||
				(adjustedScore == bestMatch.Confidence && totalConsumed == bestMatch.Consumed &&
					string(ac.Callsign) < bestMatch.Callsign)
			if isBetter {
				if adjustedScore > bestMatch.Confidence || totalConsumed > bestMatch.Consumed {
					tieCount = 0 // New best, reset tie count
				}
				bestMatch = CallsignMatch{
					Callsign:       string(ac.Callsign),
					SpokenKey:      spokenName,
					Confidence:     adjustedScore,
					Consumed:       totalConsumed, // Include skipped tokens
					AddressingForm: ac.AddressingForm,
				}
				bestStartPos = startPos
			}
		}
	}

	// If there are ties (multiple candidates with same best score/consumed) AND the entire
	// input was just the airline name with no additional tokens, it's ambiguous.
	// When there are additional tokens beyond the match, they could be a garbled flight
	// number, so use the tie-breaker instead of rejecting.
	if tieCount > 0 && bestMatch.Consumed == 1 && len(tokens) == 1 {
		logLocalStt("  ambiguous: %d candidates tied with score=%.2f consumed=%d (airline only, no additional tokens), rejecting match",
			tieCount+1, bestMatch.Confidence, bestMatch.Consumed)
		return CallsignMatch{}, tokens
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

	// Also try flight-number fallback for marginal matches - a clear flight number
	// match may be better than a weak airline-name match
	if bestMatch.Confidence < 0.7 {
		if match, remaining := tryFlightNumberOnlyMatch(tokens, aircraft); match.Callsign != "" {
			// Use the flight number match if it's at least as confident
			if match.Confidence >= bestMatch.Confidence {
				logLocalStt("  flight-number fallback %q (conf=%.2f) beats marginal match %q (conf=%.2f)",
					match.Callsign, match.Confidence, bestMatch.Callsign, bestMatch.Confidence)
				return match, remaining
			}
		}
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

	// Bonus for matching both airline and number.
	// Don't cap at 1.0 so exact flight number matches rank higher than suffix matches.
	if matchCount >= 2 {
		avgScore *= 1.1
	}

	// When we matched airline but not the flight number, consume any trailing
	// numeric tokens as "garbled callsign noise" rather than leaving them to be
	// misinterpreted as commands. Skip "at" if it precedes numbers (common STT artifact).
	if matchCount == 1 && number != "" && consumed < len(tokens) {
		// Skip "at" if followed by numbers (STT often inserts "at" in garbled callsigns)
		if strings.ToLower(tokens[consumed].Text) == "at" && consumed+1 < len(tokens) {
			if tokens[consumed+1].Type == TokenNumber {
				consumed++
			}
		}
		// Consume consecutive numeric tokens as garbled flight number
		for consumed < len(tokens) && tokens[consumed].Type == TokenNumber {
			consumed++
		}
		// Penalize for not matching the flight number well
		avgScore *= 0.8
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
	if jwScore >= 0.7 {
		return jwScore, consumed
	}

	// For 2-digit numbers, if the trailing digit matches (like "91" vs "81"),
	// give a reasonable score since ATC often uses trailing digits to disambiguate.
	// STT commonly confuses similar-sounding digits (e.g., "eight" vs "nine").
	if len(built) == 2 && len(expectedNum) == 2 && built[1] == expectedNum[1] {
		return 0.7, consumed
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

		// Find aircraft whose flight number matches (exact or fuzzy)
		var exactMatch *struct {
			spokenKey string
			ac        Aircraft
		}
		var fuzzyMatch *struct {
			spokenKey string
			ac        Aircraft
			score     float64
		}
		exactCount, fuzzyCount := 0, 0

		for spokenName, ac := range aircraft {
			_, flightNum := splitCallsign(string(ac.Callsign))
			if flightNum == numStr {
				exactMatch = &struct {
					spokenKey string
					ac        Aircraft
				}{spokenName, ac}
				exactCount++
			} else if len(numStr) >= 3 && len(flightNum) >= 3 {
				// Try fuzzy match for longer flight numbers (3+ digits)
				score := JaroWinkler(numStr, flightNum)
				if score >= 0.8 {
					fuzzyMatch = &struct {
						spokenKey string
						ac        Aircraft
						score     float64
					}{spokenName, ac, score}
					fuzzyCount++
				}
			}
		}

		// Prefer exact match if unique
		if exactCount == 1 && exactMatch != nil {
			logLocalStt("  flight-number-only fallback: %q matches %q (ICAO=%q) exactly",
				numStr, exactMatch.spokenKey, exactMatch.ac.Callsign)
			return CallsignMatch{
				Callsign:   string(exactMatch.ac.Callsign),
				SpokenKey:  exactMatch.spokenKey,
				Confidence: 0.7,
				Consumed:   i + 1,
			}, tokens[i+1:]
		}

		// Use fuzzy match if unique and no exact matches
		if exactCount == 0 && fuzzyCount == 1 && fuzzyMatch != nil {
			logLocalStt("  flight-number-only fallback: %q fuzzy matches %q (ICAO=%q, score=%.2f)",
				numStr, fuzzyMatch.spokenKey, fuzzyMatch.ac.Callsign, fuzzyMatch.score)
			return CallsignMatch{
				Callsign:   string(fuzzyMatch.ac.Callsign),
				SpokenKey:  fuzzyMatch.spokenKey,
				Confidence: 0.65,
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
	AircraftType        string                       // Aircraft type code (e.g., "C172", "BE36")
	Fixes               map[string]string            // spoken name -> fix ID
	CandidateApproaches map[string]string            // spoken name -> approach ID
	ApproachFixes       map[string]map[string]string // approach ID -> (spoken name -> fix ID)
	AssignedApproach    string
	SID                 string
	STAR                string
	Altitude            int                        // Current altitude in feet
	State               string                     // "departure", "arrival", "overflight", "on approach", "vfr flight following"
	ControllerFrequency string                     // Current controller position the aircraft is tuned to
	TrackingController  string                     // Controller tracking this aircraft (from flight plan)
	AddressingForm      sim.CallsignAddressingForm // How this aircraft was addressed (based on which key matched)
	LAHSORunways        []string                   // Runways that intersect the approach runway (for LAHSO matching)
}

// detectWeightClass checks the early tokens (callsign region) for "heavy" or "super".
// Returns the weight class if found, empty string otherwise.
func detectWeightClass(tokens []Token) string {
	// Check first 5 tokens (reasonable callsign region)
	limit := min(len(tokens), 5)
	for i := 0; i < limit; i++ {
		text := strings.ToLower(tokens[i].Text)
		if text == "heavy" || text == "super" {
			return text
		}
	}
	return ""
}

// filterByWeightClass returns only aircraft whose spoken name contains the weight class.
func filterByWeightClass(aircraft map[string]Aircraft, weightClass string) map[string]Aircraft {
	filtered := make(map[string]Aircraft)
	for spokenName, ac := range aircraft {
		if strings.Contains(strings.ToLower(spokenName), weightClass) {
			filtered[spokenName] = ac
		}
	}
	return filtered
}

package stt

import (
	"strconv"
	"strings"

	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
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
//
// Matching proceeds in phases:
//  1. Weight class filtering - if "heavy"/"super" found, filter aircraft first
//  2. Exact phrase match - tokens exactly match a spoken name
//  3. Fuzzy airline + flight number match - match airline then flight number
//  4. Flight-number-only fallback - for garbled/missing airline names
func MatchCallsign(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	logLocalStt("MatchCallsign: %d tokens, %d aircraft", len(tokens), len(aircraft))
	if len(tokens) == 0 || len(aircraft) == 0 {
		return CallsignMatch{}, tokens
	}

	// Phase 1: Weight class filtering
	// If "heavy" or "super" found in early tokens, filter to matching aircraft
	if weightClassIdx := findWeightClassTokenIndex(tokens); weightClassIdx != -1 {
		weightClass := tokens[weightClassIdx].Text
		if filtered := filterByWeightClass(aircraft, weightClass); len(filtered) > 0 && len(filtered) < len(aircraft) {
			logLocalStt("  detected %q in callsign region, filtering to %d aircraft", weightClass, len(filtered))
			// Only pass the tokens up to and including heavy/super
			match, _ := MatchCallsign(tokens[:weightClassIdx+1], filtered)
			// Return the tokens following heavy/super with the match
			return match, tokens[weightClassIdx+1:]
		}
	}

	// Phase 2: Exact phrase match
	// Try to find an exact match - if tokens exactly match a spoken name, use it immediately.
	// Also try suffix matching (e.g., "2 victor romeo" matches end of "november 9 2 2 victor romeo").
	// Try different phrase lengths (longest first for more specific matches) and also try dropping
	// 1 or 2 tokens from the start.
	for start := range min(3, len(tokens)) {
		maxPhraseLen := min(len(tokens)-start, 8) // Reasonable max for callsigns like GA N-numbers
		parts := util.MapSlice(tokens[start:start+maxPhraseLen], func(t Token) string { return t.Text })
		for len(parts) > 1 {
			phrase := strings.Join(parts, " ")

			for spokenName, ac := range aircraft {
				// Normalize the spoken name the same way we normalize transcripts
				// so "two" becomes "2", etc.
				normalizedSpoken := strings.Join(NormalizeTranscript(spokenName), " ")
				if strings.EqualFold(phrase, normalizedSpoken) {
					logLocalStt("  exact match: %q -> %q (consumed %d)", phrase, ac.Callsign, start+len(parts))
					return CallsignMatch{
						Callsign:       string(ac.Callsign),
						SpokenKey:      spokenName,
						Confidence:     1.0,
						Consumed:       start + len(parts),
						AddressingForm: ac.AddressingForm,
					}, tokens[start+len(parts):]
				}
				// Also check if phrase matches a suffix of the spoken name (for abbreviated GA callsigns)
				if len(parts) >= 3 && strings.HasPrefix(string(ac.Callsign), "N") && strings.HasSuffix(strings.ToLower(normalizedSpoken), strings.ToLower(phrase)) {
					logLocalStt("  suffix match: %q -> %q (consumed %d)", phrase, ac.Callsign, start+len(parts))
					return CallsignMatch{
						Callsign:       string(ac.Callsign),
						SpokenKey:      spokenName,
						Confidence:     0.95,
						Consumed:       start + len(parts),
						AddressingForm: 0, // Default - we matched suffix but controller said more
					}, tokens[start+len(parts):]
				}
			}

			parts = parts[:len(parts)-1]
		}
	}

	// Phase 3: Fuzzy airline + flight number match
	// Filter by airline name, then match flight number
	for skip := range min(3, len(tokens)) {
		toks := tokens[skip:]
		if len(toks) == 0 {
			break
		}

		// Step 1: Filter aircraft by airline telephony match (all words must match)
		airlineMatches := filterByAirlineMatch(toks, aircraft)
		if len(airlineMatches) == 0 {
			continue // try skipping more tokens
		}

		// Step 2: For each airline match, try to match flight number
		var exactMatches, fuzzyMatches []callsignCandidate
		for _, am := range airlineMatches {
			remaining := toks[am.airlineTokens:]
			flightNum := flightNumber(string(am.ac.Callsign))
			if flightNum == "" {
				continue
			}

			exact, consumed, score := matchFlightNumber(remaining, flightNum)
			if exact {
				exactMatches = append(exactMatches, callsignCandidate{am, consumed, score, true})
			} else if score > 0 {
				fuzzyMatches = append(fuzzyMatches, callsignCandidate{am, consumed, score, false})
			}
		}

		// Step 2b: Also check for exact flight number matches in ALL aircraft,
		// regardless of airline match. An exact flight number is a strong signal.
		// Scan first few positions for a number token.
		for startPos := range min(3, len(toks)) {
			t := toks[startPos]
			if t.Type != TokenNumber && !IsDigit(t.Text) {
				continue
			}
			builtNum, consumed := collectDigits(toks[startPos:], 4)
			if builtNum == "" {
				continue
			}
			for spokenName, ac := range aircraft {
				flightNum := flightNumber(string(ac.Callsign))
				if flightNum == builtNum {
					// Check if we already have this as an exact match
					alreadyHave := false
					for _, em := range exactMatches {
						if string(em.ac.Callsign) == string(ac.Callsign) {
							alreadyHave = true
							break
						}
					}
					if !alreadyHave {
						exactMatches = append(exactMatches, callsignCandidate{
							airlineMatch: airlineMatch{
								spokenKey:     spokenName,
								ac:            ac,
								airlineTokens: startPos, // tokens before the number
								airlineExact:  false,
								airlineScore:  0.5, // nominal score for flight-number-only
							},
							flightTokens: consumed,
							flightScore:  1.0,
							flightExact:  true,
						})
					}
				}
			}
			break // Only try the first number position
		}

		// Step 3: Return based on what we found
		if len(exactMatches) == 1 {
			return makeCallsignMatch(exactMatches[0], skip, tokens), tokens[skip+exactMatches[0].totalConsumed():]
		}
		if len(exactMatches) > 1 {
			// Multiple exact flight number matches - disambiguate by SpokenKey
			if best := findExactSpokenKeyMatch(toks, exactMatches); best != nil {
				return makeCallsignMatch(*best, skip, tokens), tokens[skip+best.totalConsumed():]
			}
			best := bestByScore(exactMatches)
			return makeCallsignMatch(best, skip, tokens), tokens[skip+best.totalConsumed():]
		}
		if len(fuzzyMatches) == 1 {
			return makeCallsignMatch(fuzzyMatches[0], skip, tokens), tokens[skip+fuzzyMatches[0].totalConsumed():]
		}
		if len(fuzzyMatches) > 1 {
			if best := findExactSpokenKeyMatch(toks, fuzzyMatches); best != nil {
				return makeCallsignMatch(*best, skip, tokens), tokens[skip+best.totalConsumed():]
			}
			best := bestByScore(fuzzyMatches)
			return makeCallsignMatch(best, skip, tokens), tokens[skip+best.totalConsumed():]
		}

		// Step 4: Fallback to airline-only match if we have airline matches but no flight number
		// This handles cases where STT splits an airline name (e.g., "south west" for "Southwest")
		// and the remaining tokens don't contain the flight number.
		// BUT: only do this if there's no better flight-number-only match available.
		if len(airlineMatches) > 0 && len(exactMatches) == 0 && len(fuzzyMatches) == 0 {
			// Check if flight-number-only match would find something better
			if flightOnlyMatch, _ := tryFlightNumberOnlyMatch(toks, aircraft); flightOnlyMatch.Callsign != "" {
				// If the flight-number-only match is for a different callsign,
				// prefer it over the airline-only match
				bestAirlineCallsign := ""
				for _, am := range airlineMatches {
					if bestAirlineCallsign == "" || string(am.ac.Callsign) < bestAirlineCallsign {
						bestAirlineCallsign = string(am.ac.Callsign)
					}
				}
				if flightOnlyMatch.Callsign != bestAirlineCallsign {
					// Flight-number-only match found a different callsign, skip airline-only
					continue
				}
			}

			// Pick the best airline-only match (by score, then callsign for determinism)
			best := airlineMatches[0]
			for _, am := range airlineMatches[1:] {
				if am.airlineScore > best.airlineScore {
					best = am
				} else if am.airlineScore == best.airlineScore && string(am.ac.Callsign) < string(best.ac.Callsign) {
					best = am
				}
			}
			// Only accept airline-only matches with reasonably high scores
			if best.airlineScore < 0.7 {
				continue
			}
			// Create a candidate with no flight tokens consumed
			candidate := callsignCandidate{best, 0, 0.5, false}
			return makeCallsignMatch(candidate, skip, tokens), tokens[skip+candidate.totalConsumed():]
		}
	}

	// Note: GA abbreviated forms ("3 alpha bravo", "skyhawk 3 alpha bravo") are
	// handled naturally by the main matching logic since aircraft have multiple
	// entries in the map (e.g., "november 1 2 3 alpha bravo" and "skyhawk 3 alpha bravo").

	// Phase 4: Flight-number-only fallback
	// Try to match using just the flight number when airline name is garbled/missing
	if match, remaining := tryFlightNumberOnlyMatch(tokens, aircraft); match.Callsign != "" {
		return match, remaining
	}

	return CallsignMatch{}, tokens
}

// airlineMatch represents an aircraft that matched on airline name.
type airlineMatch struct {
	spokenKey     string
	ac            Aircraft
	airlineTokens int     // tokens consumed for airline
	airlineExact  bool    // true if exact match
	airlineScore  float64 // JW similarity score for airline (1.0 for exact)
}

// callsignCandidate represents a potential callsign match.
type callsignCandidate struct {
	airlineMatch
	flightTokens int
	flightScore  float64
	flightExact  bool
}

func (c callsignCandidate) totalConsumed() int {
	return c.airlineTokens + c.flightTokens
}

// parseAirlineParts extracts airline word parts from a spoken name.
// "American 5936" -> ["american"]
// "China Southern 940" -> ["china", "southern"]
// "November 123AB" -> ["november"]
func parseAirlineParts(spokenName string) []string {
	parts := strings.Fields(strings.ToLower(spokenName))
	if len(parts) == 0 {
		return nil
	}

	// Find where the airline name ends (first numeric part or heavy/super)
	airlineEndIdx := 0
	for i, part := range parts {
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

	return parts[:airlineEndIdx]
}

// matchWord compares word to target with fuzzy matching.
// Returns (exact, score) where exact is true if strings match case-insensitively,
// and score is the similarity (1.0 for exact, JaroWinkler otherwise).
// Phonetic matches get a minimum score of 0.85.
func matchWord(word, target string) (exact bool, score float64) {
	if strings.EqualFold(word, target) {
		return true, 1.0
	}
	score = JaroWinkler(word, target)
	if PhoneticMatch(word, target) && score < 0.85 {
		score = 0.85
	}
	return false, score
}

// filterByAirlineMatch filters aircraft by airline telephony match.
// Uses a tiered threshold: exact (1.0), high (>=0.85), and low (>=0.35-0.4).
// Low-threshold matches are included to enable combined scoring with flight numbers.
func filterByAirlineMatch(tokens []Token, aircraft map[string]Aircraft) []airlineMatch {
	var matches []airlineMatch
	for spokenName, ac := range aircraft {
		if am, ok := tryMatchAirline(tokens, spokenName, ac); ok {
			matches = append(matches, am)
		}
	}
	return matches
}

// tryMatchAirline attempts to match tokens to a single aircraft's airline name.
func tryMatchAirline(tokens []Token, spokenName string, ac Aircraft) (airlineMatch, bool) {
	parts := parseAirlineParts(spokenName)
	if len(parts) == 0 || len(tokens) == 0 {
		return airlineMatch{}, false
	}
	tokenText := strings.ToLower(tokens[0].Text)

	// Single-word airline
	if len(parts) == 1 {
		exact, score := matchWord(tokenText, parts[0])
		if score >= 0.35 {
			return airlineMatch{spokenName, ac, 1, exact, score}, true
		}
		return airlineMatch{}, false
	}

	// Multi-word: try matching all parts to consecutive tokens
	if len(tokens) >= len(parts) {
		if am, ok := matchMultiWordAirline(tokens, parts, spokenName, ac); ok {
			return am, true
		}
	}

	// Multi-word: try concatenated form (e.g., "airfrance")
	concat := strings.Join(parts, "")
	if exact, score := matchWord(tokenText, concat); score >= 0.85 {
		return airlineMatch{spokenName, ac, 1, exact, score}, true
	}

	// Multi-word: try first word only (e.g., "southwest")
	// Handles cases like "Southwest seven 95" where "seven" is part of flight number
	exact, score := matchWord(tokenText, parts[0])
	if score >= 0.4 {
		return airlineMatch{spokenName, ac, 1, exact, score}, true
	}

	return airlineMatch{}, false
}

// matchMultiWordAirline tries to match each airline part to consecutive tokens.
func matchMultiWordAirline(tokens []Token, parts []string, spokenName string, ac Aircraft) (airlineMatch, bool) {
	allExact := true
	totalScore := 0.0
	for i, part := range parts {
		tokenText := strings.ToLower(tokens[i].Text)
		exact, score := matchWord(tokenText, part)
		if score < 0.85 {
			return airlineMatch{}, false
		}
		if !exact {
			allExact = false
		}
		totalScore += score
	}
	return airlineMatch{spokenName, ac, len(parts), allExact, totalScore / float64(len(parts))}, true
}

// collectDigits builds a digit string from consecutive number tokens.
// Returns the string and number of tokens consumed.
// If maxTokens > 0, stops after that many tokens are consumed.
func collectDigits(tokens []Token, maxTokens int) (string, int) {
	var numStr strings.Builder
	consumed := 0
	for _, t := range tokens {
		if maxTokens > 0 && consumed >= maxTokens {
			break
		}
		if t.Type == TokenNumber {
			numStr.WriteString(strconv.Itoa(t.Value))
			consumed++
		} else if IsNumber(t.Text) || IsDigit(t.Text) {
			numStr.WriteString(t.Text)
			consumed++
		} else {
			break
		}
	}
	return numStr.String(), consumed
}

// matchFlightNumber matches tokens against an expected flight number.
// Returns (exact, consumed, score).
func matchFlightNumber(tokens []Token, expectedNum string) (exact bool, consumed int, score float64) {
	if len(tokens) == 0 || expectedNum == "" {
		return false, 0, 0
	}

	// Build flight number from consecutive digit/letter tokens
	var builtNum strings.Builder
	consumed = 0

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
		return false, 0, 0
	}

	built := builtNum.String()

	// Exact match
	if built == expectedNum {
		return true, consumed, 1.0
	}

	// Check if built number is a suffix of expected (partial match)
	if strings.HasSuffix(expectedNum, built) {
		return false, consumed, 0.85
	}

	// Check if expected is a suffix of built (overshot)
	if strings.HasSuffix(built, expectedNum) {
		return false, consumed, 0.8
	}

	// Fuzzy match on the number
	jwScore := JaroWinkler(built, expectedNum)
	if jwScore >= 0.7 {
		return false, consumed, jwScore
	}

	// For 2-digit numbers, if the trailing digit matches (like "91" vs "81"),
	// give a reasonable score since ATC often uses trailing digits to disambiguate.
	if len(built) == 2 && len(expectedNum) == 2 && built[1] == expectedNum[1] {
		return false, consumed, 0.7
	}

	return false, 0, 0
}

// findExactSpokenKeyMatch checks if tokens exactly spell out one candidate's SpokenKey.
func findExactSpokenKeyMatch(tokens []Token, candidates []callsignCandidate) *callsignCandidate {
	var match *callsignCandidate

	for i := range candidates {
		c := &candidates[i]
		totalTokens := c.totalConsumed()
		if totalTokens > len(tokens) {
			continue
		}

		// Build the phrase from tokens
		parts := make([]string, totalTokens)
		for j := 0; j < totalTokens; j++ {
			parts[j] = tokens[j].Text
		}
		phrase := strings.Join(parts, " ")

		if strings.EqualFold(phrase, c.spokenKey) {
			if match != nil {
				// Multiple matches, can't disambiguate
				return nil
			}
			match = c
		}
	}

	return match
}

// bestByScore returns the candidate with the highest score.
// Score = 2 for exact airline + 2 for exact flight + flightScore
// Tie-breaks alphabetically by SpokenKey for determinism.
func bestByScore(candidates []callsignCandidate) callsignCandidate {
	if len(candidates) == 0 {
		return callsignCandidate{}
	}

	best := candidates[0]
	bestScore := candidateScore(best)

	for _, c := range candidates[1:] {
		score := candidateScore(c)
		if score > bestScore || (score == bestScore && c.spokenKey < best.spokenKey) {
			best = c
			bestScore = score
		}
	}

	return best
}

func candidateScore(c callsignCandidate) float64 {
	// Combined scoring: airline score (0-1) + flight score (0-1)
	// Weight them equally for a max score of 2.0
	score := c.airlineScore + c.flightScore
	return score
}

// makeCallsignMatch creates a CallsignMatch from a candidate.
func makeCallsignMatch(c callsignCandidate, skip int, tokens []Token) CallsignMatch {
	consumed := skip + c.totalConsumed()

	// Consume heavy/super suffix if present
	if consumed < len(tokens) {
		suffix := strings.ToLower(tokens[consumed].Text)
		if suffix == "heavy" || suffix == "super" {
			consumed++
		}
	}

	// Combined confidence from airline and flight scores
	// Both scores are 0-1, so average them and scale to 0.6-1.0 range
	combinedScore := (c.airlineScore + c.flightScore) / 2.0
	conf := 0.6 + 0.4*combinedScore

	if skip > 0 {
		conf *= (1.0 - 0.1*float64(skip))
	}

	logLocalStt("  matched: %q (conf=%.2f, consumed=%d, skip=%d)", c.ac.Callsign, conf, consumed, skip)

	return CallsignMatch{
		Callsign:       string(c.ac.Callsign),
		SpokenKey:      c.spokenKey,
		Confidence:     conf,
		Consumed:       consumed,
		AddressingForm: c.ac.AddressingForm,
	}
}

// tryFlightNumberOnlyMatch attempts to match a callsign using just the flight number.
// Used as fallback when airline name is garbled/missing. Only succeeds if the number
// uniquely identifies one aircraft.
func tryFlightNumberOnlyMatch(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	// Scan tokens for numbers, but only in the first few tokens where a callsign would appear.
	// Callsigns are at the start of transmissions and typically consume 2-4 tokens
	// (airline + flight number, possibly with "heavy"/"super").
	// Scanning further would incorrectly match numbers from commands (e.g., altitudes, headings).
	maxScan := min(6, len(tokens))

	for startIdx := range maxScan {
		builtNum, numTokens := collectDigits(tokens[startIdx:maxScan], 0)
		if builtNum == "" {
			continue
		}
		endIdx := startIdx + numTokens

		// Find aircraft whose flight number matches (exact or fuzzy)
		var exactKey string
		var exactAc Aircraft
		var fuzzyKey string
		var fuzzyAc Aircraft
		var fuzzyScore float64
		exactCount, fuzzyCount := 0, 0

		for spokenName, ac := range aircraft {
			flightNum := flightNumber(string(ac.Callsign))
			if flightNum == builtNum {
				exactKey, exactAc = spokenName, ac
				exactCount++
			} else if strings.HasSuffix(builtNum, flightNum) && len(flightNum) >= 2 {
				// Suffix match: "922" contains "22" at end (garbled leading digit)
				fuzzyKey, fuzzyAc, fuzzyScore = spokenName, ac, 0.75
				fuzzyCount++
			} else if strings.Contains(builtNum, flightNum) && len(flightNum) >= 2 {
				// Substring match: "92210" contains "22" (digits merged by normalization)
				fuzzyKey, fuzzyAc, fuzzyScore = spokenName, ac, 0.65
				fuzzyCount++
			} else if len(builtNum) >= 3 && len(flightNum) >= 3 {
				// Try fuzzy match for longer flight numbers (3+ digits)
				if score := JaroWinkler(builtNum, flightNum); score >= 0.8 {
					fuzzyKey, fuzzyAc, fuzzyScore = spokenName, ac, score
					fuzzyCount++
				}
			}
		}

		// Prefer exact match if unique
		if exactCount == 1 {
			logLocalStt("  flight-number-only fallback: %q matches %q (ICAO=%q) exactly",
				builtNum, exactKey, exactAc.Callsign)
			return CallsignMatch{
				Callsign:   string(exactAc.Callsign),
				SpokenKey:  exactKey,
				Confidence: 0.7,
				Consumed:   endIdx,
			}, tokens[endIdx:]
		}

		// Use fuzzy match if unique and no exact matches
		if exactCount == 0 && fuzzyCount == 1 {
			logLocalStt("  flight-number-only fallback: %q fuzzy matches %q (ICAO=%q, score=%.2f)",
				builtNum, fuzzyKey, fuzzyAc.Callsign, fuzzyScore)
			return CallsignMatch{
				Callsign:   string(fuzzyAc.Callsign),
				SpokenKey:  fuzzyKey,
				Confidence: 0.65,
				Consumed:   endIdx,
			}, tokens[endIdx:]
		}
	}

	return CallsignMatch{}, nil
}

// "AAL5936" -> "5936"
// "N123AB" -> "123AB"
func flightNumber(callsign string) string {
	for i, c := range callsign {
		if c >= '0' && c <= '9' {
			return callsign[i:]
		}
	}
	return ""
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
	State               string                     // "departure", "arrival", "cleared approach", "overflight", "vfr flight following"
	ControllerFrequency string                     // Current controller position the aircraft is tuned to
	TrackingController  string                     // Controller tracking this aircraft (from flight plan)
	AddressingForm      sim.CallsignAddressingForm // How this aircraft was addressed (based on which key matched)
	LAHSORunways        []string                   // Runways that intersect the approach runway (for LAHSO matching)
}

// findWeightClassTokenIndex checks the early tokens (callsign region) for "heavy" or "super".
// Returns the index of the corresponding token if found, -1 otherwise.
func findWeightClassTokenIndex(tokens []Token) int {
	// Check first 7 tokens (reasonable callsign region)
	for i := range min(len(tokens), 7) {
		text := strings.ToLower(tokens[i].Text)
		if text == "heavy" || text == "super" {
			return i
		}
	}
	return -1
}

// filterByWeightClass returns only aircraft whose spoken name contains the weight class.
func filterByWeightClass(aircraft map[string]Aircraft, weightClass string) map[string]Aircraft {
	filtered := make(map[string]Aircraft)
	for spokenName, ac := range aircraft {
		if strings.HasSuffix(strings.ToLower(spokenName), strings.ToLower(weightClass)) {
			filtered[spokenName] = ac
		}
	}
	return filtered
}

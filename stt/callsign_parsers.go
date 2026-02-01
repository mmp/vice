package stt

import (
	"strconv"
	"strings"
)

// callsignMatcher is the interface for callsign pattern elements.
type callsignMatcher interface {
	// match attempts to match tokens against aircraft candidates.
	// Returns results with matched aircraft and scores.
	match(ctx *callsignMatchContext) []callsignMatchResult
}

// callsignMatchContext holds state during pattern matching.
type callsignMatchContext struct {
	Tokens     []Token               // All tokens
	TokenPos   int                   // Current position in tokens
	Aircraft   map[string]Aircraft   // All aircraft (may be filtered by weight class)
	Candidates []callsignMatchResult // Candidates from previous matchers (for pipeline)
	Skip       int                   // Number of tokens skipped at start
}

// callsignMatchResult represents a potential callsign match.
type callsignMatchResult struct {
	SpokenKey     string   // Key in the aircraft map
	AC            Aircraft // Matched aircraft
	Consumed      int      // Total tokens consumed so far
	AirlineScore  float64  // Score from airline matching (0-1)
	AirlineExact  bool     // True if airline matched exactly
	AirlineTokens int      // Tokens consumed for airline
	FlightScore   float64  // Score from flight number matching (0-1)
	FlightExact   bool     // True if flight number matched exactly
	FlightTokens  int      // Tokens consumed for flight number
	Skip          int      // Tokens skipped at start
}

// totalScore returns the combined score.
func (r *callsignMatchResult) totalScore() float64 {
	return r.AirlineScore + r.FlightScore
}

// skipMatcher skips 0 to N garbage tokens at the start.
// It doesn't consume tokens itself but sets the context's skip position.
type skipMatcher struct {
	maxSkip int
}

func (m *skipMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	// This matcher just records max skip - actual skipping is done by the engine
	// Return empty to signal this is a configuration matcher
	return nil
}

// airlineMatcher matches airline telephony names against tokens.
type airlineMatcher struct{}

func (m *airlineMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if ctx.TokenPos >= len(ctx.Tokens) {
		return nil
	}

	var results []callsignMatchResult
	for spokenName, ac := range ctx.Aircraft {
		am, ok := tryMatchAirline(ctx.Tokens[ctx.TokenPos:], spokenName, ac)
		if !ok {
			continue
		}
		results = append(results, callsignMatchResult{
			SpokenKey:     am.spokenKey,
			AC:            am.ac,
			Consumed:      ctx.TokenPos + am.airlineTokens,
			AirlineScore:  am.airlineScore,
			AirlineExact:  am.airlineExact,
			AirlineTokens: am.airlineTokens,
			Skip:          ctx.Skip,
		})
	}
	return results
}

// flightMatcher matches flight number digits against airline match candidates.
type flightMatcher struct{}

func (m *flightMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if len(ctx.Candidates) == 0 {
		return nil
	}

	var results []callsignMatchResult
	for _, cand := range ctx.Candidates {
		remaining := ctx.Tokens[cand.Consumed:]
		flightNum := flightNumber(string(cand.AC.Callsign))
		if flightNum == "" {
			continue
		}

		exact, consumed, score := matchFlightNumber(remaining, flightNum)
		if consumed == 0 {
			continue
		}

		result := cand
		result.Consumed += consumed
		result.FlightScore = score
		result.FlightExact = exact
		result.FlightTokens = consumed
		results = append(results, result)
	}
	return results
}

// exactPhraseMatcher matches tokens as an exact phrase against spoken names.
type exactPhraseMatcher struct{}

func (m *exactPhraseMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if ctx.TokenPos >= len(ctx.Tokens) {
		return nil
	}

	toks := ctx.Tokens[ctx.TokenPos:]
	maxPhraseLen := min(len(toks), 8) // Reasonable max for callsigns

	var results []callsignMatchResult

	// Try different phrase lengths (longest first for more specific matches)
	for length := maxPhraseLen; length >= 1; length-- {
		parts := make([]string, length)
		for i := 0; i < length; i++ {
			parts[i] = toks[i].Text
		}
		phrase := strings.Join(parts, " ")

		for spokenName, ac := range ctx.Aircraft {
			// Normalize the spoken name the same way we normalize transcripts
			normalizedSpoken := strings.Join(NormalizeTranscript(spokenName), " ")
			if strings.EqualFold(phrase, normalizedSpoken) {
				results = append(results, callsignMatchResult{
					SpokenKey:    spokenName,
					AC:           ac,
					Consumed:     ctx.TokenPos + length,
					AirlineScore: 1.0,
					AirlineExact: true,
					FlightScore:  1.0,
					FlightExact:  true,
					Skip:         ctx.Skip,
				})
			}
		}

		// If we found exact matches at this length, return them (longest wins)
		if len(results) > 0 {
			return results
		}
	}

	return nil
}

// suffixPhraseMatcher matches tokens as a suffix of GA callsigns.
type suffixPhraseMatcher struct{}

func (m *suffixPhraseMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if ctx.TokenPos >= len(ctx.Tokens) {
		return nil
	}

	toks := ctx.Tokens[ctx.TokenPos:]
	maxPhraseLen := min(len(toks), 8)

	var results []callsignMatchResult

	// Try different phrase lengths (need at least 3 for suffix match)
	for length := maxPhraseLen; length >= 3; length-- {
		parts := make([]string, length)
		for i := 0; i < length; i++ {
			parts[i] = toks[i].Text
		}
		phrase := strings.Join(parts, " ")

		for spokenName, ac := range ctx.Aircraft {
			// Only apply suffix matching to GA callsigns (start with N)
			if !strings.HasPrefix(string(ac.Callsign), "N") {
				continue
			}

			normalizedSpoken := strings.Join(NormalizeTranscript(spokenName), " ")
			if strings.HasSuffix(strings.ToLower(normalizedSpoken), strings.ToLower(phrase)) {
				results = append(results, callsignMatchResult{
					SpokenKey:    spokenName,
					AC:           ac,
					Consumed:     ctx.TokenPos + length,
					AirlineScore: 0.95,
					AirlineExact: false, // Suffix match, not exact
					FlightScore:  0.95,
					FlightExact:  false,
					Skip:         ctx.Skip,
				})
			}
		}

		// If we found suffix matches at this length, return them
		if len(results) > 0 {
			return results
		}
	}

	return nil
}

// gaNovemberMatcher matches "november" + number against GA callsigns.
// Handles abbreviated callsigns like "November one zero zero" -> N9910Z.
type gaNovemberMatcher struct{}

func (m *gaNovemberMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if ctx.TokenPos >= len(ctx.Tokens) {
		return nil
	}

	toks := ctx.Tokens[ctx.TokenPos:]
	if len(toks) < 2 {
		return nil
	}

	// First token must be "november"
	if !strings.EqualFold(toks[0].Text, "november") {
		return nil
	}

	// Second token should be a number
	if toks[1].Type != TokenNumber && !IsDigit(toks[1].Text) {
		return nil
	}
	spokenDigits := toks[1].Text

	var results []callsignMatchResult
	for spokenName, ac := range ctx.Aircraft {
		// Only match GA callsigns (start with N)
		if !strings.HasPrefix(string(ac.Callsign), "N") {
			continue
		}
		// Prefer spoken names starting with "november" since that's what we matched
		if !strings.HasPrefix(strings.ToLower(spokenName), "november") {
			continue
		}

		// Extract digits from callsign (e.g., "N9910Z" -> "9910")
		callsignDigits := flightNumber(string(ac.Callsign))
		if callsignDigits == "" {
			continue
		}

		// Try various fuzzy matches
		var score float64

		// Exact match
		if callsignDigits == spokenDigits {
			score = 1.0
		} else if strings.HasSuffix(callsignDigits, spokenDigits) {
			// Suffix match: "9910" ends with "10" - spoken "10" matches
			score = 0.85
		} else if strings.Contains(callsignDigits, spokenDigits) {
			// Substring match
			score = 0.75
		} else if len(spokenDigits) >= 2 && len(callsignDigits) >= 2 {
			// Fuzzy: check if trailing digits match
			// "100" -> last 2 chars "00" vs "9910" -> last 2 chars "10"
			// Or "100" -> first 2 chars "10" vs "9910" -> contains "10"? Yes
			if strings.Contains(callsignDigits, spokenDigits[:len(spokenDigits)-1]) {
				// "100"[:2] = "10", "9910" contains "10" - good match
				score = 0.7
			} else {
				// JW similarity
				jw := JaroWinkler(spokenDigits, callsignDigits)
				if jw >= 0.65 {
					score = jw * 0.9 // Reduce since it's fuzzy
				}
			}
		}

		if score > 0 {
			results = append(results, callsignMatchResult{
				SpokenKey:    spokenName,
				AC:           ac,
				Consumed:     ctx.TokenPos + 2, // "november" + number
				AirlineScore: 0.8,              // Nominal for "november"
				FlightScore:  score,
				FlightExact:  score == 1.0,
				FlightTokens: 1,
				Skip:         ctx.Skip,
			})
		}
	}

	return results
}

// flightOnlyMatcher matches flight numbers against all aircraft (not just candidates).
// Used for the flight-only fallback pattern when airline is garbled.
type flightOnlyMatcher struct{}

func (m *flightOnlyMatcher) match(ctx *callsignMatchContext) []callsignMatchResult {
	if ctx.TokenPos >= len(ctx.Tokens) {
		return nil
	}

	var results []callsignMatchResult
	toks := ctx.Tokens[ctx.TokenPos:]

	// Scan for numbers in first few tokens (keep tight since this is last-resort)
	maxScan := min(3, len(toks))
	for startIdx := range maxScan {
		builtNum, numTokens := collectDigits(toks[startIdx:maxScan], 0)
		if builtNum == "" {
			continue
		}

		// Find aircraft whose flight number matches
		for spokenName, ac := range ctx.Aircraft {
			flightNum := flightNumber(string(ac.Callsign))
			if flightNum == "" {
				continue
			}

			var score float64
			var exact bool

			if flightNum == builtNum {
				exact = true
				score = 1.0
			} else if strings.HasSuffix(builtNum, flightNum) && len(flightNum) >= 2 {
				// Suffix match: "922" contains "22" at end
				score = 0.75
			} else if strings.Contains(builtNum, flightNum) && len(flightNum) >= 2 {
				// Substring match
				score = 0.65
			} else if len(builtNum) >= 3 && len(flightNum) >= 3 {
				// Fuzzy match
				if jwScore := JaroWinkler(builtNum, flightNum); jwScore >= 0.8 {
					score = jwScore
				}
			}

			if score > 0 {
				results = append(results, callsignMatchResult{
					SpokenKey:    spokenName,
					AC:           ac,
					Consumed:     ctx.TokenPos + startIdx + numTokens,
					AirlineScore: 0.5, // Nominal score
					FlightScore:  score,
					FlightExact:  exact,
					FlightTokens: numTokens,
					Skip:         ctx.Skip,
				})
			}
		}

		// Only use first number position found
		if len(results) > 0 {
			break
		}
	}

	return results
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

		// Try concatenating two tokens (handles "jet blue" -> "jetblue")
		// Check this first since it consumes more tokens and may match better
		if len(tokens) >= 2 {
			concat := tokenText + strings.ToLower(tokens[1].Text)
			exact2, score2 := matchWord(concat, parts[0])
			// Use concatenated form if it matches better (prefer consuming more tokens)
			if score2 >= 0.85 && score2 > score {
				return airlineMatch{spokenName, ac, 2, exact2, score2}, true
			}
		}

		// Fall back to single-token match
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

	// Multi-word: try first word only (e.g., "southwest")
	// Handles cases like "Southwest seven 95" where "seven" is part of flight number
	// Try this BEFORE concatenated form to get higher scores
	exact, score := matchWord(tokenText, parts[0])
	if score >= 0.4 {
		return airlineMatch{spokenName, ac, 1, exact, score}, true
	}

	// Multi-word: try concatenated form (e.g., "airfrance")
	// This is a fallback when first word doesn't match well
	concat := strings.Join(parts, "")
	if exact, score := matchWord(tokenText, concat); score >= 0.85 {
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

// airlineMatch represents an aircraft that matched on airline name.
// Used internally during matching.
type airlineMatch struct {
	spokenKey     string
	ac            Aircraft
	airlineTokens int     // tokens consumed for airline
	airlineExact  bool    // true if exact match
	airlineScore  float64 // JW similarity score for airline (1.0 for exact)
}

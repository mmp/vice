package stt

import (
	"strconv"
	"strings"
)

// patternMatch holds a match result with its pattern context.
type patternMatch struct {
	result  *callsignMatchResult
	pattern CallsignPattern
	match   CallsignMatch
}

// matchCallsignWithPatterns attempts to match tokens to an aircraft callsign using DSL patterns.
// Returns the best match and remaining tokens after the callsign.
func matchCallsignWithPatterns(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	logLocalStt("matchCallsignWithPatterns: %d tokens, %d aircraft", len(tokens), len(aircraft))
	if len(tokens) == 0 || len(aircraft) == 0 {
		return CallsignMatch{}, tokens
	}

	// Collect candidates from all patterns
	var allMatches []patternMatch
	patterns := sortedCallsignPatterns()

	for _, pattern := range patterns {
		// Apply condition to filter aircraft if present
		filteredAircraft := filterAircraftByCondition(aircraft, pattern.Condition)
		if len(filteredAircraft) == 0 {
			continue
		}

		// Try to match this pattern
		results := matchPattern(pattern, tokens, filteredAircraft)
		if len(results) == 0 {
			logLocalStt("  pattern %q: no results", pattern.Name)
			continue
		}
		logLocalStt("  pattern %q: %d results", pattern.Name, len(results))
		for i, r := range results {
			if i < 5 { // Limit to first 5
				logLocalStt("    result %d: %q (airline=%.3f, flight=%.3f, skip=%d)",
					i, r.SpokenKey, r.AirlineScore, r.FlightScore, r.Skip)
			}
		}

		// Select the best candidate from this pattern
		selected := selectCandidate(results, pattern, tokens)
		if selected == nil {
			continue
		}
		logLocalStt("    pattern %q: selected %q (airline=%.2f, flight=%.2f, skip=%d)",
			pattern.Name, selected.SpokenKey, selected.AirlineScore, selected.FlightScore, selected.Skip)

		// Apply scoring function first if present (it may modify scores)
		if pattern.ScoringFunc != nil {
			conf := pattern.ScoringFunc(selected)
			if conf == 0 {
				logLocalStt("    pattern %q: scoring rejected %q", pattern.Name, selected.SpokenKey)
				continue // Scoring function rejected this match
			}
		}

		// Check minimum score (after scoring function has set FlightScore for airline_only)
		if pattern.MinScore > 0 && selected.totalScore() < pattern.MinScore {
			logLocalStt("    pattern %q: below MinScore (%.2f < %.2f) for %q",
				pattern.Name, selected.totalScore(), pattern.MinScore, selected.SpokenKey)
			continue
		}

		// Build result and add to candidates
		match := buildCallsignMatch(selected, pattern, tokens)
		allMatches = append(allMatches, patternMatch{
			result:  selected,
			pattern: pattern,
			match:   match,
		})
	}

	if len(allMatches) == 0 {
		return CallsignMatch{}, tokens
	}

	// Select the best match across all patterns
	best := selectBestMatch(allMatches)

	logLocalStt("  pattern %q matched: %q (conf=%.2f, consumed=%d, addressingForm=%d, spokenKey=%q)",
		best.pattern.Name, best.match.Callsign, best.match.Confidence, best.match.Consumed,
		best.match.AddressingForm, best.match.SpokenKey)

	return best.match, tokens[best.match.Consumed:]
}

// selectBestMatch chooses the best match from candidates across all patterns.
func selectBestMatch(matches []patternMatch) patternMatch {
	if len(matches) == 0 {
		return patternMatch{}
	}

	best := matches[0]
	for _, m := range matches[1:] {
		// Prefer higher confidence
		if m.match.Confidence > best.match.Confidence {
			best = m
			continue
		}
		if m.match.Confidence < best.match.Confidence {
			continue
		}
		// Same confidence - prefer higher pattern priority
		if m.pattern.Priority > best.pattern.Priority {
			best = m
			continue
		}
		if m.pattern.Priority < best.pattern.Priority {
			continue
		}
		// Same priority - prefer lower skip (fewer skipped tokens)
		if m.result.Skip < best.result.Skip {
			best = m
			continue
		}
		if m.result.Skip > best.result.Skip {
			continue
		}
		// Same skip - prefer more consumed tokens (more specific match)
		if m.match.Consumed > best.match.Consumed {
			best = m
		}
	}
	return best
}

// matchPattern tries to match a pattern against tokens.
// Returns matching candidates from the first successful skip value.
func matchPattern(pattern CallsignPattern, tokens []Token, aircraft map[string]Aircraft) []callsignMatchResult {
	// Find skip matcher to determine max skip
	maxSkip := 0
	for _, m := range pattern.Matchers {
		if sm, ok := m.(*skipMatcher); ok {
			maxSkip = sm.maxSkip
			break
		}
	}

	// Try with different skip values (0, 1, 2, ..., maxSkip)
	// Return results from the first skip value that produces matches
	for skip := 0; skip <= maxSkip && skip < len(tokens); skip++ {
		ctx := &callsignMatchContext{
			Tokens:     tokens,
			TokenPos:   skip,
			Aircraft:   aircraft,
			Candidates: nil,
			Skip:       skip,
		}

		results := runMatchers(pattern.Matchers, ctx)
		if len(results) > 0 {
			return results
		}
	}

	return nil
}

// runMatchers executes the pattern's matchers in sequence.
func runMatchers(matchers []callsignMatcher, ctx *callsignMatchContext) []callsignMatchResult {
	var results []callsignMatchResult

	for _, m := range matchers {
		// Skip the skip matcher (it's handled by the outer loop)
		if _, ok := m.(*skipMatcher); ok {
			continue
		}

		matcherResults := m.match(ctx)

		// Candidate-generating matchers (start fresh)
		if _, ok := m.(*airlineMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // Airline match required
			}
			ctx.Candidates = matcherResults
			continue
		}
		if _, ok := m.(*exactPhraseMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // Exact phrase match required
			}
			ctx.Candidates = matcherResults
			continue
		}
		if _, ok := m.(*suffixPhraseMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // Suffix phrase match required
			}
			ctx.Candidates = matcherResults
			continue
		}
		if _, ok := m.(*flightOnlyMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // Flight-only match required
			}
			ctx.Candidates = matcherResults
			continue
		}
		if _, ok := m.(*gaNovemberMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // GA november match required
			}
			ctx.Candidates = matcherResults
			continue
		}

		// Filtering matchers (require candidates to exist and match)
		if _, ok := m.(*flightMatcher); ok {
			if len(matcherResults) == 0 {
				return nil // Flight number match required
			}
			ctx.Candidates = matcherResults
			continue
		}

		// Unknown matcher type - just use results if any
		if len(matcherResults) > 0 {
			ctx.Candidates = matcherResults
		}
	}

	results = ctx.Candidates
	return results
}

// selectCandidate chooses the best candidate from multiple matches.
func selectCandidate(candidates []callsignMatchResult, pattern CallsignPattern, tokens []Token) *callsignMatchResult {
	if len(candidates) == 0 {
		return nil
	}

	// If pattern requires unique match, check count
	if pattern.RequireUnique && len(candidates) > 1 {
		// Try to disambiguate by SpokenKey
		if best := findExactSpokenKeyMatch(tokens, candidates); best != nil {
			return best
		}
		// Multiple matches, can't use this pattern
		return nil
	}

	if len(candidates) == 1 {
		return &candidates[0]
	}

	// Multiple candidates - try SpokenKey disambiguation first
	if best := findExactSpokenKeyMatch(tokens, candidates); best != nil {
		return best
	}

	// Try flight hint disambiguation - look for number nearby that might match flight number
	if best := findFlightHintMatch(tokens, candidates); best != nil {
		return best
	}

	// Fall back to best by score
	best := bestByScore(candidates)
	return &best
}

// findFlightHintMatch looks for a number token after the airline match that might
// help disambiguate candidates by matching flight numbers.
func findFlightHintMatch(tokens []Token, candidates []callsignMatchResult) *callsignMatchResult {
	if len(candidates) < 2 {
		return nil
	}

	// Find the best consumed position (where to start looking for flight hints)
	maxConsumed := 0
	for _, c := range candidates {
		if c.Consumed > maxConsumed {
			maxConsumed = c.Consumed
		}
	}

	// Look for number tokens in the next few positions and collect them
	var hintParts []string
	for i := maxConsumed; i < len(tokens) && i < maxConsumed+5; i++ {
		t := tokens[i]
		if t.Type == TokenNumber {
			hintParts = append(hintParts, strconv.Itoa(t.Value))
			continue
		}
		if IsDigit(t.Text) || IsNumber(t.Text) {
			hintParts = append(hintParts, t.Text)
			continue
		}
		// Stop at non-number token (but allow one skip for garbage like "the")
		if len(hintParts) > 0 {
			break
		}
	}

	if len(hintParts) == 0 {
		return nil
	}
	hintNum := strings.Join(hintParts, "")

	// Score candidates by how well hintNum matches their flight number
	var bestMatch *callsignMatchResult
	var bestHintScore float64

	for i := range candidates {
		c := &candidates[i]
		flightNum := flightNumber(string(c.AC.Callsign))
		if flightNum == "" {
			continue
		}

		// Exact match
		if flightNum == hintNum {
			if bestHintScore < 1.0 {
				bestMatch = c
				bestHintScore = 1.0
			}
			continue
		}

		// Suffix match: hintNum is a suffix of flightNum
		if strings.HasSuffix(flightNum, hintNum) {
			score := 0.9
			if score > bestHintScore {
				bestMatch = c
				bestHintScore = score
			}
			continue
		}

		// hintNum contains flightNum
		if strings.Contains(hintNum, flightNum) {
			score := 0.85
			if score > bestHintScore {
				bestMatch = c
				bestHintScore = score
			}
		}
	}

	// Only use flight hint if it strongly discriminates
	if bestHintScore >= 0.85 {
		return bestMatch
	}
	return nil
}

// findExactSpokenKeyMatch checks if tokens exactly spell out one candidate's SpokenKey.
func findExactSpokenKeyMatch(tokens []Token, candidates []callsignMatchResult) *callsignMatchResult {
	var match *callsignMatchResult

	for i := range candidates {
		c := &candidates[i]
		totalTokens := c.Consumed - c.Skip
		if c.Skip+totalTokens > len(tokens) {
			continue
		}

		// Build the phrase from tokens
		parts := make([]string, totalTokens)
		for j := 0; j < totalTokens; j++ {
			parts[j] = tokens[c.Skip+j].Text
		}
		phrase := strings.Join(parts, " ")

		if strings.EqualFold(phrase, c.SpokenKey) {
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
func bestByScore(candidates []callsignMatchResult) callsignMatchResult {
	if len(candidates) == 0 {
		return callsignMatchResult{}
	}

	best := candidates[0]
	bestScore := best.totalScore()

	for _, c := range candidates[1:] {
		score := c.totalScore()
		// Tie-break: prefer lower addressing form (0 = full name is better than 1 = type+trailing3)
		// Then alphabetically by Callsign for determinism
		if score > bestScore {
			best = c
			bestScore = score
		} else if score == bestScore {
			// Prefer addressingForm 0 (full form) over 1 (type+trailing3)
			if c.AC.AddressingForm < best.AC.AddressingForm {
				best = c
			} else if c.AC.AddressingForm == best.AC.AddressingForm && string(c.AC.Callsign) < string(best.AC.Callsign) {
				best = c
			}
		}
	}

	return best
}

// buildCallsignMatch creates a CallsignMatch from a match result.
func buildCallsignMatch(result *callsignMatchResult, pattern CallsignPattern, tokens []Token) CallsignMatch {
	consumed := result.Consumed

	// Consume heavy/super suffix if present
	if consumed < len(tokens) {
		suffix := strings.ToLower(tokens[consumed].Text)
		if suffix == "heavy" || suffix == "super" {
			consumed++
		}
	}

	// Calculate confidence
	var conf float64
	if pattern.FixedConfidence > 0 {
		conf = pattern.FixedConfidence
	} else if pattern.ScoringFunc != nil {
		conf = pattern.ScoringFunc(result)
	} else {
		// Default confidence calculation
		// Both scores are 0-1, so average them and scale to 0.6-1.0 range
		combinedScore := (result.AirlineScore + result.FlightScore) / 2.0
		conf = 0.6 + 0.4*combinedScore
	}

	// Reduce confidence for skipped tokens
	if result.Skip > 0 {
		conf *= (1.0 - 0.1*float64(result.Skip))
	}

	return CallsignMatch{
		Callsign:       string(result.AC.Callsign),
		SpokenKey:      result.SpokenKey,
		Confidence:     conf,
		Consumed:       consumed,
		AddressingForm: result.AC.AddressingForm,
	}
}

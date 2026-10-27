// speech/stt/callsign_engine.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/util"
)

// patternMatch holds a match result with its pattern context.
type patternMatch struct {
	result  *callsignMatchResult
	pattern CallsignPattern
	match   CallsignMatch
}

// callsignCandidates returns all distinct callsign interpretations of the
// leading tokens, best first. The ordering is: match confidence, then
// pattern priority, then fewer skipped tokens, then more tokens consumed.
func callsignCandidates(tokens []Token, aircraft map[string]Aircraft) []CallsignMatch {
	logLocalStt("matchCallsignWithPatterns: %d tokens, %d aircraft", len(tokens), len(aircraft))
	if len(tokens) == 0 || len(aircraft) == 0 {
		return nil
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
		selected := selectCandidate(results, pattern)
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
		return nil
	}

	// An exactly spoken callsign is resolved: fuzzier readings of the
	// transmission are not alternatives to it.
	if exact := util.FilterSlice(allMatches, func(m patternMatch) bool { return m.result.isExact() }); len(exact) > 0 {
		m := slices.MinFunc(exact, func(a, b patternMatch) int { return a.result.Skip - b.result.Skip })
		logLocalStt("  exact callsign %q (spokenKey=%q, skip=%d): resolved", m.match.Callsign, m.match.SpokenKey, m.result.Skip)
		return []CallsignMatch{m.match}
	}

	// Rank matches across all patterns, then keep the best per callsign.
	slices.SortStableFunc(allMatches, func(a, b patternMatch) int {
		if a.match.Confidence != b.match.Confidence {
			if a.match.Confidence > b.match.Confidence {
				return -1
			}
			return 1
		}
		if a.pattern.Priority != b.pattern.Priority {
			return b.pattern.Priority - a.pattern.Priority
		}
		if a.result.Skip != b.result.Skip {
			return a.result.Skip - b.result.Skip
		}
		return b.match.Consumed - a.match.Consumed
	})

	// Keep the best candidate per (callsign, consumed) span: the same
	// aircraft via different spans are real alternatives — command
	// parsability decides between them downstream.
	var cands []CallsignMatch
	seen := make(map[string]bool)
	for _, m := range allMatches {
		key := fmt.Sprintf("%s/%d", m.match.Callsign, m.match.Consumed)
		if seen[key] {
			continue
		}
		seen[key] = true
		cands = append(cands, m.match)
	}

	best := cands[0]
	logLocalStt("  best callsign candidate: %q (conf=%.2f, consumed=%d, addressingForm=%d, spokenKey=%q; %d candidates)",
		best.Callsign, best.Confidence, best.Consumed, best.AddressingForm, best.SpokenKey, len(cands))

	return cands
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
	// Collect results from all skip values so the best can be selected.
	var allResults []callsignMatchResult
	for skip := 0; skip <= maxSkip && skip < len(tokens); skip++ {
		ctx := &callsignMatchContext{
			Tokens:     tokens,
			TokenPos:   skip,
			Aircraft:   aircraft,
			Candidates: nil,
			Skip:       skip,
		}

		allResults = append(allResults, runMatchers(pattern.Matchers, ctx)...)
	}

	return allResults
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
func selectCandidate(candidates []callsignMatchResult, pattern CallsignPattern) *callsignMatchResult {
	if len(candidates) == 0 {
		return nil
	}

	// An exactly spoken callsign settles the match: nothing later in the
	// transmission can outweigh it.
	if exact := util.FilterSlice(candidates, func(c callsignMatchResult) bool { return c.isExact() }); len(exact) > 0 {
		e := slices.MinFunc(exact, func(a, b callsignMatchResult) int { return a.Skip - b.Skip })
		return &e
	}

	// If pattern requires unique match, check count
	if pattern.RequireUnique && len(candidates) > 1 {
		return nil
	}

	if len(candidates) == 1 {
		return &candidates[0]
	}

	best := bestByScore(candidates)
	return &best
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

	// A perfect flight number match is strong evidence of the correct
	// callsign; ensure it isn't dragged below 0.92 by a weak airline
	// score or skip penalty, and that it outranks fuzzy flight matches
	// riding on a good airline score ("Flexjet 798" must not beat
	// "ExecJet 1986" when the transcript said "1986").
	if result.FlightScore >= 1.0 {
		conf = max(conf, 0.92)
	}

	return CallsignMatch{
		Callsign:       string(result.AC.Callsign),
		SpokenKey:      result.SpokenKey,
		Confidence:     conf,
		Consumed:       consumed,
		AddressingForm: result.AC.AddressingForm,
	}
}

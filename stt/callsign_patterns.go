package stt

import (
	"strings"
)

func init() {
	registerAllCallsignPatterns()
}

// registerAllCallsignPatterns registers all callsign matching patterns.
// Weight class filtering is handled separately before pattern matching.
func registerAllCallsignPatterns() {
	// Exact phrase match (highest priority)
	// Matches when tokens exactly match a spoken name in the aircraft map
	RegisterCallsignPattern("{skip:2} {exact_phrase}",
		WithCallsignName("exact_phrase"),
		WithCallsignPriority(100),
		WithCallsignConfidence(1.0),
	)

	// GA suffix match
	// Matches when tokens match the suffix of a GA callsign (starts with "N")
	// e.g., "2 victor romeo" matches end of "november 9 2 2 victor romeo"
	RegisterCallsignPattern("{skip:2} {suffix_phrase}",
		WithCallsignName("ga_suffix"),
		WithCallsignPriority(95),
		WithCallsignConfidence(0.95),
		WithCallsignRequire(func(ac Aircraft) bool {
			return strings.HasPrefix(string(ac.Callsign), "N")
		}),
	)

	// Airline + flight number match
	// The most common pattern: match airline name then flight number
	// Skip:3 handles cases like "stand by ah JetBlue 98" with multiple garbage words
	RegisterCallsignPattern("{skip:3} {airline} {flight}",
		WithCallsignName("airline_flight"),
		WithCallsignPriority(80),
	)

	// Airline-only match (fallback)
	// When flight number is garbled/missing but airline is clear
	// Excludes GA aircraft (N-numbers) since they don't have airline names
	RegisterCallsignPattern("{skip:3} {airline}",
		WithCallsignName("airline_only"),
		WithCallsignPriority(60),
		WithCallsignMinScore(1.3), // Require high airline score (0.8 airline + 0.5 nominal flight)
		WithCallsignScoring(airlineOnlyScoring),
		WithCallsignRequire(func(ac Aircraft) bool {
			return !strings.HasPrefix(string(ac.Callsign), "N")
		}),
	)

	// GA november + number pattern
	// Matches "november" followed by digits against GA callsigns
	// Handles cases like "November one zero zero" matching N9910Z
	RegisterCallsignPattern("{skip:2} {ga_november}",
		WithCallsignName("ga_november"),
		WithCallsignPriority(55),
		WithCallsignConfidence(0.75),
		WithCallsignRequire(func(ac Aircraft) bool {
			return strings.HasPrefix(string(ac.Callsign), "N")
		}),
	)

	// Flight number only (last resort)
	// When airline name is completely garbled but flight number is clear
	// Only accepts exact flight number matches that uniquely identify one aircraft
	// Lower skip (2) since we want the number near the start, not buried in transcript
	RegisterCallsignPattern("{skip:2} {flight_only}",
		WithCallsignName("flight_only"),
		WithCallsignPriority(50),
		WithCallsignScoring(flightOnlyScoring),
	)
}

// airlineOnlyScoring scores airline-only matches.
// Sets a nominal flight score since no flight number was matched.
func airlineOnlyScoring(result *callsignMatchResult) float64 {
	// Require high airline score for airline-only matches
	if result.AirlineScore < 0.7 {
		return 0 // Below threshold
	}
	// Set nominal flight score for confidence calculation
	result.FlightScore = 0.5
	// Combined confidence
	combinedScore := (result.AirlineScore + result.FlightScore) / 2.0
	return 0.6 + 0.4*combinedScore
}

// flightOnlyScoring scores flight-number-only matches.
// Sets a nominal airline score since no airline was matched.
func flightOnlyScoring(result *callsignMatchResult) float64 {
	// Set nominal airline score
	result.AirlineScore = 0.5
	// Lower confidence for flight-only matches
	return 0.7
}

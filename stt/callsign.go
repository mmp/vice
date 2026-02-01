package stt

import (
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
//
// Matching proceeds using a declarative pattern-based approach:
//  1. Weight class filtering - if "heavy"/"super" found, filter aircraft first
//  2. Pattern-based matching - uses DSL patterns in priority order
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

	// Phase 2: Pattern-based matching using DSL
	return matchCallsignWithPatterns(tokens, aircraft)
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

// flightNumber extracts the flight number portion of a callsign.
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

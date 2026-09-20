package stt

import (
	"strconv"
	"strings"
)

// CommandsEquivalent checks if two command strings are equivalent,
// considering altitude-aware flexibility for A/D/C commands.
// For example, "A40" and "D40" are equivalent if the aircraft is above 4000 ft.
func CommandsEquivalent(expected, actual string, aircraft map[string]Aircraft) bool {
	if expected == actual {
		return true
	}

	// Split into callsign and commands
	expectedParts := strings.Fields(expected)
	actualParts := strings.Fields(actual)

	if len(expectedParts) != len(actualParts) {
		return false
	}

	if len(expectedParts) == 0 {
		return true
	}

	// First part is callsign - must match exactly
	if expectedParts[0] != actualParts[0] {
		return false
	}

	callsign := expectedParts[0]

	// Find the aircraft for altitude context.
	// Strip /T suffix for lookup since aircraft map may have either form.
	rawCallsign := strings.TrimSuffix(callsign, "/T")
	var ac Aircraft
	var found bool
	for _, a := range aircraft {
		if strings.TrimSuffix(a.Callsign, "/T") == rawCallsign {
			ac = a
			found = true
			break
		}
	}

	// Compare each command
	for i := 1; i < len(expectedParts); i++ {
		if !commandEquivalent(expectedParts[i], actualParts[i], ac, found) {
			return false
		}
	}

	return true
}

// commandEquivalent checks if two individual commands are equivalent.
func commandEquivalent(expected, actual string, ac Aircraft, hasAircraftContext bool) bool {
	if expected == actual {
		return true
	}

	// Contact tower with and without an explicit frequency are the same
	// instruction (the sim looks up the tower frequency when it is absent).
	if (expected == "TO" || strings.HasPrefix(expected, "TO/")) &&
		(actual == "TO" || strings.HasPrefix(actual, "TO/")) {
		return true
	}

	// A visual-approach clearance with and without the runway direction
	// letter is the same instruction: when the controller says just
	// "runway two two", the sim resolves the bare number to the unique
	// matching runway.
	if strings.HasPrefix(expected, "CVA") && strings.HasPrefix(actual, "CVA") {
		stripDir := func(s string) (string, byte) {
			if n := len(s); n > 0 && (s[n-1] == 'L' || s[n-1] == 'R' || s[n-1] == 'C') {
				return s[:n-1], s[n-1]
			}
			return s, 0
		}
		expBase, expDir := stripDir(expected[3:])
		actBase, actDir := stripDir(actual[3:])
		if expBase == actBase && (expDir == 0 || actDir == 0 || expDir == actDir) {
			return true
		}
	}

	// Check for A/D/C altitude command equivalence; then-sequenced forms
	// (TA/TD/TC) are compared the same way when both are sequenced.
	if hasAircraftContext && len(expected) > 2 && len(actual) > 2 &&
		expected[0] == 'T' && actual[0] == 'T' {
		expected, actual = expected[1:], actual[1:]
	}
	if hasAircraftContext && len(expected) > 1 && len(actual) > 1 {
		expType := expected[0]
		actType := actual[0]
		expAlt := expected[1:]
		actAlt := actual[1:]

		// Both must have the same altitude value
		if expAlt != actAlt {
			return false
		}

		// Check if they're altitude commands (A, D, or C followed by digits)
		if !IsNumber(expAlt) {
			return false
		}

		alt, err := strconv.Atoi(expAlt)
		if err != nil {
			return false
		}
		altFeet := alt * 100

		// A and D are equivalent if aircraft is above target altitude
		if (expType == 'A' && actType == 'D') || (expType == 'D' && actType == 'A') {
			if ac.Altitude > altFeet {
				return true
			}
		}

		// A and C are equivalent if aircraft is below target altitude
		if (expType == 'A' && actType == 'C') || (expType == 'C' && actType == 'A') {
			if ac.Altitude < altFeet {
				return true
			}
		}
	}

	return false
}

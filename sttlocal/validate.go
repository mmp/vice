package sttlocal

import (
	"strconv"
	"strings"
)

// ValidationResult holds the result of command validation.
type ValidationResult struct {
	ValidCommands []string // Commands that passed validation
	Confidence    float64  // Adjusted confidence based on validation
	Errors        []string // Validation error messages (for debugging)
}

// ValidateCommands validates a list of commands against aircraft state.
// Returns filtered commands and adjusted confidence.
func ValidateCommands(commands []string, ac STTAircraft) ValidationResult {
	if len(commands) == 0 {
		return ValidationResult{Confidence: 0}
	}

	var valid []string
	var errors []string
	penalty := 0.0

	for _, cmd := range commands {
		err := validateCommand(cmd, ac)
		if err == "" {
			valid = append(valid, cmd)
		} else {
			errors = append(errors, err)
			penalty += 0.15 // Each invalid command reduces confidence
		}
	}

	// Calculate confidence based on valid ratio and penalties
	validRatio := float64(len(valid)) / float64(len(commands))
	confidence := validRatio * (1.0 - penalty)
	if confidence < 0 {
		confidence = 0
	}

	return ValidationResult{
		ValidCommands: valid,
		Confidence:    confidence,
		Errors:        errors,
	}
}

// validateCommand validates a single command against aircraft state.
// Returns empty string if valid, or an error message if invalid.
func validateCommand(cmd string, ac STTAircraft) string {
	if len(cmd) == 0 {
		return "empty command"
	}

	switch cmd[0] {
	case 'D':
		// D could be Descend (D{ALT}) or Direct (D{FIX})
		if len(cmd) > 1 {
			rest := cmd[1:]
			if isAllDigits(rest) {
				// Descend command - validate altitude
				return validateDescend(rest, ac)
			}
			// Direct to fix - no state validation needed
		}

	case 'C':
		// C could be Climb (C{ALT}), Cleared approach (C{APPR}), or Cross fix
		if len(cmd) > 1 {
			rest := cmd[1:]

			// Check for special cases first
			if strings.HasPrefix(rest, "VS") {
				// CVS - climb via SID
				return validateClimbViaSID(ac)
			}
			if strings.HasPrefix(rest, "AC") {
				// CAC - cancel approach clearance
				return validateCancelApproach(ac)
			}

			// Check if it contains a slash (cross fix command)
			if strings.Contains(rest, "/") {
				// Cross fix command - no special validation
				return ""
			}

			// Check if all digits (altitude) or approach code
			if isAllDigits(rest) {
				return validateClimb(rest, ac)
			}

			// Otherwise it's a cleared approach
			return validateClearedApproach(rest, ac)
		}

	case 'A':
		// A alone = VFR altitude discretion
		// A{ALT} = maintain altitude
		// A{FIX}/C{APPR} = at fix cleared approach
		if len(cmd) == 1 {
			return validateVFRAltitude(ac)
		}
		// A{ALT} or complex - no special validation
		return ""

	case 'E':
		// E could be: EC (expedite climb), ED (expedite descent), or E{APPR} (expect approach)
		if cmd == "EC" {
			// Expedite climb - valid for departures, overflights
			if ac.State == "arrival" || ac.State == "on approach" {
				return "expedite climb unlikely for arrival/approach"
			}
			return ""
		}
		if cmd == "ED" {
			// Expedite descent - valid for arrivals, overflights
			if ac.State == "departure" {
				return "expedite descent unlikely for departure"
			}
			return ""
		}
		// Expect approach (E{APPR})
		if len(cmd) > 1 {
			return validateExpectApproach(cmd[1:], ac)
		}

	case 'T':
		// T could be turn degrees (T{N}L/R), contact tower (TO), or then commands
		if cmd == "TO" {
			return validateContactTower(ac)
		}
		// Turn degrees, TC, TD, TS - no special validation
		return ""

	case 'L', 'R', 'H':
		// Heading commands - generally valid for any state
		return ""

	case 'S':
		// Speed commands
		if cmd == "SA" || cmd == "SH" || cmd == "SS" {
			// Say commands - always valid
			return ""
		}
		if strings.HasPrefix(cmd, "SQ") {
			// Squawk commands - always valid
			return ""
		}
		// S{SPD}, SMIN, SMAX - always valid
		return ""

	case 'F':
		// FC - frequency change
		if cmd == "FC" {
			// Always valid
			return ""
		}

	case 'G':
		// GA - go ahead
		if cmd == "GA" {
			return validateGoAhead(ac)
		}

	case 'I':
		// I - intercept localizer, ID - ident
		return ""
	}

	return ""
}

// Validation helper functions

func validateDescend(altStr string, ac STTAircraft) string {
	alt, err := strconv.Atoi(altStr)
	if err != nil {
		return "invalid altitude format"
	}

	// Convert encoded altitude to feet
	altFeet := alt * 100

	// Descend target must be below current altitude
	if altFeet >= ac.Altitude {
		return "descend target must be below current altitude"
	}

	// Note: We don't block descend for departures because explicit "descend and
	// maintain" commands are unambiguous. If the controller said it clearly,
	// they meant it (e.g., traffic separation). State-based blocking is only
	// appropriate for ambiguous commands that might be misrecognized.

	return ""
}

func validateClimb(altStr string, ac STTAircraft) string {
	alt, err := strconv.Atoi(altStr)
	if err != nil {
		return "invalid altitude format"
	}

	// Convert encoded altitude to feet
	altFeet := alt * 100

	// Climb target must be above current altitude
	if altFeet <= ac.Altitude {
		return "climb target must be above current altitude"
	}

	// Note: We don't block climb for arrivals because explicit "climb and
	// maintain" commands are unambiguous. If the controller said it clearly,
	// they meant it (e.g., traffic separation). State-based blocking is only
	// appropriate for ambiguous commands that might be misrecognized.

	return ""
}

func validateClearedApproach(apprCode string, ac STTAircraft) string {
	// Can only clear approach if assigned_approach is set
	if ac.AssignedApproach == "" {
		return "cannot clear approach without assigned approach (use expect instead)"
	}

	// The approach should match the assigned approach
	if !strings.EqualFold(apprCode, ac.AssignedApproach) {
		// Allow it but it's unusual
	}

	return ""
}

func validateExpectApproach(apprCode string, ac STTAircraft) string {
	// Expect approach is valid when assigned_approach is empty
	// (actually valid in both cases, but more common when empty)

	// Must not be departure or overflight
	if ac.State == "departure" {
		return "departure aircraft cannot expect approach"
	}

	return ""
}

func validateContactTower(ac STTAircraft) string {
	// Contact tower only valid for aircraft on approach
	if ac.State != "on approach" {
		return "contact tower only valid for aircraft on approach"
	}
	return ""
}

func validateClimbViaSID(ac STTAircraft) string {
	// Climb via SID only for departures
	if ac.State != "departure" {
		return "climb via SID only valid for departures"
	}
	return ""
}

func validateCancelApproach(ac STTAircraft) string {
	// Cancel approach only for aircraft on approach
	if ac.State != "on approach" {
		return "cancel approach only valid for aircraft on approach"
	}
	return ""
}

func validateGoAhead(ac STTAircraft) string {
	// Go ahead typically for VFR check-ins
	if ac.State != "vfr flight following" {
		// Not an error, just less common
	}
	return ""
}

func validateVFRAltitude(ac STTAircraft) string {
	// VFR altitude discretion only for VFR
	if ac.State != "vfr flight following" {
		return "altitude discretion only valid for VFR"
	}
	return ""
}

// isAllDigits returns true if string contains only digits.
func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ValidateCommandsForState filters commands based on aircraft state likelihood.
// Returns commands that are appropriate for the current state.
func ValidateCommandsForState(commands []string, state string) []string {
	var filtered []string

	for _, cmd := range commands {
		if isCommandValidForState(cmd, state) {
			filtered = append(filtered, cmd)
		}
	}

	return filtered
}

// isCommandValidForState checks if a command is valid for the given state.
func isCommandValidForState(cmd string, state string) bool {
	if len(cmd) == 0 {
		return false
	}

	switch state {
	case "departure":
		// Departures: climbs, headings, direct, speed, FC, CVS
		// Not: descend, approach clearances, TO
		if cmd[0] == 'D' && len(cmd) > 1 && isAllDigits(cmd[1:]) {
			return false // Descend
		}
		if cmd == "TO" {
			return false
		}
		if cmd[0] == 'E' || (cmd[0] == 'C' && len(cmd) > 1 && !isAllDigits(cmd[1:])) {
			return false // Approach commands
		}

	case "arrival":
		// Arrivals: descend, headings, speed, approach expect/clear, DVS
		// Not typically: climb
		if cmd[0] == 'C' && len(cmd) > 1 && isAllDigits(cmd[1:]) {
			return false // Climb
		}
		if cmd == "TO" {
			return false
		}

	case "on approach":
		// On approach: speed, TO, CAC
		// Not typically: altitude, heading, navigation
		// Allow all but with lower confidence handled elsewhere

	case "vfr flight following":
		// VFR: GA, transponder, RST, A (discretion)
		// Less common: heading, altitude

	case "overflight":
		// Overflights: altitude (either direction), headings, DVS, FC
		// Not: approach, TO
		if cmd == "TO" {
			return false
		}
	}

	return true
}

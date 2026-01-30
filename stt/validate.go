package stt

import (
	"strconv"
	"strings"

	"github.com/mmp/vice/util"
)

// ValidationResult holds the result of command validation.
type ValidationResult struct {
	ValidCommands []string // Commands that passed validation
	Confidence    float64  // Adjusted confidence based on validation
	Errors        []string // Validation error messages (for debugging)
}

// ValidateCommands validates a list of commands against aircraft state.
// Returns filtered commands and adjusted confidence.
func ValidateCommands(commands []string, ac Aircraft) ValidationResult {
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

			// Convert invalid altitude commands to SAYAGAIN/ALTITUDE.
			// When a climb/descend command has an implausible altitude (e.g., climb
			// to 1000 when at 5000), it likely means the altitude was misheard.
			if isAltitudeError(err) {
				valid = append(valid, "SAYAGAIN/ALTITUDE")
			}
		}
	}

	// Filter incompatible command combinations
	valid, combinationErrors := filterIncompatibleCommands(valid)
	errors = append(errors, combinationErrors...)
	penalty += 0.15 * float64(len(combinationErrors))

	// Deduplicate SAYAGAIN commands - keep only first of each type
	valid = deduplicateSayAgainCommands(valid)

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
func validateCommand(cmd string, ac Aircraft) string {
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
		// A{FIX}/I = at fix intercept localizer
		if len(cmd) == 1 {
			return validateVFRAltitude(ac)
		}
		// A{ALT} or complex - no special validation
		return ""

	case 'E':
		// E could be: EC (expedite climb), ED (expedite descent), or E{APPR} (expect approach)
		if cmd == "EC" {
			// Expedite climb - valid for departures, overflights
			if ac.State == "arrival" || ac.State == "cleared approach" {
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
		// FC - frequency change: always allow if clearly heard.
		// The sim handles invalid situations (e.g., "unable" readback).
		return ""

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

func validateDescend(altStr string, ac Aircraft) string {
	alt, err := strconv.Atoi(altStr)
	if err != nil {
		return "invalid altitude format"
	}

	// Convert encoded altitude to feet
	altFeet := alt * 100

	// Descend target must be at or below current altitude
	// Note: equality is allowed as a no-op (controller may re-issue current altitude)
	if altFeet > ac.Altitude {
		return "descend target must be below current altitude"
	}

	// Note: We don't block descend for departures because explicit "descend and
	// maintain" commands are unambiguous. If the controller said it clearly,
	// they meant it (e.g., traffic separation). State-based blocking is only
	// appropriate for ambiguous commands that might be misrecognized.

	return ""
}

func validateClimb(altStr string, ac Aircraft) string {
	alt, err := strconv.Atoi(altStr)
	if err != nil {
		return "invalid altitude format"
	}

	// Convert encoded altitude to feet
	altFeet := alt * 100

	// Climb target must be at or above current altitude
	// Note: equality is allowed as a no-op (controller may re-issue current altitude)
	if altFeet < ac.Altitude {
		return "climb target must be above current altitude"
	}

	// Note: We don't block climb for arrivals because explicit "climb and
	// maintain" commands are unambiguous. If the controller said it clearly,
	// they meant it (e.g., traffic separation). State-based blocking is only
	// appropriate for ambiguous commands that might be misrecognized.

	return ""
}

func validateClearedApproach(_ string, _ Aircraft) string {
	// Note: We allow clearing approach even without a prior "expect" command.
	// If the controller clearly says "cleared ILS runway 22 left", that's
	// unambiguous. The pilot will respond appropriately if they weren't
	// previously told to expect the approach.
	return ""
}

func validateExpectApproach(_ string, ac Aircraft) string {
	// Expect approach is valid when assigned_approach is empty
	// (actually valid in both cases, but more common when empty)

	// Must not be departure or overflight
	if ac.State == "departure" {
		return "departure aircraft cannot expect approach"
	}

	return ""
}

func validateContactTower(_ Aircraft) string {
	// Always allow TO - sim handles "unable" readback for non-arrivals
	return ""
}

func validateClimbViaSID(ac Aircraft) string {
	// Climb via SID only for departures
	if ac.State != "departure" {
		return "climb via SID only valid for departures"
	}
	return ""
}

func validateCancelApproach(ac Aircraft) string {
	// Cancel approach only valid for aircraft cleared for approach
	if ac.State == "cleared approach" {
		return ""
	}
	return "cancel approach only valid for aircraft cleared for approach"
}

func validateGoAhead(_ Aircraft) string {
	// Go ahead is valid for any aircraft state (typically VFR check-ins)
	return ""
}

func validateVFRAltitude(ac Aircraft) string {
	// VFR altitude discretion only for VFR
	if ac.State != "vfr flight following" {
		return "altitude discretion only valid for VFR"
	}
	return ""
}

// filterIncompatibleCommands removes commands that are incompatible with each other.
// For example, heading commands (L/R/H) are incompatible with direct-to-fix commands
// because you can't be cleared direct to a fix and also given a heading.
func filterIncompatibleCommands(commands []string) ([]string, []string) {
	var errors []string

	// Check if there's a direct-to-fix command (D{FIX} or SAYAGAIN/FIX)
	hasDirectFix := false
	for _, cmd := range commands {
		if cmd == "SAYAGAIN/FIX" {
			hasDirectFix = true
			break
		}
		if len(cmd) > 1 && cmd[0] == 'D' && !isAllDigits(cmd[1:]) {
			hasDirectFix = true
			break
		}
	}

	// If there's a direct-to-fix command, filter out heading commands
	if hasDirectFix {
		var filtered []string
		for _, cmd := range commands {
			// Heading commands start with L, R, or H followed by digits
			if len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R' || cmd[0] == 'H') && isAllDigits(cmd[1:]) {
				errors = append(errors, "heading command incompatible with direct-to-fix")
				continue
			}
			filtered = append(filtered, cmd)
		}
		return filtered, errors
	}

	return commands, errors
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

// isAltitudeError returns true if the error indicates an altitude-related issue.
// This helps determine when to convert a failed command to SAYAGAIN/ALTITUDE.
func isAltitudeError(err string) bool {
	return strings.Contains(err, "climb target must be above") ||
		strings.Contains(err, "descend target must be below")
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

	case "cleared approach":
		// Cleared for approach: speed, TO, CAC
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

// deduplicateSayAgainCommands removes duplicate SAYAGAIN commands, keeping only
// the first occurrence of each type. For example, if the commands include
// ["C50", "SAYAGAIN/HEADING", "SAYAGAIN/HEADING"], the output will be
// ["C50", "SAYAGAIN/HEADING"].
func deduplicateSayAgainCommands(commands []string) []string {
	seen := make(map[string]bool)
	return util.FilterSlice(commands, func(cmd string) bool {
		if strings.HasPrefix(cmd, "SAYAGAIN/") {
			if seen[cmd] {
				return false
			}
			seen[cmd] = true
		}
		return true
	})
}

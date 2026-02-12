package stt

import (
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
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

			// Convert invalid speed commands to SAYAGAIN/SPEED.
			// When a speed is way below the aircraft's minimum (e.g., 20 kts for
			// a jet), it likely means the speed was garbled in transcription.
			if isSpeedError(err) {
				valid = append(valid, "SAYAGAIN/SPEED")
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
			if IsNumber(rest) {
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
			if IsNumber(rest) {
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
		if strings.HasPrefix(cmd, "SAYAGAIN") {
			// SAYAGAIN/* commands - mostly valid, but some need state validation.
			// SAYAGAIN/APPROACH should not be issued for departures since they can't expect approaches.
			if cmd == "SAYAGAIN/APPROACH" && ac.State == "departure" {
				return "departure aircraft cannot expect approach"
			}
			return ""
		}
		if cmd == "S" || cmd == "SMIN" || cmd == "SMAX" || cmd == "SPRES" {
			// Cancel speed restriction, slowest practical, max speed, present speed - always valid
			return ""
		}
		// S{SPD} - validate against aircraft performance
		if len(cmd) > 1 {
			return validateSpeed(cmd[1:], ac)
		}
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

	// Descend target must be at or below current altitude (with tolerance).
	// Allow small overshoot because the aircraft may already be descending toward
	// the target and the altimeter reads slightly below it.
	// E.g., aircraft at 3901 ft descending to 4000 ft — essentially at altitude.
	if altFeet > ac.Altitude+200 {
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

	// Climb target must be at or above current altitude (with tolerance).
	// Allow small overshoot because the aircraft may already be climbing toward
	// the target and the altimeter reads slightly above it.
	if altFeet < ac.Altitude-200 {
		return "climb target must be above current altitude"
	}

	// Note: We don't block climb for arrivals because explicit "climb and
	// maintain" commands are unambiguous. If the controller said it clearly,
	// they meant it (e.g., traffic separation). State-based blocking is only
	// appropriate for ambiguous commands that might be misrecognized.

	return ""
}

func validateSpeed(spdStr string, ac Aircraft) string {
	// Speed commands can be "180" or "180/U5DME" (speed until) - extract just the speed
	if idx := strings.Index(spdStr, "/"); idx > 0 {
		spdStr = spdStr[:idx]
	}

	// Handle speed constraint suffixes: "-" (do not exceed) and "+" (at or above)
	spdStr = strings.TrimSuffix(spdStr, "-")
	spdStr = strings.TrimSuffix(spdStr, "+")

	spd, err := strconv.Atoi(spdStr)
	if err != nil {
		return "invalid speed format"
	}

	// Look up aircraft performance to get minimum speed
	if ac.AircraftType != "" && av.DB != nil {
		if perf, ok := av.DB.AircraftPerformance[ac.AircraftType]; ok {
			if minSpeed := perf.Speed.Min; minSpeed > 0 {
				// If speed is less than 75% of aircraft minimum, it's likely garbled.  Allow
				// issuing speeds slightly below what's possible--in that case, pilots will read
				// back "unable".
				if float32(spd) < minSpeed*0.75 {
					return "speed too low for aircraft type"
				}
			}
		}
	}

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

func validateCancelApproach(_ Aircraft) string {
	// Don't validate - if the controller gave this instruction, pass it through.
	// If invalid, the pilot will respond appropriately.
	return ""
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
		if len(cmd) > 1 && cmd[0] == 'D' && !IsNumber(cmd[1:]) {
			hasDirectFix = true
			break
		}
	}

	// Check if there's a cleared approach command (C{approach} but not CVS or CAC)
	hasApproachClearance := false
	for _, cmd := range commands {
		if len(cmd) > 1 && cmd[0] == 'C' && cmd != "CVS" && cmd != "CAC" && !IsNumber(cmd[1:]) {
			// Check it's not a cross-fix command (contains /)
			if !strings.Contains(cmd, "/") {
				hasApproachClearance = true
				break
			}
		}
	}

	var filtered []string
	for _, cmd := range commands {
		// If there's a direct-to-fix command, filter out heading commands
		if hasDirectFix {
			if len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R' || cmd[0] == 'H') && IsNumber(cmd[1:]) {
				errors = append(errors, "heading command incompatible with direct-to-fix")
				continue
			}
		}

		// If there's an approach clearance, filter out standalone intercept localizer (I)
		// The intercept is implicit in the approach clearance
		if hasApproachClearance && cmd == "I" {
			continue
		}

		filtered = append(filtered, cmd)
	}

	return filtered, errors
}

// isAltitudeError returns true if the error indicates an altitude-related issue.
// This helps determine when to convert a failed command to SAYAGAIN/ALTITUDE.
func isAltitudeError(err string) bool {
	return strings.Contains(err, "climb target must be above") ||
		strings.Contains(err, "descend target must be below")
}

// isSpeedError returns true if the error indicates a speed-related issue.
// This helps determine when to convert a failed command to SAYAGAIN/SPEED.
func isSpeedError(err string) bool {
	return strings.Contains(err, "speed too low for aircraft type")
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
		if cmd[0] == 'D' && len(cmd) > 1 && IsNumber(cmd[1:]) {
			return false // Descend
		}
		if cmd == "TO" {
			return false
		}
		if cmd[0] == 'E' || (cmd[0] == 'C' && len(cmd) > 1 && !IsNumber(cmd[1:])) {
			return false // Approach commands
		}

	case "arrival":
		// Arrivals: descend, headings, speed, approach expect/clear, DVS
		// Not typically: climb
		if cmd[0] == 'C' && len(cmd) > 1 && IsNumber(cmd[1:]) {
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

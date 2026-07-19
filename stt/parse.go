package stt

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
)

func removeCombinedApproved(commands []string) []string {
	if len(commands) <= 1 {
		return commands
	}
	return slices.DeleteFunc(commands, func(cmd string) bool { return cmd == "APPROVED" })
}

// convertAltitudeToSpeedIfKnots checks if "knots" appears anywhere in the tokens.
// If "knots" is present but no speed command was parsed, it means the altitude
// command should actually be a speed command (the "knots" wasn't matched to anything).
func convertAltitudeToSpeedIfKnots(tokens []Token, commands []string) []string {
	// Check if "knots" appears anywhere in the transcript
	hasKnots := false
	for _, t := range tokens {
		if strings.ToLower(t.Text) == "knots" {
			hasKnots = true
			break
		}
	}

	if !hasKnots {
		return commands
	}

	// Check if there's already a speed command - if so, "knots" was matched properly
	for _, cmd := range commands {
		if len(cmd) > 0 && cmd[0] == 'S' {
			return commands
		}
	}

	// Convert altitude commands to speed commands
	result := make([]string, len(commands))
	for i, cmd := range commands {
		if len(cmd) > 1 && cmd[0] == 'A' && IsNumber(cmd[1:]) {
			result[i] = "S" + cmd[1:]
			logLocalStt("  converted altitude to speed due to 'knots': %s -> %s", cmd, result[i])
		} else {
			result[i] = cmd
		}
	}
	return result
}

// tryImplicitApproachMatch checks if tokens form an approach reference without an
// explicit "expect" or "cleared" prefix. If so, it infers the command type:
// - If no approach assigned → "E{approach}" (expect approach)
// - If approach assigned and matches what we heard → "C{approach}" (cleared approach)
// - If approach assigned but different → "E{approach}" (expect different approach)
func tryImplicitApproachMatch(tokens []Token, ac Aircraft) (string, int) {
	if len(ac.CandidateApproaches) == 0 {
		return "", 0
	}

	// Try to match an approach reference using type+runway matching
	// Don't use the fallback for implicit matches - those are contextual and shouldn't
	// infer an approach from a mismatched runway number
	appr, _, consumed := matchApproachByTypeAndNumberWithFallback(tokens, ac.CandidateApproaches, ac.AssignedApproach, false)
	if consumed == 0 {
		return "", 0
	}

	// Skip trailing "approach" word if present
	if consumed < len(tokens) && strings.ToLower(tokens[consumed].Text) == "approach" {
		consumed++
	}

	// Determine if this should be "expect" or "cleared" based on assigned approach
	prefix := "E" // Default to expect
	if ac.AssignedApproach != "" && approachMatchesAssigned(appr, ac.AssignedApproach) {
		// Aircraft already has this approach assigned, so hearing it again means cleared
		prefix = "C"
	}

	return prefix + appr, consumed
}

// coalesceAfterFixAltitudes transforms bare altitude commands that follow a
// cross-fix command into after-fix commands. When a controller says "cross FIX
// at ALT1, descend and maintain ALT2" as a single utterance, the parser
// independently matches "CFIX/AALT1" and "DALT2". The bare "DALT2" should
// become "AFIX/DALT2" (after fix, descend and maintain) rather than a direct
// altitude assignment.
func coalesceAfterFixAltitudes(commands []string) []string {
	for i := 0; i+1 < len(commands); i++ {
		fix := extractPlainCrossFixTarget(commands[i])
		if fix == "" {
			continue
		}
		crossAlt := extractCrossAltitude(commands[i])
		if transformed := transformToAfterFix(fix, commands[i+1], crossAlt); transformed != "" {
			logLocalStt("  coalesced after-fix altitude: %s + %s -> %s", commands[i], commands[i+1], transformed)
			commands[i+1] = transformed
			i++ // skip the transformed command
		}
	}
	return commands
}

func extractPlainCrossFixTarget(cmd string) string {
	if !strings.HasPrefix(cmd, "C") {
		return ""
	}
	parts := strings.Split(cmd, "/")
	if len(parts) < 2 {
		return ""
	}
	if _, _, err := av.ParseDistanceDirection(parts[1]); err == nil {
		return ""
	}
	return extractFixFromCrossCommand(cmd)
}

// extractFixFromCrossCommand extracts the fix name from a cross-fix command
// like "CROSLY/A60". Returns "" if the command is not a cross-fix command.
func extractFixFromCrossCommand(cmd string) string {
	if len(cmd) < 2 || cmd[0] != 'C' {
		return ""
	}
	fix, _, found := strings.Cut(cmd[1:], "/")
	if !found || fix == "" {
		return ""
	}
	// Exclude pure-number "fixes" — those are climb commands like "C90"
	if IsNumber(fix) {
		return ""
	}
	return fix
}

// extractCrossAltitude returns the altitude from a cross-fix command like
// "CROSLY/A60" → 60, "CROSLY/A57+" → 57. Returns 0 if no altitude found.
func extractCrossAltitude(cmd string) int {
	parts := strings.Split(cmd, "/")
	for _, part := range parts[1:] {
		if len(part) < 2 || part[0] != 'A' {
			continue
		}
		// Strip trailing +/- modifier
		numStr := part[1:]
		numStr = strings.TrimRight(numStr, "+-")
		if IsNumber(numStr) {
			return ParseNumber(numStr)
		}
	}
	return 0
}

// transformToAfterFix converts a bare altitude command to an after-fix form.
// crossAlt is the altitude from the preceding cross-fix command, used to
// disambiguate bare "maintain" (A) commands into climb vs descend.
// Returns "" if the command is not a bare altitude command.
func transformToAfterFix(fix, cmd string, crossAlt int) string {
	if len(cmd) < 2 {
		return ""
	}

	// Strip "then" prefix if present
	inner := cmd
	if inner[0] == 'T' && len(inner) > 1 {
		inner = inner[1:]
	}

	if len(inner) < 2 {
		return ""
	}

	switch inner[0] {
	case 'D':
		if IsNumber(inner[1:]) {
			return fmt.Sprintf("A%s/D%s", fix, inner[1:])
		}
	case 'C':
		if IsNumber(inner[1:]) {
			return fmt.Sprintf("A%s/C%s", fix, inner[1:])
		}
	case 'A':
		if IsNumber(inner[1:]) {
			alt := ParseNumber(inner[1:])
			if crossAlt > 0 && alt > crossAlt {
				return fmt.Sprintf("A%s/C%s", fix, inner[1:])
			}
			return fmt.Sprintf("A%s/D%s", fix, inner[1:])
		}
	}
	return ""
}

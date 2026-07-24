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

// resolveExpedite replaces the bare-"expedite" marker with EC or ED based on
// the nearest preceding altitude command: a climb yields EC, otherwise ED
// (descend, or the default when no altitude command precedes it).
func resolveExpedite(commands []string) []string {
	for i, cmd := range commands {
		if cmd != "EXPEDITE" {
			continue
		}
		dir := "ED"
		for j := i - 1; j >= 0; j-- {
			c := strings.TrimPrefix(commands[j], "T")
			if len(c) > 1 && (c[0] == 'C' || c[0] == 'D' || c[0] == 'A') && IsNumber(c[1:]) {
				if c[0] == 'C' {
					dir = "EC"
				}
				break
			}
		}
		commands[i] = dir
	}
	return commands
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

// mergeUntilFixMarkers folds "UNTILFIX/{fix}" markers ("maintain that
// until Dennis") into the nearest preceding speed command as an
// until-fix restriction: S180 + UNTILFIX/DNNIS -> S180/UDNNIS. A marker
// with no speed command to modify is dropped.
func mergeUntilFixMarkers(commands []string) []string {
	for i := 0; i < len(commands); i++ {
		fix, ok := strings.CutPrefix(commands[i], "UNTILFIX/")
		if !ok {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if len(commands[j]) > 1 && commands[j][0] == 'S' && IsNumber(commands[j][1:]) {
				logLocalStt("  merged until-fix: %s + %s -> %s/U%s", commands[j], commands[i], commands[j], fix)
				commands[j] += "/U" + fix
				break
			}
		}
		commands = slices.Delete(commands, i, i+1)
		i--
	}
	return commands
}

// dropFixSayAgainBeforeClearance removes a SAYAGAIN/FIX request when the
// same transmission also contains an approach clearance: "direct
// {unrecognized} cleared River Visual" — the clearance defines the
// routing, so asking for the fix again is unhelpful.
func dropFixSayAgainBeforeClearance(commands []string) []string {
	hasClearance := slices.ContainsFunc(commands, func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'C' && cmd != "CVS" && cmd != "CAC" &&
			!IsNumber(cmd[1:]) && !strings.Contains(cmd, "/")
	})
	if !hasClearance {
		return commands
	}
	return slices.DeleteFunc(commands, func(cmd string) bool { return cmd == "SAYAGAIN/FIX" })
}

// resolveGarbledClearanceVerb converts an approach clearance to an
// expect-approach when the aircraft has no assigned approach and the word
// "cleared" was never actually spoken (the clearance verb was recovered
// from a garble like "clicked"). ATC assigns approaches expect-first, so
// with nothing on file a clearance with an unintelligible verb is almost
// certainly the expect leg; an explicit "cleared" is honored as spoken.
func resolveGarbledClearanceVerb(tokens []Token, commands []string, ac Aircraft) []string {
	if ac.AssignedApproach != "" {
		return commands
	}
	for i, cmd := range commands {
		if getCommandCategory(cmd) != "cleared_approach" || cmd == "CVS" || cmd == "CAC" ||
			strings.Contains(cmd, "/") || strings.HasPrefix(cmd, "CSI") {
			continue
		}
		if slices.ContainsFunc(tokens, func(t Token) bool {
			return strings.EqualFold(t.Text, "cleared") || strings.EqualFold(t.Text, "clear")
		}) {
			return commands
		}
		logLocalStt("  garbled clearance verb with no assigned approach: %s -> E%s", cmd, cmd[1:])
		commands[i] = "E" + cmd[1:]
	}
	return commands
}

// dedupeRepeatedHeadings keeps only the last of multiple absolute heading
// commands in one transmission: a controller restating a heading ("right
// heading two seven zero, ah, right heading zero nine zero") is
// correcting the earlier one, not issuing two turns. Sequential turns are
// spoken with "then" and parse as T-prefixed commands, which are left
// alone.
func dedupeRepeatedHeadings(commands []string) []string {
	last := -1
	count := 0
	for i, cmd := range commands {
		if isAbsoluteHeadingCommand(cmd) {
			last = i
			count++
		}
	}
	if count < 2 {
		return commands
	}
	logLocalStt("  repeated heading commands: keeping only %s", commands[last])
	out := commands[:0]
	for i, cmd := range commands {
		if !isAbsoluteHeadingCommand(cmd) || i == last {
			out = append(out, cmd)
		}
	}
	return out
}

// isAbsoluteHeadingCommand reports whether cmd assigns an absolute
// heading (H270, L090, R110) as opposed to a relative turn or a
// direct-to-fix.
func isAbsoluteHeadingCommand(cmd string) bool {
	return len(cmd) >= 2 && (cmd[0] == 'H' || cmd[0] == 'L' || cmd[0] == 'R') && IsNumber(cmd[1:])
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

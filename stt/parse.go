package stt

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
)

// ParseCommands parses tokens into a sequence of commands using the registered command templates.
func ParseCommands(tokens []Token, ac Aircraft) ([]string, float64) {
	logLocalStt("ParseCommands: %d tokens", len(tokens))
	if len(tokens) == 0 {
		return nil, 0
	}

	var commands []string
	var totalConf float64
	pos := 0
	isThen := false
	matchedAny := false                        // Track if any command pattern matched (including empty-command patterns)
	excludeCategories := make(map[string]bool) // Track categories already matched

	for pos < len(tokens) {
		// Check for "then" keyword.
		// Also treat "the" as "then" when followed by a command keyword and we
		// already have at least one command — STT often garbles "then" as "the".
		// Must check BEFORE filler word skip since "the" is a filler word.
		if tokens[pos].Text == "then" || (len(commands) > 0 && tokens[pos].Text == "the" && pos+1 < len(tokens) &&
			(tokens[pos+1].Text == "descend" || tokens[pos+1].Text == "climb" || tokens[pos+1].Text == "maintain")) {
			logLocalStt("  found 'then' (or 'the' as then) at position %d", pos)
			isThen = true
			pos++
			continue
		}

		// Check for "leaving|passing {altitude} ..." pattern BEFORE filler-word skip.
		// "leaving" is a filler word (to prevent fuzzy match with "heading"), but it
		// also starts the conditional LV command. When followed by an altitude, treat
		// it as a command keyword rather than filler.
		if (tokens[pos].Text == "leaving" || tokens[pos].Text == "passing") &&
			pos+1 < len(tokens) && looksLikeAltitude(tokens[pos+1]) {
			logLocalStt("  found %q before altitude at position %d, treating as LV command start", tokens[pos].Text, pos)
			match, newPos := matchCommandNew(tokens, pos, ac, isThen, excludeCategories)
			if newPos > pos {
				logLocalStt("  matched LV command: %q (conf=%.2f, consumed=%d)", match.Command, match.Confidence, newPos-pos)
				matchedAny = true
				if match.Command != "" {
					commands = append(commands, match.Command)
					totalConf += match.Confidence
					if category := getCommandCategory(match.Command); category != "" {
						excludeCategories[category] = true
					}
				}
				pos = newPos
				isThen = false
				continue
			}
		}

		// Skip filler words
		if IsFillerWord(tokens[pos].Text) {
			logLocalStt("  skipping filler word: %q", tokens[pos].Text)
			pos++
			continue
		}

		// Check for "at {altitude}" pattern - implicit "then" trigger
		if tokens[pos].Text == "at" && pos+1 < len(tokens) && looksLikeAltitude(tokens[pos+1]) {
			logLocalStt("  found 'at {altitude}' pattern at position %d, triggering then", pos)
			isThen = true
			pos += 2
			continue
		}

		// Try to match a command
		match, newPos := matchCommandNew(tokens, pos, ac, isThen, excludeCategories)
		if newPos > pos {
			logLocalStt("  matched command: %q (conf=%.2f, consumed=%d, isThen=%v)",
				match.Command, match.Confidence, newPos-pos, isThen)
			matchedAny = true
			if match.Command != "" {
				commands = append(commands, match.Command)
				totalConf += match.Confidence

				// Track this command's category to prevent duplicate types
				if category := getCommandCategory(match.Command); category != "" {
					excludeCategories[category] = true
				}

				// If this is an expect approach command, add the approach's fixes
				// to the aircraft context for subsequent command parsing.
				if strings.HasPrefix(match.Command, "E") && len(match.Command) > 1 {
					approachID := match.Command[1:]
					// Strip LAHSO suffix if present (e.g., "I22L/LAHSO26" -> "I22L")
					if idx := strings.Index(approachID, "/LAHSO"); idx != -1 {
						approachID = approachID[:idx]
					}
					if approachFixes, ok := ac.ApproachFixes[approachID]; ok {
						logLocalStt("  adding %d fixes from approach %s to aircraft context", len(approachFixes), approachID)
						if ac.Fixes == nil {
							ac.Fixes = make(map[string]string)
						}
						for spoken, fix := range approachFixes {
							if _, exists := ac.Fixes[spoken]; !exists {
								ac.Fixes[spoken] = fix
							}
						}
					}
				}
			}
			pos = newPos
			isThen = false
		} else {
			// Before skipping, check if tokens form an implicit approach reference
			// (approach name without "expect" or "cleared" prefix)
			if cmd, consumed := tryImplicitApproachMatch(tokens[pos:], ac); consumed > 0 {
				logLocalStt("  implicit approach match: %q (consumed=%d)", cmd, consumed)
				commands = append(commands, cmd)
				totalConf += 1.0
				pos += consumed
				continue
			}
			logLocalStt("  no match at token[%d]=%q, skipping", pos, tokens[pos].Text)
			pos++
		}
	}

	if len(commands) == 0 {
		if matchedAny {
			// A command pattern matched but produced no output (e.g., "standby
			// for the approach") — return empty commands with positive
			// confidence so the caller doesn't treat this as a failed parse.
			logLocalStt("ParseCommands: matched but no commands to issue")
			return nil, 1
		}
		logLocalStt("ParseCommands: no commands found")
		return nil, 0
	}

	// Post-processing: if "knots" appears in the transcript, convert altitude commands to speed
	commands = convertAltitudeToSpeedIfKnots(tokens, commands)
	commands = coalesceAfterFixAltitudes(commands)
	commandsBeforeApprovalFilter := len(commands)
	commands = removeCombinedApproved(commands)
	totalConf -= float64(commandsBeforeApprovalFilter - len(commands))

	avgConf := totalConf / float64(len(commands))
	logLocalStt("ParseCommands: result=%v (avgConf=%.2f)", commands, avgConf)
	return commands, avgConf
}

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

// matchCommandNew tries to match tokens against registered commands.
// excludeCategories contains command categories that should not be matched
// (because a command of that category was already matched in this transmission).
func matchCommandNew(tokens []Token, startPos int, ac Aircraft, isThen bool, excludeCategories map[string]bool) (CommandMatch, int) {
	var bestMatch CommandMatch
	var bestPriority int
	var bestSayAgain CommandMatch
	var bestSayAgainPriority int

	for _, cmd := range sttCommands {
		match, endPos := tryMatchCommand(tokens, startPos, cmd, ac, isThen)
		consumed := endPos - startPos
		if consumed > 0 {
			// Check if this command's category is excluded
			category := getCommandCategory(match.Command)
			if category != "" && excludeCategories[category] {
				continue
			}

			if match.IsSayAgain {
				if cmd.priority > bestSayAgainPriority || (cmd.priority == bestSayAgainPriority && consumed > bestSayAgain.Consumed) {
					bestSayAgain = match
					bestSayAgainPriority = cmd.priority
				}
			} else {
				if cmd.priority > bestPriority || (cmd.priority == bestPriority && consumed > bestMatch.Consumed) {
					bestMatch = match
					bestPriority = cmd.priority
				}
			}
		}
	}

	if bestMatch.Consumed > 0 {
		return bestMatch, startPos + bestMatch.Consumed
	}
	if bestSayAgain.Consumed > 0 {
		return bestSayAgain, startPos + bestSayAgain.Consumed
	}
	return CommandMatch{}, startPos
}

// tryMatchCommand attempts to match tokens against a single command template.
func tryMatchCommand(tokens []Token, startPos int, cmd sttCommand, ac Aircraft, isThen bool) (CommandMatch, int) {
	pos := startPos
	var values []any
	skipWords := extractSkipWords(cmd.template)

	for i, m := range cmd.matchers {
		// Only allow slack for non-first matchers in the template.
		// The first keyword must match at or near the current position.
		allowSlack := i > 0
		res := m.match(tokens, pos, ac, skipWords, allowSlack)

		if res.consumed == 0 {
			// Non-optional matcher failed
			if !m.isOptional() {
				// Return SAYAGAIN if we've matched enough context and this command
				// is marked for clarification on type parser failure.
				// When the parser has tokens but can't match them, i > 0 suffices.
				// When at end of tokens, require >1 consumed token to avoid false
				// triggers from single stray keywords (e.g., "cleared" alone).
				// Commands with sayAgainMinTokens require more tokens consumed
				// (e.g., "at {fix} cleared {approach}" needs the fix to match).
				minTokens := cmd.sayAgainMinTokens
				if minTokens <= 0 {
					minTokens = 1
				}
				enoughContext := pos-startPos >= minTokens && ((pos < len(tokens) && i > 0) || pos-startPos > 1)
				if res.sayAgain != "" && enoughContext && cmd.sayAgainOnFail {
					return CommandMatch{
						Command:    "SAYAGAIN/" + res.sayAgain,
						Confidence: 0.5,
						Consumed:   pos - startPos,
						IsSayAgain: true,
					}, pos
				}
				return CommandMatch{}, startPos
			}
			// Optional matcher didn't match, that's ok
			if _, ok := m.(*optionalGroupMatcher); ok {
				// Add nil values for optional group parameters
				for _, inner := range m.(*optionalGroupMatcher).matchers {
					if _, isTM := inner.(*typedMatcher); isTM {
						values = append(values, nil)
					}
				}
			}
		} else {
			pos = res.consumed
			if res.value != nil {
				// Handle optional group values (slice of values)
				if vals, ok := res.value.([]any); ok {
					values = append(values, vals...)
				} else {
					values = append(values, res.value)
				}
			}
		}
	}

	// Build command string by invoking handler
	cmdStr := invokeHandler(cmd.handler, values, isThen, cmd.thenVariant)

	return CommandMatch{
		Command:    cmdStr,
		Confidence: 1.0,
		Consumed:   pos - startPos,
		IsThen:     isThen,
	}, pos
}

// invokeHandler calls the handler function with the extracted values.
func invokeHandler(handler any, values []any, isThen bool, thenVariant string) string {
	handlerVal := reflect.ValueOf(handler)
	handlerType := handlerVal.Type()

	// Build argument list
	args := make([]reflect.Value, handlerType.NumIn())
	valueIdx := 0

	for i := 0; i < handlerType.NumIn(); i++ {
		paramType := handlerType.In(i)

		if valueIdx >= len(values) {
			// Missing value - use zero value
			args[i] = reflect.Zero(paramType)
			continue
		}

		val := values[valueIdx]
		valueIdx++

		if val == nil {
			// nil for optional parameters
			args[i] = reflect.Zero(paramType)
		} else if paramType.Kind() == reflect.Ptr {
			// Pointer parameter (optional)
			if val == nil {
				args[i] = reflect.Zero(paramType)
			} else {
				ptr := reflect.New(paramType.Elem())
				ptr.Elem().Set(reflect.ValueOf(val))
				args[i] = ptr
			}
		} else {
			// Regular parameter
			args[i] = reflect.ValueOf(val)
		}
	}

	// Call handler
	results := handlerVal.Call(args)
	cmdStr := results[0].String()

	// Apply then variant if needed
	if isThen && thenVariant != "" && cmdStr != "" {
		// The thenVariant is a format string that transforms the command
		// For simple cases, just prepend "T" to the command
		if !strings.HasPrefix(cmdStr, "T") {
			cmdStr = "T" + cmdStr
		}
	}

	return cmdStr
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

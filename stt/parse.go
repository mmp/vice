package stt

import (
	"fmt"
	"reflect"
	"strings"
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
	excludeCategories := make(map[string]bool) // Track categories already matched

	for pos < len(tokens) {
		// Check for "then" keyword
		if tokens[pos].Text == "then" {
			logLocalStt("  found 'then' keyword at position %d", pos)
			isThen = true
			pos++
			continue
		}

		// Check for "the" followed by descent/climb keywords - STT often garbles "then" as "the"
		// Must check BEFORE filler word skip since "the" is a filler word.
		if len(commands) > 0 && tokens[pos].Text == "the" && pos+1 < len(tokens) {
			nextText := strings.ToLower(tokens[pos+1].Text)
			if nextText == "descend" || nextText == "climb" || nextText == "maintain" {
				logLocalStt("  found 'the' + descent/climb keyword at position %d, treating as 'then'", pos)
				isThen = true
				pos++
				continue
			}
		}

		// Skip filler words
		if IsFillerWord(tokens[pos].Text) {
			logLocalStt("  skipping filler word: %q", tokens[pos].Text)
			pos++
			continue
		}

		// Skip "radar contact" phrase (informational, not a command)
		if strings.ToLower(tokens[pos].Text) == "radar" && pos+1 < len(tokens) &&
			strings.ToLower(tokens[pos+1].Text) == "contact" {
			logLocalStt("  skipping 'radar contact' phrase at position %d", pos)
			pos += 2
			continue
		}

		// Check for "at {altitude}" pattern - implicit "then" trigger
		if tokens[pos].Text == "at" && pos+1 < len(tokens) {
			nextToken := tokens[pos+1]
			if nextToken.Type == TokenAltitude ||
				(nextToken.Type == TokenNumber && nextToken.Value >= 100 && nextToken.Value <= 600) ||
				(nextToken.Type == TokenNumber && nextToken.Value >= 1000 && nextToken.Value <= 60000 && nextToken.Value%100 == 0) {
				logLocalStt("  found 'at {altitude}' pattern at position %d, triggering then", pos)
				isThen = true
				pos += 2
				continue
			}
		}

		// Check for "{altitude} until established" pattern
		if isAltitudeToken(tokens[pos]) && pos+2 < len(tokens) {
			if strings.ToLower(tokens[pos+1].Text) == "until" &&
				FuzzyMatch(tokens[pos+2].Text, "established", 0.8) {
				alt := extractAltitudeValue(tokens[pos])
				if alt > 0 {
					cmd := fmt.Sprintf("A%d", alt)
					logLocalStt("  found '{altitude} until established' pattern: %s", cmd)
					commands = append(commands, cmd)
					totalConf += 1.0
					pos += 3
					// Skip "on the localizer"
					for pos < len(tokens) {
						text := strings.ToLower(tokens[pos].Text)
						if text == "on" || text == "the" || text == "localizer" ||
							text == "glide" || text == "slope" || text == "glideslope" {
							pos++
						} else {
							break
						}
					}
					continue
				}
			}
		}

		// Skip "expect further clearance" phrase
		if tokens[pos].Text == "expect" && pos+2 < len(tokens) {
			if strings.ToLower(tokens[pos+1].Text) == "further" &&
				strings.ToLower(tokens[pos+2].Text) == "clearance" {
				logLocalStt("  skipping 'expect further clearance' at position %d", pos)
				pos += 3
				for pos < len(tokens) {
					if tokens[pos].Type == TokenNumber || IsDigit(tokens[pos].Text) {
						pos++
					} else {
						break
					}
				}
				continue
			}
		}

		// Try to match a command
		match, newPos := matchCommandNew(tokens, pos, ac, isThen, excludeCategories)
		if newPos > pos {
			logLocalStt("  matched command: %q (conf=%.2f, consumed=%d, isThen=%v)",
				match.Command, match.Confidence, newPos-pos, isThen)
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
		logLocalStt("ParseCommands: no commands found")
		return nil, 0
	}

	// Post-processing: if "knots" appears in the transcript, convert altitude commands to speed
	commands = convertAltitudeToSpeedIfKnots(tokens, commands)

	avgConf := totalConf / float64(len(commands))
	logLocalStt("ParseCommands: result=%v (avgConf=%.2f)", commands, avgConf)
	return commands, avgConf
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
				// Return SAYAGAIN if we've matched at least one element (i > 0) and
				// this command is marked as requiring clarification on type parser failure.
				if res.sayAgain != "" && i > 0 && cmd.sayAgainOnFail {
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

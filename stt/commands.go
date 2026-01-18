package stt

import (
	"fmt"
	"strconv"
	"strings"
)

// ArgType specifies the type of argument a command expects.
type ArgType int

const (
	ArgNone ArgType = iota
	ArgAltitude
	ArgHeading
	ArgSpeed
	ArgFix
	ArgApproach
	ArgSquawk
	ArgDegrees
	ArgFixAltitude // Fix followed by altitude (e.g., "cross MERIT at 5000")
	ArgFixSpeed    // Fix followed by speed (e.g., "cross MERIT at 250 knots")
	ArgFixHeading  // Fix followed by heading (e.g., "depart MERIT heading 180")
)

// CommandTemplate defines a pattern for recognizing a command.
type CommandTemplate struct {
	Name        string     // Template name for debugging
	Keywords    [][]string // Required keyword sequences (alternatives within each group)
	ArgType     ArgType    // Type of argument expected
	OutputFmt   string     // Format string for output (e.g., "D%d", "L%03d")
	ThenVariant string     // Output format for "then" variant (e.g., "TD%d")
	Priority    int        // Higher priority wins when multiple match
	SkipWords   []string   // Words to skip during matching
}

// CommandMatch represents a matched command.
type CommandMatch struct {
	Command    string  // The output command string
	Confidence float64 // Match confidence
	Consumed   int     // Tokens consumed
	IsThen     bool    // Whether this is a "then" sequenced command
}

// commandTemplates defines patterns for recognizing ATC commands.
//
// Priority Guidelines:
// When multiple templates match the same tokens, the one with higher priority wins.
// On equal priority, the template that consumes more tokens wins (more specific match).
//
// Priority Levels:
//   - 20: Informational phrases that should block command parsing (e.g., "radar contact")
//   - 15: Highly specific multi-keyword commands (e.g., "climb via sid", "cancel approach clearance")
//   - 12: Compound commands with specific intent (e.g., "present heading", "squawk ident")
//   - 10: Standard commands with keyword sequences (e.g., "descend maintain", "turn left heading")
//   - 8:  Shorter variants of standard commands (e.g., "fly heading", "turn degrees")
//   - 5:  Single-keyword commands (e.g., "descend", "heading")
//   - 2-3: Fallback commands that match broadly (e.g., "maintain" for altitude or speed)
//
// The hierarchy ensures that:
//   - "descend maintain 8000" matches descend_maintain (10), not just descend (5)
//   - "climb via the sid" matches climb_via_sid (15), not climb (5)
//   - "present heading" matches present_heading (12), not heading_only (5)
//   - "turn left heading 270" matches turn_left_heading (10), not heading_only (5)
var commandTemplates = []CommandTemplate{
	// === ALTITUDE COMMANDS ===
	{
		Name:        "descend_maintain",
		Keywords:    [][]string{{"descend", "descended", "descending"}, {"maintain"}},
		ArgType:     ArgAltitude,
		OutputFmt:   "D%d",
		ThenVariant: "TD%d",
		Priority:    10,
		SkipWords:   []string{"and", "to"},
	},
	{
		Name:        "descend",
		Keywords:    [][]string{{"descend", "descended", "descending"}},
		ArgType:     ArgAltitude,
		OutputFmt:   "D%d",
		ThenVariant: "TD%d",
		Priority:    5,
		SkipWords:   []string{"and", "to"},
	},
	{
		Name:        "climb_maintain",
		Keywords:    [][]string{{"climb", "climbed", "climbing"}, {"maintain"}},
		ArgType:     ArgAltitude,
		OutputFmt:   "C%d",
		ThenVariant: "TC%d",
		Priority:    10,
		SkipWords:   []string{"and", "to"},
	},
	{
		Name:        "climb",
		Keywords:    [][]string{{"climb", "climbed", "climbing"}},
		ArgType:     ArgAltitude,
		OutputFmt:   "C%d",
		ThenVariant: "TC%d",
		Priority:    5,
		SkipWords:   []string{"and", "to"},
	},
	{
		Name:      "maintain_altitude",
		Keywords:  [][]string{{"maintain"}},
		ArgType:   ArgAltitude,
		OutputFmt: "A%d",
		Priority:  3, // Lower than climb/descend
		SkipWords: []string{"at"},
	},
	{
		Name:      "expedite_descent",
		Keywords:  [][]string{{"expedite"}, {"descent", "descend", "your"}},
		ArgType:   ArgNone,
		OutputFmt: "ED",
		Priority:  10,
	},
	{
		Name:      "expedite_climb",
		Keywords:  [][]string{{"expedite"}, {"climb", "your"}},
		ArgType:   ArgNone,
		OutputFmt: "EC",
		Priority:  10,
	},
	{
		Name:      "climb_via_sid",
		Keywords:  [][]string{{"climb"}, {"via"}, {"sid", "the"}},
		ArgType:   ArgNone,
		OutputFmt: "CVS",
		Priority:  15,
	},
	{
		Name:      "descend_via_star",
		Keywords:  [][]string{{"descend"}, {"via"}, {"star", "the"}},
		ArgType:   ArgNone,
		OutputFmt: "DVS",
		Priority:  15,
	},
	{
		Name:      "say_altitude",
		Keywords:  [][]string{{"say"}, {"altitude"}},
		ArgType:   ArgNone,
		OutputFmt: "SA",
		Priority:  10,
	},

	// === HEADING COMMANDS ===
	{
		Name:      "turn_left_heading",
		Keywords:  [][]string{{"turn", "left"}, {"left", "heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "L%03d",
		Priority:  10,
		SkipWords: []string{"to"},
	},
	{
		Name:      "turn_right_heading",
		Keywords:  [][]string{{"turn", "right"}, {"right", "heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "R%03d",
		Priority:  10,
		SkipWords: []string{"to"},
	},
	{
		Name:      "fly_heading",
		Keywords:  [][]string{{"fly", "heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "H%03d",
		Priority:  8,
		SkipWords: []string{"heading"},
	},
	{
		Name:      "heading_only",
		Keywords:  [][]string{{"heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "H%03d",
		Priority:  5,
	},
	{
		Name:      "present_heading",
		Keywords:  [][]string{{"present"}, {"heading"}},
		ArgType:   ArgNone,
		OutputFmt: "H",
		Priority:  12,
		SkipWords: []string{"fly"}, // "fly present heading" should match
	},
	{
		Name:      "turn_degrees_left",
		Keywords:  [][]string{{"turn"}},
		ArgType:   ArgDegrees,
		OutputFmt: "T%dL",
		Priority:  8,
		SkipWords: []string{"degrees", "left"},
	},
	{
		Name:      "turn_degrees_right",
		Keywords:  [][]string{{"turn"}},
		ArgType:   ArgDegrees,
		OutputFmt: "T%dR",
		Priority:  8,
		SkipWords: []string{"degrees", "right"},
	},
	{
		Name:      "say_heading",
		Keywords:  [][]string{{"say"}, {"heading"}},
		ArgType:   ArgNone,
		OutputFmt: "SH",
		Priority:  10,
	},

	// === SPEED COMMANDS ===
	{
		Name:        "reduce_speed",
		Keywords:    [][]string{{"reduce", "slow"}},
		ArgType:     ArgSpeed,
		OutputFmt:   "S%d",
		ThenVariant: "TS%d",
		Priority:    10,
		SkipWords:   []string{"speed", "to"},
	},
	{
		Name:        "increase_speed",
		Keywords:    [][]string{{"increase"}},
		ArgType:     ArgSpeed,
		OutputFmt:   "S%d",
		ThenVariant: "TS%d",
		Priority:    10,
		SkipWords:   []string{"speed", "to"},
	},
	{
		Name:        "speed_only",
		Keywords:    [][]string{{"speed"}},
		ArgType:     ArgSpeed,
		OutputFmt:   "S%d",
		ThenVariant: "TS%d",
		Priority:    5,
		SkipWords:   []string{"to"},
	},
	{
		Name:        "maintain_speed",
		Keywords:    [][]string{{"maintain"}},
		ArgType:     ArgSpeed,
		OutputFmt:   "S%d",
		ThenVariant: "TS%d",
		Priority:    2, // Very low - only match if clearly speed context
		SkipWords:   []string{"speed"},
	},
	{
		Name:      "slowest_practical",
		Keywords:  [][]string{{"slowest", "minimum"}, {"practical", "speed"}},
		ArgType:   ArgNone,
		OutputFmt: "SMIN",
		Priority:  12,
	},
	{
		Name:      "maximum_speed",
		Keywords:  [][]string{{"maximum", "best"}, {"forward", "speed"}},
		ArgType:   ArgNone,
		OutputFmt: "SMAX",
		Priority:  12,
	},
	{
		Name:      "say_speed",
		Keywords:  [][]string{{"say"}, {"speed", "airspeed"}},
		ArgType:   ArgNone,
		OutputFmt: "SS",
		Priority:  10,
	},

	// === NAVIGATION COMMANDS ===
	{
		Name:      "direct_fix",
		Keywords:  [][]string{{"direct", "proceed"}},
		ArgType:   ArgFix,
		OutputFmt: "D%s",
		Priority:  10,
		SkipWords: []string{"to"},
	},
	{
		Name:      "cross_fix_altitude",
		Keywords:  [][]string{{"cross"}},
		ArgType:   ArgFixAltitude,
		OutputFmt: "C%s/A%d",
		Priority:  10,
		SkipWords: []string{"at"},
	},
	{
		Name:      "cross_fix_speed",
		Keywords:  [][]string{{"cross"}},
		ArgType:   ArgFixSpeed,
		OutputFmt: "C%s/S%d",
		Priority:  10,
		SkipWords: []string{"at"},
	},
	{
		Name:      "depart_fix_heading",
		Keywords:  [][]string{{"depart"}},
		ArgType:   ArgFixHeading,
		OutputFmt: "D%s/H%03d",
		Priority:  10,
		SkipWords: []string{"heading"},
	},

	// === APPROACH COMMANDS ===
	{
		Name:      "expect_approach",
		Keywords:  [][]string{{"expect", "vectors"}},
		ArgType:   ArgApproach,
		OutputFmt: "E%s",
		Priority:  10,
	},
	{
		Name:      "cleared_approach",
		Keywords:  [][]string{{"cleared"}},
		ArgType:   ArgApproach,
		OutputFmt: "C%s",
		Priority:  8,
		SkipWords: []string{"approach"},
	},
	{
		Name:      "cleared_straight_in",
		Keywords:  [][]string{{"cleared"}, {"straight"}},
		ArgType:   ArgApproach,
		OutputFmt: "CSI%s",
		Priority:  12,
		SkipWords: []string{"in"}, // "in" follows "straight" but shouldn't be a required keyword
	},
	{
		Name:      "cancel_approach",
		Keywords:  [][]string{{"cancel"}, {"approach"}, {"clearance"}},
		ArgType:   ArgNone,
		OutputFmt: "CAC",
		Priority:  15,
	},
	{
		Name:      "intercept_localizer",
		Keywords:  [][]string{{"intercept", "join"}, {"localizer", "the"}},
		ArgType:   ArgNone,
		OutputFmt: "I",
		Priority:  10,
	},

	// === TRANSPONDER COMMANDS ===
	{
		Name:      "squawk_code",
		Keywords:  [][]string{{"squawk"}},
		ArgType:   ArgSquawk,
		OutputFmt: "SQ%s",
		Priority:  10,
		SkipWords: []string{"code"},
	},
	{
		Name:      "squawk_ident",
		Keywords:  [][]string{{"squawk"}, {"ident"}},
		ArgType:   ArgNone,
		OutputFmt: "ID",
		Priority:  12,
	},
	{
		Name:      "ident_only",
		Keywords:  [][]string{{"ident"}},
		ArgType:   ArgNone,
		OutputFmt: "ID",
		Priority:  10,
	},
	{
		Name:      "squawk_standby",
		Keywords:  [][]string{{"squawk"}, {"standby"}},
		ArgType:   ArgNone,
		OutputFmt: "SQS",
		Priority:  12,
	},
	{
		Name:      "squawk_altitude",
		Keywords:  [][]string{{"squawk"}, {"altitude", "mode"}},
		ArgType:   ArgNone,
		OutputFmt: "SQA",
		Priority:  12,
	},

	// === HANDOFF COMMANDS ===
	{
		Name:      "radar_contact_info",
		Keywords:  [][]string{{"radar"}, {"contact"}},
		ArgType:   ArgNone,
		OutputFmt: "", // Empty - informational only, no command
		Priority:  20, // Higher than contact commands
	},
	{
		Name:      "contact_tower",
		Keywords:  [][]string{{"contact"}, {"tower"}},
		ArgType:   ArgNone,
		OutputFmt: "TO",
		Priority:  15,
	},
	{
		Name:      "frequency_change",
		Keywords:  [][]string{{"contact"}},
		ArgType:   ArgNone,
		OutputFmt: "FC",
		Priority:  5,
	},

	// === VFR/MISC COMMANDS ===
	{
		Name:      "go_ahead",
		Keywords:  [][]string{{"go"}, {"ahead"}},
		ArgType:   ArgNone,
		OutputFmt: "GA",
		Priority:  15,
	},
	{
		Name:      "radar_services_terminated",
		Keywords:  [][]string{{"radar"}, {"services"}, {"terminated"}},
		ArgType:   ArgNone,
		OutputFmt: "RST",
		Priority:  15,
	},
	{
		Name:      "resume_own_navigation",
		Keywords:  [][]string{{"resume"}, {"own"}, {"navigation"}},
		ArgType:   ArgNone,
		OutputFmt: "RON",
		Priority:  15,
	},
	{
		Name:      "vfr_altitude_discretion",
		Keywords:  [][]string{{"altitude"}, {"discretion", "your"}},
		ArgType:   ArgNone,
		OutputFmt: "A",
		Priority:  10,
	},
}

// ParseCommands parses tokens into a sequence of commands.
func ParseCommands(tokens []Token, ac Aircraft) ([]string, float64) {
	logLocalStt("ParseCommands: %d tokens", len(tokens))
	if len(tokens) == 0 {
		return nil, 0
	}

	var commands []string
	var totalConf float64
	i := 0
	isThen := false

	for i < len(tokens) {
		// Skip filler words
		if IsFillerWord(tokens[i].Text) {
			logLocalStt("  skipping filler word: %q", tokens[i].Text)
			i++
			continue
		}

		// Check for "then" keyword
		if tokens[i].Text == "then" {
			logLocalStt("  found 'then' keyword at position %d", i)
			isThen = true
			i++
			continue
		}

		// Check for "at {altitude}" pattern - implicit "then" trigger
		// e.g., "at 3000, reduce speed to 180" means "when you reach 3000, reduce speed"
		if tokens[i].Text == "at" && i+1 < len(tokens) {
			nextToken := tokens[i+1]
			// Check if next token is an altitude
			if nextToken.Type == TokenAltitude ||
				(nextToken.Type == TokenNumber && nextToken.Value >= 10 && nextToken.Value <= 600) ||
				(nextToken.Type == TokenNumber && nextToken.Value >= 1000 && nextToken.Value <= 60000 && nextToken.Value%100 == 0) {
				logLocalStt("  found 'at {altitude}' pattern at position %d (alt=%d), triggering then", i, nextToken.Value)
				isThen = true
				i += 2 // Skip "at" and the altitude
				continue
			}
		}

		// Check for "{altitude} until established" pattern (PTAC instruction)
		// e.g., "4000 until established on the localizer" -> A40
		if isAltitudeToken(tokens[i]) && i+2 < len(tokens) {
			if strings.ToLower(tokens[i+1].Text) == "until" &&
				FuzzyMatch(tokens[i+2].Text, "established", 0.8) {
				alt := extractAltitudeValue(tokens[i])
				if alt > 0 {
					cmd := fmt.Sprintf("A%d", alt)
					logLocalStt("  found '{altitude} until established' pattern: %s", cmd)
					commands = append(commands, cmd)
					totalConf += 1.0
					i += 3 // Skip altitude, "until", "established"
					// Skip additional words like "on the localizer"
					for i < len(tokens) {
						text := strings.ToLower(tokens[i].Text)
						if text == "on" || text == "the" || text == "localizer" ||
							text == "glide" || text == "slope" || text == "glideslope" {
							i++
						} else {
							break
						}
					}
					continue
				}
			}
		}

		// Try to match a command
		match, consumed := matchCommand(tokens[i:], ac, isThen)
		if consumed > 0 {
			logLocalStt("  matched command: %q (conf=%.2f, consumed=%d, isThen=%v)",
				match.Command, match.Confidence, consumed, isThen)
			// Skip empty commands (informational phrases like "radar contact")
			if match.Command != "" {
				commands = append(commands, match.Command)
				totalConf += match.Confidence
			}
			i += consumed
			isThen = false // Reset after using
		} else {
			logLocalStt("  no match at token[%d]=%q, skipping", i, tokens[i].Text)
			i++ // Skip unrecognized token
		}
	}

	if len(commands) == 0 {
		logLocalStt("ParseCommands: no commands found")
		return nil, 0
	}

	avgConf := totalConf / float64(len(commands))
	logLocalStt("ParseCommands: result=%v (avgConf=%.2f)", commands, avgConf)
	return commands, avgConf
}

// matchCommand tries to match tokens against all command templates.
func matchCommand(tokens []Token, ac Aircraft, isThen bool) (CommandMatch, int) {
	var bestMatch CommandMatch
	var bestPriority int

	for _, tmpl := range commandTemplates {
		match, consumed := tryMatchTemplate(tokens, tmpl, ac, isThen)
		if consumed > 0 && (tmpl.Priority > bestPriority || (tmpl.Priority == bestPriority && consumed > bestMatch.Consumed)) {
			bestMatch = match
			bestPriority = tmpl.Priority
		}
	}

	return bestMatch, bestMatch.Consumed
}

// tryMatchTemplate attempts to match tokens against a single template.
func tryMatchTemplate(tokens []Token, tmpl CommandTemplate, ac Aircraft, isThen bool) (CommandMatch, int) {
	consumed := 0

	// Match each keyword group in sequence.
	// For each group, we skip over skip words and filler words, then try to
	// match the first significant token against any keyword in that group.
	for _, keywordGroup := range tmpl.Keywords {
		// Skip over skip words and filler words to find the next significant token
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if contains(tmpl.SkipWords, text) || IsFillerWord(text) {
				consumed++
				continue
			}
			break
		}

		// Check if we ran out of tokens
		if consumed >= len(tokens) {
			return CommandMatch{}, 0
		}

		// Try to match the current token against any keyword in the group
		text := strings.ToLower(tokens[consumed].Text)
		matched := false
		for _, kw := range keywordGroup {
			if FuzzyMatch(text, kw, 0.8) {
				matched = true
				consumed++
				break
			}
		}

		if !matched {
			return CommandMatch{}, 0
		}
	}

	// Log when keyword matching succeeds for debugging
	if tmpl.Name == "depart_fix_heading" || tmpl.Name == "direct_fix" {
		var nextTokens []string
		for i := consumed; i < min(consumed+3, len(tokens)); i++ {
			nextTokens = append(nextTokens, tokens[i].Text)
		}
		logLocalStt("  tryMatchTemplate %q: keywords matched, consumed=%d, next tokens=%v",
			tmpl.Name, consumed, nextTokens)
	}

	// Skip any remaining skip words and filler words before extracting argument
	for consumed < len(tokens) {
		text := strings.ToLower(tokens[consumed].Text)
		if contains(tmpl.SkipWords, text) || IsFillerWord(text) {
			consumed++
		} else {
			break
		}
	}

	// Extract argument if needed
	var argStr string
	argConf := 1.0

	switch tmpl.ArgType {
	case ArgNone:
		// No argument needed

	case ArgAltitude:
		alt, altConsumed := extractAltitude(tokens[consumed:])
		if altConsumed == 0 {
			return CommandMatch{}, 0
		}
		// Apply context-aware altitude correction for climb/descend commands
		// This handles cases where Whisper drops trailing zeros (e.g., "one seven oh" -> "17" instead of "170")
		if corrected, ok := shouldCorrectAltitude(tmpl, alt, int(ac.Altitude)); ok {
			logLocalStt("  altitude correction: %d -> %d (aircraft at %.0f ft)", alt, corrected, ac.Altitude)
			alt = corrected
		}
		argStr = strconv.Itoa(alt)
		consumed += altConsumed

	case ArgHeading:
		hdg, hdgConsumed := extractHeading(tokens[consumed:])
		if hdgConsumed == 0 {
			return CommandMatch{}, 0
		}
		argStr = fmt.Sprintf("%03d", hdg)
		consumed += hdgConsumed

	case ArgSpeed:
		spd, spdConsumed := extractSpeed(tokens[consumed:])
		if spdConsumed == 0 {
			return CommandMatch{}, 0
		}
		argStr = strconv.Itoa(spd)
		consumed += spdConsumed

	case ArgFix:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			return CommandMatch{}, 0
		}
		argStr = fix
		argConf = fixConf
		consumed += fixConsumed

	case ArgApproach:
		appr, apprConf, apprConsumed := extractApproach(tokens[consumed:], ac.CandidateApproaches)
		if apprConsumed == 0 {
			return CommandMatch{}, 0
		}
		argStr = appr
		argConf = apprConf
		consumed += apprConsumed

	case ArgSquawk:
		code, sqkConsumed := extractSquawk(tokens[consumed:])
		if sqkConsumed == 0 {
			return CommandMatch{}, 0
		}
		argStr = code
		consumed += sqkConsumed

	case ArgDegrees:
		deg, dir, degConsumed := extractDegrees(tokens[consumed:])
		if degConsumed == 0 {
			return CommandMatch{}, 0
		}
		if dir == "left" {
			argStr = fmt.Sprintf("%dL", deg)
		} else {
			argStr = fmt.Sprintf("%dR", deg)
		}
		consumed += degConsumed

	case ArgFixAltitude:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += fixConsumed
		// Skip "at" and filler words between fix and altitude
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "at" || IsFillerWord(text) {
				consumed++
			} else {
				break
			}
		}
		alt, altConsumed := extractAltitude(tokens[consumed:])
		if altConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += altConsumed
		// Build command directly for compound type
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix, alt)
		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q alt=%d consumed=%d",
			tmpl.Name, cmd, fix, alt, consumed)
		return CommandMatch{
			Command:    cmd,
			Confidence: fixConf,
			Consumed:   consumed,
			IsThen:     isThen,
		}, consumed

	case ArgFixSpeed:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += fixConsumed
		// Skip "at" and filler words between fix and speed
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "at" || IsFillerWord(text) {
				consumed++
			} else {
				break
			}
		}
		spd, spdConsumed := extractSpeed(tokens[consumed:])
		if spdConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += spdConsumed
		// Build command directly for compound type
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix, spd)
		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q spd=%d consumed=%d",
			tmpl.Name, cmd, fix, spd, consumed)
		return CommandMatch{
			Command:    cmd,
			Confidence: fixConf,
			Consumed:   consumed,
			IsThen:     isThen,
		}, consumed

	case ArgFixHeading:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			logLocalStt("  tryMatchTemplate %q: fix extraction failed, tokens=%v fixes=%d",
				tmpl.Name, tokens[consumed:], len(ac.Fixes))
			return CommandMatch{}, 0
		}
		consumed += fixConsumed
		// Skip "heading" and filler words between fix and heading value
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "heading" || IsFillerWord(text) {
				consumed++
			} else {
				break
			}
		}
		hdg, hdgConsumed := extractHeading(tokens[consumed:])
		if hdgConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += hdgConsumed
		// Build command directly for compound type
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix, hdg)
		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q hdg=%d consumed=%d",
			tmpl.Name, cmd, fix, hdg, consumed)
		return CommandMatch{
			Command:    cmd,
			Confidence: fixConf,
			Consumed:   consumed,
			IsThen:     isThen,
		}, consumed
	}

	// Build the command string
	var cmd string
	if isThen && tmpl.ThenVariant != "" {
		if tmpl.ArgType == ArgDegrees {
			cmd = "T" + argStr
		} else if argStr != "" {
			cmd = fmt.Sprintf(tmpl.ThenVariant, mustAtoi(argStr))
		} else {
			// No argument but template might expect one - use ThenVariant only if safe
			if strings.Contains(tmpl.ThenVariant, "%") {
				logLocalStt("WARNING: template %q has ThenVariant=%q with format spec but no arg",
					tmpl.Name, tmpl.ThenVariant)
				return CommandMatch{}, 0
			}
			cmd = tmpl.ThenVariant
		}
	} else {
		if tmpl.ArgType == ArgDegrees {
			cmd = "T" + argStr
		} else if argStr != "" {
			switch tmpl.ArgType {
			case ArgFix, ArgApproach, ArgSquawk:
				cmd = fmt.Sprintf(tmpl.OutputFmt, argStr)
			default:
				cmd = fmt.Sprintf(tmpl.OutputFmt, mustAtoi(argStr))
			}
		} else {
			// No argument - use OutputFmt only if it doesn't have format specifiers
			if strings.Contains(tmpl.OutputFmt, "%") {
				logLocalStt("WARNING: template %q has OutputFmt=%q with format spec but no arg",
					tmpl.Name, tmpl.OutputFmt)
				return CommandMatch{}, 0
			}
			cmd = tmpl.OutputFmt
		}
	}

	logLocalStt("  tryMatchTemplate %q: cmd=%q argStr=%q consumed=%d",
		tmpl.Name, cmd, argStr, consumed)

	return CommandMatch{
		Command:    cmd,
		Confidence: argConf,
		Consumed:   consumed,
		IsThen:     isThen,
	}, consumed
}

// extractAltitude extracts an altitude value from tokens.
func extractAltitude(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	// Check for altitude token
	for i, t := range tokens {
		if i > 3 { // Don't look too far
			break
		}

		// Skip numbers followed by "mile" or "miles" - those are distances, not altitudes
		if i+1 < len(tokens) {
			nextText := strings.ToLower(tokens[i+1].Text)
			if nextText == "mile" || nextText == "miles" {
				continue
			}
		}

		if t.Type == TokenAltitude {
			return t.Value, i + 1
		}
		if t.Type == TokenNumber {
			// Heuristic: if it looks like altitude encoding (2-3 digits, reasonable value)
			// But exclude the speed range (100-400) since those are ambiguous and more likely
			// to be speeds. Real altitudes in that range are typically spoken as "one eight
			// thousand" not "180".
			if t.Value >= 10 && t.Value <= 600 && (t.Value < 100 || t.Value > 400) {
				return t.Value, i + 1
			}
			// Large number might be raw feet
			if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
				return t.Value / 100, i + 1
			}
			// Single digit 1-9 in altitude context means thousands
			// e.g., "descend and maintain niner" -> 9 means 9000 feet = 90 encoded
			if t.Value >= 1 && t.Value <= 9 {
				logLocalStt("  extractAltitude: single digit %d interpreted as %d000 ft (encoded %d)",
					t.Value, t.Value, t.Value*10)
				return t.Value * 10, i + 1
			}
		}
	}

	return 0, 0
}

// extractHeading extracts a heading value (1-360) from tokens.
// Only called after command context has determined a heading is expected.
func extractHeading(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	for i, t := range tokens {
		if i > 3 {
			break
		}
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			return t.Value, i + 1
		}
	}

	return 0, 0
}

// extractSpeed extracts a speed value from tokens.
// Only called after command context has determined a speed is expected.
func extractSpeed(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	for i, t := range tokens {
		if i > 3 {
			break
		}
		if t.Type == TokenNumber && t.Value >= 100 && t.Value <= 400 {
			return t.Value, i + 1
		}
	}

	return 0, 0
}

// extractFix extracts a fix name from tokens by matching against known fixes.
func extractFix(tokens []Token, fixes map[string]string) (string, float64, int) {
	if len(tokens) == 0 {
		logLocalStt("  extractFix: no tokens to match")
		return "", 0, 0
	}
	if len(fixes) == 0 {
		logLocalStt("  extractFix: no fixes in aircraft context, cannot match")
		return "", 0, 0
	}

	logLocalStt("  extractFix: trying to match tokens[0]=%q against %d fixes", tokens[0].Text, len(fixes))

	var bestFix string
	var bestScore float64
	var bestLength int

	// Build candidate phrases (1-3 words)
	for length := min(3, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := 0; i < length; i++ {
			parts = append(parts, tokens[i].Text)
		}
		phrase := strings.Join(parts, " ")

		// Try exact match first - return immediately
		for spokenName, fixID := range fixes {
			if strings.EqualFold(phrase, spokenName) {
				logLocalStt("  extractFix: matched %q -> %q (exact)", phrase, fixID)
				return fixID, 1.0, length
			}
		}

		// Try fuzzy match - find the best one (with length ratio check to prevent over-matching)
		for spokenName, fixID := range fixes {
			// Reject if phrase is much longer than fix name (prevents "pucky heading 180" matching "Pucky")
			lenRatio := float64(len(phrase)) / float64(len(spokenName))
			if lenRatio > 1.5 {
				continue
			}
			score := JaroWinkler(phrase, spokenName)
			if score >= 0.85 && score > bestScore {
				bestFix = fixID
				bestScore = score
				bestLength = length
			}
			if PhoneticMatch(phrase, spokenName) && bestScore < 0.85 {
				bestFix = fixID
				bestScore = 0.85
				bestLength = length
			}
			// Try vowel-normalized comparison for syllable contractions
			// (e.g., "gail" should match "gayel")
			normPhrase := normalizeVowels(phrase)
			normSpoken := normalizeVowels(spokenName)
			if normPhrase != phrase || normSpoken != spokenName {
				normScore := JaroWinkler(normPhrase, normSpoken)
				if normScore >= 0.85 && normScore*0.95 > bestScore {
					bestFix = fixID
					bestScore = normScore * 0.95 // Slight penalty for needing normalization
					bestLength = length
				}
			}
		}
	}

	if bestFix != "" {
		logLocalStt("  extractFix: matched %q -> %q (fuzzy %.2f)", tokens[0].Text, bestFix, bestScore)
		return bestFix, bestScore, bestLength
	}
	logLocalStt("  extractFix: no match found for %q", tokens[0].Text)
	return "", 0, 0
}

// extractApproach extracts an approach from tokens.
func extractApproach(tokens []Token, approaches map[string]string) (string, float64, int) {
	if len(tokens) == 0 || len(approaches) == 0 {
		return "", 0, 0
	}

	var bestAppr string
	var bestScore float64
	var bestLength int

	// Build candidate phrases (1-7 words for approach names, since spoken numbers expand)
	for length := min(7, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := 0; i < length; i++ {
			// Expand numeric tokens to spoken form to match telephony
			// e.g., "22" -> "two two"
			if tokens[i].Type == TokenNumber {
				parts = append(parts, spokenDigits(tokens[i].Value))
			} else {
				parts = append(parts, tokens[i].Text)
			}
		}
		phrase := strings.Join(parts, " ")

		// Try exact match first - return immediately
		for spokenName, apprID := range approaches {
			if strings.EqualFold(phrase, spokenName) {
				return apprID, 1.0, length
			}
		}

		// Try fuzzy match - find the best one
		for spokenName, apprID := range approaches {
			score := JaroWinkler(phrase, spokenName)
			if score >= 0.80 && score > bestScore {
				bestAppr = apprID
				bestScore = score
				bestLength = length
			}
		}
	}

	if bestAppr != "" {
		return bestAppr, bestScore, bestLength
	}
	return "", 0, 0
}

// spokenDigits converts a number to its spoken digit form.
// e.g., 22 -> "two two", 4 -> "four"
func spokenDigits(n int) string {
	digitWords := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "niner"}
	if n < 0 {
		n = -n
	}
	if n == 0 {
		return "zero"
	}
	var parts []string
	for n > 0 {
		parts = append([]string{digitWords[n%10]}, parts...)
		n /= 10
	}
	return strings.Join(parts, " ")
}

// extractSquawk extracts a 4-digit squawk code from tokens.
func extractSquawk(tokens []Token) (string, int) {
	if len(tokens) == 0 {
		return "", 0
	}

	// Try to build 4-digit code from digit tokens
	var code strings.Builder
	consumed := 0

	for consumed < len(tokens) && code.Len() < 4 {
		t := tokens[consumed]
		if IsDigit(t.Text) {
			code.WriteString(t.Text)
			consumed++
		} else if t.Type == TokenNumber && t.Value >= 0 && t.Value <= 7777 {
			code.WriteString(fmt.Sprintf("%04d", t.Value))
			consumed++
			break
		} else {
			break
		}
	}

	if code.Len() == 4 {
		return code.String(), consumed
	}

	return "", 0
}

// extractDegrees extracts a degree turn value and direction.
func extractDegrees(tokens []Token) (int, string, int) {
	if len(tokens) == 0 {
		return 0, "", 0
	}

	var deg int
	var dir string
	consumed := 0

	// Look for number and direction
	for consumed < len(tokens) && consumed < 5 {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		if t.Type == TokenNumber && t.Value > 0 && t.Value <= 180 {
			deg = t.Value
		} else if text == "left" {
			dir = "left"
		} else if text == "right" {
			dir = "right"
		} else if text == "degrees" || text == "degree" {
			// Skip
		}
		consumed++

		if deg > 0 && dir != "" {
			break
		}
	}

	if deg > 0 && dir != "" {
		return deg, dir, consumed
	}

	return 0, "", 0
}

// Helper functions

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func mustAtoi(s string) int {
	// Remove leading zeros for proper formatting
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

// shouldCorrectAltitude checks if an extracted altitude should be multiplied by 10
// to recover a likely dropped trailing zero from transcription errors.
// For example, "one seven oh" might be transcribed as "one seventh at" -> 17 instead of 170.
func shouldCorrectAltitude(tmpl CommandTemplate, alt int, acAltitude int) (int, bool) {
	altFeet := alt * 100
	isClimb := strings.Contains(tmpl.Name, "climb")
	isDescend := strings.Contains(tmpl.Name, "descend")

	if isClimb && altFeet <= acAltitude {
		// Climb but altitude is at or below current - try *10
		correctedFeet := alt * 1000
		if correctedFeet > acAltitude && correctedFeet <= 60000 {
			return alt * 10, true
		}
	}
	if isDescend && altFeet >= acAltitude {
		// Descend but altitude is at or above current - try *10
		correctedFeet := alt * 1000
		if correctedFeet < acAltitude && correctedFeet >= 1000 {
			return alt * 10, true
		}
	}
	return alt, false
}

// isAltitudeToken returns true if the token represents an altitude value.
func isAltitudeToken(t Token) bool {
	if t.Type == TokenAltitude {
		return true
	}
	if t.Type == TokenNumber {
		// Encoded altitude (10-600 means 1000-60000 ft)
		// But exclude speed range (100-400) to avoid ambiguity
		if t.Value >= 10 && t.Value <= 600 && (t.Value < 100 || t.Value > 400) {
			return true
		}
		// Raw feet value
		if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
			return true
		}
	}
	return false
}

// extractAltitudeValue extracts the encoded altitude from a token.
// Returns the altitude in hundreds of feet (e.g., 40 for 4000 ft).
func extractAltitudeValue(t Token) int {
	if t.Type == TokenAltitude {
		return t.Value
	}
	if t.Type == TokenNumber {
		// Already encoded (10-600), excluding speed range (100-400)
		if t.Value >= 10 && t.Value <= 600 && (t.Value < 100 || t.Value > 400) {
			return t.Value
		}
		// Raw feet - convert to encoded
		if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
			return t.Value / 100
		}
	}
	return 0
}

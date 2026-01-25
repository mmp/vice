package stt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
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
	ArgApproachLAHSO // Approach with optional LAHSO (land and hold short)
	ArgSquawk
	ArgDegrees
	ArgFixAltitude  // Fix followed by altitude (e.g., "cross MERIT at 5000")
	ArgFixSpeed     // Fix followed by speed (e.g., "cross MERIT at 250 knots")
	ArgFixHeading   // Fix followed by heading (e.g., "depart MERIT heading 180")
	ArgFixApproach  // Fix followed by approach (e.g., "at FERGI cleared River Visual")
	ArgFixIntercept // Fix followed by intercept localizer (e.g., "at FERGI intercept the localizer")
	ArgSID          // SID name (e.g., "climb via the Kennedy Five")
	ArgSTAR         // STAR name (e.g., "descend via the Camrn Four")
	ArgHold         // Hold with optional parameters (e.g., "hold west of MERIT on the 280 radial, 2 minute legs, left turns")
)

// CommandTemplate defines a pattern for recognizing a command.
type CommandTemplate struct {
	Name            string     // Template name for debugging
	Keywords        [][]string // Required keyword sequences (alternatives within each group)
	ArgType         ArgType    // Type of argument expected
	OutputFmt       string     // Format string for output (e.g., "D%d", "L%03d")
	ThenVariant     string     // Output format for "then" variant (e.g., "TD%d")
	Priority        int        // Higher priority wins when multiple match
	SkipWords       []string   // Words to skip during matching
	SkipNonKeywords bool       // Skip any words between keywords (e.g., "contact [facility] tower")
}

// CommandMatch represents a matched command.
type CommandMatch struct {
	Command    string  // The output command string
	Confidence float64 // Match confidence
	Consumed   int     // Tokens consumed
	IsThen     bool    // Whether this is a "then" sequenced command
	IsSayAgain bool    // True if this is a partial match that needs say-again
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
//
// SAYAGAIN Commands:
// When keywords are matched but the associated value cannot be extracted (e.g., STT transcribed
// "fly heading blark bling five" where the heading is garbled), a SAYAGAIN/TYPE command is
// generated instead. The pilot will read back the valid commands and ask for clarification
// on the missed part. Supported SAYAGAIN types:
//   - SAYAGAIN/HEADING - heading value couldn't be extracted
//   - SAYAGAIN/ALTITUDE - altitude value couldn't be extracted
//   - SAYAGAIN/SPEED - speed value couldn't be extracted
//   - SAYAGAIN/APPROACH - approach name couldn't be matched
//   - SAYAGAIN/TURN - turn degrees couldn't be extracted
//   - SAYAGAIN/SQUAWK - squawk code couldn't be extracted
//   - SAYAGAIN/FIX - fix name couldn't be matched
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
		Keywords:  [][]string{{"climb"}, {"via"}},
		ArgType:   ArgSID,
		OutputFmt: "CVS",
		Priority:  15,
		SkipWords: []string{"the"},
	},
	{
		Name:      "descend_via_star",
		Keywords:  [][]string{{"descend"}, {"via"}},
		ArgType:   ArgSTAR,
		OutputFmt: "DVS",
		Priority:  15,
		SkipWords: []string{"the"},
	},
	{
		Name:      "say_altitude",
		Keywords:  [][]string{{"say"}, {"altitude"}},
		ArgType:   ArgNone,
		OutputFmt: "SA",
		Priority:  10,
	},

	// === HEADING COMMANDS ===
	// Note: turn_left_heading and turn_right_heading REQUIRE "left" or "right" to be
	// explicitly present in the transcript. This prevents transcription errors like
	// "flight heading" (instead of "fly heading") from incorrectly matching as a
	// directional turn command.
	{
		Name:      "turn_left_heading",
		Keywords:  [][]string{{"left"}, {"heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "L%03d",
		Priority:  10,
		SkipWords: []string{"turn", "to"},
	},
	{
		Name:      "turn_right_heading",
		Keywords:  [][]string{{"right"}, {"heading"}},
		ArgType:   ArgHeading,
		OutputFmt: "R%03d",
		Priority:  10,
		SkipWords: []string{"turn", "to"},
	},
	// Turn direction without explicit "heading" keyword
	// Handles "turn left 310", "left 310" patterns
	{
		Name:      "turn_left_only",
		Keywords:  [][]string{{"left"}},
		ArgType:   ArgHeading,
		OutputFmt: "L%03d",
		Priority:  7, // Lower than turn_left_heading (10) to prefer explicit matches
		SkipWords: []string{"turn", "to"},
	},
	{
		Name:      "turn_right_only",
		Keywords:  [][]string{{"right"}},
		ArgType:   ArgHeading,
		OutputFmt: "R%03d",
		Priority:  7, // Lower than turn_right_heading (10) to prefer explicit matches
		SkipWords: []string{"turn", "to"},
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
		Keywords:  [][]string{{"slowest", "minimum"}, {"practical", "speed", "possible"}},
		ArgType:   ArgNone,
		OutputFmt: "SMIN",
		Priority:  12,
		SkipWords: []string{"approach"}, // "minimum approach speed" is valid phrasing
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
	{
		Name:      "cancel_speed_restriction",
		Keywords:  [][]string{{"cancel"}, {"speed"}},
		ArgType:   ArgNone,
		OutputFmt: "S",
		Priority:  10,
		SkipWords: []string{"restrictions", "restriction"},
	},
	{
		Name:      "resume_normal_speed",
		Keywords:  [][]string{{"resume"}, {"normal"}, {"speed"}},
		ArgType:   ArgNone,
		OutputFmt: "S",
		Priority:  12,
	},
	{
		Name:      "final_approach_speed",
		Keywords:  [][]string{{"reduce"}, {"final", "minimum"}, {"approach"}, {"speed"}},
		ArgType:   ArgNone,
		OutputFmt: "SMIN",
		Priority:  15,
		SkipWords: []string{"to"},
	},

	// === NAVIGATION COMMANDS ===
	{
		Name:      "direct_fix",
		Keywords:  [][]string{{"direct", "proceed"}},
		ArgType:   ArgFix,
		OutputFmt: "D%s",
		Priority:  10,
		SkipWords: []string{"to", "at"},
	},
	{
		// "cleared direct <fix>" or "cleared <fix>" - both mean direct to fix
		// This is separate from direct_fix because it needs to skip "direct" after "cleared"
		Name:      "cleared_direct_fix",
		Keywords:  [][]string{{"cleared"}},
		ArgType:   ArgFix,
		OutputFmt: "D%s",
		Priority:  7, // Lower than cleared_approach (8) so approach clearances are preferred
		SkipWords: []string{"to", "at", "direct"},
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
	// Hold commands
	// "hold (direction) of (fix) as published" - direction is ignored, just extract fix
	{
		Name:      "hold_published",
		Keywords:  [][]string{{"hold"}},
		ArgType:   ArgFix,
		OutputFmt: "H%s",
		Priority:  10,
		SkipWords: []string{"north", "south", "east", "west", "northeast", "northwest", "southeast", "southwest", "of", "at", "as", "published"},
	},
	// "hold (direction) of (fix) on the (radial) radial [inbound], (minutes) minute legs, (left/right) turns"
	// This needs special handling via ArgHold to extract multiple parameters
	{
		Name:      "hold_controller_specified",
		Keywords:  [][]string{{"hold"}},
		ArgType:   ArgHold,
		OutputFmt: "H%s", // Format string is filled in by extractHold
		Priority:  15,    // Higher priority than hold_published to try first
		SkipWords: []string{"north", "south", "east", "west", "northeast", "northwest", "southeast", "southwest", "of", "at", "the", "inbound"},
	},

	// === APPROACH COMMANDS ===
	{
		Name:      "at_fix_cleared_approach",
		Keywords:  [][]string{{"at"}},
		ArgType:   ArgFixApproach,
		OutputFmt: "A%s/C%s",
		Priority:  15, // High priority - specific compound command
		SkipWords: []string{"cleared", "clear", "for", "approach"},
	},
	{
		Name:      "at_fix_intercept_localizer",
		Keywords:  [][]string{{"at"}},
		ArgType:   ArgFixIntercept,
		OutputFmt: "A%s/I",
		Priority:  15, // High priority - specific compound command
		SkipWords: []string{"intercept", "join", "the", "localizer", "runway"},
	},
	{
		Name:      "expect_approach",
		Keywords:  [][]string{{"expect", "vectors"}},
		ArgType:   ArgApproachLAHSO,
		OutputFmt: "E%s",
		Priority:  10,
	},
	{
		Name:      "cleared_approach",
		Keywords:  [][]string{{"cleared"}},
		ArgType:   ArgApproach,
		OutputFmt: "C%s",
		Priority:  8,
		SkipWords: []string{"approach", "for"},
	},
	{
		Name:      "clear_to_approach", // Whisper sometimes mistranscribes "cleared" as "clear to" or "clear for"
		Keywords:  [][]string{{"clear"}, {"to", "for"}},
		ArgType:   ArgApproach,
		OutputFmt: "C%s",
		Priority:  8,
		SkipWords: []string{"approach"},
	},
	{
		Name:      "localizer_approach", // "localizer runway X approach" implies cleared approach
		Keywords:  [][]string{{"localizer"}},
		ArgType:   ArgApproach,
		OutputFmt: "C%s",
		Priority:  7, // Lower than cleared_approach so explicit "cleared" is preferred
		SkipWords: []string{"approach", "acquired"},
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
		Name:            "intercept_localizer",
		Keywords:        [][]string{{"intercept", "join", "set"}, {"localizer"}},
		ArgType:         ArgNone,
		OutputFmt:       "I",
		Priority:        10,
		SkipWords:       []string{"work", "going", "gonna", "to", "low", "load", "look", "runway", "left", "right"}, // STT garbage and runway designator before "localizer"
		SkipNonKeywords: true,                                                                                       // Allow runway numbers between "intercept" and "localizer"
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
	{
		Name:      "squawk_on",
		Keywords:  [][]string{{"transponder"}, {"on"}},
		ArgType:   ArgNone,
		OutputFmt: "SQON",
		Priority:  12,
	},
	{
		Name:      "squawk_normal",
		Keywords:  [][]string{{"squawk"}, {"normal"}},
		ArgType:   ArgNone,
		OutputFmt: "SQON",
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
		Name:            "contact_tower",
		Keywords:        [][]string{{"contact"}, {"tower"}},
		ArgType:         ArgNone,
		OutputFmt:       "TO",
		Priority:        15,
		SkipNonKeywords: true, // Allow facility names between "contact" and "tower"
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
		// Note: We require flight levels >= 100 to avoid false positives from noise
		// (small numbers like 55 could be garbled callsign parts, not altitudes)
		if tokens[i].Text == "at" && i+1 < len(tokens) {
			nextToken := tokens[i+1]
			// Check if next token is an altitude (FL100+ or raw feet 1000+)
			if nextToken.Type == TokenAltitude ||
				(nextToken.Type == TokenNumber && nextToken.Value >= 100 && nextToken.Value <= 600) ||
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

		// Skip "expect further clearance" phrase (informational only, not a command)
		// e.g., "hold at MERIT expect further clearance 1230"
		if tokens[i].Text == "expect" && i+2 < len(tokens) {
			if strings.ToLower(tokens[i+1].Text) == "further" &&
				strings.ToLower(tokens[i+2].Text) == "clearance" {
				logLocalStt("  skipping 'expect further clearance' at position %d", i)
				i += 3 // Skip "expect further clearance"
				// Skip any following digits (the time, e.g., "1 2 3 0")
				for i < len(tokens) {
					if tokens[i].Type == TokenNumber || IsDigit(tokens[i].Text) {
						i++
					} else {
						break
					}
				}
				continue
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
// Returns the best match. Full matches are preferred over SAYAGAIN (partial) matches.
// A SAYAGAIN match is only returned if no full match is found.
func matchCommand(tokens []Token, ac Aircraft, isThen bool) (CommandMatch, int) {
	var bestMatch CommandMatch
	var bestPriority int
	var bestSayAgain CommandMatch
	var bestSayAgainPriority int

	for _, tmpl := range commandTemplates {
		match, consumed := tryMatchTemplate(tokens, tmpl, ac, isThen)
		if consumed > 0 {
			if match.IsSayAgain {
				// Track best SAYAGAIN match separately
				if tmpl.Priority > bestSayAgainPriority || (tmpl.Priority == bestSayAgainPriority && consumed > bestSayAgain.Consumed) {
					bestSayAgain = match
					bestSayAgainPriority = tmpl.Priority
				}
			} else {
				// Track best full match
				if tmpl.Priority > bestPriority || (tmpl.Priority == bestPriority && consumed > bestMatch.Consumed) {
					bestMatch = match
					bestPriority = tmpl.Priority
				}
			}
		}
	}

	// Prefer full match over SAYAGAIN match
	if bestMatch.Consumed > 0 {
		return bestMatch, bestMatch.Consumed
	}
	if bestSayAgain.Consumed > 0 {
		logLocalStt("  matchCommand: no full match, using SAYAGAIN: %q", bestSayAgain.Command)
		return bestSayAgain, bestSayAgain.Consumed
	}
	return CommandMatch{}, 0
}

// tryMatchTemplate attempts to match tokens against a single template.
func tryMatchTemplate(tokens []Token, tmpl CommandTemplate, ac Aircraft, isThen bool) (CommandMatch, int) {
	consumed := 0

	// Match each keyword group in sequence.
	// For each group, we skip over skip words and filler words, then try to
	// match the first significant token against any keyword in that group.
	// SkipNonKeywords only applies BETWEEN keyword groups, not before the first one.
	firstKeywordMatched := false
	for _, keywordGroup := range tmpl.Keywords {
		// Skip over skip words and filler words to find the next significant token
		// If SkipNonKeywords is set AND we've already matched the first keyword,
		// also skip any word that doesn't match a keyword
		matched := false
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if slices.Contains(tmpl.SkipWords, text) || IsFillerWord(text) {
				consumed++
				continue
			}

			// Try to match the current token against any keyword in the group
			for _, kw := range keywordGroup {
				if FuzzyMatch(text, kw, 0.8) {
					matched = true
					consumed++
					break
				}
			}

			if matched {
				break
			}

			// If SkipNonKeywords is set and we've already matched the first keyword,
			// skip this non-matching token and continue looking.
			// Don't skip before the first keyword - that would cause templates to
			// consume tokens that belong to earlier commands.
			if tmpl.SkipNonKeywords && firstKeywordMatched {
				consumed++
				continue
			}

			// Otherwise, this is a non-matching significant token - fail
			return CommandMatch{}, 0
		}
		firstKeywordMatched = true

		// Check if we ran out of tokens without matching
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
		if slices.Contains(tmpl.SkipWords, text) || IsFillerWord(text) {
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
		// Special case: frequency_change ("contact") should not match if followed by a
		// command keyword. This prevents misrecognized "radar contact" (where "radar"
		// was dropped) from being interpreted as a frequency change when followed by
		// actual commands like "climb and maintain".
		if tmpl.Name == "frequency_change" && consumed < len(tokens) {
			nextWord := strings.ToLower(tokens[consumed].Text)
			if isCommandKeyword(nextWord) {
				logLocalStt("  frequency_change rejected: followed by command keyword %q", nextWord)
				return CommandMatch{}, 0
			}
		}

	case ArgAltitude:
		alt, altConsumed := extractAltitude(tokens[consumed:])
		if altConsumed == 0 {
			// For climb/descend templates, try extracting flight level values (100-400).
			// These are excluded from regular extractAltitude to avoid conflicts with speeds,
			// but in climb/descend context they're clearly altitudes (e.g., "climb and maintain one one five" = FL115).
			if strings.HasPrefix(tmpl.Name, "climb") || strings.HasPrefix(tmpl.Name, "descend") {
				alt, altConsumed = extractFlightLevelAltitude(tokens[consumed:])
			}
		}
		if altConsumed == 0 {
			// Keywords matched but couldn't extract altitude - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgAltitude, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but altitude extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
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
			// Keywords matched but couldn't extract heading - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgHeading, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but heading extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = fmt.Sprintf("%03d", hdg)
		consumed += hdgConsumed

	case ArgSpeed:
		spd, spdConsumed := extractSpeed(tokens[consumed:])
		if spdConsumed == 0 {
			// Keywords matched but couldn't extract speed - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgSpeed, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but speed extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = strconv.Itoa(spd)
		consumed += spdConsumed

	case ArgFix:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			// Keywords matched but couldn't extract fix - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgFix, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but fix extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = fix
		argConf = fixConf
		consumed += fixConsumed

	case ArgApproach:
		appr, apprConf, apprConsumed := extractApproach(tokens[consumed:], ac.CandidateApproaches)
		if apprConsumed == 0 {
			// Keywords matched but couldn't extract approach - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgApproach, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but approach extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = appr
		argConf = apprConf
		consumed += apprConsumed

	case ArgApproachLAHSO:
		appr, apprConf, apprConsumed := extractApproach(tokens[consumed:], ac.CandidateApproaches)
		if apprConsumed == 0 {
			// Keywords matched but couldn't extract approach - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgApproach, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but approach extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = appr
		argConf = apprConf
		consumed += apprConsumed

		// Look for optional LAHSO (land and hold short) in remaining tokens
		if lahsoRwy, lahsoConsumed := extractLAHSO(tokens[consumed:], ac.LAHSORunways); lahsoConsumed > 0 {
			argStr = appr + "/LAHSO" + lahsoRwy
			consumed += lahsoConsumed
			logLocalStt("  %s: extracted LAHSO runway %s", tmpl.Name, lahsoRwy)
		}

	case ArgSquawk:
		code, sqkConsumed := extractSquawk(tokens[consumed:])
		if sqkConsumed == 0 {
			// Keywords matched but couldn't extract squawk code - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgSquawk, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but squawk extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
			return CommandMatch{}, 0
		}
		argStr = code
		consumed += sqkConsumed

	case ArgSID:
		sidConsumed := extractSID(tokens[consumed:], ac.SID)
		if sidConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += sidConsumed
		// ArgSID doesn't need argStr - the command is just "CVS"

	case ArgSTAR:
		starConsumed := extractSTAR(tokens[consumed:], ac.STAR)
		if starConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += starConsumed
		// ArgSTAR doesn't need argStr - the command is just "DVS"

	case ArgDegrees:
		deg, dir, degConsumed := extractDegrees(tokens[consumed:])
		if degConsumed == 0 {
			// Keywords matched but couldn't extract turn degrees - return SAYAGAIN if appropriate
			if shouldGenerateSayAgain(tokens, consumed) {
				if sayAgainCmd := sayAgainCommandForArg(ArgDegrees, tmpl.Priority); sayAgainCmd != "" {
					logLocalStt("  %s: keywords matched but turn degrees extraction failed, returning %s", tmpl.Name, sayAgainCmd)
					return CommandMatch{Command: sayAgainCmd, Confidence: 0.5, Consumed: consumed, IsSayAgain: true}, consumed
				}
			}
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
		// Skip "at" and filler words, detect "or above" / "or below" modifiers
		altModifier := "" // "+" for at or above, "-" for at or below
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "at" || IsFillerWord(text) {
				consumed++
			} else if text == "or" && consumed+1 < len(tokens) {
				nextText := strings.ToLower(tokens[consumed+1].Text)
				if nextText == "above" {
					altModifier = "+"
					consumed += 2
				} else if nextText == "below" {
					altModifier = "-"
					consumed += 2
				} else {
					break
				}
			} else {
				break
			}
		}
		alt, altConsumed := extractAltitude(tokens[consumed:])
		if altConsumed == 0 {
			return CommandMatch{}, 0
		}
		consumed += altConsumed
		// Build command directly for compound type, with optional modifier
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix, alt) + altModifier
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

	case ArgFixApproach:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			logLocalStt("  tryMatchTemplate %q: fix extraction failed", tmpl.Name)
			return CommandMatch{}, 0
		}
		consumed += fixConsumed
		// Skip "cleared", "clear", "for", "approach" and filler words between fix and approach name
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if slices.Contains(tmpl.SkipWords, text) || IsFillerWord(text) {
				consumed++
			} else {
				break
			}
		}
		appr, apprConf, apprConsumed := extractApproach(tokens[consumed:], ac.CandidateApproaches)
		if apprConsumed == 0 {
			logLocalStt("  tryMatchTemplate %q: approach extraction failed", tmpl.Name)
			return CommandMatch{}, 0
		}
		consumed += apprConsumed
		// Build command directly for compound type
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix, appr)
		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q appr=%q consumed=%d",
			tmpl.Name, cmd, fix, appr, consumed)
		// Use the lower confidence between fix and approach
		conf := fixConf
		if apprConf < conf {
			conf = apprConf
		}
		return CommandMatch{
			Command:    cmd,
			Confidence: conf,
			Consumed:   consumed,
			IsThen:     isThen,
		}, consumed

	case ArgFixIntercept:
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			logLocalStt("  tryMatchTemplate %q: fix extraction failed", tmpl.Name)
			return CommandMatch{}, 0
		}
		consumed += fixConsumed
		// Skip filler words and template skip words (intercept, the, localizer, runway, etc.)
		// Also skip optional runway identifiers (e.g., "2 2 left")
		foundIntercept := false
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if slices.Contains(tmpl.SkipWords, text) || IsFillerWord(text) {
				// Use fuzzy matching for key terms that STT might slightly mangle
				if FuzzyMatch(text, "intercept", 0.8) || FuzzyMatch(text, "join", 0.8) {
					foundIntercept = true
				}
				consumed++
			} else if tokens[consumed].Type == TokenNumber {
				// Skip runway numbers (e.g., "2 2" in "2 2 left localizer")
				consumed++
			} else if text == "left" || text == "right" || text == "center" {
				// Skip runway direction indicators
				consumed++
			} else if FuzzyMatch(text, "localizer", 0.8) {
				// Also accept fuzzy match for localizer
				consumed++
			} else {
				break
			}
		}
		// Require at least "intercept" keyword to be found
		if !foundIntercept {
			logLocalStt("  tryMatchTemplate %q: 'intercept' keyword not found", tmpl.Name)
			return CommandMatch{}, 0
		}
		// Build command with just the fix
		cmd := fmt.Sprintf(tmpl.OutputFmt, fix)
		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q consumed=%d",
			tmpl.Name, cmd, fix, consumed)
		return CommandMatch{
			Command:    cmd,
			Confidence: fixConf,
			Consumed:   consumed,
			IsThen:     isThen,
		}, consumed

	case ArgHold:
		// Extract fix name first
		fix, fixConf, fixConsumed := extractFix(tokens[consumed:], ac.Fixes)
		if fixConsumed == 0 {
			logLocalStt("  tryMatchTemplate %q: fix extraction failed", tmpl.Name)
			return CommandMatch{}, 0
		}
		consumed += fixConsumed

		// Try to extract hold parameters (radial, minutes, turn direction)
		// Pattern: "on the (radial) radial [inbound], (minutes) minute legs, (left/right) turns"
		holdCmd, holdConsumed := extractHoldParams(tokens[consumed:], fix)
		if holdConsumed == 0 {
			// No hold parameters found - this should fall back to hold_published template
			logLocalStt("  tryMatchTemplate %q: no hold parameters found, falling back", tmpl.Name)
			return CommandMatch{}, 0
		}
		consumed += holdConsumed

		logLocalStt("  tryMatchTemplate %q: cmd=%q fix=%q consumed=%d",
			tmpl.Name, holdCmd, fix, consumed)
		return CommandMatch{
			Command:    holdCmd,
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
			case ArgFix, ArgApproach, ArgApproachLAHSO, ArgSquawk:
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
			// Check if there's a better altitude right after this one
			// e.g., "10 11000" where "10" is garbled and "11000" is the real altitude
			if i+1 < len(tokens) && tokens[i+1].Type == TokenNumber {
				next := tokens[i+1]
				// If current is small (< 100) and next is raw feet, prefer next
				if t.Value < 100 && next.Value >= 1000 && next.Value <= 60000 && next.Value%100 == 0 {
					logLocalStt("  extractAltitude: skipping garbled %d, using %d", t.Value, next.Value/100)
					return next.Value / 100, i + 2
				}
			}

			// Heuristic: if it looks like altitude encoding (2-3 digits, reasonable value).
			// Exclude the speed range (100-400) since those are ambiguous and more likely
			// to be speeds. Flight levels in that range are handled by the allowFlightLevel
			// variant called from climb/descend templates.
			if t.Value >= 10 && t.Value <= 600 && (t.Value < 100 || t.Value > 400) {
				return t.Value, i + 1
			}
			// Large number might be raw feet
			if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
				return t.Value / 100, i + 1
			}
			// STT sometimes adds extra zeros: "9,000" -> "900,000" or "12,000" -> "120,000"
			// Detect values 100x too large and correct them
			if t.Value >= 100000 && t.Value <= 6000000 && t.Value%10000 == 0 {
				corrected := t.Value / 100
				if corrected >= 1000 && corrected <= 60000 {
					logLocalStt("  extractAltitude: corrected %d -> %d (extra zeros)", t.Value, corrected/100)
					return corrected / 100, i + 1
				}
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

// extractFlightLevelAltitude extracts altitude values in the flight level range (100-400).
// This is used as a fallback for climb/descend templates where values like 115 (FL115)
// are clearly altitudes, not speeds. Regular extractAltitude excludes this range to
// avoid conflicts with speed commands.
func extractFlightLevelAltitude(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	for i, t := range tokens {
		if i > 3 {
			break
		}
		if t.Type == TokenNumber && t.Value >= 100 && t.Value <= 400 {
			logLocalStt("  extractFlightLevelAltitude: accepting %d as FL (climb/descend context)", t.Value)
			return t.Value, i + 1
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
		// Handle 4-digit values where first 3 digits form valid heading (e.g., 2801 → 280)
		// STT sometimes appends trailing garbage to headings
		if t.Type == TokenNumber && t.Value > 360 && t.Value < 10000 {
			// Try dropping the last digit
			hdg := t.Value / 10
			if hdg >= 1 && hdg <= 360 {
				logLocalStt("  extractHeading: corrected %d -> %d (dropped trailing digit)", t.Value, hdg)
				return hdg, i + 1
			}
		}
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			hdg := t.Value

			// Check for pattern: small_number + "to" + larger_number
			// e.g., "10 to 130" where "10" is garbled and "130" is the real heading
			// STT sometimes produces this when transcribing "one three zero"
			if hdg < 100 && i+2 < len(tokens) {
				nextText := strings.ToLower(tokens[i+1].Text)
				if nextText == "to" && tokens[i+2].Type == TokenNumber {
					largerHdg := tokens[i+2].Value
					if largerHdg >= 100 && largerHdg <= 360 {
						logLocalStt("  extractHeading: skipping garbled %d, using %d", hdg, largerHdg)
						return largerHdg, i + 3
					}
				}
			}

			// Headings are always spoken as 3 digits and almost always multiples of 10.
			// Use Token.Text to determine if user said leading zero:
			// - "020" (Text starts with 0) = unambiguous heading 020
			// - "36" (2 digits, doesn't end in 0) = trailing zero dropped, heading 360
			// - "10" (2 digits, ends in 0) = ambiguous, could be 010 or 100, be conservative
			text := t.Text
			hasLeadingZero := len(text) > 0 && text[0] == '0'

			if hasLeadingZero {
				// User said "zero two zero" - unambiguous, use value as-is
				// For single digit after leading zero (e.g., "08"), multiply by 10 for heading 080
				if hdg < 10 {
					hdg *= 10
				}
				logLocalStt("  extractHeading: %d from %q (has leading zero, unambiguous)", hdg, text)
			} else if len(text) == 2 && hdg >= 10 && hdg <= 36 && hdg%10 != 0 {
				// 2-digit number not ending in 0 (like 36, 27, 14): trailing zero was likely dropped
				// Headings are almost always multiples of 10, so "two seven" is much more likely
				// to be 270 than 027. If they meant 027, they would say "zero two seven".
				expanded := hdg * 10
				logLocalStt("  extractHeading: expanded %d -> %d (2-digit %q, trailing zero dropped)", hdg, expanded, text)
				hdg = expanded
			} else if hdg < 10 {
				// Single digit without leading zero context - assume "zero X" = 0X0
				hdg *= 10
			}
			// For 2-digit numbers ending in 0 (10, 20, 30): ambiguous, use as-is (conservative)
			return hdg, i + 1
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
		if t.Type == TokenNumber {
			// Normal speed range (100-400 knots)
			if t.Value >= 100 && t.Value <= 400 {
				return t.Value, i + 1
			}
			// Handle 4-digit speeds with extra digit (e.g., "1709" → 170, "2101" → 210)
			// STT sometimes appends an extra digit to speeds
			if t.Value > 400 {
				corrected := t.Value / 10
				if corrected >= 100 && corrected <= 400 {
					logLocalStt("  extractSpeed: corrected %d -> %d (extra digit)", t.Value, corrected)
					return corrected, i + 1
				}
			}
			// Handle 2-digit speeds with missing leading digit (e.g., "30" → 230, "70" → 170)
			// STT sometimes drops the leading digit for speeds spoken as "two three zero"
			// Try prepending "2" first (typical approach speeds are 200-250), then "1"
			if t.Value >= 10 && t.Value < 100 {
				// Try prepending "2" first - typical for "increase speed to X"
				corrected := 200 + t.Value
				if corrected >= 200 && corrected <= 290 {
					logLocalStt("  extractSpeed: corrected %d -> %d (missing leading 2)", t.Value, corrected)
					return corrected, i + 1
				}
				// Try prepending "1" - typical for slower speeds
				corrected = 100 + t.Value
				if corrected >= 140 && corrected <= 190 {
					logLocalStt("  extractSpeed: corrected %d -> %d (missing leading 1)", t.Value, corrected)
					return corrected, i + 1
				}
			}
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

		// Try exact match first - store it as best match (don't return yet, need to check for spelling)
		for spokenName, fixID := range fixes {
			if strings.EqualFold(phrase, spokenName) {
				logLocalStt("  extractFix: matched %q -> %q (exact)", phrase, fixID)
				bestFix = fixID
				bestScore = 1.0
				bestLength = length
				// Break out of both loops - exact match is best possible spoken match
				goto checkSpelling
			}
		}

		// Try fuzzy match - find the best one (with length ratio check to prevent over-matching)
		// Use alphabetically earlier fixID as tie-breaker for determinism.
		for spokenName, fixID := range fixes {
			// Reject if phrase is much longer than fix name (prevents "pucky heading 180" matching "Pucky")
			lenRatio := float64(len(phrase)) / float64(len(spokenName))
			if lenRatio > 1.5 {
				continue
			}
			score := JaroWinkler(phrase, spokenName)
			if score >= 0.78 && (score > bestScore || (score == bestScore && fixID < bestFix)) {
				bestFix = fixID
				bestScore = score
				bestLength = length
			}
			if PhoneticMatch(phrase, spokenName) && (bestScore < 0.80 || (bestScore == 0.80 && fixID < bestFix)) {
				bestFix = fixID
				bestScore = 0.80
				bestLength = length
			}
			// Try vowel-normalized comparison for syllable contractions
			// (e.g., "gail" should match "gayel")
			normPhrase := normalizeVowels(phrase)
			normSpoken := normalizeVowels(spokenName)
			if normPhrase != phrase || normSpoken != spokenName {
				normScore := JaroWinkler(normPhrase, normSpoken)
				adjustedScore := normScore * 0.95 // Slight penalty for needing normalization
				if normScore >= 0.78 && (adjustedScore > bestScore || (adjustedScore == bestScore && fixID < bestFix)) {
					bestFix = fixID
					bestScore = adjustedScore
					bestLength = length
				}
			}
			// Try consonant-only matching for fix names with vowel STT errors
			// (e.g., "zizou" should match "zzooo" since both have consonants "zz")
			if len(phrase) >= 3 && len(spokenName) >= 3 {
				phraseCons := extractConsonants(phrase)
				spokenCons := extractConsonants(spokenName)
				if len(phraseCons) >= 2 && phraseCons == spokenCons && (bestScore < 0.78 || (bestScore == 0.78 && fixID < bestFix)) {
					bestFix = fixID
					bestScore = 0.78 // Conservative score for consonant-only match
					bestLength = length
				}
			}
		}
	}

checkSpelling:
	// After initial match attempt, look for spelling patterns that may follow.
	// Controllers often spell out fix names: "direct Deer Park, that's delta papa kilo"
	searchStart := bestLength
	if bestFix == "" {
		// No match yet - still check for spelling after the first word
		searchStart = 1
	}

	if searchStart > 0 && searchStart < len(tokens) {
		spelledFix, spellingConf, spellingConsumed := extractSpelledFix(tokens[searchStart:], fixes)
		if spelledFix != "" {
			totalConsumed := searchStart + spellingConsumed
			if bestFix == "" {
				// No initial match - use spelling as primary
				logLocalStt("  extractFix: no spoken match, using spelling %q", spelledFix)
				return spelledFix, spellingConf, totalConsumed
			}
			if spelledFix == bestFix {
				// Spelling confirms our match - boost confidence
				logLocalStt("  extractFix: spelling confirms match %q", bestFix)
				return bestFix, max(bestScore, 0.98), totalConsumed
			}
			// Spelling contradicts match - prefer spelling (more explicit)
			logLocalStt("  extractFix: spelling %q overrides spoken match %q", spelledFix, bestFix)
			return spelledFix, spellingConf, totalConsumed
		}
	}

	if bestFix != "" {
		logLocalStt("  extractFix: matched %q -> %q (fuzzy %.2f)", tokens[0].Text, bestFix, bestScore)
		return bestFix, bestScore, bestLength
	}
	logLocalStt("  extractFix: no match found for %q", tokens[0].Text)
	return "", 0, 0
}

// extractConsonants extracts only consonants from a string (for fuzzy matching).
func extractConsonants(s string) string {
	var result strings.Builder
	s = strings.ToUpper(s) // isVowel expects uppercase
	for _, c := range s {
		if c >= 'A' && c <= 'Z' && !isVowel(byte(c)) {
			result.WriteRune(c)
		}
	}
	return strings.ToLower(result.String())
}

// extractSpelledFix looks for a spelled-out fix name in tokens using NATO phonetic alphabet.
// Handles patterns like "that's delta papa kilo" or "charlie alpha mike romeo november".
// Returns the fix ID if spelling matches a fix, confidence, and tokens consumed.
func extractSpelledFix(tokens []Token, fixes map[string]string) (string, float64, int) {
	if len(tokens) == 0 || len(fixes) == 0 {
		return "", 0, 0
	}

	startIdx := 0

	// Check for trigger phrase ("that's", "spelled", etc.)
	if IsSpellingTrigger(tokens[0].Text) {
		startIdx = 1
		if startIdx >= len(tokens) {
			return "", 0, 0
		}
	}

	// Extract NATO letters from remaining tokens
	var words []string
	for i := startIdx; i < len(tokens); i++ {
		words = append(words, tokens[i].Text)
	}

	spelled, natoConsumed := ExtractNATOSpelling(words)
	if len(spelled) < 2 {
		// Need at least 2 letters for a valid fix spelling
		return "", 0, 0
	}

	logLocalStt("  extractSpelledFix: extracted spelling %q from %d NATO words", spelled, natoConsumed)

	// Check if spelled string matches any fix ID (by value)
	for _, fixID := range fixes {
		if strings.EqualFold(spelled, fixID) {
			totalConsumed := startIdx + natoConsumed
			logLocalStt("  extractSpelledFix: spelling %q matches fix %q", spelled, fixID)
			return fixID, 0.95, totalConsumed
		}
	}

	logLocalStt("  extractSpelledFix: spelling %q does not match any fix", spelled)
	return "", 0, 0
}

// extractApproach extracts an approach from tokens.
func extractApproach(tokens []Token, approaches map[string]string) (string, float64, int) {
	if len(tokens) == 0 || len(approaches) == 0 {
		return "", 0, 0
	}

	// First, try type+number matching: extract approach type and runway number from tokens,
	// then find a candidate that matches both. This handles garbage words between type and
	// number (e.g., "ils front of a niner" should match "I L S runway niner" → I9).
	if appr, conf, consumed := matchApproachByTypeAndNumber(tokens, approaches); consumed > 0 {
		return appr, conf, consumed
	}

	var bestAppr string
	var bestScore float64
	var bestLength int

	// Build candidate phrases (1-7 words for approach names, since spoken numbers expand)
	for length := min(7, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := range length {
			// Expand numeric tokens to spoken form to match telephony
			// e.g., "22" -> "two two"
			if tokens[i].Type == TokenNumber {
				parts = append(parts, spokenDigits(tokens[i].Value))
			} else {
				parts = append(parts, tokens[i].Text)
			}
		}
		phrase := strings.Join(parts, " ")

		// Generate phrase variants to handle letter separation issues
		phraseVariants := generateApproachPhraseVariants(phrase)

		for _, variant := range phraseVariants {
			// Try exact match first - return immediately
			for spokenName, apprID := range approaches {
				if strings.EqualFold(variant, spokenName) {
					return apprID, 1.0, length
				}
			}

			// Try fuzzy match - find the best one.
			// Use alphabetically earlier apprID as tie-breaker for determinism.
			for spokenName, apprID := range approaches {
				score := JaroWinkler(variant, spokenName)
				if score >= 0.80 && (score > bestScore || (score == bestScore && apprID < bestAppr)) {
					bestAppr = apprID
					bestScore = score
					bestLength = length
				}
			}
		}
	}

	if bestAppr != "" {
		return bestAppr, bestScore, bestLength
	}
	return "", 0, 0
}

// matchApproachByTypeAndNumber tries to match approach by extracting the approach type
// (ILS, RNAV, visual, etc.) and runway number separately, ignoring garbage words between them.
// This handles cases like "ils front of a niner" where STT inserts garbage between type and number.
func matchApproachByTypeAndNumber(tokens []Token, approaches map[string]string) (string, float64, int) {
	if len(tokens) == 0 {
		return "", 0, 0
	}

	// Extract approach type from the beginning of tokens
	approachType, typeConsumed := extractApproachType(tokens)
	if approachType == "" {
		return "", 0, 0
	}

	// Look for runway number anywhere in the remaining tokens
	remainingTokens := tokens[typeConsumed:]
	runwayNum, runwayDir, numPos := extractRunwayNumber(remainingTokens)
	if runwayNum == "" {
		return "", 0, 0
	}

	// Validate: check if there's a suspicious word after the runway number (and direction).
	// If there's an unknown word immediately after, it's likely garbage and we should
	// fall back to fuzzy matching. This prevents "atlas runway one month" from matching
	// when "month" is garbage.
	afterNumPos := numPos + 1
	if runwayDir != "" {
		afterNumPos++ // Skip the direction word too
	}
	if afterNumPos < len(remainingTokens) {
		afterWord := strings.ToLower(remainingTokens[afterNumPos].Text)
		// Allow filler words, approach-related words, and common command keywords
		validAfterWords := map[string]bool{
			"approach": true, "for": true, "and": true, "the": true, "a": true,
			"maintain": true, "speed": true, "until": true, "cleared": true,
			"our": true, // Common before "approach" in STT
		}
		if !validAfterWords[afterWord] && !IsFillerWord(afterWord) {
			// Unknown word after runway - likely garbage, reject the match
			return "", 0, 0
		}
	}

	// Build the runway designator (e.g., "niner", "one two", "two eight left")
	runwaySpoken := runwayNum
	if runwayDir != "" {
		runwaySpoken += " " + runwayDir
	}

	// Find a matching approach that has both the type and runway number
	var bestAppr string
	var bestScore float64
	for spokenName, apprID := range approaches {
		spokenLower := strings.ToLower(spokenName)

		// Check if the candidate contains the approach type
		if !approachTypeMatches(spokenLower, approachType) {
			continue
		}

		// Check if the candidate's runway matches our extracted runway
		// The runway in the candidate should start with our spoken runway number
		if !runwayMatches(spokenLower, runwaySpoken) {
			continue
		}

		// We have a type+number match - calculate confidence based on specificity
		score := 0.95 // High confidence for type+number match
		if score > bestScore || (score == bestScore && apprID < bestAppr) {
			bestAppr = apprID
			bestScore = score
		}
	}

	if bestAppr != "" {
		// Consumed = type tokens + position of number + 1 for number itself + 1 for direction if present
		consumed := typeConsumed + numPos + 1
		if runwayDir != "" {
			consumed++ // Account for direction word (left/right/center)
		}
		logLocalStt("  matchApproachByTypeAndNumber: type=%q runway=%q -> %q (consumed=%d)",
			approachType, runwaySpoken, bestAppr, consumed)
		return bestAppr, bestScore, consumed
	}
	return "", 0, 0
}

// extractApproachType extracts the approach type from the beginning of tokens.
// Returns the type (e.g., "ils", "rnav", "visual") and number of tokens consumed.
func extractApproachType(tokens []Token) (string, int) {
	if len(tokens) == 0 {
		return "", 0
	}

	text := strings.ToLower(tokens[0].Text)

	// Single-word approach types
	switch text {
	case "ils", "alice", "dallas", "als", "atlas", "dialogues", "dial": // STT errors for "ILS"
		return "ils", 1
	case "rnav":
		return "rnav", 1
	case "visual":
		return "visual", 1
	case "vor":
		return "vor", 1
	case "localizer", "loc":
		return "localizer", 1
	}

	// Multi-word: "i l s" (spelled out ILS)
	if text == "i" && len(tokens) >= 3 {
		if strings.ToLower(tokens[1].Text) == "l" && strings.ToLower(tokens[2].Text) == "s" {
			return "ils", 3
		}
	}

	// "r nav" or "r-nav" (already split by hyphen removal)
	if text == "r" && len(tokens) >= 2 && strings.ToLower(tokens[1].Text) == "nav" {
		return "rnav", 2
	}

	return "", 0
}

// extractRunwayNumber looks for a runway number in tokens.
// Returns the spoken number (e.g., "niner", "one two"), optional direction, and position.
// Checks for direction both before and after the number. If direction appears before
// the number (e.g., "right 30"), that takes precedence over direction after (e.g., "30 left").
func extractRunwayNumber(tokens []Token) (string, string, int) {
	for i, t := range tokens {
		value := t.Value
		// Handle "tN" patterns (e.g., "t7" for garbled "twenty-seven" → 27)
		if t.Type == TokenWord && len(t.Text) == 2 && strings.ToLower(t.Text[:1]) == "t" {
			if digit := t.Text[1]; digit >= '0' && digit <= '9' {
				value = 20 + int(digit-'0')
			}
		}
		if (t.Type == TokenNumber || value > 0) && value >= 1 && value <= 36 {
			num := spokenDigits(value)
			dir := ""

			// First, check for direction BEFORE the number (e.g., "ILS right 30")
			// This pattern often occurs in approach names like "ILS right runway 30"
			if i > 0 {
				prevText := strings.ToLower(tokens[i-1].Text)
				switch prevText {
				case "left", "l":
					dir = "left"
				case "right", "r":
					dir = "right"
				case "center", "c":
					dir = "center"
				}
			}

			// If no direction before, check after the number (e.g., "30 left")
			if dir == "" && i+1 < len(tokens) {
				nextText := strings.ToLower(tokens[i+1].Text)
				switch nextText {
				case "left", "l":
					dir = "left"
				case "right", "r":
					dir = "right"
				case "center", "c":
					dir = "center"
				}
			}
			return num, dir, i
		}
	}
	return "", "", -1
}

// approachTypeMatches checks if a spoken approach name contains the given approach type.
func approachTypeMatches(spokenLower, approachType string) bool {
	switch approachType {
	case "ils":
		return strings.Contains(spokenLower, "i l s") || strings.Contains(spokenLower, "ils")
	case "rnav":
		return strings.Contains(spokenLower, "r-nav") || strings.Contains(spokenLower, "rnav") || strings.Contains(spokenLower, "r nav")
	case "visual":
		return strings.Contains(spokenLower, "visual")
	case "vor":
		return strings.Contains(spokenLower, "v o r") || strings.Contains(spokenLower, "vor")
	case "localizer":
		return strings.Contains(spokenLower, "localizer") || strings.Contains(spokenLower, "loc")
	}
	return false
}

// runwayMatches checks if the candidate approach's runway matches the extracted runway.
// The runway in the candidate (after "runway") must start with our spoken runway number.
// This prevents "two" from matching "two two left" since the candidate starts with "two two".
func runwayMatches(spokenLower, runwaySpoken string) bool {
	// Find "runway " in the candidate
	idx := strings.Index(spokenLower, "runway ")
	if idx == -1 {
		return false
	}

	// Get the part after "runway "
	runwayPart := spokenLower[idx+7:] // len("runway ") == 7

	// The candidate's runway should start with our extracted runway
	// e.g., "niner" matches "niner" or "niner left"
	// but "two" should NOT match "two two left" (must match "two" or "two left")
	if strings.HasPrefix(runwayPart, runwaySpoken) {
		// Check that what follows is either end of string, space, or direction
		rest := runwayPart[len(runwaySpoken):]
		if rest == "" {
			return true
		}
		if rest[0] == ' ' {
			// Check if followed by direction (left/right/center) or end
			restTrimmed := strings.TrimPrefix(rest, " ")
			return restTrimmed == "" ||
				strings.HasPrefix(restTrimmed, "left") ||
				strings.HasPrefix(restTrimmed, "right") ||
				strings.HasPrefix(restTrimmed, "center")
		}
	}
	return false
}

// extractLAHSO extracts a LAHSO (land and hold short) runway from tokens.
// Looks for patterns like "land and hold short of runway 26" or "hold short runway 26".
// Returns the matched runway ID and number of tokens consumed.
// extractLAHSO looks for "land and hold short" pattern and extracts the LAHSO runway.
// Returns the matched runway and total tokens consumed from the start of the pattern.
// Expects tokens starting from "land" or "hold" keyword.
func extractLAHSO(tokens []Token, lahsoRunways []string) (string, int) {
	if len(tokens) == 0 || len(lahsoRunways) == 0 {
		return "", 0
	}

	// Find pattern: [land] [and] hold short [of] [runway] <runway>
	// "land and" is expected but we also accept just "hold short" for robustness
	landIdx := -1
	holdIdx := -1
	shortIdx := -1

	for i, t := range tokens {
		text := strings.ToLower(t.Text)
		switch {
		case (text == "land" || FuzzyMatch(text, "land", 0.8)) && landIdx == -1:
			landIdx = i
		case (text == "hold" || FuzzyMatch(text, "hold", 0.8)) && holdIdx == -1:
			holdIdx = i
		case (text == "short" || FuzzyMatch(text, "short", 0.8)) && shortIdx == -1:
			shortIdx = i
		}
	}

	// Need "hold short" with short after hold
	if holdIdx == -1 || shortIdx == -1 || shortIdx <= holdIdx {
		return "", 0
	}

	// Determine start of pattern for consumed count
	patternStart := holdIdx
	if landIdx != -1 && landIdx < holdIdx {
		patternStart = landIdx
	}

	// Find runway after "short", skipping fillers
	searchIdx := shortIdx + 1
	for searchIdx < len(tokens) {
		text := strings.ToLower(tokens[searchIdx].Text)
		if text == "of" || text == "runway" || text == "the" || text == "and" {
			searchIdx++
			continue
		}
		break
	}

	if searchIdx >= len(tokens) {
		return "", 0
	}

	// Try to extract runway from remaining tokens
	rwy, consumed := matchLAHSORunway(tokens[searchIdx:], lahsoRunways)
	if rwy != "" {
		return rwy, searchIdx + consumed - patternStart
	}

	return "", 0
}

// matchLAHSORunway matches tokens against available LAHSO runways.
// Handles both clean numeric input and garbled STT output.
func matchLAHSORunway(tokens []Token, lahsoRunways []string) (string, int) {
	if len(tokens) == 0 {
		return "", 0
	}

	// Helper for direction suffix
	directionSuffix := func(text string) string {
		switch strings.ToLower(text) {
		case "left", "l":
			return "L"
		case "right", "r":
			return "R"
		case "center", "c":
			return "C"
		}
		return ""
	}

	// Try numeric match first (clean STT case)
	if tokens[0].Type == TokenNumber && tokens[0].Value >= 1 && tokens[0].Value <= 36 {
		num := tokens[0].Value
		consumed := 1
		suffix := ""

		// Look for direction, skipping "and"
		for consumed < len(tokens) && consumed < 3 {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "and" {
				consumed++
				continue
			}
			if s := directionSuffix(text); s != "" {
				suffix = s
				consumed++
			}
			break
		}

		runwayStr := fmt.Sprintf("%d%s", num, suffix)

		// Exact match
		for _, rwy := range lahsoRunways {
			if rwy == runwayStr {
				logLocalStt("  extractLAHSO: exact match %q", runwayStr)
				return rwy, consumed
			}
		}

		// Number match (direction might be wrong or missing)
		numStr := fmt.Sprintf("%d", num)
		for _, rwy := range lahsoRunways {
			if strings.TrimRight(rwy, "LRC") == numStr {
				logLocalStt("  extractLAHSO: number match %q -> %q", runwayStr, rwy)
				return rwy, consumed
			}
		}
	}

	// Fuzzy match: collect tokens and match against runway spoken forms
	var detectedSuffix string
	consumed := 0
	for i := 0; i < len(tokens) && i < 4; i++ {
		if s := directionSuffix(tokens[i].Text); s != "" {
			detectedSuffix = s
		}
		consumed++
	}

	// Filter by direction if detected
	candidates := lahsoRunways
	if detectedSuffix != "" {
		candidates = util.FilterSlice(lahsoRunways, func(rwy string) bool {
			return strings.HasSuffix(rwy, detectedSuffix)
		})
	}

	// If only one candidate, use it
	if len(candidates) == 1 {
		logLocalStt("  extractLAHSO: single candidate %q", candidates[0])
		return candidates[0], consumed
	}

	// Try fuzzy match first token against runway numbers
	firstText := strings.ToLower(tokens[0].Text)
	for _, rwy := range candidates {
		rwyNum := strings.TrimRight(rwy, "LRC")
		spoken := spokenRunway(rwy)

		// Check if token matches spoken form or phonetically matches number
		if strings.Contains(spoken, firstText) || PhoneticMatch(firstText, rwyNum) {
			logLocalStt("  extractLAHSO: fuzzy match %q -> %q", firstText, rwy)
			return rwy, consumed
		}
	}

	// Fallback: if we have direction and candidates, use first
	if detectedSuffix != "" && len(candidates) > 0 {
		logLocalStt("  extractLAHSO: direction fallback -> %q", candidates[0])
		return candidates[0], consumed
	}

	return "", 0
}

// spokenRunway returns the spoken form of a runway (e.g., "31L" -> "three one left")
func spokenRunway(rwy string) string {
	var parts []string
	for _, ch := range rwy {
		switch {
		case ch >= '0' && ch <= '9':
			digitWords := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "niner"}
			parts = append(parts, digitWords[ch-'0'])
		case ch == 'L' || ch == 'l':
			parts = append(parts, "left")
		case ch == 'R' || ch == 'r':
			parts = append(parts, "right")
		case ch == 'C' || ch == 'c':
			parts = append(parts, "center")
		}
	}
	return strings.Join(parts, " ")
}

// generateApproachPhraseVariants generates variants of an approach phrase
// to handle common STT issues with separated letters and missing words.
// For example: "l s runway 7 right" → also try "i l s runway 7 right"
// For example: "ils two eight center" → also try "i l s runway two eight center"
func generateApproachPhraseVariants(phrase string) []string {
	variants := []string{phrase}

	// Handle "l s" → "i l s" (missing "i" in "ILS")
	if strings.Contains(phrase, "l s ") {
		variant := strings.Replace(phrase, "l s ", "i l s ", 1)
		variants = append(variants, variant)
	}

	// Handle "ls" → "ils" (already joined but missing "i")
	if strings.HasPrefix(phrase, "ls ") {
		variant := "ils " + phrase[3:]
		variants = append(variants, variant)
	}

	// Handle "ils" → "i l s" (Whisper sometimes joins "ILS" into one word)
	if strings.HasPrefix(phrase, "ils ") {
		variant := "i l s " + phrase[4:]
		variants = append(variants, variant)
	}

	// Handle "rnav" → "r-nav" (approach telephony uses hyphenated form)
	if strings.HasPrefix(phrase, "rnav ") {
		variant := "r-nav " + phrase[5:]
		variants = append(variants, variant)
	}

	// Generate variants with "runway" inserted after approach type prefixes.
	// Handles cases where user omits "runway" but candidate includes it
	// (e.g., "i l s two eight center" should match "I L S runway two eight center")
	approachPrefixes := []string{"i l s ", "ils ", "visual ", "rnav ", "r-nav ", "v o r ", "vor ", "localizer ", "loc "}
	var runwayVariants []string
	for _, v := range variants {
		for _, prefix := range approachPrefixes {
			if strings.HasPrefix(v, prefix) && !strings.Contains(v, "runway") {
				runwayVariant := prefix + "runway " + v[len(prefix):]
				runwayVariants = append(runwayVariants, runwayVariant)
				break
			}
		}
	}
	variants = append(variants, runwayVariants...)

	return variants
}

// extractSID extracts a SID name from tokens by matching against the aircraft's assigned SID.
// Also accepts the generic word "sid" as a match (for "climb via the sid" without specific name).
// Returns the number of tokens consumed (0 if no match).
func extractSID(tokens []Token, sid string) int {
	if len(tokens) == 0 {
		return 0
	}

	// Check for generic "sid" word first (handles "climb via the sid")
	if strings.EqualFold(tokens[0].Text, "sid") {
		logLocalStt("  extractSID: matched generic 'sid'")
		return 1
	}

	// If aircraft has no SID assigned, we can only match the generic word
	if sid == "" {
		logLocalStt("  extractSID: no SID assigned and no generic match")
		return 0
	}

	// Get the telephony for this SID
	sidTelephony := av.GetSIDTelephony(sid)
	logLocalStt("  extractSID: looking for SID=%q telephony=%q", sid, sidTelephony)

	// Build candidate phrases (1-4 words for SID names)
	for length := min(4, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := range length {
			// Expand numeric tokens to spoken form
			if tokens[i].Type == TokenNumber {
				parts = append(parts, spokenDigits(tokens[i].Value))
			} else {
				parts = append(parts, tokens[i].Text)
			}
		}
		phrase := strings.Join(parts, " ")

		// Try exact match
		if strings.EqualFold(phrase, sidTelephony) {
			logLocalStt("  extractSID: exact match %q -> %q", phrase, sid)
			return length
		}

		// Try fuzzy match
		score := JaroWinkler(phrase, sidTelephony)
		if score >= 0.80 {
			logLocalStt("  extractSID: fuzzy match %q -> %q (score=%.2f)", phrase, sid, score)
			return length
		}

		// Try phonetic match
		if PhoneticMatch(phrase, sidTelephony) {
			logLocalStt("  extractSID: phonetic match %q -> %q", phrase, sid)
			return length
		}
	}

	logLocalStt("  extractSID: no match found")
	return 0
}

// extractSTAR extracts a STAR name from tokens by matching against the aircraft's assigned STAR.
// Also accepts the generic word "star" as a match (for "descend via the star" without specific name).
// Returns the number of tokens consumed (0 if no match).
func extractSTAR(tokens []Token, star string) int {
	if len(tokens) == 0 {
		return 0
	}

	// Check for generic "star" word first (handles "descend via the star")
	// Also handle common STT errors: "stars" (plural), "start" (mishearing)
	text := strings.ToLower(tokens[0].Text)
	if text == "star" || text == "stars" || text == "start" {
		logLocalStt("  extractSTAR: matched generic %q as 'star'", text)
		return 1
	}

	// If aircraft has no STAR assigned, we can only match the generic word
	if star == "" {
		logLocalStt("  extractSTAR: no STAR assigned and no generic match")
		return 0
	}

	// Get the telephony for this STAR
	starTelephony := av.GetSTARTelephony(star)
	logLocalStt("  extractSTAR: looking for STAR=%q telephony=%q", star, starTelephony)

	// Build candidate phrases (1-4 words for STAR names)
	for length := min(4, len(tokens)); length >= 1; length-- {
		var parts []string
		for i := range length {
			// Expand numeric tokens to spoken form
			if tokens[i].Type == TokenNumber {
				parts = append(parts, spokenDigits(tokens[i].Value))
			} else {
				parts = append(parts, tokens[i].Text)
			}
		}
		phrase := strings.Join(parts, " ")

		// Try exact match
		if strings.EqualFold(phrase, starTelephony) {
			logLocalStt("  extractSTAR: exact match %q -> %q", phrase, star)
			return length
		}

		// Try fuzzy match
		score := JaroWinkler(phrase, starTelephony)
		if score >= 0.80 {
			logLocalStt("  extractSTAR: fuzzy match %q -> %q (score=%.2f)", phrase, star, score)
			return length
		}

		// Try phonetic match
		if PhoneticMatch(phrase, starTelephony) {
			logLocalStt("  extractSTAR: phonetic match %q -> %q", phrase, star)
			return length
		}
	}

	logLocalStt("  extractSTAR: no match found")
	return 0
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
// Uses word order to disambiguate: "turn 20 left" is a degrees turn,
// but "turn left 20" is interpreted as heading (direction before number).
// The "degrees" keyword overrides this: "turn left 20 degrees" is a degrees turn.
func extractDegrees(tokens []Token) (int, string, int) {
	if len(tokens) == 0 {
		return 0, "", 0
	}

	var deg int
	var dir string
	var degPos, dirPos int = -1, -1
	var hasDegreesKeyword bool
	consumed := 0

	// Look for number and direction, tracking positions
	for consumed < len(tokens) && consumed < 5 {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		if t.Type == TokenNumber && t.Value > 0 && t.Value <= 45 && degPos == -1 {
			deg = t.Value
			degPos = consumed
		} else if (text == "left" || text == "right") && dirPos == -1 {
			dir = text
			dirPos = consumed
		} else if text == "degrees" || text == "degree" {
			hasDegreesKeyword = true
		}
		consumed++

		// Keep scanning even after finding both to check for "degrees" keyword
		if deg > 0 && dir != "" && !hasDegreesKeyword {
			// Continue scanning a couple more tokens for "degrees"
			for i := 0; i < 2 && consumed < len(tokens); i++ {
				if text := strings.ToLower(tokens[consumed].Text); text == "degrees" || text == "degree" {
					hasDegreesKeyword = true
					consumed++
					break
				}
				consumed++
			}
			break
		}
	}

	// Only return match if:
	// 1. Both number and direction found, AND
	// 2. Number came before direction OR "degrees" keyword present
	if deg > 0 && dir != "" {
		if degPos < dirPos || hasDegreesKeyword {
			// Snap to nearest 5 degrees (ATC standard)
			// e.g., 23 -> 20, 27 -> 25, 33 -> 35
			deg = ((deg + 2) / 5) * 5
			if deg == 0 {
				deg = 5 // Minimum 5 degrees
			}
			return deg, dir, consumed
		}
	}

	return 0, "", 0
}

// extractHoldParams extracts hold parameters from tokens for controller-specified holds.
// Pattern: "on the (radial) radial [inbound], (minutes) minute legs, (left/right) turns"
// Returns the full hold command string and number of tokens consumed.
// Returns empty string and 0 if no hold parameters found.
func extractHoldParams(tokens []Token, fix string) (string, int) {
	if len(tokens) == 0 {
		return "", 0
	}

	consumed := 0
	var radial int
	var minutes int
	turnDir := "R" // Default to right turns

	// Skip filler words
	skipHoldFillers := func() {
		for consumed < len(tokens) {
			text := strings.ToLower(tokens[consumed].Text)
			if text == "on" || text == "the" || text == "inbound" || IsFillerWord(text) {
				consumed++
			} else {
				break
			}
		}
	}

	skipHoldFillers()

	// Look for radial specification: "(number) radial" or "(number) bearing"
	foundRadial := false
	for consumed < len(tokens) {
		t := tokens[consumed]
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			// Found a number, check if followed by "radial" or "bearing"
			if consumed+1 < len(tokens) {
				nextText := strings.ToLower(tokens[consumed+1].Text)
				if nextText == "radial" || nextText == "bearing" {
					radial = t.Value
					consumed += 2 // Skip number and "radial"/"bearing"
					foundRadial = true
					break
				}
			}
		}
		consumed++
		if consumed > 10 { // Don't scan too far
			break
		}
	}

	if !foundRadial {
		return "", 0
	}

	skipHoldFillers()

	// Look for leg duration: "(number) minute legs"
	for consumed < len(tokens) {
		t := tokens[consumed]
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 10 {
			// Check if followed by "minute" then "legs"
			if consumed+1 < len(tokens) {
				nextText := strings.ToLower(tokens[consumed+1].Text)
				if nextText == "minute" {
					minutes = t.Value
					consumed += 2 // Skip number and "minute"
					// Skip "legs" if present
					if consumed < len(tokens) && strings.ToLower(tokens[consumed].Text) == "legs" {
						consumed++
					}
					break
				}
			}
		}
		consumed++
		if consumed > 20 { // Don't scan too far
			break
		}
	}

	// Look for turn direction: "left turns" or "right turns"
	for consumed < len(tokens) {
		text := strings.ToLower(tokens[consumed].Text)
		if text == "left" {
			turnDir = "L"
			consumed++
			// Skip "turns" if present
			if consumed < len(tokens) && strings.ToLower(tokens[consumed].Text) == "turns" {
				consumed++
			}
			break
		} else if text == "right" {
			turnDir = "R"
			consumed++
			// Skip "turns" if present
			if consumed < len(tokens) && strings.ToLower(tokens[consumed].Text) == "turns" {
				consumed++
			}
			break
		}
		consumed++
		if consumed > 25 { // Don't scan too far
			break
		}
	}

	// Skip "expect further clearance" and any following digits if present
	consumed = skipExpectFurtherClearance(tokens, consumed)

	// Build the command string: HFIX/Rradial/minutesM/L or R
	// Format from parseHold: HFIX/R{radial}/{minutes}M/{L|R}
	var cmd strings.Builder
	cmd.WriteString("H")
	cmd.WriteString(fix)
	cmd.WriteString("/R")
	cmd.WriteString(strconv.Itoa(radial))
	if minutes > 0 {
		cmd.WriteString("/")
		cmd.WriteString(strconv.Itoa(minutes))
		cmd.WriteString("M")
	}
	cmd.WriteString("/")
	cmd.WriteString(turnDir)

	return cmd.String(), consumed
}

// skipExpectFurtherClearance skips "expect further clearance" and any following digits.
// Returns the new consumed position.
func skipExpectFurtherClearance(tokens []Token, start int) int {
	consumed := start

	// Skip filler words first
	for consumed < len(tokens) && IsFillerWord(strings.ToLower(tokens[consumed].Text)) {
		consumed++
	}

	// Look for "expect further clearance" pattern
	if consumed+2 < len(tokens) {
		t1 := strings.ToLower(tokens[consumed].Text)
		t2 := strings.ToLower(tokens[consumed+1].Text)
		t3 := strings.ToLower(tokens[consumed+2].Text)
		if t1 == "expect" && t2 == "further" && t3 == "clearance" {
			consumed += 3
			// Skip any following digits (the time, e.g., "1 2 3 0")
			for consumed < len(tokens) {
				if tokens[consumed].Type == TokenNumber || IsDigit(tokens[consumed].Text) {
					consumed++
				} else {
					break
				}
			}
		}
	}

	return consumed
}

// isCommandKeyword returns true if the word is (or fuzzy-matches) a command keyword.
// Used to detect when "contact" is followed by a command rather than a controller name.
func isCommandKeyword(word string) bool {
	commandKeywords := []string{
		"climb", "climbed", "climbing",
		"descend", "descended", "descending",
		"maintain",
		"turn", "left", "right",
		"heading",
		"speed", "reduce", "increase",
		"direct", "proceed",
		"cleared", "expect", "vectors",
		"squawk", "ident", "transponder",
		"cross", "expedite",
		"fly", "intercept",
		"cancel", "resume",
		"say",
	}
	for _, kw := range commandKeywords {
		if FuzzyMatch(word, kw, 0.8) {
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

// sayAgainCommandForArg returns the SAYAGAIN/TYPE command for a given argument type.
// Returns empty string if this argument type should not generate SAYAGAIN commands.
// SAYAGAIN commands are generated when STT recognizes command keywords but fails
// to extract the associated value (e.g., "fly heading blark bling five").
//
// The priority parameter is the template priority. Low-priority templates (< 5) are
// fallback matchers that shouldn't generate SAYAGAIN since they match broadly and
// often trigger on words that are part of other phrases.
func sayAgainCommandForArg(argType ArgType, priority int) string {
	// Don't generate SAYAGAIN for low-priority fallback templates
	if priority < 5 {
		return ""
	}

	switch argType {
	case ArgAltitude:
		return "SAYAGAIN/ALTITUDE"
	case ArgHeading:
		return "SAYAGAIN/HEADING"
	case ArgSpeed:
		return "SAYAGAIN/SPEED"
	case ArgApproach:
		return "SAYAGAIN/APPROACH"
	case ArgSquawk:
		return "SAYAGAIN/SQUAWK"
	case ArgFix:
		return "SAYAGAIN/FIX"
	case ArgDegrees:
		return "SAYAGAIN/TURN"
	default:
		// Other arg types (ArgNone, ArgSID, ArgSTAR, compound types) don't generate SAYAGAIN
		return ""
	}
}

// shouldGenerateSayAgain checks if we should generate a SAYAGAIN command after
// keywords matched but argument extraction failed.
// Returns false if:
// - No remaining tokens after keyword consumption
// - The next token is a command keyword (indicates a different command follows, not garbled argument)
// - The remaining tokens look like goodbye phrases or facility names (not commands)
func shouldGenerateSayAgain(tokens []Token, consumed int) bool {
	// No remaining tokens - nothing was garbled, just incomplete
	if consumed >= len(tokens) {
		return false
	}

	// Check if remaining token is a command keyword
	nextText := strings.ToLower(tokens[consumed].Text)
	if isCommandKeyword(nextText) {
		return false
	}

	// Check for facility type words that indicate this is controller identification, not a command
	// e.g., "contact socal approach 12515" - "approach" is part of facility name
	facilityWords := map[string]bool{
		"approach": true, "departure": true, "center": true, "tower": true,
		"ground": true, "radio": true,
	}
	if facilityWords[nextText] {
		return false
	}

	// Check for informational/mode words that complete certain phrases
	// e.g., "squawk VFR" - VFR is not a garbled squawk code, it's the mode
	// e.g., "vectors for sequence" - sequence is not a garbled approach, it's spacing
	infoWords := map[string]bool{
		"vfr": true, "ifr": true, "frequency": true, "change": true,
		"approved": true, "terminated": true, "services": true,
		"sequence": true, "sequencing": true, "spacing": true,
	}
	if infoWords[nextText] {
		return false
	}
	// Check "for <info_word>" pattern (e.g., "vectors for sequence")
	if nextText == "for" && consumed+1 < len(tokens) {
		afterFor := strings.ToLower(tokens[consumed+1].Text)
		if infoWords[afterFor] {
			return false
		}
	}

	// Check for goodbye/acknowledgment phrases that often end transmissions
	// e.g., "see ya", "good day", "have a good one"
	goodbyeWords := map[string]bool{
		"see": true, "seeya": true, "goodbye": true, "good": true,
		"have": true, "roger": true, "wilco": true, "thanks": true,
		"thank": true, "you": true, "ya": true, "day": true,
	}
	return !goodbyeWords[nextText]
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
		corrected := alt * 10
		// Never correct into flight levels (>=18,000 ft) - requires explicit "flight level" speech
		if corrected >= 180 {
			return alt, false
		}
		correctedFeet := alt * 1000
		if correctedFeet > acAltitude && correctedFeet <= 60000 {
			return corrected, true
		}
	}
	if isDescend && altFeet >= acAltitude {
		// Descend but altitude is at or above current - try *10
		corrected := alt * 10
		// Never correct into flight levels (>=18,000 ft) - requires explicit "flight level" speech
		if corrected >= 180 {
			return alt, false
		}
		correctedFeet := alt * 1000
		if correctedFeet < acAltitude && correctedFeet >= 1000 {
			return corrected, true
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

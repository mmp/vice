package stt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
)

// CommandMatch represents a matched command.
type CommandMatch struct {
	Command    string      // The output command string
	Name       string      // Matched template's name, for logging
	Confidence float64     // Per-token match score
	Score      float64     // Coverage-adjusted score used to rank competing matches
	Kind       commandKind // What kind of segment this template matched
	Consumed   int         // Tokens consumed
	IsThen     bool        // Whether this is a "then" sequenced command
	IsSayAgain bool        // True if this is a partial match that needs say-again
}

// categoryRule maps a command pattern to its category. First match wins.
type categoryRule struct {
	match    func(cmd string) bool
	category string
}

// categoryRules defines command-to-category mappings, tried in order.
// Categories prevent duplicate commands of the same type in a single transmission.
var categoryRules = []categoryRule{
	// Advisory commands: named explicitly since their letters collide
	// with the single-letter command prefixes below (CWT would read as a
	// C-approach, AP/1/5 as an A-crossing, TRAFFIC as a T-turn).
	{match: func(cmd string) bool { return cmd == "CWT" }, category: "wake"},
	{match: func(cmd string) bool { return cmd == "AP" || strings.HasPrefix(cmd, "AP/") }, category: "advisory"},
	{match: func(cmd string) bool { return strings.HasPrefix(cmd, "TRAFFIC") }, category: "traffic"},
	{match: func(cmd string) bool {
		return cmd == "VISSEP" || cmd == "RST" || cmd == "TO" || strings.HasPrefix(cmd, "TO/")
	}, category: ""},
	// T-prefix: turn commands (TL, TR) → heading
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && (cmd[1] == 'L' || cmd[1] == 'R')
	}, category: "heading"},
	// Bare turns to a heading (L270, R080) → heading; L/R followed by a
	// letter is left/right direct-to-fix (LDDARIC) → navigation.
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R') && cmd[1] >= '0' && cmd[1] <= '9'
	}, category: "heading"},
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R') && cmd[1] == 'D'
	}, category: "navigation"},
	// T-prefix: then-descend/climb (TD, TC) → altitude
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && (cmd[1] == 'D' || cmd[1] == 'C')
	}, category: "altitude"},
	// T-prefix: then-speed (TS) → speed
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && cmd[1] == 'S'
	}, category: "speed"},
	// T-prefix: then-heading (TH) → heading
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && cmd[1] == 'H'
	}, category: "heading"},
	// D/C/A + digit → altitude (D40, C40, A40)
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'D' || cmd[0] == 'C' || cmd[0] == 'A') && cmd[1] >= '0' && cmd[1] <= '9'
	}, category: "altitude"},
	// D with /H → depart_heading
	{match: func(cmd string) bool {
		return cmd[0] == 'D' && strings.Contains(cmd, "/H")
	}, category: "depart_heading"},
	// D + letter → navigation (direct-to-fix)
	{match: func(cmd string) bool { return cmd[0] == 'D' }, category: "navigation"},
	// A with / → cleared_approach (at fix cleared approach: AHOLID/CI0L, AHOLID/I)
	{match: func(cmd string) bool {
		return cmd[0] == 'A' && strings.Contains(cmd, "/")
	}, category: "cleared_approach"},
	// A + letter → navigation
	{match: func(cmd string) bool { return cmd[0] == 'A' }, category: "navigation"},
	// C with / → crossing (cross fix at altitude/speed/mach: CFIX/A40, CFIX/S250, CFIX/M80)
	{match: func(cmd string) bool {
		return cmd[0] == 'C' && strings.Contains(cmd, "/")
	}, category: "crossing"},
	// C + letter without / → cleared_approach (CI9L)
	{match: func(cmd string) bool { return cmd[0] == 'C' }, category: "cleared_approach"},
	// SAYAGAIN → no category
	{match: func(cmd string) bool { return strings.HasPrefix(cmd, "SAYAGAIN") }, category: ""},
	// S → speed
	{match: func(cmd string) bool { return cmd[0] == 'S' }, category: "speed"},
	// H → heading
	{match: func(cmd string) bool { return cmd[0] == 'H' }, category: "heading"},
	// E → expect_approach
	{match: func(cmd string) bool { return cmd[0] == 'E' }, category: "expect_approach"},
}

// CommandCategory returns the category of an output command ("heading",
// "altitude", "speed", ...), or "" if it has none. Exported for analysis
// tooling (cmd/stteval).
func CommandCategory(cmd string) string {
	return getCommandCategory(cmd)
}

// getCommandCategory returns the category of a command based on its prefix.
// Categories are used to prevent duplicate commands of the same type in a single transmission.
func getCommandCategory(cmd string) string {
	if cmd == "" {
		return ""
	}
	for _, rule := range categoryRules {
		if rule.match(cmd) {
			return rule.category
		}
	}
	return ""
}

// looksLikeAltitude returns true if a token looks like an altitude value.
// Used for the "at {altitude}" implicit-then pattern in ParseCommands.
func looksLikeAltitude(t Token) bool {
	if t.Type == TokenAltitude {
		return true
	}
	if t.Type == TokenNumber {
		if t.Value >= 100 && t.Value <= 600 {
			return true
		}
		if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
			return true
		}
	}
	return false
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

	// Words that should not be consumed as part of a STAR name - these are
	// command keywords that likely follow the STAR reference
	excludeTrailing := map[string]bool{
		"arrival": true, "approach": true, "departure": true,
	}

	// Build candidate phrases (1-4 words for STAR names)
	for length := min(4, len(tokens)); length >= 1; length-- {
		// Don't consume trailing command keywords as part of the STAR name
		lastToken := strings.ToLower(tokens[length-1].Text)
		if length > 1 && excludeTrailing[lastToken] {
			continue
		}
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
			fmt.Fprintf(&code, "%04d", t.Value)
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

		// Once we have both degree value and direction, handle exit conditions
		if deg > 0 && dir != "" {
			if hasDegreesKeyword {
				// Already have everything we need - stop scanning
				break
			}
			// Continue scanning a couple more tokens for "degrees" keyword
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

// speedUntilResult represents the result of extracting a speed "until" specification.
type speedUntilResult struct {
	suffix string // e.g., "ROSLY", "5DME", "6"
}

// extractSpeedUntil extracts a speed "until" specification from tokens.
// Looks for patterns like:
//   - "until ROSLY" → fix name
//   - "until 5 DME" / "until 5 D M E" → DME distance
//   - "until 6 mile final" → mile final
//
// Also handles common STT errors for "until": "unto", "on two", "intel", "and tell"
// Returns the result and number of tokens consumed.
func extractSpeedUntil(tokens []Token, ac Aircraft) (speedUntilResult, int) {
	if len(tokens) == 0 {
		return speedUntilResult{}, 0
	}

	consumed := 0

	// Look for "until" keyword or its STT variants. A little garble may
	// intervene ("one seven zero now its to bokus"): tolerate up to two
	// noise words, but never reach past a command keyword — that starts a
	// new command and any "to" beyond it belongs there.
	untilFound := false
	noise := 0
	for consumed < len(tokens) && consumed < 6 {
		text := strings.ToLower(tokens[consumed].Text)
		if isUntilKeyword(text) {
			untilFound = true
			consumed++
			break
		}
		// Skip filler words and speed-related words (e.g., "knots" between speed and "until")
		if IsFillerWord(text) || isSpeedFillerWord(text) {
			consumed++
			continue
		}
		if IsCommandKeyword(text) || noise >= 2 {
			break
		}
		noise++
		consumed++
	}

	if !untilFound || consumed >= len(tokens) {
		return speedUntilResult{}, 0
	}

	// Skip filler words between "until" and the number (e.g., "until the 5 mile final")
	for consumed < len(tokens) && IsFillerWord(strings.ToLower(tokens[consumed].Text)) {
		consumed++
	}
	if consumed >= len(tokens) {
		return speedUntilResult{}, 0
	}

	// Now try to match what comes after "until"

	// 1. Try DME pattern: number followed by "DME" or "D M E"
	if dme, dmeConsumed := extractDME(tokens[consumed:]); dmeConsumed > 0 {
		return speedUntilResult{suffix: fmt.Sprintf("%dDME", dme)}, consumed + dmeConsumed
	}

	// 2. Try mile final pattern: number followed by "mile(s) final"
	if miles, mileConsumed := extractMileFinal(tokens[consumed:]); mileConsumed > 0 {
		return speedUntilResult{suffix: fmt.Sprintf("%d", miles)}, consumed + mileConsumed
	}

	// 3. Try fix name match from aircraft's known fixes (includes approach fixes)
	if fix, _, fixConsumed := extractFix(tokens[consumed:], ac.Fixes); fixConsumed > 0 {
		return speedUntilResult{suffix: fix}, consumed + fixConsumed
	}

	// 4. Bare number after "until" — commonly shortened "until N mile final"
	if tokens[consumed].Type == TokenNumber && tokens[consumed].Value >= 1 && tokens[consumed].Value <= 20 {
		return speedUntilResult{suffix: fmt.Sprintf("%d", tokens[consumed].Value)}, consumed + 1
	}

	return speedUntilResult{}, 0
}

// isUntilKeyword checks if a word is "until" or a common STT transcription error.
func isUntilKeyword(text string) bool {
	switch text {
	case "until", "unto", "intel", "untill", "intil", "til", "till":
		return true
	case "to":
		// "maintain one seven zero knots TO five miles final": accepted
		// because the caller validates that a real until-target (fix,
		// DME, mile final) follows.
		return true
	}
	return false
}

// isSpeedFillerWord checks if a word commonly appears between a speed and "until".
// For example: "maintain 180 knots or greater until 5 mile final" - skip "knots", "or", "greater".
func isSpeedFillerWord(text string) bool {
	switch text {
	case "knots", "kts", "knot", "or", "greater", "better", "less":
		return true
	}
	return false
}

// extractDME extracts a DME distance from tokens.
// Handles: "5 DME", "5 D M E", "5DME"
func extractDME(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	// First token should be a number
	if tokens[0].Type != TokenNumber || tokens[0].Value < 1 || tokens[0].Value > 30 {
		return 0, 0
	}
	num := tokens[0].Value
	consumed := 1

	if consumed >= len(tokens) {
		return 0, 0
	}

	// Check for "DME" or "D M E" or "D. M. E." patterns. "dm" is a common STT
	// clipping of "DME" that the short-word fuzzy guard rejects, so match it
	// explicitly.
	nextText := strings.ToLower(tokens[consumed].Text)
	if nextText == "dme" || nextText == "dm" || FuzzyMatch(nextText, "dme", 0.8) {
		return num, consumed + 1
	}

	// Check for spelled out "D M E", or "D M" (trailing E clipped).
	if nextText == "d" && consumed+1 < len(tokens) && strings.ToLower(tokens[consumed+1].Text) == "m" {
		if consumed+2 < len(tokens) && strings.ToLower(tokens[consumed+2].Text) == "e" {
			return num, consumed + 3
		}
		return num, consumed + 2
	}

	return 0, 0
}

// extractMileFinal extracts a mile final specification from tokens.
// Handles: "6 mile final", "5 miles final"
func extractMileFinal(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	// First token should be a number. If it's a word, try fuzzy matching
	// against digit words — in the structural pattern [word] mile final,
	// the first position must be a number semantically.
	var num int
	if tokens[0].Type == TokenNumber && tokens[0].Value >= 1 && tokens[0].Value <= 20 {
		num = tokens[0].Value
	} else if tokens[0].Type == TokenWord && len(tokens) >= 3 {
		next1 := strings.ToLower(tokens[1].Text)
		next2 := strings.ToLower(tokens[2].Text)
		if (next1 == "mile" || next1 == "miles") && next2 == "final" {
			if v := fuzzyMatchDigitWord(tokens[0].Text); v > 0 {
				num = v
			}
		}
	}
	if num == 0 {
		return 0, 0
	}
	consumed := 1

	if consumed >= len(tokens) {
		return 0, 0
	}

	// Check for "mile" or "miles"
	nextText := strings.ToLower(tokens[consumed].Text)
	if nextText != "mile" && nextText != "miles" {
		return 0, 0
	}
	consumed++

	if consumed >= len(tokens) {
		return 0, 0
	}

	// Check for "final"
	if strings.ToLower(tokens[consumed].Text) == "final" {
		return num, consumed + 1
	}

	return 0, 0
}

// fuzzyMatchDigitWord tries to fuzzy-match a garbled word against digit words
// 1-10. Returns the digit value on match, or 0 if no match exceeds the threshold.
func fuzzyMatchDigitWord(word string) int {
	word = strings.ToLower(word)
	targets := []struct {
		word  string
		value int
	}{
		{"one", 1}, {"two", 2}, {"three", 3}, {"four", 4}, {"five", 5},
		{"six", 6}, {"seven", 7}, {"eight", 8}, {"nine", 9}, {"ten", 10},
	}

	bestVal := 0
	bestScore := 0.65 // minimum threshold
	for _, t := range targets {
		score := JaroWinkler(word, t.word)
		if score > bestScore {
			bestScore = score
			bestVal = t.value
		}
	}
	return bestVal
}

// Compile-time check that slices package is used
var _ = slices.Contains[[]string]

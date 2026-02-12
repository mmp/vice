package stt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// CommandMatch represents a matched command.
type CommandMatch struct {
	Command    string  // The output command string
	Confidence float64 // Match confidence
	Consumed   int     // Tokens consumed
	IsThen     bool    // Whether this is a "then" sequenced command
	IsSayAgain bool    // True if this is a partial match that needs say-again
}

// getCommandCategory returns the category of a command based on its prefix.
// Categories are used to prevent duplicate commands of the same type in a single transmission.
// For example, once an altitude command is matched, we shouldn't match another altitude command.
func getCommandCategory(cmd string) string {
	if cmd == "" {
		return ""
	}

	// Handle "then" variants by stripping T prefix
	if len(cmd) > 1 && cmd[0] == 'T' {
		// Check if second char indicates a turn (TL, TR) vs then-command (TD, TC, TS, TH)
		switch cmd[1] {
		case 'L', 'R':
			// TL20, TR20 are turn commands (heading category)
			return "heading"
		case 'D', 'C':
			// TD40, TC40 are then-descend/climb (altitude category)
			return "altitude"
		case 'S':
			// TS180 is then-speed (speed category)
			return "speed"
		case 'H':
			// TH270 is then-heading (heading category)
			return "heading"
		}
	}

	switch cmd[0] {
	case 'D', 'C', 'A':
		// D40 (descend), C40 (climb), A40 (altitude assignment)
		// But D can also be direct-to-fix like DFORPE, and C can be approach clearance like CI9L
		// Check if followed by digit to determine if it's altitude vs navigation/approach
		if len(cmd) > 1 && cmd[1] >= '0' && cmd[1] <= '9' {
			return "altitude"
		}
		// D followed by letter is direct-to-fix or depart-fix-heading
		if cmd[0] == 'D' {
			if strings.Contains(cmd, "/H") {
				return "depart_heading"
			}
			return "navigation"
		}
		// A followed by letter could be approach-related (AFIX/C for "at fix cleared approach")
		if cmd[0] == 'A' {
			return "navigation"
		}
		// C followed by letter could be:
		// - Crossing restriction: CNOLEY/A40 (contains '/') - categorize by suffix
		// - Approach clearance: CI9L (no '/')
		if strings.Contains(cmd, "/") {
			// Crossing restriction - categorize by what follows the '/'
			if strings.Contains(cmd, "/A") {
				return "altitude"
			}
			if strings.Contains(cmd, "/S") {
				return "speed"
			}
			return "navigation" // default for other crossing restrictions
		}
		// C followed by letter without '/' is approach clearance (CI9L)
		return "cleared_approach"
	case 'S':
		// S180 (speed), but SAYAGAIN is not a speed command
		if strings.HasPrefix(cmd, "SAYAGAIN") {
			return ""
		}
		return "speed"
	case 'H':
		// H270 (heading)
		return "heading"
	case 'E':
		// EI9L (expect approach)
		// Use different category from cleared_approach so both can appear in same transmission
		return "expect_approach"
	}

	return ""
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
			// Handle 3-digit values that are likely thousands with decimal artifacts
			// e.g., "9.00" → 900 means 9000 feet, "5.00" → 500 means 5000 feet
			// These are outside the ambiguous speed range (100-400)
			if t.Value >= 500 && t.Value <= 900 && t.Value%100 == 0 {
				encoded := t.Value / 10
				logLocalStt("  extractAltitude: interpreted %d as %d000 ft (encoded %d)", t.Value, t.Value/100, encoded)
				return encoded, i + 1
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

		// Skip numbers that follow "speed" keyword - those are speed values, not headings.
		// This prevents "left approach speed 180" from matching as heading 180.
		if i > 0 && strings.ToLower(tokens[i-1].Text) == "speed" {
			continue
		}

		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			hdg := t.Value

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
			// Round down to nearest 10 - ATC speeds are always multiples of 10.
			if t.Value >= 100 && t.Value <= 400 {
				rounded := (t.Value / 10) * 10
				if rounded != t.Value {
					logLocalStt("  extractSpeed: rounded %d -> %d (to nearest 10)", t.Value, rounded)
				}
				return rounded, i + 1
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
			// PhoneticMatch check first - handles STT errors that add syllables (e.g., "klomanad" → "Clomn")
			// This is checked regardless of length ratio since phonetic matching accounts for extra syllables.
			if PhoneticMatch(phrase, spokenName) && (bestScore < 0.80 || (bestScore == 0.80 && fixID < bestFix)) {
				bestFix = fixID
				bestScore = 0.80
				bestLength = length
			}
			// Reject if phrase is much longer than fix name (prevents "pucky heading 180" matching "Pucky")
			// Only applies to non-phonetic JW matching below.
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
			// Try phonetic matching against the fix identifier itself.
			// STT may transcribe the identifier pronunciation rather than the
			// spoken form (e.g., "bacal" for fix BAKEL, spoken as "bake").
			fixIDLower := strings.ToLower(fixID)
			if fixIDLower != strings.ToLower(spokenName) {
				if PhoneticMatch(phrase, fixIDLower) && (bestScore < 0.78 || (bestScore == 0.78 && fixID < bestFix)) {
					bestFix = fixID
					bestScore = 0.78
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
// assignedApproach is the approach the aircraft was previously told to expect (e.g., "ILS Runway 10R").
// When there are multiple matches with equal scores, the assigned approach is preferred.
func extractApproach(tokens []Token, approaches map[string]string, assignedApproach string) (string, float64, int) {
	if len(tokens) == 0 || len(approaches) == 0 {
		return "", 0, 0
	}

	// First, try type+number matching: extract approach type and runway number from tokens,
	// then find a candidate that matches both. This handles garbage words between type and
	// number (e.g., "ils front of a niner" should match "I L S runway niner" → I9).
	if appr, conf, consumed := matchApproachByTypeAndNumber(tokens, approaches, assignedApproach); consumed > 0 {
		return appr, conf, consumed
	}

	var bestAppr string
	var bestScore float64
	var bestLength int

	// Extract spoken direction from all tokens (left/right/center at any position)
	// This helps prefer approaches matching the spoken direction.
	spokenDir := extractSpokenDirection(tokens)

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
			// Prefer assigned approach on ties, otherwise use alphabetically earlier apprID for determinism.
			for spokenName, apprID := range approaches {
				score := JaroWinkler(variant, spokenName)
				if score >= 0.80 {
					// Bonus for matching spoken direction: if user said "left" and approach ends in "L",
					// boost the score. This helps "ils ... left" match "I7L" over "I28".
					if spokenDir != 0 && approachHasDirection(apprID, spokenDir) {
						score += 0.05
					}

					// Bonus for matching the assigned/expected approach: prefer the
					// approach the aircraft was told to expect. This helps when the
					// approach type is garbled but the runway matches.
					if approachMatchesAssigned(apprID, assignedApproach) {
						score += 0.03
					}

					isBetter := score > bestScore
					if !isBetter && score == bestScore {
						// Tie-breaker: prefer assigned approach, then alphabetically earlier
						bestMatchesAssigned := approachMatchesAssigned(bestAppr, assignedApproach)
						thisMatchesAssigned := approachMatchesAssigned(apprID, assignedApproach)
						if thisMatchesAssigned && !bestMatchesAssigned {
							isBetter = true
						} else if thisMatchesAssigned == bestMatchesAssigned && apprID < bestAppr {
							isBetter = true
						}
					}
					if isBetter {
						bestAppr = apprID
						bestScore = score
						bestLength = length
					}
				}
			}
		}
	}

	if bestAppr != "" {
		return bestAppr, bestScore, bestLength
	}

	// Fallback: match by runway number + direction, then disambiguate by approach type.
	// This handles garbled approach types like "a less four right" → "ILS runway four right".
	// Limit search range: don't look past "cleared" which indicates a new clearance command.
	// Other command keywords like "turn" or "heading" are NOT boundaries here because they
	// commonly appear as garbled approach text (e.g., "isle turn to new" for "ILS runway").
	searchTokens := tokens
	for i := 1; i < len(tokens); i++ {
		if strings.ToLower(tokens[i].Text) == "cleared" {
			searchTokens = tokens[:i]
			break
		}
	}
	if runwayNum, runwayDir, numPos := extractRunwayNumber(searchTokens); runwayNum != "" {
		runwaySpoken := runwayNum
		if runwayDir != "" {
			runwaySpoken += " " + runwayDir
		}

		// Filter approaches to those matching the runway
		var matchingApproaches []struct {
			spokenName string
			apprID     string
		}
		for spokenName, apprID := range approaches {
			if runwayMatches(strings.ToLower(spokenName), runwaySpoken) {
				matchingApproaches = append(matchingApproaches, struct {
					spokenName string
					apprID     string
				}{spokenName, apprID})
			}
		}

		if len(matchingApproaches) == 1 {
			// Only one approach matches the runway - use it
			consumed := numPos + 1
			if runwayDir != "" {
				consumed++
			}
			logLocalStt("  extractApproach: unique runway match %q -> %q", runwaySpoken, matchingApproaches[0].apprID)
			return matchingApproaches[0].apprID, 0.85, consumed
		} else if len(matchingApproaches) > 1 {
			// Multiple approaches match - disambiguate using prefix tokens
			// Get tokens before the runway number, stopping at "runway" keyword
			var prefixParts []string
			for i := range numPos {
				text := strings.ToLower(tokens[i].Text)
				if text == "runway" {
					break // Don't include "runway" in the prefix
				}
				if tokens[i].Type == TokenNumber {
					prefixParts = append(prefixParts, spokenDigits(tokens[i].Value))
				} else {
					prefixParts = append(prefixParts, text)
				}
			}
			prefixPhrase := strings.Join(prefixParts, " ")

			// Find the best matching approach by comparing prefix to approach type portion
			var bestMatch string
			var bestMatchScore float64
			for _, ma := range matchingApproaches {
				// Extract the approach type portion (before "runway")
				spokenLower := strings.ToLower(ma.spokenName)
				typeEnd := strings.Index(spokenLower, "runway")
				if typeEnd == -1 {
					typeEnd = len(spokenLower)
				}
				approachTypePortion := strings.TrimSpace(spokenLower[:typeEnd])

				// Compare using Jaro-Winkler
				score := JaroWinkler(prefixPhrase, approachTypePortion)

				// Also try phonetic matching for short garbled inputs
				if PhoneticMatch(prefixPhrase, approachTypePortion) {
					score = max(score, 0.85)
				}

				if score > bestMatchScore || (score == bestMatchScore && ma.apprID < bestMatch) {
					bestMatchScore = score
					bestMatch = ma.apprID
				}
			}

			// When disambiguating between multiple runway matches, pick the best match.
			// When the prefix is garbled, prefer the assigned approach when available.
			if bestMatch != "" && bestMatchScore >= 0.30 {
				// If we have an assigned approach, check if one of the matching
				// approaches matches the assigned approach's type. This handles
				// garbled prefixes like "off" for "ILS".
				if bestMatchScore < 0.80 && assignedApproach != "" {
					assignedLower := strings.ToLower(assignedApproach)
					// Extract RNAV variant from assigned approach (e.g., "z" from "rnav z runway 27")
					assignedVariant := extractRnavVariant(assignedLower)

					var typeMatch string // best type-only match
					for _, ma := range matchingApproaches {
						if matchesAssignedRunway(ma.apprID, assignedApproach) {
							// Check if approach type matches assigned
							spokenLower := strings.ToLower(ma.spokenName)
							isILS := strings.Contains(assignedLower, "ils") &&
								(strings.Contains(spokenLower, "i l s") || strings.Contains(spokenLower, "ils"))
							isRNAV := strings.Contains(assignedLower, "rnav") &&
								(strings.Contains(spokenLower, "r-nav") || strings.Contains(spokenLower, "rnav"))
							if isILS || isRNAV {
								// For RNAV variants, also check variant letter (Z/Y/W/X)
								if isRNAV && assignedVariant != "" {
									idVariant := extractApproachIDVariant(ma.apprID)
									if idVariant == assignedVariant {
										bestMatch = ma.apprID
										logLocalStt("  extractApproach: runway match, low score (%.2f), using assigned approach %q (variant %q)",
											bestMatchScore, bestMatch, assignedVariant)
										typeMatch = "" // exact match found, skip fallback
										break
									}
									if typeMatch == "" {
										typeMatch = ma.apprID
									}
									continue
								}
								bestMatch = ma.apprID
								logLocalStt("  extractApproach: runway match, low score (%.2f), using assigned approach %q",
									bestMatchScore, bestMatch)
								typeMatch = "" // exact match found
								break
							}
						}
					}
					if typeMatch != "" {
						// Fallback: type matched but variant didn't — use first type match
						bestMatch = typeMatch
						logLocalStt("  extractApproach: runway match, low score (%.2f), using assigned approach %q (variant fallback)",
							bestMatchScore, bestMatch)
					}
				}
				consumed := numPos + 1
				if runwayDir != "" {
					consumed++
				}
				logLocalStt("  extractApproach: runway match with type disambiguation %q -> %q (score=%.2f)",
					runwaySpoken, bestMatch, bestMatchScore)
				return bestMatch, 0.80, consumed
			}
		}
	}

	// Final fallback: when approach type is garbled but we have a direction that matches
	// the assigned approach, use it. This handles cases like "at last turn two two left"
	// where "at last turn" is garbled "ILS" and we have assigned approach "ILS Runway 22L".
	// Use the bounded search tokens so we don't match direction words from subsequent commands.
	boundedDir := extractSpokenDirection(searchTokens)
	if boundedDir != 0 && assignedApproach != "" {
		// Extract direction from assigned approach
		assignedUpper := strings.ToUpper(assignedApproach)
		var assignedDir byte
		if len(assignedUpper) > 0 {
			lastChar := assignedUpper[len(assignedUpper)-1]
			if lastChar == 'L' || lastChar == 'R' || lastChar == 'C' {
				assignedDir = lastChar
			}
		}

		if assignedDir != 0 && assignedDir == boundedDir {
			// Extract approach type from assigned approach (e.g., "ILS Runway 13L" → "ils")
			assignedLower := strings.ToLower(assignedApproach)
			var assignedType string
			if strings.Contains(assignedLower, "ils") {
				assignedType = "ils"
			} else if strings.Contains(assignedLower, "rnav") || strings.Contains(assignedLower, "r-nav") {
				assignedType = "rnav"
			} else if strings.Contains(assignedLower, "visual") {
				assignedType = "visual"
			} else if strings.Contains(assignedLower, "vor") {
				assignedType = "vor"
			} else if strings.Contains(assignedLower, "localizer") {
				assignedType = "localizer"
			}

			// Find the approach ID that best matches the assigned approach.
			// Prefer the approach whose type matches the assigned approach type.
			var bestApprID string
			var bestSpokenName string
			for spokenName, apprID := range approaches {
				if matchesAssignedRunway(apprID, assignedApproach) {
					spokenLower := strings.ToLower(spokenName)
					thisMatchesType := assignedType != "" && approachTypeMatches(spokenLower, assignedType)
					bestMatchesType := assignedType != "" && approachTypeMatches(strings.ToLower(bestSpokenName), assignedType)

					if bestApprID == "" {
						bestApprID = apprID
						bestSpokenName = spokenName
					} else if thisMatchesType && !bestMatchesType {
						bestApprID = apprID
						bestSpokenName = spokenName
					} else if thisMatchesType == bestMatchesType && apprID < bestApprID {
						bestApprID = apprID // Alphabetical tiebreaker for determinism
						bestSpokenName = spokenName
					}
				}
			}
			if bestApprID != "" {
				// Find position of direction word to calculate consumed tokens
				consumed := len(searchTokens)
				for i, t := range searchTokens {
					text := strings.ToLower(t.Text)
					if text == "left" || text == "right" || text == "center" ||
						text == "l" || text == "r" || text == "c" || text == "west" {
						consumed = i + 1
						break
					}
				}
				logLocalStt("  extractApproach: no type match, falling back to assigned approach %q (dir=%c)",
					bestApprID, boundedDir)
				return bestApprID, 0.75, consumed
			}
		}
	}

	// Fallback: when the approach type and runway are garbled beyond recognition
	// but the word "approach" is present, confirming approach context. If there's
	// only one candidate, match it.
	if slices.ContainsFunc(tokens, func(t Token) bool {
		return t.Type == TokenWord && strings.ToLower(t.Text) == "approach"
	}) && len(approaches) == 1 {
		var apprID string
		for _, id := range approaches {
			apprID = id
		}
		logLocalStt("  extractApproach: single candidate with 'approach' keyword -> %q", apprID)
		return apprID, 0.70, len(tokens)
	}

	return "", 0, 0
}

// approachMatchesAssigned checks if an approach ID matches the assigned approach.
// For example, "I0R" matches "ILS Runway 10R" because both end with "R" (runway 10 Right).
func approachMatchesAssigned(approachID, assignedApproach string) bool {
	if assignedApproach == "" || approachID == "" {
		return false
	}

	// Extract runway direction from assigned approach (last character if it's L/R/C)
	assignedApproach = strings.ToUpper(strings.TrimSpace(assignedApproach))
	var assignedDir byte
	if len(assignedApproach) > 0 {
		lastChar := assignedApproach[len(assignedApproach)-1]
		if lastChar == 'L' || lastChar == 'R' || lastChar == 'C' {
			assignedDir = lastChar
		}
	}

	// Extract runway direction from approach ID (last character if it's L/R/C)
	approachID = strings.ToUpper(strings.TrimSpace(approachID))
	var approachDir byte
	if len(approachID) > 0 {
		lastChar := approachID[len(approachID)-1]
		if lastChar == 'L' || lastChar == 'R' || lastChar == 'C' {
			approachDir = lastChar
		}
	}

	// If both have directions, they should match
	if assignedDir != 0 && approachDir != 0 {
		return assignedDir == approachDir
	}

	// If neither has a direction, or only one has a direction, consider it a match
	// (this allows for approaches like "I9" to match "ILS Runway 9")
	return true
}

// matchesAssignedRunway checks if an approach ID matches the runway in the assigned approach.
// For example, "I8R" should match "ILS Runway 18R" because both refer to runway 18 right.
// The approach ID format is: type prefix + runway number (possibly compressed) + direction.
// Examples: I8R (ILS 18R), I2L (ILS 22L), R1R (RNAV 31R)
func matchesAssignedRunway(approachID, assignedApproach string) bool {
	if assignedApproach == "" || approachID == "" {
		return false
	}

	// Extract runway designator from assigned approach (e.g., "18R" from "ILS Runway 18R")
	assignedUpper := strings.ToUpper(assignedApproach)
	runwayIdx := strings.Index(assignedUpper, "RUNWAY")
	if runwayIdx == -1 {
		// No "RUNWAY" keyword, try matching just by direction
		return approachMatchesAssigned(approachID, assignedApproach)
	}

	// Get everything after "RUNWAY " and trim
	runwayPart := strings.TrimSpace(assignedUpper[runwayIdx+6:])
	// runwayPart is now something like "18R" or "22L" or "9"

	// Extract the numeric part and direction from the assigned runway
	var assignedNum string
	var assignedDir byte
	for i, c := range runwayPart {
		if c >= '0' && c <= '9' {
			assignedNum += string(c)
		} else if c == 'L' || c == 'R' || c == 'C' {
			assignedDir = byte(c)
			break
		} else if c != ' ' {
			// Stop at any non-digit, non-direction character
			break
		}
		_ = i
	}

	if assignedNum == "" {
		return false
	}

	// Extract runway info from approach ID (e.g., "8R" from "I8R")
	// Skip the type prefix (first letter for ILS/RNAV, or first two for variants like "RY")
	approachUpper := strings.ToUpper(approachID)
	var idNum string
	var idDir byte
	for _, c := range approachUpper {
		if c >= '0' && c <= '9' {
			idNum += string(c)
		} else if c == 'L' || c == 'R' || c == 'C' {
			idDir = byte(c)
		}
	}

	if idNum == "" {
		return false
	}

	// Check if directions match (if both have directions)
	if assignedDir != 0 && idDir != 0 && assignedDir != idDir {
		return false
	}

	// Check if runway numbers match
	// The approach ID may use a compressed format: "8R" for runway 18R, "2L" for 22L
	// So we check if the assigned runway ends with the ID runway number
	return strings.HasSuffix(assignedNum, idNum)
}

// extractRnavVariant extracts the RNAV variant letter from an assigned approach string.
// E.g., "rnav z runway 27" → "z", "rnav yankee runway 13l" → "y".
func extractRnavVariant(assignedLower string) string {
	idx := strings.Index(assignedLower, "rnav")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(assignedLower[idx+4:])
	if rest == "" {
		return ""
	}
	// The variant is the first word after "rnav": "z", "y", "x", "w", "zulu", "yankee", etc.
	word := strings.Fields(rest)[0]
	switch word {
	case "z", "zulu":
		return "z"
	case "y", "yankee":
		return "y"
	case "x", "x-ray", "xray":
		return "x"
	case "w", "whiskey":
		return "w"
	}
	return ""
}

// extractApproachIDVariant extracts the RNAV variant from an approach ID.
// E.g., "RZ7" → "z", "RY1L" → "y", "I3R" → "".
func extractApproachIDVariant(approachID string) string {
	upper := strings.ToUpper(approachID)
	if len(upper) < 2 || upper[0] != 'R' {
		return ""
	}
	switch upper[1] {
	case 'Z':
		return "z"
	case 'Y':
		return "y"
	case 'X':
		return "x"
	case 'W':
		return "w"
	}
	return ""
}

// extractSpokenDirection looks for a direction word (left/right/center) in the tokens.
// Returns 'L', 'R', 'C', or 0 if no direction found.
func extractSpokenDirection(tokens []Token) byte {
	for _, t := range tokens {
		switch strings.ToLower(t.Text) {
		case "left", "l", "west": // "west" is STT error for "left"
			return 'L'
		case "right", "r":
			return 'R'
		case "center", "c":
			return 'C'
		}
	}
	return 0
}

// approachHasDirection checks if an approach ID ends with the given direction (L/R/C).
func approachHasDirection(approachID string, dir byte) bool {
	if len(approachID) == 0 || dir == 0 {
		return false
	}
	lastChar := approachID[len(approachID)-1]
	// Handle both upper and lower case
	if lastChar >= 'a' && lastChar <= 'z' {
		lastChar -= 32 // Convert to uppercase
	}
	return lastChar == dir
}

// matchApproachByTypeAndNumber tries to match approach by extracting the approach type
// (ILS, RNAV, visual, etc.) and runway number separately, ignoring garbage words between them.
// This handles cases like "ils front of a niner" where STT inserts garbage between type and number.
// assignedApproach is used to prefer the expected approach when there are ties.
// allowFallback controls whether to fall back to the assigned approach when the runway doesn't match.
// Set to true only when there's an explicit command keyword (cleared, expect).
func matchApproachByTypeAndNumber(tokens []Token, approaches map[string]string, assignedApproach string) (string, float64, int) {
	return matchApproachByTypeAndNumberWithFallback(tokens, approaches, assignedApproach, true)
}

// matchApproachByTypeAndNumberWithFallback is the core implementation with fallback control.
func matchApproachByTypeAndNumberWithFallback(tokens []Token, approaches map[string]string, assignedApproach string, allowFallback bool) (string, float64, int) {
	if len(tokens) == 0 {
		return "", 0, 0
	}

	// Extract approach type from the beginning of tokens
	approachType, typeConsumed := extractApproachType(tokens)
	if approachType == "" {
		return "", 0, 0
	}

	// Look for approach variant letter (e.g., "zulu", "yankee") between type and runway
	remainingTokens := tokens[typeConsumed:]
	approachVariant, variantConsumed := extractApproachVariant(remainingTokens)
	if variantConsumed > 0 {
		remainingTokens = remainingTokens[variantConsumed:]
	}

	// Look for runway number anywhere in the remaining tokens
	runwayNum, runwayDir, numPos := extractRunwayNumber(remainingTokens)
	if runwayNum == "" {
		// No valid runway number found, but if we have an assigned approach with matching
		// type and direction, use it. This handles cases like "ils turn 918 right" where
		// the runway number is garbled but we can still infer from the assigned approach.
		if allowFallback && assignedApproach != "" {
			// Look for direction word anywhere in remaining tokens
			spokenDir := extractSpokenDirection(remainingTokens)
			if spokenDir != 0 {
				assignedLower := strings.ToLower(assignedApproach)
				if approachTypeMatches(assignedLower, approachType) {
					// Extract direction from assigned approach
					var assignedDir byte
					if len(assignedApproach) > 0 {
						lastChar := assignedApproach[len(assignedApproach)-1]
						if lastChar == 'L' || lastChar == 'l' {
							assignedDir = 'L'
						} else if lastChar == 'R' || lastChar == 'r' {
							assignedDir = 'R'
						} else if lastChar == 'C' || lastChar == 'c' {
							assignedDir = 'C'
						}
					}

					if assignedDir != 0 && assignedDir == spokenDir {
						// Find the approach ID that best matches the assigned approach.
						// We need to match the full runway number, not just the direction,
						// because multiple approaches may have the same direction (e.g., I3R, I8R).
						var bestApprID string
						for spokenName, apprID := range approaches {
							spokenLower := strings.ToLower(spokenName)
							if !approachTypeMatches(spokenLower, approachType) {
								continue
							}
							// Check if this approach ID matches the assigned approach's runway
							// by comparing the runway number portion (e.g., "8R" in "I8R" vs "18R" in assigned)
							if matchesAssignedRunway(apprID, assignedApproach) {
								bestApprID = apprID
								break
							}
						}
						if bestApprID != "" {
							// Find position of direction word to calculate consumed tokens
							dirPos := -1
							for i, t := range remainingTokens {
								text := strings.ToLower(t.Text)
								if text == "left" || text == "right" || text == "center" ||
									text == "l" || text == "r" || text == "c" || text == "west" {
									dirPos = i
									break
								}
							}
							consumed := typeConsumed + variantConsumed + dirPos + 1
							logLocalStt("  matchApproachByTypeAndNumber: no valid runway, falling back to assigned approach %q (type=%q dir=%c)",
								bestApprID, approachType, spokenDir)
							return bestApprID, 0.80, consumed
						}
					}
				}
			}
		}
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
			"our": true, "at": true, // Common before "approach" in STT; "at" is garble for left/right
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

		// If we extracted a variant letter (e.g., "zulu" → "z"), the candidate must match it.
		// This distinguishes RNAV Z from RNAV Y approaches.
		if approachVariant != "" && !approachVariantMatches(apprID, approachVariant) {
			continue
		}

		// We have a type+number match - calculate confidence based on specificity
		score := 0.95 // High confidence for type+number match

		// Tie-breaker: prefer assigned approach, then alphabetically earlier
		isBetter := score > bestScore
		if !isBetter && score == bestScore && bestAppr != "" {
			bestMatchesAssigned := approachMatchesAssigned(bestAppr, assignedApproach)
			thisMatchesAssigned := approachMatchesAssigned(apprID, assignedApproach)
			if thisMatchesAssigned && !bestMatchesAssigned {
				isBetter = true
			} else if thisMatchesAssigned == bestMatchesAssigned && apprID < bestAppr {
				isBetter = true
			}
		}

		if isBetter || bestAppr == "" {
			bestAppr = apprID
			bestScore = score
		}
	}

	if bestAppr != "" {
		// Consumed = type tokens + variant tokens + position of number + 1 for number itself + 1 for direction if present
		consumed := typeConsumed + variantConsumed + numPos + 1
		if runwayDir != "" {
			consumed++ // Account for direction word (left/right/center)
		}
		logLocalStt("  matchApproachByTypeAndNumber: type=%q variant=%q runway=%q -> %q (consumed=%d)",
			approachType, approachVariant, runwaySpoken, bestAppr, consumed)
		return bestAppr, bestScore, consumed
	}

	// Fallback: if no runway matched but we have an assigned approach with matching type and direction,
	// use the assigned approach. This handles transcription errors like "runway 21 left" when only
	// "runway 31 left" exists. The type (ILS) and direction (left) match, just the runway number is wrong.
	// Only enabled when there's an explicit command keyword (cleared, expect) to avoid false positives
	// from implicit approach mentions that are purely contextual.
	if allowFallback && assignedApproach != "" && runwayDir != "" {
		assignedLower := strings.ToLower(assignedApproach)
		// Check if assigned approach has the same type
		if approachTypeMatches(assignedLower, approachType) {
			// Extract direction from assigned approach (last character if L/R/C)
			var assignedDir byte
			if len(assignedApproach) > 0 {
				lastChar := assignedApproach[len(assignedApproach)-1]
				if lastChar == 'L' || lastChar == 'l' {
					assignedDir = 'L'
				} else if lastChar == 'R' || lastChar == 'r' {
					assignedDir = 'R'
				} else if lastChar == 'C' || lastChar == 'c' {
					assignedDir = 'C'
				}
			}

			// Normalize spoken direction to single char
			var spokenDirChar byte
			switch runwayDir {
			case "left":
				spokenDirChar = 'L'
			case "right":
				spokenDirChar = 'R'
			case "center":
				spokenDirChar = 'C'
			}

			// If directions match, find the approach ID for the assigned approach
			if assignedDir != 0 && assignedDir == spokenDirChar {
				for spokenName, apprID := range approaches {
					spokenLower := strings.ToLower(spokenName)
					if approachTypeMatches(spokenLower, approachType) && approachMatchesAssigned(apprID, assignedApproach) {
						consumed := typeConsumed + numPos + 1
						if runwayDir != "" {
							consumed++
						}
						logLocalStt("  matchApproachByTypeAndNumber: runway mismatch, falling back to assigned approach %q (type=%q dir=%c)",
							apprID, approachType, spokenDirChar)
						return apprID, 0.85, consumed // Lower confidence for fallback
					}
				}
			}
		}
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
	case "ils":
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

// extractApproachVariant extracts an approach variant letter from tokens.
// Looks for NATO phonetic letters like "zulu", "yankee", "alpha" that distinguish
// approach variants (e.g., RNAV Z vs RNAV Y).
// Returns the variant letter (lowercase) and number of tokens consumed.
func extractApproachVariant(tokens []Token) (string, int) {
	if len(tokens) == 0 {
		return "", 0
	}

	// Check the first token for a NATO phonetic letter
	text := strings.ToLower(tokens[0].Text)

	// Use the natoAlphabet map from normalize.go to convert phonetic words to letters
	if letter, ok := ConvertNATOLetter(text); ok {
		return letter, 1
	}

	return "", 0
}

// approachVariantMatches checks if an approach ID contains the given variant letter.
// For approach IDs like "RY7" (RNAV Yankee runway 7) or "RZ7" (RNAV Zulu runway 7),
// the variant letter is the second character.
func approachVariantMatches(approachID string, variant string) bool {
	if len(approachID) < 2 || variant == "" {
		return false
	}

	// The approach ID format is typically: TYPE + VARIANT + RUNWAY
	// e.g., "RY7" = R(NAV) + Y(ankee) + runway 7
	//       "RZ7" = R(NAV) + Z(ulu) + runway 7
	//       "I9" = I(LS) + runway 9 (no variant)
	// The variant letter is usually the second character for RNAV approaches
	secondChar := strings.ToLower(string(approachID[1]))
	return secondChar == variant
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
				case "left", "l", "west": // "west" is STT error for "left"
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
				case "left", "l", "west": // "west" is STT error for "left"
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
		// Localizer is the lateral component of ILS, so "localizer" approach matches ILS approaches
		return strings.Contains(spokenLower, "localizer") || strings.Contains(spokenLower, "loc") ||
			strings.Contains(spokenLower, "i l s") || strings.Contains(spokenLower, "ils")
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

		// Reciprocal runway match: 31L = 13R, 31R = 13L (same physical pavement)
		// Reciprocal number is (N + 18) mod 36, with 0 becoming 36
		reciprocalNum := (num + 18) % 36
		if reciprocalNum == 0 {
			reciprocalNum = 36
		}
		// Swap direction: L ↔ R, C stays C
		reciprocalSuffix := suffix
		if suffix == "L" {
			reciprocalSuffix = "R"
		} else if suffix == "R" {
			reciprocalSuffix = "L"
		}
		reciprocalRwy := fmt.Sprintf("%d%s", reciprocalNum, reciprocalSuffix)
		for _, rwy := range lahsoRunways {
			if rwy == reciprocalRwy {
				// Return the spoken runway ID (what the controller said), not the internal ID
				logLocalStt("  extractLAHSO: reciprocal match %q (internal %q)", runwayStr, rwy)
				return runwayStr, consumed
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

// extractTraffic extracts traffic advisory components: o'clock position, distance in miles, and altitude.
// Pattern: "(N) o'clock, (M) miles, (direction), (aircraft type), (at) (altitude)"
// Returns o'clock (1-12), miles, encoded altitude (in 100s of feet), and tokens consumed.
// The direction and aircraft type are ignored.
func extractTraffic(tokens []Token) (int, int, int, int) {
	if len(tokens) == 0 {
		return 0, 0, 0, 0
	}

	consumed := 0
	var oclock, miles, alt int

	// Phase 1: Find o'clock position (1-12)
	for consumed < len(tokens) && consumed < 10 {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		// Skip filler words
		if IsFillerWord(text) || text == "at" || text == "your" {
			consumed++
			continue
		}

		// Check for number followed by "o'clock" or just a number in range 1-12
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 12 {
			oclock = t.Value
			consumed++
			// Skip "o'clock" if present
			if consumed < len(tokens) && FuzzyMatch(tokens[consumed].Text, "o'clock", 0.8) {
				consumed++
			}
			break
		}
		consumed++
	}

	if oclock == 0 {
		return 0, 0, 0, 0
	}

	// Phase 2: Find distance in miles
	for consumed < len(tokens) && consumed < 20 {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		// Skip filler words and punctuation-like words
		if IsFillerWord(text) || text == "at" || text == "about" || text == "approximately" {
			consumed++
			continue
		}

		// Check for number followed by "miles" or "mile"
		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 50 {
			miles = t.Value
			consumed++
			// Skip "miles" or "mile" if present
			if consumed < len(tokens) {
				nextText := strings.ToLower(tokens[consumed].Text)
				if FuzzyMatch(nextText, "miles", 0.8) || FuzzyMatch(nextText, "mile", 0.8) {
					consumed++
				}
			}
			break
		}
		consumed++
	}

	if miles == 0 {
		return 0, 0, 0, 0
	}

	// Phase 3: Skip direction and aircraft type, find altitude
	// The altitude is the current position of the traffic. If followed by "climbing/descending XXXX",
	// we use the first altitude (current), not the target.
	// Altitudes are multiples of 100 feet; aircraft types like 787 are not.
	for consumed < len(tokens) && consumed < 40 {
		t := tokens[consumed]

		// Check for altitude pattern (TokenAltitude from "N thousand" parsing)
		if t.Type == TokenAltitude {
			// Validate: altitudes > 600 (60,000 ft) are implausible.
			// This handles cases like "Boeing 737 at 3 thousand" where STT produces
			// a garbled token combining aircraft type (737) with altitude (30).
			// Try to extract just the altitude portion (last 2 digits).
			if t.Value > 600 {
				// Extract last 2 digits as the altitude (e.g., 73730 → 30)
				extracted := t.Value % 100
				if extracted >= 10 && extracted <= 60 {
					logLocalStt("  extractTraffic: extracted altitude %d from implausible %d", extracted, t.Value)
					alt = extracted
					consumed++

					// Check if the next token is also an implausible altitude that
					// refines this one (adds hundreds). This handles garbled speech
					// like "737 five thousand eight five thousand nine hundred" where
					// the first token gives thousands (50) and the second adds
					// hundreds (59).
					if consumed < len(tokens) && tokens[consumed].Type == TokenAltitude && tokens[consumed].Value > 600 {
						nextExtracted := tokens[consumed].Value % 100
						if nextExtracted > extracted && nextExtracted <= 60 && nextExtracted/10 == extracted/10 {
							logLocalStt("  extractTraffic: refined altitude %d -> %d from implausible %d", alt, nextExtracted, tokens[consumed].Value)
							alt = nextExtracted
							consumed++
						}
					}

					break
				}
				// If last 2 digits aren't valid, skip and keep looking
				consumed++
				continue
			}
			alt = t.Value
			consumed++
			break
		}

		// Check for number that might be altitude
		if t.Type == TokenNumber {
			// Skip aircraft type numbers (737, 787, A320, etc.) - these are not altitudes
			if isAircraftTypeNumber(t.Value) {
				consumed++
				continue
			}

			// Look ahead for "thousand" pattern
			if consumed+1 < len(tokens) {
				nextText := strings.ToLower(tokens[consumed+1].Text)
				if FuzzyMatch(nextText, "thousand", 0.8) {
					// "N thousand" pattern - multiply by 10 to get encoded altitude.
					// Max valid altitude with "thousand" is 17,000 ft (N=17);
					// above that it's "flight level". When N is too large,
					// preceding noise digits were merged in by the tokenizer;
					// extract the trailing 1-2 digits as the real value.
					n := t.Value
					if n > 17 {
						if last2 := n % 100; last2 >= 1 && last2 <= 17 {
							n = last2
						} else if last1 := n % 10; last1 >= 1 && last1 <= 17 {
							n = last1
						} else {
							consumed++
							continue
						}
					}
					alt = n * 10
					consumed += 2
					// Check for "N hundred" after thousand
					if consumed+1 < len(tokens) {
						if tokens[consumed].Type == TokenNumber {
							hundredsVal := tokens[consumed].Value
							if consumed+1 < len(tokens) && FuzzyMatch(tokens[consumed+1].Text, "hundred", 0.8) {
								alt += hundredsVal
								consumed += 2
							}
						}
					}
					break
				} else if FuzzyMatch(nextText, "hundred", 0.8) {
					// "N hundred" pattern (low altitude like "five hundred")
					alt = t.Value
					consumed += 2
					break
				}
			}

			// Raw number in feet - must be multiple of 100 to be an altitude
			// This skips aircraft types like 787, 737, etc.
			if t.Value >= 500 && t.Value <= 60000 && t.Value%100 == 0 {
				alt = t.Value / 100
				consumed++
				// Skip "climbing/descending to XXXX" - we use current altitude, not target
				if consumed < len(tokens) {
					nextText := strings.ToLower(tokens[consumed].Text)
					if FuzzyMatch(nextText, "climbing", 0.8) || FuzzyMatch(nextText, "descending", 0.8) {
						consumed++
						if consumed < len(tokens) && tokens[consumed].Type == TokenNumber {
							consumed++
						}
					}
				}
				break
			}
		}

		consumed++
	}

	if alt == 0 {
		return 0, 0, 0, 0
	}

	// Consume trailing traffic advisory words that follow the altitude.
	// These are part of the traffic call and should not be re-parsed as commands.
	// Pattern: "[descending/climbing] [report [traffic] in sight]"
	for consumed < len(tokens) {
		text := strings.ToLower(tokens[consumed].Text)
		if FuzzyMatch(text, "descending", 0.8) || FuzzyMatch(text, "climbing", 0.8) ||
			text == "descend" || text == "climb" {
			consumed++
			continue
		}
		if text == "report" || text == "sight" || text == "in" || IsFillerWord(text) {
			consumed++
			continue
		}
		break
	}

	return oclock, miles, alt, consumed
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
		// Handle 3-digit values that are likely thousands with decimal artifacts
		// e.g., "9.00" → 900 means 9000 feet, "5.00" → 500 means 5000 feet
		// These are outside the ambiguous speed range (100-400)
		if t.Value >= 500 && t.Value <= 900 && t.Value%100 == 0 {
			return t.Value / 10
		}
	}
	return 0
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

	// Look for "until" keyword or its STT variants
	untilFound := false
	for consumed < len(tokens) && consumed < 5 {
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
		break
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
	}
	return false
}

// isSpeedFillerWord checks if a word commonly appears between a speed and "until".
// For example: "maintain 180 knots or greater until 5 mile final" - skip "knots", "or", "greater".
func isSpeedFillerWord(text string) bool {
	switch text {
	case "knots", "kts", "knot", "or", "greater", "better":
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

	// Check for "DME" or "D M E" or "D. M. E." patterns
	nextText := strings.ToLower(tokens[consumed].Text)
	if nextText == "dme" || FuzzyMatch(nextText, "dme", 0.8) {
		return num, consumed + 1
	}

	// Check for spelled out "D M E"
	if nextText == "d" && consumed+2 < len(tokens) {
		if strings.ToLower(tokens[consumed+1].Text) == "m" &&
			strings.ToLower(tokens[consumed+2].Text) == "e" {
			return num, consumed + 3
		}
	}

	return 0, 0
}

// extractMileFinal extracts a mile final specification from tokens.
// Handles: "6 mile final", "5 miles final"
func extractMileFinal(tokens []Token) (int, int) {
	if len(tokens) == 0 {
		return 0, 0
	}

	// First token should be a number
	if tokens[0].Type != TokenNumber || tokens[0].Value < 1 || tokens[0].Value > 20 {
		return 0, 0
	}
	num := tokens[0].Value
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

// isAircraftTypeNumber returns true if the number matches a common aircraft type.
// Used to filter out aircraft types from altitude extraction in traffic advisories.
// Common types: Boeing 737/747/757/767/777/787, Airbus A319/A320/A321/A330/A340/A350/A380
func isAircraftTypeNumber(n int) bool {
	switch n {
	// Boeing narrow-body
	case 737, 738, 739, 757:
		return true
	// Boeing wide-body
	case 747, 767, 777, 787:
		return true
	// Airbus narrow-body (A3xx)
	case 319, 320, 321:
		return true
	// Airbus wide-body
	case 330, 340, 350, 380:
		return true
	// Common variants with generation suffix (e.g., 738 for 737-800)
	case 788, 789: // 787-8, 787-9
		return true
	case 748: // 747-8
		return true
	case 359: // A350-900
		return true
	}
	return false
}

// Compile-time check that slices package is used
var _ = slices.Contains[[]string]

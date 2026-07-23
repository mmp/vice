package stt

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/util"
)

// This file matches transcript spans against the aircraft's candidate
// approaches (and visual approaches / LAHSO clearances): decomposing
// spoken approach names into type, variant, runway, and direction and
// scoring them against the tokens.

// extractApproach extracts an approach from tokens.
// assignedApproach is the approach the aircraft was previously told to expect (e.g., "ILS Runway 10R").
// When there are multiple matches with equal scores, the assigned approach is preferred.
func extractApproach(tokens []Token, approaches map[string]string, assignedApproach string, allowGarbled, requireEvidence bool) (string, float64, int) {
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

	// Extract the spoken runway number so the fuzzy loop can reject candidates
	// whose runway number disagrees. Without this, the JW prefix boost from
	// shared "i l s runway " can let a wrong-runway candidate outscore the right one.
	// Only honor numbers that are explicitly preceded by "runway" so unrelated
	// digits ("three mile approach") don't trigger spurious rejections. Filtering
	// is gated by anyApproachMatchesRunway: if no candidate matches the spoken
	// runway (the number was likely mis-transcribed), let fuzzy matching proceed
	// without filtering so the existing fallback behavior is preserved.
	spokenRunwayNum := ""
	if num, _, pos := extractRunwayNumber(tokens); num != "" && pos > 0 &&
		strings.EqualFold(tokens[pos-1].Text, "runway") {
		spokenRunwayNum = num
	}
	anyApproachMatchesRunway := false
	if spokenRunwayNum != "" {
		for spokenName := range approaches {
			nameLower := strings.ToLower(spokenName)
			if strings.Contains(nameLower, "runway ") && runwayConsistent(nameLower, spokenRunwayNum) {
				anyApproachMatchesRunway = true
				break
			}
		}
	}

	// Skip fuzzy loop when the first token is "runway" — there's no approach type
	// info to match against, so JW scores against full candidate names are misleading
	// (e.g., "runway" gets a Winkler prefix boost against "r-nav"). The runway
	// fallback below handles this correctly.
	skipFuzzyLoop := len(tokens) > 0 && strings.ToLower(tokens[0].Text) == "runway"

	// Build candidate phrases (1-7 words for approach names, since spoken numbers expand)
	for length := min(7, len(tokens)); length >= 1 && !skipFuzzyLoop; length-- {
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
				if anyApproachMatchesRunway {
					nameLower := strings.ToLower(spokenName)
					if strings.Contains(nameLower, "runway ") && !runwayConsistent(nameLower, spokenRunwayNum) {
						continue
					}
				}
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
	if runwayNum, runwayDir, numPos := extractRunwayNumber(searchTokens); runwayNum != "" && numPos <= 5 {
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
		// Sort by apprID for deterministic disambiguation when scores tie.
		slices.SortFunc(matchingApproaches, func(a, b struct {
			spokenName string
			apprID     string
		}) int {
			return strings.Compare(a.apprID, b.apprID)
		})

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

			// Also build a suffix phrase from tokens after the runway number+direction.
			// This handles non-canonical pilot phrasings where the approach type comes
			// after the runway, e.g., "runway four right rnav zulu approach".
			var suffixParts []string
			suffixStart := numPos + 1
			if runwayDir != "" {
				suffixStart++
			}
			for k := suffixStart; k < len(tokens); k++ {
				text := strings.ToLower(tokens[k].Text)
				if text == "approach" || text == "runway" || IsFillerWord(text) {
					continue
				}
				if tokens[k].Type == TokenNumber {
					suffixParts = append(suffixParts, spokenDigits(tokens[k].Value))
				} else {
					suffixParts = append(suffixParts, text)
				}
			}
			suffixPhrase := strings.Join(suffixParts, " ")

			// Find the best matching approach by comparing prefix/suffix to approach type portion
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

				// Compare using Jaro-Winkler against both prefix and suffix; take the better.
				score := JaroWinkler(prefixPhrase, approachTypePortion)
				if s := JaroWinkler(suffixPhrase, approachTypePortion); s > score {
					score = s
				}

				// Also try phonetic matching for short garbled inputs
				if PhoneticMatch(prefixPhrase, approachTypePortion) ||
					PhoneticMatch(suffixPhrase, approachTypePortion) {
					score = max(score, 0.85)
				}

				if score > bestMatchScore || (score == bestMatchScore && ma.apprID < bestMatch) {
					bestMatchScore = score
					bestMatch = ma.apprID
				}
			}

			// When disambiguating between multiple runway matches, pick the best match.
			// When the prefix is garbled, prefer the assigned approach when available.
			if bestMatch != "" && (bestMatchScore >= 0.30 || prefixPhrase == "") {
				// If we have an assigned approach, check if one of the matching
				// approaches matches the assigned approach's type. This handles
				// garbled prefixes like "off" for "ILS".
				if bestMatchScore < 0.80 && assignedApproach != "" {
					assignedLower := strings.ToLower(assignedApproach)
					// Extract RNAV variant from assigned approach (e.g., "z" from "rnav z runway 27")
					assignedVariant := extractAssignedVariant(assignedLower)

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
								// When the assigned approach has a variant letter (Z/Y/X/W),
								// also check the candidate's variant — applies to both ILS
								// (e.g., IZ6 for "ILS Z Runway 6") and RNAV.
								if assignedVariant != "" {
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

	// Garbled-type fallback: the approach type word is unrecognizable ("aisles",
	// "a less", "lstu", "idols" for ILS; "the honor of" for a name), but this is
	// an explicit expect/cleared clearance (extractApproach is only reached from
	// that path). Score every candidate against the spoken runway digits,
	// direction, variant, and the assigned approach and take the best.
	if allowGarbled {
		if appr, consumed := matchGarbledApproach(tokens, approaches, assignedApproach, requireEvidence); consumed > 0 {
			return appr, 0.75, consumed
		}
	}

	return "", 0, 0
}

// candidateRunway parses a candidate's spoken approach name into its runway
// digit string and direction (e.g. "I L S runway two four right" -> "24", 'R';
// "r-nav zulu runway one two" -> "12", 0).
func candidateRunway(spokenName string) (digits string, dir byte) {
	lower := strings.ToLower(spokenName)
	idx := strings.Index(lower, "runway ")
	if idx < 0 {
		return "", 0
	}
	for _, w := range strings.Fields(lower[idx+len("runway "):]) {
		if d, ok := digitWords[w]; ok {
			digits += d
			continue
		}
		if IsNumber(w) {
			digits += w
			continue
		}
		switch w {
		case "left", "l":
			dir = 'L'
		case "right", "r":
			dir = 'R'
		case "center", "c":
			dir = 'C'
		}
		// Stop once we have digits and hit a non-digit word (direction handled).
		if digits != "" {
			break
		}
	}
	return digits, dir
}

// matchGarbledApproach is the last-resort approach matcher for an explicit
// expect/cleared clearance whose type word is garbled beyond recognition. It
// requires the span to open with a garbled type WORD (not a bare runway
// number), so a genuinely typeless "expect two four right" still yields
// SAYAGAIN. It scores every candidate by runway-digit overlap, spoken
// direction, spoken variant (zulu/yankee), the assigned approach, and an ILS
// prior (garbled types are overwhelmingly ILS) and returns the best.
func matchGarbledApproach(tokens []Token, approaches map[string]string, assignedApproach string, requireEvidence bool) (string, int) {
	// Require a leading garbled type word.
	i := 0
	for i < len(tokens) && IsFillerWord(strings.ToLower(tokens[i].Text)) {
		i++
	}
	if i >= len(tokens) {
		return "", 0
	}
	first := strings.ToLower(tokens[i].Text)
	// A number/direction/"runway" opener means the type was never spoken (a bare
	// runway is genuinely ambiguous); a command keyword ("direct", "intercept",
	// "cancel") means this span is a different command, not a garbled approach.
	if tokens[i].Type == TokenNumber || IsCommandKeyword(first) ||
		first == "runway" || first == "cancel" || first == "unable" {
		return "", 0
	}

	// Consume the approach phrase up to a command boundary, gathering signals.
	end := i
	spokenDigits := ""
	var spokenDir byte
	var spokenVariant string
	for j := i; j < len(tokens); j++ {
		w := strings.ToLower(tokens[j].Text)
		// A trailing "left"/"right"/"center" is the runway direction, so capture
		// it even though those words are also command boundaries elsewhere.
		if w == "left" || w == "right" || w == "center" {
			spokenDir = w[0] - 'a' + 'A'
			end = j
			continue
		}
		if j > i && IsCommandKeyword(w) {
			break
		}
		end = j
		switch {
		case tokens[j].Type == TokenNumber:
			spokenDigits += tokens[j].Text
		default:
			if v, ok := ConvertNATOLetter(w); ok && (v == "z" || v == "y") {
				spokenVariant = v
			}
		}
	}

	best, bestScore, bestEvidence := "", 0.0, false
	for spokenName, apprID := range approaches {
		candDigits, candDir := candidateRunway(spokenName)
		candVariant := ""
		if len(apprID) >= 2 && (apprID[1] == 'Y' || apprID[1] == 'Z') {
			candVariant = strings.ToLower(string(apprID[1]))
		}

		// Hard filters: spoken direction and variant must not conflict.
		if spokenDir != 0 && candDir != 0 && candDir != spokenDir {
			continue
		}
		if spokenVariant != "" && candVariant != spokenVariant {
			continue
		}

		// evidence: a positive signal from the transcript itself (runway
		// digits, direction, or variant), as opposed to leaning only on the
		// assigned approach.
		score, evidence := 0.0, false
		if candDigits != "" && strings.Contains(spokenDigits, candDigits) {
			score += 3.0
			evidence = true
		} else if candDigits != "" && spokenDigits != "" && strings.HasSuffix(spokenDigits, candDigits[len(candDigits)-1:]) {
			score += 1.0
			evidence = true
		}
		if spokenDir != 0 && candDir == spokenDir {
			score += 1.5
			evidence = true
		}
		if matchesAssignedRunway(apprID, assignedApproach) {
			score += 2.0
		}
		if apprID != "" && apprID[0] == 'I' {
			score += 0.5 // garbled types are overwhelmingly ILS
		}
		if spokenVariant != "" && candVariant == spokenVariant {
			score += 0.3
			evidence = true
		}

		if score > bestScore || (score == bestScore && best != "" && apprID < best) {
			best, bestScore, bestEvidence = apprID, score, evidence
		}
	}

	// A recognizable approach-type word anywhere in the span (e.g. "cleared ILS"
	// with the runway garbled) confirms an approach clearance and satisfies the
	// evidence requirement even without a runway signal.
	hasTypeWord := false
	for k := i; k <= end && !hasTypeWord; k++ {
		// "localizer" is excluded: a bare localizer reference is an intercept
		// instruction ("at FIX intercept localizer" -> I), not a clearance.
		if t, _ := extractApproachType(tokens[k:]); t != "" && t != "localizer" {
			hasTypeWord = true
		}
	}

	if bestScore < 1.5 || (requireEvidence && !bestEvidence && !hasTypeWord) {
		return "", 0
	}
	logLocalStt("  matchGarbledApproach: digits=%q dir=%c variant=%q -> %q (score=%.1f)",
		spokenDigits, dirOrDash(spokenDir), spokenVariant, best, bestScore)
	return best, end + 1
}

// dirOrDash renders a direction byte for logging, using '-' for none.
func dirOrDash(dir byte) byte {
	if dir == 0 {
		return '-'
	}
	return dir
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

// extractAssignedVariant extracts the variant letter (z/y/x/w) from an
// assigned approach string. E.g., "rnav z runway 27" → "z",
// "ils z runway 6" → "z", "rnav yankee runway 13l" → "y".
func extractAssignedVariant(assignedLower string) string {
	var rest string
	if idx := strings.Index(assignedLower, "rnav"); idx != -1 {
		rest = strings.TrimSpace(assignedLower[idx+4:])
	} else if idx := strings.Index(assignedLower, "ils"); idx != -1 {
		rest = strings.TrimSpace(assignedLower[idx+3:])
	} else {
		return ""
	}
	if rest == "" {
		return ""
	}
	// The variant is the first word after the type: "z", "y", "x", "w", "zulu", "yankee", etc.
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

// extractApproachIDVariant extracts the variant letter from an approach ID.
// E.g., "RZ7" → "z", "IZ6" → "z", "RY1L" → "y", "I3R" → "".
func extractApproachIDVariant(approachID string) string {
	upper := strings.ToUpper(approachID)
	if len(upper) < 2 {
		return ""
	}
	if upper[0] != 'R' && upper[0] != 'I' {
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

	// If we found a runway number but no explicit direction, try phonetic inference
	// on the next token. STT often garbles "left"/"right" into short words like "at".
	// Compare the metaphone encoding of the next token against direction words and
	// pick the best match if it's clearly better than the alternatives.
	if runwayNum != "" && runwayDir == "" && numPos+1 < len(remainingTokens) {
		nextText := strings.ToLower(remainingTokens[numPos+1].Text)
		// Don't try phonetic inference on command keywords — "direct", "cleared",
		// etc. are real words, not garbled direction words.
		if !IsCommandKeyword(nextText) {
			if dir := inferRunwayDirectionPhonetic(nextText); dir != "" {
				logLocalStt("  matchApproachByTypeAndNumber: inferred direction %q from garbled %q", dir, nextText)
				runwayDir = dir
			}
		}
	}

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
		// No assigned approach fallback worked. If we have a type+variant and
		// exactly one candidate approach matches, use it. This handles garbled
		// runway numbers (e.g., "rnav zulu approach from ITN" where "from ITN"
		// is a garbled "twenty seven" but there's only one RNAV Zulu approach).
		if allowFallback && approachVariant != "" {
			var matches []string
			for spokenName, apprID := range approaches {
				spokenLower := strings.ToLower(spokenName)
				if approachTypeMatches(spokenLower, approachType) && approachVariantMatches(apprID, approachVariant) {
					matches = append(matches, apprID)
				}
			}
			if len(matches) == 1 {
				// Consume the garbled runway tokens that follow the variant.
				// Scan forward past "approach", "runway", filler, and non-keyword
				// words that are part of the garbled runway reference.
				consumed := typeConsumed + variantConsumed
				for j := 0; j < len(remainingTokens); j++ {
					w := strings.ToLower(remainingTokens[j].Text)
					if w == "approach" || w == "runway" || IsFillerWord(w) {
						consumed++
						continue
					}
					if IsCommandKeyword(w) {
						break
					}
					consumed++ // garbled runway token
				}
				logLocalStt("  matchApproachByTypeAndNumber: garbled runway, unique type+variant match %q (type=%q variant=%q)",
					matches[0], approachType, approachVariant)
				return matches[0], 0.85, consumed
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

	// Single-word approach types, matched with the shared similarity
	// primitive so garbled renderings ("isle", "arnav") are recognized.
	if text == "loc" {
		return "localizer", 1
	}
	for _, typ := range []string{"ils", "rnav", "visual", "vor", "localizer"} {
		if WordScore(text, typ) >= 0.8 {
			return typ, 1
		}
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

// inferRunwayDirectionPhonetic tries to infer a runway direction ("left", "right", "center")
// from a garbled word by comparing metaphone encodings. Returns the best direction if one
// is clearly better than the others, or "" if no confident inference can be made.
func inferRunwayDirectionPhonetic(word string) string {
	wordPrimary, _ := DoubleMetaphone(word)
	if wordPrimary == "" {
		return ""
	}

	type dirScore struct {
		dir   string
		score float64
	}

	dirs := []dirScore{
		{"left", JaroWinkler(wordPrimary, func() string { p, _ := DoubleMetaphone("left"); return p }())},
		{"right", JaroWinkler(wordPrimary, func() string { p, _ := DoubleMetaphone("right"); return p }())},
		{"center", JaroWinkler(wordPrimary, func() string { p, _ := DoubleMetaphone("center"); return p }())},
	}

	// Find best and second-best
	var best, secondBest dirScore
	for _, d := range dirs {
		if d.score > best.score {
			secondBest = best
			best = d
		} else if d.score > secondBest.score {
			secondBest = d
		}
	}

	// Require a minimum score and clear margin over the runner-up
	if best.score >= 0.5 && best.score > secondBest.score+0.05 {
		return best.dir
	}
	return ""
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

// runwayConsistent returns true when the candidate's "runway X" portion is
// compatible with the spoken runway number. Compatibility is first-token
// equality (so spoken "three" matches candidate "runway three zero" when a
// pilot trimmed the trailing digit) with niner/nine normalized as equivalent.
// Returns true when the candidate has no "runway " marker (don't gate it).
func runwayConsistent(candidateLower, spokenRunwayNum string) bool {
	idx := strings.Index(candidateLower, "runway ")
	if idx == -1 {
		return true
	}
	candFirst, _, _ := strings.Cut(candidateLower[idx+7:], " ")
	spokenFirst, _, _ := strings.Cut(spokenRunwayNum, " ")
	if candFirst == "niner" {
		candFirst = "nine"
	}
	if spokenFirst == "niner" {
		spokenFirst = "nine"
	}
	return candFirst == spokenFirst
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

func visualApproachTelephonyVariants(rwy string) []string {
	spoken := spokenRunway(rwy)
	return []string{
		"visual runway " + spoken,
		"visual approach runway " + spoken,
		"visual " + spoken,
	}
}

func matchVisualApproach(tokens []Token, candidates map[string]string) (string, int) {
	approachType, typeConsumed := extractApproachType(tokens)
	if approachType != "visual" {
		return "", 0
	}

	remainingTokens := tokens[typeConsumed:]
	runwaySpoken, runwayDir, numPos := extractRunwayNumber(remainingTokens)
	if runwaySpoken == "" {
		return "", 0
	}
	if runwayDir != "" {
		runwaySpoken += " " + runwayDir
	}

	seen := make(map[string]struct{})
	var matches []string
	for _, rwy := range candidates {
		if _, ok := seen[rwy]; ok {
			continue
		}
		seen[rwy] = struct{}{}

		spoken := spokenRunway(rwy)
		if spoken == runwaySpoken ||
			(runwayDir == "" && strings.TrimSpace(strings.TrimSuffix(spoken, " left")) == runwaySpoken) ||
			(runwayDir == "" && strings.TrimSpace(strings.TrimSuffix(spoken, " right")) == runwaySpoken) ||
			(runwayDir == "" && strings.TrimSpace(strings.TrimSuffix(spoken, " center")) == runwaySpoken) {
			matches = append(matches, rwy)
		}
	}
	if len(matches) != 1 {
		return "", 0
	}

	consumed := typeConsumed + numPos + 1
	if runwayDir != "" {
		consumed++
	}
	logLocalStt("  matchVisualApproach: runway=%q -> %q (consumed=%d)", runwaySpoken, matches[0], consumed)
	return matches[0], consumed
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

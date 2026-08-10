package stt

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// This file matches transcript spans against the aircraft's candidate
// approaches (and visual approaches / LAHSO clearances): decomposing
// spoken approach names into type, variant, runway, and direction and
// scoring them against the tokens.

// CandidateApproach is one approach an aircraft could be cleared for,
// expanded from the aircraft's canonical-name-to-code map. Id is the
// scenario's approach code and is what emitted commands carry; it is
// facility-authored and has no fixed layout ("RZ8R" at one field, "Z8R" or
// even "R4L" at another), so nothing may be read from its characters. The
// approach's type, variant letter, and runway all come from FullName, the
// canonical name ("RNAV Z Runway 28R"); Spoken is its telephony, and is what
// transcript spans are aligned against.
type CandidateApproach struct {
	Id       string
	FullName string
	Spoken   string
}

// candidateApproaches expands an aircraft's approaches into the records the
// matchers work with, ordered by approach ID so every scan over them is
// deterministic.
func candidateApproaches(approaches map[string]string) []CandidateApproach {
	cands := make([]CandidateApproach, 0, len(approaches))
	for fullName, id := range approaches {
		cands = append(cands, CandidateApproach{
			Id:       id,
			FullName: fullName,
			Spoken:   av.GetApproachTelephony(fullName),
		})
	}
	slices.SortFunc(cands, func(a, b CandidateApproach) int { return strings.Compare(a.Id, b.Id) })
	return cands
}

// variant returns the approach's variant letter ("z"/"y"/"x"/"w"), or "" if
// it has none.
func (c CandidateApproach) variant() string {
	return extractAssignedVariant(strings.ToLower(c.FullName))
}

// runway returns the approach's runway digits and side.
func (c CandidateApproach) runway() (digits string, dir byte) {
	return approachRunway(c.FullName)
}

// matchesType reports whether the approach is of the given spoken type
// ("ils", "rnav", "visual", "vor", "localizer").
func (c CandidateApproach) matchesType(approachType string) bool {
	lower := strings.ToLower(c.FullName)
	switch approachType {
	case "ils":
		return strings.Contains(lower, "ils")
	case "rnav":
		return strings.Contains(lower, "rnav")
	case "visual":
		return strings.Contains(lower, "visual")
	case "vor":
		return strings.Contains(lower, "vor")
	case "localizer":
		// The localizer is the lateral component of an ILS, so a spoken
		// "localizer" also matches the ILS approaches.
		return strings.Contains(lower, "localizer") || strings.Contains(lower, "loc") ||
			strings.Contains(lower, "ils")
	}
	return false
}

// matchesAssigned reports whether this is the approach the aircraft was
// told to expect; assigned is the same canonical name (sim.Track.Approach).
func (c CandidateApproach) matchesAssigned(assigned string) bool {
	return assigned != "" && strings.EqualFold(c.FullName, assigned)
}

// onAssignedRunway reports whether the approach serves the runway of the
// assigned approach — a weaker relation than matchesAssigned, used where a
// different approach to the same runway is still a plausible reading.
func (c CandidateApproach) onAssignedRunway(assigned string) bool {
	assignedDigits, assignedDir := approachRunway(assigned)
	if assignedDigits == "" {
		return false
	}
	digits, dir := c.runway()
	if digits != assignedDigits {
		return false
	}
	return dir == assignedDir || dir == 0 || assignedDir == 0
}

// candidateWithId returns the candidate approach with the given approach ID.
func candidateWithId(approaches []CandidateApproach, id string) (CandidateApproach, bool) {
	if i := slices.IndexFunc(approaches, func(c CandidateApproach) bool { return c.Id == id }); i != -1 {
		return approaches[i], true
	}
	return CandidateApproach{}, false
}

// approachTypeOf returns the type word of a canonical approach name
// ("ils", "rnav", "localizer", "visual", "vor"), or "" if it names none.
func approachTypeOf(fullName string) string {
	lower := strings.ToLower(fullName)
	for _, t := range []string{"ils", "rnav", "localizer", "visual", "vor"} {
		if strings.Contains(lower, t) {
			return t
		}
	}
	return ""
}

// approachRunway parses the runway out of a canonical approach name:
// "RNAV Z Runway 28R" -> ("28", 'R'), "ILS Runway 4 Right" -> ("4", 'R'),
// "ILS Runway 9" -> ("9", 0). Returns ("", 0) if the name has no runway.
func approachRunway(fullName string) (digits string, dir byte) {
	_, after, ok := strings.Cut(strings.ToUpper(fullName), "RUNWAY ")
	if !ok {
		return "", 0
	}
	for _, c := range after {
		switch {
		case c >= '0' && c <= '9':
			if dir != 0 { // digits after the side belong to something else
				return digits, dir
			}
			digits += string(c)
		case c == 'L' || c == 'R' || c == 'C':
			dir = byte(c)
		case c == ' ':
		default:
			return digits, dir
		}
	}
	return digits, dir
}

// extractApproach extracts an approach from tokens.
// assignedApproach is the approach the aircraft was previously told to expect (e.g., "ILS Runway 10R").
// When there are multiple matches with equal scores, the assigned approach is preferred.
func extractApproach(tokens []Token, approaches []CandidateApproach, assignedApproach string, allowGarbled, requireEvidence bool) (string, float64, int) {
	if len(tokens) == 0 || len(approaches) == 0 {
		return "", 0, 0
	}

	// A clearance verb is never part of an approach name, so the slot is
	// being offered tokens that open a different command. Absorbing one
	// silently rewrites the instruction — "expect ILS two two left" swallowed
	// into an at-fix template reads back as a clearance.
	for _, t := range tokens {
		text := strings.ToLower(t.Text)
		if IsFillerWord(text) {
			continue
		}
		if approachSpanVerb[text] {
			return "", 0, 0
		}
		break
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
	var bestMatchesAssigned bool

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
		for _, appr := range approaches {
			nameLower := strings.ToLower(appr.Spoken)
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

	// When the tokens contain a runway number that matches no candidate at
	// all, the number was garbled in transcription ("two three" for "two
	// six"). Letter similarity can't recover it — "two three" is about as
	// close to "two five" as to "two six" — so the assigned approach gets a
	// decisive preference instead of the usual tie-breaking nudge, unless a
	// clearly spoken direction contradicts the assigned runway.
	garbledRunwayNum := false
	if num, _, pos := extractRunwayNumber(tokens); num != "" && pos >= 0 {
		garbledRunwayNum = !slices.ContainsFunc(approaches, func(appr CandidateApproach) bool {
			return runwayMatches(strings.ToLower(appr.Spoken), num)
		})
		if d := extractSpokenDirection(tokens); d != 0 {
			if _, ad := approachRunway(assignedApproach); ad != 0 && ad != d {
				garbledRunwayNum = false
			}
		}
	}

	// Bound candidate phrases at keywords that start a clearly different
	// command: "ils maintain two one zero" must not fuzzy-match "i l s
	// runway two four right" just because the runway-inserted variant
	// shares a long prefix. Heading/turn words are NOT boundaries — they
	// commonly appear as garbled approach text ("i l s turn to around
	// three left" for "ILS runway two three left").
	maxPhrase := len(tokens)
	for i, t := range tokens {
		if i > 0 && approachPhraseBoundary[strings.ToLower(t.Text)] {
			maxPhrase = i
			break
		}
	}

	// A spoken approach type is a hard constraint, wherever in the phrase it
	// falls: "cleared ILS runway two eight right" must never select the RNAV
	// Y or Z to the same runway, however the letter-similarity scores land.
	spokenType := ""
	for i := range maxPhrase {
		if t, _ := extractApproachType(tokens[i:]); t != "" {
			spokenType = t
			break
		}
	}

	// Build candidate phrases (1-7 words for approach names, since spoken numbers expand)
	for length := min(7, maxPhrase); length >= 1 && !skipFuzzyLoop; length-- {
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
			for _, appr := range approaches {
				if strings.EqualFold(variant, appr.Spoken) {
					return appr.Id, 1.0, length
				}
			}

			// Try fuzzy match - find the best one.
			// Prefer assigned approach on ties, otherwise use alphabetically earlier ID for determinism.
			for _, appr := range approaches {
				if spokenType != "" && !appr.matchesType(spokenType) {
					continue
				}
				if anyApproachMatchesRunway {
					nameLower := strings.ToLower(appr.Spoken)
					if strings.Contains(nameLower, "runway ") && !runwayConsistent(nameLower, spokenRunwayNum) {
						continue
					}
				}
				score := JaroWinkler(variant, appr.Spoken)
				if score >= 0.80 {
					// Bonus for matching spoken direction: if user said "left" and the
					// approach serves a left runway, boost the score. This helps
					// "ils ... left" match "I7L" over "I28".
					if _, dir := appr.runway(); spokenDir != 0 && dir == spokenDir {
						score += 0.05
					}

					// Bonus for matching the assigned/expected approach: prefer the
					// approach the aircraft was told to expect. This helps when the
					// approach type is garbled but the runway matches. With a garbled
					// runway number the bonus is decisive and runway-number-aware, so
					// only the actually-assigned runway collects it.
					if garbledRunwayNum {
						if appr.onAssignedRunway(assignedApproach) {
							score += 0.12
						}
					} else if appr.matchesAssigned(assignedApproach) {
						score += 0.03
					}

					isBetter := score > bestScore
					if !isBetter && score == bestScore {
						// Tie-breaker: prefer assigned approach, then alphabetically earlier
						thisMatchesAssigned := appr.matchesAssigned(assignedApproach)
						if thisMatchesAssigned && !bestMatchesAssigned {
							isBetter = true
						} else if thisMatchesAssigned == bestMatchesAssigned && appr.Id < bestAppr {
							isBetter = true
						}
					}
					if isBetter {
						bestAppr = appr.Id
						bestMatchesAssigned = appr.matchesAssigned(assignedApproach)
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
	// Bounded at the same phrase boundary as the fuzzy loop above. Other command keywords
	// like "turn" or "heading" are NOT boundaries because they commonly appear as garbled
	// approach text (e.g., "isle turn to new" for "ILS runway").
	searchTokens := tokens[:maxPhrase]
	if runwayNum, runwayDir, numPos := extractRunwayNumber(searchTokens); runwayNum != "" && numPos <= 5 {
		runwaySpoken := runwayNum
		if runwayDir != "" {
			runwaySpoken += " " + runwayDir
		}

		// The approaches serving the runway; already ordered by approach ID,
		// so disambiguation is deterministic when scores tie.
		matchingApproaches := util.FilterSlice(approaches, func(appr CandidateApproach) bool {
			return runwayMatches(strings.ToLower(appr.Spoken), runwaySpoken)
		})

		if len(matchingApproaches) == 1 {
			// Only one approach matches the runway - use it
			consumed := numPos + 1
			if runwayDir != "" {
				consumed++
			}
			appr := matchingApproaches[0]
			logLocalStt("  extractApproach: unique runway match %q -> %q", runwaySpoken, appr.Id)
			return appr.Id, 0.85, consumed
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
			for _, appr := range matchingApproaches {
				// Extract the approach type portion (before "runway")
				spokenLower := strings.ToLower(appr.Spoken)
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

				if score > bestMatchScore || (score == bestMatchScore && appr.Id < bestMatch) {
					bestMatchScore = score
					bestMatch = appr.Id
				}
			}

			// When disambiguating between multiple runway matches, pick the best match.
			// When the prefix is garbled, prefer the assigned approach when available.
			if bestMatch != "" && (bestMatchScore >= 0.30 || prefixPhrase == "") {
				// A garbled type prefix ("off" for "ILS") can't choose between the
				// approaches serving the runway; the approach the aircraft was told
				// to expect is the best reading, and failing that any approach of
				// the assigned type on the assigned runway.
				if bestMatchScore < 0.80 && assignedApproach != "" {
					assignedType := approachTypeOf(assignedApproach)
					var typeMatch string
					for _, appr := range matchingApproaches {
						if appr.matchesAssigned(assignedApproach) {
							bestMatch = appr.Id
							logLocalStt("  extractApproach: runway match, low score (%.2f), using assigned approach %q",
								bestMatchScore, bestMatch)
							typeMatch = "" // exact match found
							break
						}
						if typeMatch == "" && assignedType != "" &&
							appr.onAssignedRunway(assignedApproach) && appr.matchesType(assignedType) {
							typeMatch = appr.Id
						}
					}
					if typeMatch != "" {
						bestMatch = typeMatch
						logLocalStt("  extractApproach: runway match, low score (%.2f), using assigned approach's type %q",
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
		if _, assignedDir := approachRunway(assignedApproach); assignedDir == boundedDir {
			assignedType := approachTypeOf(assignedApproach)

			// Find the approach that best matches the assigned approach,
			// preferring one whose type matches it too.
			var bestApprID string
			var bestMatchesType bool
			for _, appr := range approaches {
				if !appr.onAssignedRunway(assignedApproach) {
					continue
				}
				matchesType := assignedType != "" && appr.matchesType(assignedType)
				if bestApprID == "" || (matchesType && !bestMatchesType) ||
					(matchesType == bestMatchesType && appr.Id < bestApprID) {
					bestApprID = appr.Id
					bestMatchesType = matchesType
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
	if slices.ContainsFunc(searchTokens, func(t Token) bool {
		return t.Type == TokenWord && strings.ToLower(t.Text) == "approach"
	}) && len(approaches) == 1 {
		var apprID string
		for _, appr := range approaches {
			apprID = appr.Id
		}
		logLocalStt("  extractApproach: single candidate with 'approach' keyword -> %q", apprID)
		return apprID, 0.70, len(searchTokens)
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

// approachSpanVerb lists the words that can never open an approach name:
// they introduce the command the approach belongs to (or a different command
// entirely), so an approach span starting on one has been mis-anchored.
var approachSpanVerb = map[string]bool{
	"cleared": true, "clear": true, "expect": true, "expecting": true,
	"vectors": true, "vector": true, "cancel": true, "intercept": true,
}

// approachPhraseBoundary lists the command keywords that end an approach
// phrase: speed, altitude, and handoff commands never garble from spoken
// approach names, and neither do the clearance verbs — a phrase that runs
// through one has swallowed the verb of the command it belongs to, which is
// how "expect ILS two two left" ends up read back as a clearance.
var approachPhraseBoundary = map[string]bool{
	"maintain": true, "speed": true, "slow": true, "reduce": true, "increase": true,
	"descend": true, "climb": true, "altitude": true, "mach": true,
	"contact": true, "squawk": true,
	"cleared": true, "clear": true, "expect": true, "expecting": true,
	"vectors": true, "vector": true, "cancel": true, "intercept": true,
}

// matchGarbledApproach is the last-resort approach matcher for an explicit
// expect/cleared clearance whose type word is garbled beyond recognition. It
// requires the span to open with a garbled type WORD (not a bare runway
// number), so a genuinely typeless "expect two four right" still yields
// SAYAGAIN. It scores every candidate by runway-digit overlap, spoken
// direction, spoken variant (zulu/yankee), the assigned approach, and an ILS
// prior (garbled types are overwhelmingly ILS) and returns the best.
func matchGarbledApproach(tokens []Token, approaches []CandidateApproach, assignedApproach string, requireEvidence bool) (string, int) {
	// Require a leading garbled type word.
	i := 0
	for i < len(tokens) && IsFillerWord(strings.ToLower(tokens[i].Text)) {
		i++
	}
	if i >= len(tokens) {
		return "", 0
	}
	first := strings.ToLower(tokens[i].Text)
	// A number/direction/"runway" opener means the type was never spoken (a
	// bare runway is genuinely ambiguous) — unless the number is exactly
	// the assigned approach's runway ("cleared nine" with ILS runway 9 on
	// file), which is unambiguous shorthand. A command keyword ("direct",
	// "intercept", "cancel") means this span is a different command, not a
	// garbled approach.
	if tokens[i].Type == TokenNumber {
		// Several approach types can serve the assigned runway; prefer the
		// one whose type matches the assigned approach (I9 over R9 when
		// ILS runway 9 is on file), alphabetical for determinism.
		assignedType := approachTypeOf(assignedApproach)
		best, bestMatchesType := "", false
		for _, appr := range approaches {
			if digits, _ := appr.runway(); digits != tokens[i].Text ||
				!appr.onAssignedRunway(assignedApproach) {
				continue
			}
			matchesType := assignedType != "" && appr.matchesType(assignedType)
			if best == "" || (matchesType && !bestMatchesType) ||
				(matchesType == bestMatchesType && appr.Id < best) {
				best, bestMatchesType = appr.Id, matchesType
			}
		}
		if best != "" {
			logLocalStt("  matchGarbledApproach: bare runway %q matches assigned -> %q", tokens[i].Text, best)
			return best, i + 1
		}
		return "", 0
	}
	if IsCommandKeyword(first) || first == "runway" || first == "cancel" || first == "unable" {
		return "", 0
	}

	// Consume the approach phrase up to a command boundary, gathering signals.
	end := i
	var spokenDigits strings.Builder
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
			spokenDigits.WriteString(tokens[j].Text)
		default:
			if v, ok := ConvertNATOLetter(w); ok && (v == "z" || v == "y") {
				spokenVariant = v
			}
		}
	}

	best, bestScore, bestEvidence := "", 0.0, false
	for _, appr := range approaches {
		candDigits, candDir := appr.runway()
		candVariant := appr.variant()

		// Hard filters: spoken direction and variant must not conflict.
		if spokenDir != 0 && candDir != 0 && candDir != spokenDir {
			continue
		}
		if spokenVariant != "" && candVariant != spokenVariant {
			continue
		}

		// evidence: a positive signal from the transcript itself (runway
		// digits, direction, variant, or the candidate's own name showing
		// through the garble), as opposed to leaning only on the assigned
		// approach.
		score, evidence := 0.0, false
		if candDigits != "" && strings.Contains(spokenDigits.String(), candDigits) {
			score += 3.0
			evidence = true
		} else if candDigits != "" && spokenDigits.String() != "" && strings.HasSuffix(spokenDigits.String(), candDigits[len(candDigits)-1:]) {
			score += 1.0
			evidence = true
		}
		if spokenDir != 0 && candDir == spokenDir {
			score += 1.5
			evidence = true
		}
		if appr.onAssignedRunway(assignedApproach) || appr.matchesAssigned(assignedApproach) {
			score += 2.0
		}
		if appr.matchesType("ils") {
			score += 0.5 // garbled types are overwhelmingly ILS
		}
		if spokenVariant != "" && candVariant == spokenVariant {
			score += 0.3
			evidence = true
		}

		// Word-level alignment of the garbled span against the candidate's
		// spoken name: "very visual only one non" resembles "River Visual
		// runway one niner" far more than "r-nav yankee runway one niner",
		// which digits/direction/variant alone cannot see.
		align := approachNameAlignScore(tokens[i:end+1], appr.Spoken)
		score += 2 * align
		if align >= 0.45 {
			evidence = true
		}

		if score > bestScore || (score == bestScore && best != "" && appr.Id < best) {
			best, bestScore, bestEvidence = appr.Id, score, evidence
		}
	}

	// A recognizable approach-type word anywhere in the span (e.g. "cleared ILS"
	// with the runway garbled) confirms an approach clearance and satisfies the
	// evidence requirement even without a runway signal. The literal word
	// "approach" — including as the command boundary just past the span
	// ("cleared dialist approach") — counts the same way: it confirms this
	// is an approach clearance, not a misread "cleared direct FIX".
	hasTypeWord := false
	for k := i; k <= min(end+1, len(tokens)-1) && !hasTypeWord; k++ {
		// "localizer" is excluded: a bare localizer reference is an intercept
		// instruction ("at FIX intercept localizer" -> I), not a clearance.
		if t, _ := extractApproachType(tokens[k:]); t != "" && t != "localizer" {
			hasTypeWord = true
		}
		if WordScore(strings.ToLower(tokens[k].Text), "approach") >= 0.8 {
			hasTypeWord = true
		}
	}

	if bestScore < 1.5 || (requireEvidence && !bestEvidence && !hasTypeWord) {
		return "", 0
	}
	logLocalStt("  matchGarbledApproach: digits=%q dir=%c variant=%q -> %q (score=%.1f)",
		spokenDigits.String(), dirOrDash(spokenDir), spokenVariant, best, bestScore)
	return best, end + 1
}

// approachNameAlignScore measures how much of a candidate approach's
// spoken name shows through a garbled span. The span's tokens are aligned
// in order against the name's words; each aligned pair contributes its
// WordScore above a noise floor, and adjacent-token merges are tried so
// fusions and splits ("a very" for "river") still align. The result is
// the summed above-floor credit — roughly, the number of name words
// clearly present.
func approachNameAlignScore(tokens []Token, spokenName string) float64 {
	nameWords := NormalizeTranscript(spokenName)
	if len(nameWords) == 0 {
		return 0
	}
	const floor = 0.6

	pair := func(w, name string) float64 {
		if s := WordScore(w, name); s >= floor {
			return s - floor
		}
		return 0
	}

	// DP over (span position, name position): skip either side freely,
	// or align 1:1 / 2:1 (merged span tokens against one name word).
	prev := make([]float64, len(nameWords)+1)
	cur := make([]float64, len(nameWords)+1)
	twoAgo := make([]float64, len(nameWords)+1)
	for i := 1; i <= len(tokens); i++ {
		w := strings.ToLower(tokens[i-1].Text)
		for j := 1; j <= len(nameWords); j++ {
			cur[j] = max(prev[j], cur[j-1], prev[j-1]+pair(w, nameWords[j-1]))
			if i >= 2 {
				merged := strings.ToLower(tokens[i-2].Text) + w
				cur[j] = max(cur[j], twoAgo[j-1]+pair(merged, nameWords[j-1]))
			}
		}
		twoAgo, prev, cur = prev, cur, twoAgo
		clear(cur)
	}
	return prev[len(nameWords)]
}

// dirOrDash renders a direction byte for logging, using '-' for none.
func dirOrDash(dir byte) byte {
	if dir == 0 {
		return '-'
	}
	return dir
}

// extractAssignedVariant extracts the variant letter (z/y/x/w) from an
// assigned approach string. E.g., "rnav z runway 27" → "z",
// "ils z runway 6" → "z", "rnav yankee runway 13l" → "y".
func extractAssignedVariant(assignedLower string) string {
	var rest string
	if _, after, ok := strings.Cut(assignedLower, "rnav"); ok {
		rest = strings.TrimSpace(after)
	} else if _, after, ok := strings.Cut(assignedLower, "ils"); ok {
		rest = strings.TrimSpace(after)
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

// extractSpokenDirection looks for a direction word (left/right/center) in the tokens.
// Returns 'L', 'R', 'C', or 0 if no direction found.
func extractSpokenDirection(tokens []Token) byte {
	for i, t := range tokens {
		switch strings.ToLower(t.Text) {
		case "left", "west": // "west" is STT error for "left"
			return 'L'
		case "l":
			// An "l" directly followed by "s" is a fragment of "ILS"
			// ("cleared L S two three"), not a runway direction.
			if i+1 < len(tokens) && strings.EqualFold(tokens[i+1].Text, "s") {
				continue
			}
			return 'L'
		case "right", "r":
			return 'R'
		case "center", "c":
			return 'C'
		}
	}
	return 0
}

// matchApproachByTypeAndNumber tries to match approach by extracting the approach type
// (ILS, RNAV, visual, etc.) and runway number separately, ignoring garbage words between them.
// This handles cases like "ils front of a niner" where STT inserts garbage between type and number.
// assignedApproach is used to prefer the expected approach when there are ties.
// allowFallback controls whether to fall back to the assigned approach when the runway doesn't match.
// Set to true only when there's an explicit command keyword (cleared, expect).
func matchApproachByTypeAndNumber(tokens []Token, approaches []CandidateApproach, assignedApproach string) (string, float64, int) {
	return matchApproachByTypeAndNumberWithFallback(tokens, approaches, assignedApproach, true)
}

// matchApproachByTypeAndNumberWithFallback is the core implementation with fallback control.
func matchApproachByTypeAndNumberWithFallback(tokens []Token, approaches []CandidateApproach, assignedApproach string, allowFallback bool) (string, float64, int) {
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
				if approachTypeOf(assignedApproach) == approachType {
					_, assignedDir := approachRunway(assignedApproach)

					if assignedDir != 0 && assignedDir == spokenDir {
						// Find the approach that best matches the assigned approach.
						// We need to match the full runway number, not just the direction,
						// because multiple approaches may have the same direction (e.g., I3R, I8R).
						var bestApprID string
						for _, appr := range approaches {
							if appr.matchesType(approachType) && appr.onAssignedRunway(assignedApproach) {
								bestApprID = appr.Id
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
			for _, appr := range approaches {
				if appr.matchesType(approachType) && appr.variant() == approachVariant {
					matches = append(matches, appr.Id)
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
	var bestMatchesAssigned bool
	for _, appr := range approaches {

		// Check if the candidate is of the spoken approach type
		if !appr.matchesType(approachType) {
			continue
		}

		// Check if the candidate's runway matches our extracted runway
		// The runway in the candidate should start with our spoken runway number
		if !runwayMatches(strings.ToLower(appr.Spoken), runwaySpoken) {
			continue
		}

		// If we extracted a variant letter (e.g., "zulu" → "z"), the candidate must match it.
		// This distinguishes RNAV Z from RNAV Y approaches.
		if approachVariant != "" && appr.variant() != approachVariant {
			continue
		}

		// We have a type+number match - calculate confidence based on specificity
		score := 0.95 // High confidence for type+number match

		// Tie-breaker: prefer assigned approach, then alphabetically earlier
		isBetter := score > bestScore
		if !isBetter && score == bestScore && bestAppr != "" {
			thisMatchesAssigned := appr.matchesAssigned(assignedApproach)
			if thisMatchesAssigned && !bestMatchesAssigned {
				isBetter = true
			} else if thisMatchesAssigned == bestMatchesAssigned && appr.Id < bestAppr {
				isBetter = true
			}
		}

		if isBetter || bestAppr == "" {
			bestAppr = appr.Id
			bestMatchesAssigned = appr.matchesAssigned(assignedApproach)
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
		// Check if assigned approach has the same type
		if approachTypeOf(assignedApproach) == approachType {
			_, assignedDir := approachRunway(assignedApproach)

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

			// If directions match, find the assigned approach among the candidates
			if assignedDir != 0 && assignedDir == spokenDirChar {
				for _, appr := range approaches {
					if appr.matchesAssigned(assignedApproach) {
						consumed := typeConsumed + numPos + 1
						if runwayDir != "" {
							consumed++
						}
						logLocalStt("  matchApproachByTypeAndNumber: runway mismatch, falling back to assigned approach %q (type=%q dir=%c)",
							appr.Id, approachType, spokenDirChar)
						return appr.Id, 0.85, consumed // Lower confidence for fallback
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

// approachVariantWords are the only NATO letters an approach variant uses;
// the FAA numbers alternate procedures downward from Z.
var approachVariantWords = []struct{ word, letter string }{
	{"whiskey", "w"}, {"x-ray", "x"}, {"yankee", "y"}, {"zulu", "z"},
}

// approachVariantLetter recognizes a spoken approach variant word, tolerating
// whisper's garbles of it ("zulu's", "zoolu"). Losing the variant is
// expensive: with nothing to tell RNAV Y from RNAV Z, every downstream
// tie-break falls back to alphabetical order and silently picks Y.
func approachVariantLetter(word string) (string, bool) {
	best, bestScore := "", 0.0
	for _, v := range approachVariantWords {
		if s := WordScore(word, v.word); s >= 0.85 && s > bestScore {
			best, bestScore = v.letter, s
		}
	}
	return best, best != ""
}

// extractApproachVariant extracts an approach variant letter from the start
// of tokens, skipping filler ("r-nav the zulu").
// Returns the variant letter (lowercase) and number of tokens consumed.
func extractApproachVariant(tokens []Token) (string, int) {
	pos := 0
	for pos < len(tokens) && IsFillerWord(strings.ToLower(tokens[pos].Text)) {
		pos++
	}
	if pos >= len(tokens) {
		return "", 0
	}

	if letter, ok := approachVariantLetter(strings.ToLower(tokens[pos].Text)); ok {
		return letter, pos + 1
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

// runwayConsistent returns true when the candidate's "runway X" portion is
// compatible with the spoken runway number. Compatibility is first-token
// equality (so spoken "three" matches candidate "runway three zero" when a
// pilot trimmed the trailing digit) with niner/nine normalized as equivalent.
// Returns true when the candidate has no "runway " marker (don't gate it).
func runwayConsistent(candidateLower, spokenRunwayNum string) bool {
	_, after, ok := strings.Cut(candidateLower, "runway ")
	if !ok {
		return true
	}
	candFirst, _, _ := strings.Cut(after, " ")
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
	_, after, ok := strings.Cut(spokenLower, "runway ")
	if !ok {
		return false
	}

	// Get the part after "runway "
	runwayPart := after // len("runway ") == 7

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

// precedingResemblesNamedVisual reports whether the token(s) just before
// position pos (where a "visual" approach-type word matched) resemble the
// leading name word of a charted visual approach among the candidates —
// e.g. "a very" before "visual" resembling "River" of "River Visual".
func precedingResemblesNamedVisual(tokens []Token, pos int, approaches []CandidateApproach) bool {
	if pos == 0 {
		return false
	}
	texts := []string{strings.ToLower(tokens[pos-1].Text)}
	if pos >= 2 {
		texts = append(texts, strings.ToLower(tokens[pos-2].Text)+texts[0])
	}
	for _, appr := range approaches {
		words := strings.Fields(strings.ToLower(appr.Spoken))
		if len(words) < 2 || words[0] == "visual" || !slices.Contains(words, "visual") {
			continue
		}
		if slices.ContainsFunc(texts, func(t string) bool {
			return JaroWinkler(t, words[0]) >= 0.7 || phoneticScore(t, words[0]) >= scorePhoneticPartial
		}) {
			return true
		}
	}
	return false
}

func matchVisualApproach(tokens []Token, candidates map[string]string) (string, int) {
	approachType, typeConsumed := extractApproachType(tokens)
	if approachType != "visual" {
		return "", 0
	}

	remainingTokens := tokens[typeConsumed:]
	runwaySpoken, runwayDir, numPos := extractRunwayNumber(remainingTokens)
	if runwaySpoken == "" {
		// A 3-digit number whose leading two digits form a runway has a
		// garbled trailing digit merged in ("one zero nine" for "one zero
		// right" -> 109); recover the runway and let the unique-candidate
		// check below sort out the direction.
		for i, t := range remainingTokens {
			if t.Type == TokenNumber && t.Value > 100 && t.Value <= 369 {
				if rwy := t.Value / 10; rwy >= 1 && rwy <= 36 {
					runwaySpoken, numPos = spokenDigits(rwy), i
					break
				}
			}
		}
		if runwaySpoken == "" {
			return "", 0
		}
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

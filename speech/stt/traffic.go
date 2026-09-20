package stt

import (
	"strings"
)

// This file extracts traffic advisories: o'clock position, distance in
// miles, altitude, and the trailing visual-separation phrasing. The
// extraction is phase-structured (position, then distance, then
// altitude) since advisories follow that spoken order; numeric garble
// recovery is delegated to DecodeNumber.

// extractTraffic extracts traffic advisory components: o'clock position, distance in miles, and altitude.
// Pattern: "(N) o'clock, (M) miles, (direction), (aircraft type), (at) (altitude)"
// Returns o'clock (1-12), miles, encoded altitude (in 100s of feet),
// whether the altitude was given as "altitude unknown", whether other
// traffic will maintain visual separation, and tokens consumed. The
// direction and aircraft type are ignored.
func extractTraffic(tokens []Token) (int, int, int, bool, bool, int) {
	if len(tokens) == 0 {
		return 0, 0, 0, false, false, 0
	}

	consumed := 0
	var oclock, miles, alt int
	var altUnknown bool

	// Phase 1: Find o'clock position (1-12)
	for consumed < len(tokens) && consumed < 10 {
		t := tokens[consumed]
		text := strings.ToLower(t.Text)

		// Skip filler words
		if IsFillerWord(text) || text == "at" || text == "your" {
			consumed++
			continue
		}

		// Check for number followed by "o'clock" or just a number in range
		// 1-12. Merged-digit recovery ("eight eleven o'clock" -> 811 ->
		// 11) requires the explicit "o'clock" anchor.
		if t.Type == TokenNumber {
			oclockFollows := consumed+1 < len(tokens) && FuzzyMatch(tokens[consumed+1].Text, "o'clock", 0.8)

			if cands := DecodeNumber(tokens, consumed, NumberContext{Kind: NumOClock}); len(cands) > 0 &&
				(cands[0].Score == numScoreLiteral || oclockFollows) {
				oclock = cands[0].Value
				if cands[0].Score < numScoreLiteral {
					logLocalStt("  extractTraffic: extracted o'clock %d from merged %d", oclock, t.Value)
				}
				consumed++
				// Skip "o'clock" if present
				if oclockFollows {
					consumed++
				}
				break
			}
		}
		consumed++
	}

	if oclock == 0 {
		return 0, 0, 0, false, false, 0
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
		return 0, 0, 0, false, false, 0
	}

	// Phase 3: Skip direction, runway relationship, and aircraft type; find altitude.
	// The altitude is the current position of the traffic. If followed by "climbing/descending XXXX",
	// we use the first altitude (current), not the target.
	// Altitudes are multiples of 100 feet; aircraft types like 787 are not.
	otherAircraftWillMaintainVisualSeparation := false
	for consumed < len(tokens) && consumed < 40 {
		t := tokens[consumed]

		if next, ok := consumeTrafficLandingParallel(tokens, consumed); ok {
			consumed = next
			continue
		}

		// Check for "altitude unknown" / "unknown altitude" phrasing.
		if next, ok := consumeAltitudeUnknown(tokens, consumed); ok {
			altUnknown = true
			consumed = next
			break
		}

		// Check for an altitude reading at this position. Value decoding
		// (including merged aircraft-type recovery and thousand/hundred
		// anchors) is DecodeNumber's job; the phase logic here handles
		// aircraft-type vetoes, cross-token refinement, and trailing
		// climb/descend phrasing.
		if t.Type == TokenAltitude {
			cands := DecodeNumber(tokens, consumed, NumberContext{Kind: NumTrafficAltitude})
			if len(cands) == 0 {
				// Implausible altitude with no recovery - skip and keep looking
				consumed++
				continue
			}
			c := cands[0]
			alt = c.Value
			consumed += c.Consumed

			if c.Score < numScoreLiteral {
				logLocalStt("  extractTraffic: extracted altitude %d from implausible %d", alt, t.Value)
				// Check if the next token is also an implausible altitude that
				// refines this one (adds hundreds). This handles garbled speech
				// like "737 five thousand eight five thousand nine hundred" where
				// the first token gives thousands (50) and the second adds
				// hundreds (59).
				if consumed < len(tokens) && tokens[consumed].Type == TokenAltitude && tokens[consumed].Value > 600 {
					nextExtracted := tokens[consumed].Value % 100
					if nextExtracted > alt && nextExtracted <= 60 && nextExtracted/10 == alt/10 {
						logLocalStt("  extractTraffic: refined altitude %d -> %d from implausible %d", alt, nextExtracted, tokens[consumed].Value)
						alt = nextExtracted
						consumed++
					}
				}
			} else {
				// Check if a nearby token refines this altitude (adds hundreds).
				// e.g., "5 thousand [noise] 5 thousand 9 hundred" → tokens 50, [8], 59.
				// Pick 59 over 50 since it's a more precise reading of the same altitude.
				for j := consumed; j < len(tokens) && j < consumed+3; j++ {
					if tokens[j].Type == TokenAltitude && tokens[j].Value > alt &&
						tokens[j].Value <= 600 && tokens[j].Value/10 == alt/10 {
						logLocalStt("  extractTraffic: refined altitude %d -> %d", alt, tokens[j].Value)
						alt = tokens[j].Value
						consumed = j + 1
						break
					}
				}
			}
			break
		}

		// Skip aircraft type numbers (737, 787, A320, etc.) - these are not altitudes
		if t.Type == TokenNumber && isAircraftTypeNumber(t.Value) {
			consumed++
			continue
		}

		// Check for number that might be altitude
		if t.Type == TokenNumber {
			if cands := DecodeNumber(tokens, consumed, NumberContext{Kind: NumTrafficAltitude}); len(cands) > 0 {
				c := cands[0]

				// Prefer an explicitly "at"-anchored altitude just ahead:
				// "westbound eighty three hundred at three thousand" — the
				// "at" reading is the traffic's altitude, the unanchored
				// one is garble.
				for j := consumed + c.Consumed; j < len(tokens) && j <= consumed+c.Consumed+3; j++ {
					if strings.ToLower(tokens[j].Text) != "at" || j+1 >= len(tokens) {
						continue
					}
					if ac := DecodeNumber(tokens, j+1, NumberContext{Kind: NumTrafficAltitude}); len(ac) > 0 {
						logLocalStt("  extractTraffic: preferring at-anchored altitude %d over %d", ac[0].Value, c.Value)
						alt = ac[0].Value
						consumed = j + 1 + ac[0].Consumed
					}
					break
				}
				if alt > 0 {
					break
				}

				alt = c.Value
				consumed += c.Consumed
				// After a raw-feet reading, skip "climbing/descending to
				// XXXX" - we use current altitude, not target.
				if c.Consumed == 1 && consumed < len(tokens) {
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

		// No altitude detected at this token. Before consuming it as
		// noise, check if it begins the "(other aircraft) has you in
		// sight and will maintain visual separation" phrase — if so,
		// we're past the altitude (none was given) and need to stop
		// here so the post-loop trailing-word consumer doesn't run
		// over it.
		if next, ok := consumeOtherAircraftMaintainsVisualSeparation(tokens, consumed); ok {
			otherAircraftWillMaintainVisualSeparation = true
			consumed = next
			break
		}

		consumed++
	}

	// If we got o'clock and miles but no altitude, treat altitude as unknown.
	// Controllers commonly omit altitude for parallel-final or pop-up traffic.
	if alt == 0 && !altUnknown {
		altUnknown = true
	}

	if !otherAircraftWillMaintainVisualSeparation {
		if next, ok := consumeOtherAircraftMaintainsVisualSeparation(tokens, consumed); ok {
			otherAircraftWillMaintainVisualSeparation = true
			consumed = next
		}
	}

	// Consume trailing traffic advisory words that follow the altitude.
	// These are part of the traffic call and should not be re-parsed as commands.
	// Pattern: "[descending/climbing] [report [traffic] in sight]"
	//
	// If "airport" or "field" appears in the upcoming tokens, the trailing
	// phrase is requesting an airport sighting (e.g. "report it and the airport
	// in sight"). Leave those tokens in place so the AP handler can match.
	hasAirportSighting := false
	for j := consumed; j < len(tokens) && j < consumed+8; j++ {
		text := strings.ToLower(tokens[j].Text)
		if text == "airport" || text == "field" {
			hasAirportSighting = true
			break
		}
	}
	if !hasAirportSighting {
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
	}

	return oclock, miles, alt, altUnknown, otherAircraftWillMaintainVisualSeparation, consumed
}

// consumeAltitudeUnknown recognizes "[at] altitude unknown" or
// "unknown altitude" — the controller is reporting traffic from a primary
// return without Mode C. STT transcription is noisy, so each word is matched
// fuzzily. Returns the new token index and true if matched.
func consumeAltitudeUnknown(tokens []Token, pos int) (int, bool) {
	// Skip a leading "at".
	start := pos
	if start < len(tokens) && strings.ToLower(tokens[start].Text) == "at" {
		start++
	}
	if start+1 >= len(tokens) {
		return pos, false
	}

	a, b := tokens[start].Text, tokens[start+1].Text
	if FuzzyMatch(a, "altitude", 0.8) && FuzzyMatch(b, "unknown", 0.8) {
		return start + 2, true
	}
	if FuzzyMatch(a, "unknown", 0.8) && FuzzyMatch(b, "altitude", 0.8) {
		return start + 2, true
	}
	return pos, false
}

// consumeOtherAircraftMaintainsVisualSeparation recognizes the phrase
// "...you in sight and will maintain visual separation" at the end of a
// traffic advisory. The leading subject ("they have", "(callsign) has", "it
// has", etc.) is absorbed by the skip-token branch, since real transcripts
// vary the subject pronoun and verb form. STT transcription is noisy, so we
// fuzzy-match each word, tolerate a dropped word by advancing the phrase,
// and tolerate extra spurious tokens by skipping them. The phrase is
// accepted if at least 7 of its 8 words match.
func consumeOtherAircraftMaintainsVisualSeparation(tokens []Token, pos int) (int, bool) {
	phrase := []string{"you", "in", "sight", "and", "will", "maintain", "visual", "separation"}

	phraseIdx, tokenIdx := 0, pos
	matches := 0
	lastMatchedTokenIdx := pos - 1
	endTokenIdx := min(pos+len(phrase)+3, len(tokens))

	for phraseIdx < len(phrase) && tokenIdx < endTokenIdx {
		text := tokens[tokenIdx].Text

		if FuzzyMatch(text, phrase[phraseIdx], 0.8) {
			matches++
			lastMatchedTokenIdx = tokenIdx
			phraseIdx++
			tokenIdx++
			continue
		}

		// The expected phrase word may have been dropped; if the following
		// phrase word matches the current token, skip ahead in the phrase.
		if phraseIdx+1 < len(phrase) && FuzzyMatch(text, phrase[phraseIdx+1], 0.8) {
			phraseIdx++
			continue
		}

		// Otherwise treat the current token as a stray insertion and skip it.
		tokenIdx++
	}

	if matches < 7 {
		return pos, false
	}
	return lastMatchedTokenIdx + 1, true
}

// consumeTrafficPositionDescriptor recognizes a descriptor-style traffic
// position phrase that does not include an o'clock value. Used for traffic
// advisories where the controller gives only relative direction:
//   - "[off|to] [your] left|right|nose|tail"
//   - "[from] [the] {compass_dir}" (north, south, east, west, ...)
//
// Note: "off", "to", "your", and "the" are filler words elsewhere in the
// pipeline and may already have been stripped before reaching this point,
// so this function also accepts the bare direction word.
func consumeTrafficPositionDescriptor(tokens []Token, pos int) (int, bool) {
	if pos >= len(tokens) {
		return pos, false
	}

	cur := pos
	// Optional "off" / "to" / "from".
	if t := strings.ToLower(tokens[cur].Text); t == "off" || t == "to" || t == "from" {
		cur++
	}
	// Optional "your" / "the".
	if cur < len(tokens) {
		if t := strings.ToLower(tokens[cur].Text); t == "your" || t == "the" {
			cur++
		}
	}
	if cur >= len(tokens) {
		return pos, false
	}

	t := strings.ToLower(tokens[cur].Text)
	if t == "left" || t == "right" || t == "nose" || t == "tail" {
		return cur + 1, true
	}
	if _, ok := compassDirections[t]; ok {
		return cur + 1, true
	}
	for long := range compassDirections {
		if len(t) >= 3 && FuzzyMatch(t, long, 0.8) {
			return cur + 1, true
		}
	}
	return pos, false
}

func consumeTrafficLandingParallel(tokens []Token, pos int) (int, bool) {
	if pos >= len(tokens) {
		return pos, false
	}

	text := strings.ToLower(tokens[pos].Text)
	if text != "landing" && text != "land" {
		return pos, false
	}

	next := pos + 1
	if next < len(tokens) && strings.ToLower(tokens[next].Text) == "the" {
		next++
	}
	if next >= len(tokens) || !FuzzyMatch(tokens[next].Text, "parallel", 0.8) {
		return pos, false
	}
	next++

	if next < len(tokens) && FuzzyMatch(tokens[next].Text, "runway", 0.8) {
		next++
	}
	return next, true
}

// extractHoldParams extracts hold parameters from tokens for controller-specified holds.
// Pattern: "on the (radial) radial [inbound], (minutes) minute legs, (left/right) turns"
// Returns the full hold command string and number of tokens consumed.
// Returns empty string and 0 if no hold parameters found.

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

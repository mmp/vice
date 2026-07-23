package stt

import (
	"maps"
	"math"
	"slices"
	"strings"
)

// This file implements the command-sequence decoder: a small beam search
// over segmentations of the post-callsign tokens into commands, discourse
// markers ("then"), and skipped noise. Each state carries a cumulative
// score; states are ranked by coverage-adjusted score so an interpretation
// that explains tokens as commands outranks one that skips them, and the
// best-scoring complete segmentation wins.

// beamWidth bounds how many candidate segmentations are tracked.
const beamWidth = 4

// beamState is one candidate segmentation of the command tokens. score is
// the sum of the coverage-adjusted scores of its matches: each matched
// token is credited by how much its quality exceeds the noise baseline,
// while skipped tokens contribute nothing, so segmentations that explain
// the transcript outrank ones that skip it.
type beamState struct {
	pos        int
	commands   []string
	totalConf  float64 // sum of per-command match confidences
	score      float64 // cumulative coverage-adjusted score
	isThen     bool
	cats       map[string]bool   // command categories already matched
	fixes      map[string]string // ac.Fixes, extended by expect-approach injection
	kinds      uint8             // bitset (1<<commandKind) of matched segment kinds
	matchedAny bool              // a pattern matched, even if it emitted no command
}

// transmissionParse is the decoded interpretation of the post-callsign
// tokens: the commands to issue plus what kinds of segments were matched,
// so output assembly can recognize informational-only transmissions.
type transmissionParse struct {
	commands []string
	conf     float64
	score    float64 // winning segmentation's coverage-adjusted score
	kinds    uint8   // bitset (1<<commandKind) of matched segment kinds
}

// sawKind reports whether a segment of kind k was matched.
func (tp transmissionParse) sawKind(k commandKind) bool {
	return tp.kinds&(1<<k) != 0
}

// ParseCommands parses tokens into a sequence of commands using the
// registered command templates.
func ParseCommands(tokens []Token, ac Aircraft) ([]string, float64) {
	tp := parseTransmission(tokens, ac)
	return tp.commands, tp.conf
}

// parseTransmission parses tokens into commands plus the kinds of the
// matched segments.
func parseTransmission(tokens []Token, ac Aircraft) transmissionParse {
	logLocalStt("ParseCommands: %d tokens", len(tokens))
	if len(tokens) == 0 {
		return transmissionParse{}
	}

	best := decodeCommands(tokens, ac)

	// Make the winning state's injected approach fixes visible to the
	// caller (validation checks fixes against the same map).
	if ac.Fixes != nil {
		maps.Copy(ac.Fixes, best.fixes)
	}

	commands := best.commands
	if len(commands) == 0 {
		if best.matchedAny {
			// A command pattern matched but produced no output (e.g.,
			// "standby for the approach") — return empty commands with
			// positive confidence so the caller doesn't treat this as a
			// failed parse.
			logLocalStt("ParseCommands: matched but no commands to issue")
			return transmissionParse{conf: 1, score: best.score, kinds: best.kinds}
		}
		logLocalStt("ParseCommands: no commands found")
		return transmissionParse{score: best.score, kinds: best.kinds}
	}

	// Post-processing: if "knots" appears in the transcript, convert altitude commands to speed
	commands = convertAltitudeToSpeedIfKnots(tokens, commands)
	commands = resolveExpedite(commands)
	commands = coalesceAfterFixAltitudes(commands)
	commandsBeforeApprovalFilter := len(commands)
	commands = removeCombinedApproved(commands)
	totalConf := best.totalConf - float64(commandsBeforeApprovalFilter-len(commands))

	avgConf := totalConf / float64(len(commands))
	logLocalStt("ParseCommands: result=%v (avgConf=%.2f)", commands, avgConf)
	return transmissionParse{commands: commands, conf: avgConf, score: best.score, kinds: best.kinds}
}

// decodeCommands runs the beam search and returns the best complete state.
func decodeCommands(tokens []Token, ac Aircraft) beamState {
	states := []beamState{{fixes: ac.Fixes}}

	for range 2 * len(tokens) {
		anyLive := false
		var next []beamState
		for _, st := range states {
			if st.pos >= len(tokens) {
				next = append(next, st)
				continue
			}
			anyLive = true
			next = append(next, expandState(st, tokens, ac)...)
		}
		if !anyLive {
			break
		}

		slices.SortStableFunc(next, func(a, b beamState) int {
			if a.score != b.score {
				if a.score > b.score {
					return -1
				}
				return 1
			}
			// Deterministic tie-breaks: further along, then more commands.
			if a.pos != b.pos {
				return b.pos - a.pos
			}
			return len(b.commands) - len(a.commands)
		})
		if len(next) > beamWidth {
			next = next[:beamWidth]
		}
		states = next
	}

	for i, st := range states {
		logLocalStt("  beam final[%d]: score=%.3f pos=%d commands=%v", i, st.score, st.pos, st.commands)
	}
	return states[0]
}

// expandState generates the successor states for one step of the beam.
func expandState(st beamState, tokens []Token, ac Aircraft) []beamState {
	pos := st.pos

	// "then" sequencing. Also treat "the" as "then" when followed by an
	// altitude command word and we already have at least one command —
	// STT often garbles "then" as "the".
	if tokens[pos].Text == "then" || (len(st.commands) > 0 && tokens[pos].Text == "the" && pos+1 < len(tokens) &&
		startsAltitudeCommand(tokens[pos+1])) {
		logLocalStt("  found 'then' (or 'the' as then) at position %d", pos)
		st.pos++
		st.isThen = true
		return []beamState{st}
	}

	// "at {altitude}" implies "then" sequencing for what follows.
	if tokens[pos].Text == "at" && pos+1 < len(tokens) && looksLikeAltitude(tokens[pos+1]) {
		logLocalStt("  found 'at {altitude}' pattern at position %d, triggering then", pos)
		st.pos += 2
		st.isThen = true
		return []beamState{st}
	}

	var succ []beamState
	matched := false

	// Match commands at this position, branching on competing
	// segmentations. Position identifications only occur at the head of a
	// transmission (right after the callsign); elsewhere a position word
	// is part of a command ("contact Boston approach") or trailing
	// chatter.
	acHere := ac
	acHere.Fixes = st.fixes
	matches := matchCommands(tokens, pos, acHere, st.isThen, st.cats)
	matches = slices.DeleteFunc(matches, func(m CommandMatch) bool {
		return m.Kind == kindPositionID && pos > 4
	})
	for i, match := range matches {
		logLocalStt("  matched command: %q [%s] (pos=%d, conf=%.2f, consumed=%d, isThen=%v)",
			match.Command, match.Name, pos, match.Confidence, match.Consumed, st.isThen)
		matched = true
		next := applyMatch(st, match, pos+match.Consumed, acHere)
		if i > 0 {
			next.score += math.Log(scoreAltSegmentation)
		}
		succ = append(succ, next)
	}
	if len(matches) == 0 {
		// An approach name without an "expect"/"cleared" prefix. This is
		// weak evidence — no clearance keyword was heard — so like
		// SAYAGAIN it ranks above skipping the tokens but below any
		// keyword-anchored parse of the same span.
		if cmd, consumed := tryImplicitApproachMatch(tokens[pos:], acHere); consumed > 0 {
			logLocalStt("  implicit approach match: %q (consumed=%d)", cmd, consumed)
			imp := st
			imp.commands = append(slices.Clone(st.commands), cmd)
			imp.totalConf += 1.0
			imp.score += float64(consumed) * scoreSayAgainCredit
			imp.pos += consumed
			imp.matchedAny = true
			succ = append(succ, imp)
		}
	}

	// Skip this token. Filler words skip freely — a template that
	// explains one (like an acknowledgment) still wins on match credit.
	// Content noise costs the same everywhere — a beam skip here and
	// slack absorption inside a template both earn exactly the baseline
	// (zero net credit) — so an interpretation can't win by laundering
	// its skips through template slack. Skipping a content token where a
	// match was available is the exception: that is nearly prohibited, so
	// the decoder overrides an available interpretation only for a large
	// downstream gain.
	skip := st
	skip.pos++
	if IsFillerWord(tokens[pos].Text) {
		logLocalStt("  skipping filler word: %q", tokens[pos].Text)
	} else {
		logLocalStt("  no match at token[%d]=%q, skipping", pos, tokens[pos].Text)
		if matched {
			skip.score += math.Log(scoreSkipOverMatch)
		}
	}
	succ = append(succ, skip)

	return succ
}

// applyMatch extends a state with a template match, tracking category
// exclusions and expect-approach fix injection.
func applyMatch(st beamState, match CommandMatch, newPos int, ac Aircraft) beamState {
	st.commands = slices.Clone(st.commands)
	st.matchedAny = true
	st.kinds |= 1 << match.Kind

	if match.Command != "" {
		// A handler may emit multiple space-separated commands (e.g.,
		// "D{fix} A{fix}/I"). Split so each is tracked, categorized, and
		// post-processed individually.
		for _, cmd := range strings.Fields(match.Command) {
			st.commands = append(st.commands, cmd)
			st.totalConf += match.Confidence

			if category := getCommandCategory(cmd); category != "" {
				st.cats = cloneSet(st.cats)
				st.cats[category] = true
			}

			// If this is an expect approach command, add the approach's
			// fixes to the aircraft context for subsequent command parsing.
			if strings.HasPrefix(cmd, "E") && len(cmd) > 1 {
				approachID := cmd[1:]
				// Strip LAHSO suffix if present (e.g., "I22L/LAHSO26" -> "I22L")
				if idx := strings.Index(approachID, "/LAHSO"); idx != -1 {
					approachID = approachID[:idx]
				}
				if approachFixes, ok := ac.ApproachFixes[approachID]; ok {
					logLocalStt("  adding %d fixes from approach %s to aircraft context", len(approachFixes), approachID)
					fixes := make(map[string]string, len(st.fixes)+len(approachFixes))
					maps.Copy(fixes, st.fixes)
					for spoken, fix := range approachFixes {
						if _, exists := fixes[spoken]; !exists {
							fixes[spoken] = fix
						}
					}
					st.fixes = fixes
				}
			}
		}
	}

	st.score += match.Score
	st.pos = newPos
	st.isThen = false
	return st
}

// startsAltitudeCommand reports whether the token reads as the start of an
// altitude command (descend/climb/maintain), including garbled renderings.
func startsAltitudeCommand(t Token) bool {
	text := strings.ToLower(t.Text)
	for _, kw := range []string{"descend", "climb", "maintain"} {
		if WordScore(text, kw) >= 0.8 {
			return true
		}
		for _, c := range confusionTable[text] {
			if strings.HasPrefix(c.target, kw) {
				return true
			}
		}
	}
	return false
}

// cloneSet returns a copy of the set, treating nil as empty.
func cloneSet(s map[string]bool) map[string]bool {
	c := make(map[string]bool, len(s)+1)
	maps.Copy(c, s)
	return c
}

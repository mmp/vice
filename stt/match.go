package stt

import (
	"math"
	"reflect"
	"slices"
	"strings"
)

// This file implements scored template matching. A template's matchers
// extend partial matches through the token stream, accumulating a
// log-score from the WordScores of matched keywords, slot values, and
// penalties for tokens skipped as noise. Competing complete matches are
// then ranked by per-token score, so a template whose words align well
// with the transcript beats one that had to stretch, and the ranking
// composes with callsign and validation scoring downstream.

// templateBeamWidth bounds how many partial matches are tracked while
// extending a single template.
const templateBeamWidth = 4

// partialMatch is an in-progress match of a template against the tokens.
type partialMatch struct {
	pos      int     // absolute position in tokens after consumption
	values   []any   // extracted slot values, in template order
	logScore float64 // sum of log(score) over matched words, values, and skips
	nTok     int     // scored (non-filler) tokens consumed
}

// matcher is the interface for template elements.
type matcher interface {
	// extend returns the ways this element can extend pm, best first, or
	// none if a required element cannot match. sayAgain carries the slot's
	// SAYAGAIN type hint when a slot's value could not be parsed.
	extend(tokens []Token, ac Aircraft, skipWords []string, allowSlack bool, pm partialMatch) (exts []partialMatch, sayAgain string)

	// isOptional reports whether this element may consume no tokens.
	isOptional() bool
}

// phraseWord is one word position in a phraseMatcher: a set of keyword
// alternatives, possibly optional.
type phraseWord struct {
	keywords []string
	optional bool
}

// phraseMatcher matches a run of adjacent literal template words (required
// and optional) as a unit.
type phraseMatcher struct {
	words []phraseWord
}

func (m *phraseMatcher) isOptional() bool {
	for _, w := range m.words {
		if !w.optional {
			return false
		}
	}
	return true
}

func (m *phraseMatcher) extend(tokens []Token, ac Aircraft, skipWords []string, allowSlack bool, pm partialMatch) ([]partialMatch, string) {
	var results []partialMatch
	// Slack is only available once the phrase is anchored: either a prior
	// element already consumed tokens, or a word of this phrase matched.
	// Unmatched leading optionals must not let the first required word
	// hunt forward ("[the] airport ..." reaching past unrelated words).
	var walk func(wi int, cur partialMatch, anchored bool)
	walk = func(wi int, cur partialMatch, anchored bool) {
		for ; wi < len(m.words); wi++ {
			w := m.words[wi]
			if w.optional {
				prev := cur.pos
				next := matchOptionalWord(tokens, cur, w.keywords)
				if next.pos > prev {
					// A doubled keyword — an optional echoing the required
					// word before it ("direct [direct]") — may really be a
					// garbled slot value ("direct direct" for "direct
					// DARIC"): branch on leaving it unconsumed; scoring
					// picks the reading.
					if pi := wi - 1; pi >= 0 && !m.words[pi].optional &&
						keywordsOverlap(m.words[pi].keywords, w.keywords) {
						walk(wi+1, cur, anchored)
					}
					cur = next
					anchored = true
				}
				continue
			}
			// STT often merges adjacent words into one token ("turnwright"
			// for "turn right"): the token may satisfy this word fused with
			// the next required word. Explored as a branch, not a fallback:
			// a token like "cautionway" also direct-matches "caution" alone,
			// and only the rest of the phrase decides which reading is right.
			if ni := m.nextRequired(wi + 1); ni >= 0 {
				if next, ok := matchFusedWords(tokens, cur, w.keywords, m.words[ni].keywords); ok {
					walk(ni+1, next, true)
				}
			}
			next, ok := matchRequiredWord(tokens, cur, w.keywords, skipWords, anchored)
			if !ok {
				return
			}
			cur = next
			anchored = true
		}
		results = append(results, cur)
	}
	walk(0, pm, allowSlack)
	return results, ""
}

// keywordsOverlap reports whether the two keyword alternative sets share
// a word.
func keywordsOverlap(a, b []string) bool {
	return slices.ContainsFunc(a, func(w string) bool { return slices.Contains(b, w) })
}

// nextRequired returns the index of the first non-optional word at or
// after wi, or -1.
func (m *phraseMatcher) nextRequired(wi int) int {
	for ; wi < len(m.words); wi++ {
		if !m.words[wi].optional {
			return wi
		}
	}
	return -1
}

// matchFusedWords matches one transcript token against two adjacent
// template words ("turnwright" ~ "turn"+"right"). The token is split at
// every position and both halves must independently match their word —
// one fuzzy concatenation score would let long tokens falsely anchor
// templates.
func matchFusedWords(tokens []Token, pm partialMatch, first, second []string) (partialMatch, bool) {
	pos := pm.pos
	for pos < len(tokens) && IsFillerWord(strings.ToLower(tokens[pos].Text)) {
		pos++
	}
	if pos >= len(tokens) {
		return pm, false
	}
	text := strings.ToLower(tokens[pos].Text)

	// Known two-word confusions: the transcript word stands for both
	// template words but carries too little letter evidence for the
	// split verification below ("fighting" for "fly heading").
	for _, k1 := range first {
		for _, k2 := range second {
			if c := confusionScore(text, k1+" "+k2); c >= 0.8 {
				pm.pos = pos + 1
				pm.logScore += math.Log(c)
				pm.nTok++
				return pm, true
			}
		}
	}

	// A fused token is necessarily long; short ones can't span two words.
	if len(text) < 7 {
		return pm, false
	}

	best := 0.0
	for split := 3; split <= len(text)-2; split++ {
		prefix, suffix := text[:split], text[split:]
		for _, k1 := range first {
			s1 := WordScore(prefix, k1)
			if s1 < 0.8 {
				continue
			}
			for _, k2 := range second {
				if s2 := WordScore(suffix, k2); s2 >= 0.8 {
					if s := min(s1, s2); s > best {
						best = s
					}
				}
			}
		}
	}
	if best == 0 {
		return pm, false
	}
	pm.pos = pos + 1
	pm.logScore += math.Log(best)
	pm.nTok++
	return pm, true
}

// matchOptionalWord matches an optional literal word at the current
// position only; if it doesn't match, the partial passes unchanged.
func matchOptionalWord(tokens []Token, pm partialMatch, keywords []string) partialMatch {
	if pm.pos >= len(tokens) {
		return pm
	}
	text := strings.ToLower(tokens[pm.pos].Text)
	for _, kw := range keywords {
		// Number tokens and short keywords (2 chars or less like "to",
		// "at") require an exact match to prevent false positives like
		// "torch" matching "to".
		if tokens[pm.pos].Type != TokenWord || len(kw) <= 2 {
			if text == kw {
				pm.pos++
				pm.nTok++
				return pm
			}
			continue
		}
		// Longer keywords fuzzy match, but not across a >2x length
		// difference (e.g., "torch" vs "direct").
		if len(text) > 2*len(kw) || len(kw) > 2*len(text) {
			continue
		}
		if ws := WordScore(text, kw); ws >= 0.8 {
			pm.pos++
			pm.logScore += math.Log(ws)
			pm.nTok++
			return pm
		}
	}
	return pm
}

// matchRequiredWord matches a required literal word, skipping leading
// filler and optionally up to 3 noise tokens (slack).
func matchRequiredWord(tokens []Token, pm partialMatch, keywords []string, skipWords []string, allowSlack bool) (partialMatch, bool) {
	pos := pm.pos

	// Skip filler words — but don't skip a filler that is one of this
	// word's targets. Some words (e.g. "to") are ambient filler globally,
	// yet carry meaning in specific templates ("reduce speed X to FIX").
	for pos < len(tokens) {
		text := strings.ToLower(tokens[pos].Text)
		if IsFillerWord(text) && !slices.Contains(keywords, text) {
			pos++
			continue
		}
		break
	}
	if pos >= len(tokens) {
		return pm, false
	}

	text := strings.ToLower(tokens[pos].Text)

	// If the next token is a TokenAltitude (high-confidence altitude from a
	// "thousand" pattern), require a near-exact match. This prevents weak
	// matches like "claimed"→"climbed" from consuming altitude tokens that
	// should be handled by standalone_altitude.
	threshold := 0.80
	if pos+1 < len(tokens) && tokens[pos+1].Type == TokenAltitude {
		threshold = scoreFuzzyCap
	}

	if ws := tokenKeywordScore(tokens[pos], keywords, threshold, true); ws > 0 {
		pm.pos = pos + 1
		pm.logScore += math.Log(ws)
		pm.nTok++
		return pm, true
	}

	// Slack: skip up to 3 unrecognized tokens (STT garbage) between clear
	// keywords. Only for non-leading words, so the first keyword of a
	// template stays anchored.
	if allowSlack {
		// A command-boundary keyword here that isn't one of this template's
		// skip words starts a different command; don't skip past it.
		if IsCommandKeyword(text) && !slices.Contains(skipWords, text) {
			return pm, false
		}

		for checkPos := pos + 1; checkPos < len(tokens); checkPos++ {
			// The slack budget counts skipped content words only; fillers
			// pass for free here as everywhere else.
			if countNonFiller(tokens[pos:checkPos]) > 3 {
				break
			}
			checkText := strings.ToLower(tokens[checkPos].Text)

			if IsFillerWord(checkText) {
				continue
			}

			ws := tokenKeywordScore(tokens[checkPos], keywords, 0.8, false)

			// Stop at command boundary keywords unless the keyword is what
			// we're looking for (e.g., stop at "speed" when matching a
			// heading after "left" in "left approach speed 180").
			if IsCommandKeyword(checkText) && ws == 0 {
				break
			}

			if ws > 0 {
				// Tokens skipped as noise earn exactly the baseline: no
				// credit, no penalty in the coverage-adjusted score. Only
				// genuinely matched words earn positive credit, so a
				// template that absorbs garbage never outranks one that
				// explains the same tokens.
				skipped := countNonFiller(tokens[pos:checkPos])
				pm.pos = checkPos + 1
				pm.logScore += math.Log(ws) + float64(skipped)*math.Log(scoreTokenBaseline)
				pm.nTok += 1 + skipped
				return pm, true
			}
		}
	}

	return pm, false
}

// bestKeywordScore returns the best WordScore of text against the keyword
// alternatives, or 0 if none reaches the threshold. ratioGuard suppresses
// fuzzy matches across a >2x length difference.
func bestKeywordScore(text string, keywords []string, threshold float64, ratioGuard bool) float64 {
	best := 0.0
	for _, kw := range keywords {
		if ratioGuard && (len(text) > 2*len(kw) || len(kw) > 2*len(text)) {
			continue
		}
		if ws := WordScore(text, kw); ws >= threshold && ws > best {
			best = ws
		}
	}
	return best
}

// tokenKeywordScore scores a token against keyword alternatives. Number
// tokens are precise data and only match a literal exactly — letter
// similarity between digit strings is meaningless ("13" must not
// fuzzy-match the literal "1").
func tokenKeywordScore(t Token, keywords []string, threshold float64, ratioGuard bool) float64 {
	text := strings.ToLower(t.Text)
	if t.Type != TokenWord {
		if slices.Contains(keywords, text) {
			return 1
		}
		return 0
	}
	return bestKeywordScore(text, keywords, threshold, ratioGuard)
}

// countNonFiller returns how many of the tokens are not filler words.
func countNonFiller(tokens []Token) int {
	n := 0
	for _, t := range tokens {
		if !IsFillerWord(strings.ToLower(t.Text)) {
			n++
		}
	}
	return n
}

// slotMatcher extracts a typed value via its typeParser.
type slotMatcher struct {
	parser typeParser
}

func (m *slotMatcher) isOptional() bool { return false }

func (m *slotMatcher) extend(tokens []Token, ac Aircraft, skipWords []string, allowSlack bool, pm partialMatch) ([]partialMatch, string) {
	pos := pm.pos
	if pos >= len(tokens) {
		// Still call the parser to get its sayAgain hint for incomplete
		// commands (e.g., "climb maintain" with no altitude triggers
		// SAYAGAIN/ALTITUDE).
		_, _, sayAgain := m.parser.parse(tokens, pos, ac)
		return nil, sayAgain
	}

	// Skip filler words, but preserve "to"/"too"/"tu" before a number since
	// parsers may interpret those as the digit "two" (e.g., "heading to
	// niner zero" -> heading 290).
	for pos < len(tokens) {
		text := strings.ToLower(tokens[pos].Text)
		if IsFillerWord(text) {
			if (text == "to" || text == "too" || text == "tu") &&
				pos+1 < len(tokens) && tokens[pos+1].Type == TokenNumber {
				break
			}
			pos++
			continue
		}
		break
	}
	if pos >= len(tokens) {
		_, _, sayAgain := m.parser.parse(tokens, pos, ac)
		return nil, sayAgain
	}

	value, consumed, sayAgain := m.parser.parse(tokens, pos, ac)
	if consumed > 0 {
		ext := pm
		ext.values = append(slices.Clone(pm.values), value)
		ext.pos = pos + consumed
		// Slot values currently score 1.0; slot parsers will supply their
		// own scores once they produce ranked candidates. Exception: the
		// {garbled_word} and {facility_word} slots consume tokens as
		// noise, so they earn only the noise baseline — a template
		// absorbing garbage must not outrank an interpretation that
		// explains the same tokens.
		switch m.parser.(type) {
		case *garbledWordParser, *facilityWordParser:
			ext.logScore += float64(consumed) * math.Log(scoreTokenBaseline)
		case *contactFrequencyParser:
			// The garbled-facility-plus-frequency pattern is template
			// evidence, not explained content: credited as noise so its
			// span cannot out-cover an explicit facility word ("contact
			// tarom 19.1" must read as contact-tower, not as a frequency
			// change, when "tarom" carries tower evidence).
			ext.logScore += float64(consumed) * math.Log(scoreTokenBaseline)
		}
		ext.nTok += consumed
		return []partialMatch{ext}, ""
	}

	// Anchored parsers (e.g., visualApproachParser) must find their
	// keyword at the exact starting position. Skipping non-filler tokens
	// to reach it would let this pattern win over a sibling pattern
	// whose payload includes those tokens (e.g., a charted-visual
	// approach name preceding "visual").
	if ap, ok := m.parser.(anchoredParser); ok && ap.anchored() {
		return nil, sayAgain
	}

	// Slack: skip up to 2 unrecognized tokens to find a value. A
	// command-boundary keyword at the current position that isn't part of
	// this template starts a new command; don't reach past it, which would
	// pull a value belonging to the new command into this one. Exception:
	// the directional modifiers "left"/"right" attach to a turn/heading
	// and are often garbled noise (e.g. "cleared right ended", where
	// "right" is a mangled "direct"), so allow skipping one of them.
	if allowSlack {
		maxSlack := 2
		if posText := strings.ToLower(tokens[pos].Text); IsCommandKeyword(posText) && !slices.Contains(skipWords, posText) {
			if posText == "left" || posText == "right" {
				maxSlack = 1
			} else {
				maxSlack = 0
			}
		}
		for checkPos := pos + 1; checkPos < len(tokens); checkPos++ {
			// The slack budget counts skipped content words only; fillers
			// pass for free here as everywhere else.
			if countNonFiller(tokens[pos:checkPos]) > maxSlack {
				break
			}
			checkText := strings.ToLower(tokens[checkPos].Text)
			if IsFillerWord(checkText) {
				continue
			}
			if IsCommandKeyword(checkText) {
				break
			}

			value, consumed, _ = m.parser.parse(tokens, checkPos, ac)
			if consumed > 0 {
				// As in matchRequiredWord, skipped noise earns the baseline.
				skipped := countNonFiller(tokens[pos:checkPos])
				ext := pm
				ext.values = append(slices.Clone(pm.values), value)
				ext.pos = checkPos + consumed
				ext.logScore += float64(skipped) * math.Log(scoreTokenBaseline)
				ext.nTok += consumed + skipped
				return []partialMatch{ext}, ""
			}
		}
	}

	return nil, sayAgain
}

// optionalGroupMatcher wraps a sequence of matchers that match as a unit
// or not at all. If the group doesn't match, each slot in it contributes a
// nil value.
type optionalGroupMatcher struct {
	matchers []matcher
}

func (m *optionalGroupMatcher) isOptional() bool { return true }

func (m *optionalGroupMatcher) extend(tokens []Token, ac Aircraft, skipWords []string, allowSlack bool, pm partialMatch) ([]partialMatch, string) {
	cur := pm
	matched := true
	for i, inner := range m.matchers {
		innerSlack := allowSlack && i > 0
		exts, _ := inner.extend(tokens, ac, skipWords, innerSlack, cur)
		if len(exts) == 0 {
			matched = false
			break
		}
		cur = exts[0]
	}
	if matched {
		return []partialMatch{cur}, ""
	}

	skip := pm
	skip.values = slices.Clone(pm.values)
	for range countSlots(m.matchers) {
		skip.values = append(skip.values, nil)
	}
	return []partialMatch{skip}, ""
}

// countSlots counts the typed slots in a matcher sequence, recursing into
// optional groups.
func countSlots(matchers []matcher) int {
	n := 0
	for _, m := range matchers {
		switch mm := m.(type) {
		case *slotMatcher:
			n++
		case *optionalGroupMatcher:
			n += countSlots(mm.matchers)
		}
	}
	return n
}

// slotTypes returns the Go types of the typed slots in a matcher sequence,
// in template order. Slots inside optional groups become pointer types
// (nil when the group is absent).
func slotTypes(matchers []matcher) []reflect.Type {
	var types []reflect.Type
	for _, m := range matchers {
		switch mm := m.(type) {
		case *slotMatcher:
			types = append(types, mm.parser.goType())
		case *optionalGroupMatcher:
			for _, inner := range slotTypes(mm.matchers) {
				types = append(types, reflect.PointerTo(inner))
			}
		}
	}
	return types
}

// matchCommands returns the best-scoring template matches at startPos: the
// overall best, plus the best alternative that consumes a different span.
// The decoder explores both, so a long match cannot silently shadow a
// competing segmentation (e.g. one command absorbing tokens that are
// really two commands). excludeCategories contains command categories that
// should not be matched (because a command of that category was already
// matched in this transmission).
func matchCommands(tokens []Token, startPos int, ac Aircraft, isThen bool, excludeCategories map[string]bool) []CommandMatch {
	var bestMatch, altMatch CommandMatch
	var bestPriority, altPriority int
	var bestSayAgain CommandMatch
	var bestSayAgainPriority int

	for _, cmd := range sttCommands {
		match, endPos := tryMatchCommand(tokens, startPos, cmd, ac, isThen)
		consumed := endPos - startPos
		if consumed <= 0 {
			continue
		}
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
		} else if betterMatch(match, cmd.priority, bestMatch, bestPriority) {
			if bestMatch.Consumed > 0 && bestMatch.Consumed != match.Consumed {
				altMatch, altPriority = bestMatch, bestPriority
			}
			bestMatch, bestPriority = match, cmd.priority
		} else if match.Consumed != bestMatch.Consumed && betterMatch(match, cmd.priority, altMatch, altPriority) {
			altMatch, altPriority = match, cmd.priority
		}
	}

	if bestMatch.Consumed > 0 {
		if altMatch.Consumed > 0 {
			return []CommandMatch{bestMatch, altMatch}
		}
		return []CommandMatch{bestMatch}
	}
	if bestSayAgain.Consumed > 0 {
		return []CommandMatch{bestSayAgain}
	}
	return nil
}

// betterMatch ranks competing template matches: registration priority
// first, then coverage-adjusted score, then tokens consumed. The
// coverage-adjusted score credits each explained token by its match
// quality relative to the noise baseline, so among same-priority
// templates a longer parse that explains more of the transcript at good
// quality beats a short exact prefix. Priority remains primary until slot
// parsers report honest per-token consumption; scores alone cannot yet
// distinguish a template that matched noise from one that explained it.
func betterMatch(a CommandMatch, aPriority int, b CommandMatch, bPriority int) bool {
	if b.Consumed == 0 {
		return true
	}
	if aPriority != bPriority {
		return aPriority > bPriority
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Consumed > b.Consumed
}

// tryMatchCommand attempts to match tokens against a single command
// template, tracking a small beam of scored partial matches.
func tryMatchCommand(tokens []Token, startPos int, cmd sttCommand, ac Aircraft, isThen bool) (CommandMatch, int) {
	partials := []partialMatch{{pos: startPos}}

	for i, m := range cmd.matchers {
		// Only allow slack for non-first matchers in the template: the
		// first keyword must match at or near the current position.
		allowSlack := i > 0

		var next []partialMatch
		bestFail := partialMatch{pos: -1}
		var failSayAgain string
		for _, pm := range partials {
			exts, sayAgain := m.extend(tokens, ac, cmd.skipWords, allowSlack, pm)
			if len(exts) == 0 {
				if sayAgain != "" && (bestFail.pos == -1 || pm.logScore > bestFail.logScore) {
					bestFail = pm
					failSayAgain = sayAgain
				}
				continue
			}
			next = append(next, exts...)
		}

		if len(next) == 0 {
			if m.isOptional() {
				continue
			}
			// Return SAYAGAIN if we've matched enough context and this
			// command is marked for clarification on slot failure. When the
			// parser has tokens but can't match them, i > 0 suffices; at
			// end of tokens, require >1 consumed token to avoid false
			// triggers from single stray keywords ("cleared" alone).
			// Commands with sayAgainMinTokens require more tokens consumed
			// (e.g., "at {fix} cleared {approach}" needs the fix to match).
			if failSayAgain != "" && cmd.sayAgainOnFail {
				pos := bestFail.pos
				minTokens := cmd.sayAgainMinTokens
				if minTokens <= 0 {
					minTokens = 1
				}
				enoughContext := pos-startPos >= minTokens && ((pos < len(tokens) && i > 0) || pos-startPos > 1)
				if enoughContext {
					return CommandMatch{
						Command:    "SAYAGAIN/" + failSayAgain,
						Confidence: 0.5,
						Score:      float64(pos-startPos) * scoreSayAgainCredit,
						Consumed:   pos - startPos,
						IsSayAgain: true,
					}, pos
				}
			}
			return CommandMatch{}, startPos
		}

		slices.SortStableFunc(next, func(a, b partialMatch) int {
			if a.logScore != b.logScore {
				if a.logScore > b.logScore {
					return -1
				}
				return 1
			}
			return b.pos - a.pos
		})
		if len(next) > templateBeamWidth {
			next = next[:templateBeamWidth]
		}
		partials = next
	}

	best := partials[0]
	cmdStr := invokeHandler(cmd.handler, best.values, isThen, cmd.thenVariant)

	confidence := 1.0
	if best.nTok > 0 {
		confidence = math.Exp(best.logScore / float64(best.nTok))
	}
	return CommandMatch{
		Command:    cmdStr,
		Name:       cmd.name,
		Confidence: confidence,
		Score:      best.logScore - float64(best.nTok)*math.Log(scoreTokenBaseline),
		Kind:       cmd.kind,
		Consumed:   best.pos - startPos,
		IsThen:     isThen,
	}, best.pos
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
			ptr := reflect.New(paramType.Elem())
			ptr.Elem().Set(reflect.ValueOf(val))
			args[i] = ptr
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

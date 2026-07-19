package stt

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
)

// typeParser is the interface for type-specific value extraction.
type typeParser interface {
	// parse extracts a value from tokens starting at pos.
	// Returns (value, tokensConsumed, sayAgainType).
	// If extraction fails, returns (nil, 0, "TYPE") for SAYAGAIN or (nil, 0, "") to fail silently.
	parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string)

	// goType returns the Go type this parser extracts.
	goType() reflect.Type
}

// altitudeParser extracts altitude values.
// Handles: "flight level 230", "2 3 thousand", "23000", encoded values.
type altitudeParser struct {
	// allowFlightLevel allows values 100-400 that might otherwise be speeds.
	// Set to true when in climb/descend context.
	allowFlightLevel bool
}

func (p *altitudeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *altitudeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "ALTITUDE"
	}

	for i := pos; i < len(tokens) && i < pos+4; i++ {
		// Skip numbers followed by "mile"/"miles" (distances) or "knots" (speeds)
		if i+1 < len(tokens) {
			nextText := strings.ToLower(tokens[i+1].Text)
			if nextText == "mile" || nextText == "miles" || nextText == "knots" {
				continue
			}
		}

		// If a TokenAltitude is nearby (within next 2 tokens), prefer it over
		// a plain number here - the number is likely noise (e.g., "18 3
		// thousand" where "18" is noise and "3 thousand" is the altitude).
		if tokens[i].Type == TokenNumber {
			for j := i + 1; j < len(tokens) && j <= i+2; j++ {
				if tokens[j].Type == TokenAltitude {
					return tokens[j].Value, j - pos + 1, ""
				}
			}
		}

		ctx := NumberContext{Kind: NumAltitude, AC: ac, AllowFlightLevel: p.allowFlightLevel}
		if cands := DecodeNumber(tokens, i, ctx); len(cands) > 0 {
			return cands[0].Value, i - pos + cands[0].Consumed, ""
		}
	}

	return nil, 0, "ALTITUDE"
}

// headingParser extracts heading values (1-360).
type headingParser struct{}

func (p *headingParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *headingParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "HEADING"
	}

	// If the first token is a command keyword (like "contact"), this is not a heading
	// command context - silently fail so we don't trigger SAYAGAIN.
	// E.g., "heading contact boston center" - "contact" is a command keyword.
	if IsCommandKeyword(tokens[pos].Text) {
		return nil, 0, ""
	}

	// Headings should follow immediately (or with at most 1 intervening token for
	// transcription errors). Looking 4 tokens ahead is too permissive and causes
	// false matches like "heading contact boston center 128" being parsed as H128.
	maxLookahead := min(2, len(tokens)-pos)

	for i := pos; i < pos+maxLookahead; i++ {
		// Skip numbers that follow "speed" keyword - those are speed values, not headings.
		if i > pos && strings.ToLower(tokens[i-1].Text) == "speed" {
			continue
		}

		// A number followed by "point" is a frequency, not a heading.
		// E.g., "heading contact boston center 128 point 75".
		if tokens[i].Type == TokenNumber && i+1 < len(tokens) &&
			strings.ToLower(tokens[i+1].Text) == "point" {
			continue
		}

		if cands := DecodeNumber(tokens, i, NumberContext{Kind: NumHeading, AC: ac}); len(cands) > 0 {
			return cands[0].Value, i - pos + cands[0].Consumed, ""
		}
	}

	return nil, 0, "HEADING"
}

// speedParser extracts speed values (100-400 knots).
type speedParser struct{}

func (p *speedParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *speedParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "SPEED"
	}

	hitCmdBoundary := false
	for i := pos; i < len(tokens) && i < pos+4; i++ {
		// Stop at command boundary keywords - these indicate a new command context.
		// For example, in "cross IZEKO at 30 cleared ILS 22 left", when looking for
		// a speed after "at", we should stop at "cleared" rather than finding "22".
		if tokens[i].Type == TokenWord && IsCommandKeyword(tokens[i].Text) {
			hitCmdBoundary = true
			break
		}

		if cands := DecodeNumber(tokens, i, NumberContext{Kind: NumSpeed, AC: ac}); len(cands) > 0 {
			return cands[0].Value, i - pos + cands[0].Consumed, ""
		}
	}

	// If the scan hit a command-keyword boundary without finding any number,
	// silently fail rather than emit SAYAGAIN/SPEED. This avoids spurious
	// clarification requests for "maintain heading" / "maintain best forward
	// speed" patterns where "maintain" is followed by a non-speed command.
	if hitCmdBoundary {
		return nil, 0, ""
	}
	return nil, 0, "SPEED"
}

// machParser extracts mach speed values (60-99).
type machParser struct{}

func (p *machParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *machParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "MACH"
	}

	if cands := DecodeNumber(tokens, pos, NumberContext{Kind: NumMach, AC: ac}); len(cands) > 0 {
		return cands[0].Value, cands[0].Consumed, ""
	}

	return nil, 0, "MACH"
}

// fixParser extracts navigation fix names.
type fixParser struct{}

func (p *fixParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *fixParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) || len(ac.Fixes) == 0 {
		return nil, 0, "FIX"
	}

	// Delegate to existing extractFix function
	fix, _, consumed := extractFix(tokens[pos:], ac.Fixes)
	if consumed > 0 {
		return fix, consumed, ""
	}

	return nil, 0, "FIX"
}

// approachParser extracts approach names.
type approachParser struct {
	allowLAHSO bool
}

func (p *approachParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *approachParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) || len(ac.CandidateApproaches) == 0 {
		return nil, 0, "APPROACH"
	}

	// Delegate to existing extractApproach function
	// Pass the assigned approach so we prefer it when there are ties
	appr, _, consumed := extractApproach(tokens[pos:], ac.CandidateApproaches, ac.AssignedApproach)
	if consumed == 0 {
		return nil, 0, "APPROACH"
	}

	// Handle LAHSO if allowed
	if p.allowLAHSO && pos+consumed < len(tokens) {
		if lahsoRwy, lahsoConsumed := extractLAHSO(tokens[pos+consumed:], ac.LAHSORunways); lahsoConsumed > 0 {
			return appr + "/LAHSO" + lahsoRwy, consumed + lahsoConsumed, ""
		}
	}

	return appr, consumed, ""
}

// anchoredParser is an optional extension of typeParser. Parsers that
// implement it and return true from anchored() require the matcher to find
// their keyword at the exact starting position; the typedMatcher slack
// mechanism is skipped. This prevents the slack from skipping over tokens
// that legitimately belong to a different command's payload (e.g., a
// charted-visual approach name preceding "visual").
type anchoredParser interface {
	typeParser
	anchored() bool
}

type visualApproachParser struct {
	allowLAHSO bool
}

func (p *visualApproachParser) anchored() bool { return true }

func (p *visualApproachParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *visualApproachParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	approachType, _ := extractApproachType(tokens[pos:])
	if approachType != "visual" {
		return nil, 0, ""
	}

	runway, consumed := matchVisualApproach(tokens[pos:], ac.CandidateVisualApproaches)
	if consumed == 0 {
		return nil, 0, "APPROACH"
	}

	if p.allowLAHSO && pos+consumed < len(tokens) {
		if lahsoRwy, lahsoConsumed := extractLAHSO(tokens[pos+consumed:], ac.LAHSORunways); lahsoConsumed > 0 {
			return runway + "/LAHSO" + lahsoRwy, consumed + lahsoConsumed, ""
		}
	}

	return runway, consumed, ""
}

// squawkParser extracts 4-digit squawk codes.
type squawkParser struct{}

func (p *squawkParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *squawkParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "SQUAWK"
	}

	// Delegate to existing extractSquawk function
	code, consumed := extractSquawk(tokens[pos:])
	if consumed > 0 {
		return code, consumed, ""
	}

	return nil, 0, "SQUAWK"
}

// degreesParser extracts turn degrees (1-45) with direction.
type degreesParser struct{}

func (p *degreesParser) goType() reflect.Type {
	// Returns a struct with deg and dir
	return reflect.TypeOf(degreesResult{})
}

type degreesResult struct {
	degrees   int
	direction string
}

func (p *degreesParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "TURN"
	}

	// Delegate to existing extractDegrees function
	deg, dir, consumed := extractDegrees(tokens[pos:])
	if consumed > 0 {
		return degreesResult{degrees: deg, direction: dir}, consumed, ""
	}

	return nil, 0, "TURN"
}

// sidParser extracts SID names.
type sidParser struct{}

func (p *sidParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *sidParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Delegate to existing extractSID function
	consumed := extractSID(tokens[pos:], ac.SID)
	if consumed > 0 {
		return ac.SID, consumed, ""
	}

	return nil, 0, ""
}

// starParser extracts STAR names.
type starParser struct{}

func (p *starParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *starParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Delegate to existing extractSTAR function
	consumed := extractSTAR(tokens[pos:], ac.STAR)
	if consumed > 0 {
		return ac.STAR, consumed, ""
	}

	return nil, 0, ""
}

// rangeParser extracts numbers within a specified range.
type rangeParser struct {
	minVal int
	maxVal int
}

func (p *rangeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *rangeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	t := tokens[pos]
	if t.Type != TokenNumber || t.Value < 0 {
		return nil, 0, ""
	}

	// Spoken digit sequences arrive as separate tokens ("one two miles" →
	// 1 2): extend with following single-digit tokens while the combined
	// value stays in range.
	value, consumed := t.Value, 1
	for pos+consumed < len(tokens) {
		nt := tokens[pos+consumed]
		if nt.Type != TokenNumber || nt.Value < 0 || nt.Value > 9 {
			break
		}
		v := value*10 + nt.Value
		if v < p.minVal || v > p.maxVal {
			break
		}
		value, consumed = v, consumed+1
	}
	if value >= p.minVal && value <= p.maxVal {
		return value, consumed, ""
	}
	return nil, 0, ""
}

// trafficParser extracts traffic advisory components.
type trafficParser struct{}

func (p *trafficParser) goType() reflect.Type {
	return reflect.TypeOf(trafficResult{})
}

type trafficResult struct {
	oclock                      int
	miles                       int
	altitude                    int
	altitudeUnknown             bool
	otherTrafficMaintainsVisual bool
}

func (p *trafficParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Delegate to existing extractTraffic function
	oclock, miles, alt, altUnknown, otherTrafficMaintainsVisual, consumed := extractTraffic(tokens[pos:])
	if consumed > 0 {
		return trafficResult{
			oclock:                      oclock,
			miles:                       miles,
			altitude:                    alt,
			altitudeUnknown:             altUnknown,
			otherTrafficMaintainsVisual: otherTrafficMaintainsVisual,
		}, consumed, ""
	}

	return nil, 0, ""
}

// trafficVisualSepParser recognizes a descriptor-position traffic advisory
// whose only actionable content is "(other aircraft) has you in sight and
// will maintain visual separation". The controller gives a non-o'clock
// position ("off your left", "from the north", etc.); the pilot has no
// command to issue. Pattern is registered with an empty-string handler so
// the simulator treats it as informational chatter.
type trafficVisualSepParser struct{}

func (p *trafficVisualSepParser) goType() reflect.Type { return reflect.TypeOf(true) }

func (p *trafficVisualSepParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	cur, ok := consumeTrafficPositionDescriptor(tokens, pos)
	if !ok {
		return nil, 0, ""
	}

	// Search forward for the visual-sep phrase, allowing intervening filler
	// such as "landing the parallel runway", aircraft type words, the
	// subject pronoun, etc. Bounded so we don't scan unrelated phrasing.
	end := min(cur+14, len(tokens))
	for try := cur; try < end; try++ {
		if next, ok := consumeOtherAircraftMaintainsVisualSeparation(tokens, try); ok {
			return true, next - pos, ""
		}
	}
	return nil, 0, ""
}

// holdParser extracts hold parameters.
type holdParser struct{}

func (p *holdParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *holdParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	// First extract the fix
	fix, _, fixConsumed := extractFix(tokens[pos:], ac.Fixes)
	if fixConsumed == 0 {
		return nil, 0, ""
	}

	// Then try to extract hold parameters
	holdCmd, holdConsumed := extractHoldParams(tokens[pos+fixConsumed:], fix)
	if holdConsumed > 0 {
		return holdCmd, fixConsumed + holdConsumed, ""
	}

	// Fall back to just the fix for "as published" holds
	return "H" + fix, fixConsumed, ""
}

// textParser extracts a single word token. Used for free-form text like facility names.
type textParser struct{}

func (p *textParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *textParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	// Just consume one word token (not a number)
	if tokens[pos].Type == TokenWord {
		return tokens[pos].Text, 1, ""
	}
	return nil, 0, ""
}

// atisLetterParser extracts a NATO phonetic letter for ATIS information.
// It handles exact matches, fuzzy matches against NATO words, and
// multi-word garbles (e.g., "pop up" for "papa").
type atisLetterParser struct{}

func (p *atisLetterParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *atisLetterParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) || tokens[pos].Type != TokenWord {
		return nil, 0, ""
	}

	word := strings.ToLower(tokens[pos].Text)

	// "information" is the ATIS keyword itself, never a NATO letter — it
	// otherwise fuzzy-matches "uniform" via phonetic prefix.
	if word == "information" {
		return nil, 0, ""
	}

	// Exact NATO match.
	if letter, ok := ConvertNATOLetter(word); ok {
		return strings.ToUpper(letter), 1, ""
	}

	// Try combining with the next token for garbled multi-word NATO
	// (e.g., "pop up" → "papa").
	if pos+1 < len(tokens) && tokens[pos+1].Type == TokenWord {
		combined := word + tokens[pos+1].Text
		if letter, ok := ConvertNATOLetter(combined); ok {
			return strings.ToUpper(letter), 2, ""
		}
		// Fuzzy match combined form.
		if letter, ok := fuzzyNATOLetter(combined, 0.8); ok {
			return strings.ToUpper(letter), 2, ""
		}
	}

	// Fuzzy match single word.
	if letter, ok := fuzzyNATOLetter(word, 0.8); ok {
		return strings.ToUpper(letter), 1, ""
	}

	return nil, 0, ""
}

// fuzzyNATOLetter fuzzy-matches a word against all NATO phonetic words
// using FuzzyMatch (Jaro-Winkler + phonetic matching).
// Returns the corresponding letter if a match is found.
func fuzzyNATOLetter(word string, threshold float64) (string, bool) {
	for natoWord, letter := range natoAlphabet {
		if FuzzyMatch(word, natoWord, threshold) {
			return letter, true
		}
	}
	return "", false
}

// positionVetoKeywords are words that rule out reading the surrounding
// tokens as a controller position identification: a facility name never
// contains them, and their presence means a real command is being spoken.
var positionVetoKeywords = map[string]bool{
	"climb": true, "climbing": true, "descend": true, "descending": true,
	"maintain": true, "turn": true, "heading": true, "speed": true,
	"direct": true, "proceed": true, "cleared": true, "expect": true,
	"contact": true, "squawk": true, "ident": true, "cross": true,
	"hold": true, "intercept": true, "fly": true, "reduce": true,
	"increase": true, "expedite": true, "cancel": true, "canceled": true,
	"cancelled": true, "resume": true, "vectors": true, "go": true,
	"standby": true, "left": true, "right": true, "center": true,
	"runway": true, "ils": true, "rnav": true, "localizer": true,
	"visual": true, "approach": true, "departure": true,
}

// facilityWordParser extracts a single word token that could be part of a
// facility name ("New York", "Boston"). Unlike garbled_word it accepts
// words that happen to be command keywords elsewhere ("set" in "Bassett"),
// but never the position-veto keywords that mark a real command.
type facilityWordParser struct{}

func (p *facilityWordParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *facilityWordParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	text := strings.ToLower(tokens[pos].Text)
	if tokens[pos].Type == TokenWord && !positionVetoKeywords[text] &&
		!commandVocabulary[text] && !IsFillerWord(text) && len(confusionTable[text]) == 0 {
		return tokens[pos].Text, 1, ""
	}
	return nil, 0, ""
}

// garbledWordParser extracts a single word token that is NOT a command keyword.
// Used for matching garbled facility names without accidentally consuming command keywords.
// aircraftTypeParser consumes an aircraft-type description in a
// follow-the-traffic advisory: a manufacturer word and/or type digits
// ("boeing triple seven", "bus three three" for a garbled Airbus A330,
// "heavy 777"). Requires a manufacturer word, a known type number, or a
// "triple"/"double" digit pattern so it can't absorb arbitrary numbers.
type aircraftTypeParser struct{}

var aircraftMakerWords = []string{"boeing", "airbus", "bus", "embraer", "cessna", "gulfstream", "md"}

func (p *aircraftTypeParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *aircraftTypeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	consumed := 0
	sawEvidence := false
	for pos+consumed < len(tokens) {
		t := tokens[pos+consumed]
		if t.Type == TokenNumber {
			if isAircraftTypeNumber(t.Value) {
				sawEvidence = true
				consumed++
				continue
			}
			// Bare digits continue a type read off digit-by-digit ("bus
			// three three", possibly tokenizer-merged to 33), but only once
			// a maker word anchored the read.
			if sawEvidence && t.Value <= 99 {
				consumed++
				continue
			}
			break
		}
		text := strings.ToLower(t.Text)
		if slices.Contains(aircraftMakerWords, text) {
			sawEvidence = true
			consumed++
			continue
		}
		// "triple seven" / "double seven" digit patterns.
		if (text == "triple" || text == "double") && pos+consumed+1 < len(tokens) &&
			tokens[pos+consumed+1].Type == TokenNumber && tokens[pos+consumed+1].Value <= 9 {
			sawEvidence = true
			consumed += 2
			continue
		}
		break
	}
	if !sawEvidence {
		return nil, 0, ""
	}
	return "", consumed, ""
}

type garbledWordParser struct{}

func (p *garbledWordParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *garbledWordParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	// Match a word token that is NOT a command keyword. A word with a
	// confusion-table reading is a garbled command word ("club" for
	// "climb"), not absorbable noise.
	if tokens[pos].Type == TokenWord && !IsCommandKeyword(tokens[pos].Text) &&
		len(confusionTable[strings.ToLower(tokens[pos].Text)]) == 0 {
		return tokens[pos].Text, 1, ""
	}
	return nil, 0, ""
}

// speedUntilParser extracts a speed "until" specification (fix, DME, or mile final).
type speedUntilParser struct{}

func (p *speedUntilParser) goType() reflect.Type {
	return reflect.TypeOf(speedUntilResult{})
}

func (p *speedUntilParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	result, consumed := extractSpeedUntil(tokens[pos:], ac)
	if consumed > 0 {
		return result, consumed, ""
	}

	return nil, 0, ""
}

// dmeParser extracts a DME distance like "10 DME" or "10 D M E".
type dmeParser struct{}

func (p *dmeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *dmeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	dist, consumed := extractDME(tokens[pos:])
	if consumed > 0 {
		return dist, consumed, ""
	}
	return nil, 0, ""
}

// standaloneAltitudeParser only matches TokenAltitude tokens (created by "thousand").
// This is stricter than altitudeParser - it won't match plain numbers.
type standaloneAltitudeParser struct{}

func (p *standaloneAltitudeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *standaloneAltitudeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Only match TokenAltitude (created by "thousand" etc.), not plain numbers
	if tokens[pos].Type == TokenAltitude {
		return tokens[pos].Value, 1, ""
	}

	return nil, 0, ""
}

// contactFrequencyParser matches the pattern for "contact <facility> <frequency>".
// It looks for tokens ending with a frequency pattern: 2-3 digit number + "point" + 1-2 digit number.
// This handles garbled facility names like "contact for ersena one two seven point zero".
type contactFrequencyParser struct{}

func (p *contactFrequencyParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *contactFrequencyParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Look for the frequency pattern: number (10-9999) + "point" + number (0-99)
	// We accept up to 9999 because spoken digit-by-digit frequencies like "one two four"
	// can be combined with preceding garbled words (e.g., "seven one two four" where
	// "seven" is mis-heard "center") resulting in numbers like 7124.
	// Scan up to 10 tokens ahead looking for this pattern
	maxLookahead := min(10, len(tokens)-pos)

	for i := pos; i < pos+maxLookahead-2; i++ {
		t := tokens[i]

		// Check if this token is a number in valid frequency range (10-9999)
		if t.Type != TokenNumber || t.Value < 10 || t.Value > 9999 {
			continue
		}

		// Check if next token is "point"
		if i+1 >= len(tokens) {
			continue
		}
		pointToken := tokens[i+1]
		if pointToken.Type != TokenWord || strings.ToLower(pointToken.Text) != "point" {
			continue
		}

		// Check if token after "point" is a number (0-99)
		if i+2 >= len(tokens) {
			continue
		}
		decimalToken := tokens[i+2]
		if decimalToken.Type != TokenNumber || decimalToken.Value < 0 || decimalToken.Value > 99 {
			continue
		}

		// Found a valid frequency pattern - consume all tokens up to and including it
		consumed := (i + 3) - pos
		return "frequency", consumed, ""
	}

	return nil, 0, ""
}

// frequencyValueParser extracts an aviation VHF frequency value
// from the pattern: NUMBER "point" NUMBER. Returns av.Frequency
// (integer with ×1000 scaling, matching av.NewFrequency).
type frequencyValueParser struct{}

func (p *frequencyValueParser) goType() reflect.Type { return reflect.TypeOf(av.Frequency(0)) }

func (p *frequencyValueParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos+2 >= len(tokens) {
		return nil, 0, ""
	}

	whole := tokens[pos]
	if whole.Type != TokenNumber || whole.Value < 100 || whole.Value > 999 {
		return nil, 0, ""
	}

	point := tokens[pos+1]
	if point.Type != TokenWord || strings.ToLower(point.Text) != "point" {
		return nil, 0, ""
	}

	dec := tokens[pos+2]
	if dec.Type != TokenNumber || dec.Value < 0 || dec.Value > 99 {
		return nil, 0, ""
	}

	// Decimal scaling is based on the number of digits spoken, not the
	// numeric magnitude. "point nine" (text="9") → 0.9 (×100).
	// "point four five" (text="45") → 0.45 (×10). Tokenize preserves
	// original digit count in Text (see parseDigitSequence).
	scale := 100
	if len(dec.Text) >= 2 {
		scale = 10
	}
	khz := whole.Value*1000 + dec.Value*scale
	return av.Frequency(khz), 3, ""
}

// compassDirParser extracts a cardinal/ordinal compass direction.
// Matches: north, south, east, west, northeast, northwest, southeast, southwest.
// Returns the short abbreviation (N, S, E, W, NE, NW, SE, SW) as a string.
type compassDirParser struct{}

func (p *compassDirParser) goType() reflect.Type { return reflect.TypeOf("") }

var compassDirections = map[string]string{
	"north": "N", "south": "S", "east": "E", "west": "W",
	"northeast": "NE", "northwest": "NW", "southeast": "SE", "southwest": "SW",
}

func (p *compassDirParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	text := strings.ToLower(tokens[pos].Text)
	if short, ok := compassDirections[text]; ok {
		return short, 1, ""
	}
	// Fuzzy match
	for long, short := range compassDirections {
		if len(text) >= 3 && FuzzyMatch(text, long, 0.80) {
			return short, 1, ""
		}
	}
	return nil, 0, ""
}

// getTypeParser returns the appropriate parser for a type identifier.
func getTypeParser(typeID string) typeParser {
	switch typeID {
	case "altitude":
		return &altitudeParser{}
	case "altitude_fl":
		return &altitudeParser{allowFlightLevel: true}
	case "heading":
		return &headingParser{}
	case "speed":
		return &speedParser{}
	case "mach":
		return &machParser{}
	case "fix":
		return &fixParser{}
	case "approach":
		return &approachParser{}
	case "approach_lahso":
		return &approachParser{allowLAHSO: true}
	case "visual_approach_lahso":
		return &visualApproachParser{allowLAHSO: true}
	case "squawk":
		return &squawkParser{}
	case "degrees":
		return &degreesParser{}
	case "sid":
		return &sidParser{}
	case "star":
		return &starParser{}
	case "traffic":
		return &trafficParser{}
	case "traffic_visual_sep":
		return &trafficVisualSepParser{}
	case "hold":
		return &holdParser{}
	case "text":
		return &textParser{}
	case "atis_letter":
		return &atisLetterParser{}
	case "garbled_word":
		return &garbledWordParser{}
	case "aircraft_type":
		return &aircraftTypeParser{}
	case "facility_word":
		return &facilityWordParser{}
	case "speed_until":
		return &speedUntilParser{}
	case "dme":
		return &dmeParser{}
	case "contact_frequency":
		return &contactFrequencyParser{}
	case "frequency_value":
		return &frequencyValueParser{}
	case "standalone_altitude":
		return &standaloneAltitudeParser{}
	case "compass_dir":
		return &compassDirParser{}
	default:
		// Check for range pattern: num:min-max
		if strings.HasPrefix(typeID, "num:") {
			parts := strings.Split(typeID[4:], "-")
			if len(parts) == 2 {
				minVal, err1 := strconv.Atoi(parts[0])
				maxVal, err2 := strconv.Atoi(parts[1])
				if err1 == nil && err2 == nil {
					return &rangeParser{minVal: minVal, maxVal: maxVal}
				}
			}
		}
		return nil
	}
}

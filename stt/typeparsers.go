package stt

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// typeParser is the interface for type-specific value extraction.
type typeParser interface {
	// identifier returns the parser name (e.g., "altitude", "heading").
	identifier() string

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

func (p *altitudeParser) identifier() string {
	return "altitude"
}

func (p *altitudeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *altitudeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "ALTITUDE"
	}

	for i := pos; i < len(tokens) && i < pos+4; i++ {
		t := tokens[i]

		// Skip numbers followed by "mile" or "miles" - those are distances
		if i+1 < len(tokens) {
			nextText := strings.ToLower(tokens[i+1].Text)
			if nextText == "mile" || nextText == "miles" {
				continue
			}
		}

		if t.Type == TokenAltitude {
			return t.Value, i - pos + 1, ""
		}

		if t.Type == TokenNumber {
			// If a TokenAltitude is nearby (within next 2 tokens), prefer it over
			// this number - the number is likely noise (e.g., "18 3 thousand" where
			// "18" is noise and "3 thousand" is the actual altitude).
			for j := i + 1; j < len(tokens) && j <= i+2; j++ {
				if tokens[j].Type == TokenAltitude {
					return tokens[j].Value, j - pos + 1, ""
				}
			}

			// Standard altitude encoding (10-99 means 1000-9900 ft, 100+ for higher)
			// Exclude speed range 100-400 unless allowFlightLevel is set
			if t.Value >= 10 && t.Value <= 600 {
				if t.Value < 100 || t.Value > 400 || p.allowFlightLevel {
					alt := t.Value

					// Context-aware altitude correction: if the parsed altitude is below
					// the aircraft's current altitude and multiplying by 10 gives a valid
					// altitude, assume the user meant the higher value.
					// E.g., aircraft at 7000 ft, "16" → 160 (16,000 ft), not 16 (1,600 ft)
					currentAlt := ac.Altitude / 100 // Convert feet to hundreds
					if alt < currentAlt && alt*10 <= 600 {
						logLocalStt("  altitude correction: %d -> %d (aircraft at %d ft)", alt, alt*10, ac.Altitude)
						alt = alt * 10
					}

					return alt, i - pos + 1, ""
				}
			}

			// Large number in raw feet
			if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
				return t.Value / 100, i - pos + 1, ""
			}

			// Handle "repeated altitude" pattern: "one three thirteen" → 1313
			// where the altitude is stated twice. Pattern: 4-digit ABAB.
			if t.Value >= 1010 && t.Value <= 6060 {
				firstHalf := t.Value / 100
				secondHalf := t.Value % 100
				if firstHalf == secondHalf && firstHalf >= 10 && firstHalf <= 60 {
					logLocalStt("  repeated altitude pattern: %d -> %d", t.Value, firstHalf*10)
					return firstHalf * 10, i - pos + 1, ""
				}
			}

			// Single digit 1-9 means thousands
			if t.Value >= 1 && t.Value <= 9 {
				return t.Value * 10, i - pos + 1, ""
			}
		}
	}

	return nil, 0, "ALTITUDE"
}

// headingParser extracts heading values (1-360).
type headingParser struct{}

func (p *headingParser) identifier() string {
	return "heading"
}

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
		t := tokens[i]

		// Skip numbers that follow "speed" keyword - those are speed values, not headings.
		if i > pos && strings.ToLower(tokens[i-1].Text) == "speed" {
			continue
		}

		// Check if this number is followed by "point" - that indicates a frequency, not a heading.
		// E.g., "heading contact boston center 128 point 75" - the 128.75 is a frequency.
		if t.Type == TokenNumber && i+1 < len(tokens) {
			nextText := strings.ToLower(tokens[i+1].Text)
			if nextText == "point" {
				continue
			}
		}

		// Handle 4-digit values where first 3 digits form valid heading (e.g., 1507 → 150)
		if t.Type == TokenNumber && t.Value > 360 && t.Value < 10000 {
			hdg := t.Value / 10
			if hdg >= 1 && hdg <= 360 {
				return hdg, i - pos + 1, ""
			}
		}

		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			hdg := t.Value

			// Handle leading zero and dropped trailing zero patterns
			text := t.Text
			hasLeadingZero := len(text) > 0 && text[0] == '0'

			if hasLeadingZero {
				// Rewrite single-digit headings to add trailing zero, with exception:
				// For 3-digit inputs like "005" that are multiples of 5, leave as-is
				// (e.g., "001" -> 010 likely transcription error, but "005" is valid heading 5).
				// For 2-digit like "05", always multiply by 10 (heading 050 - common shorthand).
				if hdg < 10 && (len(text) < 3 || hdg%5 != 0) {
					hdg *= 10
				}
			} else if len(text) == 2 && hdg >= 10 && hdg <= 36 && hdg%10 != 0 {
				hdg *= 10
			} else if hdg < 10 {
				hdg *= 10
			}

			return hdg, i - pos + 1, ""
		}
	}

	return nil, 0, "HEADING"
}

// speedParser extracts speed values (100-400 knots).
type speedParser struct{}

func (p *speedParser) identifier() string {
	return "speed"
}

func (p *speedParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *speedParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, "SPEED"
	}

	for i := pos; i < len(tokens) && i < pos+4; i++ {
		t := tokens[i]

		// Stop at command boundary keywords - these indicate a new command context.
		// For example, in "cross IZEKO at 30 cleared ILS 22 left", when looking for
		// a speed after "at", we should stop at "cleared" rather than finding "22".
		if t.Type == TokenWord && IsCommandKeyword(t.Text) {
			break
		}

		if t.Type == TokenNumber {
			// Normal speed range
			// Round down to nearest 10 - ATC speeds are always multiples of 10.
			if t.Value >= 100 && t.Value <= 400 {
				rounded := (t.Value / 10) * 10
				return rounded, i - pos + 1, ""
			}

			// 4-digit starting with 2 and ending in 0: the leading "2"
			// is likely "to" misheard as "two" (e.g., "speed to one seven
			// zero" → "speed 2170" → 170).
			if t.Value >= 2000 && t.Value < 3000 && t.Value%10 == 0 {
				last3 := t.Value % 1000
				if last3 >= 100 && last3 <= 400 {
					return last3, i - pos + 1, ""
				}
			}

			// 4-digit with extra digit (e.g., 1909 → 190)
			if t.Value > 400 {
				corrected := t.Value / 10
				if corrected >= 100 && corrected <= 400 {
					return corrected, i - pos + 1, ""
				}
			}

			// Handle 2-digit speeds followed by a trailing zero token
			// STT sometimes splits "two one zero" into "21" "0"
			if t.Value >= 10 && t.Value <= 40 && i+1 < len(tokens) {
				next := tokens[i+1]
				if next.Type == TokenNumber && next.Value == 0 {
					combined := t.Value * 10
					if combined >= 100 && combined <= 400 {
						return combined, i - pos + 2, ""
					}
				}
			}
		}
	}

	return nil, 0, "SPEED"
}

// fixParser extracts navigation fix names.
type fixParser struct{}

func (p *fixParser) identifier() string {
	return "fix"
}

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

func (p *approachParser) identifier() string {
	return "approach"
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

// squawkParser extracts 4-digit squawk codes.
type squawkParser struct{}

func (p *squawkParser) identifier() string {
	return "squawk"
}

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

func (p *degreesParser) identifier() string {
	return "degrees"
}

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

func (p *sidParser) identifier() string {
	return "sid"
}

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

func (p *starParser) identifier() string {
	return "star"
}

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

func (p *rangeParser) identifier() string {
	return fmt.Sprintf("num:%d-%d", p.minVal, p.maxVal)
}

func (p *rangeParser) goType() reflect.Type {
	return reflect.TypeOf(0)
}

func (p *rangeParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	t := tokens[pos]
	if t.Type == TokenNumber && t.Value >= p.minVal && t.Value <= p.maxVal {
		return t.Value, 1, ""
	}

	return nil, 0, ""
}

// trafficParser extracts traffic advisory components.
type trafficParser struct{}

func (p *trafficParser) identifier() string {
	return "traffic"
}

func (p *trafficParser) goType() reflect.Type {
	return reflect.TypeOf(trafficResult{})
}

type trafficResult struct {
	oclock   int
	miles    int
	altitude int
}

func (p *trafficParser) parse(tokens []Token, pos int, ac Aircraft) (any, int, string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}

	// Delegate to existing extractTraffic function
	oclock, miles, alt, consumed := extractTraffic(tokens[pos:])
	if consumed > 0 {
		return trafficResult{oclock: oclock, miles: miles, altitude: alt}, consumed, ""
	}

	return nil, 0, ""
}

// holdParser extracts hold parameters.
type holdParser struct{}

func (p *holdParser) identifier() string {
	return "hold"
}

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

func (p *textParser) identifier() string {
	return "text"
}

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

func (p *atisLetterParser) identifier() string {
	return "atis_letter"
}

func (p *atisLetterParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *atisLetterParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) || tokens[pos].Type != TokenWord {
		return nil, 0, ""
	}

	word := strings.ToLower(tokens[pos].Text)

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

// garbledWordParser extracts a single word token that is NOT a command keyword.
// Used for matching garbled facility names without accidentally consuming command keywords.
type garbledWordParser struct{}

func (p *garbledWordParser) identifier() string {
	return "garbled_word"
}

func (p *garbledWordParser) goType() reflect.Type {
	return reflect.TypeOf("")
}

func (p *garbledWordParser) parse(tokens []Token, pos int, ac Aircraft) (value any, consumed int, sayAgain string) {
	if pos >= len(tokens) {
		return nil, 0, ""
	}
	// Match a word token that is NOT a command keyword
	if tokens[pos].Type == TokenWord && !IsCommandKeyword(tokens[pos].Text) {
		return tokens[pos].Text, 1, ""
	}
	return nil, 0, ""
}

// speedUntilParser extracts a speed "until" specification (fix, DME, or mile final).
type speedUntilParser struct{}

func (p *speedUntilParser) identifier() string {
	return "speed_until"
}

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

// standaloneAltitudeParser only matches TokenAltitude tokens (created by "thousand").
// This is stricter than altitudeParser - it won't match plain numbers.
type standaloneAltitudeParser struct{}

func (p *standaloneAltitudeParser) identifier() string {
	return "standalone_altitude"
}

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

func (p *contactFrequencyParser) identifier() string {
	return "contact_frequency"
}

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

// getTypeParser returns the appropriate parser for a type identifier.
func getTypeParser(typeID string) typeParser {
	switch typeID {
	case "altitude":
		return &altitudeParser{}
	case "heading":
		return &headingParser{}
	case "speed":
		return &speedParser{}
	case "fix":
		return &fixParser{}
	case "approach":
		return &approachParser{}
	case "approach_lahso":
		return &approachParser{allowLAHSO: true}
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
	case "hold":
		return &holdParser{}
	case "text":
		return &textParser{}
	case "atis_letter":
		return &atisLetterParser{}
	case "garbled_word":
		return &garbledWordParser{}
	case "speed_until":
		return &speedUntilParser{}
	case "contact_frequency":
		return &contactFrequencyParser{}
	case "standalone_altitude":
		return &standaloneAltitudeParser{}
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

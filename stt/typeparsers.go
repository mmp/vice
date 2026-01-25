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
			// Check for garbled altitude pattern
			if i+1 < len(tokens) && tokens[i+1].Type == TokenNumber {
				next := tokens[i+1]
				if t.Value < 100 && next.Value >= 1000 && next.Value <= 60000 && next.Value%100 == 0 {
					return next.Value / 100, i - pos + 2, ""
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

			// Handle STT transcribing "ten thousand" as "1000" or "1,000"
			// This specific error is common because "ten" sounds like "1" to STT.
			// Only apply to 1000 specifically - other values like 3000, 8000 are
			// more likely to mean actual 3,000 or 8,000 feet.
			if t.Value == 1000 {
				return 100, i - pos + 1, "" // 10,000 feet
			}

			// Large number in raw feet
			if t.Value >= 1000 && t.Value <= 60000 && t.Value%100 == 0 {
				return t.Value / 100, i - pos + 1, ""
			}

			// STT adds extra zeros
			if t.Value >= 100000 && t.Value <= 6000000 && t.Value%10000 == 0 {
				corrected := t.Value / 100
				if corrected >= 1000 && corrected <= 60000 {
					return corrected / 100, i - pos + 1, ""
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

	for i := pos; i < len(tokens) && i < pos+4; i++ {
		t := tokens[i]

		// Handle 4-digit values where first 3 digits form valid heading
		if t.Type == TokenNumber && t.Value > 360 && t.Value < 10000 {
			hdg := t.Value / 10
			if hdg >= 1 && hdg <= 360 {
				return hdg, i - pos + 1, ""
			}
		}

		if t.Type == TokenNumber && t.Value >= 1 && t.Value <= 360 {
			hdg := t.Value

			// Check for "N to M" pattern (garbled)
			if hdg < 100 && i+2 < len(tokens) {
				nextText := strings.ToLower(tokens[i+1].Text)
				if nextText == "to" && tokens[i+2].Type == TokenNumber {
					largerHdg := tokens[i+2].Value
					if largerHdg >= 100 && largerHdg <= 360 {
						return largerHdg, i - pos + 3, ""
					}
				}
			}

			// Handle leading zero and dropped trailing zero patterns
			text := t.Text
			hasLeadingZero := len(text) > 0 && text[0] == '0'

			if hasLeadingZero {
				if hdg < 10 {
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

		if t.Type == TokenNumber {
			// Normal speed range
			if t.Value >= 100 && t.Value <= 400 {
				return t.Value, i - pos + 1, ""
			}

			// 4-digit with extra digit
			if t.Value > 400 {
				corrected := t.Value / 10
				if corrected >= 100 && corrected <= 400 {
					return corrected, i - pos + 1, ""
				}
			}

			// 2-digit with missing leading digit
			if t.Value >= 10 && t.Value < 100 {
				// Try prepending "2"
				corrected := 200 + t.Value
				if corrected >= 200 && corrected <= 290 {
					return corrected, i - pos + 1, ""
				}
				// Try prepending "1"
				corrected = 100 + t.Value
				if corrected >= 140 && corrected <= 190 {
					return corrected, i - pos + 1, ""
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
	appr, _, consumed := extractApproach(tokens[pos:], ac.CandidateApproaches)
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

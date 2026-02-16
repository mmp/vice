// eram/parsetypes.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"reflect"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// typeParser defines the interface for parsing and validating typed parameters.
type typeParser interface {
	// Identifier returns the token identifier for this handler (e.g., "ALL_TEXT", "FLID").
	Identifier() string

	// Parse attempts to extract this type from the input text.
	// The text parameter contains the unparsed portion starting at the current position.
	// Parsers should consume from the start of text (no automatic space skipping).
	// Returns:
	//   value: the extracted value
	//   remaining: the unconsumed portion of text
	//   matched: true if this looks like this type
	//   err: non-nil if matched but invalid
	Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (value any, remaining string, matched bool, err error)

	// GoType returns the Go type this handler produces
	GoType() reflect.Type

	ConsumesClick() bool
}

// typeParsers is a slice of typeParsers ordered by priority (earlier = higher priority).
var typeParsers = []typeParser{
	&numberParser{id: "#", digits: 1},
	&numberParser{id: "##", digits: 2},

	// Track/Flight identification
	&flidParser{},
	&slewParser{},

	// QS HSF (heading / speed-mach / free-text)
	&hsfTextParser{},
	&hsfSpeedParser{},
	&hsfHeadingParser{},

	// Altitude parsers
	&eramAltAParser{},
	&eramAltIParser{},

	// Controller/Sector
	&sectorIDParser{},

	// Navigation
	&fixParser{},
	&crrLocParser{},
	&crrLabelParser{},
	&locSymParser{}, // Matches location symbol 'w' in text

	// Generic type parsers
	&numberParser{id: "NUM"},

	// Text handlers
	&fieldParser{},
	&allTextParser{},

	// Mouse click consumers
	&posParser{},

	// QU minutes
	&minutesParser{},
}

// typeParserMap provides O(1) lookup of parsers by identifier.
// typeParserPriority maps identifier to priority (position in typeParsers).
// Both are built at package initialization time via buildTypeParserMaps().
var typeParserMap, typeParserPriority = buildTypeParserMaps()

func buildTypeParserMaps() (map[string]typeParser, map[string]int) {
	handlerMap := make(map[string]typeParser, len(typeParsers))
	priorityMap := make(map[string]int, len(typeParsers))
	for i, h := range typeParsers {
		id := h.Identifier()
		if _, exists := handlerMap[id]; exists {
			panic("duplicate type parser identifier: " + id)
		}
		handlerMap[id] = h
		priorityMap[id] = i
	}
	return handlerMap, priorityMap
}

// getTypeParser returns the typeParser for the given identifier, or nil if not found.
func getTypeParser(id string) typeParser {
	return typeParserMap[id]
}

// getTypePriority returns the priority (position in typeParsers) for the given identifier.
// Lower values indicate higher priority. Returns -1 if not found.
func getTypePriority(id string) int {
	if p, ok := typeParserPriority[id]; ok {
		return p
	}
	return -1
}

func isAlpha(ch byte) bool { return ch >= 'A' && ch <= 'Z' }
func isNum(ch byte) bool   { return ch >= '0' && ch <= '9' }
func isAlphaNum(ch byte) bool {
	return isAlpha(ch) || isNum(ch)
}

// fpSpecType is the Go type for FlightPlanSpecifier.
var fpSpecType = reflect.TypeOf(sim.FlightPlanSpecifier{})

///////////////////////////////////////////////////////////////////////////
// Parser implementations

// flidParser parses ERAM Flight ID - looks up tracks by CID, ACID, beacon code, or list index
type flidParser struct{}

func (h *flidParser) Identifier() string { return "FLID" }

func (h *flidParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// Try to find track by FLID (CID, ACID, beacon, or list index)
	trk, ok := ctx.Client.State.GetTrackByFLID(field)
	if ok {
		return trk, remaining, true, nil
	}

	return nil, text, false, nil
}

func (h *flidParser) GoType() reflect.Type { return reflect.TypeOf((*sim.Track)(nil)) }
func (h *flidParser) ConsumesClick() bool  { return false }

// slewParser matches a clicked track (mouse click on scope)
type slewParser struct{}

func (h *slewParser) Identifier() string { return "SLEW" }

func (h *slewParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if input.clickedTrack == nil {
		return nil, text, false, nil
	}
	if strings.TrimSpace(text) != "" {
		return nil, text, false, nil
	}
	return input.clickedTrack, "", true, nil
}

func (h *slewParser) GoType() reflect.Type { return reflect.TypeOf((*sim.Track)(nil)) }
func (h *slewParser) ConsumesClick() bool  { return true }

// eramAltAParser parses assigned altitude (3 digits, e.g., "350" for FL350)
type eramAltAParser struct{}

func (h *eramAltAParser) Identifier() string { return "ERAM_ALT_A" }

func (h *eramAltAParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if len(field) != 3 {
		return nil, text, false, nil
	}

	// All 3 characters must be digits
	for i := 0; i < 3; i++ {
		if !isNum(field[i]) {
			return nil, text, false, nil
		}
	}

	alt, err := strconv.Atoi(field)
	if err != nil {
		return nil, text, false, nil
	}

	// Return altitude in feet (multiply by 100)
	return alt * 100, remaining, true, nil
}

func (h *eramAltAParser) GoType() reflect.Type { return reflect.TypeOf(0) }
func (h *eramAltAParser) ConsumesClick() bool  { return false }

// eramAltIParser parses interim altitude with optional P/L prefix (e.g., "230", "P230", "L180")
type eramAltIParser struct{}

func (h *eramAltIParser) Identifier() string { return "ERAM_ALT_I" }

func (h *eramAltIParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	altStr := field
	var interimType string

	// Check for P or L prefix
	if len(field) > 0 && (field[0] == 'P' || field[0] == 'L') {
		interimType = string(field[0])
		altStr = field[1:]
	}

	if len(altStr) != 3 {
		return nil, text, false, nil
	}

	// All 3 characters must be digits
	for i := 0; i < 3; i++ {
		if !isNum(altStr[i]) {
			return nil, text, false, nil
		}
	}

	alt, err := strconv.Atoi(altStr)
	if err != nil {
		return nil, text, false, nil
	}

	// Return InterimAltitude struct with altitude in feet and optional type
	return InterimAltitude{Altitude: alt * 100, Type: interimType}, remaining, true, nil
}

func (h *eramAltIParser) GoType() reflect.Type { return reflect.TypeOf(InterimAltitude{}) }
func (h *eramAltIParser) ConsumesClick() bool  { return false }

// InterimAltitude holds an interim altitude value with optional type (P for pilot, L for local)
type InterimAltitude struct {
	Altitude int    // Altitude in feet
	Type     string // "" for none, "P" for pilot, "L" for local
}

// sectorIDParser parses ERAM sector identifiers (e.g., "1A", "2B", "15")
type sectorIDParser struct{}

func (h *sectorIDParser) Identifier() string { return "SECTOR_ID" }

func (h *sectorIDParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// Sector ID formats:
	// - Single digit + letter: "1A", "2B" (most common)
	// - Two digits: "15", "20" (for centers)
	// - Facility + sector: "B20", "N2K"
	// - Single letter (rare, for single-character shortcuts)

	if len(field) == 2 && isNum(field[0]) && isAlpha(field[1]) {
		// Standard format: digit + letter
		return field, remaining, true, nil
	}
	if len(field) == 2 && isNum(field[0]) && isNum(field[1]) {
		// Two-digit sector
		return field, remaining, true, nil
	}
	if len(field) == 3 && isAlpha(field[0]) && isAlphaNum(field[1]) && isAlphaNum(field[2]) &&
		(isNum(field[1]) || isNum(field[2])) {
		// Facility + sector (e.g., B20, N2K)
		return field, remaining, true, nil
	}
	if len(field) == 1 && isAlpha(field[0]) {
		// Single letter shortcut
		return field, remaining, true, nil
	}

	return nil, text, false, nil
}

func (h *sectorIDParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *sectorIDParser) ConsumesClick() bool  { return false }

// fixParser parses navigation fix names and returns the fix name (validation happens in handler)
type fixParser struct{}

func (h *fixParser) Identifier() string { return "FIX" }

func (h *fixParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !isAlpha(field[0]) {
		return nil, text, false, nil
	}

	// Fix names are typically 2-5 characters, all alphanumeric starting with a letter
	if len(field) < 2 || len(field) > 5 {
		return nil, text, false, nil
	}

	// Validate all characters are alphanumeric
	for i := 1; i < len(field); i++ {
		if !isAlpha(field[i]) && !isNum(field[i]) {
			return nil, text, false, nil
		}
	}

	return field, remaining, true, nil
}

func (h *fixParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *fixParser) ConsumesClick() bool  { return false }

// numberParser parses integer numbers.
type numberParser struct {
	id     string
	digits int // 0 for variable length
}

func (h *numberParser) Identifier() string { return h.id }

func (h *numberParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	var num, remainder string
	if h.digits != 0 {
		if len(text) < h.digits {
			return nil, text, false, nil
		}
		num, remainder = text[:h.digits], text[h.digits:]
		// Verify all digits
		for i := 0; i < h.digits; i++ {
			if !isNum(num[i]) {
				return nil, text, false, nil
			}
		}
	} else {
		num, remainder, _ = util.CutFunc(text, func(ch rune) bool { return !isNum(byte(ch)) })
	}

	if len(num) == 0 {
		return nil, text, false, nil
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return nil, text, false, nil
	}

	return n, remainder, true, nil
}

func (h *numberParser) GoType() reflect.Type { return reflect.TypeOf(0) }
func (h *numberParser) ConsumesClick() bool  { return false }

// fieldParser extracts a single space-delimited field (token).
type fieldParser struct{}

func (h *fieldParser) Identifier() string { return "FIELD" }

func (h *fieldParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}
	return field, remaining, true, nil
}

func (h *fieldParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *fieldParser) ConsumesClick() bool  { return false }

// allTextParser captures all remaining text from the current position.
// Unlike strings.Cut which stops at spaces, this captures everything.
type allTextParser struct{}

func (h *allTextParser) Identifier() string { return "ALL_TEXT" }

func (h *allTextParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" {
		return nil, text, false, nil
	}
	return text, "", true, nil
}

func (h *allTextParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *allTextParser) ConsumesClick() bool  { return false }

// posParser handles click position as lat/long (for commands that need position without a track)
type posParser struct{}

func (h *posParser) Identifier() string { return "POS" }

func (h *posParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Only match clicks on empty space, not on tracks. Use SLEW for track clicks.
	if !input.hasClick || input.clickedTrack != nil || len(input.mousePositions) == 0 {
		return nil, text, false, nil
	}

	var p [2]float32
	if input.posIsLatLong {
		p = input.mousePositions[0]
	} else {
		p = input.transforms.LatLongFromWindowP(input.mousePositions[0])
	}
	return p, text, true, nil
}

func (h *posParser) GoType() reflect.Type { return reflect.TypeOf((*[2]float32)(nil)).Elem() }
func (h *posParser) ConsumesClick() bool  { return true }

///////////////////////////////////////////////////////////////////////////
// Additional ERAM-specific parsers

// acidParser parses aircraft IDs (callsigns)
type acidParser struct{}

func (h *acidParser) Identifier() string { return "ACID" }

func (h *acidParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" || !isAlpha(text[0]) {
		return nil, text, false, nil
	}
	i := 1
	for i < len(text) && i < 7 /* 7 chars maximum */ && (isAlpha(text[i]) || isNum(text[i])) {
		i++
	}
	return sim.ACID(text[:i]), text[i:], true, nil
}

func (h *acidParser) GoType() reflect.Type { return reflect.TypeOf(sim.ACID("")) }
func (h *acidParser) ConsumesClick() bool  { return false }

// beaconParser parses beacon/squawk codes (4 octal digits)
type beaconParser struct{}

func (h *beaconParser) Identifier() string { return "BCN" }

func (h *beaconParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 4 {
		return nil, text, false, nil
	}

	if sq, err := av.ParseSquawk(text[:4]); err != nil {
		return nil, text, false, nil
	} else {
		return sq, text[4:], true, nil
	}
}

func (h *beaconParser) GoType() reflect.Type { return reflect.TypeOf(av.Squawk(0)) }
func (h *beaconParser) ConsumesClick() bool  { return false }

// mapGroupParser parses video map group names
type mapGroupParser struct{}

func (h *mapGroupParser) Identifier() string { return "MAP_GROUP" }

func (h *mapGroupParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// Return the map group name - validation happens in the handler
	return field, remaining, true, nil
}

func (h *mapGroupParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *mapGroupParser) ConsumesClick() bool  { return false }

// crrLabelParser parses CRR group labels (1-5 alphanumeric characters)
type crrLabelParser struct{}

func (h *crrLabelParser) Identifier() string { return "CRR_LABEL" }

func (h *crrLabelParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// CRR labels must be 1-5 alphanumeric characters
	if len(field) < 1 || len(field) > 5 {
		return nil, text, false, nil
	}

	// Validate all characters are alphanumeric
	for i := 0; i < len(field); i++ {
		if !isAlpha(field[i]) && !isNum(field[i]) {
			return nil, text, false, nil
		}
	}

	return strings.ToUpper(field), remaining, true, nil
}

func (h *crrLabelParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *crrLabelParser) ConsumesClick() bool  { return false }

// crrLocParser parses CRR location tokens (//FIX, //FRD, //lat/long)
// Returns a CRRLocation struct with the location and original token
type crrLocParser struct{}

// CRRLocation represents a parsed CRR location from //FIX, //FRD, etc.
type CRRLocation struct {
	Location math.Point2LL
	Token    string // Original token without // prefix (for deriving labels)
}

func (h *crrLocParser) Identifier() string { return "CRR_LOC" }

func (h *crrLocParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !strings.HasPrefix(field, "//") {
		return nil, text, false, nil
	}

	// Parse using existing CRR location logic
	loc, ok := parseLocation(ctx, field)
	if !ok {
		return nil, text, false, nil
	}

	token := strings.TrimPrefix(strings.ToUpper(field), "//")
	return CRRLocation{Location: loc, Token: token}, remaining, true, nil
}

func (h *crrLocParser) GoType() reflect.Type { return reflect.TypeOf(CRRLocation{}) }
func (h *crrLocParser) ConsumesClick() bool  { return false }

// locSymParser matches the location symbol 'w' embedded in text from clicking.
// Returns [2]float32 lat/long coordinates from CommandInput.
type locSymParser struct{}

func (h *locSymParser) Identifier() string { return "LOC_SYM" }

func (h *locSymParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Must have a click with positions available and the text must start with the location symbol
	if !input.hasClick || len(input.mousePositions) == 0 {
		return nil, text, false, nil
	}

	// Check if text starts with the location symbol (possibly with leading space)
	trimmed := strings.TrimLeft(text, " ")
	if !strings.HasPrefix(trimmed, locationSymbol) {
		return nil, text, false, nil
	}

	// Consume the location symbol (and any leading space)
	remaining := strings.TrimPrefix(trimmed, locationSymbol)

	// Determine which position to use: total positions minus remaining w's in the text
	// (including the one we're consuming now).
	idx := len(input.mousePositions) - strings.Count(text, locationSymbol)
	if idx < 0 || idx >= len(input.mousePositions) {
		return nil, text, false, nil
	}

	p := input.mousePositions[idx]
	if !input.posIsLatLong {
		p = input.transforms.LatLongFromWindowP(p)
	}

	return p, remaining, true, nil
}

func (h *locSymParser) GoType() reflect.Type { return reflect.TypeOf((*[2]float32)(nil)).Elem() }
func (h *locSymParser) ConsumesClick() bool  { return true } // Uses click position from input

// minutesParse parses minutes for QU
type minutesParser struct{}

func (h *minutesParser) Identifier() string { return "MINUTES" }

func (h *minutesParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	minutes, err := strconv.Atoi(field)
	if err != nil {
		return nil, text, false, nil
	}
	return minutes, remaining, true, nil
}

func (h *minutesParser) GoType() reflect.Type { return reflect.TypeOf(0) }
func (h *minutesParser) ConsumesClick() bool  { return false }

///////////////////////////////////////////////////////////////////////////
// QS HSF parsers

type hsfTextParser struct{}

func (h *hsfTextParser) Identifier() string { return "HSF_TEXT" }

func (h *hsfTextParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}
	if field[0] != '`' && !strings.HasPrefix(field, circleClear) {
		return nil, text, false, nil
	}
	// Require at least one character after the indicator.
	if len(field) < 2 {
		return nil, text, true, ErrCommandFormat
	}
	// Free text is limited to 8 characters (excluding the indicator).
	if len(field)-1 > 8 {
		return nil, text, true, NewERAMError("INVALID TEXT FORMAT")
	}
	return field, remaining, true, nil
}

func (h *hsfTextParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *hsfTextParser) ConsumesClick() bool  { return false }

// Headings don't have to be actual headings, just cannot be > 4 characters.
type hsfHeadingParser struct{}

func (h *hsfHeadingParser) Identifier() string { return "HSF_HDG" }

func (h *hsfHeadingParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}
	// Disallow other QS syntaxes from being interpreted as headings.
	if strings.HasPrefix(field, "/") || strings.HasPrefix(field, "`") || strings.HasPrefix(field, "*") {
		return nil, text, false, nil
	}
	if len(field) > 4 {
		return nil, text, true, NewERAMError("INVALID HEADING FORMAT")
	}
	return field, remaining, true, nil
}

func (h *hsfHeadingParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *hsfHeadingParser) ConsumesClick() bool  { return false }

// hsfSpeedParser parses QS speed/mach scratchpad entries. It returns the canonical
// form used for storage and display (e.g. "S250", "M75", "S250+", "M75-").
//
// Supported formats:
//   - /250     => S250 (inferred, 3 digits => knots)
//   - /75      => M75  (inferred, 2 digits => mach)
//   - /S250    => S250 (explicit speed)
//   - /M75     => M75  (explicit mach)
//
// Modifiers:
//   - Inferred: /250+, /75-, /M75- allowed
//
// Restrictions:
//   - /S250+ invalid
//   - explicit mach with 3 digits (e.g. /M750) cannot have +/- (e.g. /M750+ invalid)
type hsfSpeedParser struct{}

func (h *hsfSpeedParser) Identifier() string { return "HSF_SPEED" }

func (h *hsfSpeedParser) Parse(ep *ERAMPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || field[0] != '/' {
		return nil, text, false, nil
	}

	rest := field[1:]
	if rest == "" {
		return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
	}

	// Optional trailing + or -
	var mod byte
	if n := len(rest); n > 0 && (rest[n-1] == '+' || rest[n-1] == '-') {
		mod = rest[n-1]
		rest = rest[:n-1]
	}
	if rest == "" {
		return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
	}

	upper := strings.ToUpper(rest)

	// Explicit speed: /S250. + and - don't work here.
	if strings.HasPrefix(upper, "S") {
		if mod != 0 {
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}
		d := rest[1:]
		if len(d) != 3 || !allDigits(d) {
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}
		return "S" + d, remaining, true, nil
	}

	// Explicit mach: /M75 or /M750. Can be 2-3 digits. + and - don't work when 3 digits.
	if strings.HasPrefix(upper, "M") {
		d := rest[1:]
		if len(d) < 2 || len(d) > 3 || !allDigits(d) {
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}

		if v, _ := strconv.Atoi(d); v < 10 || v > 999 { // 2-3 digits restriction
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}
		if len(d) == 3 && mod != 0 { // 3 digits +/- restriction
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}
		out := "M" + d
		if mod != 0 {
			out += string(mod)
		}
		return out, remaining, true, nil
	}

	// Inferred: digits only. 2 -> mach; 3 -> speed
	if !allDigits(rest) {
		return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
	}
	switch len(rest) {
	case 2:
		// mach
		if v, _ := strconv.Atoi(rest); v < 10 || v > 99 {
			return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
		}
		out := "M" + rest
		if mod != 0 {
			out += string(mod)
		}
		return out, remaining, true, nil
	case 3:
		// speed
		out := "S" + rest
		if mod != 0 {
			out += string(mod)
		}
		return out, remaining, true, nil
	default:
		return nil, text, true, NewERAMError("INVALID SPEED FORMAT")
	}
}

func (h *hsfSpeedParser) GoType() reflect.Type { return reflect.TypeOf("") }
func (h *hsfSpeedParser) ConsumesClick() bool  { return false }

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isNum(s[i]) {
			return false
		}
	}
	return true
}

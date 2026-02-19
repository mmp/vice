// stars/parsetypes.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// typeParser defines the interface for parsing and validating typed parameters.
type typeParser interface {
	// Identifier returns the token identifier for this handler (e.g., "ALL_TEXT", "ACID").
	Identifier() string

	// Parse attempts to extract this type from the input text.
	// The text parameter contains the unparsed portion starting at the current position.
	// Parsers should consume from the start of text (no automatic space skipping).
	// Returns:
	//   value: the extracted value
	//   remaining: the unconsumed portion of text
	//   matched: true if this looks like this type
	//   err: non-nil if matched but invalid
	Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (value any, remaining string, matched bool, err error)

	// GoType returns the Go type this handler produces
	GoType() reflect.Type

	ConsumesClick() bool
}

// typeParsers is a slice of typeParsers ordered by priority (earlier = higher priority).
var typeParsers = []typeParser{
	&numberParser{id: "#", digits: 1},
	&numberParser{id: "##", digits: 2},

	// Typed with format validation
	&spcParser{},
	&trackACIDParser{},
	&trackBeaconParser{},
	&trackIndexParser{},
	&trackSuspendedIndexParser{},
	&unassociatedFPParser{},
	&acidParser{},
	&beaconParser{},
	&beaconBlockParser{},
	&tcp1Parser{},
	&tcp2Parser{},
	&tcwParser{},
	&triTCPParser{},
	&artccParser{},
	&airportIdParser{},
	&crdaRegionIdParser{},
	&timeParser{},
	&altFilter6Parser{},
	&qlRegionParser{},
	&fdamRegionParser{},
	&raIndexParser{userOnly: false},
	&raIndexParser{userOnly: true},
	&fixParser{},

	// Generic type parsers
	&numberParser{id: "NUM"},
	&tpaFloatParser{},
	&floatParser{},
	&raTextParser{expectLocation: false, closedShape: false},
	&raTextParser{expectLocation: true, closedShape: false},
	&raTextParser{expectLocation: false, closedShape: true},
	&raTextParser{expectLocation: true, closedShape: true},
	&raLocationParser{},
	&qlPositionsParser{},

	// Flight plan entities
	&fpACIDParser{},
	&fpBeaconParser{},
	&fpSP1Parser{},
	&fpTriSP1Parser{},
	&fpPlusSP2Parser{},
	&fpAltAParser{},
	&fpAltPParser{},
	&fpAltRParser{},
	&fpTriAltAParser{},
	&fpPlusAltAParser{},
	&fpPlus2AltRParser{},
	&fpTCPParser{},
	&fpNumActypeParser{},
	&fpNumAcType4Parser{},
	&fpActypeParser{},
	&fpCoordTimeParser{},
	&fpFixPairParser{},
	&fpExitFixParser{},
	&fpFlttypeParser{},
	&fpRulesParser{},
	&fpRNAVParser{},
	&fpVFRFixesParser{},

	// Catch-all text handlers
	&fieldParser{},
	&allTextParser{},

	// Mouse click consumers
	&slewParser{},
	&ghostSlewParser{},
	&posParser{},
	&posNormParser{},
	&posRawParser{},
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

///////////////////////////////////////////////////////////////////////////
// Parser implementations

type spcParser struct{}

func (h *spcParser) Identifier() string { return "SPC" }

func (h *spcParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) >= 2 && ctx.Client.StringIsSPC(text[:2]) {
		return text[:2], text[2:], true, nil
	} else {
		return nil, text, false, nil
	}
}

func (h *spcParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *spcParser) ConsumesClick() bool  { return false }

type trackACIDParser struct{}

func (h *trackACIDParser) Identifier() string { return "TRK_ACID" }

func (h *trackACIDParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	acid, _ := util.CutAtSpace(text)
	if acid == "" {
		return nil, text, false, nil
	}

	idx := slices.IndexFunc(sp.visibleTracks, func(trk sim.Track) bool {
		return trk.IsAssociated() && trk.FlightPlan.ACID == sim.ACID(acid)
	})
	if idx == -1 {
		return nil, text, false, nil
	}

	return &sp.visibleTracks[idx], strings.TrimPrefix(text, acid), true, nil
}

func (h *trackACIDParser) GoType() reflect.Type { return reflect.TypeFor[*sim.Track]() }
func (h *trackACIDParser) ConsumesClick() bool  { return false }

type trackBeaconParser struct{}

func (h *trackBeaconParser) Identifier() string { return "TRK_BCN" }

func (h *trackBeaconParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 4 {
		return nil, text, false, nil
	}

	sq, err := av.ParseSquawk(text[:4])
	if err != nil {
		return nil, text, false, nil
	}

	idx := slices.IndexFunc(sp.visibleTracks, func(trk sim.Track) bool {
		return trk.IsAssociated() && trk.Squawk == sq
	})
	if idx == -1 {
		return nil, text, false, nil
	}

	return &sp.visibleTracks[idx], text[4:], true, nil
}

func (h *trackBeaconParser) GoType() reflect.Type { return reflect.TypeFor[*sim.Track]() }
func (h *trackBeaconParser) ConsumesClick() bool  { return false }

type trackIndexParser struct{}

func (h *trackIndexParser) Identifier() string { return "TRK_INDEX" }

func (h *trackIndexParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	num, remainder, _ := util.CutFunc(text, func(ch rune) bool { return !isNum(byte(ch)) })

	if len(num) == 0 { // no numbers, no match
		return nil, text, false, nil
	}

	listIndex, err := strconv.Atoi(num)
	if err != nil {
		return nil, text, false, nil
	}

	trk, ok := util.SeqLookupFunc(slices.Values(sp.visibleTracks), func(trk sim.Track) bool {
		return trk.IsAssociated() && trk.FlightPlan.ListIndex == listIndex
	})
	if !ok {
		return nil, text, false, nil
	}

	return &trk, remainder, true, nil
}

func (h *trackIndexParser) GoType() reflect.Type { return reflect.TypeFor[*sim.Track]() }
func (h *trackIndexParser) ConsumesClick() bool  { return false }

type trackSuspendedIndexParser struct{}

func (h *trackSuspendedIndexParser) Identifier() string { return "TRK_INDEX_SUSPENDED" }

func (h *trackSuspendedIndexParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	num, remainder, _ := util.CutFunc(text, func(ch rune) bool { return !isNum(byte(ch)) })

	if len(num) == 0 { // no numbers, no match
		return nil, text, false, nil
	}

	listIndex, err := strconv.Atoi(num)
	if err != nil {
		return nil, text, false, nil
	}

	trk, ok := util.SeqLookupFunc(maps.Values(ctx.Client.State.Tracks),
		func(trk *sim.Track) bool {
			return trk.IsAssociated() && trk.FlightPlan.Suspended && trk.FlightPlan.CoastSuspendIndex == listIndex
		})
	if !ok {
		return nil, text, false, nil
	}

	return trk, remainder, true, nil
}

func (h *trackSuspendedIndexParser) GoType() reflect.Type { return reflect.TypeFor[*sim.Track]() }
func (h *trackSuspendedIndexParser) ConsumesClick() bool  { return false }

type slewParser struct{}

func (h *slewParser) Identifier() string { return "SLEW" }

func (h *slewParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	return input.clickedTrack, text, input.clickedTrack != nil, nil
}

func (h *slewParser) GoType() reflect.Type { return reflect.TypeFor[*sim.Track]() }
func (h *slewParser) ConsumesClick() bool  { return true }

type ghostSlewParser struct{}

func (h *ghostSlewParser) Identifier() string { return "GHOST_SLEW" }

func (h *ghostSlewParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	return input.clickedGhost, text, input.clickedGhost != nil, nil
}

func (h *ghostSlewParser) GoType() reflect.Type { return reflect.TypeFor[*av.GhostTrack]() }
func (h *ghostSlewParser) ConsumesClick() bool  { return true }

// unassociatedFPParser looks up an existing flight plan by ACID, beacon code, or list index
type unassociatedFPParser struct{}

func (h *unassociatedFPParser) Identifier() string { return "UNASSOC_FP" }

func (h *unassociatedFPParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) >= 4 {
		if sq, err := av.ParseSquawk(text[:4]); err == nil {
			for _, fp := range ctx.Client.State.UnassociatedFlightPlans {
				if sq == fp.AssignedSquawk {
					return fp, text[4:], true, nil
				}
			}
		}
	}

	if len(text) >= 2 && isNum(text[0]) && isNum(text[1]) {
		n, _ := strconv.Atoi(text[:2])
		for _, fp := range ctx.Client.State.UnassociatedFlightPlans {
			if fp.ListIndex != sim.UnsetSTARSListIndex && n == fp.ListIndex {
				return fp, text[2:], true, nil
			}
		}
	}

	if field, remainder := util.CutAtSpace(text); field != "" {
		for _, fp := range ctx.Client.State.UnassociatedFlightPlans {
			if fp.ACID == sim.ACID(field) {
				return fp, remainder, true, nil
			}
		}
	}

	return nil, text, false, nil
}

func (h *unassociatedFPParser) GoType() reflect.Type {
	return reflect.TypeFor[*sim.NASFlightPlan]()
}
func (h *unassociatedFPParser) ConsumesClick() bool { return false }

type acidParser struct{}

func (h *acidParser) Identifier() string { return "ACID" }

func (h *acidParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" || !isAlpha(text[0]) {
		return nil, text, false, nil
	}
	i := 1
	for i < len(text) && i < 7 /* 7 chars maximum */ && (isAlpha(text[i]) || isNum(text[i])) {
		i++
	}
	return sim.ACID(text[:i]), text[i:], true, nil
}

func (h *acidParser) GoType() reflect.Type { return reflect.TypeFor[sim.ACID]() }
func (h *acidParser) ConsumesClick() bool  { return false }

// beaconParser parses beacon codes.
type beaconParser struct{}

func (h *beaconParser) Identifier() string { return "BCN" }

func (h *beaconParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 4 {
		return nil, text, false, nil
	}

	if sq, err := av.ParseSquawk(text[:4]); err != nil {
		return nil, text, false, nil
	} else {
		return sq, text[4:], true, nil
	}
}

func (h *beaconParser) GoType() reflect.Type { return reflect.TypeFor[av.Squawk]() }
func (h *beaconParser) ConsumesClick() bool  { return false }

// beaconBlockParser parses beacon blocks (2 octal digits).
type beaconBlockParser struct{}

func (h *beaconBlockParser) Identifier() string { return "BCN_BLOCK" }

func (h *beaconBlockParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 2 {
		return nil, text, false, nil
	}

	if sq, err := strconv.ParseInt(text[:2], 8 /* base 8! */, 32); err != nil {
		return nil, text, false, nil
	} else {
		return av.Squawk(sq), text[2:], true, nil
	}
}

func (h *beaconBlockParser) GoType() reflect.Type { return reflect.TypeFor[av.Squawk]() }
func (h *beaconBlockParser) ConsumesClick() bool  { return false }

// tcp1Parser parses single-character controller position IDs.
// TCP1 is always 1 character: letter A-Z
type tcp1Parser struct{}

func (h *tcp1Parser) Identifier() string { return "TCP1" }

func (h *tcp1Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) == 0 || !isAlpha(text[0]) { // Must be letter A-Z
		return nil, text, false, nil
	}
	return text[:1], text[1:], true, nil
}

func (h *tcp1Parser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *tcp1Parser) ConsumesClick() bool  { return false }

// tcp2Parser parses two-character controller position IDs.
// TCP2 is always 2 characters: digit 1-9 followed by letter A-Z
type tcp2Parser struct{}

func (h *tcp2Parser) Identifier() string { return "TCP2" }

func (h *tcp2Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 2 || !isNum(text[0]) || !isAlpha(text[1]) {
		return nil, text, false, nil
	}
	return text[:2], text[2:], true, nil
}

func (h *tcp2Parser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *tcp2Parser) ConsumesClick() bool  { return false }

// triTCPParser parses triangle-prefixed TCPs--always 3 characters
type triTCPParser struct{}

func (h *triTCPParser) Identifier() string { return "TCP_TRI" }

func (h *triTCPParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if !strings.HasPrefix(text, STARSTriangleCharacter) {
		return nil, text, false, nil
	}

	tcp := strings.TrimPrefix(text, STARSTriangleCharacter)
	// Should be a digit identifying the facility followed by a regular digit+character TCP identifier.
	if len(tcp) < 3 || !isNum(tcp[0]) || !isNum(tcp[1]) || !isAlpha(tcp[2]) {
		return nil, text, false, nil
	}

	return STARSTriangleCharacter + tcp[:3], tcp[3:], true, nil
}

func (h *triTCPParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *triTCPParser) ConsumesClick() bool  { return false }

// tcwParser parses two-character terminal display workstation IDs.
// TCW is always 2 characters: digit 1-9 followed by letter A-Z
type tcwParser struct{}

func (h *tcwParser) Identifier() string { return "TCW" }

func (h *tcwParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 2 || !isNum(text[0]) || !isAlpha(text[1]) {
		return nil, text, false, nil
	}
	return sim.TCW(text[:2]), text[2:], true, nil
}

func (h *tcwParser) GoType() reflect.Type { return reflect.TypeFor[sim.TCW]() }
func (h *tcwParser) ConsumesClick() bool  { return false }

// artccParser parses ARTCC position identifiers (3 chars: letter + 2 digits, e.g., "Z90", or one letter, e.g. "C").
type artccParser struct{}

func (h *artccParser) Identifier() string { return "ARTCC" }

func (h *artccParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) >= 3 && isAlpha(text[0]) && isNum(text[1]) && isNum(text[2]) {
		return text[:3], text[3:], true, nil
	} else if len(text) >= 1 && text[0] == 'C' {
		return text[:1], text[1:], true, nil
	} else {
		return nil, text, false, nil
	}
}

func (h *artccParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *artccParser) ConsumesClick() bool  { return false }

// airportIdParser parses airport ids: 3 characters
type airportIdParser struct{}

func (h *airportIdParser) Identifier() string { return "AIRPORT_ID" }

func (h *airportIdParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 3 {
		return nil, text, false, nil
	}
	if _, ok := av.DB.LookupAirport(text[:3]); ok {
		return text[:3], text[3:], true, nil
	}
	return nil, text, false, nil
}

func (h *airportIdParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *airportIdParser) ConsumesClick() bool  { return false }

type crdaRegionIdParser struct{}

func (h *crdaRegionIdParser) Identifier() string { return "CRDA_REGION_ID" }

func (h *crdaRegionIdParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if len(text) < 1 {
		return nil, text, false, nil
	}

	ps := sp.currentPrefs()
	if isAlpha(text[0]) {
		// Could be "APT REGION_NAME" — try to match airport prefix first
		if len(text) >= 5 && text[3] == ' ' {
			if _, ok := av.DB.LookupAirport(text[:3]); ok {
				ap := text[:3]
				rest := text[4:]

				// Longest-match against region names from enabled pairs at this airport
				var bestMatch *CRDARunwayState
				bestLen := 0
				for i, pair := range sp.CRDAPairs {
					if pair.Airport != ap {
						continue
					}
					pairState := &ps.CRDA.RunwayPairState[i]
					if !pairState.Enabled {
						continue
					}
					for j, regionName := range pair.Regions {
						if len(regionName) > len(rest) {
							continue
						}
						if rest[:len(regionName)] == regionName && len(regionName) > bestLen {
							bestMatch = &pairState.RunwayState[j]
							bestLen = len(regionName)
						}
					}
				}
				if bestMatch != nil {
					return bestMatch, rest[bestLen:], true, nil
				}
				return nil, text, false, nil
			}
		}

		// No airport prefix: fall through to no-prefix matching below
	}

	// No airport prefix: longest-match region name against all enabled pairs.
	var bestMatch *CRDARunwayState
	var bestRemainder string
	bestLen := 0
	ambiguous := false
	for i, pair := range sp.CRDAPairs {
		pairState := &ps.CRDA.RunwayPairState[i]
		if !pairState.Enabled {
			continue
		}
		for j, regionName := range pair.Regions {
			if len(regionName) > len(text) {
				continue
			}
			if text[:len(regionName)] != regionName {
				continue
			}
			if len(regionName) > bestLen {
				bestMatch = &pairState.RunwayState[j]
				bestRemainder = text[len(regionName):]
				bestLen = len(regionName)
				ambiguous = false
			} else if len(regionName) == bestLen && bestMatch != &pairState.RunwayState[j] {
				ambiguous = true
			}
		}
	}

	if ambiguous {
		return nil, text, true, ErrSTARSIllegalParam
	}
	if bestMatch != nil {
		return bestMatch, bestRemainder, true, nil
	}

	return nil, text, false, nil
}

func (h *crdaRegionIdParser) GoType() reflect.Type { return reflect.TypeFor[*CRDARunwayState]() }
func (h *crdaRegionIdParser) ConsumesClick() bool  { return false }

// numberParser parses integer numbers.
type numberParser struct {
	id     string
	digits int
}

func (h *numberParser) Identifier() string { return h.id }

func (h *numberParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	var num, remainder string
	if h.digits != 0 {
		if len(text) < h.digits {
			return nil, text, false, nil
		}
		num, remainder = text[:h.digits], text[h.digits:]
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

func (h *numberParser) GoType() reflect.Type { return reflect.TypeFor[int]() }
func (h *numberParser) ConsumesClick() bool  { return false }

// floatParser parses floating point numbers.
type floatParser struct{}

func (h *floatParser) Identifier() string { return "FLOAT" }

func (h *floatParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	decimalsSeen := 0
	num, remainder, _ := util.CutFunc(text, func(ch rune) bool {
		if ch == '.' {
			if decimalsSeen == 1 { // we've hit a second period; bail
				return true
			}
			decimalsSeen++
			return false
		} else {
			return !isNum(byte(ch))
		}
	})

	if num == "" {
		return nil, text, false, nil
	}

	f, err := strconv.ParseFloat(num, 32)
	if err != nil {
		return nil, text, false, nil
	}

	return float32(f), remainder, true, nil
}

func (h *floatParser) GoType() reflect.Type { return reflect.TypeFor[float32]() }
func (h *floatParser) ConsumesClick() bool  { return false }

// tpaFloatParser parses floating point numbers for the TPA commands (j-rings, etc.)
type tpaFloatParser struct{}

func (h *tpaFloatParser) Identifier() string { return "TPA_FLOAT" }

func (h *tpaFloatParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	i := 0
	num := float32(0)
	for i < len(text) {
		if isNum(text[i]) {
			num = 10*num + float32(text[i]-'0')
			i++
		} else if text[i] == '.' {
			if num > 9 {
				// Decimal point only allowed up to 9.x
				return nil, text, true, ErrSTARSCommandFormat
			}
			i++
			if i == len(text) || !isNum(text[i]) {
				// Nothing following the decimal
				return nil, text, true, ErrSTARSCommandFormat
			}
			return num + float32(text[i]-'0')/10, text[i+1:], true, nil
		} else {
			break
		}
	}
	return num, text[i:], true, nil
}

func (h *tpaFloatParser) GoType() reflect.Type { return reflect.TypeFor[float32]() }
func (h *tpaFloatParser) ConsumesClick() bool  { return false }

// fieldParser extracts a single space-delimited field (token).
type fieldParser struct{}

func (h *fieldParser) Identifier() string { return "FIELD" }

func (h *fieldParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}
	return field, remaining, true, nil
}

func (h *fieldParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *fieldParser) ConsumesClick() bool  { return false }

// allTextParser captures all remaining text from the current position.
// Unlike strings.Cut which stops at spaces, this captures everything.
type allTextParser struct{}

func (h *allTextParser) Identifier() string { return "ALL_TEXT" }

func (h *allTextParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" {
		return nil, text, false, nil
	}
	return text, "", true, nil
}

func (h *allTextParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *allTextParser) ConsumesClick() bool  { return false }

// timeParser parses 4-digit 24-hour time format (e.g., "0830", "1645", "2359").
// Returns a string in HHMM format. Validates that HH is 00-23 and MM is 00-59.
type timeParser struct{}

func (h *timeParser) Identifier() string { return "TIME" }

func (h *timeParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Need 4 digits
	if len(text) < 4 {
		return nil, text, false, nil
	}

	_, err := time.Parse("1504", text[:4])
	if err != nil {
		return nil, text, false, nil
	}

	return text[:4], text[4:], true, nil
}

func (h *timeParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *timeParser) ConsumesClick() bool  { return false }

// altFilter6Parser parses 6-digit altitude filter format (e.g., "010250" → low=1000ft, high=25000ft).
// Each 3-digit group represents altitude in hundreds of feet.
type altFilter6Parser struct{}

func (h *altFilter6Parser) Identifier() string { return "ALT_FILTER_6" }

func (h *altFilter6Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Need 6 digits
	if len(text) < 6 {
		return nil, text, false, nil
	}

	for i := range text[:6] {
		if !isNum(text[i]) {
			return nil, text, false, nil
		}
	}

	// Parse the 6 digits into two 3-digit groups
	low, err := strconv.Atoi(text[:3])
	if err != nil {
		return nil, text, false, nil
	}
	high, err := strconv.Atoi(text[3:6])
	if err != nil {
		return nil, text, false, nil
	}

	// Return in feet (from 100s as given)
	return [2]int{low * 100, high * 100}, text[6:], true, nil
}

func (h *altFilter6Parser) GoType() reflect.Type { return reflect.TypeFor[[2]int]() }
func (h *altFilter6Parser) ConsumesClick() bool  { return false }

// qlRegionParser validates and parses quicklook region IDs.
type qlRegionParser struct{}

func (h *qlRegionParser) Identifier() string { return "QL_REGION" }

func (h *qlRegionParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !ctx.FacilityAdaptation.Filters.Quicklook.HaveId(field) {
		return nil, text, false, nil
	}
	return field, remaining, true, nil
}

func (h *qlRegionParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *qlRegionParser) ConsumesClick() bool  { return false }

// fdamRegionParser validates and parses FDAM region IDs.
type fdamRegionParser struct{}

func (h *fdamRegionParser) Identifier() string { return "FDAM_REGION" }

func (h *fdamRegionParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !ctx.FacilityAdaptation.Filters.FDAM.HaveId(field) {
		return nil, text, false, nil
	}
	return field, remaining, true, nil
}

func (h *fdamRegionParser) GoType() reflect.Type { return reflect.TypeFor[string]() }
func (h *fdamRegionParser) ConsumesClick() bool  { return false }

// raIndexParser parses and validates restriction area indices and returns the associated RestrictionArea
type raIndexParser struct {
	userOnly bool
}

func (h *raIndexParser) Identifier() string {
	return util.Select(h.userOnly, "USER_", "") + "RA_INDEX"
}

func (h *raIndexParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	num, remainder, _ := util.CutFunc(text, func(ch rune) bool { return !isNum(byte(ch)) })

	idx, err := strconv.Atoi(num)
	if err != nil {
		return nil, text, false, nil
	}

	if sp.getRestrictionArea(ctx, idx, h.userOnly) != nil {
		return idx, remainder, true, nil
	}

	return text, "", true, ErrSTARSIllegalGeoId
}

func (h *raIndexParser) GoType() reflect.Type { return reflect.TypeFor[int]() }
func (h *raIndexParser) ConsumesClick() bool  { return false }

// raTextParser parses restriction area text with modifiers (△ blink, + shade, *N color).
// closedShape controls whether + (shade) and *N (color) are valid; they are only
// allowed for closed shapes (circles and polygons), not for text-only restriction areas.
type raTextParser struct {
	expectLocation bool
	closedShape    bool
}

type RAText struct {
	text   [2]string
	blink  bool
	shaded bool
	color  int
	pos    math.Point2LL // only set for RA_TEXT_AND_LOCATION
}

func (h *raTextParser) Identifier() string {
	return util.Select(h.closedShape, "RA_CLOSED_TEXT", "RA_TEXT") +
		util.Select(h.expectLocation, "_AND_LOCATION", "")
}

func (h *raTextParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" {
		return nil, text, false, nil
	}

	var parsed RAText
	doTriPlus := func(s string) error {
		var getColor bool
		for _, ch := range s {
			if getColor {
				if ch < '1' || ch > '8' {
					return ErrSTARSIllegalColor
				}
				parsed.color = int(ch - '0') // 1-based indexing
				getColor = false
			} else if string(ch) == STARSTriangleCharacter {
				parsed.blink = true
			} else if ch == '+' && h.closedShape {
				parsed.shaded = true
			} else if ch == '*' && h.closedShape {
				getColor = true
			} else {
				return ErrSTARSCommandFormat
			}
		}
		if getColor {
			return ErrSTARSCommandFormat
		}
		return nil
	}

	tidyRAText := func(s string) string {
		s = strings.TrimSpace(strings.ReplaceAll(s, ".", " "))
		if len(s) > 32 {
			// In theory we're suppose to give a CAPACITY error in this case..
			s = s[:32]
		}
		return s
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, text, true, ErrSTARSCommandFormat
	}

	// It's illegal to give the additional options as the text for the
	// first line so return an error if it parses cleanly.
	if doTriPlus(fields[0]) == nil {
		return nil, text, true, ErrSTARSCommandFormat
	}

	parsed.text[0] = tidyRAText(fields[0])
	fields = fields[1:]

	if h.expectLocation {
		// Semi tricky: need to handle input like "RBV RBV RBV": that's two lines of text and then a fix,
		// or "RBV RBV 180 5", which is one line of text and then a fix with bearing and distance.
		// So, we'll work backward to find the first valid location parse.
		n := len(fields)
		if n >= 1 {
			if p, _, matched, err := parseRALocation(sp, ctx, fields[n-1]); matched && err == nil {
				parsed.pos = p
				fields = fields[:n-1]
			}
		}
		if n >= 3 {
			if p, _, matched, err := parseRALocation(sp, ctx, strings.Join(fields[n-3:], " ")); matched && err == nil {
				parsed.pos = p
				fields = fields[:n-3]
			}
		}
		if parsed.pos.IsZero() {
			// We've already parsed text fields, so we matched but location is invalid
			return nil, text, true, ErrSTARSCommandFormat
		}
	}

	// We may be done, may get a ∆ or +, or may have a second line of text.
	if len(fields) > 0 {
		if doTriPlus(fields[0]) == nil {
			fields = fields[1:]
		} else {
			parsed.text[1] = tidyRAText(fields[0])
			fields = fields[1:]

			if len(fields) > 0 && doTriPlus(fields[0]) == nil {
				fields = fields[1:]
			}
		}
	}

	return parsed, strings.Join(fields, " "), true, nil
}

func (h *raTextParser) GoType() reflect.Type { return reflect.TypeFor[RAText]() }
func (h *raTextParser) ConsumesClick() bool  { return false }

// raLocationParser parses restriction area locations (DMS, fix names, bearing/distance).
type raLocationParser struct{}

func (h *raLocationParser) Identifier() string { return "RA_LOCATION" }

func (h *raLocationParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	p, remaining, matched, err := parseRALocation(sp, ctx, text)
	return p, remaining, matched, err
}

// parseRALocation parses restriction area locations (DMS, fix names, bearing/distance).
// Returns (point, remaining, matched, error) where:
// - matched=false, err=nil: input doesn't look like a location
// - matched=true, err=nil: successfully parsed location
// - matched=true, err!=nil: recognized as location but invalid format
func parseRALocation(sp *STARSPane, ctx *panes.Context, text string) (math.Point2LL, string, bool, error) {
	parseBasic := func(pos string) (math.Point2LL, bool) {
		if latstr, longstr, ok := strings.Cut(pos, "/"); ok { // latitude/longitude
			if len(latstr) != 7 || (latstr[6] != 'N' && latstr[6] != 'S') {
				return math.Point2LL{}, false
			}

			convert := func(str string, neg bool) (float32, bool) {
				lat, err := strconv.Atoi(str)
				if err != nil {
					return 0, false
				}
				deg := (lat / 10000)
				min := (lat / 100) % 100
				sec := lat % 100
				v := float32(deg) + float32(min)/60 + float32(sec)/3600
				if neg {
					v = -v
				}
				return v, true
			}

			var p math.Point2LL
			var ok bool
			p[1], ok = convert(latstr[:6], latstr[6] == 'S')
			if !ok {
				return math.Point2LL{}, false
			}

			if len(longstr) != 8 || (longstr[7] != 'E' && longstr[7] != 'W') {
				return math.Point2LL{}, false
			}
			p[0], ok = convert(longstr[:7], longstr[7] == 'W')
			if !ok {
				return math.Point2LL{}, false
			}
			return p, true
		} else if p, ok := sp.significantPoints[pos]; ok {
			return p.Location, true
		} else if p, ok := av.DB.LookupWaypoint(pos); ok {
			return p, true
		} else {
			return math.Point2LL{}, false
		}
	}

	pos, remaining := util.CutAtSpace(text)
	p, ok := parseBasic(pos)
	if !ok {
		// First token doesn't look like a valid position - not a match
		return math.Point2LL{}, text, false, nil
	}

	// Bearing and distance may only be given with named fixes
	if remaining != "" && !strings.Contains(pos, "/") {
		var bearingStr, distStr string
		bearingStr, remaining = util.CutAtSpace(remaining[1:]) // skip space
		if remaining == "" {
			// Matched a position but bearing/distance format is invalid
			return math.Point2LL{}, text, true, ErrSTARSCommandFormat
		}
		distStr, remaining = util.CutAtSpace(remaining[1:]) // skip space

		bearing, err := strconv.Atoi(bearingStr)
		if err != nil || bearing < 1 || bearing > 360 {
			// Matched a position but bearing is invalid
			return math.Point2LL{}, text, true, ErrSTARSCommandFormat
		}

		dist, err := strconv.ParseFloat(distStr, 32)
		if err != nil || dist > 125 {
			// Matched a position but distance is invalid
			return math.Point2LL{}, text, true, ErrSTARSCommandFormat
		}

		p = math.Offset2LL(p, float32(bearing), float32(dist), ctx.NmPerLongitude, ctx.MagneticVariation)
	}

	// TODO: ILL GEO LOC if lat/long or fix-offset location is "not on the system plane"(?)

	return p, remaining, true, nil
}

func (h *raLocationParser) GoType() reflect.Type { return reflect.TypeFor[math.Point2LL]() }
func (h *raLocationParser) ConsumesClick() bool  { return false }

// posParser handles click position as lat/long (math.Point2LL).
type posParser struct{}

func (h *posParser) Identifier() string { return "POS" }

func (h *posParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Only match clicks on empty space, not on tracks. Use SLEW for track clicks.
	if !input.hasClick || input.clickedTrack != nil {
		return nil, text, false, nil
	}
	p := input.transforms.LatLongFromWindowP(input.mousePosition)
	return p, text, true, nil
}

func (h *posParser) GoType() reflect.Type { return reflect.TypeFor[math.Point2LL]() }
func (h *posParser) ConsumesClick() bool  { return true }

// posNormParser handles click position as normalized coordinates ([2]float32).
type posNormParser struct{}

func (h *posNormParser) Identifier() string { return "POS_NORM" }

func (h *posNormParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Only match clicks on empty space, not on tracks. Use SLEW for track clicks.
	if !input.hasClick || input.clickedTrack != nil {
		return nil, text, false, nil
	}
	return input.transforms.NormalizedFromWindowP(input.mousePosition), text, true, nil
}

func (h *posNormParser) GoType() reflect.Type { return reflect.TypeFor[[2]float32]() }
func (h *posNormParser) ConsumesClick() bool  { return true }

// posRawParser handles click position as raw window coordinates ([2]float32).
type posRawParser struct{}

func (h *posRawParser) Identifier() string { return "POS_RAW" }

func (h *posRawParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	// Only match clicks on empty space, not on tracks. Use SLEW for track clicks.
	if !input.hasClick || input.clickedTrack != nil {
		return nil, text, false, nil
	}
	return input.mousePosition, text, true, nil
}

func (h *posRawParser) GoType() reflect.Type { return reflect.TypeFor[[2]float32]() }
func (h *posRawParser) ConsumesClick() bool  { return true }

// fixParser parses navigation fix names and returns their location.
type fixParser struct{}

func (h *fixParser) Identifier() string { return "FIX" }

func (h *fixParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !isAlpha(field[0]) {
		return nil, text, false, nil
	}

	// Look up the fix location
	if p, ok := ctx.Client.State.Locate(field); ok {
		return p, remaining, true, nil
	}

	return nil, text, true, ErrSTARSIllegalFix
}

func (h *fixParser) GoType() reflect.Type { return reflect.TypeFor[math.Point2LL]() }
func (h *fixParser) ConsumesClick() bool  { return false }

// qlPositionsParser parses quicklook position updates (e.g., "1A+B ").
type qlPositionsParser struct{}

func (h *qlPositionsParser) Identifier() string { return "QL_POSITIONS" }

func (h *qlPositionsParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	if text == "" {
		return nil, text, false, nil
	}

	// Quicklook position specifiers are lists of TCPs, possibly with + suffixes.
	// They may be given as two-character TCPs: a number and a letter, or just a letter,
	// in which case a) they are taken to be of the same control subset as the user and b)
	// they must be followed by a space unless they are at the end of the string.
	var tcps []string
	i := 0

	for i < len(text) {
		var tcp string

		if isNum(text[i]) {
			// Two-character TCP: digit followed by letter
			if i+1 >= len(text) || !isAlpha(text[i+1]) {
				return nil, text, false, nil
			}
			tcp = text[i : i+2]
			i += 2
		} else if isAlpha(text[i]) {
			// Single-letter TCP (same subset as user)
			tcp = string(text[i])
			i++
		} else {
			return nil, text, false, nil
		}

		// Check for optional + suffix
		if i < len(text) && text[i] == '+' {
			tcp += "+"
			i++
		}

		tcps = append(tcps, tcp)

		// Single-letter TCPs require a space separator (unless at end)
		if len(tcp) == 1 || (len(tcp) == 2 && tcp[1] == '+') {
			if i < len(text) {
				if text[i] != ' ' {
					return nil, text, false, nil
				}
				i++
			}
		}
	}

	return tcps, text[i:], true, nil
}

func (h *qlPositionsParser) GoType() reflect.Type { return reflect.TypeFor[[]string]() }
func (h *qlPositionsParser) ConsumesClick() bool  { return false }

///////////////////////////////////////////////////////////////////////////
// FP Field Type Parsers
//
// These parsers return partial FlightPlanSpecifiers with just their fields set.
// Multiple FlightPlanSpecifiers are merged before being passed to command handlers.

// fpSpecType is the Go type for FlightPlanSpecifier.
var fpSpecType = reflect.TypeFor[sim.FlightPlanSpecifier]()

type fpACIDParser struct{}

func (h *fpACIDParser) Identifier() string { return "FP_ACID" }

func (h *fpACIDParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// ACID must start with a letter
	if field[0] < 'A' || field[0] > 'Z' {
		return nil, text, false, nil
	}
	// No more than 7 characters
	if len(field) > 7 {
		return nil, text, true, ErrSTARSIllegalACID
	}

	var spec sim.FlightPlanSpecifier
	spec.ACID.Set(sim.ACID(field))
	return spec, remaining, true, nil
}

func (h *fpACIDParser) GoType() reflect.Type { return fpSpecType }
func (h *fpACIDParser) ConsumesClick() bool  { return false }

type fpBeaconParser struct{}

func (h *fpBeaconParser) Identifier() string { return "FP_BEACON" }

func (h *fpBeaconParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// Valid beacon formats: +, /, /1-/4, or 4-digit code
	if field == "+" || field == "/" || field == "/1" || field == "/2" || field == "/3" || field == "/4" ||
		(len(field) == 4 && util.IsAllNumbers(field)) {
		var spec sim.FlightPlanSpecifier
		spec.SquawkAssignment.Set(field)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpBeaconParser) GoType() reflect.Type { return fpSpecType }
func (h *fpBeaconParser) ConsumesClick() bool  { return false }

type fpSP1Parser struct{}

func (h *fpSP1Parser) Identifier() string { return "FP_SP1" }

func (h *fpSP1Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// Validate scratchpad
	if err := checkScratchpad(ctx, field, false, false); err != nil {
		if err == ErrSTARSCommandFormat {
			return nil, text, false, nil
		}
		return nil, text, true, err
	}

	var spec sim.FlightPlanSpecifier
	spec.Scratchpad.Set(field)
	return spec, remaining, true, nil
}

func (h *fpSP1Parser) GoType() reflect.Type { return fpSpecType }
func (h *fpSP1Parser) ConsumesClick() bool  { return false }

func checkScratchpad(ctx *panes.Context, contents string, isSecondary, isImplied bool) error {
	lc := len([]rune(contents))
	fac := ctx.FacilityAdaptation

	if !fac.CheckScratchpad(contents) {
		return ErrSTARSCommandFormat
	}

	if !isSecondary && isImplied && lc == 1 {
		// One-character for primary is only allowed via [MF]Y
		return ErrSTARSCommandFormat
	}

	if !isSecondary && isImplied {
		// For the implied version (i.e., not [multifunc]Y), it also can't
		// match one of the TCPs
		if lc == 2 {
			for _, ctrl := range ctx.Client.State.Controllers {
				if ctrl.FacilityIdentifier == "" && ctrl.Position == contents {
					return ErrSTARSCommandFormat
				}
			}
		}
	}

	return nil
}

type fpTriSP1Parser struct{}

func (h *fpTriSP1Parser) Identifier() string { return "FP_TRI_SP1" }

func (h *fpTriSP1Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	if !strings.HasPrefix(field, STARSTriangleCharacter) {
		return nil, text, false, nil
	}
	scratchpad := strings.TrimPrefix(field, STARSTriangleCharacter)

	if err := checkScratchpad(ctx, scratchpad, false, false); err != nil {
		if err == ErrSTARSCommandFormat {
			return nil, text, false, nil
		}
		return nil, text, true, err
	}

	var spec sim.FlightPlanSpecifier
	spec.Scratchpad.Set(scratchpad)
	return spec, remaining, true, nil
}

func (h *fpTriSP1Parser) GoType() reflect.Type { return fpSpecType }
func (h *fpTriSP1Parser) ConsumesClick() bool  { return false }

type fpPlusSP2Parser struct{}

func (h *fpPlusSP2Parser) Identifier() string { return "FP_PLUS_SP2" }

func (h *fpPlusSP2Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	if !strings.HasPrefix(field, "+") {
		return nil, text, false, nil
	}
	scratchpad := strings.TrimPrefix(field, "+")

	if err := checkScratchpad(ctx, scratchpad, true, false); err != nil {
		if err == ErrSTARSCommandFormat {
			return nil, text, false, nil
		}
		return nil, text, true, err
	}

	var spec sim.FlightPlanSpecifier
	spec.SecondaryScratchpad.Set(scratchpad)
	return spec, remaining, true, nil
}

func (h *fpPlusSP2Parser) GoType() reflect.Type { return fpSpecType }
func (h *fpPlusSP2Parser) ConsumesClick() bool  { return false }

func parseFpAltitudeField(field string) (int, bool) {
	if len(field) != 3 {
		return 0, false
	}
	alt, err := strconv.Atoi(field)
	if err != nil {
		return 0, false
	}
	return alt * 100, true
}

type fpAltAParser struct{}

func (h *fpAltAParser) Identifier() string { return "FP_ALT_A" }

func (h *fpAltAParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if alt, ok := parseFpAltitudeField(field); ok {
		var spec sim.FlightPlanSpecifier
		spec.AssignedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpAltAParser) GoType() reflect.Type { return fpSpecType }
func (h *fpAltAParser) ConsumesClick() bool  { return false }

type fpAltPParser struct{}

func (h *fpAltPParser) Identifier() string { return "FP_ALT_P" }

func (h *fpAltPParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if alt, ok := parseFpAltitudeField(field); ok {
		var spec sim.FlightPlanSpecifier
		spec.PilotReportedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpAltPParser) GoType() reflect.Type { return fpSpecType }
func (h *fpAltPParser) ConsumesClick() bool  { return false }

type fpAltRParser struct{}

func (h *fpAltRParser) Identifier() string { return "FP_ALT_R" }

func (h *fpAltRParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if alt, ok := parseFpAltitudeField(field); ok {
		var spec sim.FlightPlanSpecifier
		spec.RequestedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpAltRParser) GoType() reflect.Type { return fpSpecType }
func (h *fpAltRParser) ConsumesClick() bool  { return false }

type fpTriAltAParser struct{}

func (h *fpTriAltAParser) Identifier() string { return "FP_TRI_ALT_A" }

func (h *fpTriAltAParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !strings.HasPrefix(field, STARSTriangleCharacter) {
		return nil, text, false, nil
	}
	altStr := strings.TrimPrefix(field, STARSTriangleCharacter)
	if alt, ok := parseFpAltitudeField(altStr); ok {
		var spec sim.FlightPlanSpecifier
		spec.AssignedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpTriAltAParser) GoType() reflect.Type { return fpSpecType }
func (h *fpTriAltAParser) ConsumesClick() bool  { return false }

type fpPlusAltAParser struct{}

func (h *fpPlusAltAParser) Identifier() string { return "FP_PLUS_ALT_A" }

func (h *fpPlusAltAParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !strings.HasPrefix(field, "+") {
		return nil, text, false, nil
	}
	altStr := strings.TrimPrefix(field, "+")
	if alt, ok := parseFpAltitudeField(altStr); ok {
		var spec sim.FlightPlanSpecifier
		spec.AssignedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpPlusAltAParser) GoType() reflect.Type { return fpSpecType }
func (h *fpPlusAltAParser) ConsumesClick() bool  { return false }

type fpPlus2AltRParser struct{}

func (h *fpPlus2AltRParser) Identifier() string { return "FP_PLUS2_ALT_R" }

func (h *fpPlus2AltRParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || !strings.HasPrefix(field, "++") {
		return nil, text, false, nil
	}
	altStr := strings.TrimPrefix(field, "++")
	if alt, ok := parseFpAltitudeField(altStr); ok {
		var spec sim.FlightPlanSpecifier
		spec.RequestedAltitude.Set(alt)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpPlus2AltRParser) GoType() reflect.Type { return fpSpecType }
func (h *fpPlus2AltRParser) ConsumesClick() bool  { return false }

type fpTCPParser struct{}

func (h *fpTCPParser) Identifier() string { return "FP_TCP" }

func (h *fpTCPParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	// TCP must be 2 characters: digit + letter
	if len(field) != 2 || !isNum(field[0]) || !isAlpha(field[1]) {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	spec.TrackingController.Set(sim.TCP(field))
	return spec, remaining, true, nil
}

func (h *fpTCPParser) GoType() reflect.Type { return fpSpecType }
func (h *fpTCPParser) ConsumesClick() bool  { return false }

func parseAcTypeWithEqSuffix(s string) (acType, eqSuffix string, ok bool) {
	acType, suffix, _ := strings.Cut(s, "/")

	if len(acType) < 2 || acType[0] < 'A' || acType[0] > 'Z' {
		return "", "", false
	}

	acType = strings.TrimRight(acType, "*")

	if len(suffix) > 0 {
		if len(suffix) != 1 || suffix[0] < 'A' || suffix[0] > 'Z' {
			return "", "", false
		}
		eqSuffix = suffix
	}
	return acType, eqSuffix, true
}

type fpNumActypeParser struct{}

func (h *fpNumActypeParser) Identifier() string { return "FP_NUM_ACTYPE" }

func (h *fpNumActypeParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	var count int
	s := field

	// Check for formation count
	if tf := strings.Split(s, "/"); len(tf) == 3 {
		c, err := strconv.Atoi(tf[0])
		if err != nil || len(tf[0]) > 2 {
			return nil, text, true, ErrSTARSCommandFormat
		}
		count = c
		s = strings.Join(tf[1:], "/")
	}

	acType, eqSuffix, ok := parseAcTypeWithEqSuffix(s)
	if !ok {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	if count > 0 {
		spec.AircraftCount.Set(count)
	}
	spec.AircraftType.Set(acType)
	if eqSuffix != "" {
		spec.EquipmentSuffix.Set(eqSuffix)
	}
	return spec, remaining, true, nil
}

func (h *fpNumActypeParser) GoType() reflect.Type { return fpSpecType }
func (h *fpNumActypeParser) ConsumesClick() bool  { return false }

type fpNumAcType4Parser struct{}

func (h *fpNumAcType4Parser) Identifier() string { return "FP_NUM_ACTYPE4" }

func (h *fpNumAcType4Parser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	var count int
	tf := strings.Split(field, "/")

	// Check for formation count
	if len(tf) == 3 {
		c, err := strconv.Atoi(tf[0])
		if err != nil || len(tf[0]) > 2 {
			return nil, text, true, ErrSTARSCommandFormat
		}
		count = c
		tf = tf[1:]
	}

	// Require exactly 4 chars for aircraft type
	if len(tf[0]) != 4 {
		return nil, text, false, nil
	}

	acType, eqSuffix, ok := parseAcTypeWithEqSuffix(strings.Join(tf, "/"))
	if !ok {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	if count > 0 {
		spec.AircraftCount.Set(count)
	}
	spec.AircraftType.Set(acType)
	if eqSuffix != "" {
		spec.EquipmentSuffix.Set(eqSuffix)
	}
	return spec, remaining, true, nil
}

func (h *fpNumAcType4Parser) GoType() reflect.Type { return fpSpecType }
func (h *fpNumAcType4Parser) ConsumesClick() bool  { return false }

type fpActypeParser struct{}

func (h *fpActypeParser) Identifier() string { return "FP_ACTYPE" }

func (h *fpActypeParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	acType, eqSuffix, ok := parseAcTypeWithEqSuffix(field)
	if !ok {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	spec.AircraftType.Set(acType)
	if eqSuffix != "" {
		spec.EquipmentSuffix.Set(eqSuffix)
	}
	return spec, remaining, true, nil
}

func (h *fpActypeParser) GoType() reflect.Type { return fpSpecType }
func (h *fpActypeParser) ConsumesClick() bool  { return false }

type fpCoordTimeParser struct{}

func (h *fpCoordTimeParser) Identifier() string { return "FP_COORD_TIME" }

func (h *fpCoordTimeParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	if len(field) != 5 || field[4] != 'E' {
		return nil, text, false, nil
	}

	t, err := time.Parse("1504", field[:4])
	if err != nil {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	spec.CoordinationTime.Set(t)
	return spec, remaining, true, nil
}

func (h *fpCoordTimeParser) GoType() reflect.Type { return fpSpecType }
func (h *fpCoordTimeParser) ConsumesClick() bool  { return false }

type fpFixPairParser struct{}

func (h *fpFixPairParser) Identifier() string { return "FP_FIX_PAIR" }

func (h *fpFixPairParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	entry, rest, ok := strings.Cut(field, "*")
	if !ok {
		return nil, text, false, nil
	}

	exit, flttp, _ := strings.Cut(rest, "*")

	var spec sim.FlightPlanSpecifier
	if entry != "" {
		spec.EntryFix.Set(entry)
	}
	if exit != "" {
		spec.ExitFix.Set(exit)
	}

	if len(flttp) > 0 {
		switch flttp {
		case "A":
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
		case "P":
			spec.TypeOfFlight.Set(av.FlightTypeDeparture)
		case "E":
			spec.TypeOfFlight.Set(av.FlightTypeOverflight)
		default:
			return nil, text, true, ErrSTARSCommandFormat
		}
	}

	return spec, remaining, true, nil
}

func (h *fpFixPairParser) GoType() reflect.Type { return fpSpecType }
func (h *fpFixPairParser) ConsumesClick() bool  { return false }

type fpExitFixParser struct{}

func (h *fpExitFixParser) Identifier() string { return "FP_EXIT_FIX" }

func (h *fpExitFixParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" || len(field) != 4 || field[0] != '*' {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	spec.ExitFix.Set(field[1:])
	return spec, remaining, true, nil
}

func (h *fpExitFixParser) GoType() reflect.Type { return fpSpecType }
func (h *fpExitFixParser) ConsumesClick() bool  { return false }

type fpFlttypeParser struct{}

func (h *fpFlttypeParser) Identifier() string { return "FP_FLT_TYPE" }

func (h *fpFlttypeParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	switch field {
	case "A":
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
	case "P":
		spec.TypeOfFlight.Set(av.FlightTypeDeparture)
	case "E":
		spec.TypeOfFlight.Set(av.FlightTypeOverflight)
	default:
		return nil, text, false, nil
	}
	return spec, remaining, true, nil
}

func (h *fpFlttypeParser) GoType() reflect.Type { return fpSpecType }
func (h *fpFlttypeParser) ConsumesClick() bool  { return false }

type fpRulesParser struct{}

func (h *fpRulesParser) Identifier() string { return "FP_RULES" }

func (h *fpRulesParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	if !strings.HasPrefix(field, ".") {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	switch strings.TrimPrefix(field, ".") {
	case "V", "P":
		spec.Rules.Set(av.FlightRulesVFR)
	case "E", "":
		spec.Rules.Set(av.FlightRulesIFR)
	default:
		return nil, text, true, ErrSTARSIllegalValue
	}
	return spec, remaining, true, nil
}

func (h *fpRulesParser) GoType() reflect.Type { return fpSpecType }
func (h *fpRulesParser) ConsumesClick() bool  { return false }

type fpRNAVParser struct{}

func (h *fpRNAVParser) Identifier() string { return "FP_RNAV" }

func (h *fpRNAVParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "R" {
		var spec sim.FlightPlanSpecifier
		spec.RNAVToggle.Set(true)
		return spec, remaining, true, nil
	}
	return nil, text, false, nil
}

func (h *fpRNAVParser) GoType() reflect.Type { return fpSpecType }
func (h *fpRNAVParser) ConsumesClick() bool  { return false }

type fpVFRFixesParser struct{}

func (h *fpVFRFixesParser) Identifier() string { return "FP_VFR_FIXES" }

func (h *fpVFRFixesParser) Parse(sp *STARSPane, ctx *panes.Context, input *CommandInput, text string) (any, string, bool, error) {
	field, remaining := util.CutAtSpace(text)
	if field == "" {
		return nil, text, false, nil
	}

	var spec sim.FlightPlanSpecifier
	var entry, exit string
	var isIntermediate bool

	if dep, arr, ok := strings.Cut(field, "*"); ok {
		if len(dep) != 3 || len(arr) < 3 || len(arr) > 4 {
			return nil, text, false, nil
		}
		entry = dep
		isIntermediate = strings.HasSuffix(arr, "*")
		exit = strings.TrimSuffix(arr, "*")
	} else {
		if len(field) != 3 {
			return nil, text, false, nil
		}
		exit = field
	}

	// Validate exit fix is an airport (unless intermediate)
	if !isIntermediate {
		if _, ok := av.DB.Airports[exit]; !ok {
			if _, ok := av.DB.Airports["K"+exit]; !ok {
				return nil, text, false, nil
			}
		}
	}

	if entry != "" {
		spec.EntryFix.Set(entry)
	}
	spec.ExitFix.Set(exit)
	if isIntermediate {
		spec.ExitFixIsIntermediate.Set(true)
	}
	return spec, remaining, true, nil
}

func (h *fpVFRFixesParser) GoType() reflect.Type { return fpSpecType }
func (h *fpVFRFixesParser) ConsumesClick() bool  { return false }

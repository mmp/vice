// aviation/squawk.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

/////////////////////////////////////////////////////////////////////////
// Squawk Codes and SPCs

type Squawk int

func (sq Squawk) String() string { return fmt.Sprintf("%04o", sq) }

// IsDiscrete returns true if the code is a discrete beacon code; the
// non-discrete codes are those ending in "00" (1200, 4000, ...), which are
// assigned to more than one aircraft at a time.
func (sq Squawk) IsDiscrete() bool { return sq&0o77 != 0 }

func ParseSquawk(s string) (Squawk, error) {
	if len(s) != 4 {
		return Squawk(0), ErrInvalidSquawkCode
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil || sq < 0 || sq > 0o7777 {
		return Squawk(0), ErrInvalidSquawkCode
	}
	return Squawk(sq), nil
}

func ParseSquawkOrBlock(s string) (Squawk, error) {
	if len(s) != 4 && len(s) != 2 {
		return Squawk(0), ErrInvalidSquawkCode
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil || sq < 0 || sq > 0o7777 {
		return Squawk(0), ErrInvalidSquawkCode
	}
	return Squawk(sq), nil
}

// SPC (Special Purpose Code) is a unique beacon code,
// indicate an emergency or non-standard operation.
type SPC struct {
	Squawk Squawk
	Code   string
}

var spcs = map[Squawk]string{
	Squawk(0o7400): "LL", // Lost link
	Squawk(0o7500): "HJ", // Hijack/Unlawful Interference
	Squawk(0o7600): "RF", // Communication Failure
	Squawk(0o7700): "EM", // Emergency
	Squawk(0o7777): "MI", // Military interceptor operations
}

func SquawkIsSPC(squawk Squawk) (ok bool, code string) {
	return squawk.IsSPC()
}

// IsSPC returns true if the given squawk code is an SPC.
// The second return value is a string giving the two-letter abbreviated SPC it corresponds to.
func (sq Squawk) IsSPC() (ok bool, code string) {
	code, ok = spcs[sq]
	return
}

func StringIsSPC(code string) bool {
	for scpCode := range maps.Values(spcs) {
		if scpCode == code {
			return true
		}
	}
	return false
}

// FormatAltitude returns an altitude in feet rounded down to the next
// hundred and written the way a controller says it: a flight level at and
// above 18,000', otherwise thousands and hundreds. Altitudes below sea level
// are written with a leading minus sign.

///////////////////////////////////////////////////////////////////////////
// EnrouteSquawkCodePool

type EnrouteSquawkCodePool struct {
	Available *util.IntRangeSet

	// Initial is maintained as a read-only snapshot of the initial set of
	// available codes; it allows us to catch cases where the caller tries
	// to return code that is inside the range we cover but was removed
	// from the pool when it was first initialized.
	Initial *util.IntRangeSet
}

func removeInvalidCodes(codes *util.IntRangeSet) {
	// Remove the non-discrete codes (i.e., ones ending in 00).
	for i := 0; i <= 0o7700; i += 0o100 {
		_ = codes.Take(i)
	}

	takeRange := func(start, end int) {
		for i := start; i <= end; i++ {
			_ = codes.Take(i)
		}
	}
	takeBlock := func(start int) {
		takeRange(start, start+64)
	}

	// Remove various reserved squawk codes, per 7110.66G
	// https://www.faa.gov/documentLibrary/media/Order/FAA_Order_JO_7110.66G_NBCAP.pdf.
	takeBlock(0o1200)
	_ = codes.Take(0o2000)
	takeRange(0o4400, 0o4433)
	takeRange(0o4434, 0o4437)
	takeRange(0o4440, 0o4452)
	_ = codes.Take(0o4453)
	takeRange(0o4454, 0o4477)
	_ = codes.Take(0o7400)
	takeRange(0o7501, 0o7577)
	_ = codes.Take(0o7500)
	_ = codes.Take(0o7600)
	takeRange(0o7601, 0o7607)
	_ = codes.Take(0o7700)
	takeRange(0o7701, 0o7707)
	_ = codes.Take(0o7777)

	// FIXME: these probably shouldn't be hardcoded like this but should be available to PCT.
	takeBlock(0o5100) // PCT TRACON for DC SFRA/FRZ
	takeBlock(0o5200) // PCT TRACON for DC SFRA/FRZ

	takeBlock(0o5000)
	takeBlock(0o5400)
	takeBlock(0o6100)
	takeBlock(0o6400)

	_ = codes.Take(0o7777)
	for squawk := range spcs {
		_ = codes.Take(int(squawk))
	}
}

func MakeEnrouteSquawkCodePool(loc *LocalSquawkCodePool) *EnrouteSquawkCodePool {
	p := &EnrouteSquawkCodePool{
		Initial: util.MakeIntRangeSet(0o1001, 0o7777),
	}

	removeInvalidCodes(p.Initial)

	// Remove codes in the local pool as well
	if loc != nil {
		for _, pool := range loc.Pools {
			for _, rng := range pool.Ranges {
				for sq := rng[0]; sq <= rng[1]; sq++ {
					_ = p.Initial.Take(int(sq))
				}
			}
		}
		for _, r := range loc.BeaconCodeTable.VFRCodes {
			for sq := r[0]; sq <= r[1]; sq++ {
				_ = p.Initial.Take(int(sq))
			}
		}
	}

	p.Available = p.Initial.Clone()

	return p
}

func (p *EnrouteSquawkCodePool) Get(r *rand.Rand) (Squawk, error) {
	code, err := p.Available.GetRandom(r)
	if err != nil {
		return Squawk(0), ErrNoMoreAvailableSquawkCodes
	} else {
		return Squawk(code), nil
	}
}

func (p *EnrouteSquawkCodePool) IsAssigned(code Squawk) bool {
	return !p.Available.IsAvailable(int(code))
}

func (p *EnrouteSquawkCodePool) InInitialPool(code Squawk) bool {
	return p.Initial.IsAvailable(int(code))
}

func (p *EnrouteSquawkCodePool) Return(code Squawk) error {
	if !p.Initial.InRange(int(code)) || !p.Initial.IsAvailable(int(code)) {
		// It's not ours; just ignore it.
		return nil
	}
	if err := p.Available.Return(int(code)); err != nil {
		return ErrSquawkCodeUnassigned
	}
	return nil
}

func (p *EnrouteSquawkCodePool) Take(code Squawk) error {
	if p.IsAssigned(code) {
		return ErrSquawkCodeAlreadyAssigned
	}
	if err := p.Available.Take(int(code)); err != nil {
		return ErrSquawkCodeNotManagedByPool
	}
	return nil
}

func (p *EnrouteSquawkCodePool) NumAvailable() int {
	return p.Available.Count()
}

///////////////////////////////////////////////////////////////////////////
// LocalSquawkCodePool

// SSR Codes Windows
type LocalSquawkCodePoolSpecifier struct {
	Pools           map[string]PoolSpecifier `json:"auto_assignable_codes"`
	BeaconCodeTable BeaconCodeTableSpecifier `json:"beacon_code_table"`
}

type PoolSpecifier struct {
	Ranges  []string `json:"ranges"`
	Rules   string   `json:"flight_rules"`
	Backups string   `json:"backup_pool_list"`
	// TODO: no_flight_plan_exclusion: bool, if true, don't show WHO in DB if it exits the departure filter region.
}

type BeaconCodeTableSpecifier struct {
	VFRCodes []string `json:"vfr_codes"` // Array of squawk code ranges
	// TODO: MSAW
}

// Doesn't return an error since errors are logged to the ErrorLogger
// (which in turn will cause validation to fail if there are any issues...)
func parseCodeRange(s string, e *util.ErrorLogger) [2]Squawk {
	if low, high, ok := strings.Cut(s, "-"); ok {
		// Code range
		slow, err := ParseSquawk(low)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", low)
		}
		shigh, err := ParseSquawk(high)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", high)
		}
		if slow > shigh {
			e.ErrorString("first squawk code %q is greater than second %q", slow, shigh)
		}
		return [2]Squawk{slow, shigh}
	} else {
		// Single code
		sq, err := ParseSquawk(s)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", s)
		}
		return [2]Squawk{sq, sq}
	}
}

// Helper function to parse ranges from an array of strings
func parseCodeRanges(ranges []string, e *util.ErrorLogger) [][2]Squawk {
	var result [][2]Squawk

	// Parse ranges from the array
	for _, r := range ranges {
		result = append(result, parseCodeRange(r, e))
	}

	return result
}

func (s *LocalSquawkCodePoolSpecifier) Finalize(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if len(s.Pools) == 0 {
		return
	} else {
		if vpool, ok := s.Pools["vfr"]; !ok {
			e.ErrorString(`must specify "vfr" squawk pool`)
		} else if vpool.Rules != "" && vpool.Rules != "v" {
			e.ErrorString(`"rules" cannot be specified for the "vfr" pool`)
		}

		if ipool, ok := s.Pools["ifr"]; !ok {
			e.ErrorString(`must specify "ifr" squawk pool`)
		} else if ipool.Rules != "" && ipool.Rules != "i" {
			e.ErrorString(`"rules" cannot be specified for the "ifr" pool`)
		}
		// Numbered ones optional(?)

		// Both check individual pools for valid ranges and internal overlaps and also
		// check for overlaps with previous ranges.
		var allRanges [][][2]Squawk
		for name, spec := range s.Pools {
			e.Push("Code pool " + name)
			if name != "ifr" && name != "vfr" && name != "1" && name != "2" &&
				name != "3" && name != "4" {
				e.ErrorString(`Pool name %q is invalid: must be one of "ifr", "vfr", `+
					`"1", "2", "3", or "4".`, name)
			}

			// Validate input: must provide Ranges
			if len(spec.Ranges) == 0 {
				e.ErrorString(`must specify "ranges" for pool %q`, name)
			}

			// Parse all the ranges for this pool
			poolRanges := parseCodeRanges(spec.Ranges, e)

			// Check for overlaps within this pool
			overlaps := func(a, b [2]Squawk) bool {
				return (a[0] >= b[0] && a[0] <= b[1]) || (a[1] >= b[0] && a[1] <= b[1])
			}
			for i := range poolRanges {
				for j := range i {
					if overlaps(poolRanges[i], poolRanges[j]) {
						e.ErrorString("Range %s-%s overlaps with range %s-%s in the same pool",
							poolRanges[i][0], poolRanges[i][1], poolRanges[j][0], poolRanges[j][1])
					}
				}

				for _, ranges := range allRanges {
					for _, rng := range ranges {
						if overlaps(poolRanges[i], rng) {
							e.ErrorString("Range %s-%s overlaps with range %s-%s in other pool",
								poolRanges[i][0], poolRanges[i][1], rng[0], rng[1])
						}
					}
				}
			}
			allRanges = append(allRanges, poolRanges)

			if spec.Rules != "" && spec.Rules != "i" && spec.Rules != "v" {
				e.ErrorString(`"rules" must be "i" or "v"`)
			}

			for i, ch := range spec.Backups {
				if string(ch) == strings.TrimPrefix(name, "/") {
					e.ErrorString("Can't specify ourself %q as a backup pool", string(ch))
				} else if strings.Contains(spec.Backups[:i], string(ch)) {
					e.ErrorString("Can't repeat the same backup pool %q", string(ch))
				} else if ch < '1' && ch > '4' {
					e.ErrorString(`Backup pools can only contain "1", "2", "3", or "4".`)
				}
			}

			e.Pop()
		}
	}

	e.Push(`"beacon_code_table"`)
	// Validate VFR codes using the same parser
	_ = parseCodeRanges(s.BeaconCodeTable.VFRCodes, e)
	e.Pop()
}

type LocalSquawkCodePool struct {
	Pools           map[string]LocalPool
	BeaconCodeTable BeaconCodeTable
}

type BeaconCodeTable struct {
	VFRCodes [][2]Squawk
	// TODO: MSAW
}

type LocalPool struct {
	Initial     *util.IntRangeSet
	Available   *util.IntRangeSet
	Ranges      [][2]Squawk
	Backups     string
	FlightRules FlightRules
}

func MakeLocalSquawkCodePool(spec LocalSquawkCodePoolSpecifier) *LocalSquawkCodePool {
	// Assume spec has already been validated
	p := &LocalSquawkCodePool{Pools: make(map[string]LocalPool)}
	if len(spec.Pools) == 0 {
		// Return a reasonable default
		p.Pools["vfr"] = LocalPool{
			Initial:     util.MakeIntRangeSet(0o201, 0o277),
			Available:   util.MakeIntRangeSet(0o201, 0o277),
			Ranges:      [][2]Squawk{{0o0201, 0o0277}},
			FlightRules: FlightRulesVFR,
		}
		p.Pools["ifr"] = LocalPool{
			Initial:     util.MakeIntRangeSet(0o301, 0o377),
			Available:   util.MakeIntRangeSet(0o301, 0o377),
			Ranges:      [][2]Squawk{{0o0301, 0o0377}},
			FlightRules: FlightRulesIFR,
		}
	} else {
		for name, pspec := range spec.Pools {
			poolRanges := parseCodeRanges(pspec.Ranges, nil)

			// Find the min and max values to create the IntRangeSet
			r := [2]int{int(poolRanges[0][0]), int(poolRanges[0][1])}
			for _, rng := range poolRanges {
				r[0] = min(r[0], int(rng[0]))
				r[1] = max(r[1], int(rng[1]))
			}

			// Create an IntRangeSet covering the full range
			rs := util.MakeIntRangeSet(r[0], r[1])

			// Remove values that are not in any of the specified ranges
			for sq := r[0]; sq <= r[1]; sq++ {
				if !slices.ContainsFunc(poolRanges, func(r [2]Squawk) bool { return sq >= int(r[0]) && sq <= int(r[1]) }) {
					// sq not in any of the ranges.
					_ = rs.Take(sq)
				}
			}

			p.Pools[name] = LocalPool{
				Initial:   rs.Clone(),
				Available: rs,
				Ranges:    poolRanges,
				Backups:   pspec.Backups,
				FlightRules: func() FlightRules {
					if name == "vfr" || pspec.Rules == "v" {
						return FlightRulesVFR
					}
					return FlightRulesIFR
				}(),
			}
		}
	}

	if len(spec.BeaconCodeTable.VFRCodes) == 0 {
		p.BeaconCodeTable.VFRCodes = [][2]Squawk{{0o1200, 0o1277}}
	} else {
		p.BeaconCodeTable.VFRCodes = parseCodeRanges(spec.BeaconCodeTable.VFRCodes, nil)
	}

	return p
}

func (p *LocalSquawkCodePool) IsReservedVFRCode(sq Squawk) bool {
	for _, r := range p.BeaconCodeTable.VFRCodes {
		if sq >= r[0] && sq <= r[1] {
			return true
		}
	}
	return false
}

// inbound rules are only used to choose a VFR/IFR pool if spec == ""
func (p *LocalSquawkCodePool) Get(spec string, rules FlightRules, r *rand.Rand) (Squawk, FlightRules, error) {
	if spec == "" {
		if rules == FlightRulesIFR {
			spec = "ifr"
		} else {
			spec = "vfr"
		}
	} else if spec == "+" {
		spec = "ifr"
	} else if spec == "/" {
		spec = "vfr"
	} else if len(spec) == 2 && spec[0] == '/' {
		spec = spec[1:]
	}

	if sq, err := ParseSquawk(spec); err == nil && len(spec) == 4 {
		// Remove it from the corresponding pool for auto-assignment. (But
		// it's ok to assign the same code multiple times...)
		for _, pool := range p.Pools {
			for _, rng := range pool.Ranges {
				if sq >= rng[0] && sq <= rng[1] {
					_ = pool.Available.Take(int(sq))
					return sq, pool.FlightRules, nil
				}
			}
		}
		// It's fine if it's not it any of the pools
		return sq, rules, nil
	}

	if pool, ok := p.Pools[spec]; !ok {
		return Squawk(0), FlightRulesUnknown, ErrBadPoolSpecifier
	} else {
		backups := pool.Backups
		rules := pool.FlightRules // initial pool's rules are sticky even if we go to a backup
		for {
			if sq, err := pool.Available.GetRandom(r); err == nil {
				return Squawk(sq), rules, nil
			} else if len(backups) == 0 {
				return Squawk(0), rules, ErrNoMoreAvailableSquawkCodes
			} else {
				pool = p.Pools[string(backups[0])]
				backups = backups[1:]
			}
		}
	}
}

func (p *LocalSquawkCodePool) IsAssigned(code Squawk) bool {
	for _, pool := range p.Pools {
		if pool.Initial.IsAvailable(int(code)) && !pool.Available.IsAvailable(int(code)) {
			return true
		}
	}
	return false
}

func (p *LocalSquawkCodePool) InInitialPool(code Squawk) bool {
	for _, pool := range p.Pools {
		if pool.Initial.IsAvailable(int(code)) {
			return true
		}
	}
	return false
}

func (p *LocalSquawkCodePool) Return(sq Squawk) error {
	for _, pool := range p.Pools {
		if pool.Available.InRange(int(sq)) {
			return pool.Available.Return(int(sq))
		}
	}
	return fmt.Errorf("returned code %s not in any pool's range", sq)
}

///////////////////////////////////////////////////////////////////////////

///////////////////////////////////////////////////////////////////////////
// CWT functions

// cwtApproachSeparation returns the raw CWT approach separation table value.
// If 0 is returned, minimum radar separation should be used.
func cwtApproachSeparation(front, back string) float32 {
	if len(front) != 1 || front[0] < 'A' || front[0] > 'I' {
		return 10
	}
	if len(back) != 1 || back[0] < 'A' || back[0] > 'I' {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-2
	cwtOnApproachLookUp := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 6},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 6}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 6, 6},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtOnApproachLookUp[f][b]
}

// CWTApproachSeparation returns the required approach separation between
// aircraft of the given CWT categories. When the CWT table specifies no
// extra wake turbulence separation (0), minimum radar separation applies:
// 2.5 NM on eligible runways, 3 NM otherwise.
func CWTApproachSeparation(frontCWT, backCWT string, eligible25nm bool) float32 {
	sep := cwtApproachSeparation(frontCWT, backCWT)
	if sep == 0 {
		sep = float32(util.Select(eligible25nm, 2.5, 3))
	}
	return sep
}

// CWTDirectlyBehindSeparation returns the required separation between
// aircraft of the two given CWT categories. If 0 is returned, minimum
// radar separation should be used.
func CWTDirectlyBehindSeparation(front, back string) float32 {
	if len(front) != 1 || front[0] < 'A' || front[0] > 'I' {
		return 10
	}
	if len(back) != 1 || back[0] < 'A' || back[0] > 'I' {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-1
	cwtBehindLookup := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 5}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtBehindLookup[f][b]
}

///////////////////////////////////////////////////////////////////////////

// AircraftClass is a bitmask selecting aircraft for route matching: by engine
// type, or for jets by weight, since heavies are routed differently at some
// facilities. In JSON it is a string or array of strings among "prop",
// "turboprop", "jet", "heavy", and "nonheavy", where "jet" covers both jet
// classes; the zero value admits every aircraft.
type AircraftClass uint8

const (
	AircraftClassProp AircraftClass = 1 << iota
	AircraftClassTurboprop
	AircraftClassHeavyJet
	AircraftClassNonheavyJet
)

var aircraftClassNames = map[string]AircraftClass{
	"prop":      AircraftClassProp,
	"turboprop": AircraftClassTurboprop,
	"jet":       AircraftClassHeavyJet | AircraftClassNonheavyJet,
	"heavy":     AircraftClassHeavyJet,
	"nonheavy":  AircraftClassNonheavyJet,
}

// AircraftClassOf returns the class an aircraft type falls in, or zero if the
// type is unknown.
func AircraftClassOf(db Database, acType string) AircraftClass {
	perf, ok := db.AircraftPerformance(acType)
	if !ok {
		return 0
	}
	switch perf.Engine.AircraftType {
	case "P":
		return AircraftClassProp
	case "T":
		return AircraftClassTurboprop
	case "J":
		if perf.WeightClass == "H" || perf.WeightClass == "J" {
			return AircraftClassHeavyJet
		}
		return AircraftClassNonheavyJet
	}
	return 0
}

// Matches reports whether the aircraft type falls in the class; an
// unrestricted class matches every type and an unknown type matches no
// restricted class.
func (c AircraftClass) Matches(db Database, acType string) bool {
	return c == 0 || c&AircraftClassOf(db, acType) != 0
}

// expand returns the classes the value admits, spelling out the every-aircraft
// meaning of the zero value.
func (c AircraftClass) expand() AircraftClass {
	if c == 0 {
		return AircraftClassProp | AircraftClassTurboprop | AircraftClassHeavyJet | AircraftClassNonheavyJet
	}
	return c
}

// coveredBy reports whether every class the value admits is set in the given
// bitmask of classes, which unlike an AircraftClass means just the ones it has
// set: a zero mask covers nothing.
func (c AircraftClass) coveredBy(classes AircraftClass) bool {
	return c.expand()&^classes == 0
}

func (c *AircraftClass) UnmarshalJSON(b []byte) error {
	var names []string
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		names = []string{s}
	} else if err := json.Unmarshal(b, &names); err != nil {
		return err
	}

	*c = 0
	for _, name := range names {
		class, ok := aircraftClassNames[name]
		if !ok {
			return fmt.Errorf("%q: unknown aircraft class; options: %s", name,
				strings.Join(util.SortedMapKeys(aircraftClassNames), ", "))
		}
		*c |= class
	}
	return nil
}

// names returns the class names the value admits; empty for the
// unrestricted zero value.
func (c AircraftClass) names() []string {
	var names []string
	if c&AircraftClassProp != 0 {
		names = append(names, "prop")
	}
	if c&AircraftClassTurboprop != 0 {
		names = append(names, "turboprop")
	}
	switch c & (AircraftClassHeavyJet | AircraftClassNonheavyJet) {
	case AircraftClassHeavyJet | AircraftClassNonheavyJet:
		names = append(names, "jet")
	case AircraftClassHeavyJet:
		names = append(names, "heavy")
	case AircraftClassNonheavyJet:
		names = append(names, "nonheavy")
	}
	return names
}

func (c AircraftClass) String() string {
	return strings.Join(c.names(), ", ")
}

func (c AircraftClass) MarshalJSON() ([]byte, error) {
	names := c.names()
	if len(names) == 1 {
		return json.Marshal(names[0])
	}
	return json.Marshal(names)
}

// CheckJSON implements util.JSONChecker; class values are validated during
// unmarshaling.
func (c *AircraftClass) CheckJSON(json any) bool {
	return util.TypeCheckJSON[string](json) || util.TypeCheckJSON[[]string](json)
}

// SplitCallsign splits a callsign into ICAO prefix and flight number.
// For "UAL123" returns ("UAL", "123"). For "N12345" returns ("N", "12345").
func SplitCallsign(callsign string) (prefix, number string) {
	if idx := strings.IndexAny(callsign, "0123456789"); idx != -1 {
		return callsign[:idx], callsign[idx:]
	}
	return callsign, ""
}

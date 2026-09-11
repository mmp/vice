// pkg/aviation/aviation_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

func TestFrequencyFormat(t *testing.T) {
	type FS struct {
		f Frequency
		s string
	}

	for _, fs := range []FS{{f: Frequency(121900), s: "121.900"},
		{f: Frequency(130050), s: "130.050"},
		{f: Frequency(128000), s: "128.000"},
	} {
		if fs.f.String() != fs.s {
			t.Errorf("Frequency String() %q; expected %q", fs.f.String(), fs.s)
		}
	}
}

func TestFrequencySpoken(t *testing.T) {
	// Each frequency should give exactly these spoken forms, in particular
	// keeping the leading zero of fractions below .10.
	for _, fs := range []struct {
		f       Frequency
		spokens []string
	}{
		{f: Frequency(127050), spokens: []string{"27 zero five", "one 27 point zero five",
			"27 point zero five", "one two seven point zero five"}},
		{f: Frequency(118075), spokens: []string{"18 zero seven", "one 18 point zero seven",
			"18 point zero seven", "one one eight point zero seven"}},
		{f: Frequency(121900), spokens: []string{"21 90", "one 21 point 9", "21 point 9",
			"one two one point niner"}},
		{f: Frequency(133450), spokens: []string{"33 45", "one 33 point 45", "33 point 45",
			"one three three point four five"}},
		{f: Frequency(128000), spokens: []string{"28 zero", "one 28 point zero", "28 point zero",
			"one two eight point zero"}},
	} {
		r := rand.Make()
		var got []string
		for seed := range 100 {
			r.Seed(uint64(seed))
			if s := (FrequencySnippetFormatter{}).Spoken(r, fs.f); !slices.Contains(got, s) {
				got = append(got, s)
			}
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(fs.spokens))
		if !slices.Equal(got, want) {
			t.Errorf("Frequency %s spoken forms %q; expected %q", fs.f, got, want)
		}
	}
}

func TestParseSquawk(t *testing.T) {
	for _, squawk := range []string{"", "1", "11111", "7778", "0801", "9000"} {
		if _, err := ParseSquawk(squawk); err == nil {
			t.Errorf("Expected error return value for invalid squawk %q", squawk)
		}
		if _, err := ParseSquawkOrBlock(squawk); err == nil {
			t.Errorf("Expected error return value for invalid squawk %q", squawk)
		}
	}

	for _, squawk := range []string{"12", "76"} {
		if _, err := ParseSquawk(squawk); err == nil {
			t.Errorf("Expected error return value for invalid squawk %q", squawk)
		}
		if _, err := ParseSquawkOrBlock(squawk); err != nil {
			t.Errorf("Unexpected error return value for squawk block %q", squawk)
		}
	}

	for _, squawk := range []string{"0601", "3700", "7777", "0000", "1724"} {
		if ps, err := ParseSquawk(squawk); err != nil {
			t.Errorf("%v: Unexpected error return value for valid squawk %q", err, squawk)
		} else if ps.String() != squawk {
			t.Errorf("Parsing squawk %s doesn't give match from String(): %s", squawk, ps.String())
		}
	}
}

func TestParseAltitudeRestriction(t *testing.T) {
	type testcase struct {
		s  string
		ar AltitudeRestriction
	}
	for _, test := range []testcase{
		{s: "1000", ar: MakeAtAltitudeRestriction(1000)},
		{s: "3000-5000", ar: MakeRangeAltitudeRestriction(3000, 5000)},
		{s: "7000+", ar: MakeAtOrAboveAltitudeRestriction(7000)},
		{s: "9000-", ar: MakeAtOrBelowAltitudeRestriction(9000)},
	} {
		ar, err := ParseAltitudeRestriction(test.s)
		if err != nil {
			t.Errorf("%s: unexpected error parsing: %v", test.s, err)
		}
		if ar.Range[0] != test.ar.Range[0] || ar.Range[1] != test.ar.Range[1] {
			t.Errorf("%s: got range %v, expected %v", test.s, ar, test.ar)
		}
		if enc := ar.Encoded(); enc != test.s {
			t.Errorf("encoding mismatch: got %q, expected %q", enc, test.s)
		}
	}
}

func TestSquawkCodePoolBasics(t *testing.T) {
	p := MakeEnrouteSquawkCodePool(nil)

	r := rand.Make()
	sq, err := p.Get(r)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if !p.IsAssigned(sq) {
		t.Errorf("squawk not reported as assigned")
	}

	if err := p.Return(sq); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if p.IsAssigned(sq) {
		t.Errorf("unused squawk reported as assigned")
	}

	if err := p.Take(sq); err != nil {
		t.Errorf("unable to take unassigned code")
	}

	if !p.IsAssigned(sq) {
		t.Errorf("squawk not reported as assigned")
	}
}

func TestSquawkCodePoolRandoms(t *testing.T) {
	p := MakeEnrouteSquawkCodePool(nil)
	assigned := make(map[Squawk]any)

	r := rand.Make()
	for i := range 100000 {
		sq, err := p.Get(r)
		if err != nil && p.NumAvailable() > 0 {
			t.Errorf("unexpected error: %v", err)
		} else if _, ok := assigned[sq]; ok {
			t.Errorf("%s: squawk code assigned more than once", sq)
		} else {
			assigned[sq] = nil
		}

		if i%100 == 0 {
			// Exhaustive check, only do it occasionally.
			for sq := range assigned {
				if !p.IsAssigned(sq) {
					t.Errorf("%s: assigned squawk reported as unassigned", sq)
				}
			}
		}

		avail := p.NumAvailable()
		if r.Float32() < .4 || avail == 0 {
			// return one of ours
			for sq = range assigned {
				delete(assigned, sq)
				break
			}
			p.Return(sq)

			if p.NumAvailable() != avail+1 {
				t.Errorf("didn't report another one available?")
			}
		}
	}
}

func TestCwtApproachSeparationTable(t *testing.T) {
	type testcase struct {
		front, back string
		expect      float32
	}
	for _, tc := range []testcase{
		{front: "B", back: "G", expect: 5},
		{front: "NOWGT", back: "G", expect: 10},
		{front: "A", back: "D", expect: 6},
		{front: "C", back: "D", expect: 0},
		{front: "C", back: "E", expect: 3.5},
		{front: "D", back: "B", expect: 3},
		{front: "E", back: "H", expect: 0},
		{front: "E", back: "I", expect: 4},
	} {
		if s := cwtApproachSeparation(tc.front, tc.back); s != tc.expect {
			t.Errorf("cwtApproachSeparation(%q, %q) = %f. Expected %f", tc.front, tc.back, s, tc.expect)
		}
	}
}

func TestDirectlyBehindCWTSeparation(t *testing.T) {
	type testcase struct {
		front, back string
		expect      float32
	}
	for _, tc := range []testcase{
		{front: "B", back: "G", expect: 5},
		{front: "B", back: "I", expect: 5},
		{front: "NOWGT", back: "G", expect: 10},
		{front: "A", back: "D", expect: 6},
		{front: "C", back: "D", expect: 0},
		{front: "C", back: "E", expect: 3.5},
		{front: "D", back: "B", expect: 3},
		{front: "E", back: "H", expect: 0},
		{front: "E", back: "I", expect: 4},
		{front: "F", back: "I", expect: 0},
	} {
		if s := CWTDirectlyBehindSeparation(tc.front, tc.back); s != tc.expect {
			t.Errorf("CWTDirectlyBehindSeparation(%q, %q) = %f. Expected %f", tc.front, tc.back, s, tc.expect)
		}
	}
}

func TestCWTApproachSeparation(t *testing.T) {
	type testcase struct {
		front, back  string
		eligible25nm bool
		expect       float32
	}
	for _, tc := range []testcase{
		// Table lookup value, no 2.5nm
		{front: "B", back: "G", eligible25nm: false, expect: 5},
		// Zero from table → defaults to 3
		{front: "G", back: "G", eligible25nm: false, expect: 3},
		// Zero from table, eligible → 2.5
		{front: "G", back: "G", eligible25nm: true, expect: 2.5},
		{front: "E", back: "A", eligible25nm: true, expect: 2.5},
		// Bug fix cases: these have 0 in the CWT matrix, so eligible → 2.5
		{front: "F", back: "G", eligible25nm: true, expect: 2.5},
		{front: "F", back: "H", eligible25nm: true, expect: 2.5},
		{front: "C", back: "B", eligible25nm: true, expect: 2.5},
		// Same pairs without eligibility → 3
		{front: "F", back: "G", eligible25nm: false, expect: 3},
		{front: "F", back: "H", eligible25nm: false, expect: 3},
		{front: "C", back: "B", eligible25nm: false, expect: 3},
		// Non-zero table values are unaffected by eligibility
		{front: "A", back: "I", eligible25nm: true, expect: 8},
		{front: "D", back: "I", eligible25nm: true, expect: 6},
		// Not eligible
		{front: "E", back: "A", eligible25nm: false, expect: 3},
	} {
		if got := CWTApproachSeparation(tc.front, tc.back, tc.eligible25nm); got != tc.expect {
			t.Errorf("CWTApproachSeparation(%q, %q, %v) = %v, expected %v",
				tc.front, tc.back, tc.eligible25nm, got, tc.expect)
		}
	}
}

func TestLocalSquawkCodePool(t *testing.T) {
	spec := LocalSquawkCodePoolSpecifier{
		Pools: map[string]PoolSpecifier{
			"ifr": {
				Ranges:  []string{"0101-0177"},
				Backups: "1",
			},
			"vfr": {
				Ranges:  []string{"0201-0277"},
				Backups: "2",
			},
			"1": {
				Ranges:  []string{"0301-0377"},
				Backups: "234",
			},
			"2": {
				Ranges:  []string{"0401-0477"},
				Backups: "341",
			},
			"3": {
				Ranges: []string{"1602"},
			},
			"4": {
				Ranges:  []string{"0501-0577"},
				Backups: "12",
			},
		},
		BeaconCodeTable: BeaconCodeTableSpecifier{
			VFRCodes: []string{"1200-1202", "1204"},
		},
	}

	var e util.ErrorLogger
	spec.PostDeserialize(&e)
	if e.HaveErrors() {
		t.Fatalf("Validation errors: %s", e.String())
	}

	pool := MakeLocalSquawkCodePool(spec)

	r := rand.Make()
	seen := make(map[Squawk]any)
	get := func(spec string, rules FlightRules) Squawk {
		sq, _, err := pool.Get(spec, rules, r)
		if err != nil {
			t.Errorf("+: %s", err)
		}
		if _, ok := seen[sq]; ok {
			t.Errorf("%s has been returned twice", sq)
		}
		seen[sq] = nil
		return sq
	}

	if c := get("+", FlightRulesIFR); c < 0o0101 || c > 0o0177 {
		t.Errorf("unexpected code %s", c)
	}
	if c := get("/", FlightRulesVFR); c < 0o0201 || c > 0o0277 {
		t.Errorf("unexpected code %s", c)
	}
	if c := get("/3", FlightRulesUnknown); c != 0o1602 {
		t.Errorf("unexpected code %s", c)
	}
	if _, _, err := pool.Get("/3", FlightRulesUnknown, r); err == nil {
		t.Errorf("didn't get expected error from empty pool")
	}

	// Exhaust the IFR pool and make sure we go to pool 1 next. There are
	// 63 codes and one has been taken, so take 62 more now.
	for range 62 {
		if c := get("+", FlightRulesIFR); c < 0o0101 || c > 0o0177 {
			t.Errorf("unexpected code %s", c)
		}
	}
	if c := get("+", FlightRulesIFR); c < 0o0301 || c > 0o0377 { // should go to the backup
		t.Errorf("unexpected code %s", c)
	}

	// Keep taking from IFR / pool 1 until pool 1 is exhausted. Once 1 is exhausted, IFR should report an error since it only has a single backup pool, pool 1.
	// We've only taken 1 from pool 1, so here we go...
	for range 31 {
		if c := get("+", FlightRulesIFR); c < 0o0301 || c > 0o0377 {
			t.Errorf("unexpected code %s", c)
		}
		if c := get("/1", FlightRulesIFR); c < 0o0301 || c > 0o0377 {
			t.Errorf("unexpected code %s", c)
		}
	}

	// Now IFR should fail and going to pool 1 directly should go to pool 2.
	if _, _, err := pool.Get("ifr", FlightRulesIFR, r); err == nil {
		t.Errorf("didn't get expected error from empty pool")
	}
	if c := get("/1", FlightRulesIFR); c < 0o0401 || c > 0o0477 {
		t.Errorf("unexpected code %s", c)
	}
}

// An arrival that spells out its waypoints rather than naming a STAR to take
// them from is still recognizably on one, so long as it flies the STAR's own
// legs and the STARs into the airport haven't converged by the time it starts.
func TestDeriveSTAR(t *testing.T) {
	star := func(fixes ...string) STAR {
		var wps WaypointArray
		for _, f := range fixes {
			wps = append(wps, Waypoint{Fix: f})
		}
		return STAR{Transitions: map[string]WaypointArray{"ALL": wps}}
	}

	// Two STARs that come in from different directions and merge for the last
	// two fixes, as the STARs into an airport tend to; MIPP4 also has a runway
	// transition off the end of it.
	mipp4 := star("MIPP", "LIZZI", "BEUTY", "APPLE", "PROUD")
	mipp4.RunwayWaypoints = map[string]WaypointArray{
		"13": star("PROUD", "KRANN", "CRADL", "ETHYN").Transitions["ALL"],
	}

	oldDB := DB
	DB = &StaticDatabase{Airports: map[ICAOAirportCode]FAAAirport{
		"KTST": {Id: "KTST", STARs: map[string]STAR{
			"MIPP4":  mipp4,
			"PROUD2": star("HOLEY", "BRAND", "KORRY", "APPLE", "PROUD"),
		}},
		"KNOS": {Id: "KNOS"},
	}}
	t.Cleanup(func() { DB = oldDB })

	arrival := func(airport ICAOAirportCode, fixes ...string) *Arrival {
		ar := &Arrival{Airports: []ICAOAirportCode{airport}}
		for _, f := range fixes {
			ar.Waypoints = append(ar.Waypoints, Waypoint{Fix: f})
		}
		return ar
	}

	for _, tc := range []struct {
		name string
		arr  *Arrival
		want string
	}{
		{
			name: "runs along one of the STARs",
			arr:  arrival("KTST", "LIZZI", "BEUTY", "APPLE", "PROUD"),
			want: "MIPP4",
		},
		{
			name: "carries on past the end of the STAR",
			arr:  arrival("KTST", "_handoff", "HOLEY", "BRAND", "KORRY", "APPLE", "PROUD", "RWY13"),
			want: "PROUD2",
		},
		{
			name: "one leg and hardly anything else",
			arr:  arrival("KTST", "BEUTY", "APPLE"),
			want: "MIPP4",
		},
		{
			name: "one leg of a route that is mostly elsewhere",
			arr:  arrival("KTST", "OWNWY", "BEUTY", "APPLE", "ELSEW", "RWY13"),
			want: "",
		},
		{
			name: "joins the STAR at one fix and leaves again",
			arr:  arrival("KTST", "LIZZI", "OWNWY", "RWY13"),
			want: "",
		},
		{
			name: "crosses one fix of a STAR it doesn't start on",
			arr:  arrival("KTST", "OWNWY", "KORRY", "RWY13"),
			want: "",
		},
		{
			// The shape of an arrival down an airway to a STAR's last fix: the
			// two fixes it has in common are a leg of the STAR, but it flies
			// its own way between them.
			name: "crosses two fixes of a STAR without flying the leg",
			arr:  arrival("KTST", "BEUTY", "OWNWY", "APPLE", "RWY13"),
			want: "",
		},
		{
			name: "flies only the runway transition off the end",
			arr:  arrival("KTST", "KRANN", "CRADL", "ETHYN"),
			want: "MIPP4",
		},
		{
			name: "only on the part the STARs share",
			arr:  arrival("KTST", "APPLE", "PROUD"),
			want: "",
		},
		{
			name: "one fix in common is a coincidence",
			arr:  arrival("KTST", "PROUD", "ELSEW"),
			want: "",
		},
		{
			name: "airport has no STARs",
			arr:  arrival("KNOS", "LIZZI", "BEUTY", "APPLE", "PROUD"),
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.arr.deriveSTAR(); got != tc.want {
				t.Errorf("deriveSTAR = %q, want %q", got, tc.want)
			}
		})
	}
}

// An arrival that names none of the airports it serves takes them from the FAA
// CIFP's entry for its STAR, and must name them itself if that isn't an option.
func TestArrivalAirports(t *testing.T) {
	fixes := []string{"MIPP", "LIZZI", "BEUTY", "APPLE", "PROUD"}
	var wps WaypointArray
	loc := testLocator{}
	dbFixes := make(map[string]Fix)
	for i, f := range fixes {
		p := math.Point2LL{float32(-73 - i), float32(40 + i)}
		wps = append(wps, Waypoint{Fix: f})
		loc[f] = p
		dbFixes[f] = Fix{Id: f, Location: p}
	}
	mipp4 := STAR{Transitions: map[string]WaypointArray{"ALL": wps}}

	oldDB := DB
	// MIPP4 serves two of the three airports; the CIFP records it once under
	// each of them.
	DB = &StaticDatabase{
		Airports: map[ICAOAirportCode]FAAAirport{
			"KTST": {Id: "KTST", STARs: map[string]STAR{"MIPP4": mipp4}},
			"KNOS": {Id: "KNOS", STARs: map[string]STAR{"MIPP4": mipp4}},
			"KOTH": {Id: "KOTH"},
		},
		Fixes: dbFixes,
	}
	t.Cleanup(func() { DB = oldDB })

	scenarioAirports := map[ICAOAirportCode]*Airport{"KTST": {}, "KNOS": {}, "KOTH": {}}
	controlPositions := map[ControlPosition]*Controller{"1T": {}}

	for _, tc := range []struct {
		name string
		arr  Arrival
		want []ICAOAirportCode
		err  string
	}{
		{
			name: "takes the airports from the STAR",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP"},
			want: []ICAOAirportCode{"KNOS", "KTST"},
		},
		{
			name: "airports given win over the STAR's",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []ICAOAirportCode{"KTST"}},
			want: []ICAOAirportCode{"KTST"},
		},
		{
			name: "airports given are sorted",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []ICAOAirportCode{"KTST", "KNOS"}},
			want: []ICAOAirportCode{"KNOS", "KTST"},
		},
		{
			name: "airlines don't imply the airports",
			arr: Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP",
				Airlines: map[ICAOAirportCode][]ArrivalAirline{"KNOS": nil}},
			want: []ICAOAirportCode{"KNOS", "KTST"},
		},
		{
			name: "airlines into an airport the arrival doesn't serve",
			arr: Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []ICAOAirportCode{"KTST"},
				Airlines: map[ICAOAirportCode][]ArrivalAirline{"KNOS": nil}},
			err: `"airlines" gives airlines into "KNOS"`,
		},
		{
			name: "an airport the scenario hasn't got",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []ICAOAirportCode{"KNIL"}},
			err:  `arrival airport "KNIL" unknown`,
		},
		{
			name: "must name the airports without a STAR to go on",
			arr:  Arrival{FlightStripDisplayRoute: "MIPP LIZZI", Waypoints: wps},
			err:  `must name the airports the arrival serves`,
		},
		{
			name: "a STAR the CIFP hasn't got is no help either",
			arr:  Arrival{STAR: "NOPE1", Waypoints: wps},
			err:  `STAR "NOPE1" isn't charted`,
		},
		{
			name: "spelling out the waypoints doesn't excuse an uncharted STAR",
			arr:  Arrival{STAR: "MIPP4", Waypoints: wps, Airports: []ICAOAirportCode{"KOTH"}},
			err:  `"star" "MIPP4" isn't charted for any of the airports the arrival serves: KOTH`,
		},
		{
			name: "one of the airports having the STAR is enough",
			arr:  Arrival{STAR: "MIPP4", Waypoints: wps, Airports: []ICAOAirportCode{"KOTH", "KTST"}},
			want: []ICAOAirportCode{"KOTH", "KTST"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e util.ErrorLogger
			arr := tc.arr
			arr.InitialController = "1T"
			arr.InitialAltitudes = []int{10000}
			arr.InitialSpeed = MakeIAS(250)
			arr.PostDeserialize(loc, 45, 0, scenarioAirports, controlPositions,
				func(string) bool { return true }, &e)

			if tc.err != "" {
				if !strings.Contains(e.String(), tc.err) {
					t.Errorf("expected an error matching %q; got: %s", tc.err, e.String())
				}
			} else if !slices.Equal(arr.Airports, tc.want) {
				t.Errorf("Airports = %v, want %v (errors: %s)", arr.Airports, tc.want, e.String())
			}
		})
	}

	// An arrival that takes its waypoints from a STAR no airport in the
	// scenario is charted for can't be built at all, and says so once rather
	// than once per airport.
	t.Run("no waypoints and an uncharted STAR", func(t *testing.T) {
		var e util.ErrorLogger
		arr := Arrival{STAR: "NOPE1", SpawnWaypoint: "MIPP", InitialController: "1T",
			InitialAltitudes: []int{10000}, InitialSpeed: MakeIAS(250)}
		arr.PostDeserialize(loc, 45, 0, scenarioAirports, controlPositions,
			func(string) bool { return true }, &e)

		if !strings.Contains(e.String(), `STAR "NOPE1" isn't charted`) {
			t.Errorf("didn't get the expected error; got: %s", e.String())
		}
		if n := strings.Count(e.String(), "isn't charted"); n != 1 {
			t.Errorf("reported the uncharted STAR %d times, want 1: %s", n, e.String())
		}
	})
}

// TestArrivalApproachRoutes covers the pairing behind the /clearapp and
// /intercept checks: which approaches an arrival's aircraft may be cleared for,
// and the route each of them is flying when the clearance comes.
func TestArrivalApproachRoutes(t *testing.T) {
	i15r := &Approach{FullName: "ILS Runway 15R", Runway: "15R"}
	rz15r := &Approach{FullName: "RNAV Z Runway 15R", Runway: "15R"}
	i33l := &Approach{FullName: "ILS Runway 33L", Runway: "33L"}
	i10 := &Approach{FullName: "ILS Runway 10", Runway: "10"}
	ap := &Airport{Approaches: map[string]*Approach{"I5R": i15r, "RZ5R": rz15r, "I3L": i33l, "I10": i10}}

	id := "I5R"
	arr := Arrival{
		Airports:       []ICAOAirportCode{"KBWI"},
		ExpectApproach: util.OneOf[string, map[ICAOAirportCode]string]{A: &id},
		Waypoints:      WaypointArray{{Fix: "RAVNN"}, {Fix: "CAPKO"}},
		RunwayWaypoints: map[ICAOAirportCode]map[string]WaypointArray{
			"KBWI": {
				"15R": {{Fix: "CAPKO"}, {Fix: "ZARTZ"}},
				"33L": {{Fix: "CAPKO"}, {Fix: "KOOLZ"}},
			},
		},
	}

	// The expected approach comes first, then the rest of what a controller
	// could send them to given the runways the arrival has waypoints for.
	// ILS 10 isn't one of them.
	want := []*Approach{i15r, rz15r, i33l}
	if got := arr.joinableApproaches(ap, "KBWI"); !slices.Equal(got, want) {
		t.Errorf("joinableApproaches = %v, want %v",
			util.MapSlice(got, func(a *Approach) string { return a.FullName }),
			util.MapSlice(want, func(a *Approach) string { return a.FullName }))
	}

	for _, tc := range []struct {
		appr *Approach
		want []string
	}{
		{appr: i15r, want: []string{"RAVNN", "CAPKO", "ZARTZ"}},
		{appr: i33l, want: []string{"RAVNN", "CAPKO", "KOOLZ"}},
		// No waypoints for runway 10, so they stay on the arrival's own route.
		{appr: i10, want: []string{"RAVNN", "CAPKO"}},
	} {
		t.Run(tc.appr.FullName, func(t *testing.T) {
			got := util.MapSlice(arr.approachRoute("KBWI", tc.appr),
				func(wp Waypoint) string { return wp.Fix })
			if !slices.Equal(got, tc.want) {
				t.Errorf("approachRoute = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestArrivalApproachRouteCarriesSharedFixActions covers the fix where the
// runway waypoints take over from the arrival's own: the runway waypoint's
// copy of it carries none of the arrival's instructions, so the splice has to
// bring them across the way ExpectApproach does at runtime.
func TestArrivalApproachRouteCarriesSharedFixActions(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("RAVNN CAPKO/clearapp/intercept/nopt")
	if err != nil {
		t.Fatal(err)
	}
	arr := Arrival{
		Waypoints: wps,
		RunwayWaypoints: map[ICAOAirportCode]map[string]WaypointArray{
			"KBWI": {"15R": {{Fix: "CAPKO"}, {Fix: "ZARTZ"}}},
		},
	}

	route := arr.approachRoute("KBWI", &Approach{Runway: "15R"})
	shared := len(arr.Waypoints) - 1
	if route[shared].Fix != "CAPKO" {
		t.Fatalf("expected the runway waypoints to take over at CAPKO, got %q", route[shared].Fix)
	}
	if !slices.ContainsFunc(route[shared].ActionGroups(),
		func(g WaypointActionGroup) bool { return g.Actions.ClearApproach }) {
		t.Error("the shared fix's /clearapp didn't come across")
	}
	if !route[shared].HasInterceptApproachAction() {
		t.Error("the shared fix's /intercept didn't come across")
	}
	if !route[shared].NoPT() {
		t.Error("the shared fix's /nopt didn't come across")
	}
}

// TestArrivalWaypointActions covers "waypoint_actions" on an arrival that
// takes its route from the CIFP: where the actions land, what is rejected,
// and that the STAR in the database is left as it was.
func TestArrivalWaypointActions(t *testing.T) {
	fixes := []string{"MIPP", "LIZZI", "BEUTY", "APPLE", "PROUD", "KRANN", "ETHYN", "SNEDE", "XYZ"}
	loc := declinationLocator{testLocator{}, map[string]float32{"XYZ": 11}}
	dbFixes := make(map[string]Fix)
	for i, f := range fixes {
		p := math.Point2LL{float32(-73 - i), float32(40 + i)}
		loc.testLocator[f] = p
		dbFixes[f] = Fix{Id: f, Location: p}
	}
	oldDB := DB
	DB = &StaticDatabase{Fixes: dbFixes, Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	route := func(s string) WaypointArray {
		wps, err := parseWaypoints(s)
		if err != nil {
			t.Fatal(err)
		}
		return wps
	}

	mipp4 := STAR{
		Transitions:     map[string]WaypointArray{"ALL": route("MIPP LIZZI BEUTY APPLE PROUD")},
		RunwayWaypoints: map[string]WaypointArray{"13": route("PROUD KRANN ETHYN"), "31": route("PROUD SNEDE")},
	}
	DB.Airports = map[ICAOAirportCode]FAAAirport{
		"KTST": {Id: "KTST", STARs: map[string]STAR{"MIPP4": mipp4}, Runways: []Runway{{Id: "13"}, {Id: "31"}}},
	}

	scenarioAirports := map[ICAOAirportCode]*Airport{"KTST": {}}
	controlPositions := map[ControlPosition]*Controller{"1T": {Position: "1T"}, "C35": {Position: "C35"}}

	const baseline = "MIPP/star _handoff/ho/star LIZZI/star BEUTY/star APPLE/star PROUD/star"
	for _, tc := range []struct {
		name    string
		actions map[string]string
		want    string // encoded route; "" to keep the baseline
		want13  string // encoded runway 13 transition; "" for the STAR's own
		want31  string
		err     string
	}{
		{
			name: "no actions gets the automatic handoff",
			want: baseline,
		},
		{
			name:    "a handoff of its own replaces the automatic one",
			actions: map[string]string{"BEUTY": "ho"},
			want:    "MIPP/star LIZZI/star BEUTY/ho/star APPLE/star PROUD/star",
		},
		{
			name:    "handing off to a virtual controller also replaces it",
			actions: map[string]string{"LIZZI": "hoC35"},
			want:    "MIPP/star LIZZI/hoC35/star BEUTY/star APPLE/star PROUD/star",
		},
		{
			name:    "several actions at a fix",
			actions: map[string]string{"BEUTY": "ho,spspABC"},
			want:    "MIPP/star LIZZI/star BEUTY/ho/spspABC/star APPLE/star PROUD/star",
		},
		{
			name:    "an action on a fix past the runway split",
			actions: map[string]string{"KRANN": "h090"},
			want13:  "PROUD/star KRANN/h090/star ETHYN/star",
		},
		{
			name:    "an action where the runway transitions branch off goes on each",
			actions: map[string]string{"PROUD": "ho"},
			want:    "MIPP/star LIZZI/star BEUTY/star APPLE/star PROUD/ho/star",
			want13:  "PROUD/ho/star KRANN/star ETHYN/star",
			want31:  "PROUD/ho/star SNEDE/star",
		},
		{
			name:    "a fix on no route at all",
			actions: map[string]string{"NOPE": "ho"},
			err:     "NOPE is not in the route",
		},
		{
			name:    "a property is not an action",
			actions: map[string]string{"BEUTY": "flyover"},
			err:     "unknown action",
		},
		{
			name:    "/delete is",
			actions: map[string]string{"PROUD": "delete"},
			want:    "MIPP/star _handoff/ho/star LIZZI/star BEUTY/star APPLE/star PROUD/delete/star",
			want13:  "PROUD/delete/star KRANN/star ETHYN/star",
			want31:  "PROUD/delete/star SNEDE/star",
		},
		{
			name:    "a trigger the fix hasn't got",
			actions: map[string]string{"BEUTY/@a5000+": "ho"},
			err:     "no trigger /@a5000+",
		},
		{
			name:    "an unknown controller on the arrival's own route",
			actions: map[string]string{"BEUTY": "hoZZZ"},
			err:     "No controller found with id",
		},
		{
			name:    "an unknown controller past the runway split",
			actions: map[string]string{"KRANN": "hoZZZ"},
			err:     "No controller found with id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e util.ErrorLogger
			arr := Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", WaypointActions: tc.actions,
				InitialController: "1T", InitialAltitudes: []int{10000}, InitialSpeed: MakeIAS(250)}
			arr.PostDeserialize(loc, 45, 0, scenarioAirports, controlPositions,
				func(string) bool { return true }, &e)

			if tc.err != "" {
				if !strings.Contains(e.String(), tc.err) {
					t.Errorf("expected an error matching %q; got: %s", tc.err, e.String())
				}
				return
			}
			if e.HaveErrors() {
				t.Fatalf("unexpected errors: %s", e.String())
			}

			want := util.Select(tc.want == "", baseline, tc.want)
			if got := arr.Waypoints.Encode(); got != want {
				t.Errorf("route = %q, want %q", got, want)
			}
			for rwy, want := range map[string]string{
				"13": util.Select(tc.want13 == "", "PROUD/star KRANN/star ETHYN/star", tc.want13),
				"31": util.Select(tc.want31 == "", "PROUD/star SNEDE/star", tc.want31),
			} {
				if got := arr.RunwayWaypoints["KTST"][rwy].Encode(); got != want {
					t.Errorf("runway %s = %q, want %q", rwy, got, want)
				}
			}
		})
	}

	// A heading action that tracks a navaid's radial has to be located along
	// with the rest of the route, so the actions go on before the locations
	// are resolved.
	t.Run("an action naming a fix of its own is located", func(t *testing.T) {
		var e util.ErrorLogger
		arr := Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP",
			WaypointActions:   map[string]string{"APPLE": "tXYZ-R090", "KRANN": "tXYZ-R090"},
			InitialController: "1T", InitialAltitudes: []int{10000}, InitialSpeed: MakeIAS(250)}
		arr.PostDeserialize(loc, 45, 0, scenarioAirports, controlPositions,
			func(string) bool { return true }, &e)
		if e.HaveErrors() {
			t.Fatalf("unexpected errors: %s", e.String())
		}

		for fix, wps := range map[string]WaypointArray{
			"APPLE": arr.Waypoints,
			"KRANN": arr.RunwayWaypoints["KTST"]["13"],
		} {
			i := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.Fix == fix })
			if i == -1 {
				t.Fatalf("%s not in %s", fix, wps.Encode())
			}
			h := wps[i].ActionGroups()[0].Actions.Heading
			if h.FixLocation != loc.testLocator["XYZ"] {
				t.Errorf("%s: radial fix at %v, want %v", fix, h.FixLocation, loc.testLocator["XYZ"])
			}
			if h.FixVariation != 11 {
				t.Errorf("%s: radial variation %v, want the station's 11", fix, h.FixVariation)
			}
		}
	})

	// Actions are added to a copy of the CIFP's route: writing through to the
	// database would give every other arrival on the STAR the same ones.
	t.Run("the STAR in the database is untouched", func(t *testing.T) {
		star := DB.Airports["KTST"].STARs["MIPP4"]
		for _, wps := range []WaypointArray{star.Transitions["ALL"], star.RunwayWaypoints["13"],
			star.RunwayWaypoints["31"]} {
			for _, wp := range wps {
				if len(wp.ActionGroups()) > 0 {
					t.Errorf("%s picked up %s", wp.Fix, WaypointArray{wp}.Encode())
				}
			}
		}
	})

	t.Run("not allowed with waypoints of its own", func(t *testing.T) {
		var e util.ErrorLogger
		arr := Arrival{STAR: "MIPP4", Waypoints: route("MIPP LIZZI BEUTY APPLE PROUD"),
			Airports: []ICAOAirportCode{"KTST"}, WaypointActions: map[string]string{"BEUTY": "ho"},
			InitialController: "1T", InitialAltitudes: []int{10000}, InitialSpeed: MakeIAS(250)}
		arr.PostDeserialize(loc, 45, 0, scenarioAirports, controlPositions,
			func(string) bool { return true }, &e)

		if !strings.Contains(e.String(), "applies only to a route taken from the CIFP") {
			t.Errorf("didn't get the expected error; got: %s", e.String())
		}
	})
}

func TestFormatAltitude(t *testing.T) {
	for _, tc := range []struct {
		alt  float32
		want string
	}{
		{-1000, "-1,000"},
		{-145, "-200"}, // KCLR's runway 8 threshold
		{-85, "-100"},  // KTRM's runway 30 threshold
		{0, "0"},
		{500, "500"},
		{999, "900"},
		{1600, "1,600"},
		{3457, "3,400"},
		{17999, "17,900"},
		{18000, "FL180"},
		{35000, "FL350"},
	} {
		if got := FormatAltitude(tc.alt); got != tc.want {
			t.Errorf("FormatAltitude(%g) = %q, want %q", tc.alt, got, tc.want)
		}
	}
}

func TestAirspeedJSON(t *testing.T) {
	for _, tc := range []struct {
		json string
		want Airspeed
	}{
		{`250`, MakeIAS(250)},
		{`"250"`, MakeIAS(250)},
		{`"M85"`, MakeMach(.85)},
	} {
		var a Airspeed
		if err := json.Unmarshal([]byte(tc.json), &a); err != nil {
			t.Errorf("%s: %v", tc.json, err)
		} else if a != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.json, a, tc.want)
		}

		b, err := json.Marshal(tc.want)
		if err != nil {
			t.Fatal(err)
		}
		var round Airspeed
		if err := json.Unmarshal(b, &round); err != nil {
			t.Errorf("%s: round trip: %v", tc.json, err)
		} else if round != tc.want {
			t.Errorf("%s: round tripped to %+v", tc.json, round)
		}
	}

	for _, s := range []string{`"180-210"`, `"210+"`, `"250-"`} {
		var a Airspeed
		if err := json.Unmarshal([]byte(s), &a); err == nil {
			t.Errorf("%s: expected a range to be rejected; got %+v", s, a)
		}
	}
}

func TestAirspeedIAS(t *testing.T) {
	temp := MakeTemperatureFromCelsius(-56.5) // ISA in the flight levels
	if ias := MakeIAS(250).IAS(35000, temp); ias != 250 {
		t.Errorf("knots don't vary with altitude: got %f", ias)
	}
	// Mach 0.85 is around 260 knots indicated at FL350 and slower higher up.
	if ias := MakeMach(.85).IAS(35000, temp); ias < 250 || ias > 270 {
		t.Errorf("M85 at FL350: got %f, want ~260", ias)
	}
	if MakeMach(.85).IAS(45000, temp) >= MakeMach(.85).IAS(35000, temp) {
		t.Error("indicated airspeed for a Mach number should fall with altitude")
	}
}

func TestCheckSpeed(t *testing.T) {
	errors := func(a Airspeed) string {
		var e util.ErrorLogger
		checkSpeed(&e, `"initial_speed"`, a)
		return e.String()
	}

	for _, spd := range []Airspeed{MakeIAS(20), MakeIAS(400), MakeMach(85)} {
		if errs := errors(spd); errs == "" {
			t.Errorf("%s: expected an error", spd)
		}
	}
	for _, spd := range []Airspeed{MakeIAS(50), MakeIAS(60), MakeIAS(250), MakeIAS(350), MakeMach(.85)} {
		if errs := errors(spd); errs != "" {
			t.Errorf("%s: unexpected error %q", spd, errs)
		}
	}
}

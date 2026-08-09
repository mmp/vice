// pkg/aviation/aviation_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
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
// them from is still recognizably on one, so long as the STARs into the airport
// haven't converged by the time it starts.
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
	DB = &StaticDatabase{Airports: map[string]FAAAirport{
		"KTST": {Id: "KTST", STARs: map[string]STAR{
			"MIPP4":  mipp4,
			"PROUD2": star("HOLEY", "BRAND", "KORRY", "APPLE", "PROUD"),
		}},
		"KNOS": {Id: "KNOS"},
	}}
	t.Cleanup(func() { DB = oldDB })

	arrival := func(airport string, fixes ...string) *Arrival {
		ar := &Arrival{Airports: []string{airport}}
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
			name: "joins the STAR at one fix and leaves again",
			arr:  arrival("KTST", "LIZZI", "OWNWY", "RWY13"),
			want: "MIPP4",
		},
		{
			name: "crosses one fix of a STAR it doesn't start on",
			arr:  arrival("KTST", "OWNWY", "KORRY", "RWY13"),
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
		Airports: map[string]FAAAirport{
			"KTST": {Id: "KTST", STARs: map[string]STAR{"MIPP4": mipp4}},
			"KNOS": {Id: "KNOS", STARs: map[string]STAR{"MIPP4": mipp4}},
			"KOTH": {Id: "KOTH"},
		},
		Fixes: dbFixes,
	}
	t.Cleanup(func() { DB = oldDB })

	scenarioAirports := map[string]*Airport{"KTST": {}, "KNOS": {}, "KOTH": {}}
	controlPositions := map[ControlPosition]*Controller{"1T": {}}

	for _, tc := range []struct {
		name string
		arr  Arrival
		want []string
		err  string
	}{
		{
			name: "takes the airports from the STAR",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP"},
			want: []string{"KNOS", "KTST"},
		},
		{
			name: "airports given win over the STAR's",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []string{"KTST"}},
			want: []string{"KTST"},
		},
		{
			name: "airports given are sorted",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []string{"KTST", "KNOS"}},
			want: []string{"KNOS", "KTST"},
		},
		{
			name: "airlines don't imply the airports",
			arr: Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP",
				Airlines: map[string][]ArrivalAirline{"KNOS": nil}},
			want: []string{"KNOS", "KTST"},
		},
		{
			name: "airlines into an airport the arrival doesn't serve",
			arr: Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []string{"KTST"},
				Airlines: map[string][]ArrivalAirline{"KNOS": nil}},
			err: `"airlines" gives airlines into "KNOS"`,
		},
		{
			name: "an airport the scenario hasn't got",
			arr:  Arrival{STAR: "MIPP4", SpawnWaypoint: "MIPP", Airports: []string{"KNIL"}},
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
			arr:  Arrival{STAR: "MIPP4", Waypoints: wps, Airports: []string{"KOTH"}},
			err:  `"star" "MIPP4" isn't charted for any of the airports the arrival serves: KOTH`,
		},
		{
			name: "one of the airports having the STAR is enough",
			arr:  Arrival{STAR: "MIPP4", Waypoints: wps, Airports: []string{"KOTH", "KTST"}},
			want: []string{"KOTH", "KTST"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e util.ErrorLogger
			arr := tc.arr
			arr.InitialController = "1T"
			arr.InitialAltitudes = []int{10000}
			arr.InitialSpeed = 250
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
			InitialAltitudes: []int{10000}, InitialSpeed: 250}
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

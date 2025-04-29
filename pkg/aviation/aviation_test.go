// pkg/aviation/aviation_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"testing"

	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

func TestFrequencyFormat(t *testing.T) {
	type FS struct {
		f Frequency
		s string
	}

	for _, fs := range []FS{FS{f: Frequency(121900), s: "121.900"},
		FS{f: Frequency(130050), s: "130.050"},
		FS{f: Frequency(128000), s: "128.000"},
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
		testcase{s: "1000", ar: AltitudeRestriction{Range: [2]float32{1000, 1000}}},
		testcase{s: "3000-5000", ar: AltitudeRestriction{Range: [2]float32{3000, 5000}}},
		testcase{s: "7000+", ar: AltitudeRestriction{Range: [2]float32{7000, 0}}},
		testcase{s: "9000-", ar: AltitudeRestriction{Range: [2]float32{0, 9000}}},
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
	assigned := make(map[Squawk]interface{})

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

func TestApproachCWTSeparation(t *testing.T) {
	type testcase struct {
		front, back string
		expect      float32
	}
	for _, tc := range []testcase{
		testcase{front: "B", back: "G", expect: 5},
		testcase{front: "NOWGT", back: "G", expect: 10},
		testcase{front: "A", back: "D", expect: 6},
		testcase{front: "C", back: "D", expect: 0},
		testcase{front: "C", back: "E", expect: 3.5},
		testcase{front: "D", back: "B", expect: 3},
		testcase{front: "E", back: "H", expect: 0},
		testcase{front: "E", back: "I", expect: 4},
	} {
		if s := CWTApproachSeparation(tc.front, tc.back); s != tc.expect {
			t.Errorf("CWTApproachSeparation(%q, %q) = %f. Expected %f", tc.front, tc.back, s, tc.expect)
		}
	}
}

func TestDirectlyBehindCWTSeparation(t *testing.T) {
	type testcase struct {
		front, back string
		expect      float32
	}
	for _, tc := range []testcase{
		testcase{front: "B", back: "G", expect: 5},
		testcase{front: "B", back: "I", expect: 5},
		testcase{front: "NOWGT", back: "G", expect: 10},
		testcase{front: "A", back: "D", expect: 6},
		testcase{front: "C", back: "D", expect: 0},
		testcase{front: "C", back: "E", expect: 3.5},
		testcase{front: "D", back: "B", expect: 3},
		testcase{front: "E", back: "H", expect: 0},
		testcase{front: "E", back: "I", expect: 4},
		testcase{front: "F", back: "I", expect: 0},
	} {
		if s := CWTDirectlyBehindSeparation(tc.front, tc.back); s != tc.expect {
			t.Errorf("CWTDirectlyBehindSeparation(%q, %q) = %f. Expected %f", tc.front, tc.back, s, tc.expect)
		}
	}
}

func TestLocalSquawkCodePool(t *testing.T) {
	spec := LocalSquawkCodePoolSpecifier{
		Pools: map[string]PoolSpecifier{
			"ifr": PoolSpecifier{
				Range:   "0101-0177",
				Backups: "1",
			},
			"vfr": PoolSpecifier{
				Range:   "0201-0277",
				Backups: "2",
			},
			"1": PoolSpecifier{
				Range:   "0301-0377",
				Backups: "234",
			},
			"2": PoolSpecifier{
				Range:   "0401-0477",
				Backups: "341",
			},
			"3": PoolSpecifier{
				Range: "1602",
			},
			"4": PoolSpecifier{
				Range:   "0501-0577",
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
	seen := make(map[Squawk]interface{})
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

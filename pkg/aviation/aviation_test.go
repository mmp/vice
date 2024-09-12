// pkg/aviation/aviation_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"testing"

	"github.com/mmp/vice/pkg/rand"
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
	for _, squawk := range []string{"11111", "7778", "0801", "9000"} {
		if _, err := ParseSquawk(squawk); err == nil {
			t.Errorf("Expected error return value for invalid squawk %q", squawk)
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
	for _, p := range []*SquawkCodePool{MakeCompleteSquawkCodePool(), MakeSquawkBankCodePool(1), MakeSquawkBankCodePool(6)} {
		sq, err := p.Get()
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

		if err := p.Claim(sq); err != nil {
			t.Errorf("unable to claim unassigned code")
		}

		if !p.IsAssigned(sq) {
			t.Errorf("squawk not reported as assigned")
		}
	}
}

func TestSquawkCodePoolRandoms(t *testing.T) {
	for _, p := range []*SquawkCodePool{MakeCompleteSquawkCodePool(), MakeSquawkBankCodePool(1), MakeSquawkBankCodePool(6)} {
		assigned := make(map[Squawk]interface{})

		for i := range 100000 {
			sq, err := p.Get()
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
			if rand.Float32() < .4 || avail == 0 {
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
}

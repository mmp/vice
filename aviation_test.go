// aviation_test.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
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
			t.Errorf("encoding mismatch: got \"%s\", expected \"%s\"", enc, test.s)
		}
	}
}

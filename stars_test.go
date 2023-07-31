// stars_test.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
)

func TestParseQLControllers(t *testing.T) {
	w := World{
		Callsign: "JFK_APP",
		Controllers: map[string]*Controller{
			"JFK_APP": &Controller{Callsign: "JFK_APP", SectorId: "2G"},
			"JFK_TWR": &Controller{Callsign: "JFK_TWR", SectorId: "2W"},
			"EWR_APP": &Controller{Callsign: "EWR_APP", SectorId: "4P"},
			"NY_CTR":  &Controller{Callsign: "NY_CTR", SectorId: "N56"},
		},
	}

	type testcase struct {
		s   string
		exp []QuickLookPosition
	}

	for i, c := range []testcase{
		// basic
		testcase{s: "2G 4P",
			exp: []QuickLookPosition{
				QuickLookPosition{Id: "2G", Callsign: "JFK_APP", Plus: false},
				QuickLookPosition{Id: "4P", Callsign: "EWR_APP", Plus: false},
			},
		},
		// no space
		testcase{s: "N564P",
			exp: []QuickLookPosition{
				QuickLookPosition{Id: "N56", Callsign: "NY_CTR", Plus: false},
				QuickLookPosition{Id: "4P", Callsign: "EWR_APP", Plus: false},
			},
		},
		// plus
		testcase{s: "2W+N56",
			exp: []QuickLookPosition{
				QuickLookPosition{Id: "2W", Callsign: "JFK_TWR", Plus: true},
				QuickLookPosition{Id: "N56", Callsign: "NY_CTR", Plus: false},
			},
		},
		// implicit prefix of our #id
		testcase{s: "N56+W 4P",
			exp: []QuickLookPosition{
				QuickLookPosition{Id: "N56", Callsign: "NY_CTR", Plus: true},
				QuickLookPosition{Id: "2W", Callsign: "JFK_TWR", Plus: false},
				QuickLookPosition{Id: "4P", Callsign: "EWR_APP", Plus: false},
			},
		},
		// implicit prefix of our #id + plus
		testcase{s: "N56+W+ 4P",
			exp: []QuickLookPosition{
				QuickLookPosition{Id: "N56", Callsign: "NY_CTR", Plus: true},
				QuickLookPosition{Id: "2W", Callsign: "JFK_TWR", Plus: true},
				QuickLookPosition{Id: "4P", Callsign: "EWR_APP", Plus: false},
			},
		},
	} {
		pos, rem, err := parseQuickLookPositions(&w, c.s)
		if err != nil {
			t.Errorf("test %d: parse fail: %v. rem: %s", i, err, rem)
		}
		if len(pos) != len(c.exp) {
			t.Errorf("test %d: expected %d. got %+v", i, len(c.exp), pos)
		}
		for i, p := range pos {
			if p.Id != c.exp[i].Id || p.Callsign != c.exp[i].Callsign || p.Plus != c.exp[i].Plus {
				t.Errorf("test %d: expected %+v, got %+v", i, c.exp[i], p)
			}
		}
	}
}

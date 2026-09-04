// radar/routeui_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
)

// route is shorthand for one of an exit's departure routes.
type route struct {
	exit     string
	sid      string
	aircraft av.AircraftClass
}

func testAirport(rwys map[string][]route) *av.Airport {
	ap := &av.Airport{DepartureRoutes: make(map[av.RunwayID]map[av.ExitID]av.ExitRoutes)}
	for rwy, routes := range rwys {
		exits := make(map[av.ExitID]av.ExitRoutes)
		for _, r := range routes {
			exits[av.ExitID(r.exit)] = append(exits[av.ExitID(r.exit)],
				&av.ExitRoute{SID: r.sid, Aircraft: r.aircraft})
		}
		ap.DepartureRoutes[av.RunwayID(rwy)] = exits
	}
	return ap
}

func testRates(rwys ...string) map[av.RunwayID]map[string]float32 {
	rates := make(map[av.RunwayID]map[string]float32)
	for _, rwy := range rwys {
		rates[av.RunwayID(rwy)] = map[string]float32{"": 10}
	}
	return rates
}

// format renders a row the way the departures table does, as
// "SID|aircraft|runways|exits", for compact comparison.
func format(r departureRow) string {
	sid := r.group.SID
	if sid == "" {
		sid = "--"
	}
	return strings.Join([]string{sid, r.group.Aircraft.String(),
		strings.Join(r.runways, ","), strings.Join(r.exits, ",")}, "|")
}

func checkRows(t *testing.T, ap *av.Airport, rates map[av.RunwayID]map[string]float32, expected []string) {
	t.Helper()
	rows := departureRows("KTST", ap, rates)
	var got []string
	for _, r := range rows {
		got = append(got, format(r))
	}
	if len(got) != len(expected) {
		t.Errorf("got %d rows %v, expected %d %v", len(got), got, len(expected), expected)
		return
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("row %d: got %q, expected %q", i, got[i], expected[i])
		}
	}
}

// A SID's transitions are one row, and the row covers every runway it is
// flown from.
func TestDepartureRowsGroupBySID(t *testing.T) {
	ap := testAirport(map[string][]route{
		"25R": {{exit: "SCTRR", sid: "SUMMR2"}, {exit: "STOKD", sid: "SUMMR2"},
			{exit: "LAS", sid: "ORCKA5"}},
		"25L": {{exit: "SCTRR", sid: "SUMMR2"}, {exit: "LAS", sid: "ORCKA5"}},
	})
	checkRows(t, ap, testRates("25R", "25L"), []string{
		"ORCKA5||25L,25R|LAS",
		"SUMMR2||25L,25R|SCTRR,STOKD",
	})
}

// Runways are listed by their base name, without repeats for the suffixed
// configurations that share one.
func TestDepartureRowsRunwaySuffixes(t *testing.T) {
	ap := testAirport(map[string][]route{
		"13.Coney":    {{exit: "WHITE", sid: "TNNIS6"}},
		"13.LGAConey": {{exit: "WHITE", sid: "TNNIS6"}},
		"13":          {{exit: "WHITE", sid: "TNNIS6"}},
	})
	checkRows(t, ap, testRates("13", "13.Coney", "13.LGAConey"), []string{"TNNIS6||13|WHITE"})
}

// Routes with no SID are offered one exit at a time rather than collapsing
// into a single unnamed row.
func TestDepartureRowsNoSID(t *testing.T) {
	ap := testAirport(map[string][]route{
		"17": {{exit: "CEW"}, {exit: "MGM"}},
		"26": {{exit: "CEW"}, {exit: "MGM"}},
	})
	checkRows(t, ap, testRates("17", "26"), []string{
		"--||17,26|CEW",
		"--||17,26|MGM",
	})
}

// An exit whose routes fly different SIDs puts each in its own row: the
// enable bits must not collide the way the old runway/exit key did.
func TestDepartureRowsExitWithSeveralSIDs(t *testing.T) {
	jet, turboprop, prop := av.AircraftClassHeavyJet|av.AircraftClassNonheavyJet,
		av.AircraftClassTurboprop, av.AircraftClassProp
	ap := testAirport(map[string][]route{
		"16L": {
			{exit: "PAE", sid: "MONTN2", aircraft: jet},
			{exit: "PAE", aircraft: turboprop},
			{exit: "PAE", aircraft: prop},
			{exit: "ZELAK", sid: "MONTN2", aircraft: jet},
			{exit: "ARRIE", sid: "BANGR9"},
		},
	})
	checkRows(t, ap, testRates("16L"), []string{
		"--|prop|16L|PAE",
		"--|turboprop|16L|PAE",
		"BANGR9||16L|ARRIE",
		"MONTN2|jet|16L|PAE,ZELAK",
	})

	// Each row's group is distinct, so a checkbox for one never enables
	// another's routes.
	seen := make(map[DepartureGroup]bool)
	for _, r := range departureRows("KTST", ap, testRates("16L")) {
		if seen[r.group] {
			t.Errorf("%v: group appears in more than one row", r.group)
		}
		seen[r.group] = true
	}
	for dr := range ScenarioDepartureRoutes(ap, testRates("16L")) {
		if !seen[dr.Group] {
			t.Errorf("%s: route's group has no row", dr.Exit)
		}
	}
}

// Only the runways the scenario launches from are offered.
func TestDepartureRowsInactiveRunways(t *testing.T) {
	ap := testAirport(map[string][]route{
		"27": {{exit: "JLI", sid: "BRDR7"}},
		"9":  {{exit: "JLI", sid: "BRDR7"}, {exit: "OCN", sid: "Obstacle"}},
	})
	checkRows(t, ap, testRates("27"), []string{"BRDR7||27|JLI"})
}

func TestJoinWrapped(t *testing.T) {
	for _, tc := range []struct {
		items    []string
		width    int
		expected string
	}{
		{nil, 10, ""},
		{[]string{"ABC"}, 10, "ABC"},
		// A single item longer than the width still gets its own line.
		{[]string{"ABCDEFGHIJKL", "MN"}, 10, "ABCDEFGHIJKL,\nMN"},
		{[]string{"ABCD", "EFGH", "IJKL"}, 10, "ABCD, EFGH,\nIJKL"},
		{[]string{"A", "B", "C"}, 72, "A, B, C"},
	} {
		if got := joinWrapped(tc.items, tc.width); got != tc.expected {
			t.Errorf("joinWrapped(%v, %d) = %q, expected %q", tc.items, tc.width, got, tc.expected)
		}
		// No line runs past the width unless a single item does.
		for line := range strings.SplitSeq(joinWrapped(tc.items, tc.width), "\n") {
			if len(line) > tc.width+1 && !slices.ContainsFunc(tc.items, func(s string) bool { return len(s) >= tc.width }) {
				t.Errorf("%q: line %q is longer than %d", tc.items, line, tc.width)
			}
		}
	}
}

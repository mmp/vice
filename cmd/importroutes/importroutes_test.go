// cmd/importroutes/importroutes_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func TestClassifyAircraft(t *testing.T) {
	for _, tc := range []struct {
		text     string
		aircraft string
		rnav     bool
	}{
		{"", "", false},
		{"TURBOJETS", "jet", false},
		{"JETS ONLY", "jet", false},
		{"JETS ONLY. RNAV 1 - DME/DME/IRU OR GPS", "jet", true},
		{"RNAV TURBOJET", "jet", true},
		{"PROPS ONLY", "prop", false},
		{"SINGLE ENGINE ONLY", "prop", false},
		{"NON-JETS/NON-TURBOPROPS ONLY", "prop", false},
		{"RNAV 1 - DME/DME/IRU OR GPS", "", true},
		{"DME/DME/IRU OR GPS REQUIRED", "", true},
	} {
		aircraft, rnav := classifyAircraft(tc.text)
		if aircraft != tc.aircraft || rnav != tc.rnav {
			t.Errorf("classifyAircraft(%q) = %q, %v; want %q, %v",
				tc.text, aircraft, rnav, tc.aircraft, tc.rnav)
		}
	}
}

// A synthetic geometry: origin at the origin, destination due east, one fix
// on the direct line and another far off to the north.
func testLocations() (airport, fix func(string) (math.Point2LL, bool)) {
	airports := map[string]math.Point2LL{
		"KAAA": {0, 0},
		"KBBB": {4, 0},
	}
	fixes := map[string]math.Point2LL{
		"ONWAY": {2, 0},
		"FARRR": {2, 3},
	}
	airport = func(s string) (math.Point2LL, bool) { p, ok := airports[s]; return p, ok }
	fix = func(s string) (math.Point2LL, bool) { p, ok := fixes[s]; return p, ok }
	return
}

func TestRouteLengthRatio(t *testing.T) {
	airport, fix := testLocations()
	orig, _ := airport("KAAA")
	dest, _ := airport("KBBB")

	direct := routeLengthRatio(orig, dest, "ONWAY", fix)
	if direct > 1.01 {
		t.Errorf("on-route fix gave ratio %v, want ~1", direct)
	}

	dogleg := routeLengthRatio(orig, dest, "FARRR", fix)
	if dogleg < 1.5 {
		t.Errorf("dogleg gave ratio %v, want well above 1", dogleg)
	}

	// Airway and procedure tokens don't resolve and must not affect the length.
	withAirway := routeLengthRatio(orig, dest, "ONWAY J123 NOSUCH5", fix)
	if withAirway != direct {
		t.Errorf("unresolvable tokens changed the ratio: %v vs %v", withAirway, direct)
	}
}

func TestCullCDRs(t *testing.T) {
	airport, fix := testLocations()

	cdrs := []route{
		{orig: "KAAA", dest: "KBBB", typ: "CDR", rnav: true, route: "KAAA FARRR KBBB"},
		{orig: "KAAA", dest: "KBBB", typ: "CDR", rnav: true, route: "KAAA ONWAY KBBB"},
		{orig: "KAAA", dest: "KBBB", typ: "CDR", rnav: false, route: "KAAA FARRR ONWAY KBBB"},
	}
	kept := cullCDRs(cdrs, airport, fix)

	if len(kept) != 2 {
		t.Fatalf("kept %d routes, want 2: %v", len(kept), kept)
	}
	slices.SortFunc(kept, compareRoutes)
	if kept[0].route != "KAAA ONWAY KBBB" || !kept[0].rnav {
		t.Errorf("most direct route not kept first: %v", kept[0])
	}
	if kept[1].route != "KAAA FARRR ONWAY KBBB" || kept[1].rnav {
		t.Errorf("conventional alternate not kept: %v", kept[1])
	}

	// With a conventional route the most direct, there is no alternate.
	cdrs[1].rnav = false
	if kept := cullCDRs(cdrs, airport, fix); len(kept) != 1 {
		t.Errorf("kept %d routes, want just the most direct: %v", len(kept), kept)
	}
}

func TestDedupe(t *testing.T) {
	routes := []route{
		{orig: "KAAA", dest: "KBBB", typ: "H", route: "KAAA ONWAY KBBB"},
		{orig: "KAAA", dest: "KBBB", typ: "CDR", route: "KAAA ONWAY KBBB"},
		{orig: "KAAA", dest: "KBBB", typ: "CDR", route: "KAAA FARRR KBBB"},
	}
	kept := dedupe(routes)
	if len(kept) != 2 {
		t.Fatalf("kept %d routes, want 2: %v", len(kept), kept)
	}
	if kept[0].typ != "H" || kept[1].route != "KAAA FARRR KBBB" {
		t.Errorf("wrong routes kept: %v", kept)
	}
}

func TestCompareRoutes(t *testing.T) {
	routes := []route{
		{orig: "KAAA", dest: "KBBB", typ: "CDR", seq: 1, route: "r1"},
		{orig: "KAAA", dest: "KBBB", typ: "H", seq: 2, route: "r2"},
		{orig: "KAAA", dest: "KBBB", typ: "H", seq: 1, route: "r3"},
		{orig: "KAAA", dest: "KACK", typ: "TEC", seq: 1, route: "r4"},
	}
	slices.SortFunc(routes, compareRoutes)

	var got []string
	for _, r := range routes {
		got = append(got, r.route)
	}
	want := []string{"r4", "r3", "r2", "r1"} // KACK sorts before KBBB
	if !slices.Equal(got, want) {
		t.Errorf("sorted order %v, want %v", got, want)
	}
}

// Two airways in a row, with a fix that both pass through where the route
// should have named one.
func TestMendRoute(t *testing.T) {
	// PINNS and WOKRO are both on V64 and V363, and PINNS comes first along
	// V64, so only the midpoint tells them apart.
	airways := map[string][]av.Airway{
		"V64":  {{Name: "V64", Fixes: []av.AirwayFix{{Fix: "SLI"}, {Fix: "PINNS"}, {Fix: "WOKRO"}, {Fix: "KELPS"}}}},
		"V363": {{Name: "V363", Fixes: []av.AirwayFix{{Fix: "PINNS"}, {Fix: "WOKRO"}, {Fix: "DANAH"}}}},
		"V23":  {{Name: "V23", Fixes: []av.AirwayFix{{Fix: "DANAH"}, {Fix: "OCN"}}}},
		"V91":  {{Name: "V91", Fixes: []av.AirwayFix{{Fix: "BDR"}, {Fix: "MAD"}}}},
	}
	// The SLI-DANAH midpoint is (0, 2): WOKRO sits all but on it, PINNS well
	// short of it.
	locations := map[string]math.Point2LL{
		"SLI": {0, 0}, "DANAH": {0, 4}, "PINNS": {0, 0.5}, "WOKRO": {0, 2.1},
		"KELPS": {1, 1}, "OCN": {0, 5}, "FUL": {-1, 0},
	}
	airwaysOf := func(name string) ([]av.Airway, bool) { a, ok := airways[name]; return a, ok }
	locate := func(fix string) (math.Point2LL, bool) { p, ok := locations[fix]; return p, ok }

	for _, tc := range []struct {
		route, want string
		notes       int
	}{
		// A single airway is how a route is written and is left alone.
		{"FUL SLI V64 KELPS", "FUL SLI V64 KELPS", 0},
		// Both PINNS and WOKRO can be flown from SLI on V64 and on to DANAH
		// on V363; WOKRO is the nearer the midpoint. The single V23 after
		// the pair is untouched.
		{"FUL SLI V64 V363 DANAH V23 OCN", "FUL SLI V64 WOKRO V363 DANAH V23 OCN", 1},
		// V91 and V363 pass through no common fix, so the leg goes direct.
		{"FUL SLI V91 V363 DANAH", "FUL SLI DANAH", 1},
		// A pair with no fix ahead of it has nothing to be mended against.
		{"FUL SLI V64 V363", "FUL SLI", 1},
		// The route joins and leaves V64 at SLI, which names no stretch of
		// it; the airway and the repeat of SLI go.
		{"FUL SLI V64 SLI KELPS", "FUL SLI KELPS", 1},
		// The same, with the airway at the end of the route.
		{"FUL KELPS V64 KELPS", "FUL KELPS", 1},
	} {
		got, notes := mendRoute(tc.route, airwaysOf, locate)
		if got != tc.want {
			t.Errorf("mendRoute(%q) = %q, want %q", tc.route, got, tc.want)
		}
		if len(notes) != tc.notes {
			t.Errorf("mendRoute(%q) reported %v, want %d notes", tc.route, notes, tc.notes)
		}
	}
}

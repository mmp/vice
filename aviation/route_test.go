// pkg/aviation/route_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type testLocator map[string]math.Point2LL

func (tl testLocator) Locate(fix string) (math.Point2LL, bool) {
	p, ok := tl[fix]
	return p, ok
}

func (tl testLocator) Similar(fix string) []string {
	return nil
}

func (tl testLocator) LocateDME(fix string) (math.Point2LL, int, bool) {
	p, ok := tl[fix]
	return p, 33, ok
}

func TestHoldEntry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		turn         TurnDirection
		headingToFix math.MagneticHeading
		want         HoldEntry
	}{
		{
			name:         "right direct",
			turn:         TurnRight,
			headingToFix: 100,
			want:         HoldEntryDirect,
		},
		{
			name:         "right parallel",
			turn:         TurnRight,
			headingToFix: 330,
			want:         HoldEntryParallel,
		},
		{
			name:         "right teardrop",
			turn:         TurnRight,
			headingToFix: 250,
			want:         HoldEntryTeardrop,
		},
		{
			name:         "left direct",
			turn:         TurnLeft,
			headingToFix: 20,
			want:         HoldEntryDirect,
		},
		{
			name:         "left parallel",
			turn:         TurnLeft,
			headingToFix: 220,
			want:         HoldEntryParallel,
		},
		{
			name:         "left teardrop",
			turn:         TurnLeft,
			headingToFix: 310,
			want:         HoldEntryTeardrop,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hold := Hold{InboundCourse: 90, TurnDirection: tc.turn}
			if got := hold.Entry(tc.headingToFix); got != tc.want {
				t.Fatalf("Entry(%v) = %v, want %v", tc.headingToFix, got, tc.want)
			}
		})
	}
}

func TestParseWaypointActionGroups(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("_EWR4_4La/h039@a500/r055@d4.0IEZA/l290/ho5W")
	if err != nil {
		t.Fatal(err)
	}
	if len(wps) != 1 {
		t.Fatalf("expected 1 waypoint, got %d", len(wps))
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %d", len(groups))
	}
	if groups[0].Actions.Heading.Heading != 39 {
		t.Errorf("expected first heading 39, got %d", groups[0].Actions.Heading.Heading)
	}
	if groups[0].Until.Type != WaypointActionAltitude || groups[0].Until.Altitude != 500 {
		t.Fatalf("unexpected altitude action group: %+v", groups[0])
	}

	if groups[1].Actions.Heading.Turn != TurnRight || groups[1].Actions.Heading.Heading != 55 {
		t.Errorf("expected right turn to heading 55, got turn %v heading %d",
			groups[1].Actions.Heading.Turn, groups[1].Actions.Heading.Heading)
	}
	if groups[1].Until.Type != WaypointActionDME || groups[1].Until.DMEFix != "IEZA" || groups[1].Until.DMEDistance != 4 {
		t.Fatalf("unexpected DME action group: %+v", groups[1])
	}

	if groups[2].Actions.Heading.Turn != TurnLeft || groups[2].Actions.Heading.Heading != 290 {
		t.Errorf("expected left turn to heading 290, got turn %v heading %d",
			groups[2].Actions.Heading.Turn, groups[2].Actions.Heading.Heading)
	}
	if groups[2].Actions.HandoffController != "5W" {
		t.Fatalf("expected final action group handoff to 5W, got %+v", groups[2].Actions)
	}
	if groups[2].Until.Type != WaypointActionNoTermination {
		t.Fatalf("unexpected final action group: %+v", groups[2])
	}
}

func TestInitializeActionGroupDMEFix(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("_EWR4_4La/r055@d4.0IEZA/l290")
	if err != nil {
		t.Fatal(err)
	}

	loc := testLocator{
		"_EWR4_4La": {-74.161563, 40.695431},
		"IEZA":      {-74.161563, 40.695431},
	}
	wps = wps.InitializeLocations(loc, 45, 0, false, nil)

	groups := wps[0].ActionGroups()
	if len(groups) == 0 {
		t.Fatal("expected action groups")
	}
	if groups[0].Until.DMEFixLocation.IsZero() {
		t.Fatal("expected initialized DME fix location")
	}
	if groups[0].Until.DMEFixElevation != 33 {
		t.Fatalf("expected initialized DME fix elevation 33, got %d", groups[0].Until.DMEFixElevation)
	}
}

func TestParseLegacyModifierAfterActionGroup(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("_EWR4_4La/h039@a500/r055/radius2.0/land")
	if err != nil {
		t.Fatal(err)
	}
	if len(wps[0].ActionGroups()) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(wps[0].ActionGroups()))
	}
	if wps[0].Radius() != 2 {
		t.Fatalf("expected legacy radius modifier to apply after action group, got %.1f", wps[0].Radius())
	}
	if !wps[0].Land() {
		t.Fatal("expected legacy land modifier to apply after action group")
	}
}

func TestParseActionGroupClearApproachAndDuplicateAltitudes(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("_EWR4_4La/h039@a500/r055/clearapp")
	if err != nil {
		t.Fatal(err)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(groups))
	}
	if !groups[1].Actions.ClearApproach {
		t.Fatal("expected clearapp in final action group")
	}

	if _, err := parseWaypoints("_EWR4_4La/h039@a500/c50/c100"); err == nil {
		t.Fatal("expected duplicate climb altitude action to fail")
	}
	if _, err := parseWaypoints("_EWR4_4La/h039@a500/d50/d100"); err == nil {
		t.Fatal("expected duplicate descend altitude action to fail")
	}
}

func TestParseInterceptApproachFlag(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("AROSLY/intercept FORDS")
	if err != nil {
		t.Fatal(err)
	}
	if !wps[0].InterceptApproach() {
		t.Fatal("expected /intercept to set the InterceptApproach flag")
	}
	if wps[1].InterceptApproach() {
		t.Fatal("expected /intercept to apply only to its waypoint")
	}

	encoded := WaypointArray(wps).Encode()
	if !strings.Contains(encoded, "AROSLY/intercept") {
		t.Fatalf("expected encoded route to round-trip /intercept, got %q", encoded)
	}
}

func TestCheckArrivalInterceptRequiresApproach(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("AROSLY/intercept FORDS")
	if err != nil {
		t.Fatal(err)
	}

	var e util.ErrorLogger
	WaypointArray(wps).CheckArrival(&e, nil, false, func(string) bool { return true })
	if !e.HaveErrors() {
		t.Fatal("expected error when /intercept is used without an assigned approach")
	}

	var ok util.ErrorLogger
	WaypointArray(wps).CheckArrival(&ok, nil, true, func(string) bool { return true })
	if ok.HaveErrors() {
		t.Fatalf("expected no error when an approach is assigned, got errors")
	}
}

func TestParseActionGroupErrorIncludesWaypointContext(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	_, err := parseWaypoints("KJFK-13R/h314@4 SKORR")
	if err == nil {
		t.Fatal("expected invalid action termination")
	}
	for _, want := range []string{"KJFK-13R/h314@4", "/h314@4", "4: invalid waypoint action termination"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error %q to include %q", err, want)
		}
	}
}

// A real-world route from the city-pair database is space-separated fix and
// airway identifiers, with none of the "/" modifiers the scenario route parser
// understands. Every waypoint that survives has a location to fly to.
func TestRouteWaypoints(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: map[string][]Airway{
		"Q75": {{Name: "Q75", Fixes: []AirwayFix{{Fix: "BIGGY"}, {Fix: "MIDDL"}, {Fix: "TEUFL"}}}},
	}}
	t.Cleanup(func() { DB = oldDB })

	loc := testLocator{
		"BIGGY": math.Point2LL{-74.5, 40.4},
		"MIDDL": math.Point2LL{-75.5, 39.4},
		"TEUFL": math.Point2LL{-76.5, 38.4},
		"TPA":   math.Point2LL{-82.5, 28.0},
	}

	// SLI341/019 is a radial/DME fix: the database has a handful and none of
	// them can be placed, so they drop out rather than derailing the route.
	wps := RouteWaypoints("BIGGY Q75 TEUFL DADES2 SLI341/019 TPA").
		InitializeLocations(loc, 45, 12, true /* allowSlop */, nil)

	var got []string
	for _, wp := range wps {
		if wp.Location.IsZero() {
			t.Errorf("waypoint %q has no location", wp.Fix)
		}
		got = append(got, wp.Fix)
	}
	want := []string{"BIGGY", "MIDDL", "TEUFL", "TPA"}
	if !slices.Equal(got, want) {
		t.Errorf("route waypoints = %v, want %v", got, want)
	}
}

func TestRouteSTAR(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{
		Airports: map[string]FAAAirport{
			"KSAN": {Id: "KSAN", STARs: map[string]STAR{"LUCKI1": {}}},
			"KJFK": {Id: "KJFK", STARs: map[string]STAR{"LENDY6": {}, "PARCH4": {}}},
			"KFLL": {Id: "KFLL", STARs: map[string]STAR{"CUUDA4": {}}},
		},
		Airways: map[string][]Airway{"Q86": nil, "J121": nil},
	}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct {
		route, icao string
		star, entry string
	}{
		{"KORD PIPPN PWE PLNDL Q86 TTRUE LUCKI1 KSAN", "KSAN", "LUCKI1", "TTRUE"},
		// The route files a stale revision of the STAR.
		{"KORD TTRUE LUCKI2 KSAN", "KSAN", "LUCKI1", "TTRUE"},
		// The token ahead of the STAR is an airway, not an entry fix.
		{"KORD PWE Q86 LUCKI1 KSAN", "KSAN", "LUCKI1", ""},
		// No trailing airport token, and the airport in its 3-letter form.
		{"PMM ELX LENDY6", "KJFK", "LENDY6", "ELX"},
		{"EWR WHITE Q409 MAJIK CUUDA3 FLL", "KFLL", "CUUDA4", "MAJIK"},
		// Routes that end with a plain fix or an airway name no STAR.
		{"CAMRN ROBER KJFK", "KJFK", "", ""},
		{"LGA V16 J121 KJFK", "KJFK", "", ""},
		// A procedure the airport doesn't chart names no STAR.
		{"KORD TTRUE LUCKI1 KJFK", "KJFK", "", ""},
	} {
		star, entry := RouteSTAR(tc.route, tc.icao)
		if star != tc.star || entry != tc.entry {
			t.Errorf("RouteSTAR(%q, %s) = %q, %q; want %q, %q",
				tc.route, tc.icao, star, entry, tc.star, tc.entry)
		}
	}
}

func TestHourRanges(t *testing.T) {
	for _, tc := range []struct {
		encoded string
		hours   []int
	}{
		{"", nil},
		{"5", []int{5}},
		{"6-9", []int{6, 7, 8, 9}},
		{"0-2,22-23", []int{0, 1, 2, 22, 23}},
		{"3,11-12,23", []int{3, 11, 12, 23}},
	} {
		var h HourRanges
		for _, hour := range tc.hours {
			h.Add(hour)
		}
		if got := h.String(); got != tc.encoded {
			t.Errorf("hours %v encoded as %q, want %q", tc.hours, got, tc.encoded)
		}

		var rt HourRanges
		if err := json.Unmarshal([]byte(`"`+tc.encoded+`"`), &rt); err != nil {
			t.Errorf("%q: %v", tc.encoded, err)
		} else if rt != h {
			t.Errorf("%q decoded to %b, want %b", tc.encoded, rt, h)
		}

		for hour := range 24 {
			want := false
			for _, in := range tc.hours {
				want = want || in == hour
			}
			if got := h.Contains(hour); got != want {
				t.Errorf("%q Contains(%d) = %v, want %v", tc.encoded, hour, got, want)
			}
		}
	}

	var h HourRanges
	if err := json.Unmarshal([]byte(`"25"`), &h); err == nil {
		t.Errorf("hour 25 did not error")
	}
}

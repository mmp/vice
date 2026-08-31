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

func (tl testLocator) Declination(fix string) (float32, bool) {
	return 0, false
}

// declinationLocator is a testLocator whose navaids have station declinations.
type declinationLocator struct {
	testLocator
	declinations map[string]float32
}

func (dl declinationLocator) Declination(fix string) (float32, bool) {
	d, ok := dl.declinations[fix]
	return d, ok
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

	wps, err := parseWaypoints("_EWR4_4La/h039/@a500+/r055/@IEZA-D4.0+/l290/ho5W")
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

func TestParseCourseTermination(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("RNGRR/h200/@crs220 WAVEY")
	if err != nil {
		t.Fatal(err)
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 1 {
		t.Fatalf("expected 1 action group, got %d", len(groups))
	}
	if groups[0].Actions.Heading.Heading != 200 || groups[0].Actions.Heading.Track {
		t.Errorf("expected heading 200, got %+v", groups[0].Actions.Heading)
	}
	if groups[0].Until.Type != WaypointActionCourse || groups[0].Until.Course != 220 {
		t.Fatalf("unexpected course action group: %+v", groups[0])
	}

	encoded := WaypointArray(wps).Encode()
	if !strings.Contains(encoded, "RNGRR/h200/@crs220") {
		t.Fatalf("expected encoded route to round-trip /@crs, got %q", encoded)
	}
}

func TestParseRadialCourseTermination(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("KSEA-34R/h164/@crsSEA-R161 NEVJO")
	if err != nil {
		t.Fatal(err)
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 1 {
		t.Fatalf("expected 1 action group, got %d", len(groups))
	}
	until := groups[0].Until
	if until.Type != WaypointActionCourse || until.Course != 161 || until.CourseFix != "SEA" {
		t.Fatalf("unexpected course action group: %+v", groups[0])
	}

	if encoded := WaypointArray(wps).Encode(); !strings.Contains(encoded, "KSEA-34R/h164/@crsSEA-R161") {
		t.Fatalf("expected encoded route to round-trip /@crs<navaid>-R, got %q", encoded)
	}

	loc := testLocator{
		"KSEA-34R": {-122.308, 47.431},
		"NEVJO":    {-122.310, 47.252},
		"SEA":      {-122.310, 47.435},
	}
	// Without a station declination, the course is referenced to the area's
	// variation.
	wps = wps.InitializeLocations(loc, 40.7, -15, false, nil)
	if v := wps[0].ActionGroups()[0].Until.CourseFixVariation; v != -15 {
		t.Errorf("expected the course to be referenced to the area's variation -15, got %g", v)
	}

	// A VOR's radials are referenced to its station declination instead.
	wps = wps.InitializeLocations(declinationLocator{loc, map[string]float32{"SEA": -19}}, 40.7, -15, false, nil)
	if v := wps[0].ActionGroups()[0].Until.CourseFixVariation; v != -19 {
		t.Errorf("expected the course to be referenced to SEA's declination -19, got %g", v)
	}

	e := &util.ErrorLogger{}
	wps.InitializeLocations(testLocator{"KSEA-34R": {-122.308, 47.431}, "NEVJO": {-122.310, 47.252}}, 40.7, 0, false, e)
	if !e.HaveErrors() {
		t.Error("expected an error for an unknown course navaid")
	}
}

func TestParseCourseTerminationErrors(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct{ route, want string }{
		{"RNGRR/h200/@crs220/l290 WAVEY", "/@crs must be the last trigger"},
		{"RNGRR/h200/@crs220", "has no following fix"},
		{"RNGRR/h200/@crs0 WAVEY", "heading must be between 1-360"},
		{"RNGRR/h200/@crsabc WAVEY", "expected a course after crs"},
		{"RNGRR/h200/@crsHLN-R WAVEY", "expected a radial after HLN-R"},
		{"RNGRR/h200/@crsHLN-R400 WAVEY", "heading must be between 1-360"},
		{"RNGRR/h0 WAVEY", "heading must be between 1-360"},
	} {
		_, err := parseWaypoints(tc.route)
		if err == nil {
			t.Errorf("%s: expected an error", tc.route)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected error %q to include %q", tc.route, err, tc.want)
		}
	}
}

func TestInitializeActionGroupDMEFix(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	wps, err := parseWaypoints("_EWR4_4La/r055/@IEZA-D4.0+/l290")
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

	wps, err := parseWaypoints("_EWR4_4La/h039/@a500+/r055/radius2.0/land")
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

	wps, err := parseWaypoints("_EWR4_4La/h039/@a500+/r055/clearapp")
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

	if _, err := parseWaypoints("_EWR4_4La/h039/@a500+/c5000/c10000"); err == nil {
		t.Fatal("expected duplicate climb altitude action to fail")
	}
	if _, err := parseWaypoints("_EWR4_4La/h039/@a500+/d5000/d10000"); err == nil {
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

func TestCheckArrivalCatchesHundredsOfFeetAltitudes(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	check := func(route string) bool {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		WaypointArray(wps).CheckArrival(&e, nil, false, func(string) bool { return true })
		return e.HaveErrors()
	}

	if !check("CCC/a120 ROBER/a50 ZULAB/a1800 KJFK-31R/a100") {
		t.Error("expected errors for altitude restrictions given in hundreds of feet")
	}
	if !check("CAMRN/a120+ MEALS/a300- ZULAB/a1800 KJFK-31L/a100") {
		t.Error("expected errors for at or above/below restrictions in hundreds of feet")
	}
	if check("CCC/a12000 ROBER/a5000 ZULAB/a1800 KJFK-31R/a100") {
		t.Error("unexpected errors for altitude restrictions in feet")
	}
	if check("CCC/a12000 ROBER/a5000 ZULAB/a001/s130 KJFK-31R/delete") {
		t.Error("unexpected errors at the deletion waypoint and the one before it")
	}
}

func TestParseActionGroupErrorIncludesWaypointContext(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	_, err := parseWaypoints("KJFK-13R/h314/@4 SKORR")
	if err == nil {
		t.Fatal("expected invalid action termination")
	}
	for _, want := range []string{"KJFK-13R/h314/@4", "/@4", "unknown trigger"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error %q to include %q", err, want)
		}
	}
}

// A /@d trigger is a distance flown in nautical miles; it round-trips and
// takes no + or - since distance flown only increases.
func TestParseDistanceTrigger(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	route := "NETAA/t338/@d7.9/lt208/@crs178 REYLO"
	wps, err := parseWaypoints(route)
	if err != nil {
		t.Fatal(err)
	}
	if got := wps.Encode(); got != route {
		t.Errorf("expected %q to round-trip, got %q", route, got)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(groups))
	}
	if until := groups[0].Until; until.Type != WaypointActionDistance || until.Distance != 7.9 {
		t.Errorf("expected a 7.9nm distance termination, got %+v", until)
	}

	for _, tc := range []struct{ route, want string }{
		{"NETAA/t338/@d REYLO", "unknown trigger"},
		{"NETAA/t338/@d0 REYLO", "must be positive"},
		{"NETAA/t338/@d7.9+ REYLO", "invalid distance"},
		{"NETAA/t338/@dx REYLO", "invalid distance"},
	} {
		_, err := parseWaypoints(tc.route)
		if err == nil {
			t.Errorf("%s: expected an error", tc.route)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected error %q to include %q", tc.route, err, tc.want)
		}
	}
}

func TestParseMultipleRestrictionsIsError(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	_, err := parseWaypoints("BACAS/a10000-/a8000+/s210 FRING")
	if err == nil {
		t.Fatal("expected error for multiple altitude restrictions on one fix")
	}
	for _, want := range []string{"BACAS/a10000-/a8000+/s210", "multiple altitude restrictions"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error %q to include %q", err, want)
		}
	}

	if _, err := parseWaypoints("BACAS/a8000-10000/s210 FRING"); err != nil {
		t.Fatalf("unexpected error for altitude range: %v", err)
	}

	_, err = parseWaypoints("BACAS/a8000-10000/s250/s250 FRING")
	if err == nil {
		t.Fatal("expected error for multiple speed restrictions on one fix")
	}
	if !strings.Contains(err.Error(), "multiple speed restrictions") {
		t.Fatalf("unexpected error %q", err)
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

func TestParseRadialTermination(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	const route = "KDLS-25/t069/@a647+/h120/@LTJ-R165/tLTJ-R165/@a4000+ LTJ"
	wps, err := parseWaypoints(route)
	if err != nil {
		t.Fatal(err)
	}
	if got := wps.Encode(); got != route {
		t.Errorf("expected the route to round-trip, got %q", got)
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %d", len(groups))
	}
	if groups[1].Actions.Heading.Heading != 120 || groups[1].Actions.Heading.Track {
		t.Errorf("expected heading 120, got %+v", groups[1].Actions.Heading)
	}
	if groups[1].Until.Type != WaypointActionRadial || groups[1].Until.Radial != 165 || groups[1].Until.RadialFix != "LTJ" {
		t.Fatalf("unexpected radial action group: %+v", groups[1])
	}
	if h := groups[2].Actions.Heading; h.Heading != 165 || !h.Track || h.Fix != "LTJ" {
		t.Errorf("expected the LTJ 165 radial to be tracked, got %+v", h)
	}
	if groups[2].Until.Type != WaypointActionAltitude || groups[2].Until.Altitude != 4000 {
		t.Fatalf("unexpected altitude action group: %+v", groups[2])
	}

	loc := testLocator{
		"KDLS-25": {-121.16, 45.62},
		"LTJ":     {-121.10, 45.71},
	}
	wps = wps.InitializeLocations(loc, 45, -15, false, nil)
	groups = wps[0].ActionGroups()
	if groups[1].Until.RadialFixLocation.IsZero() {
		t.Error("expected initialized radial fix location")
	}
	if groups[2].Actions.Heading.FixLocation.IsZero() {
		t.Error("expected initialized tracked radial fix location")
	}
	// Without a station declination, the radial is referenced to the area's
	// variation.
	if v := groups[1].Until.RadialFixVariation; v != -15 {
		t.Errorf("expected the radial to be referenced to the area's variation -15, got %g", v)
	}
	if v := groups[2].Actions.Heading.FixVariation; v != -15 {
		t.Errorf("expected the tracked radial to be referenced to the area's variation -15, got %g", v)
	}

	// A VOR's radials are referenced to its station declination instead.
	wps = wps.InitializeLocations(declinationLocator{loc, map[string]float32{"LTJ": -21}}, 45, -15, false, nil)
	groups = wps[0].ActionGroups()
	if v := groups[1].Until.RadialFixVariation; v != -21 {
		t.Errorf("expected the radial to be referenced to LTJ's declination -21, got %g", v)
	}
	if v := groups[2].Actions.Heading.FixVariation; v != -21 {
		t.Errorf("expected the tracked radial to be referenced to LTJ's declination -21, got %g", v)
	}

	e := &util.ErrorLogger{}
	wps.InitializeLocations(testLocator{"KDLS-25": {-121.16, 45.62}}, 45, 0, false, e)
	if !e.HaveErrors() {
		t.Error("expected an error for an unknown radial navaid")
	}
}

func TestParseRadialTrack(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	// A radial to track is a heading action with the navaid as its fix.
	for _, tc := range []struct {
		route string
		turn  TurnDirection
	}{
		{"KSNS-8/tSNS-R255", TurnClosest},
		{"KSNS-8/ltSNS-R255", TurnLeft},
		{"KSNS-8/rtSNS-R255", TurnRight},
	} {
		wps, err := parseWaypoints(tc.route)
		if err != nil {
			t.Fatalf("%s: %v", tc.route, err)
		}
		if got := wps.Encode(); got != tc.route {
			t.Errorf("expected %q to round-trip, got %q", tc.route, got)
		}
		groups := wps[0].ActionGroups()
		if len(groups) != 1 {
			t.Fatalf("%s: expected 1 action group, got %d", tc.route, len(groups))
		}
		if h := groups[0].Actions.Heading; h.Heading != 255 || !h.Track || h.Fix != "SNS" || h.Turn != tc.turn {
			t.Errorf("%s: unexpected heading action %+v", tc.route, h)
		}
		if groups[0].Until.Type != WaypointActionNoTermination {
			t.Errorf("%s: unexpected termination %+v", tc.route, groups[0].Until)
		}
	}
}

func TestParseRadialErrors(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct{ route, want string }{
		{"KSNS-8/hSNS-R255", "can only be tracked"},
		{"KSNS-8/lSNS-R255", "can only be tracked"},
		{"KSNS-8/h084/@a484+/rSNS-R255", "can only be tracked"},
		{"KDLS-25/h120/@LTJ-R", "expected a radial after LTJ-R"},
		{"KDLS-25/h120/@-R165", "expected a radial as NAVAID-R"},
		{"KDLS-25/h120/@LTJ-R0", "heading must be between 1-360"},
	} {
		_, err := parseWaypoints(tc.route)
		if err == nil {
			t.Errorf("%s: expected an error", tc.route)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected error %q to include %q", tc.route, err, tc.want)
		}
	}
}

func TestEncodeTurnDirection(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct {
		route             string
		turn, headingTurn TurnDirection
	}{
		{"KDLS-25/h120/@LTJ-R165/tLTJ-R165/@a4000+/ld LTJ", TurnLeft, TurnClosest},
		{"SKORR/rd WAVEY/a3000+ SHIPP", TurnRight, TurnClosest},
		{"SKORR/rd WAVEY/ph", TurnRight, TurnClosest},
		// The turn toward a fix and the turn onto its heading are separate.
		{"SKORR WAVEY/l090", TurnClosest, TurnLeft},
		{"SKORR/rd WAVEY/l090", TurnRight, TurnLeft},
	} {
		wps, err := parseWaypoints(tc.route)
		if err != nil {
			t.Fatalf("%s: %v", tc.route, err)
		}
		h, _ := wps[1].HeadingAction()
		if wps[1].Turn() != tc.turn || h.Turn != tc.headingTurn {
			t.Errorf("%s: got turn %v and heading turn %v, want %v and %v", tc.route,
				wps[1].Turn(), h.Turn, tc.turn, tc.headingTurn)
		}
		if got := wps.Encode(); got != tc.route {
			t.Errorf("expected %q to round-trip, got %q", tc.route, got)
		}
	}
}

func TestParseArcDirection(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct {
		route     string
		direction DMEArcDirection
		radius    float32
	}{
		{"TOMDY/arc7SSI SAUSE", DMEArcDirectionUnset, 7},
		{"TOMDY/larc7SSI SAUSE", DMEArcDirectionCounterClockwise, 7},
		{"TOMDY/rarc2.54SSI SAUSE", DMEArcDirectionClockwise, 2.54},
		{"WUPMA/rarc7.5 ALABE", DMEArcDirectionClockwise, 0},
	} {
		wps, err := parseWaypoints(tc.route)
		if err != nil {
			t.Fatalf("%s: %v", tc.route, err)
		}
		arc := wps[0].Arc()
		if arc == nil {
			t.Fatalf("%s: expected an arc", tc.route)
		}
		if arc.Direction != tc.direction || arc.Radius != tc.radius {
			t.Errorf("%s: got arc %+v", tc.route, *arc)
		}
		if got := wps.Encode(); got != tc.route {
			t.Errorf("expected %q to round-trip, got %q", tc.route, got)
		}
	}
}

func TestParseActionsIntoGroups(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	// Actions at a fix form a single open action group and round-trip.
	for _, route := range []string{"RNGRR/h223", "RNGRR/h223/ho5W/c5000", "KJFK-4L/ho5S/@a2500+"} {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		if groups := wps[0].ActionGroups(); len(groups) != 1 {
			t.Errorf("%s: expected 1 action group, got %+v", route, groups)
		}
		if got := wps.Encode(); got != route {
			t.Errorf("expected %q to round-trip, got %q", route, got)
		}
	}

	wps, err := parseWaypoints("RNGRR/h223/ho5W/c5000")
	if err != nil {
		t.Fatal(err)
	}
	actions := wps[0].ActionGroups()[0].Actions
	if actions.Heading.Heading != 223 || actions.HandoffController != "5W" || actions.ClimbAltitude != 5000 {
		t.Errorf("unexpected actions %+v", actions)
	}

	// Actions after a termination start the next group; a termination
	// ends the group the actions before it were merged into.
	wps, err = parseWaypoints("KLGB-12/h301/@a400+/ho4R/h200/@a3000+/ho6K")
	if err != nil {
		t.Fatal(err)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %+v", groups)
	}
	if g := groups[1]; g.Actions.HandoffController != "4R" || g.Actions.Heading.Heading != 200 ||
		g.Until.Type != WaypointActionAltitude || g.Until.Altitude != 3000 {
		t.Errorf("unexpected second group %+v", g)
	}
	if g := groups[2]; g.Actions.HandoffController != "6K" || g.Until.Type != WaypointActionNoTermination {
		t.Errorf("unexpected third group %+v", g)
	}
}

func TestParseTriggerErrors(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, tc := range []struct{ route, want string }{
		{"KJFK-4L/@a2000+/l110", "trigger /@a2000+ must follow an action; use /ph"},
		{"KJFK-4L/h044/@a2000+/@a3000+", "trigger /@a3000+ must follow an action"},
		{"KJFK-4L/h044/@a70000+", "between 0 and 60000"},
		{"KJFK-4L/h044/@IEZA-D0+", "must be positive"},
		{"KJFK-4L/h044/@IEZA-Dx+", "invalid DME distance"},
		{"KJFK-4L/h044/@-D2.0+", "expected a navaid before -D"},
		{"KJFK-4L/h044/@4000", "unknown trigger"},
		{"KJFK-4L/h044/@a2000", "followed by + (at or above) or - (at or below)"},
		{"KJFK-4L/h044/@IEZA-D2.0", "followed by + (at or beyond) or - (within)"},
	} {
		_, err := parseWaypoints(tc.route)
		if err == nil {
			t.Errorf("%s: expected an error", tc.route)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected error %q to include %q", tc.route, err, tc.want)
		}
	}
}

func TestParseTriggerFixWithHyphen(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	const route = "KJFK-4L/h044/@6A1-1-D2.0+/l110/@6A1-1-R090/ph"
	wps, err := parseWaypoints(route)
	if err != nil {
		t.Fatal(err)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %+v", groups)
	}
	if u := groups[0].Until; u.Type != WaypointActionDME || u.DMEFix != "6A1-1" || u.DMEDistance != 2 {
		t.Errorf("unexpected DME trigger %+v", u)
	}
	if u := groups[1].Until; u.Type != WaypointActionRadial || u.RadialFix != "6A1-1" || u.Radial != 90 {
		t.Errorf("unexpected radial trigger %+v", u)
	}
	if got := wps.Encode(); got != route {
		t.Errorf("expected %q to round-trip, got %q", route, got)
	}
}

func TestEncodeTriggerRoundTrip(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, route := range []string{
		"KHLN-23/h054/@a4277+/l274/@HLN-R322/tHLN-R322/@a8100+/rd PXR",
		"KEWR-4L/h219/@a500+/l190/@ILSQ-D2.3+/r220/c10000",
		"KDVT-7R/h254/@a1878+/r060/@PXR-R336/tPXR-R336/@a4000+/ld PXR",
		"RNGRR/h200/@crs220 WAVEY",
		"KJFK-4L/ho5E/@a2500+ PONAE",
	} {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		if got := wps.Encode(); got != route {
			t.Errorf("expected %q to round-trip, got %q", route, got)
		}
	}
}

func TestParseClimbDescendAltitudes(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, route := range []string{"MERIT/c50", "MERIT/c0", "MERIT/d45", "MERIT/c60100", "MERIT/d1250"} {
		if _, err := parseWaypoints(route); err == nil {
			t.Errorf("%s: expected an error", route)
		} else if !strings.Contains(err.Error(), "multiple of 100 between 100 and 60000 feet") {
			t.Errorf("%s: unexpected error %q", route, err)
		}
	}

	wps, err := parseWaypoints("MERIT/c5000 ROBER/d3000")
	if err != nil {
		t.Fatal(err)
	}
	if a := wps[0].ActionGroups()[0].Actions; a.ClimbAltitude != 5000 || a.DescendAltitude != 0 {
		t.Errorf("unexpected actions %+v", a)
	}
	if a := wps[1].ActionGroups()[0].Actions; a.DescendAltitude != 3000 || a.ClimbAltitude != 0 {
		t.Errorf("unexpected actions %+v", a)
	}

	if _, err := parseWaypoints("GLRIA/a3000+/lhilpt1.0min/pta70000"); err == nil ||
		!strings.Contains(err.Error(), "between 0 and 60000 feet") {
		t.Errorf("expected a range error for /pta70000, got %v", err)
	}
}

func TestCheckBasicsCatchesHundredsOfFeetAltitudes(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	errors := func(route string) string {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		WaypointArray(wps).CheckOverflight(&e, nil, func(string) bool { return true })
		return e.String()
	}

	if errs := errors("CAMRN/d200 ZULAB/d1800 KJFK-31L/d100"); !strings.Contains(errs, "/d200 is below") ||
		!strings.Contains(errs, "/d20000") || strings.Contains(errs, "/d100 ") {
		t.Errorf("expected only /d200 to be flagged, got %q", errs)
	}
	if errs := errors("MERIT/c300 KLGA-4/c1500"); !strings.Contains(errs, "/c300 is below") ||
		!strings.Contains(errs, "/c30000") {
		t.Errorf("expected /c300 to be flagged, got %q", errs)
	}
	if errs := errors("CAMRN/d2000 ZULAB/d1800 KJFK-31L/d100"); errs != "" {
		t.Errorf("unexpected errors for altitudes in feet: %q", errs)
	}
	if errs := errors("DHP GLRIA/a3000+/lhilpt1.0min/pta45/iaf PIANA/a3000+/faf VEPCO/a2000+"); !strings.Contains(errs, "/pta45 is below") ||
		!strings.Contains(errs, "/pta4500") {
		t.Errorf("expected /pta45 to be flagged, got %q", errs)
	}
	if errs := errors("DHP GLRIA/a3000+/lhilpt1.0min/pta3000/iaf PIANA/a3000+/faf VEPCO/a2000+"); errs != "" {
		t.Errorf("unexpected errors for procedure turn altitude in feet: %q", errs)
	}
}

func TestCheckDepartureAltitudesBelowFieldElevation(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	errors := func(route string, elevation int) string {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		WaypointArray(wps).CheckDeparture(&e, elevation, nil, func(string) bool { return true })
		return e.String()
	}

	if errs := errors("KDEN-16R/h170/c3000 ROCKI", 5434); !strings.Contains(errs, "/c3000 is at or below the 5434' field elevation") {
		t.Errorf("expected /c3000 to be flagged at Denver, got %q", errs)
	}
	if errs := errors("KDEN-16R/h170/c7000 ROCKI/d5000", 5434); !strings.Contains(errs, "/d5000 is at or below") {
		t.Errorf("expected /d5000 to be flagged at Denver, got %q", errs)
	}
	if errs := errors("KDEN-16R/h170/c7000 ROCKI/d10000", 5434); errs != "" {
		t.Errorf("unexpected errors: %q", errs)
	}
}

func TestWaypointActionTerminationEncoded(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	for _, s := range []string{"/@a500+", "/@a3000-", "/@IEZA-D4.0+", "/@ILSQ-D2.3-", "/@crs220", "/@HLN-R322", "/@d7.9"} {
		wps, err := parseWaypoints("FIX/h039" + s + " NEXT/h100")
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		groups := wps[0].ActionGroups()
		if got := groups[0].Until.Encoded(); got != s {
			t.Errorf("%s: encoded as %q", s, got)
		}
		if got := groups[0].Encoded(); got != "/h039"+s {
			t.Errorf("%s: group encoded as %q", s, got)
		}
		if got := wps[1].ActionGroups()[0].Until.Encoded(); got != "" {
			t.Errorf("%s: open-ended group encoded as %q", s, got)
		}
	}
}

func TestProcedureTurnLegLimit(t *testing.T) {
	for _, tc := range []struct {
		pt          ProcedureTurn
		appr        ApproachType
		nm, minutes float32
	}{
		{ProcedureTurn{NmLimit: 6}, ILSApproach, 6, 0},
		{ProcedureTurn{MinuteLimit: 2}, RNAVApproach, 0, 2},
		{ProcedureTurn{}, ILSApproach, 0, 1},
		{ProcedureTurn{}, LocalizerApproach, 0, 1},
		{ProcedureTurn{}, VORApproach, 0, 1},
		{ProcedureTurn{}, RNAVApproach, 4, 0},
		{ProcedureTurn{}, VisualApproach, 0, 0},
	} {
		nm, minutes := tc.pt.LegLimit(tc.appr)
		if nm != tc.nm || minutes != tc.minutes {
			t.Errorf("%+v %v: got %v nm, %v minutes; want %v, %v", tc.pt, tc.appr, nm, minutes, tc.nm, tc.minutes)
		}
	}
}

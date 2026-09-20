// pkg/aviation/route_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"maps"
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

// The test locator knows no airways; tests that need one set testDB.Airways.

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

// samePosition reports whether two lat-longs are the same point to within a
// hundredth of a mile. Comparing them exactly is hostage to the compiler's
// freedom to fuse a multiply into the following add, which it takes on arm64
// but not on amd64: a point interpolated along a leg then lands an ulp or two
// from the same point computed any other way.
func samePosition(a, b math.Point2LL) bool {
	return math.NMDistance2LL(a, b) < 0.01
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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	if !wps[0].HasLandAction() {
		t.Fatal("expected legacy land modifier to apply after action group")
	}
}

func TestParseActionGroupClearApproachAndDuplicateAltitudes(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	wps, err := parseWaypoints("AROSLY/intercept FORDS")
	if err != nil {
		t.Fatal(err)
	}
	if !wps[0].HasInterceptApproachAction() {
		t.Fatal("expected /intercept to set the InterceptApproach action")
	}
	if wps[1].HasInterceptApproachAction() {
		t.Fatal("expected /intercept to apply only to its waypoint")
	}

	encoded := WaypointArray(wps).Encode()
	if !strings.Contains(encoded, "AROSLY/intercept") {
		t.Fatalf("expected encoded route to round-trip /intercept, got %q", encoded)
	}
}

// TestSequencedRemovalActions checks that /delete, /land and /intercept join
// the action group they are written in rather than applying to the whole fix,
// so that a trigger can hold them off.
func TestSequencedRemovalActions(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	for _, route := range []string{"AROSLY/delete FORDS", "AROSLY/land FORDS", "AROSLY/intercept FORDS"} {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		if got := wps.Encode(); got != route {
			t.Errorf("got %q, want %q", got, route)
		}
	}

	const held = "AROSLY/ph/@a4000+/delete FORDS"
	wps, err := parseWaypoints(held)
	if err != nil {
		t.Fatal(err)
	}
	groups := wps[0].ActionGroups()
	if len(groups) != 2 {
		t.Fatalf("expected 2 action groups, got %d", len(groups))
	}
	if groups[0].Actions.Delete {
		t.Error("/delete after the trigger landed in the group before it")
	}
	if !groups[1].Actions.Delete {
		t.Error("/delete didn't land in the group after the trigger")
	}
	if got := wps.Encode(); got != held {
		t.Errorf("got %q, want %q", got, held)
	}
}

func TestCheckArrivalInterceptRequiresApproach(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: map[string][]Airway{
		"Q75": {{Name: "Q75", Fixes: []AirwayFix{{Fix: "BIGGY"}, {Fix: "MIDDL"}, {Fix: "TEUFL"}}}},
	}}
	t.Cleanup(func() { testDB = oldDB })

	loc := testLocator{
		"BIGGY": math.Point2LL{-74.5, 40.4},
		"MIDDL": math.Point2LL{-75.5, 39.4},
		"TEUFL": math.Point2LL{-76.5, 38.4},
		"TPA":   math.Point2LL{-82.5, 28.0},
	}

	// SLI341/019 is a radial/DME fix: the database has a handful and none of
	// them can be placed, so they drop out rather than derailing the route.
	wps := RouteWaypoints(testLocator{}, "BIGGY Q75 TEUFL DADES2 SLI341/019 TPA").
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
	oldDB := testDB
	testDB = testDatabase{
		Airports: map[ICAOAirportCode]testAirport{
			"KSAN": {Id: "KSAN", LocalCode: "SAN", STARs: map[string]STAR{"LUCKI1": {}}},
			"KJFK": {Id: "KJFK", LocalCode: "JFK", STARs: map[string]STAR{"LENDY6": {}, "PARCH4": {}}},
			"KFLL": {Id: "KFLL", LocalCode: "FLL", STARs: map[string]STAR{"CUUDA4": {}}},
		},
		Airways: map[string][]Airway{"Q86": nil, "J121": nil},
	}
	t.Cleanup(func() { testDB = oldDB })

	for _, tc := range []struct {
		route       string
		icao        ICAOAirportCode
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
		star, entry := RouteSTAR(testLocator{}, tc.route, tc.icao)
		if star != tc.star || entry != tc.entry {
			t.Errorf("RouteSTAR(testLocator{}, %q, %s) = %q, %q; want %q, %q",
				tc.route, tc.icao, star, entry, tc.star, tc.entry)
		}
	}
}

func TestTrimDepartureAirportTokens(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{
		Airports: map[ICAOAirportCode]testAirport{
			"5A8":  {Id: "5A8", LocalCode: "5A8"},
			"KDRA": {Id: "KDRA", LocalCode: "NV65"},
			"KJFK": {Id: "KJFK", LocalCode: "JFK"},
			"KMTJ": {Id: "KMTJ", LocalCode: "MTJ"},
			"KOAK": {Id: "KOAK", LocalCode: "OAK"},
			"KSEA": {Id: "KSEA", LocalCode: "SEA"},
			"PABE": {Id: "PABE", LocalCode: "BET"},
			"PANC": {Id: "PANC", LocalCode: "ANC"},
			"PHKO": {Id: "PHKO", LocalCode: "KOA"},
			"PHOG": {Id: "PHOG", LocalCode: "OGG"},
		},
		Airways: map[string][]Airway{"J133": nil, "J501": nil, "V5": nil, "V23": nil, "V361": nil},
	}
	t.Cleanup(func() { testDB = oldDB })

	for _, tc := range []struct {
		route string
		icao  ICAOAirportCode
		want  string
	}{
		{"KJFK DEEZZ6 CANDR J60 DJB", "KJFK", "DEEZZ6 CANDR J60 DJB"},
		// The origin's id sits behind the SID token.
		{"MAUI5 OGG LNY JULLE5", "PHOG", "MAUI5 LNY JULLE5"},
		{"OGG LNY JULLE5", "PHOG", "LNY JULLE5"},
		{"OAK6 OAK DEDHD RBL LMT HAWKZ8", "KOAK", "OAK6 DEDHD RBL LMT HAWKZ8"},
		{"BET GASTO", "PABE", "GASTO"},
		// The first BET names the airport; the second is the VOR, J501's entry.
		{"BET BET J501 SQA AMOTT4", "PABE", "BET J501 SQA AMOTT4"},
		// The id doubles as the VOR that enters the airway.
		{"MTJ2 MTJ V361 ICIES V484 HAQHY SSKII4", "KMTJ", "MTJ2 MTJ V361 ICIES V484 HAQHY SSKII4"},
		{"KOA V5 MYNAH V11 UPP", "PHKO", "KOA V5 MYNAH V11 UPP"},
		// An airway with nothing after it can never expand, so its entry is
		// no reason to keep the airport token.
		{"MONTN2 SEA V23", "KSEA", "MONTN2 V23"},
		// TED is a navaid on Anchorage's field, not an id of the airport; it
		// is flown (the TURN8 initial climb goes to it) and stays.
		{"TED SQA VIDDA V319 WEEKE BET", "PANC", "TED SQA VIDDA V319 WEEKE BET"},
		{"ANC TED ELLAM OMSUN", "PANC", "TED ELLAM OMSUN"},
		{"TED", "PANC", "TED"},
		// Plenty of airport ids end in a digit; one of those names the
		// airport, not a procedure.
		{"NV65 BTY MISEN", "KDRA", "BTY MISEN"},
		{"5A8 AKN", "5A8", "AKN"},
		{"", "PHOG", ""},
	} {
		got := strings.Join(TrimDepartureAirportTokens(testLocator{}, strings.Fields(tc.route), tc.icao), " ")
		if got != tc.want {
			t.Errorf("TrimDepartureAirportTokens(testLocator{}, %q, %s) = %q, want %q", tc.route, tc.icao, got, tc.want)
		}
	}
}

func TestTrimDestinationAirportTokens(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{
		Airports: map[ICAOAirportCode]testAirport{
			"KDRA": {Id: "KDRA", LocalCode: "NV65"},
			"KPDX": {Id: "KPDX", LocalCode: "PDX"},
			"PABE": {Id: "PABE", LocalCode: "BET"},
			"PADL": {Id: "PADL", LocalCode: "DLG"},
			"PHTO": {Id: "PHTO", LocalCode: "ITO"},
		},
		Airways: map[string][]Airway{"J501": nil, "V2": nil, "V16": nil, "V319": nil},
	}
	t.Cleanup(func() { testDB = oldDB })

	for _, tc := range []struct{ route, icao, want string }{
		{"TRUKN2 GRTFL MACHU TMBRS4 PDX", "KPDX", "TRUKN2 GRTFL MACHU TMBRS4"},
		// The token ends an airway: it is V2's exit fix and stays.
		{"PALAY3 LNY V16 UPP V2 ITO", "PHTO", "PALAY3 LNY V16 UPP V2 ITO"},
		// The last BET names the airport; with it gone the next one does
		// too, and neither ends an airway.
		{"TED SQA VIDDA V319 WEEKE BET BET", "PABE", "TED SQA VIDDA V319 WEEKE"},
		// The second BET is J501's exit and stays.
		{"TED J501 BET BET", "PABE", "TED J501 BET"},
		{"ENA DLG DLG", "PADL", "ENA"},
		{"DLG", "PADL", ""},
		// A digit-ending id at the end of the route names the airport, not a
		// procedure.
		{"BTY MISEN NV65", "KDRA", "BTY MISEN"},
		{"", "PHTO", ""},
	} {
		got := strings.Join(TrimDestinationAirportTokens(testLocator{}, strings.Fields(tc.route), ICAOAirportCode(tc.icao)), " ")
		if got != tc.want {
			t.Errorf("TrimDestinationAirportTokens(testLocator{}, %q, %s) = %q, want %q", tc.route, tc.icao, got, tc.want)
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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

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

func TestRouteAltitudeFloor(t *testing.T) {
	atOrAbove := func(fix string, alt float32) Waypoint {
		wp := Waypoint{Fix: fix}
		wp.SetAltitudeRestriction(MakeAtOrAboveAltitudeRestriction(alt))
		return wp
	}
	atOrBelow := func(fix string, alt float32) Waypoint {
		wp := Waypoint{Fix: fix}
		wp.SetAltitudeRestriction(MakeAtOrBelowAltitudeRestriction(alt))
		return wp
	}

	oldDB := testDB
	testDB = testDatabase{
		Airports: map[ICAOAirportCode]testAirport{
			"KSNA": {Id: "KSNA", SIDs: map[string]SID{
				"FINZZ3": {EnrouteTransitions: map[string]WaypointArray{
					"MISEN": {atOrBelow("STREL", 5000), atOrAbove("FINZZ", 10000),
						atOrAbove("ZOOMM", 16000), {Fix: "MISEN"}},
					"NNAVY": {atOrAbove("FINZZ", 10000), {Fix: "NNAVY"}},
				}},
				"HAWWC3": {EnrouteTransitions: map[string]WaypointArray{
					"IKAYE": {atOrBelow("PIJIN", 4000), {Fix: "IKAYE"}},
				}},
			}},
			"KLAS": {Id: "KLAS", STARs: map[string]STAR{
				"RNDRZ4": {
					Transitions: map[string]WaypointArray{
						"MISEN": {atOrAbove("MISEN", 24000), atOrAbove("WATEV", 19000)},
						"EED":   {{Fix: "EED"}, atOrAbove("ZELMA", 19000)},
					},
					// Descent restrictions off the STAR's runway transitions
					// are no constraint on where the flight cruises.
					RunwayWaypoints: map[string]WaypointArray{
						"RWY19B": {atOrAbove("BUETY", 30000)},
					},
				},
			}},
			// The CIFP covers no procedures outside the US.
			"EGLL": {Id: "EGLL"},
			// JFK's STARs are open routes with no crossing restrictions.
			"KJFK": {Id: "KJFK", STARs: map[string]STAR{
				"PARCH4": {Transitions: map[string]WaypointArray{"ENE": {{Fix: "ENE"}, {Fix: "PARCH"}}}},
			}},
		},
		Airways: map[string][]Airway{"J146": nil},
	}
	t.Cleanup(func() { testDB = oldDB })

	for _, tc := range []struct {
		route    string
		from, to ICAOAirportCode
		floor    int
	}{
		// The SID requires 16,000 and the STAR 24,000.
		{"FINZZ3 MISEN RNDRZ4", "KSNA", "KLAS", 24000},
		// The route files a stale revision of the SID.
		{"FINZZ2 MISEN RNDRZ4", "KSNA", "KLAS", 24000},
		// An airway between the entry fix and the STAR doesn't hide it.
		{"FINZZ3 MISEN J146 RNDRZ4", "KSNA", "KLAS", 24000},
		// Naming no transition of the STAR, every way of flying it is at or
		// above the lowest transition's floor.
		{"FINZZ3 NNAVY BLAZN RNDRZ4", "KSNA", "KLAS", 19000},
		// Only the SID has anything to say.
		{"FINZZ3 NNAVY BLAZN LAS", "KSNA", "KLAS", 10000},
		// A SID with only at-or-below restrictions is no floor at all.
		{"HAWWC3 IKAYE", "KSNA", "KSFO", 0},
		// No CIFP procedures at the origin, and an open-route STAR.
		{"ALLRY TUSKY ENE PARCH4", "EGLL", "KJFK", 0},
		// A route naming no procedure at all.
		{"BIGGY MIDDL TEUFL", "KJFK", "KLAS", 0},
	} {
		if floor := RouteAltitudeFloor(testLocator{}, tc.route, tc.from, tc.to); floor != tc.floor {
			t.Errorf("%q %s->%s: floor = %d, want %d", tc.route, tc.from, tc.to, floor, tc.floor)
		}
	}
}

func TestApplyActions(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	const route = "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"
	for _, tc := range []struct {
		key, value  string
		want        string // encoded route, or "" if an error is expected
		errContains string
	}{
		{key: "GNNRR", value: "hoC35", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/hoC35 FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "GNNRR", value: "hoC35/po5J", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/hoC35/po5J FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		// The value's groups replace the ones charted at the fix, so keeping
		// the charted climb means restating it.
		{key: "KSFO-10L", value: "hoC35", want: "KSFO-10L/hoC35 GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "KSFO-10L", value: "h284/hoC35/@a513+", want: "KSFO-10L/h284/hoC35/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "KSFO-10L", value: "h284/@a513+/hoC35", want: "KSFO-10L/h284/@a513+/hoC35 GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "FIXXX", value: "h123/@a500+/h234/tc/@a1000+/h012", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/tc/@a1000+/h012 BEBOP"},
		{key: "BEBOP", value: "h090", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP/h090"},
		{key: "BEBOP", value: "delete", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP/delete"},
		{key: "BEBOP", value: "land", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP/land"},
		{key: "GNNRR", value: "intercept", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/intercept FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "GNNRR", value: "h180/@d2.5/tc", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/h180/@d2.5/tc FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		// /@crs may end a value when a fix follows on the route.
		{key: "GNNRR", value: "h200/@crs220", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/h200/@crs220 FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		// Properties: /flyover is set, and a restriction replaces the charted
		// restriction of its kind.
		{key: "GNNRR", value: "flyover/ho", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/flyover/ho FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "GNNRR", value: "a5000/s210", want: "KSFO-10L/h284/@a513+ GNNRR/a5000/s210 FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		// A value with no actions or triggers leaves the charted groups alone.
		{key: "FIXXX", value: "a5000", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+ FIXXX/a5000/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		// /ld and /rd give the turn at the fix onto the leg that follows; the
		// flag lands on the next fix, which encodes it on the fix before it.
		{key: "GNNRR", value: "ld", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/ld FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "GNNRR", value: "ho/rd", want: "KSFO-10L/h284/@a513+ GNNRR/a2500+/ho/rd FIXXX/h123/@a500+/h234/@a1000+/h012 BEBOP"},
		{key: "NOPE", value: "hoC35", errContains: "not in the route"},
		{key: "GNNRR/@a513+", value: "hoC35", errContains: "triggers go in the value"},
		{key: "GNNRR/h090", value: "hoC35", errContains: "triggers go in the value"},
		{key: "", value: "hoC35", errContains: "no fix"},
		{key: "GNNRR", value: "", errContains: "empty value"},
		{key: "GNNRR", value: "hoC35/", errContains: "no command found after /"},
		{key: "GNNRR", value: "hoC35,po5J", errContains: "not commas"},
		{key: "GNNRR", value: "@a500+", errContains: "must follow an action"},
		{key: "GNNRR", value: "@a", errContains: "must follow an action"},
		{key: "GNNRR", value: "c123", errContains: "invalid waypoint action /c123"},
		{key: "GNNRR", value: "c7000/d3000", errContains: "cannot specify both"},
		{key: "KSFO-10L", value: "h090/h100", errContains: "multiple heading"},
		{key: "GNNRR", value: "bogus", errContains: "unknown fix modifier"},
		{key: "BEBOP", value: "h200/@crs220", errContains: "no following fix"},
		// Waypoint options that describe route structure or approach coding
		// aren't for "waypoint_actions" to give.
		{key: "GNNRR", value: "iaf", errContains: "only actions"},
		{key: "GNNRR", value: "nopt", errContains: "only actions"},
		{key: "GNNRR", value: "arc10HLN", errContains: "only actions"},
		{key: "GNNRR", value: "airwayV23", errContains: "only actions"},
		{key: "GNNRR", value: "radius2", errContains: "only actions"},
		{key: "BEBOP", value: "ld", errContains: "no following fix to turn to"},
		// Offsets get their own test below; only their keys and the options
		// their values may carry are checked here.
		{key: "GNNRR@0.5", value: "flyover/ho", errContains: `/flyover may not be specified at an "@" offset fix`},
		{key: "GNNRR@0.5", value: "a5000", errContains: `restrictions may not be specified at an "@" offset fix`},
		{key: "GNNRR@0.5", value: "ld/ho", errContains: `/ld and /rd may not be specified at an "@" offset fix`},
		{key: "GNNRR@0", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@1", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@-0.5", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@1.5", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@abc", value: "hoC35", errContains: "invalid offset"},
		// ParseFloat takes these without complaint.
		{key: "GNNRR@nan", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@inf", value: "hoC35", errContains: "must be greater than 0 and less than 1"},
		{key: "GNNRR@", value: "hoC35", errContains: "invalid offset"},
		{key: "@0.5", value: "hoC35", errContains: "no fix"},
		{key: "FIXXX@0.5/@a500+", value: "tc", errContains: "triggers go in the value"},
	} {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		fix, offset, err := parseWaypointActionKey(tc.key)
		if err == nil {
			wps, err = wps.applyActions(fix, offset, tc.value)
		}
		if tc.errContains != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("%q %q: expected error containing %q, got %v", tc.key, tc.value, tc.errContains, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q %q: %v", tc.key, tc.value, err)
		} else if got := wps.Encode(); got != tc.want {
			t.Errorf("%q %q: got %q, want %q", tc.key, tc.value, got, tc.want)
		}
	}
}

// TestInsertOffsetActions covers "waypoint_actions" keys that place their
// actions at a point along the leg after their fix.
func TestInsertOffsetActions(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	// The fixes run east along a line so that the interpolated points fall
	// where arithmetic says they do.
	const nmPerLongitude = 60
	at := func(x float32) math.Point2LL { return math.NM2LL([2]float32{x, 0}, nmPerLongitude) }
	loc := declinationLocator{
		testLocator{"BLARK": at(0), "CKING": at(10), "DRIFT": at(30), "HLN": at(5)},
		map[string]float32{"HLN": 11},
	}

	route := func(t *testing.T, s string) WaypointArray {
		t.Helper()
		wps, err := parseWaypoints(s)
		if err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		wps = wps.InitializeLocations(loc, nmPerLongitude, 0, false, &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		return wps
	}

	// offset does with a key what the SID and arrival paths do: parse it,
	// insert the point, and then place it along with the rest of the route.
	offset := func(wps WaypointArray, key, actions string, e *util.ErrorLogger) (WaypointArray, error) {
		fix, off, err := parseWaypointActionKey(key)
		if err != nil {
			return wps, err
		}
		if wps, err = wps.applyActions(fix, off, actions); err != nil {
			return wps, err
		}
		return wps.InitializeLocations(loc, nmPerLongitude, 0, false, e), nil
	}

	insert := func(t *testing.T, wps WaypointArray, key, actions string) WaypointArray {
		t.Helper()
		var e util.ErrorLogger
		wps, err := offset(wps, key, actions, &e)
		if err != nil {
			t.Fatal(err)
		}
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		return wps
	}

	t.Run("the point goes where the offset says", func(t *testing.T) {
		wps := insert(t, route(t, "BLARK CKING DRIFT"), "CKING@.75", "hoC35")
		if got, want := wps.Encode(), "BLARK CKING _CKING-DRIFT@0.75/hoC35 DRIFT"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := wps[2].Location, at(25); got != want {
			t.Errorf("location %s, want %s", got.DDString(), want.DDString())
		}
	})

	t.Run("several on a leg are flown in order", func(t *testing.T) {
		// The keys sort the other way round: '.' comes before '0'.
		wps := insert(t, route(t, "BLARK CKING DRIFT"), "CKING@.7", "ho")
		wps = insert(t, wps, "CKING@0.5", "spspAB")
		want := "BLARK CKING _CKING-DRIFT@0.5/spspAB _CKING-DRIFT@0.7/ho DRIFT"
		if got := wps.Encode(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("an action's own fix is located", func(t *testing.T) {
		wps := insert(t, route(t, "BLARK CKING DRIFT"), "CKING@.5", "tHLN-R090")
		h := wps[2].ActionGroups()[0].Actions.Heading
		if h.FixLocation != at(5) || h.FixVariation != 11 {
			t.Errorf("heading fix at %s variation %f", h.FixLocation.DDString(), h.FixVariation)
		}
	})

	// Every fix of every charted SID and STAR does locate, so this is the
	// defensive case: were one ever to go missing where slop keeps the route,
	// interpolating toward its zero location would put the point an ocean away
	// and, since that location isn't zero, keep it when the fix is dropped.
	t.Run("the far end of the leg is missing", func(t *testing.T) {
		away := func(x float32) math.Point2LL { return math.NM2LL([2]float32{600 + x, 2400}, nmPerLongitude) }
		slop := declinationLocator{testLocator{"BLARK": away(0), "CKING": away(10)}, nil}

		wps, err := parseWaypoints("BLARK CKING DRIFT")
		if err != nil {
			t.Fatal(err)
		}
		fix, off, err := parseWaypointActionKey("CKING@0.5")
		if err != nil {
			t.Fatal(err)
		}
		if wps, err = wps.applyActions(fix, off, "hoC35"); err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		wps = wps.InitializeLocations(slop, nmPerLongitude, 0, true, &e)
		if got, want := wps.Encode(), "BLARK CKING"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	for _, tc := range []struct {
		name, key, errContains string
		arc                    bool
		airway                 string
		turn                   bool
		already                string
	}{
		{name: "no fix after it", key: "DRIFT@0.5", errContains: "ends the route"},
		{name: "not on the route", key: "NOPE@0.5", errContains: "not in the route"},
		{name: "along a DME arc", key: "CKING@0.5", arc: true, errContains: "DME arc"},
		// The airway's fixes come between the two, so the offset would not be
		// measured to the fix the key's author sees next in the route.
		{name: "along an airway", key: "CKING@0.5", airway: "V23", errContains: "V23 follows it"},
		// The turn at CKING is made toward the point, so the direction on the
		// far fix would never be consulted.
		{name: "a turn direction on the leg", key: "CKING@0.5", turn: true,
			errContains: "a point partway along it would defeat"},
		// Distinct keys, but they name the same fraction of the same leg.
		{name: "two keys on the same point", key: "CKING@0.50", already: "CKING@.5",
			errContains: "another key already puts a point there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wps := route(t, "BLARK CKING DRIFT")
			if tc.arc {
				wps[1].InitExtra().Arc = &DMEArc{Fix: "HLN", Radius: 5}
			}
			if tc.airway != "" {
				wps[1].InitExtra().Airway = tc.airway
			}
			if tc.turn {
				wps[2].SetTurn(TurnLeft)
			}
			if tc.already != "" {
				wps = insert(t, wps, tc.already, "ho")
			}
			var e util.ErrorLogger
			if _, err := offset(wps, tc.key, "ho", &e); err == nil ||
				!strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestResolveActionControllers(t *testing.T) {
	resolve := func(cp ControlPosition) ControlPosition {
		if cp == "C35" {
			return "NCT_C35"
		}
		return cp
	}
	for _, tc := range []struct{ in, want string }{
		{"hoC35", "hoNCT_C35"},
		{"hoC35/po5J", "hoNCT_C35/po5J"},
		{"tc/poC35/h090", "tc/poNCT_C35/h090"},
		{"bogus/hoC35", "bogus/hoNCT_C35"},
		// Triggers and properties pass through untouched.
		{"h284/hoC35/@a513+", "h284/hoNCT_C35/@a513+"},
		{"flyover/poC35", "flyover/poNCT_C35"},
	} {
		if got := ResolveActionControllers(tc.in, resolve); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCheckApproachJoins covers the load-time check that /clearapp and
// /intercept can reach the approach: something from the action's fix onward
// has to be on it, unless a heading has the aircraft vectored to it instead.
func TestCheckApproachJoins(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	ils := &Approach{
		FullName:  "ILS Runway 15R",
		Waypoints: []WaypointArray{{{Fix: "ZARTZ"}, {Fix: "XPRTO"}, {Fix: "KEVVN"}}},
	}

	for _, tc := range []struct {
		name  string
		route string
		err   bool
	}{
		{name: "the action's own fix is on the approach", route: "HUNNN ZARTZ/clearapp WEXUM"},
		{name: "a fix further along is on the approach", route: "HUNNN/clearapp ZARTZ WEXUM"},
		{name: "nothing from the action onward reaches it", route: "HUNNN ZARTZ WEXUM/clearapp", err: true},
		{name: "/intercept has the same reach", route: "HUNNN ZARTZ WEXUM/intercept", err: true},
		{name: "an open-ended heading is vectored to the approach", route: "HUNNN/h330 LURRL WEXUM/clearapp"},
		{name: "the action's own waypoint gives the heading", route: "HUNNN LURRL WEXUM/h330/clearapp"},
		{name: "a terminated heading comes back to the route",
			route: "HUNNN/h330/@a5000- LURRL WEXUM/clearapp", err: true},
		{name: "an open-ended track is not an assigned heading",
			route: "HUNNN/tABC-R090 LURRL WEXUM/clearapp", err: true},
		{name: "a later action group heading is not an assigned heading",
			route: "HUNNN ZARTZ WEXUM/h330/@d1/h340/clearapp", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wps, err := parseWaypoints(tc.route)
			if err != nil {
				t.Fatal(err)
			}

			var e util.ErrorLogger
			wps.checkApproachJoins(ils, &e)

			if e.HaveErrors() != tc.err {
				t.Errorf("got errors %v, want %v: %s", e.HaveErrors(), tc.err, e.String())
			}
		})
	}
}

// TestActionGroupHeading covers the three ways a waypoint's action groups can
// steer the aircraft. nav flies them and scenario validation reads them, so
// the two agree only as long as both go through here.
func TestActionGroupHeading(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	for _, tc := range []struct {
		name string
		fix  string
		want ActionGroupHeadingKind
	}{
		{name: "no actions at all", fix: "WEXUM", want: ActionGroupHeadingNone},
		{name: "a lone open group with no heading", fix: "WEXUM/clearapp", want: ActionGroupHeadingNone},
		{name: "a lone open heading", fix: "WEXUM/h330/clearapp", want: ActionGroupHeadingAssigned},
		{name: "the present heading", fix: "WEXUM/ph", want: ActionGroupHeadingAssigned},
		{name: "a heading that ends at a termination", fix: "WEXUM/h330/@a5000-", want: ActionGroupHeadingManeuvers},
		{name: "a heading in a later group", fix: "WEXUM/h330/@d1/h340", want: ActionGroupHeadingManeuvers},
		{name: "a tracked course", fix: "WEXUM/t330", want: ActionGroupHeadingManeuvers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wps, err := parseWaypoints(tc.fix)
			if err != nil {
				t.Fatal(err)
			}
			if _, got := ActionGroupHeading(wps[0].ActionGroups()); got != tc.want {
				t.Errorf("ActionGroupHeading = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCheckApproachBelowSeaLevelThreshold(t *testing.T) {
	// KTRM's runway 30 threshold is 130' below sea level, so with a 45'
	// threshold crossing height the approach's final fix sits at -85'.
	check := func(thresholdAltitude float32) string {
		wps := WaypointArray{{Fix: "JULLY"}, {Fix: "TEDSE"}, {Fix: "_30_THRESHOLD"}}
		wps[0].SetAltitudeRestriction(MakeAtAltitudeRestriction(3000))
		wps[1].SetFAF(true)
		wps[1].SetAltitudeRestriction(MakeAtAltitudeRestriction(1600))
		wps[2].SetAltitudeRestriction(MakeAtAltitudeRestriction(thresholdAltitude))

		var e util.ErrorLogger
		CheckApproaches(&e, []WaypointArray{wps}, true, nil, func(string) bool { return true })
		return e.String()
	}

	if errs := check(-85); errs != "" {
		t.Errorf("unexpected errors for a below sea level threshold: %q", errs)
	}
	if errs := check(-5000); !strings.Contains(errs, "Invalid altitude restriction") {
		t.Errorf("expected -5000' to be rejected, got %q", errs)
	}
}

func TestCheckSpeedRange(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { testDB = oldDB })

	errors := func(route string) string {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		var e util.ErrorLogger
		WaypointArray(wps).CheckOverflight(&e, nil, func(string) bool { return true })
		return e.String()
	}

	for _, route := range []string{"CAMRN/s20 ZULAB", "CAMRN/s400 ZULAB", "CAMRN/s180-400 ZULAB"} {
		if errs := errors(route); !strings.Contains(errs, "speeds must be between 50 and 350 knots") {
			t.Errorf("%s: expected the speed to be rejected, got %q", route, errs)
		}
	}

	for _, route := range []string{"CAMRN/s130 ZULAB", "CAMRN/s350 ZULAB", "CAMRN/s250- ZULAB",
		"CAMRN/s210+ ZULAB", "CAMRN/s180-210 ZULAB", "CAMRN/sM78 ZULAB"} {
		if errs := errors(route); errs != "" {
			t.Errorf("%s: unexpected errors %q", route, errs)
		}
	}

	if errs := errors("CAMRN/sM8500 ZULAB"); !strings.Contains(errs, "Mach must be between") {
		t.Errorf("expected Mach 85 to be rejected, got %q", errs)
	}
}

func TestSpliceRoutes(t *testing.T) {
	oldDB := testDB
	testDB = testDatabase{Airways: map[string][]Airway{"Q167": nil}}
	t.Cleanup(func() { testDB = oldDB })

	parse := func(route string) WaypointArray {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		// Routes are spliced once they are finalized, which is where an
		// airway entry is folded into the fix it leaves.
		var e util.ErrorLogger
		wps = wps.takeAirways(testLocator{}, &e)
		if e.HaveErrors() {
			t.Fatalf("%s: unexpected errors", route)
		}
		return wps
	}
	fixes := func(wps WaypointArray) string {
		return strings.Join(util.MapSlice(wps, func(wp Waypoint) string { return wp.Fix }), " ")
	}

	// Routes that don't meet at a fix are joined as they are.
	if got := fixes(SpliceRoutes(parse("BUCYK JSTER SSOXS"), parse("BUZRD SEY"))); got != "BUCYK JSTER SSOXS BUZRD SEY" {
		t.Errorf("unexpected spliced route %q", got)
	}

	// The fix they share becomes one waypoint that keeps the exit route's
	// restriction and the airway the departure's route leaves it on.
	base, next := parse("BUCYK JSTER SSOXS/a10000-/flyover"), parse("SSOXS Q167 RIFLE")
	wps := SpliceRoutes(base, next)
	if got := fixes(wps); got != "BUCYK JSTER SSOXS RIFLE" {
		t.Errorf("unexpected spliced route %q", got)
	}
	if ssoxs := wps[2]; ssoxs.Airway() != "Q167" {
		t.Errorf("expected the merged fix to keep airway Q167, got %q", ssoxs.Airway())
	} else if ar := ssoxs.AltitudeRestriction(); ar == nil || ar.Range != [2]float32{0, 10000} {
		t.Errorf("unexpected merged altitude restriction %+v", ar)
	} else if !ssoxs.FlyOver() {
		t.Error("expected the merged fix to still be a flyover")
	}
	if base[2].Airway() != "" || next[0].Fix != "SSOXS" {
		t.Error("expected the routes being spliced to be left alone")
	}

	// Restrictions both of them give become the range that satisfies each,
	// unless there is no such range, where the exit route's governs.
	wps = SpliceRoutes(parse("JSTER SSOXS/a5000+/s250-"), parse("SSOXS/a13000-/s280- BUZRD"))
	if ar := wps[1].AltitudeRestriction(); ar == nil || ar.Range != [2]float32{5000, 13000} {
		t.Errorf("unexpected merged altitude restriction %+v", ar)
	}
	if sr := wps[1].SpeedRestriction(); sr == nil || sr.Range != [2]float32{0, 250} {
		t.Errorf("unexpected merged speed restriction %+v", sr)
	}
	wps = SpliceRoutes(parse("JSTER SSOXS/a15000+"), parse("SSOXS/a13000- BUZRD"))
	if ar := wps[1].AltitudeRestriction(); ar == nil || ar.Range != [2]float32{15000, MaxAltitude} {
		t.Errorf("unexpected merged altitude restriction %+v", ar)
	}

	// The departure route's actions run after the exit route's own.
	wps = SpliceRoutes(parse("JSTER SSOXS/h090/@a5000+/ho2J"), parse("SSOXS/ho5W BUZRD"))
	groups := wps[1].ActionGroups()
	if len(groups) != 3 {
		t.Fatalf("expected 3 action groups, got %+v", groups)
	}
	if groups[0].Until.Altitude != 5000 || groups[1].Actions.HandoffController != "2J" ||
		groups[2].Actions.HandoffController != "5W" {
		t.Errorf("unexpected merged action groups %+v", groups)
	}

	// A route that names the shared fix more than once collapses into it too.
	if got := fixes(SpliceRoutes(parse("JSTER SPLNT"), parse("SPLNT SPLNT STOKD"))); got != "JSTER SPLNT STOKD" {
		t.Errorf("unexpected spliced route %q", got)
	}
}

// testDatabase stands in for the published data in this package's tests,
// which can't reach for the real one: it lives in a package that imports
// this one.
type testDatabase struct {
	Airports            map[ICAOAirportCode]testAirport
	Airways             map[string][]Airway
	Fixes               map[string]bool
	Navaids             map[string]bool
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline
}

type testAirport struct {
	Id         ICAOAirportCode
	Name       string
	Country    string
	ARTCC      string
	Location   math.Point2LL
	Elevation  int
	LocalCode  FAAAirportCode
	Runways    []Runway
	Approaches map[string]Approach
	SIDs       map[string]SID
	STARs      map[string]STAR
}

// testDB is what testLocator answers from; tests set it and restore it.
var testDB testDatabase

func (tl testLocator) AirportLocation(icao ICAOAirportCode) (math.Point2LL, bool) {
	ap, ok := testDB.Airports[icao]
	return ap.Location, ok
}

func (tl testLocator) IsPublishedAirport(icao ICAOAirportCode) bool {
	_, ok := testDB.Airports[icao]
	return ok
}

func (tl testLocator) AirportElevation(icao ICAOAirportCode) int {
	return testDB.Airports[icao].Elevation
}

func (tl testLocator) AirportRunways(icao ICAOAirportCode) []Runway {
	return testDB.Airports[icao].Runways
}

func (tl testLocator) AirportApproaches(icao ICAOAirportCode) map[string]Approach {
	return testDB.Airports[icao].Approaches
}

func (tl testLocator) AirportSIDs(icao ICAOAirportCode) map[string]SID {
	return testDB.Airports[icao].SIDs
}

func (tl testLocator) AirportSTARs(icao ICAOAirportCode) map[string]STAR {
	return testDB.Airports[icao].STARs
}

func (tl testLocator) ValidRunways(icao ICAOAirportCode) string {
	var ids []string
	for _, r := range testDB.Airports[icao].Runways {
		ids = append(ids, r.Id)
	}
	return strings.Join(ids, ", ")
}

func (tl testLocator) AirportFAACode(icao ICAOAirportCode) (FAAAirportCode, bool) {
	ap, ok := testDB.Airports[icao]
	return ap.LocalCode, ok && ap.LocalCode != ""
}

func (tl testLocator) IsNavaidOrFix(fix string) bool {
	return testDB.Navaids[fix] || testDB.Fixes[fix]
}

func (tl testLocator) CheckAirport(role string, id ICAOAirportCode) error {
	if _, ok := testDB.Airports[id]; ok {
		return nil
	}
	return fmt.Errorf("%s airport %q unknown", role, id)
}

func (tl testLocator) Airways(name string) ([]Airway, bool) {
	aw, ok := testDB.Airways[name]
	return aw, ok
}

func (tl testLocator) Airline(icao string) (Airline, bool) {
	al, ok := testDB.Airlines[icao]
	return al, ok
}

func (tl testLocator) AircraftPerformance(acType string) (AircraftPerformance, bool) {
	p, ok := testDB.AircraftPerformance[acType]
	return p, ok
}

func (tl testLocator) IsGAFleet(name string) bool {
	_, ok := testDB.Airlines["N"].Fleets[name]
	return ok
}

func (tl testLocator) GAFleetNames() []string {
	return slices.Collect(maps.Keys(testDB.Airlines["N"].Fleets))
}

func (tl testLocator) InClassBOrC(p math.Point2LL, alt int) bool { return false }

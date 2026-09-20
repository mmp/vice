// aviation/airport_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
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

func TestTrafficRouteSetUnmarshal(t *testing.T) {
	var ts TrafficRouteSet
	if err := json.Unmarshal([]byte(`"MERIT ROBUC3"`), &ts); err != nil {
		t.Fatalf("bare string: %v", err)
	}
	if len(ts) != 1 || ts[0].Route != "MERIT ROBUC3" || ts[0].Aircraft != 0 {
		t.Errorf("bare string gave %+v", ts)
	}

	if err := json.Unmarshal([]byte(`[
		{"route": "ELVAE WHITE Q409 CUUDA3", "aircraft": "jet"},
		{"route": "DIALO V276 SIE", "aircraft": ["prop", "turboprop"]}
	]`), &ts); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ts) != 2 || ts[0].Aircraft != AircraftClassHeavyJet|AircraftClassNonheavyJet ||
		ts[1].Aircraft != AircraftClassProp|AircraftClassTurboprop {
		t.Errorf("list gave %+v", ts)
	}
}

func TestTrafficRouteSetRoutes(t *testing.T) {
	seedTestPerformance(t)

	ts := TrafficRouteSet{
		{Route: "JETRT", Aircraft: AircraftClassHeavyJet | AircraftClassNonheavyJet},
		{Route: "SLOWRT", Aircraft: AircraftClassProp | AircraftClassTurboprop},
		{Route: "ANYRT"},
	}
	for _, tc := range []struct {
		acType string
		want   []string
	}{
		{"B738", []string{"JETRT", "ANYRT"}},
		{"C172", []string{"SLOWRT", "ANYRT"}},
		{"ZZZZ", []string{"ANYRT"}},
	} {
		if got := ts.Routes(tc.acType); !slices.Equal(got, tc.want) {
			t.Errorf("Routes(%s) = %v, want %v", tc.acType, got, tc.want)
		}
	}
}

func TestExitRoutesUnmarshal(t *testing.T) {
	var er ExitRoutes
	if err := json.Unmarshal([]byte(`{"sid": "LGA7", "cleared_altitude": 5000}`), &er); err != nil {
		t.Fatalf("single route: %v", err)
	}
	if len(er) != 1 || er[0].SID != "LGA7" || er[0].ClearedAltitude != 5000 || er[0].Aircraft != 0 {
		t.Errorf("single route gave %+v", er[0])
	}

	if err := json.Unmarshal([]byte(`[
		{"sid": "LGA7", "cleared_altitude": 2000, "aircraft": ["prop", "turboprop"]},
		{"sid": "LGA7", "cleared_altitude": 4000}
	]`), &er); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(er) != 2 || er[0].Aircraft != AircraftClassProp|AircraftClassTurboprop ||
		er[1].Aircraft != 0 || er[1].ClearedAltitude != 4000 {
		t.Errorf("list gave %+v, %+v", er[0], er[1])
	}
}

func TestExitRoutesForAircraft(t *testing.T) {
	seedTestPerformance(t)

	slow := &ExitRoute{ClearedAltitude: 2000, Aircraft: AircraftClassProp | AircraftClassTurboprop}
	heavy := &ExitRoute{ClearedAltitude: 4000, Aircraft: AircraftClassHeavyJet}
	er := ExitRoutes{slow, heavy}

	for _, tc := range []struct {
		acType string
		want   *ExitRoute
	}{
		{"C172", slow},
		{"DH8D", slow},
		{"B77W", heavy},
		{"B738", nil}, // nonheavy jets have nowhere to go
		{"ZZZZ", nil},
	} {
		if got := er.ForAircraft(tc.acType); got != tc.want {
			t.Errorf("ForAircraft(%s) = %+v, want %+v", tc.acType, got, tc.want)
		}
	}

	// A route with no "aircraft" takes everything that is left.
	any := &ExitRoute{ClearedAltitude: 5000}
	if got := append(er, any).ForAircraft("B738"); got != any {
		t.Errorf("catch-all route: got %+v, want %+v", got, any)
	}

	routes := ExitRoutesForAircraft(map[ExitID]ExitRoutes{"NORTH": er, "SOUTH": {any}}, "B738")
	if len(routes) != 1 || routes["SOUTH"] != any {
		t.Errorf("exits with no route for the aircraft should drop out; got %+v", routes)
	}
}

// A misspelled member must be reported as such however the routes are given.
func TestExitRoutesCheckJSONErrors(t *testing.T) {
	for _, js := range []string{
		`{"clered_altitude": 5000}`,
		`[{"sid": "LGA7", "cleared_altitude": 5000}, {"clered_altitude": 5000}]`,
	} {
		var value any
		if err := json.Unmarshal([]byte(js), &value); err != nil {
			t.Fatalf("%s: %v", js, err)
		}

		var e util.ErrorLogger
		var er ExitRoutes
		er.CheckJSONErrors(value, &e)
		if !strings.Contains(e.String(), "clered_altitude") {
			t.Errorf("%s: got errors %q, expected the misspelled member to be named", js, e.String())
		}
	}
}

func TestRouteReachesExit(t *testing.T) {
	wps := func(fixes ...string) WaypointArray {
		return util.MapSlice(fixes, func(f string) Waypoint { return Waypoint{Fix: f} })
	}

	oldDB := DB
	DB = &StaticDatabase{
		Airways: make(map[string][]Airway),
		Airports: map[ICAOAirportCode]FAAAirport{
			"KEWR": {SIDs: map[string]SID{
				"CUTTN3": { // the scenario declares the stale CUTTN2
					Common:             wps("ZORRO", "HANKO"),
					EnrouteTransitions: map[string]WaypointArray{"MGM": wps("ZORRO", "HANKO", "CUTTN", "MGM")},
				},
			}},
		},
	}
	t.Cleanup(func() { DB = oldDB })

	ap := &Airport{
		DepartureRoutes: map[RunwayID]map[ExitID]ExitRoutes{
			"22R": {
				"WHITE": {{SID: "PORTT4"}},
				"HANKO": {{SID: "CUTTN2"}},
				"BNA":   {{SID: "PENCL2"}},
				"OCN.D": {{}},
			},
		},
	}

	for _, tc := range []struct {
		route string
		want  bool
	}{
		{"KEWR ELVAE NECCK WHITE Q409 CRPLR", true}, // exit fix mid-route
		{"CUTTN2 CUTTN MGM MEI", true},              // resumes past the exit on the SID's path
		{"KEWR CUTTN J75 MGM", true},                // same, with no SID token at all
		{"CUTTN2 KEWR CUTTN MGM MEI", true},         // the airport's id behind the SID token
		{"CUTTN2 ZORRO V1 MGM", true},               // leaves the SID before the exit; it lies ahead
		{"CUTTN2 IGB MEI", false},                   // names the SID but never touches its charted path
		{"OCN V23 LAX", true},                       // suffixed exit id
		{"KEWR DIALO V276 SIE", false},              // no exit anywhere
		{"KATL PENCL2 BNA J75 IGB", true},           // SID and exit both named
	} {
		if got := ap.routeReachesExit(testLocator{}, tc.route, "KEWR"); got != tc.want {
			t.Errorf("routeReachesExit(%q) = %v, want %v", tc.route, got, tc.want)
		}
	}
}

func exitTestAirport() *Airport {
	return &Airport{
		DepartureRoutes: map[RunwayID]map[ExitID]ExitRoutes{
			"22R": {"WHITE": {{}}, "HANKO": {{}}, "DEEZZ": {{SID: "DEEZZ6.CANDR"}},
				"WHTIE": {{}}, "VFRN": {{}}},
			"13L": {"WHITE": {{}}, "OCN.D": {{}}, "OCN.T": {{}}},
		},
		Departures: []Departure{
			{Exit: "WHITE"},  // on both runways
			{Exit: "OCN.D"},  // on one of the two
			{Exit: "CANDR"},  // on neither: the route is keyed by its SID name
			{Exit: "HANKO."}, // Base() matches a route but the full id doesn't
		},
		ExitCategories: map[ExitID]string{
			"WHITE": "North",
			"CANDR": "DEEZZ", // used by a departure, so not dead
			"HANKO": "North",
			"OCN":   "West",           // the base fix of two exits
			"OCN.T": "West.Turboprop", // one variant overriding it
			"GLYDE": "South",          // named by no route and no departure
			"VFRN":  "VFR",            // a pseudo-gate the airport means to have
		},
	}
}

func TestCheckExits(t *testing.T) {
	var e util.ErrorLogger
	// WHTIE is the misspelling; VFRN is a pseudo-gate that names no fix on purpose.
	loc := testLocator{"WHITE": {}, "HANKO": {}, "OCN": {}, "CANDR": {}, "GLYDE": {}, "DEEZZ": {}}
	exitTestAirport().checkExits(loc, &e)

	for _, want := range []string{
		`departure exit "CANDR": no runway`,
		`departure exit "HANKO.": no runway`,
		`"exit_categories" exit "GLYDE" is used by no`,
		`"departure_routes" exit "WHTIE" names no fix`,
	} {
		if !strings.Contains(e.String(), want) {
			t.Errorf("expected an error containing %q; got %q", want, e.String())
		}
	}
	// "OCN" is used: it is the base fix of exits that take its category. VFRN
	// names no fix but the airport gives it a category, so it stands.
	for _, unwanted := range []string{"WHITE", "OCN", `"CANDR" is used`, "DEEZZ", "VFRN"} {
		if strings.Contains(e.String(), unwanted) {
			t.Errorf("unexpected error mentioning %q: %q", unwanted, e.String())
		}
	}
}

func TestExitCategory(t *testing.T) {
	ap := exitTestAirport()
	for exit, want := range map[ExitID]string{
		"WHITE":  "North",          // exact
		"OCN.D":  "West",           // no category of its own; takes its base fix's
		"OCN.T":  "West.Turboprop", // its own category wins over its base fix's
		"HANKO.": "North",          // an empty suffix still resolves to the base fix
		"DEEZZ":  "",               // categorized under the fix the SID leads to
		"BUZRD":  "",               // unknown
	} {
		if got := ap.ExitCategory(exit); got != want {
			t.Errorf("ExitCategory(%q) = %q, want %q", exit, got, want)
		}
	}
}

// initializeTestExitRoute runs er.initialize for a departure off a 2nm
// east-facing KXXX runway 9. route, if non-empty, gives the route's
// waypoints, located out ahead of the runway (except the departure end,
// KXXX-27); the route's ClimboutActions, if any, is parsed and applied.
// DB must already map KXXX.
func initializeTestExitRoute(t *testing.T, er ExitRoute, route string) ExitRoute {
	t.Helper()
	er, e := tryInitializeTestExitRoute(t, er, route)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}
	return er
}

// tryInitializeTestExitRoute is initializeTestExitRoute for cases that expect
// initialize to report errors; failures before it still stop the test.
func tryInitializeTestExitRoute(t *testing.T, er ExitRoute, route string) (ExitRoute, *util.ErrorLogger) {
	t.Helper()

	const nmPerLongitude = 60
	at := func(p [2]float32) math.Point2LL {
		return math.NM2LL([2]float32{100 + p[0], 100 + p[1]}, nmPerLongitude)
	}
	r := Runway{Id: "9", Heading: 90, Threshold: at([2]float32{0, 0})}
	rend := Runway{Id: "27", Heading: 270, Threshold: at([2]float32{2, 0})}

	if route != "" {
		wps, err := parseWaypoints(route)
		if err != nil {
			t.Fatal(err)
		}
		for i := range wps {
			if wps[i].Fix == "KXXX-27" {
				wps[i].Location = rend.Threshold
			} else {
				wps[i].Location = at([2]float32{float32(4 + i), 0})
			}
		}
		er.Waypoints = wps
	}
	var override Waypoint
	if er.ClimboutActions != "" {
		var err error
		if override, err = er.parseClimboutActions(); err != nil {
			t.Fatal(err)
		}
	}
	var e util.ErrorLogger
	er.initialize(testLocator{}, "KXXX", "9", r, rend, nmPerLongitude, 0, nil, override, &e)
	return er, &e
}

func TestInitialHeading(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{
		Airways:  make(map[string][]Airway),
		Airports: map[ICAOAirportCode]FAAAirport{"KXXX": {Elevation: 313}},
	}
	t.Cleanup(func() { DB = oldDB })

	initialized := func(t *testing.T, er ExitRoute, route string) string {
		t.Helper()
		return initializeTestExitRoute(t, er, route).Waypoints.Encode()
	}

	// The tower's heading is flown from the mid-runway waypoint, after the
	// centerline track to 400' above the field. It supersedes the SID's own
	// legs from the departure end.
	er := ExitRoute{ClearedAltitude: 5000, InitialHeading: 345}
	if got, want := initialized(t, er, "KXXX-27/h011/@a820+/h011 RIGNZ/a3000+ JCOBY"),
		"9/sid 9-mid/t090/@a713+/h345/sid RIGNZ/a3000+/sid JCOBY/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A SID that starts at a fix rather than the departure end keeps it.
	if got, want := initialized(t, er, "BUTRZ/a3000+ CLTCH KERRK"),
		"9/sid 9-mid/t090/@a713+/h345/sid BUTRZ/a3000+/sid CLTCH/sid KERRK/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Without an initial heading, a charted transition's initial legs are
	// flown once the aircraft is 400' up.
	er = ExitRoute{ClearedAltitude: 5000}
	if got, want := initialized(t, er, "KXXX-27/h284/@a513+ GNNRR/a2500+"),
		"9/sid 9-mid/t090/@a713+/h284/@a513+/sid GNNRR/a2500+/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// The departure end waypoint's restrictions come along with its actions.
	if got, want := initialized(t, er, "KXXX-27/a1500-/h284 GNNRR/a2500+"),
		"9/sid 9-mid/a1500-/t090/@a713+/h284/sid GNNRR/a2500+/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A route with no waypoints of its own still tracks the centerline.
	if got, want := initialized(t, ExitRoute{ClearedAltitude: 5000, InitialHeading: 345}, ""),
		"9/sid 9-mid/t090/@a713+/h345/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Sim actions at the departure end survive an initial heading: the
	// charted legs are superseded but the actions run at 400' with the turn.
	er = ExitRoute{ClearedAltitude: 5000, InitialHeading: 345}
	if got, want := initialized(t, er, "KXXX-27/h011/@a820+/hoC35 RIGNZ/a3000+ JCOBY"),
		"9/sid 9-mid/t090/@a713+/h345/hoC35/sid RIGNZ/a3000+/sid JCOBY/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A departure-end waypoint that only carries sim actions keeps them as
	// their own group, flown once the aircraft is 400' up.
	er = ExitRoute{ClearedAltitude: 5000}
	if got, want := initialized(t, er, "KXXX-27/hoC35 GNNRR/a2500+"),
		"9/sid 9-mid/t090/@a713+/hoC35/sid GNNRR/a2500+/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestClimboutActions(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{
		Airways:  make(map[string][]Airway),
		Airports: map[ICAOAirportCode]FAAAirport{"KXXX": {Elevation: 313}},
	}
	t.Cleanup(func() { DB = oldDB })

	initialized := func(t *testing.T, er ExitRoute, route string) string {
		t.Helper()
		return initializeTestExitRoute(t, er, route).Waypoints.Encode()
	}

	// A bare heading is equivalent to "initial_heading": it supersedes the
	// SID's own legs from the departure end.
	er := ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345"}
	if got, want := initialized(t, er, "KXXX-27/h011/@a820+/h011 RIGNZ/a3000+ JCOBY"),
		"9/sid 9-mid/t090/@a713+/h345/sid RIGNZ/a3000+/sid JCOBY/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := initialized(t, er, "BUTRZ/a3000+ CLTCH KERRK"),
		"9/sid 9-mid/t090/@a713+/h345/sid BUTRZ/a3000+/sid CLTCH/sid KERRK/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := initialized(t, ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345"}, ""),
		"9/sid 9-mid/t090/@a713+/h345/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Further actions come along with the turn, and a /tc marks the route as
	// waiting to contact departure.
	ir := initializeTestExitRoute(t, ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h280/tc"}, "")
	if got, want := ir.Waypoints.Encode(), "9/sid 9-mid/t090/@a713+/h280/tc/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !ir.WaitToContactDeparture {
		t.Errorf("WaitToContactDeparture not set for override with /tc")
	}

	// A trigger splits the actions into ordered groups: fly heading 170 and
	// only contact departure a mile from the turn. The heading is
	// respecified after the trigger, since a triggered group's heading ends
	// with it.
	er = ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h170/@d1.0/h170/tc"}
	if got, want := initialized(t, er, ""),
		"9/sid 9-mid/t090/@a713+/h170/@d1.0/h170/tc/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Sim actions at the departure end run at 400' with the turn, merged
	// into the override's first group; the charted legs are superseded.
	er = ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345/tc"}
	if got, want := initialized(t, er, "KXXX-27/h011/@a820+/hoC35 RIGNZ/a3000+ JCOBY"),
		"9/sid 9-mid/t090/@a713+/h345/hoC35/tc/sid RIGNZ/a3000+/sid JCOBY/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// An override without a heading leaves the charted legs alone and its
	// actions follow them.
	er = ExitRoute{ClearedAltitude: 5000, ClimboutActions: "tc"}
	if got, want := initialized(t, er, "KXXX-27/h284/@a513+ GNNRR/a2500+"),
		"9/sid 9-mid/t090/@a713+/h284/@a513+/tc/sid GNNRR/a2500+/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// The override's restrictions apply at the midpoint, superseding any
	// from departure-end waypoints.
	er = ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345/s210"}
	if got, want := initialized(t, er, "KXXX-27/a1500-/h284 GNNRR/a2500+"),
		"9/sid 9-mid/a1500-/s210/t090/@a713+/h345/sid GNNRR/a2500+/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A /ld or /rd gives the turn made turning on course at the end of the
	// climbout; its flag lands on the first fix of the route, which encodes
	// it on the midpoint waypoint before it.
	er = ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345/ld"}
	if got, want := initialized(t, er, "BUTRZ/a3000+ CLTCH KERRK"),
		"9/sid 9-mid/t090/@a713+/h345/sid/ld BUTRZ/a3000+/sid CLTCH/sid KERRK/sid"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// With no route waypoints there is no fix to turn to.
	_, e := tryInitializeTestExitRoute(t, ExitRoute{ClearedAltitude: 5000, ClimboutActions: "h345/ld"}, "")
	if s := e.String(); !strings.Contains(s, "no fix of the route to turn to") {
		t.Errorf("expected an error for /ld with no route; got: %s", s)
	}

	// Options must be a single /-separated set.
	er = ExitRoute{ClimboutActions: "h345 tc"}
	if _, err := er.parseClimboutActions(); err == nil {
		t.Errorf("no error for climbout actions with multiple waypoints")
	}
	er = ExitRoute{ClimboutActions: "h999"}
	if _, err := er.parseClimboutActions(); err == nil {
		t.Errorf("no error for climbout actions with invalid heading")
	}

	// The parsed waypoint is named so that errors from the checks it goes
	// through carry the "climbout_actions" label.
	er = ExitRoute{ClimboutActions: "hoZZZ"}
	if wp, err := er.parseClimboutActions(); err != nil {
		t.Fatal(err)
	} else {
		var e util.ErrorLogger
		WaypointArray{wp}.checkBasics(&e, nil, func(string) bool { return true })
		if s := e.String(); !strings.Contains(s, "climbout_actions") {
			t.Errorf("expected the error to name climbout_actions: %s", s)
		}
	}
}

// TestDepartureRouteAlongSID covers the check that a departure's route not
// continue along its exit's SID past the exit fix.
func TestDepartureRouteAlongSID(t *testing.T) {
	wps := func(fixes ...string) WaypointArray {
		return util.MapSlice(fixes, func(f string) Waypoint { return Waypoint{Fix: f} })
	}

	oldDB := DB
	DB = &StaticDatabase{
		Airports: map[ICAOAirportCode]FAAAirport{
			"KXXX": {SIDs: map[string]SID{
				// An MSP SCHEP1-style SID: vectors to the first fix, so no
				// runway transitions.
				"AAAAA1": {
					Common:             wps("HUGIR", "MCONL", "SCHEP"),
					EnrouteTransitions: map[string]WaypointArray{"RXANN": wps("HUGIR", "MCONL", "SCHEP", "RXANN")},
				},
				// An MSP COULT7-style SID whose vectored portion overflies a
				// fix (TAXEE) the CIFP doesn't chart.
				"BBBBB2": {
					Common:             wps("COULT"),
					EnrouteTransitions: map[string]WaypointArray{"DLL": wps("COULT", "LMFRY", "DLL")},
				},
				// An MSP WLSTN7-style SID whose fixes are all in its runway
				// transitions.
				"CCCCC3": {
					RunwayTransitions:  map[string]WaypointArray{"9": wps("KXXX-27", "SNINE", "DWIYT", "WLSTN")},
					EnrouteTransitions: map[string]WaypointArray{"GRB": wps("WLSTN", "GRB")},
				},
			}},
		},
	}
	t.Cleanup(func() { DB = oldDB })

	ap := &Airport{
		DepartureRoutes: map[RunwayID]map[ExitID]ExitRoutes{
			"9": {
				"HUGIR.JET": ExitRoutes{&ExitRoute{SID: "AAAAA1"}},
				"RXANN.JET": ExitRoutes{&ExitRoute{SID: "AAAAA1"}},
				"TAXEE":     ExitRoutes{&ExitRoute{SID: "BBBBB2"}},
				"SNINE":     ExitRoutes{&ExitRoute{SID: "CCCCC3"}},
				"NOSID":     ExitRoutes{&ExitRoute{}},
			},
		},
	}

	for _, tc := range []struct {
		name  string
		exit  ExitID
		route string
		want  string // expected new exit in the error, "" for no error
	}{
		{name: "SID fixes spelled past the exit", exit: "HUGIR.JET",
			route: "HUGIR MCONL SCHEP RXANN TEYOU LLUKY", want: "RXANN"},
		{name: "route leaves the SID at the exit", exit: "RXANN.JET",
			route: "RXANN TEYOU LLUKY", want: ""},
		{name: "restrictions on the route's fixes", exit: "HUGIR.JET",
			route: "HUGIR MCONL/a9000+ SCHEP RXANN TEYOU", want: "RXANN"},
		{name: "leaving the SID at the next charted fix is fine", exit: "HUGIR.JET",
			route: "HUGIR MCONL DIRCT", want: ""},
		{name: "two charted fixes past the exit", exit: "HUGIR.JET",
			route: "HUGIR MCONL SCHEP DIRCT", want: "SCHEP"},
		{name: "an exit the CIFP doesn't chart", exit: "TAXEE",
			route: "TAXEE COULT LMFRY DLL DABJU", want: "DLL"},
		{name: "fixes from the runway transition", exit: "SNINE",
			route: "SNINE DWIYT WLSTN GRB", want: "GRB"},
		{name: "an exit whose route names no SID", exit: "NOSID",
			route: "NOSID MCONL SCHEP", want: ""},
		{name: "an off-SID fix right after the exit", exit: "HUGIR.JET",
			route: "HUGIR DIRCT SCHEP", want: ""},
		{name: "the exit ends the route", exit: "HUGIR.JET",
			route: "TONCE HUGIR", want: ""},
	} {
		var e util.ErrorLogger
		dep := Departure{Exit: tc.exit, Route: tc.route}
		var err error
		if dep.RouteWaypoints, err = parseWaypoints(tc.route); err != nil {
			t.Fatalf("%s: %v", tc.route, err)
		}
		ap.checkDepartureRouteAlongSID(testLocator{}, "KXXX", &dep, &e)
		if tc.want == "" {
			if e.HaveErrors() {
				t.Errorf("%s: unexpected error: %s", tc.name, e.String())
			}
		} else if !e.HaveErrors() {
			t.Errorf("%s: no error returned", tc.name)
		} else if s := e.String(); !strings.Contains(s, "make "+tc.want+" the exit") {
			t.Errorf("%s: error doesn't advise exit %s: %s", tc.name, tc.want, s)
		}
	}
}

// TestChartedSIDRoute covers the check that a departure route spelling out
// the SID it names as the CIFP charts it should name the SID and give only
// what it adds to it.
func TestChartedSIDRoute(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	const nmPerLongitude = 60
	at := func(p [2]float32) math.Point2LL {
		return math.NM2LL([2]float32{100 + p[0], 100 + p[1]}, nmPerLongitude)
	}
	r := Runway{Id: "9", Heading: 90, Threshold: at([2]float32{0, 0})}
	rend := Runway{Id: "27", Heading: 270, Threshold: at([2]float32{2, 0})}

	route := func(s string) WaypointArray {
		wps, err := parseWaypoints(s)
		if err != nil {
			t.Fatal(err)
		}
		return wps
	}
	// BUTRZ4 leaves runway 9 on a charted heading; GNNRR2 has no runway
	// transition, so a route flying it needs a tower heading of its own.
	butrz4 := SID{
		RunwayTransitions: map[string]WaypointArray{"9": route("KXXX-27/h011/@a820+")},
		Common:            route("BUTRZ/a3000+"),
		EnrouteTransitions: map[string]WaypointArray{
			"CLTCH": route("BUTRZ/a3000+ CLTCH"), "KERRK": route("BUTRZ/a3000+ KERRK")},
	}
	gnnrr2 := SID{
		Common:             route("GNNRR/a2500+"),
		EnrouteTransitions: map[string]WaypointArray{"CLTCH": route("GNNRR/a2500+ CLTCH")},
	}
	DB.Airports = map[ICAOAirportCode]FAAAirport{
		"KXXX": {Elevation: 313, SIDs: map[string]SID{"BUTRZ4": butrz4, "GNNRR2": gnnrr2}},
	}

	loc := testLocator{"KXXX-27": rend.Threshold, "BUTRZ": at([2]float32{6, 0}),
		"GNNRR": at([2]float32{6, 2}), "CLTCH": at([2]float32{10, 0}), "KERRK": at([2]float32{10, 4})}

	for _, tc := range []struct {
		name  string
		sid   string
		route string
		want  string // the error's advice; "" for no error
	}{
		{
			name:  "the SID off the runway as charted",
			sid:   "BUTRZ4",
			route: "KXXX-27/h011/@a820+ BUTRZ/a3000+ CLTCH",
			want:  `drop them and let "sid" give the route`,
		},
		{
			name:  "an enroute transition named in the SID",
			sid:   "BUTRZ4.KERRK",
			route: "KXXX-27/h011/@a820+ BUTRZ/a3000+ KERRK",
			want:  `drop them and let "sid" give the route`,
		},
		{
			name:  "actions of its own along the SID",
			sid:   "BUTRZ4",
			route: "KXXX-27/h011/@a820+ BUTRZ/a3000+/hoC35 CLTCH/spspAB",
			want:  `drop them and give "waypoint_actions": {"BUTRZ":"hoC35","CLTCH":"spspAB"}`,
		},
		{
			name:  "a tower heading where the CIFP charts no runway transition",
			sid:   "GNNRR2",
			route: "KXXX-27/h345 GNNRR/a2500+ CLTCH",
			want:  `drop them and give "initial_heading": 345`,
		},
		{
			name:  "a tower heading and actions",
			sid:   "GNNRR2",
			route: "KXXX-27/h345 GNNRR/a2500+/hoC35 CLTCH",
			want:  `drop them and give "initial_heading": 345 and "waypoint_actions": {"GNNRR":"hoC35"}`,
		},
		{
			// "initial_heading" only stands in for an uncharted transition,
			// but a "waypoint_actions" value can replace the charted legs.
			name:  "a tower heading over a charted runway transition",
			sid:   "BUTRZ4",
			route: "KXXX-27/h345 BUTRZ/a3000+ CLTCH",
			want:  `drop them and give "waypoint_actions": {"KXXX-27":"h345"}`,
		},
		{
			name:  "a restriction the CIFP hasn't got",
			sid:   "GNNRR2",
			route: "KXXX-27/h345 GNNRR/a4000 CLTCH",
		},
		{
			name:  "a fix the SID hasn't got",
			sid:   "GNNRR2",
			route: "KXXX-27/h345 GNNRR/a2500+ KERRK CLTCH",
		},
		{
			name:  "a turn direction, which an initial heading can't give",
			sid:   "GNNRR2",
			route: "KXXX-27/r345 GNNRR/a2500+ CLTCH",
		},
		{
			name:  "a restriction at the departure end, which has nowhere to go",
			sid:   "GNNRR2",
			route: "KXXX-27/a1500-/h345 GNNRR/a2500+ CLTCH",
		},
		{
			name:  "a route that names no SID",
			route: "KXXX-27/h011/@a820+ BUTRZ/a3000+ CLTCH",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e util.ErrorLogger
			er := ExitRoute{SID: tc.sid, ClearedAltitude: 5000}
			er.Waypoints = route(tc.route).InitializeLocations(loc, nmPerLongitude, 0, true, &e)
			if e.HaveErrors() {
				t.Fatal(e.String())
			}
			er.checkChartedSIDRoute(loc, "KXXX", "9", []ExitID{"CLTCH"}, r, rend, loc, nmPerLongitude, 0, &e)

			if tc.want == "" {
				if e.HaveErrors() {
					t.Errorf("unexpected error: %s", e.String())
				}
				return
			}
			advice := e.String()
			if !strings.Contains(advice, tc.want) {
				t.Errorf("expected advice %q; got: %s", tc.want, advice)
				return
			}

			// Following the advice must fly the hand-written route: apply its
			// "waypoint_actions" to the SID from the CIFP and compare, past
			// any departure-end waypoints an "initial_heading" stands in for.
			actions := make(map[string]string)
			if _, after, ok := strings.Cut(advice, `"waypoint_actions": `); ok {
				obj := after[:strings.Index(after, "}")+1]
				if err := json.Unmarshal([]byte(obj), &actions); err != nil {
					t.Fatalf("%s: %v", obj, err)
				}
			}
			initialHeading := strings.Contains(advice, `"initial_heading": `)

			sid, transition, _ := strings.Cut(tc.sid, ".")
			charted, err := sidWaypoints(loc, "KXXX", sid, transition, "9", "CLTCH", initialHeading)
			if err != nil {
				t.Fatal(err)
			}
			var scratch util.ErrorLogger
			amend := ExitRoute{WaypointActions: actions}
			amended := amend.amendSIDWaypoints(charted, &scratch)
			amended = amended.InitializeLocations(loc, nmPerLongitude, 0, true, &scratch)
			if scratch.HaveErrors() {
				t.Fatal(scratch.String())
			}
			hand := er.Waypoints
			for initialHeading && len(hand) > 0 && atDepartureEnd(hand[0], r, rend, nmPerLongitude) {
				hand = hand[1:]
			}
			if !amended.sameRoute(hand) {
				t.Errorf("the advice flies %q, not the route's %q", amended.Encode(), hand.Encode())
			}
		})
	}
}

// TestSIDOffsetActions covers "waypoint_actions" that place their actions at
// a point along one of the SID's legs.
func TestSIDOffsetActions(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airways: make(map[string][]Airway)}
	t.Cleanup(func() { DB = oldDB })

	const nmPerLongitude = 60
	at := func(p [2]float32) math.Point2LL {
		return math.NM2LL([2]float32{100 + p[0], 100 + p[1]}, nmPerLongitude)
	}
	rend := Runway{Id: "27", Heading: 270, Threshold: at([2]float32{2, 0})}

	route := func(s string) WaypointArray {
		wps, err := parseWaypoints(s)
		if err != nil {
			t.Fatal(err)
		}
		return wps
	}
	butrz4 := SID{
		RunwayTransitions:  map[string]WaypointArray{"9": route("KXXX-27/h011/@a820+")},
		Common:             route("BUTRZ/a3000+"),
		EnrouteTransitions: map[string]WaypointArray{"CLTCH": route("BUTRZ/a3000+ CLTCH")},
	}
	DB.Airports = map[ICAOAirportCode]FAAAirport{
		"KXXX": {Elevation: 313, SIDs: map[string]SID{"BUTRZ4": butrz4}},
	}
	loc := testLocator{"KXXX-27": rend.Threshold, "BUTRZ": at([2]float32{6, 0}),
		"CLTCH": at([2]float32{10, 0})}

	amended := func(t *testing.T, actions map[string]string, e *util.ErrorLogger) WaypointArray {
		t.Helper()
		er := ExitRoute{SID: "BUTRZ4", ClearedAltitude: 5000, WaypointActions: actions}
		wps, err := sidWaypoints(loc, "KXXX", "BUTRZ4", "CLTCH", "9", "CLTCH", false)
		if err != nil {
			t.Fatal(err)
		}
		wps = er.amendSIDWaypoints(wps, e)
		return wps.InitializeLocations(loc, nmPerLongitude, 0, true, e)
	}

	t.Run("a point along a leg", func(t *testing.T) {
		var e util.ErrorLogger
		wps := amended(t, map[string]string{"BUTRZ@0.25": "hoC35"}, &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		want := "KXXX-27/h011/@a820+ BUTRZ/a3000+ _BUTRZ-CLTCH@0.25/hoC35 CLTCH"
		if got := wps.Encode(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := wps[2].Location, at([2]float32{7, 0}); !samePosition(got, want) {
			t.Errorf("location %s, want %s", got.DDString(), want.DDString())
		}
	})

	// The departure end waypoint is stripped once the aircraft is flying the
	// runway centerline to 400', but a point out along its leg is a fix of the
	// route like any other.
	t.Run("a point along the leg off the runway", func(t *testing.T) {
		var e util.ErrorLogger
		er := ExitRoute{SID: "BUTRZ4", ClearedAltitude: 5000}
		er.Waypoints = amended(t, map[string]string{"KXXX-27@0.5": "hoC35"}, &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		r := Runway{Id: "9", Heading: 90, Threshold: at([2]float32{0, 0})}
		er.initialize(loc, "KXXX", "9", r, rend, nmPerLongitude, 0, nil, Waypoint{}, &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		want := "9/sid 9-mid/t090/@a713+/h011/@a820+/sid _KXXX-27-BUTRZ@0.5/hoC35/sid BUTRZ/a3000+/sid CLTCH/sid"
		if got := er.Waypoints.Encode(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// A point that lands in the window the departure end of the runway claims
	// would be stripped with it rather than flown where it was asked for.
	t.Run("a point at the departure end of the runway", func(t *testing.T) {
		var e util.ErrorLogger
		er := ExitRoute{SID: "BUTRZ4", ClearedAltitude: 5000}
		er.Waypoints = amended(t, map[string]string{"KXXX-27@0.05": "hoC35"}, &e)
		if e.HaveErrors() {
			t.Fatal(e.String())
		}
		r := Runway{Id: "9", Heading: 90, Threshold: at([2]float32{0, 0})}
		er.initialize(loc, "KXXX", "9", r, rend, nmPerLongitude, 0, nil, Waypoint{}, &e)
		if !strings.Contains(e.String(), "departure end of the runway") {
			t.Errorf("didn't get the expected error; got: %s", e.String())
		}
	})

	t.Run("a point along the leg after the exit", func(t *testing.T) {
		var e util.ErrorLogger
		amended(t, map[string]string{"CLTCH@0.5": "hoC35"}, &e)
		if !strings.Contains(e.String(), "ends the route") {
			t.Errorf("didn't get the expected error; got: %s", e.String())
		}
	})

	// A SID's own fixes are resolved with slop; a fix an action names is no
	// different at an offset than at the fix itself.
	t.Run("a fix the actions name is resolved with the same slop as the route", func(t *testing.T) {
		var e util.ErrorLogger
		amended(t, map[string]string{"BUTRZ@0.5": "tNOPE-R090"}, &e)
		if e.HaveErrors() {
			t.Errorf("unexpected errors: %s", e.String())
		}
	})
}

func TestExitRouteFirstFixBehindRunway(t *testing.T) {
	oldDB := DB
	DB = &StaticDatabase{Airports: map[ICAOAirportCode]FAAAirport{"KXXX": {Elevation: 313}}}
	t.Cleanup(func() { DB = oldDB })

	const nmPerLongitude = 60
	// A 2 nm runway running east, away from the origin so that no location
	// reads as unset.
	at := func(p [2]float32) math.Point2LL {
		return math.NM2LL([2]float32{100 + p[0], 100 + p[1]}, nmPerLongitude)
	}
	r := Runway{Id: "9", Heading: 90, Threshold: at([2]float32{0, 0})}
	rend := Runway{Id: "27", Heading: 270, Threshold: at([2]float32{2, 0})}
	for _, tc := range []struct {
		name string
		at   [2]float32 // nm east, north of the threshold
		bad  bool
	}{
		// The departure end itself is taken for the runway rather than a fix.
		{name: "departure end", at: [2]float32{2, 0}},
		{name: "just past the departure end", at: [2]float32{2.3, 0}},
		{name: "short of the departure end", at: [2]float32{1.6, 0}, bad: true},
		{name: "abeam, short of the departure end", at: [2]float32{1.5, 1}, bad: true},
		{name: "threshold", at: [2]float32{0, 0}, bad: true},
		{name: "well behind the threshold", at: [2]float32{-1, 0}},
	} {
		var e util.ErrorLogger
		er := ExitRoute{ClearedAltitude: 5000, Waypoints: WaypointArray{{Fix: "FIRST", Location: at(tc.at)}}}
		er.initialize(testLocator{}, "KXXX", "9", r, rend, nmPerLongitude, 0, nil, Waypoint{}, &e)
		if e.HaveErrors() != tc.bad {
			t.Errorf("%s: errors %v, want error %v", tc.name, e.String(), tc.bad)
		}
	}
}

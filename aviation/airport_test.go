// aviation/airport_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

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
		{"CUTTN2 MGM MEI", true},                    // SID reaching an exit
		{"CUTTN1 MGM MEI", true},                    // stale SID revision
		{"OCN V23 LAX", true},                       // suffixed exit id
		{"KEWR DIALO V276 SIE", false},              // no exit anywhere
		{"KATL PENCL2 BNA J75 IGB", true},           // SID and exit both named
	} {
		if got := ap.routeReachesExit(tc.route, "KEWR"); got != tc.want {
			t.Errorf("routeReachesExit(%q) = %v, want %v", tc.route, got, tc.want)
		}
	}
}

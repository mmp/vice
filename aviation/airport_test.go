// aviation/airport_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"slices"
	"testing"
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

func TestRouteReachesExit(t *testing.T) {
	ap := &Airport{
		DepartureRoutes: map[RunwayID]map[ExitID]*ExitRoute{
			"22R": {
				"WHITE": &ExitRoute{SID: "PORTT4"},
				"HANKO": &ExitRoute{SID: "CUTTN2"},
				"BNA":   &ExitRoute{SID: "PENCL2"},
				"OCN.D": &ExitRoute{},
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

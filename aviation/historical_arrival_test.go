// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"strings"
	"testing"
)

func TestHistoricalArrivalValidation(t *testing.T) {
	old := testDB
	t.Cleanup(func() { testDB = old })
	loc := testLocator{"FIRST": {-93, 45}, "SECOND": {-93.1, 45}, "THIRD": {-93.2, 45}, "FOURTH": {-93.3, 45}, "FAR": {-80, 25}}
	wps := WaypointArray{{Fix: "FIRST"}, {Fix: "SECOND"}, {Fix: "THIRD"}, {Fix: "FOURTH"}}
	testDB = testDatabase{Airports: map[ICAOAirportCode]testAirport{"KTST": {Id: "KTST", STARs: map[string]STAR{"CURRENT1": {Transitions: map[string]WaypointArray{"ALL": wps}}}}}, Fixes: map[string]bool{"FIRST": true, "SECOND": true, "THIRD": true, "FOURTH": true, "FAR": true}}
	airports := map[ICAOAirportCode]*Airport{"KTST": {}}
	controls := map[ControlPosition]*Controller{"1T": {}}
	for _, tc := range []struct {
		name string
		edit func(*Arrival)
		want string
	}{
		{"retired explicit route", func(*Arrival) {}, ""},
		{"normal scenario remains strict", func(a *Arrival) { a.HistoricalProcedure = false }, `isn't charted`},
		{"still needs explicit route for retired STAR", func(a *Arrival) { a.Waypoints = nil; a.SpawnWaypoint = "FIRST" }, "Couldn't find waypoint"},
		{"unknown waypoint", func(a *Arrival) { a.Waypoints[1].Fix = "UNKNOWN" }, "unable to locate waypoint"},
		{"implausible leg", func(a *Arrival) { a.Waypoints[1].Fix = "FAR" }, "suspiciously far"},
		{"missing speed", func(a *Arrival) { a.InitialSpeed = MakeIAS(0) }, "initial_speed"},
		{"missing altitude", func(a *Arrival) { a.InitialAltitudes = nil }, "initial_altitude"},
		{"unknown controller", func(a *Arrival) { a.InitialController = "NONE" }, "initial_controller"},
		{"missing airport", func(a *Arrival) { a.Airports = nil }, "name the airports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Arrival{HistoricalProcedure: true, STAR: "RETIRED1", Waypoints: append(WaypointArray(nil), wps...), Airports: []ICAOAirportCode{"KTST"}, InitialController: "1T", InitialAltitudes: []int{10000}, InitialSpeed: MakeIAS(250)}
			tc.edit(&a)
			var e util.ErrorLogger
			a.Finalize(loc, 45, 0, airports, controls, func(string) bool { return true }, &e)
			if tc.want == "" {
				if e.HaveErrors() {
					t.Fatal(e.String())
				}
				for _, wp := range a.Waypoints {
					if !wp.OnSTAR() {
						t.Fatal("manual STAR marker lost")
					}
					if wp.Location == (math.Point2LL{}) {
						t.Fatal("waypoint not resolved")
					}
				}
			} else if !strings.Contains(e.String(), tc.want) {
				t.Fatalf("expected %q; got %s", tc.want, e.String())
			}
		})
	}
}

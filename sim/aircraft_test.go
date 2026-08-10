// sim/aircraft_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
)

// 1 degree of latitude is ~60 NM, so waypoints are placed along the meridian
// at known distances from (0, 0).
func wp(fix string, latDeg float32) av.Waypoint {
	return av.Waypoint{Fix: fix, Location: math.Point2LL{0, latDeg}}
}

// The aircraft sits at (0, 0), thousands of miles from both KJFK and KBOS,
// so neither airport appears in these fixes; see
// TestGetSTTFixes_AirportsOnlyWhenNear.
func makeAircraftForSTTFixes(wps []av.Waypoint) *Aircraft {
	return &Aircraft{
		FlightPlan: av.FlightPlan{
			DepartureAirport: "KJFK",
			ArrivalAirport:   "KBOS",
		},
		Nav: nav.Nav{
			FlightState: nav.FlightState{Position: math.Point2LL{0, 0}},
			Waypoints:   wps,
		},
	}
}

// sidWp returns a waypoint flagged as being on a SID, as
// Airport.PostDeserialize does for departure exit route waypoints.
func sidWp(fix string, latDeg float32) av.Waypoint {
	w := wp(fix, latDeg)
	w.SetOnSID(true)
	return w
}

func TestGetSTTFixes_STARS_DepartureSIDAndExit(t *testing.T) {
	wps := []av.Waypoint{
		sidWp("SIDAA", 0.5), // ~30 NM
		sidWp("SIDBB", 1.6), // ~96 NM — far away, but included regardless
		wp("EXITF", 2.5),    // the exit fix, not part of the SID
		wp("ENRTE", 3.0),    // enroute fix — not included
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.FlightPlan.Exit = "EXITF"

	got := ac.GetSTTFixes(false)
	want := []string{"SIDAA", "SIDBB", "EXITF"}
	if !equalStrings(got, want) {
		t.Errorf("STARS departure: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_ArrivalFullRoute(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 0.5),  // ~30 NM
		wp("MIDDL", 2.0), // ~120 NM
		wp("FAR", 5.0),   // ~300 NM — still included
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeArrival

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "MIDDL", "FAR"}
	if !equalStrings(got, want) {
		t.Errorf("STARS arrival: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_ArrivalExpectedApproach(t *testing.T) {
	appr := &av.Approach{
		Waypoints: []av.WaypointArray{{wp("TRANS", 1.5), wp("FINAL", 0.9)}},
	}

	// Told to expect the approach but not yet on it: all of the approach's
	// fixes are included, without duplicating ones shared with the route.
	ac := makeAircraftForSTTFixes([]av.Waypoint{wp("NEAR", 0.5), wp("TRANS", 1.5)})
	ac.TypeOfFlight = av.FlightTypeArrival
	ac.Nav.Approach.Assigned = appr

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "TRANS", "FINAL"}
	if !equalStrings(got, want) {
		t.Errorf("expecting approach: got %v, want %v", got, want)
	}

	// Once the aircraft has joined the approach, the remaining route
	// carries the remaining approach fixes, so nothing more is added.
	onAppr := wp("FINAL", 0.9)
	onAppr.SetOnApproach(true)
	ac = makeAircraftForSTTFixes([]av.Waypoint{onAppr})
	ac.TypeOfFlight = av.FlightTypeArrival
	ac.Nav.Approach.Assigned = appr

	got = ac.GetSTTFixes(false)
	want = []string{"FINAL"}
	if !equalStrings(got, want) {
		t.Errorf("joined approach: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_Overflight120NM(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 1.0),  // ~60 NM
		wp("MIDDL", 1.9), // ~114 NM
		wp("FAR", 2.5),   // ~150 NM — culled
		wp("FARTH", 3.0),
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeOverflight

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "MIDDL"}
	if !equalStrings(got, want) {
		t.Errorf("STARS overflight: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_OverflightFirstFixAlwaysIncluded(t *testing.T) {
	wps := []av.Waypoint{
		wp("FIRST", 2.5), // ~150 NM — included anyway since it's the first
		wp("SECND", 3.0), // ~180 NM — culled
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeOverflight

	got := ac.GetSTTFixes(false)
	want := []string{"FIRST"}
	if !equalStrings(got, want) {
		t.Errorf("STARS overflight: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_ERAM_Allows300NMAndCapsAt5(t *testing.T) {
	wps := []av.Waypoint{
		wp("ALPHA", 0.5), // 30 NM
		wp("BRAVO", 1.0), // 60 NM
		wp("CHARL", 1.5), // 90 NM
		wp("DELTA", 2.5), // 150 NM
		wp("ECHO", 3.5),  // 210 NM
		wp("FOXTR", 4.0), // 240 NM — in range but exceeds count
		wp("GOLF", 6.0),  // 360 NM — beyond range
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(true)
	want := []string{"ALPHA", "BRAVO", "CHARL", "DELTA", "ECHO"}
	if !equalStrings(got, want) {
		t.Errorf("ERAM: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_ERAM_CullsBeyond300NM(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 2.0), // 120 NM
		wp("MID", 4.0),  // 240 NM
		wp("FAR", 6.0),  // 360 NM — beyond 300
		wp("FARTHER", 7.0),
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(true)
	want := []string{"NEAR", "MID"}
	if !equalStrings(got, want) {
		t.Errorf("ERAM: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_SkipsInternalAndShortFixes(t *testing.T) {
	wps := []av.Waypoint{
		wp("_INT", 0.2),   // internal, underscore-prefixed
		wp("AB", 0.3),     // too short
		wp("OK", 0.4),     // too short (len 2)
		wp("GOOD", 0.5),   // valid
		wp("LONGER", 1.0), // length 6, too long
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(false)
	want := []string{"GOOD"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An airport is worth naming only when the aircraft is near it: a Teterboro
// departure is never sent direct to Orlando, and carrying it costs a slot in
// the fix vocabulary and in the whisper prompt. The same rule applies to both
// airports whatever the type of flight.
func TestGetSTTFixes_AirportsOnlyWhenNear(t *testing.T) {
	jfk, ok := av.DB.LookupAirport("KJFK")
	if !ok {
		t.Fatal("KJFK not in the database")
	}

	for _, tc := range []struct {
		name               string
		departure, arrival string
		want               []string
	}{
		{"both near", "KJFK", "KLGA", []string{"KLGA", "KJFK", "GOOD"}},
		{"distant destination", "KJFK", "KMCO", []string{"KJFK", "GOOD"}},
		{"distant origin", "KMCO", "KLGA", []string{"KLGA", "GOOD"}},
		{"both distant", "KMCO", "KSFO", []string{"GOOD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ac := makeAircraftForSTTFixes([]av.Waypoint{{Fix: "GOOD", Location: jfk.Location}})
			ac.Nav.FlightState.Position = jfk.Location
			ac.FlightPlan.DepartureAirport = tc.departure
			ac.FlightPlan.ArrivalAirport = tc.arrival
			ac.TypeOfFlight = av.FlightTypeArrival

			if got := ac.GetSTTFixes(false); !equalStrings(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

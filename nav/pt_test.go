// nav/pt_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// makePTFlight builds a FlightTest with a manually-constructed RNAV
// approach containing a procedure turn at the first waypoint. The route
// string should start with the PT fix and include PT modifiers (e.g.
// "/pt45", "/hilpt4.0nm"). All waypoints are marked as on-approach.
// Modeled on the RNAV 15R at KISP which has a HILPT at FORMU.
func makePTFlight(t *testing.T, routeStr string, alt, speed float32) *FlightTest {
	t.Helper()

	wps := parseRoute(t, routeStr)
	for i := range wps {
		wps[i].SetOnApproach(true)
	}

	// Build a minimal RNAV approach from the waypoints.
	ap := &av.Approach{
		Id:       "TEST",
		FullName: "RNAV TEST",
		Type:     av.RNAVApproach,
		Runway:   "15R",
		Waypoints: []av.WaypointArray{
			util.DuplicateSlice(wps),
		},
	}

	// Use KISP (Islip) for airport metadata; the RNAV 15R has a real
	// procedure turn at FORMU.
	arrAirport, ok := av.DB.Airports["KISP"]
	if !ok {
		t.Fatal("KISP not in database")
	}
	depAirport, ok := av.DB.Airports["KMCO"]
	if !ok {
		t.Fatal("KMCO not in database")
	}

	nmPerLong := math.NMPerLongitudeAt(arrAirport.Location)
	magVar, err := av.DB.MagneticGrid.Lookup(arrAirport.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}

	if rwy, ok := av.LookupRunway("KISP", "15R"); ok {
		ap.Threshold = rwy.Threshold
	}
	if opp, ok := av.LookupOppositeRunway("KISP", "15R"); ok {
		ap.OppositeThreshold = opp.Threshold
	}

	rng := &rand.Rand{PCG32: rand.NewPCG32()}
	rng.Seed(42)

	// Inbound course: PT fix (wps[0]) toward the next fix (wps[1]).
	// Position the aircraft on the outbound side of the PT fix, heading
	// inbound, to simulate arriving from a feeder route.
	var hdg math.MagneticHeading
	var startPos math.Point2LL
	if len(wps) > 1 {
		inboundTrue := math.Heading2LL(wps[0].Location, wps[1].Location, nmPerLong)
		outboundTrue := math.OppositeHeading(inboundTrue)
		startPos = math.Offset2LL(wps[0].Location, outboundTrue, 5, nmPerLong)
		hdg = math.TrueToMagnetic(inboundTrue, magVar)
	}

	navWps := make([]av.Waypoint, len(wps)+1)
	copy(navWps, wps)
	navWps[len(wps)] = av.Waypoint{
		Fix:      "KISP",
		Location: arrAirport.Location,
	}

	n := &Nav{
		Perf:           av.DB.AircraftPerformance["A320"],
		FinalAltitude:  alt,
		FixAssignments: make(map[string]NavFixAssignment),
		Rand:           rng,
		Waypoints:      navWps,
		FlightState: FlightState{
			MagneticVariation:         magVar,
			NmPerLongitude:            nmPerLong,
			Position:                  startPos,
			Heading:                   hdg,
			Altitude:                  alt,
			IAS:                       speed,
			GS:                        speed,
			DepartureAirportLocation:  depAirport.Location,
			DepartureAirportElevation: float32(depAirport.Elevation),
			ArrivalAirportLocation:    arrAirport.Location,
			ArrivalAirportElevation:   float32(arrAirport.Elevation),
			ArrivalAirport: av.Waypoint{
				Fix:      "KISP",
				Location: arrAirport.Location,
			},
		},
	}

	// Set up approach state: assigned and cleared.
	n.Approach = NavApproach{
		Assigned:   ap,
		AssignedId: "TEST",
		Cleared:    true,
	}

	fp := av.FlightPlan{
		Rules:            av.FlightRulesIFR,
		AircraftType:     "A320",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KISP",
		Altitude:         int(alt),
	}

	return &FlightTest{
		t:        t,
		nav:      n,
		fp:       fp,
		callsign: "TEST001",
		simTime:  NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)),
		maxTicks: 7200,
		weather:  func(a float32) wx.Sample { return wx.MakeStandardSampleForAltitude(a) },
	}
}

// TestStandard45ProcedureTurnCompletes verifies that a standard 45-degree
// procedure turn creates the correct maneuver sequence and completes
// without getting stuck (regression test for 26fe133c and 409198f5).
func TestStandard45ProcedureTurnCompletes(t *testing.T) {
	// FORMU is the IAF on the RNAV 15R at KISP. Use it as the PT fix
	// with an inbound course toward ZIVUX. The /pt45 modifier creates
	// a standard 45-degree PT.
	f := makePTFlight(t, "FORMU/pt45/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, 0, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	if len(f.nav.Heading.Maneuvers) != 7 {
		t.Fatalf("expected 7 maneuvers for standard 45 PT, got %d", len(f.nav.Heading.Maneuvers))
	}

	// Run the simulation and verify all maneuvers complete.
	f.AfterTicks(600, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			// PT completed successfully
		} else {
			t.Logf("tick 600: %d maneuvers remaining", len(f.nav.Heading.Maneuvers))
		}
	})

	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			t.Errorf("PT did not complete after 1200 ticks: %d maneuvers remain", len(f.nav.Heading.Maneuvers))
		}
	})

	f.Run()
}

// TestRacetrackPTCreatesManeuvers verifies that a racetrack procedure
// turn (HILPT) creates a maneuver sequence and completes
// (regression test for 0c45e6bc).
func TestRacetrackPTCreatesManeuvers(t *testing.T) {
	f := makePTFlight(t, "FORMU/hilpt4.0nm/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, 0, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	// Racetrack PTs create at least 4 maneuvers (varies by entry type).
	if len(f.nav.Heading.Maneuvers) < 4 {
		t.Fatalf("expected at least 4 maneuvers for racetrack PT, got %d", len(f.nav.Heading.Maneuvers))
	}

	// Run and verify completion.
	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			t.Errorf("racetrack PT did not complete after 1200 ticks: %d maneuvers remain",
				len(f.nav.Heading.Maneuvers))
		}
	})

	f.Run()
}

// TestPTWaypointFlyover verifies that waypoints with procedure turns are
// treated as flyover points — the PT only triggers when ETA < 2s, not
// before (regression test for cf822df4).
func TestPTWaypointFlyover(t *testing.T) {
	wps := parseRoute(t, "FORMU/pt45/flyover ZIVUX")
	for i := range wps {
		wps[i].SetOnApproach(true)
	}

	// Verify the PT fix has FlyOver set.
	if !wps[0].FlyOver() {
		t.Errorf("PT waypoint should have FlyOver flag set")
	}

	// Verify the PT data is present.
	if wps[0].ProcedureTurn() == nil {
		t.Errorf("PT waypoint should have ProcedureTurn data")
	}
	if wps[0].ProcedureTurn().Type != av.PTStandard45 {
		t.Errorf("expected PTStandard45, got %d", wps[0].ProcedureTurn().Type)
	}
}

// TestProcedureTurnDescendsToExitAltitude verifies that a procedure turn
// with ExitAltitude descends the aircraft during the inbound leg.
func TestProcedureTurnDescendsToExitAltitude(t *testing.T) {
	// Use /pta2000 to set exit altitude to 2000.
	f := makePTFlight(t, "FORMU/pt45/pta2000/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, 0, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	if len(f.nav.Heading.Maneuvers) != 7 {
		t.Fatalf("expected 7 maneuvers, got %d", len(f.nav.Heading.Maneuvers))
	}

	// The last maneuver should have AssignAltitude set to 2000.
	lastManeuver := f.nav.Heading.Maneuvers[6]
	if lastManeuver.AssignAltitude == nil {
		t.Errorf("last maneuver should have AssignAltitude set")
	} else if *lastManeuver.AssignAltitude != 2000 {
		t.Errorf("expected exit altitude 2000, got %.0f", *lastManeuver.AssignAltitude)
	}

	// Run the simulation and verify altitude near 2000 after PT completes.
	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			// PT completed; altitude should be near 2000.
			f.AssertAltitudeNear(2000, 500)
		}
	})

	f.Run()
}

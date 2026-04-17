// nav/commands_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// TestCommandValidation verifies that invalid commands produce UnableIntents.
func TestCommandValidation(t *testing.T) {
	makeNav := func(t *testing.T) *FlightTest {
		t.Helper()
		return NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  11000,
			InitialSpeed:     250,
		})
	}

	t.Run("AltitudeAboveCeiling", func(t *testing.T) {
		f := makeNav(t)
		intent := f.nav.AssignAltitude(99000, false, f.simTime, 0)
		AssertUnable(t, intent)
	})

	t.Run("SpeedBelowLanding", func(t *testing.T) {
		f := makeNav(t)
		sr := av.MakeAtSpeedRestriction(80)
		intent := f.nav.AssignSpeed(&sr, false)
		AssertUnable(t, intent)
	})

	t.Run("DirectFixInvalid", func(t *testing.T) {
		f := makeNav(t)
		intent := f.nav.DirectFix("ZZZZZZ", av.TurnClosest, f.simTime, 0)
		AssertUnable(t, intent)
	})

	t.Run("CrossFixNotInRoute", func(t *testing.T) {
		f := makeNav(t)
		ar := av.MakeAtAltitudeRestriction(5000)
		intent := f.nav.CrossFixAt("NOTINROUTE", &ar, nil)
		AssertUnable(t, intent)
	})

	t.Run("ClearedApproachWithoutExpect", func(t *testing.T) {
		f := makeNav(t)
		intent := f.nav.ClearedApproach("I22L", nil, f.simTime, false)
		AssertUnable(t, intent)
	})

	t.Run("DescendViaSTARNotOnSTAR", func(t *testing.T) {
		// Create a nav without OnSTAR waypoints
		f := NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL DETGY HAUPT",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  11000,
			InitialSpeed:     250,
		})
		intent := f.nav.DescendViaSTAR(f.simTime)
		AssertUnable(t, intent)
	})

	t.Run("ExpediteDescentWhenLevel", func(t *testing.T) {
		// Aircraft at 11000, no assigned descent — level flight
		f := NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL DETGY HAUPT",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  11000,
			InitialSpeed:     250,
			AssignedAltitude: 11000,
		})
		intent := f.nav.ExpediteDescent()
		AssertUnable(t, intent)
	})
}

// TestLeftDirectFix verifies that DirectFix with TurnLeft turns the long way.
func TestLeftDirectFix(t *testing.T) {
	// Aircraft heading north (360). Fix is DETGY which is roughly east
	// of SAJUL. By requesting TurnLeft, the aircraft should turn counter-
	// clockwise (heading decreasing from 360 toward 270).
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	// Assign heading north first, then direct to DETGY with left turn
	f.AssignHeading(360, av.TurnClosest)

	f.AfterTicks(30, func(f *FlightTest) {
		f.DirectFixWithTurn("DETGY", av.TurnLeft)
	})

	// Check after some time that the deferred heading includes a left turn
	f.AfterTicks(60, func(f *FlightTest) {
		if dh := f.nav.DeferredNavHeading; dh != nil && dh.Turn != nil {
			if *dh.Turn != av.TurnLeft {
				t.Errorf("expected TurnLeft in deferred heading, got %v", *dh.Turn)
			}
		}
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		// Just verify the fix was reached
	})

	f.Run()
}

// TestRightDirectFix verifies that DirectFix with TurnRight turns the short way.
func TestRightDirectFix(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignHeading(360, av.TurnClosest)

	f.AfterTicks(30, func(f *FlightTest) {
		f.DirectFixWithTurn("DETGY", av.TurnRight)
	})

	f.AfterTicks(60, func(f *FlightTest) {
		if dh := f.nav.DeferredNavHeading; dh != nil && dh.Turn != nil {
			if *dh.Turn != av.TurnRight {
				t.Errorf("expected TurnRight in deferred heading, got %v", *dh.Turn)
			}
		}
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		// Verify fix reached
	})

	f.Run()
}

// TestExpectDirectReducesDelay verifies that calling ExpectDirect before
// DirectFix results in a shorter pilot delay.
func TestExpectDirectReducesDelay(t *testing.T) {
	makeFlight := func(t *testing.T) *FlightTest {
		return NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  11000,
			InitialSpeed:     250,
			OnSTAR:           true,
		})
	}

	// Without ExpectDirect
	fNoExpect := makeFlight(t)
	fNoExpect.nav.AssignHeading(math.MagneticHeading(360), av.TurnClosest, fNoExpect.simTime, 0)
	// Wait for heading to take effect
	for i := 0; i < 10; i++ {
		wxs := fNoExpect.weather(fNoExpect.nav.FlightState.Altitude)
		fNoExpect.nav.UpdateWithWeather(fNoExpect.callsign, wxs, &fNoExpect.fp, 0, fNoExpect.simTime, nil)
		fNoExpect.simTime = fNoExpect.simTime.Add(1e9) // 1 second
	}
	fNoExpect.nav.DirectFix("DETGY", av.TurnClosest, fNoExpect.simTime, 0)
	noExpectDelay := fNoExpect.nav.DeferredNavHeading.Time

	// With ExpectDirect
	fExpect := makeFlight(t)
	fExpect.nav.AssignHeading(math.MagneticHeading(360), av.TurnClosest, fExpect.simTime, 0)
	for i := 0; i < 10; i++ {
		wxs := fExpect.weather(fExpect.nav.FlightState.Altitude)
		fExpect.nav.UpdateWithWeather(fExpect.callsign, wxs, &fExpect.fp, 0, fExpect.simTime, nil)
		fExpect.simTime = fExpect.simTime.Add(1e9)
	}
	fExpect.nav.ExpectDirect("DETGY")
	fExpect.nav.DirectFix("DETGY", av.TurnClosest, fExpect.simTime, 0)
	expectDelay := fExpect.nav.DeferredNavHeading.Time

	// With expect, the delay should be shorter (earlier time)
	if !expectDelay.Before(noExpectDelay) {
		t.Errorf("ExpectDirect did not reduce delay: expect=%v noExpect=%v", expectDelay, noExpectDelay)
	}
}

// TestCrossDistanceFromFixAtDirectionUnable verifies that
// CrossDistanceFromFixAt returns unable when the direction is wrong.
func TestCrossDistanceFromFixAtDirectionUnable(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	// Determine the actual approach direction (SAJUL→DETGY), then pick the opposite.
	fixLoc := f.nav.Waypoints[1].Location   // DETGY
	priorLoc := f.nav.Waypoints[0].Location // SAJUL
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	oppositeDir := math.ShortCompass(math.OppositeHeading(approachHeading))
	dir, err := math.ParseCardinalOrdinalDirection(oppositeDir)
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	AssertUnable(t, intent)
}

// TestCrossDistanceFromFixAtDistanceUnable verifies that
// CrossDistanceFromFixAt returns unable when the distance exceeds the segment.
func TestCrossDistanceFromFixAtDistanceUnable(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	// Determine the correct direction.
	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	approachDir := math.ShortCompass(approachHeading)
	dir, err := math.ParseCardinalOrdinalDirection(approachDir)
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	// Distance of 9999 miles should exceed any segment.
	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 9999, dir, &ar, nil)
	AssertUnable(t, intent)
}

// TestCrossDistanceFromFixAtNotInRoute verifies that an unknown fix returns unable.
func TestCrossDistanceFromFixAtNotInRoute(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("BOGUS", 5, math.West, &ar, nil)
	AssertUnable(t, intent)
}

// TestCrossDistanceFromFixAtInsertsWaypoint verifies that a synthetic waypoint
// is inserted at the correct position in the route.
func TestCrossDistanceFromFixAtInsertsWaypoint(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	// Find the correct approach direction.
	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	approachDir := math.ShortCompass(approachHeading)
	dir, err := math.ParseCardinalOrdinalDirection(approachDir)
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	wpsBefore := len(f.nav.Waypoints)
	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	// Waypoints should have one more entry.
	if len(f.nav.Waypoints) != wpsBefore+1 {
		t.Errorf("expected %d waypoints, got %d", wpsBefore+1, len(f.nav.Waypoints))
	}

	// The synthetic waypoint should be before DETGY (at index 1).
	if f.nav.Waypoints[1].Fix[0] != '_' {
		t.Errorf("expected synthetic waypoint (underscore prefix) at index 1, got %q",
			f.nav.Waypoints[1].Fix)
	}
	if !f.nav.Waypoints[1].OnSTAR() {
		t.Errorf("expected synthetic waypoint to preserve STAR membership")
	}
	if f.nav.Waypoints[2].Fix != "DETGY" {
		t.Errorf("expected DETGY at index 2, got %q", f.nav.Waypoints[2].Fix)
	}
}

// TestCrossDistanceFromFixAtUsesDeferredWaypoints verifies that when a
// deferred route exists, the synthetic waypoint is inserted into that route
// rather than the current nav.Waypoints slice.
func TestCrossDistanceFromFixAtUsesDeferredWaypoints(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	origNavLen := len(f.nav.Waypoints)
	deferred := append([]av.Waypoint(nil), f.nav.Waypoints[1:]...)
	f.nav.DeferredNavHeading = &DeferredNavHeading{Waypoints: deferred}

	fixLoc := deferred[0].Location
	priorLoc := f.nav.FlightState.Position
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	if len(f.nav.Waypoints) != origNavLen {
		t.Fatalf("expected nav.Waypoints length %d, got %d", origNavLen, len(f.nav.Waypoints))
	}
	if len(f.nav.DeferredNavHeading.Waypoints) != len(deferred)+1 {
		t.Fatalf("expected deferred waypoints length %d, got %d",
			len(deferred)+1, len(f.nav.DeferredNavHeading.Waypoints))
	}
	if f.nav.DeferredNavHeading.Waypoints[0].Fix[0] != '_' {
		t.Fatalf("expected synthetic waypoint inserted at deferred index 0, got %q",
			f.nav.DeferredNavHeading.Waypoints[0].Fix)
	}
	if !f.nav.DeferredNavHeading.Waypoints[0].OnSTAR() {
		t.Fatalf("expected deferred synthetic waypoint to preserve STAR membership")
	}
	if f.nav.DeferredNavHeading.Waypoints[1].Fix != "DETGY" {
		t.Fatalf("expected DETGY after synthetic waypoint, got %q",
			f.nav.DeferredNavHeading.Waypoints[1].Fix)
	}
}

func TestCrossDistanceFromFixAtIgnoresEmptyDeferredRoute(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	origNavLen := len(f.nav.Waypoints)
	f.nav.DeferredNavHeading = &DeferredNavHeading{}

	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	if len(f.nav.Waypoints) != origNavLen+1 {
		t.Fatalf("expected nav.Waypoints length %d, got %d", origNavLen+1, len(f.nav.Waypoints))
	}
	if len(f.nav.DeferredNavHeading.Waypoints) != 0 {
		t.Fatalf("expected empty deferred route to remain empty, got %d waypoints", len(f.nav.DeferredNavHeading.Waypoints))
	}
}

// TestCrossDistanceFromFixAtReplacement verifies that altitude and speed/mach
// synthetic waypoints are independent and replace correctly.
func TestCrossDistanceFromFixAtReplacement(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	// Find the correct approach direction.
	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	dir, _ := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))

	// 1. Initial command: both altitude and speed.
	ar1 := av.MakeAtAltitudeRestriction(8000)
	sr1 := av.MakeAtSpeedRestriction(230)
	f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar1, &sr1)

	// Original waypoints: SAJUL, DETGY, HAUPT, KJFK (4)
	// After adding one combined synthetic waypoint: SAJUL, _DETGY/5W, DETGY, HAUPT, KJFK (5)
	if len(f.nav.Waypoints) != 5 {
		t.Fatalf("expected 5 waypoints, got %d", len(f.nav.Waypoints))
	}
	expected5 := fmt.Sprintf("_DETGY/5%s", dir.ShortString())
	if f.nav.Waypoints[1].Fix != expected5 {
		t.Errorf("expected %s at index 1, got %q", expected5, f.nav.Waypoints[1].Fix)
	}
	if f.nav.Waypoints[1].AltitudeRestriction() == nil || f.nav.Waypoints[1].SpeedRestriction() == nil {
		t.Fatalf("expected combined synthetic waypoint to carry both altitude and speed restrictions")
	}

	// 2. Update only altitude.
	ar2 := av.MakeAtAltitudeRestriction(9000)
	f.nav.CrossDistanceFromFixAt("DETGY", 7, dir, &ar2, nil)

	// The 5-mile waypoint should retain only speed; altitude moves to the 7-mile waypoint.
	if len(f.nav.Waypoints) != 6 {
		t.Fatalf("expected 6 waypoints after altitude update, got %d", len(f.nav.Waypoints))
	}
	expected7 := fmt.Sprintf("_DETGY/7%s", dir.ShortString())
	if f.nav.Waypoints[1].Fix != expected7 {
		t.Errorf("expected %s at index 1, got %q", expected7, f.nav.Waypoints[1].Fix)
	}
	if f.nav.Waypoints[1].AltitudeRestriction() == nil || f.nav.Waypoints[1].SpeedRestriction() != nil {
		t.Fatalf("expected 7-mile waypoint to carry only altitude restriction")
	}
	if f.nav.Waypoints[2].Fix != expected5 {
		t.Errorf("expected %s at index 2, got %q", expected5, f.nav.Waypoints[2].Fix)
	}
	if f.nav.Waypoints[2].AltitudeRestriction() != nil || f.nav.Waypoints[2].SpeedRestriction() == nil {
		t.Fatalf("expected 5-mile waypoint to carry only speed restriction")
	}

	// 3. Update only speed.
	sr2 := av.MakeAtSpeedRestriction(210)
	f.nav.CrossDistanceFromFixAt("DETGY", 6, dir, nil, &sr2)

	// The old 5-mile speed waypoint should be gone, replaced by a 6-mile speed waypoint.
	if len(f.nav.Waypoints) != 6 {
		t.Fatalf("expected 6 waypoints after speed update, got %d", len(f.nav.Waypoints))
	}
	expected6 := fmt.Sprintf("_DETGY/6%s", dir.ShortString())
	if f.nav.Waypoints[1].Fix != expected7 {
		t.Errorf("expected %s at index 1, got %q", expected7, f.nav.Waypoints[1].Fix)
	}
	if f.nav.Waypoints[2].Fix != expected6 {
		t.Errorf("expected %s at index 2, got %q", expected6, f.nav.Waypoints[2].Fix)
	}
	if f.nav.Waypoints[1].AltitudeRestriction() == nil || f.nav.Waypoints[1].SpeedRestriction() != nil {
		t.Fatalf("expected 7-mile waypoint to carry only altitude restriction")
	}
	if f.nav.Waypoints[2].AltitudeRestriction() != nil || f.nav.Waypoints[2].SpeedRestriction() == nil {
		t.Fatalf("expected 6-mile waypoint to carry only speed restriction")
	}
}

// TestCrossDistanceFromFixAtRejectsOrthogonalDirection verifies that
// orthogonal directions are rejected instead of being mapped onto the route leg.
func TestCrossDistanceFromFixAtRejectsOrthogonalDirection(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	orthogonalDir := math.ShortCompass(math.OffsetHeading(approachHeading, 90))
	dir, err := math.ParseCardinalOrdinalDirection(orthogonalDir)
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	AssertUnable(t, intent)
}

// TestCrossDistanceFromFixAtUsesUnderscoreNamedPriorWaypoint verifies that a
// real prior waypoint with an underscore-prefixed name is still used as the leg start.
func TestCrossDistanceFromFixAtUsesUnderscoreNamedPriorWaypoint(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	f.nav.Waypoints[0].Fix = "_THRESHOLD_HELPER"

	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude),
		f.nav.FlightState.MagneticVariation,
	)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(5000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	if len(f.nav.Waypoints) < 3 {
		t.Fatalf("expected inserted synthetic waypoint")
	}
	if !f.nav.Waypoints[1].SyntheticCrossing() {
		t.Fatalf("expected synthetic waypoint at index 1, got %q", f.nav.Waypoints[1].Fix)
	}
	if got := math.NMDistance2LL(f.nav.Waypoints[1].Location, f.nav.Waypoints[2].Location); got < 4.5 || got > 5.5 {
		t.Fatalf("expected synthetic waypoint about 5nm before DETGY on the actual leg, got %.2f", got)
	}
}

// TestAtFixClearedInvalidFix verifies that AtFixCleared with a fix not on
// the approach returns an UnableIntent.
func TestAtFixClearedInvalidFix(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000/star LEFER/a4000/star ROSLY/a3000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  7000,
		InitialSpeed:     210,
	})

	f.ExpectApproach("I22L")
	intent := f.nav.AtFixCleared("BOGUS", "I22L", f.simTime, 0, false)
	AssertUnable(t, intent)
}

// setupClearedVisual returns a FlightTest positioned on a simple straight-in
// visual approach to the given runway at KJFK, with the approach already
// cleared. The route is intercept (10nm) → 3nm final → threshold.
func setupClearedVisual(t *testing.T, runway string) *FlightTest {
	t.Helper()
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  4000,
		InitialSpeed:     200,
	})

	rwy, ok := av.LookupRunway("KJFK", runway)
	if !ok {
		t.Fatalf("unknown runway KJFK/%s", runway)
	}

	nmPerLong := f.nav.FlightState.NmPerLongitude
	magVar := f.nav.FlightState.MagneticVariation
	rwyTrue := math.MagneticToTrue(rwy.Heading, magVar)
	reciprocal := math.TrueHeading(math.NormalizeHeading(float32(rwyTrue) + 180))

	offset := func(dist float32) math.Point2LL {
		return math.Offset2LL(rwy.Threshold, reciprocal, dist, nmPerLong)
	}

	f.nav.FlightState.Position = offset(12)
	f.nav.FlightState.Heading = math.TrueToMagnetic(rwyTrue, magVar)

	intercept := av.Waypoint{Fix: "_" + runway + "_INTERCEPT", Location: offset(10)}
	intercept.SetOnApproach(true)

	final3 := av.Waypoint{Fix: "_" + runway + "_3NM_FINAL", Location: offset(3)}
	final3.SetOnApproach(true)
	final3.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(rwy.Elevation) + 900))

	threshold := av.Waypoint{Fix: "_" + runway + "_THRESHOLD", Location: rwy.Threshold}
	threshold.SetOnApproach(true)
	threshold.SetLand(true)
	threshold.SetFlyOver(true)
	threshold.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(rwy.Elevation + rwy.ThresholdCrossingHeight)))

	f.nav.Waypoints = []av.Waypoint{intercept, final3, threshold, f.nav.FlightState.ArrivalAirport}

	f.nav.Approach.Assigned = &av.Approach{
		Id:        "VIS" + runway,
		FullName:  "Visual Approach Runway " + runway,
		Type:      av.VisualApproach,
		Runway:    runway,
		Threshold: rwy.Threshold,
	}
	f.nav.Approach.AssignedId = "VIS" + runway
	f.nav.Approach.Cleared = true

	return f
}

// TestCrossDMEAtRequiresVisualApproach verifies that CrossDMEAt returns
// Unable when the aircraft isn't cleared for a visual approach.
func TestCrossDMEAtRequiresVisualApproach(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	ar := av.MakeAtAltitudeRestriction(3000)
	AssertUnable(t, f.nav.CrossDMEAt(10, &ar, nil))

	// Also unable when an ILS approach is expected but no visual clearance.
	f.ExpectApproach("I22L")
	AssertUnable(t, f.nav.CrossDMEAt(10, &ar, nil))

	// And unable when cleared for a non-visual approach whose ID happens to
	// start with "V" (e.g., a VOR approach) — the check must be type-based,
	// not a brittle prefix match.
	f.nav.Approach.Assigned = &av.Approach{
		Id:     "V22L",
		Type:   av.VORApproach,
		Runway: "22L",
	}
	f.nav.Approach.AssignedId = "V22L"
	f.nav.Approach.Cleared = true
	AssertUnable(t, f.nav.CrossDMEAt(10, &ar, nil))
}

// TestCrossDMEAtOutOfRange verifies that distances outside [1, 30] are rejected.
func TestCrossDMEAtOutOfRange(t *testing.T) {
	f := setupClearedVisual(t, "22L")
	ar := av.MakeAtAltitudeRestriction(3000)
	AssertUnable(t, f.nav.CrossDMEAt(0, &ar, nil))
	AssertUnable(t, f.nav.CrossDMEAt(-5, &ar, nil))
	AssertUnable(t, f.nav.CrossDMEAt(31, &ar, nil))
}

// TestCrossDMEAtInsertsWaypoint verifies that a synthetic DME waypoint is
// inserted at the correct distance from the threshold along the approach route.
func TestCrossDMEAtInsertsWaypoint(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	ar := av.MakeAtAltitudeRestriction(3000)
	intent := f.nav.CrossDMEAt(5, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	idx := -1
	for i, wp := range f.nav.Waypoints {
		if wp.Fix == "_22L_5DME" {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("synthetic _22L_5DME waypoint not inserted; route: %v", waypointFixes(f.nav.Waypoints))
	}
	if !f.nav.Waypoints[idx].SyntheticCrossing() {
		t.Errorf("expected synthetic waypoint flag on _22L_5DME")
	}
	if !f.nav.Waypoints[idx].OnApproach() {
		t.Errorf("expected OnApproach flag on _22L_5DME")
	}
	if ar := f.nav.Waypoints[idx].AltitudeRestriction(); ar == nil || ar.Range[0] != 3000 {
		t.Errorf("expected 3000 altitude restriction on synthetic waypoint")
	}

	threshold := f.nav.Waypoints[len(f.nav.Waypoints)-2].Location
	if d := math.NMDistance2LL(f.nav.Waypoints[idx].Location, threshold); d < 4.5 || d > 5.5 {
		t.Errorf("expected synthetic waypoint ~5nm from threshold, got %.2f", d)
	}
}

// TestCrossDMEAtExtrapolates verifies that dist beyond the route length
// causes the synthetic waypoint to be extrapolated past wp[0].
func TestCrossDMEAtExtrapolates(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	ar := av.MakeAtAltitudeRestriction(4000)
	intent := f.nav.CrossDMEAt(15, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	if f.nav.Waypoints[0].Fix != "_22L_15DME" {
		t.Fatalf("expected _22L_15DME at index 0, got %q", f.nav.Waypoints[0].Fix)
	}
	threshold := f.nav.Waypoints[len(f.nav.Waypoints)-2].Location
	if d := math.NMDistance2LL(f.nav.Waypoints[0].Location, threshold); d < 14.5 || d > 15.5 {
		t.Errorf("expected extrapolated waypoint ~15nm from threshold, got %.2f", d)
	}
}

// TestCrossDMEAtReplacement verifies that altitude- and speed-only synthetic
// DME waypoints replace cleanly and independently.
func TestCrossDMEAtReplacement(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	ar1 := av.MakeAtAltitudeRestriction(3000)
	sr1 := av.MakeAtSpeedRestriction(210)
	f.nav.CrossDMEAt(5, &ar1, &sr1)

	// One combined synthetic for 5 DME with both restrictions.
	found := slicesIndex(f.nav.Waypoints, "_22L_5DME")
	if found < 0 {
		t.Fatalf("expected _22L_5DME, got %v", waypointFixes(f.nav.Waypoints))
	}
	if f.nav.Waypoints[found].AltitudeRestriction() == nil || f.nav.Waypoints[found].SpeedRestriction() == nil {
		t.Fatalf("expected 5 DME waypoint to carry both restrictions")
	}

	// Update only altitude at a different distance → new 7 DME waypoint
	// carries altitude; 5 DME waypoint retains only speed.
	ar2 := av.MakeAtAltitudeRestriction(4000)
	f.nav.CrossDMEAt(7, &ar2, nil)

	five := slicesIndex(f.nav.Waypoints, "_22L_5DME")
	seven := slicesIndex(f.nav.Waypoints, "_22L_7DME")
	if five < 0 || seven < 0 {
		t.Fatalf("expected both _22L_5DME and _22L_7DME, got %v", waypointFixes(f.nav.Waypoints))
	}
	if seven >= five {
		t.Errorf("expected _22L_7DME (farther from threshold) before _22L_5DME in route")
	}
	if f.nav.Waypoints[seven].AltitudeRestriction() == nil || f.nav.Waypoints[seven].SpeedRestriction() != nil {
		t.Errorf("expected 7 DME to carry only altitude")
	}
	if f.nav.Waypoints[five].AltitudeRestriction() != nil || f.nav.Waypoints[five].SpeedRestriction() == nil {
		t.Errorf("expected 5 DME to carry only speed")
	}

	// Replace the speed at a new 6 DME; the 5 DME speed-only waypoint should disappear.
	sr2 := av.MakeAtSpeedRestriction(190)
	f.nav.CrossDMEAt(6, nil, &sr2)

	if idx := slicesIndex(f.nav.Waypoints, "_22L_5DME"); idx >= 0 {
		t.Errorf("expected _22L_5DME to be removed, got %v", waypointFixes(f.nav.Waypoints))
	}
	if idx := slicesIndex(f.nav.Waypoints, "_22L_6DME"); idx < 0 {
		t.Errorf("expected _22L_6DME after speed replacement")
	}
}

// TestCrossDMEAtShortRouteAfterDeletion verifies that reissuing a CDME
// command when the remaining route is just [existing_DME_synthetic, threshold]
// — which happens after the aircraft has passed all prior visual waypoints —
// returns Unable instead of panicking.
func TestCrossDMEAtShortRouteAfterDeletion(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	ar1 := av.MakeAtAltitudeRestriction(3000)
	if _, ok := f.nav.CrossDMEAt(5, &ar1, nil).(av.UnableIntent); ok {
		t.Fatalf("unexpected unable on first CDME")
	}

	// Simulate the aircraft having passed all prior waypoints: route is
	// reduced to just the synthetic DME waypoint, the threshold, and the
	// arrival airport.
	dmeIdx := slicesIndex(f.nav.Waypoints, "_22L_5DME")
	if dmeIdx < 0 {
		t.Fatalf("expected _22L_5DME in route")
	}
	threshold := len(f.nav.Waypoints) - 2
	airport := len(f.nav.Waypoints) - 1
	f.nav.Waypoints = []av.Waypoint{f.nav.Waypoints[dmeIdx], f.nav.Waypoints[threshold], f.nav.Waypoints[airport]}

	// Reissue altitude-only CDME: the deletion pass removes the 5 DME
	// synthetic (alt was its only restriction), leaving just the threshold.
	// Must return Unable, not panic.
	ar2 := av.MakeAtAltitudeRestriction(2500)
	AssertUnable(t, f.nav.CrossDMEAt(7, &ar2, nil))
}

// TestCrossDMEAtUsesDeferredWaypoints verifies the synthetic waypoint is
// inserted into DeferredNavHeading.Waypoints when present.
func TestCrossDMEAtUsesDeferredWaypoints(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	origLen := len(f.nav.Waypoints)
	deferred := append([]av.Waypoint(nil), f.nav.Waypoints...)
	f.nav.DeferredNavHeading = &DeferredNavHeading{Waypoints: deferred}

	ar := av.MakeAtAltitudeRestriction(3000)
	if _, ok := f.nav.CrossDMEAt(5, &ar, nil).(av.UnableIntent); ok {
		t.Fatalf("unexpected unable")
	}

	if len(f.nav.Waypoints) != origLen {
		t.Errorf("expected nav.Waypoints untouched (len %d), got %d", origLen, len(f.nav.Waypoints))
	}
	if slicesIndex(f.nav.DeferredNavHeading.Waypoints, "_22L_5DME") < 0 {
		t.Errorf("expected _22L_5DME in deferred route, got %v",
			waypointFixes(f.nav.DeferredNavHeading.Waypoints))
	}
}

// TestHeadingAfterAltitudeDeferredBySpeedDoesNotRequestAltitude verifies
// that when a controller issues "descend and maintain X" followed by a
// speed reduction large enough to defer the altitude into AfterSpeed mode,
// then issues a heading off the STAR, the pilot does NOT ask "what altitude
// do you want us at?" — the altitude is still assigned, just deferred.
func TestHeadingAfterAltitudeDeferredBySpeedDoesNotRequestAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star LEFER/a4000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  9000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignAltitude(3000)
	if f.nav.Altitude.Assigned == nil || *f.nav.Altitude.Assigned != 3000 {
		t.Fatalf("expected Assigned=3000 after AssignAltitude, got %v", f.nav.Altitude.Assigned)
	}

	// Speed delta of 60kt > 20kt threshold, which moves the assigned
	// altitude into AfterSpeed and clears Altitude.Assigned.
	f.AssignSpeed(190)
	if f.nav.Altitude.AfterSpeed == nil || *f.nav.Altitude.AfterSpeed != 3000 {
		t.Fatalf("expected AfterSpeed=3000 after AssignSpeed, got %v", f.nav.Altitude.AfterSpeed)
	}
	if f.nav.Altitude.Assigned != nil {
		t.Fatalf("expected Assigned=nil after deferral, got %v", *f.nav.Altitude.Assigned)
	}

	f.AssignHeading(310, av.TurnRight)

	if f.nav.Approach.RequestAltitude {
		t.Error("pilot should not request altitude after heading: 3000 was already assigned (deferred via AfterSpeed)")
	}
}

// TestHeadingOffSTARWithNoAltitudeRequestsAltitude is the positive control
// for TestHeadingAfterAltitudeDeferredBySpeedDoesNotRequestAltitude: with
// no altitude assigned at all, the pilot should ask.
func TestHeadingOffSTARWithNoAltitudeRequestsAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star LEFER/a4000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  9000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignHeading(310, av.TurnRight)

	if !f.nav.Approach.RequestAltitude {
		t.Error("pilot should request altitude when vectored off STAR with no assigned altitude")
	}
}

func slicesIndex(wps []av.Waypoint, name string) int {
	for i, wp := range wps {
		if wp.Fix == name {
			return i
		}
	}
	return -1
}

func waypointFixes(wps []av.Waypoint) []string {
	fixes := make([]string, len(wps))
	for i, wp := range wps {
		fixes[i] = wp.Fix
	}
	return fixes
}

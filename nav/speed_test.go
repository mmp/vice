// nav/speed_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// TestSTARSpeedRestrictions verifies that STAR speed restrictions are
// respected at each fix (regression test for 9ae3110c).
func TestSTARSpeedRestrictions(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/s250/star DETGY/a7000/s210/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     280,
	})

	// Before DETGY: the SAJUL/s250 restriction should slow the aircraft.
	calls := 0
	f.BeforeFix("DETGY", func(f *FlightTest) {
		calls++
		if calls > 30 {
			f.AssertSpeedBelow(255)
		}
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertSpeedNear(210, 15)
	})

	f.Run()
}

// TestSpeed250Below10000 verifies that aircraft decelerate to 250kt or
// below when descending through 10000 ft.
func TestSpeed250Below10000(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  12000,
		InitialSpeed:     280,
	})

	// At DETGY (7000), the aircraft has descended through 10000 and
	// should have slowed to 250 or below.
	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertSpeedBelow(255)
	})

	f.Run()
}

// TestAfterFixSpeed verifies that "after fix, maintain speed" fires
// when the aircraft passes the named fix (regression test for 3155bf14).
func TestAfterFixSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AfterFixSpeed("DETGY", 210)

	// Before DETGY: speed should stay near 250
	f.BeforeFix("DETGY", func(f *FlightTest) {
		f.AssertSpeedAbove(240)
	})

	// After DETGY the after-fix speed fires and the aircraft decelerates
	f.AtFix("HAUPT", func(f *FlightTest) {
		f.AssertSpeedNear(210, 15)
	})

	f.Run()
}

func TestAssignSpeedUntilPreservesRangeRestriction(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	until := &av.SpeedUntil{Fix: "DETGY"}

	above := av.MakeAtOrAboveSpeedRestriction(250)
	aboveIntent, ok := f.nav.AssignSpeedUntil(&above, until).(av.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent for at-or-above speed until, got %T", aboveIntent)
	}
	if aboveIntent.Type != av.SpeedAtOrAbove || aboveIntent.Until != until {
		t.Fatalf("expected at-or-above speed until intent, got %+v", aboveIntent)
	}

	below := av.MakeAtOrBelowSpeedRestriction(210)
	belowIntent, ok := f.nav.AssignSpeedUntil(&below, until).(av.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent for at-or-below speed until, got %T", belowIntent)
	}
	if belowIntent.Type != av.SpeedAtOrBelow || belowIntent.Until != until {
		t.Fatalf("expected at-or-below speed until intent, got %+v", belowIntent)
	}
}

func TestAssignedAtOrAboveSpeedDoesNotAccelerateWhenAlreadyCompliant(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "B38M",
		InitialAltitude:  3000,
		InitialSpeed:     210,
		OnSTAR:           true,
	})

	sr := av.MakeAtOrAboveSpeedRestriction(190)
	f.nav.AssignSpeed(&sr, false)

	targetAltitude, _, _ := f.nav.TargetAltitude()
	targetSpeed, _ := f.nav.TargetSpeed(targetAltitude, &f.fp, f.weather(f.nav.FlightState.Altitude), nil, nil)
	if targetSpeed != f.nav.FlightState.IAS {
		t.Fatalf("target speed = %.0f, want current compliant speed %.0f", targetSpeed, f.nav.FlightState.IAS)
	}
}

func TestAssignedAtOrBelowSpeedDoesNotAccelerateWhenAlreadyCompliant(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "B38M",
		InitialAltitude:  3000,
		InitialSpeed:     190,
		OnSTAR:           true,
	})

	sr := av.MakeAtOrBelowSpeedRestriction(210)
	f.nav.AssignSpeed(&sr, false)

	targetAltitude, _, _ := f.nav.TargetAltitude()
	targetSpeed, _ := f.nav.TargetSpeed(targetAltitude, &f.fp, f.weather(f.nav.FlightState.Altitude), nil, nil)
	if targetSpeed != f.nav.FlightState.IAS {
		t.Fatalf("target speed = %.0f, want current compliant speed %.0f", targetSpeed, f.nav.FlightState.IAS)
	}
}

// TestVisualApproachSpeedUntilFiveMileFinal verifies that "maintain X
// until 5 mile final" holds the assigned speed until the aircraft is
// inside 5 NM of the runway threshold on a visual approach. Regression
// for a bug where the speed restriction was cancelled ~3 NM early
// because visual-approach routes didn't terminate with the arrival
// airport (so DistanceToEndOfApproach measured to the _3NM_FINAL
// waypoint instead of the threshold).
func TestVisualApproachSpeedUntilFiveMileFinal(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	// setupClearedVisual lays down [intercept (10nm), _22L_3NM_FINAL (3nm),
	// threshold, arrival airport]. Simulate the aircraft having already
	// passed the intercept fix so the remaining route is
	// [3NM_FINAL, threshold, airport].
	intercept := f.nav.Waypoints[0].Location
	final3 := f.nav.Waypoints[1].Location
	f.nav.Waypoints = f.nav.Waypoints[1:]

	// Position the aircraft 7 NM from the threshold (4 NM before 3NM_FINAL).
	pos7 := math.Point2LL(math.Lerp2f(4.0/7.0, final3, intercept))
	f.nav.FlightState.Position = pos7

	if d, err := f.nav.DistanceToEndOfApproach(); err != nil {
		t.Fatalf("DistanceToEndOfApproach error: %v", err)
	} else if d < 6.5 || d > 7.5 {
		t.Fatalf("at 7 NM final: DistanceToEndOfApproach = %.2f, want ~7", d)
	}

	sr := av.MakeAtSpeedRestriction(210)
	f.nav.AssignSpeedUntil(&sr, &av.SpeedUntil{MileFinal: 5})
	if f.nav.Speed.Assigned == nil {
		t.Fatal("AssignSpeedUntil should store the speed restriction")
	}

	targetAltitude, _, _ := f.nav.TargetAltitude()
	spd, _ := f.nav.TargetSpeed(targetAltitude, &f.fp, f.weather(f.nav.FlightState.Altitude), nil, nil)
	if f.nav.Speed.Assigned == nil {
		t.Fatal("speed restriction cleared too early at 7 NM from threshold")
	}
	if spd < 205 || spd > 215 {
		t.Errorf("at 7 NM final: target speed = %.0f, want ~210", spd)
	}

	// Move to 4 NM from threshold (1 NM before 3NM_FINAL); the hardcoded
	// "inside 5 mile final" cancellation should now fire.
	pos4 := math.Point2LL(math.Lerp2f(1.0/7.0, final3, intercept))
	f.nav.FlightState.Position = pos4
	if d, err := f.nav.DistanceToEndOfApproach(); err != nil {
		t.Fatalf("DistanceToEndOfApproach error: %v", err)
	} else if d < 3.5 || d > 4.5 {
		t.Fatalf("at 4 NM final: DistanceToEndOfApproach = %.2f, want ~4", d)
	}

	f.nav.TargetSpeed(targetAltitude, &f.fp, f.weather(f.nav.FlightState.Altitude), nil, nil)
	if f.nav.Speed.Assigned != nil {
		t.Errorf("speed restriction should be cleared inside 5 NM final, still set to %v", f.nav.Speed.Assigned)
	}
}

// TestDirectSpeedCancelsAfterFixSpeed verifies that a direct speed
// assignment clears any pending after-fix speed (regression test for 3155bf14).
func TestDirectSpeedCancelsAfterFixSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AfterFixSpeed("DETGY", 210)

	// After setting after-fix, assign a direct speed — should clear it
	f.AfterTicks(30, func(f *FlightTest) {
		f.AssignSpeed(230)
		// The after-fix speed for DETGY should be cleared
		nfa := f.nav.FixAssignments["DETGY"]
		if nfa.Depart.Speed != nil {
			t.Errorf("after-fix speed for DETGY should be nil after direct speed assignment, got %v", nfa.Depart.Speed)
		}
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		// Should be near 230 (the direct assignment), not 210
		f.AssertSpeedNear(230, 15)
	})

	f.Run()
}

// TestCompoundSpeed verifies multi-segment speed assignments
// (regression test for fa6cd545).
func TestCompoundSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star ROSLY/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     280,
		OnSTAR:           true,
	})

	sr250 := av.MakeAtSpeedRestriction(250)
	sr210 := av.MakeAtSpeedRestriction(210)
	sr180 := av.MakeAtSpeedRestriction(180)
	f.CompoundSpeed([]av.CompoundSpeedSegment{
		{Speed: &sr250, UntilFix: "DETGY"},
		{Speed: &sr210, UntilFix: "HAUPT"},
		{Speed: &sr180},
	})

	// Before DETGY: speed should be decelerating toward 250
	calls := 0
	f.BeforeFix("DETGY", func(f *FlightTest) {
		calls++
		if calls > 30 {
			f.AssertSpeedBelow(255)
		}
	})

	// At HAUPT: the 210 segment has been active since DETGY
	f.AtFix("HAUPT", func(f *FlightTest) {
		f.AssertSpeedNear(210, 15)
	})

	// After HAUPT: the 180 segment kicks in
	f.AtFix("LEFER", func(f *FlightTest) {
		f.AssertSpeedNear(180, 15)
	})

	f.Run()
}

// TestAfterFixDescendAltitude verifies "after fix, descend and maintain"
// (regression test for 9274cf11).
func TestAfterFixDescendAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star LEFER/a4000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
	})

	f.AfterFixAltitude("DETGY", 5000)

	// At DETGY: the after-fix altitude should be assigned
	f.AtFix("DETGY", func(f *FlightTest) {
		if f.nav.Altitude.Assigned == nil || *f.nav.Altitude.Assigned != 5000 {
			t.Errorf("at DETGY: expected assigned altitude 5000, got %v", f.nav.Altitude.Assigned)
		}
	})

	// At HAUPT: aircraft should have descended past the charted 6000
	// toward the assigned 5000
	f.AtFix("HAUPT", func(f *FlightTest) {
		f.AssertAltitudeNear(5000, 200)
	})

	f.Run()
}

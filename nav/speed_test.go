// nav/speed_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
)

// TestViaProcedureSpeedOverridesPublished verifies that a speed assigned
// with climb via SID or descend via STAR, the "except maintain (speed)"
// form, replaces the procedure's published speeds, and that a plain via
// instruction cancels an assigned speed.
func TestViaProcedureSpeedOverridesPublished(t *testing.T) {
	newArrival := func(t *testing.T) *FlightTest {
		return NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL/a10000/s250/star DETGY/a7000/s210/star HAUPT/a6000/star",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  11000,
			InitialSpeed:     250,
			AssignedAltitude: 11000,
		})
	}

	t.Run("STAR", func(t *testing.T) {
		f := newArrival(t)
		f.AfterTicks(10, func(f *FlightTest) {
			f.DescendViaSTAR()
			f.AssignSpeed(240)
		})
		f.AtFix("DETGY", func(f *FlightTest) { f.AssertSpeedNear(240, 10) })
		f.Run()
	})

	t.Run("PlainViaCancelsAssignedSpeed", func(t *testing.T) {
		f := newArrival(t)
		f.AfterTicks(10, func(f *FlightTest) {
			f.AssignSpeed(240)
			f.DescendViaSTAR()
			if f.nav.Speed.Assigned != nil {
				t.Errorf("expected descend via STAR to cancel the assigned speed, got %v", f.nav.Speed.Assigned)
			}
		})
		f.AtFix("DETGY", func(f *FlightTest) { f.AssertSpeedNear(210, 15) })
		f.Run()
	})

	t.Run("SID", func(t *testing.T) {
		f := NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "IAH TTAPS/a4000-/s210 BOTLL/a5000- MMUGS GRAYN/a11000+ YOKEM SBI LLA",
			DepartureAirport: "KIAH",
			ArrivalAirport:   "KMCO",
			AircraftType:     "B739",
			InitialAltitude:  2500,
			InitialSpeed:     210,
			ClearedAltitude:  5000,
			OnSID:            true,
		})
		f.nav.FinalAltitude = 35000
		f.nav.FlightState.InitialDepartureClimb = true
		f.ClimbViaSID()
		f.AssignSpeed(250)

		f.AtFix("BOTLL", func(f *FlightTest) { f.AssertSpeedNear(250, 10) })
		f.Run()
	})
}

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

	until := &speech.SpeedUntil{Fix: "DETGY"}

	above := av.MakeAtOrAboveSpeedRestriction(250)
	aboveIntent, ok := f.nav.AssignSpeedUntil(&above, until, f.temp()).(speech.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent for at-or-above speed until, got %T", aboveIntent)
	}
	if aboveIntent.Type != speech.SpeedAtOrAbove || aboveIntent.Until != until {
		t.Fatalf("expected at-or-above speed until intent, got %+v", aboveIntent)
	}

	below := av.MakeAtOrBelowSpeedRestriction(210)
	belowIntent, ok := f.nav.AssignSpeedUntil(&below, until, f.temp()).(speech.SpeedIntent)
	if !ok {
		t.Fatalf("expected SpeedIntent for at-or-below speed until, got %T", belowIntent)
	}
	if belowIntent.Type != speech.SpeedAtOrBelow || belowIntent.Until != until {
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
	f.nav.AssignSpeed(&sr, false, f.temp())

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
	f.nav.AssignSpeed(&sr, false, f.temp())

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
	f.nav.AssignSpeedUntil(&sr, &speech.SpeedUntil{MileFinal: 5}, f.temp())
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
	f.CompoundSpeed([]speech.CompoundSpeedSegment{
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
	// toward the assigned 5000. (HAUPT has a large course change, so it is
	// sequenced ~2.4nm before the fix when the fly-by turn begins, with
	// some of the descent still to come.)
	f.AtFix("HAUPT", func(f *FlightTest) {
		f.AssertAltitudeNear(5000, 350)
	})

	f.Run()
}

// TestDeferredSpeedHoldsCurrentSpeed verifies that a speed reduction
// deferred by a subsequent altitude assignment ("reduce speed to 180",
// then "descend and maintain 6,000") is flown by holding the current speed
// during the descent rather than accelerating back to the default speed
// profile.
func TestDeferredSpeedHoldsCurrentSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	var deferredIAS float32
	f.AfterTicks(5, func(f *FlightTest) {
		f.AssignSpeed(180)
	})
	f.AfterTicks(25, func(f *FlightTest) {
		// Still more than 20kts from 180, so the reduction is deferred.
		f.AssignAltitude(6000)
		if f.nav.Speed.AfterAltitude == nil {
			t.Fatalf("speed assignment was not deferred by the descent")
		}
		deferredIAS = f.nav.FlightState.IAS
	})

	f.BeforeFix("DETGY", func(f *FlightTest) {
		if deferredIAS > 0 {
			f.AssertSpeedBelow(deferredIAS + 1)
		}
	})

	// Once level at 6,000, the deferred reduction takes effect.
	f.AfterTicks(300, func(f *FlightTest) {
		f.AssertAltitudeNear(6000, 50)
		f.AssertSpeedNear(180, 5)
	})

	f.Run()
}

// TestThenSpeedHoldsCurrentSpeed verifies the same for an explicitly
// deferred assignment: "descend and maintain 6,000, then reduce speed to
// 180".
func TestThenSpeedHoldsCurrentSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     210,
		OnSTAR:           true,
	})

	var deferredIAS float32
	f.AfterTicks(5, func(f *FlightTest) {
		f.AssignAltitude(6000)
		f.AssignSpeedAfterAltitude(180)
		deferredIAS = f.nav.FlightState.IAS
	})

	f.BeforeFix("DETGY", func(f *FlightTest) {
		if deferredIAS > 0 {
			f.AssertSpeedBelow(deferredIAS + 1)
		}
	})

	f.AfterTicks(300, func(f *FlightTest) {
		f.AssertAltitudeNear(6000, 50)
		f.AssertSpeedNear(180, 5)
	})

	f.Run()
}

// TestSpeedNotDeferredWhenLevel verifies that restating the altitude an
// aircraft is already level at does not defer an in-progress speed
// reduction; there is no level off to defer it to, so it would never take
// effect.
func TestSpeedNotDeferredWhenLevel(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AfterTicks(5, func(f *FlightTest) {
		f.AssignSpeed(180)
	})
	f.AfterTicks(25, func(f *FlightTest) {
		f.AssignAltitude(8000)
		if f.nav.Speed.AfterAltitude != nil {
			t.Errorf("speed assignment deferred to the current altitude")
		}
	})

	f.AfterTicks(150, func(f *FlightTest) {
		f.AssertSpeedNear(180, 5)
	})

	f.Run()
}

// TestAssignedSpeedIsFlown verifies that the fastest speed an aircraft
// accepts is the speed it actually flies: its groundspeed matches the TAS
// for its IAS rather than being held to its cruise speed.
func TestAssignedSpeedIsFlown(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "C172",
		InitialAltitude:  5000,
		InitialSpeed:     100,
		AssignedAltitude: 5000,
	})

	maxIAS := 10 * math.Floor(f.nav.maxIAS(f.temp())/10)
	over := av.MakeAtSpeedRestriction(maxIAS + 10)
	AssertUnable(t, f.nav.AssignSpeed(&over, false, f.temp()))

	sr := av.MakeAtSpeedRestriction(maxIAS)
	if intent, ok := f.nav.AssignSpeed(&sr, false, f.temp()).(speech.UnableIntent); ok {
		t.Fatalf("%.0f knots: unexpected unable: %v", maxIAS, intent)
	}

	f.AfterTicks(120, func(f *FlightTest) {
		f.AssertSpeedNear(maxIAS, 1)
		tas := av.IASToTAS(f.nav.FlightState.IAS, f.nav.FlightState.Altitude, f.temp())
		if tas <= f.nav.Perf.Speed.CruiseTAS {
			t.Errorf("TAS %.1f for %.0f KIAS should exceed the %.0f knot cruise TAS", tas, maxIAS, f.nav.Perf.Speed.CruiseTAS)
		}
		if math.Abs(f.nav.FlightState.GS-tas) > 1 {
			t.Errorf("groundspeed %.1f doesn't match TAS %.1f in calm wind", f.nav.FlightState.GS, tas)
		}
	})
	f.Run()
}

// TestJetMaxSpeedBelowCrossover verifies that jets are limited by the
// estimated V_MO at low altitude rather than their maximum TAS converted to
// IAS.
func TestJetMaxSpeedBelowCrossover(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
		AssignedAltitude: 10000,
	})

	if m := f.nav.maxIAS(f.temp()); m < 300 || m > 360 {
		t.Errorf("A320 maximum IAS at 10,000': got %.1f, want 300-360", m)
	}
	fast := av.MakeAtSpeedRestriction(400)
	AssertUnable(t, f.nav.AssignSpeed(&fast, false, f.temp()))
	ok := av.MakeAtSpeedRestriction(300)
	if intent, unable := f.nav.AssignSpeed(&ok, false, f.temp()).(speech.UnableIntent); unable {
		t.Errorf("300 knots: unexpected unable: %v", intent)
	}
}

// TestSlowestPracticalBelowMinSpeed verifies that an aircraft whose
// performance data has a minimum speed above its landing speed can still
// slow to the speed it was cleared for.
func TestSlowestPracticalBelowMinSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "B732",
		InitialAltitude:  5000,
		InitialSpeed:     180,
		AssignedAltitude: 5000,
	})
	if f.nav.Perf.Speed.Min <= f.nav.Perf.Speed.Landing+5 {
		t.Fatalf("B732 performance data no longer has min %.0f above landing+5 %.0f",
			f.nav.Perf.Speed.Min, f.nav.Perf.Speed.Landing+5)
	}

	f.nav.MaintainSlowestPractical()
	f.AfterTicks(120, func(f *FlightTest) {
		f.AssertSpeedNear(f.nav.Perf.Speed.Landing+5, 1)
	})
	f.Run()
}

// TestMachAssignmentLimits verifies that Mach assignments are limited by
// what the aircraft can actually fly and that an accepted Mach number is
// reached.
func TestMachAssignmentLimits(t *testing.T) {
	flight := func(acType string, alt, mach float32) *FlightTest {
		return NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL DETGY HAUPT",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     acType,
			InitialAltitude:  alt,
			InitialSpeed:     av.MachToIAS(mach, alt),
			AssignedAltitude: alt,
		})
	}
	assign := func(f *FlightTest, mach float32) speech.CommandIntent {
		return f.nav.AssignMach(mach, false, f.temp())
	}
	accepted := func(f *FlightTest, mach float32) {
		t.Helper()
		if intent, ok := assign(f, mach).(speech.UnableIntent); ok {
			t.Errorf("M%.2f: unexpected unable: %v", mach, intent)
		}
	}

	// The C56X's maximum TAS is reached at about M0.64 at FL350, well
	// below its maximum Mach number.
	f := flight("C56X", 35000, .60)
	AssertUnable(t, assign(f, .70))
	AssertUnable(t, assign(f, .65))
	accepted(f, .64)
	f.AfterTicks(120, func(f *FlightTest) {
		if m := f.nav.Mach(); math.Abs(m-.64) > .005 {
			t.Errorf("C56X at FL350: got M%.3f, want M0.64", m)
		}
	})
	f.Run()

	// When the maximum Mach number is the limit, it is accepted exactly.
	f = flight("A320", 35000, .78)
	accepted(f, .83)
	AssertUnable(t, assign(f, .84))

	// Mach may be assigned at FL240 and above.
	accepted(flight("A320", 24000, .60), .65)
	AssertUnable(t, assign(flight("A320", 23000, .60), .65))
}

// TestMachAssignmentDefersClimb verifies that a climb deferred until an
// assigned Mach number is reached isn't left pending indefinitely.
func TestMachAssignmentDefersClimb(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "C56X",
		InitialAltitude:  35000,
		InitialSpeed:     av.MachToIAS(.55, 35000),
		AssignedAltitude: 35000,
	})
	f.AssignAltitude(39000)
	f.nav.AssignMach(.64, false, f.temp())
	if f.nav.Altitude.AfterSpeed == nil {
		t.Fatal("expected the climb to be deferred until the speed change completes")
	}

	f.AfterTicks(600, func(f *FlightTest) {
		f.AssertAltitudeAbove(36000)
	})
	f.Run()
}

// nav/approach_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	gomath "math"
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func TestSelectVisualApproachRouteUsesLaterViableIntercept(t *testing.T) {
	nmPerLong := float32(60)
	n := Nav{
		FlightState: FlightState{
			Position:          math.NM2LL([2]float32{1, -1}, nmPerLong),
			Heading:           225,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: 0,
		},
	}
	n.Perf.Category.CWT = "F" // commercial jet → stabilized-final filter applies

	ref := &av.Approach{
		Type:      av.RNAVApproach,
		Runway:    "36",
		Threshold: math.NM2LL([2]float32{0, 0}, nmPerLong),
		Waypoints: []av.WaypointArray{{
			{Fix: "BASE0", Location: math.NM2LL([2]float32{1, -4}, nmPerLong)},
			{Fix: "BASE1", Location: math.NM2LL([2]float32{-2, -4}, nmPerLong)},
			{Fix: "DOGLEG0", Location: math.NM2LL([2]float32{-2, -2}, nmPerLong)},
			{Fix: "DOGLEG1", Location: math.NM2LL([2]float32{0, -2}, nmPerLong)},
			{Fix: "RW36", Location: math.NM2LL([2]float32{0, 0}, nmPerLong)},
		}},
	}

	join := n.selectVisualApproachRoute(nil, []*av.Approach{ref})
	if join == nil {
		t.Fatal("expected visual join candidate")
	}

	got := math.LL2NM(join.location, nmPerLong)
	want := [2]float32{-2, -4}
	if math.Distance2f(got, want) > 0.05 {
		t.Fatalf("join = %.2f, %.2f; want later viable intercept near %.2f, %.2f",
			got[0], got[1], want[0], want[1])
	}
	if join.segment != 0 {
		t.Fatalf("segment = %d, want 0 for the base segment", join.segment)
	}
}

// TestSelectVisualApproachRouteJFK13L is a regression test for the VIV3852
// case: aircraft south of BUZON-TELEX, heading 040°, cleared for the JFK
// 13L visual. The two reference routes (northern: BUZON-TELEX-CAXUN; southern:
// ASALT-CNRSE-LEISA-SILJY-ROBJE) both end at RW13L. The aircraft's heading
// crosses BUZON-TELEX with distanceToThreshold ~9 nm — previously rejected
// by a `> 8` cap, falling through to a synthesized 3-NM final that visually
// looked like joining the southern reference. The fix is selecting the
// northern intercept.
func TestSelectVisualApproachRouteJFK13L(t *testing.T) {
	faa, ok := av.DB.Airports["KJFK"]
	if !ok {
		t.Fatal("KJFK not in aviation DB")
	}
	nmPerLong := math.NMPerLongitudeAt(faa.Location)
	magVar, err := av.DB.MagneticGrid.Lookup(faa.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup: %v", err)
	}

	northern := parseRoute(t, "BUZON/a2900/iaf TELEX/a2100+/if CAXUN/a1500+/faf")
	southern := parseRoute(t, "ASALT/if/a3000/s210 CNRSE/a2000+/faf LEISA/a1246+ SILJY/a835+ ROBJE/a450+")
	rwy, ok := av.LookupRunway("KJFK", "13L")
	if !ok {
		t.Fatal("KJFK 13L not found")
	}
	thresholdWP := av.Waypoint{Fix: "RW13L", Location: rwy.Threshold}
	northern = append(northern, thresholdWP)
	southern = append(southern, thresholdWP)

	ref := &av.Approach{
		Type:      av.VisualApproach,
		Runway:    "13L",
		Threshold: rwy.Threshold,
		Waypoints: []av.WaypointArray{southern, northern},
	}

	// Aircraft south of BUZON-TELEX line, roughly between BUZON and TELEX,
	// heading 040° toward the segment. Position is in NM offsets from BUZON.
	var buzon math.Point2LL
	for _, wp := range northern {
		if wp.Fix == "BUZON" {
			buzon = wp.Location
		}
	}
	bNM := math.LL2NM(buzon, nmPerLong)
	pos := math.NM2LL([2]float32{bNM[0] + 1.76, bNM[1] - 0.69}, nmPerLong)

	n := Nav{
		FlightState: FlightState{
			Position:          pos,
			Heading:           40,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: magVar,
		},
	}
	n.Perf.Category.CWT = "F" // commercial jet (A320)

	join := n.selectVisualApproachRoute(nil, []*av.Approach{ref})
	if join == nil {
		t.Fatal("expected a visual join")
	}
	if join.finalPoint {
		t.Errorf("expected an intercept, got synthesized 3-NM final")
	}
	if join.route[0].Fix != "BUZON" {
		t.Errorf("joined the wrong reference: route[0]=%s, want BUZON (northern)",
			join.route[0].Fix)
	}
	if join.segment != 0 {
		t.Errorf("segment = %d, want 0 (BUZON-TELEX intercept)", join.segment)
	}
	if join.distanceToThreshold < 3 || join.distanceToThreshold > 10 {
		t.Errorf("distanceToThreshold = %.2f, want roughly 6-10 nm", join.distanceToThreshold)
	}
}

func TestPrepareForChartedVisualSkipsBehindSegmentIntercept(t *testing.T) {
	nmPerLong := float32(60)
	n := Nav{
		FlightState: FlightState{
			Position:       math.NM2LL([2]float32{0, 0}, nmPerLong),
			Heading:        90,
			NmPerLongitude: nmPerLong,
			ArrivalAirport: av.Waypoint{Fix: "KTEST"},
		},
		Approach: NavApproach{
			Assigned: &av.Approach{
				Type:   av.ChartedVisualApproach,
				Runway: "09",
				Waypoints: []av.WaypointArray{{
					{Fix: "A", Location: math.NM2LL([2]float32{-1, -1}, nmPerLong)},
					{Fix: "B", Location: math.NM2LL([2]float32{-1, 1}, nmPerLong)},
					{Fix: "C", Location: math.NM2LL([2]float32{1, 1}, nmPerLong)},
					{Fix: "D", Location: math.NM2LL([2]float32{1, -1}, nmPerLong)},
				}},
			},
		},
	}

	intent := n.prepareForChartedVisual()
	if _, unable := intent.(av.UnableIntent); unable {
		t.Fatalf("unexpected unable intent: %v", intent)
	}
	if len(n.Waypoints) < 2 {
		t.Fatalf("waypoints = %v", n.Waypoints)
	}
	if n.Waypoints[0].Fix != "intercept" {
		t.Fatalf("first waypoint = %q, want intercept", n.Waypoints[0].Fix)
	}

	intercept := math.LL2NM(n.Waypoints[0].Location, nmPerLong)
	if math.Abs(intercept[0]-1) > 0.05 || math.Abs(intercept[1]) > 0.05 {
		t.Fatalf("intercept = %.2f, %.2f; want near 1.00, 0.00", intercept[0], intercept[1])
	}
}

// TestDirectToApproachFixNoDescentWithoutClearance verifies that going
// direct to a fix on the approach does NOT cause descent unless the
// approach has been cleared (regression test for 36b2bd31).
//
// ROSLY is on the I22L approach. When the aircraft's route does not
// include ROSLY, DirectFix finds it via the approach waypoints (source =
// waypointSourceApproach) and sets InterceptState = OnApproachCourse
// without enabling altitude restrictions.
func TestDirectToApproachFixNoDescentWithoutClearance(t *testing.T) {
	// Route does NOT include ROSLY so that DirectFix will find it on the approach.
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000, // hold at 5000
	})

	f.ExpectApproach("I22L")
	// Direct to ROSLY — which is on the I22L approach but not in our route
	f.DirectFix("ROSLY")

	f.AfterTicks(20, func(f *FlightTest) {
		// InterceptState should be OnApproachCourse (tracking laterally)
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Errorf("expected OnApproachCourse, got %d", f.nav.Approach.InterceptState)
		}
		// Should NOT be cleared
		if f.nav.Approach.Cleared {
			t.Errorf("approach should not be cleared")
		}
	})

	// After 100 ticks without clearance, altitude should be roughly unchanged
	f.AfterTicks(100, func(f *FlightTest) {
		f.AssertAltitudeNear(5000, 200)
	})

	// Now clear the approach
	f.AfterTicks(101, func(f *FlightTest) {
		f.ClearedApproach("I22L")
	})

	// Give enough time for the descent to start after clearance at tick 101.
	// At tick 200 the aircraft should be descending.
	f.AfterTicks(200, func(f *FlightTest) {
		f.AssertAltitudeBelow(5000)
	})

	f.AtFix("ROSLY", func(f *FlightTest) {
		// After clearance and descent, should be below 5000
	})

	f.Run()
}

// TestAtFixClearedApproach verifies that "at fix, cleared approach"
// activates the approach clearance when the aircraft passes the named fix
// (regression test for 61db003f).
func TestAtFixClearedApproach(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  7000,
		InitialSpeed:     210,
	})

	f.ExpectApproach("I22L")
	f.AtFixCleared("ROSLY", "I22L", false)

	// Before ROSLY: approach should NOT be cleared
	f.BeforeFix("ROSLY", func(f *FlightTest) {
		if f.nav.Approach.Cleared {
			t.Errorf("tick %d: approach should not be cleared before ROSLY", f.tick)
		}
	})

	// After ROSLY: the at-fix clearance should have fired
	f.AtFix("ROSLY", func(f *FlightTest) {
		if !f.nav.Approach.Cleared {
			t.Errorf("approach should be cleared after passing ROSLY")
		}
	})

	f.Run()
}

// TestAtFixInterceptApproachOnlyFix verifies that "at FIX intercept the
// localizer" works when the named fix is on the assigned approach but not
// yet in the aircraft's route (e.g., aircraft is being vectored). The
// command should splice the direct-to-fix route in via directFixWaypoints
// and arm the at-fix intercept.
func TestAtFixInterceptApproachOnlyFix(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectApproach("I22L")
	intent := f.AtFixIntercept("ROSLY")
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("AtFixIntercept returned unable: %+v", intent)
	}

	if f.nav.Approach.AtFixInterceptFix != "ROSLY" {
		t.Errorf("AtFixInterceptFix = %q, want ROSLY", f.nav.Approach.AtFixInterceptFix)
	}
	if f.nav.Approach.InterceptState != OnApproachCourse {
		t.Errorf("expected OnApproachCourse, got %d", f.nav.Approach.InterceptState)
	}
	if f.nav.Approach.Cleared {
		t.Errorf("approach should not be cleared")
	}
}

func TestAtFixInterceptSkipsRouteHeadingAtApproachFix(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "ERNEI/a7000-8000 GOSHI/a6000/s210/h035",
		DepartureAirport: "KDCA",
		ArrivalAirport:   "KBOS",
		AircraftType:     "B738",
		InitialAltitude:  6000,
		InitialSpeed:     210,
		AssignedAltitude: 6000,
	})
	f.SetWind(360, 27)
	f.ExpectApproach("I4R")
	f.AtFixIntercept("GOSHI")

	f.AtFix("GOSHI", func(f *FlightTest) {
		if f.nav.Heading.Assigned != nil {
			t.Fatalf("at-fix intercept left aircraft on heading %03.0f, want LNAV", *f.nav.Heading.Assigned)
		}
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Fatalf("InterceptState = %d, want OnApproachCourse", f.nav.Approach.InterceptState)
		}
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != "WINNI" {
			t.Fatalf("next waypoint = %v, want WINNI", f.nav.Waypoints)
		}
	})

	f.Run()
}

// TestAtFixClearedApproachOnlyFix verifies the same approach-only-fix
// relaxation for AtFixCleared.
func TestAtFixClearedApproachOnlyFix(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectApproach("I22L")
	f.AtFixCleared("ROSLY", "I22L", false)

	if f.nav.Approach.AtFixClearedRoute == nil {
		t.Errorf("AtFixClearedRoute should be populated for approach-only fix")
	}
}

// TestApproachWindCorrectionOnLocalizer verifies that an aircraft tracks
// the extended centerline closely despite crosswind. The bug (216f11fa)
// caused the aircraft to drift off the localizer because TurningToJoin
// flew the raw runway heading without crabbing into the wind.
func TestApproachWindCorrectionOnLocalizer(t *testing.T) {
	// Position northwest of the localizer (left of outbound course).
	// The westerly wind pushes the aircraft east toward the centerline.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(12, -3)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   180,
	})

	// Strong crosswind from the west — 40kt creates a crab angle (~11°)
	// that exceeds the old heading tolerance of 10° in shouldTurnToIntercept.
	f.SetWind(270, 40)
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	// Once established on the approach (OnApproachCourse), the aircraft
	// should stay within 0.1nm of the extended centerline despite
	// the crosswind. Without the wind correction fix, it drifts to
	// ~1.9nm before slowly oscillating back.
	for tick := 200; tick <= 800; tick += 10 {
		f.AfterTicks(tick, func(f *FlightTest) {
			if f.nav.Approach.InterceptState == OnApproachCourse {
				f.AssertOnExtendedCenterline(0.1)
			}
		})
	}

	f.Run()
}

// TestDirectFixRevokesApproachClearance verifies that giving a "direct fix"
// to an aircraft that has been cleared for an approach implicitly revokes
// the approach clearance: the aircraft continues to fly the approach
// waypoints laterally but does not descend via approach altitude
// restrictions.
func TestDirectFixRevokesApproachClearance(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	// This sequence must run after the first tick so the random state
	// and deferred-heading timing match the intended scenario.
	f.AfterTicks(1, func(f *FlightTest) {
		f.ExpectApproach("I22L")
		// 1. Direct to ROSLY (on the I22L approach)
		f.DirectFix("ROSLY")
		// 2. Clear the approach
		f.ClearedApproach("I22L")
		// 3. Direct to ROSLY again — revokes the approach clearance
		f.DirectFix("ROSLY")
	})

	// InterceptState should be NotIntercepting (approach revoked)
	f.AfterTicks(20, func(f *FlightTest) {
		if f.nav.Approach.InterceptState != NotIntercepting {
			t.Errorf("expected NotIntercepting after second direct fix, got %d",
				f.nav.Approach.InterceptState)
		}
	})

	// Altitude should remain near 5000 — no approach descent
	f.AfterTicks(150, func(f *FlightTest) {
		f.AssertAltitudeNear(5000, 200)
	})

	f.Run()
}

// TestRNAVApproachViaHeading verifies that an aircraft on a heading can
// intercept the T-leg of an RNAV approach (BLINZ→DEBYE) and descend
// when cleared.
func TestRNAVApproachViaHeading(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "N040.55.22.265,W073.32.11.726",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KFRG",
		AircraftType:     "A320",
		InitialAltitude:  2500,
		InitialSpeed:     180,
		InitialHeading:   120,
	})
	f.ExpectApproach("R19")
	f.ClearedApproach("R19")

	f.AtFix("MOIRE", func(f *FlightTest) {
		f.AssertAltitudeNear(1500, 200)
	})
	f.Run()
}

// TestRNAVInterceptNoDescent verifies that an aircraft given an intercept
// command (without approach clearance) tracks the lateral course but does
// not descend.
func TestRNAVInterceptNoDescent(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "N040.55.22.265,W073.32.11.726",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KFRG",
		AircraftType:     "A320",
		InitialAltitude:  2500,
		InitialSpeed:     180,
		InitialHeading:   120,
	})
	f.ExpectApproach("R19")
	f.InterceptApproach()

	f.AtFix("MOIRE", func(f *FlightTest) {
		f.AssertAltitudeNear(2500, 50)
	})
	f.Run()
}

// TestLocalizerFlythroughSteepIntercept verifies that an aircraft on a
// heading that creates a >45° intercept angle is detected as an overshoot
// after it flies through the localizer, and requests vectors (since the
// heading is too far from the approach course for recovery).
func TestLocalizerFlythroughSteepIntercept(t *testing.T) {
	// Position 10nm out, 2nm right of outbound
	// Aircraft heading 280° (~56° intercept)
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(10, -2)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   280,
	})
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	// After the aircraft crosses through the localizer, overshoot detection
	// fires. The heading check should fail → request vectors.
	f.AfterTicks(300, func(f *FlightTest) {
		if f.nav.Approach.InterceptState != NotIntercepting {
			t.Errorf("expected NotIntercepting, got %d", f.nav.Approach.InterceptState)
		}
		if !f.nav.Approach.RequestVectors {
			t.Errorf("expected RequestVectors to be set")
		}
	})

	f.Run()
}

// TestOvershootPreservesSpeed verifies that after the pilot announces an
// overshoot and requests vectors, the aircraft holds its current IAS rather
// than accelerating to the default 250 below 10k.
func TestOvershootPreservesSpeed(t *testing.T) {
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(10, -2)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   280,
	})
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	f.AfterTicks(300, func(f *FlightTest) {
		if !f.nav.Approach.MissedApproachIntercept {
			t.Errorf("expected MissedApproachIntercept to be set after overshoot")
		}
		if f.nav.FlightState.IAS > 200 {
			t.Errorf("IAS %.0f: expected speed to be held near 180 after overshoot, not climbing toward 250",
				f.nav.FlightState.IAS)
		}
	})

	f.Run()
}

// TestLocalizerFlythroughLateTurn verifies that an aircraft with a
// reasonable intercept angle (~24°) but positioned very close to the
// localizer (0.3nm) overshoots because it crosses through before or
// during the turn, and then recovers
func TestLocalizerFlythroughLateTurn(t *testing.T) {
	// Position 8nm out, only 0.3nm left of outbound (NW side).
	// Heading 200° (~24° intercept) crosses through quickly.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(8, 0.3)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   250,
	})
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	// The aircraft overshoots but recovery criteria are met:
	// heading ~24° < 45°, well within capture cone at ~10nm from antenna,
	// well before FAF. Should eventually establish on the localizer.
	for tick := 200; tick <= 800; tick += 50 {
		f.AfterTicks(tick, func(f *FlightTest) {
			if f.nav.Approach.InterceptState == OnApproachCourse {
				f.AssertOnExtendedCenterline(0.05)
			}
		})
	}

	// By ZALPO the aircraft should have recovered and be on the approach.
	f.AtFix("ZALPO", func(f *FlightTest) {
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Errorf("expected OnApproachCourse after recovery, got %d", f.nav.Approach.InterceptState)
		}
		if f.nav.Approach.RequestVectors {
			t.Errorf("RequestVectors should not be set after successful recovery")
		}
	})

	f.Run()
}

// TestLocalizerOvershootRecovery verifies that after an overshoot, the
// recovery does not oscillate back and forth across the localizer. The
// aircraft is placed 0.5nm NW of the localizer with a ~24° intercept;
// it overshoots, receives a recovery heading, and the shouldTurnToIntercept
// mid-turn check switches it to the runway heading before re-crossing,
// resulting in a smooth capture with at most 2 centerline crossings.
func TestLocalizerOvershootRecovery(t *testing.T) {
	// Position 10nm out, 0.5nm left of outbound (NW side).
	// Heading 200° (~24° intercept) crosses through the localizer,
	// triggering overshoot recovery via handleLocalizerOvershoot.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(10, -0.5)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   200,
	})
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	var prevSD float32
	crossings := 0

	for tick := 2; tick <= 800; tick++ {
		f.AfterTicks(tick, func(f *FlightTest) {
			sd := f.SignedCenterlineDistance()

			if tick <= 10 || tick%10 == 0 {
				var assignedHdg float32
				if f.nav.Heading.Assigned != nil {
					assignedHdg = float32(*f.nav.Heading.Assigned)
				}
				t.Logf("tick=%d state=%d hdg=%.0f sd=%.3f assignedHdg=%.0f reqVec=%v",
					tick, f.nav.Approach.InterceptState,
					f.nav.FlightState.Heading, sd,
					assignedHdg,
					f.nav.Approach.RequestVectors)
			}

			// Count centerline crossings to detect oscillation.
			if tick > 5 && prevSD != 0 &&
				math.Sign(sd) != math.Sign(prevSD) &&
				math.Abs(sd) > 0.02 && math.Abs(prevSD) > 0.02 {
				crossings++
				t.Logf("tick=%d centerline crossing #%d: sd %.3f → %.3f",
					tick, crossings, prevSD, sd)
			}
			prevSD = sd
		})
	}

	f.AtFix("ZALPO", func(f *FlightTest) {
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Errorf("expected OnApproachCourse by ZALPO, got %d", f.nav.Approach.InterceptState)
		}
		if f.nav.Approach.RequestVectors {
			t.Errorf("RequestVectors should not be set after successful recovery")
		}
		// One or two crossings are OK (converging to centerline);
		// three or more indicates oscillation.
		if crossings > 2 {
			t.Errorf("oscillation detected: %d centerline crossings (expected ≤ 2)", crossings)
		}
	})

	f.Run()
}

func TestReissuedApproachClearanceDuringLocalizerCapture(t *testing.T) {
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(10, -0.5)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   200,
	})

	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	reissued := false
	for tick := 1; tick <= 300; tick++ {
		f.AfterTicks(tick, func(f *FlightTest) {
			if !reissued && f.nav.Approach.InterceptState == TurningToJoin {
				state := f.nav.Approach.InterceptState
				f.ClearedApproach("I22L")
				if f.nav.Approach.InterceptState != state {
					t.Fatalf("reissued clearance changed intercept state from %d to %d",
						state, f.nav.Approach.InterceptState)
				}
				reissued = true
			}
			if f.nav.Approach.RequestVectors {
				t.Fatalf("tick %d: RequestVectors unexpectedly set", tick)
			}
		})
	}

	f.AtFix("ZALPO", func(f *FlightTest) {
		if !reissued {
			t.Fatal("approach clearance was never reissued during capture")
		}
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Errorf("expected OnApproachCourse by ZALPO, got %d", f.nav.Approach.InterceptState)
		}
	})

	f.Run()
}

// TestLocalizerOvershootNearFAF verifies that an aircraft that overshoots
// the localizer too close to the FAF (within 2nm along the approach course)
// cannot recover and requests vectors instead.
func TestLocalizerOvershootNearFAF(t *testing.T) {
	// Position 1nm outbound from FAF, 0.3nm right of outbound
	// (SE = left of inbound). Too close to FAF for recovery.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.FAFOffset(1, 0.3)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   200,
	})

	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	// The aircraft is too close to the FAF for recovery → requests vectors.
	f.AfterTicks(100, func(f *FlightTest) {
		if f.nav.Approach.InterceptState != NotIntercepting {
			t.Errorf("expected NotIntercepting, got %d", f.nav.Approach.InterceptState)
		}
		if !f.nav.Approach.RequestVectors {
			t.Errorf("expected RequestVectors to be set (too close to FAF)")
		}
	})

	f.Run()
}

// TestHeadingAndClearanceWhenOffHeading verifies that issuing a heading
// change together with an approach clearance does not trigger a spurious
// "unable to intercept" when the aircraft is still turning to the newly
// assigned heading. shouldTurnToIntercept must not be evaluated from the
// stale physical heading — it would simulate a fake direct turn from
// the current heading to the localizer and wrongly report a major
// overshoot.
func TestHeadingAndClearanceWhenOffHeading(t *testing.T) {
	// Aircraft 5nm outbound, 0.3nm NW of centerline, flying outbound
	// (~044°) — close to the extended centerline but heading the opposite
	// direction from landing. Controller issues heading 200° (a ~24°
	// intercept angle against the ~224° localizer) together with approach
	// clearance. Before the fix the pilot would immediately reject with
	// "unable to intercept" on the very first tick.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(5, -0.3)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   44,
	})

	f.ExpectApproach("I22L")
	f.AssignHeading(200, av.TurnClosest)
	f.ClearedApproach("I22L")

	// RequestVectors must never be set: the aircraft should turn to 200°
	// and then evaluate the intercept normally.
	for tick := 1; tick <= 300; tick++ {
		f.AfterTicks(tick, func(f *FlightTest) {
			if f.nav.Approach.RequestVectors {
				t.Fatalf("tick %d: RequestVectors unexpectedly set (hdg=%.0f)",
					tick, f.nav.FlightState.Heading)
			}
		})
	}

	f.Run()
}

// TestLocalizerOvershootOutsideOldTwoDegreeConeRecovers verifies that a
// minor overshoot outside the old 2 degree cone is still recoverable. A
// localizer recovery envelope that narrow caused ordinary vectored final
// intercepts to be rejected, which then made the aircraft request vectors
// and drop its approach clearance.
func TestLocalizerOvershootOutsideOldTwoDegreeConeRecovers(t *testing.T) {
	// Position 8nm from threshold, 0.5nm right of outbound (SE = left
	// of inbound). This is outside the old 2° threshold cone but still a
	// normal localizer recovery.
	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	pos := apg.ThresholdOffset(8, 0.5)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   200,
	})

	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	for tick := 1; tick <= 300; tick++ {
		f.AfterTicks(tick, func(f *FlightTest) {
			if f.nav.Approach.RequestVectors {
				t.Fatalf("tick %d: RequestVectors unexpectedly set", tick)
			}
		})
	}

	f.AtFix("ZALPO", func(f *FlightTest) {
		if f.nav.Approach.InterceptState != OnApproachCourse {
			t.Errorf("expected OnApproachCourse after recovery, got %d", f.nav.Approach.InterceptState)
		}
		if f.nav.Approach.RequestVectors {
			t.Errorf("RequestVectors should not be set after successful recovery")
		}
	})

	f.Run()
}

// TestThresholdOffset verifies that ThresholdOffset places points at the
// correct distance and lateral offset from the runway threshold.
func TestThresholdOffset(t *testing.T) {
	apg := LookupApproachGeometry(t, "KJFK", "I22L")

	// On centerline: distance from threshold should match distNM.
	for _, dist := range []float32{1, 5, 10, 20} {
		pos := apg.ThresholdOffset(dist, 0)
		got := math.NMDistance2LLFast(apg.Threshold, pos, apg.NmPerLongitude)
		if gomath.Abs(float64(got-dist)) > 0.05 {
			t.Errorf("ThresholdOffset(%.0f, 0): distance from threshold = %.3f nm, want ~%.0f",
				dist, got, dist)
		}
	}

	// On centerline: point should lie on the extended centerline.
	for _, dist := range []float32{5, 10} {
		pos := apg.ThresholdOffset(dist, 0)
		posNM := math.LL2NM(pos, apg.NmPerLongitude)
		threshNM := math.LL2NM(apg.Threshold, apg.NmPerLongitude)
		// Use a second point on the centerline for the line.
		pos2 := apg.ThresholdOffset(dist+1, 0)
		pos2NM := math.LL2NM(pos2, apg.NmPerLongitude)
		deviation := math.PointLineDistance(posNM, threshNM, pos2NM)
		if deviation > 0.001 {
			t.Errorf("ThresholdOffset(%.0f, 0): %.4f nm off centerline", dist, deviation)
		}
	}

	// Lateral offset: distance from the on-course point should match |lateralNM|.
	for _, tc := range []struct {
		dist, lateral float32
	}{
		{10, 2},
		{10, -2},
		{5, 0.5},
		{5, -0.5},
	} {
		onCourse := apg.ThresholdOffset(tc.dist, 0)
		offset := apg.ThresholdOffset(tc.dist, tc.lateral)
		lateralDist := math.NMDistance2LLFast(onCourse, offset, apg.NmPerLongitude)
		want := float32(gomath.Abs(float64(tc.lateral)))
		if gomath.Abs(float64(lateralDist-want)) > 0.05 {
			t.Errorf("ThresholdOffset(%.0f, %.1f): lateral distance = %.3f nm, want ~%.1f",
				tc.dist, tc.lateral, lateralDist, want)
		}
	}

	// Sign convention: negative lateral = left of outbound.
	// For KJFK 22L (runway heading ~224° true, outbound ~044°),
	// left of outbound points roughly NW, right points SE.
	left := apg.ThresholdOffset(10, -1)
	right := apg.ThresholdOffset(10, 1)
	onCourse := apg.ThresholdOffset(10, 0)

	// Left and right should be on opposite sides of the centerline.
	leftNM := math.LL2NM(left, apg.NmPerLongitude)
	rightNM := math.LL2NM(right, apg.NmPerLongitude)
	courseNM := math.LL2NM(onCourse, apg.NmPerLongitude)
	threshNM := math.LL2NM(apg.Threshold, apg.NmPerLongitude)
	sdLeft := math.SignedPointLineDistance(leftNM, threshNM, courseNM)
	sdRight := math.SignedPointLineDistance(rightNM, threshNM, courseNM)
	if math.Sign(sdLeft) == math.Sign(sdRight) {
		t.Errorf("left (sd=%.3f) and right (sd=%.3f) should be on opposite sides of centerline",
			sdLeft, sdRight)
	}

	// Perpendicularity: the on-course → offset vector should be ~90° from the course.
	outbound := math.OppositeHeading(apg.RunwayHeading)
	offsetHdg := math.Heading2LL(onCourse, right, apg.NmPerLongitude)
	angleDiff := math.HeadingDifference(outbound, offsetHdg)
	if gomath.Abs(float64(angleDiff)-90) > 1 {
		t.Errorf("offset direction %.1f° differs from course %.1f° by %.1f° (want ~90°)",
			float32(offsetHdg), float32(outbound), angleDiff)
	}
}

// TestFAFOffset verifies that FAFOffset places points at the correct
// distance and lateral offset from the FAF.
func TestFAFOffset(t *testing.T) {
	apg := LookupApproachGeometry(t, "KJFK", "I22L")

	// On centerline: distance from FAF should match distNM.
	for _, dist := range []float32{1, 3, 5} {
		pos := apg.FAFOffset(dist, 0)
		got := math.NMDistance2LLFast(apg.FAFLocation, pos, apg.NmPerLongitude)
		if gomath.Abs(float64(got-dist)) > 0.05 {
			t.Errorf("FAFOffset(%.0f, 0): distance from FAF = %.3f nm, want ~%.0f",
				dist, got, dist)
		}
	}

	// Lateral offset magnitude.
	for _, tc := range []struct {
		dist, lateral float32
	}{
		{1, 0.3},
		{1, -0.3},
		{3, 1},
	} {
		onCourse := apg.FAFOffset(tc.dist, 0)
		offset := apg.FAFOffset(tc.dist, tc.lateral)
		lateralDist := math.NMDistance2LLFast(onCourse, offset, apg.NmPerLongitude)
		want := float32(gomath.Abs(float64(tc.lateral)))
		if gomath.Abs(float64(lateralDist-want)) > 0.05 {
			t.Errorf("FAFOffset(%.0f, %.1f): lateral distance = %.3f nm, want ~%.1f",
				tc.dist, tc.lateral, lateralDist, want)
		}
	}

	// FAFOffset should use the same course as ThresholdOffset (outbound heading).
	// A point at FAFOffset(0, 0) should be at the FAF location.
	atFAF := apg.FAFOffset(0, 0)
	fafDist := math.NMDistance2LLFast(apg.FAFLocation, atFAF, apg.NmPerLongitude)
	if fafDist > 0.01 {
		t.Errorf("FAFOffset(0, 0) is %.4f nm from FAF, want ~0", fafDist)
	}

	// FAF should lie on the extended centerline (same outbound course as threshold).
	fafNM := math.LL2NM(apg.FAFLocation, apg.NmPerLongitude)
	threshNM := math.LL2NM(apg.Threshold, apg.NmPerLongitude)
	farNM := math.LL2NM(apg.ThresholdOffset(20, 0), apg.NmPerLongitude)
	fafDeviation := math.PointLineDistance(fafNM, threshNM, farNM)
	if fafDeviation > 0.1 {
		t.Errorf("FAF is %.3f nm off extended centerline (want <0.1)", fafDeviation)
	}
}

// TestExpectVisualApproachSynthesizesAssigned verifies that EVA{runway}
// synthesizes a VisualApproach with aggregated waypoints from the airport's
// VisualApproach and ILS/Localizer references, and assigns it.
func TestExpectVisualApproachSynthesizesAssigned(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	intent := f.ExpectVisualApproach("22L")
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("ExpectVisualApproach returned unable: %+v", intent)
	}

	if got := f.nav.Approach.AssignedId; got != "_VIS22L" {
		t.Errorf("AssignedId = %q, want _VIS22L", got)
	}
	ap := f.nav.Approach.Assigned
	if ap == nil {
		t.Fatal("Assigned is nil after EVA")
	}
	if ap.Type != av.VisualApproach {
		t.Errorf("Assigned.Type = %v, want VisualApproach", ap.Type)
	}
	if ap.Runway != "22L" {
		t.Errorf("Assigned.Runway = %q, want 22L", ap.Runway)
	}
	if (ap.Threshold == math.Point2LL{}) {
		t.Error("Assigned.Threshold is zero")
	}
	if len(ap.Waypoints) == 0 {
		t.Fatal("Assigned has no Waypoints")
	}

	// VisualReferences should include at least the I22L (ILS) approach.
	var sawILS bool
	for _, ref := range f.nav.Approach.VisualReferences {
		if ref.Type == av.ILSApproach {
			sawILS = true
		}
	}
	if !sawILS {
		t.Errorf("VisualReferences did not include any ILSApproach; got %d refs", len(f.nav.Approach.VisualReferences))
	}
}

// TestExpectVisualApproachStripsProcedureTurns verifies that ProcedureTurn
// metadata is cleared in the synthesized waypoints so that
// flyProcedureTurnIfNecessary / prepareForApproach do not try to fly an ILS
// procedure turn under a visual.
func TestExpectVisualApproachStripsProcedureTurns(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	f.ExpectVisualApproach("22L")

	for _, route := range f.nav.Approach.Assigned.Waypoints {
		for _, wp := range route {
			if wp.ProcedureTurn() != nil {
				t.Errorf("waypoint %s has ProcedureTurn set in synthesized visual", wp.Fix)
			}
		}
	}
}

// TestDirectFixOnILSAfterEVA verifies the user's "direct {ils-fix}" workflow
// after EVA: the aircraft routes via the ILS waypoints, InterceptState becomes
// OnApproachCourse, and InterceptedReference is set to the ILS approach.
func TestDirectFixOnILSAfterEVA(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectVisualApproach("22L")
	// ROSLY is on the I22L approach (not in the route).
	f.DirectFix("ROSLY")

	if f.nav.Approach.InterceptState != OnApproachCourse {
		t.Errorf("InterceptState = %d, want OnApproachCourse", f.nav.Approach.InterceptState)
	}
	if ref := f.nav.Approach.InterceptedReference; ref == nil {
		t.Error("InterceptedReference is nil; expected I22L ILS approach")
	} else if ref.Type != av.ILSApproach {
		t.Errorf("InterceptedReference.Type = %v, want ILSApproach", ref.Type)
	}
	if f.nav.Approach.Cleared {
		t.Error("approach should not be cleared")
	}
}

// TestAtFixInterceptILSAfterEVA verifies "at {fix} intercept the localizer"
// after EVA: AtFixInterceptFix is armed and InterceptedReference points at
// the ILS reference.
func TestAtFixInterceptILSAfterEVA(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectVisualApproach("22L")
	intent := f.AtFixIntercept("ROSLY")
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("AtFixIntercept returned unable: %+v", intent)
	}

	if f.nav.Approach.AtFixInterceptFix != "ROSLY" {
		t.Errorf("AtFixInterceptFix = %q, want ROSLY", f.nav.Approach.AtFixInterceptFix)
	}
	if ref := f.nav.Approach.InterceptedReference; ref == nil {
		t.Error("InterceptedReference is nil; expected I22L ILS approach")
	} else if ref.Type != av.ILSApproach {
		t.Errorf("InterceptedReference.Type = %v, want ILSApproach", ref.Type)
	}
}

// TestClearedVisualAlongILSUsesILSGeometry verifies that when CVA is issued
// after the aircraft has been committed to an ILS reference, the cleared
// visual flies the ILS route geometry — Assigned.Threshold/OppositeThreshold
// match the runway, and there are no synthetic _TOD/_3NM_FINAL waypoints.
func TestClearedVisualAlongILSUsesILSGeometry(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectVisualApproach("22L")
	f.DirectFix("ROSLY") // commits to the I22L route
	intent := f.ClearedVisualApproach("22L")
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("ClearedVisualApproach returned unable: %+v", intent)
	}

	if !f.nav.Approach.Cleared {
		t.Error("approach should be cleared")
	}
	ap := f.nav.Approach.Assigned
	if ap == nil {
		t.Fatal("Assigned is nil after CVA")
	}
	if ap.Type != av.VisualApproach {
		t.Errorf("Assigned.Type = %v, want VisualApproach", ap.Type)
	}
	if ap.FullName != "Visual Approach Runway 22L" {
		t.Errorf("Assigned.FullName = %q, want %q", ap.FullName, "Visual Approach Runway 22L")
	}
	for _, route := range ap.Waypoints {
		for _, wp := range route {
			if len(wp.Fix) > 0 && wp.Fix[0] == '_' {
				// Allow _RW prefixes (threshold) but not synthetic _TOD/_3NM_FINAL
				if wp.Fix == "_22L_TOD" || wp.Fix == "_22L_3NM_FINAL" || wp.Fix == "_22L_APPROACH_JOIN" {
					t.Errorf("Assigned.Waypoints contains synthetic visual waypoint %q; expected ILS-only route", wp.Fix)
				}
			}
		}
	}
}

// TestExpectVisualApproachResetsExpectApproachState verifies that EVA after
// a prior ExpectApproach clears any stale ILS state (InterceptState,
// AtFixInterceptFix, etc.).
func TestExpectVisualApproachResetsExpectApproachState(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	f.ExpectApproach("I22L")
	f.AtFixIntercept("ROSLY")
	if f.nav.Approach.AtFixInterceptFix == "" {
		t.Fatal("setup error: AtFixInterceptFix not set")
	}

	f.ExpectVisualApproach("22L")

	if f.nav.Approach.AtFixInterceptFix != "" {
		t.Errorf("AtFixInterceptFix = %q after EVA, want \"\"", f.nav.Approach.AtFixInterceptFix)
	}
	if f.nav.Approach.InterceptedReference != nil {
		t.Error("InterceptedReference should be cleared after EVA")
	}
	if f.nav.Approach.AssignedId != "_VIS22L" {
		t.Errorf("AssignedId = %q, want _VIS22L", f.nav.Approach.AssignedId)
	}
}

// TestInterceptApproachUnderEVAOnHeadingCommitsToLocalizer verifies that
// "intercept the localizer" issued while on a heading under a synthesized
// visual approach commits the aircraft to the ILS reference and yields an
// intent whose readback will mention the localizer.
func TestInterceptApproachUnderEVAOnHeadingCommitsToLocalizer(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	})

	f.ExpectVisualApproach("22L")
	f.AssignHeading(250, av.TurnLeft)

	intent := f.InterceptApproach()
	ai, ok := intent.(av.ApproachIntent)
	if !ok {
		t.Fatalf("InterceptApproach returned %T, want ApproachIntent", intent)
	}
	if ai.Type != av.ApproachIntercept {
		t.Errorf("Type = %v, want ApproachIntercept", ai.Type)
	}
	if !ai.HasLocalizer {
		t.Error("HasLocalizer = false, want true")
	}
	if ref := f.nav.Approach.InterceptedReference; ref == nil {
		t.Error("InterceptedReference is nil; expected I22L ILS reference")
	} else if ref.Type != av.ILSApproach {
		t.Errorf("InterceptedReference.Type = %v, want ILSApproach", ref.Type)
	}
}

// TestGetApproachPopulatesVisualReferencesForNamedVisual verifies that
// looking up a named VisualApproach (e.g., a charted "Mount Vernon Visual
// Runway 1") populates VisualReferences with itself, so that a subsequent
// ClearedApproach can synthesize a route from those waypoints rather than
// failing with "we don't know runway X".
func TestGetApproachPopulatesVisualReferencesForNamedVisual(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	named := &av.Approach{
		Id:       "MTV",
		FullName: "Mount Vernon Visual Runway 22L",
		Type:     av.VisualApproach,
		Runway:   "22L",
		Waypoints: []av.WaypointArray{{
			{Fix: "MTV1", Location: math.NM2LL([2]float32{-10, -10}, f.nav.FlightState.NmPerLongitude)},
			{Fix: "MTV2", Location: math.NM2LL([2]float32{-5, -5}, f.nav.FlightState.NmPerLongitude)},
		}},
	}
	airport := &av.Airport{Approaches: map[string]*av.Approach{"MTV": named}}

	ap, refs, err := f.nav.getApproach(airport, "MTV")
	if err != nil {
		t.Fatalf("getApproach(MTV) error: %v", err)
	}
	if ap != named {
		t.Errorf("getApproach returned wrong approach")
	}
	if len(refs) != 1 || refs[0] != named {
		t.Errorf("VisualReferences = %v, want %v", refs, []*av.Approach{named})
	}
}

// TestSelectVisualReferencesPrioritization verifies that ILS/Localizer are
// always included, and the visual-style component is picked by priority:
// VisualApproach > VOR > RNAV > ChartedVisual.
func TestSelectVisualReferencesPrioritization(t *testing.T) {
	mk := func(ty av.ApproachType, rwy string) *av.Approach {
		return &av.Approach{Type: ty, Runway: rwy}
	}
	hasType := func(refs []*av.Approach, ty av.ApproachType) bool {
		for _, r := range refs {
			if r.Type == ty {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name     string
		approach map[string]*av.Approach
		want     []av.ApproachType // expected types present (order-insensitive)
		notWant  []av.ApproachType // types that must not be present
	}{
		{
			name: "ILS and Visual together",
			approach: map[string]*av.Approach{
				"I22L": mk(av.ILSApproach, "22L"),
				"V22L": mk(av.VisualApproach, "22L"),
				"R22L": mk(av.RNAVApproach, "22L"),
			},
			want:    []av.ApproachType{av.ILSApproach, av.VisualApproach},
			notWant: []av.ApproachType{av.RNAVApproach},
		},
		{
			name: "ILS only — used alone",
			approach: map[string]*av.Approach{
				"I22L": mk(av.ILSApproach, "22L"),
			},
			want:    []av.ApproachType{av.ILSApproach},
			notWant: nil,
		},
		{
			name: "no ILS, VOR + RNAV — VOR wins by priority",
			approach: map[string]*av.Approach{
				"V22L": mk(av.VORApproach, "22L"),
				"R22L": mk(av.RNAVApproach, "22L"),
			},
			want:    []av.ApproachType{av.VORApproach},
			notWant: []av.ApproachType{av.RNAVApproach, av.ILSApproach},
		},
		{
			name: "no ILS, no VOR, RNAV + Charted — RNAV wins",
			approach: map[string]*av.Approach{
				"R22L":   mk(av.RNAVApproach, "22L"),
				"BVL22L": mk(av.ChartedVisualApproach, "22L"),
			},
			want:    []av.ApproachType{av.RNAVApproach},
			notWant: []av.ApproachType{av.ChartedVisualApproach},
		},
		{
			name: "different runway base ignored",
			approach: map[string]*av.Approach{
				"I31R": mk(av.ILSApproach, "31R"),
			},
			want:    nil,
			notWant: []av.ApproachType{av.ILSApproach},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs := selectVisualReferences(&av.Airport{Approaches: tt.approach}, "22L")
			for _, ty := range tt.want {
				if !hasType(refs, ty) {
					t.Errorf("missing %v in references; got %d refs", ty, len(refs))
				}
			}
			for _, ty := range tt.notWant {
				if hasType(refs, ty) {
					t.Errorf("unexpected %v in references", ty)
				}
			}
			if len(tt.want) == 0 && len(refs) != 0 {
				t.Errorf("expected empty references, got %d", len(refs))
			}
		})
	}
}

// TestClearedVisualReissuedDoesNotRecomputeRoute verifies that a re-issued
// CVA on an already-cleared visual approach is a no-op for the route — it
// re-acknowledges the clearance without rebuilding the descent geometry.
func TestClearedVisualReissuedDoesNotRecomputeRoute(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	f.ExpectVisualApproach("22L")
	if intent := f.ClearedVisualApproach("22L"); intent == nil {
		t.Fatal("first CVA returned nil intent")
	}
	if !f.nav.Approach.Cleared {
		t.Fatal("setup: approach not cleared after first CVA")
	}

	before := slices.Clone(f.nav.Waypoints)
	beforeAp := f.nav.Approach.Assigned

	if intent := f.ClearedVisualApproach("22L"); intent == nil {
		t.Fatal("second CVA returned nil intent")
	}

	if f.nav.Approach.Assigned != beforeAp {
		t.Error("Assigned was rebuilt by second CVA")
	}
	if len(f.nav.Waypoints) != len(before) {
		t.Errorf("Waypoints length changed: before=%d after=%d", len(before), len(f.nav.Waypoints))
	} else {
		for i := range before {
			if before[i].Fix != f.nav.Waypoints[i].Fix ||
				before[i].Location != f.nav.Waypoints[i].Location {
				t.Errorf("Waypoint[%d] mutated: before=%+v after=%+v", i, before[i], f.nav.Waypoints[i])
			}
		}
	}
}

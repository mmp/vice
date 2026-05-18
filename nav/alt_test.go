// nav/alt_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func TestAssignAltitudeDelaysVerticalGuidance(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
	})

	f.AssignAltitude(3000)
	if f.nav.Altitude.Assigned == nil || *f.nav.Altitude.Assigned != 3000 {
		t.Fatalf("expected Assigned=3000, got %v", f.nav.Altitude.Assigned)
	}
	if f.nav.Altitude.ActiveAssigned != nil {
		t.Fatalf("expected no active assigned altitude before delay, got %.0f", *f.nav.Altitude.ActiveAssigned)
	}
	if f.nav.Altitude.ActivateAt.IsZero() {
		t.Fatal("expected delayed altitude activation")
	}

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil)
	f.AssertLevelFlight()

	f.simTime = f.nav.Altitude.ActivateAt
	wxs = f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil)

	if f.nav.Altitude.ActiveAssigned == nil || *f.nav.Altitude.ActiveAssigned != 3000 {
		t.Fatalf("expected ActiveAssigned=3000 after delay, got %v", f.nav.Altitude.ActiveAssigned)
	}
	if !f.nav.Altitude.ActivateAt.IsZero() {
		t.Fatalf("expected activation time to be cleared, got %v", f.nav.Altitude.ActivateAt)
	}
	f.AssertDescending()
}

func TestAssignAltitudeKeepsPreviousActiveAltitudeDuringDelay(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
	})
	f.nav.setAssignedAltitude(5000)

	f.AssignAltitude(3000)
	target, _, _ := f.nav.TargetAltitude()
	if target != 5000 {
		t.Fatalf("expected previous active assigned altitude 5000 during delay, got %.0f", target)
	}

	f.simTime = f.nav.Altitude.ActivateAt
	f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
	target, _, _ = f.nav.TargetAltitude()
	if target != 3000 {
		t.Fatalf("expected new assigned altitude 3000 after delay, got %.0f", target)
	}
}

func TestAssignAltitudeKeepsSTARDescentDuringDelay(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
	})

	for range 300 {
		wxs := f.weather(f.nav.FlightState.Altitude)
		f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)
		if f.nav.FlightState.AltitudeRate < -50 {
			break
		}
	}
	if f.nav.FlightState.AltitudeRate >= -50 {
		t.Fatal("expected STAR descent to have started before assigning altitude")
	}

	f.AssignAltitude(3000)
	target, _, _ := f.nav.TargetAltitude()
	if target == 3000 {
		t.Fatal("expected STAR target, not pending controller altitude, during delay")
	}
	if target == f.nav.FlightState.Altitude {
		t.Fatalf("expected STAR descent to continue during delay, target %.0f current %.0f",
			target, f.nav.FlightState.Altitude)
	}
}

func TestSayAltitudeReportsPendingAssignedAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
	})

	f.AssignAltitude(3000)
	intent, ok := f.nav.SayAltitude().(av.ReportAltitudeIntent)
	if !ok {
		t.Fatalf("expected ReportAltitudeIntent, got %T", intent)
	}
	if intent.Assigned == nil || *intent.Assigned != 3000 {
		t.Fatalf("expected say altitude to report Assigned=3000, got %v", intent.Assigned)
	}
	if intent.Direction != av.AltitudeDescend {
		t.Fatalf("expected descent direction, got %v", intent.Direction)
	}
}

func TestExpediteDuringAssignedAltitudeDelay(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
	})

	f.AssignAltitude(3000)
	intent := f.nav.ExpediteDescent()
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("expected expedite to apply to pending assigned altitude, got unable: %v", intent)
	}
	if f.nav.Altitude.Rate != RateExpedite {
		t.Fatalf("expected expedite rate to be saved during delay, got %v", f.nav.Altitude.Rate)
	}

	f.simTime = f.nav.Altitude.ActivateAt.Add(time.Second)
	f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
	if f.nav.Altitude.Rate != RateExpedite {
		t.Fatalf("expected expedite rate after delayed activation, got %v", f.nav.Altitude.Rate)
	}
	f.AssertDescending()
}

// TestSTARDescentMeetsRestrictions verifies that an aircraft descending
// via a STAR meets altitude restrictions at each fix.
func TestSTARDescentMeetsRestrictions(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star LEFER/a4000/star ROSLY/a3000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertAltitudeNear(7000, 100)
	})
	f.AtFix("HAUPT", func(f *FlightTest) {
		// HAUPT is only 5.6nm from DETGY. The descent algorithm's
		// ramp-up period (~13s) consumes nearly 1nm of that distance,
		// limiting achievable precision at closely-spaced fixes.
		f.AssertAltitudeNear(6000, 200)
	})
	f.AtFix("LEFER", func(f *FlightTest) {
		f.AssertAltitudeNear(4000, 100)
	})
	f.AtFix("ROSLY", func(f *FlightTest) {
		f.AssertAltitudeNear(3000, 100)
	})

	f.Run()
}

// TestDescentPreservedOnApproachTransition verifies that a controller-
// assigned descent continues through the transition to approach mode
// (regression test for ecfa0ce7).
//
// Scenario: aircraft is descending via STAR, controller assigns "descend
// to 3000" and clears the approach. The aircraft must not level off when
// the approach waypoints are spliced in — it should continue descending.
func TestDescentPreservedOnApproachTransition(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000/star LEFER/a4000/star ROSLY/a3000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  7000,
		InitialSpeed:     210,
	})

	f.AtFix("HAUPT", func(f *FlightTest) {
		f.ExpectApproach("I22L")
		f.AssignAltitude(3000)
		f.ClearedApproach("I22L")
	})

	// Bug ecfa0ce7: after passing approach fixes, clearAltitudeForApproach
	// must preserve the assigned 3000 as a Cleared altitude so descent
	// continues through the approach transition.
	f.AtFix("LEFER", func(f *FlightTest) {
		if f.nav.Altitude.Cleared == nil || *f.nav.Altitude.Cleared != 3000 {
			t.Errorf("tick %d: expected Cleared=3000 preserved after approach transition, got %v",
				f.tick, f.nav.Altitude.Cleared)
		}
		f.AssertAltitudeBelow(3800)
		f.AssertDescending()
	})

	f.Run()
}

// TestApproachAtOrAboveDescentTarget verifies that "at or above"
// restrictions on approach waypoints are treated as descent targets,
// not as already-satisfied constraints (regression test for eb46d623).
//
// Scenario: aircraft is cleared for I22L approach at 5000 ft with NO
// assigned altitude. The approach fix ROSLY has "at or above 3000".
// Without the fix, the aircraft stays at 5000 because the restriction
// is "satisfied". With the fix, "at or above" is converted to "at 3000"
// on a cleared approach, driving descent.
func TestApproachAtOrAboveDescentTarget(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "LEFER/a4000/star ROSLY/a3000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
	})

	f.ExpectApproach("I22L")
	// No AssignAltitude — descent must be driven purely by the
	// approach "at or above" restriction being treated as a target.
	f.ClearedApproach("I22L")

	// ROSLY is on the I22L approach with "at or above 3000". With the
	// fix, it's treated as "at 3000", driving descent from 5000.
	// Without the fix, 5000 satisfies "at or above 3000" so no descent.
	calls := 0
	f.BeforeFix("ROSLY", func(f *FlightTest) {
		calls++
		if calls > 30 {
			f.AssertDescending()
		}
	})

	f.Run()
}

// TestAssignedAltitudeOverridesSTAR verifies that a controller-assigned
// altitude takes priority over STAR restrictions.
func TestAssignedAltitudeOverridesSTAR(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
	})

	// Assign 5000 immediately — should override the 10000/7000 restrictions.
	f.AssignAltitude(5000)

	// At DETGY (charted restriction 7000), the aircraft should be at
	// 5000 — the assigned altitude — not leveled at 7000.
	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertAltitudeNear(5000, 100)
	})

	f.Run()
}

func TestAltitudeTargetStopsAtQueuedHold(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT/a6000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	hold := altitudeTargetTestHold("DETGY")
	if intent := f.nav.HoldAtFix(f.callsign, "DETGY", &hold); intent != nil {
		if _, ok := intent.(av.UnableIntent); ok {
			t.Fatalf("unexpected unable intent: %v", intent)
		}
	}
	if f.nav.Heading.Hold != nil {
		t.Fatal("expected hold to be queued, not active")
	}

	if target, ok := f.nav.findAltitudeTarget(); ok {
		t.Fatalf("expected no altitude target past queued hold, got %.0f at %s", target.altitude, target.fix)
	}
}

func TestAltitudeTargetStopsAtActiveHold(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "DETGY HAUPT/a6000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	hold := altitudeTargetTestHold("DETGY")
	if intent := f.nav.HoldAtFix(f.callsign, "DETGY", &hold); intent != nil {
		if _, ok := intent.(av.UnableIntent); ok {
			t.Fatalf("unexpected unable intent: %v", intent)
		}
	}
	if f.nav.Heading.Hold == nil {
		t.Fatal("expected active hold")
	}

	if target, ok := f.nav.findAltitudeTarget(); ok {
		t.Fatalf("expected no altitude target past active hold, got %.0f at %s", target.altitude, target.fix)
	}
}

func TestAltitudeTargetIncludesHoldingFixRestriction(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "DETGY/a8000 HAUPT/a6000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	hold := altitudeTargetTestHold("DETGY")
	if intent := f.nav.HoldAtFix(f.callsign, "DETGY", &hold); intent != nil {
		if _, ok := intent.(av.UnableIntent); ok {
			t.Fatalf("unexpected unable intent: %v", intent)
		}
	}

	target, ok := f.nav.findAltitudeTarget()
	if !ok {
		t.Fatal("expected altitude target at holding fix")
	}
	if target.fix != "DETGY" || target.altitude != 8000 {
		t.Fatalf("expected target 8000 at DETGY, got %.0f at %s", target.altitude, target.fix)
	}
}

func altitudeTargetTestHold(fix string) av.Hold {
	return av.Hold{
		Fix:           fix,
		InboundCourse: 180,
		TurnDirection: av.TurnRight,
		LegMinutes:    1,
	}
}

// TestCrossFixAtAltitude verifies that "cross fix at altitude"
// assignments are respected when the restriction differs from charted.
func TestCrossFixAtAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	// "Cross DETGY at 8000" — controller raises the charted 7000
	// restriction to 8000. The aircraft must level at 8000, not
	// descend through to the charted 7000.
	ar := av.MakeAtAltitudeRestriction(8000)
	f.nav.CrossFixAt("DETGY", &ar, nil)

	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertAltitudeNear(8000, 100)
	})

	f.Run()
}

// TestCrossDistanceFromFixAtAltitude verifies that "cross N miles dir of fix
// at altitude" causes the aircraft to descend to the correct altitude at
// the synthetic waypoint position.
func TestCrossDistanceFromFixAtAltitude(t *testing.T) {
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
	approachHeading := math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude)
	approachDir := math.ShortCompass(approachHeading)
	dir, err := math.ParseCardinalOrdinalDirection(approachDir)
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	// "Cross 5 miles [dir] of DETGY at 8000"
	ar := av.MakeAtAltitudeRestriction(8000)
	intent := f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	// The synthetic waypoint name starts with underscore.
	syntheticFix := f.nav.Waypoints[1].Fix
	f.AtFix(syntheticFix, func(f *FlightTest) {
		f.AssertAltitudeNear(8000, 150)
	})

	f.Run()
}

func TestCrossDistanceFromApproachFixAtAltitudeBeforeClearance(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "B763",
		InitialAltitude:  12000,
		InitialSpeed:     250,
	})

	f.ExpectApproach("I22L")
	f.DirectFix("ROSLY")

	assignedWps := f.nav.AssignedWaypoints()
	roslyIdx := slices.IndexFunc(assignedWps, func(wp av.Waypoint) bool { return wp.Fix == "ROSLY" })
	if roslyIdx == -1 {
		t.Fatalf("expected ROSLY in route, got %v", av.WaypointArray(assignedWps).Encode())
	}
	fixLoc := assignedWps[roslyIdx].Location
	priorLoc := f.nav.FlightState.Position
	if roslyIdx > 0 {
		priorLoc = assignedWps[roslyIdx-1].Location
	}
	approachHeading := math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(3000)
	intent := f.nav.CrossDistanceFromFixAt("ROSLY", 5, dir, &ar, nil)
	if _, ok := intent.(av.UnableIntent); ok {
		t.Fatalf("unexpected unable: %v", intent)
	}

	f.AfterTicks(30, func(f *FlightTest) {
		if f.nav.FlightState.Altitude >= 12000 {
			f.t.Fatalf("expected aircraft to start descending for controller crossing restriction; altitude %.0f", f.nav.FlightState.Altitude)
		}
	})

	f.Run()
}

// TestDescendViaSTAR verifies that DescendViaSTAR clears assigned
// altitude and lets the aircraft follow charted restrictions.
func TestDescendViaSTAR(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  11000,
		InitialSpeed:     250,
		AssignedAltitude: 11000, // held at 11000
	})

	// Verify the aircraft is held at 11000 before DVS.
	f.AfterTicks(30, func(f *FlightTest) {
		f.AssertAltitudeNear(11000, 50)
		f.nav.DescendViaSTAR(f.simTime)
	})

	// After DVS, aircraft should descend to meet DETGY's 7000 restriction.
	f.AtFix("DETGY", func(f *FlightTest) {
		f.AssertAltitudeNear(7000, 100)
	})

	f.Run()
}

// TestDescentContinuesAfterMissedRestriction verifies that an aircraft
// continues descending after missing an altitude restriction at a fix,
// rather than leveling off (regression test for 3c74afba).
//
// Scenario: aircraft starts at DETGY at 7500 (500ft above the 7000
// restriction) and immediately passes it. The next fix (HAUPT at 6500)
// is close but the altitude difference is small enough that geometric
// descent hasn't started ("Not time yet"). The carried restriction from
// DETGY should drive descent toward 7000 in the interim.
func TestDescentContinuesAfterMissedRestriction(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		// HAUPT's restriction is set to 6500 (not the charted 6000) so
		// the geometric rate from 7500 to 6500 stays below the "start
		// descent" threshold, exercising the carried-restriction fallback.
		Waypoints:        "DETGY/a7000/star HAUPT/a6500/star LEFER/a4000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  7500,
		InitialSpeed:     180,
	})

	// Aircraft passes DETGY immediately at 7500 (missing the 7000
	// restriction). The geometric descent to HAUPT/6500 hasn't started
	// yet. Without the fix, the aircraft levels at 7500. With the fix,
	// the carried restriction from DETGY drives descent toward 7000.
	calls := 0
	f.BetweenFixes("DETGY", "HAUPT", func(f *FlightTest) {
		calls++
		if calls > 20 {
			f.AssertDescending()
		}
	})

	f.AtFix("HAUPT", func(f *FlightTest) {})
	f.Run()
}

// TestSpeedAssignmentPausesDescentWhenLarge verifies that a large speed
// change (>20kt) during descent causes the aircraft to pause descent
// until speed is achieved (regression test for 756909b6).
func TestSpeedAssignmentPausesDescentWhenLarge(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignAltitude(3000)

	assignedSpeed := false
	assignedTick := -1
	f.BeforeFix("HAUPT", func(f *FlightTest) {
		if assignedSpeed {
			return
		}
		if f.nav.Altitude.ActiveAssigned == nil {
			return
		}
		f.AssertDescending()
		f.AssignSpeed(180)
		assignedSpeed = true
		assignedTick = f.tick
	})

	// Shortly after the speed assignment, descent should be paused
	f.BeforeFix("HAUPT", func(f *FlightTest) {
		if !assignedSpeed || f.tick == assignedTick {
			return
		}
		if f.nav.Altitude.AfterSpeed == nil {
			return
		}
		f.AssertNotDescending()
	})

	f.AtFix("HAUPT", func(f *FlightTest) {
		// Eventually the aircraft should be descending again
		f.AssertAltitudeBelow(8000)
	})

	f.Run()
}

func TestAltitudeAfterSpeedDelaysAfterSpeedReached(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL DETGY HAUPT",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
	})
	f.AssignSpeed(180)
	intent := f.nav.AssignAltitude(3000, true, f.simTime, 0)
	if altIntent, ok := intent.(av.AltitudeIntent); !ok || altIntent.AfterSpeed == nil || *altIntent.AfterSpeed != 180 {
		t.Fatalf("expected altitude after speed intent, got %T: %v", intent, intent)
	}

	for range 300 {
		wxs := f.weather(f.nav.FlightState.Altitude)
		f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)
		if f.nav.Altitude.AfterSpeed == nil {
			break
		}
	}
	if f.nav.Altitude.AfterSpeed != nil {
		t.Fatal("expected speed condition to schedule altitude assignment")
	}
	if f.nav.Altitude.Assigned == nil || *f.nav.Altitude.Assigned != 3000 {
		t.Fatalf("expected Assigned=3000 after speed reached, got %v", f.nav.Altitude.Assigned)
	}
	if f.nav.Altitude.ActiveAssigned != nil {
		t.Fatalf("expected altitude to remain inactive after speed reached, got %.0f", *f.nav.Altitude.ActiveAssigned)
	}
	if f.nav.Altitude.ActivateAt.IsZero() {
		t.Fatal("expected delayed activation after speed reached")
	}
	f.AssertLevelFlight()

	f.simTime = f.nav.Altitude.ActivateAt.Add(time.Second)
	f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
	if f.nav.Altitude.ActiveAssigned == nil || *f.nav.Altitude.ActiveAssigned != 3000 {
		t.Fatalf("expected ActiveAssigned=3000 after delayed activation, got %v", f.nav.Altitude.ActiveAssigned)
	}
	f.AssertDescending()
}

// TestSmallSpeedChangeDuringDescentContinues verifies that a small speed
// change (<=20kt) during descent does NOT pause the descent.
func TestSmallSpeedChangeDuringDescentContinues(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  8000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignAltitude(3000)

	f.AfterTicks(10, func(f *FlightTest) {
		f.AssertDescending()
		// Small speed change — only 10kt delta
		f.AssignSpeed(240)
	})

	f.AfterTicks(12, func(f *FlightTest) {
		if f.nav.Altitude.AfterSpeed != nil {
			t.Errorf("tick %d: AfterSpeed should NOT be set for small speed change", f.tick)
		}
	})

	f.AtFix("DETGY", func(f *FlightTest) {
		// Descent should have continued
		f.AssertAltitudeBelow(7500)
	})

	f.Run()
}

// TestExpediteDescentThroughAltitude verifies that ExpediteDescentThrough
// applies RateExpedite above the "through" altitude and reverts to
// RateNormal below it (regression test for 3888830d).
func TestExpediteDescentThroughAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	f.AssignAltitude(3000)
	f.ExpediteDescentThrough(7000)

	// Above 7000: rate should be expedite
	f.AfterTicks(20, func(f *FlightTest) {
		if f.nav.FlightState.Altitude > 7000 {
			if f.nav.Altitude.Rate != RateExpedite {
				t.Errorf("tick %d: expected RateExpedite above 7000, got %d (alt=%.0f)",
					f.tick, f.nav.Altitude.Rate, f.nav.FlightState.Altitude)
			}
		}
	})

	// Check multiple ticks: after passing through 7000 but still above
	// the target (3000), rate should have reverted to RateNormal.
	// Without the fix, rate stays RateExpedite all the way down.
	for tick := 40; tick <= 150; tick += 5 {
		tickCopy := tick
		f.AfterTicks(tickCopy, func(f *FlightTest) {
			alt := f.nav.FlightState.Altitude
			if alt < 6800 && alt > 3500 {
				if f.nav.Altitude.Rate != RateNormal {
					t.Errorf("tick %d: expected RateNormal below 7000 (alt=%.0f), got %d",
						f.tick, alt, f.nav.Altitude.Rate)
				}
			}
		})
	}

	f.AtFix("DETGY", func(f *FlightTest) {
		// Just reach the fix
	})

	f.Run()
}

// TestGoodRateDescentFasterThanNormal verifies that GoodRateDescent
// causes more altitude loss per tick than normal descent.
func TestGoodRateDescentFasterThanNormal(t *testing.T) {
	makeTestFlight := func() *FlightTest {
		return NewArrivalFlight(t, ArrivalConfig{
			Waypoints:        "SAJUL/star DETGY/star HAUPT/star LEFER/star",
			DepartureAirport: "KMCO",
			ArrivalAirport:   "KJFK",
			AircraftType:     "A320",
			InitialAltitude:  10000,
			InitialSpeed:     250,
			OnSTAR:           true,
		})
	}

	runForTicks := func(f *FlightTest, ticks int) {
		for range ticks {
			wxs := f.weather(f.nav.FlightState.Altitude)
			f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil)
			f.simTime = f.simTime.Add(1e9)
		}
	}

	// Normal rate
	fNormal := makeTestFlight()
	fNormal.nav.AssignAltitude(3000, false, fNormal.simTime, 0)
	runForTicks(fNormal, 120)
	normalAlt := fNormal.nav.FlightState.Altitude

	// Good rate
	fGood := makeTestFlight()
	fGood.nav.AssignAltitude(3000, false, fGood.simTime, 0)
	fGood.nav.GoodRateDescent()
	runForTicks(fGood, 120)
	goodAlt := fGood.nav.FlightState.Altitude

	// Good rate should have descended more (lower altitude)
	if goodAlt >= normalAlt {
		t.Errorf("good rate alt %.0f should be lower than normal rate alt %.0f", goodAlt, normalAlt)
	}
}

// TestAtmosClimbFactorNoNaN verifies that atmosClimbFactor does not
// return NaN or Inf at high altitudes (regression test for b51ae87d).
func TestAtmosClimbFactorNoNaN(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/star DETGY/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "B738",
		InitialAltitude:  35000,
		InitialSpeed:     250,
		OnSTAR:           true,
	})

	for _, alt := range []float32{35000, 40000, 45000} {
		f.nav.FlightState.Altitude = alt
		wxs := wx.MakeStandardSampleForAltitude(alt)
		factor := f.nav.atmosClimbFactor(wxs)

		if factor != factor { // NaN check
			t.Errorf("atmosClimbFactor(alt=%.0f) returned NaN", alt)
		}
		if factor < 0.5 || factor > 1.0 {
			t.Errorf("atmosClimbFactor(alt=%.0f) = %.3f, expected [0.5, 1.0]", alt, factor)
		}
	}
}

// TestCrossDMEAtAltitude verifies that a "cross N DME at altitude" restriction
// on a cleared visual approach drives descent to that altitude by the time
// the aircraft reaches the synthetic waypoint.
func TestCrossDMEAtAltitude(t *testing.T) {
	f := setupClearedVisual(t, "22L")
	f.nav.FlightState.Altitude = 4000

	ar := av.MakeAtAltitudeRestriction(3000)
	if _, ok := f.nav.CrossDMEAt(5, &ar, nil).(av.UnableIntent); ok {
		t.Fatalf("unexpected unable")
	}

	f.AtFix("_22L_5DME", func(f *FlightTest) {
		f.AssertAltitudeNear(3000, 150)
	})

	f.Run()
}

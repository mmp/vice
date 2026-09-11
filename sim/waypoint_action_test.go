// sim/waypoint_action_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
)

// TestClearApproachActionJoinsAtItsFix verifies that the sim hands a
// /clearapp action the fix it fired at. nav drops that fix from the route
// before the sim runs the action, so on an arrival that ends there it is all
// that connects the aircraft to the approach; without it the aircraft carries
// on to the airport at its current altitude.
func TestClearApproachActionJoinsAtItsFix(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	s.STARSComputer = makeSTARSComputer("TEST")

	ac := MakeTestAircraft("AAL123", "13L")
	s.Aircraft[ac.ADSBCallsign] = ac

	iaf := av.Waypoint{Fix: "IAFXX", Location: [2]float32{0, 4.0 / 60}}
	faf := av.Waypoint{Fix: "FAFXX", Location: [2]float32{0, 2.0 / 60}}
	ac.Nav.Approach.Assigned.Waypoints = []av.WaypointArray{{iaf, faf}}
	// The arrival ended at IAFXX, which nav has just passed and removed.
	ac.Nav.Waypoints = []av.Waypoint{ac.Nav.FlightState.ArrivalAirport}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{
		Waypoint: iaf,
		Actions:  av.WaypointActions{ClearApproach: true},
	})

	if !ac.Nav.Approach.Cleared {
		t.Fatal("the aircraft was not cleared for the approach")
	}
	if ac.Nav.Waypoints[0].Fix != faf.Fix {
		fixes := make([]string, len(ac.Nav.Waypoints))
		for i, wp := range ac.Nav.Waypoints {
			fixes[i] = wp.Fix
		}
		t.Errorf("expected the approach to pick up after %s at %s, got %v", iaf.Fix, faf.Fix, fixes)
	}
}

// TestRemovalActionsApplyToHumanControlledAircraft checks that /delete and
// /land take effect however the aircraft is being worked: unlike the handoffs
// and scratchpad settings alongside them, they are not instructions a virtual
// controller issues, so a human's aircraft is removed too.
func TestRemovalActionsApplyToHumanControlledAircraft(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())

	for _, tc := range []struct {
		name     string
		actions  av.WaypointActions
		altitude float32
		want     bool // the aircraft should be gone
	}{
		{name: "delete", actions: av.WaypointActions{Delete: true}, altitude: 3000, want: true},
		{name: "land", actions: av.WaypointActions{Land: true}, altitude: 100, want: true},
		{name: "land too high", actions: av.WaypointActions{Land: true}, altitude: 3000, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(lg)
			s.STARSComputer = makeSTARSComputer("TEST")

			ac := MakeTestAircraft("AAL123", "13L")
			ac.Nav.FlightState.Altitude = tc.altitude
			s.Aircraft[ac.ADSBCallsign] = ac

			threshold := av.Waypoint{Fix: "_13L_THRESHOLD"}
			threshold.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(100))

			s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Waypoint: threshold, Actions: tc.actions})

			if _, ok := s.Aircraft[ac.ADSBCallsign]; ok == tc.want {
				t.Errorf("aircraft present = %v, want %v", ok, !tc.want)
			}
		})
	}
}

// TestScriptedCommandsLeaveRollbackHistoryAlone checks that waypoint commands
// do not displace what the controller at the TCW last transmitted: a rollback
// must still undo the controller's own instruction, not the scenario's.
func TestScriptedCommandsLeaveRollbackHistoryAlone(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	for _, cs := range []av.ADSBCallsign{"AAL111", "AAL222"} {
		s.Aircraft[cs] = MakeTestAircraft(cs, "22L")
	}

	if res := s.RunAircraftControlCommands(E2ETCW(), "AAL111", "L010", 0); res.Error != nil {
		t.Fatal(res.Error)
	}
	if res := s.RunScriptedControlCommands(E2ETCW(), "AAL222", "L040"); res.Error != nil {
		t.Fatal(res.Error)
	}
	s.RunAircraftControlCommands(E2ETCW(), "AAL111", "ROLLBACK", 0)

	if _, ok := s.Aircraft["AAL111"].Nav.AssignedHeading(); ok {
		t.Error("rollback did not undo the controller's own transmission")
	}
	if hdg, ok := s.Aircraft["AAL222"].Nav.AssignedHeading(); !ok || hdg != 40 {
		t.Errorf("rollback undid the scripted command: heading = %v (ok=%v), want 40", hdg, ok)
	}
}

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
		Fix:     iaf.Fix,
		Actions: av.WaypointActions{ClearApproach: true},
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

// sim/update_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// TestVirtualControllerAltitudeEntries checks that the altitudes a virtual
// controller assigns along a route show up in the data block the way the
// entries that controller would have made do: a climb that stops short of the
// aircraft's altitude is an interim altitude and everything else amends the
// assigned altitude.
func TestVirtualControllerAltitudeEntries(t *testing.T) {
	const cruise = 35000

	sidWaypoint := func() av.Waypoint {
		wp := av.Waypoint{Fix: "MERIT"}
		wp.SetOnSID(true)
		return wp
	}
	starWaypoint := func(restriction float32) av.Waypoint {
		wp := av.Waypoint{Fix: "LENDY"}
		wp.SetOnSTAR(true)
		if restriction != 0 {
			wp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(restriction))
		}
		return wp
	}
	approachWaypoint := av.Waypoint{Fix: "APP"}
	approachWaypoint.SetOnApproach(true)
	approachWaypoint.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(3000))

	for _, tc := range []struct {
		name             string
		facility         string
		humanControlled  bool
		approachCleared  bool
		waypoints        []av.Waypoint
		actions          av.WaypointActions
		assignedAltitude int
		interimAltitude  int
	}{
		{
			name:             "climb short of the assigned altitude is interim",
			actions:          av.WaypointActions{ClimbAltitude: 17000},
			assignedAltitude: cruise,
			interimAltitude:  17000,
		},
		{
			name:             "climb to the assigned altitude clears the interim",
			actions:          av.WaypointActions{ClimbAltitude: cruise},
			assignedAltitude: cruise,
		},
		{
			name:             "descent amends the assigned altitude",
			actions:          av.WaypointActions{DescendAltitude: 24000},
			assignedAltitude: 24000,
		},
		{
			name:             "climb via the SID except an altitude is interim",
			waypoints:        []av.Waypoint{sidWaypoint()},
			actions:          av.WaypointActions{ClimbViaSID: true, ExceptAltitude: 23000},
			assignedAltitude: cruise,
			interimAltitude:  23000,
		},
		{
			name:             "climb via the SID goes to the filed altitude",
			waypoints:        []av.Waypoint{sidWaypoint()},
			actions:          av.WaypointActions{ClimbViaSID: true},
			assignedAltitude: cruise,
		},
		{
			name:             "descend via the STAR takes the procedure's bottom",
			waypoints:        []av.Waypoint{starWaypoint(11000)},
			actions:          av.WaypointActions{DescendViaSTAR: true},
			assignedAltitude: 11000,
		},
		{
			name:             "descend via ignores an uncleared approach",
			waypoints:        []av.Waypoint{starWaypoint(11000), approachWaypoint},
			actions:          av.WaypointActions{DescendViaSTAR: true},
			assignedAltitude: 11000,
		},
		{
			name:             "descend via includes a cleared approach",
			waypoints:        []av.Waypoint{starWaypoint(11000), approachWaypoint},
			approachCleared:  true,
			actions:          av.WaypointActions{DescendViaSTAR: true},
			assignedAltitude: 3000,
		},
		{
			name:             "descend via with no applicable restrictions preserves the entry",
			waypoints:        []av.Waypoint{starWaypoint(0), approachWaypoint},
			actions:          av.WaypointActions{DescendViaSTAR: true},
			assignedAltitude: cruise,
		},
		{
			name:             "descend via the STAR except an altitude amends it",
			waypoints:        []av.Waypoint{starWaypoint(11000)},
			actions:          av.WaypointActions{DescendViaSTAR: true, ExceptAltitude: 19000},
			assignedAltitude: 19000,
		},
		{
			name:             "a via with no procedure ahead changes nothing",
			actions:          av.WaypointActions{ClimbViaSID: true, ExceptAltitude: 23000},
			assignedAltitude: cruise,
		},
		{
			name:             "an aircraft a human is working is left alone",
			humanControlled:  true,
			actions:          av.WaypointActions{DescendAltitude: 24000},
			assignedAltitude: cruise,
		},
		{
			name:             "STARS leaves the fields to the controller",
			facility:         "N90",
			actions:          av.WaypointActions{DescendAltitude: 24000},
			assignedAltitude: cruise,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(testLogger())
			s.State.Facility = "ZNY"
			if tc.facility != "" {
				s.State.Facility = tc.facility
			}
			s.ScenarioDefaultConsolidation = PositionConsolidation{TCP("2A"): nil}

			ac := MakeTestAircraft("AAL123", "13L")
			ac.Nav.Perf.Ceiling = 41000
			ac.Nav.FlightState.Altitude = cruise
			ac.Nav.Waypoints = tc.waypoints
			ac.Nav.Approach.Cleared = tc.approachCleared
			ac.FlightPlan.Altitude = cruise
			ac.ControllerFrequency = util.Select(tc.humanControlled, ControlPosition("2A"), ControlPosition(""))
			ac.NASFlightPlan = &NASFlightPlan{ACID: "AAL123", AssignedAltitude: cruise}
			s.Aircraft[ac.ADSBCallsign] = ac

			s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: tc.actions})

			if got := ac.NASFlightPlan.AssignedAltitude; got != tc.assignedAltitude {
				t.Errorf("assigned altitude: got %d, expected %d", got, tc.assignedAltitude)
			}
			if got := ac.NASFlightPlan.InterimAlt; got != tc.interimAltitude {
				t.Errorf("interim altitude: got %d, expected %d", got, tc.interimAltitude)
			}
			if got := ac.NASFlightPlan.InterimType; got != InterimNormal {
				t.Errorf("interim type: got %s, expected T", got)
			}
		})
	}
}

// TestVirtualControllerInterimAltitudeCleared checks that an interim altitude
// a virtual controller entered earlier doesn't outlive the clearance that
// replaced it.
func TestVirtualControllerInterimAltitudeCleared(t *testing.T) {
	s := NewTestSim(testLogger())
	s.State.Facility = "ZNY"
	s.ScenarioDefaultConsolidation = PositionConsolidation{TCP("2A"): nil}

	ac := MakeTestAircraft("AAL123", "13L")
	ac.Nav.Perf.Ceiling = 41000
	ac.FlightPlan.Altitude = 35000
	ac.ControllerFrequency = ""
	ac.NASFlightPlan = &NASFlightPlan{ACID: "AAL123", AssignedAltitude: 35000}
	s.Aircraft[ac.ADSBCallsign] = ac

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{ClimbAltitude: 17000}})
	if ac.NASFlightPlan.InterimAlt != 17000 {
		t.Fatalf("expected an interim altitude of 17000, got %d", ac.NASFlightPlan.InterimAlt)
	}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{ClimbAltitude: 35000}})
	if ac.NASFlightPlan.InterimAlt != 0 {
		t.Errorf("the interim altitude outlived the climb to the assigned altitude: %d",
			ac.NASFlightPlan.InterimAlt)
	}
}

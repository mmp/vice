// client/state_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"testing"

	"github.com/mmp/vice/sim"
)

func TestGetInitialAltitudeLimits(t *testing.T) {
	unrestricted := sim.UnrestrictedAltitudeLimits

	// makeState returns the state for a user signed in to sector 08, with the
	// given limits adapted for that position and for the scenario.
	makeState := func(position, scenario sim.AltitudeLimits) SimState {
		var ss SimState
		ss.UserTCW = "08"
		ss.ScenarioAltitudeLimits = scenario
		ss.FacilityAdaptation.Controllers = map[sim.ControlPosition]*sim.STARSController{
			"08": {AltitudeLimits: position},
		}
		return ss
	}

	for _, c := range []struct {
		what               string
		position, scenario sim.AltitudeLimits
		targets, ldbs      [2]int
	}{
		{"nothing adapted", sim.AltitudeLimits{}, sim.AltitudeLimits{}, unrestricted, unrestricted},
		{"scenario only", sim.AltitudeLimits{}, sim.AltitudeLimits{Combined: [2]int{0, 230}},
			[2]int{0, 230}, [2]int{0, 230}},
		{"position only", sim.AltitudeLimits{Combined: [2]int{240, 999}}, sim.AltitudeLimits{},
			[2]int{240, 999}, [2]int{240, 999}},
		// The position wins outright: the scenario's LDB filter is not mixed
		// into a position that adapts only its target filter.
		{"position wins", sim.AltitudeLimits{Targets: [2]int{240, 999}},
			sim.AltitudeLimits{LDBs: [2]int{50, 180}}, [2]int{240, 999}, unrestricted},
	} {
		ss := makeState(c.position, c.scenario)
		targets, ldbs := ss.GetInitialAltitudeLimits()
		if targets != c.targets || ldbs != c.ldbs {
			t.Errorf("%s: got targets %v, LDBs %v; expected %v and %v", c.what, targets, ldbs,
				c.targets, c.ldbs)
		}
	}

	// A position with no adaptation of its own falls through to the scenario.
	var ss SimState
	ss.UserTCW = "09"
	ss.ScenarioAltitudeLimits = sim.AltitudeLimits{LDBs: [2]int{50, 180}}
	if targets, ldbs := ss.GetInitialAltitudeLimits(); targets != unrestricted || ldbs != [2]int{50, 180} {
		t.Errorf("unadapted position: got targets %v, LDBs %v", targets, ldbs)
	}
}

// sim/flightplan_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
)

// Each kind of assigned altitude replaces whatever kind the flight had, and
// the data block shows no single altitude for VFR and VFR-on-top ones.
func TestFlightPlanUpdateReplacesAssignedAltitude(t *testing.T) {
	s := NewTestSim(testLogger())
	fp := &FlightPlan{AltitudeBlock: [2]int{20000, 25000}}

	var vfr FlightPlanSpecifier
	vfr.AssignedAltitude.Set(10500)
	vfr.AssignedAltitudeQualifier.Set(VFRAltitude)
	fp.Update(vfr, s)
	if fp.HasAltitudeBlock() || fp.AssignedAltitudeQualifier != VFRAltitude || fp.DataBlockAltitude() != 0 {
		t.Errorf("VFR/105: got %+v, data block altitude %d", fp, fp.DataBlockAltitude())
	}

	var hard FlightPlanSpecifier
	hard.AssignedAltitude.Set(30000)
	fp.Update(hard, s)
	if fp.AssignedAltitudeQualifier != PlainAltitude || fp.DataBlockAltitude() != 30000 {
		t.Errorf("hard altitude: got qualifier %v, data block altitude %d", fp.AssignedAltitudeQualifier,
			fp.DataBlockAltitude())
	}

	var block FlightPlanSpecifier
	block.AltitudeBlock.Set([2]int{20000, 25000})
	fp.Update(block, s)
	if fp.AssignedAltitude != 0 || fp.DataBlockAltitude() != 0 {
		t.Errorf("block: got altitude %d, data block altitude %d", fp.AssignedAltitude, fp.DataBlockAltitude())
	}
}

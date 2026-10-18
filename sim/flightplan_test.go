// sim/flightplan_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

// Amending a flight plan's beacon code gives its old code back to the pool
// and takes the new one out of it, so the pool can't hand the new code to
// another flight. A code another flight plan has is refused.
func TestModifyFlightPlanAmendsSquawk(t *testing.T) {
	s := NewTestSim(testLogger())
	s.LocalCodePool = av.MakeLocalSquawkCodePool(av.LocalSquawkCodePoolSpecifier{})
	s.ERAMComputer = makeERAMComputer("ZNY", s.LocalCodePool)
	s.STARSComputer = makeSTARSComputer("N90")
	pool := s.ERAMComputer.SquawkCodePool

	addFlight := func(acid ACID, sq av.Squawk) *FlightPlan {
		if err := pool.Take(sq); err != nil {
			t.Fatalf("%s: %v", acid, err)
		}
		ac := MakeTestAircraft(av.ADSBCallsign(acid), "22L")
		fp := &FlightPlan{ACID: acid, PlanType: RemoteEnroute, AssignedSquawk: sq, TrackingController: "125.0"}
		ac.AssociateFlightPlan(fp)
		s.Aircraft[ac.ADSBCallsign] = ac
		return fp
	}
	aal := addFlight("AAL123", 0o2301)
	addFlight("JBU456", 0o2302)

	var spec FlightPlanSpecifier
	spec.SquawkAssignment.Set("3301")
	if err := s.ModifyFlightPlan("TEST", "AAL123", spec); err != nil {
		t.Fatalf("ModifyFlightPlan: %v", err)
	}
	if aal.AssignedSquawk != 0o3301 {
		t.Errorf("assigned squawk %s, want 3301", aal.AssignedSquawk)
	}
	if pool.IsAssigned(0o2301) {
		t.Error("previous code 2301 was not returned to the pool")
	}
	if !pool.IsAssigned(0o3301) {
		t.Error("new code 3301 was not taken from the pool")
	}

	spec.SquawkAssignment.Set("2302")
	if err := s.ModifyFlightPlan("TEST", "AAL123", spec); err != ErrDuplicateBeacon {
		t.Errorf("JBU456's code: got %v, want %v", err, ErrDuplicateBeacon)
	}
	if aal.AssignedSquawk != 0o3301 {
		t.Errorf("refused amendment changed the code to %s", aal.AssignedSquawk)
	}
}

// Deleting a flight plan's beacon code gives it back to the pool.
func TestModifyFlightPlanDeletesSquawk(t *testing.T) {
	s := NewTestSim(testLogger())
	s.LocalCodePool = av.MakeLocalSquawkCodePool(av.LocalSquawkCodePoolSpecifier{})
	s.ERAMComputer = makeERAMComputer("ZNY", s.LocalCodePool)
	pool := s.ERAMComputer.SquawkCodePool
	if err := pool.Take(0o2301); err != nil {
		t.Fatal(err)
	}
	ac := MakeTestAircraft("AAL123", "22L")
	fp := &FlightPlan{ACID: "AAL123", PlanType: RemoteEnroute, AssignedSquawk: 0o2301}
	ac.AssociateFlightPlan(fp)
	s.Aircraft[ac.ADSBCallsign] = ac

	var spec FlightPlanSpecifier
	spec.SquawkAssignment.Set("")
	if err := s.ModifyFlightPlan("TEST", "AAL123", spec); err != nil {
		t.Fatalf("ModifyFlightPlan: %v", err)
	}
	if fp.AssignedSquawk != 0 {
		t.Errorf("assigned squawk %s, want none", fp.AssignedSquawk)
	}
	if pool.IsAssigned(0o2301) {
		t.Error("deleted code 2301 was not returned to the pool")
	}
}

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

// An entry sets a scratchpad even to the text it already has, which an ERAM
// entry of the current heading or speed does; only STARS undoes a re-entered
// scratchpad, and its client does that.
func TestFlightPlanUpdateSetsScratchpads(t *testing.T) {
	s := NewTestSim(testLogger())
	fp := &FlightPlan{Scratchpad: "240", SecondaryScratchpad: "S250"}

	var spec FlightPlanSpecifier
	spec.Scratchpad.Set("250")
	spec.SecondaryScratchpad.Set("S250")
	for range 2 {
		fp.Update(spec, s)
		if fp.Scratchpad != "250" || fp.PriorScratchpad != "240" || fp.SecondaryScratchpad != "S250" {
			t.Errorf("got scratchpads %q (prior %q) and %q, want 250 (prior 240) and S250",
				fp.Scratchpad, fp.PriorScratchpad, fp.SecondaryScratchpad)
		}
	}
}

// Associating a new flight plan with a track applies the entry once: the
// scratchpads stay as entered, one beacon code leaves the pool, and the
// tracking controller's workstation owns the track.
func TestAssociateFlightPlanAppliesSpecOnce(t *testing.T) {
	s := NewTestSim(testLogger())
	s.STARSComputer = makeSTARSComputer("TEST")
	s.LocalCodePool = av.MakeLocalSquawkCodePool(av.LocalSquawkCodePoolSpecifier{})
	s.ERAMComputer = &ERAMComputer{SquawkCodePool: av.MakeEnrouteSquawkCodePool(s.LocalCodePool)}
	s.State.Controllers = map[ControlPosition]*av.Controller{"125.0": {Position: "125.0"}}
	ac := MakeTestAircraft("N123AB", "13L")
	s.Aircraft[ac.ADSBCallsign] = ac

	var spec FlightPlanSpecifier
	spec.ACID.Set("N123AB")
	spec.Scratchpad.Set("ABC")
	spec.SecondaryScratchpad.Set("22L")
	spec.TypeOfFlight.Set(av.FlightTypeArrival)
	spec.TrackingController.Set("125.0")
	spec.SquawkAssignment.Set("+")
	if err := s.AssociateFlightPlan("TEST", ac.ADSBCallsign, spec); err != nil {
		t.Fatalf("AssociateFlightPlan: %v", err)
	}

	fp := ac.FlightPlan
	if fp.Scratchpad != "ABC" || fp.SecondaryScratchpad != "22L" {
		t.Errorf("got scratchpads %q and %q, want ABC and 22L", fp.Scratchpad, fp.SecondaryScratchpad)
	}
	if fp.OwningTCW != "TEST" {
		t.Errorf("got owner %q, want TEST", fp.OwningTCW)
	}
	var assigned []av.Squawk
	for sq := av.Squawk(0); sq <= 0o7777; sq++ {
		if s.LocalCodePool.IsAssigned(sq) {
			assigned = append(assigned, sq)
		}
	}
	if len(assigned) != 1 || assigned[0] != fp.AssignedSquawk {
		t.Errorf("codes taken from the pool: %v, want only the flight plan's %s", assigned, fp.AssignedSquawk)
	}
}

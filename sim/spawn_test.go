package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
)

// makeTestSim creates a minimal Sim with the infrastructure needed for
// addAircraftNoLock and deleteAircraft (squawk pool, STARS computer, etc.).
func makeTestSim() *Sim {
	sc := makeSTARSComputer("test")
	ec := &ERAMComputer{
		SquawkCodePool: av.MakeEnrouteSquawkCodePool(nil),
	}
	lg := log.New(false, "debug", "")
	return &Sim{
		Aircraft:          make(map[av.ADSBCallsign]*Aircraft),
		STARSComputer:     sc,
		ERAMComputer:      ec,
		SquawkWarnedACIDs: make(map[ACID]any),
		State:             &CommonState{},
		lg:                lg,
		Rand:              vrand.Make(),
	}
}

func makeTestBenchAircraft(callsign string) Aircraft {
	return Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		TypeOfFlight: av.FlightTypeArrival,
		Mode:         av.TransponderModeAltitude,
		FlightPlan: av.FlightPlan{
			ArrivalAirport: "KJFK",
			AircraftType:   "B738",
			Rules:          av.FlightRulesIFR,
		},
		Nav: nav.Nav{
			Rand: vrand.Make(),
		},
		NASFlightPlan: &NASFlightPlan{
			ACID:           ACID(callsign),
			ArrivalAirport: "KJFK",
			Rules:          av.FlightRulesIFR,
			TypeOfFlight:   av.FlightTypeArrival,
			AircraftCount:  1,
			AircraftType:   "B738",
		},
	}
}

func TestAddAndDeleteTestBenchAircraft(t *testing.T) {
	s := makeTestSim()
	initialAvailable := len(s.STARSComputer.AvailableIndices)

	// Spawn a test bench aircraft (has NASFlightPlan with no squawk).
	ac := makeTestBenchAircraft("AAL100")
	s.addAircraftNoLock(ac)

	// Verify squawk and list index were assigned.
	serverAc := s.Aircraft[av.ADSBCallsign("AAL100")]
	if serverAc.Squawk == av.Squawk(0) {
		t.Error("squawk not assigned")
	}
	if serverAc.NASFlightPlan.ListIndex == UnsetSTARSListIndex {
		t.Error("list index not assigned")
	}
	if len(s.STARSComputer.AvailableIndices) != initialAvailable-1 {
		t.Errorf("available indices: got %d, want %d", len(s.STARSComputer.AvailableIndices), initialAvailable-1)
	}
	// Flight plan should NOT be in STARSComputer.FlightPlans (it's associated).
	if len(s.STARSComputer.FlightPlans) != 0 {
		t.Errorf("unassociated flight plans: got %d, want 0", len(s.STARSComputer.FlightPlans))
	}

	// Delete the aircraft.
	s.deleteAircraft(serverAc)

	if len(s.Aircraft) != 0 {
		t.Errorf("aircraft map not empty after delete")
	}
	if len(s.STARSComputer.AvailableIndices) != initialAvailable {
		t.Errorf("available indices after delete: got %d, want %d", len(s.STARSComputer.AvailableIndices), initialAvailable)
	}
}

func TestDeleteAircraftSliceUsesServerCopy(t *testing.T) {
	s := makeTestSim()
	initialAvailable := len(s.STARSComputer.AvailableIndices)

	// Spawn a test bench aircraft.
	ac := makeTestBenchAircraft("AAL200")
	s.addAircraftNoLock(ac)

	// Simulate what the client has: a stale copy without server-assigned
	// list index or squawk.
	clientCopy := makeTestBenchAircraft("AAL200")

	// Delete via the slice path (as the test bench Clear button does).
	s.DeleteAircraftSlice("", []Aircraft{clientCopy})

	if len(s.Aircraft) != 0 {
		t.Errorf("aircraft map not empty after DeleteAircraftSlice")
	}
	if len(s.STARSComputer.AvailableIndices) != initialAvailable {
		t.Errorf("available indices after delete: got %d, want %d", len(s.STARSComputer.AvailableIndices), initialAvailable)
	}
	if len(s.STARSComputer.FlightPlans) != 0 {
		t.Errorf("orphaned unassociated flight plans: got %d, want 0", len(s.STARSComputer.FlightPlans))
	}
}

func TestSpawnAndClearMultipleTestBenchAircraft(t *testing.T) {
	s := makeTestSim()
	initialAvailable := len(s.STARSComputer.AvailableIndices)

	// Spawn 3 aircraft, clear them, spawn 2 more, clear them.
	callsigns := []string{"AAL301", "AAL302", "AAL303"}
	var clientCopies []Aircraft
	for _, cs := range callsigns {
		ac := makeTestBenchAircraft(cs)
		clientCopies = append(clientCopies, ac)
		s.addAircraftNoLock(ac)
	}

	if len(s.STARSComputer.AvailableIndices) != initialAvailable-3 {
		t.Errorf("after spawn 3: available indices got %d, want %d",
			len(s.STARSComputer.AvailableIndices), initialAvailable-3)
	}

	s.DeleteAircraftSlice("", clientCopies)

	if len(s.Aircraft) != 0 {
		t.Errorf("aircraft remaining after first clear: %d", len(s.Aircraft))
	}
	if len(s.STARSComputer.AvailableIndices) != initialAvailable {
		t.Errorf("after clear 3: available indices got %d, want %d",
			len(s.STARSComputer.AvailableIndices), initialAvailable)
	}

	// Second batch.
	callsigns2 := []string{"UAL401", "UAL402"}
	var clientCopies2 []Aircraft
	for _, cs := range callsigns2 {
		ac := makeTestBenchAircraft(cs)
		clientCopies2 = append(clientCopies2, ac)
		s.addAircraftNoLock(ac)
	}

	s.DeleteAircraftSlice("", clientCopies2)

	if len(s.STARSComputer.AvailableIndices) != initialAvailable {
		t.Errorf("after clear all: available indices got %d, want %d",
			len(s.STARSComputer.AvailableIndices), initialAvailable)
	}
	if len(s.STARSComputer.FlightPlans) != 0 {
		t.Errorf("orphaned unassociated flight plans: %d", len(s.STARSComputer.FlightPlans))
	}

	// Check for duplicate indices in the available pool.
	seen := make(map[int]bool)
	for _, idx := range s.STARSComputer.AvailableIndices {
		if seen[idx] {
			t.Errorf("duplicate index %d in AvailableIndices", idx)
		}
		seen[idx] = true
	}
}

// sim/eram_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
)

// TestDeriveERAMFixPairFullyContained verifies that a departure whose
// destination is a local facility airport (an internal / fully-contained
// flight) gets the destination airport as its exit fix instead of a
// route/zone-based boundary partition.
func TestDeriveERAMFixPairFullyContained(t *testing.T) {
	s := &Sim{
		State: &CommonState{
			ERAMCoordination: &enroute.Coordination{Coord: &enroute.ArtsCoordEntry{}},
			Airports:         map[string]*av.Airport{"KVPC": {}, "KCPP": {}},
		},
	}
	// Internal departure KVPC -> KCPP (both local): exit fix = destination (K
	// stripped), route/zone skipped.
	ac := &Aircraft{TypeOfFlight: av.FlightTypeDeparture,
		FlightPlan: av.FlightPlan{ArrivalAirport: "KCPP"}}
	fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, ExitFix: "SOONE"}
	res := s.deriveERAMFixPair(fp, ac)
	if !res.OK || res.Fix != "CPP" {
		t.Errorf("fully-contained: got fix=%q ok=%v, want CPP/true", res.Fix, res.OK)
	}
	if fp.ExitFix != "CPP" {
		t.Errorf("ExitFix = %q, want CPP", fp.ExitFix)
	}
	// LocalArrival is flagged for the caller to reclassify, but deriveERAMFixPair
	// itself leaves TypeOfFlight as Departure so the departure fix-pair owner is
	// used before the reclassification happens.
	if !fp.LocalArrival {
		t.Error("LocalArrival not set for internal flight")
	}
	if fp.TypeOfFlight != av.FlightTypeDeparture {
		t.Errorf("deriveERAMFixPair should leave TypeOfFlight=Departure, got %v", fp.TypeOfFlight)
	}
}

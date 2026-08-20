// sim/eram_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"io"
	"log/slog"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
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

// TestDeriveERAMFixPairNormalizesFix verifies that a coordination fix an
// arts_coordination rule names in full is stored on the flight plan as the
// 3-character id flight plans carry, so that the fix pairs, adapted fix
// criteria, and airspace awareness rules matching against it can.
func TestDeriveERAMFixPairNormalizesFix(t *testing.T) {
	coordSim := func(points map[string]SignificantPoint, fix string) *Sim {
		return &Sim{
			State: &CommonState{
				ERAMCoordination: &enroute.Coordination{
					Coord: &enroute.ArtsCoordEntry{
						RouteBased: []enroute.RouteRule{{Type: "string", ID: fix, DefaultFix: fix}},
					},
				},
				Airports:           map[string]*av.Airport{},
				FacilityAdaptation: FacilityAdaptation{SignificantPoints: points},
			},
		}
	}

	for _, tc := range []struct {
		name   string
		points map[string]SignificantPoint
		fix    string
		want   string
	}{
		{"adapted short name", map[string]SignificantPoint{"BOSOX": {ShortName: "BOX"}}, "BOSOX", "BOX"},
		{"short name defaults to the first three characters",
			map[string]SignificantPoint{"ROBUCC": {}}, "ROBUCC", "ROB"},
		{"unadapted fix is left alone", nil, "ROBUCC", "ROBUCC"},
		{"already a 3-character id", map[string]SignificantPoint{"PVD": {}}, "PVD", "PVD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := coordSim(tc.points, tc.fix)

			ac := &Aircraft{TypeOfFlight: av.FlightTypeDeparture,
				FlightPlan: av.FlightPlan{DepartureAirport: "KBOS", ArrivalAirport: "KORD"}}
			fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, Route: tc.fix}
			if res := s.deriveERAMFixPair(fp, ac); !res.OK || res.Fix != tc.want {
				t.Errorf("departure: got fix=%q ok=%v, want %q/true", res.Fix, res.OK, tc.want)
			}
			if fp.ExitFix != tc.want {
				t.Errorf("departure ExitFix = %q, want %q", fp.ExitFix, tc.want)
			}

			// An arrival's coordination fix lands on the entry side and is
			// normalized the same way.
			arrAc := &Aircraft{TypeOfFlight: av.FlightTypeArrival,
				FlightPlan: av.FlightPlan{DepartureAirport: "KORD", ArrivalAirport: "KBOS"}}
			arrFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, Route: tc.fix}
			s.deriveERAMFixPair(arrFp, arrAc)
			if arrFp.EntryFix != tc.want {
				t.Errorf("arrival EntryFix = %q, want %q", arrFp.EntryFix, tc.want)
			}
		})
	}
}

// TestAssignedLevelForCoord verifies that ARTS coordination's "Assigned"
// altitude compares against the flight's actual operational level rather than
// always falling back to filed cruise, per fixPairLevel, when
// NASFlightPlan.AssignedAltitude hasn't been set yet (as for a TRACON-facility
// spawn).
func TestAssignedLevelForCoord(t *testing.T) {
	// Departures are unaffected: same as fixPairLevel (requested altitude).
	depFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, RequestedAltitude: 24000, AssignedAltitude: 10000}
	if got := assignedLevelForCoord(depFp, &Aircraft{}); got != 240 {
		t.Errorf("departure = %d, want 240 (fixPairLevel unchanged)", got)
	}

	// Arrival with AssignedAltitude already known (an ERAM-facility spawn) uses it directly.
	arrFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, AssignedAltitude: 11000, RequestedAltitude: 35000}
	if got := assignedLevelForCoord(arrFp, &Aircraft{}); got != 110 {
		t.Errorf("arrival with AssignedAltitude = %d, want 110", got)
	}

	// Arrival with no AssignedAltitude (a TRACON-facility spawn) but a
	// waypoint altitude restriction derives the nav/route level instead of
	// falling back to filed cruise.
	wp := av.Waypoint{Fix: "FIXXX"}
	wp.SetAltitudeRestriction(av.MakeAtOrBelowAltitudeRestriction(8000))
	noAssignedFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, RequestedAltitude: 35000}
	ac := &Aircraft{Nav: nav.Nav{Waypoints: av.WaypointArray{wp}, FlightState: nav.FlightState{Altitude: 12000}}}
	if got := assignedLevelForCoord(noAssignedFp, ac); got != 80 {
		t.Errorf("arrival with restriction, no AssignedAltitude = %d, want 80 (nav/route derived)", got)
	}

	// No AssignedAltitude and no restrictions falls back to fixPairLevel (cruise).
	noDataFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeOverflight, RequestedAltitude: 24000}
	if got := assignedLevelForCoord(noDataFp, &Aircraft{}); got != 240 {
		t.Errorf("overflight no data = %d, want 240 (fixPairLevel fallback)", got)
	}
}

// TestRestoreERAMCoordinationGeometry verifies that Activate's restore step
// re-derives ZoneArea.Center from CenterStr, which JSON save/load loses since
// it's tagged json:"-" (ParseGeometry only runs at scenario-group load time,
// not when a saved sim is restored).
func TestRestoreERAMCoordinationGeometry(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Nil coordination, and a coordination with no resolved entry, are no-ops.
	restoreERAMCoordinationGeometry(nil, lg)
	restoreERAMCoordinationGeometry(&enroute.Coordination{}, lg)

	ec := &enroute.Coordination{
		ComputerID: "BOA",
		Coord: &enroute.ArtsCoordEntry{
			ZoneBased: []enroute.ZoneArea{{AreaID: "Z1", CenterStr: "N043.33.30.000,W069.30.00.000"}},
		},
	}
	restoreERAMCoordinationGeometry(ec, lg)
	if ec.Coord.ZoneBased[0].Center.IsZero() {
		t.Error("zone area Center should be parsed from CenterStr, not left at its post-JSON-restore zero value")
	}
}

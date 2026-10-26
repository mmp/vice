// sim/vfr_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
)

// nmNorth returns p moved n nautical miles north.
func nmNorth(p math.Point2LL, n float32) math.Point2LL {
	return math.Point2LL{p[0], p[1] + n/60}
}

// nmWest returns p moved n nautical miles west.
func nmWest(p math.Point2LL, n float32) math.Point2LL {
	return math.Point2LL{p[0] - n/math.NMPerLongitudeAt(p), p[1]}
}

// TestFlightFollowingRequestPosition verifies that a flight following request
// gives the aircraft's position relative to a point the controller knows
// rather than the nearest obscure one: the departure airport within 3 miles,
// then the scenario's reporting points within 7, then the scenario's airports
// and VORs within 10, and otherwise the closest airport in the database.
func TestFlightFollowingRequestPosition(t *testing.T) {
	db.InitDB()
	s := NewTestSim(testLogger())
	s.State.Controllers = map[ControlPosition]*av.Controller{"125.0": {RadioName: "NorCal Approach"}}
	for _, icao := range []av.ICAOAirportCode{"KSQL", "KOAK"} {
		s.State.Airports[icao] = &av.Airport{Location: db.DB.Airports[icao].Location}
	}

	hwd := db.DB.Airports["KHWD"].Location
	s.State.NmPerLongitude = math.NMPerLongitudeAt(hwd)
	s.VFRReportingPoints = []av.VFRReportingPoint{{
		Description: "the Moraga reservoir",
		Location:    av.ScenarioPoint2LL{Point2LL: nmNorth(hwd, 11)},
	}}

	ac := MakeTestAircraft("N671", "22L")
	ac.FlightRules = av.FlightRulesVFR
	ac.AircraftType = "C172"
	ac.DepartureAirport = "KHWD"
	ac.Nav.FlightState.DepartureAirportLocation = hwd
	ac.RequestedFlightFollowing = true
	s.Aircraft[ac.ADSBCallsign] = ac

	for _, tc := range []struct {
		name string
		pos  math.Point2LL
		want string
	}{
		{"on the departure airport", hwd, "departing KHWD"},
		{"just off the departure airport", nmNorth(hwd, 2.4), "2 miles North of KHWD"},
		{"departure airport nearer than the reporting point", nmNorth(hwd, 5.4), "5 miles North of KHWD"},
		{"reporting point nearer than the departure airport", nmNorth(hwd, 6.4), "5 miles South of the Moraga reservoir"},
		// Palo Alto is 2 miles away but is not in the scenario; San Carlos is 6 miles away and is.
		{"scenario airport beats a nearer inactive one", nmNorth(db.DB.Airports["KPAO"].Location, 2), "of KSQL"},
		// San Carlos is 9 miles away, so the VOR underneath wins.
		{"VOR nearer than the scenario airport", db.DB.Navaids["SFO"].Location, "overhead San Francisco VOR"},
		// The Oakland VOR is on the field and a tenth of a mile nearer.
		{"scenario airport beats its own VOR", nmWest(db.DB.Airports["KOAK"].Location, 3), "3 miles West of KOAK"},
		{"nothing familiar nearby", db.DB.Airports["KLVK"].Location, "overhead KLVK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ac.Nav.FlightState.Position = tc.pos
			s.enqueuePilotTransmission(ac.ADSBCallsign, "125.0", PendingTransmissionFlightFollowingFull)
			s.State.SimTime = s.State.SimTime.Add(time.Second)
			pt := s.NextPilotTransmission(E2ETCW())
			if pt == nil {
				t.Fatal("no pilot transmission published")
			}
			if !strings.Contains(pt.Written, tc.want) {
				t.Errorf("expected %q in %q", tc.want, pt.Written)
			}
			s.TakePilotTransmission(E2ETCW(), *pt)
		})
	}
}

// BenchmarkVFRPositionReference measures choosing a position reference when
// nothing familiar is nearby, which scans every VOR and airport in the
// database.
func BenchmarkVFRPositionReference(b *testing.B) {
	db.InitDB()
	s := NewTestSim(testLogger())
	hwd := db.DB.Airports["KHWD"].Location
	s.State.NmPerLongitude = math.NMPerLongitudeAt(hwd)
	s.State.Airports["KOAK"] = &av.Airport{Location: db.DB.Airports["KOAK"].Location}

	ac := MakeTestAircraft("N671", "22L")
	ac.DepartureAirport = "KHWD"
	ac.Nav.FlightState.DepartureAirportLocation = hwd
	ac.Nav.FlightState.Position = db.DB.Airports["KLVK"].Location

	for b.Loop() {
		s.vfrPositionReference(ac)
	}
}

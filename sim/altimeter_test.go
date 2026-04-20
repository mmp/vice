// sim/altimeter_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

// knownTestAirportLocations maps ICAO codes used in tests to approximate
// lat/lon coordinates so nearestActualAltim can find them in av.DB.
var knownTestAirportLocations = map[string]math.Point2LL{
	"KJFK": {-73.78, 40.64},
	"KLGA": {-73.87, 40.77},
	"KEWR": {-74.17, 40.69},
	"KABE": {-75.44, 40.65},
}

func newTestSimWithMETAR(t *testing.T, settings map[string]float32) *Sim {
	t.Helper()
	metar := make(map[string]wx.METAR)

	// Ensure av.DB is initialised and contains the airports we need.
	if av.DB == nil {
		av.DB = &av.StaticDatabase{Airports: map[string]av.FAAAirport{}}
	}
	for icao, altInHg := range settings {
		// wx.METAR.Altimeter is in hPa; Altimeter_inHg() returns 0.02953 * Altimeter.
		// So set Altimeter = altInHg / 0.02953 to get the desired inHg value.
		metar[icao] = wx.METAR{
			ICAO:      icao,
			Altimeter: altInHg / 0.02953,
		}
		// Register a stub airport entry so av.DB.Airports lookups succeed.
		if _, exists := av.DB.Airports[icao]; !exists {
			loc, ok := knownTestAirportLocations[icao]
			if !ok {
				loc = math.Point2LL{} // zero-island as fallback
			}
			av.DB.Airports[icao] = av.FAAAirport{Location: loc}
		}
	}
	t.Cleanup(func() {
		for icao := range settings {
			delete(av.DB.Airports, icao)
		}
	})
	return &Sim{
		State:                       &CommonState{DynamicState: DynamicState{METAR: metar}},
		SimulateIncorrectAltimeters: true,
	}
}

func TestAltimBiasFeet(t *testing.T) {
	tests := []struct {
		name              string
		nearestActualInHg float32
		pilotInHg         float32
		want              float32
	}{
		{"zero pilot short-circuits", 30.05, 0, 0},
		{"equal values give zero bias", 30.05, 30.05, 0},
		{"pilot too low yields positive bias", 30.10, 30.00, 100},
		{"pilot too high yields negative bias", 30.00, 30.10, -100},
		{"realistic small mismatch", 30.05, 30.03, 20},
		{"large geographic delta", 30.20, 29.80, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := altimBiasFeet(tc.nearestActualInHg, tc.pilotInHg)
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("altimBiasFeet(%v, %v) = %v, want %v",
					tc.nearestActualInHg, tc.pilotInHg, got, tc.want)
			}
		})
	}
}

func TestNearestActualAltimEmptyMap(t *testing.T) {
	s := &Sim{State: &CommonState{DynamicState: DynamicState{METAR: map[string]wx.METAR{}}}}
	got := s.nearestActualAltim(math.Point2LL{-73.78, 40.64}) // KJFK area
	if got != 0 {
		t.Errorf("nearestActualAltim with empty map = %v, want 0", got)
	}
}

func TestInitPilotAltimSetsCorrectForArrival(t *testing.T) {
	s := newTestSimWithMETAR(t, map[string]float32{"KJFK": 30.05})
	ac := &Aircraft{TypeOfFlight: av.FlightTypeArrival}
	ac.Nav.FlightState.Position = math.Point2LL{-73.78, 40.64} // near KJFK
	s.initPilotAltim(ac)
	if math.Abs(ac.PilotAltim-30.05) > 0.001 {
		t.Errorf("arrival PilotAltim = %v, want 30.05", ac.PilotAltim)
	}
}

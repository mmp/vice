// sim/altimeter_integration_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// newTestAircraftAtAltitude returns a minimal Aircraft suitable for the
// altitude-bias integration test: arrival, IFR, on a virtual frequency,
// at the requested altitude with that altitude assigned.
func newTestAircraftAtAltitude(t *testing.T, altitude float32) *Aircraft {
	t.Helper()
	ac := &Aircraft{
		ADSBCallsign:        "TEST123",
		TypeOfFlight:        av.FlightTypeArrival,
		ControllerFrequency: "TEST_TCP",
	}
	ac.Nav.FlightState.Altitude = altitude
	ac.Nav.FlightState.IAS = 250
	ac.Nav.FlightState.GS = 250
	// NmPerLongitude prevents updatePositionAndGS from producing NaN
	// coordinates that corrupt nearestActualAltim lookups.  ~45.5 is
	// correct for 40-41° N (NY/NJ area).
	ac.Nav.FlightState.NmPerLongitude = 45.5
	assigned := altitude
	ac.Nav.Altitude.Assigned = &assigned
	// Set realistic climb/descent rates so updateAltitude can actually move
	// the aircraft when a bias shifts the target altitude.
	ac.Nav.Perf.Rate.Climb = 2000   // ft/min
	ac.Nav.Perf.Rate.Descent = 2000 // ft/min
	ac.Nav.Perf.Speed.V2 = 150      // kts; ensures IsAirborne() returns true at IAS=250
	return ac
}

// tickOnce advances the sim by one update cycle. Mirrors the per-aircraft
// loop body in sim.go in miniature.
func (s *Sim) tickOnce() {
	s.State.SimTime = s.State.SimTime.Add(time.Second)
	stubModel := &wx.Model{}
	for _, ac := range s.Aircraft {
		ac.Update(stubModel, s.altimBiasFor(ac), s.State.SimTime, nil, nil)
	}
}

// TestAltimeterBiasShiftsScopedAltitude verifies the full physics path:
// an aircraft set to one airport's altimeter, flown near a different
// airport with a different altimeter, accrues bias visible on the scope.
func TestAltimeterBiasShiftsScopedAltitude(t *testing.T) {
	s := newTestSimWithMETAR(t, map[string]float32{
		"KJFK": 30.05,
		"KABE": 29.95, // ~70 NM west, lower altimeter
	})
	// Initialize runtime-only fields that GenerateContactTransmission needs.
	s.Rand = rand.Make()
	s.eventStream = NewEventStream(nil)

	ac := newTestAircraftAtAltitude(t, 5000)
	ac.Nav.FlightState.Position = math.Point2LL{-73.78, 40.64} // KJFK
	ac.PilotAltim = 30.05
	ac.PilotAltimSetAt = s.State.SimTime
	if s.Aircraft == nil {
		s.Aircraft = make(map[av.ADSBCallsign]*Aircraft)
	}
	s.Aircraft[ac.ADSBCallsign] = ac

	// Tick once near KJFK — bias should be ~0.
	s.tickOnce()
	if d := math.Abs(ac.Nav.FlightState.Altitude - 5000); d > 1 {
		t.Errorf("near KJFK: altitude = %v, want ~5000 (delta %v)", ac.Nav.FlightState.Altitude, d)
	}

	// Teleport near KABE.
	ac.Nav.FlightState.Position = math.Point2LL{-75.44, 40.65} // KABE
	// Run several ticks to let the aircraft drift to the new biased target.
	for i := 0; i < 60; i++ {
		s.tickOnce()
	}
	// Expected bias: (29.95 - 30.05) * 1000 = -100 ft.
	if d := math.Abs(ac.Nav.FlightState.Altitude - 4900); d > 50 {
		t.Errorf("near KABE after settle: altitude = %v, want ~4900 (delta %v)", ac.Nav.FlightState.Altitude, d)
	}

	// Issue an altimeter-setting command. The dispatcher mutates PilotAltim
	// immediately and returns a readback intent.
	intent := s.handleAltimeterSetting(ac, 2995)
	if intent == nil {
		t.Fatal("expected an AltimeterReadbackIntent, got nil")
	}
	if _, ok := intent.(av.AltimeterReadbackIntent); !ok {
		t.Fatalf("expected AltimeterReadbackIntent, got %T", intent)
	}

	if math.Abs(ac.PilotAltim-29.95) > 0.001 {
		t.Errorf("after readback: PilotAltim = %v, want 29.95", ac.PilotAltim)
	}

	// Tick to let the aircraft drift back. Bias is now 0 (29.95 == 29.95).
	for i := 0; i < 60; i++ {
		s.tickOnce()
	}
	if d := math.Abs(ac.Nav.FlightState.Altitude - 5000); d > 50 {
		t.Errorf("after correction: altitude = %v, want ~5000 (delta %v)", ac.Nav.FlightState.Altitude, d)
	}
}

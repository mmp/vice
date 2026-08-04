// sim/spawn_pattern_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/nav"
)

var patternTestTime = NewSimTime(time.Date(2026, time.August, 4, 15, 0, 0, 0, time.UTC))

// vfrArrival returns an arrival to the given airport whose next waypoint is
// in the given phase of a VFR arrival.
func vfrArrival(callsign, airport string, phase uint8) *Aircraft {
	return &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		FlightPlan:   av.FlightPlan{ArrivalAirport: airport},
		Nav:          nav.Nav{Waypoints: av.WaypointArray{{Fix: "_wp", VFRPhase: phase}}},
	}
}

// holdingArrival returns an arrival orbiting at the given airport since the
// given time, waiting for its turn to enter the pattern.
func holdingArrival(callsign, airport string, since Time) *Aircraft {
	ac := vfrArrival(callsign, airport, av.VFRPhaseOrbit)
	ac.HoldingSince = since
	return ac
}

func makePatternSim(acs ...*Aircraft) *Sim {
	s := &Sim{
		Aircraft:     make(map[av.ADSBCallsign]*Aircraft),
		PatternState: make(map[string]*PatternState),
	}
	for _, ac := range acs {
		s.Aircraft[ac.ADSBCallsign] = ac
	}
	return s
}

func TestLongestHoldingArrival(t *testing.T) {
	first := holdingArrival("N111", "KFOK", patternTestTime)
	second := holdingArrival("N222", "KFOK", patternTestTime.Add(2*time.Minute))
	elsewhere := holdingArrival("N333", "KGON", patternTestTime.Add(-time.Hour))
	inbound := vfrArrival("N444", "KFOK", av.VFRPhaseStraightIn)
	s := makePatternSim(second, first, elsewhere, inbound)

	if got := s.longestHoldingArrival("KFOK"); got != first {
		t.Errorf("longest holder at KFOK is %v, expected N111", got)
	}
	if got := s.longestHoldingArrival("KGON"); got != elsewhere {
		t.Errorf("longest holder at KGON is %v, expected N333", got)
	}
	if got := s.longestHoldingArrival("KISP"); got != nil {
		t.Errorf("longest holder at KISP is %v, expected none", got)
	}
}

func TestLongestHoldingArrivalBreaksTiesByCallsign(t *testing.T) {
	first := holdingArrival("N111", "KFOK", patternTestTime)
	second := holdingArrival("N222", "KFOK", patternTestTime)
	s := makePatternSim(first, second)

	// Both started holding at the same time, so the result can't be left to
	// map iteration order.
	for range 10 {
		if got := s.longestHoldingArrival("KFOK"); got != first {
			t.Fatalf("longest holder is %v, expected N111", got)
		}
	}
}

func TestFreshArrivalsHoldBehindWaitingTraffic(t *testing.T) {
	// Nothing in the pattern and no one waiting: go on in.
	if s := makePatternSim(); s.arrivalsMustHold("KFOK") {
		t.Error("arrival had to hold at an airport with no traffic")
	}

	// Someone is already holding: a new arrival can't take the slot from
	// under it, even though the pattern is clear.
	s := makePatternSim(holdingArrival("N111", "KFOK", patternTestTime))
	if !s.arrivalsMustHold("KFOK") {
		t.Error("arrival jumped the queue ahead of an aircraft that was already holding")
	}

	// No one waiting, but there's no room to enter.
	s = makePatternSim(vfrArrival("N222", "KFOK", av.VFRPhaseDownwind))
	if !s.arrivalsMustHold("KFOK") {
		t.Error("arrival entered the pattern with traffic on the entry downwind")
	}
}

func TestHoldingArrivalsAdmittedInOrder(t *testing.T) {
	fokFirst := holdingArrival("N111", "KFOK", patternTestTime)
	fokSecond := holdingArrival("N222", "KFOK", patternTestTime.Add(time.Minute))
	gon := holdingArrival("N333", "KGON", patternTestTime.Add(time.Hour))
	hvn := holdingArrival("N444", "KHVN", patternTestTime)
	hvnBlocker := vfrArrival("N555", "KHVN", av.VFRPhaseDownwind)
	s := makePatternSim(fokFirst, fokSecond, gon, hvn, hvnBlocker)

	admit := s.holdingArrivalsToAdmit()

	// One per airport with room: the longest-waiting one at KFOK and the
	// only one at KGON. KHVN's pattern is occupied, so nobody gets in there.
	want := []*Aircraft{fokFirst, gon}
	if len(admit) != len(want) {
		t.Fatalf("admitting %d aircraft, expected %d", len(admit), len(want))
	}
	for i := range want {
		if admit[i] != want[i] {
			t.Errorf("admitting %s at index %d, expected %s", admit[i].ADSBCallsign, i,
				want[i].ADSBCallsign)
		}
	}
}

func TestPatternAircraftOnDownwindBlocksAdmission(t *testing.T) {
	holding := holdingArrival("N111", "KFOK", patternTestTime)
	patternAC := vfrArrival("N222", "KFOK", av.VFRPhaseDownwind)
	s := makePatternSim(holding, patternAC)
	s.PatternState["KFOK"] = &PatternState{
		Aircraft: []PatternAircraft{{ADSBCallsign: patternAC.ADSBCallsign, Phase: PatternDownwind}},
	}

	if admit := s.holdingArrivalsToAdmit(); len(admit) != 0 {
		t.Errorf("admitted %d aircraft with a pattern aircraft on downwind, expected none", len(admit))
	}

	// Once it turns base there's room for the holding aircraft to enter.
	s.PatternState["KFOK"].Aircraft[0].Phase = PatternBase
	patternAC.Nav.Waypoints[0].VFRPhase = av.VFRPhaseBase
	admit := s.holdingArrivalsToAdmit()
	if len(admit) != 1 || admit[0] != holding {
		t.Errorf("admitted %v, expected just N111", admit)
	}
}

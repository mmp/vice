// sim/spawn_inbound_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/traffic"
)

// A flight at an airport the scenario lands no traffic at must not hold up the
// arrivals behind it: the queue is shared across every airport a facility
// works, so one stuck at the head would stall all of them.
func TestZeroRateArrivalsDoNotBlock(t *testing.T) {
	day := traffic.FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC))
	spawn := NewSimTime(time.Date(2026, time.April, 15, 8, 0, 0, 0, time.UTC))
	arrival := func(airport av.ICAOAirportCode, callsign, group string) ScheduledArrival {
		return ScheduledArrival{
			ScheduledFlight: ScheduledFlight{Callsign: callsign, AircraftType: "B738",
				DepartureAirport: "KATL", ArrivalAirport: airport,
				Source: TrafficSourceHistorical, SpawnTime: spawn, Day: day, Minute: 8 * 60},
			Group: group,
		}
	}

	s := NewTestSim(testLogger())
	s.State.SimTime = spawn
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates: map[string]map[string]float32{
			"PUCKY1": {"KFRG": 0, "KJFK": 12},
			"CAMRN5": {"KJFK": 12},
		},
		InboundFlowEnabled: map[string]map[string]bool{
			"PUCKY1": {"KFRG": false, "KJFK": true},
			"CAMRN5": {"KJFK": true},
		},
	}
	s.Schedule.Arrivals = []ScheduledArrival{
		arrival("KFRG", "DAL1", "PUCKY1"), // PUCKY1 lands nothing at KFRG
		arrival("KFRG", "DAL2", "PUCKY1"),
		arrival("KJFK", "DAL3", "PUCKY1"), // but it does at KJFK
		arrival("KJFK", "DAL4", "CAMRN5"), // and this one belongs to another flow
	}

	// The KFRG arrivals are discarded when due rather than deferring, so the
	// KJFK ones behind them are reached; creating those fails in this bare
	// test sim (there are no inbound flows to fly), but only after they were
	// taken up in their turn.
	s.spawnScheduledArrivals()

	if s.discardedArrivals["KFRG"] != 2 {
		t.Errorf("discarded %v, expected both KFRG arrivals", s.discardedArrivals)
	}
	if n := len(s.Schedule.Arrivals); n != 0 {
		t.Errorf("%d arrivals left in the queue, expected all processed", n)
	}
}

// An inbound flow's arrivals open up to 10 miles in trail where the data
// leaves room, closing up toward 6 only as far as it takes to hold none of
// those coming up behind more than 3 minutes past its data time. 6 miles is a
// hard floor, however late a burst runs. At 240 knots, 6 miles takes 90
// seconds and 10 takes 150.
func TestArrivalFlowSpacing(t *testing.T) {
	launch := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	arrival := func(callsign, group string, airport av.ICAOAirportCode, after time.Duration) ScheduledArrival {
		return testScheduledArrival(callsign, group, airport, launch.Add(after))
	}

	s := NewTestSim(testLogger())
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates:   map[string]map[string]float32{"TEST": {"KJFK": 12, "KFRG": 6}},
		InboundFlowEnabled: map[string]map[string]bool{"TEST": {"KJFK": true, "KFRG": false}},
	}
	s.State.SimTime = launch
	if !s.arrivalFlowSpaced("TEST") {
		t.Error("held a flow that has launched nothing")
	}

	for _, tc := range []struct {
		name  string
		queue []ScheduledArrival
		trail time.Duration
	}{
		{"a lone pair opens to 10 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "TEST", "KJFK", 12*time.Minute),
		}, 150 * time.Second},
		{"three close together share the hold at 8 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 30*time.Second),
			arrival("DAL2", "TEST", "KJFK", time.Minute),
			arrival("DAL3", "TEST", "KJFK", 20*time.Minute),
		}, 120 * time.Second},
		{"a burst closes up to 6 miles", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 30*time.Second),
			arrival("DAL2", "TEST", "KJFK", time.Minute),
			arrival("DAL3", "TEST", "KJFK", 90*time.Second),
			arrival("DAL4", "TEST", "KJFK", 2*time.Minute),
		}, 90 * time.Second},
		{"another flow's arrivals don't crowd it", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "OTHER", "KJFK", 30*time.Second),
			arrival("DAL3", "OTHER", "KJFK", time.Minute),
		}, 150 * time.Second},
		{"arrivals at an airport switched off don't crowd it", []ScheduledArrival{
			arrival("DAL1", "TEST", "KJFK", 20*time.Second),
			arrival("DAL2", "TEST", "KFRG", 30*time.Second),
			arrival("DAL3", "TEST", "KFRG", time.Minute),
		}, 150 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.ArrivalLaunches = map[string]ArrivalLaunch{"TEST": {Time: launch, TAS: 240}}
			s.Schedule.Arrivals = tc.queue

			s.State.SimTime = launch.Add(tc.trail - time.Second)
			if s.arrivalFlowSpaced("TEST") {
				t.Errorf("launched sooner than %s behind the last arrival", tc.trail)
			}
			s.State.SimTime = launch.Add(tc.trail + time.Second)
			if !s.arrivalFlowSpaced("TEST") {
				t.Errorf("held longer than %s behind the last arrival", tc.trail)
			}
		})
	}
}

// Recording a launch makes the aircraft its flow's last, with its true
// airspeed.
func TestRecordArrivalLaunch(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := NewTestSim(testLogger())
	s.State.SimTime = now
	s.ArrivalLaunches = map[string]ArrivalLaunch{"TEST": {Time: now.Add(-10 * time.Minute), TAS: 250}}
	ac := &Aircraft{Nav: nav.Nav{FlightState: nav.FlightState{IAS: 250, Altitude: 12000}}}

	s.recordArrivalLaunch("TEST", ac)

	last := s.ArrivalLaunches["TEST"]
	if last.Time != now {
		t.Errorf("flow's last launch is at %s, want this one at %s", last.Time, now)
	}
	if last.TAS <= 290 || last.TAS >= 320 {
		t.Errorf("recorded %.0f knots, want the true airspeed of 250 knots at 12,000'", last.TAS)
	}
}

// Due published arrivals in a flow that isn't spaced yet stay queued in
// order, and hold up no other flow's. Scenario arrivals are spaced by their
// rates and never held.
func TestSpacedFlowHoldsOnlyItsOwnArrivals(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := NewTestSim(testLogger())
	s.State.SimTime = now
	s.State.LaunchConfig = LaunchConfig{
		InboundFlowRates:   map[string]map[string]float32{"PUCKY1": {"KJFK": 7}, "MIP4": {"KLGA": 30}},
		InboundFlowEnabled: map[string]map[string]bool{"PUCKY1": {"KJFK": true}, "MIP4": {"KLGA": true}},
	}
	justLaunched := ArrivalLaunch{Time: now.Add(-30 * time.Second), TAS: 300}
	s.ArrivalLaunches = map[string]ArrivalLaunch{"PUCKY1": justLaunched, "CAMRN5": justLaunched}
	scenario := testScheduledArrival("AAL4", "CAMRN5", "KJFK", now)
	scenario.Source = TrafficSourceScenario
	s.Schedule.Arrivals = []ScheduledArrival{
		testScheduledArrival("DAL1", "PUCKY1", "KJFK", now.Add(-5*time.Minute)),
		testScheduledArrival("DAL2", "MIP4", "KLGA", now.Add(-time.Minute)),
		testScheduledArrival("DAL3", "PUCKY1", "KJFK", now),
		scenario,
	}

	// DAL2 and AAL4 are taken up in their turn; creating them fails in this
	// bare test sim, which has no flows to fly, and so they leave the queue.
	s.spawnScheduledArrivals()

	var queued []string
	for _, e := range s.Schedule.Arrivals {
		queued = append(queued, e.Callsign)
	}
	if !slices.Equal(queued, []string{"DAL1", "DAL3"}) {
		t.Errorf("queue holds %v, want the PUCKY1 arrivals waiting in order", queued)
	}
}

// An inbound flight's ERAM hard altitude is what it was cleared to, since
// conflict alert takes a level flight to be free to go anywhere between its
// altitude and the hard altitude.
func TestSetInboundERAMAltitudes(t *testing.T) {
	restricted := func(fix string, ar av.AltitudeRestriction) av.Waypoint {
		wp := av.Waypoint{Fix: fix}
		wp.SetAltitudeRestriction(ar)
		return wp
	}
	// The BAUBB3 as the ZLA scenarios fly it: its bottom is 4000 at EZKEL.
	baubb := av.WaypointArray{
		restricted("TCUPS", av.MakeAtOrAboveAltitudeRestriction(26000)),
		restricted("BAUBB", av.MakeRangeAltitudeRestriction(11000, 13000)),
		{Fix: "STYFF"},
		restricted("EZKEL", av.MakeAtAltitudeRestriction(4000)),
	}

	for _, tc := range []struct {
		name              string
		wps               av.WaypointArray
		assigned, cleared float32
		expected          int
	}{
		{name: "assigned altitude", wps: baubb, assigned: 21000, expected: 21000},
		{name: "assigned wins over cleared", wps: baubb, assigned: 21000, cleared: 24000, expected: 21000},
		{name: "descend via, except maintain", wps: baubb, cleared: 24000, expected: 24000},
		{name: "descend via", wps: baubb, expected: 4000},
		{name: "no restrictions", wps: av.WaypointArray{{Fix: "GVE"}, {Fix: "BAILZ"}}, expected: 35000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fp FlightPlan
			fp.setInboundERAMAltitudes(tc.wps, tc.assigned, tc.cleared, 35000)
			if fp.AssignedAltitude != tc.expected {
				t.Errorf("assigned altitude %d, expected %d", fp.AssignedAltitude, tc.expected)
			}
			if fp.DataBlockAltitude() != tc.expected {
				t.Errorf("data block altitude %d, expected %d", fp.DataBlockAltitude(), tc.expected)
			}
		})
	}
}

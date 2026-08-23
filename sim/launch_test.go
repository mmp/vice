// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
)

// launchTestSim is the bare Sim the launch and recycle bookkeeping needs; no
// aircraft are ever created in these tests.
func launchTestSim() *Sim {
	return &Sim{
		lg:       &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),
		State:    &CommonState{},
	}
}

func testScheduledArrival(callsign, group, airport string, spawn Time) ScheduledArrival {
	return ScheduledArrival{
		ScheduledFlight: ScheduledFlight{Callsign: callsign, AircraftType: "B738",
			DepartureAirport: "KATL", ArrivalAirport: airport,
			Source: TrafficSourceHistorical, SpawnTime: spawn},
		Group: group,
	}
}

// Recycling a published flight removes it and pulls the rest of its flow
// earlier by the gap it leaves, carrying the shift in SpawnOffset; other
// flows are untouched.
func TestRecycleShiftsPublishedFlow(t *testing.T) {
	t0 := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := launchTestSim()
	s.State.SimTime = t0
	s.Schedule.Arrivals = []ScheduledArrival{
		testScheduledArrival("DAL1", "TEST", "KMSP", t0),
		testScheduledArrival("DAL4", "OTHER", "KMSP", t0.Add(2*time.Minute)),
		testScheduledArrival("DAL2", "TEST", "KMSP", t0.Add(5*time.Minute)),
		testScheduledArrival("DAL3", "TEST", "KMSP", t0.Add(9*time.Minute)),
	}

	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "DAL1"}); err != nil {
		t.Fatalf("recycle: %v", err)
	}

	want := []struct {
		callsign string
		spawn    Time
		offset   time.Duration
	}{
		{"DAL2", t0, -5 * time.Minute},
		{"DAL4", t0.Add(2 * time.Minute), 0},
		{"DAL3", t0.Add(4 * time.Minute), -5 * time.Minute},
	}
	if len(s.Schedule.Arrivals) != len(want) {
		t.Fatalf("%d arrivals left, want %d", len(s.Schedule.Arrivals), len(want))
	}
	for i, expected := range want {
		got := s.Schedule.Arrivals[i]
		if got.Callsign != expected.callsign || got.SpawnTime != expected.spawn ||
			got.SpawnOffset != expected.offset {
			t.Errorf("arrival %d = %s at %s offset %s, want %s at %s offset %s",
				i, got.Callsign, got.SpawnTime.Time(), got.SpawnOffset,
				expected.callsign, expected.spawn.Time(), expected.offset)
		}
	}

	// The shift is ordinary sim state: it survives a save and reload.
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded Sim
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s.Schedule, reloaded.Schedule) {
		t.Error("recycled schedule changed across a save and reload")
	}

	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "N12345"}); !errors.Is(err, ErrNoMatchingFlight) {
		t.Errorf("recycling an unknown callsign returned %v, want ErrNoMatchingFlight", err)
	}
}

// A turnaround puts the same callsign in the schedule twice, arriving and
// later departing again. Launching or recycling from an arrival slot must act
// on the arrival, not the departure leg.
func TestRecycleTurnaroundActsOnTheRightLeg(t *testing.T) {
	t0 := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := launchTestSim()
	s.State.SimTime = t0

	s.Schedule.Arrivals = []ScheduledArrival{
		testScheduledArrival("LXJ415", "TEST", "KFRG", t0),
	}
	s.Schedule.Departures = []ScheduledDeparture{{
		ScheduledFlight: ScheduledFlight{Callsign: "LXJ415", AircraftType: "B738",
			DepartureAirport: "KFRG", ArrivalAirport: "KATL",
			Source: TrafficSourceHistorical, SpawnTime: t0.Add(time.Hour)},
	}}

	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "LXJ415"}); err != nil {
		t.Fatalf("recycle arrival: %v", err)
	}
	if len(s.Schedule.Arrivals) != 0 || len(s.Schedule.Departures) != 1 {
		t.Error("recycling the arrival leg touched the departure leg")
	}

	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "LXJ415", Departure: true}); err != nil {
		t.Fatalf("recycle departure: %v", err)
	}
	if len(s.Schedule.Departures) != 0 {
		t.Error("recycling the departure leg left it queued")
	}
}

// Published traffic can fly the same callsign more than once a day in the
// same direction; the flight data's day and minute pick out which queued
// flight a slot is showing.
func TestRecycleSameCallsignTwice(t *testing.T) {
	t0 := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := launchTestSim()
	s.State.SimTime = t0

	first := testScheduledArrival("DAL7", "TEST", "KMSP", t0)
	first.Minute = 8 * 60
	second := testScheduledArrival("DAL7", "TEST", "KMSP", t0.Add(6*time.Hour))
	second.Minute = 14 * 60
	s.Schedule.Arrivals = []ScheduledArrival{first, second}

	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "DAL7", Minute: 14 * 60}); err != nil {
		t.Fatalf("recycle: %v", err)
	}
	if len(s.Schedule.Arrivals) != 1 || s.Schedule.Arrivals[0].Minute != 8*60 {
		t.Error("recycling the later DAL7 did not remove it")
	}
}

// A sim saved in manual departure mode carries PendingVFR but not the retry
// timers, which are just throttles; refilling after a reload must not trip
// over the missing map when VFR sampling fails.
func TestVFRRetryTimersAfterReload(t *testing.T) {
	s := launchTestSim()
	s.Rand = rand.Make()
	s.State.LaunchConfig.DepartureMode = LaunchManual
	s.State.LaunchConfig.VFRAirportRates = map[string]float32{"KFRG": 10}
	// As after a reload: the serialized pending map is present, the
	// unserialized retry timers are not. Sampling fails in this bare sim, so
	// the refill records a retry time.
	s.PendingVFR = make(map[string]*Aircraft)

	s.refillPendingLaunches()

	if _, ok := s.nextVFRSample["KFRG"]; !ok {
		t.Error("no retry time recorded after a failed VFR sample")
	}
}

// The manual launch slots' pending flights reserve their callsigns, so no
// two slots can end up showing the same one.
func TestPendingCallsignsAreReserved(t *testing.T) {
	s := launchTestSim()
	s.STARSComputer = makeSTARSComputer("TEST")
	s.PendingArrivals = map[string]*ScheduledArrival{
		"TEST/KMSP": {ScheduledFlight: ScheduledFlight{Callsign: "COA123"}},
	}
	s.PendingVFR = map[string]*Aircraft{
		"KFRG": {ADSBCallsign: "N123AB"},
	}

	callsigns := s.currentCallsigns()
	for _, callsign := range []av.ADSBCallsign{"COA123", "N123AB"} {
		if !slices.Contains(callsigns, callsign) {
			t.Errorf("pending callsign %s not reserved", callsign)
		}
	}
}

// Recycling a pending scenario flight just discards the sampled identity;
// nothing was allocated for it, so the STARS list indices are untouched.
func TestRecyclePendingAllocatesNothing(t *testing.T) {
	s := launchTestSim()
	s.STARSComputer = makeSTARSComputer("TEST")
	indices := len(s.STARSComputer.AvailableIndices)

	s.PendingArrivals = map[string]*ScheduledArrival{
		"TEST/KMSP": {ScheduledFlight: ScheduledFlight{Callsign: "AAL123"}},
	}
	if err := s.RecycleLaunchAircraft("TCW", LaunchFlight{Callsign: "AAL123"}); err != nil {
		t.Fatalf("recycle: %v", err)
	}
	if len(s.PendingArrivals) != 0 {
		t.Error("pending arrival still present after recycle")
	}
	if len(s.STARSComputer.AvailableIndices) != indices {
		t.Errorf("%d list indices available after recycle, want %d",
			len(s.STARSComputer.AvailableIndices), indices)
	}
}

// Switching launches from manual back to automatic pushes the kind's schedule
// later by the time spent in manual mode: every flight resumes as far from
// launch as it was when manual mode began, rather than a backlog spawning at
// once.
func TestManualModeShiftsScheduleOnResume(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := launchTestSim()
	s.State.SimTime = now
	s.State.LaunchConfig.ArrivalMode = LaunchAutomatic
	old := s.State.LaunchConfig
	old.ArrivalMode = LaunchManual
	s.ArrivalManualSince = now.Add(-20 * time.Minute)
	s.Schedule.ScenarioGeneratedUntil = now.Add(8 * time.Hour)

	// DAL1 was ten minutes from spawning when manual mode began; DAL2 was
	// twenty-three. AAL5 is a scenario arrival near the end of the generated
	// window, which the shift would push past it.
	scenario := testScheduledArrival("AAL5", "TEST", "KMSP", now.Add(7*time.Hour+50*time.Minute))
	scenario.Source = TrafficSourceScenario
	s.Schedule.Arrivals = []ScheduledArrival{
		testScheduledArrival("DAL1", "TEST", "KMSP", now.Add(-10*time.Minute)),
		testScheduledArrival("DAL2", "OTHER", "KMSP", now.Add(3*time.Minute)),
		scenario,
	}

	s.applyScheduleConfigChanges(&old)

	want := []struct {
		callsign string
		spawn    Time
		offset   time.Duration
	}{
		{"DAL1", now.Add(10 * time.Minute), 20 * time.Minute},
		{"DAL2", now.Add(23 * time.Minute), 20 * time.Minute},
		// AAL5 was pushed past the generation horizon and dropped; extension
		// regenerates that stretch at the configured rates.
	}
	if len(s.Schedule.Arrivals) != len(want) {
		t.Fatalf("%d arrivals, want %d", len(s.Schedule.Arrivals), len(want))
	}
	for i, expected := range want {
		got := s.Schedule.Arrivals[i]
		if got.Callsign != expected.callsign || got.SpawnTime != expected.spawn ||
			got.SpawnOffset != expected.offset {
			t.Errorf("arrival %d = %s at %s offset %s, want %s at %s offset %s",
				i, got.Callsign, got.SpawnTime.Time(), got.SpawnOffset,
				expected.callsign, expected.spawn.Time(), expected.offset)
		}
	}
	if !s.ArrivalManualSince.IsZero() {
		t.Error("ArrivalManualSince not cleared on the switch back to automatic")
	}

	// Switching to manual records when, so the next switch back knows how
	// long the schedule sat.
	old = s.State.LaunchConfig
	s.State.LaunchConfig.ArrivalMode = LaunchManual
	s.applyScheduleConfigChanges(&old)
	if s.ArrivalManualSince != now {
		t.Errorf("ArrivalManualSince = %s after switching to manual, want %s",
			s.ArrivalManualSince.Time(), now.Time())
	}
}

// A scenario departure whose runway gate pool is backed up stays queued until
// the pool drains rather than being lost.
func TestGateBacklogDefersScheduledDepartures(t *testing.T) {
	now := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := launchTestSim()
	s.State.SimTime = now

	depState := &RunwayLaunchState{}
	for range 10 {
		depState.Gate = append(depState.Gate, DepartureAircraft{})
	}
	s.DepartureState = map[string]map[av.RunwayID]*RunwayLaunchState{
		"KMSP": {"12L": depState},
	}

	s.Schedule.Departures = []ScheduledDeparture{{
		ScheduledFlight: ScheduledFlight{Callsign: "AAL1", AircraftType: "B738",
			DepartureAirport: "KMSP", ArrivalAirport: "KATL",
			Source: TrafficSourceScenario, SpawnTime: now.Add(-time.Minute)},
		Runway: "12L",
	}}

	s.spawnScheduledDepartures()
	if len(s.Schedule.Departures) != 1 {
		t.Fatal("dropped a due departure while the gate was backed up")
	}

	// Once the gate drains, the entry is taken up in its turn; creating it
	// fails in this bare test sim, but it is no longer deferred.
	depState.Gate = depState.Gate[:5]
	s.spawnScheduledDepartures()
	if len(s.Schedule.Departures) != 0 {
		t.Error("departure still deferred after the gate drained")
	}
}

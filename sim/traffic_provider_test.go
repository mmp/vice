// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"
)

func TestScheduleTrafficProviderOrdersDeparturesFromSelectedStartTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.Local))
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{Callsign: "DAL2", Origin: "KMSP", Destination: "KATL", ScheduledMinute: 14*60 + 10},
			{Callsign: "DAL1", Origin: "KMSP", Destination: "KORD", ScheduledMinute: 14*60 + 2},
			{Callsign: "DAL0", Origin: "KMSP", Destination: "KDEN", ScheduledMinute: 13*60 + 55},
			{Callsign: "DAL3", Origin: "KATL", Destination: "KMSP", ScheduledMinute: 14*60 + 1},
		},
	}

	provider := newScheduleTrafficProvider(schedule, 14*60, start)
	if len(provider.departures) != 3 {
		t.Fatalf("got %d departures, want 3", len(provider.departures))
	}

	want := []struct {
		callsign string
		offset   time.Duration
	}{
		{"DAL1", 2 * time.Minute},
		{"DAL2", 10 * time.Minute},
		{"DAL0", 23*time.Hour + 55*time.Minute},
	}
	for i, expected := range want {
		got := provider.departures[i]
		if got.flight.Callsign != expected.callsign || got.offset != expected.offset {
			t.Errorf("departure %d = %s at %s, want %s at %s", i, got.flight.Callsign, got.offset,
				expected.callsign, expected.offset)
		}
	}
}

func TestScheduleTrafficProviderWaitsForPublishedTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.Local))
	provider := newScheduleTrafficProvider(BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{{
			Callsign: "DAL123", Origin: "KMSP", Destination: "KORD", AircraftType: "A320",
			ScheduledMinute: 14*60 + 5,
		}},
	}, 14*60, start)

	s := &Sim{State: &CommonState{DynamicState: DynamicState{SimTime: start}}}
	ac, delay, err := provider.createIFRDeparture(s, "KMSP", "12L")
	if err != nil {
		t.Fatalf("createIFRDeparture: %v", err)
	}
	if ac != nil {
		t.Fatal("created departure before its published time")
	}
	if delay != 5*time.Minute {
		t.Fatalf("delay = %s, want 5m", delay)
	}
}

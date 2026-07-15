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

func TestScheduleTrafficProviderOrdersArrivalsFromSelectedStartTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.Local))
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{Callsign: "DAL102", Origin: "KATL", Destination: "KMSP", ScheduledMinute: 14*60 + 12},
			{Callsign: "DAL101", Origin: "KORD", Destination: "KMSP", ScheduledMinute: 14*60 + 3},
			{Callsign: "DAL100", Origin: "KDEN", Destination: "KMSP", ScheduledMinute: 13*60 + 50},
			{Callsign: "DAL200", Origin: "KMSP", Destination: "KATL", ScheduledMinute: 14*60 + 1},
		},
	}

	provider := newScheduleTrafficProvider(schedule, 14*60, start)
	if len(provider.arrivals) != 3 {
		t.Fatalf("got %d arrivals, want 3", len(provider.arrivals))
	}

	want := []struct {
		callsign string
		offset   time.Duration
	}{
		{"DAL101", 3 * time.Minute},
		{"DAL102", 12 * time.Minute},
		{"DAL100", 23*time.Hour + 50*time.Minute},
	}

	for i, expected := range want {
		got := provider.arrivals[i]
		if got.flight.Callsign != expected.callsign || got.offset != expected.offset {
			t.Errorf(
				"arrival %d = %s at %s, want %s at %s",
				i,
				got.flight.Callsign,
				got.offset,
				expected.callsign,
				expected.offset,
			)
		}
	}
}

func TestScheduleTrafficProviderWaitsForPublishedArrivalTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.Local))
	provider := newScheduleTrafficProvider(BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{{
			Callsign:        "DAL321",
			Origin:          "KORD",
			Destination:     "KMSP",
			AircraftType:    "A320",
			ScheduledMinute: 14*60 + 7,
		}},
	}, 14*60, start)

	s := &Sim{
		State: &CommonState{
			DynamicState: DynamicState{
				SimTime: start,
			},
		},
	}

	ac, delay, err := provider.createInbound(
		s,
		"TEST",
		map[string]float32{"KMSP": 10},
		false,
	)
	if err != nil {
		t.Fatalf("createInbound: %v", err)
	}
	if ac != nil {
		t.Fatal("created arrival before its published time")
	}
	if delay != 7*time.Minute {
		t.Fatalf("delay = %s, want 7m", delay)
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

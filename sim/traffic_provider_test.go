// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

// publishedProviderTestSim builds the minimal Sim the published-traffic provider needs:
// the user-selected start time, the sim clock rewound for prespawn (which is
// when the provider is first built), and a "TEST" inbound flow that lands KMSP
// arrivals from the origins the tests use.
func publishedProviderTestSim(start Time) *Sim {
	return &Sim{
		startTime: start,
		State: &CommonState{
			DynamicState: DynamicState{
				SimTime: NewSimTime(start.Time().Add(-PrespawnDuration)),
				LaunchConfig: LaunchConfig{
					InboundFlowEnabled: map[string]map[string]bool{"TEST": {"KMSP": true}},
				},
			},
			InboundFlows: map[string]*av.InboundFlow{
				"TEST": {
					Arrivals: []av.Arrival{{
						Airlines: map[string][]av.ArrivalAirline{
							"KMSP": {
								{Airport: "KATL"},
								{Airport: "KORD"},
								{Airport: "KDEN"},
							},
						},
					}},
				},
			},
		},
	}
}

func TestPublishedTrafficProviderOrdersDeparturesFromSelectedStartTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{Callsign: "DAL2", Origin: "KMSP", Destination: "KATL", PublishedMinute: 14*60 + 10},
			{Callsign: "DAL1", Origin: "KMSP", Destination: "KORD", PublishedMinute: 14*60 + 2},
			{Callsign: "DAL0", Origin: "KMSP", Destination: "KDEN", PublishedMinute: 13*60 + 55},
			{Callsign: "DAL3", Origin: "KATL", Destination: "KMSP", PublishedMinute: 14*60 + 1},
		},
	}

	provider := newTimetableTrafficProvider(publishedProviderTestSim(start), timetable, 14*60, 100, 100)
	if len(provider.departures) != 3 {
		t.Fatalf("got %d departures, want 3", len(provider.departures))
	}

	// DAL0's published time is a few minutes before the selected start, so it
	// spawns during the prespawn warm-up rather than wrapping to the end of
	// the day.
	want := []struct {
		callsign string
		spawn    Time
	}{
		{"DAL0", start.Add(-5 * time.Minute)},
		{"DAL1", start.Add(2 * time.Minute)},
		{"DAL2", start.Add(10 * time.Minute)},
	}
	for i, expected := range want {
		got := provider.departures[i]
		if got.flight.Callsign != expected.callsign || got.spawn != expected.spawn {
			t.Errorf("departure %d = %s at %s, want %s at %s", i, got.flight.Callsign,
				got.spawn.Time(), expected.callsign, expected.spawn.Time())
		}
	}
}

func TestPublishedTrafficProviderOrdersArrivalsFromSelectedStartTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{Callsign: "DAL102", Origin: "KATL", Destination: "KMSP", PublishedMinute: 14*60 + 12},
			{Callsign: "DAL101", Origin: "KORD", Destination: "KMSP", PublishedMinute: 14*60 + 3},
			{Callsign: "DAL100", Origin: "KDEN", Destination: "KMSP", PublishedMinute: 13*60 + 50},
			{Callsign: "DAL200", Origin: "KMSP", Destination: "KATL", PublishedMinute: 14*60 + 1},
		},
	}

	provider := newTimetableTrafficProvider(publishedProviderTestSim(start), timetable, 14*60, 100, 100)
	if len(provider.arrivals) != 3 {
		t.Fatalf("got %d arrivals, want 3", len(provider.arrivals))
	}

	// Arrivals spawn flightSpawnLead ahead of their published arrival times;
	// DAL100's published time is before the selected start, so it spawns
	// during the prespawn warm-up.
	want := []struct {
		callsign string
		spawn    Time
	}{
		{"DAL100", start.Add(-10*time.Minute - flightSpawnLead)},
		{"DAL101", start.Add(3*time.Minute - flightSpawnLead)},
		{"DAL102", start.Add(12*time.Minute - flightSpawnLead)},
	}

	for i, expected := range want {
		got := provider.arrivals[i]
		if got.flight.Callsign != expected.callsign || got.spawn != expected.spawn {
			t.Errorf("arrival %d = %s at %s, want %s at %s", i, got.flight.Callsign,
				got.spawn.Time(), expected.callsign, expected.spawn.Time())
		}
		if got.group != "TEST" {
			t.Errorf("arrival %d group = %q, want TEST", i, got.group)
		}
	}
}

func TestPublishedTrafficProviderWaitsForPublishedArrivalTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(start)
	provider := newTimetableTrafficProvider(s, Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{{
			Callsign:        "DAL321",
			Origin:          "KORD",
			Destination:     "KMSP",
			AircraftType:    "A320",
			PublishedMinute: 14*60 + 7,
		}},
	}, 14*60, 100, 100)

	// The arrival is due to spawn flightSpawnLead before its published 14:07
	// arrival, i.e. at 13:52; ten minutes before that, nothing should be
	// created yet.
	s.State.SimTime = start.Add(-8*time.Minute - flightSpawnLead)

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
	if delay != 15*time.Minute {
		t.Fatalf("delay = %s, want 15m", delay)
	}
}

func TestPublishedTrafficProviderWaitsForPublishedTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(start)
	provider := newTimetableTrafficProvider(s, Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{{
			Callsign: "DAL123", Origin: "KMSP", Destination: "KORD", AircraftType: "A320",
			PublishedMinute: 14*60 + 5,
		}},
	}, 14*60, 100, 100)

	// A timetable's published departure times are pushback, so the departure
	// spawns at its published 14:05 time exactly.
	s.State.SimTime = start

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

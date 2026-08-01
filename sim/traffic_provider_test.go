// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

// publishedProviderTestSim builds the minimal Sim the published-traffic provider needs:
// the user-selected start time, the sim clock rewound for prespawn (which is
// when the provider is first built), and a "TEST" inbound flow that lands KMSP
// arrivals from the origins the tests use.
//
// The flow's rate is zero on purpose: that is how much traffic the scenario's own
// generator makes and has no bearing on published arrivals, which arrive when the
// data says. Listing the flow at all is what makes it a way into KMSP, so it is
// enabled here exactly as MakeLaunchConfig would leave it.
func publishedProviderTestSim(start Time) *Sim {
	return &Sim{
		StartTime: start,
		State: &CommonState{
			DynamicState: DynamicState{
				SimTime: NewSimTime(start.Time().Add(-PrespawnDuration)),
				LaunchConfig: LaunchConfig{
					InboundFlowRates:   map[string]map[string]float32{"TEST": {"KMSP": 0}},
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
		if got.placement.group != "TEST" {
			t.Errorf("arrival %d group = %q, want TEST", i, got.placement.group)
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

// testFlight is one published flight on a fixed day, named the way the flight
// data names them: the facility airport it operates at and the airport at the
// other end.
func testFlight(callsign, airport, other string, departure bool, hour, minute int) av.Flight {
	return av.Flight{
		Airport:      airport,
		Callsign:     callsign,
		Other:        other,
		AircraftType: "C560",
		Day:          av.FlightDataDayNumber(time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)),
		Minute:       hour*60 + minute,
		Departure:    departure,
	}
}

func flightNames(flights []av.Flight) []string {
	names := make([]string, len(flights))
	for i, f := range flights {
		direction := "arrives"
		if f.Departure {
			direction = "departs"
		}
		names[i] = fmt.Sprintf("%s %s %s", f.Callsign, direction, f.Airport)
	}
	return names
}

// A flight whose ADS-B track came in pieces is recorded once per piece, so it
// must be flown once rather than as several aircraft sharing a callsign.
func TestDropRepeatedRecords(t *testing.T) {
	for _, test := range []struct {
		name    string
		flights []av.Flight
		want    []string
	}{
		{
			name: "the same departure recorded twice a minute apart",
			flights: []av.Flight{
				testFlight("JTL881", "KORD", "KBMG", true, 11, 33),
				testFlight("JTL881", "KORD", "KBMG", true, 11, 34),
			},
			want: []string{"JTL881 departs KORD"},
		},
		{
			name: "repeats that disagree about the other airport are still one flight",
			flights: []av.Flight{
				testFlight("UAL219", "KORD", "PHNL", true, 14, 55),
				testFlight("UAL219", "KORD", "KHAF", true, 14, 55),
			},
			want: []string{"UAL219 departs KORD"},
		},
		{
			name: "a turnaround is two operations, not a repeat",
			flights: []av.Flight{
				testFlight("AAL2151", "KMSP", "KCLT", false, 17, 7),
				testFlight("AAL2151", "KMSP", "KCLT", true, 17, 47),
			},
			want: []string{"AAL2151 arrives KMSP", "AAL2151 departs KMSP"},
		},
		{
			name: "the same operation hours later is a second flight",
			flights: []av.Flight{
				testFlight("N435FG", "KMDW", "KJAX", true, 8, 0),
				testFlight("N435FG", "KMDW", "KJAX", true, 18, 6),
			},
			want: []string{"N435FG departs KMDW", "N435FG departs KMDW"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kept, dropped := dropRepeatedRecords(test.flights)
			if got := flightNames(kept); !slices.Equal(got, test.want) {
				t.Errorf("kept %v, want %v", got, test.want)
			}
			if want := len(test.flights) - len(test.want); dropped != want {
				t.Errorf("dropped %d, want %d", dropped, want)
			}
		})
	}
}

// A flight between two of a facility's airports is recorded at both ends, so it
// must be flown once--as its departure--rather than as two aircraft sharing a
// callsign.
func TestDropReturnedLegs(t *testing.T) {
	for _, test := range []struct {
		name    string
		flights []av.Flight
		want    []string
	}{
		{
			name: "leg recorded at both ends flies as its departure",
			flights: []av.Flight{
				testFlight("N5367H", "KARR", "KDPA", true, 20, 43),
				testFlight("N5367H", "KDPA", "KARR", false, 20, 50),
			},
			want: []string{"N5367H departs KARR"},
		},
		{
			name: "arrival from outside the facility is untouched",
			flights: []av.Flight{
				testFlight("DAL1", "KMSP", "KATL", false, 14, 12),
			},
			want: []string{"DAL1 arrives KMSP"},
		},
		{
			name: "each leg of a round trip is recognized on its own",
			flights: []av.Flight{
				testFlight("N37RD", "KLOT", "KARR", true, 16, 33),
				testFlight("N37RD", "KARR", "KLOT", false, 16, 45),
				testFlight("N37RD", "KARR", "KLOT", true, 17, 21),
				testFlight("N37RD", "KLOT", "KARR", false, 17, 32),
			},
			want: []string{"N37RD departs KLOT", "N37RD departs KARR"},
		},
		{
			name: "the same callsign flown again hours later is a separate flight",
			flights: []av.Flight{
				testFlight("JLG426", "KMDW", "KDPA", true, 10, 0),
				testFlight("JLG426", "KDPA", "KMDW", false, 16, 0),
			},
			want: []string{"JLG426 departs KMDW", "JLG426 arrives KDPA"},
		},
		{
			name: "a departure elsewhere doesn't stand in for the arrival's own",
			flights: []av.Flight{
				testFlight("EJA610", "KORD", "KMDW", true, 2, 28),
				testFlight("EJA610", "KDPA", "KARR", false, 2, 44),
			},
			want: []string{"EJA610 departs KORD", "EJA610 arrives KDPA"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kept, dropped := dropReturnedLegs(test.flights)
			if got := flightNames(kept); !slices.Equal(got, test.want) {
				t.Errorf("kept %v, want %v", got, test.want)
			}
			if want := len(test.flights) - len(test.want); dropped != want {
				t.Errorf("dropped %d, want %d", dropped, want)
			}
		})
	}
}

// A sim that has been saved and reloaded must keep flying its published
// traffic. The start time anchors a timetable's daily cycle, so if it doesn't
// survive the round trip every flight lands in the distant past, is judged
// already missed, and the sim runs with no traffic at all and no complaint.
func TestPublishedTrafficSurvivesSaveAndReload(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{Callsign: "DAL1", Origin: "KMSP", Destination: "KORD", PublishedMinute: 14*60 + 2},
			{Callsign: "DAL2", Origin: "KATL", Destination: "KMSP", PublishedMinute: 14*60 + 12},
		},
	}

	s := publishedProviderTestSim(start)
	before := newTimetableTrafficProvider(s, timetable, 14*60, 100, 100)
	if len(before.departures) != 1 || len(before.arrivals) != 1 {
		t.Fatalf("before reload: %d departures/%d arrivals, want 1/1",
			len(before.departures), len(before.arrivals))
	}

	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded Sim
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reloaded.StartTime != s.StartTime {
		t.Fatalf("StartTime = %s after reload, want %s", reloaded.StartTime.Time(), s.StartTime.Time())
	}

	after := newTimetableTrafficProvider(&reloaded, timetable, 14*60, 100, 100)
	if len(after.departures) != len(before.departures) || len(after.arrivals) != len(before.arrivals) {
		t.Fatalf("after reload: %d departures/%d arrivals, want %d/%d",
			len(after.departures), len(after.arrivals), len(before.departures), len(before.arrivals))
	}
	for i := range before.departures {
		if after.departures[i].spawn != before.departures[i].spawn {
			t.Errorf("departure %s spawns at %s after reload, want %s", before.departures[i].flight.Callsign,
				after.departures[i].spawn.Time(), before.departures[i].spawn.Time())
		}
	}
	for i := range before.arrivals {
		if after.arrivals[i].spawn != before.arrivals[i].spawn {
			t.Errorf("arrival %s spawns at %s after reload, want %s", before.arrivals[i].flight.Callsign,
				after.arrivals[i].spawn.Time(), before.arrivals[i].spawn.Time())
		}
	}
}

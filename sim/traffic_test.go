// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// publishedProviderTestSim builds the minimal Sim the published-traffic provider needs:
// the user-selected start time, the sim clock rewound for prespawn (which is
// when the provider is first built), a "TEST" inbound flow into KMSP, and two
// departure runways there with a gate apiece. The aviation database is swapped
// for a synthetic one with no city-pair routes, so every flight places through
// the great-circle gate and the tests stay independent of the real route data.
//
// The flow's rate is zero on purpose: that is how much traffic the scenario's own
// generator makes and has no bearing on published arrivals, which arrive when the
// data says. Listing the flow at all is what makes it a way into KMSP, so it is
// enabled here exactly as MakeLaunchConfig would leave it.
func publishedProviderTestSim(t *testing.T, start Time) *Sim {
	oldDB := av.DB
	av.DB = &av.StaticDatabase{
		Airports: map[string]av.FAAAirport{
			"KMSP": {Id: "KMSP", Location: math.Point2LL{-93.2, 44.9}},
			"KATL": {Id: "KATL", Location: math.Point2LL{-84.4, 33.6}},
			"KORD": {Id: "KORD", Location: math.Point2LL{-87.9, 42.0}},
			"KDEN": {Id: "KDEN", Location: math.Point2LL{-104.7, 39.9}},
		},
		Fixes: map[string]av.Fix{
			"DEPSE": {Id: "DEPSE", Location: math.Point2LL{-92.0, 44.0}},
			"DEPSW": {Id: "DEPSW", Location: math.Point2LL{-94.5, 44.0}},
		},
	}
	t.Cleanup(func() { av.DB = oldDB })

	return &Sim{
		StartTime: start,
		State: &CommonState{
			NmPerLongitude: 45,
			DynamicState: DynamicState{
				SimTime: NewSimTime(start.Time().Add(-PrespawnDuration)),
				LaunchConfig: LaunchConfig{
					// The tests all start at 14:00, which is where their
					// timetables' days start too.
					TimetableStartMinute:        14 * 60,
					PublishedArrivalRateScale:   1,
					PublishedDepartureRateScale: 1,
					InboundFlowRates:            map[string]map[string]float32{"TEST": {"KMSP": 0}},
					InboundFlowEnabled:          map[string]map[string]bool{"TEST": {"KMSP": true}},
					DepartureEnabled: map[string]map[av.RunwayID]map[string]bool{
						"KMSP": {"12L": {"": true}, "30R": {"": true}},
					},
				},
			},
			Airports: map[string]*av.Airport{
				"KMSP": {
					DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
						"12L": {"DEPSE": {{}}},
						"30R": {"DEPSW": {{}}},
					},
				},
			},
			DepartureRunways: []DepartureRunway{
				{Airport: "KMSP", Runway: "12L"},
				{Airport: "KMSP", Runway: "30R"},
			},
			InboundFlows: map[string]*av.InboundFlow{
				// Two gates, so that each of the origins above has one
				// pointing plausibly its way.
				"TEST": {
					Arrivals: []av.Arrival{
						{
							Airports:  []string{"KMSP"},
							Waypoints: av.WaypointArray{{Fix: "GATSE", Location: math.Point2LL{-92.5, 44.0}}},
						},
						{
							Airports:  []string{"KMSP"},
							Waypoints: av.WaypointArray{{Fix: "GATWE", Location: math.Point2LL{-95.5, 44.7}}},
						},
					},
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

	provider := newTimetableTrafficProvider(publishedProviderTestSim(t, start), timetable)
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

	provider := newTimetableTrafficProvider(publishedProviderTestSim(t, start), timetable)
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
	s := publishedProviderTestSim(t, start)
	provider := newTimetableTrafficProvider(s, Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{{
			Callsign:        "DAL321",
			Origin:          "KORD",
			Destination:     "KMSP",
			AircraftType:    "A320",
			PublishedMinute: 14*60 + 7,
		}},
	})

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
	s := publishedProviderTestSim(t, start)
	provider := newTimetableTrafficProvider(s, Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{{
			Callsign: "DAL123", Origin: "KMSP", Destination: "KORD", AircraftType: "A320",
			PublishedMinute: 14*60 + 5,
		}},
	})

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

// The runways at an airport hold different gates, so a runway steps over the
// flights that leave through another's rather than dropping them. JFK departing
// 13L/R is the case that caught this: the north and east gates are off 13L and
// the south and west ones off 13R, so whichever runway asked first was dropping
// the other's flights.
func TestPublishedDeparturesWaitForTheirRunway(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.SimTime = start

	p := &publishedTrafficProvider{departures: []publishedFlight{
		{flight: testFlight("DAL1", "KMSP", "KORD", true, 14, 0), spawn: start},
		{flight: testFlight("DAL2", "KMSP", "KDEN", true, 14, 1), spawn: start},
	}}

	// KORD lies out the southeast gate, which only 12L launches.
	if got := p.nextDepartureFor(s, "KMSP", "12L"); got != 0 {
		t.Errorf("12L takes departure %d, want DAL1 to KORD", got)
	}
	// 30R holds the southwest gate, so DAL1 isn't its to fly; it takes DAL2
	// rather than dropping DAL1 to get to it.
	if got := p.nextDepartureFor(s, "KMSP", "30R"); got != 1 {
		t.Errorf("30R takes departure %d, want DAL2 to KDEN", got)
	}
	if len(p.departures) != 2 {
		t.Errorf("%d departures left in the queue, want both still waiting", len(p.departures))
	}
}

// A flight no runway at the airport can fly is dropped when it comes due, which
// is when the drop is worth reporting; leaving it in the queue any longer would
// stall the departures behind it.
func TestPublishedDeparturesDropWhatNoRunwayFlies(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	// Due north of KMSP: neither gate heads anywhere near it.
	av.DB.Airports["KGFK"] = av.FAAAirport{Id: "KGFK", Location: math.Point2LL{-93.2, 48.0}}

	p := &publishedTrafficProvider{departures: []publishedFlight{
		{flight: testFlight("DAL1", "KMSP", "KGFK", true, 14, 0), spawn: start},
	}}

	s.State.SimTime = NewSimTime(start.Time().Add(-time.Minute))
	if got := p.nextDepartureFor(s, "KMSP", "12L"); got != -1 {
		t.Errorf("12L takes departure %d, want none", got)
	}
	if len(p.departures) != 1 {
		t.Error("dropped a departure before its published time")
	}

	s.State.SimTime = start
	if got := p.nextDepartureFor(s, "KMSP", "12L"); got != -1 {
		t.Errorf("12L takes departure %d, want none", got)
	}
	if len(p.departures) != 0 {
		t.Error("kept a departure no runway can fly")
	}
}

// The rate scale draws published flights in towards the start time rather than
// leaving any of them out: at 2x, a flight twenty minutes into the data operates
// ten minutes into the sim. Each direction is scaled on its own.
func TestPublishedTrafficProviderScalesRates(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.LaunchConfig.PublishedDepartureRateScale = 2

	p := newHistoricalTrafficProvider(s, []av.Flight{
		testFlight("DAL1", "KMSP", "KORD", true, 14, 20),
		testFlight("DAL2", "KMSP", "KATL", true, 14, 40),
		testFlight("DAL3", "KMSP", "KDEN", false, 14, 30),
	})

	// Historical flights of both kinds spawn flightSpawnLead ahead of the time
	// they operate at.
	wantDepartures := []struct {
		callsign string
		spawn    Time
	}{
		{"DAL1", start.Add(10*time.Minute - flightSpawnLead)},
		{"DAL2", start.Add(20*time.Minute - flightSpawnLead)},
	}
	if len(p.departures) != len(wantDepartures) {
		t.Fatalf("got %d departures, want %d", len(p.departures), len(wantDepartures))
	}
	for i, expected := range wantDepartures {
		if got := p.departures[i]; got.flight.Callsign != expected.callsign || got.spawn != expected.spawn {
			t.Errorf("departure %d = %s at %s, want %s at %s", i, got.flight.Callsign,
				got.spawn.Time(), expected.callsign, expected.spawn.Time())
		}
	}

	if len(p.arrivals) != 1 {
		t.Fatalf("got %d arrivals, want 1", len(p.arrivals))
	}
	if want := start.Add(30*time.Minute - flightSpawnLead); p.arrivals[0].spawn != want {
		t.Errorf("arrival spawns at %s with arrivals at 1x, want %s",
			p.arrivals[0].spawn.Time(), want.Time())
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

	s := publishedProviderTestSim(t, start)
	before := newTimetableTrafficProvider(s, timetable)
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

	after := newTimetableTrafficProvider(&reloaded, timetable)
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

///////////////////////////////////////////////////////////////////////////
// Traffic preview

// previewStart is the start time the preview tests select; testFlight's flights
// are on the same day.
var previewStart = time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC)

// previewLaunchConfig flies historical traffic to and from KMSP with every flow
// the preview consults switched on. The rates are zero because they say how much
// traffic the scenario's own generator would make, which has no bearing on
// published traffic; listing the flows at all is what makes them ways in and out.
func previewLaunchConfig() *LaunchConfig {
	return &LaunchConfig{
		TrafficSource:               TrafficSourceHistorical,
		PublishedArrivalRateScale:   1,
		PublishedDepartureRateScale: 1,
		DepartureRates:              map[string]map[av.RunwayID]map[string]float32{"KMSP": {"30L": {"": 0}}},
		DepartureEnabled:            map[string]map[av.RunwayID]map[string]bool{"KMSP": {"30L": {"": true}}},
		InboundFlowRates:            map[string]map[string]float32{"TEST": {"KMSP": 0}},
		InboundFlowEnabled:          map[string]map[string]bool{"TEST": {"KMSP": true}},
	}
}

// countsByMinute reports the minutes an operation was counted in, leaving out
// the empty ones so that a test can name only what it expects.
func countsByMinute(counts []uint16) map[int]int {
	byMinute := make(map[int]int)
	for minute, count := range counts {
		if count > 0 {
			byMinute[minute] = int(count)
		}
	}
	return byMinute
}

func totalCount(counts []uint16) int {
	total := 0
	for _, count := range counts {
		total += int(count)
	}
	return total
}

// lastCountedMinute is the last minute anything was counted in, or -1 if
// nothing was.
func lastCountedMinute(counts []uint16) int {
	last := -1
	for minute, count := range counts {
		if count > 0 {
			last = minute
		}
	}
	return last
}

func TestTrafficCountsBinsOperationsByMinute(t *testing.T) {
	pad := int(TrafficCountsPad / time.Minute)
	flights := []av.Flight{
		testFlight("DAL1", "KMSP", "KJFK", true, 12, 0),  // before the preview reaches back
		testFlight("DAL2", "KMSP", "KATL", true, 13, 45), // in the pad ahead of the start
		testFlight("DAL3", "KMSP", "KORD", true, 14, 0),  // at the start
		testFlight("DAL4", "KMSP", "KDEN", false, 15, 30),
		testFlight("DAL5", "KMSP", "KBOS", true, 17, 0), // past the end
	}

	departures, arrivals, err := TrafficCounts(previewLaunchConfig(), previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if len(departures) != TrafficCountsMinutes || len(arrivals) != TrafficCountsMinutes {
		t.Fatalf("got %d/%d counts, want %d", len(departures), len(arrivals), TrafficCountsMinutes)
	}

	if got, want := countsByMinute(departures), map[int]int{pad - 15: 1, pad: 1}; !maps.Equal(got, want) {
		t.Errorf("departures at %v, want %v", got, want)
	}
	if got, want := countsByMinute(arrivals), map[int]int{pad + 90: 1}; !maps.Equal(got, want) {
		t.Errorf("arrivals at %v, want %v", got, want)
	}
}

// The preview has to leave out what the sim will: an airport with all of its
// flows switched off flies nothing, whatever the data says it did.
func TestTrafficCountsSkipsDisabledFlows(t *testing.T) {
	flights := []av.Flight{
		testFlight("DAL1", "KMSP", "KORD", true, 14, 10),
		testFlight("DAL2", "KMSP", "KDEN", false, 14, 20),
	}

	noDepartures := previewLaunchConfig()
	noDepartures.DepartureEnabled["KMSP"]["30L"][""] = false
	departures, arrivals, err := TrafficCounts(noDepartures, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 0 {
		t.Errorf("%d departures with the runway off, want 0", got)
	}
	if got := totalCount(arrivals); got != 1 {
		t.Errorf("%d arrivals with the runway off, want 1", got)
	}

	noArrivals := previewLaunchConfig()
	noArrivals.InboundFlowEnabled["TEST"]["KMSP"] = false
	departures, arrivals, err = TrafficCounts(noArrivals, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 1 {
		t.Errorf("%d departures with the flow off, want 1", got)
	}
	if got := totalCount(arrivals); got != 0 {
		t.Errorf("%d arrivals with the flow off, want 0", got)
	}
}

// A rate scale doesn't pick and choose among the flights: they all fly, in less
// of the sim's clock. So the preview counts the same operations either way, and
// scaling up is visible in how far into the window the last one falls.
func TestTrafficCountsHonorsPublishedRateScales(t *testing.T) {
	var flights []av.Flight
	for i := range 40 {
		flights = append(flights, testFlight(fmt.Sprintf("DAL%d", i), "KMSP", "KORD", true, 14, 2*i))
		flights = append(flights, testFlight(fmt.Sprintf("AAL%d", i), "KMSP", "KDEN", false, 14, 2*i+1))
	}
	pad := int(TrafficCountsPad / time.Minute)

	all := previewLaunchConfig()
	departures, arrivals, err := TrafficCounts(all, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 40 {
		t.Fatalf("%d departures at 1x, want 40", got)
	}
	if got := totalCount(arrivals); got != 40 {
		t.Fatalf("%d arrivals at 1x, want 40", got)
	}
	if got, want := lastCountedMinute(departures), pad+78; got != want {
		t.Errorf("last departure at minute %d at 1x, want %d", got, want)
	}

	none := previewLaunchConfig()
	none.PublishedDepartureRateScale = 0
	departures, arrivals, err = TrafficCounts(none, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 0 {
		t.Errorf("%d departures at 0x, want 0", got)
	}
	if got := totalCount(arrivals); got != 40 {
		t.Errorf("%d arrivals with departures at 0x, want 40", got)
	}

	fast := previewLaunchConfig()
	fast.PublishedDepartureRateScale = 2
	departures, arrivals, err = TrafficCounts(fast, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 40 {
		t.Errorf("%d departures at 2x, want 40", got)
	}
	if got, want := lastCountedMinute(departures), pad+39; got != want {
		t.Errorf("last departure at minute %d at 2x, want %d", got, want)
	}
	if got, want := lastCountedMinute(arrivals), pad+79; got != want {
		t.Errorf("last arrival at minute %d with departures at 2x, want %d", got, want)
	}
}

// The dedup the provider does has to happen here too, or the preview promises
// traffic the sim then merges away.
func TestTrafficCountsMergesRepeatedRecords(t *testing.T) {
	flights := []av.Flight{
		testFlight("DAL1", "KMSP", "KATL", true, 14, 10),
		testFlight("DAL1", "KMSP", "KORD", true, 14, 12),
	}

	departures, _, err := TrafficCounts(previewLaunchConfig(), previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 1 {
		t.Errorf("%d departures, want 1: the two records are one flight", got)
	}
}

// Scenario traffic comes at the rates the user sets, so there is nothing here to
// preview and saying so beats returning an empty plot that reads as "no traffic".
func TestTrafficCountsRejectsScenarioTraffic(t *testing.T) {
	lc := previewLaunchConfig()
	lc.TrafficSource = TrafficSourceScenario
	if _, _, err := TrafficCounts(lc, previewStart, nil); err == nil {
		t.Error("previewing scenario traffic succeeded, want an error")
	}
}

// Traffic the scenario flies only for realism is not the user's to work, so a
// preview of what a start time brings them leaves it out.
func TestTrafficCountsSkipsBackgroundTraffic(t *testing.T) {
	flights := []av.Flight{
		testFlight("DAL1", "KMSP", "KORD", true, 14, 10),
		testFlight("DAL2", "KMSP", "KDEN", false, 14, 20),
	}

	backgroundDepartures := previewLaunchConfig()
	backgroundDepartures.DepartureBackground = map[string]map[av.RunwayID]map[string]bool{
		"KMSP": {"30L": {"": true}},
	}
	departures, arrivals, err := TrafficCounts(backgroundDepartures, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 0 {
		t.Errorf("%d departures from a background flow, want 0", got)
	}
	if got := totalCount(arrivals); got != 1 {
		t.Errorf("%d arrivals, want 1: only the departures are background", got)
	}

	backgroundArrivals := previewLaunchConfig()
	backgroundArrivals.InboundFlowBackground = map[string]map[string]bool{"TEST": {"KMSP": true}}
	departures, arrivals, err = TrafficCounts(backgroundArrivals, previewStart, flights)
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(departures); got != 1 {
		t.Errorf("%d departures, want 1: only the arrivals are background", got)
	}
	if got := totalCount(arrivals); got != 0 {
		t.Errorf("%d arrivals into a background flow, want 0", got)
	}
}

// An airport a background flow serves is still the user's if some other flow
// brings them traffic there.
func TestTrafficCountsKeepsAirportsWithOneWorkedFlow(t *testing.T) {
	lc := previewLaunchConfig()
	lc.InboundFlowRates["QUIET"] = map[string]float32{"KMSP": 0}
	lc.InboundFlowEnabled["QUIET"] = map[string]bool{"KMSP": true}
	lc.InboundFlowBackground = map[string]map[string]bool{"QUIET": {"KMSP": true}}

	_, arrivals, err := TrafficCounts(lc, previewStart,
		[]av.Flight{testFlight("DAL2", "KMSP", "KDEN", false, 14, 20)})
	if err != nil {
		t.Fatalf("TrafficCounts: %v", err)
	}
	if got := totalCount(arrivals); got != 1 {
		t.Errorf("%d arrivals, want 1: TEST still lands the user's traffic at KMSP", got)
	}
}

///////////////////////////////////////////////////////////////////////////
// Background traffic classification

// The classification tests below work a facility whose only human position is
// HUM; VIRT is a position the facility defines but nobody signs in to.
const (
	humanPosition   = "HUM"
	virtualPosition = "VIRT"
)

func testControlPositions() map[TCP]*av.Controller {
	return map[TCP]*av.Controller{
		humanPosition:   {Position: humanPosition},
		virtualPosition: {Position: virtualPosition},
	}
}

func testControllerConfiguration(inbound map[string]TCP) *ControllerConfiguration {
	return &ControllerConfiguration{
		DefaultConsolidation: PositionConsolidation{humanPosition: nil},
		InboundAssignments:   inbound,
	}
}

// testWaypoints runs the route through the parser the scenarios use, so that
// "/ho" and "/hoHUM" mean here what they mean there.
func testWaypoints(t *testing.T, route string) av.WaypointArray {
	t.Helper()
	encoded, err := json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal %q: %v", route, err)
	}
	var waypoints av.WaypointArray
	if err := json.Unmarshal(encoded, &waypoints); err != nil {
		t.Fatalf("parse waypoints %q: %v", route, err)
	}
	return waypoints
}

func TestMarkBackgroundDepartures(t *testing.T) {
	for _, test := range []struct {
		name              string
		airportController av.ControlPosition
		routeController   av.ControlPosition
		handoffController av.ControlPosition
		route             string
		want              bool
	}{
		{
			name:              "flown start to finish by a virtual controller",
			airportController: virtualPosition,
			route:             "FIXA FIXB",
			want:              true,
		},
		{
			name:              "virtual until it is handed to a human",
			airportController: virtualPosition,
			handoffController: humanPosition,
			route:             "FIXA FIXB/ho",
			want:              false,
		},
		{
			name:              "handed off, but only to another virtual controller",
			airportController: virtualPosition,
			handoffController: virtualPosition,
			route:             "FIXA FIXB/ho",
			want:              true,
		},
		{
			name:              "a route naming the human position outright",
			airportController: virtualPosition,
			route:             "FIXA FIXB/ho" + humanPosition,
			want:              false,
		},
		{
			name:              "a route naming another virtual position",
			airportController: virtualPosition,
			route:             "FIXA FIXB/ho" + virtualPosition,
			want:              true,
		},
		{
			name:  "nobody takes it from the human who releases it",
			route: "FIXA FIXB",
			want:  false,
		},
		{
			name:            "the route, rather than the airport, gives it away",
			routeController: virtualPosition,
			route:           "FIXA FIXB",
			want:            true,
		},
		{
			// A departure controller the facility never defines is no reason to
			// keep the departure from its human, which is how the sim reads it.
			name:              "an unknown departure controller",
			airportController: "NOPE",
			route:             "FIXA FIXB",
			want:              false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			airports := map[string]*av.Airport{
				"KTST": {
					DepartureController: test.airportController,
					DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
						"30L": {"EXIT": {{
							DepartureController: test.routeController,
							HandoffController:   test.handoffController,
							Waypoints:           testWaypoints(t, test.route),
						}}},
					},
				},
			}
			lc := &LaunchConfig{
				DepartureRates: map[string]map[av.RunwayID]map[string]float32{"KTST": {"30L": {"": 10}}},
			}
			MarkBackgroundTraffic(airports, nil, testControllerConfiguration(nil), testControlPositions(), lc)

			if got := lc.DepartureIsBackground("KTST", "30L", ""); got != test.want {
				t.Errorf("background = %v, want %v", got, test.want)
			}
		})
	}
}

// Only the exits in a category decide whether that category is background; a
// worked route in another category is somebody else's traffic.
func TestMarkBackgroundDeparturesByCategory(t *testing.T) {
	airports := map[string]*av.Airport{
		"KTST": {
			DepartureController: virtualPosition,
			ExitCategories:      map[av.ExitID]string{"NORTH": "North", "SOUTH": "South"},
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"30L": {
					"NORTH": {{HandoffController: humanPosition, Waypoints: testWaypoints(t, "FIXA FIXB/ho")}},
					"SOUTH": {{Waypoints: testWaypoints(t, "FIXA FIXB")}},
				},
			},
		},
	}
	lc := &LaunchConfig{
		DepartureRates: map[string]map[av.RunwayID]map[string]float32{
			"KTST": {"30L": {"North": 10, "South": 10}},
		},
	}
	MarkBackgroundTraffic(airports, nil, testControllerConfiguration(nil), testControlPositions(), lc)

	if lc.DepartureIsBackground("KTST", "30L", "North") {
		t.Error("the North category is handed to a human; it is not background")
	}
	if !lc.DepartureIsBackground("KTST", "30L", "South") {
		t.Error("the South category never leaves a virtual controller; it is background")
	}
}

// A runway with no route to judge is left alone: guessing wrong here hides
// traffic the user will have to work.
func TestMarkBackgroundDeparturesLeavesUnjudgedAlone(t *testing.T) {
	airports := map[string]*av.Airport{"KTST": {DepartureController: virtualPosition}}
	lc := &LaunchConfig{
		DepartureRates: map[string]map[av.RunwayID]map[string]float32{"KTST": {"30L": {"": 10}}},
	}
	MarkBackgroundTraffic(airports, nil, testControllerConfiguration(nil), testControlPositions(), lc)

	if lc.DepartureIsBackground("KTST", "30L", "") {
		t.Error("a runway with no departure routes was called background")
	}
}

func TestMarkBackgroundInboundFlows(t *testing.T) {
	for _, test := range []struct {
		name              string
		initialController av.ControlPosition
		assignment        TCP
		route             string
		want              bool
	}{
		{
			name:              "worked from the moment it spawns",
			initialController: humanPosition,
			route:             "FIXA FIXB",
			want:              false,
		},
		{
			name:              "handed to whoever works the flow",
			initialController: virtualPosition,
			assignment:        humanPosition,
			route:             "FIXA FIXB/ho",
			want:              false,
		},
		{
			name:              "handed on, but the flow is assigned to a virtual controller",
			initialController: virtualPosition,
			assignment:        virtualPosition,
			route:             "FIXA FIXB/ho",
			want:              true,
		},
		{
			name:              "never handed anywhere",
			initialController: virtualPosition,
			route:             "FIXA FIXB",
			want:              true,
		},
		{
			name:              "handed to the human by name",
			initialController: virtualPosition,
			route:             "FIXA FIXB/ho" + humanPosition,
			want:              false,
		},
		{
			name:              "handed between virtual sectors only",
			initialController: virtualPosition,
			route:             "FIXA FIXB/ho" + virtualPosition,
			want:              true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			flows := map[string]*av.InboundFlow{
				"FLOW": {Arrivals: []av.Arrival{{
					InitialController: test.initialController,
					Airports:          []string{"KTST"},
					Waypoints:         testWaypoints(t, test.route),
				}}},
			}
			lc := &LaunchConfig{
				InboundFlowRates: map[string]map[string]float32{"FLOW": {"KTST": 10}},
			}
			cc := testControllerConfiguration(map[string]TCP{"FLOW": test.assignment})
			MarkBackgroundTraffic(nil, flows, cc, testControlPositions(), lc)

			if got := lc.InboundFlowIsBackground("FLOW", "KTST"); got != test.want {
				t.Errorf("background = %v, want %v", got, test.want)
			}
		})
	}
}

// Overflights ride the same flows and are judged the same way; a flow that
// brings none is not background for them.
func TestMarkBackgroundOverflights(t *testing.T) {
	flows := map[string]*av.InboundFlow{
		"OVER": {Overflights: []av.Overflight{{
			InitialController: virtualPosition,
			Waypoints:         testWaypoints(t, "FIXA FIXB"),
		}}},
		"NONE": {Arrivals: []av.Arrival{{
			InitialController: humanPosition,
			Airports:          []string{"KTST"},
			Waypoints:         testWaypoints(t, "FIXA FIXB"),
		}}},
	}
	lc := &LaunchConfig{
		InboundFlowRates: map[string]map[string]float32{
			"OVER": {"overflights": 5},
			"NONE": {"overflights": 5},
		},
	}
	MarkBackgroundTraffic(nil, flows, testControllerConfiguration(nil), testControlPositions(), lc)

	if !lc.InboundFlowIsBackground("OVER", "overflights") {
		t.Error("overflights that stay with a virtual controller are background")
	}
	if lc.InboundFlowIsBackground("NONE", "overflights") {
		t.Error("a flow with no overflights has none to call background")
	}
}

// An arrival that doesn't serve the airport says nothing about it.
func TestMarkBackgroundInboundLeavesUnservedAirportAlone(t *testing.T) {
	flows := map[string]*av.InboundFlow{
		"FLOW": {Arrivals: []av.Arrival{{
			InitialController: virtualPosition,
			Airports:          []string{"KOTH"},
			Waypoints:         testWaypoints(t, "FIXA FIXB"),
		}}},
	}
	lc := &LaunchConfig{
		InboundFlowRates: map[string]map[string]float32{"FLOW": {"KTST": 10}},
	}
	MarkBackgroundTraffic(nil, flows, testControllerConfiguration(nil), testControlPositions(), lc)

	if lc.InboundFlowIsBackground("FLOW", "KTST") {
		t.Error("a flow with no arrival serving KTST was called background there")
	}
}

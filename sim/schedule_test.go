// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
)

// scenarioScheduleTestSim builds the minimal Sim scenario schedule generation
// needs: one departure runway, one inbound flow with arrivals and overflights,
// and rates for each. It uses the real aviation database (loaded in TestMain)
// so that airline and callsign sampling work.
func scenarioScheduleTestSim(start Time) *Sim {
	return &Sim{
		StartTime:     start,
		Rand:          rand.Make(),
		lg:            &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Aircraft:      make(map[av.ADSBCallsign]*Aircraft),
		STARSComputer: &STARSComputer{},
		State: &CommonState{
			DynamicState: DynamicState{
				SimTime: NewSimTime(start.Time().Add(-PrespawnDuration)),
				LaunchConfig: LaunchConfig{
					TrafficSource:        TrafficSourceScenario,
					DepartureRateScale:   1,
					InboundFlowRateScale: 1,
					DepartureRates: map[string]map[av.RunwayID]map[string]float32{
						"KMSP": {"12L": {"": 30}},
					},
					InboundFlowRates: map[string]map[string]float32{
						"TEST": {"KMSP": 20, "overflights": 10},
					},
				},
			},
			Airports: map[string]*av.Airport{
				"KMSP": {
					Departures: []av.Departure{{
						Exit:        "DEPSE",
						Destination: "KATL",
						Airlines:    []av.DepartureAirline{{AirlineSpecifier: av.AirlineSpecifier{ICAO: "AAL"}}},
					}},
					DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
						"12L": {"DEPSE": {{}}},
					},
				},
			},
			DepartureRunways: []DepartureRunway{{Airport: "KMSP", Runway: "12L"}},
			InboundFlows: map[string]*av.InboundFlow{
				"TEST": {
					Arrivals: []av.Arrival{{
						Airports: []string{"KMSP"},
						Airlines: map[string][]av.ArrivalAirline{
							"KMSP": {{AirlineSpecifier: av.AirlineSpecifier{ICAO: "AAL"}, Airport: "KATL"}},
						},
					}},
					Overflights: []av.Overflight{{
						Airlines: []av.OverflightAirline{{
							AirlineSpecifier: av.AirlineSpecifier{ICAO: "AAL"},
							DepartureAirport: "KATL",
							ArrivalAirport:   "KORD",
						}},
					}},
				},
			},
		},
	}
}

func checkSortedSchedule(t *testing.T, fs *FlightSchedule) {
	t.Helper()
	for i := 1; i < len(fs.Departures); i++ {
		if compareScheduledFlights(fs.Departures[i-1].ScheduledFlight, fs.Departures[i].ScheduledFlight) > 0 {
			t.Errorf("departures out of order at %d", i)
		}
	}
	for i := 1; i < len(fs.Arrivals); i++ {
		if compareScheduledFlights(fs.Arrivals[i-1].ScheduledFlight, fs.Arrivals[i].ScheduledFlight) > 0 {
			t.Errorf("arrivals out of order at %d", i)
		}
	}
	for i := 1; i < len(fs.Overflights); i++ {
		if compareScheduledFlights(fs.Overflights[i-1].ScheduledFlight, fs.Overflights[i].ScheduledFlight) > 0 {
			t.Errorf("overflights out of order at %d", i)
		}
	}
}

func TestScheduleOrdersPublishedDeparturesFromSelectedStartTime(t *testing.T) {
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

	s := publishedProviderTestSim(t, start)
	s.State.LaunchConfig.TrafficSource = TrafficSourceTimetable
	s.schedulePublishedFlights(timetableFlights(start, timetable, &s.State.LaunchConfig), 0)
	s.Schedule.sortEntries()

	if len(s.Schedule.Departures) != 3 {
		t.Fatalf("got %d departures, want 3", len(s.Schedule.Departures))
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
		got := s.Schedule.Departures[i]
		if got.Callsign != expected.callsign || got.SpawnTime != expected.spawn {
			t.Errorf("departure %d = %s at %s, want %s at %s", i, got.Callsign,
				got.SpawnTime.Time(), expected.callsign, expected.spawn.Time())
		}
		if got.Source != TrafficSourceTimetable {
			t.Errorf("departure %d source = %s, want Timetable", i, got.Source)
		}
	}
}

func TestScheduleOrdersPublishedArrivalsFromSelectedStartTime(t *testing.T) {
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

	s := publishedProviderTestSim(t, start)
	s.State.LaunchConfig.TrafficSource = TrafficSourceTimetable
	s.schedulePublishedFlights(timetableFlights(start, timetable, &s.State.LaunchConfig), 0)
	s.Schedule.sortEntries()

	if len(s.Schedule.Arrivals) != 3 {
		t.Fatalf("got %d arrivals, want 3", len(s.Schedule.Arrivals))
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
		got := s.Schedule.Arrivals[i]
		if got.Callsign != expected.callsign || got.SpawnTime != expected.spawn {
			t.Errorf("arrival %d = %s at %s, want %s at %s", i, got.Callsign,
				got.SpawnTime.Time(), expected.callsign, expected.spawn.Time())
		}
		if got.Group != "TEST" {
			t.Errorf("arrival %d group = %q, want TEST", i, got.Group)
		}
		if got.DropReason != "" {
			t.Errorf("arrival %d has drop reason %q", i, got.DropReason)
		}
	}
}

// The rate scale draws published flights in towards the start time rather than
// leaving any of them out: at 2x, a flight twenty minutes into the data
// operates ten minutes into the sim. Each direction is scaled on its own.
func TestScheduleScalesPublishedRates(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.LaunchConfig.PublishedDepartureRateScale = 2

	s.schedulePublishedFlights([]av.Flight{
		testFlight("DAL1", "KMSP", "KORD", true, 14, 20),
		testFlight("DAL2", "KMSP", "KATL", true, 14, 40),
		testFlight("DAL3", "KMSP", "KDEN", false, 14, 30),
	}, flightSpawnLead)
	s.Schedule.sortEntries()

	// Historical flights of both kinds spawn flightSpawnLead ahead of the time
	// they operate at.
	wantDepartures := []struct {
		callsign string
		spawn    Time
	}{
		{"DAL1", start.Add(10*time.Minute - flightSpawnLead)},
		{"DAL2", start.Add(20*time.Minute - flightSpawnLead)},
	}
	if len(s.Schedule.Departures) != len(wantDepartures) {
		t.Fatalf("got %d departures, want %d", len(s.Schedule.Departures), len(wantDepartures))
	}
	for i, expected := range wantDepartures {
		if got := s.Schedule.Departures[i]; got.Callsign != expected.callsign || got.SpawnTime != expected.spawn {
			t.Errorf("departure %d = %s at %s, want %s at %s", i, got.Callsign,
				got.SpawnTime.Time(), expected.callsign, expected.spawn.Time())
		}
	}

	if len(s.Schedule.Arrivals) != 1 {
		t.Fatalf("got %d arrivals, want 1", len(s.Schedule.Arrivals))
	}
	if want := start.Add(30*time.Minute - flightSpawnLead); s.Schedule.Arrivals[0].SpawnTime != want {
		t.Errorf("arrival spawns at %s with arrivals at 1x, want %s",
			s.Schedule.Arrivals[0].SpawnTime.Time(), want.Time())
	}
}

func TestScenarioScheduleGeneration(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := scenarioScheduleTestSim(start)
	s.generateSchedule()

	from := s.State.SimTime
	until := from.Add(scenarioScheduleHorizon)
	if s.Schedule.ScenarioGeneratedUntil != until {
		t.Errorf("ScenarioGeneratedUntil = %s, want %s",
			s.Schedule.ScenarioGeneratedUntil.Time(), until.Time())
	}
	checkSortedSchedule(t, &s.Schedule)

	// 30/hour over 8 hours with waits drawn within 85%-115% of the average.
	if n := len(s.Schedule.Departures); n < 200 || n > 290 {
		t.Errorf("generated %d departures, want roughly 240", n)
	}
	// Waits are at least 85% of the average spacing; entries the sampler
	// skipped can stretch a gap but never shrink one.
	minWait := time.Duration(0.85 * 3600 / 30 * float32(time.Second))
	for i := 1; i < len(s.Schedule.Departures); i++ {
		if gap := s.Schedule.Departures[i].SpawnTime.Sub(s.Schedule.Departures[i-1].SpawnTime); gap < minWait-time.Second {
			t.Errorf("departures %d and %d only %s apart, want at least %s", i-1, i, gap, minWait)
		}
	}

	for i, dep := range s.Schedule.Departures {
		if dep.Callsign == "" || dep.AircraftType == "" {
			t.Fatalf("departure %d has empty identity: %+v", i, dep)
		}
		if _, ok := av.DB.AircraftPerformance[dep.AircraftType]; !ok {
			t.Errorf("departure %d type %s not in performance database", i, dep.AircraftType)
		}
		if dep.Runway != "12L" || dep.DepartureIndex != 0 || dep.DepartureAirport != "KMSP" {
			t.Errorf("departure %d placement wrong: %+v", i, dep)
		}
		if dep.SpawnTime.Before(from) || !dep.SpawnTime.Before(until) {
			t.Errorf("departure %d spawn %s outside generation window", i, dep.SpawnTime.Time())
		}
		if dep.Source != TrafficSourceScenario {
			t.Errorf("departure %d source = %s, want Scenario", i, dep.Source)
		}
	}

	if n := len(s.Schedule.Arrivals); n < 130 || n > 195 {
		t.Errorf("generated %d arrivals, want roughly 160", n)
	}
	for i, arr := range s.Schedule.Arrivals {
		if arr.Callsign == "" || arr.AircraftType == "" {
			t.Fatalf("arrival %d has empty identity: %+v", i, arr)
		}
		if arr.Group != "TEST" || arr.Index != 0 || arr.ArrivalAirport != "KMSP" {
			t.Errorf("arrival %d placement wrong: %+v", i, arr)
		}
	}

	if n := len(s.Schedule.Overflights); n < 60 || n > 100 {
		t.Errorf("generated %d overflights, want roughly 80", n)
	}
	for i, of := range s.Schedule.Overflights {
		if of.Callsign == "" || of.AircraftType == "" {
			t.Fatalf("overflight %d has empty identity: %+v", i, of)
		}
		if of.Group != "TEST" || of.Index != 0 {
			t.Errorf("overflight %d placement wrong: %+v", i, of)
		}
	}
}

// Two sims with identical random-number state must generate identical
// schedules: map iteration during generation goes through sorted keys.
func TestScheduleGenerationDeterministic(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s1 := scenarioScheduleTestSim(start)
	s2 := scenarioScheduleTestSim(start)

	state, err := json.Marshal(s1.Rand)
	if err != nil {
		t.Fatalf("marshal rand: %v", err)
	}
	if err := json.Unmarshal(state, s2.Rand); err != nil {
		t.Fatalf("unmarshal rand: %v", err)
	}

	s1.generateSchedule()
	s2.generateSchedule()
	if !reflect.DeepEqual(s1.Schedule, s2.Schedule) {
		t.Error("identical random state generated different schedules")
	}
}

func TestExtendSchedule(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := scenarioScheduleTestSim(start)
	s.generateSchedule()
	firstUntil := s.Schedule.ScenarioGeneratedUntil

	// Far from the end of the generated window, nothing changes.
	before := len(s.Schedule.Departures)
	s.State.SimTime = firstUntil.Add(-2 * time.Hour)
	s.extendSchedule()
	if len(s.Schedule.Departures) != before || s.Schedule.ScenarioGeneratedUntil != firstUntil {
		t.Fatal("extended the schedule before its end was near")
	}

	s.State.SimTime = firstUntil.Add(-30 * time.Minute)
	s.extendSchedule()
	if want := firstUntil.Add(scenarioScheduleHorizon); s.Schedule.ScenarioGeneratedUntil != want {
		t.Errorf("ScenarioGeneratedUntil = %s, want %s",
			s.Schedule.ScenarioGeneratedUntil.Time(), want.Time())
	}
	if len(s.Schedule.Departures) <= before {
		t.Error("no departures added by extension")
	}
	if last := s.Schedule.Departures[len(s.Schedule.Departures)-1]; !last.SpawnTime.After(firstUntil) {
		t.Error("extension added no departures past the previous window")
	}
	checkSortedSchedule(t, &s.Schedule)
}

// The arrival push windows follow the schedule the runtime push machine used
// to keep: the first push starts within the first push-frequency interval and
// each later one a jittered frequency after the previous one ends.
func TestScenarioPushWindows(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := scenarioScheduleTestSim(start)
	s.State.LaunchConfig.ArrivalPushes = true
	s.State.LaunchConfig.ArrivalPushFrequencyMinutes = 20
	s.State.LaunchConfig.ArrivalPushLengthMinutes = 10

	from := s.State.SimTime
	until := from.Add(scenarioScheduleHorizon)
	windows := s.scenarioPushWindows(from, until)
	if len(windows) == 0 {
		t.Fatal("no push windows over eight hours")
	}

	if first := windows[0].start; first.Before(from.Add(time.Minute)) || first.After(from.Add(21*time.Minute)) {
		t.Errorf("first push starts %s after the window opens, want between 1m and 21m",
			first.Sub(from))
	}
	for i, w := range windows {
		if got := w.end.Sub(w.start); got != 10*time.Minute {
			t.Errorf("push %d lasts %s, want 10m", i, got)
		}
		if !w.start.Before(until) {
			t.Errorf("push %d starts after the generation window", i)
		}
		if i > 0 {
			gap := w.start.Sub(windows[i-1].end)
			if gap < 18*time.Minute || gap > 22*time.Minute {
				t.Errorf("gap before push %d is %s, want 20m plus or minus 2m", i, gap)
			}
		}
	}
}

func testScheduledDeparture(callsign, airport, other string, spawn Time) ScheduledDeparture {
	return ScheduledDeparture{
		ScheduledFlight: ScheduledFlight{Callsign: callsign, AircraftType: "C560",
			DepartureAirport: airport, ArrivalAirport: other,
			Source: TrafficSourceTimetable, SpawnTime: spawn},
	}
}

// Nothing spawns before its scheduled time: the consumption loop leaves
// future entries alone.
func TestScheduleWaitsForSpawnTime(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.SimTime = start
	s.Schedule.Departures = []ScheduledDeparture{
		testScheduledDeparture("DAL1", "KMSP", "KORD", start.Add(5*time.Minute)),
	}
	s.Schedule.Arrivals = []ScheduledArrival{{
		ScheduledFlight: ScheduledFlight{Callsign: "DAL2", AircraftType: "C560",
			DepartureAirport: "KORD", ArrivalAirport: "KMSP",
			Source: TrafficSourceTimetable, SpawnTime: start.Add(7 * time.Minute)},
		Group: "TEST",
	}}

	s.spawnScheduledDepartures()
	s.spawnScheduledArrivals()

	if len(s.Schedule.Departures) != 1 || len(s.Schedule.Arrivals) != 1 {
		t.Error("created a scheduled flight before its spawn time")
	}
}

// The runways at an airport hold different gates, so a flight leaves through
// the runway whose gates its real route uses. JFK departing 13L/R is the case
// that caught this: the north and east gates are off 13L and the south and
// west ones off 13R.
func TestScheduledDeparturesResolveTheirRunway(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.SimTime = start

	// KORD lies out the southeast gate, which only 12L launches; KDEN out the
	// southwest one, which is 30R's.
	toKORD := testScheduledDeparture("DAL1", "KMSP", "KORD", start)
	toKDEN := testScheduledDeparture("DAL2", "KMSP", "KDEN", start.Add(time.Minute))

	if runway, _, _, err := s.resolveScheduledDepartureRunway(&toKORD); err != nil || runway != "12L" {
		t.Errorf("KORD departure resolves to %q (%v), want 12L", runway, err)
	}
	if runway, _, _, err := s.resolveScheduledDepartureRunway(&toKDEN); err != nil || runway != "30R" {
		t.Errorf("KDEN departure resolves to %q (%v), want 30R", runway, err)
	}
}

// The runway a published flight leaves from is the one whose gates fly its own
// route, not merely the first one that can come up with something. KMSP is the
// case that caught this: 12L sorts first and has no southeast gate, so an
// Atlanta flight borrowed Birmingham's route out the northeast one while 12R,
// which flies the real Atlanta route, went untried.
func TestScheduledDeparturesPreferTheRunwayThatFliesTheirRoute(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{{Route: "KORG EAST J1 KTGT", Type: "H"}})
	seedTestRoutes(t, "KEAS", []av.AirportPairRoute{{Route: "KORG EASTN J2 KEAS", Type: "H"}})

	s := &Sim{State: &CommonState{
		NmPerLongitude: testNmPerLongitude,
		DynamicState: DynamicState{
			LaunchConfig: LaunchConfig{
				DepartureEnabled: map[string]map[av.RunwayID]map[string]bool{
					"KORG": {"12L": {"jet": true}, "30R": {"jet": true}},
				},
			},
		},
		Airports: map[string]*av.Airport{
			"KORG": {
				ExitCategories: map[av.ExitID]string{"EASTN": "jet", "EAST": "jet"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
					// 12L sorts first but only reaches KTGT by way of KEAS.
					"12L": {"EASTN": {{}}},
					"30R": {"EAST": {{}}},
				},
			},
		},
		DepartureRunways: []DepartureRunway{
			{Airport: "KORG", Runway: "12L", Category: "jet"},
			{Airport: "KORG", Runway: "30R", Category: "jet"},
		},
	}}

	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	e := testScheduledDeparture("DAL1", "KORG", "KTGT", start)
	e.AircraftType = "B738"
	runway, _, choice, err := s.resolveScheduledDepartureRunway(&e)
	if err != nil {
		t.Fatalf("resolveScheduledDepartureRunway: %v", err)
	}
	if runway != "30R" {
		t.Errorf("resolved to runway %q (%s), want 30R, which flies the KTGT route", runway, choice.how)
	}
	if choice.candidate.dep.Exit != "EAST" {
		t.Errorf("left through %q (%s), want EAST", choice.candidate.dep.Exit, choice.how)
	}
}

// A flight no runway at the airport can fly is dropped when it comes due,
// which is when the drop is worth reporting; leaving it in the queue any
// longer would stall the departures behind it.
func TestScheduleDropsDeparturesNoRunwayFlies(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	// Due north of KMSP: neither gate heads anywhere near it.
	av.DB.Airports["KGFK"] = av.FAAAirport{Id: "KGFK", Location: math.Point2LL{-93.2, 48.0}}

	s.Schedule.Departures = []ScheduledDeparture{
		testScheduledDeparture("DAL1", "KMSP", "KGFK", start),
	}

	s.State.SimTime = NewSimTime(start.Time().Add(-time.Minute))
	s.spawnScheduledDepartures()
	if len(s.Schedule.Departures) != 1 {
		t.Error("dropped a departure before its scheduled time")
	}

	s.State.SimTime = start
	s.spawnScheduledDepartures()
	if len(s.Schedule.Departures) != 0 {
		t.Error("kept a departure no runway can fly")
	}
}

// A rate scale of zero parks a direction's published flights rather than
// dropping them: the schedule is the data's only copy, so raising the scale
// again must bring the traffic back.
func TestZeroRateScaleParksPublishedFlights(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.LaunchConfig.TrafficSource = TrafficSourceTimetable
	s.State.LaunchConfig.PublishedDepartureRateScale = 0

	s.schedulePublishedFlights([]av.Flight{
		testFlight("DAL1", "KMSP", "KORD", true, 14, 20),
		testFlight("DAL2", "KMSP", "KATL", false, 14, 30),
	}, 0)
	s.Schedule.sortEntries()

	if len(s.Schedule.Departures) != 1 {
		t.Fatalf("got %d departures, want the parked one", len(s.Schedule.Departures))
	}
	if spawn := s.Schedule.Departures[0].SpawnTime; spawn.Before(start.Add(300 * 24 * time.Hour)) {
		t.Errorf("departure spawns at %s with its scale zero, want parked far out", spawn.Time())
	}
	if len(s.Schedule.Arrivals) != 1 || s.Schedule.Arrivals[0].SpawnTime != start.Add(30*time.Minute-flightSpawnLead) {
		t.Error("arrival direction affected by the departure scale")
	}

	// Raising the scale restores the departure's time from the flight data.
	s.State.SimTime = start
	s.State.LaunchConfig.PublishedDepartureRateScale = 2
	s.rewritePublishedSpawnTimes()
	if want := start.Add(10 * time.Minute); s.Schedule.Departures[0].SpawnTime != want {
		t.Errorf("departure spawns at %s after raising the scale, want %s",
			s.Schedule.Departures[0].SpawnTime.Time(), want.Time())
	}
}

// Recycling a published departure shifts the flights of the clicked slot's
// runway, the one whose gates fly it.
func TestRecycleDepartureUsesSlotRunway(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.SimTime = start

	// Both fly out the southeast gate, which only 12L launches.
	s.Schedule.Departures = []ScheduledDeparture{
		testScheduledDeparture("DAL1", "KMSP", "KORD", start),
		testScheduledDeparture("DAL2", "KMSP", "KORD", start.Add(5*time.Minute)),
	}

	err := s.RecycleLaunchAircraft("TCW",
		LaunchFlight{Callsign: "DAL1", Departure: true, Runway: "12L"})
	if err != nil {
		t.Fatalf("recycle: %v", err)
	}
	if len(s.Schedule.Departures) != 1 {
		t.Fatalf("%d departures left, want 1", len(s.Schedule.Departures))
	}
	if got := s.Schedule.Departures[0]; got.Callsign != "DAL2" || got.SpawnTime != start ||
		got.SpawnOffset != -5*time.Minute {
		t.Errorf("DAL2 at %s offset %s after recycle, want %s offset -5m",
			got.SpawnTime.Time(), got.SpawnOffset, start.Time())
	}
}

// Arrival placements bake in which inbound flows are enabled, so toggling a
// flow refits the queued published arrivals: one whose flow was switched off
// is re-placed (or marked unflyable) rather than silently dropped, and
// re-enabling the flow takes it back.
func TestFlowEnableTogglesRefitArrivals(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := publishedProviderTestSim(t, start)
	s.State.SimTime = start
	s.State.LaunchConfig.TrafficSource = TrafficSourceTimetable
	s.schedulePublishedFlights([]av.Flight{
		testFlight("DAL1", "KMSP", "KATL", false, 14, 30),
	}, 0)
	if len(s.Schedule.Arrivals) != 1 || s.Schedule.Arrivals[0].DropReason != "" {
		t.Fatalf("arrival not queued and placed: %+v", s.Schedule.Arrivals)
	}

	old := s.State.LaunchConfig
	s.State.LaunchConfig.InboundFlowEnabled = map[string]map[string]bool{"TEST": {"KMSP": false}}
	s.applyScheduleConfigChanges(&old)
	if s.Schedule.Arrivals[0].DropReason == "" {
		t.Error("arrival still placed with its only flow disabled")
	}

	old = s.State.LaunchConfig
	s.State.LaunchConfig.InboundFlowEnabled = map[string]map[string]bool{"TEST": {"KMSP": true}}
	s.applyScheduleConfigChanges(&old)
	if e := s.Schedule.Arrivals[0]; e.DropReason != "" || e.Group != "TEST" {
		t.Errorf("arrival not re-placed after re-enabling its flow: %+v", e)
	}
}

// The schedule is authoritative state: it must survive a save and reload
// byte for byte, mutations included.
func TestScheduleSurvivesSaveAndReload(t *testing.T) {
	start := NewSimTime(time.Date(2026, time.July, 14, 14, 0, 0, 0, time.UTC))
	s := scenarioScheduleTestSim(start)
	s.generateSchedule()

	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded Sim
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s.Schedule, reloaded.Schedule) {
		t.Error("schedule changed across a save and reload")
	}
}

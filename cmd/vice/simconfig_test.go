// simconfig_timetable_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"os"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func TestMain(m *testing.M) {
	// TimetableStartMinute needs the airport database to find time zones.
	db.InitDB()
	os.Exit(m.Run())
}

func TestNormalizeTrafficSourceConfig(t *testing.T) {
	spec := &scenario.Spec{
		Timetables: []traffic.TimetableSummary{
			{ID: "development-test", Name: "Development Test", Airport: "KMSP"},
		},
		TrafficSources: []sim.TrafficSource{sim.TrafficSourceScenario, sim.TrafficSourceTimetable},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:               sim.TrafficSourceTimetable,
			TimetableStartMinute:        2000,
			PublishedArrivalRateScale:   -0.5,
			PublishedDepartureRateScale: 10,
		},
	}

	normalizeTrafficSourceConfig(spec)

	if got, want := spec.LaunchConfig.TimetableID, "development-test"; got != want {
		t.Fatalf("TimetableID = %q, want %q", got, want)
	}
	if got, want := spec.LaunchConfig.TimetableAirport, av.ICAOAirportCode("KMSP"); got != want {
		t.Fatalf("TimetableAirport = %q, want %q", got, want)
	}
	if got, want := spec.LaunchConfig.TimetableStartMinute, 1439; got != want {
		t.Fatalf("TimetableStartMinute = %d, want %d", got, want)
	}
	if got, want := spec.LaunchConfig.PublishedArrivalRateScale, float32(0); got != want {
		t.Fatalf("PublishedArrivalRateScale = %f, want %f", got, want)
	}

	if got, want := spec.LaunchConfig.PublishedDepartureRateScale,
		float32(sim.MaxPublishedRateScale); got != want {
		t.Fatalf("PublishedDepartureRateScale = %f, want %f", got, want)
	}
}

func TestNormalizeTrafficSourceConfigWithoutTimetables(t *testing.T) {
	spec := &scenario.Spec{
		TrafficSources: []sim.TrafficSource{sim.TrafficSourceScenario},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource: sim.TrafficSourceTimetable,
			TimetableID:   "missing",
		},
	}

	normalizeTrafficSourceConfig(spec)

	if spec.LaunchConfig.TrafficSource != sim.TrafficSourceScenario {
		t.Fatalf("TrafficSource = %v, want scenario", spec.LaunchConfig.TrafficSource)
	}
	if spec.LaunchConfig.TimetableID != "" {
		t.Fatalf("TimetableID = %q, want empty", spec.LaunchConfig.TimetableID)
	}
	if spec.LaunchConfig.TimetableAirport != "" {
		t.Fatalf("TimetableAirport = %q, want empty", spec.LaunchConfig.TimetableAirport)
	}
}

func TestNormalizeTrafficSourceConfigWithoutHistoricalFlights(t *testing.T) {
	spec := &scenario.Spec{
		TrafficSources: []sim.TrafficSource{sim.TrafficSourceScenario},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource: sim.TrafficSourceHistorical,
		},
	}

	normalizeTrafficSourceConfig(spec)

	if spec.LaunchConfig.TrafficSource != sim.TrafficSourceScenario {
		t.Fatalf("TrafficSource = %v, want scenario", spec.LaunchConfig.TrafficSource)
	}
}

// A scenario with no airlines of its own can't be flown from the scenario
// source, so the client must start on one the server actually offers.
func TestNormalizeTrafficSourceConfigWithoutScenarioTraffic(t *testing.T) {
	spec := &scenario.Spec{
		TrafficSources: []sim.TrafficSource{sim.TrafficSourceHistorical},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource: sim.TrafficSourceScenario,
		},
	}

	normalizeTrafficSourceConfig(spec)

	if spec.LaunchConfig.TrafficSource != sim.TrafficSourceHistorical {
		t.Fatalf("TrafficSource = %v, want historical", spec.LaunchConfig.TrafficSource)
	}
}

// The sim start time is chosen in UTC; a timetable needs it as a local clock
// time at its airport. KMSP is America/Chicago.
func TestTimetableStartMinuteSummer(t *testing.T) {
	// 19:00Z on July 14 is 14:00 CDT.
	got, err := sim.TimetableStartMinute(time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("TimetableStartMinute: %v", err)
	}
	if want := 14 * 60; got != want {
		t.Fatalf("TimetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteWinter(t *testing.T) {
	// 20:00Z on January 14 is 14:00 CST; the same local time is an hour later
	// in UTC than it is in summer.
	got, err := sim.TimetableStartMinute(time.Date(2026, time.January, 14, 20, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("TimetableStartMinute: %v", err)
	}
	if want := 14 * 60; got != want {
		t.Fatalf("TimetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteAcrossLocalMidnight(t *testing.T) {
	// 04:00Z on July 16 is still 23:00 CDT on July 15.
	got, err := sim.TimetableStartMinute(time.Date(2026, time.July, 16, 4, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("TimetableStartMinute: %v", err)
	}
	if want := 23 * 60; got != want {
		t.Fatalf("TimetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteRejectsUnknownAirport(t *testing.T) {
	if _, err := sim.TimetableStartMinute(time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
		"XXXX"); err == nil {
		t.Fatal("expected unknown-airport error")
	}
}

// metarConfig is a dialog with METAR on hand for the given airports, in the
// state metarAirportsByTraffic reads.
func metarConfig(airports ...av.ICAOAirportCode) *NewSimConfiguration {
	c := &NewSimConfiguration{airportMETAR: make(map[av.ICAOAirportCode][]wx.METAR)}
	for _, ap := range airports {
		c.airportMETAR[ap] = []wx.METAR{{ICAO: string(ap)}}
	}
	return c
}

func TestMetarAirportsByTraffic(t *testing.T) {
	// KZZZ is the busy airport in every case below, so alphabetical order--the
	// old behavior, and the tie-break--is the order the ranking has to undo.
	scenarioSpec := &scenario.Spec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:      sim.TrafficSourceScenario,
			DepartureRateScale: 1,
			DepartureRates: map[av.ICAOAirportCode]map[av.RunwayID]map[string]float32{
				"KAAA": {"18": {"": 5}},
				"KZZZ": {"36": {"": 30}},
			},
		},
	}
	timetableSpec := &scenario.Spec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:    sim.TrafficSourceTimetable,
			TimetableAirport: "KZZZ",
		},
	}
	historicalSpec := &scenario.Spec{
		LaunchConfig: sim.LaunchConfig{TrafficSource: sim.TrafficSourceHistorical},
	}

	for _, tc := range []struct {
		name       string
		spec       *scenario.Spec
		operations map[av.ICAOAirportCode]int
		want       []av.ICAOAirportCode
		wantOK     bool
	}{
		{"scenario rates", scenarioSpec, nil, []av.ICAOAirportCode{"KZZZ", "KAAA", "KBBB"}, true},
		{"timetable airport", timetableSpec, nil, []av.ICAOAirportCode{"KZZZ", "KAAA", "KBBB"}, true},
		{"historical before counts", historicalSpec, nil, []av.ICAOAirportCode{"KAAA", "KBBB", "KZZZ"}, false},
		{"historical counts", historicalSpec, map[av.ICAOAirportCode]int{"KZZZ": 40, "KBBB": 10},
			[]av.ICAOAirportCode{"KZZZ", "KBBB", "KAAA"}, true},
		{"historical quiet window", historicalSpec, map[av.ICAOAirportCode]int{},
			[]av.ICAOAirportCode{"KAAA", "KBBB", "KZZZ"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := metarConfig("KZZZ", "KAAA", "KBBB")
			c.trafficPreviewOperations = tc.operations
			airports, ok := c.metarAirportsByTraffic(tc.spec)
			if !slices.Equal(airports, tc.want) {
				t.Errorf("airports = %v, want %v", airports, tc.want)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// day is an instant in UTC, as the weather and flight coverage intervals are.
func day(month time.Month, d, hour int) time.Time {
	return time.Date(2026, month, d, hour, 0, 0, 0, time.UTC)
}

// jfk places a scenario solidly in America/New_York.
var jfk = math.Point2LL{-73.78, 40.64}

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// localDays is the run of local midnights from day first to day last inclusive.
func localDays(loc *time.Location, month time.Month, first, last int) []time.Time {
	var days []time.Time
	for d := first; d <= last; d++ {
		days = append(days, time.Date(2026, month, d, 0, 0, 0, 0, loc))
	}
	return days
}

func TestValidStartDays(t *testing.T) {
	loc := newYork(t)

	for _, tc := range []struct {
		name     string
		source   sim.TrafficSource
		wx       []util.TimeInterval
		flights  []util.TimeInterval
		expected []time.Time
	}{
		{
			// May 1 0000Z is the evening of April 30 in New York and May 10
			// 0000Z the evening of May 9, so the whole local days inside the
			// coverage are May 1 through May 8. Counting UTC days instead gave
			// nine, one of which the calendar could never offer--the bug that
			// snapped start times to midnight.
			name:     "whole local days only",
			source:   sim.TrafficSourceScenario,
			wx:       []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			expected: localDays(loc, time.May, 1, 8),
		},
		{
			// Only historical traffic comes from the flight data, so nothing
			// else has any reason to be narrowed by it.
			name:     "scenario traffic ignores the flight data",
			source:   sim.TrafficSourceScenario,
			wx:       []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			flights:  []util.TimeInterval{{day(time.May, 4, 0), day(time.May, 5, 0)}},
			expected: localDays(loc, time.May, 1, 8),
		},
		{
			// The May 2026 outage: the data stops on the 4th and comes back
			// mid-afternoon on the 7th. With a day held back from each stretch
			// so a sim started at its end still has traffic to fly, only May 1
			// and 2 remain whole local days.
			name:   "a gap in the flight data is not offered",
			source: sim.TrafficSourceHistorical,
			wx:     []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			flights: []util.TimeInterval{
				{day(time.May, 1, 0), day(time.May, 4, 23)},
				{day(time.May, 7, 18), day(time.May, 10, 0)},
			},
			expected: localDays(loc, time.May, 1, 2),
		},
		{
			// A sim started here would run out of traffic immediately.
			name:     "a stretch shorter than a day drops out",
			source:   sim.TrafficSourceHistorical,
			wx:       []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			flights:  []util.TimeInterval{{day(time.May, 4, 0), day(time.May, 4, 18)}},
			expected: nil,
		},
		{
			name:     "no weather leaves nothing to offer",
			source:   sim.TrafficSourceHistorical,
			wx:       nil,
			flights:  []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			expected: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &NewSimConfiguration{availableWXIntervals: tc.wx}
			spec := &scenario.Spec{Center: jfk, HistoricalFlightIntervals: tc.flights}
			spec.LaunchConfig.TrafficSource = tc.source

			got := c.validStartDays(spec)
			if len(got) != len(tc.expected) {
				t.Fatalf("got %v, expected %v", got, tc.expected)
			}
			for i := range got {
				if !got[i].Equal(tc.expected[i]) {
					t.Errorf("day %d: got %v, expected %v", i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestDayWindows(t *testing.T) {
	loc := newYork(t)
	clock := airportClock{loc: loc, local: true}

	// March 8 2026 is the spring-forward day (23 hours long) and November 1
	// the fall-back day (25 hours); the windows should stick to the wall
	// clock regardless.
	days := []time.Time{
		time.Date(2026, time.March, 7, 0, 0, 0, 0, loc),
		time.Date(2026, time.March, 8, 0, 0, 0, 0, loc),
		time.Date(2026, time.November, 1, 0, 0, 0, 0, loc),
	}

	daytime := dayWindows(days, clock, 7, 19)
	for i, w := range daytime {
		if h := w.Start().In(loc).Hour(); h != 7 {
			t.Errorf("window %d starts at local hour %d, expected 7", i, h)
		}
		if h := w.End().In(loc).Hour(); h != 19 {
			t.Errorf("window %d ends at local hour %d, expected 19", i, h)
		}
	}

	full := dayWindows(days, clock, 0, 24)
	for i, w := range full {
		if !w.Start().Equal(days[i]) || !w.End().Equal(days[i].AddDate(0, 0, 1)) {
			t.Errorf("window %d is %v, expected all of %v", i, w, days[i])
		}
	}
	if d := full[1].Duration(); d != 23*time.Hour {
		t.Errorf("spring-forward day is %v long, expected 23h", d)
	}
	if d := full[2].Duration(); d != 25*time.Hour {
		t.Errorf("fall-back day is %v long, expected 25h", d)
	}
}

func TestValidateAndAdjustDatePreservesTime(t *testing.T) {
	loc := newYork(t)
	clock := airportClock{loc: loc, local: true}
	validDays := localDays(loc, time.May, 5, 6)

	// A date on a valid day is left alone.
	date := time.Date(2026, time.May, 5, 9, 30, 0, 0, loc).UTC()
	if validateAndAdjustDate(&date, clock, validDays) {
		t.Errorf("date on a valid day was adjusted to %v", date)
	}

	// One on an invalid day moves to the nearest valid day at the same time
	// of day.
	date = time.Date(2026, time.May, 9, 9, 30, 0, 0, loc).UTC()
	if !validateAndAdjustDate(&date, clock, validDays) {
		t.Error("date on an invalid day was not adjusted")
	}
	if expected := time.Date(2026, time.May, 6, 9, 30, 0, 0, loc); !date.Equal(expected) {
		t.Errorf("got %v, expected %v", date, expected)
	}
}

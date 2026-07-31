// simconfig_timetable_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"os"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

func TestMain(m *testing.M) {
	// timetableStartTimeUTC needs av.DB.AirportTimeZones.
	av.InitDB()
	os.Exit(m.Run())
}

func TestNormalizeTrafficSourceConfig(t *testing.T) {
	spec := &server.ScenarioSpec{
		PrimaryAirport: "KMSP",
		Timetables: []sim.TimetableSummary{
			{ID: "development-test", Name: "Development Test"},
		},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:                sim.TrafficSourceTimetable,
			TimetableStartMinute:         2000,
			PublishedArrivalPercentage:   -5,
			PublishedDeparturePercentage: 150,
		},
	}

	normalizeTrafficSourceConfig(spec)

	if got, want := spec.LaunchConfig.TimetableID, "development-test"; got != want {
		t.Fatalf("TimetableID = %q, want %q", got, want)
	}
	if got, want := spec.LaunchConfig.TimetableStartMinute, 1439; got != want {
		t.Fatalf("TimetableStartMinute = %d, want %d", got, want)
	}
	if got, want := spec.LaunchConfig.PublishedArrivalPercentage, 0; got != want {
		t.Fatalf("PublishedArrivalPercentage = %d, want %d", got, want)
	}

	if got, want := spec.LaunchConfig.PublishedDeparturePercentage, 100; got != want {
		t.Fatalf("PublishedDeparturePercentage = %d, want %d", got, want)
	}
}

func TestNormalizeTrafficSourceConfigWithoutTimetables(t *testing.T) {
	spec := &server.ScenarioSpec{
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
}

func TestNormalizeTrafficSourceConfigWithoutHistoricalFlights(t *testing.T) {
	spec := &server.ScenarioSpec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource: sim.TrafficSourceHistorical,
		},
	}

	normalizeTrafficSourceConfig(spec)

	if spec.LaunchConfig.TrafficSource != sim.TrafficSourceScenario {
		t.Fatalf("TrafficSource = %v, want scenario", spec.LaunchConfig.TrafficSource)
	}
}

// The timetable start time is interpreted in the airport's local time zone,
// from av.DB.AirportTimeZones; KMSP is in America/Chicago.
func TestTimetableStartTimeUTCSummer(t *testing.T) {
	base := time.Date(2026, time.July, 14, 3, 25, 0, 0, time.UTC)

	got, err := timetableStartTimeUTC(base, 14*60, "KMSP")
	if err != nil {
		t.Fatalf("timetableStartTimeUTC: %v", err)
	}

	want := time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("timetableStartTimeUTC = %s, want %s", got, want)
	}
}

func TestTimetableStartTimeUTCWinter(t *testing.T) {
	base := time.Date(2026, time.January, 14, 3, 25, 0, 0, time.UTC)

	got, err := timetableStartTimeUTC(base, 14*60, "KMSP")
	if err != nil {
		t.Fatalf("timetableStartTimeUTC: %v", err)
	}

	want := time.Date(2026, time.January, 14, 20, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("timetableStartTimeUTC = %s, want %s", got, want)
	}
}

func TestTimetableStartTimeUTCPreservesSelectedScenarioDate(t *testing.T) {
	// Even though 02:00Z on July 15 is still July 14 in Minneapolis,
	// the selected Vice scenario date remains July 15.
	base := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)

	got, err := timetableStartTimeUTC(base, 23*60, "KMSP")
	if err != nil {
		t.Fatalf("timetableStartTimeUTC: %v", err)
	}

	// 23:00 CDT on July 15 is 04:00Z on July 16.
	want := time.Date(2026, time.July, 16, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("timetableStartTimeUTC = %s, want %s", got, want)
	}
}

func TestTimetableStartTimeUTCRejectsUnknownAirport(t *testing.T) {
	_, err := timetableStartTimeUTC(
		time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
		14*60,
		"XXXX",
	)
	if err == nil {
		t.Fatal("expected unknown-airport error")
	}
}

func TestParseTimetableStartTime(t *testing.T) {
	tests := map[string]int{
		"1400":  14 * 60,
		"14:00": 14 * 60,
		"9:30":  9*60 + 30,
		"0930":  9*60 + 30,
		"9":     9 * 60,
	}

	for input, want := range tests {
		got, ok := parseTimetableStartTime(input)
		if !ok {
			t.Errorf("parseTimetableStartTime(%q) rejected valid time", input)
			continue
		}
		if got != want {
			t.Errorf("parseTimetableStartTime(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseTimetableStartTimeRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{
		"",
		"25:00",
		"14:99",
		"2400",
		"abcd",
		"12:30:00",
	} {
		if _, ok := parseTimetableStartTime(input); ok {
			t.Errorf("parseTimetableStartTime(%q) accepted invalid time", input)
		}
	}
}

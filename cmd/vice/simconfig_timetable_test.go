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

// The sim start time is chosen in UTC; a timetable needs it as a local clock
// time at its airport, from av.DB.AirportTimeZones. KMSP is America/Chicago.
func TestTimetableStartMinuteSummer(t *testing.T) {
	// 19:00Z on July 14 is 14:00 CDT.
	got, err := timetableStartMinute(time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("timetableStartMinute: %v", err)
	}
	if want := 14 * 60; got != want {
		t.Fatalf("timetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteWinter(t *testing.T) {
	// 20:00Z on January 14 is 14:00 CST; the same local time is an hour later
	// in UTC than it is in summer.
	got, err := timetableStartMinute(time.Date(2026, time.January, 14, 20, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("timetableStartMinute: %v", err)
	}
	if want := 14 * 60; got != want {
		t.Fatalf("timetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteAcrossLocalMidnight(t *testing.T) {
	// 04:00Z on July 16 is still 23:00 CDT on July 15.
	got, err := timetableStartMinute(time.Date(2026, time.July, 16, 4, 0, 0, 0, time.UTC), "KMSP")
	if err != nil {
		t.Fatalf("timetableStartMinute: %v", err)
	}
	if want := 23 * 60; got != want {
		t.Fatalf("timetableStartMinute = %d, want %d", got, want)
	}
}

func TestTimetableStartMinuteRejectsUnknownAirport(t *testing.T) {
	if _, err := timetableStartMinute(time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
		"XXXX"); err == nil {
		t.Fatal("expected unknown-airport error")
	}
}

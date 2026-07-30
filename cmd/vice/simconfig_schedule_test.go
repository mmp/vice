// simconfig_schedule_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
	"time"

	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

func TestNormalizeScheduleLaunchConfig(t *testing.T) {
	spec := &server.ScenarioSpec{
		PrimaryAirport: "KMSP",
		RealWorldSchedules: []sim.BuiltInScheduleSummary{
			{ID: "development-test", Name: "Development Test"},
		},
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:               sim.TrafficSourceRealWorldSchedule,
			ScheduleStartMinute:         2000,
			ScheduleArrivalPercentage:   -5,
			ScheduleDeparturePercentage: 150,
		},
	}

	normalizeScheduleLaunchConfig(spec)

	if got, want := spec.LaunchConfig.ScheduleID, "development-test"; got != want {
		t.Fatalf("ScheduleID = %q, want %q", got, want)
	}
	if got, want := spec.LaunchConfig.ScheduleStartMinute, 1439; got != want {
		t.Fatalf("ScheduleStartMinute = %d, want %d", got, want)
	}
	if got, want := spec.LaunchConfig.ScheduleArrivalPercentage, 0; got != want {
		t.Fatalf("ScheduleArrivalPercentage = %d, want %d", got, want)
	}

	if got, want := spec.LaunchConfig.ScheduleDeparturePercentage, 100; got != want {
		t.Fatalf("ScheduleDeparturePercentage = %d, want %d", got, want)
	}
}

func TestNormalizeScheduleLaunchConfigWithoutSchedules(t *testing.T) {
	spec := &server.ScenarioSpec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource: sim.TrafficSourceRealWorldSchedule,
			ScheduleID:    "missing",
		},
	}

	normalizeScheduleLaunchConfig(spec)

	if spec.LaunchConfig.TrafficSource != sim.TrafficSourceRandom {
		t.Fatalf("TrafficSource = %v, want random", spec.LaunchConfig.TrafficSource)
	}
	if spec.LaunchConfig.ScheduleID != "" {
		t.Fatalf("ScheduleID = %q, want empty", spec.LaunchConfig.ScheduleID)
	}
}
func TestScheduleStartTimeUTCSummer(t *testing.T) {
	base := time.Date(2026, time.July, 14, 3, 25, 0, 0, time.UTC)

	got, err := scheduleStartTimeUTC(base, 14*60, "America/Chicago")
	if err != nil {
		t.Fatalf("scheduleStartTimeUTC: %v", err)
	}

	want := time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("scheduleStartTimeUTC = %s, want %s", got, want)
	}
}

func TestScheduleStartTimeUTCWinter(t *testing.T) {
	base := time.Date(2026, time.January, 14, 3, 25, 0, 0, time.UTC)

	got, err := scheduleStartTimeUTC(base, 14*60, "America/Chicago")
	if err != nil {
		t.Fatalf("scheduleStartTimeUTC: %v", err)
	}

	want := time.Date(2026, time.January, 14, 20, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("scheduleStartTimeUTC = %s, want %s", got, want)
	}
}

func TestScheduleStartTimeUTCPreservesSelectedScenarioDate(t *testing.T) {
	// Even though 02:00Z on July 15 is still July 14 in Chicago,
	// the selected Vice scenario date remains July 15.
	base := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)

	got, err := scheduleStartTimeUTC(base, 23*60, "America/Chicago")
	if err != nil {
		t.Fatalf("scheduleStartTimeUTC: %v", err)
	}

	// 23:00 CDT on July 15 is 04:00Z on July 16.
	want := time.Date(2026, time.July, 16, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("scheduleStartTimeUTC = %s, want %s", got, want)
	}
}

func TestScheduleStartTimeUTCRejectsUnknownTimezone(t *testing.T) {
	_, err := scheduleStartTimeUTC(
		time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
		14*60,
		"Not/A_Timezone",
	)
	if err == nil {
		t.Fatal("expected invalid timezone error")
	}
}

func TestParseScheduleStartTime(t *testing.T) {
	tests := map[string]int{
		"1400":  14 * 60,
		"14:00": 14 * 60,
		"9:30":  9*60 + 30,
		"0930":  9*60 + 30,
		"9":     9 * 60,
	}

	for input, want := range tests {
		got, ok := parseScheduleStartTime(input)
		if !ok {
			t.Errorf("parseScheduleStartTime(%q) rejected valid time", input)
			continue
		}
		if got != want {
			t.Errorf("parseScheduleStartTime(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseScheduleStartTimeRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{
		"",
		"25:00",
		"14:99",
		"2400",
		"abcd",
		"12:30:00",
	} {
		if _, ok := parseScheduleStartTime(input); ok {
			t.Errorf("parseScheduleStartTime(%q) accepted invalid time", input)
		}
	}
}

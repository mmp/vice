// simconfig_schedule_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"

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
			TrafficSource:             sim.TrafficSourceRealWorldSchedule,
			ScheduleStartMinute:       2000,
			ScheduleTrafficPercentage: 0,
		},
	}

	normalizeScheduleLaunchConfig(spec)

	if got, want := spec.LaunchConfig.ScheduleID, "development-test"; got != want {
		t.Fatalf("ScheduleID = %q, want %q", got, want)
	}
	if got, want := spec.LaunchConfig.ScheduleStartMinute, 1439; got != want {
		t.Fatalf("ScheduleStartMinute = %d, want %d", got, want)
	}
	if got, want := spec.LaunchConfig.ScheduleTrafficPercentage, 1; got != want {
		t.Fatalf("ScheduleTrafficPercentage = %d, want %d", got, want)
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

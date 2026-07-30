// sim/launch_config_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import "testing"

func TestMakeLaunchConfigScheduleDefaults(t *testing.T) {
	lc := MakeLaunchConfig(nil, 1, 0, nil, nil, false)

	if lc.TrafficSource != TrafficSourceRandom {
		t.Fatalf("TrafficSource = %v, want TrafficSourceRandom", lc.TrafficSource)
	}
	if lc.ScheduleID != "" {
		t.Fatalf("ScheduleID = %q, want empty", lc.ScheduleID)
	}
	if lc.ScheduleStartMinute != 0 {
		t.Fatalf("ScheduleStartMinute = %d, want 0", lc.ScheduleStartMinute)
	}
	if lc.ScheduleArrivalPercentage != 100 {
		t.Fatalf(
			"ScheduleArrivalPercentage = %d, want 100",
			lc.ScheduleArrivalPercentage,
		)
	}

	if lc.ScheduleDeparturePercentage != 100 {
		t.Fatalf(
			"ScheduleDeparturePercentage = %d, want 100",
			lc.ScheduleDeparturePercentage,
		)
	}
}

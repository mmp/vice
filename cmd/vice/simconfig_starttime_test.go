// simconfig_starttime_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
	"time"

	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func day(month time.Month, d, hour int) time.Time {
	return time.Date(2026, month, d, hour, 0, 0, 0, time.UTC)
}

func TestStartTimeIntervals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   sim.TrafficSource
		wx       []util.TimeInterval
		flights  []util.TimeInterval
		expected []util.TimeInterval
	}{
		{
			// Only historical traffic comes from the flight data, so nothing
			// else has any reason to be narrowed by it.
			name:     "scenario traffic ignores the flight data",
			source:   sim.TrafficSourceScenario,
			wx:       []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			flights:  []util.TimeInterval{{day(time.May, 4, 0), day(time.May, 5, 0)}},
			expected: []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
		},
		{
			// The May 2026 outage: the data stops on the 4th and comes back
			// mid-afternoon on the 7th, so those days can't be started in.
			name:   "a gap in the flight data is not offered",
			source: sim.TrafficSourceHistorical,
			wx:     []util.TimeInterval{{day(time.May, 1, 0), day(time.May, 10, 0)}},
			flights: []util.TimeInterval{
				{day(time.May, 1, 0), day(time.May, 4, 23)},
				{day(time.May, 7, 18), day(time.May, 10, 0)},
			},
			expected: []util.TimeInterval{
				{day(time.May, 1, 0), day(time.May, 3, 23)},
				{day(time.May, 7, 18), day(time.May, 9, 0)},
			},
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
			spec := &server.ScenarioSpec{HistoricalFlightIntervals: tc.flights}
			spec.LaunchConfig.TrafficSource = tc.source

			got := c.startTimeIntervals(spec)
			if len(got) != len(tc.expected) {
				t.Fatalf("got %v, expected %v", got, tc.expected)
			}
			for i := range got {
				if !got[i].Start().Equal(tc.expected[i].Start()) ||
					!got[i].End().Equal(tc.expected[i].End()) {
					t.Errorf("interval %d: got %v, expected %v", i, got[i], tc.expected[i])
				}
			}
		})
	}
}

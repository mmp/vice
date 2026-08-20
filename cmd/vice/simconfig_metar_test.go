// simconfig_metar_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/wx"
)

// metarConfig is a dialog with METAR on hand for the given airports, in the
// state metarAirportsByTraffic reads.
func metarConfig(airports ...string) *NewSimConfiguration {
	c := &NewSimConfiguration{airportMETAR: make(map[string][]wx.METAR)}
	for _, ap := range airports {
		c.airportMETAR[ap] = []wx.METAR{{ICAO: ap}}
	}
	return c
}

func TestMetarAirportsByTraffic(t *testing.T) {
	// KZZZ is the busy airport in every case below, so alphabetical order--the
	// old behavior, and the tie-break--is the order the ranking has to undo.
	scenarioSpec := &server.ScenarioSpec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:      sim.TrafficSourceScenario,
			DepartureRateScale: 1,
			DepartureRates: map[string]map[av.RunwayID]map[string]float32{
				"KAAA": {"18": {"": 5}},
				"KZZZ": {"36": {"": 30}},
			},
		},
	}
	timetableSpec := &server.ScenarioSpec{
		LaunchConfig: sim.LaunchConfig{
			TrafficSource:    sim.TrafficSourceTimetable,
			TimetableAirport: "KZZZ",
		},
	}
	historicalSpec := &server.ScenarioSpec{
		LaunchConfig: sim.LaunchConfig{TrafficSource: sim.TrafficSourceHistorical},
	}

	for _, tc := range []struct {
		name       string
		spec       *server.ScenarioSpec
		operations map[string]int
		want       []string
		wantOK     bool
	}{
		{"scenario rates", scenarioSpec, nil, []string{"KZZZ", "KAAA", "KBBB"}, true},
		{"timetable airport", timetableSpec, nil, []string{"KZZZ", "KAAA", "KBBB"}, true},
		{"historical before counts", historicalSpec, nil, []string{"KAAA", "KBBB", "KZZZ"}, false},
		{"historical counts", historicalSpec, map[string]int{"KZZZ": 40, "KBBB": 10},
			[]string{"KZZZ", "KBBB", "KAAA"}, true},
		{"historical quiet window", historicalSpec, map[string]int{},
			[]string{"KAAA", "KBBB", "KZZZ"}, true},
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

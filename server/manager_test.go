// server/manager_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"
	"io"
	"testing"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"

	"log/slog"
)

// A request names its scenario, and nothing has vetted the name by the time it
// arrives. A group is catalogued only if it has a scenario that can be flown,
// so neither the catalog nor the scenario in it can be taken on faith.
func TestMakeSimConfigurationRejectsUnknownScenario(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var sm SimManager
	sm.scenarios.Store(&scenario.Tables{
		Groups: map[string]map[string]*scenario.Group{
			"ABC": {"Group": {}},
		},
		Catalogs: map[string]map[string]*scenario.Catalog{
			"ABC": {"Group": {Scenarios: map[string]*scenario.Spec{"Known": {}}}},
		},
	})

	for _, tc := range []struct{ facility, group, scenario string }{
		{"ABC", "Group", "Unknown"}, // catalogued group, scenario that isn't in it
		{"ABC", "Uncatalogued", "Known"},
		{"XYZ", "Group", "Known"},
	} {
		req := &NewSimRequest{Facility: tc.facility, GroupName: tc.group, ScenarioName: tc.scenario,
			ScenarioSpec: &scenario.Spec{}}
		_, err := sm.makeSimConfiguration(req, lg)
		if !errors.Is(err, ErrInvalidSimConfiguration) {
			t.Errorf("%s/%s/%s: error is %v, want %v", tc.facility, tc.group, tc.scenario,
				err, ErrInvalidSimConfiguration)
		}
	}
}

// A scenario the catalog has can still be asked for with traffic it doesn't
// offer, which is the server's call to make and not the client's.
func TestMakeSimConfigurationRejectsUnofferedTraffic(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var sm SimManager
	sm.scenarios.Store(&scenario.Tables{
		Groups: map[string]map[string]*scenario.Group{
			"ABC": {"Group": {}},
		},
		Catalogs: map[string]map[string]*scenario.Catalog{
			"ABC": {"Group": {Scenarios: map[string]*scenario.Spec{
				"Known": {TrafficSources: []sim.TrafficSource{sim.TrafficSourceScenario}},
			}}},
		},
	})

	req := &NewSimRequest{Facility: "ABC", GroupName: "Group", ScenarioName: "Known",
		ScenarioSpec: &scenario.Spec{}}
	req.ScenarioSpec.LaunchConfig.TrafficSource = sim.TrafficSourceHistorical
	if _, err := sm.makeSimConfiguration(req, lg); !errors.Is(err, ErrInvalidTrafficSource) {
		t.Errorf("error is %v, want %v", err, ErrInvalidTrafficSource)
	}
}

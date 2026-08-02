// server/scenario_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"os"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func TestMain(m *testing.M) {
	// Parsing a route consults the airway and fix databases, and loading the
	// built-in scenarios needs all of it.
	av.InitDB()
	os.Exit(m.Run())
}

// The scenarios Vice ships with are the real test of the background-traffic
// classification (sim.MarkBackgroundTraffic; its unit tests live with it):
// these are cases checked by hand against the scenario and facility
// configuration files, and they are what would quietly go wrong if the walk
// drifted from what the sim does at spawn.
func TestBackgroundTrafficInBuiltinScenarios(t *testing.T) {
	var e util.ErrorLogger
	_, catalogs, _, _, _ := LoadScenarioGroups("", "", "", &e, log.New(false, "error", ""))
	if e.HaveErrors() {
		t.Fatal("built-in scenarios failed to load")
	}

	launchConfig := func(facility, group, scenario string) *sim.LaunchConfig {
		t.Helper()
		catalog, ok := catalogs[facility][group]
		if !ok {
			t.Fatalf("no scenario catalog for %s/%s", facility, group)
		}
		spec, ok := catalog.Scenarios[scenario]
		if !ok {
			t.Fatalf("no scenario %s/%s/%s", facility, group, scenario)
		}
		return &spec.LaunchConfig
	}

	// A departure position: it works every departure and none of the arrivals,
	// which the scenario lands for realism on its ".AUTO" flows.
	dfw := launchConfig("D10", "D10", "DEPARTURE SF")
	if got, want := dfw.WorkedDepartureRate(), dfw.TotalDepartureRate(); got != want {
		t.Errorf("D10 DEPARTURE SF works %v/hr of %v/hr departures, want all of them", got, want)
	}
	if got := dfw.WorkedArrivalRate(); got != 0 {
		t.Errorf("D10 DEPARTURE SF works %v/hr of arrivals, want none: they are all background", got)
	}
	if dfw.TotalArrivalRate() == 0 {
		t.Error("D10 DEPARTURE SF has no arrivals at all, so the case above proves nothing")
	}

	// LGA works its neighbors' departures: KEWR and KJFK start with virtual
	// controllers but their routes hand off to 1L, which is an LGA position.
	lga := launchConfig("N90", "KLGA", "LGA Dep 13 Land 22 HPN 16")
	for _, airport := range []string{"KLGA", "KEWR", "KJFK", "KHPN"} {
		for runway, categories := range lga.DepartureRates[airport] {
			for category := range categories {
				if lga.DepartureIsBackground(airport, runway, category) {
					t.Errorf("LGA scenario calls %s/%s/%q background, but it hands off to 1L",
						airport, runway, category)
				}
			}
		}
	}

	// The other side of the same airspace: an EWR departure position works no
	// LGA traffic, and the scenario's background runway variant is background.
	ewr := launchConfig("N90", "KEWR", "KEWR 4L Departure")
	if !ewr.DepartureIsBackground("KLGA", "31", "") {
		t.Error("KEWR 4L Departure should not work KLGA's departures")
	}
	if ewr.DepartureIsBackground("KEWR", "4L", "") {
		t.Error("KEWR 4L Departure works its own departures")
	}
	if got := ewr.WorkedArrivalRate(); got != 0 {
		t.Errorf("KEWR 4L Departure works %v/hr of arrivals, want none", got)
	}
}

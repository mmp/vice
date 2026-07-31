// sim/launch_config_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestMakeLaunchConfigTrafficSourceDefaults(t *testing.T) {
	lc := MakeLaunchConfig(nil, 1, 0, nil, nil, false)

	if lc.TrafficSource != TrafficSourceScenario {
		t.Fatalf("TrafficSource = %v, want TrafficSourceScenario", lc.TrafficSource)
	}
	if lc.TimetableID != "" {
		t.Fatalf("TimetableID = %q, want empty", lc.TimetableID)
	}
	if lc.TimetableStartMinute != 0 {
		t.Fatalf("TimetableStartMinute = %d, want 0", lc.TimetableStartMinute)
	}
	if lc.PublishedArrivalPercentage != 100 {
		t.Fatalf(
			"PublishedArrivalPercentage = %d, want 100",
			lc.PublishedArrivalPercentage,
		)
	}

	if lc.PublishedDeparturePercentage != 100 {
		t.Fatalf(
			"PublishedDeparturePercentage = %d, want 100",
			lc.PublishedDeparturePercentage,
		)
	}
}

// Departure enables mirror whether each runway's default rate is non-zero.
// Inbound flows don't: every flow a scenario lists for an airport is a way into
// it whatever its rate, since the enables are what published arrivals consult
// and those arrive when their data says. Overflights are left out entirely;
// they stay randomly generated.
func TestMakeLaunchConfigEnabledDefaults(t *testing.T) {
	dep := []DepartureRunway{
		{Airport: "KJFK", Runway: "13R", Category: "North", DefaultRate: 6},
		{Airport: "KJFK", Runway: "13R", Category: "Water", DefaultRate: 0},
		{Airport: "KLGA", Runway: "13", Category: "", DefaultRate: 10},
	}
	inbound := map[string]map[string]float32{
		"PROUD": {"KLGA": 20, "KJFK": 0},
		"CAMRN": {"KJFK": 15, "overflights": 5},
	}

	lc := MakeLaunchConfig(dep, 1, 0, nil, inbound, false)

	depWant := map[string]map[av.RunwayID]map[string]bool{
		"KJFK": {"13R": {"North": true, "Water": false}},
		"KLGA": {"13": {"": true}},
	}
	if !departureEnabledEqual(lc.DepartureEnabled, depWant) {
		t.Errorf("DepartureEnabled = %v, want %v", lc.DepartureEnabled, depWant)
	}

	inboundWant := map[string]map[string]bool{
		// KJFK is listed at rate zero and is enabled all the same.
		"PROUD": {"KLGA": true, "KJFK": true},
		"CAMRN": {"KJFK": true},
	}
	for flow, want := range inboundWant {
		for ap, enabled := range want {
			if lc.InboundFlowEnabled[flow][ap] != enabled {
				t.Errorf("InboundFlowEnabled[%s][%s] = %v, want %v",
					flow, ap, lc.InboundFlowEnabled[flow][ap], enabled)
			}
		}
	}
	if _, ok := lc.InboundFlowEnabled["CAMRN"]["overflights"]; ok {
		t.Errorf("InboundFlowEnabled should not include overflights")
	}
}

func TestDepartureEnabledEqual(t *testing.T) {
	a := map[string]map[av.RunwayID]map[string]bool{
		"KJFK": {"13R": {"North": true, "Water": false}},
	}
	b := map[string]map[av.RunwayID]map[string]bool{
		"KJFK": {"13R": {"North": true, "Water": false}},
	}
	if !departureEnabledEqual(a, b) {
		t.Errorf("equal maps reported unequal")
	}

	b["KJFK"]["13R"]["Water"] = true
	if departureEnabledEqual(a, b) {
		t.Errorf("maps with different values reported equal")
	}

	b["KJFK"]["13R"]["Water"] = false
	b["KJFK"]["31L"] = map[string]bool{"North": true}
	if departureEnabledEqual(a, b) {
		t.Errorf("maps with different keys reported equal")
	}
}

// sim/launch_config_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
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
	if lc.PublishedArrivalRateScale != 1 {
		t.Fatalf(
			"PublishedArrivalRateScale = %f, want 1",
			lc.PublishedArrivalRateScale,
		)
	}

	if lc.PublishedDepartureRateScale != 1 {
		t.Fatalf(
			"PublishedDepartureRateScale = %f, want 1",
			lc.PublishedDepartureRateScale,
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

// backgroundRateConfig is a launch config with traffic at two airports, one of
// which the scenario flies purely for realism.
func backgroundRateConfig() *LaunchConfig {
	return &LaunchConfig{
		DepartureRateScale:   1,
		InboundFlowRateScale: 1,
		DepartureRates: map[string]map[av.RunwayID]map[string]float32{
			"KMSP": {"30L": {"": 20}},
			"KSTP": {"32": {"": 5}},
		},
		DepartureBackground: map[string]map[av.RunwayID]map[string]bool{
			"KSTP": {"32": {"": true}},
		},
		InboundFlowRates: map[string]map[string]float32{
			"WORKED":     {"KMSP": 30, "overflights": 4},
			"BACKGROUND": {"KMSP": 12, "overflights": 6},
		},
		InboundFlowBackground: map[string]map[string]bool{
			"BACKGROUND": {"KMSP": true, "overflights": true},
		},
	}
}

// The Worked rates leave out the traffic nobody works; the Total ones, which
// bound what the sim generates, count all of it.
func TestWorkedRatesExcludeBackgroundTraffic(t *testing.T) {
	lc := backgroundRateConfig()

	for _, test := range []struct {
		name       string
		total      float32
		worked     float32
		wantTotal  float32
		wantWorked float32
	}{
		{"departures", lc.TotalDepartureRate(), lc.WorkedDepartureRate(), 25, 20},
		{"arrivals", lc.TotalArrivalRate(), lc.WorkedArrivalRate(), 42, 30},
		{"overflights", lc.TotalOverflightRate(), lc.WorkedOverflightRate(), 10, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.total != test.wantTotal {
				t.Errorf("total %s rate = %v, want %v", test.name, test.total, test.wantTotal)
			}
			if test.worked != test.wantWorked {
				t.Errorf("worked %s rate = %v, want %v", test.name, test.worked, test.wantWorked)
			}
		})
	}

	// The rate scales apply to both alike.
	lc.DepartureRateScale, lc.InboundFlowRateScale = 2, 2
	if got := lc.WorkedDepartureRate(); got != 40 {
		t.Errorf("scaled worked departure rate = %v, want 40", got)
	}
	if got := lc.WorkedArrivalRate(); got != 60 {
		t.Errorf("scaled worked arrival rate = %v, want 60", got)
	}
}

// WorkedAirportRates splits the worked departure and arrival rates by airport,
// applying the rate scales and leaving out background traffic, overflights, and
// with them any airport with nothing worked at all.
func TestWorkedAirportRates(t *testing.T) {
	lc := backgroundRateConfig()

	if got, want := lc.WorkedAirportRates(), map[string]float32{"KMSP": 50}; !maps.Equal(got, want) {
		t.Errorf("WorkedAirportRates = %v, want %v", got, want)
	}

	lc.DepartureRateScale, lc.InboundFlowRateScale = 2, 2
	if got, want := lc.WorkedAirportRates(), map[string]float32{"KMSP": 100}; !maps.Equal(got, want) {
		t.Errorf("scaled WorkedAirportRates = %v, want %v", got, want)
	}
}

// A config nobody classified--one built without a scenario to walk--has to go on
// reporting all of its traffic rather than none of it.
func TestWorkedRatesWithoutClassification(t *testing.T) {
	lc := backgroundRateConfig()
	lc.DepartureBackground, lc.InboundFlowBackground = nil, nil

	if got, want := lc.WorkedDepartureRate(), lc.TotalDepartureRate(); got != want {
		t.Errorf("worked departure rate = %v, want the total %v", got, want)
	}
	if got, want := lc.WorkedArrivalRate(), lc.TotalArrivalRate(); got != want {
		t.Errorf("worked arrival rate = %v, want the total %v", got, want)
	}
	if got, want := lc.WorkedOverflightRate(), lc.TotalOverflightRate(); got != want {
		t.Errorf("worked overflight rate = %v, want the total %v", got, want)
	}
}

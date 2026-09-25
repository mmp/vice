// eram/prefs_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/sim"
)

func TestParseAltitudeBlock(t *testing.T) {
	for _, s := range []string{"000B999", "100B230", "234B400", "050B050", "230B100"} {
		block, ok := parseAltitudeBlock(s)
		if !ok {
			t.Errorf("%q: rejected", s)
		} else if got := formatAltitudeBlock(block); got != s {
			t.Errorf("%q: formatted back as %q", s, got)
		}
	}

	// Malformed entries are rejected, so QD gives FORMAT.
	for _, s := range []string{"", "100B23", "100B2300", "AAAAAAA", "100X230", "10BB230",
		"1 0B230"} {
		if _, ok := parseAltitudeBlock(s); ok {
			t.Errorf("%q: accepted", s)
		}
	}
}

func TestAltitudeLimitsFilterRange(t *testing.T) {
	ep := &Scope{prefSet: &PrefrenceSet{}}
	for _, limits := range [][2]int{{0, 999}, {100, 230}, {234, 400}} {
		if err := handleAltitudeLimitsFilter(ep, limits); err != nil {
			t.Errorf("%v: %v", limits, err)
		} else if f := ep.currentPrefs().AltitudeLimits; f.Targets != limits || f.LDBs != limits {
			t.Errorf("%v: filters set to %v and %v", limits, f.Targets, f.LDBs)
		}
	}

	// Degenerate and inverted ranges leave the filters as they were.
	for _, limits := range [][2]int{{0, 0}, {50, 50}, {200, 100}} {
		if err := handleAltitudeLimitsFilter(ep, limits); err != ErrInvalidAltitudeLimits {
			t.Errorf("%v: got error %v", limits, err)
		}
		if f := ep.currentPrefs().AltitudeLimits; f.Targets != [2]int{234, 400} {
			t.Errorf("%v: filter changed to %v", limits, f.Targets)
		}
	}
}

func TestAltitudeInLimits(t *testing.T) {
	limits := [2]int{234, 400}
	for _, alt := range []float32{23350, 23400, 30000, 40049} {
		if !altitudeInLimits(alt, limits) {
			t.Errorf("%.0f: filtered out of %v", alt, limits)
		}
	}
	for _, alt := range []float32{0, 23349, 40050, 45000} {
		if altitudeInLimits(alt, limits) {
			t.Errorf("%.0f: let through %v", alt, limits)
		}
	}
}

func TestResetAppliesAdaptedAltitudeLimits(t *testing.T) {
	// stateFor returns the state for a user signed in to sector 08 with the
	// given limits adapted for that position.
	stateFor := func(limits sim.AltitudeLimits) client.SimState {
		var ss client.SimState
		ss.UserTCW = "08"
		ss.FacilityAdaptation.Controllers = map[sim.ControlPosition]*sim.STARSController{
			"08": {AltitudeLimits: limits},
		}
		return ss
	}

	// Nothing adapted: the filters let everything through and stay combined.
	p := makeDefaultPreferences()
	p.Reset(stateFor(sim.AltitudeLimits{}))
	if p.AltitudeLimits.Targets != sim.UnrestrictedAltitudeLimits ||
		p.AltitudeLimits.LDBs != sim.UnrestrictedAltitudeLimits || p.AltitudeLimits.Split {
		t.Errorf("unadapted: got %+v", p.AltitudeLimits)
	}

	// A combined filter is applied to both and stays combined.
	p = makeDefaultPreferences()
	p.Reset(stateFor(sim.AltitudeLimits{Combined: [2]int{0, 230}}))
	if p.AltitudeLimits.Targets != [2]int{0, 230} || p.AltitudeLimits.LDBs != [2]int{0, 230} ||
		p.AltitudeLimits.Split {
		t.Errorf("combined: got %+v", p.AltitudeLimits)
	}

	// Filters that differ come up split.
	p = makeDefaultPreferences()
	p.Reset(stateFor(sim.AltitudeLimits{Targets: [2]int{240, 999}, LDBs: [2]int{50, 180}}))
	if p.AltitudeLimits.Targets != [2]int{240, 999} || p.AltitudeLimits.LDBs != [2]int{50, 180} ||
		!p.AltitudeLimits.Split {
		t.Errorf("split: got %+v", p.AltitudeLimits)
	}

	// A filter the controller adapts on its own splits from the unrestricted one.
	p = makeDefaultPreferences()
	p.Reset(stateFor(sim.AltitudeLimits{Targets: [2]int{240, 999}}))
	if p.AltitudeLimits.Targets != [2]int{240, 999} ||
		p.AltitudeLimits.LDBs != sim.UnrestrictedAltitudeLimits || !p.AltitudeLimits.Split {
		t.Errorf("targets only: got %+v", p.AltitudeLimits)
	}

	// Reset also brings the scope range along; with nothing adapted it is the default.
	if p.Range != defaultERAMRange {
		t.Errorf("range: got %v, expected %v", p.Range, float32(defaultERAMRange))
	}
}

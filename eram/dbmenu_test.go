// eram/dbmenu_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// The altitude menu's FP row shows the assigned altitude in the form it was
// assigned, whenever there is an interim altitude or the assigned altitude
// is not a single altitude.
func TestAltitudeMenuFPLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		fp   sim.FlightPlan
		want string // "" when the row is not shown
	}{
		{"single altitude", sim.FlightPlan{AssignedAltitude: 35000}, ""},
		{"interim", sim.FlightPlan{AssignedAltitude: 35000, InterimAlt: 24000}, "FP 350"},
		{"local interim", sim.FlightPlan{AssignedAltitude: 35000, LocalInterimAlt: 24000}, "FP 350"},
		{"descending via a STAR", sim.FlightPlan{PerceivedAssigned: 9000, InterimAlt: 11000}, "FP 090"},
		{"block", sim.FlightPlan{AltitudeBlock: [2]int{22000, 23000}}, "FP 220B230"},
		{"block and interim", sim.FlightPlan{AltitudeBlock: [2]int{22000, 23000}, InterimAlt: 21000}, "FP 220B230"},
		{"VFR", sim.FlightPlan{AssignedAltitudeQualifier: sim.VFRAltitude}, "FP VFR"},
		{"VFR-on-top", sim.FlightPlan{AssignedAltitude: 10500, AssignedAltitudeQualifier: sim.OTPAltitude},
			"FP OTP/105"},
	} {
		label, ok := altitudeMenuFPLabel(&tc.fp)
		if ok != (tc.want != "") || label != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, label, ok, tc.want)
		}
	}
}

// The altitude menu lists altitudes 1,000 feet apart up to FL410 and 2,000
// feet apart above it.
func TestAltitudeMenuAlts(t *testing.T) {
	for i := 1; i < len(altitudeMenuAlts); i++ {
		hi, lo := altitudeMenuAlts[i-1], altitudeMenuAlts[i]
		if step := util.Select(lo >= 410, 20, 10); hi-lo != step {
			t.Errorf("%03d follows %03d; expected a step of %d", lo, hi, step)
		}
	}
	if first, last := altitudeMenuAlts[0], altitudeMenuAlts[len(altitudeMenuAlts)-1]; first != 590 || last != 10 {
		t.Errorf("list runs from %03d to %03d; expected 590 to 010", first, last)
	}
}

// In turn mode the heading menu lists the eleven turns of 5 to 55 degrees,
// two to a row as figures 16-2 and 16-3 show.
func TestHeadingMenuTurnRows(t *testing.T) {
	want := [][2]string{{"50L", "55L"}, {"40L", "45L"}, {"30L", "35L"}, {"20L", "25L"}, {"10L", "15L"}, {"", "5L"}}
	if got := headingMenuRows(true, false); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A stored heading gives the closest listed value current value emphasis.
func TestHeadingMenuCurrentValue(t *testing.T) {
	for _, tc := range []struct {
		heading string
		lt, rt  bool
		want    string
	}{
		{"245", false, false, "245"},
		{"247", false, false, "245"},
		{"248", false, false, "250"},
		{"358", false, false, "360"},
		{"002", false, false, "360"},
		{"003", false, false, "005"},
		{"000", false, false, ""},
		{"361", false, false, ""},
		{"45", false, false, ""},
		{"PH", false, false, ""},
		{"30L", false, false, ""},
		{"30L", true, false, "30L"},
		{"23L", true, false, "25L"},
		{"22L", true, false, "20L"},
		{"4L", true, false, ""},
		{"75L", true, false, ""},
		{"30R", true, false, ""},
		{"53R", false, true, "55R"},
	} {
		if got := headingMenuCurrentValue(tc.heading, tc.lt, tc.rt); got != tc.want {
			t.Errorf("%q (LT %v, RT %v): got %q, want %q", tc.heading, tc.lt, tc.rt, got, tc.want)
		}
	}
}

// The speed menu opens in the units of the assigned speed or, with none, in
// Mach at or above FL290.
func TestSpeedMenuOpensInMach(t *testing.T) {
	for _, tc := range []struct {
		name string
		fp   sim.FlightPlan
		want bool
	}{
		{"FL350", sim.FlightPlan{AssignedAltitude: 35000}, true},
		{"FL290", sim.FlightPlan{AssignedAltitude: 29000}, true},
		{"FL280", sim.FlightPlan{AssignedAltitude: 28000}, false},
		{"interim below FL290", sim.FlightPlan{AssignedAltitude: 35000, InterimAlt: 28000}, false},
		{"block", sim.FlightPlan{AltitudeBlock: [2]int{30000, 34000}}, true},
		{"assigned speed", sim.FlightPlan{AssignedAltitude: 35000, SecondaryScratchpad: "S250"}, false},
		{"assigned Mach", sim.FlightPlan{AssignedAltitude: 28000, SecondaryScratchpad: "M78"}, true},
	} {
		if got := speedMenuOpensInMach(&tc.fp); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The speed menu lists 380 to 70 knots and Mach .99 to .61.
func TestSpeedMenuValues(t *testing.T) {
	for _, tc := range []struct {
		mach        bool
		n           int
		first, last speedValue
	}{
		{false, 32, speedValue{"S380", "380"}, speedValue{"S070", "70"}},
		{true, 39, speedValue{"M99", ".99"}, speedValue{"M61", ".61"}},
	} {
		v := speedMenuValues(tc.mach)
		if len(v) != tc.n || v[0] != tc.first || v[len(v)-1] != tc.last {
			t.Errorf("Mach %v: got %d values from %v to %v; want %d from %v to %v", tc.mach, len(v), v[0],
				v[len(v)-1], tc.n, tc.first, tc.last)
		}
	}
}

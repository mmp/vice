// eram/dbmenu_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	"github.com/mmp/vice/sim"
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

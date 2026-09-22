// eram/prefs_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import "testing"

func TestParseAltitudeLimits(t *testing.T) {
	for _, s := range []string{"000B999", "100B230", "234B400", "050B050"} {
		limits, ok := parseAltitudeLimits(s)
		if !ok {
			t.Errorf("%q: rejected", s)
		} else if got := formatAltitudeLimits(limits); got != s {
			t.Errorf("%q: formatted back as %q", s, got)
		}
	}

	// Malformed entries and inverted ranges are both rejected; the entry box
	// restores the previous filter and QD gives FORMAT.
	for _, s := range []string{"", "100B23", "100B2300", "AAAAAAA", "100X230", "10BB230",
		"1 0B230", "230B100"} {
		if _, ok := parseAltitudeLimits(s); ok {
			t.Errorf("%q: accepted", s)
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

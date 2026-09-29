// aviation/db/lookups_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"testing"

	"github.com/mmp/vice/math"
)

// TestLocateFRD checks that a fix-radial-distance names the point that far out
// along the fix's magnetic radial. A VHF navaid's radials are referenced to
// its station declination: JFK's is 12W, so its 090 radial runs 078 true.
func TestLocateFRD(t *testing.T) {
	InitDB()

	jfk, _ := Lookups{}.Locate("JFK")
	p, ok := Lookups{}.Locate("jfk090020")
	if !ok {
		t.Fatal("JFK090020 doesn't locate")
	}
	if d := math.NMDistance2LL(jfk, p); math.Abs(d-20) > 0.2 {
		t.Errorf("JFK090020 is %.2fnm from JFK", d)
	}
	if hdg := math.Heading2LL(jfk, p, math.NMPerLongitudeAt(jfk)); math.Abs(float32(hdg)-78) > 0.5 {
		t.Errorf("JFK090020 is %.1f true from JFK, want 078", hdg)
	}

	for _, s := range []string{
		"J090020",     // one letter
		"JFK09002",    // five digits
		"JFK0900200",  // seven digits
		"JFK09A020",   // not digits
		"JFK361020",   // no such radial
		"ZZZZZ090020", // no such fix
	} {
		if _, ok := (Lookups{}).Locate(s); ok {
			t.Errorf("%s located", s)
		}
	}
}

// TestFRDRoundTrip checks that an FRD vice writes for a position names it
// again when it is read back, to within the whole degree and nm it gives,
// whether the fix is a navaid with a station declination or a fix without one.
func TestFRDRoundTrip(t *testing.T) {
	InitDB()

	for _, fix := range []string{"JFK", "MERIT"} {
		loc, ok := Lookups{}.Locate(fix)
		if !ok {
			t.Fatalf("%s doesn't locate", fix)
		}
		for _, hdg := range []float32{0, 45, 179, 270, 359.8} {
			p := math.Offset2LL(loc, math.TrueHeading(hdg), 23.3, math.NMPerLongitudeAt(loc))
			frd, ok := FormatFRD(fix, loc, p)
			if !ok {
				t.Errorf("%s: no FRD for %v", fix, p)
				continue
			}
			if q, ok := (Lookups{}).Locate(frd); !ok {
				t.Errorf("%s doesn't locate", frd)
			} else if d := math.NMDistance2LL(p, q); d > 0.6 {
				t.Errorf("%s is %.2fnm from the position it was written for", frd, d)
			}
		}
	}
}

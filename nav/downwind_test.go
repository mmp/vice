// nav/downwind_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
)

// downwindFlight returns a jet at alt feet on the downwind heading, offset
// by hdgOffset degrees, at a point abeam nm out from the KSFO 28L threshold
// along the final and lateral nm to its right, looking in the landing
// direction, expecting the visual.
func downwindFlight(t *testing.T, abeam, lateral, hdgOffset, alt float32) *FlightTest {
	t.Helper()
	rwy, ok := av.LookupRunway(db.Lookups{}, "KSFO", "28L")
	if !ok {
		t.Fatal("KSFO 28L not found")
	}
	nmPerLong := math.NMPerLongitudeAt(rwy.Threshold)
	magVar, err := db.DB.MagneticGrid.Lookup(rwy.Threshold)
	if err != nil {
		t.Fatal(err)
	}
	landing := float32(math.MagneticToTrue(rwy.Heading, magVar))
	p := math.Offset2LL(rwy.Threshold, math.TrueHeading(math.NormalizeHeading(landing+180)), abeam, nmPerLong)
	p = math.Offset2LL(p, math.TrueHeading(math.NormalizeHeading(landing+90)), lateral, nmPerLong)
	hdg := math.TrueToMagnetic(math.TrueHeading(math.NormalizeHeading(landing+180+hdgOffset)), magVar)
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        p.DMSString(),
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KSFO",
		AircraftType:     "A320",
		InitialAltitude:  alt,
		InitialSpeed:     210,
		AssignedAltitude: alt,
		InitialHeading:   float32(hdg),
	})
	f.ExpectVisualApproach("28L")
	return f
}

// TestVisualFromDownwindFliesPattern checks that an aircraft cleared for the
// visual on a downwind flies the pattern: abeam 6 nm at 3,000 ft it turns
// base right away onto a 6 nm final, on either side of the final; abeam
// the threshold or past it, it flies on to abeam NEPIC, 3.4 nm out, and
// turns base there; at 6,000 ft abeam the threshold the pattern is
// extended until it is long enough to descend on the glidepath, so the
// join is 7.4 nm out.
func TestVisualFromDownwindFliesPattern(t *testing.T) {
	for _, tc := range []struct {
		name                string
		abeam, lateral, alt float32
		route               []string // through the join
		joinOut             float32
	}{
		{"left downwind abeam 6", 6, -4, 3000, []string{"_28L_FINAL_TURN"}, 6},
		{"right downwind abeam 6", 6, 4, 3000, []string{"_28L_FINAL_TURN"}, 6},
		{"left downwind abeam the threshold", 0, -4, 3000, []string{"_28L_BASE_TURN", "NEPIC"}, 3.36},
		{"past the threshold", -3, -4, 3000, []string{"_28L_BASE_TURN", "NEPIC"}, 3.36},
		{"high abeam the threshold", 0, -4, 6000, []string{"_28L_BASE_TURN", "_28L_FINAL_TURN"}, 7.4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := downwindFlight(t, tc.abeam, tc.lateral, 0, tc.alt)
			if intent, unable := f.ClearedVisualApproach("28L").(speech.UnableIntent); unable {
				t.Fatalf("unexpected unable: %v", intent)
			}
			fixes := navRouteFixes(f.nav)
			if len(fixes) < len(tc.route) || !slices.Equal(fixes[:len(tc.route)], tc.route) || !slices.Contains(fixes, "_28L_3NM_FINAL") {
				t.Fatalf("route = %v, want it to start %v and go through the 3 nm final", fixes, tc.route)
			}
			joinIdx := len(tc.route) - 1
			rwy, _ := av.LookupRunway(db.Lookups{}, "KSFO", "28L")
			if d := math.NMDistance2LL(f.nav.Waypoints[joinIdx].Location, rwy.Threshold); math.Abs(d-tc.joinOut) > 0.15 {
				t.Errorf("join %s is %.2f nm from the threshold, want %.1f", fixes[joinIdx], d, tc.joinOut)
			}
			// A base turn is ahead on the heading, abeam the join.
			nmPerLong := f.nav.FlightState.NmPerLongitude
			pos := math.LL2NM(f.nav.FlightState.Position, nmPerLong)
			join := math.LL2NM(f.nav.Waypoints[joinIdx].Location, nmPerLong)
			hdg := math.HeadingVector(math.MagneticToTrue(f.nav.FlightState.Heading, f.nav.FlightState.MagneticVariation))
			if fixes[0] == "_28L_BASE_TURN" {
				base := math.LL2NM(f.nav.Waypoints[0].Location, nmPerLong)
				if ahead := math.Dot(math.Sub2f(base, pos), hdg); ahead < 0.5 ||
					math.Abs(math.SignedPointLineDistance(base, pos, math.Add2f(pos, hdg))) > 0.05 {
					t.Errorf("base turn %.2f nm ahead and %.2f nm off the heading; want on it",
						ahead, math.SignedPointLineDistance(base, pos, math.Add2f(pos, hdg)))
				}
				if d := math.Abs(math.Dot(math.Sub2f(join, base), hdg)); d > 0.05 {
					t.Errorf("base turn is %.2f nm from abeam the join", d)
				}
			} else if d := math.Abs(math.Dot(math.Sub2f(join, pos), hdg)); d > 0.15 {
				t.Errorf("join is %.2f nm from abeam the aircraft, which should turn base right away", d)
			}
			f.AtFix(fixes[joinIdx], func(f *FlightTest) {})
			f.AtFix("_28L_3NM_FINAL", func(f *FlightTest) {})
			f.Run()
		})
	}
}

// TestVisualOnBaseFliesToFinal checks that the pattern isn't flown by an
// aircraft already heading across toward the final with the runway ahead
// of it: from 4 nm left of the final 8 nm out, heading 90 degrees off the
// landing direction toward it, it flies to the 3 nm final, which lies 3 nm
// from the threshold.
func TestVisualOnBaseFliesToFinal(t *testing.T) {
	f := downwindFlight(t, 8, -4, -90, 3000)
	if intent, unable := f.ClearedVisualApproach("28L").(speech.UnableIntent); unable {
		t.Fatalf("unexpected unable: %v", intent)
	}
	fixes := navRouteFixes(f.nav)
	if fixes[0] != "_28L_3NM_FINAL" {
		t.Fatalf("route = %v, want the 3 nm final first", fixes)
	}
	rwy, _ := av.LookupRunway(db.Lookups{}, "KSFO", "28L")
	if d := math.NMDistance2LL(f.nav.Waypoints[0].Location, rwy.Threshold); math.Abs(d-3) > 0.1 {
		t.Errorf("3 nm final is %.2f nm from the threshold", d)
	}
}

// TestVisualInsideShortFinalIsUnable checks that an aircraft already inside
// the 3 nm final, pointed at the runway, is refused the visual rather than
// turned back for the 3 nm point behind it.
func TestVisualInsideShortFinalIsUnable(t *testing.T) {
	f := downwindFlight(t, 1, -1, 180+30, 1500)
	intent := f.ClearedVisualApproach("28L")
	if _, unable := intent.(speech.UnableIntent); !unable {
		t.Fatalf("intent = %v, want unable; route %v", intent, navRouteFixes(f.nav))
	}
}

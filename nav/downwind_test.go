// nav/downwind_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
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

// ksnaChartedVisual returns KSNA's charted Visual Runway 20R as its scenario
// defines it, from SAGER in along the ILS course.
func ksnaChartedVisual(t *testing.T) ExpectedApproach {
	t.Helper()
	ll := func(s string) math.Point2LL {
		p, err := math.ParseLatLong([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	wp := func(fix, loc string, floor float32) av.Waypoint {
		w := av.Waypoint{Fix: fix, Location: ll(loc)}
		w.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(floor))
		w.SetOnApproach(true)
		return w
	}
	dyers := wp("DYERS", "N033.43.31.972,W117.50.13.989", 0)
	dyers.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(1040))
	threshold := wp("_20R_THRESHOLD", "N033.40.53.947,W117.51.54.212", 0)
	threshold.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(92))
	threshold.SetFlyOver(true)
	threshold.MergeActions(av.WaypointActions{Land: true})
	return ExpectedApproach{Approach: &av.Approach{
		Id:                "VR",
		FullName:          "Visual Runway 20R",
		Type:              av.ChartedVisualApproach,
		Runway:            "20R",
		Threshold:         threshold.Location,
		OppositeThreshold: ll("N033.40.04.110,W117.52.25.797"),
		Waypoints: []av.WaypointArray{{
			wp("SAGER", "N033.55.13.119,W117.42.47.724", 4000),
			wp("SNAKE", "N033.50.51.370,W117.45.34.744", 3400),
			wp("HUKEM", "N033.48.42.143,W117.46.57.004", 2800),
			wp("LEMON", "N033.46.45.455,W117.48.11.217", 2200),
			dyers,
			threshold,
		}},
	}}
}

// TestVisualClearanceFromAnywhereLands checks that a pilot cleared for a
// visual approach on a heading, from anywhere around the runway outside a
// short final, accepts the clearance, keeps it, and lands within 20 minutes
// without ever circling, which is turning more than 330 degrees in one
// unbroken turn: plain visuals to KSFO 28L, KSNA 20R and KJFK 22L, and
// KSNA's charted visual to 20R. The aircraft starts 300 feet above the
// visual's profile, and at least 1,800 feet above the field.
func TestVisualClearanceFromAnywhereLands(t *testing.T) {
	for _, tc := range []struct {
		airport av.ICAOAirportCode
		runway  string
		charted bool
	}{
		{"KSFO", "28L", false},
		{"KSNA", "20R", false},
		{"KSNA", "20R", true},
		{"KJFK", "22L", false},
	} {
		rwy, ok := av.LookupRunway(db.Lookups{}, tc.airport, tc.runway)
		if !ok {
			t.Fatalf("%s %s not found", tc.airport, tc.runway)
		}
		nmPerLong := math.NMPerLongitudeAt(rwy.Threshold)
		magVar, err := db.DB.MagneticGrid.Lookup(rwy.Threshold)
		if err != nil {
			t.Fatal(err)
		}
		landing := float32(math.MagneticToTrue(rwy.Heading, magVar))
		elev := float32(rwy.Elevation)

		for _, dist := range []float32{3.5, 7, 14} {
			for bearing := float32(0); bearing < 360; bearing += 45 {
				for hdg := float32(0); hdg < 360; hdg += 45 {
					name := fmt.Sprintf("%s %s (charted %v) %.1f nm out at %.0f degrees, heading %.0f off the runway",
						tc.airport, tc.runway, tc.charted, dist, bearing, hdg)
					p := math.Offset2LL(rwy.Threshold, math.TrueHeading(math.NormalizeHeading(landing+180+bearing)), dist, nmPerLong)
					alt := elev + max(1500, 900+(dist-3)*ftPerNMOnGlideslope) + 300
					f := NewArrivalFlight(t, ArrivalConfig{
						Waypoints:        p.DMSString(),
						DepartureAirport: "KMCO",
						ArrivalAirport:   tc.airport,
						AircraftType:     "A320",
						InitialAltitude:  alt,
						InitialSpeed:     190,
						AssignedAltitude: alt,
						InitialHeading:   float32(math.TrueToMagnetic(math.TrueHeading(math.NormalizeHeading(landing+hdg)), magVar)),
					})
					var intent speech.CommandIntent
					if tc.charted {
						f.nav.ExpectApproach("VR", ksnaChartedVisual(t), nil)
						intent = f.nav.ClearedApproach("VR", nil, f.simTime, 0, false, "")
					} else {
						f.ExpectVisualApproach(tc.runway)
						intent = f.ClearedVisualApproach(tc.runway)
					}
					if u, unable := intent.(speech.UnableIntent); unable {
						t.Errorf("%s: refused: %v", name, u)
						continue
					}

					var turned, maxTurned float32 // the present unbroken turn, and the largest
					landed := false
					for range 1200 {
						hdg := f.nav.FlightState.Heading
						f.Step(1)
						fs := &f.nav.FlightState
						if dh := math.HeadingSignedTurn(hdg, fs.Heading); math.Abs(dh) >= 1 && math.Sign(dh) != -math.Sign(turned) {
							turned += dh
						} else {
							turned = 0
						}
						maxTurned = max(maxTurned, math.Abs(turned))
						if !f.nav.Approach.Cleared {
							t.Errorf("%s: clearance dropped after %d s", name, f.tick)
							break
						}
						if math.NMDistance2LL(fs.Position, rwy.Threshold) < 0.2 && fs.Altitude-elev < 600 {
							landed = true
							break
						}
					}
					if maxTurned > 330 {
						t.Errorf("%s: circled, turning %.0f degrees without a break", name, maxTurned)
					}
					if !landed && f.nav.Approach.Cleared {
						t.Errorf("%s: didn't land in 20 minutes", name)
					}
				}
			}
		}
	}
}

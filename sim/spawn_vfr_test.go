// sim/spawn_vfr_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// TestAdjustRouteForShelves checks the restrictions a VFR route is given
// where it passes under a Class B shelf: waypoints beneath it are held at
// the under-shelf altitude, a waypoint is added just before the shelf's edge
// so that the aircraft is down before it and one just past the far edge so
// that it stays down until clear, and the rest of the route keeps its
// cruise altitude. A route passes over airspace whose ceiling is below its
// cruise altitude. Routes with no room under the shelf are rejected.
func TestAdjustRouteForShelves(t *testing.T) {
	const cruise = 6500
	origin := math.Point2LL{0, 0}
	along := func(nm float32) math.Point2LL { return math.Offset2LL(origin, 90, nm, testNmPerLongitude) }
	distance := func(p math.Point2LL) float32 { return math.NMDistance2LL(origin, p) }

	// The route sim builds: waypoints every 10nm, at or above cruise for the
	// first half and at or below it for the second.
	route := func() []av.Waypoint {
		var wps []av.Waypoint
		for i, fix := range []string{"A", "B", "C", "D"} {
			wp := av.Waypoint{Fix: fix, Location: along(float32(10 * i))}
			if i < 2 {
				wp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(cruise))
			} else {
				wp.SetAltitudeRestriction(av.MakeAtOrBelowAltitudeRestriction(cruise))
			}
			wps = append(wps, wp)
		}
		return wps
	}

	// shelf returns a grid holding one volume with the given floor over the
	// stretch of the route from west to east nm.
	shelf := func(west, east float32, floor int) *db.AirspaceGrid {
		corners := []math.Point2LL{
			math.Offset2LL(along(west), 0, 3, testNmPerLongitude),
			math.Offset2LL(along(east), 0, 3, testNmPerLongitude),
			math.Offset2LL(along(east), 180, 3, testNmPerLongitude),
			math.Offset2LL(along(west), 180, 3, testNmPerLongitude),
		}
		bounds := math.Extent2DFromPoints(util.MapSlice(corners, func(p math.Point2LL) [2]float32 { return p }))
		return db.MakeAirspaceGrid([]*av.AirspaceVolume{{
			Type:          av.AirspaceVolumePolygon,
			Floor:         floor,
			Ceiling:       10000,
			Vertices:      corners,
			PolygonBounds: &bounds,
		}})
	}

	// mva returns a grid with one MVA of the given altitude over the same stretch.
	mva := func(west, east float32, alt int) *db.MVAGrid {
		ring := [][2]float32{
			math.Offset2LL(along(west), 0, 3, testNmPerLongitude),
			math.Offset2LL(along(east), 0, 3, testNmPerLongitude),
			math.Offset2LL(along(east), 180, 3, testNmPerLongitude),
			math.Offset2LL(along(west), 180, 3, testNmPerLongitude),
		}
		return db.MakeMVAGrid([]db.MVA{{
			MinimumLimit: alt,
			Bounds:       math.Extent2DFromPoints(ring),
			ExteriorRing: ring,
		}})
	}

	describe := func(wps []av.Waypoint) string {
		return strings.Join(util.MapSlice(wps, func(wp av.Waypoint) string {
			return wp.Fix + "/" + wp.AltitudeRestriction().Encoded()
		}), " ")
	}

	for _, tc := range []struct {
		name         string
		bravo        *db.AirspaceGrid
		mva          *db.MVAGrid
		depElevation int
		want         string // "" means the route is rejected
		entry, exit  float32
	}{
		{
			name:  "shelf mid-route",
			bravo: shelf(9, 19, 3000),
			want:  "A/6500+ _shelf1@2500/2500 B/2500 _shelf2@2500/2500 C/6500- D/6500-",
			entry: 9, exit: 19,
		},
		{
			name:  "departure field under the shelf",
			bravo: shelf(-3, 7, 1200),
			want:  "A/1000 _shelf1@1000/1000 B/6500+ C/6500- D/6500-",
			exit:  7,
		},
		{
			name:  "whole route under the shelf",
			bravo: shelf(-3, 33, 3000),
			want:  "A/2500 B/2500 C/2500- D/2500-",
		},
		{
			name:  "shelf above cruise",
			bravo: shelf(9, 19, 7000),
			want:  "A/6500+ B/6500+ C/6500- D/6500-",
		},
		{
			name:  "airspace below cruise",
			bravo: db.MakeAirspaceGrid([]*av.AirspaceVolume{testAirspaceBox(12, -3, 18, 3, 1200, 3000)}),
			want:  "A/6500+ B/6500+ C/6500- D/6500-",
		},
		{
			name:  "airspace below cruise near the field",
			bravo: db.MakeAirspaceGrid([]*av.AirspaceVolume{testAirspaceBox(3, -3, 8, 3, 1200, 3000)}),
			want:  "A/6500+ _shelf1@1000/1000 _shelf2@1000/1000 B/6500+ C/6500- D/6500-",
			entry: 3, exit: 8,
		},
		{
			name:         "no room over the field",
			bravo:        shelf(-3, 7, 1200),
			depElevation: 600,
		},
		{
			name:  "MVA above the shelf altitude",
			bravo: shelf(9, 19, 3000),
			mva:   mva(9, 19, 4000),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(testLogger())
			s.vfrAirspace = tc.bravo
			s.mvaGrid = util.Select(tc.mva != nil, tc.mva, db.MakeMVAGrid(nil))
			dep := db.Airport{Location: along(0), Elevation: tc.depElevation}
			arr := db.Airport{Location: along(30)}

			wps, ok := s.adjustRouteForShelves(route(), cruise, dep, arr)
			if tc.want == "" {
				if ok {
					t.Fatalf("expected the route to be rejected, got %s", describe(wps))
				}
				return
			}
			if !ok {
				t.Fatal("route unexpectedly rejected")
			}
			if got := describe(wps); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}

			// The added waypoints sit within the mile before the shelf's
			// near edge and the mile past its far edge.
			for _, wp := range wps {
				if !strings.HasPrefix(wp.Fix, "_shelf") {
					continue
				}
				d := distance(wp.Location)
				entering := tc.entry > 0 && d > tc.entry-1 && d <= tc.entry
				leaving := tc.exit > 0 && d >= tc.exit && d < tc.exit+1
				if !entering && !leaving {
					t.Errorf("%s at %.2fnm is not just before %.0fnm or just past %.0fnm", wp.Fix, d, tc.entry, tc.exit)
				}
			}
		})
	}
}

func TestVFRAvoids(t *testing.T) {
	for _, tc := range []struct {
		typ               db.SpecialUseAirspaceType
		designation, name string
		want              bool
	}{
		{db.ProhibitedArea, "56", "P-56", true},
		{db.RestrictedArea, "2508", "R-2508", true},
		{db.WarningArea, "386", "W-386", true},
		{db.MilitaryOperationsArea, "GAP A L", "GAP A LOW MOA", false},
		{db.AlertArea, "211", "A-211", false},
		{db.UnspecifiedArea, "LIVERMORE", "LIVERMORE", true},
		{db.UnspecifiedArea, "NY SFRA", "NEW YORK HUDSON RVR EXCLUSION", false},
		{db.UnspecifiedArea, "ANC SATR", "ANCHORAGE MERRILL SEGMENT", false},
		{db.UnspecifiedArea, "DC SFRA", "WASHINGTON DC SFRA", false},
		{db.UnspecifiedArea, "DC SFRA", "WASHINGTON DC FRZ", true},
		{db.UnspecifiedArea, "GCNPSFRA E", "GRAND CANYON BRIGHT ANGEL FFZ", true},
	} {
		sua := db.SpecialUseAirspace{Type: tc.typ, Designation: tc.designation}
		vol := db.SpecialUseAirspaceVolume{AirspaceVolume: av.AirspaceVolume{Description: tc.name}}
		if got := vfrAvoids(sua, vol); got != tc.want {
			t.Errorf("%c %s %q: got %v, want %v", tc.typ, tc.designation, tc.name, got, tc.want)
		}
	}
}

// testAirspaceBox returns a polygonal volume over the box from (x0, y0) to
// (x1, y1) nm.
func testAirspaceBox(x0, y0, x1, y1 float32, floor, ceiling int) *av.AirspaceVolume {
	nm := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, testNmPerLongitude) }
	vertices := []math.Point2LL{nm(x0, y0), nm(x0, y1), nm(x1, y1), nm(x1, y0)}
	bounds := math.Extent2DFromP2LLs(vertices)
	return &av.AirspaceVolume{
		Type:          av.AirspaceVolumePolygon,
		Floor:         floor,
		Ceiling:       ceiling,
		Vertices:      vertices,
		PolygonBounds: &bounds,
	}
}

// TestClearAltitude checks that a VFR route passes over airspace whose
// ceiling is far enough below it and under airspace whose floor is far
// enough above the ground, taking the stack of volumes together.
func TestClearAltitude(t *testing.T) {
	const cruise = 6500
	for _, tc := range []struct {
		name string
		vols []*av.AirspaceVolume
		want int
	}{
		{"none", nil, cruise},
		{"low ceiling", []*av.AirspaceVolume{testAirspaceBox(-1, -1, 1, 1, 0, 3000)}, cruise},
		{"ceiling at cruise", []*av.AirspaceVolume{testAirspaceBox(-1, -1, 1, 1, 0, 6400)}, 0},
		{"elevated", []*av.AirspaceVolume{testAirspaceBox(-1, -1, 1, 1, 5000, 9000)}, 4500},
		{"floor above cruise", []*av.AirspaceVolume{testAirspaceBox(-1, -1, 1, 1, 7000, 9000)}, cruise},
		{"under one into another", []*av.AirspaceVolume{
			testAirspaceBox(-1, -1, 1, 1, 0, 4000),
			testAirspaceBox(-1, -1, 1, 1, 4500, 9000),
		}, 0},
		{"under one and over another", []*av.AirspaceVolume{
			testAirspaceBox(-1, -1, 1, 1, 0, 2000),
			testAirspaceBox(-1, -1, 1, 1, 4500, 9000),
		}, 4000},
	} {
		if got := clearAltitude(tc.vols, cruise, func(*av.AirspaceVolume) bool { return true }); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}

	// Airspace the flight isn't sure to be above is flown under.
	low := []*av.AirspaceVolume{testAirspaceBox(-1, -1, 1, 1, 1200, 3000)}
	if got := clearAltitude(low, cruise, func(*av.AirspaceVolume) bool { return false }); got != 1000 {
		t.Errorf("not sure to be above it: got %d, want 1000", got)
	}
}

// TestDetourAroundAirspace checks that a VFR route goes around the shorter
// side of airspace it can neither pass over nor fly under, keeping clear of
// it, and that it is rejected where there is no reasonable way around.
func TestDetourAroundAirspace(t *testing.T) {
	const cruise = 6500
	nm := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, testNmPerLongitude) }
	// The departure runway, then a waypoint every 10nm east to the last.
	route := func() []av.Waypoint {
		wps := []av.Waypoint{{Fix: "RWY", Location: nm(0, 0)}, {Fix: "OPP", Location: nm(1, 0)}}
		for i, fix := range []string{"A", "B", "C", "D"} {
			wps = append(wps, av.Waypoint{Fix: fix, Location: nm(float32(10*(i+1)), 0)})
		}
		return wps
	}
	fixes := func(wps []av.Waypoint) string {
		var f []string
		for _, wp := range wps {
			if !strings.HasPrefix(wp.Fix, "_detour") {
				f = append(f, wp.Fix)
			} else if f[len(f)-1] != "*" {
				f = append(f, "*")
			}
		}
		return strings.Join(f, " ")
	}

	for _, tc := range []struct {
		name  string
		vols  []*av.AirspaceVolume
		want  string // fixes of the route, "*" for detour waypoints; "" if rejected
		north bool   // whether the detour goes north of the airspace
	}{
		{
			name:  "goes around the shorter side",
			vols:  []*av.AirspaceVolume{testAirspaceBox(15, -4, 25, 2, 0, 10000)},
			want:  "RWY OPP A * C D",
			north: true,
		},
		{
			name: "passes over a low ceiling",
			vols: []*av.AirspaceVolume{testAirspaceBox(15, -4, 25, 2, 0, 3000)},
			want: "RWY OPP A B C D",
		},
		{
			name: "goes around a low ceiling it hasn't climbed over",
			vols: []*av.AirspaceVolume{testAirspaceBox(3, -1, 6, 3, 0, 3000)},
			want: "RWY OPP * A B C D",
		},
		{
			name: "flies under a high floor",
			vols: []*av.AirspaceVolume{testAirspaceBox(15, -4, 25, 2, 5000, 10000)},
			want: "RWY OPP A B C D",
		},
		{
			name: "goes around a stack with no room under or over",
			vols: []*av.AirspaceVolume{
				testAirspaceBox(15, -4, 25, 2, 0, 2000),
				testAirspaceBox(15, -4, 25, 2, 2000, 10000),
			},
			want:  "RWY OPP A * C D",
			north: true,
		},
		{
			name: "goes around both",
			vols: []*av.AirspaceVolume{
				testAirspaceBox(15, -4, 25, 2, 0, 10000),
				testAirspaceBox(18, 2.5, 22, 15, 0, 10000),
			},
			want: "RWY OPP A * C D",
		},
		{
			name: "over the last waypoint",
			vols: []*av.AirspaceVolume{testAirspaceBox(35, -2, 45, 2, 0, 10000)},
		},
		{
			name: "between the last waypoint and the field",
			vols: []*av.AirspaceVolume{testAirspaceBox(42, -1, 43, 1, 0, 3000)},
		},
		{
			name: "over the runway",
			vols: []*av.AirspaceVolume{testAirspaceBox(0.5, -2, 5, 2, 0, 10000)},
		},
		{
			name: "too far around",
			vols: []*av.AirspaceVolume{testAirspaceBox(15, -100, 17, 100, 0, 10000)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(testLogger())
			s.State.NmPerLongitude = testNmPerLongitude
			s.vfrAirspace = db.MakeAirspaceGrid(tc.vols)
			dep, arr := db.Airport{Location: nm(0, 0)}, db.Airport{Location: nm(45, 0)}

			wps, ok := s.detourAroundAirspace(route(), cruise, dep, arr)
			if tc.want == "" {
				if ok {
					t.Fatalf("expected the route to be rejected, got %s", fixes(wps))
				}
				return
			}
			if !ok {
				t.Fatal("route unexpectedly rejected")
			}
			if got := fixes(wps); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}

			if !strings.Contains(tc.want, "*") {
				return
			}
			for _, wp := range wps {
				if !strings.HasPrefix(wp.Fix, "_detour") {
					continue
				}
				if north := math.LL2NM(wp.Location, testNmPerLongitude)[1] > 0; north != tc.north && tc.name != "goes around both" {
					t.Errorf("%s: expected the detour to go north %v", wp.Fix, tc.north)
				}
				for _, vol := range tc.vols {
					if vol.Near(wp.Location, 0.9*vfrDetourMargin) {
						t.Errorf("%s is within the margin of the airspace", wp.Fix)
					}
				}
			}
			for i := 1; i < len(wps); i++ {
				for _, vol := range tc.vols {
					if vol.CoversSegment(wps[i-1].Location, wps[i].Location) {
						t.Errorf("%s-%s passes through the airspace", wps[i-1].Fix, wps[i].Fix)
					}
				}
			}
		})
	}
}

// TestVFRTerminalCeiling checks that airspace keeps VFR arrivals from a field
// only when it lies over where they fly.
// XTST's runway 9 runs east from the origin, so its left-traffic pattern is
// to the north and the orbit that arrivals hold in is centered 3nm south of
// the threshold.
func TestVFRTerminalCeiling(t *testing.T) {
	installIntersectingRunwayFixture(t)
	ap := db.DB.Airports["XTST"]

	for _, tc := range []struct {
		name    string
		vol     *av.AirspaceVolume
		ceiling int
	}{
		{"beside the pattern", testAirspaceBox(-1, 3, 3, 5, 0, 10000), maxVFRAltitude},
		{"over the downwind", testAirspaceBox(-1, 0.5, 3, 5, 0, 10000), 0},
		{"over the orbit", testAirspaceBox(-1, -5, 1, -4.5, 0, 10000), 0},
		{"between orbit and pattern", testAirspaceBox(0.4, -0.8, 0.6, -0.6, 0, 10000), 0},
		{"between orbit and pattern with room", testAirspaceBox(0.4, -0.8, 0.6, -0.6, 4000, 10000), 3500},
		{"from scattered orbit to pattern", testAirspaceBox(1.2, -0.8, 1.3, -0.6, 0, 10000), 0},
		{"between orbit and straight in", testAirspaceBox(-1.85, -1, -1.75, -0.8, 0, 10000), 0},
		{"over the pattern with room", testAirspaceBox(-1, 0.5, 3, 5, 4000, 10000), 3500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(testLogger())
			s.State.NmPerLongitude = testNmPerLongitude
			s.State.VFRRunways[ap.Id] = ap.Runways[0]
			s.vfrAirspace = db.MakeAirspaceGrid([]*av.AirspaceVolume{tc.vol})
			s.vfrTerminalAlts = make(map[av.ICAOAirportCode]int)

			ceiling, ok := s.vfrTerminalCeiling(ap)
			if ceiling != tc.ceiling {
				t.Errorf("got ceiling %d, want %d", ceiling, tc.ceiling)
			}
			if ok != (tc.ceiling > 0) {
				t.Errorf("got arrival allowed %v, want %v", ok, tc.ceiling > 0)
			}
		})
	}
}

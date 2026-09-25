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
			s.bravoAirspace = tc.bravo
			s.charlieAirspace = db.MakeAirspaceGrid(nil)
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

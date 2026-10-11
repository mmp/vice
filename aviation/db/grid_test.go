// aviation/db/grid_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// TestAirspaceGridQueries checks the grid's answers against every volume,
// for points and segments around volumes of a range of sizes on either side
// of the equator and the prime meridian, where the cells' coordinates change
// sign.
func TestAirspaceGridQueries(t *testing.T) {
	r := rand.Make()
	for _, origin := range []math.Point2LL{{-77, 38.8}, {0, 0}, {-157.9, 21.3}, {149.9, 61.2}} {
		nmPerLongitude := math.NMPerLongitudeAt(origin)
		at := func(x, y float32) math.Point2LL {
			return math.Add2f(origin, math.NM2LL([2]float32{x, y}, nmPerLongitude))
		}

		var vols []*av.AirspaceVolume
		for range 40 {
			c := [2]float32{r.Float32Range(-40, 40), r.Float32Range(-40, 40)}
			radius := r.Float32Range(0.2, 6)
			var vertices []math.Point2LL
			for i := range 7 {
				hdg := math.Radians(float32(i) * 360 / 7)
				d := radius * r.Float32Range(0.5, 1)
				vertices = append(vertices, at(c[0]+d*math.Sin(hdg), c[1]+d*math.Cos(hdg)))
			}
			bounds := math.Extent2DFromP2LLs(vertices)
			vols = append(vols, &av.AirspaceVolume{
				Type:          av.AirspaceVolumePolygon,
				Floor:         r.Intn(5000),
				Ceiling:       5000 + r.Intn(5000),
				Vertices:      vertices,
				PolygonBounds: &bounds,
			})
		}
		g := MakeAirspaceGrid(vols)

		for range 2000 {
			p := at(r.Float32Range(-45, 45), r.Float32Range(-45, 45))
			alt := r.Intn(10000)
			want := slices.ContainsFunc(vols, func(v *av.AirspaceVolume) bool { return v.Inside(p, alt) })
			if got := g.Inside(p, alt); got != want {
				t.Errorf("%v: Inside(%v, %d) = %v, want %v", origin, p, alt, got, want)
			}

			rad := r.Float32Range(0, airspaceCellMargin)
			wantNear := util.FilterSlice(vols, func(v *av.AirspaceVolume) bool { return v.Near(p, rad) })
			if got := g.Near(p, rad); !sameVolumes(got, wantNear) {
				t.Errorf("%v: Near(%v, %.2f) found %d volumes, want %d", origin, p, rad, len(got), len(wantNear))
			}

			q := at(r.Float32Range(-45, 45), r.Float32Range(-45, 45))
			wantCrossed := util.FilterSlice(vols, func(v *av.AirspaceVolume) bool { return v.CoversSegment(p, q) })
			if got := g.Crossed(p, q); !sameVolumes(got, wantCrossed) {
				t.Errorf("%v: Crossed(%v, %v) found %d volumes, want %d", origin, p, q, len(got), len(wantCrossed))
			}
		}
	}
}

func sameVolumes(a, b []*av.AirspaceVolume) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(v *av.AirspaceVolume) bool { return !slices.Contains(b, v) })
}

// aviation/db/terrain_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"slices"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
)

func TestTerrainTileKeyFileName(t *testing.T) {
	for _, tc := range []struct {
		key  TerrainTileKey
		name string
	}{
		{TerrainTileKey{39, -107}, "N39W107.zst"},
		{TerrainTileKey{52, 172}, "N52E172.zst"},
		{TerrainTileKey{5, -60}, "N05W060.zst"},
		{TerrainTileKey{-14, -171}, "S14W171.zst"},
	} {
		if got := tc.key.FileName(); got != tc.name {
			t.Errorf("%v: got %q, want %q", tc.key, got, tc.name)
		}
		if k, err := parseTerrainTileKey(tc.name); err != nil || k != tc.key {
			t.Errorf("%s: got %v, %v, want %v", tc.name, k, err, tc.key)
		}
	}
	for _, name := range []string{"N39W107", "N39W107.zst~", "X39W107.zst", "N39W1077.zst", "n39w107.zst", "N3W107.zst"} {
		if k, err := parseTerrainTileKey(name); err == nil {
			t.Errorf("%s: parsed as %v, expected an error", name, k)
		}
	}
}

func TestTerrainLookup(t *testing.T) {
	const n = TerrainTileSize
	cell := func(k TerrainTileKey, r, c int) uint8 { return uint8(r + 3*c + k.Longitude) }
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []TerrainTileKey{{39, -107}, {51, 179}, {51, -180}}
	terrain := &Terrain{Tiles: make(map[TerrainTileKey][]byte)}
	for _, k := range keys {
		tile := make([]byte, n*n)
		for r := range n {
			for c := range n {
				tile[r*n+c] = cell(k, r, c)
			}
		}
		terrain.Tiles[k] = enc.EncodeAll(tile, nil)
	}
	g := terrain.MakeGrid()

	check := func(p math.Point2LL, want int) {
		t.Helper()
		if got := g.HighestTerrain(p); got != want {
			t.Errorf("%v: got %d, want %d", p, got, want)
		}
	}

	for _, k := range keys {
		for _, rc := range [][2]int{{0, 0}, {0, n - 1}, {n - 1, 0}, {n - 1, n - 1}, {17, 101}, {200, 3}} {
			r, c := rc[0], rc[1]
			center := math.Point2LL{float32(k.Longitude) + (float32(c)+0.5)/n, float32(k.Latitude+1) - (float32(r)+0.5)/n}
			check(center, 100*int(cell(k, r, c)))
		}
		// The south and west edges are in the tile.
		check(math.Point2LL{float32(k.Longitude), float32(k.Latitude)}, 100*int(cell(k, n-1, 0)))
	}

	// Across the dateline.
	east, west := TerrainTileKey{51, 179}, TerrainTileKey{51, -180}
	check(math.Point2LL{179.999, 51.5}, 100*int(cell(east, n/2, n-1)))
	check(math.Point2LL{180, 51.5}, 100*int(cell(west, n/2, 0)))
	check(math.Point2LL{-180, 51.5}, 100*int(cell(west, n/2, 0)))
	check(math.Point2LL{-180.001, 51.5}, 100*int(cell(east, n/2, n-1)))

	// Where there is no tile, including just past the north and east edges
	// of one.
	check(math.Point2LL{-140, 30}, 0)
	check(math.Point2LL{-106.5, 40}, 0)
	check(math.Point2LL{-106, 39.5}, 0)
	check(math.Point2LL{-140, 30}, 0)
}

func TestTerrainResource(t *testing.T) {
	InitDB()
	g := DB.Terrain.MakeGrid()

	// Each cell holds the highest terrain within 0.5 nm of it, so a point
	// may see terrain somewhat further away than that.
	for _, tc := range []struct {
		name    string
		p       math.Point2LL
		low, hi int
	}{
		{"Denali", math.Point2LL{-151.0070, 63.0692}, 20300, 20300},
		{"KASE", math.Point2LL{-106.8688, 39.2232}, 8500, 8600},
		{"Badwater Basin", math.Point2LL{-116.7700, 36.2300}, 2400, 2500}, // below sea level, at the foot of a ridge
		{"KDDC", math.Point2LL{-99.9656, 37.7634}, 2600, 2800},
		{"Pacific Ocean", math.Point2LL{-140, 30}, 0, 0},
	} {
		if alt := g.HighestTerrain(tc.p); alt < tc.low || alt > tc.hi {
			t.Errorf("%s: terrain %d', expected %d' to %d'", tc.name, alt, tc.low, tc.hi)
		}
	}
}

// TestTerrainUnderMVAs checks the terrain against the MVA charts, which keep
// about 1000' above it. A point is checked only if the polygon it is in also
// holds every point within reach of the terrain filter, so that the terrain
// the filter takes in is all under that polygon's MVA. The stored terrain is
// rounded up and may be another 100' higher so that it compresses better,
// and a chart may leave a little less than 1000', so the terrain may be up to
// 200' closer to the MVA than that.
func TestTerrainUnderMVAs(t *testing.T) {
	InitDB()
	g := DB.Terrain.MakeGrid()

	// A cell holds the terrain within 0.5 nm of its edges; from a point in
	// it that may be up to about 1.1 nm away.
	const reach = 1.1
	const step = 1. / 60 // about 1 nm
	around := []math.TrueHeading{0, 45, 90, 135, 180, 225, 270, 315}
	var checked int
	for tracon, mvas := range util.SortedMap(DB.MVAs) {
		mg := MakeMVAGrid(mvas)
		bounds := mvas[0].Bounds
		for _, m := range mvas {
			bounds = math.Union(math.Union(bounds, m.Bounds.P0), m.Bounds.P1)
		}
		failures := 0
		for lat := bounds.P0[1]; lat < bounds.P1[1]; lat += step {
			nmPerLongitude := math.NMPerLongitudeAt(math.Point2LL{0, lat})
			for lon := bounds.P0[0]; lon < bounds.P1[0]; lon += step * 60 / nmPerLongitude {
				p := math.Point2LL{lon, lat}
				entries := mg.getEntries(p)
				i := slices.IndexFunc(entries, func(m MVA) bool { return m.Inside(p) })
				if i == -1 {
					continue
				}
				m := entries[i]
				if slices.ContainsFunc(around, func(hdg math.TrueHeading) bool {
					return !m.Inside(math.Offset2LL(p, hdg, reach, nmPerLongitude))
				}) {
					continue
				}
				checked++
				if alt := g.HighestTerrain(p); alt+800 > m.MinimumLimit && failures < 5 {
					failures++
					t.Errorf("%s: %v: terrain %d' is within 800' of the MVA %d'", tracon, p, alt, m.MinimumLimit)
				}
			}
		}
	}
	if checked < 100000 {
		t.Errorf("only %d points checked", checked)
	}
}

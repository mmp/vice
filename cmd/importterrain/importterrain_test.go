// cmd/importterrain/importterrain_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
)

func TestFilterTile(t *testing.T) {
	const n = db.TerrainTileSize

	// One peak in the 30N120W source tile, at the cell in the northwest
	// corner of the one-degree tile 39N107W; another at 51.5N in the 50N180W
	// tile, in its first column east of the dateline.
	var g sourceGrid
	cells := make([]uint8, tileRows*tileCols)
	peakRow, peakCol := (50-40)*n, (-107+120)*n
	cells[peakRow*tileCols+peakCol] = 42
	g.add(gmtedTile{30, -120}, cells)
	west := make([]uint8, tileRows*tileCols)
	west[((70-52)*n+n/2)*tileCols] = 17
	g.add(gmtedTile{50, -180}, west)

	// At 40N a cell is 0.25 nm tall and 0.19 nm wide, so 0.5 nm is 2 rows
	// and 3 columns.
	v := g.filterTile(db.TerrainTileKey{Latitude: 39, Longitude: -107})
	for r := range n {
		for c := range n {
			want := uint8(0)
			if r <= 2 && c <= 3 {
				want = 42
			}
			if v[r*n+c] != want {
				t.Fatalf("39N107W cell (%d,%d) is %d, want %d", r, c, v[r*n+c], want)
			}
		}
	}
	// The tiles to the north and west see it too.
	if v := g.filterTile(db.TerrainTileKey{Latitude: 40, Longitude: -107}); v[(n-1)*n] != 42 || v[(n-2)*n] != 42 || v[(n-3)*n] != 0 {
		t.Errorf("40N107W south edge: %d %d %d, want 42 42 0", v[(n-1)*n], v[(n-2)*n], v[(n-3)*n])
	}
	if v := g.filterTile(db.TerrainTileKey{Latitude: 39, Longitude: -108}); v[n-1] != 42 || v[n-3] != 42 || v[n-4] != 0 {
		t.Errorf("39N108W east edge: %d %d %d, want 42 42 0", v[n-1], v[n-3], v[n-4])
	}

	// The peak by the dateline reaches across it into the last columns of
	// 51N179E. At 52N, that tile's poleward edge, a cell is 0.15 nm wide, so
	// 0.5 nm is 4 columns.
	v = g.filterTile(db.TerrainTileKey{Latitude: 51, Longitude: 179})
	if v[n/2*n+n-1] != 17 || v[n/2*n+n-4] != 17 || v[n/2*n+n-5] != 0 {
		t.Errorf("51N179E east edge: %d %d %d, want 17 17 0", v[n/2*n+n-1], v[n/2*n+n-4], v[n/2*n+n-5])
	}
}

func TestSnap(t *testing.T) {
	const n = db.TerrainTileSize
	v := make([]uint8, n*n)
	for r := range n {
		for c := range n {
			// Terrain that wanders by a few hundred feet, with the
			// highest values the encoding holds in one corner.
			v[r*n+c] = uint8(100 + 3*math.Sin(float32(r)/7) + 2*math.Cos(float32(c)/5) + float32((r*c)%3))
			if r < 4 && c < 4 {
				v[r*n+c] = 255 - uint8(r%2)
			}
		}
	}
	s := snap(v)

	repeats := func(x []uint8) int {
		k := 0
		for i := 1; i < len(x); i++ {
			if x[i] == x[i-1] {
				k++
			}
		}
		return k
	}
	if repeats(s) <= repeats(v) {
		t.Errorf("snapped tile has %d repeated cells, unsnapped %d", repeats(s), repeats(v))
	}
	for i := range v {
		if s[i] < v[i] || int(s[i]) > int(v[i])+1 {
			t.Fatalf("cell %d: snapped to %d from %d", i, s[i], v[i])
		}
	}
}

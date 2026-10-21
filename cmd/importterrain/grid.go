// cmd/importterrain/grid.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
)

const (
	// cellsPerDegree gives the output's 15 arc-second cells.
	cellsPerDegree = db.TerrainTileSize
	// A source tile spans 20 degrees of latitude and 30 of longitude.
	tileRows = 20 * cellsPerDegree
	tileCols = 30 * cellsPerDegree
	// gridRows and gridCols are the cells of the whole globe.
	gridRows = 180 * cellsPerDegree
	gridCols = 360 * cellsPerDegree
)

// sourceGrid holds the source tiles at the output's resolution, as hundreds
// of feet rounded up, in rows of tiles going south from 90N and columns going
// east from 180W. Cells outside the tiles read as sea level.
type sourceGrid [gridRows / tileRows][gridCols / tileCols][]uint8

func (g *sourceGrid) add(t gmtedTile, cells []uint8) {
	g[(90-t.lat)/20-1][(t.lon+180)/30] = cells
}

// at returns the cell row cells south of 90N and col cells east of 180W.
func (g *sourceGrid) at(row, col int) uint8 {
	if row < 0 || row >= gridRows {
		return 0
	}
	col = (col%gridCols + gridCols) % gridCols
	if t := g[row/tileRows][col/tileCols]; t != nil {
		return t[(row%tileRows)*tileCols+col%tileCols]
	}
	return 0
}

// reduce converts a source tile's elevations to the output resolution: each
// 15 arc-second cell takes the highest of the 2 x 2 source cells in it, in
// hundreds of feet rounded up. Missing data and anything below sea level
// read as sea level.
func reduce(elev []int16) []uint8 {
	const metersToFeet = 3.28084
	cells := make([]uint8, tileRows*tileCols)
	for r := range tileRows {
		for c := range tileCols {
			i := 2*r*gmtedCols + 2*c
			m := max(elev[i], elev[i+1], elev[i+gmtedCols], elev[i+gmtedCols+1], 0)
			cells[r*tileCols+c] = uint8(min(255, math.Ceil(float32(m)*metersToFeet/100)))
		}
	}
	return cells
}

// filterTile returns the output tile k: each cell holds the highest source
// cell within 0.5 nm of any point in it.
func (g *sourceGrid) filterTile(k db.TerrainTileKey) []uint8 {
	const n = db.TerrainTileSize
	// A cell is 0.25 nm tall and 0.25 cos(latitude) nm wide. The width is
	// taken at the tile's poleward edge so that the window reaches 0.5 nm
	// across all of it.
	poleward := max(math.Abs(k.Latitude), math.Abs(k.Latitude+1))
	kr := 2
	kc := int(math.Ceil(0.5 / (0.25 * math.Cos(math.Radians(float32(poleward))))))
	row0, col0 := (90-k.Latitude-1)*n, (k.Longitude+180)*n

	// The highest within kc columns, for each column of the tile in each
	// row it and kr rows on either side of it span.
	wide := make([]uint8, (n+2*kr)*n)
	for r := range n + 2*kr {
		for c := range n {
			var m uint8
			for dc := -kc; dc <= kc; dc++ {
				m = max(m, g.at(row0-kr+r, col0+c+dc))
			}
			wide[r*n+c] = m
		}
	}

	cells := make([]uint8, n*n)
	for r := range n {
		for c := range n {
			var m uint8
			for dr := range 2*kr + 1 {
				m = max(m, wide[(r+dr)*n+c])
			}
			cells[r*n+c] = m
		}
	}
	return cells
}

// snap returns a copy of the tile v in which cells are raised by one where
// that makes them repeat their neighbor above or to the left, so that the
// tile compresses better. It works within the tile alone, since tiles are
// decompressed independently.
func snap(v []uint8) []uint8 {
	const n = db.TerrainTileSize
	s := slices.Clone(v)
	within := func(i int, x uint8) bool { return v[i] <= x && int(x) <= int(v[i])+1 }
	for r := 1; r < n; r++ {
		for c := range n {
			if up := s[(r-1)*n+c]; within(r*n+c, up) {
				s[r*n+c] = up
			}
		}
	}
	for r := range n {
		for c := 1; c < n; c++ {
			if left := s[r*n+c-1]; within(r*n+c, left) {
				s[r*n+c] = left
			}
		}
	}
	for i := range s {
		if !within(i, s[i]) {
			panic(fmt.Sprintf("snapped cell %d holds %d, filtered %d", i, s[i], v[i]))
		}
	}
	return s
}

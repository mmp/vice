// aviation/db/grid.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	av "github.com/mmp/vice/aviation"
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////

func UnderBravoShelf(grid *AirspaceGrid, p math.Point2LL, alt int) bool {
	if grid == nil {
		return false
	}
	return grid.Below(p, alt)
}

///////////////////////////////////////////////////////////////////////////
// AirspaceGrid

// AirspaceGrid organizes AirspaceVolume definitions and provides efficient in volume tests via
// a grid in lat-long space that records which of a potentially large set of volumes overlap
// grid cells. Grid cells are initialized on demand rather than upfront, which saves storage
type AirspaceGrid struct {
	volumes []*av.AirspaceVolume
	entries map[[2]int][]*av.AirspaceVolume
}

func MakeAirspaceGrid(v []*av.AirspaceVolume) *AirspaceGrid {
	return &AirspaceGrid{
		volumes: slices.Clone(v),
		entries: make(map[[2]int][]*av.AirspaceVolume),
	}
}

func (g *AirspaceGrid) getEntries(p math.Point2LL) []*av.AirspaceVolume {
	// Quantize coordinates to grid; roughly 6nm resolution (at least in
	// latitude...)
	pq := [2]int{int(10 * p[0]), int(10 * p[1])}

	if vols, ok := g.entries[pq]; ok {
		return vols
	} else {
		// Center of the grid cell
		pc := math.Point2LL{(float32(pq[0]) + 0.5) / 10, (float32(pq[1]) + 0.5) / 10}

		vols := util.FilterSlice(g.volumes, func(v *av.AirspaceVolume) bool {
			// Assumes both polygonal and an initialized PolygonBounds...
			// The distance check has some slop in it just so we can be
			// lazy about thinking about rounding in the grid quantization.
			return math.NMDistance2LL(v.PolygonBounds.ClosestPointInBox(pc), pc) < 10
		})
		g.entries[pq] = vols
		return vols
	}
}

func (g *AirspaceGrid) Inside(p math.Point2LL, alt int) bool {
	for _, vol := range g.getEntries(p) {
		if vol.Inside(p, alt) {
			return true
		}
	}
	return false
}

func (g *AirspaceGrid) Below(p math.Point2LL, alt int) bool {
	for _, vol := range g.getEntries(p) {
		if vol.Below(p, alt) {
			return true
		}
	}
	return false
}

// ShelfFloor returns the lowest floor of the volumes lying over p, and whether
// any lies over it at all. Flying below the floor keeps clear of all of them;
// a floor at the surface leaves nowhere to fly under.
func (g *AirspaceGrid) ShelfFloor(p math.Point2LL) (int, bool) {
	floor, covered := 0, false
	for _, vol := range g.getEntries(p) {
		if vol.Covers(p) && (!covered || vol.Floor < floor) {
			floor, covered = vol.Floor, true
		}
	}
	return floor, covered
}

// MVAGrid organizes MVA definitions and provides efficient lookups via a
// grid in lat-long space that records which MVAs overlap grid cells. Grid
// cells are initialized on demand rather than upfront.
type MVAGrid struct {
	mvas    []MVA
	entries map[[2]int][]MVA
}

func MakeMVAGrid(mvas []MVA) *MVAGrid {
	return &MVAGrid{
		mvas:    slices.Clone(mvas),
		entries: make(map[[2]int][]MVA),
	}
}

func (g *MVAGrid) getEntries(p [2]float32) []MVA {
	// Quantize coordinates to grid; roughly 6nm resolution
	pq := [2]int{int(10 * p[0]), int(10 * p[1])}

	if mvas, ok := g.entries[pq]; ok {
		return mvas
	} else {
		// Center of the grid cell
		pc := [2]float32{(float32(pq[0]) + 0.5) / 10, (float32(pq[1]) + 0.5) / 10}

		mvas := util.FilterSlice(g.mvas, func(m MVA) bool {
			// Check if grid cell center is within 10nm of the MVA bounds
			return math.NMDistance2LL(m.Bounds.ClosestPointInBox(pc), pc) < 10
		})
		g.entries[pq] = mvas
		return mvas
	}
}

// GetMVA returns the maximum MVA altitude at the given location.
// Returns 0 if no MVA covers the point.
func (g *MVAGrid) GetMVA(p [2]float32) int {
	for _, mva := range g.getEntries(p) {
		if mva.Inside(p) {
			return mva.MinimumLimit
		}
	}
	return 0
}

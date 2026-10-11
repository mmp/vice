// aviation/db/grid.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"iter"
	"slices"

	av "github.com/mmp/vice/aviation"
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

// airspaceCell returns the cell of an AirspaceGrid that p lies in: a tenth
// of a degree on a side, roughly 6nm in latitude.
func airspaceCell(p math.Point2LL) [2]int {
	return [2]int{int(math.Floor(10 * p[0])), int(math.Floor(10 * p[1]))}
}

// airspaceCellMargin is how far, in nm, beyond a grid cell the volumes it
// holds may reach. Lookups for points that close to a cell may use its
// entries.
const airspaceCellMargin = 2

func (g *AirspaceGrid) getEntries(pq [2]int) []*av.AirspaceVolume {
	if vols, ok := g.entries[pq]; ok {
		return vols
	}

	p0 := math.Point2LL{float32(pq[0]) / 10, float32(pq[1]) / 10}
	dlat := float32(airspaceCellMargin) / 60
	dlong := airspaceCellMargin / math.NMPerLongitudeAt(math.Point2LL{p0[0], p0[1] + 0.05})
	cell := math.Extent2D{
		P0: [2]float32{p0[0] - dlong, p0[1] - dlat},
		P1: [2]float32{p0[0] + 0.1 + dlong, p0[1] + 0.1 + dlat},
	}
	// Assumes both polygonal and an initialized PolygonBounds...
	vols := util.FilterSlice(g.volumes, func(v *av.AirspaceVolume) bool { return math.Overlaps(cell, *v.PolygonBounds) })
	g.entries[pq] = vols
	return vols
}

func (g *AirspaceGrid) Inside(p math.Point2LL, alt int) bool {
	for _, vol := range g.getEntries(airspaceCell(p)) {
		if vol.Inside(p, alt) {
			return true
		}
	}
	return false
}

func (g *AirspaceGrid) Below(p math.Point2LL, alt int) bool {
	for _, vol := range g.getEntries(airspaceCell(p)) {
		if vol.Below(p, alt) {
			return true
		}
	}
	return false
}

// Over returns the volumes that lie over p.
func (g *AirspaceGrid) Over(p math.Point2LL) iter.Seq[*av.AirspaceVolume] {
	return func(yield func(*av.AirspaceVolume) bool) {
		for _, vol := range g.getEntries(airspaceCell(p)) {
			if vol.Covers(p) && !yield(vol) {
				return
			}
		}
	}
}

// Crossed returns the volumes that lie over some part of the segment from a
// to b.
func (g *AirspaceGrid) Crossed(a, b math.Point2LL) []*av.AirspaceVolume {
	// Every point of the segment is within airspaceCellMargin of one of
	// these, so their cells hold every volume it could pass under.
	var candidates []*av.AirspaceVolume
	n := 1 + int(math.NMDistance2LL(a, b)/(2*airspaceCellMargin))
	var prev [2]int
	for i := range n + 1 {
		pq := airspaceCell(math.Lerp2f(float32(i)/float32(n), a, b))
		if i > 0 && pq == prev {
			continue
		}
		prev = pq
		for _, vol := range g.getEntries(pq) {
			if !slices.Contains(candidates, vol) {
				candidates = append(candidates, vol)
			}
		}
	}
	return util.FilterSlice(candidates, func(vol *av.AirspaceVolume) bool { return vol.CoversSegment(a, b) })
}

// Near returns the volumes that lie over some point within r nm of p, for r
// no more than airspaceCellMargin.
func (g *AirspaceGrid) Near(p math.Point2LL, r float32) []*av.AirspaceVolume {
	return util.FilterSlice(g.getEntries(airspaceCell(p)), func(vol *av.AirspaceVolume) bool { return vol.Near(p, r) })
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

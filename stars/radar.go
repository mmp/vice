// stars/radar.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"maps"
	"slices"
	"sort"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

func (sp *Pane) makeSignificantPoints(ss client.SimState) {
	sp.significantPoints = maps.Clone(ss.FacilityAdaptation.SignificantPoints)
	sp.significantPointsSlice = nil
	for _, pt := range sp.significantPoints {
		sp.significantPointsSlice = append(sp.significantPointsSlice, pt)
	}

	tryAdd := func(name string, desc string, loc math.Point2LL) {
		if _, ok := sp.significantPoints[name]; ok {
			return
		}

		pt := sim.SignificantPoint{
			Name:        name,
			Description: desc,
			Location:    av.ScenarioPoint2LL{Point2LL: loc},
		}
		sp.significantPoints[name] = pt
		sp.significantPointsSlice = append(sp.significantPointsSlice, pt)
	}

	// Adapted fix-pair airports carry their own name, location, and
	// abbreviation; add them ahead of the database airports below so the
	// adaptation wins.
	for id, ap := range ss.FacilityAdaptation.Airports {
		if _, ok := sp.significantPoints[id]; ok {
			continue
		}
		pt := sim.SignificantPoint{
			Name:         id,
			Abbreviation: ap.Abbreviation,
			Description:  ap.Name,
			Location:     ap.Location,
		}
		sp.significantPoints[id] = pt
		sp.significantPointsSlice = append(sp.significantPointsSlice, pt)
	}

	// All airports within 250nm
	center := ss.GetInitialCenter()
	for name, ap := range db.DB.Airports {
		if math.NMDistance2LL(ap.Location, center) < 250 {
			id := db.AirportDisplayId(name)
			tryAdd(id, id+" AIRPORT", ap.Location)

			for _, rwy := range ap.Runways {
				// e.g. JFK22LT -> JFK RWY 22L THRESHOLD
				tryAdd(id+rwy.Id+"T", id+" RWY "+rwy.Id+" THRESHOLD", rwy.Threshold)
			}
		}
	}

	for name, nav := range db.DB.Navaids {
		if math.NMDistance2LL(nav.Location, center) < 250 {
			tryAdd(name, name+" "+nav.Type, nav.Location)
		}
	}

	for name, fix := range db.DB.Fixes {
		if math.NMDistance2LL(fix.Location, center) < 250 {
			// FIXME: should be INTERSECTION not WAYPOINT potentially
			tryAdd(name, name+" WAYPOINT", fix.Location)
		}
	}

	// Flight plans carry the 3-character fix id of an adapted significant
	// point: its short name, or the first three characters of its name when no
	// short name is adapted. Alias the points by that id so lookups by
	// flight-plan fix resolve.
	fa := &ss.FacilityAdaptation
	for _, name := range slices.Sorted(maps.Keys(fa.SignificantPoints)) {
		if id := fa.FixPairFixID(name); id != name {
			if _, ok := sp.significantPoints[id]; !ok {
				sp.significantPoints[id] = fa.SignificantPoints[name]
			}
		}
	}

	// Sort the slice
	slices.SortFunc(sp.significantPointsSlice, func(a, b sim.SignificantPoint) int {
		return strings.Compare(a.Name, b.Name)
	})
}

const (
	RadarModeSingle = iota
	RadarModeMulti
	RadarModeFused
)

func (sp *Pane) radarMode(radarSites map[string]*av.RadarSite) int {
	if len(radarSites) == 0 {
		// Straight-up fused mode if none are specified.
		return RadarModeFused
	}

	ps := sp.currentPrefs()
	if _, ok := radarSites[ps.RadarSiteSelected]; ps.RadarSiteSelected != "" && ok {
		return RadarModeSingle
	} else if ps.FusedRadarMode {
		return RadarModeFused
	} else {
		return RadarModeMulti
	}
}

func (sp *Pane) updateVisibleTracks(ctx *scope.Context) {
	sp.visibleTracks = sp.visibleTracks[:0]

	ps := sp.currentPrefs()
	single := sp.radarMode(ctx.FacilityAdaptation.RadarSites) == RadarModeSingle

	for _, trk := range ctx.Client.State.Tracks {
		visible := false
		state := sp.TrackState[trk.ADSBCallsign]

		if trk.IsUnsupportedDB() {
			visible = true
		} else if sp.radarMode(ctx.FacilityAdaptation.RadarSites) == RadarModeFused {
			// If it hasn't been culled server-side due to e.g. the surface tracking filters,
			// we can see it.
			visible = true
		} else {
			// Otherwise see if any of the radars can see it
			for id, site := range ctx.FacilityAdaptation.RadarSites {
				if single && ps.RadarSiteSelected != id {
					continue
				}

				if p, s, _ := site.CheckVisibility(trk.Location, int(trk.TrueAltitude)); p || s {
					visible = true
				}
			}
		}

		if visible {
			sp.visibleTracks = append(sp.visibleTracks, *trk)

			// Is this the first we've seen it?
			if state.FirstRadarTrackTime.IsZero() {
				state.FirstRadarTrackTime = ctx.InterpolatedSimTime
			}
		}
	}

	// Per-aircraft stuff: tracks, datablocks, vector lines, range rings, ...
	// Sort the aircraft so that they are always drawn in the same order
	// (go's map iterator randomization otherwise randomizes the order,
	// which can cause shimmering when datablocks overlap (especially if
	// one is selected). We'll go with alphabetical by callsign, with the
	// selected aircraft, if any, always drawn last.
	sort.Slice(sp.visibleTracks, func(i, j int) bool {
		return sp.visibleTracks[i].ADSBCallsign < sp.visibleTracks[j].ADSBCallsign
	})
}

func (sp *Pane) radarSiteId(radarSites map[string]*av.RadarSite) string {
	switch sp.radarMode(radarSites) {
	case RadarModeSingle:
		return sp.currentPrefs().RadarSiteSelected
	case RadarModeMulti:
		return "MULTI"
	case RadarModeFused:
		return "FUSED"
	default:
		return "UNKNOWN"
	}
}

func (sp *Pane) setRadarModeMulti() {
	ps := sp.currentPrefs()

	ps.RadarSiteSelected = ""
	if ps.FusedRadarMode {
		sp.discardTracks = true
		ps.FusedRadarMode = false
	}
}

func (sp *Pane) setRadarModeFused() {
	ps := sp.currentPrefs()

	ps.RadarSiteSelected = ""
	if !ps.FusedRadarMode {
		ps.FusedRadarMode = true
		sp.discardTracks = true
	}
}

// Returns the cardinal-ordinal direction associated with the numbpad keys,
// interpreting 5 as the center; (nil, true) is returned for '5' and
// (nil, false) is returned for an invalid key.
func (sp *Pane) numpadToDirection(key int) (*math.CardinalOrdinalDirection, bool) {
	if key < 1 || key > 9 {
		return nil, false
	}
	if key == 5 {
		return nil, true
	}
	if sp.FlipNumericKeypad {
		dirs := [9]math.CardinalOrdinalDirection{
			math.NorthWest, math.North, math.NorthEast,
			math.West, math.CardinalOrdinalDirection(-1), math.East,
			math.SouthWest, math.South, math.SouthEast,
		}
		return &dirs[key-1], true
	} else {
		dirs := [9]math.CardinalOrdinalDirection{
			math.SouthWest, math.South, math.SouthEast,
			math.West, math.CardinalOrdinalDirection(-1), math.East,
			math.NorthWest, math.North, math.NorthEast,
		}
		return &dirs[key-1], true
	}
}

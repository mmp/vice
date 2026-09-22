// scenario/filters.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"maps"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// airportVolumeId names a default airport filter region within the
// 7-character limit on airspace volume ids; a 4-character airport identifier
// with a 4-character suffix runs over and is truncated.
func airportVolumeId(airport, suffix string) string {
	id := airport + suffix
	if len(id) > 7 {
		id = id[:7]
	}
	return id
}

func makeCircleAirportFilters(id string, description string, radius float32,
	ceiling int, airports []av.ICAOAirportCode, e *util.ErrorLogger) sim.FilterRegions {
	var regions sim.FilterRegions
	for _, apname := range airports {
		ap, ok := db.DB.Airports[apname]
		if !ok {
			e.ErrorString("Airport %q not found", apname)
			continue
		}
		name := db.AirportDisplayId(apname)
		regions = append(regions, sim.FilterRegion{
			AirspaceVolume: av.AirspaceVolume{
				Id:          airportVolumeId(name, id),
				Description: name + " " + description,
				Type:        av.AirspaceVolumeCircle,
				Floor:       ap.Elevation,
				Ceiling:     ap.Elevation + ceiling,
				Center:      av.ScenarioPoint2LL{Point2LL: ap.Location},
				Radius:      radius,
			},
		})
	}
	return regions
}

func makePolygonAirportFilters(id string, description string, delta float32,
	ceiling int, airports []av.ICAOAirportCode, nmPerLongitude float32, e *util.ErrorLogger) sim.FilterRegions {
	var regions sim.FilterRegions
	for _, apname := range airports {
		ap, ok := db.DB.Airports[apname]
		if !ok {
			e.ErrorString("Airport %q not found", apname)
			continue
		}
		name := db.AirportDisplayId(apname)

		p := util.MapSlice(ap.Runways, func(r av.Runway) [2]float32 { return math.LL2NM(r.Threshold, nmPerLongitude) })
		var hull [][2]float32

		if len(p) == 2 {
			// Single runway so compute an OBB directly.
			v := math.Normalize2f(math.Sub2f(p[1], p[0]))
			v = math.Scale2f(v, delta)
			nv := math.Scale2f(v, -1)
			vp := [2]float32{v[1], -v[0]} // perp
			nvp := math.Scale2f(vp, -1)

			hull = [][2]float32{
				math.Add2f(p[0], math.Add2f(nv, vp)),
				math.Add2f(p[1], math.Add2f(v, vp)),
				math.Add2f(p[1], math.Add2f(v, nvp)),
				math.Add2f(p[0], math.Add2f(nv, nvp))}
		} else {
			// Convex hull of the runway threshold points
			hull = math.ConvexHull(p)

			// Expand the hull by delta: hacky polygon dilation--
			// compute the average point as a center and then offset
			// each away from it.
			var c [2]float32
			for _, p := range hull {
				c = math.Add2f(c, p)
			}
			c = math.Scale2f(c, 1/float32(len(hull)))
			for i := range hull {
				v := math.Sub2f(hull[i], c)
				hull[i] = math.Add2f(hull[i], math.Scale2f(v, delta))
			}
		}

		// Back to lat-long for the AirspaceVolume
		pll := util.MapSlice(hull, func(p [2]float32) math.Point2LL { return math.NM2LL(p, nmPerLongitude) })

		regions = append(regions, sim.FilterRegion{
			AirspaceVolume: av.AirspaceVolume{
				Id:          airportVolumeId(name, id),
				Description: name + " " + description,
				Type:        av.AirspaceVolumePolygon,
				Floor:       ap.Elevation,
				Ceiling:     ap.Elevation + ceiling,
				Vertices:    pll,
			},
		})
	}
	return regions
}

// pruneAirportFilters removes the filter regions for airports the scenario
// doesn't use. A facility's adaptation covers all of its airports but a
// scenario generally uses only a few of them; the rest are just clutter in
// the processing areas list. A region that doesn't cover any of the
// facility's airports (e.g. a facility-wide suppression area) is always kept.
//
// Which airports count depends on what the filter does. Alerting and
// acquisition are only of interest where the scenario has IFR traffic.
// Arrival drop and surface tracking, however, determine whether an aircraft
// has a track at all: surface tracking is what keeps aircraft on the ground
// from painting, so pruning it at an airport with VFR traffic would put
// taxiing aircraft on the scope. Filters that aren't tied to an airport at
// all--secondary drop and VFR inhibit, which restrict airspace--are left
// alone.
func pruneAirportFilters(fa *sim.FacilityAdaptation, airports []av.ICAOAirportCode, dep []sim.DepartureRunway,
	arr []sim.ArrivalRunway, vfrRates map[av.ICAOAirportCode]float32) {
	ifr := make(map[av.ICAOAirportCode]bool)
	for _, rwy := range dep {
		ifr[rwy.Airport] = true
	}
	for _, rwy := range arr {
		ifr[rwy.Airport] = true
	}
	any := maps.Clone(ifr)
	for icao, rate := range vfrRates {
		if rate > 0 {
			any[icao] = true
		}
	}

	prune := func(regions *sim.FilterRegions, active map[av.ICAOAirportCode]bool) {
		*regions = util.FilterSlice(*regions, func(r sim.FilterRegion) bool {
			covered := false
			for _, name := range airports {
				ap, ok := db.DB.Airports[name]
				if !ok || !r.Inside(ap.Location, ap.Elevation) {
					continue
				}
				if active[name] {
					return true
				}
				covered = true
			}
			return !covered
		})
	}

	f := &fa.Filters
	prune(&f.AutoAcquisition, ifr)
	prune(&f.Departure, ifr)
	prune(&f.InhibitCA, ifr)
	prune(&f.InhibitMSAW, ifr)
	prune(&f.ArrivalDrop, any)
	prune(&f.SurfaceTracking, any)
}

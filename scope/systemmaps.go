// scope/systemmaps.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"fmt"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

// SystemMapSpec describes the facility whose system maps are to be generated.
type SystemMapSpec struct {
	Facility          string
	Center            math.Point2LL
	NmPerLongitude    float32
	MagneticVariation float32
	Adaptation        *sim.FacilityAdaptation
	Airports          map[av.ICAOAirportCode]*av.Airport
	ArrivalAirports   map[av.ICAOAirportCode]any
}

// SystemMaps returns the maps that are synthesized at runtime rather than
// read from a video map library: the adapted filter regions, minimum
// vectoring altitudes, nearby class B and C airspace, radar coverage, and
// ATPA approach volumes. Ids follow the STARS convention of numbering
// filter regions and airspace from 700, radar coverage from 801, and ATPA
// volumes from 901; resolving collisions with library map ids is the
// caller's business.
func SystemMaps(spec SystemMapSpec) []Map {
	g := systemMapGen{spec: spec, id: 700}

	fa := spec.Adaptation
	g.addRegions("CASU", "CA SUPPRESSION AREA ALL", filterVolumes(fa.Filters.InhibitCA))
	g.addRegions("MSAWSU", "MSAW SUPPRESSION AREA ALL", filterVolumes(fa.Filters.InhibitMSAW))
	g.addRegions("AUTOACQ", "AUTO ACQUISITION AREA ALL", filterVolumes(fa.Filters.AutoAcquisition))
	g.addRegions("ARRDEP", "ARRIVAL DROP AREA ALL", filterVolumes(fa.Filters.ArrivalDrop))
	g.addRegions("DEP", "DEPARTURE AREA ALL", filterVolumes(fa.Filters.Departure))
	g.addRegions("SECDROP", "SECONDARY DROP AREA ALL", filterVolumes(fa.Filters.SecondaryDrop))
	g.addRegions("SURFTRK", "SURFACE TRACKING AREA ALL", filterVolumes(fa.Filters.SurfaceTracking))
	g.addRegions("QLRGNS", "QUICKLOOK REGIONS ALL",
		util.MapSlice(fa.Filters.Quicklook, func(r sim.QuicklookRegion) av.AirspaceVolume { return r.AirspaceVolume }))
	g.addRegions("FDAMRGNS", "FDAM REGIONS ALL",
		util.MapSlice(fa.Filters.FDAM, func(r sim.FDAMRegion) av.AirspaceVolume { return r.AirspaceVolume }))
	g.addRegions("VFRINH", "VFR INHIBIT AREA ALL", filterVolumes(fa.Filters.VFRInhibit))
	g.addRegions("HORGNS", "HANDOFF REGIONS ALL",
		util.MapSlice(fa.Filters.Handoff, func(r sim.HandoffFilterRegion) av.AirspaceVolume { return r.AirspaceVolume }))

	g.addMVAs()
	g.addClassAirspace(av.DB.BravoAirspace, "B")
	g.addClassAirspace(av.DB.CharlieAirspace, "C")

	g.id = 801
	g.addRadarCoverage()

	g.id = 901
	g.addATPAVolumes()

	return g.maps
}

// filterVolumes extracts the airspace volume of each of a filter kind's
// regions. The filter region types all embed av.AirspaceVolume but are
// otherwise unrelated, so the ones that aren't plain sim.FilterRegions map
// themselves at the call site.
func filterVolumes(regions sim.FilterRegions) []av.AirspaceVolume {
	return util.MapSlice(regions, func(r sim.FilterRegion) av.AirspaceVolume { return r.AirspaceVolume })
}

type systemMapGen struct {
	spec SystemMapSpec
	maps []Map
	id   int
}

func (g *systemMapGen) add(label, name string, draw func(cb *renderer.CommandBuffer)) {
	m := Map{
		STARSMap: videomaps.STARSMap{
			Label:    label,
			Name:     name,
			Id:       g.id,
			Category: VideoMapProcessingAreas,
		},
	}
	g.id++
	draw(&m.CommandBuffer)
	g.maps = append(g.maps, m)
}

// addRegions adds a map holding all of a filter kind's regions followed by
// one map per region.
func (g *systemMapGen) addRegions(label, name string, volumes []av.AirspaceVolume) {
	if len(volumes) == 0 {
		return
	}

	g.add(label, name, func(cb *renderer.CommandBuffer) {
		for _, v := range volumes {
			g.drawAirspace(v, cb)
		}
	})

	for _, v := range volumes {
		g.add(strings.ToUpper(v.Id), strings.ToUpper(v.Description), func(cb *renderer.CommandBuffer) {
			g.drawAirspace(v, cb)
		})
	}
}

func (g *systemMapGen) drawAirspace(a av.AirspaceVolume, cb *renderer.CommandBuffer) {
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	switch a.Type {
	case av.AirspaceVolumePolygon:
		var v [][2]float32
		for _, vtx := range a.Vertices {
			v = append(v, [2]float32(vtx))
		}
		ld.AddLineLoop(v)

		for _, h := range a.Holes {
			var v [][2]float32
			for _, vtx := range h {
				v = append(v, [2]float32(vtx))
			}
			ld.AddLineLoop(v)
		}
	case av.AirspaceVolumeCircle:
		ld.AddLatLongCircle(a.Center, g.spec.NmPerLongitude, a.Radius, 360)
	default:
		panic("unhandled AirspaceVolume type")
	}

	ld.GenerateCommands(cb)
}

func (g *systemMapGen) addMVAs() {
	g.add(g.spec.Facility+" MVA", "ALL MINIMUM VECTORING ALTITUDES", func(cb *renderer.CommandBuffer) {
		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		for _, mva := range av.DB.MVAs[g.spec.Facility] {
			ld.AddLineLoop(mva.ExteriorRing)
			p := math.Extent2DFromPoints(mva.ExteriorRing).Center()
			ld.AddNumber(p, 0.005, fmt.Sprintf("%d", mva.MinimumLimit/100))
		}
		ld.GenerateCommands(cb)
	})
}

func (g *systemMapGen) addClassAirspace(airspace map[string][]av.AirspaceVolume, class string) {
	center := g.spec.Center
	// Sorted so the ids these maps land on are the same from run to run.
	for name, volumes := range util.SortedMap(airspace) {
		if math.NMDistance2LL(volumes[0].PolygonBounds.ClosestPointInBox(center), center) > 75 {
			continue
		}

		g.add(name, name+" CLASS "+class, func(cb *renderer.CommandBuffer) {
			for _, v := range volumes {
				g.drawAirspace(v, cb)
			}
		})
	}
}

func (g *systemMapGen) addRadarCoverage() {
	for name, site := range util.SortedMap(g.spec.Adaptation.RadarSites) {
		g.add(name+"RCM", name+" RADAR COVERAGE MAP", func(cb *renderer.CommandBuffer) {
			ld := renderer.GetLinesDrawBuilder()
			defer renderer.ReturnLinesDrawBuilder(ld)

			ld.AddLatLongCircle(site.Position, g.spec.NmPerLongitude, float32(site.PrimaryRange), 360)
			ld.AddLatLongCircle(site.Position, g.spec.NmPerLongitude, float32(site.SecondaryRange), 360)
			ld.GenerateCommands(cb)
		})
	}
}

func (g *systemMapGen) addATPAVolumes() {
	for _, name := range util.SortedMapKeys(g.spec.ArrivalAirports) {
		ap := g.spec.Airports[name]
		for rwy, vol := range util.SortedMap(ap.ATPAVolumes) {
			label := "A" + av.AirportDisplayId(name) + rwy
			if len(label) > 7 {
				label = label[:7]
			}

			g.add(label, string(name)+rwy+" ATPA APPROACH VOLUME", func(cb *renderer.CommandBuffer) {
				ld := renderer.GetLinesDrawBuilder()
				defer renderer.ReturnLinesDrawBuilder(ld)

				rect := vol.GetRect(g.spec.NmPerLongitude, g.spec.MagneticVariation)
				for i := range rect {
					ld.AddLine(rect[i], rect[(i+1)%len(rect)])
				}
				ld.GenerateCommands(cb)
			})
		}
	}
}

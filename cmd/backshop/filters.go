// cmd/backshop/filters.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// drawFiltersTab lists the adapted filter regions and the other adapted
// volumes, each with a checkbox that draws its extent on the map.
func (in *inspector) drawFiltersTab(a *app) {
	if !imgui.BeginTabItem("Filters") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	ss := &a.cc.State
	f := &ss.FacilityAdaptation.Filters

	imgui.Text("Color:")
	imgui.SameLine()
	imgui.ColorEdit3V("##filtercolor", &in.overlays.volumeColor,
		imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)
	imgui.SameLine()
	if imgui.Button("Draw none") {
		clear(in.overlays.volumes)
	}
	imgui.Separator()

	regionRows := func(regions sim.FilterRegions) []filterRow {
		return util.MapSlice(regions, func(r sim.FilterRegion) filterRow {
			return filterRow{vol: r.AirspaceVolume, source: util.Select(r.Default, sourceDefault, sourceAdapted)}
		})
	}
	adaptedRows := func(volumes []av.AirspaceVolume) []filterRow {
		return util.MapSlice(volumes, func(v av.AirspaceVolume) filterRow {
			return filterRow{vol: v, source: sourceAdapted}
		})
	}

	in.filterSection(a, "CA suppression", regionRows(f.InhibitCA))
	in.filterSection(a, "MSAW suppression", regionRows(f.InhibitMSAW))
	in.filterSection(a, "Auto acquisition", regionRows(f.AutoAcquisition))
	in.filterSection(a, "Arrival drop", regionRows(f.ArrivalDrop))
	in.filterSection(a, "Departure", regionRows(f.Departure))
	in.filterSection(a, "Secondary drop", regionRows(f.SecondaryDrop))
	in.filterSection(a, "Surface tracking", regionRows(f.SurfaceTracking))
	in.filterSection(a, "VFR inhibit", regionRows(f.VFRInhibit))
	in.filterSection(a, "Quicklook", adaptedRows(
		util.MapSlice(f.Quicklook, func(r sim.QuicklookRegion) av.AirspaceVolume { return r.AirspaceVolume })))
	in.filterSection(a, "FDAM", adaptedRows(
		util.MapSlice(f.FDAM, func(r sim.FDAMRegion) av.AirspaceVolume { return r.AirspaceVolume })))
	in.filterSection(a, "Handoff", adaptedRows(
		util.MapSlice(f.Handoff, func(r sim.HandoffFilterRegion) av.AirspaceVolume { return r.AirspaceVolume })))

	// ATPA volumes and TFRs come from the scenario and the TFR feed rather
	// than the adaptation, so there is nothing to say about where they came
	// from that the section name doesn't already.
	in.filterSection(a, "ATPA approach volumes", rows(atpaVolumes(ss)))
	in.filterSection(a, "TFRs", rows(tfrVolumes(ss)))

	if len(ss.FacilityAdaptation.RadarSites) > 0 && imgui.CollapsingHeaderBoolPtr("Radar sites", nil) {
		flags, size := tableSize(len(ss.FacilityAdaptation.RadarSites), 10)
		if imgui.BeginTableV("radar", 5, flags, size, 0) {
			setupDrawColumn()
			imgui.TableSetupColumnV("Site", imgui.TableColumnFlagsWidthFixed, 60, 0)
			imgui.TableSetupColumnV("Primary", imgui.TableColumnFlagsWidthFixed, 60, 0)
			imgui.TableSetupColumnV("Secondary", imgui.TableColumnFlagsWidthFixed, 70, 0)
			imgui.TableSetupColumn("Position")
			imgui.TableHeadersRow()
			for name, site := range util.SortedMap(ss.FacilityAdaptation.RadarSites) {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				for _, r := range []int32{site.PrimaryRange, site.SecondaryRange} {
					id := fmt.Sprintf("rcm-%s-%d", name, r)
					drawToggle(in.overlays.volumes, id, func() mapVolume {
						return mapVolume{
							label: name,
							vol: av.AirspaceVolume{
								Type: av.AirspaceVolumeCircle, Center: site.Position, Radius: float32(r),
							},
						}
					})
					imgui.SameLine()
				}
				imgui.NewLine()
				imgui.TableNextColumn()
				imgui.Text(name)
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%d nm", site.PrimaryRange))
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%d nm", site.SecondaryRange))
				imgui.TableNextColumn()
				in.locationCell(a, "rcm"+name, site.Position.Point2LL)
			}
			imgui.EndTable()
		}
	}
}

// locationCell draws a lat-long that can be copied and that centers the map
// when double-clicked. id keeps rows distinct.
func (in *inspector) locationCell(a *app, id string, p math.Point2LL) {
	dms := strings.ReplaceAll(p.DMSString(), " ", "")
	if imgui.SelectableBool(dms + "##" + id) {
		a.plat.GetClipboard().SetClipboard(dms)
		a.status = "copied " + dms
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Click to copy; double-click to center the map here")
	}
	if imgui.IsItemHovered() && imgui.IsMouseDoubleClicked(0) {
		a.scope.center = p
	}
}

// setupDrawColumn declares the narrow leading column every table here uses
// for its draw checkbox.
func setupDrawColumn() {
	imgui.TableSetupColumnV("Draw", imgui.TableColumnFlagsWidthFixed|imgui.TableColumnFlagsNoResize, 36, 0)
}

func atpaVolumes(ss *client.SimState) []av.AirspaceVolume {
	var out []av.AirspaceVolume
	for _, name := range util.SortedMapKeys(ss.ArrivalAirports) {
		ap := ss.Airports[name]
		for rwy, vol := range util.SortedMap(ap.ATPAVolumes) {
			rect := vol.GetRect(ss.NmPerLongitude, ss.MagneticVariation)
			out = append(out, av.AirspaceVolume{
				Id:          string(name) + rwy,
				Description: "ATPA approach volume",
				Type:        av.AirspaceVolumePolygon,
				Floor:       int(vol.Floor),
				Ceiling:     int(vol.Ceiling),
				Vertices:    rect[:],
				PolygonBounds: func() *math.Extent2D {
					e := math.Extent2DFromPoints(util.MapSlice(rect[:],
						func(p math.Point2LL) [2]float32 { return p }))
					return &e
				}(),
			})
		}
	}
	return out
}

func tfrVolumes(ss *client.SimState) []av.AirspaceVolume {
	var out []av.AirspaceVolume
	for _, tfr := range ss.TFRs {
		for i, loop := range tfr.Points {
			e := math.Extent2DFromPoints(util.MapSlice(loop, func(p math.Point2LL) [2]float32 { return p }))
			out = append(out, av.AirspaceVolume{
				Id:            fmt.Sprintf("%s-%d", tfr.LocalName, i),
				Description:   tfr.Type + " " + tfr.AltDescr,
				Type:          av.AirspaceVolumePolygon,
				Vertices:      loop,
				PolygonBounds: &e,
			})
		}
	}
	return out
}

// Where a region came from, as the Source column puts it: the facility's own
// adaptation, or vice, which draws default filters around the runways of an
// airport the adaptation leaves uncovered.
const (
	sourceAdapted = "adapted"
	sourceDefault = "default"
)

// filterRow is one region as the tab lists it: the volume to draw, and where it
// came from when that is a question worth answering.
type filterRow struct {
	vol    av.AirspaceVolume
	source string
}

// rows lists volumes that have no adaptation to have come from.
func rows(volumes []av.AirspaceVolume) []filterRow {
	return util.MapSlice(volumes, func(v av.AirspaceVolume) filterRow { return filterRow{vol: v} })
}

func (in *inspector) filterSection(a *app, name string, regions []filterRow) {
	if len(regions) == 0 {
		return
	}

	defaults := 0
	for _, r := range regions {
		if r.source == sourceDefault {
			defaults++
		}
	}
	label := fmt.Sprintf("%s (%d)", name, len(regions))
	if defaults > 0 {
		label += fmt.Sprintf(", %d default", defaults)
	}
	if !imgui.CollapsingHeaderBoolPtr(label, nil) {
		return
	}

	imgui.PushIDStr(name)
	defer imgui.PopID()

	if imgui.SmallButton("Draw all") {
		for i, r := range regions {
			in.overlays.volumes[overlayID(name, i, r.vol)] = mapVolume{label: r.vol.Id, vol: r.vol}
		}
	}
	imgui.SameLine()
	if imgui.SmallButton("Draw none") {
		for i, r := range regions {
			delete(in.overlays.volumes, overlayID(name, i, r.vol))
		}
	}

	flags, size := tableSize(len(regions), 10)
	if !imgui.BeginTableV("f", 6, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Id", imgui.TableColumnFlagsWidthFixed, 90, 0)
	imgui.TableSetupColumnV("Source", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Shape", imgui.TableColumnFlagsWidthFixed, 110, 0)
	imgui.TableSetupColumnV("Alts", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumn("Description")
	imgui.TableHeadersRow()

	for i, r := range regions {
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		drawToggle(in.overlays.volumes, overlayID(name, i, r.vol), func() mapVolume {
			return mapVolume{label: r.vol.Id, vol: r.vol}
		})
		imgui.TableNextColumn()
		imgui.Text(r.vol.Id)
		imgui.TableNextColumn()
		if r.source == sourceDefault {
			imgui.TextDisabled(r.source)
			tooltip("The facility's adaptation covers no filter here, so vice drew one\n" +
				"around the airport's runways.")
		} else {
			imgui.Text(r.source)
		}
		imgui.TableNextColumn()
		imgui.Text(volumeShape(r.vol))
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("%d-%d", r.vol.Floor, r.vol.Ceiling))
		imgui.TableNextColumn()
		imgui.TextWrapped(r.vol.Description)
		imgui.PopID()
	}
	imgui.EndTable()
}

// overlayID names a region uniquely across the filter kinds, which reuse ids
// between them.
func overlayID(kind string, i int, v av.AirspaceVolume) string {
	return fmt.Sprintf("%s/%d/%s", kind, i, v.Id)
}

func volumeShape(v av.AirspaceVolume) string {
	switch v.Type {
	case av.AirspaceVolumeCircle:
		return fmt.Sprintf("circle r=%.1f nm", v.Radius)
	case av.AirspaceVolumePolygon:
		s := fmt.Sprintf("polygon, %d pts", len(v.Vertices))
		if len(v.Holes) > 0 {
			s += fmt.Sprintf(" (%d holes)", len(v.Holes))
		}
		return s
	default:
		return ""
	}
}

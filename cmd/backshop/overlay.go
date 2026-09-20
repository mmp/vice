// cmd/backshop/overlay.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// mapVolume is an airspace volume the Filters tab has asked to be drawn.
type mapVolume struct {
	label string
	vol   av.AirspaceVolume
}

// mapPoint is a database entry the Database tab has asked to be drawn.
type mapPoint struct {
	label string
	p     math.Point2LL
}

// overlays is the geometry the inspector puts on the map that is neither a
// video map nor a procedure: filter regions and entries from the aviation
// database. Each is keyed by an id so that a checkbox can add and remove it.
type overlays struct {
	volumes map[string]mapVolume
	points  map[string]mapPoint

	volumeColor [3]float32
	pointColor  [3]float32
}

func (o *overlays) init() {
	o.volumes = make(map[string]mapVolume)
	o.points = make(map[string]mapPoint)
	o.volumeColor = [3]float32{.2, .8, .8}
	o.pointColor = [3]float32{.5, .9, .5}
}

// drawToggle draws a draw/don't-draw checkbox for one item, adding it to or
// removing it from the set as it changes.
func drawToggle[T any](set map[string]T, id string, value func() T) {
	_, on := set[id]
	if imgui.Checkbox("##draw-"+id, &on) {
		if on {
			set[id] = value()
		} else {
			delete(set, id)
		}
	}
}

func (in *inspector) drawOverlays(a *app, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	o := &in.overlays
	if len(o.volumes) == 0 && len(o.points) == 0 {
		return
	}

	nmPerLongitude := a.cc.State.NmPerLongitude
	volColor, ptColor := rgb(o.volumeColor), rgb(o.pointColor)

	// Volumes are drawn in lat-long so that circles stay circles; the
	// markers and all the text are in window coordinates.
	worldLines := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(worldLines)
	windowLines := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(windowLines)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	volStyle := renderer.TextStyle{Font: a.scope.textFont, Color: volColor, DrawBackground: true}
	ptStyle := renderer.TextStyle{Font: a.scope.textFont, Color: ptColor, DrawBackground: true}

	for _, v := range util.SortedMap(o.volumes) {
		drawVolume(v.vol, nmPerLongitude, volColor, worldLines)
		td.AddTextCentered(v.label, transforms.WindowFromLatLongP(volumeCenter(v.vol)), volStyle)
	}

	for _, pt := range util.SortedMap(o.points) {
		pw := transforms.WindowFromLatLongP(pt.p)
		const half = 4
		windowLines.AddLine([2]float32{pw[0] - half, pw[1] - half}, [2]float32{pw[0] + half, pw[1] + half}, ptColor)
		windowLines.AddLine([2]float32{pw[0] - half, pw[1] + half}, [2]float32{pw[0] + half, pw[1] - half}, ptColor)
		td.AddText(pt.label, [2]float32{pw[0] + 6, pw[1] + 14}, ptStyle)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	worldLines.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	windowLines.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func drawVolume(v av.AirspaceVolume, nmPerLongitude float32, color renderer.RGB, ld *renderer.ColoredLinesDrawBuilder) {
	switch v.Type {
	case av.AirspaceVolumeCircle:
		ld.AddLatLongCircle(v.Center, nmPerLongitude, v.Radius, 90, color)
	case av.AirspaceVolumePolygon:
		addLoop(ld, v.Vertices, color)
		for _, h := range v.Holes {
			addLoop(ld, h, color)
		}
	}
}

func addLoop(ld *renderer.ColoredLinesDrawBuilder, pts []math.Point2LL, color renderer.RGB) {
	for i := range pts {
		ld.AddLine(pts[i], pts[(i+1)%len(pts)], color)
	}
}

func volumeCenter(v av.AirspaceVolume) math.Point2LL {
	if v.Type == av.AirspaceVolumePolygon && v.PolygonBounds != nil {
		return v.PolygonBounds.Center()
	}
	return v.Center
}

// radar/videomaps.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// ClientVideoMap extends sim.VideoMap with client-side rendering capabilities.
type ClientVideoMap struct {
	sim.VideoMap

	// CommandBuffer holds commands to draw the solid-line geometry; dashed lines, symbols, and
	// labels are kept on the embedded VideoMap and drawn separately at draw time so their stipple
	// pattern / glyph / text can account for scope scale and display DPI.
	CommandBuffer renderer.CommandBuffer
}

type ERAMVideoMap struct {
	sim.ERAMMap
	CommandBuffer renderer.CommandBuffer
}

// BuildClientVideoMaps converts []sim.VideoMap to ClientVideoMaps,
// generating CommandBuffers for the solid-line portion.
func BuildClientVideoMaps(maps []sim.VideoMap) []ClientVideoMap {
	if len(maps) == 0 {
		return nil
	}

	clientMaps := make([]ClientVideoMap, len(maps))
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, m := range maps {
		clientMaps[i] = ClientVideoMap{VideoMap: m}

		ld.Reset()
		hasSolid := false
		for _, line := range m.Lines {
			if line.Style != sim.LineStyleSolid {
				continue // dashed lines drawn separately at draw time
			}
			fl := util.MapSlice(line.Points, func(p math.Point2LL) [2]float32 { return p })
			ld.AddLineStrip(fl)
			hasSolid = true
		}
		if hasSolid {
			ld.GenerateCommands(&clientMaps[i].CommandBuffer)
		}
	}

	return clientMaps
}

// TODO: Eventually combine them
func BuildERAMClientVideoMaps(maps []sim.ERAMMap) []ERAMVideoMap {
	if len(maps) == 0 {
		return nil
	}

	clientMaps := make([]ERAMVideoMap, len(maps))
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, m := range maps {
		clientMaps[i] = ERAMVideoMap{ERAMMap: m}

		ld.Reset()
		hasSolid := false
		for _, line := range m.Lines {
			if line.Style != sim.LineStyleSolid {
				continue // dashed lines drawn separately at draw time
			}
			fl := util.MapSlice(line.Points, func(p math.Point2LL) [2]float32 { return p })
			ld.AddLineStrip(fl)
			hasSolid = true
		}
		if hasSolid {
			ld.GenerateCommands(&clientMaps[i].CommandBuffer)
		}
	}

	return clientMaps
}

// radar/videomaps.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
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
	CommandBuffer renderer.CommandBuffer
}

type ERAMVideoMap struct {
	sim.ERAMMap
	CommandBuffer renderer.CommandBuffer
}

// BuildClientVideoMaps converts []sim.VideoMap to ClientVideoMaps,
// generating CommandBuffers along the way.
func BuildClientVideoMaps(maps []sim.VideoMap) []ClientVideoMap {
	if len(maps) == 0 {
		return nil
	}

	clientMaps := make([]ClientVideoMap, len(maps))
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, m := range maps {
		clientMaps[i] = ClientVideoMap{VideoMap: m}

		if len(m.Lines) > 0 {
			ld.Reset()

			for _, lines := range m.Lines {
				// Convert Point2LL to [2]float32 for the renderer
				fl := util.MapSlice(lines, func(p math.Point2LL) [2]float32 { return p })
				ld.AddLineStrip(fl)
			}

			ld.GenerateCommands(&clientMaps[i].CommandBuffer)

			// Clear Lines after conversion to save memory
			clientMaps[i].Lines = nil
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

		if len(m.Lines) > 0 {
			ld.Reset()

			for _, lines := range m.Lines {
				// Convert Point2LL to [2]float32 for the renderer
				fl := util.MapSlice(lines, func(p math.Point2LL) [2]float32 { return p })
				ld.AddLineStrip(fl)
			}

			ld.GenerateCommands(&clientMaps[i].CommandBuffer)

			// Clear Lines after conversion to save memory
			clientMaps[i].Lines = nil
		}
	}

	return clientMaps
}

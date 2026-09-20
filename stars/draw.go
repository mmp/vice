// stars/draw.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func (sp *Pane) drawWX(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	if !sp.wxNextHistoryStepTime.IsZero() && ctx.InterpolatedSimTime.After(sp.wxNextHistoryStepTime) {
		sp.wxHistoryDraw--
		if sp.wxHistoryDraw > 0 {
			sp.wxNextHistoryStepTime = ctx.InterpolatedSimTime.Add(5 * time.Second)
		} else {
			sp.wxNextHistoryStepTime = sim.Time{}
			if sp.previewAreaOutput == "IN PROGRESS" {
				sp.previewAreaOutput = ""
			}
		}
	}

	weatherBrightness := float32(ps.Brightness.Weather) / float32(100)
	wxStipple := ps.Brightness.WxContrast.ScaleRGB(sp.Colors.WXStipple)
	sp.weatherRadar.Draw(ctx, sp.wxHistoryDraw, weatherBrightness, sp.Colors.WX, wxStipple,
		sp.Colors.WXLevelStipple, ps.DisplayWeatherLevel, transforms, cb)
}

func (sp *Pane) drawTRACONBoundary(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	if !sp.showTRACONBoundary {
		return
	}

	facility, ok := av.DB.LookupFacility(ctx.Client.State.Facility)
	if !ok {
		return
	}

	// Draw the TRACON boundary as a red circle
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	ld.AddLatLongCircle(facility.Center(), ctx.NmPerLongitude, facility.Radius, 360)

	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(renderer.RGB{R: 1, G: 0, B: 0})
	ld.GenerateCommands(cb)
}

func (sp *Pane) drawVideoMaps(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	transforms.LoadLatLongViewingMatrices(cb)

	cb.LineWidth(1, ctx.DPIScale)
	var draw []scope.Map
	for _, vm := range sp.allVideoMaps {
		if _, ok := ps.VideoMapVisible[vm.Id]; ok {
			draw = append(draw, vm)
		}
	}
	slices.SortFunc(draw, func(a, b scope.Map) int { return a.Id - b.Id })

	for _, vm := range draw {
		if vm.Group == 0 {
			cidx := math.Clamp(vm.Color-1, 0, len(sp.Colors.MapA)-1) // switch to 0-based indexing
			color := ps.Brightness.VideoGroupA.ScaleRGB(sp.Colors.MapA[cidx])
			cb.SetRGB(color)
		} else {
			cidx := math.Clamp(vm.Color-1, 0, len(sp.Colors.MapB)-1) // switch to 0-based indexing
			color := ps.Brightness.VideoGroupB.ScaleRGB(sp.Colors.MapB[cidx])
			cb.SetRGB(color)
		}
		cb.Call(vm.CommandBuffer)
	}
}

var restrictionAreaStipple [32]uint32 = [32]uint32{
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
	0b10001000100010001000100010001000,
	0,
	0b00100010001000100010001000100010,
	0,
}

var restrictionAreaHighDPIStipple [32]uint32 = [32]uint32{
	0b11000000110000001100000011000000,
	0b11000000110000001100000011000000,
	0,
	0,
	0b00001100000011000000110000001100,
	0b00001100000011000000110000001100,
	0,
	0,
	0b11000000110000001100000011000000,
	0b11000000110000001100000011000000,
	0,
	0,
	0b00001100000011000000110000001100,
	0b00001100000011000000110000001100,
	0,
	0,
	0b11000000110000001100000011000000,
	0b11000000110000001100000011000000,
	0,
	0,
	0b00001100000011000000110000001100,
	0b00001100000011000000110000001100,
	0,
	0,
	0b11000000110000001100000011000000,
	0b11000000110000001100000011000000,
	0,
	0,
	0b00001100000011000000110000001100,
	0b00001100000011000000110000001100,
	0,
	0,
}

func (sp *Pane) drawWIPRestrictionArea(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ra := sp.wipRestrictionArea
	if ra == nil {
		return
	}
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	var trid *renderer.TrianglesDrawBuilder

	if ra.CircleRadius > 0 {
		if ra.Shaded {
			trid = renderer.GetTrianglesDrawBuilder()
			defer renderer.ReturnTrianglesDrawBuilder(trid)
			trid.AddLatLongCircle(ra.CircleCenter, ctx.NmPerLongitude, ra.CircleRadius, 90)
		}
		ld.AddLatLongCircle(ra.CircleCenter, ctx.NmPerLongitude, ra.CircleRadius, 90)
	} else if len(ra.Vertices) > 0 && len(ra.Vertices[0]) > 0 {
		verts := sp.wipRestrictionArea.Vertices[0]
		for i := range len(verts) - 1 {
			ld.AddLine(verts[i], verts[i+1])
		}

		if ctx.Mouse != nil {
			sp.wipRestrictionAreaMouseMoved = sp.wipRestrictionAreaMouseMoved ||
				(ctx.Mouse.Pos != sp.wipRestrictionAreaMousePos)
			// Only draw the line to the mouse cursor if it has moved since we started entering
			if sp.wipRestrictionAreaMouseMoved && sp.previewAreaInput == "" {
				pm := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
				ld.AddLine(verts[len(verts)-1], pm)
			}
		}
	}

	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ps := sp.currentPrefs()
	color := ps.Brightness.VideoGroupB.ScaleRGB(sp.Colors.MapB[0])
	cb.SetRGB(color)

	ld.GenerateCommands(cb)
	if trid != nil {
		cb.EnablePolygonStipple()
		trid.GenerateCommands(cb)
		cb.DisablePolygonStipple()
	}
}

func (sp *Pane) getRestrictionArea(ctx *scope.Context, idx int, userOnly bool) (av.RestrictionArea, bool) {
	if userOnly && idx > av.MaxRestrictionAreas {
		return av.RestrictionArea{}, false
	}
	ra, ok := ctx.Client.State.RestrictionAreas[idx]
	return ra, ok
}

func (sp *Pane) drawRestrictionAreas(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	sp.drawWIPRestrictionArea(ctx, transforms, cb)

	ps := sp.currentPrefs()
	draw := make(map[int]av.RestrictionArea)
	for idx, s := range ps.RestrictionAreaSettings {
		if !s.Visible {
			continue
		}
		if ra, ok := ctx.Client.State.RestrictionAreas[idx]; ok {
			draw[idx] = ra
		}
	}

	if len(draw) == 0 {
		return
	}

	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	// Draw the geometric bits before the text
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	if ctx.DPIScale > 1.5 {
		cb.PolygonStipple(restrictionAreaHighDPIStipple)
	} else {
		cb.PolygonStipple(restrictionAreaStipple)
	}

	for _, ra := range util.SortedMap(draw) {
		ld.Reset()
		trid.Reset()

		cidx := math.Clamp(ra.Color-1, 0, len(sp.Colors.RestrictionAreaGeom)-1) // 1-based indexing
		color := ps.Brightness.VideoGroupB.ScaleRGB(sp.Colors.RestrictionAreaGeom[cidx])
		cb.SetRGB(color)

		if ra.CircleRadius > 0 {
			if ra.Shaded {
				trid.AddLatLongCircle(ra.CircleCenter, ctx.NmPerLongitude, ra.CircleRadius, 90)
			}
			ld.AddLatLongCircle(ra.CircleCenter, ctx.NmPerLongitude, ra.CircleRadius, 90)
		} else {
			for _, loop := range ra.Vertices {
				if nv := len(loop); nv > 0 {
					for i := range nv - 1 {
						ld.AddLine(loop[i], loop[i+1])
					}
					if ra.Closed {
						ld.AddLine(loop[nv-1], loop[0])
					}
				}
			}
			if ra.Shaded {
				for _, tri := range ra.Tris {
					trid.AddTriangle(tri[0], tri[1], tri[2])
				}
			}
		}
		if ra.Shaded {
			cb.EnablePolygonStipple()
			trid.GenerateCommands(cb)
			cb.DisablePolygonStipple()
		}
		ld.GenerateCommands(cb)
	}

	// Draw text
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	font := sp.systemFont(ctx, ps.CharSize.Lists)
	halfSeconds := time.Now().UnixMilli() / 500
	blinkDim := halfSeconds&1 == 0
	color := ps.Brightness.VideoGroupB.ScaleRGB(sp.Colors.RestrictionAreaText)

	for idx, ra := range util.SortedMap(draw) {
		var text string
		if !ra.HideId {
			text = fmt.Sprintf("[%d]", idx)
		}

		settings := ps.RestrictionAreaSettings[idx]
		if ra.Text[0] != "" && !settings.HideText {
			indent := len(text)
			text += strings.ToUpper(ra.Text[0])
			if ra.Text[1] != "" {
				text += "\n"
				if indent > 0 {
					text += fmt.Sprintf("%*c", indent, ' ')
				}
				text += strings.ToUpper(ra.Text[1])
			}
		}

		p := transforms.WindowFromLatLongP(ra.TextPosition)
		blinking := settings.ForceBlinkingText || (ra.BlinkingText && !settings.StopBlinkingText)
		if blinking && blinkDim {
			td.AddTextCentered(text, p, renderer.TextStyle{Font: font, Color: color.Scale(0.5)})
		} else {
			td.AddTextCentered(text, p, renderer.TextStyle{Font: font, Color: color})
		}
	}
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *Pane) drawCRDARegions(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	transforms.LoadLatLongViewingMatrices(cb)

	ps := sp.currentPrefs()
	drawRegion := func(region *av.CRDARegion, rwyState CRDARunwayState) {
		if rwyState.DrawCourseLines {
			courseLine := region.CourseLine(ctx.NmPerLongitude)
			ld := renderer.GetLinesDrawBuilder()
			cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(sp.Colors.GhostDatablock))
			for k := 0; k+1 < len(courseLine); k++ {
				ld.AddLine(courseLine[k], courseLine[k+1])
			}

			ld.GenerateCommands(cb)
			renderer.ReturnLinesDrawBuilder(ld)
		}

		if rwyState.DrawQualificationRegion {
			poly := region.QualificationPolygon(ctx.NmPerLongitude)

			ld := renderer.GetLinesDrawBuilder()
			cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(sp.Colors.GhostDatablock))
			polyF32 := make([][2]float32, len(poly))
			for k, p := range poly {
				polyF32[k] = p
			}
			ld.AddLineLoop(polyF32)

			ld.GenerateCommands(cb)
			renderer.ReturnLinesDrawBuilder(ld)
		}
	}
	for i, state := range ps.CRDA.RunwayPairState {
		drawRegion(sp.CRDAPairs[i].Source, state.SourceState)
		drawRegion(sp.CRDAPairs[i].Ghost, state.GhostState)
	}
}

func (sp *Pane) drawMouseCursor(ctx *scope.Context, mouseOverDCB bool) {
	if mouseOverDCB {
		// scope/pane.go already called ClearCursorOverride this frame, so
		// the OS will draw imgui's standard arrow.
		return
	}
	ps := sp.currentPrefs()
	if sp.hideMouseCursor { // auto home
		if ctx.Mouse != nil && ctx.Mouse.Pos != ps.CursorHome {
			sp.hideMouseCursor = false // it moved
		}
		if sp.hideMouseCursor {
			imgui.SetMouseCursor(imgui.MouseCursorNone)
			return
		}
	}
	if sp.activeSpinner != nil || sp.commandMode == CommandModePlaceCenter {
		// These both enter the platform's mouseDeltaMode (see
		// drawDCBMouseDeltaButton and drawDCBSpinner), which hides the OS
		// cursor for us. Just leave the override clear.
		return
	}

	// STARS Operators Manual 4-74: FDB brightness is used for the cursor
	fg := ps.Brightness.FullDatablocks.ScaleRGB(sp.Colors.Cursor)
	bg := ps.Brightness.BackgroundContrast.ScaleRGB(sp.Colors.Background)
	if c := sp.crossCursor(ctx.Platform, ps.CharSize.Datablocks, fg, bg); c != nil {
		c.SetOverride()
	}
}

// crossCursor returns the OS cursor for the "+" at the given size, building
// it on first use. The cache is invalidated when fg or bg change.
func (sp *Pane) crossCursor(p platform.Platform, sizeIdx int, fg, bg renderer.RGB) platform.Cursor {
	if fg != sp.crossFg || bg != sp.crossBg {
		for i, c := range sp.crossCursors {
			if c != nil {
				c.Destroy()
			}
			sp.crossCursors[i] = nil
		}
		sp.crossFg, sp.crossBg = fg, bg
	}
	sizeIdx = min(4, sizeIdx)
	if sp.crossCursors[sizeIdx] == nil {
		img, hot := rasterizeStarsCross(sizeIdx, fg, bg)
		c, err := p.CreateCursorFromImage(img, hot[0], hot[1])
		if err != nil {
			return nil
		}
		sp.crossCursors[sizeIdx] = c
	}
	return sp.crossCursors[sizeIdx]
}

// rasterizeStarsCross composites the "+" foreground glyph over its wider
// mask outline. Returns the image and the hotspot (the center of the cross).
// sizeIdx is the user's CharSize.Datablocks value; valid range is [0,4].
func rasterizeStarsCross(sizeIdx int, fg, bg renderer.RGB) (*image.RGBA, [2]int) {
	gi := 2 * min(4, sizeIdx)
	fgG := starsCursors.Glyphs[gi]
	bgG := starsCursors.Glyphs[gi+1] // mask: larger, sets the image size
	W, H := bgG.Bounds[0], bgG.Bounds[1]
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	bgG.Rasterize(img, 0, 0, rgbToRGBA(bg))
	dx := (W - fgG.Bounds[0]) / 2
	dy := (H - fgG.Bounds[1]) / 2
	fgG.Rasterize(img, dx, dy, rgbToRGBA(fg))
	return img, [2]int{W / 2, H / 2}
}

func rgbToRGBA(c renderer.RGB) color.RGBA {
	clamp := func(v float32) uint8 {
		return uint8(math.Clamp(v*255+0.5, 0, 255))
	}
	return color.RGBA{R: clamp(c.R), G: clamp(c.G), B: clamp(c.B), A: 255}
}

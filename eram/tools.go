/*
TODO:
1. Fix mouse to put top box not the bottom box
*/
package eram

import (
	"fmt"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

var commandDrawState struct {
	cb                   *renderer.CommandBuffer
	mouse                *platform.MouseState
	commandBigPosition   [2]float32
	commandSmallPosition [2]float32
}

func (ep *ERAMPane) drawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	// scale := 5 // Change this to the correct size (if this is even needed)
	ep.startDrawCommandInput(ctx, transforms, cb)

	// End draw

	// ps := ep.currentPrefs()
	ep.drawSmallCommandOutput(ctx)
	ep.drawBigCommandInput(ctx)

}

func (ep *ERAMPane) startDrawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	commandDrawState.cb = cb
	commandDrawState.mouse = ctx.Mouse
	ps := ep.currentPrefs()
	commandDrawState.commandBigPosition = ps.commandBigPosition
	commandDrawState.commandSmallPosition = ps.commandSmallPosition

	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMInputFont(),
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}

	if ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func (ep *ERAMPane) drawBigCommandInput(ctx *panes.Context) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()

	sz := [2]float32{390, 77} // fixed output box; MCA.Width only affects wrap cols.
	font := ep.ERAMFont(math.Clamp(ps.MCA.Font, 1, 3))
	cols := math.Clamp(ps.MCA.Width, 1, 200)
	brightFactor := float32(ps.MCA.Bright) / 100

	p0 := ps.commandBigPosition // top-left of the output box
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	e2 := p2
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})
	color := renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})

	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)

	out, _ := util.WrapText(ep.feedbackArea.String(), cols, 0, true, true)
	ep.feedbackArea.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandBigPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.feedbackArea, [2]float32{p0[0] + 2, p0[1] - 2}, font, brightFactor)

	// Draw the smaller top box now.  Size may change if the input text
	// requires more room.
	inputSize := float32(38)
	input := ep.Input.String() + "_"
	inText, _ := util.WrapText(input, cols, 0, true, true)
	_, h := font.BoundText(inText, 0)
	if float32(h)+4 > inputSize {
		inputSize = float32(h) + 4
	}

	p0[1] = ps.commandBigPosition[1] + inputSize
	sz[1] = inputSize
	p1 = math.Add2f(p0, [2]float32{sz[0], 0})
	p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
	e0 := p0
	p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
	color = renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)

	// Input style: white text scaled first by Brightness.Text (preserves the
	// original visual at MCA.Bright == 100) and then by MCA.Bright/100.
	inputColor := ps.Brightness.Text.ScaleRGB(toolbarTextColor).Scale(brightFactor)
	style := renderer.TextStyle{Font: font, Color: inputColor}

	// Input text inside the small box.  Clip to its extent.
	winBase = math.Add2f([2]float32{ps.commandBigPosition[0], ps.commandBigPosition[1] + inputSize}, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - inputSize},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	td.AddText(inText, [2]float32{p0[0] + 2, p0[1] - 2}, style)

	// Restore the scissor to the pane extent
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	extent := math.Extent2DFromPoints([][2]float32{e0, e2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if mouse != nil {
		if mouseInside && !ep.repositionLargeInput && ep.mouseTertiaryClicked(mouse) {
			if _, open := ep.popup.(*mcaPopup); open {
				ep.popup = nil
			} else {
				host := math.Extent2D{P0: [2]float32{e0[0], e2[1]}, P1: [2]float32{e0[0] + sz[0], e0[1]}}
				const w, h = mcaPopupWidth, 5 * 18
				origin := ep.OpenPopupAt(ctx, [2]float32{e0[0] + sz[0], e0[1]}, w, h, ep.ERAMFont(2), host)
				ep.popup = &mcaPopup{origin: origin}
			}
			mouse.Clicked = [platform.MouseButtonCount]bool{}
		} else {
			if (mouseInside && ep.mousePrimaryClicked(mouse)) != ep.repositionLargeInput {
				if !ep.repositionLargeInput {
					ep.timeSinceRepo = time.Now() // only do it on first click
				}
				extent := ctx.PaneExtent
				extent.P1[1] -= 115 // Adjust the extent to the top box
				ctx.Platform.StartCaptureMouse(extent)
				ep.repositionLargeInput = true
				// Draw the outline of the box starting from the cursor as the top left corner.
				sz = [2]float32{390, 115} // Size of the entire command input box
				p0 = mouse.Pos
				p1 = math.Add2f(p0, [2]float32{sz[0], 0})
				p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
				p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
				color = renderer.RGB{1, 1, 1} // White outline. TODO: Check if brightness affects this.
				ld.AddLine(p0, p1, color)
				ld.AddLine(p1, p2, color)
				ld.AddLine(p2, p3, color)
				ld.AddLine(p3, p0, color)

			}
			if (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && ep.repositionLargeInput &&
				time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
				// get the mouse position and set the commandBigPosition to that
				ps.commandBigPosition = mouse.Pos
				ps.commandBigPosition[1] -= 38
				ep.repositionLargeInput = false
				ctx.Platform.EndCaptureMouse()
			}
		}
	}
	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)
}

func (ep *ERAMPane) drawSmallCommandOutput(ctx *panes.Context) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()

	sz := [2]float32{325, 77} // fixed; RA.Width only affects wrap cols.
	font := ep.ERAMFont(math.Clamp(ps.RA.Font, 1, 3))
	cols := math.Clamp(ps.RA.Width, 1, 200)
	brightFactor := float32(ps.RA.Bright) / 100

	p0 := ps.commandSmallPosition
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})
	color := renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})

	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
	// Draw wrapped text output in the box

	out, _ := util.WrapText(ep.responseArea.String(), cols, 0, true, false)
	ep.responseArea.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandSmallPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.responseArea, [2]float32{p0[0] + 2, p0[1] - 2}, font, brightFactor)

	// Restore scissor
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	extent := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if mouse != nil {
		if mouseInside && !ep.repositionResponseArea && ep.mouseTertiaryClicked(mouse) {
			if _, open := ep.popup.(*raPopup); open {
				ep.popup = nil
			} else {
				host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + sz[0], p0[1]}}
				const w, h = raPopupWidth, 5 * 18
				origin := ep.OpenPopupAt(ctx, [2]float32{p0[0] + sz[0], p0[1]}, w, h, ep.ERAMFont(2), host)
				ep.popup = &raPopup{origin: origin}
			}
			mouse.Clicked = [platform.MouseButtonCount]bool{}
		} else {
			if (mouseInside && ep.mousePrimaryClicked(mouse)) != ep.repositionResponseArea {
				if !ep.repositionResponseArea {
					ep.timeSinceRepo = time.Now() // only do it on first click
				}
				extent := ctx.PaneExtent
				extent.P1[1] -= 77 // Adjust the extent to the top box
				ctx.Platform.StartCaptureMouse(extent)
				ep.repositionResponseArea = true
				// Draw the outline of the box starting from the cursor as the top left corner.
				sz = [2]float32{325, 77} // Size of the entire command input box
				p0 = mouse.Pos
				p1 = math.Add2f(p0, [2]float32{sz[0], 0})
				p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
				p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
				color = renderer.RGB{1, 1, 1} // White outline. TODO: Check if brightness affects this.
				ld.AddLine(p0, p1, color)
				ld.AddLine(p1, p2, color)
				ld.AddLine(p2, p3, color)
				ld.AddLine(p3, p0, color)

			}
			if (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && ep.repositionResponseArea &&
				time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
				// get the mouse position and set the commandBigPosition to that
				ps.commandSmallPosition = mouse.Pos
				ep.repositionResponseArea = false
				ctx.Platform.EndCaptureMouse()
			}
		}
	}

	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)
}

// writeText draws inputText one character at a time, preserving per-character
// colors. brightFactor multiplicatively scales each character's stored color
// (1.0 = unchanged, < 1 = dim, > 1 = brighten with OpenGL-side clamping).
func (ep *ERAMPane) writeText(td *renderer.TextDrawBuilder, text inputText, loc [2]float32, font *renderer.Font, brightFactor float32) {
	start0 := loc[0]
	style := renderer.TextStyle{Font: font}
	for _, char := range text {
		ch := char.char
		if ch != '\n' {
			style.Color = char.color.Scale(brightFactor)
			loc = td.AddText(string(ch), loc, style)
		} else {
			loc[0] = start0                             // reset the x position
			loc[1] -= float32(font.Size) * float32(1.4) // edit this value
		}

	}
}

func (ep *ERAMPane) ScaledRGBFromColorPickerRGB(input [3]float32) renderer.RGB {
	ps := ep.currentPrefs()
	return ps.Brightness.Backlight.ScaleRGB(renderer.RGB{input[0], input[1], input[2]})
}

func (ep *ERAMPane) drawScenarioArrivalRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := ep.ScaledRGBFromColorPickerRGB(*ep.IFPHelpers.ArrivalsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.arrivals != nil {
		for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.arrivals[name] == nil {
				continue
			}

			for i, arr := range flow.Arrivals {
				if ep.scopeDraw.arrivals == nil || !ep.scopeDraw.arrivals[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, arr.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

				// Draw runway-specific waypoints
				for rwyWps := range util.SortedMapValues(arr.RunwayWaypoints) {
					for rwy, wp := range util.SortedMap(rwyWps) {
						radar.DrawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

						if len(wp) > 1 {
							// Draw the runway number in the middle of the line
							// between the first two waypoints.
							pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
						} else if wp[0].Heading != 0 {
							// This should be the only other case... The heading arrow is drawn
							// up to 2nm out, so put the runway 1nm along its axis.
							a := math.Radians(math.MagneticToTrue(wp[0].MagneticHeading(), ctx.MagneticVariation))
							v := math.SinCos(a)
							pend := math.LL2NM(wp[0].Location, ctx.NmPerLongitude)
							pend = math.Add2f(pend, v)
							pell := math.NM2LL(pend, ctx.NmPerLongitude)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pell), style)
						}
					}
				}
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioApproachRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := ep.ScaledRGBFromColorPickerRGB(*ep.IFPHelpers.ApproachesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.approaches != nil {
		for _, rwy := range ctx.Client.State.ArrivalRunways {
			if ep.scopeDraw.approaches[rwy.Airport] == nil {
				continue
			}
			ap := ctx.Client.State.Airports[rwy.Airport]
			for name, appr := range util.SortedMap(ap.Approaches) {
				if appr.Runway == rwy.Runway.Base() && ep.scopeDraw.approaches[rwy.Airport][name] {
					for _, wp := range appr.Waypoints {
						radar.DrawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)
					}
				}
			}
		}
	}

	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioDepartureRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := ep.ScaledRGBFromColorPickerRGB(*ep.IFPHelpers.DeparturesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.departures != nil {
		for name, ap := range util.SortedMap(ctx.Client.State.Airports) {
			if ep.scopeDraw.departures[name] == nil {
				continue
			}

			for rwy, exitRoutes := range util.SortedMap(ap.DepartureRoutes) {
				if ep.scopeDraw.departures[name][string(rwy)] == nil {
					continue
				}

				for exit, exitRoute := range util.SortedMap(exitRoutes) {
					if ep.scopeDraw.departures[name][string(rwy)][string(exit)] {
						radar.DrawWaypoints(ctx, exitRoute.Waypoints, drawnWaypoints, transforms,
							td, style, ld, pd, ldr, color)
					}
				}
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioOverflightRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := ep.ScaledRGBFromColorPickerRGB(*ep.IFPHelpers.OverflightsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.overflights != nil {
		for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.overflights[name] == nil {
				continue
			}

			for i, of := range flow.Overflights {
				if ep.scopeDraw.overflights == nil || !ep.scopeDraw.overflights[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, of.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioAirspaceRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := ep.ScaledRGBFromColorPickerRGB(*ep.IFPHelpers.AirspaceColor)
	style := renderer.TextStyle{
		Font:           ep.systemFont[3],
		Color:          color,
		DrawBackground: true, // default BackgroundColor is fine
	}

	if ep.scopeDraw.airspace != nil {
		for ctrl, vols := range util.SortedMap(ep.scopeDraw.airspace) {
			for volname, enabled := range util.SortedMap(vols) {
				if !enabled {
					continue
				}

				for _, vol := range ctx.Client.State.Airspace[ctrl][volname] {
					for _, pts := range vol.Boundaries {
						if len(pts) < 2 {
							continue
						}
						for i := range pts[:len(pts)-1] {
							ld.AddLine(pts[i], pts[i+1], color)
						}
					}
					label := strings.ToUpper(vol.Label)
					td.AddTextCentered(label, transforms.WindowFromLatLongP(vol.LabelPosition), style)
				}
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font, cb *renderer.CommandBuffer) {
	if len(ep.scopeDraw.arrivals) == 0 && len(ep.scopeDraw.approaches) == 0 && len(ep.scopeDraw.departures) == 0 &&
		len(ep.scopeDraw.overflights) == 0 && len(ep.scopeDraw.airspace) == 0 {
		return
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	pd := renderer.GetColoredTrianglesDrawBuilder() // for circles
	defer renderer.ReturnColoredTrianglesDrawBuilder(pd)
	ldr := renderer.GetColoredLinesDrawBuilder() // for restrictions--in window coords...
	defer renderer.ReturnColoredLinesDrawBuilder(ldr)

	// Track which waypoints have been drawn so that we don't repeatedly
	// draw the same one.  (This is especially important since the
	// placement of the labels depends on the inbound/outbound segments,
	// which may be different for different uses of the waypoint...)
	drawnWaypoints := make(map[string]interface{})

	ep.drawScenarioArrivalRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	ep.drawScenarioApproachRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	ep.drawScenarioDepartureRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	ep.drawScenarioOverflightRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	ep.drawScenarioAirspaceRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
}

func (ep *ERAMPane) drawPlotPoints(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if len(ep.drawRoutePoints) == 0 {
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, pt := range ep.drawRoutePoints {
		pwin := transforms.WindowFromLatLongP(pt)
		ld.AddCircle(pwin, 10, 30)
		if i+1 < len(ep.drawRoutePoints) {
			ld.AddLine(pwin, transforms.WindowFromLatLongP(ep.drawRoutePoints[i+1]))
		}
	}
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(renderer.RGB{1, .3, .3})
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (ep *ERAMPane) drawClock(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)

	ps := ep.currentPrefs()

	if ps.clockPosition == [2]float32{} {
		ps.clockPosition = [2]float32{10, ctx.PaneExtent.Height() - 300}
	}

	fontNum := math.Clamp(ps.TimeView.Font, 1, 3)
	font := ep.ERAMFont(fontNum)
	bright := radar.Brightness(ps.TimeView.Bright)
	textColor := bright.ScaleRGB(renderer.RGB{1, 1, 1})

	simTime := ctx.Client.State.SimTime
	timeStr := simTime.Format("1504 05")

	// Box sized from the cell-metric bounds with a 4px margin. BoundText
	// reports the cell extent (including font padding and the trailing
	// advance past the last glyph), so the visible glyphs will sit slightly
	// off-center inside the box; that's a font-metric quirk to revisit
	// alongside BoundText itself.
	tw, th := font.BoundText(timeStr, 0)
	horizontalPxLength := float32(tw) + 8
	verticalPxLength := float32(th) + 8

	p0 := ps.clockPosition
	p1 := math.Add2f(p0, [2]float32{horizontalPxLength, 0})
	p2 := math.Add2f(p1, [2]float32{0, -verticalPxLength})
	p3 := math.Add2f(p2, [2]float32{-horizontalPxLength, 0})

	if ps.TimeView.Opaque {
		bgColor := bright.ScaleRGB(renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0})
		trid.AddQuad(p0, p1, p2, p3, bgColor)
	}

	if ps.TimeView.ShowBorder {
		cb.LineWidth(.3, ctx.DPIScale)
		ld.AddLine(p0, p1, textColor)
		ld.AddLine(p1, p2, textColor)
		ld.AddLine(p2, p3, textColor)
		ld.AddLine(p3, p0, textColor)
		cb.LineWidth(1, ctx.DPIScale)
	}

	center := [2]float32{p0[0] + horizontalPxLength/2, p0[1] - verticalPxLength/2}
	td.AddTextCentered(timeStr, center, renderer.TextStyle{Font: font, Color: textColor})

	// check if the clock is clicked on for repos
	extent := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)

	if mouseInside && !ep.repositionClock && ep.mouseTertiaryClicked(mouse) {
		if _, open := ep.popup.(*timeViewPopup); open {
			ep.popup = nil
		} else {
			host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + horizontalPxLength, p0[1]}}
			const w, h = timeViewPopupWidth, 5 * 18
			origin := ep.OpenPopupAt(ctx, math.Add2f(p0, [2]float32{horizontalPxLength, 0}), w, h, ep.ERAMFont(2), host)
			ep.popup = &timeViewPopup{origin: origin}
		}
		mouse.Clicked = [platform.MouseButtonCount]bool{}
	} else if (mouseInside && ep.mousePrimaryClicked(mouse)) != ep.repositionClock {
		if !ep.repositionClock {
			ep.timeSinceRepo = time.Now()
		}
		extent := ctx.PaneExtent
		extent.P1[1] -= verticalPxLength
		ctx.Platform.StartCaptureMouse(extent)
		ep.repositionClock = true

		sz := [2]float32{horizontalPxLength, verticalPxLength}
		if mouse != nil {
			p0 = mouse.Pos
			p1 = math.Add2f(p0, [2]float32{sz[0], 0})
			p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
			p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
			color := renderer.RGB{1, 1, 1} // White outline. TODO: Check if brightness affects this.
			ld.AddLine(p0, p1, color)
			ld.AddLine(p1, p2, color)
			ld.AddLine(p2, p3, color)
			ld.AddLine(p3, p0, color)

			if (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && ep.repositionClock &&
				time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
				ps.clockPosition = mouse.Pos
				ep.repositionClock = false
				ctx.Platform.EndCaptureMouse()
			}
		}
	}

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

const mcaPopupWidth = 150

// mcaPopup is the configuration menu for the Message Composition Area.
type mcaPopup struct {
	origin [2]float32
}

func (m *mcaPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		{Label: fmt.Sprintf("PA LINES %d", ps.MCA.PALines), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.PALines, 1, 50, 1)
			return false
		}},
		{Label: fmt.Sprintf("WIDTH %d", ps.MCA.Width), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.Width, 10, 200, 1)
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.MCA.Font), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.MCA.Bright), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.Bright, 0, 100, 1)
			return false
		}},
	}

	cfg := ERAMMenuConfig{
		Title: "MCA",
		Width: mcaPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, m.origin, cfg)
}

const raPopupWidth = 150

// raPopup is the configuration menu for the Response Area.
type raPopup struct {
	origin [2]float32
}

func (r *raPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		{Label: fmt.Sprintf("WIDTH %d", ps.RA.Width), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Width, 10, 200, 1)
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.RA.Font), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.RA.Bright), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Bright, 0, 100, 1)
			return false
		}},
		{Label: "CLEAR", BgColor: popupBlackBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			ep.responseArea.Clear()
			return false
		}},
	}

	cfg := ERAMMenuConfig{
		Title: "RA",
		Width: raPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, r.origin, cfg)
}

const timeViewPopupWidth = 120

// timeViewPopup is the configuration menu for the Time View (clock).
type timeViewPopup struct {
	origin [2]float32
}

func (t *timeViewPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	tLabel := "T"
	tBg := popupBlackBg
	if ps.TimeView.Opaque {
		tLabel = "O"
		tBg = popupGreyBg
	}
	borderBg := util.Select(ps.TimeView.ShowBorder, popupGreyBg, popupBlackBg)

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: popupTextColor, Centered: true, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.TimeView.Opaque = !ps.TimeView.Opaque
			}
			return false
		}},
		{Label: "BORDER", BgColor: borderBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.TimeView.ShowBorder = !ps.TimeView.ShowBorder
			}
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.TimeView.Font), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.TimeView.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.TimeView.Bright), BgColor: popupGreenBg, Color: popupTextColor, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.TimeView.Bright, 0, 100, 1)
			return false
		}},
	}

	cfg := ERAMMenuConfig{
		Title: "TIME",
		Width: timeViewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, t.origin, cfg)
}

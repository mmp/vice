/*
TODO:
1. Fix mouse to put top box not the bottom box
*/
package eram

import (
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

	// For extent2D, save the top left of the input and bottem right of the output

	ps := ep.currentPrefs()
	sz := [2]float32{390, 77}
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
	// Draw wrapped output text in the big box
	style := renderer.TextStyle{
		Font:  ep.ERAMInputFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	out, _ := util.WrapText(ep.bigOutput.String(), cols, 0, true, true)
	ep.bigOutput.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandBigPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.bigOutput, [2]float32{p0[0] + 2, p0[1] - 2})

	// Draw the smaller top box now.  Size may change if the input text
	// requires more room.
	inputSize := float32(38)
	bx, _ = style.Font.BoundText("X", 0)
	cols = int(sz[0] / float32(bx))
	input := ep.Input.String() + "_"
	inText, _ := util.WrapText(input, cols, 0, true, true)
	_, h := style.Font.BoundText(inText, style.LineSpacing)
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
	sz := [2]float32{325, 77}
	style := renderer.TextStyle{
		Font:  ep.ERAMInputFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	inputSize := float32(77)
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	inText, _ := util.WrapText(ep.smallOutput.String(), cols, 0, true, false)
	_, h := style.Font.BoundText(inText, style.LineSpacing)
	if float32(h)+4 > inputSize {
		inputSize = float32(h) + 4
	}
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

	cols = int(sz[0] / float32(bx))
	out, _ := util.WrapText(ep.smallOutput.String(), cols, 0, true, false)
	ep.smallOutput.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandSmallPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.smallOutput, [2]float32{p0[0] + 2, p0[1] - 2})

	// Restore scissor
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	extent := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if mouse != nil {
		if (mouseInside && ep.mousePrimaryClicked(mouse)) != ep.repositionSmallOutput {
			if !ep.repositionSmallOutput {
				ep.timeSinceRepo = time.Now() // only do it on first click
			}
			extent := ctx.PaneExtent
			extent.P1[1] -= 77 // Adjust the extent to the top box
			ctx.Platform.StartCaptureMouse(extent)
			ep.repositionSmallOutput = true
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
		if (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && ep.repositionSmallOutput &&
			time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
			// get the mouse position and set the commandBigPosition to that
			ps.commandSmallPosition = mouse.Pos
			ep.repositionSmallOutput = false
			ctx.Platform.EndCaptureMouse()
		}
	}

	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)
}

func (ep *ERAMPane) writeText(td *renderer.TextDrawBuilder, text inputText, loc [2]float32) {
	start0 := loc[0]
	font := ep.ERAMInputFont()
	style := renderer.TextStyle{Font: font}
	for _, char := range text {
		ch := char.char
		if ch != '\n' {
			style.Color = char.color
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
		for _, name := range util.SortedMapKeys(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.arrivals[name] == nil {
				continue
			}

			arrivals := ctx.Client.State.InboundFlows[name].Arrivals
			for i, arr := range arrivals {
				if ep.scopeDraw.arrivals == nil || !ep.scopeDraw.arrivals[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, arr.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

				// Draw runway-specific waypoints
				for _, ap := range util.SortedMapKeys(arr.RunwayWaypoints) {
					for _, rwy := range util.SortedMapKeys(arr.RunwayWaypoints[ap]) {
						wp := arr.RunwayWaypoints[ap][rwy]
						radar.DrawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

						if len(wp) > 1 {
							// Draw the runway number in the middle of the line
							// between the first two waypoints.
							pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
						} else if wp[0].Heading != 0 {
							// This should be the only other case... The heading arrow is drawn
							// up to 2nm out, so put the runway 1nm along its axis.
							a := math.Radians(float32(wp[0].Heading) - ctx.MagneticVariation)
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
			for _, name := range util.SortedMapKeys(ap.Approaches) {
				appr := ap.Approaches[name]
				if appr.Runway == rwy.Runway && ep.scopeDraw.approaches[rwy.Airport][name] {
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
		for _, name := range util.SortedMapKeys(ctx.Client.State.Airports) {
			if ep.scopeDraw.departures[name] == nil {
				continue
			}

			ap := ctx.Client.State.Airports[name]
			for _, rwy := range util.SortedMapKeys(ap.DepartureRoutes) {
				if ep.scopeDraw.departures[name][rwy] == nil {
					continue
				}

				exitRoutes := ap.DepartureRoutes[rwy]
				for _, exit := range util.SortedMapKeys(exitRoutes) {
					if ep.scopeDraw.departures[name][rwy][exit] {
						radar.DrawWaypoints(ctx, exitRoutes[exit].Waypoints, drawnWaypoints, transforms,
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
		for _, name := range util.SortedMapKeys(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.overflights[name] == nil {
				continue
			}

			overflights := ctx.Client.State.InboundFlows[name].Overflights
			for i, of := range overflights {
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
		for _, ctrl := range util.SortedMapKeys(ep.scopeDraw.airspace) {
			for _, volname := range util.SortedMapKeys(ep.scopeDraw.airspace[ctrl]) {
				if !ep.scopeDraw.airspace[ctrl][volname] {
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

	horizontalPxLength := float32(120)
	verticalPxLength := float32(40)
	ps := ep.currentPrefs()

	if ps.clockPosition == [2]float32{} {
		ps.clockPosition = [2]float32{10, ctx.PaneExtent.Height() - 300}
	}

	p0 := ps.clockPosition
	p1 := math.Add2f(p0, [2]float32{horizontalPxLength, 0})
	p2 := math.Add2f(p1, [2]float32{0, -verticalPxLength})
	p3 := math.Add2f(p2, [2]float32{-horizontalPxLength, 0})
	p4 := math.Add2f(p3, [2]float32{0, verticalPxLength})

	cb.LineWidth(.3, ctx.DPIScale)
	ld.AddLine(p0, p1, renderer.RGB{1, 1, 1})
	ld.AddLine(p1, p2, renderer.RGB{1, 1, 1})
	ld.AddLine(p2, p3, renderer.RGB{1, 1, 1})
	ld.AddLine(p3, p4, renderer.RGB{1, 1, 1})
	cb.LineWidth(1, ctx.DPIScale)

	verticalOffset := float32(3)
	center := [2]float32{p0[0] + horizontalPxLength/2, p0[1] - verticalPxLength/2 + verticalOffset}

	simTime := ctx.Client.State.SimTime
	timeStr := simTime.Format("1504 05")

	td.AddTextCentered(timeStr, center, renderer.TextStyle{Font: ep.ERAMFont(3), Color: renderer.RGB{1, 1, 1}})

	// check if the clock is clicked on for repos
	extent := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if (mouseInside && ep.mousePrimaryClicked(mouse)) != ep.repositionClock {
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

	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

/*
TODO:
1. Fix mouse to put top box not the bottom box
*/
package eram

import (
	"fmt"
	"strings"

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
	ep.startDrawCommandInput(ctx, transforms, cb)
	ep.drawSmallCommandOutput(ctx, transforms, cb)
	ep.drawBigCommandInput(ctx, transforms, cb)
}

func (ep *ERAMPane) startDrawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	commandDrawState.cb = cb
	commandDrawState.mouse = ctx.Mouse
	ps := ep.currentPrefs()
	commandDrawState.commandBigPosition = ps.commandBigPosition
	commandDrawState.commandSmallPosition = ps.commandSmallPosition

	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMInputFont(),
		Color:       colors.toolbar.text,
		LineSpacing: 0,
	}

	if ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

// drawBigCommandInput renders the MCA: a feedback box (fixed 390×77) below an
// input box (390×inputSize, where inputSize grows beyond 38px if wrapped input
// exceeds it). Both boxes share black bg and a white border; the seam between
// them is drawn by View as part of the outer border (and a separator line in
// the body).
func (ep *ERAMPane) drawBigCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	const feedbackH = float32(77)
	const width = float32(390)
	font := ep.ERAMFont(math.Clamp(ps.MCA.Font, 1, 3))
	cols := math.Clamp(ps.MCA.Width, 1, 200)
	brightFactor := float32(ps.MCA.Bright) / 100

	// Compute input box height (grows with wrapped text).
	input := ep.Input.String() + "_"
	inText, _ := util.WrapText(input, cols, 0, true, true)
	h := font.LayoutBounds(inText, 0).Height()
	inputH := float32(38)
	if h+4 > inputH {
		inputH = h + 4
	}

	out, _ := util.WrapText(ep.feedbackArea.String(), cols, 0, true, true)
	ep.feedbackArea.formatWrap(ps, out)

	// ps.commandBigPosition is the top-left of the feedback box (prefs
	// semantics). View sees the top-left of the whole envelope (input top).
	viewPos := [2]float32{ps.commandBigPosition[0], ps.commandBigPosition[1] + inputH}

	v := View{
		Position:   &viewPos,
		Reposition: &ep.mcaRepo,
		Width:      width,
		BodyHeight: inputH + feedbackH,
		ShowBorder: true,
		Brightness: ps.Brightness.Border,
		OnBodyTertiaryMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*mcaPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				mcaPopupWidth, 5*18, ep.ERAMFont(2), host)
			return &mcaPopup{origin: origin}
		},
		Body: func(body math.Extent2D, b *ViewBuilders) {
			// body.P1 = top-right (top of input); body.P0 = bottom-left (bottom of feedback).
			seamY := body.P1[1] - inputH

			// Seam line between input and feedback.
			borderColor := ps.Brightness.Border.ScaleRGB(colors.view.border)
			b.Ld.AddLine([2]float32{body.P0[0], seamY}, [2]float32{body.P1[0], seamY}, borderColor)

			dpi := ctx.Platform.FramebufferSize()[1] / ctx.Platform.DisplaySize()[1]
			paneOrigin := ctx.PaneExtent.P0
			inputTopLeft := [2]float32{body.P0[0], body.P1[1]}
			feedbackTopLeft := [2]float32{body.P0[0], seamY}

			// Input text (top box).
			winBase := math.Add2f(inputTopLeft, paneOrigin)
			b.CB.SetScissorBounds(math.Extent2D{
				P0: [2]float32{winBase[0], winBase[1] - inputH},
				P1: [2]float32{winBase[0] + width, winBase[1]},
			}, dpi)
			inputColor := ps.Brightness.Text.ScaleRGB(colors.toolbar.text).Scale(brightFactor)
			b.Td.AddText(inText, [2]float32{inputTopLeft[0] + 2, inputTopLeft[1] - 2},
				renderer.TextStyle{Font: font, Color: inputColor})

			// Feedback text (bottom box).
			winBase = math.Add2f(feedbackTopLeft, paneOrigin)
			b.CB.SetScissorBounds(math.Extent2D{
				P0: [2]float32{winBase[0], winBase[1] - feedbackH},
				P1: [2]float32{winBase[0] + width, winBase[1]},
			}, dpi)
			ep.writeText(b.Td, ep.feedbackArea, [2]float32{feedbackTopLeft[0] + 2, feedbackTopLeft[1] - 2}, font, brightFactor)

			b.CB.SetScissorBounds(ctx.PaneExtent, dpi)
		},
	}
	ep.DrawView(ctx, transforms, cb, v)

	// View may have updated viewPos via drag; translate back to prefs semantics.
	ps.commandBigPosition = [2]float32{viewPos[0], viewPos[1] - inputH}
}

// drawSmallCommandOutput renders the RA: a single 325×77 box with the wrapped
// response-area text.
func (ep *ERAMPane) drawSmallCommandOutput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	const width = float32(325)
	const height = float32(77)
	font := ep.ERAMFont(math.Clamp(ps.RA.Font, 1, 3))
	cols := math.Clamp(ps.RA.Width, 1, 200)
	brightFactor := float32(ps.RA.Bright) / 100

	out, _ := util.WrapText(ep.responseArea.String(), cols, 0, true, false)
	ep.responseArea.formatWrap(ps, out)

	v := View{
		Position:   &ps.commandSmallPosition,
		Reposition: &ep.raRepo,
		Width:      width,
		BodyHeight: height,
		ShowBorder: true,
		Brightness: radar.Brightness(ps.RA.Bright),
		OnBodyTertiaryMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*raPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				raPopupWidth, 5*18, ep.ERAMFont(2), host)
			return &raPopup{origin: origin}
		},
		Body: func(body math.Extent2D, b *ViewBuilders) {
			dpi := ctx.Platform.FramebufferSize()[1] / ctx.Platform.DisplaySize()[1]
			topLeft := [2]float32{body.P0[0], body.P1[1]}
			winBase := math.Add2f(topLeft, ctx.PaneExtent.P0)
			b.CB.SetScissorBounds(math.Extent2D{
				P0: [2]float32{winBase[0], winBase[1] - height},
				P1: [2]float32{winBase[0] + width, winBase[1]},
			}, dpi)
			ep.writeText(b.Td, ep.responseArea, [2]float32{topLeft[0] + 2, topLeft[1] - 2}, font, brightFactor)
			b.CB.SetScissorBounds(ctx.PaneExtent, dpi)
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
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
	return ps.Brightness.Backlight.ScaleRGB(renderer.RGB{R: input[0], G: input[1], B: input[2]})
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
	cb.SetRGB(colors.drawRoute)
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (ep *ERAMPane) drawClock(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	if ps.clockPosition == [2]float32{} {
		ps.clockPosition = [2]float32{10, ctx.PaneExtent.Height() - 300}
	}

	fontNum := math.Clamp(ps.TimeView.Font, 1, 3)
	font := ep.ERAMFont(fontNum)
	bright := radar.Brightness(ps.TimeView.Bright)
	textColor := bright.ScaleRGB(colors.view.clockText)

	timeStr := ctx.Client.State.SimTime.Format("1504 05")

	ext := font.InkBounds(timeStr, 0)
	width := ext.Width() + 8
	height := ext.Height() + 8

	v := View{
		Position:     &ps.clockPosition,
		Reposition:   &ep.clockRepo,
		Width:        width,
		BodyHeight:   height,
		ShowBorder:   ps.TimeView.ShowBorder,
		Opaque:       ps.TimeView.Opaque,
		Brightness:   bright,
		OpaqueOnlyBg: true,
		OnBodyTertiaryMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*timeViewPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				timeViewPopupWidth, 5*18, ep.ERAMFont(2), host)
			return &timeViewPopup{origin: origin}
		},
		Body: func(body math.Extent2D, b *ViewBuilders) {
			center := [2]float32{(body.P0[0] + body.P1[0]) / 2, (body.P0[1] + body.P1[1]) / 2}
			b.Td.AddTextCentered(timeStr, center, renderer.TextStyle{Font: font, Color: textColor})
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

const mcaPopupWidth = 150

// mcaPopup is the configuration menu for the Message Composition Area.
type mcaPopup struct {
	origin [2]float32
}

func (m *mcaPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		{Label: fmt.Sprintf("PA LINES %d", ps.MCA.PALines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.PALines, 1, 50, 1)
			return false
		}},
		{Label: fmt.Sprintf("WIDTH %d", ps.MCA.Width), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.Width, 10, 200, 1)
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.MCA.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.MCA.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.MCA.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
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
		{Label: fmt.Sprintf("WIDTH %d", ps.RA.Width), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Width, 10, 200, 1)
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.RA.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.RA.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.RA.Bright, 0, 100, 1)
			return false
		}},
		{Label: "CLEAR", BgColor: colors.popup.backgroundBlack, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
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
	tBg := colors.popup.backgroundBlack
	if ps.TimeView.Opaque {
		tLabel = "O"
		tBg = colors.popup.backgroundGrey
	}
	borderBg := util.Select(ps.TimeView.ShowBorder, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: colors.popup.text, Centered: true, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.TimeView.Opaque = !ps.TimeView.Opaque
			}
			return false
		}},
		{Label: "BORDER", BgColor: borderBg, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.TimeView.ShowBorder = !ps.TimeView.ShowBorder
			}
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.TimeView.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.TimeView.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.TimeView.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
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

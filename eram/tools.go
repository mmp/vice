/*
TODO:
1. Fix mouse to put top box not the bottom box
*/
package eram

import (
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

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

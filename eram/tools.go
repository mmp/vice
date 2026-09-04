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

func (ep *ERAMPane) drawScenarioArrivalRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawn *radar.DrawnRoutes, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := renderer.RGBFromArray(*ep.IFPHelpers.ArrivalsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.Arrivals != nil {
		for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.Arrivals[name] == nil {
				continue
			}

			for i, arr := range flow.Arrivals {
				if ep.scopeDraw.Arrivals == nil || !ep.scopeDraw.Arrivals[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, arr.Waypoints, radar.ArrivalRouteContext(arr), drawn, transforms, td, style, ld, pd, ldr, color)

				// Draw runway-specific waypoints
				for rwyWps := range util.SortedMapValues(arr.RunwayWaypoints) {
					for rwy, wp := range util.SortedMap(rwyWps) {
						radar.DrawWaypoints(ctx, wp, radar.ArrivalRouteContext(arr), drawn, transforms, td, style, ld, pd, ldr, color)

						if len(wp) > 1 {
							// Draw the runway number in the middle of the line
							// between the first two waypoints.
							pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
						} else if h, ok := wp[0].HeadingAction(); ok {
							// This should be the only other case... The heading arrow is drawn
							// up to 3nm out, so put the runway 1nm along its axis.
							a := math.Radians(math.MagneticToTrue(math.MagneticHeading(h.Heading), ctx.MagneticVariation))
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
	cb *renderer.CommandBuffer, drawn *radar.DrawnRoutes, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := renderer.RGBFromArray(*ep.IFPHelpers.ApproachesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.Approaches != nil {
		for _, rwy := range ctx.Client.State.ArrivalRunways {
			if ep.scopeDraw.Approaches[rwy.Airport] == nil {
				continue
			}
			ap := ctx.Client.State.Airports[rwy.Airport]
			for name, appr := range util.SortedMap(ap.Approaches) {
				if appr.Runway == rwy.Runway.Base() && ep.scopeDraw.Approaches[rwy.Airport][name] {
					for _, wp := range appr.Waypoints {
						radar.DrawWaypoints(ctx, wp, radar.ApproachRouteContext(appr), drawn, transforms, td, style, ld, pd, ldr, color)
					}
				}
			}
		}
	}

	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioDepartureRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawn *radar.DrawnRoutes, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := renderer.RGBFromArray(*ep.IFPHelpers.DeparturesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	for icao, rates := range util.SortedMap(ctx.Client.State.LaunchConfig.DepartureRates) {
		if ep.scopeDraw.Departures[icao] == nil {
			continue
		}
		for dr := range radar.ScenarioDepartureRoutes(ctx.Client.State.Airports[icao], rates) {
			if !ep.scopeDraw.Departures[icao][dr.Group] {
				continue
			}
			radar.DrawWaypoints(ctx, dr.Route.Waypoints, radar.DepartureRouteContext(icao, dr.Route), drawn, transforms,
				td, style, ld, pd, ldr, color)
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioOverflightRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawn *radar.DrawnRoutes, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := renderer.RGBFromArray(*ep.IFPHelpers.OverflightsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if ep.scopeDraw.Overflights != nil {
		for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
			if ep.scopeDraw.Overflights[name] == nil {
				continue
			}

			for i, of := range flow.Overflights {
				if ep.scopeDraw.Overflights == nil || !ep.scopeDraw.Overflights[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, of.Waypoints, radar.OverflightRouteContext(of), drawn, transforms, td, style, ld, pd, ldr, color)
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (ep *ERAMPane) drawScenarioAirspaceRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawn *radar.DrawnRoutes, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := renderer.RGBFromArray(*ep.IFPHelpers.AirspaceColor)
	style := renderer.TextStyle{
		Font:           ep.systemFont[3],
		Color:          color,
		DrawBackground: true, // default BackgroundColor is fine
	}

	if ep.scopeDraw.Airspace != nil {
		for ctrl, vols := range util.SortedMap(ep.scopeDraw.Airspace) {
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
	if ep.scopeDraw.Empty() {
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
	// draw the same one, and what has been labeled so that routes sharing
	// legs stack their labels rather than drawing them over each other.
	drawn := radar.NewDrawnRoutes()

	ep.drawScenarioArrivalRoutes(ctx, transforms, font, cb, drawn, td, ld, pd, ldr)
	ep.drawScenarioApproachRoutes(ctx, transforms, font, cb, drawn, td, ld, pd, ldr)
	ep.drawScenarioDepartureRoutes(ctx, transforms, font, cb, drawn, td, ld, pd, ldr)
	ep.drawScenarioOverflightRoutes(ctx, transforms, font, cb, drawn, td, ld, pd, ldr)
	ep.drawScenarioAirspaceRoutes(ctx, transforms, font, cb, drawn, td, ld, pd, ldr)
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

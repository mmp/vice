// pkg/panes/stars/tools.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	_ "embed"
	"fmt"
	gomath "math"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// Additional useful things we may draw on radar scopes...

// DrawCompass emits drawing commands to draw compass heading directions at
// the edges of the current window. It takes a center point p in lat-long
// coordinates, transformation functions and the radar scope's current
// rotation angle, if any.  Drawing commands are added to the provided
// command buffer, which is assumed to have projection matrices set up for
// drawing using window coordinates.
func (sp *STARSPane) drawCompass(ctx *panes.Context, scopeExtent math.Extent2D, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.Compass == 0 {
		return
	}

	// Window coordinates of the center point.
	// TODO: should we explicitly handle the case of this being outside the window?
	ctr := util.Select(ps.UseUserCenter, ps.UserCenter, ps.DefaultCenter)
	pw := transforms.WindowFromLatLongP(ctr)
	bounds := math.Extent2D{P1: [2]float32{scopeExtent.Width(), scopeExtent.Height()}}
	font := sp.systemFont(ctx, ps.CharSize.Tools)
	color := ps.Brightness.Compass.ScaleRGB(STARSCompassColor)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	// Draw lines at a 5 degree spacing.
	for h := float32(5); h <= 360; h += 5 {
		hr := h
		dir := math.SinCos(math.Radians(hr))
		// Find the intersection of the line from the center point to the edge of the window.
		isect, _, t := bounds.IntersectRay(pw, dir)
		if !isect {
			// Happens on initial launch w/o a sector file...
			//lg.Infof("no isect?! p %+v dir %+v bounds %+v", pw, dir, ctx.paneExtent)
			continue
		}

		// Draw a short line from the intersection point at the edge to the
		// point ten pixels back inside the window toward the center.
		pEdge := math.Add2f(pw, math.Scale2f(dir, t))
		pInset := math.Add2f(pw, math.Scale2f(dir, t-10))
		ld.AddLine(pEdge, pInset)

		// Every 10 degrees draw a heading label.
		if int(h)%10 == 0 {
			// Generate the label ourselves rather than via fmt.Sprintf,
			// out of some probably irrelevant attempt at efficiency.
			label := []byte{'0', '0', '0'}
			hi := int(h)
			for i := 2; i >= 0 && hi != 0; i-- {
				label[i] = byte('0' + hi%10)
				hi /= 10
			}

			bx, by := font.BoundText(string(label), 0)

			// Initial inset to place the text--a little past the end of
			// the line.
			pText := math.Add2f(pw, math.Scale2f(dir, t-14))

			// Finer text positioning depends on which edge of the window
			// pane we're on; this is made more grungy because text drawing
			// is specified w.r.t. the position of the upper-left corner...
			if math.Abs(pEdge[0]) < .125 {
				// left edge
				pText[1] += float32(by) / 2
			} else if math.Abs(pEdge[0]-bounds.P1[0]) < .125 {
				// right edge
				pText[0] -= float32(bx)
				pText[1] += float32(by) / 2
			} else if math.Abs(pEdge[1]) < .125 {
				// bottom edge
				pText[0] -= float32(bx) / 2
				pText[1] += float32(by)
			} else if math.Abs(pEdge[1]-bounds.P1[1]) < .125 {
				// top edge
				pText[0] -= float32(bx) / 2
			} else {
				ctx.Lg.Infof("Edge borkage! pEdge %+v, bounds %+v", pEdge, bounds)
			}

			td.AddText(string(label), pText, renderer.TextStyle{Font: font, Color: color})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(color)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// DrawRangeRings draws ten circles around the specified lat-long point in
// steps of the specified radius (in nm).
func (sp *STARSPane) drawRangeRings(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.RangeRings == 0 {
		return
	}

	pixelDistanceNm := transforms.PixelDistanceNM(ctx.NmPerLongitude)
	ctr := util.Select(ps.UseUserRangeRingsCenter, ps.RangeRingsUserCenter, ps.DefaultCenter)
	centerWindow := transforms.WindowFromLatLongP(ctr)

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i := 1; i < 40; i++ {
		// Radius of this ring in pixels
		r := float32(i) * float32(ps.RangeRingRadius) / pixelDistanceNm
		ld.AddCircle(centerWindow, r, 360)
	}

	cb.LineWidth(1, ctx.DPIScale)
	color := ps.Brightness.RangeRings.ScaleRGB(STARSRangeRingColor)
	cb.SetRGB(color)
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// Other utilities

// If distance to a significant point is being displayed or if the user has
// run the "find" command to highlight a point in the world, draw a blinking
// square at that point for a few seconds.
func (sp *STARSPane) drawHighlighted(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	remaining := time.Until(sp.highlightedLocationEndTime)
	if remaining < 0 {
		return
	}

	// "The color of the blinking square is the same as that for blinking
	// data block information"(?)
	ps := sp.currentPrefs()
	color := ps.Brightness.FullDatablocks.ScaleRGB(STARSUntrackedAircraftColor)
	halfSeconds := ctx.Now.UnixMilli() / 500
	blinkDim := halfSeconds&1 == 0
	if blinkDim {
		color = color.Scale(0.5)
	}

	p := transforms.WindowFromLatLongP(sp.highlightedLocation)
	delta := float32(4)
	td := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(td)
	td.AddQuad(math.Add2f(p, [2]float32{-delta, -delta}), math.Add2f(p, [2]float32{delta, -delta}),
		math.Add2f(p, [2]float32{delta, delta}), math.Add2f(p, [2]float32{-delta, delta}))

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawVFRAirports(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if !sp.showVFRAirports {
		return
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	ps := sp.currentPrefs()
	color := ps.Brightness.Lines.RGB()
	style := renderer.TextStyle{
		Font:  sp.systemFont(ctx, ps.CharSize.Tools),
		Color: color,
	}

	for name, ap := range ctx.Client.State.DepartureAirports {
		if ap.VFRRateSum() > 0 {
			pll := av.DB.Airports[name].Location
			pw := transforms.WindowFromLatLongP(pll)
			ld.AddCircle(pw, 10, 32)

			td.AddText(name, math.Add2f(pw, [2]float32{12, 0}), style)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

// Draw all of the range-bearing lines that have been specified.
func (sp *STARSPane) drawRBLs(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.currentPrefs()
	color := ps.Brightness.Lines.RGB() // check
	style := renderer.TextStyle{
		Font:  sp.systemFont(ctx, ps.CharSize.Tools),
		Color: color,
	}

	drawRBL := func(p0 math.Point2LL, p1 math.Point2LL, idx int, gs float32) {
		// Format the range-bearing line text for the two positions.
		hdg := math.Heading2LL(p0, p1, ctx.NmPerLongitude, ctx.MagneticVariation)
		dist := math.NMDistance2LL(p0, p1)
		text := fmt.Sprintf(" %03d/%.2f", int(hdg+.5), dist) // leading space for alignment
		if gs != 0 {
			// Add ETA in minutes
			eta := 60 * dist / gs
			text += fmt.Sprintf("/%d", int(eta+.5))
		}
		text += fmt.Sprintf("-%d", idx)

		// And draw the line and the text.
		pText := transforms.WindowFromLatLongP(p1) // draw at right endpoint
		//pText[1] += float32(style.Font.Size / 2)   // vertically align
		td.AddText(text, pText, style)
		ld.AddLine(p0, p1, color)
	}

	// Maybe draw a wip RBL with p1 as the mouse's position
	if sp.wipRBL != nil {
		wp := sp.wipRBL.P[0]
		if ctx.Mouse != nil {
			p1 := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			if wp.ADSBCallsign != "" {
				if trk, ok := ctx.GetTrackByCallsign(wp.ADSBCallsign); ok && sp.datablockVisible(ctx, *trk) &&
					slices.ContainsFunc(tracks, func(t sim.Track) bool { return t.ADSBCallsign == trk.ADSBCallsign }) {
					state := sp.TrackState[wp.ADSBCallsign]
					drawRBL(state.track.Location, p1, len(sp.RangeBearingLines)+1, state.track.Groundspeed)
				}
			} else {
				drawRBL(wp.Loc, p1, len(sp.RangeBearingLines)+1, 0)
			}
		}
	}

	for i, rbl := range sp.RangeBearingLines {
		if p0, p1 := rbl.GetPoints(ctx, tracks, sp); !p0.IsZero() && !p1.IsZero() {
			gs := float32(0)

			// If one but not both are tracks, get the groundspeed so we
			// can display an ETA.
			if rbl.P[0].ADSBCallsign != "" && rbl.P[1].ADSBCallsign == "" {
				if state, ok := sp.TrackState[rbl.P[0].ADSBCallsign]; ok {
					gs = state.track.Groundspeed
				}
			} else if rbl.P[1].ADSBCallsign != "" && rbl.P[0].ADSBCallsign == "" {
				if state, ok := sp.TrackState[rbl.P[1].ADSBCallsign]; ok {
					gs = state.track.Groundspeed
				}
			}

			drawRBL(p0, p1, i+1, gs)
		}
	}

	// Remove stale ones that include aircraft that have landed, etc.
	sp.RangeBearingLines = util.FilterSlice(sp.RangeBearingLines, func(rbl STARSRangeBearingLine) bool {
		p0, p1 := rbl.GetPoints(ctx, tracks, sp)
		return !p0.IsZero() && !p1.IsZero()
	})

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	cs0, cs1 := sp.MinSepAircraft[0], sp.MinSepAircraft[1]
	if cs0 == "" || cs1 == "" {
		// Two aircraft haven't been specified.
		return
	}
	trk0, ok0 := ctx.GetTrackByCallsign(cs0)
	trk1, ok1 := ctx.GetTrackByCallsign(cs1)
	if !ok0 || !ok1 {
		// Missing track(s)
		return
	}

	ps := sp.currentPrefs()
	color := ps.Brightness.Lines.RGB()

	s0, ok0 := sp.TrackState[trk0.ADSBCallsign]
	s1, ok1 := sp.TrackState[trk1.ADSBCallsign]
	if !ok0 || !ok1 {
		return
	}

	// Go ahead and draw the minimum separation lines and text.
	p0ll, p1ll := trk0.Location, trk1.Location
	nmPerLongitude := ctx.NmPerLongitude
	magneticVariation := ctx.MagneticVariation

	d0ll := s0.HeadingVector(nmPerLongitude, magneticVariation)
	d1ll := s1.HeadingVector(nmPerLongitude, magneticVariation)

	p0, d0 := math.LL2NM(p0ll, nmPerLongitude), math.LL2NM(d0ll, nmPerLongitude)
	p1, d1 := math.LL2NM(p1ll, nmPerLongitude), math.LL2NM(d1ll, nmPerLongitude)

	// Find the parametric distance along the respective rays of the
	// aircrafts' courses where they at at a minimum distance; this is
	// linearly extrapolating their positions.
	tmin := math.RayRayMinimumDistance(p0, d0, p1, d1)

	// If something blew up in RayRayMinimumDistance then just bail out here.
	if gomath.IsInf(float64(tmin), 0) || gomath.IsNaN(float64(tmin)) {
		return
	}

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	font := sp.systemFont(ctx, ps.CharSize.Tools)

	// Draw the separator lines (and triangles, if appropriate.)
	var pw0, pw1 [2]float32          // Window coordinates of the points of minimum approach
	var p0tmin, p1tmin math.Point2LL // Lat-long coordinates of the points of minimum approach
	if tmin < 0 {
		// The closest approach was in the past; just draw a line between
		// the two tracks and initialize the above coordinates.
		ld.AddLine(p0ll, p1ll, color)
		p0tmin, p1tmin = p0ll, p1ll
		pw0, pw1 = transforms.WindowFromLatLongP(p0ll), transforms.WindowFromLatLongP(p1ll)
	} else {
		// Closest approach in the future: draw a line from each track to
		// the minimum separation line as well as the minimum separation
		// line itself.
		p0tmin = math.NM2LL(math.Add2f(p0, math.Scale2f(d0, tmin)), nmPerLongitude)
		p1tmin = math.NM2LL(math.Add2f(p1, math.Scale2f(d1, tmin)), nmPerLongitude)
		ld.AddLine(p0ll, p0tmin, color)
		ld.AddLine(p0tmin, p1tmin, color)
		ld.AddLine(p1tmin, p1ll, color)

		// Draw filled triangles centered at p0tmin and p1tmin.
		pw0, pw1 = transforms.WindowFromLatLongP(p0tmin), transforms.WindowFromLatLongP(p1tmin)
		style := renderer.TextStyle{Font: font, Color: color}
		td.AddTextCentered(STARSFilledUpTriangle, pw0, style)
		td.AddTextCentered(STARSFilledUpTriangle, pw1, style)
	}

	// Draw the text for the minimum distance
	// Center the text along the minimum distance line
	pText := math.Mid2f(pw0, pw1)
	style := renderer.TextStyle{
		Font:            font,
		Color:           color,
		DrawBackground:  true,
		BackgroundColor: renderer.RGB{},
	}
	text := fmt.Sprintf("%.2fNM", math.NMDistance2LL(p0tmin, p1tmin))
	if tmin < 0 {
		text = "NO XING\n" + text
	}
	td.AddTextCentered(text, pText, style)

	// Add the corresponding drawing commands to the CommandBuffer.
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawScenarioRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font, cb *renderer.CommandBuffer) {
	if len(sp.scopeDraw.arrivals) == 0 && len(sp.scopeDraw.approaches) == 0 && len(sp.scopeDraw.departures) == 0 &&
		len(sp.scopeDraw.overflights) == 0 && len(sp.scopeDraw.airspace) == 0 {
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

	sp.drawScenarioArrivalRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	sp.drawScenarioApproachRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	sp.drawScenarioDepartureRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	sp.drawScenarioOverflightRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
	sp.drawScenarioAirspaceRoutes(ctx, transforms, font, cb, drawnWaypoints, td, ld, pd, ldr)
}

func (sp *STARSPane) drawScenarioArrivalRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := sp.ScaledRGBFromColorPickerRGB(*sp.IFPHelpers.ArrivalsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if sp.scopeDraw.arrivals != nil {
		for _, name := range util.SortedMapKeys(ctx.Client.State.InboundFlows) {
			if sp.scopeDraw.arrivals[name] == nil {
				continue
			}

			arrivals := ctx.Client.State.InboundFlows[name].Arrivals
			for i, arr := range arrivals {
				if sp.scopeDraw.arrivals == nil || !sp.scopeDraw.arrivals[name][i] {
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

func (sp *STARSPane) drawScenarioApproachRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := sp.ScaledRGBFromColorPickerRGB(*sp.IFPHelpers.ApproachesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if sp.scopeDraw.approaches != nil {
		for _, rwy := range ctx.Client.State.ArrivalRunways {
			if sp.scopeDraw.approaches[rwy.Airport] == nil {
				continue
			}
			ap := ctx.Client.State.Airports[rwy.Airport]
			for _, name := range util.SortedMapKeys(ap.Approaches) {
				appr := ap.Approaches[name]
				if appr.Runway == rwy.Runway && sp.scopeDraw.approaches[rwy.Airport][name] {
					for _, wp := range appr.Waypoints {
						radar.DrawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)
					}
				}
			}
		}
	}

	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (sp *STARSPane) drawScenarioDepartureRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := sp.ScaledRGBFromColorPickerRGB(*sp.IFPHelpers.DeparturesColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if sp.scopeDraw.departures != nil {
		for _, name := range util.SortedMapKeys(ctx.Client.State.Airports) {
			if sp.scopeDraw.departures[name] == nil {
				continue
			}

			ap := ctx.Client.State.Airports[name]
			for _, rwy := range util.SortedMapKeys(ap.DepartureRoutes) {
				if sp.scopeDraw.departures[name][rwy] == nil {
					continue
				}

				exitRoutes := ap.DepartureRoutes[rwy]
				for _, exit := range util.SortedMapKeys(exitRoutes) {
					if sp.scopeDraw.departures[name][rwy][exit] {
						radar.DrawWaypoints(ctx, exitRoutes[exit].Waypoints, drawnWaypoints, transforms,
							td, style, ld, pd, ldr, color)
					}
				}
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (sp *STARSPane) drawScenarioOverflightRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := sp.ScaledRGBFromColorPickerRGB(*sp.IFPHelpers.OverflightsColor)

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	if sp.scopeDraw.overflights != nil {
		for _, name := range util.SortedMapKeys(ctx.Client.State.InboundFlows) {
			if sp.scopeDraw.overflights[name] == nil {
				continue
			}

			overflights := ctx.Client.State.InboundFlows[name].Overflights
			for i, of := range overflights {
				if sp.scopeDraw.overflights == nil || !sp.scopeDraw.overflights[name][i] {
					continue
				}

				radar.DrawWaypoints(ctx, of.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (sp *STARSPane) drawScenarioAirspaceRoutes(ctx *panes.Context, transforms radar.ScopeTransformations, font *renderer.Font,
	cb *renderer.CommandBuffer, drawnWaypoints map[string]interface{}, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {

	color := sp.ScaledRGBFromColorPickerRGB(*sp.IFPHelpers.AirspaceColor)
	ps := sp.currentPrefs()
	style := renderer.TextStyle{
		Font:           sp.systemFont(ctx, ps.CharSize.Tools),
		Color:          color,
		DrawBackground: true, // default BackgroundColor is fine
	}

	if sp.scopeDraw.airspace != nil {
		for _, ctrl := range util.SortedMapKeys(sp.scopeDraw.airspace) {
			for _, volname := range util.SortedMapKeys(sp.scopeDraw.airspace[ctrl]) {
				if !sp.scopeDraw.airspace[ctrl][volname] {
					continue
				}

				for _, vol := range ctx.Client.State.Airspace[ctrl][volname] {
					for _, pts := range vol.Boundaries {
						for i := range pts[:len(pts)-1] {
							ld.AddLine(pts[i], pts[i+1], color)
						}
					}

					td.AddTextCentered(vol.Label, transforms.WindowFromLatLongP(vol.LabelPosition), style)
				}
			}
		}
	}
	radar.GenerateRouteDrawingCommands(cb, transforms, ctx, ld, pd, td, ldr)
}

func (sp *STARSPane) ScaledRGBFromColorPickerRGB(input [3]float32) renderer.RGB {
	ps := sp.currentPrefs()
	return ps.Brightness.Lists.ScaleRGB(renderer.RGB{input[0], input[1], input[2]})
}

func (sp *STARSPane) drawPTLs(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]
		if !state.HaveHeading() {
			continue
		}

		if trk.IsUnassociated() && !state.DisplayPTL {
			// untracked only PTLs if they're individually enabled (I think); 6-13.
			continue
		}
		// We have it or it's an inbound handoff to us.
		ourTrack := trk.IsAssociated() && (trk.FlightPlan.TrackingController == ctx.UserTCP ||
			trk.FlightPlan.HandoffTrackController == ctx.UserTCP)
		if !state.DisplayPTL && !ps.PTLAll && !(ps.PTLOwn && ourTrack) {
			continue
		}

		if ps.PTLLength == 0 {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(state.track.Groundspeed) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := state.TrackHeading(ctx.NmPerLongitude)
		h := math.SinCos(math.Radians(hdg))
		h = math.Scale2f(h, dist)
		end := math.Add2f(math.LL2NM(state.track.Location, ctx.NmPerLongitude), h)

		ld.AddLine(state.track.Location, math.NM2LL(end, ctx.NmPerLongitude), color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Datablocks)
	color := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)

	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]

		// Format a radius/length for printing, ditching the ".0" if it's
		// an integer value.
		format := func(v float32) string {
			if v == float32(int(v)) {
				return strconv.Itoa(int(v))
			} else {
				return fmt.Sprintf("%.1f", v)
			}
		}

		if state.JRingRadius > 0 {
			const nsegs = 360
			pc := transforms.WindowFromLatLongP(state.track.Location)
			radius := state.JRingRadius / transforms.PixelDistanceNM(ctx.NmPerLongitude)
			ld.AddCircle(pc, radius, nsegs, color)

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				// draw the ring size around 7.5 o'clock
				// vector from center to the circle there
				v := [2]float32{-.707106 * radius, -.707106 * radius} // -sqrt(2)/2
				// move up to make space for the text
				v[1] += float32(font.Size) + 3
				pt := math.Add2f(pc, v)
				textStyle := renderer.TextStyle{Font: font, Color: color}
				td.AddText(format(state.JRingRadius), pt, textStyle)
			}
		}
		atpaStatus := state.ATPAStatus // this may change

		// If warning/alert cones are inhibited but monitor cones are not,
		// we may still draw a monitor cone.
		if (atpaStatus == ATPAStatusWarning || atpaStatus == ATPAStatusAlert) &&
			(!ps.DisplayATPAWarningAlertCones || (state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert)) {
			atpaStatus = ATPAStatusMonitor
		}

		drawATPAMonitor := atpaStatus == ATPAStatusMonitor && ps.DisplayATPAMonitorCones &&
			(state.DisplayATPAMonitor == nil || *state.DisplayATPAMonitor) &&
			state.IntrailDistance-state.MinimumMIT <= 2 // monitor only if within 2nm of MIT requirement
		drawATPAWarning := atpaStatus == ATPAStatusWarning && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPAAlert := atpaStatus == ATPAStatusAlert && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPACone := drawATPAMonitor || drawATPAWarning || drawATPAAlert

		if state.HaveHeading() && (state.ConeLength > 0 || drawATPACone) {
			// Find the length of the cone in pixel coordinates)
			lengthNM := max(state.ConeLength, state.MinimumMIT)
			length := lengthNM / transforms.PixelDistanceNM(ctx.NmPerLongitude)

			// Form a triangle; the end of the cone is 10 pixels wide
			pts := [3][2]float32{{0, 0}, {-5, length}, {5, length}}

			// Now we'll rotate the vertices so that it points in the
			// appropriate direction.
			var coneHeading float32
			if drawATPACone {
				// The cone is oriented to point toward the leading aircraft.
				if sfront, ok := sp.TrackState[state.ATPALeadAircraftCallsign]; ok {
					coneHeading = math.Heading2LL(state.track.Location, sfront.track.Location,
						ctx.NmPerLongitude, ctx.MagneticVariation)
				}
			} else {
				// The cone is oriented along the aircraft's heading.
				coneHeading = state.TrackHeading(ctx.NmPerLongitude) + ctx.MagneticVariation
			}
			rot := math.Rotator2f(coneHeading)
			for i := range pts {
				pts[i] = rot(pts[i])
			}

			coneColor := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)
			if atpaStatus == ATPAStatusWarning {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAWarningColor)
			} else if atpaStatus == ATPAStatusAlert {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAAlertColor)
			}

			// We've got what we need to draw a polyline with the
			// aircraft's position as an anchor.
			pw := transforms.WindowFromLatLongP(state.track.Location)
			for i := range pts {
				pts[i] = math.Add2f(pts[i], pw)
			}
			ld.AddLineLoop(coneColor, pts[:])

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				textStyle := renderer.TextStyle{Font: font, Color: coneColor}

				pCenter := math.Add2f(pw, rot(math.Scale2f([2]float32{0, 0.5}, length)))

				// Draw a quad in the background color behind the text
				text := format(lengthNM)
				bx, by := textStyle.Font.BoundText(" "+text+" ", 0)
				fbx, fby := float32(bx), float32(by+2)
				trid.AddQuad(math.Add2f(pCenter, [2]float32{-fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, fby / 2}),
					math.Add2f(pCenter, [2]float32{-fbx / 2, fby / 2}))

				td.AddTextCentered(text, pCenter, textStyle)
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	cb.SetRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawSelectedRoute(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if sp.drawRouteAircraft == "" {
		return
	}
	trk, ok := ctx.GetTrackByCallsign(sp.drawRouteAircraft)
	if !ok {
		sp.drawRouteAircraft = ""
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	prev := sp.TrackState[sp.drawRouteAircraft].track.Location
	for _, p := range trk.Route {
		ld.AddLine(prev, p)
		prev = p
	}

	prefs := sp.currentPrefs()
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(prefs.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawPlotPoints(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if len(sp.drawRoutePoints) == 0 {
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, pt := range sp.drawRoutePoints {
		pwin := transforms.WindowFromLatLongP(pt)
		ld.AddCircle(pwin, 10, 30)
		if i+1 < len(sp.drawRoutePoints) {
			ld.AddLine(pwin, transforms.WindowFromLatLongP(sp.drawRoutePoints[i+1]))
		}
	}
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(renderer.RGB{1, .3, .3})
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

type STARSRangeBearingLine struct {
	P [2]struct {
		// If callsign is given, use that aircraft's position;
		// otherwise we have a fixed position.
		Loc          math.Point2LL
		ADSBCallsign av.ADSBCallsign
	}
}

func (rbl STARSRangeBearingLine) GetPoints(ctx *panes.Context, tracks []sim.Track, sp *STARSPane) (math.Point2LL, math.Point2LL) {
	// Each line endpoint may be specified either by a track's
	// position or by a fixed position.
	getLoc := func(i int) math.Point2LL {
		if state, ok := sp.TrackState[rbl.P[i].ADSBCallsign]; ok {
			return state.track.Location
		}
		return rbl.P[i].Loc
	}
	return getLoc(0), getLoc(1)
}

func rblSecondClickHandler(ctx *panes.Context, sp *STARSPane, tracks []sim.Track, pw [2]float32,
	transforms radar.ScopeTransformations) (status CommandStatus) {
	if sp.wipRBL == nil {
		// this shouldn't happen, but let's not crash if it does...
		return
	}

	rbl := *sp.wipRBL
	sp.wipRBL = nil
	if trk, _ := sp.tryGetClosestTrack(ctx, pw, transforms, tracks); trk != nil {
		rbl.P[1].ADSBCallsign = trk.ADSBCallsign
	} else {
		rbl.P[1].Loc = transforms.LatLongFromWindowP(pw)
	}
	sp.RangeBearingLines = append(sp.RangeBearingLines, rbl)
	status.clear = true
	return
}

func (sp *STARSPane) displaySignificantPointInfo(p0, p1 math.Point2LL, nmPerLongitude, magneticVariation float32) (status CommandStatus) {
	// Find the closest significant point to p1.
	minDist := float32(1000000)
	var closest *sim.SignificantPoint
	for _, sigpt := range sp.significantPointsSlice {
		d := math.NMDistance2LL(sigpt.Location, p1)
		if d < minDist {
			minDist = d
			closest = &sigpt
		}
	}

	sp.wipSignificantPoint = nil
	status.clear = true

	if closest == nil {
		// No significant points defined?
		return
	}

	// Display a blinking square at the point
	sp.highlightedLocation = closest.Location
	sp.highlightedLocationEndTime = time.Now().Add(5 * time.Second)

	// 6-148
	format := func(sig sim.SignificantPoint) string {
		d := math.NMDistance2LL(p0, sig.Location)
		str := ""
		if d > 1 { // no bearing range if within 1nm
			hdg := math.Heading2LL(p0, sig.Location, nmPerLongitude, magneticVariation)
			str = fmt.Sprintf("%03d/%.2f ", int(hdg), d)
			for len(str) < 9 {
				str += " "
			}
		}

		if sig.Description != "" {
			return str + strings.ToUpper(sig.Description)
		} else {
			return str + sig.Name
		}
	}

	str := format(*closest)

	// Up to 5 additional, if they are within 1nm of the selected point
	n := 0
	for _, sig := range sp.significantPointsSlice {
		if sig.Name != closest.Name && math.NMDistance2LL(sig.Location, closest.Location) < 1 {
			str += "\n" + format(sig)
			n++
			if n == 5 {
				break
			}
		}
	}

	status.output = str

	return
}

func toSignificantPointClickHandler(ctx *panes.Context, sp *STARSPane, tracks []sim.Track, pw [2]float32,
	transforms radar.ScopeTransformations) (status CommandStatus) {
	if sp.wipSignificantPoint == nil {
		status.clear = true
		return
	} else {
		p1 := transforms.LatLongFromWindowP(pw)
		return sp.displaySignificantPointInfo(*sp.wipSignificantPoint, p1,
			ctx.NmPerLongitude, ctx.MagneticVariation)
	}
}

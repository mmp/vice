// cmd/backshop/routes.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// routeDraw is which of the scenario's procedures are drawn on the map, and
// in what color.
type routeDraw struct {
	arrivals    map[string]map[int]bool
	approaches  map[av.ICAOAirportCode]map[string]bool
	departures  map[av.ICAOAirportCode]map[string]map[string]bool // airport -> runway -> exit
	overflights map[string]map[int]bool
	airspace    map[sim.ControlPosition]map[string]bool
	holds       map[string]av.Hold
	allHolds    bool

	groupDeparturesBySID bool

	arrivalsColor    [3]float32
	approachesColor  [3]float32
	departuresColor  [3]float32
	overflightsColor [3]float32
	airspaceColor    [3]float32
	holdsColor       [3]float32
}

func (r *routeDraw) init() {
	r.groupDeparturesBySID = true
	r.arrivalsColor = [3]float32{.1, .9, .9}
	r.approachesColor = [3]float32{.1, .9, .1}
	r.departuresColor = [3]float32{.9, .5, .1}
	r.overflightsColor = [3]float32{.9, .1, .9}
	r.airspaceColor = [3]float32{.4, .6, 1}
	r.holdsColor = [3]float32{.9, .9, .3}
	r.reset()
}

func (r *routeDraw) reset() {
	r.arrivals = make(map[string]map[int]bool)
	r.approaches = make(map[av.ICAOAirportCode]map[string]bool)
	r.departures = make(map[av.ICAOAirportCode]map[string]map[string]bool)
	r.overflights = make(map[string]map[int]bool)
	r.airspace = nil
	r.holds = make(map[string]av.Hold)
	r.allHolds = false
}

func (r *routeDraw) anyEnabled() bool {
	for _, m := range r.arrivals {
		for _, b := range m {
			if b {
				return true
			}
		}
	}
	for _, m := range r.approaches {
		for _, b := range m {
			if b {
				return true
			}
		}
	}
	for _, rwys := range r.departures {
		for _, exits := range rwys {
			for _, b := range exits {
				if b {
					return true
				}
			}
		}
	}
	for _, m := range r.overflights {
		for _, b := range m {
			if b {
				return true
			}
		}
	}
	for _, vols := range r.airspace {
		for _, b := range vols {
			if b {
				return true
			}
		}
	}
	return len(r.holds) > 0
}

func rgb(c [3]float32) renderer.RGB { return renderer.RGB{R: c[0], G: c[1], B: c[2]} }

///////////////////////////////////////////////////////////////////////////
// Drawing

// drawRoutes draws every procedure the Routes tab has enabled. Waypoints
// shared by several routes are drawn once and their labels stacked, which
// is what DrawnRoutes tracks.
func (in *inspector) drawRoutes(a *app, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	r := &in.routes
	if a.cc == nil || !r.anyEnabled() {
		return
	}

	ss := &a.cc.State
	nm, magvar := ss.NmPerLongitude, ss.MagneticVariation
	dpi := a.plat.DPIScale()
	font := a.scope.textFont

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	pd := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(pd)
	ldr := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ldr)

	drawn := scope.NewDrawnRoutes()
	drawnHolds := make(map[string]any)

	style := func(c [3]float32) renderer.TextStyle {
		return renderer.TextStyle{Font: font, Color: rgb(c), DrawBackground: true}
	}

	// Arrivals
	arrColor, arrStyle := rgb(r.arrivalsColor), style(r.arrivalsColor)
	for name, flow := range util.SortedMap(ss.InboundFlows) {
		for i, arr := range flow.Arrivals {
			if !r.arrivals[name][i] {
				continue
			}
			scope.DrawWaypoints(nm, magvar, arr.Waypoints, scope.ArrivalRouteContext(arr), drawn, transforms,
				td, arrStyle, ld, pd, ldr, arrColor)
			scope.SkipProcedureTurnHolds(arr.Waypoints, drawnHolds)

			if arr.STAR != "" {
				for airport := range arr.RunwayWaypoints {
					for _, holds := range av.DB.TerminalHolds[airport] {
						for _, h := range holds {
							if h.Procedure == arr.STAR {
								scope.DrawHoldPattern(nm, magvar, transforms, h, arrColor, td, ld, arrStyle, drawn, drawnHolds)
							}
						}
					}
				}
				scope.DrawEnrouteHolds(nm, magvar, transforms, arr.Waypoints, arr.STAR, arrColor, ld, td, arrStyle, drawn, drawnHolds)
			}

			for _, rwys := range util.SortedMap(arr.RunwayWaypoints) {
				for rwy, wp := range util.SortedMap(rwys) {
					scope.DrawWaypoints(nm, magvar, wp, scope.ArrivalRouteContext(arr), drawn, transforms,
						td, arrStyle, ld, pd, ldr, arrColor)
					if len(wp) > 1 {
						pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
						td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), arrStyle)
					} else if h, ok := wp[0].HeadingAction(); ok {
						ang := math.Radians(math.MagneticToTrue(math.MagneticHeading(h.Heading), magvar))
						pend := math.Add2f(math.LL2NM(wp[0].Location, nm), math.SinCos(ang))
						td.AddTextCentered(rwy, transforms.WindowFromLatLongP(math.NM2LL(pend, nm)), arrStyle)
					}
				}
			}
		}
	}

	// Approaches
	apprColor, apprStyle := rgb(r.approachesColor), style(r.approachesColor)
	for _, rwy := range ss.ArrivalRunways {
		ap, ok := ss.Airports[rwy.Airport]
		if !ok {
			continue
		}
		for name, appr := range util.SortedMap(ap.Approaches) {
			if appr.Runway != rwy.Runway.Base() || !r.approaches[rwy.Airport][name] {
				continue
			}
			for _, wp := range appr.Waypoints {
				scope.DrawWaypoints(nm, magvar, wp, scope.ApproachRouteContext(appr), drawn, transforms,
					td, apprStyle, ld, pd, ldr, apprColor)
				scope.SkipProcedureTurnHolds(wp, drawnHolds)
			}
			for _, holds := range av.DB.TerminalHolds[rwy.Airport] {
				for _, h := range holds {
					if h.Procedure != name {
						continue
					}
					scope.DrawHoldPattern(nm, magvar, transforms, h, apprColor, td, ld, apprStyle, drawn, drawnHolds)
					pMissed, _ := av.DB.LookupWaypoint(h.Fix)
					ld.AddDashedLine(av.DB.Airports[rwy.Airport].Location, pMissed, .005, .0075, apprColor)
				}
			}
			for _, wp := range appr.Waypoints {
				scope.DrawEnrouteHolds(nm, magvar, transforms, wp, name, apprColor, ld, td, apprStyle, drawn, drawnHolds)
			}
		}
	}

	// Departures
	depColor, depStyle := rgb(r.departuresColor), style(r.departuresColor)
	for name, ap := range util.SortedMap(ss.Airports) {
		if r.departures[name] == nil {
			continue
		}
		for rwy, exitRoutes := range util.SortedMap(ap.DepartureRoutes) {
			if r.departures[name][string(rwy)] == nil {
				continue
			}
			for exit, routes := range util.SortedMap(exitRoutes) {
				if !r.departures[name][string(rwy)][string(exit)] {
					continue
				}
				for _, exitRoute := range routes {
					scope.DrawWaypoints(nm, magvar, exitRoute.Waypoints, scope.DepartureRouteContext(name, exitRoute),
						drawn, transforms, td, depStyle, ld, pd, ldr, depColor)
				}
			}
		}
	}

	// Overflights
	ofColor, ofStyle := rgb(r.overflightsColor), style(r.overflightsColor)
	for name, flow := range util.SortedMap(ss.InboundFlows) {
		for i, of := range flow.Overflights {
			if !r.overflights[name][i] {
				continue
			}
			scope.DrawWaypoints(nm, magvar, of.Waypoints, scope.OverflightRouteContext(of), drawn, transforms,
				td, ofStyle, ld, pd, ldr, ofColor)
		}
	}

	// Controller airspace
	asColor, asStyle := rgb(r.airspaceColor), style(r.airspaceColor)
	for tcp, vols := range util.SortedMap(r.airspace) {
		for volname, enabled := range util.SortedMap(vols) {
			if !enabled {
				continue
			}
			for _, vol := range ss.Airspace[tcp][volname] {
				for _, pts := range vol.Boundaries {
					for i := range max(len(pts)-1, 0) {
						ld.AddLine(pts[i], pts[i+1], asColor)
					}
				}
				td.AddTextCentered(vol.Label, transforms.WindowFromLatLongP(vol.LabelPosition), asStyle)
			}
		}
	}

	// Charted holds selected on their own
	holdColor, holdStyle := rgb(r.holdsColor), style(r.holdsColor)
	for _, hold := range util.SortedMap(r.holds) {
		scope.DrawHoldPattern(nm, magvar, transforms, hold, holdColor, td, ld, holdStyle, drawn, drawnHolds)
	}

	scope.GenerateRouteDrawingCommands(cb, transforms, dpi, ld, pd, td, ldr)
}

///////////////////////////////////////////////////////////////////////////
// UI

// drawProceduresTab picks which of the scenario's procedures--its arrivals,
// approaches, departures, overflights, airspace, and holds--are drawn on the
// map, and in what color.
func (in *inspector) drawProceduresTab(a *app) {
	if !imgui.BeginTabItem("Procedures") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	r := &in.routes
	ss := &a.cc.State

	if imgui.Button("Clear all") {
		r.reset()
	}

	colorPicker := func(id string, c *[3]float32) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("##"+id, c, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)
	}

	if imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		colorPicker("arrcolor", &r.arrivalsColor)
		in.drawArrivals(a)
	}

	if imgui.CollapsingHeaderBoolPtr("Approaches", nil) {
		colorPicker("apprcolor", &r.approachesColor)
		flags, size := tableSize(approachCount(ss), 12)
		if imgui.BeginTableV("appr", 6, flags, size, 0) {
			setupDrawColumn()
			imgui.TableSetupColumnV("Airport", imgui.TableColumnFlagsWidthFixed, 55, 0)
			imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 55, 0)
			imgui.TableSetupColumnV("Code", imgui.TableColumnFlagsWidthFixed, 50, 0)
			imgui.TableSetupColumnV("FAF", imgui.TableColumnFlagsWidthFixed, 60, 0)
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, rwy := range ss.ArrivalRunways {
				ap, ok := ss.Airports[rwy.Airport]
				if !ok {
					continue
				}
				if r.approaches[rwy.Airport] == nil {
					r.approaches[rwy.Airport] = make(map[string]bool)
				}
				for name, appr := range util.SortedMap(ap.Approaches) {
					if appr.Runway != rwy.Runway.Base() {
						continue
					}
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := r.approaches[rwy.Airport][name]
					imgui.Checkbox("##appr-"+string(rwy.Airport)+"-"+string(rwy.Runway)+"-"+name, &enabled)
					r.approaches[rwy.Airport][name] = enabled
					imgui.TableNextColumn()
					imgui.Text(string(rwy.Airport))
					imgui.TableNextColumn()
					imgui.Text(string(rwy.Runway))
					imgui.TableNextColumn()
					imgui.Text(name)
					imgui.TableNextColumn()
					for _, wp := range appr.Waypoints[0] {
						if wp.FAF() {
							imgui.Text(wp.Fix)
							break
						}
					}
					imgui.TableNextColumn()
					imgui.TextWrapped(appr.FullName)
				}
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		colorPicker("depcolor", &r.departuresColor)
		imgui.SameLine()
		// One row per SID is the way to find a procedure; one row per exit
		// is the way to draw a single route out of a SID that fans out to
		// twenty of them and is unreadable all at once.
		imgui.Checkbox("Group by SID", &r.groupDeparturesBySID)
		if r.groupDeparturesBySID {
			in.drawDepartureSIDs(a)
		} else {
			in.drawDepartureExits(a)
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
		colorPicker("ofcolor", &r.overflightsColor)
		flags, size := tableSize(overflightCount(ss), 10)
		if imgui.BeginTableV("of", 3, flags, size, 0) {
			setupDrawColumn()
			imgui.TableSetupColumnV("Overflight", imgui.TableColumnFlagsWidthFixed, 90, 0)
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for name, flow := range util.SortedMap(ss.InboundFlows) {
				if len(flow.Overflights) == 0 {
					continue
				}
				if r.overflights[name] == nil {
					r.overflights[name] = make(map[int]bool)
				}
				for i, of := range flow.Overflights {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := r.overflights[name][i]
					imgui.Checkbox(fmt.Sprintf("##of-%s-%d", name, i), &enabled)
					r.overflights[name][i] = enabled
					imgui.TableNextColumn()
					imgui.Text(name)
					imgui.TableNextColumn()
					imgui.TextWrapped(util.Select(of.Description != "", of.Description, "--"))
				}
			}
			imgui.EndTable()
		}
	}

	if len(ss.Airspace) > 0 && imgui.CollapsingHeaderBoolPtr("Controller airspace", nil) {
		colorPicker("ascolor", &r.airspaceColor)
		if r.airspace == nil {
			r.airspace = make(map[sim.ControlPosition]map[string]bool)
			for ctrl, sectors := range ss.Airspace {
				r.airspace[ctrl] = make(map[string]bool)
				for _, sector := range util.SortedMapKeys(sectors) {
					r.airspace[ctrl][sector] = false
				}
			}
		}
		for pos, vols := range util.SortedMap(r.airspace) {
			hdr := string(pos)
			if ctrl, ok := ss.Controllers[pos]; ok {
				hdr += " (" + ctrl.Position + ")"
			}
			if !imgui.TreeNodeExStr(hdr) {
				continue
			}
			flags, size := tableSize(len(vols), 12)
			if imgui.BeginTableV("vols-"+string(pos), 3, flags, size, 0) {
				for vol, b := range util.SortedMap(vols) {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					if imgui.Checkbox("##"+string(pos)+vol, &b) {
						vols[vol] = b
					}
					imgui.TableNextColumn()
					imgui.Text(vol)
					imgui.TableNextColumn()
					imgui.Text(altitudeRanges(ss.Airspace[pos][vol]))
				}
				imgui.EndTable()
			}
			imgui.TreePop()
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Charted holds", nil) {
		colorPicker("holdcolor", &r.holdsColor)
		in.drawHoldsUI(a)
	}
}

// servesArrivalAirport reports whether the arrival brings traffic to one of
// the airports the scenario takes IFR arrivals at. A scenario group's inbound
// flows cover the whole facility, so most of them land somewhere the scenario
// isn't working.
func servesArrivalAirport(ss *client.SimState, arr av.Arrival) bool {
	return slices.ContainsFunc(arr.Airports, func(ap av.ICAOAirportCode) bool {
		_, ok := ss.ArrivalAirports[ap]
		return ok
	})
}

// hasIFRDepartures reports whether the scenario launches IFR departures from
// the airport. ss.DepartureAirports also holds the ones that only see VFR
// traffic, which have no departure routes to show.
func hasIFRDepartures(ss *client.SimState, airport av.ICAOAirportCode) bool {
	_, ok := ss.LaunchConfig.DepartureRates[airport]
	return ok
}

func altitudeRanges(vols []av.ControllerAirspaceVolume) string {
	var s []string
	for _, v := range vols {
		s = append(s, fmt.Sprintf("%d-%d", v.LowerLimit/100, v.UpperLimit/100))
	}
	return strings.Join(s, ", ")
}

// drawHoldsUI lists the charted holds within the scope's range, since the
// full database has far too many to scroll.
func (in *inspector) drawHoldsUI(a *app) {
	r := &in.routes
	ss := &a.cc.State

	if imgui.Checkbox("All holds in range", &r.allHolds) {
		clear(r.holds)
	}

	type entry struct {
		fix  string
		hold av.Hold
		dist float32
	}
	var entries []entry
	for fix, holds := range util.SortedMap(av.DB.EnrouteHolds) {
		p, ok := av.DB.LookupWaypoint(fix)
		if !ok {
			continue
		}
		if d := math.NMDistance2LL(p, ss.Center); d < a.scope.rangeNM {
			entries = append(entries, entry{fix: fix, hold: holds[0], dist: d})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if c := cmp.Compare(a.dist, b.dist); c != 0 {
			return c
		}
		return strings.Compare(a.fix, b.fix)
	})

	if r.allHolds {
		for _, e := range entries {
			r.holds[e.fix] = e.hold
		}
	}

	flags, size := tableSize(len(entries), 12)
	if !imgui.BeginTableV("holds", 4, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Fix", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Distance", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumn("Procedure")
	imgui.TableHeadersRow()

	for _, e := range entries {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		_, enabled := r.holds[e.fix]
		if imgui.Checkbox("##hold-"+e.fix, &enabled) {
			if enabled {
				r.holds[e.fix] = e.hold
			} else {
				delete(r.holds, e.fix)
			}
		}
		imgui.TableNextColumn()
		imgui.Text(e.fix)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("%.0f nm", e.dist))
		imgui.TableNextColumn()
		imgui.Text(e.hold.Procedure)
	}
	imgui.EndTable()
}

// drawDepartureExits lists one row per runway and exit, for picking out a
// single route from a SID that serves many.
func (in *inspector) drawDepartureExits(a *app) {
	r := &in.routes
	ss := &a.cc.State

	type exitRow struct {
		airport      av.ICAOAirportCode
		runway       av.RunwayID
		exit         av.ExitID
		sids         []string
		descriptions []string
		background   bool
	}
	var rows []exitRow
	for airport, ap := range util.SortedMap(ss.Airports) {
		if !hasIFRDepartures(ss, airport) {
			continue
		}
		if r.departures[airport] == nil {
			r.departures[airport] = make(map[string]map[string]bool)
		}
		for rwy, exitRoutes := range util.SortedMap(ap.DepartureRoutes) {
			if r.departures[airport][string(rwy)] == nil {
				r.departures[airport][string(rwy)] = make(map[string]bool)
			}
			for exit, routes := range util.SortedMap(exitRoutes) {
				row := exitRow{airport: airport, runway: rwy, exit: exit,
					background: departureIsBackground(ss, airport, rwy, exit)}
				for _, er := range routes {
					if er.SID != "" && !slices.Contains(row.sids, er.SID) {
						row.sids = append(row.sids, er.SID)
					}
					if er.Description != "" && !slices.Contains(row.descriptions, er.Description) {
						row.descriptions = append(row.descriptions, er.Description)
					}
				}
				rows = append(rows, row)
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b exitRow) int {
		if c := strings.Compare(string(a.airport), string(b.airport)); c != 0 {
			return c
		}
		return backgroundOrder(a.background, b.background)
	})

	flags, size := tableSize(len(rows), 14)
	if !imgui.BeginTableV("depexits", 7, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Airport", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Exit", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("SID", imgui.TableColumnFlagsWidthFixed, 70, 0)
	setupBackgroundColumn()
	imgui.TableSetupColumn("Description")
	imgui.TableHeadersRow()

	for _, row := range rows {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		enabled := r.departures[row.airport][string(row.runway)][string(row.exit)]
		imgui.Checkbox("##dx-"+string(row.airport)+string(row.runway)+string(row.exit), &enabled)
		r.departures[row.airport][string(row.runway)][string(row.exit)] = enabled
		imgui.TableNextColumn()
		imgui.Text(string(row.airport))
		imgui.TableNextColumn()
		imgui.Text(string(row.runway))
		imgui.TableNextColumn()
		imgui.Text(string(row.exit))
		imgui.TableNextColumn()
		imgui.Text(strings.Join(row.sids, ", "))
		imgui.TableNextColumn()
		imgui.Text(backgroundText(row.background))
		imgui.TableNextColumn()
		imgui.TextWrapped(strings.Join(row.descriptions, " / "))
	}
	imgui.EndTable()
}

// sidKey identifies a row of the departures table: one SID at one airport,
// split by whether its routes are worked or background traffic so that a SID
// serving both doesn't hide the difference.
type sidKey struct {
	airport    av.ICAOAirportCode
	sid        string
	background bool
}

// sidGroup collects the departure routes a sidKey covers.
type sidGroup struct {
	routes       []runwayExit
	descriptions []string
}

type runwayExit struct {
	runway av.RunwayID
	exit   av.ExitID
}

// drawDepartureSIDs lists one row per SID rather than per runway and exit: a
// SID is what a facility engineer is looking at, and its exits fan out from
// the same procedure.
func (in *inspector) drawDepartureSIDs(a *app) {
	r := &in.routes
	ss := &a.cc.State

	groups := make(map[sidKey]*sidGroup)
	var keys []sidKey
	for airport, ap := range util.SortedMap(ss.Airports) {
		if !hasIFRDepartures(ss, airport) {
			continue
		}
		if r.departures[airport] == nil {
			r.departures[airport] = make(map[string]map[string]bool)
		}
		for rwy, exitRoutes := range util.SortedMap(ap.DepartureRoutes) {
			if r.departures[airport][string(rwy)] == nil {
				r.departures[airport][string(rwy)] = make(map[string]bool)
			}
			for exit, routes := range util.SortedMap(exitRoutes) {
				k := sidKey{airport: airport, background: departureIsBackground(ss, airport, rwy, exit)}
				for _, er := range routes {
					k.sid = er.SID
					g, ok := groups[k]
					if !ok {
						g = &sidGroup{}
						groups[k] = g
						keys = append(keys, k)
					}
					if re := (runwayExit{rwy, exit}); !slices.Contains(g.routes, re) {
						g.routes = append(g.routes, re)
					}
					if er.Description != "" && !slices.Contains(g.descriptions, er.Description) {
						g.descriptions = append(g.descriptions, er.Description)
					}
				}
			}
		}
	}
	slices.SortFunc(keys, func(a, b sidKey) int {
		if c := strings.Compare(string(a.airport), string(b.airport)); c != 0 {
			return c
		}
		if c := backgroundOrder(a.background, b.background); c != 0 {
			return c
		}
		return strings.Compare(a.sid, b.sid)
	})

	flags, size := tableSize(len(keys), 14)
	if !imgui.BeginTableV("dep", 7, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Airport", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("SID", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Runways", imgui.TableColumnFlagsWidthStretch, 1, 0)
	imgui.TableSetupColumnV("Exits", imgui.TableColumnFlagsWidthStretch, 3, 0)
	setupBackgroundColumn()
	imgui.TableSetupColumnV("Description", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableHeadersRow()

	for _, k := range keys {
		g := groups[k]
		imgui.TableNextRow()
		imgui.TableNextColumn()
		enabled := slices.ContainsFunc(g.routes, func(re runwayExit) bool {
			return r.departures[k.airport][string(re.runway)][string(re.exit)]
		})
		imgui.Checkbox(fmt.Sprintf("##dep-%s-%s-%v", k.airport, k.sid, k.background), &enabled)
		for _, re := range g.routes {
			r.departures[k.airport][string(re.runway)][string(re.exit)] = enabled
		}
		imgui.TableNextColumn()
		imgui.Text(string(k.airport))
		imgui.TableNextColumn()
		imgui.Text(k.sid)
		imgui.TableNextColumn()
		// A runway may appear once per suffixed variant; the physical runway
		// is what is worth showing.
		var rwys, exits []string
		for _, re := range g.routes {
			if base, _, _ := strings.Cut(string(re.runway), "."); !slices.Contains(rwys, base) {
				rwys = append(rwys, base)
			}
			if !slices.Contains(exits, string(re.exit)) {
				exits = append(exits, string(re.exit))
			}
		}
		imgui.TextWrapped(strings.Join(rwys, ", "))
		imgui.TableNextColumn()
		imgui.TextWrapped(strings.Join(exits, ", "))
		imgui.TableNextColumn()
		imgui.Text(backgroundText(k.background))
		imgui.TableNextColumn()
		imgui.TextWrapped(strings.Join(g.descriptions, " / "))
	}
	imgui.EndTable()
}

// drawArrivals lists the arrivals that bring traffic to the scenario's
// arrival airports.
func (in *inspector) drawArrivals(a *app) {
	r := &in.routes
	ss := &a.cc.State

	type arrivalRow struct {
		flow       string
		index      int
		arr        av.Arrival
		background bool
	}
	var rows []arrivalRow
	for name, flow := range util.SortedMap(ss.InboundFlows) {
		if r.arrivals[name] == nil && len(flow.Arrivals) > 0 {
			r.arrivals[name] = make(map[int]bool)
		}
		for i, arr := range flow.Arrivals {
			if !servesArrivalAirport(ss, arr) {
				continue
			}
			rows = append(rows, arrivalRow{flow: name, index: i, arr: arr,
				background: arrivalIsBackground(ss, name, arr)})
		}
	}
	slices.SortStableFunc(rows, func(a, b arrivalRow) int {
		return backgroundOrder(a.background, b.background)
	})

	flags, size := tableSize(len(rows), 12)
	if !imgui.BeginTableV("arr", 5, flags, size, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Arrival", imgui.TableColumnFlagsWidthFixed, 90, 0)
	imgui.TableSetupColumnV("Airport(s)", imgui.TableColumnFlagsWidthStretch, 1, 0)
	setupBackgroundColumn()
	imgui.TableSetupColumnV("Description", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableHeadersRow()

	for _, row := range rows {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		enabled := r.arrivals[row.flow][row.index]
		imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", row.flow, row.index), &enabled)
		r.arrivals[row.flow][row.index] = enabled
		imgui.TableNextColumn()
		imgui.Text(row.flow)
		imgui.TableNextColumn()
		imgui.TextWrapped(airportList(row.arr.Airports))
		imgui.TableNextColumn()
		imgui.Text(backgroundText(row.background))
		imgui.TableNextColumn()
		imgui.TextWrapped(util.Select(row.arr.Description != "", row.arr.Description, "--"))
	}
	imgui.EndTable()
}

// setupBackgroundColumn declares the column that marks the routes flown from
// start to finish by virtual controllers.
func setupBackgroundColumn() {
	imgui.TableSetupColumnV("Background", imgui.TableColumnFlagsWidthFixed, 75, 0)
}

func backgroundText(background bool) string {
	return util.Select(background, "yes", "")
}

// backgroundOrder sorts the traffic no human ever works after the traffic the
// scenario is actually about.
func backgroundOrder(a, b bool) int {
	return util.Select(a, 1, 0) - util.Select(b, 1, 0)
}

// departureIsBackground reports whether an exit's departures are flown start
// to finish by virtual controllers. The launch config records that per
// departure category, which is how a scenario's departure rates are keyed; a
// runway whose rates aren't split by category is recorded under "".
func departureIsBackground(ss *client.SimState, airport av.ICAOAirportCode, rwy av.RunwayID, exit av.ExitID) bool {
	if ss.LaunchConfig.DepartureIsBackground(airport, rwy, "") {
		return true
	}
	ap, ok := ss.Airports[airport]
	return ok && ss.LaunchConfig.DepartureIsBackground(airport, rwy, ap.ExitCategory(exit))
}

// arrivalIsBackground reports whether everything the arrival brings to the
// scenario's arrival airports is flown by virtual controllers.
func arrivalIsBackground(ss *client.SimState, flow string, arr av.Arrival) bool {
	for _, ap := range arr.Airports {
		if _, ok := ss.ArrivalAirports[ap]; !ok {
			continue
		}
		if !ss.LaunchConfig.InboundFlowIsBackground(flow, string(ap)) {
			return false
		}
	}
	return true
}

// approachCount and overflightCount are how many rows their tables will have,
// which is what sizes them.
func approachCount(ss *client.SimState) int {
	n := 0
	for _, rwy := range ss.ArrivalRunways {
		ap, ok := ss.Airports[rwy.Airport]
		if !ok {
			continue
		}
		for _, appr := range ap.Approaches {
			if appr.Runway == rwy.Runway.Base() {
				n++
			}
		}
	}
	return n
}

func overflightCount(ss *client.SimState) int {
	n := 0
	for _, flow := range ss.InboundFlows {
		n += len(flow.Overflights)
	}
	return n
}

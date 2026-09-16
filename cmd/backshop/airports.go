// cmd/backshop/airports.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// airportsTab lists the scenario's airports and, for the ones expanded, how
// the scenario sets each of them up. Its draw checkboxes are the ones the
// Procedures and CIFP tabs offer, so a route checked here is drawn in the
// color those tabs set and shows up checked there.
type airportsTab struct {
	expanded map[av.ICAOAirportCode]bool

	// flownProceduresOnly limits the published procedures listed for an
	// airport to the SIDs and STARs its scenario routes name.
	flownProceduresOnly bool
}

func (t *airportsTab) init() {
	t.expanded = make(map[av.ICAOAirportCode]bool)
	t.flownProceduresOnly = true
}

func (t *airportsTab) reset() { clear(t.expanded) }

func (in *inspector) drawAirportsTab(a *app) {
	if !imgui.BeginTabItem("Airports") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	t := &in.airports
	ss := &a.cc.State

	imgui.TextWrapped("Expand an airport for how the scenario sets it up. Routes checked below are " +
		"drawn in the colors the Procedures and CIFP tabs set.")

	if imgui.SmallButton("Expand all") {
		for name := range ss.Airports {
			t.expanded[name] = true
		}
	}
	imgui.SameLine()
	if imgui.SmallButton("Collapse all") {
		clear(t.expanded)
	}

	flags, size := tableSize(len(ss.Airports), 12)
	if imgui.BeginTableV("apts", 6, flags, size, 0) {
		imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed|imgui.TableColumnFlagsNoResize, 26, 0)
		imgui.TableSetupColumnV("Airport", imgui.TableColumnFlagsWidthFixed, 55, 0)
		imgui.TableSetupColumnV("Departures", imgui.TableColumnFlagsWidthFixed, 80, 0)
		imgui.TableSetupColumnV("Arrivals", imgui.TableColumnFlagsWidthFixed, 60, 0)
		imgui.TableSetupColumnV("Elevation", imgui.TableColumnFlagsWidthFixed, 65, 0)
		imgui.TableSetupColumn("Runways")
		imgui.TableHeadersRow()

		for _, name := range util.SortedMapKeys(ss.Airports) {
			_, arr := ss.ArrivalAirports[name]
			imgui.TableNextRow()
			imgui.TableNextColumn()
			if imgui.ArrowButton("##expand"+string(name),
				util.Select(t.expanded[name], imgui.DirDown, imgui.DirRight)) {
				t.expanded[name] = !t.expanded[name]
			}
			imgui.TableNextColumn()
			imgui.Text(string(name))
			imgui.TableNextColumn()
			imgui.Text(departureKinds(ss, name))
			imgui.TableNextColumn()
			imgui.Text(util.Select(arr, "yes", ""))
			imgui.TableNextColumn()
			if ap, ok := av.DB.Airports[name]; ok {
				imgui.Text(fmt.Sprint(ap.Elevation))
				imgui.TableNextColumn()
				imgui.Text(ap.ValidRunways())
			} else {
				imgui.TableNextColumn()
			}
		}
		imgui.EndTable()
	}

	for _, name := range util.SortedMapKeys(ss.Airports) {
		if t.expanded[name] {
			in.drawAirportDetail(a, name)
		}
	}
}

// drawAirportDetail is what the scenario and the aviation database say about
// one airport, drawn below the table when its row is expanded.
func (in *inspector) drawAirportDetail(a *app, icao av.ICAOAirportCode) {
	ap := a.cc.State.Airports[icao]

	imgui.PushIDStr(string(icao))
	defer imgui.PopID()

	title := string(icao)
	if dbap, ok := av.DB.Airports[icao]; ok && dbap.Name != "" {
		title += " - " + dbap.Name
	}
	imgui.SeparatorText(title)

	section := func(label string, open bool, draw func()) {
		var flags imgui.TreeNodeFlags
		if open {
			flags = imgui.TreeNodeFlagsDefaultOpen
		}
		if imgui.CollapsingHeaderTreeNodeFlagsV(label, flags) {
			draw()
		}
	}

	section("Configuration", true, func() { in.drawAirportConfiguration(a, icao, ap) })
	section("Active runways", true, func() { in.drawAirportRunways(a, icao) })
	section("Approaches", true, func() { in.drawAirportApproaches(a, icao, ap) })
	section("Departure exits", true, func() { in.drawAirportExits(a, icao, ap) })
	section("Arrivals", true, func() { in.drawAirportArrivals(a, icao) })
	section("Published SIDs and STARs", false, func() { in.drawAirportProcedures(a, icao, ap) })
	if len(ap.Departures) > 0 {
		section("Departure destinations", false, func() { in.drawAirportDestinations(a, ap) })
	}
}

func (in *inspector) drawAirportConfiguration(a *app, icao av.ICAOAirportCode, ap *av.Airport) {
	ss := &a.cc.State

	if !imgui.BeginTableV("cfg", 2, tableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()
	imgui.TableSetupColumnV("Item", imgui.TableColumnFlagsWidthFixed, 170, 0)
	imgui.TableSetupColumn("Value")
	imgui.TableHeadersRow()

	label := func(k string) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(k)
		imgui.TableNextColumn()
	}
	// Rows with nothing to say are left out rather than shown empty; most of
	// an airport's settings are unset in most scenarios.
	row := func(k, v string) {
		if v != "" {
			label(k)
			imgui.TextWrapped(v)
		}
	}

	label("Location")
	in.locationCell(a, "apt", ap.Location)

	if dbap, ok := av.DB.Airports[icao]; ok {
		row("FAA id", string(dbap.LocalCode))
		row("Elevation", fmt.Sprintf("%d ft", dbap.Elevation))
		row("ARTCC", dbap.ARTCC)
		row("Runways", dbap.ValidRunways())
	}

	row("IFR departure rate", rateText(departureRate(ss, icao)))
	row("VFR departure rate", rateText(ss.LaunchConfig.VFRAirportRates[icao]))
	row("Arrival rate", rateText(arrivalRate(ss, icao)))
	if rwy, ok := ss.VFRRunways[icao]; ok {
		row("VFR runway", rwy.Id)
	}
	if ap.VFR.Randoms.Rate > 0 {
		row("VFR random routes", rateText(ap.VFR.Randoms.Rate)+fleetText(ap.VFR.Randoms.Fleet))
	}
	row("VFR routes", strings.Join(util.MapSlice(ap.VFR.Routes,
		func(r av.VFRRouteSpec) string { return r.Name }), ", "))

	if ap.TowerListIndex != 0 {
		row("Tower list", fmt.Sprint(ap.TowerListIndex))
	}
	row("Departure controller", string(ap.DepartureController))
	row("Hold for release", util.Select(ap.HoldForRelease, "yes", ""))
	row("Departure runways as one", strings.Join(ap.DepartureRunwaysAsOne, ", "))
	row("Omit arrival scratchpad", util.Select(ap.OmitArrivalScratchpad, "yes", ""))
	if ap.PrintDepartureStrips != nil {
		row("Print departure strips", util.Select(*ap.PrintDepartureStrips, "yes", "no"))
	}
	if ap.PrintArrivalStrips != nil {
		row("Print arrival strips", util.Select(*ap.PrintArrivalStrips, "yes", "no"))
	}
	row("Exit categories", strings.Join(exitCategories(ap), ", "))
	row("ATPA volumes", strings.Join(util.SortedMapKeys(ap.ATPAVolumes), ", "))
	if n := len(ap.CRDARegions); n > 0 {
		row("CRDA", fmt.Sprintf("%d regions, %d pairs", n, len(ap.CRDAPairs)))
	}
	if n := len(ap.TrafficRoutes.Departures) + len(ap.TrafficRoutes.Arrivals); n > 0 {
		row("Traffic routes", fmt.Sprintf("%d departure, %d arrival",
			len(ap.TrafficRoutes.Departures), len(ap.TrafficRoutes.Arrivals)))
	}
}

// drawAirportRunways lists the runways the scenario is working: the ones it
// departs from, lands on, or both.
func (in *inspector) drawAirportRunways(a *app, icao av.ICAOAirportCode) {
	ss := &a.cc.State

	type runwayUse struct {
		departures []string
		arrival    bool
		goAround   *sim.GoAroundProcedure
	}
	use := make(map[av.RunwayID]*runwayUse)
	get := func(id av.RunwayID) *runwayUse {
		if u, ok := use[id]; ok {
			return u
		}
		u := &runwayUse{}
		use[id] = u
		return u
	}
	for _, dep := range ss.DepartureRunways {
		if dep.Airport != icao {
			continue
		}
		u := get(dep.Runway)
		rate := rateText(ss.LaunchConfig.DepartureRates[icao][dep.Runway][dep.Category])
		if rate == "" {
			rate = "no traffic"
		}
		u.departures = append(u.departures, strings.TrimSpace(dep.Category+" "+rate))
	}
	for _, arr := range ss.ArrivalRunways {
		if arr.Airport != icao {
			continue
		}
		u := get(arr.Runway)
		u.arrival, u.goAround = true, arr.GoAround
	}

	if len(use) == 0 {
		imgui.Text("The scenario works no runways at this airport.")
		return
	}

	flags, size := tableSize(len(use), 10)
	if !imgui.BeginTableV("rwys", 6, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Departures", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableSetupColumnV("Arrivals", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Heading", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Threshold", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Go around", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableHeadersRow()

	for id, u := range util.SortedMap(use) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(string(id))
		imgui.TableNextColumn()
		imgui.TextWrapped(strings.Join(u.departures, ", "))
		imgui.TableNextColumn()
		imgui.Text(util.Select(u.arrival, "yes", ""))
		imgui.TableNextColumn()
		rwy, ok := av.LookupRunway(icao, id.Base())
		if ok {
			imgui.Text(fmt.Sprintf("%03.0f", float32(rwy.Heading)))
		}
		imgui.TableNextColumn()
		if ok {
			imgui.Text(fmt.Sprintf("%d ft", rwy.Elevation))
		}
		imgui.TableNextColumn()
		imgui.TextWrapped(goAroundText(u.goAround))
	}
}

// drawAirportApproaches lists the approaches to the runways the scenario is
// landing on, one row per transition so that each route can be read and
// copied on its own.
func (in *inspector) drawAirportApproaches(a *app, icao av.ICAOAirportCode, ap *av.Airport) {
	ss := &a.cc.State
	r := &in.routes

	type approachRow struct {
		runway string
		name   string
		appr   *av.Approach
	}
	var rows []approachRow
	nroutes := 0
	for _, arr := range ss.ArrivalRunways {
		if arr.Airport != icao {
			continue
		}
		base := arr.Runway.Base()
		for name, appr := range util.SortedMap(ap.Approaches) {
			if appr.Runway != base ||
				slices.ContainsFunc(rows, func(r approachRow) bool { return r.name == name }) {
				continue
			}
			rows = append(rows, approachRow{runway: base, name: name, appr: appr})
			nroutes += len(appr.Waypoints)
		}
	}

	if len(rows) == 0 {
		imgui.Text("No approaches are defined for the runways the scenario is landing on.")
		return
	}
	if r.approaches[icao] == nil {
		r.approaches[icao] = make(map[string]bool)
	}

	flags, size := tableSize(nroutes, 12)
	if !imgui.BeginTableV("appr", 6, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	setupDrawColumn()
	imgui.TableSetupColumnV("Code", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Type", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("FAF", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumn("Route")
	imgui.TableHeadersRow()

	for _, row := range rows {
		imgui.PushIDStr(row.name)
		// The checkbox draws the whole approach, so it goes on the first of
		// its transitions and the columns that name it aren't repeated.
		for i, wps := range row.appr.Waypoints {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			if i == 0 {
				enabled := r.approaches[icao][row.name]
				imgui.Checkbox("##draw", &enabled)
				r.approaches[icao][row.name] = enabled
				imgui.TableNextColumn()
				imgui.Text(row.name)
				itemTooltip(row.appr.FullName)
				imgui.TableNextColumn()
				imgui.Text(row.runway)
				imgui.TableNextColumn()
				imgui.Text(row.appr.Type.String())
			} else {
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				imgui.TableNextColumn()
			}
			imgui.TableNextColumn()
			imgui.Text(fafFix(wps))
			imgui.TableNextColumn()
			in.routeCell(a, fmt.Sprint(i), wps.Encode())
		}
		imgui.PopID()
	}
}

// drawAirportExits lists the exits departures leave by from the runways the
// scenario is departing, with the route each one flies.
func (in *inspector) drawAirportExits(a *app, icao av.ICAOAirportCode, ap *av.Airport) {
	ss := &a.cc.State
	r := &in.routes

	routes := slices.Collect(radar.ScenarioDepartureRoutes(ap, ss.LaunchConfig.DepartureRates[icao]))
	if len(routes) == 0 {
		imgui.Text("The scenario flies no departures from this airport.")
		return
	}
	if r.departures[icao] == nil {
		r.departures[icao] = make(map[string]map[string]bool)
	}

	flags, size := tableSize(len(routes), 12)
	if !imgui.BeginTableV("exits", 7, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	setupDrawColumn()
	imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Exit", imgui.TableColumnFlagsWidthFixed, 65, 0)
	imgui.TableSetupColumnV("SID", imgui.TableColumnFlagsWidthFixed, 65, 0)
	imgui.TableSetupColumnV("Assigned", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumnV("Cleared", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumn("Route")
	imgui.TableHeadersRow()

	for i, dr := range routes {
		rwy, exit := string(dr.Runway), string(dr.Exit)
		if r.departures[icao][rwy] == nil {
			r.departures[icao][rwy] = make(map[string]bool)
		}
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		enabled := r.departures[icao][rwy][exit]
		imgui.Checkbox("##draw", &enabled)
		r.departures[icao][rwy][exit] = enabled
		imgui.TableNextColumn()
		imgui.Text(rwy)
		imgui.TableNextColumn()
		imgui.Text(exit)
		itemTooltip(exitTooltip(ss, icao, ap, dr))
		imgui.TableNextColumn()
		imgui.Text(dr.Route.SID)
		imgui.TableNextColumn()
		imgui.Text(altitudeText(dr.Route.AssignedAltitude))
		imgui.TableNextColumn()
		imgui.Text(altitudeText(dr.Route.ClearedAltitude))
		imgui.TableNextColumn()
		in.routeCell(a, "route", dr.Route.Waypoints.Encode())
		imgui.PopID()
	}
}

// drawAirportArrivals lists the scenario's arrivals that bring traffic here,
// with the route each flies and the runway transitions it has for this
// airport.
func (in *inspector) drawAirportArrivals(a *app, icao av.ICAOAirportCode) {
	ss := &a.cc.State
	r := &in.routes

	type arrivalRow struct {
		flow  string
		index int
		arr   av.Arrival
	}
	var rows []arrivalRow
	for name, flow := range util.SortedMap(ss.InboundFlows) {
		if r.arrivals[name] == nil && len(flow.Arrivals) > 0 {
			r.arrivals[name] = make(map[int]bool)
		}
		for i, arr := range flow.Arrivals {
			if slices.Contains(arr.Airports, icao) {
				rows = append(rows, arrivalRow{flow: name, index: i, arr: arr})
			}
		}
	}

	if len(rows) == 0 {
		imgui.Text("No inbound flow brings traffic to this airport.")
		return
	}

	flags, size := tableSize(len(rows), 12)
	if !imgui.BeginTableV("arr", 6, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	setupDrawColumn()
	imgui.TableSetupColumnV("Flow", imgui.TableColumnFlagsWidthFixed, 90, 0)
	imgui.TableSetupColumnV("STAR", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Rate", imgui.TableColumnFlagsWidthFixed, 45, 0)
	setupBackgroundColumn()
	imgui.TableSetupColumn("Route")
	imgui.TableHeadersRow()

	for _, row := range rows {
		imgui.PushIDInt(int32(row.index))
		imgui.PushIDStr(row.flow)
		imgui.TableNextRow()
		imgui.TableNextColumn()
		enabled := r.arrivals[row.flow][row.index]
		imgui.Checkbox("##draw", &enabled)
		r.arrivals[row.flow][row.index] = enabled
		imgui.TableNextColumn()
		imgui.Text(row.flow)
		itemTooltip(row.arr.Description)
		imgui.TableNextColumn()
		imgui.Text(row.arr.STAR)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("%.0f", ss.LaunchConfig.InboundFlowRates[row.flow][string(icao)]))
		imgui.TableNextColumn()
		imgui.Text(backgroundText(arrivalIsBackground(ss, row.flow, row.arr)))
		imgui.TableNextColumn()
		in.routeCell(a, "main", row.arr.Waypoints.Encode())
		for rwy, wps := range util.SortedMap(row.arr.RunwayWaypoints[icao]) {
			imgui.Text(rwy + ":")
			imgui.SameLine()
			in.routeCell(a, rwy, wps.Encode())
		}
		imgui.PopID()
		imgui.PopID()
	}
}

// drawAirportProcedures lists the SIDs and STARs the FAA CIFP charts for the
// airport, one row per transition, so that what the scenario flies can be
// compared against what is published.
func (in *inspector) drawAirportProcedures(a *app, icao av.ICAOAirportCode, ap *av.Airport) {
	dbap, ok := av.DB.Airports[icao]
	if !ok {
		imgui.Text(string(icao) + " is not in the CIFP.")
		return
	}

	t := &in.airports
	flown := flownProcedures(&a.cc.State, icao, ap)

	type procedureRow struct {
		kind  string
		flown bool
		route av.CIFPRoute
	}
	var rows []procedureRow
	add := func(kind, name string, routes []av.CIFPRoute) {
		isFlown := flown[av.ProcedureBase(name)]
		if t.flownProceduresOnly && !isFlown {
			return
		}
		for _, r := range routes {
			rows = append(rows, procedureRow{kind: kind, flown: isFlown, route: r})
		}
	}
	for name, sid := range util.SortedMap(dbap.SIDs) {
		add("SID", name, sid.Routes(name))
	}
	for name, star := range util.SortedMap(dbap.STARs) {
		add("STAR", name, star.Routes(name))
	}

	imgui.Checkbox("Only the procedures the scenario flies", &t.flownProceduresOnly)
	if len(rows) == 0 {
		imgui.Text("Nothing to show.")
		return
	}

	flags, size := tableSize(len(rows), 12)
	if !imgui.BeginTableV("cifp", 5, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	setupDrawColumn()
	imgui.TableSetupColumnV("Kind", imgui.TableColumnFlagsWidthFixed, 40, 0)
	imgui.TableSetupColumnV("Procedure", imgui.TableColumnFlagsWidthFixed, 110, 0)
	imgui.TableSetupColumnV("Flown", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumn("Route")
	imgui.TableHeadersRow()

	for i, row := range rows {
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		drawToggle(in.cifp.draw, string(icao)+" "+row.route.Name,
			func() av.WaypointArray { return locateRoute(a, row.route.Waypoints) })
		imgui.TableNextColumn()
		imgui.Text(row.kind)
		imgui.TableNextColumn()
		imgui.Text(row.route.Name)
		imgui.TableNextColumn()
		imgui.Text(util.Select(row.flown, "yes", ""))
		imgui.TableNextColumn()
		in.routeCell(a, "route", row.route.Route)
		imgui.PopID()
	}
}

// drawAirportDestinations lists the airport's departures: the route the
// scenario files for each destination it sends traffic to.
func (in *inspector) drawAirportDestinations(a *app, ap *av.Airport) {
	flags, size := tableSize(len(ap.Departures), 12)
	if !imgui.BeginTableV("dests", 4, flags, size, 0) {
		return
	}
	defer imgui.EndTable()
	imgui.TableSetupColumnV("Exit", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Destination", imgui.TableColumnFlagsWidthFixed, 80, 0)
	imgui.TableSetupColumnV("Altitude", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumn("Route")
	imgui.TableHeadersRow()

	for i, dep := range ap.Departures {
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(string(dep.Exit))
		imgui.TableNextColumn()
		imgui.Text(string(dep.Destination))
		itemTooltip(dep.Description)
		imgui.TableNextColumn()
		imgui.Text(strings.Join(util.MapSlice(dep.Altitudes, func(alt int) string { return fmt.Sprint(alt) }), ", "))
		imgui.TableNextColumn()
		in.routeCell(a, "route", dep.Route)
		imgui.PopID()
	}
}

// routeCell draws a route with a button that copies it, which is the form it
// takes in a scenario's JSON.
func (in *inspector) routeCell(a *app, id, route string) {
	if route == "" {
		return
	}
	a.copyButton(id, route)
	imgui.SameLine()
	imgui.TextWrapped(route)
}

// itemTooltip shows text describing the item just drawn. imgui takes the text
// as a printf format, so a percent sign in what a scenario says has to be
// escaped rather than read as a conversion.
func itemTooltip(s string) {
	if s != "" {
		imgui.SetItemTooltip(strings.ReplaceAll(s, "%", "%%"))
	}
}

// flownProcedures are the SIDs and STARs the scenario's own routes name at
// the airport, keyed by procedure name without its revision number: a
// scenario carries the revision the CIFP charted when it was written.
func flownProcedures(ss *client.SimState, icao av.ICAOAirportCode, ap *av.Airport) map[string]bool {
	flown := make(map[string]bool)
	for _, exitRoutes := range ap.DepartureRoutes {
		for _, routes := range exitRoutes {
			for _, er := range routes {
				if sid, _, _ := strings.Cut(er.SID, "."); sid != "" {
					flown[av.ProcedureBase(sid)] = true
				}
			}
		}
	}
	for _, flow := range ss.InboundFlows {
		for _, arr := range flow.Arrivals {
			if !slices.Contains(arr.Airports, icao) {
				continue
			}
			for _, star := range append([]string{arr.STAR}, arr.STARFeeds...) {
				if star != "" {
					flown[av.ProcedureBase(star)] = true
				}
			}
		}
	}
	return flown
}

// exitTooltip is the rest of what a departure route says: the settings that
// are set rarely enough that a column for each would leave the table mostly
// empty.
func exitTooltip(ss *client.SimState, icao av.ICAOAirportCode, ap *av.Airport, dr radar.DepartureRoute) string {
	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, k+": "+v)
		}
	}
	add("Category", ap.ExitCategory(dr.Exit))
	add("Description", dr.Route.Description)
	if dr.Route.Aircraft != 0 {
		add("Aircraft", dr.Route.Aircraft.String())
	}
	if dr.Route.InitialHeading != 0 {
		add("Initial heading", fmt.Sprintf("%03d", dr.Route.InitialHeading))
	}
	add("Climbout", dr.Route.ClimboutActions)
	add("Departure controller", string(dr.Route.DepartureController))
	add("Handoff controller", string(dr.Route.HandoffController))
	if dr.Route.IsRNAV {
		add("RNAV", "yes")
	}
	if dr.Route.HoldForRelease {
		add("Hold for release", "yes")
	}
	if departureIsBackground(ss, icao, dr.Runway, dr.Exit) {
		add("Background", "yes")
	}
	return strings.Join(lines, "\n")
}

// departureRate is the rate at which the scenario launches IFR departures
// from the airport, over all of its runways and departure categories.
func departureRate(ss *client.SimState, icao av.ICAOAirportCode) float32 {
	var rate float32
	for _, categories := range ss.LaunchConfig.DepartureRates[icao] {
		for _, r := range categories {
			rate += r
		}
	}
	return rate
}

// arrivalRate is the rate at which the scenario's inbound flows bring traffic
// to the airport.
func arrivalRate(ss *client.SimState, icao av.ICAOAirportCode) float32 {
	var rate float32
	for _, airports := range ss.LaunchConfig.InboundFlowRates {
		rate += airports[string(icao)]
	}
	return rate
}

func rateText(rate float32) string {
	if rate == 0 {
		return ""
	}
	return fmt.Sprintf("%.0f/hour", rate)
}

func fleetText(fleet string) string {
	if fleet == "" {
		return ""
	}
	return " (" + fleet + " fleet)"
}

func altitudeText(alt int) string {
	if alt == 0 {
		return ""
	}
	return fmt.Sprint(alt)
}

func goAroundText(g *sim.GoAroundProcedure) string {
	if g == nil {
		return ""
	}
	parts := []string{util.Select(g.IsRunwayHeading, "runway heading", fmt.Sprintf("%03d", g.Heading))}
	if g.Altitude != 0 {
		parts = append(parts, fmt.Sprint(g.Altitude))
	}
	if g.HandoffController != "" {
		parts = append(parts, "handoff "+string(g.HandoffController))
	}
	if len(g.HoldDepartures) > 0 {
		parts = append(parts, "hold "+strings.Join(g.HoldDepartures, "/"))
	}
	return strings.Join(parts, ", ")
}

func fafFix(wps av.WaypointArray) string {
	for _, wp := range wps {
		if wp.FAF() {
			return wp.Fix
		}
	}
	return ""
}

// exitCategories are the distinct departure categories the airport's exits
// are sorted into; the scenario's departure rates are keyed by them.
func exitCategories(ap *av.Airport) []string {
	var categories []string
	for _, c := range ap.ExitCategories {
		if c != "" && !slices.Contains(categories, c) {
			categories = append(categories, c)
		}
	}
	slices.Sort(categories)
	return categories
}

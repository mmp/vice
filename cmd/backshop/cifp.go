// cmd/backshop/cifp.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// cifpTab shows what `vice -routes` prints: the SIDs, STARs, and approaches
// the FAA CIFP codes for an airport, in the encoded form that goes into a
// scenario's JSON.
type cifpTab struct {
	airport string
	filter  string

	// draw holds the routes checked in the table, keyed by their row, with
	// their waypoints located so that they can be drawn.
	draw  map[string]av.WaypointArray
	color [3]float32
}

func (c *cifpTab) init() {
	c.draw = make(map[string]av.WaypointArray)
	c.color = [3]float32{.95, .55, .55}
}

// drawCIFPProcedures draws the procedures checked in the CIFP tab. They are
// drawn separately from the scenario's own routes: the point of showing them
// is to compare the two.
func (in *inspector) drawCIFPProcedures(a *app, transforms scope.ScopeTransformations, cb *renderer.CommandBuffer) {
	c := &in.cifp
	if len(c.draw) == 0 {
		return
	}

	ss := &a.cc.State
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	pd := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(pd)
	ldr := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ldr)

	color := rgb(c.color)
	style := renderer.TextStyle{Font: a.scope.textFont, Color: color, DrawBackground: true}
	drawn := scope.NewDrawnRoutes()
	for _, wps := range util.SortedMap(c.draw) {
		scope.DrawWaypoints(ss.NmPerLongitude, ss.MagneticVariation, wps, scope.RouteDrawContext{}, drawn,
			transforms, td, style, ld, pd, ldr, color)
	}

	scope.GenerateRouteDrawingCommands(cb, transforms, a.plat.DPIScale(), ld, pd, td, ldr)
}

// locateRoute returns a copy of a procedure's waypoints with their positions
// filled in. The copy is what keeps the database's own waypoints, which the
// rest of vice shares, from acquiring this sim's geometry.
func locateRoute(a *app, wps av.WaypointArray) av.WaypointArray {
	if a.cc == nil {
		return nil
	}
	ss := &a.cc.State
	var scratch util.ErrorLogger
	return wps.Clone().InitializeLocations(enroute.DBLocator{}, ss.NmPerLongitude,
		ss.MagneticVariation, true, &scratch)
}

func (in *inspector) drawCIFPTab(a *app) {
	if !imgui.BeginTabItem("CIFP") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	c := &in.cifp
	airports := ifrAirports(&a.cc.State)
	if !slices.Contains(airports, av.ICAOAirportCode(c.airport)) {
		c.airport = ""
		if len(airports) > 0 {
			c.airport = string(airports[0])
		}
	}

	imgui.SetNextItemWidth(120)
	if imgui.BeginCombo("##airport", c.airport) {
		for _, ap := range airports {
			if imgui.SelectableBool(string(ap)) {
				c.airport = string(ap)
			}
		}
		imgui.EndCombo()
	}
	imgui.SameLine()
	imgui.SetNextItemWidth(160)
	imgui.InputTextWithHint("##cifpfilter", "filter", &c.filter, 0, nil)

	icao := c.airport
	ap, ok := av.DB.Airports[av.ICAOAirportCode(icao)]
	if !ok {
		if icao != "" {
			imgui.Text(icao + ": not in the CIFP")
		}
		return
	}

	var rows []av.CIFPRoute
	for name, sid := range util.SortedMap(ap.SIDs) {
		rows = append(rows, sid.Routes(name)...)
	}
	sidCount := len(rows)
	for name, star := range util.SortedMap(ap.STARs) {
		rows = append(rows, star.Routes(name)...)
	}
	starEnd := len(rows)
	for name, appr := range util.SortedMap(ap.Approaches) {
		for _, wp := range appr.Waypoints {
			rows = append(rows, av.CIFPRoute{Name: name, Route: wp.Encode(), Waypoints: wp})
		}
	}

	kind := func(i int) string {
		switch {
		case i < sidCount:
			return "SID"
		case i < starEnd:
			return "STAR"
		default:
			return "APPR"
		}
	}

	imgui.Text("Color:")
	imgui.SameLine()
	imgui.ColorEdit3V("##cifpcolor", &c.color, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)
	imgui.SameLine()
	if imgui.SmallButton("Draw none") {
		clear(c.draw)
	}
	imgui.SameLine()
	imgui.Text(fmt.Sprintf("%d drawn", len(c.draw)))

	filter := strings.ToUpper(c.filter)
	if !imgui.BeginTableV("cifp", 5, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Kind", imgui.TableColumnFlagsWidthFixed, 40, 0)
	imgui.TableSetupColumnV("Name", imgui.TableColumnFlagsWidthFixed, 110, 0)
	imgui.TableSetupColumn("Route")
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableHeadersRow()

	for i, r := range rows {
		if filter != "" && !strings.Contains(strings.ToUpper(r.Name), filter) &&
			!strings.Contains(strings.ToUpper(r.Route), filter) {
			continue
		}
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		drawToggle(c.draw, fmt.Sprintf("%s/%d", icao, i), func() av.WaypointArray { return locateRoute(a, r.Waypoints) })
		imgui.TableNextColumn()
		imgui.Text(kind(i))
		imgui.TableNextColumn()
		imgui.Text(r.Name)
		imgui.TableNextColumn()
		imgui.TextWrapped(r.Route)
		imgui.TableNextColumn()
		a.copyButton(r.Name, r.Route)
		imgui.PopID()
	}
	imgui.EndTable()
}

// ifrAirports are the scenario's airports with IFR traffic: the ones it
// launches IFR departures from and the ones its inbound flows land at. They
// are the airports whose charted procedures are worth comparing a scenario's
// routes against.
func ifrAirports(ss *client.SimState) []av.ICAOAirportCode {
	airports := make(map[av.ICAOAirportCode]any)
	for ap := range ss.LaunchConfig.DepartureRates {
		airports[ap] = nil
	}
	for ap := range ss.ArrivalAirports {
		airports[ap] = nil
	}
	return util.SortedMapKeys(airports)
}

func (in *inspector) drawBriefTab(a *app) {
	if !imgui.BeginTabItem("Brief") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}
	if a.cc.State.ScenarioBrief == "" {
		imgui.Text("This facility has no brief.")
		return
	}

	if imgui.Button("Reload brief") {
		if err := a.cc.ReloadScenarioBrief(); err != nil {
			a.status = "brief reload failed: " + err.Error()
		} else {
			in.brief.Invalidate()
		}
	}
	imgui.Separator()

	in.brief.Draw(a.cc.State.ScenarioBrief, a.plat, a.config.UIFontSize, &a.cc.State, a.cc, a.lg)
}

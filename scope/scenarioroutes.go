// scope/scenarioroutes.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// TableFlags makes big(ish) tables in the scenario info window somewhat more
// legible.
const TableFlags = imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
	imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

// RouteColors are the colors the scenario's routes are drawn in, as chosen
// in the scenario info window.
type RouteColors struct {
	Arrivals, Approaches, Departures, Overflights, Airspace, Holds [3]float32
}

// RouteDrawer draws the scenario info window's controls for choosing which
// of a scenario's arrivals, approaches, departures, overflights, airspace
// volumes, and charted holds the scope draws, records what the user has
// chosen, and draws it. Only the colors are saved with the scope's
// settings; the choices are made afresh for each scenario.
type RouteDrawer struct {
	Colors RouteColors

	arrivals    map[string]map[int]bool                        // inbound flow -> index
	approaches  map[av.ICAOAirportCode]map[string]bool         // airport -> approach
	departures  map[av.ICAOAirportCode]map[DepartureGroup]bool // airport -> group
	overflights map[string]map[int]bool                        // inbound flow -> index
	airspace    map[sim.ControlPosition]map[string]bool        // position -> volume
	// holds are the charted holds drawn on their own, by display name;
	// allHolds chooses every one within range.
	holds    map[string]av.Hold
	allHolds bool
}

// Activate gives the colors their defaults if none have been saved.
func (rd *RouteDrawer) Activate() {
	if rd.Colors == (RouteColors{}) {
		green := [3]float32{.1, .9, .1}
		rd.Colors = RouteColors{Arrivals: green, Approaches: green, Departures: green,
			Overflights: green, Airspace: green, Holds: [3]float32{.9, .9, .1}}
	}
}

// Clear forgets the selections; they are rebuilt from scratch for the next
// scenario.
func (rd *RouteDrawer) Clear() {
	rd.arrivals = nil
	rd.approaches = nil
	rd.departures = nil
	rd.overflights = nil
	rd.airspace = nil
	rd.holds = nil
}

// Empty reports whether nothing at all has been selected to draw.
func (rd *RouteDrawer) Empty() bool {
	return len(rd.arrivals) == 0 && len(rd.approaches) == 0 && len(rd.departures) == 0 &&
		len(rd.overflights) == 0 && len(rd.airspace) == 0 && len(rd.holds) == 0
}

// DrawUI draws the info window's sections for choosing the routes,
// approaches, and airspace volumes to draw.
func (rd *RouteDrawer) DrawUI(c *client.ControlClient, lg *log.Logger) {
	rd.drawArrivalsUI(c)
	rd.drawApproachesUI(c, lg)
	rd.drawDeparturesUI(c)
	rd.drawOverflightsUI(c)
	rd.drawAirspaceUI(c)
}

func drawColorPicker(id string, color *[3]float32) {
	imgui.Text("Color:")
	imgui.SameLine()
	imgui.ColorEdit3V(id, color, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)
}

// listWrapWidth is how many characters of a comma-separated list a row
// shows before continuing on another line; a SID with many transitions or
// an arrival serving many airports otherwise stretches the table far past
// the width of the window.
const listWrapWidth = 72

// joinWrapped joins the items with ", ", starting a new line whenever the
// one being built would grow past width characters. The comma stays at the
// end of the line it breaks after.
func joinWrapped[S ~string](items []S, width int) string {
	var b strings.Builder
	line := 0
	for i, item := range items {
		switch {
		case i == 0:
		case line+len(item)+2 > width:
			b.WriteString(",\n")
			line = 0
		default:
			b.WriteString(", ")
			line += 2
		}
		b.WriteString(string(item))
		line += len(item)
	}
	return b.String()
}

// tableColumn is a column of one of the scenario info window's tables.
type tableColumn[T any] struct {
	name string
	text func(T) string
}

// drawSelectionTable draws a table of rows, each led by a checkbox that
// selects it for drawing. A column that no row fills in is left out
// altogether--scenarios that give no aircraft class or description have no
// use for those columns--while a row that leaves one blank that others fill
// in gets dashes.
func drawSelectionTable[T any](id string, rows []T, check func(T), cols ...tableColumn[T]) {
	text := util.MapSlice(rows, func(r T) []string {
		return util.MapSlice(cols, func(c tableColumn[T]) string { return c.text(r) })
	})
	used := make([]bool, len(cols))
	n := int32(1)
	for i := range cols {
		used[i] = slices.ContainsFunc(text, func(t []string) bool { return t[i] != "" })
		if used[i] {
			n++
		}
	}

	if !imgui.BeginTableV(id, n, TableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()

	imgui.TableSetupColumn("Draw")
	for i, c := range cols {
		if used[i] {
			imgui.TableSetupColumn(c.name)
		}
	}
	imgui.TableHeadersRow()

	for i, r := range rows {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		check(r)

		for j := range cols {
			if !used[j] {
				continue
			}
			imgui.TableNextColumn()
			imgui.Text(util.Select(text[i][j] == "", "--", text[i][j]))
		}
	}
}

func (rd *RouteDrawer) drawArrivalsUI(c *client.ControlClient) {
	if !imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		return
	}
	drawColorPicker("Draw Color##1", &rd.Colors.Arrivals)

	if rd.arrivals == nil {
		rd.arrivals = make(map[string]map[int]bool)
	}

	type row struct {
		flow  string
		index int
		arr   *av.Arrival
	}
	var rows []row
	for name, flow := range util.SortedMap(c.State.InboundFlows) {
		if len(flow.Arrivals) == 0 || len(c.State.LaunchConfig.InboundFlowRates[name]) == 0 {
			// Not used in the current scenario.
			continue
		}
		if rd.arrivals[name] == nil {
			rd.arrivals[name] = make(map[int]bool)
		}
		for i := range flow.Arrivals {
			rows = append(rows, row{flow: name, index: i, arr: &flow.Arrivals[i]})
		}
	}

	drawSelectionTable("arr", rows,
		func(r row) {
			enabled := rd.arrivals[r.flow][r.index]
			imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", r.flow, r.index), &enabled)
			rd.arrivals[r.flow][r.index] = enabled
		},
		tableColumn[row]{"Arrival", func(r row) string { return r.flow }},
		tableColumn[row]{"Airport(s)", func(r row) string { return joinWrapped(r.arr.Airports, listWrapWidth) }},
		tableColumn[row]{"Aircraft", func(r row) string { return r.arr.Aircraft.String() }},
		tableColumn[row]{"Description", func(r row) string { return r.arr.Description }})
}

func (rd *RouteDrawer) drawApproachesUI(c *client.ControlClient, lg *log.Logger) {
	if !imgui.CollapsingHeaderBoolPtr("Approaches", nil) {
		return
	}
	drawColorPicker("Draw Color##2", &rd.Colors.Approaches)

	if rd.approaches == nil {
		rd.approaches = make(map[av.ICAOAirportCode]map[string]bool)
	}

	type row struct {
		airport av.ICAOAirportCode
		runway  av.RunwayID
		name    string
		appr    *av.Approach
	}
	var rows []row
	for _, rwy := range c.State.ArrivalRunways {
		ap, ok := c.State.Airports[rwy.Airport]
		if !ok {
			lg.Errorf("%s: arrival airport not in world airports", rwy.Airport)
			continue
		}
		if rd.approaches[rwy.Airport] == nil {
			rd.approaches[rwy.Airport] = make(map[string]bool)
		}
		for name, appr := range util.SortedMap(ap.Approaches) {
			if appr.Runway == rwy.Runway.Base() {
				rows = append(rows, row{airport: rwy.Airport, runway: rwy.Runway, name: name, appr: appr})
			}
		}
	}

	drawSelectionTable("appr", rows,
		func(r row) {
			enabled := rd.approaches[r.airport][r.name]
			imgui.Checkbox("##enable-"+string(r.airport)+"-"+string(r.runway)+"-"+r.name, &enabled)
			rd.approaches[r.airport][r.name] = enabled
		},
		tableColumn[row]{"Airport", func(r row) string { return string(r.airport) }},
		tableColumn[row]{"Runway", func(r row) string { return string(r.runway) }},
		tableColumn[row]{"Code", func(r row) string { return r.name }},
		tableColumn[row]{"Description", func(r row) string { return r.appr.FullName }},
		tableColumn[row]{"FAF", func(r row) string {
			if i := slices.IndexFunc(r.appr.Waypoints[0], func(wp av.Waypoint) bool { return wp.FAF() }); i != -1 {
				return r.appr.Waypoints[0][i].Fix
			}
			return ""
		}})
}

///////////////////////////////////////////////////////////////////////////
// Departures

// departureRow is a group's row in the departures table.
type departureRow struct {
	airport      av.ICAOAirportCode
	group        DepartureGroup
	runways      []string
	exits        []string
	descriptions []string
}

// departureRows collects the airport's departure routes into the rows of the
// departures table, ordered by SID, then exit, then aircraft class.
func departureRows(icao av.ICAOAirportCode, ap *av.Airport, rates map[av.RunwayID]map[string]float32) []departureRow {
	var rows []departureRow
	for dr := range ScenarioDepartureRoutes(ap, rates) {
		i := slices.IndexFunc(rows, func(r departureRow) bool { return r.group == dr.Group })
		if i == -1 {
			rows = append(rows, departureRow{airport: icao, group: dr.Group})
			i = len(rows) - 1
		}
		row := &rows[i]
		if rwy := dr.Runway.Base(); !slices.Contains(row.runways, rwy) {
			row.runways = append(row.runways, rwy)
		}
		if !slices.Contains(row.exits, string(dr.Exit)) {
			row.exits = append(row.exits, string(dr.Exit))
		}
		if d := dr.Route.Description; d != "" && !slices.Contains(row.descriptions, d) {
			row.descriptions = append(row.descriptions, d)
		}
	}

	slices.SortFunc(rows, func(a, b departureRow) int {
		return cmp.Or(strings.Compare(a.group.SID, b.group.SID),
			strings.Compare(string(a.group.Exit), string(b.group.Exit)),
			strings.Compare(a.group.Aircraft.String(), b.group.Aircraft.String()))
	})
	return rows
}

func (rd *RouteDrawer) drawDeparturesUI(c *client.ControlClient) {
	if !imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		return
	}
	drawColorPicker("Draw Color##3", &rd.Colors.Departures)

	if rd.departures == nil {
		rd.departures = make(map[av.ICAOAirportCode]map[DepartureGroup]bool)
	}

	var rows []departureRow
	for icao, rates := range util.SortedMap(c.State.LaunchConfig.DepartureRates) {
		if rd.departures[icao] == nil {
			rd.departures[icao] = make(map[DepartureGroup]bool)
		}
		rows = append(rows, departureRows(icao, c.State.Airports[icao], rates)...)
	}

	drawSelectionTable("departures", rows,
		func(r departureRow) {
			enabled := rd.departures[r.airport][r.group]
			imgui.Checkbox("##enable-"+string(r.airport)+"-"+r.group.SID+"-"+string(r.group.Exit)+"-"+
				r.group.Aircraft.String(), &enabled)
			rd.departures[r.airport][r.group] = enabled
		},
		tableColumn[departureRow]{"Airport", func(r departureRow) string { return string(r.airport) }},
		tableColumn[departureRow]{"SID", func(r departureRow) string { return r.group.SID }},
		tableColumn[departureRow]{"Aircraft", func(r departureRow) string { return r.group.Aircraft.String() }},
		tableColumn[departureRow]{"Runways", func(r departureRow) string { return strings.Join(r.runways, ", ") }},
		tableColumn[departureRow]{"Exits", func(r departureRow) string { return joinWrapped(r.exits, listWrapWidth) }},
		tableColumn[departureRow]{"Description", func(r departureRow) string {
			return strings.Join(r.descriptions, " / ")
		}})
}

func (rd *RouteDrawer) drawOverflightsUI(c *client.ControlClient) {
	if !imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
		return
	}
	drawColorPicker("Draw Color##4", &rd.Colors.Overflights)

	if rd.overflights == nil {
		rd.overflights = make(map[string]map[int]bool)
	}

	type row struct {
		flow  string
		index int
		of    *av.Overflight
	}
	var rows []row
	for name, flow := range util.SortedMap(c.State.InboundFlows) {
		if _, ok := c.State.LaunchConfig.InboundFlowRates[name]["overflights"]; !ok || len(flow.Overflights) == 0 {
			// Not used in the current scenario.
			continue
		}
		if rd.overflights[name] == nil {
			rd.overflights[name] = make(map[int]bool)
		}
		for i := range flow.Overflights {
			rows = append(rows, row{flow: name, index: i, of: &flow.Overflights[i]})
		}
	}

	drawSelectionTable("over", rows,
		func(r row) {
			enabled := rd.overflights[r.flow][r.index]
			imgui.Checkbox(fmt.Sprintf("##of-%s-%d", r.flow, r.index), &enabled)
			rd.overflights[r.flow][r.index] = enabled
		},
		tableColumn[row]{"Overflight", func(r row) string { return r.flow }},
		tableColumn[row]{"Description", func(r row) string { return r.of.Description }})
}

func (rd *RouteDrawer) drawAirspaceUI(c *client.ControlClient) {
	if len(c.State.Airspace) == 0 || !imgui.CollapsingHeaderBoolPtr("Airspace", nil) {
		return
	}
	drawColorPicker("Draw Color##5", &rd.Colors.Airspace)

	if rd.airspace == nil {
		rd.airspace = make(map[sim.ControlPosition]map[string]bool)
		for ctrl, sectors := range c.State.Airspace {
			rd.airspace[ctrl] = make(map[string]bool)
			for _, sector := range util.SortedMapKeys(sectors) {
				rd.airspace[ctrl][sector] = false
			}
		}
	}

	for pos, vols := range util.SortedMap(rd.airspace) {
		hdr := string(pos)
		if ctrl, ok := c.State.Controllers[pos]; ok {
			hdr += " (" + ctrl.Position + ")"
		}
		if !imgui.TreeNodeExStr(hdr) {
			continue
		}
		if imgui.BeginTableV("volumes", 2, TableFlags, imgui.Vec2{}, 0) {
			for vol, b := range util.SortedMap(vols) {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				if imgui.Checkbox("##"+vol, &b) {
					vols[vol] = b
				}
				imgui.TableNextColumn()
				imgui.Text(vol)
			}

			imgui.EndTable()
		}
		imgui.TreePop()
	}
}

///////////////////////////////////////////////////////////////////////////
// Informational tables

func DrawTowerListsUI(c *client.ControlClient) {
	if !imgui.CollapsingHeaderBoolPtr("Tower/Coordination Lists", nil) {
		return
	}
	if !imgui.BeginTableV("tclists", 3, TableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()

	imgui.TableSetupColumn("Id")
	imgui.TableSetupColumn("Type")
	imgui.TableSetupColumn("Airports")
	imgui.TableHeadersRow()

	for i, ap := range c.TowerListAirports() {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(strconv.Itoa(i + 1))
		imgui.TableNextColumn()
		imgui.Text("Tower")
		imgui.TableNextColumn()
		imgui.Text(string(ap))
	}

	cl := util.DuplicateSlice(c.State.FacilityAdaptation.Lists.Coordination)
	slices.SortFunc(cl, func(a, b sim.CoordinationList) int { return strings.Compare(a.Id, b.Id) })

	for _, list := range cl {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(list.Id)
		imgui.TableNextColumn()
		imgui.Text("Coord. (" + list.Name + ")")
		imgui.TableNextColumn()
		imgui.Text(joinWrapped(list.Airports, 1000))
	}
}

func DrawAirspaceAwarenessUI(c *client.ControlClient) {
	userPos := c.State.PrimaryPositionForTCW(c.State.UserTCW)
	userArea := ""
	if ctrl, ok := c.State.Controllers[userPos]; ok {
		userArea = ctrl.Area
	}
	aa := c.State.FacilityAdaptation.AirspaceAwarenessForArea(userArea)
	if len(aa) == 0 || !imgui.CollapsingHeaderBoolPtr("Airspace Awareness", nil) {
		return
	}
	if !imgui.BeginTableV("awareness", 4, TableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()

	imgui.TableSetupColumn("Fix")
	imgui.TableSetupColumn("Altitude")
	imgui.TableSetupColumn("A/C Type")
	imgui.TableSetupColumn("Controller")
	imgui.TableHeadersRow()

	for _, aware := range aa {
		for _, fix := range aware.Fix {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(fix)
			imgui.TableNextColumn()
			alt := ""
			if aware.AltitudeRange[0] > 0 {
				if aware.AltitudeRange[1] < 60000 {
					alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + " - " +
						av.FormatAltitude(float32(aware.AltitudeRange[1]))
				} else {
					alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + "+"
				}
			} else if aware.AltitudeRange[1] < 60000 {
				alt = av.FormatAltitude(float32(aware.AltitudeRange[1])) + "-"
			}
			imgui.Text(alt)
			imgui.TableNextColumn()
			imgui.Text(strings.Join(aware.AircraftType, ", "))
			imgui.TableNextColumn()
			imgui.Text(aware.ReceivingController)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Holds

// DrawHoldsUI draws the info window's section for choosing charted holds
// to draw: the enroute holds within rangeNM of center that aren't part of
// a procedure, since those are drawn with their procedures.
func (rd *RouteDrawer) DrawHoldsUI(center math.Point2LL, rangeNM float32) {
	if !imgui.CollapsingHeaderBoolPtr("Holds", nil) {
		return
	}
	drawColorPicker("Draw Color##6", &rd.Colors.Holds)

	candidateHolds := make(map[string]av.Hold)
	for fix, holds := range util.SortedMap(db.DB.EnrouteHolds) {
		loc, _ := db.DB.LookupWaypoint(fix)
		if math.NMDistance2LL(center, loc) > rangeNM {
			continue
		}
		for _, h := range holds {
			if h.Procedure == "" {
				candidateHolds[h.DisplayName()] = h
			}
		}
	}

	if imgui.Checkbox("Draw all holds", &rd.allHolds) && !rd.allHolds {
		clear(rd.holds)
	}
	if rd.allHolds {
		rd.holds = candidateHolds
		imgui.BeginDisabled()
	}

	const ncol = 4
	if imgui.BeginTableV("holds", ncol, TableFlags, imgui.Vec2{}, 0) {
		if rd.holds == nil {
			rd.holds = make(map[string]av.Hold)
		}
		i := 0
		for name, hold := range util.SortedMap(candidateHolds) {
			if i%ncol == 0 {
				imgui.TableNextRow()
			}
			imgui.TableNextColumn()

			_, enabled := rd.holds[name]
			enabled = enabled || rd.allHolds
			if imgui.Checkbox(name+"##hold", &enabled) {
				if enabled {
					rd.holds[name] = hold
				} else {
					delete(rd.holds, name)
				}
			}
			i++
		}
		imgui.EndTable()
	}

	if rd.allHolds {
		imgui.EndDisabled()
	}
}

///////////////////////////////////////////////////////////////////////////
// Drawing

// routeBuilders are the draw builders the chosen routes are drawn with.
// They are shared so that routes drawn together don't repeat each other's
// waypoints and routes sharing legs stack their labels rather than drawing
// them over each other; likewise, a fix may have several charted holds, of
// which only the first is drawn.
type routeBuilders struct {
	drawn      *DrawnRoutes
	drawnHolds map[string]any
	td         *renderer.TextDrawBuilder
	ld         *renderer.ColoredLinesDrawBuilder
	pd         *renderer.ColoredTrianglesDrawBuilder // for circles
	ldr        *renderer.ColoredLinesDrawBuilder     // for restrictions, in window coordinates
}

// Draw draws the chosen routes, approaches, airspace volumes, and holds,
// with their labels in font and the airspace volumes' labels in
// airspaceFont.
func (rd *RouteDrawer) Draw(ctx *Context, transforms Transformations, font, airspaceFont *renderer.Font,
	cb *renderer.CommandBuffer) {
	if rd.Empty() {
		return
	}

	b := routeBuilders{
		drawn:      NewDrawnRoutes(),
		drawnHolds: make(map[string]any),
		td:         renderer.GetTextDrawBuilder(),
		ld:         renderer.GetColoredLinesDrawBuilder(),
		pd:         renderer.GetColoredTrianglesDrawBuilder(),
		ldr:        renderer.GetColoredLinesDrawBuilder(),
	}
	defer renderer.ReturnTextDrawBuilder(b.td)
	defer renderer.ReturnColoredLinesDrawBuilder(b.ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(b.pd)
	defer renderer.ReturnColoredLinesDrawBuilder(b.ldr)

	rd.drawArrivals(ctx, transforms, font, &b)
	rd.drawApproaches(ctx, transforms, font, &b)
	rd.drawDepartures(ctx, transforms, font, &b)
	rd.drawOverflights(ctx, transforms, font, &b)
	rd.drawAirspace(ctx, transforms, airspaceFont, &b)
	rd.drawHolds(ctx, transforms, font, &b)

	GenerateRouteDrawingCommands(cb, transforms, ctx.DPIScale, b.ld, b.pd, b.td, b.ldr)
}

// routeStyle returns the text style and line color for routes drawn in
// color.
func routeStyle(font *renderer.Font, color [3]float32) (renderer.TextStyle, renderer.RGB) {
	rgb := renderer.RGBFromArray(color)
	return renderer.TextStyle{Font: font, Color: rgb, DrawBackground: true}, rgb
}

func (rd *RouteDrawer) drawArrivals(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Arrivals)
	for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
		for i, arr := range flow.Arrivals {
			if !rd.arrivals[name][i] {
				continue
			}
			DrawWaypoints(ctx.NmPerLongitude, ctx.MagneticVariation, arr.Waypoints, ArrivalRouteContext(arr), b.drawn,
				transforms, b.td, style, b.ld, b.pd, b.ldr, color)
			SkipProcedureTurnHolds(arr.Waypoints, b.drawnHolds)

			if arr.STAR != "" {
				// The STAR's charted holds at the airports it serves, and
				// the enroute holds at its waypoints.
				for airport := range util.SortedMap(arr.RunwayWaypoints) {
					for _, holds := range db.DB.TerminalHolds[airport] {
						for _, h := range holds {
							if h.Procedure == arr.STAR {
								DrawHoldPattern(ctx.NmPerLongitude, ctx.MagneticVariation, transforms, h, color, b.td, b.ld,
									style, b.drawn, b.drawnHolds)
							}
						}
					}
				}
				DrawEnrouteHolds(ctx.NmPerLongitude, ctx.MagneticVariation, transforms, arr.Waypoints, arr.STAR, color,
					b.ld, b.td, style, b.drawn, b.drawnHolds)
			}

			for _, rwys := range util.SortedMap(arr.RunwayWaypoints) {
				for rwy, wp := range util.SortedMap(rwys) {
					DrawWaypoints(ctx.NmPerLongitude, ctx.MagneticVariation, wp, ArrivalRouteContext(arr), b.drawn,
						transforms, b.td, style, b.ld, b.pd, b.ldr, color)

					if len(wp) > 1 {
						// Draw the runway number in the middle of the line
						// between the first two waypoints.
						pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
						b.td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
					} else if h, ok := wp[0].HeadingAction(); ok {
						// This should be the only other case... The heading arrow is drawn
						// up to 3nm out, so put the runway 1nm along its axis.
						a := math.Radians(math.MagneticToTrue(math.MagneticHeading(h.Heading), ctx.MagneticVariation))
						v := math.SinCos(a)
						pend := math.LL2NM(wp[0].Location, ctx.NmPerLongitude)
						pend = math.Add2f(pend, v)
						pell := math.NM2LL(pend, ctx.NmPerLongitude)
						b.td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pell), style)
					}
				}
			}
		}
	}
}

func (rd *RouteDrawer) drawApproaches(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Approaches)
	for _, rwy := range ctx.Client.State.ArrivalRunways {
		if rd.approaches[rwy.Airport] == nil {
			continue
		}
		ap := ctx.Client.State.Airports[rwy.Airport]
		for name, appr := range util.SortedMap(ap.Approaches) {
			if appr.Runway != rwy.Runway.Base() || !rd.approaches[rwy.Airport][name] {
				continue
			}
			for _, wp := range appr.Waypoints {
				DrawWaypoints(ctx.NmPerLongitude, ctx.MagneticVariation, wp, ApproachRouteContext(appr), b.drawn,
					transforms, b.td, style, b.ld, b.pd, b.ldr, color)
				SkipProcedureTurnHolds(wp, b.drawnHolds)
			}
			DrawReportingPoints(appr, b.drawn, transforms, b.td, style, b.ldr, color)

			// The approach's charted holds, at its missed approach point,
			// with a dashed line to it from the airport, and the enroute
			// holds at its waypoints.
			for _, holds := range db.DB.TerminalHolds[rwy.Airport] {
				for _, h := range holds {
					if h.Procedure == name {
						DrawHoldPattern(ctx.NmPerLongitude, ctx.MagneticVariation, transforms, h, color, b.td, b.ld,
							style, b.drawn, b.drawnHolds)
						pMissed, _ := db.DB.LookupWaypoint(h.Fix)
						pAp := db.DB.Airports[rwy.Airport].Location
						b.ld.AddDashedLine(pAp, pMissed, .005, .0075, color)
					}
				}
			}
			for _, wp := range appr.Waypoints {
				DrawEnrouteHolds(ctx.NmPerLongitude, ctx.MagneticVariation, transforms, wp, name, color, b.ld, b.td,
					style, b.drawn, b.drawnHolds)
			}
		}
	}
}

func (rd *RouteDrawer) drawDepartures(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Departures)
	for icao, rates := range util.SortedMap(ctx.Client.State.LaunchConfig.DepartureRates) {
		if rd.departures[icao] == nil {
			continue
		}
		for dr := range ScenarioDepartureRoutes(ctx.Client.State.Airports[icao], rates) {
			if rd.departures[icao][dr.Group] {
				DrawWaypoints(ctx.NmPerLongitude, ctx.MagneticVariation, dr.Route.Waypoints,
					DepartureRouteContext(icao, dr.Route), b.drawn, transforms, b.td, style, b.ld, b.pd, b.ldr, color)
			}
		}
	}
}

func (rd *RouteDrawer) drawOverflights(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Overflights)
	for name, flow := range util.SortedMap(ctx.Client.State.InboundFlows) {
		for i, of := range flow.Overflights {
			if rd.overflights[name][i] {
				DrawWaypoints(ctx.NmPerLongitude, ctx.MagneticVariation, of.Waypoints, OverflightRouteContext(of),
					b.drawn, transforms, b.td, style, b.ld, b.pd, b.ldr, color)
			}
		}
	}
}

func (rd *RouteDrawer) drawAirspace(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Airspace)
	for pos, vols := range util.SortedMap(rd.airspace) {
		for volname, enabled := range util.SortedMap(vols) {
			if !enabled {
				continue
			}
			for _, vol := range ctx.Client.State.Airspace[pos][volname] {
				for _, pts := range vol.Boundaries {
					if len(pts) < 2 {
						continue
					}
					for i := range pts[:len(pts)-1] {
						b.ld.AddLine(pts[i].Point2LL, pts[i+1].Point2LL, color)
					}
				}
				b.td.AddTextCentered(strings.ToUpper(vol.Label), transforms.WindowFromLatLongP(vol.LabelPosition.Point2LL), style)
			}
		}
	}
}

func (rd *RouteDrawer) drawHolds(ctx *Context, transforms Transformations, font *renderer.Font, b *routeBuilders) {
	style, color := routeStyle(font, rd.Colors.Holds)
	for _, hold := range util.SortedMap(rd.holds) {
		DrawHoldPattern(ctx.NmPerLongitude, ctx.MagneticVariation, transforms, hold, color, b.td, b.ld, style,
			b.drawn, b.drawnHolds)
	}
}

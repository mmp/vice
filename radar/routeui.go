// radar/routeui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// TableFlags makes big(ish) tables in the scenario info window somewhat more
// legible.
const TableFlags = imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
	imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

// RouteDrawer draws the scenario info window's controls for choosing which
// of a scenario's arrivals, approaches, departures, overflights, and
// airspace volumes the scope draws, and records what the user has chosen.
type RouteDrawer struct {
	Arrivals    map[string]map[int]bool                 // inbound flow -> index
	Approaches  map[string]map[string]bool              // airport -> approach
	Departures  map[string]map[DepartureGroup]bool      // airport -> group
	Overflights map[string]map[int]bool                 // inbound flow -> index
	Airspace    map[sim.ControlPosition]map[string]bool // position -> volume
}

// Clear forgets the selections; they are rebuilt from scratch for the next
// scenario.
func (rd *RouteDrawer) Clear() {
	rd.Arrivals = nil
	rd.Approaches = nil
	rd.Departures = nil
	rd.Overflights = nil
	rd.Airspace = nil
}

// Empty reports whether nothing at all has been selected to draw.
func (rd *RouteDrawer) Empty() bool {
	return len(rd.Arrivals) == 0 && len(rd.Approaches) == 0 && len(rd.Departures) == 0 &&
		len(rd.Overflights) == 0 && len(rd.Airspace) == 0
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
func joinWrapped(items []string, width int) string {
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
		b.WriteString(item)
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

func (rd *RouteDrawer) DrawArrivalsUI(c *client.ControlClient, color *[3]float32) {
	if !imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		return
	}
	drawColorPicker("Draw Color##1", color)

	if rd.Arrivals == nil {
		rd.Arrivals = make(map[string]map[int]bool)
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
		if rd.Arrivals[name] == nil {
			rd.Arrivals[name] = make(map[int]bool)
		}
		for i := range flow.Arrivals {
			rows = append(rows, row{flow: name, index: i, arr: &flow.Arrivals[i]})
		}
	}

	drawSelectionTable("arr", rows,
		func(r row) {
			enabled := rd.Arrivals[r.flow][r.index]
			imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", r.flow, r.index), &enabled)
			rd.Arrivals[r.flow][r.index] = enabled
		},
		tableColumn[row]{"Arrival", func(r row) string { return r.flow }},
		tableColumn[row]{"Airport(s)", func(r row) string { return joinWrapped(r.arr.Airports, listWrapWidth) }},
		tableColumn[row]{"Aircraft", func(r row) string { return r.arr.Aircraft.String() }},
		tableColumn[row]{"Description", func(r row) string { return r.arr.Description }})
}

func (rd *RouteDrawer) DrawApproachesUI(c *client.ControlClient, color *[3]float32, lg *log.Logger) {
	if !imgui.CollapsingHeaderBoolPtr("Approaches", nil) {
		return
	}
	drawColorPicker("Draw Color##2", color)

	if rd.Approaches == nil {
		rd.Approaches = make(map[string]map[string]bool)
	}

	type row struct {
		airport string
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
		if rd.Approaches[rwy.Airport] == nil {
			rd.Approaches[rwy.Airport] = make(map[string]bool)
		}
		for name, appr := range util.SortedMap(ap.Approaches) {
			if appr.Runway == rwy.Runway.Base() {
				rows = append(rows, row{airport: rwy.Airport, runway: rwy.Runway, name: name, appr: appr})
			}
		}
	}

	drawSelectionTable("appr", rows,
		func(r row) {
			enabled := rd.Approaches[r.airport][r.name]
			imgui.Checkbox("##enable-"+r.airport+"-"+string(r.runway)+"-"+r.name, &enabled)
			rd.Approaches[r.airport][r.name] = enabled
		},
		tableColumn[row]{"Airport", func(r row) string { return r.airport }},
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
	airport      string
	group        DepartureGroup
	runways      []string
	exits        []string
	descriptions []string
}

// departureRows collects the airport's departure routes into the rows of the
// departures table, ordered by SID, then exit, then aircraft class.
func departureRows(icao string, ap *av.Airport, rates map[av.RunwayID]map[string]float32) []departureRow {
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

func (rd *RouteDrawer) DrawDeparturesUI(c *client.ControlClient, color *[3]float32) {
	if !imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		return
	}
	drawColorPicker("Draw Color##3", color)

	if rd.Departures == nil {
		rd.Departures = make(map[string]map[DepartureGroup]bool)
	}

	var rows []departureRow
	for icao, rates := range util.SortedMap(c.State.LaunchConfig.DepartureRates) {
		if rd.Departures[icao] == nil {
			rd.Departures[icao] = make(map[DepartureGroup]bool)
		}
		rows = append(rows, departureRows(icao, c.State.Airports[icao], rates)...)
	}

	drawSelectionTable("departures", rows,
		func(r departureRow) {
			enabled := rd.Departures[r.airport][r.group]
			imgui.Checkbox("##enable-"+r.airport+"-"+r.group.SID+"-"+string(r.group.Exit)+"-"+
				r.group.Aircraft.String(), &enabled)
			rd.Departures[r.airport][r.group] = enabled
		},
		tableColumn[departureRow]{"Airport", func(r departureRow) string { return r.airport }},
		tableColumn[departureRow]{"SID", func(r departureRow) string { return r.group.SID }},
		tableColumn[departureRow]{"Aircraft", func(r departureRow) string { return r.group.Aircraft.String() }},
		tableColumn[departureRow]{"Runways", func(r departureRow) string { return strings.Join(r.runways, ", ") }},
		tableColumn[departureRow]{"Exits", func(r departureRow) string { return joinWrapped(r.exits, listWrapWidth) }},
		tableColumn[departureRow]{"Description", func(r departureRow) string {
			return strings.Join(r.descriptions, " / ")
		}})
}

func (rd *RouteDrawer) DrawOverflightsUI(c *client.ControlClient, color *[3]float32) {
	if !imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
		return
	}
	drawColorPicker("Draw Color##4", color)

	if rd.Overflights == nil {
		rd.Overflights = make(map[string]map[int]bool)
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
		if rd.Overflights[name] == nil {
			rd.Overflights[name] = make(map[int]bool)
		}
		for i := range flow.Overflights {
			rows = append(rows, row{flow: name, index: i, of: &flow.Overflights[i]})
		}
	}

	drawSelectionTable("over", rows,
		func(r row) {
			enabled := rd.Overflights[r.flow][r.index]
			imgui.Checkbox(fmt.Sprintf("##of-%s-%d", r.flow, r.index), &enabled)
			rd.Overflights[r.flow][r.index] = enabled
		},
		tableColumn[row]{"Overflight", func(r row) string { return r.flow }},
		tableColumn[row]{"Description", func(r row) string { return r.of.Description }})
}

func (rd *RouteDrawer) DrawAirspaceUI(c *client.ControlClient, color *[3]float32) {
	if len(c.State.Airspace) == 0 || !imgui.CollapsingHeaderBoolPtr("Airspace", nil) {
		return
	}
	drawColorPicker("Draw Color##5", color)

	if rd.Airspace == nil {
		rd.Airspace = make(map[sim.ControlPosition]map[string]bool)
		for ctrl, sectors := range c.State.Airspace {
			rd.Airspace[ctrl] = make(map[string]bool)
			for _, sector := range util.SortedMapKeys(sectors) {
				rd.Airspace[ctrl][sector] = false
			}
		}
	}

	for pos, vols := range util.SortedMap(rd.Airspace) {
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
		imgui.Text(ap)
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
		imgui.Text(strings.Join(list.Airports, ", "))
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

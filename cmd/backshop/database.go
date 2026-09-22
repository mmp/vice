// cmd/backshop/database.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// dbCategory is which part of db.DB the Database tab is showing.
type dbCategory int32

const (
	dbAirports dbCategory = iota
	dbNavaids
	dbFixes
	dbAirways
	dbAircraft
	dbAirlines
	dbFacilities
)

var dbCategoryNames = []string{"Airports", "Navaids", "Fixes", "Airways", "Aircraft types", "Airlines", "Facilities"}

type databaseTab struct {
	category dbCategory
	search   string
	nearOnly bool
	// drawAll is set for one frame by the "Draw all listed" button.
	drawAll bool
}

func (d *databaseTab) init() {
	d.nearOnly = true
}

// maxDatabaseRows bounds what the tables show: the fix database alone runs
// to tens of thousands of entries and imgui would happily try to lay all of
// them out.
const maxDatabaseRows = 500

func (in *inspector) drawDatabaseTab(a *app) {
	if !imgui.BeginTabItem("Database") {
		return
	}
	defer imgui.EndTabItem()

	d := &in.db
	imgui.SetNextItemWidth(160)
	if imgui.BeginCombo("##dbcat", dbCategoryNames[d.category]) {
		for i, name := range dbCategoryNames {
			if imgui.SelectableBool(name) {
				d.category = dbCategory(i)
			}
		}
		imgui.EndCombo()
	}
	imgui.SameLine()
	imgui.SetNextItemWidth(160)
	imgui.InputTextWithHint("##dbsearch", "search", &d.search, 0, nil)
	if d.category != dbAircraft && d.category != dbAirlines {
		imgui.SameLine()
		imgui.Checkbox("In range", &d.nearOnly)

		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("##dbcolor", &in.overlays.pointColor,
			imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)
		imgui.SameLine()
		if imgui.SmallButton("Draw all listed") {
			d.drawAll = true
		}
		imgui.SameLine()
		if imgui.SmallButton("Draw none") {
			clear(in.overlays.points)
		}
		imgui.SameLine()
		imgui.Text(fmt.Sprintf("%d drawn", len(in.overlays.points)))
	}
	// drawAll is honored by the row loop, which is the only place that
	// knows which entries the search and range settings actually list.
	defer func() { d.drawAll = false }()

	switch d.category {
	case dbAirports:
		in.drawAirports(a)
	case dbNavaids:
		in.drawNavaids(a)
	case dbFixes:
		in.drawFixes(a)
	case dbAirways:
		in.drawAirways(a)
	case dbAircraft:
		in.drawAircraftTypes(a)
	case dbAirlines:
		in.drawAirlines(a)
	case dbFacilities:
		in.drawFacilities(a)
	}
}

// inRange reports whether a location should be listed given the "In range"
// setting; without a sim there is no center to measure from.
func (in *inspector) inRange(a *app, p math.Point2LL) bool {
	if !in.db.nearOnly || a.cc == nil {
		return true
	}
	return math.NMDistance2LL(p, a.scope.center) <= a.scope.rangeNM
}

func (in *inspector) matches(s ...string) bool {
	q := strings.ToUpper(in.db.search)
	if q == "" {
		return true
	}
	return slices.ContainsFunc(s, func(v string) bool { return strings.Contains(strings.ToUpper(v), q) })
}

// pointToggle draws the checkbox that puts one database entry on the map,
// honoring the "Draw all listed" button for the rows the filters let through.
func (in *inspector) pointToggle(id, label string, p math.Point2LL) {
	if in.db.drawAll {
		in.overlays.points[id] = mapPoint{label: label, p: p}
	}
	drawToggle(in.overlays.points, id, func() mapPoint { return mapPoint{label: label, p: p} })
}

// idCell draws the identifier of a database entry: clicking it copies the
// entry's lat-long and double-clicking centers the map there. The position
// isn't shown--it is wide enough to squeeze the rest of the row out and it
// is a click away--so uniq keeps rows distinct where ids repeat.
func (in *inspector) idCell(a *app, id, uniq string, p math.Point2LL) {
	if imgui.SelectableBool(id + "##" + uniq) {
		dms := strings.ReplaceAll(p.DMSString(), " ", "")
		a.plat.GetClipboard().SetClipboard(dms)
		a.status = "copied " + dms
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Click to copy the position; double-click to center the map here")
	}
	if imgui.IsItemHovered() && imgui.IsMouseDoubleClicked(0) {
		a.scope.center = p
	}
}

func (in *inspector) drawAirports(a *app) {
	if !imgui.BeginTableV("airports", 6, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Id", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Elev", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("ARTCC", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Name", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableSetupColumnV("Runways", imgui.TableColumnFlagsWidthStretch, 1, 0)
	imgui.TableHeadersRow()

	n := 0
	for icao, ap := range util.SortedMap(db.DB.Airports) {
		if n >= maxDatabaseRows {
			break
		}
		id := string(icao)
		if !in.matches(id, ap.Name) || !in.inRange(a, ap.Location) {
			continue
		}
		n++
		imgui.TableNextRow()
		imgui.TableNextColumn()
		in.pointToggle("ap/"+id, id, ap.Location)
		imgui.TableNextColumn()
		in.idCell(a, id, "ap", ap.Location)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprint(ap.Elevation))
		imgui.TableNextColumn()
		imgui.Text(ap.ARTCC)
		imgui.TableNextColumn()
		imgui.TextWrapped(ap.Name)
		imgui.TableNextColumn()
		imgui.TextWrapped(ap.ValidRunways())
	}
	imgui.EndTable()
}

func (in *inspector) drawNavaids(a *app) {
	if !imgui.BeginTableV("navaids", 5, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Id", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Type", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Decl", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumn("Name")
	imgui.TableHeadersRow()

	n := 0
	for id, nav := range util.SortedMap(db.DB.Navaids) {
		if n >= maxDatabaseRows {
			break
		}
		if !in.matches(id, nav.Name) || !in.inRange(a, nav.Location) {
			continue
		}
		n++
		imgui.TableNextRow()
		imgui.TableNextColumn()
		in.pointToggle("nav/"+id, id, nav.Location)
		imgui.TableNextColumn()
		in.idCell(a, id, "nav", nav.Location)
		imgui.TableNextColumn()
		imgui.Text(nav.Type)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("%.1f", nav.Declination))
		imgui.TableNextColumn()
		imgui.TextWrapped(nav.Name)
	}
	imgui.EndTable()
}

func (in *inspector) drawFixes(a *app) {
	if !imgui.BeginTableV("fixes", 2, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumn("Id")
	imgui.TableHeadersRow()

	n := 0
	for id, fix := range util.SortedMap(db.DB.Fixes) {
		if n >= maxDatabaseRows {
			break
		}
		if !in.matches(id) || !in.inRange(a, fix.Location) {
			continue
		}
		n++
		imgui.TableNextRow()
		imgui.TableNextColumn()
		in.pointToggle("fix/"+id, id, fix.Location)
		imgui.TableNextColumn()
		in.idCell(a, id, "fix", fix.Location)
	}
	imgui.EndTable()
}

func (in *inspector) drawAirways(a *app) {
	if !imgui.BeginTableV("airways", 3, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	imgui.TableSetupColumn("Airway")
	imgui.TableSetupColumn("Fixes")
	imgui.TableSetupColumn("")
	imgui.TableHeadersRow()

	n := 0
	for name, airways := range util.SortedMap(db.DB.Airways) {
		if n >= maxDatabaseRows {
			break
		}
		if !in.matches(name) {
			continue
		}
		for i, aw := range airways {
			var fixes []string
			near := !in.db.nearOnly || a.cc == nil
			for _, f := range aw.Fixes {
				fixes = append(fixes, f.Fix)
				if !near {
					if p, ok := db.DB.LookupWaypoint(f.Fix); ok && in.inRange(a, p) {
						near = true
					}
				}
			}
			if !near {
				continue
			}
			n++
			route := strings.Join(fixes, " ")
			imgui.PushIDInt(int32(i))
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(name)
			imgui.TableNextColumn()
			imgui.TextWrapped(route)
			imgui.TableNextColumn()
			a.copyButton(name, route)
			imgui.PopID()
		}
	}
	imgui.EndTable()
}

func (in *inspector) drawAircraftTypes(a *app) {
	if !imgui.BeginTableV("actypes", 5, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	imgui.TableSetupColumnV("ICAO", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("CWT", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("Engines", imgui.TableColumnFlagsWidthFixed, 70, 0)
	imgui.TableSetupColumnV("Ceiling", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumn("Name")
	imgui.TableHeadersRow()

	n := 0
	for id, perf := range util.SortedMap(db.DB.AircraftPerformance) {
		if n >= maxDatabaseRows {
			break
		}
		if !in.matches(id, perf.Name) {
			continue
		}
		n++
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(id)
		imgui.TableNextColumn()
		imgui.Text(perf.Category.CWT)
		imgui.TableNextColumn()
		imgui.Text(perf.Engine.AircraftType)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprint(perf.Ceiling))
		imgui.TableNextColumn()
		imgui.TextWrapped(perf.Name)
	}
	imgui.EndTable()
}

func (in *inspector) drawAirlines(a *app) {
	if !imgui.BeginTableV("airlines", 4, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	imgui.TableSetupColumnV("ICAO", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Callsign", imgui.TableColumnFlagsWidthStretch, 1, 0)
	imgui.TableSetupColumnV("Fleets", imgui.TableColumnFlagsWidthStretch, 1, 0)
	imgui.TableSetupColumnV("Name", imgui.TableColumnFlagsWidthStretch, 2, 0)
	imgui.TableHeadersRow()

	n := 0
	for id, al := range util.SortedMap(db.DB.Airlines) {
		if n >= maxDatabaseRows {
			break
		}
		if !in.matches(id, al.Name, al.Callsign.Name) {
			continue
		}
		n++
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(id)
		imgui.TableNextColumn()
		imgui.TextWrapped(al.Callsign.Name)
		imgui.TableNextColumn()
		imgui.TextWrapped(strings.Join(util.SortedMapKeys(al.Fleets), ", "))
		imgui.TableNextColumn()
		imgui.TextWrapped(al.Name)
	}
	imgui.EndTable()
}

func (in *inspector) drawFacilities(a *app) {
	if !imgui.BeginTableV("facilities", 4, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	setupDrawColumn()
	imgui.TableSetupColumnV("Id", imgui.TableColumnFlagsWidthFixed, 50, 0)
	imgui.TableSetupColumnV("Kind", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumn("Name")
	imgui.TableHeadersRow()

	row := func(id, kind, name string, p math.Point2LL) {
		if !in.matches(id, name) || !in.inRange(a, p) {
			return
		}
		imgui.TableNextRow()
		imgui.TableNextColumn()
		in.pointToggle(kind+"/"+id, id, p)
		imgui.TableNextColumn()
		in.idCell(a, id, kind, p)
		imgui.TableNextColumn()
		imgui.Text(kind)
		imgui.TableNextColumn()
		imgui.TextWrapped(name)
	}

	for id, f := range util.SortedMap(db.DB.ARTCCs) {
		row(id, "ARTCC", f.Name, math.Point2LL{f.Longitude, f.Latitude})
	}
	for id, f := range util.SortedMap(db.DB.TRACONs) {
		row(id, "TRACON", f.Name, math.Point2LL{f.Longitude, f.Latitude})
	}
	for id, f := range util.SortedMap(db.DB.ATCTs) {
		row(id, "ATCT", f.Name, math.Point2LL{f.Longitude, f.Latitude})
	}
	imgui.EndTable()
}

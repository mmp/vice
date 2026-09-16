// cmd/backshop/inspector.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// tableFlags is the styling every table here shares. Scrolling is not part
// of it: a scrolling table always takes the whole height it is given, which
// leaves empty space under the last row of a short one, so tableSize turns
// it on only for a table that doesn't fit.
const tableFlags = imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
	imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

// scrollingTableFlags is for the tables that take the rest of the tab and
// scroll within it.
const scrollingTableFlags = tableFlags | imgui.TableFlagsScrollY

// tableSize gives the flags and outer size for a table with rows rows: it
// ends just below its last row up to maxRows and scrolls past that.
func tableSize(rows, maxRows int) (imgui.TableFlags, imgui.Vec2) {
	if rows <= maxRows {
		return tableFlags, imgui.Vec2{}
	}
	// A row is as tall as the draw checkbox these tables lead with; the
	// header row is a line of text.
	pad := 2 * imgui.CurrentStyle().CellPadding().Y
	return scrollingTableFlags,
		imgui.Vec2{Y: imgui.TextLineHeight() + pad + float32(maxRows)*(imgui.FrameHeight()+pad)}
}

// warningColor marks what the tool has found wrong: a scenario error, a route
// the scenario can't fly, a file it can't read.
var warningColor = imgui.Vec4{X: 1, Y: 0.35, Z: 0.35, W: 1}

// inspector is the side window: the textual half of the tool, where a
// scenario is chosen and everything vice knows about it can be read and
// copied.
type inspector struct {
	airports   airportsTab
	routes     routeDraw
	pairRoutes routesTab
	traffic    trafficTab
	playback   playback
	db         databaseTab
	cifp       cifpTab
	brief      gui.Brief
	videoMaps  videoMapsTab
	tts        ttsTab

	overlays overlays

	// launchType is the ICAO aircraft type the Launch tab launches with.
	launchType string
}

func (in *inspector) init(config *Config) {
	in.airports.init()
	in.routes.init()
	in.playback.init()
	in.db.init()
	in.cifp.init()
	in.overlays.init()
	in.tts.init(config)
}

func (in *inspector) resetSim() {
	in.airports.reset()
	in.routes.reset()
	in.traffic.reset()
	in.pairRoutes.reset()
	// The recorded flights were flown in the scenario that just went away.
	in.playback.reset()
	in.brief.Invalidate()
	// The overlays and the drawn CIFP procedures name regions, fixes, and
	// routes of the scenario that just went away; keeping them would draw the
	// old facility's geometry.
	clear(in.overlays.volumes)
	clear(in.overlays.points)
	clear(in.cifp.draw)
	in.tts.reset()
}

func (in *inspector) draw(a *app) {
	displaySize := a.plat.DisplaySize()
	// Wide enough that the busier tables--a SID's exits, an airport's
	// runways--have somewhere to go.
	const width = 760
	imgui.SetNextWindowPosV(imgui.Vec2{X: max(displaySize[0]-width-20, 0), Y: a.menuBarHeight + 10},
		imgui.CondFirstUseEver, imgui.Vec2{})
	imgui.SetNextWindowSizeV(imgui.Vec2{X: width, Y: displaySize[1] - a.menuBarHeight - 30}, imgui.CondFirstUseEver)

	if !imgui.Begin("Inspector") {
		imgui.End()
		return
	}

	// Everything here describes the scenario a reload is in the middle of
	// replacing, and the sim it is running in goes away with it, so while
	// one is under way the inspector is shown but can't be acted on.
	imgui.BeginDisabledV(a.reload != nil)
	if imgui.BeginTabBar("tabs") {
		in.drawScenarioTab(a)
		in.drawAirportsTab(a)
		in.drawMapsTab(a)
		in.drawProceduresTab(a)
		in.drawRoutesTab(a)
		in.drawTrafficTab(a)
		in.drawFiltersTab(a)
		in.drawDatabaseTab(a)
		in.drawCIFPTab(a)
		in.drawTTSTab(a)
		in.drawBriefTab(a)
		in.drawLaunchTab(a)
		in.drawVideoMapsTab(a)
		imgui.EndTabBar()
	}
	imgui.EndDisabled()

	imgui.End()
}

///////////////////////////////////////////////////////////////////////////
// Scenario

func (in *inspector) drawScenarioTab(a *app) {
	if !imgui.BeginTabItem("Scenario") {
		return
	}
	defer imgui.EndTabItem()

	catalogs := a.catalogs()
	if len(catalogs) == 0 {
		imgui.Text("No scenarios loaded.")
		return
	}

	changed := false
	imgui.SetNextItemWidth(200)
	if imgui.BeginCombo("Facility", a.sel.facility) {
		for name := range util.SortedMap(catalogs) {
			if imgui.SelectableBool(name) && name != a.sel.facility {
				a.sel.facility, a.sel.group, a.sel.scenario = name, "", ""
				changed = true
			}
		}
		imgui.EndCombo()
	}

	groups := catalogs[a.sel.facility]
	imgui.SetNextItemWidth(200)
	if imgui.BeginCombo("Scenario group", a.sel.group) {
		for name := range util.SortedMap(groups) {
			if imgui.SelectableBool(name) && name != a.sel.group {
				a.sel.group, a.sel.scenario = name, ""
				changed = true
			}
		}
		imgui.EndCombo()
	}

	if catalog, ok := groups[a.sel.group]; ok {
		imgui.SetNextItemWidth(200)
		if imgui.BeginCombo("Scenario", a.sel.scenario) {
			for name := range util.SortedMap(catalog.Scenarios) {
				if imgui.SelectableBool(name) && name != a.sel.scenario {
					a.sel.scenario = name
					changed = true
				}
			}
			imgui.EndCombo()
		}
	}

	if spec := a.selectedSpec(); spec != nil && spec.Description != "" {
		imgui.TextWrapped(spec.Description)
	}

	if changed {
		a.normalizeSelection()
		a.startSim()
	}

	imgui.Separator()

	if imgui.Button("Reload scenarios") {
		a.reloadScenarios()
	}
	imgui.SameLine()
	if imgui.Button("Restart sim") {
		a.startSim()
	}
	imgui.SameLine()
	imgui.Text(reloadShortcut + " reloads")

	imgui.Separator()
	in.drawExtraFilesUI(a)

	if len(a.reloadErrors) > 0 {
		imgui.Separator()
		imgui.TextColored(warningColor, fmt.Sprintf("%d error(s):", len(a.reloadErrors)))
		imgui.SameLine()
		a.copyButton("errors", strings.Join(a.reloadErrors, "\n"))
		if imgui.BeginChildStrV("errors", imgui.Vec2{Y: 160}, imgui.ChildFlagsBorders, 0) {
			for _, e := range a.reloadErrors {
				imgui.TextWrapped(e)
			}
		}
		imgui.EndChild()
	}

	if a.cc == nil {
		return
	}

	imgui.Separator()
	in.drawScenarioSummary(a)
	imgui.Separator()
	in.drawScenarioControllers(a)
}

func (in *inspector) drawScenarioSummary(a *app) {
	ss := &a.cc.State

	// The summary is short enough to show whole, so it takes the flags
	// without scrolling and sizes itself to its rows.
	if !imgui.BeginTableV("summary", 2, tableFlags, imgui.Vec2{}, 0) {
		return
	}
	row := func(k, v string) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(k)
		imgui.TableNextColumn()
		imgui.TextWrapped(v)
	}
	imgui.TableSetupColumnV("Item", imgui.TableColumnFlagsWidthFixed, 170, 0)
	imgui.TableSetupColumn("Value")
	imgui.TableHeadersRow()

	row("Facility", ss.Facility)
	row("Configuration", ss.ConfigurationId)
	row("Center", ss.Center.DMSString())
	row("Range", fmt.Sprintf("%.0f nm", ss.Range))
	row("Magnetic variation", fmt.Sprintf("%.2f", ss.MagneticVariation))
	row("Nm per longitude", fmt.Sprintf("%.4f", ss.NmPerLongitude))
	row("Video map file", ss.ControllerVideoMapFile)
	row("IFR departure airports", airportList(util.SortedMapKeys(ss.LaunchConfig.DepartureRates)))
	row("VFR departure airports", airportList(util.SortedMapKeys(ss.LaunchConfig.VFRAirportRates)))
	row("Arrival airports", airportList(util.SortedMapKeys(ss.ArrivalAirports)))
	row("Controllers", fmt.Sprint(len(ss.Controllers)))
	row("Inbound flows", fmt.Sprint(len(ss.InboundFlows)))
	row("TFRs", fmt.Sprint(len(ss.TFRs)))
	imgui.EndTable()
}

func (in *inspector) drawScenarioControllers(a *app) {
	if !imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		return
	}

	ss := &a.cc.State
	flags, size := tableSize(len(ss.Controllers), 12)
	if !imgui.BeginTableV("ctrls", 4, flags, size, 0) {
		return
	}
	imgui.TableSetupColumn("TCP")
	imgui.TableSetupColumn("Position")
	imgui.TableSetupColumn("Frequency")
	imgui.TableSetupColumn("Radio name")
	imgui.TableHeadersRow()
	for _, pos := range util.SortedMapKeys(ss.Controllers) {
		ctrl := ss.Controllers[pos]
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(string(pos))
		imgui.TableNextColumn()
		imgui.Text(ctrl.Position)
		imgui.TableNextColumn()
		imgui.Text(ctrl.Frequency.String())
		imgui.TableNextColumn()
		imgui.Text(ctrl.RadioName)
	}
	imgui.EndTable()
}

// departureKinds says what the scenario launches out of an airport: most of
// its departure airports only see VFR traffic, and just a few fly the IFR
// procedures there is anything to inspect.
func departureKinds(ss *client.SimState, airport av.ICAOAirportCode) string {
	var kinds []string
	if hasIFRDepartures(ss, airport) {
		kinds = append(kinds, "IFR")
	}
	if ss.LaunchConfig.VFRAirportRates[airport] > 0 {
		kinds = append(kinds, "VFR")
	}
	return strings.Join(kinds, ", ")
}

// airportList formats airport identifiers for a table cell.
func airportList(airports []av.ICAOAirportCode) string {
	return strings.Join(util.MapSlice(airports, func(icao av.ICAOAirportCode) string { return string(icao) }), ", ")
}

///////////////////////////////////////////////////////////////////////////
// Maps

func (in *inspector) drawMapsTab(a *app) {
	if !imgui.BeginTabItem("Maps") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	s := &a.scope
	imgui.Text(s.mapLibrary)
	imgui.SameLine()
	a.copyButton("maplib", s.mapLibrary)

	imgui.SetNextItemWidth(180)
	imgui.InputTextWithHint("##filter", "filter", &s.mapFilter, 0, nil)
	imgui.SameLine()
	if imgui.Button("None") {
		for i := range s.maps {
			s.maps[i].visible = false
		}
	}
	imgui.SameLine()
	if imgui.Button("Scenario defaults") {
		s.showScenarioDefaultMaps(a.cc)
	}

	// The sections are drawn in a child of their own so that the library
	// name, the filter, and the buttons stay put while they scroll.
	if !imgui.BeginChildStrV("mapsections", imgui.Vec2{}, 0, 0) {
		imgui.EndChild()
		return
	}
	defer imgui.EndChild()

	filter := strings.ToUpper(s.mapFilter)
	for _, section := range s.mapSections(a.cc.State.ScenarioDefaultVideoGroup) {
		shown := util.FilterSlice(section.maps, func(i int) bool { return s.maps[i].matches(filter) })
		if len(shown) == 0 {
			continue
		}
		drawn := len(util.FilterSlice(shown, func(i int) bool { return s.maps[i].visible }))

		flags := imgui.TreeNodeFlags(0)
		if section.open {
			flags = imgui.TreeNodeFlagsDefaultOpen
		}
		// The count in the header changes as maps are turned on and off, so
		// the section's id is given explicitly rather than taken from it.
		title := fmt.Sprintf("%s: %d of %d drawn###%s", section.name, drawn, len(shown), section.name)
		if imgui.CollapsingHeaderTreeNodeFlagsV(title, flags) {
			in.drawMapSection(a, section.name, shown)
		}
	}
}

// drawMapSection lists one section's maps, each with the checkbox that draws
// it.
func (in *inspector) drawMapSection(a *app, id string, maps []int) {
	s := &a.scope
	if !imgui.BeginTableV("maps"+id, 5, tableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()

	setupDrawColumn()
	imgui.TableSetupColumnV("Id", imgui.TableColumnFlagsWidthFixed, 40, 0)
	imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 80, 0)
	imgui.TableSetupColumnV("Category", imgui.TableColumnFlagsWidthFixed, 150, 0)
	imgui.TableSetupColumn("Name")
	imgui.TableHeadersRow()

	for _, i := range maps {
		m := &s.maps[i]
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Checkbox("##draw", &m.visible)
		imgui.TableNextColumn()
		if m.Id != 0 {
			imgui.Text(fmt.Sprint(m.Id))
		}
		imgui.TableNextColumn()
		imgui.Text(m.Label)
		imgui.TableNextColumn()
		imgui.Text(categoryName(m.Category, m.system))
		imgui.TableNextColumn()
		if imgui.SelectableBool(m.Name) {
			name := m.defaultMapName()
			a.plat.GetClipboard().SetClipboard(name)
			a.status = "copied " + name
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Click to copy the name a scenario names the map by")
		}
		imgui.PopID()
	}
}

func categoryName(category int, system bool) string {
	if system {
		return "SYSTEM"
	}
	if category >= 0 && category < len(radar.VideoMapCategoryNames) {
		if n := radar.VideoMapCategoryNames[category]; n != "" {
			return n
		}
	}
	return ""
}

///////////////////////////////////////////////////////////////////////////
// Additional files

// drawExtraFilesUI picks the work-in-progress scenario, video map, and
// brief files to load alongside the installed ones. Choosing one takes
// effect on the next reload, which is also what the button does.
func (in *inspector) drawExtraFilesUI(a *app) {
	imgui.TextWrapped("Additional scenario, video map, brief, and facility configuration files to load " +
		"alongside the installed ones. A scenario whose \"name\" matches an installed one replaces it.")

	changed := false
	picked := func(c bool, err error) {
		changed = changed || c
		if err != nil {
			a.status = err.Error()
		}
	}

	picked(gui.DrawFilePicker("Scenario", "scenario", &a.config.ScenarioFile, []string{"*.json"}))
	picked(gui.DrawFilePicker("Video map", "videomap", &a.config.VideoMapFile, []string{"*.mappack"}))
	picked(gui.DrawFilePicker("Brief", "brief", &a.config.ScenarioBriefFile, []string{"*.md"}))
	picked(gui.DrawFileListPicker("Facility configuration", "facilityconfig",
		&a.config.FacilityConfigFiles, []string{"*.json"}))

	if changed {
		a.reloadScenarios()
	}
}

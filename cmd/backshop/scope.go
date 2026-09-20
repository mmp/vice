// cmd/backshop/scope.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// tool is what a click on the map does.
type tool int

const (
	toolNone tool = iota
	// toolDrawRoute accumulates clicked points and keeps them on the
	// clipboard as a space-separated list, as STARS's .DRAWROUTE does.
	toolDrawRoute
	// toolMeasure reports the distance and magnetic bearing between two
	// clicked points.
	toolMeasure
)

// icon is the glyph the menu bar shows for the tool.
func (t tool) icon() string {
	switch t {
	case toolDrawRoute:
		return renderer.FontAwesomeIconDrawPolygon
	case toolMeasure:
		return renderer.FontAwesomeIconRuler
	default:
		return renderer.FontAwesomeIconArrowsAlt
	}
}

// help is what the tool's button says on hover. Each click on the map already
// puts what the tool has drawn on the clipboard, so there is nothing else to
// go and press.
func (t tool) help() string {
	switch t {
	case toolDrawRoute:
		return "Draw route: each click adds a point and copies the route so far.\n" +
			"Backspace removes the last point, escape clears them all."
	case toolMeasure:
		return "Measure: click the two ends. Escape clears the measurement."
	default:
		return "Pan: drag to move the map, the wheel zooms.\n" +
			"Control-shift-click copies a position in any mode."
	}
}

// scopeMap is one entry in the scope's map list: either a library map, whose
// full geometry is drawn every frame, or a system map that vice synthesizes,
// which arrives as a prebuilt command buffer.
type scopeMap struct {
	radar.Map
	// group is the ERAM map group the map belongs to; a STARS library map
	// and a system map have none. base marks a group's base map, which ERAM
	// draws whenever the group is loaded and gives no way to turn off.
	group   string
	base    bool
	system  bool
	visible bool
}

type scope struct {
	center  math.Point2LL
	rangeNM float32

	maps       []scopeMap
	mapFilter  string
	mapLibrary string
	// mapsScenario identifies the scenario the current map list is for, so
	// that a scenario change resets which maps are shown.
	mapsScenario string

	// Video map symbols and labels are drawn with the ERAM bitmap fonts the
	// maps were authored against. Everything backshop adds itself--route
	// annotations, datablocks, its own labels--is ordinary text, including
	// lowercase, which those fonts render as ATC symbols rather than
	// letters, so it uses Roboto instead.
	symbolFont [3]*renderer.Font
	mapFont    [4]*renderer.Font
	textFont   *renderer.Font

	// zoomTarget is where the wheel has asked the range to go; rangeNM
	// eases toward it. zoomAnchor is the position the zoom keeps under
	// zoomAnchorWindow while it does.
	zoomTarget       float32
	zoomAnchor       math.Point2LL
	zoomAnchorWindow [2]float32

	activeTool  tool
	routePoints []math.Point2LL
	measure     []math.Point2LL

	cursorLatLong     math.Point2LL
	haveCursorLatLong bool

	transforms radar.ScopeTransformations
}

func (s *scope) init() {
	s.rangeNM = 50
	s.zoomTarget = s.rangeNM
}

// setTool switches what a click on the map does, dropping what the tool
// being left behind drew: a route or a measurement belongs to the mode it
// was made in, and leaving it up only clutters the map.
func (s *scope) setTool(t tool) {
	if t != s.activeTool {
		s.clearTool()
		s.activeTool = t
	}
}

// clearTool drops the points the active tool has collected.
func (s *scope) clearTool() {
	s.routePoints = nil
	s.measure = nil
}

func (s *scope) haveToolPoints() bool {
	return len(s.routePoints) > 0 || len(s.measure) > 0
}

// scopeFontSize is the size of the text backshop draws on the scope: route
// annotations, datablocks, and its own labels. Small, since a busy departure
// procedure puts a lot of it on the map at once.
const scopeFontSize = 12

// initFonts bakes the bitmap fonts video maps are drawn with. It needs a
// live renderer, so it happens separately from init.
func (s *scope) initFonts(r renderer.Renderer, p platform.Platform) {
	fonts := radar.CreateERAMFonts(r, p)
	s.symbolFont = [3]*renderer.Font{
		radar.FindERAMFont(fonts, "EramGeomap-16.pcf", 15),
		radar.FindERAMFont(fonts, "EramGeomap-18.pcf", 17),
		radar.FindERAMFont(fonts, "EramGeomap-20.pcf", 19),
	}
	s.mapFont = [4]*renderer.Font{
		radar.FindERAMFont(fonts, "EramText-9.pcf", 11),
		radar.FindERAMFont(fonts, "EramText-11.pcf", 13),
		radar.FindERAMFont(fonts, "EramText-14.pcf", 17),
		radar.FindERAMFont(fonts, "EramText-16.pcf", 18),
	}
	s.textFont = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: scopeFontSize})
}

// MapSymbolFont and MapLabelFont implement radar.MapFonts.
func (s *scope) MapSymbolFont(size int) *renderer.Font {
	return s.symbolFont[math.Clamp(size, 1, 3)-1]
}

func (s *scope) MapLabelFont(size int) *renderer.Font {
	return s.mapFont[math.Clamp(size, 1, 4)-1]
}

// resetSim points the scope at a new sim. scenario identifies which one, so
// that reloading the scenario being edited keeps the maps the user turned
// on, while switching to a different one starts from its own defaults.
func (s *scope) resetSim(c *client.ControlClient, scenario string) {
	s.resetView(c)
	s.clearTool()

	keep := scenario == s.mapsScenario && scenario != ""
	s.mapsScenario = scenario
	s.rebuildMaps(c, keep)
}

func (s *scope) resetView(c *client.ControlClient) {
	if c == nil {
		return
	}
	s.center = c.State.Center
	s.rangeNM = c.State.Range
	if s.rangeNM == 0 {
		s.rangeNM = 50
	}
	s.zoomTarget = s.rangeNM
}

// rebuildMaps collects everything drawable for the current sim: the maps in
// the facility's library plus the ones vice synthesizes from its adaptation.
// The maps the scenario puts on the DCB start out visible.
func (s *scope) rebuildMaps(c *client.ControlClient, keepVisible bool) {
	prev := make(map[string]bool)
	if keepVisible {
		for _, m := range s.maps {
			prev[m.Name] = m.visible
		}
	}
	s.maps = nil

	if c == nil {
		return
	}
	ss := &c.State
	s.mapLibrary = ss.ControllerVideoMapFile

	if lib, err := c.LoadVideoMapLibrary(ss.ControllerVideoMapFile); err != nil {
		s.mapLibrary += " (" + err.Error() + ")"
	} else {
		// radar.BuildMaps takes one flat list, so where each ERAM map came
		// from is carried alongside it and put back afterwards.
		type origin struct {
			group string
			base  bool
		}
		var library []av.STARSMap
		var origins []origin
		add := func(m av.STARSMap, o origin) {
			library = append(library, m)
			origins = append(origins, o)
		}

		for _, m := range util.SortedMap(lib.Maps) {
			add(m, origin{})
		}
		// An ARTCC's library holds ERAM map groups rather than STARS maps.
		// They carry the same geometry, so flatten them into the same list
		// with the group they came from in their names.
		for name, group := range util.SortedMap(lib.ERAMMapGroups) {
			if !group.BaseMap.IsEmpty() {
				add(eramMap(name, "BASE", group.BaseMap), origin{group: name, base: true})
			}
			for _, m := range group.Maps {
				// A group's filter menu has twenty slots whether or not the
				// facility filled them all; an empty one has nothing to
				// draw and no name to list it under.
				if !m.IsEmpty() {
					add(eramMap(name, "", m), origin{group: name})
				}
			}
		}
		for i, m := range radar.BuildMaps(library) {
			s.maps = append(s.maps, scopeMap{Map: m, group: origins[i].group, base: origins[i].base})
		}
	}

	for _, m := range radar.SystemMaps(radar.SystemMapSpec{
		Facility:          ss.Facility,
		Center:            ss.Center,
		NmPerLongitude:    ss.NmPerLongitude,
		MagneticVariation: ss.MagneticVariation,
		Adaptation:        &ss.FacilityAdaptation,
		Airports:          ss.Airports,
		ArrivalAirports:   ss.ArrivalAirports,
	}) {
		s.maps = append(s.maps, scopeMap{Map: m, system: true})
	}

	if !keepVisible {
		s.showScenarioDefaultMaps(c)
		return
	}
	for i := range s.maps {
		if v, ok := prev[s.maps[i].Name]; ok {
			s.maps[i].visible = v
		}
	}
}

// eramMap presents one map from an ERAM group as a STARSMap so that
// everything drawable can live in a single list. label names the map within
// its group when the map itself carries no label, as the base map doesn't.
func eramMap(group, label string, m av.ERAMMap) av.STARSMap {
	if label == "" {
		label = m.Label()
	}
	return av.STARSMap{
		Name:     strings.TrimSpace(group + " " + label),
		Label:    label,
		Category: radar.VideoMapNoCategory,
		Lines:    m.Lines,
		Symbols:  m.Symbols,
		Labels:   m.Labels,
	}
}

// defaultMapName is what a scenario's "default_maps" would name the map by.
// An ARTCC's maps are named within their own group, so the group prefix that
// keeps the tool's single list unambiguous is not part of it.
func (m scopeMap) defaultMapName() string {
	if m.group != "" {
		return m.Label
	}
	return m.Name
}

// matches reports whether the map is one the Maps tab's filter is looking
// for; an empty filter takes everything.
func (m scopeMap) matches(filter string) bool {
	return filter == "" || strings.Contains(strings.ToUpper(m.Name), filter) ||
		strings.Contains(strings.ToUpper(m.Label), filter)
}

// mapSection is a run of the scope's maps that the Maps tab draws under one
// header: an ERAM map group, the facility's video map library, or the maps
// vice synthesizes from the facility adaptation.
type mapSection struct {
	name string
	// open is whether the section starts out expanded; the group the
	// scenario opens with is the one worth seeing first.
	open bool
	// maps indexes the scope's maps.
	maps []int
}

const (
	librarySectionName = "Video map library"
	systemSectionName  = "System maps"
)

// mapSections groups the scope's maps for display. An ARTCC's library is a
// set of map groups of which the display loads one at a time, so each gets a
// section of its own with the scenario's group first.
func (s *scope) mapSections(defaultGroup string) []mapSection {
	var sections []mapSection
	add := func(name string, open bool, i int) {
		if j := slices.IndexFunc(sections, func(sec mapSection) bool { return sec.name == name }); j != -1 {
			sections[j].maps = append(sections[j].maps, i)
			return
		}
		sections = append(sections, mapSection{name: name, open: open, maps: []int{i}})
	}

	for i, m := range s.maps {
		switch {
		case m.system:
			add(systemSectionName, true, i)
		case m.group != "":
			add(m.group, m.group == defaultGroup, i)
		default:
			add(librarySectionName, true, i)
		}
	}

	if i := slices.IndexFunc(sections, func(sec mapSection) bool { return sec.name == defaultGroup }); i > 0 {
		ordered := append([]mapSection{sections[i]}, sections[:i]...)
		sections = append(ordered, sections[i+1:]...)
	}
	return sections
}

// showScenarioDefaultMaps makes visible exactly the maps the scenario puts
// up for the primary consolidated position. A facility that adapts video
// maps per controller may leave that controller's default list empty and
// give the defaults on the scenario instead, so fall back to those.
//
// An ARTCC names its maps within a map group rather than across the whole
// library, so for one of those the defaults are found among the scenario's
// group: its base map, which ERAM draws whenever the group is loaded, plus
// whichever of the group's maps the defaults name. The other groups are
// loadable but nothing in them is up to start with.
func (s *scope) showScenarioDefaultMaps(c *client.ControlClient) {
	if c == nil {
		return
	}
	ss := &c.State

	names := ss.ControllerDefaultVideoMaps
	if len(names) == 0 {
		names = ss.ScenarioDefaultVideoMaps
	}

	for i := range s.maps {
		m := &s.maps[i]
		loaded := m.group == "" || m.group == ss.ScenarioDefaultVideoGroup
		m.visible = loaded && (m.base || slices.Contains(names, m.defaultMapName()))
	}
}

const (
	minRangeNM = 1
	maxRangeNM = 800
	// zoomPerNotch is how much one wheel notch changes the range.
	zoomPerNotch = 0.85
	// zoomHalfLife is how long the range takes to close half the distance
	// to the target, in seconds: short enough to feel immediate, long
	// enough to read as motion rather than a jump.
	zoomHalfLife = 0.06
)

// updateZoom eases the range toward what the wheel has asked for, holding
// the position the zoom is anchored on under the same pixel throughout.
func (s *scope) updateZoom(extent math.Extent2D, ss *client.SimState) {
	if s.zoomTarget == 0 {
		s.zoomTarget = s.rangeNM
	}
	if s.rangeNM == s.zoomTarget {
		return
	}

	// Close a fixed fraction of the remaining ratio each frame rather than a
	// fixed number of miles: zooming is multiplicative, so equal times
	// should give equal ratios, and the easing then looks the same whether
	// the scope is at 5 miles or 500.
	dt := imgui.CurrentIO().DeltaTime()
	t := 1 - math.Pow(0.5, dt/zoomHalfLife)
	r := s.rangeNM * math.Pow(s.zoomTarget/s.rangeNM, t)
	if math.Abs(r-s.zoomTarget) < 0.001*s.zoomTarget {
		r = s.zoomTarget
	}
	s.rangeNM = r

	// Put the anchor back under the pixel it was grabbed at.
	if s.zoomAnchorWindow != [2]float32{} {
		tr := radar.GetScopeTransformations(extent, ss.NmPerLongitude, s.center, s.rangeNM,
			scopeRotation(ss))
		s.center = math.Add2LL(s.center, math.Sub2LL(s.zoomAnchor, tr.LatLongFromWindowP(s.zoomAnchorWindow)))
	}
}

// scopeRotation returns the scope's up direction, following the radar system
// the facility runs: ERAM is true north up, STARS is magnetic north up.
func scopeRotation(ss *client.SimState) float32 {
	if av.DB.IsARTCC(ss.Facility) {
		return 0
	}
	return ss.MagneticVariation
}

var (
	libraryMapColor = renderer.RGB{R: 0.35, G: 0.42, B: 0.55}
	systemMapColor  = renderer.RGB{R: 0.55, G: 0.42, B: 0.18}
	toolColor       = renderer.RGB{R: 0.2, G: 0.85, B: 0.2}
	aircraftColor   = renderer.RGB{R: 0.9, G: 0.9, B: 0.9}
)

func (s *scope) draw(a *app, menuBarHeight float32) {
	displaySize := a.plat.DisplaySize()
	extent := math.Extent2D{P1: [2]float32{displaySize[0], displaySize[1] - menuBarHeight}}

	cb := renderer.GetCommandBuffer()
	defer renderer.ReturnCommandBuffer(cb)
	cb.ClearRGB(renderer.RGB{})

	if a.cc == nil {
		a.render.RenderCommandBuffer(cb)
		return
	}

	ss := &a.cc.State
	s.updateZoom(extent, ss)
	s.transforms = radar.GetScopeTransformations(extent, ss.NmPerLongitude, s.center, s.rangeNM,
		scopeRotation(ss))

	s.handleMouse(a, extent, displaySize)

	cb.SetDrawBounds(extent, a.plat.FramebufferSize()[1]/displaySize[1])
	s.drawMaps(a, cb)
	a.inspector.drawRoutes(a, s.transforms, cb)
	a.inspector.drawCIFPProcedures(a, s.transforms, cb)
	a.inspector.drawOverlays(a, s.transforms, cb)
	s.drawAircraft(a, cb)
	s.drawTools(a, cb)
	cb.ResetState()

	a.render.RenderCommandBuffer(cb)
}

func (s *scope) drawMaps(a *app, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	var lineBuf [][2]float32

	// Everything in a map is drawn in one color; backshop is inspecting the
	// geometry rather than reproducing a scope's BCG brightness groups, so
	// every index gets the same value and nothing is invisible.
	var libraryRGB [256]renderer.RGB
	for i := range libraryRGB {
		libraryRGB[i] = libraryMapColor
	}

	// The system maps come as prebuilt command buffers in lat-long space.
	s.transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	cb.SetRGB(systemMapColor)
	for _, m := range s.maps {
		if m.visible && m.system {
			cb.Call(m.CommandBuffer)
		}
	}

	// Library maps are drawn in full: dashed lines, symbols, and labels all
	// need window-space sizes, so they are rebuilt every frame.
	s.transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	for _, m := range s.maps {
		if m.visible && !m.system {
			radar.DrawMapFeatures(m.Lines, m.Symbols, m.Labels, &libraryRGB, s, s.transforms, ld, td, &lineBuf)
		}
	}
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawAircraft draws the recorded flights at the moment the Launch tab's
// scrubber is sitting on. Nothing live is drawn: the sim has already flown
// every aircraft to its deletion by the time a recording exists.
func (s *scope) drawAircraft(a *app, cb *renderer.CommandBuffer) {
	p := &a.inspector.playback
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	style := renderer.TextStyle{Font: s.textFont, Color: aircraftColor}

	track := func(samples []sim.FlightSample, color renderer.RGB) {
		for i := 1; i < len(samples); i++ {
			ld.AddLine(s.transforms.WindowFromLatLongP(samples[i-1].Location),
				s.transforms.WindowFromLatLongP(samples[i].Location), color)
		}
	}

	// What an aircraft has flown is drawn brighter than what it has yet to
	// fly, so that where it is along its route reads at a glance.
	pastColor := aircraftColor.Scale(0.7)
	futureColor := pastColor.Scale(0.5)

	for _, f := range p.visible() {
		past, future := f.flown()
		track(past, pastColor)
		track(future, futureColor)

		sample, ok := f.position()
		if !ok {
			continue
		}
		pw := s.transforms.WindowFromLatLongP(sample.Location)
		const half = 4
		ld.AddLine([2]float32{pw[0] - half, pw[1]}, [2]float32{pw[0] + half, pw[1]}, aircraftColor)
		ld.AddLine([2]float32{pw[0], pw[1] - half}, [2]float32{pw[0], pw[1] + half}, aircraftColor)
		td.AddText(datablock(f, sample), [2]float32{pw[0] + 8, pw[1] + 20}, style)
	}

	s.transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (s *scope) drawTools(a *app, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	style := renderer.TextStyle{Font: s.textFont, Color: toolColor, DrawBackground: true}

	mark := func(p math.Point2LL) [2]float32 {
		pw := s.transforms.WindowFromLatLongP(p)
		const half = 5
		ld.AddLine([2]float32{pw[0] - half, pw[1] - half}, [2]float32{pw[0] + half, pw[1] + half}, toolColor)
		ld.AddLine([2]float32{pw[0] - half, pw[1] + half}, [2]float32{pw[0] + half, pw[1] - half}, toolColor)
		return pw
	}

	for i, p := range s.routePoints {
		pw := mark(p)
		if i > 0 {
			ld.AddLine(s.transforms.WindowFromLatLongP(s.routePoints[i-1]), pw, toolColor)
		}
		td.AddText(fmt.Sprintf("%d", i+1), [2]float32{pw[0] + 6, pw[1] + 16}, style)
	}

	if len(s.measure) == 2 {
		p0, p1 := s.measure[0], s.measure[1]
		pw0, pw1 := mark(p0), mark(p1)
		ld.AddLine(pw0, pw1, toolColor)

		nm := math.NMDistance2LL(p0, p1)
		hdg := math.TrueToMagnetic(math.Heading2LL(p0, p1, a.cc.State.NmPerLongitude),
			a.cc.State.MagneticVariation)
		mid := [2]float32{(pw0[0] + pw1[0]) / 2, (pw0[1] + pw1[1]) / 2}
		td.AddText(fmt.Sprintf("%.2f nm / %03.0f", nm, float32(hdg)), [2]float32{mid[0] + 6, mid[1] + 16}, style)
	} else if len(s.measure) == 1 {
		mark(s.measure[0])
	}

	s.transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, a.plat.DPIScale())
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (s *scope) handleMouse(a *app, extent math.Extent2D, displaySize [2]float32) {
	io := imgui.CurrentIO()
	s.haveCursorLatLong = false
	if io.WantCaptureMouse() {
		return
	}

	mouse := a.plat.GetMouse()
	// GetMouse reports window coordinates with y growing downward; the
	// scope's are pane-relative with y growing up.
	pos := [2]float32{mouse.Pos[0], displaySize[1] - 1 - mouse.Pos[1]}
	if !extent.Inside(pos) {
		return
	}

	s.cursorLatLong = s.transforms.LatLongFromWindowP(pos)
	s.haveCursorLatLong = true

	if mouse.Wheel[1] != 0 {
		// The wheel moves a target the view eases toward rather than the
		// range itself; a notch is otherwise a visible jump, and a trackpad
		// delivers a flurry of them. The point under the cursor is the
		// anchor the zoom holds still for as long as it runs.
		s.zoomTarget = math.Clamp(s.zoomTarget*math.Pow(zoomPerNotch, mouse.Wheel[1]), minRangeNM, maxRangeNM)
		s.zoomAnchor, s.zoomAnchorWindow = s.cursorLatLong, pos
	}

	if mouse.Dragging[platform.MouseButtonPrimary] || mouse.Dragging[platform.MouseButtonSecondary] {
		delta := [2]float32{mouse.DragDelta[0], -mouse.DragDelta[1]}
		s.center = math.Sub2LL(s.center, s.transforms.LatLongFromWindowV(delta))
		return
	}

	keyboard := a.plat.GetKeyboard()
	shiftControl := keyboard != nil && keyboard.KeyShift() && keyboard.KeyControl()

	if mouse.Released[platform.MouseButtonPrimary] {
		p := s.transforms.LatLongFromWindowP(pos)
		switch {
		case shiftControl:
			a.plat.GetClipboard().SetClipboard(strings.ReplaceAll(p.DMSString(), " ", ""))
			a.status = "copied " + strings.ReplaceAll(p.DMSString(), " ", "")
		case s.activeTool == toolDrawRoute:
			s.routePoints = append(s.routePoints, p)
			s.copyRoutePoints(a)
		case s.activeTool == toolMeasure:
			if len(s.measure) == 2 {
				s.measure = nil
			}
			s.measure = append(s.measure, p)
		}
	}

	if keyboard == nil {
		return
	}
	if _, ok := keyboard.Pressed[imgui.KeyEscape]; ok {
		s.setTool(toolNone)
	}
	if s.activeTool == toolDrawRoute {
		if _, ok := keyboard.Pressed[imgui.KeyBackspace]; ok && len(s.routePoints) > 0 {
			s.routePoints = s.routePoints[:len(s.routePoints)-1]
			s.copyRoutePoints(a)
		}
	}
}

func (s *scope) copyRoutePoints(a *app) {
	a.plat.GetClipboard().SetClipboard(s.routePointsString())
	a.status = fmt.Sprintf("%d points", len(s.routePoints))
}

func (s *scope) routePointsString() string {
	var pts []string
	for _, p := range s.routePoints {
		pts = append(pts, strings.ReplaceAll(p.DMSString(), " ", ""))
	}
	return strings.Join(pts, " ")
}

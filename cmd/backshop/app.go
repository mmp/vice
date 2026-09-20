// cmd/backshop/app.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// simStartTime is when a sim backshop creates starts. Facility engineering
// cares about geometry rather than about matching a particular day's
// weather, so a fixed recent time keeps runs repeatable.
func simStartTime() time.Time {
	return time.Now().UTC().Truncate(time.Hour)
}

// controllerInitials is what backshop signs on as; it is only ever a
// single-user local sim, so the value just has to be something.
const controllerInitials = "FE"

type app struct {
	config *Config
	plat   platform.Platform
	render renderer.Renderer
	lg     *log.Logger

	mgr *client.ConnectionManager
	cc  *client.ControlClient

	scope     scopeView
	inspector inspector

	// Which scenario is selected in the picker, which may differ from the
	// one the current sim is flying until the user starts it.
	sel selection

	reload       *client.ScenarioReload
	reloadErrors []string
	status       string

	// Modal dialogs waiting to be shown, and the pending check for a newer
	// release, which puts one up if it finds one.
	dialogs        []*gui.ModalDialog
	newReleaseChan chan *util.Release

	// quit is set when the user asks to quit from a dialog; the main loop
	// exits at the end of the frame so that the config is still saved.
	quit bool

	menuBarHeight float32
}

// selection is the facility / scenario group / scenario the picker is on.
type selection struct {
	facility string
	group    string
	scenario string
}

func newApp(config *Config, plat platform.Platform, render renderer.Renderer, lg *log.Logger) (*app, error) {
	a := &app{
		config: config,
		plat:   plat,
		render: render,
		lg:     lg,
	}

	mgr, errorLogger, overrideErrors := client.MakeLocalServerManager(config.overrideFiles(),
		func() bool { return false }, lg,
		func(c *client.ControlClient) { a.setControlClient(c) },
		func(err error) { lg.Errorf("server: %v", err) })
	if errorLogger.HaveErrors() {
		return nil, fmt.Errorf("%s", errorLogger.String())
	}
	a.mgr = mgr
	if overrideErrors != "" {
		a.reloadErrors = strings.Split(strings.TrimSpace(overrideErrors), "\n")
	}

	a.scope.init()
	if render != nil {
		a.scope.initFonts(render, plat)
	}
	a.inspector.init(config)

	// Held aside because startSim writes the facility it actually opened
	// back to the config.
	wantFacility := config.LastFacility
	a.sel = selection{facility: config.LastFacility, group: config.LastGroup, scenario: config.LastScenario}
	a.normalizeSelection()
	if a.sel.scenario != "" {
		a.startSim()
	}
	if wantFacility != "" && wantFacility != a.sel.facility {
		// Substituting silently would leave someone who mistyped a facility
		// staring at the wrong airspace and wondering why. This goes after
		// startSim, which clears the status line on success.
		a.status = fmt.Sprintf("no facility %q; opened %s instead", wantFacility, a.sel.facility)
		lg.Warnf("%s", a.status)
	}

	// Neither of these has anywhere to put a dialog under -selftest, which
	// runs without a window.
	if plat != nil {
		a.newReleaseChan = make(chan *util.Release)
		go checkForNewRelease(a.newReleaseChan, lg)

		if config.WhatsNewIndex < len(whatsNew) {
			a.showDialog(&whatsNewDialog{config: config})
		}
	}

	return a, nil
}

func (a *app) catalogs() map[string]map[string]*scenario.Catalog {
	if a.mgr == nil || a.mgr.LocalServer == nil {
		return nil
	}
	return a.mgr.LocalServer.GetScenarioCatalogs()
}

// normalizeSelection fixes up a selection that names a facility, group, or
// scenario the loaded scenarios don't have, which happens both on a first
// run and after an edit renames something.
func (a *app) normalizeSelection() {
	catalogs := a.catalogs()
	if len(catalogs) == 0 {
		a.sel = selection{}
		return
	}

	groups, ok := catalogs[a.sel.facility]
	if !ok {
		a.sel.facility, groups = util.FirstSortedMapEntry(catalogs)
		a.sel.group = ""
	}

	// A scenario may be named without its group, both from -scenarioname and
	// after an edit moves one; look for it across the facility's groups
	// before falling back.
	if a.sel.scenario != "" && !hasScenario(groups[a.sel.group], a.sel.scenario) {
		for name, catalog := range util.SortedMap(groups) {
			if hasScenario(catalog, a.sel.scenario) {
				a.sel.group = name
				break
			}
		}
	}

	catalog, ok := groups[a.sel.group]
	if !ok {
		a.sel.group, catalog = util.FirstSortedMapEntry(groups)
	}

	if _, ok := catalog.Scenarios[a.sel.scenario]; !ok {
		a.sel.scenario = catalog.DefaultScenario
		if _, ok := catalog.Scenarios[a.sel.scenario]; !ok {
			a.sel.scenario, _ = util.FirstSortedMapEntry(catalog.Scenarios)
		}
	}
}

func hasScenario(catalog *scenario.Catalog, scenario string) bool {
	if catalog == nil {
		return false
	}
	_, ok := catalog.Scenarios[scenario]
	return ok
}

func (a *app) selectedSpec() *scenario.Spec {
	catalogs := a.catalogs()
	if catalog, ok := catalogs[a.sel.facility][a.sel.group]; ok {
		return catalog.Scenarios[a.sel.scenario]
	}
	return nil
}

// startSim creates a sim for the selected scenario. Automatic traffic is
// off to start with: an empty scope is what you want when you are checking
// geometry, and the Launch tab turns it back on.
func (a *app) startSim() {
	spec := a.selectedSpec()
	if spec == nil {
		a.status = "no scenario selected"
		return
	}

	req := server.MakeNewSimRequest()
	req.Facility = a.sel.facility
	req.GroupName = a.sel.group
	req.ScenarioName = a.sel.scenario
	req.ScenarioSpec = spec
	req.StartTime = simStartTime()
	req.Initials = controllerInitials
	req.Privileged = true // so that any aircraft can be given commands

	lc := &req.ScenarioSpec.LaunchConfig
	lc.DepartureRateScale = 0
	lc.InboundFlowRateScale = 0
	lc.VFRDepartureRateScale = 0
	lc.EmergencyAircraftRate = 0
	lc.DepartureMode = sim.LaunchManual
	lc.ArrivalMode = sim.LaunchManual
	lc.OverflightMode = sim.LaunchManual

	if err := a.mgr.CreateNewSim(req, controllerInitials, a.mgr.LocalServer, a.lg); err != nil {
		a.status = fmt.Sprintf("unable to start %s: %v", a.sel.scenario, err)
		a.lg.Errorf("CreateNewSim: %v", err)
		return
	}

	a.config.LastFacility, a.config.LastGroup, a.config.LastScenario = a.sel.facility, a.sel.group, a.sel.scenario
	a.status = ""
}

func (a *app) setControlClient(c *client.ControlClient) {
	a.cc = c
	a.inspector.resetSim()
	title := "backshop"
	if c != nil {
		a.scope.resetSim(c, a.sel.facility+"/"+a.sel.group+"/"+a.sel.scenario)
		title += ": " + c.State.Facility + " / " + a.sel.scenario
	}
	// The platform is nil under -selftest, which runs without a window.
	if a.plat != nil {
		a.plat.SetWindowTitle(title)
	}
}

func (a *app) update() {
	a.mgr.Update(a.plat, a.lg)

	a.pollNewRelease()

	a.inspector.videoMaps.pollCRC(a)
	a.inspector.traffic.poll()
	// Playback runs off the wall clock, so it has to be stepped every frame
	// rather than only while the Launch tab is in front.
	a.inspector.playback.advance()

	if a.reload != nil && a.reload.Done() {
		result, err := a.reload.Result()
		a.reload = nil
		switch {
		case err != nil:
			a.reloadErrors = []string{err.Error()}
			a.status = "reload failed"
		case len(result.Errors) > 0:
			a.reloadErrors = result.Errors
			a.status = fmt.Sprintf("reload found %d error(s); scenarios unchanged", len(result.Errors))
		default:
			a.reloadErrors = nil
			if result.OverrideErrors != "" {
				a.reloadErrors = strings.Split(strings.TrimSpace(result.OverrideErrors), "\n")
			}
			a.normalizeSelection()
			a.startSim()
			a.status = "reloaded"
		}
	}
}

// reloadScenarios re-reads the scenario JSON from disk and restarts the sim
// on the reloaded scenario, which is the whole point of the tool: the edit
// loop shouldn't include relaunching.
func (a *app) reloadScenarios() {
	if a.reload != nil || a.mgr.LocalServer == nil {
		return
	}
	a.reload = a.mgr.LocalServer.ReloadScenarios(a.reloadArgs())
	a.status = "reloading..."
}

// reloadArgs are the override files the user has picked; they are sent with
// every reload so that a new choice takes effect without restarting.
func (a *app) reloadArgs() server.ReloadScenariosArgs {
	return server.ReloadScenariosArgs{Overrides: a.config.overrideFiles()}
}

// reloadShortcut is how the reload key is written in the UI. Control-R
// works everywhere; on a Mac so does Command-R, which is the one a Mac user
// reaches for.
var reloadShortcut = util.Select(runtime.GOOS == "darwin", "Cmd+R", "Ctrl+R")

// reloadKeyPressed reports whether the user just asked for a reload from
// the keyboard.
func reloadKeyPressed() bool {
	io := imgui.CurrentIO()
	if io.WantCaptureKeyboard() || !imgui.IsKeyPressedBool(imgui.KeyR) {
		return false
	}
	return io.KeyCtrl() || (runtime.GOOS == "darwin" && io.KeySuper())
}

func (a *app) draw() {
	if reloadKeyPressed() {
		a.reloadScenarios()
	}

	a.drawMenuBar()

	if a.cc != nil {
		// Nothing here acts on sim events, but they have to be drained or
		// the client's queue grows without bound.
		a.cc.DrainEvents()
	}

	a.scope.draw(a, a.menuBarHeight)
	a.inspector.draw(a)

	a.drawDialogs()
}

func (a *app) drawMenuBar() {
	if !imgui.BeginMainMenuBar() {
		return
	}

	if imgui.BeginMenu("File") {
		if imgui.MenuItemBoolV("Reload scenarios", reloadShortcut, false, a.reload == nil) {
			a.reloadScenarios()
		}
		imgui.EndMenu()
	}

	if imgui.BeginMenu("View") {
		if imgui.MenuItemBool("Reset view") {
			a.scope.resetView(a.cc)
		}
		imgui.EndMenu()
	}

	a.drawToolButtons()

	// The status line and the cursor's position live in the menu bar so
	// that the map itself stays uncluttered.
	imgui.SameLineV(imgui.WindowWidth()-460, 0)
	if a.scope.haveCursorLatLong {
		p := a.scope.cursorLatLong
		dms := strings.ReplaceAll(p.DMSString(), " ", "")
		if imgui.SmallButton(dms + "##copy") {
			a.plat.GetClipboard().SetClipboard(dms)
		}
		imgui.SameLine()
		imgui.Text(fmt.Sprintf("(%.6f, %.6f)", p[1], p[0]))
	}
	if a.status != "" {
		imgui.SameLine()
		imgui.TextColored(imgui.Vec4{X: 1, Y: 0.8, Z: 0.3, W: 1}, a.status)
	}

	a.menuBarHeight = imgui.WindowSize().Y
	imgui.EndMainMenuBar()
}

// drawToolButtons draws what a click on the map does. It lives in the menu
// bar rather than in a tab because it applies whatever the inspector is
// showing.
func (a *app) drawToolButtons() {
	for _, t := range []tool{toolNone, toolDrawRoute, toolMeasure} {
		imgui.SameLine()
		active := a.scope.activeTool == t
		if active {
			imgui.PushStyleColorVec4(imgui.ColButton, imgui.Vec4{X: 0.2, Y: 0.45, Z: 0.75, W: 1})
		}
		if imgui.Button(t.icon()) {
			a.scope.setTool(t)
		}
		if active {
			imgui.PopStyleColor()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip(t.help())
		}
	}

	imgui.SameLine()
	imgui.BeginDisabledV(!a.scope.haveToolPoints())
	if imgui.Button(gui.Icons.Trash) {
		a.scope.clearTool()
	}
	imgui.EndDisabled()
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Clear the drawn route or measurement")
	}
}

func (a *app) shutdown() {
	if a.mgr != nil {
		a.mgr.Disconnect()
	}
}

// copyButton draws a small button that puts s on the clipboard; every piece
// of text backshop shows is bound for hand-written JSON.
func (a *app) copyButton(id, s string) {
	if imgui.SmallButton("copy##" + id) {
		a.plat.GetClipboard().SetClipboard(s)
	}
}

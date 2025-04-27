// launch.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

var (
	airportWind sync.Map
	windRequest = make(map[string]chan struct{})
)

type NewSimConfiguration struct {
	server.NewSimConfiguration

	selectedTRACONConfigs map[string]*server.Configuration

	displayError error

	mgr            *server.ConnectionManager
	selectedServer *server.Server
	defaultTRACON  *string
	tfrCache       *av.TFRCache
	lg             *log.Logger
}

func MakeNewSimConfiguration(mgr *server.ConnectionManager, defaultTRACON *string, tfrCache *av.TFRCache, lg *log.Logger) *NewSimConfiguration {
	c := &NewSimConfiguration{
		lg:                  lg,
		mgr:                 mgr,
		selectedServer:      mgr.LocalServer,
		defaultTRACON:       defaultTRACON,
		tfrCache:            tfrCache,
		NewSimConfiguration: server.MakeNewSimConfiguration(),
	}

	c.SetTRACON(*defaultTRACON)

	return c
}

func (c *NewSimConfiguration) SetTRACON(name string) {
	var ok bool
	configs := c.selectedServer.GetConfigs()
	if c.selectedTRACONConfigs, ok = configs[name]; !ok {
		if name != "" {
			c.lg.Errorf("%s: TRACON not found!", name)
		}
		// Pick one at random
		name = util.SortedMapKeys(configs)[rand.Intn(len(configs))]
		c.selectedTRACONConfigs = configs[name]
	}
	c.TRACONName = name
	c.GroupName = util.SortedMapKeys(c.selectedTRACONConfigs)[0]

	c.SetScenario(c.GroupName, c.selectedTRACONConfigs[c.GroupName].DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var groupConfig *server.Configuration
	if groupConfig, ok = c.selectedTRACONConfigs[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.TRACONName)
		groupName = util.SortedMapKeys(c.selectedTRACONConfigs)[0]
		groupConfig = c.selectedTRACONConfigs[c.GroupName]
	}
	c.GroupName = groupName

	if c.Scenario, ok = groupConfig.ScenarioConfigs[scenarioName]; !ok {
		if scenarioName != "" {
			c.lg.Errorf("%s: scenario not found in group %s", scenarioName, c.GroupName)
		}
		scenarioName = groupConfig.DefaultScenario
		c.Scenario = groupConfig.ScenarioConfigs[scenarioName]
	}
	c.ScenarioName = scenarioName
}

func (c *NewSimConfiguration) UIButtonText() string {
	return util.Select(c.NewSimType == server.NewSimJoinRemote, "Join", "Next")
}

func (c *NewSimConfiguration) ShowRatesWindow() bool {
	return c.NewSimType == server.NewSimCreateLocal || c.NewSimType == server.NewSimCreateRemote
}

func (c *NewSimConfiguration) DrawUI(p platform.Platform) bool {
	if err := c.mgr.UpdateRemoteSims(); err != nil {
		c.lg.Warnf("UpdateRemoteSims: %v", err)
	}

	if c.displayError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		if errors.Is(c.displayError, server.ErrRPCTimeout) || util.IsRPCServerError(c.displayError) {
			imgui.Text("Unable to reach vice server")
		} else if errors.Is(c.displayError, server.ErrInvalidPassword) {
			imgui.Text("Invalid password entered")
		} else {
			imgui.Text(c.displayError.Error())
		}
		imgui.PopStyleColor()
		imgui.Separator()
	}

	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if c.mgr.RemoteServer != nil {
		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Server type:")

			origType := c.NewSimType

			imgui.TableNextColumn()
			if imgui.RadioButtonIntPtr("Create single-controller", &c.NewSimType, server.NewSimCreateLocal) &&
				origType != server.NewSimCreateLocal {
				c.selectedServer = c.mgr.LocalServer
				c.SetTRACON(*c.defaultTRACON)
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			if imgui.RadioButtonIntPtr("Create multi-controller", &c.NewSimType, server.NewSimCreateRemote) &&
				origType != server.NewSimCreateRemote {
				c.selectedServer = c.mgr.RemoteServer
				c.SetTRACON(*c.defaultTRACON)
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			if len(c.mgr.RemoteServer.GetRunningSims()) == 0 {
				imgui.BeginDisabled()
			}
			if imgui.RadioButtonIntPtr("Join multi-controller", &c.NewSimType, server.NewSimJoinRemote) &&
				origType != server.NewSimJoinRemote {
				c.selectedServer = c.mgr.RemoteServer
				c.displayError = nil
			}
			if len(c.mgr.RemoteServer.GetRunningSims()) == 0 {
				imgui.EndDisabled()
			}

			imgui.EndTable()
		}
	} else {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Unable to connect to the multi-controller vice server; " +
			"only single-player scenarios are available.")
		imgui.PopStyleColor()
		c.NewSimType = server.NewSimCreateLocal
	}
	imgui.Separator()

	if c.NewSimType == server.NewSimCreateLocal || c.NewSimType == server.NewSimCreateRemote {
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 600, tableScale * 300}, 0.) {
			imgui.TableSetupColumn("ARTCC")
			imgui.TableSetupColumn("ATCT/TRACON")
			imgui.TableSetupColumn("Scenario")
			imgui.TableHeadersRow()
			imgui.TableNextRow()

			// ARTCCs
			artccs := make(map[string]interface{})
			allTRACONs := util.SortedMapKeys(c.selectedServer.GetConfigs())
			for _, tracon := range allTRACONs {
				artccs[av.DB.TRACONs[tracon].ARTCC] = nil
			}
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("artccs", imgui.Vec2{tableScale * 150, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for _, artcc := range util.SortedMapKeys(artccs) {
					label := fmt.Sprintf("%s (%s)", artcc, strings.ReplaceAll(av.DB.ARTCCs[artcc].Name, " Center", ""))
					if imgui.SelectableBoolV(label, artcc == av.DB.TRACONs[c.TRACONName].ARTCC, 0, imgui.Vec2{}) &&
						artcc != av.DB.TRACONs[c.TRACONName].ARTCC {
						// a new ARTCC was chosen; reset the TRACON to the first one with that ARTCC
						idx := slices.IndexFunc(allTRACONs, func(tracon string) bool { return artcc == av.DB.TRACONs[tracon].ARTCC })
						c.SetTRACON(allTRACONs[idx])
					}
				}
			}
			imgui.EndChild()

			// TRACONs for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("tracons", imgui.Vec2{tableScale * 150, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for _, tracon := range allTRACONs {
					if av.DB.TRACONs[tracon].ARTCC != av.DB.TRACONs[c.TRACONName].ARTCC {
						continue
					}
					name := strings.TrimSuffix(av.DB.TRACONs[tracon].Name, " TRACON")
					name = strings.TrimSuffix(name, " ATCT/TRACON")
					name = strings.TrimSuffix(name, " Tower")
					label := fmt.Sprintf("%s (%s)", tracon, name)
					if imgui.SelectableBoolV(label, tracon == c.TRACONName, 0, imgui.Vec2{}) && tracon != c.TRACONName {
						// TRACON selected
						c.SetTRACON(tracon)
					}
				}
			}
			imgui.EndChild()

			// Scenarios for the tracon
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("scenarios", imgui.Vec2{tableScale * 300, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for _, groupName := range util.SortedMapKeys(c.selectedTRACONConfigs) {
					group := c.selectedTRACONConfigs[groupName]
					for _, name := range util.SortedMapKeys(group.ScenarioConfigs) {
						if imgui.SelectableBoolV(name, name == c.ScenarioName, 0, imgui.Vec2{}) {
							c.SetScenario(groupName, name)
						}
					}
				}
			}
			imgui.EndChild()

			imgui.EndTable()
		}

		if sc := c.Scenario.SplitConfigurations; sc.Len() > 1 {
			if imgui.BeginComboV("Split", c.Scenario.SelectedSplit, imgui.ComboFlagsHeightLarge) {
				for _, split := range sc.Splits() {
					if imgui.SelectableBoolV(split, split == c.Scenario.SelectedSplit, 0, imgui.Vec2{}) {
						var err error
						c.Scenario.SelectedSplit = split
						c.Scenario.SelectedController, err = sc.GetPrimaryController(split)
						if err != nil {
							c.lg.Errorf("unable to find primary controller: %v", err)
						}
					}
				}
				imgui.EndCombo()
			}
		}

		if imgui.BeginTableV("scenario", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			if c.NewSimType == server.NewSimCreateRemote {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Name:")
				imgui.TableNextColumn()
				imgui.Text(c.NewSimName)
			}

			fmtPosition := func(id string) string {
				if tracon := c.selectedTRACONConfigs[c.GroupName]; tracon != nil {
					if ctrl, ok := tracon.ControlPositions[id]; ok {
						id += " (" + ctrl.Position + ")"
					}
				}
				return id
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Control Position:")
			imgui.TableNextColumn()
			imgui.Text(fmtPosition(c.Scenario.SelectedController))
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Checkbox("Allow Instructor Sign-ins", &c.InstructorAllowed)

			if len(c.Scenario.ArrivalRunways) > 0 {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Landing:")
				imgui.TableNextColumn()

				var a []string
				for _, rwy := range c.Scenario.ArrivalRunways {
					a = append(a, rwy.Airport+"/"+rwy.Runway)
				}
				sort.Strings(a)
				imgui.Text(strings.Join(a, ", "))
			}
			validAirport := c.Scenario.PrimaryAirport != "KAAC"

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Wind:")
			if !validAirport {
				imgui.BeginDisabled()
			}
			imgui.Checkbox("Live Weather", &c.LiveWeather)
			if !validAirport {
				c.LiveWeather = false
			}
			if !validAirport {
				imgui.EndDisabled()
			}

			if c.NewSimType == server.NewSimCreateRemote {
				imgui.Checkbox("Require Password", &c.RequirePassword)
				if c.RequirePassword {
					imgui.InputTextMultiline("Password", &c.Password, imgui.Vec2{}, 0, nil)
					if c.Password == "" {
						imgui.SameLine()
						imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
						imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
						imgui.PopStyleColor()
					}
				}
			}

			imgui.TableNextColumn()
			wind := c.Scenario.Wind
			if c.LiveWeather {
				if w, ok := airportWind.Load(c.Scenario.PrimaryAirport); !ok {
					primary := c.Scenario.PrimaryAirport
					if wind, ok = getWind(primary, c.lg); !ok {
						wind = c.Scenario.Wind
					}
				} else {
					wind = w.(av.Wind)
				}
			}

			var dir string
			if wind.Direction == -1 {
				dir = "Variable"
			} else {
				dir = fmt.Sprintf("%03d", wind.Direction)
			}

			if wind.Gust > wind.Speed {
				imgui.Text(fmt.Sprintf("%s at %d gust %d", dir, wind.Speed, wind.Gust))
			} else {
				imgui.Text(fmt.Sprintf("%s at %d", dir, wind.Speed))
			}

			if !c.LiveWeather {
				imgui.BeginDisabled()
			}
			refresh := imgui.Button("Refresh Weather")
			if refresh {
				refreshWeather()
			}
			if !c.LiveWeather {
				imgui.EndDisabled()
			}
			imgui.EndTable()
		}
	} else {
		// Join remote
		runningSims := c.mgr.RemoteServer.GetRunningSims()

		rs, ok := runningSims[c.SelectedRemoteSim]
		if !ok || c.SelectedRemoteSim == "" {
			c.SelectedRemoteSim = util.SortedMapKeys(runningSims)[0]

			rs = runningSims[c.SelectedRemoteSim]
			if _, ok := rs.CoveredPositions[rs.PrimaryController]; !ok {
				// If the primary position isn't currently covered, make that the default selection.
				c.SelectedRemoteSimPosition = rs.PrimaryController
			}
		}

		imgui.Text("Available simulations:")
		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingFixedFit
		if imgui.BeginTableV("simulation", 4, flags, imgui.Vec2{tableScale * 700, 0}, 0.) {
			imgui.TableSetupColumn("") // lock
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Configuration")
			imgui.TableSetupColumn("Controllers")
			imgui.TableHeadersRow()

			for _, simName := range util.SortedMapKeys(runningSims) {
				rs := runningSims[simName]
				if len(rs.AvailablePositions) == 0 {
					// No open positions left; don't even offer it.
					continue
				}

				imgui.PushIDStr(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(renderer.FontAwesomeIconLock)
				}
				imgui.TableNextColumn()

				selected := simName == c.SelectedRemoteSim
				selFlags := imgui.SelectableFlagsSpanAllColumns | imgui.SelectableFlagsNoAutoClosePopups
				if imgui.SelectableBoolV(simName, selected, selFlags, imgui.Vec2{}) {
					c.SelectedRemoteSim = simName

					rs = runningSims[c.SelectedRemoteSim]
					if _, ok := rs.CoveredPositions[rs.PrimaryController]; !ok {
						// If the primary position isn't currently covered, make that the default selection.
						c.SelectedRemoteSimPosition = rs.PrimaryController
					}
				}

				imgui.TableNextColumn()
				imgui.Text(runningSims[simName].ScenarioName)

				imgui.TableNextColumn()
				covered, available := len(rs.CoveredPositions), len(rs.AvailablePositions)
				controllers := fmt.Sprintf("%d / %d", covered, covered+available)
				imgui.Text(controllers)
				if imgui.IsItemHovered() && len(rs.CoveredPositions) > 0 {
					imgui.SetTooltip(strings.Join(util.SortedMapKeys(rs.CoveredPositions), ", "))
				}

				imgui.PopID()
			}
			imgui.EndTable()
		}

		// Handle the case of someone else signing in to the position
		if _, ok := rs.AvailablePositions[c.SelectedRemoteSimPosition]; c.SelectedRemoteSimPosition != "Observer" && !ok {
			c.SelectedRemoteSimPosition = util.SortedMapKeys(rs.AvailablePositions)[0]
		}

		fmtPosition := func(id string) string {
			if ctrl, ok := rs.AvailablePositions[id]; ok {
				id += " (" + ctrl.Position + ")"
			}
			return id
		}

		if imgui.BeginCombo("Position", fmtPosition(c.SelectedRemoteSimPosition)) {
			for _, pos := range util.SortedMapKeys(rs.AvailablePositions) {
				if pos[0] == '_' {
					continue
				}

				if imgui.SelectableBoolV(fmtPosition(pos), pos == c.SelectedRemoteSimPosition, 0, imgui.Vec2{}) {
					c.SelectedRemoteSimPosition = pos
				}
			}

			if imgui.SelectableBoolV("Observer", "Observer" == c.SelectedRemoteSimPosition, 0, imgui.Vec2{}) {
				c.SelectedRemoteSimPosition = "Observer"
			}

			imgui.EndCombo()
		}
		if rs.RequirePassword {
			imgui.InputTextMultiline("Password", &c.RemoteSimPassword, imgui.Vec2{}, 0, nil)
		}
		if !rs.InstructorAllowed {
			imgui.BeginDisabled()
		}
		imgui.Checkbox("Sign-in as Instructor", &c.Instructor)
		if !rs.InstructorAllowed {
			imgui.EndDisabled()
		}
	}

	return false
}

func (c *NewSimConfiguration) DrawRatesUI(p platform.Platform) bool {
	drawDepartureUI(&c.Scenario.LaunchConfig, p)
	drawVFRDepartureUI(&c.Scenario.LaunchConfig, p)
	drawArrivalUI(&c.Scenario.LaunchConfig, p)
	drawOverflightUI(&c.Scenario.LaunchConfig, p)
	return false
}

func scaleRate(rate, scale float32) float32 {
	rate *= scale
	if rate <= 0.5 {
		// Since we round to the nearest int when displaying rates in the UI,
		// we don't want to ever launch for ones that have rate 0.
		return 0
	}
	return rate
}

func sumRateMap2(rates map[string]map[string]float32, scale float32) float32 {
	var sum float32
	for _, categoryRates := range rates {
		for _, rate := range categoryRates {
			sum += scaleRate(rate, scale)
		}
	}
	return sum
}

func getWind(airport string, lg *log.Logger) (av.Wind, bool) {
	for key, done := range windRequest {
		select {
		case <-done:
			delete(windRequest, key)
		default:
			// no wind yet
		}
	}

	if wind, ok := airportWind.Load(airport); ok {
		// The wind is in the map
		return wind.(av.Wind), true
	} else if _, ok := windRequest[airport]; ok {
		// it's been requested, but we don't have it yet
		return av.Wind{}, false
	} else {
		// It hasn't been requested nor is in airportWind
		done := make(chan struct{}, 1)
		windRequest[airport] = done
		go func(done chan<- struct{}, airport string) {
			defer close(done)

			weather, err := av.GetWeather(airport)
			if err != nil {
				lg.Errorf("%v", err)
				return
			}

			airportWind.Store(airport, weather[0].Wind)
		}(done, airport)

		return av.Wind{}, false
	}
}

func refreshWeather() {
	var wg sync.WaitGroup

	// Wait for all active requests to complete.
	wg.Add(len(windRequest))
	for _, ch := range windRequest {
		go func(done <-chan struct{}) {
			defer wg.Done()

			<-done
		}(ch)
	}
	wg.Wait()
	clear(windRequest)

	airportWind.Clear()
}

func (c *NewSimConfiguration) OkDisabled() bool {
	return c.NewSimType == server.NewSimCreateRemote && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
}

func (c *NewSimConfiguration) Start() error {
	c.TFRs = c.tfrCache.TFRsForTRACON(c.TRACONName, c.lg)

	if err := c.mgr.CreateNewSim(c.NewSimConfiguration, c.selectedServer); err != nil {
		c.lg.Errorf("CreateNewSim failed: %v", err)
		return err
	} else {
		*c.defaultTRACON = c.TRACONName
		return nil
	}
}

func drawDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if len(lc.DepartureRates) == 0 {
		return
	}

	imgui.Text("Departures")

	var sumRates float32
	airportRunwayNumCategories := make(map[string]int) // key is e.g. JFK/22R, then count of active categories
	for ap, runwayRates := range lc.DepartureRates {
		sumRates += sumRateMap2(runwayRates, lc.DepartureRateScale)

		for rwy, categories := range runwayRates {
			airportRunwayNumCategories[ap+"/"+rwy] = airportRunwayNumCategories[ap+"/"+rwy] + len(categories)
		}
	}
	maxDepartureCategories := 0
	for _, n := range airportRunwayNumCategories {
		maxDepartureCategories = math.Max(n, maxDepartureCategories)
	}

	imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", int(sumRates+0.5)))

	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("Departure rate scale", &lc.DepartureRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if lc.DepartureRateScale == 0 {
		imgui.BeginDisabled()
	}
	adrColumns := math.Min(3, maxDepartureCategories)
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", int32(2+2*adrColumns), flags, imgui.Vec2{tableScale * float32(200+200*adrColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		imgui.TableSetupColumn("Runway")
		for range adrColumns {
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
		}
		imgui.TableHeadersRow()

		for _, airport := range util.SortedMapKeys(lc.DepartureRates) {
			imgui.PushIDStr(airport)
			for _, runway := range util.SortedMapKeys(lc.DepartureRates[airport]) {
				imgui.PushIDStr(runway)
				adrColumn := 0

				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(airport)
				imgui.TableNextColumn()
				rshort, _, _ := strings.Cut(runway, ".") // don't include extras in the UI
				imgui.Text(rshort)
				imgui.TableNextColumn()

				for _, category := range util.SortedMapKeys(lc.DepartureRates[airport][runway]) {
					imgui.PushIDStr(category)

					if adrColumn > 0 && adrColumn%adrColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.TableNextColumn()
						imgui.TableNextColumn()
					}

					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					r := int32(lc.DepartureRateScale*lc.DepartureRates[airport][runway][category] + 0.5)
					if imgui.InputIntV("##adr", &r, 0, 120, 0) {
						lc.DepartureRates[airport][runway][category] = float32(r) / lc.DepartureRateScale
						changed = true
					}
					imgui.TableNextColumn()
					adrColumn++

					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.DepartureRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawVFRDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if len(lc.VFRAirports) == 0 {
		return
	}

	sumVFRRates := 0
	for _, ap := range lc.VFRAirports {
		r := float32(ap.VFRRateSum()) * lc.VFRDepartureRateScale
		if r > 0 {
			sumVFRRates += int(r)
		}
	}
	imgui.Text(fmt.Sprintf("Overall VFR departure rate: %d / hour", sumVFRRates))
	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("VFR reparture rate scale", &lc.VFRDepartureRateScale, 0, 2, "%.1f", imgui.SliderFlagsNoInput) || changed

	imgui.Separator()

	return
}

func drawArrivalUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Figure out the maximum number of inbound flows per airport to figure
	// out the number of table columns and also sum up the overall arrival
	// rate.
	var sumRates float32
	numAirportFlows := make(map[string]int)
	for _, agr := range lc.InboundFlowRates {
		for ap, rate := range agr {
			rate = scaleRate(rate, lc.InboundFlowRateScale)
			if ap != "overflights" {
				numAirportFlows[ap] = numAirportFlows[ap] + 1
				sumRates += rate
			}
		}
	}
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	maxAirportFlows := 0
	for _, n := range numAirportFlows {
		maxAirportFlows = math.Max(n, maxAirportFlows)
	}

	imgui.Text("Arrivals")
	imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", int(sumRates+0.5)))

	changed = imgui.SliderFloatV("Arrival/overflight rate scale", &lc.InboundFlowRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	changed = imgui.Checkbox("Include random arrival pushes", &lc.ArrivalPushes) || changed
	if !lc.ArrivalPushes {
		imgui.BeginDisabled()
	}
	freq := int32(lc.ArrivalPushFrequencyMinutes)
	changed = imgui.SliderInt("Push frequency (minutes)", &freq, 3, 60) || changed
	lc.ArrivalPushFrequencyMinutes = int(freq)
	min := int32(lc.ArrivalPushLengthMinutes)
	changed = imgui.SliderInt("Length of push (minutes)", &min, 5, 30) || changed
	lc.ArrivalPushLengthMinutes = int(min)
	if !lc.ArrivalPushes {
		imgui.EndDisabled()
	}

	aarColumns := math.Min(3, maxAirportFlows)
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("arrivalgroups", int32(1+2*aarColumns), flags, imgui.Vec2{tableScale * float32(150+250*aarColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range aarColumns {
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("AAR")
		}
		imgui.TableHeadersRow()

		for _, ap := range util.SortedMapKeys(numAirportFlows) {
			imgui.PushIDStr(ap)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(ap)

			aarCol := 0
			for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
				imgui.PushIDStr(group)
				if rate, ok := lc.InboundFlowRates[group][ap]; ok {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					r := int32(rate*lc.InboundFlowRateScale + 0.5)
					if imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0) {
						changed = true
						lc.InboundFlowRates[group][ap] = float32(r) / lc.InboundFlowRateScale
					}
					aarCol++

				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawOverflightUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Sum up the overall overflight rate
	overflightGroups := make(map[string]interface{})
	var sumRates float32
	for group, rates := range lc.InboundFlowRates {
		if rate, ok := rates["overflights"]; ok {
			rate = scaleRate(rate, lc.InboundFlowRateScale)
			sumRates += rate
			overflightGroups[group] = nil
		}
	}
	if len(overflightGroups) == 0 {
		return
	}

	imgui.Text("Overflights")
	imgui.Text(fmt.Sprintf("Overall overflight rate: %d / hour", int(sumRates+0.5)))

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("overflights", 2, flags, imgui.Vec2{tableScale * 400, 0}, 0.) {
		imgui.TableSetupColumn("Group")
		imgui.TableSetupColumn("Rate")
		imgui.TableHeadersRow()

		for _, group := range util.SortedMapKeys(overflightGroups) {
			imgui.PushIDStr(group)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(group)
			imgui.TableNextColumn()
			r := int32(lc.InboundFlowRates[group]["overflights"]*lc.InboundFlowRateScale + 0.5)
			if imgui.InputIntV("##of", &r, 0, 120, 0) {
				changed = true
				lc.InboundFlowRates[group]["overflights"] = float32(r) / lc.InboundFlowRateScale
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	return
}

///////////////////////////////////////////////////////////////////////////

type LaunchControlWindow struct {
	client              *server.ControlClient
	departures          []*LaunchDeparture
	vfrDepartures       []*LaunchDeparture
	arrivalsOverflights []*LaunchArrivalOverflight
	lg                  *log.Logger
}

type LaunchDeparture struct {
	Aircraft           sim.Aircraft
	Airport            string
	Runway             string
	Category           string
	LastLaunchCallsign av.ADSBCallsign
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (ld *LaunchDeparture) Reset() {
	ld.LastLaunchCallsign = ""
	ld.LastLaunchTime = time.Time{}
	ld.TotalLaunches = 0
}

type LaunchArrivalOverflight struct {
	Aircraft           sim.Aircraft
	Group              string
	Airport            string
	LastLaunchCallsign av.ADSBCallsign
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (la *LaunchArrivalOverflight) Reset() {
	la.LastLaunchCallsign = ""
	la.LastLaunchTime = time.Time{}
	la.TotalLaunches = 0
}

func MakeLaunchControlWindow(client *server.ControlClient, lg *log.Logger) *LaunchControlWindow {
	lc := &LaunchControlWindow{client: client}

	config := &client.State.LaunchConfig
	for _, airport := range util.SortedMapKeys(config.DepartureRates) {
		runwayRates := config.DepartureRates[airport]
		for _, rwy := range util.SortedMapKeys(runwayRates) {
			for _, category := range util.SortedMapKeys(runwayRates[rwy]) {
				lc.departures = append(lc.departures, &LaunchDeparture{
					Airport:  airport,
					Runway:   rwy,
					Category: category,
				})
			}
		}
	}
	for i := range lc.departures {
		lc.spawnIFRDeparture(lc.departures[i])
	}

	for _, airport := range util.SortedMapKeys(config.VFRAirports) {
		rwy := client.State.VFRRunways[airport]
		lc.vfrDepartures = append(lc.vfrDepartures, &LaunchDeparture{Airport: airport, Runway: rwy.Id})
	}
	for i := range lc.vfrDepartures {
		lc.spawnVFRDeparture(lc.vfrDepartures[i])
	}

	for _, group := range util.SortedMapKeys(config.InboundFlowRates) {
		for ap := range config.InboundFlowRates[group] {
			lc.arrivalsOverflights = append(lc.arrivalsOverflights,
				&LaunchArrivalOverflight{
					Group:   group,
					Airport: ap,
				})
		}
	}
	for i := range lc.arrivalsOverflights {
		lc.spawnArrivalOverflight(lc.arrivalsOverflights[i])
	}

	return lc
}

func (lc *LaunchControlWindow) spawnIFRDeparture(dep *LaunchDeparture) {
	lc.client.CreateDeparture(dep.Airport, dep.Runway, dep.Category, av.FlightRulesIFR, &dep.Aircraft,
		func(err error) { lc.lg.Warnf("CreateDeparture: %v", err) })
}

func (lc *LaunchControlWindow) spawnVFRDeparture(dep *LaunchDeparture) {
	lc.client.CreateDeparture(dep.Airport, dep.Runway, dep.Category, av.FlightRulesVFR, &dep.Aircraft,
		func(err error) {
			if server.TryDecodeError(err) != sim.ErrViolatedAirspace {
				lc.lg.Warnf("CreateDeparture: %v", err)
			}
		})
}

func (lc *LaunchControlWindow) spawnArrivalOverflight(lac *LaunchArrivalOverflight) {
	if lac.Airport != "overflights" {
		lc.client.CreateArrival(lac.Group, lac.Airport, &lac.Aircraft,
			func(err error) { lc.lg.Warnf("CreateArrival: %v", err) })
	} else {
		lc.client.CreateOverflight(lac.Group, &lac.Aircraft,
			func(err error) { lc.lg.Warnf("CreateOverflight: %v", err) })
	}
}

func (lc *LaunchControlWindow) getLastDeparture(airport, runway string) (callsign av.ADSBCallsign, launch time.Time) {
	match := func(dep *LaunchDeparture) bool {
		return dep.Airport == airport && dep.Runway == runway
	}
	if idx := slices.IndexFunc(lc.departures, match); idx != -1 {
		callsign, launch = lc.departures[idx].LastLaunchCallsign, lc.departures[idx].LastLaunchTime
	}
	if idx := slices.IndexFunc(lc.vfrDepartures, match); idx != -1 {
		if callsign == "" || lc.vfrDepartures[idx].LastLaunchTime.After(launch) {
			callsign, launch = lc.vfrDepartures[idx].LastLaunchCallsign, lc.vfrDepartures[idx].LastLaunchTime
		}
	}
	return
}

func (lc *LaunchControlWindow) Draw(eventStream *sim.EventStream, p platform.Platform) {
	showLaunchControls := true
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, float32(p.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Launch Control", &showLaunchControls, imgui.WindowFlagsAlwaysAutoResize)

	ctrl := lc.client.State.LaunchConfig.Controller
	if lc.client.State.MultiControllers != nil {
		imgui.Text("Controlling controller: " + util.Select(ctrl == "", "(none)", ctrl))
		if ctrl == lc.client.State.UserTCP {
			if imgui.Button("Release launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		} else {
			if imgui.Button("Take launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		}
	}

	canLaunch := ctrl == lc.client.State.UserTCP || (lc.client.State.MultiControllers == nil && ctrl == "") ||
		lc.client.State.AmInstructor()
	if canLaunch {
		imgui.Text("Mode:")
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Manual", &lc.client.State.LaunchConfig.Mode, sim.LaunchManual) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic", &lc.client.State.LaunchConfig.Mode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}

		width, _ := ui.font.BoundText(renderer.FontAwesomeIconPlayCircle, 0)
		// Right-justify
		imgui.SameLine()
		imgui.Text("                            ")
		imgui.SameLine()
		//	imgui.SetCursorPos(imgui.Vec2{imgui.CursorPosX() + imgui.ContentRegionAvail().X - float32(3*width+10),
		imgui.SetCursorPos(imgui.Vec2{imgui.WindowWidth() - float32(7*width), imgui.CursorPosY()})
		if lc.client != nil && lc.client.Connected() {
			if lc.client.State.Paused {
				if imgui.Button(renderer.FontAwesomeIconPlayCircle) {
					lc.client.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Resume simulation")
				}
			} else {
				if imgui.Button(renderer.FontAwesomeIconPauseCircle) {
					lc.client.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Pause simulation")
				}
			}
		}

		imgui.SameLine()
		if imgui.Button(renderer.FontAwesomeIconTrash) {
			uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
				title: "Are you sure?",
				query: "All aircraft will be deleted. Go ahead?",
				ok: func() {
					lc.client.DeleteAllAircraft(nil)
					for _, dep := range lc.departures {
						dep.Reset()
					}
					for _, ac := range lc.arrivalsOverflights {
						ac.Reset()
					}
				},
			}, p), true)
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Delete all aircraft and restart")
		}

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

		if lc.client.State.LaunchConfig.Mode == sim.LaunchManual {
			mitAndTime := func(ac *sim.Aircraft, launchPosition math.Point2LL,
				lastLaunchCallsign av.ADSBCallsign, lastLaunchTime time.Time) {

				imgui.TableNextColumn()
				if prev, ok := lc.client.State.GetTrackByCallsign(lastLaunchCallsign); ok {
					dist := math.NMDistance2LL(prev.Location, launchPosition)
					imgui.Text(fmt.Sprintf("%.1f", dist))

					imgui.TableNextColumn()

					delta := lc.client.CurrentTime().Sub(lastLaunchTime).Round(time.Second).Seconds()
					m, s := int(delta)/60, int(delta)%60
					imgui.Text(fmt.Sprintf("%02d:%02d", m, s))
				} else {
					imgui.TableNextColumn()
				}
			}

			if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
				ndep := util.ReduceSlice(lc.departures, func(dep *LaunchDeparture, n int) int {
					return n + dep.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Departures: %d total", ndep))

				// Sort departures by airport, then runway, then category
				sortedDeps := util.DuplicateSlice(lc.departures)
				slices.SortFunc(sortedDeps, func(a, b *LaunchDeparture) int {
					return strings.Compare(a.Airport+"/"+a.Runway+"/"+a.Category,
						b.Airport+"/"+b.Runway+"/"+b.Category)
				})

				// Find the maximum number of categories for any airport
				maxCategories, curCategories := 0, 1
				lastAp := ""
				for _, d := range sortedDeps {
					if d.Airport != lastAp {
						maxCategories = math.Max(maxCategories, curCategories)
						curCategories = 1
						lastAp = d.Airport
					} else {
						curCategories++
					}
				}

				nColumns := math.Min(3, maxCategories)
				if imgui.BeginTableV("dep", int32(1+9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
					imgui.TableSetupColumn("Airport")
					for range nColumns {
						imgui.TableSetupColumn("Rwy")
						imgui.TableSetupColumn("Category")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("Type")
						imgui.TableSetupColumn("Exit")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()

					lastAp := ""
					curColumn := 0
					for _, dep := range sortedDeps {
						if dep.Airport != lastAp {
							imgui.TableNextRow()
							lastAp = dep.Airport
							curColumn = 0

							imgui.TableNextColumn()
							imgui.Text(dep.Airport)
						} else if curColumn+1 == nColumns {
							curColumn = 0
							imgui.TableNextRow()
							imgui.TableNextColumn()
						} else {
							curColumn++
						}

						imgui.TableNextColumn()
						rwy, _, _ := strings.Cut(dep.Runway, ".")
						imgui.Text(rwy)
						imgui.TableNextColumn()
						imgui.Text(dep.Category)

						imgui.PushIDStr(dep.Airport + " " + dep.Runway + " " + dep.Category)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(dep.TotalLaunches))

						if dep.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.AircraftType)

							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.Exit)

							lastCallsign, lastTime := lc.getLastDeparture(dep.Airport, dep.Runway)
							mitAndTime(&dep.Aircraft, dep.Aircraft.Position(), lastCallsign, lastTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchDeparture(dep.Aircraft, dep.Runway)
								dep.LastLaunchCallsign = dep.Aircraft.ADSBCallsign
								dep.LastLaunchTime = lc.client.CurrentTime()
								dep.TotalLaunches++

								dep.Aircraft = sim.Aircraft{}
								lc.spawnIFRDeparture(dep)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								dep.Aircraft = sim.Aircraft{}
								lc.spawnIFRDeparture(dep)
							}
						} else {
							for range 7 {
								imgui.TableNextColumn()
							}
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}

			if len(lc.vfrDepartures) > 0 && imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
				ndep := util.ReduceSlice(lc.vfrDepartures, func(dep *LaunchDeparture, n int) int {
					return n + dep.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("VFR Departures: %d total", ndep))

				nColumns := math.Min(2, len(lc.vfrDepartures))
				if imgui.BeginTableV("vfrdep", int32(9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
					for range nColumns {
						imgui.TableSetupColumn("Airport")
						imgui.TableSetupColumn("Rwy")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("Dest.")
						imgui.TableSetupColumn("Type")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()
					imgui.TableNextRow()

					for i, dep := range lc.vfrDepartures {
						if i%nColumns == 0 {
							imgui.TableNextRow()
						}

						imgui.PushIDStr(dep.Airport)
						imgui.TableNextColumn()
						imgui.Text(dep.Airport)
						imgui.TableNextColumn()
						imgui.Text(dep.Runway)
						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(dep.TotalLaunches))

						if dep.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.ArrivalAirport)

							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.AircraftType)

							lastCallsign, lastTime := lc.getLastDeparture(dep.Airport, dep.Runway)
							mitAndTime(&dep.Aircraft, dep.Aircraft.Position(), lastCallsign, lastTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchDeparture(dep.Aircraft, dep.Runway)
								dep.LastLaunchCallsign = dep.Aircraft.ADSBCallsign
								dep.LastLaunchTime = lc.client.CurrentTime()
								dep.TotalLaunches++

								dep.Aircraft = sim.Aircraft{}
								lc.spawnVFRDeparture(dep)
							}
						} else {
							// Since VFR routes are randomly sampled and then checked,
							// it may take a while to find a valid one; keep trying until
							// we get one.
							lc.spawnVFRDeparture(dep)
							for range 5 {
								imgui.TableNextColumn()
							}
						}
						imgui.TableNextColumn()
						if imgui.Button(renderer.FontAwesomeIconRedo) {
							dep.Aircraft = sim.Aircraft{}
							lc.spawnVFRDeparture(dep)
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}

			if imgui.CollapsingHeaderBoolPtr("Arrivals / Overflights", nil) {
				narof := util.ReduceSlice(lc.arrivalsOverflights, func(arr *LaunchArrivalOverflight, n int) int {
					return n + arr.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Arrivals/Overflights: %d total", narof))

				sortedInbound := util.DuplicateSlice(lc.arrivalsOverflights)
				slices.SortFunc(sortedInbound, func(a, b *LaunchArrivalOverflight) int {
					return strings.Compare(a.Airport+"/"+a.Group, b.Airport+"/"+b.Group)
				})

				maxGroups, numGroups := 0, 1
				lastAirport := ""
				for _, ao := range sortedInbound {
					if ao.Airport != lastAirport {
						maxGroups = math.Max(maxGroups, numGroups)
						lastAirport = ao.Airport
						numGroups = 1
					} else {
						numGroups++
					}
				}
				numColumns := math.Min(maxGroups, 3)

				if imgui.BeginTableV("arrof", int32(1+7*numColumns), flags, imgui.Vec2{tableScale * float32(100+350*numColumns), 0}, 0.0) {
					imgui.TableSetupColumn("Airport")
					for range numColumns {
						imgui.TableSetupColumn("Group")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("A/C Type")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()

					curColumn := 0
					lastAirport := ""
					for _, arof := range sortedInbound {
						if arof.Airport != lastAirport {
							imgui.TableNextRow()
							lastAirport = arof.Airport
							curColumn = 0
							imgui.TableNextColumn()
							imgui.Text(arof.Airport)
						} else if curColumn+1 == numColumns {
							curColumn = 0
							imgui.TableNextRow()
							imgui.TableNextColumn()
							imgui.Text("")
						} else {
							curColumn++
						}

						imgui.PushIDStr(arof.Group + arof.Airport)

						imgui.TableNextColumn()
						imgui.Text(arof.Group)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(arof.TotalLaunches))

						if arof.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(arof.Aircraft.FlightPlan.AircraftType)

							mitAndTime(&arof.Aircraft, arof.Aircraft.Position(), arof.LastLaunchCallsign,
								arof.LastLaunchTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchArrivalOverflight(arof.Aircraft)
								arof.LastLaunchCallsign = arof.Aircraft.ADSBCallsign
								arof.LastLaunchTime = lc.client.CurrentTime()
								arof.TotalLaunches++

								arof.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arof)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								arof.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arof)
							}
						} else {
							for range 5 {
								imgui.TableNextColumn()
							}
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}
		} else {
			changed := false
			if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
				changed = drawDepartureUI(&lc.client.State.LaunchConfig, p)
			}
			if len(lc.client.State.LaunchConfig.VFRAirports) > 0 &&
				imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
				changed = drawVFRDepartureUI(&lc.client.State.LaunchConfig, p) || changed
			}
			if imgui.CollapsingHeaderBoolPtr("Arrivals / Overflights", nil) {
				changed = drawArrivalUI(&lc.client.State.LaunchConfig, p) || changed
				changed = drawOverflightUI(&lc.client.State.LaunchConfig, p) || changed
			}

			if changed {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
			}
		}
	}

	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
		imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

	releaseAircraft := lc.client.State.GetRegularReleaseDepartures()
	if len(releaseAircraft) > 0 && imgui.CollapsingHeaderBoolPtr("Hold For Release", nil) {
		slices.SortFunc(releaseAircraft, func(a, b sim.ReleaseDeparture) int {
			// Just by airport, otherwise leave in FIFO order
			return strings.Compare(a.DepartureAirport, b.DepartureAirport)
		})

		if imgui.BeginTableV("Releases", 5, flags, imgui.Vec2{tableScale * 600, 0}, 0) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("A/C Type")
			imgui.TableSetupColumn("Exit")
			// imgui.TableSetupColumn("#Release")
			imgui.TableHeadersRow()

			lastAp := ""
			for _, ac := range releaseAircraft {
				imgui.PushIDStr(string(ac.ADSBCallsign))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(ac.DepartureAirport)
				imgui.TableNextColumn()
				imgui.Text(string(ac.ADSBCallsign))
				imgui.TableNextColumn()
				imgui.Text(ac.AircraftType)
				imgui.TableNextColumn()
				imgui.Text(ac.Exit)
				if ac.DepartureAirport != lastAp && !ac.Released {
					// Only allow releasing the first-up unreleased one.
					lastAp = ac.DepartureAirport
					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
						lc.client.ReleaseDeparture(ac.ADSBCallsign,
							func(err error) { lc.lg.Errorf("%s: %v", ac.ADSBCallsign, err) })
					}
				}
				imgui.PopID()
			}

			imgui.EndTable()
		}
	}

	imgui.End()

	if !showLaunchControls {
		lc.client.TakeOrReturnLaunchControl(eventStream)
		ui.showLaunchControl = false
	}
}

func drawScenarioInfoWindow(config *Config, c *server.ControlClient, p platform.Platform, lg *log.Logger) bool {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show := true
	imgui.BeginV(c.State.SimDescription, &show, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		// Make big(ish) tables somewhat more legible
		tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
			imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("TCP")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Frequency")
			imgui.TableSetupColumn("Name")
			imgui.TableHeadersRow()

			// Sort 2-char before 3-char and then alphabetically
			sorted := slices.Collect(maps.Keys(c.State.Controllers))
			slices.SortFunc(sorted, func(a, b string) int {
				if len(a) < len(b) {
					return -1
				} else if len(a) > len(b) {
					return 1
				} else {
					return strings.Compare(a, b)
				}
			})

			for _, id := range sorted {
				ctrl := c.State.Controllers[id]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(ctrl.Id())
				imgui.TableNextColumn()
				if slices.Contains(c.State.HumanControllers, ctrl.Id()) {
					sq := renderer.FontAwesomeIconCheckSquare
					// Center the square in the column
					// https://stackoverflow.com/a/66109051
					pos := imgui.CursorPosX() + float32(imgui.ColumnWidth()) - imgui.CalcTextSize(sq).X - imgui.ScrollX() -
						2*imgui.CurrentStyle().ItemSpacing().X
					if pos > imgui.CursorPosX() {
						imgui.SetCursorPos(imgui.Vec2{X: pos, Y: imgui.CursorPos().Y})
					}
					imgui.Text(sq)
				}
				imgui.TableNextColumn()
				imgui.Text(ctrl.Frequency.String())
				imgui.TableNextColumn()
				imgui.Text(ctrl.Position)
			}

			imgui.EndTable()
		}
	}

	config.DisplayRoot.VisitPanes(func(pane panes.Pane) {
		if draw, ok := pane.(panes.InfoWindowDrawer); ok {
			draw.DrawInfo(c, p, lg)
		}
	})
	imgui.End()

	return show
}

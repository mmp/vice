// sim.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/rpc"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/checkandmate1/AirportWeatherData"
	"github.com/mmp/imgui-go/v4"
)

type SimConfiguration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*Controller
	DefaultScenario  string
}

type SimScenarioConfiguration struct {
	SelectedController  string
	SelectedSplit       string
	SplitConfigurations SplitConfigurationSet
	PrimaryAirport      string

	Wind         Wind
	LaunchConfig LaunchConfig

	DepartureRunways []ScenarioGroupDepartureRunway
	ArrivalRunways   []ScenarioGroupArrivalRunway
}

const ServerSimCallsign = "__SERVER__"

const (
	LaunchAutomatic = iota
	LaunchManual
)

// LaunchConfig collects settings related to launching aircraft in the sim.
type LaunchConfig struct {
	// Controller is the controller in charge of the launch settings; if empty then
	// launch control may be taken by any signed in controller.
	Controller string
	// LaunchManual or LaunchAutomatic
	Mode int

	DepartureChallenge float32
	GoAroundRate       float32
	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates           map[string]map[string]int
	ArrivalPushes               bool
	ArrivalPushFrequencyMinutes int
	ArrivalPushLengthMinutes    int
}

func MakeLaunchConfig(dep []ScenarioGroupDepartureRunway, arr map[string]map[string]int) LaunchConfig {
	lc := LaunchConfig{
		DepartureChallenge:          0.25,
		GoAroundRate:                0.05,
		ArrivalGroupRates:           arr,
		ArrivalPushFrequencyMinutes: 20,
		ArrivalPushLengthMinutes:    10,
	}

	// Walk the departure runways to create the map for departures.
	lc.DepartureRates = make(map[string]map[string]map[string]int)
	for _, rwy := range dep {
		if _, ok := lc.DepartureRates[rwy.Airport]; !ok {
			lc.DepartureRates[rwy.Airport] = make(map[string]map[string]int)
		}
		if _, ok := lc.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
			lc.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]int)
		}
		lc.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate
	}

	return lc
}

func (lc *LaunchConfig) DrawActiveDepartureRunways() {
	var runways []string
	for airport, runwayRates := range lc.DepartureRates {
		for runway, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				if rate > 0 {
					runways = append(runways, airport+"/"+runway)
					break
				}
			}
		}
	}

	if len(runways) > 0 {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Departing:")
		imgui.TableNextColumn()

		sort.Strings(runways)
		imgui.Text(strings.Join(runways, ", "))
	}
}

func (lc *LaunchConfig) DrawDepartureUI() (changed bool) {
	if len(lc.DepartureRates) == 0 {
		return
	}

	sumRates := 0
	for _, runwayRates := range lc.DepartureRates {
		for _, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				sumRates += rate
			}
		}
	}

	imgui.Text("Departures")

	imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", sumRates))

	changed = imgui.SliderFloatV("Sequencing challenge", &lc.DepartureChallenge, 0, 1, "%.02f", 0) || changed
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{tableScale * 500, 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		imgui.TableSetupColumn("Runway")
		imgui.TableSetupColumn("Category")
		imgui.TableSetupColumn("ADR")
		imgui.TableHeadersRow()

		for _, airport := range SortedMapKeys(lc.DepartureRates) {
			imgui.PushID(airport)
			for _, runway := range SortedMapKeys(lc.DepartureRates[airport]) {
				imgui.PushID(runway)
				for _, category := range SortedMapKeys(lc.DepartureRates[airport][runway]) {
					imgui.PushID(category)

					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(airport)
					imgui.TableNextColumn()
					rshort, _, _ := strings.Cut(runway, ".") // don't include extras in the UI
					imgui.Text(rshort)
					imgui.TableNextColumn()
					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					r := int32(lc.DepartureRates[airport][runway][category])
					changed = imgui.InputIntV("##adr", &r, 0, 120, 0) || changed
					lc.DepartureRates[airport][runway][category] = int(r)

					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	imgui.Separator()

	return
}

func (lc *LaunchConfig) DrawArrivalUI() (changed bool) {
	if len(lc.ArrivalGroupRates) == 0 {
		return
	}

	// Figure out how many unique airports we've got for AAR columns in the table
	// and also sum up the overall arrival rate
	allAirports := make(map[string]interface{})
	sumRates := 0
	for _, agr := range lc.ArrivalGroupRates {
		for ap, rate := range agr {
			allAirports[ap] = nil
			sumRates += rate
		}
	}

	imgui.Text("Arrivals")
	imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", sumRates))
	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	changed = imgui.Checkbox("Include random arrival pushes", &lc.ArrivalPushes) || changed
	uiStartDisable(!lc.ArrivalPushes)
	freq := int32(lc.ArrivalPushFrequencyMinutes)
	changed = imgui.SliderInt("Push frequency (minutes)", &freq, 3, 60) || changed
	lc.ArrivalPushFrequencyMinutes = int(freq)
	min := int32(lc.ArrivalPushLengthMinutes)
	changed = imgui.SliderInt("Length of push (minutes)", &min, 5, 30) || changed
	lc.ArrivalPushLengthMinutes = int(min)
	uiEndDisable(!lc.ArrivalPushes)

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
	if imgui.BeginTableV("arrivalgroups", 3, flags, imgui.Vec2{tableScale * 500, 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		imgui.TableSetupColumn("Arrival")
		imgui.TableSetupColumn("AAR")
		imgui.TableHeadersRow()

		for _, group := range SortedMapKeys(lc.ArrivalGroupRates) {
			imgui.PushID(group)
			for _, ap := range SortedMapKeys(allAirports) {
				imgui.PushID(ap)
				if rate, ok := lc.ArrivalGroupRates[group][ap]; ok {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(ap)
					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					r := int32(rate)
					changed = imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0) || changed
					lc.ArrivalGroupRates[group][ap] = int(r)
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	return
}

type NewSimConfiguration struct {
	TRACONName      string
	TRACON          map[string]*SimConfiguration
	GroupName       string
	Scenario        *SimScenarioConfiguration
	ScenarioName    string
	selectedServer  *SimServer
	NewSimName      string // for create remote only
	RequirePassword bool   // for create remote only
	Password        string // for create remote only
	NewSimType      int

	LiveWeather               bool
	SelectedRemoteSim         string
	SelectedRemoteSimPosition string
	RemoteSimPassword         string // for join remote only

	lastRemoteSimsUpdate time.Time
	updateRemoteSimsCall *PendingCall

	displayError error
}

type RemoteSim struct {
	GroupName          string
	ScenarioName       string
	PrimaryController  string
	RequirePassword    bool
	AvailablePositions map[string]struct{}
	CoveredPositions   map[string]struct{}
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

func MakeNewSimConfiguration() NewSimConfiguration {
	c := NewSimConfiguration{
		selectedServer: localServer,
		NewSimName:     getRandomAdjectiveNoun(),
	}

	c.SetTRACON(globalConfig.LastTRACON)

	return c
}

func (c *NewSimConfiguration) updateRemoteSims() {
	if time.Since(c.lastRemoteSimsUpdate) > 2*time.Second && remoteServer != nil {
		c.lastRemoteSimsUpdate = time.Now()
		var rs map[string]*RemoteSim
		c.updateRemoteSimsCall = &PendingCall{
			Call:      remoteServer.Go("SimManager.GetRunningSims", 0, &rs, nil),
			IssueTime: time.Now(),
			OnSuccess: func(result any) {
				remoteServer.runningSims = rs
			},
			OnErr: func(e error) {
				lg.Errorf("GetRunningSims error: %v", e)

				// nil out the server if we've lost the connection; the
				// main loop will attempt to reconnect.
				if isRPCServerError(e) {
					remoteServer = nil
				}
			},
		}
	}
}

func (c *NewSimConfiguration) SetTRACON(name string) {
	var ok bool
	if c.TRACON, ok = c.selectedServer.configs[name]; !ok {
		if name != "" {
			lg.Errorf("%s: TRACON not found!", name)
		}
		configs := c.selectedServer.configs
		// Pick one at random
		name = SortedMapKeys(configs)[rand.Intn(len(configs))]
		c.TRACON = configs[name]
	}
	c.TRACONName = name
	c.GroupName = SortedMapKeys(c.TRACON)[0]

	c.SetScenario(c.GroupName, c.TRACON[c.GroupName].DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var groupConfig *SimConfiguration
	if groupConfig, ok = c.TRACON[groupName]; !ok {
		lg.Errorf("%s: group not found in TRACON %s", groupName, c.TRACONName)
		groupName = SortedMapKeys(c.TRACON)[0]
		groupConfig = c.TRACON[c.GroupName]
	}
	c.GroupName = groupName

	if c.Scenario, ok = groupConfig.ScenarioConfigs[scenarioName]; !ok {
		if scenarioName != "" {
			lg.Errorf("%s: scenario not found in group %s", scenarioName, c.GroupName)
		}
		scenarioName = groupConfig.DefaultScenario
		c.Scenario = groupConfig.ScenarioConfigs[scenarioName]
	}
	c.ScenarioName = scenarioName
}

func (c *NewSimConfiguration) UIButtonText() string {
	return Select(c.NewSimType == NewSimJoinRemote, "Join", "Next")
}

func (c *NewSimConfiguration) ShowRatesWindow() bool {
	return c.NewSimType == NewSimCreateLocal || c.NewSimType == NewSimCreateRemote
}

func (c *NewSimConfiguration) DrawUI() bool {
	if c.updateRemoteSimsCall != nil && c.updateRemoteSimsCall.CheckFinished(nil) {
		c.updateRemoteSimsCall = nil
	} else {
		c.updateRemoteSims()
	}

	if c.displayError != nil {
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{1, .5, .5, 1})
		if errors.Is(c.displayError, ErrRPCTimeout) || isRPCServerError(c.displayError) {
			imgui.Text("Unable to reach vice server")
		} else {
			imgui.Text(c.displayError.Error())
		}
		imgui.PopStyleColor()
		imgui.Separator()
	}

	tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
	if remoteServer != nil {
		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Server type:")

			origType := c.NewSimType

			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create single-controller", &c.NewSimType, NewSimCreateLocal) &&
				origType != NewSimCreateLocal {
				c.selectedServer = localServer
				c.SetTRACON(globalConfig.LastTRACON)
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create multi-controller", &c.NewSimType, NewSimCreateRemote) &&
				origType != NewSimCreateRemote {
				c.selectedServer = remoteServer
				c.SetTRACON(globalConfig.LastTRACON)
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			uiStartDisable(len(remoteServer.runningSims) == 0)
			if imgui.RadioButtonInt("Join multi-controller", &c.NewSimType, NewSimJoinRemote) &&
				origType != NewSimJoinRemote {
				c.selectedServer = remoteServer
				c.displayError = nil
			}
			uiEndDisable(len(remoteServer.runningSims) == 0)

			imgui.EndTable()
		}
	} else {
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Unable to connect to the multi-controller vice server; " +
			"only single-player scenarios are available.")
		imgui.PopStyleColor()
		c.NewSimType = NewSimCreateLocal
	}
	imgui.Separator()

	if c.NewSimType == NewSimCreateLocal || c.NewSimType == NewSimCreateRemote {
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 600, tableScale * 300}, 0.) {
			imgui.TableSetupColumn("ARTCC")
			imgui.TableSetupColumn("ATCT/TRACON")
			imgui.TableSetupColumn("Scenario")
			imgui.TableHeadersRow()
			imgui.TableNextRow()

			// ARTCCs
			artccs := make(map[string]interface{})
			allTRACONs := SortedMapKeys(c.selectedServer.configs)
			for _, tracon := range allTRACONs {
				artccs[database.TRACONs[tracon].ARTCC] = nil
			}
			imgui.TableNextColumn()
			if imgui.BeginChildV("artccs", imgui.Vec2{tableScale * 150, tableScale * 350}, false, /* border */
				imgui.WindowFlagsNoResize) {
				for _, artcc := range SortedMapKeys(artccs) {
					label := fmt.Sprintf("%s (%s)", artcc, strings.ReplaceAll(database.ARTCCs[artcc].Name, " Center", ""))
					if imgui.SelectableV(label, artcc == database.TRACONs[c.TRACONName].ARTCC, 0, imgui.Vec2{}) &&
						artcc != database.TRACONs[c.TRACONName].ARTCC {
						// a new ARTCC was chosen; reset the TRACON to the first one with that ARTCC
						idx := slices.IndexFunc(allTRACONs, func(tracon string) bool { return artcc == database.TRACONs[tracon].ARTCC })
						c.SetTRACON(allTRACONs[idx])
					}
				}
			}
			imgui.EndChild()

			// TRACONs for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildV("tracons", imgui.Vec2{tableScale * 150, tableScale * 350}, false, /* border */
				imgui.WindowFlagsNoResize) {
				for _, tracon := range allTRACONs {
					if database.TRACONs[tracon].ARTCC != database.TRACONs[c.TRACONName].ARTCC {
						continue
					}
					name := strings.TrimSuffix(database.TRACONs[tracon].Name, " TRACON")
					name = strings.TrimSuffix(name, " ATCT/TRACON")
					name = strings.TrimSuffix(name, " Tower")
					label := fmt.Sprintf("%s (%s)", tracon, name)
					if imgui.SelectableV(label, tracon == c.TRACONName, 0, imgui.Vec2{}) && tracon != c.TRACONName {
						// TRACON selected
						c.SetTRACON(tracon)
					}
				}
			}
			imgui.EndChild()

			// Scenarios for the tracon
			imgui.TableNextColumn()
			if imgui.BeginChildV("scenarios", imgui.Vec2{tableScale * 300, tableScale * 350}, false, /* border */
				imgui.WindowFlagsNoResize) {
				for _, groupName := range SortedMapKeys(c.TRACON) {
					group := c.TRACON[groupName]
					for _, name := range SortedMapKeys(group.ScenarioConfigs) {
						if imgui.SelectableV(name, name == c.ScenarioName, 0, imgui.Vec2{}) {
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
					if imgui.SelectableV(split, split == c.Scenario.SelectedSplit, 0, imgui.Vec2{}) {
						c.Scenario.SelectedSplit = split
						c.Scenario.SelectedController = sc.GetPrimaryController(split)
					}
				}
				imgui.EndCombo()
			}
		}

		if c.NewSimType == NewSimCreateRemote {
			if imgui.InputTextV("Name", &c.NewSimName, imgui.InputTextFlagsCallbackAlways,
				func(cb imgui.InputTextCallbackData) int32 {
					// Prevent excessively-long names...
					const MaxLength = 32
					if l := len(cb.Buffer()); l > MaxLength {
						cb.DeleteBytes(MaxLength-1, l-MaxLength)
					}
					return 0
				}) {
				c.displayError = nil
			}
			if c.NewSimName == "" {
				imgui.SameLine()
				imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(FontAwesomeIconExclamationTriangle)
				imgui.PopStyleColor()
			}

			imgui.Checkbox("Require Password", &c.RequirePassword)
			if c.RequirePassword {
				imgui.InputTextV("Password", &c.Password, 0, nil)
				if c.Password == "" {
					imgui.SameLine()
					imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .1, .1, 1})
					imgui.Text(FontAwesomeIconExclamationTriangle)
					imgui.PopStyleColor()
				}
			}
		}

		if imgui.BeginTableV("scenario", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Control Position:")
			imgui.TableNextColumn()
			imgui.Text(c.Scenario.SelectedController)

			c.Scenario.LaunchConfig.DrawActiveDepartureRunways()

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
			validAirport := c.Scenario.PrimaryAirport != "KAAC" && remoteServer != nil

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Wind:")
			uiStartDisable(!validAirport)
			imgui.Checkbox("Live Weather", &c.LiveWeather)
			if !validAirport {
				c.LiveWeather = false
			}
			uiEndDisable(!validAirport)
			imgui.TableNextColumn()
			wind := c.Scenario.Wind
			if c.LiveWeather {
				var ok bool
				if wind, ok = airportWind[c.Scenario.PrimaryAirport]; !ok {
					primary := c.Scenario.PrimaryAirport
					wind, ok = getWind(primary)
					if !ok {
						wind = c.Scenario.Wind
					}
				}
			}
			var dir string
			if wind.Direction == -1 {
				dir = "Variable"
			} else {
				dir = fmt.Sprintf("%v", wind.Direction)
			}
			if wind.Gust > wind.Speed {
				imgui.Text(fmt.Sprintf("%v at %d gust %d", dir, wind.Speed, wind.Gust))
			} else {
				imgui.Text(fmt.Sprintf("%v at %d", dir, wind.Speed))
			}
			uiStartDisable(!c.LiveWeather)
			refresh := imgui.Button("Refresh Weather")
			if refresh {
				clear(airportWind)
				clear(windRequest)
			}
			uiEndDisable(!c.LiveWeather)
			imgui.EndTable()

		}
	} else {
		// Join remote
		runningSims := remoteServer.runningSims

		rs, ok := runningSims[c.SelectedRemoteSim]
		if !ok || c.SelectedRemoteSim == "" {
			c.SelectedRemoteSim = SortedMapKeys(runningSims)[0]

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

			for _, simName := range SortedMapKeys(runningSims) {
				rs := runningSims[simName]
				if len(rs.AvailablePositions) == 0 {
					// No open positions left; don't even offer it.
					continue
				}

				imgui.PushID(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(FontAwesomeIconLock)
				}
				imgui.TableNextColumn()

				selected := simName == c.SelectedRemoteSim
				selFlags := imgui.SelectableFlagsSpanAllColumns | imgui.SelectableFlagsDontClosePopups
				if imgui.SelectableV(simName, selected, selFlags, imgui.Vec2{}) {
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
					imgui.SetTooltip(strings.Join(SortedMapKeys(rs.CoveredPositions), ", "))
				}

				imgui.PopID()
			}
			imgui.EndTable()
		}

		// Handle the case of someone else signing in to the position
		if _, ok := rs.AvailablePositions[c.SelectedRemoteSimPosition]; c.SelectedRemoteSimPosition != "Observer" && !ok {
			c.SelectedRemoteSimPosition = SortedMapKeys(rs.AvailablePositions)[0]
		}

		if imgui.BeginComboV("Position", c.SelectedRemoteSimPosition, 0) {
			for _, pos := range SortedMapKeys(rs.AvailablePositions) {
				if pos[0] == '_' {
					continue
				}
				if imgui.SelectableV(pos, pos == c.SelectedRemoteSimPosition, 0, imgui.Vec2{}) {
					c.SelectedRemoteSimPosition = pos
				}
			}

			if imgui.SelectableV("Observer", "Observer" == c.SelectedRemoteSimPosition, 0, imgui.Vec2{}) {
				c.SelectedRemoteSimPosition = "Observer"
			}

			imgui.EndCombo()
		}
		if rs.RequirePassword {
			imgui.InputTextV("Password", &c.RemoteSimPassword, 0, nil)
		}
	}

	return false
}

func (c *NewSimConfiguration) DrawRatesUI() bool {
	c.Scenario.LaunchConfig.DrawDepartureUI()
	c.Scenario.LaunchConfig.DrawArrivalUI()
	return false
}

func getWind(airport string) (Wind, bool) {
	for airport, ch := range windRequest {
		select {
		case w := <-ch:
			dirStr := fmt.Sprintf("%v", w.Wdir)
			dir, err := strconv.Atoi(dirStr)
			if err != nil {
				lg.Errorf("Error converting %v to an int: %v", dirStr, err)
			}
			airportWind[airport] = Wind{
				Direction: int32(dir),
				Speed:     int32(w.Wspd),
				Gust:      int32(w.Wgst),
			}
			delete(windRequest, airport)
		default:
			// no wind yet
		}
	}

	if _, ok := airportWind[airport]; ok {
		// The wind is in the map
		return airportWind[airport], true
	} else if _, ok := windRequest[airport]; ok {
		// it's been requested but we don't have it yet
		return Wind{}, false
	} else {
		// It hasn't been requested nor is in airportWind
		c := make(chan getweather.MetarData)
		windRequest[airport] = c
		go func() {
			weather, err := getweather.GetWeather(airport)
			if len(err) != 0 {
				lg.Errorf("%v", err)
			}
			c <- weather
		}()
		return Wind{}, false
	}

}

func (c *NewSimConfiguration) OkDisabled() bool {
	return c.NewSimType == NewSimCreateRemote && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
}

func (c *NewSimConfiguration) Start() error {
	var result NewSimResult
	if err := c.selectedServer.CallWithTimeout("SimManager.New", c, &result); err != nil {
		err = TryDecodeError(err)

		if err == ErrRPCTimeout || err == ErrRPCVersionMismatch || errors.Is(err, rpc.ErrShutdown) {
			// Problem with the connection to the remote server? Let the main
			// loop try to reconnect.
			remoteServer = nil
		}

		return err
	}

	result.World.simProxy = &SimProxy{
		ControllerToken: result.ControllerToken,
		Client:          c.selectedServer.RPCClient,
	}

	globalConfig.LastTRACON = c.TRACONName

	newWorldChan <- result.World

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	Name string

	mu LoggingMutex

	ScenarioGroup string
	Scenario      string

	World           *World
	controllers     map[string]*ServerController // from token
	SignOnPositions map[string]*Controller

	eventStream *EventStream
	lg          *Logger

	LaunchConfig LaunchConfig

	// airport -> runway -> category
	lastDeparture map[string]map[string]map[string]*Departure

	// We track an overall "at what time do we launch the next departure"
	// time for each airport. When that time is reached, we'll pick a
	// runway, category, etc., based on the respective rates.
	NextDepartureSpawn map[string]time.Time `json:"NextDepartureSpawn2"` // avoid parse errors on old configs

	// Key is arrival group name
	NextArrivalSpawn map[string]time.Time

	// callsign -> auto accept time
	Handoffs map[string]time.Time
	// callsign -> "to" controller
	PointOuts map[string]map[string]PointOut

	TotalDepartures int
	TotalArrivals   int

	ReportingPoints []ReportingPoint

	RequirePassword bool
	Password        string

	lastSimUpdate time.Time

	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..
	updateTimeSlop time.Duration

	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	lastLogTime    time.Time
	SimRate        float32
	Paused         bool

	NextPushStart time.Time // both w.r.t. sim time
	PushEnd       time.Time

	STARSInputOverride string
}

type PointOut struct {
	FromController string
	AcceptTime     time.Time
}

type ServerController struct {
	Callsign            string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
	events              *EventsSubscription
}

func (sc *ServerController) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("callsign", sc.Callsign),
		slog.Time("last_update", sc.lastUpdateCall),
		slog.Bool("warned_no_update", sc.warnedNoUpdateCalls))
}

func NewSim(ssc NewSimConfiguration, scenarioGroups map[string]map[string]*ScenarioGroup, isLocal bool, lg *Logger) *Sim {
	lg = lg.With(slog.String("sim_name", ssc.NewSimName))

	tracon, ok := scenarioGroups[ssc.TRACONName]
	if !ok {
		lg.Errorf("%s: unknown TRACON", ssc.TRACONName)
		return nil
	}
	sg, ok := tracon[ssc.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", ssc.GroupName)
		return nil
	}
	sc, ok := sg.Scenarios[ssc.ScenarioName]
	if !ok {
		lg.Errorf("%s: unknown scenario", ssc.ScenarioName)
		return nil
	}

	s := &Sim{
		ScenarioGroup: ssc.GroupName,
		Scenario:      ssc.ScenarioName,
		LaunchConfig:  ssc.Scenario.LaunchConfig,

		controllers: make(map[string]*ServerController),

		eventStream: NewEventStream(),
		lg:          lg,

		lastDeparture: make(map[string]map[string]map[string]*Departure),

		ReportingPoints: sg.ReportingPoints,

		Password:        ssc.Password,
		RequirePassword: ssc.RequirePassword,

		SimTime:        time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:   1,
		Handoffs:  make(map[string]time.Time),
		PointOuts: make(map[string]map[string]PointOut),
	}

	if !isLocal {
		s.Name = ssc.NewSimName
	}

	if s.LaunchConfig.ArrivalPushes {
		// Figure out when the next arrival push will start
		m := 1 + rand.Intn(s.LaunchConfig.ArrivalPushFrequencyMinutes)
		s.NextPushStart = time.Now().Add(time.Duration(m) * time.Minute)
	}

	for ap := range s.LaunchConfig.DepartureRates {
		s.lastDeparture[ap] = make(map[string]map[string]*Departure)
		for rwy := range s.LaunchConfig.DepartureRates[ap] {
			s.lastDeparture[ap][rwy] = make(map[string]*Departure)
		}
	}

	s.SignOnPositions = make(map[string]*Controller)
	add := func(callsign string) {
		if ctrl, ok := sg.ControlPositions[callsign]; !ok {
			lg.Errorf("%s: control position unknown??!", callsign)
		} else {
			ctrlCopy := *ctrl
			ctrlCopy.IsHuman = true
			s.SignOnPositions[callsign] = &ctrlCopy
		}
	}
	if *server {
		for callsign := range sc.SplitConfigurations.GetConfiguration(ssc.Scenario.SelectedSplit) {
			add(callsign)
		}
	} else {
		add(sc.SoloController)
	}

	s.World = newWorld(ssc, s, sg, sc)

	s.setInitialSpawnTimes()

	return s
}

func newWorld(ssc NewSimConfiguration, s *Sim, sg *ScenarioGroup, sc *Scenario) *World {
	w := NewWorld()
	w.Callsign = "__SERVER__"
	if *server {
		w.PrimaryController = sc.SplitConfigurations.GetPrimaryController(ssc.Scenario.SelectedSplit)
		w.MultiControllers = sc.SplitConfigurations.GetConfiguration(ssc.Scenario.SelectedSplit)
	} else {
		w.PrimaryController = sc.SoloController
	}
	w.TRACON = sg.TRACON
	w.MagneticVariation = sg.MagneticVariation
	w.NmPerLongitude = sg.NmPerLongitude
	w.Wind = sc.Wind
	w.Airports = sg.Airports
	w.Fixes = sg.Fixes
	w.PrimaryAirport = sg.PrimaryAirport
	stars := sg.STARSFacilityAdaptation
	w.RadarSites = stars.RadarSites
	w.Center = Select(stars.Center.IsZero(), stars.Center, stars.Center)
	w.Range = Select(sc.Range == 0, stars.Range, sc.Range)
	w.DefaultMaps = sc.DefaultMaps
	w.STARSMaps = stars.Maps
	w.InhibitCAVolumes = stars.InhibitCAVolumes
	w.Scratchpads = stars.Scratchpads
	w.ArrivalGroups = sg.ArrivalGroups
	w.ApproachAirspace = sc.ApproachAirspace
	w.DepartureAirspace = sc.DepartureAirspace
	w.DepartureRunways = sc.DepartureRunways
	w.ArrivalRunways = sc.ArrivalRunways
	w.LaunchConfig = s.LaunchConfig
	w.SimIsPaused = s.Paused
	w.SimRate = s.SimRate
	w.SimName = s.Name
	w.SimDescription = s.Scenario
	w.SimTime = s.SimTime
	w.STARSFacilityAdaptation = sg.STARSFacilityAdaptation

	for _, callsign := range sc.VirtualControllers {
		// Skip controllers that are in MultiControllers
		if w.MultiControllers != nil {
			if _, ok := w.MultiControllers[callsign]; ok {
				continue
			}
		}

		if ctrl, ok := sg.ControlPositions[callsign]; ok {
			w.Controllers[callsign] = ctrl
		} else {
			s.lg.Errorf("%s: controller not found in ControlPositions??", callsign)
		}
	}

	// Make some fake METARs; slightly different for all airports.
	var alt int

	fakeMETAR := func(icao string) {
		alt = 2980 + rand.Intn(40)
		spd := w.Wind.Speed - 3 + rand.Int31n(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((w.Wind.Direction + 5) / 10)
			dir += [3]int32{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := w.Wind.Gust - 3 + rand.Int31n(6)
			if gst-w.Wind.Speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		w.METAR[icao] = &METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

	realMETAR := func(icao string) {
		weather, errors := getweather.GetWeather(icao)
		if len(errors) != 0 {
			s.lg.Errorf("Error getting weather for %v.", icao)
		}
		fullMETAR := weather.RawMETAR
		altimiter := getAltimiter(fullMETAR)
		var err error

		if err != nil {
			s.lg.Errorf("Error converting altimiter to an intiger: %v.", altimiter)
		}
		var wind string
		spd := weather.Wspd
		var dir float64
		if weather.Wdir == -1 {
			dirInt := weather.Wdir.(int)
			dir = float64(dirInt)
		}
		var ok bool
		dir, ok = weather.Wdir.(float64)
		if !ok {
			lg.Errorf("Error converting %v into a float64: actual type %T", dir, dir)
		}
		if spd <= 0 {
			wind = "00000KT"
		} else if dir == -1 {
			wind = fmt.Sprintf("VRB%vKT", spd)
		} else {
			wind = fmt.Sprintf("%03d%02d", int(dir), spd)
			gst := weather.Wgst
			if gst > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		w.METAR[icao] = &METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   "A" + altimiter,
		}
	}

	w.DepartureAirports = make(map[string]*Airport)
	for name := range s.LaunchConfig.DepartureRates {
		w.DepartureAirports[name] = w.GetAirport(name)
	}
	w.ArrivalAirports = make(map[string]*Airport)
	for _, airportRates := range s.LaunchConfig.ArrivalGroupRates {
		for name := range airportRates {
			w.ArrivalAirports[name] = w.GetAirport(name)
		}
	}
	if ssc.LiveWeather {
		for ap := range w.DepartureAirports {
			realMETAR(ap)
		}
		for ap := range w.ArrivalAirports {
			realMETAR(ap)
		}
	} else {
		for ap := range w.DepartureAirports {
			fakeMETAR(ap)
		}
		for ap := range w.ArrivalAirports {
			fakeMETAR(ap)
		}
	}

	return w
}

func getAltimiter(metar string) string {
	for _, indexString := range []string{" A3", " A2"} {
		index := strings.Index(metar, indexString)
		if index != -1 && index+6 < len(metar) {
			return metar[index+2 : index+6]
		}
	}
	return ""
}

func (s *Sim) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", s.Name),
		slog.String("scenario_group", s.ScenarioGroup),
		slog.String("scenario", s.Scenario),
		slog.Any("controllers", s.World.Controllers),
		slog.Any("launch_config", s.LaunchConfig),
		slog.Any("next_departure_spawn", s.NextDepartureSpawn),
		slog.Any("next_arrival_spawn", s.NextArrivalSpawn),
		slog.Any("automatic_handoffs", s.Handoffs),
		slog.Any("automatic_pointouts", s.PointOuts),
		slog.Int("departures", s.TotalDepartures),
		slog.Int("arrivals", s.TotalArrivals),
		slog.Time("sim_time", s.SimTime),
		slog.Float64("sim_rate", float64(s.SimRate)),
		slog.Bool("paused", s.Paused),
		slog.Time("next_push_start", s.NextPushStart),
		slog.Time("push_end", s.PushEnd),
		slog.Any("aircraft", s.World.Aircraft))
}

func (s *Sim) SignOn(callsign string) (*World, string, error) {
	if err := s.signOn(callsign); err != nil {
		return nil, "", err
	}

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}
	token := base64.StdEncoding.EncodeToString(buf[:])

	s.controllers[token] = &ServerController{
		Callsign:       callsign,
		lastUpdateCall: time.Now(),
		events:         s.eventStream.Subscribe(),
	}

	w := NewWorld()
	w.Assign(s.World)
	w.Callsign = callsign

	return w, token, nil
}

func (s *Sim) signOn(callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if callsign != "Observer" {
		if s.controllerIsSignedIn(callsign) {
			return ErrControllerAlreadySignedIn
		}

		ctrl, ok := s.SignOnPositions[callsign]
		if !ok {
			return ErrNoController
		}
		s.World.Controllers[callsign] = ctrl

		if callsign == s.World.PrimaryController {
			// The primary controller signed in so the sim will resume.
			// Reset lastUpdateTime so that the next time Update() is
			// called for the sim, we don't try to run a ton of steps.
			s.lastUpdateTime = time.Now()
		}
	}

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: callsign + " has signed on.",
	})
	s.lg.Infof("%s: controller signed on", callsign)

	return nil
}

func (s *Sim) SignOff(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		// Drop track on controlled aircraft
		for _, ac := range s.World.Aircraft {
			ac.HandleControllerDisconnect(ctrl.Callsign, s.World)
		}

		if ctrl.Callsign == s.LaunchConfig.Controller {
			// give up control of launches so someone else can take it.
			s.LaunchConfig.Controller = ""
		}

		ctrl.events.Unsubscribe()
		delete(s.controllers, token)
		delete(s.World.Controllers, ctrl.Callsign)

		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: ctrl.Callsign + " has signed off.",
		})
		s.lg.Infof("%s: controller signing off", ctrl.Callsign)
	}
	return nil
}

func (s *Sim) ChangeControlPosition(token string, callsign string, keepTracks bool) error {
	ctrl, ok := s.controllers[token]
	if !ok {
		return ErrInvalidControllerToken
	}
	oldCallsign := ctrl.Callsign

	s.lg.Infof("%s: switching to %s", oldCallsign, callsign)

	// Make sure we can successfully sign on before signing off from the
	// current position.
	if err := s.signOn(callsign); err != nil {
		return err
	}
	ctrl.Callsign = callsign

	delete(s.World.Controllers, oldCallsign)

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: oldCallsign + " has signed off.",
	})

	for _, ac := range s.World.Aircraft {
		if keepTracks {
			ac.TransferTracks(oldCallsign, ctrl.Callsign)
		} else {
			ac.HandleControllerDisconnect(ctrl.Callsign, s.World)
		}
	}

	return nil
}

func (s *Sim) TogglePause(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.Paused = !s.Paused
		s.lg.Infof("paused: %v", s.Paused)
		s.lastUpdateTime = time.Now() // ignore time passage...
		return nil
	}
}

func (s *Sim) PostEvent(e Event) {
	s.eventStream.Post(e)
}

type GlobalMessage struct {
	Message        string
	FromController string
}

type SimWorldUpdate struct {
	Aircraft    map[string]*Aircraft
	Controllers map[string]*Controller
	Time        time.Time

	LaunchConfig LaunchConfig

	SimIsPaused     bool
	SimRate         float32
	STARSInput      string
	Events          []Event
	TotalDepartures int
	TotalArrivals   int
}

func (wu *SimWorldUpdate) UpdateWorld(w *World, eventStream *EventStream) {
	w.Aircraft = wu.Aircraft
	if wu.Controllers != nil {
		w.Controllers = wu.Controllers
	}

	w.LaunchConfig = wu.LaunchConfig

	w.SimTime = wu.Time
	w.SimIsPaused = wu.SimIsPaused
	w.SimRate = wu.SimRate
	w.STARSInputOverride = wu.STARSInput
	w.TotalDepartures = wu.TotalDepartures
	w.TotalArrivals = wu.TotalArrivals

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range wu.Events {
		eventStream.Post(e)
	}
}

func (s *Sim) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		ctrl.lastUpdateCall = time.Now()
		if ctrl.warnedNoUpdateCalls {
			ctrl.warnedNoUpdateCalls = false
			s.lg.Warnf("%s: connection re-established", ctrl.Callsign)
			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: ctrl.Callsign + " is back online.",
			})
		}

		*update = SimWorldUpdate{
			Aircraft:        s.World.Aircraft,
			Controllers:     s.World.Controllers,
			Time:            s.SimTime,
			LaunchConfig:    s.LaunchConfig,
			SimIsPaused:     s.Paused,
			SimRate:         s.SimRate,
			Events:          ctrl.events.Get(),
			TotalDepartures: s.TotalDepartures,
			TotalArrivals:   s.TotalArrivals,
		}

		return nil
	}
}

func (s *Sim) Activate(lg *Logger) {
	if s.Name == "" {
		s.lg = lg
	} else {
		s.lg = lg.With(slog.String("sim_name", s.Name))
	}

	if s.controllers == nil {
		s.controllers = make(map[string]*ServerController)
	}
	if s.eventStream == nil {
		s.eventStream = NewEventStream()
	}

	now := time.Now()
	s.lastUpdateTime = now
	s.World.lastUpdateRequest = now

	s.lastDeparture = make(map[string]map[string]map[string]*Departure)
	for ap := range s.LaunchConfig.DepartureRates {
		s.lastDeparture[ap] = make(map[string]map[string]*Departure)
		for rwy := range s.LaunchConfig.DepartureRates[ap] {
			s.lastDeparture[ap][rwy] = make(map[string]*Departure)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	startUpdate := time.Now()
	defer func() {
		if d := time.Since(startUpdate); d > 200*time.Millisecond {
			lg.Warn("unexpectedly long Sim Update() call", slog.Duration("duration", d),
				slog.Any("sim", s))
		}
	}()

	for _, ac := range s.World.Aircraft {
		ac.Check(s.lg)
	}

	if s.Name != "" {
		// Sign off controllers we haven't heard from in 15 seconds so that
		// someone else can take their place. We only make this check for
		// multi-controller sims; we don't want to do this for local sims
		// so that we don't kick people off e.g. when their computer
		// sleeps.
		for token, ctrl := range s.controllers {
			if time.Since(ctrl.lastUpdateCall) > 5*time.Second {
				if !ctrl.warnedNoUpdateCalls {
					ctrl.warnedNoUpdateCalls = true
					s.lg.Warnf("%s: no messages for 5 seconds", ctrl.Callsign)
					s.eventStream.Post(Event{
						Type:    StatusMessageEvent,
						Message: ctrl.Callsign + " has not been heard from for 5 seconds. Connection lost?",
					})
				}

				if time.Since(ctrl.lastUpdateCall) > 15*time.Second {
					s.lg.Warnf("%s: signing off idle controller", ctrl.Callsign)
					s.mu.Unlock(s.lg)
					s.SignOff(token)
					s.mu.Lock(s.lg)
				}
			}
		}
	}

	if s.Paused {
		return
	}

	if !s.controllerIsSignedIn(s.World.PrimaryController) {
		// Pause the sim if the primary controller is gone
		return
	}

	// Figure out how much time has passed since the last update: wallclock
	// time is scaled by the sim rate, then we add in any time from the
	// last update that wasn't accounted for.
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.SimRate*float32(elapsed)) + s.updateTimeSlop
	// Run the sim for this many seconds
	ns := int(elapsed.Truncate(time.Second).Seconds())
	if ns > 10 {
		s.lg.Warn("unexpected hitch in update rate", slog.Duration("elapsed", elapsed),
			slog.Int("steps", ns), slog.Duration("slop", s.updateTimeSlop))
	}
	for i := 0; i < ns; i++ {
		s.SimTime = s.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = elapsed - elapsed.Truncate(time.Second)
	s.World.SimTime = s.SimTime

	s.lastUpdateTime = time.Now()

	// Log the current state of everything once a minute
	if time.Since(s.lastLogTime) > time.Minute {
		s.lastLogTime = time.Now()
		s.lg.Info("sim", slog.Any("state", s))
	}
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.SimTime

	for callsign, t := range s.Handoffs {
		if !now.After(t) {
			continue
		}

		if ac, ok := s.World.Aircraft[callsign]; ok && ac.HandoffTrackController != "" &&
			!s.controllerIsSignedIn(ac.HandoffTrackController) {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.TrackingController,
				ToController:   ac.HandoffTrackController,
				Callsign:       ac.Callsign,
			})
			s.lg.Info("automatic handoff accept", slog.String("callsign", ac.Callsign),
				slog.String("from", ac.TrackingController),
				slog.String("to", ac.HandoffTrackController))

			ac.TrackingController = ac.HandoffTrackController
			ac.HandoffTrackController = ""
		}
		delete(s.Handoffs, callsign)
	}

	for callsign, acPointOuts := range s.PointOuts {
		for toController, po := range acPointOuts {
			if !now.After(po.AcceptTime) {
				continue
			}

			if ac, ok := s.World.Aircraft[callsign]; ok && !s.controllerIsSignedIn(toController) {
				// Note that "to" and "from" are swapped in the event,
				// since the ack is coming from the "to" controller of the
				// original point out.
				s.eventStream.Post(Event{
					Type:           AcknowledgedPointOutEvent,
					FromController: toController,
					ToController:   po.FromController,
					Callsign:       ac.Callsign,
				})
				s.lg.Info("automatic pointout accept", slog.String("callsign", ac.Callsign),
					slog.String("by", toController), slog.String("to", po.FromController))

				delete(s.PointOuts[callsign], toController)
			}
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now
		for callsign, ac := range s.World.Aircraft {
			passedWaypoint := ac.Update(s.World, s, s.lg)
			if passedWaypoint != nil && passedWaypoint.Handoff {
				// Handoff from virtual controller to a human controller.
				ctrl := s.ResolveController(ac.WaypointHandoffController)

				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.TrackingController,
					ToController:   ctrl,
				})

				ac.HandoffTrackController = ctrl
			}

			// Contact the departure controller
			if ac.IsDeparture() && ac.DepartureContactAltitude != 0 &&
				ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude {
				// Time to check in
				ctrl := s.ResolveController(ac.DepartureContactController)
				lg.Info("contacting departure controller", slog.String("callsign", ctrl))

				airportName := ac.FlightPlan.DepartureAirport
				if ap, ok := s.World.Airports[airportName]; ok && ap.Name != "" {
					airportName = ap.Name
				}

				msg := "departing " + airportName + ", " + ac.Nav.DepartureMessage()
				PostRadioEvents(ac.Callsign, []RadioTransmission{RadioTransmission{
					Controller: ctrl,
					Message:    msg,
					Type:       RadioTransmissionContact,
				}}, s)

				// Clear this out so we only send one contact message
				ac.DepartureContactAltitude = 0

				// Only after we're on frequency can the controller start
				// issuing control commands.. (Note that track may have
				// already been handed off to the next controller at this
				// point.)
				ac.ControllingController = ctrl
			}

			// Cull far-away departures/arrivals
			if ac.IsDeparture() {
				if ap := s.World.GetAirport(ac.FlightPlan.DepartureAirport); ap != nil &&
					nmdistance2ll(ac.Position(), ap.Location) > 250 {
					s.lg.Info("culled far-away departure", slog.String("callsign", callsign))
					delete(s.World.Aircraft, callsign)
				}
			} else if ap := s.World.GetAirport(ac.FlightPlan.ArrivalAirport); ap != nil &&
				nmdistance2ll(ac.Position(), ap.Location) > 250 {
				// We only expect this case to hit for an unattended vice,
				// where aircraft are being spawned but are then flying
				// along on a heading without being controlled...
				s.lg.Info("culled far-away arrival", slog.String("callsign", callsign))
				delete(s.World.Aircraft, callsign)
			}
		}
	}

	// Don't spawn automatically if someone is spawning manually.
	if s.LaunchConfig.Mode == LaunchAutomatic {
		s.spawnAircraft()
	}
}

func (s *Sim) ResolveController(callsign string) string {
	if s.World.MultiControllers == nil {
		// Single controller
		return s.World.PrimaryController
	} else if len(s.controllers) == 0 {
		// This can happen during the prespawn phase right after launching but
		// before the user has been signed in.
		return s.World.PrimaryController
	} else {
		c := s.World.MultiControllers.ResolveController(callsign,
			func(callsign string) bool {
				return s.controllerIsSignedIn(callsign)
			})
		if c == "" { // This shouldn't happen...
			return s.World.PrimaryController
		}
		return c
	}
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return time.Since(s.lastUpdateTime)
}

func (s *Sim) controllerIsSignedIn(callsign string) bool {
	for _, ctrl := range s.controllers {
		if ctrl.Callsign == callsign {
			return true
		}
	}
	return false
}

func (s *Sim) prespawn() {
	s.lg.Info("starting aircraft prespawn")

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		s.SimTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.SimTime = time.Now()
	s.World.SimTime = s.SimTime
	s.lastUpdateTime = time.Now()

	s.lg.Info("finished aircraft prespawn")
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func (s *Sim) setInitialSpawnTimes() {
	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	randomSpawn := func(rate int) time.Time {
		if rate == 0 {
			return time.Now().Add(365 * 24 * time.Hour)
		}
		avgWait := 3600 / rate
		delta := rand.Intn(avgWait) - avgWait/2 - initialSimSeconds
		return time.Now().Add(time.Duration(delta) * time.Second)
	}

	s.NextArrivalSpawn = make(map[string]time.Time)
	for group, rates := range s.LaunchConfig.ArrivalGroupRates {
		rateSum := 0
		for _, rate := range rates {
			rateSum += rate
		}
		s.NextArrivalSpawn[group] = randomSpawn(rateSum)
	}

	s.NextDepartureSpawn = make(map[string]time.Time)
	for airport, runwayRates := range s.LaunchConfig.DepartureRates {
		rateSum := 0

		for _, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				rateSum += rate
			}
		}

		s.NextDepartureSpawn[airport] = randomSpawn(rateSum)
	}
}

func sampleRateMap(rates map[string]int) (string, int) {
	// Choose randomly in proportion to the rates in the map
	rateSum := 0
	var result string
	for item, rate := range rates {
		if rate == 0 {
			continue
		}
		rateSum += rate
		// Weighted reservoir sampling...
		if rand.Float32() < float32(rate)/float32(rateSum) {
			result = item
		}
	}
	return result, rateSum
}

func sampleRateMap2(rates map[string]map[string]int) (string, string, int) {
	// Choose randomly in proportion to the rates in the map
	rateSum := 0
	var result0, result1 string
	for item0, rateMap := range rates {
		for item1, rate := range rateMap {
			if rate == 0 {
				continue
			}
			rateSum += rate
			// Weighted reservoir sampling...
			if rand.Float32() < float32(rate)/float32(rateSum) {
				result0 = item0
				result1 = item1
			}
		}
	}
	return result0, result1, rateSum
}

func randomWait(rate int, pushActive bool) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}
	if pushActive {
		rate = rate * 3 / 2
	}

	avgSeconds := 3600 / float32(rate)
	seconds := lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
	return time.Duration(seconds * float32(time.Second))
}

func (s *Sim) spawnAircraft() {
	now := s.SimTime

	if !s.NextPushStart.IsZero() && now.After(s.NextPushStart) {
		// party time
		s.PushEnd = now.Add(time.Duration(s.LaunchConfig.ArrivalPushLengthMinutes) * time.Minute)
		s.lg.Info("arrival push starting", slog.Time("end_time", s.PushEnd))
		s.NextPushStart = time.Time{}
	}
	if !s.PushEnd.IsZero() && now.After(s.PushEnd) {
		// end push
		m := -2 + rand.Intn(4) + s.LaunchConfig.ArrivalPushFrequencyMinutes
		s.NextPushStart = now.Add(time.Duration(m) * time.Minute)
		s.lg.Info("arrival push ending", slog.Time("next_start", s.NextPushStart))
		s.PushEnd = time.Time{}
	}

	pushActive := now.Before(s.PushEnd)

	for group, airportRates := range s.LaunchConfig.ArrivalGroupRates {
		if now.After(s.NextArrivalSpawn[group]) {
			arrivalAirport, rateSum := sampleRateMap(airportRates)

			goAround := rand.Float32() < s.LaunchConfig.GoAroundRate
			if ac, err := s.World.CreateArrival(group, arrivalAirport, goAround); err != nil {
				s.lg.Error("CreateArrival error: %v", err)
			} else if ac != nil {
				s.launchAircraftNoLock(*ac)
				s.NextArrivalSpawn[group] = now.Add(randomWait(rateSum, pushActive))
			}
		}
	}

	for airport, spawnTime := range s.NextDepartureSpawn {
		if !now.After(spawnTime) {
			continue
		}

		// Figure out which category to launch
		runway, category, rateSum := sampleRateMap2(s.LaunchConfig.DepartureRates[airport])
		if rateSum == 0 {
			s.lg.Errorf("%s: couldn't find an active runway for spawning departure?", airport)
			continue
		}

		prevDep := s.lastDeparture[airport][runway][category]
		s.lg.Infof("%s/%s/%s: previous departure", airport, runway, category)
		ac, dep, err := s.World.CreateDeparture(airport, runway, category,
			s.LaunchConfig.DepartureChallenge, prevDep)
		if err != nil {
			s.lg.Errorf("CreateDeparture error: %v", err)
		} else {
			s.lastDeparture[airport][runway][category] = dep
			s.lg.Infof("%s/%s/%s: launch departure", airport, runway, category)
			s.launchAircraftNoLock(*ac)
			s.NextDepartureSpawn[airport] = now.Add(randomWait(rateSum, false))
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Commands from the user

func (s *Sim) SetSimRate(token string, rate float32) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.SimRate = rate
		s.lg.Infof("sim rate set to %f", s.SimRate)
		return nil
	}
}

func (s *Sim) SetLaunchConfig(token string, lc LaunchConfig) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ctrl.Callsign != s.LaunchConfig.Controller {
		return ErrNotLaunchController
	} else {
		// Update the next spawn time for any rates that changed.
		for ap, rwyRates := range lc.DepartureRates {
			newSum, oldSum := 0, 0
			for rwy, categoryRates := range rwyRates {
				for category, rate := range categoryRates {
					newSum += rate
					oldSum += s.LaunchConfig.DepartureRates[ap][rwy][category]
				}
			}
			if newSum != oldSum {
				s.lg.Infof("%s: departure rate changed %d -> %d", ap, oldSum, newSum)
				s.NextDepartureSpawn[ap] = s.SimTime.Add(randomWait(newSum, false))
			}
		}
		for group, groupRates := range lc.ArrivalGroupRates {
			newSum, oldSum := 0, 0
			for ap, rate := range groupRates {
				newSum += rate
				oldSum += s.LaunchConfig.ArrivalGroupRates[group][ap]
			}
			if newSum != oldSum {
				pushActive := s.SimTime.Before(s.PushEnd)
				s.lg.Infof("%s: arrival rate changed %d -> %d", group, oldSum, newSum)
				s.NextArrivalSpawn[group] = s.SimTime.Add(randomWait(newSum, pushActive))
			}

		}

		s.LaunchConfig = lc
		return nil
	}
}

func (s *Sim) TakeOrReturnLaunchControl(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if lctrl := s.LaunchConfig.Controller; lctrl != "" && ctrl.Callsign != lctrl {
		return ErrNotLaunchController
	} else if lctrl == "" {
		s.LaunchConfig.Controller = ctrl.Callsign
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: ctrl.Callsign + " is now controlling aircraft launches.",
		})
		s.lg.Infof("%s: now controlling launches", ctrl.Callsign)
		return nil
	} else {
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: s.LaunchConfig.Controller + " is no longer controlling aircraft launches.",
		})
		s.lg.Infof("%s: no longer controlling launches", ctrl.Callsign)
		s.LaunchConfig.Controller = ""
		return nil
	}
}

func (s *Sim) LaunchAircraft(ac Aircraft) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.launchAircraftNoLock(ac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) launchAircraftNoLock(ac Aircraft) {
	if _, ok := s.World.Aircraft[ac.Callsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!", slog.String("callsign", ac.Callsign))
		return
	}

	s.World.Aircraft[ac.Callsign] = &ac

	ac.Nav.Check(s.lg)

	if ac.IsDeparture() {
		s.TotalDepartures++
		s.lg.Info("launched departure", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else {
		s.TotalArrivals++
		s.lg.Info("launched arrival", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	}
}

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *Controller, ac *Aircraft) error,
	cmd func(*Controller, *Aircraft) []RadioTransmission) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.World.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if sc.Callsign == "Observer" {
			return ErrOtherControllerHasTrack
		}

		ctrl := s.World.GetControllerByCallsign(sc.Callsign)
		if ctrl == nil {
			s.lg.Error("controller unknown", slog.String("controller", sc.Callsign),
				slog.Any("world_controllers", s.World.Controllers))
			return ErrNoController
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			preAc := *ac
			radioTransmissions := cmd(ctrl, ac)
			s.lg.Info("dispatch_command", slog.String("callsign", ac.Callsign),
				slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
				slog.Any("radio_transmissions", radioTransmissions))
			PostRadioEvents(ac.Callsign, radioTransmissions, s)
			return nil
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) []RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) []RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

func (s *Sim) GlobalMessage(global GlobalMessageArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		Message:        global.Message,
		FromController: global.FromController,
	})

	return nil
}

func (s *Sim) SetScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.Scratchpad = scratchpad
			return nil
		})
}

func (s *Sim) SetSecondaryScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.SecondaryScratchpad = scratchpad
			return nil
		})
}

func (s *Sim) Ident(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *Controller, ac *Aircraft) error {
			// Can't ask for ident if they're on someone else's frequency.
			if ac.ControllingController != "" && ac.ControllingController != c.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			s.eventStream.Post(Event{
				Type:     IdentEvent,
				Callsign: ac.Callsign,
			})

			return []RadioTransmission{RadioTransmission{
				Controller: ctrl.Callsign,
				Message:    "ident",
				Type:       RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(token, callsign string, dir *CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != c.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.GlobalLeaderLineDirection = dir
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				Callsign:            ac.Callsign,
				FromController:      callsign,
				LeaderLineDirection: dir,
			})
			return nil
		})
}

func (s *Sim) InitiateTrack(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.TrackingController = ctrl.Callsign
			if ac.DepartureContactAltitude == 0 {
				// If they have already contacted departure, then
				// initiating track gives control as well; otherwise
				// ControllingController is left unset until contact.
				ac.ControllingController = ctrl.Callsign
			}
			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: ctrl.Callsign,
			})

			return nil
		})
}

func (s *Sim) DropTrack(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.TrackingController = ""
			ac.ControllingController = ""
			s.eventStream.Post(Event{
				Type:           DroppedTrackEvent,
				Callsign:       ac.Callsign,
				FromController: ctrl.Callsign,
			})
			return nil
		})
}

func (s *Sim) HandoffTrack(token, callsign, controller string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			if octrl := s.World.GetControllerByCallsign(controller); octrl == nil {
				return ErrNoController
			} else if octrl.Callsign == ctrl.Callsign {
				// Can't handoff to ourself
				return ErrInvalidController
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			octrl := s.World.GetControllerByCallsign(controller)

			s.eventStream.Post(Event{
				Type:           OfferedHandoffEvent,
				FromController: ctrl.Callsign,
				ToController:   octrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = octrl.Callsign

			// Add them to the auto-accept map even if the target is
			// covered; this way, if they sign off in the interim, we still
			// end up accepting it automatically.
			acceptDelay := 4 + rand.Intn(10)
			s.Handoffs[ac.Callsign] = s.SimTime.Add(time.Duration(acceptDelay) * time.Second)
			return nil
		})
}

func (s *Sim) HandoffControl(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			var radioTransmissions []RadioTransmission
			if octrl := s.World.GetControllerByCallsign(ac.TrackingController); octrl != nil {
				name := Select(octrl.FullName != "", octrl.FullName, octrl.Callsign)
				bye := Sample("good day", "seeya")
				contact := Sample("contact ", "over to ", "")
				goodbye := contact + name + " on " + octrl.Frequency.String() + ", " + bye
				radioTransmissions = append(radioTransmissions, RadioTransmission{
					Controller: ac.ControllingController,
					Message:    goodbye,
					Type:       RadioTransmissionReadback,
				})
				radioTransmissions = append(radioTransmissions, RadioTransmission{
					Controller: ac.TrackingController,
					Message:    ac.ContactMessage(s.ReportingPoints),
					Type:       RadioTransmissionContact,
				})
			} else {
				radioTransmissions = append(radioTransmissions, RadioTransmission{
					Controller: ac.ControllingController,
					Message:    "goodbye",
					Type:       RadioTransmissionReadback,
				})
			}

			s.eventStream.Post(Event{
				Type:           HandoffControllEvent,
				FromController: ac.ControllingController,
				ToController:   ac.TrackingController,
				Callsign:       ac.Callsign,
			})

			ac.ControllingController = ac.TrackingController

			// Go ahead and climb departures the rest of the way and send
			// them direct to their first fix (if they aren't already).
			octrl := s.World.GetControllerByCallsign(ac.TrackingController)
			if ac.IsDeparture() && !octrl.IsHuman {
				s.lg.Info("departing on course", slog.String("callsign", ac.Callsign),
					slog.Int("final_altitude", ac.FlightPlan.Altitude))
				ac.DepartOnCourse()
			}

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.HandoffTrackController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			ac.TrackingController = ctrl.Callsign
			if !s.controllerIsSignedIn(ac.ControllingController) {
				// Take immediate control on handoffs from virtual
				ac.ControllingController = ctrl.Callsign
				return []RadioTransmission{RadioTransmission{
					Controller: ctrl.Callsign,
					Message:    ac.ContactMessage(s.ReportingPoints),
					Type:       RadioTransmissionContact,
				}}
			} else {
				return nil
			}
		})
}

func (s *Sim) CancelHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			delete(s.Handoffs, ac.Callsign)
			ac.HandoffTrackController = ""
			return nil
		})
}

func (s *Sim) RedirectHandoff(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if octrl := s.World.GetControllerByCallsign(controller); octrl == nil {
				return ErrNoController
			} else if octrl.Callsign == ctrl.Callsign || octrl.Callsign == ac.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return ErrInvalidController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return ErrInvalidFacility
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			octrl := s.World.GetControllerByCallsign(controller)
			ac.RedirectedHandoff.OriginalOwner = ac.TrackingController
			ac.RedirectedHandoff.Redirector = append(ac.ForceQLControllers, ctrl.Callsign)
			ac.RedirectedHandoff.RedirectedTo = octrl.Callsign
			ac.RedirectedHandoff.RDIndicator = true
			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			if ac.RedirectedHandoff.RDIndicator && ac.RedirectedHandoff.RedirectedTo == ctrl.Callsign { // Accept
				ac.ControllingController = ctrl.Callsign
				ac.HandoffTrackController = ""
				ac.TrackingController = ac.RedirectedHandoff.RedirectedTo
				ac.RedirectedHandoff = RedirectedHandoff{
					RDIndicator:   true,
					OriginalOwner: ac.RedirectedHandoff.OriginalOwner,
					Accepted:      time.Now(),
				}
			} else if len(ac.RedirectedHandoff.Redirector) > 1 && slices.Contains(ac.RedirectedHandoff.Redirector, ctrl.Callsign) { // Recall

				for index := range ac.RedirectedHandoff.Redirector {
					if ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-index-1] == ctrl.Callsign {
						ac.RedirectedHandoff.RedirectedTo = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-index-1]
						ac.RedirectedHandoff.Redirector = ac.RedirectedHandoff.Redirector[:len(ac.RedirectedHandoff.Redirector)-index-1]
						break
					}
				}
			} else {
				if ac.RedirectedHandoff.OriginalOwner == ctrl.Callsign {
					ac.RedirectedHandoff = RedirectedHandoff{} // Clear RD
				}
			}
			return nil
		})
}

func (s *Sim) ForceQL(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if s.World.GetControllerByCallsign(controller) == nil {
				return ErrNoController
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			octrl := s.World.GetControllerByCallsign(controller)
			ac.ForceQLControllers = append(ac.ForceQLControllers, octrl.Callsign)
			return nil
		})
}

func (s *Sim) RemoveForceQL(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.ForceQLControllers = FilterSlice(ac.ForceQLControllers, func(qlController string) bool { return qlController != controller })
			return nil
		})
}

func (s *Sim) PointOut(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			} else if octrl := s.World.GetControllerByCallsign(controller); octrl == nil {
				return ErrNoController
			} else if octrl.Callsign == ctrl.Callsign {
				// Can't point out to ourself
				return ErrInvalidController
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			octrl := s.World.GetControllerByCallsign(controller)
			s.eventStream.Post(Event{
				Type:           PointOutEvent,
				FromController: ctrl.Callsign,
				ToController:   octrl.Callsign,
				Callsign:       ac.Callsign,
			})

			// As with handoffs, always add it to the auto-accept list for now.
			acceptDelay := 4 + rand.Intn(10)
			if s.PointOuts[ac.Callsign] == nil {
				s.PointOuts[ac.Callsign] = make(map[string]PointOut)
			}
			s.PointOuts[ac.Callsign][octrl.Callsign] = PointOut{
				FromController: ctrl.Callsign,
				AcceptTime:     s.SimTime.Add(time.Duration(acceptDelay) * time.Second),
			}

			return nil
		})
}

func (s *Sim) AcknowledgePointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if _, ok := s.PointOuts[callsign]; !ok {
				return ErrNotPointedOutToMe
			} else if _, ok := s.PointOuts[callsign][ctrl.Callsign]; !ok {
				return ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: ctrl.Callsign,
				ToController:   s.PointOuts[callsign][ctrl.Callsign].FromController,
				Callsign:       ac.Callsign,
			})
			if len(ac.PointOutHistory) < 20 {
				ac.PointOutHistory = append([]string{ctrl.Callsign}, ac.PointOutHistory...)
			} else {
				ac.PointOutHistory = ac.PointOutHistory[:19]
				ac.PointOutHistory = append([]string{ctrl.Callsign}, ac.PointOutHistory...)
			}

			delete(s.PointOuts[callsign], ctrl.Callsign)
			return nil
		})
}

func (s *Sim) RejectPointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if _, ok := s.PointOuts[callsign]; !ok {
				return ErrNotPointedOutToMe
			} else if _, ok := s.PointOuts[callsign][ctrl.Callsign]; !ok {
				return ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: ctrl.Callsign,
				ToController:   s.PointOuts[callsign][ctrl.Callsign].FromController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts[callsign], ctrl.Callsign)
			return nil
		})
}

func (s *Sim) ToggleSPCOverride(token, callsign, spc string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.ToggleSPCOverride(spc)
			return nil
		})
}

func (s *Sim) AssignAltitude(token, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

func (s *Sim) SetTemporaryAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			ac.TempAltitude = altitude
			return nil
		})
}

type HeadingArgs struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			if hdg.Present {
				return ac.FlyPresentHeading()
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
		})
}

func (s *Sim) AssignSpeed(token, callsign string, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) ExpediteDescent(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(token, callsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(token, callsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(token, callsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(token, callsign, fix string, ar *AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(token, callsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.AtFixCleared(fix, approach)
		})
}

func (s *Sim) ExpectApproach(token, callsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.ExpectApproach(approach, s.World, s.lg)
		})
}

func (s *Sim) ClearedApproach(token, callsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			if straightIn {
				return ac.ClearedStraightInApproach(approach, s.World)
			} else {
				return ac.ClearedApproach(approach, s.World)
			}
		})
}

func (s *Sim) InterceptLocalizer(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.InterceptLocalizer(s.World)
		})
}

func (s *Sim) CancelApproachClearance(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				// Upgrade to unexpected versus it just being controller initiated.
				resp[i].Type = RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			return ac.ContactTower(s.World)
		})
}

func (s *Sim) DeleteAircraft(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if lctrl := s.LaunchConfig.Controller; lctrl != "" && lctrl != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) []RadioTransmission {
			if ac.IsDeparture() {
				s.TotalDepartures--
			} else {
				s.TotalArrivals--
			}

			s.lg.Info("deleted aircraft", slog.String("callsign", ac.Callsign),
				slog.String("controller", ctrl.Callsign))
			delete(s.World.Aircraft, ac.Callsign)
			return nil
		})
}

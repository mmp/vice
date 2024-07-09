// pkg/sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

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

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
	getweather "github.com/checkandmate1/AirportWeatherData"
	"github.com/mmp/imgui-go/v4"
)

const initialSimSeconds = 45

var (
	airportWind = make(map[string]av.Wind)
	windRequest = make(map[string]chan getweather.MetarData)
)

type Configuration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*av.Controller
	DefaultScenario  string
}

type SimScenarioConfiguration struct {
	SelectedController  string
	SelectedSplit       string
	SplitConfigurations av.SplitConfigurationSet
	PrimaryAirport      string

	Wind         av.Wind
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

func (lc *LaunchConfig) DrawDepartureUI(p platform.Platform) (changed bool) {
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

	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{tableScale * 500, 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		imgui.TableSetupColumn("Runway")
		imgui.TableSetupColumn("Category")
		imgui.TableSetupColumn("ADR")
		imgui.TableHeadersRow()

		for _, airport := range util.SortedMapKeys(lc.DepartureRates) {
			imgui.PushID(airport)
			for _, runway := range util.SortedMapKeys(lc.DepartureRates[airport]) {
				imgui.PushID(runway)
				for _, category := range util.SortedMapKeys(lc.DepartureRates[airport][runway]) {
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

func (lc *LaunchConfig) DrawArrivalUI(p platform.Platform) (changed bool) {
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
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("arrivalgroups", 3, flags, imgui.Vec2{tableScale * 500, 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		imgui.TableSetupColumn("Arrival")
		imgui.TableSetupColumn("AAR")
		imgui.TableHeadersRow()

		for _, group := range util.SortedMapKeys(lc.ArrivalGroupRates) {
			imgui.PushID(group)
			for _, ap := range util.SortedMapKeys(allAirports) {
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
	TRACON          map[string]*Configuration
	GroupName       string
	Scenario        *SimScenarioConfiguration
	ScenarioName    string
	localServer     **Server
	remoteServer    **Server
	selectedServer  *Server
	NewSimName      string // for create remote only
	RequirePassword bool   // for create remote only
	Password        string // for create remote only
	NewSimType      int

	LiveWeather               bool
	SelectedRemoteSim         string
	SelectedRemoteSimPosition string
	RemoteSimPassword         string // for join remote only

	lastRemoteSimsUpdate time.Time
	updateRemoteSimsCall *util.PendingCall

	DisplayError error

	lg            *log.Logger
	ch            chan *Connection
	defaultTRACON *string
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

func MakeNewSimConfiguration(ch chan *Connection, defaultTRACON *string, localServer **Server,
	remoteServer **Server, lg *log.Logger) NewSimConfiguration {
	c := NewSimConfiguration{
		lg:             lg,
		ch:             ch,
		localServer:    localServer,
		remoteServer:   remoteServer,
		selectedServer: *localServer,
		defaultTRACON:  defaultTRACON,
		NewSimName:     rand.AdjectiveNoun(),
	}

	c.SetTRACON(*defaultTRACON)

	return c
}

func (c *NewSimConfiguration) updateRemoteSims() {
	if time.Since(c.lastRemoteSimsUpdate) > 2*time.Second && *c.remoteServer != nil {
		c.lastRemoteSimsUpdate = time.Now()
		var rs map[string]*RemoteSim
		c.updateRemoteSimsCall = &util.PendingCall{
			Call:      (*c.remoteServer).Go("SimManager.GetRunningSims", 0, &rs, nil),
			IssueTime: time.Now(),
			OnSuccess: func(result any) {
				if *c.remoteServer != nil {
					(*c.remoteServer).runningSims = rs
				}
			},
			OnErr: func(e error) {
				c.lg.Errorf("GetRunningSims error: %v", e)

				// nil out the server if we've lost the connection; the
				// main loop will attempt to reconnect.
				if util.IsRPCServerError(e) {
					*c.remoteServer = nil
				}
			},
		}
	}
}

func (c *NewSimConfiguration) SetTRACON(name string) {
	var ok bool
	if c.TRACON, ok = c.selectedServer.configs[name]; !ok {
		if name != "" {
			c.lg.Errorf("%s: TRACON not found!", name)
		}
		configs := c.selectedServer.configs
		// Pick one at random
		name = util.SortedMapKeys(configs)[rand.Intn(len(configs))]
		c.TRACON = configs[name]
	}
	c.TRACONName = name
	c.GroupName = util.SortedMapKeys(c.TRACON)[0]

	c.SetScenario(c.GroupName, c.TRACON[c.GroupName].DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var groupConfig *Configuration
	if groupConfig, ok = c.TRACON[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.TRACONName)
		groupName = util.SortedMapKeys(c.TRACON)[0]
		groupConfig = c.TRACON[c.GroupName]
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
	return util.Select(c.NewSimType == NewSimJoinRemote, "Join", "Next")
}

func (c *NewSimConfiguration) ShowRatesWindow() bool {
	return c.NewSimType == NewSimCreateLocal || c.NewSimType == NewSimCreateRemote
}

func (c *NewSimConfiguration) DrawUI(p platform.Platform) bool {
	if c.updateRemoteSimsCall != nil && c.updateRemoteSimsCall.CheckFinished() {
		c.updateRemoteSimsCall = nil
	} else {
		c.updateRemoteSims()
	}

	if c.DisplayError != nil {
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{1, .5, .5, 1})
		if errors.Is(c.DisplayError, ErrRPCTimeout) || util.IsRPCServerError(c.DisplayError) {
			imgui.Text("Unable to reach vice server")
		} else {
			imgui.Text(c.DisplayError.Error())
		}
		imgui.PopStyleColor()
		imgui.Separator()
	}

	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if *c.remoteServer != nil {
		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Server type:")

			origType := c.NewSimType

			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create single-controller", &c.NewSimType, NewSimCreateLocal) &&
				origType != NewSimCreateLocal {
				c.selectedServer = *c.localServer
				c.SetTRACON(*c.defaultTRACON)
				c.DisplayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create multi-controller", &c.NewSimType, NewSimCreateRemote) &&
				origType != NewSimCreateRemote {
				c.selectedServer = *c.remoteServer
				c.SetTRACON(*c.defaultTRACON)
				c.DisplayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			uiStartDisable(len((*c.remoteServer).runningSims) == 0)
			if imgui.RadioButtonInt("Join multi-controller", &c.NewSimType, NewSimJoinRemote) &&
				origType != NewSimJoinRemote {
				c.selectedServer = *c.remoteServer
				c.DisplayError = nil
			}
			uiEndDisable(len((*c.remoteServer).runningSims) == 0)

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
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 600, tableScale * 300}, 0.) {
			imgui.TableSetupColumn("ARTCC")
			imgui.TableSetupColumn("ATCT/TRACON")
			imgui.TableSetupColumn("Scenario")
			imgui.TableHeadersRow()
			imgui.TableNextRow()

			// ARTCCs
			artccs := make(map[string]interface{})
			allTRACONs := util.SortedMapKeys(c.selectedServer.configs)
			for _, tracon := range allTRACONs {
				artccs[av.DB.TRACONs[tracon].ARTCC] = nil
			}
			imgui.TableNextColumn()
			if imgui.BeginChildV("artccs", imgui.Vec2{tableScale * 150, tableScale * 350}, false, /* border */
				imgui.WindowFlagsNoResize) {
				for _, artcc := range util.SortedMapKeys(artccs) {
					label := fmt.Sprintf("%s (%s)", artcc, strings.ReplaceAll(av.DB.ARTCCs[artcc].Name, " Center", ""))
					if imgui.SelectableV(label, artcc == av.DB.TRACONs[c.TRACONName].ARTCC, 0, imgui.Vec2{}) &&
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
			if imgui.BeginChildV("tracons", imgui.Vec2{tableScale * 150, tableScale * 350}, false, /* border */
				imgui.WindowFlagsNoResize) {
				for _, tracon := range allTRACONs {
					if av.DB.TRACONs[tracon].ARTCC != av.DB.TRACONs[c.TRACONName].ARTCC {
						continue
					}
					name := strings.TrimSuffix(av.DB.TRACONs[tracon].Name, " TRACON")
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
				for _, groupName := range util.SortedMapKeys(c.TRACON) {
					group := c.TRACON[groupName]
					for _, name := range util.SortedMapKeys(group.ScenarioConfigs) {
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
				c.DisplayError = nil
			}
			if c.NewSimName == "" {
				imgui.SameLine()
				imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
				imgui.PopStyleColor()
			}

			imgui.Checkbox("Require Password", &c.RequirePassword)
			if c.RequirePassword {
				imgui.InputTextV("Password", &c.Password, 0, nil)
				if c.Password == "" {
					imgui.SameLine()
					imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .1, .1, 1})
					imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
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
			validAirport := c.Scenario.PrimaryAirport != "KAAC" && *c.remoteServer != nil

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
					wind, ok = getWind(primary, c.lg)
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
		runningSims := (*c.remoteServer).runningSims

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

				imgui.PushID(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(renderer.FontAwesomeIconLock)
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

		if imgui.BeginComboV("Position", c.SelectedRemoteSimPosition, 0) {
			for _, pos := range util.SortedMapKeys(rs.AvailablePositions) {
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

func (c *NewSimConfiguration) DrawRatesUI(p platform.Platform) bool {
	c.Scenario.LaunchConfig.DrawDepartureUI(p)
	c.Scenario.LaunchConfig.DrawArrivalUI(p)
	return false
}

func getWind(airport string, lg *log.Logger) (av.Wind, bool) {
	for airport, ch := range windRequest {
		select {
		case w := <-ch:
			dirStr := fmt.Sprintf("%v", w.Wdir)
			dir, err := strconv.Atoi(dirStr)
			if err != nil {
				lg.Errorf("Error converting %v to an int: %v", dirStr, err)
			}
			airportWind[airport] = av.Wind{
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
		return av.Wind{}, false
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
		return av.Wind{}, false
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
			*c.remoteServer = nil
		}

		return err
	}

	*c.defaultTRACON = c.TRACONName

	c.ch <- &Connection{
		SimState: *result.SimState,
		SimProxy: &Proxy{
			ControllerToken: result.ControllerToken,
			Client:          c.selectedServer.RPCClient,
		},
	}

	return nil
}

type Connection struct {
	SimState State
	SimProxy *Proxy
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	Name string

	mu util.LoggingMutex

	ScenarioGroup string
	Scenario      string

	State *State

	controllers     map[string]*ServerController // from token
	SignOnPositions map[string]*av.Controller

	eventStream *EventStream
	lg          *log.Logger

	LaunchConfig LaunchConfig

	// airport -> runway -> category
	lastDeparture map[string]map[string]map[string]*av.Departure

	sameGateDepartures int
	sameDepartureCap   int

	// We track an overall "at what time do we launch the next departure"
	// time for each airport. When that time is reached, we'll pick a
	// runway, category, etc., based on the respective rates.
	NextDepartureSpawn map[string]time.Time `json:"NextDepartureSpawn2"` // avoid parse errors on old configs

	// Key is arrival group name
	NextArrivalSpawn map[string]time.Time

	Handoffs map[string]Handoff
	// callsign -> "to" controller
	PointOuts map[string]map[string]PointOut

	TotalDepartures int
	TotalArrivals   int

	ReportingPoints []av.ReportingPoint

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
}

type Handoff struct {
	Time              time.Time
	ReceivingFacility string // only for auto accept
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

func NewSim(ssc NewSimConfiguration, scenarioGroups map[string]map[string]*ScenarioGroup, isLocal bool,
	mapLib *av.VideoMapLibrary, lg *log.Logger) *Sim {
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

	// We will finally go ahead and initialize the STARSMaps needed for the
	// scenario (under the hope that the async load of the one we need has
	// finished by now so that the first GetMap() call doesn't stall.
	fa := &sg.STARSFacilityAdaptation
	initializeMaps := func(maps []string) []av.VideoMap {
		var sm []av.VideoMap
		for _, name := range maps {
			if name == "" {
				sm = append(sm, av.VideoMap{})
			} else {
				m, err := mapLib.GetMap(fa.VideoMapFile, name)
				if err != nil {
					// This should be caught earlier, during scenario
					// validation with the video map manifests...
					panic(err)
				} else {
					sm = append(sm, *m)
				}
			}
		}

		// Pad out with empty maps if not enough were specified.
		for len(sm) < NumSTARSMaps {
			sm = append(sm, av.VideoMap{})
		}
		return sm
	}

	if len(fa.VideoMapNames) > 0 && len(fa.VideoMaps) == 0 {
		fa.VideoMaps = initializeMaps(fa.VideoMapNames)
	}
	for ctrl, config := range fa.ControllerConfigs {
		config.VideoMaps = initializeMaps(config.VideoMapNames)
		fa.ControllerConfigs[ctrl] = config
	}

	s := &Sim{
		ScenarioGroup: ssc.GroupName,
		Scenario:      ssc.ScenarioName,
		LaunchConfig:  ssc.Scenario.LaunchConfig,

		controllers: make(map[string]*ServerController),

		eventStream: NewEventStream(lg),
		lg:          lg,

		lastDeparture: make(map[string]map[string]map[string]*av.Departure),

		ReportingPoints: sg.ReportingPoints,

		Password:        ssc.Password,
		RequirePassword: ssc.RequirePassword,

		SimTime:        time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:   1,
		Handoffs:  make(map[string]Handoff),
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
		s.lastDeparture[ap] = make(map[string]map[string]*av.Departure)
		for rwy := range s.LaunchConfig.DepartureRates[ap] {
			s.lastDeparture[ap][rwy] = make(map[string]*av.Departure)
		}
	}

	s.SignOnPositions = make(map[string]*av.Controller)
	add := func(callsign string) {
		if ctrl, ok := sg.ControlPositions[callsign]; !ok {
			lg.Errorf("%s: control position unknown??!", callsign)
		} else {
			ctrlCopy := *ctrl
			ctrlCopy.IsHuman = true
			s.SignOnPositions[callsign] = &ctrlCopy
		}
	}
	if !isLocal {
		configs, err := sc.SplitConfigurations.GetConfiguration(ssc.Scenario.SelectedSplit)
		if err != nil {
			lg.Errorf("unable to get configurations for split: %v", err)
		}
		for callsign := range configs {
			add(callsign)
		}
	} else {
		add(sc.SoloController)
	}

	s.State = newState(ssc.Scenario.SelectedSplit, ssc.LiveWeather, isLocal, s, sg, sc, lg)

	s.setInitialSpawnTimes()

	return s
}

func (s *Sim) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", s.Name),
		slog.String("scenario_group", s.ScenarioGroup),
		slog.String("scenario", s.Scenario),
		slog.Any("controllers", s.State.Controllers),
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
		slog.Any("aircraft", s.State.Aircraft))
}

func (s *Sim) SignOn(callsign string) (*State, string, error) {
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

	// Make a deep copy so that if the server is running on the same
	// system, that the client doesn't see updates until they're explicitly
	// sent. (And similarly, that any speculative client changes to the
	// World state to improve responsiveness don't actually affect the
	// server.)
	ss, err := deep.Copy(*s.State)
	ss.Callsign = callsign

	return &ss, token, err
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
			return av.ErrNoController
		}
		s.State.Controllers[callsign] = ctrl

		if callsign == s.State.PrimaryController {
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
		for _, ac := range s.State.Aircraft {
			ac.HandleControllerDisconnect(ctrl.Callsign, s.State.PrimaryController)
		}

		if ctrl.Callsign == s.LaunchConfig.Controller {
			// give up control of launches so someone else can take it.
			s.LaunchConfig.Controller = ""
		}

		ctrl.events.Unsubscribe()
		delete(s.controllers, token)
		delete(s.State.Controllers, ctrl.Callsign)

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

	delete(s.State.Controllers, oldCallsign)

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: oldCallsign + " has signed off.",
	})

	for _, ac := range s.State.Aircraft {
		if keepTracks {
			ac.TransferTracks(oldCallsign, ctrl.Callsign)
		} else {
			ac.HandleControllerDisconnect(ctrl.Callsign, s.State.PrimaryController)
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

type WorldUpdate struct {
	Aircraft    map[string]*av.Aircraft
	Controllers map[string]*av.Controller
	Time        time.Time

	ERAMComputers ERAMComputers

	LaunchConfig LaunchConfig

	SimIsPaused     bool
	SimRate         float32
	Events          []Event
	TotalDepartures int
	TotalArrivals   int
}

func (s *Sim) GetWorldUpdate(token string, update *WorldUpdate) error {
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

		var err error
		*update, err = deep.Copy(WorldUpdate{
			Aircraft:        s.State.Aircraft,
			Controllers:     s.State.Controllers,
			ERAMComputers:   s.State.ERAMComputers,
			Time:            s.SimTime,
			LaunchConfig:    s.LaunchConfig,
			SimIsPaused:     s.Paused,
			SimRate:         s.SimRate,
			Events:          ctrl.events.Get(),
			TotalDepartures: s.TotalDepartures,
			TotalArrivals:   s.TotalArrivals,
		})

		if err != nil {
			panic(err)
		}
		return err
	}
}

func (s *Sim) Activate(lg *log.Logger) {
	if s.Name == "" {
		s.lg = lg
	} else {
		s.lg = lg.With(slog.String("sim_name", s.Name))
	}

	if s.controllers == nil {
		s.controllers = make(map[string]*ServerController)
	}
	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}

	now := time.Now()
	s.lastUpdateTime = now

	s.lastDeparture = make(map[string]map[string]map[string]*av.Departure)
	for ap := range s.LaunchConfig.DepartureRates {
		s.lastDeparture[ap] = make(map[string]map[string]*av.Departure)
		for rwy := range s.LaunchConfig.DepartureRates[ap] {
			s.lastDeparture[ap][rwy] = make(map[string]*av.Departure)
		}
	}
}

func (s *Sim) PreSave() {
	if s.State != nil {
		s.State.PreSave()
	}
}

func (s *Sim) PostLoad(ml *av.VideoMapLibrary) error {
	if s.State != nil {
		return s.State.PostLoad(ml)
	}
	return nil
}

func (s *State) PreSave() {
	s.STARSFacilityAdaptation.PreSave()
}

func (s *State) PostLoad(ml *av.VideoMapLibrary) error {
	return s.STARSFacilityAdaptation.PostLoad(ml)
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	startUpdate := time.Now()
	defer func() {
		if d := time.Since(startUpdate); d > 200*time.Millisecond {
			s.lg.Warn("unexpectedly long Sim Update() call", slog.Duration("duration", d),
				slog.Any("sim", s))
		}
	}()

	for _, ac := range s.State.Aircraft {
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

	if !s.controllerIsSignedIn(s.State.PrimaryController) {
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
	s.State.SimTime = s.SimTime

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
		if !now.After(t.Time) {
			continue
		}

		if ac, ok := s.State.Aircraft[callsign]; ok && ac.HandoffTrackController != "" &&
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

			if ac, ok := s.State.Aircraft[callsign]; ok && !s.controllerIsSignedIn(toController) {
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
		for callsign, ac := range s.State.Aircraft {
			passedWaypoint := ac.Update(s.State, s.lg)
			if passedWaypoint != nil {
				if passedWaypoint.Handoff {
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

				if passedWaypoint.PointOut != "" {
					for _, ctrl := range s.State.Controllers {
						// Look for a controller with a matching TCP id.
						if ctrl.SectorId == passedWaypoint.PointOut {
							// Don't do the point out if a human is
							// controlling the aircraft.
							if fromCtrl := s.State.Controllers[ac.ControllingController]; fromCtrl != nil && !fromCtrl.IsHuman {
								s.pointOut(ac.Callsign, fromCtrl, ctrl)
								break
							}
						}
					}
				}

				if passedWaypoint.Delete {
					s.lg.Info("deleting aircraft at waypoint", slog.Any("waypoint", passedWaypoint))
					delete(s.State.Aircraft, ac.Callsign)
				}
			}

			// Possibly go around
			// FIXME: maintain GoAroundDistance, state, in Sim, not Aircraft
			if ac.GoAroundDistance != nil {
				if d, err := ac.DistanceToEndOfApproach(); err == nil && d < *ac.GoAroundDistance {
					s.lg.Info("randomly going around")
					ac.GoAroundDistance = nil // only go around once
					rt := ac.GoAround()
					ac.ControllingController = s.State.DepartureController(ac, s.lg)
					PostRadioEvents(ac.Callsign, rt, s)

					// If it was handed off to tower, hand it back to us
					if ac.TrackingController != "" && ac.TrackingController != ac.ApproachController {
						ac.HandoffTrackController = s.State.DepartureController(ac, s.lg)
						if ac.HandoffTrackController == "" {
							ac.HandoffTrackController = ac.ApproachController
						}
						s.PostEvent(Event{
							Type:           OfferedHandoffEvent,
							Callsign:       ac.Callsign,
							FromController: ac.TrackingController,
							ToController:   ac.ApproachController,
						})
					}
				}
			}

			// Contact the departure controller
			if ac.IsDeparture() && ac.DepartureContactAltitude != 0 &&
				ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude {
				// Time to check in
				ctrl := s.ResolveController(ac.DepartureContactController)
				s.lg.Info("contacting departure controller", slog.String("callsign", ctrl))

				airportName := ac.FlightPlan.DepartureAirport
				if ap, ok := s.State.Airports[airportName]; ok && ap.Name != "" {
					airportName = ap.Name
				}

				msg := "departing " + airportName + ", " + ac.Nav.DepartureMessage()
				PostRadioEvents(ac.Callsign, []av.RadioTransmission{av.RadioTransmission{
					Controller: ctrl,
					Message:    msg,
					Type:       av.RadioTransmissionContact,
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
				if ap := s.State.Airports[ac.FlightPlan.DepartureAirport]; ap != nil &&
					math.NMDistance2LL(ac.Position(), ap.Location) > 250 {
					s.lg.Info("culled far-away departure", slog.String("callsign", callsign))
					s.State.DeleteAircraft(ac)
				}
			} else if ap := s.State.Airports[ac.FlightPlan.ArrivalAirport]; ap != nil &&
				math.NMDistance2LL(ac.Position(), ap.Location) > 250 {
				// We only expect this case to hit for an unattended vice,
				// where aircraft are being spawned but are then flying
				// along on a heading without being controlled...
				s.lg.Info("culled far-away arrival", slog.String("callsign", callsign))
				s.State.DeleteAircraft(ac)
			}
		}
	}

	// Don't spawn automatically if someone is spawning manually.
	if s.LaunchConfig.Mode == LaunchAutomatic {
		s.spawnAircraft()
	}

	s.State.ERAMComputers.Update(s.State.TRACON, now, s.eventStream, s.lg)
}

func PostRadioEvents(from string, transmissions []av.RadioTransmission, ep EventPoster) {
	for _, rt := range transmissions {
		ep.PostEvent(Event{
			Type:                  RadioTransmissionEvent,
			Callsign:              from,
			ToController:          rt.Controller,
			Message:               rt.Message,
			RadioTransmissionType: rt.Type,
		})
	}
}

func (s *Sim) ResolveController(callsign string) string {
	if s.State.MultiControllers == nil {
		// Single controller
		return s.State.PrimaryController
	} else if len(s.controllers) == 0 {
		// This can happen during the prespawn phase right after launching but
		// before the user has been signed in.
		return s.State.PrimaryController
	} else {
		c, err := s.State.MultiControllers.ResolveController(callsign,
			func(callsign string) bool {
				return s.controllerIsSignedIn(callsign)
			})
		if err != nil {
			s.lg.Errorf("%s: unable to resolve controller: %v", callsign, err)
		}

		if c == "" { // This shouldn't happen...
			return s.State.PrimaryController
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
	s.State.SimTime = s.SimTime
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
	seconds := math.Lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
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
			if ac, err := s.CreateArrival(group, arrivalAirport, goAround, s.lg); err != nil {
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
		ac, dep, err := s.CreateDeparture(airport, runway, category,
			s.LaunchConfig.DepartureChallenge, prevDep, s.lg)
		if err != nil {
			s.lg.Infof("CreateDeparture error: %v", err)
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

func (s *Sim) LaunchAircraft(ac av.Aircraft) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.launchAircraftNoLock(ac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) launchAircraftNoLock(ac av.Aircraft) {
	if _, ok := s.State.Aircraft[ac.Callsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!", slog.String("callsign", ac.Callsign))
		return
	}

	s.State.Aircraft[ac.Callsign] = &ac

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
	check func(c *av.Controller, ac *av.Aircraft) error,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.State.Aircraft[callsign]; !ok {
		return av.ErrNoAircraftForCallsign
	} else {
		if sc.Callsign == "Observer" {
			return av.ErrOtherControllerHasTrack
		}

		ctrl := s.State.Controllers[sc.Callsign]
		if ctrl == nil {
			s.lg.Error("controller unknown", slog.String("controller", sc.Callsign),
				slog.Any("world_controllers", s.State.Controllers))
			return av.ErrNoController
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			preAc := *ac
			radioTransmissions := cmd(ctrl, ac)
			s.lg.Info("dispatch_command", slog.String("callsign", ac.Callsign),
				slog.Any("prepost_aircraft", []av.Aircraft{preAc, *ac}),
				slog.Any("radio_transmissions", radioTransmissions))
			PostRadioEvents(ac.Callsign, radioTransmissions, s)
			return nil
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
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
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.Scratchpad = scratchpad

			err := s.State.ERAMComputers.SetScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				s.lg.Errorf("%s/%s: SetScratchPad %s: %v", ac.Callsign, ctrl.Facility,
					scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) SetSecondaryScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.SecondaryScratchpad = scratchpad

			err := s.State.ERAMComputers.SetSecondaryScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				s.lg.Errorf("%s/%s: SetSecondaryScratchPad %s: %v", ac.Callsign, ctrl.Facility,
					scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) ChangeSquawk(token, callsign string, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.Squawk = sq

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Callsign,
				Message:    "squawk " + sq.String(),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) Ident(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:     IdentEvent,
				Callsign: ac.Callsign,
			})

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Callsign,
				Message:    "ident",
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(token, callsign string, dir *math.CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != c.Callsign {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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

func (s *Sim) AutoAssociateFP(token, callsign string, fp *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AddTrackInformation(callsign, TrackInformation{
				TrackOwner:      ac.TrackingController, // Should happen initially, so ac.TrackingController can still be used
				FlightPlan:      fp,
				AutoAssociateFP: true,
			})

			return nil
		})

}

func (s *Sim) CreateUnsupportedTrack(token, callsign string, ut *UnsupportedTrack) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	callsign = util.SortedMapKeys(s.State.Aircraft)[0]
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AddUnsupportedTrack(*ut)
			return nil
		})
}

func (s *Sim) UploadFlightPlan(token string, Type int, plan *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ctrl := s.State.Controllers[s.controllers[token].Callsign]
	eram, stars, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
	if err != nil {
		return err

	}

	switch Type {
	case LocalNonEnroute:
		stars.ContainedPlans[plan.FlightPlan.AssignedSquawk] = plan
	case LocalEnroute, RemoteEnroute:
		eram.FlightPlans[plan.FlightPlan.AssignedSquawk] = plan
	}

	return nil
}

func (s *Sim) InitiateTrack(token, callsign string, fp *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
			}
			if octrl := s.State.Controllers[controller]; octrl == nil {
				return av.ErrNoController
			} else if octrl.Callsign == ctrl.Callsign {
				// Can't handoff to ourself
				return av.ErrInvalidController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]

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
			s.Handoffs[ac.Callsign] = Handoff{
				Time: s.SimTime.Add(time.Duration(acceptDelay) * time.Second),
			}
			return nil
		})
}

func (s *Sim) HandoffControl(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			if octrl := s.State.Controllers[ac.TrackingController]; octrl != nil {
				name := util.Select(octrl.FullName != "", octrl.FullName, octrl.Callsign)
				bye := rand.Sample("good day", "seeya")
				contact := rand.Sample("contact ", "over to ", "")
				goodbye := contact + name + " on " + octrl.Frequency.String() + ", " + bye
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    goodbye,
					Type:       av.RadioTransmissionReadback,
				})
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.TrackingController,
					Message:    ac.ContactMessage(s.ReportingPoints),
					Type:       av.RadioTransmissionContact,
				})
			} else {
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    "goodbye",
					Type:       av.RadioTransmissionReadback,
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
			octrl := s.State.Controllers[ac.TrackingController]
			if ac.IsDeparture() && octrl != nil && !octrl.IsHuman {
				s.lg.Info("departing on course", slog.String("callsign", ac.Callsign),
					slog.Int("final_altitude", ac.FlightPlan.Altitude))
				ac.DepartOnCourse(s.lg)
			}

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.HandoffTrackController != ctrl.Callsign {
				return av.ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
				return []av.RadioTransmission{av.RadioTransmission{
					Controller: ctrl.Callsign,
					Message:    ac.ContactMessage(s.ReportingPoints),
					Type:       av.RadioTransmissionContact,
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
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, ac.Callsign)
			ac.HandoffTrackController = ""
			ac.RedirectedHandoff = av.RedirectedHandoff{}
			return nil
		})
}

func (s *Sim) RedirectHandoff(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if octrl := s.State.Controllers[controller]; octrl == nil {
				return av.ErrNoController
			} else if octrl.Callsign == ctrl.Callsign || octrl.Callsign == ac.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return av.ErrInvalidController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return av.ErrInvalidFacility
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			ac.RedirectedHandoff.OriginalOwner = ac.TrackingController
			if ac.RedirectedHandoff.ShouldFallbackToHandoff(ctrl.Callsign, octrl.Callsign) {
				ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[0]
				ac.RedirectedHandoff = av.RedirectedHandoff{}
				return nil
			}
			ac.RedirectedHandoff.AddRedirector(ctrl)
			ac.RedirectedHandoff.RedirectedTo = octrl.Callsign
			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if ac.RedirectedHandoff.RedirectedTo == ctrl.Callsign { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: ac.RedirectedHandoff.OriginalOwner,
					ToController:   ctrl.Callsign,
					Callsign:       ac.Callsign,
				})
				ac.ControllingController = ctrl.Callsign
				ac.HandoffTrackController = ""
				ac.TrackingController = ac.RedirectedHandoff.RedirectedTo
				ac.RedirectedHandoff = av.RedirectedHandoff{}
			} else if ac.RedirectedHandoff.GetLastRedirector() == ctrl.Callsign { // Recall (only the last redirector is able to recall)
				if len(ac.RedirectedHandoff.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					ac.RedirectedHandoff.RedirectedTo = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
					ac.RedirectedHandoff = av.RedirectedHandoff{}
				}
			}
			return nil
		})
}

func (s *Sim) ForceQL(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if s.State.Controllers[controller] == nil {
				return av.ErrNoController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: ctrl.Callsign,
				ToController:   octrl.Callsign,
				Callsign:       ac.Callsign,
			})

			return nil
		})
}

func (s *Sim) PointOut(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
			} else if octrl := s.State.Controllers[controller]; octrl == nil {
				return av.ErrNoController
			} else if octrl.Callsign == ctrl.Callsign {
				// Can't point out to ourself
				return av.ErrInvalidController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.pointOut(ac.Callsign, ctrl, octrl)
			return nil
		})
}

func (s *Sim) pointOut(callsign string, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.Callsign,
		ToController:   to.Callsign,
		Callsign:       callsign,
	})

	acceptDelay := 4 + rand.Intn(10)
	if s.PointOuts[callsign] == nil {
		s.PointOuts[callsign] = make(map[string]PointOut)
	}
	s.PointOuts[callsign][to.Callsign] = PointOut{
		FromController: from.Callsign,
		AcceptTime:     s.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if _, ok := s.PointOuts[callsign]; !ok {
				return av.ErrNotPointedOutToMe
			} else if _, ok := s.PointOuts[callsign][ctrl.Callsign]; !ok {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if _, ok := s.PointOuts[callsign]; !ok {
				return av.ErrNotPointedOutToMe
			} else if _, ok := s.PointOuts[callsign][ctrl.Callsign]; !ok {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.ToggleSPCOverride(spc)
			return nil
		})
}

func (s *Sim) AssignAltitude(token, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

func (s *Sim) SetTemporaryAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
	Turn            av.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(token, callsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(token, callsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(token, callsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(token, callsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(token, callsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AtFixCleared(fix, approach)
		})
}

func (s *Sim) ExpectApproach(token, callsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.State.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return av.ErrUnknownAirport
		}
	}

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.State.ArrivalGroups, s.lg)
		})
}

func (s *Sim) ClearedApproach(token, callsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if straightIn {
				return ac.ClearedStraightInApproach(approach, s.State.ArrivalGroups)
			} else {
				return ac.ClearedApproach(approach, s.State.ArrivalGroups, s.lg)
			}
		})
}

func (s *Sim) InterceptLocalizer(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.InterceptLocalizer(s.State.ArrivalGroups)
		})
}

func (s *Sim) CancelApproachClearance(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				resp[i].Type = av.RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ContactTower(s.State.Controllers, s.lg)
		})
}

func (s *Sim) DeleteAircraft(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if lctrl := s.LaunchConfig.Controller; lctrl != "" && lctrl != ctrl.Callsign {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if ac.IsDeparture() {
				s.TotalDepartures--
			} else {
				s.TotalArrivals--
			}

			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: fmt.Sprintf("%s deleted %s", ctrl.Callsign, ac.Callsign),
			})

			s.lg.Info("deleted aircraft", slog.String("callsign", ac.Callsign),
				slog.String("controller", ctrl.Callsign))

			s.State.DeleteAircraft(ac)

			return nil
		})
}

func (s *Sim) DeleteAllAircraft(token string) error {
	for cs := range s.State.Aircraft {
		if err := s.DeleteAircraft(token, cs); err != nil {
			return err
		}
	}
	return nil
}

// If |b| is true, all following imgui elements will be disabled (and drawn
// accordingly).
func uiStartDisable(b bool) {
	if b {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
}

// Each call to uiStartDisable should have a matching call to uiEndDisable,
// with the same Boolean value passed to it.
func uiEndDisable(b bool) {
	if b {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
}

func (ss *State) sampleAircraft(icao, fleet string, lg *log.Logger) (*av.Aircraft, string) {
	al, ok := av.DB.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil, ""
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil, ""
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fl {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := av.DB.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from fleet %+v, airline %s",
			aircraft, fleet, icao)
		return nil, ""
	}

	// random callsign
	callsign := strings.ToUpper(icao)
	for {
		format := "####"
		if len(al.Callsign.CallsignFormats) > 0 {
			format = rand.SampleSlice(al.Callsign.CallsignFormats)
		}

		id := ""
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					// Don't start with a 0.
					id += strconv.Itoa(1 + rand.Intn(9))
				} else {
					id += strconv.Itoa(rand.Intn(10))
				}
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			}
		}
		if _, ok := ss.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
			callsign += id
			break
		}
	}

	squawk := av.Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &av.Aircraft{
		Callsign: callsign,
		Squawk:   squawk,
		Mode:     av.Charlie,
	}, acType
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string, goAround bool, lg *log.Logger) (*av.Aircraft, error) {
	arrivals := s.State.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := rand.SampleFiltered(arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := rand.SampleSlice(arr.Airlines[arrivalAirport])
	ac, acType := s.State.sampleAircraft(airline.ICAO, airline.Fleet, lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	// ac.Squawk = artcc.CreateSquawk()
	ac.FlightPlan = ac.NewFlightPlan(av.IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		arrivalController, err = s.State.MultiControllers.GetArrivalController(arrivalGroup)
		if err != nil {
			lg.Error("Unable to resolve arrival controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}

		if arrivalController == "" {
			arrivalController = s.State.PrimaryController
		}
	}

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], s.State.ArrivalGroups,
		arrivalGroup, idx, arrivalController, goAround, s.State.NmPerLongitude, s.State.MagneticVariation, lg); err != nil {
		return nil, err
	}

	return ac, nil
}

func (s *Sim) CreateDeparture(departureAirport, runway, category string, challenge float32,
	lastDeparture *av.Departure, lg *log.Logger) (*av.Aircraft, *av.Departure, error) {
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]

	var dep *av.Departure
	if s.sameDepartureCap == 0 {
		s.sameDepartureCap = rand.Intn(3) + 1 // Set the initial max same departure cap (1-3)
	}
	if rand.Float32() < challenge && lastDeparture != nil && s.sameGateDepartures < s.sameDepartureCap {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		pred := util.Select(rand.Float32() < .5,
			func(d av.Departure) bool { return d.Exit == lastDeparture.Exit },
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && ap.ExitCategories[d.Exit] == ap.ExitCategories[lastDeparture.Exit]
			})

		if idx := rand.SampleFiltered(ap.Departures, pred); idx == -1 {
			// This should never happen...
			lg.Errorf("%s/%s/%s: unable to sample departure", departureAirport, runway, category)
		} else {
			dep = &ap.Departures[idx]
		}

	}

	if dep == nil {
		// Sample uniformly, minding the category, if specified
		idx := rand.SampleFiltered(ap.Departures,
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
			})

		if idx == -1 {
			// This shouldn't ever happen...
			return nil, nil, fmt.Errorf("%s/%s: unable to find a valid departure",
				departureAirport, rwy.Runway)
		}
		dep = &ap.Departures[idx]
	}

	if lastDeparture != nil && (dep.Exit == lastDeparture.Exit && s.sameGateDepartures >= s.sameDepartureCap) {
		return nil, nil, fmt.Errorf("couldn't make a departure")
	}

	// Same gate buffer is a random int between 3-4 that gives a period after a few same gate departures.
	// For example, WHITE, WHITE, WHITE, DIXIE, NEWEL, GAYEL, MERIT, DIXIE, DIXIE
	// Another same-gate departure will not be happen untill after MERIT (in this example) because of the buffer.
	sameGateBuffer := rand.Intn(2) + 3

	if s.sameGateDepartures >= s.sameDepartureCap+sameGateBuffer || (lastDeparture != nil && dep.Exit != lastDeparture.Exit) { // reset back to zero if its at 7 or if there is a new gate
		s.sameDepartureCap = rand.Intn(3) + 1
		s.sameGateDepartures = 0
	}

	airline := rand.SampleSlice(dep.Airlines)
	ac, acType := s.State.sampleAircraft(airline.ICAO, airline.Fleet, lg)
	if ac == nil {
		return nil, nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = ac.NewFlightPlan(av.IFR, acType, departureAirport, dep.Destination)
	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(ap, departureAirport, dep, runway, exitRoute,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State.Scratchpads,
		s.State.PrimaryController, s.State.MultiControllers, lg); err != nil {
		return nil, nil, err
	}

	/* Keep adding to World sameGateDepartures number until the departure cap + the buffer so that no more
	same-gate departures are launched, then reset it to zero. Once the buffer is reached, it will reset World sameGateDepartures to zero*/
	s.sameGateDepartures += 1

	return ac, dep, nil
}

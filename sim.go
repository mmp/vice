// sim.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mmp/imgui-go/v4"
	"golang.org/x/exp/slog"
)

type SimConfiguration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*Controller
	DefaultScenario  string
}

type SimScenarioConfiguration struct {
	SelectedController string
	Wind               Wind
	LaunchConfig       LaunchConfig

	DepartureRunways []ScenarioGroupDepartureRunway
	ArrivalRunways   []ScenarioGroupArrivalRunway
}

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
	ArrivalGroupRates map[string]map[string]int
}

func MakeLaunchConfig(dep []ScenarioGroupDepartureRunway, arr map[string]map[string]int) LaunchConfig {
	lc := LaunchConfig{
		DepartureChallenge: 0.25,
		GoAroundRate:       0.05,
		ArrivalGroupRates:  arr,
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

	if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{500, 0}, 0.) {
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
					imgui.Text(runway)
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
	nAirports := len(allAirports)

	imgui.Text("Arrivals")
	imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", sumRates))
	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	if imgui.BeginTableV("arrivalgroups", 1+nAirports, flags, imgui.Vec2{500, 0}, 0.) {
		imgui.TableSetupColumn("Arrival")
		sortedAirports := SortedMapKeys(allAirports)
		for _, ap := range sortedAirports {
			imgui.TableSetupColumn(ap + " AAR")
		}
		imgui.TableHeadersRow()

		for _, group := range SortedMapKeys(lc.ArrivalGroupRates) {
			imgui.PushID(group)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(group)
			for _, ap := range sortedAirports {
				imgui.TableNextColumn()
				if rate, ok := lc.ArrivalGroupRates[group][ap]; ok {
					r := int32(rate)
					changed = imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0) || changed
					lc.ArrivalGroupRates[group][ap] = int(r)
				}
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	return
}

type NewSimConfiguration struct {
	Group          *SimConfiguration
	GroupName      string
	Scenario       *SimScenarioConfiguration
	ScenarioName   string
	selectedServer *SimServer
	NewSimName     string // for create remote only
	NewSimType     int

	SelectedRemoteSim         string
	SelectedRemoteSimPosition string
	lastRemoteSimsUpdate      time.Time
	updateRemoteSimsCall      *PendingCall

	displayError error
}

type RemoteSim struct {
	GroupName          string
	ScenarioName       string
	PrimaryController  string
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

	c.SetScenarioGroup(globalConfig.LastScenarioGroup)

	return c
}

func (c *NewSimConfiguration) updateRemoteSims() {
	if time.Since(c.lastRemoteSimsUpdate) > 2*time.Second && remoteServer != nil {
		c.lastRemoteSimsUpdate = time.Now()
		var rs map[string]*RemoteSim
		c.updateRemoteSimsCall = &PendingCall{
			Call:      remoteServer.client.Go("SimManager.GetRunningSims", 0, &rs, nil),
			IssueTime: time.Now(),
			OnSuccess: func(result any) {
				remoteServer.runningSims = rs
			},
			OnErr: func(e error) {
				lg.Errorf("GetRunningSims: %v", e)

				// nil out the server if we've lost the connection; the
				// main loop will attempt to reconnect.
				if isRPCServerError(e) {
					remoteServer = nil
				}
			},
		}
	}
}

func (c *NewSimConfiguration) SetScenarioGroup(name string) {
	var ok bool
	if c.Group, ok = c.selectedServer.configs[name]; !ok {
		if name != "" {
			lg.Errorf("%s: scenario group not found!", name)
		}
		name = SortedMapKeys(c.selectedServer.configs)[0] // first one
		c.Group = c.selectedServer.configs[name]
	}
	c.GroupName = name

	c.SetScenario(c.Group.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(name string) {
	var ok bool
	if c.Scenario, ok = c.Group.ScenarioConfigs[name]; !ok {
		if name != "" {
			lg.Errorf("%s: scenario not found in group %s", name, c.GroupName)
		}
		name = SortedMapKeys(c.Group.ScenarioConfigs)[0]
		c.Scenario = c.Group.ScenarioConfigs[name]
	}
	c.ScenarioName = name
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

	if remoteServer != nil {
		sc := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{sc * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Server type:")

			origType := c.NewSimType

			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create single-controller", &c.NewSimType, NewSimCreateLocal) &&
				origType != NewSimCreateLocal {
				c.selectedServer = localServer
				c.SetScenarioGroup("")
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create multi-controller", &c.NewSimType, NewSimCreateRemote) &&
				origType != NewSimCreateRemote {
				c.selectedServer = remoteServer
				c.SetScenarioGroup("")
				c.displayError = nil
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			if imgui.RadioButtonInt("Join multi-controller", &c.NewSimType, NewSimJoinRemote) &&
				origType != NewSimJoinRemote {
				c.selectedServer = remoteServer
				c.displayError = nil
			}

			imgui.EndTable()
		}
	} else {
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Unable to connect to the multi-controller vice server; " +
			"only single-player scenarios are available.")
		imgui.PopStyleColor()
	}
	imgui.Separator()

	if c.NewSimType == NewSimCreateLocal || c.NewSimType == NewSimCreateRemote {
		if imgui.BeginComboV("Scenario Group", c.GroupName, imgui.ComboFlagsHeightLarge) {
			for _, name := range SortedMapKeys(c.selectedServer.configs) {
				if imgui.SelectableV(name, name == c.GroupName, 0, imgui.Vec2{}) {
					c.SetScenarioGroup(name)
				}
			}
			imgui.EndCombo()
		}

		if imgui.BeginComboV("Config", c.ScenarioName, imgui.ComboFlagsHeightLarge) {
			for _, name := range SortedMapKeys(c.Group.ScenarioConfigs) {
				if imgui.SelectableV(name, name == c.ScenarioName, 0, imgui.Vec2{}) {
					c.SetScenario(name)
				}
			}
			imgui.EndCombo()
		}

		if c.NewSimType == NewSimCreateRemote {
			if imgui.InputTextV("Name", &c.NewSimName, 0, nil) {
				c.displayError = nil
			}
			if c.NewSimName == "" {
				imgui.SameLine()
				imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(FontAwesomeIconExclamationTriangle)
				imgui.PopStyleColor()
			}
		}

		if imgui.BeginTableV("scenario", 2, 0, imgui.Vec2{500, 0}, 0.) {
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

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Wind:")
			imgui.TableNextColumn()
			wind := c.Scenario.Wind
			if wind.Gust > wind.Speed {
				imgui.Text(fmt.Sprintf("%03d at %d gust %d", wind.Direction, wind.Speed, wind.Gust))
			} else {
				imgui.Text(fmt.Sprintf("%03d at %d", wind.Direction, wind.Speed))
			}
			imgui.EndTable()

		}
		imgui.Separator()

		c.Scenario.LaunchConfig.DrawDepartureUI()
		c.Scenario.LaunchConfig.DrawArrivalUI()
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
		if imgui.BeginTableV("simulation", 3, flags, imgui.Vec2{500, 0}, 0.) {
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
	}

	return false
}

func (c *NewSimConfiguration) OkDisabled() bool {
	return c.NewSimType == NewSimCreateRemote && c.NewSimName == ""
}

func (c *NewSimConfiguration) Start() error {
	var result NewSimResult
	if err := c.selectedServer.client.CallWithTimeout("SimManager.New", c, &result); err != nil {
		// Problem with the connection to the remote server? Let the main
		// loop try to reconnect.
		remoteServer = nil

		return err
	}

	result.World.simProxy = &SimProxy{
		ControllerToken: result.ControllerToken,
		Client:          c.selectedServer.client,
	}

	globalConfig.LastScenarioGroup = c.GroupName

	newWorldChan <- result.World

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	Name string

	mu sync.Mutex

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

	// The same runway may be present multiple times in DepartureRates,
	// with different categories. However, we want to make sure that we
	// don't spawn two aircraft on the same runway at the same time (or
	// close to it).  Therefore, here we track a per-runway "when's the
	// next time that we will spawn *something* from the runway" time.
	// When the time is up, we'll figure out which specific category to
	// use...
	// airport -> runway -> time
	NextDepartureSpawn map[string]map[string]time.Time

	// Key is arrival group name
	NextArrivalSpawn map[string]time.Time

	Handoffs map[string]time.Time

	TotalDepartures int
	TotalArrivals   int

	lastSimUpdate time.Time

	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..
	updateTimeSlop time.Duration

	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	lastLogTime    time.Time
	SimRate        float32
	Paused         bool

	STARSInputOverride string
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

func NewSim(ssc NewSimConfiguration, scenarioGroups map[string]*ScenarioGroup, isLocal bool, lg *Logger) *Sim {
	lg = &Logger{Logger: lg.With(slog.String("sim_name", ssc.NewSimName))}

	sg, ok := scenarioGroups[ssc.GroupName]
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

		SimTime:        time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:  1,
		Handoffs: make(map[string]time.Time),
	}

	if !isLocal {
		s.Name = ssc.NewSimName
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
		for callsign := range sc.MultiControllers {
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
		w.PrimaryController, _ = GetPrimaryController(sc.MultiControllers)
		w.MultiControllers = DuplicateMap(sc.MultiControllers)
	} else {
		w.PrimaryController = sc.SoloController
	}
	w.MagneticVariation = sg.MagneticVariation
	w.NmPerLongitude = sg.NmPerLongitude
	w.Wind = sc.Wind
	w.Airports = sg.Airports
	w.Fixes = sg.Fixes
	w.PrimaryAirport = sg.PrimaryAirport
	w.RadarSites = sg.RadarSites
	w.Center = sg.Center
	w.Range = sg.Range
	w.DefaultMap = sc.DefaultMap
	w.STARSMaps = sg.STARSMaps
	w.Scratchpads = sg.Scratchpads
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

	for _, callsign := range sc.VirtualControllers {
		if ctrl, ok := sg.ControlPositions[callsign]; ok {
			w.Controllers[callsign] = ctrl
		} else {
			s.lg.Errorf("%s: controller not found in ControlPositions??", callsign)
		}
	}

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)
	fakeMETAR := func(icao string) {
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

	for ap := range w.DepartureAirports {
		fakeMETAR(ap)
	}
	for ap := range w.ArrivalAirports {
		fakeMETAR(ap)
	}

	return w
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
		slog.Int("departures", s.TotalDepartures),
		slog.Int("arrivals", s.TotalArrivals),
		slog.Time("sim_time", s.SimTime),
		slog.Float64("sim_rate", float64(s.SimRate)),
		slog.Bool("paused", s.Paused),
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
	s.mu.Lock()
	defer s.mu.Unlock()

	if callsign != "Observer" {
		if s.controllerIsSignedIn(callsign) {
			return ErrControllerAlreadySignedIn
		}

		ctrl, ok := s.SignOnPositions[callsign]
		if !ok {
			return ErrNoController
		}
		s.World.Controllers[callsign] = ctrl

	}

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: callsign + " has signed on.",
	})
	s.lg.Infof("%s: controller signed", callsign)

	return nil
}

func (s *Sim) SignOff(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		// Drop track on controlled aircraft
		for _, ac := range s.World.Aircraft {
			ac.DropControllerTrack(ctrl.Callsign)
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
			ac.DropControllerTrack(ctrl.Callsign)
		}
	}

	return nil
}

func (s *Sim) TogglePause(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func controllersChanged(a, b map[string]*Controller) bool {
	if len(a) != len(b) {
		return true
	}
	for cs := range a {
		if _, ok := b[cs]; !ok {
			return true
		}
	}
	return false
}

func (s *Sim) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		ctrl.lastUpdateCall = time.Now()
		if ctrl.warnedNoUpdateCalls {
			ctrl.warnedNoUpdateCalls = false
			s.lg.Errorf("%s: connection re-established", ctrl.Callsign)
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

func (s *Sim) Activate(lg *Logger) error {
	s.lg = &Logger{Logger: lg.With(slog.String("sim_name", s.Name))}

	var e ErrorLogger

	if s.controllers == nil {
		s.controllers = make(map[string]*ServerController)
	}
	if s.eventStream == nil {
		s.eventStream = NewEventStream()
	}

	initializeWaypointLocations := func(waypoints []Waypoint, e *ErrorLogger) {
		for i, wp := range waypoints {
			if e != nil {
				e.Push("Fix " + wp.Fix)
			}
			if pos, ok := s.World.Locate(wp.Fix); !ok {
				if e != nil {
					e.ErrorString("unable to locate waypoint")
				}
			} else {
				waypoints[i].Location = pos
			}
			if e != nil {
				e.Pop()
			}
		}
	}

	now := time.Now()
	s.lastUpdateTime = now
	s.World.lastUpdateRequest = now

	for _, ac := range s.World.Aircraft {
		e.Push(ac.Callsign)

		if ap := ac.Approach; ap != nil {
			for i := range ap.Waypoints {
				initializeWaypointLocations(ap.Waypoints[i], &e)
			}
		}

		for rwy, wp := range ac.ArrivalRunwayWaypoints {
			e.Push("Arrival runway " + rwy)
			initializeWaypointLocations(wp, &e)
			e.Pop()
		}

		e.Pop()
	}

	for callsign := range s.World.Controllers {
		s.World.Controllers[callsign].Callsign = callsign
	}

	for _, rwy := range s.World.DepartureRunways {
		for _, route := range rwy.ExitRoutes {
			initializeWaypointLocations(route.Waypoints, &e)
		}
	}

	s.lastDeparture = make(map[string]map[string]map[string]*Departure)
	for ap := range s.LaunchConfig.DepartureRates {
		s.lastDeparture[ap] = make(map[string]map[string]*Departure)
		for rwy := range s.LaunchConfig.DepartureRates[ap] {
			s.lastDeparture[ap][rwy] = make(map[string]*Departure)
		}
	}

	for _, arrivals := range s.World.ArrivalGroups {
		for _, arr := range arrivals {
			initializeWaypointLocations(arr.Waypoints, &e)
			for _, rwp := range arr.RunwayWaypoints {
				initializeWaypointLocations(rwp, &e)
			}
		}
	}

	if e.HaveErrors() {
		e.PrintErrors(s.lg)
		return ErrRestoringSavedState
	}
	return nil

}

func (s *Sim) SetSTARSInput(input string) {
	s.STARSInputOverride = input
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock()
	defer s.mu.Unlock()

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
					s.lg.Errorf("%s: no messages for 5 seconds", ctrl.Callsign)
					s.eventStream.Post(Event{
						Type:    StatusMessageEvent,
						Message: ctrl.Callsign + " has not been heard from for 5 seconds. Connection lost?",
					})
				}

				if time.Since(ctrl.lastUpdateCall) > 15*time.Second {
					s.lg.Errorf("%s: signing off idle controller", ctrl.Callsign)
					s.mu.Unlock()
					s.SignOff(token)
					s.mu.Lock()
				}
			}
		}
	}

	paused := s.Paused || !s.controllerIsSignedIn(s.World.PrimaryController)

	if !paused {
		// Figure out how much time has passed since the last update: wallclock
		// time is scaled by the sim rate, then we add in any time from the
		// last update that wasn't accounted for.
		elapsed := time.Since(s.lastUpdateTime)
		elapsed = time.Duration(s.SimRate*float32(elapsed)) + s.updateTimeSlop
		// Run the sim for this many seconds
		ns := int(elapsed.Truncate(time.Second).Seconds())
		for i := 0; i < ns; i++ {
			s.SimTime = s.SimTime.Add(time.Second)
			s.updateState()
		}
		s.updateTimeSlop = elapsed - elapsed.Truncate(time.Second)

		s.lastUpdateTime = time.Now()
	}

	// Log the current state of everything once a minute
	if time.Since(s.lastLogTime) > time.Minute {
		s.lastLogTime = time.Now()

		if paused {
			s.lg.Info("sim is paused", slog.Any("state", s))
		} else {
			s.lg.Info("sim is active", slog.Any("state", s))
		}
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
			s.lg.Infof("%s: automatic accept of handoff from %s to %s", ac.Callsign,
				ac.TrackingController, ac.HandoffTrackController)

			ac.TrackingController = ac.HandoffTrackController
			ac.HandoffTrackController = ""
		}
		delete(s.Handoffs, callsign)
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now
		for callsign, ac := range s.World.Aircraft {
			ac.Update(s.World, s.World, s)

			// Cull departures that are far from the airport.
			if ap := s.World.GetAirport(ac.FlightPlan.DepartureAirport); ap != nil && ac.IsDeparture {
				if nmdistance2ll(ac.Position, ap.Location) > 200 {
					s.lg.Infof("%s: culled far-away departure", callsign)
					delete(s.World.Aircraft, callsign)
				}
			}

			// FIXME: this is sort of ugly to have here...
			if ac.HandoffTrackController == s.World.Callsign {
				// We hit a /ho at a fix; update to the correct controller.
				// Note that s.controllers may be empty when initially
				// running the sim after it has been launched. Just hand
				// off to the primary controller in that case...
				if len(s.World.MultiControllers) > 0 && len(s.controllers) > 0 {
					callsign := ""
					if ac.IsDeparture {
						callsign = s.getDepartureController(ac)
					} else {
						callsign = ac.ArrivalHandoffController
					}
					if callsign == "" {
						ac.HandoffTrackController = ""
					}

					i := 0
					for {
						if s.controllerIsSignedIn(callsign) {
							ac.HandoffTrackController = callsign
							break
						}
						mc, ok := s.World.MultiControllers[callsign]
						if !ok {
							s.lg.Errorf("%s: failed to find controller in MultiControllers", callsign)
							ac.HandoffTrackController = ""
							break
						}
						callsign = mc.BackupController

						i++
						if i == 20 {
							s.lg.Errorf("%s: unable to find backup for arrival handoff controller",
								ac.ArrivalHandoffController)
							ac.HandoffTrackController = ""
							break
						}
					}

				} else {
					ac.HandoffTrackController = s.World.PrimaryController
				}

				s.lg.Infof("%s: resolved handoff: %s -> %s", ac.Callsign, ac.ControllingController,
					ac.HandoffTrackController)

				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.ControllingController,
					ToController:   ac.HandoffTrackController,
				})
			}
		}
	}

	// Don't spawn automatically if someone is spawning manually.
	if s.LaunchConfig.Mode == LaunchAutomatic {
		s.spawnAircraft()
	}
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSimUpdate)
}

func (s *Sim) controllerIsSignedIn(callsign string) bool {
	for _, ctrl := range s.controllers {
		if ctrl.Callsign == callsign {
			return true
		}
	}
	return false
}

func (s *Sim) getDepartureController(ac *Aircraft) string {
	for cs, ctrl := range s.World.MultiControllers {
		if ctrl.Departure && s.controllerIsSignedIn(cs) {
			return cs
		}
	}
	return s.World.PrimaryController
}

func (s *Sim) prespawn() {
	s.lg.Infof("starting aircraft prespawn")

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		s.SimTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.SimTime = time.Now()
	s.lastUpdateTime = time.Now()

	s.lg.Infof("finished aircraft prespawn")
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

	s.NextDepartureSpawn = make(map[string]map[string]time.Time)
	for airport, runwayRates := range s.LaunchConfig.DepartureRates {
		spawn := make(map[string]time.Time)

		for runway, categoryRates := range runwayRates {
			rateSum := 0
			for _, rate := range categoryRates {
				rateSum += rate
			}
			if rateSum > 0 {
				spawn[runway] = randomSpawn(rateSum)
			}
		}

		if len(spawn) > 0 {
			s.NextDepartureSpawn[airport] = spawn
		}
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

func randomWait(rate int) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}
	avgSeconds := 3600 / float32(rate)
	seconds := lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
	return time.Duration(seconds * float32(time.Second))
}

func (s *Sim) spawnAircraft() {
	now := s.SimTime

	for group, airportRates := range s.LaunchConfig.ArrivalGroupRates {
		if now.After(s.NextArrivalSpawn[group]) {
			arrivalAirport, rateSum := sampleRateMap(airportRates)

			goAround := rand.Float32() < s.LaunchConfig.GoAroundRate
			if ac, err := s.World.CreateArrival(group, arrivalAirport, goAround); err != nil {
				s.lg.Errorf("CreateArrival: %v", err)
			} else if ac != nil {
				s.launchAircraftNoLock(*ac)
				s.lg.Info("spawned arrival", slog.Any("aircraft", ac))
				s.NextArrivalSpawn[group] = now.Add(randomWait(rateSum))
			}
		}
	}

	for airport, runwayTimes := range s.NextDepartureSpawn {
		for runway, spawnTime := range runwayTimes {
			if !now.After(spawnTime) {
				continue
			}

			// Figure out which category to launch
			category, rateSum := sampleRateMap(s.LaunchConfig.DepartureRates[airport][runway])
			if rateSum == 0 {
				s.lg.Errorf("%s/%s: couldn't find a matching runway for spawning departure?", airport, runway)
				continue
			}

			prevDep := s.lastDeparture[airport][runway][category]
			s.lg.Infof("%s/%s/%s: previous departure", airport, runway, category)
			ac, dep, err := s.World.CreateDeparture(airport, runway, category,
				s.LaunchConfig.DepartureChallenge, prevDep)
			if err != nil {
				s.lg.Errorf("CreateDeparture: %v", err)
			} else {
				s.lastDeparture[airport][runway][category] = dep
				s.lg.Infof("%s/%s/%s: launch departure", airport, runway, category)
				s.launchAircraftNoLock(*ac)
				s.lg.Info("spawned departure", slog.Any("aircraft", ac))
				s.NextDepartureSpawn[airport][runway] = now.Add(randomWait(rateSum))
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Commands from the user

func (s *Sim) SetSimRate(token string, rate float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.SimRate = rate
		s.lg.Infof("sim rate set to %f", s.SimRate)
		return nil
	}
}

func (s *Sim) SetLaunchConfig(token string, lc LaunchConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ctrl.Callsign != s.LaunchConfig.Controller {
		return ErrNotLaunchController
	} else {
		// Update the next spawn time for any rates that changed.
		for ap, rwyRates := range lc.DepartureRates {
			for rwy, categoryRates := range rwyRates {
				newSum, oldSum := 0, 0
				for category, rate := range categoryRates {
					newSum += rate
					oldSum += s.LaunchConfig.DepartureRates[ap][rwy][category]
				}
				if newSum != oldSum {
					s.lg.Infof("%s/%s: departure rate changed %d -> %d", ap, rwy, oldSum, newSum)
					s.NextDepartureSpawn[ap][rwy] = s.SimTime.Add(randomWait(newSum))
				}
			}
		}
		for group, groupRates := range lc.ArrivalGroupRates {
			newSum, oldSum := 0, 0
			for ap, rate := range groupRates {
				newSum += rate
				oldSum += s.LaunchConfig.ArrivalGroupRates[group][ap]
			}
			if newSum != oldSum {
				s.lg.Infof("%s: arrival rate changed %d -> %d", group, oldSum, newSum)
				s.NextArrivalSpawn[group] = s.SimTime.Add(randomWait(newSum))
			}

		}

		s.LaunchConfig = lc
		return nil
	}
}

func (s *Sim) TakeOrReturnLaunchControl(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

	s.launchAircraftNoLock(ac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) launchAircraftNoLock(ac Aircraft) {
	if _, ok := s.World.Aircraft[ac.Callsign]; ok {
		s.lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
		return
	}
	s.World.Aircraft[ac.Callsign] = &ac

	if ac.IsDeparture {
		s.TotalDepartures++
	} else {
		s.TotalArrivals++
	}

	ac.MagneticVariation = s.World.MagneticVariation
	ac.NmPerLongitude = s.World.NmPerLongitude

	ac.RunWaypointCommands(ac.Waypoints[0], s.World, s)

	ac.Position = ac.Waypoints[0].Location
	if ac.Position.IsZero() {
		s.lg.Errorf("%s: uninitialized initial waypoint position! %+v", ac.Callsign, ac.Waypoints[0])
		return
	}

	ac.Heading = float32(ac.Waypoints[0].Heading)
	if ac.Heading == 0 { // unassigned, so get the heading from the next fix
		ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, ac.NmPerLongitude,
			ac.MagneticVariation)
	}
	ac.Waypoints = FilterSlice(ac.Waypoints[1:], func(wp Waypoint) bool { return !wp.Location.IsZero() })
}

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *Controller, ac *Aircraft) error,
	cmd func(*Controller, *Aircraft) (string, string, error)) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.World.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if sc.Callsign == "Observer" {
			return ErrOtherControllerHasTrack
		}

		ctrl := s.World.GetController(sc.Callsign)
		if ctrl == nil {
			s.lg.Error(sc.Callsign+": couldn't get controller",
				slog.Any("world_controllers", s.World.Controllers))
			return ErrNoController
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			octrl := ac.ControllingController

			preResponse, postResponse, err := cmd(ctrl, ac)
			if preResponse != "" {
				s.lg.Infof("%s@%s: %s", ac.Callsign, octrl, preResponse)
				s.eventStream.Post(Event{
					Type:         RadioTransmissionEvent,
					Callsign:     ac.Callsign,
					ToController: octrl,
					Message:      preResponse,
				})
			}
			if postResponse != "" {
				s.lg.Infof("%s@%s: %s", ac.Callsign, ac.ControllingController, postResponse)
				s.eventStream.Post(Event{
					Type:         RadioTransmissionEvent,
					Callsign:     ac.Callsign,
					ToController: ac.ControllingController,
					Message:      postResponse,
				})
			}
			return err
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, string, error)) error {
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
	cmd func(*Controller, *Aircraft) (string, string, error)) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

func (s *Sim) SetScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			ac.Scratchpad = scratchpad
			return "", "", nil
		})
}

func (s *Sim) InitiateTrack(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: ctrl.Callsign,
			})
			return "", "", nil
		})
}

func (s *Sim) DropTrack(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			ac.TrackingController = ""
			ac.ControllingController = ""
			s.eventStream.Post(Event{
				Type:           DroppedTrackEvent,
				Callsign:       ac.Callsign,
				FromController: ctrl.Callsign,
			})
			return "", "", nil
		})
}

func (s *Sim) HandoffTrack(token, callsign, controller string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			if octrl := s.World.GetController(controller); octrl == nil {
				return "", "", ErrNoController
			} else {
				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					FromController: ctrl.Callsign,
					ToController:   octrl.Callsign,
					Callsign:       ac.Callsign,
				})

				ac.HandoffTrackController = octrl.Callsign

				acceptDelay := 4 + rand.Intn(10)
				s.Handoffs[ac.Callsign] = s.SimTime.Add(time.Duration(acceptDelay) * time.Second)
				return "", "", nil
			}
		})
}

func (s *Sim) HandoffControl(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			ac.ControllingController = ac.TrackingController

			// Go ahead and climb departures the rest of the way now.
			if ac.IsDeparture {
				s.lg.Infof("%s: climbing to %d", ac.Callsign, ac.FlightPlan.Altitude)
				ac.Nav.V = &MaintainAltitude{Altitude: float32(ac.FlightPlan.Altitude)}
			}

			if octrl := s.World.GetController(ac.ControllingController); octrl != nil {
				name := Select(octrl.FullName != "", octrl.FullName, octrl.Callsign)
				goodbye := fmt.Sprintf("over to %s on %s, good day", name, octrl.Frequency)
				contact := ac.Nav.ContactMessage(ac)
				return goodbye, contact, nil
			} else {
				return "goodbye", "", nil
			}
		})
}

func (s *Sim) AcceptHandoff(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.HandoffTrackController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			ac.TrackingController = ctrl.Callsign
			if !s.controllerIsSignedIn(ac.ControllingController) {
				// Only take control on handoffs from virtual
				ac.ControllingController = ctrl.Callsign
			}

			return "", "", nil
		})
}

func (s *Sim) RejectHandoff(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.HandoffTrackController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			s.eventStream.Post(Event{
				Type:           RejectedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			return "", "", nil
		})
}

func (s *Sim) CancelHandoff(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			delete(s.Handoffs, ac.Callsign)
			ac.HandoffTrackController = ""
			return "", "", nil
		})
}

func (s *Sim) PointOut(token, callsign, controller string) error {
	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			if octrl := s.World.GetController(controller); octrl == nil {
				return "", "", ErrNoController
			} else {
				s.eventStream.Post(Event{
					Type:           PointOutEvent,
					FromController: ctrl.Callsign,
					ToController:   octrl.Callsign,
					Callsign:       ac.Callsign,
				})
				return "", "", nil
			}
		})
}

func (s *Sim) AssignAltitude(token, callsign string, altitude int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.AssignAltitude(altitude)
			return resp, "", err
		})
}

func (s *Sim) SetTemporaryAltitude(token, callsign string, altitude int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			ac.TempAltitude = altitude
			return "", "", nil
		})
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			if hdg.Present {
				if _, err := ac.AssignHeading(int(ac.Heading), TurnClosest); err == nil {
					return "fly present heading", "", nil
				} else {
					return "", "", err
				}
			} else if hdg.LeftDegrees != 0 {
				resp, err := ac.TurnLeft(hdg.LeftDegrees)
				return resp, "", err
			} else if hdg.RightDegrees != 0 {
				resp, err := ac.TurnRight(hdg.RightDegrees)
				return resp, "", err
			} else {
				resp, err := ac.AssignHeading(hdg.Heading, hdg.Turn)
				return resp, "", err
			}
		})
}

func (s *Sim) AssignSpeed(token, callsign string, speed int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.AssignSpeed(speed)
			return resp, "", err
		})
}

func (s *Sim) DirectFix(token, callsign, fix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.DirectFix(fix)
			return resp, "", err
		})
}

func (s *Sim) DepartFixHeading(token, callsign, fix string, heading int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.DepartFixHeading(fix, heading)
			return resp, "", err
		})
}

func (s *Sim) CrossFixAt(token, callsign, fix string, alt, speed int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.CrossFixAt(fix, alt, speed)
			return resp, "", err
		})
}

func (s *Sim) ExpectApproach(token, callsign, approach string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.ExpectApproach(approach, s.World)
			return resp, "", err
		})
}

func (s *Sim) ClearedApproach(token, callsign, approach string, straightIn bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			if straightIn {
				resp, err := ac.ClearedStraightInApproach(approach, s.World)
				return resp, "", err
			} else {
				resp, err := ac.ClearedApproach(approach, s.World)
				return resp, "", err
			}
		})
}

func (s *Sim) CancelApproachClearance(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			resp, err := ac.CancelApproachClearance()
			return resp, "", err
		})
}

func (s *Sim) GoAround(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			return ac.GoAround(), "", nil
		})
}

func (s *Sim) DeleteAircraft(token, callsign string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if lctrl := s.LaunchConfig.Controller; lctrl != "" && lctrl != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, string, error) {
			if ac.IsDeparture {
				s.TotalDepartures--
			} else {
				s.TotalArrivals--
			}

			lg.Infof("%s: deleting aircraft", ac.Callsign)
			delete(s.World.Aircraft, ac.Callsign)
			return "", "", nil
		})
}

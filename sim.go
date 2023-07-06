// sim.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

func init() {
	gob.Register(&FlyHeading{})
	gob.Register(&FlyRoute{})
	gob.Register(&FlyRacetrackPT{})
	gob.Register(&FlyStandard45PT{})

	gob.Register(&MaintainSpeed{})
	gob.Register(&FinalApproachSpeed{})

	gob.Register(&MaintainAltitude{})
	gob.Register(&FlyRacetrackPT{})

	gob.Register(&SpeedAfterAltitude{})
	gob.Register(&AltitudeAfterSpeed{})
	gob.Register(&ApproachSpeedAt5DME{})
	gob.Register(&ClimbOnceAirborne{})
	gob.Register(&TurnToInterceptLocalizer{})
	gob.Register(&HoldLocalizerAfterIntercept{})
	gob.Register(&GoAround{})
}

var (
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrControllerAlreadySignedIn = errors.New("controller with that callsign already signed in")
	ErrInvalidControllerToken    = errors.New("invalid controller token")
)

type SimConfiguration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*Controller
	DefaultScenario  string
}

type SimScenarioConfiguration struct {
	DepartureChallenge   float32
	GoAroundRate         float32
	SelectedController   string
	AllControlPositions  []string
	OpenControlPositions []string
	Wind                 Wind

	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]int

	DepartureRunways []ScenarioGroupDepartureRunway
	ArrivalRunways   []ScenarioGroupArrivalRunway
}

type SimServer struct {
	name    string
	client  *rpc.Client
	configs map[string]*SimConfiguration
}

type NewSimConfiguration struct {
	Group                     *SimConfiguration
	GroupName                 string
	Scenario                  *SimScenarioConfiguration
	ScenarioName              string
	localServer, remoteServer *SimServer
	selectedServer            *SimServer
	newSimName                string // for create remote only
	NewSimType                int
	availableRemoteSims       map[string]*RemoteSim
	lastRemoteSimsUpdate      time.Time
	updateRemoteSimsCall      *PendingCall
}

type RemoteSim struct {
	GroupName          string
	ScenarioName       string
	AvailablePositions map[string]interface{}
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

func MakeNewSimConfiguration(localServer *SimServer, remoteServer *SimServer) NewSimConfiguration {
	c := NewSimConfiguration{
		localServer:    localServer,
		remoteServer:   remoteServer,
		selectedServer: localServer,
	}

	c.updateRemoteSims()

	c.SetScenarioGroup(globalConfig.LastScenarioGroup)

	return c
}

func (c *NewSimConfiguration) updateRemoteSims() {
	if time.Since(c.lastRemoteSimsUpdate) > 1*time.Second && c.remoteServer != nil {
		c.lastRemoteSimsUpdate = time.Now()
		c.updateRemoteSimsCall = &PendingCall{
			Call:      c.remoteServer.client.Go("SimManager.GetRunningSims", 0, nil, nil),
			IssueTime: time.Now(),
			OnSuccess: func(avail any) {
				if avail != nil {
					var ok bool
					if c.availableRemoteSims, ok = avail.(map[string]*RemoteSim); !ok {
						lg.Errorf("got type %T rather than map[string]*RemoteSim", avail)
					}
				}
			},
			OnErr: func(e error) {
				lg.Errorf("%v", e)
			},
		}
	}
}

func (c *NewSimConfiguration) SetScenarioGroup(name string) {
	var ok bool
	if c.Group, ok = c.selectedServer.configs[name]; !ok {
		lg.Errorf("%s: scenario group not found!", name)
		name = SortedMapKeys(c.selectedServer.configs)[0] // first one
		c.Group = c.selectedServer.configs[name]
	}
	c.GroupName = name

	c.SetScenario(c.Group.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(name string) {
	var ok bool
	if c.Scenario, ok = c.Group.ScenarioConfigs[name]; !ok {
		lg.Errorf("%s: scenario not found in group %s", name, c.GroupName)
		name = SortedMapKeys(c.Group.ScenarioConfigs)[0]
		c.Scenario = c.Group.ScenarioConfigs[name]
	}
	c.ScenarioName = name
}

func (c *NewSimConfiguration) DrawUI() bool {
	if c.updateRemoteSimsCall != nil && c.updateRemoteSimsCall.CheckFinished(nil) {
		c.updateRemoteSimsCall = nil
	}

	if c.remoteServer != nil {
		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Server type:")

			origType := c.NewSimType

			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create single-controller", &c.NewSimType, NewSimCreateLocal) &&
				origType != NewSimCreateLocal {
				c.selectedServer = c.localServer
				c.SetScenarioGroup("")
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			if imgui.RadioButtonInt("Create multi-controller", &c.NewSimType, NewSimCreateRemote) &&
				origType != NewSimCreateRemote {
				c.selectedServer = c.remoteServer
				c.SetScenarioGroup("")
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			disable := len(c.availableRemoteSims) == 0
			uiStartDisable(disable)
			if imgui.RadioButtonInt("Join multi-controller", &c.NewSimType, NewSimJoinRemote) &&
				origType != NewSimJoinRemote {
				c.selectedServer = c.remoteServer
			}
			uiEndDisable(disable)

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
			imgui.InputTextV("Name", &c.newSimName, 0, nil)
			if c.newSimName == "" {
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

			if len(c.Scenario.DepartureRates) > 0 {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Departing:")
				imgui.TableNextColumn()

				var runways []string
				for airport, runwayRates := range c.Scenario.DepartureRates {
					for runway, categoryRates := range runwayRates {
						for _, rate := range categoryRates {
							if rate > 0 {
								runways = append(runways, airport+"/"+runway)
								break
							}
						}
					}
				}
				sort.Strings(runways)
				imgui.Text(strings.Join(runways, ", "))
			}

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

		if len(c.Scenario.DepartureRunways) > 0 {
			imgui.Separator()
			imgui.Text("Departures")

			sumRates := 0
			for _, runwayRates := range c.Scenario.DepartureRates {
				for _, categoryRates := range runwayRates {
					for _, rate := range categoryRates {
						sumRates += rate
					}
				}
			}
			imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", sumRates))

			imgui.SliderFloatV("Sequencing challenge", &c.Scenario.DepartureChallenge, 0, 1, "%.02f", 0)
			flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

			if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{500, 0}, 0.) {
				imgui.TableSetupColumn("Airport")
				imgui.TableSetupColumn("Runway")
				imgui.TableSetupColumn("Category")
				imgui.TableSetupColumn("ADR")
				imgui.TableHeadersRow()

				for _, airport := range SortedMapKeys(c.Scenario.DepartureRates) {
					imgui.PushID(airport)
					for _, runway := range SortedMapKeys(c.Scenario.DepartureRates[airport]) {
						imgui.PushID(runway)
						for _, category := range SortedMapKeys(c.Scenario.DepartureRates[airport][runway]) {
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

							r := int32(c.Scenario.DepartureRates[airport][runway][category])
							imgui.InputIntV("##adr", &r, 0, 120, 0)
							c.Scenario.DepartureRates[airport][runway][category] = int(r)

							imgui.PopID()
						}
						imgui.PopID()
					}
					imgui.PopID()
				}
				imgui.EndTable()
			}
		}

		if len(c.Scenario.ArrivalGroupRates) > 0 {
			// Figure out how many unique airports we've got for AAR columns in the table
			// and also sum up the overall arrival rate
			allAirports := make(map[string]interface{})
			sumRates := 0
			for _, agr := range c.Scenario.ArrivalGroupRates {
				for ap, rate := range agr {
					allAirports[ap] = nil
					sumRates += rate
				}
			}
			nAirports := len(allAirports)

			imgui.Separator()
			imgui.Text("Arrivals")
			imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", sumRates))
			imgui.SliderFloatV("Go around probability", &c.Scenario.GoAroundRate, 0, 1, "%.02f", 0)

			flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("arrivalgroups", 1+nAirports, flags, imgui.Vec2{500, 0}, 0.) {
				imgui.TableSetupColumn("Arrival")
				sortedAirports := SortedMapKeys(allAirports)
				for _, ap := range sortedAirports {
					imgui.TableSetupColumn(ap + " AAR")
				}
				imgui.TableHeadersRow()

				for _, group := range SortedMapKeys(c.Scenario.ArrivalGroupRates) {
					imgui.PushID(group)
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(group)
					for _, ap := range sortedAirports {
						imgui.TableNextColumn()
						if rate, ok := c.Scenario.ArrivalGroupRates[group][ap]; ok {
							r := int32(rate)
							imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0)
							c.Scenario.ArrivalGroupRates[group][ap] = int(r)
						}
					}
					imgui.PopID()
				}
				imgui.EndTable()
			}
		}
	} else {
		// Join remote
		if len(c.Scenario.AllControlPositions) > 1 {
			if imgui.BeginComboV("Control Position", c.Scenario.SelectedController, imgui.ComboFlagsHeightLarge) {
				for _, controllerName := range c.Scenario.AllControlPositions {
					if controllerName[0] == '_' {
						continue
					}
					disable := Find(c.Scenario.OpenControlPositions, controllerName) == -1
					uiStartDisable(disable)
					if imgui.SelectableV(controllerName, controllerName == c.Scenario.SelectedController, 0, imgui.Vec2{}) {
						c.Scenario.SelectedController = controllerName
					}
					uiEndDisable(disable)
				}
				imgui.EndCombo()
			}
		}
	}

	return false
}

func (c *NewSimConfiguration) OkDisabled() bool {
	return c.NewSimType == NewSimCreateRemote && c.newSimName == ""
}

func (c *NewSimConfiguration) Start() error {
	var result NewSimResult
	if err := c.selectedServer.client.Call("SimManager.New", c, &result); err != nil {
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

type SimProxy struct {
	ControllerToken string
	Client          *rpc.Client
}

func (s *SimProxy) TogglePause() *rpc.Call {
	return s.Client.Go("Sim.TogglePause", s.ControllerToken, nil, nil)
}

func (s *SimProxy) SignOff(_, _ *struct{}) error {
	return s.Client.Call("Sim.SignOff", s.ControllerToken, nil)
}

func (s *SimProxy) GetSerializeSim() (*Sim, error) {
	var sim Sim
	err := s.Client.Call("SimManager.GetSerializeSim", s.ControllerToken, &sim)
	return &sim, err
}

func (s *SimProxy) GetWorldUpdate(wu *SimWorldUpdate) *rpc.Call {
	return s.Client.Go("Sim.GetWorldUpdate", s.ControllerToken, wu, nil)
}

func (s *SimProxy) SetSimRate(r float32) *rpc.Call {
	return s.Client.Go("Sim.SetSimRate",
		&SimRateSpecifier{
			ControllerToken: s.ControllerToken,
			Rate:            r,
		}, nil, nil)
}

func (s *SimProxy) TakeOrReturnLaunchControl() *rpc.Call {
	return s.Client.Go("Sim.TakeOrReturnLaunchControl", s.ControllerToken, nil, nil)
}

func (s *SimProxy) SetScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetScratchpad", &AircraftPropertiesSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *SimProxy) InitiateTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) DropTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DropTrack", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.HandoffTrack", &HandoffSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *SimProxy) HandoffControl(callsign string) *rpc.Call {
	return s.Client.Go("Sim.HandoffControl", &HandoffSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) AcceptHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptHandoff", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) CancelHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.CancelHandoff", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) AssignAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetAltitude", &AltitudeAssignment{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) SetTemporaryAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetTemporaryAltitude", &AltitudeAssignment{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) GoAround(callsign string) *rpc.Call {
	return s.Client.Go("Sim.GoAround", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) DeleteAircraft(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DeleteAircraft", &AircraftSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

type AircraftCommandsSpecifier struct {
	ControllerToken string
	Callsign        string
	Commands        string
}

func (s *SimProxy) RunAircraftCommands(callsign string, cmds string) *rpc.Call {
	return s.Client.Go("Sim.RunAircraftCommands", &AircraftCommandsSpecifier{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, nil, nil)
}

func (s *SimProxy) LaunchAircraft(ac Aircraft) *rpc.Call {
	return s.Client.Go("Sim.LaunchAircraft", &LaunchSpecifier{
		ControllerToken: s.ControllerToken,
		Aircraft:        ac,
	}, nil, nil)
}

///////////////////////////////////////////////////////////////////////////
// SimManager

type SimManager struct {
	scenarioGroups       map[string]*ScenarioGroup
	configs              map[string]*SimConfiguration
	activeSims           map[string]*Sim
	controllerTokenToSim map[string]*Sim
}

func NewSimManager(scenarioGroups map[string]*ScenarioGroup,
	simConfigurations map[string]*SimConfiguration) *SimManager {
	return &SimManager{
		scenarioGroups:       scenarioGroups,
		configs:              simConfigurations,
		activeSims:           make(map[string]*Sim),
		controllerTokenToSim: make(map[string]*Sim),
	}
}

type NewSimResult struct {
	World           *World
	ControllerToken string
}

func (sm *SimManager) New(config *NewSimConfiguration, result *NewSimResult) error {
	sim := NewSim(*config, sm.scenarioGroups)
	sim.prespawn()

	return sm.Add(sim, result)
}

func (sm *SimManager) Add(sim *Sim, result *NewSimResult) error {
	sim.Activate()

	// Empty sim name is just a local sim, so no problem with replacing it...
	if _, ok := sm.activeSims[sim.Name]; ok && sim.Name != "" {
		return errors.New(sim.Name + ": a sim with that name already exists")
	}
	sm.activeSims[sim.Name] = sim

	world, token, err := sim.SignOn(sim.World.PrimaryController)
	if err != nil {
		return err
	}
	sm.controllerTokenToSim[token] = sim

	go func() {
		for {
			sim.Update()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	*result = NewSimResult{
		World:           world,
		ControllerToken: token,
	}

	return nil
}

func (sm *SimManager) GetSimConfigurations(_ int, result *map[string]*SimConfiguration) error {
	*result = sm.configs
	lg.Printf("Encoded scenario groups size: %d", encodedGobSize(*result))
	return nil
}

func (sm *SimManager) GetRunningSims(_ int, result *map[string]map[string]interface{}) error {
	running := make(map[string]map[string]interface{})
	for name, s := range sm.activeSims {
		running[name] = make(map[string]interface{})

		// Figure out which positions are available; start with all of the possible ones,
		// then delete those that are active
		s.mu.Lock()
		running[name][s.World.PrimaryController] = nil
		for callsign := range s.World.MultiControllers {
			running[name][callsign] = nil
		}
		for _, ctrl := range s.controllers {
			delete(running[name], ctrl.Callsign)
		}
		s.mu.Unlock()
	}

	*result = running
	return nil
}

func (sm *SimManager) GetSerializeSim(token string, s *Sim) error {
	if sm.controllerTokenToSim == nil {
		return ErrNoSimForControllerToken
	}
	sim, ok := sm.controllerTokenToSim[token]
	if !ok {
		return ErrNoSimForControllerToken
	}
	*s = *sim
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SimDispatcher

type SimDispatcher struct {
	sm *SimManager
}

func (sd *SimDispatcher) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	if sim, ok := sd.sm.controllerTokenToSim[token]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.GetWorldUpdate(token, update)
	}
}

func (sd *SimDispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[token]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TakeOrReturnLaunchControl(token)
	}
}

func (sd *SimDispatcher) SetSimRate(r *SimRateSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[r.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetSimRate(r, nil)
	}
}

func (sd *SimDispatcher) TogglePause(token string, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[token]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TogglePause(token, nil)
	}
}

func (sd *SimDispatcher) SetScratchpad(a *AircraftPropertiesSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetScratchpad(a, nil)
	}
}

func (sd *SimDispatcher) InitiateTrack(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.InitiateTrack(a, nil)
	}
}

func (sd *SimDispatcher) DropTrack(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DropTrack(a, nil)
	}
}

func (sd *SimDispatcher) HandoffTrack(h *HandoffSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.HandoffTrack(h, nil)
	}
}

func (sd *SimDispatcher) HandoffControl(h *HandoffSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.HandoffControl(h, nil)
	}
}

func (sd *SimDispatcher) AcceptHandoff(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AcceptHandoff(a, nil)
	}
}

func (sd *SimDispatcher) CancelHandoff(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.CancelHandoff(a, nil)
	}
}

func (sd *SimDispatcher) AssignAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[alt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AssignAltitude(alt, nil)
	}
}

func (sd *SimDispatcher) SetTemporaryAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[alt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetTemporaryAltitude(alt, nil)
	}
}

func (sd *SimDispatcher) AssignHeading(hdg *HeadingAssignment, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[hdg.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AssignHeading(hdg, nil)
	}
}

func (sd *SimDispatcher) AssignSpeed(sa *SpeedAssignment, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[sa.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AssignSpeed(sa, nil)
	}
}

func (sd *SimDispatcher) DirectFix(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[f.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DirectFix(f, nil)
	}
}

func (sd *SimDispatcher) DepartFixHeading(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[f.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DepartFixHeading(f, nil)
	}
}

func (sd *SimDispatcher) CrossFixAt(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[f.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.CrossFixAt(f, nil)
	}
}

func (sd *SimDispatcher) ExpectApproach(a *ApproachAssignment, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ExpectApproach(a, nil)
	}
}

func (sd *SimDispatcher) ClearedApproach(c *ApproachClearance, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[c.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ClearedApproach(c, nil)
	}
}

func (sd *SimDispatcher) GoAround(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.GoAround(a, nil)
	}
}

func (sd *SimDispatcher) DeleteAircraft(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DeleteAircraft(a, nil)
	}
}

type AircraftCommandsError struct {
	error
	Remaining []string
}

func (e AircraftCommandsError) Error() string {
	s := e.error.Error()
	if len(e.Remaining) > 0 {
		s += " remaining: " + strings.Join(e.Remaining, " ")
	}
	return s
}

func (sd *SimDispatcher) RunAircraftCommands(cmds *AircraftCommandsSpecifier, _ *struct{}) error {
	sim, ok := sd.sm.controllerTokenToSim[cmds.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}

	commands := strings.Fields(cmds.Commands)

	for i, command := range commands {
		wrapError := func(e error) error {
			return &AircraftCommandsError{
				error:     e,
				Remaining: commands[i:],
			}
		}

		switch command[0] {
		case 'D':
			if components := strings.Split(command, "/"); len(components) > 1 {
				// Depart <fix> at heading <hdg>
				fix := components[0][1:]

				if components[1][0] != 'H' {
					return wrapError(ErrInvalidCommandSyntax)
				}
				if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
					return wrapError(err)
				} else if err := sim.DepartFixHeading(&FixSpecifier{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Fix:             fix,
					Heading:         hdg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignAltitude(&AltitudeAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Altitude:        100 * alt,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if _, ok := sim.World.Locate(string(command[1:])); ok {
				if err := sim.DirectFix(&FixSpecifier{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Fix:             command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				return wrapError(ErrInvalidCommandSyntax)
			}

		case 'H':
			if len(command) == 1 {
				if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Present:         true,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				return wrapError(err)
			} else if err := sim.AssignHeading(&HeadingAssignment{
				ControllerToken: cmds.ControllerToken,
				Callsign:        cmds.Callsign,
				Heading:         hdg,
				Turn:            TurnClosest,
			}, nil); err != nil {
				return wrapError(err)
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					LeftDegrees:     deg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Heading:         hdg,
					Turn:            TurnLeft,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					RightDegrees:    deg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Heading:         hdg,
					Turn:            TurnRight,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'C', 'A':
			if len(command) > 4 && command[:3] == "CSI" && !isAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := sim.ClearedApproach(&ApproachClearance{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[3:],
					StraightIn:      true,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if command[0] == 'C' && len(command) > 2 && !isAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
					// Cross fix [at altitude] [at speed]
					fix := components[0][1:]
					alt, speed := 0, 0

					for _, cmd := range components[1:] {
						if len(cmd) == 0 {
							return wrapError(ErrInvalidCommandSyntax)
						}

						var err error
						if cmd[0] == 'A' {
							if alt, err = strconv.Atoi(cmd[1:]); err != nil {
								return wrapError(err)
							}
						} else if cmd[0] == 'S' {
							if speed, err = strconv.Atoi(cmd[1:]); err != nil {
								return wrapError(err)
							}
						} else {
							return wrapError(ErrInvalidCommandSyntax)
						}
					}

					if err := sim.CrossFixAt(&FixSpecifier{
						ControllerToken: cmds.ControllerToken,
						Callsign:        cmds.Callsign,
						Fix:             fix,
						Altitude:        100 * alt,
						Speed:           speed,
					}, nil); err != nil {
						return wrapError(err)
					}
				} else if err := sim.ClearedApproach(&ApproachClearance{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignAltitude(&AltitudeAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Altitude:        100 * alt,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := sim.AssignSpeed(&SpeedAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Speed:           0,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignSpeed(&SpeedAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Speed:           kts,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'E':
			// Expect approach.
			if len(command) > 1 {
				if err := sim.ExpectApproach(&ApproachAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				return wrapError(ErrInvalidCommandSyntax)
			}

		default:
			return wrapError(ErrInvalidCommandSyntax)
		}
	}
	return nil
}

type LaunchSpecifier struct {
	ControllerToken string
	Aircraft        Aircraft
}

func (sd *SimDispatcher) LaunchAircraft(ls *LaunchSpecifier, _ *struct{}) error {
	sim, ok := sd.sm.controllerTokenToSim[ls.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	sim.LaunchAircraft(ls.Aircraft)
	return nil
}

func RunSimServer() {
	l, err := net.Listen("tcp", ":8000")
	if err != nil {
		lg.Errorf("tcp listen: %v", err)
		return
	}

	// If we're just running the server, we don't care about the returned
	// configs...
	runServer(l, false)
}

func TryConnectRemoteServer(hostname string) (chan *SimServer, error) {
	client, err := rpc.DialHTTP("tcp", hostname)
	if err != nil {
		return nil, err
	}

	ch := make(chan *SimServer, 1)
	go func() {
		var configs map[string]*SimConfiguration
		if err := client.Call("SimManager.GetSimConfigurations", 0, &configs); err != nil {
			close(ch)
			lg.Errorf("%v", err)
		} else {
			ch <- &SimServer{
				name:    "Network (Multi-controller)",
				client:  client,
				configs: configs,
			}
		}
	}()

	return ch, nil
}

func LaunchLocalSimServer() (chan *SimServer, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}

	port := l.Addr().(*net.TCPAddr).Port

	configsChan := runServer(l, true)

	ch := make(chan *SimServer, 1)
	go func() {
		configs := <-configsChan

		client, err := rpc.DialHTTP("tcp", fmt.Sprintf("localhost:%d", port))
		if err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}

		ch <- &SimServer{
			name:    "Local (Single controller)",
			client:  client,
			configs: configs,
		}
	}()

	return ch, nil
}

func runServer(l net.Listener, isLocal bool) chan map[string]*SimConfiguration {
	ch := make(chan map[string]*SimConfiguration, 1)

	server := func() {
		var e ErrorLogger
		scenarioGroups, simConfigurations := LoadScenarioGroups(&e)
		if e.HaveErrors() {
			e.PrintErrors()
			os.Exit(1)
		}

		// Filter the scenarios and configs: for local, we only want ones
		// with solo_controller specified, and for the remote server, we
		// only want the ones with multi_controllers.

		sm := NewSimManager(scenarioGroups, simConfigurations)
		rpc.Register(sm)
		rpc.RegisterName("Sim", &SimDispatcher{sm: sm})
		rpc.HandleHTTP()

		ch <- simConfigurations

		lg.Printf("Listening on %+v", l)
		http.Serve(l, nil) // noreturn
	}

	if isLocal {
		go server()
	} else {
		server()
	}
	return ch
}

///////////////////////////////////////////////////////////////////////////

type Sim struct {
	Name string // mostly for multi-controller...

	mu sync.Mutex

	ScenarioGroup string
	Scenario      string

	World           *World
	controllers     map[string]*ServerController // from token
	SignOnPositions map[string]*Controller

	eventStream *EventStream

	LaunchController string

	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]int

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

	DepartureChallenge float32
	GoAroundRate       float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	CurrentTime    time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	SimRate        float32
	Paused         bool
}

type ServerController struct {
	Callsign string
	events   *EventsSubscription
}

func NewSim(ssc NewSimConfiguration, scenarioGroups map[string]*ScenarioGroup) *Sim {
	rand.Seed(time.Now().UnixNano())

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
		Name:          ssc.newSimName,
		ScenarioGroup: ssc.GroupName,
		Scenario:      ssc.ScenarioName,

		controllers: make(map[string]*ServerController),

		eventStream: NewEventStream(),

		DepartureRates:    DuplicateMap(ssc.Scenario.DepartureRates),
		ArrivalGroupRates: DuplicateMap(ssc.Scenario.ArrivalGroupRates),

		CurrentTime:    time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:            1,
		DepartureChallenge: ssc.Scenario.DepartureChallenge,
		GoAroundRate:       ssc.Scenario.GoAroundRate,
		Handoffs:           make(map[string]time.Time),
	}

	s.SignOnPositions = make(map[string]*Controller)
	add := func(callsign string) {
		if callsign[0] == '_' { // virtual position for handoff management
			return
		}
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
	w.DepartureRates = s.DepartureRates
	w.ArrivalGroupRates = s.ArrivalGroupRates
	w.GoAroundRate = s.GoAroundRate

	for _, callsign := range sc.VirtualControllers {
		if ctrl, ok := sg.ControlPositions[callsign]; ok {
			w.Controllers[callsign] = ctrl
		} else {
			lg.Errorf("%s: controller not found in ControlPositions??", callsign)
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
	for name, runwayRates := range s.DepartureRates {
		for _, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				if rate > 0 {
					w.DepartureAirports[name] = w.GetAirport(name)
				}
			}
		}
	}
	w.ArrivalAirports = make(map[string]*Airport)
	for _, airportRates := range s.ArrivalGroupRates {
		for name, rate := range airportRates {
			if rate > 0 {
				w.ArrivalAirports[name] = w.GetAirport(name)
			}
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

func (s *Sim) SignOn(callsign string) (*World, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.controllerIsSignedIn(callsign) {
		return nil, "", ErrControllerAlreadySignedIn
	}

	w := NewWorld()
	w.Assign(s.World)
	w.Callsign = callsign

	ctrl, ok := s.SignOnPositions[callsign]
	if !ok {
		return nil, "", ErrNoController
	}
	s.World.Controllers[callsign] = ctrl

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}

	token := base64.StdEncoding.EncodeToString(buf[:])
	s.controllers[token] = &ServerController{
		Callsign: callsign,
		events:   s.eventStream.Subscribe(),
	}

	return w, token, nil
}

func (s *Sim) SignOff(token string, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		ctrl.events.Unsubscribe()
		delete(s.controllers, token)
		delete(s.World.Controllers, ctrl.Callsign)
	}
	return nil
}

func (s *Sim) TogglePause(token string, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.Paused = !s.Paused
		s.lastUpdateTime = time.Now() // ignore time passage...
		return nil
	}
}

func (s *Sim) PostEvent(e Event) {
	s.eventStream.Post(e)
}

type SimWorldUpdate struct {
	Aircraft         map[string]*Aircraft
	Controllers      map[string]*Controller
	Time             time.Time
	LaunchController string
	SimIsPaused      bool
	SimRate          float32
	SimDescription   string
	Events           []Event
}

func (wu *SimWorldUpdate) UpdateWorld(w *World, eventStream *EventStream) {
	w.Aircraft = wu.Aircraft
	w.Controllers = wu.Controllers
	w.LaunchController = wu.LaunchController
	w.SimTime = wu.Time
	w.SimIsPaused = wu.SimIsPaused
	w.SimRate = wu.SimRate
	w.SimDescription = wu.SimDescription

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range wu.Events {
		eventStream.Post(e)
	}
}

func (s *Sim) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		*update = SimWorldUpdate{
			Aircraft:         s.World.Aircraft,
			Controllers:      s.World.Controllers,
			Time:             s.CurrentTime,
			LaunchController: s.LaunchController,
			SimIsPaused:      s.Paused,
			SimRate:          s.SimRate,
			SimDescription:   s.Scenario,
			Events:           ctrl.events.Get(),
		}
		return nil
	}
}

func (s *Sim) Activate() error {
	var e ErrorLogger

	s.controllers = make(map[string]*ServerController)
	s.eventStream = NewEventStream()

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

	// A number of time.Time values are included in the serialized World.
	// updateTime is a helper function that rewrites them to be in terms of
	// the current time, using the serializion time as a baseline.
	now := time.Now()
	serializeTime := s.CurrentTime
	updateTime := func(t time.Time) time.Time {
		return now.Add(t.Sub(serializeTime))
	}

	s.CurrentTime = now
	s.lastUpdateTime = now

	for _, ac := range s.World.Aircraft {
		e.Push(ac.Callsign)

		// Rewrite the radar track times to be w.r.t now
		for i := range ac.Tracks {
			ac.Tracks[i].Time = updateTime(ac.Tracks[i].Time)
		}

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

	for i, rwy := range s.World.DepartureRunways {
		s.World.DepartureRunways[i].lastDeparture = nil
		for _, route := range rwy.ExitRoutes {
			initializeWaypointLocations(route.Waypoints, &e)
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

	for ho, t := range s.Handoffs {
		s.Handoffs[ho] = updateTime(t)
	}

	for group, t := range s.NextArrivalSpawn {
		s.NextArrivalSpawn[group] = updateTime(t)
	}

	for airport, runwayTimes := range s.NextDepartureSpawn {
		for runway, t := range runwayTimes {
			s.NextDepartureSpawn[airport][runway] = updateTime(t)
		}
	}

	if e.HaveErrors() {
		e.PrintErrors()
		return errors.New("Errors during state restoration")
	}
	return nil

}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Paused {
		return
	}

	// Update the current time
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.SimRate * float32(elapsed))
	s.CurrentTime = s.CurrentTime.Add(elapsed)
	s.lastUpdateTime = time.Now()

	s.updateState()
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.CurrentTime
	for callsign, t := range s.Handoffs {
		if now.After(t) {
			if ac, ok := s.World.Aircraft[callsign]; ok {
				s.eventStream.Post(Event{
					Type:           AcceptedHandoffEvent,
					FromController: ac.TrackingController,
					ToController:   ac.OutboundHandoffController,
					Callsign:       ac.Callsign,
				})

				ac.TrackingController = ac.OutboundHandoffController
				ac.OutboundHandoffController = ""
			}
			delete(s.Handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now
		for _, ac := range s.World.Aircraft {
			ac.Update(s.World, s.World, s)

			// FIXME: this is sort of ugly to have here...
			if ac.InboundHandoffController == s.World.Callsign {
				// We hit a /ho at a fix; update to the correct controller.
				// Note that s.controllers may be empty when initially
				// running the sim after it has been launched. Just hand
				// off to the primary controller in that case...
				if len(s.World.MultiControllers) > 0 && len(s.controllers) > 0 {
					callsign := ""
					if ac.IsDeparture {
						for cs, ctrl := range s.World.MultiControllers {
							if ctrl.Departure {
								if s.controllerIsSignedIn(cs) {
									callsign = cs
								} else {
									callsign = s.World.PrimaryController
								}
								break
							}
						}
					} else {
						callsign = ac.ArrivalHandoffController
					}
					if callsign == "" {
						ac.InboundHandoffController = ""
					}

					i := 0
					for {
						if s.controllerIsSignedIn(callsign) {
							ac.InboundHandoffController = callsign
							break
						}
						callsign = s.World.MultiControllers[callsign].BackupController
						i++
						if i == 20 {
							lg.Errorf("%s: unable to find backup for arrival handoff controller",
								ac.ArrivalHandoffController)
							ac.InboundHandoffController = ""
							break
						}
					}

				} else {
					ac.InboundHandoffController = s.World.PrimaryController
				}

				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.ControllingController,
					ToController:   ac.InboundHandoffController,
				})
			}
		}
	}

	// Add a new radar track every 5 seconds.  While we're at it, cull
	// departures that are far from the airport.
	if now.Sub(s.lastTrackUpdate) >= 5*time.Second {
		s.lastTrackUpdate = now

		for callsign, ac := range s.World.Aircraft {
			if ap := s.World.GetAirport(ac.FlightPlan.DepartureAirport); ap != nil && ac.IsDeparture {
				if nmdistance2ll(ac.Position, ap.Location) > 200 {
					delete(s.World.Aircraft, callsign)
					continue
				}
			}

			ac.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - ac.MagneticVariation,
				Time:        now,
			})
		}
	}

	// Don't spawn automatically if someone is spawning manually.
	if s.LaunchController == "" {
		s.spawnAircraft()
	}
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
	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		s.CurrentTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.CurrentTime = time.Now()
	s.lastUpdateTime = time.Now()
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
	for group, rates := range s.ArrivalGroupRates {
		rateSum := 0
		for _, rate := range rates {
			rateSum += rate
		}
		s.NextArrivalSpawn[group] = randomSpawn(rateSum)
	}

	s.NextDepartureSpawn = make(map[string]map[string]time.Time)
	for airport, runwayRates := range s.DepartureRates {
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

func (s *Sim) spawnAircraft() {
	now := s.CurrentTime

	randomWait := func(rate int) time.Duration {
		if rate == 0 {
			return 365 * 24 * time.Hour
		}
		avgSeconds := 3600 / float32(rate)
		seconds := lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
		return time.Duration(seconds * float32(time.Second))
	}

	for group, airportRates := range s.ArrivalGroupRates {
		if now.After(s.NextArrivalSpawn[group]) {
			arrivalAirport, rateSum := sampleRateMap(airportRates)

			goAround := rand.Float32() < s.GoAroundRate
			if ac, err := s.World.CreateArrival(group, arrivalAirport, goAround); err != nil {
				lg.Errorf("%v", err)
			} else if ac != nil {
				s.launchAircraftNoLock(*ac)
				lg.Printf("%s: spawned arrival", ac.Callsign)
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
			category, rateSum := sampleRateMap(s.DepartureRates[airport][runway])
			if rateSum == 0 {
				lg.Errorf("%s/%s: couldn't find a matching runway for spawning departure?", airport, runway)
				continue
			}

			idx := FindIf(s.World.DepartureRunways,
				func(r ScenarioGroupDepartureRunway) bool {
					return r.Airport == airport && r.Runway == runway && r.Category == category
				})
			if idx == -1 {
				lg.Errorf("%s/%s/%s: couldn't find airport/runway/category for spawning departure. rates %s dep runways %s",
					airport, runway, category, spew.Sdump(s.DepartureRates[airport][runway]), spew.Sdump(s.World.DepartureRunways))
				continue
			}

			ac, err := s.World.CreateDeparture(airport, runway, category, s.DepartureChallenge)
			if err != nil {
				lg.Errorf("%v", err)
			} else {
				s.launchAircraftNoLock(*ac)
				lg.Printf("%s: starting takeoff roll", ac.Callsign)
				s.NextDepartureSpawn[airport][runway] = now.Add(randomWait(rateSum))
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Commands from the user

type SimRateSpecifier struct {
	ControllerToken string
	Rate            float32
}

func (s *Sim) SetSimRate(r *SimRateSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.controllers[r.ControllerToken]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.SimRate = r.Rate
		return nil
	}
}

func (s *Sim) TakeOrReturnLaunchControl(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if s.LaunchController != "" && ctrl.Callsign != s.LaunchController {
		return fmt.Errorf("Launches are already under the control of %s",
			s.LaunchController)
	} else if s.LaunchController == "" {
		s.LaunchController = ctrl.Callsign
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: ctrl.Callsign + " is now controlling aircraft launches",
		})
		return nil
	} else {
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: s.LaunchController + " is no longer controlling aircraft launches",
		})
		s.LaunchController = ""
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
		lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
		return
	}
	s.World.Aircraft[ac.Callsign] = &ac

	ac.MagneticVariation = s.World.MagneticVariation
	ac.NmPerLongitude = s.World.NmPerLongitude

	ac.RunWaypointCommands(ac.Waypoints[0], s.World, s)

	ac.Position = ac.Waypoints[0].Location
	if ac.Position.IsZero() {
		lg.Errorf("%s: uninitialized initial waypoint position! %+v", ac.Callsign, ac.Waypoints[0])
		return
	}

	ac.Heading = float32(ac.Waypoints[0].Heading)
	if ac.Heading == 0 { // unassigned, so get the heading from the next fix
		ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, ac.NmPerLongitude,
			ac.MagneticVariation)
	}
	ac.Waypoints = FilterSlice(ac.Waypoints[1:], func(wp Waypoint) bool { return !wp.Location.IsZero() })

	s.eventStream.Post(Event{
		Type: StatusMessageEvent,
		Message: fmt.Sprintf("%s launched %s departing %s",
			s.LaunchController, ac.Callsign, ac.FlightPlan.DepartureAirport),
	})

}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        string
}

type AircraftPropertiesSpecifier struct {
	ControllerToken string
	Callsign        string
	Scratchpad      string
}

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *Controller, ac *Aircraft) error,
	cmd func(*Controller, *Aircraft) (string, error)) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.World.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		ctrl := s.World.GetController(sc.Callsign)
		if ctrl == nil {
			lg.Errorf("couldn't get controller \"%s\". world controllers: %s",
				sc.Callsign, spew.Sdump(s.World.Controllers))
			panic("yolo")
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			response, err := cmd(ctrl, ac)
			if response != "" {
				lg.Printf("%s: %s", ac.Callsign, response)
				s.eventStream.Post(Event{
					Type:     RadioTransmissionEvent,
					Callsign: ac.Callsign,
					Message:  response,
				})
			}
			return err
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, error)) error {
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
	cmd func(*Controller, *Aircraft) (string, error)) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

func (s *Sim) SetScratchpad(a *AircraftPropertiesSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.Scratchpad = a.Scratchpad
			return "", nil
		})
}

func (s *Sim) InitiateTrack(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			s.eventStream.Post(Event{Type: InitiatedTrackEvent, Callsign: ac.Callsign})
			return "", nil
		})
}

func (s *Sim) DropTrack(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ""
			ac.ControllingController = ""
			s.eventStream.Post(Event{Type: DroppedTrackEvent, Callsign: ac.Callsign})
			return "", nil
		})
}

type HandoffSpecifier struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (s *Sim) HandoffTrack(h *HandoffSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(h.ControllerToken, h.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if octrl := s.World.GetController(h.Controller); octrl == nil {
				return "", ErrNoController
			} else {
				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					FromController: ctrl.Callsign,
					ToController:   octrl.Callsign,
					Callsign:       ac.Callsign,
				})

				ac.OutboundHandoffController = octrl.Callsign
				acceptDelay := 4 + rand.Intn(10)
				s.Handoffs[ac.Callsign] = s.CurrentTime.Add(time.Duration(acceptDelay) * time.Second)
				return "", nil
			}
		})
}

func (s *Sim) HandoffControl(h *HandoffSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(h.ControllerToken, h.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.ControllingController = ac.TrackingController

			// Go ahead and climb departures the rest of the way now.
			if ac.IsDeparture {
				lg.Errorf("%s: climbing to %d", ac.Callsign, ac.FlightPlan.Altitude)
				ac.Nav.V = &MaintainAltitude{Altitude: float32(ac.FlightPlan.Altitude)}
			}

			if octrl := s.World.GetController(ac.ControllingController); octrl != nil {
				if octrl.FullName != "" {
					return fmt.Sprintf("over to %s on %s, good day", octrl.FullName, octrl.Frequency), nil
				} else {
					return fmt.Sprintf("over to %s on %s, good day", octrl.Callsign, octrl.Frequency), nil
				}
			} else {
				return "goodbye", nil
			}
		})
}

func (s *Sim) AcceptHandoff(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.InboundHandoffController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.InboundHandoffController = ""
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			return "", nil
		})
}

func (s *Sim) CancelHandoff(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			delete(s.Handoffs, ac.Callsign)
			ac.OutboundHandoffController = ""
			return "", nil
		})
}

type AltitudeAssignment struct {
	ControllerToken string
	Callsign        string
	Altitude        int
}

func (s *Sim) AssignAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignAltitude(alt.Altitude) })
}

func (s *Sim) SetTemporaryAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchTrackingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TempAltitude = alt.Altitude
			return "", nil
		})
}

type HeadingAssignment struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingAssignment, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if hdg.Present {
				if _, err := ac.AssignHeading(int(ac.Heading), TurnClosest); err == nil {
					return "fly present heading", nil
				} else {
					return "", err
				}
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
		})
}

type SpeedAssignment struct {
	ControllerToken string
	Callsign        string
	Speed           int
}

func (s *Sim) AssignSpeed(sa *SpeedAssignment, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(sa.ControllerToken, sa.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignSpeed(sa.Speed) })
}

type FixSpecifier struct {
	ControllerToken string
	Callsign        string
	Fix             string
	Heading         int
	Altitude        int
	Speed           int
}

func (s *Sim) DirectFix(f *FixSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DirectFix(f.Fix) })
}

func (s *Sim) DepartFixHeading(f *FixSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DepartFixHeading(f.Fix, f.Heading) })
}

func (s *Sim) CrossFixAt(f *FixSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.CrossFixAt(f.Fix, f.Altitude, f.Speed) })
}

type ApproachAssignment struct {
	ControllerToken string
	Callsign        string
	Approach        string
}

func (s *Sim) ExpectApproach(a *ApproachAssignment, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			return ac.ExpectApproach(a.Approach, s.World)
		})
}

type ApproachClearance struct {
	ControllerToken string
	Callsign        string
	Approach        string
	StraightIn      bool
}

func (s *Sim) ClearedApproach(c *ApproachClearance, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(c.ControllerToken, c.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if c.StraightIn {
				return ac.ClearedStraightInApproach(c.Approach, s.World)
			} else {
				return ac.ClearedApproach(c.Approach, s.World)
			}
		})
}

func (s *Sim) GoAround(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchControllingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			return ac.GoAround(), nil
		})
}

func (s *Sim) DeleteAircraft(a *AircraftSpecifier, _ *struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if s.LaunchController != "" && s.LaunchController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			delete(s.World.Aircraft, ac.Callsign)
			return "", nil
		})
}

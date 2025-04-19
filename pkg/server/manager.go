// pkg/server/manager.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// SimManager

type SimManager struct {
	scenarioGroups     map[string]map[string]*ScenarioGroup
	configs            map[string]map[string]*Configuration
	activeSims         map[string]*ActiveSim
	controllersByToken map[string]*HumanController
	mu                 util.LoggingMutex
	mapManifests       map[string]*sim.VideoMapManifest
	startTime          time.Time
	lg                 *log.Logger
}

type Configuration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*av.Controller
	DefaultScenario  string
}

type HumanController struct {
	asim                *ActiveSim
	tcp                 string
	token               string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
}

type SimScenarioConfiguration struct {
	SelectedController  string
	SelectedSplit       string
	SplitConfigurations av.SplitConfigurationSet
	PrimaryAirport      string

	Wind         av.Wind
	LaunchConfig sim.LaunchConfig

	DepartureRunways []sim.DepartureRunway
	ArrivalRunways   []sim.ArrivalRunway
}

type ActiveSim struct {
	name            string
	scenarioGroup   string
	scenario        string
	sim             *sim.Sim
	allowInstructor bool
	password        string
	local           bool

	controllersByTCP map[string]*HumanController
}

func (as *ActiveSim) AddHumanController(tcp, token string) *HumanController {
	hc := &HumanController{
		asim:           as,
		tcp:            tcp,
		lastUpdateCall: time.Now(),
		token:          token,
	}
	as.controllersByTCP[tcp] = hc
	return hc
}

func NewSimManager(scenarioGroups map[string]map[string]*ScenarioGroup,
	simConfigurations map[string]map[string]*Configuration, manifests map[string]*sim.VideoMapManifest,
	lg *log.Logger) *SimManager {
	return &SimManager{
		scenarioGroups:     scenarioGroups,
		configs:            simConfigurations,
		activeSims:         make(map[string]*ActiveSim),
		controllersByToken: make(map[string]*HumanController),
		mapManifests:       manifests,
		startTime:          time.Now(),
		lg:                 lg,
	}
}

type NewSimResult struct {
	SimState        *sim.State
	ControllerToken string
}

func (sm *SimManager) New(config *NewSimConfiguration, result *NewSimResult) error {
	if config.NewSimType == NewSimCreateLocal || config.NewSimType == NewSimCreateRemote {
		lg := sm.lg.With(slog.String("sim_name", config.NewSimName))
		if nsc := sm.makeSimConfiguration(config, lg); nsc != nil {
			manifest := sm.mapManifests[nsc.STARSFacilityAdaptation.VideoMapFile]
			sim := sim.NewSim(*nsc, manifest, lg)
			as := &ActiveSim{
				name:             config.NewSimName,
				scenarioGroup:    config.GroupName,
				scenario:         config.ScenarioName,
				sim:              sim,
				allowInstructor:  config.InstructorAllowed,
				password:         config.Password,
				local:            config.NewSimType == NewSimCreateLocal,
				controllersByTCP: make(map[string]*HumanController),
			}
			return sm.Add(as, result, true)
		} else {
			return ErrInvalidSSimConfiguration
		}
	} else {
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)

		as, ok := sm.activeSims[config.SelectedRemoteSim]
		if !ok {
			return ErrNoNamedSim
		}

		if as.password != "" && config.RemoteSimPassword != as.password {
			return ErrInvalidPassword
		}

		ss, token, err := sm.signOn(as, config.SelectedRemoteSimPosition, config.Instructor)
		if err != nil {
			return err
		}

		hc := as.AddHumanController(config.SelectedRemoteSimPosition, token)
		sm.controllersByToken[token] = hc

		*result = NewSimResult{
			SimState:        ss,
			ControllerToken: token,
		}
		return nil
	}
}

func (sm *SimManager) makeSimConfiguration(config *NewSimConfiguration, lg *log.Logger) *sim.NewSimConfiguration {
	tracon, ok := sm.scenarioGroups[config.TRACONName]
	if !ok {
		lg.Errorf("%s: unknown TRACON", config.TRACONName)
		return nil
	}
	sg, ok := tracon[config.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", config.GroupName)
		return nil
	}
	sc, ok := sg.Scenarios[config.ScenarioName]
	if !ok {
		lg.Errorf("%s: unknown scenario", config.ScenarioName)
		return nil
	}

	description := util.Select(config.NewSimType == NewSimCreateLocal, " "+config.ScenarioName,
		"@"+config.NewSimName+": "+config.ScenarioName)

	nsc := sim.NewSimConfiguration{
		TFRs:                    config.TFRs,
		LiveWeather:             config.LiveWeather,
		TRACON:                  config.TRACONName,
		LaunchConfig:            config.Scenario.LaunchConfig,
		STARSFacilityAdaptation: deep.MustCopy(sg.STARSFacilityAdaptation),
		IsLocal:                 config.NewSimType == NewSimCreateLocal,
		DepartureRunways:        sc.DepartureRunways,
		ArrivalRunways:          sc.ArrivalRunways,
		ReportingPoints:         sg.ReportingPoints,
		Description:             description,
		MagneticVariation:       sg.MagneticVariation,
		NmPerLongitude:          sg.NmPerLongitude,
		Wind:                    sc.Wind,
		Airports:                sg.Airports,
		Fixes:                   sg.Fixes,
		PrimaryAirport:          sg.PrimaryAirport,
		Center:                  util.Select(sc.Center.IsZero(), sg.STARSFacilityAdaptation.Center, sc.Center),
		Range:                   util.Select(sc.Range == 0, sg.STARSFacilityAdaptation.Range, sc.Range),
		DefaultMaps:             sc.DefaultMaps,
		InboundFlows:            sg.InboundFlows,
		Airspace:                sg.Airspace,
		ControllerAirspace:      sc.Airspace,
		ControlPositions:        sg.ControlPositions,
		VirtualControllers:      sc.VirtualControllers,
		SignOnPositions:         make(map[string]*av.Controller),
	}

	if !nsc.IsLocal {
		selectedSplit := config.Scenario.SelectedSplit
		var err error
		nsc.PrimaryController, err = sc.SplitConfigurations.GetPrimaryController(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get primary controller: %v", err)
		}
		nsc.MultiControllers, err = sc.SplitConfigurations.GetConfiguration(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get multi controllers: %v", err)
		}
	} else {
		nsc.PrimaryController = sc.SoloController
	}

	add := func(callsign string) {
		if ctrl, ok := sg.ControlPositions[callsign]; !ok {
			lg.Errorf("%s: control position unknown??!", callsign)
		} else {
			nsc.SignOnPositions[callsign] = ctrl
		}
	}
	if !nsc.IsLocal {
		configs, err := sc.SplitConfigurations.GetConfiguration(config.Scenario.SelectedSplit)
		if err != nil {
			lg.Errorf("unable to get configurations for split: %v", err)
		}
		for callsign := range configs {
			add(callsign)
		}
	} else {
		add(sc.SoloController)
	}

	return &nsc
}

func (sm *SimManager) AddLocal(sim *sim.Sim, result *NewSimResult) error {
	as := &ActiveSim{ // no password, etc.
		sim:              sim,
		controllersByTCP: make(map[string]*HumanController),
		local:            true,
	}
	return sm.Add(as, result, false)
}

func (sm *SimManager) Add(as *ActiveSim, result *NewSimResult, prespawn bool) error {
	if as.sim.State == nil {
		return errors.New("incomplete Sim; nil *State")
	}

	lg := sm.lg
	if as.name != "" {
		lg = lg.With(slog.String("sim_name", as.name))
	}
	as.sim.Activate(lg)

	sm.mu.Lock(sm.lg)

	// Empty sim name is just a local sim, so no problem with replacing it...
	if _, ok := sm.activeSims[as.name]; ok && as.name != "" {
		sm.mu.Unlock(sm.lg)
		return ErrDuplicateSimName
	}

	sm.lg.Infof("%s: adding sim", as.name)
	sm.activeSims[as.name] = as

	instuctor := as.sim.Instructors[as.sim.State.PrimaryController]
	ss, token, err := sm.signOn(as, as.sim.State.PrimaryController, instuctor)
	if err != nil {
		return err
	}

	hc := as.AddHumanController(as.sim.State.PrimaryController, token)
	sm.controllersByToken[token] = hc

	sm.mu.Unlock(sm.lg)

	// Run prespawn after the primary controller is signed in.
	if prespawn {
		as.sim.Prespawn()
	}

	go func() {
		defer sm.lg.CatchAndReportCrash()

		// Terminate idle Sims after 4 hours, but not unnamed Sims, since
		// they're local and not running on the server.
		for !sm.SimShouldExit(as.sim) {
			if !as.local {
				// Sign off controllers we haven't heard from in 15 seconds so that
				// someone else can take their place. We only make this check for
				// multi-controller sims; we don't want to do this for local sims
				// so that we don't kick people off e.g. when their computer
				// sleeps.
				sm.mu.Lock(sm.lg) // FIXME: have a per-ActiveSim lock?
				for tcp, ctrl := range as.controllersByTCP {
					if time.Since(ctrl.lastUpdateCall) > 5*time.Second {
						if !ctrl.warnedNoUpdateCalls {
							ctrl.warnedNoUpdateCalls = true
							sm.lg.Warnf("%s: no messages for 5 seconds", tcp)
							as.sim.PostEvent(sim.Event{
								Type:    sim.StatusMessageEvent,
								Message: tcp + " has not been heard from for 5 seconds. Connection lost?",
							})
						}

						if time.Since(ctrl.lastUpdateCall) > 15*time.Second {
							sm.lg.Warnf("%s: signing off idle controller", tcp)
							if err := sm.signOff(ctrl.token); err != nil {
								sm.lg.Errorf("%s: error signing off idle controller: %v", tcp, err)
								if _, ok := sm.controllersByToken[token]; ok {
									delete(sm.controllersByToken[token].asim.controllersByTCP, ctrl.tcp)
									delete(sm.controllersByToken, token)
								}
							}
						}
					}
				}
				sm.mu.Unlock(sm.lg)
			}

			as.sim.Update()
			time.Sleep(100 * time.Millisecond)
		}

		sm.lg.Infof("%s: terminating sim after %s idle", as.name, as.sim.IdleTime())
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)
		delete(sm.activeSims, as.name)
		// FIXME: these don't get cleaned up during Sim SignOff()
		for _, ctrl := range as.controllersByTCP {
			delete(sm.controllersByToken, ctrl.token)
		}
	}()

	*result = NewSimResult{
		SimState:        ss,
		ControllerToken: token,
	}

	return nil
}

type ConnectResult struct {
	Configurations map[string]map[string]*Configuration
	RunningSims    map[string]*RemoteSim
}

func (sm *SimManager) Connect(version int, result *ConnectResult) error {
	if version != ViceRPCVersion {
		return ErrRPCVersionMismatch
	}

	// Before we acquire the lock...
	if err := sm.GetRunningSims(0, &result.RunningSims); err != nil {
		return err
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	result.Configurations = sm.configs

	return nil
}

// assume SimManager lock is held
func (sm *SimManager) signOn(as *ActiveSim, tcp string, instructor bool) (*sim.State, string, error) {
	ss, err := as.sim.SignOn(tcp, instructor)
	if err != nil {
		return nil, "", err
	}

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}
	token := base64.StdEncoding.EncodeToString(buf[:])

	return ss, token, nil
}

func (sm *SimManager) SignOff(token string) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.signOff(token)
}

func (sm *SimManager) signOff(token string) error {
	if ctrl, s, ok := sm.lookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else if err := s.SignOff(ctrl.tcp); err != nil {
		return err
	} else {
		delete(sm.controllersByToken[token].asim.controllersByTCP, ctrl.tcp)
		delete(sm.controllersByToken, token)

		return nil
	}
}

func (sm *SimManager) GetRunningSims(_ int, result *map[string]*RemoteSim) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	running := make(map[string]*RemoteSim)
	for name, as := range sm.activeSims {
		rs := &RemoteSim{
			GroupName:         as.scenarioGroup,
			ScenarioName:      as.scenario,
			PrimaryController: as.sim.State.PrimaryController,
			RequirePassword:   as.password != "",
			InstructorAllowed: as.allowInstructor,
		}

		rs.AvailablePositions, rs.CoveredPositions = as.sim.GetAvailableCoveredPositions()

		running[name] = rs
	}

	*result = running
	return nil
}

func (sm *SimManager) LookupController(token string) (*HumanController, *sim.Sim, bool) {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.lookupController(token)
}

func (sm *SimManager) lookupController(token string) (*HumanController, *sim.Sim, bool) {
	if ctrl, ok := sm.controllersByToken[token]; ok {
		return ctrl, ctrl.asim.sim, true
	}
	return nil, nil, false
}

const simIdleLimit = 4 * time.Hour

func (sm *SimManager) SimShouldExit(sim *sim.Sim) bool {
	if sim.IdleTime() < simIdleLimit {
		return false
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	nIdle := 0
	for _, as := range sm.activeSims {
		if as.sim.IdleTime() >= simIdleLimit {
			nIdle++
		}
	}
	return nIdle > 10
}

func (sm *SimManager) GetSerializeSim(token string, s *sim.Sim) error {
	if _, sim, ok := sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)

		*s = sim.GetSerializeSim()
	}
	return nil
}

type simStatus struct {
	Name               string
	Config             string
	IdleTime           time.Duration
	Controllers        string
	TotalIFR, TotalVFR int
}

func (sm *SimManager) GetWorldUpdate(token string, update *sim.WorldUpdate) error {
	sm.mu.Lock(sm.lg)

	if ctrl, ok := sm.controllersByToken[token]; !ok {
		sm.mu.Unlock(sm.lg)
		return ErrNoSimForControllerToken
	} else {
		s := ctrl.asim.sim
		ctrl.lastUpdateCall = time.Now()
		if ctrl.warnedNoUpdateCalls {
			ctrl.warnedNoUpdateCalls = false
			sm.lg.Warnf("%s: connection re-established", ctrl.tcp)
			s.PostEvent(sim.Event{
				Type:    sim.StatusMessageEvent,
				Message: ctrl.tcp + " is back online.",
			})
		}

		// Grab this before unlock.
		local := ctrl.asim.local

		sm.mu.Unlock(sm.lg)

		s.GetWorldUpdate(ctrl.tcp, update, local)

		return nil
	}
}

func (ss simStatus) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", ss.Name),
		slog.String("config", ss.Config),
		slog.Duration("idle", ss.IdleTime),
		slog.String("controllers", ss.Controllers),
		slog.Int("total_ifr", ss.TotalIFR),
		slog.Int("total_vfr", ss.TotalVFR))
}

func (sm *SimManager) getSimStatus() []simStatus {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var ss []simStatus
	for _, name := range util.SortedMapKeys(sm.activeSims) {
		as := sm.activeSims[name]
		status := simStatus{
			Name:     name,
			Config:   as.scenario,
			IdleTime: as.sim.IdleTime().Round(time.Second),
			TotalIFR: as.sim.State.TotalIFR,
			TotalVFR: as.sim.State.TotalVFR,
		}

		status.Controllers = strings.Join(as.sim.ActiveControllers(), ", ")

		ss = append(ss, status)
	}

	return ss
}

type SimBroadcastMessage struct {
	Password string
	Message  string
}

func (sm *SimManager) Broadcast(m *SimBroadcastMessage, _ *struct{}) error {
	pw, err := os.ReadFile("password")
	if err != nil {
		return err
	}

	password := strings.TrimRight(string(pw), "\n\r")
	if password != m.Password {
		return ErrInvalidPassword
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	sm.lg.Infof("Broadcasting message: %s", m.Message)

	for _, as := range sm.activeSims {
		as.sim.PostEvent(sim.Event{
			Type:    sim.ServerBroadcastMessageEvent,
			Message: m.Message,
		})
	}
	return nil
}

func BroadcastMessage(hostname, msg, password string, lg *log.Logger) {
	client, err := getClient(hostname, lg)
	if err != nil {
		lg.Errorf("unable to get client for broadcast: %v", err)
		return
	}

	err = client.CallWithTimeout("SimManager.Broadcast", &SimBroadcastMessage{
		Password: password,
		Message:  msg,
	}, nil)

	if err != nil {
		lg.Errorf("broadcast error: %v", err)
	}
}

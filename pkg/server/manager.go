// pkg/server/manager.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
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
	scenarioGroups       map[string]map[string]*ScenarioGroup
	configs              map[string]map[string]*Configuration
	activeSims           map[string]ActiveSim
	controllerTokenToSim map[string]*sim.Sim
	mu                   util.LoggingMutex
	mapManifests         map[string]*av.VideoMapManifest
	startTime            time.Time
	lg                   *log.Logger
}

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
}

func NewSimManager(scenarioGroups map[string]map[string]*ScenarioGroup,
	simConfigurations map[string]map[string]*Configuration, manifests map[string]*av.VideoMapManifest,
	lg *log.Logger) *SimManager {
	return &SimManager{
		scenarioGroups:       scenarioGroups,
		configs:              simConfigurations,
		activeSims:           make(map[string]ActiveSim),
		controllerTokenToSim: make(map[string]*sim.Sim),
		mapManifests:         manifests,
		startTime:            time.Now(),
		lg:                   lg,
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
			as := ActiveSim{
				name:            config.NewSimName,
				scenarioGroup:   config.GroupName,
				scenario:        config.ScenarioName,
				sim:             sim,
				password:        config.Password,
				allowInstructor: config.InstructorAllowed,
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
		if _, ok := as.sim.State.Controllers[config.SelectedRemoteSimPosition]; ok {
			return av.ErrNoController
		}

		if as.password != "" && config.RemoteSimPassword != as.password {
			return ErrInvalidPassword
		}

		ss, token, err := as.sim.SignOn(config.SelectedRemoteSimPosition, config.Instructor)
		if err != nil {
			return err
		}

		sm.controllerTokenToSim[token] = as.sim

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

	nsc := sim.NewSimConfiguration{
		TFRs:                    config.TFRs,
		LiveWeather:             config.LiveWeather,
		TRACON:                  config.TRACONName,
		LaunchConfig:            config.Scenario.LaunchConfig,
		STARSFacilityAdaptation: deep.MustCopy(sg.STARSFacilityAdaptation),
		IsLocal:                 config.NewSimType == NewSimCreateLocal,
		SimName:                 util.Select(config.NewSimType == NewSimCreateLocal, "", config.NewSimName),
		DepartureRunways:        sc.DepartureRunways,
		ArrivalRunways:          sc.ArrivalRunways,
		ReportingPoints:         sg.ReportingPoints,
		Description:             config.ScenarioName,
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
			ctrlCopy := *ctrl
			ctrlCopy.IsHuman = true
			nsc.SignOnPositions[callsign] = &ctrlCopy
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
	as := ActiveSim{sim: sim} // no password, etc.
	return sm.Add(as, result, false)
}

func (sm *SimManager) Add(as ActiveSim, result *NewSimResult, prespawn bool) error {
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

	sm.mu.Unlock(sm.lg)
	instuctor := as.sim.Instructors[as.sim.State.PrimaryController]
	ss, token, err := as.sim.SignOn(as.sim.State.PrimaryController, instuctor)
	if err != nil {
		return err
	}

	sm.mu.Lock(sm.lg)
	sm.controllerTokenToSim[token] = as.sim
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
			as.sim.Update()
			time.Sleep(100 * time.Millisecond)
		}

		sm.lg.Infof("%s: terminating sim after %s idle", as.name, as.sim.IdleTime())
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)
		delete(sm.activeSims, as.name)
		// FIXME: these don't get cleaned up during Sim SignOff()
		for tok, s := range sm.controllerTokenToSim {
			if s == as.sim {
				delete(sm.controllerTokenToSim, tok)
			}
		}
	}()

	*result = NewSimResult{
		SimState:        ss,
		ControllerToken: token,
	}

	return nil
}

type SignOnResult struct {
	Configurations map[string]map[string]*Configuration
	RunningSims    map[string]*RemoteSim
}

func (sm *SimManager) SignOn(version int, result *SignOnResult) error {
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
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

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

func (sm *SimManager) ControllerTokenToSim(token string) (*sim.Sim, bool) {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	sim, ok := sm.controllerTokenToSim[token]
	return sim, ok
}

type simStatus struct {
	Name             string
	Config           string
	IdleTime         time.Duration
	Controllers      string
	TotalDepartures  int
	TotalArrivals    int
	TotalOverflights int
}

func (ss simStatus) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", ss.Name),
		slog.String("config", ss.Config),
		slog.Duration("idle", ss.IdleTime),
		slog.String("controllers", ss.Controllers),
		slog.Int("departures", ss.TotalDepartures),
		slog.Int("arrivals", ss.TotalArrivals),
		slog.Int("overflights", ss.TotalOverflights))
}

func (sm *SimManager) getSimStatus() []simStatus {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var ss []simStatus
	for _, name := range util.SortedMapKeys(sm.activeSims) {
		as := sm.activeSims[name]
		status := simStatus{
			Name:             name,
			Config:           as.scenario,
			IdleTime:         as.sim.IdleTime().Round(time.Second),
			TotalDepartures:  as.sim.TotalDepartures,
			TotalArrivals:    as.sim.TotalArrivals,
			TotalOverflights: as.sim.TotalOverflights,
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

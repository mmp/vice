// server.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
)

const ViceRPCVersion = 5

type SimServer struct {
	name        string
	client      *RPCClient
	configs     map[string]*SimConfiguration
	runningSims map[string]*RemoteSim
	err         error
}

///////////////////////////////////////////////////////////////////////////

type SimProxy struct {
	ControllerToken string
	Client          *RPCClient
}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        string
}

func (s *SimProxy) TogglePause() *rpc.Call {
	return s.Client.Go("Sim.TogglePause", s.ControllerToken, nil, nil)
}

func (s *SimProxy) SignOff(_, _ *struct{}) error {
	return s.Client.CallWithTimeout("Sim.SignOff", s.ControllerToken, nil)
}

func (s *SimProxy) ChangeControlPosition(callsign string, keepTracks bool) error {
	return s.Client.CallWithTimeout("Sim.ChangeControlPosition",
		&ChangeControlPositionArgs{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
			KeepTracks:      keepTracks,
		}, nil)
}

func (s *SimProxy) GetSerializeSim() (*Sim, error) {
	var sim Sim
	err := s.Client.CallWithTimeout("SimManager.GetSerializeSim", s.ControllerToken, &sim)
	return &sim, err
}

func (s *SimProxy) GetWorldUpdate(wu *SimWorldUpdate) *rpc.Call {
	return s.Client.Go("Sim.GetWorldUpdate", s.ControllerToken, wu, nil)
}

func (s *SimProxy) SetSimRate(r float32) *rpc.Call {
	return s.Client.Go("Sim.SetSimRate",
		&SetSimRateArgs{
			ControllerToken: s.ControllerToken,
			Rate:            r,
		}, nil, nil)
}

func (s *SimProxy) SetLaunchConfig(lc LaunchConfig) *rpc.Call {
	return s.Client.Go("Sim.SetLaunchConfig",
		&SetLaunchConfigArgs{
			ControllerToken: s.ControllerToken,
			Config:          lc,
		}, nil, nil)
}

func (s *SimProxy) TakeOrReturnLaunchControl() *rpc.Call {
	return s.Client.Go("Sim.TakeOrReturnLaunchControl", s.ControllerToken, nil, nil)
}

func (s *SimProxy) SetScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetScratchpad", &SetScratchpadArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *SimProxy) InitiateTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", &InitiateTrackArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) DropTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DropTrack", &DropTrackArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.HandoffTrack", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *SimProxy) HandoffControl(callsign string) *rpc.Call {
	return s.Client.Go("Sim.HandoffControl", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) AcceptHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptHandoff", &AcceptHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) RejectHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.RejectHandoff", &RejectHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) CancelHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.CancelHandoff", &CancelHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) PointOut(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.PointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *SimProxy) AssignAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetAltitude", &AssignAltitudeArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) SetTemporaryAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetTemporaryAltitude", &AssignAltitudeArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) DeleteAircraft(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DeleteAircraft", &DeleteAircraftArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

type AircraftCommandsArgs struct {
	ControllerToken string
	Callsign        string
	Commands        string
}

func (s *SimProxy) RunAircraftCommands(callsign string, cmds string) *rpc.Call {
	return s.Client.Go("Sim.RunAircraftCommands", &AircraftCommandsArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, nil, nil)
}

func (s *SimProxy) LaunchAircraft(ac Aircraft) *rpc.Call {
	return s.Client.Go("Sim.LaunchAircraft", &LaunchAircraftArgs{
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
	mu                   sync.Mutex
	startTime            time.Time
}

func NewSimManager(scenarioGroups map[string]*ScenarioGroup,
	simConfigurations map[string]*SimConfiguration) *SimManager {
	return &SimManager{
		scenarioGroups:       scenarioGroups,
		configs:              simConfigurations,
		activeSims:           make(map[string]*Sim),
		controllerTokenToSim: make(map[string]*Sim),
		startTime:            time.Now(),
	}
}

type NewSimResult struct {
	World           *World
	ControllerToken string
}

func (sm *SimManager) New(config *NewSimConfiguration, result *NewSimResult) error {
	if config.NewSimType == NewSimCreateLocal || config.NewSimType == NewSimCreateRemote {
		sim := NewSim(*config, sm.scenarioGroups, config.NewSimType == NewSimCreateLocal)
		sim.prespawn()
		return sm.Add(sim, result)
	} else {
		sm.mu.Lock()
		defer sm.mu.Unlock()

		sim, ok := sm.activeSims[config.SelectedRemoteSim]
		if !ok {
			return ErrNoNamedSim
		}
		if _, ok := sim.World.Controllers[config.SelectedRemoteSimPosition]; ok {
			return ErrNoController
		}

		world, token, err := sim.SignOn(config.SelectedRemoteSimPosition)
		if err != nil {
			return err
		}

		sm.controllerTokenToSim[token] = sim

		*result = NewSimResult{
			World:           world,
			ControllerToken: token,
		}
		return nil
	}
}

func (sm *SimManager) Add(sim *Sim, result *NewSimResult) error {
	if err := sim.Activate(); err != nil {
		lg.Printf("%s: activate fail: %v", sim.Name, err)
		return err
	}

	sm.mu.Lock()

	// Empty sim name is just a local sim, so no problem with replacing it...
	if _, ok := sm.activeSims[sim.Name]; ok && sim.Name != "" {
		sm.mu.Unlock()
		return ErrDuplicateSimName
	}

	lg.Printf("%s: starting new sim", sim.Name)
	sm.activeSims[sim.Name] = sim

	sm.mu.Unlock()

	world, token, err := sim.SignOn(sim.World.PrimaryController)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	sm.controllerTokenToSim[token] = sim
	sm.mu.Unlock()

	go func() {
		// Terminate idle Sims after 4 hours, but not unnamed Sims, since
		// they're local and not running on the server.
		for !sm.SimShouldExit(sim) {
			sim.Update()
			time.Sleep(100 * time.Millisecond)
		}

		lg.Printf("%s: terminating sim after %s idle", sim.Name, sim.IdleTime())
		sm.mu.Lock()
		delete(sm.activeSims, sim.Name)
		// FIXME: these don't get cleaned up during Sim SignOff()
		for tok, s := range sm.controllerTokenToSim {
			if s == sim {
				delete(sm.controllerTokenToSim, tok)
			}
		}
		sm.mu.Unlock()
	}()

	*result = NewSimResult{
		World:           world,
		ControllerToken: token,
	}

	return nil
}

type SignOnResult struct {
	Configurations map[string]*SimConfiguration
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

	sm.mu.Lock()
	defer sm.mu.Unlock()

	result.Configurations = sm.configs

	return nil
}

func (sm *SimManager) GetRunningSims(_ int, result *map[string]*RemoteSim) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	running := make(map[string]*RemoteSim)
	for name, s := range sm.activeSims {
		s.mu.Lock()
		rs := &RemoteSim{
			GroupName:          s.ScenarioGroup,
			ScenarioName:       s.Scenario,
			PrimaryController:  s.World.PrimaryController,
			AvailablePositions: make(map[string]struct{}),
			CoveredPositions:   make(map[string]struct{}),
		}

		// Figure out which positions are available; start with all of the possible ones,
		// then delete those that are active
		rs.AvailablePositions[s.World.PrimaryController] = struct{}{}
		for callsign := range s.World.MultiControllers {
			rs.AvailablePositions[callsign] = struct{}{}
		}
		for _, ctrl := range s.controllers {
			delete(rs.AvailablePositions, ctrl.Callsign)
			if wc, ok := s.World.Controllers[ctrl.Callsign]; ok && wc.IsHuman {
				rs.CoveredPositions[ctrl.Callsign] = struct{}{}
			}
		}
		s.mu.Unlock()

		running[name] = rs
	}

	*result = running
	return nil
}

const simIdleLimit = 4 * time.Hour

func (sm *SimManager) SimShouldExit(sim *Sim) bool {
	if sim.IdleTime() < simIdleLimit {
		return false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	nIdle := 0
	for _, sim := range sm.activeSims {
		if sim.IdleTime() >= simIdleLimit {
			nIdle++
		}
	}
	return nIdle > 10
}

func (sm *SimManager) GetSerializeSim(token string, s *Sim) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

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

func (sm *SimManager) ControllerTokenToSim(token string) (*Sim, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sim, ok := sm.controllerTokenToSim[token]
	return sim, ok
}

type SimStatus struct {
	Name            string
	Config          string
	IdleTime        time.Duration
	Controllers     string
	TotalDepartures int
	TotalArrivals   int
}

func (sm *SimManager) GetSimStatus() []SimStatus {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var ss []SimStatus
	for _, name := range SortedMapKeys(sm.activeSims) {
		sim := sm.activeSims[name]
		status := SimStatus{
			Name:            name,
			Config:          sim.Scenario,
			IdleTime:        sim.IdleTime().Round(time.Second),
			TotalDepartures: sim.TotalDepartures,
			TotalArrivals:   sim.TotalArrivals,
		}

		var controllers []string
		for _, ctrl := range sim.controllers {
			controllers = append(controllers, ctrl.Callsign)
		}
		sort.Strings(controllers)
		status.Controllers = strings.Join(controllers, ", ")

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

	sm.mu.Lock()
	defer sm.mu.Unlock()

	lg.Printf("Broadcasting message: %s", m.Message)

	for _, sim := range sm.activeSims {
		sim.mu.Lock()

		sim.eventStream.Post(Event{
			Type:    ServerBroadcastMessageEvent,
			Message: m.Message,
		})

		sim.mu.Unlock()
	}
	return nil
}

func BroadcastMessage(hostname, msg, password string) {
	client, err := getClient(hostname)
	if err != nil {
		lg.Errorf("%v", err)
		return
	}

	err = client.CallWithTimeout("SimManager.Broadcast", &SimBroadcastMessage{
		Password: password,
		Message:  msg,
	}, nil)

	if err != nil {
		lg.Errorf("%v", err)
	}
}

///////////////////////////////////////////////////////////////////////////
// SimDispatcher

type SimDispatcher struct {
	sm *SimManager
}

func (sd *SimDispatcher) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.GetWorldUpdate(token, update)
	}
}

func (sd *SimDispatcher) SignOff(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SignOff(token)
	}
}

type ChangeControlPositionArgs struct {
	ControllerToken string
	Callsign        string
	KeepTracks      bool
}

func (sd *SimDispatcher) ChangeControlPosition(cs *ChangeControlPositionArgs, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(cs.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ChangeControlPosition(cs.ControllerToken, cs.Callsign, cs.KeepTracks)
	}
}

func (sd *SimDispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TakeOrReturnLaunchControl(token)
	}
}

type SetSimRateArgs struct {
	ControllerToken string
	Rate            float32
}

func (sd *SimDispatcher) SetSimRate(r *SetSimRateArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[r.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetSimRate(r.ControllerToken, r.Rate)
	}
}

type SetLaunchConfigArgs struct {
	ControllerToken string
	Config          LaunchConfig
}

func (sd *SimDispatcher) SetLaunchConfig(lc *SetLaunchConfigArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[lc.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetLaunchConfig(lc.ControllerToken, lc.Config)
	}
}

func (sd *SimDispatcher) TogglePause(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TogglePause(token)
	}
}

type SetScratchpadArgs struct {
	ControllerToken string
	Callsign        string
	Scratchpad      string
}

func (sd *SimDispatcher) SetScratchpad(a *SetScratchpadArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetScratchpad(a.ControllerToken, a.Callsign, a.Scratchpad)
	}
}

type InitiateTrackArgs AircraftSpecifier

func (sd *SimDispatcher) InitiateTrack(it *InitiateTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[it.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.InitiateTrack(it.ControllerToken, it.Callsign)
	}
}

type DropTrackArgs AircraftSpecifier

func (sd *SimDispatcher) DropTrack(dt *DropTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[dt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DropTrack(dt.ControllerToken, dt.Callsign)
	}
}

type HandoffArgs struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (sd *SimDispatcher) HandoffTrack(h *HandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.HandoffTrack(h.ControllerToken, h.Callsign, h.Controller)
	}
}

func (sd *SimDispatcher) HandoffControl(h *HandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.HandoffControl(h.ControllerToken, h.Callsign)
	}
}

type AcceptHandoffArgs AircraftSpecifier

func (sd *SimDispatcher) AcceptHandoff(ah *AcceptHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ah.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AcceptHandoff(ah.ControllerToken, ah.Callsign)
	}
}

type RejectHandoffArgs AircraftSpecifier

func (sd *SimDispatcher) RejectHandoff(rh *RejectHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[rh.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.RejectHandoff(rh.ControllerToken, rh.Callsign)
	}
}

type CancelHandoffArgs AircraftSpecifier

func (sd *SimDispatcher) CancelHandoff(ch *CancelHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ch.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.CancelHandoff(ch.ControllerToken, ch.Callsign)
	}
}

type PointOutArgs struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (sd *SimDispatcher) PointOut(po *PointOutArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.PointOut(po.ControllerToken, po.Callsign, po.Controller)
	}
}

type AssignAltitudeArgs struct {
	ControllerToken string
	Callsign        string
	Altitude        int
}

func (sd *SimDispatcher) AssignAltitude(alt *AssignAltitudeArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[alt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AssignAltitude(alt.ControllerToken, alt.Callsign, alt.Altitude)
	}
}

func (sd *SimDispatcher) SetTemporaryAltitude(alt *AssignAltitudeArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[alt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetTemporaryAltitude(alt.ControllerToken, alt.Callsign, alt.Altitude)
	}
}

type DeleteAircraftArgs AircraftSpecifier

func (sd *SimDispatcher) DeleteAircraft(da *DeleteAircraftArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[da.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DeleteAircraft(da.ControllerToken, da.Callsign)
	}
}

func (sd *SimDispatcher) RunAircraftCommands(cmds *AircraftCommandsArgs, _ *struct{}) error {
	token, callsign := cmds.ControllerToken, cmds.Callsign
	sim, ok := sd.sm.controllerTokenToSim[token]
	if !ok {
		return ErrNoSimForControllerToken
	}

	commands := strings.Fields(cmds.Commands)

	for i, command := range commands {
		switch command[0] {
		case 'A', 'C':
			if command == "CAC" {
				// Cancel approach clearance
				if err := sim.CancelApproachClearance(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if command == "CVS" {
				if err := sim.ClimbViaSID(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if len(command) > 4 && command[:3] == "CSI" && !isAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := sim.ClearedApproach(token, callsign, command[3:], true); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if command[0] == 'C' && len(command) > 2 && !isAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
					// Cross fix [at altitude] [at speed]
					fix := components[0][1:]
					var ar *AltitudeRestriction
					speed := 0

					for _, cmd := range components[1:] {
						if len(cmd) == 0 {
							sim.SetSTARSInput(strings.Join(commands[i:], " "))
							return ErrInvalidCommandSyntax
						}

						var err error
						if cmd[0] == 'A' && len(cmd) > 1 {
							if ar, err = ParseAltitudeRestriction(cmd[1:]); err != nil {
								sim.SetSTARSInput(strings.Join(commands[i:], " "))
								return ErrInvalidCommandSyntax
							}
							// User input here is 100s of feet, while AltitudeRestriction is feet...
							ar.Range[0] *= 100
							ar.Range[1] *= 100
						} else if cmd[0] == 'S' {
							if speed, err = strconv.Atoi(cmd[1:]); err != nil {
								sim.SetSTARSInput(strings.Join(commands[i:], " "))
								return err
							}
						} else {
							sim.SetSTARSInput(strings.Join(commands[i:], " "))
							return ErrInvalidCommandSyntax
						}
					}

					if err := sim.CrossFixAt(token, callsign, fix, ar, speed); err != nil {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return err
					}
				} else if err := sim.ClearedApproach(token, callsign, command[1:], false); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				if command[0] == 'A' {
					components := strings.Split(command, "/")
					if len(components) != 2 || len(components[1]) == 0 || components[1][0] != 'C' {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return ErrInvalidCommandSyntax
					}

					fix := strings.ToUpper(components[0][1:])
					approach := components[1][1:]
					if err := sim.AtFixCleared(token, callsign, fix, approach); err != nil {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return err
					}
				}

				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignAltitude(token, callsign, 100*alt); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			}

		case 'D':
			if command == "DVS" {
				if err := sim.DescendViaSTAR(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
				fix := components[0][1:]

				switch components[1][0] {
				case 'D':
					// Depart <fix1> direct <fix2>
					if err := sim.DepartFixDirect(token, callsign, fix, components[1][1:]); err != nil {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return err
					}
				case 'H':
					// Depart <fix> at heading <hdg>
					if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return err
					} else if err := sim.DepartFixHeading(token, callsign, fix, hdg); err != nil {
						sim.SetSTARSInput(strings.Join(commands[i:], " "))
						return err
					}

				default:
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return ErrInvalidCommandSyntax
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignAltitude(token, callsign, 100*alt); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if _, ok := sim.World.Locate(string(command[1:])); ok {
				if err := sim.DirectFix(token, callsign, command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				sim.SetSTARSInput(strings.Join(commands[i:], " "))
				return ErrInvalidCommandSyntax
			}

		case 'E':
			if command == "ED" {
				if err := sim.ExpediteDescent(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if command == "EC" {
				if err := sim.ExpediteClimb(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if len(command) > 1 {
				// Expect approach.
				if err := sim.ExpectApproach(token, callsign, command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				sim.SetSTARSInput(strings.Join(commands[i:], " "))
				return ErrInvalidCommandSyntax
			}

		case 'H':
			if len(command) == 1 {
				if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Present:         true,
				}); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				sim.SetSTARSInput(strings.Join(commands[i:], " "))
				return err
			} else if err := sim.AssignHeading(&HeadingArgs{
				ControllerToken: token,
				Callsign:        callsign,
				Heading:         hdg,
				Turn:            TurnClosest,
			}); err != nil {
				sim.SetSTARSInput(strings.Join(commands[i:], " "))
				return err
			}

		case 'I':
			if len(command) == 1 {
				if err := sim.InterceptLocalizer(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				sim.SetSTARSInput(strings.Join(commands[i:], " "))
				return ErrInvalidCommandSyntax
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					LeftDegrees:     deg,
				}); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Heading:         hdg,
					Turn:            TurnLeft,
				}); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					RightDegrees:    deg,
				}); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Heading:         hdg,
					Turn:            TurnRight,
				}); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := sim.AssignSpeed(token, callsign, 0); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if command == "SMIN" {
				if err := sim.MaintainSlowestPractical(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else if command == "SMAX" {
				if err := sim.MaintainMaximumForward(token, callsign); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				} else if err := sim.AssignSpeed(token, callsign, kts); err != nil {
					sim.SetSTARSInput(strings.Join(commands[i:], " "))
					return err
				}
			}

		default:
			sim.SetSTARSInput(strings.Join(commands[i:], " "))
			return ErrInvalidCommandSyntax
		}
	}
	return nil
}

type LaunchAircraftArgs struct {
	ControllerToken string
	Aircraft        Aircraft
}

func (sd *SimDispatcher) LaunchAircraft(ls *LaunchAircraftArgs, _ *struct{}) error {
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

func getClient(hostname string) (*RPCClient, error) {
	conn, err := net.Dial("tcp", hostname)
	if err != nil {
		return nil, err
	}

	cc, err := MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := MakeGOBClientCodec(cc)
	if *logRPC {
		codec = MakeLoggingClientCodec(hostname, codec)
	}
	return &RPCClient{rpc.NewClientWithCodec(codec)}, nil
}

func TryConnectRemoteServer(hostname string) (chan *SimServer, error) {
	client, err := getClient(hostname)
	if err != nil {
		return nil, err
	}

	ch := make(chan *SimServer, 1)
	go func() {
		var so SignOnResult
		start := time.Now()
		if err := client.CallWithTimeout("SimManager.SignOn", ViceRPCVersion, &so); err != nil {
			ch <- &SimServer{
				err: err,
			}
		} else {
			lg.Printf("%s: server returned configuration in %s", hostname, time.Since(start))
			ch <- &SimServer{
				name:        "Network (Multi-controller)",
				client:      client,
				configs:     so.Configurations,
				runningSims: so.RunningSims,
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

		client, err := getClient(fmt.Sprintf("localhost:%d", port))
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

		server := rpc.NewServer()

		sm := NewSimManager(scenarioGroups, simConfigurations)
		if err := server.Register(sm); err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}
		if err := server.RegisterName("Sim", &SimDispatcher{sm: sm}); err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}

		go launchHTTPStats(sm)

		ch <- simConfigurations

		lg.Printf("Listening on %+v", l)

		for {
			conn, err := l.Accept()
			lg.Printf("%s: new connection", conn.RemoteAddr())
			if err != nil {
				lg.Errorf("Accept error: %v", err)
			} else if cc, err := MakeCompressedConn(MakeLoggingConn(conn)); err != nil {
				lg.Errorf("MakeCompressedConn: %v", err)
			} else {
				codec := MakeGOBServerCodec(cc)
				if *logRPC {
					codec = MakeLoggingServerCodec(conn.RemoteAddr().String(), codec)
				}
				go server.ServeCodec(codec)
			}
		}
	}

	if isLocal {
		go server()
	} else {
		server()
	}
	return ch
}

///////////////////////////////////////////////////////////////////////////
// Status / statistics via HTTP...

var launchTime time.Time

func launchHTTPStats(sm *SimManager) {
	launchTime = time.Now()
	http.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		statsHandler(w, r, sm)
	})
	http.HandleFunc("/vice-logs/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if f, err := os.Open("." + r.URL.String()); err == nil {
			if _, err := io.Copy(w, f); err != nil {
				lg.Errorf("%s: %v", r.URL.String(), err)
			}
		}
	})

	if err := http.ListenAndServe(":6502", nil); err != nil {
		lg.Printf("Failed to start HTTP server for stats: %v\n", err)
	}
}

type ServerStats struct {
	Uptime           time.Duration
	AllocMemory      uint64
	TotalAllocMemory uint64
	SysMemory        uint64
	RX, TX           int64
	NumGC            uint32
	NumGoRoutines    int
	CPUUsage         int

	SimStatus []SimStatus
	Errors    string
	LogFiles  []ServerLogFile
}

type ServerLogFile struct {
	Filename string
	Date     string
	Size     int64
}

func formatBytes(v int64) string {
	if v < 1024 {
		return fmt.Sprintf("%d B", v)
	} else if v < 1024*1024 {
		return fmt.Sprintf("%d KiB", v/1024)
	} else if v < 1024*1024*1024 {
		return fmt.Sprintf("%d MiB", v/1024/1024)
	} else {
		return fmt.Sprintf("%d GiB", v/1024/1024/1024)
	}
}

var templateFuncs = template.FuncMap{"bytes": formatBytes}

var statsTemplate = template.Must(template.New("").Funcs(templateFuncs).Parse(`
<!DOCTYPE html>
<html>
<head>
<title>vice vice baby</title>
</head>
<style>
table {
  border-collapse: collapse;
  width: 100%;
}

th, td {
  border: 1px solid #dddddd;
  padding: 8px;
  text-align: left;
}

tr:nth-child(even) {
  background-color: #f2f2f2;
}

#log {
    font-family: "Courier New", monospace;  /* use a monospace font */
    width: 100%;
    height: 500px;
    font-size: 12px;
    overflow: auto;  /* add scrollbars as necessary */
    white-space: pre-wrap;  /* wrap text */
    border: 1px solid #ccc;
    padding: 10px;
}
</style>
<body>
<h1>Server Status</h1>
<ul>
  <li>Uptime: {{.Uptime}}</li>
  <li>CPU usage: {{.CPUUsage}}%</li>
  <li>Bandwidth: {{bytes .RX}} RX, {{bytes .TX}} TX</li>
  <li>Allocated memory: {{.AllocMemory}} MB</li>
  <li>Total allocated memory: {{.TotalAllocMemory}} MB</li>
  <li>System memory: {{.SysMemory}} MB</li>
  <li>Garbage collection passes: {{.NumGC}}</li>
  <li>Running goroutines: {{.NumGoRoutines}}</li>
</ul>

<h1>Sim Status</h1>
<table>
  <tr>
  <th>Name</th>
  <th>Scenario</th>
  <th>Dep</th>
  <th>Arr</th>
  <th>Idle Time</th>
  <th>Active Controllers</th>

{{range .SimStatus}}
  </tr>
  <td>{{.Name}}</td>
  <td>{{.Config}}</td>
  <td>{{.TotalDepartures}}</td>
  <td>{{.TotalArrivals}}</td>
  <td>{{.IdleTime}}</td>
  <td><tt>{{.Controllers}}</tt></td>
</tr>
{{end}}
</table>

<h1>Errors</h1>
<div id="log" class="bot">
{{.Errors}}
</div>

<h1>Logs</h1>
<ul>
{{range .LogFiles}}
<li><a href="/vice-logs/{{.Filename}}">{{.Filename}}</a> - {{.Date}} - ({{bytes .Size}})</li>
{{end}}
</ul>

<script>
window.onload = function() {
    var divs = document.getElementsByClassName("bot");
    for (var i = 0; i < divs.length; i++) {
        divs[i].scrollTop = divs[i].scrollHeight - divs[i].clientHeight;
    }
}
</script>

</body>
</html>
`))

func statsHandler(w http.ResponseWriter, r *http.Request, sm *SimManager) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	usage, _ := cpu.Percent(time.Second, false)
	stats := ServerStats{
		Uptime:           time.Since(launchTime).Round(time.Second),
		AllocMemory:      m.Alloc / (1024 * 1024),
		TotalAllocMemory: m.TotalAlloc / (1024 * 1024),
		SysMemory:        m.Sys / (1024 * 1024),
		NumGC:            m.NumGC,
		NumGoRoutines:    runtime.NumGoroutine(),
		CPUUsage:         int(math.Round(usage[0])),

		SimStatus: sm.GetSimStatus(),
	}
	if errs, err := os.ReadFile("vice-logs/errors"); err == nil {
		stats.Errors = string(errs)
	}
	stats.RX, stats.TX = GetLoggedRPCBandwidth()

	/*
		if de, err := os.ReadDir("vice-logs"); err == nil {
			for _, entry := range de {
				if info, err := entry.Info(); err == nil {
					stats.LogFiles = append(stats.LogFiles,
						ServerLogFile{
							Filename: entry.Name(),
							Date:     info.ModTime().Format(time.RFC1123),
							Size:     info.Size(),
						})
				}
			}
		}
		sort.Slice(stats.LogFiles, func(i, j int) bool {
			return stats.LogFiles[i].Filename < stats.LogFiles[j].Filename
		})
	*/

	statsTemplate.Execute(w, stats)
}

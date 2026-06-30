// server/session.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Types and Constructors

type simSession struct {
	name               string
	scenarioGroup      string
	scenario           string
	sim                *sim.Sim
	password           string
	connectionsByToken map[string]*connectionState

	lg *log.Logger
	mu util.LoggingMutex
}

func makeSimSession(name, scenarioGroup, scenario, password string, s *sim.Sim, lg *log.Logger) *simSession {
	if name != "" {
		lg = lg.With(slog.String("sim_name", name))
	}
	return &simSession{
		name:               name,
		scenarioGroup:      scenarioGroup,
		scenario:           scenario,
		sim:                s,
		password:           password,
		lg:                 lg,
		connectionsByToken: make(map[string]*connectionState),
	}
}

func makeLocalSimSession(s *sim.Sim, lg *log.Logger) *simSession {
	return makeSimSession("", "", "", "", s, lg)
}

// connectionState holds state for a single human's connection to a sim at a TCW.
type connectionState struct {
	token               string
	tcw                 sim.TCW
	initials            string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
	stateUpdateEventSub *sim.EventsSubscription

	// lastSentGen is the most recent publication generation that has been
	// delivered to this client via GetStateUpdate. The long-poll waits for
	// the sim's pubGen to advance past this value.
	lastSentGen uint64
}

///////////////////////////////////////////////////////////////////////////
// Controller Lifecycle

func (ss *simSession) AddHumanController(token string, tcw sim.TCW, initials string,
	sub *sim.EventsSubscription) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	ss.connectionsByToken[token] = &connectionState{
		token:               token,
		tcw:                 tcw,
		initials:            initials,
		lastUpdateCall:      time.Now(),
		stateUpdateEventSub: sub,
	}

	// Update pause state - may unpause sim now that a human is connected
	ss.updateSimPauseState()
}

type signOffResult struct {
	TCW        sim.TCW
	Initials   string
	UsersAtTCW int
}

func (ss *simSession) SignOff(token string) (signOffResult, bool) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	conn, ok := ss.connectionsByToken[token]
	if !ok {
		return signOffResult{}, false
	}

	result := signOffResult{
		TCW:      conn.tcw,
		Initials: conn.initials,
	}

	// Unsubscribe from events before deleting
	if conn.stateUpdateEventSub != nil {
		conn.stateUpdateEventSub.Unsubscribe()
	}

	delete(ss.connectionsByToken, token)

	// Count remaining users at this TCW
	for _, c := range ss.connectionsByToken {
		if c.tcw == result.TCW {
			result.UsersAtTCW++
		}
	}

	// Update pause state - may pause sim if no humans remain
	ss.updateSimPauseState()

	return result, true
}

func (ss *simSession) CullIdleControllers(sm *SimManager) {
	ss.mu.Lock(ss.lg)

	var tokensToSignOff []string
	for token, conn := range ss.connectionsByToken {
		if time.Since(conn.lastUpdateCall) > StateUpdateWarn {
			if !conn.warnedNoUpdateCalls {
				conn.warnedNoUpdateCalls = true
				ss.lg.Warnf("%s: no messages for %s", conn.tcw, StateUpdateWarn)
				ss.sim.PostEvent(sim.Event{
					Type: sim.StatusMessageEvent,
					WrittenText: fmt.Sprintf("%s (%s) has not been heard from for %s. Connection lost?",
						string(conn.tcw), conn.initials, StateUpdateWarn),
				})
			}

			if time.Since(conn.lastUpdateCall) > StateUpdateKick {
				ss.lg.Warnf("%s (%s): signing off idle controller", conn.tcw, conn.initials)
				// Collect tokens to sign off after releasing the lock
				tokensToSignOff = append(tokensToSignOff, token)
			}
		}
	}
	ss.mu.Unlock(ss.lg)

	// Sign off controllers without holding ss.mu to avoid deadlock
	for _, token := range tokensToSignOff {
		if err := sm.SignOff(token); err != nil {
			ss.lg.Errorf("error signing off idle controller: %v", err)
		}
		// Note: SignOff handles deletion from connectionsByToken
	}
}

// updateSimPauseState pauses the sim if no humans are connected, unpauses if at least one.
// Must be called with ss.mu held.
func (ss *simSession) updateSimPauseState() {
	hasHumans := util.SeqContainsFunc(maps.Values(ss.connectionsByToken),
		func(conn *connectionState) bool { return conn.tcw != "" })
	ss.sim.SetPausedByServer(!hasHumans)
}

///////////////////////////////////////////////////////////////////////////
// State Updates and Controller Context

// State-update timing constants.
const (
	// StateUpdateMaxWait is the bound past which we assume the publish loop has stalled, in which
	// case WaitForStateUpdate returns ErrSimPublishStalled and the client surfaces a
	// connection-lost status event.
	StateUpdateMaxWait = 2 * time.Second

	// StateUpdateWarn and StateUpdateKick are the lastUpdateCall ages at which the server
	// respectively warns the other controllers and signs an unresponsive client off; they must
	// exceed StateUpdateMaxWait so a healthy long-poll cycle doesn't trip them.
	StateUpdateWarn = 5 * time.Second
	StateUpdateKick = 15 * time.Second
)

// GetStateUpdate is the main entry point for periodic state updates from a controller. It is a
// long-poll: when the caller's lastSentGen is already behind the sim's publication generation, it
// returns the current snapshot immediately; otherwise it parks until the sim publishes new state,
// the heartbeat timer fires, or the sim is destroyed.
func (ss *simSession) GetStateUpdate(token string) (*SimStateUpdate, error) {
	ss.mu.Lock(ss.lg)
	conn, ok := ss.connectionsByToken[token]
	if !ok {
		ss.mu.Unlock(ss.lg)
		ss.lg.Errorf("%s: unknown token for sim", token)
		return nil, nil
	}

	conn.lastUpdateCall = time.Now()
	if conn.warnedNoUpdateCalls {
		conn.warnedNoUpdateCalls = false
		ss.lg.Warnf("%s(%s): connection re-established", conn.tcw, conn.initials)
		ss.sim.PostEvent(sim.Event{
			Type:        sim.StatusMessageEvent,
			WrittenText: fmt.Sprintf("%s (%s) is back online.", string(conn.tcw), conn.initials),
		})
	}

	tcw := conn.tcw
	eventSub := conn.stateUpdateEventSub
	sinceGen := conn.lastSentGen
	ss.mu.Unlock(ss.lg)

	update, gen, err := ss.sim.WaitForStateUpdate(tcw, sinceGen, StateUpdateMaxWait)
	if err != nil {
		return nil, err
	}

	// The client may have signed off while we were waiting. Re-validate under
	// ss.mu before calling eventSub.Get(): SignOff serializes Unsubscribe via
	// ss.mu, so this closes the lifecycle race that would otherwise produce
	// "Attempted to get with unregistered subscription" errors.
	ss.mu.Lock(ss.lg)
	var events []sim.Event
	if conn, ok = ss.connectionsByToken[token]; ok {
		conn.lastSentGen = gen
		events = eventSub.Get()
	}
	ss.mu.Unlock(ss.lg)

	return &SimStateUpdate{
		StateUpdate: update,
		ActiveTCWs:  ss.GetActiveTCWs(),
		Events:      ss.sim.PrepareRadioTransmissionsForTCW(tcw, events),
	}, nil
}

// MakeControllerContext returns a ControllerContext for the given token, or nil if not found.
func (ss *simSession) MakeControllerContext(token string) *controllerContext {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	conn, ok := ss.connectionsByToken[token]
	if !ok {
		return nil
	}
	return &controllerContext{
		token:    token,
		tcw:      conn.tcw,
		initials: conn.initials,
		sim:      ss.sim,
		eventSub: conn.stateUpdateEventSub,
		session:  ss,
	}
}

///////////////////////////////////////////////////////////////////////////
// Position/TCW State Queries (for GetRunningSims)

func (ss *simSession) GetCurrentConsolidation() map[sim.TCW]TCPConsolidation {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	tcwInitials := make(map[sim.TCW][]string)
	for _, conn := range ss.connectionsByToken {
		tcwInitials[conn.tcw] = append(tcwInitials[conn.tcw], conn.initials)
	}

	// Get consolidation from sim and add initials
	consolidation := make(map[sim.TCW]TCPConsolidation)
	for tcw, cons := range ss.sim.GetCurrentConsolidation() {
		consolidation[tcw] = TCPConsolidation{
			TCPConsolidation: *cons,
			Initials:         tcwInitials[tcw],
		}
	}

	return consolidation
}

// getActiveTCWs returns the set of TCWs that have at least one human signed in.
// Must be called with ss.mu held.
func (ss *simSession) getActiveTCWs() []sim.TCW {
	var tcws []string
	for _, conn := range ss.connectionsByToken {
		if conn.tcw != "" {
			tcws = append(tcws, string(conn.tcw))
		}
	}
	slices.Sort(tcws)
	tcws = slices.Compact(tcws) // may have multiple connections to a TCW...
	return util.MapSlice(tcws, func(tcw string) sim.TCW { return sim.TCW(tcw) })
}

// GetActiveTCWs returns a sorted list of TCWs that have humans signed in.
func (ss *simSession) GetActiveTCWs() []sim.TCW {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	return ss.getActiveTCWs()
}

// RequestContact pops the next pending contact for the TCW, generates the transmission
// with current aircraft state, and returns text + voice name for client-side synthesis.
// Returns empty values if no contact is pending.
func (ss *simSession) RequestContact(tcw sim.TCW) (text string, voiceName string, callsign av.ADSBCallsign, ty av.RadioTransmissionType) {
	// Get all positions controlled by this TCW (primary + consolidated secondaries)
	cons := ss.sim.State.CurrentConsolidation[tcw]
	if cons == nil {
		return "", "", "", 0
	}
	positions := cons.OwnedPositions()

	// Try pending contacts from any of the controlled positions
	for {
		pc := ss.sim.PopReadyContact(positions)
		if pc == nil {
			return "", "", "", 0
		}

		// Generate the contact transmission with current aircraft state
		spokenText, _ := ss.sim.GenerateContactTransmission(pc)
		if spokenText == "" {
			// Aircraft may be gone or invalid - try the next one
			continue
		}

		voiceName := ss.sim.VoiceAssigner.GetVoice(pc.ADSBCallsign, ss.sim.Rand)

		return spokenText, voiceName, pc.ADSBCallsign, av.RadioTransmissionContact
	}
}

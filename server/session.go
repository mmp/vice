// server/session.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
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
	voiceAssigner      *VoiceAssigner

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
		voiceAssigner:      NewVoiceAssigner(),
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
	speechWs            *websocket.Conn
	disableTTS          bool
	stateUpdateEventSub *sim.EventsSubscription
	ttsEventSub         *sim.EventsSubscription
	ttsRequests         ttsRequestPool
}

///////////////////////////////////////////////////////////////////////////
// Controller Lifecycle

func (ss *simSession) AddHumanController(token string, tcw sim.TCW, initials string, disableTTS bool,
	sub *sim.EventsSubscription) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	// Create a separate subscription for TTS rather than using stateUpdateEventSub so it doesn't
	// miss updates after state update calls drain the event stream updates.
	var ttsEventSub *sim.EventsSubscription
	if !disableTTS {
		ttsEventSub = ss.sim.Subscribe()
	}

	ss.connectionsByToken[token] = &connectionState{
		token:               token,
		tcw:                 tcw,
		initials:            initials,
		lastUpdateCall:      time.Now(),
		disableTTS:          disableTTS,
		stateUpdateEventSub: sub,
		ttsEventSub:         ttsEventSub,
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
	if conn.ttsEventSub != nil {
		conn.ttsEventSub.Unsubscribe()
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

	// Sign off controllers we haven't heard from in 15 seconds so that someone else can take their
	// place.
	var tokensToSignOff []string
	for token, conn := range ss.connectionsByToken {
		if time.Since(conn.lastUpdateCall) > 5*time.Second {
			if !conn.warnedNoUpdateCalls {
				conn.warnedNoUpdateCalls = true
				ss.lg.Warnf("%s: no messages for 5 seconds", conn.tcw)
				ss.sim.PostEvent(sim.Event{
					Type: sim.StatusMessageEvent,
					WrittenText: fmt.Sprintf("%s (%s) has not been heard from for 5 seconds. Connection lost?",
						string(conn.tcw), conn.initials),
				})
			}

			if time.Since(conn.lastUpdateCall) > 15*time.Second {
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

// GetStateUpdate populates the update with session state and processes TTS.
// This is the main entry point for periodic state updates from a controller.
func (ss *simSession) GetStateUpdate(token string, tts sim.TTSProvider) *SimStateUpdate {
	ss.mu.Lock(ss.lg)
	conn, ok := ss.connectionsByToken[token]
	if !ok {
		ss.mu.Unlock(ss.lg)
		ss.lg.Errorf("%s: unknown token for sim", token)
		return nil
	}

	// Update last call time and handle reconnection
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

	var ttsEvents []sim.Event
	if conn.ttsEventSub != nil {
		ttsEvents = conn.ttsEventSub.Get()
	}
	ss.mu.Unlock(ss.lg)

	ss.ProcessTTS(token, ttsEvents, tts)

	return &SimStateUpdate{
		StateUpdate: ss.sim.GetStateUpdate(),
		ActiveTCWs:  ss.GetActiveTCWs(),
		Events:      ss.sim.PrepareRadioTransmissionsForTCW(tcw, eventSub.Get()),
	}
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

///////////////////////////////////////////////////////////////////////////
// TTS/Speech

func (ss *simSession) HandleSpeechWSConnection(token string, w http.ResponseWriter, r *http.Request) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	if conn, ok := ss.connectionsByToken[token]; !ok {
		ss.lg.Errorf("%s: unknown token", token)
	} else {
		if conn.speechWs != nil {
			conn.speechWs.Close()
		}

		var err error
		upgrader := websocket.Upgrader{EnableCompression: false}
		conn.speechWs, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			ss.lg.Errorf("Unable to upgrade speech websocket: %v", err)
		}
	}
}

func (ss *simSession) SendSpeechMP3s() int64 {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	var nb int
	for _, conn := range ss.connectionsByToken {
		if conn.speechWs == nil {
			continue
		}

		for _, ps := range conn.ttsRequests.drainCompleted(ss.lg) {
			nb += len(ps.MP3)

			w, err := conn.speechWs.NextWriter(websocket.BinaryMessage)
			if err != nil {
				ss.lg.Errorf("speechWs: %v", err)
				continue
			}

			if err := msgpack.NewEncoder(w).Encode(ps); err != nil {
				ss.lg.Errorf("speechWs encode: %v", err)
				continue
			}

			if err := w.Close(); err != nil {
				ss.lg.Errorf("speechWs close: %v", err)
				continue
			}
		}
	}

	return int64(nb)
}

// ProcessTTS processes radio transmission events and initiates TTS generation.
func (ss *simSession) ProcessTTS(token string, events []sim.Event, tts sim.TTSProvider) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	conn, ok := ss.connectionsByToken[token]
	if !ok || conn.disableTTS || tts == nil {
		return
	}

	ss.voiceAssigner.TryInit(tts, ss.lg)

	for _, e := range ss.sim.PrepareRadioTransmissionsForTCW(conn.tcw, events) {
		if e.Type != sim.RadioTransmissionEvent || !ss.sim.TCWControlsPosition(conn.tcw, e.ToController) {
			continue
		}

		if e.SpokenText == "" {
			continue
		}

		if voice, ok := ss.voiceAssigner.GetVoice(e.ADSBCallsign); !ok {
			ss.lg.Warnf("TTS: no voice available for %s", e.ADSBCallsign)
		} else {
			conn.ttsRequests.add(ttsRequest{
				callsign: e.ADSBCallsign,
				ty:       e.RadioTransmissionType,
				text:     e.SpokenText,
				fut:      tts.TextToSpeech(voice, e.SpokenText),
			})
		}
	}
}

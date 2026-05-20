// client/client.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"fmt"
	"net"
	"net/rpc"
	"slices"
	"sort"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
	"github.com/mmp/vice/tts"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

type ControlClient struct {
	controllerToken string
	client          *RPCClient
	remoteServer    *RPCClient // Remote server for STT log reporting (set when running local sim)

	// Speech/TTS management
	transmissions         *TransmissionManager
	disableTTSPtr         *bool
	sttActive             bool
	LastTranscription     string
	LastCommand           string
	LastWhisperDurationMs int64 // Last whisper transcription time in milliseconds
	eventStream           *sim.EventStream

	// Streaming STT state
	streamingSTT   *streamingSTT
	sttTranscriber *stt.Transcriber
	pttReleaseTime time.Time // Wall clock time when PTT was released (for latency tracking)

	// Last callsign that replied "AGAIN" - allows controller to repeat command without callsign
	lastAgainCallsign av.ADSBCallsign

	lg *log.Logger
	mu sync.Mutex

	lastUpdateApplied time.Time
	lastReturnedTime  sim.Time
	updateCall        *pendingCall

	pendingCalls []*pendingCall

	SessionStats SessionStats

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	State SimState
}

// This is the client-side representation of a server (perhaps could be better-named...)
type Server struct {
	*RPCClient

	AvailableWXByFacility map[string][]util.TimeInterval

	name        string
	catalogs    map[string]map[string]*server.ScenarioCatalog
	runningSims map[string]*server.RunningSim
}

type SessionStats struct {
	Departures    int
	Arrivals      int
	IntraFacility int
	Overflights   int

	SignOnTime time.Time
	Initials   string

	initialized   bool
	seenCallsigns map[av.ADSBCallsign]any
}

func (s *SessionStats) Update(ss *SimState) {
	for _, trk := range ss.Tracks {
		if trk.FlightPlan == nil || !ss.UserControlsTrack(trk) {
			continue
		}
		if _, ok := s.seenCallsigns[trk.ADSBCallsign]; ok {
			continue
		}
		s.seenCallsigns[trk.ADSBCallsign] = nil

		// Don't count pre-existing aircraft from before sign-on.
		if !s.initialized {
			continue
		}

		if trk.IsDeparture() {
			s.Departures++
		} else if trk.IsArrival() {
			s.Arrivals++
		} else if trk.IsOverflight() {
			s.Overflights++
		}
	}
	s.initialized = true
}

func (c *ControlClient) RPCClient() *RPCClient {
	return c.client
}

func (c *ControlClient) SetRemoteServer(remote *RPCClient) {
	c.mu.Lock()
	c.remoteServer = remote
	c.mu.Unlock()
}

type RPCClient struct {
	*rpc.Client
}

func (c *RPCClient) callWithTimeout(serviceMethod string, args any, reply any) error {
	pc := &pendingCall{
		Call:      c.Go(serviceMethod, args, reply, nil),
		IssueTime: time.Now(),
	}

	for {
		select {
		case <-pc.Call.Done:
			return pc.Call.Error

		case <-time.After(10 * time.Second):
			if !util.DebuggerIsRunning() {
				return fmt.Errorf("%s: %w", serviceMethod, server.ErrRPCTimeout)
			}
		}
	}
}

type pendingCall struct {
	Call      *rpc.Call
	IssueTime time.Time
	Callback  func(*sim.EventStream, *SimState, error)
}

func makeRPCCall(call *rpc.Call, callback func(error)) *pendingCall {
	return &pendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *SimState, err error) {
			if callback != nil {
				callback(err)
			}
		},
	}
}

func makeStateUpdateRPCCall(call *rpc.Call, update *server.SimStateUpdate, callback func(error)) *pendingCall {
	return &pendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *SimState, err error) {
			if err == nil {
				update.Apply(&state.SimState, es)
			} else {
				es.Post(sim.Event{
					Type:        sim.ErrorMessageEvent,
					WrittenText: "Server state update failed: " + err.Error(),
				})
			}
			if callback != nil {
				callback(err)
			}
		},
	}
}

func (p *pendingCall) CheckFinished() bool {
	select {
	case <-p.Call.Done:
		return true
	default:
		return false
	}
}

func (p *pendingCall) InvokeCallback(es *sim.EventStream, state *SimState) {
	if p.Callback != nil {
		p.Callback(es, state, p.Call.Error)
	}
}

func NewControlClient(ss server.SimState, controllerToken string, disableTTSPtr *bool, initials string,
	client *RPCClient, lg *log.Logger) *ControlClient {
	cc := &ControlClient{
		controllerToken:   controllerToken,
		client:            client,
		lg:                lg,
		lastUpdateApplied: time.Now(),
		State:             SimState{ss},
		transmissions:     NewTransmissionManager(lg),
		disableTTSPtr:     disableTTSPtr,
		sttTranscriber:    stt.NewTranscriber(lg),
	}

	cc.SessionStats.SignOnTime = ss.SimTime.Time()
	cc.SessionStats.Initials = initials
	cc.SessionStats.seenCallsigns = make(map[av.ADSBCallsign]any)

	return cc
}

func (c *ControlClient) Status() string {
	if c == nil {
		return "[disconnected]"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.State.SimDescription == "" {
		return "[disconnected]"
	} else {
		stats := c.SessionStats
		deparr := fmt.Sprintf(" [ %d departures %d arrivals %d intrafacility %d overflights ]",
			stats.Departures, stats.Arrivals, stats.IntraFacility, stats.Overflights)
		return string(c.State.UserTCW) + c.State.SimDescription + deparr
	}
}

func (c *ControlClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.client.callWithTimeout(server.SignOffRPC, c.controllerToken, nil); err != nil {
		c.lg.Errorf("Error signing off from sim: %v", err)
	}
	c.State.Tracks = nil
	c.State.UnassociatedFlightPlans = nil
	c.State.Controllers = nil
}

func (c *ControlClient) addCall(pc *pendingCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingCalls = append(c.pendingCalls, pc)
}

func (c *ControlClient) AirspaceForTCW(tcw sim.TCW) []av.ControllerAirspaceVolume {
	var vols []av.ControllerAirspaceVolume
	for _, pos := range c.State.GetPositionsForTCW(tcw) {
		for _, avol := range util.SortedMap(c.State.Airspace[pos]) {
			vols = append(vols, avol...)
		}
	}
	return vols
}

func (c *ControlClient) GetUpdates(eventStream *sim.EventStream, p platform.Platform, onErr func(error)) {
	if c.client == nil {
		return
	}

	// Track if we need to call onErr after releasing the lock; calling it
	// with the lock held is verboten since it may call Disconnect(), which
	// also needs the lock.
	var callbackErr error
	var completedCalls []*pendingCall
	var updateCallFinished *pendingCall

	c.mu.Lock()

	// Store eventStream for STT event posting and TTS latency tracking
	c.eventStream = eventStream
	c.transmissions.SetEventStream(eventStream)

	if c.updateCall != nil {
		if c.updateCall.CheckFinished() {
			updateCallFinished = c.updateCall
			c.updateCall = nil
			c.SessionStats.Update(&c.State)
		} else {
			callbackErr = checkTimeout(c.updateCall, eventStream)
		}
	}

	// State delivery is server-paced: keep exactly one GetStateUpdate outstanding.
	if c.updateCall == nil { // First call or just got an update
		var update server.SimStateUpdate
		c.updateCall = makeStateUpdateRPCCall(c.client.Go(server.GetStateUpdateRPC, c.controllerToken, &update, nil), &update, nil)
	}

	c.updateSpeech(p)

	// Check if we should request a contact transmission from the server.
	// We request contacts when the server has TTS capability, even if the user
	// has disabled TTS locally. This ensures pilots still join the frequency
	// and text transmissions appear. Audio playback is controlled separately.
	// The actual request is made after releasing the lock.
	shouldRequestContact := c.transmissions.ShouldRequestContact()

	if callbackErr == nil {
		completedCalls, callbackErr = c.checkPendingRPCs(eventStream)
	}

	c.mu.Unlock()

	// Make RPC calls that need addCall after releasing the lock
	if shouldRequestContact {
		c.RequestContactTransmission()
	}

	// Invoke callbacks after releasing lock to avoid deadlock.
	// InterpolatedSimTime extrapolates from State.SimTime + (now -
	// lastUpdateApplied); we only re-anchor when DynamicState was
	// actually replaced (i.e. the publication generation advanced).
	// Otherwise repeated same-gen responses — RPC errors or the
	// simDone path that returns the current snapshot without a fresh
	// publish — would freeze the displayed clock until extrapolation
	// re-catches up to where it had been.
	if updateCallFinished != nil {
		prevGen := c.State.GenerationIndex
		updateCallFinished.InvokeCallback(eventStream, &c.State)
		if c.State.GenerationIndex > prevGen {
			now := time.Now()
			c.mu.Lock()
			c.lastUpdateApplied = now
			c.mu.Unlock()
		}
	}
	for _, call := range completedCalls {
		call.InvokeCallback(eventStream, &c.State)
	}
	if callbackErr != nil && onErr != nil {
		onErr(callbackErr)
	}
}

func (c *ControlClient) updateSpeech(p platform.Platform) {
	// Delegate to TransmissionManager
	c.transmissions.Update(p, c.State.Paused, c.sttActive)
}

func (c *ControlClient) checkPendingRPCs(eventStream *sim.EventStream) ([]*pendingCall, error) {
	var completed []*pendingCall
	c.pendingCalls = slices.DeleteFunc(c.pendingCalls,
		func(call *pendingCall) bool {
			if call.CheckFinished() {
				completed = append(completed, call)
				return true
			}
			return false
		})

	for _, call := range c.pendingCalls {
		if err := checkTimeout(call, eventStream); err != nil {
			return completed, err
		}
	}
	return completed, nil
}

func checkTimeout(call *pendingCall, eventStream *sim.EventStream) error {
	// The long-poll GetStateUpdate is bounded by server.StateUpdateMaxWait
	// and returns an error (not a hung RPC) on the server's own timeout.
	// This check catches genuine network failures where the RPC never
	// returns at all; StateUpdateWarn is comfortably above MaxWait.
	if time.Since(call.IssueTime) > server.StateUpdateWarn && !util.DebuggerIsRunning() {
		eventStream.Post(sim.Event{
			Type: sim.StatusMessageEvent,
			WrittenText: fmt.Sprintf("No response from server for over %s. Network connection may be lost.",
				server.StateUpdateWarn),
		})
		return server.ErrRPCTimeout
	}
	return nil
}

func (c *ControlClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.client != nil
}

func (c *ControlClient) GetSimRate() float32 {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.State.SimRate == 0 {
		return 1
	}
	return c.State.SimRate
}

// InterpolatedSimTime returns an extrapolated value that models the current
// Sim's time. (Because the Sim may be running remotely, we have to make some
// approximations, though they shouldn't cause much trouble since we get an
// update from the Sim at least once a second...)
func (c *ControlClient) InterpolatedSimTime() sim.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := c.State.SimTime

	if !c.State.Paused && !c.lastUpdateApplied.IsZero() {
		// Extrapolate forward from the moment the most recent server
		// snapshot was applied.
		d := time.Since(c.lastUpdateApplied)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(c.State.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(c.lastReturnedTime) {
		c.lastReturnedTime = t
	}
	return c.lastReturnedTime
}

func (c *ControlClient) TowerListAirports() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Figure out airport<-->tower list assignments. Sort the airports
	// according to their TowerListIndex, putting zero (i.e., unassigned)
	// indices at the end. Break ties alphabetically by airport name. The
	// first three then are assigned to the corresponding tower list.
	ap := util.SortedMapKeys(c.State.ArrivalAirports)
	sort.Slice(ap, func(a, b int) bool {
		ai := c.State.Airports[ap[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := c.State.Airports[ap[b]].TowerListIndex
		if bi == 0 {
			bi = 1000
		}
		if ai == bi {
			return a < b
		}
		return ai < bi
	})
	if len(ap) > 3 {
		ap = ap[:3]
	}
	return ap
}

func (c *ControlClient) StringIsSPC(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return av.StringIsSPC(s) || slices.Contains(c.State.FacilityAdaptation.Datablocks.CustomSPCs, s)
}

func (c *ControlClient) RadioIsActive() bool {
	return c.transmissions.IsPlaying()
}

func (c *ControlClient) HoldRadioTransmissions() {
	c.transmissions.HoldAfterTransmission()
}

// BeginGarble holds pilot transmissions while the user has PTT pressed
// during ongoing audio playback (garble mode). This prevents queued
// transmissions from starting playback while the user is still holding PTT
// trying to interrupt. Pair with EndGarble on PTT release.
func (c *ControlClient) BeginGarble() {
	c.transmissions.Hold()
	c.lg.Info("SPEECH: garble hold acquired")
}

func (c *ControlClient) EndGarble() {
	c.transmissions.Unhold()
	c.lg.Info("SPEECH: garble hold released")
}

func (c *ControlClient) LastTTSCallsign() av.ADSBCallsign {
	return c.transmissions.LastTransmissionCallsign()
}

func (c *ControlClient) GetPrecipURL(t sim.Time, callback func(url string, nextTime sim.Time, err error)) {
	args := wx.PrecipURLArgs{
		Facility: c.State.Facility,
		Time:     t.Time(),
	}
	var result wx.PrecipURL
	c.addCall(makeRPCCall(c.client.Go(wx.GetPrecipURLRPC, args, &result, nil),
		func(err error) {
			if callback != nil {
				callback(result.URL, sim.NewSimTime(result.NextTime), err)
			}
		}))
}

func (c *ControlClient) GetAtmosGrid(t time.Time, callback func(*wx.AtmosGrid, error)) {
	spec := wx.GetAtmosArgs{
		Facility:       c.State.Facility,
		Time:           t,
		PrimaryAirport: c.State.PrimaryAirport,
	}
	var result wx.GetAtmosResult
	c.addCall(makeRPCCall(c.client.Go(wx.GetAtmosGridRPC, spec, &result, nil),
		func(err error) {
			if callback != nil {
				if result.AtmosByPointSOA != nil {
					callback(result.AtmosByPointSOA.ToAOS().GetGrid(), err)
				} else {
					callback(nil, err)
				}
			}
		}))
}

///////////////////////////////////////////////////////////////////////////
// Server

type serverConnection struct {
	Server *Server
	Err    error
}

func (s *Server) Close() error {
	return s.RPCClient.Close()
}

func (s *Server) GetScenarioCatalogs() map[string]map[string]*server.ScenarioCatalog {
	return s.catalogs
}

func (s *Server) setRunningSims(rs map[string]*server.RunningSim) {
	s.runningSims = rs
}

func (s *Server) GetRunningSims() map[string]*server.RunningSim {
	return s.runningSims
}

func getClient(hostname string, lg *log.Logger) (*RPCClient, error) {
	conn, err := net.Dial("tcp", hostname)
	if err != nil {
		return nil, err
	}

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(hostname, codec, lg)
	return &RPCClient{rpc.NewClientWithCodec(codec)}, nil
}

func TryConnectRemoteServer(hostname string, lg *log.Logger) chan *serverConnection {
	ch := make(chan *serverConnection, 1)
	go func() {
		if client, err := getClient(hostname, lg); err != nil {
			ch <- &serverConnection{Err: err}
			return
		} else {
			var cr server.ConnectResult
			start := time.Now()
			if err := client.callWithTimeout(server.ConnectRPC, server.ViceRPCVersion, &cr); err != nil {
				ch <- &serverConnection{Err: err}
			} else {
				lg.Debugf("%s: server returned configuration in %s", hostname, time.Since(start))
				ch <- &serverConnection{
					Server: &Server{
						RPCClient:             client,
						AvailableWXByFacility: cr.AvailableWXByFacility,
						name:                  "Network (Multi-controller)",
						catalogs:              cr.ScenarioCatalogs,
						runningSims:           cr.RunningSims,
					},
				}
			}
		}
	}()

	return ch
}

func BroadcastMessage(hostname, msg, password string, lg *log.Logger) {
	client, err := getClient(hostname, lg)
	if err != nil {
		lg.Errorf("unable to get client for broadcast: %v", err)
		return
	}

	err = client.callWithTimeout(server.BroadcastRPC, &server.BroadcastMessage{
		Password: password,
		Message:  msg,
	}, nil)

	if err != nil {
		lg.Errorf("broadcast error: %v", err)
	}
}

// Thread-safe access to STT fields
func (c *ControlClient) SetLastTranscription(s string) {
	c.mu.Lock()
	c.LastTranscription = s
	c.mu.Unlock()
}

func (c *ControlClient) GetLastTranscription() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastTranscription
}

func (c *ControlClient) SetLastCommand(s string) {
	c.mu.Lock()
	c.LastCommand = s
	c.mu.Unlock()
}

func (c *ControlClient) GetLastCommand() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastCommand
}

func (c *ControlClient) GetLastWhisperDurationMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastWhisperDurationMs
}

///////////////////////////////////////////////////////////////////////////
// Local TTS synthesis helpers

// synthesizeAndEnqueueReadback synthesizes text and enqueues it as a readback.
// Called from a goroutine. On failure, Unhold() is called because RunAircraftCommands
// calls Hold() before issuing the command to prevent contacts while waiting for the readback.
func (c *ControlClient) synthesizeAndEnqueueReadback(callsign av.ADSBCallsign, text, voice string) {
	radioSeed := uint32(util.HashString64(string(callsign)))
	if pcm, err := tts.SynthesizeReadbackTTS(text, voice, radioSeed); err != nil {
		c.lg.Errorf("TTS synthesis error for %s: %v", callsign, err)
		c.transmissions.Unhold()
	} else if pcm == nil {
		// TTS not available, silently unhold
		c.transmissions.Unhold()
	} else {
		durationMs := int64(len(pcm)) * 1000 / platform.AudioSampleRate
		c.lg.Infof("SPEECH queued readback: %s (%dms audio) %q", callsign, durationMs, text)
		c.transmissions.EnqueueReadbackPCM(callsign, av.RadioTransmissionReadback, pcm)
	}
}

// synthesizeAndEnqueueContact synthesizes text and enqueues it as a contact transmission.
// Called from a goroutine. Unlike readbacks, no Hold() is acquired before requesting
// contacts, so no Unhold() is needed on failure.
func (c *ControlClient) synthesizeAndEnqueueContact(callsign av.ADSBCallsign, ty av.RadioTransmissionType, text, voice string) {
	radioSeed := uint32(util.HashString64(string(callsign)))
	if pcm, err := tts.SynthesizeContactTTS(text, voice, radioSeed); err != nil {
		c.lg.Errorf("TTS synthesis error for %s: %v", callsign, err)
	} else if pcm != nil {
		durationMs := int64(len(pcm)) * 1000 / platform.AudioSampleRate
		c.lg.Infof("SPEECH queued contact: %s (%dms audio) %q", callsign, durationMs, text)
		c.transmissions.EnqueueTransmissionPCM(callsign, ty, pcm)
	}
	c.transmissions.SetContactRequested(false)
}

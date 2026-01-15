// client/client.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

// stepOnProbability is the chance that another pilot will transmit
// despite a priority readback being expected.
const stepOnProbability = 0.05

type ControlClient struct {
	controllerToken string
	client          *RPCClient

	speechWs                 *websocket.Conn
	speechCh                 chan sim.PilotSpeech
	bufferedSpeech           []sim.PilotSpeech
	playingSpeech            bool
	holdSpeech               bool
	sttActive                bool
	lastSpeechHoldTime       time.Time
	awaitReadbackCallsign    av.ADSBCallsign
	lastTransmissionCallsign av.ADSBCallsign
	LastTranscription        string
	LastCommand              string
	eventStream              *sim.EventStream

	// Streaming STT state
	streamingSTT *streamingSTT

	lg *log.Logger
	mu sync.Mutex

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *pendingCall
	lastUpdateLatency time.Duration

	pendingCalls []*pendingCall

	SessionStats SessionStats

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	State SimState
}

// This is the client-side representation of a server (perhaps could be better-named...)
type Server struct {
	*RPCClient

	HaveTTS             bool
	AvailableWXByTRACON map[string][]util.TimeInterval

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

	seenCallsigns map[av.ADSBCallsign]any
}

func (s *SessionStats) Update(ss *SimState) {
	for i, trk := range ss.Tracks {
		if fp := trk.FlightPlan; fp != nil {
			// Use track ownership check (via OwningTCW).
			if !ss.UserControlsTrack(ss.Tracks[i]) {
				continue // not ours
			}
			if _, ok := s.seenCallsigns[trk.ADSBCallsign]; ok {
				continue // seen it already
			}
			s.seenCallsigns[trk.ADSBCallsign] = nil
			if trk.IsDeparture() {
				s.Departures++
			} else if trk.IsArrival() {
				s.Arrivals++
			} else if trk.IsOverflight() {
				s.Overflights++
			}
		}
	}
}

func (c *ControlClient) RPCClient() *RPCClient {
	return c.client
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

		case <-time.After(5 * time.Second):
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

func NewControlClient(ss server.SimState, controllerToken string, wsURL string, initials string, client *RPCClient, lg *log.Logger) *ControlClient {
	cc := &ControlClient{
		controllerToken:   controllerToken,
		client:            client,
		lg:                lg,
		lastUpdateRequest: time.Now(),
		State:             SimState{ss},
	}

	if wsURL != "" {
		cc.speechWs, cc.speechCh = initializeSpeechWebsocket(controllerToken, wsURL, lg)
	}

	cc.SessionStats.SignOnTime = ss.SimTime
	cc.SessionStats.Initials = initials
	cc.SessionStats.seenCallsigns = make(map[av.ADSBCallsign]any)
	return cc
}

func initializeSpeechWebsocket(controllerToken string, wsURL string, lg *log.Logger) (*websocket.Conn, chan sim.PilotSpeech) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+controllerToken)

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+wsURL+"/speech", header)
	if err != nil {
		lg.Warnf("speech websocket: %v", err)
		return nil, nil
	}

	speechCh := make(chan sim.PilotSpeech)
	go func() {
		for {
			ty, r, err := conn.NextReader()
			if err != nil {
				var cerr *websocket.CloseError
				if errors.As(err, &cerr) {
					// all good, we're shutting down
					lg.Errorf("websocket closed; exiting client reader")
				} else {
					lg.Errorf("speech websocket read: %T, %v", err, err)
				}
				return
			}
			if ty != websocket.BinaryMessage {
				lg.Errorf("expected binary message, got %d", ty)
				continue
			}

			var ps sim.PilotSpeech
			if err := msgpack.NewDecoder(r).Decode(&ps); err != nil {
				lg.Errorf("PilotSpeech: %v", err)
				continue
			}

			speechCh <- ps
		}
	}()

	return conn, speechCh
}

func (c *ControlClient) HaveTTS() bool {
	return c.speechWs != nil
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
	if c.speechWs != nil {
		c.speechWs.Close()
		c.speechWs = nil
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

	// Store eventStream for STT event posting
	c.eventStream = eventStream

	if c.updateCall != nil {
		if c.updateCall.CheckFinished() {
			updateCallFinished = c.updateCall
			c.updateCall = nil
			c.SessionStats.Update(&c.State)
		} else {
			callbackErr = checkTimeout(c.updateCall, eventStream)
		}
	}

	c.updateSpeech(p)

	if callbackErr == nil {
		completedCalls, callbackErr = c.checkPendingRPCs(eventStream)
	}

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/c.State.SimRate, 0.05, 1)
	if d := time.Since(c.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if c.updateCall != nil && !util.DebuggerIsRunning() {
			c.lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			c.mu.Unlock()
			// Invoke callbacks after releasing lock to avoid deadlock
			if updateCallFinished != nil {
				updateCallFinished.InvokeCallback(eventStream, &c.State)
			}
			for _, call := range completedCalls {
				call.InvokeCallback(eventStream, &c.State)
			}
			if callbackErr != nil && onErr != nil {
				onErr(callbackErr)
			}
			return
		}
		c.lastUpdateRequest = time.Now()

		var update server.SimStateUpdate
		issueTime := time.Now()
		c.updateCall = makeStateUpdateRPCCall(c.client.Go(server.GetStateUpdateRPC, c.controllerToken, &update, nil), &update,
			func(err error) {
				d := time.Since(issueTime)
				c.lastUpdateLatency = d
				if d > 250*time.Millisecond {
					c.lg.Warnf("Slow world update response %s", d)
				} else {
					c.lg.Debugf("World update response time %s", d)
				}
			})
	}

	c.mu.Unlock()

	// Invoke callbacks after releasing lock to avoid deadlock
	if updateCallFinished != nil {
		updateCallFinished.InvokeCallback(eventStream, &c.State)
	}
	for _, call := range completedCalls {
		call.InvokeCallback(eventStream, &c.State)
	}
	if callbackErr != nil && onErr != nil {
		onErr(callbackErr)
	}
}

func (c *ControlClient) updateSpeech(p platform.Platform) {
	// See if anything new has arrived from the server
loop:
	for {
		select {
		case ps, ok := <-c.speechCh:
			if ok {
				c.bufferedSpeech = append(c.bufferedSpeech, ps)
			}
		default:
			break loop
		}
	}

	if c.holdSpeech && time.Now().After(c.lastSpeechHoldTime) {
		// Time out user-requested holds after 5 seconds (if another
		// request hasn't kept the request alive).
		c.holdSpeech = false
	}

	if c.holdSpeech || c.sttActive || c.playingSpeech || len(c.bufferedSpeech) == 0 || c.State.Paused {
		// Don't/can't kick off additional speech playback
		return
	}

	// Time to play speech
	if c.awaitReadbackCallsign != "" {
		// Prioritize responses from issued aircraft instructions over cold calls.
		isResponse := func(ps sim.PilotSpeech) bool { return ps.Callsign == c.awaitReadbackCallsign }
		if idx := slices.IndexFunc(c.bufferedSpeech, isResponse); idx != -1 {
			// Found the awaited readback - play it
			bs := c.bufferedSpeech[idx]
			c.bufferedSpeech = append(c.bufferedSpeech[:idx], c.bufferedSpeech[idx+1:]...)

			// Handle empty MP3 (TTS error case)
			if len(bs.MP3) == 0 {
				c.lg.Warnf("Skipping speech for %s due to empty MP3 (TTS error)", bs.Callsign)
				c.awaitReadbackCallsign = ""
				c.holdSpeech = false
				return
			}

			if err := p.TryEnqueueSpeechMP3(bs.MP3, func() {
				c.awaitReadbackCallsign = ""
				c.playingSpeech = false
				c.holdSpeech = true
				c.lastTransmissionCallsign = bs.Callsign
				c.lastSpeechHoldTime = time.Now().Add(3 * time.Second / 2)
			}); err == nil {
				c.playingSpeech = true
			}
		} else if len(c.bufferedSpeech) > 0 && rand.Make().Float32() < stepOnProbability {
			// Awaited readback not yet available, but another pilot "steps on" the frequency
			bs := c.bufferedSpeech[0]
			c.bufferedSpeech = c.bufferedSpeech[1:]

			if len(bs.MP3) == 0 {
				c.lg.Warnf("Skipping speech for %s due to empty MP3 (TTS error)", bs.Callsign)
				return
			}

			c.lg.Infof("Pilot %s stepping on frequency (awaiting %s)", bs.Callsign, c.awaitReadbackCallsign)
			if err := p.TryEnqueueSpeechMP3(bs.MP3, func() {
				c.playingSpeech = false
				// Don't clear awaitReadbackCallsign - still waiting for that pilot
				c.lastTransmissionCallsign = bs.Callsign
				c.lastSpeechHoldTime = time.Now().Add(2 * time.Second)
			}); err == nil {
				c.playingSpeech = true
			}
		}
		// else: awaited readback not available and no step-on - wait
	} else {
		// No priority callsign - play in FIFO order
		bs := c.bufferedSpeech[0]
		c.bufferedSpeech = c.bufferedSpeech[1:]

		// Handle empty MP3 (TTS error case)
		if len(bs.MP3) == 0 {
			c.lg.Warnf("Skipping speech for %s due to empty MP3 (TTS error)", bs.Callsign)
			return
		}

		if err := p.TryEnqueueSpeechMP3(bs.MP3, func() {
			c.playingSpeech = false
			c.holdSpeech = true
			c.lastTransmissionCallsign = bs.Callsign
			c.lastSpeechHoldTime = time.Now().Add(2 * time.Second)
		}); err == nil {
			c.playingSpeech = true
		}
	}
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
	if time.Since(call.IssueTime) > 5*time.Second && !util.DebuggerIsRunning() {
		eventStream.Post(sim.Event{
			Type:        sim.StatusMessageEvent,
			WrittenText: "No response from server for over 5 seconds. Network connection may be lost.",
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

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (c *ControlClient) CurrentTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := c.State.SimTime

	if !c.State.Paused && !c.lastUpdateRequest.IsZero() {
		d := time.Since(c.lastUpdateRequest)

		// Account for RPC overhead using half of the observed latency
		if c.lastUpdateLatency > 0 {
			d -= c.lastUpdateLatency / 2
		}
		d = max(0, d)

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

	return av.StringIsSPC(s) || slices.Contains(c.State.FacilityAdaptation.CustomSPCs, s)
}

func (c *ControlClient) RadioIsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.HaveTTS() && (c.playingSpeech || c.awaitReadbackCallsign != "")
}

func (c *ControlClient) HoldRadioTransmissions() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.HaveTTS() {
		c.holdSpeech = true
		c.lastSpeechHoldTime = time.Now().Add(5 * time.Second)
	}
}

func (c *ControlClient) AllowRadioTransmissions() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.holdSpeech = false
}

func (c *ControlClient) LastTTSCallsign() av.ADSBCallsign {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastTransmissionCallsign
}

func (c *ControlClient) GetPrecipURL(t time.Time, callback func(url string, nextTime time.Time, err error)) {
	args := server.PrecipURLArgs{
		Facility: c.State.Facility,
		Time:     t,
	}
	var result server.PrecipURL
	c.addCall(makeRPCCall(c.client.Go(server.GetPrecipURLRPC, args, &result, nil),
		func(err error) {
			if callback != nil {
				callback(result.URL, result.NextTime, err)
			}
		}))
}

func (c *ControlClient) GetAtmosGrid(t time.Time, callback func(*wx.AtmosGrid, error)) {
	spec := server.GetAtmosArgs{
		Facility:       c.State.Facility,
		Time:           t,
		PrimaryAirport: c.State.PrimaryAirport,
	}
	var result server.GetAtmosResult
	c.addCall(makeRPCCall(c.client.Go(server.GetAtmosGridRPC, spec, &result, nil),
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
// STT

var whisperModel *whisper.Model
var whisperModelErr error
var whisperModelOnce sync.Once
var whisperModelMu sync.Mutex

// PreloadWhisperModel loads the whisper model in the background so it's
// ready when PTT is first pressed. This avoids blocking the UI.
func PreloadWhisperModel(lg *log.Logger) {
	go func() {
		whisperModelOnce.Do(func() {
			lg.Info("Preloading whisper model...")
			model := util.LoadResourceBytes("models/ggml-small.en.bin")
			whisperModel, whisperModelErr = whisper.LoadModelFromBytes(model)
			if whisperModelErr != nil {
				lg.Errorf("Failed to load whisper model: %v", whisperModelErr)
			} else {
				lg.Info("Whisper model loaded")
			}
		})
	}()
}

func makeWhisperPrompt(state SimState) string {
	// Build initial prompt with common phrases, aircraft telephony, and approaches.
	// Most important items first since whisper has a 224 token limit.
	promptParts := []string{
		"climb and maintain", "descend and maintain", "maintain",
		"turn left", "turn right", "fly heading", "proceed direct", "expect the",
		"reduce speed to", "maintain maximum forward speed", "contact tower",
		"expect", "vectors", "squawk", "ident", "altimieter", "radar contact",
		"reduce to final approach speed", "miles from", "established", "cleared",
	}

	// Add telephony, approaches, and fixes for user-controlled tracks.
	assignedApproaches := make(map[string]struct{})
	fixes := make(map[string]struct{})
	for _, trk := range state.Tracks {
		if state.UserControlsTrack(trk) && trk.IsAssociated() {
			tele := av.GetTelephony(string(trk.ADSBCallsign), trk.FlightPlan.CWTCategory)
			promptParts = append(promptParts, tele)
			if trk.Approach != "" {
				assignedApproaches[trk.Approach] = struct{}{}
			}
			for _, fix := range trk.Fixes {
				fixes[fix] = struct{}{}
			}
		}
	}

	// Add assigned approaches (higher priority)
	for appr := range assignedApproaches {
		promptParts = append(promptParts, av.GetApproachTelephony(appr))
	}

	// Add active approaches (converted to spoken form, excluding already-added assigned ones)
	activeApproaches := make(map[string]struct{})
	for _, ar := range state.ArrivalRunways {
		if ap, ok := state.Airports[ar.Airport]; ok {
			for _, appr := range ap.Approaches {
				if appr.Runway == ar.Runway {
					activeApproaches[appr.FullName] = struct{}{}
				}
			}
		}
	}
	for appr := range activeApproaches {
		if _, assigned := assignedApproaches[appr]; !assigned {
			promptParts = append(promptParts, av.GetApproachTelephony(appr))
		}
	}

	// Add fixes (lower priority, may get truncated by token limit)
	for fix := range fixes {
		promptParts = append(promptParts, av.GetFixTelephony(fix))
	}

	return strings.Join(promptParts, ", ")
}

// postSTTEvent posts an STTCommandEvent to the event stream.
func (c *ControlClient) postSTTEvent(transcript, command string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.eventStream.Post(sim.Event{
		Type:          sim.STTCommandEvent,
		STTTranscript: transcript,
		STTCommand:    command,
	})
}

// streamingSTT holds state for a streaming transcription session.
type streamingSTT struct {
	transcriber *whisper.StreamingTranscriber
	resultChan  <-chan whisper.StreamingResult
	state       SimState // Snapshot of state at start of streaming
}

// StartStreamingSTT begins a streaming transcription session.
// Audio samples can be fed via FeedAudioToStreaming.
// Call StopStreamingSTT to end the session and process the result.
func (c *ControlClient) StartStreamingSTT(lg *log.Logger) error {
	// Load model if not already loaded
	whisperModelOnce.Do(func() {
		model := util.LoadResourceBytes("models/ggml-small.en.bin")
		whisperModel, whisperModelErr = whisper.LoadModelFromBytes(model)
	})
	if whisperModelErr != nil {
		return fmt.Errorf("whisper LoadModelFromBytes: %w", whisperModelErr)
	}

	// Snapshot state for prompt construction
	state := c.State

	st := whisper.NewStreamingTranscriber(whisperModel, &whisperModelMu, whisper.Options{
		InitialPrompt: makeWhisperPrompt(state),
	})

	c.mu.Lock()
	c.streamingSTT = &streamingSTT{
		transcriber: st,
		resultChan:  st.Start(),
		state:       state,
	}
	// Set STT active state - hold speech playback during recording/processing.
	// Set lastSpeechHoldTime far in future so timeout doesn't fire during recording.
	// The actual timeout (7s) is set in StopStreamingSTT when PTT is released.
	c.sttActive = true
	c.holdSpeech = true
	c.lastSpeechHoldTime = time.Now().Add(time.Hour)
	c.mu.Unlock()

	lg.Info("SPEECH: streaming STT started, sttActive=true holdSpeech=true")

	// Start goroutine to handle streaming results
	go c.handleStreamingResults(lg)

	return nil
}

// handleStreamingResults processes intermediate transcription results.
func (c *ControlClient) handleStreamingResults(lg *log.Logger) {
	defer lg.CatchAndReportCrash()

	c.mu.Lock()
	stt := c.streamingSTT
	c.mu.Unlock()

	if stt == nil {
		return
	}

	for result := range stt.resultChan {
		if result.IsFinal {
			break
		}
		// Update UI with intermediate result (add "..." to indicate in progress)
		c.SetLastTranscription(result.Text + "...")
	}
}

// StopStreamingSTT ends the streaming session and processes the final result.
// The session is captured synchronously to avoid races, then processing
// continues asynchronously to avoid blocking the UI.
func (c *ControlClient) StopStreamingSTT(lg *log.Logger) {
	// Synchronously capture and clear the session to avoid race if user
	// quickly presses PTT again
	c.mu.Lock()
	stt := c.streamingSTT
	c.streamingSTT = nil
	c.sttActive = false
	// Now that PTT is released, set the actual timeout for holdSpeech.
	// This gives STT processing (Whisper finalize + Claude) time to complete.
	c.lastSpeechHoldTime = time.Now().Add(7 * time.Second)
	c.mu.Unlock()

	lg.Info("SPEECH: streaming STT stopped, sttActive=false, holdSpeech timeout started")

	if stt == nil {
		// No active session - StartStreamingSTT was never called or failed
		// before acquiring the mutex, so don't unlock
		return
	}

	// Capture start time before spawning goroutine so we measure from PTT release
	whisperStart := time.Now()

	// Process the rest asynchronously to avoid blocking the UI
	go func() {
		defer lg.CatchAndReportCrash()

		// Get final transcription
		finalText := stt.transcriber.Stop()
		whisperDuration := time.Since(whisperStart)

		lg.Infof("streaming whisper transcription %q (final pass in %s)", finalText, whisperDuration)

		c.SetLastTranscription(finalText)

		if finalText == "" || finalText == "[BLANK_AUDIO]" {
			c.postSTTEvent("", "")
			return
		}

		// Process the command through the same flow as batch mode
		c.ProcessSTTTranscript(finalText, whisperDuration, func(callsign, command string, sttDuration time.Duration, err error) {
			if err != nil {
				lg.Infof("STT command error: %v", err)
				c.postSTTEvent(finalText, "Error: "+err.Error())
			} else {
				lg.Infof("STT command: %s %s", callsign, command)
				fullCommand := callsign + " " + command
				c.SetLastCommand(fullCommand)
				c.postSTTEvent(finalText, fullCommand)
			}
		})
	}()
}

// FeedAudioToStreaming sends audio samples to the streaming transcriber.
func (c *ControlClient) FeedAudioToStreaming(samples []int16) {
	c.mu.Lock()
	stt := c.streamingSTT
	c.mu.Unlock()

	if stt != nil && stt.transcriber != nil {
		stt.transcriber.AddSamples(samples)
	}
}

// ProcessSTTTranscript sends the transcript to the server for decoding and command execution.
func (c *ControlClient) ProcessSTTTranscript(transcript string, whisperDuration time.Duration,
	callback func(callsign, command string, sttDuration time.Duration, err error)) {
	var result server.ProcessSTTTranscriptResult
	c.addCall(makeRPCCall(c.client.Go(server.ProcessSTTTranscriptRPC, &server.ProcessSTTTranscriptArgs{
		ControllerToken: c.controllerToken,
		Transcript:      transcript,
		WhisperDuration: whisperDuration,
		NumCores:        runtime.NumCPU(),
	}, &result, nil),
		func(err error) {
			c.mu.Lock()
			if err == nil && result.Callsign != "" {
				// Set priority for readback from addressed pilot.
				c.awaitReadbackCallsign = av.ADSBCallsign(result.Callsign)
			}
			// Clear holdSpeech now that STT processing is complete.
			// The hold's purpose was to prevent speech during STT processing;
			// now we can let the awaited readback (or other buffered speech) play.
			c.holdSpeech = false
			c.mu.Unlock()
			if callback != nil {
				callback(result.Callsign, result.Command, result.STTDuration, err)
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
						RPCClient:           client,
						HaveTTS:             cr.HaveTTS,
						AvailableWXByTRACON: cr.AvailableWXByTRACON,
						name:                "Network (Multi-controller)",
						catalogs:            cr.ScenarioCatalogs,
						runningSims:         cr.RunningSims,
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

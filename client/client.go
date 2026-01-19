// client/client.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"
	"fmt"
	"net"
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
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"golang.org/x/sys/cpu"
)

type ControlClient struct {
	controllerToken string
	client          *RPCClient

	// Speech/TTS management
	transmissions         *TransmissionManager
	haveTTS               bool // whether TTS is enabled for this session
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

	// Previous STT context for bug reporting
	prevSTTContext *STTBugContext

	// Whisper performance tracking for slow GPU detection
	recentWhisperDurations  []time.Duration // Sliding window of recent whisper durations
	slowPerformanceReported bool            // True if we've already reported slow performance

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

// STTBugContext stores context from a previous STT decode for bug reporting.
type STTBugContext struct {
	Transcript      string                  // Raw whisper transcript
	AircraftContext map[string]stt.Aircraft // Aircraft context used for decoding
	DebugLogs       []string                // Captured logLocalStt output
	DecodedCommand  string                  // Result of DecodeTranscript
	Timestamp       time.Time               // When this decode happened
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

func NewControlClient(ss server.SimState, controllerToken string, haveTTS bool, disableTTSPtr *bool, initials string, client *RPCClient, lg *log.Logger) *ControlClient {
	cc := &ControlClient{
		controllerToken:   controllerToken,
		client:            client,
		lg:                lg,
		lastUpdateRequest: time.Now(),
		State:             SimState{ss},
		transmissions:     NewTransmissionManager(lg),
		haveTTS:           haveTTS,
		disableTTSPtr:     disableTTSPtr,
		sttTranscriber:    stt.NewTranscriber(lg),
	}

	cc.SessionStats.SignOnTime = ss.SimTime
	cc.SessionStats.Initials = initials
	cc.SessionStats.seenCallsigns = make(map[av.ADSBCallsign]any)
	return cc
}

func (c *ControlClient) HaveTTS() bool {
	return c.haveTTS
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
				// Process RadioSpeech (pilot-initiated transmissions) from state update
				// Skip if TTS is disabled at runtime (user toggled it off)
				ttsEnabled := c.disableTTSPtr == nil || !*c.disableTTSPtr
				if err == nil && len(update.RadioSpeech) > 0 && ttsEnabled {
					c.transmissions.EnqueueFromStateUpdate(update.RadioSpeech)
				}

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
	return c.HaveTTS() && c.transmissions.IsPlaying()
}

func (c *ControlClient) HoldRadioTransmissions() {
	if c.HaveTTS() {
		c.transmissions.HoldAfterTransmission()
	}
}

func (c *ControlClient) AllowRadioTransmissions() {
	// Hold timeouts are handled by TransmissionManager - this is now a no-op
	// but kept for API compatibility
}

func (c *ControlClient) LastTTSCallsign() av.ADSBCallsign {
	return c.transmissions.LastTransmissionCallsign()
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
var whisperModelName string
var whisperModelErr error
var whisperModelMu sync.Mutex
var whisperModelDone = make(chan struct{})
var whisperModelOnce sync.Once

// ErrCPUNotSupported is returned when the CPU doesn't support the required
// instruction sets for speech-to-text (AVX on x86/amd64).
var ErrCPUNotSupported = errors.New("CPU does not support required instructions for speech-to-text")

// checkCPUSupport verifies that the CPU supports the instruction sets
// required by the whisper library. Returns an error if not supported.
func checkCPUSupport() error {
	// Only x86/amd64 needs AVX support check; ARM uses NEON which is always available.
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "386" {
		return nil
	}

	// Use golang.org/x/sys/cpu for reliable cross-platform feature detection.
	if cpu.X86.HasAVX {
		return nil
	}

	return ErrCPUNotSupported
}

// GetWhisperModelName returns the name of the currently loaded whisper model.
func GetWhisperModelName() string {
	whisperModelMu.Lock()
	defer whisperModelMu.Unlock()
	return whisperModelName
}

// PreloadWhisperModel loads the whisper model in the background so it's
// ready when PTT is first pressed. This avoids blocking the UI.
// Model selection is automatic: ggml-small.en.bin when GPU acceleration is
// available (macOS Metal, Windows Vulkan), ggml-tiny.en.bin otherwise (CPU-only).
func PreloadWhisperModel(lg *log.Logger) {
	go func() {
		whisperModelOnce.Do(func() {
			defer close(whisperModelDone)

			// Check CPU compatibility before attempting to load.
			if err := checkCPUSupport(); err != nil {
				whisperModelErr = fmt.Errorf("%w (AVX instruction set not available)", ErrCPUNotSupported)
				lg.Warnf("Speech-to-text unavailable: %v", whisperModelErr)
				return
			}

			// Use larger model when a discrete GPU or macOS Metal is available.
			// On Windows, integrated GPUs (Intel UHD/Iris, AMD APU) are too slow for
			// larger models, so we only use them with a discrete GPU.
			// On macOS, Metal provides good performance even on integrated GPUs.
			var modelName string
			if runtime.GOOS == "darwin" || whisper.GPUDiscrete() {
				modelName = "ggml-small.en.bin"
			} else {
				modelName = "ggml-tiny.en.bin"
			}

			lg.Infof("Preloading whisper model: %s", modelName)
			modelBytes := util.LoadResourceBytes("models/" + modelName)
			whisperModelMu.Lock()
			whisperModel, whisperModelErr = whisper.LoadModelFromBytes(modelBytes)
			if whisperModelErr != nil {
				lg.Errorf("Failed to load whisper model: %v", whisperModelErr)
				whisperModelMu.Unlock()
				return
			}
			whisperModelName = modelName
			lg.Infof("Whisper model loaded: %s", modelName)
			whisperModelMu.Unlock()

			// Run warmup transcription with 1 second of silence.  The first inference pass on a
			// newly loaded model is slower due to shader compilation and memory allocation. Running
			// a warmup pass ensures the first real transcription works reliably.
			warmupST := whisper.NewStreamingTranscriber(whisperModel, &whisperModelMu, whisper.Options{})
			warmupST.Start()
			warmupST.AddSamples(make([]int16, 16000)) // 1 second at 16kHz
			warmupST.Stop()
			lg.Info("Whisper model warmed up")
		})
	}()
}

// WhisperModelError waits for the whisper model to finish loading and returns
// any error that occurred. Returns nil if the model loaded successfully.
// This can be used to check if STT is available and show an error dialog if not.
func WhisperModelError() error {
	<-whisperModelDone
	return whisperModelErr
}

// IsSTTAvailable returns true if speech-to-text is available.
// This blocks until the whisper model finishes loading.
func IsSTTAvailable() bool {
	return WhisperModelError() == nil
}

func makeWhisperPrompt(state SimState) string {
	// Build initial prompt with common phrases, aircraft telephony, and approaches.
	// Most important items first since whisper has a 224 token limit.
	promptParts := []string{
		"climb and maintain", "descend and maintain", "maintain", "direct", "cleared direct",
		"turn left", "turn right", "fly heading", "proceed direct", "expect the",
		"reduce speed to", "maintain maximum forward speed", "contact tower",
		"expect", "vectors", "squawk", "ident", "altimieter", "radar contact",
		"reduce to final approach speed", "miles from", "established", "cleared",
		"until established", "on the localizer", "flight level", "niner",
	}

	// Add telephony and approaches for user-controlled tracks.
	// Collect fixes separately using map to dedupe.
	assignedApproaches := make(map[string]struct{})
	fixes := make(map[string]struct{})
	for _, trk := range state.Tracks {
		if state.UserControlsTrack(trk) && trk.IsAssociated() {
			tele := av.GetTelephony(string(trk.ADSBCallsign), trk.FlightPlan.CWTCategory)
			promptParts = append(promptParts, tele)
			if trk.Approach != "" {
				assignedApproaches[trk.Approach] = struct{}{}
			}
			// Add up to 3 upcoming fixes from this aircraft's route
			for i, fix := range trk.Fixes {
				if i >= 3 {
					break
				}
				fixes[fix] = struct{}{}
			}
		}
	}

	// Add assigned approaches (higher priority)
	for appr := range assignedApproaches {
		promptParts = append(promptParts, av.GetApproachTelephony(appr))
	}

	// Collect active approaches and their fixes
	activeApproaches := make(map[string]struct{})
	for _, ar := range state.ArrivalRunways {
		if ap, ok := state.Airports[ar.Airport]; ok {
			for _, appr := range ap.Approaches {
				if appr.Runway == ar.Runway {
					activeApproaches[appr.FullName] = struct{}{}
					// Add all fixes from this active approach
					for _, wps := range appr.Waypoints {
						for _, wp := range wps {
							if len(wp.Fix) >= 3 && len(wp.Fix) <= 5 && wp.Fix[0] != '_' {
								fixes[wp.Fix] = struct{}{}
							}
						}
					}
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
func (c *ControlClient) postSTTEvent(transcript, command, timings string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.eventStream.Post(sim.Event{
		Type:          sim.STTCommandEvent,
		STTTranscript: transcript,
		STTCommand:    command,
		STTTimings:    timings,
	})
}

// GetAndClearPTTReleaseTime returns the PTT release time and clears it.
// Returns zero time if no PTT release is pending.
func (c *ControlClient) GetAndClearPTTReleaseTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.pttReleaseTime
	c.pttReleaseTime = time.Time{}
	return t
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
	// Wait for initial model load to complete
	<-whisperModelDone
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
	// Hold speech playback during recording/processing
	c.sttActive = true
	c.transmissions.Hold()
	c.mu.Unlock()

	lg.Info("SPEECH: streaming STT started, sttActive=true")

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
	sttSession := c.streamingSTT
	c.streamingSTT = nil
	c.sttActive = false
	// Keep hold active during async processing; Unhold() is called when done
	c.mu.Unlock()

	lg.Info("SPEECH: streaming STT stopped, sttActive=false")

	if sttSession == nil {
		return
	}

	// Capture start time before spawning goroutine so we measure from PTT release
	pttReleaseTime := time.Now()
	c.mu.Lock()
	c.pttReleaseTime = pttReleaseTime
	c.mu.Unlock()

	// Process the rest asynchronously to avoid blocking the UI
	go func() {
		defer lg.CatchAndReportCrash()

		// Get final transcription from whisper
		finalText := sttSession.transcriber.Stop()
		whisperDuration := time.Since(pttReleaseTime)

		c.mu.Lock()
		c.LastWhisperDurationMs = whisperDuration.Milliseconds()
		c.LastTranscription = finalText

		// Track recent whisper durations for slow performance detection
		c.recentWhisperDurations = append(c.recentWhisperDurations, whisperDuration)
		const maxTrackedDurations = 5
		if len(c.recentWhisperDurations) > maxTrackedDurations {
			c.recentWhisperDurations = c.recentWhisperDurations[1:]
		}

		// Check for consistently slow performance (all recent durations > 1 second)
		// Only check when using ggml-small model (not ggml-tiny)
		slowPerformanceThreshold := time.Second
		modelName := GetWhisperModelName()
		isSlowCandidate := strings.Contains(modelName, "small") && len(c.recentWhisperDurations) >= 3
		shouldReport := isSlowCandidate && !c.slowPerformanceReported

		if shouldReport {
			allSlow := true
			for _, d := range c.recentWhisperDurations {
				if d < slowPerformanceThreshold {
					allSlow = false
					break
				}
			}
			shouldReport = allSlow
		}
		c.mu.Unlock()

		// Report slow performance outside the lock
		if shouldReport {
			c.reportSlowWhisperPerformance(lg)
		}

		if finalText == "" || finalText == "[BLANK_AUDIO]" {
			c.transmissions.Unhold()
			c.postSTTEvent("", "", "")
			return
		}

		// Check for "STT bug" command pattern
		// After normalization, "sierra tango tango bug" becomes ["s", "t", "t", "bug", ...]
		words := stt.NormalizeTranscript(finalText)
		if len(words) >= 4 && words[0] == "s" && words[1] == "t" && words[2] == "t" && words[3] == "bug" {
			// Extract user explanation (words after "bug")
			var explanation string
			if len(words) > 4 {
				explanation = strings.Join(words[4:], " ")
			}

			c.reportSTTBug(explanation, lg)
			c.transmissions.Unhold()
			c.postSTTEvent(finalText, "[STT Bug Reported]", "")
			return
		}

		// Build aircraft context before decoding
		aircraftCtx := c.sttTranscriber.BuildAircraftContext(&c.State.UserState, c.State.UserTCW)

		// Get controller radio name for position identification detection
		controllerRadioName := ""
		primaryPos := c.State.UserState.PrimaryPositionForTCW(c.State.UserTCW)
		if ctrl, ok := c.State.UserState.Controllers[primaryPos]; ok && ctrl != nil {
			controllerRadioName = ctrl.RadioName
		}

		// Start capturing debug logs
		stt.StartCapture()

		// Decode transcript locally using current state
		decoded, err := c.sttTranscriber.DecodeTranscript(aircraftCtx, finalText, controllerRadioName)

		// Stop capturing and get debug logs
		debugLogs := stt.StopCapture()

		totalDuration := time.Since(pttReleaseTime)
		timingStr := fmt.Sprintf("%.0fms", float64(totalDuration.Microseconds())/1000)

		// Store this context as "previous" for future bug reports
		c.mu.Lock()
		c.prevSTTContext = &STTBugContext{
			Transcript:      finalText,
			AircraftContext: aircraftCtx,
			DebugLogs:       debugLogs,
			DecodedCommand:  decoded,
			Timestamp:       time.Now(),
		}
		c.mu.Unlock()

		if err != nil {
			lg.Infof("STT decode error: %v", err)
			c.transmissions.Unhold()
			c.postSTTEvent(finalText, "Error: "+err.Error(), timingStr)
			return
		}

		if decoded == "" {
			lg.Infof("STT: no command decoded from %q", finalText)
			c.transmissions.Unhold()
			c.postSTTEvent(finalText, decoded, timingStr)
			return
		}

		// Parse callsign and command from decoded result
		// BLOCKED is special: no callsign, server picks a random aircraft
		var callsign, command string
		if decoded == "BLOCKED" {
			command = "BLOCKED"
		} else {
			callsign, command, _ = strings.Cut(decoded, " ")
		}
		lg.Infof("STT command: %s %s", callsign, command)

		c.SetLastCommand(decoded)
		c.postSTTEvent(finalText, decoded, timingStr)

		// Execute the command via RPC (this handles TTS readback)
		c.RunAircraftCommands(av.ADSBCallsign(callsign), command, false, false,
			totalDuration, finalText,
			aircraftCtx, strings.Join(debugLogs, "\n"),
			func(message string, remainingInput string) {
				c.transmissions.Unhold()
				if message != "" {
					lg.Infof("STT command result: %s", message)
				}
			})
	}()
}

// FeedAudioToStreaming sends audio samples to the streaming transcriber.
func (c *ControlClient) FeedAudioToStreaming(samples []int16) {
	c.mu.Lock()
	sttSession := c.streamingSTT
	c.mu.Unlock()

	if sttSession != nil && sttSession.transcriber != nil {
		sttSession.transcriber.AddSamples(samples)
	}
}

// reportSTTBug sends an STT bug report to the server using the previous STT context.
func (c *ControlClient) reportSTTBug(explanation string, lg *log.Logger) {
	c.mu.Lock()
	prevCtx := c.prevSTTContext
	recentDurations := slices.Clone(c.recentWhisperDurations)
	c.mu.Unlock()

	if prevCtx == nil {
		lg.Info("STT bug reported but no previous context available")
		return
	}

	args := server.STTBugReportArgs{
		ControllerToken:   c.controllerToken,
		PrevTranscript:    prevCtx.Transcript,
		PrevCommand:       prevCtx.DecodedCommand,
		AircraftContext:   prevCtx.AircraftContext,
		DebugLogs:         prevCtx.DebugLogs,
		UserExplanation:   explanation,
		ReportTime:        time.Now(),
		GPUInfo:           whisper.GetGPUInfo(),
		WhisperModelName:  GetWhisperModelName(),
		RecentDurations:   recentDurations,
		IsSlowPerformance: false,
	}

	c.addCall(makeRPCCall(c.client.Go(server.ReportSTTBugRPC, &args, nil, nil),
		func(err error) {
			if err != nil {
				lg.Errorf("STT bug report failed: %v", err)
			} else {
				lg.Info("STT bug report submitted")
			}
		}))
}

// reportSlowWhisperPerformance sends an automatic bug report when whisper is consistently slow.
// This indicates a possible GPU selection issue (integrated GPU selected instead of discrete).
func (c *ControlClient) reportSlowWhisperPerformance(lg *log.Logger) {
	c.mu.Lock()
	if c.slowPerformanceReported {
		c.mu.Unlock()
		return
	}
	c.slowPerformanceReported = true
	recentDurations := slices.Clone(c.recentWhisperDurations)
	prevCtx := c.prevSTTContext
	c.mu.Unlock()

	// Include previous STT context if available for debugging
	var prevTranscript, prevCommand string
	var aircraftContext map[string]stt.Aircraft
	var debugLogs []string
	if prevCtx != nil {
		prevTranscript = prevCtx.Transcript
		prevCommand = prevCtx.DecodedCommand
		aircraftContext = prevCtx.AircraftContext
		debugLogs = prevCtx.DebugLogs
	}

	args := server.STTBugReportArgs{
		ControllerToken:   c.controllerToken,
		PrevTranscript:    prevTranscript,
		PrevCommand:       prevCommand,
		AircraftContext:   aircraftContext,
		DebugLogs:         debugLogs,
		UserExplanation:   "Automatic: slow whisper performance detected",
		ReportTime:        time.Now(),
		GPUInfo:           whisper.GetGPUInfo(),
		WhisperModelName:  GetWhisperModelName(),
		RecentDurations:   recentDurations,
		IsSlowPerformance: true,
	}

	lg.Warn("Reporting slow whisper performance - possible integrated GPU selection issue")

	c.addCall(makeRPCCall(c.client.Go(server.ReportSTTBugRPC, &args, nil, nil),
		func(err error) {
			if err != nil {
				lg.Errorf("Slow performance report failed: %v", err)
			} else {
				lg.Info("Slow performance report submitted")
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

func (c *ControlClient) GetLastWhisperDurationMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastWhisperDurationMs
}

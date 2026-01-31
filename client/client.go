// client/client.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/sys/cpu"
)

type ControlClient struct {
	controllerToken string
	client          *RPCClient
	remoteServer    *RPCClient // Remote server for STT log reporting (set when running local sim)

	// Speech/TTS management
	transmissions         *TransmissionManager
	haveTTS               bool // whether TTS is enabled for this session
	disableTTSPtr         *bool
	sttActive             bool
	LastTranscription     string
	LastCommand           string
	LastWhisperDurationMs int64 // Last whisper transcription time in milliseconds
	eventStream           *sim.EventStream

	// WebSocket for async speech delivery
	speechWsServerAddr string
	speechWsConn       *websocket.Conn
	speechWsClose      chan struct{}

	// Streaming STT state
	streamingSTT   *streamingSTT
	sttTranscriber *stt.Transcriber
	pttReleaseTime time.Time // Wall clock time when PTT was released (for latency tracking)

	// Previous STT context for bug reporting
	prevSTTContext *STTBugContext

	// Last callsign that replied "AGAIN" - allows controller to repeat command without callsign
	lastAgainCallsign av.ADSBCallsign

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
	host        string // hostname for WebSocket connections
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

func NewControlClient(ss server.SimState, controllerToken string, haveTTS bool, speechWSPort int, speechServerHost string,
	disableTTSPtr *bool, initials string, client *RPCClient, lg *log.Logger) *ControlClient {
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
		speechWsClose:     make(chan struct{}),
	}

	cc.SessionStats.SignOnTime = ss.SimTime
	cc.SessionStats.Initials = initials
	cc.SessionStats.seenCallsigns = make(map[av.ADSBCallsign]any)

	// Connect to WebSocket for async speech delivery if available
	if speechWSPort > 0 && haveTTS {
		cc.speechWsServerAddr = fmt.Sprintf("ws://%s:%d/speech", speechServerHost, speechWSPort)
		go cc.connectSpeechWebSocket()
	}

	return cc
}

func (c *ControlClient) HaveTTS() bool {
	return c.haveTTS
}

// connectSpeechWebSocket establishes and maintains a WebSocket connection for async speech delivery.
func (c *ControlClient) connectSpeechWebSocket() {
	defer c.lg.CatchAndReportCrash()

	for {
		select {
		case <-c.speechWsClose:
			return
		default:
		}

		c.lg.Infof("Connecting to speech WebSocket: %s", c.speechWsServerAddr)

		// Set up the request with Authorization header
		header := http.Header{}
		header.Set("Authorization", "Bearer "+c.controllerToken)

		dialer := websocket.Dialer{}
		conn, _, err := dialer.Dial(c.speechWsServerAddr, header)
		if err != nil {
			c.lg.Warnf("Speech WebSocket dial error: %v", err)
			select {
			case <-c.speechWsClose:
				return
			case <-time.After(5 * time.Second):
				continue // Retry connection
			}
		}

		c.mu.Lock()
		c.speechWsConn = conn
		c.mu.Unlock()

		c.lg.Infof("Speech WebSocket connected")

		// Read messages until error or close
		c.readSpeechMessages(conn)

		c.mu.Lock()
		c.speechWsConn = nil
		c.mu.Unlock()

		// Small delay before reconnecting
		select {
		case <-c.speechWsClose:
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// readSpeechMessages reads PilotSpeech messages from the WebSocket and enqueues them.
func (c *ControlClient) readSpeechMessages(conn *websocket.Conn) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.lg.Warnf("Speech WebSocket read error: %v", err)
			}
			return
		}

		var ps sim.PilotSpeech
		if err := msgpack.Unmarshal(message, &ps); err != nil {
			c.lg.Errorf("Speech WebSocket unmarshal error: %v", err)
			continue
		}

		// Decode MP3 to PCM here (off the main thread) to avoid frame drops
		pcm, err := platform.DecodeSpeechMP3(ps.MP3)
		if err != nil {
			c.lg.Errorf("Speech WebSocket MP3 decode error for %s: %v", ps.Callsign, err)
			continue
		}

		c.lg.Infof("Received speech via WebSocket for %s (%d bytes)", ps.Callsign, len(ps.MP3))

		// Enqueue with pre-decoded PCM (high priority, front of queue)
		c.transmissions.EnqueueReadbackPCM(ps.Callsign, ps.Type, pcm)
	}
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
	// Close the WebSocket connection if open (do this before acquiring mu since
	// the WebSocket receiver also needs mu)
	if c.speechWsClose != nil {
		close(c.speechWsClose)
	}

	c.mu.Lock()
	if c.speechWsConn != nil {
		c.speechWsConn.Close()
		c.speechWsConn = nil
	}
	c.mu.Unlock()

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
	var shouldRequestContact bool

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

	// Check if we should request a contact transmission from the server
	// Only do this if TTS is enabled. The actual request is made after
	// releasing the lock since addCall also needs the lock.
	ttsEnabled := c.haveTTS && (c.disableTTSPtr == nil || !*c.disableTTSPtr)
	if ttsEnabled && c.transmissions.ShouldRequestContact() {
		c.transmissions.SetContactRequested(true)
		shouldRequestContact = true
	}

	if callbackErr == nil {
		completedCalls, callbackErr = c.checkPendingRPCs(eventStream)
	}

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/c.State.SimRate, 0.05, 1)
	if d := time.Since(c.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if c.updateCall != nil && !util.DebuggerIsRunning() {
			c.lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			c.mu.Unlock()
			// Make RPC calls that need addCall after releasing the lock
			if shouldRequestContact {
				c.RequestContactTransmission()
			}
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

	// Make RPC calls that need addCall after releasing the lock
	if shouldRequestContact {
		c.RequestContactTransmission()
	}

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
var whisperModelNameAtomic atomic.Value // stores string, for lock-free reads from UI
var whisperModelErr error
var whisperModelMu sync.Mutex
var whisperModelDone chan struct{}
var whisperModelStarted bool
var whisperModelStartMu sync.Mutex
var whisperRealtimeFactor float64 // ratio of transcription time to audio duration from benchmark

// Benchmark status for UI display
var whisperBenchmarkStatus string
var whisperBenchmarkStatusMu sync.Mutex
var whisperIsBenchmarking bool // true only when actually running benchmarks, not just loading cached model

// WhisperBenchmarkIndex is the current benchmark generation. If the stored
// index in config is less than this, re-benchmarking is triggered. Increment
// this when benchmark criteria change (e.g., models, thresholds).
const WhisperBenchmarkIndex = 4

// Callback to save model selection to config
var whisperSaveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)

// Benchmark report data to be sent to server
var whisperBenchmarkReport *server.WhisperBenchmarkReport
var whisperBenchmarkReportMu sync.Mutex
var whisperBenchmarkReported bool

// STT evaluation mode - when enabled, runs audio through all models and prints comparison
var sttEvalEnabled bool

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
// Uses atomic load to avoid blocking the UI thread during whisper inference.
func GetWhisperModelName() string {
	if v := whisperModelNameAtomic.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// GetWhisperModelTiers returns the list of available whisper models, from smallest to largest.
func GetWhisperModelTiers() []string {
	return whisperModelTiers
}

// SetSTTEvalEnabled enables or disables STT evaluation mode.
// When enabled, each voice command runs through all whisper models and results are printed.
func SetSTTEvalEnabled(enabled bool) {
	sttEvalEnabled = enabled
}

// SelectWhisperModel directly selects a whisper model without benchmarking.
// This is used when the user manually chooses a model from the settings dropdown.
func SelectWhisperModel(lg *log.Logger, modelName string, saveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)) {
	whisperModelStartMu.Lock()
	// Close existing model if any
	whisperModelMu.Lock()
	if whisperModel != nil {
		whisperModel.Close()
		whisperModel = nil
	}
	whisperModelNameAtomic.Store("")
	whisperModelErr = nil
	whisperRealtimeFactor = 0
	whisperModelMu.Unlock()

	// Reset state
	whisperModelStarted = true
	whisperModelDone = make(chan struct{})
	whisperSaveCallback = saveCallback
	whisperModelStartMu.Unlock()

	go func() {
		defer close(whisperModelDone)

		deviceID := whisper.ProcessorDescription()
		// Use 0 for realtime factor since we're not benchmarking
		loadModelDirect(modelName, deviceID, 0, lg)
	}()
}

// GetWhisperDeviceID returns the device identifier used for whisper inference.
func GetWhisperDeviceID() string {
	return whisper.ProcessorDescription()
}

// IsWhisperBenchmarkDone returns true if the whisper model loading/benchmarking has completed.
func IsWhisperBenchmarkDone() bool {
	whisperModelStartMu.Lock()
	done := whisperModelDone
	whisperModelStartMu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// IsWhisperBenchmarking returns true if we're currently running actual benchmarks
// (as opposed to just loading a cached model). This is used to determine whether
// to show the benchmark progress dialog.
func IsWhisperBenchmarking() bool {
	whisperBenchmarkStatusMu.Lock()
	defer whisperBenchmarkStatusMu.Unlock()
	return whisperIsBenchmarking
}

// GetWhisperBenchmarkStatus returns the current benchmark status message for UI display.
func GetWhisperBenchmarkStatus() string {
	whisperBenchmarkStatusMu.Lock()
	defer whisperBenchmarkStatusMu.Unlock()
	return whisperBenchmarkStatus
}

func setWhisperBenchmarkStatus(status string) {
	whisperBenchmarkStatusMu.Lock()
	whisperBenchmarkStatus = status
	whisperBenchmarkStatusMu.Unlock()
	fmt.Printf("[whisper-benchmark] %s\n", status)
}

// ReportWhisperBenchmark sends the benchmark results to the remote server if available.
// This should be called once the connection manager has established a connection to the
// remote server. Returns true if the report was sent, false if no report available or
// already reported.
func ReportWhisperBenchmark(remoteServer *Server, lg *log.Logger) bool {
	if remoteServer == nil {
		return false
	}

	whisperBenchmarkReportMu.Lock()
	defer whisperBenchmarkReportMu.Unlock()

	if whisperBenchmarkReport == nil || whisperBenchmarkReported {
		return false
	}

	// Send the report asynchronously - we don't need to wait for the response
	go func() {
		var reply struct{}
		err := remoteServer.callWithTimeout(server.ReportWhisperBenchmarkRPC, whisperBenchmarkReport, &reply)
		if err != nil {
			lg.Warnf("Failed to report whisper benchmark: %v", err)
		} else {
			lg.Info("Whisper benchmark report sent to server")
		}
	}()

	whisperBenchmarkReported = true
	return true
}

// ForceWhisperRebenchmark closes the current model and triggers a fresh benchmark.
// This should be called when the user wants to re-run the benchmark.
func ForceWhisperRebenchmark(lg *log.Logger, saveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)) {
	whisperModelStartMu.Lock()
	// Close existing model if any
	whisperModelMu.Lock()
	if whisperModel != nil {
		whisperModel.Close()
		whisperModel = nil
	}
	whisperModelNameAtomic.Store("")
	whisperModelErr = nil
	whisperRealtimeFactor = 0
	whisperModelMu.Unlock()

	// Reset state for new benchmark
	whisperModelStarted = false
	whisperModelDone = nil
	whisperModelStartMu.Unlock()

	// Set benchmarking flag early to avoid race with UI check
	whisperBenchmarkStatusMu.Lock()
	whisperIsBenchmarking = true
	whisperBenchmarkStatusMu.Unlock()

	// Start fresh benchmark (pass 0 for cachedBenchmarkIndex to force rebenchmark)
	PreloadWhisperModel(lg, "", "", 0, 0, saveCallback)
}

// benchmarkModel loads a model, runs warmup passes, then benchmarks it with
// 1 second of silence. Returns the minimum latency from multiple passes.
// Multiple passes are needed because GPU performance can vary significantly
// due to power states, thermal throttling, and system load.
func benchmarkModel(modelName string) (latencyMs int64, model *whisper.Model, err error) {
	setWhisperBenchmarkStatus(fmt.Sprintf("Loading %s...", modelName))

	modelBytes := util.LoadResourceBytes("models/" + modelName)
	model, err = whisper.LoadModelFromBytes(modelBytes)
	if err != nil {
		fmt.Printf("[whisper-benchmark] Failed to load %s: %v\n", modelName, err)
		return 0, nil, err
	}

	var benchMu sync.Mutex
	samples := make([]int16, platform.AudioInputSampleRate) // 1 second of silence

	// runPass runs a single transcription pass and returns the latency
	runPass := func() int64 {
		start := time.Now()
		t := whisper.NewTranscriber(model, &benchMu, whisper.Options{Language: "en"})
		t.AddSamples(samples)
		t.Stop() // Discard text and audio duration for benchmark
		return time.Since(start).Milliseconds()
	}

	// Warmup passes to trigger shader compilation, memory allocation,
	// and bring GPU up to full power state.
	setWhisperBenchmarkStatus(fmt.Sprintf("Warming up %s...", modelName))
	for i := 0; i < 2; i++ {
		runPass()
	}

	// Benchmark passes - take the minimum (best case) latency.
	// The minimum represents true performance without interference from
	// transient system issues like background processes or thermal throttling.
	const numPasses = 3
	setWhisperBenchmarkStatus(fmt.Sprintf("Benchmarking %s...", modelName))
	var minLatency int64 = -1
	for i := 0; i < numPasses; i++ {
		lat := runPass()
		fmt.Printf("[whisper-benchmark] %s pass %d: %dms\n", modelName, i+1, lat)
		if minLatency < 0 || lat < minLatency {
			minLatency = lat
		}
	}

	setWhisperBenchmarkStatus(fmt.Sprintf("%s: %dms (best of %d)", modelName, minLatency, numPasses))
	return minLatency, model, nil
}

// Model size tiers for progressive benchmarking (smallest to largest)
var whisperModelTiers = []string{
	"ggml-base.en-jlvatc-q5_0.bin",
	"ggml-small.en-jlvatc-q5_0.bin",
	"ggml-medium.en-jlvatc-q5_0.bin",
}

// PreloadWhisperModel loads the whisper model in the background so it's
// ready when PTT is first pressed. This avoids blocking the UI.
//
// If cachedModelName and cachedDeviceID match the current device and the
// cachedBenchmarkIndex matches the current WhisperBenchmarkIndex, the cached
// model is loaded directly without benchmarking. Otherwise, a full benchmark
// is performed.
//
// The saveCallback is called when a model is selected (after benchmarking)
// to allow saving the selection to config.
func PreloadWhisperModel(lg *log.Logger, cachedModelName, cachedDeviceID string, cachedBenchmarkIndex int, cachedRealtimeFactor float64, saveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)) {
	whisperModelStartMu.Lock()
	if whisperModelStarted {
		whisperModelStartMu.Unlock()
		return
	}
	whisperModelStarted = true
	whisperModelDone = make(chan struct{})
	whisperSaveCallback = saveCallback
	whisperModelStartMu.Unlock()

	go func() {
		defer close(whisperModelDone)

		setWhisperBenchmarkStatus("Checking CPU compatibility...")

		// Check CPU compatibility before attempting to load.
		if err := checkCPUSupport(); err != nil {
			whisperModelErr = fmt.Errorf("%w (AVX instruction set not available)", ErrCPUNotSupported)
			lg.Warnf("Speech-to-text unavailable: %v", whisperModelErr)
			setWhisperBenchmarkStatus("CPU not supported")
			return
		}

		currentDeviceID := whisper.ProcessorDescription()

		// If no GPU available (Windows/Linux without Vulkan), just use tiny model.
		// On macOS, Metal is always available and handled by whisper.cpp internally.
		if runtime.GOOS != "darwin" && !whisper.GPUEnabled() {
			setWhisperBenchmarkStatus("No GPU available, using tiny model")
			lg.Info("No GPU available, using tiny whisper model")
			modelName := "ggml-tiny.en.bin"
			loadModelDirect(modelName, currentDeviceID, 0, lg) // 0 = unknown realtime factor
			return
		}

		// Check if we can use the cached model
		if cachedModelName != "" && cachedDeviceID == currentDeviceID && cachedBenchmarkIndex >= WhisperBenchmarkIndex {
			setWhisperBenchmarkStatus(fmt.Sprintf("Using cached model: %s", cachedModelName))
			lg.Infof("Using cached whisper model: %s (device: %s)", cachedModelName, currentDeviceID)
			if loadModelDirect(cachedModelName, currentDeviceID, cachedRealtimeFactor, lg) {
				return
			}
			// Model no longer exists (e.g., removed from distribution), fall through to benchmark
			fmt.Printf("[whisper-benchmark] Cached model %q no longer available - re-benchmarking\n", cachedModelName)
		} else if cachedModelName != "" {
			if cachedDeviceID != currentDeviceID {
				fmt.Printf("[whisper-benchmark] Device changed: was %q, now %q - re-benchmarking\n",
					cachedDeviceID, currentDeviceID)
				lg.Infof("Whisper device changed, re-benchmarking")
			} else if cachedBenchmarkIndex < WhisperBenchmarkIndex {
				fmt.Printf("[whisper-benchmark] Benchmark criteria changed (index %d -> %d) - re-benchmarking\n",
					cachedBenchmarkIndex, WhisperBenchmarkIndex)
				lg.Infof("Whisper benchmark criteria changed, re-benchmarking")
			}
		}

		// GPU is available - benchmark models progressively
		whisperBenchmarkStatusMu.Lock()
		whisperIsBenchmarking = true
		whisperBenchmarkStatusMu.Unlock()

		runBenchmark(lg, currentDeviceID)

		whisperBenchmarkStatusMu.Lock()
		whisperIsBenchmarking = false
		whisperBenchmarkStatusMu.Unlock()
	}()
}

// loadModelDirect loads a model without benchmarking (used for cached or no-GPU case).
// Returns true if the model was loaded successfully, false if it doesn't exist or failed to load.
func loadModelDirect(modelName, deviceID string, cachedRealtimeFactor float64, lg *log.Logger) bool {
	modelPath := "models/" + modelName
	if !util.ResourceExists(modelPath) {
		lg.Warnf("Cached whisper model %q not found, will re-benchmark", modelName)
		return false
	}
	modelBytes := util.LoadResourceBytes(modelPath)
	whisperModelMu.Lock()
	var err error
	whisperModel, err = whisper.LoadModelFromBytes(modelBytes)
	if err != nil {
		whisperModelErr = err
		lg.Errorf("Failed to load whisper model: %v", err)
		whisperModelMu.Unlock()
		setWhisperBenchmarkStatus("Failed to load model")
		return false
	}
	whisperModelNameAtomic.Store(modelName)
	whisperRealtimeFactor = cachedRealtimeFactor
	whisperModelMu.Unlock()

	// Warmup pass
	setWhisperBenchmarkStatus(fmt.Sprintf("Warming up %s...", modelName))
	warmupT := whisper.NewTranscriber(whisperModel, &whisperModelMu, whisper.Options{Language: "en"})
	warmupT.AddSamples(make([]int16, platform.AudioInputSampleRate)) // 1 second
	warmupT.Stop()                                                   // Discard results for warmup

	setWhisperBenchmarkStatus(fmt.Sprintf("Selected: %s", modelName))
	lg.Infof("Whisper model loaded: %s (realtimeFactor=%.3f)", modelName, cachedRealtimeFactor)

	// Save to config if callback provided
	if whisperSaveCallback != nil {
		whisperSaveCallback(modelName, deviceID, WhisperBenchmarkIndex, cachedRealtimeFactor)
	}
	return true
}

// runBenchmark performs the full progressive benchmark to select the best model
func runBenchmark(lg *log.Logger, deviceID string) {
	setWhisperBenchmarkStatus("Starting benchmark (GPU available)")
	lg.Info("Starting whisper model benchmark")

	// Relaxed thresholds to favor larger models (better accuracy).
	// With Whisper's fixed encoder time (~60-80% of total), a 1s benchmark with
	// 450ms threshold gives ~1.2x safety factor for real 3s commands.
	// Real 3s command: ~450 * 1.2 = ~540ms, acceptable for most use cases.
	const (
		continueThresholdMs = 300 // <300ms: fast enough, try larger model
		acceptThresholdMs   = 450 // Must process 1s of speech in <450ms to be usable
	)

	var selectedModel *whisper.Model
	var selectedName string
	var selectedLatency int64

	// Track all results for final summary
	type benchResult struct {
		name    string
		latency int64
		status  string
	}
	var allResults []benchResult

	// Progressively benchmark models from smallest to largest
	for _, modelName := range whisperModelTiers {
		latencyMs, model, err := benchmarkModel(modelName)
		if err != nil {
			fmt.Printf("[whisper-benchmark] Skipping %s due to error\n", modelName)
			continue
		}
		allResults = append(allResults, benchResult{modelName, latencyMs, ""})

		if latencyMs > acceptThresholdMs {
			// Too slow (>350ms) - can't use this model, use the previous one
			fmt.Printf("[whisper-benchmark] %s too slow (%dms > %dms), using previous\n",
				modelName, latencyMs, acceptThresholdMs)
			model.Close()
			break
		}

		// This model is acceptable - update selection
		if selectedModel != nil {
			selectedModel.Close()
		}
		selectedModel = model
		selectedName = modelName
		selectedLatency = latencyMs

		if latencyMs > continueThresholdMs {
			// Acceptable but not fast (250-350ms) - stop here
			fmt.Printf("[whisper-benchmark] %s acceptable (%dms), stopping\n", modelName, latencyMs)
			break
		}

		// Fast enough (<250ms) - continue to try larger model
		fmt.Printf("[whisper-benchmark] %s fast (%dms), trying larger\n", modelName, latencyMs)
	}

	// Check if we found any usable model
	if selectedModel == nil {
		whisperModelErr = errors.New("no model fast enough (need <350ms for 1s of speech)")
		lg.Error("No whisper model fast enough")
		setWhisperBenchmarkStatus("No model fast enough")
		return
	}

	// Calculate realtime factor: ratio of transcription time to audio duration
	// Used to enable quality features (beam search) on fast hardware
	realtimeFactor := float64(selectedLatency) / 1000.0 // latency for 1s audio

	// Print summary and build report for server
	fmt.Printf("[whisper-benchmark] === Results Summary ===\n")
	var reportResults []server.WhisperBenchmarkResult
	for i := range allResults {
		r := &allResults[i]
		if r.latency <= continueThresholdMs {
			r.status = "FAST"
		} else if r.latency <= acceptThresholdMs {
			r.status = "OK"
		} else {
			r.status = "SLOW"
		}
		marker := ""
		status := r.status
		if r.name == selectedName {
			marker = " <-- SELECTED"
			status = "selected"
		}
		fmt.Printf("[whisper-benchmark]   %s: %dms [%s]%s\n", r.name, r.latency, r.status, marker)
		reportResults = append(reportResults, server.WhisperBenchmarkResult{
			ModelName: r.name,
			LatencyMs: r.latency,
			Status:    status,
		})
	}
	fmt.Printf("[whisper-benchmark] Realtime factor: %.3f (%.1fx realtime)\n", realtimeFactor, 1.0/realtimeFactor)

	// Store benchmark report for later sending to server
	whisperBenchmarkReportMu.Lock()
	whisperBenchmarkReport = &server.WhisperBenchmarkReport{
		DeviceName:    deviceID,
		SelectedModel: selectedName,
		Results:       reportResults,
	}
	whisperBenchmarkReported = false // Allow reporting this new benchmark
	whisperBenchmarkReportMu.Unlock()

	whisperModelMu.Lock()
	whisperModel = selectedModel
	whisperModelNameAtomic.Store(selectedName)
	whisperRealtimeFactor = realtimeFactor
	whisperModelMu.Unlock()

	setWhisperBenchmarkStatus(fmt.Sprintf("Selected: %s (%dms)", selectedName, selectedLatency))
	lg.Infof("Whisper model selected: %s (%dms, realtimeFactor=%.3f)", selectedName, selectedLatency, realtimeFactor)

	// Save to config if callback provided
	if whisperSaveCallback != nil {
		whisperSaveCallback(selectedName, deviceID, WhisperBenchmarkIndex, realtimeFactor)
	}
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
		"climb via", "descend via", "arrival",
		"hold", "as published", "radial inbound", "minute legs", "left turns", "right turns",
		"expect further clearance",
	}

	// Add telephony and approaches for user-controlled tracks.
	// Collect fixes separately using map to dedupe.
	assignedApproaches := make(map[string]struct{})
	fixes := make(map[string]struct{})
	for _, trk := range state.Tracks {
		if state.UserControlsTrack(trk) && trk.IsAssociated() {
			callsign := string(trk.ADSBCallsign)
			tele := av.GetCallsignSpoken(callsign, trk.FlightPlan.CWTCategory)
			promptParts = append(promptParts, tele)

			// For GA callsigns (N-prefix), also add type+trailing3 variants
			if strings.HasPrefix(callsign, "N") && trk.FlightPlan.AircraftType != "" {
				typePronunciations := av.GetACTypePronunciations(trk.FlightPlan.AircraftType)
				if len(typePronunciations) > 0 {
					trailing3 := av.GetTrailing3Spoken(callsign)
					if trailing3 != "" {
						// Only use pronunciations without numbers to avoid callsign confusion
						for _, typeSpoken := range typePronunciations {
							if !strings.ContainsAny(typeSpoken, "0123456789") {
								promptParts = append(promptParts, typeSpoken+" "+trailing3)
							}
						}
					}
				}
			}

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

	// Collect active SIDs from departure airports
	activeSIDs := make(map[string]struct{})
	for _, dr := range state.DepartureRunways {
		if ap, ok := state.Airports[dr.Airport]; ok {
			if rwyRoutes, ok := ap.DepartureRoutes[dr.Runway]; ok {
				for _, route := range rwyRoutes {
					if route.SID != "" {
						activeSIDs[route.SID] = struct{}{}
					}
				}
			}
		}
	}
	for sid := range activeSIDs {
		promptParts = append(promptParts, av.GetSIDTelephony(sid))
	}

	// Collect active STARs from inbound flows
	activeSTARs := make(map[string]struct{})
	for _, flow := range state.InboundFlows {
		for _, arr := range flow.Arrivals {
			if arr.STAR != "" {
				activeSTARs[arr.STAR] = struct{}{}
			}
		}
	}
	for star := range activeSTARs {
		promptParts = append(promptParts, av.GetSTARTelephony(star))
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

// streamingSTT holds state for a transcription session.
type streamingSTT struct {
	transcriber *whisper.Transcriber
	state       SimState // Snapshot of state at start of streaming
}

// StartStreamingSTT begins a transcription session.
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

	st := whisper.NewTranscriber(whisperModel, &whisperModelMu, whisper.Options{
		Language:       "en",
		InitialPrompt:  makeWhisperPrompt(state),
		RealtimeFactor: whisperRealtimeFactor,
	})

	c.mu.Lock()
	c.streamingSTT = &streamingSTT{
		transcriber: st,
		state:       state,
	}
	// Hold speech playback during recording/processing
	c.sttActive = true
	c.transmissions.Hold()
	c.mu.Unlock()

	lg.Info("SPEECH: STT started, sttActive=true")

	return nil
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

		// Get final transcription from whisper (with audio if eval mode enabled)
		var finalText string
		var audioDuration time.Duration
		var audioSamples []float32

		if sttEvalEnabled {
			finalText, audioDuration, audioSamples = sttSession.transcriber.StopWithAudio()
		} else {
			finalText, audioDuration = sttSession.transcriber.Stop()
		}
		whisperDuration := time.Since(pttReleaseTime)

		lg.Infof("Whisper transcription completed in %v: %q", whisperDuration, finalText)

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
			shouldReport = util.SeqContainsAllFunc(slices.Values(c.recentWhisperDurations),
				func(d time.Duration) bool { return d >= slowPerformanceThreshold })
		}
		c.mu.Unlock()

		// Report slow performance outside the lock
		if shouldReport {
			c.reportSlowWhisperPerformance(lg)
		}

		// Run STT evaluation if enabled
		if sttEvalEnabled && len(audioSamples) > 0 {
			// Build aircraft context for STT evaluation
			evalAircraftCtx := c.sttTranscriber.BuildAircraftContext(&c.State.UserState, c.State.UserTCW)
			runSTTEvaluation(audioSamples, audioDuration, c.sttTranscriber, evalAircraftCtx, lg)
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
		callsign, command, _ := strings.Cut(decoded, " ")
		lg.Infof("STT command: %s %s", callsign, command)

		c.SetLastCommand(decoded)
		c.postSTTEvent(finalText, decoded, timingStr)

		// Track AGAIN responses for fallback callsign
		if command == "AGAIN" {
			c.mu.Lock()
			c.lastAgainCallsign = av.ADSBCallsign(callsign)
			c.mu.Unlock()
			lg.Infof("STT: set lastAgainCallsign=%s", callsign)
		} else {
			// Clear the last AGAIN callsign on successful command
			c.mu.Lock()
			if c.lastAgainCallsign != "" {
				lg.Infof("STT: clearing lastAgainCallsign (was %s)", c.lastAgainCallsign)
				c.lastAgainCallsign = ""
			}
			c.mu.Unlock()
		}

		// Execute the command via RPC (TTS readback will arrive via WebSocket)
		whisperModelName := GetWhisperModelName()
		c.RunAircraftCommands(AircraftCommandRequest{
			Callsign:          av.ADSBCallsign(callsign),
			Commands:          command,
			WhisperDuration:   totalDuration,
			AudioDuration:     audioDuration,
			WhisperTranscript: finalText,
			WhisperModel:      whisperModelName,
			AircraftContext:   aircraftCtx,
			STTDebugLogs:      debugLogs,
		}, func(message string, remainingInput string) {
			c.transmissions.Unhold()
			if message != "" {
				lg.Infof("STT command result: %s", message)
			}
		})

		// Report STT to remote server for logging (when running local sim)
		c.mu.Lock()
		remoteServer := c.remoteServer
		c.mu.Unlock()
		if remoteServer != nil {
			go remoteServer.Go(server.ReportSTTLogRPC, &server.STTLogArgs{
				Callsign:          callsign,
				Commands:          command,
				WhisperDuration:   totalDuration,
				AudioDuration:     audioDuration,
				WhisperTranscript: finalText,
				WhisperProcessor:  whisper.ProcessorDescription(),
				WhisperModel:      whisperModelName,
				AircraftContext:   aircraftCtx,
				STTDebugLogs:      debugLogs,
			}, nil, nil)
		}
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
				// Extract just the host part (without port) for WebSocket connections
				host, _, _ := net.SplitHostPort(hostname)
				if host == "" {
					host = hostname
				}
				ch <- &serverConnection{
					Server: &Server{
						RPCClient:           client,
						HaveTTS:             cr.HaveTTS,
						AvailableWXByTRACON: cr.AvailableWXByTRACON,
						name:                "Network (Multi-controller)",
						host:                host,
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

const sttEvalSampleRate = 16000

// runSTTEvaluation runs the audio through all whisper models and prints comparison results.
func runSTTEvaluation(audioSamples []float32, audioDuration time.Duration, sttTranscriber *stt.Transcriber, aircraftCtx map[string]stt.Aircraft, lg *log.Logger) {
	// Find all whisper models
	modelsDir := "resources/models"
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		fmt.Printf("\n[STT-EVAL] Failed to read models directory: %v\n", err)
		return
	}

	var modelFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".bin") {
			modelFiles = append(modelFiles, entry.Name())
		}
	}

	if len(modelFiles) == 0 {
		fmt.Printf("\n[STT-EVAL] No whisper models found in %s\n", modelsDir)
		return
	}

	sort.Strings(modelFiles)

	// Convert float32 audio to int16 for transcription API
	audioInt16 := make([]int16, len(audioSamples))
	for i, s := range audioSamples {
		// Clamp to int16 range
		val := s * 32768.0
		if val > 32767 {
			val = 32767
		} else if val < -32768 {
			val = -32768
		}
		audioInt16[i] = int16(val)
	}

	fmt.Printf("\n[STT-EVAL] Evaluating %.2fs audio against %d models...\n", audioDuration.Seconds(), len(modelFiles))

	type evalResult struct {
		model      string
		transcript string
		commands   string
		duration   time.Duration
	}
	var results []evalResult
	var firstTranscript string

	for _, modelFile := range modelFiles {
		modelPath := filepath.Join(modelsDir, modelFile)

		modelData, err := os.ReadFile(modelPath)
		if err != nil {
			fmt.Printf("[STT-EVAL] %-35s | error reading: %v\n", modelFile, err)
			continue
		}

		model, err := whisper.LoadModelFromBytes(modelData)
		if err != nil {
			fmt.Printf("[STT-EVAL] %-35s | error loading: %v\n", modelFile, err)
			continue
		}

		opts := whisper.Options{
			Language: "en",
			Threads:  0, // use all cores
		}

		start := time.Now()
		transcript, err := whisper.TranscribeWithModel(model, audioInt16, sttEvalSampleRate, 1, opts)
		elapsed := time.Since(start)

		model.Close()

		if err != nil {
			fmt.Printf("[STT-EVAL] %-35s | error transcribing: %v\n", modelFile, err)
			continue
		}

		if firstTranscript == "" && transcript != "" {
			firstTranscript = transcript
		}

		// Run through STT pipeline
		commands, _ := sttTranscriber.DecodeTranscript(aircraftCtx, transcript, "")

		results = append(results, evalResult{
			model:      modelFile,
			transcript: transcript,
			commands:   commands,
			duration:   elapsed,
		})
	}

	// Save audio as WAV
	wavFilename := sttEvalGenerateFilename(firstTranscript)
	if err := sttEvalWriteWAV(wavFilename, audioInt16); err != nil {
		fmt.Printf("[STT-EVAL] Failed to save WAV: %v\n", err)
	} else {
		fmt.Printf("[STT-EVAL] Audio saved to: %s\n", wavFilename)
	}

	// Print results table
	fmt.Printf("\n[STT-EVAL] Results:\n")
	for _, r := range results {
		fmt.Printf("[STT-EVAL] %-35s | %4dms | %-50s | %s\n", r.model, r.duration.Milliseconds(), r.transcript, r.commands)
	}
	fmt.Println()
}

func sttEvalGenerateFilename(transcript string) string {
	if transcript == "" {
		return fmt.Sprintf("recording_%d.wav", time.Now().Unix())
	}

	// Clean the transcript for use as filename
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s]`)
	clean := reg.ReplaceAllString(transcript, "")
	clean = strings.ToLower(strings.ReplaceAll(clean, " ", "_"))

	// Truncate if too long
	if len(clean) > 50 {
		clean = clean[:50]
	}
	clean = strings.TrimRight(clean, "_")

	if clean == "" {
		return fmt.Sprintf("recording_%d.wav", time.Now().Unix())
	}

	return clean + ".wav"
}

func sttEvalWriteWAV(filename string, samples []int16) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	numChannels := uint16(1)
	bitsPerSample := uint16(16)
	byteRate := uint32(sttEvalSampleRate) * uint32(numChannels) * uint32(bitsPerSample) / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := uint32(len(samples) * 2)
	fileSize := 36 + dataSize

	// RIFF header
	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, fileSize)
	f.Write([]byte("WAVE"))

	// fmt chunk
	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, uint32(16)) // chunk size
	binary.Write(f, binary.LittleEndian, uint16(1))  // PCM format
	binary.Write(f, binary.LittleEndian, numChannels)
	binary.Write(f, binary.LittleEndian, uint32(sttEvalSampleRate))
	binary.Write(f, binary.LittleEndian, byteRate)
	binary.Write(f, binary.LittleEndian, blockAlign)
	binary.Write(f, binary.LittleEndian, bitsPerSample)

	// data chunk
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, dataSize)

	// Write samples
	for _, sample := range samples {
		binary.Write(f, binary.LittleEndian, sample)
	}

	return nil
}

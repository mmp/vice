// client/stt.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
	"github.com/mmp/vice/util"
	"golang.org/x/sys/cpu"
)

///////////////////////////////////////////////////////////////////////////
// TransmissionManager

// TransmissionManager manages queuing and playback of radio transmissions.
// It centralizes the logic for playing MP3s in the correct order and handling
// playback state like holds after transmissions.
type TransmissionManager struct {
	mu           sync.Mutex
	queue        []queuedTransmission // pending transmissions
	playing      bool
	holdCount    int       // explicit holds (e.g., during STT recording/processing)
	holdUntil    time.Time // time-based hold for post-transmission pauses
	lastCallsign av.ADSBCallsign
	eventStream  *sim.EventStream
	lg           *log.Logger

	// Contact request management
	lastWasContact   bool
	contactRequested bool
}

// queuedTransmission holds a transmission ready for playback with pre-decoded PCM audio.
type queuedTransmission struct {
	Callsign       av.ADSBCallsign
	Type           av.RadioTransmissionType
	PCM            []int16 // Pre-decoded PCM audio
	PTTReleaseTime time.Time
}

// NewTransmissionManager creates a new TransmissionManager.
func NewTransmissionManager(lg *log.Logger) *TransmissionManager {
	return &TransmissionManager{
		lg: lg,
	}
}

// SetEventStream sets the event stream for posting TTS latency events.
// This must be called before Update() will post latency events.
func (tm *TransmissionManager) SetEventStream(es *sim.EventStream) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.eventStream = es
}

// EnqueueReadbackPCM adds a readback with pre-decoded PCM to the front of the queue (high priority).
func (tm *TransmissionManager) EnqueueReadbackPCM(callsign av.ADSBCallsign, ty av.RadioTransmissionType, pcm []int16) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Readback arrived - release the hold that was set when command was sent
	if tm.holdCount > 0 {
		tm.holdCount--
	}

	if len(pcm) == 0 {
		tm.lg.Warnf("Skipping readback for %s due to empty PCM", callsign)
		return
	}

	// Drop any pending initial contact for this aircraft; the controller
	// has already talked to them so the check-in is stale.
	tm.queue = slices.DeleteFunc(tm.queue, func(qt queuedTransmission) bool {
		return qt.Callsign == callsign && qt.Type == av.RadioTransmissionContact
	})

	qt := queuedTransmission{
		Callsign: callsign,
		Type:     ty,
		PCM:      pcm,
	}
	// Insert at front - readbacks have priority
	tm.queue = append([]queuedTransmission{qt}, tm.queue...)
}

// EnqueueTransmissionPCM adds a pilot transmission with pre-decoded PCM to the queue.
func (tm *TransmissionManager) EnqueueTransmissionPCM(callsign av.ADSBCallsign, ty av.RadioTransmissionType, pcm []int16) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(pcm) == 0 {
		tm.lg.Warnf("Skipping transmission for %s due to empty PCM", callsign)
		return
	}

	qt := queuedTransmission{
		Callsign: callsign,
		Type:     ty,
		PCM:      pcm,
	}
	tm.queue = append(tm.queue, qt)
}

// Update manages playback state, called each frame.
// It handles hold timeouts and initiates playback when appropriate.
func (tm *TransmissionManager) Update(p platform.Platform, paused, sttActive bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Don't play speech while paused or during STT recording
	if paused || sttActive {
		return
	}

	// Check if there's an explicit hold (e.g., STT processing)
	if tm.holdCount > 0 {
		return
	}

	// Check if we're in a time-based hold period (post-transmission pause)
	if time.Now().Before(tm.holdUntil) {
		return
	}

	// Can't play if already playing or nothing to play
	if tm.playing || len(tm.queue) == 0 {
		return
	}

	// Get next speech to play
	qt := tm.queue[0]
	tm.queue = tm.queue[1:]

	// Track whether this is a contact (vs readback)
	isContact := qt.Type == av.RadioTransmissionContact

	finishedCallback := func() {
		tm.mu.Lock()
		defer tm.mu.Unlock()

		tm.playing = false
		tm.lastCallsign = qt.Callsign
		tm.lastWasContact = isContact

		// Different hold times based on transmission type:
		// - After contact: 8 seconds (controller needs time to respond)
		// - After readback: 3 seconds (brief pause before next contact)
		if isContact {
			tm.holdUntil = time.Now().Add(8 * time.Second)
		} else {
			tm.holdUntil = time.Now().Add(3 * time.Second)
		}
	}

	// Enqueue pre-decoded PCM for playback
	if err := p.TryEnqueueSpeechPCM(qt.PCM, finishedCallback); err == nil {
		tm.playing = true
	}
}

// HoldAfterTransmission sets a hold period, used when the user initiates
// their own transmission and we should pause pilot transmissions briefly.
func (tm *TransmissionManager) HoldAfterTransmission() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.holdUntil = time.Now().Add(2 * time.Second)
}

// HoldAfterSilentContact sets a hold period after processing a contact without
// audio playback (when TTS is disabled). This maintains proper pacing of contacts.
func (tm *TransmissionManager) HoldAfterSilentContact(callsign av.ADSBCallsign) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.lastCallsign = callsign
	tm.lastWasContact = true
	// 8 seconds is the same hold time used after playing a contact transmission
	tm.holdUntil = time.Now().Add(8 * time.Second)
}

// Hold increments the hold counter, preventing playback until Unhold is called.
// Used during STT recording/processing to prevent speech playback.
func (tm *TransmissionManager) Hold() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.holdCount++
}

// Unhold decrements the hold counter. Playback resumes when count reaches zero.
func (tm *TransmissionManager) Unhold() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.holdCount > 0 {
		tm.holdCount--
	}
}

// LastTransmissionCallsign returns the callsign of the last played transmission.
func (tm *TransmissionManager) LastTransmissionCallsign() av.ADSBCallsign {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.lastCallsign
}

// IsPlaying returns true if a transmission is currently playing.
func (tm *TransmissionManager) IsPlaying() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.playing
}

// ShouldRequestContact returns true if the client should request a contact from the server.
// It checks that we're not playing, not held, queue is empty, and no request is pending.
func (tm *TransmissionManager) ShouldRequestContact() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.contactRequested || tm.playing || tm.holdCount > 0 || len(tm.queue) > 0 {
		return false
	}

	return time.Now().After(tm.holdUntil)
}

// SetContactRequested marks that we've sent a contact request and are waiting.
func (tm *TransmissionManager) SetContactRequested(requested bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.contactRequested = requested
}

// IsContactRequested returns true if we're waiting for a contact response.
func (tm *TransmissionManager) IsContactRequested() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.contactRequested
}

///////////////////////////////////////////////////////////////////////////
// Whisper integration

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

// SelectWhisperModel directly selects a whisper model without benchmarking.
// This is used when the user manually chooses a model from the settings dropdown.
func SelectWhisperModel(lg *log.Logger, modelName string,
	saveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)) {
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
func PreloadWhisperModel(lg *log.Logger, cachedModelName, cachedDeviceID string, cachedBenchmarkIndex int,
	cachedRealtimeFactor float64, saveCallback func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64)) {
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
		"expect further clearance", "mach", "say mach", "maintain mach",
	}

	// Add telephony and approaches for user-controlled tracks.
	// Collect fixes separately using map to dedupe.
	assignedApproaches := make(map[string]struct{})
	fixes := make(map[string]struct{})
	for _, trk := range state.Tracks {
		if state.UserControlsTrack(trk) && trk.IsAssociated() {
			callsign := string(trk.ADSBCallsign)
			tele := av.GetCallsignSpoken(callsign, trk.CWTCategory)
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
				if appr.Runway == ar.Runway.Base() {
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

	// Add current ATIS letters so whisper recognizes "information <letter>"
	for _, letter := range state.ATISLetter {
		if nato, ok := av.NATOPhonetic[letter]; ok {
			promptParts = append(promptParts, "information "+nato)
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

		// Get final transcription from whisper
		finalText, audioDuration := sttSession.transcriber.Stop()
		whisperDuration := time.Since(pttReleaseTime)

		lg.Infof("Whisper transcription completed in %v: %q", whisperDuration, finalText)

		c.mu.Lock()
		c.LastWhisperDurationMs = whisperDuration.Milliseconds()
		c.LastTranscription = finalText

		c.mu.Unlock()

		if finalText == "" || finalText == "[BLANK_AUDIO]" {
			c.transmissions.Unhold()
			c.postSTTEvent("", "", "")
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

		if err != nil {
			lg.Infof("STT decode error: %v", err)
			c.transmissions.Unhold()
			c.postSTTEvent(finalText, "Error: "+err.Error(), timingStr)
			return
		}

		whisperModelName := GetWhisperModelName()

		var callsign, command string
		if decoded == "" {
			lg.Infof("STT: no command decoded from %q", finalText)
			c.transmissions.Unhold()
			c.postSTTEvent(finalText, decoded, timingStr)
		} else {
			// Parse callsign and command from decoded result
			callsign, command, _ = strings.Cut(decoded, " ")
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
		}

		// Report STT to server for logging, including no-command cases.
		// For local sim clients with a remote server, send to remoteServer.
		// For no-command cases without a remote server (remote sim client),
		// send directly since the dispatcher won't log it.
		c.mu.Lock()
		remoteServer := c.remoteServer
		c.mu.Unlock()
		sttLogArgs := &server.STTLogArgs{
			Callsign:          callsign,
			Commands:          command,
			WhisperDuration:   totalDuration,
			AudioDuration:     audioDuration,
			WhisperTranscript: finalText,
			WhisperProcessor:  whisper.ProcessorDescription(),
			WhisperModel:      whisperModelName,
			AircraftContext:   aircraftCtx,
			STTDebugLogs:      debugLogs,
		}
		if remoteServer != nil {
			go remoteServer.Go(server.ReportSTTLogRPC, sttLogArgs, nil, nil)
		} else if decoded == "" {
			go c.client.Go(server.ReportSTTLogRPC, sttLogArgs, nil, nil)
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

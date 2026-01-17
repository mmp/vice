package autowhisper

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	whisper "github.com/mmp/vice/autowhisper/internal/whisper"
)

// StreamingResult contains a transcription result from the streaming transcriber.
type StreamingResult struct {
	Text      string
	IsFinal   bool
	Timestamp time.Time
}

// StreamingTranscriber handles real-time audio streaming to whisper.
// It uses a sliding window approach to process audio incrementally.
type StreamingTranscriber struct {
	model   *Model
	modelMu *sync.Mutex // External mutex to serialize whisper access
	opts    Options

	// Audio buffer
	audioBuffer []float32
	bufferMu    sync.Mutex

	// Streaming state
	stopChan   chan struct{}
	loopDone   chan struct{} // Signals processLoop has exited
	resultChan chan StreamingResult
	stopped    bool
	stoppedMu  sync.Mutex

	// Timing configuration (in samples at 16kHz)
	stepSamples   int // How often to process
	lengthSamples int // Context window
	keepSamples   int // Overlap to keep

	// Previous results for deduplication
	lastText string

	// Accumulated audio for final processing
	allAudio []float32
}

// NewStreamingTranscriber creates a new streaming transcriber.
// The modelMu mutex is used to serialize access to the whisper model.
// Parameters are tuned for short ATC commands:
//   - stepMs: 500ms - process every 500ms
//   - lengthMs: 3000ms - context window of 3 seconds
//   - keepMs: 200ms - overlap between windows
func NewStreamingTranscriber(m *Model, modelMu *sync.Mutex, opts Options) *StreamingTranscriber {
	const sampleRate = 16000
	stepMs := 500
	lengthMs := 3000
	keepMs := 200

	return &StreamingTranscriber{
		model:         m,
		modelMu:       modelMu,
		opts:          opts,
		stopChan:      make(chan struct{}),
		loopDone:      make(chan struct{}),
		resultChan:    make(chan StreamingResult, 10),
		stepSamples:   stepMs * sampleRate / 1000,
		lengthSamples: lengthMs * sampleRate / 1000,
		keepSamples:   keepMs * sampleRate / 1000,
	}
}

// Start begins the streaming session and returns a channel for results.
// The channel will receive intermediate results as audio is processed.
// Call Stop() to end the session and get the final result.
func (st *StreamingTranscriber) Start() <-chan StreamingResult {
	go st.processLoop()
	return st.resultChan
}

// AddSamples adds new audio samples to the streaming buffer.
// This should be called from the audio capture callback.
func (st *StreamingTranscriber) AddSamples(samples []int16) {
	// Convert int16 to float32
	floats := make([]float32, len(samples))
	for i, s := range samples {
		floats[i] = float32(s) / 32768.0
	}

	st.bufferMu.Lock()
	st.audioBuffer = append(st.audioBuffer, floats...)
	st.allAudio = append(st.allAudio, floats...)
	st.bufferMu.Unlock()
}

// Stop ends the streaming session and returns the final transcription.
// This performs a final transcription pass on all accumulated audio.
func (st *StreamingTranscriber) Stop() string {
	st.stoppedMu.Lock()
	if st.stopped {
		st.stoppedMu.Unlock()
		return st.lastText
	}
	st.stopped = true
	st.stoppedMu.Unlock()

	close(st.stopChan)

	// Wait for processLoop to fully exit before we call transcribe,
	// since whisper isn't safe for concurrent use
	<-st.loopDone

	// Final transcription on all accumulated audio
	st.bufferMu.Lock()
	allAudio := st.allAudio
	st.bufferMu.Unlock()

	if len(allAudio) == 0 {
		close(st.resultChan)
		return ""
	}

	finalText := st.transcribe(allAudio)

	// Send final result
	st.resultChan <- StreamingResult{
		Text:      finalText,
		IsFinal:   true,
		Timestamp: time.Now(),
	}
	close(st.resultChan)

	return finalText
}

// processLoop runs the sliding window transcription loop.
func (st *StreamingTranscriber) processLoop() {
	defer close(st.loopDone) // Signal that we've exited

	ticker := time.NewTicker(time.Duration(st.stepSamples*1000/16000) * time.Millisecond)
	defer ticker.Stop()

	var previousAudio []float32

	for {
		select {
		case <-st.stopChan:
			return
		case <-ticker.C:
			st.bufferMu.Lock()
			if len(st.audioBuffer) < st.stepSamples {
				st.bufferMu.Unlock()
				continue
			}

			// Take new samples from buffer
			newSamples := st.audioBuffer
			st.audioBuffer = nil
			st.bufferMu.Unlock()

			// Build audio window: previous overlap + new samples
			audioWindow := make([]float32, 0, st.lengthSamples)
			if len(previousAudio) > 0 {
				takeFromPrev := min(len(previousAudio), st.keepSamples)
				audioWindow = append(audioWindow, previousAudio[len(previousAudio)-takeFromPrev:]...)
			}
			audioWindow = append(audioWindow, newSamples...)

			// Trim to max length
			if len(audioWindow) > st.lengthSamples {
				audioWindow = audioWindow[len(audioWindow)-st.lengthSamples:]
			}

			// Run transcription
			text := st.transcribe(audioWindow)

			// Only emit if text changed and is non-empty
			if text != st.lastText && text != "" && text != "[BLANK_AUDIO]" {
				st.lastText = text
				select {
				case st.resultChan <- StreamingResult{
					Text:      text,
					IsFinal:   false,
					Timestamp: time.Now(),
				}:
				default:
					// Channel full, skip this update
				}
			}

			// Save for next iteration's overlap
			previousAudio = audioWindow
		}
	}
}

// transcribe runs whisper on the given audio samples.
func (st *StreamingTranscriber) transcribe(audio []float32) string {
	if st.model == nil || st.model.model == nil || len(audio) == 0 {
		return ""
	}

	transcribeStart := time.Now()

	// Acquire mutex to serialize whisper access
	mutexStart := time.Now()
	st.modelMu.Lock()
	defer st.modelMu.Unlock()
	mutexWait := time.Since(mutexStart)

	ctxStart := time.Now()
	ctx, err := st.model.model.NewContext()
	if err != nil {
		return ""
	}
	defer ctx.Close() // Free C-allocated params
	ctxCreate := time.Since(ctxStart)

	// Configure context
	if st.opts.Threads > 0 {
		ctx.SetThreads(uint(st.opts.Threads))
	} else {
		ctx.SetThreads(uint(runtime.NumCPU()))
	}
	ctx.SetTranslate(st.opts.Translate)
	ctx.SetSplitOnWord(true)
	ctx.SetTokenTimestamps(st.opts.TokenTimestamps)
	if st.opts.MaxTokensPerSegment > 0 {
		ctx.SetMaxTokensPerSegment(st.opts.MaxTokensPerSegment)
	}
	if strings.TrimSpace(st.opts.InitialPrompt) != "" {
		ctx.SetInitialPrompt(st.opts.InitialPrompt)
	}

	// Disable temperature fallback to prevent multiple decode passes.
	// Default whisper retries at increasingly higher temperatures (0.2, 0.4, etc.)
	// when confidence is low, which can cause 2-10x slowdowns.
	// For ATC commands we expect clear speech, so disable retries.
	ctx.SetTemperatureFallback(-1.0)

	// Language selection
	lang := strings.TrimSpace(st.opts.Language)
	if lang == "" {
		lang = "auto"
	}
	if lang != "auto" {
		if err := ctx.SetLanguage(lang); err != nil {
			return ""
		}
	}

	// Process with segment callback to collect results
	var segments []string
	segmentCb := func(seg whisper.Segment) {
		segments = append(segments, seg.Text)
	}

	processStart := time.Now()
	if err := ctx.Process(audio, nil, segmentCb, nil); err != nil {
		return ""
	}
	processTime := time.Since(processStart)

	totalTime := time.Since(transcribeStart)
	audioDuration := time.Duration(len(audio)) * time.Second / 16000

	// Log timing details for slow transcriptions (>500ms)
	if totalTime > 500*time.Millisecond {
		logWhisperTiming("SLOW whisper: total=%v (mutex=%v, ctx=%v, process=%v) audio=%v samples=%d",
			totalTime, mutexWait, ctxCreate, processTime, audioDuration, len(audio))
	}

	return strings.TrimSpace(strings.Join(segments, " "))
}

// logWhisperTiming logs whisper timing information. Can be enabled via build tag or always-on for debugging.
func logWhisperTiming(format string, args ...interface{}) {
	// Always log for now to diagnose the issue
	println("[whisper-timing]", fmt.Sprintf(format, args...))
}

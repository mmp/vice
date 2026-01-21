package autowhisper

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	whisper "github.com/mmp/vice/autowhisper/internal/whisper"
)

// Transcriber accumulates audio during PTT and transcribes on Stop().
// This replaces the previous streaming approach which was wasteful:
// it processed 3s windows every 500ms during PTT but discarded all
// intermediate results and reprocessed ALL audio on PTT release anyway.
type Transcriber struct {
	model   *Model
	modelMu *sync.Mutex // External mutex to serialize whisper access
	opts    Options

	// Audio buffer
	audio   []float32
	audioMu sync.Mutex
}

// NewTranscriber creates a new transcriber for accumulating audio.
// The modelMu mutex is used to serialize access to the whisper model.
func NewTranscriber(m *Model, modelMu *sync.Mutex, opts Options) *Transcriber {
	return &Transcriber{
		model:   m,
		modelMu: modelMu,
		opts:    opts,
	}
}

// AddSamples adds new audio samples to the buffer.
// This should be called from the audio capture callback.
func (t *Transcriber) AddSamples(samples []int16) {
	// Convert int16 to float32
	floats := make([]float32, len(samples))
	for i, s := range samples {
		floats[i] = float32(s) / 32768.0
	}

	t.audioMu.Lock()
	t.audio = append(t.audio, floats...)
	t.audioMu.Unlock()
}

// Stop ends the recording session and returns the final transcription
// along with the duration of the recorded audio.
func (t *Transcriber) Stop() (text string, audioDuration time.Duration) {
	t.audioMu.Lock()
	audio := t.audio
	t.audio = nil // Clear for potential reuse
	t.audioMu.Unlock()

	if len(audio) == 0 {
		return "", 0
	}

	// Calculate audio duration from sample count (16kHz sample rate)
	audioDuration = time.Duration(len(audio)) * time.Second / 16000

	return t.transcribe(audio), audioDuration
}

// transcribe runs whisper on the given audio samples.
func (t *Transcriber) transcribe(audio []float32) string {
	if t.model == nil || t.model.model == nil || len(audio) == 0 {
		return ""
	}

	transcribeStart := time.Now()

	// Acquire mutex to serialize whisper access
	mutexStart := time.Now()
	t.modelMu.Lock()
	defer t.modelMu.Unlock()
	mutexWait := time.Since(mutexStart)

	ctxStart := time.Now()
	ctx, err := t.model.model.NewContext()
	if err != nil {
		return ""
	}
	defer ctx.Close() // Free C-allocated params
	ctxCreate := time.Since(ctxStart)

	// Configure context
	if t.opts.Threads > 0 {
		ctx.SetThreads(uint(t.opts.Threads))
	} else {
		ctx.SetThreads(uint(runtime.NumCPU()))
	}
	ctx.SetTranslate(t.opts.Translate)
	ctx.SetSplitOnWord(true)
	ctx.SetTokenTimestamps(t.opts.TokenTimestamps)
	if t.opts.MaxTokensPerSegment > 0 {
		ctx.SetMaxTokensPerSegment(t.opts.MaxTokensPerSegment)
	}
	if strings.TrimSpace(t.opts.InitialPrompt) != "" {
		ctx.SetInitialPrompt(t.opts.InitialPrompt)
	}

	// Disable temperature fallback to prevent multiple decode passes.
	// Default whisper retries at increasingly higher temperatures (0.2, 0.4, etc.)
	// when confidence is low, which can cause 2-10x slowdowns.
	// For ATC commands we expect clear speech, so disable retries.
	ctx.SetTemperatureFallback(-1.0)

	// Enable beam search for fast hardware (bounded ~1.5x impact on decoder,
	// which is only 20-40% of total compute, so ~1.1-1.2x total impact).
	// RealtimeFactor < 0.05 means the model processes audio at 20x+ realtime.
	if t.opts.RealtimeFactor > 0 && t.opts.RealtimeFactor < 0.05 {
		ctx.SetBeamSize(3)
	}

	// Language selection
	lang := strings.TrimSpace(t.opts.Language)
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

// logWhisperTiming logs whisper timing information.
func logWhisperTiming(format string, args ...any) {
	println("[whisper-timing]", fmt.Sprintf(format, args...))
}

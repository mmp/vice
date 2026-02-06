// client/speech.go
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
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// kokoroVoiceNames maps voice IDs to their names.
// Prefixes: af/am = American English, bf/bm = British English, ef/em = Spanish,
// ff = French, hf/hm = Hindi, if/im = Italian, jf/jm = Japanese, pf/pm = Portuguese, zf/zm = Chinese
var kokoroVoiceNames = []string{
	"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica", "af_kore", "af_nicole", "af_nova",
	"af_river", "af_sarah", "af_sky", "am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
	"am_michael", "am_onyx", "am_puck", "am_santa", "bf_alice", "bf_emma", "bf_isabella", "bf_lily",
	"bm_daniel", "bm_fable", "bm_george", "bm_lewis", "ef_dora", "em_alex", "ff_siwis", "hf_alpha",
	"hf_beta", "hm_omega", "hm_psi", "if_sara", "im_nicola", "jf_alpha", "jf_gongitsune", "jf_nezumi",
	"jf_tebukuro", "jm_kumo", "pf_dora", "pm_alex", "pm_santa", "zf_xiaobei", "zf_xiaoni", "zf_xiaoxiao",
	"zf_xiaoyi", "zm_yunjian", "zm_yunxi", "zm_yunxia", "zm_yunyang",
}

// LocalTTS wraps sherpa-onnx Kokoro TTS for local synthesis.
type LocalTTS struct {
	tts     *sherpa.OfflineTts
	mu      sync.Mutex
	lg      *log.Logger
	loadErr error
	done    chan struct{}
}

// Global TTS instance
var (
	globalTTS     *LocalTTS
	globalTTSOnce sync.Once
)

// GetLocalTTS returns the global TTS instance.
func GetLocalTTS() *LocalTTS {
	return globalTTS
}

func PreloadTTSModel(lg *log.Logger) {
	globalTTSOnce.Do(func() {
		globalTTS = &LocalTTS{
			lg:   lg,
			done: make(chan struct{}),
		}
		go globalTTS.load()
	})
}

func (t *LocalTTS) load() {
	defer close(t.done)
	defer t.lg.CatchAndReportCrash()

	t.lg.Info("Loading Kokoro TTS model...")

	// Check if model files exist
	modelPath := "models/kokoro-multi-lang-v1_0"
	modelFile := modelPath + "/model.onnx"

	if !util.ResourceExists(modelFile) {
		t.loadErr = fmt.Errorf("TTS model not found: %s", modelFile)
		t.lg.Warnf("TTS unavailable: %v", t.loadErr)
		return
	}

	// Get the filesystem path to the model directory
	// sherpa-onnx needs actual file paths, not embedded data
	modelDir := util.GetResourcePath(modelPath)

	config := &sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Kokoro: sherpa.OfflineTtsKokoroModelConfig{
				Model:   modelDir + "/model.onnx",
				Voices:  modelDir + "/voices.bin",
				Tokens:  modelDir + "/tokens.txt",
				DataDir: modelDir + "/espeak-ng-data",
				DictDir: modelDir + "/dict",
				Lexicon: modelDir + "/lexicon-us-en.txt",
				Lang:    "en-us",
			},
			NumThreads: runtime.NumCPU(),
			Debug:      0,
		},
		MaxNumSentences: 1,
	}

	tts := sherpa.NewOfflineTts(config)
	if tts == nil {
		t.loadErr = errors.New("failed to create TTS engine")
		t.lg.Errorf("TTS unavailable: %v", t.loadErr)
		return
	}

	t.mu.Lock()
	t.tts = tts
	t.mu.Unlock()

	t.lg.Info("Kokoro TTS model loaded successfully")
}

// CheckTTSLoadError returns any TTS loading error if loading has completed.
// Returns (nil, false) if loading is still in progress.
// Returns (err, true) if loading completed (err may be nil if successful).
// This is safe to call from the main thread for UI error display.
func CheckTTSLoadError() (error, bool) {
	if globalTTS == nil {
		return nil, false
	}
	select {
	case <-globalTTS.done:
		return globalTTS.loadErr, true
	default:
		return nil, false
	}
}

// SynthesizeTTS generates PCM audio from text using the specified voice.
// voiceName should be one of the Kokoro voice names (e.g., "af_alloy", "bf_alice").
// Returns PCM samples in int16 format at the audio output sample rate.
// Returns nil without error if the text is empty or if TTS could not be initialized.
func SynthesizeTTS(text string, voice string) ([]int16, error) {
	start := time.Now()
	defer func() {
		fmt.Printf("%s: %q in %s\n", voice, text, time.Since(start))
	}()
	return globalTTS.Synthesize(text, voice)
}

func (t *LocalTTS) Synthesize(text string, voice string) ([]int16, error) {
	// Wait for loading to complete
	<-t.done

	t.mu.Lock()
	tts := t.tts
	t.mu.Unlock()

	if tts == nil || text == "" { // TTS load error or empty text; just return
		return nil, nil
	}

	voiceID := slices.Index(kokoroVoiceNames, voice)
	if voiceID < 0 {
		return nil, fmt.Errorf("%s: invalid voice", voice)
	}

	t.mu.Lock()
	audio := tts.Generate(text, voiceID, t.voiceSpeed(voice))
	t.mu.Unlock()

	if audio == nil || len(audio.Samples) == 0 {
		return nil, fmt.Errorf("TTS generation failed for text: %q", text)
	}

	// Convert float32 samples to int16 and resample to output rate
	pcm := t.convertAndResample(audio.Samples, audio.SampleRate)

	addNoise(pcm)

	return pcm, nil
}

// voiceSpeed returns the TTS speed multiplier for the given voice name.
func (t *LocalTTS) voiceSpeed(voice string) float32 {
	if strings.HasPrefix(voice, "zf_") || strings.HasPrefix(voice, "zm_") {
		return 1.3 // Chinese voices work better with a slower speed
	} else if voice == "af_jessica" {
		return 1.5
	} else {
		return 1.75
	}
}

// convertAndResample converts float32 samples to int16 and resamples to the
// audio output rate expected by the platform.
func (t *LocalTTS) convertAndResample(samples []float32, sampleRate int) []int16 {
	// Resample if needed
	if sampleRate != platform.AudioSampleRate {
		samples = resampleAudio(samples, sampleRate, platform.AudioSampleRate)
	}

	// Convert to int16
	pcm := make([]int16, len(samples))
	for i, s := range samples {
		// Clamp to [-1, 1] range and convert to int16
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		pcm[i] = int16(s * 32767.0)
	}

	return pcm
}

// resampleAudio resamples audio from srcRate to dstRate using linear interpolation.
func resampleAudio(samples []float32, srcRate, dstRate int) []float32 {
	if srcRate == dstRate {
		return samples
	}

	ratio := float64(dstRate) / float64(srcRate)
	outLen := int(float64(len(samples)) * ratio)
	out := make([]float32, outLen)

	for i := range out {
		srcPos := float64(i) / ratio
		srcIdx := int(srcPos)
		frac := float32(srcPos - float64(srcIdx))

		if srcIdx+1 < len(samples) {
			out[i] = samples[srcIdx]*(1-frac) + samples[srcIdx+1]*frac
		} else if srcIdx < len(samples) {
			out[i] = samples[srcIdx]
		}
	}

	return out
}

// addNoise applies radio-style noise and static to PCM audio samples.
func addNoise(pcm []int16) {
	r := rand.Make()
	amp := 256 + r.Intn(512)
	freqs := []int{10 + r.Intn(5), 18 + r.Intn(5)}
	noises := []int{0, 0}
	for i, v := range pcm {
		n := 0
		for j := range freqs {
			if i%freqs[j] == 0 {
				noises[j] = -amp + r.Intn(2*amp)
			}
			n += noises[j]
		}

		pcm[i] = int16(math.Clamp(n+int(v), -32768, 32767))
	}

	// Random squelch
	if false && r.Float32() < 0.1 {
		length := platform.AudioSampleRate/2 + r.Intn(platform.AudioSampleRate/4)
		freqs := []int{100 + r.Intn(50), 500 + r.Intn(250), 1500 + r.Intn(500), 4000 + r.Intn(1000)}
		start := r.Intn(4 * len(pcm) / 5)
		const amp = 20000

		n := min(len(pcm), start+length)
		for i := start; i < n; i++ {
			sq := -amp + r.Intn(2*amp)
			for _, fr := range freqs {
				sq += int(amp * math.Sin(float32(fr*i)*2*3.14159/platform.AudioSampleRate))
			}
			sq /= 1 + len(freqs) // normalize
			pcm[i] = int16(math.Clamp(int(pcm[i]/4)+sq, -32768, 32767))
		}
	}
}

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

		// Post latency event if this is from an STT command (PTTReleaseTime is set)
		if !qt.PTTReleaseTime.IsZero() {
			latencyMs := int(time.Since(qt.PTTReleaseTime).Milliseconds())
			if tm.eventStream != nil {
				tm.eventStream.Post(sim.Event{
					Type:         sim.TTSPlaybackStartedEvent,
					ADSBCallsign: qt.Callsign,
					TTSLatencyMs: latencyMs,
				})
			}
		}
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
// It also returns true slightly early (2s before hold expires) to hide TTS latency.
func (tm *TransmissionManager) ShouldRequestContact() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.contactRequested || tm.playing || tm.holdCount > 0 || len(tm.queue) > 0 {
		return false
	}

	// Request early to hide latency: when hold expires in less than 2s
	// (or has already expired)
	return time.Until(tm.holdUntil) < 2*time.Second
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

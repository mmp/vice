// tts/engine.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package tts

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
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

// localTTS wraps two Kokoro TTS instances for local synthesis.
// The readback instance runs at normal priority; the contact instance runs
// at lower OS thread priority so readbacks aren't blocked by contacts.
type localTTS struct {
	sharedWeights    *SharedWeights
	readbackTTS      *OfflineTts
	contactTTS       *OfflineTts
	readbackMu       sync.Mutex
	contactMu        sync.Mutex
	lg               *log.Logger
	loadErr          error
	done             chan struct{} // closed when loading completes
	targetSampleRate int
}

// Global TTS instance
var (
	globalTTS     *localTTS
	globalTTSOnce sync.Once
)

func PreloadTTSModel(lg *log.Logger, targetSampleRate int) {
	globalTTSOnce.Do(func() {
		globalTTS = &localTTS{
			lg:               lg,
			done:             make(chan struct{}),
			targetSampleRate: targetSampleRate,
		}
		go globalTTS.load()
	})
}

func (t *localTTS) load() {
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

	// Load model weights once for sharing between instances
	shared := NewSharedWeights(modelDir+"/model.onnx", modelDir+"/voices.bin")
	if shared == nil {
		t.loadErr = errors.New("failed to load TTS model weights")
		t.lg.Errorf("TTS unavailable: %v", t.loadErr)
		return
	}

	makeConfig := func(lowPriority bool) Config {
		return Config{
			ModelPath:    modelDir + "/model.onnx",
			VoicesPath:   modelDir + "/voices.bin",
			TokensPath:   modelDir + "/tokens.txt",
			DataDir:      modelDir + "/espeak-ng-data",
			DictDir:      modelDir + "/dict",
			LexiconPath:  modelDir + "/lexicon-us-en.txt",
			Lang:         "en-us",
			NumThreads:   runtime.NumCPU(),
			LowPriority:  lowPriority,
			MaxSentences: 1,
		}
	}

	readback := NewOfflineTts(makeConfig(false), shared)
	contact := NewOfflineTts(makeConfig(true), shared)
	if readback == nil || contact == nil {
		t.loadErr = errors.New("failed to create TTS engine(s)")
		t.lg.Errorf("TTS unavailable: %v", t.loadErr)
		if readback != nil {
			readback.Delete()
		}
		if contact != nil {
			contact.Delete()
		}
		shared.Delete()
		return
	}

	t.sharedWeights = shared
	t.readbackTTS = readback
	t.contactTTS = contact
	t.lg.Info("Kokoro TTS models loaded successfully")
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

func (t *localTTS) synthesize(mu *sync.Mutex, ttsEngine *OfflineTts,
	text, voice string) ([]int16, error) {
	<-t.done

	if ttsEngine == nil || text == "" {
		return nil, nil
	}

	voiceID := slices.Index(kokoroVoiceNames, voice)
	if voiceID < 0 {
		return nil, fmt.Errorf("%s: invalid voice", voice)
	}

	mu.Lock()
	defer mu.Unlock()

	audio := ttsEngine.Generate(text, voiceID, t.voiceSpeed(voice))
	if audio == nil || len(audio.Samples) == 0 {
		return nil, fmt.Errorf("TTS generation failed for text: %q", text)
	}

	pcm := t.convertAndResample(audio.Samples, audio.SampleRate)
	addNoise(pcm, t.targetSampleRate)
	return pcm, nil
}

// synthesizeReadback generates speech using the high-priority TTS instance.
func (t *localTTS) synthesizeReadback(text, voice string) ([]int16, error) {
	return t.synthesize(&t.readbackMu, t.readbackTTS, text, voice)
}

// synthesizeContact generates speech using the low-priority TTS instance.
func (t *localTTS) synthesizeContact(text, voice string) ([]int16, error) {
	return t.synthesize(&t.contactMu, t.contactTTS, text, voice)
}

// SynthesizeReadbackTTS generates PCM audio for a readback using the
// high-priority TTS instance.
func SynthesizeReadbackTTS(text, voice string) ([]int16, error) {
	start := time.Now()
	defer func() {
		fmt.Printf("readback %s: %q in %s\n", voice, text, time.Since(start))
	}()
	return globalTTS.synthesizeReadback(text, voice)
}

// SynthesizeContactTTS generates PCM audio for a contact using the
// low-priority TTS instance.
func SynthesizeContactTTS(text, voice string) ([]int16, error) {
	start := time.Now()
	defer func() {
		fmt.Printf("contact %s: %q in %s\n", voice, text, time.Since(start))
	}()
	return globalTTS.synthesizeContact(text, voice)
}

// voiceSpeed returns the TTS speed multiplier for the given voice name.
func (t *localTTS) voiceSpeed(voice string) float32 {
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
func (t *localTTS) convertAndResample(samples []float32, sampleRate int) []int16 {
	// Resample if needed
	if sampleRate != t.targetSampleRate {
		samples = resampleAudio(samples, sampleRate, t.targetSampleRate)
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
func addNoise(pcm []int16, sampleRate int) {
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
		length := sampleRate/2 + r.Intn(sampleRate/4)
		freqs := []int{100 + r.Intn(50), 500 + r.Intn(250), 1500 + r.Intn(500), 4000 + r.Intn(1000)}
		start := r.Intn(4 * len(pcm) / 5)
		const amp = 20000

		n := min(len(pcm), start+length)
		for i := start; i < n; i++ {
			sq := -amp + r.Intn(2*amp)
			for _, fr := range freqs {
				sq += int(amp * math.Sin(float32(fr*i)*2*3.14159/float32(sampleRate)))
			}
			sq /= 1 + len(freqs) // normalize
			pcm[i] = int16(math.Clamp(int(pcm[i]/4)+sq, -32768, 32767))
		}
	}
}

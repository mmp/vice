package autowhisper

import (
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"

	whisper "github.com/mmp/vice/autowhisper/internal/whisper"
)

// Options configures the transcription behavior.
type Options struct {
	// Language to use for speech recognition. Use "auto" to auto-detect (default).
	Language string
	// Translate to English if supported by model.
	Translate bool
	// Number of threads to use. If 0, uses runtime.NumCPU().
	Threads int
	// Enable word-level splitting for more granular segments.
	SplitOnWord bool
	// Initial system prompt to bias decoding.
	InitialPrompt string
	// Enable token timestamps (may reduce speed).
	TokenTimestamps bool
	// Max tokens per segment (0 = no limit).
	MaxTokensPerSegment uint
}

// TranscribeFile loads the model at modelPath, reads the WAV at audioPath,
// converts it to 16 kHz mono if needed, and returns the full transcription text.
//
// Requirements:
// - audioPath must be a WAV file. Other formats should be converted upstream.
func TranscribeFile(modelPath, audioPath string, opts Options) (string, error) {
	// Load model
	model, err := whisper.New(modelPath)
	if err != nil {
		return "", err
	}
	defer model.Close()

	// Create context
	ctx, err := model.NewContext()
	if err != nil {
		return "", err
	}

	// Configure context
	if opts.Threads > 0 {
		ctx.SetThreads(uint(opts.Threads))
	} else {
		ctx.SetThreads(uint(runtime.NumCPU()))
	}
	ctx.SetTranslate(opts.Translate)
	ctx.SetSplitOnWord(opts.SplitOnWord)
	ctx.SetTokenTimestamps(opts.TokenTimestamps)
	if opts.MaxTokensPerSegment > 0 {
		ctx.SetMaxTokensPerSegment(opts.MaxTokensPerSegment)
	}
	if strings.TrimSpace(opts.InitialPrompt) != "" {
		ctx.SetInitialPrompt(opts.InitialPrompt)
	}

	// Language selection
	lang := strings.TrimSpace(opts.Language)
	if lang == "" {
		lang = "auto"
	}
	if lang != "auto" {
		if err := ctx.SetLanguage(lang); err != nil {
			return "", err
		}
	}

	// Load audio and convert to 16k mono []float32
	pcm, err := ReadWavAsFloat32Mono16k(audioPath)
	if err != nil {
		return "", err
	}
	if len(pcm) == 0 {
		return "", errors.New("empty audio after conversion")
	}

	// Process
	if err := ctx.Process(pcm, nil, nil, nil); err != nil {
		return "", err
	}

	// Collect segments
	var b strings.Builder
	for {
		seg, err := ctx.NextSegment()
		if err != nil {
			if errors.Is(err, fmt.Errorf("%w", err)) { // keep linter happy; io.EOF checked below via string
				// no-op
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(seg.Text)
	}
	return strings.TrimSpace(b.String()), nil
}

// TranscribePCM16 takes raw PCM16 samples (interleaved if stereo), automatically converts
// them to 16 kHz mono, and returns the transcription text.
// inSampleRate is the input sample rate in Hz, inChannels must be 1 or 2.
func Transcribe(modelData []byte, pcm []int16, inSampleRate, inChannels int, opts Options) (string, error) {
	// Load model from bytes
	model, err := whisper.NewFromBytes(modelData)
	if err != nil {
		return "", err
	}
	defer model.Close()

	// Create context
	ctx, err := model.NewContext()
	if err != nil {
		return "", err
	}

	// Configure context
	if opts.Threads > 0 {
		ctx.SetThreads(uint(opts.Threads))
	} else {
		ctx.SetThreads(uint(runtime.NumCPU()))
	}
	ctx.SetTranslate(opts.Translate)
	ctx.SetSplitOnWord(opts.SplitOnWord)
	ctx.SetTokenTimestamps(opts.TokenTimestamps)
	if opts.MaxTokensPerSegment > 0 {
		ctx.SetMaxTokensPerSegment(opts.MaxTokensPerSegment)
	}
	if strings.TrimSpace(opts.InitialPrompt) != "" {
		ctx.SetInitialPrompt(opts.InitialPrompt)
	}

	// Language selection
	lang := strings.TrimSpace(opts.Language)
	if lang == "" {
		lang = "auto"
	}
	if lang != "auto" {
		if err := ctx.SetLanguage(lang); err != nil {
			return "", err
		}
	}

	// Convert to 16k mono []float32
	pcmF32, err := pcmInt16ToFloat32Mono16k(pcm, inSampleRate, inChannels)
	if err != nil {
		return "", err
	}
	if len(pcmF32) == 0 {
		return "", errors.New("empty audio after conversion")
	}

	// Process
	if err := ctx.Process(pcmF32, nil, nil, nil); err != nil {
		return "", err
	}

	// Collect segments
	var b strings.Builder
	for {
		seg, err := ctx.NextSegment()
		if err != nil {
			if errors.Is(err, fmt.Errorf("%w", err)) {
				// no-op
			}
		}
		if err != nil {
			if strings.Contains(err.Error(), "EOF") {
				break
			}
			return "", err
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(seg.Text)
	}
	return strings.TrimSpace(b.String()), nil
}

// pcmInt16ToFloat32Mono16k converts interleaved PCM16 to mono 16 kHz float32.
func pcmInt16ToFloat32Mono16k(pcm []int16, inRate, inChans int) ([]float32, error) {
	if inRate <= 0 {
		return nil, errors.New("invalid sample rate")
	}
	if inChans != 1 && inChans != 2 {
		return nil, errors.New("unsupported channel count")
	}

	// To mono (float64 for processing precision)
	var mono []float64
	if inChans == 1 {
		mono = make([]float64, len(pcm))
		for i := range pcm {
			v := float64(pcm[i]) / 32768.0
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			mono[i] = v
		}
	} else { // stereo interleaved
		if len(pcm)%2 != 0 {
			return nil, errors.New("stereo pcm length is not even")
		}
		frames := len(pcm) / 2
		mono = make([]float64, frames)
		for i := 0; i < frames; i++ {
			l := float64(pcm[2*i]) / 32768.0
			r := float64(pcm[2*i+1]) / 32768.0
			// clamp
			if l > 1 {
				l = 1
			} else if l < -1 {
				l = -1
			}
			if r > 1 {
				r = 1
			} else if r < -1 {
				r = -1
			}
			mono[i] = 0.5 * (l + r)
		}
	}

	// Resample to 16k using linear interpolation
	const outRate = 16000
	if inRate != outRate {
		ratio := float64(outRate) / float64(inRate)
		outLen := int(math.Ceil(float64(len(mono)) * ratio))
		res := make([]float64, outLen)
		for i := 0; i < outLen; i++ {
			srcPos := float64(i) / ratio
			j := int(math.Floor(srcPos))
			t := srcPos - float64(j)
			if j+1 < len(mono) {
				res[i] = (1-t)*mono[j] + t*mono[j+1]
			} else {
				res[i] = mono[j]
			}
		}
		mono = res
	}

	// Convert to float32
	out := make([]float32, len(mono))
	for i := range mono {
		out[i] = float32(mono[i])
	}
	return out, nil
}

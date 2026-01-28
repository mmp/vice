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

// Model wraps a loaded whisper model for reuse across transcriptions.
type Model struct {
	model whisper.Model
}

// LoadModelFromBytes loads a whisper model from bytes for reuse.
func LoadModelFromBytes(data []byte) (*Model, error) {
	m, err := whisper.NewFromBytes(data)
	if err != nil {
		return nil, err
	}
	return &Model{model: m}, nil
}

// Close releases the model resources.
func (m *Model) Close() error {
	if m.model != nil {
		return m.model.Close()
	}
	return nil
}

// GPUEnabled returns true if GPU acceleration is being used for inference.
// On Windows or Linux with Vulkan support, this is true if a Vulkan GPU is available.
// On macOS, Metal is always used (handled by the whisper.cpp library internally).
// On Linux or Windows without Vulkan, this returns false (CPU-only).
func GPUEnabled() bool {
	return whisper.GPUEnabled()
}

// GPUDiscrete returns true if a discrete GPU is being used for inference.
// On Windows or Linux with Vulkan, this distinguishes between discrete GPUs (NVIDIA,
// AMD Radeon, Intel Arc) and integrated GPUs (Intel UHD/Iris, AMD APU graphics).
// On other platforms (macOS), this returns false; callers should handle
// macOS specially since Metal provides good performance even on integrated GPUs.
func GPUDiscrete() bool {
	return whisper.GPUDiscrete()
}

// GPUDeviceInfo re-exports the GPU device information type.
type GPUDeviceInfo = whisper.GPUDeviceInfo

// GPUInfo re-exports the GPU information type.
type GPUInfo = whisper.GPUInfo

// GetGPUInfo returns detailed information about GPU acceleration status and devices.
// This includes all available GPU devices, their memory, and which device is selected.
func GetGPUInfo() GPUInfo {
	return whisper.GetGPUInfo()
}

// ProcessorDescription returns a string describing the processor being used for whisper.
// If GPU acceleration is enabled, it returns the GPU device description.
// If running on CPU, it returns CPU info with OS, architecture, and core count.
func ProcessorDescription() string {
	info := GetGPUInfo()
	if info.Enabled && len(info.Devices) > 0 {
		// Find the selected GPU device
		for _, dev := range info.Devices {
			if dev.Index == info.SelectedIndex {
				if dev.TotalMemory > 0 {
					return fmt.Sprintf("GPU: %s (%dMB)", dev.Description, dev.TotalMemory/(1024*1024))
				}
				return fmt.Sprintf("GPU: %s", dev.Description)
			}
		}
		// Fallback if selected index not found in devices
		dev := info.Devices[0]
		if dev.TotalMemory > 0 {
			return fmt.Sprintf("GPU: %s (%dMB)", dev.Description, dev.TotalMemory/(1024*1024))
		}
		return fmt.Sprintf("GPU: %s", dev.Description)
	}
	return fmt.Sprintf("CPU: %s/%s (%d cores)", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

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
	// RealtimeFactor is the ratio of transcription time to audio duration from benchmarking.
	// A value < 0.05 (20x+ realtime) indicates fast hardware suitable for beam search.
	// A value of 0 means unknown/not benchmarked.
	RealtimeFactor float64
}

// TranscribeWithModel transcribes PCM16 audio using a pre-loaded model.
// This is more efficient when transcribing multiple audio samples as it
// avoids reloading the model each time.
func TranscribeWithModel(m *Model, pcm []int16, inSampleRate, inChannels int, opts Options) (string, error) {
	if m == nil || m.model == nil {
		return "", errors.New("model not loaded")
	}

	// Create context
	ctx, err := m.model.NewContext()
	if err != nil {
		return "", err
	}
	defer ctx.Close() // Free C-allocated params

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

	// Disable temperature fallback to prevent multiple decode passes on uncertain audio
	ctx.SetTemperatureFallback(-1.0)

	// Language selection (only for multilingual models; .en models are already English-only)
	lang := strings.TrimSpace(opts.Language)
	if lang == "" {
		lang = "auto"
	}
	if lang != "auto" && ctx.IsMultilingual() {
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

// pcmInt16ToFloat32Mono16k converts interleaved PCM16 to mono 16 kHz float32.
func pcmInt16ToFloat32Mono16k(pcm []int16, inRate, inChans int) ([]float32, error) {
	if inRate <= 0 {
		return nil, errors.New("invalid sample rate")
	}
	if inChans != 1 && inChans != 2 {
		return nil, errors.New("unsupported channel count")
	}

	// To mono (float64 for processing precision)
	// Note: int16 range is [-32768, 32767], so dividing by 32768.0 gives [-1, 1)
	var mono []float64
	if inChans == 1 {
		mono = make([]float64, len(pcm))
		for i := range pcm {
			mono[i] = float64(pcm[i]) / 32768.0
		}
	} else { // stereo interleaved
		if len(pcm)%2 != 0 {
			return nil, errors.New("stereo pcm length is not even")
		}
		frames := len(pcm) / 2
		mono = make([]float64, frames)
		for i := range frames {
			l := float64(pcm[2*i]) / 32768.0
			r := float64(pcm[2*i+1]) / 32768.0
			mono[i] = 0.5 * (l + r)
		}
	}

	// Resample to 16k using linear interpolation
	const outRate = 16000
	if inRate != outRate {
		ratio := float64(outRate) / float64(inRate)
		outLen := int(math.Ceil(float64(len(mono)) * ratio))
		res := make([]float64, outLen)
		for i := range outLen {
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

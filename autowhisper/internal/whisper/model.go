package whisper

import (
	"fmt"
	"os"
	"runtime"

	whisperlow "github.com/mmp/vice/autowhisper/internal/whisperlow"
)

type model struct {
	path string
	ctx  *whisperlow.Context
}

var _ Model = (*model)(nil)

func New(path string) (Model, error) {
	m := new(model)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	ctx, msg := whisperlow.Whisper_init(path)
	if ctx == nil {
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrUnableToLoadModel, msg)
		}
		return nil, ErrUnableToLoadModel
	}
	m.ctx = ctx
	m.path = path
	return m, nil
}

func NewFromBytes(data []byte) (Model, error) {
	m := new(model)
	ctx, msg := whisperlow.Whisper_init_from_buffer(data)
	if ctx == nil {
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrUnableToLoadModel, msg)
		}
		return nil, ErrUnableToLoadModel
	}
	m.ctx = ctx
	return m, nil
}

// SetLogCallback routes whisper.cpp library log messages through the
// supplied callback. Passing nil silences the library. The callback may
// be invoked from any thread (whisper.cpp logs from worker threads).
func SetLogCallback(cb func(level int, text string)) {
	whisperlow.Whisper_log_set_forward(cb)
}

// GGML log levels (mirrors enum ggml_log_level in ggml.h).
const (
	LogLevelNone  = whisperlow.GGMLLogLevelNone
	LogLevelDebug = whisperlow.GGMLLogLevelDebug
	LogLevelInfo  = whisperlow.GGMLLogLevelInfo
	LogLevelWarn  = whisperlow.GGMLLogLevelWarn
	LogLevelError = whisperlow.GGMLLogLevelError
	LogLevelCont  = whisperlow.GGMLLogLevelCont
)

func (m *model) Close() error {
	if m.ctx != nil {
		m.ctx.Whisper_free()
	}
	m.ctx = nil
	return nil
}

func (m *model) IsMultilingual() bool {
	return m.ctx.Whisper_is_multilingual() != 0
}

func (m *model) Languages() []string {
	result := make([]string, 0, whisperlow.Whisper_lang_max_id())
	for i := 0; i < whisperlow.Whisper_lang_max_id(); i++ {
		str := whisperlow.Whisper_lang_str(i)
		if m.ctx.Whisper_lang_id(str) >= 0 {
			result = append(result, str)
		}
	}
	return result
}

// GPUEnabled returns true if GPU acceleration is being used for inference.
func GPUEnabled() bool {
	return whisperlow.GPUEnabled()
}

// GPUDiscrete returns true if a discrete GPU is being used for inference.
func GPUDiscrete() bool {
	return whisperlow.GPUDiscrete()
}

// GPUDeviceInfo re-exports whisperlow.GPUDeviceInfo for external use.
type GPUDeviceInfo = whisperlow.GPUDeviceInfo

// GPUInfo re-exports whisperlow.GPUInfo for external use.
type GPUInfo = whisperlow.GPUInfo

// GetGPUInfo returns detailed information about GPU acceleration status.
func GetGPUInfo() GPUInfo {
	return whisperlow.GetGPUInfo()
}

func (m *model) NewContext() (Context, error) {
	if m.ctx == nil {
		return nil, ErrInternalAppError
	}
	params := m.ctx.Whisper_full_default_params(whisperlow.SAMPLING_GREEDY)
	params.SetTranslate(false)
	params.SetPrintSpecial(false)
	params.SetPrintProgress(false)
	params.SetPrintRealtime(false)
	params.SetPrintTimestamps(false)
	params.SetThreads(runtime.NumCPU())
	params.SetNoContext(true)
	return newContext(m, params)
}

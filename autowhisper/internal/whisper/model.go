package whisper

import (
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
	// silence native logs before init
	whisperlow.Whisper_log_set_silent()
	if ctx := whisperlow.Whisper_init(path); ctx == nil {
		return nil, ErrUnableToLoadModel
	} else {
		m.ctx = ctx
		m.path = path
	}
	return m, nil
}

func NewFromBytes(data []byte) (Model, error) {
	m := new(model)
	// silence native logs before init
	whisperlow.Whisper_log_set_silent()
	if ctx := whisperlow.Whisper_init_from_buffer(data); ctx == nil {
		return nil, ErrUnableToLoadModel
	} else {
		m.ctx = ctx
	}
	return m, nil
}

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

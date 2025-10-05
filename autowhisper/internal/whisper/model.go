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
	for i := range whisperlow.Whisper_lang_max_id() {
		str := whisperlow.Whisper_lang_str(i)
		if m.ctx.Whisper_lang_id(str) >= 0 {
			result = append(result, str)
		}
	}
	return result
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

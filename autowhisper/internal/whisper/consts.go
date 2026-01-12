package whisper

import (
	"errors"

	whisperlow "github.com/mmp/vice/autowhisper/internal/whisperlow"
)

var (
	ErrUnableToLoadModel    = errors.New("unable to load model")
	ErrInternalAppError     = errors.New("internal application error")
	ErrProcessingFailed     = errors.New("processing failed")
	ErrUnsupportedLanguage  = errors.New("unsupported language")
	ErrModelNotMultilingual = errors.New("model is not multilingual")
)

const SampleRate = whisperlow.SampleRate
const SampleBits = whisperlow.SampleBits

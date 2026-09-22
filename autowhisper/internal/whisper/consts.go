// autowhisper/internal/whisper/consts.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

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

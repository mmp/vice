package whisperlow

import (
	"fmt"
)

/*
#include <whisper.h>
#include <stdlib.h>
*/
import "C"

import "unsafe"

func (p *Params) SetTranslate(v bool)       { p.ptr.translate = toBool(v) }
func (p *Params) SetSplitOnWord(v bool)     { p.ptr.split_on_word = toBool(v) }
func (p *Params) SetNoContext(v bool)       { p.ptr.no_context = toBool(v) }
func (p *Params) SetSingleSegment(v bool)   { p.ptr.single_segment = toBool(v) }
func (p *Params) SetPrintSpecial(v bool)    { p.ptr.print_special = toBool(v) }
func (p *Params) SetPrintProgress(v bool)   { p.ptr.print_progress = toBool(v) }
func (p *Params) SetPrintRealtime(v bool)   { p.ptr.print_realtime = toBool(v) }
func (p *Params) SetPrintTimestamps(v bool) { p.ptr.print_timestamps = toBool(v) }

// Set language id
func (p *Params) SetLanguage(lang int) error {
	if lang == -1 {
		p.ptr.language = nil
		return nil
	}
	str := C.whisper_lang_str(C.int(lang))
	if str == nil {
		return ErrInvalidLanguage
	}
	p.ptr.language = str
	return nil
}

// Get language id
func (p *Params) Language() int {
	if p.ptr.language == nil {
		return -1
	}
	return int(C.whisper_lang_id(p.ptr.language))
}

// Threads available
func (p *Params) Threads() int { return int(p.ptr.n_threads) }

// Set number of threads to use
func (p *Params) SetThreads(threads int) { p.ptr.n_threads = C.int(threads) }

// Set start offset in ms
func (p *Params) SetOffset(offset_ms int) { p.ptr.offset_ms = C.int(offset_ms) }

// Set audio duration to process in ms
func (p *Params) SetDuration(duration_ms int) { p.ptr.duration_ms = C.int(duration_ms) }

// Set timestamp token probability threshold (~0.01)
func (p *Params) SetTokenThreshold(t float32) { p.ptr.thold_pt = C.float(t) }

// Set timestamp token sum probability threshold (~0.01)
func (p *Params) SetTokenSumThreshold(t float32) { p.ptr.thold_ptsum = C.float(t) }

// Set max segment length in characters
func (p *Params) SetMaxSegmentLength(n int) { p.ptr.max_len = C.int(n) }

func (p *Params) SetTokenTimestamps(b bool) { p.ptr.token_timestamps = toBool(b) }

// Set max tokens per segment (0 = no limit)
func (p *Params) SetMaxTokensPerSegment(n int) { p.ptr.max_tokens = C.int(n) }

// Set audio encoder context
func (p *Params) SetAudioCtx(n int) { p.ptr.audio_ctx = C.int(n) }

func (p *Params) SetMaxContext(n int) { p.ptr.n_max_text_ctx = C.int(n) }

func (p *Params) SetBeamSize(n int) { p.ptr.beam_search.beam_size = C.int(n) }

func (p *Params) SetEntropyThold(t float32) { p.ptr.entropy_thold = C.float(t) }

func (p *Params) SetTemperature(t float32) { p.ptr.temperature = C.float(t) }

// Sets the fallback temperature incrementation
// Pass -1.0 to disable this feature
func (p *Params) SetTemperatureFallback(t float32) { p.ptr.temperature_inc = C.float(t) }

// Set initial prompt. Frees any previously set prompt.
func (p *Params) SetInitialPrompt(prompt string) {
	if p.ptr.initial_prompt != nil {
		C.free(unsafe.Pointer(p.ptr.initial_prompt))
	}
	p.ptr.initial_prompt = C.CString(prompt)
}

func toBool(v bool) C.bool {
	if v {
		return C.bool(true)
	}
	return C.bool(false)
}

func (p *Params) String() string {
	str := "<whisper.params"
	str += fmt.Sprintf(" strategy=%v", p.ptr.strategy)
	str += fmt.Sprintf(" n_threads=%d", p.ptr.n_threads)
	if p.ptr.language != nil {
		str += fmt.Sprintf(" language=%s", C.GoString(p.ptr.language))
	}
	str += fmt.Sprintf(" n_max_text_ctx=%d", p.ptr.n_max_text_ctx)
	str += fmt.Sprintf(" offset_ms=%d", p.ptr.offset_ms)
	str += fmt.Sprintf(" duration_ms=%d", p.ptr.duration_ms)
	str += fmt.Sprintf(" audio_ctx=%d", p.ptr.audio_ctx)
	str += fmt.Sprintf(" initial_prompt=%s", C.GoString(p.ptr.initial_prompt))
	str += fmt.Sprintf(" entropy_thold=%f", p.ptr.entropy_thold)
	str += fmt.Sprintf(" temperature=%f", p.ptr.temperature)
	str += fmt.Sprintf(" temperature_inc=%f", p.ptr.temperature_inc)
	str += fmt.Sprintf(" beam_size=%d", p.ptr.beam_search.beam_size)
	if p.ptr.translate {
		str += " translate"
	}
	if p.ptr.no_context {
		str += " no_context"
	}
	if p.ptr.single_segment {
		str += " single_segment"
	}
	if p.ptr.print_special {
		str += " print_special"
	}
	if p.ptr.print_progress {
		str += " print_progress"
	}
	if p.ptr.print_realtime {
		str += " print_realtime"
	}
	if p.ptr.print_timestamps {
		str += " print_timestamps"
	}
	if p.ptr.token_timestamps {
		str += " token_timestamps"
	}
	return str + ">"
}

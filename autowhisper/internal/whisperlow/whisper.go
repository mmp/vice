package whisperlow

import (
	"errors"
	"unsafe"
)

/*
#cgo CFLAGS: -I${SRCDIR}/../../../whisper.cpp/include -I${SRCDIR}/../../../whisper.cpp/ggml/include
#cgo LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/src/libwhisper.a ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/libggml.a ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/libggml-base.a ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/libggml-cpu.a -lm -lstdc++
#cgo linux LDFLAGS: -fopenmp
#cgo darwin LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/ggml-blas/libggml-blas.a ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/ggml-metal/libggml-metal.a -framework Accelerate -framework Metal -framework Foundation -framework CoreGraphics
#cgo windows LDFLAGS:
#include <whisper.h>
#include <stdlib.h>

extern void callNewSegment(void* user_data, int new);
extern void callProgress(void* user_data, int progress);
extern bool callEncoderBegin(void* user_data);

static void whisper_new_segment_cb(struct whisper_context* ctx, struct whisper_state* state, int n_new, void* user_data) {
    if(user_data != NULL && ctx != NULL) {
        callNewSegment(user_data, n_new);
    }
}

static void whisper_progress_cb(struct whisper_context* ctx, struct whisper_state* state, int progress, void* user_data) {
    if(user_data != NULL && ctx != NULL) {
        callProgress(user_data, progress);
    }
}

static bool whisper_encoder_begin_cb(struct whisper_context* ctx, struct whisper_state* state, void* user_data) {
    if(user_data != NULL && ctx != NULL) {
        return callEncoderBegin(user_data);
    }
    return false;
}

static struct whisper_full_params whisper_full_default_params_cb(struct whisper_context* ctx, enum whisper_sampling_strategy strategy) {
    struct whisper_full_params params = whisper_full_default_params(strategy);
    params.new_segment_callback = whisper_new_segment_cb;
    params.new_segment_callback_user_data = (void*)(ctx);
    params.encoder_begin_callback = whisper_encoder_begin_cb;
    params.encoder_begin_callback_user_data = (void*)(ctx);
    params.progress_callback = whisper_progress_cb;
    params.progress_callback_user_data = (void*)(ctx);
    return params;
}
*/
import "C"

type (
	Context          C.struct_whisper_context
	Token            C.whisper_token
	TokenData        C.struct_whisper_token_data
	SamplingStrategy C.enum_whisper_sampling_strategy
	Params           C.struct_whisper_full_params
)

const (
	SAMPLING_GREEDY      SamplingStrategy = C.WHISPER_SAMPLING_GREEDY
	SAMPLING_BEAM_SEARCH SamplingStrategy = C.WHISPER_SAMPLING_BEAM_SEARCH
)

const (
	SampleRate = C.WHISPER_SAMPLE_RATE
	SampleBits = uint16(unsafe.Sizeof(C.float(0))) * 8
	NumFFT     = C.WHISPER_N_FFT
	HopLength  = C.WHISPER_HOP_LENGTH
	ChunkSize  = C.WHISPER_CHUNK_SIZE
)

var (
	ErrTokenizerFailed  = errors.New("whisper_tokenize failed")
	ErrAutoDetectFailed = errors.New("whisper_lang_auto_detect failed")
	ErrConversionFailed = errors.New("whisper_convert failed")
	ErrInvalidLanguage  = errors.New("invalid language")
)

func Whisper_init(path string) *Context {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	if ctx := C.whisper_init_from_file_with_params(cPath, C.whisper_context_default_params()); ctx != nil {
		return (*Context)(ctx)
	} else {
		return nil
	}
}

func (ctx *Context) Whisper_free() { C.whisper_free((*C.struct_whisper_context)(ctx)) }

func (ctx *Context) Whisper_pcm_to_mel(data []float32, threads int) error {
	if C.whisper_pcm_to_mel((*C.struct_whisper_context)(ctx), (*C.float)(&data[0]), C.int(len(data)), C.int(threads)) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_set_mel(data []float32, n_mel int) error {
	if C.whisper_set_mel((*C.struct_whisper_context)(ctx), (*C.float)(&data[0]), C.int(len(data)), C.int(n_mel)) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_encode(offset, threads int) error {
	if C.whisper_encode((*C.struct_whisper_context)(ctx), C.int(offset), C.int(threads)) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_decode(tokens []Token, past, threads int) error {
	if C.whisper_decode((*C.struct_whisper_context)(ctx), (*C.whisper_token)(&tokens[0]), C.int(len(tokens)), C.int(past), C.int(threads)) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_tokenize(text string, tokens []Token) (int, error) {
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	if n := C.whisper_tokenize((*C.struct_whisper_context)(ctx), cText, (*C.whisper_token)(&tokens[0]), C.int(len(tokens))); n >= 0 {
		return int(n), nil
	}
	return 0, ErrTokenizerFailed
}

func (ctx *Context) Whisper_lang_id(lang string) int { return int(C.whisper_lang_id(C.CString(lang))) }
func Whisper_lang_max_id() int                       { return int(C.whisper_lang_max_id()) }
func Whisper_lang_str(id int) string                 { return C.GoString(C.whisper_lang_str(C.int(id))) }

func (ctx *Context) Whisper_lang_auto_detect(offset_ms, n_threads int) ([]float32, error) {
	probs := make([]float32, Whisper_lang_max_id()+1)
	if n := int(C.whisper_lang_auto_detect((*C.struct_whisper_context)(ctx), C.int(offset_ms), C.int(n_threads), (*C.float)(&probs[0]))); n < 0 {
		return nil, ErrAutoDetectFailed
	}
	return probs, nil
}

func (ctx *Context) Whisper_n_len() int {
	return int(C.whisper_n_len((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_n_vocab() int {
	return int(C.whisper_n_vocab((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_n_text_ctx() int {
	return int(C.whisper_n_text_ctx((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_n_audio_ctx() int {
	return int(C.whisper_n_audio_ctx((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_is_multilingual() int {
	return int(C.whisper_is_multilingual((*C.struct_whisper_context)(ctx)))
}

func (ctx *Context) Whisper_token_to_str(token Token) string {
	return C.GoString(C.whisper_token_to_str((*C.struct_whisper_context)(ctx), C.whisper_token(token)))
}
func (ctx *Context) Whisper_token_eot() Token {
	return Token(C.whisper_token_eot((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_sot() Token {
	return Token(C.whisper_token_sot((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_prev() Token {
	return Token(C.whisper_token_prev((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_solm() Token {
	return Token(C.whisper_token_solm((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_not() Token {
	return Token(C.whisper_token_not((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_beg() Token {
	return Token(C.whisper_token_beg((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_lang(lang_id int) Token {
	return Token(C.whisper_token_lang((*C.struct_whisper_context)(ctx), C.int(lang_id)))
}
func (ctx *Context) Whisper_token_translate() Token {
	return Token(C.whisper_token_translate((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_token_transcribe() Token {
	return Token(C.whisper_token_transcribe((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_print_timings() {
	C.whisper_print_timings((*C.struct_whisper_context)(ctx))
}
func (ctx *Context) Whisper_reset_timings() {
	C.whisper_reset_timings((*C.struct_whisper_context)(ctx))
}
func Whisper_print_system_info() string { return C.GoString(C.whisper_print_system_info()) }

func (ctx *Context) Whisper_full_default_params(strategy SamplingStrategy) Params {
	return Params(C.whisper_full_default_params_cb((*C.struct_whisper_context)(ctx), C.enum_whisper_sampling_strategy(strategy)))
}

func (ctx *Context) Whisper_full(
	params Params,
	samples []float32,
	encoderBeginCallback func() bool,
	newSegmentCallback func(int),
	progressCallback func(int),
) error {
	registerEncoderBeginCallback(ctx, encoderBeginCallback)
	registerNewSegmentCallback(ctx, newSegmentCallback)
	registerProgressCallback(ctx, progressCallback)
	defer registerEncoderBeginCallback(ctx, nil)
	defer registerNewSegmentCallback(ctx, nil)
	defer registerProgressCallback(ctx, nil)
	if C.whisper_full((*C.struct_whisper_context)(ctx), (C.struct_whisper_full_params)(params), (*C.float)(&samples[0]), C.int(len(samples))) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_full_parallel(params Params, samples []float32, processors int, encoderBeginCallback func() bool, newSegmentCallback func(int)) error {
	registerEncoderBeginCallback(ctx, encoderBeginCallback)
	registerNewSegmentCallback(ctx, newSegmentCallback)
	defer registerEncoderBeginCallback(ctx, nil)
	defer registerNewSegmentCallback(ctx, nil)
	if C.whisper_full_parallel((*C.struct_whisper_context)(ctx), (C.struct_whisper_full_params)(params), (*C.float)(&samples[0]), C.int(len(samples)), C.int(processors)) == 0 {
		return nil
	}
	return ErrConversionFailed
}

func (ctx *Context) Whisper_full_lang_id() int {
	return int(C.whisper_full_lang_id((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_full_n_segments() int {
	return int(C.whisper_full_n_segments((*C.struct_whisper_context)(ctx)))
}
func (ctx *Context) Whisper_full_get_segment_t0(segment int) int64 {
	return int64(C.whisper_full_get_segment_t0((*C.struct_whisper_context)(ctx), C.int(segment)))
}
func (ctx *Context) Whisper_full_get_segment_t1(segment int) int64 {
	return int64(C.whisper_full_get_segment_t1((*C.struct_whisper_context)(ctx), C.int(segment)))
}
func (ctx *Context) Whisper_full_get_segment_text(segment int) string {
	return C.GoString(C.whisper_full_get_segment_text((*C.struct_whisper_context)(ctx), C.int(segment)))
}
func (ctx *Context) Whisper_full_n_tokens(segment int) int {
	return int(C.whisper_full_n_tokens((*C.struct_whisper_context)(ctx), C.int(segment)))
}
func (ctx *Context) Whisper_full_get_token_text(segment int, token int) string {
	return C.GoString(C.whisper_full_get_token_text((*C.struct_whisper_context)(ctx), C.int(segment), C.int(token)))
}
func (ctx *Context) Whisper_full_get_token_id(segment int, token int) Token {
	return Token(C.whisper_full_get_token_id((*C.struct_whisper_context)(ctx), C.int(segment), C.int(token)))
}
func (ctx *Context) Whisper_full_get_token_data(segment int, token int) TokenData {
	return TokenData(C.whisper_full_get_token_data((*C.struct_whisper_context)(ctx), C.int(segment), C.int(token)))
}
func (ctx *Context) Whisper_full_get_token_p(segment int, token int) float32 {
	return float32(C.whisper_full_get_token_p((*C.struct_whisper_context)(ctx), C.int(segment), C.int(token)))
}

var (
	cbNewSegment   = make(map[unsafe.Pointer]func(int))
	cbProgress     = make(map[unsafe.Pointer]func(int))
	cbEncoderBegin = make(map[unsafe.Pointer]func() bool)
)

func registerNewSegmentCallback(ctx *Context, fn func(int)) {
	if fn == nil {
		delete(cbNewSegment, unsafe.Pointer(ctx))
	} else {
		cbNewSegment[unsafe.Pointer(ctx)] = fn
	}
}

func registerProgressCallback(ctx *Context, fn func(int)) {
	if fn == nil {
		delete(cbProgress, unsafe.Pointer(ctx))
	} else {
		cbProgress[unsafe.Pointer(ctx)] = fn
	}
}

func registerEncoderBeginCallback(ctx *Context, fn func() bool) {
	if fn == nil {
		delete(cbEncoderBegin, unsafe.Pointer(ctx))
	} else {
		cbEncoderBegin[unsafe.Pointer(ctx)] = fn
	}
}

//export callNewSegment
func callNewSegment(user_data unsafe.Pointer, new C.int) {
	if fn, ok := cbNewSegment[user_data]; ok {
		fn(int(new))
	}
}

//export callProgress
func callProgress(user_data unsafe.Pointer, progress C.int) {
	if fn, ok := cbProgress[user_data]; ok {
		fn(int(progress))
	}
}

//export callEncoderBegin
func callEncoderBegin(user_data unsafe.Pointer) C.bool {
	if fn, ok := cbEncoderBegin[user_data]; ok {
		if fn() {
			return C.bool(true)
		}
		return C.bool(false)
	}
	return true
}

func (t TokenData) T0() int64 { return int64(t.t0) }
func (t TokenData) T1() int64 { return int64(t.t1) }
func (t TokenData) Id() Token { return Token(t.id) }

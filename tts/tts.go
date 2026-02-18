// tts/tts.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package tts

/*
#cgo CFLAGS: -I${SRCDIR}/../sherpa-onnx/sherpa-onnx/c-api

#cgo !windows LDFLAGS: ${SRCDIR}/../sherpa-onnx/build_go/lib/libsherpa-onnx-c-api.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libsherpa-onnx-core.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libsherpa-onnx-fstfar.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libsherpa-onnx-fst.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libsherpa-onnx-kaldifst-core.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libkaldi-decoder-core.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libkaldi-native-fbank-core.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libssentencepiece_core.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libpiper_phonemize.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libespeak-ng.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libucd.a ${SRCDIR}/../sherpa-onnx/build_go/lib/libkissfft-float.a -lm

#cgo darwin LDFLAGS: ${SRCDIR}/../sherpa-onnx/build_go/_deps/onnxruntime-src/lib/libonnxruntime.a -lstdc++ -framework Foundation -framework Accelerate
#cgo linux LDFLAGS: ${SRCDIR}/../sherpa-onnx/build_go/_deps/onnxruntime-src/lib/libonnxruntime.a -lstdc++ -lpthread -ldl
#cgo windows LDFLAGS: -L${SRCDIR}/../sherpa-onnx/build_go/lib -lsherpa-onnx-c-api -L${SRCDIR}/../sherpa-onnx/build_go/_deps/onnxruntime-src/lib -lonnxruntime -lstdc++ -static-libstdc++ -static-libgcc

#include <c-api.h>
#include <stdlib.h>
*/
import "C"

import "unsafe"

// SharedWeights holds model data loaded once for sharing between TTS instances.
type SharedWeights struct {
	p *C.SherpaOnnxTtsSharedWeights
}

// NewSharedWeights loads model.onnx and voices.bin into memory for sharing.
func NewSharedWeights(modelPath, voicesPath string) *SharedWeights {
	cModel := C.CString(modelPath)
	defer C.free(unsafe.Pointer(cModel))
	cVoices := C.CString(voicesPath)
	defer C.free(unsafe.Pointer(cVoices))

	p := C.SherpaOnnxCreateTtsSharedWeights(cModel, cVoices)
	if p == nil {
		return nil
	}
	return &SharedWeights{p: p}
}

// Delete frees the shared weights.
func (sw *SharedWeights) Delete() {
	if sw != nil && sw.p != nil {
		C.SherpaOnnxDestroyTtsSharedWeights(sw.p)
		sw.p = nil
	}
}

// OfflineTts wraps a sherpa-onnx offline TTS instance.
type OfflineTts struct {
	tts *C.SherpaOnnxOfflineTts
}

// GeneratedAudio holds synthesized audio data.
type GeneratedAudio struct {
	Samples    []float32
	SampleRate int
}

// Config holds TTS configuration parameters.
type Config struct {
	ModelPath    string
	VoicesPath   string
	TokensPath   string
	DataDir      string
	DictDir      string
	LexiconPath  string
	Lang         string
	NumThreads   int
	LowPriority  bool
	MaxSentences int
}

// NewOfflineTts creates a TTS instance. If shared is non-nil, model weights
// are shared with other instances using the same SharedWeights.
func NewOfflineTts(config Config, shared *SharedWeights) *OfflineTts {
	c := C.struct_SherpaOnnxOfflineTtsConfig{}

	cModel := C.CString(config.ModelPath)
	defer C.free(unsafe.Pointer(cModel))
	cVoices := C.CString(config.VoicesPath)
	defer C.free(unsafe.Pointer(cVoices))
	cTokens := C.CString(config.TokensPath)
	defer C.free(unsafe.Pointer(cTokens))
	cDataDir := C.CString(config.DataDir)
	defer C.free(unsafe.Pointer(cDataDir))
	cDictDir := C.CString(config.DictDir)
	defer C.free(unsafe.Pointer(cDictDir))
	cLexicon := C.CString(config.LexiconPath)
	defer C.free(unsafe.Pointer(cLexicon))
	cLang := C.CString(config.Lang)
	defer C.free(unsafe.Pointer(cLang))

	c.model.kokoro.model = cModel
	c.model.kokoro.voices = cVoices
	c.model.kokoro.tokens = cTokens
	c.model.kokoro.data_dir = cDataDir
	c.model.kokoro.dict_dir = cDictDir
	c.model.kokoro.lexicon = cLexicon
	c.model.kokoro.lang = cLang

	c.model.num_threads = C.int(config.NumThreads)
	if config.LowPriority {
		c.model.low_priority_threads = 1
	}
	c.max_num_sentences = C.int(config.MaxSentences)

	var impl *C.SherpaOnnxOfflineTts
	if shared != nil && shared.p != nil {
		impl = C.SherpaOnnxCreateOfflineTtsWithSharedWeights(&c, shared.p)
	} else {
		impl = C.SherpaOnnxCreateOfflineTts(&c)
	}

	if impl == nil {
		return nil
	}
	return &OfflineTts{tts: impl}
}

// Generate synthesizes speech from text.
func (t *OfflineTts) Generate(text string, voiceID int, speed float32) *GeneratedAudio {
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))

	audio := C.SherpaOnnxOfflineTtsGenerate(t.tts, cText, C.int(voiceID), C.float(speed))
	if audio == nil {
		return nil
	}
	defer C.SherpaOnnxDestroyOfflineTtsGeneratedAudio(audio)

	n := int(audio.n)
	if n == 0 {
		return nil
	}

	samples := make([]float32, n)
	src := unsafe.Slice(audio.samples, n)
	for i := range n {
		samples[i] = float32(src[i])
	}

	return &GeneratedAudio{
		Samples:    samples,
		SampleRate: int(audio.sample_rate),
	}
}

// Delete frees the TTS instance.
func (t *OfflineTts) Delete() {
	if t != nil && t.tts != nil {
		C.SherpaOnnxDestroyOfflineTts(t.tts)
		t.tts = nil
	}
}

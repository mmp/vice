// pkg/platform/audio.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

// typedef unsigned char uint8;
// void audioCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"

	"github.com/veandco/go-sdl2/sdl"
)

const AudioSampleRate = 12000

type audioEngine struct {
	pinner  runtime.Pinner
	effects []audioEffect
	mu      sync.Mutex
	config  *Config
}

type audioEffect struct {
	pcm            []byte
	playOnceCount  int
	playContinuous bool
	playOffset     int
}

func (a *audioEngine) Initialize(config *Config, lg *log.Logger) {
	lg.Info("Starting to initialize audio")

	a.config = config

	user := (unsafe.Pointer)(a)
	a.pinner.Pin(user)
	a.pinner.Pin(config)

	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  512,
		Callback: sdl.AudioCallback(C.audioCallback),
		UserData: user,
	}
	sdl.OpenAudio(&spec, nil)
	sdl.PauseAudio(false)

	lg.Info("Finished initializing audio")
}

func (a *audioEngine) AddPCM(pcm []byte, rate int) (int, error) {
	if rate != AudioSampleRate {
		return 0, fmt.Errorf("%d: sample rate doesn't match audio engine's %d",
			rate, AudioSampleRate)
	}
	a.effects = append(a.effects, audioEffect{pcm: pcm})
	return len(a.effects), nil
}

func (a *audioEngine) PlayAudioOnce(index int) {
	if !a.config.AudioEnabled || index == 0 {
		return
	}

	a.mu.Lock()
	a.effects[index-1].playOnceCount++
	a.mu.Unlock()
}

func (a *audioEngine) StartPlayAudioContinuous(index int) {
	if !a.config.AudioEnabled || index == 0 {
		return
	}

	a.mu.Lock()
	a.effects[index-1].playContinuous = true
	a.mu.Unlock()
}

func (a *audioEngine) StopPlayAudioContinuous(index int) {
	if index == 0 {
		return
	}

	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	a.mu.Lock()
	if a.effects[index-1].playContinuous {
		a.effects[index-1].playContinuous = false
		a.effects[index-1].playOffset = 0
	}
	a.mu.Unlock()
}

//export audioCallback
func audioCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	out := unsafe.Slice(ptr, n)
	a := (*audioEngine)(user)

	accum := make([]int, n/2)
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.effects {
		e := &a.effects[i]
		buf := make([]byte, n)
		bread := buf
		for len(bread) > 0 && (e.playContinuous || e.playOnceCount > 0) {
			nc := copy(bread, e.pcm[e.playOffset:])
			e.playOffset += nc
			bread = bread[nc:]

			if e.playOffset == len(e.pcm) {
				e.playOffset = 0
				if e.playOnceCount > 0 {
					e.playOnceCount--
				}
			}
		}

		for i := 0; i < len(buf)/2; i++ {
			accum[i] += int(int16(buf[2*i])|int16(buf[2*i+1])<<8) / 2
		}
	}

	for i := 0; i < n/2; i++ {
		v := int16(math.Clamp(accum[i], -32768, 32767))
		out[2*i] = C.uint8(v & 0xff)
		out[2*i+1] = C.uint8((v >> 8) & 0xff)
	}
}

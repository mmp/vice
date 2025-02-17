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

	"github.com/tosone/minimp3"
	"github.com/veandco/go-sdl2/sdl"
)

const AudioSampleRate = 44100

type audioEngine struct {
	pinner  runtime.Pinner
	effects []audioEffect
	mu      sync.Mutex
	volume  int
}

type audioEffect struct {
	pcm            []byte
	playOnceCount  int
	playContinuous bool
	playOffset     int
}

func (a *audioEngine) Initialize(lg *log.Logger) {
	lg.Info("Starting to initialize audio")

	a.volume = 10

	user := (unsafe.Pointer)(a)
	a.pinner.Pin(user)

	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  2048,
		Callback: sdl.AudioCallback(C.audioCallback),
		UserData: user,
	}
	if err := sdl.OpenAudio(&spec, nil); err != nil {
		lg.Errorf("SDL OpenAudio: %v", err)
	}
	sdl.PauseAudio(false)

	lg.Info("Finished initializing audio")
}

func (a *audioEngine) AddPCM(pcm []byte, rate int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rate != AudioSampleRate {
		return 0, fmt.Errorf("%d: sample rate doesn't match audio engine's %d",
			rate, AudioSampleRate)
	}
	a.effects = append(a.effects, audioEffect{pcm: pcm})
	return len(a.effects), nil
}

func (a *audioEngine) AddMP3(mp3 []byte) (int, error) {
	if dec, pcm, err := minimp3.DecodeFull(mp3); err != nil {
		return -1, err
	} else if dec.Channels != 1 {
		return -1, fmt.Errorf("expected 1 channel, got %d", dec.Channels)
	} else {
		return a.AddPCM(pcm, dec.SampleRate)
	}
}

func (a *audioEngine) SetAudioVolume(vol int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.volume = math.Clamp(vol, 0, 10)
}

func (a *audioEngine) PlayAudioOnce(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playOnceCount++
}

func (a *audioEngine) StartPlayAudioContinuous(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playContinuous = true
}

func (a *audioEngine) StopPlayAudio(index int) {
	if index == 0 {
		return
	}

	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	a.mu.Lock()
	a.effects[index-1].playContinuous = false
	a.effects[index-1].playOffset = 0
	a.effects[index-1].playOnceCount = 0
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
		v := int16(math.Clamp(accum[i]*a.volume/10, -32768, 32767))
		out[2*i] = C.uint8(v & 0xff)
		out[2*i+1] = C.uint8((v >> 8) & 0xff)
	}
}

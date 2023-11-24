// audio.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// typedef unsigned char uint8;
// void audioCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"C"
	"bytes"
	"fmt"
	"io"
	"reflect"
	"sync"
	"unsafe"

	mp3 "github.com/hajimehoshi/go-mp3"
	"github.com/mmp/imgui-go/v4"
	"github.com/veandco/go-sdl2/sdl"
)

const AudioSampleRate = 16000

type AudioType int

// The types of events we may play audio for; note that not all are
// currently used.
const (
	AudioConflictAlert = iota
	AudioEmergencySquawk
	AudioMinimumSafeAltitudeWarning
	AudioModeCIntruder
	AudioInboundHandoff
	AudioCommandError
	AudioNumTypes
)

func (ae AudioType) String() string {
	return [...]string{
		"Conflict Alert",
		"Emergency Squawk Code",
		"Minimum Safe Altitude Warning",
		"Mode C Intruder",
		"Inbound Handoff",
		"Command Error",
	}[ae]
}

type AudioEngine struct {
	AudioEnabled  bool
	EffectEnabled [AudioNumTypes]bool

	effects        [AudioNumTypes]*mp3.Decoder
	playOnceCount  [AudioNumTypes]int
	playContinuous [AudioNumTypes]bool

	mu sync.Mutex
}

func (a *AudioEngine) SetDefaults() {
	a.AudioEnabled = true
	for i := 0; i < AudioNumTypes; i++ {
		a.EffectEnabled[i] = true
	}
}

func (a *AudioEngine) PlayOnce(e AudioType) {
	if !a.AudioEnabled || !a.EffectEnabled[e] {
		return
	}

	a.mu.Lock()
	a.playOnceCount[e]++
	a.mu.Unlock()
}

func (a *AudioEngine) StartPlayContinuous(e AudioType) {
	if !a.AudioEnabled || !a.EffectEnabled[e] {
		return
	}

	a.mu.Lock()
	a.playContinuous[e] = true
	a.mu.Unlock()
}

func (a *AudioEngine) StopPlayContinuous(e AudioType) {
	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	a.mu.Lock()
	if a.playContinuous[e] {
		a.playContinuous[e] = false
		if _, err := a.effects[e].Seek(0, io.SeekStart); err != nil {
			panic(err)
		}
	}
	a.mu.Unlock()
}

//export audioCallback
func audioCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	hdr := reflect.SliceHeader{Data: uintptr(unsafe.Pointer(ptr)), Len: n, Cap: n}
	out := *(*[]C.uint8)(unsafe.Pointer(&hdr))
	a := &globalConfig.Audio

	accum := make([]int, n/2)
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 0; i < AudioNumTypes; i++ {
		buf := make([]byte, 2*n) // go-mp3 always gives 2 channels * 16-bits
		bread := buf
		for len(bread) > 0 && (a.playContinuous[i] || a.playOnceCount[i] > 0) {
			nr, err := io.ReadFull(a.effects[i], bread)
			bread = bread[nr:]
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				if a.playOnceCount[i] > 0 {
					a.playOnceCount[i]--
				}
				if _, err := a.effects[i].Seek(0, io.SeekStart); err != nil {
					panic(err)
				}
			} else if err != nil {
				panic(err)
			}
		}

		for i := 0; i < len(buf)/4; i++ {
			// just take the first channel
			accum[i] += int(int16(buf[4*i])|int16(buf[4*i+1])<<8) / 2
		}
	}

	for i := 0; i < n/2; i++ {
		v := int16(clamp(accum[i], -32768, 32767))
		out[2*i] = C.uint8(v & 0xff)
		out[2*i+1] = C.uint8((v >> 8) & 0xff)
	}
}

func (a *AudioEngine) loadMP3(filename string) *mp3.Decoder {
	r := bytes.NewReader(LoadResource("audio/" + filename))
	dec, err := mp3.NewDecoder(r)
	if err != nil {
		panic(err)
	}

	if dec.SampleRate() != AudioSampleRate {
		panic(fmt.Sprintf("expected %d Hz sample rate, got %d", AudioSampleRate, dec.SampleRate()))
	}

	return dec
}

func (a *AudioEngine) Activate() error {
	lg.Info("Starting to initialize audio")

	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  512,
		Callback: sdl.AudioCallback(C.audioCallback),
	}
	sdl.OpenAudio(&spec, nil)
	sdl.PauseAudio(false)

	a.effects[AudioConflictAlert] = a.loadMP3("ca.mp3")
	a.effects[AudioEmergencySquawk] = a.loadMP3("emergency.mp3")
	a.effects[AudioMinimumSafeAltitudeWarning] = a.loadMP3("msaw.mp3")
	a.effects[AudioModeCIntruder] = a.loadMP3("intruder.mp3")
	a.effects[AudioInboundHandoff] = a.loadMP3("263124__pan14__sine-octaves-up-beep.mp3")
	a.effects[AudioCommandError] = a.loadMP3("426888__thisusernameis__beep4.mp3")

	lg.Info("Finished initializing audio")
	return nil
}

func (a *AudioEngine) DrawUI() {
	imgui.Checkbox("Enable Sound Effects", &a.AudioEnabled)
	imgui.Separator()

	uiStartDisable(!a.AudioEnabled)
	// Not all of the ones available in the engine are used, so only offer these up:
	for _, i := range []AudioType{AudioConflictAlert, AudioInboundHandoff, AudioCommandError} {
		if imgui.Checkbox(AudioType(i).String(), &a.EffectEnabled[i]) && a.EffectEnabled[i] {
			n := Select(i == AudioConflictAlert, 5, 1)
			for j := 0; j < n; j++ {
				a.PlayOnce(i)
			}
		}
	}
	uiEndDisable(!a.AudioEnabled)
}

// audio.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// typedef unsigned char uint8;
// void audioCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"C"
	"sync"
	"unsafe"

	"github.com/mmp/imgui-go/v4"
	"github.com/tosone/minimp3"
	"github.com/veandco/go-sdl2/sdl"
)
import "github.com/mmp/vice/pkg/math"

const AudioSampleRate = 12000

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
	AudioHandoffAccepted
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
		"Handoff Accepted",
	}[ae]
}

type AudioEngine struct {
	AudioEnabled  bool
	EffectEnabled [AudioNumTypes]bool

	effects [AudioNumTypes]AudioEffect

	mu sync.Mutex
}

type AudioEffect struct {
	pcm            []byte
	playOnceCount  int
	playContinuous bool
	playOffset     int
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
	a.effects[e].playOnceCount++
	a.mu.Unlock()
}

func (a *AudioEngine) StartPlayContinuous(e AudioType) {
	if !a.AudioEnabled || !a.EffectEnabled[e] {
		return
	}

	a.mu.Lock()
	a.effects[e].playContinuous = true
	a.mu.Unlock()
}

func (a *AudioEngine) StopPlayContinuous(e AudioType) {
	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	a.mu.Lock()
	if a.effects[e].playContinuous {
		a.effects[e].playContinuous = false
		a.effects[e].playOffset = 0
	}
	a.mu.Unlock()
}

//export audioCallback
func audioCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	out := unsafe.Slice(ptr, n)
	a := &globalConfig.Audio

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

func (a *AudioEngine) loadMP3(filename string) AudioEffect {
	dec, pcm, err := minimp3.DecodeFull(LoadResource("audio/" + filename))
	if err != nil {
		lg.Errorf("%s: unable to decode mp3: %v", filename, err)
	}
	if dec.SampleRate != AudioSampleRate {
		lg.Errorf("expected %d Hz sample rate, got %d", AudioSampleRate, dec.SampleRate)
	}
	if dec.Channels != 1 {
		lg.Errorf("expected 1 channel, got %d", dec.Channels)
	}

	return AudioEffect{pcm: pcm}
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
	a.effects[AudioHandoffAccepted] = a.loadMP3("321104__nsstudios__blip2.mp3")

	lg.Info("Finished initializing audio")
	return nil
}

func (a *AudioEngine) DrawUI() {
	imgui.Checkbox("Enable Sound Effects", &a.AudioEnabled)
	imgui.Separator()

	uiStartDisable(!a.AudioEnabled)
	// Not all of the ones available in the engine are used, so only offer these up:
	for _, i := range []AudioType{AudioConflictAlert, AudioInboundHandoff, AudioHandoffAccepted, AudioCommandError} {
		if imgui.Checkbox(AudioType(i).String(), &a.EffectEnabled[i]) && a.EffectEnabled[i] {
			n := Select(i == AudioConflictAlert, 5, 1)
			for j := 0; j < n; j++ {
				a.PlayOnce(i)
			}
		}
	}
	uiEndDisable(!a.AudioEnabled)
}

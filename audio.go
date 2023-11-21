// audio.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmp/imgui-go/v4"
	"github.com/veandco/go-sdl2/sdl"
	"golang.org/x/exp/slog"
)

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

	effects [AudioNumTypes]*SoundEffect
	mu      sync.Mutex
}

type SoundEffect struct {
	wav              []byte
	duration         time.Duration
	device           sdl.AudioDeviceID
	continuous       bool
	refillQueueLimit uint32
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

	if se := a.effects[e]; se != nil {
		// Make sure an SDL panic doesn't take down vice
		defer func() {
			if err := recover(); err != nil {
				lg.Error("SDL panic playing audio", slog.Any("panic", err))
			}
		}()

		// Only call into SDL from one thread at a time
		a.mu.Lock()
		defer a.mu.Unlock()

		if err := sdl.QueueAudio(se.device, se.wav); err != nil {
			lg.Errorf("Unable to queue SDL audio: %v", err)
		}

		// Release the device so it starts playing the sound.
		sdl.PauseAudioDevice(se.device, false)
	}
}

func (a *AudioEngine) StartPlayContinuous(e AudioType) {
	if !a.AudioEnabled || !a.EffectEnabled[e] {
		return
	}

	if se := a.effects[e]; se != nil {
		defer func() {
			if err := recover(); err != nil {
				lg.Error("SDL panic playing audio", slog.Any("panic", err))
			}
		}()

		// Only call into SDL from one thread at a time
		a.mu.Lock()
		defer a.mu.Unlock()

		if !se.continuous {
			se.continuous = true
			for i := 0; i < 10; i++ {
				if err := sdl.QueueAudio(se.device, se.wav); err != nil {
					lg.Errorf("Unable to queue SDL audio: %v", err)
				}
			}
			se.refillQueueLimit = sdl.GetQueuedAudioSize(se.device) / 5

			// Release the device so it starts playing the sound.
			sdl.PauseAudioDevice(se.device, false)
		} else if sdl.GetQueuedAudioSize(se.device) < se.refillQueueLimit {
			// Queue up more repeats so there's no break in playback
			for i := 0; i < 10; i++ {
				if err := sdl.QueueAudio(se.device, se.wav); err != nil {
					lg.Errorf("Unable to queue SDL audio: %v", err)
				}
			}
		}
	}
}

func (a *AudioEngine) StopPlayContinuous(e AudioType) {
	defer func() {
		if err := recover(); err != nil {
			lg.Error("SDL panic playing audio", slog.Any("panic", err))
		}
	}()

	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	if se := a.effects[e]; se != nil {
		// Only call into SDL from one thread at a time
		a.mu.Lock()
		if se.continuous {
			se.continuous = false
			sdl.ClearQueuedAudio(se.device)
		}
		a.mu.Unlock()
	}
}

func (a *AudioEngine) addEffect(t AudioType, filename string) {
	wav := LoadResource("audio/" + filename)
	rw, err := sdl.RWFromMem([]byte(wav))
	if err != nil {
		lg.Errorf("%s: unable to add audio effect: %v", filename, err)
		return
	}

	loaded, spec := sdl.LoadWAVRW(rw, false /* do not free */)

	duration := float32(len([]byte(wav))) /
		float32(int(spec.Freq)*int(spec.Channels)*int(spec.Format.BitSize())/8)

	var obtained sdl.AudioSpec
	audioDevice, err := sdl.OpenAudioDevice("", false /* no record */, spec, &obtained, 0)
	if err != nil {
		lg.Errorf("Unable to open SDL audio device: %v", err)
		return
	}

	a.effects[t] = &SoundEffect{
		wav:      loaded,
		duration: time.Duration(duration * float32(time.Second)),
		device:   audioDevice,
	}

	if err = rw.Close(); err != nil {
		lg.Errorf("SDL error: %v", err)
	}
}

func (a *AudioEngine) Activate() error {
	lg.Info("Starting to initialize audio")
	err := sdl.Init(sdl.INIT_AUDIO)
	if err != nil {
		return fmt.Errorf("failed to initialize SDL2 audio: %w", err)
	}

	a.addEffect(AudioConflictAlert, "ca.wav")
	a.addEffect(AudioEmergencySquawk, "emergency.wav")
	a.addEffect(AudioMinimumSafeAltitudeWarning, "msaw.wav")
	a.addEffect(AudioModeCIntruder, "intruder.wav")
	a.addEffect(AudioInboundHandoff, "263124__pan14__sine-octaves-up-beep.wav")
	a.addEffect(AudioCommandError, "426888__thisusernameis__beep4.wav")

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

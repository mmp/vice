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

var (
	soundEffects map[string]*SoundEffect
	sdlMutex     sync.Mutex
)

// AudioEvent represents a predefined event will lead to a sound being played, if the user has
// associated one with it.
type AudioEvent int

const (
	AudioEventConflictAlert = iota
	AudioEventInboundHandoff
	AudioEventHandoffAccepted
	AudioEventCommandError
	AudioEventCount
)

func (ae AudioEvent) String() string {
	return [...]string{
		"Conflict Alert",
		"Inbound Handoff",
		"Handoff Accepted",
		"Command Error",
	}[ae]
}

type AudioSettings struct {
	SoundEffects [AudioEventCount]string
	AudioEnabled bool

	muteUntil     time.Time
	lastPlay      [AudioEventCount]time.Time
	lastPlayMutex sync.Mutex
}

// Play no sounds for the specified period of time. This is mostly useful
// so that when we replay trace files and have a ton of audio events in the
// first few seconds we can just drop all of them and not have a
// cacophonous beginning.
func (a *AudioSettings) MuteFor(d time.Duration) {
	a.muteUntil = time.Now().Add(d)
}

func (a *AudioSettings) PlaySound(e AudioEvent) {
	if !a.AudioEnabled || !time.Now().After(a.muteUntil) || soundEffects == nil {
		return
	}

	if effect := a.SoundEffects[e]; effect == "" {
		return
	} else if se, ok := soundEffects[effect]; !ok {
		// This should only happen if a built-in sound effect is removed
		// and an old config file refers to it. Arguably the user should be
		// notified in this (unexpected) case...
		lg.Errorf("%s: sound effect disappeared?!", effect)
		a.SoundEffects[e] = ""
	} else {
		a.lastPlayMutex.Lock()
		defer a.lastPlayMutex.Unlock()
		if time.Since(a.lastPlay[e]) > 2*time.Second {
			a.lastPlay[e] = time.Now()
			se.Play()
		}
	}
}

type SoundEffect struct {
	name     string
	wav      []byte
	duration time.Duration
	repeat   int
	spec     *sdl.AudioSpec
}

func (s *SoundEffect) Play() {
	// Play the sound effect in a separate thread so that Play()
	// immediately returns to the caller.
	go func() {
		defer func() {
			if err := recover(); err != nil {
				lg.Error("SDL panic playing audio", slog.Any("panic", err))
			}
		}()

		// SDL seems to be crashy if multiple threads call its functions
		// concurrently even if they're operating independently...
		sdlMutex.Lock()

		// TODO: it's a little unclear what best practices are here. Should
		// we open an audio device for each SoundEffect and then leave it
		// open the whole time? Should we try to open a minimal number of
		// them, sharing them when the sdl.AudioSpec is compatiable?
		// The following at least works correctly...
		var obtained sdl.AudioSpec
		audioDevice, err := sdl.OpenAudioDevice("", false /* no record */, s.spec, &obtained, 0)
		if err != nil {
			lg.Errorf("Unable to open SDL audio device: %v", err)
			sdlMutex.Unlock()
			return
		}

		for i := 0; i < s.repeat; i++ {
			if err = sdl.QueueAudio(audioDevice, s.wav); err != nil {
				lg.Errorf("Unable to queue SDL audio: %v", err)
			}
		}

		// Release the device so it starts playing the sound.
		sdl.PauseAudioDevice(audioDevice, false)
		sdlMutex.Unlock()

		// Wait for the sound to finish playing before closing the audio
		// device. We would really like to just do time.Sleep(s.repeat *
		// s.duration), but sadly the computation of s.duration in
		// addEffect() is somehow borked.
		for {
			time.Sleep(100 * time.Millisecond)
			sdlMutex.Lock()
			sz := sdl.GetQueuedAudioSize(audioDevice)
			sdlMutex.Unlock()
			if sz == 0 {
				// and make sure it drains...
				time.Sleep(100 * time.Millisecond)
				break
			}
		}

		sdlMutex.Lock()
		sdl.CloseAudioDevice(audioDevice)
		sdlMutex.Unlock()
	}()
}

func addEffect(filename string, name string, repeat int) {
	wav := LoadResource("audio/" + filename)
	rw, err := sdl.RWFromMem([]byte(wav))
	if err != nil {
		lg.Errorf("%s: unable to add audio effect: %v", name, err)
		return
	}

	loaded, spec := sdl.LoadWAVRW(rw, false /* do not free */)

	if _, ok := soundEffects[name]; ok {
		lg.Error("audio: sound effect \"" + name + "\" used repeatedly")
		return
	}

	// The computed duration here is apparently incorrect. FIXME.
	duration := float32(len([]byte(wav))) /
		float32(int(spec.Freq)*int(spec.Channels)*int(spec.Format.BitSize())/8)
	soundEffects[name] = &SoundEffect{
		name:     name,
		wav:      loaded,
		duration: time.Duration(duration * 1e9),
		repeat:   repeat,
		spec:     spec,
	}

	if err = rw.Close(); err != nil {
		lg.Errorf("SDL error: %v", err)
	}
	// TODO: in principle it seems that we should be calling rw.Free()
	// here, though doing so leads to a panic about trying to free
	// something that was not allocated.
}

func audioInit() error {
	lg.Info("Starting to initialize audio")
	err := sdl.Init(sdl.INIT_AUDIO)
	if err != nil {
		return fmt.Errorf("failed to initialize SDL2 audio: %w", err)
	}

	soundEffects = make(map[string]*SoundEffect)
	addEffect("389511__bbrocer__digital-alarm-loop.wav", "Alarm - Digital", 2)
	addEffect("529626__beetlemuse__alert-1.wav", "Alert", 1)
	addEffect("320181__dland__hint.wav", "Hint", 1)
	addEffect("242501__gabrielaraujo__powerup-success.wav", "Success", 1)
	addEffect("427961__michaelatoz__alertwo-cc0.wav", "Alert 2", 1)
	addEffect("321104__nsstudios__blip2.wav", "Blip", 1)
	addEffect("263125__pan14__sine-fifths-up-beep.wav", "Beep Up Fifths", 1)
	addEffect("263124__pan14__sine-octaves-up-beep.wav", "Beep Up", 1)
	addEffect("263123__pan14__sine-tri-tone-down-negative-beep-amb-verb.wav", "Beep Negative", 1)
	addEffect("263129__pan14__sine-up-flutter-beep.wav", "Beep Flutter", 1)
	addEffect("263126__pan14__tone-beep-lower-slower.wav", "Beep Slow", 1)
	addEffect("263132__pan14__tri-tone-up-beep.wav", "Beep Tone Up", 1)
	addEffect("263655__pan14__upward-beep-chromatic-fifths.wav", "Beep Chromatic", 1)
	addEffect("487588__ranner__ui-click.wav", "Click", 1)
	addEffect("387533__soundwarf__alert-short.wav", "Alert Short", 1)
	addEffect("426888__thisusernameis__beep4.wav", "Beep Double", 1)

	lg.Info("Finished initializing audio")
	return nil
}

func (a *AudioSettings) DrawUI() {
	imgui.Checkbox("Enable Sound Effects", &a.AudioEnabled)

	if a.AudioEnabled {
		sortedSounds := SortedMapKeys(soundEffects)

		for i := 0; i < AudioEventCount; i++ {
			event := AudioEvent(i).String()
			current := a.SoundEffects[i]
			if current == "" {
				current = "(None)"
			}
			if imgui.BeginComboV(event, current, imgui.ComboFlagsHeightLarge) {
				flags := imgui.SelectableFlagsNone
				if imgui.SelectableV("(None)", a.SoundEffects[i] == "", flags, imgui.Vec2{}) {
					a.SoundEffects[i] = ""
				}
				for _, sound := range sortedSounds {
					if imgui.SelectableV(sound, sound == a.SoundEffects[i], flags, imgui.Vec2{}) {
						a.SoundEffects[i] = sound
						soundEffects[sound].Play()
					}
				}
				imgui.EndCombo()
			}
		}
	}
}

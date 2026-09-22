// platform/sdl2/audio.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sdl2

// typedef unsigned char uint8;
// void audioCallback(void *userdata, uint8 *stream, int len);
// void audioInputCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform/audio"
	"github.com/mmp/vice/rand"

	"github.com/tosone/minimp3"
	"github.com/veandco/go-sdl2/sdl"
)

type engine struct {
	pinner        runtime.Pinner
	effects       []audioEffect
	speechq       []int16
	speechcb      func()
	speechGarbled bool
	deviceOpen    bool
	mu            sync.Mutex
	volume        int

	rec         *recorder
	playbackErr error
}

type audioEffect struct {
	pcm            []int16
	playOnceCount  int
	playContinuous bool
	playOffset     int
}

// New initializes SDL audio and returns an Engine for audio playback and
// microphone capture. If the output device can't be opened, the returned
// Engine still supports capture and reports the failure from
// AudioPlaybackError. requestMicrophone should be false for tools that play
// audio but never record any, which would otherwise put a microphone
// permission prompt in front of the user for nothing.
func New(lg *log.Logger, requestMicrophone bool) audio.Engine {
	if requestMicrophone {
		// Request microphone permission before SDL audio is initialized;
		// this avoids potential conflicts between AVFoundation and CoreAudio.
		status := microphoneAuthorizationStatus()
		lg.Infof("Microphone authorization status: %s", status)
		switch status {
		case micAuthNotDetermined:
			lg.Info("Requesting microphone permission (dialog will appear)...")
			requestMicrophoneAccess()
		case micAuthDenied:
			lg.Warn("Microphone access denied - enable in System Settings > Privacy & Security > Microphone")
		case micAuthRestricted:
			lg.Warn("Microphone access restricted by system policy")
		}
	}

	a := &engine{rec: newRecorder(lg)}
	if err := a.initialize(lg); err != nil {
		a.playbackErr = err
		lg.Errorf("Audio playback unavailable: %v", err)
	}
	return a
}

// initialize opens the audio output device. An error is returned if it
// can't be opened; audio playback is then unavailable for the session.
func (a *engine) initialize(lg *log.Logger) error {
	lg.Info("Starting to initialize audio")

	a.volume = 10

	user := (unsafe.Pointer)(a)
	a.pinner.Pin(user)

	spec := sdl.AudioSpec{
		Freq:     audio.SampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  2048,
		Callback: sdl.AudioCallback(C.audioCallback),
		UserData: user,
	}
	if err := sdl.OpenAudio(&spec, nil); err != nil {
		a.pinner.Unpin()
		lg.SetAudioDriver(fmt.Sprintf("unavailable (%v)", err))
		return fmt.Errorf("SDL OpenAudio: %w", err)
	}
	a.deviceOpen = true
	sdl.PauseAudio(false)
	lg.SetAudioDriver(sdl.GetCurrentAudioDriver())

	lg.Info("Finished initializing audio")
	return nil
}

func (a *engine) Dispose() {
	a.rec.close()

	if !a.deviceOpen {
		return
	}
	sdl.PauseAudio(true)
	sdl.CloseAudio()
	a.pinner.Unpin()
	a.deviceOpen = false
}

func (a *engine) AddPCM(pcm []byte, rate int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rate != audio.SampleRate {
		return 0, fmt.Errorf("%d: sample rate doesn't match audio engine's %d",
			rate, audio.SampleRate)
	}

	a.effects = append(a.effects, audioEffect{pcm: pcm16FromBytes(pcm)})
	return len(a.effects), nil
}

func pcm16FromBytes(pcm []byte) []int16 {
	pcm16 := make([]int16, len(pcm)/2)
	for i := range pcm16 {
		pcm16[i] = int16(pcm[2*i]) | (int16(pcm[2*i+1]) << 8)
	}
	return pcm16
}

func (a *engine) AddMP3(mp3 []byte) (int, error) {
	if dec, pcm, err := minimp3.DecodeFull(mp3); err != nil {
		return -1, err
	} else if dec.Channels != 1 {
		return -1, fmt.Errorf("expected 1 channel, got %d", dec.Channels)
	} else {
		return a.AddPCM(pcm, dec.SampleRate)
	}
}

func (a *engine) TryEnqueueSpeechPCM(pcm []int16, finished func()) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.speechq) > 0 {
		return audio.ErrCurrentlyPlayingSpeech
	}
	if !a.deviceOpen {
		return audio.ErrPlaybackUnavailable
	}
	if len(pcm) == 0 {
		return errors.New("no speech samples to play")
	}

	a.speechq = pcm
	a.speechcb = finished
	return nil
}

func (a *engine) AppendSpeechPCM(pcm []int16) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.deviceOpen {
		// Nothing will consume the queue, so don't let it grow without bound.
		return
	}
	a.speechq = append(a.speechq, pcm...)
}

func (a *engine) StopSpeech() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.speechq, a.speechcb = nil, nil
}

func (a *engine) SetAudioVolume(vol int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.volume = math.Clamp(vol, 0, 10)
}

func (a *engine) PlayAudioOnce(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playOnceCount++
}

func (a *engine) StartPlayAudioContinuous(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playContinuous = true
}

func (a *engine) StopPlayAudio(index int) {
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

func (a *engine) SetSpeechGarbled(garbled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.speechGarbled = garbled
}

func (a *engine) IsPlayingSpeech() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.speechq) > 0
}

func (a *engine) RemainingSpeechDuration() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Duration(len(a.speechq)) * time.Second / time.Duration(audio.SampleRate)
}

func (a *engine) StartAudioCapture() error {
	return a.rec.startCapture()
}

func (a *engine) StartAudioCaptureWithDevice(deviceName string) error {
	return a.rec.startCaptureWithDevice(deviceName)
}

func (a *engine) StopAudioCapture() {
	a.rec.stopCapture()
}

func (a *engine) IsAudioCapturing() bool {
	return a.rec.isCapturing()
}

func (a *engine) GetAudioPreroll() []int16 {
	return a.rec.preroll()
}

func (a *engine) StartAudioRecording() error {
	return a.rec.startRecording()
}

func (a *engine) StartAudioRecordingWithDevice(deviceName string) error {
	return a.rec.startRecordingWithDevice(deviceName)
}

func (a *engine) StopAudioRecording() ([]int16, error) {
	return a.rec.stopRecording()
}

func (a *engine) IsAudioRecording() bool {
	return a.rec.isRecording()
}

func (a *engine) GetAudioInputDevices() []string {
	return inputDevices()
}

func (a *engine) SetAudioStreamCallback(cb func([]int16)) {
	a.rec.setStreamCallback(cb)
}

func (a *engine) AudioPlaybackError() error {
	return a.playbackErr
}

//export audioCallback
func audioCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	out := unsafe.Slice(ptr, n)
	a := (*engine)(user)

	accum := make([]int, n/2)
	var cbToCall func() // Save callback to call after releasing the lock

	a.mu.Lock()

	as := 0 // accum index for speech
	var r *rand.Rand
	if a.speechGarbled && len(a.speechq) > 0 {
		r = rand.Make()
	}
	for len(a.speechq) > 0 && as < len(accum) {
		sample := int(a.speechq[0])
		if a.speechGarbled {
			// Audio ducking: reduce to 25% volume
			sample = sample / 4
			// Add loud static noise
			sample += r.IntRange(-4000, 4000)
		}
		accum[as] = sample
		as++
		a.speechq = a.speechq[1:]
	}
	if len(a.speechq) == 0 && as > 0 && a.speechcb != nil {
		cbToCall = a.speechcb // Save callback, don't call yet
		a.speechcb = nil
	}

	for i := range a.effects {
		e := &a.effects[i]
		buf := make([]int16, n/2)
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

		for i := 0; i < len(buf); i++ {
			accum[i] += int(buf[i])
		}
	}

	for i := 0; i < n/2; i++ {
		v := math.Clamp(accum[i]*a.volume/10, -32768, 32767)
		out[2*i] = C.uint8(v & 0xff)
		out[2*i+1] = C.uint8((v >> 8) & 0xff)
	}

	a.mu.Unlock()

	// Call callback outside lock to prevent deadlock with tm.mu
	if cbToCall != nil {
		cbToCall()
	}
}

//export audioInputCallback
func audioInputCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	in := unsafe.Slice(ptr, n)
	ar := (*recorder)(user)

	// Convert bytes to int16 samples
	samples := make([]int16, n/2)
	for i := 0; i < n/2; i++ {
		samples[i] = int16(in[2*i]) | (int16(in[2*i+1]) << 8)
	}

	ar.addAudioData(samples)
}

// platform/audio_input.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/veandco/go-sdl2/sdl"
)

// AudioRecorder handles microphone recording
type AudioRecorder struct {
	deviceID sdl.AudioDeviceID
	recording bool
	audioData []int16
	mu        sync.Mutex
	lg        *log.Logger
}

// NewAudioRecorder creates a new audio recorder
func NewAudioRecorder(lg *log.Logger) *AudioRecorder {
	return &AudioRecorder{
		lg: lg,
	}
}

// StartRecording starts recording audio from the microphone
func (ar *AudioRecorder) StartRecording() error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.recording {
		return fmt.Errorf("already recording")
	}

	// Open audio device for recording
	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  2048,
		Callback: sdl.AudioCallback(C.audioInputCallback),
		UserData: unsafe.Pointer(ar),
	}

	deviceID, err := sdl.OpenAudioDevice("", true, &spec, nil, 0)
	if err != nil {
		return fmt.Errorf("failed to open audio device: %v", err)
	}

	ar.deviceID = deviceID
	ar.recording = true
	ar.audioData = make([]int16, 0, AudioSampleRate*60) // Pre-allocate for up to 60 seconds

	// Start recording
	sdl.PauseAudioDevice(deviceID, false)

	ar.lg.Info("Started audio recording")
	return nil
}

// StopRecording stops recording and returns the recorded audio data
func (ar *AudioRecorder) StopRecording() ([]int16, error) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if !ar.recording {
		return nil, fmt.Errorf("not recording")
	}

	// Stop recording
	sdl.PauseAudioDevice(ar.deviceID, true)
	sdl.CloseAudioDevice(ar.deviceID)

	ar.recording = false
	audioData := ar.audioData
	ar.audioData = nil

	ar.lg.Infof("Stopped audio recording, captured %d samples", len(audioData))
	return audioData, nil
}

// IsRecording returns true if currently recording
func (ar *AudioRecorder) IsRecording() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.recording
}

// addAudioData adds audio data to the recording buffer
func (ar *AudioRecorder) addAudioData(data []int16) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	
	if ar.recording {
		ar.audioData = append(ar.audioData, data...)
	}
}
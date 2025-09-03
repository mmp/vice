// platform/audio_input.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

// typedef unsigned char uint8;
// void audioInputCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/veandco/go-sdl2/sdl"
)

// AudioRecorder handles microphone recording
type AudioRecorder struct {
	deviceID  sdl.AudioDeviceID
	openDevices map[string]sdl.AudioDeviceID
	recording bool
	audioData []int16
	mu        sync.Mutex
	lg        *log.Logger
	pinner    runtime.Pinner
}

// NewAudioRecorder creates a new audio recorder
func NewAudioRecorder(lg *log.Logger) *AudioRecorder {
	return &AudioRecorder{
		lg: lg,
	}
}

// StartRecording starts recording audio from the default microphone
func (ar *AudioRecorder) StartRecording() error {
	return ar.StartRecordingWithDevice("")
}

// StartRecordingWithDevice starts recording audio from the specified microphone
func (ar *AudioRecorder) StartRecordingWithDevice(deviceName string) error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.recording {
		return fmt.Errorf("already recording")
	}

	// Open audio device for recording
	user := unsafe.Pointer(ar)
	ar.pinner.Pin(user)
	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  2048,
		Callback: sdl.AudioCallback(C.audioInputCallback),
		UserData: user,
	}

	// Use SDL's default device mechanism for empty string
	var deviceID sdl.AudioDeviceID
	var err error
	if _, ok := ar.openDevices[deviceName]; ok { // Add device to openDevices if not present
		deviceID = ar.openDevices[deviceName]
	} else {
		deviceID, err = sdl.OpenAudioDevice(deviceName, true, &spec, nil, 0)
		if err != nil {
			ar.pinner.Unpin()
			return fmt.Errorf("failed to open audio device: %v", err)
		}
	}

	ar.deviceID = deviceID
	ar.recording = true
	ar.audioData = make([]int16, 0, AudioSampleRate*60) // Pre-allocate for up to 60 seconds

	// Start recording
	sdl.PauseAudioDevice(deviceID, false)

	ar.lg.Infof("Started audio recording from device: %s", deviceName)
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

	// Safe to unpin now that device is closed and callback won't run
	ar.pinner.Unpin()

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

// GetAudioInputDevices returns a list of available audio input devices
func GetAudioInputDevices() []string {
	count := sdl.GetNumAudioDevices(true) // true for capture devices
	devices := make([]string, 0, count)

	for i := 0; i < count; i++ {
		name := sdl.GetAudioDeviceName(i, true)
		if name != "" {
			devices = append(devices, name)
		}
	}

	return devices
}

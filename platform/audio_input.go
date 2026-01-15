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
	deviceID       sdl.AudioDeviceID
	deviceOpen     bool   // Whether the device is currently open
	currentDevice  string // Name of the currently open device
	recording      bool
	audioData      []int16
	streamCallback func(samples []int16) // Optional callback for streaming audio
	mu             sync.Mutex
	lg             *log.Logger
	pinner         runtime.Pinner
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

	// If a different device is requested, close the current one first
	if ar.deviceOpen && ar.currentDevice != deviceName {
		sdl.PauseAudioDevice(ar.deviceID, true)
		sdl.CloseAudioDevice(ar.deviceID)
		ar.pinner.Unpin()
		ar.deviceOpen = false
		ar.lg.Infof("Closed audio device %q to switch to %q", ar.currentDevice, deviceName)
	}

	// Open the device if not already open
	if !ar.deviceOpen {
		user := unsafe.Pointer(ar)
		ar.pinner.Pin(user)
		spec := sdl.AudioSpec{
			Freq:     AudioInputSampleRate,
			Format:   sdl.AUDIO_S16SYS,
			Channels: 1,
			Samples:  2048,
			Callback: sdl.AudioCallback(C.audioInputCallback),
			UserData: user,
		}

		// Empty string uses SDL's default device
		deviceID, err := sdl.OpenAudioDevice(deviceName, true, &spec, nil, 0)
		if err != nil {
			ar.pinner.Unpin()
			return fmt.Errorf("failed to open audio device: %v", err)
		}

		ar.deviceID = deviceID
		ar.deviceOpen = true
		ar.currentDevice = deviceName
		ar.lg.Infof("Opened audio device: %q", deviceName)
	}

	ar.recording = true
	ar.audioData = nil // Will grow dynamically as needed

	// Start recording (unpause the device)
	sdl.PauseAudioDevice(ar.deviceID, false)

	ar.lg.Infof("Started audio recording")
	return nil
}

// StopRecording stops recording and returns the recorded audio data
func (ar *AudioRecorder) StopRecording() ([]int16, error) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if !ar.recording {
		return nil, fmt.Errorf("not recording")
	}

	// Pause recording (but keep device open to avoid SDL audio subsystem issues)
	sdl.PauseAudioDevice(ar.deviceID, true)

	ar.recording = false
	audioData := ar.audioData
	ar.audioData = nil

	ar.lg.Infof("Stopped audio recording, captured %d samples", len(audioData))
	return audioData, nil
}

// Close closes the audio recording device. Should be called when the application exits.
func (ar *AudioRecorder) Close() {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.deviceOpen {
		sdl.PauseAudioDevice(ar.deviceID, true)
		sdl.CloseAudioDevice(ar.deviceID)
		ar.pinner.Unpin()
		ar.deviceOpen = false
		ar.lg.Info("Closed audio recording device")
	}
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
		if ar.streamCallback != nil {
			ar.streamCallback(data)
		}
	}
}

// SetStreamCallback sets a callback function that receives audio samples
// as they are recorded. This enables streaming audio to a transcriber.
// Pass nil to disable the callback.
func (ar *AudioRecorder) SetStreamCallback(cb func([]int16)) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.streamCallback = cb
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

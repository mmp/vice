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

// PrerollDuration is how much audio to buffer before PTT for capturing transmission starts.
const PrerollDuration = 200 // milliseconds

// prerollSamples is the number of samples to buffer (200ms at 16kHz = 3200 samples)
const prerollSamples = AudioInputSampleRate * PrerollDuration / 1000

// AudioRecorder handles microphone recording
type AudioRecorder struct {
	deviceID       sdl.AudioDeviceID
	deviceOpen     bool   // Whether the device is currently open
	currentDevice  string // Name of the currently open device
	capturing      bool   // Whether device is actively capturing to preroll buffer
	recording      bool   // Whether we're accumulating audio for a transmission
	audioData      []int16
	prerollBuffer  []int16               // Ring buffer for pre-PTT audio
	prerollPos     int                   // Write position in ring buffer
	streamCallback func(samples []int16) // Optional callback for streaming audio
	mu             sync.Mutex
	lg             *log.Logger
	pinner         runtime.Pinner
}

// NewAudioRecorder creates a new audio recorder
func NewAudioRecorder(lg *log.Logger) *AudioRecorder {
	return &AudioRecorder{
		lg:            lg,
		prerollBuffer: make([]int16, prerollSamples),
	}
}

// StartCapture starts continuous background audio capture to the preroll buffer.
// This should be called when the app is ready to accept PTT input.
func (ar *AudioRecorder) StartCapture() error {
	return ar.StartCaptureWithDevice("")
}

// StartCaptureWithDevice starts continuous background audio capture from the specified device.
func (ar *AudioRecorder) StartCaptureWithDevice(deviceName string) error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.capturing {
		// Already capturing - check if device change is needed
		if ar.currentDevice == deviceName {
			return nil
		}
		// Different device requested - close and reopen
		sdl.PauseAudioDevice(ar.deviceID, true)
		sdl.CloseAudioDevice(ar.deviceID)
		ar.pinner.Unpin()
		ar.deviceOpen = false
		ar.capturing = false
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

	// Clear preroll buffer
	ar.prerollPos = 0
	for i := range ar.prerollBuffer {
		ar.prerollBuffer[i] = 0
	}

	ar.capturing = true
	sdl.PauseAudioDevice(ar.deviceID, false)
	ar.lg.Info("Started background audio capture")
	return nil
}

// StopCapture stops background audio capture.
func (ar *AudioRecorder) StopCapture() {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if !ar.capturing {
		return
	}

	sdl.PauseAudioDevice(ar.deviceID, true)
	ar.capturing = false
	ar.recording = false
	ar.lg.Info("Stopped background audio capture")
}

// IsCapturing returns true if background capture is active.
func (ar *AudioRecorder) IsCapturing() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.capturing
}

// StartRecording starts recording audio from the default microphone.
// If capture is already active, includes the preroll buffer.
func (ar *AudioRecorder) StartRecording() error {
	return ar.StartRecordingWithDevice("")
}

// StartRecordingWithDevice starts recording audio from the specified microphone.
// If capture is already active on this device, includes the preroll buffer.
// Otherwise, opens the device and starts fresh (no preroll).
func (ar *AudioRecorder) StartRecordingWithDevice(deviceName string) error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.recording {
		return fmt.Errorf("already recording")
	}

	// If capture is already running on the same device, use preroll
	if ar.capturing && ar.currentDevice == deviceName {
		// Extract preroll buffer in order (it's a ring buffer)
		ar.audioData = ar.getPrerollLocked()
		ar.recording = true
		ar.lg.Infof("Started recording with %d preroll samples", len(ar.audioData))
		return nil
	}

	// Need to open/switch device - no preroll available
	if ar.deviceOpen && ar.currentDevice != deviceName {
		sdl.PauseAudioDevice(ar.deviceID, true)
		sdl.CloseAudioDevice(ar.deviceID)
		ar.pinner.Unpin()
		ar.deviceOpen = false
		ar.capturing = false
		ar.lg.Infof("Closed audio device %q to switch to %q", ar.currentDevice, deviceName)
	}

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
	ar.capturing = true // Also start capturing for preroll on next recording
	ar.audioData = nil
	sdl.PauseAudioDevice(ar.deviceID, false)
	ar.lg.Info("Started audio recording (no preroll)")
	return nil
}

// getPrerollLocked extracts the preroll buffer contents in chronological order.
// Must be called with ar.mu held.
func (ar *AudioRecorder) getPrerollLocked() []int16 {
	result := make([]int16, prerollSamples)
	// Copy from prerollPos to end, then from start to prerollPos
	copy(result, ar.prerollBuffer[ar.prerollPos:])
	copy(result[prerollSamples-ar.prerollPos:], ar.prerollBuffer[:ar.prerollPos])
	return result
}

// GetPreroll returns a copy of the current preroll buffer contents.
// This is useful for feeding preroll samples to a transcriber when starting recording.
func (ar *AudioRecorder) GetPreroll() []int16 {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if !ar.capturing {
		return nil
	}
	return ar.getPrerollLocked()
}

// StopRecording stops recording and returns the recorded audio data.
// If capture was active, it continues running for future preroll.
func (ar *AudioRecorder) StopRecording() ([]int16, error) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if !ar.recording {
		return nil, fmt.Errorf("not recording")
	}

	ar.recording = false
	audioData := ar.audioData
	ar.audioData = nil

	// If we're not in capture mode, pause the device
	if !ar.capturing {
		sdl.PauseAudioDevice(ar.deviceID, true)
	}

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
		ar.capturing = false
		ar.recording = false
		ar.lg.Info("Closed audio recording device")
	}
}

// IsRecording returns true if currently recording
func (ar *AudioRecorder) IsRecording() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.recording
}

// addAudioData adds audio data to the preroll and recording buffers.
// Called from the SDL audio callback.
func (ar *AudioRecorder) addAudioData(data []int16) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if !ar.capturing {
		return
	}

	// Always update preroll ring buffer when capturing
	for _, sample := range data {
		ar.prerollBuffer[ar.prerollPos] = sample
		ar.prerollPos = (ar.prerollPos + 1) % prerollSamples
	}

	// If actively recording, also accumulate and stream
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

// platform/sdl2/audio_input.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sdl2

// typedef unsigned char uint8;
// void audioInputCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform/audio"

	"github.com/veandco/go-sdl2/sdl"
)

// prerollDuration is how much audio to buffer before PTT for capturing transmission starts.
const prerollDuration = 200 // milliseconds

// prerollSamples is the number of samples to buffer (200ms at 16kHz = 3200 samples)
const prerollSamples = audio.InputSampleRate * prerollDuration / 1000

// recorder handles microphone recording
type recorder struct {
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

// newRecorder creates a new audio recorder
func newRecorder(lg *log.Logger) *recorder {
	return &recorder{
		lg:            lg,
		prerollBuffer: make([]int16, prerollSamples),
	}
}

// startCapture starts continuous background audio capture to the preroll buffer.
// This should be called when the app is ready to accept PTT input.
func (ar *recorder) startCapture() error {
	return ar.startCaptureWithDevice("")
}

// startCaptureWithDevice starts continuous background audio capture from the specified device.
func (ar *recorder) startCaptureWithDevice(deviceName string) error {
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
			Freq:     audio.InputSampleRate,
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

// stopCapture stops background audio capture.
func (ar *recorder) stopCapture() {
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

// isCapturing returns true if background capture is active.
func (ar *recorder) isCapturing() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.capturing
}

// startRecording starts recording audio from the default microphone.
// If capture is already active, includes the preroll buffer.
func (ar *recorder) startRecording() error {
	return ar.startRecordingWithDevice("")
}

// startRecordingWithDevice starts recording audio from the specified microphone.
// If capture is already active on this device, includes the preroll buffer.
// Otherwise, opens the device and starts fresh (no preroll).
func (ar *recorder) startRecordingWithDevice(deviceName string) error {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.recording {
		return fmt.Errorf("already recording")
	}

	// If capture is already running on the same device, use preroll
	if ar.capturing && ar.currentDevice == deviceName {
		// Extract preroll buffer in order (it's a ring buffer)
		ar.audioData = ar.prerollLocked()
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
			Freq:     audio.InputSampleRate,
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

// prerollLocked extracts the preroll buffer contents in chronological order.
// Must be called with ar.mu held.
func (ar *recorder) prerollLocked() []int16 {
	result := make([]int16, prerollSamples)
	// Copy from prerollPos to end, then from start to prerollPos
	copy(result, ar.prerollBuffer[ar.prerollPos:])
	copy(result[prerollSamples-ar.prerollPos:], ar.prerollBuffer[:ar.prerollPos])
	return result
}

// preroll returns a copy of the current preroll buffer contents.
// This is useful for feeding preroll samples to a transcriber when starting recording.
func (ar *recorder) preroll() []int16 {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if !ar.capturing {
		return nil
	}
	return ar.prerollLocked()
}

// stopRecording stops recording and returns the recorded audio data.
// If capture was active, it continues running for future preroll.
func (ar *recorder) stopRecording() ([]int16, error) {
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

// close closes the audio recording device. Should be called when the application exits.
func (ar *recorder) close() {
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

// isRecording returns true if currently recording
func (ar *recorder) isRecording() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.recording
}

// addAudioData adds audio data to the preroll and recording buffers.
// Called from the SDL audio callback.
func (ar *recorder) addAudioData(data []int16) {
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

// setStreamCallback sets a callback function that receives audio samples
// as they are recorded. This enables streaming audio to a transcriber.
// Pass nil to disable the callback.
func (ar *recorder) setStreamCallback(cb func([]int16)) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.streamCallback = cb
}

// inputDevices returns a list of available audio input devices
func inputDevices() []string {
	count := sdl.GetNumAudioDevices(true) // true for capture devices
	if count < 0 {
		// SDL returns -1 when it can't enumerate capture devices, which
		// includes the case where the audio subsystem failed to initialize;
		// only the default device is available then.
		return nil
	}

	devices := make([]string, 0, count)

	for i := 0; i < count; i++ {
		name := sdl.GetAudioDeviceName(i, true)
		if name != "" {
			devices = append(devices, name)
		}
	}

	return devices
}

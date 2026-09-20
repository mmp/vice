// platform/audio/audio.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package audio defines the interface to vice's audio playback and capture
// without pulling in any particular implementation of it.
package audio

import (
	"errors"
	"time"
)

const SampleRate = 44100
const InputSampleRate = 16000 // Whisper's native sample rate

var (
	ErrCurrentlyPlayingSpeech = errors.New("Speech is currently playing")
	ErrPlaybackUnavailable    = errors.New("Audio playback is unavailable")
)

// Engine is the interface to audio effect playback, speech synthesis
// playback, and microphone capture.
type Engine interface {
	// AddPCM registers an audio effect encoded via pulse code modulation.
	// It is assumed to be one channel audio sampled at SampleRate.
	// The integer return value identifies the effect and can be passed to
	// the audio playing entrypoints.
	AddPCM(pcm []byte, rate int) (int, error)

	// Registers an MP3-based audio effect. As with AddPCM, assumes one
	// channel sampled at SampleRate. The integer return value
	// identifies the effect and can be passed to the audio playing
	// entrypoints.
	AddMP3(mp3 []byte) (int, error)

	// TryEnqueueSpeechPCM queues pre-decoded PCM speech audio for playback.
	// If speech is currently being played, ErrCurrentlyPlayingSpeech is
	// returned and the caller should try again later; any other error means
	// the audio can't be played at all and should be discarded. If non-nil,
	// the provided callback function is called after the speech has
	// finished; it is not called if an error is returned.
	TryEnqueueSpeechPCM(pcm []int16, finished func()) error

	// AppendSpeechPCM appends PCM samples to the speech playback queue.
	// Unlike TryEnqueueSpeechPCM, this does not fail if speech is already
	// playing; it simply appends to the existing queue.
	AppendSpeechPCM(pcm []int16)

	// StopSpeech discards any speech audio that is queued or playing, so
	// that the next thing enqueued is heard immediately. The callback given
	// to TryEnqueueSpeechPCM is not called for the canceled audio.
	StopSpeech()

	// SetSpeechGarbled enables or disables garbling of speech audio.
	// When enabled, speech is ducked and static noise is added.
	SetSpeechGarbled(garbled bool)

	// IsPlayingSpeech returns true if speech audio is currently playing.
	IsPlayingSpeech() bool

	// RemainingSpeechDuration returns how much speech audio is still
	// queued for playback. Returns 0 when no speech is playing.
	RemainingSpeechDuration() time.Duration

	// SetAudioVolume sets the volume for audio playback; the value passed
	// should be between 0 and 10.
	SetAudioVolume(vol int)

	// PlayAudioOnce plays the audio effect identified by the given identifier
	// once. Multiple audio effects may be played simultaneously.
	PlayAudioOnce(id int)

	// StartPlayAudioContinuous	starts playing the specified audio effect
	// continuously, until StopPlayAudioContinuous is called.
	StartPlayAudioContinuous(id int)

	// StopPlayAudio stops playback of the audio effect specified
	// by the given identifier.
	StopPlayAudio(id int)

	// Audio capture methods for continuous background capture with preroll buffer.
	// StartAudioCapture begins capturing to the preroll buffer (200ms ring buffer).
	// This allows recording to include audio from before PTT was pressed.
	StartAudioCapture() error
	StartAudioCaptureWithDevice(deviceName string) error
	StopAudioCapture()
	IsAudioCapturing() bool
	// GetAudioPreroll returns the current preroll buffer (200ms of audio before now).
	// Returns nil if not capturing.
	GetAudioPreroll() []int16

	// Audio recording methods. If capture is active, recording includes preroll.
	StartAudioRecording() error
	StartAudioRecordingWithDevice(deviceName string) error
	StopAudioRecording() ([]int16, error)
	IsAudioRecording() bool
	GetAudioInputDevices() []string

	// AudioPlaybackError returns a non-nil error if the audio output device
	// couldn't be opened at startup; no audio will be heard in that case.
	// It doesn't imply that audio capture is unavailable.
	AudioPlaybackError() error

	// SetAudioStreamCallback sets a callback that receives audio samples
	// as they are recorded. This enables streaming audio to a transcriber.
	// Pass nil to disable the callback.
	SetAudioStreamCallback(cb func([]int16))

	// Dispose closes the audio devices and frees the associated resources.
	Dispose()
}

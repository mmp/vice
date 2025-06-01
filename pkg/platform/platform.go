// pkg/platform/platform.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

import (
	"errors"

	"github.com/mmp/vice/pkg/math"

	"github.com/AllenDang/cimgui-go/imgui"
)

var ErrCurrentlyPlayingSpeech = errors.New("Speech is currently playing")

// Platform is the interface that abstracts platform-specific features like
// creating windows, mouse and keyboard handling, etc.
type Platform interface {
	// NewFrame marks the begin of a render pass; it forwards all current state to imgui IO.
	NewFrame()

	// ProcessEvents handles all pending window events. Returns true if
	// there were any events and false otherwise.
	ProcessEvents() bool

	// PostRender performs the buffer swap.
	PostRender()

	// Dispose is called when the application is shutting down and is when
	// resources are be freed.
	Dispose()

	// ShouldStop returns true if the window is to be closed.
	ShouldStop() bool

	// CancelShouldStop cancels a user's request to close the window.
	CancelShouldStop()

	// SetWindowTitle sets the title of the appllication window.
	SetWindowTitle(text string)

	// InputCharacters returns a string of all the characters (generally at most one!) that have
	// been entered since the last call to ProcessEvents.
	InputCharacters() string

	// EnableVSync specifies whether v-sync should be used when rendering;
	// v-sync is on by default and should only be disabled for benchmarking.
	EnableVSync(sync bool)

	// EnableFullScreen switches between the application running in windowed and fullscreen mode.
	EnableFullScreen(fullscreen bool)

	// IsFullScreen() returns true if the application is in full-screen mode.
	IsFullScreen() bool

	// GetAllMonitorNames() returns an array of all available monitors' names.
	GetAllMonitorNames() []string

	// DisplaySize returns the dimension of the display.
	DisplaySize() [2]float32

	// WindowSize returns the size of the window.
	WindowSize() [2]int

	// WindowSize returns the position of the window on the screen.
	WindowPosition() [2]int

	// FramebufferSize returns the dimension of the framebuffer.
	FramebufferSize() [2]float32

	// GetClipboard() returns an object that implements the imgui.Clipboard
	// interface so that copy and paste can be supported.
	GetClipboard() imgui.ClipboardHandler

	// Enables a mode where the mouse is constrained to be within the
	// specified pixel extent, specified in window coordinates.
	StartCaptureMouse(e math.Extent2D)

	// Disable mouse capture.
	EndCaptureMouse()

	// Enter/leave a mouse capture mode where the change in mouse position
	// is returned each frame through MouseState DeltaPos. The cursor is
	// hidden and the returned mouse position is kept fixed.
	StartMouseDeltaMode()
	StopMouseDeltaMode()

	// Moves the mouse cursor to the given position specified in window coordinates.
	SetMousePosition([2]float32)

	// Scaling factor to account for Retina-style displays
	DPIScale() float32

	// GetMouse returns a MouseState object that encapsulates the current state
	// of the mouse (position, buttons pressed, mouse wheel motion, etc.)
	GetMouse() *MouseState

	// GetKeyboard returns a KeyboardState object that stores keyboard input
	// and which keys are currently down.
	GetKeyboard() *KeyboardState

	// AddPCM registers an audio effect encoded via pulse code modulation.
	// It is assumed to be one channel audio sampled at AudioSampleRate.
	// The integer return value identifies the effect and can be passed to
	// the audio playing entrypoints.
	AddPCM(pcm []byte, rate int) (int, error)

	// Registers an MP3-based audio effect. As with AddPCM, assumes one
	// channel sampled at AudioSampleRate. The integer return value
	// identifies the effect and can be passed to the audio playing
	// entrypoints.
	AddMP3(mp3 []byte) (int, error)

	// Possibly MP3 audio from speech synthesis; it will be played once and
	// discarded. If speech is currently being played,
	// ErrCurrentlyPlayingSpeech is returned. If non-nil, the provided
	// callback function is called after the speech has finished.
	TryEnqueueSpeechMP3(mp3 []byte, finished func()) error

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
}

// platform/platform.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package platform abstracts the operating system services vice needs:
// creating windows, mouse and keyboard handling, and audio. The
// implementations live in subpackages so that code that only uses the
// interfaces doesn't pull in GLFW or SDL2.
package platform

import (
	"image"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform/audio"

	"github.com/AllenDang/cimgui-go/imgui"
)

// Config holds the window settings that are remembered between runs.
type Config struct {
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int

	MainWindowSquare bool

	EnableMSAA bool

	StartInFullScreen bool
	FullScreenMonitor int
}

// Window is the interface to the application window and to the mouse and
// keyboard input that arrives through it.
type Window interface {
	// NewFrame marks the begin of a render pass; it forwards all current state to imgui IO.
	NewFrame()

	// ProcessEvents handles all pending window events. Returns true if
	// there were any events and false otherwise.
	ProcessEvents() bool

	// PostRender performs the buffer swap.
	PostRender()

	// RenderImgui finalizes imgui's draw lists for the frame, draws them,
	// and then updates and draws any secondary viewport windows, leaving
	// the main window's context current.
	RenderImgui()

	// MakeContextCurrent makes the main window's OpenGL context current.
	// Used to restore state after rendering secondary viewport windows.
	MakeContextCurrent()

	// InitViewportBackends initializes the imgui GLFW and OpenGL3 backends
	// for multi-viewport support. Must be called after OpenGL is initialized.
	InitViewportBackends()

	// SetViewportFloating sets the OS-level always-on-top attribute for the
	// secondary viewport window with the given imgui platform handle. Only
	// windows dragged outside the main window have one.
	SetViewportFloating(handle uintptr, floating bool)

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

	// SetMainWindowSquare enables or disables a square aspect ratio for the main window.
	SetMainWindowSquare(square bool)

	// IsAppFocused returns true if the application has OS focus.
	IsAppFocused() bool

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

	// CreateCursorFromImage builds a Cursor from an in-memory RGBA image with
	// the given hotspot. The caller retains ownership of img.
	CreateCursorFromImage(img *image.RGBA, hotspotX, hotspotY int) (Cursor, error)

	// ClearCursorOverride removes any cursor override set with
	// Cursor.SetOverride.
	ClearCursorOverride()
}

// Platform is everything the application needs from the operating system:
// a window to draw in, the input that arrives through it, and audio.
type Platform interface {
	Window
	audio.Engine
}

// Join returns a Platform that dispatches windowing calls to w and audio
// calls to a.
func Join(w Window, a audio.Engine) Platform {
	return joined{Window: w, Engine: a}
}

type joined struct {
	Window
	audio.Engine
}

// Dispose is given explicitly since both halves of the Platform declare it.
func (j joined) Dispose() {
	j.Engine.Dispose()
	j.Window.Dispose()
}

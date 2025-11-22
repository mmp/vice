// pkg/platform/sdl2_windows.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build windows

package platform

import (
	"fmt"
	gomath "math"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/veandco/go-sdl2/sdl"
)

// sdl2Platform implements the Platform interface using SDL2 for Windows.
type sdl2Platform struct {
	audioEngine

	imguiIO *imgui.IO

	window *sdl.Window
	config *Config

	time            uint64
	inputCharacters string
	anyEvents       bool
	lastMouseX      int32
	lastMouseY      int32
	windowTitle     string
	mouseCapture    math.Extent2D
	heldFKeys       map[imgui.Key]interface{}

	mouseDeltaMode         bool
	mouseDeltaStartPos     [2]float32
	mouseDeltaWindowCenter [2]float32
	mouseDelta             [2]float32

	shouldStop bool
}

// Config is defined in glfw.go for non-Windows, redefined here for Windows
type Config struct {
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int

	EnableMSAA bool

	StartInFullScreen bool
	FullScreenMonitor int
}

// New returns a new instance of a Platform implemented with SDL2.
func New(config *Config, lg *log.Logger) (Platform, error) {
	lg.Info("Starting SDL2 initialization for Windows")

	// Note: SDL_INIT_AUDIO is already done by audioEngine.Initialize
	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_EVENTS); err != nil {
		return nil, fmt.Errorf("failed to initialize SDL2: %w", err)
	}

	version := sdl.Version{}
	sdl.GetVersion(&version)
	lg.Infof("SDL2: %d.%d.%d", version.Major, version.Minor, version.Patch)

	io := imgui.CurrentIO()
	io.SetBackendFlags(io.BackendFlags() | imgui.BackendFlagsHasMouseCursors)

	// Get display bounds to set default window size
	displayBounds, err := sdl.GetDisplayBounds(0)
	if err == nil {
		if config.InitialWindowSize[0] == 0 || config.InitialWindowSize[1] == 0 {
			config.InitialWindowSize[0] = int(displayBounds.W) - 200
			config.InitialWindowSize[1] = int(displayBounds.H) - 300
		}
	}

	// Validate window position
	if config.InitialWindowPosition[0] < 0 || config.InitialWindowPosition[1] < 0 ||
		config.InitialWindowPosition[0] > int(displayBounds.W) || config.InitialWindowPosition[1] > int(displayBounds.H) {
		config.InitialWindowPosition = [2]int{100, 100}
	}

	// Create window
	flags := sdl.WINDOW_SHOWN | sdl.WINDOW_RESIZABLE | sdl.WINDOW_ALLOW_HIGHDPI

	var window *sdl.Window

	numDisplays, _ := sdl.GetNumVideoDisplays()
	if config.FullScreenMonitor >= numDisplays {
		config.FullScreenMonitor = 0
	}

	if config.StartInFullScreen {
		flags |= sdl.WINDOW_FULLSCREEN_DESKTOP
	}

	window, err = sdl.CreateWindow(
		"vice",
		int32(config.InitialWindowPosition[0]),
		int32(config.InitialWindowPosition[1]),
		int32(config.InitialWindowSize[0]),
		int32(config.InitialWindowSize[1]),
		flags,
	)
	if err != nil {
		sdl.Quit()
		return nil, fmt.Errorf("failed to create window: %w", err)
	}

	platform := &sdl2Platform{
		config:    config,
		imguiIO:   io,
		window:    window,
		heldFKeys: make(map[imgui.Key]interface{}),
	}

	// Enable text input
	sdl.StartTextInput()

	lg.Info("Finished SDL2 initialization")

	platform.audioEngine.Initialize(lg)

	return platform, nil
}

func (p *sdl2Platform) DPIScale() float32 {
	// Get DPI scale from SDL2
	ddpi, _, _, err := sdl.GetDisplayDPI(0)
	if err != nil {
		return 1.0
	}
	// 96 DPI is the baseline
	return ddpi / 96.0
}

func (p *sdl2Platform) EnableVSync(sync bool) {
	// VSync is controlled by the renderer (D3D9) present parameters
	// This is a no-op for SDL2 platform since we're not using OpenGL
}

func (p *sdl2Platform) GetAllMonitorNames() []string {
	numDisplays, err := sdl.GetNumVideoDisplays()
	if err != nil {
		return nil
	}

	var names []string
	for i := 0; i < numDisplays; i++ {
		name, err := sdl.GetDisplayName(i)
		if err != nil {
			name = fmt.Sprintf("Display %d", i)
		}
		names = append(names, fmt.Sprintf("(%d) %s", i, name))
	}
	return names
}

func (p *sdl2Platform) EnableFullScreen(fullscreen bool) {
	if fullscreen {
		p.window.SetFullscreen(uint32(sdl.WINDOW_FULLSCREEN_DESKTOP))
		p.config.StartInFullScreen = true
	} else {
		p.window.SetFullscreen(0)
		p.config.StartInFullScreen = false
	}
}

func (p *sdl2Platform) IsFullScreen() bool {
	flags := p.window.GetFlags()
	return (flags & sdl.WINDOW_FULLSCREEN_DESKTOP) != 0
}

func (p *sdl2Platform) Dispose() {
	sdl.StopTextInput()
	p.window.Destroy()
	sdl.Quit()
}

func (p *sdl2Platform) InputCharacters() string {
	return p.inputCharacters
}

func (p *sdl2Platform) ShouldStop() bool {
	return p.shouldStop
}

func (p *sdl2Platform) CancelShouldStop() {
	p.shouldStop = false
}

func (p *sdl2Platform) ProcessEvents() bool {
	p.inputCharacters = ""
	p.anyEvents = false

	for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
		p.anyEvents = true

		switch e := event.(type) {
		case *sdl.QuitEvent:
			p.shouldStop = true

		case *sdl.WindowEvent:
			// Handle window events if needed

		case *sdl.MouseMotionEvent:
			// Mouse motion handled in NewFrame

		case *sdl.MouseButtonEvent:
			p.handleMouseButton(e)

		case *sdl.MouseWheelEvent:
			p.imguiIO.AddMouseWheelDelta(float32(e.X), float32(e.Y))

		case *sdl.KeyboardEvent:
			p.handleKeyboard(e)

		case *sdl.TextInputEvent:
			text := e.GetText()
			p.imguiIO.AddInputCharactersUTF8(text)
			p.inputCharacters += text
		}
	}

	if p.anyEvents {
		return true
	}

	// Check for mouse movement
	x, y, _ := sdl.GetMouseState()
	if x != p.lastMouseX || y != p.lastMouseY {
		p.lastMouseX, p.lastMouseY = x, y
		return true
	}

	return false
}

func (p *sdl2Platform) handleMouseButton(e *sdl.MouseButtonEvent) {
	var button int
	switch e.Button {
	case sdl.BUTTON_LEFT:
		button = int(MouseButtonPrimary)
	case sdl.BUTTON_RIGHT:
		button = int(MouseButtonSecondary)
	case sdl.BUTTON_MIDDLE:
		button = int(MouseButtonTertiary)
	default:
		return
	}

	if e.Type == sdl.MOUSEBUTTONDOWN {
		p.imguiIO.SetMouseButtonDown(button, true)
	} else {
		p.imguiIO.SetMouseButtonDown(button, false)
	}

	p.updateKeyModifiers()
}

func (p *sdl2Platform) handleKeyboard(e *sdl.KeyboardEvent) {
	p.updateKeyModifiers()

	// Track function keys
	for i := 0; i < 16; i++ {
		key := sdl.K_F1 + sdl.Keycode(i)
		state := sdl.GetKeyboardState()
		scancode := sdl.GetScancodeFromKey(key)
		if state[scancode] != 0 {
			p.heldFKeys[imgui.KeyF1+imgui.Key(i)] = nil
		} else {
			delete(p.heldFKeys, imgui.KeyF1+imgui.Key(i))
		}
	}

	imguiKey := sdlKeyToImguiKey(e.Keysym.Sym)
	if imguiKey != imgui.KeyNone {
		p.imguiIO.AddKeyEvent(imguiKey, e.Type == sdl.KEYDOWN)
	}
}

func (p *sdl2Platform) updateKeyModifiers() {
	mod := sdl.GetModState()
	p.imguiIO.AddKeyEvent(imgui.ModShift, (mod&sdl.KMOD_SHIFT) != 0)
	p.imguiIO.AddKeyEvent(imgui.ModCtrl, (mod&sdl.KMOD_CTRL) != 0)
	p.imguiIO.AddKeyEvent(imgui.ModAlt, (mod&sdl.KMOD_ALT) != 0)
	p.imguiIO.AddKeyEvent(imgui.ModSuper, (mod&sdl.KMOD_GUI) != 0)
}

func (p *sdl2Platform) DisplaySize() [2]float32 {
	w, h := p.window.GetSize()
	return [2]float32{float32(w), float32(h)}
}

func (p *sdl2Platform) WindowSize() [2]int {
	w, h := p.window.GetSize()
	return [2]int{int(w), int(h)}
}

func (p *sdl2Platform) WindowPosition() [2]int {
	x, y := p.window.GetPosition()
	return [2]int{int(x), int(y)}
}

func (p *sdl2Platform) FramebufferSize() [2]float32 {
	w, h := p.window.GetSize()
	// For SDL2, window size is typically the same as framebuffer size on Windows
	// unless using high DPI
	return [2]float32{float32(w), float32(h)}
}

func (p *sdl2Platform) NewFrame() {
	// Setup display size
	displaySize := p.DisplaySize()
	p.imguiIO.SetDisplaySize(imgui.Vec2{X: displaySize[0], Y: displaySize[1]})

	// Setup time step
	currentTime := sdl.GetPerformanceCounter()
	frequency := sdl.GetPerformanceFrequency()
	if p.time > 0 {
		deltaTime := float32(currentTime-p.time) / float32(frequency)
		p.imguiIO.SetDeltaTime(deltaTime)
	}
	p.time = currentTime

	// Setup mouse position
	flags := p.window.GetFlags()
	if (flags & sdl.WINDOW_INPUT_FOCUS) != 0 {
		x, y, _ := sdl.GetMouseState()
		pc := [2]float32{float32(x), float32(y)}

		if p.mouseCapture.Width() > 0 && p.mouseCapture.Height() > 0 && !p.mouseCapture.Inside(pc) {
			pc = p.mouseCapture.ClosestPointInBox(pc)
		}
		p.imguiIO.SetMousePos(imgui.Vec2{X: pc[0], Y: pc[1]})
	} else {
		p.imguiIO.SetMousePos(imgui.Vec2{X: -gomath.MaxFloat32, Y: -gomath.MaxFloat32})
	}

	// Handle mouse capture bounds
	if p.mouseCapture.Width() > 0 && p.mouseCapture.Height() > 0 {
		x, y, _ := sdl.GetMouseState()
		pc := [2]float32{float32(x), float32(y)}
		if !p.mouseCapture.Inside(pc) {
			pc = p.mouseCapture.ClosestPointInBox(pc)
			p.window.WarpMouseInWindow(int32(pc[0]), int32(pc[1]))
		}
	}

	// Handle delta mode
	if p.mouseDeltaMode {
		x, y, _ := sdl.GetMouseState()
		currentPos := [2]float32{float32(x), float32(y)}
		p.mouseDelta = math.Sub2f(currentPos, p.mouseDeltaWindowCenter)
		p.window.WarpMouseInWindow(int32(p.mouseDeltaWindowCenter[0]), int32(p.mouseDeltaWindowCenter[1]))
	}

	// Update cursor
	cursor := imgui.CurrentMouseCursor()
	if p.mouseDeltaMode || cursor == imgui.MouseCursorNone {
		sdl.ShowCursor(sdl.DISABLE)
	} else {
		sdl.ShowCursor(sdl.ENABLE)
		// Set appropriate cursor based on imgui cursor
		var sdlCursor *sdl.Cursor
		switch cursor {
		case imgui.MouseCursorArrow:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_ARROW)
		case imgui.MouseCursorTextInput:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_IBEAM)
		case imgui.MouseCursorResizeNS:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_SIZENS)
		case imgui.MouseCursorResizeEW:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_SIZEWE)
		case imgui.MouseCursorHand:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_HAND)
		default:
			sdlCursor = sdl.CreateSystemCursor(sdl.SYSTEM_CURSOR_ARROW)
		}
		if sdlCursor != nil {
			sdl.SetCursor(sdlCursor)
		}
	}
}

func (p *sdl2Platform) PostRender() {
	// Buffer swap is handled by the D3D9 renderer's Present call
}

func (p *sdl2Platform) SetWindowTitle(text string) {
	if text != p.windowTitle {
		p.window.SetTitle(text)
		p.windowTitle = text
	}
}

func (p *sdl2Platform) GetClipboard() imgui.ClipboardHandler {
	return sdl2Clipboard{}
}

type sdl2Clipboard struct{}

func (cb sdl2Clipboard) GetClipboard() string {
	text, _ := sdl.GetClipboardText()
	return text
}

func (cb sdl2Clipboard) SetClipboard(text string) {
	sdl.SetClipboardText(text)
}

func (p *sdl2Platform) StartCaptureMouse(e math.Extent2D) {
	p.StopMouseDeltaMode()
	p.mouseCapture = math.Extent2D{
		P0: [2]float32{math.Ceil(e.P0[0]), math.Ceil(e.P0[1])},
		P1: [2]float32{math.Floor(e.P1[0]), math.Floor(e.P1[1])},
	}
}

func (p *sdl2Platform) EndCaptureMouse() {
	p.mouseCapture = math.Extent2D{}
}

func (p *sdl2Platform) StartMouseDeltaMode() {
	p.EndCaptureMouse()

	p.mouseDeltaMode = true
	p.mouseDelta = [2]float32{}
	x, y, _ := sdl.GetMouseState()
	p.mouseDeltaStartPos = [2]float32{float32(x), float32(y)}

	// Put mouse at center of window
	wsz := p.WindowSize()
	wsz[0] /= 2
	wsz[1] /= 2
	p.window.WarpMouseInWindow(int32(wsz[0]), int32(wsz[1]))
	p.mouseDeltaWindowCenter = [2]float32{float32(wsz[0]), float32(wsz[1])}
}

func (p *sdl2Platform) StopMouseDeltaMode() {
	if p.mouseDeltaMode {
		p.mouseDeltaMode = false
		p.SetMousePosition(p.mouseDeltaStartPos)
	}
}

func (p *sdl2Platform) SetMousePosition(pos [2]float32) {
	p.window.WarpMouseInWindow(int32(pos[0]), int32(pos[1]))
}

func (p *sdl2Platform) GetMouse() *MouseState {
	x, y, buttons := sdl.GetMouseState()
	pos := [2]float32{float32(x), float32(y)}

	// Apply mouse capture
	if p.mouseCapture.Width() > 0 && p.mouseCapture.Height() > 0 && !p.mouseCapture.Inside(pos) {
		pos = p.mouseCapture.ClosestPointInBox(pos)
	}

	state := &MouseState{
		Pos: pos,
	}

	if p.mouseDeltaMode {
		state.DeltaPos = p.mouseDelta
	}

	state.Down[MouseButtonPrimary] = (buttons & sdl.ButtonLMask()) != 0
	state.Down[MouseButtonSecondary] = (buttons & sdl.ButtonRMask()) != 0
	state.Down[MouseButtonTertiary] = (buttons & sdl.ButtonMMask()) != 0

	return state
}

func (p *sdl2Platform) GetKeyboard() *KeyboardState {
	return &KeyboardState{
		Input:     p.inputCharacters,
		Pressed:   make(map[imgui.Key]interface{}),
		HeldFKeys: p.heldFKeys,
	}
}

func (p *sdl2Platform) WindowHandle() uintptr {
	info, err := p.window.GetWMInfo()
	if err != nil {
		return 0
	}
	// On Windows, we need to get the HWND from the SysWMInfo
	// The info.GetWindowsInfo() returns the Windows-specific info
	return uintptr(info.GetWindowsInfo().Window)
}

// sdlKeyToImguiKey converts SDL keycodes to imgui keys
func sdlKeyToImguiKey(key sdl.Keycode) imgui.Key {
	switch key {
	case sdl.K_TAB:
		return imgui.KeyTab
	case sdl.K_LEFT:
		return imgui.KeyLeftArrow
	case sdl.K_RIGHT:
		return imgui.KeyRightArrow
	case sdl.K_UP:
		return imgui.KeyUpArrow
	case sdl.K_DOWN:
		return imgui.KeyDownArrow
	case sdl.K_PAGEUP:
		return imgui.KeyPageUp
	case sdl.K_PAGEDOWN:
		return imgui.KeyPageDown
	case sdl.K_HOME:
		return imgui.KeyHome
	case sdl.K_END:
		return imgui.KeyEnd
	case sdl.K_INSERT:
		return imgui.KeyInsert
	case sdl.K_DELETE:
		return imgui.KeyDelete
	case sdl.K_BACKSPACE:
		return imgui.KeyBackspace
	case sdl.K_SPACE:
		return imgui.KeySpace
	case sdl.K_RETURN:
		return imgui.KeyEnter
	case sdl.K_ESCAPE:
		return imgui.KeyEscape
	case sdl.K_QUOTE:
		return imgui.KeyApostrophe
	case sdl.K_COMMA:
		return imgui.KeyComma
	case sdl.K_MINUS:
		return imgui.KeyMinus
	case sdl.K_PERIOD:
		return imgui.KeyPeriod
	case sdl.K_SLASH:
		return imgui.KeySlash
	case sdl.K_SEMICOLON:
		return imgui.KeySemicolon
	case sdl.K_EQUALS:
		return imgui.KeyEqual
	case sdl.K_LEFTBRACKET:
		return imgui.KeyLeftBracket
	case sdl.K_BACKSLASH:
		return imgui.KeyBackslash
	case sdl.K_RIGHTBRACKET:
		return imgui.KeyRightBracket
	case sdl.K_BACKQUOTE:
		return imgui.KeyGraveAccent
	case sdl.K_CAPSLOCK:
		return imgui.KeyCapsLock
	case sdl.K_SCROLLLOCK:
		return imgui.KeyScrollLock
	case sdl.K_NUMLOCKCLEAR:
		return imgui.KeyNumLock
	case sdl.K_PRINTSCREEN:
		return imgui.KeyPrintScreen
	case sdl.K_PAUSE:
		return imgui.KeyPause
	case sdl.K_KP_0:
		return imgui.KeyKeypad0
	case sdl.K_KP_1:
		return imgui.KeyKeypad1
	case sdl.K_KP_2:
		return imgui.KeyKeypad2
	case sdl.K_KP_3:
		return imgui.KeyKeypad3
	case sdl.K_KP_4:
		return imgui.KeyKeypad4
	case sdl.K_KP_5:
		return imgui.KeyKeypad5
	case sdl.K_KP_6:
		return imgui.KeyKeypad6
	case sdl.K_KP_7:
		return imgui.KeyKeypad7
	case sdl.K_KP_8:
		return imgui.KeyKeypad8
	case sdl.K_KP_9:
		return imgui.KeyKeypad9
	case sdl.K_KP_PERIOD:
		return imgui.KeyKeypadDecimal
	case sdl.K_KP_DIVIDE:
		return imgui.KeyKeypadDivide
	case sdl.K_KP_MULTIPLY:
		return imgui.KeyKeypadMultiply
	case sdl.K_KP_MINUS:
		return imgui.KeyKeypadSubtract
	case sdl.K_KP_PLUS:
		return imgui.KeyKeypadAdd
	case sdl.K_KP_ENTER:
		return imgui.KeyKeypadEnter
	case sdl.K_KP_EQUALS:
		return imgui.KeyKeypadEqual
	case sdl.K_LSHIFT:
		return imgui.KeyLeftShift
	case sdl.K_LCTRL:
		return imgui.KeyLeftCtrl
	case sdl.K_LALT:
		return imgui.KeyLeftAlt
	case sdl.K_LGUI:
		return imgui.KeyLeftSuper
	case sdl.K_RSHIFT:
		return imgui.KeyRightShift
	case sdl.K_RCTRL:
		return imgui.KeyRightCtrl
	case sdl.K_RALT:
		return imgui.KeyRightAlt
	case sdl.K_RGUI:
		return imgui.KeyRightSuper
	case sdl.K_APPLICATION:
		return imgui.KeyMenu
	case sdl.K_0:
		return imgui.Key0
	case sdl.K_1:
		return imgui.Key1
	case sdl.K_2:
		return imgui.Key2
	case sdl.K_3:
		return imgui.Key3
	case sdl.K_4:
		return imgui.Key4
	case sdl.K_5:
		return imgui.Key5
	case sdl.K_6:
		return imgui.Key6
	case sdl.K_7:
		return imgui.Key7
	case sdl.K_8:
		return imgui.Key8
	case sdl.K_9:
		return imgui.Key9
	case sdl.K_a:
		return imgui.KeyA
	case sdl.K_b:
		return imgui.KeyB
	case sdl.K_c:
		return imgui.KeyC
	case sdl.K_d:
		return imgui.KeyD
	case sdl.K_e:
		return imgui.KeyE
	case sdl.K_f:
		return imgui.KeyF
	case sdl.K_g:
		return imgui.KeyG
	case sdl.K_h:
		return imgui.KeyH
	case sdl.K_i:
		return imgui.KeyI
	case sdl.K_j:
		return imgui.KeyJ
	case sdl.K_k:
		return imgui.KeyK
	case sdl.K_l:
		return imgui.KeyL
	case sdl.K_m:
		return imgui.KeyM
	case sdl.K_n:
		return imgui.KeyN
	case sdl.K_o:
		return imgui.KeyO
	case sdl.K_p:
		return imgui.KeyP
	case sdl.K_q:
		return imgui.KeyQ
	case sdl.K_r:
		return imgui.KeyR
	case sdl.K_s:
		return imgui.KeyS
	case sdl.K_t:
		return imgui.KeyT
	case sdl.K_u:
		return imgui.KeyU
	case sdl.K_v:
		return imgui.KeyV
	case sdl.K_w:
		return imgui.KeyW
	case sdl.K_x:
		return imgui.KeyX
	case sdl.K_y:
		return imgui.KeyY
	case sdl.K_z:
		return imgui.KeyZ
	case sdl.K_F1:
		return imgui.KeyF1
	case sdl.K_F2:
		return imgui.KeyF2
	case sdl.K_F3:
		return imgui.KeyF3
	case sdl.K_F4:
		return imgui.KeyF4
	case sdl.K_F5:
		return imgui.KeyF5
	case sdl.K_F6:
		return imgui.KeyF6
	case sdl.K_F7:
		return imgui.KeyF7
	case sdl.K_F8:
		return imgui.KeyF8
	case sdl.K_F9:
		return imgui.KeyF9
	case sdl.K_F10:
		return imgui.KeyF10
	case sdl.K_F11:
		return imgui.KeyF11
	case sdl.K_F12:
		return imgui.KeyF12
	case sdl.K_F13:
		return imgui.KeyF13
	case sdl.K_F14:
		return imgui.KeyF14
	case sdl.K_F15:
		return imgui.KeyF15
	case sdl.K_F16:
		return imgui.KeyF16
	case sdl.K_F17:
		return imgui.KeyF17
	case sdl.K_F18:
		return imgui.KeyF18
	case sdl.K_F19:
		return imgui.KeyF19
	case sdl.K_F20:
		return imgui.KeyF20
	case sdl.K_F21:
		return imgui.KeyF21
	case sdl.K_F22:
		return imgui.KeyF22
	case sdl.K_F23:
		return imgui.KeyF23
	case sdl.K_F24:
		return imgui.KeyF24
	default:
		return imgui.KeyNone
	}
}

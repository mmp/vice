// pkg/platform/glfw.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This a slightly modified version of the GLFW/SDL2 infrastructure from
// imgui-go-examples, where the main addition is cursor handling
// (backported from imgui's backends/imgui_impl_glfw.cpp), and some
// additional handling of text input outside of the imgui path.

package platform

import (
	"fmt"
	gomath "math"
	"runtime"
	"strconv"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v2.1/gl"
	"github.com/go-gl/glfw/v3.3/glfw"
)

// glfwPlatform implements the Platform interface using GLFW.
type glfwPlatform struct {
	audioEngine

	imguiIO *imgui.IO

	window *glfw.Window
	config *Config

	time                   float64
	mouseJustPressed       [3]bool
	mouseCursors           [imgui.MouseCursorCOUNT]*glfw.Cursor
	currentCursor          *glfw.Cursor
	inputCharacters        string
	anyEvents              bool
	lastMouseX, lastMouseY float64
	multisample            bool
	windowTitle            string
	mouseCapture           math.Extent2D
	// These are the keys that are actively held down; for now just the
	// function keys, since all we currently need is F1 for beaconator.
	heldFKeys map[imgui.Key]interface{}

	mouseDeltaMode         bool
	mouseDeltaStartPos     [2]float32
	mouseDeltaWindowCenter [2]float32
	mouseDelta             [2]float32
}

type Config struct {
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int

	EnableMSAA bool

	StartInFullScreen bool
	FullScreenMonitor int
}

// New returns a new instance of a Platform implemented with a window
// of the specified size open at the specified position on the screen.
func New(config *Config, lg *log.Logger) (Platform, error) {
	lg.Info("Starting GLFW initialization")
	err := glfw.Init()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw: %w", err)
	}
	lg.Infof("GLFW: %s", glfw.GetVersionString())

	io := imgui.CurrentIO()
	io.SetBackendFlags(io.BackendFlags() | imgui.BackendFlagsHasMouseCursors)

	glfw.WindowHint(glfw.ContextVersionMajor, 2)
	glfw.WindowHint(glfw.ContextVersionMinor, 1)

	vm := glfw.GetPrimaryMonitor().GetVideoMode()
	if config.InitialWindowSize[0] == 0 || config.InitialWindowSize[1] == 0 {
		if runtime.GOOS == "windows" {
			config.InitialWindowSize[0] = vm.Width - 200
			config.InitialWindowSize[1] = vm.Height - 300
		} else {
			config.InitialWindowSize[0] = vm.Width - 150
			config.InitialWindowSize[1] = vm.Height - 150
		}
	}

	// If window position is out of bounds, create the window at (100, 100)
	if config.InitialWindowPosition[0] < 0 || config.InitialWindowPosition[1] < 0 ||
		config.InitialWindowPosition[0] > vm.Width || config.InitialWindowPosition[1] > vm.Height {
		config.InitialWindowPosition = [2]int{100, 100}
	}
	// Start with an invisible window so that we can position it first
	glfw.WindowHint(glfw.Visible, 0)
	// Disable GLFW_AUTO_ICONIFY to stop the window from automatically minimizing in fullscreen
	glfw.WindowHint(glfw.AutoIconify, 0)
	// Maybe enable multisampling
	if config.EnableMSAA {
		glfw.WindowHint(glfw.Samples, 4)
	}
	var window *glfw.Window
	monitors := glfw.GetMonitors()
	if config.FullScreenMonitor >= len(monitors) {
		// Monitor saved in config not found, fallback to default
		config.FullScreenMonitor = 0
	}
	if config.StartInFullScreen {
		vm := monitors[config.FullScreenMonitor].GetVideoMode()
		window, err = glfw.CreateWindow(vm.Width, vm.Height, "vice", monitors[config.FullScreenMonitor], nil)
	} else {
		window, err = glfw.CreateWindow(config.InitialWindowSize[0], config.InitialWindowSize[1], "vice", nil, nil)
	}
	if err != nil {
		glfw.Terminate()
		return nil, fmt.Errorf("failed to create window: %w", err)
	}
	window.SetPos(config.InitialWindowPosition[0], config.InitialWindowPosition[1])
	window.Show()
	window.MakeContextCurrent()

	platform := &glfwPlatform{
		config:      config,
		imguiIO:     io,
		window:      window,
		multisample: config.EnableMSAA,
		heldFKeys:   make(map[imgui.Key]interface{}),
	}
	platform.installCallbacks()
	platform.createMouseCursors()
	platform.EnableVSync(true)

	glfw.SetMonitorCallback(platform.MonitorCallback)

	lg.Info("Finished GLFW initialization")

	platform.audioEngine.Initialize(lg)

	return platform, nil
}

func (g *glfwPlatform) DPIScale() float32 {
	if runtime.GOOS == "windows" {
		sx, sy := g.window.GetContentScale()
		return float32(int((sx + sy) / 2))
	} else {
		return g.FramebufferSize()[0] / g.DisplaySize()[0]
	}
}

func (g *glfwPlatform) EnableVSync(sync bool) {
	if sync {
		glfw.SwapInterval(1)
	} else {
		glfw.SwapInterval(0)
	}
}

// Detecting whether the window is already in native (MacOS) fullscreen is a bit tricky, since GLFW doesn't have
// a function for this. To prevent unexpected behavior, it needs to only allow to either fullscreen natively or through SetWindowMonitor.
// The function assumes the window is in native fullscreen if it's maximized and the window size matches one of the monitor's size.
func (g *glfwPlatform) IsMacOSNativeFullScreen() bool {
	if runtime.GOOS == "darwin" && g.window.GetAttrib(glfw.Maximized) == glfw.True {
		monitors := glfw.GetMonitors()
		windowSize := g.WindowSize()

		for _, monitor := range monitors {
			vm := monitor.GetVideoMode()
			if windowSize[0] == vm.Width && windowSize[1] == vm.Height {
				return true
			}
		}
	}
	return false
}

func (g *glfwPlatform) GetAllMonitorNames() []string {
	var monitorNames []string
	monitors := glfw.GetMonitors()
	for index, monitor := range monitors {
		monitorNames = append(monitorNames, "("+strconv.Itoa(index)+") "+monitor.GetName())
	}
	return monitorNames
}

func (g *glfwPlatform) MonitorCallback(monitor *glfw.Monitor, event glfw.PeripheralEvent) {
	if event == glfw.Disconnected {
		g.config.FullScreenMonitor = 0
		g.config.StartInFullScreen = false
	}
}

func (g *glfwPlatform) Dispose() {
	g.window.Destroy()
	glfw.Terminate()
}

func (g *glfwPlatform) InputCharacters() string {
	return g.inputCharacters
}

func (g *glfwPlatform) ShouldStop() bool {
	return g.window.ShouldClose()
}

func (g *glfwPlatform) CancelShouldStop() {
	g.window.SetShouldClose(false)
}

func (g *glfwPlatform) ProcessEvents() bool {
	g.inputCharacters = ""
	g.anyEvents = false

	glfw.PollEvents()

	if g.anyEvents {
		return true
	}

	for i := 0; i < len(g.mouseJustPressed); i++ {
		if g.window.GetMouseButton(glfwButtonIDByIndex[imgui.MouseButton(i)]) == glfw.Press {
			return true
		}
	}

	x, y := g.window.GetCursorPos()
	if x != g.lastMouseX || y != g.lastMouseY {
		g.lastMouseX, g.lastMouseY = x, y
		return true
	}

	return false
}

func (g *glfwPlatform) DisplaySize() [2]float32 {
	w, h := g.window.GetSize()
	return [2]float32{float32(w), float32(h)}
}

func (g *glfwPlatform) WindowSize() [2]int {
	w, h := g.window.GetSize()
	return [2]int{w, h}
}

func (g *glfwPlatform) WindowPosition() [2]int {
	x, y := g.window.GetPos()
	return [2]int{x, y}
}

func (g *glfwPlatform) FramebufferSize() [2]float32 {
	w, h := g.window.GetFramebufferSize()
	return [2]float32{float32(w), float32(h)}
}

func (g *glfwPlatform) NewFrame() {
	if g.multisample {
		gl.Enable(gl.MULTISAMPLE)
	}

	// Setup display size (every frame to accommodate for window resizing)
	displaySize := g.DisplaySize()
	g.imguiIO.SetDisplaySize(imgui.Vec2{X: displaySize[0], Y: displaySize[1]})

	// Setup time step
	currentTime := glfw.GetTime()
	if g.time > 0 {
		g.imguiIO.SetDeltaTime(float32(currentTime - g.time))
	}
	g.time = currentTime

	// Setup inputs
	if g.window.GetAttrib(glfw.Focused) != 0 {
		pc := g.getCursorPos()
		if g.mouseCapture.Width() > 0 && g.mouseCapture.Height() > 0 && !g.mouseCapture.Inside(pc) {
			pc = g.mouseCapture.ClosestPointInBox(pc)
		}
		g.imguiIO.SetMousePos(imgui.Vec2{X: pc[0], Y: pc[1]})
	} else {
		g.imguiIO.SetMousePos(imgui.Vec2{X: -gomath.MaxFloat32, Y: -gomath.MaxFloat32})
	}

	for i := 0; i < len(g.mouseJustPressed); i++ {
		down := g.mouseJustPressed[i] ||
			(g.window.GetMouseButton(glfwButtonIDByIndex[imgui.MouseButton(i)]) == glfw.Press)
		g.imguiIO.SetMouseButtonDown(i, down)
		g.mouseJustPressed[i] = false
	}

	// Mouse cursor
	imgui_cursor := imgui.CurrentMouseCursor()

	if g.mouseDeltaMode || imgui_cursor == imgui.MouseCursorNone {
		// Hide OS mouse cursor if imgui is drawing it or if it wants no cursor
		g.window.SetInputMode(glfw.CursorMode, glfw.CursorHidden)
	} else {
		// Show OS mouse cursor
		cursor := g.mouseCursors[imgui_cursor]
		if cursor == nil {
			cursor = g.mouseCursors[imgui.MouseCursorArrow]
		}
		if cursor != g.currentCursor {
			g.currentCursor = cursor
			g.window.SetCursor(cursor)
		}

		g.window.SetInputMode(glfw.CursorMode, glfw.CursorNormal)
	}

	// If mouse capture is enabled, check the mouse position and clamp it
	// to the bounds if necessary.
	if g.mouseCapture.Width() > 0 && g.mouseCapture.Height() > 0 {
		pc := g.getCursorPos()
		if !g.mouseCapture.Inside(pc) {
			pc = g.mouseCapture.ClosestPointInBox(pc)
			g.window.SetCursorPos(float64(pc[0]), float64(pc[1]))
		}
	}
	if g.mouseDeltaMode {
		g.mouseDelta = math.Sub2f(g.getCursorPos(), g.mouseDeltaWindowCenter)
		g.window.SetCursorPos(float64(g.mouseDeltaWindowCenter[0]), float64(g.mouseDeltaWindowCenter[1]))
	}
}

func (g *glfwPlatform) getCursorPos() [2]float32 {
	x, y := g.window.GetCursorPos()
	return [2]float32{float32(int(x)), float32(int(y))}
}

func (g *glfwPlatform) PostRender() {
	g.window.SwapBuffers()
}

func (g *glfwPlatform) installCallbacks() {
	g.window.SetMouseButtonCallback(g.mouseButtonChange)
	g.window.SetScrollCallback(g.mouseScrollChange)
	g.window.SetKeyCallback(g.keyChange)
	g.window.SetCharCallback(g.charChange)
}

var glfwButtonIndexByID = map[glfw.MouseButton]imgui.MouseButton{
	glfw.MouseButton1: MouseButtonPrimary,
	glfw.MouseButton2: MouseButtonSecondary,
	glfw.MouseButton3: MouseButtonTertiary,
}

var glfwButtonIDByIndex = map[imgui.MouseButton]glfw.MouseButton{
	MouseButtonPrimary:   glfw.MouseButton1,
	MouseButtonSecondary: glfw.MouseButton2,
	MouseButtonTertiary:  glfw.MouseButton3,
}

func (g *glfwPlatform) mouseButtonChange(window *glfw.Window, rawButton glfw.MouseButton, action glfw.Action, mods glfw.ModifierKey) {
	buttonIndex, known := glfwButtonIndexByID[rawButton]

	if !known {
		return
	}

	g.anyEvents = true
	if action == glfw.Press {
		g.mouseJustPressed[buttonIndex] = true
	}
	g.updateKeyModifiers()
}

func (g *glfwPlatform) mouseScrollChange(window *glfw.Window, x, y float64) {
	g.anyEvents = true
	g.imguiIO.AddMouseWheelDelta(float32(x), float32(y))
}

func (g *glfwPlatform) keyChange(window *glfw.Window, keycode glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	g.anyEvents = true
	g.updateKeyModifiers()

	// TODO: this can probably be done more cleanly/consistently through imgui
	for i, k := range []glfw.Key{glfw.KeyF1, glfw.KeyF2, glfw.KeyF3, glfw.KeyF4, glfw.KeyF5, glfw.KeyF6, glfw.KeyF7, glfw.KeyF8,
		glfw.KeyF9, glfw.KeyF10, glfw.KeyF11, glfw.KeyF12, glfw.KeyF13, glfw.KeyF14, glfw.KeyF15, glfw.KeyF16} {
		if g.window.GetKey(k) == glfw.Press {
			g.heldFKeys[imgui.KeyF1+imgui.Key(i)] = nil
		}
		if g.window.GetKey(k) == glfw.Release {
			delete(g.heldFKeys, imgui.KeyF1+imgui.Key(i))
		}
	}

	if action != glfw.Press && action != glfw.Release {
		return
	}

	kc := translateUntranslatedKey(keycode, scancode)
	imguikey := glfwKeyToImguiKey(kc)
	g.imguiIO.AddKeyEvent(imguikey, action == glfw.Press)
}

func (g *glfwPlatform) updateKeyModifiers() {
	g.imguiIO.AddKeyEvent(imgui.ModShift, g.window.GetKey(glfw.KeyLeftShift) == glfw.Press || g.window.GetKey(glfw.KeyRightShift) == glfw.Press)
	g.imguiIO.AddKeyEvent(imgui.ModAlt, g.window.GetKey(glfw.KeyLeftAlt) == glfw.Press || g.window.GetKey(glfw.KeyRightAlt) == glfw.Press)
	if runtime.GOOS == "darwin" {
		// imgui "helpfully" swaps the Command and Control modifier keys on
		// OSX. So we need to undo that here so that control still comes
		// through as control.
		g.imguiIO.AddKeyEvent(imgui.ModSuper, g.window.GetKey(glfw.KeyLeftControl) == glfw.Press || g.window.GetKey(glfw.KeyRightControl) == glfw.Press)
		g.imguiIO.AddKeyEvent(imgui.ModCtrl, g.window.GetKey(glfw.KeyLeftSuper) == glfw.Press || g.window.GetKey(glfw.KeyRightSuper) == glfw.Press)
	} else {
		g.imguiIO.AddKeyEvent(imgui.ModCtrl, g.window.GetKey(glfw.KeyLeftControl) == glfw.Press || g.window.GetKey(glfw.KeyRightControl) == glfw.Press)
		g.imguiIO.AddKeyEvent(imgui.ModSuper, g.window.GetKey(glfw.KeyLeftSuper) == glfw.Press || g.window.GetKey(glfw.KeyRightSuper) == glfw.Press)
	}
}

func (g *glfwPlatform) charChange(window *glfw.Window, char rune) {
	g.anyEvents = true
	g.imguiIO.AddInputCharactersUTF8(string(char))
	g.inputCharacters = g.inputCharacters + string(char)
}

func (g *glfwPlatform) createMouseCursors() {
	g.mouseCursors[imgui.MouseCursorArrow] = glfw.CreateStandardCursor(glfw.ArrowCursor)
	g.mouseCursors[imgui.MouseCursorTextInput] = glfw.CreateStandardCursor(glfw.IBeamCursor)
	g.mouseCursors[imgui.MouseCursorResizeAll] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNS] = glfw.CreateStandardCursor(glfw.VResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeEW] = glfw.CreateStandardCursor(glfw.HResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeNESW] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNWSE] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorHand] = glfw.CreateStandardCursor(glfw.HandCursor)

}

func (g *glfwPlatform) SetWindowTitle(text string) {
	if text != g.windowTitle {
		g.window.SetTitle(text)
		g.windowTitle = text
	}
}

func (g *glfwPlatform) GetClipboard() imgui.ClipboardHandler {
	return glfwClipboard{window: g.window}
}

type glfwClipboard struct {
	window *glfw.Window
}

func (cb glfwClipboard) GetClipboard() string {
	return cb.window.GetClipboardString()
}

func (cb glfwClipboard) SetClipboard(text string) {
	cb.window.SetClipboardString(text)
}

func (g *glfwPlatform) StartCaptureMouse(e math.Extent2D) {
	g.StopMouseDeltaMode()
	g.mouseCapture = math.Extent2D{
		P0: [2]float32{math.Ceil(e.P0[0]), math.Ceil(e.P0[1])},
		P1: [2]float32{math.Floor(e.P1[0]), math.Floor(e.P1[1])}}
}

func (g *glfwPlatform) EndCaptureMouse() {
	g.mouseCapture = math.Extent2D{}
}

func (g *glfwPlatform) StartMouseDeltaMode() {
	g.EndCaptureMouse()

	g.mouseDeltaMode = true
	g.mouseDelta = [2]float32{}
	g.mouseDeltaStartPos = g.getCursorPos()

	// Put the mouse at the center of the window (where we'll reset it
	// after each frame) so that there's plenty of room for movement to get
	// deltas in all directions.
	wsz := g.WindowSize()
	wsz[0] /= 2
	wsz[1] /= 2
	g.window.SetCursorPos(float64(wsz[0]), float64(wsz[1]))
	g.mouseDeltaWindowCenter = g.getCursorPos()
}

func (g *glfwPlatform) StopMouseDeltaMode() {
	if g.mouseDeltaMode {
		g.mouseDeltaMode = false
		g.SetMousePosition(g.mouseDeltaStartPos)
	}
}

func (g *glfwPlatform) SetMousePosition(p [2]float32) {
	g.window.SetCursorPos(float64(p[0]), float64(p[1]))
}

// Translation of ImGui_ImplGlfw_TranslateUntranslatedKey from imgui/backends/imgui_impl_glfw.cpp
func translateUntranslatedKey(key glfw.Key, scancode int) glfw.Key {
	if key >= glfw.KeyKP0 && key <= glfw.KeyKPEqual {
		return key
	}
	name := glfw.GetKeyName(key, scancode)
	// glfw.GetError(nil)
	if len(name) == 1 {
		if name[0] >= '0' && name[0] <= '9' {
			return glfw.Key0 + glfw.Key(name[0]-'0')
		} else if name[0] >= 'A' && name[0] <= 'Z' {
			return glfw.KeyA + glfw.Key(name[0]-'A')
		} else if name[0] >= 'a' && name[0] <= 'z' {
			return glfw.KeyA + glfw.Key(name[0]-'a')
		} else {
			chars := map[byte]glfw.Key{
				'`':  glfw.KeyGraveAccent,
				'-':  glfw.KeyMinus,
				'=':  glfw.KeyEqual,
				'[':  glfw.KeyLeftBracket,
				']':  glfw.KeyRightBracket,
				'\\': glfw.KeyBackslash,
				',':  glfw.KeyComma,
				';':  glfw.KeySemicolon,
				'\'': glfw.KeyApostrophe,
				'.':  glfw.KeyPeriod,
				'/':  glfw.KeySlash,
			}
			if k, ok := chars[name[0]]; ok {
				return k
			}
		}
	}
	return key
}

func glfwKeyToImguiKey(keycode glfw.Key) imgui.Key {
	switch keycode {
	case glfw.KeyTab:
		return imgui.KeyTab
	case glfw.KeyLeft:
		return imgui.KeyLeftArrow
	case glfw.KeyRight:
		return imgui.KeyRightArrow
	case glfw.KeyUp:
		return imgui.KeyUpArrow
	case glfw.KeyDown:
		return imgui.KeyDownArrow
	case glfw.KeyPageUp:
		return imgui.KeyPageUp
	case glfw.KeyPageDown:
		return imgui.KeyPageDown
	case glfw.KeyHome:
		return imgui.KeyHome
	case glfw.KeyEnd:
		return imgui.KeyEnd
	case glfw.KeyInsert:
		return imgui.KeyInsert
	case glfw.KeyDelete:
		return imgui.KeyDelete
	case glfw.KeyBackspace:
		return imgui.KeyBackspace
	case glfw.KeySpace:
		return imgui.KeySpace
	case glfw.KeyEnter:
		return imgui.KeyEnter
	case glfw.KeyEscape:
		return imgui.KeyEscape
	case glfw.KeyApostrophe:
		return imgui.KeyApostrophe
	case glfw.KeyComma:
		return imgui.KeyComma
	case glfw.KeyMinus:
		return imgui.KeyMinus
	case glfw.KeyPeriod:
		return imgui.KeyPeriod
	case glfw.KeySlash:
		return imgui.KeySlash
	case glfw.KeySemicolon:
		return imgui.KeySemicolon
	case glfw.KeyEqual:
		return imgui.KeyEqual
	case glfw.KeyLeftBracket:
		return imgui.KeyLeftBracket
	case glfw.KeyBackslash:
		return imgui.KeyBackslash
	case glfw.KeyWorld1:
		return imgui.KeyOem102
	case glfw.KeyWorld2:
		return imgui.KeyOem102
	case glfw.KeyRightBracket:
		return imgui.KeyRightBracket
	case glfw.KeyGraveAccent:
		return imgui.KeyGraveAccent
	case glfw.KeyCapsLock:
		return imgui.KeyCapsLock
	case glfw.KeyScrollLock:
		return imgui.KeyScrollLock
	case glfw.KeyNumLock:
		return imgui.KeyNumLock
	case glfw.KeyPrintScreen:
		return imgui.KeyPrintScreen
	case glfw.KeyPause:
		return imgui.KeyPause
	case glfw.KeyKP0:
		return imgui.KeyKeypad0
	case glfw.KeyKP1:
		return imgui.KeyKeypad1
	case glfw.KeyKP2:
		return imgui.KeyKeypad2
	case glfw.KeyKP3:
		return imgui.KeyKeypad3
	case glfw.KeyKP4:
		return imgui.KeyKeypad4
	case glfw.KeyKP5:
		return imgui.KeyKeypad5
	case glfw.KeyKP6:
		return imgui.KeyKeypad6
	case glfw.KeyKP7:
		return imgui.KeyKeypad7
	case glfw.KeyKP8:
		return imgui.KeyKeypad8
	case glfw.KeyKP9:
		return imgui.KeyKeypad9
	case glfw.KeyKPDecimal:
		return imgui.KeyKeypadDecimal
	case glfw.KeyKPDivide:
		return imgui.KeyKeypadDivide
	case glfw.KeyKPMultiply:
		return imgui.KeyKeypadMultiply
	case glfw.KeyKPSubtract:
		return imgui.KeyKeypadSubtract
	case glfw.KeyKPAdd:
		return imgui.KeyKeypadAdd
	case glfw.KeyKPEnter:
		return imgui.KeyKeypadEnter
	case glfw.KeyKPEqual:
		return imgui.KeyKeypadEqual
	case glfw.KeyLeftShift:
		return imgui.KeyLeftShift
	case glfw.KeyLeftControl:
		return imgui.KeyLeftCtrl
	case glfw.KeyLeftAlt:
		return imgui.KeyLeftAlt
	case glfw.KeyLeftSuper:
		return imgui.KeyLeftSuper
	case glfw.KeyRightShift:
		return imgui.KeyRightShift
	case glfw.KeyRightControl:
		return imgui.KeyRightCtrl
	case glfw.KeyRightAlt:
		return imgui.KeyRightAlt
	case glfw.KeyRightSuper:
		return imgui.KeyRightSuper
	case glfw.KeyMenu:
		return imgui.KeyMenu
	case glfw.Key0:
		return imgui.Key0
	case glfw.Key1:
		return imgui.Key1
	case glfw.Key2:
		return imgui.Key2
	case glfw.Key3:
		return imgui.Key3
	case glfw.Key4:
		return imgui.Key4
	case glfw.Key5:
		return imgui.Key5
	case glfw.Key6:
		return imgui.Key6
	case glfw.Key7:
		return imgui.Key7
	case glfw.Key8:
		return imgui.Key8
	case glfw.Key9:
		return imgui.Key9
	case glfw.KeyA:
		return imgui.KeyA
	case glfw.KeyB:
		return imgui.KeyB
	case glfw.KeyC:
		return imgui.KeyC
	case glfw.KeyD:
		return imgui.KeyD
	case glfw.KeyE:
		return imgui.KeyE
	case glfw.KeyF:
		return imgui.KeyF
	case glfw.KeyG:
		return imgui.KeyG
	case glfw.KeyH:
		return imgui.KeyH
	case glfw.KeyI:
		return imgui.KeyI
	case glfw.KeyJ:
		return imgui.KeyJ
	case glfw.KeyK:
		return imgui.KeyK
	case glfw.KeyL:
		return imgui.KeyL
	case glfw.KeyM:
		return imgui.KeyM
	case glfw.KeyN:
		return imgui.KeyN
	case glfw.KeyO:
		return imgui.KeyO
	case glfw.KeyP:
		return imgui.KeyP
	case glfw.KeyQ:
		return imgui.KeyQ
	case glfw.KeyR:
		return imgui.KeyR
	case glfw.KeyS:
		return imgui.KeyS
	case glfw.KeyT:
		return imgui.KeyT
	case glfw.KeyU:
		return imgui.KeyU
	case glfw.KeyV:
		return imgui.KeyV
	case glfw.KeyW:
		return imgui.KeyW
	case glfw.KeyX:
		return imgui.KeyX
	case glfw.KeyY:
		return imgui.KeyY
	case glfw.KeyZ:
		return imgui.KeyZ
	case glfw.KeyF1:
		return imgui.KeyF1
	case glfw.KeyF2:
		return imgui.KeyF2
	case glfw.KeyF3:
		return imgui.KeyF3
	case glfw.KeyF4:
		return imgui.KeyF4
	case glfw.KeyF5:
		return imgui.KeyF5
	case glfw.KeyF6:
		return imgui.KeyF6
	case glfw.KeyF7:
		return imgui.KeyF7
	case glfw.KeyF8:
		return imgui.KeyF8
	case glfw.KeyF9:
		return imgui.KeyF9
	case glfw.KeyF10:
		return imgui.KeyF10
	case glfw.KeyF11:
		return imgui.KeyF11
	case glfw.KeyF12:
		return imgui.KeyF12
	case glfw.KeyF13:
		return imgui.KeyF13
	case glfw.KeyF14:
		return imgui.KeyF14
	case glfw.KeyF15:
		return imgui.KeyF15
	case glfw.KeyF16:
		return imgui.KeyF16
	case glfw.KeyF17:
		return imgui.KeyF17
	case glfw.KeyF18:
		return imgui.KeyF18
	case glfw.KeyF19:
		return imgui.KeyF19
	case glfw.KeyF20:
		return imgui.KeyF20
	case glfw.KeyF21:
		return imgui.KeyF21
	case glfw.KeyF22:
		return imgui.KeyF22
	case glfw.KeyF23:
		return imgui.KeyF23
	case glfw.KeyF24:
		return imgui.KeyF24
	default:
		return imgui.KeyNone
	}
}

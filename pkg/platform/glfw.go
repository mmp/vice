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

	"github.com/go-gl/gl/v2.1/gl"
	"github.com/go-gl/glfw/v3.3/glfw"
	"github.com/mmp/imgui-go/v4"
)

// glfwPlatform implements the Platform interface using GLFW.
type glfwPlatform struct {
	audioEngine

	imguiIO imgui.IO

	window *glfw.Window
	config *Config

	time                   float64
	mouseJustPressed       [3]bool
	mouseCursors           [imgui.MouseCursorCount]*glfw.Cursor
	currentCursor          *glfw.Cursor
	inputCharacters        string
	anyEvents              bool
	lastMouseX, lastMouseY float64
	multisample            bool
	windowTitle            string
	mouseCapture           math.Extent2D
	// These are the keys that are actively held down; for now just the
	// function keys, since all we currently need is F1 for beaconator.
	heldFKeys map[Key]interface{}

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
	io.SetBackendFlags(io.GetBackendFlags() | imgui.BackendFlagsHasMouseCursors)

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
		heldFKeys:   make(map[Key]interface{}),
	}
	platform.setKeyMapping()
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
		if g.window.GetMouseButton(glfwButtonIDByIndex[i]) == glfw.Press {
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
		g.imguiIO.SetMousePosition(imgui.Vec2{X: pc[0], Y: pc[1]})
	} else {
		g.imguiIO.SetMousePosition(imgui.Vec2{X: -gomath.MaxFloat32, Y: -gomath.MaxFloat32})
	}

	for i := 0; i < len(g.mouseJustPressed); i++ {
		down := g.mouseJustPressed[i] || (g.window.GetMouseButton(glfwButtonIDByIndex[i]) == glfw.Press)
		g.imguiIO.SetMouseButtonDown(i, down)
		g.mouseJustPressed[i] = false
	}

	// Mouse cursor
	imgui_cursor := imgui.MouseCursor()

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

func (g *glfwPlatform) setKeyMapping() {
	// Keyboard mapping. ImGui will use those indices to peek into the io.KeysDown[] array.
	g.imguiIO.KeyMap(imgui.KeyTab, int(glfw.KeyTab))
	g.imguiIO.KeyMap(imgui.KeyLeftArrow, int(glfw.KeyLeft))
	g.imguiIO.KeyMap(imgui.KeyRightArrow, int(glfw.KeyRight))
	g.imguiIO.KeyMap(imgui.KeyUpArrow, int(glfw.KeyUp))
	g.imguiIO.KeyMap(imgui.KeyDownArrow, int(glfw.KeyDown))
	g.imguiIO.KeyMap(imgui.KeyPageUp, int(glfw.KeyPageUp))
	g.imguiIO.KeyMap(imgui.KeyPageDown, int(glfw.KeyPageDown))
	g.imguiIO.KeyMap(imgui.KeyHome, int(glfw.KeyHome))
	g.imguiIO.KeyMap(imgui.KeyEnd, int(glfw.KeyEnd))
	g.imguiIO.KeyMap(imgui.KeyInsert, int(glfw.KeyInsert))
	g.imguiIO.KeyMap(imgui.KeyDelete, int(glfw.KeyDelete))
	g.imguiIO.KeyMap(imgui.KeyBackspace, int(glfw.KeyBackspace))
	g.imguiIO.KeyMap(imgui.KeySpace, int(glfw.KeySpace))
	g.imguiIO.KeyMap(imgui.KeyEnter, int(glfw.KeyEnter))
	g.imguiIO.KeyMap(imgui.KeyEscape, int(glfw.KeyEscape))
	g.imguiIO.KeyMap(imgui.KeyA, int(glfw.KeyA))
	g.imguiIO.KeyMap(imgui.KeyC, int(glfw.KeyC))
	g.imguiIO.KeyMap(imgui.KeyV, int(glfw.KeyV))
	g.imguiIO.KeyMap(imgui.KeyX, int(glfw.KeyX))
	g.imguiIO.KeyMap(imgui.KeyY, int(glfw.KeyY))
	g.imguiIO.KeyMap(imgui.KeyZ, int(glfw.KeyZ))
}

func (g *glfwPlatform) installCallbacks() {
	g.window.SetMouseButtonCallback(g.mouseButtonChange)
	g.window.SetScrollCallback(g.mouseScrollChange)
	g.window.SetKeyCallback(g.keyChange)
	g.window.SetCharCallback(g.charChange)
}

var glfwButtonIndexByID = map[glfw.MouseButton]int{
	glfw.MouseButton1: MouseButtonPrimary,
	glfw.MouseButton2: MouseButtonSecondary,
	glfw.MouseButton3: MouseButtonTertiary,
}

var glfwButtonIDByIndex = map[int]glfw.MouseButton{
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
}

func (g *glfwPlatform) mouseScrollChange(window *glfw.Window, x, y float64) {
	g.anyEvents = true
	g.imguiIO.AddMouseWheelDelta(float32(x), float32(y))
}

func (g *glfwPlatform) keyChange(window *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	g.anyEvents = true
	if action == glfw.Press {
		g.imguiIO.KeyPress(int(key))
	}
	if action == glfw.Release {
		g.imguiIO.KeyRelease(int(key))
	}

	for i, k := range []glfw.Key{glfw.KeyF1, glfw.KeyF2, glfw.KeyF3, glfw.KeyF4, glfw.KeyF5, glfw.KeyF6, glfw.KeyF7, glfw.KeyF8,
		glfw.KeyF9, glfw.KeyF10, glfw.KeyF11, glfw.KeyF12, glfw.KeyF13, glfw.KeyF14, glfw.KeyF15, glfw.KeyF16} {
		if g.window.GetKey(k) == glfw.Press {
			g.heldFKeys[Key(KeyF1+i)] = nil
		}
		if g.window.GetKey(k) == glfw.Release {
			delete(g.heldFKeys, Key(KeyF1+i))
		}
	}

	// Modifiers are not reliable across systems
	g.imguiIO.KeyCtrl(int(glfw.KeyLeftControl), int(glfw.KeyRightControl))
	g.imguiIO.KeyShift(int(glfw.KeyLeftShift), int(glfw.KeyRightShift))
	g.imguiIO.KeyAlt(int(glfw.KeyLeftAlt), int(glfw.KeyRightAlt))
	g.imguiIO.KeySuper(int(glfw.KeyLeftSuper), int(glfw.KeyRightSuper))
}

func (g *glfwPlatform) charChange(window *glfw.Window, char rune) {
	g.anyEvents = true
	g.imguiIO.AddInputCharacters(string(char))
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

func (g *glfwPlatform) GetClipboard() imgui.Clipboard {
	return glfwClipboard{window: g.window}
}

type glfwClipboard struct {
	window *glfw.Window
}

func (cb glfwClipboard) Text() (string, error) {
	return cb.window.GetClipboardString(), nil
}

func (cb glfwClipboard) SetText(text string) {
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

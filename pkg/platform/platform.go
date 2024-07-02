// platform.go
//
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
	GetClipboard() imgui.Clipboard
	// Enables a mode where the mouse is constrained to be within the
	// specified pixel extent, specified in window coordinates.
	StartCaptureMouse(e math.Extent2D)
	// Disable mouse capture.
	EndCaptureMouse()
	// Scaling factor to account for Retina-style displays
	DPIScale() float32

	GetMouse() *MouseState
	GetKeyboard() *KeyboardState
}

///////////////////////////////////////////////////////////////////////////

// GLFWPlatform implements the Platform interface using GLFW.
type GLFWPlatform struct {
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
}

type Config struct {
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int

	EnableMSAA bool

	StartInFullScreen bool
	FullScreenMonitor int
}

// NewGLFWPlatform returns a new instance of a GLFWPlatform with a window
// of the specified size open at the specified position on the screen.
func NewGLFW(io imgui.IO, config *Config, lg *log.Logger) (Platform, error) {
	lg.Info("Starting GLFW initialization")
	err := glfw.Init()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw: %w", err)
	}
	lg.Infof("GLFW: %s", glfw.GetVersionString())

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

	platform := &GLFWPlatform{
		config:      config,
		imguiIO:     io,
		window:      window,
		multisample: config.EnableMSAA,
	}
	platform.setKeyMapping()
	platform.installCallbacks()
	platform.createMouseCursors()
	platform.EnableVSync(true)

	glfw.SetMonitorCallback(platform.MonitorCallback)

	lg.Info("Finished GLFW initialization")
	return platform, nil
}

func (g *GLFWPlatform) DPIScale() float32 {
	if runtime.GOOS == "windows" {
		sx, sy := g.window.GetContentScale()
		return float32(int((sx + sy) / 2))
	} else {
		return g.FramebufferSize()[0] / g.DisplaySize()[0]
	}
}

func (g *GLFWPlatform) EnableVSync(sync bool) {
	if sync {
		glfw.SwapInterval(1)
	} else {
		glfw.SwapInterval(0)
	}
}

// Detecting whether the window is already in native (MacOS) fullscreen is a bit tricky, since GLFW doesn't have
// a function for this. To prevent unexpected behavior, it needs to only allow to either fullscreen natively or through SetWindowMonitor.
// The function assumes the window is in native fullscreen if it's maximized and the window size matches one of the monitor's size.
func (g *GLFWPlatform) IsMacOSNativeFullScreen() bool {
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

func (g *GLFWPlatform) GetAllMonitorNames() []string {
	var monitorNames []string
	monitors := glfw.GetMonitors()
	for index, monitor := range monitors {
		monitorNames = append(monitorNames, "("+strconv.Itoa(index)+") "+monitor.GetName())
	}
	return monitorNames
}

func (g *GLFWPlatform) MonitorCallback(monitor *glfw.Monitor, event glfw.PeripheralEvent) {
	if event == glfw.Disconnected {
		g.config.FullScreenMonitor = 0
		g.config.StartInFullScreen = false
	}
}

func (g *GLFWPlatform) Dispose() {
	g.window.Destroy()
	glfw.Terminate()
}

func (g *GLFWPlatform) InputCharacters() string {
	return g.inputCharacters
}

func (g *GLFWPlatform) ShouldStop() bool {
	return g.window.ShouldClose()
}

func (g *GLFWPlatform) CancelShouldStop() {
	g.window.SetShouldClose(false)
}

func (g *GLFWPlatform) ProcessEvents() bool {
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

func (g *GLFWPlatform) DisplaySize() [2]float32 {
	w, h := g.window.GetSize()
	return [2]float32{float32(w), float32(h)}
}

func (g *GLFWPlatform) WindowSize() [2]int {
	w, h := g.window.GetSize()
	return [2]int{w, h}
}

func (g *GLFWPlatform) WindowPosition() [2]int {
	x, y := g.window.GetPos()
	return [2]int{x, y}
}

func (g *GLFWPlatform) FramebufferSize() [2]float32 {
	w, h := g.window.GetFramebufferSize()
	return [2]float32{float32(w), float32(h)}
}

func (g *GLFWPlatform) NewFrame() {
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
		x, y := g.window.GetCursorPos()
		xy32 := [2]float32{float32(x), float32(y)}
		if g.mouseCapture.Width() > 0 && g.mouseCapture.Height() > 0 && !g.mouseCapture.Inside(xy32) {
			xy32 = g.mouseCapture.ClosestPointInBox(xy32)
		}
		g.imguiIO.SetMousePosition(imgui.Vec2{X: xy32[0], Y: xy32[1]})
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

	if imgui_cursor == imgui.MouseCursorNone {
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
		x, y := g.window.GetCursorPos()
		xy32 := [2]float32{float32(x), float32(y)}
		if !g.mouseCapture.Inside(xy32) {
			xy32 = g.mouseCapture.ClosestPointInBox(xy32)
			g.window.SetCursorPos(float64(xy32[0]), float64(xy32[1]))
		}
	}
}

func (g *GLFWPlatform) PostRender() {
	g.window.SwapBuffers()
}

func (g *GLFWPlatform) setKeyMapping() {
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

func (g *GLFWPlatform) installCallbacks() {
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

func (g *GLFWPlatform) mouseButtonChange(window *glfw.Window, rawButton glfw.MouseButton, action glfw.Action, mods glfw.ModifierKey) {
	buttonIndex, known := glfwButtonIndexByID[rawButton]

	if !known {
		return
	}

	g.anyEvents = true
	if action == glfw.Press {
		g.mouseJustPressed[buttonIndex] = true
	}
}

func (g *GLFWPlatform) mouseScrollChange(window *glfw.Window, x, y float64) {
	g.anyEvents = true
	g.imguiIO.AddMouseWheelDelta(float32(x), float32(y))
}

func (g *GLFWPlatform) keyChange(window *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	g.anyEvents = true
	if action == glfw.Press {
		g.imguiIO.KeyPress(int(key))
	}
	if action == glfw.Release {
		g.imguiIO.KeyRelease(int(key))
	}

	// Modifiers are not reliable across systems
	g.imguiIO.KeyCtrl(int(glfw.KeyLeftControl), int(glfw.KeyRightControl))
	g.imguiIO.KeyShift(int(glfw.KeyLeftShift), int(glfw.KeyRightShift))
	g.imguiIO.KeyAlt(int(glfw.KeyLeftAlt), int(glfw.KeyRightAlt))
	g.imguiIO.KeySuper(int(glfw.KeyLeftSuper), int(glfw.KeyRightSuper))
}

func (g *GLFWPlatform) charChange(window *glfw.Window, char rune) {
	g.anyEvents = true
	g.imguiIO.AddInputCharacters(string(char))
	g.inputCharacters = g.inputCharacters + string(char)
}

func (g *GLFWPlatform) createMouseCursors() {
	g.mouseCursors[imgui.MouseCursorArrow] = glfw.CreateStandardCursor(glfw.ArrowCursor)
	g.mouseCursors[imgui.MouseCursorTextInput] = glfw.CreateStandardCursor(glfw.IBeamCursor)
	g.mouseCursors[imgui.MouseCursorResizeAll] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNS] = glfw.CreateStandardCursor(glfw.VResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeEW] = glfw.CreateStandardCursor(glfw.HResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeNESW] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNWSE] = glfw.CreateStandardCursor(glfw.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorHand] = glfw.CreateStandardCursor(glfw.HandCursor)

}

func (g *GLFWPlatform) SetWindowTitle(text string) {
	if text != g.windowTitle {
		g.window.SetTitle(text)
		g.windowTitle = text
	}
}

func (g *GLFWPlatform) GetClipboard() imgui.Clipboard {
	return GLFWClipboard{window: g.window}
}

type GLFWClipboard struct {
	window *glfw.Window
}

func (cb GLFWClipboard) Text() (string, error) {
	return cb.window.GetClipboardString(), nil
}

func (cb GLFWClipboard) SetText(text string) {
	cb.window.SetClipboardString(text)
}

func (g *GLFWPlatform) StartCaptureMouse(e math.Extent2D) {
	g.mouseCapture = math.Extent2D{
		P0: [2]float32{math.Ceil(e.P0[0]), math.Ceil(e.P0[1])},
		P1: [2]float32{math.Floor(e.P1[0]), math.Floor(e.P1[1])}}
}

func (g *GLFWPlatform) EndCaptureMouse() {
	g.mouseCapture = math.Extent2D{}
}

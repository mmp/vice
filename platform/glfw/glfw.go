// platform/glfw/glfw.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This a slightly modified version of the GLFW infrastructure from
// imgui-go-examples, where the main addition is cursor handling
// (backported from imgui's backends/imgui_impl_glfw.cpp), and some
// additional handling of text input outside of the imgui path.

// Package glfw implements platform.Window using GLFW.
package glfw

import (
	"fmt"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	implglfw "github.com/AllenDang/cimgui-go/impl/glfw"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
	"github.com/go-gl/gl/v2.1/gl"
	glfw3 "github.com/go-gl/glfw/v3.4/glfw"
)

// glfwPlatform implements platform.Window using GLFW.
type glfwPlatform struct {
	imguiIO *imgui.IO

	window *glfw3.Window
	config *platform.Config

	mouseJustPressed       [3]bool
	mouseCursors           [imgui.MouseCursorCOUNT]*glfw3.Cursor
	currentCursor          *glfw3.Cursor
	cursorOverride         *glfw3.Cursor
	inputCharacters        string
	anyEvents              bool
	lastMouseX, lastMouseY float64
	multisample            bool
	windowTitle            string
	mouseCapture           math.Extent2D
	// These are the keys that are actively held down; for now just the
	// function keys, since all we currently need is F1 for beaconator.
	heldFKeys map[imgui.Key]any

	mouseDeltaMode         bool
	mouseDeltaStartPos     [2]float32
	mouseDeltaWindowCenter [2]float32
	mouseDelta             [2]float32

	appFocused bool
}

// New opens the application window at the size and position given by config
// and returns the platform.Window that manages it.
func New(config *platform.Config, lg *log.Logger) (platform.Window, error) {
	lg.Info("Starting GLFW initialization")
	err := glfw3.Init()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw: %w", err)
	}
	lg.Infof("GLFW: %s", glfw3.GetVersionString())

	io := imgui.CurrentIO()
	io.SetBackendFlags(io.BackendFlags() | imgui.BackendFlagsHasMouseCursors)

	glfw3.WindowHint(glfw3.ContextVersionMajor, 2)
	glfw3.WindowHint(glfw3.ContextVersionMinor, 1)

	vm := glfw3.GetPrimaryMonitor().GetVideoMode()
	if config.InitialWindowSize[0] == 0 || config.InitialWindowSize[1] == 0 {
		if runtime.GOOS == "windows" {
			config.InitialWindowSize[0] = vm.Width - 200
			config.InitialWindowSize[1] = vm.Height - 300
		} else {
			config.InitialWindowSize[0] = vm.Width - 150
			config.InitialWindowSize[1] = vm.Height - 150
		}
	}
	if config.MainWindowSquare {
		config.InitialWindowSize = squareWindowSize(config.InitialWindowSize)
	}

	// If window position is out of bounds, create the window at (100, 100)
	if config.InitialWindowPosition[0] < 0 || config.InitialWindowPosition[1] < 0 ||
		config.InitialWindowPosition[0] > vm.Width || config.InitialWindowPosition[1] > vm.Height {
		config.InitialWindowPosition = [2]int{100, 100}
	}
	// Start with an invisible window so that we can position it first
	glfw3.WindowHint(glfw3.Visible, 0)
	// Disable GLFW_AUTO_ICONIFY to stop the window from automatically minimizing in fullscreen
	glfw3.WindowHint(glfw3.AutoIconify, 0)
	// Maybe enable multisampling
	if config.EnableMSAA {
		glfw3.WindowHint(glfw3.Samples, 4)
	}
	var window *glfw3.Window
	monitors := glfw3.GetMonitors()
	if config.FullScreenMonitor >= len(monitors) {
		// Monitor saved in config not found, fallback to default
		config.FullScreenMonitor = 0
	}
	if config.StartInFullScreen {
		vm := monitors[config.FullScreenMonitor].GetVideoMode()
		window, err = glfw3.CreateWindow(vm.Width, vm.Height, "vice", monitors[config.FullScreenMonitor], nil)
	} else {
		window, err = glfw3.CreateWindow(config.InitialWindowSize[0], config.InitialWindowSize[1], "vice", nil, nil)
	}
	if err != nil {
		glfw3.Terminate()
		return nil, fmt.Errorf("failed to create window: %w", err)
	}
	if config.MainWindowSquare {
		window.SetAspectRatio(1, 1)
	}
	window.SetPos(config.InitialWindowPosition[0], config.InitialWindowPosition[1])
	window.Show()
	window.MakeContextCurrent()

	g := &glfwPlatform{
		config:      config,
		imguiIO:     io,
		window:      window,
		multisample: config.EnableMSAA,
		heldFKeys:   make(map[imgui.Key]any),
		appFocused:  true,
	}
	g.installCallbacks()
	g.createMouseCursors()
	g.EnableVSync(true)

	glfw3.SetMonitorCallback(g.MonitorCallback)

	lg.Info("Finished GLFW initialization")

	return g, nil
}

func squareWindowSize(size [2]int) [2]int {
	if size[0] <= 0 || size[1] <= 0 {
		return size
	}
	s := min(size[0], size[1])
	return [2]int{s, s}
}

func (g *glfwPlatform) SetMainWindowSquare(square bool) {
	g.config.MainWindowSquare = square
	if square {
		g.window.SetAspectRatio(1, 1)
		if !g.IsFullScreen() {
			size := squareWindowSize(g.WindowSize())
			g.window.SetSize(size[0], size[1])
		}
	} else {
		g.window.SetAspectRatio(glfw3.DontCare, glfw3.DontCare)
	}
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
		glfw3.SwapInterval(1)
	} else {
		glfw3.SwapInterval(0)
	}
}
func (g *glfwPlatform) GetAllMonitorNames() []string {
	var monitorNames []string
	monitors := glfw3.GetMonitors()
	for index, monitor := range monitors {
		monitorNames = append(monitorNames, "("+strconv.Itoa(index)+") "+monitor.GetName())
	}
	return monitorNames
}

func (g *glfwPlatform) MonitorCallback(monitor *glfw3.Monitor, event glfw3.PeripheralEvent) {
	if event == glfw3.Disconnected {
		g.config.FullScreenMonitor = 0
		g.config.StartInFullScreen = false
	}
}

func (g *glfwPlatform) Dispose() {
	// Shut down viewport backends before destroying the window.
	imgui.DestroyPlatformWindows()
	implogl3.Shutdown()
	implglfw.Shutdown()

	g.window.Destroy()
	glfw3.Terminate()
}

func (g *glfwPlatform) GetMouse() *platform.MouseState {
	return platform.NewMouseState(util.Select(g.mouseDeltaMode, g.mouseDelta, [2]float32{}))
}

func (g *glfwPlatform) GetKeyboard() *platform.KeyboardState {
	return platform.NewKeyboardState(g.InputCharacters(), g.heldFKeys)
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

	glfw3.PollEvents()

	if g.anyEvents {
		return true
	}

	for i := range len(g.mouseJustPressed) {
		if g.window.GetMouseButton(glfwButtonIDByIndex[imgui.MouseButton(i)]) == glfw3.Press {
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
	// Let the imgui backends update their internal state for viewport management.
	implogl3.NewFrame()
	implglfw.NewFrame()
	g.ensureViewportMonitors()
	clampMonitorWorkBounds()

	// Re-apply the macOS Ctrl↔Super swap. The imgui GLFW backend's
	// callbacks call ImGui_ImplGlfw_UpdateKeyModifiers which maps
	// physical keys directly (Control→ModCtrl, Super→ModSuper),
	// overwriting the swap we set in our own callbacks. We re-apply
	// it here so the correct mapping is in effect for the rest of
	// the frame.
	g.updateKeyModifiers()

	if g.multisample {
		gl.Enable(gl.MULTISAMPLE)
	}

	// implglfw.NewFrame() sets io.DisplaySize (window size in screen coords)
	// and io.DisplayFramebufferScale (framebuffer/window ratio) correctly.
	// We don't override them; our renderer reads display size and scale
	// from the draw data.

	// Clear stale mouseJustPressed flags. The imgui GLFW backend now
	// handles mouse button and position tracking for all windows (main
	// and secondary viewports) via its installed callbacks.
	for i := range len(g.mouseJustPressed) {
		g.mouseJustPressed[i] = false
	}

	// Mouse cursor
	imgui_cursor := imgui.CurrentMouseCursor()

	if g.cursorOverride != nil {
		// A pane set a specific OS cursor (e.g., ERAM); show it
		// regardless of the imgui cursor state.
		g.currentCursor = g.cursorOverride
		g.window.SetCursor(g.cursorOverride)
		g.window.SetInputMode(glfw3.CursorMode, glfw3.CursorNormal)
	} else if g.mouseDeltaMode || imgui_cursor == imgui.MouseCursorNone {
		// Hide OS mouse cursor (the pane draws its own)
		g.window.SetInputMode(glfw3.CursorMode, glfw3.CursorHidden)
	} else {
		// Show standard OS mouse cursor
		cursor := g.mouseCursors[imgui_cursor]
		if cursor == nil {
			cursor = g.mouseCursors[imgui.MouseCursorArrow]
		}
		if cursor != g.currentCursor {
			g.currentCursor = cursor
			g.window.SetCursor(cursor)
		}
		g.window.SetInputMode(glfw3.CursorMode, glfw3.CursorNormal)
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

func (g *glfwPlatform) ensureViewportMonitors() {
	io := imgui.CurrentIO()
	if io.ConfigFlags()&imgui.ConfigFlagsViewportsEnable == 0 {
		return
	}
	if imgui.CurrentPlatformIO().Monitors().Size > 0 {
		return
	}

	backendFlags := io.BackendFlags()
	backendFlags &^= imgui.BackendFlagsPlatformHasViewports
	backendFlags &^= imgui.BackendFlagsHasMouseHoveredViewport
	io.SetBackendFlags(backendFlags)
	io.SetConfigFlags(io.ConfigFlags() &^ imgui.ConfigFlagsViewportsEnable)
}

// clampMonitorWorkBounds fixes a bug where glfwGetMonitorWorkarea returns
// work bounds that extend outside the monitor's main bounds on Windows
// with certain DPI scaling configurations. imgui asserts that WorkRect is
// contained in MainRect during NewFrame; without this clamp the assertion
// crashes the application. See https://github.com/ocornut/imgui/issues/7219
//
// The implementation accesses the C ImGuiPlatformMonitor struct fields
// directly via unsafe.Pointer rather than using the cimgui-go Go wrapper
// methods, which crash under Go 1.25's stricter cgo pointer checker due
// to the way ReinterpretCast passes pointers across the cgo boundary.
func clampMonitorWorkBounds() {
	pio := imgui.CurrentPlatformIO()
	monitors := pio.Monitors()
	if monitors.Size == 0 {
		return
	}

	// monitors.Data is *PlatformMonitor, a Go struct whose sole field
	// is CData (*C.ImGuiPlatformMonitor), a C pointer to the first
	// element of the C array of monitors.
	arrayBase := *(*unsafe.Pointer)(unsafe.Pointer(monitors.Data))

	// C struct layout (64-bit):
	//   ImVec2 MainPos  [offset  0] (2 x float32)
	//   ImVec2 MainSize [offset  8]
	//   ImVec2 WorkPos  [offset 16]
	//   ImVec2 WorkSize [offset 24]
	//   float  DpiScale [offset 32]
	//   void*  PlatformHandle [offset 40, with 4 bytes padding]
	// Total size: 48 bytes on 64-bit, 40 on 32-bit.
	ptrSize := unsafe.Sizeof(uintptr(0))
	monitorSize := uintptr(36) + (ptrSize-(36%ptrSize))%ptrSize + ptrSize

	for i := range monitors.Size {
		mon := unsafe.Add(arrayBase, int(uintptr(i)*monitorSize))

		mainPosX := *(*float32)(unsafe.Add(mon, 0))
		mainPosY := *(*float32)(unsafe.Add(mon, 4))
		mainSizeX := *(*float32)(unsafe.Add(mon, 8))
		mainSizeY := *(*float32)(unsafe.Add(mon, 12))

		// Guard against monitors with uninitialized main bounds (e.g. during
		// hot-plug or driver quirks). imgui asserts MainSize > 0 in NewFrame.
		if mainSizeX <= 0 || mainSizeY <= 0 {
			*(*float32)(unsafe.Add(mon, 8)) = 1
			*(*float32)(unsafe.Add(mon, 12)) = 1
			*(*float32)(unsafe.Add(mon, 16)) = mainPosX // WorkPos = MainPos
			*(*float32)(unsafe.Add(mon, 20)) = mainPosY
			*(*float32)(unsafe.Add(mon, 24)) = 1
			*(*float32)(unsafe.Add(mon, 28)) = 1
			continue
		}

		workPosX := (*float32)(unsafe.Add(mon, 16))
		workPosY := (*float32)(unsafe.Add(mon, 20))
		workSizeX := (*float32)(unsafe.Add(mon, 24))
		workSizeY := (*float32)(unsafe.Add(mon, 28))

		mainRight := mainPosX + mainSizeX
		mainBottom := mainPosY + mainSizeY

		if *workPosX < mainPosX {
			*workSizeX -= mainPosX - *workPosX
			*workPosX = mainPosX
		}
		if *workPosY < mainPosY {
			*workSizeY -= mainPosY - *workPosY
			*workPosY = mainPosY
		}
		if *workPosX+*workSizeX > mainRight {
			*workSizeX = mainRight - *workPosX
		}
		if *workPosY+*workSizeY > mainBottom {
			*workSizeY = mainBottom - *workPosY
		}

		// Safety net: if work bounds are still invalid after clamping
		// (e.g. negative size from work area entirely outside main
		// bounds, or GLFW reporting bogus coordinates during display
		// changes), fall back to main bounds as imgui recommends.
		if *workSizeX <= 0 || *workSizeY <= 0 ||
			*workPosX < mainPosX || *workPosY < mainPosY ||
			*workPosX+*workSizeX > mainRight || *workPosY+*workSizeY > mainBottom {
			*workPosX = mainPosX
			*workPosY = mainPosY
			*workSizeX = mainSizeX
			*workSizeY = mainSizeY
		}
	}
}

func (g *glfwPlatform) getCursorPos() [2]float32 {
	x, y := g.window.GetCursorPos()
	return [2]float32{float32(int(x)), float32(int(y))}
}

func (g *glfwPlatform) PostRender() {
	g.window.SwapBuffers()
}

// RenderImgui draws imgui's contents with the OpenGL 3 backend. Both the
// main window and the secondary viewports go through it, which avoids the
// DPI discrepancies that came from drawing imgui with our own renderer.
func (g *glfwPlatform) RenderImgui() {
	imgui.Render()
	implogl3.RenderDrawData(imgui.CurrentDrawData())

	if imgui.CurrentIO().ConfigFlags()&imgui.ConfigFlagsViewportsEnable != 0 {
		imgui.UpdatePlatformWindows()
		imgui.RenderPlatformWindowsDefault()
		g.MakeContextCurrent()
	}
}

func (g *glfwPlatform) MakeContextCurrent() {
	defer func() {
		if r := recover(); r != nil {
			// The GLFW Go bindings queue errors in a channel and
			// panic on the next GLFW call if the error wasn't
			// consumed. Clipboard FormatUnavailable errors on
			// Windows can leak through imgui's internal GLFW calls
			// and surface here.
		}
	}()
	g.window.MakeContextCurrent()
}

// InitViewportBackends initializes the imgui GLFW and OpenGL3 backends
// for multi-viewport support. Must be called after OpenGL is initialized
// (i.e., after gl.Init() in ogl21.NewRenderer).
func (g *glfwPlatform) InitViewportBackends() {
	// Extract the raw *C.GLFWwindow pointer from go-gl/glfw's Window.
	// Window.data (*C.GLFWwindow) is the first field of the struct.
	rawPtr := *(*unsafe.Pointer)(unsafe.Pointer(g.window))
	implWindow := implglfw.NewGLFWwindowFromC(rawPtr)

	// Initialize the GLFW backend WITHOUT installing callbacks yet.
	implglfw.InitForOpenGL(implWindow, false)
	// Install callbacks on the main window. The default chaining behavior
	// chains only for the main window (window == bd->Window), which ensures
	// go-gl/glfw's callbacks fire for the main window but not for secondary
	// viewport windows (which go-gl/glfw doesn't know about).
	implglfw.InstallCallbacks(implWindow)

	// Initialize the OpenGL3 backend with GLSL 1.20 for OpenGL 2.1 compatibility.
	implogl3.InitV("#version 120")
	g.ensureViewportMonitors()

}

// SetViewportFloating sets the GLFW_FLOATING attribute on a secondary
// viewport window identified by its raw GLFWwindow* handle. The GLFW
// backend only sets this at window creation; this allows dynamic toggling
// for pin/unpin behavior.
func (g *glfwPlatform) SetViewportFloating(handle uintptr, floating bool) {
	if handle == 0 {
		return
	}
	// Wrap the raw GLFWwindow* as a go-gl/glfw Window.
	// glfw3.Window's first field is data *C.GLFWwindow.
	var win glfw3.Window
	*(*unsafe.Pointer)(unsafe.Pointer(&win)) = unsafe.Add(nil, handle)

	val := 0
	if floating {
		val = 1
	}
	win.SetAttrib(glfw3.Floating, val)
}

func (g *glfwPlatform) IsAppFocused() bool {
	return g.appFocused
}

func (g *glfwPlatform) installCallbacks() {
	g.window.SetMouseButtonCallback(g.mouseButtonChange)
	g.window.SetScrollCallback(g.mouseScrollChange)
	g.window.SetKeyCallback(g.keyChange)
	g.window.SetCharCallback(g.charChange)
	g.window.SetFocusCallback(g.focusChange)
}

func (g *glfwPlatform) focusChange(window *glfw3.Window, focused bool) {
	g.appFocused = focused
	g.anyEvents = true
}

var glfwButtonIndexByID = map[glfw3.MouseButton]imgui.MouseButton{
	glfw3.MouseButton1: platform.MouseButtonPrimary,
	glfw3.MouseButton2: platform.MouseButtonSecondary,
	glfw3.MouseButton3: platform.MouseButtonTertiary,
}

var glfwButtonIDByIndex = map[imgui.MouseButton]glfw3.MouseButton{
	platform.MouseButtonPrimary:   glfw3.MouseButton1,
	platform.MouseButtonSecondary: glfw3.MouseButton2,
	platform.MouseButtonTertiary:  glfw3.MouseButton3,
}

func (g *glfwPlatform) mouseButtonChange(window *glfw3.Window, rawButton glfw3.MouseButton, action glfw3.Action, mods glfw3.ModifierKey) {
	buttonIndex, known := glfwButtonIndexByID[rawButton]

	if !known {
		return
	}

	g.anyEvents = true
	if action == glfw3.Press {
		g.mouseJustPressed[buttonIndex] = true
	}
	g.updateKeyModifiers()
}

func (g *glfwPlatform) mouseScrollChange(window *glfw3.Window, x, y float64) {
	g.anyEvents = true
	g.imguiIO.AddMouseWheelDelta(float32(x), float32(y))
}

func (g *glfwPlatform) keyChange(window *glfw3.Window, keycode glfw3.Key, scancode int, action glfw3.Action, mods glfw3.ModifierKey) {
	g.anyEvents = true
	g.updateKeyModifiers()

	// TODO: this can probably be done more cleanly/consistently through imgui
	for i, k := range []glfw3.Key{glfw3.KeyF1, glfw3.KeyF2, glfw3.KeyF3, glfw3.KeyF4, glfw3.KeyF5, glfw3.KeyF6, glfw3.KeyF7, glfw3.KeyF8,
		glfw3.KeyF9, glfw3.KeyF10, glfw3.KeyF11, glfw3.KeyF12, glfw3.KeyF13, glfw3.KeyF14, glfw3.KeyF15, glfw3.KeyF16} {
		if g.window.GetKey(k) == glfw3.Press {
			g.heldFKeys[imgui.KeyF1+imgui.Key(i)] = nil
		}
		if g.window.GetKey(k) == glfw3.Release {
			delete(g.heldFKeys, imgui.KeyF1+imgui.Key(i))
		}
	}

	if action != glfw3.Press && action != glfw3.Release {
		return
	}

	kc := translateUntranslatedKey(keycode, scancode)
	imguikey := glfwKeyToImguiKey(kc)
	g.imguiIO.AddKeyEvent(imguikey, action == glfw3.Press)
}

func (g *glfwPlatform) updateKeyModifiers() {
	g.imguiIO.AddKeyEvent(imgui.ModShift, g.window.GetKey(glfw3.KeyLeftShift) == glfw3.Press || g.window.GetKey(glfw3.KeyRightShift) == glfw3.Press)
	g.imguiIO.AddKeyEvent(imgui.ModAlt, g.window.GetKey(glfw3.KeyLeftAlt) == glfw3.Press || g.window.GetKey(glfw3.KeyRightAlt) == glfw3.Press)
	g.imguiIO.AddKeyEvent(imgui.ModCtrl, g.window.GetKey(glfw3.KeyLeftControl) == glfw3.Press || g.window.GetKey(glfw3.KeyRightControl) == glfw3.Press)
	g.imguiIO.AddKeyEvent(imgui.ModSuper, g.window.GetKey(glfw3.KeyLeftSuper) == glfw3.Press || g.window.GetKey(glfw3.KeyRightSuper) == glfw3.Press)
}

func (g *glfwPlatform) charChange(window *glfw3.Window, char rune) {
	g.anyEvents = true
	// imgui character input is handled by implglfw.InstallCallbacks.
	g.inputCharacters = g.inputCharacters + string(char)
}

func (g *glfwPlatform) createMouseCursors() {
	g.mouseCursors[imgui.MouseCursorArrow] = glfw3.CreateStandardCursor(glfw3.ArrowCursor)
	g.mouseCursors[imgui.MouseCursorTextInput] = glfw3.CreateStandardCursor(glfw3.IBeamCursor)
	g.mouseCursors[imgui.MouseCursorResizeAll] = glfw3.CreateStandardCursor(glfw3.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNS] = glfw3.CreateStandardCursor(glfw3.VResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeEW] = glfw3.CreateStandardCursor(glfw3.HResizeCursor)
	g.mouseCursors[imgui.MouseCursorResizeNESW] = glfw3.CreateStandardCursor(glfw3.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorResizeNWSE] = glfw3.CreateStandardCursor(glfw3.ArrowCursor) // FIXME: GLFW doesn't have this.
	g.mouseCursors[imgui.MouseCursorHand] = glfw3.CreateStandardCursor(glfw3.HandCursor)

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
	window *glfw3.Window
}

func (cb glfwClipboard) GetClipboard() (result string) {
	defer func() {
		if r := recover(); r != nil {
			// On Windows, the GLFW clipboard can panic with
			// FormatUnavailable if the clipboard contents can't be
			// converted to a string (e.g., an image is copied).
			result = ""
		}
	}()
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
func translateUntranslatedKey(key glfw3.Key, scancode int) glfw3.Key {
	if key >= glfw3.KeyKP0 && key <= glfw3.KeyKPEqual {
		return key
	}
	name := glfw3.GetKeyName(key, scancode)
	// glfw3.GetError(nil)
	if len(name) == 1 {
		if name[0] >= '0' && name[0] <= '9' {
			return glfw3.Key0 + glfw3.Key(name[0]-'0')
		} else if name[0] >= 'A' && name[0] <= 'Z' {
			return glfw3.KeyA + glfw3.Key(name[0]-'A')
		} else if name[0] >= 'a' && name[0] <= 'z' {
			return glfw3.KeyA + glfw3.Key(name[0]-'a')
		} else {
			chars := map[byte]glfw3.Key{
				'`':  glfw3.KeyGraveAccent,
				'-':  glfw3.KeyMinus,
				'=':  glfw3.KeyEqual,
				'[':  glfw3.KeyLeftBracket,
				']':  glfw3.KeyRightBracket,
				'\\': glfw3.KeyBackslash,
				',':  glfw3.KeyComma,
				';':  glfw3.KeySemicolon,
				'\'': glfw3.KeyApostrophe,
				'.':  glfw3.KeyPeriod,
				'/':  glfw3.KeySlash,
			}
			if k, ok := chars[name[0]]; ok {
				return k
			}
		}
	}
	return key
}

func glfwKeyToImguiKey(keycode glfw3.Key) imgui.Key {
	switch keycode {
	case glfw3.KeyTab:
		return imgui.KeyTab
	case glfw3.KeyLeft:
		return imgui.KeyLeftArrow
	case glfw3.KeyRight:
		return imgui.KeyRightArrow
	case glfw3.KeyUp:
		return imgui.KeyUpArrow
	case glfw3.KeyDown:
		return imgui.KeyDownArrow
	case glfw3.KeyPageUp:
		return imgui.KeyPageUp
	case glfw3.KeyPageDown:
		return imgui.KeyPageDown
	case glfw3.KeyHome:
		return imgui.KeyHome
	case glfw3.KeyEnd:
		return imgui.KeyEnd
	case glfw3.KeyInsert:
		return imgui.KeyInsert
	case glfw3.KeyDelete:
		return imgui.KeyDelete
	case glfw3.KeyBackspace:
		return imgui.KeyBackspace
	case glfw3.KeySpace:
		return imgui.KeySpace
	case glfw3.KeyEnter:
		return imgui.KeyEnter
	case glfw3.KeyEscape:
		return imgui.KeyEscape
	case glfw3.KeyApostrophe:
		return imgui.KeyApostrophe
	case glfw3.KeyComma:
		return imgui.KeyComma
	case glfw3.KeyMinus:
		return imgui.KeyMinus
	case glfw3.KeyPeriod:
		return imgui.KeyPeriod
	case glfw3.KeySlash:
		return imgui.KeySlash
	case glfw3.KeySemicolon:
		return imgui.KeySemicolon
	case glfw3.KeyEqual:
		return imgui.KeyEqual
	case glfw3.KeyLeftBracket:
		return imgui.KeyLeftBracket
	case glfw3.KeyBackslash:
		return imgui.KeyBackslash
	case glfw3.KeyWorld1:
		return imgui.KeyOem102
	case glfw3.KeyWorld2:
		return imgui.KeyOem102
	case glfw3.KeyRightBracket:
		return imgui.KeyRightBracket
	case glfw3.KeyGraveAccent:
		return imgui.KeyGraveAccent
	case glfw3.KeyCapsLock:
		return imgui.KeyCapsLock
	case glfw3.KeyScrollLock:
		return imgui.KeyScrollLock
	case glfw3.KeyNumLock:
		return imgui.KeyNumLock
	case glfw3.KeyPrintScreen:
		return imgui.KeyPrintScreen
	case glfw3.KeyPause:
		return imgui.KeyPause
	case glfw3.KeyKP0:
		return imgui.KeyKeypad0
	case glfw3.KeyKP1:
		return imgui.KeyKeypad1
	case glfw3.KeyKP2:
		return imgui.KeyKeypad2
	case glfw3.KeyKP3:
		return imgui.KeyKeypad3
	case glfw3.KeyKP4:
		return imgui.KeyKeypad4
	case glfw3.KeyKP5:
		return imgui.KeyKeypad5
	case glfw3.KeyKP6:
		return imgui.KeyKeypad6
	case glfw3.KeyKP7:
		return imgui.KeyKeypad7
	case glfw3.KeyKP8:
		return imgui.KeyKeypad8
	case glfw3.KeyKP9:
		return imgui.KeyKeypad9
	case glfw3.KeyKPDecimal:
		return imgui.KeyKeypadDecimal
	case glfw3.KeyKPDivide:
		return imgui.KeyKeypadDivide
	case glfw3.KeyKPMultiply:
		return imgui.KeyKeypadMultiply
	case glfw3.KeyKPSubtract:
		return imgui.KeyKeypadSubtract
	case glfw3.KeyKPAdd:
		return imgui.KeyKeypadAdd
	case glfw3.KeyKPEnter:
		return imgui.KeyKeypadEnter
	case glfw3.KeyKPEqual:
		return imgui.KeyKeypadEqual
	case glfw3.KeyLeftShift:
		return imgui.KeyLeftShift
	case glfw3.KeyLeftControl:
		return imgui.KeyLeftCtrl
	case glfw3.KeyLeftAlt:
		return imgui.KeyLeftAlt
	case glfw3.KeyLeftSuper:
		return imgui.KeyLeftSuper
	case glfw3.KeyRightShift:
		return imgui.KeyRightShift
	case glfw3.KeyRightControl:
		return imgui.KeyRightCtrl
	case glfw3.KeyRightAlt:
		return imgui.KeyRightAlt
	case glfw3.KeyRightSuper:
		return imgui.KeyRightSuper
	case glfw3.KeyMenu:
		return imgui.KeyMenu
	case glfw3.Key0:
		return imgui.Key0
	case glfw3.Key1:
		return imgui.Key1
	case glfw3.Key2:
		return imgui.Key2
	case glfw3.Key3:
		return imgui.Key3
	case glfw3.Key4:
		return imgui.Key4
	case glfw3.Key5:
		return imgui.Key5
	case glfw3.Key6:
		return imgui.Key6
	case glfw3.Key7:
		return imgui.Key7
	case glfw3.Key8:
		return imgui.Key8
	case glfw3.Key9:
		return imgui.Key9
	case glfw3.KeyA:
		return imgui.KeyA
	case glfw3.KeyB:
		return imgui.KeyB
	case glfw3.KeyC:
		return imgui.KeyC
	case glfw3.KeyD:
		return imgui.KeyD
	case glfw3.KeyE:
		return imgui.KeyE
	case glfw3.KeyF:
		return imgui.KeyF
	case glfw3.KeyG:
		return imgui.KeyG
	case glfw3.KeyH:
		return imgui.KeyH
	case glfw3.KeyI:
		return imgui.KeyI
	case glfw3.KeyJ:
		return imgui.KeyJ
	case glfw3.KeyK:
		return imgui.KeyK
	case glfw3.KeyL:
		return imgui.KeyL
	case glfw3.KeyM:
		return imgui.KeyM
	case glfw3.KeyN:
		return imgui.KeyN
	case glfw3.KeyO:
		return imgui.KeyO
	case glfw3.KeyP:
		return imgui.KeyP
	case glfw3.KeyQ:
		return imgui.KeyQ
	case glfw3.KeyR:
		return imgui.KeyR
	case glfw3.KeyS:
		return imgui.KeyS
	case glfw3.KeyT:
		return imgui.KeyT
	case glfw3.KeyU:
		return imgui.KeyU
	case glfw3.KeyV:
		return imgui.KeyV
	case glfw3.KeyW:
		return imgui.KeyW
	case glfw3.KeyX:
		return imgui.KeyX
	case glfw3.KeyY:
		return imgui.KeyY
	case glfw3.KeyZ:
		return imgui.KeyZ
	case glfw3.KeyF1:
		return imgui.KeyF1
	case glfw3.KeyF2:
		return imgui.KeyF2
	case glfw3.KeyF3:
		return imgui.KeyF3
	case glfw3.KeyF4:
		return imgui.KeyF4
	case glfw3.KeyF5:
		return imgui.KeyF5
	case glfw3.KeyF6:
		return imgui.KeyF6
	case glfw3.KeyF7:
		return imgui.KeyF7
	case glfw3.KeyF8:
		return imgui.KeyF8
	case glfw3.KeyF9:
		return imgui.KeyF9
	case glfw3.KeyF10:
		return imgui.KeyF10
	case glfw3.KeyF11:
		return imgui.KeyF11
	case glfw3.KeyF12:
		return imgui.KeyF12
	case glfw3.KeyF13:
		return imgui.KeyF13
	case glfw3.KeyF14:
		return imgui.KeyF14
	case glfw3.KeyF15:
		return imgui.KeyF15
	case glfw3.KeyF16:
		return imgui.KeyF16
	case glfw3.KeyF17:
		return imgui.KeyF17
	case glfw3.KeyF18:
		return imgui.KeyF18
	case glfw3.KeyF19:
		return imgui.KeyF19
	case glfw3.KeyF20:
		return imgui.KeyF20
	case glfw3.KeyF21:
		return imgui.KeyF21
	case glfw3.KeyF22:
		return imgui.KeyF22
	case glfw3.KeyF23:
		return imgui.KeyF23
	case glfw3.KeyF24:
		return imgui.KeyF24
	default:
		return imgui.KeyNone
	}
}

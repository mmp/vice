// scope/scope.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"runtime"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// Scope is a radar scope display--STARS or ERAM. It operates in window
// coordinates: (0,0) is lower left, just in its own drawing area, oblivious
// to the full window size.
type Scope interface {
	// Activate is called once at startup time; it should do general,
	// Sim-independent initialization.
	Activate(r renderer.Renderer, p platform.Platform, lg *log.Logger)

	// LoadedSim is called when vice is restarted and a Sim is loaded from disk.
	LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger)

	// ResetSim is called when a brand new Sim is launched
	ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger)

	CanTakeKeyboardFocus() bool
	Draw(ctx *Context, cb *renderer.CommandBuffer)
}

// UIDrawer is implemented by displays that contribute a section to the
// settings window.
type UIDrawer interface {
	DisplayName() string
	DrawUI(p platform.Platform, config *platform.Config)
}

// InfoWindowDrawer is implemented by displays that add to the scenario
// info window.
type InfoWindowDrawer interface {
	DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger)
}

// Upgrader is implemented by displays that must migrate saved
// preferences when the config version changes.
type Upgrader interface {
	Upgrade(prev, current int)
}

var (
	wm struct {
		// Normally the window that the mouse is over gets mouse events,
		// though if the user has started a click-drag, then the one that
		// received the click keeps getting events until the mouse button
		// is released.  mouseConsumerOverride records such a scope.
		mouseConsumerOverride Scope

		focus KeyboardFocus
	}
)

type KeyboardFocus struct {
	current any
}

func (f *KeyboardFocus) Take(p any) {
	f.current = p
}

func (f *KeyboardFocus) Release() {
	f.current = nil
}

func (f *KeyboardFocus) Current() any {
	return f.current
}

// DrawScope renders the radar scope, which fills the entire display area
// below the menu bar. Messages and flight strips are drawn separately, in
// their own floating imgui windows.
func DrawScope(sc Scope, p platform.Platform, r renderer.Renderer,
	controlClient *client.ControlClient, menuBarHeight float32, events []sim.Event, lg *log.Logger) renderer.Stats {
	if controlClient == nil {
		commandBuffer := renderer.GetCommandBuffer(lg)
		defer renderer.ReturnCommandBuffer(commandBuffer)
		commandBuffer.ClearRGB(renderer.RGB{})
		return r.RenderCommandBuffer(commandBuffer)
	}

	if wm.focus.Current() == nil || wm.focus.Current() != sc {
		if sc.CanTakeKeyboardFocus() {
			wm.focus.Take(sc)
		}
	}

	fbSize := p.FramebufferSize()
	displaySize := p.DisplaySize()

	// Area left for actually drawing the scope
	scopeDisplayExtent := math.Extent2D{
		P0: [2]float32{0, 0},
		P1: [2]float32{displaySize[0], displaySize[1] - menuBarHeight},
	}

	// Get the mouse position from imgui; convert from screen coordinates
	// to main-window-relative coordinates (with multi-viewport, MousePos
	// returns OS screen coords), then flip y to match our window coords.
	mainViewportPos := imgui.MainViewport().Pos()
	mousePos := [2]float32{
		imgui.MousePos().X - mainViewportPos.X,
		displaySize[1] - 1 - (imgui.MousePos().Y - mainViewportPos.Y),
	}

	io := imgui.CurrentIO()

	// If the user has clicked or is dragging in the scope, record it in
	// mouseConsumerOverride so that we continue to dispatch mouse
	// events until the mouse button is released.
	isDragging := imgui.IsMouseDraggingV(platform.MouseButtonPrimary, 0.) ||
		imgui.IsMouseDraggingV(platform.MouseButtonSecondary, 0.) ||
		imgui.IsMouseDraggingV(platform.MouseButtonTertiary, 0.)
	isClicked := imgui.IsMouseClickedBool(platform.MouseButtonPrimary) ||
		imgui.IsMouseClickedBool(platform.MouseButtonSecondary) ||
		imgui.IsMouseClickedBool(platform.MouseButtonTertiary)
	if !io.WantCaptureMouse() && (isDragging || isClicked) && wm.mouseConsumerOverride == nil {
		wm.mouseConsumerOverride = sc
	} else if io.WantCaptureMouse() {
		wm.mouseConsumerOverride = nil
	}

	p.ClearCursorOverride()

	commandBuffer := renderer.GetCommandBuffer(lg)
	defer renderer.ReturnCommandBuffer(commandBuffer)
	commandBuffer.ClearRGB(renderer.RGB{})

	var keyboard *platform.KeyboardState
	if !imgui.CurrentIO().WantCaptureKeyboard() {
		keyboard = p.GetKeyboard()
	}

	haveFocus := sc == wm.focus.Current() && !imgui.CurrentIO().WantCaptureKeyboard()
	ctx := Context{
		DrawExtent:          scopeDisplayExtent,
		ParentDrawExtent:    scopeDisplayExtent,
		Platform:            p,
		DrawPixelScale:      util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1)),
		PixelsPerInch:       util.Select(runtime.GOOS == "windows", 96*p.DPIScale(), float32(72)),
		DPIScale:            p.DPIScale(),
		Renderer:            r,
		Keyboard:            keyboard,
		HaveFocus:           haveFocus,
		InterpolatedSimTime: controlClient.InterpolatedSimTime(),
		Events:              events,
		Lg:                  lg,
		MenuBarHeight:       menuBarHeight,
		KeyboardFocus:       &wm.focus,
		Client:              controlClient,
		UserTCW:             controlClient.State.UserTCW,
		NmPerLongitude:      controlClient.State.NmPerLongitude,
		MagneticVariation:   controlClient.State.MagneticVariation,
		FacilityAdaptation:  &controlClient.State.FacilityAdaptation,
		displaySize:         p.DisplaySize(),
	}

	ownsMouse := wm.mouseConsumerOverride == sc ||
		(wm.mouseConsumerOverride == nil &&
			!io.WantCaptureMouse() &&
			scopeDisplayExtent.Inside(mousePos))
	if ownsMouse {
		ctx.InitializeMouse(p)
	}

	commandBuffer.SetDrawBounds(scopeDisplayExtent, p.FramebufferSize()[1]/p.DisplaySize()[1])
	sc.Draw(&ctx, commandBuffer)
	commandBuffer.ResetState()

	if !isDragging && !isClicked {
		wm.mouseConsumerOverride = nil
	}

	if fbSize[0] > 0 && fbSize[1] > 0 {
		return r.RenderCommandBuffer(commandBuffer)
	}
	return renderer.Stats{}
}

// pkg/panes/panes.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	// Activate is called once at startup time; it should do general,
	// Sim-independent initialization.
	Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger)

	// LoadedSim is called when vice is restarted and a Sim is loaded from disk.
	LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger)

	// ResetSim is called when a brand new Sim is launched
	ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger)

	CanTakeKeyboardFocus() bool
	Draw(ctx *Context, cb *renderer.CommandBuffer)
}

type UIDrawer interface {
	DisplayName() string
	DrawUI(p platform.Platform, config *platform.Config)
}

type InfoWindowDrawer interface {
	DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger)
}

type PaneUpgrader interface {
	Upgrade(prev, current int)
}

var UICautionColor renderer.RGB = renderer.RGBFromHex(0xB7B513)
var UITextColor renderer.RGB = renderer.RGB{R: 0.85, G: 0.85, B: 0.85}
var UITextHighlightColor renderer.RGB = renderer.RGBFromHex(0xB2B338)
var UIErrorColor renderer.RGB = renderer.RGBFromHex(0xE94242)

type Context struct {
	PaneExtent       math.Extent2D
	ParentPaneExtent math.Extent2D

	Platform platform.Platform
	// If we want something to be n pixels big, scale factor to apply to n.
	// (This is necessary for Windows high-DPI displays, which just expose
	// their native resolution to us, vs. Mac which pretends retina
	// displays are 72dpi as far as graphics commands.)
	DrawPixelScale float32
	PixelsPerInch  float32
	// DPIScale is similar to DrawPixelScale but always includes the
	// "retina" factor; this is mostly useful for drawing "chunky" 1
	// pixel-wide lines and the like.
	DPIScale float32

	Renderer  renderer.Renderer
	Mouse     *platform.MouseState
	Keyboard  *platform.KeyboardState
	HaveFocus bool
	Now       time.Time
	Lg        *log.Logger

	MenuBarHeight float32

	KeyboardFocus *KeyboardFocus

	Client *client.ControlClient

	// from Client.State, here for convenience
	UserTCW            sim.TCW // User's workstation identifier
	NmPerLongitude     float32
	MagneticVariation  float32
	FacilityAdaptation *sim.FacilityAdaptation

	// Full display size, including the menu and status bar.
	displaySize [2]float32
}

func (ctx *Context) InitializeMouse(p platform.Platform) {
	ctx.Mouse = p.GetMouse()

	ctx.Mouse.Pos = ctx.WindowToPane(ctx.Mouse.Pos)
	// Negate y to go to pane coordinates
	ctx.Mouse.DeltaPos[1] *= -1
	ctx.Mouse.Wheel[1] *= -1
	ctx.Mouse.DragDelta[1] *= -1
}

// NewFuzzContext creates a Context suitable for fuzz testing.
// This is used by the -starsrandoms mode to inject random commands.
func NewFuzzContext(p platform.Platform, r renderer.Renderer, c *client.ControlClient, lg *log.Logger) *Context {
	displaySize := p.DisplaySize()
	return &Context{
		PaneExtent:         math.Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{displaySize[0], displaySize[1]}},
		ParentPaneExtent:   math.Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{displaySize[0], displaySize[1]}},
		Platform:           p,
		DrawPixelScale:     1,
		PixelsPerInch:      72,
		DPIScale:           p.DPIScale(),
		Renderer:           r,
		HaveFocus:          true,
		Now:                time.Now(),
		Lg:                 lg,
		Client:             c,
		UserTCW:            c.State.UserTCW,
		NmPerLongitude:     c.State.NmPerLongitude,
		MagneticVariation:  c.State.MagneticVariation,
		FacilityAdaptation: &c.State.FacilityAdaptation,
		displaySize:        displaySize,
	}
}

func (ctx *Context) SetMousePosition(p [2]float32) {
	ctx.Mouse.Pos = p
	ctx.Platform.SetMousePosition(ctx.PaneToWindow(p))
}

// Convert to pane coordinates:
// platform gives us the mouse position w.r.t. the full window, so we need
// to subtract out displayExtent.p0 to get coordinates w.r.t. the
// current pane.  Further, it has (0,0) in the upper left corner of the
// window, so we need to flip y w.r.t. the full window resolution.
func (ctx *Context) WindowToPane(p [2]float32) [2]float32 {
	return [2]float32{
		p[0] - ctx.PaneExtent.P0[0],
		ctx.displaySize[1] - 1 - ctx.PaneExtent.P0[1] - p[1],
	}
}

func (ctx *Context) PaneToWindow(p [2]float32) [2]float32 {
	return [2]float32{
		p[0] + ctx.PaneExtent.P0[0],
		-(p[1] - ctx.displaySize[1] + 1 + ctx.PaneExtent.P0[1]),
	}
}

func (ctx *Context) SetWindowCoordinateMatrices(cb *renderer.CommandBuffer) {
	w := float32(int(ctx.PaneExtent.Width() + 0.5))
	h := float32(int(ctx.PaneExtent.Height() + 0.5))
	cb.LoadProjectionMatrix(math.Identity3x3().Ortho(0, w, 0, h))
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// Convenience methods since these are frequently used.
func (ctx *Context) GetTrackByCallsign(callsign av.ADSBCallsign) (*sim.Track, bool) {
	return ctx.Client.State.GetTrackByCallsign(callsign)
}

func (ctx *Context) GetOurTrackByCallsign(callsign av.ADSBCallsign) (*sim.Track, bool) {
	return ctx.Client.State.GetOurTrackByCallsign(callsign)
}

func (ctx *Context) GetTrackByACID(acid sim.ACID) (*sim.Track, bool) {
	return ctx.Client.State.GetTrackByACID(acid)
}

func (ctx *Context) GetOurTrackByACID(acid sim.ACID) (*sim.Track, bool) {
	return ctx.Client.State.GetOurTrackByACID(acid)
}

// UserControlsPosition returns true if the current user controls the given position
// (either as their primary position or as a consolidated secondary).
func (ctx *Context) UserControlsPosition(pos sim.ControlPosition) bool {
	return ctx.Client.State.UserControlsPosition(pos)
}

// UserOwnsFlightPlan returns true if the current user owns the given flight plan.
// Track ownership is determined by the OwningTCW field.
func (ctx *Context) UserOwnsFlightPlan(fp *sim.NASFlightPlan) bool {
	return fp != nil && fp.OwningTCW == ctx.UserTCW
}

// ResolveController returns the signed-in controller who controls the given position.
// If the position is a primary controller, it returns it as-is. If it is
// a consolidated secondary, it returns the primary position of the controlling TCW.
func (ctx *Context) ResolveController(pos sim.ControlPosition) sim.ControlPosition {
	return ctx.Client.State.ResolveController(pos)
}

// GetResolvedController returns the controller for the given position, resolving consolidated
// secondary positions to their controlling primary. Returns nil if not found.
func (ctx *Context) GetResolvedController(pos sim.ControlPosition) *av.Controller {
	return ctx.Client.State.Controllers[ctx.Client.State.ResolveController(pos)]
}

// UserPrimaryPosition returns the user's primary position (the position they signed into).
func (ctx *Context) UserPrimaryPosition() sim.ControlPosition {
	return ctx.Client.State.PrimaryPositionForTCW(ctx.UserTCW)
}

func (ctx *Context) PrimaryTCPForTCW(tcw sim.TCW) sim.TCP {
	return sim.TCP(ctx.Client.State.PrimaryPositionForTCW(tcw))
}

// UserController returns the Controller for the user's primary position.
func (ctx *Context) UserController() *av.Controller {
	return ctx.Client.State.Controllers[ctx.UserPrimaryPosition()]
}

func (ctx *Context) TCWIsPrivileged(tcw sim.TCW) bool {
	return ctx.Client.State.TCWIsPrivileged(tcw)
}

// UserWasRedirector returns true if any of the user's controlled positions
// are in the given redirector list.
func (ctx *Context) UserWasRedirector(redirectors []sim.ControlPosition) bool {
	for _, pos := range ctx.Client.State.GetPositionsForTCW(ctx.UserTCW) {
		if slices.Contains(redirectors, pos) {
			return true
		}
	}
	return false
}

// IsHandoffToUser returns true if the given track is being handed off to any
// of the user's controlled TCPs (primary or secondary).
func (ctx *Context) IsHandoffToUser(trk *sim.Track) bool {
	if trk.IsUnassociated() {
		return false
	}
	sfp := trk.FlightPlan
	// Check if handoff target is one of user's TCPs
	if !ctx.UserControlsPosition(sfp.HandoffController) {
		return false
	}
	// Not if we're a redirector (unless redirected back to us)
	if ctx.UserWasRedirector(sfp.RedirectedHandoff.Redirector) &&
		!ctx.UserControlsPosition(sfp.RedirectedHandoff.RedirectedTo) {
		return false
	}
	return true
}

// Returns all aircraft that match the given suffix. If instructor, returns
// all matching aircraft; otherwise only ones under the current
// controller's control are considered for matching.
func (ctx *Context) TracksFromACIDSuffix(suffix string) []*sim.Track {
	match := func(trk *sim.Track) bool {
		if trk.IsUnassociated() {
			return strings.HasSuffix(string(trk.ADSBCallsign), suffix)
		} else {
			fp := trk.FlightPlan
			if !strings.HasSuffix(string(fp.ACID), suffix) {
				return false
			}

			if ctx.UserControlsPosition(trk.ControllerFrequency) || ctx.TCWIsPrivileged(ctx.UserTCW) {
				return true
			}

			// Hold for release aircraft still in the list
			if ctx.UserOwnsFlightPlan(trk.FlightPlan) && trk.ControllerFrequency == "" {
				return true
			}
			return false
		}
	}
	return slices.Collect(util.FilterSeq(maps.Values(ctx.Client.State.Tracks), match))
}

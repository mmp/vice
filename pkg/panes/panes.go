// pkg/panes/panes.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"fmt"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/client"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	// Activate is called once at startup time; it should do general,
	// Sim-independent initialization.
	Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger)

	// LoadedSim is called when vice is restarted and a Sim is loaded from disk.
	LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger)

	// ResetSim is called when a brand new Sim is launched
	ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger)

	CanTakeKeyboardFocus() bool
	Hide() bool
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

var UIControlColor renderer.RGB = renderer.RGB{R: 0.2754237, G: 0.2754237, B: 0.2754237}
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
	UserTCP            string
	NmPerLongitude     float32
	MagneticVariation  float32
	FacilityAdaptation *sim.STARSFacilityAdaptation

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

var paneUnmarshalRegistry map[string]func([]byte) (Pane, error) = make(map[string]func([]byte) (Pane, error))

func RegisterUnmarshalPane(name string, fn func([]byte) (Pane, error)) {
	if _, ok := paneUnmarshalRegistry[name]; ok {
		panic(name + " registered multiple times")
	}
	paneUnmarshalRegistry[name] = fn
}

func UnmarshalPane(paneType string, data []byte) (Pane, error) {
	if paneType == "" {
		return nil, nil
	} else if _, name, ok := strings.Cut(paneType, "."); ok { // e.g. "*panes.MessagesPane"
		if fn, ok := paneUnmarshalRegistry[name]; ok {
			return fn(data)
		}
	}
	fmt.Printf("reg %+v\n\n", paneUnmarshalRegistry)
	return NewEmptyPane(), fmt.Errorf("%s: Unhandled type in config file", paneType)
}

///////////////////////////////////////////////////////////////////////////
// EmptyPane

type EmptyPane struct {
	// Empty struct types may all have the same address, which breaks
	// assorted assumptions elsewhere in the system....
	Wtfgo int
}

func NewEmptyPane() *EmptyPane { return &EmptyPane{} }

func init() {
	RegisterUnmarshalPane("EmptyPane", func(d []byte) (Pane, error) {
		return &EmptyPane{}, nil // nothing to unmarshal
	})
}

func (ep *EmptyPane) Activate(renderer.Renderer, platform.Platform, *sim.EventStream, *log.Logger) {}
func (ep *EmptyPane) LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}
func (ep *EmptyPane) ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}
func (ep *EmptyPane) CanTakeKeyboardFocus() bool { return false }
func (ep *EmptyPane) Hide() bool                 { return false }

func (ep *EmptyPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {}

///////////////////////////////////////////////////////////////////////////
// ScrollBar

// ScrollBar provides functionality for a basic scrollbar for use in Pane
// implementations.  (Since those are not handled by imgui, we can't use
// imgui's scrollbar there.)
type ScrollBar struct {
	offset            int
	barWidth          int
	nItems, nVisible  int
	accumDrag         float32
	invert            bool
	vertical          bool
	mouseClickedInBar bool
}

// NewVerticalScrollBar returns a new ScrollBar instance with the given width.
// invert indicates whether the scrolled items are drawn from the bottom
// of the Pane or the top; invert should be true if they are being drawn
// from the bottom.
func NewVerticalScrollBar(width int, invert bool) *ScrollBar {
	return &ScrollBar{barWidth: width, invert: invert, vertical: true}
}

// Update should be called once per frame, providing the total number of things
// being drawn, the number of them that are visible, and the Context passed
// to the Pane's Draw method (so that mouse events can be handled, if appropriate.
func (sb *ScrollBar) Update(nItems int, nVisible int, ctx *Context) {
	sb.nItems = nItems
	sb.nVisible = nVisible

	if sb.nItems > sb.nVisible {
		sign := float32(1)
		if sb.invert {
			sign = -1
		}

		if ctx.Mouse != nil {
			sb.offset += int(sign * ctx.Mouse.Wheel[1])

			if ctx.Mouse.Clicked[0] {
				sb.mouseClickedInBar = util.Select(sb.vertical,
					ctx.Mouse.Pos[0] >= ctx.PaneExtent.Width()-float32(sb.PixelExtent()),
					ctx.Mouse.Pos[1] >= ctx.PaneExtent.Height()-float32(sb.PixelExtent()))

				sb.accumDrag = 0
			}

			if ctx.Mouse.Dragging[0] && sb.mouseClickedInBar {
				axis := util.Select(sb.vertical, 1, 0)
				wh := util.Select(sb.vertical, ctx.PaneExtent.Height(), ctx.PaneExtent.Width())
				sb.accumDrag += -sign * ctx.Mouse.DragDelta[axis] * float32(sb.nItems) / wh
				if math.Abs(sb.accumDrag) >= 1 {
					sb.offset += int(sb.accumDrag)
					sb.accumDrag -= float32(int(sb.accumDrag))
				}
			}
		}
		sb.offset = math.Clamp(sb.offset, 0, sb.nItems-sb.nVisible)
	} else {
		sb.offset = 0
	}
}

// Offset returns the offset into the items at which drawing should start
// (i.e., the items before the offset are offscreen.)  Note that the scroll
// offset is reported in units of the number of items passed to Update;
// thus, if scrolling text, the number of items might be measured in lines
// of text, or it might be measured in scanlines.  The choice determines
// whether scrolling happens at the granularity of entire lines at a time
// or is continuous.
func (sb *ScrollBar) Offset() int {
	return sb.offset
}

// Visible indicates whether the scrollbar will be drawn (it disappears if
// all of the items can fit onscreen.)
func (sb *ScrollBar) Visible() bool {
	return sb.nItems > sb.nVisible
}

// Draw emits the drawing commands for the scrollbar into the provided
// CommandBuffer.
func (sb *ScrollBar) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	if !sb.Visible() {
		return
	}

	// The visible region is [offset,offset+nVisible].
	// Visible region w.r.t. [0,1]
	v0, v1 := float32(sb.offset)/float32(sb.nItems), float32(sb.offset+sb.nVisible)/float32(sb.nItems)
	if sb.invert {
		v0, v1 = 1-v0, 1-v1
	}

	quad := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(quad)

	const edgeSpace = 2
	pw, ph := ctx.PaneExtent.Width(), ctx.PaneExtent.Height()

	if sb.vertical {
		// Visible region in window coordinates
		wy0, wy1 := math.Lerp(v0, ph-edgeSpace, edgeSpace), math.Lerp(v1, ph-edgeSpace, edgeSpace)
		quad.AddQuad([2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy0},
			[2]float32{pw - float32(edgeSpace), wy0},
			[2]float32{pw - float32(edgeSpace), wy1},
			[2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy1}, UIControlColor)
	} else {
		wx0, wx1 := math.Lerp(v0, pw-edgeSpace, edgeSpace), math.Lerp(v1, pw-edgeSpace, edgeSpace)
		quad.AddQuad([2]float32{wx0, ph - float32(sb.barWidth) - float32(edgeSpace)},
			[2]float32{wx0, ph - float32(edgeSpace)},
			[2]float32{wx1, ph - float32(edgeSpace)},
			[2]float32{wx1, ph - float32(sb.barWidth) - float32(edgeSpace)}, UIControlColor)
	}

	quad.GenerateCommands(cb)
}

func (sb *ScrollBar) PixelExtent() int {
	return sb.barWidth + 4 /* for edge space... */
}

// wm.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file contains functionality related to vice's "window manager",
// which is more or less how we're describing the handling of the tiled
// window layout.  Other than the main menu bar, which is handled
// via imgui calls in ui.go, all of the rest of the window is managed here.
// At the top is the status bar and then the rest of the window is
// a kd-tree of Panes, separated by SplitLines.

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mmp/imgui-go/v4"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
)

var (
	// Assorted state related to the window tiling is collected in the wm
	// struct.
	wm struct {
		// Normally the Pane that the mouse is over gets mouse events,
		// though if the user has started a click-drag, then the Pane that
		// received the click keeps getting events until the mouse button
		// is released.  mouseConsumerOverride records such a pane.
		mouseConsumerOverride panes.Pane

		focus WMKeyboardFocus

		lastAircraftResponse string
	}
)

type WMKeyboardFocus struct {
	initial panes.Pane

	// Pane that currently holds the keyboard focus
	current panes.Pane
	// Stack of Panes that previously held focus; if a Pane takes focus
	// temporarily (e.g., the FlightStripPane), then this lets us pop
	// back to the previous one (e.g., the CLIPane.)
	stack []panes.Pane
}

func (f *WMKeyboardFocus) Take(p panes.Pane) {
	f.current = p
	f.stack = nil
}

func (f *WMKeyboardFocus) TakeTemporary(p panes.Pane) {
	if f.current != p {
		f.stack = append(f.stack, f.current)
		f.current = p
	}
}

func (f *WMKeyboardFocus) Release() {
	if n := len(f.stack); n > 0 {
		f.current = f.stack[n-1]
		f.stack = f.stack[:n-1]
	} else {
		f.current = f.initial
	}
}

func (f *WMKeyboardFocus) Current() panes.Pane {
	return f.current
}

///////////////////////////////////////////////////////////////////////////
// SplitLine

type SplitType int

const (
	SplitAxisNone = iota
	SplitAxisX
	SplitAxisY
)

// SplitLine represents a line separating two Panes in the display hierarchy.
// It implements the Pane interface, which simplifies some of the details of
// drawing and interacting with the display hierarchy.
type SplitLine struct {
	// Offset in [0,1] with respect to the parent Pane's bounds.
	Pos  float32
	Axis SplitType
}

func (s *SplitLine) Duplicate(nameAsCopy bool) panes.Pane {
	panic("SplitLine Duplicate shouldn't have been called...")
}

func (s *SplitLine) Activate(*sim.State, renderer.Renderer, platform.Platform,
	*sim.EventStream, *log.Logger) {
}
func (s *SplitLine) Deactivate()                  {}
func (s *SplitLine) Reset(sim.State, *log.Logger) {}
func (s *SplitLine) CanTakeKeyboardFocus() bool   { return false }

func (s *SplitLine) Name() string {
	return "Split Line"
}

func (s *SplitLine) Draw(ctx *panes.Context, cb *renderer.CommandBuffer) {
	if ctx.Mouse != nil {
		if s.Axis == SplitAxisX {
			ctx.Mouse.SetCursor(imgui.MouseCursorResizeEW)
		} else {
			ctx.Mouse.SetCursor(imgui.MouseCursorResizeNS)
		}

		if ctx.Mouse.Dragging[platform.MouseButtonSecondary] {
			delta := ctx.Mouse.DragDelta

			if s.Axis == SplitAxisX {
				s.Pos += delta[0] / ctx.ParentPaneExtent.Width()
			} else {
				s.Pos += delta[1] / ctx.ParentPaneExtent.Height()
			}
			// Just in case
			s.Pos = math.Clamp(s.Pos, .01, .99)
		}
	}

	// The drawing code sets the scissor and viewport to cover just the
	// pixel area of each pane so an easy way to draw a split line is to
	// just issue a clear.
	cb.ClearRGB(panes.UIControlColor)
}

func splitLineWidth(p platform.Platform) int {
	return int(2*p.DPIScale() + 0.5)
}

///////////////////////////////////////////////////////////////////////////
// DisplayNode

// DisplayNode represents a node in the Pane display hierarchy, which is a
// kd-tree.
type DisplayNode struct {
	// non-nil only for leaf nodes: iff splitAxis == SplitAxisNone
	Pane      panes.Pane
	SplitLine SplitLine
	// non-nil only for interior notes: iff splitAxis != SplitAxisNone
	Children [2]*DisplayNode
}

// NodeForPane searches a display node hierarchy for a given Pane,
// returning the associated DisplayNode.
func (d *DisplayNode) NodeForPane(pane panes.Pane) *DisplayNode {
	if d.Pane == pane {
		return d
	}
	if d.Children[0] == nil {
		// We've reached a leaf node without finding it.
		return nil
	}
	d0 := d.Children[0].NodeForPane(pane)
	if d0 != nil {
		return d0
	}
	return d.Children[1].NodeForPane(pane)
}

// ParentNodeForPane returns both the DisplayNode one level up the
// hierarchy from the specified Pane and the index into the children nodes
// for that node that leads to the specified Pane.
func (d *DisplayNode) ParentNodeForPane(pane panes.Pane) (*DisplayNode, int) {
	if d == nil {
		return nil, -1
	}

	if d.Children[0] != nil && d.Children[0].Pane == pane {
		return d, 0
	} else if d.Children[1] != nil && d.Children[1].Pane == pane {
		return d, 1
	}

	if c0, idx := d.Children[0].ParentNodeForPane(pane); c0 != nil {
		return c0, idx
	}
	return d.Children[1].ParentNodeForPane(pane)
}

// TypedDisplayNodePane helps with marshaling to and unmarshaling from
// JSON, which is how the configuration and settings are saved between
// sessions. Most of this works out pretty much for free thanks to go's
// JSON support and ability to automatically inspect and serialize structs.
// The one messy bit is that when we save the DisplayNode hierarchy,
// although the public member variables of Panes are automatically
// serialized, the types of the Panes are not.  Therefore, we instead
// marshal/unmarshal TypedDisplayNodePane instances, which carry along a
// string representation of the associated Pane type stored at a
// DisplayNode.
type TypedDisplayNodePane struct {
	DisplayNode
	Type string
}

// MarshalJSON is called when a DisplayNode is to be marshaled into JSON.
// Here we instead marshal out a TypedDisplayNodePane that also stores
// the Pane's type.
func (d *DisplayNode) MarshalJSON() ([]byte, error) {
	td := TypedDisplayNodePane{DisplayNode: *d}
	if d.Pane != nil {
		td.Type = fmt.Sprintf("%T", d.Pane)
	}
	return json.Marshal(td)
}

// UnmarshalJSON unmarshals text into a DisplayNode; its main task is to
// use the type sting that comes along in the TypedDisplayNodePane to
// determine which Pane type to unmarshal the Pane's member variables into.
func (d *DisplayNode) UnmarshalJSON(s []byte) error {
	var m map[string]*json.RawMessage
	if err := json.Unmarshal(s, &m); err != nil {
		return err
	}

	// First unmarshal the basics.
	var paneType string
	if err := json.Unmarshal(*m["Type"], &paneType); err != nil {
		return err
	}
	if err := json.Unmarshal(*m["SplitLine"], &d.SplitLine); err != nil {
		return err
	}
	if err := json.Unmarshal(*m["Children"], &d.Children); err != nil {
		return err
	}

	// Now create the appropriate Pane type based on the type string.
	if paneType == "" {
		return nil
	}
	pane, err := panes.UnmarshalPane(paneType, *m["Pane"])

	if err == nil {
		d.Pane = pane
	}
	return err
}

// VisitPanes visits all of the Panes in a DisplayNode hierarchy, calling
// the provided callback function for each one.
func (d *DisplayNode) VisitPanes(visit func(panes.Pane)) {
	switch d.SplitLine.Axis {
	case SplitAxisNone:
		visit(d.Pane)
	default:
		d.Children[0].VisitPanes(visit)
		visit(&d.SplitLine)
		d.Children[1].VisitPanes(visit)
	}
}

// VisitPanesWithBounds visits all of the panes in a DisplayNode hierarchy,
// giving each one both its own bounding box in window coordinates as well
// the bounding box of its parent node in the DisplayNodeTree.
func (d *DisplayNode) VisitPanesWithBounds(displayExtent math.Extent2D, parentDisplayExtent math.Extent2D, p platform.Platform,
	visit func(math.Extent2D, math.Extent2D, panes.Pane)) {
	switch d.SplitLine.Axis {
	case SplitAxisNone:
		visit(displayExtent, parentDisplayExtent, d.Pane)
	case SplitAxisX:
		d0, ds, d1 := splitX(displayExtent, d.SplitLine.Pos, splitLineWidth(p))
		d.Children[0].VisitPanesWithBounds(d0, displayExtent, p, visit)
		visit(ds, displayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(d1, displayExtent, p, visit)
	case SplitAxisY:
		d0, ds, d1 := splitY(displayExtent, d.SplitLine.Pos, splitLineWidth(p))
		d.Children[0].VisitPanesWithBounds(d0, displayExtent, p, visit)
		visit(ds, displayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(d1, displayExtent, p, visit)
	}
}

// SplitX returns a new DisplayNode that is the result of splitting the
// provided node horizontally direction at the specified offset (which should
// be between 0 and 1), storing the node as the new node's first child, and
// storing newChild as the's second child.
func (d *DisplayNode) SplitX(x float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		panic(fmt.Sprintf("DisplayNode splitting a non-leaf node: %v", d))
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: x},
		Children: [2]*DisplayNode{d, newChild}}
}

// SplitY returns a new DisplayNode from splitting the provided node
// vertically, analogous to the SplitX method.
func (d *DisplayNode) SplitY(y float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		panic(fmt.Sprintf("DisplayNode splitting a non-leaf node: %v", d))
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: y},
		Children: [2]*DisplayNode{d, newChild}}
}

func splitX(e math.Extent2D, x float32, lineWidth int) (math.Extent2D, math.Extent2D, math.Extent2D) {
	e0 := e
	es := e
	e1 := e
	split := (1-x)*e.P0[0] + x*e.P1[0]
	s0 := split - float32(lineWidth)/2
	s1 := split + float32(lineWidth)/2
	s0, s1 = math.Floor(s0), math.Ceil(s1)
	e0.P1[0] = s0
	es.P0[0] = s0
	es.P1[0] = s1
	e1.P0[0] = s1
	return e0, es, e1
}

func splitY(e math.Extent2D, y float32, lineWidth int) (math.Extent2D, math.Extent2D, math.Extent2D) {
	e0 := e
	es := e
	e1 := e
	split := (1-y)*e.P0[1] + y*e.P1[1]
	s0 := split - float32(lineWidth)/2
	s1 := split + float32(lineWidth)/2
	s0, s1 = math.Floor(s0), math.Ceil(s1)
	e0.P1[1] = s0
	es.P0[1] = s0
	es.P1[1] = s1
	e1.P0[1] = s1
	return e0, es, e1
}

// FindPaneForMouse returns the Pane that the provided mouse position p is inside.
func (d *DisplayNode) FindPaneForMouse(displayExtent math.Extent2D, p [2]float32,
	plat platform.Platform) panes.Pane {
	if !displayExtent.Inside(p) {
		return nil
	}
	if d.SplitLine.Axis == SplitAxisNone {
		// We've reached a leaf node and found the pane.
		return d.Pane
	}

	// Compute the extents of the two nodes and the split line.
	var d0, ds, d1 math.Extent2D
	if d.SplitLine.Axis == SplitAxisX {
		d0, ds, d1 = splitX(displayExtent, d.SplitLine.Pos, splitLineWidth(plat))

		// Round the X extents to integer coordinates, to benefit the split
		// line--since it's relatively small, it's helpful to make it a
		// larger target.
		d0.P1[0] = math.Floor(d0.P1[0])
		ds.P0[0] = math.Floor(ds.P0[0])
		ds.P1[0] = math.Ceil(ds.P1[0])
		d1.P0[0] = math.Ceil(d1.P0[0])

	} else {
		d0, ds, d1 = splitY(displayExtent, d.SplitLine.Pos, splitLineWidth(plat))

		// For a y split, similarly round y bounds up/down to integer
		// coordinates to give the split line a better chance.
		d0.P1[1] = math.Floor(d0.P1[1])
		ds.P0[1] = math.Floor(ds.P0[1])
		ds.P1[1] = math.Ceil(ds.P1[1])
		d1.P0[1] = math.Ceil(d1.P0[1])
	}

	// Now figure out which it is inside.
	if d0.Inside(p) {
		return d.Children[0].FindPaneForMouse(d0, p, plat)
	} else if ds.Inside(p) {
		return &d.SplitLine
	} else if d1.Inside(p) {
		return d.Children[1].FindPaneForMouse(d1, p, plat)
	} else {
		panic("Mouse not overlapping anything?")
		return nil
	}
}

func (d *DisplayNode) String() string {
	return d.getString("")
}

func (d *DisplayNode) getString(indent string) string {
	if d == nil {
		return ""
	}
	s := fmt.Sprintf(indent+"%p split %d pane %p (%T)\n", d, d.SplitLine.Axis, d.Pane, d.Pane)
	s += d.Children[0].getString(indent + "     ")
	s += d.Children[1].getString(indent + "     ")
	return s
}

///////////////////////////////////////////////////////////////////////////

// wmPaneIsPresent checks to see if the specified Pane is present in the
// display hierarchy.
func wmPaneIsPresent(pane panes.Pane, root *DisplayNode) bool {
	found := false
	root.VisitPanes(func(p panes.Pane) {
		if p == pane {
			found = true
		}
	})
	return found
}

// wmDrawPanes is called each time through the main rendering loop; it
// handles all of the details of drawing the Panes in the display
// hierarchy, making sure they don't inadvertently draw over other panes,
// and providing mouse and keyboard events only to the Pane that should
// respectively be receiving them.
func wmDrawPanes(p platform.Platform, r renderer.Renderer, controlClient *sim.ControlClient, stats *Stats, lg *log.Logger) {
	if controlClient == nil {
		commandBuffer := renderer.GetCommandBuffer()
		commandBuffer.ClearRGB(renderer.RGB{})
		stats.render = r.RenderCommandBuffer(commandBuffer)
		renderer.ReturnCommandBuffer(commandBuffer)
		return
	}

	var filter func(d *DisplayNode) *DisplayNode
	filter = func(d *DisplayNode) *DisplayNode {
		if fsp, ok := d.Children[0].Pane.(*panes.FlightStripPane); ok && fsp.HideFlightStrips {
			return filter(d.Children[1])
		} else if fsp, ok := d.Children[1].Pane.(*panes.FlightStripPane); ok && fsp.HideFlightStrips {
			return filter(d.Children[0])
		} else {
			return d
		}
	}
	root := filter(globalConfig.DisplayRoot)

	if wm.focus.Current() == nil || !wmPaneIsPresent(wm.focus.Current(), root) {
		sp := getPaneByType[*panes.STARSPane]()
		if sp == nil {
			panic("No STARSPane?")
		}
		wm.focus = WMKeyboardFocus{initial: sp, current: sp}
	}

	// Useful values related to the display size.
	fbSize := p.FramebufferSize()
	displaySize := p.DisplaySize()

	// Area left for actually drawing Panes
	paneDisplayExtent := math.Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{displaySize[0], displaySize[1] - ui.menuBarHeight}}

	// Get the mouse position from imgui; flip y so that it lines up with
	// our window coordinates.
	mousePos := [2]float32{imgui.MousePos().X, displaySize[1] - 1 - imgui.MousePos().Y}

	// Figure out which Pane the mouse is in.
	mousePane := root.FindPaneForMouse(paneDisplayExtent, mousePos, p)

	io := imgui.CurrentIO()

	// If the user has clicked or is dragging in a Pane, record it in
	// mouseConsumerOverride so that we can continue to dispatch mouse
	// events to that Pane until the mouse button is released, even if the
	// mouse is no longer above it.
	isDragging := imgui.IsMouseDragging(platform.MouseButtonPrimary, 0.) ||
		imgui.IsMouseDragging(platform.MouseButtonSecondary, 0.) ||
		imgui.IsMouseDragging(platform.MouseButtonTertiary, 0.)
	isClicked := imgui.IsMouseClicked(platform.MouseButtonPrimary) ||
		imgui.IsMouseClicked(platform.MouseButtonSecondary) ||
		imgui.IsMouseClicked(platform.MouseButtonTertiary)
	if !io.WantCaptureMouse() && (isDragging || isClicked) && wm.mouseConsumerOverride == nil {
		wm.mouseConsumerOverride = mousePane
	} else if io.WantCaptureMouse() {
		// However, clear the mouse override if imgui wants mouse events
		wm.mouseConsumerOverride = nil
	}

	// Set the default mouse cursor; the pane that owns the mouse may
	// override this..
	imgui.SetMouseCursor(imgui.MouseCursorArrow)

	// All of the Panes' draw commands will be added to commandBuffer.
	commandBuffer := renderer.GetCommandBuffer()
	defer renderer.ReturnCommandBuffer(commandBuffer)

	// Now traverse all of the Panes...
	// First clear the entire window to the background color.
	commandBuffer.ClearRGB(renderer.RGB{})

	// Handle tabbing between STARS/Messages pane
	var keyboard *platform.KeyboardState
	if !imgui.CurrentIO().WantCaptureKeyboard() {
		keyboard = p.GetKeyboard()
	}

	if keyboard != nil && keyboard.IsPressed(platform.KeyTab) {
		cur := wm.focus.Current()
		if _, ok := cur.(*panes.MessagesPane); ok {
			if s := getPaneByType[*panes.STARSPane](); s != nil {
				wm.focus.Take(s)
			}
		} else if _, ok := cur.(*panes.STARSPane); ok {
			if m := getPaneByType[*panes.MessagesPane](); m != nil {
				wm.focus.Take(m)
			}
		}
	}

	// Actually visit the panes.
	root.VisitPanesWithBounds(paneDisplayExtent, paneDisplayExtent, p,
		func(paneExtent math.Extent2D, parentExtent math.Extent2D, pane panes.Pane) {
			haveFocus := pane == wm.focus.Current() && !imgui.CurrentIO().WantCaptureKeyboard()
			ctx := panes.Context{
				PaneExtent:       paneExtent,
				ParentPaneExtent: parentExtent,
				Platform:         p,
				Renderer:         r,
				Keyboard:         keyboard,
				HaveFocus:        haveFocus,
				Now:              time.Now(),
				Lg:               lg,
				MenuBarHeight:    ui.menuBarHeight,
				AudioEnabled:     &globalConfig.AudioEnabled,
				KeyboardFocus:    &wm.focus,
				ControlClient:    controlClient,
			}

			// Similarly make the mouse events available only to the
			// one Pane that should see them.
			ownsMouse := wm.mouseConsumerOverride == pane ||
				(wm.mouseConsumerOverride == nil &&
					!io.WantCaptureMouse() &&
					paneExtent.Inside(mousePos))
			if ownsMouse {
				// Full display size, including the menu and status bar.
				displayTrueFull := math.Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{displaySize[0], displaySize[1]}}
				ctx.InitializeMouse(displayTrueFull, p)
			}

			// Specify the scissor rectangle and viewport that
			// correspond to the pixels that the Pane covers. In this
			// way, not only can the Pane be implemented in terms of
			// Pane coordinates, independent of where it is actually
			// placed in the overall window, but this also ensures that
			// the Pane can't inadvertently draw over other Panes.
			commandBuffer.SetDrawBounds(paneExtent, p.FramebufferSize()[1]/p.DisplaySize()[1])

			// Let the Pane do its thing
			pane.Draw(&ctx, commandBuffer)

			// And reset the graphics state to the standard baseline,
			// so no state changes leak and affect subsequent drawing.
			commandBuffer.ResetState()
		})

	// Clear mouseConsumerOverride if the user has stopped dragging;
	// only do this after visiting the Panes so that the override Pane
	// still sees the mouse button release event.
	if !isDragging && !isClicked {
		wm.mouseConsumerOverride = nil
	}

	// fbSize will be (0,0) if the window is minimized, in which case we
	// can skip rendering. It's still important to do all of the pane
	// traversal, etc., though, so that events are still consumed and
	// memory use doesn't grow.
	if fbSize[0] > 0 && fbSize[1] > 0 {
		stats.render = r.RenderCommandBuffer(commandBuffer)
	}
}

func getPaneByType[T any]() T {
	var t T
	globalConfig.DisplayRoot.VisitPanes(func(pane panes.Pane) {
		if p, ok := pane.(T); ok {
			t = p
		}
	})
	return t
}

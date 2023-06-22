// wm.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
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
	"strings"

	"github.com/mmp/imgui-go/v4"
)

var (
	// Assorted state related to the window tiling is collected in the wm
	// struct.
	wm struct {
		// When a Pane's entry in the "Subwindows" menu is selected, these
		// two maps are populated to indicate that the Pane's configuration
		// window should be shown.
		showPaneSettings map[Pane]*bool
		showPaneName     map[Pane]string

		// Normally the Pane that the mouse is over gets mouse events,
		// though if the user has started a click-drag, then the Pane that
		// received the click keeps getting events until the mouse button
		// is released.  mouseConsumerOverride records such a pane.
		mouseConsumerOverride Pane
		// Pane that currently holds the keyboard focus
		keyboardFocusPane Pane
		// Stack of Panes that previously held focus; if a Pane takes focus
		// temporarily (e.g., the FlightStripPane), then this lets us pop
		// back to the previous one (e.g., the CLIPane.)
		keyboardFocusStack []Pane

		lastAircraftResponse string
		events               *EventsSubscription
	}
)

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

func (s *SplitLine) Duplicate(nameAsCopy bool) Pane {
	lg.Errorf("This actually should never be called...")
	return &SplitLine{}
}

func (s *SplitLine) Activate(*World)            {}
func (s *SplitLine) Deactivate()                {}
func (s *SplitLine) CanTakeKeyboardFocus() bool { return false }

func (s *SplitLine) Name() string {
	return "Split Line"
}

func (s *SplitLine) Draw(ctx *PaneContext, cb *CommandBuffer) {
	if ctx.mouse != nil && ctx.mouse.Dragging[MouseButtonSecondary] {
		delta := ctx.mouse.DragDelta

		if s.Axis == SplitAxisX {
			s.Pos += delta[0] / ctx.parentPaneExtent.Width()
		} else {
			s.Pos += delta[1] / ctx.parentPaneExtent.Height()
		}
		// Just in case
		s.Pos = clamp(s.Pos, .01, .99)
	}

	// The drawing code sets the scissor and viewport to cover just the
	// pixel area of each pane so an easy way to draw a split line is to
	// just issue a clear.
	cb.ClearRGB(UIControlColor)
}

func splitLineWidth() int {
	return int(2*dpiScale(platform) + 0.5)
}

///////////////////////////////////////////////////////////////////////////
// DisplayNode

// DisplayNode represents a node in the Pane display hierarchy, which is a
// kd-tree.
type DisplayNode struct {
	// non-nil only for leaf nodes: iff splitAxis == SplitAxisNone
	Pane      Pane
	SplitLine SplitLine
	// non-nil only for interior notes: iff splitAxis != SplitAxisNone
	Children [2]*DisplayNode
}

// NodeForPane searches a display node hierarchy for a given Pane,
// returning the associated DisplayNode.
func (d *DisplayNode) NodeForPane(pane Pane) *DisplayNode {
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
func (d *DisplayNode) ParentNodeForPane(pane Pane) (*DisplayNode, int) {
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
	pane, err := unmarshalPane(paneType, *m["Pane"])

	if err == nil {
		d.Pane = pane
	}
	return err
}

// VisitPanes visits all of the Panes in a DisplayNode hierarchy, calling
// the provided callback function for each one.
func (d *DisplayNode) VisitPanes(visit func(Pane)) {
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
func (d *DisplayNode) VisitPanesWithBounds(displayExtent Extent2D, parentDisplayExtent Extent2D,
	visit func(Extent2D, Extent2D, Pane)) {
	switch d.SplitLine.Axis {
	case SplitAxisNone:
		visit(displayExtent, parentDisplayExtent, d.Pane)
	case SplitAxisX:
		d0, ds, d1 := splitX(displayExtent, d.SplitLine.Pos, splitLineWidth())
		d.Children[0].VisitPanesWithBounds(d0, displayExtent, visit)
		visit(ds, displayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(d1, displayExtent, visit)
	case SplitAxisY:
		d0, ds, d1 := splitY(displayExtent, d.SplitLine.Pos, splitLineWidth())
		d.Children[0].VisitPanesWithBounds(d0, displayExtent, visit)
		visit(ds, displayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(d1, displayExtent, visit)
	}
}

// SplitX returns a new DisplayNode that is the result of splitting the
// provided node horizontally direction at the specified offset (which should
// be between 0 and 1), storing the node as the new node's first child, and
// storing newChild as the's second child.
func (d *DisplayNode) SplitX(x float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		lg.Errorf("splitting a non-leaf node: %v", d)
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: x},
		Children: [2]*DisplayNode{d, newChild}}
}

// SplitY returns a new DisplayNode from splitting the provided node
// vertically, analogous to the SplitX method.
func (d *DisplayNode) SplitY(y float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		lg.Errorf("splitting a non-leaf node: %v", d)
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: y},
		Children: [2]*DisplayNode{d, newChild}}
}

func splitX(e Extent2D, x float32, lineWidth int) (Extent2D, Extent2D, Extent2D) {
	e0 := e
	es := e
	e1 := e
	split := (1-x)*e.p0[0] + x*e.p1[0]
	s0 := split - float32(lineWidth)/2
	s1 := split + float32(lineWidth)/2
	s0, s1 = floor(s0), ceil(s1)
	e0.p1[0] = s0
	es.p0[0] = s0
	es.p1[0] = s1
	e1.p0[0] = s1
	return e0, es, e1
}

func splitY(e Extent2D, y float32, lineWidth int) (Extent2D, Extent2D, Extent2D) {
	e0 := e
	es := e
	e1 := e
	split := (1-y)*e.p0[1] + y*e.p1[1]
	s0 := split - float32(lineWidth)/2
	s1 := split + float32(lineWidth)/2
	s0, s1 = floor(s0), ceil(s1)
	e0.p1[1] = s0
	es.p0[1] = s0
	es.p1[1] = s1
	e1.p0[1] = s1
	return e0, es, e1
}

// FindPaneForMouse returns the Pane that the provided mouse position p is inside.
func (d *DisplayNode) FindPaneForMouse(displayExtent Extent2D, p [2]float32) Pane {
	if !displayExtent.Inside(p) {
		return nil
	}
	if d.SplitLine.Axis == SplitAxisNone {
		// We've reached a leaf node and found the pane.
		return d.Pane
	}

	// Compute the extents of the two nodes and the split line.
	var d0, ds, d1 Extent2D
	if d.SplitLine.Axis == SplitAxisX {
		d0, ds, d1 = splitX(displayExtent, d.SplitLine.Pos, splitLineWidth())

		// Round the X extents to integer coordinates, to benefit the split
		// line--since it's relatively small, it's helpful to make it a
		// larger target.
		d0.p1[0] = floor(d0.p1[0])
		ds.p0[0] = floor(ds.p0[0])
		ds.p1[0] = ceil(ds.p1[0])
		d1.p0[0] = ceil(d1.p0[0])

	} else {
		d0, ds, d1 = splitY(displayExtent, d.SplitLine.Pos, splitLineWidth())

		// For a y split, similarly round y bounds up/down to integer
		// coordinates to give the split line a better chance.
		d0.p1[1] = floor(d0.p1[1])
		ds.p0[1] = floor(ds.p0[1])
		ds.p1[1] = ceil(ds.p1[1])
		d1.p0[1] = ceil(d1.p0[1])
	}

	// Now figure out which it is inside.
	if d0.Inside(p) {
		return d.Children[0].FindPaneForMouse(d0, p)
	} else if ds.Inside(p) {
		return &d.SplitLine
	} else if d1.Inside(p) {
		return d.Children[1].FindPaneForMouse(d1, p)
	} else {
		lg.Errorf("Mouse not overlapping anything?")
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

// wmInit handles general initialization for the window (pane) management
// system.
func wmInit(w *World) {
	wm.showPaneSettings = make(map[Pane]*bool)
	wm.showPaneName = make(map[Pane]string)
	wm.events = w.SubscribeEvents()
}

// wmAddPaneMenuSettings is called to populate the top-level "Subwindows"
// menu.
// wmDrawUI draws any open Pane settings windows.
func wmDrawUI(p Platform) {
	globalConfig.DisplayRoot.VisitPanes(func(pane Pane) {
		if show, ok := wm.showPaneSettings[pane]; ok && *show {
			if uid, ok := pane.(PaneUIDrawer); ok {
				imgui.BeginV(wm.showPaneName[pane]+" settings", show, imgui.WindowFlagsAlwaysAutoResize)
				uid.DrawUI()
				imgui.End()
			}
		}
	})
}

// wmTakeKeyboardFocus allows a Pane to take the keyboard
// focus. isTransient can be used to indicate that the focus will later be
// given up, at which point the previously-focused Pane should get the
// keyboard focus back.
func wmTakeKeyboardFocus(pane Pane, isTransient bool) {
	if wm.keyboardFocusPane == pane {
		return
	}
	if isTransient && wm.keyboardFocusPane != nil {
		wm.keyboardFocusStack = append(wm.keyboardFocusStack, wm.keyboardFocusPane)
	}
	if !isTransient {
		// We can discard anything in the stack if this pane is not
		// planning on giving it back.
		wm.keyboardFocusStack = nil
	}
	wm.keyboardFocusPane = pane
}

// wmReleaseKeyboardFocus allows a Pane to give up the keyboard focus; it
// is returned to the last item on the stack.
func wmReleaseKeyboardFocus() {
	if n := len(wm.keyboardFocusStack); n > 0 {
		wm.keyboardFocusPane = wm.keyboardFocusStack[n-1]
		wm.keyboardFocusStack = wm.keyboardFocusStack[:n-1]
	}
}

// wmPaneIsPresent checks to see if the specified Pane is present in the
// display hierarchy.
func wmPaneIsPresent(pane Pane) bool {
	found := false
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
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
func wmDrawPanes(p Platform, r Renderer) {
	if !wmPaneIsPresent(wm.keyboardFocusPane) {
		// It was deleted in the config editor or a new config was loaded.
		wm.keyboardFocusPane = nil
	}
	if wm.keyboardFocusPane == nil {
		// Take any one that can take keyboard events.
		if wm.keyboardFocusPane == nil {
			globalConfig.DisplayRoot.VisitPanes(func(pane Pane) {
				if pane.CanTakeKeyboardFocus() {
					wm.keyboardFocusPane = pane
				}
			})
		}
	}

	// Useful values related to the display size.
	fbSize := p.FramebufferSize()
	displaySize := p.DisplaySize()
	highDPIScale := fbSize[1] / displaySize[1]

	topItemsHeight := ui.menuBarHeight + wmStatusBarHeight()

	// Area left for actually drawing Panes
	paneDisplayExtent := Extent2D{p0: [2]float32{0, 0}, p1: [2]float32{displaySize[0], displaySize[1] - topItemsHeight}}

	// Get the mouse position from imgui; flip y so that it lines up with
	// our window coordinates.
	mousePos := [2]float32{imgui.MousePos().X, displaySize[1] - 1 - imgui.MousePos().Y}

	// Figure out which Pane the mouse is in.
	mousePane := globalConfig.DisplayRoot.FindPaneForMouse(paneDisplayExtent, mousePos)

	io := imgui.CurrentIO()

	// If the user has clicked or is dragging in a Pane, record it in
	// mouseConsumerOverride so that we can continue to dispatch mouse
	// events to that Pane until the mouse button is released, even if the
	// mouse is no longer above it.
	isDragging := imgui.IsMouseDragging(MouseButtonPrimary, 0.) ||
		imgui.IsMouseDragging(MouseButtonSecondary, 0.) ||
		imgui.IsMouseDragging(MouseButtonTertiary, 0.)
	isClicked := imgui.IsMouseClicked(MouseButtonPrimary) ||
		imgui.IsMouseClicked(MouseButtonSecondary) ||
		imgui.IsMouseClicked(MouseButtonTertiary)
	if !io.WantCaptureMouse() && (isDragging || isClicked) && wm.mouseConsumerOverride == nil {
		wm.mouseConsumerOverride = mousePane
	} else if io.WantCaptureMouse() {
		// However, clear the mouse override if imgui wants mouse events
		wm.mouseConsumerOverride = nil
	}

	// Set the mouse cursor depending on what the mouse is hovering over.
	setCursorForPane := func(p Pane) {
		if sl, ok := p.(*SplitLine); ok {
			// For split lines, the cursor changes to indicate what a
			// click-and-drag will do..
			if sl.Axis == SplitAxisX {
				imgui.SetMouseCursor(imgui.MouseCursorResizeEW)
			} else {
				imgui.SetMouseCursor(imgui.MouseCursorResizeNS)
			}
		} else {
			imgui.SetMouseCursor(imgui.MouseCursorArrow) // just to be sure; it may be this already
		}
	}
	if wm.mouseConsumerOverride != nil {
		setCursorForPane(wm.mouseConsumerOverride)
	} else {
		setCursorForPane(mousePane)
	}

	// All of the Panes' draw commands will be added to commandBuffer.
	commandBuffer := GetCommandBuffer()
	defer ReturnCommandBuffer(commandBuffer)

	// Now traverse all of the Panes...
	// First clear the entire window to the background color.
	commandBuffer.ClearRGB(RGB{})

	// Draw the status bar underneath the menu bar
	wmDrawStatusBar(fbSize, displaySize, commandBuffer, p)

	// By default we'll visit the tree starting at
	// DisplayRoot. However, if a Pane has been maximized to cover the
	// whole screen, we will instead start with it.
	root := globalConfig.DisplayRoot

	// Actually visit the panes.
	var keyboard *KeyboardState
	if !imgui.CurrentIO().WantCaptureKeyboard() {
		keyboard = NewKeyboardState(p)
	}
	root.VisitPanesWithBounds(paneDisplayExtent, paneDisplayExtent,
		func(paneExtent Extent2D, parentExtent Extent2D, pane Pane) {
			haveFocus := pane == wm.keyboardFocusPane && !imgui.CurrentIO().WantCaptureKeyboard()
			ctx := PaneContext{
				paneExtent:       paneExtent,
				parentPaneExtent: parentExtent,
				platform:         p,
				renderer:         r,
				keyboard:         keyboard,
				haveFocus:        haveFocus}

			// Similarly make the mouse events available only to the
			// one Pane that should see them.
			ownsMouse := wm.mouseConsumerOverride == pane ||
				(wm.mouseConsumerOverride == nil &&
					!io.WantCaptureMouse() &&
					paneExtent.Inside(mousePos))
			if ownsMouse {
				// Full display size, including the menu and status bar.
				displayTrueFull := Extent2D{p0: [2]float32{0, 0}, p1: [2]float32{displaySize[0], displaySize[1]}}
				ctx.InitializeMouse(displayTrueFull)
			}

			// Specify the scissor rectangle and viewport that
			// correspond to the pixels that the Pane covers. In this
			// way, not only can the Pane be implemented in terms of
			// Pane coordinates, independent of where it is actually
			// placed in the overall window, but this also ensures that
			// the Pane can't inadvertently draw over other Panes.
			//
			// One messy detail here is that these windows are
			// specified in framebuffer coordinates, not display
			// coordinates, so they must be scaled by the DPI scale for
			// e.g., retina displays.
			x0, y0 := int(highDPIScale*paneExtent.p0[0]), int(highDPIScale*paneExtent.p0[1])
			w, h := int(highDPIScale*paneExtent.Width()), int(highDPIScale*paneExtent.Height())
			commandBuffer.Scissor(x0, y0, w, h)
			commandBuffer.Viewport(x0, y0, w, h)

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

// wmDrawStatus bar draws the status bar underneath the main menu bar
func wmDrawStatusBar(fbSize [2]float32, displaySize [2]float32, cb *CommandBuffer, platform Platform) {
	var texts []string
	textCallsign := ""
	for _, event := range wm.events.Get() {
		if event.Type != RadioTransmissionEvent {
			continue
		}

		// Split the callsign into the ICAO and the flight number
		// Note: this is buggy if we process multiple senders in a
		// single call here, but that shouldn't happen...
		idx := strings.IndexAny(event.Callsign, "0123456789")
		if idx == -1 {
			textCallsign = event.Callsign
		} else {
			// Try to get the telephony.
			icao, flight := event.Callsign[:idx], event.Callsign[idx:]
			if cs, ok := database.Callsigns[icao]; ok {
				textCallsign = cs.Telephony + " " + flight
				if ac := world.GetAircraft(event.Callsign); ac != nil {
					if fp := ac.FlightPlan; fp != nil {
						if strings.HasPrefix(fp.AircraftType, "H/") {
							textCallsign += " heavy"
						} else if strings.HasPrefix(fp.AircraftType, "J/") || strings.HasPrefix(fp.AircraftType, "S/") {
							textCallsign += " super"
						}
					}
				}
			} else {
				textCallsign = event.Callsign
			}
		}

		texts = append(texts, event.Message)
	}
	if texts != nil {
		wm.lastAircraftResponse = strings.Join(texts, ", ") + ", " + textCallsign
	}

	if wm.lastAircraftResponse == "" {
		return
	}

	top := displaySize[1] - ui.menuBarHeight
	bottom := displaySize[1] - ui.menuBarHeight - wmStatusBarHeight()
	statusBarDisplayExtent := Extent2D{p0: [2]float32{0, bottom}, p1: [2]float32{displaySize[0], top}}
	statusBarFbExtent := statusBarDisplayExtent.Scale(dpiScale(platform))

	cb.Scissor(int(statusBarFbExtent.p0[0]), int(statusBarFbExtent.p0[1]),
		int(statusBarFbExtent.Width()+.5), int(statusBarFbExtent.Height()+.5))
	cb.Viewport(int(statusBarFbExtent.p0[0]), int(statusBarFbExtent.p0[1]),
		int(statusBarFbExtent.Width()+.5), int(statusBarFbExtent.Height()+.5))

	statusBarHeight := wmStatusBarHeight()
	cb.LoadProjectionMatrix(Identity3x3().Ortho(0, displaySize[0], 0, statusBarHeight))
	cb.LoadModelViewMatrix(Identity3x3())

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	textp := [2]float32{15, float32(5 + ui.font.size)}
	style := TextStyle{Font: ui.font, Color: UITextColor}
	td.AddText(wm.lastAircraftResponse, textp, style)

	// Finally, add the text drawing commands to the graphics command buffer.
	cb.ResetState()
	td.GenerateCommands(cb)

	cb.ResetState()
}

func wmStatusBarHeight() float32 {
	return float32(10 + ui.font.size)
}

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
	"sort"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/mmp/imgui-go/v4"
)

var (
	// Assorted state related to the window tiling is collected in the wm
	// struct.
	wm struct {
		// Variables related to the config editors.
		showConfigEditor bool
		// When two Panes are to be selected (e.g., to exchange them), this
		// records the first one.
		paneFirstPick Pane
		// Callback function for when a pane is picked during config
		// editing.
		handlePanePick     func(Pane) bool
		paneCreatePrompt   string
		paneConfigHelpText string
		// Backup copy of the Pane node hierarchy for use when the user
		// clicks the discard button.
		editorBackupRoot *DisplayNode
		// Buttons that are displayed during config editing.
		configButtons ModalButtonSet

		// When a Pane's entry in the "Subwindows" menu is selected, these
		// two maps are populated to indicate that the Pane's configuration
		// window should be shown.
		showPaneSettings map[Pane]*bool
		showPaneName     map[Pane]string

		// If a single Pane is to be displayed (via control-f), this is a
		// *DisplayNode for it.
		fullScreenDisplayNode *DisplayNode

		configEditorHeight float32 // in pixels; zero if the config editor is not active

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

func (s *SplitLine) Activate(cs *ColorScheme)   {}
func (s *SplitLine) Deactivate()                {}
func (s *SplitLine) CanTakeKeyboardFocus() bool { return false }

func (s *SplitLine) Name() string {
	return "Split Line"
}

func (s *SplitLine) Draw(ctx *PaneContext, cb *CommandBuffer) {
	if ctx.mouse != nil && ctx.mouse.dragging[mouseButtonSecondary] {
		delta := ctx.mouse.dragDelta

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
	cb.ClearRGB(ctx.cs.UIControl)
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

// Duplicate makes a deep copy of a display node hierarchy.
func (d *DisplayNode) Duplicate() *DisplayNode {
	dupe := &DisplayNode{}

	if d.Pane != nil {
		dupe.Pane = d.Pane.Duplicate(false)
	}
	dupe.SplitLine = d.SplitLine

	if d.SplitLine.Axis != SplitAxisNone {
		dupe.Children[0] = d.Children[0].Duplicate()
		dupe.Children[1] = d.Children[1].Duplicate()
	}
	return dupe
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

	// Now create the appropriate Pane type based on the type string and
	// then unmarshal its member variables.
	switch paneType {
	case "":
		// nil pane

	case "*main.AirportInfoPane":
		var aip AirportInfoPane
		if err := json.Unmarshal(*m["Pane"], &aip); err != nil {
			return err
		}
		d.Pane = &aip

	case "*main.CLIPane":
		var clip CLIPane
		if err := json.Unmarshal(*m["Pane"], &clip); err != nil {
			return err
		}
		d.Pane = &clip

	case "*main.EmptyPane":
		var ep EmptyPane
		if err := json.Unmarshal(*m["Pane"], &ep); err != nil {
			return err
		}
		d.Pane = &ep

	case "*main.FlightPlanPane":
		var fp FlightPlanPane
		if err := json.Unmarshal(*m["Pane"], &fp); err != nil {
			return err
		}
		d.Pane = &fp

	case "*main.FlightStripPane":
		var fs FlightStripPane
		if err := json.Unmarshal(*m["Pane"], &fs); err != nil {
			return err
		}
		d.Pane = &fs

	case "*main.NotesViewPane":
		var nv NotesViewPane
		if err := json.Unmarshal(*m["Pane"], &nv); err != nil {
			return err
		}
		d.Pane = &nv

	case "*main.PerformancePane":
		var pp PerformancePane
		if err := json.Unmarshal(*m["Pane"], &pp); err != nil {
			return err
		}
		d.Pane = &pp

	case "*main.RadarScopePane":
		var rsp RadarScopePane
		if err := json.Unmarshal(*m["Pane"], &rsp); err != nil {
			return err
		}
		d.Pane = &rsp

	case "*main.ReminderPane":
		var rp ReminderPane
		if err := json.Unmarshal(*m["Pane"], &rp); err != nil {
			return err
		}
		d.Pane = &rp

	default:
		lg.Errorf("%s: Unhandled type in config file", paneType)
		d.Pane = NewEmptyPane() // don't crash at least
	}

	return nil
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
		d0, ds, d1 := displayExtent.SplitX(d.SplitLine.Pos, splitLineWidth(), true)
		d.Children[0].VisitPanesWithBounds(d0, displayExtent, visit)
		visit(ds, displayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(d1, displayExtent, visit)
	case SplitAxisY:
		d0, ds, d1 := displayExtent.SplitY(d.SplitLine.Pos, splitLineWidth(), true)
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
		d0, ds, d1 = displayExtent.SplitX(d.SplitLine.Pos, splitLineWidth(), true)

		// Round the X extents to integer coordinates, to benefit the split
		// line--since it's relatively small, it's helpful to make it a
		// larger target.
		d0.p1[0] = floor(d0.p1[0])
		ds.p0[0] = floor(ds.p0[0])
		ds.p1[0] = ceil(ds.p1[0])
		d1.p0[0] = ceil(d1.p0[0])

	} else {
		d0, ds, d1 = displayExtent.SplitY(d.SplitLine.Pos, splitLineWidth(), true)

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
func wmInit() {
	lg.Printf("Starting wm initialization")

	// All that this function currently does is initialize the buttons for use in the config editor.
	wm.configButtons.Add("Copy", func() func(pane Pane) bool {
		wm.paneConfigHelpText = "Select window to copy"
		return func(pane Pane) bool {
			if wm.paneFirstPick == nil {
				wm.paneFirstPick = pane
				wm.paneConfigHelpText = "Select destination for copy"
				return false
			} else {
				node := positionConfig.DisplayRoot.NodeForPane(pane)
				lg.Printf("about to copy %p %+T to node %v.\ntree: %s", pane, pane, node,
					positionConfig.DisplayRoot.String())
				node.Pane = wm.paneFirstPick.Duplicate(true)
				wm.paneFirstPick = nil
				wm.paneConfigHelpText = ""
				lg.Printf("new tree:\n%s",
					positionConfig.DisplayRoot.String())
				return true
			}
		}
	}, func() bool { return positionConfig.DisplayRoot.Children[0] != nil })

	wm.configButtons.Add("Exchange",
		func() func(pane Pane) bool {
			wm.paneConfigHelpText = "Select first window for exchange"

			return func(pane Pane) bool {
				if wm.paneFirstPick == nil {
					wm.paneFirstPick = pane
					wm.paneConfigHelpText = "Select second window for exchange"
					return false
				} else {
					n0 := positionConfig.DisplayRoot.NodeForPane(wm.paneFirstPick)
					n1 := positionConfig.DisplayRoot.NodeForPane(pane)
					lg.Printf("about echange nodes %p %+v %p %+v.\ntree: %s", n0, n0, n1, n1,
						positionConfig.DisplayRoot.String())
					if pane != wm.paneFirstPick {
						n0.Pane, n1.Pane = n1.Pane, n0.Pane
					}
					wm.paneFirstPick = nil
					wm.paneConfigHelpText = ""
					lg.Printf("new tree:\n%s", positionConfig.DisplayRoot.String())
					return true
				}
			}
		}, func() bool { return positionConfig.DisplayRoot.Children[0] != nil })

	handleSplitPick := func(axis SplitType) func() func(pane Pane) bool {
		return func() func(pane Pane) bool {
			wm.paneConfigHelpText = "Select window to split"
			return func(pane Pane) bool {
				lg.Printf("about to split %p %+T.\ntree: %s", pane, pane,
					positionConfig.DisplayRoot.String())
				node := positionConfig.DisplayRoot.NodeForPane(pane)
				node.Children[0] = &DisplayNode{Pane: &EmptyPane{}}
				node.Children[1] = &DisplayNode{Pane: pane}
				node.Pane = nil
				node.SplitLine.Pos = 0.5
				node.SplitLine.Axis = axis
				wm.paneConfigHelpText = ""
				lg.Printf("new tree:\n%s", positionConfig.DisplayRoot.String())
				return true
			}
		}
	}
	wm.configButtons.Add("Split Horizontally", handleSplitPick(SplitAxisX),
		func() bool { return true })
	wm.configButtons.Add("Split Vertically", handleSplitPick(SplitAxisY),
		func() bool { return true })
	wm.configButtons.Add("Delete", func() func(pane Pane) bool {
		wm.paneConfigHelpText = "Select window to delete"
		return func(pane Pane) bool {
			lg.Printf("about to delete %p %+T.\ntree: %s", pane, pane,
				positionConfig.DisplayRoot.String())
			node, idx := positionConfig.DisplayRoot.ParentNodeForPane(pane)
			other := idx ^ 1
			*node = *node.Children[other]
			wm.paneConfigHelpText = ""
			lg.Printf("new tree:\n%s", positionConfig.DisplayRoot.String())
			return true
		}
	}, func() bool { return positionConfig.DisplayRoot.Children[0] != nil })

	lg.Printf("Finished wm initialization")
}

// wmAddPaneMenuSettings is called to populate the top-level "Subwindows"
// menu.
func wmAddPaneMenuSettings() {
	// Each Pane that implements the PaneUIDrawer interface gets an entry
	// in the "Subwindows" menu in the main menu bar. First collect those.
	var panes []Pane
	positionConfig.DisplayRoot.VisitPanes(func(pane Pane) {
		if _, ok := pane.(PaneUIDrawer); ok {
			panes = append(panes, pane)
		}
	})

	// Sort them by name.
	sort.Slice(panes, func(i, j int) bool { return panes[i].Name() < panes[j].Name() })

	for i, pane := range panes {
		// It's possible that multiple panes may have the same name;
		// disambiguate their imgui tags via ## just in case.
		if imgui.MenuItem(pane.Name() + "...##" + fmt.Sprintf("%d", i)) {
			// copy the name so that it can be edited...
			wm.showPaneName[pane] = pane.Name()
			// Allocate a new bool that indicates whether the window for
			// the pane is displayed.
			t := true
			wm.showPaneSettings[pane] = &t
		}
	}
}

func wmDrawConfigEditor(p Platform) {
	imgui.PushFont(ui.font.ifont)

	var flags imgui.WindowFlags
	flags = imgui.WindowFlagsNoDecoration
	flags |= imgui.WindowFlagsNoSavedSettings
	flags |= imgui.WindowFlagsNoNav
	flags |= imgui.WindowFlagsNoResize

	displaySize := p.DisplaySize()
	y := ui.menuBarHeight + wmStatusBarHeight()
	imgui.SetNextWindowPosV(imgui.Vec2{X: 0, Y: y}, imgui.ConditionAlways, imgui.Vec2{})
	imgui.SetNextWindowSize(imgui.Vec2{displaySize[0], 60})
	imgui.BeginV("Config editor", nil, flags)

	cs := positionConfig.GetColorScheme()
	imgui.PushStyleColor(imgui.StyleColorText, cs.Text.imgui())

	setPicked := func(newPane Pane) func(pane Pane) bool {
		return func(pane Pane) bool {
			node := positionConfig.DisplayRoot.NodeForPane(pane)
			node.Pane = newPane
			wm.paneCreatePrompt = ""
			wm.paneConfigHelpText = ""
			return true
		}
	}
	imgui.SetNextItemWidth(imgui.WindowWidth() * .2)
	prompt := wm.paneCreatePrompt
	if prompt == "" {
		prompt = "Create New..."
	}
	if imgui.BeginComboV("##Set...", prompt, imgui.ComboFlagsHeightLarge) {
		if imgui.Selectable("Airport information") {
			wm.paneCreatePrompt = "Airport information"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewAirportInfoPane())
		}
		if imgui.Selectable("Command-line interface") {
			wm.paneCreatePrompt = "Command-line interface"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewCLIPane())
		}
		if imgui.Selectable("Empty") {
			wm.paneCreatePrompt = "Empty"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewEmptyPane())
		}
		if imgui.Selectable("Flight plan") {
			wm.paneCreatePrompt = "Flight plan"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewFlightPlanPane())
		}
		if imgui.Selectable("Flight strip") {
			wm.paneCreatePrompt = "Flight strip"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewFlightStripPane())
		}
		if imgui.Selectable("Notes Viewer") {
			wm.paneCreatePrompt = "Notes viewer"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewNotesViewPane())
		}
		if imgui.Selectable("Performance statistics") {
			wm.paneCreatePrompt = "Performance statistics"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewPerformancePane())
		}
		if imgui.Selectable("Radar Scope (Generic)") {
			wm.paneCreatePrompt = "Radar scope (Generic)"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewRadarScopePane("(Unnamed)"))
		}
		if imgui.Selectable("Reminders") {
			wm.paneCreatePrompt = "Reminders"
			wm.paneConfigHelpText = "Select location for new " + wm.paneCreatePrompt + " window"
			wm.handlePanePick = setPicked(NewReminderPane())
		}
		imgui.EndCombo()
	}

	imgui.SameLine()

	wm.configButtons.Draw()

	if wm.handlePanePick != nil {
		imgui.SameLine()
		if imgui.Button("Cancel") {
			wm.handlePanePick = nil
			wm.paneFirstPick = nil
			wm.paneConfigHelpText = ""
			wm.configButtons.Clear()
		}
	}

	imgui.SameLine()
	imgui.SetCursorPos(imgui.Vec2{platform.DisplaySize()[0] - float32(110), imgui.CursorPosY()})
	if imgui.Button("Save") {
		wm.showConfigEditor = false
		wm.paneConfigHelpText = ""
		wm.editorBackupRoot = nil
	}
	imgui.SameLine()
	if imgui.Button("Revert") {
		positionConfig.DisplayRoot = wm.editorBackupRoot
		wm.showConfigEditor = false
		wm.paneConfigHelpText = ""
		wm.editorBackupRoot = nil
	}

	imgui.Text(wm.paneConfigHelpText)

	imgui.PopStyleColor()
	imgui.End()

	imgui.PopFont()
}

// wmDrawUI draws any open Pane settings windows.
func wmDrawUI(p Platform) {
	positionConfig.DisplayRoot.VisitPanes(func(pane Pane) {
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
	positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
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
func wmDrawPanes(platform Platform, renderer Renderer) {
	if !wmPaneIsPresent(wm.keyboardFocusPane) {
		// It was deleted in the config editor or a new config was loaded.
		wm.keyboardFocusPane = nil
	}
	if wm.keyboardFocusPane == nil {
		// Pick one that can take it. Try to find a CLI pane first since that's
		// most likely where the user would prefer to start out...
		positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
			if _, ok := p.(*CLIPane); ok {
				wm.keyboardFocusPane = p
			}
		})
		// If there's no CLIPane then go ahead and take any one that can
		// take keyboard events.
		if wm.keyboardFocusPane == nil {
			positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
				if p.CanTakeKeyboardFocus() {
					wm.keyboardFocusPane = p
				}
			})
		}
	}

	// Useful values related to the display size.
	fbSize := platform.FramebufferSize()
	displaySize := platform.DisplaySize()
	highDPIScale := fbSize[1] / displaySize[1]

	if wm.showConfigEditor {
		wm.configEditorHeight = 60 // FIXME: hardcoded
	} else {
		wm.configEditorHeight = 0
	}
	topItemsHeight := ui.menuBarHeight + wmStatusBarHeight() + wm.configEditorHeight

	// Area left for actually drawing Panes
	paneDisplayExtent := Extent2D{p0: [2]float32{0, 0}, p1: [2]float32{displaySize[0], displaySize[1] - topItemsHeight}}

	// Get the mouse position from imgui; flip y so that it lines up with
	// our window coordinates.
	mousePos := [2]float32{imgui.MousePos().X, displaySize[1] - 1 - imgui.MousePos().Y}

	// Figure out which Pane the mouse is in.
	var mousePane Pane
	if wm.fullScreenDisplayNode == nil {
		mousePane = positionConfig.DisplayRoot.FindPaneForMouse(paneDisplayExtent, mousePos)
	} else {
		// mousePane = wm.fullScreenDisplayNode.Pane ?
		mousePane = wm.fullScreenDisplayNode.FindPaneForMouse(paneDisplayExtent, mousePos)
	}

	io := imgui.CurrentIO()
	// Handle control-F, which either makes a Pane take up the window, or
	// goes back to the regular configuration.
	if !io.WantCaptureKeyboard() && platform.IsControlFPressed() {
		if wm.fullScreenDisplayNode == nil {
			// Don't maximize empty panes or split lines
			if _, ok := mousePane.(*SplitLine); !ok && mousePane != nil {
				wm.fullScreenDisplayNode = positionConfig.DisplayRoot.NodeForPane(mousePane)
			}
		} else {
			wm.fullScreenDisplayNode = nil
		}
	}

	// If the config editor is waiting for a Pane to be picked and the user
	// clicked in a Pane, report that news back.
	if wm.handlePanePick != nil && imgui.IsMouseClicked(mouseButtonPrimary) && mousePane != nil {
		// Ignore clicks on  split lines, however.
		if _, split := mousePane.(*SplitLine); !split {
			if wm.handlePanePick(mousePane) {
				wm.handlePanePick = nil
			}
		}
	}

	// If the user has clicked or is dragging in a Pane, record it in
	// mouseConsumerOverride so that we can continue to dispatch mouse
	// events to that Pane until the mouse button is released, even if the
	// mouse is no longer above it.
	isDragging := imgui.IsMouseDragging(mouseButtonPrimary, 0.) ||
		imgui.IsMouseDragging(mouseButtonSecondary, 0.) ||
		imgui.IsMouseDragging(mouseButtonTertiary, 0.)
	isClicked := imgui.IsMouseClicked(mouseButtonPrimary) ||
		imgui.IsMouseClicked(mouseButtonSecondary) ||
		imgui.IsMouseClicked(mouseButtonTertiary)
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

	// fbSize will be (0,0) if the window is minimized, in which case we
	// can skip all this...
	if fbSize[0] > 0 && fbSize[1] > 0 {
		// Now traverse all of the Panes...
		// First clear the entire window to the background color.
		commandBuffer.ClearRGB(positionConfig.GetColorScheme().Background)

		// Draw the status bar underneath the menu bar
		wmDrawStatusBar(fbSize, displaySize, commandBuffer)

		// By default we'll visit the tree starting at
		// DisplayRoot. However, if a Pane has been maximized to cover the
		// whole screen, we will instead start with it.
		root := positionConfig.DisplayRoot
		if wm.fullScreenDisplayNode != nil {
			root = wm.fullScreenDisplayNode
		}

		// Actually visit the panes.
		var keyboard *KeyboardState
		if !imgui.CurrentIO().WantCaptureKeyboard() {
			keyboard = NewKeyboardState()
		}
		root.VisitPanesWithBounds(paneDisplayExtent, paneDisplayExtent,
			func(paneExtent Extent2D, parentExtent Extent2D, pane Pane) {
				haveFocus := pane == wm.keyboardFocusPane && !imgui.CurrentIO().WantCaptureKeyboard()
				ctx := PaneContext{
					paneExtent:       paneExtent,
					parentPaneExtent: parentExtent,
					platform:         platform,
					events:           eventStream,
					keyboard:         keyboard,
					haveFocus:        haveFocus,
					cs:               positionConfig.GetColorScheme()}

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

				// If the config editor is active and the user has clicked
				// a button that is expecting a Pane to be selected (e.g.,
				// to delete it, etc.), then blend a semi-transparent
				// quadrilateral over the pane that the mouse is inside to
				// indicate that it is selected.
				if pane == mousePane && wm.handlePanePick != nil {
					ctx.SetWindowCoordinateMatrices(commandBuffer)
					commandBuffer.Blend()

					w, h := paneExtent.Width(), paneExtent.Height()
					p := [4][2]float32{[2]float32{0, 0}, [2]float32{w, 0}, [2]float32{w, h}, [2]float32{0, h}}
					pidx := commandBuffer.Float2Buffer(p[:])

					indices := [4]int32{0, 1, 2, 3}
					indidx := commandBuffer.IntBuffer(indices[:])

					commandBuffer.SetRGBA(RGBA{0.5, 0.5, 0.5, 0.5})
					commandBuffer.VertexArray(pidx, 2, 2*4)
					commandBuffer.DrawQuads(indidx, 4)
					commandBuffer.ResetState()
				}

				// Draw a border around the pane if it has keyboard focus.
				if haveFocus {
					ctx.SetWindowCoordinateMatrices(commandBuffer)
					w, h := paneExtent.Width(), paneExtent.Height()
					drawBorder(commandBuffer, w, h, ctx.cs.TextHighlight)
				}
			})

		// Clear mouseConsumerOverride if the user has stopped dragging;
		// only do this after visiting the Panes so that the override Pane
		// still sees the mouse button release event.
		if !isDragging && !isClicked {
			wm.mouseConsumerOverride = nil
		}

		// Finally, render the entire command buffer for all of the Panes
		// all at once.
		stats.render = renderer.RenderCommandBuffer(commandBuffer)
	}

	if wm.showConfigEditor {
		wmDrawConfigEditor(platform)
	}
}

// drawBorder emits drawing commands to the provided CommandBuffer to draw
// a border rectangle with given dimensions, inset 1 pixel.
func drawBorder(cb *CommandBuffer, w, h float32, color RGB) {
	p := [4][2]float32{[2]float32{0, 0}, [2]float32{w, 0}, [2]float32{w, h}, [2]float32{0, h}}
	pidx := cb.Float2Buffer(p[:])

	indidx := cb.IntBuffer([]int32{0, 1, 1, 2, 2, 3, 3, 0})

	cb.LineWidth(2)
	cb.SetRGB(color)
	cb.VertexArray(pidx, 2, 2*4)
	cb.DrawLines(indidx, 8)
	cb.ResetState()
}

// wmActivateNewConfig is called when a new PositionConfig is activated so
// that the window management code can take care of housekeeping.
func wmActivateNewConfig(old *PositionConfig, nw *PositionConfig) {
	// Position changed. First deactivate the old one
	if old != nw {
		if old != nil {
			old.DisplayRoot.VisitPanes(func(p Pane) { p.Deactivate() })
		}
		cs := nw.GetColorScheme()
		nw.DisplayRoot.VisitPanes(func(p Pane) { p.Activate(cs) })
	}

	wm.showPaneSettings = make(map[Pane]*bool)
	wm.showPaneName = make(map[Pane]string)
	wm.keyboardFocusPane = nil
}

// wmDrawStatus bar draws the status bar underneath the main menu bar
func wmDrawStatusBar(fbSize [2]float32, displaySize [2]float32, cb *CommandBuffer) {
	// First see if any of the errors have cleared
	for k, cleared := range ui.errorText {
		if cleared() {
			delete(ui.errorText, k)
		}
	}

	if len(ui.errorText) == 0 {
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
	proj := mgl32.Ortho2D(0, displaySize[0], 0, statusBarHeight)
	cb.LoadProjectionMatrix(proj)
	cb.LoadModelViewMatrix(mgl32.Ident4())

	ctx := PaneContext{
		paneExtent:       statusBarDisplayExtent,
		parentPaneExtent: Extent2D{p1: displaySize},
		platform:         platform,
		events:           eventStream,
		cs:               positionConfig.GetColorScheme(),
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	textp := [2]float32{15, 5 + float32(len(ui.errorText)*ui.font.size)}
	errorStyle := TextStyle{Font: ui.font, Color: ctx.cs.TextError}
	for _, k := range SortedMapKeys(ui.errorText) {
		textp = td.AddText(k+"\n", textp, errorStyle)
	}

	// Finally, add the text drawing commands to the graphics command buffer.
	td.GenerateCommands(cb)

	cb.ResetState()
}

func wmStatusBarHeight() float32 {
	if len(ui.errorText) == 0 {
		return 0
	}
	// Reserve lines as needed for error text.
	return float32(10 + (len(ui.errorText))*ui.font.size)
}

///////////////////////////////////////////////////////////////////////////
// ModalButtonSet

// ModalButtonSet handles some of the housekeeping for the buttons used
// when editing configs, allowing buttons to be shown or not depending on
// external state and handling pane selection through provided callbacks.
type ModalButtonSet struct {
	active    string
	names     []string
	callbacks []func() func(Pane) bool
	show      []func() bool
}

// Add adds a button with the given text to the button set. The value
// returned show callback determines whether the button is drawn, and the
// selected callback is called if the button is pressed and a Pane is then
// selected by the user.
func (m *ModalButtonSet) Add(text string, selected func() func(Pane) bool, show func() bool) {
	m.names = append(m.names, text)
	m.callbacks = append(m.callbacks, selected)
	m.show = append(m.show, show)
}

// Clear deselects the currently active button, if any.
func (m *ModalButtonSet) Clear() {
	m.active = ""
}

// Draw draws the buttons and handles user interaction.
func (m *ModalButtonSet) Draw() {
	for i, name := range m.names {
		// Skip invisible buttons.
		if !m.show[i]() {
			continue
		}

		if m.active == name {
			// If the button has already been pressed and we're waiting for
			// a pane to be selected draw it in its 'hovered' state,
			// regardless of whether the mouse is actually hovering over
			// it.
			imgui.PushID(m.active)

			h := imgui.CurrentStyle().Color(imgui.StyleColorButtonHovered)
			imgui.PushStyleColor(imgui.StyleColorButton, h) // active

			imgui.Button(name)
			if imgui.IsItemClicked() {
				// If the button is clicked again, roll back and deselect
				// it.
				wm.handlePanePick = nil
				m.active = ""
			}
			imgui.PopStyleColorV(1)
			imgui.PopID()
		} else if imgui.Button(name) {
			// First click of the button. Make it active.
			m.active = name

			wm.paneFirstPick = nil

			// Get the actual callback for pane selection (and allow the
			// user to do some prep work, knowing they've been selected)
			callback := m.callbacks[i]()

			// Register the pane pick callback to dispatch pane selection
			// to this button's callback.
			wm.handlePanePick = func(pane Pane) bool {
				// But now wrap the pick callback in our own function so
				// that we can clear |active| after successful selection.
				result := callback(pane)
				if result {
					m.active = ""
				}
				return result
			}
		}
		// Keep all of the buttons on the same line.
		if i < len(m.names)-1 {
			imgui.SameLine()
		}
	}
}

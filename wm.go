// wm.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mmp/imgui-go/v4"
)

var (
	wm struct {
		showConfigEditor   bool
		paneFirstPick      Pane
		handlePanePick     func(Pane) bool
		paneCreatePrompt   string
		paneConfigHelpText string
		editorBackupRoot   *DisplayNode

		configButtons ModalButtonSet

		showPaneSettings map[Pane]*bool
		showPaneName     map[Pane]string

		showPaneAsRoot  bool
		nodeFilter      func(*DisplayNode) *DisplayNode
		nodeFilterUnset bool

		topControlsHeight float32

		mouseConsumerOverride Pane
		keyboardFocusPane     Pane
		keyboardFocusStack    []Pane
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

type SplitLine struct {
	Pos  float32
	Axis SplitType
}

func (s *SplitLine) Duplicate(nameAsCopy bool) Pane {
	lg.Errorf("This actually should never be called...")
	return &SplitLine{}
}

func (s *SplitLine) Activate(cs *ColorScheme)      {}
func (s *SplitLine) Deactivate()                   {}
func (s *SplitLine) CanTakeKeyboardFocus() bool    { return false }
func (s *SplitLine) ProcessEvents(es *EventStream) {}

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

	cb.ClearRGB(ctx.cs.UIControl)
}

func splitLineWidth() int {
	return int(3*dpiScale(platform) + 0.5)
}

///////////////////////////////////////////////////////////////////////////
// DisplayNode

type DisplayNode struct {
	Pane      Pane // set iff splitAxis == SplitAxisNone
	SplitLine SplitLine
	Children  [2]*DisplayNode // set iff splitAxis != SplitAxisNone
}

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

func (d *DisplayNode) NodeForPane(pane Pane) *DisplayNode {
	if d.Pane == pane {
		return d
	}
	if d.Children[0] == nil {
		return nil
	}
	d0 := d.Children[0].NodeForPane(pane)
	if d0 != nil {
		return d0
	}
	return d.Children[1].NodeForPane(pane)
}

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

type TypedDisplayNodePane struct {
	DisplayNode
	Type string
}

func (d *DisplayNode) MarshalJSON() ([]byte, error) {
	td := TypedDisplayNodePane{DisplayNode: *d}
	if d.Pane != nil {
		td.Type = fmt.Sprintf("%T", d.Pane)
	}
	return json.Marshal(td)
}

func (d *DisplayNode) UnmarshalJSON(s []byte) error {
	var m map[string]*json.RawMessage
	if err := json.Unmarshal(s, &m); err != nil {
		return err
	}

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
	}

	return nil
}

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

func (d *DisplayNode) VisitPanesWithBounds(nodeFilter func(*DisplayNode) *DisplayNode,
	framebufferExtent Extent2D, displayExtent Extent2D,
	parentDisplayExtent Extent2D, fullDisplayExtent Extent2D,
	visit func(Extent2D, Extent2D, Extent2D, Extent2D, Pane)) {
	d = nodeFilter(d)

	switch d.SplitLine.Axis {
	case SplitAxisNone:
		visit(framebufferExtent, displayExtent, parentDisplayExtent, fullDisplayExtent, d.Pane)
	case SplitAxisX:
		f0, fs, f1 := framebufferExtent.SplitX(d.SplitLine.Pos, splitLineWidth())
		d0, ds, d1 := displayExtent.SplitX(d.SplitLine.Pos, splitLineWidth())
		d.Children[0].VisitPanesWithBounds(nodeFilter, f0, d0, displayExtent, fullDisplayExtent, visit)
		visit(fs, ds, displayExtent, fullDisplayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(nodeFilter, f1, d1, displayExtent, fullDisplayExtent, visit)
	case SplitAxisY:
		f0, fs, f1 := framebufferExtent.SplitY(d.SplitLine.Pos, splitLineWidth())
		d0, ds, d1 := displayExtent.SplitY(d.SplitLine.Pos, splitLineWidth())
		d.Children[0].VisitPanesWithBounds(nodeFilter, f0, d0, displayExtent, fullDisplayExtent, visit)
		visit(fs, ds, displayExtent, fullDisplayExtent, &d.SplitLine)
		d.Children[1].VisitPanesWithBounds(nodeFilter, f1, d1, displayExtent, fullDisplayExtent, visit)
	}
}

func (d *DisplayNode) SplitX(x float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		lg.Errorf("splitting a non-leaf node: %v", d)
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: x},
		Children: [2]*DisplayNode{d, newChild}}
}

func (d *DisplayNode) SplitY(y float32, newChild *DisplayNode) *DisplayNode {
	if d.SplitLine.Axis != SplitAxisNone {
		lg.Errorf("splitting a non-leaf node: %v", d)
	}
	return &DisplayNode{SplitLine: SplitLine{Axis: SplitAxisX, Pos: y},
		Children: [2]*DisplayNode{d, newChild}}
}

func findPaneForMouse(node *DisplayNode, displayExtent Extent2D, p [2]float32) Pane {
	if !displayExtent.Inside(p) {
		return nil
	}
	if node.SplitLine.Axis == SplitAxisNone {
		return node.Pane
	}
	var d0, ds, d1 Extent2D
	if node.SplitLine.Axis == SplitAxisX {
		d0, ds, d1 = displayExtent.SplitX(node.SplitLine.Pos, splitLineWidth())
	} else {
		d0, ds, d1 = displayExtent.SplitY(node.SplitLine.Pos, splitLineWidth())
	}
	if d0.Inside(p) {
		return findPaneForMouse(node.Children[0], d0, p)
	} else if ds.Inside(p) {
		return &node.SplitLine
	} else if d1.Inside(p) {
		return findPaneForMouse(node.Children[1], d1, p)
	} else {
		lg.Errorf("Mouse not overlapping anything?")
		return nil
	}
}

func wmInit() {
	lg.Printf("Starting wm initialization")
	wm.nodeFilter = func(node *DisplayNode) *DisplayNode { return node }
	wm.nodeFilterUnset = true

	var pthelper func(indent string, node *DisplayNode) string
	pthelper = func(indent string, node *DisplayNode) string {
		if node == nil {
			return ""
		}
		s := fmt.Sprintf(indent+"%p split %d pane %p (%T)\n", node, node.SplitLine.Axis, node.Pane, node.Pane)
		s += pthelper(indent+"     ", node.Children[0])
		s += pthelper(indent+"     ", node.Children[1])
		return s
	}
	printtree := func() string {
		return pthelper("", positionConfig.DisplayRoot)
	}

	wm.configButtons.Add("Copy", func() func(pane Pane) bool {
		wm.paneConfigHelpText = "Select window to copy"
		return func(pane Pane) bool {
			if wm.paneFirstPick == nil {
				wm.paneFirstPick = pane
				wm.paneConfigHelpText = "Select destination for copy"
				return false
			} else {
				node := positionConfig.DisplayRoot.NodeForPane(pane)
				lg.Printf("about to copy %p %+T to node %v.\ntree: %s", pane, pane, node, printtree())
				node.Pane = wm.paneFirstPick.Duplicate(true)
				wm.paneFirstPick = nil
				wm.paneConfigHelpText = ""
				lg.Printf("new tree:\n%s", printtree())
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
					lg.Printf("about echange nodes %p %+v %p %+v.\ntree: %s", n0, n0, n1, n1, printtree())
					if pane != wm.paneFirstPick {
						n0.Pane, n1.Pane = n1.Pane, n0.Pane
					}
					wm.paneFirstPick = nil
					wm.paneConfigHelpText = ""
					lg.Printf("new tree:\n%s", printtree())
					return true
				}
			}
		}, func() bool { return positionConfig.DisplayRoot.Children[0] != nil })

	handleSplitPick := func(axis SplitType) func() func(pane Pane) bool {
		return func() func(pane Pane) bool {
			wm.paneConfigHelpText = "Select window to split"
			return func(pane Pane) bool {
				lg.Printf("about to split %p %+T.\ntree: %s", pane, pane, printtree())
				node := positionConfig.DisplayRoot.NodeForPane(pane)
				node.Children[0] = &DisplayNode{Pane: &EmptyPane{}}
				node.Children[1] = &DisplayNode{Pane: pane}
				node.Pane = nil
				node.SplitLine.Pos = 0.5
				node.SplitLine.Axis = axis
				wm.paneConfigHelpText = ""
				lg.Printf("new tree:\n%s", printtree())
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
			lg.Printf("about to delete %p %+T.\ntree: %s", pane, pane, printtree())
			node, idx := positionConfig.DisplayRoot.ParentNodeForPane(pane)
			other := idx ^ 1
			*node = *node.Children[other]
			wm.paneConfigHelpText = ""
			lg.Printf("new tree:\n%s", printtree())
			return true
		}
	}, func() bool { return positionConfig.DisplayRoot.Children[0] != nil })

	lg.Printf("Finished wm initialization")
}

func wmProcessEvents(es *EventStream) {
	positionConfig.DisplayRoot.VisitPanes(func(pane Pane) {
		pane.ProcessEvents(es)
	})
}

func wmAddPaneMenuSettings() {
	var panes []Pane
	positionConfig.DisplayRoot.VisitPanes(func(pane Pane) {
		if _, ok := pane.(PaneUIDrawer); ok {
			panes = append(panes, pane)
		}
	})

	// sort by name
	sort.Slice(panes, func(i, j int) bool { return panes[i].Name() < panes[j].Name() })

	for _, pane := range panes {
		if imgui.MenuItem(pane.Name() + "...") {
			// copy the name so that it can be edited...
			wm.showPaneName[pane] = pane.Name()
			t := true
			wm.showPaneSettings[pane] = &t
		}
	}
}

func wmDrawUI(p Platform) {
	wm.topControlsHeight = ui.topControlsHeight
	if wm.showConfigEditor {
		var flags imgui.WindowFlags
		flags = imgui.WindowFlagsNoDecoration
		flags |= imgui.WindowFlagsNoSavedSettings
		flags |= imgui.WindowFlagsNoNav
		flags |= imgui.WindowFlagsNoResize

		displaySize := p.DisplaySize()
		imgui.SetNextWindowPosV(imgui.Vec2{X: 0, Y: ui.topControlsHeight}, imgui.ConditionAlways, imgui.Vec2{})
		imgui.SetNextWindowSize(imgui.Vec2{displaySize[0], 60}) //displaySize[1]})
		wm.topControlsHeight += 60
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
		if imgui.BeginCombo("##Set...", prompt) {
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
			if imgui.Selectable("Radar Scope") {
				wm.paneCreatePrompt = "Radar scope"
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
	}

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

func wmTakeKeyboardFocus(pane Pane, isTransient bool) {
	if wm.keyboardFocusPane == pane {
		return
	}
	if isTransient && wm.keyboardFocusPane != nil {
		wm.keyboardFocusStack = append(wm.keyboardFocusStack, wm.keyboardFocusPane)
	}
	if !isTransient {
		wm.keyboardFocusStack = nil
	}
	wm.keyboardFocusPane = pane
}

func wmReleaseKeyboardFocus() {
	if n := len(wm.keyboardFocusStack); n > 0 {
		wm.keyboardFocusPane = wm.keyboardFocusStack[n-1]
		wm.keyboardFocusStack = wm.keyboardFocusStack[:n-1]
	}
}

func wmPaneIsPresent(pane Pane) bool {
	found := false
	positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
		if p == pane {
			found = true
		}
	})
	return found
}

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

	io := imgui.CurrentIO()

	fbSize := platform.FramebufferSize()
	displaySize := platform.DisplaySize()
	heightRatio := fbSize[1] / displaySize[1]

	fbFull := Extent2D{p0: [2]float32{0, 0},
		p1: [2]float32{fbSize[0], fbSize[1] - heightRatio*wm.topControlsHeight}}
	displayFull := Extent2D{p0: [2]float32{0, 0},
		p1: [2]float32{displaySize[0], displaySize[1] - wm.topControlsHeight}}
	displayTrueFull := Extent2D{p0: [2]float32{0, 0},
		p1: [2]float32{displaySize[0], displaySize[1]}}

	if !io.WantCaptureKeyboard() && platform.IsControlFPressed() {
		wm.showPaneAsRoot = !wm.showPaneAsRoot
	}

	mousePos := imgui.MousePos()
	// Yaay, y flips
	mousePos.Y = displaySize[1] - 1 - mousePos.Y

	var mousePane Pane
	if wm.showPaneAsRoot && wm.nodeFilterUnset {
		pane := findPaneForMouse(positionConfig.DisplayRoot, displayFull,
			[2]float32{mousePos.X, mousePos.Y})
		// Don't maximize empty panes or split lines
		if _, ok := pane.(*SplitLine); !ok && pane != nil {
			wm.nodeFilter = func(node *DisplayNode) *DisplayNode {
				return &DisplayNode{Pane: pane}
			}
			mousePane = pane
			wm.nodeFilterUnset = false
		}
	}
	if !wm.showPaneAsRoot {
		if !wm.nodeFilterUnset {
			wm.nodeFilter = func(node *DisplayNode) *DisplayNode { return node }
			wm.nodeFilterUnset = true
		}
		mousePane = findPaneForMouse(positionConfig.DisplayRoot, displayFull,
			[2]float32{mousePos.X, mousePos.Y})
	}

	if wm.handlePanePick != nil && imgui.IsMouseClicked(mouseButtonPrimary) && mousePane != nil {
		// Filter out splits
		if _, split := mousePane.(*SplitLine); !split {
			if wm.handlePanePick(mousePane) {
				wm.handlePanePick = nil
			}
		}
	}

	// Clear the mouse override if imgui wants mouse events or if there
	// is no longer any click or drag action.
	isDragging := imgui.IsMouseDragging(mouseButtonPrimary, 0.) ||
		imgui.IsMouseDragging(mouseButtonSecondary, 0.) ||
		imgui.IsMouseDragging(mouseButtonTertiary, 0.)
	isClicked := imgui.IsMouseClicked(mouseButtonPrimary) ||
		imgui.IsMouseClicked(mouseButtonSecondary) ||
		imgui.IsMouseClicked(mouseButtonTertiary)
	if io.WantCaptureMouse() || (!isDragging && !isClicked) {
		wm.mouseConsumerOverride = nil
	}
	// Set the mouse override if it's unset but it should be.
	if !io.WantCaptureMouse() && (isDragging || isClicked) && wm.mouseConsumerOverride == nil {
		wm.mouseConsumerOverride = mousePane
	}

	// Set the mouse cursor
	setCursorForPane := func(p Pane) {
		if sl, ok := p.(*SplitLine); ok {
			if sl.Axis == SplitAxisX {
				imgui.SetMouseCursor(imgui.MouseCursorResizeEW)
			} else {
				imgui.SetMouseCursor(imgui.MouseCursorResizeNS)
			}
		} else {
			imgui.SetMouseCursor(imgui.MouseCursorArrow) // just to be sure; may be already
		}
	}
	if wm.mouseConsumerOverride != nil {
		setCursorForPane(wm.mouseConsumerOverride)
	} else {
		setCursorForPane(mousePane)
	}

	mouseInScope := func(p imgui.Vec2, extent Extent2D) bool {
		if io.WantCaptureMouse() {
			return false
		}
		return extent.Inside([2]float32{p.X, p.Y})
	}

	// Get all of the draw lists
	var commandBuffer CommandBuffer
	if fbSize[0] > 0 && fbSize[1] > 0 {
		commandBuffer.ClearRGB(positionConfig.GetColorScheme().Background)

		positionConfig.DisplayRoot.VisitPanesWithBounds(wm.nodeFilter, fbFull, displayFull, displayFull, displayTrueFull,
			func(fb Extent2D, disp Extent2D, parentDisp Extent2D, fullDisp Extent2D, pane Pane) {
				ctx := PaneContext{
					paneExtent:        disp,
					parentPaneExtent:  parentDisp,
					fullDisplayExtent: fullDisp,
					highDPIScale:      fbFull.Height() / displayFull.Height(),
					platform:          platform,
					cs:                positionConfig.GetColorScheme()}

				if pane == wm.keyboardFocusPane {
					ctx.InitializeKeyboard()
				}

				ownsMouse := wm.mouseConsumerOverride == pane ||
					(wm.mouseConsumerOverride == nil && mouseInScope(mousePos, disp) &&
						!io.WantCaptureMouse())
				if ownsMouse {
					ctx.InitializeMouse()
				}

				commandBuffer.Scissor(int(fb.p0[0]), int(fb.p0[1]), int(fb.Width()+.5), int(fb.Height()+.5))
				commandBuffer.Viewport(int(fb.p0[0]), int(fb.p0[1]), int(fb.Width()+.5), int(fb.Height()+.5))
				pane.Draw(&ctx, &commandBuffer)
				commandBuffer.ResetState()

				if pane == mousePane && wm.handlePanePick != nil {
					// Blend in the plane selection quad
					ctx.SetWindowCoordinateMatrices(&commandBuffer)
					commandBuffer.Blend()

					w, h := disp.Width(), disp.Height()
					p := [4][2]float32{[2]float32{0, 0}, [2]float32{w, 0}, [2]float32{w, h}, [2]float32{0, h}}
					pidx := commandBuffer.Float2Buffer(p[:])

					indices := [4]int32{0, 1, 2, 3}
					indidx := commandBuffer.IntBuffer(indices[:])

					commandBuffer.SetRGBA(RGBA{0.5, 0.5, 0.5, 0.5})
					commandBuffer.VertexArray(pidx, 2, 2*4)
					commandBuffer.DrawQuads(indidx, 4)
					commandBuffer.ResetState()
				}
				if pane == wm.keyboardFocusPane {
					// Draw a border around it
					ctx.SetWindowCoordinateMatrices(&commandBuffer)
					w, h := disp.Width(), disp.Height()
					p := [4][2]float32{[2]float32{1, 1}, [2]float32{w - 1, 1}, [2]float32{w - 1, h - 1}, [2]float32{1, h - 1}}
					pidx := commandBuffer.Float2Buffer(p[:])

					indidx := commandBuffer.IntBuffer([]int32{0, 1, 1, 2, 2, 3, 3, 0})

					commandBuffer.SetRGB(ctx.cs.TextHighlight)
					commandBuffer.VertexArray(pidx, 2, 2*4)
					commandBuffer.DrawLines(indidx, 8)
					commandBuffer.ResetState()
				}
			})

		stats.render = renderer.RenderCommandBuffer(&commandBuffer)
	}
}

func wmActivateNewConfig(old *PositionConfig, nw *PositionConfig, cs *ColorScheme) {
	// Position changed. First deactivate the old one
	if old != nil {
		old.DisplayRoot.VisitPanes(func(p Pane) { p.Deactivate() })
	}
	wm.showPaneSettings = make(map[Pane]*bool)
	wm.showPaneName = make(map[Pane]string)
	nw.DisplayRoot.VisitPanes(func(p Pane) { p.Activate(cs) })
	wm.keyboardFocusPane = nil
}

///////////////////////////////////////////////////////////////////////////
// ModalButtonSet

type ModalButtonSet struct {
	active    string
	names     []string
	callbacks []func() func(Pane) bool
	show      []func() bool
}

func (m *ModalButtonSet) Add(name string, callback func() func(Pane) bool, show func() bool) {
	m.names = append(m.names, name)
	m.callbacks = append(m.callbacks, callback)
	m.show = append(m.show, show)
}

func (m *ModalButtonSet) Clear() {
	m.active = ""
}

func (m *ModalButtonSet) Draw() {
	for i, name := range m.names {
		if !m.show[i]() {
			continue
		}
		if m.active == name {
			imgui.PushID(m.active)

			h := imgui.CurrentStyle().Color(imgui.StyleColorButtonHovered)
			imgui.PushStyleColor(imgui.StyleColorButton, h) // active

			imgui.Button(name)
			if imgui.IsItemClicked() {
				wm.handlePanePick = nil
				m.active = ""
			}
			imgui.PopStyleColorV(1)
			imgui.PopID()
		} else if imgui.Button(name) {
			m.active = name

			wm.paneFirstPick = nil
			// Get the actual callback for pane selection (and allow the
			// user to do some prep work, knowing they've been selected)
			callback := m.callbacks[i]()

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
		if i < len(m.names)-1 {
			imgui.SameLine()
		}
	}
}

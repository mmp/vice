// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Activate()
	Deactivate()

	CanTakeKeyboardFocus() bool

	Draw(ctx *PaneContext, cb *CommandBuffer)
}

type PaneUIDrawer interface {
	DrawUI()
}

type PaneContext struct {
	paneExtent       Extent2D
	parentPaneExtent Extent2D

	platform  Platform
	mouse     *MouseState
	keyboard  *KeyboardState
	haveFocus bool
	events    *EventStream
}

type MouseState struct {
	Pos           [2]float32
	Down          [MouseButtonCount]bool
	Clicked       [MouseButtonCount]bool
	Released      [MouseButtonCount]bool
	DoubleClicked [MouseButtonCount]bool
	Dragging      [MouseButtonCount]bool
	DragDelta     [2]float32
	Wheel         [2]float32
}

const (
	MouseButtonPrimary   = 0
	MouseButtonSecondary = 1
	MouseButtonTertiary  = 2
	MouseButtonCount     = 3
)

func (ctx *PaneContext) InitializeMouse(fullDisplayExtent Extent2D) {
	ctx.mouse = &MouseState{}

	// Convert to pane coordinates:
	// imgui gives us the mouse position w.r.t. the full window, so we need
	// to subtract out displayExtent.p0 to get coordinates w.r.t. the
	// current pane.  Further, it has (0,0) in the upper left corner of the
	// window, so we need to flip y w.r.t. the full window resolution.
	pos := imgui.MousePos()
	ctx.mouse.Pos[0] = pos.X - ctx.paneExtent.p0[0]
	ctx.mouse.Pos[1] = fullDisplayExtent.p1[1] - 1 - ctx.paneExtent.p0[1] - pos.Y

	io := imgui.CurrentIO()
	wx, wy := io.MouseWheel()
	ctx.mouse.Wheel = [2]float32{wx, -wy}

	for b := 0; b < MouseButtonCount; b++ {
		ctx.mouse.Down[b] = imgui.IsMouseDown(b)
		ctx.mouse.Released[b] = imgui.IsMouseReleased(b)
		ctx.mouse.Clicked[b] = imgui.IsMouseClicked(b)
		ctx.mouse.DoubleClicked[b] = imgui.IsMouseDoubleClicked(b)
		ctx.mouse.Dragging[b] = imgui.IsMouseDragging(b, 0)
		if ctx.mouse.Dragging[b] {
			delta := imgui.MouseDragDelta(b, 0.)
			// Negate y to go to pane coordinates
			ctx.mouse.DragDelta = [2]float32{delta.X, -delta.Y}
			imgui.ResetMouseDragDelta(b)
		}
	}
}

type Key int

const (
	KeyEnter = iota
	KeyUpArrow
	KeyDownArrow
	KeyLeftArrow
	KeyRightArrow
	KeyHome
	KeyEnd
	KeyBackspace
	KeyDelete
	KeyEscape
	KeyTab
	KeyPageUp
	KeyPageDown
	KeyShift
	KeyControl
	KeyAlt
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

type KeyboardState struct {
	Input   string
	Pressed map[Key]interface{}
}

func NewKeyboardState() *KeyboardState {
	keyboard := &KeyboardState{Pressed: make(map[Key]interface{})}

	keyboard.Input = platform.InputCharacters()

	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnter)) {
		keyboard.Pressed[KeyEnter] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDownArrow)) {
		keyboard.Pressed[KeyDownArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyUpArrow)) {
		keyboard.Pressed[KeyUpArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyLeftArrow)) {
		keyboard.Pressed[KeyLeftArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyRightArrow)) {
		keyboard.Pressed[KeyRightArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyHome)) {
		keyboard.Pressed[KeyHome] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnd)) {
		keyboard.Pressed[KeyEnd] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyBackspace)) {
		keyboard.Pressed[KeyBackspace] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDelete)) {
		keyboard.Pressed[KeyDelete] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEscape)) {
		keyboard.Pressed[KeyEscape] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyTab)) {
		keyboard.Pressed[KeyTab] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageUp)) {
		keyboard.Pressed[KeyPageUp] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageDown)) {
		keyboard.Pressed[KeyPageDown] = nil
	}
	const ImguiF1 = 290
	for i := 0; i < 12; i++ {
		if imgui.IsKeyPressed(ImguiF1 + i) {
			keyboard.Pressed[Key(int(KeyF1)+i)] = nil
		}
	}
	io := imgui.CurrentIO()
	if io.KeyShiftPressed() {
		keyboard.Pressed[KeyShift] = nil
	}
	if io.KeyCtrlPressed() {
		keyboard.Pressed[KeyControl] = nil
	}
	if io.KeyAltPressed() {
		keyboard.Pressed[KeyAlt] = nil
	}

	return keyboard
}

func (k *KeyboardState) IsPressed(key Key) bool {
	_, ok := k.Pressed[key]
	return ok
}

func (ctx *PaneContext) SetWindowCoordinateMatrices(cb *CommandBuffer) {
	w := float32(int(ctx.paneExtent.Width() + 0.5))
	h := float32(int(ctx.paneExtent.Height() + 0.5))
	cb.LoadProjectionMatrix(Identity3x3().Ortho(0, w, 0, h))
	cb.LoadModelViewMatrix(Identity3x3())
}

// Helper function to unmarshal the JSON of a Pane of a given type T.
func unmarshalPaneHelper[T Pane](data []byte) (Pane, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

func unmarshalPane(paneType string, data []byte) (Pane, error) {
	switch paneType {
	case "":
		// nil pane
		return nil, nil

	case "*main.EmptyPane":
		return unmarshalPaneHelper[*EmptyPane](data)

	case "*main.FlightStripPane":
		return unmarshalPaneHelper[*FlightStripPane](data)

	case "*main.STARSPane":
		return unmarshalPaneHelper[*STARSPane](data)

	default:
		lg.Errorf("%s: Unhandled type in config file", paneType)
		return NewEmptyPane(), nil
	}
}

///////////////////////////////////////////////////////////////////////////
// EmptyPane

type EmptyPane struct {
	// Empty struct types may all have the same address, which breaks
	// assorted assumptions elsewhere in the system....
	wtfgo int
}

func NewEmptyPane() *EmptyPane { return &EmptyPane{} }

func (ep *EmptyPane) Activate()                  {}
func (ep *EmptyPane) Deactivate()                {}
func (ep *EmptyPane) CanTakeKeyboardFocus() bool { return false }

func (ep *EmptyPane) Name() string { return "(Empty)" }

func (ep *EmptyPane) Draw(ctx *PaneContext, cb *CommandBuffer) {}

///////////////////////////////////////////////////////////////////////////
// FlightStripPane

type FlightStripPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	AutoAddDepartures         bool
	AutoAddArrivals           bool
	AutoAddTracked            bool
	AutoAddAcceptedHandoffs   bool
	AutoRemoveDropped         bool
	AutoRemoveHandoffs        bool
	AddPushed                 bool
	CollectDeparturesArrivals bool

	strips        []string // callsigns
	addedAircraft map[string]interface{}

	mouseDragging       bool
	lastMousePos        [2]float32
	selectedStrip       int
	selectedAnnotation  int
	annotationCursorPos int

	eventsId  EventSubscriberId
	scrollbar *ScrollBar

	selectedAircraft *Aircraft
}

func NewFlightStripPane() *FlightStripPane {
	return &FlightStripPane{
		AddPushed:          true,
		FontIdentifier:     FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 14},
		selectedStrip:      -1,
		selectedAnnotation: -1,
	}
}

func (fsp *FlightStripPane) Activate() {
	if fsp.font = GetFont(fsp.FontIdentifier); fsp.font == nil {
		fsp.font = GetDefaultFont()
		fsp.FontIdentifier = fsp.font.id
	}
	if fsp.addedAircraft == nil {
		fsp.addedAircraft = make(map[string]interface{})
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewScrollBar(4, true)
	}
	fsp.eventsId = eventStream.Subscribe()
}

func (fsp *FlightStripPane) Deactivate() {
	eventStream.Unsubscribe(fsp.eventsId)
	fsp.eventsId = InvalidEventSubscriberId
}

func (fsp *FlightStripPane) isDeparture(ac *Aircraft) bool {
	if ac.FlightPlan == nil {
		return false
	}
	_, ok := scenarioGroup.Airports[ac.FlightPlan.DepartureAirport]
	return ok
}

func (fsp *FlightStripPane) isArrival(ac *Aircraft) bool {
	if ac.FlightPlan == nil {
		return false
	}
	_, ok := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport]
	return ok
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return false /*true*/ }

func (fsp *FlightStripPane) processEvents(es *EventStream) {
	possiblyAdd := func(ac *Aircraft) {
		callsign := ac.Callsign
		if _, ok := fsp.addedAircraft[callsign]; ok {
			return
		}

		if ac.FlightPlan == nil {
			return
		}

		fsp.strips = append(fsp.strips, callsign)
		fsp.addedAircraft[callsign] = nil
	}

	remove := func(ac *Aircraft) {
		fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool { return callsign != ac.Callsign })
	}

	for _, event := range es.Get(fsp.eventsId) {
		switch v := event.(type) {
		case *PushedFlightStripEvent:
			if Find(fsp.strips, v.callsign) == -1 {
				fsp.strips = append(fsp.strips, v.callsign)
			}

		case *AddedAircraftEvent:
			if fsp.AutoAddTracked && v.ac.TrackingController == sim.Callsign() {
				possiblyAdd(v.ac)
			} else if v.ac.TrackingController == "" &&
				((fsp.AutoAddDepartures && fsp.isDeparture(v.ac)) || (fsp.AutoAddArrivals && fsp.isArrival(v.ac))) {
				possiblyAdd(v.ac)
			}

		case *ModifiedAircraftEvent:
			if fsp.AutoAddTracked && v.ac.TrackingController == sim.Callsign() {
				possiblyAdd(v.ac)
			} else if v.ac.TrackingController == "" &&
				((fsp.AutoAddDepartures && fsp.isDeparture(v.ac)) || (fsp.AutoAddArrivals && fsp.isArrival(v.ac))) {
				possiblyAdd(v.ac)
			}

		case *InitiatedTrackEvent:
			if fsp.AutoAddTracked && v.ac.TrackingController == sim.Callsign() {
				possiblyAdd(v.ac)
			}

		case *DroppedTrackEvent:
			if fsp.AutoRemoveDropped {
				remove(v.ac)
			}

		case *AcceptedHandoffEvent:
			if fsp.AutoAddAcceptedHandoffs && v.ac.TrackingController == sim.Callsign() {
				possiblyAdd(v.ac)
			} else if fsp.AutoRemoveHandoffs && v.ac.TrackingController != sim.Callsign() {
				remove(v.ac)
			}

		case *RemovedAircraftEvent:
			remove(v.ac)
		}
	}

	// TODO: is this needed? Shouldn't there be a RemovedAircraftEvent?
	fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool {
		ac := sim.GetAircraft(callsign)
		return ac != nil
	})

	if fsp.CollectDeparturesArrivals {
		isDeparture := func(callsign string) bool {
			if ac := sim.GetAircraft(callsign); ac == nil {
				return false
			} else {
				return fsp.isDeparture(ac)
			}
		}
		dep := FilterSlice(fsp.strips, isDeparture)
		arr := FilterSlice(fsp.strips, func(callsign string) bool { return !isDeparture(callsign) })

		fsp.strips = fsp.strips[:0]
		fsp.strips = append(fsp.strips, dep...)
		fsp.strips = append(fsp.strips, arr...)
	}
}

func (fsp *FlightStripPane) Name() string { return "Flight Strips" }

func (fsp *FlightStripPane) DrawUI() {
	imgui.Checkbox("Automatically add departures", &fsp.AutoAddDepartures)
	imgui.Checkbox("Automatically add arrivals", &fsp.AutoAddArrivals)
	imgui.Checkbox("Add pushed flight strips", &fsp.AddPushed)
	imgui.Checkbox("Automatically add when track is initiated", &fsp.AutoAddTracked)
	imgui.Checkbox("Automatically add handoffs", &fsp.AutoAddAcceptedHandoffs)
	imgui.Checkbox("Automatically remove dropped tracks", &fsp.AutoRemoveDropped)
	imgui.Checkbox("Automatically remove accepted handoffs", &fsp.AutoRemoveHandoffs)

	imgui.Checkbox("Collect departures and arrivals together", &fsp.CollectDeparturesArrivals)

	if newFont, changed := DrawFontPicker(&fsp.FontIdentifier, "Font"); changed {
		fsp.font = newFont
	}
}

func (fsp *FlightStripPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	fsp.processEvents(ctx.events)

	// Font width and height
	bx, _ := fsp.font.BoundText(" ", 0)
	fw, fh := float32(bx), float32(fsp.font.size)

	ctx.SetWindowCoordinateMatrices(cb)

	// 4 lines of text, 2 lines on top and below for padding, 1 pixel separator line
	vpad := float32(2)
	stripHeight := 1 + 2*vpad + 4*fh

	visibleStrips := int(ctx.paneExtent.Height() / stripHeight)
	fsp.scrollbar.Update(len(fsp.strips), visibleStrips, ctx)

	indent := float32(int32(fw / 2))
	// column widths
	width0 := 10 * fw
	width1 := 6 * fw
	width2 := 5 * fw
	widthAnn := 5 * fw

	widthCenter := ctx.paneExtent.Width() - width0 - width1 - width2 - 3*widthAnn
	if fsp.scrollbar.Visible() {
		widthCenter -= float32(fsp.scrollbar.Width())
	}
	if widthCenter < 0 {
		// not sure what to do if it comes to this...
		widthCenter = 20 * fw
	}

	drawWidth := ctx.paneExtent.Width()
	if fsp.scrollbar.Visible() {
		drawWidth -= float32(fsp.scrollbar.Width())
	}

	// This can happen if, for example, the last aircraft is selected and
	// then another one is removed. It might be better if selectedAircraft
	// was a pointer to something that FlightStripPane managed, so that
	// this sort of case would be handled more naturally... (And note that
	// tracking the callsign won't work if we want to have strips for the
	// same aircraft twice in a pane, for what that's worth...)
	if fsp.selectedStrip >= len(fsp.strips) {
		fsp.selectedStrip = len(fsp.strips) - 1
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)
	selectionLd := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(selectionLd)

	// Draw from the bottom
	scrollOffset := fsp.scrollbar.Offset()
	y := stripHeight - 1 - vpad
	for i := scrollOffset; i < min(len(fsp.strips), visibleStrips+scrollOffset+1); i++ {
		callsign := fsp.strips[i]
		strip := sim.GetFlightStrip(callsign)
		ac := sim.GetAircraft(callsign)
		if ac == nil {
			lg.Errorf("%s: no aircraft for callsign?!", strip.callsign)
			continue
		}
		fp := ac.FlightPlan

		style := TextStyle{Font: fsp.font, Color: RGB{.1, .1, .1}}

		// Draw background quad for this flight strip
		qb := GetColoredTrianglesDrawBuilder()
		defer ReturnColoredTrianglesDrawBuilder(qb)
		bgColor := func() RGB {
			/*if fsp.isDeparture(ac) {
				return ctx.cs.DepartureStrip
			} else {
				return ctx.cs.ArrivalStrip
			}*/
			return RGB{.9, .9, .85}
		}()
		y0, y1 := y+1+vpad-stripHeight, y+1+vpad
		qb.AddQuad([2]float32{0, y0}, [2]float32{drawWidth, y0}, [2]float32{drawWidth, y1}, [2]float32{0, y1}, bgColor)
		qb.GenerateCommands(cb)

		x := indent

		// First column; 3 entries
		td.AddText(callsign, [2]float32{x, y}, style)
		if fp != nil {
			td.AddText(fp.AircraftType, [2]float32{x, y - fh*3/2}, style)
			td.AddText(fp.Rules.String(), [2]float32{x, y - fh*3}, style)
		}
		ld.AddLine([2]float32{width0, y}, [2]float32{width0, y - stripHeight})

		// Second column; 3 entries
		x += width0
		td.AddText(ac.AssignedSquawk.String(), [2]float32{x, y}, style)
		td.AddText(fmt.Sprintf("%d", ac.TempAltitude), [2]float32{x, y - fh*3/2}, style)
		if fp != nil {
			td.AddText(fmt.Sprintf("%d", fp.Altitude), [2]float32{x, y - fh*3}, style)
		}
		ld.AddLine([2]float32{width0, y - 4./3.*fh}, [2]float32{width0 + width1, y - 4./3.*fh})
		ld.AddLine([2]float32{width0, y - 8./3.*fh}, [2]float32{width0 + width1, y - 8./3.*fh})
		ld.AddLine([2]float32{width0 + width1, y}, [2]float32{width0 + width1, y - stripHeight})

		// Third column; (up to) 4 entries
		x += width1
		if fp != nil {
			td.AddText(fp.DepartureAirport, [2]float32{x, y}, style)
			td.AddText(fp.ArrivalAirport, [2]float32{x, y - fh}, style)
			td.AddText(fp.AlternateAirport, [2]float32{x, y - 2*fh}, style)
		}
		td.AddText(ac.Scratchpad, [2]float32{x, y - 3*fh}, style)
		ld.AddLine([2]float32{width0 + width1 + width2, y},
			[2]float32{width0 + width1 + width2, y - stripHeight})

		// Fourth column: route and remarks
		x += width2
		if fp != nil {
			cols := int(widthCenter / fw)
			// Line-wrap the route to fit the box and break it into lines.
			route, _ := wrapText(fp.Route, cols, 2 /* indent */, true)
			text := strings.Split(route, "\n")
			// Add a blank line if the route only used one line.
			if len(text) < 2 {
				text = append(text, "")
			}
			// Similarly for the remarks
			remarks, _ := wrapText(fp.Remarks, cols, 2 /* indent */, true)
			text = append(text, strings.Split(remarks, "\n")...)
			// Limit to the first four lines so we don't spill over.
			if len(text) > 4 {
				text = text[:4]
			}
			// Truncate all lines to the column limit; wrapText() lets things
			// spill over if it's unable to break a long word by itself on a
			// line, for example.
			for i, line := range text {
				if len(line) > cols {
					text[i] = text[i][:cols]
				}
			}
			td.AddText(strings.Join(text, "\n"), [2]float32{x, y}, style)
		}

		// Annotations
		x += widthCenter
		var editResult int
		for ai, ann := range strip.annotations {
			ix, iy := ai%3, ai/3
			xp, yp := x+float32(ix)*widthAnn+indent, y-float32(iy)*1.5*fh

			if ctx.haveFocus && fsp.selectedStrip == i && ai == fsp.selectedAnnotation {
				// If were currently editing this annotation, don't draw it
				// normally but instead draw it including a cursor, update
				// it according to keyboard input, etc.
				cursorStyle := TextStyle{Font: fsp.font, Color: bgColor,
					DrawBackground: true, BackgroundColor: style.Color}
				editResult, _ = uiDrawTextEdit(&strip.annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
					ctx.keyboard, [2]float32{xp, yp}, style, cursorStyle, cb)
				if len(strip.annotations[fsp.selectedAnnotation]) >= 3 {
					// Limit it to three characters
					strip.annotations[fsp.selectedAnnotation] = strip.annotations[fsp.selectedAnnotation][:3]
					fsp.annotationCursorPos = min(fsp.annotationCursorPos, len(strip.annotations[fsp.selectedAnnotation]))
				}
			} else {
				td.AddText(ann, [2]float32{xp, yp}, style)
			}
		}

		// Only process this after drawing all of the annotations since
		// otherwise we can end up with cascading tabbing ahead and the
		// like.
		switch editResult {
		case TextEditReturnNone, TextEditReturnTextChanged:
			// nothing to do
		case TextEditReturnEnter:
			fsp.selectedStrip = -1
			wmReleaseKeyboardFocus()
		case TextEditReturnNext:
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 1) % 9
			fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
		case TextEditReturnPrev:
			// +8 rather than -1 to keep it positive for the mod...
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 8) % 9
			fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
		}

		// Horizontal lines
		ld.AddLine([2]float32{x, y - 4./3.*fh}, [2]float32{drawWidth, y - 4./3.*fh})
		ld.AddLine([2]float32{x, y - 8./3.*fh}, [2]float32{drawWidth, y - 8./3.*fh})
		// Vertical lines
		for i := 0; i < 3; i++ {
			xp := x + float32(i)*widthAnn
			ld.AddLine([2]float32{xp, y}, [2]float32{xp, y - stripHeight})
		}

		// Line at the top
		yl := y + 1 + vpad
		ld.AddLine([2]float32{0, yl}, [2]float32{drawWidth, yl})

		y += stripHeight
	}

	// Handle selection, deletion, and reordering
	if ctx.mouse != nil {
		// Ignore clicks if the mouse is over the scrollbar (and it's being drawn)
		if ctx.mouse.Clicked[MouseButtonPrimary] && ctx.mouse.Pos[0] <= drawWidth {
			// from the bottom
			stripIndex := int(ctx.mouse.Pos[1] / stripHeight)
			stripIndex += scrollOffset
			if stripIndex < len(fsp.strips) {
				io := imgui.CurrentIO()
				if io.KeyShiftPressed() {
					// delete the flight strip
					copy(fsp.strips[stripIndex:], fsp.strips[stripIndex+1:])
					fsp.strips = fsp.strips[:len(fsp.strips)-1]
				} else {
					// select the aircraft
					callsign := fsp.strips[stripIndex]
					fsp.selectedAircraft = sim.GetAircraft(callsign)
				}
			}
		}
		if ctx.mouse.Dragging[MouseButtonPrimary] {
			fsp.mouseDragging = true
			fsp.lastMousePos = ctx.mouse.Pos

			// Offset so that the selection region is centered over the
			// line between two strips; the index then is to the lower one.
			splitIndex := int(ctx.mouse.Pos[1]/stripHeight + 0.5)
			yl := float32(splitIndex) * stripHeight
			selectionLd.AddLine([2]float32{0, yl}, [2]float32{drawWidth, yl})
		}
	}
	if fsp.mouseDragging && (ctx.mouse == nil || !ctx.mouse.Dragging[MouseButtonPrimary]) {
		fsp.mouseDragging = false

		if fsp.selectedAircraft == nil {
			lg.Printf("No selected aircraft for flight strip drag?!")
		} else {
			// Figure out the index for the selected aircraft.
			selectedIndex := func() int {
				callsign := fsp.selectedAircraft.Callsign
				for i, fs := range fsp.strips {
					if fs == callsign {
						return i
					}
				}
				lg.Printf("Couldn't find %s in flight strips?!", callsign)
				return -1
			}()

			// The selected aircraft was set from the original mouse down so
			// now we just need to move it to be in the right place given where
			// the button was released.
			destinationIndex := int(fsp.lastMousePos[1]/stripHeight + 0.5)
			destinationIndex += scrollOffset
			destinationIndex = clamp(destinationIndex, 0, len(fsp.strips))

			if selectedIndex != -1 && selectedIndex != destinationIndex {
				// First remove it from the slice
				fs := fsp.strips[selectedIndex]
				fsp.strips = append(fsp.strips[:selectedIndex], fsp.strips[selectedIndex+1:]...)

				if selectedIndex < destinationIndex {
					destinationIndex--
				}

				// And stuff it in there
				fin := fsp.strips[destinationIndex:]
				fsp.strips = append([]string{}, fsp.strips[:destinationIndex]...)
				fsp.strips = append(fsp.strips, fs)
				fsp.strips = append(fsp.strips, fin...)
			}
		}
	}
	// Take focus if the user clicks in the annotations
	/*
		if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
			annotationStartX := drawWidth - 3*widthAnn
			if xp := ctx.mouse.Pos[0]; xp >= annotationStartX && xp < drawWidth {
				stripIndex := int(ctx.mouse.Pos[1]/stripHeight) + scrollOffset
				if stripIndex < len(fsp.strips) {
					wmTakeKeyboardFocus(fsp, true)
					fsp.selectedStrip = stripIndex

					// Figure out which annotation was selected
					xa := int(ctx.mouse.Pos[0]-annotationStartX) / int(widthAnn)
					ya := 2 - (int(ctx.mouse.Pos[1])%int(stripHeight))/(int(stripHeight)/3)
					xa, ya = clamp(xa, 0, 2), clamp(ya, 0, 2) // just in case
					fsp.selectedAnnotation = 3*ya + xa

					callsign := fsp.strips[fsp.selectedStrip]
					strip := sim.GetFlightStrip(callsign)
					fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
				}
			}
		}
	*/
	fsp.scrollbar.Draw(ctx, cb)

	cb.SetRGB(UIControlColor)
	cb.LineWidth(1)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	cb.SetRGB(UITextHighlightColor)
	cb.LineWidth(3)
	selectionLd.GenerateCommands(cb)
}

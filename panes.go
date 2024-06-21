// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Activate(w *World, r Renderer, eventStream *EventStream)
	Deactivate()
	ResetWorld(w *World)

	CanTakeKeyboardFocus() bool

	Draw(ctx *PaneContext, cb *CommandBuffer)
}

type PaneUIDrawer interface {
	DrawUI()
}

type PaneUpgrader interface {
	Upgrade(prev, current int)
}

type PaneContext struct {
	paneExtent       Extent2D
	parentPaneExtent Extent2D

	platform  Platform
	renderer  Renderer
	world     *World
	mouse     *MouseState
	keyboard  *KeyboardState
	haveFocus bool
	now       time.Time
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

func (ms *MouseState) SetCursor(id imgui.MouseCursorID) {
	imgui.SetMouseCursor(id)
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

func NewKeyboardState(p Platform) *KeyboardState {
	keyboard := &KeyboardState{Pressed: make(map[Key]interface{})}

	keyboard.Input = p.InputCharacters()

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

	case "*main.MessagesPane":
		return unmarshalPaneHelper[*MessagesPane](data)

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

func (ep *EmptyPane) Activate(*World, Renderer, *EventStream) {}
func (ep *EmptyPane) Deactivate()                             {}
func (ep *EmptyPane) ResetWorld(w *World)                     {}
func (ep *EmptyPane) CanTakeKeyboardFocus() bool              { return false }

func (ep *EmptyPane) Name() string { return "(Empty)" }

func (ep *EmptyPane) Draw(ctx *PaneContext, cb *CommandBuffer) {}

///////////////////////////////////////////////////////////////////////////
// FlightStripPane

type FlightStripPane struct {
	FontSize int
	font     *Font

	HideFlightStrips          bool
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

	events    *EventsSubscription
	scrollbar *ScrollBar

	selectedAircraft string
}

func NewFlightStripPane() *FlightStripPane {
	return &FlightStripPane{
		AddPushed:          true,
		FontSize:           12,
		selectedStrip:      -1,
		selectedAnnotation: -1,
	}
}

func (fsp *FlightStripPane) Activate(w *World, r Renderer, eventStream *EventStream) {
	if fsp.FontSize == 0 {
		fsp.FontSize = 12
	}
	if fsp.font = GetFont(FontIdentifier{Name: "Flight Strip Printer", Size: fsp.FontSize}); fsp.font == nil {
		fsp.font = GetDefaultFont()
	}
	if fsp.addedAircraft == nil {
		fsp.addedAircraft = make(map[string]interface{})
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewVerticalScrollBar(4, true)
	}
	fsp.events = eventStream.Subscribe()

	if w != nil {
		for _, ac := range w.GetAllAircraft() {
			if fsp.AutoAddTracked && ac.TrackingController == w.Callsign && ac.FlightPlan != nil {
				fsp.strips = append(fsp.strips, ac.Callsign)
				fsp.addedAircraft[ac.Callsign] = nil
			} else if ac.TrackingController == "" &&
				((fsp.AutoAddDepartures && ac.IsDeparture()) || (fsp.AutoAddArrivals && !ac.IsDeparture())) {
				fsp.strips = append(fsp.strips, ac.Callsign)
				fsp.addedAircraft[ac.Callsign] = nil
			}
		}
	}
}

func (fsp *FlightStripPane) Deactivate() {
	fsp.events.Unsubscribe()
	fsp.events = nil
}

func (fsp *FlightStripPane) ResetWorld(w *World) {
	fsp.strips = nil
	fsp.addedAircraft = make(map[string]interface{})
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return false /*true*/ }

func (fsp *FlightStripPane) processEvents(w *World) {
	possiblyAdd := func(ac *Aircraft) {
		if _, ok := fsp.addedAircraft[ac.Callsign]; ok {
			return
		}

		if ac.FlightPlan == nil {
			return
		}

		fsp.strips = append(fsp.strips, ac.Callsign)
		fsp.addedAircraft[ac.Callsign] = nil
	}

	// First account for changes in world.Aircraft
	// Added aircraft
	for _, ac := range w.Aircraft {
		if fsp.AutoAddTracked && ac.TrackingController == w.Callsign {
			possiblyAdd(ac)
		} else if ac.TrackingController == "" &&
			((fsp.AutoAddDepartures && ac.IsDeparture()) || (fsp.AutoAddArrivals && !ac.IsDeparture())) {
			possiblyAdd(ac)
		}
	}
	// Removed aircraft
	fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool {
		_, ok := w.Aircraft[callsign]
		return ok
	})

	remove := func(c string) {
		fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool { return callsign != c })
		if fsp.selectedAircraft == c {
			fsp.selectedAircraft = ""
		}
	}

	for _, event := range fsp.events.Get() {
		switch event.Type {
		case PushedFlightStripEvent:
			if ac, ok := w.Aircraft[event.Callsign]; ok && fsp.AddPushed {
				possiblyAdd(ac)
			}

		case InitiatedTrackEvent:
			if ac, ok := w.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddTracked && ac.TrackingController == w.Callsign {
					possiblyAdd(ac)
				}
			}

		case DroppedTrackEvent:
			if fsp.AutoRemoveDropped {
				remove(event.Callsign)
			}

		case AcceptedHandoffEvent, AcceptedRedirectedHandoffEvent:
			if ac, ok := w.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddAcceptedHandoffs && ac.TrackingController == w.Callsign {
					possiblyAdd(ac)
				}
			}

		case HandoffControllEvent:
			if ac, ok := w.Aircraft[event.Callsign]; ok {
				if fsp.AutoRemoveHandoffs && ac.TrackingController != w.Callsign {
					remove(event.Callsign)
				}
			}
		}
	}

	// TODO: is this needed? Shouldn't there be a RemovedAircraftEvent?
	fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool {
		ac := w.GetAircraft(callsign, false)
		return ac != nil
	})

	if fsp.CollectDeparturesArrivals {
		isDeparture := func(callsign string) bool {
			ac := w.GetAircraft(callsign, false)
			return ac != nil && ac.IsDeparture()
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
	show := !fsp.HideFlightStrips
	imgui.Checkbox("Show flight strips", &show)
	fsp.HideFlightStrips = !show

	uiStartDisable(fsp.HideFlightStrips)
	imgui.Checkbox("Automatically add departures", &fsp.AutoAddDepartures)
	imgui.Checkbox("Automatically add arrivals", &fsp.AutoAddArrivals)
	imgui.Checkbox("Add pushed flight strips", &fsp.AddPushed)
	imgui.Checkbox("Automatically add when track is initiated", &fsp.AutoAddTracked)
	imgui.Checkbox("Automatically add handoffs", &fsp.AutoAddAcceptedHandoffs)
	imgui.Checkbox("Automatically remove dropped tracks", &fsp.AutoRemoveDropped)
	imgui.Checkbox("Automatically remove accepted handoffs", &fsp.AutoRemoveHandoffs)

	imgui.Checkbox("Collect departures and arrivals together", &fsp.CollectDeparturesArrivals)

	id := FontIdentifier{Name: fsp.font.id.Name, Size: fsp.FontSize}
	if newFont, changed := DrawFontSizeSelector(&id); changed {
		fsp.FontSize = newFont.size
		fsp.font = newFont
	}
	uiEndDisable(fsp.HideFlightStrips)
}

func (fsp *FlightStripPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	fsp.processEvents(ctx.world)

	// Font width and height
	// the 'Flight Strip Printer' font seems to have an unusually thin space,
	// so instead use 'X' to get the expected per-character width for layout.
	bx, _ := fsp.font.BoundText("X", 0)
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
		widthCenter -= float32(fsp.scrollbar.PixelExtent())
	}
	if widthCenter < 0 {
		// not sure what to do if it comes to this...
		widthCenter = 20 * fw
	}

	drawWidth := ctx.paneExtent.Width()
	if fsp.scrollbar.Visible() {
		drawWidth -= float32(fsp.scrollbar.PixelExtent())
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
	trid := GetTrianglesDrawBuilder()
	defer ReturnTrianglesDrawBuilder(trid)

	// Draw from the bottom
	scrollOffset := fsp.scrollbar.Offset()
	y := stripHeight - 1 - vpad
	for i := scrollOffset; i < min(len(fsp.strips), visibleStrips+scrollOffset+1); i++ {
		callsign := fsp.strips[i]
		strip := ctx.world.GetFlightStrip(callsign)
		ac := ctx.world.GetAircraft(callsign, false)
		if ac == nil {
			lg.Errorf("%s: no aircraft for callsign?!", strip.Callsign)
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
		td.AddText(ac.FlightPlan.AssignedSquawk.String(), [2]float32{x, y}, style)
		td.AddText(strconv.Itoa(ac.TempAltitude), [2]float32{x, y - fh*3/2}, style)
		if fp != nil {
			td.AddText(strconv.Itoa(fp.Altitude), [2]float32{x, y - fh*3}, style)
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
		for ai, ann := range strip.Annotations {
			ix, iy := ai%3, ai/3
			xp, yp := x+float32(ix)*widthAnn+indent, y-float32(iy)*1.5*fh

			if ctx.haveFocus && fsp.selectedStrip == i && ai == fsp.selectedAnnotation {
				// If were currently editing this annotation, don't draw it
				// normally but instead draw it including a cursor, update
				// it according to keyboard input, etc.
				cursorStyle := TextStyle{Font: fsp.font, Color: bgColor,
					DrawBackground: true, BackgroundColor: style.Color}
				editResult, _ = uiDrawTextEdit(&strip.Annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
					ctx.keyboard, [2]float32{xp, yp}, style, cursorStyle, cb)
				if len(strip.Annotations[fsp.selectedAnnotation]) >= 3 {
					// Limit it to three characters
					strip.Annotations[fsp.selectedAnnotation] = strip.Annotations[fsp.selectedAnnotation][:3]
					fsp.annotationCursorPos = min(fsp.annotationCursorPos, len(strip.Annotations[fsp.selectedAnnotation]))
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
			fsp.annotationCursorPos = len(strip.Annotations[fsp.selectedAnnotation])
		case TextEditReturnPrev:
			// +8 rather than -1 to keep it positive for the mod...
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 8) % 9
			fsp.annotationCursorPos = len(strip.Annotations[fsp.selectedAnnotation])
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
					fsp.selectedAircraft = callsign
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
			trid.AddQuad([2]float32{0, yl - 1}, [2]float32{drawWidth, yl - 1},
				[2]float32{drawWidth, yl + 1}, [2]float32{0, yl + 1})
		}
	}
	if fsp.mouseDragging && (ctx.mouse == nil || !ctx.mouse.Dragging[MouseButtonPrimary]) {
		fsp.mouseDragging = false

		if fsp.selectedAircraft == "" {
			lg.Debug("No selected aircraft for flight strip drag?!")
		} else {
			// Figure out the index for the selected aircraft.
			selectedIndex := func() int {
				for i, fs := range fsp.strips {
					if fs == fsp.selectedAircraft {
						return i
					}
				}
				lg.Warnf("Couldn't find %s in flight strips?!", fsp.selectedAircraft)
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
					strip := ctx.world.GetFlightStrip(callsign)
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
	trid.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// MessagesPane

type Message struct {
	contents string
	system   bool
	error    bool
	global   bool
}

type CLIInput struct {
	cmd    string
	cursor int
}

type MessagesPane struct {
	FontIdentifier FontIdentifier
	font           *Font
	scrollbar      *ScrollBar
	events         *EventsSubscription
	messages       []Message
	NextController map[string]string 

	// Command-input-related
	input         CLIInput
	history       []CLIInput
	historyOffset int // for up arrow / downarrow. Note: counts from the end! 0 when not in history
	savedInput    CLIInput
}

func NewMessagesPane() *MessagesPane {
	return &MessagesPane{
		FontIdentifier: FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 16},
	}
}

func (mp *MessagesPane) Name() string { return "Messages" }

func (mp *MessagesPane) Activate(w *World, r Renderer, eventStream *EventStream) {
	if mp.font = GetFont(mp.FontIdentifier); mp.font == nil {
		mp.font = GetDefaultFont()
		mp.FontIdentifier = mp.font.id
	}
	if mp.scrollbar == nil {
		mp.scrollbar = NewVerticalScrollBar(4, true)
	}
	mp.events = eventStream.Subscribe()
}

func (mp *MessagesPane) Deactivate() {
	mp.events.Unsubscribe()
	mp.events = nil
}

func (mp *MessagesPane) ResetWorld(w *World) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return true }

func (mp *MessagesPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}
}

func (mp *MessagesPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	mp.processEvents(ctx.world)

	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		wmTakeKeyboardFocus(mp, false)
	}
	mp.processKeyboard(ctx)

	nLines := len(mp.messages) + 1 /* prompt */
	lineHeight := float32(mp.font.size + 1)
	visibleLines := int(ctx.paneExtent.Height() / lineHeight)
	mp.scrollbar.Update(nLines, visibleLines, ctx)

	drawWidth := ctx.paneExtent.Width()
	if mp.scrollbar.Visible() {
		drawWidth -= float32(mp.scrollbar.PixelExtent())
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	indent := float32(2)

	scrollOffset := mp.scrollbar.Offset()
	y := lineHeight

	// Draw the prompt and any input text
	cliStyle := TextStyle{Font: mp.font, Color: RGB{1, 1, .2}}
	cursorStyle := TextStyle{Font: mp.font, LineSpacing: 0,
		Color: RGB{1, 1, .2}, DrawBackground: true, BackgroundColor: RGB{1, 1, 1}}
	ci := mp.input

	prompt := "> "
	if !ctx.haveFocus {
		// Don't draw the cursor if we don't have keyboard focus
		td.AddText(prompt+ci.cmd, [2]float32{indent, y}, cliStyle)
	} else if ci.cursor == len(ci.cmd) {
		// cursor at the end
		td.AddTextMulti([]string{prompt + string(ci.cmd), " "}, [2]float32{indent, y},
			[]TextStyle{cliStyle, cursorStyle})
	} else {
		// cursor in the middle
		sb := prompt + ci.cmd[:ci.cursor]
		sc := ci.cmd[ci.cursor : ci.cursor+1]
		se := ci.cmd[ci.cursor+1:]
		styles := []TextStyle{cliStyle, cursorStyle, cliStyle}
		td.AddTextMulti([]string{sb, sc, se}, [2]float32{indent, y}, styles)
	}
	y += lineHeight

	for i := scrollOffset; i < min(len(mp.messages), visibleLines+scrollOffset+1); i++ {
		// TODO? wrap text
		msg := mp.messages[len(mp.messages)-1-i]

		s := TextStyle{Font: mp.font, Color: msg.Color()}
		td.AddText(msg.contents, [2]float32{indent, y}, s)
		y += lineHeight
	}

	ctx.SetWindowCoordinateMatrices(cb)
	if ctx.haveFocus {
		// Yellow border around the edges
		ld := GetLinesDrawBuilder()
		defer ReturnLinesDrawBuilder(ld)

		w, h := ctx.paneExtent.Width(), ctx.paneExtent.Height()
		ld.AddLineLoop([][2]float32{{0, 0}, {w, 0}, {w, h}, {0, h}})
		cb.SetRGB(RGB{1, 1, 0}) // yellow
		ld.GenerateCommands(cb)
	}
	mp.scrollbar.Draw(ctx, cb)
	td.GenerateCommands(cb)
}

func (mp *MessagesPane) processKeyboard(ctx *PaneContext) {
	if ctx.keyboard == nil || !ctx.haveFocus {
		return
	}

	if ctx.keyboard.IsPressed(KeyTab) {
		// focus back to the STARS Pane (assume just one...)
		globalConfig.DisplayRoot.VisitPanes(func(pane Pane) {
			if sp, ok := pane.(*STARSPane); ok {
				wmTakeKeyboardFocus(sp, false)
				delete(ctx.keyboard.Pressed, KeyTab) // prevent cycling back and forth
			}
		})
	}

	// Grab keyboard input
	if len(mp.input.cmd) > 0 && mp.input.cmd[0] == '/' {
		mp.input.InsertAtCursor(ctx.keyboard.Input)
	} else {
		mp.input.InsertAtCursor(strings.ToUpper(ctx.keyboard.Input))
	}

	if ctx.keyboard.IsPressed(KeyUpArrow) {
		if mp.historyOffset < len(mp.history) {
			if mp.historyOffset == 0 {
				mp.savedInput = mp.input // save current input in case we return
			}
			mp.historyOffset++
			mp.input = mp.history[len(mp.history)-mp.historyOffset]
			mp.input.cursor = len(mp.input.cmd)
		}
	}
	if ctx.keyboard.IsPressed(KeyDownArrow) {
		if mp.historyOffset > 0 {
			mp.historyOffset--
			if mp.historyOffset == 0 {
				mp.input = mp.savedInput
				mp.savedInput = CLIInput{}
			} else {
				mp.input = mp.history[len(mp.history)-mp.historyOffset]
			}
			mp.input.cursor = len(mp.input.cmd)
		}
	}

	if ctx.keyboard.IsPressed(KeyLeftArrow) {
		if mp.input.cursor > 0 {
			mp.input.cursor--
		}
	}
	if ctx.keyboard.IsPressed(KeyRightArrow) {
		if mp.input.cursor < len(mp.input.cmd) {
			mp.input.cursor++
		}
	}
	if ctx.keyboard.IsPressed(KeyHome) {
		mp.input.cursor = 0
	}
	if ctx.keyboard.IsPressed(KeyEnd) {
		mp.input.cursor = len(mp.input.cmd)
	}
	if ctx.keyboard.IsPressed(KeyBackspace) {
		mp.input.DeleteBeforeCursor()
	}
	if ctx.keyboard.IsPressed(KeyDelete) {
		mp.input.DeleteAfterCursor()
	}
	if ctx.keyboard.IsPressed(KeyEscape) {
		if mp.input.cursor > 0 {
			mp.input = CLIInput{}
		}
	}

	if ctx.keyboard.IsPressed(KeyEnter) && strings.TrimSpace(mp.input.cmd) != "" {
		mp.runCommands(ctx.world)
	}
}

func (msg *Message) Color() RGB {
	switch {
	case msg.error:
		return RGB{.9, .1, .1}
	case msg.global:
		return RGB{0.012, 0.78, 0.016}
	default:
		return RGB{1, 1, 1}
	}
}

func (mp *MessagesPane) runCommands(w *World) {
	mp.input.cmd = strings.TrimSpace(mp.input.cmd)

	if mp.input.cmd[0] == '/' {
		w.SendGlobalMessage(GlobalMessage{
			FromController: w.Callsign,
			Message:        w.Callsign + ": " + mp.input.cmd[1:],
		})
		mp.messages = append(mp.messages, Message{contents: w.Callsign + ": " + mp.input.cmd[1:], global: true})
		mp.history = append(mp.history, mp.input)
		mp.input = CLIInput{}
		return
	}

	callsign, cmd, ok := strings.Cut(mp.input.cmd, " ")
	mp.messages = append(mp.messages, Message{contents: "> " + mp.input.cmd})
	mp.history = append(mp.history, mp.input)
	mp.input = CLIInput{}

	if ok {
		if ac := w.GetAircraft(callsign, true /*abbreviated*/); ac != nil {
			w.RunAircraftCommands(ac.Callsign, cmd,mp.NextController[ac.Callsign], func(errorString string, remainingCommands string) {
				if errorString != "" {
					mp.messages = append(mp.messages, Message{contents: errorString, error: true})
				}
				if remainingCommands != "" && mp.input.cmd == "" {
					mp.input.cmd = callsign + " " + remainingCommands
					mp.input.cursor = len(mp.input.cmd)
				}
			})
		} else {
			mp.messages = append(mp.messages, Message{contents: callsign + ": no such aircraft", error: true})
		}
	} else {
		mp.messages = append(mp.messages, Message{contents: "invalid command: " + callsign, error: true})
	}
}

func (ci *CLIInput) InsertAtCursor(s string) {
	if len(s) == 0 {
		return
	}

	ci.cmd = ci.cmd[:ci.cursor] + s + ci.cmd[ci.cursor:]

	// place cursor after the inserted text
	ci.cursor += len(s)
}

func (ci *CLIInput) DeleteBeforeCursor() {
	if ci.cursor > 0 {
		ci.cmd = ci.cmd[:ci.cursor-1] + ci.cmd[ci.cursor:]
		ci.cursor--
	}
}

func (ci *CLIInput) DeleteAfterCursor() {
	if ci.cursor < len(ci.cmd) {
		ci.cmd = ci.cmd[:ci.cursor] + ci.cmd[ci.cursor+1:]
	}
}

func (mp *MessagesPane) processEvents(w *World) {
	lastRadioCallsign := ""
	var lastRadioType RadioTransmissionType
	var unexpectedTransmission bool
	var transmissions []string

	addTransmissions := func() {
		// Split the callsign into the ICAO and the flight number
		// Note: this is buggy if we process multiple senders in a
		// single call here, but that shouldn't happen...
		callsign := lastRadioCallsign
		radioCallsign := lastRadioCallsign
		if idx := strings.IndexAny(callsign, "0123456789"); idx != -1 {
			// Try to get the telephony.
			icao, flight := callsign[:idx], callsign[idx:]
			if telephony, ok := database.Callsigns[icao]; ok {
				radioCallsign = telephony + " " + flight
				if ac := w.GetAircraft(callsign, false); ac != nil {
					if fp := ac.FlightPlan; fp != nil {
						if strings.HasPrefix(fp.AircraftType, "H/") {
							radioCallsign += " heavy"
						} else if strings.HasPrefix(fp.AircraftType, "J/") || strings.HasPrefix(fp.AircraftType, "S/") {
							radioCallsign += " super"
						}
					}
				}
			}
		}

		response := strings.Join(transmissions, ", ")
		var msg Message
		if lastRadioType == RadioTransmissionContact {
			ctrl := w.Controllers[w.Callsign]
			fullName := ctrl.FullName
			if ac := w.Aircraft[callsign]; ac != nil && ac.IsDeparture() {
				// Always refer to the controller as "departure" for departing aircraft.
				fullName = strings.ReplaceAll(fullName, "approach", "departure")
			}
			msg = Message{contents: fullName + ", " + radioCallsign + ", " + response}
		} else {
			if len(response) > 0 {
				response = strings.ToUpper(response[:1]) + response[1:]
			}
			msg = Message{contents: response + ". " + radioCallsign, error: unexpectedTransmission}
		}
		lg.Debug("radio_transmission", slog.String("callsign", callsign), slog.Any("message", msg))
		mp.messages = append(mp.messages, msg)
	}

	for _, event := range mp.events.Get() {
		switch event.Type {
		case RadioTransmissionEvent:
			if event.ToController == w.Callsign {
				if event.Callsign != lastRadioCallsign || event.RadioTransmissionType != lastRadioType {
					if len(transmissions) > 0 {
						addTransmissions()
						transmissions = nil
						unexpectedTransmission = false
					}
					lastRadioCallsign = event.Callsign
					lastRadioType = event.RadioTransmissionType
				}
				transmissions = append(transmissions, event.Message)
				unexpectedTransmission = unexpectedTransmission || (event.RadioTransmissionType == RadioTransmissionUnexpected)
			}
		case GlobalMessageEvent:
			if event.FromController != w.Callsign {
				mp.messages = append(mp.messages, Message{contents: event.Message, global: true})
			}
		case StatusMessageEvent:
			// Don't spam the same message repeatedly; look in the most recent 5.
			n := len(mp.messages)
			start := max(0, n-5)
			if !slices.ContainsFunc(mp.messages[start:],
				func(m Message) bool { return m.contents == event.Message }) {
				mp.messages = append(mp.messages,
					Message{
						contents: event.Message,
						system:   true,
					})
			}

		case TrackClickedEvent:
			if cmd := strings.TrimSpace(mp.input.cmd); cmd != "" {
				mp.input.cmd = event.Callsign + " " + cmd
				mp.runCommands(w)
			}
		case AcceptedHandoffEvent, AcceptedRedirectedHandoffEvent:
			if ac, ok := w.Aircraft[event.Callsign]; ok && event.FromController == w.Callsign{
				if mp.NextController == nil {
					mp.NextController = make(map[string]string)
				}
				mp.NextController[ac.Callsign] = event.ToController
				fmt.Printf("%v: setting next controller for %v to be %v.\n", w.Callsign, ac.Callsign, event.ToController)
			}
		}
	}

	if len(transmissions) > 0 {
		addTransmissions()
	}
}

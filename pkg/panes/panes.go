// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Activate(ss *sim.State, r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream,
		lg *log.Logger)
	Deactivate()
	Reset(ss sim.State, lg *log.Logger)

	CanTakeKeyboardFocus() bool

	Draw(ctx *Context, cb *renderer.CommandBuffer)
}

type UIDrawer interface {
	DrawUI(p platform.Platform, config *platform.Config)
}

type KeyboardFocus interface {
	Take(p Pane)
	TakeTemporary(p Pane)
	Release()
	Current() Pane
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

	Platform  platform.Platform
	Renderer  renderer.Renderer
	Mouse     *platform.MouseState
	Keyboard  *platform.KeyboardState
	HaveFocus bool
	Now       time.Time
	Lg        *log.Logger

	MenuBarHeight float32
	AudioEnabled  *bool

	KeyboardFocus KeyboardFocus

	ControlClient *sim.ControlClient
}

func (ctx *Context) InitializeMouse(fullDisplayExtent math.Extent2D, p platform.Platform) {
	ctx.Mouse = p.GetMouse()

	// Convert to pane coordinates:
	// platform gives us the mouse position w.r.t. the full window, so we need
	// to subtract out displayExtent.p0 to get coordinates w.r.t. the
	// current pane.  Further, it has (0,0) in the upper left corner of the
	// window, so we need to flip y w.r.t. the full window resolution.
	ctx.Mouse.Pos[0] = ctx.Mouse.Pos[0] - ctx.PaneExtent.P0[0]
	ctx.Mouse.Pos[1] = fullDisplayExtent.P1[1] - 1 - ctx.PaneExtent.P0[1] - ctx.Mouse.Pos[1]

	// Negate y to go to pane coordinates
	ctx.Mouse.Wheel[1] *= -1
	ctx.Mouse.DragDelta[1] *= -1
}

func (ctx *Context) SetWindowCoordinateMatrices(cb *renderer.CommandBuffer) {
	w := float32(int(ctx.PaneExtent.Width() + 0.5))
	h := float32(int(ctx.PaneExtent.Height() + 0.5))
	cb.LoadProjectionMatrix(math.Identity3x3().Ortho(0, w, 0, h))
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// Helper function to unmarshal the JSON of a Pane of a given type T.
func unmarshalPaneHelper[T Pane](data []byte) (Pane, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

func UnmarshalPane(paneType string, data []byte) (Pane, error) {
	switch paneType {
	case "":
		// nil pane
		return nil, nil

	case "*main.EmptyPane", "*panes.EmptyPane":
		return unmarshalPaneHelper[*EmptyPane](data)

	case "*main.FlightStripPane", "*panes.FlightStripPane":
		return unmarshalPaneHelper[*FlightStripPane](data)

	case "*main.MessagesPane", "*panes.MessagesPane":
		return unmarshalPaneHelper[*MessagesPane](data)

	case "*main.STARSPane", "*panes.STARSPane":
		return unmarshalPaneHelper[*STARSPane](data)

	default:
		return NewEmptyPane(), fmt.Errorf("%s: Unhandled type in config file", paneType)
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

func (ep *EmptyPane) Activate(*sim.State, renderer.Renderer, platform.Platform,
	*sim.EventStream, *log.Logger) {
}
func (ep *EmptyPane) Deactivate()                        {}
func (ep *EmptyPane) Reset(ss sim.State, lg *log.Logger) {}
func (ep *EmptyPane) CanTakeKeyboardFocus() bool         { return false }

func (ep *EmptyPane) Name() string { return "(Empty)" }

func (ep *EmptyPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {}

///////////////////////////////////////////////////////////////////////////
// FlightStripPane

type FlightStripPane struct {
	FontSize int
	font     *renderer.Font

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

	events    *sim.EventsSubscription
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

func (fsp *FlightStripPane) Activate(ss *sim.State, r renderer.Renderer, p platform.Platform,
	eventStream *sim.EventStream, lg *log.Logger) {
	if fsp.FontSize == 0 {
		fsp.FontSize = 12
	}
	if fsp.font = renderer.GetFont(renderer.FontIdentifier{Name: "Flight Strip Printer", Size: fsp.FontSize}); fsp.font == nil {
		fsp.font = renderer.GetDefaultFont()
	}
	if fsp.addedAircraft == nil {
		fsp.addedAircraft = make(map[string]interface{})
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewVerticalScrollBar(4, true)
	}
	fsp.events = eventStream.Subscribe()

	if ss != nil {
		for _, ac := range ss.Aircraft {
			if fsp.AutoAddTracked && ac.TrackingController == ss.Callsign && ac.FlightPlan != nil {
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

func (fsp *FlightStripPane) Reset(ss sim.State, lg *log.Logger) {
	fsp.strips = nil
	fsp.addedAircraft = make(map[string]interface{})
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return false /*true*/ }

func (fsp *FlightStripPane) processEvents(ctx *Context) {
	possiblyAdd := func(ac *av.Aircraft) {
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
	for _, ac := range ctx.ControlClient.Aircraft {
		if fsp.AutoAddTracked && ac.TrackingController == ctx.ControlClient.Callsign {
			possiblyAdd(ac)
		} else if ac.TrackingController == "" &&
			((fsp.AutoAddDepartures && ac.IsDeparture()) || (fsp.AutoAddArrivals && !ac.IsDeparture())) {
			possiblyAdd(ac)
		}
	}
	// Removed aircraft
	fsp.strips = util.FilterSlice(fsp.strips, func(callsign string) bool {
		_, ok := ctx.ControlClient.Aircraft[callsign]
		return ok
	})

	remove := func(c string) {
		fsp.strips = util.FilterSlice(fsp.strips, func(callsign string) bool { return callsign != c })
		if fsp.selectedAircraft == c {
			fsp.selectedAircraft = ""
		}
	}

	for _, event := range fsp.events.Get() {
		switch event.Type {
		case sim.PushedFlightStripEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok && fsp.AddPushed {
				possiblyAdd(ac)
			}
		case sim.InitiatedTrackEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddTracked && ac.TrackingController == ctx.ControlClient.Callsign {
					possiblyAdd(ac)
				}
			}
		case sim.DroppedTrackEvent:
			if fsp.AutoRemoveDropped {
				remove(event.Callsign)
			}
		case sim.AcceptedHandoffEvent, sim.AcceptedRedirectedHandoffEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddAcceptedHandoffs && ac.TrackingController == ctx.ControlClient.Callsign {
					possiblyAdd(ac)
				}
			}
		case sim.HandoffControllEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok {
				if fsp.AutoRemoveHandoffs && ac.TrackingController != ctx.ControlClient.Callsign {
					remove(event.Callsign)
				}
			}
		}
	}

	// TODO: is this needed? Shouldn't there be a RemovedAircraftEvent?
	fsp.strips = util.FilterSlice(fsp.strips, func(callsign string) bool {
		return ctx.ControlClient.Aircraft[callsign] != nil
	})

	if fsp.CollectDeparturesArrivals {
		isDeparture := func(callsign string) bool {
			ac := ctx.ControlClient.Aircraft[callsign]
			return ac != nil && ac.IsDeparture()
		}
		dep := util.FilterSlice(fsp.strips, isDeparture)
		arr := util.FilterSlice(fsp.strips, func(callsign string) bool { return !isDeparture(callsign) })

		fsp.strips = fsp.strips[:0]
		fsp.strips = append(fsp.strips, dep...)
		fsp.strips = append(fsp.strips, arr...)
	}
}

func (fsp *FlightStripPane) Name() string { return "Flight Strips" }

func (fsp *FlightStripPane) DrawUI(p platform.Platform, config *platform.Config) {
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

	id := renderer.FontIdentifier{Name: fsp.font.Id.Name, Size: fsp.FontSize}
	if newFont, changed := renderer.DrawFontSizeSelector(&id); changed {
		fsp.FontSize = newFont.Size
		fsp.font = newFont
	}
	uiEndDisable(fsp.HideFlightStrips)
}

func (fsp *FlightStripPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	fsp.processEvents(ctx)

	// Font width and height
	// the 'Flight Strip Printer' font seems to have an unusually thin space,
	// so instead use 'X' to get the expected per-character width for layout.
	bx, _ := fsp.font.BoundText("X", 0)
	fw, fh := float32(bx), float32(fsp.font.Size)

	ctx.SetWindowCoordinateMatrices(cb)

	// 4 lines of text, 2 lines on top and below for padding, 1 pixel separator line
	vpad := float32(2)
	stripHeight := 1 + 2*vpad + 4*fh

	visibleStrips := int(ctx.PaneExtent.Height() / stripHeight)
	fsp.scrollbar.Update(len(fsp.strips), visibleStrips, ctx)

	indent := float32(int32(fw / 2))
	// column widths
	width0 := 10 * fw
	width1 := 6 * fw
	width2 := 5 * fw
	widthAnn := 5 * fw

	widthCenter := ctx.PaneExtent.Width() - width0 - width1 - width2 - 3*widthAnn
	if fsp.scrollbar.Visible() {
		widthCenter -= float32(fsp.scrollbar.PixelExtent())
	}
	if widthCenter < 0 {
		// not sure what to do if it comes to this...
		widthCenter = 20 * fw
	}

	drawWidth := ctx.PaneExtent.Width()
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

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	// Draw from the bottom
	scrollOffset := fsp.scrollbar.Offset()
	y := stripHeight - 1 - vpad
	for i := scrollOffset; i < math.Min(len(fsp.strips), visibleStrips+scrollOffset+1); i++ {
		callsign := fsp.strips[i]
		strip := ctx.ControlClient.Aircraft[callsign].Strip
		ac := ctx.ControlClient.Aircraft[callsign]
		if ac == nil {
			ctx.Lg.Errorf("%s: no aircraft for callsign?!", strip.Callsign)
			continue
		}
		fp := ac.FlightPlan

		style := renderer.TextStyle{Font: fsp.font, Color: renderer.RGB{.1, .1, .1}}

		// Draw background quad for this flight strip
		qb := renderer.GetColoredTrianglesDrawBuilder()
		defer renderer.ReturnColoredTrianglesDrawBuilder(qb)
		bgColor := func() renderer.RGB {
			/*if fsp.isDeparture(ac) {
				return ctx.cs.DepartureStrip
			} else {
				return ctx.cs.ArrivalStrip
			}*/
			return renderer.RGB{.9, .9, .85}
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
			route, _ := util.WrapText(fp.Route, cols, 2 /* indent */, true)
			text := strings.Split(route, "\n")
			// Add a blank line if the route only used one line.
			if len(text) < 2 {
				text = append(text, "")
			}
			// Similarly for the remarks
			remarks, _ := util.WrapText(fp.Remarks, cols, 2 /* indent */, true)
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

			if ctx.HaveFocus && fsp.selectedStrip == i && ai == fsp.selectedAnnotation {
				// If were currently editing this annotation, don't draw it
				// normally but instead draw it including a cursor, update
				// it according to keyboard input, etc.
				cursorStyle := renderer.TextStyle{Font: fsp.font, Color: bgColor,
					DrawBackground: true, BackgroundColor: style.Color}
				editResult, _ = uiDrawTextEdit(&strip.Annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
					ctx.Keyboard, [2]float32{xp, yp}, style, cursorStyle, ctx.KeyboardFocus, cb)
				if len(strip.Annotations[fsp.selectedAnnotation]) >= 3 {
					// Limit it to three characters
					strip.Annotations[fsp.selectedAnnotation] = strip.Annotations[fsp.selectedAnnotation][:3]
					fsp.annotationCursorPos = math.Min(fsp.annotationCursorPos, len(strip.Annotations[fsp.selectedAnnotation]))
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
			ctx.KeyboardFocus.Release()
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
	if ctx.Mouse != nil {
		// Ignore clicks if the mouse is over the scrollbar (and it's being drawn)
		if ctx.Mouse.Clicked[platform.MouseButtonPrimary] && ctx.Mouse.Pos[0] <= drawWidth {
			// from the bottom
			stripIndex := int(ctx.Mouse.Pos[1] / stripHeight)
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
		if ctx.Mouse.Dragging[platform.MouseButtonPrimary] {
			fsp.mouseDragging = true
			fsp.lastMousePos = ctx.Mouse.Pos

			// Offset so that the selection region is centered over the
			// line between two strips; the index then is to the lower one.
			splitIndex := int(ctx.Mouse.Pos[1]/stripHeight + 0.5)
			yl := float32(splitIndex) * stripHeight
			trid.AddQuad([2]float32{0, yl - 1}, [2]float32{drawWidth, yl - 1},
				[2]float32{drawWidth, yl + 1}, [2]float32{0, yl + 1})
		}
	}
	if fsp.mouseDragging && (ctx.Mouse == nil || !ctx.Mouse.Dragging[platform.MouseButtonPrimary]) {
		fsp.mouseDragging = false

		if fsp.selectedAircraft == "" {
			ctx.Lg.Debug("No selected aircraft for flight strip drag?!")
		} else {
			// Figure out the index for the selected aircraft.
			selectedIndex := func() int {
				for i, fs := range fsp.strips {
					if fs == fsp.selectedAircraft {
						return i
					}
				}
				ctx.Lg.Warnf("Couldn't find %s in flight strips?!", fsp.selectedAircraft)
				return -1
			}()

			// The selected aircraft was set from the original mouse down so
			// now we just need to move it to be in the right place given where
			// the button was released.
			destinationIndex := int(fsp.lastMousePos[1]/stripHeight + 0.5)
			destinationIndex += scrollOffset
			destinationIndex = math.Clamp(destinationIndex, 0, len(fsp.strips))

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
		if ctx.Mouse != nil && ctx.Mouse.Clicked[MouseButtonPrimary] {
			annotationStartX := drawWidth - 3*widthAnn
			if xp := ctx.Mouse.Pos[0]; xp >= annotationStartX && xp < drawWidth {
				stripIndex := int(ctx.Mouse.Pos[1]/stripHeight) + scrollOffset
				if stripIndex < len(fsp.strips) {
					wmTakeKeyboardFocus(fsp, true)
					fsp.selectedStrip = stripIndex

					// Figure out which annotation was selected
					xa := int(ctx.Mouse.Pos[0]-annotationStartX) / int(widthAnn)
					ya := 2 - (int(ctx.Mouse.Pos[1])%int(stripHeight))/(int(stripHeight)/3)
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
	cb.LineWidth(1, ctx.Platform.DPIScale())
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
	FontIdentifier renderer.FontIdentifier
	font           *renderer.Font
	scrollbar      *ScrollBar
	events         *sim.EventsSubscription
	messages       []Message

	// Command-input-related
	input         CLIInput
	history       []CLIInput
	historyOffset int // for up arrow / downarrow. Note: counts from the end! 0 when not in history
	savedInput    CLIInput
}

func NewMessagesPane() *MessagesPane {
	return &MessagesPane{
		FontIdentifier: renderer.FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 16},
	}
}

func (mp *MessagesPane) Name() string { return "Messages" }

func (mp *MessagesPane) Activate(ss *sim.State, r renderer.Renderer, p platform.Platform,
	eventStream *sim.EventStream, lg *log.Logger) {
	if mp.font = renderer.GetFont(mp.FontIdentifier); mp.font == nil {
		mp.font = renderer.GetDefaultFont()
		mp.FontIdentifier = mp.font.Id
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

func (mp *MessagesPane) Reset(ss sim.State, lg *log.Logger) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return true }

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}
}

func (mp *MessagesPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	mp.processEvents(ctx)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		ctx.KeyboardFocus.Take(mp)
	}
	mp.processKeyboard(ctx)

	nLines := len(mp.messages) + 1 /* prompt */
	lineHeight := float32(mp.font.Size + 1)
	visibleLines := int(ctx.PaneExtent.Height() / lineHeight)
	mp.scrollbar.Update(nLines, visibleLines, ctx)

	drawWidth := ctx.PaneExtent.Width()
	if mp.scrollbar.Visible() {
		drawWidth -= float32(mp.scrollbar.PixelExtent())
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	indent := float32(2)

	scrollOffset := mp.scrollbar.Offset()
	y := lineHeight

	// Draw the prompt and any input text
	cliStyle := renderer.TextStyle{Font: mp.font, Color: renderer.RGB{1, 1, .2}}
	cursorStyle := renderer.TextStyle{Font: mp.font, LineSpacing: 0,
		Color: renderer.RGB{1, 1, .2}, DrawBackground: true, BackgroundColor: renderer.RGB{1, 1, 1}}
	ci := mp.input

	prompt := "> "
	if !ctx.HaveFocus {
		// Don't draw the cursor if we don't have keyboard focus
		td.AddText(prompt+ci.cmd, [2]float32{indent, y}, cliStyle)
	} else if ci.cursor == len(ci.cmd) {
		// cursor at the end
		td.AddTextMulti([]string{prompt + string(ci.cmd), " "}, [2]float32{indent, y},
			[]renderer.TextStyle{cliStyle, cursorStyle})
	} else {
		// cursor in the middle
		sb := prompt + ci.cmd[:ci.cursor]
		sc := ci.cmd[ci.cursor : ci.cursor+1]
		se := ci.cmd[ci.cursor+1:]
		styles := []renderer.TextStyle{cliStyle, cursorStyle, cliStyle}
		td.AddTextMulti([]string{sb, sc, se}, [2]float32{indent, y}, styles)
	}
	y += lineHeight

	for i := scrollOffset; i < math.Min(len(mp.messages), visibleLines+scrollOffset+1); i++ {
		// TODO? wrap text
		msg := mp.messages[len(mp.messages)-1-i]

		s := renderer.TextStyle{Font: mp.font, Color: msg.Color()}
		td.AddText(msg.contents, [2]float32{indent, y}, s)
		y += lineHeight
	}

	ctx.SetWindowCoordinateMatrices(cb)
	if ctx.HaveFocus {
		// Yellow border around the edges
		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		w, h := ctx.PaneExtent.Width(), ctx.PaneExtent.Height()
		ld.AddLineLoop([][2]float32{{0, 0}, {w, 0}, {w, h}, {0, h}})
		cb.SetRGB(renderer.RGB{1, 1, 0}) // yellow
		ld.GenerateCommands(cb)
	}
	mp.scrollbar.Draw(ctx, cb)
	td.GenerateCommands(cb)
}

func (mp *MessagesPane) processKeyboard(ctx *Context) {
	if ctx.Keyboard == nil || !ctx.HaveFocus {
		return
	}

	// Grab keyboard input
	if len(mp.input.cmd) > 0 && mp.input.cmd[0] == '/' {
		mp.input.InsertAtCursor(ctx.Keyboard.Input)
	} else {
		mp.input.InsertAtCursor(strings.ToUpper(ctx.Keyboard.Input))
	}

	if ctx.Keyboard.IsPressed(platform.KeyUpArrow) {
		if mp.historyOffset < len(mp.history) {
			if mp.historyOffset == 0 {
				mp.savedInput = mp.input // save current input in case we return
			}
			mp.historyOffset++
			mp.input = mp.history[len(mp.history)-mp.historyOffset]
			mp.input.cursor = len(mp.input.cmd)
		}
	}
	if ctx.Keyboard.IsPressed(platform.KeyDownArrow) {
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

	if (ctx.Keyboard.IsPressed(platform.KeyControl) || ctx.Keyboard.IsPressed(platform.KeySuper)) && ctx.Keyboard.IsPressed(platform.KeyV) {
		c, err := ctx.Platform.GetClipboard().Text()
		if err == nil {
			mp.input.InsertAtCursor(c)
		}
	}
	if ctx.Keyboard.IsPressed(platform.KeyLeftArrow) {
		if mp.input.cursor > 0 {
			mp.input.cursor--
		}
	}

	if ctx.Keyboard.IsPressed(platform.KeyRightArrow) {
		if mp.input.cursor < len(mp.input.cmd) {
			mp.input.cursor++
		}
	}
	if ctx.Keyboard.IsPressed(platform.KeyHome) {
		mp.input.cursor = 0
	}
	if ctx.Keyboard.IsPressed(platform.KeyEnd) {
		mp.input.cursor = len(mp.input.cmd)
	}
	if ctx.Keyboard.IsPressed(platform.KeyBackspace) {
		mp.input.DeleteBeforeCursor()
	}
	if ctx.Keyboard.IsPressed(platform.KeyDelete) {
		mp.input.DeleteAfterCursor()
	}
	if ctx.Keyboard.IsPressed(platform.KeyEscape) {
		if mp.input.cursor > 0 {
			mp.input = CLIInput{}
		}
	}

	if ctx.Keyboard.IsPressed(platform.KeyEnter) && strings.TrimSpace(mp.input.cmd) != "" {
		mp.runCommands(ctx)
	}
}

func (msg *Message) Color() renderer.RGB {
	switch {
	case msg.error:
		return renderer.RGB{.9, .1, .1}
	case msg.global:
		return renderer.RGB{0.012, 0.78, 0.016}
	default:
		return renderer.RGB{1, 1, 1}
	}
}

func (mp *MessagesPane) runCommands(ctx *Context) {
	mp.input.cmd = strings.TrimSpace(mp.input.cmd)

	if mp.input.cmd[0] == '/' {
		ctx.ControlClient.SendGlobalMessage(sim.GlobalMessage{
			FromController: ctx.ControlClient.Callsign,
			Message:        ctx.ControlClient.Callsign + ": " + mp.input.cmd[1:],
		})
		mp.messages = append(mp.messages, Message{contents: ctx.ControlClient.Callsign + ": " + mp.input.cmd[1:], global: true})
		mp.history = append(mp.history, mp.input)
		mp.input = CLIInput{}
		return
	}

	callsign, cmd, ok := strings.Cut(mp.input.cmd, " ")
	mp.messages = append(mp.messages, Message{contents: "> " + mp.input.cmd})
	mp.history = append(mp.history, mp.input)
	mp.input = CLIInput{}

	if ok {
		if ac := ctx.ControlClient.AircraftFromPartialCallsign(callsign); ac != nil {
			ctx.ControlClient.RunAircraftCommands(ac.Callsign, cmd,
				func(errorString string, remainingCommands string) {
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

func (mp *MessagesPane) processEvents(ctx *Context) {
	lastRadioCallsign := ""
	var lastRadioType av.RadioTransmissionType
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
			if telephony, ok := av.DB.Callsigns[icao]; ok {
				radioCallsign = telephony + " " + flight
				if ac := ctx.ControlClient.Aircraft[callsign]; ac != nil {
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
		if lastRadioType == av.RadioTransmissionContact {
			ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
			fullName := ctrl.FullName
			if ac := ctx.ControlClient.Aircraft[callsign]; ac != nil && ac.IsDeparture() {
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
		ctx.Lg.Debug("radio_transmission", slog.String("callsign", callsign), slog.Any("message", msg))
		mp.messages = append(mp.messages, msg)
	}

	for _, event := range mp.events.Get() {
		switch event.Type {
		case sim.RadioTransmissionEvent:
			if event.ToController == ctx.ControlClient.Callsign {
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
				unexpectedTransmission = unexpectedTransmission || (event.RadioTransmissionType == av.RadioTransmissionUnexpected)
			}
		case sim.GlobalMessageEvent:
			if event.FromController != ctx.ControlClient.Callsign {
				mp.messages = append(mp.messages, Message{contents: event.Message, global: true})
			}
		case sim.StatusMessageEvent:
			// Don't spam the same message repeatedly; look in the most recent 5.
			n := len(mp.messages)
			start := math.Max(0, n-5)
			if !slices.ContainsFunc(mp.messages[start:],
				func(m Message) bool { return m.contents == event.Message }) {
				mp.messages = append(mp.messages,
					Message{
						contents: event.Message,
						system:   true,
					})
			}
		case sim.TrackClickedEvent:
			if cmd := strings.TrimSpace(mp.input.cmd); cmd != "" {
				mp.input.cmd = event.Callsign + " " + cmd
				mp.runCommands(ctx)
			}
		}
	}

	if len(transmissions) > 0 {
		addTransmissions()
	}
}

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

///////////////////////////////////////////////////////////////////////////
// Text editing

const (
	TextEditReturnNone = iota
	TextEditReturnTextChanged
	TextEditReturnEnter
	TextEditReturnNext
	TextEditReturnPrev
)

// uiDrawTextEdit handles the basics of interactive text editing; it takes
// a string and cursor position and then renders them with the specified
// style, processes keyboard inputs and updates the string accordingly.
func uiDrawTextEdit(s *string, cursor *int, keyboard *platform.KeyboardState, pos [2]float32, style,
	cursorStyle renderer.TextStyle, focus KeyboardFocus, cb *renderer.CommandBuffer) (exit int, posOut [2]float32) {
	// Make sure we can depend on it being sensible for the following
	*cursor = math.Clamp(*cursor, 0, len(*s))
	originalText := *s

	// Draw the text and the cursor
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	if *cursor == len(*s) {
		// cursor at the end
		posOut = td.AddTextMulti([]string{*s, " "}, pos, []renderer.TextStyle{style, cursorStyle})
	} else {
		// cursor in the middle
		sb, sc, se := (*s)[:*cursor], (*s)[*cursor:*cursor+1], (*s)[*cursor+1:]
		styles := []renderer.TextStyle{style, cursorStyle, style}
		posOut = td.AddTextMulti([]string{sb, sc, se}, pos, styles)
	}
	td.GenerateCommands(cb)

	// Handle various special keys.
	if keyboard != nil {
		if keyboard.IsPressed(platform.KeyBackspace) && *cursor > 0 {
			*s = (*s)[:*cursor-1] + (*s)[*cursor:]
			*cursor--
		}
		if keyboard.IsPressed(platform.KeyDelete) && *cursor < len(*s)-1 {
			*s = (*s)[:*cursor] + (*s)[*cursor+1:]
		}
		if keyboard.IsPressed(platform.KeyLeftArrow) {
			*cursor = math.Max(*cursor-1, 0)
		}
		if keyboard.IsPressed(platform.KeyRightArrow) {
			*cursor = math.Min(*cursor+1, len(*s))
		}
		if keyboard.IsPressed(platform.KeyEscape) {
			// clear out the string
			*s = ""
			*cursor = 0
		}
		if keyboard.IsPressed(platform.KeyEnter) {
			focus.Release()
			exit = TextEditReturnEnter
		}
		if keyboard.IsPressed(platform.KeyTab) {
			if keyboard.IsPressed(platform.KeyShift) {
				exit = TextEditReturnPrev
			} else {
				exit = TextEditReturnNext
			}
		}

		// And finally insert any regular characters into the appropriate spot
		// in the string.
		if keyboard.Input != "" {
			*s = (*s)[:*cursor] + keyboard.Input + (*s)[*cursor:]
			*cursor += len(keyboard.Input)
		}
	}

	if exit == TextEditReturnNone && *s != originalText {
		exit = TextEditReturnTextChanged
	}

	return
}

// If |b| is true, all following imgui elements will be disabled (and drawn
// accordingly).
func uiStartDisable(b bool) {
	if b {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
}

// Each call to uiStartDisable should have a matching call to uiEndDisable,
// with the same Boolean value passed to it.
func uiEndDisable(b bool) {
	if b {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}

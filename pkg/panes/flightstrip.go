// pkg/panes/flightstrip.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

type FlightStripPane struct {
	FontSize int
	font     *renderer.Font

	HideFlightStrips          bool
	AutoAddDepartures         bool
	AutoAddArrivals           bool
	AutoAddOverflights        bool
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

func init() {
	RegisterUnmarshalPane("FlightStripPane", func(d []byte) (Pane, error) {
		var p FlightStripPane
		err := json.Unmarshal(d, &p)
		return &p, err
	})
}

func NewFlightStripPane() *FlightStripPane {
	return &FlightStripPane{
		AddPushed:               true,
		AutoAddDepartures:       true,
		AutoAddTracked:          true,
		AutoAddAcceptedHandoffs: true,
		AutoRemoveDropped:       true,
		AutoRemoveHandoffs:      true,

		FontSize:           12,
		selectedStrip:      -1,
		selectedAnnotation: -1,
	}
}

func (fsp *FlightStripPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
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
}

func (fsp *FlightStripPane) possiblyAddAircraft(ss *sim.State, ac *av.Aircraft) {
	if _, ok := fsp.addedAircraft[ac.Callsign]; ok {
		return
	}
	if ac.FlightPlan == nil {
		return
	}

	add := fsp.AutoAddTracked && ac.TrackingController == ss.Callsign && ac.FlightPlan != nil
	add = add || ac.TrackingController == "" && fsp.AutoAddDepartures && ss.IsDeparture(ac)
	add = add || ac.TrackingController == "" && fsp.AutoAddArrivals && ss.IsArrival(ac)
	add = add || ac.TrackingController == "" && fsp.AutoAddOverflights && ss.IsOverflight(ac)

	if add {
		fsp.strips = append(fsp.strips, ac.Callsign)
		fsp.addedAircraft[ac.Callsign] = nil
	}
}

func (fsp *FlightStripPane) LoadedSim(ss sim.State, lg *log.Logger) {}

func (fsp *FlightStripPane) ResetSim(ss sim.State, lg *log.Logger) {
	fsp.strips = nil
	fsp.addedAircraft = make(map[string]interface{})
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return false /*true*/ }

func (fsp *FlightStripPane) processEvents(ctx *Context) {
	// First account for changes in world.Aircraft
	// Added aircraft
	for _, ac := range ctx.ControlClient.Aircraft {
		fsp.possiblyAddAircraft(&ctx.ControlClient.State, ac)
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
				fsp.possiblyAddAircraft(&ctx.ControlClient.State, ac)
			}
		case sim.InitiatedTrackEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddTracked && ac.TrackingController == ctx.ControlClient.Callsign {
					fsp.possiblyAddAircraft(&ctx.ControlClient.State, ac)
				}
			}
		case sim.DroppedTrackEvent:
			if fsp.AutoRemoveDropped {
				remove(event.Callsign)
			}
		case sim.AcceptedHandoffEvent, sim.AcceptedRedirectedHandoffEvent:
			if ac, ok := ctx.ControlClient.Aircraft[event.Callsign]; ok {
				if fsp.AutoAddAcceptedHandoffs && ac.TrackingController == ctx.ControlClient.Callsign {
					fsp.possiblyAddAircraft(&ctx.ControlClient.State, ac)
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
			return ac != nil && ctx.ControlClient.State.IsDeparture(ac)
		}
		dep := util.FilterSlice(fsp.strips, isDeparture)
		arr := util.FilterSlice(fsp.strips, func(callsign string) bool { return !isDeparture(callsign) })

		fsp.strips = fsp.strips[:0]
		fsp.strips = append(fsp.strips, dep...)
		fsp.strips = append(fsp.strips, arr...)
	}
}

func (fsp *FlightStripPane) DisplayName() string { return "Flight Strips" }

func (fsp *FlightStripPane) Hide() bool { return fsp.HideFlightStrips }

func (fsp *FlightStripPane) DrawUI(p platform.Platform, config *platform.Config) {
	show := !fsp.HideFlightStrips
	imgui.Checkbox("Show flight strips", &show)
	fsp.HideFlightStrips = !show

	uiStartDisable(fsp.HideFlightStrips)
	imgui.Checkbox("Automatically add departures", &fsp.AutoAddDepartures)
	imgui.Checkbox("Automatically add arrivals", &fsp.AutoAddArrivals)
	imgui.Checkbox("Automatically add overflights", &fsp.AutoAddOverflights)
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
				editResult, _ = drawTextEdit(&strip.Annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
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
		case textEditReturnNone, textEditReturnTextChanged:
			// nothing to do
		case textEditReturnEnter:
			fsp.selectedStrip = -1
			ctx.KeyboardFocus.Release()
		case textEditReturnNext:
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 1) % 9
			fsp.annotationCursorPos = len(strip.Annotations[fsp.selectedAnnotation])
		case textEditReturnPrev:
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
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	cb.SetRGB(UITextHighlightColor)
	trid.GenerateCommands(cb)
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

///////////////////////////////////////////////////////////////////////////
// Text editing

const (
	textEditReturnNone = iota
	textEditReturnTextChanged
	textEditReturnEnter
	textEditReturnNext
	textEditReturnPrev
)

// drawTextEdit handles the basics of interactive text editing; it takes
// a string and cursor position and then renders them with the specified
// style, processes keyboard inputs and updates the string accordingly.
func drawTextEdit(s *string, cursor *int, keyboard *platform.KeyboardState, pos [2]float32, style,
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
		if keyboard.WasPressed(platform.KeyBackspace) && *cursor > 0 {
			*s = (*s)[:*cursor-1] + (*s)[*cursor:]
			*cursor--
		}
		if keyboard.WasPressed(platform.KeyDelete) && *cursor < len(*s)-1 {
			*s = (*s)[:*cursor] + (*s)[*cursor+1:]
		}
		if keyboard.WasPressed(platform.KeyLeftArrow) {
			*cursor = math.Max(*cursor-1, 0)
		}
		if keyboard.WasPressed(platform.KeyRightArrow) {
			*cursor = math.Min(*cursor+1, len(*s))
		}
		if keyboard.WasPressed(platform.KeyEscape) {
			// clear out the string
			*s = ""
			*cursor = 0
		}
		if keyboard.WasPressed(platform.KeyEnter) {
			focus.Release()
			exit = textEditReturnEnter
		}
		if keyboard.WasPressed(platform.KeyTab) {
			if keyboard.WasPressed(platform.KeyShift) {
				exit = textEditReturnPrev
			} else {
				exit = textEditReturnNext
			}
		}

		// And finally insert any regular characters into the appropriate spot
		// in the string.
		if keyboard.Input != "" {
			*s = (*s)[:*cursor] + keyboard.Input + (*s)[*cursor:]
			*cursor += len(keyboard.Input)
		}
	}

	if exit == textEditReturnNone && *s != originalText {
		exit = textEditReturnTextChanged
	}

	return
}

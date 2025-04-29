// pkg/panes/flightstrip.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
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
	DarkMode                  bool

	strips     []sim.ACID
	addedPlans map[sim.ACID]interface{}

	mouseDragging       bool
	lastMousePos        [2]float32
	selectedStrip       int
	selectedAnnotation  int
	annotationCursorPos int

	events    *sim.EventsSubscription
	scrollbar *ScrollBar

	selectedACID sim.ACID

	// computer id number: 000-999
	CIDs          map[sim.ACID]int
	AllocatedCIDs map[int]interface{}
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
		CIDs:               make(map[sim.ACID]int),
		AllocatedCIDs:      make(map[int]interface{}),
	}
}

func (fsp *FlightStripPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if fsp.FontSize == 0 {
		fsp.FontSize = 12
	}
	if fsp.font = renderer.GetFont(renderer.FontIdentifier{Name: "Flight Strip Printer", Size: fsp.FontSize}); fsp.font == nil {
		fsp.font = renderer.GetDefaultFont()
	}
	if fsp.addedPlans == nil {
		fsp.addedPlans = make(map[sim.ACID]interface{})
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewVerticalScrollBar(4, true)
	}
	if fsp.CIDs == nil {
		fsp.CIDs = make(map[sim.ACID]int)
	}
	if fsp.AllocatedCIDs == nil {
		fsp.AllocatedCIDs = make(map[int]interface{})
	}

	fsp.events = eventStream.Subscribe()
}

func (fsp *FlightStripPane) getCID(acid sim.ACID) int {
	if id, ok := fsp.CIDs[acid]; ok {
		return id
	}

	// Find a free one. Start searching at a random offset.
	start := rand.Make().Intn(1000)
	for i := range 1000 {
		idx := (start + i) % 1000
		if _, ok := fsp.AllocatedCIDs[idx]; !ok {
			fsp.CIDs[acid] = idx
			fsp.AllocatedCIDs[idx] = nil
			return idx
		}
	}
	// Couldn't find one(?!)
	fsp.CIDs[acid] = start
	return start
}

func (fsp *FlightStripPane) possiblyAdd(fp *sim.STARSFlightPlan, tcp string) {
	if _, ok := fsp.addedPlans[fp.ACID]; ok {
		// We've seen it before.
		return
	}

	if fp.TrackingController == tcp {
		fsp.strips = append(fsp.strips, fp.ACID)
		fsp.addedPlans[fp.ACID] = nil
	}
}

func (fsp *FlightStripPane) LoadedSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}

func (fsp *FlightStripPane) ResetSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	fsp.strips = nil
	fsp.addedPlans = make(map[sim.ACID]interface{})
	fsp.CIDs = make(map[sim.ACID]int)
	fsp.AllocatedCIDs = make(map[int]interface{})
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return false /*true*/ }

func (fsp *FlightStripPane) processEvents(ctx *Context) {
	// First account for changes in world.Aircraft
	// Added aircraft
	for _, trk := range ctx.Client.State.Tracks {
		if trk.FlightPlan != nil {
			fsp.possiblyAdd(trk.FlightPlan, ctx.UserTCP)
		}
	}

	remove := func(acid sim.ACID) {
		fsp.strips = util.FilterSliceInPlace(fsp.strips, func(a sim.ACID) bool { return acid != a })
		if fsp.selectedACID == acid {
			fsp.selectedACID = ""
		}
		// Free up the CID
		if cid, ok := fsp.CIDs[acid]; ok {
			delete(fsp.CIDs, acid)
			delete(fsp.AllocatedCIDs, cid)
		}
	}

	for _, event := range fsp.events.Get() {
		switch event.Type {
		case sim.PushedFlightStripEvent:
			// For all of these it's possible that we have an event for an
			// aircraft that was deleted shortly afterward. So it's
			// necessary to check that it's still in
			// ControlClient.Aircraft.
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil && fsp.AddPushed {
				fsp.possiblyAdd(fp, ctx.UserTCP)
			}

		case sim.FlightPlanAssociatedEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil && fsp.AutoAddTracked {
				fsp.possiblyAdd(fp, ctx.UserTCP)
			}

		case sim.AcceptedHandoffEvent, sim.AcceptedRedirectedHandoffEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil && fsp.AutoAddAcceptedHandoffs {
				fsp.possiblyAdd(fp, ctx.UserTCP)
			}

		case sim.HandoffControlEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil && fp.TrackingController != ctx.UserTCP {
				remove(event.ACID)
			}
		}
	}

	// Danger: this is now O(n^2)
	// Remove ones that have been deleted or have had their flight plan deleted.
	fsp.strips = util.FilterSliceInPlace(fsp.strips, func(acid sim.ACID) bool {
		return ctx.Client.State.GetFlightPlanForACID(acid) != nil
	})

	if fsp.CollectDeparturesArrivals {
		isDeparture := func(acid sim.ACID) bool {
			fp := ctx.Client.State.GetFlightPlanForACID(acid)
			return fp != nil && fp.TypeOfFlight == av.FlightTypeDeparture
		}
		dep := util.FilterSlice(fsp.strips, isDeparture)
		arr := util.FilterSlice(fsp.strips, func(acid sim.ACID) bool { return !isDeparture(acid) })

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

	if fsp.HideFlightStrips {
		imgui.BeginDisabled()
	}
	imgui.Checkbox("Automatically add departures", &fsp.AutoAddDepartures)
	imgui.Checkbox("Automatically add arrivals", &fsp.AutoAddArrivals)
	imgui.Checkbox("Automatically add overflights", &fsp.AutoAddOverflights)
	imgui.Checkbox("Add pushed flight strips", &fsp.AddPushed)
	imgui.Checkbox("Automatically add when track is initiated", &fsp.AutoAddTracked)
	imgui.Checkbox("Automatically add handoffs", &fsp.AutoAddAcceptedHandoffs)
	imgui.Checkbox("Automatically remove dropped tracks", &fsp.AutoRemoveDropped)
	imgui.Checkbox("Automatically remove accepted handoffs", &fsp.AutoRemoveHandoffs)

	imgui.Checkbox("Collect departures and arrivals together", &fsp.CollectDeparturesArrivals)
	imgui.Checkbox("Night mode", &fsp.DarkMode)

	id := renderer.FontIdentifier{Name: fsp.font.Id.Name, Size: fsp.FontSize}
	if newFont, changed := renderer.DrawFontSizeSelector(&id); changed {
		fsp.FontSize = newFont.Size
		fsp.font = newFont
	}
	if fsp.HideFlightStrips {
		imgui.EndDisabled()
	}
}

func (fsp *FlightStripPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	fsp.processEvents(ctx)

	// Font width and height
	// the 'Flight Strip Printer' font seems to have an unusually thin space,
	// so instead use 'X' to get the expected per-character width for layout.
	bx, _ := fsp.font.BoundText("X", 0)
	fw, fh := float32(bx), float32(fsp.font.Size)

	// 3 lines of text, 2 lines on top and below for padding, 1 pixel separator line
	vpad := float32(2)
	stripHeight := float32(int(1 + 2*vpad + 4*fh))

	visibleStrips := int(ctx.PaneExtent.Height() / stripHeight)
	fsp.scrollbar.Update(len(fsp.strips), visibleStrips, ctx)

	indent := float32(int32(fw / 2))
	// column widths in pixels
	width0 := 10 * fw
	width1 := 6 * fw
	width2 := 6 * fw
	widthAnn := 5 * fw

	// The full width in pixels we have for drawing flight strips.
	drawWidth := ctx.PaneExtent.Width()
	if fsp.scrollbar.Visible() {
		drawWidth -= float32(fsp.scrollbar.PixelExtent())
	}

	// The width of the center region (where the route, etc., go) is set to
	// take all of the space left after the fixed-width columns have taken
	// their part.
	widthCenter := drawWidth - width0 - width1 - width2 - 3*widthAnn
	if widthCenter < 0 {
		// not sure what to do if it comes to this...
		widthCenter = 20 * fw
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

	darkmode := func(rgb renderer.RGB) renderer.RGB {
		if fsp.DarkMode {
			return renderer.RGB{R: 1 - rgb.R, G: 1 - rgb.G, B: 1 - rgb.B}
		}
		return rgb
	}

	// Draw the background for all of them
	qb := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(qb)
	bgColor := darkmode(renderer.RGB{.9, .9, .85})
	y0, y1 := float32(0), float32(math.Min(len(fsp.strips), visibleStrips))*stripHeight-1
	qb.AddQuad([2]float32{0, y0}, [2]float32{drawWidth, y0}, [2]float32{drawWidth, y1}, [2]float32{0, y1}, bgColor)

	ctx.SetWindowCoordinateMatrices(cb)
	qb.GenerateCommands(cb)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	// Draw from the bottom
	style := renderer.TextStyle{Font: fsp.font, Color: darkmode(renderer.RGB{.1, .1, .1})}
	scrollOffset := fsp.scrollbar.Offset()
	y := stripHeight - 1
	for i := scrollOffset; i < math.Min(len(fsp.strips), visibleStrips+scrollOffset+1); i++ {
		sfp := ctx.Client.State.GetFlightPlanForACID(fsp.strips[i])
		if sfp == nil {
			continue
		}
		fp := ctx.Client.State.ACFlightPlans[av.ADSBCallsign(fsp.strips[i])] // HAX: conflates callsign/ACID
		var strip av.FlightStrip

		x := float32(0)

		drawColumn := func(line0, line1, line2 string, width float32, lines bool) {
			td.AddText(line0, [2]float32{x + indent, y - vpad}, style)
			td.AddText(line1, [2]float32{x + indent, y - vpad - stripHeight/3}, style)
			td.AddText(line2, [2]float32{x + indent, y - vpad - stripHeight*2/3}, style)
			// Line on the right
			ld.AddLine([2]float32{x + width, y}, [2]float32{x + width, y - stripHeight})
			if lines {
				// Horizontal lines
				ld.AddLine([2]float32{x, y - stripHeight/3}, [2]float32{x + width, y - stripHeight/3})
				ld.AddLine([2]float32{x, y - stripHeight*2/3}, [2]float32{x + width, y - stripHeight*2/3})
			}
		}

		formatRoute := func(route string, width float32, nlines int) []string {
			// Lay the lines out, breaking at cols, but don't worry about
			// having too many lines for now.
			cols := int(width / fw)
			var lines []string
			var b strings.Builder
			fixes := strings.Fields(route)
			for _, fix := range fixes {
				n := len(fix)
				if n > 15 {
					// Assume it's a latlong; skip it.
					continue
				}
				if b.Len() > 0 {
					// Space after the previous one on this line.
					b.WriteByte(' ')
				}
				if b.Len()+n > cols {
					// Would overflow the current line; start a new one.
					lines = append(lines, b.String())
					b.Reset()
				}

				b.WriteString(fix)
			}
			if b.Len() > 0 {
				lines = append(lines, b.String())
			}

			// Make sure we return at least the number of lines requested.
			for len(lines) < nlines {
				lines = append(lines, "")
			}

			if len(lines) > nlines && len(fixes) > 1 {
				// We have too many lines. Go back and patch up the last
				// line so that it has *** and the final fix at the end.
				last := fixes[len(fixes)-1]
				need := len(last) + 3
				line := lines[nlines-1]
				// Keep chopping the last fix off until we have enough space.
				for len(line)+need > cols {
					idx := strings.LastIndexByte(line, ' ')
					if idx == -1 {
						// We're down to an empty string; give up since
						// we're inevitably going to overflow.
						break
					}
					line = line[:idx]
				}
				lines[nlines-1] = line + "***" + last
			}
			return lines[:nlines]
		}

		// First column; 3 entries: callsign, aircraft type, 3-digit id number
		cid := fmt.Sprintf("%03d", fsp.getCID(sfp.ACID))
		drawColumn(string(sfp.ACID), sfp.CWTCategory+"/"+sfp.AircraftType, cid, width0, false)

		x += width0
		if sfp.TypeOfFlight == av.FlightTypeDeparture {
			// Second column; 3 entries: squawk, proposed time, requested altitude
			proposedTime := "P" + sfp.CoordinationTime.UTC().Format("1504")
			drawColumn(sfp.AssignedSquawk.String(), proposedTime, strconv.Itoa(sfp.RequestedAltitude/100),
				width1, true)

			// Third column: departure airport, (empty), (empty)
			x += width1
			// Departures
			drawColumn(fp.DepartureAirport, "", "", width2, false)

			x += width2
			// Fourth column: route and destination airport
			route := formatRoute(fp.Route+" "+fp.ArrivalAirport, widthCenter, 3)
			drawColumn(route[0], route[1], route[2], widthCenter, false)
		} else if sfp.TypeOfFlight == av.FlightTypeArrival {
			// Second column; 3 entries: squawk, previous fix, coordination fix
			drawColumn(sfp.AssignedSquawk.String(), "", "", width1, true)

			x += width1
			// Third column: eta of arrival at coordination fix / destination airport, empty, empty
			arrivalTime := "A" + sfp.CoordinationTime.UTC().Format("1504")
			drawColumn(arrivalTime, "", "", width2, false)

			// Fourth column: IFR, destination airport
			x += width2
			drawColumn("IFR", "", fp.ArrivalAirport, widthCenter, false)
		} else {
			// Overflight
			// Second column; 3 entries: squawk, entry fix, exit fix
			drawColumn(sfp.AssignedSquawk.String(), "", "", width1, true)

			x += width1
			// Third column: eta of arrival at entry coordination fix, empty, empty
			arrivalTime := "E" + sfp.CoordinationTime.UTC().Format("1504")
			drawColumn(arrivalTime, "", "", width2, false)

			// Fourth column: altitude, route
			x += width2
			// TODO: e.g. "VFR/65" for altitude if it's VFR
			route := formatRoute(fp.DepartureAirport+" "+fp.Route+" "+fp.ArrivalAirport, widthCenter, 2)
			drawColumn(strconv.Itoa(fp.Altitude/100), route[0], route[1], widthCenter, false)
		}

		// Annotations
		x += widthCenter
		var editResult int
		for ai, ann := range strip.Annotations {
			ix, iy := ai%3, ai/3
			xp, yp := x+float32(ix)*widthAnn+indent, y-float32(iy)*stripHeight/3

			if ctx.HaveFocus && fsp.selectedStrip == i && ai == fsp.selectedAnnotation {
				// If were currently editing this annotation, don't draw it
				// normally but instead draw it including a cursor, update
				// it according to keyboard input, etc.
				cursorStyle := renderer.TextStyle{Font: fsp.font, Color: bgColor,
					DrawBackground: true, BackgroundColor: style.Color}
				editResult, _ = drawTextEdit(&strip.Annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
					ctx.Keyboard, [2]float32{xp, yp}, style, cursorStyle, *ctx.KeyboardFocus, cb)
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
		ld.AddLine([2]float32{x, y - stripHeight/3}, [2]float32{drawWidth, y - stripHeight/3})
		ld.AddLine([2]float32{x, y - stripHeight*2/3}, [2]float32{drawWidth, y - stripHeight*2/3})
		// Vertical lines
		for i := 0; i < 3; i++ {
			xp := x + float32(i)*widthAnn
			ld.AddLine([2]float32{xp, y}, [2]float32{xp, y - stripHeight})
		}

		// Line at the top
		ld.AddLine([2]float32{0, y}, [2]float32{drawWidth, y})

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
				if io.KeyShift() {
					// delete the flight strip
					copy(fsp.strips[stripIndex:], fsp.strips[stripIndex+1:])
					fsp.strips = fsp.strips[:len(fsp.strips)-1]
				} else {
					// select the aircraft
					fsp.selectedACID = fsp.strips[stripIndex]
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

		if fsp.selectedACID == "" {
			ctx.Lg.Debug("No selected aircraft for flight strip drag?!")
		} else {
			// Figure out the index for the selected aircraft.
			selectedIndex := func() int {
				for i, fs := range fsp.strips {
					if fs == fsp.selectedACID {
						return i
					}
				}
				ctx.Lg.Warnf("Couldn't find %s in flight strips?!", fsp.selectedACID)
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
				fsp.strips = append([]sim.ACID{}, fsp.strips[:destinationIndex]...)
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

	cb.SetRGB(darkmode(UIControlColor))
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	cb.SetRGB(darkmode(UITextHighlightColor))
	trid.GenerateCommands(cb)
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
		if keyboard.WasPressed(imgui.KeyBackspace) && *cursor > 0 {
			*s = (*s)[:*cursor-1] + (*s)[*cursor:]
			*cursor--
		}
		if keyboard.WasPressed(imgui.KeyDelete) && *cursor < len(*s)-1 {
			*s = (*s)[:*cursor] + (*s)[*cursor+1:]
		}
		if keyboard.WasPressed(imgui.KeyLeftArrow) {
			*cursor = math.Max(*cursor-1, 0)
		}
		if keyboard.WasPressed(imgui.KeyRightArrow) {
			*cursor = math.Min(*cursor+1, len(*s))
		}
		if keyboard.WasPressed(imgui.KeyEscape) {
			// clear out the string
			*s = ""
			*cursor = 0
		}
		if keyboard.WasPressed(imgui.KeyEnter) {
			focus.Release()
			exit = textEditReturnEnter
		}
		if keyboard.WasPressed(imgui.KeyTab) {
			if keyboard.KeyShift() {
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

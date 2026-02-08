// panes/flightstrip.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

type FlightStripPane struct {
	FontSize int
	font     *renderer.Font

	HideFlightStrips bool
	DarkMode         bool

	// Display ordering, reconciled each frame with the server list.
	ACIDDisplayOrder []sim.ACID

	// Drag-reorder state
	dragSelectedACID sim.ACID
	dragStartPos     [2]float32
	dragActive       bool

	scrollbar *ScrollBar

	// Annotation editing state: copied from server on click, written back via RPC on commit.
	// editingACID == "" means no editing is active.
	editingACID         sim.ACID
	editingAnnotation   int // which cell (0-8) is selected
	editingAnnotations  [9]string
	annotationCursorPos int

	// Right-click context menu state
	contextMenuACID   sim.ACID
	contextMenuTarget sim.TCP
	contextMenuPos    [2]float32    // anchor position (set on click)
	contextMenuExtent math.Extent2D // drawn bounds (set by drawContextMenu)
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
		FontSize: 12,
	}
}

func (fsp *FlightStripPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if fsp.FontSize == 0 {
		fsp.FontSize = 12
	}
	if fsp.font = renderer.GetFont(renderer.FontIdentifier{Name: renderer.FlightStripPrinter, Size: fsp.FontSize}); fsp.font == nil {
		fsp.font = renderer.GetDefaultFont()
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewVerticalScrollBar(4, true)
	}
}

func (fsp *FlightStripPane) LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
}

func (fsp *FlightStripPane) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	fsp.ACIDDisplayOrder = nil
	fsp.editingACID = ""
	fsp.dragSelectedACID = ""
	fsp.dragActive = false
	fsp.contextMenuACID = ""
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return true }

// reconcileOrder syncs localOrder with the server's strip ACID list:
// new ACIDs are appended, removed ones are pruned. There's a bunch of O(n^2)
// over ACIDs here, but it should be fine(tm).
func (fsp *FlightStripPane) reconcileOrder(stripACIDs []sim.ACID) {
	// Remove ACIDs from localOrder that no longer exist.
	fsp.ACIDDisplayOrder = util.FilterSlice(fsp.ACIDDisplayOrder, func(acid sim.ACID) bool {
		return slices.Contains(stripACIDs, acid)
	})

	// Add new server ACIDs at the end of localOrder.
	for acid := range slices.Values(stripACIDs) {
		if !slices.Contains(fsp.ACIDDisplayOrder, acid) {
			fsp.ACIDDisplayOrder = append(fsp.ACIDDisplayOrder, acid)
		}
	}

	// If the edited strip is no longer present, cancel editing.
	if fsp.editingACID != "" && !slices.Contains(fsp.ACIDDisplayOrder, fsp.editingACID) {
		fsp.editingACID = ""
	}
	if fsp.dragSelectedACID != "" && !slices.Contains(fsp.ACIDDisplayOrder, fsp.dragSelectedACID) {
		fsp.dragSelectedACID = ""
		fsp.dragActive = false
	}
	if fsp.contextMenuACID != "" && !slices.Contains(fsp.ACIDDisplayOrder, fsp.contextMenuACID) {
		fsp.contextMenuACID = ""
	}
}

func (fsp *FlightStripPane) Hide() bool { return fsp.HideFlightStrips }

var _ UIDrawer = (*FlightStripPane)(nil)

func (fsp *FlightStripPane) DisplayName() string { return "Flight Strips" }

func (fsp *FlightStripPane) DrawUI(p platform.Platform, config *platform.Config) {
	show := !fsp.HideFlightStrips
	imgui.Checkbox("Show flight strips", &show)
	fsp.HideFlightStrips = !show

	if fsp.HideFlightStrips {
		imgui.BeginDisabled()
	}
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

///////////////////////////////////////////////////////////////////////////
// Flight strip layout and drawing

// stripLayout holds pixel metrics for drawing flight strips.
type stripLayout struct {
	fw, fh        float32
	vpad          float32
	stripHeight   float32
	visibleStrips int
	indent        float32
	width0        float32 // callsign / type / CID
	width1        float32 // squawk / time / altitude
	width2        float32 // airport
	widthAnn      float32 // single annotation column
	widthCenter   float32 // route
	drawWidth     float32 // total drawable width
	darkMode      bool
}

func (l stripLayout) dark(rgb renderer.RGB) renderer.RGB {
	if l.darkMode {
		return renderer.RGB{R: 1 - rgb.R, G: 1 - rgb.G, B: 1 - rgb.B}
	}
	return rgb
}

func (fsp *FlightStripPane) layoutStrips(ctx *Context) stripLayout {
	// The 'Flight Strip Printer' font seems to have an unusually thin
	// space, so instead use 'X' to get the expected per-character width
	// for layout.
	bx, _ := fsp.font.BoundText("X", 0)
	fw := float32(bx)
	fh := float32(fsp.font.Size)

	// 3 lines of text, 2 lines on top and below for padding,
	// 1 pixel separator line.
	vpad := float32(2)
	stripHeight := float32(int(1 + 2*vpad + 4*fh))

	visibleStrips := int(ctx.PaneExtent.Height() / stripHeight)
	fsp.scrollbar.Update(len(fsp.ACIDDisplayOrder), visibleStrips, ctx)

	indent := float32(int32(fw / 2))
	width0 := 10 * fw
	width1 := 6 * fw
	width2 := 6 * fw
	widthAnn := 5 * fw

	drawWidth := ctx.PaneExtent.Width()
	if fsp.scrollbar.Visible() {
		drawWidth -= float32(fsp.scrollbar.PixelExtent())
	}

	// The center region (route, etc.) takes all space left after the
	// fixed-width columns.
	widthCenter := drawWidth - width0 - width1 - width2 - 3*widthAnn
	if widthCenter < 0 {
		widthCenter = 20 * fw
	}

	return stripLayout{
		fw: fw, fh: fh,
		vpad:          vpad,
		stripHeight:   stripHeight,
		visibleStrips: visibleStrips,
		indent:        indent,
		width0:        width0,
		width1:        width1,
		width2:        width2,
		widthAnn:      widthAnn,
		widthCenter:   widthCenter,
		drawWidth:     drawWidth,
		darkMode:      fsp.DarkMode,
	}
}

// formatRoute word-wraps a route string into nlines lines that fit within
// the given pixel width. If the route overflows, the last line is
// truncated with *** followed by the final fix.
func formatRoute(route string, fw, width float32, nlines int) []string {
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
			b.WriteByte(' ')
		}
		if b.Len()+n > cols {
			lines = append(lines, b.String())
			b.Reset()
		}
		b.WriteString(fix)
	}
	if b.Len() > 0 {
		lines = append(lines, b.String())
	}

	for len(lines) < nlines {
		lines = append(lines, "")
	}

	if len(lines) > nlines && len(fixes) > 1 {
		// Too many lines: patch the last visible line so it has ***
		// and the final fix at the end.
		last := fixes[len(fixes)-1]
		need := len(last) + 3
		line := lines[nlines-1]
		for len(line)+need > cols {
			idx := strings.LastIndexByte(line, ' ')
			if idx == -1 {
				break
			}
			line = line[:idx]
		}
		lines[nlines-1] = line + "***" + last
	}
	return lines[:nlines]
}

func (fsp *FlightStripPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	fsp.reconcileOrder(ctx.Client.State.FlightStripACIDs)

	// Commit annotations if the pane lost focus while editing.
	if fsp.editingACID != "" && !ctx.HaveFocus {
		ctx.Client.AnnotateFlightStrip(fsp.editingACID, fsp.editingAnnotations)
		fsp.editingACID = ""
	}

	lay := fsp.layoutStrips(ctx)

	// Process mouse input before drawing so drags update order this frame.
	scrollOffset := fsp.scrollbar.Offset()
	fsp.handleMouse(ctx, lay, scrollOffset)

	// Background
	qb := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(qb)
	bgColor := lay.dark(renderer.RGB{R: .9, G: .9, B: .85})
	y0, y1 := float32(0), ctx.PaneExtent.Height()
	qb.AddQuad([2]float32{0, y0}, [2]float32{lay.drawWidth, y0},
		[2]float32{lay.drawWidth, y1}, [2]float32{0, y1}, bgColor)

	// Highlight the strip being dragged
	if fsp.dragActive {
		if di := slices.Index(fsp.ACIDDisplayOrder, fsp.dragSelectedACID); di >= scrollOffset {
			hy := float32(di-scrollOffset) * lay.stripHeight
			dragColor := lay.dark(renderer.RGB{R: .82, G: .82, B: .77})
			qb.AddQuad([2]float32{0, hy}, [2]float32{lay.drawWidth, hy},
				[2]float32{lay.drawWidth, hy + lay.stripHeight}, [2]float32{0, hy + lay.stripHeight},
				dragColor)
		}
	}

	ctx.SetWindowCoordinateMatrices(cb)
	qb.GenerateCommands(cb)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	// Draw from the bottom
	style := renderer.TextStyle{Font: fsp.font, Color: lay.dark(renderer.RGB{R: .1, G: .1, B: .1})}
	y := lay.stripHeight - 1
	for i := scrollOffset; i < min(len(fsp.ACIDDisplayOrder), lay.visibleStrips+scrollOffset+1); i++ {
		acid := fsp.ACIDDisplayOrder[i]
		sfp := ctx.Client.State.GetFlightPlanForACID(acid)
		track := ctx.Client.State.Tracks[av.ADSBCallsign(acid)] // HAX: conflates callsign/ACID
		fsp.drawStrip(ctx, cb, acid, sfp, track, y, lay, style, bgColor, td, ld)
		y += lay.stripHeight
	}

	fsp.scrollbar.Draw(ctx, cb)

	cb.SetRGB(lay.dark(UIControlColor))
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	// Context menu drawn last so it renders on top of strip text and lines.
	fsp.drawContextMenu(ctx, cb, lay)
}

func (fsp *FlightStripPane) drawStrip(ctx *Context, cb *renderer.CommandBuffer, acid sim.ACID,
	sfp *sim.NASFlightPlan, track *sim.Track, y float32, lay stripLayout, style renderer.TextStyle,
	bgColor renderer.RGB, td *renderer.TextDrawBuilder, ld *renderer.LinesDrawBuilder) {

	x := float32(0)

	drawCol := func(line0, line1, line2 string, width float32, hlines bool) {
		td.AddText(line0, [2]float32{x + lay.indent, y - lay.vpad}, style)
		td.AddText(line1, [2]float32{x + lay.indent, y - lay.vpad - lay.stripHeight/3}, style)
		td.AddText(line2, [2]float32{x + lay.indent, y - lay.vpad - lay.stripHeight*2/3}, style)
		ld.AddLine([2]float32{x + width, y}, [2]float32{x + width, y - lay.stripHeight})
		if hlines {
			ld.AddLine([2]float32{x, y - lay.stripHeight/3}, [2]float32{x + width, y - lay.stripHeight/3})
			ld.AddLine([2]float32{x, y - lay.stripHeight*2/3}, [2]float32{x + width, y - lay.stripHeight*2/3})
		}
	}

	// Column 0: callsign, aircraft type, CID
	cid := fmt.Sprintf("%03d", sfp.StripCID)
	drawCol(string(sfp.ACID), sfp.CWTCategory+"/"+sfp.AircraftType, cid, lay.width0, false)

	// Extract fields that may come from either the track or the flight plan.
	depAirport, arrAirport := "", sfp.ArrivalAirport
	filedRoute := sfp.Route
	filedAlt := sfp.RequestedAltitude
	if track != nil {
		depAirport = track.DepartureAirport
		arrAirport = track.ArrivalAirport
		filedRoute = track.FiledRoute
		filedAlt = track.FiledAltitude
	}

	x += lay.width0
	switch sfp.TypeOfFlight {
	case av.FlightTypeDeparture:
		proposedTime := "P" + sfp.CoordinationTime.UTC().Format("1504")
		drawCol(sfp.AssignedSquawk.String(), proposedTime, strconv.Itoa(sfp.RequestedAltitude/100),
			lay.width1, true)

		x += lay.width1
		drawCol(depAirport, "", "", lay.width2, false)

		x += lay.width2
		route := formatRoute(filedRoute+" "+arrAirport, lay.fw, lay.widthCenter, 3)
		drawCol(route[0], route[1], route[2], lay.widthCenter, false)

	case av.FlightTypeArrival:
		drawCol(sfp.AssignedSquawk.String(), "", "", lay.width1, true)

		x += lay.width1
		arrivalTime := "A" + sfp.CoordinationTime.UTC().Format("1504")
		drawCol(arrivalTime, "", "", lay.width2, false)

		x += lay.width2
		drawCol(util.Select(sfp.Rules == av.FlightRulesIFR, "IFR", "VFR"), "", arrAirport,
			lay.widthCenter, false)

	default: // Overflight
		drawCol(sfp.AssignedSquawk.String(), "", "", lay.width1, true)

		x += lay.width1
		arrivalTime := "E" + sfp.CoordinationTime.UTC().Format("1504")
		drawCol(arrivalTime, "", "", lay.width2, false)

		x += lay.width2
		// TODO: e.g. "VFR/65" for altitude if it's VFR
		route := formatRoute(depAirport+" "+filedRoute+" "+arrAirport, lay.fw, lay.widthCenter, 2)
		drawCol(strconv.Itoa(filedAlt/100), route[0], route[1], lay.widthCenter, false)
	}

	// Annotations
	x += lay.widthCenter
	annots := util.Select(fsp.editingACID == acid, fsp.editingAnnotations, sfp.StripAnnotations)
	fsp.drawAnnotations(ctx, cb, acid, annots, x, y, lay, style, bgColor, td, ld)

	// Top separator line
	ld.AddLine([2]float32{0, y}, [2]float32{lay.drawWidth, y})
}

func (fsp *FlightStripPane) drawAnnotations(ctx *Context, cb *renderer.CommandBuffer, acid sim.ACID,
	annots [9]string, x, y float32, lay stripLayout, style renderer.TextStyle, bgColor renderer.RGB,
	td *renderer.TextDrawBuilder, ld *renderer.LinesDrawBuilder) {
	var editResult int
	cellH := lay.stripHeight / 3
	// Round vertical centering offset to integer pixels so glyph quads
	// stay pixel-aligned and don't appear smaller from subpixel sampling.
	vyOff := float32(int((cellH - lay.fh) / 2))
	for ai := range 9 {
		ix, iy := ai%3, ai/3
		yp := float32(int(y - float32(iy)*cellH - vyOff))

		if ctx.HaveFocus && fsp.editingACID == acid && ai == fsp.editingAnnotation {
			// Left-align while editing so the cursor doesn't shift as you type.
			xp := x + float32(ix)*lay.widthAnn + lay.indent
			cursorStyle := renderer.TextStyle{
				Font:            fsp.font,
				Color:           bgColor,
				DrawBackground:  true,
				BackgroundColor: style.Color,
			}
			editResult, _ = drawTextEdit(&fsp.editingAnnotations[ai], &fsp.annotationCursorPos,
				ctx.Keyboard, [2]float32{xp, yp}, style, cursorStyle, *ctx.KeyboardFocus, cb)

			if len(fsp.editingAnnotations[ai]) > 3 {
				fsp.editingAnnotations[ai] = fsp.editingAnnotations[ai][:3]
				fsp.annotationCursorPos = min(fsp.annotationCursorPos, len(fsp.editingAnnotations[ai]))
			}
		} else {
			bx, _ := fsp.font.BoundText(annots[ai], 0)
			xp := float32(int(x + float32(ix)*lay.widthAnn + (lay.widthAnn-float32(bx))/2))
			td.AddText(annots[ai], [2]float32{xp, yp}, style)
		}
	}

	// Process edit result after drawing all annotations to avoid
	// cascading tab-ahead.
	switch editResult {
	case textEditReturnEnter:
		ctx.Client.AnnotateFlightStrip(fsp.editingACID, fsp.editingAnnotations)
		fsp.editingACID = ""
		ctx.KeyboardFocus.Release()
	case textEditReturnNext:
		fsp.editingAnnotation = (fsp.editingAnnotation + 1) % 9
		fsp.annotationCursorPos = len(fsp.editingAnnotations[fsp.editingAnnotation])
	case textEditReturnPrev:
		fsp.editingAnnotation = (fsp.editingAnnotation + 8) % 9
		fsp.annotationCursorPos = len(fsp.editingAnnotations[fsp.editingAnnotation])
	}

	// Annotation grid lines
	ld.AddLine([2]float32{x, y - lay.stripHeight/3}, [2]float32{lay.drawWidth, y - lay.stripHeight/3})
	ld.AddLine([2]float32{x, y - lay.stripHeight*2/3}, [2]float32{lay.drawWidth, y - lay.stripHeight*2/3})
	for j := range 3 {
		xp := x + float32(j)*lay.widthAnn
		ld.AddLine([2]float32{xp, y}, [2]float32{xp, y - lay.stripHeight})
	}
}

func (fsp *FlightStripPane) handleMouse(ctx *Context, lay stripLayout, scrollOffset int) {
	if ctx.Mouse == nil {
		return
	}

	// Handle context menu release
	if fsp.contextMenuACID != "" && ctx.Mouse.Released[platform.MouseButtonSecondary] {
		if fsp.contextMenuExtent.Inside(ctx.Mouse.Pos) {
			ctx.Client.PushFlightStrip(fsp.contextMenuACID, fsp.contextMenuTarget)
		}
		fsp.contextMenuACID = ""
	}

	mx, my := ctx.Mouse.Pos[0], ctx.Mouse.Pos[1]
	stripIndex := int(my/lay.stripHeight) + scrollOffset
	annotationStartX := lay.drawWidth - 3*lay.widthAnn

	// Primary click
	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] && mx <= lay.drawWidth {
		if stripIndex < len(fsp.ACIDDisplayOrder) {
			if mx >= annotationStartX {
				// Click in annotation area: start editing
				acid := fsp.ACIDDisplayOrder[stripIndex]

				// Commit any previous editing session for a different strip
				if fsp.editingACID != "" && fsp.editingACID != acid {
					ctx.Client.AnnotateFlightStrip(fsp.editingACID, fsp.editingAnnotations)
				}

				if editFP := ctx.Client.State.GetFlightPlanForACID(acid); editFP != nil {
					fsp.editingACID = acid
					fsp.editingAnnotations = editFP.StripAnnotations

					ctx.KeyboardFocus.Take(fsp)
					col := int((mx - annotationStartX) / lay.widthAnn)
					innerRow := 2 - (int(my)%int(lay.stripHeight))/(int(lay.stripHeight)/3)
					ai := innerRow*3 + col
					fsp.editingAnnotation = math.Clamp(ai, 0, 8)
					fsp.annotationCursorPos = len(fsp.editingAnnotations[fsp.editingAnnotation])
				}
			} else {
				// Click outside annotations: select for drag, cancel editing
				if fsp.editingACID != "" {
					ctx.Client.AnnotateFlightStrip(fsp.editingACID, fsp.editingAnnotations)
					fsp.editingACID = ""
					ctx.KeyboardFocus.Release()
				}
				fsp.dragSelectedACID = fsp.ACIDDisplayOrder[stripIndex]
				fsp.dragStartPos = ctx.Mouse.Pos
				fsp.dragActive = false
			}
		}
	}

	// Right-click: show context menu for push
	if ctx.Mouse.Clicked[platform.MouseButtonSecondary] && mx <= lay.drawWidth {
		if stripIndex >= 0 && stripIndex < len(fsp.ACIDDisplayOrder) {
			acid := fsp.ACIDDisplayOrder[stripIndex]
			sfp := ctx.Client.State.GetFlightPlanForACID(acid)
			if sfp != nil {
				var toTCP sim.TCP
				if sfp.HandoffController != "" &&
					!ctx.Client.State.TCWControlsPosition(ctx.UserTCW, sfp.HandoffController) {
					toTCP = sfp.HandoffController
				} else if sfp.OwningTCW != ctx.UserTCW &&
					!ctx.Client.State.TCWControlsPosition(ctx.UserTCW, sfp.TrackingController) {
					toTCP = sfp.TrackingController
				}
				if toTCP != "" {
					fsp.contextMenuACID = acid
					fsp.contextMenuTarget = toTCP
					fsp.contextMenuPos = ctx.Mouse.Pos
				}
			}
		}
	}

	// Drag: dynamically reorder strips as the mouse moves
	if ctx.Mouse.Down[platform.MouseButtonPrimary] && fsp.dragSelectedACID != "" {
		dx := ctx.Mouse.Pos[0] - fsp.dragStartPos[0]
		dy := ctx.Mouse.Pos[1] - fsp.dragStartPos[1]
		if !fsp.dragActive && dx*dx+dy*dy > 0 {
			fsp.dragActive = true
		}
		if fsp.dragActive {
			srcIndex := slices.Index(fsp.ACIDDisplayOrder, fsp.dragSelectedACID)
			dstIndex := int(my/lay.stripHeight) + scrollOffset
			dstIndex = math.Clamp(dstIndex, 0, len(fsp.ACIDDisplayOrder)-1)

			if srcIndex != -1 && srcIndex != dstIndex {
				fsp.ACIDDisplayOrder = slices.Delete(fsp.ACIDDisplayOrder, srcIndex, srcIndex+1)
				fsp.ACIDDisplayOrder = slices.Insert(fsp.ACIDDisplayOrder, dstIndex, fsp.dragSelectedACID)
			}
		}
	}

	// Release or button-up: end drag
	if !ctx.Mouse.Down[platform.MouseButtonPrimary] {
		fsp.dragSelectedACID = ""
		fsp.dragActive = false
	}
}

// drawContextMenu renders the push-to context menu popup.
func (fsp *FlightStripPane) drawContextMenu(ctx *Context, cb *renderer.CommandBuffer, lay stripLayout) {
	if fsp.contextMenuACID == "" {
		return
	}

	menuText := "PUSH TO " + string(fsp.contextMenuTarget)
	bx, _ := fsp.font.BoundText(menuText, 0)
	pad := float32(8)
	menuW := float32(bx) + 2*pad
	menuH := lay.fh + 2*pad

	menuX := fsp.contextMenuPos[0]
	menuY := fsp.contextMenuPos[1]

	// Cache bounds for hit testing in handleMouse.
	fsp.contextMenuExtent = math.Extent2D{
		P0: [2]float32{menuX, menuY - menuH},
		P1: [2]float32{menuX + menuW, menuY},
	}

	// Drop shadow
	shadowOff := float32(3)
	shadowColor := util.Select(lay.darkMode,
		renderer.RGB{R: .03, G: .03, B: .04},
		renderer.RGB{R: .55, G: .55, B: .52})
	menuBg := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(menuBg)
	menuBg.AddQuad(
		[2]float32{menuX + shadowOff, menuY - shadowOff},
		[2]float32{menuX + menuW + shadowOff, menuY - shadowOff},
		[2]float32{menuX + menuW + shadowOff, menuY - menuH - shadowOff},
		[2]float32{menuX + shadowOff, menuY - menuH - shadowOff},
		shadowColor)

	// Background: warm cream, a step up from the strip background
	menuBg.AddQuad(
		[2]float32{menuX, menuY},
		[2]float32{menuX + menuW, menuY},
		[2]float32{menuX + menuW, menuY - menuH},
		[2]float32{menuX, menuY - menuH},
		lay.dark(renderer.RGB{R: 1, G: .97, B: .88}))
	menuBg.GenerateCommands(cb)

	// Border
	menuLD := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(menuLD)
	menuLD.AddLineLoop([][2]float32{
		{menuX, menuY},
		{menuX + menuW, menuY},
		{menuX + menuW, menuY - menuH},
		{menuX, menuY - menuH},
	})
	cb.SetRGB(lay.dark(renderer.RGB{R: .45, G: .42, B: .35}))
	cb.LineWidth(1, ctx.DPIScale)
	menuLD.GenerateCommands(cb)

	// Text
	menuTD := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(menuTD)
	menuTD.AddText(menuText, [2]float32{menuX + pad, menuY - pad},
		renderer.TextStyle{Font: fsp.font, Color: lay.dark(renderer.RGB{R: .1, G: .1, B: .1})})
	menuTD.GenerateCommands(cb)
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
			*cursor = max(*cursor-1, 0)
		}
		if keyboard.WasPressed(imgui.KeyRightArrow) {
			*cursor = min(*cursor+1, len(*s))
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

// panes/flightstrip.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

type FlightStripPane struct {
	FontSize int
	font     *renderer.Font

	DarkMode bool

	// Display ordering, reconciled each frame with the server list.
	ACIDDisplayOrder []sim.ACID

	// Drag-reorder state: draggingACID is the strip being moved.
	draggingACID sim.ACID

	// Annotation editing state: copied from server on click, written back via RPC on commit.
	// editingACID == "" means no editing is active.
	editingACID          sim.ACID
	editingAnnotation    int // which cell (0-8) is selected
	editingAnnotations   [9]string
	editingNeedsFocus    bool
	tabConsumedThisFrame bool
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
}

func (fsp *FlightStripPane) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	fsp.ACIDDisplayOrder = nil
	fsp.editingACID = ""
	fsp.draggingACID = ""
}

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
	if fsp.draggingACID != "" && !slices.Contains(fsp.ACIDDisplayOrder, fsp.draggingACID) {
		fsp.draggingACID = ""
	}
}

// commitAnnotations sends the current annotation edits to the server and
// also writes them to the local flight plan for immediate display, avoiding
// a flicker while waiting for the server's next state update.
func (fsp *FlightStripPane) commitAnnotations(c *client.ControlClient) {
	if sfp := c.State.GetFlightPlanForACID(fsp.editingACID); sfp != nil {
		sfp.StripAnnotations = fsp.editingAnnotations
	}
	c.AnnotateFlightStrip(fsp.editingACID, fsp.editingAnnotations)
}

var _ UIDrawer = (*FlightStripPane)(nil)

func (fsp *FlightStripPane) DisplayName() string { return "Flight Strips" }

func (fsp *FlightStripPane) DrawUI(p platform.Platform, config *platform.Config) {
	imgui.Checkbox("Night mode", &fsp.DarkMode)

	id := renderer.FontIdentifier{Name: fsp.font.Id.Name, Size: fsp.FontSize}
	if newFont, changed := renderer.DrawFontSizeSelector(&id); changed {
		fsp.FontSize = newFont.Size
		fsp.font = newFont
	}
}

///////////////////////////////////////////////////////////////////////////
// Flight strip layout and drawing

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

///////////////////////////////////////////////////////////////////////////
// DrawWindow renders the flight strip pane as a floating imgui window
// using imgui tables for layout.

func (fsp *FlightStripPane) DrawWindow(show *bool, c *client.ControlClient,
	p platform.Platform, lg *log.Logger) {

	fsp.reconcileOrder(c.State.FlightStripACIDs)

	imgui.SetNextWindowSizeConstraints(imgui.Vec2{X: 400, Y: 200}, imgui.Vec2{X: 4096, Y: 4096})
	imgui.BeginV("Flight Strips", show, 0)
	if fsp.font != nil {
		fsp.font.ImguiPush()
	}

	// Commit annotations if the window lost focus while editing.
	if fsp.editingACID != "" && !imgui.IsWindowFocused() {
		fsp.commitAnnotations(c)
		fsp.editingACID = ""
	}

	// Push colors for dark/light mode.
	var textColor, bgColor, borderStrong, borderLight imgui.Vec4
	if fsp.DarkMode {
		textColor = imgui.Vec4{X: 0.82, Y: 0.85, Z: 0.88, W: 1}
		bgColor = imgui.Vec4{X: 0.11, Y: 0.12, Z: 0.14, W: 1}
		borderStrong = imgui.Vec4{X: 0.35, Y: 0.38, Z: 0.42, W: 1}
		borderLight = imgui.Vec4{X: 0.25, Y: 0.28, Z: 0.32, W: 1}
	} else {
		textColor = imgui.Vec4{X: 0.1, Y: 0.1, Z: 0.1, W: 1}
		bgColor = imgui.Vec4{X: 0.9, Y: 0.9, Z: 0.85, W: 1}
		borderStrong = imgui.Vec4{X: 0.5, Y: 0.5, Z: 0.45, W: 1}
		borderLight = imgui.Vec4{X: 0.6, Y: 0.6, Z: 0.55, W: 1}
	}
	imgui.PushStyleColorVec4(imgui.ColText, textColor)
	imgui.PushStyleColorVec4(imgui.ColTableRowBg, bgColor)
	imgui.PushStyleColorVec4(imgui.ColTableRowBgAlt, bgColor)
	imgui.PushStyleColorVec4(imgui.ColTableBorderStrong, borderStrong)
	imgui.PushStyleColorVec4(imgui.ColTableBorderLight, borderLight)
	imgui.PushStyleColorVec4(imgui.ColHeaderHovered, imgui.Vec4{})
	imgui.PushStyleColorVec4(imgui.ColHeaderActive, imgui.Vec4{})

	fw := imgui.CalcTextSizeV("X", false, 0).X

	type stripRect struct {
		acid     sim.ACID
		min, max imgui.Vec2
	}
	var stripRects []stripRect
	fsp.tabConsumedThisFrame = false

	for i, acid := range fsp.ACIDDisplayOrder {
		sfp := c.State.GetFlightPlanForACID(acid)
		if sfp == nil {
			continue
		}
		track := c.State.Tracks[av.ADSBCallsign(acid)]

		imgui.PushIDInt(int32(i))
		tableMin, tableMax := fsp.drawStripImgui(acid, sfp, track, c, fw)
		imgui.PopID()

		if tableMin.X != tableMax.X { // table was rendered
			stripRects = append(stripRects, stripRect{acid: acid, min: tableMin, max: tableMax})

			// Draw yellow border around the strip being dragged.
			if fsp.draggingACID == acid {
				yellowCol := imgui.ColorU32Vec4(imgui.Vec4{X: 1, Y: 1, Z: 0, W: 1})
				imgui.WindowDrawList().AddRectV(tableMin, tableMax, yellowCol, 0, 0, 2)
			}
		}
	}

	// Dynamic reorder: while dragging, place the strip at the visual
	// position that the mouse overlaps.
	if fsp.draggingACID != "" {
		mouseY := imgui.MousePos().Y
		srcIdx := slices.Index(fsp.ACIDDisplayOrder, fsp.draggingACID)
		if srcIdx >= 0 {
			// Find the strip the mouse overlaps.
			targetIdx := -1
			for _, r := range stripRects {
				if r.acid == fsp.draggingACID {
					continue
				}
				if mouseY >= r.min.Y && mouseY <= r.max.Y {
					targetIdx = slices.Index(fsp.ACIDDisplayOrder, r.acid)
					break
				}
			}

			if targetIdx >= 0 && targetIdx != srcIdx {
				without := slices.Clone(fsp.ACIDDisplayOrder)
				without = slices.Delete(without, srcIdx, srcIdx+1)
				fsp.ACIDDisplayOrder = slices.Insert(without, targetIdx, fsp.draggingACID)
			}
		}
		if imgui.IsMouseReleased(0) {
			fsp.draggingACID = ""
		}
	}

	imgui.PopStyleColorV(7)

	if fsp.font != nil {
		imgui.PopFont()
	}
	imgui.End()
}

// drawStripImgui renders a single flight strip as an imgui table with
// 7 columns (callsign, squawk/time, airport, route, ann0, ann1, ann2)
// and 3 rows. It returns the table's screen-space bounding rect for
// drag-reorder hit testing (zero if the table was not rendered).
func (fsp *FlightStripPane) drawStripImgui(acid sim.ACID, sfp *sim.NASFlightPlan,
	track *sim.Track, c *client.ControlClient, fw float32) (tableMin, tableMax imgui.Vec2) {

	tableFlags := imgui.TableFlagsBorders | imgui.TableFlagsSizingFixedFit |
		imgui.TableFlagsRowBg | imgui.TableFlagsNoHostExtendX
	if !imgui.BeginTableV("strip", 7, tableFlags, imgui.Vec2{X: -1}, 0) {
		return
	}

	// Column widths matching the reference strip layout.
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 8*fw, 0) // callsign/type/CID
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 5*fw, 0) // squawk/time/alt
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 5*fw, 0) // airport
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthStretch, 0, 0)  // route
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 3*fw, 0) // annotation col 0
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 3*fw, 0) // annotation col 1
	imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, 3*fw, 0) // annotation col 2

	// Build cell content for the 3x7 grid.
	var cells [3][4]string // columns 0-3 only; annotations handled separately

	cells[0][0] = string(sfp.ACID)
	cells[1][0] = sfp.CWTCategory + "/" + sfp.AircraftType
	cells[2][0] = fmt.Sprintf("%03d", sfp.StripCID)

	depAirport, arrAirport := "", sfp.ArrivalAirport
	filedRoute := sfp.Route
	filedAlt := sfp.RequestedAltitude
	if track != nil {
		depAirport = track.DepartureAirport
		arrAirport = track.ArrivalAirport
		filedRoute = track.FiledRoute
		filedAlt = track.FiledAltitude
	}

	// Estimate route column width for word-wrapping.
	routeWidth := imgui.ContentRegionAvail().X - (8+5+5+3*3)*fw
	if routeWidth < 20*fw {
		routeWidth = 20 * fw
	}

	switch sfp.TypeOfFlight {
	case av.FlightTypeDeparture:
		proposedTime := "P" + sfp.CoordinationTime.UTC().Format("1504")
		cells[0][1] = sfp.AssignedSquawk.String()
		cells[1][1] = proposedTime
		cells[2][1] = strconv.Itoa(sfp.RequestedAltitude / 100)

		cells[0][2] = depAirport

		route := formatRoute(filedRoute+" "+arrAirport, fw, routeWidth, 3)
		cells[0][3], cells[1][3], cells[2][3] = route[0], route[1], route[2]

	case av.FlightTypeArrival:
		cells[0][1] = sfp.AssignedSquawk.String()

		arrivalTime := "A" + sfp.CoordinationTime.UTC().Format("1504")
		cells[0][2] = arrivalTime

		cells[0][3] = util.Select(sfp.Rules == av.FlightRulesIFR, "IFR", "VFR")
		cells[2][3] = arrAirport

	default: // Overflight
		cells[0][1] = sfp.AssignedSquawk.String()

		arrivalTime := "E" + sfp.CoordinationTime.UTC().Format("1504")
		cells[0][2] = arrivalTime

		route := formatRoute(depAirport+" "+filedRoute+" "+arrAirport, fw, routeWidth, 2)
		cells[0][3] = strconv.Itoa(filedAlt / 100)
		cells[1][3], cells[2][3] = route[0], route[1]
	}

	// Callback that enforces a 3-character limit by truncating after each edit.
	charLimit := func(data imgui.InputTextCallbackData) int {
		if data.BufTextLen() > 3 {
			data.DeleteChars(3, data.BufTextLen()-3)
		}
		return 0
	}

	// Draw 3 rows.
	annots := util.Select(fsp.editingACID == acid, fsp.editingAnnotations, sfp.StripAnnotations)
	for row := range 3 {
		imgui.TableNextRow()

		// Column 0: selectable used as a drag handle for reordering.
		imgui.TableSetColumnIndex(0)
		imgui.SelectableBoolV(fmt.Sprintf("%s##row%d", cells[row][0], row), false, imgui.SelectableFlagsSpanAllColumns|imgui.SelectableFlagsAllowOverlap, imgui.Vec2{})

		// Initiate drag when column 0 is clicked and dragged. The
		// Selectable stays "active" while the mouse button is held,
		// so IsItemActive remains true during the drag.
		if fsp.draggingACID == "" && imgui.IsItemActive() && imgui.IsMouseDragging(0) {
			fsp.draggingACID = acid
		}

		// Columns 1-3: plain text.
		for col := 1; col < 4; col++ {
			imgui.TableSetColumnIndex(int32(col))
			imgui.TextUnformatted(cells[row][col])
		}

		// Columns 4-6: annotation cells (editable).
		for acol := range 3 {
			imgui.TableSetColumnIndex(int32(4 + acol))
			ai := row*3 + acol

			imgui.PushIDInt(int32(ai))
			if fsp.editingACID == acid && fsp.editingAnnotation == ai {
				imgui.SetNextItemWidth(-1)
				if fsp.editingNeedsFocus {
					imgui.SetKeyboardFocusHere()
					fsp.editingNeedsFocus = false
				}

				flags := imgui.InputTextFlagsEnterReturnsTrue | imgui.InputTextFlagsCallbackEdit
				tabPressed := imgui.IsKeyPressedBool(imgui.KeyTab) && !fsp.tabConsumedThisFrame

				if imgui.InputTextWithHint("##ann", "", &fsp.editingAnnotations[ai], flags, charLimit) {
					// Enter pressed: commit annotations.
					fsp.commitAnnotations(c)
					fsp.editingACID = ""
				} else if tabPressed {
					// Tab cycles to the next annotation cell without committing.
					// Must be checked before IsItemDeactivatedAfterEdit because
					// imgui deactivates the input on Tab.
					fsp.tabConsumedThisFrame = true
					if imgui.CurrentIO().KeyShift() {
						fsp.editingAnnotation = (fsp.editingAnnotation + 8) % 9
					} else {
						fsp.editingAnnotation = (fsp.editingAnnotation + 1) % 9
					}
					fsp.editingNeedsFocus = true
				} else if imgui.IsItemDeactivatedAfterEdit() {
					// Lost focus for another reason: commit annotations.
					fsp.commitAnnotations(c)
					fsp.editingACID = ""
				}

				// Escape clears the cell.
				if imgui.IsKeyPressedBool(imgui.KeyEscape) {
					fsp.editingAnnotations[ai] = ""
				}
			} else {
				label := annots[ai]
				if label == "" {
					label = " "
				}
				if imgui.SelectableBool(label) && fsp.draggingACID == "" {
					// Commit any previous editing session for a different strip.
					if fsp.editingACID != "" && fsp.editingACID != acid {
						fsp.commitAnnotations(c)
					}
					fsp.editingACID = acid
					fsp.editingAnnotation = ai
					if editFP := c.State.GetFlightPlanForACID(acid); editFP != nil {
						fsp.editingAnnotations = editFP.StripAnnotations
					}
					fsp.editingNeedsFocus = true
				}
				// Allow drag initiation from annotation cells too.
				if fsp.draggingACID == "" && imgui.IsItemActive() && imgui.IsMouseDragging(0) {
					fsp.draggingACID = acid
				}
			}
			imgui.PopID()
		}
	}

	imgui.EndTable()
	tableMin = imgui.ItemRectMin()
	tableMax = imgui.ItemRectMax()

	// Right-click context menu for push.
	if imgui.BeginPopupContextItemV("push_ctx", imgui.PopupFlagsMouseButtonRight) {
		var toTCP sim.TCP
		if sfp.HandoffController != "" &&
			!c.State.TCWControlsPosition(c.State.UserTCW, sfp.HandoffController) {
			toTCP = sfp.HandoffController
		} else if sfp.OwningTCW != c.State.UserTCW &&
			!c.State.TCWControlsPosition(c.State.UserTCW, sfp.TrackingController) {
			toTCP = sfp.TrackingController
		}
		if toTCP != "" {
			if imgui.SelectableBoolV(
				"PUSH TO "+string(toTCP), false, imgui.SelectableFlagsNone, imgui.Vec2{}) {
				c.PushFlightStrip(acid, toTCP)
			}
		} else {
			imgui.TextDisabled("No push target")
		}
		imgui.EndPopup()
	}

	return
}

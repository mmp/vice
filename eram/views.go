// eram/views.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Implementations of all Views (main view display and menu popups) are in this file, except
// for CRR, which is complex enough to get its own file: crr.go

package eram

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

///////////////////////////////////////////////////////////////////////////
// ALTIM SET

// drawAltimSetView renders the ALTIM SET floating window.
func (ep *ERAMPane) drawAltimSetView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.AltimSet.Visible {
		return
	}

	listFont := ep.ERAMFont(ps.AltimSet.Font)
	textWidth := func(s string) float32 { return listFont.LayoutBounds(s, 0).Width() }
	textColor := ep.viewTextColor(ps.AltimSet.Bright)

	var rows []Row
	for _, icao := range ep.AltimSetAirports {
		rows = append(rows, altimRow(ctx, icao, textColor, listFont, textWidth))
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.AltimSet.Position,
		ID:         "altim-set",
		Title:      "ALTIM SET",
		Opaque:     ps.AltimSet.Opaque,
		ShowBorder: ps.AltimSet.ShowBorder,
		Brightness: ps.AltimSet.Bright,
		OnMenu: ep.makeViewMenu(ctx, "altim-set", 8,
			func(pb popupBase) popup { return &altimSetPopup{popupBase: pb} }),
		MinimizeTarget: &ps.AltimSet.Visible,
		RowSource: &ViewRowSource{
			Rows:                  rows,
			FontIndex:             ps.AltimSet.Font,
			ContentChars:          len("MMMM   1353 999  "),
			MaxCols:               ps.AltimSet.Col,
			VisibleRows:           ps.AltimSet.Lines,
			ReserveBadgeColumn:    true,
			ShowBadges:            ps.AltimSet.ShowIndicators,
			RowSpacing:            RowSpacingCompact,
			ScrollState:           &ep.altimSetScroll,
			EmptyKeepsColumnWidth: true,
			SelectableState:       &ep.altimSetSelect,
			OnRowDelete: func(icao string) {
				ep.AltimSetAirports = slices.DeleteFunc(ep.AltimSetAirports, func(ap string) bool { return ap == icao })
			},
		},
	})
}

// altimRow builds one ALTIM SET row from an airport's METAR (or a missing-data
// placeholder). The altimeter portion is underlined via AfterDraw when below
// the standard 29.92 inHg. The badge column and row color are filled in by
// the View; this function just constructs the row text and the AfterDraw
// underline (which captures `color` for the line).
func altimRow(ctx *panes.Context, icao string, color renderer.RGB,
	font *renderer.Font, textWidth func(string) float32) Row {

	displayID := altimDisplayID(icao)
	metar, hasMetar := ctx.Client.State.METAR[icao]
	if !hasMetar {
		return Row{ID: icao, Body: fmt.Sprintf("%-4s   -M-  ", displayID)}
	}
	timeStr, altStr, altRaw := altimMetarForDisplay(metar)
	prefix := fmt.Sprintf("%-4s  ", displayID)
	timeField := fmt.Sprintf("%4s", timeStr)
	mid := " "
	altField := fmt.Sprintf("%3s", altStr)
	line := prefix + timeField + mid + altField + "  "

	row := Row{ID: icao, Body: line}
	if altRaw > 0 && altRaw < 2992 && altStr != "..." {
		offsetX := textWidth(prefix) + textWidth(timeField) + textWidth(mid)
		fieldW := textWidth(altField)
		// Position the underline 2px below the ink bottom of the digits so it
		// tracks the actual glyph descent across font sizes.
		inkBottom := font.InkBounds(altField, 0).P0[1]
		row.AfterDraw = func(_ math.Extent2D, bodyOrigin [2]float32, b *ViewBuilders) {
			uy := bodyOrigin[1] + inkBottom - 2
			ux := bodyOrigin[0] + offsetX
			b.Ld.AddLine([2]float32{ux, uy}, [2]float32{ux + fieldW, uy}, color)
		}
	}
	return row
}

// altimMetarForDisplay returns the formatted METAR data for display, extracting
// the observation time (HHMM) and 3-digit altimeter setting from a wx.METAR object.
func altimMetarForDisplay(metar wx.METAR) (timeStr string, altStr string, altRaw int) {
	// Extract HHMM from the METAR observation time
	timeStr = fmt.Sprintf("%02d%02d", metar.Time.Hour(), metar.Time.Minute())

	// Convert altimeter from inHg to 4-digit A-setting format
	// A-setting is always 4 digits (e.g., 2992, 3012)
	altRaw = int(math.Round(metar.Altimeter_inHg() * 100))
	if altRaw > 0 {
		// Last 3 digits of the 4-digit A-setting
		altStr = fmt.Sprintf("%03d", altRaw%1000)
	}

	return
}

// altimDisplayID returns the short display identifier for an ICAO code.
// US airports (K + 3-letter IATA), Pacific territories (P + 3-letter IATA),
// and Caribbean territories (T + 3-letter IATA) drop the leading letter for display.
func altimDisplayID(icao string) string {
	if len(icao) == 4 && (icao[0] == 'K' || icao[0] == 'P' || icao[0] == 'T') {
		return icao[1:]
	}
	return icao
}

// altimSetPopup is the popup-interface impl for the ALTIM SET configuration menu.
// The origin is captured at open time from the view's current geometry, since
// the view width depends on dynamic state (column count).
type altimSetPopup struct {
	popupBase
}

func (a *altimSetPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.AltimSet.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.AltimSet.ShowBorder, "BORDER"),
		ep.makeToggleMenuItem(&ps.AltimSet.ShowIndicators, "TEAROFF"),
		{Label: fmt.Sprintf("LINES %d", ps.AltimSet.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text,
			OnClick: func(_ ERAMMenuClickType) bool {
				handleClick(ep, &ps.AltimSet.Lines, 3, 24, 1)
				maxOffset := max(0, len(ep.AltimSetAirports)-ps.AltimSet.Lines)
				ep.altimSetScroll.Offset = math.Clamp(ep.altimSetScroll.Offset, 0, maxOffset)
				return false
			}},
		makeIntMenuItem(ep, &ps.AltimSet.Col, "COL", 1, 4, 1),
		makeIntMenuItem(ep, &ps.AltimSet.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.AltimSet.Bright, "BRIGHT", 0, 100, 1),
		{Label: "TEMPLATE", BgColor: colors.popup.backgroundBlack, Color: colors.popup.text},
	}

	cfg := ERAMMenuConfig{
		Title: "AS",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2), // Menu always uses FONT 2, not affected by FONT setting
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, a.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// CODE

func (ep *ERAMPane) drawBeaconCodeView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.BeaconCodeView.Visible {
		return
	}

	// 4-digit beacon code plus a trailing "." for manually-added entries —
	// space for the dot is reserved on every cell so columns line up.
	const cellChars = 5

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.BeaconCodeView.Position,
		ID:         "beacon",
		Title:      "CODE",
		Opaque:     ps.BeaconCodeView.Opaque,
		ShowBorder: ps.BeaconCodeView.ShowBorder,
		Brightness: ps.BeaconCodeView.Bright,
		OnMenu: ep.makeViewMenu(ctx, "beacon", 7,
			func(pb popupBase) popup { return &beaconCodeViewPopup{popupBase: pb} }),
		MinimizeTarget: &ps.BeaconCodeView.Visible,
		RowSource: &ViewRowSource{
			Rows:                    beaconCodeRows(ctx, ep, ps),
			FontIndex:               ps.BeaconCodeView.Font,
			ContentChars:            cellChars,
			MaxCols:                 ps.BeaconCodeView.Col,
			VisibleRows:             ps.BeaconCodeView.Lines,
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
		},
	})
}

// beaconCodeRows returns the flat row list shown by drawBeaconCodeView:
// manually-added codes (with trailing ".") plus the codes of aircraft whose
// tracks we own, in the order dictated by SortManual. Row.Color is left zero
// — the View fills in the default text color.
func beaconCodeRows(ctx *panes.Context, ep *ERAMPane, ps *Preferences) []Row {
	// Codes of aircraft whose tracks we own.
	var owned []av.Squawk
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			owned = append(owned, trk.Squawk)
		}
	}

	makeRow := func(code av.Squawk) Row {
		s := code.String()
		if slices.Contains(ep.AddedBeaconCodes, code) {
			s += "."
		}
		return Row{ID: code.String(), Body: s}
	}

	var rows []Row
	if ps.BeaconCodeView.SortManual {
		// First the (sorted) manually added codes, then the sorted owned codes
		// that aren't also manually added.
		added := slices.Clone(ep.AddedBeaconCodes)
		slices.Sort(added)
		for _, code := range added {
			rows = append(rows, makeRow(code))
		}
		ownedOnly := slices.Clone(owned)
		ownedOnly = slices.DeleteFunc(ownedOnly, func(c av.Squawk) bool {
			return slices.Contains(ep.AddedBeaconCodes, c)
		})
		slices.Sort(ownedOnly)
		for _, code := range ownedOnly {
			rows = append(rows, makeRow(code))
		}
	} else {
		// All codes (added + owned) merged and sorted; manual ones keep their ".".
		all := append(slices.Clone(ep.AddedBeaconCodes), owned...)
		slices.Sort(all)
		all = slices.Compact(all)
		for _, code := range all {
			rows = append(rows, makeRow(code))
		}
	}
	return rows
}

type beaconCodeViewPopup struct {
	popupBase
}

func (b *beaconCodeViewPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.BeaconCodeView.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Lines, "LINES", 3, 24, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Col, "COL", 1, 5, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Bright, "BRIGHT", 0, 100, 1),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.SortManual, "SORT MAN"),
	}

	cfg := ERAMMenuConfig{
		Title: "CODE",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, b.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// MCA - Message Composition Area

func (ep *ERAMPane) drawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
}

func (ep *ERAMPane) startDrawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMInputFont(),
		Color:       colors.toolbar.text,
		LineSpacing: 0,
	}

	if ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

// drawMessageCompositionArea renders the MCA: a feedback box below an input
// box (where inputH grows beyond 38px if wrapped input exceeds it). Both
// boxes share black bg and a white border; the seam between them is drawn by
// View as part of the outer border (and a separator line in the body). Width
// fits ps.MCA.Width characters of the selected font plus 2px side padding.
func (ep *ERAMPane) drawMessageCompositionArea(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	font := ep.ERAMFont(ps.MCA.Font)
	cols := ps.MCA.Width
	width := float32(cols)*charWidth(font) + 4
	lineSpacing := int(float32(font.Size) * 0.4)
	feedbackH := font.LayoutBounds("0", lineSpacing).Height()*float32(ps.MCA.PALines) + 4

	// Compute input box height (grows with wrapped text).
	input := ep.Input.String() + "_"
	inText, _ := util.WrapText(input, cols, 0, true, true)
	h := font.LayoutBounds(inText, lineSpacing).Height()
	inputH := max(float32(38), h+4)

	// Feedback rendering: icon (success/error) in its own color, then the
	// message in default text color. The wrap input includes the icon so the
	// available width on the first line accounts for it.
	var feedbackIcon, feedbackRest string
	var feedbackIconColor renderer.RGB
	if ep.feedbackArea.kind != feedbackNone {
		switch ep.feedbackArea.kind {
		case feedbackSuccess:
			feedbackIcon, feedbackIconColor = checkMark, colors.successGreen
		case feedbackError:
			feedbackIcon, feedbackIconColor = xMark, colors.errorRed
		}
		wrapped, _ := util.WrapText(feedbackIcon+" "+ep.feedbackArea.msg, cols, 0, true, true)
		// wrapped starts with the icon char; the rest starts with the space
		// that pushes the message past the icon's column on the first line.
		feedbackRest = wrapped[len(feedbackIcon):]
	}

	// ps.MCA.Position is the top-left of the feedback box (prefs
	// semantics). View sees the top-left of the whole envelope (input top).
	viewPos := [2]float32{ps.MCA.Position[0], ps.MCA.Position[1] + inputH}

	v := View{
		Position:   &viewPos,
		ID:         "mca",
		Width:      width,
		BodyHeight: inputH + feedbackH,
		ShowBorder: true,
		Brightness: ps.Brightness.Border,
		OnBodyTertiaryMenu: ep.makeViewMenu(ctx, "mca", 4,
			func(pb popupBase) popup { return &mcaPopup{popupBase: pb} }),
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			// body.P1 = top-right (top of input); body.P0 = bottom-left (bottom of feedback).
			seamY := body.P1[1] - inputH

			// Seam line between input and feedback.
			borderColor := ps.Brightness.Border.ScaleRGB(colors.view.border)
			b.Ld.AddLine([2]float32{body.P0[0], seamY}, [2]float32{body.P1[0], seamY}, borderColor)

			inputTopLeft := [2]float32{body.P0[0], body.P1[1]}
			feedbackTopLeft := [2]float32{body.P0[0], seamY}
			textStyle := renderer.TextStyle{Font: font, Color: ps.MCA.Bright.ScaleRGB(colors.toolbar.text), LineSpacing: lineSpacing}

			// Input text (top box).
			b.Td.AddText(inText, [2]float32{inputTopLeft[0] + 2, inputTopLeft[1] - 2}, textStyle)

			// Feedback text (bottom box). Two AddText calls share the same
			// origin so newline resets in `feedbackRest` land at column 0.
			loc := [2]float32{feedbackTopLeft[0] + 2, feedbackTopLeft[1] - 2}
			iconStyle := renderer.TextStyle{Font: font, Color: ps.MCA.Bright.ScaleRGB(feedbackIconColor), LineSpacing: lineSpacing}
			b.Td.AddText(feedbackIcon, loc, iconStyle)
			b.Td.AddText(feedbackRest, loc, textStyle)
		},
	}
	ep.DrawView(ctx, transforms, cb, v)

	// View may have updated viewPos via drag; translate back to prefs semantics.
	ps.MCA.Position = [2]float32{viewPos[0], viewPos[1] - inputH}
}

// mcaPopup is the configuration menu for the Message Composition Area.
type mcaPopup struct {
	popupBase
}

func (m *mcaPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		makeIntMenuItem(ep, &ps.MCA.PALines, "PA LINES", 1, 50, 1),
		makeIntMenuItem(ep, &ps.MCA.Width, "WIDTH", 30, 50, 20),
		makeIntMenuItem(ep, &ps.MCA.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.MCA.Bright, "BRIGHT", 0, 100, 1),
	}

	cfg := ERAMMenuConfig{
		Title: "MCA",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, m.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// RA: Response Area

// drawResponseArea renders the RA: a single box with the wrapped
// response-area text. Width fits ps.RA.Width characters of the selected font
// plus 2px side padding.
func (ep *ERAMPane) drawResponseArea(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	const height = 77
	font := ep.ERAMFont(ps.RA.Font)
	cols := ps.RA.Width
	width := float32(cols)*charWidth(font) + 4
	lineSpacing := int(float32(font.Size) * 0.4)

	wrapped, _ := util.WrapText(ep.responseArea, cols, 0, true, false)
	textColor := ps.RA.Bright.ScaleRGB(colors.toolbar.text)

	v := View{
		Position:   &ps.RA.Position,
		ID:         "ra",
		Width:      width,
		BodyHeight: height,
		ShowBorder: true,
		Brightness: ps.RA.Bright,
		OnBodyTertiaryMenu: ep.makeViewMenu(ctx, "ra", 4,
			func(pb popupBase) popup { return &raPopup{popupBase: pb} }),
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			topLeft := [2]float32{body.P0[0], body.P1[1]}
			b.Td.AddText(wrapped, [2]float32{topLeft[0] + 2, topLeft[1] - 2},
				renderer.TextStyle{Font: font, Color: textColor, LineSpacing: lineSpacing})
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// raPopup is the configuration menu for the Response Area.
type raPopup struct {
	popupBase
}

func (r *raPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		makeIntMenuItem(ep, &ps.RA.Width, "WIDTH", 25, 50, 25),
		makeIntMenuItem(ep, &ps.RA.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.RA.Bright, "BRIGHT", 0, 100, 1),
		{Label: "CLEAR", BgColor: colors.popup.backgroundBlack, Color: colors.popup.text, OnClick: func(_ ERAMMenuClickType) bool {
			ep.responseArea = ""
			return false
		}},
	}

	cfg := ERAMMenuConfig{
		Title: "RA",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, r.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// Time

func (ep *ERAMPane) drawTimeView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	if ps.TimeView.Position == [2]float32{} {
		ps.TimeView.Position = [2]float32{10, ctx.PaneExtent.Height() - 300}
	}

	font := ep.ERAMFont(ps.TimeView.Font)
	textColor := ps.TimeView.Bright.ScaleRGB(colors.view.clockText)

	timeStr := ctx.Client.State.SimTime.Format("1504 05")

	ext := font.InkBounds(timeStr, 0)
	width := ext.Width() + 8
	height := ext.Height() + 8

	v := View{
		Position:     &ps.TimeView.Position,
		ID:           "clock",
		Width:        width,
		BodyHeight:   height,
		ShowBorder:   ps.TimeView.ShowBorder,
		Opaque:       ps.TimeView.Opaque,
		Brightness:   ps.TimeView.Bright,
		OpaqueOnlyBg: true,
		OnBodyTertiaryMenu: ep.makeViewMenu(ctx, "clock", 4,
			func(pb popupBase) popup { return &timeViewPopup{popupBase: pb} }),
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			center := [2]float32{(body.P0[0] + body.P1[0]) / 2, (body.P0[1] + body.P1[1]) / 2}
			b.Td.AddTextCentered(timeStr, center, renderer.TextStyle{Font: font, Color: textColor})
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// timeViewPopup is the configuration menu for the Time View (clock).
type timeViewPopup struct {
	popupBase
}

func (t *timeViewPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.TimeView.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.TimeView.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.TimeView.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.TimeView.Bright, "BRIGHT", 0, 100, 1),
	}

	cfg := ERAMMenuConfig{
		Title: "TIME",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, t.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// WX View

// wxDisplayID returns the short display identifier for an ICAO code.
// US airports (K + 3-letter IATA), Pacific territories (P + 3-letter IATA),
// and Caribbean territories (T + 3-letter IATA) drop the leading letter for display.
func wxDisplayID(icao string) string {
	if len(icao) == 4 && (icao[0] == 'K' || icao[0] == 'P' || icao[0] == 'T') {
		return icao[1:]
	}
	return icao
}

// drawWXView renders the WX floating window.
func (ep *ERAMPane) drawWXView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.WX.Visible {
		return
	}

	var rows []Row
	for _, icao := range ep.WXReportStations {
		rows = append(rows, Row{ID: icao, Label: wxDisplayID(icao), Body: wxMetarBody(ctx, icao)})
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.WX.Position,
		ID:         "wx",
		Title:      "WX",
		Opaque:     ps.WX.Opaque,
		ShowBorder: ps.WX.ShowBorder,
		Brightness: ps.WX.Bright,
		OnMenu: ep.makeViewMenu(ctx, "wx", 6,
			func(pb popupBase) popup { return &wxPopup{popupBase: pb} }),
		MinimizeTarget: &ps.WX.Visible,
		RowSource: &ViewRowSource{
			Rows:               rows,
			FontIndex:          ps.WX.Font,
			ContentChars:       24,
			MaxCols:            1,
			VisibleRows:        ps.WX.Lines,
			ReserveBadgeColumn: true,
			ShowBadges:         ps.WX.ShowIndicators,
			RowSpacing:         RowSpacingAiry,
			ScrollState:        &ep.wxScroll,
			SelectableState:    &ep.wxSelect,
			OnRowDelete: func(icao string) {
				ep.WXReportStations = slices.DeleteFunc(ep.WXReportStations, func(st string) bool { return st == icao })
			},
		},
	})
}

// wxMetarBody returns the row body text for a station: the wrappable METAR
// text (HHMM + remaining fields) when METAR data is available, or "-M-" as
// a status placeholder otherwise.
func wxMetarBody(ctx *panes.Context, icao string) string {
	metar, ok := ctx.Client.State.METAR[icao]
	if !ok || metar.Raw == "" {
		return "-M-"
	}

	// Find the time-group token: digits followed by 'Z'. (Plain strings.Index
	// would match the 'Z' inside station IDs like "CYYZ".)
	rawFields := strings.Fields(metar.Raw)
	timeField := -1
	for j, f := range rawFields {
		if len(f) < 5 || f[len(f)-1] != 'Z' {
			continue
		}
		allDigits := true
		for _, c := range f[:len(f)-1] {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			timeField = j
			break
		}
	}
	if timeField == -1 {
		return "-M-"
	}

	tok := rawFields[timeField]
	body := tok[len(tok)-5 : len(tok)-1]
	if timeField+1 < len(rawFields) {
		body += " " + strings.Join(rawFields[timeField+1:], " ")
	}
	return body
}

// wxPopup is the popup-interface impl for the WX REPORT configuration menu.
// The origin is captured at open time from the view's current geometry.
type wxPopup struct {
	popupBase
}

func (w *wxPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.WX.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.WX.ShowBorder, "BORDER"),
		ep.makeToggleMenuItem(&ps.WX.ShowIndicators, "TEAROFF"),
		{Label: fmt.Sprintf("LINES %d", ps.WX.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text,
			OnClick: func(_ ERAMMenuClickType) bool {
				handleClick(ep, &ps.WX.Lines, 3, 24, 1)
				maxOffset := max(0, len(ep.WXReportStations)-ps.WX.Lines)
				ep.wxScroll.Offset = math.Clamp(ep.wxScroll.Offset, 0, maxOffset)
				return false
			}},
		makeIntMenuItem(ep, &ps.WX.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.WX.Bright, "BRIGHT", 0, 100, 1),
	}

	cfg := ERAMMenuConfig{
		Title: "WX",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, w.origin, cfg)
}

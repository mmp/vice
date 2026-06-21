package eram

import (
	"fmt"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/wx"
)

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

// drawAltimSetView renders the ALTIM SET floating window.
func (ep *ERAMPane) drawAltimSetView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.AltimSet.Visible {
		return
	}

	listFont := ep.clampedFont(ps.AltimSet.Font, 1, 3)
	titleFont := ep.ERAMFont(2)
	bright := radar.Brightness(ps.AltimSet.Bright)
	lineH := lineHeight(listFont)
	textColor := bright.ScaleRGB(colors.view.text)

	numRows := len(ep.AltimSetAirports)
	visibleRows := math.Clamp(ps.AltimSet.Lines, 3, 24)
	numCols := math.Clamp(ps.AltimSet.Col, 1, 4)
	textWidth := func(s string) float32 {
		return listFont.LayoutBounds(s, 0).Width()
	}
	// Column width comes from the widest row content plus the badge column
	// (matching the title-bar M button), the gap after it, and a right inset
	// that gives inter-column breathing room. DrawView reserves the scroll
	// bar area on the right, so this column width is content-only.
	badgeWidth := titleFont.LayoutBounds("M", 0).Width()
	badgeGap := viewMPad
	sidePad := lineH / 4
	colWidth := badgeWidth + badgeGap +
		textWidth("MMMM   1353 999  ") + sidePad

	// Build one RowList per visible column.
	maxPerPage := visibleRows * numCols
	startIdx := 0
	if numRows > maxPerPage {
		startIdx = ep.altimSetScroll.Offset
	}
	actualCols := numCols
	if numRows-startIdx < maxPerPage {
		actualCols = math.Clamp((numRows-startIdx+visibleRows-1)/visibleRows, 1, numCols)
	}

	var badge *Badge
	if ps.AltimSet.ShowIndicators {
		badge = defaultBadge(titleFont, bright.ScaleRGB(colors.badge.fill))
	}

	cols := make([]*RowList, actualCols)
	for c := range cols {
		cols[c] = &RowList{
			Font:              listFont,
			Width:             colWidth,
			LineHeight:        lineH,
			ListTopPad:        lineH / 4,
			ListBottomPad:     lineH / 4,
			BottomGap:         lineH / 4,
			BadgeGap:          badgeGap,
			SidePad:           sidePad,
			LabelGap:          0,
			SelectedID:        ep.altimSetSelect.Selected,
			SelectedBgColor:   colors.popup.backgroundGrey,
			SelectedTextColor: colors.popup.backgroundBlack,
		}
	}
	for dataIdx := startIdx; dataIdx < numRows; dataIdx++ {
		displayIdx := dataIdx - startIdx
		colIdx := 0
		if numCols > 1 {
			colIdx = displayIdx / visibleRows
		}
		if colIdx >= actualCols {
			break
		}
		cols[colIdx].Rows = append(cols[colIdx].Rows, altimRow(ctx, ep.AltimSetAirports[dataIdx], badge, textColor, listFont, textWidth))
	}

	bodyHeight := float32(0)
	for _, col := range cols {
		if h := col.Measure(); h > bodyHeight {
			bodyHeight = h
		}
	}
	width := float32(actualCols) * colWidth
	if numRows == 0 {
		width = colWidth
		bodyHeight = 0
	}

	colBodyExtent := func(c int, body math.Extent2D) math.Extent2D {
		return math.Extent2D{
			P0: [2]float32{body.P0[0] + float32(c)*colWidth, body.P0[1]},
			P1: [2]float32{body.P0[0] + float32(c+1)*colWidth, body.P1[1]},
		}
	}

	v := View{
		Position:   &ps.AltimSet.Position,
		ID:         "altim-set",
		Width:      width,
		BodyHeight: bodyHeight,
		Title:      "ALTIM SET",
		BodyFont:   listFont,
		Opaque:     ps.AltimSet.Opaque,
		ShowBorder: ps.AltimSet.ShowBorder,
		Brightness: bright,
		OnMenu: ep.makeViewMenu(ctx, "altim-set", altimSetPopupWidth, (8+1)*18,
			func(pb popupBase) popup { return &altimSetPopup{popupBase: pb} }),
		MinimizeTarget: &ps.AltimSet.Visible,
		Scroll: &ViewScrollConfig{
			State:     &ep.altimSetScroll,
			MaxOffset: max(0, numRows-maxPerPage),
		},
		Body: func(body math.Extent2D, b *ViewBuilders) {
			for c, col := range cols {
				col.Draw(colBodyExtent(c, body), b)
			}
		},
		Selectable: &ViewSelectable{
			State: &ep.altimSetSelect,
			Font:  ep.ERAMFont(2),
			Items: func(body math.Extent2D) []ViewSelectableItem {
				return SelectableItems(cols, body, colBodyExtent)
			},
			OnDelete: func(label string) {
				deleteByID(&ep.AltimSetAirports, label, altimDisplayID)
			},
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// altimRow builds one ALTIM SET row from an airport's METAR (or a missing-data
// placeholder). The altimeter portion is underlined via AfterDraw when below
// the standard 29.92 inHg.
func altimRow(ctx *panes.Context, icao string, badge *Badge, color renderer.RGB,
	font *renderer.Font, textWidth func(string) float32) Row {

	displayID := altimDisplayID(icao)
	metar, hasMetar := ctx.Client.State.METAR[icao]
	if !hasMetar {
		return Row{Badge: badge, ID: displayID, AltText: fmt.Sprintf("%-4s   -M-  ", displayID), Color: color}
	}
	timeStr, altStr, altRaw := altimMetarForDisplay(metar)
	prefix := fmt.Sprintf("%-4s  ", displayID)
	timeField := fmt.Sprintf("%4s", timeStr)
	mid := " "
	altField := fmt.Sprintf("%3s", altStr)
	line := prefix + timeField + mid + altField + "  "

	row := Row{Badge: badge, ID: displayID, AltText: line, Color: color}
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

const altimSetPopupWidth = 150

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
		ep.makeIntMenuItem(&ps.AltimSet.Col, "COL", 1, 4, 1),
		ep.makeIntMenuItem(&ps.AltimSet.Font, "FONT", 1, 3, 1),
		ep.makeIntMenuItem(&ps.AltimSet.Bright, "BRIGHT", 0, 100, 1),
		{Label: "TEMPLATE", BgColor: colors.popup.backgroundBlack, Color: colors.popup.text},
	}

	cfg := ERAMMenuConfig{
		Title: "AS",
		Width: altimSetPopupWidth,
		Font:  ep.ERAMFont(2), // Menu always uses FONT 2, not affected by FONT setting
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, a.origin, cfg)
}

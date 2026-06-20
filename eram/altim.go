package eram

import (
	"fmt"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

const altimWindowWidth = 207

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

	fontNum := int(math.Clamp(float32(ps.AltimSet.Font), 1, 3))
	listFont := ep.ERAMFont(fontNum)
	bright := radar.Brightness(ps.AltimSet.Bright)
	fontScale := []float32{0.85, 1.0, 1.2}[fontNum-1]
	by := listFont.LayoutBounds("0", 0).Height()
	textColor := bright.ScaleRGB(colors.view.text)

	numRows := len(ep.AltimSetAirports)
	visibleRows := int(math.Clamp(float32(ps.AltimSet.Lines), 3, 24))
	numCols := int(math.Clamp(float32(ps.AltimSet.Col), 1, 4))
	colWidth := altimWindowWidth * fontScale
	textWidth := func(s string) float32 {
		return listFont.LayoutBounds(s, 0).Width()
	}

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
		badge = &Badge{
			Width: 13 * fontScale, Height: 17 * fontScale,
			Fill:   bright.ScaleRGB(colors.badge.fill),
			Border: colors.badge.border,
		}
	}

	cols := make([]*RowList, actualCols)
	for c := range cols {
		cols[c] = &RowList{
			Font:       listFont,
			Width:      colWidth,
			LineHeight: 17 * fontScale,
			TopPad:     8 * fontScale,
			BottomGap:  4 * fontScale,
			BadgeGap:   4 * fontScale,
			LabelGap:   0,
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
		cols[colIdx].Rows = append(cols[colIdx].Rows, altimRow(ctx, ep.AltimSetAirports[dataIdx], badge, textColor, listFont, textWidth, by))
	}

	bodyHeight := float32(0)
	for _, col := range cols {
		if h := col.Measure(); h > bodyHeight {
			bodyHeight = h
		}
	}
	width := float32(actualCols) * colWidth
	if numRows == 0 {
		width = altimWindowWidth * fontScale
		bodyHeight = 0
	}

	v := View{
		Position:   &ps.AltimSet.Position,
		Reposition: &ep.altimSetRepo,
		Width:      width,
		BodyHeight: bodyHeight,
		Title:      "ALTIM SET",
		TitleFont:  ep.ERAMFont(2),
		Opaque:     ps.AltimSet.Opaque,
		ShowBorder: ps.AltimSet.ShowBorder,
		Brightness: bright,
		OnMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*altimSetPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				altimSetPopupWidth, (8+1)*18, ep.ERAMFont(2), host)
			return &altimSetPopup{origin: origin}
		},
		OnMinimize: func() { ps.AltimSet.Visible = false },
		Scroll: &ViewScrollConfig{
			State:     &ep.altimSetScroll,
			MaxOffset: max(0, numRows-maxPerPage),
		},
		Body: func(body math.Extent2D, b *ViewBuilders) {
			for c, col := range cols {
				colBody := math.Extent2D{
					P0: [2]float32{body.P0[0] + float32(c)*colWidth, body.P0[1]},
					P1: [2]float32{body.P0[0] + float32(c+1)*colWidth, body.P1[1]},
				}
				col.Draw(colBody, b)
			}
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// altimRow builds one ALTIM SET row from an airport's METAR (or a missing-data
// placeholder). The altimeter portion is underlined via AfterDraw when below
// the standard 29.92 inHg.
func altimRow(ctx *panes.Context, icao string, badge *Badge, color renderer.RGB,
	font *renderer.Font, textWidth func(string) float32, by float32) Row {

	displayID := altimDisplayID(icao)
	metar, hasMetar := ctx.Client.State.METAR[icao]
	if !hasMetar {
		return Row{Badge: badge, AltText: fmt.Sprintf("%-4s   -M-  ", displayID), Color: color}
	}
	timeStr, altStr, altRaw := altimMetarForDisplay(metar)
	prefix := fmt.Sprintf("%-4s  ", displayID)
	timeField := fmt.Sprintf("%4s", timeStr)
	mid := " "
	altField := fmt.Sprintf("%3s", altStr)
	line := prefix + timeField + mid + altField + "  "

	row := Row{Badge: badge, AltText: line, Color: color}
	if altRaw > 0 && altRaw < 2992 && altStr != "..." {
		offsetX := textWidth(prefix) + textWidth(timeField) + textWidth(mid)
		fieldW := textWidth(altField)
		row.AfterDraw = func(extent math.Extent2D, bodyOrigin [2]float32, b *ViewBuilders) {
			uy := bodyOrigin[1] - by - 1
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
	origin [2]float32
}

func (a *altimSetPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	origin := a.origin

	// T/O button - toggles opaque mode
	tLabel := "T"
	tBg := colors.popup.backgroundBlack
	if ps.AltimSet.Opaque {
		tLabel = "O"
		tBg = colors.popup.backgroundGrey
	}

	// BORDER button - grey when ON, black when OFF
	borderBg := util.Select(ps.AltimSet.ShowBorder, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	// TEAROFF button - grey when ON, black when OFF
	tearoffBg := util.Select(ps.AltimSet.ShowIndicators, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: colors.popup.text, Centered: true,
			OnClick: func(ct ERAMMenuClickType) bool {
				if ct == MenuClickTertiary {
					ps.AltimSet.Opaque = !ps.AltimSet.Opaque
				}
				return false
			}},
		{Label: "BORDER", BgColor: borderBg, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				if ct == MenuClickTertiary {
					ps.AltimSet.ShowBorder = !ps.AltimSet.ShowBorder
				}
				return false
			}},
		{Label: "TEAROFF", BgColor: tearoffBg, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				if ct == MenuClickTertiary {
					ps.AltimSet.ShowIndicators = !ps.AltimSet.ShowIndicators
				}
				return false
			}},
		{Label: fmt.Sprintf("LINES %d", ps.AltimSet.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.AltimSet.Lines, 3, 24, 1)
				maxOffset := max(0, len(ep.AltimSetAirports)-ps.AltimSet.Lines)
				ep.altimSetScroll.Offset = math.Clamp(ep.altimSetScroll.Offset, 0, maxOffset)
				return false
			}},
		{Label: fmt.Sprintf("COL %d", ps.AltimSet.Col), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.AltimSet.Col, 1, 4, 1)
				return false
			}},
		{Label: fmt.Sprintf("FONT %d", ps.AltimSet.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.AltimSet.Font, 1, 3, 1)
				return false
			}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.AltimSet.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.AltimSet.Bright, 0, 100, 1)
				return false
			}},
		{Label: "TEMPLATE", BgColor: colors.popup.backgroundBlack, Color: colors.popup.text, Centered: false},
	}

	cfg := ERAMMenuConfig{
		Title: "AS",
		Width: altimSetPopupWidth,
		Font:  ep.ERAMFont(2), // Menu always uses FONT 2, not affected by FONT setting
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, origin, cfg)
}

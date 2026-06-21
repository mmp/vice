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
		Brightness: radar.Brightness(ps.AltimSet.Bright),
		OnMenu: ep.makeViewMenu(ctx, "altim-set", altimSetPopupWidth, (8+1)*18,
			func(pb popupBase) popup { return &altimSetPopup{popupBase: pb} }),
		MinimizeTarget: &ps.AltimSet.Visible,
		RowSource: &ViewRowSource{
			Rows:                  rows,
			FontSize:              ps.AltimSet.Font,
			ContentWidth:          textWidth("MMMM   1353 999  "),
			MaxCols:               math.Clamp(ps.AltimSet.Col, 1, 4),
			VisibleRows:           math.Clamp(ps.AltimSet.Lines, 3, 24),
			BadgeColumn:           true,
			BadgesVisible:         ps.AltimSet.ShowIndicators,
			RowSpacing:            RowSpacingCompact,
			ScrollState:           &ep.altimSetScroll,
			EmptyKeepsColumnWidth: true,
			SelectedID:            ep.altimSetSelect.Selected,
			SelectableState:       &ep.altimSetSelect,
			SelectableOnDelete:    func(label string) { deleteByID(&ep.AltimSetAirports, label, altimDisplayID) },
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
		return Row{ID: displayID, Body: fmt.Sprintf("%-4s   -M-  ", displayID)}
	}
	timeStr, altStr, altRaw := altimMetarForDisplay(metar)
	prefix := fmt.Sprintf("%-4s  ", displayID)
	timeField := fmt.Sprintf("%4s", timeStr)
	mid := " "
	altField := fmt.Sprintf("%3s", altStr)
	line := prefix + timeField + mid + altField + "  "

	row := Row{ID: displayID, Body: line}
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

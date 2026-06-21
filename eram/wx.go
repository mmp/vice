package eram

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

const wxWindowWidth = float32(320)

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

	fontNum := math.Clamp(ps.WX.Font, 1, 3)
	bright := radar.Brightness(ps.WX.Bright)
	listFont := ep.ERAMFont(fontNum)
	lineH := listFont.LayoutBounds("0", 0).Height() + 2
	badgeWidth := listFont.LayoutBounds("M", 0).Width() + 4

	visibleRows := math.Clamp(ps.WX.Lines, 3, 24)

	// Build rows from the scroll offset onward; RowList does the wrapping.
	rl := &RowList{
		Font:              listFont,
		Width:             wxWindowWidth,
		LineHeight:        lineH,
		TopPad:            lineH / 2,
		BottomGap:         lineH / 4,
		BadgeGap:          lineH / 4,
		SelectedID:        ep.wxSelect.Selected,
		SelectedBgColor:   colors.popup.backgroundGrey,
		SelectedTextColor: colors.popup.backgroundBlack,
	}
	textColor := bright.ScaleRGB(colors.view.text)
	yellowColor := bright.ScaleRGB(colors.badge.fill)
	var badgeProto *Badge
	if ps.WX.ShowIndicators {
		badgeProto = &Badge{
			Width: badgeWidth, Height: lineH,
			Fill: yellowColor, Border: colors.badge.border,
		}
	}

	rl.MaxLines = visibleRows
	for _, icao := range ep.WXReportStations {
		displayID := wxDisplayID(icao)
		body, alt := wxMetarBody(ctx, icao)
		rl.Rows = append(rl.Rows, Row{
			Badge: badgeProto, ID: displayID, Label: displayID,
			Body: body, AltText: alt, Color: textColor,
		})
	}

	// Measure first with no skip so totalLines reflects all stations; only
	// honour the persisted scroll offset when the wrapped content actually
	// overflows the visible row budget. Otherwise reset the scroll to 0 so
	// stale offsets don't hide stations.
	bodyHeight := rl.Measure()
	totalLines := rl.TotalLines()
	if totalLines <= visibleRows {
		ep.wxScroll.Offset = 0
	} else {
		rl.Skip = math.Clamp(ep.wxScroll.Offset, 0, max(len(ep.WXReportStations)-1, 0))
		ep.wxScroll.Offset = rl.Skip
		bodyHeight = rl.Measure()
	}

	v := View{
		Position:   &ps.WX.Position,
		Reposition: &ep.wxRepo,
		Width:      wxWindowWidth,
		BodyHeight: bodyHeight,
		Title:      "WX REPORT",
		TitleFont:  ep.ERAMFont(2),
		Opaque:     ps.WX.Opaque,
		ShowBorder: ps.WX.ShowBorder,
		Brightness: bright,
		OnMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*wxPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				wxPopupWidth, (6+1)*18, ep.ERAMFont(2), host)
			return &wxPopup{origin: origin}
		},
		OnMinimize: func() { ps.WX.Visible = false },
		Scroll: &ViewScrollConfig{
			State: &ep.wxScroll,
			// Offset is a station index, so the user can scroll until the last
			// station is the top of the page. Only show the scroll bar when
			// wrapping actually pushes content beyond visibleRows.
			MaxOffset: scrollMaxOffset(totalLines, visibleRows, len(ep.WXReportStations)),
		},
		Body: func(body math.Extent2D, b *ViewBuilders) { rl.Draw(body, b) },
		Selectable: &ViewSelectable{
			State: &ep.wxSelect,
			Font:  ep.ERAMFont(2),
			Items: func(body math.Extent2D) []ViewSelectableItem {
				ex := rl.Extents(body)
				items := make([]ViewSelectableItem, 0, len(ex))
				for i, e := range ex {
					row := rl.Rows[rl.VisibleFirst()+i]
					items = append(items, ViewSelectableItem{
						Extent: math.Extent2D{
							P0: [2]float32{rl.RowTextLeft(e.P0[0], row), e.P0[1]},
							P1: e.P1,
						},
						Label: row.ID,
					})
				}
				return items
			},
			OnDelete: func(label string) {
				for i, icao := range ep.WXReportStations {
					if wxDisplayID(icao) == label {
						ep.WXReportStations = slices.Delete(ep.WXReportStations, i, i+1)
						return
					}
				}
			},
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// scrollMaxOffset returns the max station-index offset when wrapped content
// (totalLines) overflows the visible row budget; 0 otherwise (no scroll bar).
func scrollMaxOffset(totalLines, visibleRows, numStations int) int {
	if totalLines <= visibleRows {
		return 0
	}
	return max(0, numStations-1)
}

// wxMetarBody returns either the wrappable METAR text (HHMM + remaining
// fields) for the station, or an AltText status string ("-M-") if no METAR
// is available.
func wxMetarBody(ctx *panes.Context, icao string) (body, alt string) {
	metar, ok := ctx.Client.State.METAR[icao]
	if !ok || metar.Raw == "" {
		return "", "-M-"
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
		return "", "-M-"
	}

	tok := rawFields[timeField]
	body = tok[len(tok)-5 : len(tok)-1]
	if timeField+1 < len(rawFields) {
		body += " " + strings.Join(rawFields[timeField+1:], " ")
	}
	return body, ""
}

const wxPopupWidth = 150

// wxPopup is the popup-interface impl for the WX REPORT configuration menu.
// The origin is captured at open time from the view's current geometry.
type wxPopup struct {
	origin [2]float32
}

func (w *wxPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	const menuWidth = wxPopupWidth

	// T/O button — shows "T" (transparent) or "O" (opaque)
	tLabel := "T"
	tBg := colors.popup.backgroundBlack
	if ps.WX.Opaque {
		tLabel = "O"
		tBg = colors.popup.backgroundGrey
	}

	// BORDER button - grey when ON, black when OFF
	borderBg := util.Select(ps.WX.ShowBorder, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	// TEAROFF button - grey when ON, black when OFF
	tearoffBg := util.Select(ps.WX.ShowIndicators, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: colors.popup.text, Centered: true,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.WX.Opaque = !ps.WX.Opaque
				return false
			}},
		{Label: "BORDER", BgColor: borderBg, Color: colors.popup.text, Centered: false,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.WX.ShowBorder = !ps.WX.ShowBorder
				return false
			}},
		{Label: "TEAROFF", BgColor: tearoffBg, Color: colors.popup.text, Centered: false,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.WX.ShowIndicators = !ps.WX.ShowIndicators
				return false
			}},
		{Label: fmt.Sprintf("LINES %d", ps.WX.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.WX.Lines, 3, 24, 1)
				maxOffset := max(0, len(ep.WXReportStations)-ps.WX.Lines)
				ep.wxScroll.Offset = math.Clamp(ep.wxScroll.Offset, 0, maxOffset)
				return false
			}},
		{Label: fmt.Sprintf("FONT %d", ps.WX.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.WX.Font, 1, 3, 1)
				return false
			}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.WX.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.WX.Bright, 0, 100, 1)
				return false
			}},
	}

	cfg := ERAMMenuConfig{
		Title: "WX",
		Width: menuWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, w.origin, cfg)
}

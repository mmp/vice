package eram

import (
	"fmt"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

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
		id := wxDisplayID(icao)
		rows = append(rows, Row{ID: id, Label: id, Body: wxMetarBody(ctx, icao)})
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.WX.Position,
		ID:         "wx",
		Title:      "WX REPORT",
		Opaque:     ps.WX.Opaque,
		ShowBorder: ps.WX.ShowBorder,
		Brightness: ps.WX.Bright,
		OnMenu: ep.makeViewMenu(ctx, "wx", 6,
			func(pb popupBase) popup { return &wxPopup{popupBase: pb} }),
		MinimizeTarget: &ps.WX.Visible,
		RowSource: &ViewRowSource{
			Rows:               rows,
			FontSize:           ps.WX.Font,
			ContentChars:       24,
			MaxCols:            1,
			VisibleRows:        ps.WX.Lines,
			BadgeColumn:        true,
			BadgesVisible:      ps.WX.ShowIndicators,
			RowSpacing:         RowSpacingAiry,
			ScrollState:        &ep.wxScroll,
			SelectedID:         ep.wxSelect.Selected,
			SelectableState:    &ep.wxSelect,
			SelectableOnDelete: func(label string) { deleteByID(&ep.WXReportStations, label, wxDisplayID) },
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

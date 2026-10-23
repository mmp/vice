// eram/wxviews.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// The weather station views: ALTIM SET, which shows the stations' altimeter
// settings, and WX REPORT, which shows their METARs, with the AR and WR
// commands that add stations to them.

///////////////////////////////////////////////////////////////////////////
// ALTIM SET

// drawAltimSetView renders the ALTIM SET floating window.
func (ep *Scope) drawAltimSetView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.AltimSet.Visible {
		return
	}

	// A pick on an entry offers to delete it (4.2.1.1.2).
	var rows []Row
	for i, icao := range ep.AltimSetAirports {
		if icao == "" {
			rows = append(rows, Row{})
			continue
		}
		row := ep.altimRow(ctx, icao)
		row.OnClick = func(c RowClick) {
			ep.openRowPickArea(ctx, c, "DELETE "+string(icao), func() { ep.removeAltimSetEntry(i) })
		}
		rows = append(rows, row)
	}

	ep.DrawView(ctx, transforms, cb, View{
		Prefs: &ps.AltimSet.ViewPreferences,
		ID:    "altim-set",
		Title: "ALTIM SET",
		Menu:  altimSetMenu{},
		RowSource: &ViewRowSource{
			Rows:                  rows,
			FontIndex:             ps.AltimSet.Font,
			ContentChars:          altimSetEntryChars,
			MaxCols:               ps.AltimSet.Col,
			VisibleRows:           ps.AltimSet.Lines,
			ReserveBadgeColumn:    true,
			TearOffs:              &altimSetTearOffs,
			ShowTearOffBars:       ps.AltimSet.ShowIndicators,
			RowSpacing:            RowSpacingCompact,
			ScrollState:           &ep.altimSetScroll,
			EmptyKeepsColumnWidth: true,
		},
	})
}

// addAltimSetStation puts icao in the first empty position of the ALTIM SET
// view, looking down each column from the left, or after the last entry if
// there is none (4.2.1.1.2).
func (ep *Scope) addAltimSetStation(icao av.ICAOAirportCode) {
	if i := slices.Index(ep.AltimSetAirports, ""); i >= 0 {
		ep.AltimSetAirports[i] = icao
	} else {
		ep.AltimSetAirports = append(ep.AltimSetAirports, icao)
	}
}

// altimSetEntryChars is the width of an ALTIM SET entry.
const altimSetEntryChars = len("MMMM   1353 999  ")

// altimSetTearOffs are the tear-offs of the ALTIM SET view's entries, by
// ICAO code.
var altimSetTearOffs = entryTearOffs{
	locations: func(ep *Scope) *map[string]math.Point2LL { return &ep.currentPrefs().AltimSet.TearOffs },
	tearOff: func(ep *Scope, ctx *scope.Context, id string) (entryTearOff, bool) {
		icao := av.ICAOAirportCode(id)
		if !slices.Contains(ep.AltimSetAirports, icao) {
			return entryTearOff{}, false
		}
		ps := ep.currentPrefs()
		return entryTearOff{
			ViewRowSource: ViewRowSource{
				Rows:               []Row{ep.altimRow(ctx, icao)},
				FontIndex:          ps.AltimSet.Font,
				ContentChars:       altimSetEntryChars,
				ReserveBadgeColumn: true,
				RowSpacing:         RowSpacingCompact,
			},
			brightness: ps.AltimSet.Bright,
			border:     ps.AltimSet.ShowBorder,
		}, true
	},
}

// removeAltimSetEntry removes the idx'th entry of the ALTIM SET view and
// its tear-off. Its position stays empty, so the entries after it don't
// move (4.2.1.1.2); empty positions after the last entry are dropped so
// the view contracts.
func (ep *Scope) removeAltimSetEntry(idx int) {
	delete(ep.currentPrefs().AltimSet.TearOffs, string(ep.AltimSetAirports[idx]))
	ep.AltimSetAirports[idx] = ""
	n := len(ep.AltimSetAirports)
	for n > 0 && ep.AltimSetAirports[n-1] == "" {
		n--
	}
	ep.AltimSetAirports = ep.AltimSetAirports[:n]
}

// altimRow builds one ALTIM SET row from an airport's METAR (or a missing-data
// placeholder). The altimeter portion is underlined via AfterDraw when below
// the standard 29.92 inHg. The tear-off bar and row color are filled in by
// the View; this function just constructs the row text and the AfterDraw
// underline.
func (ep *Scope) altimRow(ctx *scope.Context, icao av.ICAOAirportCode) Row {
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.AltimSet.Font)
	textWidth := func(s string) float32 { return font.LayoutBounds(s, 0).Width() }
	color := ep.viewTextColor(ps.AltimSet.Bright)

	displayID := db.AirportDisplayId(icao)
	metar, hasMetar := ctx.Client.State.METAR[icao]
	if !hasMetar {
		return Row{TearOff: string(icao), Body: fmt.Sprintf("%-4s   -M-  ", displayID)}
	}
	timeStr, altStr, altRaw := altimMetarForDisplay(metar)
	prefix := fmt.Sprintf("%-4s  ", displayID)
	timeField := fmt.Sprintf("%4s", timeStr)
	mid := " "
	altField := fmt.Sprintf("%3s", altStr)
	line := prefix + timeField + mid + altField + "  "

	row := Row{TearOff: string(icao), Body: line}
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
	// Extract HHMM from the METAR observation time. Times decoded from the
	// server are in the local time zone.
	t := metar.Time.UTC()
	timeStr = fmt.Sprintf("%02d%02d", t.Hour(), t.Minute())

	// Convert altimeter from inHg to 4-digit A-setting format
	// A-setting is always 4 digits (e.g., 2992, 3012)
	altRaw = int(math.Round(metar.Altimeter_inHg() * 100))
	if altRaw > 0 {
		// Last 3 digits of the 4-digit A-setting
		altStr = fmt.Sprintf("%03d", altRaw%1000)
	}

	return
}

// altimSetMenu is the ALTIM SET view's menu (figure 4-10).
type altimSetMenu struct{}

func (altimSetMenu) title() string { return "AS" }

func (altimSetMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	template := makeActionMenuItem("TEMPLATE", func(MenuClickType) {})
	// The cursor starts on TEMPLATE (4.2.1.3.1).
	template.PlaceCursor = true
	return []MenuItem{
		ep.makeBooleanMenuItem(&ps.AltimSet.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.AltimSet.ShowBorder, "BORDER"),
		ep.makeToggleMenuItem(&ps.AltimSet.ShowIndicators, "TEAROFF"),
		{Label: fmt.Sprintf("LINES %d", ps.AltimSet.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text,
			OnClick: func(click MenuClickType) bool {
				adjustMenuValue(ep, &ps.AltimSet.Lines, click, 3, 24, 1)
				maxOffset := max(0, len(ep.AltimSetAirports)-ps.AltimSet.Lines)
				ep.altimSetScroll.Offset = math.Clamp(ep.altimSetScroll.Offset, 0, maxOffset)
				return false
			}},
		makeIntMenuItem(ep, &ps.AltimSet.Col, "COL", 1, 4, 1),
		makeIntMenuItem(ep, &ps.AltimSet.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.AltimSet.Bright, "BRIGHT", 0, 100, 2),
		template,
	}
}

///////////////////////////////////////////////////////////////////////////
// WX View

// drawWXView renders the WX floating window.
func (ep *Scope) drawWXView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.WX.Visible {
		return
	}

	// A pick on an entry offers to delete it (4.2.20.2).
	var rows []Row
	for i, icao := range ep.WXReportStations {
		row := wxRow(ctx, icao)
		row.OnClick = func(c RowClick) {
			ep.openRowPickArea(ctx, c, "DELETE "+string(icao), func() { ep.removeWXReportStation(i) })
		}
		rows = append(rows, row)
	}

	ep.DrawView(ctx, transforms, cb, View{
		Prefs: &ps.WX.ViewPreferences,
		ID:    "wx",
		Title: "WX",
		Menu:  wxReportMenu{},
		RowSource: &ViewRowSource{
			Rows:               rows,
			FontIndex:          ps.WX.Font,
			ContentChars:       wxEntryChars,
			MaxCols:            1,
			VisibleRows:        ps.WX.Lines,
			ReserveBadgeColumn: true,
			TearOffs:           &wxTearOffs,
			ShowTearOffBars:    ps.WX.ShowIndicators,
			RowSpacing:         RowSpacingAiry,
			ScrollState:        &ep.wxScroll,
		},
	})
}

// wxEntryChars is the width of a WX REPORT entry; its observation wraps
// to fit.
const wxEntryChars = 24

// wxTearOffs are the tear-offs of the WX REPORT view's entries, by ICAO
// code. A torn-off entry stays displayed while the view is suppressed
// (4.2.20.2).
var wxTearOffs = entryTearOffs{
	locations: func(ep *Scope) *map[string]math.Point2LL { return &ep.currentPrefs().WX.TearOffs },
	tearOff: func(ep *Scope, ctx *scope.Context, id string) (entryTearOff, bool) {
		icao := av.ICAOAirportCode(id)
		if !slices.Contains(ep.WXReportStations, icao) {
			return entryTearOff{}, false
		}
		ps := ep.currentPrefs()
		return entryTearOff{
			ViewRowSource: ViewRowSource{
				Rows:               []Row{wxRow(ctx, icao)},
				FontIndex:          ps.WX.Font,
				ContentChars:       wxEntryChars,
				ReserveBadgeColumn: true,
				RowSpacing:         RowSpacingAiry,
			},
			brightness: ps.WX.Bright,
			border:     ps.WX.ShowBorder,
		}, true
	},
}

// removeWXReportStation removes the idx'th station of the WX REPORT view
// and its tear-off.
func (ep *Scope) removeWXReportStation(idx int) {
	delete(ep.currentPrefs().WX.TearOffs, string(ep.WXReportStations[idx]))
	ep.WXReportStations = slices.Delete(ep.WXReportStations, idx, idx+1)
}

// wxRow returns a station's entry in the WX REPORT view.
func wxRow(ctx *scope.Context, icao av.ICAOAirportCode) Row {
	return Row{TearOff: string(icao), Label: db.AirportDisplayId(icao), Body: wxMetarBody(ctx, icao)}
}

// wxMetarBody returns the row body text for a station: the wrappable METAR
// text (HHMM + remaining fields) when METAR data is available, or "-M-" as
// a status placeholder otherwise.
func wxMetarBody(ctx *scope.Context, icao av.ICAOAirportCode) string {
	metar, ok := ctx.Client.State.METAR[icao]
	if !ok || metar.Raw == "" {
		return "-M-"
	}

	// Find the time-group token: digits followed by 'Z'. (Plain strings.Index
	// would match the 'Z' inside station IDs like "CYYZ".)
	rawFields := strings.Fields(metar.Raw)
	timeField := slices.IndexFunc(rawFields, func(f string) bool {
		return len(f) >= 5 && f[len(f)-1] == 'Z' && util.IsAllNumbers(f[:len(f)-1])
	})
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

// wxReportMenu is the WX REPORT view's menu.
type wxReportMenu struct{}

func (wxReportMenu) title() string { return "WX" }

func (wxReportMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	return []MenuItem{
		ep.makeBooleanMenuItem(&ps.WX.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.WX.ShowBorder, "BORDER"),
		ep.makeToggleMenuItem(&ps.WX.ShowIndicators, "TEAROFF"),
		makeIntMenuItem(ep, &ps.WX.Lines, "LINES", 3, 24, 1),
		makeIntMenuItem(ep, &ps.WX.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.WX.Bright, "BRIGHT", 0, 100, 2),
	}
}

///////////////////////////////////////////////////////////////////////////
// AR and WR

// isStationID reports whether s has the two to five characters of an
// altimeter or weather station identifier (4.2.1.1.2, 4.2.20.1).
func isStationID(s string) bool {
	return len(s) >= 2 && len(s) <= 5
}

func lookupCommandAirport(airport string) (av.ICAOAirportCode, bool) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if !isStationID(airport) {
		return "", false
	}
	// Users type an airport's FAA id or, less often, its full ICAO id.
	if ap, ok := db.DB.LookupFAAAirport(av.FAAAirportCode(airport)); ok {
		return ap.Id, true
	}
	if ap, ok := db.DB.LookupICAOAirport(av.ICAOAirportCode(airport)); ok {
		return ap.Id, true
	}
	return "", false
}

func handleAltimAdd(ep *Scope, ctx *scope.Context, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewError("REJECT - AR - MISSING AIRPORT")
	}
	if !isStationID(airport) {
		return CommandStatus{}, NewError("REJECT - AR - INVALID AIRPORT CODE")
	}

	// Confirm airport exists in the database
	icao, ok := lookupCommandAirport(airport)
	if !ok {
		return CommandStatus{}, NewError("REJECT - AR - UNKNOWN AIRPORT")
	}

	// Toggle: if already in the list, remove it.
	if i := slices.Index(ep.AltimSetAirports, icao); i >= 0 {
		ep.removeAltimSetEntry(i)
		return CommandStatus{feedbackArea: []string{"ACCEPT", "ALTIMETER REQ"}}, nil
	}

	ep.addAltimSetStation(icao)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.AltimSet.Visible = true

	requestMETARIfMissing(ctx, icao)

	return CommandStatus{feedbackArea: []string{"ACCEPT", "ALTIMETER REQ"}}, nil
}

// WR - WX REPORT Add Airport Handler

// handleWXReportAdd adds up to three stations to the WX REPORT view, or
// removes those already in it (4.2.20.2).
func handleWXReportAdd(ep *Scope, ctx *scope.Context, stations string) (CommandStatus, error) {
	ids := strings.Fields(strings.ToUpper(stations))
	if len(ids) > 3 {
		return CommandStatus{}, ErrMessageTooLong
	}

	var icaos []av.ICAOAirportCode
	for _, id := range ids {
		if !isStationID(id) {
			return CommandStatus{}, NewError("REJECT - WR - INVALID AIRPORT CODE")
		}
		icao, ok := lookupCommandAirport(id)
		if !ok {
			return CommandStatus{}, NewError("REJECT - WR - UNKNOWN AIRPORT")
		}
		icaos = append(icaos, icao)
	}

	// Added stations go to the top of the list in the order given.
	for _, icao := range slices.Backward(icaos) {
		toggleWXReportStation(ep, ctx, icao)
	}

	return CommandStatus{feedbackArea: []string{"ACCEPT", "WEATHER STAT REQ"}}, nil
}

// toggleWXReportStation adds the station to the top of the WX REPORT view,
// or removes it if it is already there.
func toggleWXReportStation(ep *Scope, ctx *scope.Context, icao av.ICAOAirportCode) {
	if i := slices.Index(ep.WXReportStations, icao); i >= 0 {
		ep.removeWXReportStation(i)
		return
	}

	ep.WXReportStations = slices.Insert(ep.WXReportStations, 0, icao)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.WX.Visible = true

	requestMETARIfMissing(ctx, icao)
}

// requestMETARIfMissing asks the server to start supplying METAR for icao
// when the client has not yet seen any METAR for that airport. The RPC
// is fire-and-forget: if the server rejects the airport the row keeps
// showing "-M-", matching the behavior for known airports without
// bundled data.
func requestMETARIfMissing(ctx *scope.Context, icao av.ICAOAirportCode) {
	if _, ok := ctx.Client.State.METAR[icao]; !ok {
		ctx.Client.AddMETARAirport(icao)
	}
}

// WR R - WX REPORT Display (show METAR in Response Area)

func handleWXReportDisplay(ep *Scope, ctx *scope.Context, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewError("REJECT - WR R - MISSING AIRPORT")
	}
	if !isStationID(airport) {
		return CommandStatus{}, NewError("REJECT - WR R - INVALID AIRPORT CODE")
	}

	// Confirm airport exists in the database
	icao, ok := lookupCommandAirport(airport)
	if !ok {
		return CommandStatus{}, NewError("REJECT - WR R - UNKNOWN AIRPORT")
	}

	// Get METAR from the pre-populated wx system
	metar, hasMetar := ctx.Client.State.METAR[icao]

	// Determine what to display
	var displayText string
	if hasMetar && metar.Raw != "" {
		displayText = metar.Raw
	} else {
		displayText = fmt.Sprintf("NO DATA\n%s", icao)
	}

	ep.responseArea = formatInput(displayText)

	return CommandStatus{}, nil
}

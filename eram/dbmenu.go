// dbmenu.go -- the FDB field-click menus: altitude, heading, speed, and
// free-form text. Each is a popup (see menu.go) opened from a click on the
// corresponding datablock field in datablockInteractions.

package eram

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// dbMenuBase carries the state shared by all datablock menus: which flight
// the menu is for and where it is drawn.
type dbMenuBase struct {
	acid   sim.ACID
	origin [2]float32
}

// resolveTrack looks the menu's flight up fresh each frame; if it is gone
// (or has no flight plan) the menu closes and nil is returned.
func (m *dbMenuBase) resolveTrack(ep *ERAMPane, ctx *panes.Context) *sim.Track {
	trk, ok := ctx.GetTrackByACID(m.acid)
	if !ok || trk.FlightPlan == nil {
		ep.popup = nil
		return nil
	}
	return trk
}

// openDatablockMenu clamps a new menu's placement (flipping to the left of
// the datablock if it would run off the pane edge) and returns its origin.
func (ep *ERAMPane) openDatablockMenu(ctx *panes.Context, dbMain math.Extent2D, width, height float32) [2]float32 {
	pl := ep.OpenPopupAt(ctx, [2]float32{dbMain.P1[0], dbMain.P1[1]}, width, height, ep.ERAMFont(2), dbMain)
	return pl.Origin
}

// dbMenuWidth returns a menu width fitting the given body labels, the arrow
// column, and the title bar (callsign + X close button).
func dbMenuWidth(font, titleFont *renderer.Font, title string, labels ...string) float32 {
	var w float32
	for _, l := range labels {
		w = max(w, font.LayoutBounds(l, 0).Width())
	}
	w += 16 + 16 // arrow column + padding
	xW := titleFont.LayoutBounds("X", 0).Width()
	titleW := titleFont.LayoutBounds(title, 0).Width() + 2*xW + 12
	return max(w, titleW)
}

// dbMenuItemH is the row height used by the datablock menus.
const dbMenuItemH = float32(18)

///////////////////////////////////////////////////////////////////////////
// Altitude Menu

// altitudeMenuPopup is the FDB Altitude Menu, opened from Field B/C (line 2).
type altitudeMenuPopup struct {
	dbMenuBase
	offset      int
	local       bool // assign local interim altitude on selection
	procedure   bool // assign procedure altitude on selection
	initialized bool // scroll offset positioned on first draw
}

// altitudeMenuAlts is the pick list in hundreds of feet, descending.
var altitudeMenuAlts = func() []int {
	var alts []int
	for a := 600; a >= 10; a -= 10 {
		alts = append(alts, a)
	}
	return alts
}()

func (ep *ERAMPane) openAltitudeMenu(ctx *panes.Context, trk *sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(trk.FlightPlan.ACID)
	width := dbMenuWidth(font, titleFont, title, "LOCAL TALT", "PROCEDURE", "FP 000", "000 T")

	nRows := 3 // title + LOCAL TALT + PROCEDURE
	if trk.FlightPlan.InterimAlt > 0 {
		nRows += 2 // FP assigned + interim rows
	}
	height := float32(nRows+7) * dbMenuItemH

	ep.popup = &altitudeMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   trk.FlightPlan.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
	}
}

func (p *altitudeMenuPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	fp := trk.FlightPlan
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	current := fp.DataBlockAltitude() / 100 // hundreds of feet; 0 if none
	interimLetter := getInterimAltitudeType(*trk)

	if !p.initialized {
		p.initialized = true
		center := current
		if center == 0 {
			if state := ep.TrackState[trk.ADSBCallsign]; state != nil {
				center = (int(state.Track.TransponderAltitude) + 500) / 1000 * 10
			}
		}
		center = (center + 5) / 10 * 10
		if idx := slices.Index(altitudeMenuAlts, center); idx >= 0 {
			p.offset = math.Clamp(idx-3, 0, max(len(altitudeMenuAlts)-7, 0))
		}
	}

	grey := colors.popup.backgroundGrey
	textC := colors.popup.text

	clearInterim := func(_ ERAMMenuClickType) bool {
		status, err := handleClearInterimAltitude(ep, ctx, trk)
		ep.applyCommandStatus(ctx, status, err)
		ep.popup = nil
		return true
	}

	var rows []ERAMMenuItem
	if fp.InterimAlt > 0 {
		// Flight Plan Assigned pick area: clears the interim / local
		// interim / procedure altitude, reverting to the FP altitude.
		fpAlt := fp.AssignedAltitude
		if fpAlt == 0 {
			fpAlt = fp.PerceivedAssigned
		}
		rows = append(rows, ERAMMenuItem{
			Label: fmt.Sprintf("FP %03d", fpAlt/100), BgColor: grey, Color: textC, Centered: true,
			OnClick: clearInterim,
		})
		// Interim Altitude pick area.
		rows = append(rows, ERAMMenuItem{
			Label: fmt.Sprintf("%03d %s", fp.InterimAlt/100, interimLetter), BgColor: grey, Color: textC, Centered: true,
			OnClick: clearInterim,
		})
	}

	toggleRow := func(label string, v, other *bool) ERAMMenuItem {
		return ERAMMenuItem{
			Label: label, Centered: true, Color: textC,
			BgColor: util.Select(*v, colors.menu.selectedItem, grey),
			OnClick: func(_ ERAMMenuClickType) bool {
				*v = !*v
				if *v {
					*other = false
				}
				return false
			},
		}
	}
	rows = append(rows, toggleRow("LOCAL TALT", &p.local, &p.procedure))
	rows = append(rows, toggleRow("PROCEDURE", &p.procedure, &p.local))

	// Assigned Altitude pick areas, with the interim (T) pick areas to the
	// right when neither LOCAL TALT nor PROCEDURE is active.
	showInterim := !p.local && !p.procedure
	gridRows := make([][]ERAMMenuGridCell, 0, len(altitudeMenuAlts))
	for _, alt := range altitudeMenuAlts {
		isCurrent := alt == current
		assignAlt := func(_ ERAMMenuClickType) bool {
			var status CommandStatus
			var err error
			if p.local {
				status, err = handleInterimAltitude(ep, ctx, InterimAltitude{Altitude: alt * 100, Type: "L"}, trk)
			} else if p.procedure {
				status, err = handleInterimAltitude(ep, ctx, InterimAltitude{Altitude: alt * 100, Type: "P"}, trk)
			} else {
				status, err = handleAssignedAltitude(ep, ctx, alt*100, trk)
			}
			ep.applyCommandStatus(ctx, status, err)
			return true
		}

		altCell := ERAMMenuGridCell{
			Label: fmt.Sprintf("%03d", alt), Color: textC, Weight: 3, OnClick: assignAlt,
		}
		if isCurrent {
			altCell.BgColor = colors.menu.currentValue
		}

		row := []ERAMMenuGridCell{altCell}
		if showInterim {
			letter := "T"
			if isCurrent && fp.InterimAlt > 0 {
				letter = interimLetter
			}
			letterCell := ERAMMenuGridCell{
				// Assigning an interim altitude acts on the altitude to the
				// left, so hovering the T outlines both as one pick.
				Label: letter, Color: textC, Weight: 2, HoverSpansRow: true,
				OnClick: func(_ ERAMMenuClickType) bool {
					status, err := handleInterimAltitude(ep, ctx, InterimAltitude{Altitude: alt * 100}, trk)
					ep.applyCommandStatus(ctx, status, err)
					return true
				},
			}
			if isCurrent {
				letterCell.BgColor = colors.menu.currentValue
			}
			row = append(row, letterCell)
		} else {
			row = append(row, ERAMMenuGridCell{Weight: 2})
		}
		gridRows = append(gridRows, row)
	}
	rows = append(rows, ERAMMenuItem{Grid: &ERAMMenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, ERAMMenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "LOCAL TALT", "PROCEDURE", "FP 000", "000 T"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

///////////////////////////////////////////////////////////////////////////
// Heading Menu

// headingMenuPopup is the FDB Heading Menu, opened from Field D (CID) or the
// fourth-line assigned heading.
type headingMenuPopup struct {
	dbMenuBase
	offset      int
	lt, rt      bool // display left/right turn values
	initialized bool
}

func (ep *ERAMPane) openHeadingMenu(ctx *panes.Context, trk *sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(trk.FlightPlan.ACID)
	width := dbMenuWidth(font, titleFont, title, "000 000", "LT PH RT")
	height := 10 * dbMenuItemH // title + LT/PH/RT + 7 values + DELETE

	p := &headingMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   trk.FlightPlan.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
	}
	// Start in turn mode if a turn heading is currently assigned.
	if sp := trk.FlightPlan.Scratchpad; !isQSFreeTextScratchpad(sp) && len(sp) >= 2 {
		switch sp[len(sp)-1] {
		case 'L':
			p.lt = true
		case 'R':
			p.rt = true
		}
	}
	ep.popup = p
}

// headingMenuRows builds the two-column pick list. In turn mode the values
// are degrees of turn ("50L 55L" ...); otherwise absolute headings.
func headingMenuRows(lt, rt bool) [][2]string {
	var rows [][2]string
	if lt || rt {
		suffix := util.Select(lt, "L", "R")
		for t := 170; t >= 10; t -= 10 {
			rows = append(rows, [2]string{fmt.Sprintf("%d%s", t, suffix), fmt.Sprintf("%d%s", t+5, suffix)})
		}
		rows = append(rows, [2]string{"", "5" + suffix})
	} else {
		rows = append(rows, [2]string{"360", ""})
		for t := 350; t >= 10; t -= 10 {
			rows = append(rows, [2]string{fmt.Sprintf("%03d", t), fmt.Sprintf("%03d", t+5)})
		}
		rows = append(rows, [2]string{"", "005"})
	}
	return rows
}

func (p *headingMenuPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	fp := trk.FlightPlan
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	grey := colors.popup.backgroundGrey
	textC := colors.popup.text

	valueRows := headingMenuRows(p.lt, p.rt)

	if !p.initialized {
		p.initialized = true
		p.offset = max(len(valueRows)-7, 0) // small turns page by default
		if !p.lt && !p.rt {
			p.offset = 0 // absolute mode: start at 360
		}
		for i, r := range valueRows {
			if r[0] == fp.Scratchpad || r[1] == fp.Scratchpad {
				p.offset = math.Clamp(i-3, 0, max(len(valueRows)-7, 0))
				break
			}
		}
	}

	toggleTurn := func(v, other *bool) func(ERAMMenuClickType) bool {
		return func(_ ERAMMenuClickType) bool {
			*v = !*v
			if *v {
				*other = false
			}
			p.initialized = false // reposition the list for the new mode
			return false
		}
	}

	assignHeading := func(hdg string) func(ERAMMenuClickType) bool {
		return func(_ ERAMMenuClickType) bool {
			status, err := handleQSHeading(ep, ctx, hdg, trk)
			ep.applyCommandStatus(ctx, status, err)
			return true
		}
	}

	rows := []ERAMMenuItem{{Cells: []ERAMMenuCell{
		{Label: "LT", Color: textC, BgColor: util.Select(p.lt, colors.menu.selectedItem, grey), OnClick: toggleTurn(&p.lt, &p.rt)},
		{Label: "PH", Color: textC, BgColor: grey, OnClick: assignHeading("PH")},
		{Label: "RT", Color: textC, BgColor: util.Select(p.rt, colors.menu.selectedItem, grey), OnClick: toggleTurn(&p.rt, &p.lt)},
	}}}

	gridRows := make([][]ERAMMenuGridCell, 0, len(valueRows))
	for _, vr := range valueRows {
		row := make([]ERAMMenuGridCell, 0, 2)
		for _, v := range vr {
			cell := ERAMMenuGridCell{Label: v, Color: textC}
			if v != "" {
				if v == fp.Scratchpad {
					cell.BgColor = colors.menu.currentValue
				}
				cell.OnClick = assignHeading(v)
			}
			row = append(row, cell)
		}
		gridRows = append(gridRows, row)
	}
	rows = append(rows, ERAMMenuItem{Grid: &ERAMMenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	rows = append(rows, ERAMMenuItem{
		Label: "DELETE", Centered: true, BgColor: grey, Color: textC,
		OnClick: func(_ ERAMMenuClickType) bool {
			status, err := handleQSDeleteHeading(ep, ctx, trk)
			ep.applyCommandStatus(ctx, status, err)
			ep.popup = nil
			return true
		},
	})

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, ERAMMenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "000 000", "LT PH RT"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

///////////////////////////////////////////////////////////////////////////
// Speed Menu

// speedMenuPopup is the FDB Speed Menu, opened from Field E (ground speed)
// or the fourth-line assigned speed.
type speedMenuPopup struct {
	dbMenuBase
	offset      int
	mach        bool // display mach values rather than knots
	plus, minus bool // assign speeds as "greater than" / "less than"
	initialized bool
}

func (ep *ERAMPane) openSpeedMenu(ctx *panes.Context, trk *sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(trk.FlightPlan.ACID)
	width := dbMenuWidth(font, titleFont, title, "DEL M000", ".00+")
	height := 10 * dbMenuItemH // title + KT/M row + 7 values + DEL

	p := &speedMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   trk.FlightPlan.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
	}
	cur := trk.FlightPlan.SecondaryScratchpad
	p.mach = strings.HasPrefix(cur, "M")
	p.plus = strings.HasSuffix(cur, "+")
	p.minus = strings.HasSuffix(cur, "-")
	ep.popup = p
}

func (p *speedMenuPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	fp := trk.FlightPlan
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	grey := colors.popup.backgroundGrey
	textC := colors.popup.text

	// The stored value with any +/- modifier stripped, e.g. "M72" or "S250".
	currentValue := strings.TrimRight(fp.SecondaryScratchpad, "+-")

	// The pick list: (stored value, displayed label) pairs.
	type speedValue struct{ store, label string }
	var values []speedValue
	if p.mach {
		for m := 95; m >= 50; m-- {
			values = append(values, speedValue{fmt.Sprintf("M%02d", m), fmt.Sprintf(".%02d", m)})
		}
	} else {
		for kt := 450; kt >= 100; kt -= 10 {
			values = append(values, speedValue{fmt.Sprintf("S%d", kt), fmt.Sprintf("%d", kt)})
		}
	}

	if !p.initialized {
		p.initialized = true
		p.offset = 0
		center := -1
		if idx := slices.IndexFunc(values, func(v speedValue) bool { return v.store == currentValue }); idx >= 0 {
			center = idx
		} else if !p.mach {
			if state := ep.TrackState[trk.ADSBCallsign]; state != nil {
				gs := (int(state.Track.Groundspeed) + 5) / 10 * 10
				if idx := slices.IndexFunc(values, func(v speedValue) bool { return v.store == fmt.Sprintf("S%d", gs) }); idx >= 0 {
					center = idx
				}
			}
		} else {
			center = slices.IndexFunc(values, func(v speedValue) bool { return v.store == "M75" })
		}
		if center >= 0 {
			p.offset = math.Clamp(center-3, 0, max(len(values)-7, 0))
		}
	}

	mod := ""
	if p.plus {
		mod = "+"
	} else if p.minus {
		mod = "-"
	}

	toggleMod := func(v, other *bool) func(ERAMMenuClickType) bool {
		return func(_ ERAMMenuClickType) bool {
			*v = !*v
			if *v {
				*other = false
			}
			return false
		}
	}

	rows := []ERAMMenuItem{{Cells: []ERAMMenuCell{
		{Label: util.Select(p.mach, "M", "KT"), Color: textC, BgColor: grey, Weight: 2,
			OnClick: func(_ ERAMMenuClickType) bool {
				p.mach = !p.mach
				p.initialized = false // reposition the list for the new mode
				return false
			}},
		{Label: "+", Color: textC, BgColor: util.Select(p.plus, colors.menu.selectedItem, grey), OnClick: toggleMod(&p.plus, &p.minus)},
		{Label: "-", Color: textC, BgColor: util.Select(p.minus, colors.menu.selectedItem, grey), OnClick: toggleMod(&p.minus, &p.plus)},
	}}}

	gridRows := make([][]ERAMMenuGridCell, 0, len(values))
	for _, v := range values {
		isCurrent := v.store == currentValue
		label, store := v.label, v.store
		if !isCurrent {
			label += mod
			store += mod
		}
		cell := ERAMMenuGridCell{
			Label: label, Color: textC,
			OnClick: func(_ ERAMMenuClickType) bool {
				status, err := handleQSSpeed(ep, ctx, store, trk)
				ep.applyCommandStatus(ctx, status, err)
				return true
			},
		}
		if isCurrent {
			cell.BgColor = colors.menu.currentValue
		}
		gridRows = append(gridRows, []ERAMMenuGridCell{cell})
	}
	rows = append(rows, ERAMMenuItem{Grid: &ERAMMenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	rows = append(rows, ERAMMenuItem{
		Label:    util.Select(fp.SecondaryScratchpad != "", "DEL "+fp.SecondaryScratchpad, "DELETE"),
		Centered: true, BgColor: grey, Color: textC,
		OnClick: func(_ ERAMMenuClickType) bool {
			status, err := handleQSDeleteSpeed(ep, ctx, trk)
			ep.applyCommandStatus(ctx, status, err)
			ep.popup = nil
			return true
		},
	})

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, ERAMMenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "DEL M000", ".00+"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

///////////////////////////////////////////////////////////////////////////
// Free Form Text Box Menu

// freeTextMenuPopup is the FDB Free Form Text Box Menu, opened from
// fourth-line free-form text. It cannot be opened if the aircraft has no
// assigned free-form text.
type freeTextMenuPopup struct {
	dbMenuBase
	buf string
}

func (ep *ERAMPane) openFreeTextMenu(ctx *panes.Context, trk *sim.Track, dbMain math.Extent2D) {
	fp := trk.FlightPlan
	if fp == nil || !isQSFreeTextScratchpad(fp.Scratchpad) {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(fp.ACID)
	width := dbMenuWidth(font, titleFont, title, "WWWWWWWW_")
	height := 3 * dbMenuItemH // title + input + DELETE

	ep.popup = &freeTextMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   fp.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
		buf: stripQSFreeTextIndicator(fp.Scratchpad),
	}
}

// handleKeyboard consumes all keyboard input while the menu is open: typed
// characters edit the buffer, Enter saves it, Escape closes the menu.
func (p *freeTextMenuPopup) handleKeyboard(ep *ERAMPane, ctx *panes.Context) bool {
	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r <= ' ' || r > '~' || r == '`' {
			continue
		}
		if len(p.buf) >= 8 { // free text is limited to 8 characters
			break
		}
		p.buf += string(r)
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if len(p.buf) > 0 {
				p.buf = p.buf[:len(p.buf)-1]
			}
		case imgui.KeyEnter:
			if p.buf != "" {
				if trk := p.resolveTrack(ep, ctx); trk != nil {
					status, err := handleQSFreeText(ep, ctx, circleClear+p.buf, trk)
					ep.applyCommandStatus(ctx, status, err)
				}
			}
			ep.popup = nil
		case imgui.KeyEscape:
			ep.popup = nil
		}
	}
	return true
}

func (p *freeTextMenuPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	rows := []ERAMMenuItem{
		{Input: &ERAMMenuInput{Buf: &p.buf}},
		{
			Label: "DELETE", Centered: true, BgColor: colors.popup.backgroundGrey, Color: colors.popup.text,
			OnClick: func(_ ERAMMenuClickType) bool {
				status, err := handleQSDeleteAll(ep, ctx, trk)
				ep.applyCommandStatus(ctx, status, err)
				ep.popup = nil
				return true
			},
		},
	}

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, ERAMMenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "WWWWWWWW_"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

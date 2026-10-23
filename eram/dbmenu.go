// eram/dbmenu.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// The FDB field-click menus: altitude, heading, speed, free-form text, and
// the point out indicator's. Each is a popup (see menu.go) opened from a
// click on the corresponding datablock field in datablockInteractions.

// dbMenuBase carries the state shared by all datablock menus: which flight
// the menu is for and where it is drawn.
type dbMenuBase struct {
	cursorPlacement
	acid   sim.ACID
	origin [2]float32
}

// resolveTrack looks the menu's flight up fresh each frame; if it is gone
// (or has no flight plan) the menu closes and nil is returned.
func (m *dbMenuBase) resolveTrack(ep *Scope, ctx *scope.Context) *sim.Track {
	trk, ok := ctx.GetTrackByACID(m.acid)
	if !ok || trk.FlightPlan == nil {
		ep.popup = nil
		return nil
	}
	return trk
}

// openDatablockMenu clamps a new menu's placement (flipping to the left of
// the datablock if it would run off the pane edge) and returns its origin.
func (ep *Scope) openDatablockMenu(ctx *scope.Context, dbMain math.Extent2D, width, height float32) [2]float32 {
	pl := ep.OpenPopupAt(ctx, [2]float32{dbMain.P1[0], dbMain.P1[1]}, width, height, ep.ERAMFont(2), dbMain)
	return pl.Origin
}

// dbMenuWidth returns a menu width fitting the given body labels, the arrow
// column, and the title bar (callsign + X close button).
func dbMenuWidth(font, titleFont *renderer.Font, title string, labels ...string) float32 {
	w := util.ReduceMax(slices.Values(labels), 0, func(l string) float32 { return font.LayoutBounds(l, 0).Width() })
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
	offset    int
	local     bool // assign local interim altitude on selection
	procedure bool // assign procedure altitude on selection
}

// altitudeMenuAlts is the pick list in hundreds of feet, descending: steps
// of 1,000 feet up to FL410 and of 2,000 feet above it (8.6.1.2).
var altitudeMenuAlts = func() []int {
	var alts []int
	for a := 590; a > 410; a -= 20 {
		alts = append(alts, a)
	}
	for a := 410; a >= 10; a -= 10 {
		alts = append(alts, a)
	}
	return alts
}()

// altitudeMenuFPLabel returns the label of the altitude menu's Flight Plan
// Assigned Altitude pick area, which is shown when the flight has an
// interim, procedure, or local interim altitude or is assigned other than
// a single altitude (8.6.1.1.1, 8.6.1.2.c); ok is false when it is not
// shown.
func altitudeMenuFPLabel(fp *sim.FlightPlan) (label string, ok bool) {
	single := !fp.HasAltitudeBlock() && fp.AssignedAltitudeQualifier == sim.PlainAltitude
	if interim, _ := fp.DisplayedInterimAltitude(); single && interim == 0 {
		return "", false
	}
	return "FP " + formatAssignedAltitude(fp), true
}

// altitudeMenuInterimLabel returns the label of the altitude menu's Flight
// Plan Interim Altitude pick area, which shows the interim or procedure
// altitude that a local interim altitude is displayed in place of
// (8.6.1.2.d); ok is false when there are not both.
func altitudeMenuInterimLabel(fp *sim.FlightPlan) (label string, ok bool) {
	if fp.LocalInterimAlt == 0 || fp.InterimAlt == 0 {
		return "", false
	}
	return fmt.Sprintf("%03d%s", fp.InterimAlt/100, fp.InterimType), true
}

func (ep *Scope) openAltitudeMenu(ctx *scope.Context, trk *sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(trk.FlightPlan.ACID)
	fpLabel, fpRow := altitudeMenuFPLabel(trk.FlightPlan)
	width := dbMenuWidth(font, titleFont, title, "LOCAL TALT", "PROCEDURE", fpLabel, "000 T")

	nRows := 3 // title + LOCAL TALT + PROCEDURE
	if fpRow {
		nRows++
	}
	if _, ok := altitudeMenuInterimLabel(trk.FlightPlan); ok {
		nRows++
	}
	height := float32(nRows+7) * dbMenuItemH

	ep.popup = &altitudeMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   trk.FlightPlan.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
	}
}

func (p *altitudeMenuPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	fp := trk.FlightPlan
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	current := fp.DataBlockAltitude() / 100 // hundreds of feet; 0 if none
	interimAlt, interimType := fp.DisplayedInterimAltitude()

	first := p.firstDraw()
	if first {
		center := current
		if center == 0 {
			if state := ep.TrackState[trk.ADSBCallsign]; state != nil {
				center = (int(state.Track.TransponderAltitude) + 500) / 1000 * 10
			}
		}
		center = (center + 5) / 10 * 10
		if idx := slices.IndexFunc(altitudeMenuAlts, func(a int) bool { return a <= center }); idx >= 0 {
			p.offset = math.Clamp(idx-3, 0, max(len(altitudeMenuAlts)-7, 0))
		}
	}

	grey := colors.popup.backgroundGrey
	textC := colors.popup.text

	var rows []MenuItem
	fpLabel, fpRow := altitudeMenuFPLabel(fp)
	if fpRow {
		// The Flight Plan Assigned Altitude pick area removes the interim or
		// procedure altitude and the local interim altitude (8.6.1.2.c).
		// The cursor starts on it, or else on the current altitude value.
		rows = append(rows, MenuItem{
			Label: fpLabel, BgColor: grey, Color: textC, Centered: true, PlaceCursor: first,
			OnClick: func(_ MenuClickType) bool {
				var spec sim.FlightPlanSpecifier
				spec.InterimAlt.Set(0)
				spec.InterimType.Set(sim.InterimNormal)
				spec.LocalInterimAlt.Set(0)
				status, err := modifyInterimAltitude(ep, ctx, trk, spec)
				ep.applyCommandStatus(ctx, status, err)
				ep.popup = nil
				return true
			},
		})
	}
	if label, ok := altitudeMenuInterimLabel(fp); ok {
		// The Flight Plan Interim Altitude pick area removes the local
		// interim altitude, which redisplays the interim altitude.
		rows = append(rows, MenuItem{
			Label: label, BgColor: grey, Color: textC, Centered: true,
			OnClick: func(_ MenuClickType) bool {
				status, err := handleClearLocalInterimAltitude(ep, ctx, trk)
				ep.applyCommandStatus(ctx, status, err)
				ep.popup = nil
				return true
			},
		})
	}

	toggleRow := func(label string, v, other *bool) MenuItem {
		return MenuItem{
			Label: label, Centered: true, Color: textC,
			BgColor: util.Select(*v, colors.menu.selectedItem, grey),
			OnClick: func(_ MenuClickType) bool {
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
	gridRows := make([][]MenuGridCell, 0, len(altitudeMenuAlts))
	for _, alt := range altitudeMenuAlts {
		isCurrent := alt == current
		assignAlt := func(_ MenuClickType) bool {
			var status CommandStatus
			var err error
			if p.local {
				status, err = handleInterimAltitude(ep, ctx, InterimAltitude{Altitude: alt * 100, Type: sim.InterimLocal}, trk)
			} else if p.procedure {
				status, err = handleInterimAltitude(ep, ctx, InterimAltitude{Altitude: alt * 100, Type: sim.InterimProcedure}, trk)
			} else {
				status, err = handleAssignedAltitude(ep, ctx, alt*100, trk)
			}
			ep.applyCommandStatus(ctx, status, err)
			return true
		}

		altCell := MenuGridCell{
			Label: fmt.Sprintf("%03d", alt), Color: textC, Weight: 3, OnClick: assignAlt,
			PlaceCursor: first && !fpRow && isCurrent,
		}
		if isCurrent {
			altCell.BgColor = colors.menu.currentValue
		}

		row := []MenuGridCell{altCell}
		if showInterim {
			letter := "T"
			if isCurrent && interimAlt > 0 {
				letter = interimType.String()
			}
			letterCell := MenuGridCell{
				// Assigning an interim altitude acts on the altitude to the
				// left, so hovering the T outlines both as one pick.
				Label: letter, Color: textC, Weight: 2, HoverSpansRow: true,
				OnClick: func(_ MenuClickType) bool {
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
			row = append(row, MenuGridCell{Weight: 2})
		}
		gridRows = append(gridRows, row)
	}
	rows = append(rows, MenuItem{Grid: &MenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "LOCAL TALT", "PROCEDURE", fpLabel, "000 T"),
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
	initialized bool // scroll offset positioned for the current turn mode
}

func (ep *Scope) openHeadingMenu(ctx *scope.Context, trk *sim.Track, dbMain math.Extent2D) {
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
	if sp := trk.FlightPlan.Scratchpad; !isQSFreeText(sp) && len(sp) >= 2 {
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
// are the eleven turns of 5 to 55 degrees ("50L 55L" ...) (16.1.1.2);
// otherwise absolute headings.
func headingMenuRows(lt, rt bool) [][2]string {
	var rows [][2]string
	if lt || rt {
		suffix := util.Select(lt, "L", "R")
		for t := 50; t >= 10; t -= 10 {
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

// headingMenuCurrentValue returns the pick list value that the stored
// heading gives current value emphasis, or "" if none does. A compass
// heading or a turn of 5 to 55 degrees that is not a multiple of 5
// emphasizes the closest listed value (16.1.1.1, 16.1.1.2).
func headingMenuCurrentValue(heading string, lt, rt bool) string {
	digits, suffix := heading, ""
	lo, hi := 1, 360
	if lt || rt {
		suffix = util.Select(lt, "L", "R")
		var ok bool
		if digits, ok = strings.CutSuffix(heading, suffix); !ok {
			return ""
		}
		lo, hi = 5, 55
	} else if len(heading) != 3 {
		return ""
	}

	n, err := strconv.Atoi(digits)
	if err != nil || !util.IsAllNumbers(digits) || n < lo || n > hi {
		return ""
	}
	n = (n + 2) / 5 * 5
	if suffix != "" {
		return strconv.Itoa(n) + suffix
	}
	return fmt.Sprintf("%03d", util.Select(n == 0, 360, n))
}

func (p *headingMenuPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
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
	current := headingMenuCurrentValue(fp.Scratchpad, p.lt, p.rt)

	// The cursor starts on the current value or, for turns without one, on
	// the 30 degree turn (16.1.1.1, 16.1.1.2).
	var cursorValue string
	if p.firstDraw() {
		cursorValue = current
		if cursorValue == "" && (p.lt || p.rt) {
			cursorValue = "30" + util.Select(p.lt, "L", "R")
		}
	}

	if !p.initialized {
		p.initialized = true
		p.offset = 0
		for i, r := range valueRows {
			if current != "" && (r[0] == current || r[1] == current) {
				p.offset = math.Clamp(i-3, 0, max(len(valueRows)-7, 0))
				break
			}
		}
	}

	toggleTurn := func(v, other *bool) func(MenuClickType) bool {
		return func(_ MenuClickType) bool {
			*v = !*v
			if *v {
				*other = false
			}
			p.initialized = false // reposition the list for the new mode
			return false
		}
	}

	assignHeading := func(hdg string) func(MenuClickType) bool {
		return func(_ MenuClickType) bool {
			status, err := handleQSHeading(ep, ctx, hdg, trk)
			ep.applyCommandStatus(ctx, status, err)
			return true
		}
	}

	rows := []MenuItem{{Cells: []MenuCell{
		{Label: "LT", Color: textC, BgColor: util.Select(p.lt, colors.menu.selectedItem, grey), OnClick: toggleTurn(&p.lt, &p.rt)},
		{Label: "PH", Color: textC, BgColor: grey, OnClick: assignHeading("PH")},
		{Label: "RT", Color: textC, BgColor: util.Select(p.rt, colors.menu.selectedItem, grey), OnClick: toggleTurn(&p.rt, &p.lt)},
	}}}

	gridRows := make([][]MenuGridCell, 0, len(valueRows))
	for _, vr := range valueRows {
		row := make([]MenuGridCell, 0, 2)
		for _, v := range vr {
			cell := MenuGridCell{Label: v, Color: textC}
			if v != "" {
				if v == current {
					cell.BgColor = colors.menu.currentValue
				}
				cell.PlaceCursor = v == cursorValue
				cell.OnClick = assignHeading(v)
			}
			row = append(row, cell)
		}
		gridRows = append(gridRows, row)
	}
	rows = append(rows, MenuItem{Grid: &MenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	rows = append(rows, MenuItem{
		Label: "DELETE", Centered: true, BgColor: grey, Color: textC,
		OnClick: func(_ MenuClickType) bool {
			status, err := handleQSDeleteHeading(ep, ctx, trk)
			ep.applyCommandStatus(ctx, status, err)
			ep.popup = nil
			return true
		},
	})

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
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

func (ep *Scope) openSpeedMenu(ctx *scope.Context, trk *sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	title := string(trk.FlightPlan.ACID)
	width := dbMenuWidth(font, titleFont, title, "DEL M000", ".00+")
	cur := trk.FlightPlan.SecondaryScratchpad
	nRows := util.Select(cur != "", 10, 9) // title + KT/M row + 7 values + DEL if a speed is assigned
	height := float32(nRows) * dbMenuItemH

	ep.popup = &speedMenuPopup{
		dbMenuBase: dbMenuBase{
			acid:   trk.FlightPlan.ACID,
			origin: ep.openDatablockMenu(ctx, dbMain, width, height),
		},
		mach:  speedMenuOpensInMach(trk.FlightPlan),
		plus:  strings.HasSuffix(cur, "+"),
		minus: strings.HasSuffix(cur, "-"),
	}
}

// speedMenuOpensInMach reports whether the speed menu opens with Mach
// values: it does for an assigned Mach number and, with no speed assigned,
// for a flight assigned FL290 or above (11.2.2.3, 11.2.2.4).
func speedMenuOpensInMach(fp *sim.FlightPlan) bool {
	if fp.SecondaryScratchpad != "" {
		return strings.HasPrefix(fp.SecondaryScratchpad, "M")
	}
	alt := fp.DataBlockAltitude()
	if alt == 0 && fp.HasAltitudeBlock() {
		alt = fp.AltitudeBlock[0]
	}
	return alt >= 29000
}

// speedValue is a speed menu pick: the stored value and the label shown.
type speedValue struct{ store, label string }

// speedMenuValues returns the speed menu's pick list, fastest first: 400 to
// 70 knots by 10 or Mach .99 to .61 by .01. 11.2.2.4 gives 380 knots as the
// top of the list, but figures 11-2 and 11-11 show it at 400. Knots are
// stored with three digits, as QS stores them.
func speedMenuValues(mach bool) []speedValue {
	var values []speedValue
	if mach {
		for m := 99; m >= 61; m-- {
			values = append(values, speedValue{fmt.Sprintf("M%02d", m), fmt.Sprintf(".%02d", m)})
		}
	} else {
		for kt := 400; kt >= 70; kt -= 10 {
			values = append(values, speedValue{fmt.Sprintf("S%03d", kt), fmt.Sprintf("%d", kt)})
		}
	}
	return values
}

func (p *speedMenuPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
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
	first := p.firstDraw()

	values := speedMenuValues(p.mach)

	if !p.initialized {
		p.initialized = true
		p.offset = 0
		center := -1
		if idx := slices.IndexFunc(values, func(v speedValue) bool { return v.store == currentValue }); idx >= 0 {
			center = idx
		} else if !p.mach {
			if state := ep.TrackState[trk.ADSBCallsign]; state != nil {
				gs := (int(state.Track.Groundspeed) + 5) / 10 * 10
				if idx := slices.IndexFunc(values, func(v speedValue) bool { return v.store == fmt.Sprintf("S%03d", gs) }); idx >= 0 {
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

	toggleMod := func(v, other *bool) func(MenuClickType) bool {
		return func(_ MenuClickType) bool {
			*v = !*v
			if *v {
				*other = false
			}
			return false
		}
	}

	rows := []MenuItem{{Cells: []MenuCell{
		{Label: util.Select(p.mach, "M", "KT"), Color: textC, BgColor: grey, Weight: 2,
			OnClick: func(_ MenuClickType) bool {
				p.mach = !p.mach
				p.initialized = false // reposition the list for the new mode
				return false
			}},
		{Label: "+", Color: textC, BgColor: util.Select(p.plus, colors.menu.selectedItem, grey), OnClick: toggleMod(&p.plus, &p.minus)},
		{Label: "-", Color: textC, BgColor: util.Select(p.minus, colors.menu.selectedItem, grey), OnClick: toggleMod(&p.minus, &p.plus)},
	}}}

	gridRows := make([][]MenuGridCell, 0, len(values))
	for _, v := range values {
		isCurrent := v.store == currentValue
		label, store := v.label, v.store
		if !isCurrent {
			label += mod
			store += mod
		}
		// As in the altitude menu, the cursor starts on the current value.
		cell := MenuGridCell{
			Label: label, Color: textC,
			PlaceCursor: first && isCurrent,
			OnClick: func(_ MenuClickType) bool {
				status, err := handleQSSpeed(ep, ctx, store, trk)
				ep.applyCommandStatus(ctx, status, err)
				return true
			},
		}
		if isCurrent {
			cell.BgColor = colors.menu.currentValue
		}
		gridRows = append(gridRows, []MenuGridCell{cell})
	}
	rows = append(rows, MenuItem{Grid: &MenuGrid{Rows: gridRows, VisibleRows: 7, Offset: &p.offset}})

	// The Delete Speed pick area is shown only for an assigned speed (11.2.2.5).
	if fp.SecondaryScratchpad != "" {
		rows = append(rows, MenuItem{
			Label:    "DEL " + fp.SecondaryScratchpad,
			Centered: true, BgColor: grey, Color: textC,
			OnClick: func(_ MenuClickType) bool {
				status, err := handleQSDeleteSpeed(ep, ctx, trk)
				ep.applyCommandStatus(ctx, status, err)
				ep.popup = nil
				return true
			},
		})
	}

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "DEL M000", ".00+"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

///////////////////////////////////////////////////////////////////////////
// Free Form Text Box Menu

// freeTextMenuPopup is the FDB Free Form Text Box Menu, opened by clicking
// the fourth line — on existing free-form text to edit it, or on an empty
// fourth line to create it.
type freeTextMenuPopup struct {
	dbMenuBase
	buf string
}

func (ep *Scope) openFreeTextMenu(ctx *scope.Context, trk *sim.Track, dbMain math.Extent2D) {
	fp := trk.FlightPlan
	if fp == nil {
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
func (p *freeTextMenuPopup) handleKeyboard(ep *Scope, ctx *scope.Context) bool {
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
					status, err := handleQSFreeText(ep, ctx, p.buf, trk)
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

func (p *freeTextMenuPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	trk := p.resolveTrack(ep, ctx)
	if trk == nil {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)

	rows := []MenuItem{
		{Input: &MenuInput{Buf: &p.buf}},
		{
			// The cursor starts on the Free Form Delete pick area (16.3).
			Label: "DELETE", Centered: true, BgColor: colors.popup.backgroundGrey, Color: colors.popup.text,
			PlaceCursor: p.firstDraw(),
			OnClick: func(_ MenuClickType) bool {
				status, err := handleQSDeleteAll(ep, ctx, trk)
				ep.applyCommandStatus(ctx, status, err)
				ep.popup = nil
				return true
			},
		},
	}

	title := string(p.acid)
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: title, TitleLeftJustified: true,
		Width: dbMenuWidth(font, titleFont, title, "WWWWWWWW_"),
		Font:  font, TitleFont: titleFont, ItemHeight: dbMenuItemH,
		Rows: rows,
	})
}

///////////////////////////////////////////////////////////////////////////
// Point out indicator and menu

// pointOutIndicatorActive reports whether a P/A indicator should be drawn on
// datablock line 0 for the track.
func (ep *Scope) pointOutIndicatorActive(ctx *scope.Context, trk *sim.Track) bool {
	if trk.FlightPlan == nil {
		return false
	}
	fp := trk.FlightPlan
	return len(ctx.InboundPointOuts(fp)) > 0 || len(ctx.OutboundPointOuts(fp)) > 0 ||
		len(ep.AckedPointOuts[fp.ACID]) > 0
}

// pointOutIndicatorGlyph returns the indicator character (P or A) and color for the line-0
// indicator, or zero rune if nothing should be drawn. Yellow "P" is shown if any inbound or
// outbound point out is pending; white "A" if all that remain are the user's acknowledged ones.
// The TEXT brightness control sets the indicator's brightness (5.1.5.11.2),
// and dwell emphasis adds to it (5.1.5.8).
func (ep *Scope) pointOutIndicatorGlyph(ctx *scope.Context, trk *sim.Track) (rune, renderer.RGB, bool) {
	if trk.FlightPlan == nil {
		return 0, renderer.RGB{}, false
	}
	fp := trk.FlightPlan
	bright := ep.currentPrefs().Brightness.Text + ep.dwellBrightness(*trk)
	if len(ctx.InboundPointOuts(fp)) > 0 || len(ctx.OutboundPointOuts(fp)) > 0 {
		return 'P', bright.ScaleRGB(colors.yellow), true
	} else if len(ep.AckedPointOuts[fp.ACID]) > 0 {
		return 'A', bright.ScaleRGB(colors.pointOut.white), true
	}
	return 0, renderer.RGB{}, false
}

// handlePointOutIndicatorClick handles a click on the line-0 P/A point-out indicator. Clicking the
// yellow "P" opens the pop-up menu; clicking the white "A" removes it directly. With inbound
// point outs the P always opens the receiver pop-up; otherwise it opens the originator pop-up.
// dbMain is the main datablock extent; the menu is anchored at its top-right corner so it sits
// immediately to the right of the datablock.
func (ep *Scope) handlePointOutIndicatorClick(ctx *scope.Context, trk sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	fp := trk.FlightPlan
	origin := [2]float32{dbMain.P1[0], dbMain.P1[1]}
	if len(ctx.InboundPointOuts(fp)) > 0 {
		ep.popup = &pointOutPopup{acid: fp.ACID, outbound: false, origin: origin}
	} else if len(ctx.OutboundPointOuts(fp)) > 0 {
		ep.popup = &pointOutPopup{acid: fp.ACID, outbound: true, origin: origin}
	} else {
		delete(ep.AckedPointOuts, fp.ACID)
	}
}

// pointOutPopup is the popup-interface impl for the click-through pop-up
// triggered from the line-0 point-out indicator. Per-instance state (which
// ACID, originator vs receiver view, anchor origin) lives inline rather than
// on Scope.
type pointOutPopup struct {
	acid     sim.ACID
	outbound bool
	origin   [2]float32
	// originators lists the sectors in a receiver's pop-up; those the user
	// acknowledges stay listed until removed.
	originators []pointOutOriginator
}

type pointOutOriginator struct {
	pos   sim.ControlPosition
	acked bool
}

// draw renders the menu, whose one row row returns.
func (po *pointOutPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	trk, ok := ctx.Client.State.GetTrackByACID(po.acid)
	if !ok {
		ep.popup = nil
		return
	}
	row := po.row(ep, ctx, trk)
	if len(row.Segments) == 0 {
		ep.popup = nil
		return
	}

	ps := ep.currentPrefs()
	rowFont := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	titleW := titleFont.LayoutBounds(string(po.acid), 0).Width()
	xW := titleFont.LayoutBounds("X", 0).Width()
	// Left pad (4) + title + gap (xW) + X button (xW + 2*xPad) + right pad (2).
	width := max(titleW+2*xW+12, menuRowWidth(rowFont, row))

	cfg := MenuConfig{
		Title:              string(po.acid),
		TitleLeftJustified: true,
		Width:              width,
		Font:               rowFont,
		TitleFont:          titleFont,
		Rows:               []MenuItem{row},
	}

	ep.DrawERAMMenu(ctx, transforms, cb, po.origin, cfg)
}

// row returns the pop-up's row: a P followed by the other sectors of the
// point outs. In the originator's view, receivers that have not acknowledged
// are yellow and boxed and those that have are white; a click on one of
// those removes it (5.1.5.11.1). In the receiver's view, originators are
// cyan and boxed; a click acknowledges one, turning it white, and another
// click on it removes it. The P turns white once all are acknowledged
// (5.1.5.11.2).
func (po *pointOutPopup) row(ep *Scope, ctx *scope.Context, trk *sim.Track) MenuItem {
	row := MenuItem{Label: "P"}
	if po.outbound {
		row.Color = colors.yellow
		for _, p := range ctx.OutboundPointOuts(trk.FlightPlan) {
			row.Segments = append(row.Segments,
				MenuSegment{Text: sectorDisplayID(ctx, p.ToController), Color: colors.yellow, Boxed: true})
		}
		for _, receiver := range ep.AckedPointOuts[po.acid] {
			row.Segments = append(row.Segments, MenuSegment{
				Text:  sectorDisplayID(ctx, receiver),
				Color: colors.pointOut.white,
				OnClick: func(_ MenuClickType) bool {
					ep.removeAckedPointOut(po.acid, receiver)
					return false
				},
			})
		}
		return row
	}

	po.updateOriginators(ctx.InboundPointOuts(trk.FlightPlan))
	row.Color = colors.pointOut.white
	for i, o := range po.originators {
		seg := MenuSegment{Text: sectorDisplayID(ctx, o.pos), Color: colors.pointOut.white}
		if o.acked {
			seg.OnClick = func(_ MenuClickType) bool {
				po.originators = slices.Delete(po.originators, i, i+1)
				return false
			}
		} else {
			row.Color = colors.pointOut.cyan
			seg.Color, seg.Boxed = colors.pointOut.cyan, true
			seg.OnClick = func(_ MenuClickType) bool {
				po.originators[i].acked = true
				ep.acknowledgePointOut(ctx, trk, o.pos)
				return false
			}
		}
		row.Segments = append(row.Segments, seg)
	}
	return row
}

// updateOriginators brings a receiver's list of originating sectors up to
// date with the pending point outs: new ones join it, and unacknowledged ones
// that are no longer pending, having been recalled, leave it.
func (po *pointOutPopup) updateOriginators(pending []sim.PointOut) {
	isPending := func(p sim.ControlPosition) bool {
		return slices.ContainsFunc(pending, func(o sim.PointOut) bool { return o.FromController == p })
	}
	po.originators = slices.DeleteFunc(po.originators, func(o pointOutOriginator) bool {
		return !o.acked && !isPending(o.pos)
	})
	for _, p := range pending {
		if !slices.ContainsFunc(po.originators, func(o pointOutOriginator) bool { return o.pos == p.FromController }) {
			po.originators = append(po.originators, pointOutOriginator{pos: p.FromController})
		}
	}
}

// removeAckedPointOut dismisses receiver's acknowledgment of the user's point
// out.
func (ep *Scope) removeAckedPointOut(acid sim.ACID, receiver sim.ControlPosition) {
	ep.AckedPointOuts[acid] = slices.DeleteFunc(ep.AckedPointOuts[acid],
		func(p sim.ControlPosition) bool { return p == receiver })
	if len(ep.AckedPointOuts[acid]) == 0 {
		delete(ep.AckedPointOuts, acid)
	}
}

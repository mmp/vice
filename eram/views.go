// eram/views.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// drawViews, and the views with little state of their own: the Range View,
// TIME, STATUS, OUTAGE, and the CHECK LISTs. The other views are in files
// of their own or with the commands that fill them.

func (ep *Scope) drawViews(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	ep.drawResponseArea(ctx, transforms, cb)
	ep.drawMessageCompositionArea(ctx, transforms, cb)
	ep.drawAltimSetView(ctx, transforms, cb)
	ep.drawWXView(ctx, transforms, cb)
	ep.drawBeaconCodeView(ctx, transforms, cb)
	ep.drawTimeView(ctx, transforms, cb)
	ep.drawStatusView(ctx, transforms, cb)
	ep.drawOutageView(ctx, transforms, cb)
	ep.drawRangeView(ctx, transforms, cb)
	ep.drawCheckListView(ctx, transforms, cb, &emergCheckList)
	ep.drawCheckListView(ctx, transforms, cb, &posCheckList)
	ep.drawCRRView(ctx, tracks, transforms, cb)
	ep.drawCFRView(ctx, transforms, cb)
	ep.drawSAAFilterView(ctx, transforms, cb)
	ep.drawFlightEventView(ctx, transforms, cb)
	ep.drawDeptListView(ctx, transforms, cb)
	ep.drawGroupSupView(ctx, transforms, cb)
	ep.drawConflictAlertView(ctx, transforms, cb)
	if ep.popup != nil {
		ep.popup.draw(ep, ctx, transforms, cb)
	}
}

///////////////////////////////////////////////////////////////////////////
// Range View

// The Range View sets the range: in steps of 25 NM, with its slider, from
// either of two preset ranges, by typing one in, or back to the range
// before the last change (3.4). Its ranges are those the RANGE button
// shows, half of the scope's vertical extent.

const (
	minViewRange = 1
	// maxViewRange is the scroll wheel's limit as well.
	maxViewRange         = 1300
	rangeViewStep        = 25
	rangeViewTroughStep  = 10
	rangeViewEntryChars  = 4
	rangeViewSliderChars = 5
)

// rangeViewField is a Range View field that takes typed entry.
type rangeViewField int

const (
	rangeFieldNone rangeViewField = iota
	rangeFieldValue
	rangeFieldPreset1
	rangeFieldPreset2
)

var rangePresetFields = [2]rangeViewField{rangeFieldPreset1, rangeFieldPreset2}

// preset returns the index of the preset that the field is, if it is one.
func (f rangeViewField) preset() (int, bool) {
	i := slices.Index(rangePresetFields[:], f)
	return i, i >= 0
}

// rangeViewState is the Range View's transient state.
type rangeViewState struct {
	previous float32 // the range RETURN goes back to; 0 if there is none
	// sliding is set while the slider is being dragged; slideRange is the
	// range at its position, which replaces the range only when the slider
	// is dropped with TBE.
	sliding    bool
	slideRange float32
	entry      rangeViewField
	buf        string
}

// viewRange returns the range as the RANGE button and the Range View show
// it, which is half of the vertical extent Preferences.Range holds.
func (ep *Scope) viewRange() float32 {
	return ep.currentPrefs().Range / 2
}

func formatRange(r float32) string {
	if r >= 2 {
		return fmt.Sprint(int(r))
	}
	return fmt.Sprintf("%.2f", r)
}

func rangeLabel(ep *Scope) string {
	return "RANGE\n" + formatRange(ep.viewRange())
}

// setViewRange sets the range and remembers the one it replaces for RETURN.
func (ep *Scope) setViewRange(r float32) {
	ps := ep.currentPrefs()
	ep.rangeView.previous = ps.Range / 2
	ps.Range = 2 * r
}

// nudgeRange changes the range by delta, stopping at the view's limits.
func (ep *Scope) nudgeRange(delta float32) error {
	r := ep.viewRange()
	if (delta < 0 && r <= minViewRange) || (delta > 0 && r >= maxViewRange) {
		return errToolbarLimit
	}
	ep.setViewRange(math.Clamp(r+delta, minViewRange, maxViewRange))
	return nil
}

// returnRange goes back to the range before the last change; the range it
// leaves becomes the one that RETURN goes back to.
func (ep *Scope) returnRange() error {
	if ep.rangeView.previous == 0 {
		return errToolbarLimit
	}
	ep.setViewRange(ep.rangeView.previous)
	return nil
}

// rangePresetSelected reports whether a preset is the current range; it is
// then shown selected.
func (ep *Scope) rangePresetSelected(i int) bool {
	return float32(ep.currentPrefs().RangeView.Presets[i]) == ep.viewRange()
}

func (ep *Scope) selectRangePreset(i int) {
	if !ep.rangePresetSelected(i) {
		ep.setViewRange(float32(ep.currentPrefs().RangeView.Presets[i]))
	}
}

// startRangeEntry starts typed entry into a field. A selected preset can't
// be changed.
func (ep *Scope) startRangeEntry(f rangeViewField) error {
	if i, ok := f.preset(); ok && ep.rangePresetSelected(i) {
		return errToolbarLimit
	}
	ep.rangeView.entry, ep.rangeView.buf = f, ""
	return nil
}

// enterRange applies a typed entry, a whole number of NM within the view's
// limits, and reports whether it was valid.
func (ep *Scope) enterRange() bool {
	rv := &ep.rangeView
	n, err := strconv.Atoi(rv.buf)
	if err != nil || n < minViewRange || n > maxViewRange {
		return false
	}
	if i, ok := rv.entry.preset(); ok {
		ep.currentPrefs().RangeView.Presets[i] = n
	} else {
		ep.setViewRange(float32(n))
	}
	rv.entry, rv.buf = rangeFieldNone, ""
	return true
}

// rangeViewLayout is the Range View's geometry: the cells of its header,
// the row that shows the range, and the slider's trough.
type rangeViewLayout struct {
	menu, title, step, ret, close math.Extent2D
	presets                       [2]math.Extent2D
	cells                         []math.Extent2D // all of the header's, including the empty ones
	header, value, trough         math.Extent2D
	sliderWidth                   float32
}

func layoutRangeView(font *renderer.Font, topLeft [2]float32) rangeViewLayout {
	var l rangeViewLayout
	cw, rowH := charWidth(font), viewTitleHeight(font)
	x, top := topLeft[0], topLeft[1]
	cell := func(chars int) math.Extent2D {
		e := math.Extent2D{P0: [2]float32{x, top - rowH},
			P1: [2]float32{x + float32(chars)*cw + 2*titleBarButtonPad, top}}
		x = e.P1[0]
		l.cells = append(l.cells, e)
		return e
	}

	l.menu = cell(1)
	l.title = cell(len("RANGE"))
	l.step = cell(len("-/+"))
	l.ret = cell(len("RETURN"))
	cell(3)
	l.presets[0] = cell(rangeViewEntryChars)
	l.presets[1] = cell(rangeViewEntryChars)
	cell(7)
	l.close = cell(1)

	l.header = math.Extent2D{P0: [2]float32{topLeft[0], top - rowH}, P1: [2]float32{x, top}}
	l.value = l.header.Offset([2]float32{0, -rowH})
	l.trough = l.value.Offset([2]float32{0, -rowH})
	l.sliderWidth = rangeViewSliderChars*cw + 2*titleBarButtonPad
	return l
}

func (l rangeViewLayout) extent() math.Extent2D {
	return math.Extent2D{P0: l.trough.P0, P1: l.header.P1}
}

// slider returns the slider's extent at the given range; the trough spans
// the view's limits.
func (l rangeViewLayout) slider(r float32) math.Extent2D {
	t := (math.Clamp(r, minViewRange, maxViewRange) - minViewRange) / (maxViewRange - minViewRange)
	x := l.trough.P0[0] + t*(l.trough.Width()-l.sliderWidth)
	return math.Extent2D{P0: [2]float32{x, l.trough.P0[1]}, P1: [2]float32{x + l.sliderWidth, l.trough.P1[1]}}
}

// sliderRange returns the range, to the whole NM, for the slider centered
// at x.
func (l rangeViewLayout) sliderRange(x float32) float32 {
	t := math.Clamp((x-l.sliderWidth/2-l.trough.P0[0])/(l.trough.Width()-l.sliderWidth), 0, 1)
	return math.Round(minViewRange + t*(maxViewRange-minViewRange))
}

// shownRange returns the range the view shows: the slider's while it is
// being dragged and otherwise the scope's.
func (ep *Scope) shownRange() float32 {
	return util.Select(ep.rangeView.sliding, ep.rangeView.slideRange, ep.viewRange())
}

func (ep *Scope) drawRangeView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.RangeView.Visible {
		ep.rangeView.sliding = false
		ep.rangeView.entry, ep.rangeView.buf = rangeFieldNone, ""
		return
	}

	font := ep.ERAMFont(2)
	size := layoutRangeView(font, [2]float32{}).extent()
	topLeft := ep.clampViewPos(ctx, ps.RangeView.Position, size.Width(), size.Height())
	l := layoutRangeView(font, topLeft)

	if ep.rangeView.sliding && ctx.Mouse != nil {
		ep.rangeView.slideRange = l.sliderRange(ctx.Mouse.Pos[0])
	}
	ep.clickRangeView(ctx, l)
	if !ps.RangeView.Visible {
		return
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.RangeView.Position,
		ID:         "range",
		Width:      size.Width(),
		BodyHeight: size.Height(),
		Opaque:     true,
		ShowBorder: true,
		Brightness: ps.RangeView.Bright,
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			ep.drawRangeViewBody(ctx, layoutRangeView(font, [2]float32{body.P0[0], body.P1[1]}), font, b)
		},
	})
}

// clickRangeView handles a click on the Range View. The clicks that it
// leaves alone, on its title and its empty cells, move the view.
func (ep *Scope) clickRangeView(ctx *scope.Context, l rangeViewLayout) {
	mouse := ctx.Mouse
	if mouse == nil || ep.viewRepo.activeID != "" || (ep.popup != nil && ep.popupExtent.Inside(mouse.Pos)) {
		return
	}
	var c toolbarClick
	c.clicked[platform.MouseButtonPrimary] = ep.mousePrimaryClicked(mouse)
	c.clicked[platform.MouseButtonTertiary] = ep.mouseTertiaryClicked(mouse)
	if c == (toolbarClick{}) {
		return
	}
	tertiary := c.clicked[platform.MouseButtonTertiary]
	rv := &ep.rangeView

	// While the slider is dragged, TBE anywhere replaces the range with the
	// slider's and TBP cancels.
	if rv.sliding {
		if tertiary {
			ep.setViewRange(rv.slideRange)
		}
		rv.sliding = false
		ep.consumeMouseClick(mouse)
		return
	}

	p := mouse.Pos
	if rv.entry != rangeFieldNone {
		rv.entry, rv.buf = rangeFieldNone, ""
	}
	if !l.extent().Inside(p) {
		return
	}

	ps := ep.currentPrefs()
	var err error
	switch {
	case l.menu.Inside(p):
		ep.popup = ep.makeViewMenu(ctx, "range", 1,
			func(pb popupBase) popup { return &rangeViewPopup{popupBase: pb} })(l.extent())
	case l.step.Inside(p):
		err = ep.nudgeRange(util.Select(tertiary, float32(rangeViewStep), -rangeViewStep))
	case l.ret.Inside(p):
		err = ep.returnRange()
	case l.presets[0].Inside(p), l.presets[1].Inside(p):
		i := util.Select(l.presets[0].Inside(p), 0, 1)
		if tertiary {
			ep.selectRangePreset(i)
		} else {
			err = ep.startRangeEntry(rangePresetFields[i])
		}
	case l.close.Inside(p):
		ps.RangeView.Visible = false
		ep.closeViewPopup("range")
	case l.value.Inside(p):
		if !tertiary {
			err = ep.startRangeEntry(rangeFieldValue)
		}
	case l.slider(ep.viewRange()).Inside(p):
		rv.sliding, rv.slideRange = true, ep.viewRange()
	case l.trough.Inside(p):
		left := p[0] < l.slider(ep.viewRange()).P0[0]
		err = ep.nudgeRange(util.Select(left, float32(-rangeViewTroughStep), rangeViewTroughStep))
	default:
		return
	}
	if err != nil {
		ep.reportClickAtLimit(c)
	}
	ep.consumeMouseClick(mouse)
}

func (ep *Scope) drawRangeViewBody(ctx *scope.Context, l rangeViewLayout, font *renderer.Font, b *ViewBuilders) {
	ps := ep.currentPrefs()
	rv := &ep.rangeView
	text := ps.RangeView.Bright.ScaleRGB(colors.view.text)
	style := renderer.TextStyle{Font: font, Color: text}
	label := func(e math.Extent2D, s string) {
		b.Td.AddTextCentered(s, e.Center(), style)
	}
	entryText := func(f rangeViewField, s string) string {
		return util.Select(rv.entry == f, rv.buf, s)
	}

	addQuad(b.Trid, l.header, renderer.RGB{})
	addQuad(b.Trid, math.Extent2D{P0: l.menu.P0, P1: l.title.P1}, colors.view.opaqueBackground)
	addQuad(b.Trid, l.step, colors.popup.backgroundGreen)
	for i, e := range l.presets {
		if ep.rangePresetSelected(i) {
			addQuad(b.Trid, e, colors.popup.backgroundGrey)
		}
	}
	addQuad(b.Trid, math.Extent2D{P0: l.trough.P0, P1: l.value.P1}, colors.view.opaqueBackground)

	label(l.menu, "M")
	label(l.title, "RANGE")
	label(l.step, "-/+")
	label(l.ret, "RETURN")
	for i, e := range l.presets {
		label(e, entryText(rangePresetFields[i], fmt.Sprint(ps.RangeView.Presets[i])))
	}
	label(l.close, "-")

	// The range is shown over the slider.
	slider := l.slider(ep.shownRange())
	value := math.Extent2D{P0: [2]float32{slider.P0[0], l.value.P0[1]}, P1: [2]float32{slider.P1[0], l.value.P1[1]}}
	label(value, entryText(rangeFieldValue, formatRange(ep.shownRange())))

	for _, e := range l.cells {
		b.Ld.AddLine([2]float32{e.P1[0], e.P0[1]}, e.P1, colors.view.buttonOutline)
	}
	b.Ld.AddLine(l.value.P1, [2]float32{l.value.P0[0], l.value.P1[1]}, colors.view.buttonOutline)
	b.Ld.AddLine(l.trough.P1, [2]float32{l.trough.P0[0], l.trough.P1[1]}, colors.view.buttonOutline)
	b.Ld.AddLineLoop(colors.view.buttonOutline, slider.Corners())

	if ctx.Mouse != nil && !rv.sliding {
		for _, e := range []math.Extent2D{l.menu, l.step, l.ret, l.presets[0], l.presets[1], l.close, value, slider} {
			if e.Inside(ctx.Mouse.Pos) {
				b.Ld.AddLineLoop(colors.view.hoveredOutline, e.Corners())
			}
		}
	}
}

// handleRangeViewKeyboard takes the keystrokes for typed entry into the
// Range View, and Escape while its slider is dragged, returning true if it
// took them.
func (ep *Scope) handleRangeViewKeyboard(ctx *scope.Context) bool {
	rv := &ep.rangeView
	if rv.sliding {
		if _, ok := ctx.Keyboard.Pressed[imgui.KeyEscape]; ok {
			rv.sliding = false
			return true
		}
		return false
	}
	if rv.entry == rangeFieldNone {
		return false
	}

	for _, r := range ctx.Keyboard.Input {
		if r >= '0' && r <= '9' && len(rv.buf) < rangeViewEntryChars {
			rv.buf += string(r)
		}
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if n := len(rv.buf); n > 0 {
				rv.buf = rv.buf[:n-1]
			}
		case imgui.KeyEnter:
			if !ep.enterRange() {
				ep.soundAlarm()
			}
		case imgui.KeyEscape:
			rv.entry, rv.buf = rangeFieldNone, ""
		}
	}
	return true
}

// rangeViewPopup is the Range View's menu.
type rangeViewPopup struct {
	popupBase
}

func (r *rangeViewPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	ep.DrawERAMMenu(ctx, transforms, cb, r.origin, MenuConfig{
		Title: "RANGE",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  []MenuItem{makeIntMenuItem(ep, &ps.RangeView.Bright, "BRIGHT", 0, 100, 2)},
	})
}

///////////////////////////////////////////////////////////////////////////
// Status and Outage

// drawStatusView draws the Status view, which shows the state of the
// position's channels and its outages and settings (4.3.4). vice has one
// channel, which is always active, and no outages.
func (ep *Scope) drawStatusView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.StatusView.Visible {
		return
	}
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.StatusView.Position,
		ID:         "status",
		Title:      "STATUS",
		Opaque:     true,
		ShowBorder: true,
		Brightness: ps.MCA.Bright,
		OnMenu: ep.makeViewMenu(ctx, "status", 1,
			func(pb popupBase) popup { return &statusViewPopup{popupBase: pb} }),
		OnMinimize: func() { ps.StatusView.Visible = false },
		RowSource: &ViewRowSource{
			Rows:                    []Row{{Label: "CH: A - ACTIVE"}},
			FontIndex:               ps.StatusView.Font,
			ContentChars:            len("CH: A - ACTIVE"),
			MaxCols:                 1,
			VisibleRows:             1,
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
		},
	})
}

// statusViewPopup is the Status view's menu, which has only FONT
// (figure 4-262).
type statusViewPopup struct {
	popupBase
}

func (p *statusViewPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: "STATUS",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  []MenuItem{makeIntMenuItem(ep, &ps.StatusView.Font, "FONT", 1, 3, 1)},
	})
}

// drawOutageView draws the Outage view, which lists the system's outages
// (4.3.5); vice has none, so it is empty. FONT's OUTAGE sets its font.
func (ep *Scope) drawOutageView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.OutageView.Visible {
		return
	}
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.OutageView.Position,
		ID:         "outage",
		Title:      "OUTAGE",
		Opaque:     true,
		ShowBorder: true,
		Brightness: ps.Brightness.Outage,
		OnMinimize: func() { ps.OutageView.Visible = false },
		RowSource: &ViewRowSource{
			FontIndex: ps.OutageSize,
			// As wide as its widest entry would be (Table 4-18).
			ContentChars:            len("ALTIMETER SETTINGS DOWN"),
			MaxCols:                 1,
			VisibleRows:             1,
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
			EmptyKeepsColumnWidth:   true,
		},
	})
}

///////////////////////////////////////////////////////////////////////////
// Time

func (ep *Scope) drawTimeView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	if ps.TimeView.Position == [2]float32{} {
		ps.TimeView.Position = [2]float32{10, ctx.DrawExtent.Height() - 300}
	}

	font := ep.ERAMFont(ps.TimeView.Font)
	textColor := ps.TimeView.Bright.ScaleRGB(colors.view.clockText)

	timeStr := ctx.Client.State.SimTime.Format("1504 05")

	ext := font.InkBounds(timeStr, 0)
	width := ext.Width() + 8
	height := ext.Height() + 8

	v := View{
		Position:   &ps.TimeView.Position,
		ID:         "clock",
		Width:      width,
		BodyHeight: height,
		ShowBorder: ps.TimeView.ShowBorder,
		Opaque:     ps.TimeView.Opaque,
		Brightness: ps.TimeView.Bright,
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

func (t *timeViewPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []MenuItem{
		ep.makeBooleanMenuItem(&ps.TimeView.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.TimeView.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.TimeView.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.TimeView.Bright, "BRIGHT", 0, 100, 2),
	}

	cfg := MenuConfig{
		Title: "TIME",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}
	ep.DrawERAMMenu(ctx, transforms, cb, t.origin, cfg)
}

///////////////////////////////////////////////////////////////////////////
// CHECK LISTS (POS CHECK / EMERG CHECK)

// checkList is one of the check lists, each of which is displayed in a
// view of its own (4.3.6, 4.3.7).
type checkList struct {
	viewID    string
	title     string
	menuTitle string
	items     []string
	prefs     func(*Preferences) *CheckListPreferences
	toggled   func(*Scope) map[string]bool
}

var posCheckList = checkList{
	viewID:    "pos-check",
	title:     "POS CHECK",
	menuTitle: "POS CHK",
	items: []string{
		"AIRSPACE STATUS",
		"NOTAMS",
		"AIRPORT CONDITIONS (RWY, ATIS, ETC)",
		"EQUIPMENT/FREQUENCIES (NAVAID STATUS)",
		"RESTRICTIONS/TMIS",
		"WEATHER, PIREPS, DEVIATIONS",
		"UNUSUAL SITUATIONS (PAJA, NON-RVSM, ETC)",
		"CLEARANCES ISSUED OR PENDING",
		"TRAFFIC",
	},
	prefs:   func(ps *Preferences) *CheckListPreferences { return &ps.PosCheck },
	toggled: func(ep *Scope) map[string]bool { return ep.PosCheckToggled },
}

var emergCheckList = checkList{
	viewID:    "emerg-check",
	title:     "EMERG CK",
	menuTitle: "EMRG CHK",
	items: []string{
		"AIRCRAFT ID AND TYPE",
		"NATURE OF EMERGENCY",
		"PILOT'S DESIRES",
		"AIRCRAFT ALTITUDE",
		"FUEL REMAINING IN TIME",
		"FLIGHT CONDITIONS/WEATHER",
		"LAST KNOWN POSITION",
		"NUMBER OF PEOPLE ON BOARD",
		"HAZMAT ON BOARD",
	},
	prefs:   func(ps *Preferences) *CheckListPreferences { return &ps.EmergCheck },
	toggled: func(ep *Scope) map[string]bool { return ep.EmergCheckToggled },
}

// drawCheckListView renders a check list's view if it is displayed. Rows
// are click-toggleable; the toggled state lives on Scope and persists while
// the view is hidden.
func (ep *Scope) drawCheckListView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer,
	cl *checkList) {
	cps := cl.prefs(ep.currentPrefs())
	if !cps.Visible {
		return
	}

	toggled := cl.toggled(ep)
	textColor := cps.Text.ScaleRGB(colors.view.text)
	rows := make([]Row, len(cl.items))
	maxLen := 0
	for i, s := range cl.items {
		rows[i] = Row{Body: s, Color: textColor, Toggled: toggled[s]}
		if len(s) > maxLen {
			maxLen = len(s)
		}
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &cps.Position,
		ID:         cl.viewID,
		Title:      cl.title,
		Opaque:     cps.Opaque,
		ShowBorder: cps.ShowBorder,
		Brightness: cps.Text,
		OnMenu: ep.makeViewMenu(ctx, cl.viewID, 6,
			func(pb popupBase) popup { return &checkListPopup{popupBase: pb, list: cl} }),
		OnMinimize: func() { cps.Visible = false },
		RowSource: &ViewRowSource{
			Rows:           rows,
			FontIndex:      cps.Font,
			ContentChars:   maxLen,
			MaxCols:        1,
			VisibleRows:    visibleLines(cps.Lines, len(rows)),
			RowSpacing:     RowSpacingCompact,
			HighlightColor: cps.Highlight.ScaleRGB(colors.popup.backgroundGrey),
			OnRowToggle:    func(idx int) { toggled[cl.items[idx]] = !toggled[cl.items[idx]] },
		},
	})
}

type checkListPopup struct {
	popupBase
	list *checkList
}

func (c *checkListPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	cps := c.list.prefs(ep.currentPrefs())

	rows := []MenuItem{
		ep.makeBooleanMenuItem(&cps.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&cps.ShowBorder, "BORDER"),
		makeLinesMenuItem(ep, &cps.Lines, 3),
		makeIntMenuItem(ep, &cps.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &cps.Highlight, "HIGHLIGHT", 0, 100, 2),
		makeIntMenuItem(ep, &cps.Text, "TEXT", 0, 100, 2),
	}

	cfg := MenuConfig{
		Title: c.list.menuTitle,
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, c.origin, cfg)
}

// eram/rangeview.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

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

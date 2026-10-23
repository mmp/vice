// eram/views_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

// A view's menu goes with the view: suppressing the view with its "-"
// button or with its toolbar button closes the menu.
func TestSuppressedViewClosesMenu(t *testing.T) {
	ep := makeRowTestScope()
	ctx := makeTrackTestHarness().ctx
	ctx.DrawExtent = math.Extent2D{P1: [2]float32{1600, 900}}
	ctx.Keyboard = &platform.KeyboardState{}
	ctx.Platform = warpingPlatform{}
	transforms := scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-73, 40}, 100, 0)
	ps := ep.currentPrefs()
	pos := [2]float32{200, 700}
	ps.BeaconCodeView.Visible, ps.BeaconCodeView.Position = true, pos

	var cb renderer.CommandBuffer
	frame := func(mouse *platform.MouseState) {
		ctx.Mouse = mouse
		ep.viewExtents = nil
		ep.drawViews(ctx, nil, transforms, &cb)
	}
	click := func(p [2]float32) {
		m := &platform.MouseState{Pos: p}
		m.Clicked[platform.MouseButtonPrimary] = true
		frame(m)
	}
	frame(nil)
	i := slices.IndexFunc(ep.viewExtents, func(e math.Extent2D) bool { return e.P0[0] == pos[0] && e.P1[1] == pos[1] })
	if i < 0 {
		t.Fatalf("CODE view not drawn at %v: %v", pos, ep.viewExtents)
	}
	outer := ep.viewExtents[i]
	titleH := viewTitleHeight(ep.ERAMFont(2))
	menuButton := [2]float32{outer.P0[0] + 4, outer.P1[1] - titleH/2}
	minimizeButton := [2]float32{outer.P1[0] - 4, outer.P1[1] - titleH/2}

	click(menuButton)
	if !ep.viewMenuDisplayed("beacon") {
		t.Fatalf("M click displayed %+v", ep.popup)
	}
	click(minimizeButton)
	frame(nil)
	if ps.BeaconCodeView.Visible || ep.popup != nil {
		t.Errorf("after '-': displayed %v, popup %+v; want neither", ps.BeaconCodeView.Visible, ep.popup)
	}

	ps.BeaconCodeView.Visible = true
	frame(nil)
	click(menuButton)
	if !ep.viewMenuDisplayed("beacon") {
		t.Fatalf("M click displayed %+v", ep.popup)
	}
	ep.toolbarButtonByID(toolbarButtonID(t, ep, "CODE")).click(ep, toolbarClick{})
	frame(nil)
	if ps.BeaconCodeView.Visible || ep.popup != nil {
		t.Errorf("after the CODE button: displayed %v, popup %+v; want neither", ps.BeaconCodeView.Visible, ep.popup)
	}
}

// -/+ and the trough step the range, stopping at the view's limits, and
// RETURN goes back and forth between the last two ranges.
func TestRangeViewSteps(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ps := ep.currentPrefs()
	ps.Range = 2 * 150

	check := func(what string, want float32) {
		t.Helper()
		if r := ep.viewRange(); r != want {
			t.Errorf("%s: range %v, want %v", what, r, want)
		}
	}

	if err := ep.returnRange(); err == nil {
		t.Error("RETURN with no previous range was accepted")
	}
	ep.nudgeRange(rangeViewStep)
	check("+25", 175)
	ep.nudgeRange(-rangeViewTroughStep)
	check("-10", 165)
	ep.returnRange()
	check("RETURN", 175)
	ep.returnRange()
	check("RETURN again", 165)

	ps.Range = 2 * 10
	ep.nudgeRange(-rangeViewStep)
	check("-25 from 10", minViewRange)
	if err := ep.nudgeRange(-rangeViewStep); err == nil {
		t.Error("-25 at the minimum range was accepted")
	}
	ps.Range = 2 * (maxViewRange - 5)
	ep.nudgeRange(rangeViewStep)
	check("+25 near the maximum", maxViewRange)
	if err := ep.nudgeRange(rangeViewStep); err == nil {
		t.Error("+25 at the maximum range was accepted")
	}
}

// TBE on a preset selects its range; TBP on one starts typed entry, except
// on the selected one (3.4).
func TestRangeViewPresets(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ps := ep.currentPrefs()
	ps.Range = 2 * 300
	ps.RangeView.Presets = [2]int{100, 150}

	ep.selectRangePreset(1)
	if r := ep.viewRange(); r != 150 || !ep.rangePresetSelected(1) || ep.rangePresetSelected(0) {
		t.Fatalf("selecting the second preset gave range %v", r)
	}
	if err := ep.startRangeEntry(rangeFieldPreset2); err == nil {
		t.Error("typed entry into the selected preset was allowed")
	}

	if err := ep.startRangeEntry(rangeFieldPreset1); err != nil {
		t.Fatalf("typed entry into the other preset: %v", err)
	}
	for _, bad := range []string{"", "0", "1301", "12A"} {
		ep.rangeView.buf = bad
		if ep.enterRange() {
			t.Errorf("entry %q was accepted", bad)
		}
	}
	ep.rangeView.buf = "75"
	if !ep.enterRange() || ps.RangeView.Presets[0] != 75 || ep.viewRange() != 150 {
		t.Errorf("entering 75 gave presets %v and range %v", ps.RangeView.Presets, ep.viewRange())
	}

	ep.startRangeEntry(rangeFieldValue)
	ep.rangeView.buf = "40"
	if !ep.enterRange() || ep.viewRange() != 40 {
		t.Errorf("entering range 40 gave %v", ep.viewRange())
	}
}

// The slider's position and the range it gives agree, and it stops at the
// ends of its trough.
func TestRangeViewSlider(t *testing.T) {
	l := rangeViewLayout{
		trough:      math.Extent2D{P0: [2]float32{100, 0}, P1: [2]float32{500, 20}},
		sliderWidth: 40,
	}
	for _, r := range []float32{minViewRange, 125, 650, maxViewRange} {
		if got := l.sliderRange(l.slider(r).Center()[0]); math.Abs(got-r) > 2 {
			t.Errorf("slider at range %v gives range %v", r, got)
		}
	}
	if s := l.slider(maxViewRange); s.P1[0] != l.trough.P1[0] {
		t.Errorf("slider at the maximum ends at %v, want the trough's end %v", s.P1[0], l.trough.P1[0])
	}
	if r := l.sliderRange(0); r != minViewRange {
		t.Errorf("slider left of the trough gives %v", r)
	}
	if r := l.sliderRange(1000); r != maxViewRange {
		t.Errorf("slider right of the trough gives %v", r)
	}
}

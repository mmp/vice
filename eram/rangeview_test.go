// eram/rangeview_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	"github.com/mmp/vice/math"
)

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

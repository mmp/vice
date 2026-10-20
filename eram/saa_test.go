// eram/saa_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

// The SAAs of an ARTCC are the special use airspace near it, the active
// ones first; the CIFP marks P-56 active continuously, so it is hot.
func TestSAAs(t *testing.T) {
	db.InitDB()
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()}}
	var ss client.SimState
	ss.Facility = "ZDC"
	ep.makeSAAs(ss)

	i := slices.IndexFunc(ep.saas, func(a saa) bool { return a.label == "P-56" })
	if i < 0 {
		t.Fatalf("no P-56 among ZDC's %d SAAs", len(ep.saas))
	}
	if p56 := ep.saas[i]; !p56.hot || len(p56.altitudes) == 0 {
		t.Errorf("P-56: hot %v, altitudes %v", p56.hot, p56.altitudes)
	}
	if slices.ContainsFunc(ep.saas, func(a saa) bool { return a.label == "FALLON SOUTH 1 MOA" }) {
		t.Error("a Nevada MOA is among ZDC's SAAs")
	}
	if first := slices.IndexFunc(ep.saas, func(a saa) bool { return !a.hot }); first >= 0 &&
		slices.ContainsFunc(ep.saas[first:], func(a saa) bool { return a.hot }) {
		t.Error("an active SAA follows an inactive one")
	}
}

// Inactive SAAs are displayed per their COLD filter setting and active ones
// per their HOT one, except that DISPLAY shows the boundary and altitudes of
// an active one whose HOT setting is blank (4.2.18.1, 4.2.18.2).
func TestSAAFilter(t *testing.T) {
	hot, cold := &saa{hot: true}, &saa{}
	for _, c := range []struct {
		a    *saa
		s    SAASettings
		want string
	}{
		{cold, defaultSAASettings, ""},
		{hot, defaultSAASettings, "BAL"},
		{cold, SAASettings{Cold: "BL", Hot: "B"}, "BL"},
		{hot, SAASettings{Display: true}, "BA"},
		{hot, SAASettings{Hot: "B", Display: true}, "B"},
	} {
		if got := c.a.filter(c.s); got != c.want {
			t.Errorf("hot %v with %+v: %q, want %q", c.a.hot, c.s, got, c.want)
		}
	}
	for f, next := range map[string]string{"": "B", "B": "BL", "BL": ""} {
		if got := nextSAAFilter(saaColdFilters, f); got != next {
			t.Errorf("COLD after %q: %q, want %q", f, got, next)
		}
	}
	if got := nextSAAFilter(saaHotFilters, "BAL"); got != "" {
		t.Errorf("HOT after BAL: %q, want blank", got)
	}
}

// warpingPlatform stands in for the platform, which pop-ups warp the mouse
// with.
type warpingPlatform struct{ captureMousePlatform }

func (warpingPlatform) SetMousePosition([2]float32) {}

// A click places a label being torn off; a click on a torn-off label offers
// SUPPRESS LABEL/ALT, which leaves the SAA's boundary alone displayed, and
// with DELETE TEAROFF active one deletes the tear-off (4.2.18.3).
func TestSAATearOff(t *testing.T) {
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()},
		saas: []saa{{id: "P56", label: "P-56", hot: true}}}
	ep.systemFont[1] = &renderer.Font{Size: 13}
	ctx := &scope.Context{DrawExtent: math.Extent2D{P1: [2]float32{1600, 900}}, Platform: warpingPlatform{}}
	transforms := scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-77, 39}, 100, 0)
	ps := ep.currentPrefs()
	click := func(p [2]float32, button imgui.MouseButton) {
		ctx.Mouse = &platform.MouseState{Pos: p}
		ctx.Mouse.Clicked[button] = true
		ep.handleSAAMouse(ctx, transforms)
	}

	ep.saaTearingOff = "P56"
	click([2]float32{800, 450}, platform.MouseButtonPrimary)
	if s := ps.saaSettings("P56"); ep.saaTearingOff != "" || s.TearOff.IsZero() || s.Hot != "BAL" {
		t.Fatalf("after placing the tear-off: tearing off %q, settings %+v", ep.saaTearingOff, s)
	}

	ep.saaTearOffs = map[string]math.Extent2D{"P56": {P0: [2]float32{800, 440}, P1: [2]float32{850, 450}}}
	click([2]float32{810, 445}, platform.MouseButtonPrimary)
	suppress, ok := ep.popup.(*pickAreaPopup)
	if !ok || suppress.text != "SUPPRESS LABEL/ALT" {
		t.Fatalf("click on the torn-off label opened %+v", ep.popup)
	}
	suppress.onPick()
	if s := ps.saaSettings("P56"); s.Hot != "B" || s.TearOff.IsZero() {
		t.Errorf("after SUPPRESS LABEL/ALT: %+v, want the boundary alone, still torn off", s)
	}

	ep.toolbar.deleting = true
	click([2]float32{810, 445}, platform.MouseButtonTertiary)
	if s := ps.saaSettings("P56"); !s.TearOff.IsZero() || ep.toolbar.deleting {
		t.Errorf("after a TBE on it with DELETE TEAROFF active: %+v, DELETE TEAROFF active %v", s, ep.toolbar.deleting)
	}
}

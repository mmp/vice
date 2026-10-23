// eram/saa_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/util"
)

// The SAAs of an ARTCC are the special use airspace near it, the active
// ones first; the CIFP marks P-56 active continuously, so it is hot.
func TestSAAs(t *testing.T) {
	db.InitDB()
	ep := &Scope{prefSet: &PreferenceSet{Current: *makeDefaultPreferences()}}
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
// an active one whose HOT setting is blank (4.2.18.1, 4.2.18.2). An active
// SAA below the user's airspace is not displayed unless DISPLAY is on
// (4.2.18).
func TestSAAFilter(t *testing.T) {
	volume := func(floor, ceiling int) []db.SpecialUseAirspaceVolume {
		return []db.SpecialUseAirspaceVolume{{AirspaceVolume: av.AirspaceVolume{Floor: floor, Ceiling: ceiling}}}
	}
	hot, cold := &saa{hot: true, volumes: volume(0, 17999)}, &saa{volumes: volume(0, 17999)}
	lowHot, lowCold := &saa{hot: true, volumes: volume(0, 5000)}, &saa{volumes: volume(0, 5000)}
	for _, c := range []struct {
		what string
		a    *saa
		s    SAASettings
		want string
	}{
		{"cold", cold, defaultSAASettings, ""},
		{"hot", hot, defaultSAASettings, "BAL"},
		{"cold", cold, SAASettings{Cold: "BL", Hot: "B"}, "BL"},
		{"hot", hot, SAASettings{Display: true}, "BA"},
		{"hot", hot, SAASettings{Hot: "B", Display: true}, "B"},
		{"low hot", lowHot, defaultSAASettings, ""},
		{"low hot", lowHot, SAASettings{Hot: "BAL", Display: true}, "BAL"},
		{"low hot", lowHot, SAASettings{Display: true}, "BA"},
		{"low cold", lowCold, SAASettings{Cold: "B"}, "B"},
		{"hot to the floor", &saa{hot: true, volumes: volume(0, 10000)}, defaultSAASettings, ""},
		{"hot from the ceiling", &saa{hot: true, volumes: volume(60000, 99900)}, defaultSAASettings, ""},
	} {
		if got := c.a.filter(c.s, [2]int{10000, 60000}); got != c.want {
			t.Errorf("%s with %+v: %q, want %q", c.what, c.s, got, c.want)
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

// A click places a label being torn off, which shows the label and
// altitudes in the SAA's color and takes the label's place on the map
// (4.2.18.3). A click on the torn-off label offers SUPPRESS LABEL/ALT,
// which leaves the SAA's boundary alone displayed and the tear-off its bar
// alone; its bar moves it, and with DELETE TEAROFF active a TBE on it
// deletes it (4.2.18.3.1, 4.2.18.3.2).
func TestSAATearOff(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ep, ps := h.ep, h.ep.currentPrefs()
	ep.saas = []saa{{id: "P56", label: "P-56", hot: true, altitudes: []string{"000B180"}}}
	ps.setSAASettings("P56", SAASettings{Hot: "BAL", Display: true})
	p56 := viewEntry{&saaTearOffs, "P56"}

	h.ctx.Mouse = &platform.MouseState{Pos: [2]float32{100, 500}}
	ep.startEntryDrag(h.ctx, p56, h.ctx.Mouse.Pos)
	if id, ok := ep.saaBeingTornOff(); !ok || id != "P56" {
		t.Fatalf("tearing off %q, %v", id, ok)
	}
	drop := [2]float32{800, 450}
	h.click(drop, platform.MouseButtonPrimary)
	if _, ok := ps.SAAFilter.TearOffs["P56"]; !ok || ep.entryDrag != nil {
		t.Fatalf("after a click away: torn off %v, still dragging %v", ps.SAAFilter.TearOffs, ep.entryDrag != nil)
	}
	h.frame(nil)
	pt, ok := h.placed(p56)
	if !ok {
		t.Fatal("no tear-off of P-56's label")
	}
	labels := func(pt placedTearOff) []string {
		return util.MapSlice(pt.rows.Rows, func(r Row) string { return r.Label })
	}
	if got, want := labels(pt), []string{"P-56", "000B180"}; !slices.Equal(got, want) || pt.border ||
		pt.rows.Rows[0].Color != ep.saaColor(&ep.saas[0]) {
		t.Errorf("tear-off %q, border %v, color %v; want %q, no border, the SAA's color %v",
			got, pt.border, pt.rows.Rows[0].Color, want, ep.saaColor(&ep.saas[0]))
	}

	h.click([2]float32{pt.bar.P1[0] + 10, pt.extent.Center()[1]}, platform.MouseButtonPrimary)
	suppress, ok := ep.popup.(*pickAreaPopup)
	if !ok || suppress.text != "SUPPRESS LABEL/ALT" {
		t.Fatalf("click on the torn-off label opened %+v", ep.popup)
	}
	suppress.onPick()
	ep.popup = nil
	h.frame(nil)
	pt, _ = h.placed(p56)
	if s := ps.saaSettings("P56"); s.Hot != "B" || !p56.tornOff(ep) || !slices.Equal(labels(pt), []string{""}) {
		t.Errorf("after SUPPRESS LABEL/ALT: settings %+v, torn off %v, tear-off %q; want the boundary alone, still torn off, the bar alone",
			s, p56.tornOff(ep), labels(pt))
	}

	moved := [2]float32{500, 200}
	h.drag(pt.bar, moved)
	h.frame(nil)
	if pt, ok := h.placed(p56); !ok || !near(pt.bar.Center(), moved) {
		t.Errorf("moved tear-off %+v; want its bar at %v", pt, moved)
	}

	ep.toggleDeleteTearoffs()
	pt, _ = h.placed(p56)
	h.click(pt.extent.Center(), platform.MouseButtonTertiary)
	if p56.tornOff(ep) || ep.toolbar.deleting {
		t.Errorf("after a TBE on it with DELETE TEAROFF active: torn off %v, DELETE TEAROFF active %v",
			p56.tornOff(ep), ep.toolbar.deleting)
	}
}

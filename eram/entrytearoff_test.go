// eram/entrytearoff_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

type tearOffTestHarness struct {
	t          *testing.T
	ep         *Scope
	ctx        *scope.Context
	transforms scope.Transformations
}

func makeTearOffTestHarness(t *testing.T) *tearOffTestHarness {
	ep := makeRowTestScope()
	ctx := makeTrackTestHarness().ctx
	ctx.DrawExtent = math.Extent2D{P1: [2]float32{1600, 900}}
	ctx.Keyboard = &platform.KeyboardState{}
	ctx.Platform = warpingPlatform{}
	ps := ep.currentPrefs()
	ps.AltimSet.Visible, ps.AltimSet.Position = true, [2]float32{200, 700}
	ps.WX.Visible, ps.WX.Position = true, [2]float32{600, 700}
	ps.DeptList.Visible, ps.DeptList.Position = true, [2]float32{1000, 700}
	return &tearOffTestHarness{t: t, ep: ep, ctx: ctx,
		transforms: scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-73, 40}, 100, 0)}
}

// frame runs the parts of Scope.Draw that tear-offs take part in, in its
// order, with the given mouse state.
func (h *tearOffTestHarness) frame(mouse *platform.MouseState) {
	ep, ctx := h.ep, h.ctx
	ctx.Mouse = mouse
	var cb renderer.CommandBuffer
	ep.placeEntryDrag(ctx, h.transforms)
	ep.handleToolbarInput(ctx)
	ep.drawEntryTearOffs(ctx, h.transforms, &cb)
	ep.drawAltimSetView(ctx, h.transforms, &cb)
	ep.drawWXView(ctx, h.transforms, &cb)
	ep.drawDeptListView(ctx, h.transforms, &cb)
	if ctx.Mouse != nil {
		ep.handleEntryTearOffMouse(ctx)
	}
}

// click presses and releases the given logical mouse button at p.
func (h *tearOffTestHarness) click(p [2]float32, button imgui.MouseButton) {
	m := &platform.MouseState{Pos: p}
	m.Clicked[button] = true
	h.frame(m)
	m = &platform.MouseState{Pos: p}
	m.Released[button] = true
	h.frame(m)
}

// viewBar returns where the tear-off bar of a view's first entry is: a
// tear-off with its top left at the top left of the view's body lies over
// that entry.
func (h *tearOffTestHarness) viewBar(e viewEntry, viewPos [2]float32) math.Extent2D {
	h.t.Helper()
	top := viewPos[1] - viewTitleHeight(h.ep.ERAMFont(2))
	pt, ok := h.ep.layoutEntryTearOff(h.ctx, e, [2]float32{viewPos[0], top})
	if !ok {
		h.t.Fatalf("%s has no tear-off", e.id)
	}
	return pt.bar
}

// placed returns e's tear-off as drawn in the last frame.
func (h *tearOffTestHarness) placed(e viewEntry) (placedTearOff, bool) {
	i := slices.IndexFunc(h.ep.placedTearOffs, func(pt placedTearOff) bool { return pt.entry == e })
	if i < 0 {
		return placedTearOff{}, false
	}
	return h.ep.placedTearOffs[i], true
}

// drag picks the bar at bar and drops what it tears off or moves with the
// mouse at p.
func (h *tearOffTestHarness) drag(bar math.Extent2D, p [2]float32) {
	h.click(bar.Center(), platform.MouseButtonPrimary)
	h.click(p, platform.MouseButtonPrimary)
}

// A pick on an ALTIM SET entry's gold bar tears a copy off, which a click
// places on the scope, anchored to map coordinates; the bar then turns
// gray and can't tear off another. The copy is displayed while the view
// is suppressed, its bar moves it, and it goes with its entry (2.5.2).
func TestAltimSetTearOff(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ep := h.ep
	ps := ep.currentPrefs()
	ep.AltimSetAirports = []av.ICAOAirportCode{"KJFK", "KBOS"}
	jfk := viewEntry{&altimSetTearOffs, "KJFK"}
	h.frame(nil)
	bar := h.viewBar(jfk, ps.AltimSet.Position)

	h.click(bar.Center(), platform.MouseButtonPrimary)
	if d := ep.entryDrag; d == nil || d.entry != jfk {
		t.Fatalf("a pick on JFK's bar started %+v", ep.entryDrag)
	}
	drop := [2]float32{800, 300}
	h.click(drop, platform.MouseButtonPrimary)
	loc, ok := ps.AltimSet.TearOffs["KJFK"]
	if !ok || ep.entryDrag != nil {
		t.Fatalf("after a click away: torn off %v, still dragging %v", ps.AltimSet.TearOffs, ep.entryDrag != nil)
	}
	h.frame(nil)
	pt, ok := h.placed(jfk)
	if !ok || !near(pt.bar.Center(), drop) ||
		!near([2]float32{pt.extent.P0[0], pt.extent.P1[1]}, h.transforms.WindowFromLatLongP(loc)) {
		t.Fatalf("tear-off %+v anchored at %v; want its bar where it was dropped, %v", pt, loc, drop)
	}
	gold := ps.AltimSet.Bright.ScaleRGB(colors.badge.fill)
	if len(pt.rows.Rows) != 1 || pt.rows.Rows[0].Badge.Fill != gold {
		t.Errorf("tear-off rows %+v; want JFK's with a gold bar", pt.rows.Rows)
	}

	// The map moves the tear-off with it.
	ps.CurrentCenter = math.Point2LL{-72.5, 40}
	h.transforms = scope.GetTransformations(h.ctx.DrawExtent, 45, ps.CurrentCenter, 100, 0)
	h.frame(nil)
	if pt, _ := h.placed(jfk); near(pt.bar.Center(), drop) {
		t.Error("the tear-off stayed put when the map moved")
	}

	// The bar in the view is gray and doesn't tear off another copy.
	h.click(bar.Center(), platform.MouseButtonPrimary)
	if ep.entryDrag != nil {
		t.Error("a pick on the gray bar started a tear-off")
	}

	// The tear-off stays while the view is suppressed, and its bar moves
	// it.
	ps.AltimSet.Visible = false
	h.frame(nil)
	pt, ok = h.placed(jfk)
	if !ok {
		t.Fatal("the tear-off isn't displayed while the view is suppressed")
	}
	moved := [2]float32{500, 200}
	h.drag(pt.bar, moved)
	h.frame(nil)
	if pt, ok := h.placed(jfk); !ok || !near(pt.bar.Center(), moved) {
		t.Errorf("moved tear-off %+v; want its bar at %v", pt, moved)
	}

	ep.removeAltimSetEntry(0)
	if _, ok := ps.AltimSet.TearOffs["KJFK"]; ok {
		t.Error("JFK's tear-off outlived its entry")
	}
}

// A torn-off WX REPORT entry shows the whole entry, however many lines it
// wraps to, and goes with its entry (4.2.20.2). With TEAROFF off, the view
// has no bars to tear entries off with, but its tear-offs stay (2.5.3).
func TestWXReportTearOff(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ep := h.ep
	ps := ep.currentPrefs()
	ps.WX.Lines = 3
	ep.WXReportStations = []av.ICAOAirportCode{"KJFK", "KBOS"}
	h.ctx.Client.State.METAR = map[av.ICAOAirportCode]wx.METAR{
		"KJFK": {Raw: "KJFK 221651Z 18012KT 10SM FEW050 SCT250 22/14 A3002 RMK AO2 SLP165 T02220144"},
	}
	jfk := viewEntry{&wxTearOffs, "KJFK"}
	h.frame(nil)
	bar := h.viewBar(jfk, ps.WX.Position)

	ps.WX.ShowIndicators = false
	h.click(bar.Center(), platform.MouseButtonPrimary)
	if ep.entryDrag != nil {
		t.Fatal("a pick where the bar would be started a tear-off with TEAROFF off")
	}
	ps.WX.ShowIndicators = true
	h.drag(bar, [2]float32{800, 300})
	ps.WX.ShowIndicators = false
	h.frame(nil)
	pt, ok := h.placed(jfk)
	if !ok {
		t.Fatal("no tear-off with TEAROFF off")
	}
	if lines := pt.rows.measured[0].lines; len(lines) <= ps.WX.Lines || !slices.Equal(lines, pt.rows.wrap(pt.rows.Rows[0])) {
		t.Errorf("tear-off lines %q; want all of the observation's, more than the view's %d", lines, ps.WX.Lines)
	}

	ep.removeWXReportStation(0)
	h.frame(nil)
	if _, ok := h.placed(jfk); ok || len(ps.WX.TearOffs) != 0 {
		t.Errorf("JFK's tear-off outlived its entry: %v", ps.WX.TearOffs)
	}
}

// A DEPT LIST sublist torn off by its heading's bar shows its flights under
// its name and the view's title (figure 2-19); the pick doesn't suppress
// the sublist, as one on the heading does. It stays on the scope when its
// airport has no proposed departures left.
func TestDeptListTearOff(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ps := h.ep.currentPrefs()
	name := db.AirportDisplayId("KJFK")
	departure := func(acid sim.ACID, altitude int) *sim.FlightPlan {
		return &sim.FlightPlan{ACID: acid, DepartureAirport: "KJFK", TypeOfFlight: av.FlightTypeDeparture,
			TrackingController: "1A", RequestedAltitude: altitude}
	}
	h.ctx.Client.State.UnassociatedFlightPlans = []*sim.FlightPlan{departure("SWA123", 15000), departure("AAL321", 6000)}
	jfk := viewEntry{&deptListTearOffs, name}
	h.frame(nil)

	h.drag(h.viewBar(jfk, ps.DeptList.Position), [2]float32{800, 300})
	if len(ps.DeptList.SuppressedSublists) != 0 {
		t.Errorf("tearing off suppressed %v", ps.DeptList.SuppressedSublists)
	}
	labels := func() []string {
		t.Helper()
		h.frame(nil)
		pt, ok := h.placed(jfk)
		if !ok {
			t.Fatal("no tear-off of JFK's sublist")
		}
		heading := pt.rows.Rows[0]
		x, _ := pt.rows.rowContentLayout(heading, pt.extent)
		if heading.Centered || x < pt.bar.P1[0] || x+float32(len(heading.Label))*charWidth(pt.rows.Font) > pt.extent.P1[0] {
			t.Errorf("heading from %v to %v, bar %v, tear-off %v; want it after the bar and inside",
				x, x+float32(len(heading.Label))*charWidth(pt.rows.Font), pt.bar, pt.extent)
		}
		return util.MapSlice(pt.rows.Rows, func(r Row) string { return r.Label })
	}
	if got, want := labels(), []string{name + " DEPT", "AAL321   60", "SWA123  150"}; !slices.Equal(got, want) {
		t.Errorf("tear-off %q, want %q", got, want)
	}
	h.ctx.Client.State.UnassociatedFlightPlans = nil
	if got, want := labels(), []string{name + " DEPT"}; !slices.Equal(got, want) {
		t.Errorf("tear-off with no departures %q, want %q", got, want)
	}
}

// With DELETE TEAROFF active, TBPs on the gold bars of tear-offs pick
// them, view entries and toolbar buttons alike, and a TBE on a tear-off
// deletes it along with those picked. Picked entries are highlighted
// (2.5.2.1, 2.5.2.2).
func TestDeleteEntryTearOffs(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ep := h.ep
	ps := ep.currentPrefs()
	ep.AltimSetAirports = []av.ICAOAirportCode{"KJFK", "KBOS", "KLGA"}
	jfk, bos, lga := viewEntry{&altimSetTearOffs, "KJFK"}, viewEntry{&altimSetTearOffs, "KBOS"},
		viewEntry{&altimSetTearOffs, "KLGA"}
	for i, e := range []viewEntry{jfk, bos, lga} {
		e.place(ep, h.transforms.LatLongFromWindowP([2]float32{800, 400 - 50*float32(i)}))
	}
	font := toolbarButtonID(t, ep, "FONT")
	ps.TornOffButtons = map[string][2]float32{font: {100, 600}}
	h.frame(nil)
	at := func(e viewEntry) placedTearOff {
		t.Helper()
		pt, ok := h.placed(e)
		if !ok {
			t.Fatalf("no tear-off of %s", e.id)
		}
		return pt
	}
	highlighted := func(e viewEntry) bool {
		return util.SeqContainsAllFunc(slices.Values(at(e).rows.Rows), func(r Row) bool { return r.Selected })
	}
	jfkPT, bosPT := at(jfk), at(bos)

	ep.toggleDeleteTearoffs()
	h.click(jfkPT.extent.Center(), platform.MouseButtonPrimary)
	if len(ep.toolbar.selectedEntries) != 0 {
		t.Errorf("a TBP away from JFK's bar picked it: %v", ep.toolbar.selectedEntries)
	}
	h.click(jfkPT.bar.Center(), platform.MouseButtonPrimary)
	h.click(jfkPT.bar.Center(), platform.MouseButtonPrimary)
	if len(ep.toolbar.selectedEntries) != 0 {
		t.Errorf("JFK picked twice is picked: %v", ep.toolbar.selectedEntries)
	}
	h.click(jfkPT.bar.Center(), platform.MouseButtonPrimary)
	h.frame(nil)
	if !highlighted(jfk) || highlighted(lga) {
		t.Errorf("picked JFK highlighted %v, unpicked LGA highlighted %v", highlighted(jfk), highlighted(lga))
	}
	h.click(tornOffButton(t, ep, "FONT").face.Center(), platform.MouseButtonPrimary)
	h.click(bosPT.extent.Center(), platform.MouseButtonTertiary)
	if ep.toolbar.deleting || !slices.Equal(util.SortedMapKeys(ps.AltimSet.TearOffs), []string{"KLGA"}) ||
		len(ps.TornOffButtons) != 0 {
		t.Errorf("after a TBE on BOS: deleting %v, entry tear-offs %v, buttons %v; want LGA's alone",
			ep.toolbar.deleting, ps.AltimSet.TearOffs, ps.TornOffButtons)
	}
}

// An entry can't be placed on a toolbar; the drop leaves it being torn
// off, which Escape cancels (2.5.2).
func TestEntryTearOffNotOnToolbar(t *testing.T) {
	h := makeTearOffTestHarness(t)
	ep := h.ep
	ep.AltimSetAirports = []av.ICAOAirportCode{"KJFK"}
	jfk := viewEntry{&altimSetTearOffs, "KJFK"}
	h.frame(nil)
	toolbar, ok := ep.toolbar.layout.regions[masterDock]
	if !ok {
		t.Fatal("no master toolbar")
	}

	h.drag(h.viewBar(jfk, ep.currentPrefs().AltimSet.Position), toolbar.Center())
	if ep.entryDrag == nil || jfk.tornOff(ep) {
		t.Errorf("dropped on the toolbar: dragging %v, torn off %v", ep.entryDrag != nil, jfk.tornOff(ep))
	}
	if !ep.cancelToolbarGesture(h.ctx) || ep.entryDrag != nil || jfk.tornOff(ep) {
		t.Error("Escape didn't cancel the tear-off")
	}
}

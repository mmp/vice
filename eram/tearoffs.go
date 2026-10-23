// eram/tearoffs.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// Buttons and view entries torn off onto the scope (2.5): dragging them off
// and placing them, and the DELETE TEAROFF function that removes them
// (2.5.2).

///////////////////////////////////////////////////////////////////////////
// Toolbar buttons

// startToolbarDrag starts tearing a button off by its tear-off bar, or
// moving a torn-off button or the toolbar control button by theirs. There
// is only one tear-off of each button; the bar of one already torn off is
// disabled.
func (ep *Scope) startToolbarDrag(ctx *scope.Context, pb *placedButton) {
	id := pb.button.id
	if !pb.handle && ep.isTornOff(id) {
		return
	}
	origin := [2]float32{pb.tab.P0[0], pb.tab.P1[1]}
	ep.toolbar.drag = &toolbarDrag{id: id, origin: origin, offset: math.Sub2f(ctx.Mouse.Pos, origin)}
	ctx.Platform.StartCaptureMouse(ctx.DrawExtent)
}

// isTornOff reports whether a button has been torn off, onto the scope or
// a toolbar.
func (ep *Scope) isTornOff(id string) bool {
	ps := ep.currentPrefs()
	if _, torn := ps.TornOffButtons[id]; torn {
		return true
	}
	for _, ids := range ps.DockedButtons {
		if slices.Contains(ids, id) {
			return true
		}
	}
	return false
}

// removeTearoff deletes a button's tear-off, wherever it is.
func (ep *Scope) removeTearoff(id string) {
	ps := ep.currentPrefs()
	delete(ps.TornOffButtons, id)
	for dock, ids := range ps.DockedButtons {
		ps.DockedButtons[dock] = slices.DeleteFunc(ids, func(d string) bool { return d == id })
	}
}

// inMainToolbar reports whether a button is one of the main toolbar's own.
func inMainToolbar(id string) bool {
	return slices.ContainsFunc(mainToolbar.rows, func(row []toolbarButton) bool {
		return slices.ContainsFunc(row, func(b toolbarButton) bool { return b.id == id })
	})
}

// dockSlot returns the toolbar a button dropped at p goes in, and the slot
// it takes: the one it is dropped on, or the one past the toolbar's last
// button if that comes first. It returns false if the button doesn't go
// in a toolbar: one of the master toolbar's own buttons goes back to its
// place when dropped on it, and a full toolbar takes no more.
func (ep *Scope) dockSlot(ctx *scope.Context, id string, p [2]float32) (string, int, bool) {
	ps := ep.currentPrefs()
	for _, name := range allToolbars {
		if region, ok := ep.toolbar.layout.regions[name]; !ok || !region.Inside(p) {
			continue
		}
		if name == masterDock && inMainToolbar(id) {
			return "", 0, false
		}
		slots := ep.toolbarSlots(ctx, name)
		docked := util.FilterSlice(ps.DockedButtons[name], func(d string) bool { return d != id })
		if len(docked) >= len(slots) {
			return "", 0, false
		}
		nearest := 0
		for i, s := range slots {
			if math.Distance2f(s.Center(), p) < math.Distance2f(slots[nearest].Center(), p) {
				nearest = i
			}
		}
		return name, min(nearest, len(docked)), true
	}
	return "", 0, false
}

// placeToolbarDrag places the button being torn off or moved. The drag
// stays armed until the button has been moved off where it started, so
// tearing a button off and moving one already torn off are the same
// gesture: click, move, click to place. The click that places it must not
// also press whatever it lands on. A button that has only one instance
// goes back to its place in the main toolbar when it is dropped there.
func (ep *Scope) placeToolbarDrag(ctx *scope.Context) {
	mouse := ctx.Mouse
	ep.consumeMouseClick(mouse)

	d := ep.toolbar.drag
	pos := math.Sub2f(mouse.Pos, d.offset)
	if math.Distance2f(pos, d.origin) < tearoffMoveThreshold {
		return
	}
	if !ep.mousePrimaryReleased(mouse) && !ep.mouseTertiaryReleased(mouse) {
		return
	}

	ps := ep.currentPrefs()
	dock, slot, docks := ep.dockSlot(ctx, d.id, mouse.Pos)
	switch {
	case d.id == toolbarControlID:
		ps.MasterToolbarPosition = pos
	case docks:
		ep.removeTearoff(d.id)
		if ps.DockedButtons == nil {
			ps.DockedButtons = make(map[string][]string)
		}
		ps.DockedButtons[dock] = slices.Insert(ps.DockedButtons[dock], slot, d.id)
	case inMainToolbar(d.id) && ep.toolbar.layout.regions[masterDock].Inside(mouse.Pos):
		ep.removeTearoff(d.id)
	default:
		ep.removeTearoff(d.id)
		if ps.TornOffButtons == nil {
			ps.TornOffButtons = make(map[string][2]float32)
		}
		ps.TornOffButtons[d.id] = pos
	}
	ep.toolbar.drag = nil
	ctx.Platform.EndCaptureMouse()
}

// cancelToolbarGesture cancels a button or view entry being torn off or
// moved, the DELETE TEAROFF function, and an override held by a
// press-and-hold button, reporting whether any was under way.
func (ep *Scope) cancelToolbarGesture(ctx *scope.Context) bool {
	tb := &ep.toolbar
	if tb.press.down && tb.press.override != noDBOverride {
		ep.endToolbarPress(ctx)
		return true
	}
	if tb.drag == nil && ep.entryDrag == nil && !tb.deleting {
		return false
	}
	if tb.drag != nil || ep.entryDrag != nil {
		tb.drag, ep.entryDrag = nil, nil
		ctx.Platform.EndCaptureMouse()
	}
	if tb.deleting {
		ep.endDeleteTearoffs()
	}
	return true
}

// toggleDeleteTearoffs starts the DELETE TEAROFF function, or ends it if
// it is active. While it is active, a left click picks or unpicks a
// tear-off and a middle click deletes a tear-off along with the picked
// ones (2.5.1.2, 2.5.2.2).
func (ep *Scope) toggleDeleteTearoffs() {
	tb := &ep.toolbar
	if tb.deleting {
		ep.endDeleteTearoffs()
		return
	}
	tb.deleting = true
	tb.selected = make(map[string]struct{})
	tb.selectedEntries = make(map[viewEntry]struct{})
	ep.SetTemporaryCursor("EramDeletion", -1, "")
}

func (ep *Scope) endDeleteTearoffs() {
	tb := &ep.toolbar
	tb.deleting = false
	tb.selected = nil
	tb.selectedEntries = nil
	ep.ClearTemporaryCursor()
}

// deleteSelectedTearoffs deletes the tear-offs picked for deletion and
// ends the DELETE TEAROFF function.
func (ep *Scope) deleteSelectedTearoffs() {
	tb := &ep.toolbar
	for id := range tb.selected {
		ep.removeTearoff(id)
	}
	for e := range tb.selectedEntries {
		e.removeTearOff(ep)
	}
	ep.endDeleteTearoffs()
}

// clickWhileDeletingTearoffs handles a click while DELETE TEAROFF is
// active, reporting whether it took the click. Only clicks on torn-off
// copies are taken; a button that has been moved rather than copied can't
// be deleted.
func (ep *Scope) clickWhileDeletingTearoffs(ctx *scope.Context, hit toolbarHit, c toolbarClick) bool {
	pb := hit.button
	if pb == nil || !pb.handle || pb.instance.id == toolbarControlID || pb.button.moveOnly {
		return false
	}
	tb := &ep.toolbar
	id := pb.button.id
	if c.clicked[platform.MouseButtonTertiary] {
		ep.removeTearoff(id)
		ep.deleteSelectedTearoffs()
		ep.syncToolbarInstances()
		ep.layoutToolbars(ctx)
		tb.hover = toolbarHit{}
		return true
	}
	if _, ok := tb.selected[id]; ok {
		delete(tb.selected, id)
	} else {
		tb.selected[id] = struct{}{}
	}
	return true
}

// drawToolbarDragPreview draws the outline of the button being torn off or
// moved where it would be placed. Over a toolbar that would take it, a
// green grid shows the toolbar's slots that it could go in, the one it
// would go in brightest (2.5.1.1).
func (ep *Scope) drawToolbarDragPreview(ctx *scope.Context, cb *renderer.CommandBuffer) {
	d := ep.toolbar.drag
	if d == nil || ctx.Mouse == nil {
		return
	}
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	if dock, slot, ok := ep.dockSlot(ctx, d.id, ctx.Mouse.Pos); ok {
		docked := util.FilterSlice(ep.currentPrefs().DockedButtons[dock], func(id string) bool { return id != d.id })
		for i, s := range ep.toolbarSlots(ctx, dock)[:len(docked)+1] {
			ld.AddLineLoop(util.Select(i == slot, colors.toolbar.dockTarget, colors.toolbar.dockGrid), s.Corners())
		}
	}

	p0 := math.Sub2f(ctx.Mouse.Pos, d.offset)
	w := toolbarTabWidth + toolbarTabGap + toolbarFaceWidth
	ld.AddLineLoop(colors.toolbar.hoveredOutline, [][2]float32{p0, {p0[0] + w, p0[1]},
		{p0[0] + w, p0[1] - toolbarFaceHeight}, {p0[0], p0[1] - toolbarFaceHeight}})
	ld.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// View entries

// The entries of the ALTIM SET and WX REPORT views and the sublists of the
// DEPT LIST view can be torn off and placed on the scope, anchored to map
// coordinates (2.5.2). A tear-off shows its entry as the view does, kept
// up to date, but semi-transparent, and its gold bar moves it. It is
// displayed whether or not its view is and whether or not the view shows
// its tear-off bars. An entry has one tear-off at most; its bar in the
// view is gray while it has one.

// entryTearOffs are the tear-offs of a view's entries.
type entryTearOffs struct {
	// locations returns where the entries have been torn off to, by ID.
	locations func(*Scope) *map[string]math.Point2LL
	// tearOff returns what the tear-off of the entry with the given ID
	// shows, or false if the view no longer has the entry.
	tearOff func(*Scope, *scope.Context, string) (entryTearOff, bool)
}

var allEntryTearOffs = []*entryTearOffs{&altimSetTearOffs, &wxTearOffs, &deptListTearOffs}

// entryTearOff is what a tear-off shows: its entry's rows, laid out as its
// view lays out a column, with its view's brightness and border.
type entryTearOff struct {
	ViewRowSource
	brightness scope.Brightness
	border     bool
}

// viewEntry is a list entry or sublist that can be torn off.
type viewEntry struct {
	tearOffs *entryTearOffs
	id       string
}

func (e viewEntry) tornOff(ep *Scope) bool {
	_, ok := (*e.tearOffs.locations(ep))[e.id]
	return ok
}

func (e viewEntry) place(ep *Scope, p math.Point2LL) {
	locations := e.tearOffs.locations(ep)
	if *locations == nil {
		*locations = make(map[string]math.Point2LL)
	}
	(*locations)[e.id] = p
}

func (e viewEntry) removeTearOff(ep *Scope) {
	delete(*e.tearOffs.locations(ep), e.id)
}

// placedTearOff is a tear-off laid out on the scope.
type placedTearOff struct {
	entry       viewEntry
	rows        RowList
	extent, bar math.Extent2D
	border      bool
}

// entryDrag is an entry being torn off or a tear-off being moved.
type entryDrag struct {
	entry  viewEntry
	origin [2]float32 // the tear-off's top left when the drag started
	offset [2]float32 // from there to the mouse
}

// layoutEntryTearOff lays out e's tear-off with its top left at p. It
// returns false if e's view no longer has the entry.
func (ep *Scope) layoutEntryTearOff(ctx *scope.Context, e viewEntry, p [2]float32) (placedTearOff, bool) {
	t, ok := e.tearOffs.tearOff(ep, ctx, e.id)
	if !ok {
		return placedTearOff{}, false
	}
	titleFont := ep.ERAMFont(2)
	l := ep.rowColumn(&t.ViewRowSource, titleFont)
	gold := tearOffBadge(titleFont, t.brightness, false)
	l.Rows = styleRows(t.Rows, t.brightness.ScaleRGB(colors.view.text), func(string) *Badge { return gold })
	h := l.Measure()
	pt := placedTearOff{
		entry:  e,
		rows:   l,
		extent: math.Extent2D{P0: [2]float32{p[0], p[1] - h}, P1: [2]float32{p[0] + l.Width, p[1]}},
		border: t.border,
	}
	pt.rows.iterRows(pt.extent, func(i int, _ measuredRow, rowExt math.Extent2D) {
		if r := pt.rows.Rows[i]; r.TearOff != "" {
			pt.bar = pt.rows.badgeExtent(r.Badge, rowExt)
		}
	})
	return pt, true
}

// drawEntryTearOffs draws the tear-offs on the scope. One picked for
// deletion is highlighted (2.5.2.2).
func (ep *Scope) drawEntryTearOffs(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	b := &ViewBuilders{Trid: trid, Ld: ld, Td: td}

	ps := ep.currentPrefs()
	ep.placedTearOffs = ep.placedTearOffs[:0]
	for _, tearOffs := range allEntryTearOffs {
		for id, p := range util.SortedMap(*tearOffs.locations(ep)) {
			pt, ok := ep.layoutEntryTearOff(ctx, viewEntry{tearOffs, id}, transforms.WindowFromLatLongP(p))
			if !ok {
				continue
			}
			ep.placedTearOffs = append(ep.placedTearOffs, pt)
			// The lowered toolbar yields clicks to it, as it does to views.
			ep.viewExtents = append(ep.viewExtents, pt.extent)

			if _, picked := ep.toolbar.selectedEntries[pt.entry]; picked {
				for i := range pt.rows.Rows {
					pt.rows.Rows[i].Selected = true
				}
			}
			pt.rows.Draw(pt.extent, b)
			if pt.border {
				ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(colors.view.border), pt.extent.Corners())
			}
			if ctx.Mouse != nil && pt.bar.Inside(ctx.Mouse.Pos) {
				ld.AddLineLoop(colors.view.hoveredOutline, pt.bar.Expand(1).Corners())
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawEntryDragPreview outlines where the entry being torn off, or the
// tear-off being moved, would be placed.
func (ep *Scope) drawEntryDragPreview(ctx *scope.Context, cb *renderer.CommandBuffer) {
	d := ep.entryDrag
	if d == nil || ctx.Mouse == nil {
		return
	}
	pt, ok := ep.layoutEntryTearOff(ctx, d.entry, math.Sub2f(ctx.Mouse.Pos, d.offset))
	if !ok {
		return
	}
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	ld.AddLineLoop(colors.view.hoveredOutline, pt.extent.Corners())
	ld.GenerateCommands(cb)
}

// startEntryDrag starts tearing off an entry or moving its tear-off, whose
// top left is at origin.
func (ep *Scope) startEntryDrag(ctx *scope.Context, e viewEntry, origin [2]float32) {
	ep.entryDrag = &entryDrag{entry: e, origin: origin, offset: math.Sub2f(ctx.Mouse.Pos, origin)}
	ctx.Platform.StartCaptureMouse(ctx.DrawExtent)
}

// placeEntryDrag places the entry being torn off or the tear-off being
// moved as a torn-off button is placed: the drag stays armed until the
// mouse has moved off where it started, so the gesture is click, move,
// click to place. It can't be placed on a toolbar (2.5.2), and it takes
// every click until it is placed.
func (ep *Scope) placeEntryDrag(ctx *scope.Context, transforms scope.Transformations) {
	d := ep.entryDrag
	mouse := ctx.Mouse
	if d == nil || mouse == nil {
		return
	}
	ep.consumeMouseClick(mouse)

	pos := math.Sub2f(mouse.Pos, d.offset)
	onToolbar := util.MapContains(ep.toolbar.layout.regions,
		func(_ string, r math.Extent2D) bool { return r.Inside(mouse.Pos) })
	if math.Distance2f(pos, d.origin) < tearoffMoveThreshold || onToolbar ||
		(!ep.mousePrimaryReleased(mouse) && !ep.mouseTertiaryReleased(mouse)) {
		return
	}
	if _, ok := d.entry.tearOffs.tearOff(ep, ctx, d.entry.id); ok {
		d.entry.place(ep, transforms.LatLongFromWindowP(pos))
	}
	ep.entryDrag = nil
	ctx.Platform.EndCaptureMouse()
}

// handleEntryTearOffMouse takes clicks on the tear-offs. While DELETE
// TEAROFF is active, a TBP on one's bar picks it for deletion or unpicks
// it and a TBE on one deletes it along with the picked tear-offs (2.5.2.1,
// 2.5.2.2); otherwise, a pick on one's bar starts moving it.
func (ep *Scope) handleEntryTearOffMouse(ctx *scope.Context) {
	mouse := ctx.Mouse
	primary, tertiary := ep.mousePrimaryClicked(mouse), ep.mouseTertiaryClicked(mouse)
	if !primary && !tertiary {
		return
	}
	tb := &ep.toolbar
	// The last drawn is on top.
	for _, pt := range slices.Backward(ep.placedTearOffs) {
		switch {
		case tb.deleting && tertiary && pt.extent.Inside(mouse.Pos):
			pt.entry.removeTearOff(ep)
			ep.deleteSelectedTearoffs()
		case tb.deleting && primary && pt.bar.Inside(mouse.Pos):
			if _, ok := tb.selectedEntries[pt.entry]; ok {
				delete(tb.selectedEntries, pt.entry)
			} else {
				tb.selectedEntries[pt.entry] = struct{}{}
			}
		case !tb.deleting && pt.bar.Inside(mouse.Pos):
			ep.startEntryDrag(ctx, pt.entry, [2]float32{pt.extent.P0[0], pt.extent.P1[1]})
		default:
			continue
		}
		ep.consumeMouseClick(mouse)
		return
	}
}

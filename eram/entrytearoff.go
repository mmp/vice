// eram/entrytearoff.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

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

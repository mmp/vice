// eram/toolbarengine.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// The toolbar engine. The toolbars themselves are described in toolbars.go
// as a tree of the types defined here; each frame the engine lays them
// out, takes the mouse over them, and draws them. The main toolbar, the
// floating toolbar control button, and each torn-off button are instances
// of the same machinery, so a button behaves the same wherever it is
// displayed.

// toolbarButtonKind is a button's type, as the ERAM documentation
// classifies them. It determines the button's color and how its state is
// shown.
type toolbarButtonKind int

const (
	buttonToggle  toolbarButtonKind = iota // black; gray while on
	buttonMenu                             // blue; red while its menu is displayed
	buttonIncDec                           // green; a left click lowers the value, a middle click raises it
	buttonCommand                          // cyan; orange while its function is active
)

// toolbarClick is a click on a toolbar button: which of the logical mouse
// buttons were clicked, as mousePrimaryClicked and friends define them.
type toolbarClick struct {
	clicked [platform.MouseButtonCount]bool
}

// errToolbarLimit reports an increment/decrement click past the end of the
// value's range.
var errToolbarLimit = errors.New("value at its limit")

type toolbarButton struct {
	// id identifies the button. The engine assigns it from the button's
	// place in the tree; see assignToolbarIDs.
	id   string
	kind toolbarButtonKind
	// Exactly one of label and labelFunc gives the button's text.
	label     string
	labelFunc func(ep *Scope) string
	// active reports that a toggle is on or that a command's function is
	// active. A menu button is active while its menu is displayed.
	active func(ep *Scope) bool
	// click handles a click on the button. Its error reports an
	// increment/decrement click past the limit of the value.
	click     func(ep *Scope, c toolbarClick) error
	submenu   *toolbarMenu
	repeat    bool // keeps firing click while held
	noTearoff bool // has no tear-off bar but keeps the space for one
	noTab     bool // has neither a tear-off bar nor the space for one
}

func (b *toolbarButton) text(ep *Scope) string {
	if b.labelFunc != nil {
		return b.labelFunc(ep)
	}
	return b.label
}

// toolbarMenu is what a menu button displays: rows of buttons, given
// directly or by rowsFunc when they depend on the scope's state, or a
// panel of its own content. Two rows are displayed at a time; holding Alt
// displays the next two.
type toolbarMenu struct {
	id       string // assigned by the engine: its button's id
	rows     [][]toolbarButton
	rowsFunc func(ep *Scope) [][]toolbarButton
	panel    toolbarPanel
}

const toolbarMenuRows = 2 // displayed at a time

// toolbarPanel is a menu's content when it isn't buttons. The engine gives
// it the anchor for its top left corner, beside the menu's button, and the
// clicks inside its extent.
type toolbarPanel interface {
	extent(ep *Scope, anchor [2]float32) math.Extent2D
	draw(ep *Scope, ctx *scope.Context, cb *renderer.CommandBuffer, anchor [2]float32)
	click(ep *Scope, anchor, p [2]float32)
	// clickOutside is called for a click anywhere else while the panel is
	// displayed.
	clickOutside(ep *Scope)
}

// adjust lowers or raises *v by step, as a click on an increment/decrement
// button does, keeping it within [min, max].
func adjust[T ~int](v *T, c toolbarClick, min, max, step int) error {
	n := int(*v) + util.Select(c.clicked[platform.MouseButtonTertiary], step, -step)
	if n < min || n > max {
		return errToolbarLimit
	}
	*v = T(n)
	return nil
}

// Button geometry, in pixels; ERAM's toolbar buttons are the same size at
// every window size.
const (
	toolbarFaceWidth  float32 = 81
	toolbarFaceHeight float32 = toolbarFaceWidth / 2.52
	toolbarTabWidth   float32 = toolbarFaceWidth / 7.2
	toolbarTabGap     float32 = 1 // between a tear-off bar and its button's face
	toolbarButtonGap  float32 = 3 // between one button and the next
	toolbarRowPitch   float32 = toolbarFaceHeight + 3
	toolbarBandHeight float32 = 82 // the main toolbar's background: two rows of buttons
	toolbarMenuMargin float32 = 2  // a menu's background past its last button
	// The main toolbar's buttons are inset from the window's top left
	// corner by these fractions of a button's size.
	toolbarInsetX float32 = toolbarTabWidth / 0.701
	toolbarInsetY float32 = toolbarFaceHeight / 4.27
)

const (
	holdRepeatDelay    = 500 * time.Millisecond
	holdRepeatInterval = 125 * time.Millisecond
	// tearoffMoveThreshold is how far a button being torn off or moved must
	// travel before releasing the mouse places it.
	tearoffMoveThreshold = 2
)

const (
	toolbarMainID    = ""        // the main toolbar instance
	toolbarControlID = "control" // the toolbar control button, and its instance
)

// Button ids come from the buttons' places in the tree: a button's row and
// column in its menu, under the id of the menu's own button. They key the
// torn-off buttons in the preferences, so moving a button in the tree calls
// for bumping the serialization version to discard saved tear-offs.
func init() {
	assignToolbarIDs(&mainToolbar, "")
	toolbarControl.id = toolbarControlID
	assignToolbarIDs(toolbarControl.submenu, toolbarControlID+"/")
}

func assignToolbarIDs(m *toolbarMenu, prefix string) {
	for r := range m.rows {
		for c := range m.rows[r] {
			b := &m.rows[r][c]
			b.id = fmt.Sprintf("%s%d.%d", prefix, r, c)
			if b.submenu != nil {
				b.submenu.id = b.id
				assignToolbarIDs(b.submenu, b.id+"/")
			}
		}
	}
}

// buttons returns a menu's rows. Those a rowsFunc supplies are given ids
// under the menu's: from their labels, so that a torn-off GeoMap filter
// stays the same filter, or else from their places in the rows.
func (m *toolbarMenu) buttons(ep *Scope) [][]toolbarButton {
	if m.rowsFunc == nil {
		return m.rows
	}
	rows := m.rowsFunc(ep)
	for r := range rows {
		for c := range rows[r] {
			b := &rows[r][c]
			b.id = m.id + "/" + util.Select(b.label != "", b.label, fmt.Sprintf("#%d.%d", r, c))
		}
	}
	return rows
}

// pressRepeat paces a press and hold: it fires when the press starts and
// then, after holdRepeatDelay, every holdRepeatInterval until the mouse is
// released. The toolbar buttons and the popup menu rows share one.
type pressRepeat struct {
	fired bool // since the last release
	next  time.Time
}

func (p *pressRepeat) fire(now time.Time) bool {
	if !p.fired {
		p.fired = true
		p.next = now.Add(holdRepeatDelay)
		return true
	}
	if now.Before(p.next) {
		return false
	}
	p.next = now.Add(holdRepeatInterval)
	return true
}

func (p *pressRepeat) release() {
	p.fired = false
}

// toolbarInstance is one place a toolbar is displayed: the main toolbar,
// the floating toolbar control button, or a torn-off button. Each has its
// own menus displayed.
type toolbarInstance struct {
	id string // the torn-off button's id, toolbarMainID, or toolbarControlID
	// open holds the ids of the menu buttons whose menus are displayed,
	// outermost first. A torn-off button's own id comes first when its
	// menu is displayed.
	open []string
}

// closeMenu closes the given menu button's menu, if it is displayed, along
// with any menu opened from it.
func (inst *toolbarInstance) closeMenu(id string) {
	if i := slices.Index(inst.open, id); i >= 0 {
		inst.open = inst.open[:i]
	}
}

// toolbarState is the transient state of the toolbars: which menus are
// displayed where, this frame's layout, and the mouse gesture under way.
type toolbarState struct {
	main    toolbarInstance
	control toolbarInstance
	tornOff []*toolbarInstance // bottom to top
	layout  toolbarLayout
	views   []math.Extent2D // the views drawn last frame, which the lowered main toolbar is under
	hover   toolbarHit
	press   toolbarPress
	drag    *toolbarDrag
	// deleting is set while the DELETE TEAROFF function is active, with
	// the torn-off buttons picked for deletion in selected.
	deleting bool
	selected map[string]struct{}
}

func (tb *toolbarState) instances() []*toolbarInstance {
	return append([]*toolbarInstance{&tb.main, &tb.control}, tb.tornOff...)
}

// toolbarPress records the button face the mouse went down on.
type toolbarPress struct {
	instance, button string
	down             bool
	inside           bool // the mouse is still over the button
	click            toolbarClick
}

// toolbarDrag is a button being torn off or a torn-off button being moved.
type toolbarDrag struct {
	id     string
	origin [2]float32 // the tear-off bar's top left when the drag started
	offset [2]float32 // from there to the mouse
}

type placedButton struct {
	button   *toolbarButton
	instance *toolbarInstance
	level    int // depth in the instance's menus; the main toolbar's buttons are at 0
	face     math.Extent2D
	tab      math.Extent2D // the tear-off bar; empty for a button without one
}

// placedLevel is one displayed menu: its buttons, or its panel and where
// it is anchored.
type placedLevel struct {
	extent  math.Extent2D // the menu's background; the first level has none
	buttons []placedButton
	panel   toolbarPanel
	anchor  [2]float32
}

type placedInstance struct {
	instance *toolbarInstance
	levels   []placedLevel
}

type toolbarLayout struct {
	main    *placedInstance // nil while the main toolbar is hidden
	tornOff []placedInstance
	control placedInstance
}

// toolbarHit is what is under a point: a button's face or tear-off bar, a
// menu's panel, a menu's background with no button there, or nothing.
type toolbarHit struct {
	button *placedButton
	tab    bool
	panel  *placedLevel
	menu   *placedLevel
}

///////////////////////////////////////////////////////////////////////////
// Layout

// toolbarButtonByID finds the button with the given id in any of the
// toolbars; it is nil for a GeoMap filter whose map isn't loaded.
func (ep *Scope) toolbarButtonByID(id string) *toolbarButton {
	if id == toolbarControlID {
		return &toolbarControl
	}
	for _, m := range []*toolbarMenu{&mainToolbar, toolbarControl.submenu} {
		if b := ep.findToolbarButton(m, id); b != nil {
			return b
		}
	}
	return nil
}

func (ep *Scope) findToolbarButton(m *toolbarMenu, id string) *toolbarButton {
	rows := m.buttons(ep)
	for r := range rows {
		for i := range rows[r] {
			b := &rows[r][i]
			if b.id == id {
				return b
			}
			if b.submenu != nil {
				if found := ep.findToolbarButton(b.submenu, id); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

// syncTornOff matches the torn-off instances to the preferences, which
// hold the torn-off buttons and their positions.
func (ep *Scope) syncTornOff() {
	tb := &ep.toolbar
	torn := ep.currentPrefs().TornOffButtons
	tb.tornOff = util.FilterSlice(tb.tornOff, func(inst *toolbarInstance) bool {
		_, ok := torn[inst.id]
		return ok
	})
	for _, id := range util.SortedMapKeys(torn) {
		if !slices.ContainsFunc(tb.tornOff, func(inst *toolbarInstance) bool { return inst.id == id }) {
			tb.tornOff = append(tb.tornOff, &toolbarInstance{id: id})
		}
	}
}

// layoutToolbars places every displayed button for the frame.
func (ep *Scope) layoutToolbars(ctx *scope.Context) {
	ps := ep.currentPrefs()
	tb := &ep.toolbar
	tb.layout = toolbarLayout{}

	if ps.DisplayToolbar {
		origin := [2]float32{toolbarInsetX, ctx.DrawExtent.Height() - toolbarInsetY}
		pi := ep.layoutInstance(ctx, &tb.main, mainToolbar.rows, origin, true)
		tb.layout.main = &pi
	}
	for _, inst := range tb.tornOff {
		b := ep.toolbarButtonByID(inst.id)
		if b == nil {
			continue
		}
		pi := ep.layoutInstance(ctx, inst, [][]toolbarButton{{*b}}, ps.TornOffButtons[inst.id], false)
		tb.layout.tornOff = append(tb.layout.tornOff, pi)
	}
	if ps.MasterToolbarPosition == ([2]float32{}) {
		ps.MasterToolbarPosition = [2]float32{30, ctx.DrawExtent.Height() - 100}
	}
	tb.layout.control = ep.layoutInstance(ctx, &tb.control, [][]toolbarButton{{toolbarControl}},
		ps.MasterToolbarPosition, false)
}

// layoutInstance places an instance's buttons: the given rows at origin,
// then the menus displayed from them. In the main toolbar, for which bar is
// set, a displayed menu replaces all of the buttons but its own.
func (ep *Scope) layoutInstance(ctx *scope.Context, inst *toolbarInstance, rows [][]toolbarButton,
	origin [2]float32, bar bool) placedInstance {
	pi := placedInstance{instance: inst}
	level := placeToolbarRows(inst, 0, rows, origin)
	if bar && len(inst.open) > 0 {
		level.buttons = util.FilterSlice(level.buttons, func(pb placedButton) bool { return pb.button.id == inst.open[0] })
	}
	pi.levels = append(pi.levels, level)

	for depth, id := range inst.open {
		opener := pi.levels[depth].find(id)
		if opener == nil || opener.button.submenu == nil {
			inst.open = inst.open[:depth]
			break
		}
		submenu := opener.button.submenu
		at := [2]float32{opener.face.P1[0] + toolbarButtonGap, opener.face.P1[1]}
		if submenu.panel != nil {
			pi.levels = append(pi.levels, placedLevel{extent: submenu.panel.extent(ep, at), panel: submenu.panel, anchor: at})
			continue
		}

		menu := util.FilterSlice(submenu.buttons(ep), func(row []toolbarButton) bool { return len(row) > 0 })
		if len(menu) > toolbarMenuRows {
			page := 0
			if ctx.Keyboard != nil && ctx.Keyboard.KeyAlt() {
				page = 1
			}
			menu = menu[min(len(menu), page*toolbarMenuRows):min(len(menu), (page+1)*toolbarMenuRows)]
		}
		// A menu's rows start at its button's row and go down. The main
		// toolbar's menus fit in its two rows, so one with two rows that is
		// opened from the second row starts on the first instead.
		if bar {
			row := int(math.Round((origin[1] - opener.face.P1[1]) / toolbarRowPitch))
			at[1] += float32(row-max(0, min(row, toolbarMenuRows-len(menu)))) * toolbarRowPitch
		}
		sub := placeToolbarRows(inst, depth+1, menu, at)
		for _, pb := range sub.buttons {
			sub.extent = math.Union(math.Union(sub.extent, pb.face.P0), pb.face.P1)
			if !pb.tab.IsEmpty() {
				sub.extent = math.Union(sub.extent, pb.tab.P0)
			}
		}
		sub.extent.P1[0] += toolbarMenuMargin
		pi.levels = append(pi.levels, sub)
	}
	return pi
}

// placeToolbarRows places rows of buttons with the first row's top left at
// origin. A button's tear-off bar, or the space for one, comes before its
// face.
func placeToolbarRows(inst *toolbarInstance, level int, rows [][]toolbarButton, origin [2]float32) placedLevel {
	pl := placedLevel{extent: math.EmptyExtent2D()}
	for r := range rows {
		x, top := origin[0], origin[1]-float32(r)*toolbarRowPitch
		for i := range rows[r] {
			b := &rows[r][i]
			pb := placedButton{button: b, instance: inst, level: level}
			if !b.noTab {
				if !b.noTearoff {
					pb.tab = math.Extent2D{P0: [2]float32{x, top - toolbarFaceHeight}, P1: [2]float32{x + toolbarTabWidth, top}}
				}
				x += toolbarTabWidth + toolbarTabGap
			}
			pb.face = math.Extent2D{P0: [2]float32{x, top - toolbarFaceHeight}, P1: [2]float32{x + toolbarFaceWidth, top}}
			x += toolbarFaceWidth + toolbarButtonGap
			pl.buttons = append(pl.buttons, pb)
		}
	}
	return pl
}

func (pl *placedLevel) find(id string) *placedButton {
	if i := slices.IndexFunc(pl.buttons, func(pb placedButton) bool { return pb.button.id == id }); i >= 0 {
		return &pl.buttons[i]
	}
	return nil
}

// toolbarHitAt returns what is under p. The toolbar control button is over
// everything. The main toolbar is over the torn-off buttons when it is
// raised; lowered, it is under them and under the views, which are drawn
// over it after it has taken its clicks, so it yields where a view was
// drawn last frame.
func (ep *Scope) toolbarHitAt(p [2]float32) toolbarHit {
	l := &ep.toolbar.layout
	if h, ok := l.control.hit(p); ok {
		return h
	}
	raised := ep.currentPrefs().MasterToolbarRaised
	if raised && l.main != nil {
		if h, ok := l.main.hit(p); ok {
			return h
		}
	}
	for _, pi := range slices.Backward(l.tornOff) {
		if h, ok := pi.hit(p); ok {
			return h
		}
	}
	if !raised && l.main != nil && !ep.viewCovers(p) {
		if h, ok := l.main.hit(p); ok {
			return h
		}
	}
	return toolbarHit{}
}

// viewCovers reports whether a view or a popup menu was drawn over p.
func (ep *Scope) viewCovers(p [2]float32) bool {
	if ep.popup != nil && ep.popupExtent.Inside(p) {
		return true
	}
	return slices.ContainsFunc(ep.toolbar.views, func(e math.Extent2D) bool { return e.Inside(p) })
}

// hit returns what is under p in the instance, with a deeper menu over the
// one it was opened from: its background covers whatever is under it.
func (pi *placedInstance) hit(p [2]float32) (toolbarHit, bool) {
	for li := len(pi.levels) - 1; li >= 0; li-- {
		level := &pi.levels[li]
		if level.panel != nil {
			if level.extent.Inside(p) {
				return toolbarHit{panel: level}, true
			}
			continue
		}
		for i := len(level.buttons) - 1; i >= 0; i-- {
			pb := &level.buttons[i]
			if pb.face.Inside(p) {
				return toolbarHit{button: pb}, true
			}
			if !pb.tab.IsEmpty() && pb.tab.Inside(p) {
				return toolbarHit{button: pb, tab: true}, true
			}
		}
		if li > 0 && level.extent.Inside(p) {
			return toolbarHit{menu: level}, true
		}
	}
	return toolbarHit{}, false
}

// panels returns every displayed panel.
func (l *toolbarLayout) panels() []*placedLevel {
	var panels []*placedLevel
	instances := append([]*placedInstance{&l.control}, l.main)
	for i := range l.tornOff {
		instances = append(instances, &l.tornOff[i])
	}
	for _, pi := range instances {
		if pi == nil {
			continue
		}
		for i := range pi.levels {
			if pi.levels[i].panel != nil {
				panels = append(panels, &pi.levels[i])
			}
		}
	}
	return panels
}

///////////////////////////////////////////////////////////////////////////
// Input

// handleToolbarInput lays out the toolbars for the frame and handles the
// mouse over them. It runs before anything else on the scope takes clicks,
// since torn-off buttons float over everything else.
func (ep *Scope) handleToolbarInput(ctx *scope.Context) {
	ep.syncTornOff()
	ep.layoutToolbars(ctx)

	tb := &ep.toolbar
	tb.views, ep.viewExtents = ep.viewExtents, nil
	tb.hover = toolbarHit{}
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	if tb.drag != nil {
		ep.placeToolbarDrag(ctx)
		return
	}
	if ep.mousePrimaryReleased(mouse) || ep.mouseTertiaryReleased(mouse) {
		tb.press = toolbarPress{}
		ep.holdRepeat.release()
	}
	tb.hover = ep.toolbarHitAt(mouse.Pos)
	now := time.Now()

	var c toolbarClick
	c.clicked[platform.MouseButtonPrimary] = ep.mousePrimaryClicked(mouse)
	c.clicked[platform.MouseButtonTertiary] = ep.mouseTertiaryClicked(mouse)
	if c == (toolbarClick{}) {
		// A held button keeps firing if it repeats.
		pb := tb.hover.button
		tb.press.inside = tb.press.down && pb != nil && !tb.hover.tab &&
			pb.instance.id == tb.press.instance && pb.button.id == tb.press.button
		if tb.press.inside && pb.button.repeat && ep.holdRepeat.fire(now) {
			ep.clickToolbarButton(pb, tb.press.click)
		}
		return
	}

	if panel := tb.hover.panel; panel != nil {
		panel.panel.click(ep, panel.anchor, mouse.Pos)
		ep.consumeMouseClick(mouse)
		return
	}
	for _, panel := range tb.layout.panels() {
		panel.panel.clickOutside(ep)
	}
	if tb.deleting && ep.clickWhileDeletingTearoffs(ctx, tb.hover, c) {
		ep.consumeMouseClick(mouse)
		return
	}
	pb := tb.hover.button
	if pb == nil {
		if tb.hover.menu != nil {
			// A click on a menu's background goes no further.
			ep.consumeMouseClick(mouse)
		}
		return
	}
	if tb.hover.tab {
		ep.startToolbarDrag(ctx, pb)
	} else {
		tb.press = toolbarPress{instance: pb.instance.id, button: pb.button.id, down: true, inside: true, click: c}
		ep.holdRepeat.fire(now)
		ep.clickToolbarButton(pb, c)
	}
	ep.consumeMouseClick(mouse)
}

func (ep *Scope) clickToolbarButton(pb *placedButton, c toolbarClick) {
	b := pb.button
	switch {
	case b.submenu != nil:
		ep.toggleToolbarMenu(pb)
	case b.click != nil:
		if err := b.click(ep, c); err != nil {
			ep.reportClickAtLimit(c)
		}
	}
}

// reportClickAtLimit flashes the invalid cursor for an increment/decrement
// click past the value's limit.
func (ep *Scope) reportClickAtLimit(c toolbarClick) {
	ep.SetTemporaryCursor(util.Select(c.clicked[platform.MouseButtonTertiary], "EramInvalidEnter", "EramInvalidSelect"), 0.5, "")
}

// toggleToolbarMenu displays a menu button's menu, or closes it and any
// menu opened from it.
func (ep *Scope) toggleToolbarMenu(pb *placedButton) {
	inst, b := pb.instance, pb.button
	if toolbarMenuOpen(pb) {
		inst.open = inst.open[:pb.level]
		return
	}
	inst.open = append(inst.open[:pb.level], b.id)

	tb := &ep.toolbar
	if b.submenu.panel != nil {
		// A panel is displayed in one place at a time: beside the button
		// clicked most recently, whether in the toolbar or torn off.
		for _, other := range tb.instances() {
			if other != inst {
				other.closeMenu(b.id)
			}
		}
	}
	// A torn-off button's menu displays over the other torn-off buttons.
	if i := slices.Index(tb.tornOff, inst); i >= 0 {
		tb.tornOff = append(slices.Delete(tb.tornOff, i, i+1), inst)
	}
}

// toolbarMenuOpen reports whether a placed menu button's menu is displayed.
func toolbarMenuOpen(pb *placedButton) bool {
	return pb.level < len(pb.instance.open) && pb.instance.open[pb.level] == pb.button.id
}

// startToolbarDrag starts tearing a button off by its tear-off bar, or
// moving a torn-off button or the toolbar control button by theirs. There
// is only one tear-off of each button; the bar of one already torn off is
// disabled.
func (ep *Scope) startToolbarDrag(ctx *scope.Context, pb *placedButton) {
	id := pb.button.id
	if !toolbarTearoffHandle(pb) {
		if _, torn := ep.currentPrefs().TornOffButtons[id]; torn {
			return
		}
	}
	origin := [2]float32{pb.tab.P0[0], pb.tab.P1[1]}
	ep.toolbar.drag = &toolbarDrag{id: id, origin: origin, offset: math.Sub2f(ctx.Mouse.Pos, origin)}
	ctx.Platform.StartCaptureMouse(ctx.DrawExtent)
}

// toolbarTearoffHandle reports whether a placed button's tear-off bar is
// the handle of a torn-off button or of the toolbar control button, which
// moves it, rather than a bar that tears the button off.
func toolbarTearoffHandle(pb *placedButton) bool {
	return pb.level == 0 && pb.instance.id != toolbarMainID
}

// placeToolbarDrag places the button being torn off or moved. The drag
// stays armed until the button has been moved off where it started, so
// tearing a button off and moving one already torn off are the same
// gesture: click, move, click to place. The click that places it must not
// also press whatever it lands on.
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
	if d.id == toolbarControlID {
		ps.MasterToolbarPosition = pos
	} else {
		if ps.TornOffButtons == nil {
			ps.TornOffButtons = make(map[string][2]float32)
		}
		ps.TornOffButtons[d.id] = pos
	}
	ep.toolbar.drag = nil
	ctx.Platform.EndCaptureMouse()
}

// cancelToolbarGesture cancels a button being torn off or moved and the
// DELETE TEAROFF function, reporting whether either was under way.
func (ep *Scope) cancelToolbarGesture(ctx *scope.Context) bool {
	tb := &ep.toolbar
	if tb.drag == nil && !tb.deleting {
		return false
	}
	if tb.drag != nil {
		tb.drag = nil
		ctx.Platform.EndCaptureMouse()
	}
	if tb.deleting {
		ep.endDeleteTearoffs()
	}
	return true
}

// toggleDeleteTearoffs starts the DELETE TEAROFF function, or ends it if
// it is active. While it is active, a left click picks or unpicks a
// torn-off button and a middle click deletes the picked ones.
func (ep *Scope) toggleDeleteTearoffs() {
	tb := &ep.toolbar
	if tb.deleting {
		ep.endDeleteTearoffs()
		return
	}
	tb.deleting = true
	tb.selected = make(map[string]struct{})
	ep.SetTemporaryCursor("EramDeletion", -1, "")
}

func (ep *Scope) endDeleteTearoffs() {
	tb := &ep.toolbar
	tb.deleting = false
	tb.selected = nil
	ep.ClearTemporaryCursor()
}

// clickWhileDeletingTearoffs handles a click while DELETE TEAROFF is
// active, reporting whether it took the click.
func (ep *Scope) clickWhileDeletingTearoffs(ctx *scope.Context, hit toolbarHit, c toolbarClick) bool {
	tb := &ep.toolbar
	if c.clicked[platform.MouseButtonTertiary] {
		ps := ep.currentPrefs()
		for id := range tb.selected {
			delete(ps.TornOffButtons, id)
		}
		ep.endDeleteTearoffs()
		ep.syncTornOff()
		ep.layoutToolbars(ctx)
		tb.hover = toolbarHit{}
		return true
	}
	pb := hit.button
	if pb == nil || !toolbarTearoffHandle(pb) || pb.instance.id == toolbarControlID {
		return false
	}
	if _, ok := tb.selected[pb.button.id]; ok {
		delete(tb.selected, pb.button.id)
	} else {
		tb.selected[pb.button.id] = struct{}{}
	}
	return true
}

///////////////////////////////////////////////////////////////////////////
// Drawing

// drawToolbar draws the main toolbar before the views, so that they are
// drawn over it, unless it is raised; drawTearoffs then draws it over them.
func (ep *Scope) drawToolbar(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	if ep.currentPrefs().MasterToolbarRaised {
		return
	}
	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ep.drawMainToolbar(ctx, cb)
	cb.ResetState()
}

// drawTearoffs draws the torn-off buttons, the main toolbar over them if it
// is raised, a button being torn off or moved, and the toolbar control
// button over everything.
func (ep *Scope) drawTearoffs(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	transforms.LoadWindowViewingMatrices(cb)
	cb.SetScissorBounds(ctx.DrawExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	cb.LineWidth(1, ctx.DPIScale)

	l := &ep.toolbar.layout
	for i := range l.tornOff {
		ep.drawPlacedInstance(ctx, cb, &l.tornOff[i])
	}
	if ep.currentPrefs().MasterToolbarRaised {
		ep.drawMainToolbar(ctx, cb)
	}
	ep.drawToolbarDragPreview(ctx, cb)
	ep.drawPlacedInstance(ctx, cb, &l.control)
	cb.ResetState()
}

func (ep *Scope) drawMainToolbar(ctx *scope.Context, cb *renderer.CommandBuffer) {
	pi := ep.toolbar.layout.main
	if pi == nil {
		return
	}
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	w, top := ctx.DrawExtent.Width(), ctx.DrawExtent.Height()-1
	trid.AddQuad([2]float32{0, top}, [2]float32{w, top}, [2]float32{w, top - toolbarBandHeight},
		[2]float32{0, top - toolbarBandHeight}, ep.currentPrefs().Brightness.Toolbar.ScaleRGB(colors.toolbar.background))
	trid.GenerateCommands(cb)

	ep.drawPlacedInstance(ctx, cb, pi)
}

func (ep *Scope) drawPlacedInstance(ctx *scope.Context, cb *renderer.CommandBuffer, pi *placedInstance) {
	for i := range pi.levels {
		level := &pi.levels[i]
		if level.panel != nil {
			level.panel.draw(ep, ctx, cb, level.anchor)
			continue
		}
		if i > 0 {
			ep.drawToolbarMenuBackground(ctx, cb, level.extent)
		}
		for j := range level.buttons {
			ep.drawPlacedButton(ctx, cb, &level.buttons[j])
		}
	}
}

func (ep *Scope) drawToolbarMenuBackground(ctx *scope.Context, cb *renderer.CommandBuffer, e math.Extent2D) {
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	p0, p1, p2, p3 := [2]float32{e.P0[0], e.P1[1]}, e.P1, [2]float32{e.P1[0], e.P0[1]}, e.P0
	trid.AddQuad(p0, p1, p2, p3, ep.currentPrefs().Brightness.Toolbar.ScaleRGB(colors.toolbar.submenuBackground))
	trid.GenerateCommands(cb)
	ep.drawToolbarMenuOutline(ctx, cb, e)
}

func (ep *Scope) drawToolbarMenuOutline(ctx *scope.Context, cb *renderer.CommandBuffer, e math.Extent2D) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	cb.LineWidth(3, ctx.DPIScale)
	ld.AddLineLoop(ep.currentPrefs().Brightness.TBBRDR.ScaleRGB(colors.menu.tearoffOutline),
		[][2]float32{{e.P0[0], e.P1[1]}, e.P1, {e.P1[0], e.P0[1]}, e.P0})
	ld.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
}

func (ep *Scope) drawPlacedButton(ctx *scope.Context, cb *renderer.CommandBuffer, pb *placedButton) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()
	tb := &ep.toolbar
	hovered := tb.hover.button == pb
	quad := func(e math.Extent2D, color renderer.RGB, hovered bool) {
		corners := [][2]float32{{e.P0[0], e.P1[1]}, e.P1, {e.P1[0], e.P0[1]}, e.P0}
		trid.AddQuad(corners[0], corners[1], corners[2], corners[3], ps.Brightness.Button.ScaleRGB(color))
		outline := util.Select(hovered, colors.toolbar.hoveredOutline, colors.toolbar.outline)
		ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(outline), corners)
	}

	if !pb.tab.IsEmpty() {
		color := colors.toolbar.tearoffBar
		if _, torn := ps.TornOffButtons[pb.button.id]; torn && !toolbarTearoffHandle(pb) {
			color = colors.toolbar.tearoffBarTorn
		}
		quad(pb.tab, color, hovered && tb.hover.tab)
	}
	quad(pb.face, ep.toolbarButtonColor(pb), hovered && !tb.hover.tab)

	style := renderer.TextStyle{Font: ep.ERAMToolbarFont(), Color: ps.Brightness.Text.ScaleRGB(colors.toolbar.text)}
	drawToolbarText(td, pb.button.text(ep), pb.face, style)

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// toolbarButtonColor returns a button's color, which its kind and whether
// it is active determine. A button is also drawn active while it is
// pressed, and a torn-off button picked for deletion is drawn in the
// DELETE TEAROFF button's active color.
func (ep *Scope) toolbarButtonColor(pb *placedButton) renderer.RGB {
	tb := &ep.toolbar
	if _, selected := tb.selected[pb.button.id]; selected && toolbarTearoffHandle(pb) {
		return colors.toolbar.commandActive
	}

	b := pb.button
	active := toolbarMenuOpen(pb) || (b.active != nil && b.active(ep)) ||
		(tb.press.inside && tb.press.instance == pb.instance.id && tb.press.button == b.id)
	switch b.kind {
	case buttonMenu:
		return util.Select(active, colors.toolbar.menuOpen, colors.toolbar.menuButton)
	case buttonIncDec:
		return colors.toolbar.incDecButton
	case buttonCommand:
		return util.Select(active, colors.toolbar.commandActive, colors.toolbar.commandButton)
	default:
		return util.Select(active, colors.toolbar.toggleOn, colors.toolbar.toggleButton)
	}
}

// drawToolbarText draws a button's text, each line centered in the face.
// A line too wide for it is drawn from the left edge, so that it is the
// trailing characters that are lost.
func drawToolbarText(td *renderer.TextDrawBuilder, text string, face math.Extent2D, style renderer.TextStyle) {
	y := face.P1[1] - 1
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		ext := style.Font.LayoutBounds(line, style.LineSpacing)
		x := face.P0[0] + max(1, (face.Width()-ext.Width())/2)
		td.AddText(line, [2]float32{x, y}, style)
		y -= ext.Height() * 1.2
	}
}

// drawToolbarDragPreview draws the outline of the button being torn off or
// moved where it would be placed.
func (ep *Scope) drawToolbarDragPreview(ctx *scope.Context, cb *renderer.CommandBuffer) {
	d := ep.toolbar.drag
	if d == nil || ctx.Mouse == nil {
		return
	}
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	p0 := math.Sub2f(ctx.Mouse.Pos, d.offset)
	w := toolbarTabWidth + toolbarTabGap + toolbarFaceWidth
	ld.AddLineLoop(colors.toolbar.hoveredOutline, [][2]float32{p0, {p0[0] + w, p0[1]},
		{p0[0] + w, p0[1] - toolbarFaceHeight}, {p0[0], p0[1] - toolbarFaceHeight}})
	ld.GenerateCommands(cb)
}

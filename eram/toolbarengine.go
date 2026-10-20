// eram/toolbarengine.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"errors"
	"fmt"
	"maps"
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
// buttons were clicked, as mousePrimaryClicked and friends define them,
// where, and on which placed button. A click on a view menu's row has
// neither of the last two.
type toolbarClick struct {
	clicked [platform.MouseButtonCount]bool
	pos     [2]float32
	button  *placedButton
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
	// moveOnly is set for a button that has only one instance: its
	// tear-off bar moves it out of its toolbar rather than tearing off a
	// copy (2.5.1).
	moveOnly bool
	// override is the data block override that a press on a press-and-hold
	// button keeps in effect until it is released. Such a button is drawn
	// with a cut corner (5.1.5.5).
	override dbOverride
	// brightness, if set, gives the brightness of the button's face in
	// place of BRIGHT's BUTTON.
	brightness func(*Preferences) scope.Brightness
	// enabled, if set, reports whether the button can be used; a disabled
	// button's text is dimmed and clicks on it do nothing.
	enabled func(ep *Scope) bool
	// drawFace, if set, draws what is on the button's face in place of its
	// text.
	drawFace func(ep *Scope, f toolbarFace)
}

// toolbarFace is a button's face being drawn: its extent, the builders to
// draw on it with, and the style of its text.
type toolbarFace struct {
	extent math.Extent2D
	ld     *renderer.ColoredLinesDrawBuilder
	trid   *renderer.ColoredTrianglesDrawBuilder
	td     *renderer.TextDrawBuilder
	style  renderer.TextStyle
}

func (b *toolbarButton) usable(ep *Scope) bool {
	return b.enabled == nil || b.enabled(ep)
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
	click(ep *Scope, ctx *scope.Context, anchor, p [2]float32)
	// clickOutside is called for a click anywhere else while the panel is
	// displayed, with what is under it.
	clickOutside(ep *Scope, hit toolbarHit)
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
	toolbarSlotWidth  float32 = toolbarTabWidth + toolbarTabGap + toolbarFaceWidth + toolbarButtonGap
	toolbarBandHeight float32 = 82 // the main toolbar's background: two rows of buttons
	toolbarMenuMargin float32 = 2  // a menu's background past its last button
	// toolbarCutCorner is the length of the sides of a press-and-hold
	// button's cut corner.
	toolbarCutCorner float32 = toolbarFaceWidth / 8
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
	// The toolbars' names in Preferences.DockedButtons.
	masterDock = "MASTER"
	horizDock  = "HORIZ"
	leftDock   = "LEFT"
	rightDock  = "RIGHT"
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
	// vertical displays the instance's menus vertically, the transpose of
	// how they are displayed horizontally: each of a menu's rows is a
	// column, and a menu hangs below its button (2.3.1).
	vertical bool
	// dock is the support toolbar a torn-off button is in, if it is in one.
	dock string
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
	override         dbOverride // kept in effect while the press is held
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
	// handle is set for a torn-off button and the toolbar control button,
	// whose tear-off bars move them rather than tearing off a copy.
	handle bool
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
	// regions holds the extents of the displayed toolbars other than the
	// main toolbar's instance: the bands of the horizontal ones and the
	// columns of the vertical ones.
	regions map[string]math.Extent2D
	arrows  []toolbarArrow
	// supportSlots holds the slots of the displayed support toolbars.
	supportSlots map[string][]math.Extent2D
}

// toolbarArrow is a pick area in a horizontal toolbar's left margin that
// moves it up or down to another container (2.3.5.2).
type toolbarArrow struct {
	extent  math.Extent2D
	toolbar string
	dir     int // -1 up, 1 down
}

// toolbarHit is what is under a point: a button's face or tear-off bar, a
// menu's panel, a menu's background with no button there, a toolbar's
// arrow, or nothing.
type toolbarHit struct {
	button *placedButton
	tab    bool
	panel  *placedLevel
	menu   *placedLevel
	arrow  *toolbarArrow
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
// hold the buttons torn off onto the scope, with their positions, and those
// in the support toolbars. A button in a vertical support toolbar displays
// its menus vertically.
func (ep *Scope) syncTornOff() {
	tb := &ep.toolbar
	ps := ep.currentPrefs()
	docks := make(map[string]string) // id -> its support toolbar, or "" for the scope
	for id := range ps.TornOffButtons {
		docks[id] = ""
	}
	for _, name := range supportToolbars {
		for _, id := range ps.DockedButtons[name] {
			docks[id] = name
		}
	}
	tb.tornOff = util.FilterSlice(tb.tornOff, func(inst *toolbarInstance) bool {
		_, ok := docks[inst.id]
		return ok
	})
	for _, id := range util.SortedMapKeys(docks) {
		if !slices.ContainsFunc(tb.tornOff, func(inst *toolbarInstance) bool { return inst.id == id }) {
			tb.tornOff = append(tb.tornOff, &toolbarInstance{id: id})
		}
	}
	for _, inst := range tb.tornOff {
		inst.dock = docks[inst.id]
		inst.vertical = inst.dock == leftDock || inst.dock == rightDock
	}
}

// layoutToolbars places every displayed button for the frame.
func (ep *Scope) layoutToolbars(ctx *scope.Context) {
	ps := ep.currentPrefs()
	tb := &ep.toolbar
	tb.layout = toolbarLayout{}

	tb.layout.regions = layoutToolbarBands(ctx, ep.toolbarContainers())
	for _, name := range []string{leftDock, rightDock} {
		if ep.supportToolbar(name).Displayed {
			tb.layout.regions[name] = ep.supportColumn(ctx, name)
		}
	}
	if ps.DisplayToolbar {
		pi := ep.layoutInstance(ctx, &tb.main, mainToolbar.rows, ep.mainToolbarOrigin(), true)
		tb.layout.main = &pi
	}
	for _, t := range horizontalToolbars {
		band, ok := tb.layout.regions[t.name]
		if !ok {
			continue
		}
		// The up arrow is at the left of the first row and the down arrow
		// at the left of the last one, or of the lower half of a toolbar
		// with one row.
		top := band.P1[1] + 1 - toolbarInsetY
		h := util.Select(t.rows == 1, toolbarFaceHeight/2, toolbarFaceHeight)
		bottom := top - float32(t.rows-1)*toolbarRowPitch - toolbarFaceHeight
		arrow := func(top float32) math.Extent2D {
			return math.Extent2D{P0: [2]float32{2, top - h}, P1: [2]float32{toolbarInsetX - 3, top}}
		}
		if _, ok := ep.toolbarMove(ctx, t.name, -1); ok {
			tb.layout.arrows = append(tb.layout.arrows, toolbarArrow{extent: arrow(top), toolbar: t.name, dir: -1})
		}
		if _, ok := ep.toolbarMove(ctx, t.name, 1); ok {
			tb.layout.arrows = append(tb.layout.arrows, toolbarArrow{extent: arrow(bottom + h), toolbar: t.name, dir: 1})
		}
	}
	tb.layout.supportSlots = make(map[string][]math.Extent2D)
	for _, name := range supportToolbars {
		ep.layoutSupportToolbar(ctx, name)
	}
	for _, inst := range tb.tornOff {
		b := ep.toolbarButtonByID(inst.id)
		if b == nil || inst.dock != "" {
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
// set, a displayed menu replaces all of the buttons but its own, and a
// button that has been moved out of it leaves its place empty. A menu
// displayed from another menu replaces that menu's buttons in the columns
// past its own button's (figures 2-22, 3-12, and 3-19).
func (ep *Scope) layoutInstance(ctx *scope.Context, inst *toolbarInstance, rows [][]toolbarButton,
	origin [2]float32, bar bool) placedInstance {
	pi := placedInstance{instance: inst}
	level := placeToolbarRows(inst, 0, rows, origin, false)
	if bar {
		// The main toolbar holds the buttons docked in it after its own.
		docked := ep.placeDockedButtons(inst, ep.currentPrefs().DockedButtons[masterDock], masterSlots(ctx, origin))
		level.buttons = append(level.buttons, docked...)
		level.buttons = util.FilterSlice(level.buttons, func(pb placedButton) bool {
			if len(inst.open) > 0 {
				return pb.button.id == inst.open[0]
			}
			return pb.handle || !pb.button.moveOnly || !ep.isTornOff(pb.button.id)
		})
	} else {
		for i := range level.buttons {
			level.buttons[i].handle = true
		}
	}
	pi.levels = append(pi.levels, level)

	for depth, id := range inst.open {
		parent := &pi.levels[depth]
		pb := parent.find(id)
		if pb == nil || pb.button.submenu == nil {
			inst.open = inst.open[:depth]
			break
		}
		opener := *pb
		if depth > 0 {
			parent.buttons = util.FilterSlice(parent.buttons, func(b placedButton) bool {
				if inst.vertical {
					return b.face.P1[1] >= opener.face.P1[1]
				}
				return b.face.P0[0] <= opener.face.P0[0]
			})
			parent.fitExtent()
		}
		submenu := opener.button.submenu
		at := [2]float32{opener.face.P1[0] + toolbarButtonGap, opener.face.P1[1]}
		if inst.vertical {
			at = [2]float32{opener.face.P0[0] - toolbarTabWidth - toolbarTabGap, opener.face.P1[1] - toolbarRowPitch}
		}
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
		sub := placeToolbarRows(inst, depth+1, menu, at, inst.vertical)
		sub.fitExtent()
		// A vertical menu's columns run right, so one that would go past
		// the window's right edge is moved left of it.
		if over := sub.extent.P1[0] - ctx.DrawExtent.Width(); inst.vertical && over > 0 {
			sub.shift(-over)
		}
		pi.levels = append(pi.levels, sub)
	}
	return pi
}

// placeDockedButtons places the buttons with the given ids in the given
// slots of a toolbar, in order, as torn-off copies.
func (ep *Scope) placeDockedButtons(inst *toolbarInstance, ids []string, slots []math.Extent2D) []placedButton {
	var pbs []placedButton
	for i, id := range ids[:min(len(ids), len(slots))] {
		b := ep.toolbarButtonByID(id)
		if b == nil {
			continue
		}
		s := slots[i]
		pbs = append(pbs, placedButton{button: b, instance: inst, handle: true,
			tab:  math.Extent2D{P0: s.P0, P1: [2]float32{s.P0[0] + toolbarTabWidth, s.P1[1]}},
			face: math.Extent2D{P0: [2]float32{s.P1[0] - toolbarFaceWidth, s.P0[1]}, P1: s.P1}})
	}
	return pbs
}

// masterSlots returns the master toolbar's slots for torn-off buttons in
// order: the end of its shorter row, then the columns past its longer one,
// top then bottom, as far as the window is wide (2.3.2). Each spans a
// button's tear-off bar and face.
func masterSlots(ctx *scope.Context, origin [2]float32) []math.Extent2D {
	slot := func(r, c int) math.Extent2D {
		x, top := origin[0]+float32(c)*toolbarSlotWidth, origin[1]-float32(r)*toolbarRowPitch
		return math.Extent2D{P0: [2]float32{x, top - toolbarFaceHeight},
			P1: [2]float32{x + toolbarSlotWidth - toolbarButtonGap, top}}
	}
	n := util.ReduceSlice(mainToolbar.rows, 0, func(row []toolbarButton, n int) int { return max(n, len(row)) })
	var slots []math.Extent2D
	for r, row := range mainToolbar.rows {
		for c := len(row); c < n; c++ {
			slots = append(slots, slot(r, c))
		}
	}
	for c := n; origin[0]+float32(c+1)*toolbarSlotWidth <= ctx.DrawExtent.Width(); c++ {
		for r := range mainToolbar.rows {
			slots = append(slots, slot(r, c))
		}
	}
	return slots
}

// fitExtent fits a menu's background to its buttons and their tear-off
// bars.
func (pl *placedLevel) fitExtent() {
	pl.extent = math.EmptyExtent2D()
	for _, pb := range pl.buttons {
		pl.extent = math.Union(math.Union(pl.extent, pb.face.P0), pb.face.P1)
		if !pb.tab.IsEmpty() {
			pl.extent = math.Union(pl.extent, pb.tab.P0)
		}
	}
	pl.extent.P1[0] += toolbarMenuMargin
}

// placeToolbarRows places rows of buttons with the first row's top left at
// origin. A button's tear-off bar, or the space for one, comes before its
// face. Placed vertically, each row is a column instead, and the faces of
// a column line up whether or not they have tear-off bars.
func placeToolbarRows(inst *toolbarInstance, level int, rows [][]toolbarButton, origin [2]float32,
	vertical bool) placedLevel {
	pl := placedLevel{extent: math.EmptyExtent2D()}
	for r := range rows {
		x, top := origin[0], origin[1]-float32(r)*toolbarRowPitch
		for i := range rows[r] {
			if vertical {
				x, top = origin[0]+float32(r)*toolbarSlotWidth, origin[1]-float32(i)*toolbarRowPitch
			}
			b := &rows[r][i]
			pb := placedButton{button: b, instance: inst, level: level}
			if !b.noTab || vertical {
				if !b.noTab && !b.noTearoff {
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

// shift moves a menu's buttons and background dx to the right.
func (pl *placedLevel) shift(dx float32) {
	move := func(e *math.Extent2D) {
		if !e.IsEmpty() {
			e.P0[0] += dx
			e.P1[0] += dx
		}
	}
	move(&pl.extent)
	for i := range pl.buttons {
		move(&pl.buttons[i].face)
		move(&pl.buttons[i].tab)
	}
}

func (pl *placedLevel) find(id string) *placedButton {
	if i := slices.IndexFunc(pl.buttons, func(pb placedButton) bool { return pb.button.id == id }); i >= 0 {
		return &pl.buttons[i]
	}
	return nil
}

// toolbarHitAt returns what is under p. The toolbar control button is over
// everything. The raised toolbars are over the torn-off buttons on the
// scope; the lowered ones are under them and under the views, which are
// drawn over them after they have taken their clicks, so they yield where
// a view was drawn last frame.
func (ep *Scope) toolbarHitAt(p [2]float32) toolbarHit {
	l := &ep.toolbar.layout
	if h, ok := l.control.hit(p); ok {
		return h
	}
	if h, ok := ep.toolbarsHit(p, true); ok {
		return h
	}
	for _, pi := range slices.Backward(l.tornOff) {
		if pi.instance.dock != "" {
			continue
		}
		if h, ok := pi.hit(p); ok {
			return h
		}
	}
	if !ep.viewCovers(p) {
		if h, ok := ep.toolbarsHit(p, false); ok {
			return h
		}
	}
	return toolbarHit{}
}

// toolbarsHit returns what of the raised or lowered toolbars, as given, is
// under p: one of their buttons or menus, or one of their arrows.
func (ep *Scope) toolbarsHit(p [2]float32, raised bool) (toolbarHit, bool) {
	l := &ep.toolbar.layout
	for _, pi := range slices.Backward(l.tornOff) {
		if pi.instance.dock != "" && ep.toolbarRaised(pi.instance.dock) == raised {
			if h, ok := pi.hit(p); ok {
				return h, true
			}
		}
	}
	if l.main != nil && ep.toolbarRaised(masterDock) == raised {
		if h, ok := l.main.hit(p); ok {
			return h, true
		}
	}
	for i, a := range l.arrows {
		if ep.toolbarRaised(a.toolbar) == raised && a.extent.Inside(p) {
			return toolbarHit{arrow: &l.arrows[i]}, true
		}
	}
	return toolbarHit{}, false
}

// toolbarCovers reports whether p is over a displayed toolbar's background.
func (ep *Scope) toolbarCovers(p [2]float32) bool {
	return util.SeqContainsFunc(maps.Values(ep.toolbar.layout.regions), func(e math.Extent2D) bool { return e.Inside(p) })
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

// placedInstances returns the instances placed: the toolbar control
// button's, the main toolbar's if it is displayed, and the torn-off
// buttons'.
func (l *toolbarLayout) placedInstances() []*placedInstance {
	instances := []*placedInstance{&l.control}
	if l.main != nil {
		instances = append(instances, l.main)
	}
	for i := range l.tornOff {
		instances = append(instances, &l.tornOff[i])
	}
	return instances
}

// panels returns every displayed panel.
func (l *toolbarLayout) panels() []*placedLevel {
	var panels []*placedLevel
	for _, pi := range l.placedInstances() {
		for i := range pi.levels {
			if pi.levels[i].panel != nil {
				panels = append(panels, &pi.levels[i])
			}
		}
	}
	return panels
}

// findPlaced returns where the button with the given id is displayed in the
// given instance, or in any instance if inst is nil; it returns nil if it
// isn't displayed.
func (l *toolbarLayout) findPlaced(inst *toolbarInstance, id string) *placedButton {
	for _, pi := range l.placedInstances() {
		if inst != nil && pi.instance != inst {
			continue
		}
		for i := range pi.levels {
			if pb := pi.levels[i].find(id); pb != nil {
				return pb
			}
		}
	}
	return nil
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
		ep.endToolbarPress(ctx)
		ep.holdRepeat.release()
	}
	tb.hover = ep.toolbarHitAt(mouse.Pos)
	now := time.Now()

	var c toolbarClick
	c.clicked[platform.MouseButtonPrimary] = ep.mousePrimaryClicked(mouse)
	c.clicked[platform.MouseButtonTertiary] = ep.mouseTertiaryClicked(mouse)
	c.pos = mouse.Pos
	if c.clicked == ([platform.MouseButtonCount]bool{}) {
		// A held button keeps firing if it repeats.
		pb := tb.hover.button
		tb.press.inside = tb.press.down && pb != nil && !tb.hover.tab &&
			pb.instance.id == tb.press.instance && pb.button.id == tb.press.button
		if tb.press.inside && pb.button.repeat && ep.holdRepeat.fire(now) {
			ep.clickToolbarButton(pb, tb.press.click)
		}
		return
	}
	if tb.press.down && tb.press.override != noDBOverride {
		// Pressing another trackball button ends an override (5.1.5.5).
		ep.endToolbarPress(ctx)
		ep.consumeMouseClick(mouse)
		return
	}

	if panel := tb.hover.panel; panel != nil {
		panel.panel.click(ep, ctx, panel.anchor, mouse.Pos)
		ep.consumeMouseClick(mouse)
		return
	}
	for _, panel := range tb.layout.panels() {
		panel.panel.clickOutside(ep, tb.hover)
	}
	if tb.deleting && ep.clickWhileDeletingTearoffs(ctx, tb.hover, c) {
		ep.consumeMouseClick(mouse)
		return
	}
	if a := tb.hover.arrow; a != nil {
		if containers, ok := ep.toolbarMove(ctx, a.toolbar, a.dir); ok {
			ep.setToolbarContainers(containers)
		}
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
	} else if pb.button.usable(ep) {
		tb.press = toolbarPress{instance: pb.instance.id, button: pb.button.id, down: true, inside: true, click: c,
			override: pb.button.override}
		if pb.button.override != noDBOverride {
			// The cursor is locked on the button while it is held.
			lockCursor(ctx, pb.face)
		}
		ep.holdRepeat.fire(now)
		ep.clickToolbarButton(pb, c)
	}
	ep.consumeMouseClick(mouse)
}

// endToolbarPress ends the press on a toolbar button, along with the
// override it held in effect and the cursor lock that came with it.
func (ep *Scope) endToolbarPress(ctx *scope.Context) {
	if ep.toolbar.press.override != noDBOverride {
		ctx.Platform.EndCaptureMouse()
	}
	ep.toolbar.press = toolbarPress{}
}

// dbOverrideHeld reports whether a press-and-hold button is keeping the
// given override in effect.
func (ep *Scope) dbOverrideHeld(o dbOverride) bool {
	p := &ep.toolbar.press
	return p.down && p.override == o
}

// lockCursor keeps the cursor within e. The platform takes the extent in
// window coordinates, which run down from the top of the window, above
// the menu bar.
func lockCursor(ctx *scope.Context, e math.Extent2D) {
	h := ctx.DrawExtent.Height() + ctx.MenuBarHeight
	e.P0[1], e.P1[1] = h-e.P1[1], h-e.P0[1]
	ctx.Platform.StartCaptureMouse(e)
}

func (ep *Scope) clickToolbarButton(pb *placedButton, c toolbarClick) {
	b := pb.button
	c.button = pb
	switch {
	case b.submenu != nil:
		// TBE on the toolbar control button displays its menu vertically
		// and TBP horizontally (2.3.1).
		if pb.instance == &ep.toolbar.control && pb.level == 0 && !toolbarMenuOpen(pb) {
			pb.instance.vertical = c.clicked[platform.MouseButtonTertiary]
		}
		ep.toggleToolbarMenu(pb)
	case b.click != nil:
		if err := b.click(ep, c); err != nil {
			ep.reportClickAtLimit(c)
		}
	}
}

// reportClickAtLimit reports an increment/decrement click past the value's
// limit as an invalid click.
func (ep *Scope) reportClickAtLimit(c toolbarClick) {
	ep.reportInvalidClick(c.clicked[platform.MouseButtonTertiary])
}

// reportInvalidClick flashes the invalid cursor for the trackball button
// clicked, TBE's if tertiary is set and TBP's otherwise, and sounds the
// alarm.
func (ep *Scope) reportInvalidClick(tertiary bool) {
	ep.SetTemporaryCursor(util.Select(tertiary, "EramInvalidEnter", "EramInvalidSelect"), 0.5, "")
	ep.soundAlarm()
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

// closeToolbarPanel closes the menus that display the given panel.
func (ep *Scope) closeToolbarPanel(p toolbarPanel) {
	for _, inst := range ep.toolbar.instances() {
		for _, id := range inst.open {
			if b := ep.toolbarButtonByID(id); b != nil && b.submenu != nil && b.submenu.panel == p {
				inst.closeMenu(id)
				break
			}
		}
	}
}

// opensPanel reports whether a placed button is one that displays the given
// panel.
func opensPanel(pb *placedButton, p toolbarPanel) bool {
	return pb != nil && pb.button.submenu != nil && pb.button.submenu.panel == p
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

// cancelToolbarGesture cancels a button being torn off or moved, the
// DELETE TEAROFF function, and an override held by a press-and-hold
// button, reporting whether any was under way.
func (ep *Scope) cancelToolbarGesture(ctx *scope.Context) bool {
	tb := &ep.toolbar
	if tb.press.down && tb.press.override != noDBOverride {
		ep.endToolbarPress(ctx)
		return true
	}
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
// torn-off button and a middle click deletes a torn-off button along with
// the picked ones (2.5.1.2).
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
		for id := range tb.selected {
			ep.removeTearoff(id)
		}
		ep.endDeleteTearoffs()
		ep.syncTornOff()
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

///////////////////////////////////////////////////////////////////////////
// Drawing

// drawToolbar draws the lowered toolbars before the views, so that they
// are drawn over them; drawTearoffs draws the raised ones over the views.
func (ep *Scope) drawToolbar(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ep.drawToolbars(ctx, cb, false)
	cb.ResetState()
}

// drawTearoffs draws the torn-off buttons on the scope, the raised
// toolbars over them, a button being torn off or moved, and the toolbar
// control button over everything.
func (ep *Scope) drawTearoffs(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	transforms.LoadWindowViewingMatrices(cb)
	cb.SetScissorBounds(ctx.DrawExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	cb.LineWidth(1, ctx.DPIScale)

	l := &ep.toolbar.layout
	for i := range l.tornOff {
		if l.tornOff[i].instance.dock == "" {
			ep.drawPlacedInstance(ctx, cb, &l.tornOff[i])
		}
	}
	ep.drawToolbars(ctx, cb, true)
	ep.drawToolbarDragPreview(ctx, cb)
	ep.drawPlacedInstance(ctx, cb, &l.control)
	cb.ResetState()
}

// drawToolbars draws the displayed toolbars that are raised or lowered, as
// given: their backgrounds first and then their buttons and menus, so
// that one toolbar's menus are over the others.
func (ep *Scope) drawToolbars(ctx *scope.Context, cb *renderer.CommandBuffer, raised bool) {
	l := &ep.toolbar.layout
	shown := func(name string) bool {
		_, ok := l.regions[name]
		return ok && ep.toolbarRaised(name) == raised
	}
	for _, name := range allToolbars {
		if shown(name) {
			ep.drawToolbarBand(ctx, cb, name)
		}
	}
	if l.main != nil && shown(masterDock) {
		ep.drawPlacedInstance(ctx, cb, l.main)
	}
	for i := range l.tornOff {
		if dock := l.tornOff[i].instance.dock; dock != "" && shown(dock) {
			ep.drawPlacedInstance(ctx, cb, &l.tornOff[i])
		}
	}
}

// drawToolbarBand draws a toolbar's background, and the arrows of a
// horizontal one, gray pick areas with white arrows (figure 2-10).
func (ep *Scope) drawToolbarBand(ctx *scope.Context, cb *renderer.CommandBuffer, name string) {
	ps := ep.currentPrefs()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	l := &ep.toolbar.layout
	addQuad(trid, l.regions[name], ps.Brightness.Toolbar.ScaleRGB(colors.toolbar.background))
	for i, a := range l.arrows {
		if a.toolbar != name {
			continue
		}
		e := a.extent
		addQuad(trid, e, ps.Brightness.Button.ScaleRGB(colors.toolbar.tearoffBarTorn))
		outline := util.Select(ep.toolbar.hover.arrow == &l.arrows[i], colors.toolbar.hoveredOutline,
			colors.toolbar.outline)
		ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(outline), e.Corners())
		c, w := e.Center(), e.Width()/3
		tip := [2]float32{c[0], c[1] - float32(a.dir)*e.Height()/3}
		tail := [2]float32{c[0], c[1] + float32(a.dir)*e.Height()/3}
		text := ps.Brightness.Text.ScaleRGB(colors.toolbar.text)
		ld.AddLine(tail, tip, text)
		ld.AddLine(tip, [2]float32{tip[0] - w, tip[1] + float32(a.dir)*w}, text)
		ld.AddLine(tip, [2]float32{tip[0] + w, tip[1] + float32(a.dir)*w}, text)
	}
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

// mainToolbarOrigin returns the top left of the main toolbar's first
// button's tear-off bar.
func (ep *Scope) mainToolbarOrigin() [2]float32 {
	return [2]float32{toolbarInsetX, ep.toolbar.layout.regions[masterDock].P1[1] + 1 - toolbarInsetY}
}

///////////////////////////////////////////////////////////////////////////
// Horizontal toolbar containers

// horizontalToolbar is a toolbar that the horizontal containers hold.
type horizontalToolbar struct {
	name      string
	rows      int
	displayed func(*Preferences) bool
	container func(*Preferences) *int
}

var horizontalToolbars = []horizontalToolbar{{
	name:      masterDock,
	rows:      len(mainToolbar.rows),
	displayed: func(ps *Preferences) bool { return ps.DisplayToolbar },
	container: func(ps *Preferences) *int { return &ps.MasterToolbarContainer },
}, {
	name:      horizDock,
	rows:      1,
	displayed: func(ps *Preferences) bool { return ps.HorizToolbar.Displayed },
	container: func(ps *Preferences) *int { return &ps.HorizToolbar.Container },
}}

// toolbarContainers returns the containers of the displayed horizontal
// toolbars, by name.
func (ep *Scope) toolbarContainers() map[string]int {
	ps := ep.currentPrefs()
	containers := make(map[string]int)
	for _, t := range horizontalToolbars {
		if t.displayed(ps) {
			containers[t.name] = *t.container(ps)
		}
	}
	return containers
}

func (ep *Scope) setToolbarContainers(containers map[string]int) {
	ps := ep.currentPrefs()
	for _, t := range horizontalToolbars {
		if c, ok := containers[t.name]; ok {
			*t.container(ps) = c
		}
	}
}

// layoutToolbarBands returns the bands of the horizontal toolbars in the
// given containers, which span the window. Those in the top two containers
// stack down from the window's top and those in the bottom two up from its
// bottom (2.3.5.2).
func layoutToolbarBands(ctx *scope.Context, containers map[string]int) map[string]math.Extent2D {
	bands := make(map[string]math.Extent2D)
	w, top, bottom := ctx.DrawExtent.Width(), ctx.DrawExtent.Height()-1, float32(0)
	for _, c := range []int{0, 1, 3, 2} {
		for _, t := range horizontalToolbars {
			if tc, ok := containers[t.name]; !ok || tc != c {
				continue
			}
			h := float32(t.rows) * toolbarBandHeight / 2
			if c < 2 {
				bands[t.name] = math.Extent2D{P0: [2]float32{0, top - h}, P1: [2]float32{w, top}}
				top -= h
			} else {
				bands[t.name] = math.Extent2D{P0: [2]float32{0, bottom}, P1: [2]float32{w, bottom + h}}
				bottom += h
			}
		}
	}
	return bands
}

// toolbarMove returns the containers after a horizontal toolbar's up or
// down arrow moves it: to the next container that way where it is
// displayed elsewhere, trading places with a toolbar already there. It
// returns false if the toolbar can't move that way.
func (ep *Scope) toolbarMove(ctx *scope.Context, name string, dir int) (map[string]int, bool) {
	cur := ep.toolbarContainers()
	band := layoutToolbarBands(ctx, cur)[name]
	for c := cur[name] + dir; c >= 0 && c < 4; c += dir {
		next := maps.Clone(cur)
		for n, nc := range next {
			if nc == c {
				next[n] = cur[name]
			}
		}
		next[name] = c
		if layoutToolbarBands(ctx, next)[name] != band {
			return next, true
		}
	}
	return nil, false
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
	bright := ps.Brightness.Button
	if pb.button.brightness != nil {
		bright = pb.button.brightness(ps)
	}
	quad := func(e math.Extent2D, color renderer.RGB, hovered bool) {
		corners := [][2]float32{{e.P0[0], e.P1[1]}, e.P1, {e.P1[0], e.P0[1]}, e.P0}
		trid.AddQuad(corners[0], corners[1], corners[2], corners[3], bright.ScaleRGB(color))
		outline := util.Select(hovered, colors.toolbar.hoveredOutline, colors.toolbar.outline)
		ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(outline), corners)
	}

	if !pb.tab.IsEmpty() {
		color := colors.toolbar.tearoffBar
		if !pb.handle && ep.isTornOff(pb.button.id) {
			color = colors.toolbar.tearoffBarTorn
		}
		quad(pb.tab, color, hovered && tb.hover.tab)
	}
	quad(pb.face, ep.toolbarButtonColor(pb), hovered && !tb.hover.tab)
	if pb.button.override != noDBOverride {
		// The cut corner is the other color of the button's two: gray
		// while it is black, and black while it is held and gray (5.1.5.5).
		f, n := pb.face, toolbarCutCorner
		held := ep.dbOverrideHeld(pb.button.override)
		trid.AddTriangle([2]float32{f.P1[0] - n, f.P1[1]}, f.P1, [2]float32{f.P1[0], f.P1[1] - n},
			ps.Brightness.Button.ScaleRGB(util.Select(held, colors.toolbar.toggleButton, colors.toolbar.toggleOn)))
	}
	if _, picked := tb.selected[pb.button.id]; picked && pb.handle {
		// A torn-off button picked for deletion is covered by an X (2.5.1.2).
		f, x := pb.face, ps.Brightness.Text.ScaleRGB(colors.toolbar.deletionMark)
		ld.AddLine(f.P0, f.P1, x)
		ld.AddLine([2]float32{f.P0[0], f.P1[1]}, [2]float32{f.P1[0], f.P0[1]}, x)
	}

	text := util.Select(pb.button.usable(ep), colors.toolbar.text, colors.toolbar.disabledText)
	style := renderer.TextStyle{Font: ep.ERAMToolbarFont(), Color: ps.Brightness.Text.ScaleRGB(text)}
	if pb.button.drawFace != nil {
		pb.button.drawFace(ep, toolbarFace{extent: pb.face, ld: ld, trid: trid, td: td, style: style})
	} else {
		drawToolbarText(td, pb.button.text(ep), pb.face, style)
	}

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// toolbarButtonColor returns a button's color, which its kind and whether
// it is active determine. A button is also drawn active while it is
// pressed.
func (ep *Scope) toolbarButtonColor(pb *placedButton) renderer.RGB {
	tb := &ep.toolbar
	b := pb.button
	active := toolbarMenuOpen(pb) || (b.active != nil && b.active(ep)) ||
		(tb.press.inside && tb.press.instance == pb.instance.id && tb.press.button == b.id) ||
		(b.override != noDBOverride && ep.dbOverrideHeld(b.override))
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
// Support toolbars

// supportToolbars are the toolbars that hold only buttons torn off the
// master toolbar (2.3.4): the horizontal one, which is in a container as
// the master toolbar is, and the left and right ones, which run down the
// edges of the window. The buttons in them behave as those torn off onto
// the scope do, so more than one of their menus may be displayed; those of
// the vertical ones display their menus vertically.
var supportToolbars = []string{horizDock, leftDock, rightDock}

// allToolbars holds every toolbar's name, the master toolbar's first.
var allToolbars = append([]string{masterDock}, supportToolbars...)

func (ep *Scope) supportToolbar(name string) *SupportToolbar {
	ps := ep.currentPrefs()
	switch name {
	case horizDock:
		return &ps.HorizToolbar
	case leftDock:
		return &ps.LeftToolbar
	default:
		return &ps.RightToolbar
	}
}

// toolbarRaised reports whether a toolbar is raised over the views and the
// torn-off buttons on the scope (2.3.5.3).
func (ep *Scope) toolbarRaised(name string) bool {
	if name == masterDock {
		return ep.currentPrefs().MasterToolbarRaised
	}
	return ep.supportToolbar(name).Raised
}

// supportColumnWidth is the width of a vertical support toolbar: a button
// with the main toolbar's inset on either side.
const supportColumnWidth = 2*toolbarInsetX + toolbarSlotWidth - toolbarButtonGap

// supportColumn returns the extent of a vertical support toolbar: along the
// window's left or right edge, between the horizontal toolbars at its top
// and at its bottom.
func (ep *Scope) supportColumn(ctx *scope.Context, name string) math.Extent2D {
	l := &ep.toolbar.layout
	top, bottom := ctx.DrawExtent.Height()-1, float32(0)
	for n, c := range ep.toolbarContainers() {
		if c < 2 {
			top = min(top, l.regions[n].P0[1])
		} else {
			bottom = max(bottom, l.regions[n].P1[1])
		}
	}
	x := util.Select(name == leftDock, 0, ctx.DrawExtent.Width()-supportColumnWidth)
	return math.Extent2D{P0: [2]float32{x, bottom}, P1: [2]float32{x + supportColumnWidth, top}}
}

// toolbarSlots returns a displayed toolbar's slots for torn-off buttons in
// order, each spanning a button's tear-off bar and face: the master
// toolbar's past its own buttons, and a support toolbar's those of its
// buttons and the one past them if there is room.
func (ep *Scope) toolbarSlots(ctx *scope.Context, name string) []math.Extent2D {
	if _, ok := ep.toolbar.layout.regions[name]; !ok {
		return nil
	}
	if name == masterDock {
		return masterSlots(ctx, ep.mainToolbarOrigin())
	}
	return ep.toolbar.layout.supportSlots[name]
}

// layoutSupportToolbar places the buttons in a displayed support toolbar
// one after another along its band or down its column. Each one's menus
// are displayed within the toolbar, before the buttons after it (2.3.4),
// so they push those along. Buttons past the end of the toolbar aren't
// displayed.
func (ep *Scope) layoutSupportToolbar(ctx *scope.Context, name string) {
	tb := &ep.toolbar
	region, ok := tb.layout.regions[name]
	if !ok {
		return
	}
	vertical := name != horizDock
	at := [2]float32{region.P0[0] + toolbarInsetX, region.P1[1] + 1 - toolbarInsetY}
	slot := func() math.Extent2D {
		return math.Extent2D{P0: [2]float32{at[0], at[1] - toolbarFaceHeight},
			P1: [2]float32{at[0] + toolbarSlotWidth - toolbarButtonGap, at[1]}}
	}
	fits := func() bool {
		s := slot()
		return s.P1[0] <= region.P1[0] && s.P0[1] >= region.P0[1]
	}
	var slots []math.Extent2D
	for _, id := range ep.currentPrefs().DockedButtons[name] {
		b := ep.toolbarButtonByID(id)
		i := slices.IndexFunc(tb.tornOff, func(inst *toolbarInstance) bool { return inst.id == id })
		if b == nil || i < 0 {
			continue
		}
		if !fits() {
			break
		}
		slots = append(slots, slot())
		pi := ep.layoutInstance(ctx, tb.tornOff[i], [][]toolbarButton{{*b}}, at, false)
		tb.layout.tornOff = append(tb.layout.tornOff, pi)
		if e := pi.extent(); vertical {
			at[1] = e.P0[1] - (toolbarRowPitch - toolbarFaceHeight)
		} else {
			at[0] = e.P1[0] + toolbarButtonGap
		}
	}
	if fits() {
		slots = append(slots, slot())
	}
	tb.layout.supportSlots[name] = slots
}

// extent returns the extent of all that is placed in an instance.
func (pi *placedInstance) extent() math.Extent2D {
	e := math.EmptyExtent2D()
	add := func(x math.Extent2D) {
		if !x.IsEmpty() {
			e = math.Union(math.Union(e, x.P0), x.P1)
		}
	}
	for _, level := range pi.levels {
		add(level.extent)
		for _, pb := range level.buttons {
			add(pb.face)
			add(pb.tab)
		}
	}
	return e
}

// eram/toolbarengine_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/videomaps"
)

func makeToolbarTestScope() (*Scope, *scope.Context) {
	imgui.CreateContext()
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()}}
	ep.systemFont[0] = &renderer.Font{Size: 12}
	ep.videoMapLabel = "ZBW\nMAP"
	ep.allVideoMaps = []videomaps.ERAMMap{{LabelLine1: "MIA", LabelLine2: "1"}, {LabelLine1: "MVA", LabelLine2: "2"}}
	ep.bcgNames = []string{"MIA", "", "MVA"}
	ctx := &scope.Context{
		DrawExtent: math.Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{1600, 900}},
		Keyboard:   &platform.KeyboardState{},
	}
	return ep, ctx
}

func tertiaryClick() toolbarClick {
	var c toolbarClick
	c.clicked[platform.MouseButtonTertiary] = true
	return c
}

// walkToolbarButtons calls f for every button in the toolbars, including
// those of the generated menus.
func walkToolbarButtons(ep *Scope, f func(b *toolbarButton)) {
	var walk func(m *toolbarMenu)
	walk = func(m *toolbarMenu) {
		rows := m.buttons(ep)
		for r := range rows {
			for i := range rows[r] {
				f(&rows[r][i])
				if rows[r][i].submenu != nil {
					walk(rows[r][i].submenu)
				}
			}
		}
	}
	walk(&mainToolbar)
	f(&toolbarControl)
	walk(toolbarControl.submenu)
}

// toolbarButtonID returns the id of the button with the given text.
func toolbarButtonID(t *testing.T, ep *Scope, text string) string {
	t.Helper()
	id := ""
	walkToolbarButtons(ep, func(b *toolbarButton) {
		if b.text(ep) == text {
			id = b.id
		}
	})
	if id == "" {
		t.Fatalf("no button %q", text)
	}
	return id
}

// Every button has an id, no two share one, and each can be found by it.
func TestToolbarButtonIDs(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	seen := make(map[string]string)
	walkToolbarButtons(ep, func(b *toolbarButton) {
		if b.id == "" {
			t.Errorf("button %q has no id", b.text(ep))
		}
		if other, ok := seen[b.id]; ok {
			t.Errorf("id %q is shared by %q and %q", b.id, other, b.text(ep))
		}
		seen[b.id] = b.text(ep)
		if found := ep.toolbarButtonByID(b.id); found == nil || found.text(ep) != b.text(ep) {
			t.Errorf("looking up %q found %v", b.id, found)
		}
	})
}

func placedButtonFace(t *testing.T, ep *Scope, pi *placedInstance, text string) math.Extent2D {
	t.Helper()
	for _, level := range pi.levels {
		for _, pb := range level.buttons {
			if pb.button.text(ep) == text {
				return pb.face
			}
		}
	}
	t.Fatalf("button %q not placed", text)
	return math.Extent2D{}
}

// A menu's rows fill the two rows of the main toolbar from its button's
// right edge: one opened from the second row starts on the first.
func TestToolbarMenuRows(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ep.handleToolbarInput(ctx)
	main := ep.toolbar.layout.main
	bright := placedButtonFace(t, ep, main, "BRIGHT")
	views := placedButtonFace(t, ep, main, "VIEWS")
	if views.P1[1] != bright.P1[1]-toolbarRowPitch {
		t.Fatalf("VIEWS at %v is not one row below BRIGHT at %v", views, bright)
	}

	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "VIEWS")}
	ep.handleToolbarInput(ctx)
	main = ep.toolbar.layout.main
	if len(main.levels[0].buttons) != 1 {
		t.Errorf("expected only VIEWS in the toolbar while its menu is displayed, got %d buttons", len(main.levels[0].buttons))
	}
	altim := placedButtonFace(t, ep, main, "ALTIM\nSET")
	dept := placedButtonFace(t, ep, main, "DEPT\nLIST")
	if altim.P1[1] != bright.P1[1] || dept.P1[1] != views.P1[1] {
		t.Errorf("VIEWS menu rows at %v and %v, want the toolbar's rows at %v and %v",
			altim.P1[1], dept.P1[1], bright.P1[1], views.P1[1])
	}
	if altim.P0[0] != views.P1[0]+toolbarButtonGap+toolbarTabWidth+toolbarTabGap {
		t.Errorf("VIEWS menu starts at %v, want it just past the VIEWS button ending at %v", altim.P0[0], views.P1[0])
	}

	// A one-row menu stays on its button's row.
	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "CHECK\nLISTS")}
	ep.handleToolbarInput(ctx)
	pos := placedButtonFace(t, ep, ep.toolbar.layout.main, "POS\nCHECK")
	if pos.P1[1] != views.P1[1] {
		t.Errorf("CHECK LISTS menu at %v, want the second row at %v", pos.P1[1], views.P1[1])
	}
}

// A torn-off button's menu goes down from the button, and a menu opened
// from a torn-off button is laid out from that button rather than from
// where it sits in the toolbar.
func TestTornOffMenu(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	geoMap := toolbarButtonID(t, ep, "ZBW\nMAP")
	ps.TornOffButtons = map[string][2]float32{geoMap: {100, 600}, toolbarButtonID(t, ep, "MVA\n2"): {500, 300}}
	ep.handleToolbarInput(ctx)
	if n := len(ep.toolbar.layout.tornOff); n != 2 {
		t.Fatalf("expected 2 torn-off buttons placed, got %d", n)
	}
	for _, inst := range ep.toolbar.tornOff {
		if inst.id == geoMap {
			inst.open = []string{geoMap}
		}
	}
	ep.handleToolbarInput(ctx)
	var torn *placedInstance
	for i := range ep.toolbar.layout.tornOff {
		if ep.toolbar.layout.tornOff[i].instance.id == geoMap {
			torn = &ep.toolbar.layout.tornOff[i]
		}
	}
	root := placedButtonFace(t, ep, torn, "ZBW\nMAP")
	if root.P1[1] != 600 || root.P0[0] != 100+toolbarTabWidth+toolbarTabGap {
		t.Errorf("torn-off GeoMap button at %v, want its face just past a tear-off bar at (100, 600)", root)
	}
	first := placedButtonFace(t, ep, torn, "MIA\n1")
	if first.P1[1] != root.P1[1] || first.P0[0] != root.P1[0]+toolbarButtonGap+toolbarTabWidth+toolbarTabGap {
		t.Errorf("first filter at %v, want it on the torn-off button's row just past it at %v", first, root)
	}
}

// A torn-off button whose map isn't loaded isn't displayed but keeps its
// position for when it is.
func TestTornOffUnknownButton(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	ps.TornOffButtons = map[string][2]float32{"1.3/NOT LOADED": {100, 600}, toolbarButtonID(t, ep, "FONT"): {100, 500}}
	ep.handleToolbarInput(ctx)
	if n := len(ep.toolbar.layout.tornOff); n != 1 {
		t.Errorf("expected only FONT placed, got %d torn-off buttons", n)
	}
	if _, ok := ps.TornOffButtons["1.3/NOT LOADED"]; !ok {
		t.Error("the unplaced tear-off was dropped from the preferences")
	}
}

// The altitude limits panel is displayed beside the ALT LIM button that was
// clicked most recently, in the toolbar or torn off, never both.
func TestAltitudeLimitsPanel(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	altLim := toolbarButtonID(t, ep, altitudeLimitsButtonLabel(ep))
	ep.currentPrefs().TornOffButtons = map[string][2]float32{altLim: {100, 600}}
	ep.handleToolbarInput(ctx)

	open := func(inst *toolbarInstance) {
		pb := placedButton{button: ep.toolbarButtonByID(altLim), instance: inst}
		ep.toggleToolbarMenu(&pb)
		ep.handleToolbarInput(ctx)
	}
	open(&ep.toolbar.main)
	if len(ep.toolbar.layout.main.levels) != 2 || ep.toolbar.layout.main.levels[1].panel == nil {
		t.Fatal("the panel isn't displayed in the toolbar")
	}
	open(ep.toolbar.tornOff[0])
	if len(ep.toolbar.main.open) != 0 {
		t.Error("opening the torn-off button's panel left the toolbar's displayed")
	}
	if torn := ep.toolbar.layout.tornOff[0]; len(torn.levels) != 2 || torn.levels[1].panel == nil {
		t.Error("the panel isn't displayed beside the torn-off button")
	}
}

// [Enter] in the altitude limits sub-entry box locks a valid entry, brings
// the previous setting back when nothing was entered, and shows INVALID
// for anything else (3.14).
func TestAltitudeLimitsEnter(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	enter := func(buf string) {
		ep.altLimits.field, ep.altLimits.buf = altitudeLimitsCombined, buf
		ctx.Keyboard.Pressed = map[imgui.Key]any{imgui.KeyEnter: nil}
		ep.handleAltitudeLimitsKeyboard(ctx)
	}
	check := func(entry string, field altitudeLimitsField, invalid bool) {
		t.Helper()
		if ep.altLimits.field != field || ep.altLimits.invalid != invalid ||
			ps.AltitudeLimits.Targets != [2]int{100, 230} || ps.AltitudeLimits.LDBs != [2]int{100, 230} {
			t.Errorf("%q: got field %v, invalid %v, limits %+v", entry, ep.altLimits.field, ep.altLimits.invalid,
				ps.AltitudeLimits)
		}
	}

	enter("100B230")
	check("100B230", altitudeLimitsNone, false)
	enter("")
	check("", altitudeLimitsNone, false)
	enter("230B100")
	check("230B100", altitudeLimitsCombined, true)
}

func TestPressRepeat(t *testing.T) {
	var p pressRepeat
	start := time.Now()
	if !p.fire(start) {
		t.Fatal("the first press didn't fire")
	}
	if p.fire(start.Add(holdRepeatDelay / 2)) {
		t.Error("fired again before the hold delay")
	}
	if !p.fire(start.Add(holdRepeatDelay)) {
		t.Error("didn't fire after the hold delay")
	}
	if p.fire(start.Add(holdRepeatDelay + holdRepeatInterval/2)) {
		t.Error("fired again before the repeat interval")
	}
	if !p.fire(start.Add(holdRepeatDelay + holdRepeatInterval)) {
		t.Error("didn't repeat after the repeat interval")
	}
	p.release()
	if !p.fire(start.Add(holdRepeatDelay + holdRepeatInterval + time.Millisecond)) {
		t.Error("a new press after release didn't fire")
	}
}

func TestAdjust(t *testing.T) {
	v := 2
	if err := adjust(&v, toolbarClick{}, 0, 5, 2); err != nil || v != 0 {
		t.Errorf("lower: got %d, %v", v, err)
	}
	if err := adjust(&v, toolbarClick{}, 0, 5, 2); err != errToolbarLimit || v != 0 {
		t.Errorf("lower past the limit: got %d, %v", v, err)
	}
	if err := adjust(&v, tertiaryClick(), 0, 5, 2); err != nil || v != 2 {
		t.Errorf("raise: got %d, %v", v, err)
	}
}

// A torn-off GeoMap filter from the menu's second page is displayed whether
// or not that page is.
func TestTornOffFilterFromSecondPage(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	for i := range 25 {
		ep.allVideoMaps = append(ep.allVideoMaps, videomaps.ERAMMap{LabelLine1: "F", LabelLine2: fmt.Sprint(i)})
	}
	ep.currentPrefs().TornOffButtons = map[string][2]float32{toolbarButtonID(t, ep, "F\n22"): {100, 600}}
	ep.handleToolbarInput(ctx)
	if n := len(ep.toolbar.layout.tornOff); n != 1 {
		t.Fatalf("expected the torn-off filter to be placed, got %d torn-off buttons", n)
	}
	placedButtonFace(t, ep, &ep.toolbar.layout.tornOff[0], "F\n22")
}

// labelFirstLines returns the first line of each button's text.
func labelFirstLines(ep *Scope, buttons []placedButton) []string {
	var lines []string
	for _, pb := range buttons {
		lines = append(lines, strings.SplitN(pb.button.text(ep), "\n", 2)[0])
	}
	return lines
}

// A menu displayed from another menu replaces that menu's buttons in the
// columns past its own button's (figures 2-22, 3-12, and 3-19), and its
// background takes the clicks that land on it where it has no button.
func TestSubmenuReplacesColumns(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ep.bcgNames = []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K"}
	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "BRIGHT"), toolbarButtonID(t, ep, "MAP\nBRIGHT")}
	ep.handleToolbarInput(ctx)
	main := ep.toolbar.layout.main
	if len(main.levels) != 3 {
		t.Fatalf("expected BRIGHT and MAP BRIGHT displayed, got %d levels", len(main.levels))
	}
	bright, mapBright := &main.levels[1], &main.levels[2]
	if got := labelFirstLines(ep, bright.buttons); !slices.Equal(got, []string{"MAP", "BCKLGHT"}) {
		t.Errorf("BRIGHT menu shows %q with MAP BRIGHT's menu displayed, want MAP BRIGHT and BCKLGHT", got)
	}
	if bright.extent.P1[0] > mapBright.extent.P0[0] {
		t.Errorf("BRIGHT menu background %v reaches under MAP BRIGHT's %v", bright.extent, mapBright.extent)
	}
	// MAP BRIGHT's second row has only K; under E is its empty background.
	empty := mapBright.buttons[4].face.Center()
	empty[1] -= toolbarRowPitch
	if hit, _ := main.hit(empty); hit.button != nil || hit.menu != mapBright {
		t.Errorf("click on MAP BRIGHT's background at %v hit %+v", empty, hit)
	}

	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "ATC\nTOOLS"), toolbarButtonID(t, ep, "WX")}
	ep.handleToolbarInput(ctx)
	atcTools := ep.toolbar.layout.main.levels[1]
	if got := labelFirstLines(ep, atcTools.buttons); !slices.Equal(got, []string{"CRR", "SPEED", "WX"}) {
		t.Errorf("ATC TOOLS menu shows %q with the WX menu displayed, want all three of its buttons", got)
	}
}

// The check lists are separate views (4.3.6, 4.3.7): displaying one
// leaves the other and its popup menu displayed, and hiding one closes its
// popup menu.
func TestCheckListsAreSeparateViews(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ps := ep.currentPrefs()
	click := func(text string) {
		t.Helper()
		if err := ep.toolbarButtonByID(toolbarButtonID(t, ep, text)).click(ep, toolbarClick{}); err != nil {
			t.Fatal(err)
		}
	}

	click("POS\nCHECK")
	ep.popup = &checkListPopup{popupBase: popupBase{viewID: posCheckList.viewID}, list: &posCheckList}
	click("EMERG\nCHECK")
	if !ps.PosCheck.Visible || !ps.EmergCheck.Visible || ep.popup == nil {
		t.Errorf("after EMERG CHECK: POS CHECK displayed %v, EMERG CHECK displayed %v, POS CHECK popup %v; want all",
			ps.PosCheck.Visible, ps.EmergCheck.Visible, ep.popup != nil)
	}
	click("POS\nCHECK")
	if ps.PosCheck.Visible || !ps.EmergCheck.Visible || ep.popup != nil {
		t.Errorf("after POS CHECK again: POS CHECK displayed %v, EMERG CHECK displayed %v, POS CHECK popup %v; want only EMERG CHECK",
			ps.PosCheck.Visible, ps.EmergCheck.Visible, ep.popup != nil)
	}
}

// MASTER TOOLBAR hides and shows the main toolbar.
func TestMasterToolbarButton(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	button := ep.toolbarButtonByID(toolbarButtonID(t, ep, "MASTER\nTOOLBAR"))
	for _, displayed := range []bool{false, true} {
		if err := button.click(ep, toolbarClick{}); err != nil {
			t.Fatal(err)
		}
		ep.handleToolbarInput(ctx)
		if (ep.toolbar.layout.main != nil) != displayed {
			t.Errorf("main toolbar displayed: %v, want %v", ep.toolbar.layout.main != nil, displayed)
		}
	}
}

// MASTER RAISE puts the main toolbar over the torn-off buttons and the
// views, and reads MASTER LOWER while it does; lowered, the toolbar is
// under both.
func TestMasterRaise(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	ep.handleToolbarInput(ctx)
	bright := placedButtonFace(t, ep, ep.toolbar.layout.main, "BRIGHT")
	// FONT torn off and dropped exactly over BRIGHT.
	ps.TornOffButtons = map[string][2]float32{toolbarButtonID(t, ep, "FONT"): {bright.P0[0] - toolbarTabWidth - toolbarTabGap, bright.P1[1]}}
	raise := ep.toolbarButtonByID(toolbarButtonID(t, ep, "MASTER\nRAISE"))
	over := bright.Center()

	ep.handleToolbarInput(ctx)
	if hit := ep.toolbarHitAt(over); hit.button == nil || hit.button.button.text(ep) != "FONT" {
		t.Errorf("lowered: the torn-off FONT isn't over BRIGHT: %+v", hit)
	}
	ps.TornOffButtons = nil
	ep.viewExtents = []math.Extent2D{{P0: bright.P0, P1: bright.P1}}
	ep.handleToolbarInput(ctx)
	if hit := ep.toolbarHitAt(over); hit.button != nil {
		t.Errorf("lowered: a view over BRIGHT doesn't cover it: %+v", hit)
	}

	if err := raise.click(ep, toolbarClick{}); err != nil {
		t.Fatal(err)
	}
	if text := raise.text(ep); text != "MASTER\nLOWER" {
		t.Errorf("raised button reads %q", text)
	}
	ps.TornOffButtons = map[string][2]float32{toolbarButtonID(t, ep, "FONT"): {bright.P0[0] - toolbarTabWidth - toolbarTabGap, bright.P1[1]}}
	ep.viewExtents = []math.Extent2D{{P0: bright.P0, P1: bright.P1}}
	ep.handleToolbarInput(ctx)
	if hit := ep.toolbarHitAt(over); hit.button == nil || hit.button.button.text(ep) != "BRIGHT" {
		t.Errorf("raised: BRIGHT isn't over the torn-off FONT and the view: %+v", hit)
	}
}

// clickToolbarAt clicks and releases the given logical mouse button at p.
func clickToolbarAt(ep *Scope, ctx *scope.Context, p [2]float32, button imgui.MouseButton) {
	ctx.Mouse = &platform.MouseState{Pos: p}
	ctx.Mouse.Clicked[button] = true
	ep.handleToolbarInput(ctx)
	ctx.Mouse = &platform.MouseState{Pos: p}
	ctx.Mouse.Released[button] = true
	ep.handleToolbarInput(ctx)
	ctx.Mouse = nil
}

// tornOffButton returns the torn-off button with the given text.
func tornOffButton(t *testing.T, ep *Scope, text string) placedButton {
	t.Helper()
	for _, pi := range ep.toolbar.layout.tornOff {
		if pb := pi.levels[0].buttons[0]; pb.button.text(ep) == text {
			return pb
		}
	}
	t.Fatalf("no torn-off button %q", text)
	return placedButton{}
}

// DELETE TEAROFF deletes the torn-off button that is middle-clicked along
// with those picked with left clicks (2.5.1.2).
func TestDeleteTearoff(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	font, bright, cursor := toolbarButtonID(t, ep, "FONT"), toolbarButtonID(t, ep, "BRIGHT"), toolbarButtonID(t, ep, "CURSOR")
	ps.TornOffButtons = map[string][2]float32{font: {100, 600}, bright: {100, 500}, cursor: {100, 400}}
	ep.handleToolbarInput(ctx)
	deleteTearoff := placedButtonFace(t, ep, ep.toolbar.layout.main, "DELETE\nTEAROFF").Center()
	check := func(when string, deleting bool, torn ...string) {
		t.Helper()
		if ep.toolbar.deleting != deleting {
			t.Errorf("%s: DELETE TEAROFF active %v, want %v", when, ep.toolbar.deleting, deleting)
		}
		if len(ps.TornOffButtons) != len(torn) {
			t.Errorf("%s: torn off %v, want %v", when, ps.TornOffButtons, torn)
		}
		for _, id := range torn {
			if _, ok := ps.TornOffButtons[id]; !ok {
				t.Errorf("%s: %q was deleted", when, id)
			}
		}
	}

	clickToolbarAt(ep, ctx, deleteTearoff, platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, tornOffButton(t, ep, "FONT").face.Center(), platform.MouseButtonTertiary)
	check("middle click on FONT", false, bright, cursor)

	clickToolbarAt(ep, ctx, deleteTearoff, platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, tornOffButton(t, ep, "BRIGHT").face.Center(), platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, [2]float32{800, 200}, platform.MouseButtonTertiary)
	check("middle click away from the torn-off buttons", true, bright, cursor)
	clickToolbarAt(ep, ctx, tornOffButton(t, ep, "CURSOR").face.Center(), platform.MouseButtonTertiary)
	check("BRIGHT picked and middle click on CURSOR", false)
}

// captureMousePlatform stands in for the platform, which captures the
// mouse while a button is torn off or moved.
type captureMousePlatform struct{ platform.Platform }

func (captureMousePlatform) StartCaptureMouse(math.Extent2D) {}
func (captureMousePlatform) EndCaptureMouse()                {}

// VECTOR's tear-off bar moves the one VECTOR button out of the main
// toolbar rather than tearing off a copy, and dropping it on the main
// toolbar puts it back (2.5.1, 3.9). DELETE TEAROFF doesn't delete it.
func TestVectorMoves(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ctx.Platform = captureMousePlatform{}
	ps := ep.currentPrefs()
	vector := vectorButton()
	text := vector.text(ep)
	id := toolbarButtonID(t, ep, text)
	inMain := func() bool {
		return slices.ContainsFunc(ep.toolbar.layout.main.levels[0].buttons,
			func(pb placedButton) bool { return pb.button.id == id })
	}
	ep.handleToolbarInput(ctx)
	tab := ep.toolbar.layout.main.levels[0].find(id).tab

	clickToolbarAt(ep, ctx, tab.Center(), platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, [2]float32{300, 400}, platform.MouseButtonPrimary)
	ep.handleToolbarInput(ctx)
	if _, ok := ps.TornOffButtons[id]; !ok || inMain() {
		t.Fatalf("VECTOR wasn't moved out of the main toolbar: torn off %v, in the toolbar %v", ps.TornOffButtons, inMain())
	}
	moved := tornOffButton(t, ep, text)

	clickToolbarAt(ep, ctx, placedButtonFace(t, ep, ep.toolbar.layout.main, "DELETE\nTEAROFF").Center(),
		platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, moved.face.Center(), platform.MouseButtonTertiary)
	if _, ok := ps.TornOffButtons[id]; !ok {
		t.Error("DELETE TEAROFF deleted VECTOR")
	}
	ep.endDeleteTearoffs()

	clickToolbarAt(ep, ctx, moved.tab.Center(), platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, [2]float32{1500, 860}, platform.MouseButtonPrimary)
	ep.handleToolbarInput(ctx)
	if _, ok := ps.TornOffButtons[id]; ok || !inMain() {
		t.Errorf("VECTOR dropped on the main toolbar wasn't put back: torn off %v, in the toolbar %v",
			ps.TornOffButtons, inMain())
	}
}

// Tear-off bars are as in the figures: none on the CURSOR menu's buttons
// (figure 3-16) or the WX menu's (figures 2-24 and 3-12), and one on
// BCKLGHT but not BUTTON (figures 2-22, 3-17, and 3-19).
func TestTearoffBars(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	// tabs opens the menus with the given buttons and returns which of the
	// last one's buttons have tear-off bars, by their labels' first lines.
	tabs := func(open ...string) map[string]bool {
		ep.toolbar.main.open = nil
		for _, text := range open {
			ep.toolbar.main.open = append(ep.toolbar.main.open, toolbarButtonID(t, ep, text))
		}
		ep.handleToolbarInput(ctx)
		levels := ep.toolbar.layout.main.levels
		buttons := levels[len(levels)-1].buttons
		m := make(map[string]bool)
		for i, line := range labelFirstLines(ep, buttons) {
			m[line] = !buttons[i].tab.IsEmpty()
		}
		return m
	}

	for name, tab := range tabs("CURSOR") {
		if tab {
			t.Errorf("CURSOR menu's %s has a tear-off bar", name)
		}
	}
	for name, tab := range tabs("ATC\nTOOLS", "WX") {
		if tab {
			t.Errorf("WX menu's %s has a tear-off bar", name)
		}
	}
	bright := tabs("BRIGHT")
	if !bright["BCKLGHT"] || bright["BUTTON"] || bright["BCKGRD"] {
		t.Errorf("BRIGHT menu tear-off bars: BCKLGHT %v, BUTTON %v, BCKGRD %v; want only BCKLGHT's",
			bright["BCKLGHT"], bright["BUTTON"], bright["BCKGRD"])
	}
}

// The LINE 4 brightness keeps its sign at zero (figure 3-17).
func TestLine4BrightnessLabel(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ep.currentPrefs().Brightness.Line4 = 0
	toolbarButtonID(t, ep, "LINE 4\n-0")
	ep.currentPrefs().Brightness.Line4 = 6
	toolbarButtonID(t, ep, "LINE 4\n-6")
}

// PREFSET shows the initials of the controller signed in (3.16).
func TestPrefsetInitials(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ep.initials = "PC"
	toolbarButtonID(t, ep, "PREFSET\nPC")
}

// lockingPlatform records the extent the cursor is locked in.
type lockingPlatform struct {
	platform.Platform
	locked *math.Extent2D
}

func (p lockingPlatform) StartCaptureMouse(e math.Extent2D) { *p.locked = e }
func (p lockingPlatform) EndCaptureMouse()                  { *p.locked = math.Extent2D{} }

// A press on CODE keeps its override in effect, with the cursor locked on
// the button, until the press is released, another trackball button is
// pressed, or the Clear key is pressed (5.1.5.5).
func TestHoldButton(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	var locked math.Extent2D
	ctx.Platform = lockingPlatform{locked: &locked}
	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "DB\nFIELDS")}
	ep.handleToolbarInput(ctx)
	code := placedButtonFace(t, ep, ep.toolbar.layout.main, "CODE")

	press := func(p [2]float32, button imgui.MouseButton) {
		ctx.Mouse = &platform.MouseState{Pos: p}
		ctx.Mouse.Clicked[button] = true
		ep.handleToolbarInput(ctx)
	}
	hold := func(p [2]float32) {
		ctx.Mouse = &platform.MouseState{Pos: p}
		ep.handleToolbarInput(ctx)
	}
	check := func(when string, held bool) {
		t.Helper()
		if ep.dbOverrideHeld(overrideCode) != held || locked.IsEmpty() == held {
			t.Errorf("%s: override held %v with the cursor locked in %v, want held %v",
				when, ep.dbOverrideHeld(overrideCode), locked, held)
		}
	}

	press(code.Center(), platform.MouseButtonPrimary)
	check("pressed", true)
	if h := ctx.DrawExtent.Height(); locked.P0[0] != code.P0[0] || locked.P0[1] != h-code.P1[1] {
		t.Errorf("cursor locked in %v, want CODE's face %v in window coordinates", locked, code)
	}
	hold([2]float32{800, 400})
	check("held off the button", true)
	if ep.dbOverrideHeld(overrideSpeed) || ep.dbOverrideHeld(overrideVRI) {
		t.Error("holding CODE holds another override")
	}
	ctx.Mouse = &platform.MouseState{Pos: code.Center()}
	ctx.Mouse.Released[platform.MouseButtonPrimary] = true
	ep.handleToolbarInput(ctx)
	check("released", false)

	press(code.Center(), platform.MouseButtonTertiary)
	check("pressed with TBE", true)
	press(code.Center(), platform.MouseButtonPrimary)
	check("TBP pressed too", false)

	press(code.Center(), platform.MouseButtonPrimary)
	if !ep.cancelToolbarGesture(ctx) {
		t.Error("Clear didn't report the override")
	}
	check("Clear pressed", false)
}

// TBP on TOOLBAR displays its menu horizontally, to its right, and TBE
// vertically: hanging below it, each row a column (2.3.1).
func TestToolbarControlMenuVertical(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ep.currentPrefs().MasterToolbarPosition = [2]float32{200, 600}
	ep.handleToolbarInput(ctx)
	toolbar := ep.toolbar.layout.control.levels[0].buttons[0].face

	clickToolbarAt(ep, ctx, toolbar.Center(), platform.MouseButtonTertiary)
	ep.handleToolbarInput(ctx)
	control := &ep.toolbar.layout.control
	master := placedButtonFace(t, ep, control, "MASTER\nTOOLBAR")
	mca := placedButtonFace(t, ep, control, "MCA\nTOOLBAR")
	raise := placedButtonFace(t, ep, control, "MASTER\nRAISE")
	if master.P0[0] != toolbar.P0[0] || master.P1[1] != toolbar.P1[1]-toolbarRowPitch {
		t.Errorf("vertical: MASTER TOOLBAR at %v, want it below TOOLBAR at %v", master, toolbar)
	}
	if mca.P0[0] != master.P0[0] || mca.P1[1] != master.P1[1]-toolbarRowPitch {
		t.Errorf("vertical: MCA TOOLBAR at %v, want it below MASTER TOOLBAR at %v", mca, master)
	}
	if raise.P1[1] != master.P1[1] || raise.P0[0] != master.P0[0]+toolbarSlotWidth {
		t.Errorf("vertical: MASTER RAISE at %v, want it right of MASTER TOOLBAR at %v", raise, master)
	}

	clickToolbarAt(ep, ctx, toolbar.Center(), platform.MouseButtonTertiary)
	clickToolbarAt(ep, ctx, toolbar.Center(), platform.MouseButtonPrimary)
	ep.handleToolbarInput(ctx)
	master = placedButtonFace(t, ep, &ep.toolbar.layout.control, "MASTER\nTOOLBAR")
	if master.P1[1] != toolbar.P1[1] || master.P0[0] <= toolbar.P1[0] {
		t.Errorf("horizontal: MASTER TOOLBAR at %v, want it right of TOOLBAR at %v", master, toolbar)
	}
}

// A tear-off dropped on the master toolbar docks in its slots past its own
// buttons, taking the slot it is dropped on and moving those after it along;
// a tear-off of one of the master toolbar's own buttons dropped on it goes
// away. While a docked button's menu is displayed, the master's other
// buttons are hidden, and DELETE TEAROFF deletes docked buttons (2.3.2,
// 2.5.1).
func TestDockInMasterToolbar(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ctx.Platform = captureMousePlatform{}
	ps := ep.currentPrefs()
	dest, typ, font := toolbarButtonID(t, ep, "DEST"), toolbarButtonID(t, ep, "TYPE"), toolbarButtonID(t, ep, "FONT")
	slots := masterSlots(ctx, mainToolbarOrigin(ctx))

	// tearOff opens DB FIELDS, tears the button with the given text off by
	// its bar, and drops it at p.
	tearOff := func(text string, p [2]float32) {
		t.Helper()
		ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "DB\nFIELDS")}
		ep.handleToolbarInput(ctx)
		var tab math.Extent2D
		for _, level := range ep.toolbar.layout.main.levels {
			if pb := level.find(toolbarButtonID(t, ep, text)); pb != nil && !pb.handle {
				tab = pb.tab
			}
		}
		clickToolbarAt(ep, ctx, tab.Center(), platform.MouseButtonPrimary)
		ep.toolbar.main.open = nil
		clickToolbarAt(ep, ctx, p, platform.MouseButtonPrimary)
		ep.handleToolbarInput(ctx)
	}
	docked := func() []string { return ps.DockedButtons[masterDock] }

	tearOff("DEST", slots[3].Center())
	if !slices.Equal(docked(), []string{dest}) {
		t.Fatalf("dropped past the end: docked %v, want DEST", docked())
	}
	copyFace := placedButtonFace(t, ep, ep.toolbar.layout.main, "DEST")
	if copyFace.P1[0] != slots[0].P1[0] || copyFace.P1[1] != slots[0].P1[1] {
		t.Errorf("docked DEST at %v, want it in the first slot %v", copyFace, slots[0])
	}
	tearOff("TYPE", slots[0].Center())
	if !slices.Equal(docked(), []string{typ, dest}) {
		t.Errorf("dropped on DEST: docked %v, want TYPE then DEST", docked())
	}

	ep.toolbar.main.open = []string{toolbarButtonID(t, ep, "VIEWS")}
	ep.handleToolbarInput(ctx)
	if slices.ContainsFunc(ep.toolbar.layout.main.levels[0].buttons, func(pb placedButton) bool { return pb.handle }) {
		t.Error("docked buttons are displayed while the VIEWS menu is")
	}
	ep.toolbar.main.open = nil
	ep.handleToolbarInput(ctx)

	// FONT torn off and dropped back on the master toolbar goes away.
	ps.TornOffButtons = map[string][2]float32{font: {100, 500}}
	ep.handleToolbarInput(ctx)
	clickToolbarAt(ep, ctx, tornOffButton(t, ep, "FONT").tab.Center(), platform.MouseButtonPrimary)
	clickToolbarAt(ep, ctx, slots[5].Center(), platform.MouseButtonPrimary)
	if ep.isTornOff(font) || len(docked()) != 2 {
		t.Errorf("FONT dropped on the master toolbar: torn off %v, docked %v", ep.isTornOff(font), docked())
	}

	ep.handleToolbarInput(ctx)
	clickToolbarAt(ep, ctx, placedButtonFace(t, ep, ep.toolbar.layout.main, "DELETE\nTEAROFF").Center(),
		platform.MouseButtonPrimary)
	var typeCopy math.Extent2D
	for _, pb := range ep.toolbar.layout.main.levels[0].buttons {
		if pb.handle && pb.button.id == typ {
			typeCopy = pb.face
		}
	}
	clickToolbarAt(ep, ctx, typeCopy.Center(), platform.MouseButtonTertiary)
	if !slices.Equal(docked(), []string{dest}) {
		t.Errorf("after deleting TYPE: docked %v, want DEST", docked())
	}
}

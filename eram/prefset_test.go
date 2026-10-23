// eram/prefset_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
)

func prefSetNames(ep *Scope) []string {
	var names []string
	for _, p := range ep.prefSet.Saved {
		names = append(names, p.Name)
	}
	return names
}

// A set is saved under a name of 2 to 10 characters; saving under an
// existing name updates that set, and there may be at most 20 (3.16.1,
// 3.16.3).
func TestPrefSetSave(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ps := ep.currentPrefs()

	for _, bad := range []string{"", "A", "ABCDEFGHIJK"} {
		if ep.savePrefSet(bad) {
			t.Errorf("saved under %q", bad)
		}
	}
	ps.Range = 100
	ep.savePrefSet("NORTH")
	ep.savePrefSet("SOUTH")
	ps.Range = 200
	ep.savePrefSet("NORTH")
	if names := prefSetNames(ep); !slices.Equal(names, []string{"NORTH", "SOUTH"}) {
		t.Fatalf("saved %q", names)
	}
	if r := ep.prefSet.Saved[0].Range; r != 200 {
		t.Errorf("NORTH has range %v after saving it again, want 200", r)
	}

	for i := len(ep.prefSet.Saved); i < maxPrefSets; i++ {
		if !ep.savePrefSet(fmt.Sprintf("SET%d", i)) {
			t.Fatalf("set %d wasn't saved", i)
		}
	}
	if ep.savePrefSet("ONEMORE") {
		t.Errorf("saved a set past the %d allowed", maxPrefSets)
	}
	if !ep.savePrefSet("SOUTH") {
		t.Errorf("couldn't update a set with %d saved", maxPrefSets)
	}
}

// Invoking a set restores its settings, which aren't changed by changes to
// the current ones, but leaves the check lists displayed or suppressed and
// the mouse buttons as they are.
func TestPrefSetApply(t *testing.T) {
	ep, _ := makeToolbarTestScope()
	ps := ep.currentPrefs()
	ps.Range = 100
	ps.VideoMapGroup = "LOW"
	ps.VideoMapVisible["MIA 1"] = nil
	ep.savePrefSet("LOW")

	ps.Range = 300
	ps.VideoMapGroup = "HIGH"
	ps.EmergCheck.Visible = true
	ps.UseRightClick = true
	delete(ps.VideoMapVisible, "MIA 1")

	group := ep.applyPrefSet(0)
	if group != "LOW" || ps.VideoMapGroup != "HIGH" {
		t.Errorf("invoked GeoMap %q and current one %q, want LOW to be loaded in place of HIGH", group, ps.VideoMapGroup)
	}
	if _, ok := ps.VideoMapVisible["MIA 1"]; !ok || ps.Range != 100 {
		t.Errorf("invoking gave range %v and maps %v", ps.Range, ps.VideoMapVisible)
	}
	if !ps.EmergCheck.Visible || !ps.UseRightClick {
		t.Error("invoking changed the check list's display or the mouse buttons")
	}
	delete(ps.VideoMapVisible, "MIA 1")
	if _, ok := ep.prefSet.Saved[0].VideoMapVisible["MIA 1"]; !ok {
		t.Error("changing the current maps changed the saved set")
	}
}

// A set holds the menus displayed in the toolbars and the direction the
// toolbar control button displays its menus in, which invoking it restores
// (Table A-2); the current settings keep them when vice is restarted.
func TestPrefSetToolbarMenus(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ps := ep.currentPrefs()
	views := []string{toolbarButtonID(t, ep, "VIEWS")}
	ep.setOpenMenus(&ep.toolbar.main, views)
	ep.setOpenMenus(&ep.toolbar.control, []string{toolbarControlID})
	ps.ToolbarControlVertical = true
	ep.savePrefSet("AB")

	ep.setOpenMenus(&ep.toolbar.main, nil)
	ep.setOpenMenus(&ep.toolbar.control, nil)
	ps.ToolbarControlVertical = false
	ep.applyPrefSet(0)
	ep.handleToolbarInput(ctx)
	if open := ep.openMenus(&ep.toolbar.main); !slices.Equal(open, views) {
		t.Errorf("after invoking the set, the main toolbar displays %q, want %q", open, views)
	}
	if open := ep.openMenus(&ep.toolbar.control); !slices.Equal(open, []string{toolbarControlID}) ||
		!ep.toolbar.control.vertical {
		t.Errorf("after invoking the set, TOOLBAR displays %q, vertically %v", open, ep.toolbar.control.vertical)
	}

	b, err := json.Marshal(ep.prefSet)
	if err != nil {
		t.Fatal(err)
	}
	var restored PrefrenceSet
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if r := restored.Current; !slices.Equal(r.OpenToolbarMenus[toolbarMainID], views) || !r.ToolbarControlVertical {
		t.Errorf("restored menus %q, TOOLBAR vertical %v", r.OpenToolbarMenus, r.ToolbarControlVertical)
	}
}

// SAVE takes a typed name, or the name of a set clicked; DELETE picks sets
// with TBP and deletes them with TBE on the last one (3.16).
func TestPrefSetMenu(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ep.initials = "MP"
	ctx.Mouse = &platform.MouseState{}
	panel := prefSetPanel{}
	click := func(row int, tertiary bool) {
		l := ep.prefSetMenuLayoutAt([2]float32{})
		rows := append(append([]math.Extent2D{l.save}, l.sets...), l.del)
		ctx.Mouse.Clicked = [platform.MouseButtonCount]bool{}
		ctx.Mouse.Clicked[platform.MouseButtonPrimary] = !tertiary
		ctx.Mouse.Clicked[platform.MouseButtonTertiary] = tertiary
		panel.click(ep, ctx, [2]float32{}, rows[row].Center())
	}
	typed := func(s string, key imgui.Key) {
		ctx.Keyboard.Input = s
		ctx.Keyboard.Pressed = map[imgui.Key]any{key: nil}
		ep.handlePrefSetKeyboard(ctx)
		ctx.Keyboard.Input, ctx.Keyboard.Pressed = "", nil
	}

	for _, name := range []string{"ab", "cd", "ef"} {
		click(0, false)
		typed(name, imgui.KeyEnter)
	}
	if names := prefSetNames(ep); !slices.Equal(names, []string{"AB", "CD", "EF"}) {
		t.Fatalf("saved %q", names)
	}

	click(0, false)
	typed("x", imgui.KeyEnter)
	if !ep.prefSetMenu.invalid {
		t.Error("a one-character name wasn't rejected")
	}
	click(2, false)
	typed("", imgui.KeyEnter)
	if ep.prefSetMenu.mode != prefSetInvoke || len(ep.prefSet.Saved) != 3 {
		t.Errorf("saving under a clicked name left mode %v and sets %q", ep.prefSetMenu.mode, prefSetNames(ep))
	}

	click(4, false) // DELETE
	click(1, false)
	click(3, true)
	if names := prefSetNames(ep); !slices.Equal(names, []string{"CD"}) {
		t.Errorf("deleting AB and EF left %q", names)
	}
	if ep.prefSetMenu.mode != prefSetInvoke || ep.feedbackArea.msg != "PREFERENCE SET(S) DELETED FOR MP" {
		t.Errorf("deleting left mode %v and feedback %q", ep.prefSetMenu.mode, ep.feedbackArea.msg)
	}
}

// The CLEAR key and the function keys close the menu, whatever it is doing
// (3.16.5).
func TestPrefSetMenuClose(t *testing.T) {
	ep, ctx := makeToolbarTestScope()
	ep.initials = "MP"
	id := toolbarButtonID(t, ep, "PREFSET\nMP")
	for _, key := range []imgui.Key{imgui.KeyEscape, imgui.KeyF1} {
		for _, mode := range []prefSetMode{prefSetInvoke, prefSetSave, prefSetDelete} {
			ep.setOpenMenus(&ep.toolbar.main, []string{id})
			ep.prefSetMenu = prefSetMenuState{mode: mode, name: "AB"}
			ctx.Keyboard.Pressed = map[imgui.Key]any{key: nil}
			ep.handlePrefSetKeyboard(ctx)
			if len(ep.openMenus(&ep.toolbar.main)) != 0 || ep.prefSetMenu.mode != prefSetInvoke || ep.prefSetMenu.name != "" {
				t.Errorf("key %v in mode %v: open %q, mode %v, name %q", key, mode,
					ep.openMenus(&ep.toolbar.main), ep.prefSetMenu.mode, ep.prefSetMenu.name)
			}
		}
	}
}

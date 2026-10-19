// eram/prefset.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/brunoga/deep"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// The PREFSET menu saves the display settings as a named preference set,
// invokes a saved set, and deletes sets (3.16). The sets are kept for each
// ARTCC; vice has no passwords for them.

const (
	maxPrefSets         = 20
	minPrefSetNameChars = 2
	maxPrefSetNameChars = 10
)

// prefSetMode is what a click on a preference set in the PREFSET menu does.
type prefSetMode int

const (
	prefSetInvoke prefSetMode = iota
	// prefSetSave enters the clicked set's name as the one to save under.
	prefSetSave
	// prefSetDelete picks the clicked set for deletion with TBP and deletes
	// it along with the picked ones with TBE.
	prefSetDelete
)

// prefSetMenuState is the transient state of the PREFSET menu.
type prefSetMenuState struct {
	mode    prefSetMode
	name    string // the name being entered to save under
	invalid bool   // the name was rejected by Enter; INVALID is displayed
	picked  map[string]struct{}
}

// savePrefSet saves the current settings under name, replacing the set of
// that name if there is one. It reports false for a name that isn't 2 to 10
// characters long, or for a new set when there are already 20.
func (ep *Scope) savePrefSet(name string) bool {
	if len(name) < minPrefSetNameChars || len(name) > maxPrefSetNameChars {
		return false
	}
	p := deep.MustCopy(ep.prefSet.Current)
	p.Name = name
	saved := &ep.prefSet.Saved
	if i := slices.IndexFunc(*saved, func(s Preferences) bool { return s.Name == name }); i >= 0 {
		(*saved)[i] = p
		return true
	}
	if len(*saved) >= maxPrefSets {
		return false
	}
	*saved = append(*saved, p)
	return true
}

// invokePrefSet makes a saved preference set the current settings, along
// with its GeoMap if the scenario has it.
func (ep *Scope) invokePrefSet(ctx *scope.Context, i int) {
	group := ep.applyPrefSet(i)
	vmf, err := ctx.Client.LoadVideoMapLibrary(ctx.Client.State.ControllerVideoMapFile)
	if err != nil {
		ctx.Lg.Errorf("%v", err)
		return
	}
	if _, ok := vmf.ERAMMapGroups[group]; ok {
		ep.setVideoMapGroup(vmf, group)
	}
}

// applyPrefSet makes a saved preference set's settings the current ones
// but for its GeoMap, which it returns. The check lists stay displayed or
// suppressed as they are (Appendix A), and the vice mouse button setting
// stays as it is.
func (ep *Scope) applyPrefSet(i int) (group string) {
	cur := &ep.prefSet.Current
	p := deep.MustCopy(ep.prefSet.Saved[i])
	p.EmergCheck.Visible, p.PosCheck.Visible = cur.EmergCheck.Visible, cur.PosCheck.Visible
	p.UseRightClick = cur.UseRightClick
	group, p.VideoMapGroup = p.VideoMapGroup, cur.VideoMapGroup
	*cur = p
	return group
}

// deletePrefSets deletes the named sets.
func (ep *Scope) deletePrefSets(names map[string]struct{}) {
	ep.prefSet.Saved = util.FilterSlice(ep.prefSet.Saved, func(p Preferences) bool {
		_, ok := names[p.Name]
		return !ok
	})
	ep.feedbackArea.Success("PREFERENCE SET(S) DELETED FOR " + ep.initials)
}

// prefSetPanel is the PREFSET menu, which the PREFSET button displays
// beside itself: SAVE, the saved sets, and DELETE (figure 3-40).
type prefSetPanel struct{}

// prefSetMenuLayout is the PREFSET menu's geometry: its extent and its
// rows.
type prefSetMenuLayout struct {
	extent    math.Extent2D
	save, del math.Extent2D
	sets      []math.Extent2D
	invalidX  float32 // where INVALID is displayed
}

// prefSetMenuPad separates the menu's text from the edges of its rows.
const prefSetMenuPad = float32(4)

func (ep *Scope) prefSetMenuLayoutAt(anchor [2]float32) prefSetMenuLayout {
	font := ep.ERAMToolbarFont()
	// As in figure 3-40, the rows are half as tall as a button and the menu
	// is half again as wide.
	rowH := toolbarFaceHeight / 2
	width := max(1.5*toolbarFaceWidth, maxPrefSetNameChars*charWidth(font)+2*prefSetMenuPad)
	invalidX := anchor[0] + width
	if ep.prefSetMenu.invalid {
		width += font.LayoutBounds(altitudeLimitsInvalid, 0).Width() + prefSetMenuPad
	}
	row := func(i int) math.Extent2D {
		top := anchor[1] - float32(i)*rowH
		return math.Extent2D{P0: [2]float32{anchor[0], top - rowH}, P1: [2]float32{anchor[0] + width, top}}
	}

	l := prefSetMenuLayout{save: row(0), invalidX: invalidX}
	for i := range ep.prefSet.Saved {
		l.sets = append(l.sets, row(i+1))
	}
	l.del = row(len(ep.prefSet.Saved) + 1)
	l.extent = math.Extent2D{P0: l.del.P0, P1: l.save.P1}
	return l
}

func (prefSetPanel) extent(ep *Scope, anchor [2]float32) math.Extent2D {
	return ep.prefSetMenuLayoutAt(anchor).extent
}

func (prefSetPanel) draw(ep *Scope, ctx *scope.Context, cb *renderer.CommandBuffer, anchor [2]float32) {
	ps := ep.currentPrefs()
	m := &ep.prefSetMenu
	font := ep.ERAMToolbarFont()
	l := ep.prefSetMenuLayoutAt(anchor)

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	text := ps.Brightness.Text.ScaleRGB(colors.toolbar.text)
	inkCenter := font.InkBounds("SAVE", 0).Center()[1]
	row := func(e math.Extent2D, bg renderer.RGB, label string, color renderer.RGB) {
		addQuad(trid, e, ps.Brightness.Button.ScaleRGB(bg))
		td.AddText(label, [2]float32{e.P0[0] + prefSetMenuPad, e.Center()[1] - inkCenter},
			renderer.TextStyle{Font: font, Color: color})
	}

	if m.mode == prefSetSave {
		row(l.save, renderer.RGB{}, m.name, text)
		ld.AddLineLoop(colors.toolbar.hoveredOutline, l.save.Corners())
		if m.invalid {
			td.AddText(altitudeLimitsInvalid, [2]float32{l.invalidX, l.save.Center()[1] - inkCenter},
				renderer.TextStyle{Font: font, Color: ps.Brightness.Text.ScaleRGB(colors.yellow)})
		}
	} else {
		row(l.save, colors.toolbar.prefSetCommand, "SAVE", text)
	}
	for i, e := range l.sets {
		name := ep.prefSet.Saved[i].Name
		_, picked := m.picked[name]
		row(e, colors.popup.backgroundGrey, name, util.Select(picked, ps.Brightness.Text.ScaleRGB(colors.yellow), text))
	}
	row(l.del, util.Select(m.mode == prefSetDelete, colors.toolbar.commandActive, colors.toolbar.prefSetCommand),
		"DELETE", text)

	if mouse := ctx.Mouse; mouse != nil {
		for _, e := range append([]math.Extent2D{l.save, l.del}, l.sets...) {
			if e.Inside(mouse.Pos) {
				ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(colors.toolbar.hoveredOutline), e.Corners())
			}
		}
	}

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
	ep.drawToolbarMenuOutline(ctx, cb, l.extent)
}

func (prefSetPanel) click(ep *Scope, ctx *scope.Context, anchor, p [2]float32) {
	m := &ep.prefSetMenu
	l := ep.prefSetMenuLayoutAt(anchor)
	switch {
	case l.save.Inside(p):
		if m.mode != prefSetSave {
			*m = prefSetMenuState{mode: prefSetSave}
		}
	case l.del.Inside(p):
		if m.mode == prefSetDelete {
			*m = prefSetMenuState{}
		} else {
			*m = prefSetMenuState{mode: prefSetDelete, picked: make(map[string]struct{})}
		}
	default:
		i := slices.IndexFunc(l.sets, func(e math.Extent2D) bool { return e.Inside(p) })
		if i == -1 {
			return
		}
		name := ep.prefSet.Saved[i].Name
		switch m.mode {
		case prefSetSave:
			m.name, m.invalid = name, false
		case prefSetDelete:
			if ep.mouseTertiaryClicked(ctx.Mouse) {
				m.picked[name] = struct{}{}
				ep.deletePrefSets(m.picked)
				*m = prefSetMenuState{}
			} else if _, ok := m.picked[name]; ok {
				delete(m.picked, name)
			} else {
				m.picked[name] = struct{}{}
			}
		default:
			ep.invokePrefSet(ctx, i)
			ep.closeToolbarPanel(prefSetPanel{})
		}
	}
}

// clickOutside closes the menu, as a click anywhere on the display does
// (3.16.5), unless the click is on a PREFSET button: that closes the menu
// itself or displays it beside the button.
func (prefSetPanel) clickOutside(ep *Scope, hit toolbarHit) {
	ep.prefSetMenu = prefSetMenuState{}
	if !opensPanel(hit.button, prefSetPanel{}) || hit.tab {
		ep.closeToolbarPanel(prefSetPanel{})
	}
}

// handlePrefSetKeyboard takes the keystrokes for the name to save a
// preference set under, returning true if it took them.
func (ep *Scope) handlePrefSetKeyboard(ctx *scope.Context) bool {
	m := &ep.prefSetMenu
	if m.mode != prefSetSave {
		return false
	}

	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r < 128 && isAlphaNum(byte(r)) && len(m.name) < maxPrefSetNameChars {
			m.name += string(r)
			m.invalid = false
		}
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if n := len(m.name); n > 0 {
				m.name = m.name[:n-1]
				m.invalid = false
			}
		case imgui.KeyEnter:
			if ep.savePrefSet(m.name) {
				*m = prefSetMenuState{}
			} else {
				m.invalid = true
				ep.soundAlarm()
			}
		case imgui.KeyEscape:
			*m = prefSetMenuState{}
		}
	}
	return true
}

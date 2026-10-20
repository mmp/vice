// eram/groupsup.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Conflict alert suppression groups and the GROUP SUP (Group Suppression)
// view, which lists them (4.2.14). Conflict alerts between the flights of
// a group are suppressed at the sectors subscribed to it. Each scope
// detects its own conflict alerts, so the groups are the user's own: the
// user's sector is the only one subscribed to them, and a group it
// unsubscribes from is obsolete and goes.

// maxSuppressionGroupID is the largest group ID; IDs have two digits.
const maxSuppressionGroupID = 99

// SuppressionGroup is a conflict alert suppression group.
type SuppressionGroup struct {
	ID      int
	Flights []sim.ACID // in alphanumeric order
	// ShowFlights displays the flights below the group's ID in the view.
	ShowFlights bool
}

func (ep *Scope) suppressionGroup(id int) *SuppressionGroup {
	if i := slices.IndexFunc(ep.SuppressionGroups, func(g SuppressionGroup) bool { return g.ID == id }); i >= 0 {
		return &ep.SuppressionGroups[i]
	}
	return nil
}

// groupSuppressed reports whether a group suppresses conflict alerts
// between two flights.
func (ep *Scope) groupSuppressed(a, b sim.ACID) bool {
	return slices.ContainsFunc(ep.SuppressionGroups, func(g SuppressionGroup) bool {
		return slices.Contains(g.Flights, a) && slices.Contains(g.Flights, b)
	})
}

// pruneSuppressionGroups drops the flights whose tracks are gone and then
// the groups that have become obsolete, with fewer than two flights.
func (ep *Scope) pruneSuppressionGroups(ctx *scope.Context) {
	for i := range ep.SuppressionGroups {
		g := &ep.SuppressionGroups[i]
		g.Flights = slices.DeleteFunc(g.Flights, func(acid sim.ACID) bool {
			_, ok := ctx.Client.State.GetTrackByACID(acid)
			return !ok
		})
	}
	ep.SuppressionGroups = slices.DeleteFunc(ep.SuppressionGroups, func(g SuppressionGroup) bool { return len(g.Flights) < 2 })
}

// suppressionGroupsChanged runs a conflict alert pass in the next frame,
// so that the alerts the groups now suppress go at once.
func (ep *Scope) suppressionGroupsChanged() {
	ep.lastConflictUpdate = time.Time{}
}

func suppressionGroupStatus(id int) CommandStatus {
	return CommandStatus{feedbackArea: []string{"ACCEPT", "GROUP SUPPRESSION", fmt.Sprintf("%02d", id)}}
}

// enterSuppressionGroup handles a group of flights entered with SG: it adds
// the group, or, unless add is set, deletes the group that already has
// exactly those flights. A new group displays the view.
func (ep *Scope) enterSuppressionGroup(tracks []*sim.Track, add bool) (CommandStatus, error) {
	var flights []sim.ACID
	for _, trk := range tracks {
		if trk.IsUnassociated() {
			return CommandStatus{}, ErrIllegalACID
		}
		flights = append(flights, trk.FlightPlan.ACID)
	}
	slices.Sort(flights)
	flights = slices.Compact(flights)
	if len(flights) < 2 {
		return CommandStatus{}, ErrCommandFormat
	}

	if i := slices.IndexFunc(ep.SuppressionGroups, func(g SuppressionGroup) bool {
		return slices.Equal(g.Flights, flights)
	}); i >= 0 {
		id := ep.SuppressionGroups[i].ID
		if !add {
			ep.SuppressionGroups = slices.Delete(ep.SuppressionGroups, i, i+1)
		}
		return suppressionGroupStatus(id), nil
	}

	id := 1
	for ep.suppressionGroup(id) != nil {
		id++
	}
	if id > maxSuppressionGroupID {
		return CommandStatus{}, ErrNoSuppressionGroupAvailable
	}
	ep.SuppressionGroups = append(ep.SuppressionGroups, SuppressionGroup{ID: id, Flights: flights})
	slices.SortFunc(ep.SuppressionGroups, func(a, b SuppressionGroup) int { return a.ID - b.ID })
	ep.currentPrefs().GroupSup.Visible = true
	ep.suppressionGroupsChanged()
	return suppressionGroupStatus(id), nil
}

// groupFlightEntry is what an SG message does with a flight of a group.
type groupFlightEntry int

const (
	toggleGroupFlight groupFlightEntry = iota // add it, or delete it if it is there
	addGroupFlight
	deleteGroupFlight
)

// enterGroupFlight adds a flight to a group or deletes it from it. A
// flight can only be deleted by the sector that has track control of it.
// Adding one displays the view.
func (ep *Scope) enterGroupFlight(ctx *scope.Context, id int, trk *sim.Track, entry groupFlightEntry) (CommandStatus, error) {
	g := ep.suppressionGroup(id)
	if trk.IsUnassociated() {
		return CommandStatus{}, ErrIllegalACID
	}
	acid := trk.FlightPlan.ACID
	if entry == deleteGroupFlight || (entry == toggleGroupFlight && slices.Contains(g.Flights, acid)) {
		return CommandStatus{}, ep.deleteGroupFlight(ctx, id, acid)
	}
	if !slices.Contains(g.Flights, acid) {
		g.Flights = append(g.Flights, acid)
		slices.Sort(g.Flights)
	}
	ep.currentPrefs().GroupSup.Visible = true
	ep.suppressionGroupsChanged()
	return suppressionGroupStatus(id), nil
}

// deleteGroupFlight deletes a flight from a group; a group left with
// fewer than two flights is obsolete and goes.
func (ep *Scope) deleteGroupFlight(ctx *scope.Context, id int, acid sim.ACID) error {
	g := ep.suppressionGroup(id)
	if g == nil {
		return ErrSuppressionGroupNotFound
	}
	if !slices.Contains(g.Flights, acid) {
		return ErrIllegalACID
	}
	if trk, ok := ctx.Client.State.GetTrackByACID(acid); !ok || !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
		return ErrIllegalUserAction
	}
	g.Flights = slices.DeleteFunc(g.Flights, func(a sim.ACID) bool { return a == acid })
	if len(g.Flights) < 2 {
		ep.unsubscribeSuppressionGroup(id)
	} else {
		ep.suppressionGroupsChanged()
	}
	return nil
}

// displaySuppressionGroup handles SG <group>, which displays a group in
// the view, and SG A <group>, which subscribes the sector to one: the view
// displays every group and the user's sector subscribes to them all, so
// both display the view.
func (ep *Scope) displaySuppressionGroup(id int) CommandStatus {
	ep.currentPrefs().GroupSup.Visible = true
	return suppressionGroupStatus(id)
}

// unsubscribeSuppressionGroup unsubscribes the user's sector from a group,
// which leaves it obsolete.
func (ep *Scope) unsubscribeSuppressionGroup(id int) CommandStatus {
	ep.SuppressionGroups = slices.DeleteFunc(ep.SuppressionGroups, func(g SuppressionGroup) bool { return g.ID == id })
	ep.suppressionGroupsChanged()
	return suppressionGroupStatus(id)
}

// drawGroupSupView draws the Group Suppression view: each group's ID and
// the sectors subscribed to it, followed by its flights if they are
// displayed. A TBP on a group's ID displays or suppresses its flights and
// a TBE offers to unsubscribe from it; a pick on a flight offers to delete
// it from its group. The view expands to show every entry and can't be
// suppressed while the sector subscribes to a group (4.2.14.2).
func (ep *Scope) drawGroupSupView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	gs := &ps.GroupSup
	if !gs.Visible {
		return
	}

	// entry is the group and, for a flight's row, the flight that each row
	// shows.
	type entry struct {
		group int
		acid  sim.ACID
	}
	sector := sectorDisplayID(ctx, ctx.UserPrimaryPosition())
	var rows []Row
	var entries []entry
	for _, g := range ep.SuppressionGroups {
		rows = append(rows, Row{Label: fmt.Sprintf("%02d/%s", g.ID, sector)})
		entries = append(entries, entry{group: g.ID})
		if g.ShowFlights {
			for _, acid := range g.Flights {
				rows = append(rows, Row{Label: "   " + string(acid)})
				entries = append(entries, entry{group: g.ID, acid: acid})
			}
		}
	}
	for len(rows) > 0 && len(rows) < gs.Lines {
		rows, entries = append(rows, Row{}), append(entries, entry{})
	}

	// The entries are at least as wide as the title bar, so that they
	// start at its left.
	const title = "GROUP SUP"
	contentChars := util.ReduceSlice(rows, len(title)+4, func(r Row, n int) int { return max(n, len(r.Label)) })

	var extents []math.Extent2D
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &gs.Position,
		ID:         "group-sup",
		Title:      title,
		Opaque:     gs.Opaque,
		ShowBorder: gs.ShowBorder,
		Brightness: gs.Bright,
		OnMenu: ep.makeViewMenu(ctx, "group-sup", 5,
			func(pb popupBase) popup { return &groupSupPopup{popupBase: pb} }),
		OnMinimize: func() { gs.Visible = len(ep.SuppressionGroups) > 0 },
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               gs.Font,
			ContentChars:            contentChars,
			MaxCols:                 1,
			VisibleRows:             max(1, len(rows)),
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
			OnRowExtents:            func(_ int, e []math.Extent2D) { extents = e },
		},
	})

	mouse := ctx.Mouse
	primary, tertiary := ep.mousePrimaryClicked(mouse), ep.mouseTertiaryClicked(mouse)
	if (!primary && !tertiary) || (ep.popup != nil && ep.popupExtent.Inside(mouse.Pos)) {
		return
	}
	font := ep.ERAMFont(2)
	for i, e := range extents {
		en := entries[i]
		if en.group == 0 || !e.Inside(mouse.Pos) {
			continue
		}
		switch {
		case en.acid != "":
			ep.popup = ep.openPickAreaPopup(ctx, e, "DELETE "+string(en.acid), font, func() {
				ep.applyCommandStatus(ctx, CommandStatus{}, ep.deleteGroupFlight(ctx, en.group, en.acid))
			})
		case tertiary:
			ep.popup = ep.openPickAreaPopup(ctx, e, "DELETE", font, func() { ep.unsubscribeSuppressionGroup(en.group) })
		default:
			g := ep.suppressionGroup(en.group)
			g.ShowFlights = !g.ShowFlights
		}
		ep.consumeMouseClick(mouse)
		return
	}
}

// groupSupPopup is the view's menu (figure 4-187). Typing while it is
// displayed replaces it with the Group Suppression Input Box, whose
// message is that of an SG command without the SG (figure 4-188).
type groupSupPopup struct {
	popupBase
	typing bool
	buf    string
}

func (p *groupSupPopup) handleKeyboard(ep *Scope, ctx *scope.Context) bool {
	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r >= ' ' && r <= '~' {
			p.typing = true
			p.buf += string(r)
		}
	}
	if !p.typing {
		return false
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if p.buf != "" {
				p.buf = p.buf[:len(p.buf)-1]
			}
		case imgui.KeyEnter:
			var msg inputText
			msg.Set("SG " + p.buf)
			status, err := ep.executeERAMCommand(ctx, msg)
			ep.applyCommandStatus(ctx, status, err)
			ep.popup = nil
		case imgui.KeyEscape:
			ep.popup = nil
		}
	}
	return true
}

func (p *groupSupPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	gs := &ep.currentPrefs().GroupSup
	rows := []MenuItem{{Input: &MenuInput{Buf: &p.buf}}}
	if !p.typing {
		rows = []MenuItem{
			ep.makeBooleanMenuItem(&gs.Opaque, "O", "T"),
			ep.makeToggleMenuItem(&gs.ShowBorder, "BORDER"),
			makeIntMenuItem(ep, &gs.Lines, "LINES", 3, 20, 1),
			makeIntMenuItem(ep, &gs.Font, "FONT", 1, 3, 1),
			makeIntMenuItem(ep, &gs.Bright, "BRIGHT", 0, 100, 2),
		}
	}
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: "GS",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	})
}

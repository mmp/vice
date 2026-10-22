// eram/deptlist.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// The DEPT LIST (Departure List) view lists the proposed departures in a
// sublist for each departure airport, with their filed altitudes
// (4.2.12). A departure is proposed until its flight plan pairs with its
// track after takeoff.

// deptListFlights returns the flight plans the view lists: those of the
// proposed departures that the user will work, as their departure
// controller or after a handoff.
func deptListFlights(ctx *scope.Context) []*sim.FlightPlan {
	return util.FilterSlice(ctx.Client.State.UnassociatedFlightPlans, func(fp *sim.FlightPlan) bool {
		departure := fp.TypeOfFlight == av.FlightTypeDeparture || fp.LocalArrival
		return departure && fp.Location.IsZero() && fp.DeleteTime.IsZero() &&
			(ctx.UserControlsPosition(fp.TrackingController) || ctx.UserControlsPosition(fp.InboundHandoffController))
	})
}

// deptSublist is one of the view's sublists: a departure airport and its
// flights in ACID order.
type deptSublist struct {
	name    string
	flights []*sim.FlightPlan
}

// deptSublists returns the view's sublists, suppressed ones included, in
// fix name order or in the order they were sorted in by hand; those that
// order doesn't name follow in fix name order. The flights the user
// deleted are left out.
func (ep *Scope) deptSublists(ctx *scope.Context) []deptSublist {
	byName := make(map[string][]*sim.FlightPlan)
	for _, fp := range deptListFlights(ctx) {
		if _, ok := ep.deptDeleted[fp.ACID]; !ok {
			name := db.AirportDisplayId(fp.DepartureAirport)
			byName[name] = append(byName[name], fp)
		}
	}
	order := ep.currentPrefs().DeptList.SublistOrder
	rank := func(name string) int {
		if i := slices.Index(order, name); i >= 0 {
			return i
		}
		return len(order)
	}
	names := slices.SortedFunc(maps.Keys(byName), func(a, b string) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a, b))
	})
	return util.MapSlice(names, func(name string) deptSublist {
		fps := byName[name]
		slices.SortFunc(fps, func(a, b *sim.FlightPlan) int { return cmp.Compare(a.ACID, b.ACID) })
		return deptSublist{name: name, flights: fps}
	})
}

// displayedDeptSublists returns the sublists that aren't suppressed.
func (ep *Scope) displayedDeptSublists(ctx *scope.Context) []deptSublist {
	suppressed := ep.currentPrefs().DeptList.SuppressedSublists
	return util.FilterSlice(ep.deptSublists(ctx), func(s deptSublist) bool { return !slices.Contains(suppressed, s.name) })
}

// formatFiledAltitude formats a flight plan's filed altitude as the view
// shows it: a block altitude, VFR, or OTP, or the requested altitude in
// hundreds of feet without leading zeros (figure 4-185).
func formatFiledAltitude(fp *sim.FlightPlan) string {
	switch {
	case fp.HasAltitudeBlock() || fp.AssignedAltitudeQualifier != sim.PlainAltitude:
		return formatAssignedAltitude(fp)
	case fp.Rules == av.FlightRulesVFR:
		return "VFR"
	case fp.RequestedAltitude == 0:
		return ""
	}
	return strconv.Itoa(fp.RequestedAltitude / 100)
}

// deptFlightRow returns a flight's row in its sublist: its AID and filed
// altitude.
func deptFlightRow(fp *sim.FlightPlan) Row {
	return Row{Label: fmt.Sprintf("%-7s %3s", fp.ACID, formatFiledAltitude(fp))}
}

// labelChars returns the length of the longest of the rows' labels.
func labelChars(rows []Row) int {
	return util.ReduceSlice(rows, 0, func(r Row, n int) int { return max(n, len(r.Label)) })
}

// deptListTearOffs are the tear-offs of the DEPT LIST view's sublists, by
// sublist name. A torn-off sublist is headed by its name and the view's
// title after its bar (figures 2-19, 13-55). It stays on the scope while
// its airport has no proposed departures and so no sublist in the view.
var deptListTearOffs = entryTearOffs{
	locations: func(ep *Scope) *map[string]math.Point2LL { return &ep.currentPrefs().DeptList.TearOffs },
	tearOff: func(ep *Scope, ctx *scope.Context, name string) (entryTearOff, bool) {
		dl := &ep.currentPrefs().DeptList
		rows := []Row{{Label: name + " DEPT", TearOff: name}}
		sublists := ep.deptSublists(ctx)
		if i := slices.IndexFunc(sublists, func(s deptSublist) bool { return s.name == name }); i >= 0 {
			rows = append(rows, util.MapSlice(sublists[i].flights, deptFlightRow)...)
		}
		return entryTearOff{
			ViewRowSource: ViewRowSource{
				Rows:               rows,
				FontIndex:          dl.Font,
				ContentChars:       labelChars(rows),
				ReserveBadgeColumn: true,
				RowSpacing:         RowSpacingCompact,
			},
			brightness: dl.Bright,
			border:     dl.ShowBorder,
		}, true
	},
}

// drawDeptListView draws the Departure List view: each displayed sublist's
// heading, with its tear-off bar if TEAROFF is on, above its flights'
// AIDs and filed altitudes. A pick on a heading suppresses its sublist.
func (ep *Scope) drawDeptListView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	dl := &ps.DeptList
	if !dl.Visible {
		return
	}

	// Deleted flights that are no longer proposed are forgotten, so that a
	// later flight with the same ACID is listed.
	flights := deptListFlights(ctx)
	maps.DeleteFunc(ep.deptDeleted, func(acid sim.ACID, _ struct{}) bool {
		return !slices.ContainsFunc(flights, func(fp *sim.FlightPlan) bool { return fp.ACID == acid })
	})

	var rows []Row
	// headings parallels rows, with the name of the sublist that each
	// heading row heads.
	var headings []string
	for i, s := range ep.displayedDeptSublists(ctx) {
		if i > 0 {
			rows, headings = append(rows, Row{}), append(headings, "")
		}
		rows = append(rows, Row{Label: s.name, Centered: true, TearOff: s.name})
		headings = append(headings, s.name)
		for _, fp := range s.flights {
			rows, headings = append(rows, deptFlightRow(fp)), append(headings, "")
		}
	}

	var extents []math.Extent2D
	first := 0
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &dl.Position,
		ID:         "dept-list",
		Title:      "DEPT",
		Opaque:     dl.Opaque,
		ShowBorder: dl.ShowBorder,
		Brightness: dl.Bright,
		OnMenu: ep.makeViewMenu(ctx, "dept-list", 7+len(dl.SuppressedSublists),
			func(pb popupBase) popup { return &deptListPopup{popupBase: pb} }),
		OnMinimize: func() { dl.Visible = false },
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               dl.Font,
			ContentChars:            labelChars(rows),
			MaxCols:                 1,
			VisibleRows:             visibleLines(dl.Lines, len(rows)),
			TearOffs:                &deptListTearOffs,
			ShowTearOffBars:         dl.ShowIndicators,
			RowSpacing:              RowSpacingCompact,
			ScrollState:             &ep.deptScroll,
			CenterColumnsInTitleBar: true,
			OnRowExtents: func(firstVisible int, e []math.Extent2D) {
				first, extents = firstVisible, e
			},
		},
	})

	mouse := ctx.Mouse
	if (!ep.mousePrimaryClicked(mouse) && !ep.mouseTertiaryClicked(mouse)) ||
		(ep.popup != nil && ep.popupExtent.Inside(mouse.Pos)) {
		return
	}
	for i, e := range extents {
		if name := headings[first+i]; name != "" && e.Inside(mouse.Pos) {
			dl.SuppressedSublists = append(dl.SuppressedSublists, name)
			ep.consumeMouseClick(mouse)
			return
		}
	}
}

// sortDeptSublists sorts the picked sublists to the top of the view in
// the order they were picked, ahead of the others in their current order.
func (ep *Scope) sortDeptSublists(ctx *scope.Context, picked []string) {
	order := slices.Clone(picked)
	for _, s := range ep.deptSublists(ctx) {
		if !slices.Contains(order, s.name) {
			order = append(order, s.name)
		}
	}
	ep.currentPrefs().DeptList.SublistOrder = order
}

// deleteDeptSublists removes the picked sublists and their flights from
// the view; flights proposed later bring a sublist back.
func (ep *Scope) deleteDeptSublists(ctx *scope.Context, picked []string) {
	if ep.deptDeleted == nil {
		ep.deptDeleted = make(map[sim.ACID]struct{})
	}
	for _, s := range ep.deptSublists(ctx) {
		if slices.Contains(picked, s.name) {
			for _, fp := range s.flights {
				ep.deptDeleted[fp.ACID] = struct{}{}
			}
		}
	}
}

// deptListPopup is the view's menu (figure 4-185). The suppressed
// sublists follow SORT; a pick on one redisplays it. SORT shows the Sort
// Pop-Up Box's items at the bottom: FIX NAME, DELETE, and the displayed
// sublists. TBPs on sublists pick them in order and a TBE picks the last
// one, which sorts them to the top of the view or, after DELETE, deletes
// them.
type deptListPopup struct {
	popupBase
	sort     bool
	deleting bool
	picked   []string
}

func (p *deptListPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	dl := &ep.currentPrefs().DeptList
	rows := []MenuItem{
		ep.makeBooleanMenuItem(&dl.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&dl.ShowBorder, "BORDER"),
		ep.makeToggleMenuItem(&dl.ShowIndicators, "TEAROFF"),
		makeLinesMenuItem(ep, &dl.Lines, 3),
		makeIntMenuItem(ep, &dl.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &dl.Bright, "BRIGHT", 0, 100, 2),
		ep.makeToggleMenuItem(&p.sort, "SORT"),
	}
	for _, name := range dl.SuppressedSublists {
		rows = append(rows, MenuItem{Label: name, BgColor: colors.popup.backgroundBlack, Color: colors.popup.text,
			OnClick: func(MenuClickType) bool {
				dl.SuppressedSublists = slices.DeleteFunc(dl.SuppressedSublists, func(s string) bool { return s == name })
				return false
			}})
	}
	if p.sort {
		rows = append(rows, p.sortRows(ep, ctx)...)
	}
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: "DEPT",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	})
}

// sortRows returns the Sort Pop-Up Box's items. FIX NAME is lit while the
// sublists are in fix name order and DELETE while picks delete them.
func (p *deptListPopup) sortRows(ep *Scope, ctx *scope.Context) []MenuItem {
	dl := &ep.currentPrefs().DeptList
	rows := []MenuItem{{
		Label:   "FIX NAME",
		BgColor: util.Select(len(dl.SublistOrder) == 0, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
		Color:   colors.popup.text,
		OnClick: func(MenuClickType) bool {
			dl.SublistOrder, p.picked = nil, nil
			return false
		},
	}, {
		Label:   "DELETE",
		BgColor: util.Select(p.deleting, colors.toolbar.commandActive, colors.popup.backgroundBlack),
		Color:   colors.popup.text,
		OnClick: func(MenuClickType) bool {
			p.deleting, p.picked = !p.deleting, nil
			return false
		},
	}}
	for _, s := range ep.displayedDeptSublists(ctx) {
		picked := slices.Contains(p.picked, s.name)
		rows = append(rows, MenuItem{
			Label:   s.name,
			BgColor: util.Select(picked, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
			Color:   util.Select(picked, colors.popup.backgroundBlack, colors.popup.backgroundGrey),
			OnClick: func(click MenuClickType) bool {
				p.pickSublist(ep, ctx, s.name, click)
				return false
			},
		})
	}
	return rows
}

func (p *deptListPopup) pickSublist(ep *Scope, ctx *scope.Context, name string, click MenuClickType) {
	if !slices.Contains(p.picked, name) {
		p.picked = append(p.picked, name)
	}
	if click != MenuClickTertiary {
		return
	}
	if p.deleting {
		ep.deleteDeptSublists(ctx, p.picked)
		p.deleting = false
	} else {
		ep.sortDeptSublists(ctx, p.picked)
	}
	p.picked = nil
}

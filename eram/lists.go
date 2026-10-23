// eram/lists.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"cmp"
	"fmt"
	"iter"
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

// The views that list flights and codes: DEPT LIST, FLIGHT EVENT, and
// BEACON CODE.

///////////////////////////////////////////////////////////////////////////
// DEPT LIST

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
		if _, ok := ep.DeptListDeleted[fp.ACID]; !ok {
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
	maps.DeleteFunc(ep.DeptListDeleted, func(acid sim.ACID, _ struct{}) bool {
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
	if ep.DeptListDeleted == nil {
		ep.DeptListDeleted = make(map[sim.ACID]struct{})
	}
	for _, s := range ep.deptSublists(ctx) {
		if slices.Contains(picked, s.name) {
			for _, fp := range s.flights {
				ep.DeptListDeleted[fp.ACID] = struct{}{}
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

///////////////////////////////////////////////////////////////////////////
// FLIGHT EVENT

// The FLIGHT EVENT (Flight Event List) view and the FEL indicator in data
// blocks (4.2.13, 5.1.5.19). Of the view's conditions, vice has only call
// sign mismatches, where a flight plan's ACID differs from the ADS-B call
// sign of the track it is paired with: there are no 24-bit addresses or
// second surveillance sources.

// felListed reports whether the view lists a track's call sign mismatch,
// as it does those of the tracks the user owns or is being offered but for
// the entries the user has deleted. Its FDB then shows the FEL indicator.
func (ep *Scope) felListed(ctx *scope.Context, trk *sim.Track) bool {
	if trk.IsUnassociated() || trk.IsUnsupportedDB() || sim.ACID(trk.ADSBCallsign) == trk.FlightPlan.ACID {
		return false
	}
	if !ctx.UserOwnsFlightPlan(trk.FlightPlan) && !ctx.IsHandoffToUser(trk) {
		return false
	}
	acid, deleted := ep.FELDeleted[trk.ADSBCallsign]
	return !deleted || acid != trk.FlightPlan.ACID
}

// felEntries returns the tracks whose call sign mismatches the view lists,
// by CID.
func (ep *Scope) felEntries(ctx *scope.Context) []*sim.Track {
	var entries []*sim.Track
	for _, trk := range ctx.Client.State.Tracks {
		if ep.felListed(ctx, trk) {
			entries = append(entries, trk)
		}
	}
	slices.SortFunc(entries, func(a, b *sim.Track) int { return cmp.Compare(a.FlightPlan.CID, b.FlightPlan.CID) })
	return entries
}

// drawFELIndicator draws the FEL indicator, a box with a gray band across
// its top (figure 5-41), in e.
func drawFELIndicator(trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	e math.Extent2D, ps *Preferences) {
	band := e
	band.P0[1] = e.P1[1] - e.Height()/3
	addQuad(trid, e, renderer.RGB{})
	addQuad(trid, band, ps.Brightness.DBFEL.ScaleRGB(colors.popup.backgroundGrey))
	ld.AddLineLoop(ps.Brightness.DBFEL.ScaleRGB(colors.view.border), e.Corners())
}

// drawFlightEventView draws the FLIGHT EVENT view. A pick on an entry
// selects it and offers to delete it. The view is suppressed when its last
// entry goes (4.2.13).
func (ep *Scope) drawFlightEventView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	entries := ep.felEntries(ctx)
	if len(entries) == 0 && ep.felHadEntries {
		ps.FlightEvent.Visible = false
	}
	ep.felHadEntries = len(entries) > 0
	if !ps.FlightEvent.Visible {
		return
	}

	font := ep.ERAMFont(ps.FlightEvent.Font)
	var rows []Row
	if len(entries) > 0 {
		// The header is underlined, as in figure 4-186.
		rows = append(rows, Row{Label: "CALLSIGN MISMATCH", Centered: true,
			AfterDraw: func(_ math.Extent2D, p [2]float32, b *ViewBuilders) {
				y := p[1] - cellHeight(font) - 2
				b.Ld.AddLine([2]float32{p[0], y}, [2]float32{p[0] + float32(len("CALLSIGN MISMATCH"))*charWidth(font), y},
					ps.FlightEvent.Bright.ScaleRGB(colors.view.text))
			}})
	}
	for _, trk := range entries {
		rows = append(rows, Row{
			Label: trk.FlightPlan.CID + " " + string(trk.FlightPlan.ACID) + " " + string(trk.ADSBCallsign) + " (ADSB)",
			ID:    string(trk.FlightPlan.ACID) + " " + string(trk.ADSBCallsign),
		})
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.FlightEvent.Position,
		ID:         "flight-event",
		Title:      "FLIGHT EVENT",
		Opaque:     ps.FlightEvent.Opaque,
		ShowBorder: ps.FlightEvent.ShowBorder,
		Brightness: ps.FlightEvent.Bright,
		OnMenu: ep.makeViewMenu(ctx, "flight-event", 5,
			func(pb popupBase) popup { return &flightEventPopup{popupBase: pb} }),
		OnMinimize: func() { ps.FlightEvent.Visible = false },
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               ps.FlightEvent.Font,
			ContentChars:            40,
			MaxCols:                 1,
			VisibleRows:             visibleLines(ps.FlightEvent.Lines, len(rows)),
			RowSpacing:              RowSpacingCompact,
			ScrollState:             &ep.felScroll,
			CenterColumnsInTitleBar: true,
			SelectableState:         &ep.felSelect,
			// The header is the first row.
			OnRowDelete: func(idx int) { ep.deleteFELEntry(entries[idx-1]) },
		},
	})
}

// deleteFELEntry deletes a track's entry; it comes back if the track's
// ACID changes and still doesn't match.
func (ep *Scope) deleteFELEntry(trk *sim.Track) {
	if ep.FELDeleted == nil {
		ep.FELDeleted = make(map[av.ADSBCallsign]sim.ACID)
	}
	ep.FELDeleted[trk.ADSBCallsign] = trk.FlightPlan.ACID
}

type flightEventPopup struct {
	popupBase
}

func (p *flightEventPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: "FEL",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows: []MenuItem{
			ep.makeBooleanMenuItem(&ps.FlightEvent.Opaque, "O", "T"),
			ep.makeToggleMenuItem(&ps.FlightEvent.ShowBorder, "BORDER"),
			makeLinesMenuItem(ep, &ps.FlightEvent.Lines, 3),
			makeIntMenuItem(ep, &ps.FlightEvent.Font, "FONT", 1, 3, 1),
			makeIntMenuItem(ep, &ps.FlightEvent.Bright, "BRIGHT", 0, 100, 2),
		},
	})
}

///////////////////////////////////////////////////////////////////////////
// BEACON CODE

func (ep *Scope) drawBeaconCodeView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.BeaconCodeView.Visible {
		return
	}

	// 4-digit beacon code plus a trailing "." for manually-added entries —
	// space for the dot is reserved on every cell so columns line up.
	const cellChars = 5

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.BeaconCodeView.Position,
		ID:         "beacon",
		Title:      "CODE",
		Opaque:     ps.BeaconCodeView.Opaque,
		ShowBorder: ps.BeaconCodeView.ShowBorder,
		Brightness: ps.BeaconCodeView.Bright,
		OnMenu: ep.makeViewMenu(ctx, "beacon", 7,
			func(pb popupBase) popup { return &beaconCodeViewPopup{popupBase: pb} }),
		OnMinimize: func() { ps.BeaconCodeView.Visible = false },
		RowSource: &ViewRowSource{
			Rows:                    beaconCodeRows(ctx, ep, ps),
			FontIndex:               ps.BeaconCodeView.Font,
			ContentChars:            cellChars,
			MaxCols:                 ps.BeaconCodeView.Col,
			VisibleRows:             ps.BeaconCodeView.Lines,
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
		},
	})
}

// beaconCodeRows returns the flat row list shown by drawBeaconCodeView:
// manually-added codes (with trailing ".") plus the codes of aircraft whose
// tracks we own, in the order dictated by SortManual. Row.Color is left zero
// — the View fills in the default text color.
func beaconCodeRows(ctx *scope.Context, ep *Scope, ps *Preferences) []Row {
	owned := slices.Collect(ep.ownedBeaconCodes(ctx))

	makeRow := func(code av.Squawk) Row {
		s := code.String()
		if slices.Contains(ep.AddedBeaconCodes, code) {
			s += "."
		}
		return Row{ID: code.String(), Body: s}
	}

	var rows []Row
	if ps.BeaconCodeView.SortManual {
		// First the (sorted) manually added codes, then the sorted owned codes
		// that aren't also manually added.
		added := slices.Clone(ep.AddedBeaconCodes)
		slices.Sort(added)
		for _, code := range added {
			rows = append(rows, makeRow(code))
		}
		ownedOnly := slices.Clone(owned)
		ownedOnly = slices.DeleteFunc(ownedOnly, func(c av.Squawk) bool {
			return slices.Contains(ep.AddedBeaconCodes, c)
		})
		slices.Sort(ownedOnly)
		for _, code := range ownedOnly {
			rows = append(rows, makeRow(code))
		}
	} else {
		// All codes (added + owned) merged and sorted; manual ones keep their ".".
		all := append(slices.Clone(ep.AddedBeaconCodes), owned...)
		slices.Sort(all)
		all = slices.Compact(all)
		for _, code := range all {
			rows = append(rows, makeRow(code))
		}
	}
	return rows
}

// ownedBeaconCodes returns the codes of the aircraft whose tracks the user
// owns, which are on the beacon code list along with those added to it.
func (ep *Scope) ownedBeaconCodes(ctx *scope.Context) iter.Seq[av.Squawk] {
	return func(yield func(av.Squawk) bool) {
		for _, trk := range ctx.Client.State.Tracks {
			if trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan) &&
				!yield(ep.TrackState[trk.ADSBCallsign].Track.Squawk) {
				return
			}
		}
	}
}

// onBeaconCodeList reports whether a code is on the beacon code list.
func (ep *Scope) onBeaconCodeList(ctx *scope.Context, code av.Squawk) bool {
	return slices.Contains(ep.AddedBeaconCodes, code) || util.SeqContains(ep.ownedBeaconCodes(ctx), code)
}

type beaconCodeViewPopup struct {
	popupBase
}

func (b *beaconCodeViewPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []MenuItem{
		ep.makeBooleanMenuItem(&ps.BeaconCodeView.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Lines, "LINES", 3, 20, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Col, "COL", 1, 5, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.BeaconCodeView.Bright, "BRIGHT", 0, 100, 2),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.SortManual, "SORT MAN"),
	}

	cfg := MenuConfig{
		Title: "CODE",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, b.origin, cfg)
}

func handleBeaconCodeViewList(ep *Scope, codes []av.Squawk) CommandStatus {
	for _, c := range codes {
		if idx := slices.Index(ep.AddedBeaconCodes, c); idx != -1 {
			ep.AddedBeaconCodes = slices.Delete(ep.AddedBeaconCodes, idx, idx+1)
		} else {
			ep.AddedBeaconCodes = append(ep.AddedBeaconCodes, c)
		}
	}

	// TODO: should there be an error if any of the codes are not owned by us?

	return CommandStatus{feedbackArea: []string{"ACCEPT", "BC LIST UPDATE"}}
}

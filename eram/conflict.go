// eram/conflict.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// ERAM Short-Term Conflict Alert (STCA). Every caUpdateInterval of sim time
// the scope predicts each eligible track pair's 3D trajectories
// caLookaheadSeconds into the future and alerts if the pair is ever
// simultaneously within both the lateral and vertical separation minima.
// Targets in conflict render with "flashing" (brightness-cycling) full
// datablocks.
//
// An aircraft flying its route is predicted to follow it; straight-line
// extrapolation would carry it through the route's turns and raise alerts
// against traffic it will never come near. One on a heading holds its track.

const (
	caUpdateInterval   = 5 * time.Second // sim-time between detection passes
	caLookaheadSeconds = 240
	caSampleStep       = 5 // seconds between prediction samples

	caLateralMinimum        = 5.0  // nm
	caReducedLateralMinimum = 3.0  // nm; applies when both targets are in reduced separation airspace
	caVerticalMinimum       = 1000 // ft
	caVerticalSlop          = 5    // ft; keeps exactly-1000-ft-separated targets from alerting on float error

	caLevelRateThreshold  = 300  // ft/min; below this the target is treated as level
	caLevelDBAltTolerance = 200  // ft; DB altitude within this of current altitude = at assigned altitude
	caMaxVerticalRate     = 2500 // ft/min; fastest a level target is assumed to head for its data block altitude

	caDimFactor = 0.5 // FDB brightness scale during the dim phase of the blink
)

// caTarget is a snapshot of one aircraft's state for conflict prediction,
// in flat NM coordinates.
type caTarget struct {
	pos   [2]float32   // NM
	vel   [2]float32   // NM/second
	alt   float32      // ft
	rate  float32      // ft/minute
	dbAlt int          // data block altitude (hard or interim), ft; 0 if unset
	route [][2]float32 // the waypoints ahead, NM, if it's flying its route
}

// position returns where the target will be t seconds from now. A target
// flying its route follows it at its current groundspeed and carries on
// along the last leg past its end.
func (tgt caTarget) position(t float32) [2]float32 {
	speed := math.Length2f(tgt.vel)
	if len(tgt.route) == 0 || speed == 0 {
		return math.Add2f(tgt.pos, math.Scale2f(tgt.vel, t))
	}

	p, dir := tgt.pos, math.Scale2f(tgt.vel, 1/speed)
	d := speed * t // NM still to fly
	for _, wp := range tgt.route {
		leg := math.Sub2f(wp, p)
		l := math.Length2f(leg)
		if l == 0 {
			continue
		}
		if d <= l {
			return math.Add2f(p, math.Scale2f(leg, d/l))
		}
		d -= l
		p, dir = wp, math.Scale2f(leg, 1/l)
	}
	return math.Add2f(p, math.Scale2f(dir, d))
}

// caAltitudeEnvelope returns the [lo, hi] altitude band the target may
// occupy t seconds from now. Climbing/descending targets extrapolate at
// their current rate, leveling off at the data block altitude if they are
// moving toward it. A level target whose data block altitude differs from
// its current altitude may start toward it at any time, so it occupies the
// band between the two that it could have covered by then at
// caMaxVerticalRate.
func caAltitudeEnvelope(tgt caTarget, t float32) (lo, hi float32) {
	db := float32(tgt.dbAlt)
	if math.Abs(tgt.rate) > caLevelRateThreshold {
		pred := tgt.alt + tgt.rate*t/60
		if tgt.dbAlt != 0 {
			if tgt.rate > 0 && db > tgt.alt {
				pred = min(pred, db)
			} else if tgt.rate < 0 && db < tgt.alt {
				pred = max(pred, db)
			}
		}
		return pred, pred
	}
	if tgt.dbAlt != 0 && math.Abs(db-tgt.alt) > caLevelDBAltTolerance {
		maxChange := float32(caMaxVerticalRate) * t / 60
		if db < tgt.alt {
			return max(db, tgt.alt-maxChange), tgt.alt
		}
		return tgt.alt, min(db, tgt.alt+maxChange)
	}
	return tgt.alt, tgt.alt
}

// caIntervalGap returns the vertical distance between two altitude bands,
// 0 if they overlap.
func caIntervalGap(alo, ahi, blo, bhi float32) float32 {
	if ahi < blo {
		return blo - ahi
	}
	if bhi < alo {
		return alo - bhi
	}
	return 0
}

// caConflict reports whether the pair is predicted to be simultaneously
// within the lateral and vertical separation minima at any point within the
// lookahead window.
func caConflict(a, b caTarget) bool {
	// Quick outs: skip the sampling loop if the pair can't get within the
	// minima inside the window even at maximum closure.
	sep := math.Length2f(math.Sub2f(a.pos, b.pos))
	maxClosure := (math.Length2f(a.vel) + math.Length2f(b.vel)) * caLookaheadSeconds
	if sep > caLateralMinimum+maxClosure {
		return false
	}
	maxAltitudeChange := func(t caTarget) float32 {
		if math.Abs(t.rate) > caLevelRateThreshold {
			return math.Abs(t.rate) / 60 * caLookaheadSeconds
		}
		if t.dbAlt != 0 {
			return min(math.Abs(float32(t.dbAlt)-t.alt), float32(caMaxVerticalRate)/60*caLookaheadSeconds)
		}
		return 0
	}
	if math.Abs(a.alt-b.alt) > caVerticalMinimum+maxAltitudeChange(a)+maxAltitudeChange(b) {
		return false
	}

	for t := float32(0); t <= caLookaheadSeconds; t += caSampleStep {
		pa, pb := a.position(t), b.position(t)
		alo, ahi := caAltitudeEnvelope(a, t)
		blo, bhi := caAltitudeEnvelope(b, t)

		latMin := float32(caLateralMinimum)
		if inReducedSeparationAirspace(ahi) && inReducedSeparationAirspace(bhi) {
			latMin = caReducedLateralMinimum
		}

		if math.Length2f(math.Sub2f(pa, pb)) < latMin &&
			caIntervalGap(alo, ahi, blo, bhi) < caVerticalMinimum-caVerticalSlop {
			return true
		}
	}
	return false
}

// CAPair is an active conflict alert between two targets. Callsigns are
// stored alphabetically ordered so a pair has a single identity.
type CAPair struct {
	ADSBCallsigns [2]av.ADSBCallsign
	Start         sim.Time
	// Suppressed is set from the Conflict Alert view; the pair's data
	// blocks don't blink while it is (4.2.5).
	Suppressed bool
}

func (p CAPair) has(callsign av.ADSBCallsign) bool {
	return p.ADSBCallsigns[0] == callsign || p.ADSBCallsigns[1] == callsign
}

// mergeCAPairs reconciles the previous alert list with the pairs detected
// this pass: pairs that remain keep their Start time and first-detected
// order, vanished pairs are dropped, and new pairs are appended.
func mergeCAPairs(prev []CAPair, detected [][2]av.ADSBCallsign, now sim.Time) []CAPair {
	var merged []CAPair
	for _, p := range prev {
		if slices.Contains(detected, p.ADSBCallsigns) {
			merged = append(merged, p)
		}
	}
	for _, d := range detected {
		if !slices.ContainsFunc(merged, func(p CAPair) bool { return p.ADSBCallsigns == d }) {
			merged = append(merged, CAPair{ADSBCallsigns: d, Start: now})
		}
	}
	return merged
}

// inConflictAlert reports whether the callsign is a member of any active
// conflict alert pair.
func (ep *Scope) inConflictAlert(callsign av.ADSBCallsign) bool {
	return slices.ContainsFunc(ep.caPairs, func(p CAPair) bool { return p.has(callsign) })
}

// caAlerting reports whether the callsign is a member of an unsuppressed
// conflict alert pair, whose data blocks blink.
func (ep *Scope) caAlerting(callsign av.ADSBCallsign) bool {
	return slices.ContainsFunc(ep.caPairs, func(p CAPair) bool { return !p.Suppressed && p.has(callsign) })
}

// caDimmed reports whether the track is in an unsuppressed conflict alert
// and its FDB is in the dim half of the bright/dim blink. The text, leader
// line, vector, and track position symbol blink together (5.1.5.7).
func (ep *Scope) caDimmed(callsign av.ADSBCallsign) bool {
	return ep.caAlerting(callsign) && time.Now().UnixMilli()/500%2 == 1
}

// updateConflictAlerts runs an STCA detection pass every caUpdateInterval
// of sim time. It rebuilds the set of predicted-conflict pairs from
// scratch and merges it into ep.caPairs. Eligibility: associated, Mode C,
// past tentative, with enough radar history to derive velocity and
// vertical rate; at least one target of a pair must be owned by a
// controller in this ERAM facility, and at least one must be worked by a
// human: virtual controllers can't maneuver the background traffic they
// work, so alerting on it is only a distraction. Pairs in a suppression
// group are left out (4.2.14). The data block altitude of
// a track a TRACON owns is ignored: TRACON controllers can't make data block
// entries, so it doesn't say where the aircraft will level off.
// Note: the caller passes ep.visibleTracks; today that is effectively all
// tracks, but if display filtering (e.g. radar holes) is ever added there,
// conflict detection coverage would narrow with it.
func (ep *Scope) updateConflictAlerts(ctx *scope.Context, tracks []sim.Track) {
	now := ctx.Client.State.SimTime
	if now.Time().Sub(ep.lastConflictUpdate) < caUpdateInterval {
		return
	}
	ep.lastConflictUpdate = now.Time()

	type caCandidate struct {
		callsign av.ADSBCallsign
		acid     sim.ACID
		target   caTarget
		owned    bool // owned by a controller in this ERAM facility
		virtual  bool // tracked by a virtual controller
	}
	var candidates []caCandidate
	for _, trk := range tracks {
		if trk.IsUnassociated() || trk.IsTentative {
			continue
		}
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil || state.Track.Mode != av.TransponderModeAltitude || !state.HaveHeading() {
			continue
		}
		dt := float32(state.TrackTime.Sub(state.PreviousTrackTime).Seconds())
		if dt <= 0 {
			continue
		}

		p0 := math.LL2NM(state.PreviousTrack.Location, ctx.NmPerLongitude)
		p1 := math.LL2NM(state.Track.Location, ctx.NmPerLongitude)
		var vel [2]float32
		if d := math.Sub2f(p1, p0); math.Length2f(d) > 0 {
			vel = math.Scale2f(math.Normalize2f(d), state.Track.Groundspeed/3600) // knots -> NM/s
		}
		rate := (state.Track.TransponderAltitude - state.PreviousTrack.TransponderAltitude) / dt * 60 // ft/min
		var route [][2]float32
		if trk.AssignedHeading == 0 {
			for _, p := range trk.Route {
				route = append(route, math.LL2NM(p, ctx.NmPerLongitude))
			}
		}
		dbAlt := trk.FlightPlan.DataBlockAltitude()
		if ctrl, ok := ctx.Client.State.Controllers[trk.FlightPlan.TrackingController]; ok && !ctrl.ERAMFacility {
			dbAlt = 0
		}

		candidates = append(candidates, caCandidate{
			callsign: trk.ADSBCallsign,
			acid:     trk.FlightPlan.ACID,
			target: caTarget{
				pos:   p1,
				vel:   vel,
				alt:   state.Track.TransponderAltitude,
				rate:  rate,
				dbAlt: dbAlt,
				route: route,
			},
			owned:   ctx.Client.State.IsLocalController(trk.FlightPlan.TrackingController),
			virtual: trk.VirtuallyControlled,
		})
	}

	var detected [][2]av.ADSBCallsign
	for i, ca := range candidates {
		for _, cb := range candidates[i+1:] {
			if (!ca.owned && !cb.owned) || (ca.virtual && cb.virtual) || ep.groupSuppressed(ca.acid, cb.acid) {
				continue
			}
			if caConflict(ca.target, cb.target) {
				pair := [2]av.ADSBCallsign{ca.callsign, cb.callsign}
				if pair[0] > pair[1] {
					pair[0], pair[1] = pair[1], pair[0]
				}
				detected = append(detected, pair)
			}
		}
	}

	ep.caPairs = mergeCAPairs(ep.caPairs, detected, now)
	// A new entry displays the Conflict Alert view (4.2.5).
	if slices.ContainsFunc(ep.caPairs, func(p CAPair) bool { return p.Start.Equal(now) }) {
		ep.currentPrefs().ConflictAlert.Visible = true
	}
}

///////////////////////////////////////////////////////////////////////////
// Conflict Alert view

// conflictAlertChars is the width of a Conflict Alert view entry: the
// suppression indicator and two ACIDs, each with its sector.
const conflictAlertChars = 28

// drawConflictAlertView draws the Conflict Alert view, which lists the
// conflict alert pairs. A TBE on an entry suppresses its pair, and one on
// the suppression indicator before it unsuppresses it (4.2.5).
func (ep *Scope) drawConflictAlertView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.ConflictAlert.Visible {
		return
	}

	indicatorWidth := titleBarButtonPad + 2*charWidth(ep.ERAMFont(ps.ConflictAlert.Font))
	var rows []Row
	for i, p := range ep.caPairs {
		rows = append(rows, Row{Label: ep.conflictAlertEntry(ctx, p), OnClick: func(c RowClick) {
			if c.Tertiary {
				ep.caPairs[i].Suppressed = c.Pos[0] > c.Extent.P0[0]+indicatorWidth
			}
		}})
	}
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.ConflictAlert.Position,
		ID:         "conflict-alert",
		Title:      "CONFLICT ALERT",
		Opaque:     ps.ConflictAlert.Opaque,
		ShowBorder: ps.ConflictAlert.ShowBorder,
		Brightness: ps.ConflictAlert.Bright,
		Menu:       conflictAlertMenu{},
		// The view can only be suppressed while it is empty.
		OnMinimize: func() { ps.ConflictAlert.Visible = len(ep.caPairs) > 0 },
		RowSource: &ViewRowSource{
			Rows:                  rows,
			FontIndex:             ps.ConflictAlert.Font,
			ContentChars:          conflictAlertChars,
			MaxCols:               1,
			VisibleRows:           max(1, len(rows)),
			RowSpacing:            RowSpacingCompact,
			EmptyKeepsColumnWidth: true,
		},
	})
}

// conflictAlertEntry returns a pair's entry in the Conflict Alert view:
// the suppression indicator, then each ACID, followed by the sector that
// owns it if the user doesn't.
func (ep *Scope) conflictAlertEntry(ctx *scope.Context, p CAPair) string {
	var acids [2]string
	for i, cs := range p.ADSBCallsigns {
		acids[i] = string(cs)
		if trk, ok := ctx.GetTrackByCallsign(cs); ok && trk.IsAssociated() {
			acids[i] = string(trk.FlightPlan.ACID)
			if !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
				acids[i] += "(" + sectorDisplayID(ctx, trk.FlightPlan.TrackingController) + ")"
			}
		}
	}
	return fmt.Sprintf("%s %-12s %s", util.Select(p.Suppressed, "X", " "), acids[0], acids[1])
}

// conflictAlertMenu is the Conflict Alert view's menu.
type conflictAlertMenu struct{}

func (conflictAlertMenu) title() string { return "CA" }

func (conflictAlertMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	return []MenuItem{
		ep.makeBooleanMenuItem(&ps.ConflictAlert.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.ConflictAlert.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.ConflictAlert.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.ConflictAlert.Bright, "BRIGHT", 0, 100, 2),
	}
}

///////////////////////////////////////////////////////////////////////////
// Suppression groups

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

	sector := sectorDisplayID(ctx, ctx.UserPrimaryPosition())
	var rows []Row
	for _, g := range ep.SuppressionGroups {
		rows = append(rows, Row{Label: fmt.Sprintf("%02d/%s", g.ID, sector), OnClick: func(c RowClick) {
			if c.Tertiary {
				ep.openRowPickArea(ctx, c, "DELETE", func() { ep.unsubscribeSuppressionGroup(g.ID) })
			} else if sg := ep.suppressionGroup(g.ID); sg != nil {
				sg.ShowFlights = !sg.ShowFlights
			}
		}})
		if g.ShowFlights {
			for _, acid := range g.Flights {
				rows = append(rows, Row{Label: "   " + string(acid), OnClick: func(c RowClick) {
					ep.openRowPickArea(ctx, c, "DELETE "+string(acid), func() {
						ep.applyCommandStatus(ctx, CommandStatus{}, ep.deleteGroupFlight(ctx, g.ID, acid))
					})
				}})
			}
		}
	}
	for len(rows) > 0 && len(rows) < gs.Lines {
		rows = append(rows, Row{})
	}

	// The entries are at least as wide as the title bar, so that they
	// start at its left.
	const title = "GROUP SUP"
	contentChars := util.ReduceSlice(rows, len(title)+4, func(r Row, n int) int { return max(n, len(r.Label)) })

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &gs.Position,
		ID:         "group-sup",
		Title:      title,
		Opaque:     gs.Opaque,
		ShowBorder: gs.ShowBorder,
		Brightness: gs.Bright,
		Menu:       &groupSupMenu{},
		OnMinimize: func() { gs.Visible = len(ep.SuppressionGroups) > 0 },
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               gs.Font,
			ContentChars:            contentChars,
			MaxCols:                 1,
			VisibleRows:             max(1, len(rows)),
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
		},
	})
}

// groupSupMenu is the view's menu (figure 4-187). Typing while it is
// displayed replaces it with the Group Suppression Input Box, whose
// message is that of an SG command without the SG (figure 4-188).
type groupSupMenu struct {
	typing bool
	buf    string
}

func (*groupSupMenu) title() string { return "GS" }

func (p *groupSupMenu) handleKeyboard(ep *Scope, ctx *scope.Context) bool {
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

func (p *groupSupMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	gs := &ep.currentPrefs().GroupSup
	if p.typing {
		return []MenuItem{{Input: &MenuInput{Buf: &p.buf}}}
	}
	return []MenuItem{
		ep.makeBooleanMenuItem(&gs.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&gs.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &gs.Lines, "LINES", 3, 20, 1),
		makeIntMenuItem(ep, &gs.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &gs.Bright, "BRIGHT", 0, 100, 2),
	}
}

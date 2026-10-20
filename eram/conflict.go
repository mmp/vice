// eram/conflict.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
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
// work, so alerting on it is only a distraction. The data block altitude of
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
			if (!ca.owned && !cb.owned) || (ca.virtual && cb.virtual) {
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

	rows := util.MapSlice(ep.caPairs, func(p CAPair) Row { return Row{Label: ep.conflictAlertEntry(ctx, p)} })
	var extents []math.Extent2D
	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.ConflictAlert.Position,
		ID:         "conflict-alert",
		Title:      "CONFLICT ALERT",
		Opaque:     ps.ConflictAlert.Opaque,
		ShowBorder: ps.ConflictAlert.ShowBorder,
		Brightness: ps.ConflictAlert.Bright,
		OnMenu: ep.makeViewMenu(ctx, "conflict-alert", 4,
			func(pb popupBase) popup { return &conflictAlertPopup{popupBase: pb} }),
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
			OnRowExtents: func(_ int, e []math.Extent2D) {
				extents = e
			},
		},
	})

	mouse := ctx.Mouse
	if !ep.mouseTertiaryClicked(mouse) {
		return
	}
	indicatorWidth := titleBarButtonPad + 2*charWidth(ep.ERAMFont(ps.ConflictAlert.Font))
	for i, e := range extents {
		if e.Inside(mouse.Pos) {
			ep.caPairs[i].Suppressed = mouse.Pos[0] > e.P0[0]+indicatorWidth
			ep.clearMouseTertiaryConsumed(mouse)
			return
		}
	}
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

type conflictAlertPopup struct {
	popupBase
}

func (c *conflictAlertPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	ep.DrawERAMMenu(ctx, transforms, cb, c.origin, MenuConfig{
		Title: "CA",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows: []MenuItem{
			ep.makeBooleanMenuItem(&ps.ConflictAlert.Opaque, "O", "T"),
			ep.makeToggleMenuItem(&ps.ConflictAlert.ShowBorder, "BORDER"),
			makeIntMenuItem(ep, &ps.ConflictAlert.Font, "FONT", 1, 3, 1),
			makeIntMenuItem(ep, &ps.ConflictAlert.Bright, "BRIGHT", 0, 100, 2),
		},
	})
}

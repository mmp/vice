// eram/conflict.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
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

	caLateralMinimum        = 5.0   // nm
	caReducedLateralMinimum = 3.0   // nm; applies when both targets are in reduced separation airspace
	caReducedSepCeiling     = 23000 // ft; reduced separation airspace is at or below FL230
	caVerticalMinimum       = 1000  // ft
	caVerticalSlop          = 5     // ft; keeps exactly-1000-ft-separated targets from alerting on float error

	caLevelRateThreshold  = 300  // ft/min; below this the target is treated as level
	caLevelDBAltTolerance = 200  // ft; DB altitude within this of current altitude = at assigned altitude
	caMaxVerticalRate     = 2500 // ft/min; fastest a level target is assumed to head for its data block altitude

	caDimFactor = 0.5 // datablock brightness scale during the dim phase of the flash cycle
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
		if ahi <= caReducedSepCeiling && bhi <= caReducedSepCeiling {
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
	return slices.ContainsFunc(ep.CAPairs, func(p CAPair) bool {
		return p.ADSBCallsigns[0] == callsign || p.ADSBCallsigns[1] == callsign
	})
}

// updateConflictAlerts runs an STCA detection pass every caUpdateInterval
// of sim time. It rebuilds the set of predicted-conflict pairs from
// scratch and merges it into ep.CAPairs. Eligibility: associated, Mode C,
// past tentative, with enough radar history to derive velocity and
// vertical rate; at least one target of a pair must be owned by a
// controller in this ERAM facility, and at least one must be worked by a
// human: virtual controllers can't maneuver the background traffic they
// work, so alerting on it is only a distraction.
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

		candidates = append(candidates, caCandidate{
			callsign: trk.ADSBCallsign,
			target: caTarget{
				pos:   p1,
				vel:   vel,
				alt:   state.Track.TransponderAltitude,
				rate:  rate,
				dbAlt: trk.FlightPlan.DataBlockAltitude(),
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

	ep.CAPairs = mergeCAPairs(ep.CAPairs, detected, now)
}

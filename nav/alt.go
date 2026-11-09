// nav/alt.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/wx"
)

type RateQualifier int

const (
	RateNormal   RateQualifier = iota
	RateGood                   // faster than normal, not maximum
	RateExpedite               // maximum rate
)

type Altitude struct {
	Assigned       *float32 // controller-assigned altitude (not yet in autopilot)
	ActiveAssigned *float32 // assigned altitude currently used for vertical guidance
	ActivateAt     Time     // non-zero while Assigned is pending activation
	Cleared        *ClearedAltitude
	AfterSpeed     *AltitudeAfterSpeed
	Rate           RateQualifier
	RateThrough    *float32                // revert to RateNormal after passing this altitude; nil = all the way
	Restriction    *av.AltitudeRestriction // The restriction at the last waypoint passed.
}

// AltitudeAfterSpeed is an altitude assignment that waits on a speed change,
// as in "reduce speed to 180, then descend and maintain 6,000": once the
// aircraft reaches Speed it is assigned Altitude, to climb or descend at Rate.
type AltitudeAfterSpeed struct {
	Altitude float32
	Speed    av.Airspeed
	Rate     RateQualifier
}

// ClearedAltitude is the altitude an aircraft flying its route's altitude
// restrictions is cleared to: the restrictions never take it above that
// altitude, and if IsFloor, never below it either. Descend via clearances and
// an arrival's initial clearance are floors; an approach clearance ends a
// floor.
type ClearedAltitude struct {
	Altitude float32
	IsFloor  bool
}

const MaximumRate = 100000

// vfrDescentRate is the rate, in feet per minute, at which a VFR pilot
// descends to enter the traffic pattern.
const vfrDescentRate = 500

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("AssignAltitude", alt, afterSpeed, simTime, delayReduction)()
	if alt > nav.Perf.Ceiling {
		return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}
	nav.clearFixAltitudes()
	intent, deferred := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if !deferred {
		nav.enqueueAssignedAltitude(alt, simTime, delayReduction)
	}
	return intent
}

// prepareAltitudeAssignment returns the readback of an altitude assignment
// and settles how it interacts with an assigned speed: the altitude may
// wait on the speed change, in which case it is stored as such and true is
// returned, leaving the caller nothing to assign; or the speed change may
// wait on the altitude.
func (nav *Nav) prepareAltitudeAssignment(alt float32, afterSpeed bool) (speech.CommandIntent, bool) {
	direction := nav.altitudeDirection(alt)
	intent := speech.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
	}

	if sr := nav.Speed.Assigned; afterSpeed && sr != nil {
		if spd, exact := sr.ExactAirspeed(); exact && spd.IAS(nav.FlightState.Altitude) != nav.FlightState.IAS {
			nav.Altitude = Altitude{AfterSpeed: &AltitudeAfterSpeed{Altitude: alt, Speed: spd}}
			intent.AfterSpeed = &spd
			return intent, true
		}
	}

	// If there is an exact speed change in progress (more than 20 knots remaining or any Mach change),
	// defer the speed assignment until after the altitude change completes.
	if sr := nav.Speed.Assigned; sr != nil && direction != speech.AltitudeMaintain {
		if spd, exact := sr.ExactValue(); exact &&
			(sr.IsMach || math.Abs(spd-nav.FlightState.IAS) > 20) {
			nav.Speed = Speed{AfterAltitude: &SpeedAfterAltitude{Speed: *sr, Altitude: alt}}
		}
	}

	return intent, false
}

// altitudeDirection returns whether reaching alt from the present altitude
// is a climb, a descent, or neither.
func (nav *Nav) altitudeDirection(alt float32) speech.AltitudeDirection {
	switch {
	case alt > nav.FlightState.Altitude:
		return speech.AltitudeClimb
	case alt < nav.FlightState.Altitude:
		return speech.AltitudeDescend
	default:
		return speech.AltitudeMaintain
	}
}

// AssignAltitudeNow assigns an altitude that takes effect immediately, with
// none of the pilot's delay in following a controller's instruction.
func (nav *Nav) AssignAltitudeNow(alt float32, afterSpeed bool) speech.CommandIntent {
	defer nav.record("AssignAltitudeNow", alt, afterSpeed)()
	if alt > nav.Perf.Ceiling {
		return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}
	intent, deferred := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if !deferred {
		nav.setAssignedAltitude(alt)
	}
	return intent
}

func (nav *Nav) setAssignedAltitude(alt float32) {
	nav.Altitude = Altitude{
		Assigned:       &alt,
		ActiveAssigned: &alt,
	}
}

func (nav *Nav) enqueueAssignedAltitude(alt float32, simTime Time, delayReduction time.Duration) {
	d := max(nav.drawDelay(nav.Delays.Altitude)-delayReduction, 0)
	nav.enqueueAltitude(alt, RateNormal, simTime.Add(d))
}

func (nav *Nav) enqueueAltitudeAfterSpeed(simTime Time) {
	a := nav.Altitude.AfterSpeed
	nav.enqueueAltitude(a.Altitude, a.Rate, simTime.Add(nav.drawDelay(nav.Delays.AltitudeAfterSpeed)))
}

// enqueueAltitude assigns alt, to be flown at rate once the pilot reacts at
// activateAt. Until then the aircraft keeps flying its previous altitude:
// an earlier assignment, or its cleared altitude and the restrictions it
// was following.
func (nav *Nav) enqueueAltitude(alt float32, rate RateQualifier, activateAt Time) {
	nav.Altitude = Altitude{
		Assigned:       &alt,
		ActiveAssigned: nav.activeAssignedAltitude(),
		ActivateAt:     activateAt,
		Rate:           rate,
		Cleared:        nav.Altitude.Cleared,
		Restriction:    nav.Altitude.Restriction,
	}
}

func (nav *Nav) ExpediteDescent() speech.CommandIntent {
	defer nav.record("ExpediteDescent")()
	return nav.setRate(RateExpedite, nil, speech.AltitudeDescend)
}

func (nav *Nav) ExpediteClimb() speech.CommandIntent {
	defer nav.record("ExpediteClimb")()
	return nav.setRate(RateExpedite, nil, speech.AltitudeClimb)
}

func (nav *Nav) ExpediteDescentThrough(throughAlt float32) speech.CommandIntent {
	defer nav.record("ExpediteDescentThrough", throughAlt)()
	return nav.setRate(RateExpedite, &throughAlt, speech.AltitudeDescend)
}

func (nav *Nav) ExpediteClimbThrough(throughAlt float32) speech.CommandIntent {
	defer nav.record("ExpediteClimbThrough", throughAlt)()
	return nav.setRate(RateExpedite, &throughAlt, speech.AltitudeClimb)
}

func (nav *Nav) GoodRateDescent() speech.CommandIntent {
	defer nav.record("GoodRateDescent")()
	return nav.setRate(RateGood, nil, speech.AltitudeDescend)
}

func (nav *Nav) GoodRateClimb() speech.CommandIntent {
	defer nav.record("GoodRateClimb")()
	return nav.setRate(RateGood, nil, speech.AltitudeClimb)
}

func (nav *Nav) GoodRateThrough(throughAlt float32) speech.CommandIntent {
	defer nav.record("GoodRateThrough", throughAlt)()
	// Infer direction from current state
	dir := speech.AltitudeDescend
	if throughAlt > nav.FlightState.Altitude {
		dir = speech.AltitudeClimb
	}
	return nav.setRate(RateGood, &throughAlt, dir)
}

func (nav *Nav) setRate(rate RateQualifier, throughAlt *float32, direction speech.AltitudeDirection) speech.CommandIntent {
	alt, _, _ := nav.TargetAltitude()
	if nav.Altitude.Assigned != nil {
		alt = *nav.Altitude.Assigned
	}

	wrongDir := (direction == speech.AltitudeDescend && alt >= nav.FlightState.Altitude) ||
		(direction == speech.AltitudeClimb && alt <= nav.FlightState.Altitude)

	if wrongDir {
		if a := nav.Altitude.AfterSpeed; a != nil {
			a.Rate = rate
			return speech.AltitudeIntent{Direction: direction, Altitude: a.Altitude, AfterSpeed: &a.Speed}
		}
		dir := "descending"
		if direction == speech.AltitudeClimb {
			dir = "climbing"
		}
		return speech.MakeUnableIntent("unable. We're not " + dir)
	}

	if nav.Altitude.Rate >= rate {
		return speech.AltitudeIntent{
			Direction:         direction,
			Altitude:          alt,
			AlreadyExpediting: true,
			GoodRate:          nav.Altitude.Rate == RateGood,
		}
	}

	nav.Altitude.Rate = rate
	nav.Altitude.RateThrough = throughAlt
	return speech.AltitudeIntent{
		Direction:   direction,
		Altitude:    alt,
		Expedite:    rate == RateExpedite,
		GoodRate:    rate == RateGood,
		RateThrough: throughAlt,
	}
}

func (nav *Nav) AltitudeOurDiscretion() speech.CommandIntent {
	defer nav.record("AltitudeOurDiscretion")()
	if nav.Altitude.Assigned == nil {
		return speech.MakeUnableIntent("unable. You never assigned us an altitude...")
	}

	nav.Altitude = Altitude{}
	if alt := nav.FinalAltitude; alt > nav.FlightState.Altitude {
		nav.Altitude.Cleared = &ClearedAltitude{Altitude: alt}
	}

	return speech.NavigationIntent{Type: speech.NavAltitudeDiscretion}
}

// climbToCruise sends the aircraft up to the given cruise altitude. A
// controller-issued altitude cancels the published altitude restrictions along
// the route, so if the aircraft has one (even one that is still waiting on a
// speed change), cruise is assigned the same way. Otherwise the aircraft is
// still climbing to a clearance limit and any remaining restrictions continue
// to apply.
func (nav *Nav) climbToCruise(alt float32) {
	if nav.Altitude.Assigned != nil || nav.Altitude.AfterSpeed != nil {
		nav.setAssignedAltitude(alt)
	} else {
		nav.Altitude = Altitude{Cleared: &ClearedAltitude{Altitude: alt}}
	}
}

// hasIssuedAltitude reports whether the aircraft has an altitude to fly
// besides its route's constraints: one assigned or cleared by a controller,
// a waypoint action, or its scenario, which it keeps flying when vectored off
// its route, or one it is already holding after an earlier vector.
// AfterSpeed counts: the altitude is assigned, just deferred until the speed
// change completes.
func (nav *Nav) hasIssuedAltitude() bool {
	if nav.Altitude.Assigned != nil || nav.Altitude.AfterSpeed != nil {
		return true
	}
	c := nav.Altitude.Cleared
	if c == nil {
		return false
	}
	// An approach may descend below a preserved clearance before the next
	// waypoint clears it. Only an unfinished descent still governs a vector.
	return !nav.Approach.Cleared || c.IsFloor || c.Altitude < nav.FlightState.Altitude
}

// clearAltitudeForApproach resets altitude state when an approach-cleared
// aircraft transitions to following approach restrictions. If the controller
// assigned a descent altitude that the aircraft hasn't yet reached, it is
// preserved as a Cleared altitude so the descent continues; the approach's
// restrictions may take the aircraft below it.
func (nav *Nav) clearAltitudeForApproach() {
	var cleared *ClearedAltitude
	if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned < nav.FlightState.Altitude {
		cleared = &ClearedAltitude{Altitude: *nav.Altitude.Assigned}
	} else if c := nav.Altitude.Cleared; c != nil && c.Altitude < nav.FlightState.Altitude {
		cleared = &ClearedAltitude{Altitude: c.Altitude}
	}
	nav.Altitude = Altitude{Cleared: cleared}
}

func (nav *Nav) activatePendingAltitude(simTime Time) {
	if nav.Altitude.ActivateAt.IsZero() || simTime.Before(nav.Altitude.ActivateAt) {
		return
	}
	nav.Altitude.ActiveAssigned = nav.Altitude.Assigned
	nav.Altitude.ActivateAt = Time{}
	// The assignment replaces the clearance the aircraft flew while the
	// pilot reacted, as it does when it takes effect at once.
	nav.Altitude.Cleared = nil
	nav.Altitude.Restriction = nil
}

// activeAssignedAltitude returns the assigned altitude the aircraft is
// flying: while an assignment waits for the pilot to act on it, the earlier
// one it replaces, if any.
func (nav *Nav) activeAssignedAltitude() *float32 {
	if a := nav.Altitude.ActiveAssigned; a != nil || !nav.Altitude.ActivateAt.IsZero() {
		return a
	}
	return nav.Altitude.Assigned
}

// updateAltitude flies the aircraft toward targetAltitude for one second,
// no faster than targetRate, and applies what the altitudes it passes
// through mean for its instructions. deltaKts and slowingTo250 are what the
// speed update did this same second.
func (nav *Nav) updateAltitude(callsign string, targetAltitude, targetRate float32, geometricDescent bool,
	deltaKts float32, slowingTo250 bool, wxs wx.Sample, simTime Time) {
	fs := &nav.FlightState
	fs.PrevAltitude = fs.Altitude

	NavLog(callsign, simTime, NavLogAltitude, "target=%.0f current=%.0f rate=%.0f targetRate=%.0f rate_qual=%d slowingTo250=%v",
		targetAltitude, fs.Altitude, fs.AltitudeRate, targetRate, nav.Altitude.Rate, slowingTo250)

	if targetAltitude == fs.Altitude {
		nav.holdAltitude()
		return
	}

	NavLog(callsign, simTime, NavLogAltitude, "atmosFactor=%.3f pressure=%.1f temp=%.1f",
		atmosClimbFactor(nav.Perf, wxs), wxs.Pressure(), wxs.Temperature().Celsius())

	next := fs.stepAltitude(nav.Perf, targetAltitude, targetRate, geometricDescent, deltaKts, nav.Altitude.Rate,
		nav.descendsOnGlidepath(), wxs)
	fs.Altitude = nav.crossAltitude(next, slowingTo250)
}

// holdAltitude is what being level at the target altitude means: the climb
// off the runway is over, and a rate the pilot was told to climb or descend
// at has been flown, unless a new altitude is about to take effect and is to
// be flown at it.
func (nav *Nav) holdAltitude() {
	fs := &nav.FlightState
	if nav.IsAirborne() {
		fs.InitialDepartureClimb = false
	}
	fs.AltitudeRate = 0
	if nav.Altitude.ActivateAt.IsZero() {
		nav.Altitude.Rate = RateNormal
		nav.Altitude.RateThrough = nil
	}
}

// crossAltitude applies what passing through altitudes on the way from the
// present altitude to next means for the aircraft's instructions, and
// returns the altitude it reaches this second: next, or an altitude it has
// to stop at first.
func (nav *Nav) crossAltitude(next float32, slowingTo250 bool) float32 {
	cur := nav.FlightState.Altitude
	crosses := func(at float32) bool {
		return (cur > at && next <= at) || (cur < at && next >= at)
	}

	// "At alt, reduce speed": the speed is taken up once the altitude is
	// reached or passed.
	if a := nav.Speed.AfterAltitude; a != nil && (nav.Speed.Assigned == nil ||
		nav.Speed.Assigned.IAS(cur).Satisfied(nav.FlightState.IAS)) && crosses(a.Altitude) {
		sr := a.Speed
		nav.Speed.Assigned = &sr
		nav.Speed.AfterAltitude = nil
	}

	// A Mach number is only flown at the altitudes it may be assigned
	// at; below them, the aircraft returns to its usual speeds.
	if next < av.MachTransitionAltitude {
		if sr := nav.Speed.Assigned; sr != nil && sr.IsMach {
			nav.Speed.Assigned = nil
		}
		if sr := nav.Speed.Restriction; sr != nil && sr.IsMach {
			nav.Speed.Restriction = nil
		}
	}

	if cur >= 10000 && next < 10000 {
		// Descending through 10,000'
		if sr := nav.Speed.Assigned; sr != nil {
			// Cancel if the minimum required speed exceeds 250kts.
			// For "at or below 280" the floor is 0, so the pilot
			// can comply at 250 — don't cancel.
			// For "at or above 260", they can't comply at 250.
			if sr.Speed().Value > 250 {
				nav.Speed.Assigned = nil
				next = 10000
			}
		}
		if sr := nav.Speed.Restriction; sr != nil {
			if sr.Speed().Value > 250 {
				nav.Speed.Restriction = nil
				next = 10000
			}
		}

		if slowingTo250 {
			// Keep it at 10k until we're done slowing
			next = 10000
		}
	}

	if nav.Altitude.RateThrough != nil && crosses(*nav.Altitude.RateThrough) {
		nav.Altitude.Rate = RateNormal
		nav.Altitude.RateThrough = nil
	}

	// A cleared visual keeps a descent the controller assigned only until
	// the aircraft gets there or the visual's profile drops below it; the
	// profile takes it on from there.
	if a := nav.activeAssignedAltitude(); a != nil && nav.Altitude.ActivateAt.IsZero() &&
		nav.clearedForVisualApproach() && cur > *a && (next <= *a || nav.visualProfileBelow(*a)) {
		nav.Altitude.Assigned = nil
		nav.Altitude.ActiveAssigned = nil
	}

	return next
}

// TargetAltitude returns the target altitude, the rate to use (ft/min),
// and whether the descent is geometric (following a computed glidepath to a fix).
func (nav *Nav) TargetAltitude() (float32, float32, bool) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetAltitude(nav.FlightState.Altitude), math.Abs(nav.FlightState.AltitudeRate), false
	}

	// Stay on the ground if we're still on the takeoff roll.
	rate := float32(MaximumRate)
	if nav.FlightState.InitialDepartureClimb && !nav.IsAirborne() {
		rate = 0 // still return the desired altitude, just no oomph to get there.
	}

	// Controller-assigned altitude overrides everything else
	if alt := nav.activeAssignedAltitude(); alt != nil {
		return *alt, rate, false
	}

	if nav.clearedForVisualOnHeading() {
		return nav.visualDescentFromHeading()
	}

	if target, ok := nav.findAltitudeTarget(); ok {
		alt := nav.limitAltitude(target.altitude)
		if target.level && alt == nav.FlightState.Altitude {
			return alt, 0, false
		}
		if nav.FlightState.Altitude < target.altitude ||
			(alt > target.altitude && alt >= nav.FlightState.Altitude) {
			// Climbing, which starts immediately, or held at or climbing
			// back to a floor above the restriction.
			return alt, rate, false
		} else {
			// Descending: compute geometric descent rate. The profile is
			// flown to the restriction; a cleared altitude above it only
			// stops the descent early.
			dist, ok := nav.routeDistanceToFix(target.fix)
			eta := dist / nav.FlightState.GS * 3600
			if ok && eta > 0 {
				geometricRate := (nav.FlightState.Altitude - target.altitude) / eta * 60

				if nav.descendsOnGlidepath() {
					return alt, geometricRate, true // exact glideslope
				}

				descent := nav.Perf.Rate.Descent
				altDiff := nav.FlightState.Altitude - target.altitude

				// The AltitudeRate check fixes a case where we enter an oscillatory state of "start
				// descent" / "no not yet" and end up descending too late to meet the restriction.
				startDescent := geometricRate > descent/2 || nav.FlightState.AltitudeRate < -50

				if nav.Waypoints[0].VFRPhase == av.VFRPhaseDownwind {
					if nav.PassedWaypoint.VFRPhase == av.VFRPhaseDownwind {
						// A VFR aircraft on downwind descends steadily to
						// the pattern's altitudes, starting abeam the
						// threshold.
						return alt, geometricRate, true
					}
					// On its way into the pattern, it descends at a normal
					// VFR rate, starting in time to level off half a minute
					// before the entry, and keeps on once it has started.
					if altDiff/vfrDescentRate*60 >= eta-30 || nav.FlightState.AltitudeRate < -vfrDescentRate/2 {
						return alt, max(geometricRate, vfrDescentRate), true
					}
				} else if nav.Approach.Cleared && nav.isFAF(target.fix) {
					// A pilot cleared for an approach starts down to the
					// final approach fix where a 3 degree path to it
					// begins, as on a glideslope, and follows the path
					// from there.
					if startDescent || altDiff >= dist*ftPerNMOnGlideslope {
						return alt, geometricRate, true
					}
				} else if startDescent {
					// Dynamic safety factor: accounts for ramp-up deficit where
					// updateAltitude takes ~13 ticks to reach full rate, causing
					// ~descent/9 ft of lost altitude change. Scale the factor
					// inversely with descent size so small descents get more margin.
					rampUpSec := float32(1) / rateMaxDeltaPercent
					rampUpDeficit := descent * rampUpSec / (2 * 60)
					safetyFactor := min(float32(1)+rampUpDeficit/max(altDiff, 100), 1.5)
					return alt, min(geometricRate*safetyFactor, descent), true
				}
			}
			// Not time yet
			if ar := nav.Altitude.Restriction; ar != nil {
				// The restriction carried forward from the fix behind us may
				// be lower than what the route ahead requires (SID
				// restrictions step up); it doesn't supersede the target.
				// Nor does it ever take the aircraft back up: at the tail of
				// a descent it would otherwise climb back toward the fix it
				// has already passed.
				held := min(max(ar.TargetAltitude(nav.FlightState.Altitude), target.altitude),
					nav.FlightState.Altitude)
				return nav.limitAltitude(held), MaximumRate, false
			}
			if c := nav.Altitude.Cleared; c != nil && c.Altitude < nav.FlightState.Altitude {
				return c.Altitude, MaximumRate, false
			}
			return nav.FlightState.Altitude, 0, false
		}
	}

	if c := nav.Altitude.Cleared; c != nil {
		return min(c.Altitude, nav.FinalAltitude), rate, false
	}

	if ar := nav.Altitude.Restriction; ar != nil {
		return ar.TargetAltitude(nav.FlightState.Altitude), rate, false
	}

	// Baseline: stay where we are
	return nav.FlightState.Altitude, 0, false
}

// limitAltitude bounds a restriction's target altitude by the cleared
// altitude: the aircraft never climbs above it, and if it is a floor, never
// descends below it. This is how "except maintain" stops a climb via or
// descend via short of the procedure's restrictions.
func (nav *Nav) limitAltitude(alt float32) float32 {
	c := nav.Altitude.Cleared
	switch {
	case c == nil:
		return alt
	case alt > nav.FlightState.Altitude:
		return min(alt, c.Altitude)
	case c.IsFloor:
		return max(alt, c.Altitude)
	default:
		return alt
	}
}

// descendsOnGlidepath reports whether the aircraft descends on the exact
// geometric path to its next altitude restriction rather than holding
// level until it has to start down: past the FAF, cleared for a visual
// approach, or on a VFR landing's base, final, or straight-in legs.
func (nav *Nav) descendsOnGlidepath() bool {
	if nav.Approach.PassedFAF || nav.clearedForVisualApproach() {
		return true
	}
	if len(nav.Waypoints) == 0 {
		return false
	}
	switch nav.Waypoints[0].VFRPhase {
	case av.VFRPhaseBase, av.VFRPhaseFinal, av.VFRPhaseStraightIn:
		return true
	default:
		return false
	}
}

// isFAF reports whether fix is the final approach fix of the route ahead.
func (nav *Nav) isFAF(fix string) bool {
	return slices.ContainsFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == fix && wp.FAF() })
}

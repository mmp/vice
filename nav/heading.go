// nav/heading.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// The Heading instruction: flying an assigned heading, a DME arc, maneuvers
// or a hold rather than the route, and the controller's heading, direct and
// on-course instructions during the seconds before the pilot acts on them.

type Heading struct {
	Assigned   *math.MagneticHeading
	Turn       *av.TurnDirection
	Arc        *av.DMEArc
	JoiningArc bool
	Maneuvers  []LateralManeuver
	Hold       *FlyHold
	Join       *RouteJoin // Maneuvers we're flying the heading to join, if set
}

// DeferredNavHeading is a heading, direct or hold from the controller that
// the pilot has not yet started to follow, and when they will: a few seconds
// after the instruction, to model the delay before pilots act on one.
type DeferredNavHeading struct {
	Time Time
	// Heading is what the aircraft flies once the delay has passed, and
	// Waypoints, if any, the route it takes up then.
	Heading   Heading
	Waypoints []av.Waypoint

	// SnapshotAltitudeOnEffect, when true, causes the current altitude to be captured into
	// Altitude.Cleared, as a floor, when this deferred heading takes effect. Used when vectoring an
	// arrival off a STAR/approach with no issued altitude so the aircraft holds the current alt.
	SnapshotAltitudeOnEffect bool
}

// hasDeferredRoute reports whether a controller-issued route assignment is
// pending (queued in DeferredNavHeading) but not yet active.
func (nav *Nav) hasDeferredRoute() bool {
	return nav.DeferredNavHeading != nil && len(nav.DeferredNavHeading.Waypoints) > 0
}

// AssignedHeading returns the aircraft's current heading assignment, if
// any, regardless of whether the pilot has yet started following it.
func (nav *Nav) AssignedHeading() (math.MagneticHeading, bool) {
	if dh := nav.DeferredNavHeading; dh != nil {
		if dh.Heading.Assigned != nil {
			return *dh.Heading.Assigned, true
		}
	} else if nav.Heading.Assigned != nil {
		return *nav.Heading.Assigned, true
	}
	return 0, false
}

// intendedHeading returns the true heading of the direction the aircraft's
// instructions are taking it: an assigned heading if it has one (including
// one the pilot hasn't started flying yet), otherwise the bearing to the next
// waypoint of its assigned route (which may also still be pending pilot
// response). The present heading answers the same question only once the
// pilot has finished turning, which is not yet the case in the seconds after
// a "direct {fix}" or mid-turn thereafter.
func (nav *Nav) intendedHeading() math.TrueHeading {
	magVar := nav.FlightState.MagneticVariation
	if hdg, ok := nav.AssignedHeading(); ok {
		return math.MagneticToTrue(hdg, magVar)
	}
	// In a hold, on a DME arc, or flying maneuvers, the route says nothing
	// about the current direction of flight.
	if nav.Heading.Hold == nil && nav.Heading.Arc == nil && len(nav.Heading.Maneuvers) == 0 {
		// Take the bearing to the first waypoint far enough away to give a
		// meaningful one: the aircraft sequences straight past any it is
		// sitting on top of.
		wps := nav.AssignedWaypoints()
		far := func(wp av.Waypoint) bool {
			return math.NMDistance2LLFast(nav.FlightState.Position, wp.Location,
				nav.FlightState.NmPerLongitude) > 0.5
		}
		if i := slices.IndexFunc(wps, far); i != -1 {
			return math.Heading2LL(nav.FlightState.Position, wps[i].Location, nav.FlightState.NmPerLongitude)
		}
	}
	return math.MagneticToTrue(nav.FlightState.Heading, magVar)
}

// onHeading reports whether the aircraft is flying a heading: one it was
// assigned, or a heading leg of a procedure, like a SID's after the climb
// off the runway.
func (nav *Nav) onHeading() bool {
	if nav.Heading.Assigned != nil {
		return true
	}
	if len(nav.Heading.Maneuvers) == 0 {
		return false
	}
	m := nav.Heading.Maneuvers[0]
	return m.FlyToward.IsZero() && m.TrackFrom.IsZero() && m.Track == 0
}

// DepartureHeadingState describes the state of a departure's heading assignment.
type DepartureHeadingState int

const (
	// NoHeading indicates no heading is assigned or upcoming
	NoHeading DepartureHeadingState = iota
	// OnHeading indicates the aircraft is currently flying an assigned heading
	OnHeading
	// TurningToHeading indicates the aircraft is about to turn to a heading
	TurningToHeading
)

// DepartureHeading returns the heading a departure will fly and its state.
// Returns the heading and state based on:
// - OnHeading: aircraft has an assigned heading
// - TurningToHeading: first waypoint has a heading (about to turn)
// - NoHeading: no assigned heading and first waypoint has no heading
func (nav *Nav) DepartureHeading() (int, DepartureHeadingState) {
	// If we have an assigned heading, we're flying it
	if nav.Heading.Assigned != nil {
		return int(*nav.Heading.Assigned), OnHeading
	}
	// If the first waypoint has a heading, we're about to turn to it
	if len(nav.Waypoints) > 0 {
		if h, ok := nav.Waypoints[0].HeadingAction(); ok && h.Heading != 0 {
			return int(h.Heading), TurningToHeading
		}
	}
	return 0, NoHeading
}

func (nav *Nav) AssignHeading(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("AssignHeading", hdg, turn, simTime, delayReduction)()
	if hdg <= 0 || hdg > 360 {
		return speech.MakeUnableIntent("unable. {hdg} isn't a valid heading", hdg)
	}

	cancelHold := nav.Heading.Hold != nil
	nav.assignHeading(hdg, turn, simTime, delayReduction)

	return speech.HeadingIntent{
		Heading:    hdg,
		Type:       speech.HeadingAssign,
		Turn:       headingTurn(turn),
		CancelHold: cancelHold,
	}
}

// headingTurn returns the readback's form of a heading's turn direction.
func headingTurn(turn av.TurnDirection) speech.HeadingTurn {
	switch turn {
	case av.TurnClosest:
		return speech.HeadingTurnClosest
	case av.TurnRight:
		return speech.HeadingTurnToRight
	case av.TurnLeft:
		return speech.HeadingTurnToLeft
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}
}

func (nav *Nav) assignHeading(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time, delayReduction time.Duration) {
	approachCleared := nav.Approach.Cleared
	snapshotAltitude := false

	if _, ok := nav.AssignedHeading(); !ok {
		// If an arrival is given a heading off of a route whose altitude
		// constraints it is flying and it hasn't been issued an altitude,
		// the pilot will request an altitude from the controller, and once
		// the deferred heading actually takes effect we capture the current
		// altitude into Altitude.Cleared so the aircraft holds whatever
		// altitude the pilot was at when they turned. An arrival that hasn't
		// reached its STAR yet is flying toward its constraints, so it does
		// the same. This is decided before the approach clearance is
		// cancelled below, since a cleared approach's constraints are ones
		// the aircraft is flying.
		if len(nav.Waypoints) > 0 &&
			(slices.ContainsFunc(nav.Waypoints, av.Waypoint.OnSTAR) || nav.Waypoints[0].OnApproach()) &&
			!nav.hasIssuedAltitude() {
			if _, ok := nav.findAltitudeTarget(); ok {
				nav.Requests.Altitude = true
				snapshotAltitude = true
				// Discard a completed approach descent so it cannot govern
				// altitude after the approach clearance is cancelled.
				nav.Altitude.Cleared = nil
			}
		}

		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one. It keeps the speed it was
		// flying the approach at, as when the controller cancels the
		// clearance.
		nav.Approach.Abandoned = nav.Approach.Abandoned || approachCleared
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.enqueueHeading(hdg, turn, approachCleared, simTime, delayReduction)
	nav.DeferredNavHeading.SnapshotAltitudeOnEffect = snapshotAltitude
}

func (nav *Nav) FlyPresentHeading(simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("FlyPresentHeading", simTime, delayReduction)()
	nav.assignHeading(nav.FlightState.Heading, av.TurnClosest, simTime, delayReduction)
	return speech.HeadingIntent{
		Heading: nav.FlightState.Heading,
		Type:    speech.HeadingPresent,
	}
}

// enqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment. delayReduction, the time
// since the pilot heard the instruction, is subtracted from the
// pilot-reaction delay (floored at zero).
func (nav *Nav) enqueueHeading(hdg math.MagneticHeading, turn av.TurnDirection, approachCleared bool, simTime Time, delayReduction time.Duration) {
	var d time.Duration
	if approachCleared {
		// Minimal delay if the aircraft has been cleared for an approach.
		d = nav.drawDelay(nav.Delays.HeadingApproachCleared)
	} else if nav.onHeading() && nav.DeferredNavHeading == nil {
		// Already flying a heading; minimal delay.
		d = nav.drawDelay(nav.Delays.HeadingFromHeading)
	} else {
		// LNAV -> heading mode
		d = nav.drawDelay(nav.Delays.HeadingFromLNAV)
	}
	d = max(d-delayReduction, 0)
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(d),
		Heading: Heading{Assigned: &hdg, Turn: &turn},
	}
}

func (nav *Nav) enqueueDirectFix(wps []av.Waypoint, turn av.TurnDirection, simTime Time, delayReduction time.Duration) {
	var d time.Duration
	if len(wps) > 0 && nav.ExpectedDirectFix == wps[0].Fix {
		// Pilot was told to expect this fix; shorter delay
		d = nav.drawDelay(nav.Delays.DirectExpected)
		nav.ExpectedDirectFix = ""
	} else if nav.Heading.Assigned == nil && nav.DeferredNavHeading == nil {
		// Already in LNAV mode; have less of a delay
		d = nav.drawDelay(nav.Delays.DirectFromLNAV)
	} else {
		// heading->LNAV--longer delay
		d = nav.drawDelay(nav.Delays.DirectFromHeading)
	}
	d = max(d-delayReduction, 0)
	dh := &DeferredNavHeading{
		Time:      simTime.Add(d),
		Waypoints: wps,
	}
	if turn != av.TurnClosest {
		dh.Heading.Turn = &turn
	}
	nav.DeferredNavHeading = dh
}

func (nav *Nav) enqueueOnCourse(simTime Time, delayReduction time.Duration) {
	d := max(nav.drawDelay(nav.Delays.OnCourse)-delayReduction, 0)
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time: simTime.Add(d),
	}
}

// activateDeferredHeading puts a controller's heading, direct, or hold into
// effect once the pilot's reaction time has passed.
func (nav *Nav) activateDeferredHeading(simTime Time) {
	dh := nav.DeferredNavHeading
	if dh == nil || simTime.Before(dh.Time) {
		return
	}

	nav.Heading = dh.Heading
	if len(dh.Waypoints) > 0 {
		// The new route starts from wherever the aircraft is; it isn't
		// on a leg of it.
		nav.Waypoints = dh.Waypoints
		nav.PassedWaypoint = av.Waypoint{}
	}
	// If the heading was assigned while the aircraft was descending on
	// a STAR/approach with no issued altitude, snapshot the current
	// altitude now (rather than at command-issue time) so the aircraft
	// holds where the pilot actually was when they turned. Re-check the
	// condition in case an altitude was issued during the deferred
	// window.
	if dh.SnapshotAltitudeOnEffect && !nav.hasIssuedAltitude() {
		nav.Altitude.Cleared = &ClearedAltitude{Altitude: nav.FlightState.Altitude, IsFloor: true}
	}
	nav.DeferredNavHeading = nil
}

// updateHeading turns the aircraft for one second toward the heading its
// instructions call for: a controller's instruction that is due takes
// effect, guidance picks the heading, and the aircraft rolls and turns
// toward it.
func (nav *Nav) updateHeading(callsign string, wxs wx.Sample, simTime Time) {
	nav.activateDeferredHeading(simTime)

	fs := &nav.FlightState
	st := nav.targetHeading(callsign, wxs, simTime)
	rate := float32(StandardTurnRate)
	if st.flat {
		fs.BankAngle = 0
	} else {
		st.turn, rate = fs.roll(nav.Perf, st.heading, st.turn, nav.TAS(wxs.Temperature()))
	}

	NavLog(callsign, simTime, NavLogHeading, "target=%.0f current=%.0f diff=%.1f turn=%v rate=%.1f bank=%.1f",
		st.heading, fs.Heading, math.HeadingDifference(fs.Heading, st.heading), st.turn, rate, fs.BankAngle)

	fs.turn(st.heading, st.turn, rate)
}

// steering is the heading guidance asks for and which way to turn to it.
// A flat turn is flown at the standard rate without rolling into a bank,
// as maneuvers and holds are; otherwise the aircraft rolls into and out of
// the turn.
type steering struct {
	heading math.MagneticHeading
	turn    av.TurnDirection
	flat    bool
}

// targetHeading returns the heading to fly this second from whatever
// governs the aircraft laterally: an approach intercept, maneuvers, a
// hold, an assigned heading, a DME arc, or the next waypoint. It also
// advances that mode: maneuvers and holds step to their next legs, an
// intercept progresses, and a turn direction is dropped once the aircraft
// is on course.
func (nav *Nav) targetHeading(callsign string, wxs wx.Sample, simTime Time) steering {
	fs := &nav.FlightState
	st := steering{heading: fs.Heading, turn: av.TurnClosest}

	switch {
	case nav.interceptingFromHeading() && nav.Heading.Assigned != nil:
		st.heading, st.turn = nav.ApproachHeading(callsign, wxs, simTime)

	case len(nav.Heading.Maneuvers) > 0:
		result := nav.flyManeuvers(&nav.Heading.Maneuvers, wxs, simTime)
		if result.completed {
			// A heading assigned along with the maneuvers was for their
			// first leg; the aircraft now resumes its route.
			nav.Heading = Heading{}
		}
		return steering{heading: result.heading, turn: result.turn, flat: true}

	case nav.Heading.Hold != nil:
		hdg, turn := nav.Heading.Hold.GetHeading(callsign, nav, wxs, simTime)
		return steering{heading: hdg, turn: turn, flat: true}

	case nav.Heading.Assigned != nil:
		st.heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			st.turn = *nav.Heading.Turn
		}

	case nav.Heading.Arc != nil && nav.Heading.JoiningArc:
		st.heading = nav.Heading.Arc.InitialHeading
		if math.HeadingDifference(fs.Heading, st.heading) < 1 {
			nav.Heading.JoiningArc = false
		}

	default:
		// Either on an arc or to a waypoint. Figure out the point we're
		// heading to and then common code will handle wind correction,
		// etc...
		var pTarget math.Point2LL
		if arc := nav.Heading.Arc; arc != nil {
			// Work in nm coordinates
			pc := math.LL2NM(arc.Center, fs.NmPerLongitude)
			pac := math.LL2NM(fs.Position, fs.NmPerLongitude)
			v := math.Sub2f(pac, pc)
			// Heading from center to aircraft, which we assume to be more
			// or less on the arc already.
			angle := float32(math.VectorHeading(v)) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(util.Select(arc.Direction.IsClockwise(), 10, -10))
			p := math.Add2f(pc, math.Scale2f(math.SinCos(math.Radians(angle)), arc.Radius))
			pTarget = math.NM2LL(p, fs.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				return st // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
			if nav.Heading.Turn != nil {
				st.turn = *nav.Heading.Turn
			} else if t := nav.Waypoints[0].Turn(); t != av.TurnClosest {
				st.turn = t
			}
		}

		// No magnetic correction yet, just the raw geometric heading vector
		trueHdg := math.Heading2LL(fs.Position, pTarget, fs.NmPerLongitude)
		v := math.SinCos(math.Radians(trueHdg))
		v = math.Scale2f(v, fs.GS)

		if nav.IsAirborne() {
			trueHdg = math.OffsetHeading(trueHdg, -wxs.Deflection(v))
		}

		st.heading = math.TrueToMagnetic(trueHdg, fs.MagneticVariation)

		// A turn direction is for the turn onto the leg. Once the aircraft
		// is on course it corrects the closest way; otherwise a correction
		// to the other side, as for a wind shift, would be flown as a 360.
		if st.turn != av.TurnClosest && math.HeadingDifference(fs.Heading, st.heading) < 1 {
			closest := av.TurnClosest
			nav.Heading.Turn = &closest
			st.turn = closest
		}
	}
	return st
}

func (nav *Nav) headingForTrack(hdg math.MagneticHeading, wxs wx.Sample) math.MagneticHeading {
	if !nav.IsAirborne() {
		// On the ground the aircraft neither crabs nor drifts with the wind,
		// so the track is flown as a heading; departures follow tracks from
		// the start of the takeoff roll.
		return hdg
	}

	// Convert magnetic track to true, then adjust for wind and convert back.
	trueHdg := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	v := math.SinCos(math.Radians(trueHdg))
	v = math.Scale2f(v, nav.FlightState.GS)
	return math.TrueToMagnetic(math.OffsetHeading(trueHdg, -wxs.Deflection(v)), nav.FlightState.MagneticVariation)
}

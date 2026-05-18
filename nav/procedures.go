// nav/procedures.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

///////////////////////////////////////////////////////////////////////////
// Procedure turns

// ManeuverCompleteType discriminates maneuver completion conditions.
type ManeuverCompleteType int

const (
	UntilHeading                ManeuverCompleteType = iota // done when aircraft heading ≈ Until.Heading
	UntilTime                                               // done after Until.Seconds elapsed (lazy start)
	UntilDist                                               // done after Until.Dist nm flown (lazy start)
	UntilFix                                                // done when ETA to Until.Fix < 2s
	UntilIntercept                                          // done when shouldTurnToIntercept fires for course through Fix
	UntilControllerIntervention                             // never completes; lasts until controller issues a new instruction
	UntilAltitude                                           // done when reaching Until.Altitude
	UntilDME                                                // done when crossing Until.DMEDistance from Until.DMEFix
)

// ManeuverComplete encapsulates the completion condition for a lateral
// maneuver. Type selects the condition; the relevant field(s) provide
// its parameters. Time and distance conditions capture their start
// state lazily on first check.
type ManeuverComplete struct {
	Type     ManeuverCompleteType
	Heading  math.MagneticHeading // target heading (UntilHeading)
	Seconds  float32              // duration in seconds (UntilTime)
	Dist     float32              // distance in nm (UntilDist)
	Fix      math.Point2LL        // target fix (UntilFix, UntilIntercept)
	Altitude int                  // target altitude (UntilAltitude)

	// UntilIntercept: inbound course to intercept and turn direction for the intercept turn.
	InterceptCourse math.MagneticHeading
	InterceptTurn   av.TurnDirection

	// UntilDME: DME distance from a fix.
	DMEDistance     float32
	DMEFix          math.Point2LL
	DMEFixElevation int

	// Lazy-init start state for time/distance conditions.
	Start    Time          // captured on first Done() call (UntilTime)
	StartPos math.Point2LL // captured on first Done() call (UntilDist)
}

func (mc *ManeuverComplete) Done(nav *Nav, simTime Time, wxs wx.Sample, targetHdg math.MagneticHeading) bool {
	switch mc.Type {
	case UntilHeading:
		return math.HeadingDifference(nav.FlightState.Heading, targetHdg) < 1
	case UntilTime:
		if mc.Start.IsZero() {
			mc.Start = simTime
		}
		return float32(simTime.Sub(mc.Start).Seconds()) >= mc.Seconds
	case UntilDist:
		if mc.StartPos.IsZero() {
			mc.StartPos = nav.FlightState.Position
		}
		return math.NMDistance2LL(mc.StartPos, nav.FlightState.Position) >= mc.Dist
	case UntilFix:
		return nav.ETA(mc.Fix) < 2
	case UntilIntercept:
		return nav.shouldTurnToIntercept(mc.Fix, mc.InterceptCourse, mc.InterceptTurn, wxs) == turnToInterceptTurn
	case UntilControllerIntervention:
		return false
	case UntilAltitude:
		return nav.FlightState.Altitude >= float32(mc.Altitude)
	case UntilDME:
		dist := math.DMEDistance(nav.FlightState.Position, nav.FlightState.Altitude,
			mc.DMEFix, float32(mc.DMEFixElevation))
		return dist >= mc.DMEDistance
	default:
		panic(fmt.Sprintf("unhandled ManeuverCompleteType: %d", mc.Type))
	}
}

// LateralManeuver describes a single phase of flight: fly a heading,
// track, or dynamically selected point until a condition is met. A
// sequence of LateralManeuvers forms a procedure turn, hold circuit, or
// ordered heading-leg instruction.
type LateralManeuver struct {
	Heading              math.MagneticHeading // heading to fly
	Track                math.MagneticHeading // if non-zero, wind-corrected heading via headingForTrack
	FlyToward            math.Point2LL        // if non-zero, heading = bearing to this point each tick
	Turn                 av.TurnDirection
	Until                ManeuverComplete
	AssignAltitude       *float32 // if non-nil, set nav.Altitude when this maneuver becomes active
	ClearAltitudeOnFinal bool
	Fix                  string
	Actions              av.WaypointActions
}

func (m *LateralManeuver) String() string {
	var action string
	if !m.FlyToward.IsZero() {
		action = "fly toward fix"
	} else if m.Track != 0 {
		action = fmt.Sprintf("fly track %03d", int(m.Track))
	} else {
		action = fmt.Sprintf("fly heading %03d", int(m.Heading))
	}

	var until string
	switch m.Until.Type {
	case UntilHeading:
		until = fmt.Sprintf("until heading %03d", int(m.Until.Heading))
	case UntilTime:
		until = fmt.Sprintf("for %.0fs", m.Until.Seconds)
	case UntilDist:
		until = fmt.Sprintf("for %.1fnm", m.Until.Dist)
	case UntilFix:
		until = "until fix"
	case UntilIntercept:
		until = fmt.Sprintf("until intercept %03d", int(m.Until.InterceptCourse))
	case UntilControllerIntervention:
		until = "until controller intervention"
	case UntilAltitude:
		until = fmt.Sprintf("until altitude %d", m.Until.Altitude)
	case UntilDME:
		until = fmt.Sprintf("until DME %.1f", m.Until.DMEDistance)
	}

	if until != "" {
		return action + " " + until
	}
	return action
}

// targetHeading computes this maneuver's current target heading, considering
// FlyToward, Track, and fixed Heading in priority order.
func (m *LateralManeuver) targetHeading(nav *Nav, wxs wx.Sample) math.MagneticHeading {
	if !m.FlyToward.IsZero() {
		hdg := math.TrueToMagnetic(
			math.Heading2LL(nav.FlightState.Position, m.FlyToward, nav.FlightState.NmPerLongitude),
			nav.FlightState.MagneticVariation)
		return nav.headingForTrack(hdg, wxs)
	} else if m.Track != 0 {
		return nav.headingForTrack(m.Track, wxs)
	}
	return m.Heading
}

func turnToTrack(track math.MagneticHeading, turn av.TurnDirection) LateralManeuver {
	return LateralManeuver{
		Track: track,
		Turn:  turn,
		Until: ManeuverComplete{Type: UntilHeading, Heading: track},
	}
}

func turnToHeading(heading math.MagneticHeading, turn av.TurnDirection) LateralManeuver {
	return LateralManeuver{
		Heading: heading,
		Turn:    turn,
		Until:   ManeuverComplete{Type: UntilHeading, Heading: heading},
	}
}

func flyHeadingForTime(heading math.MagneticHeading, seconds float32) LateralManeuver {
	return LateralManeuver{
		Heading: heading,
		Until:   ManeuverComplete{Type: UntilTime, Seconds: seconds},
	}
}

func flyHeadingForDistance(heading math.MagneticHeading, dist float32) LateralManeuver {
	return LateralManeuver{
		Heading: heading,
		Until:   ManeuverComplete{Type: UntilDist, Dist: dist},
	}
}

func flyHeadingUntilIntercept(heading math.MagneticHeading, turn av.TurnDirection,
	fix math.Point2LL, interceptCourse math.MagneticHeading) LateralManeuver {
	return LateralManeuver{
		Heading: heading,
		Turn:    turn,
		Until: ManeuverComplete{
			Type:            UntilIntercept,
			Fix:             fix,
			InterceptCourse: interceptCourse,
			InterceptTurn:   turn,
		},
	}
}

func flyTowardFix(fix math.Point2LL) LateralManeuver {
	return LateralManeuver{
		FlyToward: fix,
		Until:     ManeuverComplete{Type: UntilFix, Fix: fix},
	}
}

func flyTrackForTime(track math.MagneticHeading, seconds float32) LateralManeuver {
	return LateralManeuver{
		Track: track,
		Until: ManeuverComplete{Type: UntilTime, Seconds: seconds},
	}
}

func flyTrackUntilIntercept(track math.MagneticHeading, turn av.TurnDirection,
	fix math.Point2LL, interceptCourse math.MagneticHeading) LateralManeuver {
	return LateralManeuver{
		Track: track,
		Turn:  turn,
		Until: ManeuverComplete{
			Type:            UntilIntercept,
			Fix:             fix,
			InterceptCourse: interceptCourse,
			InterceptTurn:   turn,
		},
	}
}

func racetrackEntryManeuvers(entry av.HoldEntry, fix math.Point2LL, inbound math.MagneticHeading,
	turn av.TurnDirection, entryLeg func(math.MagneticHeading) LateralManeuver) []LateralManeuver {
	outbound := math.OppositeHeading(inbound)

	maneuvers := []LateralManeuver{
		flyTowardFix(fix),
	}

	switch entry {
	case av.HoldEntryDirect:
		return maneuvers

	case av.HoldEntryParallel:
		intercept := math.OffsetHeading(inbound, util.Select(turn == av.TurnRight, -40, 40))
		return append(maneuvers,
			turnToTrack(outbound, av.TurnClosest),
			entryLeg(outbound),
			turnToTrack(intercept, oppositeTurnDirection(turn)),
			flyTrackUntilIntercept(intercept, turn, fix, inbound),
			turnToTrack(inbound, turn),
			flyTowardFix(fix))

	case av.HoldEntryTeardrop:
		teardrop := math.OffsetHeading(inbound, util.Select(turn == av.TurnRight, 150, -150))
		base := math.OppositeHeading(teardrop)
		return append(maneuvers,
			turnToTrack(teardrop, av.TurnClosest),
			entryLeg(teardrop),
			turnToTrack(base, turn),
			flyTrackUntilIntercept(base, turn, fix, inbound),
			turnToTrack(inbound, turn),
			flyTowardFix(fix))

	default:
		panic(fmt.Sprintf("unhandled hold entry type: %d", entry))
	}
}

func oppositeTurnDirection(turn av.TurnDirection) av.TurnDirection {
	return util.Select(turn == av.TurnRight, av.TurnLeft, av.TurnRight)
}

type maneuverResult struct {
	heading   math.MagneticHeading
	turn      av.TurnDirection
	rate      float32
	completed bool
}

// flyManeuvers returns the active maneuver's heading, turn direction, and
// turn rate. It advances to the next maneuver when the completion condition
// is met. When the last maneuver completes, it clears the maneuver slice.
func (nav *Nav) flyManeuvers(maneuvers *[]LateralManeuver, wxs wx.Sample, simTime Time) maneuverResult {
	m := &(*maneuvers)[0]
	heading := m.targetHeading(nav, wxs)

	if m.Until.Done(nav, simTime, wxs, heading) {
		*maneuvers = (*maneuvers)[1:]
		if len(*maneuvers) == 0 {
			if m.ClearAltitudeOnFinal {
				nav.Altitude = NavAltitude{}
			}
			return maneuverResult{heading: heading, turn: m.Turn, rate: StandardTurnRate, completed: true}
		}
		// Recompute heading for the new maneuver
		m = &(*maneuvers)[0]
		if m.AssignAltitude != nil {
			nav.setAssignedAltitude(*m.AssignAltitude)
		}
		if event := nav.activateWaypointActions(m.Fix, m.Actions); event != nil {
			nav.PendingWaypointActionEvents = append(nav.PendingWaypointActionEvents, *event)
		}
		heading = m.targetHeading(nav, wxs)
	}

	return maneuverResult{heading: heading, turn: m.Turn, rate: StandardTurnRate}
}

func (nav *Nav) flyProcedureTurnIfNecessary() {
	wp := nav.AssignedWaypoints()
	if !nav.Approach.Cleared || len(wp) < 2 || wp[0].ProcedureTurn() == nil || nav.Approach.NoPT {
		return
	}

	if wp[0].ProcedureTurn().Entry180NoPT {
		inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude)

		acFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude)

		if math.HeadingDifference(acFixHeading, inboundHeading) < 90 {
			return
		}
	}

	// Ensure the approach waypoints are in nav.Waypoints (not just in
	// DeferredNavHeading) so they're available after the PT completes.
	nav.Waypoints = wp
	nav.DeferredNavHeading = nil

	pt := wp[0].ProcedureTurn()

	var exitAlt *float32
	if pt.ExitAltitude != 0 {
		alt := float32(pt.ExitAltitude)
		exitAlt = &alt
	}

	var maneuvers []LateralManeuver
	switch pt.Type {
	case av.PTRacetrack:
		maneuvers = makeRacetrackManeuver(nav, wp, exitAlt)
	case av.PTStandard45:
		maneuvers = makeStandard45Maneuver(nav, wp, exitAlt)
	default:
		panic("Unhandled procedure turn type")
	}

	nav.Heading = NavHeading{Maneuvers: maneuvers}
	if len(nav.Heading.Maneuvers) > 0 {
		nav.Heading.Maneuvers[len(nav.Heading.Maneuvers)-1].ClearAltitudeOnFinal = true
	}
}

func makeStandard45Maneuver(nav *Nav, wp []av.Waypoint, exitAlt *float32) []LateralManeuver {
	pt := wp[0].ProcedureTurn()
	fixLoc := wp[0].Location
	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation

	inboundHdg := math.TrueToMagnetic(math.Heading2LL(wp[0].Location, wp[1].Location, nmPerLong), magVar)
	outboundHdg := math.OppositeHeading(inboundHdg)
	awayHdg := math.OffsetHeading(outboundHdg, util.Select(pt.RightTurns, -45, 45))
	reverseHdg := math.OppositeHeading(awayHdg)
	turn := av.TurnDirection(util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft))

	intercept := flyHeadingUntilIntercept(reverseHdg, av.TurnClosest, fixLoc, inboundHdg)
	intercept.AssignAltitude = exitAlt

	return []LateralManeuver{
		flyTowardFix(fixLoc),
		turnToHeading(outboundHdg, av.TurnClosest),
		outboundLeg(nav, pt, outboundHdg, 1.2),
		turnToHeading(awayHdg, av.TurnClosest),
		outboundLeg(nav, pt, awayHdg, 1),
		turnToHeading(reverseHdg, turn),
		intercept,
	}
}

func makeRacetrackManeuver(nav *Nav, wp []av.Waypoint, exitAlt *float32) []LateralManeuver {
	pt := wp[0].ProcedureTurn()
	fixLoc := wp[0].Location
	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation

	inboundHdg := math.TrueToMagnetic(math.Heading2LL(wp[0].Location, wp[1].Location, nmPerLong), magVar)
	outboundHdg := math.OppositeHeading(inboundHdg)

	acFixHdg := math.TrueToMagnetic(math.Heading2LL(nav.FlightState.Position, fixLoc, nmPerLong), magVar)

	ptTurn := av.TurnDirection(util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft))
	entry := av.Hold{InboundCourse: inboundHdg, TurnDirection: ptTurn}.Entry(acFixHdg)
	maneuvers := racetrackEntryManeuvers(entry, fixLoc, inboundHdg, ptTurn,
		func(track math.MagneticHeading) LateralManeuver {
			return outboundTrackLeg(nav, pt, track, 1)
		})

	turnOutbound := turnToHeading(outboundHdg, ptTurn)
	turnOutbound.AssignAltitude = exitAlt
	maneuvers = append(maneuvers,
		turnOutbound,
		outboundLeg(nav, pt, outboundHdg, 1),
		turnToHeading(inboundHdg, ptTurn))

	return maneuvers
}

// outboundLeg returns a LateralManeuver for the outbound leg of a
// procedure turn, using the correct completion condition based on what was
// specified: UntilDist if a distance was given, UntilTime if a time was
// given. scale multiplies the extent (e.g. 1.5 for teardrop legs).
func outboundLeg(nav *Nav, pt *av.ProcedureTurn, heading math.MagneticHeading, scale float32) LateralManeuver {
	return LateralManeuver{
		Heading: heading,
		Turn:    av.TurnClosest,
		Until:   procedureTurnLegCompletion(nav, pt, scale),
	}
}

func outboundTrackLeg(nav *Nav, pt *av.ProcedureTurn, track math.MagneticHeading, scale float32) LateralManeuver {
	return LateralManeuver{
		Track: track,
		Turn:  av.TurnClosest,
		Until: procedureTurnLegCompletion(nav, pt, scale),
	}
}

func procedureTurnLegCompletion(nav *Nav, pt *av.ProcedureTurn, scale float32) ManeuverComplete {
	if pt.NmLimit > 0 {
		return ManeuverComplete{Type: UntilDist, Dist: pt.NmLimit * scale}
	} else if pt.MinuteLimit > 0 {
		return ManeuverComplete{Type: UntilTime, Seconds: pt.MinuteLimit * 60 * scale}
	}

	switch nav.Approach.Assigned.Type {
	case av.ILSApproach, av.LocalizerApproach, av.VORApproach:
		return ManeuverComplete{Type: UntilTime, Seconds: 60 * scale}
	case av.RNAVApproach:
		return ManeuverComplete{Type: UntilDist, Dist: 4 * scale}
	default:
		panic(fmt.Sprintf("unhandled approach type: %s", nav.Approach.Assigned.Type))
	}
}

///////////////////////////////////////////////////////////////////////////
// Holds

type FlyHold struct {
	Hold        av.Hold
	FixLocation math.Point2LL
	Entry       av.HoldEntry
	Maneuvers   []LateralManeuver
	Cancel      bool // when set, we end the hold after the last leg
}

func (fh *FlyHold) GetHeading(callsign string, nav *Nav, wxs wx.Sample, simTime Time) (math.MagneticHeading, av.TurnDirection, float32) {
	if !fh.activateManeuvers(nav, wxs) {
		return nav.FlightState.Heading, av.TurnClosest, StandardTurnRate
	}

	result := nav.flyManeuvers(&fh.Maneuvers, wxs, simTime)
	if result.completed {
		result = fh.completeManeuverSet(nav, wxs, result)
	}

	dist := math.NMDistance2LL(nav.FlightState.Position, fh.FixLocation)
	NavLog(callsign, simTime, NavLogHold, "entry=%s step=%s acHdg=%.1f targetHdg=%.1f turn=%v dist=%.1fnm",
		fh.Entry.String(), fh.currentStep(), nav.FlightState.Heading, result.heading, result.turn, dist)

	return result.heading, result.turn, result.rate
}

func (fh *FlyHold) activateManeuvers(nav *Nav, wxs wx.Sample) bool {
	if len(fh.Maneuvers) > 0 {
		return true
	}
	if fh.Cancel {
		nav.Heading = NavHeading{}
		return false
	}
	fh.Maneuvers = fh.circuitManeuvers(nav, wxs)
	return true
}

func (fh *FlyHold) completeManeuverSet(nav *Nav, wxs wx.Sample, result maneuverResult) maneuverResult {
	if !fh.activateManeuvers(nav, wxs) {
		return result
	}
	m := &fh.Maneuvers[0]
	return maneuverResult{heading: m.targetHeading(nav, wxs), turn: m.Turn, rate: result.rate}
}

func (fh *FlyHold) currentStep() string {
	if len(fh.Maneuvers) == 0 {
		return "none"
	}
	return fh.Maneuvers[0].String()
}

func (fh *FlyHold) entryManeuvers() []LateralManeuver {
	holdTurn := fh.turnDirection()
	return racetrackEntryManeuvers(fh.Entry, fh.FixLocation, fh.Hold.InboundCourse, holdTurn,
		func(track math.MagneticHeading) LateralManeuver {
			return flyTrackForTime(track, 70)
		})
}

func (fh *FlyHold) circuitManeuvers(nav *Nav, wxs wx.Sample) []LateralManeuver {
	outbound := fh.outboundHeading(nav, wxs)
	turn := fh.turnDirection()
	// Don't turn all the way so that we don't over-turn (given tracks/wind correction) and then
	// swing back toward the fix; this way, going direct to the fix will finish the turn.
	almostInbound := math.OffsetHeading(outbound, util.Select(turn == av.TurnRight, 120, -120))

	return []LateralManeuver{
		turnToHeading(outbound, turn),
		fh.outboundLeg(nav, wxs, outbound),
		turnToHeading(almostInbound, turn),
		flyTowardFix(fh.FixLocation),
	}
}

func (fh *FlyHold) outboundHeading(nav *Nav, wxs wx.Sample) math.MagneticHeading {
	inbound := fh.Hold.InboundCourse
	outbound := math.OppositeHeading(inbound)

	inboundHeading := nav.headingForTrack(inbound, wxs)
	inboundWCA := math.HeadingSignedTurn(inbound, inboundHeading)
	outboundCorrection := math.Clamp(-3*inboundWCA, -45, 45)
	return math.OffsetHeading(outbound, outboundCorrection)
}

func (fh *FlyHold) outboundLeg(nav *Nav, wxs wx.Sample, heading math.MagneticHeading) LateralManeuver {
	if fh.Hold.LegLengthNM > 0 {
		return flyHeadingForDistance(heading, fh.Hold.LegLengthNM)
	}

	mins := fh.Hold.LegMinutes
	if mins == 0 {
		mins = float32(util.Select(nav.FlightState.Altitude < 14000, 1.0, 1.5))
	}

	seconds := mins*60 + wxs.Component(float32(fh.Hold.InboundCourse))*mins
	return flyHeadingForTime(heading, max(15, seconds))
}

func (fh *FlyHold) turnDirection() av.TurnDirection {
	return util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnRight, av.TurnLeft)
}

///////////////////////////////////////////////////////////////////////////
// Airwork

func StartAirwork(wp av.Waypoint, nav Nav) *NavAirwork {
	a := &NavAirwork{
		Radius:         float32(wp.AirworkRadius()),
		Center:         wp.Location,
		AltRange:       wp.AltitudeRestriction().Range,
		RemainingSteps: wp.AirworkMinutes() * 60, // sim ticks are 1 second.
		Altitude:       nav.FlightState.Altitude,
	}

	a.Start360(nav)

	return a
}

func (aw *NavAirwork) Update(nav *Nav) bool {
	// Tick down the number of seconds we're doing this.
	aw.RemainingSteps--
	if aw.RemainingSteps == 0 {
		// Direct to the next waypoint in the route
		nav.Heading = NavHeading{}
		return false
	}

	// If we're getting close to the maximum distance from the center
	// point, turn back toward it.
	d := math.NMDistance2LL(nav.FlightState.Position, aw.Center)
	if aw.ToCenter && d < 1 {
		// Close enough
		aw.ToCenter = false
	} else if float32(aw.Radius)-d < 2.5 || aw.ToCenter {
		aw.Heading = math.TrueToMagnetic(math.Heading2LL(nav.FlightState.Position, aw.Center, nav.FlightState.NmPerLongitude),
			nav.FlightState.MagneticVariation)
		aw.TurnRate = StandardTurnRate
		aw.TurnDirection = av.TurnClosest
		aw.ToCenter = true
		return true
	}

	// Don't check IAS; we only care that we reach the heading and altitude
	// we wanted to do next.
	if nav.FlightState.Heading == aw.Heading && nav.FlightState.Altitude == aw.Altitude {
		if aw.NextMoveCounter == 0 {
			// We just finished. Clean up and Continue straight and level for a bit.
			aw.Dive = false
			aw.NextMoveCounter = nav.Rand.IntRange(5, 30)
		} else if aw.NextMoveCounter == 1 {
			// Pick a new thing.
			aw.ToCenter = false
			if nav.Rand.Float32() < .2 {
				// Do a 360
				aw.Start360(*nav)
			} else if nav.FlightState.Altitude > aw.AltRange[0]+2000 && nav.Rand.Float32() < .2 {
				// Dive.
				aw.Dive = true
				aw.Altitude = nav.Rand.Float32Range(aw.AltRange[0], aw.AltRange[0]+200)
			} else if nav.FlightState.Altitude+1000 < aw.AltRange[1] && nav.Rand.Float32() < .2 {
				// Climbing turn
				aw.Altitude = nav.Rand.Float32Range(aw.AltRange[1]-500, aw.AltRange[1])
				aw.Heading = math.MagneticHeading(nav.Rand.Float32Range(0, 360))
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, av.TurnLeft, av.TurnRight)
			} else if nav.FlightState.Altitude < aw.AltRange[0]+1000 && nav.Rand.Float32() < .2 {
				// Descending turn
				aw.Altitude = nav.Rand.Float32Range(aw.AltRange[0], aw.AltRange[0]+500)
				aw.Heading = math.MagneticHeading(nav.Rand.Float32Range(0, 360))
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, av.TurnLeft, av.TurnRight)
			} else if nav.Rand.Float32() < .2 {
				// Slow turn
				aw.Heading = math.MagneticHeading(nav.Rand.Float32Range(0, 360))
				aw.IAS = math.Lerp(.1, nav.Perf.Speed.Min, av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude))
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, av.TurnLeft, av.TurnRight)
			} else if nav.Rand.Float32() < .2 {
				// Slow, straight and level
				aw.IAS = math.Lerp(.1, nav.Perf.Speed.Min, av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude))
				aw.NextMoveCounter = 20
			} else {
				// Straight and level and then we'll reconsider.
				aw.NextMoveCounter = 10
			}
		}
		// Tick
		aw.NextMoveCounter--
	}

	return true
}

func (aw *NavAirwork) Start360(nav Nav) {
	if nav.Rand.Intn(2) == 0 {
		aw.TurnDirection = av.TurnLeft
		aw.Heading = math.OffsetHeading(nav.FlightState.Heading, 1)
	} else {
		aw.TurnDirection = av.TurnRight
		aw.Heading = math.OffsetHeading(nav.FlightState.Heading, -1)
	}
	aw.TurnRate = StandardTurnRate
}

func (aw *NavAirwork) TargetHeading() (math.MagneticHeading, av.TurnDirection, float32) {
	return aw.Heading, aw.TurnDirection, aw.TurnRate
}

func (aw *NavAirwork) TargetAltitude() (float32, float32, bool) {
	return aw.Altitude, float32(util.Select(aw.Dive, 3000, 500)), false
}

func (aw *NavAirwork) TargetSpeed() (float32, float32, bool) {
	if aw.IAS == 0 {
		return 0, 0, false
	}
	return aw.IAS, 10, true
}

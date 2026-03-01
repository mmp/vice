// nav/procedures.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

///////////////////////////////////////////////////////////////////////////
// Procedure turns

type FlyRacetrackPT struct {
	ProcedureTurn         *av.ProcedureTurn
	Fix                   string
	FixLocation           math.Point2LL
	Entry                 av.RacetrackPTEntry
	InboundHeading        float32
	OutboundHeading       float32
	OutboundTurnRate      float32
	OutboundTurnDirection av.TurnDirection
	OutboundLegLength     float32
	State                 int
}

const (
	PTStateApproaching = iota
	PTStateTurningOutbound
	PTStateFlyingOutbound
	PTStateTurningInbound
	PTStateFlyingInbound // parallel entry only
)

type FlyStandard45PT struct {
	ProcedureTurn    *av.ProcedureTurn
	Fix              string
	FixLocation      math.Point2LL
	InboundHeading   float32 // fix->airport
	AwayHeading      float32 // outbound + 45 offset
	State            int
	SecondsRemaining int
}

const (
	PT45StateApproaching = iota
	PT45StateTurningOutbound
	PT45StateFlyingOutbound
	PT45StateTurningAway
	PT45StateFlyingAway
	PT45StateTurningIn
	PT45StateFlyingIn
	PT45StateTurningToIntercept
)

func (nav *Nav) flyProcedureTurnIfNecessary() {
	wp := nav.Waypoints
	if !nav.Approach.Cleared || len(wp) < 2 || wp[0].ProcedureTurn() == nil || nav.Approach.NoPT {
		return
	}

	if wp[0].ProcedureTurn().Entry180NoPT {
		inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)

		acFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if math.HeadingDifference(acFixHeading, inboundHeading) < 90 {
			return
		}
	}

	switch wp[0].ProcedureTurn().Type {
	case av.PTRacetrack:
		// Immediate heading update here (and below) since it's the
		// autopilot doing this at the appropriate time (vs. a controller
		// instruction.)
		nav.Heading = NavHeading{RacetrackPT: MakeFlyRacetrackPT(nav, wp)}
		nav.DeferredNavHeading = nil
	case av.PTStandard45:
		nav.Heading = NavHeading{Standard45PT: MakeFlyStandard45PT(nav, wp)}
		nav.DeferredNavHeading = nil

	default:
		panic("Unhandled procedure turn type")
	}
}

func MakeFlyStandard45PT(nav *Nav, wp []av.Waypoint) *FlyStandard45PT {
	inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	awayHeading := math.OppositeHeading(inboundHeading)
	awayHeading += float32(util.Select(wp[0].ProcedureTurn().RightTurns, -45, 45))

	return &FlyStandard45PT{
		ProcedureTurn:  wp[0].ProcedureTurn(),
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		AwayHeading:    math.NormalizeHeading(awayHeading),
		State:          PTStateApproaching,
	}
}

func MakeFlyRacetrackPT(nav *Nav, wp []av.Waypoint) *FlyRacetrackPT {
	inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	aircraftFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
		nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

	pt := wp[0].ProcedureTurn()

	fp := &FlyRacetrackPT{
		ProcedureTurn:  wp[0].ProcedureTurn(),
		Entry:          pt.SelectRacetrackEntry(inboundHeading, aircraftFixHeading),
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	// Set the outbound heading. For everything but teardrop, it's the
	// opposite of the inbound heading.
	fp.OutboundHeading = math.OppositeHeading(fp.InboundHeading)
	if fp.Entry == av.TeardropEntry {
		// For teardrop, it's offset by 30 degrees, toward the outbound
		// track.
		if pt.RightTurns {
			fp.OutboundHeading = math.NormalizeHeading(fp.OutboundHeading - 30)
		} else {
			fp.OutboundHeading = math.NormalizeHeading(fp.OutboundHeading + 30)
		}
	}

	// Set the outbound turn rate
	fp.OutboundTurnRate = float32(StandardTurnRate)
	if fp.Entry == av.DirectEntryShortTurn {
		// Since we have less than 180 degrees in our turn, turn more
		// slowly so that we more or less end up the right offset distance
		// from the inbound path.
		acFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		diff := math.HeadingDifference(fp.OutboundHeading, acFixHeading)
		fp.OutboundTurnRate = 3 * diff / 180
	}

	// Set the outbound turn method.
	fp.OutboundTurnDirection = util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft)
	if fp.Entry == av.ParallelEntry {
		// Swapped turn direction
		fp.OutboundTurnDirection = util.Select(pt.RightTurns, av.TurnLeft, av.TurnRight)
	} else if fp.Entry == av.TeardropEntry {
		fp.OutboundTurnDirection = av.TurnClosest
	}

	// Figure out the outbound leg length.
	// Specified by the user?
	fp.OutboundLegLength = float32(pt.NmLimit) / 2
	if fp.OutboundLegLength == 0 {
		fp.OutboundLegLength = float32(pt.MinuteLimit) * nav.FlightState.GS / 60
	}
	if fp.OutboundLegLength == 0 {
		// Select a default based on the approach type.
		switch nav.Approach.Assigned.Type {
		case av.ILSApproach, av.LocalizerApproach, av.VORApproach:
			// 1 minute by default on these
			fp.OutboundLegLength = nav.FlightState.GS / 60
		case av.RNAVApproach:
			// 4nm by default for RNAV, though that's the distance from the
			// fix, so turn earlier...
			fp.OutboundLegLength = 2

		default:
			panic(fmt.Sprintf("unhandled approach type: %s", nav.Approach.Assigned.Type))
			//fp.OutboundLegLength = nav.FlightState.GS / 60
		}
	}
	// Lengthen it a bit for teardrop since we're flying along the
	// diagonal.
	if fp.Entry == av.TeardropEntry {
		fp.OutboundLegLength *= 1.5
	}

	return fp
}

func (fp *FlyRacetrackPT) GetHeading(nav *Nav, wxs wx.Sample) (float32, av.TurnDirection, float32) {
	pt := fp.ProcedureTurn

	switch fp.State {
	case PTStateApproaching:
		eta := nav.ETA(fp.FixLocation)
		startTurn := false

		switch fp.Entry {
		case av.DirectEntryShortTurn:
			startTurn = eta < 2

		case av.DirectEntryLongTurn:
			// Turn start is based on lining up for the inbound heading,
			// even though the actual turn will be that plus 180.
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.InboundHeading,
				fp.OutboundTurnDirection, wxs)
		case av.ParallelEntry, av.TeardropEntry:
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.OutboundHeading,
				fp.OutboundTurnDirection, wxs)
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := math.Heading2LL(nav.FlightState.Position, fp.FixLocation, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)

		return fixHeading, av.TurnClosest, StandardTurnRate
	case PTStateTurningOutbound:
		if math.HeadingDifference(nav.FlightState.Heading, fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			fp.State = PTStateFlyingOutbound
		}

		return fp.OutboundHeading, fp.OutboundTurnDirection, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := math.NMDistance2LL(nav.FlightState.Position, fp.FixLocation)

		if fp.Entry == av.TeardropEntry {
			// start the turn when we will intercept the inbound radial
			turn := util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft)
			if d > 0.5 && nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wxs) {
				fp.State = PTStateTurningInbound
			}
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, av.TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if fp.Entry == av.ParallelEntry {
			// Parallel is special: we fly at the 30 degree
			// offset-from-true-inbound heading until it is time to turn to
			// intercept.
			hdg := math.NormalizeHeading(fp.InboundHeading + float32(util.Select(pt.RightTurns, -30, 30)))
			if math.HeadingDifference(nav.FlightState.Heading, hdg) < 1 {
				fp.State = PTStateFlyingInbound
			}
			// This turn is in the opposite direction than usual
			turn := util.Select(!pt.RightTurns, av.TurnRight, av.TurnLeft)
			return hdg, av.TurnDirection(turn), StandardTurnRate
		} else {
			if math.HeadingDifference(nav.FlightState.Heading, fp.InboundHeading) < 1 {
				// otherwise go direct to the fix
				nav.Heading = NavHeading{}
				nav.Altitude = NavAltitude{}
			}

			turn := util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft)
			return fp.InboundHeading, av.TurnDirection(turn), StandardTurnRate
		}

	case PTStateFlyingInbound:
		// This state is only used for ParallelEntry
		turn := util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft)
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wxs) {
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
		}
		hdg := math.NormalizeHeading(fp.InboundHeading + float32(util.Select(pt.RightTurns, -30, 30)))
		return hdg, av.TurnClosest, StandardTurnRate
	default:
		panic("unhandled state")
	}
}

func (fp *FlyRacetrackPT) GetAltitude(nav *Nav) (float32, bool) {
	descend := fp.ProcedureTurn.ExitAltitude != 0 &&
		nav.FlightState.Altitude > float32(fp.ProcedureTurn.ExitAltitude) &&
		fp.State != PTStateApproaching
	return float32(fp.ProcedureTurn.ExitAltitude), descend
}

func (fp *FlyStandard45PT) GetHeading(nav *Nav, wxs wx.Sample) (float32, av.TurnDirection, float32) {
	outboundHeading := math.OppositeHeading(fp.InboundHeading)

	switch fp.State {
	case PT45StateApproaching:
		if nav.shouldTurnForOutbound(fp.FixLocation, outboundHeading, av.TurnClosest, wxs) {
			fp.State = PT45StateTurningOutbound
		}

		// Fly toward the fix until it's time to turn outbound
		fixHeading := math.Heading2LL(nav.FlightState.Position, fp.FixLocation,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		return fixHeading, av.TurnClosest, StandardTurnRate
	case PT45StateTurningOutbound:
		if nav.FlightState.Heading == outboundHeading {
			fp.State = PTStateFlyingOutbound
			fp.SecondsRemaining = 60
		}
		return outboundHeading, av.TurnClosest, StandardTurnRate
	case PT45StateFlyingOutbound:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {

		}
		return outboundHeading, av.TurnClosest, StandardTurnRate
	case PT45StateTurningAway:
		if nav.FlightState.Heading == fp.AwayHeading {
		}

		return fp.AwayHeading, av.TurnClosest, StandardTurnRate
	case PT45StateFlyingAway:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningIn
		}
		return fp.AwayHeading, av.TurnClosest, StandardTurnRate
	case PT45StateTurningIn:
		hdg := math.OppositeHeading(fp.AwayHeading)
		if nav.FlightState.Heading == hdg {
			fp.State = PT45StateFlyingIn
		}

		turn := util.Select(fp.ProcedureTurn.RightTurns, av.TurnRight, av.TurnLeft)
		return hdg, av.TurnDirection(turn), StandardTurnRate
	case PT45StateFlyingIn:
		turn := util.Select(fp.ProcedureTurn.RightTurns, av.TurnRight, av.TurnLeft)
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wxs) {
			fp.State = PT45StateTurningToIntercept
		}
		return nav.FlightState.Heading, av.TurnClosest, StandardTurnRate
	case PT45StateTurningToIntercept:
		if nav.FlightState.Heading == fp.InboundHeading {
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
		}

		return fp.InboundHeading, av.TurnClosest, StandardTurnRate
	default:
		return nav.FlightState.Heading, av.TurnClosest, StandardTurnRate
	}
}

///////////////////////////////////////////////////////////////////////////
// Holds

type FlyHold struct {
	Hold         av.Hold
	FixLocation  math.Point2LL
	State        HoldState
	LegStartTime time.Time
	LegStartPos  math.Point2LL
	Entry        av.HoldEntry
	Cancel       bool // when set, we end the hold after the last leg
}

type HoldState int

const (
	// Everyone starts here and then transitions to one of the next three groups depending on their entry method.
	HoldStateApproaching HoldState = iota

	HoldStateDirectTurningInitialOutbound

	HoldStateTurningForParallelEntry
	HoldStateFlyingParallelOutbound
	HoldStateTurningParallelInbound

	HoldStateFlyingTeardropOutbound
	HoldStateTurningForTeardropEntry

	// All holds cycle through these once after entry.
	HoldStateTurningOutbound
	HoldStateFlyingOutbound
	HoldStateTurningInbound
	HoldStateFlyingInbound
)

func (s HoldState) String() string {
	return []string{"Approaching", "DirectTurningInitialOutbound", "TurningForParallelEntry", "FlyingParallelOutbound",
		"TurningParallelInbound", "FlyingTeardropOutbound", "TurningForTeardropEntry", "TurningOutbound",
		"FlyingOutbound", "TurningInbound", "FlyingInbound"}[int(s)]
}

// Holds are implemented using a simple state machine where each state is handled by a function with
// this signature.  Return values: heading to fly, which direction to turn, and which state to be in
// for the next step.
type HoldStateFunc func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState)

var holdStateMachine map[HoldState]HoldStateFunc

func init() {
	holdStateMachine = map[HoldState]HoldStateFunc{
		HoldStateApproaching: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			switch fh.Entry {
			case av.HoldEntryDirect:
				// Overfly the fix before starting to turn
				if nav.ETA(fh.FixLocation) < 2 {
					return nav.FlightState.Heading, av.TurnClosest, HoldStateDirectTurningInitialOutbound
				}

			case av.HoldEntryParallel:
				outbound := math.OppositeHeading(fh.Hold.InboundCourse)
				outbound -= nav.FlightState.MagneticVariation

				if nav.shouldTurnForOutbound(fh.FixLocation, outbound, av.TurnClosest, wxs) {
					return outbound, av.TurnClosest, HoldStateTurningForParallelEntry
				}

			case av.HoldEntryTeardrop:
				if nav.ETA(fh.FixLocation) < 2 {
					// For teardrop, we want to overfly the fix before we start the entry procedure
					return nav.FlightState.Heading, av.TurnClosest, HoldStateFlyingTeardropOutbound
				}
			}

			hdg := math.Heading2LL(nav.FlightState.Position, fh.FixLocation, nav.FlightState.NmPerLongitude, 0)
			hdg = nav.headingForTrack(hdg, wxs)
			return hdg, av.TurnClosest, HoldStateApproaching
		},

		HoldStateDirectTurningInitialOutbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			// Direct entry: turn to be on the inbound course before starting the outbound turn
			// (i.e. don't cut the corner if we're not entering more or less already along the
			// inbound course.)
			inbound := nav.headingForTrack(fh.Hold.InboundCourse, wxs)
			inbound -= nav.FlightState.MagneticVariation
			if math.HeadingDifference(nav.FlightState.Heading, inbound) < 1 {
				return inbound, av.TurnClosest, HoldStateTurningOutbound
			}
			return inbound, av.TurnClosest, HoldStateDirectTurningInitialOutbound

		},

		HoldStateTurningForParallelEntry: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			outbound := nav.headingForTrack(math.OppositeHeading(fh.Hold.InboundCourse), wxs)
			outbound -= nav.FlightState.MagneticVariation
			if math.HeadingDifference(nav.FlightState.Heading, outbound) < 1 {
				return outbound, av.TurnClosest, HoldStateFlyingParallelOutbound
			}
			return outbound, av.TurnClosest, HoldStateTurningForParallelEntry
		},

		HoldStateFlyingParallelOutbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			outbound := nav.headingForTrack(math.OppositeHeading(fh.Hold.InboundCourse), wxs)
			outbound -= nav.FlightState.MagneticVariation

			sec := 70 + wxs.Component(fh.Hold.InboundCourse)
			if simTime.Sub(fh.LegStartTime) < time.Duration(sec)*time.Second {
				return outbound, av.TurnClosest, HoldStateFlyingParallelOutbound
			}
			return outbound, av.TurnClosest, HoldStateTurningParallelInbound
		},

		HoldStateTurningParallelInbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			offset := float32(util.Select(fh.Hold.TurnDirection == av.TurnRight, -40, 40))
			intercept := nav.headingForTrack(fh.Hold.InboundCourse+offset, wxs)
			intercept -= nav.FlightState.MagneticVariation

			if math.HeadingDifference(nav.FlightState.Heading, intercept) < 1 {
				turn := util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnRight, av.TurnLeft)
				if nav.shouldTurnToIntercept(fh.FixLocation, fh.Hold.InboundCourse, turn, wxs) {
					return intercept, turn, HoldStateFlyingInbound
				}
			}

			// Note: intentionally flipped!
			turn := util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnLeft, av.TurnRight)
			return intercept, turn, HoldStateTurningParallelInbound
		},

		HoldStateFlyingTeardropOutbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			offset := float32(util.Select(fh.Hold.TurnDirection == av.TurnRight, 150, -150))
			offset -= nav.FlightState.MagneticVariation
			hdg := nav.headingForTrack(fh.Hold.InboundCourse+offset, wxs)

			if math.HeadingDifference(nav.FlightState.Heading, hdg) < 1 {
				sec := 70 + wxs.Component(fh.Hold.InboundCourse)
				if simTime.Sub(fh.LegStartTime) > time.Duration(sec)*time.Second {
					return hdg, av.TurnClosest, HoldStateTurningForTeardropEntry
				}
			}
			return hdg, av.TurnClosest, HoldStateFlyingTeardropOutbound
		},

		HoldStateTurningForTeardropEntry: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			offset := float32(util.Select(fh.Hold.TurnDirection == av.TurnRight, 150, -150))
			hdg := math.OppositeHeading(fh.Hold.InboundCourse + offset)
			hdg = nav.headingForTrack(hdg, wxs)
			turn := util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnRight, av.TurnLeft)

			if nav.shouldTurnToIntercept(fh.FixLocation, fh.Hold.InboundCourse, turn, wxs) {
				return fh.Hold.InboundCourse, turn, HoldStateFlyingInbound
			}

			return hdg, turn, HoldStateTurningForTeardropEntry
		},

		HoldStateTurningOutbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			outbound := nav.headingForTrack(math.OppositeHeading(fh.Hold.InboundCourse), wxs)
			outbound -= nav.FlightState.MagneticVariation
			if math.HeadingDifference(nav.FlightState.Heading, outbound) < 1 {
				return outbound, av.TurnClosest, HoldStateFlyingOutbound
			}
			turn := util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnRight, av.TurnLeft)
			return outbound, turn, HoldStateTurningOutbound
		},

		HoldStateFlyingOutbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			done := func() bool {
				if fh.Hold.LegLengthNM > 0 {
					dist := math.NMDistance2LL(fh.LegStartPos, nav.FlightState.Position)
					return dist >= fh.Hold.LegLengthNM
				} else {
					mins := fh.Hold.LegMinutes
					if mins == 0 {
						mins = float32(util.Select(nav.FlightState.Altitude < 14000, 1.0, 1.5))
					}

					windComp := wxs.Component(fh.Hold.InboundCourse)
					adjSeconds := mins*60 + windComp*mins
					return simTime.Sub(fh.LegStartTime) >= time.Duration(adjSeconds)*time.Second
				}
			}()

			outbound := nav.headingForTrack(math.OppositeHeading(fh.Hold.InboundCourse), wxs)
			outbound -= nav.FlightState.MagneticVariation
			if done {
				return outbound, av.TurnClosest, HoldStateTurningInbound
			}
			return outbound, av.TurnClosest, HoldStateFlyingOutbound
		},

		HoldStateTurningInbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			turn := util.Select(fh.Hold.TurnDirection == av.TurnRight, av.TurnRight, av.TurnLeft)

			if math.HeadingDifference(nav.FlightState.Heading, fh.Hold.InboundCourse) > 60 {
				// Initial turn off the outbound leg
				return fh.Hold.InboundCourse, turn, HoldStateTurningInbound
			}

			if !nav.shouldTurnToIntercept(fh.FixLocation, fh.Hold.InboundCourse, turn, wxs) {
				// Don't finish the turn and instead fly present heading a bit until the point at
				// which finishing the turn would have us roll out to the inbound course.
				return nav.FlightState.Heading, turn, HoldStateTurningInbound
			}

			// Wrap up the turn and start flying the inbound leg.
			return fh.Hold.InboundCourse, av.TurnClosest, HoldStateFlyingInbound
		},

		HoldStateFlyingInbound: func(nav *Nav, fh *FlyHold, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, HoldState) {
			// Fly direct to the fix; hopefully this will be close to the inbound heading.
			hdg := math.Heading2LL(nav.FlightState.Position, fh.FixLocation, nav.FlightState.NmPerLongitude, 0)
			hdg = nav.headingForTrack(hdg, wxs)

			if nav.ETA(fh.FixLocation) < 2 {
				if fh.Cancel {
					nav.Heading = NavHeading{} // switch to straight-up direct to the next fix
				} else {
					return hdg, av.TurnClosest, HoldStateTurningOutbound
				}
			}
			return hdg, av.TurnClosest, HoldStateFlyingInbound
		},
	}
}

func (fh *FlyHold) GetHeading(callsign string, nav *Nav, wxs wx.Sample, simTime time.Time) (float32, av.TurnDirection, float32) {
	hdg, turn, newState := holdStateMachine[fh.State](nav, fh, wxs, simTime)

	if newState != fh.State {
		NavLog(callsign, simTime, NavLogHold, "STATE CHANGE: %s -> %s (fix=%s entry=%s)",
			fh.State.String(), newState.String(), fh.Hold.Fix, fh.Entry.String())
		fh.State = newState
		fh.LegStartPos = nav.FlightState.Position
		fh.LegStartTime = simTime
	}

	dist := math.NMDistance2LL(nav.FlightState.Position, fh.FixLocation)
	NavLog(callsign, simTime, NavLogHold, "state=%s acHdg=%.1f targetHdg=%.1f turn=%v dist=%.1fnm timer=%s",
		fh.State.String(), nav.FlightState.Heading, hdg, turn, dist, simTime.Sub(fh.LegStartTime))

	return hdg, turn, StandardTurnRate
}

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
		aw.Heading = math.Heading2LL(nav.FlightState.Position, aw.Center, nav.FlightState.NmPerLongitude,
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
			aw.NextMoveCounter = 5 + nav.Rand.Intn(25)
		} else if aw.NextMoveCounter == 1 {
			// Pick a new thing.
			aw.ToCenter = false
			if nav.Rand.Float32() < .2 {
				// Do a 360
				aw.Start360(*nav)
			} else if nav.FlightState.Altitude > aw.AltRange[0]+2000 && nav.Rand.Float32() < .2 {
				// Dive.
				aw.Dive = true
				aw.Altitude = aw.AltRange[0] + 200*nav.Rand.Float32()
			} else if nav.FlightState.Altitude+1000 < aw.AltRange[1] && nav.Rand.Float32() < .2 {
				// Climbing turn
				aw.Altitude = aw.AltRange[1] - 500*nav.Rand.Float32()
				aw.Heading = 360 * nav.Rand.Float32()
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, av.TurnLeft, av.TurnRight)
			} else if nav.FlightState.Altitude < aw.AltRange[0]+1000 && nav.Rand.Float32() < .2 {
				// Descending turn
				aw.Altitude = aw.AltRange[0] + 500*nav.Rand.Float32()
				aw.Heading = 360 * nav.Rand.Float32()
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, av.TurnLeft, av.TurnRight)
			} else if nav.Rand.Float32() < .2 {
				// Slow turn
				aw.Heading = 360 * nav.Rand.Float32()
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
		aw.Heading = math.NormalizeHeading(nav.FlightState.Heading + 1)
	} else {
		aw.TurnDirection = av.TurnRight
		aw.Heading = math.NormalizeHeading(nav.FlightState.Heading - 1)
	}
	aw.TurnRate = StandardTurnRate
}

func (aw *NavAirwork) TargetHeading() (heading float32, turn av.TurnDirection, rate float32) {
	return aw.Heading, aw.TurnDirection, aw.TurnRate
}

func (aw *NavAirwork) TargetAltitude() (float32, float32) {
	return aw.Altitude, float32(util.Select(aw.Dive, 3000, 500))
}

func (aw *NavAirwork) TargetSpeed() (float32, float32, bool) {
	if aw.IAS == 0 {
		return 0, 0, false
	}
	return aw.IAS, 10, true
}

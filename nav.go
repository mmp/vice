// nav.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/davecgh/go-spew/spew"
)

const MaximumRate = 100000

type NAVState struct {
	L LNavCommand
	S SNavCommand
	V VNavCommand

	FutureCommands map[FutureNavCommand]interface{}
}

func (n *NAVState) Summary(ac *Aircraft) string {
	var info []string
	info = append(info, n.L.LSummary(ac), n.S.SSummary(ac), n.V.VSummary(ac))

	var futinfo []string
	for cmd := range n.FutureCommands {
		futinfo = append(futinfo, cmd.Summary(ac))
	}
	sort.Strings(futinfo)
	info = append(info, futinfo...)

	info = FilterSlice(info, func(s string) bool { return s != "" })
	return strings.Join(info, "\n")
}

type NAVStateMarshal struct {
	LNavType       string
	LNavStruct     string
	SNavType       string
	SNavStruct     string
	VNavType       string
	VNavStruct     string
	FutureCommands [][2]string
}

func (n *NAVState) MarshalJSON() ([]byte, error) {
	var m NAVStateMarshal

	// LNav
	m.LNavType = fmt.Sprintf("%T", n.L)

	switch lnav := n.L.(type) {
	case *FlyHeading, *FlyRoute, *FlyRacetrackPT, *FlyStandard45PT:
		b, err := json.Marshal(lnav)
		if err != nil {
			return nil, err
		}
		m.LNavStruct = string(b)
	default:
		panic("unhandled lnav command type")
	}

	// SNav
	m.SNavType = fmt.Sprintf("%T", n.S)

	switch snav := n.S.(type) {
	case *MaintainSpeed, *FlyRoute, *FinalApproachSpeed:
		b, err := json.Marshal(snav)
		if err != nil {
			return nil, err
		}
		m.SNavStruct = string(b)
	default:
		panic("unhandled snav command type")
	}

	// VNav
	m.VNavType = fmt.Sprintf("%T", n.V)

	switch vnav := n.V.(type) {
	case *MaintainAltitude, *FlyRoute, *FlyRacetrackPT:
		b, err := json.Marshal(vnav)
		if err != nil {
			return nil, err
		}
		m.VNavStruct = string(b)
	default:
		panic("unhandled vnav command type")
	}

	// FutureCommands
	for cmd := range n.FutureCommands {
		switch c := cmd.(type) {
		case *SpeedAfterAltitude, *AltitudeAfterSpeed, *ApproachSpeedAt5DME, *ClimbOnceAirborne,
			*TurnToInterceptLocalizer, *HoldLocalizerAfterIntercept, *GoAround:
			s, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			m.FutureCommands = append(m.FutureCommands, [2]string{fmt.Sprintf("%T", c), string(s)})
		}
	}

	return json.Marshal(m)
}

func unmarshalStruct[T any](s string) (*T, error) {
	var t T
	err := json.Unmarshal([]byte(s), &t)
	return &t, err
}

func (n *NAVState) UnmarshalJSON(s []byte) error {
	var m NAVStateMarshal
	err := json.Unmarshal(s, &m)
	if err != nil {
		return err
	}

	switch m.LNavType {
	case "*main.FlyRoute":
		n.L, err = unmarshalStruct[FlyRoute](m.LNavStruct)
	case "*main.FlyHeading":
		n.L, err = unmarshalStruct[FlyHeading](m.LNavStruct)
	case "*main.FlyRacetrackPT":
		n.L, err = unmarshalStruct[FlyRacetrackPT](m.LNavStruct)
	case "*main.FlyStandard45PT":
		n.L, err = unmarshalStruct[FlyStandard45PT](m.LNavStruct)
	default:
		panic("unhandled lnav command")
	}
	if err != nil {
		return err
	}

	switch m.SNavType {
	case "*main.MaintainSpeed":
		n.S, err = unmarshalStruct[MaintainSpeed](m.SNavStruct)
	case "*main.FlyRoute":
		n.S, err = unmarshalStruct[FlyRoute](m.SNavStruct)
	case "*main.FinalApproachSpeed":
		n.S, err = unmarshalStruct[FinalApproachSpeed](m.SNavStruct)
	default:
		panic("unhandled snav command")
	}
	if err != nil {
		return err
	}

	switch m.VNavType {
	case "*main.MaintainAltitude":
		n.V, err = unmarshalStruct[MaintainAltitude](m.VNavStruct)
	case "*main.FlyRoute":
		n.V, err = unmarshalStruct[FlyRoute](m.VNavStruct)
	case "*main.FlyRacetrackPT":
		n.V, err = unmarshalStruct[FlyRacetrackPT](m.VNavStruct)
	default:
		panic("unhandled vnav command")
	}
	if err != nil {
		return err
	}

	n.FutureCommands = make(map[FutureNavCommand]interface{})

	for _, cmd := range m.FutureCommands {
		var fnc FutureNavCommand
		switch cmd[0] {
		case "*main.SpeedAfterAltitude":
			fnc, err = unmarshalStruct[SpeedAfterAltitude](cmd[1])
		case "*main.AltitudeAfterSpeed":
			fnc, err = unmarshalStruct[AltitudeAfterSpeed](cmd[1])
		case "*main.ApproachSpeedAt5DME":
			fnc, err = unmarshalStruct[ApproachSpeedAt5DME](cmd[1])
		case "*main.ClimbOnceAirborne":
			fnc, err = unmarshalStruct[ClimbOnceAirborne](cmd[1])
		case "*main.TurnToInterceptLocalizer":
			fnc, err = unmarshalStruct[TurnToInterceptLocalizer](cmd[1])
		case "*main.HoldLocalizerAfterIntercept":
			fnc, err = unmarshalStruct[HoldLocalizerAfterIntercept](cmd[1])
		case "*main.GoAround":
			fnc, err = unmarshalStruct[GoAround](cmd[1])
		default:
			panic("unhandled future command")
		}
		if err != nil {
			return err
		}
		n.FutureCommands[fnc] = nil
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// FutureNavCommand

type FutureNavCommand interface {
	Evaluate(ac *Aircraft) bool
	Summary(ac *Aircraft) string
}

type SpeedAfterAltitude struct {
	Altitude  float32
	FromAbove bool
	IAS       float32
}

func (saa *SpeedAfterAltitude) Evaluate(ac *Aircraft) bool {
	if (saa.FromAbove && ac.Altitude > saa.Altitude) ||
		(!saa.FromAbove && ac.Altitude < saa.Altitude) {
		return false
	}

	// Don't override final approach speed if it's in effect.
	if _, ok := ac.Nav.S.(*FinalApproachSpeed); !ok {
		ac.Nav.S = &MaintainSpeed{IAS: saa.IAS}
	}

	return true
}

func (saa *SpeedAfterAltitude) Summary(ac *Aircraft) string {
	return fmt.Sprintf("At %.0f feet, maintain %.0f knots", saa.Altitude, saa.IAS)
}

type AltitudeAfterSpeed struct {
	FromAbove bool
	IAS       float32
	Altitude  float32
}

func (aas *AltitudeAfterSpeed) Evaluate(ac *Aircraft) bool {
	if (aas.FromAbove && ac.IAS > aas.IAS) ||
		(!aas.FromAbove && ac.IAS < aas.IAS) {
		return false
	}

	ac.Nav.V = &MaintainAltitude{Altitude: aas.Altitude}
	return true
}

func (aas *AltitudeAfterSpeed) Summary(ac *Aircraft) string {
	if aas.Altitude < ac.Altitude {
		return fmt.Sprintf("At %.0f knots, descend and maintain %.0f",
			aas.IAS, aas.Altitude)
	} else {
		return fmt.Sprintf("At %.0f knots, climb and maintain %.0f",
			aas.IAS, aas.Altitude)
	}
}

type ApproachSpeedAt5DME struct{}

func (as *ApproachSpeedAt5DME) Evaluate(ac *Aircraft) bool {
	d, err := ac.FinalApproachDistance()
	if err != nil {
		ap := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport]
		d = nmdistance2ll(ac.Position, ap.Location)
	}

	if d > 5 {
		return false
	}

	lg.Printf("%s: at 5 DME; snav reducing to final approach (if not already)",
		ac.Callsign)
	ac.Nav.S = &FinalApproachSpeed{}
	return true
}

func (as *ApproachSpeedAt5DME) Summary(ac *Aircraft) string {
	return "Reduce to approach speed at 5 DME"
}

type ClimbOnceAirborne struct {
	Altitude float32
}

func (ca *ClimbOnceAirborne) Evaluate(ac *Aircraft) bool {
	// Only considers speed; assumes that this is part of the takeoff
	// commands...
	if ac.IAS < 1.1*ac.Performance.Speed.Min {
		return false
	}

	ac.Nav.V = &MaintainAltitude{Altitude: ca.Altitude}
	return true
}

func (ca *ClimbOnceAirborne) Summary(ac *Aircraft) string {
	return fmt.Sprintf("Climb and maintain %.0f once airborne", ca.Altitude)
}

type TurnToInterceptLocalizer struct{}

func (il *TurnToInterceptLocalizer) Evaluate(ac *Aircraft) bool {
	ap := ac.Approach
	if ap.Type != ILSApproach {
		panic("not an ils approach")
	}

	// allow a lot of slop, but just fly through the localizer if it's too
	// sharp an intercept
	if headingDifference(float32(ap.Heading()), ac.Heading) > 45 {
		return false
	}

	loc := ap.Line()

	if ac.ShouldTurnToIntercept(loc[0], ap.Heading(), TurnClosest) {
		lg.Printf("%s: assigned approach heading! %.1f", ac.Callsign, ap.Heading())

		ac.Nav.L = &FlyHeading{Heading: float32(ap.Heading())}
		// Just in case.. Thus we will be ready to pick up the
		// approach waypoints once we capture.
		ac.Waypoints = nil

		ac.AddFutureNavCommand(&HoldLocalizerAfterIntercept{})
		return true
	}

	return false
}

func (il *TurnToInterceptLocalizer) Summary(ac *Aircraft) string {
	return "Turn to intercept the localizer"
}

type HoldLocalizerAfterIntercept struct{}

func (hl *HoldLocalizerAfterIntercept) Evaluate(ac *Aircraft) bool {
	ap := ac.Approach
	loc := ap.Line()
	dist := PointLineDistance(ll2nm(ac.Position), ll2nm(loc[0]), ll2nm(loc[1]))
	if dist > .2 {
		return false
	}

	// we'll call that good enough. Now we need to figure out which
	// fixes in the approach are still ahead and then add them to
	// the aircraft's waypoints.
	n := len(ap.Waypoints[0])
	threshold := ll2nm(ap.Waypoints[0][n-1].Location)
	thresholdDistance := distance2f(ll2nm(ac.Position), threshold)
	lg.Printf("%s: intercepted the localizer @ %.2fnm!", ac.Callsign, thresholdDistance)

	ac.Waypoints = nil
	for i, wp := range ap.Waypoints[0] {
		// Find the first waypoint that is:
		// 1. In front of the aircraft.
		// 2. Closer to the threshold than the aircraft.
		// 3. On the localizer
		if i+1 < len(ap.Waypoints[0]) {
			wpToThresholdHeading := headingp2ll(wp.Location, ap.Waypoints[0][n-1].Location, scenarioGroup.MagneticVariation)
			lg.Errorf("%s: wpToThresholdHeading %f", wp.Fix, wpToThresholdHeading)
			if headingDifference(wpToThresholdHeading, float32(ap.Heading())) > 3 {
				lg.Errorf("%s: fix is in front but not on the localizer", wp.Fix)
				continue
			}
		}

		acToWpHeading := headingp2ll(ac.Position, wp.Location, scenarioGroup.MagneticVariation)
		inFront := headingDifference(ac.Heading, acToWpHeading) < 70
		lg.Printf("%s: %s ac heading %f wp heading %f in front %v threshold distance %f",
			ac.Callsign, wp.Fix, ac.Heading, acToWpHeading, inFront, thresholdDistance)
		if inFront && distance2f(ll2nm(wp.Location), threshold) < thresholdDistance {
			ac.Waypoints = ap.Waypoints[0][i:]
			lg.Printf("%s: added future waypoints %s...", ac.Callsign, spew.Sdump(ac.Waypoints))
			break
		}
	}

	ac.Nav.L = &FlyRoute{}
	ac.Nav.V = &FlyRoute{}
	if !ac.HaveAssignedSpeed() {
		ac.Nav.S = &FinalApproachSpeed{} // otherwise keep assigned speed until 5 DME
	}

	return true
}

func (hl *HoldLocalizerAfterIntercept) Summary(ac *Aircraft) string {
	return "Remain on the localizer"
}

type GoAround struct {
	AirportDistance float32
}

func (g *GoAround) Evaluate(ac *Aircraft) bool {
	ap := database.Airports[ac.FlightPlan.ArrivalAirport]
	if dist := nmdistance2ll(ac.Position, ap.Location); dist > g.AirportDistance {
		return false
	}

	sim.GoAround(ac)
	return true
}

func (g *GoAround) Summary(ac *Aircraft) string {
	return "" // We don't let the user see this will happen
}

///////////////////////////////////////////////////////////////////////////
// LNavCommand and implementations

type LNavCommand interface {
	GetHeading(ac *Aircraft) (float32, TurnMethod, float32) // heading, turn type, rate
	PassesWaypoints() bool
	LSummary(ac *Aircraft) string
}

type FlyHeading struct {
	Heading float32
	Turn    TurnMethod
	Rate    float32
}

func (fh *FlyHeading) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	if fh.Rate == 0 {
		// 3 degree/s turn by default
		return fh.Heading, fh.Turn, StandardTurnRate
	} else {
		return fh.Heading, fh.Turn, fh.Rate
	}
}

func (fh *FlyHeading) PassesWaypoints() bool {
	return false
}

func (fh *FlyHeading) LSummary(ac *Aircraft) string {
	switch fh.Turn {
	case TurnClosest:
		return fmt.Sprintf("Fly heading %.0f", fh.Heading)
	case TurnLeft:
		return fmt.Sprintf("Turn left heading %.0f", fh.Heading)
	case TurnRight:
		return fmt.Sprintf("Turn right heading %.0f", fh.Heading)
	default:
		return "Unhandled turn"
	}
}

type FlyRoute struct{}

func (fr *FlyRoute) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	if len(ac.Waypoints) == 0 {
		return ac.Heading, TurnClosest, StandardTurnRate
	} else {
		hdg := headingp2ll(ac.Position, ac.Waypoints[0].Location,
			scenarioGroup.MagneticVariation)
		return hdg, TurnClosest, StandardTurnRate
	}
}

func (fr *FlyRoute) PassesWaypoints() bool {
	return true
}

func (fr *FlyRoute) LSummary(ac *Aircraft) string {
	if len(ac.Waypoints) == 0 {
		return "Fly present heading"
	} else {
		wp := ac.Waypoints[0]
		s := "Fly assigned route, next fix is " + wp.Fix
		if wp.Altitude != 0 {
			s += fmt.Sprintf(", cross at %d ft", wp.Altitude)
		}
		if wp.Speed != 0 {
			s += fmt.Sprintf(", cross at %d kts", wp.Speed)
		}
		if wp.Heading != 0 {
			s += fmt.Sprintf(", depart heading %d", wp.Heading)
		}
		return s
	}
}

const (
	PTStateApproaching = iota
	PTStateTurningOutbound
	PTStateFlyingOutbound
	PTStateTurningInbound
	PTStateFlyingInbound // parallel entry only
)

type FlyRacetrackPT struct {
	ProcedureTurn      *ProcedureTurn
	Fix                string
	FixLocation        Point2LL
	Entry              RacetrackPTEntry
	InboundHeading     float32
	OutboundHeading    float32
	OutboundTurnRate   float32
	OutboundTurnMethod TurnMethod
	OutboundLegLength  float32
	State              int
}

func (fp *FlyRacetrackPT) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	pt := fp.ProcedureTurn

	switch fp.State {
	case PTStateApproaching:
		dist := nmdistance2ll(ac.Position, fp.FixLocation)
		eta := dist / ac.GS * 3600 // in seconds
		startTurn := false

		switch fp.Entry {
		case DirectEntryShortTurn:
			startTurn = eta < 2

		case DirectEntryLongTurn:
			// Turn start is based on lining up for the inbound heading,
			// even though the actual turn will be that plus 180.
			startTurn = ac.ShouldTurnForOutbound(fp.FixLocation, fp.InboundHeading, fp.OutboundTurnMethod)

		case ParallelEntry, TeardropEntry:
			startTurn = ac.ShouldTurnForOutbound(fp.FixLocation, fp.OutboundHeading, fp.OutboundTurnMethod)
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
			lg.Errorf("%s: starting outbound turn-heading %.1f rate %.2f method %s",
				ac.Callsign, fp.OutboundHeading, fp.OutboundTurnRate,
				fp.OutboundTurnMethod.String())
			//lg.Errorf("%s: full fp %s", ac.Callsign, spew.Sdump(fp))
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := headingp2ll(ac.Position, fp.FixLocation, scenarioGroup.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PTStateTurningOutbound:
		if headingDifference(ac.Heading, fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			lg.Errorf("%s: finished the turn; ac heading %.1f outbound %.1f; flying outbound leg",
				ac.Callsign, ac.Heading, fp.OutboundHeading)
			fp.State = PTStateFlyingOutbound
		}

		return fp.OutboundHeading, fp.OutboundTurnMethod, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := nmdistance2ll(ac.Position, fp.FixLocation)

		if fp.Entry == TeardropEntry {
			// start the turn when we will intercept the inbound radial
			turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
			if d > 0.5 && ac.ShouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn) {
				lg.Errorf("%s: teardrop Turning inbound!", ac.Callsign)
				fp.State = PTStateTurningInbound
			}
		} else if d > fp.OutboundLegLength {
			lg.Errorf("%s: Turning inbound!", ac.Callsign)
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if fp.Entry == ParallelEntry {
			// Parallel is special: we fly at the 30 degree
			// offset-from-true-inbound heading until it is time to turn to
			// intercept.
			hdg := NormalizeHeading(fp.InboundHeading + float32(Select(pt.RightTurns, -30, 30)))
			lg.Printf("%s: parallel inbound turning to %.1f", ac.Callsign, hdg)
			if headingDifference(ac.Heading, hdg) < 1 {
				fp.State = PTStateFlyingInbound
			}
			// This turn is in the opposite direction than usual
			turn := Select(!pt.RightTurns, TurnRight, TurnLeft)
			return hdg, TurnMethod(turn), StandardTurnRate
		} else {
			if headingDifference(ac.Heading, fp.InboundHeading) < 1 {
				// otherwise go direct to the fix
				lg.Errorf("%s: direct fix--done with the HILPT!", ac.Callsign)
				ac.Nav.L = &FlyRoute{}
				ac.Nav.V = &FlyRoute{}
			}

			turn := Select(pt.RightTurns, TurnRight, TurnLeft)
			return fp.InboundHeading, TurnMethod(turn), StandardTurnRate
		}

	case PTStateFlyingInbound:
		// This state is only used for ParallelEntry
		turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
		if ac.ShouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn) {
			lg.Errorf("%s: parallel inbound direct fix", ac.Callsign)
			ac.Nav.L = &FlyRoute{}
			ac.Nav.V = &FlyRoute{}
		}
		hdg := NormalizeHeading(fp.InboundHeading + float32(Select(pt.RightTurns, -30, 30)))
		return hdg, TurnClosest, StandardTurnRate

	default:
		panic("unhandled state")
	}
}

func (fp *FlyRacetrackPT) shouldDescend(ac *Aircraft) bool {
	return fp.ProcedureTurn.ExitAltitude != 0 && ac.Altitude > float32(fp.ProcedureTurn.ExitAltitude) &&
		fp.State != PTStateApproaching
}

func (fp *FlyRacetrackPT) GetAltitude(ac *Aircraft) (float32, float32) {
	if fp.shouldDescend(ac) {
		return float32(fp.ProcedureTurn.ExitAltitude), MaximumRate
	} else {
		fr := &FlyRoute{}
		return fr.GetAltitude(ac)
	}
}

func (fp *FlyRacetrackPT) PassesWaypoints() bool {
	return false
}

func (fp *FlyRacetrackPT) VSummary(ac *Aircraft) string {
	if fp.ProcedureTurn.ExitAltitude != 0 && ac.Altitude > float32(fp.ProcedureTurn.ExitAltitude) {
		return fmt.Sprintf("Descend to %d in the procedure turn", fp.ProcedureTurn.ExitAltitude)
	} else {
		fr := &FlyRoute{}
		return fr.VSummary(ac)
	}
}

func (fp *FlyRacetrackPT) LSummary(ac *Aircraft) string {
	s := fmt.Sprintf("Fly the %s procedure turn at %s", fp.ProcedureTurn.Type, fp.Fix)
	if fp.ProcedureTurn.Type == PTRacetrack {
		s += ", " + fp.Entry.String() + " entry"
	}
	return s
}

func MakeFlyProcedureTurn(ac *Aircraft, wp []Waypoint) (LNavCommand, VNavCommand) {
	if len(wp) < 2 {
		lg.Errorf("%s: MakeFlyProcedureTurn called with insufficient waypoints: %s",
			ac.Callsign, spew.Sdump(wp))
		return nil, nil
	}

	switch wp[0].ProcedureTurn.Type {
	case PTRacetrack:
		return MakeFlyRacetrackPT(ac, wp)

	case PTStandard45:
		return MakeFlyStandard45PT(ac, wp)

	default:
		lg.Errorf("Unhandled procedure turn type")
		return nil, nil
	}
}

type FlyStandard45PT struct {
	ProcedureTurn    *ProcedureTurn
	Fix              string
	FixLocation      Point2LL
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

func (fp *FlyStandard45PT) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	outboundHeading := OppositeHeading(fp.InboundHeading)

	switch fp.State {
	case PT45StateApproaching:
		if ac.ShouldTurnForOutbound(fp.FixLocation, outboundHeading, TurnClosest) {
			lg.Printf("%s: turning outbound to %.0f", ac.Callsign, outboundHeading)
			fp.State = PT45StateTurningOutbound
		}

		// Fly toward the fix until it's time to turn outbound
		fixHeading := headingp2ll(ac.Position, fp.FixLocation, scenarioGroup.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningOutbound:
		if ac.Heading == outboundHeading {
			fp.State = PTStateFlyingOutbound
			fp.SecondsRemaining = 60
			lg.Printf("%s: flying outbound for %ds", ac.Callsign, fp.SecondsRemaining)
		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingOutbound:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningAway
			lg.Printf("%s: turning away from outbound to %.0f", ac.Callsign, fp.AwayHeading)

		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningAway:
		if ac.Heading == fp.AwayHeading {
			fp.State = PT45StateFlyingAway
			fp.SecondsRemaining = 60
			lg.Printf("%s: flying away for %ds", ac.Callsign, fp.SecondsRemaining)
		}

		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingAway:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningIn
			lg.Printf("%s: turning in to %.0f", ac.Callsign, OppositeHeading(fp.AwayHeading))
		}
		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningIn:
		hdg := OppositeHeading(fp.AwayHeading)
		if ac.Heading == hdg {
			fp.State = PT45StateFlyingIn
			lg.Printf("%s: flying in", ac.Callsign)
		}

		turn := Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft)
		return hdg, TurnMethod(turn), StandardTurnRate

	case PT45StateFlyingIn:
		turn := TurnMethod(Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft))
		if ac.ShouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn) {
			fp.State = PT45StateTurningToIntercept
			lg.Printf("%s: starting turn to intercept %.0f", ac.Callsign, fp.InboundHeading)
		}
		return ac.Heading, TurnClosest, StandardTurnRate

	case PT45StateTurningToIntercept:
		if ac.Heading == fp.InboundHeading {
			ac.Nav.L = &FlyRoute{}
			lg.Printf("%s: done! direct to the fix now", ac.Callsign)
		}

		return fp.InboundHeading, TurnClosest, StandardTurnRate

	default:
		lg.Errorf("unhandled PT state: %d", fp.State)
		return ac.Heading, TurnClosest, StandardTurnRate
	}
}

func (fp *FlyStandard45PT) PassesWaypoints() bool {
	return false
}

func (fp *FlyStandard45PT) LSummary(ac *Aircraft) string {
	return fmt.Sprintf("Fly the standard 45/180 procedure turn at %s", fp.Fix)
}

func MakeFlyStandard45PT(ac *Aircraft, wp []Waypoint) (*FlyStandard45PT, VNavCommand) {
	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location,
		scenarioGroup.MagneticVariation)

	fp := &FlyStandard45PT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	hdg := OppositeHeading(fp.InboundHeading)
	hdg += float32(Select(wp[0].ProcedureTurn.RightTurns, -45, 45))
	fp.AwayHeading = NormalizeHeading(hdg)

	return fp, nil
}

func MakeFlyRacetrackPT(ac *Aircraft, wp []Waypoint) (*FlyRacetrackPT, *FlyRacetrackPT) {
	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location,
		scenarioGroup.MagneticVariation)
	aircraftFixHeading := headingp2ll(ac.Position, wp[0].Location,
		scenarioGroup.MagneticVariation)

	pt := wp[0].ProcedureTurn

	fp := &FlyRacetrackPT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Entry:          pt.SelectRacetrackEntry(inboundHeading, aircraftFixHeading),
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	lg.Printf("%s: entry %s", ac.Callsign, fp.Entry)

	// Set the outbound heading. For everything but teardrop, it's the
	// opposite of the inbound heading.
	fp.OutboundHeading = OppositeHeading(fp.InboundHeading)
	if fp.Entry == TeardropEntry {
		// For teardrop, it's offset by 30 degrees, toward the outbound
		// track.
		if pt.RightTurns {
			fp.OutboundHeading = NormalizeHeading(fp.OutboundHeading - 30)
		} else {
			fp.OutboundHeading = NormalizeHeading(fp.OutboundHeading + 30)
		}
	}

	// Set the outbound turn rate
	fp.OutboundTurnRate = float32(StandardTurnRate)
	if fp.Entry == DirectEntryShortTurn {
		// Since we have less than 180 degrees in our turn, turn more
		// slowly so that we more or less end up the right offset distance
		// from the inbound path.
		acFixHeading := headingp2ll(ac.Position, wp[0].Location,
			scenarioGroup.MagneticVariation)
		diff := headingDifference(fp.OutboundHeading, acFixHeading)
		fp.OutboundTurnRate = 3 * diff / 180
		lg.Printf("%s: hdg %.0f outbound hdg %.0f diff %.0f -> rate %.1f",
			ac.Callsign, acFixHeading, fp.OutboundHeading,
			headingDifference(fp.OutboundHeading, acFixHeading),
			fp.OutboundTurnRate)
	}

	// Set the outbound turn method.
	fp.OutboundTurnMethod = TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
	if fp.Entry == ParallelEntry {
		// Swapped turn direction
		fp.OutboundTurnMethod = TurnMethod(Select(pt.RightTurns, TurnLeft, TurnRight))
	} else if fp.Entry == TeardropEntry {
		// Turn may be left or right, depending on angle; nearest is always
		// correct, though.
		fp.OutboundTurnMethod = TurnClosest
	}

	// Figure out the outbound leg length.
	// Specified by the user?
	fp.OutboundLegLength = float32(pt.NmLimit) / 2
	if fp.OutboundLegLength == 0 {
		fp.OutboundLegLength = float32(pt.MinuteLimit) * ac.GS / 60
	}
	if fp.OutboundLegLength == 0 {
		// Select a default based on the approach type.
		switch ac.Approach.Type {
		case ILSApproach:
			// 1 minute by default on ILS
			fp.OutboundLegLength = ac.GS / 60

		case RNAVApproach:
			// 4nm by default for RNAV, though that's the distance from the
			// fix, so turn earlier...
			fp.OutboundLegLength = 2

		default:
			lg.Errorf("%s: unhandled approach type: %s", ac.Callsign, ac.Approach.Type)
			fp.OutboundLegLength = ac.GS / 60

		}
	}
	// Lengthen it a bit for teardrop since we're flying along the
	// diagonal.
	if fp.Entry == TeardropEntry {
		fp.OutboundLegLength *= 1.5
	}

	return fp, fp
}

///////////////////////////////////////////////////////////////////////////
// SNavCommand and implementations

type SNavCommand interface {
	GetSpeed(ac *Aircraft) (float32, float32) // IAS, rate (kts / minute)
	SSummary(ac *Aircraft) string
}

type MaintainSpeed struct {
	IAS float32
}

func (ms *MaintainSpeed) GetSpeed(ac *Aircraft) (float32, float32) {
	return ms.IAS, MaximumRate
}

func (ms *MaintainSpeed) SSummary(ac *Aircraft) string {
	return fmt.Sprintf("Maintain %.0f kts", ms.IAS)
}

func (fr *FlyRoute) GetSpeed(ac *Aircraft) (float32, float32) {
	if len(ac.Waypoints) > 0 && ac.Waypoints[0].Speed != 0 {
		eta, ok := ac.NextFixETA()
		if !ok {
			return float32(ac.Waypoints[0].Speed), MaximumRate
		}

		cs := float32(ac.Waypoints[0].Speed)

		// Start with a linear acceleration
		rate := abs(cs-ac.IAS) / float32(eta.Seconds())
		if cs < ac.IAS {
			// We're slowing so can take it easy
			rate *= 0.8
		} else {
			// We're speeding up so will start to close distance more
			// quickly, so need a higher rate
			rate *= 1.25
		}

		//lg.Errorf("%s: eta %f seconds IAS %f crossing speed %f -> rate (sec) %f",
		//ac.Callsign, eta.Seconds(), ac.IAS, cs, rate)
		return cs, rate * 60 // per minute
	} else if ac.Altitude < 10000 { // Assume it's a departure(?)
		return min(ac.Performance.Speed.Cruise, float32(250)), MaximumRate
	} else {
		// Assume climbing or descending
		return ac.Performance.Speed.Cruise * 7 / 10, MaximumRate
	}
}

func (fr *FlyRoute) SSummary(ac *Aircraft) string {
	return ""
}

type FinalApproachSpeed struct{}

func (fa *FinalApproachSpeed) GetSpeed(ac *Aircraft) (float32, float32) {
	airportPos, ok := scenarioGroup.Locate(ac.FlightPlan.ArrivalAirport)
	if !ok {
		lg.ErrorfUp1("%s: unable to find airport", ac.FlightPlan.ArrivalAirport)
		return ac.IAS, MaximumRate
	}

	toAirport := headingp2ll(ac.Position, airportPos, scenarioGroup.MagneticVariation)
	if headingDifference(toAirport, ac.Heading) > 30 {
		// Don't slow down if the aircraft isn't facing the airport (e.g.,
		// is in the middle of a procedure turn)
		return ac.IAS, MaximumRate
	}

	airportDist, err := ac.FinalApproachDistance()
	if err != nil {
		lg.Errorf("%s: couldn't get final approach distance: %v", ac.Callsign, err)
		airportDist = nmdistance2ll(ac.Position, airportPos)
	}

	// Expected speed at 10 DME, without further direction.
	spd := ac.Performance.Speed
	approachSpeed := min(1.6*spd.Landing, float32(spd.Cruise))

	if airportDist < 1 {
		return spd.Landing, MaximumRate
	} else if airportDist > 10 {
		// Don't accelerate if the aircraft is already under the target speed.
		return min(approachSpeed, ac.IAS), MaximumRate
	} else {
		return min(lerp((airportDist-1)/9, spd.Landing, approachSpeed), ac.IAS),
			MaximumRate
	}
}

func (fa *FinalApproachSpeed) SSummary(ac *Aircraft) string {
	return "Reduce to final approach speed"
}

/////////////////////////////////////////////////////////////////////////////////////
// VNavCommand and implementations

type VNavCommand interface {
	GetAltitude(ac *Aircraft) (float32, float32) // altitude, rate (feet/minute)
	VSummary(ac *Aircraft) string
}

type MaintainAltitude struct {
	Altitude float32
}

func (ma *MaintainAltitude) GetAltitude(ac *Aircraft) (float32, float32) {
	return ma.Altitude, MaximumRate
}

func (ma *MaintainAltitude) VSummary(ac *Aircraft) string {
	return fmt.Sprintf("Maintain %.0f feet", ma.Altitude)
}

func (fr *FlyRoute) GetAltitude(ac *Aircraft) (float32, float32) {
	if len(ac.Waypoints) > 0 && ac.Waypoints[0].Altitude != 0 {
		// Ignore the crossing altitude it if the aircraft is below it and
		// it has been cleared for the approach--i.e., don't climb to meet it!
		if ac.ApproachCleared && ac.Altitude < float32(ac.Waypoints[0].Altitude) {
			return ac.Altitude, 0
		}

		eta, ok := ac.NextFixETA()
		if !ok {
			return float32(ac.Waypoints[0].Altitude), MaximumRate
		}
		rate := abs(float32(ac.Waypoints[0].Altitude)-ac.Altitude) /
			float32(eta.Minutes())

		return float32(ac.Waypoints[0].Altitude), rate
	} else {
		return float32(ac.Altitude), MaximumRate
	}
}

func (fr *FlyRoute) VSummary(ac *Aircraft) string {
	return ""
}

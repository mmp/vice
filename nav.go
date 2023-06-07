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
	case *FlyHeading, *FlyRoute, *FlyProcedureTurn:
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
	case *MaintainAltitude, *FlyRoute, *FlyProcedureTurn:
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
	case "*main.FlyProcedureTurn":
		n.L, err = unmarshalStruct[FlyProcedureTurn](m.LNavStruct)
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
	case "*main.FlyProcedureTurn":
		n.V, err = unmarshalStruct[FlyProcedureTurn](m.VNavStruct)
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

	ac.Nav.S = &MaintainSpeed{IAS: saa.IAS}
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
	ap := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport]
	if nmdistance2ll(ac.Position, ap.Location) > 5 {
		return false
	}

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

	// Estimate time to intercept.  Do this using nm coordinates
	loc := ap.Line()
	loc[0], loc[1] = ll2nm(loc[0]), ll2nm(loc[1])

	pos := ll2nm(ac.Position)
	hdg := ac.Heading - scenarioGroup.MagneticVariation
	headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
	pos1 := add2f(pos, headingVector)

	// Intersection of aircraft's path with the localizer
	isect, ok := LineLineIntersect(loc[0], loc[1], pos, pos1)
	if !ok {
		lg.Errorf("no intersect!")
		return false // better luck next time...
	}

	// Is the intersection behind the aircraft? (This can happen if it
	// has flown through the localizer.) Ignore it if so.
	v := sub2f(isect, pos)
	if v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
		lg.Errorf("%s: localizer intersection is behind us...", ac.Callsign)
		return false
	}

	if ac.ShouldTurnForOutbound(nm2ll(isect), ap.Heading(), TurnClosest) {
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

	ac.GoAround(sim)
	pilotResponse(ac.Callsign, "Going around")
	return true
}

func (g *GoAround) Summary(ac *Aircraft) string {
	return "" // We don't let the user see this will happen
}

///////////////////////////////////////////////////////////////////////////
// LNavCommand and implementations

type TurnMethod int

const (
	TurnClosest = iota // default
	TurnLeft
	TurnRight
)

func (t TurnMethod) String() string {
	return []string{"closest", "left", "right"}[t]
}

const StandardTurnRate = 3

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
)

type FlyProcedureTurn struct {
	ProcedureTurn      *ProcedureTurn
	Fix                string
	FixLocation        Point2LL
	Entry              RacetrackPTEntry
	InboundHeading     float32
	OutboundHeading    float32
	OutboundTurnRate   float32
	OutboundTurnMethod TurnMethod
	OutboundLegStart   Point2LL
	OutboundLegLength  float32
	State              int
}

func (fp *FlyProcedureTurn) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	pt := fp.ProcedureTurn

	switch fp.State {
	case PTStateApproaching:
		outboundHeading := fp.InboundHeading + 180
		if outboundHeading > 360 {
			outboundHeading -= 360
		}
		outboundTurnRate := float32(StandardTurnRate)
		outboundTurnMethod := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
		dist := nmdistance2ll(ac.Position, fp.FixLocation)
		eta := dist / ac.GS * 3600 // in seconds

		// Is it time to start the turn? If so, outboundHeading and/or
		// outboundTurnRate may be modified from the defaults above...
		startTurn := false

		switch fp.Entry {
		case DirectEntryShortTurn:
			startTurn = eta < 2
			// Since we have less than 180 degrees in our turn, turn more
			// slowly so that we more or less end up the right offset
			// distance from the inbound path.
			outboundTurnRate = 3 * headingDifference(outboundHeading, ac.Heading) / 180

		case DirectEntryLongTurn:
			// Turn start is based on lining up for the inbound heading,
			// even though the actual turn will be that plus 180.
			startTurn = ac.ShouldTurnForOutbound(fp.FixLocation, fp.InboundHeading, outboundTurnMethod)

		case ParallelEntry:
			// Swapped turn direction
			outboundTurnMethod = TurnMethod(Select(pt.RightTurns, TurnLeft, TurnRight))
			startTurn = ac.ShouldTurnForOutbound(fp.FixLocation, outboundHeading, outboundTurnMethod)

		case TeardropEntry:
			startTurn = eta < 2
			if pt.RightTurns {
				hdg := outboundHeading - 30
				if hdg < 0 {
					hdg += 360
				}
				outboundHeading = hdg
			} else {
				hdg := outboundHeading + 30
				if hdg > 360 {
					hdg -= 360
				}
				outboundHeading = hdg
			}
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
			fp.OutboundHeading = outboundHeading
			fp.OutboundTurnRate = outboundTurnRate
			fp.OutboundTurnMethod = outboundTurnMethod
			lg.Errorf("%s: starting outbound turn-heading %.1f rate %.2f method %s",
				ac.Callsign, outboundHeading, outboundTurnRate,
				outboundTurnMethod.String())
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := headingp2ll(ac.Position, fp.FixLocation, scenarioGroup.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PTStateTurningOutbound:
		if abs(ac.Heading-fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			lg.Errorf("%s: finished the turn, flying outbound leg", ac.Callsign)
			fp.State = PTStateFlyingOutbound

			fp.OutboundLegLength = float32(pt.NmLimit) / 2
			if fp.OutboundLegLength == 0 {
				fp.OutboundLegLength = float32(pt.MinuteLimit) * ac.GS / 60
			}
			if fp.Entry == TeardropEntry {
				fp.OutboundLegLength /= cos(radians(30))
			}

			switch fp.Entry {
			case DirectEntryShortTurn, DirectEntryLongTurn:
				fp.OutboundLegStart = ac.Position

			case ParallelEntry, TeardropEntry:
				fp.OutboundLegStart = fp.FixLocation
			}
		}

		return fp.OutboundHeading, fp.OutboundTurnMethod, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := nmdistance2ll(ac.Position, fp.OutboundLegStart)
		if d > fp.OutboundLegLength {
			lg.Errorf("%s: Turning inbound!", ac.Callsign)
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if abs(ac.Heading-fp.InboundHeading) < 1 {
			// go direct to the fix
			lg.Errorf("%s: direct fix--done with the HILPT!", ac.Callsign)
			ac.Nav.L = &FlyRoute{}
			ac.Nav.V = &FlyRoute{}
		}

		turn := Select(pt.RightTurns, TurnRight, TurnLeft)
		if fp.Entry == ParallelEntry {
			// This turn is in the opposite direction than usual
			turn = Select(!pt.RightTurns, TurnRight, TurnLeft)
		}
		return fp.InboundHeading, TurnMethod(turn), StandardTurnRate

	default:
		panic("unhandled state")
	}
}

func (fp *FlyProcedureTurn) shouldDescend(ac *Aircraft) bool {
	return fp.ProcedureTurn.ExitAltitude != 0 && ac.Altitude > float32(fp.ProcedureTurn.ExitAltitude) &&
		fp.State != PTStateApproaching
}

func (fp *FlyProcedureTurn) GetAltitude(ac *Aircraft) (float32, float32) {
	if fp.shouldDescend(ac) {
		return float32(fp.ProcedureTurn.ExitAltitude), MaximumRate
	} else {
		fr := &FlyRoute{}
		return fr.GetAltitude(ac)
	}
}

func (fp *FlyProcedureTurn) PassesWaypoints() bool {
	return false
}

func (fp *FlyProcedureTurn) VSummary(ac *Aircraft) string {
	if fp.ProcedureTurn.ExitAltitude != 0 && ac.Altitude > float32(fp.ProcedureTurn.ExitAltitude) {
		return fmt.Sprintf("Descend to %d in the procedure turn", fp.ProcedureTurn.ExitAltitude)
	} else {
		fr := &FlyRoute{}
		return fr.VSummary(ac)
	}
}

func (fp *FlyProcedureTurn) LSummary(ac *Aircraft) string {
	s := fmt.Sprintf("Fly the %s procedure turn at %s", fp.ProcedureTurn.Type, fp.Fix)
	if fp.ProcedureTurn.Type == PTRacetrack {
		s += ", " + fp.Entry.String() + " entry"
	}
	return s
}

func MakeFlyProcedureTurn(ac *Aircraft, wp []Waypoint) *FlyProcedureTurn {
	if len(wp) < 2 {
		lg.Errorf("ac %s", spew.Sdump(ac))
		lg.Errorf("wp %s", spew.Sdump(wp))
		panic("insufficient waypopints")
	}
	if ac.NoPT {
		lg.Errorf("%s: MakeFlyProcedureTurn called even though ac.NoPT set", ac.Callsign)
	}

	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location,
		scenarioGroup.MagneticVariation)

	pt := wp[0].ProcedureTurn

	fly := &FlyProcedureTurn{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	if pt.Type == PTRacetrack {
		aircraftFixHeading := headingp2ll(ac.Position, wp[0].Location,
			scenarioGroup.MagneticVariation)
		fly.Entry = pt.SelectRacetrackEntry(inboundHeading, aircraftFixHeading)
		lg.Printf("%s: entry %s", ac.Callsign, fly.Entry)
	}

	return fly
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

		lg.Errorf("%s: eta %f seconds IAS %f crossing speed %f -> rate (sec) %f",
			ac.Callsign, eta.Seconds(), ac.IAS, cs, rate)
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

	// Expected speed at 10 DME, without further direction.
	spd := ac.Performance.Speed
	approachSpeed := min(1.6*spd.Landing, float32(spd.Cruise))

	airportDist := nmdistance2ll(ac.Position, airportPos)
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

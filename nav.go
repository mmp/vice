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
	case *FlyHeading, *FlyRoute, *HoldLocalizer:
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
	case *MaintainAltitude, *FlyRoute:
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
	case "*main.HoldLocalizer":
		n.L, err = unmarshalStruct[HoldLocalizer](m.LNavStruct)
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
	if ac.IAS < 1.1*float32(ac.Performance.Speed.Min) {
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

	// Find eta to the intercept and the turn required to align with
	// the localizer.
	dist := distance2f(pos, isect)
	eta := dist / ac.GS * 3600 // in seconds
	turn := abs(headingDifference(hdg, float32(ap.Heading())-scenarioGroup.MagneticVariation))

	// Assuming 3 degree/second turns, then we might start to turn to
	// intercept when the eta until intercept is 1/3 the number of
	// degrees to cover.  However... the aircraft approaches the
	// localizer more slowly as it turns, so we'll add another 1/2
	// fudge factor, which seems to account for that reasonably well.
	if eta < turn/3/2 {
		lg.Printf("%s: assigned approach heading! %d", ac.Callsign, ap.Heading())

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

	ac.Nav.L = &HoldLocalizer{}
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

type HoldLocalizer struct{}

func (hl *HoldLocalizer) GetHeading(ac *Aircraft) (float32, TurnMethod, float32) {
	ap := ac.Approach
	loc := ap.Line()
	dist := SignedPointLineDistance(ll2nm(ac.Position), ll2nm(loc[0]), ll2nm(loc[1]))

	if abs(dist) < .025 {
		//lg.Errorf("%s: dist %f close enough", ac.Callsign, dist)
		// close enough
		return float32(ap.Heading()), TurnClosest, StandardTurnRate
	} else if abs(dist) > .3 {
		//lg.Errorf("%s: dist %f too far", ac.Callsign, dist)
		// If it's too far away, leave it where it is; this case can in
		// particular happen if it's been given direct to a fix that's not on
		// the localizer. (FIXME: yuck; then shouldn't be trying to hold the
		// localizer yet...)
		return ac.Heading, TurnClosest, StandardTurnRate
	} else if dist < 0 {
		//lg.Errorf("%s: dist %f turn right", ac.Callsign, dist)
		return float32(ap.Heading()) + 3, TurnClosest, StandardTurnRate
	} else {
		//lg.Errorf("%s: dist %f turn left", ac.Callsign, dist)
		return float32(ap.Heading()) - 3, TurnClosest, StandardTurnRate
	}
}

func (hl *HoldLocalizer) PassesWaypoints() bool {
	return true
}

func (hl *HoldLocalizer) LSummary(ac *Aircraft) string {
	return fmt.Sprintf("Fly along the localizer")
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
		return float32(min(ac.Performance.Speed.Cruise, 250)), MaximumRate
	} else {
		// Assume climbing or descending
		return float32(ac.Performance.Speed.Cruise) * 7 / 10, MaximumRate
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
	landingSpeed := float32(spd.Landing)
	approachSpeed := min(1.6*landingSpeed, float32(spd.Cruise))

	airportDist := nmdistance2ll(ac.Position, airportPos)
	if airportDist < 1 {
		return landingSpeed, MaximumRate
	} else if airportDist > 10 {
		// Don't accelerate if the aircraft is already under the target speed.
		return min(approachSpeed, ac.IAS), MaximumRate
	} else {
		return min(lerp((airportDist-1)/9, landingSpeed, approachSpeed), ac.IAS),
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

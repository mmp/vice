// nav.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"

	"github.com/davecgh/go-spew/spew"
)

// State related to navigation. Pointers are used for optional values; nil
// -> unset/unspecified.
type Nav struct {
	FlightState    FlightState
	Perf           AircraftPerformance
	Altitude       NavAltitude
	Speed          NavSpeed
	Heading        NavHeading
	Approach       NavApproach
	FixAssignments map[string]NavFixAssignment

	Waypoints []Waypoint

	// hilpt... -> this feels very goroutiney... maybe lnav is via control
	// flow, gotos, etc and go routines..?
	//
	// lnav: fly route, flyheading, procedure turns (Maybe those are fly
	// route actually though...)
}

type FlightState struct {
	IsDeparture               bool
	DepartureAirportLocation  Point2LL
	DepartureAirportElevation float32
	ArrivalAirportLocation    Point2LL
	ArrivalAirportElevation   float32

	MagneticVariation float32
	NmPerLongitude    float32

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...
}

func (fs *FlightState) Summary() string {
	return fmt.Sprintf("heading %03d altitude %.0f ias %.1f gs %.1f",
		int(fs.Heading), fs.Altitude, fs.IAS, fs.GS)
}

type NavAltitude struct {
	Assigned        *float32
	AfterSpeed      *float32
	AfterSpeedSpeed *float32
	Expedite        bool
	// Carried after passing a waypoint
	Restriction *AltitudeRestriction
}

type NavSpeed struct {
	Assigned              *float32
	AfterAltitude         *float32
	AfterAltitudeAltitude *float32
	// Carried after passing a waypoint
	Restriction *float32
}

type NavHeading struct {
	Assigned     *float32
	Turn         *TurnMethod
	RacetrackPT  *FlyRacetrackPT
	Standard45PT *FlyStandard45PT
}

type NavApproach struct {
	Assigned       *Approach
	AssignedId     string
	Cleared        bool
	InterceptState InterceptLocalizerState
	NoPT           bool
}

type NavFixAssignment struct {
	Arrive struct {
		Altitude *AltitudeRestriction
		Speed    *float32
	}
	Depart struct {
		Heading *float32
	}
}

type InterceptLocalizerState int

const (
	NotIntercepting = iota
	InitialHeading
	TurningToJoin
	HoldingLocalizer
)

func MakeArrivalNav(w *World, arr *Arrival, fp FlightPlan, perf AircraftPerformance) *Nav {
	if nav := makeNav(w, fp, perf, arr.Waypoints); nav != nil {
		spd := arr.SpeedRestriction
		nav.Speed.Restriction = Select(spd != 0, &spd, nil)
		alt := arr.ClearedAltitude
		nav.Altitude.Restriction = &AltitudeRestriction{Range: [2]float32{alt, alt}}

		nav.FlightState.Altitude = arr.InitialAltitude
		nav.FlightState.IAS = min(arr.InitialSpeed, nav.Perf.Speed.Cruise)

		return nav
	}
	return nil
}

func MakeDepartureNav(w *World, fp FlightPlan, perf AircraftPerformance, alt float32,
	wp []Waypoint) *Nav {
	if nav := makeNav(w, fp, perf, wp); nav != nil {
		nav.Altitude.Assigned = &alt
		nav.FlightState.IsDeparture = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func makeNav(w *World, fp FlightPlan, perf AircraftPerformance, wp []Waypoint) *Nav {
	nav := &Nav{
		Perf:           perf,
		Waypoints:      DuplicateSlice(wp),
		FixAssignments: make(map[string]NavFixAssignment),
	}

	nav.FlightState = FlightState{
		MagneticVariation: w.MagneticVariation,
		NmPerLongitude:    w.NmPerLongitude,
		Position:          nav.Waypoints[0].Location,
		Heading:           float32(nav.Waypoints[0].Heading),
	}

	if nav.FlightState.Position.IsZero() {
		lg.Errorf("uninitialized initial waypoint position! %+v", nav.Waypoints[0])
		return nil
	}

	if nav.FlightState.Heading == 0 { // unassigned, so get the heading using the next fix
		nav.FlightState.Heading = headingp2ll(nav.FlightState.Position,
			nav.Waypoints[1].Location, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)
	}

	// Filter out airways...
	nav.Waypoints = FilterSlice(nav.Waypoints[1:],
		func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if ap, ok := database.Airports[fp.DepartureAirport]; !ok {
		lg.Errorf("%s: arrival airport unknown", fp.DepartureAirport)
		return nil
	} else {
		nav.FlightState.DepartureAirportLocation = ap.Location
		nav.FlightState.DepartureAirportElevation = float32(ap.Elevation)
	}
	if ap, ok := database.Airports[fp.ArrivalAirport]; !ok {
		lg.Errorf("%s: arrival airport unknown", fp.ArrivalAirport)
		return nil
	} else {
		nav.FlightState.ArrivalAirportLocation = ap.Location
		nav.FlightState.ArrivalAirportElevation = float32(ap.Elevation)
	}

	return nav
}

func (nav *Nav) TAS() float32 {
	// Simple model for the increase in TAS as a function of altitude: 2%
	// additional TAS on top of IAS for each 1000 feet.
	return nav.FlightState.IAS * (1 + .02*nav.FlightState.Altitude/1000)
}

func (nav *Nav) IsAirborne() bool {
	v2 := nav.Perf.Speed.V2
	if v2 == 0 {
		// Unfortunately we don't always have V2 in the performance database, so approximate...
		v2 = 1.15 * nav.Perf.Speed.Landing
	}

	// FIXME: this only considers speed, which is probably ok but is somewhat unsatisfying.
	// More explicitly model "on the ground" vs "airborne" states?
	return nav.FlightState.IAS > v2
}

///////////////////////////////////////////////////////////////////////////
// Communication

// Full human-readable summary of nav state for use when paused and mouse
// hover on the scope
func (nav *Nav) Summary(fp FlightPlan) string {
	var lines []string

	fs := nav.FlightState
	if fs.IsDeparture {
		lines = append(lines, "Departure from "+fp.DepartureAirport)
	} else {
		lines = append(lines, "Arrival to "+fp.ArrivalAirport)
	}

	if nav.Altitude.Assigned != nil {
		if abs(nav.FlightState.Altitude-*nav.Altitude.Assigned) < 100 {
			lines = append(lines, "At assigned altitude "+
				FormatAltitude(*nav.Altitude.Assigned))
		} else {
			line := "At " + FormatAltitude(nav.FlightState.Altitude) + " for " +
				FormatAltitude(*nav.Altitude.Assigned)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
			lines = append(lines, line)
		}
	} else if nav.Altitude.AfterSpeed != nil {
		dir := Select(*nav.Altitude.AfterSpeed > nav.FlightState.Altitude, "climb", "descend")
		lines = append(lines, fmt.Sprintf("At %.0f kts, %s to %s",
			*nav.Altitude.AfterSpeedSpeed, dir, FormatAltitude(*nav.Altitude.AfterSpeed)))
	} else if c := nav.getWaypointAltitudeConstraint(); c != nil {
		dir := Select(c.Altitude > nav.FlightState.Altitude, "Climbing", "Descending")
		lines = append(lines, dir+" to "+FormatAltitude(c.Altitude)+" to cross "+
			c.FinalFix+" at "+FormatAltitude(c.FinalAltitude))
	} else if nav.Altitude.Restriction != nil {
		tgt := nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
		if tgt == nav.FlightState.Altitude {
			lines = append(lines, "At "+FormatAltitude(tgt)+" due to previous crossing restriction")
		} else if tgt < nav.FlightState.Altitude {
			lines = append(lines, "Descending "+FormatAltitude(nav.FlightState.Altitude)+
				" to "+FormatAltitude(tgt)+" from previous crossing restriction")
		} else {
			lines = append(lines, "Climbing "+FormatAltitude(nav.FlightState.Altitude)+
				" to "+FormatAltitude(tgt)+" from previous crossing restriction")
		}
	}

	// Heading
	if nav.Heading.Assigned != nil {
		if *nav.Heading.Assigned == nav.FlightState.Heading {
			lines = append(lines, fmt.Sprintf("On assigned %03d heading",
				int(*nav.Heading.Assigned)))
		} else {
			lines = append(lines, fmt.Sprintf("Turning from %03d to assigned %03d heading",
				int(nav.FlightState.Heading), int(*nav.Heading.Assigned)))
		}
	}

	// Speed; don't be as exhaustive as we are for altitude
	ias, _ := nav.TargetSpeed()
	if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	}

	for _, fix := range SortedMapKeys(nav.FixAssignments) {
		nfa := nav.FixAssignments[fix]
		if nfa.Arrive.Altitude != nil || nfa.Arrive.Speed != nil {
			line := "Cross " + fix + " "
			if nfa.Arrive.Altitude != nil {
				line += nfa.Arrive.Altitude.Summary() + " "
			}
			if nfa.Arrive.Speed != nil {
				line += "at " + fmt.Sprintf("%.0f kts", *nfa.Arrive.Speed)
			}
			lines = append(lines, line)
		}
		if nfa.Depart.Heading != nil {
			lines = append(lines, fmt.Sprintf("Depart "+fix+" heading %03d",
				int(*nfa.Depart.Heading)))
		}
	}

	// Approach
	if nav.Approach.Assigned != nil {
		verb := Select(nav.Approach.Cleared, "Cleared", "Assigned")
		if nav.Approach.Cleared && nav.Approach.NoPT {
			verb += " straight-in"
		}
		line := verb + " " + nav.Approach.Assigned.FullName
		if nav.Approach.InterceptState == TurningToJoin {
			line += ", turning to join the localizer"
		} else if nav.Approach.InterceptState == HoldingLocalizer {
			line += ", established on the localizer"
		}
		lines = append(lines, line)

		if pt := nav.Heading.RacetrackPT; pt != nil {
			lines = append(lines,
				fmt.Sprintf("Fly the %s procedure turn at %s, %s entry", pt.ProcedureTurn.Type,
					pt.Fix, pt.Entry.String()))
			if pt.ProcedureTurn.ExitAltitude != 0 &&
				nav.FlightState.Altitude > pt.ProcedureTurn.ExitAltitude {
				lines = append(lines, fmt.Sprintf("Descend to %d in the procedure turn",
					int(pt.ProcedureTurn.ExitAltitude)))
			}
		}
		if pt := nav.Heading.Standard45PT; pt != nil {
			lines = append(lines, fmt.Sprintf("Fly the standard 45/180 procedure turn at %s", pt.Fix))
		}
	}

	lines = append(lines, "Route: "+WaypointArray(nav.Waypoints).Encode())

	return strings.Join(lines, "\n")
}

func (nav *Nav) DepartureMessage() string {
	if nav.Altitude.Assigned == nil || nav.FlightState.Altitude == *nav.Altitude.Assigned {
		return "at " + FormatAltitude(nav.FlightState.Altitude)
	} else {
		return "at " + FormatAltitude(nav.FlightState.Altitude) + " for " + FormatAltitude(*nav.Altitude.Assigned)
	}
}

func (nav *Nav) ContactMessage(reportingPoints []ReportingPoint) string {
	// We'll just handle a few cases here; this isn't supposed to be exhaustive..
	msgs := []string{}

	var closestRP *ReportingPoint
	closestRPDistance := float32(10000)
	for i, rp := range reportingPoints {
		if d := nmdistance2ll(nav.FlightState.Position, rp.Location); d < closestRPDistance {
			closestRP = &reportingPoints[i]
			closestRPDistance = d
		}
	}
	if closestRP != nil {
		direction := compass(headingp2ll(closestRP.Location, nav.FlightState.Position,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation))
		report := fmt.Sprintf("%d miles %s of %s", int(closestRPDistance+0.5), direction,
			closestRP.ReadbackName)
		msgs = append(msgs, report)
	}

	if nav.Heading.Assigned != nil {
		msgs = append(msgs, fmt.Sprintf("assigned a %03d heading",
			int(*nav.Heading.Assigned)))
	}

	if nav.Altitude.Assigned != nil {
		msgs = append(msgs, "at "+FormatAltitude(nav.FlightState.Altitude)+" for "+
			FormatAltitude(*nav.Altitude.Assigned)+" assigned")
	} else {
		msgs = append(msgs, "at "+FormatAltitude(nav.FlightState.Altitude))
	}

	if nav.Speed.Assigned != nil {
		msgs = append(msgs, fmt.Sprintf("assigned %.0f knots", *nav.Speed.Assigned))
	}

	return strings.Join(msgs, ", ")
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (nav *Nav) updateAirspeed() {
	if nav.Altitude.Expedite {
		// Don't accelerate or decelerate if we're expediting
		return
	}

	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	targetSpeed, targetRate := nav.TargetSpeed()

	// Stay within the aircraft's capabilities
	targetSpeed = clamp(targetSpeed, nav.Perf.Speed.Min, nav.Perf.Speed.Max)

	if nav.FlightState.IAS < targetSpeed {
		accel := nav.Perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, targetRate/60)
		nav.FlightState.IAS = min(targetSpeed, nav.FlightState.IAS+accel)
	} else if nav.FlightState.IAS > targetSpeed {
		decel := nav.Perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		nav.FlightState.IAS = max(targetSpeed, nav.FlightState.IAS-decel)
	}
}

func (nav *Nav) updateAltitude() {
	targetAltitude, targetRate := nav.TargetAltitude()

	if abs(targetAltitude-nav.FlightState.Altitude) < 3 {
		nav.FlightState.Altitude = targetAltitude
		nav.Altitude.Expedite = false
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := nav.Perf.Rate.Climb, nav.Perf.Rate.Descent

	// Reduce rates from highest possible to be more realistic.
	if !nav.Altitude.Expedite {
		// For high performing aircraft, reduce climb rate after 5,000'
		if climb >= 2500 && nav.FlightState.Altitude > 5000 {
			climb -= 500
		}
		if nav.FlightState.Altitude < 10000 {
			// Have a slower baseline rate of descent on approach
			descent = min(descent, 2000)
			// And reduce it based on airspeed as well
			descent *= min(nav.FlightState.IAS/250, 1)
		}
		climb = min(climb, targetRate)
		descent = min(descent, targetRate)
	}

	if nav.FlightState.Altitude < targetAltitude {
		// Simple model: we just update altitude based on the rated climb
		// rate; does not account for simultaneous acceleration, etc...
		nav.FlightState.Altitude = min(targetAltitude, nav.FlightState.Altitude+climb/60)
	} else if nav.FlightState.Altitude > targetAltitude {
		// Similarly, descent modeling doesn't account for airspeed or
		// acceleration/deceleration...
		nav.FlightState.Altitude = max(targetAltitude, nav.FlightState.Altitude-descent/60)
	}
}

func (nav *Nav) updateHeading(wind WindModel) {
	targetHeading, turnDirection, turnRate := nav.TargetHeading(wind)

	if headingDifference(nav.FlightState.Heading, targetHeading) < 1 {
		nav.FlightState.Heading = targetHeading
		return
	}

	var turn float32
	switch turnDirection {
	case TurnLeft:
		angle := NormalizeHeading(nav.FlightState.Heading - targetHeading)
		angle = min(angle, turnRate)
		turn = -angle

	case TurnRight:
		angle := NormalizeHeading(targetHeading - nav.FlightState.Heading)
		angle = min(angle, turnRate)
		turn = angle

	case TurnClosest:
		// Figure out which way is closest: first find the angle to rotate
		// the target heading by so that it's aligned with 180
		// degrees. This lets us not worry about the complexities of the
		// wrap around at 0/360..
		rot := NormalizeHeading(180 - targetHeading)
		cur := NormalizeHeading(nav.FlightState.Heading + rot) // w.r.t. 180 target
		turn = clamp(180-cur, -turnRate, turnRate)
	}

	// Finally, do the turn.
	nav.FlightState.Heading = NormalizeHeading(nav.FlightState.Heading + turn)
}

func (nav *Nav) updatePositionAndGS(wind WindModel) {
	// Calculate offset vector based on heading and current TAS.
	hdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	TAS := nav.TAS() / 3600
	flightVector := scale2f([2]float32{sin(radians(hdg)), cos(radians(hdg))}, TAS)

	// Further offset based on the wind
	var windVector [2]float32
	if nav.IsAirborne() && wind != nil {
		windVector = wind.GetWindVector(nav.FlightState.Position, nav.FlightState.Altitude)
	}

	// Update the aircraft's state
	p := add2f(ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude), add2f(flightVector, windVector))
	nav.FlightState.Position = nm2ll(p, nav.FlightState.NmPerLongitude)
	nav.FlightState.GS = length2f(add2f(flightVector, windVector)) * 3600
}

func (nav *Nav) DepartOnCourse(alt float32) {
	nav.Altitude = NavAltitude{Assigned: &alt}
	nav.Speed = NavSpeed{}
	nav.Heading = NavHeading{}
}

func (nav *Nav) Check(callsign string) {
	check := func(waypoints []Waypoint, what string) {
		for _, wp := range waypoints {
			if wp.Location.IsZero() {
				if *devmode {
					panic(wp)
				} else {
					lg.Errorf("%s: zero waypoint location for %s in %s", callsign, wp.Fix, what)
				}
			}
		}
	}

	check(nav.Waypoints, "waypoints")
	if nav.Approach.Assigned != nil {
		for i, waypoints := range nav.Approach.Assigned.Waypoints {
			check(waypoints, fmt.Sprintf("approach %d waypoints", i))
		}
	}
}

// returns passed waypoint if any
func (nav *Nav) Update(wind WindModel) *Waypoint {
	nav.updateAirspeed()
	nav.updateAltitude()
	nav.updateHeading(wind)
	nav.updatePositionAndGS(wind)

	if nav.Heading.Assigned == nil {
		return nav.updateWaypoints(wind)
	}
	return nil
}

func (nav *Nav) TargetHeading(wind WindModel) (heading float32, turn TurnMethod, rate float32) {
	heading, turn, rate = nav.FlightState.Heading, TurnClosest, 3 // baseline

	if nav.Approach.InterceptState != NotIntercepting &&
		nav.Approach.InterceptState != HoldingLocalizer {
		return nav.LocalizerHeading(wind)
	}

	if nav.Heading.RacetrackPT != nil {
		return nav.Heading.RacetrackPT.GetHeading(nav, wind)
	}
	if nav.Heading.Standard45PT != nil {
		return nav.Heading.Standard45PT.GetHeading(nav, wind)
	}

	if nav.Heading.Assigned != nil {
		heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
		return
	} else {
		if len(nav.Waypoints) == 0 {
			return // fly present heading...
		}

		// Find the heading that will take us to the next waypoint...

		// No magnetic correction yet, just the raw geometric heading vector
		hdg := headingp2ll(nav.FlightState.Position, nav.Waypoints[0].Location,
			nav.FlightState.NmPerLongitude, 0)
		v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		v = scale2f(v, nav.FlightState.GS)

		// where we'll actually end up, given the wind
		vp := add2f(v, wind.AverageWindVector())

		// Find the deflection angle of how much the wind pushes us off course.
		vn, vpn := normalize2f(v), normalize2f(vp)
		deflection := degrees(angleBetween(vn, vpn))
		// Get a signed angle: take the cross product and then (effectively)
		// dot with (0,0,1) to figure out which way it goes
		if vn[0]*vpn[1]-vn[1]*vpn[0] > 0 {
			deflection = -deflection
		}

		// Turn into the wind; this is a bit of an approximation, since
		// turning changes how much the wind affects the aircraft, but this
		// should be minor since the aircraft's speed should be much
		// greater than the wind speed...
		hdg -= deflection

		// Incorporate magnetic variation in the final heading
		hdg += nav.FlightState.MagneticVariation

		heading = NormalizeHeading(hdg)
		return
	}
}

func (nav *Nav) LocalizerHeading(wind WindModel) (heading float32, turn TurnMethod, rate float32) {
	// Baseline
	heading, turn, rate = *nav.Heading.Assigned, TurnClosest, 3

	ap := nav.Approach.Assigned

	switch nav.Approach.InterceptState {
	case InitialHeading:
		// On a heading. Is it time to turn?  Allow a lot of slop, but just
		// fly through the localizer if it's too sharp an intercept
		hdg := ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		if headingDifference(hdg, nav.FlightState.Heading) > 45 {
			return
		}

		loc := ap.Line()

		if nav.shouldTurnToIntercept(loc[0], hdg, TurnClosest, wind) {
			lg.Printf("assigned approach heading! %.1f", hdg)

			nav.Approach.InterceptState = TurningToJoin
			nav.Heading = NavHeading{Assigned: &hdg}
			// Just in case.. Thus we will be ready to pick up the
			// approach waypoints once we capture.
			nav.Waypoints = nil
		}
		return

	case TurningToJoin:
		// we've turned to intercept. have we intercepted?
		ap := nav.Approach.Assigned
		loc := ap.Line()
		dist := PointLineDistance(ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
			ll2nm(loc[0], nav.FlightState.NmPerLongitude), ll2nm(loc[1], nav.FlightState.NmPerLongitude))
		if dist > .2 {
			return
		}

		// we'll call that good enough. Now we need to figure out which
		// fixes in the approach are still ahead and then add them to
		// the aircraft's waypoints.
		n := len(ap.Waypoints[0])
		threshold := ap.Waypoints[0][n-1].Location
		thresholdDistance := nmdistance2ll(nav.FlightState.Position, threshold)
		lg.Printf("intercepted the localizer @ %.2fnm!", thresholdDistance)

		nav.Waypoints = nil
		for i, wp := range ap.Waypoints[0] {
			// Find the first waypoint that is:
			// 1. In front of the aircraft.
			// 2. Closer to the threshold than the aircraft.
			// 3. On the localizer
			if i+1 < len(ap.Waypoints[0]) {
				wpToThresholdHeading := headingp2ll(wp.Location, ap.Waypoints[0][n-1].Location,
					nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
				lg.Printf("%s: wpToThresholdHeading %f", wp.Fix, wpToThresholdHeading)
				if headingDifference(wpToThresholdHeading,
					ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)) > 3 {
					lg.Printf("%s: fix is in front but not on the localizer", wp.Fix)
					continue
				}
			}

			acToWpHeading := headingp2ll(nav.FlightState.Position, wp.Location, nav.FlightState.NmPerLongitude,
				nav.FlightState.MagneticVariation)
			inFront := headingDifference(nav.FlightState.Heading, acToWpHeading) < 70
			lg.Printf("%s ac heading %f wp heading %f in front %v threshold distance %f",
				wp.Fix, nav.FlightState.Heading, acToWpHeading, inFront, thresholdDistance)
			if inFront && nmdistance2ll(wp.Location, threshold) < thresholdDistance {
				nav.Waypoints = DuplicateSlice(ap.Waypoints[0][i:])
				lg.Printf("added future waypoints %s...", spew.Sdump(nav.Waypoints))
				break
			}
		}

		nav.Altitude = NavAltitude{}
		nav.Heading = NavHeading{}
		nav.Approach.InterceptState = HoldingLocalizer
		return
	}

	return
}

const MaximumRate = 100000

func (nav *Nav) TargetAltitude() (alt, rate float32) {
	// Baseline...
	alt, rate = nav.FlightState.Altitude, MaximumRate // FIXME: not maximum rate

	if nav.Altitude.AfterSpeed != nil &&
		(nav.Altitude.Assigned == nil || *nav.Altitude.Assigned == nav.FlightState.Altitude) {
		if nav.FlightState.IAS == *nav.Altitude.AfterSpeedSpeed {
			nav.Altitude.Assigned = nav.Altitude.AfterSpeed
			nav.Altitude.AfterSpeed = nil
			nav.Altitude.AfterSpeedSpeed = nil
		}
	}

	if nav.FlightState.IsDeparture {
		// Accel is given in "per 2 seconds...", want to return per minute..
		maxClimb := nav.Perf.Rate.Climb

		if !nav.IsAirborne() {
			// Rolling down the runway
			return nav.FlightState.Altitude, 0
		}

		elev := nav.FlightState.DepartureAirportElevation
		if nav.FlightState.Altitude-elev < 1000 {
			// Just airborne; prioritize climb, though slightly nerf the rate
			// so aircraft are not too high too soon
			return elev + 1000, 0.6 * maxClimb
		}
	}

	// Ugly to be digging into heading here, but anyway...
	if nav.Heading.RacetrackPT != nil {
		alt, ok := nav.Heading.RacetrackPT.GetAltitude(nav)
		if ok {
			return alt, MaximumRate
		}
	}

	if nav.Altitude.Assigned != nil {
		alt = *nav.Altitude.Assigned

		if nav.FlightState.IsDeparture {
			if nav.FlightState.Altitude < 10000 {
				targetSpeed := min(250, nav.Perf.Speed.Cruise)
				if nav.FlightState.IAS < targetSpeed {
					// Prioritize accelerate over climb at 1000 AGL
					rate = 0.2 * nav.Perf.Rate.Climb
					return
				}
			}

			// Climb normally if at target speed or >10,000'.
			rate = 0.7 * nav.Perf.Rate.Climb
			return
		} else {
			rate = MaximumRate
			return
		}
	} else if c := nav.getWaypointAltitudeConstraint(); c != nil {
		if c.ETA < 5 {
			return c.Altitude, MaximumRate
		} else {
			rate = abs(c.Altitude-nav.FlightState.Altitude) / c.ETA
			return c.Altitude, rate * 60 // rate is in feet per minute
		}
	}

	if nav.Altitude.Restriction != nil {
		alt = nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
	}

	return
}

type WaypointCrossingConstraint struct {
	Altitude      float32
	ETA           float32 // seconds
	FinalFix      string
	FinalAltitude float32
}

// getWaypointAltitudeConstraint looks at the waypoint altitude
// restrictions in the aircraft's upcoming route and determines the
// altitude at which it will cross the next waypoint with a crossing
// restriction. It balances the general principle of preferring to be at
// higher altitudes (speed, efficiency) with the aircraft's performance and
// subsequent altitude restrictions--e.g., sometimes it needs to be lower
// than it would otherwise at one waypoint in order to make a restriction
// at a subsequent waypoint.
func (nav *Nav) getWaypointAltitudeConstraint() *WaypointCrossingConstraint {
	getRestriction := func(i int) *AltitudeRestriction {
		wp := nav.Waypoints[i]
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
			return nfa.Arrive.Altitude
		}
		return nav.Waypoints[i].AltitudeRestriction
	}

	// Find the last waypoint that has an altitude restriction for our
	// starting point.
	lastWp := -1
	for i := len(nav.Waypoints) - 1; i >= 0; i-- {
		if getRestriction(i) != nil {
			lastWp = i
			break
		}
	}
	if lastWp == -1 {
		// No altitude restrictions, so nothing to do here.
		return nil
	}

	// Figure out what climb/descent rate we will use for modeling the
	// flight path.
	var altRate float32
	if !nav.FlightState.IsDeparture {
		altRate = nav.Perf.Rate.Descent
		// This unfortunately mirrors logic in the Aircraft
		// updateAltitude() method.  It would be nice to unify the nav
		// modeling and the aircraft's flight modeling to eliminate this...
		if nav.FlightState.Altitude < 10000 {
			altRate = min(altRate, 2000)
			altRate *= min(nav.FlightState.IAS/250, 1)
		}
		// Reduce the expected rate by a fudge factor to try to account for
		// slowing down at lower altitudes, speed reductions on approach,
		// and the fact that aircraft cut corners at turns rather than
		// going the longer way and overflying fixes.
		altRate *= 0.7
	} else {
		// This also mirrors logic in Aircraft updateAltitude() and has its
		// own fudge factor, though a smaller one. Note that it doesn't
		// include a model for pausing the climb at 10k feet to accelerate,
		// though at that point we're likely leaving the TRACON airspace
		// anyway...
		altRate = 0.9 * Select(nav.Perf.Rate.Climb > 2500, nav.Perf.Rate.Climb-500, nav.Perf.Rate.Climb)
	}

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := getRestriction(lastWp).Range
	//lg.Printf("%s: last wp %s range %+v altRate %.1f", ac.Callsign, nav.Waypoints[lastWp].Fix, altRange, altRate)

	// Unless we can't make the constraints, we'll cross the last waypoint
	// at the upper range of the altitude restrictions.
	finalAlt := altRange[1]

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += nmdistance2ll(nav.Waypoints[i+1].Location, nav.Waypoints[i].Location)
		wp := nav.Waypoints[i]

		// Does this one have a relevant altitude restriction?
		restr := getRestriction(i)
		if restr == nil {
			continue
		}

		// Ignore it if the aircraft is cleared for the approach and is below it.
		if nav.Approach.Cleared && nav.FlightState.Altitude < restr.Range[0] {
			continue
		}

		// TODO: account for decreasing GS with altitude?
		// TODO: incorporate a simple wind model in GS?
		eta := sumDist / nav.FlightState.GS * 3600 // seconds

		// Maximum change in altitude possible before reaching this
		// waypoint.
		dalt := altRate * eta / 60

		// possibleRange is altitude range might we have at this waypoint,
		// assuming we meet the constraint at the subsequent waypoint with
		// an altitude restriction. Note that dalt only applies to one
		// limit, since the aircraft can always maintain its current
		// altitude between waypoints; which limit depends on whether it is
		// climbing or descending (but then it's confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := Select(nav.FlightState.IsDeparture,
			[2]float32{altRange[0] - dalt, altRange[1]},
			[2]float32{altRange[0], altRange[1] + dalt})

		//lg.Printf("%s: distance to %s %.1f, eta %.1nav.FlightState, possible range %v", ac.Callsign, wp.Fix, sumDist, eta, possibleRange)

		// Limit the possible range to the restriction at the current
		// waypoint.
		var ok bool
		altRange, ok = restr.ClampRange(possibleRange)
		if !ok {
			lg.Errorf("unable to fulfill altitude restriction at %s: possible %v required %v",
				wp.Fix, possibleRange, restr.Range)
			// Keep using altRange, FWIW; it will be clamped to whichever of the
			// low and high of the restriction's range it is closest to.
		}

		//lg.Printf("%s: clamped range %v", wp.Fix, altRange)

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Distance and ETA between the aircraft and the first waypoint with an
	// altitude restriction.
	d := sumDist + nmdistance2ll(nav.FlightState.Position, nav.Waypoints[0].Location)
	eta := d / nav.FlightState.GS * 3600 // seconds
	alt := altRange[1]                   // prefer to be higher rather than lower

	//lg.Printf("Final alt to make restrictions: %.1f, eta %.1f", alt, eta)

	return &WaypointCrossingConstraint{
		Altitude:      alt,
		ETA:           eta,
		FinalFix:      nav.Waypoints[lastWp].Fix,
		FinalAltitude: finalAlt,
	}
}

func (nav *Nav) TargetSpeed() (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	fd, err := nav.finalApproachDistance()
	if err == nil && fd < 5 {
		// Cancel speed restrictions inside 5 mile final
		nav.Speed = NavSpeed{}
	}

	if nav.Speed.AfterAltitude != nil &&
		(nav.Speed.Assigned == nil || *nav.Speed.Assigned == nav.FlightState.IAS) {
		if nav.FlightState.Altitude == *nav.Speed.AfterAltitudeAltitude {
			// Reached altitude, now go for speed
			nav.Speed.Assigned = nav.Speed.AfterAltitude
			nav.Speed.AfterAltitude = nil
			nav.Speed.AfterAltitudeAltitude = nil
		}
	}

	// Absent speed restrictions, slow down arrivals starting 10 miles out
	if nav.Speed.Assigned == nil && fd != 0 && fd < 10 {
		// Expected speed at 10 DME, without further direction.
		spd := nav.Perf.Speed
		approachSpeed := min(1.6*spd.Landing, float32(spd.Cruise))

		if fd < 1 {
			return spd.Landing, MaximumRate
		} else if fd > 10 {
			// Don't accelerate if the aircraft is already under the target speed.
			return min(approachSpeed, nav.FlightState.IAS), MaximumRate
		} else {
			return min(lerp((fd-1)/9, spd.Landing, approachSpeed), nav.FlightState.IAS),
				MaximumRate
		}
	}

	if nav.FlightState.IsDeparture {
		targetSpeed := min(250, nav.Perf.Speed.Cruise)

		if !nav.IsAirborne() {
			return targetSpeed, 0.8 * maxAccel
		} else if nav.FlightState.Altitude-nav.FlightState.DepartureAirportElevation < 1000 {
			// Just airborne; prioritize climb
			return targetSpeed, 0.2 * maxAccel
		}
		// Otherwise fall through to the cases below
	}

	if wp, speed, eta := nav.getUpcomingSpeedRestrictionWaypoint(); wp != nil {
		if eta < 5 { // includes unknown ETA case
			return speed, MaximumRate
		}

		if speed > nav.FlightState.IAS {
			// accelerate immediately
			return speed, MaximumRate
		} else {
			// go slow on deceleration
			rate := abs(speed-nav.FlightState.IAS) / eta
			// Ad-hoc since as we slow, ETA increases...
			rate *= 0.8

			return speed, rate * 60 // per minute
		}
	}

	if nav.Speed.Assigned != nil {
		return *nav.Speed.Assigned, MaximumRate
	}

	// Something from a previous waypoint?
	if nav.Speed.Restriction != nil {
		return *nav.Speed.Restriction, MaximumRate
	}

	// Further baseline cases for if there's no waypoint-based or
	// controller-assigned speed restriction.
	if nav.FlightState.Altitude < 10000 {
		// 250kts under 10k.  We can assume a high acceleration rate for
		// departures when this kicks in at 1000' AGL given that VNav will
		// slow the rate of climb at that point until we reach the target
		// speed.
		return min(nav.Perf.Speed.Cruise, 250), 0.8 * maxAccel
	}

	// Otherwise set the target speed by lerping from 250 to cruise based
	// on altitude.
	x := clamp((nav.FlightState.Altitude-10000)/(nav.Perf.Ceiling-10000), 0, 1)
	ias := lerp(x, min(nav.Perf.Speed.Cruise, 250), nav.Perf.Speed.Cruise)
	return ias, 0.8 * maxAccel
}

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (*Waypoint, float32, float32) {
	var eta float32
	for i, wp := range nav.Waypoints {
		if i == 0 {
			eta = float32(wp.ETA(nav.FlightState.Position, nav.FlightState.GS).Seconds())
		} else {
			d := nmdistance2ll(wp.Location, nav.Waypoints[i-1].Location)
			etaHours := d / nav.FlightState.GS
			eta += etaHours * 3600
		}

		spd := float32(wp.Speed)
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
			spd = *nfa.Arrive.Speed
		}

		if spd != 0 {
			return &wp, spd, eta
		}
	}
	return nil, 0, 0
}

// finalApproachDistance returns the total remaining flying distance
// for an aircraft that has been given an approach.
func (nav *Nav) finalApproachDistance() (float32, error) {
	if nav.Approach.Assigned == nil || !nav.Approach.Cleared {
		return 0, ErrNotClearedForApproach
	}

	if nav.Heading.Assigned != nil {
		// We're not currently on the route, so it's a little unclear. Rather than
		// guessing, we'll just error out and let callers decide how to handle this.
		return 0, ErrNotFlyingRoute
	}

	// Calculate flying distance to the airport
	if wp := nav.Waypoints; len(wp) == 0 {
		// This shouldn't happen since the aircraft should be deleted when
		// it reaches the final waypoint.  Nevertheless...
		d := nmdistance2ll(nav.FlightState.Position, nav.FlightState.ArrivalAirportLocation)
		return d, nil
	} else {
		// Distance to the next fix plus sum of the distances between
		// remaining fixes.
		d := nmdistance2ll(nav.FlightState.Position, wp[0].Location)
		for i := 0; i < len(wp)-1; i++ {
			d += nmdistance2ll(wp[i].Location, wp[i+1].Location)
		}
		return d, nil
	}
}

func (nav *Nav) updateWaypoints(wind WindModel) *Waypoint {
	if len(nav.Waypoints) == 0 {
		return nil
	}

	wp := nav.Waypoints[0]

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
	} else if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if len(nav.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = headingp2ll(wp.Location, nav.Waypoints[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = nav.FlightState.Heading
	}

	if nav.shouldTurnForOutbound(wp.Location, hdg, TurnClosest, wind) {
		lg.Printf("turning outbound from %.1f to %.1f for %s", nav.FlightState.Heading,
			hdg, wp.Fix)

		if nav.Approach.Cleared {
			// The aircraft has made it to the approach fix they
			// were cleared to, so they can start to descend.
			nav.Altitude = NavAltitude{}
		}

		if wp.AltitudeRestriction != nil &&
			(!nav.Approach.Cleared || wp.AltitudeRestriction.Range[0] < nav.FlightState.Altitude) {
			// Don't climb if we're cleared approach and below the next
			// fix's altitude.
			nav.Altitude.Restriction = wp.AltitudeRestriction
		}
		if wp.Speed != 0 {
			spd := float32(wp.Speed)
			nav.Speed.Restriction = &spd
		}

		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
			// Controller-assigned heading
			hdg := *nfa.Depart.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if wp.Heading != 0 {
			// We have an outbound heading
			hdg := float32(wp.Heading)
			nav.Heading = NavHeading{Assigned: &hdg}
		}

		if wp.NoPT {
			nav.Approach.NoPT = true
		}

		nav.Waypoints = nav.Waypoints[1:]
		if nav.Heading.Assigned == nil {
			nav.flyProcedureTurnIfNecessary()
		}

		nav.Check("(unknown)")

		//lg.Printf("%s", spew.Sdump(ac))
		return &wp
	}
	return nil
}

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial.
func (nav *Nav) shouldTurnForOutbound(p Point2LL, hdg float32, turn TurnMethod, wind WindModel) bool {
	dist := nmdistance2ll(nav.FlightState.Position, p)
	eta := dist / nav.FlightState.GS * 3600 // in seconds

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Alternatively, if we're far away w.r.t. the needed turn, don't even
	// consider it. This is both for performance but also so that we don't
	// make tiny turns miles away from fixes in some cases.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := ll2nm(p, nav.FlightState.NmPerLongitude)
	hm := hdg - nav.FlightState.MagneticVariation
	p1 := add2f(p0, [2]float32{sin(radians(hm)), cos(radians(hm))})

	// Make a ghost aircraft to use to simulate the turn.
	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	initialDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position,
		nav2.FlightState.NmPerLongitude), p0, p1)

	// Don't simulate the turn longer than it will take to do it.
	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind)
		curDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position,
			nav2.FlightState.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) {
			// Aircraft is on the other side of the line than it started on.
			lg.Printf("turning now to intercept outbound in %d seconds", i)
			//globalConfig.highlightedLocation = ac2.Position
			//globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
			return true
		}
	}
	return false
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept the radial.
func (nav *Nav) shouldTurnToIntercept(p0 Point2LL, hdg float32, turn TurnMethod, wind WindModel) bool {
	p0 = ll2nm(p0, nav.FlightState.NmPerLongitude)
	p1 := add2f(p0, [2]float32{sin(radians(hdg - nav.FlightState.MagneticVariation)),
		cos(radians(hdg - nav.FlightState.MagneticVariation))})

	initialDist := SignedPointLineDistance(ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude), p0, p1)
	eta := abs(initialDist) / nav.FlightState.GS * 3600 // in seconds
	if eta < 2 {
		// Just in case, start the turn
		return true
	}

	// As above, don't consider starting the turn if we're far away.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind)
		curDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position, nav2.FlightState.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) && abs(curDist) < .25 && headingDifference(hdg, nav2.FlightState.Heading) < 3.5 {
			lg.Printf("turning now to intercept radial in %d seconds", i)
			//globalConfig.highlightedLocation = nav2.Position
			//globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
			return true
		}
	}
	return false
}

func (nav *Nav) GoAround() string {
	hdg := nav.FlightState.Heading
	nav.Heading = NavHeading{Assigned: &hdg}

	nav.Speed = NavSpeed{}

	alt := float32(1000 * int((nav.FlightState.ArrivalAirportElevation+2500)/1000))
	nav.Altitude = NavAltitude{Assigned: &alt}

	nav.Approach = NavApproach{}

	nav.Waypoints = nil

	return Sample([]string{"going around", "on the go"})
}

func (nav *Nav) AssignAltitude(alt float32) string {
	if alt > nav.Perf.Ceiling {
		return "unable. That altitude is above our ceiling."
	}

	var response string
	if alt > nav.FlightState.Altitude {
		response = "climb and maintain " + FormatAltitude(alt)
	} else if alt == nav.FlightState.Altitude {
		response = "maintain " + FormatAltitude(alt)
	} else {
		response = "descend and maintain " + FormatAltitude(alt)
	}

	if nav.Speed.Assigned != nil && *nav.Speed.Assigned != nav.FlightState.IAS {
		nav.Altitude.AfterSpeed = &alt
		spd := *nav.Speed.Assigned
		nav.Altitude.AfterSpeedSpeed = &spd

		return fmt.Sprintf("at %.0f knots, ", *nav.Speed.Assigned) + response
	} else {
		nav.Altitude = NavAltitude{Assigned: &alt}
		return response
	}
}

func (nav *Nav) AssignSpeed(speed float32) string {
	if speed == 0 {
		nav.Speed = NavSpeed{}
		return "cancel speed restrictions"
	} else if float32(speed) < nav.Perf.Speed.Landing {
		return fmt.Sprintf("unable. Our minimum speed is %.0f knots", nav.Perf.Speed.Landing)
	} else if float32(speed) > nav.Perf.Speed.Max {
		return fmt.Sprintf("unable. Our maximum speed is %.0f knots", nav.Perf.Speed.Max)
	} else if nav.Approach.Cleared {
		// TODO: make sure we're not within 5 miles...
		nav.Speed = NavSpeed{Assigned: &speed}
		return fmt.Sprintf("maintain %.0f knots until 5 mile final", speed)
	} else if nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		nav.Speed.AfterAltitude = &speed
		alt := *nav.Altitude.Assigned
		nav.Speed.AfterAltitudeAltitude = &alt

		return fmt.Sprintf("at %.0f feet maintain %.0f knots", alt, speed)
	} else {
		nav.Speed = NavSpeed{Assigned: &speed}
		if speed < nav.FlightState.IAS {
			msg := Sample([]string{"reduce speed to %.0f knots", "down to %.0f"})
			return fmt.Sprintf(msg, speed)
		} else if speed > nav.FlightState.IAS {
			msg := Sample([]string{"increase speed to %.0f knots", "up to %.0f"})
			return fmt.Sprintf(msg, speed)
		} else {
			return fmt.Sprintf("maintain %.0f knots", speed)
		}
	}
}

func (nav *Nav) ExpediteDescent() string {
	alt, _ := nav.TargetAltitude()
	if alt >= nav.FlightState.Altitude {
		return "unable. We're not descending"
	}
	if nav.Altitude.Expedite {
		return Sample([]string{"we're already expediting", "that's our best rate"})
	}

	nav.Altitude.Expedite = true
	resp := Sample([]string{"expediting down to", "expedite to"})
	return resp + " " + FormatAltitude(alt)
}

func (nav *Nav) ExpediteClimb() string {
	alt, _ := nav.TargetAltitude()
	if alt <= nav.FlightState.Altitude {
		return "unable. We're not climbing"
	}
	if nav.Altitude.Expedite {
		return Sample([]string{"we're already expediting", "that's our best rate"})
	}

	nav.Altitude.Expedite = true
	resp := Sample([]string{"expediting up to", "expedite to"})
	return resp + " " + FormatAltitude(alt)
}

func (nav *Nav) AssignHeading(hdg float32, turn TurnMethod) string {
	// Only cancel approach clearance if the aircraft wasn't on a
	// heading and now we're giving them one.
	if nav.Heading.Assigned == nil {
		nav.Approach.Cleared = false
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false

	nav.Heading = NavHeading{Assigned: &hdg, Turn: &turn}

	switch turn {
	case TurnClosest:
		return fmt.Sprintf("fly heading %03d", int(hdg))

	case TurnRight:
		return fmt.Sprintf("turn right heading %03d", int(hdg))

	case TurnLeft:
		return fmt.Sprintf("turn left heading %03d", int(hdg))

	default:
		lg.Errorf("%03d: unhandled turn type", turn)
		return fmt.Sprintf("fly heading %03d", int(hdg))
	}
}

func (nav *Nav) FlyPresentHeading() string {
	if nav.Heading.Assigned == nil {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false
	}

	hdg := nav.FlightState.Heading
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.Approach.NoPT = false

	return "fly present heading"
}

func (nav *Nav) fixInRoute(fix string) bool {
	for i := range nav.Waypoints {
		if fix == nav.Waypoints[i].Fix {
			return true
		}
	}

	if ap := nav.Approach.Assigned; ap != nil {
		for _, route := range ap.Waypoints {
			for i := range route {
				if fix == route[i].Fix {
					return true
				}
			}
		}
	}
	return false
}

func (nav *Nav) DirectFix(fix string) string {
	// Look for the fix in the waypoints in the flight plan.
	found := false
	for i, wp := range nav.Waypoints {
		if fix == wp.Fix {
			nav.Waypoints = nav.Waypoints[i:]
			found = true
			break
		}
	}

	if !found {
		if ap := nav.Approach.Assigned; ap != nil {
			for _, route := range ap.Waypoints {
				for _, wp := range route {
					if wp.Fix == fix {
						nav.Waypoints = []Waypoint{wp}
						found = true
						break
					}
				}
			}
		}
	}

	if found {
		nav.Heading = NavHeading{}
		nav.Approach.NoPT = false
		nav.Approach.InterceptState = NotIntercepting

		// If it's a VOR, read back the actual name
		if nav, ok := database.Navaids[fix]; ok {
			return "direct " + stopShouting(nav.Name)
		} else {
			return "direct " + fix
		}
	} else {
		return "unable. " + fix + " isn't in our route"
	}
}

func (nav *Nav) DepartFixHeading(fix string, hdg float32) string {
	if hdg <= 0 || hdg > 360 {
		return fmt.Sprintf("unable. Heading %.0f is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return "unable. " + fix + " isn't in our route"
	}

	nfa := nav.FixAssignments[fix]
	h := float32(hdg)
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	response := "depart "
	if aid, ok := database.Navaids[fix]; ok {
		response += stopShouting(aid.Name)
	} else {
		response += fix
	}
	return fmt.Sprintf(response+" heading %03d", int(hdg))
}

func (nav *Nav) CrossFixAt(fix string, alt float32, speed float32) string {
	if !nav.fixInRoute(fix) {
		return "unable. " + fix + " isn't in our route"
	}

	response := "cross "
	if aid, ok := database.Navaids[fix]; ok {
		response += stopShouting(aid.Name)
	} else {
		response += fix
	}

	nfa := nav.FixAssignments[fix]
	if alt != 0 {
		ar := &AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}
		nfa.Arrive.Altitude = ar
		response += " at and maintain " + FormatAltitude(alt)
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if speed != 0 {
		s := float32(speed)
		nfa.Arrive.Speed = &s
		response += fmt.Sprintf(" at %.0f knots", speed)
		// Delete other speed restrictions
		nav.Speed = NavSpeed{}
	}
	nav.FixAssignments[fix] = nfa

	return response
}

func (nav *Nav) getApproach(airport string, id string, w *World) (*Approach, error) {
	if id == "" {
		return nil, ErrInvalidApproach
	}

	ap := w.GetAirport(airport)
	if ap == nil {
		lg.Errorf("Can't find airport %s for %s approach", airport, id)
		return nil, ErrUnknownAirport
	}

	for name, appr := range ap.Approaches {
		if name == id {
			return &appr, nil
		}
	}
	return nil, ErrUnknownApproach
}

func (nav *Nav) ExpectApproach(airport string, id string, arr *Arrival, w *World) (string, error) {
	ap, err := nav.getApproach(airport, id, w)
	if err != nil {
		return "unable. We don't know the " + id + " approach.", ErrInvalidApproach
	}

	if id == nav.Approach.AssignedId && nav.Approach.Assigned != nil {
		return "you already told us to expect the " + ap.FullName + " approach.", nil
	}

	nav.Approach.Assigned = ap
	nav.Approach.AssignedId = id

	if waypoints := arr.RunwayWaypoints[ap.Runway]; len(waypoints) > 0 {
		// Try to splice the runway-specific waypoints in with the
		// aircraft's current waypoints...
		found := false
		for i, wp := range waypoints {
			if idx := FindIf(nav.Waypoints, func(w Waypoint) bool { return w.Fix == wp.Fix }); idx != -1 {
				nav.Waypoints = nav.Waypoints[:idx]
				nav.Waypoints = append(nav.Waypoints, waypoints[i:]...)

				nav.Check("(unknown)")

				found = true
				break
			}
		}

		if !found {
			lg.Errorf("aircraft waypoints %s don't match up with arrival runway waypoints %s",
				spew.Sdump(nav.Waypoints), spew.Sdump(waypoints))
			// Assume that it has (hopefully recently) passed the last fix
			// and that patching in the rest will work out..
			nav.Waypoints = DuplicateSlice(waypoints[1:])
		}
	}

	opener := Sample([]string{"we'll expect the", "expecting the", "we'll plan for the"})
	return opener + " " + ap.FullName + " approach", nil
}

func (nav *Nav) clearedApproach(airport string, id string, straightIn bool, arr *Arrival,
	w *World) (string, error) {
	var response string
	if nav.Approach.AssignedId == "" {
		// attempt to allow it anyway...
		if resp, err := nav.ExpectApproach(airport, id, arr, w); err != nil {
			return resp, err
		}
		response = "you never told us to expect an approach, but ok, "
	}

	ap := nav.Approach.Assigned
	if id != nav.Approach.AssignedId {
		return "unable. We were told to expect the " + ap.FullName + " approach...",
			ErrClearedForUnexpectedApproach
	}

	directApproachFix := false
	if nav.Heading.Assigned == nil && len(nav.Waypoints) > 0 {
		// Try to splice the current route the approach's route
		for _, approach := range ap.Waypoints {
			for i, wp := range approach {
				if wp.Fix == nav.Waypoints[0].Fix {
					directApproachFix = true
					// Add the rest of the approach waypoints to our route
					nav.Waypoints = DuplicateSlice(approach[i:])
					break
				}
			}
		}
	}

	if ap.Type == ILSApproach {
		if directApproachFix {
			// all good
		} else if nav.Heading.Assigned != nil {
			nav.Approach.InterceptState = InitialHeading
		} else {
			return "unable. We need either direct or a heading to intercept", ErrUnableCommand
		}
		// If the aircraft is on a heading, there's nothing more to do for
		// now; keep flying the heading and after we intercept we'll add
		// the rest of the waypoints to the aircraft's waypoints array.
	} else {
		// RNAV
		if !directApproachFix {
			// FIXME: allow intercepting via a heading
			return "unable. We need direct to a fix on the approach.", ErrUnableCommand
		}
	}

	// No procedure turn if it intercepts via a heading
	nav.Approach.NoPT = straightIn || nav.Heading.Assigned != nil

	// Cleared approach also cancels speed restrictions, but let's not do
	// that.
	nav.Approach.Cleared = true

	nav.flyProcedureTurnIfNecessary()

	if straightIn {
		response += "cleared straight in " + ap.FullName + " approach"
	} else {
		response += "cleared " + ap.FullName + " approach"
	}
	return response, nil
}

func (nav *Nav) CancelApproachClearance() string {
	if !nav.Approach.Cleared {
		return "we're not currently cleared for an approach"
	}

	nav.Approach.Cleared = false

	return "cancel approach clearance."
}

///////////////////////////////////////////////////////////////////////////
// Procedure turns

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

const (
	PTStateApproaching = iota
	PTStateTurningOutbound
	PTStateFlyingOutbound
	PTStateTurningInbound
	PTStateFlyingInbound // parallel entry only
)

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

func (nav *Nav) flyProcedureTurnIfNecessary() {
	wp := nav.Waypoints
	if !nav.Approach.Cleared || len(wp) < 2 || wp[0].ProcedureTurn == nil || nav.Approach.NoPT {
		return
	}

	if wp[0].ProcedureTurn.Entry180NoPT {
		inboundHeading := headingp2ll(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)
		acFixHeading := headingp2ll(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		//lg.Errorf("ac %.1f inbound %.1f diff %.1f",
		// acFixHeading, inboundHeading, headingDifference(acFixHeading, inboundHeading))

		if headingDifference(acFixHeading, inboundHeading) < 90 {
			return
		}
	}

	switch wp[0].ProcedureTurn.Type {
	case PTRacetrack:
		nav.Heading = NavHeading{RacetrackPT: MakeFlyRacetrackPT(nav, wp)}

	case PTStandard45:
		nav.Heading = NavHeading{Standard45PT: MakeFlyStandard45PT(nav, wp)}

	default:
		lg.Errorf("Unhandled procedure turn type")
	}
}

func MakeFlyStandard45PT(nav *Nav, wp []Waypoint) *FlyStandard45PT {
	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	awayHeading := OppositeHeading(inboundHeading)
	awayHeading += float32(Select(wp[0].ProcedureTurn.RightTurns, -45, 45))

	return &FlyStandard45PT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		AwayHeading:    NormalizeHeading(awayHeading),
		State:          PTStateApproaching,
	}
}

func MakeFlyRacetrackPT(nav *Nav, wp []Waypoint) *FlyRacetrackPT {
	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)
	aircraftFixHeading := headingp2ll(nav.FlightState.Position, wp[0].Location,
		nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

	pt := wp[0].ProcedureTurn

	fp := &FlyRacetrackPT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Entry:          pt.SelectRacetrackEntry(inboundHeading, aircraftFixHeading),
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	//lg.Printf("racetrack entry %s", fp.Entry)

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
		acFixHeading := headingp2ll(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		diff := headingDifference(fp.OutboundHeading, acFixHeading)
		fp.OutboundTurnRate = 3 * diff / 180
		//lg.Printf("hdg %.0f outbound hdg %.0f diff %.0f -> rate %.1f",
		//acFixHeading, fp.OutboundHeading,
		//headingDifference(fp.OutboundHeading, acFixHeading),
		//fp.OutboundTurnRate)
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
		fp.OutboundLegLength = float32(pt.MinuteLimit) * nav.FlightState.GS / 60
	}
	if fp.OutboundLegLength == 0 {
		// Select a default based on the approach type.
		switch nav.Approach.Assigned.Type {
		case ILSApproach:
			// 1 minute by default on ILS
			fp.OutboundLegLength = nav.FlightState.GS / 60

		case RNAVApproach:
			// 4nm by default for RNAV, though that's the distance from the
			// fix, so turn earlier...
			fp.OutboundLegLength = 2

		default:
			lg.Errorf("unhandled approach type: %s", nav.Approach.Assigned.Type)
			fp.OutboundLegLength = nav.FlightState.GS / 60

		}
	}
	// Lengthen it a bit for teardrop since we're flying along the
	// diagonal.
	if fp.Entry == TeardropEntry {
		fp.OutboundLegLength *= 1.5
	}

	return fp
}

func (fp *FlyRacetrackPT) GetHeading(nav *Nav, wind WindModel) (float32, TurnMethod, float32) {
	pt := fp.ProcedureTurn

	switch fp.State {
	case PTStateApproaching:
		dist := nmdistance2ll(nav.FlightState.Position, fp.FixLocation)
		eta := dist / nav.FlightState.GS * 3600 // in seconds
		startTurn := false

		switch fp.Entry {
		case DirectEntryShortTurn:
			startTurn = eta < 2

		case DirectEntryLongTurn:
			// Turn start is based on lining up for the inbound heading,
			// even though the actual turn will be that plus 180.
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.InboundHeading,
				fp.OutboundTurnMethod, wind)

		case ParallelEntry, TeardropEntry:
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.OutboundHeading,
				fp.OutboundTurnMethod, wind)
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
			//lg.Printf("starting outbound turn-heading %.1f rate %.2f method %s",
			//fp.OutboundHeading, fp.OutboundTurnRate,
			//fp.OutboundTurnMethod.String())
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := headingp2ll(nav.FlightState.Position, fp.FixLocation, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PTStateTurningOutbound:
		if headingDifference(nav.FlightState.Heading, fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			//lg.Printf("finished the turn; ac heading %.1f outbound %.1f; flying outbound leg",
			//nav.FlightState.Heading, fp.OutboundHeading)
			fp.State = PTStateFlyingOutbound
		}

		return fp.OutboundHeading, fp.OutboundTurnMethod, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := nmdistance2ll(nav.FlightState.Position, fp.FixLocation)

		if fp.Entry == TeardropEntry {
			// start the turn when we will intercept the inbound radial
			turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
			if d > 0.5 && nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind) {
				//lg.Printf("teardrop Turning inbound!")
				fp.State = PTStateTurningInbound
			}
		} else if d > fp.OutboundLegLength {
			//lg.Printf("Turning inbound!")
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if fp.Entry == ParallelEntry {
			// Parallel is special: we fly at the 30 degree
			// offset-from-true-inbound heading until it is time to turn to
			// intercept.
			hdg := NormalizeHeading(fp.InboundHeading + float32(Select(pt.RightTurns, -30, 30)))
			//lg.Printf("parallel inbound turning to %.1f", hdg)
			if headingDifference(nav.FlightState.Heading, hdg) < 1 {
				fp.State = PTStateFlyingInbound
			}
			// This turn is in the opposite direction than usual
			turn := Select(!pt.RightTurns, TurnRight, TurnLeft)
			return hdg, TurnMethod(turn), StandardTurnRate
		} else {
			if headingDifference(nav.FlightState.Heading, fp.InboundHeading) < 1 {
				// otherwise go direct to the fix
				//lg.Printf("direct fix--done with the HILPT!")
				nav.Heading = NavHeading{}
				nav.Altitude = NavAltitude{}
			}

			turn := Select(pt.RightTurns, TurnRight, TurnLeft)
			return fp.InboundHeading, TurnMethod(turn), StandardTurnRate
		}

	case PTStateFlyingInbound:
		// This state is only used for ParallelEntry
		turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind) {
			//lg.Printf("parallel inbound direct fix")
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
		}
		hdg := NormalizeHeading(fp.InboundHeading + float32(Select(pt.RightTurns, -30, 30)))
		return hdg, TurnClosest, StandardTurnRate

	default:
		panic("unhandled state")
	}
}

func (fp *FlyRacetrackPT) GetAltitude(nav *Nav) (float32, bool) {
	descend := fp.ProcedureTurn.ExitAltitude != 0 &&
		nav.FlightState.Altitude > fp.ProcedureTurn.ExitAltitude &&
		fp.State != PTStateApproaching
	return fp.ProcedureTurn.ExitAltitude, descend
}

func (fp *FlyStandard45PT) GetHeading(nav *Nav, wind WindModel) (float32, TurnMethod, float32) {
	outboundHeading := OppositeHeading(fp.InboundHeading)

	switch fp.State {
	case PT45StateApproaching:
		if nav.shouldTurnForOutbound(fp.FixLocation, outboundHeading, TurnClosest, wind) {
			//lg.Printf("turning outbound to %.0f", outboundHeading)
			fp.State = PT45StateTurningOutbound
		}

		// Fly toward the fix until it's time to turn outbound
		fixHeading := headingp2ll(nav.FlightState.Position, fp.FixLocation,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningOutbound:
		if nav.FlightState.Heading == outboundHeading {
			fp.State = PTStateFlyingOutbound
			fp.SecondsRemaining = 60
			//lg.Printf("flying outbound for %ds", fp.SecondsRemaining)
		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingOutbound:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningAway
			//lg.Printf("turning away from outbound to %.0f", fp.AwayHeading)

		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningAway:
		if nav.FlightState.Heading == fp.AwayHeading {
			fp.State = PT45StateFlyingAway
			fp.SecondsRemaining = 60
			//lg.Printf("flying away for %ds", fp.SecondsRemaining)
		}

		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingAway:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningIn
			//lg.Printf("turning in to %.0f", OppositeHeading(fp.AwayHeading))
		}
		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningIn:
		hdg := OppositeHeading(fp.AwayHeading)
		if nav.FlightState.Heading == hdg {
			fp.State = PT45StateFlyingIn
			//lg.Printf("flying in")
		}

		turn := Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft)
		return hdg, TurnMethod(turn), StandardTurnRate

	case PT45StateFlyingIn:
		turn := TurnMethod(Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind) {
			fp.State = PT45StateTurningToIntercept
			//lg.Printf("starting turn to intercept %.0f", fp.InboundHeading)
		}
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate

	case PT45StateTurningToIntercept:
		if nav.FlightState.Heading == fp.InboundHeading {
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
			//lg.Printf("done! direct to the fix now")
		}

		return fp.InboundHeading, TurnClosest, StandardTurnRate

	default:
		lg.Errorf("unhandled PT state: %d", fp.State)
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate
	}
}

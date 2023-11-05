// nav.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/exp/slog"
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

	// DeferredHeading stores a heading assignment from the controller that
	// the pilot has not yet started to follow.  Note that only a single
	// such assignment is stored; if the controller issues a first heading
	// and then a second shortly afterward, before the first has been
	// followed, it's fine for the second to override it.
	DeferredHeading *DeferredHeading

	FinalAltitude float32
	Waypoints     []Waypoint
}

// DeferredHeading stores a heading assignment from the controller and the
// time at which to start executing it; this time is set to be a few
// seconds after the controller issues it in order to model the delay
// before pilots start to follow assignments.
type DeferredHeading struct {
	// Time is just plain old wallclock time; it should be sim time, but a
	// lot of replumbing would be required to have that available where
	// needed. The downsides are minor: 1. On quit and resume, any pending
	// assignments will generally be followed immediately, and 2. if the
	// sim rate is increased, the delay will end up being longer than
	// intended.
	Time    time.Time
	Heading NavHeading
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

func (fs FlightState) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("is_departure", fs.IsDeparture),
		slog.Any("position", fs.Position),
		slog.Float64("heading", float64(fs.Heading)),
		slog.Float64("altitude", float64(fs.Altitude)),
		slog.Float64("ias", float64(fs.IAS)),
		slog.Float64("gs", float64(fs.GS)),
	)
}

type NavAltitude struct {
	Assigned        *float32 // controller assigned
	Cleared         *float32 // from initial clearance
	AfterSpeed      *float32
	AfterSpeedSpeed *float32
	Expedite        bool
	// Carried after passing a waypoint
	Restriction *AltitudeRestriction
}

type NavSpeed struct {
	Assigned                 *float32
	AfterAltitude            *float32
	AfterAltitudeAltitude    *float32
	MaintainSlowestPractical bool
	MaintainMaximumForward   bool
	// Carried after passing a waypoint
	Restriction *float32
}

type NavHeading struct {
	Assigned     *float32
	Turn         *TurnMethod
	Arc          *DMEArc
	JoiningArc   bool
	RacetrackPT  *FlyRacetrackPT
	Standard45PT *FlyStandard45PT
}

type NavApproach struct {
	Assigned          *Approach
	AssignedId        string
	Cleared           bool
	InterceptState    InterceptLocalizerState
	NoPT              bool
	AtFixClearedRoute []Waypoint
}

type NavFixAssignment struct {
	Arrive struct {
		Altitude *AltitudeRestriction
		Speed    *float32
	}
	Depart struct {
		Fix     *Waypoint
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
		// This won't be quite right but it's better than leaving GS to be
		// 0 for the first nav update tick which leads to various Inf and
		// NaN cases...
		nav.FlightState.GS = nav.FlightState.IAS

		return nav
	}
	return nil
}

func MakeDepartureNav(w *World, fp FlightPlan, perf AircraftPerformance, alt float32,
	wp []Waypoint) *Nav {
	if nav := makeNav(w, fp, perf, wp); nav != nil {
		nav.Altitude.Cleared = &alt
		nav.FlightState.IsDeparture = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func makeNav(w *World, fp FlightPlan, perf AircraftPerformance, wp []Waypoint) *Nav {
	nav := &Nav{
		Perf:           perf,
		FinalAltitude:  float32(fp.Altitude),
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
	nav.Waypoints = FilterSlice(nav.Waypoints,
		func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if ap, ok := database.Airports[fp.DepartureAirport]; !ok {
		lg.Errorf("%s: departure airport unknown", fp.DepartureAirport)
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

func (nav *Nav) v2() float32 {
	if nav.Perf.Speed.V2 == 0 {
		// Unfortunately we don't always have V2 in the performance database, so approximate...
		return 1.15 * nav.Perf.Speed.Landing
	}
	return nav.Perf.Speed.V2
}

func (nav *Nav) IsAirborne() bool {
	v2 := nav.v2()

	// FIXME: this only considers speed, which is probably ok but is somewhat unsatisfying.
	// More explicitly model "on the ground" vs "airborne" states?
	return nav.FlightState.IAS > v2
}

// AssignedHeading returns the aircraft's current heading assignment, if
// any, regardless of whether the pilot has yet started following it.
func (nav *Nav) AssignedHeading() (float32, bool) {
	if dh := nav.DeferredHeading; dh != nil {
		if dh.Heading.Assigned != nil {
			return *dh.Heading.Assigned, true
		}
	} else if nav.Heading.Assigned != nil {
		return *nav.Heading.Assigned, true
	}
	return 0, false
}

// EnqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment.
func (nav *Nav) EnqueueHeading(h NavHeading) {
	delay := 3 + 3*rand.Float32()
	now := time.Now()
	nav.DeferredHeading = &DeferredHeading{
		Time:    now.Add(time.Duration(delay * float32(time.Second))),
		Heading: h,
	}
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
	} else if c := nav.getWaypointAltitudeConstraint(); c != nil && !nav.flyingPT() {
		dir := Select(c.Altitude > nav.FlightState.Altitude, "Climbing", "Descending")
		lines = append(lines, dir+" to "+FormatAltitude(c.Altitude)+" to cross "+
			c.FinalFix+" at "+FormatAltitude(c.FinalAltitude))
	} else if nav.Altitude.Cleared != nil {
		if abs(nav.FlightState.Altitude-*nav.Altitude.Cleared) < 100 {
			lines = append(lines, "At cleared altitude "+
				FormatAltitude(*nav.Altitude.Cleared))
		} else {
			line := "At " + FormatAltitude(nav.FlightState.Altitude) + " for " +
				FormatAltitude(*nav.Altitude.Cleared)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
			lines = append(lines, line)
		}
	} else if nav.Altitude.Restriction != nil {
		tgt := nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
		if nav.FinalAltitude != 0 { // allow 0 for backwards compatability with saved
			tgt = min(tgt, nav.FinalAltitude)
		}
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
	if dh := nav.DeferredHeading; dh != nil {
		if dh.Heading.Assigned == nil && len(nav.Waypoints) > 0 {
			lines = append(lines, fmt.Sprintf("Will shortly go direct %s", nav.Waypoints[0].Fix))
		} else if dh.Heading.Assigned != nil {
			lines = append(lines, fmt.Sprintf("Will shortly start flying heading %03d", int(*dh.Heading.Assigned)))
		}
	}

	// Speed; don't be as exhaustive as we are for altitude
	ias, _ := nav.TargetSpeed(lg)
	if nav.Speed.MaintainSlowestPractical {
		lines = append(lines, fmt.Sprintf("Maintain slowest practical speed: %.0f kts", ias))
	} else if nav.Speed.MaintainMaximumForward {
		lines = append(lines, fmt.Sprintf("Maintain maximum forward speed: %.0f kts", ias))
	} else if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	} else if nav.Speed.Assigned != nil {
		lines = append(lines, fmt.Sprintf("Maintaining %.0f kts assignment", *nav.Speed.Assigned))
	} else if nav.Speed.AfterAltitude != nil && nav.Speed.AfterAltitudeAltitude != nil {
		lines = append(lines, fmt.Sprintf("At %s, maintain %0.f kts", FormatAltitude(*nav.Speed.AfterAltitudeAltitude),
			*nav.Speed.AfterAltitude))
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
		if nfa.Depart.Heading != nil && nav.Heading.Assigned == nil {
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
		switch nav.Approach.InterceptState {
		case NotIntercepting:
			// nada
		case InitialHeading:
			line += ", will join the localizer"
		case TurningToJoin:
			line += ", turning to join the localizer"
		case HoldingLocalizer:
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
	alt := func(a float32) string {
		return FormatAltitude(float32(100 * int((a+50)/100)))
	}
	if nav.Altitude.Assigned == nil || nav.FlightState.Altitude == *nav.Altitude.Assigned {
		return "at " + alt(nav.FlightState.Altitude)
	} else {
		return "at " + alt(nav.FlightState.Altitude) + " for " + alt(*nav.Altitude.Assigned)
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
		if dist := int(closestRPDistance + 0.5); dist <= 1 {
			msgs = append(msgs, "passing "+FixReadback(closestRP.Fix))
		} else {
			msgs = append(msgs, fmt.Sprintf("%d miles %s of %s", dist, direction,
				FixReadback(closestRP.Fix)))
		}
	}

	if hdg, ok := nav.AssignedHeading(); ok {
		msgs = append(msgs, fmt.Sprintf("on a %03d heading", int(hdg)))
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

func (nav *Nav) updateAirspeed(lg *Logger) {
	if nav.Altitude.Expedite {
		// Don't accelerate or decelerate if we're expediting
		lg.Debug("expediting altitude, so speed unchanged")
		return
	}

	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	targetSpeed, targetRate := nav.TargetSpeed(lg)

	// Stay within the aircraft's capabilities
	targetSpeed = clamp(targetSpeed, nav.Perf.Speed.Min, nav.Perf.Speed.Max)

	if nav.FlightState.IAS < targetSpeed {
		accel := nav.Perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, targetRate/60)
		if nav.Altitude.Assigned != nil && nav.FlightState.Altitude < *nav.Altitude.Assigned {
			// Reduce acceleration since also climbing
			accel *= 0.6
		}
		nav.FlightState.IAS = min(targetSpeed, nav.FlightState.IAS+accel)
	} else if nav.FlightState.IAS > targetSpeed {
		decel := nav.Perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		if nav.Altitude.Assigned != nil && nav.FlightState.Altitude > *nav.Altitude.Assigned {
			// Reduce deceleration since also descending
			decel *= 0.6
		}
		nav.FlightState.IAS = max(targetSpeed, nav.FlightState.IAS-decel)
	}
}

func (nav *Nav) updateAltitude(lg *Logger) {
	targetAltitude, targetRate := nav.TargetAltitude(lg)

	if nav.FinalAltitude != 0 { // allow 0 for backwards compatability with saved
		targetAltitude = min(targetAltitude, nav.FinalAltitude)
	}

	if targetAltitude == nav.FlightState.Altitude {
		return
	}

	if abs(targetAltitude-nav.FlightState.Altitude) < 3 {
		nav.FlightState.Altitude = targetAltitude
		nav.Altitude.Expedite = false
		lg.Debug("reached target altitude")
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
		if nav.Speed.Assigned != nil && nav.FlightState.IAS < *nav.Speed.Assigned {
			// Reduce rate due to concurrent acceleration
			climb *= 0.7
		}
		nav.FlightState.Altitude = min(targetAltitude, nav.FlightState.Altitude+climb/60)
	} else if nav.FlightState.Altitude > targetAltitude {
		if nav.Speed.Assigned != nil && nav.FlightState.IAS > *nav.Speed.Assigned {
			// Reduce rate due to concurrent deceleration
			descent *= 0.7
		}
		nav.FlightState.Altitude = max(targetAltitude, nav.FlightState.Altitude-descent/60)
	}
}

func (nav *Nav) updateHeading(wind WindModel, lg *Logger) {
	targetHeading, turnDirection, turnRate := nav.TargetHeading(wind, lg)

	if nav.FlightState.Heading == targetHeading {
		return
	}
	if headingDifference(nav.FlightState.Heading, targetHeading) < 1 {
		nav.FlightState.Heading = targetHeading
		return
	}
	lg.Debugf("turning for heading %.0f", targetHeading)

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

func (nav *Nav) updatePositionAndGS(wind WindModel, lg *Logger) {
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
	p := add2f(ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		add2f(flightVector, windVector))
	nav.FlightState.Position = nm2ll(p, nav.FlightState.NmPerLongitude)
	nav.FlightState.GS = length2f(add2f(flightVector, windVector)) * 3600
}

func (nav *Nav) DepartOnCourse(alt float32, exit string) {
	// Make sure we are going direct to the exit.
	if idx := FindIf(nav.Waypoints, func(wp Waypoint) bool { return wp.Fix == exit }); idx != -1 {
		nav.Waypoints = nav.Waypoints[idx:]
	}
	nav.Altitude = NavAltitude{Assigned: &alt}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
}

func (nav *Nav) Check(lg *Logger) {
	check := func(waypoints []Waypoint, what string) {
		for _, wp := range waypoints {
			if wp.Location.IsZero() {
				lg.Errorf("zero waypoint location for %s in %s", wp.Fix, what)
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
func (nav *Nav) Update(wind WindModel, lg *Logger) *Waypoint {
	nav.updateAirspeed(lg)
	nav.updateAltitude(lg)
	nav.updateHeading(wind, lg)
	nav.updatePositionAndGS(wind, lg)

	lg.Debug("nav_update", slog.Any("flight_state", nav.FlightState))

	// Don't refer to DeferredHeading here; assume that if the pilot hasn't
	// punched in a new heading assignment, we should update waypoints or
	// not as per the old assignment.
	if nav.Heading.Assigned == nil {
		return nav.updateWaypoints(wind, lg)
	}

	return nil
}

func (nav *Nav) TargetHeading(wind WindModel, lg *Logger) (heading float32, turn TurnMethod, rate float32) {
	// Is it time to start following a heading given by the controller a
	// few seconds ago?
	if dh := nav.DeferredHeading; dh != nil && time.Now().After(dh.Time) {
		lg.Debug("initiating deferred heading assignment", slog.Any("heading", dh.Heading))
		nav.Heading = dh.Heading
		nav.DeferredHeading = nil
	}

	heading, turn, rate = nav.FlightState.Heading, TurnClosest, 3 // baseline

	// nav.Heading.Assigned may still be nil pending a deferred turn
	if (nav.Approach.InterceptState == InitialHeading ||
		nav.Approach.InterceptState == TurningToJoin) && nav.Heading.Assigned != nil {
		return nav.LocalizerHeading(wind, lg)
	}

	if nav.Heading.RacetrackPT != nil {
		return nav.Heading.RacetrackPT.GetHeading(nav, wind, lg)
	}
	if nav.Heading.Standard45PT != nil {
		return nav.Heading.Standard45PT.GetHeading(nav, wind, lg)
	}

	if nav.Heading.Assigned != nil {
		heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
		lg.Debugf("heading: assigned %.0f", heading)
		return
	} else {
		// Either on an arc or to a waypoint. Figure out the point we're
		// heading to and then common code will handle wind correction,
		// etc...
		var pTarget Point2LL

		if arc := nav.Heading.Arc; arc != nil {
			if nav.Heading.JoiningArc {
				heading = nav.Heading.Arc.InitialHeading
				if headingDifference(nav.FlightState.Heading, heading) < 1 {
					nav.Heading.JoiningArc = false
				}
				return
			}

			// Work in nm coordinates
			pc := ll2nm(arc.Center, nav.FlightState.NmPerLongitude)
			pac := ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
			v := sub2f(pac, pc)
			// Heading from center to aircraft, which we assume to be more
			// or less on the arc already.
			angle := degrees(atan2(v[0], v[1])) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(Select(arc.Clockwise, 10, -10))
			p := add2f(pc, scale2f([2]float32{sin(radians(angle)), cos(radians(angle))}, arc.Radius))
			pTarget = nm2ll(p, nav.FlightState.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				lg.Debug("heading: route empty, no heading assigned", heading)
				return // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
		}

		// No magnetic correction yet, just the raw geometric heading vector
		hdg := headingp2ll(nav.FlightState.Position, pTarget, nav.FlightState.NmPerLongitude, 0)
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
		if nav.Heading.Arc != nil {
			lg.Debugf("heading: flying %.0f for %.1f %s arc", heading, nav.Heading.Arc.Radius,
				nav.Heading.Arc.Fix)
		} else {
			lg.Debugf("heading: flying %.0f to %s", heading, nav.Waypoints[0].Fix)
		}
		return
	}
}

func (nav *Nav) LocalizerHeading(wind WindModel, lg *Logger) (heading float32, turn TurnMethod, rate float32) {
	// Baseline
	heading, turn, rate = *nav.Heading.Assigned, TurnClosest, 3

	ap := nav.Approach.Assigned

	switch nav.Approach.InterceptState {
	case InitialHeading:
		// On a heading. Is it time to turn?  Allow a lot of slop, but just
		// fly through the localizer if it's too sharp an intercept
		hdg := ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		if d := headingDifference(hdg, nav.FlightState.Heading); d > 45 {
			lg.Info("heading: difference %.0f too much to intercept the localizer", d)
			return
		}

		loc := ap.Line()

		if nav.shouldTurnToIntercept(loc[0], hdg, TurnClosest, wind, lg) {
			lg.Debugf("heading: time to turn for approach heading %.1f", hdg)

			nav.Approach.InterceptState = TurningToJoin
			// The autopilot is doing this, so start the turn immediately;
			// don't use EnqueueHeading. However, leave any deferred
			// heading in place, as it represents a controller command that
			// should be followed.
			nav.Heading = NavHeading{Assigned: &hdg}
			// Just in case.. Thus we will be ready to pick up the
			// approach waypoints once we capture.
			nav.Waypoints = nil
		}
		return

	case TurningToJoin:
		// we've turned to intercept. have we intercepted?
		loc := ap.Line()
		dist := PointLineDistance(ll2nm(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
			ll2nm(loc[0], nav.FlightState.NmPerLongitude), ll2nm(loc[1], nav.FlightState.NmPerLongitude))
		if dist > .2 {
			return
		}
		lg.Debug("heading: localizer intercepted")

		// we'll call that good enough. Now we need to figure out which
		// fixes in the approach are still ahead and then add them to
		// the aircraft's waypoints.
		n := len(ap.Waypoints[0])
		threshold := ap.Waypoints[0][n-1].Location
		thresholdDistance := nmdistance2ll(nav.FlightState.Position, threshold)
		lg.Debugf("heading: intercepted the localizer @ %.2fnm!", thresholdDistance)

		nav.Waypoints = nil
		for i, wp := range ap.Waypoints[0] {
			// Find the first waypoint that is:
			// 1. In front of the aircraft.
			// 2. Closer to the threshold than the aircraft.
			// 3. On the localizer
			if i+1 < len(ap.Waypoints[0]) {
				wpToThresholdHeading := headingp2ll(wp.Location, ap.Waypoints[0][n-1].Location,
					nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
				lg.Debugf("heading: fix %s wpToThresholdHeading %f", wp.Fix, wpToThresholdHeading)
				if headingDifference(wpToThresholdHeading,
					ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)) > 3 {
					lg.Debugf("heading: fix %s is in front but not on the localizer", wp.Fix)
					continue
				}
			}

			acToWpHeading := headingp2ll(nav.FlightState.Position, wp.Location, nav.FlightState.NmPerLongitude,
				nav.FlightState.MagneticVariation)
			inFront := headingDifference(nav.FlightState.Heading, acToWpHeading) < 70
			lg.Debugf("heading: fix %s ac heading %f wp heading %f in front %v threshold distance %f",
				wp.Fix, nav.FlightState.Heading, acToWpHeading, inFront, thresholdDistance)
			if inFront && nmdistance2ll(wp.Location, threshold) < thresholdDistance {
				nav.Waypoints = DuplicateSlice(ap.Waypoints[0][i:])
				lg.Debug("heading: fix added future waypoints", slog.Any("waypoints", nav.Waypoints))
				break
			}
		}

		// Ignore the approach altitude constraints if the aircraft is only
		// intercepting but isn't cleared.
		if nav.Approach.Cleared {
			nav.Altitude = NavAltitude{}
		}
		// As with the heading assignment above under the InitialHeading
		// case, do this immediately.
		nav.Heading = NavHeading{}
		nav.Approach.InterceptState = HoldingLocalizer
		return
	}

	return
}

const MaximumRate = 100000
const initialClimbAltitude = 1500

func (nav *Nav) TargetAltitude(lg *Logger) (alt, rate float32) {
	// Baseline...
	alt, rate = nav.FlightState.Altitude, MaximumRate // FIXME: not maximum rate

	if nav.Altitude.AfterSpeed != nil &&
		(nav.Altitude.Assigned == nil || *nav.Altitude.Assigned == nav.FlightState.Altitude) {
		if nav.FlightState.IAS == *nav.Altitude.AfterSpeedSpeed {
			nav.Altitude.Assigned = nav.Altitude.AfterSpeed
			nav.Altitude.AfterSpeed = nil
			nav.Altitude.AfterSpeedSpeed = nil
			lg.Debugf("alt: reached target speed %.0f; now going for altitude %.0f",
				nav.FlightState.IAS, *nav.Altitude.Assigned)
		}
	}

	if nav.FlightState.IsDeparture {
		// Accel is given in "per 2 seconds...", want to return per minute..
		maxClimb := nav.Perf.Rate.Climb

		if !nav.IsAirborne() {
			// Rolling down the runway
			lg.Debug("alt: continuing takeoff roll")
			return nav.FlightState.Altitude, 0
		}

		elev := nav.FlightState.DepartureAirportElevation
		if nav.FlightState.Altitude-elev < initialClimbAltitude {
			// Just airborne; prioritize climb, though slightly nerf the rate
			// so aircraft are not too high too soon
			alt := elev + initialClimbAltitude
			lg.Debugf("alt: initial climb to %.0f", alt)
			return alt, 0.6 * maxClimb
		}
	}

	// Ugly to be digging into heading here, but anyway...
	if nav.Heading.RacetrackPT != nil {
		if alt, ok := nav.Heading.RacetrackPT.GetAltitude(nav); ok {
			lg.Debugf("alt: descending to %d for procedure turn", int(alt))
			return alt, MaximumRate
		}
	}

	getAssignedRate := func() float32 {
		if nav.FlightState.IsDeparture {
			if nav.FlightState.Altitude < 10000 {
				targetSpeed := min(250, nav.Perf.Speed.Cruise)
				if nav.FlightState.IAS < 0.9*targetSpeed {
					// Prioritize accelerate over climb starting at 1500 AGL
					return 0.2 * nav.Perf.Rate.Climb
				}
			}

			// Climb normally if at target speed or >10,000'.
			return 0.7 * nav.Perf.Rate.Climb
		} else {
			return MaximumRate
		}
	}

	if nav.Altitude.Assigned != nil {
		alt, rate = *nav.Altitude.Assigned, getAssignedRate()
		lg.Debugf("alt: assigned %.0f, rate %.0f", alt, rate)
		return
	} else if c := nav.getWaypointAltitudeConstraint(); c != nil && !nav.flyingPT() {
		lg.Debugf("alt: altitude %.0f for final waypoint %s in %.0f seconds", c.Altitude, c.FinalFix, c.ETA)
		if c.ETA < 5 {
			return c.Altitude, MaximumRate
		} else {
			rate = abs(c.Altitude-nav.FlightState.Altitude) / c.ETA
			return c.Altitude, rate * 60 // rate is in feet per minute
		}
	} else if nav.Altitude.Cleared != nil {
		alt, rate = *nav.Altitude.Cleared, getAssignedRate()
		lg.Debugf("alt: cleared %.0f, rate %.0f", alt, rate)
		return
	}

	if ar := nav.Altitude.Restriction; ar != nil {
		lg.Debugf("alt: previous restriction %.0f-%.0f", ar.Range[0], ar.Range[1])
		alt = nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
	}

	return
}

func (nav *Nav) flyingPT() bool {
	return (nav.Heading.RacetrackPT != nil && nav.Heading.RacetrackPT.State != PTStateApproaching) ||
		(nav.Heading.Standard45PT != nil && nav.Heading.Standard45PT.State != PT45StateApproaching)
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
	if nav.Heading.Assigned != nil {
		// ignore what's going on with the fixes
		return nil
	}

	getRestriction := func(i int) *AltitudeRestriction {
		wp := nav.Waypoints[i]
		// Return any controller-assigned constraint in preference to a
		// charted one.
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
			return nfa.Arrive.Altitude
		}
		return nav.Waypoints[i].AltitudeRestriction
	}

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft.
	lastWp := -1
	for i := len(nav.Waypoints) - 1; i >= 0; i-- {
		// Skip restrictions that don't apply (e.g. "at or above" if we're
		// already above.) I think(?) we would actually bail out and return
		// nil if we find one that doesn't apply, under the principle that
		// we should also already be meeting any restrictions that are
		// before it, but this seems less risky.
		if r := getRestriction(i); r != nil &&
			r.TargetAltitude(nav.FlightState.Altitude) != nav.FlightState.Altitude {
			lastWp = i
			break
		}
	}
	if lastWp == -1 {
		// No applicable altitude restrictions found, so nothing to do here.
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

	// Unless we can't make the constraints, we'll cross the last waypoint
	// at the upper range of the altitude restrictions.
	finalAlt := Select(altRange[1] != 0, altRange[1], 60000)

	// The cruising altitude in the flight plan takes precedence if it's lower
	// then the fix's altitude restriction.
	if nav.FinalAltitude != 0 { // allow 0 for backwards compatability with saved
		finalAlt = min(finalAlt, nav.FinalAltitude)
	}

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
		// TODO: I think this can be 'break' rather than continue...
		if nav.Approach.Cleared && nav.FlightState.Altitude < restr.Range[0] {
			continue
		}

		// TODO: account for decreasing GS with altitude?
		// TODO: incorporate a simple wind model in GS?
		eta := sumDist / nav.FlightState.GS * 3600 // seconds

		// Maximum change in altitude possible before reaching this
		// waypoint.
		dalt := altRate * eta / 60

		// possibleRange is altitude range the aircraft could have at this
		// waypoint, given its performance characteristics and assuming it
		// will meet the constraint at the subsequent waypoint with an
		// altitude restriction.
		//
		// Note that dalt only applies to one limit, since the aircraft can
		// always maintain its current altitude between waypoints; which
		// limit depends on whether it is climbing or descending (but then
		// which one and the addition/subtraction are confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := altRange
		if nav.FlightState.IsDeparture {
			possibleRange[0] -= dalt
		} else {
			possibleRange[1] += dalt
		}

		// Limit the possible range according to the restriction at the
		// current waypoint.
		var ok bool
		altRange, ok = restr.ClampRange(possibleRange)
		if !ok {
			lg.Infof("unable to fulfill altitude restriction at %s: possible %v required %v",
				wp.Fix, possibleRange, restr.Range)
			// Keep using altRange, FWIW; it will be clamped to whichever of the
			// low and high of the restriction's range it is closest to.
		}

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Add the distance to the first waypoint to get the total distance
	// (and then the ETA) between the aircraft and the first waypoint with
	// an altitude restriction.
	d := sumDist + nmdistance2ll(nav.FlightState.Position, nav.Waypoints[0].Location)
	eta := d / nav.FlightState.GS * 3600 // seconds
	alt := altRange[1]                   // prefer to be higher rather than lower

	return &WaypointCrossingConstraint{
		Altitude:      alt,
		ETA:           eta,
		FinalFix:      nav.Waypoints[lastWp].Fix,
		FinalAltitude: finalAlt,
	}
}

func (nav *Nav) TargetSpeed(lg *Logger) (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	fd, err := nav.finalApproachDistance()
	if err == nil && fd < 5 {
		// Cancel speed restrictions inside 5 mile final
		lg.Debug("speed: cancel speed restrictions at 5 mile final")
		nav.Speed = NavSpeed{}
	}

	if nav.Speed.AfterAltitude != nil &&
		(nav.Speed.Assigned == nil || *nav.Speed.Assigned == nav.FlightState.IAS) {
		if nav.FlightState.Altitude == *nav.Speed.AfterAltitudeAltitude {
			// Reached altitude, now go for speed
			lg.Debugf("speed: reached altitude %.0f; now going for speed %.0f",
				*nav.Speed.AfterAltitudeAltitude, *nav.Speed.AfterAltitude)
			nav.Speed.Assigned = nav.Speed.AfterAltitude
			nav.Speed.AfterAltitude = nil
			nav.Speed.AfterAltitudeAltitude = nil
		}
	}

	if nav.Speed.MaintainSlowestPractical {
		lg.Debug("speed: slowest practical")
		return nav.v2() + 5, MaximumRate
	}
	if nav.Speed.MaintainMaximumForward {
		lg.Debug("speed: maximum forward")
		if nav.Approach.Cleared {
			// (We expect this to usually be the case.) Ad-hoc speed based
			// on V2, also assuming some flaps are out, so we don't just
			// want to return 250 knots here...
			return min(nav.v2()*1.6, min(250, nav.Perf.Speed.Cruise)), MaximumRate
		}
		return nav.targetAltitudeIAS()
	}

	if nav.FlightState.IsDeparture {
		targetSpeed := min(250, nav.Perf.Speed.Cruise)

		if !nav.IsAirborne() {
			return targetSpeed, 0.8 * maxAccel
		} else if agl := nav.FlightState.Altitude - nav.FlightState.DepartureAirportElevation; agl < initialClimbAltitude {
			// Just airborne; prioritize climb
			lg.Debugf("speed: prioritize climb at %.0f AGL; acceleration limited", agl)
			return targetSpeed, 0.2 * maxAccel
		}
		// Otherwise fall through to the cases below
	}

	if nav.Speed.Assigned != nil {
		lg.Debugf("speed: %.0f assigned", *nav.Speed.Assigned)
		return *nav.Speed.Assigned, MaximumRate
	}

	if wp, speed, eta := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && wp != nil {
		lg.Debugf("speed: %.0f to cross %s in %.0fs", speed, wp.Fix, eta)
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

	// Something from a previous waypoint; ignore it if we're cleared for the approach.
	if nav.Speed.Restriction != nil && !nav.Approach.Cleared {
		lg.Debugf("speed: previous restriction %.0f", *nav.Speed.Restriction)
		return *nav.Speed.Restriction, MaximumRate
	}

	// Absent controller speed restrictions, slow down arrivals starting 15 miles out.
	if nav.Speed.Assigned == nil && fd != 0 && fd < 15 {
		spd := nav.Perf.Speed
		// Expected speed at 10 DME, without further direction.
		approachSpeed := 1.25 * spd.Landing

		x := clamp((fd-1)/9, float32(0), float32(1))
		ias := lerp(x, spd.Landing, approachSpeed)
		// Don't speed up after being been cleared to land.
		ias = min(ias, nav.FlightState.IAS)

		lg.Debugf("speed: approach cleared, %.1f nm out, ias %.0f", fd, ias)
		return ias, MaximumRate
	}

	if nav.Approach.Cleared {
		// Don't speed up if we're cleared and farther away
		lg.Debugf("speed: cleared approach but far away")
		return nav.FlightState.IAS, MaximumRate
	}

	// Nothing assigned by the controller or the route, so set a target
	// based on the aircraft's altitude.
	ias, rate := nav.targetAltitudeIAS()
	lg.Debugf("speed: %.0f based on altitude", ias)
	return ias, rate
}

// Compute target airspeed for higher altitudes speed by lerping from 250
// to cruise speed based on altitude.
func (nav *Nav) targetAltitudeIAS() (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	if nav.FlightState.Altitude < 10000 {
		// 250kts under 10k.  We can assume a high acceleration rate for
		// departures when this kicks in at 1500' AGL given that VNav will
		// slow the rate of climb at that point until we reach the target
		// speed.
		return min(nav.Perf.Speed.Cruise, 250), 0.8 * maxAccel
	}

	x := clamp((nav.FlightState.Altitude-10000)/(nav.Perf.Ceiling-10000), 0, 1)
	return lerp(x, min(nav.Perf.Speed.Cruise, 250), nav.Perf.Speed.Cruise), 0.8 * maxAccel
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

func (nav *Nav) updateWaypoints(wind WindModel, lg *Logger) *Waypoint {
	if len(nav.Waypoints) == 0 {
		return nil
	}

	wp := nav.Waypoints[0]

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if len(nav.Waypoints) == 1 && nav.Approach.AtFixClearedRoute != nil &&
		nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix {
		hdg = headingp2ll(wp.Location, nav.Approach.AtFixClearedRoute[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
		// depart fix direct
		hdg = headingp2ll(wp.Location, nfa.Depart.Fix.Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if wp.Arc != nil {
		// Joining a DME arc after the heading
		hdg = wp.Arc.InitialHeading
	} else if len(nav.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = headingp2ll(wp.Location, nav.Waypoints[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = nav.FlightState.Heading
	}

	if nav.shouldTurnForOutbound(wp.Location, hdg, TurnClosest, wind, lg) {
		lg.Debugf("turning outbound from %.1f to %.1f for %s", nav.FlightState.Heading,
			hdg, wp.Fix)

		clearedAtFix := nav.Approach.AtFixClearedRoute != nil && nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix
		if clearedAtFix {
			nav.Approach.Cleared = true
			nav.Speed = NavSpeed{}
			nav.Waypoints = nav.Approach.AtFixClearedRoute
		}
		if nav.Heading.Arc != nil {
			nav.Heading = NavHeading{}
		}

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
		} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
			if !nav.directFix(nfa.Depart.Fix.Fix) {
				lg.Errorf("unable direct %s after %s???", nfa.Depart.Fix.Fix, wp.Fix)
			} else {
				// Hacky: directFix updates the route but below we peel off
				// the current waypoint, so re-add it here so everything
				// works out.
				nav.Waypoints = append([]Waypoint{wp}, nav.Waypoints...)
			}
		} else if wp.Heading != 0 && !clearedAtFix {
			// We have an outbound heading
			hdg := float32(wp.Heading)
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if wp.Arc != nil {
			// Fly the DME arc
			nav.Heading = NavHeading{Arc: wp.Arc, JoiningArc: true}
		}

		if wp.NoPT {
			nav.Approach.NoPT = true
		}

		nav.Waypoints = nav.Waypoints[1:]
		if nav.Heading.Assigned == nil {
			nav.flyProcedureTurnIfNecessary()
		}

		nav.Check(lg)

		return &wp
	}
	return nil
}

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial.
func (nav *Nav) shouldTurnForOutbound(p Point2LL, hdg float32, turn TurnMethod, wind WindModel, lg *Logger) bool {
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
	nav2.DeferredHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	initialDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position,
		nav2.FlightState.NmPerLongitude), p0, p1)

	// Don't simulate the turn longer than it will take to do it.
	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind, nil)
		curDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position,
			nav2.FlightState.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) {
			// Aircraft is on the other side of the line than it started on.
			lg.Debugf("turning now to intercept outbound in %d seconds", i)
			return true
		}
	}
	return false
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept the radial.
func (nav *Nav) shouldTurnToIntercept(p0 Point2LL, hdg float32, turn TurnMethod, wind WindModel, lg *Logger) bool {
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
	nav2.DeferredHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind, nil)
		curDist := SignedPointLineDistance(ll2nm(nav2.FlightState.Position, nav2.FlightState.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) && abs(curDist) < .25 && headingDifference(hdg, nav2.FlightState.Heading) < 3.5 {
			lg.Debugf("turning now to intercept radial in %d seconds", i)
			return true
		}
	}
	return false
}

func (nav *Nav) GoAround() string {
	hdg := nav.FlightState.Heading
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.DeferredHeading = nil

	nav.Speed = NavSpeed{}

	alt := float32(1000 * int((nav.FlightState.ArrivalAirportElevation+2500)/1000))
	nav.Altitude = NavAltitude{Assigned: &alt}

	nav.Approach = NavApproach{}

	nav.Waypoints = nil

	return Sample([]string{"going around", "on the go"})
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool) string {
	if alt > nav.Perf.Ceiling {
		return "unable. That altitude is above our ceiling."
	}

	var response string
	if alt > nav.FlightState.Altitude {
		response = Sample([]string{"climb and maintain ", "up to "}) + FormatAltitude(alt)
	} else if alt == nav.FlightState.Altitude {
		response = Sample([]string{"maintain ", "we'll keep it at "}) + FormatAltitude(alt)
	} else {
		response = Sample([]string{"descend and maintain ", "down to "}) + FormatAltitude(alt)
	}

	if afterSpeed && nav.Speed.Assigned != nil && *nav.Speed.Assigned != nav.FlightState.IAS {
		nav.Altitude.AfterSpeed = &alt
		spd := *nav.Speed.Assigned
		nav.Altitude.AfterSpeedSpeed = &spd

		return fmt.Sprintf("at %.0f knots, ", *nav.Speed.Assigned) + response
	} else {
		nav.Altitude = NavAltitude{Assigned: &alt}
		return response
	}
}

func (nav *Nav) AssignSpeed(speed float32, afterAltitude bool) string {
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
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		nav.Speed.AfterAltitude = &speed
		alt := *nav.Altitude.Assigned
		nav.Speed.AfterAltitudeAltitude = &alt

		return fmt.Sprintf("at %s feet maintain %.0f knots", FormatAltitude(alt), speed)
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

func (nav *Nav) MaintainSlowestPractical() string {
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}
	return Sample([]string{"we'll maintain slowest practical speed", "slowing as much as we can"})
}

func (nav *Nav) MaintainMaximumForward() string {
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	return Sample([]string{"we'll keep it at maximum forward speed", "maintaining maximum forward speed"})
}

func (nav *Nav) ExpediteDescent() string {
	alt, _ := nav.TargetAltitude(nil)
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
	alt, _ := nav.TargetAltitude(nil)
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
	if hdg <= 0 || hdg > 360 {
		return fmt.Sprintf("unable. %.0f isn't a valid heading", hdg)
	}

	// Only cancel approach clearance if the aircraft wasn't on a
	// heading and now we're giving them one.
	if _, ok := nav.AssignedHeading(); !ok {
		nav.Approach.Cleared = false
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(NavHeading{Assigned: &hdg, Turn: &turn})

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
	if _, ok := nav.AssignedHeading(); !ok {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false
	}

	hdg := nav.FlightState.Heading
	nav.EnqueueHeading(NavHeading{Assigned: &hdg})
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

func (nav *Nav) fixPairInRoute(fixa, fixb string) (fa *Waypoint, fb *Waypoint) {
	find := func(f string, wp []Waypoint) int {
		return FindIf(wp, func(wp Waypoint) bool { return wp.Fix == f })
	}

	var apWaypoints []WaypointArray
	if nav.Approach.Assigned != nil {
		apWaypoints = nav.Approach.Assigned.Waypoints
	}

	if ia := find(fixa, nav.Waypoints); ia != -1 {
		// First fix is in the current route
		fa = &nav.Waypoints[ia]
		if ib := find(fixb, nav.Waypoints[ia:]); ib != -1 {
			// As is the second, and after the first
			fb = &nav.Waypoints[ia+ib]
			return
		}
		for _, wp := range apWaypoints {
			if idx := find(fixb, wp); idx != -1 {
				fb = &wp[idx]
				return
			}
		}
	} else {
		// Check the approaches
		for _, wp := range apWaypoints {
			if ia := find(fixa, wp); ia != -1 {
				fa = &wp[ia]
				if ib := find(fixb, wp[ia:]); ib != -1 {
					fb = &wp[ia+ib]
					return
				}
			}
		}
	}
	return
}

func (nav *Nav) directFix(fix string) bool {
	// Look for the fix in the waypoints in the flight plan.
	for i, wp := range nav.Waypoints {
		if fix == wp.Fix {
			nav.Waypoints = nav.Waypoints[i:]
			return true
		}
	}

	if ap := nav.Approach.Assigned; ap != nil {
		for _, route := range ap.Waypoints {
			for _, wp := range route {
				if wp.Fix == fix {
					nav.Waypoints = []Waypoint{wp}
					return true
				}
			}
		}
	}
	return false
}

func (nav *Nav) DirectFix(fix string) string {
	if nav.directFix(fix) {
		nav.EnqueueHeading(NavHeading{})
		nav.Approach.NoPT = false
		nav.Approach.InterceptState = NotIntercepting

		return "direct " + FixReadback(fix)
	} else {
		return "unable. " + FixReadback(fix) + " isn't in our route"
	}
}

func (nav *Nav) DepartFixDirect(fixa string, fixb string) string {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return "unable. " + fixa + " isn't in our route"
	}
	if fb == nil {
		return "unable. " + fixb + " isn't in our route after " + fixa
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return "depart " + FixReadback(fixa) + " direct " + FixReadback(fixb)
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

	response := "depart " + FixReadback(fix)
	return fmt.Sprintf(response+" heading %03d", int(hdg))
}

func (nav *Nav) CrossFixAt(fix string, ar *AltitudeRestriction, speed int) string {
	if !nav.fixInRoute(fix) {
		return "unable. " + fix + " isn't in our route"
	}

	response := "cross " + FixReadback(fix) + " "

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		response += ar.Summary()
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if speed != 0 {
		s := float32(speed)
		nfa.Arrive.Speed = &s
		response += fmt.Sprintf(" at %.0f knots", s)
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

func (nav *Nav) ExpectApproach(airport string, id string, arr *Arrival, w *World, lg *Logger) (string, error) {
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
		if len(nav.Waypoints) == 0 {
			// Nothing left on our route; assume that it has (hopefully
			// recently) passed the last fix and that patching in the rest
			// will work out.
			nav.Waypoints = DuplicateSlice(waypoints[1:])
		} else {
			// Try to splice the runway-specific waypoints in with the
			// aircraft's current waypoints...
			found := false
			for i, wp := range waypoints {
				if idx := FindIf(nav.Waypoints, func(w Waypoint) bool { return w.Fix == wp.Fix }); idx != -1 {
					nav.Waypoints = nav.Waypoints[:idx]
					nav.Waypoints = append(nav.Waypoints, waypoints[i:]...)
					found = true
					break
				}
			}

			if !found {
				// Most likely they were told to expect one runway, then
				// given a different one, but after they passed the common
				// set of waypoints on the arrival.  We'll replace the
				// waypoints but leave them on their current heading; then
				// it's over to the controller to either vector them or
				// send them direct somewhere reasonable...
				lg.Warn("aircraft waypoints don't match up with arrival runway waypoints. splicing...",
					slog.Any("aircraft", nav.Waypoints),
					slog.Any("runway", waypoints))
				nav.Waypoints = DuplicateSlice(waypoints)
				hdg := nav.FlightState.Heading
				nav.Heading = NavHeading{Assigned: &hdg}
				nav.DeferredHeading = nil
			}
		}
	}

	opener := Sample([]string{"we'll expect the", "expecting the", "we'll plan for the"})
	return opener + " " + ap.FullName + " approach", nil
}

func (nav *Nav) InterceptLocalizer(airport string, arr *Arrival, w *World) string {
	if nav.Approach.AssignedId == "" {
		return "you never told us to expect an approach"
	}

	ap := nav.Approach.Assigned
	if ap.Type != ILSApproach {
		return "we can only intercept an ILS approach"
	}
	if _, ok := nav.AssignedHeading(); !ok {
		return "we have to be flying a heading to intercept"
	}

	resp, err := nav.prepareForApproach(airport, false, arr, w)
	if err != nil {
		return resp
	} else {
		return Sample([]string{"intercepting the " + ap.FullName + " approach",
			"intercepting " + ap.FullName})
	}
}

func (nav *Nav) AtFixCleared(fix, id string) string {
	if nav.Approach.AssignedId == "" {
		return "you never told us to expect an approach"
	}

	ap := nav.Approach.Assigned
	if ap == nil {
		return "unable. We were never told to expect an approach"
	}
	if nav.Approach.AssignedId != id {
		return "unable. We were told to expect the " + ap.FullName + " approach..."
	}

	if idx := FindIf(nav.Waypoints, func(wp Waypoint) bool { return wp.Fix == fix }); idx == -1 {
		return "unable. " + fix + " is not in our route"
	}
	nav.Approach.AtFixClearedRoute = nil
	for _, route := range ap.Waypoints {
		for i, wp := range route {
			if wp.Fix == fix {
				nav.Approach.AtFixClearedRoute = DuplicateSlice(route[i:])
			}
		}
	}

	return Sample([]string{"at " + fix + ", cleared " + ap.FullName,
		"cleared " + ap.FullName + " at " + fix})
}

func (nav *Nav) prepareForApproach(airport string, straightIn bool, arr *Arrival, w *World) (string, error) {
	if nav.Approach.AssignedId == "" {
		return "you never told us to expect an approach", ErrClearedForUnexpectedApproach
	}

	ap := nav.Approach.Assigned

	directApproachFix := false
	_, assignedHeading := nav.AssignedHeading()
	if !assignedHeading && len(nav.Waypoints) > 0 {
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

	if directApproachFix {
		// all good
	} else if assignedHeading {
		nav.Approach.InterceptState = InitialHeading
	} else {
		return "unable. We need either direct or a heading to intercept", ErrUnableCommand
	}
	// If the aircraft is on a heading, there's nothing more to do for
	// now; keep flying the heading and after we intercept we'll add
	// the rest of the waypoints to the aircraft's waypoints array.

	// No procedure turn if it intercepts via a heading
	nav.Approach.NoPT = straightIn || assignedHeading

	return "", nil
}

func (nav *Nav) clearedApproach(airport string, id string, straightIn bool, arr *Arrival,
	w *World) (string, error) {
	ap := nav.Approach.Assigned
	if ap == nil {
		return "unable. We haven't been told to expect an approach", ErrClearedForUnexpectedApproach
	}
	if nav.Approach.AssignedId != id {
		return "unable. We were told to expect the " + ap.FullName + " approach...",
			ErrClearedForUnexpectedApproach
	}

	if resp, err := nav.prepareForApproach(airport, straightIn, arr, w); err != nil {
		return resp, err
	} else {
		nav.Approach.Cleared = true
		if nav.Approach.InterceptState == HoldingLocalizer {
			// First intercepted then cleared, so allow it to start descending.
			nav.Altitude = NavAltitude{}
		}
		// Cleared approach also cancels speed restrictions.
		nav.Speed = NavSpeed{}

		nav.flyProcedureTurnIfNecessary()

		if straightIn {
			return "cleared straight in " + ap.FullName + " approach", nil
		} else {
			return "cleared " + ap.FullName + " approach", nil
		}
	}
}

func (nav *Nav) CancelApproachClearance() string {
	if !nav.Approach.Cleared {
		return "we're not currently cleared for an approach"
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false

	return "cancel approach clearance."
}

func (nav *Nav) ClimbViaSID() string {
	if !nav.FlightState.IsDeparture {
		return "unable. We're not a departure"
	}
	if len(nav.Waypoints) == 0 {
		return "unable. We are not on a route"
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
	return "climb via the SID"
}

func (nav *Nav) DescendViaSTAR() string {
	if nav.FlightState.IsDeparture {
		return "unable. We're not an arrival"
	}
	if len(nav.Waypoints) == 0 {
		return "unable. We are not on a route"
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
	return "descend via the STAR"
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

		if headingDifference(acFixHeading, inboundHeading) < 90 {
			return
		}
	}

	switch wp[0].ProcedureTurn.Type {
	case PTRacetrack:
		// Immediate heading update here (and below) since it's the
		// autopilot doing this at the appropriate time (vs. a controller
		// instruction.)
		nav.Heading = NavHeading{RacetrackPT: MakeFlyRacetrackPT(nav, wp)}
		nav.DeferredHeading = nil

	case PTStandard45:
		nav.Heading = NavHeading{Standard45PT: MakeFlyStandard45PT(nav, wp)}
		nav.DeferredHeading = nil

	default:
		lg.Error("Unhandled procedure turn type")
	}
}

func MakeFlyStandard45PT(nav *Nav, wp []Waypoint) *FlyStandard45PT {
	inboundHeading := headingp2ll(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	awayHeading := OppositeHeading(inboundHeading)
	awayHeading += float32(Select(wp[0].ProcedureTurn.RightTurns, -45, 45))

	pt := &FlyStandard45PT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		AwayHeading:    NormalizeHeading(awayHeading),
		State:          PTStateApproaching,
	}

	lg.Debug("made FlyStandard45PT", slog.Any("pt", pt))

	return pt
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

		lg.Debugf("hdg %.0f outbound hdg %.0f diff %.0f -> rate %.1f",
			acFixHeading, fp.OutboundHeading, headingDifference(fp.OutboundHeading, acFixHeading),
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

	lg.Debug("made FlyRacetrackPT", slog.Any("pt", fp))

	return fp
}

func (fp *FlyRacetrackPT) GetHeading(nav *Nav, wind WindModel, lg *Logger) (float32, TurnMethod, float32) {
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
				fp.OutboundTurnMethod, wind, lg)

		case ParallelEntry, TeardropEntry:
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.OutboundHeading,
				fp.OutboundTurnMethod, wind, lg)
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
			lg.Debugf("starting outbound turn-heading %.1f rate %.2f method %s",
				fp.OutboundHeading, fp.OutboundTurnRate, fp.OutboundTurnMethod.String())
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := headingp2ll(nav.FlightState.Position, fp.FixLocation, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)
		return fixHeading, TurnClosest, StandardTurnRate

	case PTStateTurningOutbound:
		if headingDifference(nav.FlightState.Heading, fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			lg.Debugf("finished the turn; ac heading %.1f outbound %.1f; flying outbound leg",
				nav.FlightState.Heading, fp.OutboundHeading)
			fp.State = PTStateFlyingOutbound
		}

		return fp.OutboundHeading, fp.OutboundTurnMethod, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := nmdistance2ll(nav.FlightState.Position, fp.FixLocation)

		if fp.Entry == TeardropEntry {
			// start the turn when we will intercept the inbound radial
			turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
			if d > 0.5 && nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
				lg.Debug("teardrop Turning inbound!")
				fp.State = PTStateTurningInbound
			}
		} else if d > fp.OutboundLegLength {
			lg.Debug("Turning inbound!")
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if fp.Entry == ParallelEntry {
			// Parallel is special: we fly at the 30 degree
			// offset-from-true-inbound heading until it is time to turn to
			// intercept.
			hdg := NormalizeHeading(fp.InboundHeading + float32(Select(pt.RightTurns, -30, 30)))
			lg.Debugf("parallel inbound turning to %.1f", hdg)
			if headingDifference(nav.FlightState.Heading, hdg) < 1 {
				fp.State = PTStateFlyingInbound
			}
			// This turn is in the opposite direction than usual
			turn := Select(!pt.RightTurns, TurnRight, TurnLeft)
			return hdg, TurnMethod(turn), StandardTurnRate
		} else {
			if headingDifference(nav.FlightState.Heading, fp.InboundHeading) < 1 {
				// otherwise go direct to the fix
				lg.Debug("direct fix--done with the HILPT!")
				nav.Heading = NavHeading{}
				nav.Altitude = NavAltitude{}
			}

			turn := Select(pt.RightTurns, TurnRight, TurnLeft)
			return fp.InboundHeading, TurnMethod(turn), StandardTurnRate
		}

	case PTStateFlyingInbound:
		// This state is only used for ParallelEntry
		turn := TurnMethod(Select(pt.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
			lg.Debug("parallel inbound direct fix")
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

func (fp *FlyStandard45PT) GetHeading(nav *Nav, wind WindModel, lg *Logger) (float32, TurnMethod, float32) {
	outboundHeading := OppositeHeading(fp.InboundHeading)

	switch fp.State {
	case PT45StateApproaching:
		if nav.shouldTurnForOutbound(fp.FixLocation, outboundHeading, TurnClosest, wind, lg) {
			lg.Debugf("turning outbound to %.0f", outboundHeading)
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
			lg.Debugf("flying outbound for %ds", fp.SecondsRemaining)
		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingOutbound:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningAway
			lg.Debugf("turning away from outbound to %.0f", fp.AwayHeading)

		}
		return outboundHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningAway:
		if nav.FlightState.Heading == fp.AwayHeading {
			fp.State = PT45StateFlyingAway
			fp.SecondsRemaining = 60
			lg.Debugf("flying away for %ds", fp.SecondsRemaining)
		}

		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateFlyingAway:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningIn
			lg.Debugf("turning in to %.0f", OppositeHeading(fp.AwayHeading))
		}
		return fp.AwayHeading, TurnClosest, StandardTurnRate

	case PT45StateTurningIn:
		hdg := OppositeHeading(fp.AwayHeading)
		if nav.FlightState.Heading == hdg {
			fp.State = PT45StateFlyingIn
			lg.Debug("flying in")
		}

		turn := Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft)
		return hdg, TurnMethod(turn), StandardTurnRate

	case PT45StateFlyingIn:
		turn := TurnMethod(Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
			fp.State = PT45StateTurningToIntercept
			lg.Debugf("starting turn to intercept %.0f", fp.InboundHeading)
		}
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate

	case PT45StateTurningToIntercept:
		if nav.FlightState.Heading == fp.InboundHeading {
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
			lg.Debugf("done! direct to the fix now")
		}

		return fp.InboundHeading, TurnClosest, StandardTurnRate

	default:
		lg.Errorf("unhandled PT state: %d", fp.State)
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate
	}
}

// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type Aircraft struct {
	Callsign       string
	Scratchpad     string
	AssignedSquawk Squawk // from ATC
	Squawk         Squawk // actually squawking
	Mode           TransponderMode
	TempAltitude   int
	FlightPlan     *FlightPlan

	MagneticVariation float32
	NmPerLongitude    float32

	// Who has the radar track
	TrackingController string
	// Who has control of the aircraft; may not be the same as
	// TrackingController, e.g. after an aircraft has been flashed but
	// before they have been instructed to contact the new tracking
	// controller.
	ControllingController string

	// Handoff offered but not yet accepted
	HandoffTrackController string

	// The controller who gave approach clearance
	ApproachController string

	Strip     FlightStrip
	Waypoints []Waypoint

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...

	IsDeparture bool
	NoPT        bool

	Nav NAVState

	// Set for arrivals, if there are runway-specific waypoints.
	ArrivalRunwayWaypoints   map[string]WaypointArray
	ArrivalHandoffController string

	Approach            *Approach
	ApproachId          string
	ApproachCleared     bool
	HaveEnteredAirspace bool
}

func (a *Aircraft) Performance() AircraftPerformance {
	perf, ok := database.AircraftPerformance[a.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model?!", a.Callsign)
	}
	return perf
}

func (ac *Aircraft) TAS() float32 {
	// Simple model for the increase in TAS as a function of altitude: 2%
	// additional TAS on top of IAS for each 1000 feet.
	return ac.IAS * (1 + .02*ac.Altitude/1000)
}

// Returns the estimated time in which the aircraft will reach the next fix
// in its waypoints, assuming it is flying direct to it at its current
// speed.
func (ac *Aircraft) NextFixETA() (time.Duration, bool) {
	if len(ac.Waypoints) == 0 {
		return 0, false
	}
	return ac.Waypoints[0].ETA(ac.Position, ac.GS), true
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (ac *Aircraft) AddFutureNavCommand(cmd FutureNavCommand) {
	if ac.Nav.FutureCommands == nil {
		ac.Nav.FutureCommands = make(map[FutureNavCommand]interface{})
	}
	for other := range ac.Nav.FutureCommands {
		if reflect.TypeOf(cmd) == reflect.TypeOf(other) {
			delete(ac.Nav.FutureCommands, other)
		}
	}
	ac.Nav.FutureCommands[cmd] = nil
}

func (ac *Aircraft) HaveAssignedSpeed() bool {
	if _, ok := ac.Nav.S.(*MaintainSpeed); ok {
		return true
	}
	for cmd := range ac.Nav.FutureCommands {
		if reflect.TypeOf(cmd) == reflect.TypeOf(&SpeedAfterAltitude{}) {
			return true
		}
	}

	return false
}

func (ac *Aircraft) DropControllerTrack(callsign string) {
	if ac.HandoffTrackController == callsign {
		ac.HandoffTrackController = ""
	}
	if ac.ControllingController == callsign {
		if ac.TrackingController == callsign {
			ac.TrackingController = ""
			ac.ControllingController = ""
		} else {
			// Another controller has the track but not yet control;
			// just give them control
			ac.ControllingController = ac.TrackingController
		}
	}
}

func (ac *Aircraft) TransferTracks(from, to string) {
	if ac.HandoffTrackController == from {
		ac.HandoffTrackController = to
	}
	if ac.TrackingController == from {
		ac.TrackingController = to
	}
	if ac.ControllingController == from {
		ac.ControllingController = to
	}
	if ac.ApproachController == from {
		ac.ApproachController = to
	}
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

func (ac *Aircraft) Update(wind WindModel, w *World, ep EventPoster) {
	ac.updateAirspeed()
	ac.updateAltitude()
	ac.updateHeading(wind)
	ac.updatePositionAndGS(wind)
	if ac.Nav.L.PassesWaypoints() {
		ac.updateWaypoints(wind, w, ep)
	}

	for cmd := range ac.Nav.FutureCommands {
		if cmd.Evaluate(ac, ep, wind) {
			delete(ac.Nav.FutureCommands, cmd)
		}
	}
}

func (ac *Aircraft) GoAround() string {
	ac.Nav.L = &FlyHeading{Heading: ac.Heading}

	spd := ac.Performance().Speed
	targetSpeed := min(1.6*spd.Landing, 0.8*spd.Cruise)
	ac.Nav.S = &MaintainSpeed{IAS: targetSpeed}

	if ap, ok := database.Airports[ac.FlightPlan.ArrivalAirport]; ok {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(1000 * ((ap.Elevation + 2500) / 1000))}
	} else {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(1000 * ((int(ac.Altitude) + 2500) / 1000))}
	}

	ac.Approach = nil
	ac.ApproachId = ""
	ac.ApproachCleared = false
	ac.ApproachController = ""
	ac.Waypoints = nil
	ac.NoPT = false

	// Nuke all of them...
	ac.Nav.FutureCommands = make(map[FutureNavCommand]interface{})

	return "Going around"
}

func (ac *Aircraft) AssignAltitude(altitude int) (string, error) {
	if altitude > int(ac.Performance().Ceiling) {
		return "unable-that altitude is above our ceiling", ErrInvalidAltitude
	}

	var response string
	if float32(altitude) > ac.Altitude {
		response = fmt.Sprintf("climb and maintain %d", altitude)
	} else if float32(altitude) == ac.Altitude {
		response = fmt.Sprintf("maintain %d", altitude)
	} else {
		response = fmt.Sprintf("descend and maintain %d", altitude)
	}

	if spd, ok := ac.Nav.S.(*MaintainSpeed); ok && spd.IAS != ac.IAS {
		ac.AddFutureNavCommand(&AltitudeAfterSpeed{
			FromAbove: ac.IAS > spd.IAS,
			IAS:       spd.IAS,
			Altitude:  float32(altitude),
		})
		response = fmt.Sprintf("at %.0f knots, ", spd.IAS) + response
	} else {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(altitude)}
	}

	return response, nil
}

func (ac *Aircraft) AssignSpeed(speed int) (response string, err error) {
	perf := ac.Performance()

	if speed == 0 {
		response = "cancel speed restrictions"
		ac.Nav.S = &FlyRoute{}
		return
	} else if float32(speed) < perf.Speed.Landing {
		response = fmt.Sprintf("unable--our minimum speed is %.0f knots", perf.Speed.Landing)
		err = ErrUnableCommand
		return
	} else if float32(speed) > perf.Speed.Max {
		response = fmt.Sprintf("unable--our maximum speed is %.0f knots", perf.Speed.Max)
		err = ErrUnableCommand
		return
	}

	if ac.ApproachCleared {
		response = fmt.Sprintf("maintain %d knots until 5 mile final", speed)
		ac.Nav.S = &MaintainSpeed{IAS: float32(speed)}
	} else if alt, _ := ac.Nav.V.GetAltitude(ac); alt != ac.Altitude {
		response = fmt.Sprintf("at %.0f feet, maintain %d knots",
			alt, speed)
		ac.AddFutureNavCommand(&SpeedAfterAltitude{
			Altitude:  alt,
			FromAbove: alt < ac.Altitude,
			IAS:       float32(speed),
		})
	} else {
		response = fmt.Sprintf("maintain %d knots", speed)
		ac.Nav.S = &MaintainSpeed{IAS: float32(speed)}
	}
	return
}

func (ac *Aircraft) AssignHeading(heading int, turn TurnMethod) (response string, err error) {
	// A 0 heading shouldn't be specified, but at least cause the
	// aircraft to do what is intended, since 0 represents an
	// unassigned heading.
	if heading == 0 {
		heading = 360
	}
	if heading < 0 || heading > 360 {
		return "", ErrInvalidHeading
	}

	// Only cancel approach clearance if the aircraft wasn't on a
	// heading and now we're giving them one.
	if _, ok := ac.Nav.L.(*FlyHeading); !ok {
		ac.CancelApproachClearance()
	}

	switch turn {
	case TurnClosest:
		response = fmt.Sprintf("fly heading %d", heading)

	case TurnRight:
		response = fmt.Sprintf("turn right heading %d", heading)

	case TurnLeft:
		response = fmt.Sprintf("turn left heading %d", heading)
	}

	ac.NoPT = false
	ac.Nav.L = &FlyHeading{Heading: float32(heading), Turn: turn}

	return
}

func (ac *Aircraft) TurnLeft(deg int) (string, error) {
	heading := ac.Heading
	if fh, ok := ac.Nav.L.(*FlyHeading); ok {
		heading = fh.Heading
	} else {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		ac.CancelApproachClearance()
	}

	heading = NormalizeHeading(heading - float32(deg))
	ac.Nav.L = &FlyHeading{Heading: heading}
	ac.NoPT = false

	return fmt.Sprintf("turn %d degrees left", deg), nil
}

func (ac *Aircraft) TurnRight(deg int) (string, error) {
	heading := ac.Heading
	if fh, ok := ac.Nav.L.(*FlyHeading); ok {
		heading = fh.Heading
	} else {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		ac.CancelApproachClearance()
	}

	heading = NormalizeHeading(heading + float32(deg))
	ac.Nav.L = &FlyHeading{Heading: heading}
	ac.NoPT = false

	return fmt.Sprintf("turn %d degrees right", deg), nil
}

func (ac *Aircraft) visitRouteFix(fix string, cb func(*Waypoint) bool) {
	for i := range ac.Waypoints {
		if fix == ac.Waypoints[i].Fix {
			if !cb(&ac.Waypoints[i]) {
				return
			}
		}
	}

	ap := ac.Approach
	if ap == nil {
		return
	}
	for _, route := range ap.Waypoints {
		for i := range route {
			if fix == route[i].Fix {
				if !cb(&route[i]) {
					return
				}
			}
		}
	}
}

func (ac *Aircraft) DirectFix(fix string) (string, error) {
	fix = strings.ToUpper(fix)

	// Look for the fix in the waypoints in the flight plan.
	found := false
	for i, wp := range ac.Waypoints {
		if fix == wp.Fix {
			ac.Waypoints = ac.Waypoints[i:]
			found = true
			break
		}
	}

	if !found {
		if ap := ac.Approach; ap != nil {
			for _, route := range ap.Waypoints {
				for _, wp := range route {
					if wp.Fix == fix {
						ac.Waypoints = []Waypoint{wp}
						found = true
						break
					}
				}
			}
		}
	}

	if found {
		if !ac.flyProcedureTurnIfNecessary() {
			ac.Nav.L = &FlyRoute{}
		}
		ac.NoPT = false

		for cmd := range ac.Nav.FutureCommands {
			switch reflect.TypeOf(cmd) {
			case reflect.TypeOf(&HoldLocalizerAfterIntercept{}):
				delete(ac.Nav.FutureCommands, cmd)
			case reflect.TypeOf(&TurnToInterceptLocalizer{}):
				delete(ac.Nav.FutureCommands, cmd)
			}
		}

		// If it's a VOR, read back the actual name
		if nav, ok := database.Navaids[fix]; ok {
			return "direct " + stopShouting(nav.Name), nil
		} else {
			return "direct " + fix, nil
		}
	} else {
		return "", ErrFixNotInRoute
	}
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) (string, error) {
	fix = strings.ToUpper(fix)

	if hdg <= 0 || hdg > 360 {
		return "", ErrInvalidHeading
	}

	found := false
	ac.visitRouteFix(fix, func(wp *Waypoint) bool {
		wp.Heading = hdg
		found = true
		return true
	})

	if found {
		return fmt.Sprintf("depart %s heading %03d", fix, hdg), nil
	} else {
		return "", ErrFixNotInRoute
	}
}

func (ac *Aircraft) CrossFixAt(fix string, alt int, speed int) (string, error) {
	fix = strings.ToUpper(fix)

	found := false
	ac.visitRouteFix(fix, func(wp *Waypoint) bool {
		wp.Altitude = alt
		wp.Speed = speed
		found = true
		return true
	})

	if !found {
		return "", ErrFixNotInRoute
	}

	response := "cross " + fix
	if alt != 0 {
		ac.Nav.V = &FlyRoute{}
		response += fmt.Sprintf(" at and maintain %d", alt)
	}
	if speed != 0 {
		ac.Nav.S = &FlyRoute{}
		response += fmt.Sprintf(" at %d knots", speed)
	}
	return response, nil
}

func (ac *Aircraft) flyProcedureTurnIfNecessary() bool {
	wp := ac.Waypoints
	if !ac.ApproachCleared || len(wp) == 0 || wp[0].ProcedureTurn == nil || ac.NoPT {
		return false
	}

	if wp[0].ProcedureTurn.Entry180NoPT {
		inboundHeading := headingp2ll(wp[0].Location, wp[1].Location, ac.NmPerLongitude,
			ac.MagneticVariation)
		acFixHeading := headingp2ll(ac.Position, wp[0].Location, ac.NmPerLongitude,
			ac.MagneticVariation)
		lg.Errorf("%s: ac %.1f inbound %.1f diff %.1f", ac.Callsign,
			acFixHeading, inboundHeading, headingDifference(acFixHeading, inboundHeading))

		if headingDifference(acFixHeading, inboundHeading) < 90 {
			return false
		}
	}

	lnav, vnav := MakeFlyProcedureTurn(ac, ac.Waypoints)
	if lnav != nil {
		ac.Nav.L = lnav
	}
	if vnav != nil {
		ac.Nav.V = vnav
	}
	return lnav != nil || vnav != nil
}

func (ac *Aircraft) getApproach(id string, w *World) (*Approach, error) {
	if id == "" {
		return nil, ErrInvalidApproach
	}

	fp := ac.FlightPlan
	if fp == nil {
		return nil, ErrNoFlightPlan
	}

	ap := w.GetAirport(fp.ArrivalAirport)
	if ap == nil {
		lg.Errorf("Can't find airport %s for %s approach for %s", fp.ArrivalAirport, id, ac.Callsign)
		return nil, ErrArrivalAirportUnknown
	}

	for name, appr := range ap.Approaches {
		if name == id {
			return &appr, nil
		}
	}
	return nil, ErrUnknownApproach
}

func (ac *Aircraft) ExpectApproach(id string, w *World) (string, error) {
	ap, err := ac.getApproach(id, w)
	if err != nil {
		return "", err
	}

	if id == ac.ApproachId && ac.Approach != nil {
		return "you already told us to expect the " + ap.FullName + " approach", nil
	}

	ac.Approach = ap
	ac.ApproachId = id

	if waypoints, ok := ac.ArrivalRunwayWaypoints[ap.Runway]; ok && len(waypoints) > 0 {
		// Try to splice the runway-specific waypoints in with the
		// aircraft's current waypoints...
		found := false
		for i, wp := range waypoints {
			if idx := FindIf(ac.Waypoints, func(w Waypoint) bool { return w.Fix == wp.Fix }); idx != -1 {
				ac.Waypoints = ac.Waypoints[:idx]
				ac.Waypoints = append(ac.Waypoints, waypoints[i:]...)

				found = true
				break
			}
		}

		if !found {
			lg.Errorf("%s: Aircraft waypoints %s don't match up with arrival runway waypoints %s",
				ac.Callsign, spew.Sdump(ac.Waypoints), spew.Sdump(waypoints))
			// Assume that it has (hopefully recently) passed the last fix
			// and that patching in the rest will work out..
			ac.Waypoints = DuplicateSlice(waypoints[1:])
		}
	}

	return "we'll expect the " + ap.FullName + " approach", nil
}

func (ac *Aircraft) ClearedApproach(id string, w *World) (response string, err error) {
	return ac.clearedApproach(id, false, w)
}

func (ac *Aircraft) ClearedStraightInApproach(id string, w *World) (response string, err error) {
	return ac.clearedApproach(id, true, w)
}

func (ac *Aircraft) clearedApproach(id string, straightIn bool, w *World) (response string, err error) {
	if ac.ApproachId == "" {
		// allow it anyway...
		if _, err = ac.ExpectApproach(id, w); err != nil {
			return
		}
		response = "you never told us to expect an approach, but ok, cleared " + ac.Approach.FullName
		ac.ApproachId = id
	}

	ap := ac.Approach
	if id != ac.ApproachId {
		response = "but you cleared us for the " + ap.FullName + " approach..."
		err = ErrClearedForUnexpectedApproach
		return
	}
	if ac.ApproachCleared && ac.NoPT == straightIn {
		response = "you already cleared us for the " + ap.FullName + " approach..."
		return
	}

	directApproachFix := false
	var remainingApproachWaypoints []Waypoint
	if _, ok := ac.Nav.L.(*FlyRoute); ok && len(ac.Waypoints) > 0 {
		// Is the aircraft cleared direct to a waypoint on the approach?
		for _, approach := range ap.Waypoints {
			for i, wp := range approach {
				if wp.Fix == ac.Waypoints[0].Fix {
					directApproachFix = true
					if i+1 < len(approach) {
						remainingApproachWaypoints = approach[i+1:]
					}
					break
				}
			}
		}
	}

	if ap.Type == ILSApproach {
		if directApproachFix {
			if remainingApproachWaypoints != nil {
				ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
			}
		} else if _, ok := ac.Nav.L.(*FlyHeading); ok {
			ac.AddFutureNavCommand(&TurnToInterceptLocalizer{})
		} else {
			response = "we need either direct or a heading to intercept"
			return
		}
		// If the aircraft is on a heading, there's nothing more to do for
		// now; keep flying the heading and after we intercept we'll add
		// the rest of the waypoints to the aircraft's waypoints array.
	} else {
		// RNAV
		if !directApproachFix {
			response = "we need direct to a fix on the approach..."
			return
		}

		if remainingApproachWaypoints != nil {
			ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
		}

		// LNav is already known to be FlyRoute; leave SNav and VNav as is
		// until the aircraft reaches the next fix; at that point those
		// will respectively switch to FinalApproachSpeed and FlyRoute.
	}

	ac.NoPT = straightIn
	if _, ok := ac.Nav.L.(*FlyHeading); ok {
		// No procedure turn if it intercepts via a heading
		ac.NoPT = true
	}

	// Cleared approach also cancels speed restrictions, but let's not do
	// that.
	ac.ApproachCleared = true
	ac.ApproachController = ac.ControllingController
	ac.AddFutureNavCommand(&ApproachSpeedAt5DME{})

	ac.flyProcedureTurnIfNecessary()

	if straightIn {
		response += "cleared straight in " + ap.FullName + " approach"
	} else {
		response += "cleared " + ap.FullName + " approach"
	}
	return
}

func (ac *Aircraft) CancelApproachClearance() (string, error) {
	if !ac.ApproachCleared {
		return "We're not currently cleared for an approach", nil
	}

	ac.ApproachCleared = false

	for cmd := range ac.Nav.FutureCommands {
		switch reflect.TypeOf(cmd) {
		case reflect.TypeOf(&HoldLocalizerAfterIntercept{}):
			delete(ac.Nav.FutureCommands, cmd)
		case reflect.TypeOf(&TurnToInterceptLocalizer{}):
			delete(ac.Nav.FutureCommands, cmd)
		case reflect.TypeOf(&ApproachSpeedAt5DME{}):
			delete(ac.Nav.FutureCommands, cmd)
		}
	}

	return "Cancel approach clearance", nil
}

func (ac *Aircraft) updateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance()

	targetSpeed, targetRate := ac.Nav.S.GetSpeed(ac)

	// Stay within the aircraft's capabilities
	targetSpeed = clamp(targetSpeed, perf.Speed.Min, perf.Speed.Max)

	if ac.IAS < targetSpeed {
		accel := perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, targetRate/60)
		ac.IAS = min(targetSpeed, ac.IAS+accel)
	} else if ac.IAS > targetSpeed {
		decel := perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		ac.IAS = max(targetSpeed, ac.IAS-decel)
	}
}

func (ac *Aircraft) updateAltitude() {
	targetAltitude, targetRate := ac.Nav.V.GetAltitude(ac)

	if abs(targetAltitude-ac.Altitude) < 3 {
		ac.Altitude = targetAltitude
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	perf := ac.Performance()
	climb, descent := perf.Rate.Climb, perf.Rate.Descent

	// For high performing aircraft, reduce climb rate after 5,000'
	if climb >= 2500 && ac.Altitude > 5000 {
		climb -= 500
	}
	if ac.Altitude < 10000 {
		// Have a slower baseline rate of descent on approach
		descent = min(descent, 2000)
		// And reduce it based on airspeed as well
		descent *= min(ac.IAS/250, 1)
	}
	climb = min(climb, targetRate)
	descent = min(descent, targetRate)

	if ac.Altitude < targetAltitude {
		// Simple model: we just update altitude based on the rated climb
		// rate; does not account for simultaneous acceleration, etc...
		ac.Altitude = min(targetAltitude, ac.Altitude+climb/60)
	} else if ac.Altitude > targetAltitude {
		// Similarly, descent modeling doesn't account for airspeed or
		// acceleration/deceleration...
		ac.Altitude = max(targetAltitude, ac.Altitude-descent/60)
	}
}

func (ac *Aircraft) updateHeading(wind WindModel) {
	targetHeading, turnDirection, turnRate := ac.Nav.L.GetHeading(ac, wind)

	if headingDifference(ac.Heading, targetHeading) < 1 {
		ac.Heading = targetHeading
		return
	}

	var turn float32
	switch turnDirection {
	case TurnLeft:
		angle := NormalizeHeading(ac.Heading - targetHeading)
		angle = min(angle, turnRate)
		turn = -angle

	case TurnRight:
		angle := NormalizeHeading(targetHeading - ac.Heading)
		angle = min(angle, turnRate)
		turn = angle

	case TurnClosest:
		// Figure out which way is closest: first find the angle to rotate
		// the target heading by so that it's aligned with 180
		// degrees. This lets us not worry about the complexities of the
		// wrap around at 0/360..
		rot := NormalizeHeading(180 - targetHeading)
		cur := NormalizeHeading(ac.Heading + rot) // w.r.t. 180 target
		turn = clamp(180-cur, -turnRate, turnRate)
	}

	// Finally, do the turn.
	ac.Heading = NormalizeHeading(ac.Heading + turn)
}

func (ac *Aircraft) updatePositionAndGS(wind WindModel) {
	// Calculate offset vector based on heading and current TAS.
	hdg := ac.Heading - ac.MagneticVariation
	TAS := ac.TAS() / 3600
	flightVector := scale2f([2]float32{sin(radians(hdg)), cos(radians(hdg))}, TAS)

	// Further offset based on the wind
	perf := ac.Performance()
	airborne := ac.IAS >= 1.1*perf.Speed.Min
	var windVector [2]float32
	if airborne && wind != nil {
		windVector = wind.GetWindVector(ac.Position, ac.Altitude)
	}

	// Update the aircraft's state
	p := add2f(ll2nm(ac.Position, ac.NmPerLongitude), add2f(flightVector, windVector))
	ac.Position = nm2ll(p, ac.NmPerLongitude)
	ac.GS = length2f(add2f(flightVector, windVector)) * 3600
}

func (ac *Aircraft) updateWaypoints(wind WindModel, w *World, ep EventPoster) {
	if len(ac.Waypoints) == 0 {
		return
	}

	wp := ac.Waypoints[0]

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if len(ac.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = headingp2ll(wp.Location, ac.Waypoints[1].Location, ac.NmPerLongitude,
			ac.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = ac.Heading
	}

	if ac.ShouldTurnForOutbound(wp.Location, hdg, TurnClosest, wind) {
		lg.Infof("%s: turning outbound from %.1f to %.1f for %s", ac.Callsign, ac.Heading, hdg, wp.Fix)

		// Execute any commands associated with the waypoint
		ac.RunWaypointCommands(wp, w, ep)

		if ac.ApproachCleared {
			// The aircraft has made it to the approach fix they
			// were cleared to.
			ac.Nav.V = &FlyRoute{}

			// If no speed was assigned, go ahead and start slowing down.
			if !ac.HaveAssignedSpeed() {
				ac.Nav.S = &FinalApproachSpeed{}
			}
		}

		if wp.Altitude != 0 {
			if fr, ok := ac.Nav.V.(*FlyRoute); ok {
				if !ac.ApproachCleared || wp.Altitude < int(ac.Altitude) {
					// Don't climb if we're cleared approach and below the
					// next fix's altitude.
					fr.AltitudeRestriction = float32(wp.Altitude)
				}
			}
		}
		if wp.Speed != 0 {
			if fr, ok := ac.Nav.S.(*FlyRoute); ok {
				fr.SpeedRestriction = float32(wp.Speed)
			}
		}

		if wp.Heading != 0 {
			// We have an outbound heading
			ac.Nav.L = &FlyHeading{Heading: float32(wp.Heading)}
			ac.Waypoints = ac.Waypoints[1:]
		} else {
			ac.Waypoints = ac.Waypoints[1:]
			ac.flyProcedureTurnIfNecessary()
		}

		//lg.Infof("%s", spew.Sdump(ac))
	}
}

func (ac *Aircraft) RunWaypointCommands(wp Waypoint, w *World, ep EventPoster) {
	if wp.Handoff {
		ac.HandoffTrackController = w.Callsign
		if ep != nil {
			ep.PostEvent(Event{
				Type:           OfferedHandoffEvent,
				Callsign:       ac.Callsign,
				FromController: ac.ControllingController,
				ToController:   ac.HandoffTrackController,
			})
		}
	}
	if wp.Delete && w != nil {
		w.DeleteAircraft(ac, nil)
	}
	if wp.NoPT {
		ac.NoPT = true
	}
}

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial.
func (ac *Aircraft) ShouldTurnForOutbound(p Point2LL, hdg float32, turn TurnMethod, wind WindModel) bool {
	dist := nmdistance2ll(ac.Position, p)
	eta := dist / ac.GS * 3600 // in seconds

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Alternatively, if we're far away w.r.t. the needed turn, don't even
	// consider it. This is both for performance but also so that we don't
	// make tiny turns miles away from fixes in some cases.
	turnAngle := TurnAngle(ac.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := ll2nm(p, ac.NmPerLongitude)
	hm := hdg - ac.MagneticVariation
	p1 := add2f(p0, [2]float32{sin(radians(hm)), cos(radians(hm))})

	// Make a ghost aircraft to use to simulate the turn. Checking this way
	// may be somewhat brittle/dangerous, e.g., if there is underlying
	// shared mutable state between ac and ac2.
	ac2 := *ac
	// Don't call Aircraft FlyHeading() since it cancels approach
	// clearance, etc.
	ac2.Nav.L = &FlyHeading{Heading: hdg, Turn: turn}
	ac2.Nav.FutureCommands = make(map[FutureNavCommand]interface{})
	for cmd := range ac.Nav.FutureCommands {
		// Ignore all of the other future commands..
		if _, ok := cmd.(*SpeedAfterAltitude); ok {
			ac2.Nav.FutureCommands[cmd] = nil
		}
	}

	initialDist := SignedPointLineDistance(ll2nm(ac2.Position, ac2.NmPerLongitude), p0, p1)

	// Don't simulate the turn longer than it will take to do it.
	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		ac2.Update(wind, nil, nil)
		curDist := SignedPointLineDistance(ll2nm(ac2.Position, ac2.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) {
			// Aircraft is on the other side of the line than it started on.
			lg.Infof("%s: turning now to intercept outbound in %d seconds",
				ac.Callsign, i)
			//globalConfig.highlightedLocation = ac2.Position
			//globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
			return true
		}
	}
	return false
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept the radial.
func (ac *Aircraft) ShouldTurnToIntercept(p0 Point2LL, hdg float32, turn TurnMethod, wind WindModel) bool {
	p0 = ll2nm(p0, ac.NmPerLongitude)
	p1 := add2f(p0, [2]float32{sin(radians(hdg - ac.MagneticVariation)),
		cos(radians(hdg - ac.MagneticVariation))})

	initialDist := SignedPointLineDistance(ll2nm(ac.Position, ac.NmPerLongitude), p0, p1)
	eta := abs(initialDist) / ac.GS * 3600 // in seconds
	if eta < 2 {
		// Just in case, start the turn
		return true
	}

	// As above, don't consider starting the turn if we're far away.
	turnAngle := TurnAngle(ac.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	ac2 := *ac
	// Don't call Aircraft FlyHeading() since it cancels approach
	// clearance, etc.
	ac2.Nav.L = &FlyHeading{Heading: hdg, Turn: turn}
	ac2.Nav.FutureCommands = make(map[FutureNavCommand]interface{})
	for cmd := range ac.Nav.FutureCommands {
		// Ignore all of the other future commands..
		if _, ok := cmd.(*SpeedAfterAltitude); ok {
			ac2.Nav.FutureCommands[cmd] = nil
		}
	}

	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		ac2.Update(wind, nil, nil)
		curDist := SignedPointLineDistance(ll2nm(ac2.Position, ac2.NmPerLongitude), p0, p1)
		if sign(initialDist) != sign(curDist) && abs(curDist) < .25 && headingDifference(hdg, ac2.Heading) < 3.5 {
			lg.Infof("%s: turning now to intercept radial in %d seconds", ac.Callsign, i)
			//globalConfig.highlightedLocation = ac2.Position
			//globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
			return true
		}
	}
	return false
}

// FinalApproachDistance returns the total remaining flying distance
// for an aircraft that has been given an approach.
func (ac *Aircraft) FinalApproachDistance() (float32, error) {
	if ac.Approach == nil {
		return 0, fmt.Errorf("not cleared for approach")
	}

	// Calculate distance to the airport: distance to the next fix plus sum
	// of the distances between remaining fixes.
	if wp := ac.Waypoints; len(wp) == 0 {
		// This should never happen(tm), but...
		return 0, fmt.Errorf("no waypoints left??")
	} else {
		d := nmdistance2ll(ac.Position, wp[0].Location)
		for i := 0; i < len(wp)-1; i++ {
			d += nmdistance2ll(wp[i].Location, wp[i+1].Location)
		}
		return d, nil
	}
}

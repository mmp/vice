// nav/commands.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// GoAroundWithProcedure initiates a go-around with a defined procedure.
// The runwayEndWP waypoint should have Location (opposite threshold), FlyOver,
// and AltitudeRestriction set, and an action group with the outbound heading
// and the GoAroundContactController.
func (nav *Nav) GoAroundWithProcedure(altitude float32, runwayEndWP av.Waypoint) {
	nav.DeferredNavHeading = nil
	nav.Speed = Speed{}
	nav.Approach = Approach{}
	nav.setAssignedAltitude(altitude)
	nav.Waypoints = av.WaypointArray{runwayEndWP, nav.FlightState.ArrivalAirport}
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	nav.clearFixAltitudes()
	intent, ok := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if !ok {
		return intent
	}
	nav.enqueueAssignedAltitude(alt, simTime, delayReduction)
	return intent
}

func (nav *Nav) prepareAltitudeAssignment(alt float32, afterSpeed bool) (speech.CommandIntent, bool) {
	if alt > nav.Perf.Ceiling {
		return speech.MakeUnableIntent("unable. That altitude is above our ceiling."), false
	}

	var direction speech.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = speech.AltitudeClimb
	} else if alt == nav.FlightState.Altitude {
		direction = speech.AltitudeMaintain
	} else {
		direction = speech.AltitudeDescend
	}

	intent := speech.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
	}

	if sr := nav.Speed.Assigned; afterSpeed && sr != nil {
		if spd, exact := sr.ExactValue(); exact && spd != nav.FlightState.IAS {
			nav.Altitude = Altitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &spd,
			}
			intent.AfterSpeed = &spd
			return intent, false
		}
	}

	// If there's an exact speed change in progress (>=20kt remaining or any Mach change),
	// defer the speed assignment until after the altitude change completes.
	if sr := nav.Speed.Assigned; sr != nil && direction != speech.AltitudeMaintain {
		if spd, exact := sr.ExactValue(); exact &&
			(sr.IsMach || math.Abs(spd-nav.FlightState.IAS) >= 20) {
			srCopy := *sr
			nav.Speed = Speed{
				AfterAltitude:         &srCopy,
				AfterAltitudeAltitude: &alt,
			}
		}
	}

	return intent, true
}

// AssignAltitudeNow assigns an altitude that takes effect immediately, with
// none of the pilot's delay in following a controller's instruction.
func (nav *Nav) AssignAltitudeNow(alt float32, afterSpeed bool) speech.CommandIntent {
	intent, ok := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if ok {
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
	active := nav.activeAssignedAltitude()
	d := nav.Rand.DurationRange(2*time.Second, 4*time.Second)
	if d > delayReduction {
		d -= delayReduction
	} else {
		d = 0
	}
	nav.Altitude = Altitude{
		Assigned:       &alt,
		ActiveAssigned: active,
		ActivateAt:     simTime.Add(d),
	}
}

func (nav *Nav) enqueueAltitudeAfterSpeed(simTime Time) {
	alt := *nav.Altitude.AfterSpeed
	rate := nav.Altitude.RateAfterSpeed
	active := nav.activeAssignedAltitude()
	nav.Altitude = Altitude{
		Assigned:       &alt,
		ActiveAssigned: active,
		ActivateAt:     simTime.Add(nav.Rand.DurationRange(2*time.Second, 4*time.Second)),
		Rate:           rate,
	}
}

func (nav *Nav) AssignMach(mach float32, afterAltitude bool, temp av.Temperature) speech.CommandIntent {
	// Round the limits to hundredths, with slack for the round trip
	// through IAS when the maximum Mach number is what limits maxIAS.
	alt := nav.FlightState.Altitude
	minMach := math.Ceil(100*av.IASToMach(nav.Perf.Speed.Landing, alt)-0.01) / 100
	maxMach := math.Floor(100*av.IASToMach(nav.maxIAS(temp), alt)+0.01) / 100

	if mach == 0 {
		nav.Speed = Speed{}
		return speech.SpeedIntent{Type: speech.SpeedCancel}
	} else if mach < minMach {
		return speech.MakeUnableIntent("unable. Our minimum mach is {mach}", minMach)
	} else if mach > maxMach {
		return speech.MakeUnableIntent("unable. Our maximum mach is {mach}", maxMach)
	} else if !nav.machTransition() {
		return speech.MakeUnableIntent("unable. we haven't reached mach transition altitude")
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		sr := av.MakeMachRestriction(mach)
		nav.Speed = Speed{
			AfterAltitude:         &sr,
			AfterAltitudeAltitude: &alt,
		}
		return speech.SpeedIntent{Speed: mach, AfterAltitude: &alt, Type: speech.SpeedAssign, Mach: true}
	} else {
		sr := av.MakeMachRestriction(mach)
		nav.Speed = Speed{Assigned: &sr}
		// If there's an active altitude change and this is a significant speed change, defer the
		// altitude until after the Mach speed change completes.
		targetIAS := av.MachToIAS(mach, nav.FlightState.Altitude)
		if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude &&
			math.Abs(targetIAS-nav.FlightState.IAS) >= 20 {
			alt := *nav.Altitude.Assigned
			nav.Altitude = Altitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &targetIAS,
				RateAfterSpeed:  nav.Altitude.Rate,
			}
		}
		if mach < nav.Mach() {
			return speech.SpeedIntent{Speed: mach, Type: speech.SpeedReduce, Mach: true}
		} else if mach > nav.Mach() {
			return speech.SpeedIntent{Speed: mach, Type: speech.SpeedIncrease, Mach: true}
		} else {
			return speech.SpeedIntent{Speed: mach, Type: speech.SpeedAssign, Mach: true}
		}
	}
}

// checkAssignedSpeed returns an unable intent and false if the aircraft
// can't fly the given speed.
func (nav *Nav) checkAssignedSpeed(speed float32, temp av.Temperature) (speech.CommandIntent, bool) {
	maxIAS := 10 * math.Floor(nav.maxIAS(temp)/10)
	if speed < nav.Perf.Speed.Landing {
		return speech.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing), false
	} else if speed > maxIAS {
		return speech.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS), false
	}
	return nil, true
}

func (nav *Nav) AssignSpeed(sr *av.SpeedRestriction, afterAltitude bool, temp av.Temperature) speech.CommandIntent {
	nav.clearAfterFixSpeeds()

	if sr == nil {
		nav.Speed = Speed{}
		return speech.SpeedIntent{Type: speech.SpeedCancel}
	}

	// Determine the representative speed for validation and readback.
	speed, exact := sr.ExactValue()
	if !exact {
		speed = sr.Range[0]
		if speed == 0 {
			speed = sr.Range[1]
		}
	}

	if intent, ok := nav.checkAssignedSpeed(speed, temp); !ok {
		return intent
	}

	if !exact {
		// Range restriction: no afterAltitude deferral
		nav.Speed = Speed{Assigned: sr}
		if sr.Range[0] > 0 && sr.Range[1] == av.MaxRestrictionSpeed {
			return speech.SpeedIntent{Speed: sr.Range[0], Type: speech.SpeedAtOrAbove}
		}
		return speech.SpeedIntent{Speed: sr.Range[1], Type: speech.SpeedAtOrBelow}
	}

	if nav.Approach.Cleared {
		nav.Speed = Speed{Assigned: sr}
		dir := speech.SpeedAssign
		if speed < nav.FlightState.IAS {
			dir = speech.SpeedReduce
		} else if speed > nav.FlightState.IAS {
			dir = speech.SpeedIncrease
		}
		return speech.SpeedIntent{Speed: speed, Type: speech.SpeedUntilFinal, UntilFinalDirection: dir}
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		nav.Speed = Speed{
			AfterAltitude:         sr,
			AfterAltitudeAltitude: &alt,
		}
		return speech.SpeedIntent{Speed: speed, AfterAltitude: &alt, Type: speech.SpeedAssign}
	} else {
		// If there's an active altitude change and the speed change is significant (>20kt), defer
		// the altitude until after the speed change completes.
		speedDelta := math.Abs(speed - nav.FlightState.IAS)
		if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude &&
			speedDelta > 20 {
			alt := *nav.Altitude.Assigned
			nav.Altitude = Altitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &speed,
				RateAfterSpeed:  nav.Altitude.Rate,
			}
		}
		nav.Speed = Speed{Assigned: sr}
		if speed < nav.FlightState.IAS {
			return speech.SpeedIntent{Speed: speed, Type: speech.SpeedReduce}
		} else if speed > nav.FlightState.IAS {
			return speech.SpeedIntent{Speed: speed, Type: speech.SpeedIncrease}
		} else {
			return speech.SpeedIntent{Speed: speed, Type: speech.SpeedAssign}
		}
	}
}

func (nav *Nav) AssignSpeedUntil(sr *av.SpeedRestriction, until *speech.SpeedUntil, temp av.Temperature) speech.CommandIntent {
	nav.clearAfterFixSpeeds()

	speed, exact := sr.ExactValue()
	if !exact {
		speed = sr.Range[0]
		if speed == 0 {
			speed = sr.Range[1]
		}
	}

	if intent, ok := nav.checkAssignedSpeed(speed, temp); !ok {
		return intent
	}

	nav.Speed = Speed{Assigned: sr}
	if !exact {
		if sr.Range[0] > 0 && sr.Range[1] == av.MaxRestrictionSpeed {
			return speech.SpeedIntent{Speed: sr.Range[0], Type: speech.SpeedAtOrAbove, Until: until}
		}
		return speech.SpeedIntent{Speed: sr.Range[1], Type: speech.SpeedAtOrBelow, Until: until}
	}
	return speech.SpeedIntent{Speed: speed, Type: speech.SpeedUntilFinal, Until: until}
}

func (nav *Nav) MaintainSlowestPractical() speech.CommandIntent {
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{MaintainSlowestPractical: true}
	return speech.SpeedIntent{Type: speech.SpeedSlowestPractical}
}

func (nav *Nav) MaintainMaximumForward() speech.CommandIntent {
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{MaintainMaximumForward: true}
	return speech.SpeedIntent{Type: speech.SpeedMaximumForward}
}

func (nav *Nav) MaintainPresentSpeed() speech.CommandIntent {
	nav.clearAfterFixSpeeds()
	// Capture current indicated airspeed and assign it, rounded to nearest 10
	currentSpeed := nav.FlightState.IAS
	speed := float32(int((currentSpeed+5)/10) * 10)
	sr := av.MakeAtSpeedRestriction(speed)
	nav.Speed = Speed{Assigned: &sr}
	return speech.SpeedIntent{Speed: speed, Type: speech.SpeedPresentSpeed}
}

func (nav *Nav) SaySpeed(temp av.Temperature) speech.CommandIntent {
	if nav.machTransition() {
		return nav.SayMach()
	}
	return nav.SayIndicatedSpeed(temp)
}

func (nav *Nav) SayIndicatedSpeed(temp av.Temperature) speech.CommandIntent {
	currentSpeed := nav.FlightState.IAS
	intent := speech.ReportSpeedIntent{Current: currentSpeed}
	if sr := nav.Speed.Assigned; sr != nil && !sr.IsMach {
		if spd, exact := sr.ExactValue(); exact {
			intent.Assigned = &spd
		}
	} else if _, sr, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		naturalIAS, _ := nav.targetAltitudeIAS(temp)
		spd := nav.restrictedSpeed(sr, naturalIAS)
		intent.Assigned = &spd
	} else if nav.Speed.Restriction != nil {
		naturalIAS, _ := nav.targetAltitudeIAS(temp)
		spd := nav.restrictedSpeed(nav.Speed.Restriction, naturalIAS)
		intent.Assigned = &spd
	}
	return intent
}

func (nav *Nav) SayMach() speech.CommandIntent {
	if !nav.machTransition() {
		return speech.MakeUnableIntent("unable. we haven't reached mach transition altitude")
	}
	currentMach := nav.Mach()
	intent := speech.ReportMachIntent{Current: currentMach}
	if sr := nav.Speed.Assigned; sr != nil && sr.IsMach {
		if mach, exact := sr.ExactValue(); exact {
			intent.Assigned = &mach
		}
	}
	return intent
}

func (nav *Nav) SayHeading() speech.CommandIntent {
	currentHeading := nav.FlightState.Heading
	intent := speech.ReportHeadingIntent{Current: currentHeading}
	if nav.Heading.Assigned != nil {
		intent.Assigned = nav.Heading.Assigned
	}
	return intent
}

func (nav *Nav) SayAltitude() speech.CommandIntent {
	currentAltitude := nav.FlightState.Altitude
	intent := speech.ReportAltitudeIntent{Current: currentAltitude}
	if nav.Altitude.Assigned != nil {
		intent.Assigned = nav.Altitude.Assigned
		if *nav.Altitude.Assigned < currentAltitude {
			intent.Direction = speech.AltitudeDescend
		} else if *nav.Altitude.Assigned > currentAltitude {
			intent.Direction = speech.AltitudeClimb
		} else {
			intent.Direction = speech.AltitudeMaintain
		}
	}
	return intent
}

func (nav *Nav) ExpediteDescent() speech.CommandIntent {
	return nav.setRate(RateExpedite, nil, speech.AltitudeDescend)
}

func (nav *Nav) ExpediteClimb() speech.CommandIntent {
	return nav.setRate(RateExpedite, nil, speech.AltitudeClimb)
}

func (nav *Nav) ExpediteDescentThrough(throughAlt float32) speech.CommandIntent {
	return nav.setRate(RateExpedite, &throughAlt, speech.AltitudeDescend)
}

func (nav *Nav) ExpediteClimbThrough(throughAlt float32) speech.CommandIntent {
	return nav.setRate(RateExpedite, &throughAlt, speech.AltitudeClimb)
}

func (nav *Nav) GoodRateDescent() speech.CommandIntent {
	return nav.setRate(RateGood, nil, speech.AltitudeDescend)
}

func (nav *Nav) GoodRateClimb() speech.CommandIntent {
	return nav.setRate(RateGood, nil, speech.AltitudeClimb)
}

func (nav *Nav) GoodRateThrough(throughAlt float32) speech.CommandIntent {
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
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.RateAfterSpeed = rate
			return speech.AltitudeIntent{
				Direction:  direction,
				Altitude:   *nav.Altitude.AfterSpeed,
				AfterSpeed: nav.Altitude.AfterSpeedSpeed,
			}
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

func (nav *Nav) AssignHeading(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return speech.MakeUnableIntent("unable. {hdg} isn't a valid heading", hdg)
	}

	cancelHold := nav.Heading.Hold != nil
	nav.assignHeading(hdg, turn, simTime, delayReduction)

	intent := speech.HeadingIntent{
		Heading:    hdg,
		Type:       speech.HeadingAssign,
		CancelHold: cancelHold,
	}

	switch turn {
	case av.TurnClosest:
		intent.Turn = speech.HeadingTurnClosest
	case av.TurnRight:
		intent.Turn = speech.HeadingTurnToRight
	case av.TurnLeft:
		intent.Turn = speech.HeadingTurnToLeft
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}

	return intent
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
		// altitude the pilot was at when they turned. This is decided
		// before the approach clearance is cancelled below, since a cleared
		// approach's constraints are ones the aircraft is flying.
		if len(nav.Waypoints) > 0 && (nav.Waypoints[0].OnSTAR() || nav.Waypoints[0].OnApproach()) &&
			!nav.hasIssuedAltitude() {
			if _, ok := nav.findAltitudeTarget(); ok {
				nav.Approach.RequestAltitude = true
				snapshotAltitude = true
			}
		}

		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(hdg, turn, approachCleared, simTime, delayReduction)
	if snapshotAltitude && nav.DeferredNavHeading != nil {
		nav.DeferredNavHeading.SnapshotAltitudeOnEffect = true
	}
}

func (nav *Nav) FlyPresentHeading(simTime Time, delayReduction time.Duration) speech.CommandIntent {
	nav.assignHeading(nav.FlightState.Heading, av.TurnClosest, simTime, delayReduction)
	return speech.HeadingIntent{
		Heading: nav.FlightState.Heading,
		Type:    speech.HeadingPresent,
	}
}

func (nav *Nav) fixInRoute(fix string) bool {
	if slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return fix == wp.Fix }) {
		return true
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

func (nav *Nav) fixPairInRoute(fixa, fixb string) (fa *av.Waypoint, fb *av.Waypoint) {
	find := func(f string, wp []av.Waypoint) int {
		return slices.IndexFunc(wp, func(wp av.Waypoint) bool { return wp.Fix == f })
	}

	var apWaypoints []av.WaypointArray
	if nav.Approach.Assigned != nil {
		apWaypoints = nav.Approach.Assigned.Waypoints
	}

	wps := nav.AssignedWaypoints()
	if ia := find(fixa, wps); ia != -1 {
		// First fix is in the current route
		fa = &wps[ia]
		if ib := find(fixb, wps[ia:]); ib != -1 {
			// As is the second, and after the first
			fb = &wps[ia+ib]
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

type waypointSource int

const (
	waypointSourceRoute    waypointSource = iota // fix found on the STAR/route
	waypointSourceApproach                       // fix found on the assigned approach
	waypointSourceOther                          // global fix lookup
)

func (nav *Nav) directFixWaypoints(fix string) ([]av.Waypoint, waypointSource, error) {
	// Check the route first; when a fix exists on both the STAR/route
	// and the approach, we want the route waypoints so the aircraft
	// doesn't start flying the approach without being cleared.
	routeWps := nav.AssignedWaypoints()
	for i, wp := range routeWps {
		if fix == wp.Fix {
			return routeWps[i:], waypointSourceRoute, nil
		}
	}

	// Check the approach (if any).
	if route, idx := approachRouteThrough(nav.Approach.Assigned, fix); route != nil {
		return slices.Concat(route[idx:].Clone(), av.WaypointArray{nav.FlightState.ArrivalAirport}),
			waypointSourceApproach, nil
	}

	// See if it's a random fix not in the flight plan. It may name an
	// airport by either of its ids.
	p, ok := func() (math.Point2LL, bool) {
		if p, ok := db.DB.LookupWaypoint(fix); ok {
			return p, true
		} else if ap, ok := db.DB.LookupICAOAirport(av.ICAOAirportCode(fix)); ok {
			return ap.Location, true
		} else if ap, ok := db.DB.LookupFAAAirport(av.FAAAirportCode(fix)); ok {
			return ap.Location, true
		}
		return math.Point2LL{}, false
	}()
	if ok {
		// Ignore ones that are >150nm away under the assumption that it's
		// a typo in that case.
		if math.NMDistance2LL(p, nav.FlightState.Position) > 150 {
			return nil, waypointSourceOther, ErrFixIsTooFarAway
		}

		return []av.Waypoint{
			{
				Fix:      fix,
				Location: p,
			},
			nav.FlightState.ArrivalAirport,
		}, waypointSourceOther, nil
	}

	return nil, waypointSourceOther, ErrInvalidFix
}

func (nav *Nav) ExpectDirect(fix string) speech.CommandIntent {
	if _, ok := db.DB.LookupWaypoint(fix); !ok && !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
	nav.ExpectedDirectFix = fix
	return nil
}

func (nav *Nav) DirectFix(fix string, turn av.TurnDirection, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	if wps, source, err := nav.directFixWaypoints(fix); err == nil {
		if hold := nav.Heading.Hold; hold != nil {
			// We'll finish our lap and then depart the holding fix direct to the fix
			hold.Cancel = true
			nfa := FixAssignment{}
			nfa.Depart.Fix = &wps[0]
			if turn != av.TurnClosest {
				nfa.Depart.Turn = &turn
			}
			nav.FixAssignments[hold.Hold.Fix] = nfa
			if source == waypointSourceApproach && !nav.Approach.Cleared {
				nav.Approach.InterceptState = OnApproachCourse
			}
			if !nav.Approach.Cleared {
				nav.Approach.InterceptedReference = nav.visualReferenceForFix(fix)
			}
			return speech.NavigationIntent{
				Type:      speech.NavDirectFixFromHold,
				Fix:       hold.Hold.Fix,
				SecondFix: fix,
				Turn:      turn,
			}
		} else {
			nav.EnqueueDirectFix(wps, turn, simTime, delayReduction)
			nav.Approach.NoPT = false
			if source == waypointSourceApproach && !nav.Approach.Cleared {
				// The waypoints came from the approach but the aircraft
				// hasn't been cleared; track the approach course laterally.
				// Its restrictions don't apply until the clearance.
				nav.Approach.InterceptState = OnApproachCourse
			} else {
				nav.Approach.InterceptState = NotIntercepting
			}
			if !nav.Approach.Cleared {
				nav.Approach.InterceptedReference = nav.visualReferenceForFix(fix)
			}
			return speech.NavigationIntent{
				Type: speech.NavDirectFix,
				Fix:  fix,
				Turn: turn,
			}
		}
	} else if err == ErrFixIsTooFarAway {
		return speech.MakeUnableIntent("unable. {fix} is too far away to go direct", fix)
	} else {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
}

// radialVariation returns the variation a radial of the named fix is
// referenced to: a VHF navaid's station declination, or the area's
// variation for a fix without one.
func (nav *Nav) radialVariation(fix string) float32 {
	if d, ok := db.DB.Declination(fix); ok {
		return d
	}
	return nav.FlightState.MagneticVariation
}

// reachesRadial reports whether an aircraft flying the given heading from its
// current position will reach the given radial, referenced to variation, of
// a fix. Both are rays: the aircraft only flies forward, and a radial extends
// outward from its fix in one direction only, so crossing the reciprocal
// radial doesn't count.
func (nav *Nav) reachesRadial(hdg, radial math.MagneticHeading, fix math.Point2LL, variation float32) bool {
	nmPerLongitude := nav.FlightState.NmPerLongitude
	p := math.LL2NM(nav.FlightState.Position, nmPerLongitude)
	dir := math.HeadingVector(math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation))
	f := math.LL2NM(fix, nmPerLongitude)
	radialDir := math.HeadingVector(math.MagneticToTrue(radial, variation))
	// directFixWaypoints rejects fixes more than 150nm away, so the radial
	// only has to extend far enough to cover intercepts out at that range.
	_, _, _, ok := math.RaySegmentIntersect(p, dir, f, math.Add2f(f, math.Scale2f(radialDir, 500)))
	return ok
}

// InterceptRadial has the aircraft fly its assigned heading, or its present
// heading if none has been assigned, until it intercepts the given radial of
// fix. Flown inbound it then proceeds direct to the fix and continues along
// the route from there; flown outbound it tracks the radial away from the fix
// until the controller says otherwise.
func (nav *Nav) InterceptRadial(fix string, radial math.MagneticHeading, outbound bool, simTime Time,
	delayReduction time.Duration) speech.CommandIntent {
	if radial <= 0 || radial > 360 {
		return speech.MakeUnableIntent("unable. {hdg} isn't a valid radial", radial)
	}

	wps, _, err := nav.directFixWaypoints(fix)
	if err == ErrFixIsTooFarAway {
		return speech.MakeUnableIntent("unable. {fix} is too far away", fix)
	} else if err != nil {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}

	// A radial extends outward from the fix, so flying it inbound means
	// flying its reciprocal. The radial is referenced to its navaid's
	// station declination; the intercept and the track along it are flown
	// in the sim's magnetic frame.
	variation := nav.radialVariation(fix)
	course := math.TrueToMagnetic(math.MagneticToTrue(radial, variation), nav.FlightState.MagneticVariation)
	if !outbound {
		course = math.OppositeHeading(course)
	}

	hdg, turn := nav.FlightState.Heading, av.TurnClosest
	if dh := nav.DeferredNavHeading; dh != nil && dh.Heading != nil {
		hdg = *dh.Heading
		if dh.Turn != nil {
			turn = *dh.Turn
		}
	} else if nav.Heading.Assigned != nil {
		hdg = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
	}
	if !nav.reachesRadial(hdg, radial, wps[0].Location, variation) {
		return speech.MakeUnableIntent("unable to intercept the {fix} {hdg} radial", fix, radial)
	}

	// The turn onto the course is always the short way around, regardless of
	// which way the aircraft turned to take up the assigned heading.
	intercept := flyHeadingUntilIntercept(hdg, turn, wps[0].Location, course)
	intercept.Until.InterceptTurn = av.TurnClosest
	intercept.Until.InterceptFix, intercept.Until.InterceptOutbound = fix, outbound
	maneuvers := []LateralManeuver{intercept}
	if outbound {
		// There is nothing to go direct to once established, so hold the
		// radial as a ground track.
		maneuvers = append(maneuvers, LateralManeuver{
			Track: course,
			Until: ManeuverComplete{Type: UntilControllerIntervention},
		})
	}

	// Assign the heading for its approach and altitude side effects and its
	// pilot reaction delay; the maneuvers take effect along with it.
	nav.assignHeading(hdg, turn, simTime, delayReduction)
	nav.Approach.InterceptState = NotIntercepting
	dh := nav.DeferredNavHeading
	dh.Maneuvers = maneuvers
	if !outbound {
		dh.Waypoints = wps
	}

	return speech.NavigationIntent{
		Type:     speech.NavInterceptRadial,
		Fix:      fix,
		Radial:   radial,
		Outbound: outbound,
	}
}

func (nav *Nav) HoldAtFix(callsign string, fix string, hold *av.Hold) speech.CommandIntent {
	if _, ok := db.DB.LookupWaypoint(fix); !ok {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	} else if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	// Use controller-specified hold if provided, otherwise look up published hold
	var h av.Hold
	if hold != nil {
		// Controller-specified hold
		h = *hold
	} else {
		// Published hold
		holds, ok := db.DB.EnrouteHolds[fix]
		if !ok || len(holds) == 0 {
			return speech.MakeUnableIntent("unable. no published hold at {fix}", fix)
		}
		h = holds[0]
	}

	if len(nav.Waypoints) > 0 && nav.Waypoints[0].Fix == h.Fix && nav.Heading.Assigned == nil {
		// We're already direct to it for the next fix; get started.
		nav.Heading = Heading{Hold: nav.makeFlyHold(callsign, h)}
	} else if nav.hasDeferredRoute() && nav.DeferredNavHeading.Waypoints[0].Fix == h.Fix {
		nav.DeferredNavHeading.Hold = nav.makeFlyHold(callsign, h)
	} else {
		// It's a later fix. Queue it up; we'll return to it when it's the next waypoint upcoming.
		// Clobber any altitude or heading assignments associated with the fix.
		nav.FixAssignments[h.Fix] = FixAssignment{Hold: &h}
	}

	// These seem prudent to clear at this point.
	nav.Approach.Cleared = false
	nav.Approach.PassedApproachFix = false

	turnDir := util.Select(h.TurnDirection == av.TurnRight, "right", "left")
	var legLength string
	if h.LegLengthNM > 0 {
		legLength = fmt.Sprintf("%g mile", h.LegLengthNM)
	} else {
		legLength = fmt.Sprintf("%g minute", h.LegMinutes)
	}

	return speech.NavigationIntent{
		Type:          speech.NavHold,
		Fix:           h.Fix,
		HoldDirection: turnDir,
		HoldLegLength: legLength,
	}
}

func (nav *Nav) makeFlyHold(callsign string, hold av.Hold) *FlyHold {
	// Calculate heading from aircraft's current position to fix
	pHold, _ := db.DB.LookupWaypoint(hold.Fix)
	hdg := math.TrueToMagnetic(math.Heading2LL(nav.FlightState.Position, pHold, nav.FlightState.NmPerLongitude),
		nav.FlightState.MagneticVariation)

	NavLog(callsign, Time{}, NavLogHold, "makeFlyHold: headingToFix=%.1f hold_inbound=%.1f turn=%s -> %s",
		hdg, hold.InboundCourse, hold.TurnDirection, hold.Entry(hdg).String())

	fh := &FlyHold{
		Hold:        hold,
		FixLocation: pHold,
		Entry:       hold.Entry(hdg),
	}
	fh.Maneuvers = fh.entryManeuvers()
	return fh
}

func (nav *Nav) DepartFixDirect(fixa string, fixb string) speech.CommandIntent {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return speech.NavigationIntent{
		Type:      speech.NavDepartFixDirect,
		Fix:       fixa,
		SecondFix: fixb,
	}
}

func (nav *Nav) DepartFixHeading(fix string, hdg math.MagneticHeading) speech.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return speech.MakeUnableIntent("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	nfa := nav.FixAssignments[fix]
	h := hdg
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return speech.NavigationIntent{
		Type:    speech.NavDepartFixHeading,
		Fix:     fix,
		Heading: hdg,
	}
}

func (nav *Nav) CrossFixAt(fix string, ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	intent := speech.NavigationIntent{
		Type: speech.NavCrossFixAt,
		Fix:  fix,
	}

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		intent.AltRestriction = ar
		// Delete other altitude restrictions
		nav.Altitude = Altitude{}
	}
	if sr != nil {
		nfa.Arrive.Speed = sr
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS(temp)
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		// Delete other speed restrictions
		nav.Speed = Speed{}
	}
	nav.FixAssignments[fix] = nfa

	return intent
}

// clearMatchingSyntheticRestrictions walks wps and, for each waypoint where
// match(wp) returns true, clears altitude restrictions if ar != nil and speed
// restrictions if sr != nil. Waypoints left with no inline restrictions are
// removed entirely. Returns the (possibly shorter) slice; callers must
// refresh any indices into the route after calling.
func clearMatchingSyntheticRestrictions(wps []av.Waypoint, match func(*av.Waypoint) bool,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction) []av.Waypoint {
	for i := 0; i < len(wps); {
		wp := &wps[i]
		if match(wp) {
			if ar != nil {
				wp.ClearAltitudeRestriction()
			}
			if sr != nil {
				wp.ClearSpeedRestriction()
			}
			if !wp.HasAltitudeRestriction() && !wp.HasSpeedRestriction() {
				wps = slices.Delete(wps, i, i+1)
				continue
			}
		}
		i++
	}
	return wps
}

// newSyntheticWaypoint builds a synthetic crossing waypoint at the given
// location. If inheritFrom is non-nil, OnSID/OnSTAR/OnApproach flags are
// copied from it so the waypoint participates in the same route phase as its
// neighbor.
func newSyntheticWaypoint(name string, loc math.Point2LL, inheritFrom *av.Waypoint) av.Waypoint {
	wp := av.Waypoint{Fix: name, Location: loc}
	wp.SetSyntheticCrossing(true)
	if inheritFrom != nil {
		wp.SetOnSID(inheritFrom.OnSID())
		wp.SetOnSTAR(inheritFrom.OnSTAR())
		wp.SetOnApproach(inheritFrom.OnApproach())
	}
	return wp
}

// applyRestrictionsToSyntheticWaypoint sets the supplied altitude and speed
// restrictions on wp (when non-nil), records them on intent, and clears the
// corresponding nav.Altitude / nav.Speed assignments so the synthetic
// crossing supersedes any prior controller instruction of the same type.
func (nav *Nav) applyRestrictionsToSyntheticWaypoint(wp *av.Waypoint,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature, intent *speech.NavigationIntent) {
	if ar != nil {
		wp.SetAltitudeRestriction(*ar)
		intent.AltRestriction = ar
		nav.Altitude = Altitude{}
	}

	if sr != nil {
		wp.SetSpeedRestriction(*sr)
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS(temp)
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		nav.Speed = Speed{}
	}
}

func (nav *Nav) CrossDistanceFromFixAt(fix string, dist float32, dir math.CardinalOrdinalDirection,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	routeWps, commitRoute := nav.editAssignedWaypoints()

	wps := routeWps
	idx := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if idx == -1 {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	fixLoc := wps[idx].Location

	// Find the "real" prior waypoint (skip synthetic ones) to determine the segment.
	realPriorIdx := idx - 1
	for realPriorIdx >= 0 && wps[realPriorIdx].SyntheticCrossing() {
		realPriorIdx--
	}

	var priorLoc math.Point2LL
	var priorName string
	if realPriorIdx >= 0 {
		priorLoc = wps[realPriorIdx].Location
		priorName = wps[realPriorIdx].Fix
	} else {
		priorLoc = nav.FlightState.Position
	}

	// Direction validation uses the inbound magnetic course to the fix, since
	// the controller-issued direction is spoken relative to the magnetic compass.
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, nav.FlightState.NmPerLongitude),
		nav.FlightState.MagneticVariation,
	)
	if math.HeadingDifference(float32(approachHeading), dir.Heading()) > 45 {
		actualDir := math.Compass(approachHeading)
		return speech.MakeUnableIntent("unable. We're approaching {fix} from the "+actualDir, fix)
	}

	// Distance validation against the real segment.
	segLen := math.NMDistance2LL(priorLoc, fixLoc)
	if realPriorIdx >= 0 && dist >= segLen {
		return speech.MakeUnableIntent("unable. That's before {fix}", priorName)
	}
	if realPriorIdx < 0 && dist >= math.NMDistance2LL(nav.FlightState.Position, fixLoc) {
		return speech.MakeUnableIntent("unable. We're already closer to {fix}", fix)
	}

	// Compute synthetic waypoint via linear interpolation.
	t := 1 - dist/segLen
	syntheticLoc := math.Point2LL{
		math.Lerp(t, priorLoc[0], fixLoc[0]),
		math.Lerp(t, priorLoc[1], fixLoc[1]),
	}

	// 1. Remove or clear existing synthetic waypoints for this fix and restriction types.
	removePrefix := "_" + fix + "/"
	routeWps = clearMatchingSyntheticRestrictions(routeWps,
		func(wp *av.Waypoint) bool { return strings.HasPrefix(wp.Fix, removePrefix) },
		ar, sr)
	commitRoute(routeWps)

	// 2. Refresh index as waypoints might have shifted.
	wps = routeWps
	idx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })

	intent := speech.NavigationIntent{
		Type:      speech.NavCrossDistanceFromFixAt,
		Fix:       fix,
		Distance:  dist,
		Direction: dir,
	}

	// Helper to insert a synthetic waypoint in the correct order (descending distance from fix).
	insertOrdered := func(name string, loc math.Point2LL, d float32) {
		// Find the insertion index among synthetic waypoints for this fix.
		insertIdx := idx
		for insertIdx > 0 && strings.HasPrefix(wps[insertIdx-1].Fix, removePrefix) {
			if _, otherDist, _, ok := av.ParseSyntheticCrossingFix(wps[insertIdx-1].Fix); ok {
				if float32(otherDist) >= d {
					break // Current one is closer to fix than the one we are checking (or same distance).
				}
			}
			insertIdx--
		}

		// Inherit SID/STAR/Approach flags from the named target fix (wps[idx]),
		// not the insertion neighbor.
		var inherit *av.Waypoint
		if idx < len(wps) {
			inherit = &wps[idx]
		}
		routeWps = slices.Insert(routeWps, insertIdx, newSyntheticWaypoint(name, loc, inherit))
		commitRoute(routeWps)
		// Refresh wps and idx for subsequent operations.
		wps = routeWps
		idx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	}

	name := fmt.Sprintf("_%s/%d%s", fix, int(dist), dir.ShortString())
	wpIdx := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == name })
	if wpIdx == -1 {
		insertOrdered(name, syntheticLoc, dist)
		wps = routeWps
		wpIdx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == name })
	}
	wp := &routeWps[wpIdx]
	wp.Location = syntheticLoc

	// 3. Apply new inline restrictions to the synthetic waypoint.
	nav.applyRestrictionsToSyntheticWaypoint(wp, ar, sr, temp, &intent)
	commitRoute(routeWps)

	return intent
}

// CrossDMEAt inserts a synthetic crossing restriction at a given DME from
// the runway threshold of the currently cleared visual approach. The
// synthetic waypoint is placed along the approach route by walking backwards
// from the threshold accumulating track miles; if dist exceeds the total
// route length, the point is extrapolated backwards along the first leg.
func (nav *Nav) CrossDMEAt(dist float32, ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	if dist <= 0 || dist > 30 {
		return speech.MakeUnableIntent("unable, that distance is out of range")
	}

	if !nav.clearedForVisualApproach() {
		return speech.MakeUnableIntent("unable, we're not cleared for a visual approach")
	}
	ap := nav.Approach.Assigned
	runway := ap.Runway

	routeWps, commitRoute := nav.editAssignedWaypoints()

	if len(routeWps) < 2 {
		return speech.MakeUnableIntent("unable")
	}

	// 1. Clear matching restriction categories on any existing synthetic DME
	// waypoints for this runway; drop any that end up with no restrictions.
	routeWps = clearMatchingSyntheticRestrictions(routeWps, func(wp *av.Waypoint) bool {
		wpRunway, _, ok := av.ParseSyntheticDMEFix(wp.Fix)
		return ok && wpRunway == runway
	}, ar, sr)
	commitRoute(routeWps)

	// Deletion above can leave the threshold alone (e.g., the aircraft has
	// passed all prior visual waypoints and the only remaining synthetic
	// was just removed); without at least two waypoints we have no route to
	// measure along.
	if len(routeWps) < 2 {
		return speech.MakeUnableIntent("unable")
	}

	// 2. Walk backward from the threshold, accumulating track miles, and
	// place the synthetic waypoint on the first leg that spans `dist`. If
	// `dist` exceeds the full route length, extrapolate past wp[0] along
	// the direction from wp[1] back to wp[0]. The final waypoint is the
	// arrival airport (appended after the threshold); skip the
	// threshold-to-airport leg so distances are measured from the threshold.
	thresholdIdx := len(routeWps) - 2
	if thresholdIdx < 1 {
		return speech.MakeUnableIntent("unable")
	}
	nmPerLong := nav.FlightState.NmPerLongitude
	var syntheticLoc math.Point2LL
	var insertIdx int
	var cum float32 // cumulative track miles from threshold to routeWps[i+1]
	placed := false
	for i := thresholdIdx - 1; i >= 0 && !placed; i-- {
		legLen := math.NMDistance2LLFast(routeWps[i].Location, routeWps[i+1].Location, nmPerLong)
		if dist <= cum+legLen {
			t := (dist - cum) / legLen
			syntheticLoc = math.Point2LL(math.Lerp2f(t, routeWps[i+1].Location, routeWps[i].Location))
			insertIdx = i + 1
			placed = true
		}
		cum += legLen
	}
	if !placed {
		wp0NM := math.LL2NM(routeWps[0].Location, nmPerLong)
		wp1NM := math.LL2NM(routeWps[1].Location, nmPerLong)
		dirBack := math.Normalize2f(math.Sub2f(wp0NM, wp1NM))
		syntheticLoc = math.NM2LL(math.Add2f(wp0NM, math.Scale2f(dirBack, dist-cum)), nmPerLong)
		insertIdx = 0
	}

	// 4. Apply restrictions to a new or existing synthetic DME waypoint.
	name := fmt.Sprintf("_%s_%dDME", runway, int(dist))
	existingIdx := slices.IndexFunc(routeWps, func(wp av.Waypoint) bool { return wp.Fix == name })
	var wp *av.Waypoint
	if existingIdx >= 0 {
		wp = &routeWps[existingIdx]
		wp.Location = syntheticLoc
	} else {
		// Inherit SID/STAR/Approach flags from the insertion neighbor — the
		// waypoint that will sit immediately after the new one.
		var inherit *av.Waypoint
		if insertIdx < len(routeWps) {
			inherit = &routeWps[insertIdx]
		}
		routeWps = slices.Insert(routeWps, insertIdx, newSyntheticWaypoint(name, syntheticLoc, inherit))
		wp = &routeWps[insertIdx]
	}

	intent := speech.NavigationIntent{
		Type:     speech.NavCrossDME,
		Fix:      name,
		Distance: dist,
	}

	nav.applyRestrictionsToSyntheticWaypoint(wp, ar, sr, temp, &intent)
	commitRoute(routeWps)

	return intent
}

func (nav *Nav) AfterFixSpeed(fix string, sr *av.SpeedRestriction) speech.CommandIntent {
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	speed, exact := sr.ExactValue()
	if !exact {
		speed = sr.Range[0]
		if speed == 0 {
			speed = sr.Range[1]
		}
	}

	nfa := nav.FixAssignments[fix]
	nfa.Depart.Speed = sr
	nav.FixAssignments[fix] = nfa

	var stype speech.SpeedType
	if !exact {
		if sr.Range[0] > 0 {
			stype = speech.SpeedAtOrAbove
		} else {
			stype = speech.SpeedAtOrBelow
		}
	} else if speed < nav.FlightState.IAS {
		stype = speech.SpeedReduce
	} else if speed > nav.FlightState.IAS {
		stype = speech.SpeedIncrease
	} else {
		stype = speech.SpeedAssign
	}

	return speech.SpeedIntent{Speed: speed, Type: stype, AfterFix: fix}
}

func (nav *Nav) AssignCompoundSpeed(segments []speech.CompoundSpeedSegment, temp av.Temperature) speech.CommandIntent {
	// Validate all segments before applying any state changes.
	for _, seg := range segments {
		speed, exact := seg.Speed.ExactValue()
		if !exact {
			speed = seg.Speed.Range[0]
			if speed == 0 {
				speed = seg.Speed.Range[1]
			}
		}
		if intent, ok := nav.checkAssignedSpeed(speed, temp); !ok {
			return intent
		}

		if seg.UntilFix != "" && !nav.fixInRoute(seg.UntilFix) {
			return speech.MakeUnableIntent("unable. {fix} isn't in our route", seg.UntilFix)
		}
	}

	// Apply: first segment sets the current speed, subsequent segments
	// set after-fix speed assignments.
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{Assigned: segments[0].Speed}

	for i := 1; i < len(segments); i++ {
		fix := segments[i-1].UntilFix
		nfa := nav.FixAssignments[fix]
		nfa.Depart.Speed = segments[i].Speed
		nav.FixAssignments[fix] = nfa
	}

	// If the last segment has an UntilFix (no trailing open-ended speed),
	// cancel speed restrictions when the aircraft passes that fix.
	if last := segments[len(segments)-1]; last.UntilFix != "" {
		nfa := nav.FixAssignments[last.UntilFix]
		nfa.Depart.CancelSpeed = true
		nav.FixAssignments[last.UntilFix] = nfa
	}

	return speech.CompoundSpeedIntent{Segments: segments}
}

func (nav *Nav) clearAfterFixSpeeds() {
	for fix, nfa := range nav.FixAssignments {
		if nfa.Depart.Speed != nil || nfa.Depart.CancelSpeed {
			nfa.Depart.Speed = nil
			nfa.Depart.CancelSpeed = false
			nav.FixAssignments[fix] = nfa
		}
	}
}

func (nav *Nav) AfterFixAltitude(fix string, alt float32) speech.CommandIntent {
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}
	if alt > nav.Perf.Ceiling {
		return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}

	nfa := nav.FixAssignments[fix]
	nfa.Depart.Altitude = &alt
	nav.FixAssignments[fix] = nfa

	var direction speech.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = speech.AltitudeClimb
	} else if alt < nav.FlightState.Altitude {
		direction = speech.AltitudeDescend
	} else {
		direction = speech.AltitudeMaintain
	}

	return speech.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
		AfterFix:  fix,
	}
}

func (nav *Nav) clearFixAltitudes() {
	for fix, nfa := range nav.FixAssignments {
		if nfa.Depart.Altitude != nil {
			nfa.Depart.Altitude = nil
			nav.FixAssignments[fix] = nfa
		}
	}
}

func (nav *Nav) CancelApproachClearance() speech.CommandIntent {
	if !nav.Approach.Cleared {
		return speech.MakeUnableIntent("unable. we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false
	nav.Approach.ApproachClearanceCancelled = true

	return speech.ApproachIntent{Type: speech.ApproachCancel}
}

// ClimbViaSID is "climb via SID", with "except maintain exceptAlt" if it is
// non-nil. Otherwise the aircraft climbs to its cruise altitude: the
// scenario's cleared altitude for a departure is an earlier controller's
// "except maintain", and cruise is the only top altitude it gives a SID.
func (nav *Nav) ClimbViaSID(exceptAlt *float32, simTime Time) speech.CommandIntent {
	if !nav.ClimbViaSIDAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not flying a departure procedure")
	}

	nav.EnqueueOnCourse(simTime)
	return speech.ProcedureIntent{Type: speech.ProcedureClimbViaSID, ExceptAltitude: exceptAlt}
}

// DescendViaSTAR is "descend via STAR", with "except maintain exceptAlt" if
// it is non-nil; otherwise the aircraft descends to the STAR's last
// restriction.
func (nav *Nav) DescendViaSTAR(exceptAlt *float32, simTime Time) speech.CommandIntent {
	if !nav.DescendViaSTARAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not on a STAR")
	}

	nav.EnqueueOnCourse(simTime)
	return speech.ProcedureIntent{Type: speech.ProcedureDescendViaSTAR, ExceptAltitude: exceptAlt}
}

// ClimbViaSIDAtPassedFix carries out a /cvs or /cv route action at the fix
// the aircraft just passed. The aircraft is already flying its route, so
// unlike ClimbViaSID it isn't put back on course. It returns false,
// changing nothing, if no fix ahead is on a SID. An excepted altitude below
// the aircraft is descended to, as instructed.
func (nav *Nav) ClimbViaSIDAtPassedFix(exceptAlt *float32) bool {
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSID) {
		return false
	}

	ceiling := nav.FinalAltitude
	if exceptAlt != nil {
		ceiling = *exceptAlt
	}
	nav.flyProcedureRestrictions(&ClearedAltitude{Altitude: ceiling})
	return true
}

// DescendViaSTARAtPassedFix is the /dvs and /dv counterpart of
// ClimbViaSIDAtPassedFix; an excepted altitude above the aircraft is climbed
// back to.
func (nav *Nav) DescendViaSTARAtPassedFix(exceptAlt *float32) bool {
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSTAR) {
		return false
	}

	var floor *ClearedAltitude
	if exceptAlt != nil {
		floor = &ClearedAltitude{Altitude: *exceptAlt, IsFloor: true}
	}
	nav.flyProcedureRestrictions(floor)
	return true
}

// flyProcedureRestrictions cancels the assigned altitude and speed so that
// the restrictions of the procedure ahead govern, limited by cleared if it
// is non-nil. A published speed restriction the aircraft is already holding
// stays in effect: a via clearance cancels only assigned speeds (7110.65
// 5-7-1).
func (nav *Nav) flyProcedureRestrictions(cleared *ClearedAltitude) {
	nav.Altitude = Altitude{Cleared: cleared}
	nav.Speed = Speed{Restriction: nav.Speed.Restriction}
	nav.clearAfterFixSpeeds()
}

func (nav *Nav) DistanceAlongRoute(fix string) (float32, error) {
	if nav.Heading.Assigned != nil {
		return 0, ErrNotFlyingRoute
	}
	if len(nav.Waypoints) == 0 {
		return 0, nil
	}
	index := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if index == -1 {
		return 0, ErrFixNotInRoute
	}
	wp := nav.Waypoints[:index+1]
	distance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
	for i := 0; i < len(wp)-1; i++ {
		distance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
	}
	return distance, nil
}

func (nav *Nav) ResumeOwnNavigation() speech.CommandIntent {
	if nav.Heading.Assigned == nil {
		// This is a weird response but keeping the original behavior
		return speech.MakeUnableIntent("unable. I don't think you ever put us on a heading...")
	}

	nav.Heading = Heading{}
	nav.Waypoints = nav.AssignedWaypoints() // just take any deferred ones immediately.
	nav.DeferredNavHeading = nil

	if len(nav.Waypoints) > 1 {
		// Find the route segment we're closest to then go direct to the
		// end of it.  In some cases for the first segment maybe it's
		// preferable to go to the first fix but it's a little unclear what
		// the criteria should be.
		minDist := float32(1000000)
		startIdx := 0
		pac := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
		for i := 0; i < len(nav.Waypoints)-1; i++ {
			wp0, wp1 := nav.Waypoints[i], nav.Waypoints[i+1]
			p0 := math.LL2NM(wp0.Location, nav.FlightState.NmPerLongitude)
			p1 := math.LL2NM(wp1.Location, nav.FlightState.NmPerLongitude)
			if d := math.PointSegmentDistance(pac, p0, p1); d < minDist {
				minDist = d
				startIdx = i + 1
			}
		}
		nav.Waypoints = nav.Waypoints[startIdx:]
	}
	return speech.NavigationIntent{Type: speech.NavResumeOwnNav}
}

func (nav *Nav) AltitudeOurDiscretion() speech.CommandIntent {
	if nav.Altitude.Assigned == nil {
		return speech.MakeUnableIntent("unable. You never assigned us an altitude...")
	}

	nav.Altitude = Altitude{}
	if alt := nav.FinalAltitude; alt > nav.FlightState.Altitude {
		nav.Altitude.Cleared = &ClearedAltitude{Altitude: alt}
	}

	return speech.NavigationIntent{Type: speech.NavAltitudeDiscretion}
}

// hasIssuedAltitude reports whether the aircraft has an altitude to fly
// besides its route's constraints: one assigned or cleared by a controller,
// a waypoint action, or its scenario, which it keeps flying when vectored off
// its route, or one it is already holding after an earlier vector.
// AfterSpeed counts: the altitude is assigned, just deferred until the speed
// change completes.
func (nav *Nav) hasIssuedAltitude() bool {
	return nav.Altitude.Assigned != nil || nav.Altitude.AfterSpeed != nil || nav.Altitude.Cleared != nil
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}

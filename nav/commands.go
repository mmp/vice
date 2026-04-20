// nav/commands.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// GoAroundWithProcedure initiates a go-around with a defined procedure.
// The runwayEndWP waypoint should have Location (opposite threshold), FlyOver,
// Heading (outbound), AltitudeRestriction, and GoAroundContactController set.
func (nav *Nav) GoAroundWithProcedure(altitude float32, runwayEndWP av.Waypoint) {
	nav.DeferredNavHeading = nil
	nav.Speed = NavSpeed{}
	nav.Approach = NavApproach{}
	nav.setAssignedAltitude(altitude)
	nav.Waypoints = av.WaypointArray{runwayEndWP, nav.FlightState.ArrivalAirport}
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool, simTime Time, delayReduction time.Duration) av.CommandIntent {
	nav.clearFixAltitudes()
	intent, ok := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if !ok {
		return intent
	}
	// A new controller-issued altitude supersedes any pending "report
	// reaching" target.
	nav.ReportReachingAltitude = nil
	nav.enqueueAssignedAltitude(alt, simTime, delayReduction)
	return intent
}

func (nav *Nav) prepareAltitudeAssignment(alt float32, afterSpeed bool) (av.CommandIntent, bool) {
	if alt > nav.Perf.Ceiling {
		return av.MakeUnableIntent("unable. That altitude is above our ceiling."), false
	}

	var direction av.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = av.AltitudeClimb
	} else if alt == nav.FlightState.Altitude {
		direction = av.AltitudeMaintain
	} else {
		direction = av.AltitudeDescend
	}

	intent := av.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
	}

	if sr := nav.Speed.Assigned; afterSpeed && sr != nil {
		if spd, exact := sr.ExactValue(); exact && spd != nav.FlightState.IAS {
			nav.Altitude = NavAltitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &spd,
			}
			intent.AfterSpeed = &spd
			return intent, false
		}
	}

	// If there's an exact speed change in progress (>=20kt remaining or any Mach change),
	// defer the speed assignment until after the altitude change completes.
	if sr := nav.Speed.Assigned; sr != nil {
		if spd, exact := sr.ExactValue(); exact &&
			(sr.IsMach || math.Abs(spd-nav.FlightState.IAS) >= 20) {
			srCopy := *sr
			nav.Speed = NavSpeed{
				AfterAltitude:         &srCopy,
				AfterAltitudeAltitude: &alt,
			}
		}
	}

	return intent, true
}

func (nav *Nav) assignAltitudeNow(alt float32, afterSpeed bool) av.CommandIntent {
	intent, ok := nav.prepareAltitudeAssignment(alt, afterSpeed)
	if ok {
		nav.setAssignedAltitude(alt)
	}
	return intent
}

func (nav *Nav) setAssignedAltitude(alt float32) {
	nav.Altitude = NavAltitude{
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
	nav.Altitude = NavAltitude{
		Assigned:       &alt,
		ActiveAssigned: active,
		ActivateAt:     simTime.Add(d),
	}
}

func (nav *Nav) enqueueAltitudeAfterSpeed(simTime Time) {
	alt := *nav.Altitude.AfterSpeed
	rate := nav.Altitude.RateAfterSpeed
	active := nav.activeAssignedAltitude()
	nav.Altitude = NavAltitude{
		Assigned:       &alt,
		ActiveAssigned: active,
		ActivateAt:     simTime.Add(nav.Rand.DurationRange(2*time.Second, 4*time.Second)),
		Rate:           rate,
	}
}

func (nav *Nav) AssignMach(mach float32, afterAltitude bool, temp av.Temperature) av.CommandIntent {
	if mach == 0 {
		nav.Speed = NavSpeed{}
		return av.SpeedIntent{Type: av.SpeedCancel}
	} else if mach < .65 {
		return av.MakeUnableIntent("unable. Our minimum mach is 0.65")
	} else if mach > nav.Perf.Speed.MaxMach {
		return av.MakeUnableIntent("unable. Our maximum mach is {mach}", nav.Perf.Speed.MaxMach)
	} else if !nav.machTransition() {
		return av.MakeUnableIntent("unable. we haven't reached mach transition altitude")
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		sr := av.MakeMachRestriction(mach)
		nav.Speed = NavSpeed{
			AfterAltitude:         &sr,
			AfterAltitudeAltitude: &alt,
		}
		return av.SpeedIntent{Speed: mach, AfterAltitude: &alt, Type: av.SpeedAssign, Mach: true}
	} else {
		sr := av.MakeMachRestriction(mach)
		nav.Speed = NavSpeed{Assigned: &sr}
		// If there's an active altitude change and this is a significant speed change, defer the
		// altitude until after the Mach speed change completes.
		tas := av.MachToTAS(mach, temp)
		targetIAS := av.TASToIAS(tas, nav.FlightState.Altitude)
		if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude &&
			math.Abs(targetIAS-nav.FlightState.IAS) >= 20 {
			alt := *nav.Altitude.Assigned
			nav.Altitude = NavAltitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &targetIAS,
				RateAfterSpeed:  nav.Altitude.Rate,
			}
		}
		if mach < nav.Mach(temp) {
			return av.SpeedIntent{Speed: mach, Type: av.SpeedReduce, Mach: true}
		} else if mach > nav.Mach(temp) {
			return av.SpeedIntent{Speed: mach, Type: av.SpeedIncrease, Mach: true}
		} else {
			return av.SpeedIntent{Speed: mach, Type: av.SpeedAssign, Mach: true}
		}
	}
}

func (nav *Nav) AssignSpeed(sr *av.SpeedRestriction, afterAltitude bool) av.CommandIntent {
	nav.clearAfterFixSpeeds()

	if sr == nil {
		nav.Speed = NavSpeed{}
		return av.SpeedIntent{Type: av.SpeedCancel}
	}

	// Determine the representative speed for validation and readback.
	speed, exact := sr.ExactValue()
	if !exact {
		speed = sr.Range[0]
		if speed == 0 {
			speed = sr.Range[1]
		}
	}

	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if speed < nav.Perf.Speed.Landing {
		return av.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if speed > maxIAS {
		return av.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS)
	}

	if !exact {
		// Range restriction: no afterAltitude deferral
		nav.Speed = NavSpeed{Assigned: sr}
		if sr.Range[0] > 0 && sr.Range[1] == av.MaxRestrictionSpeed {
			return av.SpeedIntent{Speed: sr.Range[0], Type: av.SpeedAtOrAbove}
		}
		return av.SpeedIntent{Speed: sr.Range[1], Type: av.SpeedAtOrBelow}
	}

	if nav.Approach.Cleared {
		nav.Speed = NavSpeed{Assigned: sr}
		dir := av.SpeedAssign
		if speed < nav.FlightState.IAS {
			dir = av.SpeedReduce
		} else if speed > nav.FlightState.IAS {
			dir = av.SpeedIncrease
		}
		return av.SpeedIntent{Speed: speed, Type: av.SpeedUntilFinal, UntilFinalDirection: dir}
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		nav.Speed = NavSpeed{
			AfterAltitude:         sr,
			AfterAltitudeAltitude: &alt,
		}
		return av.SpeedIntent{Speed: speed, AfterAltitude: &alt, Type: av.SpeedAssign}
	} else {
		// If there's an active altitude change and the speed change is significant (>20kt), defer
		// the altitude until after the speed change completes.
		speedDelta := math.Abs(speed - nav.FlightState.IAS)
		if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude &&
			speedDelta > 20 {
			alt := *nav.Altitude.Assigned
			nav.Altitude = NavAltitude{
				AfterSpeed:      &alt,
				AfterSpeedSpeed: &speed,
				RateAfterSpeed:  nav.Altitude.Rate,
			}
		}
		nav.Speed = NavSpeed{Assigned: sr}
		if speed < nav.FlightState.IAS {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedReduce}
		} else if speed > nav.FlightState.IAS {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedIncrease}
		} else {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedAssign}
		}
	}
}

func (nav *Nav) AssignSpeedUntil(sr *av.SpeedRestriction, until *av.SpeedUntil) av.CommandIntent {
	nav.clearAfterFixSpeeds()

	speed, exact := sr.ExactValue()
	if !exact {
		speed = sr.Range[0]
		if speed == 0 {
			speed = sr.Range[1]
		}
	}

	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if speed < nav.Perf.Speed.Landing {
		return av.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if speed > maxIAS {
		return av.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS)
	}

	nav.Speed = NavSpeed{Assigned: sr}
	if !exact {
		if sr.Range[0] > 0 && sr.Range[1] == av.MaxRestrictionSpeed {
			return av.SpeedIntent{Speed: sr.Range[0], Type: av.SpeedAtOrAbove, Until: until}
		}
		return av.SpeedIntent{Speed: sr.Range[1], Type: av.SpeedAtOrBelow, Until: until}
	}
	return av.SpeedIntent{Speed: speed, Type: av.SpeedUntilFinal, Until: until}
}

func (nav *Nav) MaintainSlowestPractical() av.CommandIntent {
	nav.clearAfterFixSpeeds()
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}
	return av.SpeedIntent{Type: av.SpeedSlowestPractical}
}

func (nav *Nav) MaintainMaximumForward() av.CommandIntent {
	nav.clearAfterFixSpeeds()
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	return av.SpeedIntent{Type: av.SpeedMaximumForward}
}

func (nav *Nav) MaintainPresentSpeed() av.CommandIntent {
	nav.clearAfterFixSpeeds()
	// Capture current indicated airspeed and assign it, rounded to nearest 10
	currentSpeed := nav.FlightState.IAS
	speed := float32(int((currentSpeed+5)/10) * 10)
	sr := av.MakeAtSpeedRestriction(speed)
	nav.Speed = NavSpeed{Assigned: &sr}
	return av.SpeedIntent{Speed: speed, Type: av.SpeedPresentSpeed}
}

func (nav *Nav) SaySpeed(temp av.Temperature) av.CommandIntent {
	if nav.machTransition() {
		return nav.SayMach(temp)
	}
	return nav.SayIndicatedSpeed()
}

func (nav *Nav) SayIndicatedSpeed() av.CommandIntent {
	currentSpeed := nav.FlightState.IAS
	intent := av.ReportSpeedIntent{Current: currentSpeed}
	if sr := nav.Speed.Assigned; sr != nil && !sr.IsMach {
		if spd, exact := sr.ExactValue(); exact {
			intent.Assigned = &spd
		}
	} else if _, sr, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		naturalIAS, _ := nav.targetAltitudeIAS()
		spd := nav.restrictedSpeed(sr, naturalIAS)
		intent.Assigned = &spd
	} else if nav.Speed.Restriction != nil {
		naturalIAS, _ := nav.targetAltitudeIAS()
		spd := nav.restrictedSpeed(nav.Speed.Restriction, naturalIAS)
		intent.Assigned = &spd
	}
	return intent
}

func (nav *Nav) SayMach(temp av.Temperature) av.CommandIntent {
	if !nav.machTransition() {
		return av.MakeUnableIntent("unable. we haven't reached mach transition altitude")
	}
	currentMach := nav.Mach(temp)
	intent := av.ReportMachIntent{Current: currentMach}
	if sr := nav.Speed.Assigned; sr != nil && sr.IsMach {
		if mach, exact := sr.ExactValue(); exact {
			intent.Assigned = &mach
		}
	}
	return intent
}

func (nav *Nav) SayHeading() av.CommandIntent {
	currentHeading := nav.FlightState.Heading
	intent := av.ReportHeadingIntent{Current: currentHeading}
	if nav.Heading.Assigned != nil {
		intent.Assigned = nav.Heading.Assigned
	}
	return intent
}

// SayAltitude reports the pilot's *indicated* altitude. The pilot reads
// off their altimeter, which is offset from true altitude by altimBiasFeet
// when the pilot's setting differs from the local actual.
func (nav *Nav) SayAltitude(altimBiasFeet float32) av.CommandIntent {
	indicatedAltitude := nav.FlightState.Altitude - altimBiasFeet
	intent := av.ReportAltitudeIntent{Current: indicatedAltitude}
	if nav.Altitude.Assigned != nil {
		intent.Assigned = nav.Altitude.Assigned
		if *nav.Altitude.Assigned < indicatedAltitude {
			intent.Direction = av.AltitudeDescend
		} else if *nav.Altitude.Assigned > indicatedAltitude {
			intent.Direction = av.AltitudeClimb
		} else {
			intent.Direction = av.AltitudeMaintain
		}
	}
	return intent
}

func (nav *Nav) ExpediteDescent() av.CommandIntent {
	return nav.setRate(RateExpedite, nil, av.AltitudeDescend)
}

func (nav *Nav) ExpediteClimb() av.CommandIntent {
	return nav.setRate(RateExpedite, nil, av.AltitudeClimb)
}

func (nav *Nav) ExpediteDescentThrough(throughAlt float32) av.CommandIntent {
	return nav.setRate(RateExpedite, &throughAlt, av.AltitudeDescend)
}

func (nav *Nav) ExpediteClimbThrough(throughAlt float32) av.CommandIntent {
	return nav.setRate(RateExpedite, &throughAlt, av.AltitudeClimb)
}

func (nav *Nav) GoodRateDescent() av.CommandIntent {
	return nav.setRate(RateGood, nil, av.AltitudeDescend)
}

func (nav *Nav) GoodRateClimb() av.CommandIntent {
	return nav.setRate(RateGood, nil, av.AltitudeClimb)
}

func (nav *Nav) GoodRateThrough(throughAlt float32) av.CommandIntent {
	// Infer direction from current state
	dir := av.AltitudeDescend
	if throughAlt > nav.FlightState.Altitude {
		dir = av.AltitudeClimb
	}
	return nav.setRate(RateGood, &throughAlt, dir)
}

func (nav *Nav) setRate(rate RateQualifier, throughAlt *float32, direction av.AltitudeDirection) av.CommandIntent {
	alt, _, _ := nav.TargetAltitude()
	if nav.Altitude.Assigned != nil {
		alt = *nav.Altitude.Assigned
	}

	wrongDir := (direction == av.AltitudeDescend && alt >= nav.FlightState.Altitude) ||
		(direction == av.AltitudeClimb && alt <= nav.FlightState.Altitude)

	if wrongDir {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.RateAfterSpeed = rate
			return av.AltitudeIntent{
				Direction:  direction,
				Altitude:   *nav.Altitude.AfterSpeed,
				AfterSpeed: nav.Altitude.AfterSpeedSpeed,
			}
		}
		dir := "descending"
		if direction == av.AltitudeClimb {
			dir = "climbing"
		}
		return av.MakeUnableIntent("unable. We're not " + dir)
	}

	if nav.Altitude.Rate >= rate {
		return av.AltitudeIntent{
			Direction:         direction,
			Altitude:          alt,
			AlreadyExpediting: true,
			GoodRate:          nav.Altitude.Rate == RateGood,
		}
	}

	nav.Altitude.Rate = rate
	nav.Altitude.RateThrough = throughAlt
	return av.AltitudeIntent{
		Direction:   direction,
		Altitude:    alt,
		Expedite:    rate == RateExpedite,
		GoodRate:    rate == RateGood,
		RateThrough: throughAlt,
	}
}

func (nav *Nav) AssignHeading(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time, delayReduction time.Duration) av.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnableIntent("unable. {hdg} isn't a valid heading", hdg)
	}

	cancelHold := nav.Heading.Hold != nil
	nav.assignHeading(hdg, turn, simTime, delayReduction)

	intent := av.HeadingIntent{
		Heading:    hdg,
		Type:       av.HeadingAssign,
		CancelHold: cancelHold,
	}

	switch turn {
	case av.TurnClosest:
		intent.Turn = av.HeadingTurnClosest
	case av.TurnRight:
		intent.Turn = av.HeadingTurnToRight
	case av.TurnLeft:
		intent.Turn = av.HeadingTurnToLeft
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}

	return intent
}

func (nav *Nav) assignHeading(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time, delayReduction time.Duration) {
	approachCleared := nav.Approach.Cleared

	if _, ok := nav.AssignedHeading(); !ok {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false

		// If an arrival is given a heading off of a route with altitude
		// constraints, set its cleared altitude to its current altitude
		// for now. AfterSpeed counts as an explicit assignment too — the
		// controller has assigned an altitude, it's just deferred until
		// the speed change completes.
		if len(nav.Waypoints) > 0 && (nav.Waypoints[0].OnSTAR() || nav.Waypoints[0].OnApproach()) &&
			nav.Altitude.Assigned == nil && nav.Altitude.AfterSpeed == nil {
			if _, ok := nav.findAltitudeTarget(); ok {
				// Don't take a direct pointer to nav.FlightState.Altitude!
				alt := nav.FlightState.Altitude
				nav.Altitude.Cleared = &alt
				nav.Approach.RequestAltitude = true
			}
		}
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(hdg, turn, approachCleared, simTime, delayReduction)
}

func (nav *Nav) FlyPresentHeading(simTime Time, delayReduction time.Duration) av.CommandIntent {
	nav.assignHeading(nav.FlightState.Heading, av.TurnClosest, simTime, delayReduction)
	return av.HeadingIntent{
		Heading: nav.FlightState.Heading,
		Type:    av.HeadingPresent,
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
	if ap := nav.Approach.Assigned; ap != nil {
		// This is a little hacky, but... Because of the way we currently
		// interpret ARINC424 files, fixes with procedure turns have no
		// procedure turn for routes with /nopt from the previous fix.
		// Therefore, if we are going direct to a fix that has a procedure
		// turn, we can't take the first matching route but have to keep
		// looking for it in case another route has it with a PT...
		var apWps []av.Waypoint
		for _, route := range ap.Waypoints {
			for i, wp := range route {
				if wp.Fix == fix {
					apWps = append(route[i:], nav.FlightState.ArrivalAirport)
					if wp.ProcedureTurn() != nil {
						return apWps, waypointSourceApproach, nil
					}
				}
			}
		}
		if apWps != nil {
			return apWps, waypointSourceApproach, nil
		}
	}

	// See if it's a random fix not in the flight plan.
	p, ok := func() (math.Point2LL, bool) {
		if p, ok := av.DB.LookupWaypoint(fix); ok {
			return p, true
		} else if ap, ok := av.DB.Airports[fix]; ok {
			return ap.Location, true
		} else if ap, ok := av.DB.Airports["K"+fix]; len(fix) == 3 && ok {
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

func (nav *Nav) ExpectDirect(fix string) av.CommandIntent {
	if _, ok := av.DB.LookupWaypoint(fix); !ok && !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
	nav.ExpectedDirectFix = fix
	return nil
}

func (nav *Nav) DirectFix(fix string, turn av.TurnDirection, simTime Time, delayReduction time.Duration) av.CommandIntent {
	if wps, source, err := nav.directFixWaypoints(fix); err == nil {
		if hold := nav.Heading.Hold; hold != nil {
			// We'll finish our lap and then depart the holding fix direct to the fix
			hold.Cancel = true
			nfa := NavFixAssignment{}
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
			return av.NavigationIntent{
				Type:      av.NavDirectFixFromHold,
				Fix:       hold.Hold.Fix,
				SecondFix: fix,
				Turn:      turn,
			}
		} else {
			nav.EnqueueDirectFix(wps, turn, simTime, delayReduction)
			nav.Approach.NoPT = false
			if source == waypointSourceApproach && !nav.Approach.Cleared {
				// The waypoints came from the approach but the aircraft
				// hasn't been cleared; track the approach course laterally
				// but gate altitude constraints via InterceptedButNotCleared().
				nav.Approach.InterceptState = OnApproachCourse
			} else {
				nav.Approach.InterceptState = NotIntercepting
			}
			if !nav.Approach.Cleared {
				nav.Approach.InterceptedReference = nav.visualReferenceForFix(fix)
			}
			return av.NavigationIntent{
				Type: av.NavDirectFix,
				Fix:  fix,
				Turn: turn,
			}
		}
	} else if err == ErrFixIsTooFarAway {
		return av.MakeUnableIntent("unable. {fix} is too far away to go direct", fix)
	} else {
		return av.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
}

func (nav *Nav) HoldAtFix(callsign string, fix string, hold *av.Hold) av.CommandIntent {
	if _, ok := av.DB.LookupWaypoint(fix); !ok {
		return av.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	} else if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	// Use controller-specified hold if provided, otherwise look up published hold
	var h av.Hold
	if hold != nil {
		// Controller-specified hold
		h = *hold
	} else {
		// Published hold
		holds, ok := av.DB.EnrouteHolds[fix]
		if !ok || len(holds) == 0 {
			return av.MakeUnableIntent("unable. no published hold at {fix}", fix)
		}
		h = holds[0]
	}

	if len(nav.Waypoints) > 0 && nav.Waypoints[0].Fix == h.Fix && nav.Heading.Assigned == nil {
		// We're already direct to it for the next fix; get started.
		nav.Heading = NavHeading{Hold: nav.makeFlyHold(callsign, h)}
	} else if nav.DeferredNavHeading != nil && len(nav.DeferredNavHeading.Waypoints) > 0 &&
		nav.DeferredNavHeading.Waypoints[0].Fix == h.Fix {
		nav.DeferredNavHeading.Hold = nav.makeFlyHold(callsign, h)
	} else {
		// It's a later fix. Queue it up; we'll return to it when it's the next waypoint upcoming.
		// Clobber any altitude or heading assignments associated with the fix.
		nav.FixAssignments[h.Fix] = NavFixAssignment{Hold: &h}
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

	return av.NavigationIntent{
		Type:          av.NavHold,
		Fix:           h.Fix,
		HoldDirection: turnDir,
		HoldLegLength: legLength,
	}
}

func (nav *Nav) makeFlyHold(callsign string, hold av.Hold) *FlyHold {
	// Calculate heading from aircraft's current position to fix
	pHold, _ := av.DB.LookupWaypoint(hold.Fix)
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

func (nav *Nav) DepartFixDirect(fixa string, fixb string) av.CommandIntent {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return av.MakeUnableIntent("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return av.NavigationIntent{
		Type:      av.NavDepartFixDirect,
		Fix:       fixa,
		SecondFix: fixb,
	}
}

func (nav *Nav) DepartFixHeading(fix string, hdg math.MagneticHeading) av.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnableIntent("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	nfa := nav.FixAssignments[fix]
	h := hdg
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return av.NavigationIntent{
		Type:    av.NavDepartFixHeading,
		Fix:     fix,
		Heading: hdg,
	}
}

func (nav *Nav) CrossFixAt(fix string, ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	intent := av.NavigationIntent{
		Type: av.NavCrossFixAt,
		Fix:  fix,
	}

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		intent.AltRestriction = ar
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if sr != nil {
		nfa.Arrive.Speed = sr
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS()
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		// Delete other speed restrictions
		nav.Speed = NavSpeed{}
	}
	nav.FixAssignments[fix] = nfa

	return intent
}

func (nav *Nav) CrossDistanceFromFixAt(fix string, dist float32, dir math.CardinalOrdinalDirection,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	useDeferred := false
	routeWps := []av.Waypoint(nav.Waypoints)
	if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
		useDeferred = true
		routeWps = dh.Waypoints
	}
	commitRouteWps := func() {
		if useDeferred {
			nav.DeferredNavHeading.Waypoints = routeWps
		} else {
			nav.Waypoints = routeWps
		}
	}

	wps := routeWps
	idx := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if idx == -1 {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
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
		return av.MakeUnableIntent("unable. We're approaching {fix} from the "+actualDir, fix)
	}

	// Distance validation against the real segment.
	segLen := math.NMDistance2LL(priorLoc, fixLoc)
	if realPriorIdx >= 0 && dist >= segLen {
		return av.MakeUnableIntent("unable. That's before {fix}", priorName)
	}
	if realPriorIdx < 0 && dist >= math.NMDistance2LL(nav.FlightState.Position, fixLoc) {
		return av.MakeUnableIntent("unable. We're already closer to {fix}", fix)
	}

	// Compute synthetic waypoint via linear interpolation.
	t := 1 - dist/segLen
	syntheticLoc := math.Point2LL{
		math.Lerp(t, priorLoc[0], fixLoc[0]),
		math.Lerp(t, priorLoc[1], fixLoc[1]),
	}

	clearAltitude := func(wp *av.Waypoint) {
		wp.ClearAltitudeRestriction()
	}
	clearSpeed := func(wp *av.Waypoint) {
		wp.ClearSpeedRestriction()
	}
	hasInlineRestrictions := func(wp *av.Waypoint) bool {
		return wp.HasAltitudeRestriction() || wp.HasSpeedRestriction()
	}

	// 1. Remove or clear existing synthetic waypoints for this fix and restriction types.
	removePrefix := "_" + fix + "/"
	for i := 0; i < len(routeWps); {
		wp := &routeWps[i]
		if strings.HasPrefix(wp.Fix, removePrefix) {
			if ar != nil {
				clearAltitude(wp)
			}
			if sr != nil {
				clearSpeed(wp)
			}
			if !hasInlineRestrictions(wp) {
				routeWps = slices.Delete(routeWps, i, i+1)
				continue
			}
		}
		i++
	}
	commitRouteWps()

	// 2. Refresh index as waypoints might have shifted.
	wps = routeWps
	idx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })

	intent := av.NavigationIntent{
		Type:      av.NavCrossDistanceFromFixAt,
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

		wp := av.Waypoint{Fix: name, Location: loc}
		wp.SetSyntheticCrossing(true)
		if idx < len(wps) {
			wp.SetOnSID(wps[idx].OnSID())
			wp.SetOnSTAR(wps[idx].OnSTAR())
			wp.SetOnApproach(wps[idx].OnApproach())
		}
		routeWps = slices.Insert(routeWps, insertIdx, wp)
		commitRouteWps()
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
	if ar != nil {
		wp.SetAltitudeRestriction(*ar)
		intent.AltRestriction = ar
		nav.Altitude = NavAltitude{}
	}

	if sr != nil {
		wp.SetSpeedRestriction(*sr)
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS()
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		nav.Speed = NavSpeed{}
	}
	commitRouteWps()

	return intent
}

// CrossDMEAt inserts a synthetic crossing restriction at a given DME from
// the runway threshold of the currently cleared visual approach. The
// synthetic waypoint is placed along the approach route by walking backwards
// from the threshold accumulating track miles; if dist exceeds the total
// route length, the point is extrapolated backwards along the first leg.
func (nav *Nav) CrossDMEAt(dist float32, ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	if dist <= 0 || dist > 30 {
		return av.MakeUnableIntent("unable, that distance is out of range")
	}

	ap := nav.Approach.Assigned
	if ap == nil || !nav.Approach.Cleared ||
		(ap.Type != av.VisualApproach && ap.Type != av.ChartedVisualApproach) {
		return av.MakeUnableIntent("unable, we're not cleared for a visual approach")
	}
	runway := ap.Runway

	useDeferred := false
	routeWps := []av.Waypoint(nav.Waypoints)
	if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
		useDeferred = true
		routeWps = dh.Waypoints
	}
	commitRouteWps := func() {
		if useDeferred {
			nav.DeferredNavHeading.Waypoints = routeWps
		} else {
			nav.Waypoints = routeWps
		}
	}

	if len(routeWps) < 2 {
		return av.MakeUnableIntent("unable")
	}

	// 1. Clear matching restriction categories on any existing synthetic DME
	// waypoints for this runway; drop any that end up with no restrictions.
	for i := 0; i < len(routeWps); {
		wp := &routeWps[i]
		if wpRunway, _, ok := av.ParseSyntheticDMEFix(wp.Fix); ok && wpRunway == runway {
			if ar != nil {
				wp.ClearAltitudeRestriction()
			}
			if sr != nil {
				wp.ClearSpeedRestriction()
			}
			if !wp.HasAltitudeRestriction() && !wp.HasSpeedRestriction() {
				routeWps = slices.Delete(routeWps, i, i+1)
				continue
			}
		}
		i++
	}
	commitRouteWps()

	// Deletion above can leave the threshold alone (e.g., the aircraft has
	// passed all prior visual waypoints and the only remaining synthetic
	// was just removed); without at least two waypoints we have no route to
	// measure along.
	if len(routeWps) < 2 {
		return av.MakeUnableIntent("unable")
	}

	// 2. Walk backward from the threshold, accumulating track miles, and
	// place the synthetic waypoint on the first leg that spans `dist`. If
	// `dist` exceeds the full route length, extrapolate past wp[0] along
	// the direction from wp[1] back to wp[0]. The final waypoint is the
	// arrival airport (appended after the threshold); skip the
	// threshold-to-airport leg so distances are measured from the threshold.
	thresholdIdx := len(routeWps) - 2
	if thresholdIdx < 1 {
		return av.MakeUnableIntent("unable")
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
		newWp := av.Waypoint{Fix: name, Location: syntheticLoc}
		newWp.SetSyntheticCrossing(true)
		if insertIdx < len(routeWps) {
			newWp.SetOnSID(routeWps[insertIdx].OnSID())
			newWp.SetOnSTAR(routeWps[insertIdx].OnSTAR())
			newWp.SetOnApproach(routeWps[insertIdx].OnApproach())
		}
		routeWps = slices.Insert(routeWps, insertIdx, newWp)
		wp = &routeWps[insertIdx]
	}

	intent := av.NavigationIntent{
		Type:     av.NavCrossDME,
		Fix:      name,
		Distance: dist,
	}

	if ar != nil {
		wp.SetAltitudeRestriction(*ar)
		intent.AltRestriction = ar
		nav.Altitude = NavAltitude{}
	}

	if sr != nil {
		wp.SetSpeedRestriction(*sr)
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS()
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		nav.Speed = NavSpeed{}
	}
	commitRouteWps()

	return intent
}

func (nav *Nav) AfterFixSpeed(fix string, sr *av.SpeedRestriction) av.CommandIntent {
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
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

	var stype av.SpeedType
	if !exact {
		if sr.Range[0] > 0 {
			stype = av.SpeedAtOrAbove
		} else {
			stype = av.SpeedAtOrBelow
		}
	} else if speed < nav.FlightState.IAS {
		stype = av.SpeedReduce
	} else if speed > nav.FlightState.IAS {
		stype = av.SpeedIncrease
	} else {
		stype = av.SpeedAssign
	}

	return av.SpeedIntent{Speed: speed, Type: stype, AfterFix: fix}
}

func (nav *Nav) AssignCompoundSpeed(segments []av.CompoundSpeedSegment) av.CommandIntent {
	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10))

	// Validate all segments before applying any state changes.
	for _, seg := range segments {
		speed, exact := seg.Speed.ExactValue()
		if !exact {
			speed = seg.Speed.Range[0]
			if speed == 0 {
				speed = seg.Speed.Range[1]
			}
		}
		if speed < nav.Perf.Speed.Landing {
			return av.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
		} else if speed > maxIAS {
			return av.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS)
		}

		if seg.UntilFix != "" && !nav.fixInRoute(seg.UntilFix) {
			return av.MakeUnableIntent("unable. {fix} isn't in our route", seg.UntilFix)
		}
	}

	// Apply: first segment sets the current speed, subsequent segments
	// set after-fix speed assignments.
	nav.clearAfterFixSpeeds()
	nav.Speed = NavSpeed{Assigned: segments[0].Speed}

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

	return av.CompoundSpeedIntent{Segments: segments}
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

func (nav *Nav) AfterFixAltitude(fix string, alt float32) av.CommandIntent {
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}
	if alt > nav.Perf.Ceiling {
		return av.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}

	nfa := nav.FixAssignments[fix]
	nfa.Depart.Altitude = &alt
	nav.FixAssignments[fix] = nfa

	var direction av.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = av.AltitudeClimb
	} else if alt < nav.FlightState.Altitude {
		direction = av.AltitudeDescend
	} else {
		direction = av.AltitudeMaintain
	}

	return av.AltitudeIntent{
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

func (nav *Nav) CancelApproachClearance() av.CommandIntent {
	if !nav.Approach.Cleared {
		return av.MakeUnableIntent("unable. we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false

	return av.ApproachIntent{Type: av.ApproachCancel}
}

func (nav *Nav) ClimbViaSID(simTime Time) av.CommandIntent {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSID() {
		return av.MakeUnableIntent("unable. We're not flying a departure procedure")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse(simTime)
	return av.ProcedureIntent{Type: av.ProcedureClimbViaSID}
}

func (nav *Nav) DescendViaSTAR(simTime Time) av.CommandIntent {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSTAR() {
		return av.MakeUnableIntent("unable. We're not on a STAR")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse(simTime)
	return av.ProcedureIntent{Type: av.ProcedureDescendViaSTAR}
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

func (nav *Nav) ResumeOwnNavigation() av.CommandIntent {
	if nav.Heading.Assigned == nil {
		// This is a weird response but keeping the original behavior
		return av.MakeUnableIntent("unable. I don't think you ever put us on a heading...")
	}

	nav.Heading = NavHeading{}
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
	return av.NavigationIntent{Type: av.NavResumeOwnNav}
}

func (nav *Nav) AltitudeOurDiscretion() av.CommandIntent {
	if nav.Altitude.Assigned == nil {
		return av.MakeUnableIntent("unable. You never assigned us an altitude...")
	}

	nav.Altitude = NavAltitude{}
	alt := nav.FinalAltitude
	nav.Altitude.Cleared = &alt

	return av.NavigationIntent{Type: av.NavAltitudeDiscretion}
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}

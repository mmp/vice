// nav/speed.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/wx"
)

type Speed struct {
	Assigned                 *av.SpeedRestriction // controller-assigned (exact or range)
	AfterAltitude            *SpeedAfterAltitude
	MaintainSlowestPractical bool
	MaintainMaximumForward   bool
	Restriction              *av.SpeedRestriction // Carried after passing a waypoint
}

// SpeedAfterAltitude is a speed assignment that waits on an altitude change,
// as in "descend and maintain 6,000, then reduce speed to 180": once the
// aircraft reaches Altitude it is assigned Speed.
type SpeedAfterAltitude struct {
	Speed    av.SpeedRestriction
	Altitude float32
}

// TAS returns the aircraft's true airspeed in knots.
func (nav *Nav) TAS(temp av.Temperature) float32 {
	return nav.FlightState.TAS(temp)
}

func (nav *Nav) Mach() float32 {
	return av.IASToMach(nav.FlightState.IAS, nav.FlightState.Altitude)
}

// vmoCrossoverAltitude is a typical altitude at which a jet's maximum
// operating speed (V_MO) and its maximum Mach number give the same
// indicated airspeed. The performance database doesn't provide V_MO, so it
// is approximated as the maximum Mach number's IAS there.
const vmoCrossoverAltitude = 28000

// maxIAS returns the fastest indicated airspeed the aircraft can fly at
// its current altitude.
func (nav *Nav) maxIAS(temp av.Temperature) float32 {
	alt := nav.FlightState.Altitude
	ias := av.TASToIAS(nav.Perf.Speed.MaxTAS, alt, temp)
	if mach := nav.Perf.Speed.MaxMach; mach != 0 {
		if nav.Perf.Engine.AircraftType == "J" {
			alt = max(alt, vmoCrossoverAltitude)
		}
		ias = min(ias, av.MachToIAS(mach, alt))
	}
	return ias
}

// machTransition returns whether the aircraft's speed may be assigned as
// a Mach number: 7110.65 5-7-2 allows it for turbojets at or above FL240.
func (nav *Nav) machTransition() bool {
	return nav.Perf.Engine.AircraftType == "J" && nav.FlightState.Altitude >= av.MachTransitionAltitude
}

// checkAssignedSpeed returns an unable intent and false if the aircraft
// can't fly the given speed.
func (nav *Nav) checkAssignedSpeed(spd av.Airspeed, temp av.Temperature) (speech.CommandIntent, bool) {
	if spd.IsMach {
		// Round the limits to hundredths, with slack for the round trip
		// through IAS when the maximum Mach number is what limits maxIAS.
		alt := nav.FlightState.Altitude
		minMach := math.Ceil(100*av.IASToMach(nav.Perf.Speed.Landing, alt)-0.01) / 100
		maxMach := math.Floor(100*av.IASToMach(nav.maxIAS(temp), alt)+0.01) / 100

		if spd.Value < minMach {
			return speech.MakeUnableIntent("unable. Our minimum mach is {mach}", minMach), false
		} else if spd.Value > maxMach {
			return speech.MakeUnableIntent("unable. Our maximum mach is {mach}", maxMach), false
		} else if !nav.machTransition() {
			return speech.MakeUnableIntent("unable. we haven't reached mach transition altitude"), false
		}
		return nil, true
	}

	maxIAS := 10 * math.Floor(nav.maxIAS(temp)/10)
	if spd.Value < nav.Perf.Speed.Landing {
		return speech.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing), false
	} else if spd.Value > maxIAS {
		return speech.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS), false
	}
	return nil, true
}

// rangeSpeedIntent returns the readback for an at or above or an at or
// below speed restriction; a band is read back as its ceiling.
func rangeSpeedIntent(sr *av.SpeedRestriction) speech.SpeedIntent {
	spd := sr.Speed()
	if sr.Range[1] == av.MaxRestrictionSpeed {
		return speech.SpeedIntent{Speed: spd, Type: speech.SpeedAtOrAbove}
	}
	spd.Value = sr.Range[1]
	return speech.SpeedIntent{Speed: spd, Type: speech.SpeedAtOrBelow}
}

// speedChangeType returns whether reaching spd is a reduction or an
// increase from the current speed.
func (nav *Nav) speedChangeType(spd av.Airspeed) speech.SpeedType {
	if ias := spd.IAS(nav.FlightState.Altitude); ias < nav.FlightState.IAS {
		return speech.SpeedReduce
	} else if ias > nav.FlightState.IAS {
		return speech.SpeedIncrease
	}
	return speech.SpeedAssign
}

// AssignSpeed assigns a speed in knots or a Mach number; nil cancels
// speed restrictions.
func (nav *Nav) AssignSpeed(sr *av.SpeedRestriction, afterAltitude bool, temp av.Temperature) speech.CommandIntent {
	defer nav.record("AssignSpeed", sr, afterAltitude, temp)()
	if sr == nil {
		nav.clearAfterFixSpeeds()
		nav.Speed = Speed{}
		return speech.SpeedIntent{Type: speech.SpeedCancel}
	}

	spd := sr.Speed()
	if intent, ok := nav.checkAssignedSpeed(spd, temp); !ok {
		return intent
	}
	nav.clearAfterFixSpeeds()

	_, exact := sr.ExactValue()
	if nav.Approach.Cleared && exact {
		nav.Speed = Speed{Assigned: sr}
		return speech.SpeedIntent{Speed: spd, Type: speech.SpeedUntilFinal, UntilFinalDirection: nav.speedChangeType(spd)}
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		nav.Speed = Speed{AfterAltitude: &SpeedAfterAltitude{Speed: *sr, Altitude: alt}}
		intent := speech.SpeedIntent{Speed: spd, Type: speech.SpeedAssign}
		if !exact {
			intent = rangeSpeedIntent(sr)
		}
		intent.AfterAltitude = &alt
		return intent
	} else if !exact {
		nav.Speed = Speed{Assigned: sr}
		return rangeSpeedIntent(sr)
	}

	// If there's an active altitude change and the speed change is significant (>20kt), defer
	// the altitude until after the speed change completes.
	if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude &&
		math.Abs(spd.IAS(nav.FlightState.Altitude)-nav.FlightState.IAS) > 20 {
		alt := *nav.Altitude.Assigned
		nav.Altitude = Altitude{AfterSpeed: &AltitudeAfterSpeed{Altitude: alt, Speed: spd, Rate: nav.Altitude.Rate}}
	}
	nav.Speed = Speed{Assigned: sr}
	return speech.SpeedIntent{Speed: spd, Type: nav.speedChangeType(spd)}
}

func (nav *Nav) AssignSpeedUntil(sr *av.SpeedRestriction, until *speech.SpeedUntil, temp av.Temperature) speech.CommandIntent {
	defer nav.record("AssignSpeedUntil", sr, until, temp)()
	if until.Fix != "" && !nav.knowsFix(until.Fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", until.Fix)
	}

	spd := sr.Speed()
	if intent, ok := nav.checkAssignedSpeed(spd, temp); !ok {
		return intent
	}
	nav.clearAfterFixSpeeds()

	nav.Speed = Speed{Assigned: sr}
	if _, exact := sr.ExactValue(); !exact {
		intent := rangeSpeedIntent(sr)
		intent.Until = until
		return intent
	}
	return speech.SpeedIntent{Speed: spd, Type: speech.SpeedUntilFinal, Until: until}
}

func (nav *Nav) MaintainSlowestPractical() speech.CommandIntent {
	defer nav.record("MaintainSlowestPractical")()
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{MaintainSlowestPractical: true}
	return speech.SpeedIntent{Type: speech.SpeedSlowestPractical}
}

func (nav *Nav) MaintainMaximumForward() speech.CommandIntent {
	defer nav.record("MaintainMaximumForward")()
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{MaintainMaximumForward: true}
	return speech.SpeedIntent{Type: speech.SpeedMaximumForward}
}

func (nav *Nav) MaintainPresentSpeed() speech.CommandIntent {
	defer nav.record("MaintainPresentSpeed")()
	nav.clearAfterFixSpeeds()
	// Capture current indicated airspeed and assign it, rounded to nearest 10
	currentSpeed := nav.FlightState.IAS
	spd := av.MakeIAS(float32(int((currentSpeed+5)/10) * 10))
	sr := av.MakeAtAirspeedRestriction(spd)
	nav.Speed = Speed{Assigned: &sr}
	return speech.SpeedIntent{Speed: spd, Type: speech.SpeedPresentSpeed}
}

// hasIssuedSpeed reports whether a controller speed instruction, rather than
// the route's restrictions, governs the aircraft's speed.
func (nav *Nav) hasIssuedSpeed() bool {
	s := nav.Speed
	return s.Assigned != nil || s.AfterAltitude != nil || s.MaintainSlowestPractical || s.MaintainMaximumForward
}

// updateAirspeed flies the aircraft toward the speed its instructions call
// for, for one second, and applies what reaching a speed means for them. It
// returns the change in knots and whether the aircraft is slowing to 250
// knots to descend through 10,000 feet. targetAltitude and geometricDescent
// are what the altitude is doing this same second.
func (nav *Nav) updateAirspeed(callsign string, targetAltitude float32, geometricDescent bool, rules av.FlightRules,
	wxs wx.Sample, arrivalMETAR *wx.METAR, simTime Time, underBravo bool) (deltaKts float32, slowingTo250 bool) {
	fs := &nav.FlightState
	target := nav.selectTargetSpeed(targetAltitude, rules, wxs, arrivalMETAR, underBravo)
	if target.initialClimbOver {
		fs.InitialDepartureClimb = false
	}

	// Stay within the aircraft's capabilities
	ias := math.Clamp(target.ias, nav.Perf.MinimumSpeed(), nav.maxIAS(wxs.Temperature()))

	// An aircraft descending to below 10,000 feet at more than 250 knots
	// has to be at 250 to descend through it and is held there until it has
	// slowed, even if it is expediting its descent, which otherwise holds
	// its speed.
	slowingTo250 = ias == 250 && fs.Altitude >= 10000 && targetAltitude < 10000 && fs.IAS > 250
	expediting := nav.Altitude.Rate == RateExpedite && !slowingTo250

	NavLog(callsign, simTime, NavLogSpeed, "target=%.0f current=%.0f rate=%.1f", ias, fs.IAS, target.rate)

	next := fs.stepIAS(nav.Perf, ias, target.rate, targetAltitude, geometricDescent, expediting, wxs.Temperature())
	if next == fs.IAS {
		return 0, slowingTo250
	}
	nav.crossSpeed(next, simTime)
	deltaKts = next - fs.IAS
	fs.IAS = next
	return deltaKts, slowingTo250
}

// crossSpeed applies what reaching a speed on the way from the present
// speed to next means for the aircraft's instructions: an altitude
// assignment waiting on the speed change is taken up.
func (nav *Nav) crossSpeed(next float32, simTime Time) {
	if nav.Altitude.AfterSpeed == nil ||
		(nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude) {
		return
	}
	cur := nav.FlightState.IAS
	at := nav.Altitude.AfterSpeed.Speed.IAS(nav.FlightState.Altitude)
	if (cur > at && next <= at) || (cur < at && next >= at) {
		nav.enqueueAltitudeAfterSpeed(simTime)
	}
}

// cancelSpeedOnFinal drops the aircraft's speed assignments and
// restrictions inside 5 miles of the end of a cleared approach, where it
// flies its approach speed.
func (nav *Nav) cancelSpeedOnFinal() {
	if fd, ok := nav.approachDistanceRemaining(); ok && fd < 5 {
		nav.Speed = Speed{}
	}
}

// speedTarget is the speed guidance asks for: the IAS to fly and the rate
// in knots per minute to reach it at. initialClimbOver reports that a
// departure has climbed out of the altitudes its initial climb's speed
// schedule covers.
type speedTarget struct {
	ias, rate        float32
	initialClimbOver bool
}

// TargetSpeed returns the IAS the aircraft is currently trying to fly and
// the rate at which it should get there. underBravo reports whether it is
// under a Class B shelf, where a VFR aircraft holds 200 knots or less.
func (nav *Nav) TargetSpeed(targetAltitude float32, rules av.FlightRules, wxs wx.Sample, arrivalMETAR *wx.METAR,
	underBravo bool) (float32, float32) {
	t := nav.selectTargetSpeed(targetAltitude, rules, wxs, arrivalMETAR, underBravo)
	return t.ias, t.rate
}

func (nav *Nav) selectTargetSpeed(targetAltitude float32, rules av.FlightRules, wxs wx.Sample, arrivalMETAR *wx.METAR,
	underBravo bool) speedTarget {
	t := nav.governingSpeed(targetAltitude, rules, wxs, arrivalMETAR, underBravo)

	// A speed assignment deferred until an altitude is reached ("descend
	// and maintain 6,000, then reduce speed to 180") is flown by holding
	// the current speed until level off. Anything that slows the aircraft
	// still applies, but it must not accelerate in the meantime.
	if nav.Speed.AfterAltitude != nil {
		t.ias = min(t.ias, nav.FlightState.IAS)
	}

	return t
}

// governingSpeed returns the speed of whatever governs the aircraft's
// speed, in order of priority: a controller's speed instruction, a hold it
// is about to enter, a departure's initial climb, an upcoming or carried
// restriction, the 250 knot limit below 10,000 feet, the approach speed on
// final, or else the speed it flies at its altitude.
func (nav *Nav) governingSpeed(targetAltitude float32, rules av.FlightRules, wxs wx.Sample, arrivalMETAR *wx.METAR,
	underBravo bool) speedTarget {
	temp := wxs.Temperature()

	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	// Controller assignments: these override anything else.
	if nav.Speed.MaintainSlowestPractical {
		return speedTarget{ias: nav.Perf.Speed.Landing + 5, rate: MaximumRate}
	}
	if nav.Speed.MaintainMaximumForward {
		if nav.Approach.Cleared {
			// (We expect this to usually be the case.) Ad-hoc speed based
			// on V2, also assuming some flaps are out, so we don't just
			// want to return 250 knots here...
			return speedTarget{ias: min(v2(nav.Perf)*1.6, min(250, nav.FlightState.cruiseIAS(nav.Perf, temp))), rate: MaximumRate}
		}
		ias, rate := nav.targetAltitudeIAS(temp)
		return speedTarget{ias: ias, rate: rate}
	}
	if sr := nav.Speed.Assigned; sr != nil {
		// Hold the current speed if it's within an assigned range.
		return speedTarget{ias: sr.IAS(nav.FlightState.Altitude).Target(nav.FlightState.IAS), rate: MaximumRate}
	}

	if hold := nav.Heading.Hold; hold != nil && nav.ETA(hold.FixLocation) < 180 /* slow 3 minutes out */ {
		// The holding speed is a maximum; slower aircraft hold at their usual speed.
		naturalIAS, _ := nav.targetAltitudeIAS(temp)
		return speedTarget{ias: min(hold.Hold.Speed(nav.FlightState.Altitude), naturalIAS), rate: MaximumRate}
	}

	// Manage the speed profile in the initial climb
	if nav.FlightState.InitialDepartureClimb {
		agl := nav.FlightState.Altitude - nav.FlightState.DepartureAirportElevation
		isJet := nav.Perf.Engine.AircraftType == "J"

		over := (isJet && agl >= 5000) || (!isJet && agl >= 1500)

		var targetSpeed float32
		if isJet {
			if agl < 1500 {
				targetSpeed = 180
			} else {
				targetSpeed = 210
			}
		} else { // prop/turboprop
			if agl < 500 {
				targetSpeed = 1.1 * v2(nav.Perf)
			} else if agl < 1000 {
				targetSpeed = 1.2 * v2(nav.Perf)
			} else {
				targetSpeed = 1.3 * v2(nav.Perf)
			}
		}

		// Make sure we're not trying to go faster than we're able to
		targetSpeed = min(targetSpeed, nav.FlightState.cruiseIAS(nav.Perf, temp))

		// And don't accelerate past any upcoming speed restrictions
		if nav.Speed.Restriction != nil {
			targetSpeed = nav.restrictedSpeed(nav.Speed.Restriction, targetSpeed)
		}
		if _, sr, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
			targetSpeed = nav.restrictedSpeed(sr, targetSpeed)
		}

		// However, don't let anything prevent us from taking off!
		targetSpeed = max(targetSpeed, v2(nav.Perf))

		return speedTarget{ias: targetSpeed, rate: 0.8 * maxAccel, initialClimbOver: over}
	}

	pendingDecel := false
	if onSID, sr, fix, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		naturalIAS, _ := nav.targetAltitudeIAS(temp)
		speed := nav.restrictedSpeed(sr, naturalIAS)
		if speed > nav.FlightState.IAS {
			// Accelerate immediately
			return speedTarget{ias: speed, rate: MaximumRate}
		} else if onSID {
			// SID: comply immediately
			return speedTarget{ias: speed, rate: MaximumRate}
		} else {
			// Geometric deceleration check: compute how fast we'd
			// need to decelerate to hit the target speed at the fix,
			// and start when that exceeds half our capability.
			dist, ok := nav.routeDistanceToFix(fix)
			eta := dist / nav.FlightState.GS * 3600 // seconds to fix
			if ok && eta > 0 {
				neededRate := (nav.FlightState.IAS - speed) / eta * 60 // kts per minute
				decelRate := nav.Perf.Rate.Decelerate * 30             // kts per minute (Decelerate is per 2s)
				if neededRate > decelRate/2 {
					return speedTarget{ias: speed, rate: MaximumRate}
				}
			}
			// Not time to decelerate yet; fall through to let the
			// 250kt/10k rule and approach speed still apply, but
			// prevent a previous fix's restriction and the default
			// altitude-based speed from accelerating past the
			// upcoming restriction.
			pendingDecel = true
		}
	}

	// Something from a previous waypoint; ignore it if we're cleared for the approach.
	if nav.Speed.Restriction != nil && !nav.Approach.Cleared {
		naturalIAS, _ := nav.targetAltitudeIAS(temp)
		spd := nav.restrictedSpeed(nav.Speed.Restriction, naturalIAS)
		if pendingDecel {
			spd = min(spd, nav.FlightState.IAS)
		}
		return speedTarget{ias: spd, rate: MaximumRate}
	}

	// Regulatory requirement: slow to 250 kts when descending through 10,000 feet
	// This must be checked BEFORE approach-cleared logic, as it's a hard speed limit
	if nav.FlightState.Altitude >= 10000 && targetAltitude < 10000 && nav.FlightState.IAS > 250 {
		// Consider slowing to 250; estimate how long until we'll reach 10k
		dalt := nav.FlightState.Altitude - 10000
		salt := dalt / (nav.Perf.Rate.Descent / 60) // seconds until we reach 10k

		dspeed := nav.FlightState.IAS - 250
		sspeed := dspeed / (nav.Perf.Rate.Decelerate / 2) // seconds to decelerate to 250

		if salt <= sspeed {
			// Time to slow down
			return speedTarget{ias: 250, rate: MaximumRate}
		} else {
			// Otherwise reduce in general but in any case don't speed up
			// again.
			ias, rate := nav.targetAltitudeIAS(temp)
			return speedTarget{ias: min(ias, nav.FlightState.IAS), rate: rate}
		}
	}

	// On a cleared approach inside 10 miles of its end, fly the approach
	// speed, and in the last half mile slow toward the landing speed.
	if fd, ok := nav.approachDistanceRemaining(); ok && fd < 10 {
		hdg := nav.Approach.Assigned.RunwayHeading(nav.FlightState.NmPerLongitude)
		var approachSpeed float32
		if arrivalMETAR != nil && arrivalMETAR.WindDir != nil { // METAR and non-VRB winds
			windSpeed := float32(arrivalMETAR.WindSpeed)
			windGust := windSpeed // default: no gust above steady wind
			if arrivalMETAR.WindGust != nil && *arrivalMETAR.WindGust > arrivalMETAR.WindSpeed {
				windGust = float32(*arrivalMETAR.WindGust)
			}
			approachSpeed = nav.Perf.ApproachSpeed(float32(*arrivalMETAR.WindDir), windSpeed, windGust, float32(hdg))
		} else {
			approachSpeed = nav.Perf.ApproachSpeed(float32(wxs.WindDirection()), wxs.WindSpeed(), 0, float32(hdg))
		}

		if fd < 0.5 {
			// Short final: slow down to landing speed.
			approachSpeed = math.Lerp(fd/0.5, nav.Perf.Speed.Landing, approachSpeed)
		}

		// Don't speed up after being been cleared to land.
		ias := min(approachSpeed, nav.FlightState.IAS)

		return speedTarget{ias: ias, rate: MaximumRate}
	}

	if nav.Approach.Cleared || nav.Approach.Abandoned {
		// Don't speed up if we're cleared, or were until the approach fell
		// through.
		return speedTarget{ias: nav.FlightState.IAS, rate: MaximumRate}
	}

	// Nothing assigned by the controller or the route, so set a target
	// based on the aircraft's altitude.
	ias, rate := nav.targetAltitudeIAS(temp)
	if rules == av.FlightRulesVFR && underBravo {
		ias = min(ias, 200)
	}
	if pendingDecel {
		ias = min(ias, nav.FlightState.IAS)
	}

	return speedTarget{ias: ias, rate: rate}
}

// approachDistanceRemaining returns how far an aircraft on a cleared
// approach has left to fly to its end. A visual cleared on a heading,
// including one the pilot hasn't turned to yet, has no route to measure
// along until it joins the course, so its distance is taken straight to the
// threshold, which ends its speed assignment and starts its slowdown no
// later than the path it flies would.
func (nav *Nav) approachDistanceRemaining() (float32, bool) {
	if nav.clearedForVisualOnHeading() {
		return math.NMDistance2LL(nav.FlightState.Position, nav.Approach.Assigned.Threshold), true
	}
	fd, err := nav.DistanceToEndOfApproach()
	return fd, err == nil
}

// Compute target airspeed for higher altitudes speed by lerping from 250
// to cruise speed based on altitude.
func (nav *Nav) targetAltitudeIAS(temp av.Temperature) (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute
	cruiseIAS := nav.FlightState.cruiseIAS(nav.Perf, temp)

	if nav.FlightState.Altitude <= 10000 {
		// 250kts under 10k.  We can assume a high acceleration rate for
		// departures when this kicks in at 1500' AGL given that VNav will
		// slow the rate of climb at that point until we reach the target
		// speed.
		return min(cruiseIAS, 250), 0.9 * maxAccel
	}

	x := math.Clamp((nav.FlightState.Altitude-10000)/(nav.Perf.Ceiling-10000), 0, 1)
	return math.Lerp(x, min(cruiseIAS, 280), cruiseIAS), 0.8 * maxAccel
}

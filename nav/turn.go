// nav/turn.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// Turn geometry: a closed-form prediction of the path of a turn as the
// flight model flies it, and the decisions it serves, of when to start the
// turn onto a course so as to roll out on it.

// TurnRadius returns the radius in nautical miles of a turn flown at the given
// true airspeed: a standard rate turn, or a wider one where holding that rate
// would take more bank than the type allows.
func TurnRadius(perf av.AircraftPerformance, tas float32) float32 {
	return standardTurnLimits(perf).radius(tas)
}

// PatternTurnRadius returns the radius in nautical miles of a turn in the
// traffic pattern flown at the given true airspeed.
func PatternTurnRadius(tas float32) float32 {
	return patternTurnLimits(tas).radius(tas)
}

// turnLimits returns how hard the aircraft turns at the given true
// airspeed: at PatternBankAngle on the legs of a traffic pattern, and
// otherwise at the standard rate.
func (nav *Nav) turnLimits(tas float32) turnLimits {
	if nav.inTrafficPattern() {
		return patternTurnLimits(tas)
	}
	return standardTurnLimits(nav.Perf)
}

// inTrafficPattern reports whether the aircraft is flying the legs of a VFR
// traffic pattern.
func (nav *Nav) inTrafficPattern() bool {
	if len(nav.Waypoints) == 0 {
		return false
	}
	switch nav.Waypoints[0].VFRPhase {
	case av.VFRPhaseUpwind, av.VFRPhaseCrosswind, av.VFRPhaseDownwind, av.VFRPhaseBase, av.VFRPhaseFinal:
		return true
	default:
		return false
	}
}

func TurnAngle(from, to math.MagneticHeading, turn av.TurnDirection) float32 {
	switch turn {
	case av.TurnLeft:
		return float32(math.NormalizeHeading(from - to))

	case av.TurnRight:
		return float32(math.NormalizeHeading(to - from))

	case av.TurnClosest:
		return math.Abs(math.HeadingDifference(from, to))

	default:
		panic("unhandled TurnDirection")
	}
}

// isTurnRight determines if the turn from currentHdg to targetHdg should
// be a right turn, given the specified av.TurnDirection.
func isTurnRight(currentHdg, targetHdg float32, turn av.TurnDirection) bool {
	switch turn {
	case av.TurnRight:
		return true
	case av.TurnLeft:
		return false
	default: // TurnClosest
		diff := targetHdg - currentHdg
		if diff > 180 {
			diff -= 360
		} else if diff < -180 {
			diff += 360
		}
		return diff > 0
	}
}

// perpRight returns unit vector perpendicular right (clockwise 90°) to heading.
// Uses vice convention: heading 0°=North, 90°=East, direction=[sin,cos]
func perpRight(hdg float32) [2]float32 {
	rad := math.Radians(hdg)
	return [2]float32{math.Cos(rad), -math.Sin(rad)}
}

// turnPath is a closed-form model of an upcoming turn: an optional straight
// lead, a constant-rate circular arc through the full turn angle, and a
// straight tail, all displaced linearly by the wind. The leads stand in for
// the time spent rolling into and out of the bank, sized so that the model's
// duration and endpoint match the tick-by-tick roll that FlightState.roll flies.
type turnPath struct {
	start   [2]float32 // position at the start of the turn, nm coordinates
	center  [2]float32 // center of the arc, nm coordinates
	h0      float32    // initial true heading, degrees
	s       float32    // +1 for a right turn, -1 for left
	omega   float32    // turn rate on the arc, deg/s
	radius  float32    // arc radius, nm
	arcDeg  float32    // total heading change, degrees
	t1, t2  float32    // start and end times of the arc, seconds
	dur     float32    // total duration including the roll-out lead, seconds
	tasNMps float32    // true airspeed, nm/s
	wind    [2]float32 // wind velocity, nm/s
}

// resolveTurnDirection pins down which way the turn from the aircraft's
// current heading to hdg goes. The predicates resolve this from the raw
// course before crabbing the target into the wind: the crab angle can push
// a target near 180 degrees away past it, which would flip a "closest"
// turn to the wrong side.
func (nav *Nav) resolveTurnDirection(hdg math.MagneticHeading, turn av.TurnDirection) av.TurnDirection {
	if isTurnRight(float32(nav.FlightState.Heading), float32(hdg), turn) {
		return av.TurnRight
	}
	return av.TurnLeft
}

// predictTurnPath models the turn from the aircraft's current state to the
// given magnetic heading. The turn is flown in still air at the current TAS
// with the wind's displacement added on afterward, which matches how
// FlightState.advance integrates position.
func (nav *Nav) predictTurnPath(hdg math.MagneticHeading, turn av.TurnDirection, wxs wx.Sample) turnPath {
	fs := &nav.FlightState
	tasKts := nav.TAS(wxs.Temperature())

	// Steady-state turn rate in deg/s at the given bank angle, signed with
	// the bank, where positive bank is into the turn.
	limits := nav.turnLimits(tasKts)
	rate := func(bank float32) float32 { return limits.rateAtBank(bank, tasKts) }

	arcDeg := TurnAngle(fs.Heading, hdg, turn)
	s := float32(util.Select(isTurnRight(float32(fs.Heading), float32(hdg), turn), 1, -1))
	maxBank := limits.bank
	rollRate := nav.Perf.Turn.MaxBankRate

	entryBank := s * fs.BankAngle
	if len(nav.Heading.Maneuvers) > 0 || nav.Heading.Hold != nil {
		// Maneuvers and holds turn at a flat StandardTurnRate without
		// maintaining FlightState.BankAngle, so don't trust its stale value.
		entryBank = 0
	}

	// Step the bank from its entry value up to max bank and from max bank
	// back to level the same way FlightState.roll does each tick, totalling
	// the heading turned through and the ticks each phase takes.
	var hIn, hOut float32
	var kIn, kOut int
	for b := entryBank; b < maxBank; kIn++ {
		b = min(b+rollRate, maxBank)
		hIn += rate(b)
	}
	for b := maxBank; b > 0; b -= rollRate {
		hOut += rate(b)
		kOut++
	}

	tp := turnPath{
		start:   math.LL2NM(fs.Position, fs.NmPerLongitude),
		h0:      float32(math.MagneticToTrue(fs.Heading, fs.MagneticVariation)),
		s:       s,
		arcDeg:  arcDeg,
		tasNMps: tasKts / 3600,
	}
	tp.wind = nav.groundWind(wxs)

	if hIn+hOut <= arcDeg {
		// The full bank is reached: the arc turns at the max-bank rate, and
		// the straight leads make up the difference between the ticks the
		// roll phases take and the heading they turn through.
		tp.omega = rate(maxBank)
		tp.t1 = float32(kIn) - hIn/tp.omega
		tp.t2 = tp.t1 + arcDeg/tp.omega
		tp.dur = tp.t2 + float32(kOut) - hOut/tp.omega
	} else {
		// A short turn that never reaches max bank: walk the bank up until
		// rolling back to level would complete the turn, then model the
		// whole turn as a single uniform arc over those ticks.
		hTurn, k := float32(0), 0
		for b := entryBank; ; {
			var hDown float32
			kDown := 0
			for bb := b; bb > 0; bb -= rollRate {
				hDown += rate(bb)
				kDown++
			}
			if hTurn+hDown >= arcDeg || b >= maxBank {
				k += kDown
				break
			}
			b = min(b+rollRate, maxBank)
			hTurn += rate(b)
			k++
		}
		tp.omega = arcDeg / float32(max(k, 1))
		tp.t1 = 0
		tp.t2 = float32(max(k, 1))
		tp.dur = tp.t2
	}

	// updateHeading turns first and FlightState.advance then moves a full
	// second along the new heading, so over a tick the aircraft flies the
	// heading it will have at the tick's end—a half-tick ahead of the
	// continuous arc. Start the arc half a second early to match.
	tp.t1 -= 0.5
	tp.t2 -= 0.5

	tp.radius = tp.tasNMps / math.Radians(tp.omega)
	tp.center = math.Add2f(tp.start,
		math.Add2f(math.Scale2f(math.SinCos(math.Radians(tp.h0)), tp.tasNMps*tp.t1),
			math.Scale2f(perpRight(tp.h0), tp.s*tp.radius)))
	return tp
}

// position returns the predicted position t seconds into the turn, in nm
// coordinates.
func (tp *turnPath) position(t float32) [2]float32 {
	var p [2]float32
	switch {
	case t <= tp.t1:
		p = math.Add2f(tp.start, math.Scale2f(math.SinCos(math.Radians(tp.h0)), tp.tasNMps*t))
	case t <= tp.t2:
		p = math.Sub2f(tp.center, math.Scale2f(perpRight(tp.heading(t)), tp.s*tp.radius))
	default:
		h1 := tp.h0 + tp.s*tp.arcDeg
		rollout := math.Sub2f(tp.center, math.Scale2f(perpRight(h1), tp.s*tp.radius))
		p = math.Add2f(rollout, math.Scale2f(math.SinCos(math.Radians(h1)), tp.tasNMps*(t-tp.t2)))
	}
	return math.Add2f(p, math.Scale2f(tp.wind, t))
}

// reachesLine reports whether the path comes within the capture tolerance
// of the line through p0 and p1, or crosses it, by the end of the turn.
func (tp *turnPath) reachesLine(p0, p1 [2]float32) bool {
	d0 := math.SignedPointLineDistance(tp.position(0), p0, p1)
	for t := float32(0); ; t++ {
		t = min(t, tp.dur)
		d := math.SignedPointLineDistance(tp.position(t), p0, p1)
		if math.Abs(d) < courseCaptureTolerance || math.Sign(d) != math.Sign(d0) {
			return true
		}
		if t == tp.dur {
			return false
		}
	}
}

// heading returns the predicted true heading t seconds into the turn, in
// degrees; it may be outside [0,360).
func (tp *turnPath) heading(t float32) float32 {
	turned := math.Clamp((t-tp.t1)*tp.omega, 0, tp.arcDeg)
	return tp.h0 + tp.s*turned
}

// maxFlyByAngle is the largest course change flown as a pure fly-by. The
// fly-by tangency distance radius*tan(turnAngle/2) diverges as the course
// change approaches 180 degrees--no single turn rolls out on a reciprocal
// course--and past 100 degrees it carries the turn miles from the fix.
// Capping the angle caps the anticipation at tan(50) ~ 1.19 turn radii,
// equivalently capping how far inside the corner the path passes at
// sec(50)-1 ~ 0.56 radii.
const maxFlyByAngle = 100

// minPredictedTurn is the smallest turn, in degrees, whose start the path
// prediction sets. A smaller one leaves the aircraft within a few feet of
// the outbound course for seconds before the fix. That is about as finely
// as float32 positions are resolved, so whether the predicted path crosses
// the course comes down to rounding, which differs between CPUs.
const minPredictedTurn = 2

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial: when the turn's path, predicted in closed form, would cross it.
// track indicates the outbound is a ground course rather than a heading
// that drifts with the wind.
func (nav *Nav) shouldTurnForOutbound(p math.Point2LL, hdg math.MagneticHeading, turn av.TurnDirection, track bool, wxs wx.Sample) bool {
	eta := nav.ETA(p)

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Don't run the path prediction when the aircraft is clearly too far
	// out for the turn to begin: the fly-by tangency distance for the
	// turn, with the course change capped at maxFlyByAngle, plus ten
	// seconds of travel for rolling into and out of the bank. Below the
	// cap this only bounds when the prediction is worth running--the
	// prediction sets the turn point. Above it the prediction fires as
	// soon as the aircraft is inside the bound, so the bound itself sets
	// the turn point, keeping the turn near the fix.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	tas := nav.TAS(wxs.Temperature())
	radius := nav.turnLimits(tas).radius(tas)
	anticipation := radius*math.Tan(math.Radians(min(turnAngle, maxFlyByAngle)/2)) + 10*nav.FlightState.GS/3600
	if math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude) > anticipation {
		return false
	}

	// Prespawn stops at that bound rather than paying for the prediction:
	// it runs 1800 steps per aircraft before anyone sees the scope, and the
	// bound is the same closed-form turn geometry the prediction refines --
	// above maxFlyByAngle it is already what sets the turn point.
	if nav.Prespawn {
		return true
	}

	// For a ground course, predict a turn to the heading that holds it as
	// a track; the aircraft's post-turn steering to the next fix is
	// wind-corrected the same way. A plain heading is flown as is and
	// drifts with the wind, and so does the predicted path.
	predHdg := hdg
	if track {
		predHdg = nav.headingForTrack(hdg, wxs)
	}
	if TurnAngle(nav.FlightState.Heading, predHdg, turn) < minPredictedTurn {
		// The ETA check above passes the fix.
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	hdgTrue := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0, math.SinCos(math.Radians(hdgTrue)))

	tp := nav.predictTurnPath(predHdg, nav.resolveTurnDirection(hdg, turn), wxs)

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position,
		nav.FlightState.NmPerLongitude), p0, p1)

	// Start the turn once the predicted path would reach the far side of
	// the outbound course.
	for t := float32(1); ; t++ {
		t = min(t, tp.dur)
		d := math.SignedPointLineDistance(tp.position(t), p0, p1)
		if math.Sign(d) != math.Sign(initialDist) {
			return true
		}
		if t == tp.dur {
			return false
		}
	}
}

// courseCaptureTolerance is how close to a course an aircraft turning to
// join it has to get to be considered established on it, in nm.
const courseCaptureTolerance = 0.2

type turnToInterceptResult int

const (
	turnToInterceptWait turnToInterceptResult = iota
	turnToInterceptTurn
	turnToInterceptCorrectableOvershoot
	turnToInterceptMajorOvershoot
)

// Given a point and a course through it, indicates when the aircraft should start the turn to join
// that course, classifying how the turn's path, predicted in closed form, would meet it. The
// returned bool is false if the aircraft is diverging from the course; the result then describes
// how far off the course the aircraft is pointed rather than how it would roll out on it. Callers
// that have already committed to joining use that to recover; ones still waiting for the intercept
// keep flying their heading.
func (nav *Nav) shouldTurnToIntercept(p0 math.Point2LL, hdg math.MagneticHeading, turn av.TurnDirection,
	wxs wx.Sample) (result turnToInterceptResult, reaches bool) {
	p0nm := math.LL2NM(p0, nav.FlightState.NmPerLongitude)
	hdgTrue := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0nm, math.SinCos(math.Radians(hdgTrue)))

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude), p0nm, p1)
	eta := math.Abs(initialDist) / nav.FlightState.GS * 3600 // in seconds
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if eta < 2 && turnAngle < 4 {
		// Just in case, start the turn; for larger turn angles, fall
		// through to see if this is correctable.
		return turnToInterceptTurn, true
	}

	// As above, don't consider starting the turn if we're far away.
	if turnAngle < eta {
		return turnToInterceptWait, true
	}

	// Allow heading tolerance to account for the crab angle needed in crosswind.
	// Base tolerance of 10 degrees plus the calculated crab angle.
	v := math.Scale2f(math.SinCos(math.Radians(hdgTrue)), nav.FlightState.GS)
	headingTolerance := 10 + math.Abs(wxs.Deflection(v))

	// The radial is a ground course, so predict a turn to the heading that
	// holds it as a track; otherwise in a crosswind the predicted path
	// rolls out and immediately drifts back off the radial.
	tp := nav.predictTurnPath(nav.headingForTrack(hdg, wxs), nav.resolveTurnDirection(hdg, turn), wxs)

	// Since the predicted path holds the radial's course as a track after
	// rolling out, its final distance from the radial is where the aircraft
	// would settle if it turned now.
	endDist := math.SignedPointLineDistance(tp.position(tp.dur), p0nm, p1)

	for t := float32(1); ; t++ {
		t = min(t, tp.dur)
		d := math.SignedPointLineDistance(tp.position(t), p0nm, p1)
		if math.Abs(d) < 0.02 || math.Sign(d) != math.Sign(initialDist) {
			// Just past the ideal turn point, the heading still to be
			// turned at the crossing rises steeply—about 15 degrees in the
			// tick after an exact tangency—so classifying by that heading
			// alone can skip right over "start the turn". A path that ends
			// up settled on the radial is a clean intercept no matter the
			// angle it first crossed at: settling within the capture
			// tolerance is a capture.
			if math.Abs(endDist) < courseCaptureTolerance {
				return turnToInterceptTurn, true
			}
			predicted := math.TrueToMagnetic(math.TrueHeading(math.NormalizeHeading(tp.heading(t))),
				nav.FlightState.MagneticVariation)
			delta := math.HeadingDifference(hdg, predicted)
			if delta < headingTolerance {
				return turnToInterceptTurn, true
			} else if delta < headingTolerance+30 {
				return turnToInterceptCorrectableOvershoot, true
			} else {
				return turnToInterceptMajorOvershoot, true
			}
		}
		if t == tp.dur {
			break
		}
	}

	// The predicted path rolls out without reaching the radial; it then
	// holds the radial's course as a track, so its distance from it no
	// longer changes. If it ends up farther from the radial than the
	// aircraft is now, the aircraft has overshot and is diverging.
	if math.Abs(endDist) > math.Abs(initialDist) {
		delta := math.HeadingDifference(hdg, nav.FlightState.Heading)
		if math.Abs(endDist) < 0.25 && delta < 30 {
			// Near enough the course and its heading to just take it up.
			return turnToInterceptTurn, true
		}
		if delta < headingTolerance+30 {
			return turnToInterceptCorrectableOvershoot, false
		}
		return turnToInterceptMajorOvershoot, false
	}
	return turnToInterceptWait, true
}

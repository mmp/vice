// sim/commands.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/wx"
)

func (s *Sim) AssignAltitude(tcw TCW, callsign av.ADSBCallsign, altitude int, afterSpeed bool, delayReduction time.Duration) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignAltitude(altitude, afterSpeed, s.State.SimTime, delayReduction)
		})
}

type HeadingArgs struct {
	TCW            TCW
	ADSBCallsign   av.ADSBCallsign
	Heading        int
	Present        bool
	LeftDegrees    int
	RightDegrees   int
	Turn           av.TurnDirection
	DelayReduction time.Duration
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(hdg.TCW, hdg.ADSBCallsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if hdg.Present {
				return ac.FlyPresentHeading(s.State.SimTime, hdg.DelayReduction)
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees, s.State.SimTime, hdg.DelayReduction)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees, s.State.SimTime, hdg.DelayReduction)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn, s.State.SimTime, hdg.DelayReduction)
			}
		})
}

func (s *Sim) AssignMach(tcw TCW, callsign av.ADSBCallsign, mach float32, afterAltitude bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			temp := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime.Time()).Temperature()
			return ac.AssignMach(mach, afterAltitude, temp)
		})
}

func (s *Sim) AssignSpeed(tcw TCW, callsign av.ADSBCallsign, sr *av.SpeedRestriction, afterAltitude bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignSpeed(sr, afterAltitude)
		})
}

func (s *Sim) AssignSpeedUntil(tcw TCW, callsign av.ADSBCallsign, sr *av.SpeedRestriction, until *av.SpeedUntil) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignSpeedUntil(sr, until)
		})
}

func (s *Sim) AssignCompoundSpeed(tcw TCW, callsign av.ADSBCallsign, segments []av.CompoundSpeedSegment) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignCompoundSpeed(segments)
		})
}

// AssignConditional installs a deferred LV/RC action on the aircraft's
// nav state. Fires silently when sim.updateState observes the altitude
// trigger. Returns an UnableIntent if the trigger is not reachable from
// the aircraft's current vertical state; the outer error is reserved for
// lookup/authorization failures.
func (s *Sim) AssignConditional(tcw TCW, callsign av.ADSBCallsign,
	kind nav.ConditionalKind, altitude float32, action nav.ConditionalAction) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if !triggerReachable(ac, kind, altitude) {
				return av.MakeUnableIntent("unable. {alt} is out of our climb/descent path.", altitude)
			}
			ac.Nav.PendingConditionalCommand = &nav.PendingConditionalCommand{
				Kind:     kind,
				Altitude: altitude,
				Action:   action,
			}
			return av.ConditionalCommandIntent{
				Kind:     kind,
				Altitude: altitude,
				Action:   action,
			}
		})
}

// triggerReachable reports whether a LV/RC trigger altitude is
// reasonably reachable from the aircraft's current vertical state,
// allowing the controller command to be accepted.
//
// For ConditionalLeaving: accepted if the aircraft is within 500 ft of
// the trigger (so "leaving 3,000" works even for an aircraft at 3,050),
// or if the trigger lies between current altitude and assigned target.
//
// For ConditionalReaching: accepted if the trigger lies between current
// altitude and assigned target, or (if no target assigned) the aircraft
// is within 500 ft of the trigger.
func triggerReachable(ac *Aircraft, kind nav.ConditionalKind, trigger float32) bool {
	cur := ac.Nav.FlightState.Altitude
	target := ac.Nav.Altitude.Assigned
	diff := math.Abs(cur - trigger)
	switch kind {
	case nav.ConditionalLeaving:
		if diff <= 500 {
			return true
		}
		if target == nil {
			return false
		}
		return betweenAlt(trigger, cur, *target)
	case nav.ConditionalReaching:
		if target == nil {
			return diff <= 500
		}
		return betweenAlt(trigger, cur, *target)
	}
	return false
}

// betweenAlt reports whether v lies between a and b (inclusive), in
// either ordering.
func betweenAlt(v, a, b float32) bool {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return v >= lo && v <= hi
}

func (s *Sim) MaintainSlowestPractical(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) MaintainPresentSpeed(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.MaintainPresentSpeed()
		})
}

func (s *Sim) SaySpeed(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			temp := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime.Time()).Temperature()
			return ac.SaySpeed(temp)
		})
}

func (s *Sim) SayIndicatedSpeed(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.SayIndicatedSpeed()
		})
}

func (s *Sim) SayMach(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			temp := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime.Time()).Temperature()
			return ac.SayMach(temp)
		})
}

func (s *Sim) SayAltitude(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) ExpediteDescentThrough(tcw TCW, callsign av.ADSBCallsign, alt float32) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpediteDescentThrough(alt)
		})
}

func (s *Sim) ExpediteClimbThrough(tcw TCW, callsign av.ADSBCallsign, alt float32) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpediteClimbThrough(alt)
		})
}

func (s *Sim) GoodRateDescent(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.GoodRateDescent()
		})
}

func (s *Sim) GoodRateClimb(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.GoodRateClimb()
		})
}

func (s *Sim) GoodRateThrough(tcw TCW, callsign av.ADSBCallsign, alt float32) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.GoodRateThrough(alt)
		})
}

func (s *Sim) ExpectDirect(tcw TCW, callsign av.ADSBCallsign, fix string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpectDirect(fix)
		})
}

func (s *Sim) DirectFix(tcw TCW, callsign av.ADSBCallsign, fix string, turn av.TurnDirection, delayReduction time.Duration) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DirectFix(fix, turn, s.State.SimTime, delayReduction)
		})
}

func (s *Sim) HoldAtFix(tcw TCW, callsign av.ADSBCallsign, fix string, hold *av.Hold) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.HoldAtFix(fix, hold)
		})
}

func (s *Sim) DepartFixDirect(tcw TCW, callsign av.ADSBCallsign, fixa string, fixb string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(tcw TCW, callsign av.ADSBCallsign, fix string, heading int) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(tcw TCW, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, sr *av.SpeedRestriction) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CrossFixAt(fix, ar, sr)
		})
}

func (s *Sim) CrossDistanceFromFixAt(tcw TCW, callsign av.ADSBCallsign, fix string, dist float32,
	dir math.CardinalOrdinalDirection, ar *av.AltitudeRestriction,
	sr *av.SpeedRestriction) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CrossDistanceFromFixAt(fix, dist, dir, ar, sr)
		})
}

func (s *Sim) CrossDMEAt(tcw TCW, callsign av.ADSBCallsign, dist float32, ar *av.AltitudeRestriction,
	sr *av.SpeedRestriction) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CrossDMEAt(dist, ar, sr)
		})
}

func (s *Sim) AfterFixSpeed(tcw TCW, callsign av.ADSBCallsign, fix string, sr *av.SpeedRestriction) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AfterFixSpeed(fix, sr)
		})
}

func (s *Sim) AfterFixAltitude(tcw TCW, callsign av.ADSBCallsign, fix string, alt int) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AfterFixAltitude(fix, float32(alt))
		})
}

func (s *Sim) AtFixCleared(tcw TCW, callsign av.ADSBCallsign, fix, approach string, straightIn bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AtFixCleared(fix, approach, straightIn)
		})
}

func (s *Sim) AtFixIntercept(tcw TCW, callsign av.ADSBCallsign, fix string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AtFixIntercept(fix, s.lg)
		})
}

func (s *Sim) ClimbViaSID(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ClimbViaSID(s.State.SimTime)
		})
}

func (s *Sim) DescendViaSTAR(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DescendViaSTAR(s.State.SimTime)
		})
}

func (s *Sim) ContactTower(tcw TCW, callsign av.ADSBCallsign, freq av.Frequency) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			result, ok := ac.ContactTower(s.lg, freq)
			if ok {
				ac.ControllerFrequency = "_TOWER"
			}
			return result
		})
}

// ATISCommand handles the controller telling a pilot the current ATIS letter.
// If the aircraft already reported the correct ATIS, no readback is needed.
// Otherwise the pilot responds with "we'll pick up (letter)".
func (s *Sim) ATISCommand(tcw TCW, callsign av.ADSBCallsign, letter string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if ac.ReportedATIS == letter {
				return nil
			}
			ac.ReportedATIS = letter
			return av.ATISIntent{Letter: letter}
		})
}

// TrafficAdvisory handles controller-issued traffic advisories.
// Command format: TRAFFIC/oclock/miles/altitude (e.g., TRAFFIC/10/4/30 for 10 o'clock, 4 miles, 3000 ft).
// An optional trailing /VISSEP indicates the other traffic has us in sight and will maintain visual separation;
// in that case the pilot simply acknowledges without reporting the traffic in sight.
func (s *Sim) TrafficAdvisory(tcw TCW, callsign av.ADSBCallsign, command string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Parse the command: TRAFFIC/oclock/miles/altitude[/VISSEP]
	parts := strings.Split(command, "/")
	if len(parts) != 4 && len(parts) != 5 {
		return nil, ErrInvalidCommandSyntax
	}
	otherMaintainsVisual := false
	if len(parts) == 5 {
		if parts[4] != "VISSEP" {
			return nil, ErrInvalidCommandSyntax
		}
		otherMaintainsVisual = true
	}

	oclock, err := strconv.Atoi(parts[1])
	if err != nil || oclock < 1 || oclock > 12 {
		return nil, ErrInvalidCommandSyntax
	}

	miles, err := strconv.Atoi(parts[2])
	if err != nil || miles < 1 {
		return nil, ErrInvalidCommandSyntax
	}

	trafficAlt, err := strconv.Atoi(parts[3])
	if err != nil {
		return nil, ErrInvalidCommandSyntax
	}
	// trafficAlt is encoded altitude (in 100s of feet)
	trafficAltFeet := float32(trafficAlt * 100)

	return s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error { return nil },
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if otherMaintainsVisual {
				return av.TrafficAdvisoryIntent{Response: av.TrafficResponseAcknowledged}
			}
			return s.handleTrafficAdvisory(ac, oclock, miles, trafficAltFeet)
		})
}

// handleTrafficAdvisory determines the pilot response to a traffic advisory based on:
//  1. Weather conditions at the nearest reporting station (IMC -> "we're in IMC")
//  2. Presence of traffic (if no traffic in area -> "looking")
//  3. Pilot see-probability derived from METAR effective visual range, with a
//     relative-altitude boost/penalty (above against sky, below against ground)
func (s *Sim) handleTrafficAdvisory(ac *Aircraft, oclock int, miles int, trafficAltFeet float32) av.CommandIntent {
	// A fresh TRAFFIC call supersedes any earlier "looking" event still
	// queued for this aircraft (possibly for a different target); the
	// enqueue helper will re-add one if the pilot ends up looking.
	s.cancelFutureTrafficInSight(ac.ADSBCallsign)

	nearestMETAR, nearestElev := s.nearestMETAR(ac.Position())
	if nearestMETAR.ICAO != "" && !nearestMETAR.IsVMC() {
		ac.OfferedVisualSeparation = false
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseIMC}
	}

	// Convert o'clock to heading offset from aircraft heading
	// 12 o'clock = 0 degrees, 3 o'clock = 90 degrees, etc.
	oclockHeading := math.MagneticHeading((oclock % 12) * 30) // 0, 30, 60, 90... 330
	trafficHeading := math.NormalizeHeading(ac.Heading() + oclockHeading)

	// Calculate the approximate position of the reported traffic
	nmPerLong := ac.NmPerLongitude()
	magVar := ac.MagneticVariation()
	trafficPos := math.Offset2LL(ac.Position(), math.MagneticToTrue(trafficHeading, magVar), float32(miles), nmPerLong)

	// Search for actual traffic near the reported position
	// Tolerance: +/- 2 miles horizontal, +/- 1000 feet vertical
	const horizontalToleranceNM = 2.0
	const verticalToleranceFeet = 1000.0

	var trafficFound av.ADSBCallsign
	trafficDist := float32(999999)
	for cs, other := range s.Aircraft {
		if cs == ac.ADSBCallsign {
			continue // Skip self
		}

		dist := math.NMDistance2LL(trafficPos, other.Position())
		altDiff := math.Abs(other.Altitude() - trafficAltFeet)

		if dist <= horizontalToleranceNM && altDiff <= verticalToleranceFeet && dist < trafficDist {
			trafficFound = cs
			trafficDist = dist
		}
	}

	if trafficFound == "" {
		// No traffic found - respond "looking"
		ac.OfferedVisualSeparation = false
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
	}

	// Base probability from METAR-derived effective visual range at the pilot's AGL.
	altAGL := max(ac.Altitude()-nearestElev, 0)
	seeProb := pilotSeeProb(nearestMETAR.EffectiveVisualRange(altAGL), float32(miles))

	// Only apply altitude modulation + floor clamp if the target is within
	// effective visual range; otherwise the pilot simply can't see it.
	if seeProb > 0 {
		// Traffic above is easier to see against sky; below, harder against ground.
		if trafficAltFeet > ac.Altitude()+500 {
			seeProb *= 1.3
		} else if trafficAltFeet < ac.Altitude()-500 {
			seeProb *= 0.7
		}
		seeProb = max(0.2, min(0.95, seeProb))
	}

	if s.Rand.Float32() < seeProb {
		ac.TrafficInSight = true
		ac.TrafficInSightCallsign = trafficFound
		ac.TrafficInSightTime = s.State.SimTime
		ac.OfferedVisualSeparation = s.Rand.Float32() < 0.3
		return av.TrafficAdvisoryIntent{
			Response:               av.TrafficResponseTrafficSeen,
			WillMaintainSeparation: ac.OfferedVisualSeparation,
		}
	}

	// "Looking" - schedule possible delayed traffic-in-sight call
	s.enqueueFutureTrafficInSight(ac.ADSBCallsign, trafficFound)
	ac.OfferedVisualSeparation = false
	return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
}

// nearestMETAR returns the METAR and airport elevation for the reporting
// station closest to pos. Returns a zero METAR if no stations are available.
func (s *Sim) nearestMETAR(pos math.Point2LL) (wx.METAR, float32) {
	var nearest wx.METAR
	var elev float32
	closestDist := float32(999999)
	for _, metar := range s.State.METAR {
		ap, ok := s.State.Airports[metar.ICAO]
		if !ok {
			continue
		}
		dist := math.NMDistance2LL(pos, ap.Location)
		if dist >= closestDist {
			continue
		}
		closestDist = dist
		nearest = metar
		if faa, ok := av.DB.Airports[metar.ICAO]; ok {
			elev = float32(faa.Elevation)
		} else {
			elev = 0
		}
	}
	return nearest, elev
}

// MaintainVisualSeparation handles "maintain visual separation from the traffic" command.
// The aircraft should have recently reported traffic in sight.
func (s *Sim) MaintainVisualSeparation(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error { return nil },
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// Check if aircraft has traffic in sight (within last 60 seconds)
			if ac.TrafficInSight && s.State.SimTime.Sub(ac.TrafficInSightTime) < 60*time.Second {
				ac.OfferedVisualSeparation = false
				return av.VisualSeparationIntent{}
			}
			// If they don't have traffic in sight, they can't maintain visual separation
			return av.MakeUnableIntent("unable, we don't have the traffic")
		})
}

// CautionWakeTurbulence handles "caution wake turbulence" advisories.
func (s *Sim) CautionWakeTurbulence(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchAircraftCommand(tcw, callsign,
		nil,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return av.CautionWakeTurbulenceIntent{}
		})
}

// ApproveVisualSeparation handles "approved" after a pilot has volunteered
// to maintain visual separation from traffic called by the controller.
func (s *Sim) ApproveVisualSeparation(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error { return nil },
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if ac.OfferedVisualSeparation &&
				ac.TrafficInSight &&
				s.State.SimTime.Sub(ac.TrafficInSightTime) < 60*time.Second {
				ac.OfferedVisualSeparation = false
			}
			return nil
		})
}

func (s *Sim) AltitudeOurDiscretion(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AltitudeOurDiscretion()
		})
}

func (s *Sim) RadarServicesTerminated(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			s.enqueueTransponderChange(ac.ADSBCallsign, 0o1200, ac.Mode)

			// Leave our frequency
			s.cancelFutureFrequencyChange(ac.ADSBCallsign)
			ac.ControllerFrequency = ""

			return av.ContactIntent{
				Type: av.ContactRadarTerminated,
			}
		})
}

func (s *Sim) GoAhead(tcw TCW, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if !ac.WaitingForGoAhead {
				return nil
			}

			ac.WaitingForGoAhead = false

			s.enqueuePilotTransmission(ac.ADSBCallsign, s.State.PrimaryPositionForTCW(tcw), PendingTransmissionFlightFollowingFull)

			return nil
		})
	return err
}

// SayAgain triggers a pilot saying "say again" in response to an unclear command.
// Returns the spoken text for TTS synthesis and the callsign to use for voice selection.
func (s *Sim) SayAgain(tcw TCW, callsign av.ADSBCallsign) (av.ADSBCallsign, string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	tr := av.MakeReadbackTransmission("say again for")
	s.postReadbackTransmission(callsign, *tr, tcw)

	// Return spoken text with callsign suffix for TTS synthesis
	if suffix := s.readbackCallsignSuffix(callsign, tcw); suffix != nil {
		tr.Merge(suffix)
	}
	return callsign, tr.Spoken(s.Rand), nil
}

// SayNotCleared is called when the controller issues "contact tower" to an arrival
// aircraft that hasn't been cleared for an approach. The pilot responds that they
// haven't received approach clearance.
func (s *Sim) SayNotCleared(tcw TCW, callsign av.ADSBCallsign) (av.ADSBCallsign, string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	tr := av.MakeReadbackTransmission("we haven't been cleared for an approach")
	s.postReadbackTransmission(callsign, *tr, tcw)

	// Return spoken text with callsign suffix for TTS synthesis
	if suffix := s.readbackCallsignSuffix(callsign, tcw); suffix != nil {
		tr.Merge(suffix)
	}
	return callsign, tr.Spoken(s.Rand), nil
}

// SayAgainCommand returns an intent for when STT partially parsed a command but
// couldn't extract the argument. The pilot will ask the controller to repeat the
// specific part of the clearance.
func (s *Sim) SayAgainCommand(tcw TCW, callsign av.ADSBCallsign, commandType string) (av.CommandIntent, error) {
	var cmdType av.SayAgainCommandType
	switch commandType {
	case "HEADING":
		cmdType = av.SayAgainHeading
	case "ALTITUDE":
		cmdType = av.SayAgainAltitude
	case "SPEED":
		cmdType = av.SayAgainSpeed
	case "APPROACH":
		cmdType = av.SayAgainApproach
	case "TURN":
		cmdType = av.SayAgainTurn
	case "SQUAWK":
		cmdType = av.SayAgainSquawk
	case "FIX":
		cmdType = av.SayAgainFix
	default:
		return nil, ErrInvalidCommandSyntax
	}
	return av.SayAgainIntent{CommandType: cmdType}, nil
}

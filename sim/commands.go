// sim/commands.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
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

func (s *Sim) AtFixCleared(tcw TCW, callsign av.ADSBCallsign, fix, approach string, straightIn bool, delayReduction time.Duration) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AtFixCleared(fix, approach, s.State.SimTime, delayReduction, straightIn)
		})
}

func (s *Sim) AtFixIntercept(tcw TCW, callsign av.ADSBCallsign, fix string, delayReduction time.Duration) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AtFixIntercept(fix, s.State.SimTime, delayReduction)
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
func (s *Sim) TrafficAdvisory(tcw TCW, callsign av.ADSBCallsign, oclock, miles, trafficAlt int, altUnknown, otherMaintainsVisual bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error { return nil },
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// A fresh traffic call supersedes any earlier unresolved advisory or volunteered
			// separation offer.
			s.cancelFutureTrafficCheck(ac.ADSBCallsign)
			ac.clearOfferedToMaintainSeparation()

			if otherMaintainsVisual {
				return av.TrafficAdvisoryIntent{Response: av.TrafficResponseAcknowledged}
			}
			return s.handleTrafficAdvisory(ac, oclock, miles, trafficAlt, altUnknown)
		})
}

// handleTrafficAdvisory determines the pilot response to a traffic advisory based on:
//  1. Weather conditions at the nearest reporting station (IMC -> "we're in IMC")
//  2. Presence of traffic (if no traffic in area -> "looking")
//  3. Pilot see-probability derived from METAR effective visual range, with a
//     relative-altitude boost/penalty (above against sky, below against ground)
func (s *Sim) handleTrafficAdvisory(ac *Aircraft, oclock int, miles, callAlt int, altUnknown bool) av.CommandIntent {
	nearestMETAR, _ := s.nearestMETAR(ac.Position())
	if nearestMETAR.ICAO != "" && !nearestMETAR.IsVMC() {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseIMC}
	}

	// Convert o'clock to heading offset from aircraft heading
	// 12 o'clock = 0 degrees, 3 o'clock = 90 degrees, etc.
	oclockHeading := math.MagneticHeading((oclock % 12) * 30) // 0, 30, 60, 90... 330
	acHeading := math.NormalizeHeading(ac.Heading() + oclockHeading)

	// Calculate the approximate position of the reported traffic
	nmPerLong := ac.NmPerLongitude()
	magVar := ac.MagneticVariation()
	callPos := math.Offset2LL(ac.Position(), math.MagneticToTrue(acHeading, magVar), float32(miles), nmPerLong)

	// Search for actual traffic near the reported position
	// Tolerance: +/- 2 miles horizontal, +/- 1000 feet vertical
	const horizontalToleranceNM = 2.0
	const verticalToleranceFeet = 1000.0

	traffic := func() *Aircraft {
		var best *Aircraft
		for cs, candidate := range s.Aircraft {
			if cs == ac.ADSBCallsign {
				continue // Skip self
			}

			// Use altitude as a strict cutoff selector. Skipped when the
			// controller said "altitude unknown" — pick by position alone.
			if !altUnknown {
				altDiff := math.Abs(candidate.Altitude() - float32(callAlt))
				if altDiff > verticalToleranceFeet {
					continue
				}
			}

			// Distance must be in range; then we take the closest if there are multiple
			if dist := math.NMDistance2LL(callPos, candidate.Position()); dist < horizontalToleranceNM {
				if best == nil {
					best = candidate
				} else if dist < math.NMDistance2LL(callPos, best.Position()) {
					best = candidate
				}
			}
		}
		return best
	}()

	if traffic == nil {
		// Nothing there; the pilot will report that they're looking but we won't re-check in the
		// future since there's no identified aircraft to check...
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
	}

	if s.trafficIsVisible(ac, traffic) {
		sighting := ac.RecordSighting(traffic.ADSBCallsign, s.State.SimTime)
		sighting.OfferedToMaintainSeparation = s.Rand.Float32() < 0.3
		return av.TrafficAdvisoryIntent{
			Response:               av.TrafficResponseTrafficSeen,
			WillMaintainSeparation: sighting.OfferedToMaintainSeparation,
		}
	} else {
		// "Looking" - schedule a recheck
		s.enqueueFutureTrafficCheck(ac.ADSBCallsign, traffic.ADSBCallsign)
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
	}
}

func (s *Sim) trafficIsVisible(ac, traffic *Aircraft) bool {
	nearestMETAR, nearestElev := s.nearestMETAR(ac.Position())
	if nearestMETAR.ICAO != "" && !nearestMETAR.IsVMC() {
		return false
	}

	// Base probability from METAR-derived effective visual range at the pilot's AGL.
	trafficAltAGL := max(traffic.Altitude()-nearestElev, 0)
	acAltAGL := max(ac.Altitude()-nearestElev, 0)
	dist := math.NMDistance2LL(ac.Position(), traffic.Position())
	p := pilotSeeProb(nearestMETAR.EffectiveVisualRange(acAltAGL, trafficAltAGL), dist)

	// Only apply altitude modulation + floor clamp if the target is within
	// effective visual range; otherwise the pilot simply can't see it.
	if p > 0 {
		// Traffic above is easier to see against sky; below, harder against ground.
		if traffic.Altitude() > ac.Altitude()+500 {
			p *= 1.3
		} else if traffic.Altitude() < ac.Altitude()-500 {
			p *= 0.7
		}
		p = math.Clamp(p, 0.2, 0.95)
	}

	return s.Rand.Float32() < p
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
		elev = float32(av.DB.Airports[metar.ICAO].Elevation)
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
			if sighting := ac.RecentSighting(s.State.SimTime, trafficSightingMaxAge); sighting != nil {
				ac.clearOfferedToMaintainSeparation()
				sighting.MaintainingVisualSeparation = true
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
			for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
				sighting := &ac.SeenTraffic[i]
				if s.State.SimTime.Sub(sighting.SightedTime) > trafficSightingMaxAge {
					continue
				}
				if !sighting.OfferedToMaintainSeparation {
					continue
				}
				ac.clearOfferedToMaintainSeparation()
				sighting.MaintainingVisualSeparation = true
				break
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

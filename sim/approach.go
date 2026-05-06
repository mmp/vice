// sim/approach.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/util"
)

// AirportInSightInquiry handles the bare "AP" command. The controller asks
// "do you have the field in sight?" without specifying a direction; the
// pilot's response depends on weather, ceiling, and distance to the airport —
// no o'clock/bearing validation is performed.
func (s *Sim) AirportInSightInquiry(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if ac.FieldInSight || ac.RequestedVisualApproach || ac.Nav.Approach.Cleared {
				s.cancelFutureFieldCheck(ac.ADSBCallsign)
				return av.LookForFieldFound
			}
			return s.handleAirportAdvisory(ac, 0, 0)
		})
}

// TrafficInSightInquiry handles the bare "TRAFFIC" command — the controller
// asking "do you have the traffic?" without restating the call. If a queued
// FutureTrafficCheck still references a live aircraft, the pilot re-checks
// that target immediately. Otherwise the pilot looks for a single nearby
// aircraft in front and within tight tolerances; if exactly one matches, the
// pilot reports it in sight, otherwise the pilot asks where the traffic was.
func (s *Sim) TrafficInSightInquiry(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return s.handleTrafficInSightInquiry(ac)
		})
}

// handleTrafficInSightInquiry implements the bare TRAFFIC inquiry resolution.
// Caller must hold the sim mutex.
func (s *Sim) handleTrafficInSightInquiry(ac *Aircraft) av.CommandIntent {
	// If there is a queued FutureTrafficCheck for this aircraft, re-evaluate
	// visibility.
	if f, ok := s.FutureTrafficChecks[ac.ADSBCallsign]; ok {
		traffic, ok := s.Aircraft[f.TrafficCallsign]
		if !ok {
			// Traffic is gone; drop the entry and fall through to the "in front of us" search
			// below.
			delete(s.FutureTrafficChecks, ac.ADSBCallsign)
		} else if s.trafficIsVisible(ac, traffic) {
			ac.RecordSighting(f.TrafficCallsign, s.State.SimTime)
			delete(s.FutureTrafficChecks, ac.ADSBCallsign)
			return av.TrafficAdvisoryIntent{Response: av.TrafficResponseTrafficSeen}
		} else {
			return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
		}
	}

	// The geometric fallback below trusts the call when one nearby aircraft
	// matches; in IMC the pilot can't possibly see it, so report IMC instead.
	if metar, _ := s.nearestMETAR(ac.Position()); metar.ICAO != "" && !metar.IsVMC() {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseIMC}
	}

	// Look for a single aircraft within 3 NM, ±1000 ft, and ±45 degrees of the aircraft's nose.
	matches := slices.Collect(util.FilterSeq(maps.Values(s.Aircraft), func(candidate *Aircraft) bool {
		const horizontalNM = 3
		const verticalFeet = 1000
		const bearingTolerance = 45

		bearing := math.TrueToMagnetic(
			math.Heading2LL(ac.Position(), candidate.Position(), ac.NmPerLongitude()),
			ac.MagneticVariation())
		return candidate.ADSBCallsign != ac.ADSBCallsign &&
			math.Abs(candidate.Altitude()-ac.Altitude()) < verticalFeet &&
			math.NMDistance2LL(ac.Position(), candidate.Position()) < horizontalNM &&
			math.HeadingDifference(ac.Heading(), bearing) < bearingTolerance
	}))

	if len(matches) == 1 {
		ac.RecordSighting(matches[0].ADSBCallsign, s.State.SimTime)
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseTrafficSeen}
	}
	return av.TrafficAdvisoryIntent{Response: av.TrafficResponseWhereWasIt}
}

// AirportAdvisory handles the AP/{oclock}/{miles} command. The controller tells the
// pilot where to look for the airport: "airport, {oclock} o'clock, {miles} miles".
// The pilot responds with "field in sight", "looking", or an IMC indication.
func (s *Sim) AirportAdvisory(tcw TCW, callsign av.ADSBCallsign, oclock, miles int) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// If the pilot already has the field in sight — or is already
			// cleared for an approach — just confirm.
			if ac.FieldInSight || ac.RequestedVisualApproach || ac.Nav.Approach.Cleared {
				return av.LookForFieldFound
			}

			return s.handleAirportAdvisory(ac, oclock, miles)
		})
}

// handleAirportAdvisory determines the pilot's response to an AP command.
// It reuses checkAirportVisibility for METAR/VMC/ceiling/distance/bearing
// checks, then layers on AP-specific logic (o'clock validation, probability,
// looking delay).
func (s *Sim) handleAirportAdvisory(ac *Aircraft, oclock int, miles int) av.CommandIntent {
	// A fresh AP call supersedes any earlier "looking" event still queued
	// for this aircraft; the enqueue helper will re-add one if appropriate.
	s.cancelFutureFieldCheck(ac.ADSBCallsign)

	// Use the shared eligibility check for VMC, ceiling, range, and bearing.
	elig := s.checkAirportVisibility(ac)
	if !elig.FieldInSight {
		if elig.Reason == visualEligibilityIMC {
			return av.LookForFieldLookingIMC
		}
		s.enqueueFutureFieldCheck(ac.ADSBCallsign)
		if elig.Reason == visualEligibilityObscured {
			return av.LookForFieldLookingObscured
		}
		return av.LookForFieldLooking
	}

	// Validate the controller's o'clock direction against the actual bearing.
	// oclock == 0 means the controller didn't give a direction (bare "AP"
	// inquiry), so skip this check.
	if oclock > 0 {
		oclockHeading := float32((oclock % 12) * 30)
		reportedBearing := math.MagneticHeading(math.NormalizeHeading(float32(ac.Heading()) + oclockHeading))
		bearingError := math.HeadingDifference(reportedBearing, elig.BearingToAirport)
		if bearingError > 30 {
			s.enqueueFutureFieldCheck(ac.ADSBCallsign)
			return av.LookForFieldLooking
		}
	}

	if s.Rand.Float32() < pilotSeeProb(elig.MaxRange, elig.Distance) {
		ac.FieldInSight = true
		return av.LookForFieldFound
	}

	// "Looking" — schedule possible delayed field-in-sight call.
	s.enqueueFutureFieldCheck(ac.ADSBCallsign)
	return av.LookForFieldLooking
}

// samplePilotLookFireTime samples a future time at which a "looking" pilot
// will speak up. Uniform within [pilotLookDurationMin, pilotLookDurationMax];
// with probability pilotNoReportProb the pilot never speaks up this window
// (ok=false) — preserving the "sometimes the pilot just doesn't report"
// behaviour of the old per-tick dice roll.
func (s *Sim) samplePilotLookFireTime() (Time, bool) {
	if s.Rand.Float32() < pilotNoReportProb {
		return Time{}, false
	}
	return s.State.SimTime.Add(s.Rand.DurationRange(pilotLookDurationMin, pilotLookDurationMax)), true
}

func (s *Sim) enqueueFutureFieldCheck(callsign av.ADSBCallsign) {
	s.cancelFutureFieldCheck(callsign)
	if t, ok := s.samplePilotLookFireTime(); ok {
		s.FutureFieldChecks[callsign] = &FutureFieldCheck{Time: t}
	}
}

func (s *Sim) enqueueFutureTrafficCheck(callsign, traffic av.ADSBCallsign) {
	s.cancelFutureTrafficCheck(callsign)
	if t, ok := s.samplePilotLookFireTime(); ok {
		s.FutureTrafficChecks[callsign] = &FutureTrafficCheck{TrafficCallsign: traffic, Time: t}
	}
}

func (s *Sim) cancelFutureFieldCheck(callsign av.ADSBCallsign) {
	delete(s.FutureFieldChecks, callsign)
}

func (s *Sim) cancelFutureTrafficCheck(callsign av.ADSBCallsign) {
	delete(s.FutureTrafficChecks, callsign)
}

func (s *Sim) ExpectApproach(tcw TCW, callsign av.ADSBCallsign, approach string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return nil, av.ErrUnknownAirport
		}
	}

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ExpectApproach(approach, ap)
		})
}

func (s *Sim) ClearedApproach(tcw TCW, callsign av.ADSBCallsign, approach string, straightIn bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			var following *nav.FollowTraffic
			if id, visual := strings.CutPrefix(approach, "_VIS"); visual {
				rwy, _, _ := strings.Cut(id, "/LAHSO")
				// Pilot must have the field or approach-cleared preceding
				// traffic in sight before accepting a visual approach clearance.
				if traffic := s.recentApproachTrafficInSightForRunway(ac, rwy); traffic != nil {
					following = &nav.FollowTraffic{
						Position: traffic.Position(),
						Route:    traffic.Nav.Waypoints,
					}
				} else if !ac.FieldInSight && !ac.RequestedVisualApproach {
					return av.MakeUnableIntent("unable, we don't have the field in sight")
				}

				// Spontaneous "field in sight" / requested-visual / approach-traffic-
				// in-sight allows bare CVA without a preceding EVA: synthesize the
				// visual assignment so nav.ClearedApproach has the references it
				// needs.
				if ac.Nav.Approach.AssignedId != approach {
					ap := s.State.Airports[ac.FlightPlan.ArrivalAirport]
					if intent := ac.ExpectApproach(approach, ap); intent != nil {
						if _, unable := intent.(av.UnableIntent); unable {
							return intent
						}
					}
				}
			}

			if straightIn {
				return ac.ClearedStraightInApproach(approach, s.State.SimTime, following)
			} else {
				return ac.ClearedApproach(approach, s.State.SimTime, following)
			}
		})
}

func (s *Sim) InterceptApproach(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.InterceptApproach(s.lg)
		})
}

func (s *Sim) CancelApproachClearance(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) hasRecentApproachTrafficInSight(ac *Aircraft) bool {
	return s.recentApproachTrafficInSight(ac) != nil
}

func (s *Sim) recentApproachTrafficInSightForRunway(ac *Aircraft, runway string) *Aircraft {
	for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
		seen := &ac.SeenTraffic[i]
		if s.State.SimTime.Sub(seen.SightedTime) > approachTrafficSightingMaxAge {
			continue
		}
		traffic, ok := s.Aircraft[seen.Callsign]
		if !ok || !traffic.Nav.Approach.Cleared || traffic.Nav.Approach.Assigned == nil {
			continue
		}
		if traffic.Nav.Approach.Assigned.Runway == runway {
			return traffic
		}
	}
	return nil
}

func (s *Sim) recentApproachTrafficInSight(ac *Aircraft) *Aircraft {
	for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
		seen := &ac.SeenTraffic[i]
		if s.State.SimTime.Sub(seen.SightedTime) > approachTrafficSightingMaxAge {
			continue
		}

		traffic, ok := s.Aircraft[seen.Callsign]
		if ok && traffic.Nav.Approach.Cleared {
			return traffic
		}
	}
	return nil
}

// FutureFieldCheck is enqueued when a pilot says "looking" in response to
// an AP command. At fire time the processor re-validates visibility.
type FutureFieldCheck struct {
	Time Time
}

// FutureTrafficCheck is enqueued when a pilot says "looking" in response to
// a traffic call. At fire time the pilot reports traffic in sight (no
// re-validation — matching the original behaviour).
type FutureTrafficCheck struct {
	TrafficCallsign av.ADSBCallsign
	Time            Time
}

func (s *Sim) processFutureFieldChecks() {
	for callsign, f := range s.FutureFieldChecks {
		if !s.State.SimTime.After(f.Time) {
			continue
		}
		ac, ok := s.Aircraft[callsign]
		if !ok || ac.FieldInSight || ac.ControllerFrequency == "" || ac.Nav.Approach.Cleared {
			delete(s.FutureFieldChecks, callsign)
			continue
		}

		if s.checkAirportVisibility(ac).FieldInSight {
			ac.FieldInSight = true
			s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionFieldInSight)
			delete(s.FutureFieldChecks, callsign)
		} else {
			f.Time = f.Time.Add(s.Rand.DurationRange(7*time.Second, 15*time.Second)) // try again in a bit
		}
	}
}

func (s *Sim) processFutureTrafficChecks() {
	for callsign, f := range s.FutureTrafficChecks {
		if !s.State.SimTime.After(f.Time) {
			continue
		}

		// Drop this one if either the looking or the traffic aircraft are gone.
		ac, ok := s.Aircraft[callsign]
		if !ok || ac.ControllerFrequency == "" {
			delete(s.FutureTrafficChecks, callsign)
			continue
		}
		traffic, ok := s.Aircraft[f.TrafficCallsign]
		if !ok {
			delete(s.FutureTrafficChecks, callsign)
			continue
		}

		if s.trafficIsVisible(ac, traffic) {
			sighting := ac.RecordSighting(f.TrafficCallsign, s.State.SimTime)
			sighting.OfferedToMaintainSeparation = false
			s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionTrafficInSight)
			delete(s.FutureTrafficChecks, callsign)
		} else {
			f.Time = f.Time.Add(s.Rand.DurationRange(7*time.Second, 15*time.Second)) // try again in a bit
		}
	}
}

func (s *Sim) refreshSeenTraffic(ac *Aircraft) {
	now := s.State.SimTime
	ac.SeenTraffic = util.FilterSliceInPlace(ac.SeenTraffic,
		func(seen SeenAircraft) bool {
			if seen.MaintainingVisualSeparation {
				return s.trafficStillVisible(ac, &seen)
			}
			return now.Sub(seen.SightedTime) <= trafficSightingMaxAge
		})
}

func (s *Sim) trafficStillVisible(ac *Aircraft, seen *SeenAircraft) bool {
	traffic, ok := s.Aircraft[seen.Callsign]
	if !ok {
		return false
	}

	bearingToTraffic := math.TrueToMagnetic(
		math.Heading2LL(ac.Position(), traffic.Position(), ac.NmPerLongitude()),
		ac.MagneticVariation())
	if math.HeadingDifference(ac.Heading(), bearingToTraffic) > visualMaxBearingOff {
		return false
	}

	nearestMETAR, nearestElev := s.nearestMETAR(ac.Position())
	if nearestMETAR.ICAO != "" && !nearestMETAR.IsVMC() {
		return false
	}

	altAGL := max(ac.Altitude()-nearestElev, 0)
	trafficAltAGL := max(traffic.Altitude()-nearestElev, 0)
	dist := math.NMDistance2LLFast(ac.Position(), traffic.Position(), ac.NmPerLongitude())
	return pilotSeeProb(nearestMETAR.EffectiveVisualRange(altAGL, trafficAltAGL), dist) > 0
}

// canRequestVisualApproach reports whether an aircraft is eligible to
// spontaneously request the visual approach. The aircraft must be an
// arrival on frequency, assigned a non-visual approach that hasn't been
// cleared yet, and must not have already made the request.
func (ac *Aircraft) canRequestVisualApproach() bool {
	if ac.IsDeparture() || ac.FieldInSight || ac.RequestedVisualApproach || ac.ControllerFrequency == "" {
		return false
	}
	if ac.Nav.Approach.AssignedId == "" || ac.Nav.Approach.Cleared {
		return false
	}
	appr := ac.Nav.Approach.Assigned
	return appr != nil && appr.Type != av.ChartedVisualApproach && appr.Type != av.VisualApproach
}

type visualEligibilityReason int

const (
	visualEligibilityOK visualEligibilityReason = iota
	visualEligibilityIMC
	visualEligibilityOutOfRange
	visualEligibilityObscured
	visualEligibilityBadBearing
)

// VisualEligibility describes whether an aircraft can see the field
// and request a visual approach.
type VisualEligibility struct {
	FieldInSight     bool // true if VMC, within range, and airport visible
	Reason           visualEligibilityReason
	Distance         float32
	MaxRange         float32
	BearingToAirport math.MagneticHeading
}

// checkAirportVisibility determines whether the aircraft can see the field.
func (s *Sim) checkAirportVisibility(ac *Aircraft) VisualEligibility {
	arrivalAirport := ac.FlightPlan.ArrivalAirport
	ap := s.State.Airports[arrivalAirport]

	// Must be VMC at the arrival airport.
	metar, ok := s.State.METAR[arrivalAirport]
	if !ok || !metar.IsVMC() {
		return VisualEligibility{Reason: visualEligibilityIMC}
	}

	// Aircraft above the ceiling is in the clouds → can't see the field.
	if ceiling, err := metar.Ceiling(); err == nil {
		if faa, ok := av.DB.Airports[arrivalAirport]; ok {
			if ac.Altitude() > float32(faa.Elevation+ceiling) {
				return VisualEligibility{Reason: visualEligibilityIMC}
			}
		}
	}

	// Must be within effective visual range (METAR visibility + altitude bonus).
	faa := av.DB.Airports[arrivalAirport]
	altAGL := max(0, ac.Altitude()-float32(faa.Elevation))

	maxRange := metar.EffectiveVisualRange(altAGL, 0)
	dist := math.NMDistance2LL(ac.Position(), ap.Location)
	if dist > maxRange {
		reason := util.Select(metar.HasObscuration(), visualEligibilityObscured, visualEligibilityOutOfRange)
		return VisualEligibility{
			Distance: dist,
			MaxRange: maxRange,
			Reason:   reason,
		}
	}

	// The airport must be within the pilot's forward visibility arc.
	bearingToAirport := math.TrueToMagnetic(math.Heading2LL(ac.Position(), ap.Location, ac.NmPerLongitude()), ac.MagneticVariation())
	if math.HeadingDifference(ac.Heading(), bearingToAirport) > visualMaxBearingOff {
		return VisualEligibility{
			Distance:         dist,
			MaxRange:         maxRange,
			BearingToAirport: bearingToAirport,
			Reason:           visualEligibilityBadBearing,
		}
	}

	return VisualEligibility{
		FieldInSight:     true,
		Reason:           visualEligibilityOK,
		Distance:         dist,
		MaxRange:         maxRange,
		BearingToAirport: bearingToAirport,
	}
}

// Tunables for the pilot-vision model.
const (
	visualMaxBearingOff  = 120  // degrees off nose; forward visibility arc
	visualFieldProb      = 0.15 // fraction of pilots who spontaneously report field in sight
	visualRequestProb    = 0.3  // fraction of field-in-sight pilots who also request the visual
	pilotLookDurationMin = 10 * time.Second
	pilotLookDurationMax = 20 * time.Second
	pilotNoReportProb    = 0.12 // probability a "looking" pilot never speaks up this window
)

// pilotSeeProb returns a probability (0..1) that a pilot can visually
// identify a target at distNM, given the effective visual range (NM).
func pilotSeeProb(effectiveRangeNM, distNM float32) float32 {
	if effectiveRangeNM <= 0 || distNM > effectiveRangeNM {
		return 0
	}

	t := distNM / effectiveRangeNM
	if t < 0.5 {
		// It's fairly close w.r.t. the visual range, so it's highly likely it will be seen.
		return 0.98
	} else {
		// Otherwise ramp probability down to 0.3 at effectiveRangeNM. t is squared so that
		// distances up until then have higher probabilities, with a faster falloff at the end.
		// This does give a sharp cutoff at effectiveRangeNM, FWIW.
		t = 2 * (t - 0.5)
		t *= t
		return max(0, math.Lerp(t, 0.98, 0.3))
	}
}

// checkSpontaneousVisualRequest handles two per-tick behaviours for an
// arrival that has spontaneous-report flags set at spawn:
//
//  1. If VisualRequestDistance > 0 and the aircraft is closer to the arrival
//     airport than that, perform a single visibility check; request the
//     visual approach if the field is in sight, otherwise give up.
//     VisualRequestDistance is zeroed either way to prevent retries.
//
//  2. Otherwise, if WantsVisual, report "field in sight" the first tick the
//     field becomes visible. FieldInSight is then set, which disarms this
//     function via canRequestVisualApproach.
func (s *Sim) checkSpontaneousVisualRequest(ac *Aircraft) {
	if !ac.canRequestVisualApproach() || s.hasPendingCheckIn(ac.ADSBCallsign) {
		return
	}

	if ac.VisualApproachRequestDistance > 0 {
		ap := s.State.Airports[ac.FlightPlan.ArrivalAirport]
		dist := math.NMDistance2LL(ac.Position(), ap.Location)
		if dist > ac.VisualApproachRequestDistance {
			return
		}
		if s.checkAirportVisibility(ac).FieldInSight {
			ac.FieldInSight = true
			ac.RequestedVisualApproach = true
			s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionRequestVisual)
		}
		ac.VisualApproachRequestDistance = 0
	} else if ac.WantsVisualApproach && s.checkAirportVisibility(ac).FieldInSight {
		ac.FieldInSight = true
		s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionFieldInSight)
	}
}

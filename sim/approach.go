// sim/approach.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/util"
)

// notLandingHere is the response to an airport advisory for an aircraft that
// isn't landing in the sim's airspace; asking a departure or an overflight to
// look for its destination is meaningless.
var notLandingHere = av.MakeUnableIntent("unable, we're not landing here")

// AirportInSightInquiry handles the bare "AP" command. The controller asks
// "do you have the field in sight?" without specifying a direction; the
// pilot's response depends on weather, ceiling, and distance to the airport —
// no o'clock/bearing validation is performed.
func (s *Sim) AirportInSightInquiry(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if !ac.IsArrival() {
				return notLandingHere
			}
			if ac.FieldInSight || ac.RequestedVisualApproach {
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

	// Neither the reaffirmation nor the geometric fallback below rolls for visibility,
	// so gate both: in IMC the pilot can't possibly see anything out there.
	if metar, _ := s.nearestMETAR(ac.Position()); metar.ICAO != "" && !metar.IsVMC() {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseIMC}
	}

	// "Do you still have him?" — the pilot already called something in sight and can
	// still see it. Reporting in sight empties FutureTrafficChecks, so without this the
	// most common bare inquiry falls through to guessing.
	if seen := ac.RecentSighting(); seen != nil {
		if traffic, ok := s.Aircraft[seen.Callsign]; ok && ac.canSeeTraffic(traffic) {
			seen.SightedTime = s.State.SimTime
			return av.TrafficAdvisoryIntent{Response: av.TrafficResponseTrafficSeen}
		}
	}

	bearingTo := func(candidate *Aircraft) math.MagneticHeading {
		return math.TrueToMagnetic(
			math.Heading2LL(ac.Position(), candidate.Position(), ac.NmPerLongitude()),
			ac.MagneticVariation())
	}

	matches := slices.Collect(util.FilterSeq(maps.Values(s.Aircraft), func(candidate *Aircraft) bool {
		return candidate.ADSBCallsign != ac.ADSBCallsign &&
			math.Abs(candidate.Altitude()-ac.Altitude()) < trafficInquiryVerticalFeet &&
			math.NMDistance2LL(ac.Position(), candidate.Position()) < trafficInquiryRangeNM &&
			math.HeadingDifference(ac.Heading(), bearingTo(candidate)) < trafficInquiryMaxBearingOff
	}))
	if len(matches) == 0 {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseWhereWasIt}
	}

	nearest := slices.MinFunc(matches, func(a, b *Aircraft) int {
		return cmp.Compare(math.NMDistance2LL(ac.Position(), a.Position()),
			math.NMDistance2LL(ac.Position(), b.Position()))
	})

	// Several aircraft ahead are only ambiguous if they are in different places: two in
	// trail on the same bearing are "the traffic" either way, which is the normal picture
	// on a parallel visual.
	nearestBearing := bearingTo(nearest)
	if slices.ContainsFunc(matches, func(candidate *Aircraft) bool {
		return math.HeadingDifference(nearestBearing, bearingTo(candidate)) > trafficInquiryDistinctBearing
	}) {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseWhereWasIt}
	}

	ac.RecordSighting(nearest.ADSBCallsign, s.State.SimTime)
	return av.TrafficAdvisoryIntent{Response: av.TrafficResponseTrafficSeen}
}

// AirportAdvisory handles the AP/{oclock}/{miles} command. The controller tells the
// pilot where to look for the airport: "airport, {oclock} o'clock, {miles} miles".
// The pilot responds with "field in sight", "looking", or an IMC indication.
func (s *Sim) AirportAdvisory(tcw TCW, callsign av.ADSBCallsign, oclock, miles int) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if !ac.IsArrival() {
				return notLandingHere
			}

			// If the pilot already has the field in sight, just confirm.
			// Being cleared for an approach isn't enough: an aircraft on the
			// ILS in the soup can't see the field, and answering otherwise
			// would leave FieldInSight unset and a later CVA refused.
			if ac.FieldInSight || ac.RequestedVisualApproach {
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
		s.enqueueFutureFieldCheck(ac)
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
			s.enqueueFutureFieldCheck(ac)
			return av.LookForFieldLooking
		}
	}

	r, p := s.Rand.Float32(), pilotSeeProb(elig.MaxRange, elig.Distance)
	s.lg.Infof("%s: airport visibility check r=%f, p=%f", ac.ADSBCallsign, r, p)
	if r < p {
		ac.FieldInSight = true
		return av.LookForFieldFound
	}

	// "Looking" — schedule possible delayed field-in-sight call.
	s.enqueueFutureFieldCheck(ac)
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

func (s *Sim) enqueueFutureFieldCheck(ac *Aircraft) {
	s.cancelFutureFieldCheck(ac.ADSBCallsign)
	if t, ok := s.samplePilotLookFireTime(); ok {
		s.FutureFieldChecks[ac.ADSBCallsign] = &FutureFieldCheck{
			Time:             t,
			ClearedWhenAsked: ac.Nav.Approach.EffectivelyCleared(),
		}
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
				if traffic, seen := s.recentApproachTrafficInSightForRunway(ac, rwy); traffic != nil {
					seen.FollowingOnVisualApproach = true
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
					if ap == nil {
						return av.MakeUnableIntent("unable, we can't accept a visual approach there")
					}
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
			return ac.InterceptApproach()
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

// recentApproachTrafficInSightForRunway returns the traffic most recently
// reported in sight by ac that is landing the same runway at the same airport,
// together with ac's sighting of it. The pilot keeps it for as long as they can
// still see it out the window, however long ago the controller called it.
func (s *Sim) recentApproachTrafficInSightForRunway(ac *Aircraft, runway string) (*Aircraft, *SeenAircraft) {
	for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
		seen := &ac.SeenTraffic[i]
		traffic, ok := s.Aircraft[seen.Callsign]
		if !ok || !traffic.Nav.Approach.Cleared || traffic.Nav.Approach.Assigned == nil {
			continue
		}
		if traffic.FlightPlan.ArrivalAirport != ac.FlightPlan.ArrivalAirport {
			continue
		}
		if !ac.canSeeTraffic(traffic) {
			continue
		}
		if av.RunwayID(traffic.Nav.Approach.Assigned.Runway).SameRunway(av.RunwayID(runway)) {
			return traffic, seen
		}
	}
	return nil, nil
}

// FutureFieldCheck is enqueued when a pilot says "looking" in response to
// an AP command. At fire time the processor re-validates visibility.
type FutureFieldCheck struct {
	Time Time
	// ClearedWhenAsked records whether the aircraft was already cleared for an
	// approach when the controller asked for the report. If it wasn't, a
	// clearance issued while the pilot is still looking makes the report moot.
	ClearedWhenAsked bool
}

// FutureTrafficCheck is enqueued when a pilot says "looking" in response to
// a traffic call. At fire time visibility is re-checked; if the pilot still
// can't see the traffic, the check is rescheduled a few seconds out.
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
		if !ok || ac.FieldInSight || ac.ControllerFrequency == "" ||
			(!f.ClearedWhenAsked && ac.Nav.Approach.EffectivelyCleared()) {
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
	apLoc := ac.ArrivalAirportLocation()
	apElev := ac.ArrivalAirportElevation()

	// Must be VMC at the arrival airport. The arrival airport may not be one of
	// the sim's airports (e.g. a satellite field a VFR is headed for), in which
	// case there's no METAR for it; fall back to the nearest one.
	metar, ok := s.State.METAR[ac.FlightPlan.ArrivalAirport]
	if !ok {
		metar, _ = s.nearestMETAR(apLoc)
		ok = metar.ICAO != ""
	}
	// A zero METAR reports IMC, so only take its word for it if we found one.
	if ok && !metar.IsVMC() {
		return VisualEligibility{Reason: visualEligibilityIMC}
	}

	// Aircraft above the ceiling is in the clouds → can't see the field.
	if ceiling, err := metar.Ceiling(); err == nil {
		if ac.Altitude() > apElev+float32(ceiling) {
			return VisualEligibility{Reason: visualEligibilityIMC}
		}
	}

	// Must be within effective visual range (METAR visibility + altitude bonus).
	altAGL := max(0, ac.Altitude()-apElev)

	maxRange := metar.EffectiveVisualRange(altAGL, 0)
	dist := math.NMDistance2LL(ac.Position(), apLoc)
	if dist > maxRange {
		reason := util.Select(metar.HasObscuration(), visualEligibilityObscured, visualEligibilityOutOfRange)
		return VisualEligibility{
			Distance: dist,
			MaxRange: maxRange,
			Reason:   reason,
		}
	}

	// The airport must be within the pilot's forward visibility arc.
	bearingToAirport := math.TrueToMagnetic(math.Heading2LL(ac.Position(), apLoc, ac.NmPerLongitude()), ac.MagneticVariation())
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

	// Above this height AGL, arrivals won't *spontaneously* report the field in
	// sight; they wait until descended into the terminal environment. Does not
	// affect controller-prompted (AP command) reports.
	maxSpontaneousFieldInSightAGL = 8000 // ft AGL

	skyBackgroundMinFt      = 500 // ft above the observer to count as silhouetted against sky
	skyBackgroundBoost      = 1.3
	groundClutterMinTangent = 0.18 // tan(10 degrees); shallower looks are still on the horizon
	groundClutterMaxTangent = 0.58 // tan(30 degrees); steeper looks are fully into terrain
	groundClutterFactor     = 0.7

	// A pilot can't see through the roof or the floor. The certification minimum clear
	// view through the windshield is only about 15 degrees up and 17 down (AC 25.773-1),
	// and see-and-avoid models such as DEGAS use those directly, but that is the
	// guaranteed forward area rather than everything a pilot can reach: traffic well
	// outside it is routinely acquired through the side windows and by leaning. This is
	// deliberately generous, rejecting only geometry where the other aircraft is very
	// nearly overhead or underneath.
	maxVerticalViewTangent = 1.73 // tan(60 degrees)

	// Tolerances for the bare TRAFFIC inquiry, where the controller gives no direction.
	// The cone is deliberately much tighter than visualMaxBearingOff: that one asks
	// whether the pilot can physically see out that way, this one asks what "the
	// traffic" plausibly means when nothing was named.
	trafficInquiryRangeNM         = 3
	trafficInquiryVerticalFeet    = 1000
	trafficInquiryMaxBearingOff   = 60 // degrees off nose; 10 through 2 o'clock
	trafficInquiryDistinctBearing = 30 // candidates within this of the nearest are the same target
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

// withinVerticalFieldOfView reports whether traffic sits at a shallow enough angle
// above or below the observer to be visible from a cockpit at all.
func withinVerticalFieldOfView(altDiffFt, distNM float32) bool {
	return math.Abs(altDiffFt) <= distNM*math.NauticalMilesToFeet*maxVerticalViewTangent
}

// relativeAltitudeVisibility scales the see-probability by where the target sits
// relative to the observer's horizon. Depression angle is what matters, not the
// altitude difference: traffic a thousand feet low three miles ahead is still
// against the sky, which is why a leader on the same glideslope stays equally
// easy to see however far ahead it is.
func relativeAltitudeVisibility(altDiffFt, distNM float32) float32 {
	if altDiffFt > skyBackgroundMinFt {
		return skyBackgroundBoost
	}
	if altDiffFt >= 0 {
		return 1
	}

	// Zero range needs no special case: the tangent goes infinite, which clamps to the
	// full ground-clutter penalty, and that is what looking straight down deserves.
	tangent := -altDiffFt / (distNM * math.NauticalMilesToFeet)
	t := math.Clamp((tangent-groundClutterMinTangent)/(groundClutterMaxTangent-groundClutterMinTangent), 0, 1)
	return math.Lerp(t, 1, groundClutterFactor)
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

	// Don't spontaneously report the field in sight from high altitude; wait
	// until the aircraft has descended into the terminal environment.
	if ac.Altitude()-ac.ArrivalAirportElevation() > maxSpontaneousFieldInSightAGL {
		return
	}

	if ac.VisualApproachRequestDistance > 0 {
		dist := math.NMDistance2LL(ac.Position(), ac.ArrivalAirportLocation())
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
		s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionSpontaneousFieldInSight)
	}
}

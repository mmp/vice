// sim/approach.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
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
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// notLandingHere is the response to an airport advisory for an aircraft that
// isn't landing in the sim's airspace; asking a departure or an overflight to
// look for its destination is meaningless.
var notLandingHere = speech.MakeUnableIntent("unable, we're not landing here")

// AirportInSightInquiry handles the bare "AP" command. The controller asks
// "do you have the field in sight?" without specifying a direction; the
// pilot's response depends on weather, ceiling, and distance to the airport —
// no o'clock/bearing validation is performed.
func (s *Sim) AirportInSightInquiry(tcw TCW, callsign av.ADSBCallsign) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			if !ac.IsArrival() {
				return notLandingHere
			}
			if ac.FieldInSight || ac.RequestedVisualApproach {
				s.cancelFutureFieldCheck(ac.ADSBCallsign)
				return speech.LookForFieldFound
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
func (s *Sim) TrafficInSightInquiry(tcw TCW, callsign av.ADSBCallsign) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			return s.handleTrafficInSightInquiry(ac)
		})
}

// handleTrafficInSightInquiry implements the bare TRAFFIC inquiry resolution.
func (s *Sim) handleTrafficInSightInquiry(ac *Aircraft) speech.CommandIntent {
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
			return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseTrafficSeen}
		} else {
			return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseLooking}
		}
	}

	// Neither the reaffirmation nor the geometric fallback below rolls for visibility,
	// so gate both: in IMC the pilot can't possibly see anything out there.
	if metar, _ := s.nearestMETAR(ac.Position()); metar.ICAO != "" && !metar.IsVMC() {
		return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseIMC}
	}

	// "Do you still have him?" — the pilot already called something in sight and can
	// still see it. Reporting in sight empties FutureTrafficChecks, so without this the
	// most common bare inquiry falls through to guessing.
	if seen := ac.RecentSighting(); seen != nil {
		if traffic, ok := s.Aircraft[seen.Callsign]; ok && ac.canSeeTraffic(traffic) {
			seen.SightedTime = s.State.SimTime
			return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseTrafficSeen}
		}
	}

	matches := slices.Collect(util.FilterSeq(maps.Values(s.Aircraft), func(candidate *Aircraft) bool {
		return candidate.ADSBCallsign != ac.ADSBCallsign &&
			math.Abs(candidate.Altitude()-ac.Altitude()) < trafficInquiryVerticalFeet &&
			math.NMDistance2LL(ac.Position(), candidate.Position()) < trafficInquiryRangeNM &&
			math.HeadingDifference(ac.Heading(), ac.bearingTo(candidate.Position())) < trafficInquiryMaxBearingOff
	}))
	if len(matches) == 0 {
		return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseWhereWasIt}
	}

	nearest := slices.MinFunc(matches, func(a, b *Aircraft) int {
		return cmp.Compare(math.NMDistance2LL(ac.Position(), a.Position()),
			math.NMDistance2LL(ac.Position(), b.Position()))
	})

	// Several aircraft ahead are only ambiguous if they are in different places: two in
	// trail on the same bearing are "the traffic" either way, which is the normal picture
	// on a parallel visual.
	nearestBearing := ac.bearingTo(nearest.Position())
	if slices.ContainsFunc(matches, func(candidate *Aircraft) bool {
		return math.HeadingDifference(nearestBearing, ac.bearingTo(candidate.Position())) > trafficInquiryDistinctBearing
	}) {
		return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseWhereWasIt}
	}

	ac.RecordSighting(nearest.ADSBCallsign, s.State.SimTime)
	return speech.TrafficAdvisoryIntent{Response: speech.TrafficResponseTrafficSeen}
}

// AirportAdvisory handles the AP/{oclock}/{miles} command. The controller tells the
// pilot where to look for the airport: "airport, {oclock} o'clock, {miles} miles".
// The pilot responds with "field in sight", "looking", or an IMC indication.
func (s *Sim) AirportAdvisory(tcw TCW, callsign av.ADSBCallsign, oclock, miles int) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			if !ac.IsArrival() {
				return notLandingHere
			}

			// If the pilot already has the field in sight, just confirm.
			// Being cleared for an approach isn't enough: an aircraft on the
			// ILS in the soup can't see the field, and answering otherwise
			// would leave FieldInSight unset and a later CVA refused.
			if ac.FieldInSight || ac.RequestedVisualApproach {
				return speech.LookForFieldFound
			}

			return s.handleAirportAdvisory(ac, oclock, miles)
		})
}

// handleAirportAdvisory determines the pilot's response to an AP command.
func (s *Sim) handleAirportAdvisory(ac *Aircraft, oclock int, miles int) speech.CommandIntent {
	switch s.lookFor(ac, nil, oclock) {
	case sightingFound:
		ac.FieldInSight = true
		return speech.LookForFieldFound
	case sightingLookingIMC:
		return speech.LookForFieldLookingIMC
	case sightingLookingObscured:
		return speech.LookForFieldLookingObscured
	default:
		return speech.LookForFieldLooking
	}
}

// ReportingPointAdvisory asks the pilot to find a reporting point of the expected
// charted visual approach. An empty id selects by position; oclock and miles
// are zero when the controller gives no position.
func (s *Sim) ReportingPointAdvisory(tcw TCW, callsign av.ADSBCallsign, id string, oclock, miles int) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			if len(ac.expectedReportingPoints()) == 0 {
				return speech.MakeUnableIntent("unable, we're not expecting a charted visual")
			}
			rp := ac.calledReportingPoint(id, oclock, miles)
			if rp == nil {
				return speech.MakeUnableIntent("unable, that reporting point isn't on our expected approach")
			}

			if seen := ac.SightedReportingPoint; seen != nil && seen.Id == rp.Id {
				s.cancelFutureFieldCheck(ac.ADSBCallsign)
				return speech.ReportingPointIntent{Response: speech.ReportingPointInSight, Names: seen.Names}
			}

			switch s.lookFor(ac, rp, oclock) {
			case sightingFound:
				ac.SightedReportingPoint = rp
				return speech.ReportingPointIntent{Response: speech.ReportingPointInSight, Names: rp.Names}
			case sightingLookingIMC:
				return speech.ReportingPointIntent{Response: speech.ReportingPointLookingIMC}
			case sightingLookingObscured:
				return speech.ReportingPointIntent{Response: speech.ReportingPointLookingObscured}
			default:
				return speech.ReportingPointIntent{Response: speech.ReportingPointLooking}
			}
		})
}

// sightingResult is the outcome of a pilot looking for something that the
// controller called.
type sightingResult int

const (
	sightingFound sightingResult = iota
	sightingLooking
	sightingLookingIMC
	sightingLookingObscured
)

// lookFor determines whether the pilot sees the field or, if rp is
// non-nil, the charted visual reporting point rp, after the controller
// calls it at the given o'clock. If the pilot doesn't see it, they keep
// looking for it, weather permitting.
func (s *Sim) lookFor(ac *Aircraft, rp *av.ReportingPoint, oclock int) sightingResult {
	// A fresh call supersedes any earlier "looking" event still queued
	// for this aircraft; the enqueue helper will re-add one if appropriate.
	s.cancelFutureFieldCheck(ac.ADSBCallsign)

	elig := s.checkVisibility(ac, sightingLocation(ac, rp))
	if !elig.Visible {
		if elig.Reason == visualEligibilityIMC {
			return sightingLookingIMC
		}
		s.enqueueFutureFieldCheck(ac, rp)
		if elig.Reason == visualEligibilityObscured {
			return sightingLookingObscured
		}
		return sightingLooking
	}

	// Validate the controller's o'clock direction against the actual bearing.
	// oclock == 0 means the controller didn't give a direction (e.g., the
	// bare "AP" inquiry), so skip this check.
	if oclock > 0 && math.HeadingDifference(ac.oclockBearing(oclock), elig.Bearing) > 30 {
		s.enqueueFutureFieldCheck(ac, rp)
		return sightingLooking
	}

	r, p := s.Rand.Float32(), pilotSeeProb(elig.MaxRange, elig.Distance)
	s.lg.Infof("%s: visibility check r=%f, p=%f", ac.ADSBCallsign, r, p)
	if r < p {
		return sightingFound
	}

	// "Looking" — schedule possible delayed in-sight call.
	s.enqueueFutureFieldCheck(ac, rp)
	return sightingLooking
}

// sightingLocation returns the location of rp, or of the arrival airport
// if rp is nil.
func sightingLocation(ac *Aircraft, rp *av.ReportingPoint) math.Point2LL {
	if rp != nil {
		return rp.Location.Point2LL
	}
	return ac.ArrivalAirportLocation()
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

func (s *Sim) enqueueFutureFieldCheck(ac *Aircraft, rp *av.ReportingPoint) {
	s.cancelFutureFieldCheck(ac.ADSBCallsign)
	if t, ok := s.samplePilotLookFireTime(); ok {
		s.FutureFieldChecks[ac.ADSBCallsign] = &FutureFieldCheck{
			Time:             t,
			ClearedWhenAsked: ac.Nav.Approach.EffectivelyCleared(),
			ReportingPoint:   rp,
			ApproachId:       ac.Nav.Approach.AssignedId,
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

func (s *Sim) ExpectApproach(tcw TCW, callsign av.ADSBCallsign, approach string) (speech.CommandIntent, error) {
	var ap *av.Airport
	if ac, ok := s.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.ArrivalAirport]
		if ap == nil {
			return nil, av.ErrUnknownAirport
		}
	}

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			return ac.ExpectApproach(approach, ap)
		})
}

func (s *Sim) ClearedApproach(tcw TCW, callsign av.ADSBCallsign, approach string, straightIn bool,
	delayReduction time.Duration) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
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
					return speech.MakeUnableIntent("unable, we don't have the field in sight")
				}

				// Spontaneous "field in sight" / requested-visual / approach-traffic-
				// in-sight allows bare CVA without a preceding EVA: synthesize the
				// visual assignment so nav.ClearedApproach has the references it
				// needs.
				if ac.Nav.Approach.AssignedId != approach {
					ap := s.State.Airports[ac.ArrivalAirport]
					if ap == nil {
						return speech.MakeUnableIntent("unable, we can't accept a visual approach there")
					}
					if intent := ac.ExpectApproach(approach, ap); intent != nil {
						if _, unable := intent.(speech.Refusal); unable {
							return intent
						}
					}
				}
			} else if unable := s.refuseChartedVisualClearance(ac, approach); unable != nil {
				return unable
			}

			if straightIn {
				return ac.ClearedStraightInApproach(approach, s.State.SimTime, delayReduction, following)
			} else {
				return ac.ClearedApproach(approach, s.State.SimTime, delayReduction, following)
			}
		})
}

// refuseChartedVisualClearance returns the pilot's refusal of a clearance for
// the charted visual approach they are expecting if they don't have what they
// need in sight to fly it: one of its landmarks, preceding traffic landing the
// same runway, or the field. (7110.65 7-4-5 only allows the first two, but
// there's no harm in the field.) It returns nil if the pilot can accept the
// clearance or if approach isn't the charted visual they are expecting; a
// clearance restated after they have been cleared needs nothing new.
func (s *Sim) refuseChartedVisualClearance(ac *Aircraft, approach string) speech.CommandIntent {
	appr := ac.Nav.Approach.Assigned
	id, _, _ := strings.Cut(approach, "/LAHSO")
	if appr == nil || appr.Type != av.ChartedVisualApproach || (id != "" && id != ac.Nav.Approach.AssignedId) ||
		ac.Nav.Approach.EffectivelyCleared() {
		return nil
	}

	if ac.FieldInSight || ac.RequestedVisualApproach {
		return nil
	}
	if seen := ac.SightedReportingPoint; seen != nil && appr.ReportingPoints[seen.Id] != nil {
		return nil
	}
	if traffic, _ := s.recentApproachTrafficInSightForRunway(ac, appr.Runway); traffic != nil {
		return nil
	}

	// Name the landmark the controller would most likely call.
	if rp := ac.calledReportingPoint("", 0, 0); rp != nil {
		return speech.MakeUnableIntent("unable, {rp} not in sight", rp.Name())
	}
	return speech.MakeUnableIntent("unable, we don't have the field in sight")
}

func (s *Sim) InterceptApproach(tcw TCW, callsign av.ADSBCallsign) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(tcw TCW, callsign av.ADSBCallsign) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
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
		if traffic.ArrivalAirport != ac.ArrivalAirport {
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
// an AP or RP command. At fire time the processor re-validates visibility.
type FutureFieldCheck struct {
	Time Time
	// ClearedWhenAsked records whether the aircraft was already cleared for an
	// approach when the controller asked for the report. If it wasn't, a
	// clearance issued while the pilot is still looking makes the report moot.
	ClearedWhenAsked bool
	// ReportingPoint is the charted visual reporting point the pilot is
	// looking for; they are looking for the field if it is nil.
	ReportingPoint *av.ReportingPoint
	// ApproachId is the approach the aircraft was expecting when asked. A
	// look for one of its reporting points ends when the aircraft is told to
	// expect another, even one with a reporting point of the same identifier.
	ApproachId string
}

// FutureTrafficCheck is enqueued when a pilot says "looking" in response to
// a traffic call. At fire time visibility is re-checked; if the pilot still
// can't see the traffic, the check is rescheduled a few seconds out.
type FutureTrafficCheck struct {
	TrafficCallsign av.ADSBCallsign
	Time            Time
}

func (s *Sim) processFutureFieldChecks() {
	for callsign, f := range util.SortedMap(s.FutureFieldChecks) {
		if !s.State.SimTime.After(f.Time) {
			continue
		}
		ac, ok := s.Aircraft[callsign]
		rp := f.ReportingPoint
		if !ok || ac.ControllerFrequency == "" ||
			(rp == nil && ac.FieldInSight) ||
			(rp != nil && ac.SightedReportingPoint != nil && ac.SightedReportingPoint.Id == rp.Id) ||
			(rp != nil && f.ApproachId != ac.Nav.Approach.AssignedId) ||
			(!f.ClearedWhenAsked && ac.Nav.Approach.EffectivelyCleared()) {
			delete(s.FutureFieldChecks, callsign)
			continue
		}

		if s.checkVisibility(ac, sightingLocation(ac, rp)).Visible {
			if rp != nil {
				ac.SightedReportingPoint = rp
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionReportingPointInSight)
			} else {
				ac.FieldInSight = true
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionFieldInSight)
			}
			delete(s.FutureFieldChecks, callsign)
		} else {
			f.Time = f.Time.Add(s.Rand.DurationRange(7*time.Second, 15*time.Second)) // try again in a bit
		}
	}
}

func (s *Sim) processFutureTrafficChecks() {
	for callsign, f := range util.SortedMap(s.FutureTrafficChecks) {
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

// VisualEligibility describes whether an aircraft can see a point on the
// ground near its arrival airport: the field itself or one of the reporting
// points of a charted visual approach.
type VisualEligibility struct {
	Visible  bool // true if VMC, within range, and the point is in view
	Reason   visualEligibilityReason
	Distance float32
	MaxRange float32
	Bearing  math.MagneticHeading
}

// checkAirportVisibility determines whether the aircraft can see the field.
func (s *Sim) checkAirportVisibility(ac *Aircraft) VisualEligibility {
	return s.checkVisibility(ac, ac.ArrivalAirportLocation())
}

// checkVisibility determines whether the aircraft can see the point p on
// the ground near its arrival airport; the weather there governs.
func (s *Sim) checkVisibility(ac *Aircraft, p math.Point2LL) VisualEligibility {
	apLoc := ac.ArrivalAirportLocation()
	apElev := ac.ArrivalAirportElevation()

	// Must be VMC at the arrival airport. The arrival airport may not be one of
	// the sim's airports (e.g. a satellite field a VFR is headed for), in which
	// case there's no METAR for it; fall back to the nearest one.
	metar, ok := s.State.METAR[ac.ArrivalAirport]
	if !ok {
		metar, _ = s.nearestMETAR(apLoc)
		ok = metar.ICAO != ""
	}
	// A zero METAR reports IMC, so only take its word for it if we found one.
	if ok && !metar.IsVMC() {
		return VisualEligibility{Reason: visualEligibilityIMC}
	}

	// Aircraft above the ceiling is in the clouds → can't see the ground.
	if ceiling, err := metar.Ceiling(); err == nil {
		if ac.Altitude() > apElev+float32(ceiling) {
			return VisualEligibility{Reason: visualEligibilityIMC}
		}
	}

	// Must be within effective visual range (METAR visibility + altitude bonus).
	altAGL := max(0, ac.Altitude()-apElev)

	maxRange := metar.EffectiveVisualRange(altAGL, 0)
	dist := math.NMDistance2LL(ac.Position(), p)
	if dist > maxRange {
		reason := util.Select(metar.HasObscuration(), visualEligibilityObscured, visualEligibilityOutOfRange)
		return VisualEligibility{
			Distance: dist,
			MaxRange: maxRange,
			Reason:   reason,
		}
	}

	// The point must be within the pilot's forward visibility arc.
	bearing := ac.bearingTo(p)
	if math.HeadingDifference(ac.Heading(), bearing) > visualMaxBearingOff {
		return VisualEligibility{
			Distance: dist,
			MaxRange: maxRange,
			Bearing:  bearing,
			Reason:   visualEligibilityBadBearing,
		}
	}

	return VisualEligibility{
		Visible:  true,
		Reason:   visualEligibilityOK,
		Distance: dist,
		MaxRange: maxRange,
		Bearing:  bearing,
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

	// Above this height AGL, arrivals won't *spontaneously* report the field or a
	// reporting point in sight; they wait until descended into the terminal
	// environment. Does not affect controller-prompted (AP/RP command) reports.
	maxSpontaneousFieldInSightAGL = 8000 // ft AGL

	// Pilots expecting a charted visual approach only spontaneously report one
	// of its reporting points in sight when it is ahead of them: within this
	// range and this many degrees of the nose.
	spontaneousReportingPointMinNM         = 3
	spontaneousReportingPointMaxNM         = 10
	spontaneousReportingPointMaxBearingOff = 45

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
		if s.checkAirportVisibility(ac).Visible {
			ac.FieldInSight = true
			ac.RequestedVisualApproach = true
			s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionRequestVisual)
		}
		ac.VisualApproachRequestDistance = 0
	} else if ac.WantsVisualApproach && s.checkAirportVisibility(ac).Visible {
		ac.FieldInSight = true
		s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionSpontaneousFieldInSight)
	}
}

// checkSpontaneousReportingPoint has a pilot who is inclined to report things
// in sight (WantsVisualApproach) report a reporting point of the charted
// visual approach they are expecting once one is in view ahead of them.
func (s *Sim) checkSpontaneousReportingPoint(ac *Aircraft) {
	if !ac.WantsVisualApproach || ac.SightedReportingPoint != nil || ac.ControllerFrequency == "" {
		return
	}
	points := ac.expectedReportingPoints()
	if len(points) == 0 || ac.Nav.Approach.EffectivelyCleared() ||
		ac.Altitude()-ac.ArrivalAirportElevation() > maxSpontaneousFieldInSightAGL ||
		s.hasPendingCheckIn(ac.ADSBCallsign) {
		return
	}

	for _, rp := range points {
		d := math.NMDistance2LL(ac.Position(), rp.Location.Point2LL)
		if d < spontaneousReportingPointMinNM || d > spontaneousReportingPointMaxNM ||
			math.HeadingDifference(ac.Heading(), ac.bearingTo(rp.Location.Point2LL)) > spontaneousReportingPointMaxBearingOff {
			continue
		}
		if s.checkVisibility(ac, rp.Location.Point2LL).Visible {
			ac.SightedReportingPoint = rp
			s.enqueuePilotTransmission(ac.ADSBCallsign, ac.ControllerFrequency, PendingTransmissionSpontaneousReportingPointInSight)
			return
		}
	}
}

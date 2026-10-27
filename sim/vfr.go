// sim/vfr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// processInterfacilityVFR handles auto-association of interfacility VFR
// plans (5.5.13). When an unassociated LocalEnroute VFR plan shares an
// ACID with an associated track, it means the interfacility VFR command
// created a NAS VFR plan for that track. After a delay simulating ARTCC
// processing, the VFR plan auto-associates with the track, replacing the
// old local plan and transferring the NAS beacon code.
func (s *Sim) processInterfacilityVFR(now Time) {
	for i, vfrFP := range slices.Backward(s.STARSComputer.FlightPlans) {

		if vfrFP.PlanType != LocalEnroute || vfrFP.Rules != av.FlightRulesVFR {
			continue
		}
		if vfrFP.CoordinationTime.IsZero() || now.Sub(vfrFP.CoordinationTime) < 4*time.Second {
			continue
		}

		// Find the associated track with matching ACID.
		for _, ac := range s.Aircraft {
			if !ac.IsAssociated() || ac.FlightPlan.ACID != vfrFP.ACID {
				continue
			}

			// Clean up old local plan: return its local squawk to the pool.
			oldFP := ac.FlightPlan
			s.LocalCodePool.Return(oldFP.AssignedSquawk)
			s.STARSComputer.returnListIndex(oldFP.ListIndex)
			if s.CIDAllocator != nil && oldFP.CID != "" {
				if err := s.CIDAllocator.Release(oldFP.CID); err != nil {
					s.lg.Warnf("%s: %v", oldFP.CID, err)
				}
			}
			if oldFP.StripOwner != "" {
				s.freeStripCID(oldFP.StripCID)
			}

			// Remove VFR plan from unassociated list and associate with track.
			s.STARSComputer.FlightPlans = slices.Delete(s.STARSComputer.FlightPlans, i, i+1)
			ac.AssociateFlightPlan(vfrFP)

			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: vfrFP.ACID,
			})
			break
		}
	}
}

func (s *Sim) RequestFlightFollowing() error {
	if err := s.requestRandomFlightFollowing(); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) TriggerEmergency(name string) {
	// Find the specified emergency type by name
	if idx := slices.IndexFunc(s.State.Emergencies, func(em Emergency) bool { return em.Name == name }); idx == -1 {
		s.lg.Error("triggerEmergency: emergency not found", "name", name)
	} else {
		s.triggerEmergency(idx)
	}
	s.publish()
}

func (s *Sim) requestRandomFlightFollowing() error {
	candidates := make(map[av.ADSBCallsign]TCP)

	for ac := range util.SortedMapValues(s.Aircraft) {
		if ac.IsAssociated() || ac.FlightRules != av.FlightRulesVFR || ac.RequestedFlightFollowing || !ac.IsAirborne() {
			continue
		}
		if ac.Altitude() < ac.DepartureAirportElevation()+500 &&
			math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()) < 1 {
			// Barely off the ground at the departure airport.
			continue
		}
		if math.NMDistance2LL(ac.Position(), ac.ArrivalAirportLocation()) < 15 {
			// It's landing soon, so never mind.
			continue
		}
		if ac.WillDoAirwork() {
			// Aircraft doing airwork won't call in for flight following.
			continue
		}
		if ac.TouchAndGosRemaining > 0 {
			continue
		}

		for tcpStr, cc := range util.SortedMap(s.State.FacilityAdaptation.Positions) {
			tcp := s.State.ResolveController(TCP(tcpStr))
			if s.isVirtualController(tcp) {
				continue
			}
			for _, vol := range cc.FlightFollowingAirspace {
				if vol.Inside(ac.Position(), int(ac.Altitude())) {
					candidates[ac.ADSBCallsign] = tcp // first come, first served
					break
				}
			}
		}
	}

	if len(candidates) == 0 {
		return ErrNoVFRAircraftForFlightFollowing
	}

	cs, ok := rand.SampleSeq(s.Rand, slices.Values(util.SortedMapKeys(candidates)))
	if !ok {
		return ErrNoVFRAircraftForFlightFollowing
	}

	s.requestFlightFollowing(s.Aircraft[cs], candidates[cs])

	return nil
}

func (s *Sim) possiblyRequestFlightFollowing() {
	if s.prespawn || s.State.SimTime.Before(s.NextVFFRequest) {
		return
	}

	// Attempt to find an aircraft and make a request
	if err := s.requestRandomFlightFollowing(); err != nil {
		// No candidates; back off a bit before trying again
		s.NextVFFRequest = s.State.SimTime.Add(15 * time.Second)
	} else {
		s.NextVFFRequest = s.State.SimTime.Add(randomWait(float32(s.State.LaunchConfig.VFFRequestRate), false, s.Rand))
	}
}

func (s *Sim) requestFlightFollowing(ac *Aircraft, tcp TCP) {
	ac.RequestedFlightFollowing = true
	s.setControllerFrequency(ac, ControlPosition(tcp))

	// About 90% of the time, make an abbreviated request and wait for "go ahead"
	if s.Rand.Float32() < 0.9 {
		ac.WaitingForGoAhead = true
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingReq)
	} else {
		// Full flight following request
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingFull)
	}
}

// vfrPositionReference is what a VFR pilot describes their position relative
// to in a flight following request: an airport, a VOR given by its
// identifier, or a scenario reporting point.
type vfrPositionReference struct {
	Airport     av.ICAOAirportCode
	VOR         string
	Description string
	Location    math.Point2LL
}

// isReferenceAirport reports whether a database airport is one a pilot might
// name in a position report: it must come from the CIFP, which is what gives
// it runways, and not be a heliport or a minor strip.
func isReferenceAirport(ap db.Airport) bool {
	if ap.Name == "" || len(ap.Runways) == 0 {
		return false
	}
	return !slices.ContainsFunc([]string{"Airstrip", "Airpark", "Balloonport", "Base", "Field", "Heliport", "Helistop", "Helipad", "Strip"},
		func(s string) bool { return strings.HasSuffix(ap.Name, s) })
}

// vfrPositionReference chooses the point a VFR pilot reports their position
// relative to. A point the controller knows well beats a nearer obscure
// one: the departure airport, the scenario's reporting points, and then the
// scenario's airports together with VORs are each accepted within a radius
// that grows with how familiar they are. Failing all of those, the closest
// airport in the database is used. A VOR on a scenario airport's field is
// that airport, so the airport is named rather than the VOR.
func (s *Sim) vfrPositionReference(ac *Aircraft) vfrPositionReference {
	pos := ac.Position()
	closest := func(cands ...iter.Seq[vfrPositionReference]) (ref vfrPositionReference, dist float32, ok bool) {
		for _, seq := range cands {
			for c := range seq {
				if d := math.NMDistance2LLFast(pos, c.Location, s.State.NmPerLongitude); !ok || d < dist {
					ref, dist, ok = c, d, true
				}
			}
		}
		return
	}

	dep := vfrPositionReference{Airport: ac.DepartureAirport, Location: ac.DepartureAirportLocation()}
	if math.NMDistance2LLFast(pos, dep.Location, s.State.NmPerLongitude) <= 3 {
		return dep
	}

	rps := util.MapSlice(s.VFRReportingPoints, func(rp av.VFRReportingPoint) vfrPositionReference {
		return vfrPositionReference{Description: rp.Description, Location: rp.Location.Point2LL}
	})
	if ref, d, ok := closest(slices.Values(append(rps, dep))); ok && d <= 7 {
		return ref
	}

	scenario := util.MapSeq(maps.Keys(s.State.Airports), func(icao av.ICAOAirportCode) vfrPositionReference {
		return vfrPositionReference{Airport: icao, Location: s.State.Airports[icao].Location}
	})
	// The reference points of the largest airports are 3 miles from the
	// VORs on their fields.
	onScenarioAirport := func(p math.Point2LL) bool {
		for _, ap := range s.State.Airports {
			if math.NMDistance2LLFast(p, ap.Location, s.State.NmPerLongitude) < 4 {
				return true
			}
		}
		return false
	}
	vors := util.FilterSeq(maps.Values(db.DB.Navaids), func(na db.Navaid) bool {
		return na.Type == "VOR" && !onScenarioAirport(na.Location)
	})
	vorRefs := util.MapSeq(vors, func(na db.Navaid) vfrPositionReference {
		return vfrPositionReference{VOR: na.Id, Location: na.Location}
	})
	if ref, d, ok := closest(scenario, vorRefs); ok && d <= 10 {
		return ref
	}

	database := func(yield func(vfrPositionReference) bool) {
		for icao, ap := range db.DB.Airports {
			if isReferenceAirport(ap) && !yield(vfrPositionReference{Airport: icao, Location: ap.Location}) {
				return
			}
		}
	}
	if ref, _, ok := closest(database); ok {
		return ref
	}
	return dep
}

// generateFlightFollowingMessage creates the full flight following request message
// from the aircraft's current state, making its choices of what to say with r.
func (s *Sim) generateFlightFollowingMessage(ac *Aircraft, r *rand.Rand) *speech.RadioTransmission {
	rt := speech.MakeContactTransmission("[we're a|] {actype}", ac.AircraftType)

	ref := s.vfrPositionReference(ac)
	dist := math.NMDistance2LL(ac.Position(), ref.Location)
	if dist < 2 && ref.Airport == ac.DepartureAirport {
		rt.Add("departing {airport}", ac.DepartureAirport)
	} else {
		var loc string
		dir := math.Compass(math.Heading2LL(ref.Location, ac.Position(), s.State.NmPerLongitude))
		if dist < 1 {
			loc = "overhead"
		} else if nm := int(dist + 0.5); nm == 1 {
			loc = "one mile " + dir + " of"
		} else {
			loc = strconv.Itoa(nm) + " miles " + dir + " of"
		}
		switch {
		case ref.Airport != "":
			rt.Add(loc+" {airport}", ref.Airport)
		case ref.VOR != "":
			rt.Add(loc+" {fix} VOR", ref.VOR)
		default:
			rt.Add(loc + " " + ref.Description)
		}
	}

	var alt *speech.RadioTransmission
	// Get the aircraft's target altitude from the navigation system
	targetAlt, _, _ := ac.Nav.TargetAltitude()
	currentAlt := ac.Altitude()

	// Check if we're in a climb or descent (more than 100 feet difference)
	if currentAlt < targetAlt {
		// Report current altitude and target altitude when climbing or descending
		alt = speech.MakeContactTransmission("[at|] {alt} for {alt}", currentAlt, targetAlt)
	} else {
		// Just report current altitude if we're level
		alt = speech.MakeContactTransmission("at {alt}", currentAlt)
	}
	earlyAlt := r.Bool()
	if earlyAlt {
		rt.Merge(alt)
	}

	if r.Bool() {
		// Heading only sometimes
		rt.Add(math.Compass(ac.Heading()) + "bound")
	}

	rt.Add("[looking for flight-following|request flight-following|request radar advisories|request advisories] to {airport}",
		ac.ArrivalAirport)

	if !earlyAlt {
		rt.Merge(alt)
	}

	rt.Type = speech.RadioTransmissionContact
	return rt
}

func (s *Sim) ChangeSquawk(tcw TCW, callsign av.ADSBCallsign, sq av.Squawk,
	delayReduction time.Duration) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			s.enqueueTransponderChange(ac.ADSBCallsign, sq, ac.Mode, delayReduction)

			return speech.TransponderIntent{Code: &sq}
		})
}

func (s *Sim) ChangeTransponderMode(tcw TCW, callsign av.ADSBCallsign, mode av.TransponderMode,
	delayReduction time.Duration) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			s.enqueueTransponderChange(ac.ADSBCallsign, ac.Squawk, mode, delayReduction)

			return speech.TransponderIntent{Mode: &mode}
		})
}

func (s *Sim) Ident(tcw TCW, callsign av.ADSBCallsign, delayReduction time.Duration) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			return ac.Ident(s.State.SimTime, delayReduction, s.Rand)
		})
}

func (s *Sim) ResumeOwnNavigation(tcw TCW, callsign av.ADSBCallsign) (speech.CommandIntent, error) {
	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) speech.CommandIntent {
			return ac.ResumeOwnNavigation()
		})
}

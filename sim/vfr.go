// sim/vfr.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"slices"
	"strconv"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
)

// processInterfacilityVFR handles auto-association of interfacility VFR
// plans (5.5.13). When an unassociated LocalEnroute VFR plan shares an
// ACID with an associated track, it means the interfacility VFR command
// created a NAS VFR plan for that track. After a delay simulating ARTCC
// processing, the VFR plan auto-associates with the track, replacing the
// old local plan and transferring the NAS beacon code.
func (s *Sim) processInterfacilityVFR(now Time) {
	for i := len(s.STARSComputer.FlightPlans) - 1; i >= 0; i-- {
		vfrFP := s.STARSComputer.FlightPlans[i]
		if vfrFP.PlanType != LocalEnroute || vfrFP.Rules != av.FlightRulesVFR {
			continue
		}
		if vfrFP.CoordinationTime.IsZero() || now.Sub(vfrFP.CoordinationTime) < 4*time.Second {
			continue
		}

		// Find the associated track with matching ACID.
		for _, ac := range s.Aircraft {
			if !ac.IsAssociated() || ac.NASFlightPlan.ACID != vfrFP.ACID {
				continue
			}

			// Clean up old local plan: return its local squawk to the pool.
			oldFP := ac.NASFlightPlan
			s.LocalCodePool.Return(oldFP.AssignedSquawk)
			s.STARSComputer.returnListIndex(oldFP.ListIndex)
			if s.CIDAllocator != nil && oldFP.CID != "" {
				s.CIDAllocator.Release(oldFP.CID)
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
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if err := s.requestRandomFlightFollowing(); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) TriggerEmergency(name string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Find the specified emergency type by name
	if idx := slices.IndexFunc(s.State.Emergencies, func(em Emergency) bool { return em.Name == name }); idx == -1 {
		s.lg.Error("triggerEmergency: emergency not found", "name", name)
	} else {
		s.triggerEmergency(idx)
	}
	s.publish()
}

func (s *Sim) requestRandomFlightFollowing() error {
	candidates := make(map[*Aircraft]TCP)

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() || ac.FlightPlan.Rules != av.FlightRulesVFR || ac.RequestedFlightFollowing || !ac.IsAirborne() {
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

		for tcpStr, cc := range s.State.FacilityAdaptation.Controllers {
			tcp := s.State.ResolveController(TCP(tcpStr))
			if s.isVirtualController(tcp) {
				continue
			}
			for _, vol := range cc.FlightFollowingAirspace {
				if vol.Inside(ac.Position(), int(ac.Altitude())) {
					candidates[ac] = tcp // first come, first served
					break
				}
			}
		}
	}

	if len(candidates) == 0 {
		return ErrNoVFRAircraftForFlightFollowing
	}

	ac, ok := rand.SampleSeq(s.Rand, maps.Keys(candidates))
	if !ok {
		return ErrNoVFRAircraftForFlightFollowing
	}

	s.requestFlightFollowing(ac, candidates[ac])

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
	ac.ControllerFrequency = ControlPosition(tcp)

	// About 90% of the time, make an abbreviated request and wait for "go ahead"
	if s.Rand.Float32() < 0.9 {
		ac.WaitingForGoAhead = true
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingReq)
	} else {
		// Full flight following request
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingFull)
	}
}

// generateFlightFollowingMessage creates the full flight following request message.
// This is called on-demand to use current aircraft state.
func (s *Sim) generateFlightFollowingMessage(ac *Aircraft) *av.RadioTransmission {
	closestReportingPoint := func(ac *Aircraft) (string, string, float32, bool) {
		var closest *av.VFRReportingPoint
		dist := float32(1000000)
		var center math.Point2LL
		for _, rp := range s.VFRReportingPoints {
			d := math.NMDistance2LL(ac.Position(), rp.Location)
			if d != 0 && d < dist {
				dist = d
				center = rp.Location
				closest = &rp
			}
		}

		if closest != nil {
			// Possibly override with the departure airport, if we're still
			// close to it.  Note that we don't automatically consider the
			// departure airport as a candidate as it may be well outside
			// the TRACON.
			if d := math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()); d < dist {
				hdg := math.Heading2LL(ac.DepartureAirportLocation(), ac.Position(), s.State.NmPerLongitude)
				return ac.FlightPlan.DepartureAirport, math.Compass(hdg), d, true
			} else {
				hdg := math.Heading2LL(center, ac.Position(), s.State.NmPerLongitude)
				return closest.Description, math.Compass(hdg), dist, false
			}
		}
		return "", "", 0, false
	}

	rt := av.MakeContactTransmission("[we're a|] {actype}", ac.FlightPlan.AircraftType)

	rpdesc, rpdir, dist, isap := closestReportingPoint(ac)
	if math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()) < 2 {
		rt.Add("departing {airport}", ac.FlightPlan.DepartureAirport)
	} else if dist < 1 {
		if isap {
			rt.Add("overhead {airport}", rpdesc)
		} else {
			rt.Add("overhead " + rpdesc)
		}
	} else {
		nm := int(dist + 0.5)
		var loc string
		if nm == 1 {
			loc = "one mile " + rpdir
		} else {
			loc = strconv.Itoa(int(dist+0.5)) + " miles " + rpdir
		}
		if isap {
			rt.Add(loc+" of {airport}", rpdesc)
		} else {
			rt.Add(loc + " of " + rpdesc)
		}
	}

	var alt *av.RadioTransmission
	// Get the aircraft's target altitude from the navigation system
	targetAlt, _, _ := ac.Nav.TargetAltitude()
	currentAlt := ac.Altitude()

	// Check if we're in a climb or descent (more than 100 feet difference)
	if currentAlt < targetAlt {
		// Report current altitude and target altitude when climbing or descending
		alt = av.MakeContactTransmission("[at|] {alt} for {alt}", currentAlt, targetAlt)
	} else {
		// Just report current altitude if we're level
		alt = av.MakeContactTransmission("at {alt}", currentAlt)
	}
	earlyAlt := s.Rand.Bool()
	if earlyAlt {
		rt.Merge(alt)
	}

	if s.Rand.Bool() {
		// Heading only sometimes
		rt.Add(math.Compass(ac.Heading()) + "bound")
	}

	rt.Add("[looking for flight-following|request flight-following|request radar advisories|request advisories] to {airport}",
		ac.FlightPlan.ArrivalAirport)

	if !earlyAlt {
		rt.Merge(alt)
	}

	rt.Type = av.RadioTransmissionContact
	return rt
}

func (s *Sim) ChangeSquawk(tcw TCW, callsign av.ADSBCallsign, sq av.Squawk) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			s.enqueueTransponderChange(ac.ADSBCallsign, sq, ac.Mode)

			return av.TransponderIntent{Code: &sq}
		})
}

func (s *Sim) ChangeTransponderMode(tcw TCW, callsign av.ADSBCallsign, mode av.TransponderMode) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			s.enqueueTransponderChange(ac.ADSBCallsign, ac.Squawk, mode)

			return av.TransponderIntent{Mode: &mode}
		})
}

func (s *Sim) Ident(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.Ident(s.State.SimTime)
		})
}

func (s *Sim) ResumeOwnNavigation(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.ResumeOwnNavigation()
		})
}

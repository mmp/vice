// sim/handoff.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

func (s *Sim) RepositionTrack(tcw TCW, acid ACID, callsign av.ADSBCallsign, p math.Point2LL) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	// If associating with an active track, validate the target aircraft
	// BEFORE taking the flight plan from its source. This prevents orphaning
	// the flight plan if the target is invalid.
	var targetAC *Aircraft
	if callsign != "" {
		ac, ok := s.Aircraft[callsign]
		if !ok {
			return ErrNoMatchingFlight
		}
		if ac.IsAssociated() {
			return ErrTrackIsActive
		}
		targetAC = ac
	}

	// Find the corresponding flight plan.
	var fp *NASFlightPlan
	// First look for the referenced flight plan in associated aircraft.
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			if !s.TCWCanModifyTrack(tcw, ac.NASFlightPlan) {
				return av.ErrOtherControllerHasTrack
			} else if ac.NASFlightPlan.HandoffController != "" {
				return ErrTrackIsBeingHandedOff
			} else {
				fp = ac.DisassociateFlightPlan()
				break
			}
		}
	}
	if fp == nil {
		// Try unsupported DBs if we didn't find it there.
		for i, sfp := range s.STARSComputer.FlightPlans {
			if !sfp.Location.IsZero() && sfp.ACID == acid {
				if !s.TCWCanModifyTrack(tcw, sfp) {
					return av.ErrOtherControllerHasTrack
				} else if sfp.HandoffController != "" {
					return ErrTrackIsBeingHandedOff
				} else {
					fp = sfp
					s.STARSComputer.FlightPlans = slices.Delete(s.STARSComputer.FlightPlans, i, i+1)
					break
				}
			}
		}
	}
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	fp.Location = math.Point2LL{}

	// These are cleared when a track is repositioned.
	if fp.Rules == av.FlightRulesIFR {
		fp.DisableMSAW = false
		fp.DisableCA = false
		// TODO: clear CA inhibit pair
	}

	if targetAC != nil { // Associating it with an active track
		targetAC.AssociateFlightPlan(fp)
		if s.State.IsLocalController(fp.TrackingController) {
			fp.LastLocalController = fp.TrackingController
		}

		s.eventStream.Post(Event{
			Type: FlightPlanAssociatedEvent,
			ACID: fp.ACID,
		})
	} else { // Creating / moving an unsupported DB.
		fp.Location = p
		fp.OwningTCW = tcw
		s.STARSComputer.FlightPlans = append(s.STARSComputer.FlightPlans, fp)
	}
	s.publish()
	return nil
}

func (s *Sim) HandoffTrack(tcw TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if err := s.dispatchTrackedFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			// Resolve the target TCP - it may be consolidated to another controller
			resolvedTCP := s.State.ResolveController(toTCP)
			if _, ok := s.State.Controllers[resolvedTCP]; !ok {
				return av.ErrNoController
			} else if s.State.TCWControlsPosition(tcw, toTCP) {
				// Can't handoff to any position we control (primary or consolidated secondary)
				return av.ErrInvalidController
			} else if ac != nil {
				// Disallow handoff if there's a beacon code mismatch.
				squawkingSPC, _ := ac.Squawk.IsSPC()
				if ac.Squawk != ac.NASFlightPlan.AssignedSquawk && !squawkingSPC {
					return ErrBeaconMismatch
				}
			}

			// Can't hand off a local flight plan to an external facility.
			toCtrl := s.State.Controllers[resolvedTCP]
			if toCtrl.IsExternal() &&
				(fp.PlanType == LocalNonEnroute || fp.PlanType == RemoteNonEnroute) {
				return ErrIllegalTrackLocalFP
			}

			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			// Pass the original toTCP so HandoffTrackController records the actual target position
			s.handoffTrack(fp, toTCP)
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) handoffTrack(fp *NASFlightPlan, toTCP TCP) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fp.TrackingController,
		ToController:   toTCP,
	})

	fp.HandoffController = toTCP

	// Resolve the target TCP - it may be consolidated to another controller
	resolvedTCP := s.State.ResolveController(toTCP)
	if _, ok := s.State.Controllers[resolvedTCP]; !ok {
		s.lg.Errorf("Unable to handoff %s: to controller %q (resolved: %q) not found", fp.ACID, toTCP, resolvedTCP)
	}

	// Add them to the auto-accept map even if the target controller is
	// currently signed in; this way, if they sign off in the interim, we
	// still end up accepting it automatically.
	s.Handoffs[fp.ACID] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(s.Rand.DurationRange(4*time.Second, 14*time.Second)),
	}
	// If both controllers are virtual, send the departure on course (mainly so it climbs to cruise)
	resolvedFrom := s.State.ResolveController(fp.TrackingController)
	if fp.TypeOfFlight == av.FlightTypeDeparture && s.isVirtualController(resolvedFrom) && s.isVirtualController(resolvedTCP) {
		if callsign, ok := s.callsignForACID(fp.ACID); ok {
			// aircraft is a departure that will likely never talk to a human, send it on course (mainly so it climbs up to cruise)
			s.enqueueDepartOnCourse(callsign)
		}
	}
}

func (s *Sim) ContactTrackingController(tcw TCW, acid ACID) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, sfp *NASFlightPlan, ac *Aircraft) error {
			if ac == nil {
				return av.ErrNoAircraftForCallsign
			}
			if !s.TCWCanCommandAircraft(tcw, ac) {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcw TCW, sfp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			return s.contactController(s.State.PrimaryPositionForTCW(tcw), sfp, ac, sfp.TrackingController)
		})
}

func (s *Sim) ContactController(tcw TCW, acid ACID, toTCP TCP) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, sfp *NASFlightPlan, ac *Aircraft) error {
			if ac == nil {
				return av.ErrNoAircraftForCallsign
			}
			if !s.TCWCanCommandAircraft(tcw, ac) {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcw TCW, sfp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			if s.State.TCWControlsPosition(tcw, toTCP) {
				return av.MakeUnableIntent("Unable, we are already on your frequency")
			} else {
				return s.contactController(s.State.PrimaryPositionForTCW(tcw), sfp, ac, toTCP)
			}
		})
}

func (s *Sim) contactController(fromTCP TCP, sfp *NASFlightPlan, ac *Aircraft, toTCP TCP) av.CommandIntent {
	// Immediately respond to the current controller that we're
	// changing frequency.
	var intent av.ContactIntent
	if octrl, ok := s.State.Controllers[toTCP]; ok {
		if toTCP == fromTCP {
			return av.MakeUnableIntent("Unable, we are already on {freq}", octrl.Frequency)
		}
		intent = av.ContactIntent{
			Type:         av.ContactController,
			ToController: octrl,
			Frequency:    octrl.Frequency,
			IsDeparture:  ac.TypeOfFlight == av.FlightTypeDeparture,
		}
	} else {
		intent = av.ContactIntent{
			Type: av.ContactGoodbye,
		}
	}

	// Move the flight strip to the destination TCP.
	sfp.StripOwner = toTCP

	// Cancel any in-progress frequency switch and take away the
	// current controller's ability to issue control commands.
	s.cancelFutureFrequencyChange(ac.ADSBCallsign)
	ac.ControllerFrequency = ""

	// A human explicitly directing the pilot supersedes any virtual
	// controller deferred contact chain.
	delete(s.DeferredContacts, ac.ADSBCallsign)

	s.enqueueControllerContact(ac, toTCP, ControlPosition(fromTCP))

	return intent
}

func (s *Sim) AcceptHandoff(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if fp.RedirectedHandoff.RedirectedTo != "" {
				// Once redirected, the handoff can only be accepted (or
				// recalled) via the redirected handoff path.
				return av.ErrNotBeingHandedOffToMe
			}
			// Check if the caller's TCW controls the HandoffTrackController TCP (consolidation-aware)
			if s.State.TCWControlsPosition(tcw, fp.HandoffController) {
				return nil
			}
			if _, ok := s.findInboundPointOut(fp.ACID, tcw); ok {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			// The new tracking controller should be the HandoffTrackController (the target TCP),
			// not the acceptor's primary TCP. This preserves correct ownership when accepting
			// handoffs to consolidated secondary positions.
			newTrackingController := fp.HandoffController
			if po, ok := s.findInboundPointOut(fp.ACID, tcw); ok {
				// Point out accepted as handoff - use the point-out target
				newTrackingController = po.ToController
			}

			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				ACID:           fp.ACID,
				FromController: fp.TrackingController,
				ToController:   newTrackingController,
			})

			previousTrackingController := fp.TrackingController

			fp.HandoffController = ""
			fp.HandoffWasAutomatic = false
			fp.TrackingController = newTrackingController
			fp.LastLocalController = newTrackingController
			fp.OwningTCW = tcw // The accepting TCW owns the track

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, acid)

			if ac != nil {
				haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
					func(wp av.Waypoint) bool { return wp.HasTransferCommsAction() })
				if !haveTransferComms && s.isVirtualController(previousTrackingController) {
					// For a handoff from a virtual controller, transfer
					// comms only if the pilot is on the virtual's
					// frequency; otherwise defer until they arrive.
					s.virtualControllerTransferComms(ac, TCP(previousTrackingController), TCP(newTrackingController))
				}
			}
			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) CancelHandoff(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if err := s.dispatchTrackedFlightPlanCommand(tcw, acid, nil,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			// Recalling an *automatic* handoff makes the track ineligible for further auto-handoff
			// (STARS 5.1.17, p. 5-33).
			if fp.HandoffWasAutomatic {
				fp.AutoHandoffInhibited = true
				fp.HandoffWasAutomatic = false
			}
			delete(s.Handoffs, acid)
			fp.HandoffController = ""
			fp.RedirectedHandoff = RedirectedHandoff{}
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) RedirectHandoff(tcw TCW, acid ACID, controller TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			primaryTCP := s.State.PrimaryPositionForTCW(tcw)
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if s.State.TCWControlsPosition(tcw, TCP(octrl.PositionId())) || TCP(octrl.PositionId()) == fp.TrackingController {
				// Can't redirect to ourself (including consolidated positions) or the controller who initiated the handoff
				return av.ErrInvalidController
			} else if ctrl, ok := s.State.Controllers[primaryTCP]; !ok {
				return ErrUnknownController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return av.ErrInvalidFacility
			} else if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			primaryTCP := s.State.PrimaryPositionForTCW(tcw)
			octrl := s.State.Controllers[controller]
			rh := &fp.RedirectedHandoff
			rh.OriginalOwner = fp.TrackingController
			ctrl := s.State.Controllers[primaryTCP]
			if rh.ShouldFallbackToHandoff(primaryTCP, TCP(octrl.PositionId())) {
				fp.HandoffController = rh.Redirector[0]
				*rh = RedirectedHandoff{}
				return nil
			}
			rh.AddRedirector(ctrl)
			rh.RedirectedTo = TCP(octrl.PositionId())

			if s.isVirtualController(rh.RedirectedTo) {
				// A virtual controller accepts the redirected handoff
				// after a short delay, as with regular handoffs.
				s.Handoffs[fp.ACID] = Handoff{
					AutoAcceptTime: s.State.SimTime.Add(s.Rand.DurationRange(4*time.Second, 14*time.Second)),
				}
			}

			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) AcceptRedirectedHandoff(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			rh := &fp.RedirectedHandoff
			if s.State.TCWControlsPosition(tcw, rh.RedirectedTo) { // Accept
				s.acceptRedirectedHandoff(fp, ac, tcw)
			} else if s.State.TCWControlsPosition(tcw, rh.GetLastRedirector()) { // Recall (only the last redirector is able to recall)
				if len(rh.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					rh.RedirectedTo = rh.Redirector[len(rh.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					fp.HandoffController = rh.Redirector[len(rh.Redirector)-1]
					*rh = RedirectedHandoff{}
				}
			}

			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

// acceptRedirectedHandoff updates the flight plan for an accepted redirected
// handoff, whether accepted by a human controller or automatically by a
// virtual one. owningTCW is the TCW that takes ownership of the track. Comms
// follow the original handoff: if a virtual controller is talking to the
// aircraft, it sends it to the controller the handoff was first offered to,
// who can then pass it along to the accepting controller with an FC.
func (s *Sim) acceptRedirectedHandoff(fp *NASFlightPlan, ac *Aircraft, owningTCW TCW) {
	rh := &fp.RedirectedHandoff

	s.eventStream.Post(Event{
		Type:           AcceptedRedirectedHandoffEvent,
		FromController: rh.OriginalOwner,
		ToController:   rh.RedirectedTo,
		Redirectors:    rh.Redirector,
		ACID:           fp.ACID,
	})

	previousTrackingController := fp.TrackingController
	offeredToController := fp.HandoffController

	fp.HandoffController = ""
	fp.HandoffWasAutomatic = false
	fp.TrackingController = rh.RedirectedTo
	fp.LastLocalController = rh.RedirectedTo
	fp.OwningTCW = owningTCW
	*rh = RedirectedHandoff{}

	if ac != nil {
		haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
			func(wp av.Waypoint) bool { return wp.HasTransferCommsAction() })
		if !haveTransferComms && s.isVirtualController(previousTrackingController) && offeredToController != "" {
			s.virtualControllerTransferComms(ac, previousTrackingController, offeredToController)
		}
	}
}

func (s *Sim) ForceQL(tcw TCW, acid ACID, controller TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			// Per 6.12.6: force QL to the owning TCW's display requires
			// that the entering TCW owns the flight and ForceQLToSelf is adapted.
			if s.State.TCWControlsPosition(fp.OwningTCW, ControlPosition(controller)) {
				if !s.State.FacilityAdaptation.Datablocks.ForceQLToSelf || fp.OwningTCW != tcw {
					return ErrIllegalPosition
				}
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: s.State.PrimaryPositionForTCW(tcw),
				ToController:   TCP(octrl.PositionId()),
				ACID:           acid,
			})

			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) PointOut(fromTCW TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if err := s.dispatchTrackedFlightPlanCommand(fromTCW, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if octrl, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if octrl.IsExternal() && (fp.PlanType == LocalNonEnroute || fp.PlanType == RemoteNonEnroute) {
				// Can't point out a local flight plan to an external facility.
				return ErrIllegalTrackLocalFP
			} else if s.State.TCWControlsPosition(fromTCW, toTCP) {
				// Can't point out to ourself (including consolidated positions)
				return av.ErrInvalidController
			} else if fp.HandoffController != "" {
				// Can't point out if it's being handed off
				return ErrTrackIsBeingHandedOff
			}
			// STARS (per 6.12.1 / 6.12.7) rejects any PO initiation while a
			// track already has an active PO. ERAM permits concurrent POs.
			fromCtrl := s.State.Controllers[s.State.PrimaryPositionForTCW(fromTCW)]
			if fromCtrl != nil && !fromCtrl.ERAMFacility && len(s.PointOuts[acid]) > 0 {
				return ErrTrackHasActivePointOut
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			fromTCP := s.State.PrimaryPositionForTCW(fromTCW)
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(acid, ctrl, octrl)
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) pointOut(acid ACID, from *av.Controller, to *av.Controller) {
	// Always post the event
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.PositionId(),
		ToController:   to.PositionId(),
		ACID:           acid,
	})

	// But don't have duplicate entries in the PointOut slice for a repeated p/o.
	if !slices.ContainsFunc(s.PointOuts[acid], func(po PointOut) bool {
		return po.FromController == from.PositionId() && po.ToController == to.PositionId()
	}) {
		s.PointOuts[acid] = append(s.PointOuts[acid], PointOut{
			FromController: from.PositionId(),
			ToController:   to.PositionId(),
			AcceptTime:     s.State.SimTime.Add(s.Rand.DurationRange(4*time.Second, 14*time.Second)),
		})
	}
}

// findInboundPointOut returns the first pending PointOut whose ToController is
// controlled by tcw (an inbound point out the caller can act on).
func (s *Sim) findInboundPointOut(acid ACID, tcw TCW) (PointOut, bool) {
	for _, po := range s.PointOuts[acid] {
		if s.State.TCWControlsPosition(tcw, po.ToController) {
			return po, true
		}
	}
	return PointOut{}, false
}

func (s *Sim) deletePointOuts(acid ACID, match func(PointOut) bool) {
	s.PointOuts[acid] = slices.DeleteFunc(s.PointOuts[acid], match)
	if len(s.PointOuts[acid]) == 0 {
		delete(s.PointOuts, acid)
	}
}

func (s *Sim) AcknowledgePointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	acked := util.FilterSlice(s.PointOuts[acid], func(po PointOut) bool {
		return s.State.TCWControlsPosition(tcw, po.ToController)
	})

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if len(acked) == 0 {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			for _, po := range acked {
				// As with auto accepts, "to" and "from" are swapped in
				// the event since they are w.r.t. the original point out.
				s.eventStream.Post(Event{
					Type:           AcknowledgedPointOutEvent,
					FromController: po.ToController,
					ToController:   po.FromController,
					ACID:           acid,
				})
				fp.AddPointOutHistory(po.ToController)
			}

			s.deletePointOuts(acid, func(po PointOut) bool {
				return s.State.TCWControlsPosition(tcw, po.ToController)
			})

			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) RecallPointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	recalled := util.FilterSlice(s.PointOuts[acid], func(po PointOut) bool {
		return s.State.TCWControlsPosition(tcw, po.FromController)
	})

	if err := s.dispatchTrackedFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if len(recalled) == 0 {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			for _, po := range recalled {
				s.eventStream.Post(Event{
					Type:           RecalledPointOutEvent,
					FromController: po.FromController,
					ToController:   po.ToController,
					ACID:           acid,
				})
			}

			s.deletePointOuts(acid, func(po PointOut) bool {
				return s.State.TCWControlsPosition(tcw, po.FromController)
			})
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

func (s *Sim) RejectPointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	rejected := util.FilterSlice(s.PointOuts[acid], func(po PointOut) bool {
		return s.State.TCWControlsPosition(tcw, po.ToController)
	})

	if _, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if len(rejected) == 0 {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			for _, po := range rejected {
				// As with auto accepts, "to" and "from" are swapped in
				// the event since they are w.r.t. the original point out.
				s.eventStream.Post(Event{
					Type:           RejectedPointOutEvent,
					FromController: po.ToController,
					ToController:   po.FromController,
					ACID:           acid,
				})
			}

			s.deletePointOuts(acid, func(po PointOut) bool {
				return s.State.TCWControlsPosition(tcw, po.ToController)
			})

			return nil
		}); err != nil {
		return err
	}
	s.publish()
	return nil
}

// TODO: Migrate to ERAM computer.
func (s *Sim) SendRouteCoordinates(tcw TCW, acid ACID, minutes int) (err error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	defer func() {
		if err == nil {
			s.publish()
		}
	}()

	ac := s.Aircraft[av.ADSBCallsign(acid)]
	if ac == nil {
		return av.ErrNoAircraftForCallsign
	}

	// Get the aircraft's speed. TODO: Find out if ERAM uses current or filed speed.
	speed := ac.Nav.FlightState.GS

	waypoints := []av.Waypoint(ac.Nav.Waypoints)
	waypointPairs := []math.Point2LL{}
	for _, wyp := range waypoints {
		if _, ok := av.DB.LookupWaypoint(wyp.Fix); ok { // only send actual waypoints
			waypointPairs = append(waypointPairs, [2]float32{wyp.Location[0], wyp.Location[1]})
		}
	}

	if minutes == -1 {
		s.eventStream.Post(Event{
			Type:         FixCoordinatesEvent,
			ACID:         acid,
			WaypointInfo: waypointPairs,
			ToController: s.State.PrimaryPositionForTCW(tcw),
		})
		return nil
	}

	// Calculate the total distance required to be shown
	requiredDistance := speed * float32(minutes) / 60

	// Build the path starting from the aircraft's current position
	currentPos := ac.Nav.FlightState.Position
	nmPerLongitude := ac.Nav.FlightState.NmPerLongitude
	const nmPerLatitude float32 = 60

	var distance float32
	var futureWaypoints []math.Point2LL

	for _, wp := range waypointPairs {
		legDistance := math.NMDistance2LL(currentPos, wp)

		if distance+legDistance >= requiredDistance {
			// The endpoint is somewhere along this leg
			remainingDistance := requiredDistance - distance
			bearing := math.Heading2LL(currentPos, wp, nmPerLongitude)

			// Create a new waypoint at the calculated position
			location := math.Point2LL{
				currentPos[0] + remainingDistance*math.Sin(math.Radians(bearing))/nmPerLongitude,
				currentPos[1] + remainingDistance*math.Cos(math.Radians(bearing))/nmPerLatitude,
			}
			futureWaypoints = append(futureWaypoints, location)
			break
		}

		// Add this waypoint and continue
		futureWaypoints = append(futureWaypoints, wp)
		distance += legDistance
		currentPos = wp
	}

	s.eventStream.Post(Event{
		Type:         FixCoordinatesEvent,
		ACID:         acid,
		WaypointInfo: futureWaypoints,
		ToController: s.State.PrimaryPositionForTCW(tcw),
	})
	return nil
}

// TODO: Migrate to ERAM computer.
func (s *Sim) FlightPlanDirect(tcp TCP, fix string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	ac := s.Aircraft[av.ADSBCallsign(acid)]
	var success bool
	for i, wp := range ac.Nav.Waypoints {
		if wp.Fix == fix {
			// Remove all waypoints before the fix
			ac.Nav.Waypoints = ac.Nav.Waypoints[i:]
			success = true
			break
		}
	}

	if !success {
		return av.ErrNoMatchingFix
	}

	// Post event
	s.eventStream.Post(Event{
		Type:  FlightPlanDirectEvent,
		ACID:  acid,
		Route: ac.Nav.Waypoints,
	})

	s.publish()
	return nil
}

type Handoff struct {
	AutoAcceptTime    Time
	ReceivingFacility string // only for auto accept
}

///////////////////////////////////////////////////////////////////////////
// Automatic handoff processing (AHOP) configuration

// AutoHandoffOp identifies which automatic handoff processing inhibit a
// ConfigureAutoHandoff call applies to.
type AutoHandoffOp int

const (
	// AutoHandoffSite is the site-wide inhibit (STARS 8.8, p. 8-13).
	AutoHandoffSite AutoHandoffOp = iota
	// AutoHandoffTCPBoth and friends are the entering TCP's inhibits (STARS
	// 4.3, p. 4-30).
	AutoHandoffTCPBoth
	AutoHandoffTCPIntrafacility
	AutoHandoffTCPInterfacility
)

// ConfigureAutoHandoff enables or inhibits automatic handoff processing,
// either site-wide or for the entering TCW's primary TCP.
func (s *Sim) ConfigureAutoHandoff(tcw TCW, op AutoHandoffOp, enable bool) (msg string, err error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	defer func() {
		if err == nil {
			s.publish()
		}
	}()

	if op == AutoHandoffSite {
		if s.State.AutoHandoffSiteInhibited == !enable {
			return "NO CHANGE", nil
		}
		s.State.AutoHandoffSiteInhibited = !enable
		return "", nil
	}

	// 4.3: the command is rejected if the entering position has no sector or
	// if automatic handoffs are off for the whole site.
	tcp := s.State.PrimaryPositionForTCW(tcw)
	if _, ok := s.State.Controllers[tcp]; !ok {
		return "", ErrIllegalPosition
	}
	if s.State.AutoHandoffSiteInhibited {
		return "", ErrIllegalFunction
	}

	if s.State.AutoHandoffTCPInhibits == nil {
		s.State.AutoHandoffTCPInhibits = make(map[ControlPosition]AutoHandoffInhibit)
	}
	inh := s.State.AutoHandoffTCPInhibits[tcp]
	if op == AutoHandoffTCPBoth || op == AutoHandoffTCPIntrafacility {
		inh.Intrafacility = !enable
	}
	if op == AutoHandoffTCPBoth || op == AutoHandoffTCPInterfacility {
		inh.Interfacility = !enable
	}
	if inh == (AutoHandoffInhibit{}) {
		delete(s.State.AutoHandoffTCPInhibits, tcp)
	} else {
		s.State.AutoHandoffTCPInhibits[tcp] = inh
	}
	return "", nil
}

// autoHandoffDisabledByCondition reports whether a track is ineligible for
// automatic handoff processing for a reason a controller can't reverse: a
// non-discrete beacon code, being suspended, or having been disabled by a
// handoff filter row with the "D" action (STARS 5.1.7, p. 5-15).
func autoHandoffDisabledByCondition(fp *NASFlightPlan, ac *Aircraft) bool {
	return fp.AutoHandoffInhibitLocked || fp.Suspended || (ac != nil && !ac.Squawk.IsDiscrete())
}

// checkAutoHandoffToggle validates a controller's request to enable or inhibit
// automatic handoffs for a single track (STARS 5.1.7, p. 5-15 and 5.1.20,
// p. 5-38).
func (s *Sim) checkAutoHandoffToggle(fp *NASFlightPlan, ac *Aircraft) error {
	if s.State.IsExternalController(fp.TrackingController) {
		// Not valid for tracks owned by another facility.
		return av.ErrOtherControllerHasTrack
	}
	if fp.HandoffController != "" || fp.RedirectedHandoff.RedirectedTo != "" {
		return ErrTrackIsBeingHandedOff
	}
	if s.State.AutoHandoffInhibitedForTCW(fp.OwningTCW) || autoHandoffDisabledByCondition(fp, ac) {
		return ErrIllegalFunction
	}
	return nil
}

type PointOut struct {
	FromController ControlPosition
	ToController   ControlPosition
	AcceptTime     Time
}

type RedirectedHandoff struct {
	OriginalOwner ControlPosition   // Controller position
	Redirector    []ControlPosition // Redirecting controllers
	RedirectedTo  ControlPosition
}

func (rd *RedirectedHandoff) GetLastRedirector() ControlPosition {
	if length := len(rd.Redirector); length > 0 {
		return rd.Redirector[length-1]
	} else {
		return ""
	}
}

func (rd *RedirectedHandoff) ShowRDIndicator(pos ControlPosition, RDIndicatorEnd, simTime Time) bool {
	// Show "RD" to the redirect target, last redirector until the RD is accepted.
	// Show "RD" to the original owner up to 30 seconds after the RD is accepted.
	return pos != "" && (rd.RedirectedTo == pos || rd.GetLastRedirector() == pos ||
		rd.OriginalOwner == pos || RDIndicatorEnd.After(simTime))
}

func (rd *RedirectedHandoff) ShouldFallbackToHandoff(ctrl, octrl ControlPosition) bool {
	// True if the 2nd redirector redirects back to the 1st redirector
	return (len(rd.Redirector) == 1 || ((len(rd.Redirector) > 1) && rd.Redirector[1] == ctrl)) && octrl == rd.Redirector[0]
}

func (rd *RedirectedHandoff) AddRedirector(ctrl *av.Controller) {
	if len(rd.Redirector) == 0 || rd.Redirector[len(rd.Redirector)-1] != ctrl.PositionId() {
		// Don't append the same controller multiple times
		// (the case in which the last redirector recalls and then redirects again)
		rd.Redirector = append(rd.Redirector, ctrl.PositionId())
	}
}

// HandoffFilterRegion models one row of the STARS Auto-Handoff Window (DMS
// Sec. 4.7.7, p. 4-185; parameters in Table 4-30, p. 4-187). Each row is a
// handoff filter volume plus a set of "Handoff Conditions"; when a track
// enters the volume and its flight plan matches the conditions, the row's
// Handoff Action fires. Rows are processed in order, first-match-wins.
//
// The shared match criteria (flight type, owning TCP, entry/exit fix,
// scratchpads, requested-level range) are handled by the embedded
// FilterQualifiers, the same vehicle used by Quicklook and FDAM regions. The
// auto-handoff-specific columns (config plan, slave TCPs, A/C type class,
// action, receiver) are added here.
//
// Not modeled from Table 4-30: the Hdg Start / Hdg End true-heading range, and
// the Ext Sector column that accompanies an external HO Rcvr.
type HandoffFilterRegion struct {
	av.AirspaceVolume
	FilterQualifiers

	// ConfigPlan (Config Plan): the configuration plan that must be active for
	// this row to apply; blank means any plan.
	ConfigPlan string `json:"config_plan"`
	// TerminalSector (Terminal Sector): conditions the ConfigPlan match on a
	// specific terminal sector. Parsed and validated but not enforced at
	// runtime, since vice does not model per-terminal-sector plan activation.
	TerminalSector string `json:"terminal_sector"`
	// SlaveTCPs (+ Slave TCPs): when true, a TCP named in "owning_tcp" also
	// matches if it is consolidated to the flight's owning TCP.
	SlaveTCPs bool `json:"slave_tcps"`
	// OwnerTCPs takes over the "owning_tcp" list parsed by the embedded
	// FilterQualifiers, which is left empty: the match needs the SlaveTCPs
	// flag and the current consolidation, so it is made in the Sim rather than
	// by FilterQualifiers.Match.
	OwnerTCPs []ControlPosition `json:"-"`
	// ACTypeClass (A/C Type Class): the AHO aircraft type class
	// (JET/PROP/TURBO) or an exact aircraft type; blank means any.
	ACTypeClass string `json:"actype_class"`
	// HandoffAction (Handoff Action): "I" initiate, "T" transfer, or "D"
	// disable auto-handoff for the track. Mandatory in the real system; we
	// default it to "I". ("A", accept, is not supported.)
	HandoffAction string `json:"handoff_action"`
	// HORcvr (HO Rcvr): the local TCP that receives the handoff or transfer
	// for "I"/"T". Must be blank for "D". External facility receivers are not
	// supported.
	HORcvr ControlPosition `json:"ho_receiver"`
}

type HandoffFilterRegions []HandoffFilterRegion

func (r *HandoffFilterRegion) ValidateTCPs(controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	r.FilterQualifiers.PostDeserialize(controlPositions, e)
	if r.TCPsString != "" {
		e.ErrorString(`"tcps" is not supported for handoff filter regions; use "owning_tcp"`)
	}
	r.TCPs = nil

	// FilterQualifiers validated and parsed "owning_tcp"; take the result over
	// so that the Sim can widen it with "+ Slave TCPs".
	r.OwnerTCPs, r.FilterQualifiers.OwningTCPs = r.FilterQualifiers.OwningTCPs, nil

	// "+ Slave TCPs" qualifies adapted Owning TCPs, so it requires one.
	if r.SlaveTCPs && len(r.OwnerTCPs) == 0 {
		e.ErrorString(`"owning_tcp" must be set when "slave_tcps" is true`)
	}

	// ho_receiver must reference a local position.
	r.HORcvr = ControlPosition(strings.ToUpper(strings.TrimSpace(string(r.HORcvr))))
	validateLocalHORcvr := func() {
		ctrl, ok := controlPositions[TCP(r.HORcvr)]
		if !ok {
			e.ErrorString(`unknown TCP %q in "ho_receiver"`, r.HORcvr)
		} else if ctrl.FacilityIdentifier != "" {
			e.ErrorString(`TCP %q in "ho_receiver" is not a local position`, r.HORcvr)
		}
	}

	r.HandoffAction = strings.ToUpper(strings.TrimSpace(r.HandoffAction))
	if r.HandoffAction == "" {
		r.HandoffAction = "I"
	}
	switch r.HandoffAction {
	case "I", "T":
		if r.HORcvr == "" {
			e.ErrorString(`"ho_receiver" must be specified for handoff_action %q`, r.HandoffAction)
		} else {
			validateLocalHORcvr()
		}
	case "D":
		if r.HORcvr != "" {
			e.ErrorString(`"ho_receiver" must be blank for handoff_action "D"`)
		}
	case "A":
		e.ErrorString(`handoff_action "A" (accept) is not supported`)
	default:
		e.ErrorString(`invalid "handoff_action" %q: must be "I", "T", or "D"`, r.HandoffAction)
	}

	// Terminal Sector must be blank if Config Plan is blank (Table 4-30).
	r.ConfigPlan = strings.ToUpper(strings.TrimSpace(r.ConfigPlan))
	r.TerminalSector = strings.ToUpper(strings.TrimSpace(r.TerminalSector))
	if r.TerminalSector != "" {
		if len(r.TerminalSector) != 1 || r.TerminalSector[0] < 'A' || r.TerminalSector[0] > 'Z' || r.TerminalSector == "T" {
			e.ErrorString(`invalid "terminal_sector" %q: must be a single letter A-Z (not T)`, r.TerminalSector)
		}
		if r.ConfigPlan == "" {
			e.ErrorString(`"terminal_sector" must be blank when "config_plan" is blank`)
		}
	}

	r.ACTypeClass = strings.ToUpper(strings.TrimSpace(r.ACTypeClass))
}

func (r *HandoffFilterRegion) PostDeserialize(loc av.Locator, e *util.ErrorLogger) {
	r.AirspaceVolume.PostDeserialize(loc, e)
}

// matchACTypeClass matches an AHO aircraft type class (Table 4-30 "A/C Type
// Class"): the JET/PROP/TURBO classes, or an exact aircraft type.
func matchACTypeClass(field, engineClass, acType string) bool {
	switch strings.ToUpper(strings.TrimSpace(field)) {
	case "", "*":
		return true
	case "JET":
		return engineClass == "jet"
	case "PROP":
		return engineClass == "prop"
	case "TURBO", "TURBOPROP":
		return engineClass == "turboprop"
	default:
		return strings.EqualFold(field, acType)
	}
}

// engineClass returns the adaptation engine vocabulary for an aircraft type.
func engineClass(acType string) string {
	switch av.DB.AircraftPerformance[acType].Engine.AircraftType {
	case "J":
		return "jet"
	case "T":
		return "turboprop"
	default:
		return "prop"
	}
}

// qualifies reports whether a track at the given position and altitude
// satisfies this row's owner-independent conditions (volume, config plan, A/C
// type class, and the shared FilterQualifiers). Owner and slave matching is
// handled separately in the Sim since it needs the current consolidation.
func (r *HandoffFilterRegion) qualifies(p math.Point2LL, alt int, fp *NASFlightPlan,
	acType, engine, activeConfigPlan string, significantPoints map[string]SignificantPoint) bool {
	if !r.AirspaceVolume.Inside(p, alt) {
		return false
	}
	if r.ConfigPlan != "" && !strings.EqualFold(r.ConfigPlan, activeConfigPlan) {
		return false
	}
	if r.ACTypeClass != "" && !matchACTypeClass(r.ACTypeClass, engine, acType) {
		return false
	}
	// FilterQualifiers covers flight type, entry/exit fix, scratchpads,
	// requested-level range, flight rules, and SSR codes. Its "tcps" is
	// rejected for handoff rows and its "owning_tcp" moved to OwnerTCPs, so
	// neither is checked here.
	return r.FilterQualifiers.Match(fp, nil, acType, significantPoints)
}

// sim/handoff.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
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
	return nil
}

func (s *Sim) HandoffTrack(tcw TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(tcw, acid,
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
		})
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

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			// Check if the caller's TCW controls the HandoffTrackController TCP (consolidation-aware)
			if s.State.TCWControlsPosition(tcw, fp.HandoffController) {
				return nil
			}
			if po, ok := s.PointOuts[fp.ACID]; ok && s.State.TCWControlsPosition(tcw, po.ToController) {
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
			if po, ok := s.PointOuts[fp.ACID]; ok && s.State.TCWControlsPosition(tcw, po.ToController) {
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
			fp.TrackingController = newTrackingController
			fp.LastLocalController = newTrackingController
			fp.OwningTCW = tcw // The accepting TCW owns the track

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, acid)

			// Record handoff-accepted flash on the outbound (from) side
			// so relief controllers at that TCW see the same state.
			s.noteHandoffAccepted(previousTrackingController, ac)

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
		})
	return err
}

// noteHandoffAccepted writes the outbound handoff-accepted annotation
// on the TCW that just handed the track away so all relief controllers
// sharing that TCW see the accept-flash state. No-op if the from-side
// is a virtual/external controller (nothing to display).
// Caller must hold s.mu.
func (s *Sim) noteHandoffAccepted(fromPos ControlPosition, ac *Aircraft) {
	if ac == nil || fromPos == "" {
		return
	}
	if s.isVirtualController(fromPos) {
		return
	}
	tcw := s.tcwForPosition(fromPos)
	if tcw == "" {
		return
	}
	dur := time.Duration(s.State.FacilityAdaptation.Datablocks.FDB.AcceptFlashDuration) * time.Second
	if dur == 0 {
		dur = 5 * time.Second
	}
	end := s.State.SimTime.Add(dur)
	s.mutateTrackAnnotationLocked(tcw, ac.ADSBCallsign, func(a *TrackAnnotations) {
		a.OutboundHandoffAccepted = true
		a.OutboundHandoffFlashEnd = end
	})
}

func (s *Sim) CancelHandoff(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(tcw, acid, nil,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			delete(s.Handoffs, acid)
			fp.HandoffController = ""
			fp.RedirectedHandoff = RedirectedHandoff{}
		})
}

func (s *Sim) RedirectHandoff(tcw TCW, acid ACID, controller TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
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

			return nil
		})
	return err
}

func (s *Sim) AcceptRedirectedHandoff(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			rh := &fp.RedirectedHandoff
			if s.State.TCWControlsPosition(tcw, rh.RedirectedTo) { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: rh.OriginalOwner,
					ToController:   rh.RedirectedTo,
					ACID:           acid,
				})
				fp.HandoffController = ""
				fp.TrackingController = rh.RedirectedTo
				fp.LastLocalController = rh.RedirectedTo
				fp.OwningTCW = tcw
				*rh = RedirectedHandoff{}
			} else if s.State.TCWControlsPosition(tcw, rh.GetLastRedirector()) { // Recall (only the last redirector is able to recall)
				if len(rh.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					rh.RedirectedTo = rh.Redirector[len(rh.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					fp.HandoffController = rh.Redirector[len(rh.Redirector)-1]
					*rh = RedirectedHandoff{}
				}
			}

			return nil
		})
	return err
}

func (s *Sim) ForceQL(tcw TCW, acid ACID, controller TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
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
		})
	return err
}

func (s *Sim) PointOut(fromTCW TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(fromTCW, acid,
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
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			fromTCP := s.State.PrimaryPositionForTCW(fromTCW)
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(acid, ctrl, octrl)
		})
}

func (s *Sim) pointOut(acid ACID, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: TCP(from.PositionId()),
		ToController:   TCP(to.PositionId()),
		ACID:           acid,
	})

	s.PointOuts[acid] = PointOut{
		FromController: TCP(from.PositionId()),
		ToController:   TCP(to.PositionId()),
		AcceptTime:     s.State.SimTime.Add(s.Rand.DurationRange(4*time.Second, 14*time.Second)),
	}
}

func (s *Sim) AcknowledgePointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || !s.State.TCWControlsPosition(tcw, po.ToController) {
				return av.ErrNotPointedOutToMe
			}

			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			po := s.PointOuts[acid]
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				ACID:           acid,
			})
			fp.AddPointOutHistory(po.ToController)

			// Record the ack on the accepting TCW so relief controllers
			// at that position see the pointout as acknowledged.
			if ac != nil {
				s.mutateTrackAnnotationLocked(tcw, ac.ADSBCallsign, func(a *TrackAnnotations) {
					a.PointOutAcknowledged = true
				})
			}

			delete(s.PointOuts, acid)
			return nil
		})
	return err
}

func (s *Sim) RecallPointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || !s.State.TCWControlsPosition(tcw, po.FromController) {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) {
			po := s.PointOuts[acid]
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: po.FromController,
				ToController:   po.ToController,
				ACID:           acid,
			})

			delete(s.PointOuts, acid)
		})
}

func (s *Sim) RejectPointOut(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || !s.State.TCWControlsPosition(tcw, po.ToController) {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			po := s.PointOuts[acid]
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				ACID:           acid,
			})

			delete(s.PointOuts, acid)

			return nil
		})
	return err
}

// TODO: Migrate to ERAM computer.
func (s *Sim) SendRouteCoordinates(tcw TCW, acid ACID, minutes int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

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

	return nil
}

type Handoff struct {
	AutoAcceptTime    Time
	ReceivingFacility string // only for auto accept
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

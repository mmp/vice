// sim/update.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// prepareRadioTransmissions adds callsign/controller prefixes to radio transmissions.
// (Multi-command batching is now handled at intent generation time in RunAircraftControlCommands.)
// This is called for both main event subscriptions and TTS event subscriptions.
// Must be called with s.mu held.
func (s *Sim) prepareRadioTransmissions(tcw TCW, events []Event) []Event {
	primaryTCP := s.State.PrimaryPositionForTCW(tcw)
	ctrl := s.State.Controllers[primaryTCP]

	// Add identifying info to radio transmissions destined for this TCW
	for i, e := range events {
		if e.Type != RadioTransmissionEvent || e.DestinationTCW != tcw {
			continue
		}

		ac, ok := s.Aircraft[e.ADSBCallsign]
		if !ok {
			continue
		}

		var heavySuper string
		if perf, ok := db.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok && !ctrl.ERAMFacility {
			if perf.WeightClass == "H" {
				heavySuper = " heavy"
			} else if perf.WeightClass == "J" {
				heavySuper = " super"
			}
		}

		switch e.RadioTransmissionType {
		case speech.RadioTransmissionContact:
			// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super.
			// Only on initial contact, not subsequent transmissions.
			if ac.EmergencyState != nil && s.textRand.Bool() {
				heavySuper += " emergency aircraft"
			}
			csArg := speech.CallsignArg{
				Callsign:           ac.ADSBCallsign,
				IsEmergency:        ac.EmergencyState != nil,
				AlwaysFullCallsign: true,
			}
			var tr *speech.RadioTransmission
			if ac.TypeOfFlight == av.FlightTypeDeparture {
				tr = speech.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper, ctrl, csArg)
			} else {
				tr = speech.MakeContactTransmission("{actrl}, {callsign}"+heavySuper, ctrl, csArg)
			}
			w, werr := tr.Written(s.textRand)
			sp, serr := tr.Spoken(s.textRand)
			if err := cmp.Or(werr, serr); err != nil {
				// This runs once per destination TCW as events are
				// delivered, so posting here would repeat the message;
				// the transmission itself still goes out, just without
				// the controller and callsign in front of it.
				s.lg.Errorf("%s: %v", ac.ADSBCallsign, err)
			} else {
				events[i].WrittenText = w + ", " + e.WrittenText
				events[i].SpokenText = strings.TrimSuffix(sp, ".") + ", " + e.SpokenText
			}
		case speech.RadioTransmissionMixUp:
			// No additional formatting for mix-up transmissions; the callsign is already in there.
		case speech.RadioTransmissionNoId:
			// No callsign formatting for NoId transmissions (e.g., "blocked").
		default:
			csArg := speech.CallsignArg{
				Callsign:    ac.ADSBCallsign,
				IsEmergency: ac.EmergencyState != nil,
			}
			tr := speech.MakeReadbackTransmission("{callsign}"+heavySuper, csArg)
			w, werr := tr.Written(s.textRand)
			sp, serr := tr.Spoken(s.textRand)
			if err := cmp.Or(werr, serr); err != nil {
				s.lg.Errorf("%s: %v", ac.ADSBCallsign, err)
			} else {
				events[i].WrittenText = e.WrittenText + ", " + w
				events[i].SpokenText = strings.TrimSuffix(e.SpokenText, ".") + ", " + sp
			}
		}
	}

	return events
}

// PrepareRadioTransmissionsForTCW processes events for TTS, adding
// callsign/controller prefixes to radio transmissions. This is the public API
// for the server to process TTS events from a separate subscription.
func (s *Sim) PrepareRadioTransmissionsForTCW(tcw TCW, events []Event) []Event {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.prepareRadioTransmissions(tcw, events)
}

func (s *Sim) GetStateUpdate(tcw TCW) StateUpdate {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.snapshot(tcw)
}

// WaitForStateUpdate waits until the publication generation advances past sinceGen or the timeout /
// sim teardown fires, then returns the current snapshot together with the generation it was taken at.
//
// In healthy operation Sim.Update emits a heartbeat publish every ~1.1s, so the timeout arm never
// fires. If it does fire, the sim's publish loop has hung (e.g. it panicked and the goroutine
// exited); return ErrSimPublishStalled so the caller can surface a real failure rather than
// silently delivering a stale snapshot to clients.
func (s *Sim) WaitForStateUpdate(tcw TCW, sinceGen uint64, timeout time.Duration) (StateUpdate, uint64, error) {
	s.mu.Lock(s.lg)
	gen, ch := s.snapshotPub()
	if gen > sinceGen {
		update := s.snapshot(tcw)
		s.mu.Unlock(s.lg)
		return update, gen, nil
	}
	s.mu.Unlock(s.lg)

	select {
	case <-ch:
	case <-time.After(timeout):
		return StateUpdate{}, sinceGen, ErrSimPublishStalled
	case <-s.simDoneCh:
	}

	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.snapshot(tcw), s.pubGen, nil
}

// snapshot builds and deep-copies a StateUpdate for the given TCW.
func (s *Sim) snapshot(tcw TCW) StateUpdate {
	s.State.GenerationIndex = int(s.pubGen)

	update := StateUpdate{
		DynamicState:     s.State.DynamicState,
		DerivedState:     makeDerivedState(s),
		FlightStripACIDs: s.flightStripACIDsForTCW(tcw),
	}

	// While it seemed that this could be skipped, this is actually necessary
	// to avoid races: although another copy is made as it's marshaled to be
	// returned from RPC call, there may be other updates to the sim state
	// between this function returning and that happening.
	return deep.MustCopy(update)
}

// isVirtualController returns true if the given position is virtual
// (i.e., simulated by the system rather than allocated to humans).
// Virtual controllers auto-accept handoffs and pointouts.
// Human-allocatable positions (from ControllerConfig) do NOT auto-accept,
// regardless of whether a human is currently signed in.
// humanControlled reports whether a human controller is working the
// aircraft.
func (s *Sim) humanControlled(ac *Aircraft) bool {
	return s.ScenarioDefaultConsolidation.IsHumanPosition(ac.ControllerFrequency)
}

func (s *Sim) isVirtualController(pos ControlPosition) bool {
	// A controller is virtual if it's a valid control position but NOT
	// a human-allocatable position (i.e., not in the consolidation hierarchy).
	if _, ok := s.ControlPositions[TCP(pos)]; !ok {
		return false
	}
	return !s.ScenarioDefaultConsolidation.IsHumanPosition(pos)
}

func (s *Sim) isTRACONController(pos ControlPosition) bool {
	ctrl, ok := s.State.Controllers[pos]
	return ok && !ctrl.ERAMFacility
}

// areaForTCP returns the TRACON area a control position belongs to, or "" if
// it has none.
func (s *Sim) areaForTCP(tcp TCP) string {
	if ctrl, ok := s.ControlPositions[tcp]; ok {
		return ctrl.Area
	}
	return ""
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if !util.DebuggerIsRunning() {
		startUpdate := time.Now()
		defer func() {
			if d := time.Since(startUpdate); d > 200*time.Millisecond {
				s.lg.Warn("unexpectedly long Sim Update() call", slog.Duration("duration", d),
					slog.Any("sim", s))
			}
		}()

		if time.Since(s.lastControlCommandTime) > 15*time.Minute && !s.State.Paused {
			s.eventStream.Post(Event{
				Type:        StatusMessageEvent,
				WrittenText: "Pausing sim due to inactivity.",
			})

			s.State.Paused = true
			s.publish()
		}
	}

	if !s.State.Paused && !s.pausedByServer {
		// Figure out how much time has passed since the last update:
		// wallclock time is scaled by the sim rate, then we add in any
		// time from the last update that wasn't accounted for.
		elapsed := time.Since(s.lastSimUpdateTime)
		elapsed = time.Duration(s.State.SimRate * float32(elapsed))
		s.lastSimUpdateTime = time.Now()
		if s.step(elapsed) {
			// Don't bother with these Check()s if we didn't change any aircraft state
			for _, ac := range s.Aircraft {
				ac.Check(s.lg)
			}
			s.publish()
		}
	} else if time.Since(s.lastPublishTime) >= 1100*time.Millisecond {
		// If the sim is paused, keep on publishing updates so that the client doesn't think the
		// server has crashed.
		s.publish()
	}
}

func (s *Sim) applyWaypointActionEvent(ac *Aircraft, event av.WaypointActionEvent) bool {
	actions := event.Actions

	// The flight plan the waypoint's actions operate on. A departure reaches
	// its transfer of comms point a few seconds before its track tags up, and
	// handoffs still happen for tracks an external facility is working, so
	// resolve it whether or not it has associated with the aircraft yet.
	sfp := ac.NASFlightPlan
	if sfp == nil {
		sfp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
	}

	if actions.GoAroundContactController != "" {
		tcp := actions.GoAroundContactController
		s.setControllerFrequency(ac, ControlPosition(tcp))

		// Clear stale pending contacts and frequency changes from before
		// the go-around so the go-around transmission takes priority.
		s.cancelFutureFrequencyChange(ac.ADSBCallsign)
		for t := range s.PendingContacts {
			s.PendingContacts[t] = slices.DeleteFunc(s.PendingContacts[t], func(pc PendingContact) bool {
				return pc.ADSBCallsign == ac.ADSBCallsign &&
					(pc.Type == PendingTransmissionDeparture || pc.Type == PendingTransmissionArrival)
			})
		}

		s.enqueuePilotTransmission(ac.ADSBCallsign, TCP(tcp), PendingTransmissionGoAround)

		// Reassociate flight plan if controller dropped it
		if sfp != nil && ac.IsUnassociated() {
			s.STARSComputer.takeFlightPlanByACID(sfp.ACID)
			sfp.DeleteTime = Time{}
			sfp.OwningTCW = s.tcwForPosition(sfp.TrackingController)
			ac.AssociateFlightPlan(sfp)
			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: sfp.ACID,
			})
		}
		// Set up handoff from current tracker to go-around controller
		if sfp != nil && sfp.TrackingController != "" && sfp.TrackingController != TCP(tcp) {
			s.handoffTrack(sfp, TCP(tcp))
		}
	}

	// The go-around contact above applies to any aircraft; the rest are
	// instructions a virtual controller issues, and an aircraft on a human's
	// frequency is theirs to instruct.
	if !s.humanControlled(ac) {
		if s.applyVirtualControllerActions(ac, sfp, event.Waypoint.Fix, actions) {
			return true
		}
	}

	// Removing the aircraft from the sim is no controller instruction, so it
	// applies however the aircraft is being worked--and last, so that a fix
	// with both /ho and /delete still hands off before the aircraft goes.
	if actions.Delete {
		s.deleteAtWaypoint(ac, event.Waypoint)
		return true
	}
	if actions.Land {
		return s.landAtWaypoint(ac, event.Waypoint)
	}
	return false
}

// deleteAtWaypoint carries out a /delete at wp. An aircraft in the pattern
// with touch-and-gos left goes around again instead.
func (s *Sim) deleteAtWaypoint(ac *Aircraft, wp av.Waypoint) {
	if ac.TouchAndGosRemaining > 0 {
		ac.TouchAndGosRemaining--

		runway := s.bestRunwayForWind(ac.FlightPlan.ArrivalAirport)
		s.recordPatternTouchAndGo(ac, ac.FlightPlan.ArrivalAirport, runway)
		s.resetPatternLap(ac)
		s.lg.Debug("pattern touch-and-go", slog.String("callsign", string(ac.ADSBCallsign)),
			slog.Int("remaining", ac.TouchAndGosRemaining))
		return
	}

	reason := DeleteAtWaypoint
	if wp.VFRPhase != av.VFRPhaseNone {
		s.recordArrivalLanding(ac, s.bestRunwayForWind(ac.FlightPlan.ArrivalAirport))
		reason = DeleteLanded
	}
	s.lg.Debug("deleting aircraft at waypoint", slog.Any("waypoint", wp))
	s.deleteAircraft(ac, reason)
}

// landAtWaypoint carries out a /land at wp, going around if the aircraft is
// more than 200' above the fix's altitude restriction. It returns true if the
// aircraft landed and was deleted.
func (s *Sim) landAtWaypoint(ac *Aircraft, wp av.Waypoint) bool {
	// There should be an altitude restriction at the final approach waypoint, but
	// be careful.
	alt := wp.AltitudeRestriction()
	if alt != nil && ac.Altitude() > alt.TargetAltitude(ac.Altitude())+200 {
		s.goAround(ac)
		return false
	}

	var runway string
	if ac.Nav.Approach.Assigned != nil {
		runway = ac.Nav.Approach.Assigned.Runway
	} else {
		runway = s.bestRunwayForWind(ac.FlightPlan.ArrivalAirport)
	}
	s.lg.Debug("landing at waypoint", slog.Any("waypoint", wp))
	s.recordArrivalLanding(ac, runway)
	s.deleteAircraft(ac, DeleteLanded)
	return true
}

// recordArrivalLanding notes the landing for the sake of scheduling
// departures off the runway.
func (s *Sim) recordArrivalLanding(ac *Aircraft, runway string) {
	depState, ok := s.DepartureState[ac.FlightPlan.ArrivalAirport]
	if !ok {
		return
	}
	for rwyID, rwyState := range depState {
		if rwyID.Base() == runway {
			rwyState.LastArrivalLandingTime = s.State.SimTime
			rwyState.LastArrivalFlightRules = ac.FlightPlan.Rules
		}
	}
}

// applyVirtualControllerActions carries out the route actions a virtual
// controller working the aircraft issues at fix, which the aircraft has just
// crossed. It returns true if the aircraft was deleted.
func (s *Sim) applyVirtualControllerActions(ac *Aircraft, sfp *NASFlightPlan, fix string, actions av.WaypointActions) bool {
	if actions.HumanHandoff {
		// Handoff from virtual controller to a human controller.
		// During prespawn uncontrolled-only phase, cull aircraft that would be handed off to humans
		// rather than initiating the handoff.
		if s.prespawnUncontrolledOnly {
			s.deleteAircraft(ac, DeletePrespawn)
			return true
		}
		if sfp == nil {
			s.lg.Errorf("%s: no flight plan for the /ho at %s", ac.ADSBCallsign, fix)
		} else {
			s.handoffTrack(sfp, sfp.InboundHandoffController)
		}
	} else if actions.HandoffController != "" {
		// During prespawn uncontrolled-only phase, cull if handoff target is a human controller
		if s.prespawnUncontrolledOnly && !s.isVirtualController(TCP(actions.HandoffController)) {
			s.deleteAircraft(ac, DeletePrespawn)
			return true
		}
		// Only initiate the handoff if a virtual controller has the track; if
		// a human owns it, it's their call when to hand it off.
		if sfp == nil {
			s.lg.Errorf("%s: no flight plan for the /ho%s at %s", ac.ADSBCallsign,
				actions.HandoffController, fix)
		} else if s.isVirtualController(sfp.TrackingController) {
			s.handoffTrack(sfp, TCP(actions.HandoffController))
		}
	}

	if actions.ClimbAltitude != 0 {
		ac.Nav.AssignAltitudeNow(float32(actions.ClimbAltitude), false)
		s.recordVirtualAltitudeEntry(sfp, actions.ClimbAltitude, true)
	} else if actions.DescendAltitude != 0 {
		ac.Nav.AssignAltitudeNow(float32(actions.DescendAltitude), false)
		s.recordVirtualAltitudeEntry(sfp, actions.DescendAltitude, false)
	}
	var exceptAlt *float32
	if actions.ExceptAltitude != 0 {
		alt := float32(actions.ExceptAltitude)
		exceptAlt = &alt
	}
	if actions.ClimbViaSID && ac.Nav.ClimbViaSIDAtPassedFix(exceptAlt) {
		// Without an exception, the aircraft climbs to its filed altitude.
		alt := util.Select(actions.ExceptAltitude != 0, actions.ExceptAltitude, ac.FlightPlan.Altitude)
		s.recordVirtualAltitudeEntry(sfp, alt, true)
	}
	if actions.DescendViaSTAR && ac.Nav.DescendViaSTARAtPassedFix(exceptAlt) {
		if actions.ExceptAltitude != 0 {
			s.recordVirtualAltitudeEntry(sfp, actions.ExceptAltitude, false)
		} else if alt, ok := ac.Nav.LowestProcedureAltitude(); ok {
			// Without an exception, the aircraft descends to the bottom of
			// the procedure ahead.
			s.recordVirtualAltitudeEntry(sfp, int(alt), false)
		}
	}

	if actions.ClearApproach {
		ac.ClearedApproachAtPassedFix(fix, s.State.SimTime)
		if !ac.Nav.Approach.Cleared {
			s.lg.Warnf("%s: /clearapp at %s did not clear the aircraft for the %s approach",
				ac.ADSBCallsign, fix, ac.Nav.Approach.AssignedId)
		}
	}

	if actions.InterceptApproach {
		ac.InterceptApproachAtPassedFix(fix)
	}

	if actions.TransferComms {
		if sfp == nil {
			s.lg.Errorf("%s: no flight plan at the transfer of comms point", ac.ADSBCallsign)
		} else if ac.IsDeparture() && ac.DepartureContactAltitude == 0 {
			// This is a departure that hasn't contacted the departure controller yet, do it here
			s.contactDeparture(ac, sfp)
		} else {
			// We didn't enqueue this before since we knew an
			// explicit comms handoff was coming so go ahead and
			// send them to the controller's frequency. Note that
			// we use InboundHandoffController and not
			// ac.TrackingController, since the human controller
			// may have already flashed the track to a virtual
			// controller.
			ctrl := s.State.ResolveController(sfp.InboundHandoffController)
			// Make sure they've bought the handoff.
			if ctrl != sfp.HandoffController {
				s.enqueueControllerContact(ac, TCP(ctrl), ac.ControllerFrequency)
			}
		}
	}

	if sfp != nil {
		if actions.PrimaryScratchpad != "" {
			sfp.Scratchpad = actions.PrimaryScratchpad
		}
		if actions.ClearPrimaryScratchpad {
			sfp.Scratchpad = ""
		}
		if actions.SecondaryScratchpad != "" {
			sfp.SecondaryScratchpad = actions.SecondaryScratchpad
		}
		if actions.ClearSecondaryScratchpad {
			sfp.SecondaryScratchpad = ""
		}
	}

	// A point out needs a controller to make it, so it is skipped for a track
	// that hasn't associated or a pilot who isn't on a frequency; nothing
	// retries it.
	if ac.IsAssociated() && actions.PointOut != "" {
		// During prespawn uncontrolled-only phase, cull if point-out target is a human controller
		// rather than initiating the point out.
		if s.prespawnUncontrolledOnly && !s.isVirtualController(actions.PointOut) {
			s.deleteAircraft(ac, DeletePrespawn)
			return true
		}

		if ctrl, ok := s.State.Controllers[TCP(actions.PointOut)]; ok {
			// Only do automatic point outs for virtual controllers
			if s.isVirtualController(ac.ControllerFrequency) {
				fromCtrl := s.State.Controllers[TCP(ac.ControllerFrequency)]
				s.pointOut(sfp.ACID, fromCtrl, ctrl)
			}
		}
	}

	return false
}

// recordVirtualAltitudeEntry updates the flight plan's ERAM altitude fields
// to reflect an altitude a virtual controller has just assigned, as if they
// had made the corresponding keyboard entry: a climb that stops short of the
// hard altitude is an interim altitude and anything else amends the hard
// altitude. STARS leaves both to the controller, so this is only done at
// ERAM facilities.
func (s *Sim) recordVirtualAltitudeEntry(sfp *NASFlightPlan, alt int, climb bool) {
	if sfp == nil || !db.DB.IsARTCC(s.State.Facility) {
		return
	}

	if climb && sfp.AssignedAltitude != 0 && alt < sfp.AssignedAltitude {
		sfp.InterimAlt, sfp.InterimType = alt, InterimNormal
		return
	}

	sfp.AssignedAltitude = alt
	sfp.AltitudeBlock = [2]int{}
	sfp.InterimAlt, sfp.InterimType = 0, InterimNormal
}

// Step advances the simulation by the given elapsed time duration.
// It acquires the sim mutex for the duration of the step.
func (s *Sim) Step(elapsed time.Duration) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.step(elapsed)
}

// step is the inner implementation of Step; the caller must hold s.mu.
func (s *Sim) step(elapsed time.Duration) bool {
	elapsed += s.updateTimeSlop

	// Run the sim for this many seconds
	ns := int(elapsed.Truncate(time.Second).Seconds())
	if ns > 10 {
		s.lg.Warn("unexpected hitch in update rate", slog.Duration("elapsed", elapsed),
			slog.Int("steps", ns), slog.Duration("slop", s.updateTimeSlop))
	}

	// Cap steps to prevent runaway when sim rate is high and updates are slow.
	const maxSteps = 10
	ns = min(ns, maxSteps)

	for range ns {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}

	// If we were capped, discard excess time to prevent accumulation.
	s.updateTimeSlop = elapsed - time.Duration(ns)*time.Second
	if s.updateTimeSlop > time.Second {
		s.updateTimeSlop = 0
	}

	if ns > 0 {
		// Don't bother with this if we didn't change any aircraft state
		s.CheckLeaks()
	}

	return ns > 0
}

// cullDistance is how far from the facility's center an aircraft flies before
// the sim lets it go; there is no point in carrying route waypoints past it.
func (ss *CommonState) cullDistance() float32 {
	if ss.FacilityAdaptation.MaxDistance > 0 {
		return ss.FacilityAdaptation.MaxDistance
	}
	return util.Select(db.DB.IsARTCC(ss.Facility), float32(400), float32(200))
}

// Distance from the runway threshold at which a pilot who still hasn't been
// sent to tower asks about switching. Jets cover the last few miles faster, so
// they ask further out.
const (
	jetTowerSwitchDistance   = 5
	otherTowerSwitchDistance = 3
)

// shouldAskAboutTowerSwitch reports whether an aircraft is far enough along the
// approach that not having been switched to tower is worth a radio call.  The
// distance check matters for visual approaches, where the FAF marker is a
// synthetic glideslope anchor that may sit many miles from the runway.
func shouldAskAboutTowerSwitch(ac *Aircraft) bool {
	appr := ac.Nav.Approach.Assigned
	if appr == nil || !ac.Nav.Approach.Cleared || !ac.Nav.Approach.PassedFAF {
		return false
	}

	d := float32(otherTowerSwitchDistance)
	if ac.Nav.Perf.Engine.AircraftType == "J" {
		d = jetTowerSwitchDistance
	}
	return math.NMDistance2LL(ac.Position(), appr.Threshold) <= d
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.State.SimTime

	// Weather fetched in the background takes effect only here, at the
	// start of a tick, so that it doesn't depend on when the fetch finished.
	s.wxModel.Advance(now.Time())

	for acid, ho := range util.SortedMap(s.Handoffs) {
		if !now.After(ho.AutoAcceptTime) && !s.prespawn {
			continue
		}
		delete(s.Handoffs, acid)

		fp, ac, _ := s.getFlightPlanForACID(acid)
		if fp == nil {
			continue
		}
		if ac == nil {
			// A departure's track tags up a few seconds after it leaves the
			// surface tracking filter, which the auto-accept can beat; until
			// then the flight plan is reachable only by ACID, which is the
			// aircraft's callsign.
			ac = s.Aircraft[av.ADSBCallsign(acid)]
		}

		if rh := fp.RedirectedHandoff; rh.RedirectedTo != "" && s.isVirtualController(rh.RedirectedTo) {
			// Automated accept of a redirected handoff
			s.lg.Debug("automatic redirected handoff accept", slog.String("acid", string(fp.ACID)),
				slog.String("from", string(rh.OriginalOwner)),
				slog.String("to", string(rh.RedirectedTo)))
			s.acceptRedirectedHandoff(fp, ac, s.tcwForPosition(rh.RedirectedTo))
		} else if fp.HandoffController != "" && s.isVirtualController(fp.HandoffController) {
			// Automated accept
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: fp.TrackingController,
				ToController:   fp.HandoffController,
				ACID:           fp.ACID,
			})
			s.lg.Debug("automatic handoff accept", slog.String("acid", string(fp.ACID)),
				slog.String("from", string(fp.TrackingController)),
				slog.String("to", string(fp.HandoffController)))

			previousTrackingController := fp.TrackingController
			newTrackingController := fp.HandoffController

			fp.TrackingController = newTrackingController
			if s.State.IsLocalController(fp.TrackingController) {
				fp.LastLocalController = fp.TrackingController
			}
			fp.OwningTCW = s.tcwForPosition(fp.TrackingController)
			fp.HandoffController = ""
			fp.HandoffWasAutomatic = false

			if ac != nil {
				haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
					func(wp av.Waypoint) bool { return wp.HasTransferCommsAction() })
				if !haveTransferComms && s.isVirtualController(previousTrackingController) {
					s.virtualControllerTransferComms(ac, TCP(previousTrackingController), TCP(newTrackingController))
				}
			}
		}
	}

	for acid, pos := range util.SortedMap(s.PointOuts) {
		fp, _, _ := s.getFlightPlanForACID(acid)
		s.PointOuts[acid] = util.FilterSlice(pos, func(po PointOut) bool {
			if now.After(po.AcceptTime) && fp != nil && s.isVirtualController(po.ToController) {
				// Note that "to" and "from" are swapped in the event, since the ack is coming from
				// the "to" controller of the original point out.
				s.eventStream.Post(Event{
					Type:           AcknowledgedPointOutEvent,
					FromController: po.ToController,
					ToController:   po.FromController,
					ACID:           acid,
				})
				s.lg.Debug("automatic pointout accept", slog.String("acid", string(acid)),
					slog.String("by", string(po.ToController)), slog.String("to", string(po.FromController)))

				fp.AddPointOutHistory(po.ToController)
				return false // cull it
			}
			return true // keep
		})
		if fp == nil || len(s.PointOuts[acid]) == 0 {
			delete(s.PointOuts, acid)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now

		for _, callsign := range util.SortedMapKeys(s.Aircraft) {
			ac, ok := s.Aircraft[callsign]
			if !ok {
				// Gone already: a scripted control command run for an
				// aircraft earlier in the order can delete another one.
				continue
			}
			if ac.HoldForRelease && !ac.Released {
				// nvm...
				continue
			}
			if ac.WaitingForLaunch {
				continue
			}

			arrivalMETAR := s.State.METAR[ac.FlightPlan.ArrivalAirport]
			updateResult := ac.Update(s.wxModel, s.State.SimTime, &arrivalMETAR, s.bravoAirspace, nil /* s.lg*/)
			passedWaypoint := updateResult.PassedWaypoint
			ac.refreshSeenTraffic(now, s.Aircraft)

			if ac.Nav.Approach.RequestApproachClearance && ac.IsAssociated() {
				ac.Nav.Approach.RequestApproachClearance = false
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestApproachClearance)
			}

			if ac.Nav.Approach.GoAroundNoApproachClearance && ac.IsAssociated() {
				ac.Nav.Approach.GoAroundNoApproachClearance = false
				s.goAround(ac)
			}

			if ac.Nav.Approach.RequestVectors && ac.IsAssociated() {
				ac.Nav.Approach.RequestVectors = false
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestVectors)
			}

			if ac.Nav.Approach.RequestAltitude && ac.IsAssociated() {
				ac.Nav.Approach.RequestAltitude = false
				if ac.Nav.Altitude.Assigned == nil && ac.Nav.Altitude.AfterSpeed == nil {
					// An altitude may have been subsequently assigned (e.g., fly heading 120,
					// maintain 5000); skip the transmission if so. AfterSpeed counts too —
					// the altitude is assigned, just deferred until the speed change completes.
					s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestAltitude)
				}
			}

			if ac.IsAssociated() && !ac.GotContactTower && !ac.AskedAboutTowerSwitch &&
				shouldAskAboutTowerSwitch(ac) {
				// Close to the runway and still not sent to tower: ask about switching.
				ac.AskedAboutTowerSwitch = true
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestTowerSwitch)
			}

			if ac.FirstSeen.IsZero() && s.isRadarVisible(ac) {
				ac.FirstSeen = s.State.SimTime
			}

			if passedWaypoint != nil {
				for tcp, wpCommands := range util.SortedMap(s.waypointCommands) {
					if cmds, ok := wpCommands[passedWaypoint.Fix]; ok {
						func() {
							// The mutex is held when we get here, but RunScriptedControlCommands and the
							// command methods it dispatches to acquire it themselves. Release it for the
							// duration and take it back afterward, including if they panic; otherwise the
							// deferred unlock in Update would release a mutex it no longer holds.
							s.mu.Unlock(s.lg)
							defer s.mu.Lock(s.lg)

							// Execute waypoint commands using the waypoint commands controller (typically an instructor)
							nav.NavLog(string(callsign), s.State.SimTime.NavTime(), nav.NavLogCommand, "aircraft=%s fix=%s commands=%s", callsign, passedWaypoint.Fix, cmds)
							s.lg.Infof("Waypoint commands: Aircraft %s passed %s, executing: %s", callsign, passedWaypoint.Fix, cmds)
							result := s.RunScriptedControlCommands(TCW(tcp), callsign, cmds)
							if result.Error != nil {
								nav.NavLog(string(callsign), s.State.SimTime.NavTime(), nav.NavLogCommand, "aircraft=%s error=%v remaining=%s", callsign, result.Error,
									result.RemainingInput)
								s.lg.Errorf("Waypoint command execution failed: %v (remaining: %s)", result.Error, result.RemainingInput)
							} else {
								nav.NavLog(string(callsign), s.State.SimTime.NavTime(), nav.NavLogCommand, "aircraft=%s success", callsign)
							}

							// Log updated route and waypoint state after commands
							nav.LogRoute(string(callsign), s.State.SimTime.NavTime(), ac.Nav.Waypoints)
							nav.NavLog(string(callsign), s.State.SimTime.NavTime(), nav.NavLogCommand,
								"aircraft=%s post-cmd nwaypoints=%d approach_cleared=%v approach_id=%s",
								callsign, len(ac.Nav.Waypoints), ac.Nav.Approach.Cleared, ac.Nav.Approach.AssignedId)
						}()
					}
				}

			}

			deletedByAction := false
			for _, event := range updateResult.ActionEvents {
				if s.applyWaypointActionEvent(ac, event) {
					deletedByAction = true
					break
				}
			}
			if deletedByAction {
				continue
			}

			if passedWaypoint != nil && passedWaypoint.SequenceVFRLanding() {
				s.sequenceVFRLanding(ac)
			}

			// Possibly go around
			if ac.GoAroundDistance != nil {
				if d, err := ac.DistanceToEndOfApproach(); err == nil && d < *ac.GoAroundDistance {
					s.lg.Debug("randomly going around")
					ac.GoAroundDistance = nil // only go around once
					s.goAround(ac)
				}
			}

			// Cull any departures withn ~5NM of their first /tc point
			culled := false
			if s.prespawnUncontrolledOnly && ac.IsDeparture() && ac.DepartureContactAltitude == 0 {
				for _, wp := range ac.Nav.Waypoints {
					if wp.HasTransferCommsAction() {
						if math.NMDistance2LLFast(ac.Position(), wp.Location, ac.NmPerLongitude()) < 5 {
							s.deleteAircraft(ac, DeletePrespawn)
							culled = true
						}
						break
					}
				}
			}
			if culled {
				continue
			}

			// Possibly contact the departure controller
			if ac.IsDeparture() && ((ac.DepartureContactAltitude > 0 && ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude) || (ac.DepartureContactAltitude == 0 && ac.EmergencyState != nil)) {
				fp := ac.NASFlightPlan
				if fp == nil {
					fp = s.STARSComputer.lookupFlightPlanBySquawk(ac.Squawk)
				}
				if fp != nil {
					// During prespawn uncontrolled-only phase, cull departures that would
					// contact a human controller rather than initiating the contact.
					if s.prespawnUncontrolledOnly && !s.isVirtualController(fp.InboundHandoffController) {
						s.deleteAircraft(ac, DeletePrespawn)
						continue
					}

					if !s.prespawn {
						// Time to check in
						// Use the original InboundHandoffController position for the radio event,
						// not the resolved position. This ensures TCWControlsPosition checks
						// correctly match when the user has that position consolidated.
						s.contactDeparture(ac, fp)
					}
				}
			}

			// Cull far-away aircraft
			if math.NMDistance2LL(ac.Position(), s.State.Center) > s.State.cullDistance() {
				s.lg.Debug("culled far-away aircraft", slog.String("adsb_callsign", string(callsign)))
				s.deleteAircraft(ac, DeleteCulled)
			}

			// Enqueue a spontaneous "field in sight" transmission if the pilot
			// wants to report and the field is currently visible.
			s.checkSpontaneousVisualRequest(ac)
		}

		s.possiblyRequestFlightFollowing()

		s.processFutureFrequencyChanges()
		s.processVirtualControllerContacts()

		s.processFutureOnCourse()
		s.processFutureSquawkChanges()
		s.processFutureFieldChecks()
		s.processFutureTrafficChecks()

		s.updateEmergencies()

		s.checkFinalApproachSpacing()

		s.updatePatternPhases()
		s.relievePatternPressure()
		s.admitHoldingArrivals()
		s.spawnAircraft()

		s.STARSComputer.Update(s)

		s.processInterfacilityVFR(s.State.SimTime)

		// Advance METAR: drop old entries when sim time passes the next one's report time
		for ap, metar := range s.METAR {
			for len(metar) > 1 && s.State.SimTime.Time().After(metar[1].Time) {
				metar = metar[1:]
			}
			s.METAR[ap] = metar
			if len(metar) > 0 {
				if s.State.METAR == nil {
					s.State.METAR = make(map[av.ICAOAirportCode]wx.METAR)
				}
				old := s.State.METAR[ap]
				if old.Raw != "" && old.Raw != metar[0].Raw {
					if cur, ok := s.State.ATISLetter[ap]; ok {
						s.State.ATISLetter[ap] = string(rune((cur[0]-'A'+1)%26 + 'A'))
						s.ATISChangedTime[ap] = s.State.SimTime
					}
				}
				s.State.METAR[ap] = metar[0]
			}
		}
	}
}

// sim/control.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// TCWCanCommandAircraft returns true if the TCW can issue ATC commands to an aircraft
// (altitude, heading, speed, etc.). This is true if the TCW is privileged or controls
// the position whose frequency the aircraft is tuned to.
func (s *Sim) TCWCanCommandAircraft(tcw TCW, ac *Aircraft) bool {
	return s.PrivilegedTCWs[tcw] ||
		(ac != nil && s.State.TCWControlsPosition(tcw, ac.ControllerFrequency))
}

// TCWCanModifyTrack returns true if the TCW can modify the track itself (delete, reposition).
// This is true if the TCW is privileged, owns the track, or controls the TrackingController position.
func (s *Sim) TCWCanModifyTrack(tcw TCW, fp *NASFlightPlan) bool {
	return s.PrivilegedTCWs[tcw] ||
		fp.OwningTCW == tcw ||
		s.State.TCWControlsPosition(tcw, fp.TrackingController) ||
		s.State.TCWControlsPosition(tcw, fp.LastLocalController)
}

// TCWCanModifyFlightPlan returns true if the TCW can access/modify flight plan fields.
// Checks if TCW controls the owner's position (consolidation-aware). This is true if
// the TCW is privileged, owns the track, or controls the position that owns the track.
func (s *Sim) TCWCanModifyFlightPlan(tcw TCW, fp *NASFlightPlan) bool {
	return s.PrivilegedTCWs[tcw] ||
		fp.OwningTCW == tcw ||
		s.State.TCWControlsPosition(tcw, fp.TrackingController) ||
		s.State.TCWControlsPosition(tcw, fp.LastLocalController)
}

func (s *Sim) dispatchAircraftCommand(tcw TCW, callsign av.ADSBCallsign, check func(tcw TCW, ac *Aircraft) error,
	cmd func(tcw TCW, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	s.lastControlCommandTime = time.Now()

	if ac, ok := s.Aircraft[callsign]; !ok {
		return nil, av.ErrNoAircraftForCallsign
	} else if _, ok := s.State.CurrentConsolidation[tcw]; !ok {
		return nil, ErrUnknownController
	} else {
		if check != nil {
			if err := check(tcw, ac); err != nil {
				return nil, err
			}
		}

		preAc := *ac
		intent := cmd(tcw, ac)

		s.lg.Info("dispatch_command", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
			slog.Any("intent", intent))

		return intent, nil
	}
}

// dispatchControlledAircraftCommand dispatches a command to an aircraft if the
// TCW controls the position whose frequency the aircraft is tuned to.
func (s *Sim) dispatchControlledAircraftCommand(tcw TCW, callsign av.ADSBCallsign,
	cmd func(tcw TCW, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	intent, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			if !s.TCWCanCommandAircraft(tcw, ac) {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)

	// If command succeeded, cancel any pending initial contact for this aircraft.
	// This handles the case where a controller issues commands to an aircraft
	// that hasn't checked in yet.
	if err == nil {
		s.cancelPendingInitialContact(callsign)
	}

	return intent, err
}

// Note that ac may be nil, but flight plan will not be!
func (s *Sim) dispatchFlightPlanCommand(tcw TCW, acid ACID, check func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	s.lastControlCommandTime = time.Now()

	fp, ac, _ := s.getFlightPlanForACID(acid)
	if fp == nil {
		return nil, ErrNoMatchingFlightPlan
	}
	// ac may or may not be nil; we'll pass it along if we have it

	if _, ok := s.State.CurrentConsolidation[tcw]; !ok {
		return nil, ErrUnknownController
	}

	if check != nil {
		if err := check(tcw, fp, ac); err != nil {
			return nil, err
		}
	}

	preFp := *fp
	intent := cmd(tcw, fp, ac)

	s.lg.Info("dispatch_fp_command", slog.String("acid", string(fp.ACID)),
		slog.Any("prepost_fp", []NASFlightPlan{preFp, *fp}),
		slog.Any("intent", intent))

	return intent, nil
}

func (s *Sim) dispatchTrackedFlightPlanCommand(tcw TCW, acid ACID, check func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcw TCW, fp *NASFlightPlan, ac *Aircraft)) error {
	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if !s.TCWCanModifyFlightPlan(tcw, fp) {
				return av.ErrOtherControllerHasTrack
			}
			if check != nil {
				return check(tcw, fp, ac)
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			cmd(tcw, fp, ac)
			// No radio transmissions for these
			return nil
		})
	return err
}

func (s *Sim) DeleteAircraft(tcw TCW, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcw {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			s.eventStream.Post(Event{
				Type:        StatusMessageEvent,
				WrittenText: fmt.Sprintf("%s deleted %s", tcw, ac.ADSBCallsign),
			})

			s.deleteAircraft(ac)

			return nil
		})
	return err
}

func (s *Sim) deleteAircraft(ac *Aircraft) {
	if s.CIDAllocator != nil {
		if fp := ac.NASFlightPlan; fp != nil && fp.CID != "" {
			s.CIDAllocator.Release(fp.CID)
			fp.CID = ""
		} else if fp := s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign)); fp != nil && fp.CID != "" {
			s.CIDAllocator.Release(fp.CID)
			fp.CID = ""
		}
	}
	delete(s.Aircraft, ac.ADSBCallsign)

	// Remove any pending transmissions from this aircraft
	for tcp := range s.PendingContacts {
		s.PendingContacts[tcp] = slices.DeleteFunc(s.PendingContacts[tcp],
			func(pc PendingContact) bool { return pc.ADSBCallsign == ac.ADSBCallsign })
	}

	delete(s.DeferredContacts, ac.ADSBCallsign)

	// Remove any scheduled future events for this aircraft
	s.FutureOnCourse = slices.DeleteFunc(s.FutureOnCourse,
		func(foc FutureOnCourse) bool { return foc.ADSBCallsign == ac.ADSBCallsign })
	s.FutureSquawkChanges = slices.DeleteFunc(s.FutureSquawkChanges,
		func(fcs FutureChangeSquawk) bool { return fcs.ADSBCallsign == ac.ADSBCallsign })
	s.FutureEmergencyUpdates = slices.DeleteFunc(s.FutureEmergencyUpdates,
		func(feu FutureEmergencyUpdate) bool { return feu.ADSBCallsign == ac.ADSBCallsign })

	s.STARSComputer.HoldForRelease = slices.DeleteFunc(s.STARSComputer.HoldForRelease,
		func(a *Aircraft) bool { return ac.ADSBCallsign == a.ADSBCallsign })

	fp := ac.NASFlightPlan
	if fp == nil {
		fp = s.STARSComputer.takeFlightPlanByACID(ACID(ac.ADSBCallsign))
	}
	if fp != nil {
		delete(s.Handoffs, fp.ACID)
		delete(s.PointOuts, fp.ACID)
		s.deleteFlightPlan(fp)
	}

	s.lg.Info("deleted aircraft", slog.String("adsb_callsign", string(ac.ADSBCallsign)))
}

func (s *Sim) DeleteAircraftSlice(tcw TCW, aircraft []Aircraft) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for _, ac := range aircraft {
		s.deleteAircraft(&ac)
	}

	return nil
}

func (s *Sim) DeleteAllAircraft(tcw TCW) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcw {
		return av.ErrOtherControllerHasTrack
	}

	for _, ac := range s.Aircraft {
		// Only delete airborne aircraft; leave all of the ones at the
		// gate, etc., so we don't have a bubble of no departures for a
		// long time while the departure queues refill.
		if ac.IsAirborne() {
			s.deleteAircraft(ac)
		}
	}

	// Also clean up aircraft in HFR departure queues
	s.clearDepartureQueues()

	return nil
}

func (s *Sim) clearDepartureQueues() {
	// Clear HFR (Hold For Release) departure queues to remove aircraft that are held for release
	for _, runways := range s.DepartureState {
		for _, depState := range runways {
			// Delete aircraft from the Held queue (HFR)
			for _, dep := range depState.Held {
				if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok {
					s.deleteAircraft(ac)
				}
			}

			// Clear the HFR queue
			depState.Held = nil
		}
	}
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

func (s *Sim) CreateFlightPlan(tcw TCW, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	fp, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
	if err != nil {
		return err
	}

	fp.OwningTCW = tcw

	if util.SeqContainsFunc(maps.Values(s.Aircraft),
		func(ac *Aircraft) bool { return ac.IsAssociated() && ac.NASFlightPlan.ACID == fp.ACID }) {
		return ErrDuplicateACID
	}
	if slices.ContainsFunc(s.STARSComputer.FlightPlans,
		func(fp2 *NASFlightPlan) bool { return fp.ACID == fp2.ACID }) {
		return ErrDuplicateACID
	}

	fp, err = s.STARSComputer.CreateFlightPlan(fp)

	if err == nil {
		err = s.postCheckFlightPlanSpecifier(spec)
	}

	return err
}

// General checks both for create and modify; this returns errors that prevent fp creation.
func (s *Sim) preCheckFlightPlanSpecifier(spec *FlightPlanSpecifier) error {
	if spec.ACID.IsSet {
		acid := spec.ACID.Get()
		if !IsValidACID(string(acid)) {
			return ErrIllegalACID
		}
	}

	if spec.Rules.IsSet && spec.Rules.Get() == av.FlightRulesVFR {
		// Disable MSAW for VFR flight plans unless specifically enabled.
		if !spec.DisableMSAW.IsSet {
			spec.DisableMSAW.Set(true)
		}
	}

	if spec.TrackingController.IsSet {
		tcp := spec.TrackingController.Get()
		// TODO: this will need to be more sophisticated with consolidation.
		if _, ok := s.State.Controllers[tcp]; !ok {
			return ErrUnknownController
		}
	}

	if spec.SquawkAssignment.IsSet {
		if str := spec.SquawkAssignment.Get(); len(str) == 4 {
			if sq, err := av.ParseSquawk(str); err == nil && s.LocalCodePool.IsReservedVFRCode(sq) {
				return ErrIllegalBeaconCode
			}
		}
	}

	// TODO: validate entry/exit fixes

	return nil
}

// General checks both for create and modify; this returns informational
// messages that don't prevent the fp from being created.
func (s *Sim) postCheckFlightPlanSpecifier(spec FlightPlanSpecifier) error {
	if spec.AircraftType.IsSet {
		if _, ok := av.DB.AircraftPerformance[spec.AircraftType.Get()]; !ok {
			return ErrIllegalACType
		}
	}

	return nil
}

func (s *Sim) ModifyFlightPlan(tcw TCW, acid ACID, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	fp, _, active := s.getFlightPlanForACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	// Can't set assigned altitude for non-associated tracks.
	if !active && spec.AssignedAltitude.IsSet {
		return ErrTrackIsNotActive
	}

	if !s.TCWCanModifyFlightPlan(tcw, fp) {
		return av.ErrOtherControllerHasTrack
	}

	if active {
		// Modify assigned
		if spec.EntryFix.IsSet || spec.ExitFix.IsSet || spec.CoordinationTime.IsSet {
			// These can only be set for non-active flight plans: 5-171
			return ErrTrackIsActive
		}

		if spec.GlobalLeaderLineDirection.IsSet {
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				ACID:                acid,
				FromController:      s.State.PrimaryPositionForTCW(tcw),
				LeaderLineDirection: spec.GlobalLeaderLineDirection.Get(),
			})
		}
	}

	fp.Update(spec, s)

	return s.postCheckFlightPlanSpecifier(spec)
}

// Associate the specified flight plan with the track. Flight plan for ACID
// must not already exist.
func (s *Sim) AssociateFlightPlan(tcw TCW, callsign av.ADSBCallsign, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.QuickFlightPlan.IsSet && spec.QuickFlightPlan.Get() {
		base := s.State.FacilityAdaptation.FlightPlan.QuickACID
		acid := base + fmt.Sprintf("%02d", s.QuickFlightPlanIndex%100)
		spec.ACID.Set(ACID(acid))
		s.QuickFlightPlanIndex++
	}
	if !spec.ACID.IsSet {
		spec.ACID.Set(ACID(callsign))
	}

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	_, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.IsAssociated() {
				return av.ErrOtherControllerHasTrack
			}
			if s.STARSComputer.lookupFlightPlanByACID(spec.ACID.Get()) != nil {
				return ErrDuplicateACID
			}

			fp, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
			if err != nil {
				return err
			}
			if _, err := s.STARSComputer.CreateFlightPlan(fp); err != nil {
				return err
			}

			return nil
		},
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// Either the flight plan was passed in or fp was initialized  in the validation function.
			fp := s.STARSComputer.takeFlightPlanByACID(spec.ACID.Get())

			fp.Update(spec, s)

			ac.AssociateFlightPlan(fp)

			// Create a flight strip if one doesn't already exist.
			// Assign to TrackingController so the strip follows
			// the position if consolidation changes.
			if shouldCreateFlightStrip(fp) {
				owner := fp.TrackingController
				if owner == "" {
					owner = s.State.PrimaryPositionForTCW(tcw)
				}
				s.initFlightStrip(fp, owner)
			}

			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: fp.ACID,
			})

			return nil
		})

	if err == nil {
		err = s.postCheckFlightPlanSpecifier(spec)
	}
	return err
}

// Flight plan for acid must already exist; spec gives optional amendments.
func (s *Sim) ActivateFlightPlan(tcw TCW, callsign av.ADSBCallsign, acid ACID, spec *FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Validate the target aircraft BEFORE taking the flight plan
	// to avoid orphaning it if the target is invalid.
	ac, ok := s.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	if ac.IsAssociated() {
		return ErrTrackIsActive
	}

	fp := s.STARSComputer.takeFlightPlanByACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if spec != nil {
		fp.Update(*spec, s)
	}

	s.lastControlCommandTime = time.Now()

	ac.AssociateFlightPlan(fp)

	s.eventStream.Post(Event{
		Type: FlightPlanAssociatedEvent,
		ACID: fp.ACID,
	})

	return nil
}

func (s *Sim) DeleteFlightPlan(tcw TCW, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			if s.TCWCanModifyTrack(tcw, ac.NASFlightPlan) {
				fp := ac.DisassociateFlightPlan()
				fp.DeleteTime = s.State.SimTime.Add(4 * time.Minute)
				s.STARSComputer.FlightPlans = append(s.STARSComputer.FlightPlans, fp)
				return nil
			}
		}
	}

	if fp := s.STARSComputer.takeFlightPlanByACID(acid); fp != nil {
		s.deleteFlightPlan(fp)
		return nil
	}

	return ErrNoMatchingFlightPlan
}

func (s *Sim) deleteFlightPlan(fp *NASFlightPlan) {
	if s.CIDAllocator != nil && fp.CID != "" {
		s.CIDAllocator.Release(fp.CID)
	}
	if fp.StripOwner != "" {
		s.freeStripCID(fp.StripCID)
	}
	s.STARSComputer.returnListIndex(fp.ListIndex)
	if fp.PlanType == LocalNonEnroute {
		s.LocalCodePool.Return(fp.AssignedSquawk)
	} else {
		s.ERAMComputer.SquawkCodePool.Return(fp.AssignedSquawk)
	}
}

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
	acceptDelay := 4 + s.Rand.Intn(10)
	s.Handoffs[fp.ACID] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
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
	s.cancelPendingFrequencyChange(ac.ADSBCallsign)
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

			if ac != nil {
				haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
					func(wp av.Waypoint) bool { return wp.TransferComms() })
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
				if !s.State.FacilityAdaptation.ForceQLToSelf || fp.OwningTCW != tcw {
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
			fromTCP := s.State.PrimaryPositionForTCW(fromTCW)
			if octrl, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if octrl.Facility != s.State.Controllers[fromTCP].Facility {
				// Can't point out to another STARS facility.
				return av.ErrInvalidController
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

	acceptDelay := 4 + s.Rand.Intn(10)
	s.PointOuts[acid] = PointOut{
		FromController: TCP(from.PositionId()),
		ToController:   TCP(to.PositionId()),
		AcceptTime:     s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
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
	magVar := ac.Nav.FlightState.MagneticVariation
	const nmPerLatitude float32 = 60

	var distance float32
	var futureWaypoints []math.Point2LL

	for _, wp := range waypointPairs {
		legDistance := math.NMDistance2LL(currentPos, wp)

		if distance+legDistance >= requiredDistance {
			// The endpoint is somewhere along this leg
			remainingDistance := requiredDistance - distance
			bearing := math.Heading2LL(currentPos, wp, nmPerLongitude, magVar)

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

func (s *Sim) ReleaseDeparture(tcw TCW, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	ac, ok := s.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	fp := s.STARSComputer.lookupFlightPlanByACID(ACID(callsign))
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if !s.State.TCWControlsPosition(tcw, fp.InboundHandoffController) {
		return ErrInvalidDepartureController
	}

	if err := s.STARSComputer.ReleaseDeparture(callsign); err == nil {
		ac.Released = true
		ac.ReleaseTime = s.State.SimTime
		return nil
	} else {
		return err
	}
}

func (s *Sim) ShouldTriggerPilotMixUp(callsign av.ADSBCallsign) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// If pilot errors are disabled (interval == 0), never trigger mix-ups
	if s.PilotErrorInterval == 0 {
		return false
	}

	// Check if enough time has passed since the last pilot error globally
	if s.State.SimTime.Sub(s.LastPilotError) <= s.PilotErrorInterval {
		return false
	}

	// Check if we've recently communicated with this specific aircraft
	if ac, ok := s.Aircraft[callsign]; ok {
		// Don't trigger mix-up if we just communicated with this pilot
		if !ac.LastRadioTransmission.IsZero() && s.State.SimTime.Sub(ac.LastRadioTransmission) < 20*time.Second {
			return false
		}
	}

	// Update the last error time and trigger the mix-up
	s.LastPilotError = s.State.SimTime
	return true
}

// PilotMixUp is called standalone (not as part of a command batch) so it posts its own event.
// Returns the spoken text for TTS synthesis.
func (s *Sim) PilotMixUp(tcw TCW, callsign av.ADSBCallsign) (string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	intent, err := s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.PilotMixUp()
		})
	if err == nil && intent != nil {
		spokenText := s.renderAndPostReadback(callsign, tcw, []av.CommandIntent{intent})
		return spokenText, nil
	}
	return "", err
}

func (s *Sim) AssignAltitude(tcw TCW, callsign av.ADSBCallsign, altitude int, afterSpeed bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

type HeadingArgs struct {
	TCW          TCW
	ADSBCallsign av.ADSBCallsign
	Heading      int
	Present      bool
	LeftDegrees  int
	RightDegrees int
	Turn         av.TurnDirection
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(hdg.TCW, hdg.ADSBCallsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if hdg.Present {
				return ac.FlyPresentHeading(s.State.SimTime)
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees, s.State.SimTime)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees, s.State.SimTime)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn, s.State.SimTime)
			}
		})
}

func (s *Sim) AssignMach(tcw TCW, callsign av.ADSBCallsign, mach float32, afterAltitude bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			temp := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime).Temperature() + 273.15
			return ac.AssignMach(mach, afterAltitude, temp)
		})
}

func (s *Sim) AssignSpeed(tcw TCW, callsign av.ADSBCallsign, speed int, afterAltitude bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) AssignSpeedUntil(tcw TCW, callsign av.ADSBCallsign, speed int, until *av.SpeedUntil) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AssignSpeedUntil(speed, until)
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
			tempK := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime).Temperature() + 273.15
			return ac.SaySpeed(tempK)
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
			tempK := s.wxModel.Lookup(ac.Nav.FlightState.Position, ac.Nav.FlightState.Altitude, s.State.SimTime).Temperature() + 273.15
			return ac.SayMach(tempK)
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

// SayFieldInSight asks the pilot "do you have the field in sight?"
// Pilot responds based on VMC conditions and visual approach availability.
func (s *Sim) SayFieldInSight(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if ac.Nav.Approach.Assigned == nil {
				return av.MakeUnableIntent("unable. we haven't been assigned an approach")
			}

			// If the pilot already requested the visual, they've
			// confirmed field in sight — don't re-evaluate.
			if ac.RequestedVisual {
				return av.FieldInSightIntent{HasField: true, Runway: ac.Nav.Approach.Assigned.Runway}
			}

			elig := s.checkVisualEligibility(ac)
			if elig.FieldInSight {
				return av.FieldInSightIntent{HasField: true, Runway: elig.Runway}
			}
			return av.FieldInSightIntent{HasField: false}
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

func (s *Sim) DirectFix(tcw TCW, callsign av.ADSBCallsign, fix string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DirectFix(fix, s.State.SimTime)
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

func (s *Sim) CrossFixAt(tcw TCW, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, speed int, mach float32) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CrossFixAt(fix, ar, speed, mach)
		})
}

func (s *Sim) AtFixCleared(tcw TCW, callsign av.ADSBCallsign, fix, approach string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.AtFixCleared(fix, approach)
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

func (s *Sim) ExpectApproach(tcw TCW, callsign av.ADSBCallsign, approach, lahsoRunway string) (av.CommandIntent, error) {
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
			return ac.ExpectApproach(approach, ap, lahsoRunway, s.lg)
		})
}

func (s *Sim) ClearedApproach(tcw TCW, callsign av.ADSBCallsign, approach string, straightIn bool) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			var intent av.CommandIntent
			var ok bool
			if straightIn {
				intent, ok = ac.ClearedStraightInApproach(approach, s.State.SimTime, s.lg)
			} else {
				intent, ok = ac.ClearedApproach(approach, s.State.SimTime, s.lg)
			}

			if ok {
				ac.ApproachTCP = TCP(ac.ControllerFrequency)
			}
			return intent
		})
}

// ClearedVisualApproach clears the aircraft for a visual approach to the
// specified runway. Command format is "CV<runway>" (e.g. "CV13L"). The
// aircraft flies a 3nm final aligned with the runway heading to the
// threshold. For charted visual approaches (e.g., Belmont Visual), use
// the C command with the approach ID instead.
func (s *Sim) ClearedVisualApproach(tcw TCW, callsign av.ADSBCallsign, runway string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// Clear direct to the runway.
			// If the aircraft is too close for a stable approach, go around.
			intent, ok := ac.ClearedDirectVisual(runway, s.State.SimTime)
			if !ok {
				s.goAround(ac)
				return nil
			}
			ac.ApproachTCP = TCP(ac.ControllerFrequency)
			return intent
		})
}

func (s *Sim) InterceptLocalizer(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
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

func (s *Sim) ContactTower(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			result, ok := ac.ContactTower(s.lg)
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
// Command format: TRAFFIC/oclock/miles/altitude (e.g., TRAFFIC/10/4/30 for 10 o'clock, 4 miles, 3000 ft)
func (s *Sim) TrafficAdvisory(tcw TCW, callsign av.ADSBCallsign, command string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Parse the command: TRAFFIC/oclock/miles/altitude
	parts := strings.Split(command, "/")
	if len(parts) != 4 {
		return nil, ErrInvalidCommandSyntax
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
			return s.handleTrafficAdvisory(ac, oclock, miles, trafficAltFeet)
		})
}

// handleTrafficAdvisory determines the pilot response to a traffic advisory based on:
// 1. Weather conditions (IMC -> "we're in IMC")
// 2. Presence of traffic (if no traffic in area -> "looking")
// 3. Random chance based on proximity (closer/higher -> more likely "traffic in sight")
func (s *Sim) handleTrafficAdvisory(ac *Aircraft, oclock int, miles int, trafficAltFeet float32) av.CommandIntent {
	// Check weather conditions - find closest airport's METAR
	isIMC := false
	if len(s.State.METAR) > 0 {
		var closestMETAR wx.METAR
		closestDist := float32(999999)
		for _, metar := range s.State.METAR {
			if ap, ok := s.State.Airports[metar.ICAO]; ok {
				dist := math.NMDistance2LL(ac.Position(), ap.Location)
				if dist < closestDist {
					closestDist = dist
					closestMETAR = metar
				}
			}
		}
		if closestMETAR.ICAO != "" && !closestMETAR.IsVMC() {
			isIMC = true
		}
	}

	if isIMC {
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseIMC}
	}

	// Convert o'clock to heading offset from aircraft heading
	// 12 o'clock = 0 degrees, 3 o'clock = 90 degrees, etc.
	oclockHeading := float32((oclock % 12) * 30) // 0, 30, 60, 90... 330
	trafficHeading := math.NormalizeHeading(ac.Heading() + oclockHeading)

	// Calculate the approximate position of the reported traffic
	nmPerLong := ac.NmPerLongitude()
	magVar := ac.MagneticVariation()
	trafficPos := math.Offset2LL(ac.Position(), trafficHeading, float32(miles), nmPerLong, magVar)

	// Search for actual traffic near the reported position
	// Tolerance: +/- 2 miles horizontal, +/- 1000 feet vertical
	const horizontalToleranceNM = 2.0
	const verticalToleranceFeet = 1000.0

	trafficFound := false
	for cs, other := range s.Aircraft {
		if cs == ac.ADSBCallsign {
			continue // Skip self
		}

		dist := math.NMDistance2LL(trafficPos, other.Position())
		altDiff := math.Abs(other.Altitude() - trafficAltFeet)

		if dist <= horizontalToleranceNM && altDiff <= verticalToleranceFeet {
			trafficFound = true
			break
		}
	}

	if !trafficFound {
		// No traffic found - respond "looking"
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
	}

	// Traffic found - determine probability of seeing it based on:
	// 1. Distance (closer = more likely to see)
	// 2. Relative altitude (higher than us = easier to see against sky, lower = harder against ground)

	// Base probability: start at 70%
	seeProb := float32(0.7)

	// Distance factor: closer is better (linear from 1.0 at 0 miles to 0.4 at 10+ miles)
	distFactor := float32(1.0) - float32(min(miles, 10))*0.06
	seeProb *= distFactor

	// Altitude factor: traffic above is easier to see
	acAlt := ac.Altitude()
	if trafficAltFeet > acAlt+500 {
		// Traffic is significantly higher - easier to see against sky
		seeProb *= 1.3
	} else if trafficAltFeet < acAlt-500 {
		// Traffic is significantly lower - harder to see against ground
		seeProb *= 0.7
	}

	// Cap probability between 0.2 and 0.95
	seeProb = max(0.2, min(0.95, seeProb))

	// Roll the dice
	if s.Rand.Float32() < seeProb {
		ac.TrafficInSight = true
		ac.TrafficInSightTime = s.State.SimTime
		return av.TrafficAdvisoryIntent{Response: av.TrafficResponseTrafficSeen, WillMaintainSeparation: s.Rand.Float32() < 0.3}
	}

	// "Looking" - schedule possible delayed traffic-in-sight call
	ac.TrafficLookingUntil = s.State.SimTime.Add(time.Duration(10+s.Rand.Intn(10)) * time.Second)
	return av.TrafficAdvisoryIntent{Response: av.TrafficResponseLooking}
}

// checkDelayedTrafficInSight checks if an aircraft that said "looking" should now report traffic in sight.
func (s *Sim) checkDelayedTrafficInSight(ac *Aircraft) {
	// Only check if we're within the looking window
	if ac.TrafficLookingUntil.IsZero() || s.State.SimTime.After(ac.TrafficLookingUntil) {
		ac.TrafficLookingUntil = time.Time{} // Clear expired window
		return
	}

	// Must be on a frequency to transmit
	if ac.ControllerFrequency == "" {
		return
	}

	// Random chance each update to report traffic in sight (roughly 1/20 chance per second at 10 updates/sec)
	if s.Rand.Intn(200) != 0 {
		return
	}

	// Report traffic in sight
	ac.TrafficInSight = true
	ac.TrafficInSightTime = s.State.SimTime
	ac.TrafficLookingUntil = time.Time{} // Clear the looking window

	// Queue the transmission
	s.enqueuePilotTransmission(ac.ADSBCallsign, TCP(ac.ControllerFrequency), PendingTransmissionTrafficInSight)
}

// canRequestVisualApproach reports whether an aircraft is eligible to
// spontaneously request the visual approach. The aircraft must be an
// arrival on frequency, assigned a non-visual approach that hasn't been
// cleared yet, and must not have already made the request.
func (ac *Aircraft) canRequestVisualApproach() bool {
	if ac.IsDeparture() || ac.RequestedVisual || ac.ControllerFrequency == "" {
		return false
	}
	if ac.Nav.Approach.AssignedId == "" || ac.Nav.Approach.Cleared {
		return false
	}
	appr := ac.Nav.Approach.Assigned
	// Already on a visual — nothing to request.
	return appr != nil && appr.Type != av.ChartedVisualApproach
}

// VisualEligibility describes whether an aircraft can see the field
// and request a visual approach.
type VisualEligibility struct {
	FieldInSight bool    // true if VMC, within range, and airport visible
	Runway       string  // runway for the visual approach (when FieldInSight)
	Distance     float32 // distance to airport in nm (valid when FieldInSight)
	Visibility   float32 // visibility in SM (valid when FieldInSight)
}

// checkVisualEligibility determines whether the aircraft can see the field.
// A visual approach does not require a charted visual procedure; VMC and
// field in sight are sufficient.
// Shared by SayFieldInSight and checkSpontaneousVisualRequest.
func (s *Sim) checkVisualEligibility(ac *Aircraft) VisualEligibility {
	arrivalAirport := ac.FlightPlan.ArrivalAirport
	ap := s.State.Airports[arrivalAirport]
	if ap == nil {
		return VisualEligibility{}
	}

	// Must be VMC at the arrival airport.
	metar, ok := s.State.METAR[arrivalAirport]
	if !ok || !metar.IsVMC() {
		return VisualEligibility{}
	}

	// Must be within 15nm of the airport.
	dist := math.NMDistance2LLFast(ac.Position(), ap.Location, ac.NmPerLongitude())
	if dist > 15 {
		return VisualEligibility{}
	}

	// The airport must be within the pilot's forward visibility arc
	// (~120 degrees off the nose).
	bearingToAirport := math.Heading2LL(ac.Position(), ap.Location, ac.NmPerLongitude(), ac.MagneticVariation())
	if math.HeadingDifference(ac.Heading(), bearingToAirport) > 120 {
		return VisualEligibility{}
	}

	if ac.Nav.Approach.Assigned == nil {
		return VisualEligibility{}
	}
	runway := ac.Nav.Approach.Assigned.Runway

	vis, err := metar.Visibility()
	if err != nil {
		return VisualEligibility{}
	}

	return VisualEligibility{
		FieldInSight: true,
		Runway:       runway,
		Distance:     float32(dist),
		Visibility:   vis,
	}
}

// VisualDistanceFactor returns a probability scaling factor based on
// distance to the airport: 1.0 at <=3nm, tapering to 0.25 at 15nm.
func VisualDistanceFactor(dist float32) float32 {
	if dist <= 3 {
		return 1.0
	}
	return 1.0 - 0.75*(dist-3)/12
}

// VisualVisibilityFactor returns a probability scaling factor based on
// visibility: 0.3 at 3SM, ramping to 1.0 at 10SM+.
func VisualVisibilityFactor(vis float32) float32 {
	if vis >= 10 {
		return 1.0
	}
	if vis > 3 {
		return 0.3 + 0.7*(vis-3)/7
	}
	return 0.3
}

// checkSpontaneousVisualRequest checks if an arrival aircraft should
// spontaneously report "field in sight, requesting the visual approach".
// Called once per second from the update loop. The pilot only requests
// when VMC, close to the airport, and a charted visual approach exists
// for the assigned runway; probability scales with distance and visibility.
func (s *Sim) checkSpontaneousVisualRequest(ac *Aircraft) {
	if !ac.canRequestVisualApproach() {
		return
	}

	elig := s.checkVisualEligibility(ac)
	if !elig.FieldInSight {
		return
	}

	// ~3% base probability per second, scaled by distance and visibility.
	// At 3nm in clear weather this gives ~3%/s (~30s expected wait);
	// at 10nm it drops to ~1%/s.
	distFactor := VisualDistanceFactor(elig.Distance)
	visFactor := VisualVisibilityFactor(elig.Visibility)
	prob := 0.03 * distFactor * visFactor
	if s.Rand.Float32() >= prob {
		return
	}

	ac.RequestedVisual = true
	s.enqueuePilotTransmission(ac.ADSBCallsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestVisual)
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
				return av.VisualSeparationIntent{}
			}
			// If they don't have traffic in sight, they can't maintain visual separation
			return av.MakeUnableIntent("unable, we don't have the traffic")
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
			s.cancelPendingFrequencyChange(ac.ADSBCallsign)
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
	spokenText := tr.Spoken(s.Rand) + s.readbackCallsignSuffix(callsign, tcw)
	return callsign, spokenText, nil
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
	spokenText := tr.Spoken(s.Rand) + s.readbackCallsignSuffix(callsign, tcw)
	return callsign, spokenText, nil
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

///////////////////////////////////////////////////////////////////////////
// Deferred operations

// PendingTransmissionType identifies the type of pilot-initiated transmission.
type PendingTransmissionType int

const (
	PendingTransmissionDeparture                PendingTransmissionType = iota // Departure checking in
	PendingTransmissionArrival                                                 // Arrival/handoff checking in
	PendingTransmissionTrafficInSight                                          // "Traffic in sight" call
	PendingTransmissionFlightFollowingReq                                      // Abbreviated "VFR request"
	PendingTransmissionFlightFollowingFull                                     // Full flight following request
	PendingTransmissionGoAround                                                // Go-around announcement
	PendingTransmissionEmergency                                               // Emergency stage transmission
	PendingTransmissionRequestApproachClearance                                // Pilot requesting approach clearance
	PendingTransmissionRequestVisual                                           // Spontaneous "field in sight, requesting visual"
)

// PendingFrequencyChange represents a pilot switching to a new frequency.
// Once the Time passes, the aircraft's ControllerFrequency is set and
// the entry is removed.
type PendingFrequencyChange struct {
	ADSBCallsign av.ADSBCallsign
	TCP          TCP
	Time         time.Time
}

// PendingContact represents a pilot-initiated transmission waiting to be played.
type PendingContact struct {
	ADSBCallsign           av.ADSBCallsign
	TCP                    TCP
	ReadyTime              time.Time               // When pilot is ready to transmit
	Type                   PendingTransmissionType // What kind of transmission
	ReportDepartureHeading bool                    // For departures: include assigned heading
	HasQueuedEmergency     bool                    // For departures: trigger emergency after contact
	PrebuiltTransmission   *av.RadioTransmission   // For emergency transmissions: pre-built message
	FirstInFacility        bool                    // For arrivals: first contact in this TRACON facility
}

// addPendingContact adds an aircraft to the pending contacts queue for a controller.
func (s *Sim) addPendingContact(pc PendingContact) {
	if s.PendingContacts == nil {
		s.PendingContacts = make(map[TCP][]PendingContact)
	}
	s.PendingContacts[pc.TCP] = append(s.PendingContacts[pc.TCP], pc)
}

// cancelPendingFrequencyChange removes any pending frequency change for
// the given aircraft. Called when the aircraft's frequency is being managed
// directly (e.g., controller contact, radar services terminated) to prevent
// a stale queued switch from overwriting the new state.
func (s *Sim) cancelPendingFrequencyChange(callsign av.ADSBCallsign) {
	s.PendingFrequencyChanges = slices.DeleteFunc(s.PendingFrequencyChanges,
		func(pfc PendingFrequencyChange) bool {
			return pfc.ADSBCallsign == callsign
		})
}

// processPendingFrequencySwitches sets ControllerFrequency for aircraft whose
// frequency-switch time has passed, then removes those entries.
func (s *Sim) processPendingFrequencySwitches() {
	now := s.State.SimTime
	var switched []*Aircraft
	s.PendingFrequencyChanges = util.FilterSliceInPlace(s.PendingFrequencyChanges,
		func(pfc PendingFrequencyChange) bool {
			if now.After(pfc.Time) {
				if ac, ok := s.Aircraft[pfc.ADSBCallsign]; ok {
					ac.ControllerFrequency = pfc.TCP
					switched = append(switched, ac)
				}
				return false
			}
			return true
		})
	for _, ac := range switched {
		s.processDeferredContact(ac)
	}
}

// PopReadyContact removes and returns the first pending contact whose ReadyTime has passed
// for any of the given positions, or nil if none are ready yet.
// This is called when the client is ready to play a contact.
func (s *Sim) PopReadyContact(positions []TCP) *PendingContact {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.popReadyContact(positions)
}

// popReadyContact is the internal version that requires the lock to already be held.
func (s *Sim) popReadyContact(positions []TCP) *PendingContact {
	if s.PendingContacts == nil {
		return nil
	}

	// Find the first contact that's ready (ReadyTime has passed) for any position
	for _, tcp := range positions {
		for i, pc := range s.PendingContacts[tcp] {
			if s.State.SimTime.After(pc.ReadyTime) {
				s.PendingContacts[tcp] = slices.Delete(s.PendingContacts[tcp], i, i+1)
				return &pc
			}
		}
	}

	return nil
}

// processVirtualControllerContacts handles pending contacts for virtual
// controllers. Human controllers' contacts are processed by their clients
// via PopReadyContact/GenerateContactTransmission, but virtual controllers
// have no client, so we process their contacts here in the update loop.
func (s *Sim) processVirtualControllerContacts() {
	for tcp, contacts := range s.PendingContacts {
		if !s.isVirtualController(tcp) {
			continue
		}

		s.PendingContacts[tcp] = util.FilterSliceInPlace(contacts,
			func(pc PendingContact) bool {
				if !s.State.SimTime.After(pc.ReadyTime) {
					return true // not ready yet; leave it in the slice
				}

				if ac, ok := s.Aircraft[pc.ADSBCallsign]; ok && ac.IsDeparture() {
					// For departures contacting virtual controllers, enqueue climbing to
					// cruise altitude.
					s.enqueueDepartOnCourse(ac.ADSBCallsign)
				}
				// In any case, we can drop it from the pending contacts slice.
				return false
			})
	}
}

// enqueueControllerContact adds an aircraft to the pending contacts queue.
// Called when an aircraft should contact a controller (after handoff accepted, etc.)
// fromPos is the controller position the aircraft is coming from, used to
// determine whether this is the first contact in a TRACON facility (for ATIS reporting).
func (s *Sim) enqueueControllerContact(ac *Aircraft, tcp TCP, fromPos ControlPosition) {
	// Aircraft will switch frequency (2-4 sec), then listen before transmitting (3-6 sec).
	switchDelay := time.Duration(2+s.Rand.Intn(3)) * time.Second
	listenDelay := time.Duration(3+s.Rand.Intn(4)) * time.Second
	s.PendingFrequencyChanges = append(s.PendingFrequencyChanges,
		PendingFrequencyChange{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Time: s.State.SimTime.Add(switchDelay)})

	s.addPendingContact(PendingContact{
		ADSBCallsign:    ac.ADSBCallsign,
		TCP:             tcp,
		ReadyTime:       s.State.SimTime.Add(switchDelay + listenDelay),
		Type:            util.Select(ac.IsDeparture(), PendingTransmissionDeparture, PendingTransmissionArrival),
		FirstInFacility: s.isFirstFacilityContact(fromPos, tcp),
	})
}

// isFirstFacilityContact returns true if transitioning from fromPos to
// toTCP represents the aircraft's first contact in a TRACON facility.
// This is true when the target is a local TRACON controller and the
// source is external (ERAM center or a different TRACON).
func (s *Sim) isFirstFacilityContact(fromPos ControlPosition, toTCP TCP) bool {
	toCtrl, ok := s.State.Controllers[ControlPosition(toTCP)]
	if !ok || toCtrl.ERAMFacility {
		return false // Target is not a TRACON controller
	}

	fromCtrl, ok := s.State.Controllers[fromPos]
	if !ok {
		return true // Unknown source, assume new facility
	}

	// If the source is external (ERAM or different TRACON), this is the
	// first contact in the local TRACON facility.
	return fromCtrl.IsExternal()
}

// virtualControllerTransferComms handles the comms transfer when a handoff
// from a virtual controller is accepted. If the pilot is currently on the
// virtual's frequency, the transfer happens immediately; otherwise it is
// deferred until the pilot arrives on the virtual's frequency.
func (s *Sim) virtualControllerTransferComms(ac *Aircraft, virtualTCP TCP, targetTCP TCP) {
	if ac.ControllerFrequency == ControlPosition(virtualTCP) {
		// Pilot is on the virtual's frequency right now.
		if s.isVirtualController(targetTCP) {
			// Virtual-to-virtual: instant frequency change, then check
			// for further deferred contacts on the new position.
			ac.ControllerFrequency = ControlPosition(targetTCP)
			s.processDeferredContact(ac)
		} else {
			// Virtual-to-human: realistic switch/listen delay.
			s.enqueueControllerContact(ac, targetTCP, ControlPosition(virtualTCP))
		}
	} else {
		// Pilot hasn't reached the virtual's frequency yet. Store a
		// deferred contact so that when the pilot does arrive, we send
		// them onward to targetTCP.
		if s.DeferredContacts == nil {
			s.DeferredContacts = make(map[av.ADSBCallsign]map[ControlPosition]TCP)
		}
		if s.DeferredContacts[ac.ADSBCallsign] == nil {
			s.DeferredContacts[ac.ADSBCallsign] = make(map[ControlPosition]TCP)
		}
		s.DeferredContacts[ac.ADSBCallsign][ControlPosition(virtualTCP)] = targetTCP
	}
}

// processDeferredContact checks whether the aircraft's current frequency
// matches a deferred contact entry. If so, it triggers the transfer (instant
// for virtual targets, enqueued for human targets) and removes the entry.
func (s *Sim) processDeferredContact(ac *Aircraft) {
	if s.DeferredContacts == nil {
		return
	}
	m, ok := s.DeferredContacts[ac.ADSBCallsign]
	if !ok {
		return
	}
	targetTCP, ok := m[ac.ControllerFrequency]
	if !ok {
		return
	}
	delete(m, ac.ControllerFrequency)
	if len(m) == 0 {
		delete(s.DeferredContacts, ac.ADSBCallsign)
	}

	// Cancel the orphaned PendingContact for the virtual position that
	// directed the pilot here (it was created by contactController).
	virtualTCP := TCP(ac.ControllerFrequency)
	if pcs, ok := s.PendingContacts[virtualTCP]; ok {
		s.PendingContacts[virtualTCP] = slices.DeleteFunc(pcs,
			func(pc PendingContact) bool { return pc.ADSBCallsign == ac.ADSBCallsign })
	}

	if s.isVirtualController(targetTCP) {
		// Virtual-to-virtual: instant, then recurse.
		ac.ControllerFrequency = ControlPosition(targetTCP)
		s.processDeferredContact(ac)
	} else {
		// Virtual-to-human: realistic delay.
		s.enqueueControllerContact(ac, targetTCP, ac.ControllerFrequency)
	}
}

// enqueueDepartureContact adds a departure to the pending contacts queue.
// Departures are ready immediately (they're already on frequency).
func (s *Sim) enqueueDepartureContact(ac *Aircraft, tcp TCP) {
	ac.ControllerFrequency = ControlPosition(tcp)
	s.addPendingContact(PendingContact{
		ADSBCallsign:           ac.ADSBCallsign,
		TCP:                    tcp,
		Type:                   PendingTransmissionDeparture,
		ReportDepartureHeading: ac.ReportDepartureHeading,
		HasQueuedEmergency:     ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1,
	})
}

// enqueuePilotTransmission adds a pilot-initiated transmission to the pending queue.
func (s *Sim) enqueuePilotTransmission(callsign av.ADSBCallsign, tcp TCP, txType PendingTransmissionType) {
	s.addPendingContact(PendingContact{
		ADSBCallsign: callsign,
		TCP:          tcp,
		Type:         txType,
	})
}

// enqueueEmergencyTransmission adds an emergency transmission to the pending queue.
// Emergency transmissions have a pre-built message since they're generated at trigger time.
func (s *Sim) enqueueEmergencyTransmission(callsign av.ADSBCallsign, tcp TCP, rt *av.RadioTransmission) {
	s.addPendingContact(PendingContact{
		ADSBCallsign:         callsign,
		TCP:                  tcp,
		Type:                 PendingTransmissionEmergency,
		PrebuiltTransmission: rt,
	})
}

// cancelPendingInitialContact removes any pending Departure or Arrival contact
// for the given aircraft. Called when a controller issues a command to an
// aircraft that hasn't checked in yet, preventing stale check-ins.
// Caller must hold s.mu.
func (s *Sim) cancelPendingInitialContact(callsign av.ADSBCallsign) {
	if s.PendingContacts == nil {
		return
	}

	ac := s.Aircraft[callsign]
	tcp := TCP(ac.ControllerFrequency)

	s.PendingContacts[tcp] = slices.DeleteFunc(s.PendingContacts[tcp], func(pc PendingContact) bool {
		return pc.ADSBCallsign == callsign &&
			(pc.Type == PendingTransmissionDeparture || pc.Type == PendingTransmissionArrival)
	})
}

// GenerateContactTransmission generates a transmission for a pending contact.
// Returns the spoken and written text, or empty strings if the contact is invalid.
// This is called when the client requests a contact, using current aircraft state.
func (s *Sim) GenerateContactTransmission(pc *PendingContact) (spokenText, writtenText string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ac, ok := s.Aircraft[pc.ADSBCallsign]
	if !ok {
		return "", ""
	}

	var rt *av.RadioTransmission

	switch pc.Type {
	case PendingTransmissionDeparture:
		if !ac.IsAssociated() {
			return "", ""
		}
		rt = ac.Nav.DepartureMessage(pc.ReportDepartureHeading)

		// Handle emergency activation for departures
		humanAllocated := !s.isVirtualController(ac.ControllerFrequency)
		if humanAllocated && pc.HasQueuedEmergency {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}
		// For departures to virtual controllers, enqueue climbing to cruise
		if ac.IsDeparture() && !humanAllocated {
			s.enqueueDepartOnCourse(ac.ADSBCallsign)
		}

	case PendingTransmissionArrival:
		if !ac.IsAssociated() {
			return "", ""
		}
		rt = ac.ContactMessage(s.ReportingPoints)
		rt.Type = av.RadioTransmissionContact

		// Append ATIS information only when this is the first contact
		// in a TRACON facility (not when transferring between
		// controllers in the same facility, and not for ERAM controllers).
		if pc.FirstInFacility {
			arrivalAirport := ac.FlightPlan.ArrivalAirport
			if letter, ok := s.State.ATISLetter[arrivalAirport]; ok && letter != "" {
				if s.Rand.Float32() < 0.85 { // 85% of aircraft give the ATIS
					reportLetter := letter
					age := s.State.SimTime.Sub(s.ATISChangedTime[arrivalAirport])

					// Possible report having the previous ATIS if it has changed recently: always
					// report the last one in the first 20 seconds after a change, then linearly
					// ramp down the probability to zero 3 minutes after a change.
					p := 1 - max(0, (age.Seconds()-20)/(300-20))
					if s.Rand.Float32() < float32(p) {
						reportLetter = string(rune((letter[0]-'A'+25)%26 + 'A'))
					}
					ac.ReportedATIS = reportLetter
					rt.Add("[we have information {ch}|information {ch}|we have {ch}]", reportLetter)
				}
			}
		}

		// Handle emergency activation for arrivals
		humanAllocated := !s.isVirtualController(ac.ControllerFrequency)
		if humanAllocated && ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1 {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}

	case PendingTransmissionTrafficInSight:
		rt = av.MakeReadbackTransmission("[approach|], {callsign}, [we've got the traffic|we have the traffic in sight|traffic in sight now]",
			av.CallsignArg{Callsign: ac.ADSBCallsign})
		rt.Type = av.RadioTransmissionContact

	case PendingTransmissionFlightFollowingReq:
		rt = av.MakeContactTransmission("[VFR request|with a VFR request]")
		rt.Type = av.RadioTransmissionContact

	case PendingTransmissionFlightFollowingFull:
		rt = s.generateFlightFollowingMessage(ac)

	case PendingTransmissionGoAround:
		rt = av.MakeContactTransmission("[going around|on the go], ")
		targetAlt, _ := ac.Nav.TargetAltitude()
		currentAlt := ac.Altitude()
		if currentAlt < targetAlt {
			rt.Add("[at|] {alt} [climbing|for] {alt}, ", currentAlt, targetAlt)
		} else {
			rt.Add("[at|] {alt}, ", currentAlt)
		}
		if ac.GoAroundOnRunwayHeading {
			rt.Add("[runway heading|on a runway heading]")
		} else if ac.Nav.Heading.Assigned != nil {
			rt.Add("heading {hdg}", int(*ac.Nav.Heading.Assigned+0.5))
		}
		if ac.SentAroundForSpacing {
			rt.Add(", [tower sent us around for spacing|we were sent around for spacing]")
			ac.SentAroundForSpacing = false
		}
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionRequestApproachClearance:
		rt = av.MakeContactTransmission("[are we cleared for the approach|looking for the approach|we're going to need the approach here shortly]")
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionEmergency:
		if pc.PrebuiltTransmission == nil {
			return "", ""
		}
		rt = pc.PrebuiltTransmission
		rt.Type = av.RadioTransmissionUnexpected // Mark as urgent for display

	case PendingTransmissionRequestVisual:
		runway := ""
		if ac.Nav.Approach.Assigned != nil {
			runway = ac.Nav.Approach.Assigned.Runway
		}

		// Pilot just reports field in sight and requests "the visual" —
		// it's the controller's decision whether to clear a plain visual
		// (CV) or a charted visual procedure (C).
		rt = av.MakeReadbackTransmission(
			"[field in sight|we have the airport in sight], [requesting the visual|can we get the visual] [approach |]runway {rwy}",
			runway)
		rt.Type = av.RadioTransmissionContact

	default:
		return "", ""
	}

	if rt == nil {
		return "", ""
	}

	// Get the base (unprefixed) text for the event stream.
	// prepareRadioTransmissions will add the prefix when delivering to clients.
	baseSpoken := rt.Spoken(s.Rand)
	baseWritten := rt.Written(s.Rand)

	// Post the radio event with unprefixed text
	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          pc.ADSBCallsign,
		ToController:          pc.TCP,
		DestinationTCW:        s.State.TCWForPosition(pc.TCP),
		WrittenText:           baseWritten,
		SpokenText:            baseSpoken,
		RadioTransmissionType: rt.Type,
	})

	// Generate prefixed text for the TTS return value (not going through event stream)
	ctrl := s.State.Controllers[pc.TCP]
	if ctrl == nil {
		return baseSpoken, baseWritten
	}

	var heavySuper string
	if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok && !ctrl.ERAMFacility {
		if perf.WeightClass == "H" {
			heavySuper = " heavy"
		} else if perf.WeightClass == "J" {
			heavySuper = " super"
		}
	}

	// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super
	if ac.EmergencyState != nil && s.Rand.Bool() {
		heavySuper += " emergency aircraft"
	}

	csArg := av.CallsignArg{
		Callsign:           ac.ADSBCallsign,
		IsEmergency:        ac.EmergencyState != nil,
		AlwaysFullCallsign: true,
	}

	var prefix *av.RadioTransmission
	if ac.TypeOfFlight == av.FlightTypeDeparture {
		prefix = av.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper+". ", ctrl, csArg)
	} else {
		prefix = av.MakeContactTransmission("{actrl}, {callsign}"+heavySuper+". ", ctrl, csArg)
	}

	spokenText = prefix.Spoken(s.Rand) + baseSpoken
	writtenText = prefix.Written(s.Rand) + baseWritten
	return spokenText, writtenText
}

type FutureOnCourse struct {
	ADSBCallsign av.ADSBCallsign
	Time         time.Time
}

func (s *Sim) enqueueDepartOnCourse(callsign av.ADSBCallsign) {
	wait := time.Duration(10+s.Rand.Intn(15)) * time.Second
	s.FutureOnCourse = append(s.FutureOnCourse,
		FutureOnCourse{ADSBCallsign: callsign, Time: s.State.SimTime.Add(wait)})
}

type FutureChangeSquawk struct {
	ADSBCallsign av.ADSBCallsign
	Code         av.Squawk
	Mode         av.TransponderMode
	Time         time.Time
}

func (s *Sim) enqueueTransponderChange(callsign av.ADSBCallsign, code av.Squawk, mode av.TransponderMode) {
	wait := time.Duration(5+s.Rand.Intn(5)) * time.Second
	s.FutureSquawkChanges = append(s.FutureSquawkChanges,
		FutureChangeSquawk{ADSBCallsign: callsign, Code: code, Mode: mode, Time: s.State.SimTime.Add(wait)})
}

func (s *Sim) processFutureEvents() {
	s.FutureOnCourse = util.FilterSliceInPlace(s.FutureOnCourse,
		func(oc FutureOnCourse) bool {
			if s.State.SimTime.After(oc.Time) {
				if ac, ok := s.Aircraft[oc.ADSBCallsign]; ok {
					s.lg.Info("departing on course", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
						slog.Int("final_altitude", ac.FlightPlan.Altitude))
					// Clear temporary altitude
					if ac.NASFlightPlan != nil {
						ac.NASFlightPlan.InterimAlt = 0
						ac.NASFlightPlan.InterimType = 0
					}
					ac.DepartOnCourse(s.State.SimTime, s.lg)
				}
				return false
			}
			return true
		})

	s.FutureSquawkChanges = util.FilterSliceInPlace(s.FutureSquawkChanges,
		func(fcs FutureChangeSquawk) bool {
			if s.State.SimTime.After(fcs.Time) {
				if ac, ok := s.Aircraft[fcs.ADSBCallsign]; ok {
					ac.Squawk = fcs.Code
					ac.Mode = fcs.Mode
				}
				return false
			}
			return true
		})
}

///////////////////////////////////////////////////////////////////////////
// Aircraft command dispatch

var ErrInvalidCommandSyntax = fmt.Errorf("invalid command syntax")

type ControlCommandsResult struct {
	RemainingInput     string
	Error              error
	ReadbackSpokenText string          // Spoken text for TTS synthesis (if readback was generated)
	ReadbackCallsign   av.ADSBCallsign // Aircraft callsign for the readback
}

// RunAircraftControlCommands executes a space-separated string of control commands for an aircraft.
// Returns the remaining unparsed input and any error that occurred.
// This is the core command execution logic shared by the dispatcher and automated test code.
// All intents from commands are collected and rendered together as a single transmission.
func (s *Sim) RunAircraftControlCommands(tcw TCW, callsign av.ADSBCallsign, commandStr string) ControlCommandsResult {
	commands := strings.Fields(commandStr)

	// Parse addressing form suffix from callsign: /T indicates type+trailing3 addressing
	// (e.g., "skyhawk 3 alpha bravo" instead of "november 1 2 3 alpha bravo")
	addressingForm := AddressingFormFull
	if strings.HasSuffix(string(callsign), "/T") {
		addressingForm = AddressingFormTypeTrailing3
		callsign = av.ADSBCallsign(strings.TrimSuffix(string(callsign), "/T"))
	}

	// Update aircraft's last addressing form for readback rendering
	if ac, ok := s.Aircraft[callsign]; ok {
		ac.LastAddressingForm = addressingForm
	}

	// Handle ROLLBACK command first (may have other commands following)
	// ROLLBACK undoes the last command sent to a wrong aircraft due to STT callsign error
	if len(commands) > 0 && commands[0] == "ROLLBACK" {
		if err := s.rollbackLastCommand(); err != nil {
			// Log but don't return error - ROLLBACK is generated by STT, not the user,
			// so a failure indicates a bug in STT (not a user concern)
			s.lg.Warnf("ROLLBACK failed: %v", err)
		}
		commands = commands[1:]
		if len(commands) == 0 {
			return ControlCommandsResult{}
		}
	}

	// Handle special STT commands that need direct TTS synthesis
	// These short-circuit normal command processing
	if len(commands) == 1 {
		switch commands[0] {
		case "AGAIN":
			cs, spokenText, err := s.SayAgain(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		case "NOTCLEARED":
			cs, spokenText, err := s.SayNotCleared(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		}
	}

	// Take a snapshot before executing commands (for potential future rollback)
	if ac, ok := s.Aircraft[callsign]; ok {
		s.LastSTTCommand = &LastSTTCommand{
			Callsign:    callsign,
			NavSnapshot: ac.Nav.TakeSnapshot(),
		}
	}

	var intents []av.CommandIntent

	for i, command := range commands {
		intent, err := s.runOneControlCommand(tcw, callsign, command)
		if err != nil {
			// Post any collected intents before returning error
			spokenText := s.renderAndPostReadback(callsign, tcw, intents)
			return ControlCommandsResult{
				RemainingInput:     strings.Join(commands[i:], " "),
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   callsign,
			}
		}
		if intent != nil {
			intents = append(intents, intent)
		}
	}

	// Render all intents together as a single transmission
	spokenText := s.renderAndPostReadback(callsign, tcw, intents)
	return ControlCommandsResult{
		ReadbackSpokenText: spokenText,
		ReadbackCallsign:   callsign,
	}
}

// rollbackLastCommand restores the nav state of the last aircraft that received a command.
// This is used when the controller says "negative, that was for {other callsign}" to undo
// commands given to the wrong aircraft due to STT callsign misinterpretation.
func (s *Sim) rollbackLastCommand() error {
	if s.LastSTTCommand == nil {
		return ErrNoRecentCommand
	}

	ac, ok := s.Aircraft[s.LastSTTCommand.Callsign]
	if !ok {
		s.LastSTTCommand = nil
		return ErrNoRecentCommand
	}

	// Restore the nav state from the snapshot
	ac.Nav.RestoreSnapshot(s.LastSTTCommand.NavSnapshot)

	// Clear the snapshot - consecutive rollbacks should fail
	s.LastSTTCommand = nil

	return nil
}

// renderAndPostReadback renders a batch of command intents as a pilot readback transmission.
// The tcw ensures the readback goes to the controller who issued the command,
// regardless of any consolidation changes.
// Returns the spoken text for TTS synthesis, including the callsign suffix.
func (s *Sim) renderAndPostReadback(callsign av.ADSBCallsign, tcw TCW, intents []av.CommandIntent) string {
	if rt := av.RenderIntents(intents, s.Rand); rt != nil {
		s.postReadbackTransmission(callsign, *rt, tcw)
		// MixUp transmissions already include the callsign in the message
		if rt.Type == av.RadioTransmissionMixUp {
			return rt.Spoken(s.Rand)
		}
		// Return spoken text with callsign suffix for TTS synthesis
		return rt.Spoken(s.Rand) + s.readbackCallsignSuffix(callsign, tcw)
	}
	return ""
}

// readbackCallsignSuffix generates the ", [callsign] [heavy/super]." suffix for readbacks.
// This is used both for synchronous TTS and matches what prepareRadioTransmissions does for events.
func (s *Sim) readbackCallsignSuffix(callsign av.ADSBCallsign, tcw TCW) string {
	ac, ok := s.Aircraft[callsign]
	if !ok {
		return ""
	}

	primaryTCP := s.State.PrimaryPositionForTCW(tcw)
	ctrl := s.State.Controllers[primaryTCP]

	var heavySuper string
	if ctrl != nil && !ctrl.ERAMFacility {
		if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok {
			if perf.WeightClass == "H" {
				heavySuper = " heavy"
			} else if perf.WeightClass == "J" {
				heavySuper = " super"
			}
		}
	}

	// Use GACallsignArg for GA aircraft when addressed with type+trailing3 form
	var csArg any
	if strings.HasPrefix(string(callsign), "N") && ac.LastAddressingForm == AddressingFormTypeTrailing3 {
		csArg = av.GACallsignArg{
			Callsign:     ac.ADSBCallsign,
			AircraftType: ac.FlightPlan.AircraftType,
			UseTypeForm:  true,
			IsEmergency:  ac.EmergencyState != nil,
		}
	} else {
		csArg = av.CallsignArg{
			Callsign:    ac.ADSBCallsign,
			IsEmergency: ac.EmergencyState != nil,
		}
	}
	tr := av.MakeReadbackTransmission(", {callsign}"+heavySuper+". ", csArg)
	return tr.Spoken(s.Rand)
}

// parseHold parses a hold command string in the format "FIX/[option]/[option]"
// and returns the fix name and a controller-specified hold if options are present.
// Returns (fixName, nil, true) if no options are specified (use published hold).
// Returns (fixName, *Hold, true) if options are successfully parsed.
// Returns ("", nil, false) if parsing fails.
//
// Options may be:
// - L: left turns
// - R: right turns
// - xxNM: xx nautical mile legs
// - xxM: xx minute legs
// - Rxxx: inbound course on the xxx radial to the fix
//
// If options are specified, the Rxxx radial option is required.
// Multiple options of the same type result in an error.
// parseSpeedUntil parses the "until" specification from a speed command.
// Formats:
//   - "ROSLY" -> fix name
//   - "5DME"  -> 5 DME
//   - "6"     -> 6 mile final
func parseSpeedUntil(untilStr string) *av.SpeedUntil {
	untilStr = strings.ToUpper(untilStr)

	// Check for DME pattern: digits followed by DME
	if strings.HasSuffix(untilStr, "DME") {
		numStr := strings.TrimSuffix(untilStr, "DME")
		if n, err := strconv.Atoi(numStr); err == nil && n > 0 {
			return &av.SpeedUntil{DME: n}
		}
	}

	// Check for pure number (mile final)
	if n, err := strconv.Atoi(untilStr); err == nil && n > 0 {
		return &av.SpeedUntil{MileFinal: n}
	}

	// Otherwise it's a fix name
	return &av.SpeedUntil{Fix: untilStr}
}

func parseHold(command string) (string, *av.Hold, bool) {
	fix, opts, ok := strings.Cut(command, "/")
	fix = strings.ToUpper(fix)
	if !ok {
		// No options, use published hold
		return fix, nil, true
	}

	// Controller-specified hold with defaults
	hold := av.Hold{Fix: fix}
	directionSet := false

	for opt := range strings.SplitSeq(opts, "/") {
		opt = strings.ToUpper(opt)

		switch {
		case opt == "L":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnLeft
			directionSet = true

		case opt == "R":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnRight
			directionSet = true

		case strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			dist, err := strconv.ParseFloat(strings.TrimSuffix(opt, "NM"), 32)
			if err != nil || dist <= 0 {
				return "", nil, false
			}
			hold.LegLengthNM = float32(dist)

		case strings.HasSuffix(opt, "M") && !strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			time, err := strconv.ParseFloat(strings.TrimSuffix(opt, "M"), 32)
			if err != nil || time <= 0 {
				return "", nil, false
			}
			hold.LegMinutes = float32(time)

		case strings.HasPrefix(opt, "R") && len(opt) > 1:
			if hold.InboundCourse != 0 {
				return "", nil, false
			}
			radial, err := strconv.Atoi(opt[1:])
			if err != nil || radial <= 0 || radial > 360 {
				return "", nil, false
			}
			hold.InboundCourse = float32(radial)

		default:
			return "", nil, false
		}
	}

	// Radial is required for controller-specified holds
	if hold.InboundCourse == 0 {
		return "", nil, false
	}
	if !directionSet {
		hold.TurnDirection = av.TurnRight
	}
	if hold.LegMinutes == 0 && hold.LegLengthNM == 0 {
		hold.LegMinutes = 1
	}

	return fix, &hold, true
}

// runOneControlCommand executes a single control command for an aircraft.
// Returns the intent generated by the command (if any) for batching.
func (s *Sim) runOneControlCommand(tcw TCW, callsign av.ADSBCallsign, command string) (av.CommandIntent, error) {
	if len(command) == 0 {
		return nil, ErrInvalidCommandSyntax
	}

	// A###, C###, and D### all equivalently assign an altitude
	if (command[0] == 'A' || command[0] == 'C' || command[0] == 'D') && len(command) > 1 && util.IsAllNumbers(command[1:]) {
		alt, err := strconv.Atoi(command[1:])
		if err != nil {
			return nil, err
		}
		if alt > 600 && (alt%100 == 0) {
			// Sometimes STT transcript interpretation forgets altitudes are in 100s...
			alt /= 100
		}
		return s.AssignAltitude(tcw, callsign, 100*alt, false)
	}

	switch command[0] {
	case 'A':
		if command == "A" {
			return s.AltitudeOurDiscretion(tcw, callsign)
		} else if command == "AGAIN" {
			// AGAIN is handled specially in RunAircraftControlCommands for TTS synthesis
			return nil, nil
		} else if strings.HasPrefix(command, "ATIS/") {
			return s.ATISCommand(tcw, callsign, command[5:])
		} else {
			components := strings.Split(command, "/")
			if len(components) != 2 || len(components[1]) == 0 {
				return nil, ErrInvalidCommandSyntax
			}

			fix := strings.ToUpper(components[0][1:])
			switch components[1][0] {
			case 'C':
				approach := components[1][1:]
				return s.AtFixCleared(tcw, callsign, fix, approach)
			case 'I':
				return s.AtFixIntercept(tcw, callsign, fix)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		}

	case 'C':
		if command == "CAC" {
			return s.CancelApproachClearance(tcw, callsign)
		} else if command == "CVS" {
			return s.ClimbViaSID(tcw, callsign)
		} else if strings.HasPrefix(command, "CV") && command != "CVS" && len(command) > 2 {
			return s.ClearedVisualApproach(tcw, callsign, command[2:])
		} else if command == "CSI" || (strings.HasPrefix(command, "CSI") && !util.IsAllNumbers(command[3:])) {
			return s.ClearedApproach(tcw, callsign, command[3:], true)
		} else if components := strings.Split(command, "/"); len(components) > 1 {
			fix := components[0][1:]
			var ar *av.AltitudeRestriction
			speed := 0
			mach := float32(0)
			for _, cmd := range components[1:] {
				if len(cmd) == 0 {
					return nil, ErrInvalidCommandSyntax
				}

				var err error
				if cmd[0] == 'A' && len(cmd) > 1 {
					if ar, err = av.ParseAltitudeRestriction(cmd[1:]); err != nil {
						return nil, err
					}
					ar.Range[0] *= 100
					ar.Range[1] *= 100
				} else if cmd[0] == 'S' {
					speedStr := cmd[1:]
					// Strip +/- suffix for now (treat as regular speed)
					speedStr = strings.TrimSuffix(speedStr, "+")
					speedStr = strings.TrimSuffix(speedStr, "-")
					if speed, err = strconv.Atoi(speedStr); err != nil {
						return nil, err
					}
				} else if cmd[0] == 'M' {
					machStr := cmd[1:]
					machStr = strings.TrimSuffix(machStr, "+")
					machStr = strings.TrimSuffix(machStr, "-")
					mach64, err := strconv.ParseFloat(machStr, 32)
					if err != nil {
						return nil, err
					}
					if mach64 >= 1 {
						mach64 /= 100
					}
					mach = float32(mach64)
				} else {
					return nil, ErrInvalidCommandSyntax
				}
			}

			return s.CrossFixAt(tcw, callsign, fix, ar, speed, mach)
		} else if strings.HasPrefix(command, "CT") && len(command) > 2 {
			// Only treat as contact command if the TCP exists as a valid controller;
			// otherwise treat as cleared approach (e.g., "CTTL" -> cleared for TTL approach)
			tcp := TCP(command[2:])
			if _, ok := s.State.Controllers[tcp]; ok {
				return s.ContactController(tcw, ACID(callsign), tcp)
			}
			return s.ClearedApproach(tcw, callsign, command[1:], false)
		} else {
			return s.ClearedApproach(tcw, callsign, command[1:], false)
		}

	case 'D':
		if command == "DVS" {
			return s.DescendViaSTAR(tcw, callsign)
		} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
			fix := components[0][1:]

			switch components[1][0] {
			case 'D':
				return s.DepartFixDirect(tcw, callsign, fix, components[1][1:])
			case 'H':
				hdg, err := strconv.Atoi(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.DepartFixHeading(tcw, callsign, fix, hdg)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else if len(command) >= 4 && len(command) <= 6 {
			return s.DirectFix(tcw, callsign, command[1:])
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'E':
		if command == "ED" {
			return s.ExpediteDescent(tcw, callsign)
		} else if command == "EC" {
			return s.ExpediteClimb(tcw, callsign)
		} else if len(command) > 1 {
			// Parse: "EI22L/LAHSO26" -> approach="I22L", lahsoRunway="26"
			components := strings.Split(command[1:], "/")
			approach := components[0]
			var lahsoRunway string
			if len(components) > 1 && strings.HasPrefix(components[1], "LAHSO") {
				lahsoRunway = components[1][5:] // Extract runway after "LAHSO"
			}
			return s.ExpectApproach(tcw, callsign, approach, lahsoRunway)
		} else if command == "E" {
			// Bare "E" re-issues expect for the already-assigned approach
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.AssignedId != "" {
				return s.ExpectApproach(tcw, callsign, ac.Nav.Approach.AssignedId, "")
			}
			return av.MakeUnableIntent("unable. We haven't been told to expect an approach"), nil
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'F':
		if command == "FC" {
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.Cleared {
				// STT sometimes gets confused and gives FC for "contact tower" instructions, so
				// we'll just roll with that.
				return s.ContactTower(tcw, callsign)
			} else {
				return s.ContactTrackingController(tcw, ACID(callsign))
			}
		} else if command == "FS" {
			return s.SayFieldInSight(tcw, callsign)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'G':
		if command == "GA" {
			if err := s.GoAhead(tcw, callsign); err != nil {
				return nil, err
			}
			return nil, nil // GoAhead returns no intent
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'H':
		if len(command) == 1 {
			// Present heading
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				Present:      true,
			})
		} else if hdg, err := strconv.Atoi(command[1:]); err == nil {
			// Fly heading xxx
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         av.TurnClosest,
			})
		} else {
			// Hold at fix (published or controller-specified)
			if fix, hold, ok := parseHold(command[1:]); !ok {
				return nil, ErrInvalidCommandSyntax
			} else {
				return s.HoldAtFix(tcw, callsign, fix, hold)
			}
		}

	case 'I':
		if len(command) == 1 {
			return s.InterceptLocalizer(tcw, callsign)
		} else if command == "ID" {
			return s.Ident(tcw, callsign)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'L':
		if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				LeftDegrees:  deg,
			})
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         av.TurnLeft,
			})
		}
	case 'M': // mach speed
		// M78 for mach 0.78
		// + and - operators work here as well
		if len(command) != 3 {
			return nil, ErrInvalidCommandSyntax
		}

		machStr := command[1:]
		mach, err := strconv.ParseFloat(machStr, 32)
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		mach /= 100.0

		return s.AssignMach(tcw, callsign, float32(mach), false)

	case 'R':
		if command == "RON" {
			return s.ResumeOwnNavigation(tcw, callsign)
		} else if command == "RST" {
			return s.RadarServicesTerminated(tcw, callsign)
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				RightDegrees: deg,
			})
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:          tcw,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         av.TurnRight,
			})
		}

	case 'S':
		if len(command) == 1 {
			return s.AssignSpeed(tcw, callsign, 0, false)
		} else if command == "SPRES" {
			return s.MaintainPresentSpeed(tcw, callsign)
		} else if command == "SMIN" {
			return s.MaintainSlowestPractical(tcw, callsign)
		} else if command == "SMAX" {
			return s.MaintainMaximumForward(tcw, callsign)
		} else if command == "SS" {
			return s.SaySpeed(tcw, callsign)
		} else if command == "SI" {
			return s.SayIndicatedSpeed(tcw, callsign)
		} else if command == "SM" {
			return s.SayMach(tcw, callsign)
		} else if strings.HasSuffix(command, "+") {
			// Speed floor: S180+
			kts, err := strconv.Atoi(command[1 : len(command)-1])
			if err != nil {
				return nil, err
			}
			// For now treat as regular speed assignment
			return s.AssignSpeed(tcw, callsign, kts, false)
		} else if strings.HasSuffix(command, "-") {
			// Speed ceiling: S180-
			kts, err := strconv.Atoi(command[1 : len(command)-1])
			if err != nil {
				return nil, err
			}
			// For now treat as regular speed assignment
			return s.AssignSpeed(tcw, callsign, kts, false)
		} else if command == "SQS" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeStandby)
		} else if command == "SQA" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeAltitude)
		} else if command == "SQON" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeOn)
		} else if len(command) == 6 && command[:2] == "SQ" {
			sq, err := av.ParseSquawk(command[2:])
			if err != nil {
				return nil, err
			}
			return s.ChangeSquawk(tcw, callsign, sq)
		} else if command == "SH" {
			return s.SayHeading(tcw, callsign)
		} else if command == "SA" {
			return s.SayAltitude(tcw, callsign)
		} else if strings.HasPrefix(command, "SAYAGAIN/") {
			return s.SayAgainCommand(tcw, callsign, command[9:])
		} else if idx := strings.Index(command, "/U"); idx > 0 {
			// Speed until specification: S180/UROSLY, S180/U5DME, S180/U6
			speedStr := command[1:idx]
			untilStr := command[idx+2:] // after "/U"
			kts, err := strconv.Atoi(speedStr)
			if err != nil {
				return nil, err
			}
			until := parseSpeedUntil(untilStr)
			return s.AssignSpeedUntil(tcw, callsign, kts, until)
		} else {
			kts, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignSpeed(tcw, callsign, kts, false)
		}

	case 'T':
		if strings.HasPrefix(command, "TRAFFIC/") {
			return s.TrafficAdvisory(tcw, callsign, command)
		} else if command == "TO" {
			return s.ContactTower(tcw, callsign)
		} else if n := len(command); n > 2 {
			if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
				if command[n-1] == 'L' {
					return s.AssignHeading(&HeadingArgs{
						TCW:          tcw,
						ADSBCallsign: callsign,
						LeftDegrees:  deg,
					})
				} else if command[n-1] == 'R' {
					return s.AssignHeading(&HeadingArgs{
						TCW:          tcw,
						ADSBCallsign: callsign,
						RightDegrees: deg,
					})
				}
			}

			switch command[:2] {
			case "TS":
				kts, err := strconv.Atoi(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignSpeed(tcw, callsign, kts, true)
			case "TM":
				mach, err := strconv.ParseFloat(command[2:], 32)
				if err != nil {
					return nil, err
				}
				mach /= 100.0
				return s.AssignMach(tcw, callsign, float32(mach), true)
			case "TA", "TC", "TD":
				alt, err := strconv.Atoi(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignAltitude(tcw, callsign, 100*alt, true)

			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'V':
		if command == "VISSEP" {
			return s.MaintainVisualSeparation(tcw, callsign)
		}
		return nil, ErrInvalidCommandSyntax

	case 'X':
		s.DeleteAircraft(tcw, callsign)
		return nil, nil // DeleteAircraft returns no intent

	default:
		return nil, ErrInvalidCommandSyntax
	}
}

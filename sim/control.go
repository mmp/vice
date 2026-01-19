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
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
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
	return s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			if !s.TCWCanCommandAircraft(tcw, ac) {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
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
				s.deleteFlightPlan(ac.DisassociateFlightPlan())
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
			return s.contactController(s.State.PrimaryPositionForTCW(tcw), sfp, ac, toTCP)
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

	s.eventStream.Post(Event{
		Type:           HandoffControlEvent,
		FromController: ac.ControllerFrequency,
		ToController:   toTCP,
		ACID:           sfp.ACID,
	})

	// Take away the current controller's ability to issue control
	// commands.
	ac.ControllerFrequency = ""

	// In 5-10 seconds, have the aircraft contact the new controller
	// (and give them control only then).
	wait := time.Duration(5+s.Rand.Intn(10)) * time.Second
	s.enqueueControllerContact(ac.ADSBCallsign, toTCP, wait)

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
					func(wp av.Waypoint) bool { return wp.TransferComms })
				if !haveTransferComms && s.isVirtualController(previousTrackingController) {
					// For a handoff from a virtual controller, cue up a delayed
					// contact message unless there's a point later in the route when
					// comms are to be transferred.
					wait := time.Duration(5+s.Rand.Intn(10)) * time.Second
					s.enqueueControllerContact(ac.ADSBCallsign, newTrackingController, wait)
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
			if len(fp.PointOutHistory) < 20 {
				fp.PointOutHistory = append([]TCP{po.ToController}, fp.PointOutHistory...)
			} else {
				fp.PointOutHistory = fp.PointOutHistory[:19]
				fp.PointOutHistory = append([]TCP{po.ToController}, fp.PointOutHistory...)
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
	Turn         nav.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(hdg.TCW, hdg.ADSBCallsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			if hdg.Present {
				return ac.FlyPresentHeading()
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
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

func (s *Sim) SaySpeed(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.SaySpeed()
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

func (s *Sim) DirectFix(tcw TCW, callsign av.ADSBCallsign, fix string) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DirectFix(fix)
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

func (s *Sim) CrossFixAt(tcw TCW, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, speed int) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.CrossFixAt(fix, ar, speed)
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
			return ac.ExpectApproach(approach, ap, s.lg)
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
				intent, ok = ac.ClearedStraightInApproach(approach, s.lg)
			} else {
				intent, ok = ac.ClearedApproach(approach, s.lg)
			}

			if ok {
				ac.ApproachTCP = TCP(ac.ControllerFrequency)
			}
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
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(tcw TCW, callsign av.ADSBCallsign) (av.CommandIntent, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.GoAround()
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

			s.sendFullFlightFollowingRequest(ac, s.State.PrimaryPositionForTCW(tcw))

			return nil
		})
	return err
}

// SayBlocked triggers a random pilot saying "blocked" in response to an unclear transmission.
// Returns the callsign selected (for voice assignment) and the spoken text for TTS synthesis.
func (s *Sim) SayBlocked(tcw TCW) (av.ADSBCallsign, string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Have a random pilot speak up
	callsign, ok := rand.SampleWeightedSeq(s.Rand, maps.Keys(s.Aircraft), func(cs av.ADSBCallsign) int {
		ac := s.Aircraft[cs]
		return util.Select(s.TCWCanCommandAircraft(tcw, ac), 1, 0)
	})
	if !ok {
		// No aircraft available to speak
		return "", "", nil
	}

	tr := av.MakeNoIdTransmission("blocked")
	s.postReadbackTransmission(callsign, *tr, tcw)

	// Return spoken text for TTS synthesis (no callsign suffix for "blocked")
	spokenText := tr.Spoken(s.Rand)
	return callsign, spokenText, nil
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

///////////////////////////////////////////////////////////////////////////
// Deferred operations

type FutureControllerContact struct {
	ADSBCallsign av.ADSBCallsign
	TCP          TCP
	Time         time.Time
}

func (s *Sim) enqueueControllerContact(callsign av.ADSBCallsign, tcp TCP, wait time.Duration) {
	s.FutureControllerContacts = append(s.FutureControllerContacts,
		FutureControllerContact{ADSBCallsign: callsign, TCP: tcp, Time: s.State.SimTime.Add(wait)})
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

func (s *Sim) processEnqueued() {
	s.FutureControllerContacts = util.FilterSliceInPlace(s.FutureControllerContacts,
		func(c FutureControllerContact) bool {
			if !s.State.SimTime.After(c.Time) {
				return true // keep it in the slice
			}

			if ac, ok := s.Aircraft[c.ADSBCallsign]; ok {
				if ac.IsAssociated() {
					ac.ControllerFrequency = ControlPosition(c.TCP)

					rt := ac.ContactMessage(s.ReportingPoints)
					rt.Type = av.RadioTransmissionContact

					s.postContactTransmission(c.ADSBCallsign, c.TCP, *rt)

					// Activate pre-assigned external emergency; the transmission will be
					// consolidated with the initial contact transmission.
					// Check if controlling controller is a human-allocated position (not virtual)
					humanAllocated := !s.isVirtualController(ac.ControllerFrequency)
					if humanAllocated && ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1 {
						ac.EmergencyState.CurrentStage = 0
						s.runEmergencyStage(ac)
					}

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					if ac.IsDeparture() && !humanAllocated {
						s.enqueueDepartOnCourse(ac.ADSBCallsign)
					}
				} else {
					if ac.RequestedFlightFollowing {
						s.requestFlightFollowing(ac, c.TCP)
					}
				}
			}
			return false // remove it from the slice
		})

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
					ac.DepartOnCourse(s.lg)
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
		case "BLOCKED":
			cs, spokenText, err := s.SayBlocked(tcw)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
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

// renderAndPostReadback renders a batch of command intents as a pilot readback transmission.
// The tcw ensures the readback goes to the controller who issued the command,
// regardless of any consolidation changes.
// Returns the spoken text for TTS synthesis, including the callsign suffix.
func (s *Sim) renderAndPostReadback(callsign av.ADSBCallsign, tcw TCW, intents []av.CommandIntent) string {
	if rt := av.RenderIntents(intents, s.Rand); rt != nil {
		s.postReadbackTransmission(callsign, *rt, tcw)
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

	// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super.
	if ac.EmergencyState != nil && s.Rand.Bool() {
		heavySuper += " emergency aircraft"
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
		} else {
			components := strings.Split(command, "/")
			if len(components) != 2 || len(components[1]) == 0 || components[1][0] != 'C' {
				return nil, ErrInvalidCommandSyntax
			}

			fix := strings.ToUpper(components[0][1:])
			approach := components[1][1:]
			return s.AtFixCleared(tcw, callsign, fix, approach)
		}

	case 'B':
		if command == "BLOCKED" {
			// BLOCKED is handled specially in RunAircraftControlCommands for TTS synthesis
			return nil, nil
		}
		return nil, ErrInvalidCommandSyntax

	case 'C':
		if command == "CAC" {
			return s.CancelApproachClearance(tcw, callsign)
		} else if command == "CVS" {
			return s.ClimbViaSID(tcw, callsign)
		} else if len(command) > 4 && command[:3] == "CSI" && !util.IsAllNumbers(command[3:]) {
			return s.ClearedApproach(tcw, callsign, command[3:], true)
		} else if components := strings.Split(command, "/"); len(components) > 1 {
			fix := components[0][1:]
			var ar *av.AltitudeRestriction
			speed := 0

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
					if speed, err = strconv.Atoi(cmd[1:]); err != nil {
						return nil, err
					}
				} else {
					return nil, ErrInvalidCommandSyntax
				}
			}

			return s.CrossFixAt(tcw, callsign, fix, ar, speed)
		} else if strings.HasPrefix(command, "CT") && len(command) > 2 {
			return s.ContactController(tcw, ACID(callsign), TCP(command[2:]))
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
			return s.ExpectApproach(tcw, callsign, command[1:])
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
				Turn:         nav.TurnClosest,
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
				Turn:         nav.TurnLeft,
			})
		}

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
				Turn:         nav.TurnRight,
			})
		}

	case 'S':
		if len(command) == 1 {
			return s.AssignSpeed(tcw, callsign, 0, false)
		} else if command == "SMIN" {
			return s.MaintainSlowestPractical(tcw, callsign)
		} else if command == "SMAX" {
			return s.MaintainMaximumForward(tcw, callsign)
		} else if command == "SS" {
			return s.SaySpeed(tcw, callsign)
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
		} else {
			kts, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignSpeed(tcw, callsign, kts, false)
		}

	case 'T':
		if command == "TO" {
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

	case 'X':
		s.DeleteAircraft(tcw, callsign)
		return nil, nil // DeleteAircraft returns no intent

	default:
		return nil, ErrInvalidCommandSyntax
	}
}

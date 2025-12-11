// pkg/sim/control.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
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
	"github.com/mmp/vice/util"
)

func setTransmissionController(tcp string, rt *av.RadioTransmission) *av.RadioTransmission {
	if rt != nil {
		rt.Controller = tcp
	}
	return rt
}

func (s *Sim) isInstructorOrRPO(tcp string) bool {
	// Check if they're marked as an instructor in the Instructors map (for regular controllers with instructor privileges)
	if s.Instructors[tcp] {
		return true
	}
	// Also check if they're signed in as a dedicated instructor/RPO position
	ctrl, ok := s.State.Controllers[tcp]
	return ok && (ctrl.Instructor || ctrl.RPO)
}

func (s *Sim) dispatchAircraftCommand(tcp string, callsign av.ADSBCallsign,
	check func(tcp string, ac *Aircraft) error,
	cmd func(tcp string, ac *Aircraft) *av.RadioTransmission) error {
	s.lastControlCommandTime = time.Now()

	if ac, ok := s.Aircraft[callsign]; !ok {
		return av.ErrNoAircraftForCallsign
	} else if _, ok := s.humanControllers[tcp]; !ok {
		return ErrUnknownController
	} else {
		if check != nil {
			if err := check(tcp, ac); err != nil {
				return err
			}
		}

		preAc := *ac
		radioTransmission := cmd(tcp, ac)

		s.lg.Info("dispatch_command", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
			slog.Any("radio_transmission", radioTransmission))

		if radioTransmission != nil {
			s.postRadioEvent(ac.ADSBCallsign, tcp, *radioTransmission)
		}

		return nil
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControlledAircraftCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) *av.RadioTransmission) error {
	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if s.isInstructorOrRPO(tcp) {
				return nil
			} else if ac.IsUnassociated() {
				if ac.PreArrivalDropController == tcp {
					// Still allow commands to arrivals on short final.
					return nil
				}
				return ErrTrackIsNotActive
			} else if ac.NASFlightPlan.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Can issue both to aircraft we track but also unassociated VFRs
func (s *Sim) dispatchVFRAircraftCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) *av.RadioTransmission) error {
	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if s.isInstructorOrRPO(tcp) {
				return nil
			}
			// Allow issuing this command to random unassociated VFRs but
			// not IFRs that other controllers already own.
			if ac.IsAssociated() && ac.NASFlightPlan.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Note that ac may be nil, but flight plan will not be!
func (s *Sim) dispatchFlightPlanCommand(tcp string, acid ACID,
	check func(tcp string, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission) error {
	s.lastControlCommandTime = time.Now()

	fp, ac, _ := s.GetFlightPlanForACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	// ac may or may not be nil; we'll pass it along if we have it

	if _, ok := s.humanControllers[tcp]; !ok {
		return ErrUnknownController
	}

	if check != nil {
		if err := check(tcp, fp, ac); err != nil {
			return err
		}
	}

	preFp := *fp
	radioTransmission := cmd(tcp, fp, ac)

	s.lg.Info("dispatch_fp_command", slog.String("acid", string(fp.ACID)),
		slog.Any("prepost_fp", []NASFlightPlan{preFp, *fp}),
		slog.Any("radio_transmission", radioTransmission))

	if radioTransmission != nil {
		s.postRadioEvent(av.ADSBCallsign(fp.ACID), tcp, *radioTransmission)
	}

	return nil
}

func (s *Sim) dispatchTrackedFlightPlanCommand(tcp string, acid ACID,
	check func(tcp string, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcp string, fp *NASFlightPlan, ac *Aircraft)) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if fp.TrackingController != tcp && !s.isInstructorOrRPO(tcp) {
				return av.ErrOtherControllerHasTrack
			}
			if check != nil {
				return check(tcp, fp, ac)
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			cmd(tcp, fp, ac)
			// No radio transmissions for these
			return nil
		})
}

func (s *Sim) DeleteAircraft(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:        StatusMessageEvent,
				WrittenText: fmt.Sprintf("%s deleted %s", tcp, ac.ADSBCallsign),
			})

			s.deleteAircraft(ac)

			return nil
		})
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

func (s *Sim) DeleteAircraftSlice(tcp string, aircraft []Aircraft) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for _, ac := range aircraft {
		s.deleteAircraft(&ac)
	}

	return nil
}

func (s *Sim) DeleteAllAircraft(tcp string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcp {
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

func (s *Sim) ChangeSquawk(tcp string, callsign av.ADSBCallsign, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			s.enqueueTransponderChange(ac.ADSBCallsign, sq, ac.Mode)

			return setTransmissionController(tcp, av.MakeReadbackTransmission("squawk {beacon}", sq))
		})
}

func (s *Sim) ChangeTransponderMode(tcp string, callsign av.ADSBCallsign, mode av.TransponderMode) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			s.enqueueTransponderChange(ac.ADSBCallsign, ac.Squawk, mode)

			return setTransmissionController(tcp, av.MakeReadbackTransmission("squawk "+strings.ToLower(mode.String())))
		})
}

func (s *Sim) Ident(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.Ident(s.State.SimTime))
		})
}

func (s *Sim) CreateFlightPlan(tcp string, spec FlightPlanSpecifier) error {
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

	if util.SeqContainsFunc(maps.Values(s.Aircraft),
		func(ac *Aircraft) bool { return ac.IsAssociated() && ac.NASFlightPlan.ACID == fp.ACID }) {
		return ErrDuplicateACID
	}
	if slices.ContainsFunc(s.State.UnassociatedFlightPlans,
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

func (s *Sim) ModifyFlightPlan(tcp string, acid ACID, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	fp, _, active := s.GetFlightPlanForACID(acid)
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

	canModify := fp.TrackingController == tcp || fp.LastLocalController == tcp || s.State.AreInstructorOrRPO(tcp)
	if !canModify {
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
				FromController:      tcp,
				LeaderLineDirection: spec.GlobalLeaderLineDirection.Get(),
			})
		}
	}

	fp.Update(spec, s.LocalCodePool, s.ERAMComputer.SquawkCodePool)

	return s.postCheckFlightPlanSpecifier(spec)
}

// Associate the specified flight plan with the track. Flight plan for ACID
// must not already exist.
func (s *Sim) AssociateFlightPlan(callsign av.ADSBCallsign, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.QuickFlightPlan.IsSet && spec.QuickFlightPlan.Get() {
		base := s.State.FacilityAdaptation.FlightPlan.QuickACID
		acid := base + fmt.Sprintf("%02d", s.State.QuickFlightPlanIndex%100)
		spec.ACID.Set(ACID(acid))
		s.State.QuickFlightPlanIndex++
	}
	if !spec.ACID.IsSet {
		spec.ACID.Set(ACID(callsign))
	}

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	err := s.dispatchAircraftCommand(spec.TrackingController.Get(), callsign,
		func(tcp string, ac *Aircraft) error {
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
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			// Either the flight plan was passed in or fp was initialized  in the validation function.
			fp := s.STARSComputer.takeFlightPlanByACID(spec.ACID.Get())

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
func (s *Sim) ActivateFlightPlan(tcp string, callsign av.ADSBCallsign, acid ACID,
	spec *FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp := s.STARSComputer.takeFlightPlanByACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if spec != nil {
		fp.Update(*spec, s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
	}

	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsAssociated() {
				return ErrTrackIsActive
			}
			return nil
		},
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			ac.AssociateFlightPlan(fp)

			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: fp.ACID,
			})

			return nil
		})
}

func (s *Sim) DeleteFlightPlan(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.TrackingController == tcp && ac.NASFlightPlan.ACID == acid {
			s.deleteFlightPlan(ac.DisassociateFlightPlan())
			return nil
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

func (s *Sim) RepositionTrack(tcp string, acid ACID, callsign av.ADSBCallsign, p math.Point2LL) error {
	s.lastControlCommandTime = time.Now()

	// Find the corresponding flight plan.
	var fp *NASFlightPlan
	// First look for the referenced flight plan in associated aircraft.
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			if ac.NASFlightPlan.TrackingController != tcp {
				return av.ErrOtherControllerHasTrack
			} else if ac.NASFlightPlan.HandoffTrackController != "" {
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
				if sfp.TrackingController != tcp {
					return av.ErrOtherControllerHasTrack
				} else if sfp.HandoffTrackController != "" {
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

	if callsign != "" { // Associating it with an active track
		if ac, ok := s.Aircraft[callsign]; !ok {
			return ErrNoMatchingFlight
		} else if ac.IsAssociated() {
			return ErrTrackIsActive
		} else {
			ac.AssociateFlightPlan(fp)
			if s.State.IsLocalController(fp.TrackingController) {
				fp.LastLocalController = fp.TrackingController
			}

			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: fp.ACID,
			})
			return nil
		}
	} else { // Creating / moving an unsupported DB.
		fp.Location = p
		s.STARSComputer.FlightPlans = append(s.STARSComputer.FlightPlans, fp)
	}
	return nil
}

func (s *Sim) HandoffTrack(tcp string, acid ACID, toTCP string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if _, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if toTCP == tcp {
				// Can't handoff to ourself
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
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) {
			s.handoffTrack(fp, toTCP)
		})
}

func (s *Sim) handoffTrack(fp *NASFlightPlan, toTCP string) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fp.TrackingController,
		ToController:   toTCP,
	})

	fp.HandoffTrackController = toTCP

	if _, ok := s.State.Controllers[toTCP]; !ok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", fp.ACID, toTCP)
	}

	// Add them to the auto-accept map even if the target controller is
	// currently signed in; this way, if they sign off in the interim, we
	// still end up accepting it automatically.
	acceptDelay := 4 + s.Rand.Intn(10)
	s.Handoffs[fp.ACID] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
	if fp.TypeOfFlight == av.FlightTypeDeparture && !s.isActiveHumanController(fp.TrackingController) && !s.isActiveHumanController(toTCP) {
		if callsign, ok := s.callsignForACID(fp.ACID); ok {
			// aircraft is a departure that will likely never talk to a human, send it on course (mainly so it climbs up to cruise)
			s.enqueueDepartOnCourse(callsign)
		}
	}
}

func (s *Sim) ContactTrackingController(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, sfp *NASFlightPlan, ac *Aircraft) error {
			if sfp.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			if ac == nil {
				return av.ErrNoAircraftForCallsign
			}
			return nil
		},
		func(tcp string, sfp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			return s.contactController(tcp, sfp, ac, sfp.TrackingController)
		})
}

func (s *Sim) ContactController(tcp string, acid ACID, toTCP string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, sfp *NASFlightPlan, ac *Aircraft) error {
			if sfp.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			if ac == nil {
				return av.ErrNoAircraftForCallsign
			}
			return nil
		},
		func(tcp string, sfp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			return s.contactController(tcp, sfp, ac, toTCP)
		})
}

func (s *Sim) contactController(fromTCP string, sfp *NASFlightPlan, ac *Aircraft, toTCP string) *av.RadioTransmission {
	// Immediately respond to the current controller that we're
	// changing frequency.
	var resp *av.RadioTransmission
	if octrl, ok := s.State.Controllers[toTCP]; ok {
		if toTCP == fromTCP {
			resp = av.MakeReadbackTransmission("Unable, we are already on {freq}", octrl.Frequency)
		} else if ac.TypeOfFlight == av.FlightTypeDeparture {
			resp = av.MakeReadbackTransmission("[contact|over to|] {dctrl} on {freq}, [good day|seeya|]", octrl, octrl.Frequency)
		} else {
			resp = av.MakeReadbackTransmission("[contact|over to|] {actrl} on {freq}, [good day|seeya|]", octrl, octrl.Frequency)
		}
	} else {
		resp = av.MakeReadbackTransmission("[goodbye|seeya]")
	}

	s.eventStream.Post(Event{
		Type:           HandoffControlEvent,
		FromController: sfp.ControllingController,
		ToController:   toTCP,
		ACID:           sfp.ACID,
	})

	// Take away the current controller's ability to issue control
	// commands.
	sfp.ControllingController = ""

	// In 5-10 seconds, have the aircraft contact the new controller
	// (and give them control only then).
	wait := time.Duration(5+s.Rand.Intn(10)) * time.Second
	s.enqueueControllerContact(ac.ADSBCallsign, toTCP, wait)

	return setTransmissionController(sfp.ControllingController, resp)
}

func (s *Sim) AcceptHandoff(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if fp.HandoffTrackController == tcp {
				return nil
			}
			if po, ok := s.PointOuts[fp.ACID]; ok && po.ToController == tcp {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				ACID:           fp.ACID,
				FromController: fp.TrackingController,
				ToController:   tcp,
			})

			previousTrackingController := fp.TrackingController

			fp.HandoffTrackController = ""
			fp.TrackingController = tcp
			fp.LastLocalController = tcp

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, acid)

			if ac != nil {
				haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
					func(wp av.Waypoint) bool { return wp.TransferComms })
				if !haveTransferComms && !s.isActiveHumanController(previousTrackingController) {
					// For a handoff from a virtual controller, cue up a delayed
					// contact message unless there's a point later in the route when
					// comms are to be transferred.
					wait := time.Duration(5+s.Rand.Intn(10)) * time.Second
					s.enqueueControllerContact(ac.ADSBCallsign, tcp, wait)
				}
			}
			return nil
		})
}

func (s *Sim) CancelHandoff(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackedFlightPlanCommand(tcp, acid, nil,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) {
			delete(s.Handoffs, acid)
			fp.HandoffTrackController = ""
			fp.RedirectedHandoff = RedirectedHandoff{}
		})
}

func (s *Sim) RedirectHandoff(tcp string, acid ACID, controller string) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Id() == tcp || octrl.Id() == ac.NASFlightPlan.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return av.ErrInvalidController
			} else if ctrl, ok := s.State.Controllers[tcp]; !ok {
				return ErrUnknownController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return av.ErrInvalidFacility
			} else if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			rh := &fp.RedirectedHandoff
			rh.OriginalOwner = fp.TrackingController
			ctrl := s.State.Controllers[tcp]
			if rh.ShouldFallbackToHandoff(tcp, octrl.Id()) {
				fp.HandoffTrackController = rh.Redirector[0]
				*rh = RedirectedHandoff{}
				return nil
			}
			rh.AddRedirector(ctrl)
			rh.RedirectedTo = octrl.Id()

			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(tcp string, acid ACID) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			rh := &fp.RedirectedHandoff
			if rh.RedirectedTo == tcp { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: rh.OriginalOwner,
					ToController:   tcp,
					ACID:           acid,
				})
				fp.HandoffTrackController = ""
				fp.TrackingController = tcp
				fp.LastLocalController = tcp
				*rh = RedirectedHandoff{}
			} else if rh.GetLastRedirector() == tcp { // Recall (only the last redirector is able to recall)
				if len(rh.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					rh.RedirectedTo = rh.Redirector[len(rh.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					fp.HandoffTrackController = rh.Redirector[len(rh.Redirector)-1]
					*rh = RedirectedHandoff{}
				}
			}

			return nil
		})
}

func (s *Sim) ForceQL(tcp string, acid ACID, controller string) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: tcp,
				ToController:   octrl.Id(),
				ACID:           acid,
			})

			return nil
		})
}

func (s *Sim) PointOut(fromTCP string, acid ACID, toTCP string) error {
	return s.dispatchTrackedFlightPlanCommand(fromTCP, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if octrl, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if octrl.Facility != s.State.Controllers[fromTCP].Facility {
				// Can't point out to another STARS facility.
				return av.ErrInvalidController
			} else if toTCP == fromTCP {
				// Can't point out to ourself
				return av.ErrInvalidController
			} else if fp.HandoffTrackController != "" {
				// Can't point out if it's being handed off
				return ErrTrackIsBeingHandedOff
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) {
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(acid, ctrl, octrl)
		})
}

func (s *Sim) pointOut(acid ACID, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.Id(),
		ToController:   to.Id(),
		ACID:           acid,
	})

	acceptDelay := 4 + s.Rand.Intn(10)
	s.PointOuts[acid] = PointOut{
		FromController: from.Id(),
		ToController:   to.Id(),
		AcceptTime:     s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(tcp string, acid ACID) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}

			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[acid].FromController,
				ACID:           acid,
			})
			if ac.NASFlightPlan != nil {
				if len(ac.NASFlightPlan.PointOutHistory) < 20 {
					fp.PointOutHistory = append([]string{tcp}, fp.PointOutHistory...)
				} else {
					fp.PointOutHistory = fp.PointOutHistory[:19]
					fp.PointOutHistory = append([]string{tcp}, fp.PointOutHistory...)
				}
			}

			delete(s.PointOuts, acid)
			return nil
		})
}

func (s *Sim) RecallPointOut(tcp string, acid ACID) error {
	return s.dispatchTrackedFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.FromController != tcp {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[acid].ToController,
				ACID:           acid,
			})

			delete(s.PointOuts, acid)
		})
}

func (s *Sim) RejectPointOut(tcp string, acid ACID) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcp string, fp *NASFlightPlan, ac *Aircraft) *av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[acid].FromController,
				ACID:           acid,
			})

			delete(s.PointOuts, acid)

			return nil
		})
}

// TODO: Migrate to ERAM computer.
func (s *Sim) SendRouteCoordinates(tcp string, acid ACID) error {
	ac := s.Aircraft[av.ADSBCallsign(acid)]
	if ac == nil {
		return av.ErrNoAircraftForCallsign
	}
	waypoints := []av.Waypoint(ac.Nav.Waypoints)
	waypointPairs := []math.Point2LL{}
	for _, wyp := range waypoints {
		if _, ok := av.DB.LookupWaypoint(wyp.Fix); ok { // only send actual waypoints
			waypointPairs = append(waypointPairs, [2]float32{wyp.Location[0], wyp.Location[1]})
		}

	}
	ctrl := s.State.ResolveController(tcp)
	s.eventStream.Post(Event{
		Type:         FixCoordinatesEvent,
		ACID:         acid,
		WaypointInfo: waypointPairs,
		ToController: ctrl,
	})
	return nil
}

// TODO: Migrate to ERAM computer.
func (s *Sim) FlightPlanDirect(tcp, fix string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ac := s.Aircraft[av.ADSBCallsign(acid)]
	fp := ac.FlightPlan
	idx := strings.Index(fp.Route, fix)
	if idx < 0 {
		return av.ErrNoMatchingFix
	}
	rte := fp.Route[idx:]
	fp.Route = rte
	// Update sim Track as well
	trk := s.State.Tracks[ac.ADSBCallsign]
	pos, ok := av.DB.LookupWaypoint(fix)
	if !ok {
		return av.ErrNoMatchingFix // Check this pls
	}
	for i, wpPos := range trk.Route {
		if wpPos == pos {
			// Remove all waypoints before the fix
			trk.Route = trk.Route[i:]
			break
		}
	}
	// TODO: Post an event that will update the controller's output.
	return nil
}

func (s *Sim) ReleaseDeparture(tcp string, callsign av.ADSBCallsign) error {
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
	if dc := s.State.ResolveController(fp.InboundHandoffController); dc != tcp {
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

func (s *Sim) PilotMixUp(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return ac.PilotMixUp()
		})
}

func (s *Sim) AssignAltitude(tcp string, callsign av.ADSBCallsign, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.AssignAltitude(altitude, afterSpeed))
		})
}

type HeadingArgs struct {
	TCP          string
	ADSBCallsign av.ADSBCallsign
	Heading      int
	Present      bool
	LeftDegrees  int
	RightDegrees int
	Turn         nav.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(hdg.TCP, hdg.ADSBCallsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			if hdg.Present {
				return setTransmissionController(tcp, ac.FlyPresentHeading())
			} else if hdg.LeftDegrees != 0 {
				return setTransmissionController(tcp, ac.TurnLeft(hdg.LeftDegrees))
			} else if hdg.RightDegrees != 0 {
				return setTransmissionController(tcp, ac.TurnRight(hdg.RightDegrees))
			} else {
				return setTransmissionController(tcp, ac.AssignHeading(hdg.Heading, hdg.Turn))
			}
		})
}

func (s *Sim) AssignSpeed(tcp string, callsign av.ADSBCallsign, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.AssignSpeed(speed, afterAltitude))
		})
}

func (s *Sim) MaintainSlowestPractical(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.MaintainSlowestPractical())
		})
}

func (s *Sim) MaintainMaximumForward(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.MaintainMaximumForward())
		})
}

func (s *Sim) SaySpeed(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.SaySpeed())
		})
}

func (s *Sim) SayAltitude(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.SayAltitude())
		})
}

func (s *Sim) SayHeading(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.SayHeading())
		})
}

func (s *Sim) ExpediteDescent(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.ExpediteDescent())
		})
}

func (s *Sim) ExpediteClimb(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.ExpediteClimb())
		})
}

func (s *Sim) DirectFix(tcp string, callsign av.ADSBCallsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.DirectFix(fix))
		})
}

func (s *Sim) HoldAtFix(tcp string, callsign av.ADSBCallsign, fix string, hold *av.Hold) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.HoldAtFix(fix, hold))
		})
}

func (s *Sim) DepartFixDirect(tcp string, callsign av.ADSBCallsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.DepartFixDirect(fixa, fixb))
		})
}

func (s *Sim) DepartFixHeading(tcp string, callsign av.ADSBCallsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.DepartFixHeading(fix, heading))
		})
}

func (s *Sim) CrossFixAt(tcp string, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.CrossFixAt(fix, ar, speed))
		})
}

func (s *Sim) AtFixCleared(tcp string, callsign av.ADSBCallsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.AtFixCleared(fix, approach))
		})
}

func (s *Sim) ExpectApproach(tcp string, callsign av.ADSBCallsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return av.ErrUnknownAirport
		}
	}

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.ExpectApproach(approach, ap, s.lg))
		})
}

func (s *Sim) ClearedApproach(tcp string, callsign av.ADSBCallsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			var err error
			var resp *av.RadioTransmission
			if straightIn {
				resp, err = ac.ClearedStraightInApproach(approach, s.lg)
			} else {
				resp, err = ac.ClearedApproach(approach, s.lg)
			}

			if err == nil && ac.IsAssociated() {
				ac.ApproachController = ac.NASFlightPlan.ControllingController
			}
			return setTransmissionController(tcp, resp)
		})
}

func (s *Sim) InterceptLocalizer(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.InterceptApproach(s.lg))
		})
}

func (s *Sim) CancelApproachClearance(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.CancelApproachClearance())
		})
}

func (s *Sim) ClimbViaSID(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.ClimbViaSID())
		})
}

func (s *Sim) DescendViaSTAR(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.DescendViaSTAR())
		})
}

func (s *Sim) GoAround(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			rt := ac.GoAround()
			if rt != nil {
				rt.Type = av.RadioTransmissionUnexpected
			}
			return setTransmissionController(tcp, rt)
		})
}

func (s *Sim) ContactTower(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			result := ac.ContactTower(s.lg)
			if result != nil && result.Type != av.RadioTransmissionUnexpected && ac.IsAssociated() {
				ac.NASFlightPlan.ControllingController = "_TOWER"
			}
			return setTransmissionController(tcp, result)
		})

}

func (s *Sim) ResumeOwnNavigation(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.ResumeOwnNavigation())
		})
}

func (s *Sim) AltitudeOurDiscretion(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			return setTransmissionController(tcp, ac.AltitudeOurDiscretion())
		})
}

func (s *Sim) RadarServicesTerminated(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			s.enqueueTransponderChange(ac.ADSBCallsign, 0o1200, ac.Mode)

			// Leave our frequency
			if ac.IsAssociated() {
				ac.NASFlightPlan.ControllingController = ""
			}

			return setTransmissionController(tcp, av.MakeReadbackTransmission("[radar services terminated, seeya|radar services terminated, squawk VFR]"))
		})
}

func (s *Sim) GoAhead(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) *av.RadioTransmission {
			if !ac.WaitingForGoAhead {
				return nil
			}

			ac.WaitingForGoAhead = false

			// Send the full flight following request
			s.sendFullFlightFollowingRequest(ac, tcp)

			return nil
		})
}

///////////////////////////////////////////////////////////////////////////
// Deferred operations

type FutureControllerContact struct {
	ADSBCallsign av.ADSBCallsign
	TCP          string
	Time         time.Time
}

func (s *Sim) enqueueControllerContact(callsign av.ADSBCallsign, tcp string, wait time.Duration) {
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
					ac.NASFlightPlan.ControllingController = c.TCP

					rt := ac.ContactMessage(s.ReportingPoints)
					rt.Type = av.RadioTransmissionContact

					s.postRadioEvent(c.ADSBCallsign, c.TCP, *rt)

					// Activate pre-assigned external emergency; the transmission will be
					// consolidated with the initial contact transmission.
					_, human := s.humanControllers[ac.NASFlightPlan.ControllingController]
					if human && ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1 {
						ac.EmergencyState.CurrentStage = 0
						s.runEmergencyStage(ac)
					}

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					if ac.IsDeparture() && !human {
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
					s.State.Tracks[ac.ADSBCallsign].FlightPlan.InterimAlt = 0
					s.State.Tracks[ac.ADSBCallsign].FlightPlan.InterimType = 0
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
	RemainingInput string
	Error          error
}

// RunAircraftControlCommands executes a space-separated string of control commands for an aircraft.
// Returns the remaining unparsed input and any error that occurred.
// This is the core command execution logic shared by the dispatcher and automated test code.
func (s *Sim) RunAircraftControlCommands(tcp string, callsign av.ADSBCallsign, commandStr string) ControlCommandsResult {
	commands := strings.Fields(commandStr)

	for i, command := range commands {
		err := s.runOneControlCommand(tcp, callsign, command)
		if err != nil {
			return ControlCommandsResult{
				RemainingInput: strings.Join(commands[i:], " "),
				Error:          err,
			}
		}
	}

	return ControlCommandsResult{}
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
func (s *Sim) runOneControlCommand(tcp string, callsign av.ADSBCallsign, command string) error {
	if len(command) == 0 {
		return ErrInvalidCommandSyntax
	}

	// A###, C###, and D### all equivalently assign an altitude
	if (command[0] == 'A' || command[0] == 'C' || command[0] == 'D') && len(command) > 1 && util.IsAllNumbers(command[1:]) {
		alt, err := strconv.Atoi(command[1:])
		if err != nil {
			return err
		}
		if err := s.AssignAltitude(tcp, callsign, 100*alt, false); err != nil {
			return err
		}
		return nil
	}

	switch command[0] {
	case 'A':
		if command == "A" {
			if err := s.AltitudeOurDiscretion(tcp, callsign); err != nil {
				return err
			}
		} else {
			components := strings.Split(command, "/")
			if len(components) != 2 || len(components[1]) == 0 || components[1][0] != 'C' {
				return ErrInvalidCommandSyntax
			}

			fix := strings.ToUpper(components[0][1:])
			approach := components[1][1:]
			if err := s.AtFixCleared(tcp, callsign, fix, approach); err != nil {
				return err
			}
		}

	case 'C':
		if command == "CAC" {
			if err := s.CancelApproachClearance(tcp, callsign); err != nil {
				return err
			}
		} else if command == "CVS" {
			if err := s.ClimbViaSID(tcp, callsign); err != nil {
				return err
			}
		} else if len(command) > 4 && command[:3] == "CSI" && !util.IsAllNumbers(command[3:]) {
			if err := s.ClearedApproach(tcp, callsign, command[3:], true); err != nil {
				return err
			}
		} else if components := strings.Split(command, "/"); len(components) > 1 {
			fix := components[0][1:]
			var ar *av.AltitudeRestriction
			speed := 0

			for _, cmd := range components[1:] {
				if len(cmd) == 0 {
					return ErrInvalidCommandSyntax
				}

				var err error
				if cmd[0] == 'A' && len(cmd) > 1 {
					if ar, err = av.ParseAltitudeRestriction(cmd[1:]); err != nil {
						return err
					}
					ar.Range[0] *= 100
					ar.Range[1] *= 100
				} else if cmd[0] == 'S' {
					if speed, err = strconv.Atoi(cmd[1:]); err != nil {
						return err
					}
				} else {
					return ErrInvalidCommandSyntax
				}
			}

			if err := s.CrossFixAt(tcp, callsign, fix, ar, speed); err != nil {
				return err
			}
		} else if strings.HasPrefix(command, "CT") && len(command) > 2 {
			if err := s.ContactController(tcp, ACID(callsign), command[2:]); err != nil {
				return err
			}
		} else if err := s.ClearedApproach(tcp, callsign, command[1:], false); err != nil {
			return err
		}

	case 'D':
		if command == "DVS" {
			if err := s.DescendViaSTAR(tcp, callsign); err != nil {
				return err
			}
		} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
			fix := components[0][1:]

			switch components[1][0] {
			case 'D':
				if err := s.DepartFixDirect(tcp, callsign, fix, components[1][1:]); err != nil {
					return err
				}
			case 'H':
				hdg, err := strconv.Atoi(components[1][1:])
				if err != nil {
					return err
				}
				if err := s.DepartFixHeading(tcp, callsign, fix, hdg); err != nil {
					return err
				}
			default:
				return ErrInvalidCommandSyntax
			}
		} else if len(command) >= 4 && len(command) <= 6 {
			if err := s.DirectFix(tcp, callsign, command[1:]); err != nil {
				return err
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'E':
		if command == "ED" {
			if err := s.ExpediteDescent(tcp, callsign); err != nil {
				return err
			}
		} else if command == "EC" {
			if err := s.ExpediteClimb(tcp, callsign); err != nil {
				return err
			}
		} else if len(command) > 1 {
			if err := s.ExpectApproach(tcp, callsign, command[1:]); err != nil {
				return err
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'F':
		if command == "FC" {
			if err := s.ContactTrackingController(tcp, ACID(callsign)); err != nil {
				return err
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'G':
		if command == "GA" {
			if err := s.GoAhead(tcp, callsign); err != nil {
				return err
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'H':
		if len(command) == 1 {
			// Present heading
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				Present:      true,
			}); err != nil {
				return err
			}
		} else if hdg, err := strconv.Atoi(command[1:]); err == nil {
			// Fly heading xxx
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         nav.TurnClosest,
			}); err != nil {
				return err
			}
		} else {
			// Hold at fix (published or controller-specified)
			if fix, hold, ok := parseHold(command[1:]); !ok {
				return ErrInvalidCommandSyntax
			} else if err := s.HoldAtFix(tcp, callsign, fix, hold); err != nil {
				return err
			}
		}

	case 'I':
		if len(command) == 1 {
			if err := s.InterceptLocalizer(tcp, callsign); err != nil {
				return err
			}
		} else if command == "ID" {
			if err := s.Ident(tcp, callsign); err != nil {
				return err
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'L':
		if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return err
			}
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				LeftDegrees:  deg,
			}); err != nil {
				return err
			}
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return err
			}
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         nav.TurnLeft,
			}); err != nil {
				return err
			}
		}

	case 'R':
		if command == "RON" {
			if err := s.ResumeOwnNavigation(tcp, callsign); err != nil {
				return err
			}
		} else if command == "RST" {
			if err := s.RadarServicesTerminated(tcp, callsign); err != nil {
				return err
			}
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return err
			}
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				RightDegrees: deg,
			}); err != nil {
				return err
			}
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return err
			}
			if err := s.AssignHeading(&HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         nav.TurnRight,
			}); err != nil {
				return err
			}
		}

	case 'S':
		if len(command) == 1 {
			if err := s.AssignSpeed(tcp, callsign, 0, false); err != nil {
				return err
			}
		} else if command == "SMIN" {
			if err := s.MaintainSlowestPractical(tcp, callsign); err != nil {
				return err
			}
		} else if command == "SMAX" {
			if err := s.MaintainMaximumForward(tcp, callsign); err != nil {
				return err
			}
		} else if command == "SS" {
			if err := s.SaySpeed(tcp, callsign); err != nil {
				return err
			}
		} else if command == "SQS" {
			if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeStandby); err != nil {
				return err
			}
		} else if command == "SQA" {
			if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeAltitude); err != nil {
				return err
			}
		} else if command == "SQON" {
			if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeOn); err != nil {
				return err
			}
		} else if len(command) == 6 && command[:2] == "SQ" {
			sq, err := av.ParseSquawk(command[2:])
			if err != nil {
				return err
			}
			if err := s.ChangeSquawk(tcp, callsign, sq); err != nil {
				return err
			}
		} else if command == "SH" {
			if err := s.SayHeading(tcp, callsign); err != nil {
				return err
			}
		} else if command == "SA" {
			if err := s.SayAltitude(tcp, callsign); err != nil {
				return err
			}
		} else {
			kts, err := strconv.Atoi(command[1:])
			if err != nil {
				return err
			}
			if err := s.AssignSpeed(tcp, callsign, kts, false); err != nil {
				return err
			}
		}

	case 'T':
		if command == "TO" {
			if err := s.ContactTower(tcp, callsign); err != nil {
				return err
			}
		} else if n := len(command); n > 2 {
			if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
				if command[n-1] == 'L' {
					if err := s.AssignHeading(&HeadingArgs{
						TCP:          tcp,
						ADSBCallsign: callsign,
						LeftDegrees:  deg,
					}); err != nil {
						return err
					}
					return nil
				} else if command[n-1] == 'R' {
					if err := s.AssignHeading(&HeadingArgs{
						TCP:          tcp,
						ADSBCallsign: callsign,
						RightDegrees: deg,
					}); err != nil {
						return err
					}
					return nil
				}
			}

			switch command[:2] {
			case "TS":
				kts, err := strconv.Atoi(command[2:])
				if err != nil {
					return err
				}
				if err := s.AssignSpeed(tcp, callsign, kts, true); err != nil {
					return err
				}

			case "TA", "TC", "TD":
				alt, err := strconv.Atoi(command[2:])
				if err != nil {
					return err
				}
				if err := s.AssignAltitude(tcp, callsign, 100*alt, true); err != nil {
					return err
				}

			default:
				return ErrInvalidCommandSyntax
			}
		} else {
			return ErrInvalidCommandSyntax
		}

	case 'X':
		s.DeleteAircraft(tcp, callsign)

	default:
		return ErrInvalidCommandSyntax
	}

	return nil
}

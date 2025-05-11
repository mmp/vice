// pkg/sim/control.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

func (s *Sim) dispatchAircraftCommand(tcp string, callsign av.ADSBCallsign,
	check func(tcp string, ac *Aircraft) error,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	if ac, ok := s.Aircraft[callsign]; !ok {
		return av.ErrNoAircraftForCallsign
	} else if _, ok := s.State.Controllers[tcp]; !ok {
		return ErrUnknownController
	} else {
		if check != nil {
			if err := check(tcp, ac); err != nil {
				return err
			}
		}

		preAc := *ac
		radioTransmissions := cmd(tcp, ac)

		s.lg.Info("dispatch_command", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
			slog.Any("radio_transmissions", radioTransmissions))

		s.postRadioEvents(ac.ADSBCallsign, tcp, radioTransmissions)

		return nil
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControlledAircraftCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				if ac.PreArrivalDropController == tcp {
					return nil
				}
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.ControllingController != tcp && !s.Instructors[tcp] {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackedAircraftCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.TrackingController != tcp && !s.Instructors[tcp] {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Can issue both to aircraft we track but also unassociated VFRs
func (s *Sim) dispatchVFRAircraftCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			// Allow issuing this command to random unassociated VFRs but
			// not IFRs that other controllers already own.
			if ac.IsAssociated() && ac.STARSFlightPlan.ControllingController != tcp && !s.Instructors[tcp] {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Note that ac may be nil, but flight plan will not be!
func (s *Sim) dispatchFlightPlanCommand(tcp string, acid ACID,
	check func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error,
	cmd func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission) error {
	fp, _ := s.GetFlightPlanForACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	ac := s.Aircraft[av.ADSBCallsign(acid)] // FIXME: buggy if ACID and ADSB callsign don't match
	if ac != nil && ac.STARSFlightPlan.ACID != acid {
		ac = nil
	}

	if _, ok := s.State.Controllers[tcp]; !ok {
		return ErrUnknownController
	}

	if check != nil {
		if err := check(tcp, fp, ac); err != nil {
			return err
		}
	}

	preFp := *fp
	radioTransmissions := cmd(tcp, fp, ac)

	s.lg.Info("dispatch_fp_command", slog.String("acid", string(fp.ACID)),
		slog.Any("prepost_fp", []STARSFlightPlan{preFp, *fp}),
		slog.Any("radio_transmissions", radioTransmissions))

	s.postRadioEvents(av.ADSBCallsign(fp.ACID), tcp, radioTransmissions)

	return nil
}

func (s *Sim) dispatchTrackedFlightPlanCommand(tcp string, acid ACID,
	check func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error,
	cmd func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if fp.TrackingController != tcp && !s.Instructors[tcp] {
				return av.ErrOtherControllerHasTrack
			}
			if check != nil {
				return check(tcp, fp, ac)
			}
			return nil
		},
		cmd)
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
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: fmt.Sprintf("%s deleted %s", tcp, ac.ADSBCallsign),
			})

			s.lg.Info("deleted aircraft", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
				slog.String("controller", tcp))

			s.deleteAircraft(ac)

			return nil
		})
}

func (s *Sim) deleteAircraft(ac *Aircraft) {
	delete(s.Aircraft, ac.ADSBCallsign)

	s.STARSComputer.HoldForRelease = slices.DeleteFunc(s.STARSComputer.HoldForRelease,
		func(a *Aircraft) bool { return ac.ADSBCallsign == a.ADSBCallsign })

	if fp := ac.STARSFlightPlan; fp != nil {
		delete(s.Handoffs, fp.ACID)
		delete(s.PointOuts, fp.ACID)
		s.deleteFlightPlan(fp)
	}
}

func (s *Sim) DeleteAllAircraft(tcp string) error {
	for cs := range s.Aircraft {
		// Only delete airborne aircraft; leave all of the ones at the
		// gate, etc., so we don't have a bubble of no departures for a
		// long time while the departure queues refill.
		if s.Aircraft[cs].IsAirborne() {
			if err := s.DeleteAircraft(tcp, cs); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Sim) ChangeSquawk(tcp string, callsign av.ADSBCallsign, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.ADSBCallsign, sq, ac.Mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "squawk " + sq.String(),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) ChangeTransponderMode(tcp string, callsign av.ADSBCallsign, mode av.TransponderMode) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.ADSBCallsign, ac.Squawk, mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "squawk " + strings.ToLower(mode.String()),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) Ident(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchVFRAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.Ident(s.State.SimTime)
		})
}

func (s *Sim) CreateFlightPlan(tcp string, spec STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	fp, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
	if err != nil {
		return err
	}

	if util.SeqContainsFunc(maps.Values(s.Aircraft),
		func(ac *Aircraft) bool { return ac.IsAssociated() && ac.STARSFlightPlan.ACID == fp.ACID }) {
		return ErrDuplicateACID
	}
	if slices.ContainsFunc(s.State.UnassociatedFlightPlans,
		func(fp2 *STARSFlightPlan) bool { return fp.ACID == fp2.ACID }) {
		return ErrDuplicateACID
	}

	fp, err = s.STARSComputer.CreateFlightPlan(fp)

	if err == nil {
		err = s.postCheckFlightPlanSpecifier(spec)
	}

	return err
}

// General checks both for create and modify; this returns errors that prevent fp creation.
func (s *Sim) preCheckFlightPlanSpecifier(spec *STARSFlightPlanSpecifier) error {
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
func (s *Sim) postCheckFlightPlanSpecifier(spec STARSFlightPlanSpecifier) error {
	if spec.AircraftType.IsSet {
		if _, ok := av.DB.AircraftPerformance[spec.AircraftType.Get()]; !ok {
			return ErrIllegalACType
		}
	}

	return nil
}

func (s *Sim) ModifyFlightPlan(tcp string, acid ACID, spec STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp, active := s.GetFlightPlanForACID(acid)
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

	canModify := fp.TrackingController == tcp || fp.LastLocalController == tcp
	if !canModify {
		return av.ErrOtherControllerHasTrack
	}
	if active {
		// Modify assigned
		if spec.EntryFix.IsSet || spec.ExitFix.IsSet || spec.CoordinationTime.IsSet {
			// These can only be set for non-active flight plans: 5-171
			return ErrTrackIsActive
		}
		if fp.HandoffTrackController != "" {
			return ErrTrackIsBeingHandedOff
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
func (s *Sim) AssociateFlightPlan(callsign av.ADSBCallsign, spec STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.QuickFlightPlan.IsSet && spec.QuickFlightPlan.Get() {
		base := s.State.STARSFacilityAdaptation.FlightPlan.QuickACID
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
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			// Either the flight plan was passed in or fp was initialized  in the validation function.
			fp := s.STARSComputer.takeFlightPlanByACID(spec.ACID.Get())
			fp.ControllingController = tcp // HACK so we can give them instructions, pending VFR calling in
			fp.LastLocalController = tcp

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
	spec *STARSFlightPlanSpecifier) error {
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
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			// TODO: needed?
			fp.TrackingController = tcp
			fp.ControllingController = tcp // HACK so we can give them instructions, pending VFR calling in
			fp.LastLocalController = tcp

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

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.TrackingController == tcp && ac.STARSFlightPlan.ACID == acid {
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

func (s *Sim) deleteFlightPlan(fp *STARSFlightPlan) {
	s.STARSComputer.returnListIndex(fp.ListIndex)
	if fp.PlanType == LocalNonEnroute {
		s.LocalCodePool.Return(fp.AssignedSquawk)
	} else {
		s.ERAMComputer.SquawkCodePool.Return(fp.AssignedSquawk)
	}
}

func (s *Sim) RepositionTrack(tcp string, acid ACID, callsign av.ADSBCallsign, p math.Point2LL) error {
	// Find the corresponding flight plan.
	var fp *STARSFlightPlan
	// First look for the referenced flight plan in associated aircraft.
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.ACID == acid {
			if ac.STARSFlightPlan.TrackingController != tcp {
				return av.ErrOtherControllerHasTrack
			} else if ac.STARSFlightPlan.HandoffTrackController != "" {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if _, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if toTCP == tcp {
				// Can't handoff to ourself
				return av.ErrInvalidController
			} else if ac != nil {
				// Disallow handoff if there's a beacon code mismatch.
				squawkingSPC, _ := ac.Squawk.IsSPC()
				if ac.Squawk != ac.STARSFlightPlan.AssignedSquawk && !squawkingSPC {
					return ErrBeaconMismatch
				}
			}
			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			s.handoffTrack(fp, toTCP)
			return nil
		})
}

func (s *Sim) handoffTrack(fp *STARSFlightPlan, toTCP string) {
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
}

func (s *Sim) HandoffControl(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, sfp *STARSFlightPlan, ac *Aircraft) error {
			if sfp.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, sfp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			// Immediately respond to the current controller that we're
			// changing frequency.
			if octrl, ok := s.State.Controllers[sfp.TrackingController]; ok {
				if sfp.TrackingController == tcp {
					radioTransmissions = append(radioTransmissions, av.RadioTransmission{
						Controller: sfp.ControllingController,
						Message:    "Unable, we are already on " + octrl.Frequency.String(),
						Type:       av.RadioTransmissionReadback,
					})
					return radioTransmissions
				}
				bye := rand.Sample(s.Rand, "good day", "seeya")
				contact := rand.Sample(s.Rand, "contact ", "over to ", "")
				goodbye := contact + octrl.RadioName + " on " + octrl.Frequency.String() + ", " + bye
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: sfp.ControllingController,
					Message:    goodbye,
					Type:       av.RadioTransmissionReadback,
				})
			} else {
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: sfp.ControllingController,
					Message:    "goodbye",
					Type:       av.RadioTransmissionReadback,
				})
			}

			s.eventStream.Post(Event{
				Type:           HandoffControlEvent,
				FromController: sfp.ControllingController,
				ToController:   sfp.TrackingController,
				ACID:           acid,
			})

			// Take away the current controller's ability to issue control
			// commands.
			sfp.ControllingController = ""

			// In 5-10 seconds, have the aircraft contact the new controller
			// (and give them control only then).
			wait := time.Duration(5+s.Rand.Intn(10)) * time.Second
			s.enqueueControllerContact(ac.ADSBCallsign, sfp.TrackingController, wait)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if fp.HandoffTrackController == tcp {
				return nil
			}
			if po, ok := s.PointOuts[fp.ACID]; ok && po.ToController == tcp {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe

		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: fp.ControllingController,
				ToController:   tcp,
			})

			fp.HandoffTrackController = ""
			fp.TrackingController = tcp
			fp.LastLocalController = tcp

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, acid)

			if ac != nil {
				haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
					func(wp av.Waypoint) bool { return wp.TransferComms })
				if !haveTransferComms && !s.isActiveHumanController(fp.ControllingController) {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, acid)
			fp.HandoffTrackController = ""
			fp.RedirectedHandoff = RedirectedHandoff{}

			return nil
		})
}

func (s *Sim) RedirectHandoff(tcp string, acid ACID, controller string) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Id() == tcp || octrl.Id() == ac.STARSFlightPlan.TrackingController {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			rh := &fp.RedirectedHandoff
			if rh.RedirectedTo == tcp { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: rh.OriginalOwner,
					ToController:   tcp,
					ACID:           acid,
				})
				fp.ControllingController = tcp
				fp.HandoffTrackController = ""
				fp.TrackingController = rh.RedirectedTo
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
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
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(acid, ctrl, octrl)
			return nil
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
	return s.dispatchTrackedFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}

			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[acid].FromController,
				ACID:           acid,
			})
			if len(ac.STARSFlightPlan.PointOutHistory) < 20 {
				fp.PointOutHistory = append([]string{tcp}, fp.PointOutHistory...)
			} else {
				fp.PointOutHistory = fp.PointOutHistory[:19]
				fp.PointOutHistory = append([]string{tcp}, fp.PointOutHistory...)
			}

			delete(s.PointOuts, acid)

			return nil
		})
}

func (s *Sim) RecallPointOut(tcp string, acid ACID) error {
	return s.dispatchTrackedFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.FromController != tcp {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[acid].ToController,
				ACID:           acid,
			})

			delete(s.PointOuts, acid)

			return nil
		})
}

func (s *Sim) RejectPointOut(tcp string, acid ACID) error {
	return s.dispatchFlightPlanCommand(tcp, acid,
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) error {
			if po, ok := s.PointOuts[acid]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcp string, fp *STARSFlightPlan, ac *Aircraft) []av.RadioTransmission {
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

func (s *Sim) ReleaseDeparture(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

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

func (s *Sim) AssignAltitude(tcp string, callsign av.ADSBCallsign, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

type HeadingArgs struct {
	TCP          string
	ADSBCallsign av.ADSBCallsign
	Heading      int
	Present      bool
	LeftDegrees  int
	RightDegrees int
	Turn         av.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(hdg.TCP, hdg.ADSBCallsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
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

func (s *Sim) AssignSpeed(tcp string, callsign av.ADSBCallsign, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(tcp string, callsign av.ADSBCallsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(tcp string, callsign av.ADSBCallsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(tcp string, callsign av.ADSBCallsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(tcp string, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(tcp string, callsign av.ADSBCallsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.AtFixCleared(fix, approach)
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
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.lg)
		})
}

func (s *Sim) ClearedApproach(tcp string, callsign av.ADSBCallsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) (resp []av.RadioTransmission) {
			var err error
			if straightIn {
				resp, err = ac.ClearedStraightInApproach(approach)
			} else {
				resp, err = ac.ClearedApproach(approach, s.lg)
			}

			if err == nil {
				ac.ApproachController = ac.STARSFlightPlan.ControllingController
			}
			return
		})
}

func (s *Sim) InterceptLocalizer(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				resp[i].Type = av.RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControlledAircraftCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan.ControllingController = "_TOWER"
			return ac.ContactTower(s.lg)
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
			if s.State.SimTime.After(c.Time) {
				if ac, ok := s.Aircraft[c.ADSBCallsign]; ok && ac.IsAssociated() {
					ac.STARSFlightPlan.ControllingController = c.TCP
					r := []av.RadioTransmission{av.RadioTransmission{
						Controller: c.TCP,
						Message:    ac.ContactMessage(s.ReportingPoints),
						Type:       av.RadioTransmissionContact,
					}}
					s.postRadioEvents(c.ADSBCallsign, c.TCP, r)

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					_, human := s.humanControllers[ac.STARSFlightPlan.ControllingController]
					if ac.IsDeparture() && !human {
						s.enqueueDepartOnCourse(ac.ADSBCallsign)
					}
				}
				return false // remove it from the slice
			}
			return true // keep it in the slice
		})

	s.FutureOnCourse = util.FilterSliceInPlace(s.FutureOnCourse,
		func(oc FutureOnCourse) bool {
			if s.State.SimTime.After(oc.Time) {
				if ac, ok := s.Aircraft[oc.ADSBCallsign]; ok {
					s.lg.Info("departing on course", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
						slog.Int("final_altitude", ac.FlightPlan.Altitude))
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

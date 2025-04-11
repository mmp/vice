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

func (s *Sim) dispatchCommand(tcp string, callsign av.ADSBCallsign,
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

		// This controller will get the readback; grab it now in case the command changes it.
		ctrl := ac.STARSFlightPlan.ControllingController

		preAc := *ac
		radioTransmissions := cmd(tcp, ac)

		for i := range radioTransmissions {
			radioTransmissions[i].Controller = ctrl
		}

		s.lg.Info("dispatch_command", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
			slog.Any("radio_transmissions", radioTransmissions))
		s.postRadioEvents(ac.ADSBCallsign, radioTransmissions)
		return nil
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
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
func (s *Sim) dispatchTrackingCommand(tcp string, callsign av.ADSBCallsign,
	cmd func(tcp string, ac *Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(tcp, callsign,
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

func (s *Sim) DeleteAircraft(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
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

	s.ERAMComputer.DeleteAircraft(ac)
	s.STARSComputer.DeleteAircraft(ac)
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

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:         IdentEvent,
				ADSBCallsign: ac.ADSBCallsign,
			})

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "ident",
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(tcp string, callsign av.ADSBCallsign, dir *math.CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan.GlobalLeaderLineDirection = dir
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				ADSBCallsign:        ac.ADSBCallsign,
				FromController:      tcp,
				LeaderLineDirection: dir,
			})
			return nil
		})
}

func (s *Sim) CreateFlightPlan(tcp string, ty STARSFlightPlanType, spec STARSFlightPlanSpecifier) (STARSFlightPlan, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp := spec.GetFlightPlan()

	if util.SeqContainsFunc(maps.Values(s.Aircraft),
		func(ac *Aircraft) bool { return ac.IsAssociated() && ac.STARSFlightPlan.ACID == fp.ACID }) {
		return STARSFlightPlan{}, ErrDuplicateACID
	}
	if slices.ContainsFunc(s.State.UnassociatedFlightPlans,
		func(fp2 STARSFlightPlan) bool { return fp.ACID == fp2.ACID }) {
		return STARSFlightPlan{}, ErrDuplicateACID
	}

	var err error
	switch ty {
	case LocalNonEnroute:
		fp, err = s.STARSComputer.CreateFlightPlan(fp)
	case LocalEnroute, RemoteEnroute:
		var sq av.Squawk
		sq, err = s.ERAMComputer.CreateSquawk()
		if err != nil {
			return fp, err
		}
		fp.AssignedSquawk = sq
		fp, err = s.STARSComputer.CreateFlightPlan(fp)
	default:
		panic("unhandled STARSFlightPlanType")
	}

	if err == nil && spec.Location.IsSet {
		// Make an unsupported track
		s.State.UnsupportedTracks = append(s.State.UnsupportedTracks,
			RadarTrack{
				RadarTrack: av.RadarTrack{Location: spec.Location.Get()},
				FlightPlan: &fp,
			})
	}

	return fp, err
}

func (s *Sim) ModifyFlightPlan(tcp string, callsign av.ADSBCallsign, spec STARSFlightPlanSpecifier) (STARSFlightPlan, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ac, ok := s.Aircraft[callsign]; ok {
		if ac.IsUnassociated() {
			return STARSFlightPlan{}, ErrTrackIsNotActive
		}
		if ac.STARSFlightPlan.TrackingController != tcp {
			return STARSFlightPlan{}, av.ErrOtherControllerHasTrack
		}

		if ac.Mode == av.TransponderModeAltitude && !ac.STARSFlightPlan.InhibitModeCAltitudeDisplay &&
			spec.PilotReportedAltitude.GetOr(0) != 0 {
			// 5-166: must inhibit mode C display if we are getting altitude from the aircraft
			// Allow zero to clear it which various STARS commands do implicitly.
			return STARSFlightPlan{}, ErrIllegalFunction
		}

		// Modify assigned
		if spec.EntryFix.IsSet || spec.ExitFix.IsSet || spec.ETAOrPTD.IsSet {
			// These can only be set for non-active flight plans: 5-171
			return STARSFlightPlan{}, ErrTrackIsActive
		}
		if ac.STARSFlightPlan.HandoffTrackController != "" {
			return STARSFlightPlan{}, ErrTrackIsBeingHandedOff
		}

		if spec.InhibitModeCAltitudeDisplay.IsSet && !spec.InhibitModeCAltitudeDisplay.Get() &&
			ac.Mode == av.TransponderModeAltitude {
			// Clear pilot reported if toggled on and we have mode-C altitude
			ac.STARSFlightPlan.PilotReportedAltitude = 0
		}

		return ac.UpdateFlightPlan(spec), nil
	} else {
		// Modify pending
		if spec.AssignedAltitude.IsSet {
			return STARSFlightPlan{}, ErrTrackIsNotActive
		}

		return s.STARSComputer.ModifyFlightPlan(spec)
	}
}

func (s *Sim) AssociateFlightPlan(callsign av.ADSBCallsign, spec STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.CreateQuick {
		base := s.State.STARSFacilityAdaptation.FlightPlan.QuickACID
		acid := base + fmt.Sprintf("%02d", s.State.QuickFlightPlanIndex%100)
		spec.ACID.Set(ACID(acid))
		s.State.QuickFlightPlanIndex++
	}

	return s.dispatchCommand(spec.TrackingController.Get(), callsign,
		func(tcp string, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.IsAssociated() {
				return av.ErrOtherControllerHasTrack
			}
			if s.STARSComputer.lookupFlightPlanByACID(spec.ACID.Get()) == nil {
				return ErrNoMatchingFlight
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			if !spec.TrackingController.IsSet {
				spec.TrackingController.Set(tcp)
			}

			fp := spec.GetFlightPlan()
			if _, err := s.STARSComputer.CreateFlightPlan(fp); err != nil {
				s.lg.Warnf("%s: error creating flight plan: %v", fp.ACID, err)
			}
			ac.AssociateFlightPlan(s.STARSComputer.takeFlightPlanByACID(fp.ACID))

			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				ADSBCallsign: ac.ADSBCallsign,
				ToController: tcp,
			})

			return nil
		})
}

func (s *Sim) ActivateFlightPlan(tcp string, trackCallsign av.ADSBCallsign, fpACID ACID,
	spec *STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp := s.STARSComputer.takeFlightPlanByACID(fpACID)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if spec != nil {
		fp.Update(*spec)
	}

	return s.dispatchCommand(tcp, trackCallsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsAssociated() {
				return ErrTrackIsActive
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan = fp
			// TODO: needed?
			ac.STARSFlightPlan.TrackingController = tcp
			ac.STARSFlightPlan.ControllingController = tcp // HAX
			return nil
		})
}

func (s *Sim) DeleteFlightPlan(tcp string, acid ACID) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	found := false

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.TrackingController == tcp && ac.STARSFlightPlan.ACID == acid {
			ac.STARSFlightPlan = nil

			s.ERAMComputer.DeleteAircraft(ac)
			s.STARSComputer.DeleteAircraft(ac)
			found = true
		}
	}

	fp := s.STARSComputer.takeFlightPlanByACID(acid)
	found = found || fp != nil

	return util.Select(found, nil, ErrNoMatchingFlightPlan)
}

func (s *Sim) HandoffTrack(tcp string, callsign av.ADSBCallsign, toTCP string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.TrackingController != tcp {
				return av.ErrOtherControllerHasTrack
			} else if _, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if toTCP == tcp {
				// Can't handoff to ourself
				return av.ErrInvalidController
			} else {
				// Disallow handoff if there's a beacon code mismatch.
				squawkingSPC, _ := ac.Squawk.IsSPC()
				if ac.Squawk != ac.STARSFlightPlan.AssignedSquawk && !squawkingSPC {
					return ErrBeaconMismatch
				}
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.handoffTrack(tcp, toTCP, ac)
			return nil
		})
}

func (s *Sim) handoffTrack(fromTCP, toTCP string, ac *Aircraft) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fromTCP,
		ToController:   toTCP,
		ADSBCallsign:   ac.ADSBCallsign,
	})

	ac.STARSFlightPlan.HandoffTrackController = toTCP

	if _, fok := s.State.Controllers[fromTCP]; !fok {
		s.lg.Errorf("Unable to handoff %s: from controller %q not found", ac.ADSBCallsign, fromTCP)
	} else if _, tok := s.State.Controllers[toTCP]; !tok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", ac.ADSBCallsign, toTCP)
	}

	// Add them to the auto-accept map even if the target controller is
	// currently signed in; this way, if they sign off in the interim, we
	// still end up accepting it automatically.
	acceptDelay := 4 + rand.Intn(10)
	s.Handoffs[ac.ADSBCallsign] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) HandoffControl(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			// Immediately respond to the current controller that we're
			// changing frequency.
			sfp := ac.STARSFlightPlan
			if octrl, ok := s.State.Controllers[sfp.TrackingController]; ok {
				if sfp.TrackingController == tcp {
					radioTransmissions = append(radioTransmissions, av.RadioTransmission{
						Controller: sfp.ControllingController,
						Message:    "Unable, we are already on " + octrl.Frequency.String(),
						Type:       av.RadioTransmissionReadback,
					})
					return radioTransmissions
				}
				bye := rand.Sample("good day", "seeya")
				contact := rand.Sample("contact ", "over to ", "")
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
				ADSBCallsign:   ac.ADSBCallsign,
			})

			// Take away the current controller's ability to issue control
			// commands.
			sfp.ControllingController = ""

			// In 5-10 seconds, have the aircraft contact the new controller
			// (and give them control only then).
			wait := time.Duration(5+rand.Intn(10)) * time.Second
			s.enqueueControllerContact(ac.ADSBCallsign, sfp.TrackingController, wait)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.HandoffTrackController == tcp {
				return nil
			}
			if po, ok := s.PointOuts[ac.ADSBCallsign]; ok && po.ToController == tcp {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe

		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.STARSFlightPlan.ControllingController,
				ToController:   tcp,
				ADSBCallsign:   ac.ADSBCallsign,
			})

			ac.STARSFlightPlan.HandoffTrackController = ""
			ac.STARSFlightPlan.TrackingController = tcp

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, ac.ADSBCallsign)

			haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
				func(wp av.Waypoint) bool { return wp.TransferComms })
			if !haveTransferComms && !s.isActiveHumanController(ac.STARSFlightPlan.ControllingController) {
				// For a handoff from a virtual controller, cue up a delayed
				// contact message unless there's a point later in the route when
				// comms are to be transferred.
				wait := time.Duration(5+rand.Intn(10)) * time.Second
				s.enqueueControllerContact(ac.ADSBCallsign, tcp, wait)
			}

			return nil
		})
}

func (s *Sim) CancelHandoff(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, ac.ADSBCallsign)
			ac.STARSFlightPlan.HandoffTrackController = ""
			ac.STARSFlightPlan.RedirectedHandoff = RedirectedHandoff{}

			return nil
		})
}

func (s *Sim) RedirectHandoff(tcp string, callsign av.ADSBCallsign, controller string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			} else if octrl, ok := s.State.Controllers[controller]; !ok {
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
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			rh := &ac.STARSFlightPlan.RedirectedHandoff
			rh.OriginalOwner = ac.STARSFlightPlan.TrackingController
			ctrl := s.State.Controllers[tcp]
			if rh.ShouldFallbackToHandoff(tcp, octrl.Id()) {
				ac.STARSFlightPlan.HandoffTrackController = rh.Redirector[0]
				*rh = RedirectedHandoff{}
				return nil
			}
			rh.AddRedirector(ctrl)
			rh.RedirectedTo = octrl.Id()

			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(tcp string, callsign av.ADSBCallsign) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}

			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			rh := &ac.STARSFlightPlan.RedirectedHandoff
			if rh.RedirectedTo == tcp { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: rh.OriginalOwner,
					ToController:   tcp,
					ADSBCallsign:   ac.ADSBCallsign,
				})
				ac.STARSFlightPlan.ControllingController = tcp
				ac.STARSFlightPlan.HandoffTrackController = ""
				ac.STARSFlightPlan.TrackingController = rh.RedirectedTo
				*rh = RedirectedHandoff{}
			} else if rh.GetLastRedirector() == tcp { // Recall (only the last redirector is able to recall)
				if len(rh.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					rh.RedirectedTo = rh.Redirector[len(rh.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.STARSFlightPlan.HandoffTrackController = rh.Redirector[len(rh.Redirector)-1]
					*rh = RedirectedHandoff{}
				}
			}

			return nil
		})
}

func (s *Sim) ForceQL(tcp string, callsign av.ADSBCallsign, controller string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: tcp,
				ToController:   octrl.Id(),
				ADSBCallsign:   ac.ADSBCallsign,
			})

			return nil
		})
}

func (s *Sim) PointOut(fromTCP string, callsign av.ADSBCallsign, toTCP string) error {
	return s.dispatchCommand(fromTCP, callsign,
		func(tcp string, ac *Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			} else if ac.STARSFlightPlan.TrackingController != fromTCP {
				return av.ErrOtherControllerHasTrack
			} else if octrl, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
			} else if ctrl, ok := s.State.Controllers[fromTCP]; !ok {
				return av.ErrNoController
			} else if octrl.Facility != ctrl.Facility {
				// Can't point out to another STARS facility.
				return av.ErrInvalidController
			} else if toTCP == fromTCP {
				// Can't point out to ourself
				return av.ErrInvalidController
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(ac.ADSBCallsign, ctrl, octrl)
			return nil
		})
}

func (s *Sim) pointOut(callsign av.ADSBCallsign, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.Id(),
		ToController:   to.Id(),
		ADSBCallsign:   callsign,
	})

	acceptDelay := 4 + rand.Intn(10)
	s.PointOuts[callsign] = PointOut{
		FromController: from.Id(),
		ToController:   to.Id(),
		AcceptTime:     s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(tcp string, callsign av.ADSBCallsign) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}

			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].FromController,
				ADSBCallsign:   ac.ADSBCallsign,
			})
			if len(ac.STARSFlightPlan.PointOutHistory) < 20 {
				ac.STARSFlightPlan.PointOutHistory = append([]string{tcp}, ac.STARSFlightPlan.PointOutHistory...)
			} else {
				ac.STARSFlightPlan.PointOutHistory = ac.STARSFlightPlan.PointOutHistory[:19]
				ac.STARSFlightPlan.PointOutHistory = append([]string{tcp}, ac.STARSFlightPlan.PointOutHistory...)
			}

			delete(s.PointOuts, callsign)

			return nil
		})
}

func (s *Sim) RecallPointOut(tcp string, callsign av.ADSBCallsign) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.FromController != tcp {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].ToController,
				ADSBCallsign:   ac.ADSBCallsign,
			})

			delete(s.PointOuts, callsign)

			return nil
		})
}

func (s *Sim) RejectPointOut(tcp string, callsign av.ADSBCallsign) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].FromController,
				ADSBCallsign:   ac.ADSBCallsign,
			})

			delete(s.PointOuts, callsign)

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
	if dc, ok := s.State.DepartureController(ac.DepartureContactController, s.lg); ok && dc != tcp {
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

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(hdg.TCP, hdg.ADSBCallsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(tcp string, callsign av.ADSBCallsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(tcp string, callsign av.ADSBCallsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(tcp string, callsign av.ADSBCallsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(tcp string, callsign av.ADSBCallsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(tcp string, callsign av.ADSBCallsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.lg)
		})
}

func (s *Sim) ClearedApproach(tcp string, callsign av.ADSBCallsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(tcp string, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
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

	return s.dispatchControllingCommand(tcp, callsign,
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
	wait := time.Duration(10+rand.Intn(15)) * time.Second
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
	wait := time.Duration(5+rand.Intn(5)) * time.Second
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
					s.postRadioEvents(c.ADSBCallsign, r)

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

// pkg/sim/control.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

func (s *Sim) dispatchCommand(tcp string, callsign string,
	check func(tcp string, ac *av.Aircraft) error,
	cmd func(tcp string, ac *av.Aircraft) []av.RadioTransmission) error {
	if ac, ok := s.State.Aircraft[callsign]; !ok {
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

		s.lg.Info("dispatch_command", slog.String("callsign", ac.Callsign),
			slog.Any("prepost_aircraft", []av.Aircraft{preAc, *ac}),
			slog.Any("radio_transmissions", radioTransmissions))
		s.postRadioEvents(ac.Callsign, radioTransmissions)
		return nil
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(tcp string, callsign string,
	cmd func(tcp string, ac *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
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
func (s *Sim) dispatchTrackingCommand(tcp string, callsign string,
	cmd func(tcp string, ac *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
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

func (s *Sim) DeleteAircraft(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: fmt.Sprintf("%s deleted %s", tcp, ac.Callsign),
			})

			s.lg.Info("deleted aircraft", slog.String("callsign", ac.Callsign),
				slog.String("controller", tcp))

			s.deleteAircraft(ac)

			return nil
		})
}

func (s *Sim) deleteAircraft(ac *av.Aircraft) {
	delete(s.State.Aircraft, ac.Callsign)

	s.ERAMComputer.DeleteAircraft(ac)
	s.STARSComputer.DeleteAircraft(ac)
}

func (s *Sim) DeleteAllAircraft(tcp string) error {
	for cs := range s.State.Aircraft {
		// Only delete airborne aircraft; leave all of the ones at the
		// gate, etc., so we don't have a bubble of no departures for a
		// long time while the departure queues refill.
		if s.State.Aircraft[cs].IsAirborne() {
			if err := s.DeleteAircraft(tcp, cs); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Sim) ChangeSquawk(tcp, callsign string, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, sq, ac.Mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "squawk " + sq.String(),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) ChangeTransponderMode(tcp, callsign string, mode av.TransponderMode) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, ac.Squawk, mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "squawk " + strings.ToLower(mode.String()),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) Ident(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:     IdentEvent,
				Callsign: ac.Callsign,
			})

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: tcp,
				Message:    "ident",
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(tcp, callsign string, dir *math.CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan.GlobalLeaderLineDirection = dir
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				Callsign:            ac.Callsign,
				FromController:      callsign,
				LeaderLineDirection: dir,
			})
			return nil
		})
}

func (s *Sim) CreateFlightPlan(tcp string, ty av.STARSFlightPlanType, spec av.STARSFlightPlanSpecifier) (av.STARSFlightPlan, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp := spec.GetFlightPlan()

	if ac, ok := s.State.Aircraft[fp.ACID]; ok && ac.STARSFlightPlan != nil {
		return av.STARSFlightPlan{}, ErrDuplicateACID
	}
	if slices.ContainsFunc(s.State.FlightPlans,
		func(fp2 av.STARSFlightPlan) bool { return fp.ACID == fp2.ACID }) {
		return av.STARSFlightPlan{}, ErrDuplicateACID
	}

	switch ty {
	case av.LocalNonEnroute:
		return s.STARSComputer.CreateFlightPlan(fp)
	case av.LocalEnroute, av.RemoteEnroute:
		sq, err := s.ERAMComputer.CreateSquawk()
		if err != nil {
			return fp, err
		}
		fp.AssignedSquawk = sq
		return s.STARSComputer.CreateFlightPlan(fp)
	default:
		panic("unhandled STARSFlightPlanType")
	}
}

func (s *Sim) ModifyFlightPlan(tcp, callsign string, spec av.STARSFlightPlanSpecifier) (av.STARSFlightPlan, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ac, ok := s.State.Aircraft[callsign]; ok {
		if ac.IsUnassociated() {
			return av.STARSFlightPlan{}, ErrTrackIsNotActive
		}
		if ac.STARSFlightPlan.TrackingController != tcp {
			return av.STARSFlightPlan{}, av.ErrOtherControllerHasTrack
		}

		if ac.Mode == av.Altitude && !ac.STARSFlightPlan.InhibitModeCAltitudeDisplay &&
			spec.PilotReportedAltitude.GetOr(0) != 0 {
			// 5-166: must inhibit mode C display if we are getting altitude from the aircraft
			// Allow zero to clear it which various STARS commands do implicitly.
			return av.STARSFlightPlan{}, ErrIllegalFunction
		}

		// Modify assigned
		if spec.EntryFix.IsSet || spec.ExitFix.IsSet || spec.ETAOrPTD.IsSet {
			// These can only be set for non-active flight plans: 5-171
			return av.STARSFlightPlan{}, ErrTrackIsActive
		}
		if ac.STARSFlightPlan.HandoffTrackController != "" {
			return av.STARSFlightPlan{}, ErrTrackIsBeingHandedOff
		}

		if spec.InhibitModeCAltitudeDisplay.IsSet && !spec.InhibitModeCAltitudeDisplay.Get() &&
			ac.Mode == av.Altitude {
			// Clear pilot reported if toggled on and we have mode-C altitude
			ac.STARSFlightPlan.PilotReportedAltitude = 0
		}

		return ac.UpdateFlightPlan(spec), nil
	} else {
		// Modify pending
		if spec.AssignedAltitude.IsSet {
			return av.STARSFlightPlan{}, ErrTrackIsNotActive
		}

		return s.STARSComputer.ModifyFlightPlan(spec)
	}
}

func (s *Sim) AssociateFlightPlan(callsign string, spec av.STARSFlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.CreateQuick {
		base := s.State.STARSFacilityAdaptation.FlightPlan.QuickACID
		spec.ACID.Set(base + fmt.Sprintf("%02d", s.State.QuickFlightPlanIndex%100))
		s.State.QuickFlightPlanIndex++
	}

	return s.dispatchCommand(spec.InitialController.Get(), callsign,
		func(tcp string, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.IsAssociated() {
				return av.ErrOtherControllerHasTrack
			}
			if !spec.ACID.IsSet && s.STARSComputer.lookupFlightPlanByACID(callsign) == nil {
				return ErrNoMatchingFlight
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			fp := spec.GetFlightPlan()
			fp.TrackingController = tcp // FIXME: vs initial controller...
			if _, err := s.STARSComputer.CreateFlightPlan(fp); err != nil {
				s.lg.Warnf("%s: error creating flight plan: %v", fp.ACID, err)
			}
			ac.AssociateFlightPlan(s.STARSComputer.takeFlightPlanByACID(fp.ACID))

			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: tcp,
			})

			return nil
		})
}

func (s *Sim) ActivateFlightPlan(tcp, trackCallsign, fpACID string, spec *av.STARSFlightPlanSpecifier) error {
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
		func(tcp string, ac *av.Aircraft) error {
			if ac.IsAssociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.TrackingController != "" {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan = fp
			// TODO: needed?
			ac.STARSFlightPlan.TrackingController = tcp
			ac.STARSFlightPlan.ControllingController = tcp // HAX
			return nil
		})
}

func (s *Sim) DeleteFlightPlan(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	err := s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.STARSFlightPlan = nil

			s.ERAMComputer.DeleteAircraft(ac)
			s.STARSComputer.DeleteAircraft(ac)

			return nil
		})

	if err != nil {
		if fp := s.STARSComputer.takeFlightPlanByACID(callsign); fp != nil {
			return nil
		}
	}
	return err
}

func (s *Sim) HandoffTrack(tcp, callsign, toTCP string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.handoffTrack(tcp, toTCP, ac)
			return nil
		})
}

func (s *Sim) handoffTrack(fromTCP, toTCP string, ac *av.Aircraft) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fromTCP,
		ToController:   toTCP,
		Callsign:       ac.Callsign,
	})

	ac.STARSFlightPlan.HandoffTrackController = toTCP

	if _, fok := s.State.Controllers[fromTCP]; !fok {
		s.lg.Errorf("Unable to handoff %s: from controller %q not found", ac.Callsign, fromTCP)
	} else if _, tok := s.State.Controllers[toTCP]; !tok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", ac.Callsign, toTCP)
	}

	// Add them to the auto-accept map even if the target controller is
	// currently signed in; this way, if they sign off in the interim, we
	// still end up accepting it automatically.
	acceptDelay := 4 + rand.Intn(10)
	s.Handoffs[ac.Callsign] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) HandoffControl(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
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
				Callsign:       ac.Callsign,
			})

			// Take away the current controller's ability to issue control
			// commands.
			sfp.ControllingController = ""

			// In 5-10 seconds, have the aircraft contact the new controller
			// (and give them control only then).
			wait := time.Duration(5+rand.Intn(10)) * time.Second
			s.enqueueControllerContact(ac.Callsign, sfp.TrackingController, wait)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}
			if ac.STARSFlightPlan.HandoffTrackController == tcp {
				return nil
			}
			if po, ok := s.PointOuts[ac.Callsign]; ok && po.ToController == tcp {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe

		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.STARSFlightPlan.ControllingController,
				ToController:   tcp,
				Callsign:       ac.Callsign,
			})

			ac.STARSFlightPlan.HandoffTrackController = ""
			ac.STARSFlightPlan.TrackingController = tcp

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, ac.Callsign)

			haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
				func(wp av.Waypoint) bool { return wp.TransferComms })
			if !haveTransferComms && !s.isActiveHumanController(ac.STARSFlightPlan.ControllingController) {
				// For a handoff from a virtual controller, cue up a delayed
				// contact message unless there's a point later in the route when
				// comms are to be transferred.
				wait := time.Duration(5+rand.Intn(10)) * time.Second
				s.enqueueControllerContact(ac.Callsign, tcp, wait)
			}

			return nil
		})
}

func (s *Sim) CancelHandoff(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, ac.Callsign)
			ac.STARSFlightPlan.HandoffTrackController = ""
			ac.STARSFlightPlan.RedirectedHandoff = av.RedirectedHandoff{}

			return nil
		})
}

func (s *Sim) RedirectHandoff(tcp, callsign, controller string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			rh := &ac.STARSFlightPlan.RedirectedHandoff
			rh.OriginalOwner = ac.STARSFlightPlan.TrackingController
			ctrl := s.State.Controllers[tcp]
			if rh.ShouldFallbackToHandoff(tcp, octrl.Id()) {
				ac.STARSFlightPlan.HandoffTrackController = rh.Redirector[0]
				*rh = av.RedirectedHandoff{}
				return nil
			}
			rh.AddRedirector(ctrl)
			rh.RedirectedTo = octrl.Id()

			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(tcp, callsign string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}

			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			rh := &ac.STARSFlightPlan.RedirectedHandoff
			if rh.RedirectedTo == tcp { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: rh.OriginalOwner,
					ToController:   tcp,
					Callsign:       ac.Callsign,
				})
				ac.STARSFlightPlan.ControllingController = tcp
				ac.STARSFlightPlan.HandoffTrackController = ""
				ac.STARSFlightPlan.TrackingController = rh.RedirectedTo
				*rh = av.RedirectedHandoff{}
			} else if rh.GetLastRedirector() == tcp { // Recall (only the last redirector is able to recall)
				if len(rh.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					rh.RedirectedTo = rh.Redirector[len(rh.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.STARSFlightPlan.HandoffTrackController = rh.Redirector[len(rh.Redirector)-1]
					*rh = av.RedirectedHandoff{}
				}
			}

			return nil
		})
}

func (s *Sim) ForceQL(tcp, callsign, controller string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: tcp,
				ToController:   octrl.Id(),
				Callsign:       ac.Callsign,
			})

			return nil
		})
}

func (s *Sim) PointOut(fromTCP, callsign, toTCP string) error {
	return s.dispatchCommand(fromTCP, callsign,
		func(tcp string, ac *av.Aircraft) error {
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ctrl := s.State.Controllers[fromTCP]
			octrl := s.State.Controllers[toTCP]
			s.pointOut(ac.Callsign, ctrl, octrl)
			return nil
		})
}

func (s *Sim) pointOut(callsign string, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.Id(),
		ToController:   to.Id(),
		Callsign:       callsign,
	})

	acceptDelay := 4 + rand.Intn(10)
	s.PointOuts[callsign] = PointOut{
		FromController: from.Id(),
		ToController:   to.Id(),
		AcceptTime:     s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(tcp, callsign string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			if ac.IsUnassociated() {
				return ErrTrackIsNotActive
			}

			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
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

func (s *Sim) RecallPointOut(tcp, callsign string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.FromController != tcp {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].ToController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			return nil
		})
}

func (s *Sim) RejectPointOut(tcp, callsign string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != tcp {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			return nil
		})
}

func (s *Sim) ReleaseDeparture(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ac, ok := s.State.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	if dc, ok := s.State.DepartureController(ac, s.lg); ok && dc != tcp {
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

func (s *Sim) AssignAltitude(tcp, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

type HeadingArgs struct {
	TCP          string
	Callsign     string
	Heading      int
	Present      bool
	LeftDegrees  int
	RightDegrees int
	Turn         av.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(hdg.TCP, hdg.Callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
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

func (s *Sim) AssignSpeed(tcp, callsign string, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(tcp, callsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(tcp, callsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(tcp, callsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(tcp, callsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(tcp, callsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AtFixCleared(fix, approach)
		})
}

func (s *Sim) ExpectApproach(tcp, callsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.State.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return av.ErrUnknownAirport
		}
	}

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.lg)
		})
}

func (s *Sim) ClearedApproach(tcp, callsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			if straightIn {
				return ac.ClearedStraightInApproach(approach)
			} else {
				return ac.ClearedApproach(approach, s.lg)
			}
		})
}

func (s *Sim) InterceptLocalizer(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				resp[i].Type = av.RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ContactTower(s.lg)
		})
}

///////////////////////////////////////////////////////////////////////////
// Deferred operations

type FutureControllerContact struct {
	Callsign string
	TCP      string
	Time     time.Time
}

func (s *Sim) enqueueControllerContact(callsign, tcp string, wait time.Duration) {
	s.FutureControllerContacts = append(s.FutureControllerContacts,
		FutureControllerContact{Callsign: callsign, TCP: tcp, Time: s.State.SimTime.Add(wait)})
}

type FutureOnCourse struct {
	Callsign string
	Time     time.Time
}

func (s *Sim) enqueueDepartOnCourse(callsign string) {
	wait := time.Duration(10+rand.Intn(15)) * time.Second
	s.FutureOnCourse = append(s.FutureOnCourse,
		FutureOnCourse{Callsign: callsign, Time: s.State.SimTime.Add(wait)})
}

type FutureChangeSquawk struct {
	Callsign string
	Code     av.Squawk
	Mode     av.TransponderMode
	Time     time.Time
}

func (s *Sim) enqueueTransponderChange(callsign string, code av.Squawk, mode av.TransponderMode) {
	wait := time.Duration(5+rand.Intn(5)) * time.Second
	s.FutureSquawkChanges = append(s.FutureSquawkChanges,
		FutureChangeSquawk{Callsign: callsign, Code: code, Mode: mode, Time: s.State.SimTime.Add(wait)})
}

func (s *Sim) processEnqueued() {
	s.FutureControllerContacts = util.FilterSliceInPlace(s.FutureControllerContacts,
		func(c FutureControllerContact) bool {
			if s.State.SimTime.After(c.Time) {
				if ac, ok := s.State.Aircraft[c.Callsign]; ok && ac.IsAssociated() {
					ac.STARSFlightPlan.ControllingController = c.TCP
					r := []av.RadioTransmission{av.RadioTransmission{
						Controller: c.TCP,
						Message:    ac.ContactMessage(s.ReportingPoints),
						Type:       av.RadioTransmissionContact,
					}}
					s.postRadioEvents(c.Callsign, r)

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					_, human := s.humanControllers[ac.STARSFlightPlan.ControllingController]
					if ac.IsDeparture() && !human {
						s.enqueueDepartOnCourse(ac.Callsign)
					}
				}
				return false // remove it from the slice
			}
			return true // keep it in the slice
		})

	s.FutureOnCourse = util.FilterSliceInPlace(s.FutureOnCourse,
		func(oc FutureOnCourse) bool {
			if s.State.SimTime.After(oc.Time) {
				if ac, ok := s.State.Aircraft[oc.Callsign]; ok {
					s.lg.Info("departing on course", slog.String("callsign", ac.Callsign),
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
				if ac, ok := s.State.Aircraft[fcs.Callsign]; ok {
					ac.Squawk = fcs.Code
					ac.Mode = fcs.Mode
				}
				return false
			}
			return true
		})
}

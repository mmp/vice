// pkg/sim/control.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *av.Controller, ac *av.Aircraft) error,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.State.Aircraft[callsign]; !ok {
		return av.ErrNoAircraftForCallsign
	} else {
		// TODO(mtrokel): this needs to be updated for the STARS tracking stuff
		if sc.Id == "Observer" {
			return av.ErrOtherControllerHasTrack
		}

		ctrl := s.State.Controllers[sc.Id]
		if ctrl == nil {
			s.lg.Error("controller unknown", slog.String("controller", sc.Id),
				slog.Any("world_controllers", s.State.Controllers))
			return av.ErrNoController
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			preAc := *ac
			radioTransmissions := cmd(ctrl, ac)

			s.lg.Info("dispatch_command", slog.String("callsign", ac.Callsign),
				slog.Any("prepost_aircraft", []av.Aircraft{preAc, *ac}),
				slog.Any("radio_transmissions", radioTransmissions))
			s.postRadioEvents(ac.Callsign, radioTransmissions)
			return nil
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// TODO(mtrokel): this needs to be updated for the STARS tracking stuff
			if ac.ControllingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}

			/*
				trk := s.State.STARSComputer().TrackInformation[ac.Callsign]
				if trk != nil || trk.TrackOwner != ctrl.Callsign {
					return av.ErrOtherControllerHasTrack
				}
			*/

			return nil
		},
		cmd)
}

func (s *Sim) DeleteAircraft(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if s.State.IsIntraFacility(ac) {
				s.TotalDepartures--
				s.TotalArrivals--
			} else if s.State.IsDeparture(ac) {
				s.TotalDepartures--
			} else if s.State.IsArrival(ac) {
				s.TotalArrivals--
			} else {
				s.TotalOverflights--
			}

			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: fmt.Sprintf("%s deleted %s", ctrl.Id(), ac.Callsign),
			})

			s.lg.Info("deleted aircraft", slog.String("callsign", ac.Callsign),
				slog.String("controller", ctrl.Id()))

			s.State.DeleteAircraft(ac)

			return nil
		})
}

func (s *Sim) DeleteAllAircraft(token string) error {
	for cs := range s.State.Aircraft {
		if err := s.DeleteAircraft(token, cs); err != nil {
			return err
		}
	}
	for ap := range s.DeparturePool {
		s.DeparturePool[ap] = nil
	}
	for _, rwys := range s.LastDeparture {
		clear(rwys)
	}

	return nil
}

func (s *Sim) SetScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.Scratchpad = scratchpad

			err := s.State.ERAMComputers.SetScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) SetSecondaryScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.SecondaryScratchpad = scratchpad

			err := s.State.ERAMComputers.SetSecondaryScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetSecondaryScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) ChangeSquawk(token, callsign string, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, sq, ac.Mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "squawk " + sq.String(),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) ChangeTransponderMode(token, callsign string, mode av.TransponderMode) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, ac.Squawk, mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "squawk " + strings.ToLower(mode.String()),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) Ident(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:     IdentEvent,
				Callsign: ac.Callsign,
			})

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "ident",
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(token, callsign string, dir *math.CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != c.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.GlobalLeaderLineDirection = dir
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				Callsign:            ac.Callsign,
				FromController:      callsign,
				LeaderLineDirection: dir,
			})
			return nil
		})
}

func (s *Sim) AutoAssociateFP(token, callsign string, fp *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AutoAssociateFP(ac, fp)
			return nil
		})

}

func (s *Sim) CreateUnsupportedTrack(token, callsign string, ut *UnsupportedTrack) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AddUnsupportedTrack(*ut)
			return nil
		})
}

func (s *Sim) UploadFlightPlan(token string, Type int, plan *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ctrl := s.State.Controllers[s.controllers[token].Id]
	if ctrl == nil {
		s.lg.Errorf("%s: controller unknown", s.controllers[token].Id)
		return ErrUnknownController
	}
	eram, stars, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
	if err != nil {
		return err

	}

	switch Type {
	case av.LocalNonEnroute:
		stars.AddFlightPlan(plan)
	case av.LocalEnroute, av.RemoteEnroute:
		eram.AddFlightPlan(plan)
	}

	return nil
}

func (s *Sim) InitiateTrack(token, callsign string, fp *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return av.ErrOtherControllerHasTrack
			}
			if ac.Squawk == 0o1200 {
				return av.ErrNoFlightPlan
			}
			/*
				if s.State.STARSComputer().TrackInformation[ac.Callsign] != nil {
					return av.ErrOtherControllerHasTrack
				}
			*/
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// If they have already contacted departure, then initiating
			// track gives control as well; otherwise ControllingController
			// is left unset until contact.
			haveControl := ac.DepartureContactAltitude == 0

			ac.TrackingController = ctrl.Id()
			if haveControl {
				ac.ControllingController = ctrl.Id()
			}

			if err := s.State.STARSComputer().InitiateTrack(callsign, ctrl.Id(), fp, haveControl); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}
			if err := s.State.ERAMComputer().InitiateTrack(callsign, ctrl.Id(), fp); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}

			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: ctrl.Id(),
			})

			return nil
		})
}

func (s *Sim) DropTrack(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.TrackingController = ""
			ac.ControllingController = ""

			if err := s.State.STARSComputer().DropTrack(ac); err != nil {
				//s.lg.Errorf("STARS DropTrack: %v", err)
			}
			if err := s.State.ERAMComputer().DropTrack(ac); err != nil {
				//s.lg.Errorf("ERAM DropTrack: %v", err)
			}

			s.eventStream.Post(Event{
				Type:           DroppedTrackEvent,
				Callsign:       ac.Callsign,
				FromController: ctrl.Id(),
			})
			return nil
		})
}

func (s *Sim) HandoffTrack(token, callsign, controller string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			} else if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
				/*
					} else if trk := s.State.STARSComputer().TrackInformation[ac.Callsign]; trk == nil {
						// no one is tracking it
						return av.ErrOtherControllerHasTrack
					} else if trk.TrackOwner != ctrl.Callsign {
						return av.ErrOtherControllerHasTrack
				*/
			} else if octrl.Id() == ctrl.Id() {
				// Can't handoff to ourself
				return av.ErrInvalidController
			} else {
				// Disallow handoff if there's a beacon code mismatch.
				squawkingSPC, _ := ac.Squawk.IsSPC()
				if trk := s.State.STARSComputer().TrackInformation[ac.Callsign]; trk != nil && trk.FlightPlan != nil {
					if ac.Squawk != trk.FlightPlan.AssignedSquawk && !squawkingSPC {
						return ErrBeaconMismatch
					}
				} else if ac.Squawk != ac.FlightPlan.AssignedSquawk && !squawkingSPC { // workaround pending NAS fixes
					return ErrBeaconMismatch
				}
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.handoffTrack(ctrl.Id(), octrl.Id(), ac.Callsign)
			return nil
		})
}

func (s *Sim) handoffTrack(fromTCP, toTCP string, callsign string) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fromTCP,
		ToController:   toTCP,
		Callsign:       callsign,
	})

	s.State.Aircraft[callsign].HandoffTrackController = toTCP

	if from, fok := s.State.Controllers[fromTCP]; !fok {
		s.lg.Errorf("Unable to handoff %s: from controller %q not found", callsign, fromTCP)
	} else if to, tok := s.State.Controllers[toTCP]; !tok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", callsign, toTCP)
	} else if err := s.State.STARSComputer().HandoffTrack(callsign, from, to, s.State.SimTime); err != nil {
		//s.lg.Errorf("HandoffTrack: %v", err)
	}

	// Add them to the auto-accept map even if the target is
	// covered; this way, if they sign off in the interim, we still
	// end up accepting it automatically.
	acceptDelay := 4 + rand.Intn(10)
	s.Handoffs[callsign] = Handoff{
		Time: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) HandoffControl(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.ControllingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			// Immediately respond to the current controller that we're
			// changing frequency.
			if octrl, ok := s.State.Controllers[ac.TrackingController]; ok {
				if octrl.Frequency == ctrl.Frequency {
					radioTransmissions = append(radioTransmissions, av.RadioTransmission{
						Controller: ac.ControllingController,
						Message:    "Unable, we are already on " + octrl.Frequency.String(),
						Type:       av.RadioTransmissionReadback,
					})
					return radioTransmissions
				}
				bye := rand.Sample("good day", "seeya")
				contact := rand.Sample("contact ", "over to ", "")
				goodbye := contact + octrl.RadioName + " on " + octrl.Frequency.String() + ", " + bye
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    goodbye,
					Type:       av.RadioTransmissionReadback,
				})
			} else {
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    "goodbye",
					Type:       av.RadioTransmissionReadback,
				})
			}

			s.eventStream.Post(Event{
				Type:           HandoffControlEvent,
				FromController: ac.ControllingController,
				ToController:   ac.TrackingController,
				Callsign:       ac.Callsign,
			})

			if err := s.State.STARSComputer().HandoffControl(callsign, ac.TrackingController); err != nil {
				//s.lg.Errorf("HandoffControl: %v", err)
			}

			// Take away the current controller's ability to issue control
			// commands.
			ac.ControllingController = ""

			// In 5-10 seconds, have the aircraft contact the new controller
			// (and give them control only then).
			s.enqueueControllerContact(ac.Callsign, ac.TrackingController)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			/*
				trk := s.State.STARSComputer().TrackInformation[ac.Callsign]
				if trk == nil || trk.HandoffController != ctrl.Callsign {
					return av.ErrNotBeingHandedOffToMe
				}
			*/

			if ac.HandoffTrackController == ctrl.Id() {
				return nil
			}
			if po, ok := s.PointOuts[ac.Callsign]; ok && po.ToController == ctrl.Id() {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe

		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Id(),
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			ac.TrackingController = ctrl.Id()

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, ac.Callsign)

			if err := s.State.STARSComputer().AcceptHandoff(ac, ctrl, s.State.Controllers,
				s.State.STARSFacilityAdaptation, s.State.SimTime); err != nil {
				//s.lg.Errorf("AcceptHandoff: %v", err)
			}

			if !s.controllerIsSignedIn(ac.ControllingController) {
				// Don't wait for a frequency change instruction for
				// handoffs from virtual, but wait a bit before the
				// aircraft calls in at which point we have control.
				s.enqueueControllerContact(ac.Callsign, ctrl.Id())
			}

			return nil
		})
}

func (s *Sim) CancelHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, ac.Callsign)
			ac.HandoffTrackController = ""
			ac.RedirectedHandoff = av.RedirectedHandoff{}

			err := s.State.STARSComputer().CancelHandoff(ac, ctrl, s.State.Controllers, s.State.SimTime)
			if err != nil {
				//s.lg.Errorf("CancelHandoff: %v", err)
			}

			return nil
		})
}

func (s *Sim) RedirectHandoff(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Id() == ctrl.Id() || octrl.Id() == ac.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return av.ErrInvalidController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return av.ErrInvalidFacility
				/*
					} else if trk := s.State.STARSComputer().TrackInformation[callsign]; trk != nil && octrl.Callsign == trk.TrackOwner {
						return av.ErrInvalidController
				*/
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			ac.RedirectedHandoff.OriginalOwner = ac.TrackingController
			if ac.RedirectedHandoff.ShouldFallbackToHandoff(ctrl.Id(), octrl.Id()) {
				ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[0]
				ac.RedirectedHandoff = av.RedirectedHandoff{}
				return nil
			}
			ac.RedirectedHandoff.AddRedirector(ctrl)
			ac.RedirectedHandoff.RedirectedTo = octrl.Id()

			s.State.STARSComputer().RedirectHandoff(ac, ctrl, octrl)

			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if ac.RedirectedHandoff.RedirectedTo == ctrl.Id() { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: ac.RedirectedHandoff.OriginalOwner,
					ToController:   ctrl.Id(),
					Callsign:       ac.Callsign,
				})
				ac.ControllingController = ctrl.Id()
				ac.HandoffTrackController = ""
				ac.TrackingController = ac.RedirectedHandoff.RedirectedTo
				ac.RedirectedHandoff = av.RedirectedHandoff{}
			} else if ac.RedirectedHandoff.GetLastRedirector() == ctrl.Id() { // Recall (only the last redirector is able to recall)
				if len(ac.RedirectedHandoff.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					ac.RedirectedHandoff.RedirectedTo = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
					ac.RedirectedHandoff = av.RedirectedHandoff{}
				}
			}

			err := s.State.STARSComputer().AcceptRedirectedHandoff(ac, ctrl)
			if err != nil {
				//s.lg.Errorf("AcceptRedirectedHandoff: %v", err)
			}

			return nil
		})
}

func (s *Sim) ForceQL(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: ctrl.Id(),
				ToController:   octrl.Id(),
				Callsign:       ac.Callsign,
			})

			return nil
		})
}

func (s *Sim) PointOut(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			} else if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Facility != ctrl.Facility {
				// Can't point out to another STARS facility.
				return av.ErrInvalidController
			} else if octrl.Id() == ctrl.Id() {
				// Can't point out to ourself
				return av.ErrInvalidController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
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

	if err := s.State.STARSComputer().PointOut(callsign, to.Id()); err != nil {
		//s.lg.Errorf("PointOut: %v", err)
	}

	acceptDelay := 4 + rand.Intn(10)
	s.PointOuts[callsign] = PointOut{
		FromController: from.Id(),
		ToController:   to.Id(),
		AcceptTime:     s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != ctrl.Id() {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})
			if len(ac.PointOutHistory) < 20 {
				ac.PointOutHistory = append([]string{ctrl.Id()}, ac.PointOutHistory...)
			} else {
				ac.PointOutHistory = ac.PointOutHistory[:19]
				ac.PointOutHistory = append([]string{ctrl.Id()}, ac.PointOutHistory...)
			}

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().AcknowledgePointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("AcknowledgePointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) RecallPointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.FromController != ctrl.Id() {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].ToController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().RecallPointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("RecallPointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) RejectPointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != ctrl.Id() {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().RejectPointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("RejectPointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) ToggleSPCOverride(token, callsign, spc string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.ToggleSPCOverride(spc)
			return nil
		})
}

func (s *Sim) ReleaseDeparture(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// FIXME: rewrite to use dispatchCommand()
	sc, ok := s.controllers[token]
	if !ok {
		return ErrInvalidControllerToken
	}

	ac, ok := s.State.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	if s.State.DepartureController(ac, s.lg) != sc.Id {
		return ErrInvalidDepartureController
	}

	stars := s.State.STARSComputer()
	if err := stars.ReleaseDeparture(callsign); err == nil {
		ac.Released = true
		ac.ReleaseTime = s.State.SimTime
		return nil
	} else {
		return err
	}
}

func (s *Sim) AssignAltitude(token, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

func (s *Sim) SetTemporaryAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.TempAltitude = altitude
			return nil
		})
}

func (s *Sim) SetPilotReportedAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// Must own the track
			if ac.TrackingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}
			if ac.Mode == av.Altitude && !ac.InhibitModeCAltitudeDisplay {
				// 5-166: must inhibit mode C display if we are getting altitude from the aircraft
				return ErrIllegalFunction
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.PilotReportedAltitude = altitude
			return nil
		})
}

func (s *Sim) ToggleDisplayModeCAltitude(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// 5-167
			ac.InhibitModeCAltitudeDisplay = !ac.InhibitModeCAltitudeDisplay

			if !ac.InhibitModeCAltitudeDisplay && ac.Mode == av.Altitude {
				// Clear pilot reported if toggled on and we have mode-C altitude
				ac.PilotReportedAltitude = 0
			}
			return nil
		})
}

type HeadingArgs struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            av.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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

func (s *Sim) AssignSpeed(token, callsign string, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(token, callsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(token, callsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(token, callsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(token, callsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(token, callsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AtFixCleared(fix, approach)
		})
}

func (s *Sim) ExpectApproach(token, callsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.State.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return av.ErrUnknownAirport
		}
	}

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.lg)
		})
}

func (s *Sim) ClearedApproach(token, callsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if straightIn {
				return ac.ClearedStraightInApproach(approach)
			} else {
				return ac.ClearedApproach(approach, s.lg)
			}
		})
}

func (s *Sim) InterceptLocalizer(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				resp[i].Type = av.RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
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

func (s *Sim) enqueueControllerContact(callsign, tcp string) {
	wait := time.Duration(5+rand.Intn(10)) * time.Second
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
	s.FutureControllerContacts = util.FilterSlice(s.FutureControllerContacts,
		func(c FutureControllerContact) bool {
			if s.State.SimTime.After(c.Time) {
				if ac, ok := s.State.Aircraft[c.Callsign]; ok {
					ac.ControllingController = c.TCP
					r := []av.RadioTransmission{av.RadioTransmission{
						Controller: c.TCP,
						Message:    ac.ContactMessage(s.ReportingPoints),
						Type:       av.RadioTransmissionContact,
					}}
					s.postRadioEvents(c.Callsign, r)

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					ctrl := s.State.Controllers[ac.ControllingController]
					if s.State.IsDeparture(ac) && ctrl != nil && !ctrl.IsHuman {
						s.enqueueDepartOnCourse(ac.Callsign)
					}
				}
				return false // remove it from the slice
			}
			return true // keep it in the slice
		})

	s.FutureOnCourse = util.FilterSlice(s.FutureOnCourse,
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

	s.FutureSquawkChanges = util.FilterSlice(s.FutureSquawkChanges,
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

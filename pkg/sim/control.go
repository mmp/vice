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
		if err := check(tcp, ac); err != nil {
			return err
		} else {
			preAc := *ac
			radioTransmissions := cmd(tcp, ac)

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
func (s *Sim) dispatchControllingCommand(tcp string, callsign string,
	cmd func(tcp string, ac *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.ControllingController != tcp && !s.Instructors[tcp] {
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
			if ac.TrackingController != tcp && !s.Instructors[tcp] {
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

			s.State.DeleteAircraft(ac)

			return nil
		})
}

func (s *Sim) DeleteAllAircraft(tcp string) error {
	for cs := range s.State.Aircraft {
		if err := s.DeleteAircraft(tcp, cs); err != nil {
			return err
		}
	}
	for _, rwyState := range s.DepartureState {
		for _, state := range rwyState {
			state.reset(s)
		}
	}

	return nil
}

func (s *Sim) SetScratchpad(tcp, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.Scratchpad = scratchpad

			ctrl := s.State.Controllers[tcp]
			err := s.State.ERAMComputers.SetScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) SetSecondaryScratchpad(tcp, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.SecondaryScratchpad = scratchpad

			ctrl := s.State.Controllers[tcp]
			err := s.State.ERAMComputers.SetSecondaryScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetSecondaryScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
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

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
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

func (s *Sim) AutoAssociateFP(tcp, callsign string, fp *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			ctrl := s.State.Controllers[tcp]
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ctrl := s.State.Controllers[tcp]
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AutoAssociateFP(ac, fp)
			return nil
		})

}

func (s *Sim) CreateUnsupportedTrack(tcp, callsign string, ut *UnsupportedTrack) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			ctrl := s.State.Controllers[tcp]
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ctrl := s.State.Controllers[tcp]
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AddUnsupportedTrack(*ut)
			return nil
		})
}

func (s *Sim) UploadFlightPlan(tcp string, planType int, plan *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ctrl := s.State.Controllers[tcp]
	eram, stars, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
	if err != nil {
		return err

	}

	switch planType {
	case av.LocalNonEnroute:
		stars.AddFlightPlan(plan)
	case av.LocalEnroute, av.RemoteEnroute:
		eram.AddFlightPlan(plan)
	}

	return nil
}

func (s *Sim) InitiateTrack(tcp, callsign string, fp *av.STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			// If they have already contacted departure, then initiating
			// track gives control as well; otherwise ControllingController
			// is left unset until contact.
			haveControl := ac.DepartureContactAltitude == 0

			ac.TrackingController = tcp
			if haveControl {
				ac.ControllingController = tcp
			}

			if err := s.State.STARSComputer().InitiateTrack(callsign, tcp, fp, haveControl); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}
			if err := s.State.ERAMComputer().InitiateTrack(callsign, tcp, fp); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}

			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: tcp,
			})

			return nil
		})
}

func (s *Sim) DropTrack(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
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
				FromController: tcp,
			})
			return nil
		})
}

func (s *Sim) HandoffTrack(tcp, callsign, toTCP string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.TrackingController != tcp {
				return av.ErrOtherControllerHasTrack
			} else if _, ok := s.State.Controllers[toTCP]; !ok {
				return av.ErrNoController
				/*
					} else if trk := s.State.STARSComputer().TrackInformation[ac.Callsign]; trk == nil {
						// no one is tracking it
						return av.ErrOtherControllerHasTrack
					} else if trk.TrackOwner != ctrl.Callsign {
						return av.ErrOtherControllerHasTrack
				*/
			} else if toTCP == tcp {
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			s.handoffTrack(tcp, toTCP, ac.Callsign)
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

	ac := s.State.Aircraft[callsign]
	ac.HandoffTrackController = toTCP

	if from, fok := s.State.Controllers[fromTCP]; !fok {
		s.lg.Errorf("Unable to handoff %s: from controller %q not found", callsign, fromTCP)
	} else if to, tok := s.State.Controllers[toTCP]; !tok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", callsign, toTCP)
	} else if err := s.State.STARSComputer().HandoffTrack(callsign, from, to, s.State.SimTime); err != nil {
		//s.lg.Errorf("HandoffTrack: %v", err)
	}

	// Add them to the auto-accept map even if the target controller is
	// currently signed in covered; this way, if they sign off in the
	// interim, we still end up accepting it automatically.
	acceptDelay := 4 + rand.Intn(10)
	s.Handoffs[callsign] = Handoff{
		AutoAcceptTime: s.State.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) HandoffControl(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.ControllingController != tcp {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			// Immediately respond to the current controller that we're
			// changing frequency.
			if octrl, ok := s.State.Controllers[ac.TrackingController]; ok {
				if ac.TrackingController == tcp {
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
			wait := time.Duration(5+rand.Intn(10)) * time.Second
			s.enqueueControllerContact(ac.Callsign, ac.TrackingController, wait)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if ac.HandoffTrackController == tcp {
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
				FromController: ac.ControllingController,
				ToController:   tcp,
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			ac.TrackingController = tcp

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, ac.Callsign)

			if ctrl, ok := s.State.Controllers[tcp]; ok {
				if err := s.State.STARSComputer().AcceptHandoff(ac, ctrl, s.State.Controllers,
					s.State.STARSFacilityAdaptation, s.State.SimTime); err != nil {
					//s.lg.Errorf("AcceptHandoff: %v", err)
				}
			}

			haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
				func(wp av.Waypoint) bool { return wp.TransferComms })
			if !haveTransferComms && !s.isActiveHumanController(ac.ControllingController) {
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
			ac.HandoffTrackController = ""
			ac.RedirectedHandoff = av.RedirectedHandoff{}

			if ctrl, ok := s.State.Controllers[tcp]; ok {
				err := s.State.STARSComputer().CancelHandoff(ac, ctrl, s.State.Controllers, s.State.SimTime)
				if err != nil {
					//s.lg.Errorf("CancelHandoff: %v", err)
				}
			}

			return nil
		})
}

func (s *Sim) RedirectHandoff(tcp, callsign, controller string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Id() == tcp || octrl.Id() == ac.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return av.ErrInvalidController
			} else if ctrl, ok := s.State.Controllers[tcp]; !ok {
				return ErrUnknownController
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
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			ac.RedirectedHandoff.OriginalOwner = ac.TrackingController
			ctrl := s.State.Controllers[tcp]
			if ac.RedirectedHandoff.ShouldFallbackToHandoff(tcp, octrl.Id()) {
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

func (s *Sim) AcceptRedirectedHandoff(tcp, callsign string) error {
	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			if ac.RedirectedHandoff.RedirectedTo == tcp { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: ac.RedirectedHandoff.OriginalOwner,
					ToController:   tcp,
					Callsign:       ac.Callsign,
				})
				ac.ControllingController = tcp
				ac.HandoffTrackController = ""
				ac.TrackingController = ac.RedirectedHandoff.RedirectedTo
				ac.RedirectedHandoff = av.RedirectedHandoff{}
			} else if ac.RedirectedHandoff.GetLastRedirector() == tcp { // Recall (only the last redirector is able to recall)
				if len(ac.RedirectedHandoff.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					ac.RedirectedHandoff.RedirectedTo = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
					ac.RedirectedHandoff = av.RedirectedHandoff{}
				}
			}

			if ctrl, ok := s.State.Controllers[tcp]; ok {
				err := s.State.STARSComputer().AcceptRedirectedHandoff(ac, ctrl)
				if err != nil {
					//s.lg.Errorf("AcceptRedirectedHandoff: %v", err)
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
			if ac.TrackingController != fromTCP {
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

func (s *Sim) AcknowledgePointOut(tcp, callsign string) error {
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
				Type:           AcknowledgedPointOutEvent,
				FromController: tcp,
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})
			if len(ac.PointOutHistory) < 20 {
				ac.PointOutHistory = append([]string{tcp}, ac.PointOutHistory...)
			} else {
				ac.PointOutHistory = ac.PointOutHistory[:19]
				ac.PointOutHistory = append([]string{tcp}, ac.PointOutHistory...)
			}

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().AcknowledgePointOut(ac.Callsign, tcp)
			if err != nil {
				//s.lg.Errorf("AcknowledgePointOut: %v", err)
			}

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

			err := s.State.STARSComputer().RecallPointOut(ac.Callsign, tcp)
			if err != nil {
				//s.lg.Errorf("RecallPointOut: %v", err)
			}

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

			err := s.State.STARSComputer().RejectPointOut(ac.Callsign, tcp)
			if err != nil {
				//s.lg.Errorf("RejectPointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) ToggleSPCOverride(tcp, callsign, spc string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.ToggleSPCOverride(spc)
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
	if s.State.DepartureController(ac, s.lg) != tcp {
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

func (s *Sim) AssignAltitude(tcp, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

func (s *Sim) SetTemporaryAltitude(tcp, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.TempAltitude = altitude
			return nil
		})
}

func (s *Sim) SetPilotReportedAltitude(tcp, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) error {
			// Must own the track
			if ac.TrackingController != tcp && !s.Instructors[tcp] {
				return av.ErrOtherControllerHasTrack
			}
			if ac.Mode == av.Altitude && !ac.InhibitModeCAltitudeDisplay {
				// 5-166: must inhibit mode C display if we are getting altitude from the aircraft
				return ErrIllegalFunction
			}
			return nil
		},
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
			ac.PilotReportedAltitude = altitude
			return nil
		})
}

func (s *Sim) ToggleDisplayModeCAltitude(tcp, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(tcp, callsign,
		func(tcp string, ac *av.Aircraft) []av.RadioTransmission {
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
					_, human := s.humanControllers[ac.ControllingController]
					if s.State.IsDeparture(ac) && !human {
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

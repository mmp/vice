// server/dispatcher.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type dispatcher struct {
	sm *SimManager
}

const GetStateUpdateRPC = "Sim.GetStateUpdate"

func (sd *dispatcher) GetStateUpdate(token string, update *sim.StateUpdate) error {
	// Most of the methods in this file are called from the RPC dispatcher,
	// which spawns up goroutines as needed to handle requests, so if we
	// want to catch and report panics, all of the methods need to start
	// like this...
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.GetStateUpdate(token, update)
}

const SignOffRPC = "Sim.SignOff"

func (sd *dispatcher) SignOff(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.SignOff(token)
}

type ChangeControlPositionArgs struct {
	ControllerToken string
	TCP             string
	KeepTracks      bool
}

const ChangeControlPositionRPC = "Sim.ChangeControlPosition"

func (sd *dispatcher) ChangeControlPosition(cs *ChangeControlPositionArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(cs.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.ChangeControlPosition(tcp, cs.TCP, cs.KeepTracks)
	}
}

const TakeOrReturnLaunchControlRPC = "Sim.TakeOrReturnLaunchControl"

func (sd *dispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.TakeOrReturnLaunchControl(tcp)
	}
}

type SetSimRateArgs struct {
	ControllerToken string
	Rate            float32
}

const SetSimRateRPC = "Sim.SetSimRate"

func (sd *dispatcher) SetSimRate(r *SetSimRateArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(r.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.SetSimRate(tcp, r.Rate)
	}
}

type SetLaunchConfigArgs struct {
	ControllerToken string
	Config          sim.LaunchConfig
}

const SetLaunchConfigRPC = "Sim.SetLaunchConfig"

func (sd *dispatcher) SetLaunchConfig(lc *SetLaunchConfigArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(lc.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.SetLaunchConfig(tcp, lc.Config)
	}
}

const TogglePauseRPC = "Sim.TogglePause"

func (sd *dispatcher) TogglePause(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.TogglePause(tcp)
	}
}

const RequestFlightFollowingRPC = "Sim.RequestFlightFollowing"

func (sd *dispatcher) RequestFlightFollowing(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if _, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.RequestFlightFollowing()
	}
}

const FastForwardRPC = "Sim.FastForward"

func (sd *dispatcher) FastForward(token string, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.FastForward(tcp)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type AssociateFlightPlanArgs struct {
	ControllerToken     string
	Callsign            av.ADSBCallsign
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const AssociateFlightPlanRPC = "Sim.AssociateFlightPlan"

func (sd *dispatcher) AssociateFlightPlan(it *AssociateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(it.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AssociateFlightPlan(it.Callsign, it.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type ActivateFlightPlanArgs struct {
	ControllerToken     string
	TrackCallsign       av.ADSBCallsign
	FpACID              sim.ACID
	FlightPlanSpecifier *sim.FlightPlanSpecifier
}

const ActivateFlightPlanRPC = "Sim.ActivateFlightPlan"

func (sd *dispatcher) ActivateFlightPlan(af *ActivateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(af.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ActivateFlightPlan(tcp, af.TrackCallsign, af.FpACID, af.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type CreateFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const CreateFlightPlanRPC = "Sim.CreateFlightPlan"

func (sd *dispatcher) CreateFlightPlan(cfp *CreateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(cfp.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.CreateFlightPlan(tcp, cfp.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type ModifyFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
	ACID                sim.ACID
}

const ModifyFlightPlanRPC = "Sim.ModifyFlightPlan"

func (sd *dispatcher) ModifyFlightPlan(mfp *ModifyFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(mfp.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ModifyFlightPlan(tcp, mfp.ACID, mfp.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        av.ADSBCallsign
}

type ACIDSpecifier struct {
	ControllerToken string
	ACID            sim.ACID
}

type DeleteFlightPlanArgs ACIDSpecifier

const DeleteFlightPlanRPC = "Sim.DeleteFlightPlan"

func (sd *dispatcher) DeleteFlightPlan(dt *DeleteFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(dt.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteFlightPlan(tcp, dt.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type RepositionTrackArgs struct {
	ControllerToken string
	ACID            sim.ACID        // from
	Callsign        av.ADSBCallsign // to
	Position        math.Point2LL   // to
}

const RepositionTrackRPC = "Sim.RepositionTrack"

func (sd *dispatcher) RepositionTrack(rt *RepositionTrackArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(rt.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RepositionTrack(tcp, rt.ACID, rt.Callsign, rt.Position)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type HandoffArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToTCP           string
}

const HandoffTrackRPC = "Sim.HandoffTrack"

func (sd *dispatcher) HandoffTrack(h *HandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(h.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.HandoffTrack(tcp, h.ACID, h.ToTCP)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const RedirectHandoffRPC = "Sim.RedirectHandoff"

func (sd *dispatcher) RedirectHandoff(h *HandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(h.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RedirectHandoff(tcp, h.ACID, h.ToTCP)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const AcceptRedirectedHandoffRPC = "Sim.AcceptRedirectedHandoff"

func (sd *dispatcher) AcceptRedirectedHandoff(po *AcceptHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcceptRedirectedHandoff(tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type AcceptHandoffArgs ACIDSpecifier

const AcceptHandoffRPC = "Sim.AcceptHandoff"

func (sd *dispatcher) AcceptHandoff(ah *AcceptHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(ah.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcceptHandoff(tcp, ah.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type CancelHandoffArgs ACIDSpecifier

const CancelHandoffRPC = "Sim.CancelHandoff"

func (sd *dispatcher) CancelHandoff(ch *CancelHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(ch.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.CancelHandoff(tcp, ch.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type PointOutArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Controller      string
}

type ForceQLArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Controller      string
}

const ForceQLRPC = "Sim.ForceQL"

func (sd *dispatcher) ForceQL(ql *ForceQLArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(ql.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ForceQL(tcp, ql.ACID, ql.Controller)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type GlobalMessageArgs struct {
	ControllerToken string
	Message         string
}

const GlobalMessageRPC = "Sim.GlobalMessage"

func (sd *dispatcher) GlobalMessage(gm *GlobalMessageArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(gm.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.GlobalMessage(tcp, gm.Message)
	}
}

const PointOutRPC = "Sim.PointOut"

func (sd *dispatcher) PointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.PointOut(tcp, po.ACID, po.Controller)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const AcknowledgePointOutRPC = "Sim.AcknowledgePointOut"

func (sd *dispatcher) AcknowledgePointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcknowledgePointOut(tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const RecallPointOutRPC = "Sim.RecallPointOut"

func (sd *dispatcher) RecallPointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RecallPointOut(tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const RejectPointOutRPC = "Sim.RejectPointOut"

func (sd *dispatcher) RejectPointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RejectPointOut(tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type HeldDepartureArgs AircraftSpecifier

const ReleaseDepartureRPC = "Sim.ReleaseDeparture"

func (sd *dispatcher) ReleaseDeparture(hd *HeldDepartureArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(hd.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ReleaseDeparture(tcp, hd.Callsign)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type DeleteAircraftArgs AircraftSpecifier

const DeleteAllAircraftRPC = "Sim.DeleteAllAircraft"

func (sd *dispatcher) DeleteAllAircraft(da *DeleteAircraftArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(da.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteAllAircraft(tcp)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type DeleteAircraftListArgs struct {
	ControllerToken string
	Aircraft        []sim.Aircraft
}

const DeleteAircraftRPC = "Sim.DeleteAircraft"

func (sd *dispatcher) DeleteAircraft(da *DeleteAircraftListArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(da.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteAircraftSlice(tcp, da.Aircraft)
		s.GetStateUpdate(tcp, update)
		return err
	}
}

type SendRouteCoordinatesArgs struct {
	ControllerToken string
	ACID            sim.ACID
}

const SendRouteCoordinatesRPC = "Sim.SendRouteCoordinates"

func (sd *dispatcher) SendRouteCoordinates(rca *SendRouteCoordinatesArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(rca.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.SendRouteCoordinates(tcp, rca.ACID)
		s.GetStateUpdate(tcp, update)
		return err
	}
}

type FlightPlanDirectArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Fix             string
}

const FlightPlanDirectRPC = "Sim.FlightPlanDirect"

func (sd *dispatcher) FlightPlanDirect(da *FlightPlanDirectArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(da.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.FlightPlanDirect(tcp, da.Fix, da.ACID)
		s.GetStateUpdate(tcp, update)
		return err
	}
}

type AircraftCommandsArgs struct {
	ControllerToken string
	Callsign        av.ADSBCallsign
	Commands        string
}

// If an RPC call returns an error, then the result argument is not returned(!?).
// So we don't use the error type for syntax errors...
type AircraftCommandsResult struct {
	ErrorMessage   string
	RemainingInput string
}

const RunAircraftCommandsRPC = "Sim.RunAircraftCommands"

func (sd *dispatcher) RunAircraftCommands(cmds *AircraftCommandsArgs, result *AircraftCommandsResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	tcp, s, ok := sd.sm.LookupController(cmds.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}

	callsign := cmds.Callsign
	commands := strings.Fields(cmds.Commands)

	for i, command := range commands {
		rewriteError := func(err error) {
			result.RemainingInput = strings.Join(commands[i:], " ")
			if err != nil {
				result.ErrorMessage = err.Error()
			}
		}

		// A###, C###, and D### all equivalently assign an altitude
		if (command[0] == 'A' || command[0] == 'C' || command[0] == 'D') && len(command) > 1 && util.IsAllNumbers(command[1:]) {
			// Look for an altitude
			if alt, err := strconv.Atoi(command[1:]); err != nil {
				rewriteError(err)
				return nil
			} else if err := s.AssignAltitude(tcp, callsign, 100*alt, false); err != nil {
				rewriteError(err)
				return nil
			} else {
				continue
			}
		}

		switch command[0] {
		case 'A':
			if command == "A" {
				if err := s.AltitudeOurDiscretion(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				} else {
					continue
				}
			} else {
				components := strings.Split(command, "/")
				if len(components) != 2 || len(components[1]) == 0 || components[1][0] != 'C' {
					rewriteError(ErrInvalidCommandSyntax)
					return nil
				}

				fix := strings.ToUpper(components[0][1:])
				approach := components[1][1:]
				if err := s.AtFixCleared(tcp, callsign, fix, approach); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'C':
			if command == "CAC" {
				// Cancel approach clearance
				if err := s.CancelApproachClearance(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "CVS" {
				if err := s.ClimbViaSID(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 4 && command[:3] == "CSI" && !util.IsAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := s.ClearedApproach(tcp, callsign, command[3:], true); err != nil {
					rewriteError(err)
					return nil
				}
			} else if components := strings.Split(command, "/"); len(components) > 1 {
				// Cross fix [at altitude] [at speed]
				fix := components[0][1:]
				var ar *av.AltitudeRestriction
				speed := 0

				for _, cmd := range components[1:] {
					if len(cmd) == 0 {
						rewriteError(ErrInvalidCommandSyntax)
						return nil
					}

					var err error
					if cmd[0] == 'A' && len(cmd) > 1 {
						if ar, err = av.ParseAltitudeRestriction(cmd[1:]); err != nil {
							rewriteError(err)
							return nil
						}
						// User input here is 100s of feet, while AltitudeRestriction is feet...
						ar.Range[0] *= 100
						ar.Range[1] *= 100
					} else if cmd[0] == 'S' {
						if speed, err = strconv.Atoi(cmd[1:]); err != nil {
							rewriteError(err)
							return nil
						}
					} else {
						rewriteError(ErrInvalidCommandSyntax)
						return nil
					}
				}

				if err := s.CrossFixAt(tcp, callsign, fix, ar, speed); err != nil {
					rewriteError(err)
					return nil
				}
			} else if strings.HasPrefix(command, "CT") && len(command) > 2 {
				if err := s.ContactController(tcp, sim.ACID(callsign), command[2:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else if err := s.ClearedApproach(tcp, callsign, command[1:], false); err != nil {
				rewriteError(err)
				return nil
			}

		case 'D':
			if command == "DVS" {
				if err := s.DescendViaSTAR(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
				fix := components[0][1:]

				switch components[1][0] {
				case 'D':
					// Depart <fix1> direct <fix2>
					if err := s.DepartFixDirect(tcp, callsign, fix, components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					}
				case 'H':
					// Depart <fix> at heading <hdg>
					if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := s.DepartFixHeading(tcp, callsign, fix, hdg); err != nil {
						rewriteError(err)
						return nil
					}

				default:
					rewriteError(ErrInvalidCommandSyntax)
					return nil
				}
			} else if len(command) >= 4 && len(command) <= 6 {
				if err := s.DirectFix(tcp, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'E':
			if command == "ED" {
				if err := s.ExpediteDescent(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "EC" {
				if err := s.ExpediteClimb(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 1 {
				// Expect approach.
				if err := s.ExpectApproach(tcp, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'F':
			if command == "FC" {
				if err := s.ContactTrackingController(tcp, sim.ACID(callsign) /* HAX */); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'H':
			if len(command) == 1 {
				if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          tcp,
					ADSBCallsign: callsign,
					Present:      true,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				rewriteError(err)
				return nil
			} else if err := s.AssignHeading(&sim.HeadingArgs{
				TCP:          tcp,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         sim.TurnClosest,
			}); err != nil {
				rewriteError(err)
				return nil
			}

		case 'I':
			if len(command) == 1 {
				if err := s.InterceptLocalizer(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "ID" {
				if err := s.Ident(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          tcp,
					ADSBCallsign: callsign,
					LeftDegrees:  deg,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          tcp,
					ADSBCallsign: callsign,
					Heading:      hdg,
					Turn:         sim.TurnLeft,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'R':
			if command == "RON" {
				if err := s.ResumeOwnNavigation(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "RST" {
				if err := s.RadarServicesTerminated(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          tcp,
					ADSBCallsign: callsign,
					RightDegrees: deg,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          tcp,
					ADSBCallsign: callsign,
					Heading:      hdg,
					Turn:         sim.TurnRight,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := s.AssignSpeed(tcp, callsign, 0, false); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMIN" {
				if err := s.MaintainSlowestPractical(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMAX" {
				if err := s.MaintainMaximumForward(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SS" {
				if err := s.SaySpeed(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQS" {
				if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeStandby); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQA" {
				if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeAltitude); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQON" {
				if err := s.ChangeTransponderMode(tcp, callsign, av.TransponderModeOn); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) == 6 && command[:2] == "SQ" {
				if sq, err := av.ParseSquawk(command[2:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.ChangeSquawk(tcp, callsign, sq); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SH" {
				if err := s.SayHeading(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SA" {
				if err := s.SayAltitude(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if kts, err := strconv.Atoi(command[1:]); err != nil {
				rewriteError(err)
				return nil
			} else if err := s.AssignSpeed(tcp, callsign, kts, false); err != nil {
				rewriteError(err)
				return nil
			}

		case 'T':
			if command == "TO" {
				if err := s.ContactTower(tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if n := len(command); n > 2 {
				if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
					if command[n-1] == 'L' {
						// turn x degrees left
						if err := s.AssignHeading(&sim.HeadingArgs{
							TCP:          tcp,
							ADSBCallsign: callsign,
							LeftDegrees:  deg,
						}); err != nil {
							rewriteError(err)
							return nil
						} else {
							continue
						}
					} else if command[n-1] == 'R' {
						// turn x degrees right
						if err := s.AssignHeading(&sim.HeadingArgs{
							TCP:          tcp,
							ADSBCallsign: callsign,
							RightDegrees: deg,
						}); err != nil {
							rewriteError(err)
							return nil
						} else {
							continue
						}
					}
				}

				switch command[:2] {
				case "TS":
					if kts, err := strconv.Atoi(command[2:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := s.AssignSpeed(tcp, callsign, kts, true); err != nil {
						rewriteError(err)
						return nil
					}

				case "TA", "TC", "TD":
					if alt, err := strconv.Atoi(command[2:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := s.AssignAltitude(tcp, callsign, 100*alt, true); err != nil {
						rewriteError(err)
						return nil
					}

				default:
					rewriteError(ErrInvalidCommandSyntax)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'X':
			s.DeleteAircraft(tcp, callsign)

		default:
			rewriteError(ErrInvalidCommandSyntax)
			return nil
		}
	}

	return nil
}

type LaunchAircraftArgs struct {
	ControllerToken string
	Aircraft        sim.Aircraft
	DepartureRunway string
}

const LaunchAircraftRPC = "Sim.LaunchAircraft"

func (sd *dispatcher) LaunchAircraft(ls *LaunchAircraftArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	_, s, ok := sd.sm.LookupController(ls.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}
	s.LaunchAircraft(ls.Aircraft, ls.DepartureRunway)
	return nil
}

type CreateDepartureArgs struct {
	ControllerToken string
	Airport         string
	Runway          string
	Category        string
	Rules           av.FlightRules
}

const CreateDepartureRPC = "Sim.CreateDeparture"

func (sd *dispatcher) CreateDeparture(da *CreateDepartureArgs, depAc *sim.Aircraft) error {
	defer sd.sm.lg.CatchAndReportCrash()

	_, s, ok := sd.sm.LookupController(da.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}
	var ac *sim.Aircraft
	var err error
	if da.Rules == av.FlightRulesIFR {
		ac, err = s.CreateIFRDeparture(da.Airport, da.Runway, da.Category)
	} else {
		ac, err = s.CreateVFRDeparture(da.Airport)
	}

	if ac != nil && err == nil {
		*depAc = *ac
	}
	return err
}

type CreateArrivalArgs struct {
	ControllerToken string
	Group           string
	Airport         string
}

const CreateArrivalRPC = "Sim.CreateArrival"

func (sd *dispatcher) CreateArrival(aa *CreateArrivalArgs, arrAc *sim.Aircraft) error {
	defer sd.sm.lg.CatchAndReportCrash()

	_, s, ok := sd.sm.LookupController(aa.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}
	ac, err := s.CreateArrival(aa.Group, aa.Airport)
	if err == nil {
		*arrAc = *ac
	}
	return err
}

type CreateOverflightArgs struct {
	ControllerToken string
	Group           string
}

const CreateOverflightRPC = "Sim.CreateOverflight"

func (sd *dispatcher) CreateOverflight(oa *CreateOverflightArgs, ofAc *sim.Aircraft) error {
	defer sd.sm.lg.CatchAndReportCrash()

	_, s, ok := sd.sm.LookupController(oa.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}
	ac, err := s.CreateOverflight(oa.Group)
	if err == nil {
		*ofAc = *ac
	}
	return err
}

type RestrictionAreaArgs struct {
	ControllerToken string
	Index           int
	RestrictionArea av.RestrictionArea
}

type CreateRestrictionAreaResultArgs struct {
	Index       int
	StateUpdate sim.StateUpdate
}

const CreateRestrictionAreaRPC = "Sim.CreateRestrictionArea"

func (sd *dispatcher) CreateRestrictionArea(ra *RestrictionAreaArgs, result *CreateRestrictionAreaResultArgs) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if tcp, s, ok := sd.sm.LookupController(ra.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else if i, err := s.CreateRestrictionArea(ra.RestrictionArea); err != nil {
		return err
	} else {
		result.Index = i
		s.GetStateUpdate(tcp, &result.StateUpdate)
		return nil
	}
}

const UpdateRestrictionAreaRPC = "Sim.UpdateRestrictionArea"

func (sd *dispatcher) UpdateRestrictionArea(ra *RestrictionAreaArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	tcp, s, ok := sd.sm.LookupController(ra.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.UpdateRestrictionArea(ra.Index, ra.RestrictionArea)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

const DeleteRestrictionAreaRPC = "Sim.DeleteRestrictionArea"

func (sd *dispatcher) DeleteRestrictionArea(ra *RestrictionAreaArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	tcp, s, ok := sd.sm.LookupController(ra.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteRestrictionArea(ra.Index)
		if err == nil {
			s.GetStateUpdate(tcp, update)
		}
		return err
	}
}

type VideoMapsArgs struct {
	ControllerToken string
	Filename        string
}

const GetVideoMapLibraryRPC = "Sim.GetVideoMapLibrary"

func (sd *dispatcher) GetVideoMapLibrary(vm *VideoMapsArgs, vmf *sim.VideoMapLibrary) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if _, _, ok := sd.sm.LookupController(vm.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	}
	if v, err := sim.LoadVideoMapLibrary(vm.Filename); err == nil {
		*vmf = *v
		return nil
	} else {
		return err
	}
}

const GetAircraftDisplayStateRPC = "Sim.GetAircraftDisplayState"

func (sd *dispatcher) GetAircraftDisplayState(as *AircraftSpecifier, state *sim.AircraftDisplayState) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if _, s, ok := sd.sm.LookupController(as.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		var err error
		*state, err = s.GetAircraftDisplayState(as.Callsign)
		return err
	}
}

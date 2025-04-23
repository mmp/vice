// pkg/server/dispatcher.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type Dispatcher struct {
	sm *SimManager
}

func (sd *Dispatcher) GetStateUpdate(token string, update *sim.StateUpdate) error {
	// Most of the methods in this file are called from the RPC dispatcher,
	// which spawns up goroutines as needed to handle requests, so if we
	// want to catch and report panics, all of the methods need to start
	// like this...
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.GetStateUpdate(token, update)
}

func (sd *Dispatcher) SignOff(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.SignOff(token)
}

type ChangeControlPositionArgs struct {
	ControllerToken string
	TCP             string
	KeepTracks      bool
}

func (sd *Dispatcher) ChangeControlPosition(cs *ChangeControlPositionArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(cs.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.ChangeControlPosition(ctrl.tcp, cs.TCP, cs.KeepTracks)
	}
}

func (sd *Dispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.TakeOrReturnLaunchControl(ctrl.tcp)
	}
}

type SetSimRateArgs struct {
	ControllerToken string
	Rate            float32
}

func (sd *Dispatcher) SetSimRate(r *SetSimRateArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(r.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.SetSimRate(ctrl.tcp, r.Rate)
	}
}

type SetLaunchConfigArgs struct {
	ControllerToken string
	Config          sim.LaunchConfig
}

func (sd *Dispatcher) SetLaunchConfig(lc *SetLaunchConfigArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(lc.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.SetLaunchConfig(ctrl.tcp, lc.Config)
	}
}

func (sd *Dispatcher) TogglePause(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.TogglePause(ctrl.tcp)
	}
}

func (sd *Dispatcher) FastForward(token string, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.FastForward(ctrl.tcp)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type AssociateFlightPlanArgs struct {
	ControllerToken     string
	Callsign            av.ADSBCallsign
	FlightPlanSpecifier sim.STARSFlightPlanSpecifier
}

func (sd *Dispatcher) AssociateFlightPlan(it *AssociateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(it.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AssociateFlightPlan(it.Callsign, it.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type ActivateFlightPlanArgs struct {
	ControllerToken     string
	TrackCallsign       av.ADSBCallsign
	FpACID              sim.ACID
	FlightPlanSpecifier *sim.STARSFlightPlanSpecifier
}

func (sd *Dispatcher) ActivateFlightPlan(af *ActivateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(af.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ActivateFlightPlan(ctrl.tcp, af.TrackCallsign, af.FpACID, af.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type CreateFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.STARSFlightPlanSpecifier
	Type                sim.STARSFlightPlanType
}

func (sd *Dispatcher) CreateFlightPlan(cfp *CreateFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(cfp.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		_, err := s.CreateFlightPlan(ctrl.tcp, cfp.Type, cfp.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type ModifyFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.STARSFlightPlanSpecifier
	ACID                sim.ACID
}

func (sd *Dispatcher) ModifyFlightPlan(mfp *ModifyFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(mfp.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		_, err := s.ModifyFlightPlan(ctrl.tcp, mfp.ACID, mfp.FlightPlanSpecifier)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
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

func (sd *Dispatcher) DeleteFlightPlan(dt *DeleteFlightPlanArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(dt.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteFlightPlan(ctrl.tcp, dt.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
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

func (sd *Dispatcher) RepositionTrack(rt *RepositionTrackArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(rt.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RepositionTrack(ctrl.tcp, rt.ACID, rt.Callsign, rt.Position)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type HandoffArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToTCP           string
}

func (sd *Dispatcher) HandoffTrack(h *HandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(h.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.HandoffTrack(ctrl.tcp, h.ACID, h.ToTCP)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) RedirectHandoff(h *HandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(h.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RedirectHandoff(ctrl.tcp, h.ACID, h.ToTCP)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) AcceptRedirectedHandoff(po *AcceptHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcceptRedirectedHandoff(ctrl.tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type AcceptHandoffArgs ACIDSpecifier

func (sd *Dispatcher) AcceptHandoff(ah *AcceptHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(ah.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcceptHandoff(ctrl.tcp, ah.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type CancelHandoffArgs ACIDSpecifier

func (sd *Dispatcher) CancelHandoff(ch *CancelHandoffArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(ch.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.CancelHandoff(ctrl.tcp, ch.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
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

func (sd *Dispatcher) ForceQL(ql *ForceQLArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(ql.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ForceQL(ctrl.tcp, ql.ACID, ql.Controller)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type GlobalMessageArgs struct {
	ControllerToken string
	Message         string
}

func (sd *Dispatcher) GlobalMessage(gm *GlobalMessageArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(gm.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return s.GlobalMessage(ctrl.tcp, gm.Message)
	}
}

func (sd *Dispatcher) PointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.PointOut(ctrl.tcp, po.ACID, po.Controller)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) AcknowledgePointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.AcknowledgePointOut(ctrl.tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) RecallPointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RecallPointOut(ctrl.tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) RejectPointOut(po *PointOutArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(po.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.RejectPointOut(ctrl.tcp, po.ACID)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type HeldDepartureArgs AircraftSpecifier

func (sd *Dispatcher) ReleaseDeparture(hd *HeldDepartureArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(hd.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.ReleaseDeparture(ctrl.tcp, hd.Callsign)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type DeleteAircraftArgs AircraftSpecifier

func (sd *Dispatcher) DeleteAllAircraft(da *DeleteAircraftArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(da.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteAllAircraft(ctrl.tcp)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
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

func (sd *Dispatcher) RunAircraftCommands(cmds *AircraftCommandsArgs, result *AircraftCommandsResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	ctrl, s, ok := sd.sm.LookupController(cmds.ControllerToken)
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

		switch command[0] {
		case 'A', 'C':
			if command == "CAC" {
				// Cancel approach clearance
				if err := s.CancelApproachClearance(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "CVS" {
				if err := s.ClimbViaSID(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 4 && command[:3] == "CSI" && !util.IsAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := s.ClearedApproach(ctrl.tcp, callsign, command[3:], true); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command[0] == 'C' && len(command) > 2 && !util.IsAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
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

					if err := s.CrossFixAt(ctrl.tcp, callsign, fix, ar, speed); err != nil {
						rewriteError(err)
						return nil
					}
				} else if err := s.ClearedApproach(ctrl.tcp, callsign, command[1:], false); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				if command[0] == 'A' {
					components := strings.Split(command, "/")
					if len(components) != 2 || len(components[1]) == 0 || components[1][0] != 'C' {
						rewriteError(ErrInvalidCommandSyntax)
						return nil
					}

					fix := strings.ToUpper(components[0][1:])
					approach := components[1][1:]
					if err := s.AtFixCleared(ctrl.tcp, callsign, fix, approach); err != nil {
						rewriteError(err)
						return nil
					} else {
						continue
					}
				}

				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignAltitude(ctrl.tcp, callsign, 100*alt, false); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'D':
			if command == "DVS" {
				if err := s.DescendViaSTAR(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
				fix := components[0][1:]

				switch components[1][0] {
				case 'D':
					// Depart <fix1> direct <fix2>
					if err := s.DepartFixDirect(ctrl.tcp, callsign, fix, components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					}
				case 'H':
					// Depart <fix> at heading <hdg>
					if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := s.DepartFixHeading(ctrl.tcp, callsign, fix, hdg); err != nil {
						rewriteError(err)
						return nil
					}

				default:
					rewriteError(ErrInvalidCommandSyntax)
					return nil
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignAltitude(ctrl.tcp, callsign, 100*alt, false); err != nil {
					rewriteError(err)
					return nil
				}
			} else if _, ok := s.State.Locate(string(command[1:])); ok {
				if err := s.DirectFix(ctrl.tcp, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'E':
			if command == "ED" {
				if err := s.ExpediteDescent(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "EC" {
				if err := s.ExpediteClimb(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 1 {
				// Expect approach.
				if err := s.ExpectApproach(ctrl.tcp, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'F':
			if command == "FC" {
				if err := s.HandoffControl(ctrl.tcp, sim.ACID(callsign) /* HAX */); err != nil {
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
					TCP:          ctrl.tcp,
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
				TCP:          ctrl.tcp,
				ADSBCallsign: callsign,
				Heading:      hdg,
				Turn:         av.TurnClosest,
			}); err != nil {
				rewriteError(err)
				return nil
			}

		case 'I':
			if len(command) == 1 {
				if err := s.InterceptLocalizer(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "ID" {
				if err := s.Ident(ctrl.tcp, callsign); err != nil {
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
					TCP:          ctrl.tcp,
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
					TCP:          ctrl.tcp,
					ADSBCallsign: callsign,
					Heading:      hdg,
					Turn:         av.TurnLeft,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.AssignHeading(&sim.HeadingArgs{
					TCP:          ctrl.tcp,
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
					TCP:          ctrl.tcp,
					ADSBCallsign: callsign,
					Heading:      hdg,
					Turn:         av.TurnRight,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := s.AssignSpeed(ctrl.tcp, callsign, 0, false); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMIN" {
				if err := s.MaintainSlowestPractical(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMAX" {
				if err := s.MaintainMaximumForward(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SS" {
				if err := s.SaySpeed(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQS" {
				if err := s.ChangeTransponderMode(ctrl.tcp, callsign, av.TransponderModeStandby); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQA" {
				if err := s.ChangeTransponderMode(ctrl.tcp, callsign, av.TransponderModeAltitude); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SQON" {
				if err := s.ChangeTransponderMode(ctrl.tcp, callsign, av.TransponderModeOn); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) == 6 && command[:2] == "SQ" {
				if sq, err := av.ParseSquawk(command[2:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := s.ChangeSquawk(ctrl.tcp, callsign, sq); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SH" {
				if err := s.SayHeading(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SA" {
				if err := s.SayAltitude(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if kts, err := strconv.Atoi(command[1:]); err != nil {
				rewriteError(err)
				return nil
			} else if err := s.AssignSpeed(ctrl.tcp, callsign, kts, false); err != nil {
				rewriteError(err)
				return nil
			}

		case 'T':
			if command == "TO" {
				if err := s.ContactTower(ctrl.tcp, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if n := len(command); n > 2 {
				if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
					if command[n-1] == 'L' {
						// turn x degrees left
						if err := s.AssignHeading(&sim.HeadingArgs{
							TCP:          ctrl.tcp,
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
							TCP:          ctrl.tcp,
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
					} else if err := s.AssignSpeed(ctrl.tcp, callsign, kts, true); err != nil {
						rewriteError(err)
						return nil
					}

				case "TA", "TC", "TD":
					if alt, err := strconv.Atoi(command[2:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := s.AssignAltitude(ctrl.tcp, callsign, 100*alt, true); err != nil {
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
			s.DeleteAircraft(ctrl.tcp, callsign)

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

func (sd *Dispatcher) LaunchAircraft(ls *LaunchAircraftArgs, _ *struct{}) error {
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

func (sd *Dispatcher) CreateDeparture(da *CreateDepartureArgs, depAc *sim.Aircraft) error {
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

func (sd *Dispatcher) CreateArrival(aa *CreateArrivalArgs, arrAc *sim.Aircraft) error {
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

func (sd *Dispatcher) CreateOverflight(oa *CreateOverflightArgs, ofAc *sim.Aircraft) error {
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

func (sd *Dispatcher) CreateRestrictionArea(ra *RestrictionAreaArgs, result *CreateRestrictionAreaResultArgs) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if ctrl, s, ok := sd.sm.LookupController(ra.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else if i, err := s.CreateRestrictionArea(ra.RestrictionArea); err != nil {
		return err
	} else {
		result.Index = i
		s.GetStateUpdate(ctrl.tcp, &result.StateUpdate)
		return nil
	}
}

func (sd *Dispatcher) UpdateRestrictionArea(ra *RestrictionAreaArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	ctrl, s, ok := sd.sm.LookupController(ra.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.UpdateRestrictionArea(ra.Index, ra.RestrictionArea)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

func (sd *Dispatcher) DeleteRestrictionArea(ra *RestrictionAreaArgs, update *sim.StateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	ctrl, s, ok := sd.sm.LookupController(ra.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.DeleteRestrictionArea(ra.Index)
		if err == nil {
			s.GetStateUpdate(ctrl.tcp, update)
		}
		return err
	}
}

type VideoMapsArgs struct {
	ControllerToken string
	Filename        string
}

func (sd *Dispatcher) GetVideoMapLibrary(vm *VideoMapsArgs, vmf *sim.VideoMapLibrary) error {
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

func (sd *Dispatcher) GetAircraftDisplayState(as *AircraftSpecifier, state *sim.AircraftDisplayState) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if _, s, ok := sd.sm.LookupController(as.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		var err error
		*state, err = s.GetAircraftDisplayState(as.Callsign)
		return err
	}
}

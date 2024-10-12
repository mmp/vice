// pkg/sim/dispatcher.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type Dispatcher struct {
	sm *SimManager
}

func (sd *Dispatcher) GetWorldUpdate(token string, update *WorldUpdate) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.GetWorldUpdate(token, update)
	}
}

func (sd *Dispatcher) SignOff(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SignOff(token)
	}
}

type ChangeControlPositionArgs struct {
	ControllerToken string
	Callsign        string
	KeepTracks      bool
}

func (sd *Dispatcher) ChangeControlPosition(cs *ChangeControlPositionArgs, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(cs.ControllerToken); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ChangeControlPosition(cs.ControllerToken, cs.Callsign, cs.KeepTracks)
	}
}

func (sd *Dispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TakeOrReturnLaunchControl(token)
	}
}

type SetSimRateArgs struct {
	ControllerToken string
	Rate            float32
}

func (sd *Dispatcher) SetSimRate(r *SetSimRateArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[r.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetSimRate(r.ControllerToken, r.Rate)
	}
}

type SetLaunchConfigArgs struct {
	ControllerToken string
	Config          LaunchConfig
}

func (sd *Dispatcher) SetLaunchConfig(lc *SetLaunchConfigArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[lc.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetLaunchConfig(lc.ControllerToken, lc.Config)
	}
}

func (sd *Dispatcher) TogglePause(token string, _ *struct{}) error {
	if sim, ok := sd.sm.ControllerTokenToSim(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.TogglePause(token)
	}
}

type SetScratchpadArgs struct {
	ControllerToken string
	Callsign        string
	Scratchpad      string
}

func (sd *Dispatcher) SetScratchpad(a *SetScratchpadArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetScratchpad(a.ControllerToken, a.Callsign, a.Scratchpad)
	}
}

func (sd *Dispatcher) SetSecondaryScratchpad(a *SetScratchpadArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetSecondaryScratchpad(a.ControllerToken, a.Callsign, a.Scratchpad)
	}
}

func (sd *Dispatcher) AutoAssociateFP(it *InitiateTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[it.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AutoAssociateFP(it.ControllerToken, it.Callsign, it.Plan)
	}
}

type SetGlobalLeaderLineArgs struct {
	ControllerToken string
	Callsign        string
	Direction       *math.CardinalOrdinalDirection
}

func (sd *Dispatcher) SetGlobalLeaderLine(a *SetGlobalLeaderLineArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[a.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetGlobalLeaderLine(a.ControllerToken, a.Callsign, a.Direction)
	}
}

type InitiateTrackArgs struct {
	AircraftSpecifier
	Plan *STARSFlightPlan
}

func (sd *Dispatcher) InitiateTrack(it *InitiateTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[it.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.InitiateTrack(it.ControllerToken, it.Callsign, it.Plan)
	}
}

type CreateUnsupportedTrackArgs struct {
	ControllerToken  string
	Callsign         string
	UnsupportedTrack *UnsupportedTrack
}

func (sd *Dispatcher) CreateUnsupportedTrack(it *CreateUnsupportedTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[it.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.CreateUnsupportedTrack(it.ControllerToken, it.Callsign, it.UnsupportedTrack)
	}
}

type UploadPlanArgs struct {
	ControllerToken string
	Type            int
	Plan            *STARSFlightPlan
}

func (sd *Dispatcher) UploadFlightPlan(it *UploadPlanArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[it.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.UploadFlightPlan(it.ControllerToken, it.Type, it.Plan)
	}
}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        string
}

type DropTrackArgs AircraftSpecifier

func (sd *Dispatcher) DropTrack(dt *DropTrackArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[dt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DropTrack(dt.ControllerToken, dt.Callsign)
	}
}

type HandoffArgs struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (sd *Dispatcher) HandoffTrack(h *HandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.HandoffTrack(h.ControllerToken, h.Callsign, h.Controller)
	}
}

func (sd *Dispatcher) RedirectHandoff(h *HandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[h.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.RedirectHandoff(h.ControllerToken, h.Callsign, h.Controller)
	}
}

func (sd *Dispatcher) AcceptRedirectedHandoff(po *AcceptHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AcceptRedirectedHandoff(po.ControllerToken, po.Callsign)
	}
}

type AcceptHandoffArgs AircraftSpecifier

func (sd *Dispatcher) AcceptHandoff(ah *AcceptHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ah.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AcceptHandoff(ah.ControllerToken, ah.Callsign)
	}
}

type CancelHandoffArgs AircraftSpecifier

func (sd *Dispatcher) CancelHandoff(ch *CancelHandoffArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ch.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.CancelHandoff(ch.ControllerToken, ch.Callsign)
	}
}

type PointOutArgs struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

type ForceQLArgs struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (sd *Dispatcher) ForceQL(ql *ForceQLArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ql.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ForceQL(ql.ControllerToken, ql.Callsign, ql.Controller)
	}
}

type GlobalMessageArgs struct {
	ControllerToken string
	FromController  string
	Message         string
}

func (sd *Dispatcher) GlobalMessage(po *GlobalMessageArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.GlobalMessage(*po)
	}
}

func (sd *Dispatcher) PointOut(po *PointOutArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.PointOut(po.ControllerToken, po.Callsign, po.Controller)
	}
}

func (sd *Dispatcher) AcknowledgePointOut(po *PointOutArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.AcknowledgePointOut(po.ControllerToken, po.Callsign)
	}
}

func (sd *Dispatcher) RejectPointOut(po *PointOutArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[po.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.RejectPointOut(po.ControllerToken, po.Callsign)
	}
}

type ToggleSPCArgs struct {
	ControllerToken string
	Callsign        string
	SPC             string
}

func (sd *Dispatcher) ToggleSPCOverride(ts *ToggleSPCArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[ts.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ToggleSPCOverride(ts.ControllerToken, ts.Callsign, ts.SPC)
	}
}

type HeldDepartureArgs AircraftSpecifier

func (sd *Dispatcher) ReleaseDeparture(hd *HeldDepartureArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[hd.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.ReleaseDeparture(hd.ControllerToken, hd.Callsign)
	}
}

type AssignAltitudeArgs struct {
	ControllerToken string
	Callsign        string
	Altitude        int
}

func (sd *Dispatcher) SetTemporaryAltitude(alt *AssignAltitudeArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[alt.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.SetTemporaryAltitude(alt.ControllerToken, alt.Callsign, alt.Altitude)
	}
}

type DeleteAircraftArgs AircraftSpecifier

func (sd *Dispatcher) DeleteAllAircraft(da *DeleteAircraftArgs, _ *struct{}) error {
	if sim, ok := sd.sm.controllerTokenToSim[da.ControllerToken]; !ok {
		return ErrNoSimForControllerToken
	} else {
		return sim.DeleteAllAircraft(da.ControllerToken)
	}
}

type AircraftCommandsArgs struct {
	ControllerToken string
	Callsign        string
	Commands        string
}

// If an RPC call returns an error, then the result argument is not returned(!?).
// So we don't use the error type for syntax errors...
type AircraftCommandsResult struct {
	ErrorMessage   string
	RemainingInput string
}

func (sd *Dispatcher) RunAircraftCommands(cmds *AircraftCommandsArgs, result *AircraftCommandsResult) error {
	token, callsign := cmds.ControllerToken, cmds.Callsign
	sim, ok := sd.sm.controllerTokenToSim[token]
	if !ok {
		return ErrNoSimForControllerToken
	}

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
				if err := sim.CancelApproachClearance(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "CVS" {
				if err := sim.ClimbViaSID(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 4 && command[:3] == "CSI" && !util.IsAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := sim.ClearedApproach(token, callsign, command[3:], true); err != nil {
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

					if err := sim.CrossFixAt(token, callsign, fix, ar, speed); err != nil {
						rewriteError(err)
						return nil
					}
				} else if err := sim.ClearedApproach(token, callsign, command[1:], false); err != nil {
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
					if err := sim.AtFixCleared(token, callsign, fix, approach); err != nil {
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
				} else if err := sim.AssignAltitude(token, callsign, 100*alt, false); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'D':
			if command == "DVS" {
				if err := sim.DescendViaSTAR(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
				fix := components[0][1:]

				switch components[1][0] {
				case 'D':
					// Depart <fix1> direct <fix2>
					if err := sim.DepartFixDirect(token, callsign, fix, components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					}
				case 'H':
					// Depart <fix> at heading <hdg>
					if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := sim.DepartFixHeading(token, callsign, fix, hdg); err != nil {
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
				} else if err := sim.AssignAltitude(token, callsign, 100*alt, false); err != nil {
					rewriteError(err)
					return nil
				}
			} else if _, ok := sim.State.Locate(string(command[1:])); ok {
				if err := sim.DirectFix(token, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}

		case 'E':
			if command == "ED" {
				if err := sim.ExpediteDescent(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "EC" {
				if err := sim.ExpediteClimb(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) > 1 {
				// Expect approach.
				if err := sim.ExpectApproach(token, callsign, command[1:]); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				rewriteError(ErrInvalidCommandSyntax)
				return nil
			}
		case 'F':
			if command == "FC" {
				if err := sim.HandoffControl(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			}
		case 'H':
			if len(command) == 1 {
				if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Present:         true,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				rewriteError(err)
				return nil
			} else if err := sim.AssignHeading(&HeadingArgs{
				ControllerToken: token,
				Callsign:        callsign,
				Heading:         hdg,
				Turn:            av.TurnClosest,
			}); err != nil {
				rewriteError(err)
				return nil
			}

		case 'I':
			if len(command) == 1 {
				if err := sim.InterceptLocalizer(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "ID" {
				if err := sim.Ident(token, callsign); err != nil {
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
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					LeftDegrees:     deg,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Heading:         hdg,
					Turn:            av.TurnLeft,
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
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					RightDegrees:    deg,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := sim.AssignHeading(&HeadingArgs{
					ControllerToken: token,
					Callsign:        callsign,
					Heading:         hdg,
					Turn:            av.TurnRight,
				}); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := sim.AssignSpeed(token, callsign, 0, false); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMIN" {
				if err := sim.MaintainSlowestPractical(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SMAX" {
				if err := sim.MaintainMaximumForward(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SS" {
				if err := sim.SaySpeed(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if len(command) == 6 && command[:2] == "SQ" {
				if sq, err := av.ParseSquawk(command[2:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := sim.ChangeSquawk(token, callsign, sq); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SH" {
				if err := sim.SayHeading(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if command == "SA" {
				if err := sim.SayAltitude(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					rewriteError(err)
					return nil
				} else if err := sim.AssignSpeed(token, callsign, kts, false); err != nil {
					rewriteError(err)
					return nil
				}
			}

		case 'T':
			if command == "TO" {
				if err := sim.ContactTower(token, callsign); err != nil {
					rewriteError(err)
					return nil
				}
			} else if n := len(command); n > 2 {
				if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
					if command[n-1] == 'L' {
						// turn x degrees left
						if err := sim.AssignHeading(&HeadingArgs{
							ControllerToken: token,
							Callsign:        callsign,
							LeftDegrees:     deg,
						}); err != nil {
							rewriteError(err)
							return nil
						} else {
							continue
						}
					} else if command[n-1] == 'R' {
						// turn x degrees right
						if err := sim.AssignHeading(&HeadingArgs{
							ControllerToken: token,
							Callsign:        callsign,
							RightDegrees:    deg,
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
					} else if err := sim.AssignSpeed(token, callsign, kts, true); err != nil {
						rewriteError(err)
						return nil
					}

				case "TA", "TC", "TD":
					if alt, err := strconv.Atoi(command[2:]); err != nil {
						rewriteError(err)
						return nil
					} else if err := sim.AssignAltitude(token, callsign, 100*alt, true); err != nil {
						rewriteError(err)
						return nil
					}

				default:
					rewriteError(ErrInvalidCommandSyntax)
					return nil
				}
			}
		case 'X':
			sim.DeleteAircraft(token, callsign)

		default:
			rewriteError(ErrInvalidCommandSyntax)
			return nil
		}
	}

	return nil
}

type LaunchAircraftArgs struct {
	ControllerToken string
	Aircraft        av.Aircraft
}

func (sd *Dispatcher) LaunchAircraft(ls *LaunchAircraftArgs, _ *struct{}) error {
	sim, ok := sd.sm.controllerTokenToSim[ls.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	sim.LaunchAircraft(ls.Aircraft)
	return nil
}

type CreateDepartureArgs struct {
	ControllerToken string
	Airport         string
	Runway          string
	Category        string
}

func (sd *Dispatcher) CreateDeparture(da *CreateDepartureArgs, depAc *av.Aircraft) error {
	sim, ok := sd.sm.controllerTokenToSim[da.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	ac, err := sim.CreateDeparture(da.Airport, da.Runway, da.Category)
	if err == nil {
		*depAc = *ac
	}
	return err
}

type CreateArrivalArgs struct {
	ControllerToken string
	Group           string
	Airport         string
}

func (sd *Dispatcher) CreateArrival(aa *CreateArrivalArgs, arrAc *av.Aircraft) error {
	sim, ok := sd.sm.controllerTokenToSim[aa.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	ac, err := sim.CreateArrival(aa.Group, aa.Airport)
	if err == nil {
		*arrAc = *ac
	}
	return err
}

type CreateOverflightArgs struct {
	ControllerToken string
	Group           string
}

func (sd *Dispatcher) CreateOverflight(oa *CreateOverflightArgs, ofAc *av.Aircraft) error {
	sim, ok := sd.sm.controllerTokenToSim[oa.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	ac, err := sim.CreateOverflight(oa.Group)
	if err == nil {
		*ofAc = *ac
	}
	return err
}

type RestrictionAreaArgs struct {
	ControllerToken string
	Index           int
	RestrictionArea RestrictionArea
}

func (sd *Dispatcher) CreateRestrictionArea(ra *RestrictionAreaArgs, idx *int) error {
	sim, ok := sd.sm.controllerTokenToSim[ra.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	i, err := sim.CreateRestrictionArea(ra.RestrictionArea)
	if err == nil {
		*idx = i
	}
	return err
}

func (sd *Dispatcher) UpdateRestrictionArea(ra *RestrictionAreaArgs, _ *struct{}) error {
	sim, ok := sd.sm.controllerTokenToSim[ra.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	return sim.UpdateRestrictionArea(ra.Index, ra.RestrictionArea)
}

func (sd *Dispatcher) DeleteRestrictionArea(ra *RestrictionAreaArgs, _ *struct{}) error {
	sim, ok := sd.sm.controllerTokenToSim[ra.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	return sim.DeleteRestrictionArea(ra.Index)
}

type VideoMapsArgs struct {
	ControllerToken string
	Filename        string
}

func (sd *Dispatcher) GetVideoMapLibrary(vm *VideoMapsArgs, vmf *av.VideoMapLibrary) error {
	sim, ok := sd.sm.controllerTokenToSim[vm.ControllerToken]
	if !ok {
		return ErrNoSimForControllerToken
	}
	if v, err := sim.GetVideoMapLibrary(vm.Filename); err == nil {
		*vmf = *v
		return nil
	} else {
		return err
	}
}

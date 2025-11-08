// server/dispatcher.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
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

	execResult := s.RunAircraftControlCommands(tcp, cmds.Callsign, cmds.Commands)
	result.RemainingInput = execResult.RemainingInput
	if execResult.Error != nil {
		result.ErrorMessage = execResult.Error.Error()
	}

	return nil
}

type SetWaypointCommandsArgs struct {
	ControllerToken string
	Commands        string
}

const SetWaypointCommandsRPC = "Sim.SetWaypointCommands"

func (sd *dispatcher) SetWaypointCommands(args *SetWaypointCommandsArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	tcp, s, ok := sd.sm.LookupController(args.ControllerToken)
	if !ok {
		return ErrNoSimForControllerToken
	}

	return s.SetWaypointCommands(tcp, args.Commands)
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
	Filename string
}

const GetVideoMapLibraryRPC = "Sim.GetVideoMapLibrary"

func (sd *dispatcher) GetVideoMapLibrary(vm *VideoMapsArgs, vmf *sim.VideoMapLibrary) error {
	defer sd.sm.lg.CatchAndReportCrash()

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

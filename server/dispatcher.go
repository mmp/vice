// server/dispatcher.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
	"github.com/mmp/vice/util"
)

type dispatcher struct {
	sm *SimManager
}

const GetStateUpdateRPC = "Sim.GetStateUpdate"

func (sd *dispatcher) GetStateUpdate(token string, update *SimStateUpdate) error {
	// Most of the methods in this file are called from the RPC dispatcher,
	// which spawns up goroutines as needed to handle requests, so if we
	// want to catch and report panics, all of the methods need to start
	// like this...
	defer sd.sm.lg.CatchAndReportCrash()

	// GetStateUpdate may return nil if user signs off concurrently.
	if u, err := sd.sm.GetStateUpdate(token); err != nil {
		return err
	} else if u == nil {
		return ErrNoSimForControllerToken
	} else {
		*update = *u
		return nil
	}
}

const SignOffRPC = "Sim.SignOff"

func (sd *dispatcher) SignOff(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.SignOff(token)
}

const TakeOrReturnLaunchControlRPC = "Sim.TakeOrReturnLaunchControl"

func (sd *dispatcher) TakeOrReturnLaunchControl(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.sim.TakeOrReturnLaunchControl(c.tcw)
}

type SetSimRateArgs struct {
	ControllerToken string
	Rate            float32
}

const SetSimRateRPC = "Sim.SetSimRate"

func (sd *dispatcher) SetSimRate(r *SetSimRateArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(r.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.sim.SetSimRate(c.tcw, r.Rate)
}

type SetLaunchConfigArgs struct {
	ControllerToken string
	Config          sim.LaunchConfig
}

const SetLaunchConfigRPC = "Sim.SetLaunchConfig"

func (sd *dispatcher) SetLaunchConfig(lc *SetLaunchConfigArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(lc.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.sim.SetLaunchConfig(c.tcw, lc.Config)
}

const TogglePauseRPC = "Sim.TogglePause"

func (sd *dispatcher) TogglePause(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	c.sim.TogglePause()
	action := util.Select(c.sim.State.Paused, "paused", "unpaused")
	c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s (%s) has %s the sim", c.tcw, c.initials, action))
	return nil
}

const RequestFlightFollowingRPC = "Sim.RequestFlightFollowing"

func (sd *dispatcher) RequestFlightFollowing(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.sim.RequestFlightFollowing()
}

type TriggerEmergencyArgs struct {
	ControllerToken string
	EmergencyName   string
}

const TriggerEmergencyRPC = "Sim.TriggerEmergency"

func (sd *dispatcher) TriggerEmergency(args *TriggerEmergencyArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	c.sim.TriggerEmergency(args.EmergencyName)
	return nil
}

const FastForwardRPC = "Sim.FastForward"

func (sd *dispatcher) FastForward(token string, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	c.sim.FastForward()
	c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s (%s) has fast-forwarded the sim", c.tcw, c.initials))
	*update = c.GetStateUpdate()
	return nil
}

type AssociateFlightPlanArgs struct {
	ControllerToken     string
	Callsign            av.ADSBCallsign
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const AssociateFlightPlanRPC = "Sim.AssociateFlightPlan"

func (sd *dispatcher) AssociateFlightPlan(it *AssociateFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(it.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.AssociateFlightPlan(c.tcw, it.Callsign, it.FlightPlanSpecifier)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type ActivateFlightPlanArgs struct {
	ControllerToken     string
	TrackCallsign       av.ADSBCallsign
	FpACID              sim.ACID
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const ActivateFlightPlanRPC = "Sim.ActivateFlightPlan"

func (sd *dispatcher) ActivateFlightPlan(af *ActivateFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(af.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.ActivateFlightPlan(c.tcw, af.TrackCallsign, af.FpACID, &af.FlightPlanSpecifier)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type CreateFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const CreateFlightPlanRPC = "Sim.CreateFlightPlan"

func (sd *dispatcher) CreateFlightPlan(cfp *CreateFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(cfp.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.CreateFlightPlan(c.tcw, cfp.FlightPlanSpecifier)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type ModifyFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
	ACID                sim.ACID
}

const ModifyFlightPlanRPC = "Sim.ModifyFlightPlan"

func (sd *dispatcher) ModifyFlightPlan(mfp *ModifyFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(mfp.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.ModifyFlightPlan(c.tcw, mfp.ACID, mfp.FlightPlanSpecifier)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
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

func (sd *dispatcher) DeleteFlightPlan(dt *DeleteFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(dt.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.DeleteFlightPlan(c.tcw, dt.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type RepositionTrackArgs struct {
	ControllerToken string
	ACID            sim.ACID        // from
	Callsign        av.ADSBCallsign // to
	Position        math.Point2LL   // to
}

const RepositionTrackRPC = "Sim.RepositionTrack"

func (sd *dispatcher) RepositionTrack(rt *RepositionTrackArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(rt.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.RepositionTrack(c.tcw, rt.ACID, rt.Callsign, rt.Position)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type HandoffArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
}

const HandoffTrackRPC = "Sim.HandoffTrack"

func (sd *dispatcher) HandoffTrack(h *HandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(h.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.HandoffTrack(c.tcw, h.ACID, h.ToPosition)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const RedirectHandoffRPC = "Sim.RedirectHandoff"

func (sd *dispatcher) RedirectHandoff(h *HandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(h.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.RedirectHandoff(c.tcw, h.ACID, h.ToPosition)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const AcceptRedirectedHandoffRPC = "Sim.AcceptRedirectedHandoff"

func (sd *dispatcher) AcceptRedirectedHandoff(po *AcceptHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(po.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.AcceptRedirectedHandoff(c.tcw, po.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type AcceptHandoffArgs ACIDSpecifier

const AcceptHandoffRPC = "Sim.AcceptHandoff"

func (sd *dispatcher) AcceptHandoff(ah *AcceptHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ah.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.AcceptHandoff(c.tcw, ah.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type CancelHandoffArgs ACIDSpecifier

const CancelHandoffRPC = "Sim.CancelHandoff"

func (sd *dispatcher) CancelHandoff(ch *CancelHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ch.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.CancelHandoff(c.tcw, ch.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type PointOutArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
}

type ForceQLArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
}

const ForceQLRPC = "Sim.ForceQL"

func (sd *dispatcher) ForceQL(ql *ForceQLArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ql.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.ForceQL(c.tcw, ql.ACID, ql.ToPosition)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type GlobalMessageArgs struct {
	ControllerToken string
	Message         string
}

const GlobalMessageRPC = "Sim.GlobalMessage"

func (sd *dispatcher) GlobalMessage(gm *GlobalMessageArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(gm.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s(%s): %s", c.initials, c.tcw, gm.Message))
	return nil
}

const PointOutRPC = "Sim.PointOut"

func (sd *dispatcher) PointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(po.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.PointOut(c.tcw, po.ACID, po.ToPosition)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const AcknowledgePointOutRPC = "Sim.AcknowledgePointOut"

func (sd *dispatcher) AcknowledgePointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(po.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.AcknowledgePointOut(c.tcw, po.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const RecallPointOutRPC = "Sim.RecallPointOut"

func (sd *dispatcher) RecallPointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(po.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.RecallPointOut(c.tcw, po.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const RejectPointOutRPC = "Sim.RejectPointOut"

func (sd *dispatcher) RejectPointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(po.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.RejectPointOut(c.tcw, po.ACID)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type HeldDepartureArgs AircraftSpecifier

const ReleaseDepartureRPC = "Sim.ReleaseDeparture"

func (sd *dispatcher) ReleaseDeparture(hd *HeldDepartureArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(hd.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.ReleaseDeparture(c.tcw, hd.Callsign)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type DeleteAircraftArgs AircraftSpecifier

const DeleteAllAircraftRPC = "Sim.DeleteAllAircraft"

func (sd *dispatcher) DeleteAllAircraft(da *DeleteAircraftArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(da.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.DeleteAllAircraft(c.tcw)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type DeleteAircraftListArgs struct {
	ControllerToken string
	Aircraft        []sim.Aircraft
}

const DeleteAircraftRPC = "Sim.DeleteAircraft"

func (sd *dispatcher) DeleteAircraft(da *DeleteAircraftListArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(da.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.DeleteAircraftSlice(c.tcw, da.Aircraft)
	*update = c.GetStateUpdate()
	return err
}

type SendRouteCoordinatesArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Minutes         int
}

const SendRouteCoordinatesRPC = "Sim.SendRouteCoordinates"

func (sd *dispatcher) SendRouteCoordinates(rca *SendRouteCoordinatesArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(rca.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.SendRouteCoordinates(c.tcw, rca.ACID, rca.Minutes)
	*update = c.GetStateUpdate()
	return err
}

type FlightPlanDirectArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Fix             string
}

const FlightPlanDirectRPC = "Sim.FlightPlanDirect"

func (sd *dispatcher) FlightPlanDirect(da *FlightPlanDirectArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(da.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	tcp := c.sim.State.PrimaryPositionForTCW(c.tcw)
	err := c.sim.FlightPlanDirect(tcp, da.Fix, da.ACID)
	*update = c.GetStateUpdate()
	return err
}

type AircraftCommandsArgs struct {
	ControllerToken   string
	Callsign          av.ADSBCallsign
	Commands          string
	Multiple          bool
	ClickedTrack      bool
	EnableTTS         bool          // Whether to synthesize readback audio
	WhisperDuration   time.Duration // Time from PTT release to whisper completion (zero for keyboard input)
	AudioDuration     time.Duration // Duration of the recorded audio (zero for keyboard input)
	WhisperTranscript string        // Raw whisper transcript (empty for keyboard input)
	WhisperProcessor  string        // Description of the processor running whisper (GPU model or CPU info)
	WhisperModel      string
	AircraftContext   map[string]stt.Aircraft // Aircraft context used for STT decoding (for logging)
	STTDebugLogs      []string                // Local STT processing logs (for logging)
}

// If an RPC call returns an error, then the result argument is not returned(!?).
// So we don't use the error type for syntax errors...
type AircraftCommandsResult struct {
	ErrorMessage      string
	RemainingInput    string
	ReadbackText      string          // Text for client to synthesize
	ReadbackVoiceName string          // Voice name for synthesis (e.g., "am_adam")
	ReadbackCallsign  av.ADSBCallsign // Callsign for the readback
}

const RunAircraftCommandsRPC = "Sim.RunAircraftCommands"

func (sd *dispatcher) RunAircraftCommands(cmds *AircraftCommandsArgs, result *AircraftCommandsResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(cmds.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	callsign := cmds.Callsign

	rewriteError := func(err error) {
		result.RemainingInput = cmds.Commands
		if err != nil {
			result.ErrorMessage = err.Error()
		}
	}

	// Helper to populate readback fields for client-side TTS synthesis.
	setReadback := func(spokenText string) {
		if cmds.EnableTTS && spokenText != "" {
			result.ReadbackText = spokenText
			result.ReadbackVoiceName = c.sim.VoiceAssigner.GetVoice(callsign, c.sim.Rand)
			result.ReadbackCallsign = callsign
		}
	}

	if cmds.Multiple {
		spokenText, err := c.sim.PilotMixUp(c.tcw, callsign)
		if err != nil {
			rewriteError(err)
		}
		setReadback(spokenText)
		return nil // don't continue with the commands
	} else if !cmds.ClickedTrack && c.sim.ShouldTriggerPilotMixUp(callsign) {
		spokenText, err := c.sim.PilotMixUp(c.tcw, callsign)
		if err != nil {
			rewriteError(err)
		}
		setReadback(spokenText)
		return nil // don't continue with the commands
	}

	execResult := c.sim.RunAircraftControlCommands(c.tcw, cmds.Callsign, cmds.Commands)
	result.RemainingInput = execResult.RemainingInput
	if execResult.Error != nil {
		result.ErrorMessage = execResult.Error.Error()
	}
	setReadback(execResult.ReadbackSpokenText)

	// Log whisper STT commands (WhisperDuration is non-zero for voice commands)
	if cmds.WhisperDuration > 0 {
		sd.sm.lg.Info("STT command",
			slog.String("transcript", cmds.WhisperTranscript),
			slog.Float64("whisper_duration_ms", float64(cmds.WhisperDuration.Microseconds())/1000.0),
			slog.Float64("audio_duration_ms", float64(cmds.AudioDuration.Microseconds())/1000.0),
			slog.String("processor", cmds.WhisperProcessor),
			slog.String("whisper_model", cmds.WhisperModel),
			slog.String("callsign", string(cmds.Callsign)),
			slog.String("command", cmds.Commands),
			slog.Any("stt_aircraft", cmds.AircraftContext),
			slog.Any("logs", cmds.STTDebugLogs))
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

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.sim.SetWaypointCommands(c.tcw, args.Commands)
}

type LaunchAircraftArgs struct {
	ControllerToken string
	Aircraft        sim.Aircraft
	DepartureRunway string
}

const LaunchAircraftRPC = "Sim.LaunchAircraft"

func (sd *dispatcher) LaunchAircraft(ls *LaunchAircraftArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ls.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	c.sim.LaunchAircraft(ls.Aircraft, ls.DepartureRunway)
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

	c := sd.sm.LookupController(da.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	var ac *sim.Aircraft
	var err error
	if da.Rules == av.FlightRulesIFR {
		ac, err = c.sim.CreateIFRDeparture(da.Airport, da.Runway, da.Category)
	} else {
		ac, err = c.sim.CreateVFRDeparture(da.Airport)
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

	c := sd.sm.LookupController(aa.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	ac, err := c.sim.CreateArrival(aa.Group, aa.Airport)
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

	c := sd.sm.LookupController(oa.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	ac, err := c.sim.CreateOverflight(oa.Group)
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
	StateUpdate SimStateUpdate
}

const CreateRestrictionAreaRPC = "Sim.CreateRestrictionArea"

func (sd *dispatcher) CreateRestrictionArea(ra *RestrictionAreaArgs, result *CreateRestrictionAreaResultArgs) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ra.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	i, err := c.sim.CreateRestrictionArea(ra.RestrictionArea)
	if err != nil {
		return err
	}
	result.Index = i
	result.StateUpdate = c.GetStateUpdate()
	return nil
}

const UpdateRestrictionAreaRPC = "Sim.UpdateRestrictionArea"

func (sd *dispatcher) UpdateRestrictionArea(ra *RestrictionAreaArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ra.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.UpdateRestrictionArea(ra.Index, ra.RestrictionArea)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

const DeleteRestrictionAreaRPC = "Sim.DeleteRestrictionArea"

func (sd *dispatcher) DeleteRestrictionArea(ra *RestrictionAreaArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(ra.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.DeleteRestrictionArea(ra.Index)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
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

	c := sd.sm.LookupController(as.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	var err error
	*state, err = c.sim.GetAircraftDisplayState(as.Callsign)
	return err
}

type ConsolidateTCPArgs struct {
	ControllerToken string
	ReceivingTCW    sim.TCW
	SendingTCP      sim.TCP
	Type            sim.ConsolidationType
}

const ConsolidateTCPRPC = "Sim.ConsolidateTCP"

func (sd *dispatcher) ConsolidateTCP(args *ConsolidateTCPArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.ConsolidateTCP(args.ReceivingTCW, args.SendingTCP, args.Type)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type DeconsolidateTCPArgs struct {
	ControllerToken string
	TCP             sim.TCP // TCP to deconsolidate (optional - if empty, deconsolidate user's own TCP)
}

const DeconsolidateTCPRPC = "Sim.DeconsolidateTCP"

func (sd *dispatcher) DeconsolidateTCP(args *DeconsolidateTCPArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	err := c.sim.DeconsolidateTCP(c.tcw, args.TCP)
	if err == nil {
		*update = c.GetStateUpdate()
	}
	return err
}

type ATPAConfigArgs struct {
	ControllerToken string
	Op              sim.ATPAConfigOp
	VolumeId        string
}

type ATPAConfigResult struct {
	SimStateUpdate
	Output string
}

const ConfigureATPARPC = "Sim.ConfigureATPA"

func (sd *dispatcher) ConfigureATPA(args *ATPAConfigArgs, result *ATPAConfigResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	var err error
	result.Output, err = c.sim.ConfigureATPA(args.Op, args.VolumeId)
	if err == nil {
		result.SimStateUpdate = c.GetStateUpdate()
	}
	return err
}

// STTBugReportArgs contains data for an STT bug report.
type STTBugReportArgs struct {
	ControllerToken string
	PrevTranscript  string                  // Transcript of the previous transmission
	PrevCommand     string                  // Decoded command from previous transmission
	AircraftContext map[string]stt.Aircraft // Aircraft context used for decoding
	DebugLogs       []string                // Debug log lines from the decode
	UserExplanation string                  // User's explanation of the issue
	ReportTime      time.Time

	// GPU and performance information
	GPUInfo           whisper.GPUInfo // GPU acceleration status and devices
	WhisperModelName  string          // Name of the whisper model being used
	RecentDurations   []time.Duration // Recent whisper transcription durations
	IsSlowPerformance bool            // True if this is an automatic slow performance report
}

type RequestContactArgs struct {
	ControllerToken string
}

type RequestContactResult struct {
	ContactText      string          // Text to synthesize
	ContactVoiceName string          // Voice name for synthesis (e.g., "am_adam")
	ContactCallsign  av.ADSBCallsign // Callsign of the aircraft
	ContactType      av.RadioTransmissionType
}

const RequestContactTransmissionRPC = "Sim.RequestContactTransmission"

func (sd *dispatcher) RequestContactTransmission(args *RequestContactArgs, result *RequestContactResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	// Request a contact from the session - returns text and voice name for client-side synthesis
	result.ContactText, result.ContactVoiceName, result.ContactCallsign, result.ContactType = c.session.RequestContact(c.tcw)
	return nil
}

const ReportSTTBugRPC = "Sim.ReportSTTBug"

func (sd *dispatcher) ReportSTTBug(args *STTBugReportArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	// Format GPU device info for logging
	gpuDevices := util.MapSlice(args.GPUInfo.Devices, func(dev whisper.GPUDeviceInfo) string {
		return fmt.Sprintf("%s (idx=%d, type=%s, mem=%dMB/%dMB)",
			dev.Description, dev.Index, dev.DeviceType, dev.FreeMemory/(1024*1024), dev.TotalMemory/(1024*1024))
	})

	// Format recent durations for logging
	durationsStr := util.MapSlice(args.RecentDurations, func(d time.Duration) string { return d.String() })

	// Format aircraft context for logging
	aircraftStr := util.MapSlice(util.SortedMapKeys(args.AircraftContext), func(telephony string) string {
		ac := args.AircraftContext[telephony]
		return fmt.Sprintf("%s: %s state=%s alt=%d", telephony, ac.Callsign, ac.State, ac.Altitude)
	})

	reportType := util.Select(args.IsSlowPerformance, "slow_performance", "user")
	logFunc := util.Select(args.IsSlowPerformance, sd.sm.lg.Warn, sd.sm.lg.Info)

	logFunc("STT Bug Report",
		slog.String("type", reportType),
		slog.String("tcw", string(c.tcw)),
		slog.String("transcript", args.PrevTranscript),
		slog.String("decoded_command", args.PrevCommand),
		slog.String("user_explanation", args.UserExplanation),
		slog.Time("report_time", args.ReportTime),
		// GPU info
		slog.String("whisper_model", args.WhisperModelName),
		slog.Bool("gpu_enabled", args.GPUInfo.Enabled),
		slog.Int("gpu_selected_idx", args.GPUInfo.SelectedIndex),
		slog.String("gpu_devices", strings.Join(gpuDevices, "; ")),
		slog.String("recent_durations", strings.Join(durationsStr, ", ")),
		// STT context
		slog.String("debug_logs", strings.Join(args.DebugLogs, "\n")),
		slog.String("aircraft_context", strings.Join(aircraftStr, "; ")),
	)

	return nil
}

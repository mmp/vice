// server/dispatcher.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"log/slog"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

type dispatcher struct {
	sm *SimManager
}

// runSimCommand applies a controller's request to the sim and packages the
// result for the client: the state update if the sim carried the request out,
// or the reason it refused. A refusal is reported in the reply and not as the RPC's
// error, since an RPC error means the call itself didn't complete. This
// covers the RPCs that deliver a state update; the handful that reply with
// struct{} still return a refusal as the RPC error, though no caller reads it.
func (sd *dispatcher) runSimCommand(token string, update *SimStateUpdate, f func(*controllerContext) error) error {
	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	if err := c.session.apply(func() error { return f(c) }); err != nil {
		update.SimErrorMessage = err.Error()
	} else {
		*update = sd.sm.StateUpdateFor(c)
	}
	return nil
}

const GetStateUpdateRPC = "Sim.GetStateUpdate"

func (sd *dispatcher) GetStateUpdate(token string, update *SimStateUpdate) error {
	// Most of the methods in this file are called from the RPC dispatcher,
	// which spawns up goroutines as needed to handle requests, so if we
	// want to catch and report panics, all of the methods need to start
	// like this...
	defer sd.sm.lg.CatchAndReportCrash()

	u, err := sd.sm.GetStateUpdate(token)
	if err != nil {
		return err
	}
	*update = *u
	return nil
}

const SignOffRPC = "Sim.SignOff"

func (sd *dispatcher) SignOff(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.sm.SignOff(token)
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
	var err error
	c.session.withSim(func() { err = c.sim.SetSimRate(c.tcw, r.Rate) })
	return err
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
	// The flights are looked up inside the step as well, so that no other
	// launch config change can come between deciding whether the traffic
	// source changes and making the change.
	return c.session.apply(func() error {
		published := c.sim.PublishedFlightsFor(lc.Config)
		return c.sim.SetLaunchConfig(c.tcw, lc.Config, published)
	})
}

const TogglePauseRPC = "Sim.TogglePause"

func (sd *dispatcher) TogglePause(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	c.session.withSim(func() {
		action := util.Select(c.sim.TogglePause(), "paused", "unpaused")
		c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s (%s) has %s the sim", c.tcw, c.initials, action))
	})
	return nil
}

const RequestFlightFollowingRPC = "Sim.RequestFlightFollowing"

func (sd *dispatcher) RequestFlightFollowing(token string, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.session.apply(c.sim.RequestFlightFollowing)
}

type AddMETARAirportArgs struct {
	ControllerToken string
	Airport         av.ICAOAirportCode
}

const AddMETARAirportRPC = "Sim.AddMETARAirport"

func (sd *dispatcher) AddMETARAirport(args *AddMETARAirportArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.session.apply(func() error {
		metar, err := c.sim.METARWindow(args.Airport)
		if err != nil {
			c.session.lg.Errorf("%s: %v", args.Airport, err)
		}
		return c.sim.AddMETAR(args.Airport, metar)
	})
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
	return c.session.apply(func() error {
		c.sim.TriggerEmergency(args.EmergencyName)
		return nil
	})
}

const FastForwardRPC = "Sim.FastForward"

func (sd *dispatcher) FastForward(token string, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	// Not recorded as a request: the ticks it runs are recorded.
	c.session.withSim(func() {
		c.sim.FastForward()
		c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s (%s) has fast-forwarded the sim", c.tcw, c.initials))
	})
	*update = sd.sm.StateUpdateFor(c)
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

	return sd.runSimCommand(it.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.AssociateFlightPlan(c.tcw, it.Callsign, it.FlightPlanSpecifier)
	})
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

	return sd.runSimCommand(af.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.ActivateFlightPlan(c.tcw, af.TrackCallsign, af.FpACID, &af.FlightPlanSpecifier)
	})
}

type CreateFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
}

const CreateFlightPlanRPC = "Sim.CreateFlightPlan"

func (sd *dispatcher) CreateFlightPlan(cfp *CreateFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(cfp.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.CreateFlightPlan(c.tcw, cfp.FlightPlanSpecifier)
	})
}

type CreateInterfacilityVFRArgs struct {
	ControllerToken string
	ACID            sim.ACID
	IsIntermediate  bool
	RequestedAlt    int
}

const CreateInterfacilityVFRRPC = "Sim.CreateInterfacilityVFR"

func (sd *dispatcher) CreateInterfacilityVFR(args *CreateInterfacilityVFRArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.CreateInterfacilityVFR(c.tcw, args.ACID, args.IsIntermediate, args.RequestedAlt)
	})
}

type ModifyFlightPlanArgs struct {
	ControllerToken     string
	FlightPlanSpecifier sim.FlightPlanSpecifier
	ACID                sim.ACID
}

const ModifyFlightPlanRPC = "Sim.ModifyFlightPlan"

func (sd *dispatcher) ModifyFlightPlan(mfp *ModifyFlightPlanArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(mfp.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.ModifyFlightPlan(c.tcw, mfp.ACID, mfp.FlightPlanSpecifier)
	})
}

type UpdateATISGITextArgs struct {
	ControllerToken string
	Line            int
	Auxiliary       bool
	ATIS            *string
	GIText          *string
}

const UpdateATISGITextRPC = "Sim.UpdateATISGIText"

func (sd *dispatcher) UpdateATISGIText(args *UpdateATISGITextArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	var err error
	c.session.withSim(func() { err = c.sim.UpdateATISGIText(c.tcw, args.Line, args.Auxiliary, args.ATIS, args.GIText) })
	if err != nil {
		update.SimErrorMessage = err.Error()
	} else {
		*update = sd.sm.StateUpdateFor(c)
	}
	return nil
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

	return sd.runSimCommand(dt.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.DeleteFlightPlan(c.tcw, dt.ACID)
	})
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

	return sd.runSimCommand(rt.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.RepositionTrack(c.tcw, rt.ACID, rt.Callsign, rt.Position)
	})
}

type HandoffArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
}

const HandoffTrackRPC = "Sim.HandoffTrack"

func (sd *dispatcher) HandoffTrack(h *HandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(h.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.HandoffTrack(c.tcw, h.ACID, h.ToPosition)
	})
}

const RedirectHandoffRPC = "Sim.RedirectHandoff"

func (sd *dispatcher) RedirectHandoff(h *HandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(h.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.RedirectHandoff(c.tcw, h.ACID, h.ToPosition)
	})
}

const AcceptRedirectedHandoffRPC = "Sim.AcceptRedirectedHandoff"

func (sd *dispatcher) AcceptRedirectedHandoff(po *AcceptHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(po.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.AcceptRedirectedHandoff(c.tcw, po.ACID)
	})
}

type AcceptHandoffArgs ACIDSpecifier

const AcceptHandoffRPC = "Sim.AcceptHandoff"

func (sd *dispatcher) AcceptHandoff(ah *AcceptHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ah.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.AcceptHandoff(c.tcw, ah.ACID)
	})
}

type CancelHandoffArgs ACIDSpecifier

const CancelHandoffRPC = "Sim.CancelHandoff"

func (sd *dispatcher) CancelHandoff(ch *CancelHandoffArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ch.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.CancelHandoff(c.tcw, ch.ACID)
	})
}

type PointOutArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
	// FromPosition is the originator of the point out to acknowledge; all
	// are acknowledged if it is empty.
	FromPosition sim.ControlPosition
}

type ForceQLArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToPosition      sim.ControlPosition
}

const ForceQLRPC = "Sim.ForceQL"

func (sd *dispatcher) ForceQL(ql *ForceQLArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ql.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.ForceQL(c.tcw, ql.ACID, ql.ToPosition)
	})
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
	c.session.withSim(func() { c.sim.GlobalMessage(c.tcw, fmt.Sprintf("%s(%s): %s", c.initials, c.tcw, gm.Message)) })
	return nil
}

const PointOutRPC = "Sim.PointOut"

func (sd *dispatcher) PointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(po.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.PointOut(c.tcw, po.ACID, po.ToPosition)
	})
}

const AcknowledgePointOutRPC = "Sim.AcknowledgePointOut"

func (sd *dispatcher) AcknowledgePointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(po.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.AcknowledgePointOut(c.tcw, po.ACID, po.FromPosition)
	})
}

const RecallPointOutRPC = "Sim.RecallPointOut"

func (sd *dispatcher) RecallPointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(po.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.RecallPointOut(c.tcw, po.ACID)
	})
}

const RejectPointOutRPC = "Sim.RejectPointOut"

func (sd *dispatcher) RejectPointOut(po *PointOutArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(po.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.RejectPointOut(c.tcw, po.ACID)
	})
}

type HeldDepartureArgs AircraftSpecifier

const ReleaseDepartureRPC = "Sim.ReleaseDeparture"

func (sd *dispatcher) ReleaseDeparture(hd *HeldDepartureArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(hd.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.ReleaseDeparture(c.tcw, hd.Callsign)
	})
}

type DeleteAircraftArgs AircraftSpecifier

const DeleteAllAircraftRPC = "Sim.DeleteAllAircraft"

func (sd *dispatcher) DeleteAllAircraft(da *DeleteAircraftArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(da.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.DeleteAllAircraft(c.tcw)
	})
}

type SendRouteCoordinatesArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Minutes         int
}

const SendRouteCoordinatesRPC = "Sim.SendRouteCoordinates"

func (sd *dispatcher) SendRouteCoordinates(rca *SendRouteCoordinatesArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(rca.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.SendRouteCoordinates(c.tcw, rca.ACID, rca.Minutes)
	})
}

type FlightPlanDirectArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Fixes           []string
	IssueDirect     bool
	EnableTTS       bool // Whether to synthesize readback audio
}

type FlightPlanDirectResult struct {
	SimStateUpdate
	ReadbackText      string          // Text for client to synthesize
	ReadbackVoiceName string          // Voice name for synthesis (e.g., "am_adam")
	ReadbackCallsign  av.ADSBCallsign // Callsign for the readback
}

const FlightPlanDirectRPC = "Sim.FlightPlanDirect"

func (sd *dispatcher) FlightPlanDirect(da *FlightPlanDirectArgs, result *FlightPlanDirectResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(da.ControllerToken, &result.SimStateUpdate, func(c *controllerContext) error {
		callsign, readback, err := c.sim.FlightPlanDirect(c.tcw, da.ACID, da.Fixes, da.IssueDirect)
		if da.EnableTTS && readback != "" {
			result.ReadbackText = readback
			result.ReadbackVoiceName = c.sim.GetReadbackVoice(callsign)
			result.ReadbackCallsign = callsign
		}
		return err
	})
}

type AircraftCommandsArgs struct {
	ControllerToken   string
	Callsign          av.ADSBCallsign
	Commands          string
	Multiple          bool
	ClickedTrack      bool
	EnableTTS         bool          // Whether to synthesize readback audio
	WhisperDuration   time.Duration // Time from PTT release until the transcript was decoded (zero for keyboard input)
	AudioDuration     time.Duration // Duration of the recorded audio (zero for keyboard input)
	WhisperTranscript string        // Raw whisper transcript (empty for keyboard input)
	WhisperPrompt     string        // Initial prompt given to whisper (empty for keyboard input)
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

	_ = c.session.apply(func() error {
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
				result.ReadbackVoiceName = c.sim.GetReadbackVoice(callsign)
				result.ReadbackCallsign = callsign
			}
		}

		if cmds.Multiple || (!cmds.ClickedTrack && c.sim.ShouldTriggerPilotMixUp(callsign)) {
			spokenText, err := c.sim.PilotMixUp(c.tcw, callsign)
			if err != nil {
				rewriteError(err)
			}
			setReadback(spokenText)
			return err
		}

		execResult := c.sim.RunAircraftControlCommands(c.tcw, cmds.Callsign, cmds.Commands, cmds.AudioDuration,
			cmds.WhisperDuration, cmds.WhisperTranscript)
		result.RemainingInput = execResult.RemainingInput
		if execResult.Error != nil {
			result.ErrorMessage = execResult.Error.Error()
		}
		// Use execResult's callsign for voice lookup (not the local callsign, which may be "ROLLBACK")
		if cmds.EnableTTS && execResult.ReadbackSpokenText != "" {
			cs := execResult.ReadbackCallsign
			result.ReadbackText = execResult.ReadbackSpokenText
			result.ReadbackVoiceName = c.sim.GetReadbackVoice(cs)
			result.ReadbackCallsign = cs
		}
		return execResult.Error
	})

	// Log whisper STT commands (WhisperDuration is non-zero for voice commands)
	if cmds.WhisperDuration > 0 {
		sd.sm.lg.Info("STT command",
			slog.String("transcript", cmds.WhisperTranscript),
			slog.String("whisper_prompt", cmds.WhisperPrompt),
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
	return c.session.apply(func() error { return c.sim.SetWaypointCommands(c.tcw, args.Commands) })
}

type LaunchAircraftArgs struct {
	ControllerToken string
	Flight          sim.LaunchFlight
}

const LaunchAircraftRPC = "Sim.LaunchAircraft"

func (sd *dispatcher) LaunchAircraft(ls *LaunchAircraftArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ls.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.LaunchAircraft(c.tcw, ls.Flight)
	})
}

type RecycleLaunchAircraftArgs LaunchAircraftArgs

const RecycleLaunchAircraftRPC = "Sim.RecycleLaunchAircraft"

func (sd *dispatcher) RecycleLaunchAircraft(rs *RecycleLaunchAircraftArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(rs.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.RecycleLaunchAircraft(c.tcw, rs.Flight)
	})
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

	return sd.runSimCommand(ra.ControllerToken, &result.StateUpdate, func(c *controllerContext) error {
		i, err := c.sim.CreateRestrictionArea(ra.RestrictionArea)
		result.Index = i
		return err
	})
}

const UpdateRestrictionAreaRPC = "Sim.UpdateRestrictionArea"

func (sd *dispatcher) UpdateRestrictionArea(ra *RestrictionAreaArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ra.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.UpdateRestrictionArea(ra.Index, ra.RestrictionArea)
	})
}

const DeleteRestrictionAreaRPC = "Sim.DeleteRestrictionArea"

func (sd *dispatcher) DeleteRestrictionArea(ra *RestrictionAreaArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(ra.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.DeleteRestrictionArea(ra.Index)
	})
}

type MapLibraryArgs struct {
	Filename string
}

const GetMapLibraryRPC = "Sim.GetMapLibrary"

func (sd *dispatcher) GetMapLibrary(vm *MapLibraryArgs, vmf *videomaps.Library) error {
	defer sd.sm.lg.CatchAndReportCrash()

	if v, err := videomaps.LoadLibrary(vm.Filename); err == nil {
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
	c.session.withSim(func() { *state, err = c.sim.GetAircraftDisplayState(as.Callsign) })
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

	return sd.runSimCommand(args.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.ConsolidateTCP(args.ReceivingTCW, args.SendingTCP, args.Type)
	})
}

type DeconsolidateTCPArgs struct {
	ControllerToken string
	TCP             sim.TCP // TCP to deconsolidate (optional - if empty, deconsolidate user's own TCP)
}

const DeconsolidateTCPRPC = "Sim.DeconsolidateTCP"

func (sd *dispatcher) DeconsolidateTCP(args *DeconsolidateTCPArgs, update *SimStateUpdate) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, update, func(c *controllerContext) error {
		return c.sim.DeconsolidateTCP(c.tcw, args.TCP)
	})
}

type ATPAConfigArgs struct {
	ControllerToken string
	Op              sim.ATPAConfigOp
	Id              string // ATPA volume, or runway for the 2.5nm ops
}

type ATPAConfigResult struct {
	SimStateUpdate
	Output string
}

const ConfigureATPARPC = "Sim.ConfigureATPA"

func (sd *dispatcher) ConfigureATPA(args *ATPAConfigArgs, result *ATPAConfigResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, &result.SimStateUpdate, func(c *controllerContext) error {
		var err error
		result.Output, err = c.sim.ConfigureATPA(args.Op, args.Id)
		return err
	})
}

type FDAMConfigArgs struct {
	ControllerToken string
	Op              sim.FDAMConfigOp
	RegionId        string
}

type FDAMConfigResult struct {
	SimStateUpdate
	Output string
}

const ConfigureFDAMRPC = "Sim.ConfigureFDAM"

func (sd *dispatcher) ConfigureFDAM(args *FDAMConfigArgs, result *FDAMConfigResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, &result.SimStateUpdate, func(c *controllerContext) error {
		var err error
		result.Output, err = c.sim.ConfigureFDAM(args.Op, args.RegionId)
		return err
	})
}

type AutoHandoffConfigArgs struct {
	ControllerToken string
	Op              sim.AutoHandoffOp
	Enable          bool
}

type AutoHandoffConfigResult struct {
	SimStateUpdate
	Output string
}

const ConfigureAutoHandoffRPC = "Sim.ConfigureAutoHandoff"

func (sd *dispatcher) ConfigureAutoHandoff(args *AutoHandoffConfigArgs, result *AutoHandoffConfigResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, &result.SimStateUpdate, func(c *controllerContext) error {
		var err error
		result.Output, err = c.sim.ConfigureAutoHandoff(c.tcw, args.Op, args.Enable)
		return err
	})
}

type TakePilotTransmissionArgs struct {
	ControllerToken string
	Transmission    sim.PilotTransmission
}

type TakePilotTransmissionResult struct {
	SimStateUpdate
	Taken bool // whether the transmission was still next, and so is to be played
}

const TakePilotTransmissionRPC = "Sim.TakePilotTransmission"

// TakePilotTransmission is called by client code when it is ready to play the audio for a
// pilot radio transmission (note: not a readback, which is handled separately.) It checks
// if the transmission is still the first in the transmission queue and removes it if so,
// with the Taken return value indicating success. If false is returned, the client should
// not play the transmission audio; it may be from a deleted aircraft or it may no longer
// be relevant.
func (sd *dispatcher) TakePilotTransmission(args *TakePilotTransmissionArgs, result *TakePilotTransmissionResult) error {
	defer sd.sm.lg.CatchAndReportCrash()

	return sd.runSimCommand(args.ControllerToken, &result.SimStateUpdate, func(c *controllerContext) error {
		result.Taken = c.sim.TakePilotTransmission(c.tcw, args.Transmission)
		return nil
	})
}

type PushFlightStripArgs struct {
	ControllerToken string
	ACID            sim.ACID
	ToTCP           sim.TCP
}

const PushFlightStripRPC = "Sim.PushFlightStrip"

func (sd *dispatcher) PushFlightStrip(args *PushFlightStripArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	return c.session.apply(func() error { return c.sim.PushFlightStrip(c.tcw, args.ACID, args.ToTCP) })
}

type AnnotateFlightStripArgs struct {
	ControllerToken string
	ACID            sim.ACID
	Annotations     [9]string
}

const AnnotateFlightStripRPC = "Sim.AnnotateFlightStrip"

func (sd *dispatcher) AnnotateFlightStrip(args *AnnotateFlightStripArgs, _ *struct{}) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(args.ControllerToken)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	var err error
	c.session.withSim(func() { err = c.sim.AnnotateFlightStrip(c.tcw, args.ACID, args.Annotations) })
	return err
}

const RecordFlightsRPC = "Sim.RecordFlights"

func (sd *dispatcher) RecordFlights(token string, recordings *sim.FlightRecordings) error {
	defer sd.sm.lg.CatchAndReportCrash()

	c := sd.sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}
	c.session.withSim(func() { *recordings = c.sim.RecordFlights() })
	return nil
}

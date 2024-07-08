package sim

import (
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type Proxy struct {
	ControllerToken string
	Client          *util.RPCClient
}

func (s *Proxy) TogglePause() *rpc.Call {
	return s.Client.Go("Sim.TogglePause", s.ControllerToken, nil, nil)
}

func (s *Proxy) SignOff(_, _ *struct{}) error {
	if err := s.Client.CallWithTimeout("Sim.SignOff", s.ControllerToken, nil); err != nil {
		return err
	}
	// FIXME: this is handing in zstd code. Why?
	// return s.Client.Close()
	return nil
}

func (s *Proxy) ChangeControlPosition(callsign string, keepTracks bool) error {
	return s.Client.CallWithTimeout("Sim.ChangeControlPosition",
		&ChangeControlPositionArgs{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
			KeepTracks:      keepTracks,
		}, nil)
}

func (s *Proxy) GetSerializeSim() (*Sim, error) {
	var sim Sim
	err := s.Client.CallWithTimeout("SimManager.GetSerializeSim", s.ControllerToken, &sim)
	return &sim, err
}

func (s *Proxy) GetWorldUpdate(wu *WorldUpdate) *rpc.Call {
	return s.Client.Go("Sim.GetWorldUpdate", s.ControllerToken, wu, nil)
}

func (s *Proxy) SetSimRate(r float32) *rpc.Call {
	return s.Client.Go("Sim.SetSimRate",
		&SetSimRateArgs{
			ControllerToken: s.ControllerToken,
			Rate:            r,
		}, nil, nil)
}

func (s *Proxy) SetLaunchConfig(lc LaunchConfig) *rpc.Call {
	return s.Client.Go("Sim.SetLaunchConfig",
		&SetLaunchConfigArgs{
			ControllerToken: s.ControllerToken,
			Config:          lc,
		}, nil, nil)
}

func (s *Proxy) TakeOrReturnLaunchControl() *rpc.Call {
	return s.Client.Go("Sim.TakeOrReturnLaunchControl", s.ControllerToken, nil, nil)
}

func (s *Proxy) SetGlobalLeaderLine(callsign string, direction *math.CardinalOrdinalDirection) *rpc.Call {
	return s.Client.Go("Sim.SetGlobalLeaderLine", &SetGlobalLeaderLineArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Direction:       direction,
	}, nil, nil)
}

func (s *Proxy) SetScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetScratchpad", &SetScratchpadArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *Proxy) SetSecondaryScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetSecondaryScratchpad", &SetScratchpadArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *Proxy) AutoAssociateFP(callsign string, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.AutoAssociateFP", &InitiateTrackArgs{
		AircraftSpecifier: AircraftSpecifier{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
		},
		Plan: fp,
	}, nil, nil)
}

func (s *Proxy) CreateUnsupportedTrack(callsign string, ut *UnsupportedTrack) *rpc.Call {
	return s.Client.Go("Sim.CreateUnsupportedTrack", &CreateUnsupportedTrackArgs{
		ControllerToken:  s.ControllerToken,
		Callsign:         callsign,
		UnsupportedTrack: ut,
	}, nil, nil)
}

func (s *Proxy) UploadFlightPlan(Type int, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.UploadFlightPlan", &UploadPlanArgs{
		ControllerToken: s.ControllerToken,
		Type:            Type,
		Plan:            fp,
	}, nil, nil)
}

func (s *Proxy) InitiateTrack(callsign string, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", InitiateTrackArgs{
		AircraftSpecifier: AircraftSpecifier{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
		},
		Plan: fp,
	}, nil, nil)
}

type IntermTrackArgs struct {
	Token, Callsign, Initial string
	fp                       *STARSFlightPlan
}

func (s *Proxy) IntermTrack(callsign, initial string, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", IntermTrackArgs{
		Token:    s.ControllerToken,
		Callsign: callsign,
		Initial:  initial,
		fp:       fp,
	}, nil, nil)
}

func (s *Proxy) DropTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DropTrack", &DropTrackArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.HandoffTrack", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *Proxy) AcceptHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptHandoff", &AcceptHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) CancelHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.CancelHandoff", &CancelHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) GlobalMessage(global GlobalMessage) *rpc.Call {
	return s.Client.Go("Sim.GlobalMessage", &GlobalMessageArgs{
		ControllerToken: s.ControllerToken,
		Message:         global.Message,
		FromController:  global.FromController,
	}, nil, nil)
}

func (s *Proxy) ForceQL(callsign, controller string) *rpc.Call {
	return s.Client.Go("Sim.ForceQL", &ForceQLArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *Proxy) RedirectHandoff(callsign, controller string) *rpc.Call {
	return s.Client.Go("Sim.RedirectHandoff", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *Proxy) AcceptRedirectedHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptRedirectedHandoff", &AcceptHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) PointOut(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.PointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *Proxy) AcknowledgePointOut(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcknowledgePointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) RejectPointOut(callsign string) *rpc.Call {
	return s.Client.Go("Sim.RejectPointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *Proxy) ToggleSPCOverride(callsign string, spc string) *rpc.Call {
	return s.Client.Go("Sim.ToggleSPCOverride", &ToggleSPCArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		SPC:             spc,
	}, nil, nil)
}

func (s *Proxy) SetTemporaryAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetTemporaryAltitude", &AssignAltitudeArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *Proxy) DeleteAllAircraft() *rpc.Call {
	return s.Client.Go("Sim.DeleteAllAircraft", &DeleteAircraftArgs{
		ControllerToken: s.ControllerToken,
	}, nil, nil)
}

func (s *Proxy) RunAircraftCommands(callsign string, cmds string, result *AircraftCommandsResult) *rpc.Call {
	return s.Client.Go("Sim.RunAircraftCommands", &AircraftCommandsArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, result, nil)
}

func (s *Proxy) LaunchAircraft(ac av.Aircraft) *rpc.Call {
	return s.Client.Go("Sim.LaunchAircraft", &LaunchAircraftArgs{
		ControllerToken: s.ControllerToken,
		Aircraft:        ac,
	}, nil, nil)
}

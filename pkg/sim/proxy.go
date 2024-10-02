// pkg/sim/proxy.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type proxy struct {
	ControllerToken string
	Client          *util.RPCClient
}

func (s *proxy) TogglePause() *rpc.Call {
	return s.Client.Go("Sim.TogglePause", s.ControllerToken, nil, nil)
}

func (s *proxy) SignOff(_, _ *struct{}) error {
	if err := s.Client.CallWithTimeout("Sim.SignOff", s.ControllerToken, nil); err != nil {
		return err
	}
	// FIXME: this is handing in zstd code. Why?
	// return s.Client.Close()
	return nil
}

func (s *proxy) ChangeControlPosition(callsign string, keepTracks bool) error {
	return s.Client.CallWithTimeout("Sim.ChangeControlPosition",
		&ChangeControlPositionArgs{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
			KeepTracks:      keepTracks,
		}, nil)
}

func (s *proxy) GetSerializeSim() (*Sim, error) {
	var sim Sim
	err := s.Client.CallWithTimeout("SimManager.GetSerializeSim", s.ControllerToken, &sim)
	return &sim, err
}

func (s *proxy) GetWorldUpdate(wu *WorldUpdate) *rpc.Call {
	return s.Client.Go("Sim.GetWorldUpdate", s.ControllerToken, wu, nil)
}

func (s *proxy) SetSimRate(r float32) *rpc.Call {
	return s.Client.Go("Sim.SetSimRate",
		&SetSimRateArgs{
			ControllerToken: s.ControllerToken,
			Rate:            r,
		}, nil, nil)
}

func (s *proxy) SetLaunchConfig(lc LaunchConfig) *rpc.Call {
	return s.Client.Go("Sim.SetLaunchConfig",
		&SetLaunchConfigArgs{
			ControllerToken: s.ControllerToken,
			Config:          lc,
		}, nil, nil)
}

func (s *proxy) TakeOrReturnLaunchControl() *rpc.Call {
	return s.Client.Go("Sim.TakeOrReturnLaunchControl", s.ControllerToken, nil, nil)
}

func (s *proxy) SetGlobalLeaderLine(callsign string, direction *math.CardinalOrdinalDirection) *rpc.Call {
	return s.Client.Go("Sim.SetGlobalLeaderLine", &SetGlobalLeaderLineArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Direction:       direction,
	}, nil, nil)
}

func (s *proxy) SetScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetScratchpad", &SetScratchpadArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *proxy) SetSecondaryScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetSecondaryScratchpad", &SetScratchpadArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *proxy) AutoAssociateFP(callsign string, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.AutoAssociateFP", &InitiateTrackArgs{
		AircraftSpecifier: AircraftSpecifier{
			ControllerToken: s.ControllerToken,
			Callsign:        callsign,
		},
		Plan: fp,
	}, nil, nil)
}

func (s *proxy) CreateUnsupportedTrack(callsign string, ut *UnsupportedTrack) *rpc.Call {
	return s.Client.Go("Sim.CreateUnsupportedTrack", &CreateUnsupportedTrackArgs{
		ControllerToken:  s.ControllerToken,
		Callsign:         callsign,
		UnsupportedTrack: ut,
	}, nil, nil)
}

func (s *proxy) UploadFlightPlan(Type int, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.UploadFlightPlan", &UploadPlanArgs{
		ControllerToken: s.ControllerToken,
		Type:            Type,
		Plan:            fp,
	}, nil, nil)
}

func (s *proxy) InitiateTrack(callsign string, fp *STARSFlightPlan) *rpc.Call {
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

func (s *proxy) IntermTrack(callsign, initial string, fp *STARSFlightPlan) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", IntermTrackArgs{
		Token:    s.ControllerToken,
		Callsign: callsign,
		Initial:  initial,
		fp:       fp,
	}, nil, nil)
}

func (s *proxy) DropTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DropTrack", &DropTrackArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.HandoffTrack", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *proxy) AcceptHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptHandoff", &AcceptHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) CancelHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.CancelHandoff", &CancelHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) GlobalMessage(global GlobalMessage) *rpc.Call {
	return s.Client.Go("Sim.GlobalMessage", &GlobalMessageArgs{
		ControllerToken: s.ControllerToken,
		Message:         global.Message,
		FromController:  global.FromController,
	}, nil, nil)
}

func (s *proxy) ForceQL(callsign, controller string) *rpc.Call {
	return s.Client.Go("Sim.ForceQL", &ForceQLArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *proxy) RedirectHandoff(callsign, controller string) *rpc.Call {
	return s.Client.Go("Sim.RedirectHandoff", &HandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *proxy) AcceptRedirectedHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptRedirectedHandoff", &AcceptHandoffArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) PointOut(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.PointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *proxy) AcknowledgePointOut(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcknowledgePointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) RejectPointOut(callsign string) *rpc.Call {
	return s.Client.Go("Sim.RejectPointOut", &PointOutArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) ToggleSPCOverride(callsign string, spc string) *rpc.Call {
	return s.Client.Go("Sim.ToggleSPCOverride", &ToggleSPCArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		SPC:             spc,
	}, nil, nil)
}

func (s *proxy) ReleaseDeparture(callsign string) *rpc.Call {
	return s.Client.Go("Sim.ReleaseDeparture", &HeldDepartureArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *proxy) SetTemporaryAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetTemporaryAltitude", &AssignAltitudeArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *proxy) DeleteAllAircraft() *rpc.Call {
	return s.Client.Go("Sim.DeleteAllAircraft", &DeleteAircraftArgs{
		ControllerToken: s.ControllerToken,
	}, nil, nil)
}

func (s *proxy) RunAircraftCommands(callsign string, cmds string, result *AircraftCommandsResult) *rpc.Call {
	return s.Client.Go("Sim.RunAircraftCommands", &AircraftCommandsArgs{
		ControllerToken: s.ControllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, result, nil)
}

func (s *proxy) LaunchAircraft(ac av.Aircraft) *rpc.Call {
	return s.Client.Go("Sim.LaunchAircraft", &LaunchAircraftArgs{
		ControllerToken: s.ControllerToken,
		Aircraft:        ac,
	}, nil, nil)
}

func (p *proxy) CreateDeparture(airport, runway, category string, ac *av.Aircraft) *rpc.Call {
	return p.Client.Go("Sim.CreateDeparture", &CreateDepartureArgs{
		ControllerToken: p.ControllerToken,
		Airport:         airport,
		Runway:          runway,
		Category:        category,
	}, ac, nil)
}

func (p *proxy) CreateArrival(group, airport string, ac *av.Aircraft) *rpc.Call {
	return p.Client.Go("Sim.CreateArrival", &CreateArrivalArgs{
		ControllerToken: p.ControllerToken,
		Group:           group,
		Airport:         airport,
	}, ac, nil)
}

func (p *proxy) CreateOverflight(group string, ac *av.Aircraft) *rpc.Call {
	return p.Client.Go("Sim.CreateOverflight", &CreateOverflightArgs{
		ControllerToken: p.ControllerToken,
		Group:           group,
	}, ac, nil)
}

func (p *proxy) CreateRestrictionArea(ra RestrictionArea, idx *int) *rpc.Call {
	return p.Client.Go("Sim.CreateRestrictionArea", &RestrictionAreaArgs{
		ControllerToken: p.ControllerToken,
		RestrictionArea: ra,
	}, idx, nil)
}

func (p *proxy) UpdateRestrictionArea(idx int, ra RestrictionArea) *rpc.Call {
	return p.Client.Go("Sim.UpdateRestrictionArea", &RestrictionAreaArgs{
		ControllerToken: p.ControllerToken,
		Index:           idx,
		RestrictionArea: ra,
	}, nil, nil)
}

func (p *proxy) DeleteRestrictionArea(idx int) *rpc.Call {
	return p.Client.Go("Sim.DeleteRestrictionArea", &RestrictionAreaArgs{
		ControllerToken: p.ControllerToken,
		Index:           idx,
	}, nil, nil)
}

func (p *proxy) GetVideoMapLibrary(filename string, vmf *av.VideoMapLibrary) error {
	// Synchronous call
	return p.Client.Call("Sim.GetVideoMapLibrary", &VideoMapsArgs{
		ControllerToken: p.ControllerToken,
		Filename:        filename,
	}, vmf)
}

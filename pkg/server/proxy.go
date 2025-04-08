// pkg/server/proxy.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type proxy struct {
	ControllerToken string
	Client          *util.RPCClient
}

func (p *proxy) TogglePause() *rpc.Call {
	return p.Client.Go("Sim.TogglePause", p.ControllerToken, nil, nil)
}

func (p *proxy) SignOff(_, _ *struct{}) error {
	if err := p.Client.CallWithTimeout("Sim.SignOff", p.ControllerToken, nil); err != nil {
		return err
	}
	// FIXME: this is handing in zstd code. Why?
	// return p.Client.Close()
	return nil
}

func (p *proxy) ChangeControlPosition(callsign string, keepTracks bool) error {
	return p.Client.CallWithTimeout("Sim.ChangeControlPosition",
		&ChangeControlPositionArgs{
			ControllerToken: p.ControllerToken,
			Callsign:        callsign,
			KeepTracks:      keepTracks,
		}, nil)
}

func (p *proxy) GetSerializeSim() (*sim.Sim, error) {
	var s sim.Sim
	err := p.Client.CallWithTimeout("SimManager.GetSerializeSim", p.ControllerToken, &s)
	return &s, err
}

func (p *proxy) GetWorldUpdate(wu *sim.WorldUpdate) *rpc.Call {
	return p.Client.Go("Sim.GetWorldUpdate", p.ControllerToken, wu, nil)
}

func (p *proxy) SetSimRate(r float32) *rpc.Call {
	return p.Client.Go("Sim.SetSimRate",
		&SetSimRateArgs{
			ControllerToken: p.ControllerToken,
			Rate:            r,
		}, nil, nil)
}

func (p *proxy) SetLaunchConfig(lc sim.LaunchConfig) *rpc.Call {
	return p.Client.Go("Sim.SetLaunchConfig",
		&SetLaunchConfigArgs{
			ControllerToken: p.ControllerToken,
			Config:          lc,
		}, nil, nil)
}

func (p *proxy) TakeOrReturnLaunchControl() *rpc.Call {
	return p.Client.Go("Sim.TakeOrReturnLaunchControl", p.ControllerToken, nil, nil)
}

func (p *proxy) SetGlobalLeaderLine(callsign string, direction *math.CardinalOrdinalDirection) *rpc.Call {
	return p.Client.Go("Sim.SetGlobalLeaderLine", &SetGlobalLeaderLineArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Direction:       direction,
	}, nil, nil)
}

func (p *proxy) CreateFlightPlan(spec av.STARSFlightPlanSpecifier, ty av.STARSFlightPlanType, fpFinal *av.STARSFlightPlan) *rpc.Call {
	return p.Client.Go("Sim.CreateFlightPlan", &CreateFlightPlanArgs{
		ControllerToken:     p.ControllerToken,
		Type:                ty,
		FlightPlanSpecifier: spec,
	}, fpFinal, nil)
}

func (p *proxy) ModifyFlightPlan(callsign string, spec av.STARSFlightPlanSpecifier, fpFinal *av.STARSFlightPlan) *rpc.Call {
	return p.Client.Go("Sim.ModifyFlightPlan", &ModifyFlightPlanArgs{
		ControllerToken:     p.ControllerToken,
		Callsign:            callsign,
		FlightPlanSpecifier: spec,
	}, fpFinal, nil)
}

func (p *proxy) AssociateFlightPlan(callsign string, spec av.STARSFlightPlanSpecifier) *rpc.Call {
	return p.Client.Go("Sim.AssociateFlightPlan", &AssociateFlightPlanArgs{
		AircraftSpecifier: AircraftSpecifier{
			ControllerToken: p.ControllerToken,
			Callsign:        callsign,
		},
		FlightPlanSpecifier: spec,
	}, nil, nil)
}

func (p *proxy) ActivateFlightPlan(trackCallsign, fpACID string, spec *av.STARSFlightPlanSpecifier) *rpc.Call {
	return p.Client.Go("Sim.ActivateFlightPlan", &ActivateFlightPlanArgs{
		ControllerToken:     p.ControllerToken,
		TrackCallsign:       trackCallsign,
		FpACID:              fpACID,
		FlightPlanSpecifier: spec,
	}, nil, nil)
}

func (p *proxy) DeleteFlightPlan(acid string) *rpc.Call {
	return p.Client.Go("Sim.DeleteFlightPlan", &DeleteFlightPlanArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        acid,
	}, nil, nil)
}

func (p *proxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return p.Client.Go("Sim.HandoffTrack", &HandoffArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (p *proxy) AcceptHandoff(callsign string) *rpc.Call {
	return p.Client.Go("Sim.AcceptHandoff", &AcceptHandoffArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) CancelHandoff(callsign string) *rpc.Call {
	return p.Client.Go("Sim.CancelHandoff", &CancelHandoffArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) GlobalMessage(global sim.GlobalMessage) *rpc.Call {
	return p.Client.Go("Sim.GlobalMessage", &GlobalMessageArgs{
		ControllerToken: p.ControllerToken,
		Message:         global.Message,
	}, nil, nil)
}

func (p *proxy) ForceQL(callsign, controller string) *rpc.Call {
	return p.Client.Go("Sim.ForceQL", &ForceQLArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (p *proxy) RedirectHandoff(callsign, controller string) *rpc.Call {
	return p.Client.Go("Sim.RedirectHandoff", &HandoffArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (p *proxy) AcceptRedirectedHandoff(callsign string) *rpc.Call {
	return p.Client.Go("Sim.AcceptRedirectedHandoff", &AcceptHandoffArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) PointOut(callsign string, controller string) *rpc.Call {
	return p.Client.Go("Sim.PointOut", &PointOutArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (p *proxy) AcknowledgePointOut(callsign string) *rpc.Call {
	return p.Client.Go("Sim.AcknowledgePointOut", &PointOutArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) RecallPointOut(callsign string) *rpc.Call {
	return p.Client.Go("Sim.RecallPointOut", &PointOutArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) RejectPointOut(callsign string) *rpc.Call {
	return p.Client.Go("Sim.RejectPointOut", &PointOutArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) ReleaseDeparture(callsign string) *rpc.Call {
	return p.Client.Go("Sim.ReleaseDeparture", &HeldDepartureArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
	}, nil, nil)
}

func (p *proxy) DeleteAllAircraft() *rpc.Call {
	return p.Client.Go("Sim.DeleteAllAircraft", &DeleteAircraftArgs{
		ControllerToken: p.ControllerToken,
	}, nil, nil)
}

func (p *proxy) RunAircraftCommands(callsign string, cmds string, result *AircraftCommandsResult) *rpc.Call {
	return p.Client.Go("Sim.RunAircraftCommands", &AircraftCommandsArgs{
		ControllerToken: p.ControllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, result, nil)
}

func (p *proxy) LaunchAircraft(ac av.Aircraft, departureRunway string) *rpc.Call {
	return p.Client.Go("Sim.LaunchAircraft", &LaunchAircraftArgs{
		ControllerToken: p.ControllerToken,
		Aircraft:        ac,
		DepartureRunway: departureRunway,
	}, nil, nil)
}

func (p *proxy) CreateDeparture(airport, runway, category string, rules av.FlightRules, ac *av.Aircraft) *rpc.Call {
	return p.Client.Go("Sim.CreateDeparture", &CreateDepartureArgs{
		ControllerToken: p.ControllerToken,
		Airport:         airport,
		Runway:          runway,
		Category:        category,
		Rules:           rules,
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

func (p *proxy) CreateRestrictionArea(ra av.RestrictionArea, idx *int) *rpc.Call {
	return p.Client.Go("Sim.CreateRestrictionArea", &RestrictionAreaArgs{
		ControllerToken: p.ControllerToken,
		RestrictionArea: ra,
	}, idx, nil)
}

func (p *proxy) UpdateRestrictionArea(idx int, ra av.RestrictionArea) *rpc.Call {
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

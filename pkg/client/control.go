// pkg/client/control.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
)

func (c *ControlClient) TakeOrReturnLaunchControl(eventStream *sim.EventStream) {
	c.addCall(makeRPCCall(c.client.Go("Sim.TakeOrReturnLaunchControl", c.controllerToken, nil, nil),
		func(err error) {
			if err != nil {
				eventStream.Post(sim.Event{
					Type:        sim.StatusMessageEvent,
					WrittenText: err.Error(),
				})
			}
		}))
}

func (c *ControlClient) LaunchDeparture(ac sim.Aircraft, rwy string) {
	c.addCall(makeRPCCall(c.client.Go("Sim.LaunchAircraft", &server.LaunchAircraftArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        ac,
		DepartureRunway: rwy,
	}, nil, nil), nil))
}

func (c *ControlClient) LaunchArrivalOverflight(ac sim.Aircraft) {
	c.addCall(makeRPCCall(c.client.Go("Sim.LaunchAircraft", &server.LaunchAircraftArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        ac,
		DepartureRunway: "",
	}, nil, nil), nil))
}

func (c *ControlClient) SendGlobalMessage(global sim.GlobalMessage) {
	c.addCall(makeRPCCall(c.client.Go("Sim.GlobalMessage", &server.GlobalMessageArgs{
		ControllerToken: c.controllerToken,
		Message:         global.Message,
	}, nil, nil), nil))
}

func (c *ControlClient) CreateFlightPlan(spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go("Sim.CreateFlightPlan", &server.CreateFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update, callback))
}

func (c *ControlClient) ModifyFlightPlan(acid sim.ACID, spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go("Sim.ModifyFlightPlan", &server.ModifyFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			ACID:                acid,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update, callback))
}

func (c *ControlClient) AssociateFlightPlan(callsign av.ADSBCallsign, spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go("Sim.AssociateFlightPlan", &server.AssociateFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			Callsign:            callsign,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update,
			func(err error) {
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) ActivateFlightPlan(callsign av.ADSBCallsign, fpACID sim.ACID, spec *sim.STARSFlightPlanSpecifier,
	callback func(error)) {
	var update sim.StateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go("Sim.ActivateFlightPlan", &server.ActivateFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			TrackCallsign:       callsign,
			FpACID:              fpACID,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update,
			func(err error) {
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) DeleteFlightPlan(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.DeleteFlightPlan", &server.DeleteFlightPlanArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RepositionTrack(acid sim.ACID, callsign av.ADSBCallsign, p math.Point2LL, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.RepositionTrack", &server.RepositionTrackArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		Callsign:        callsign,
		Position:        p,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) HandoffTrack(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.HandoffTrack", &server.HandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToTCP:           controller,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcceptHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.AcceptHandoff", &server.AcceptHandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update,
		func(err error) {
			if callback != nil {
				callback(err)
			}
		}))
}

func (c *ControlClient) RedirectHandoff(acid sim.ACID, tcp string, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.RedirectHandoff", &server.HandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToTCP:           tcp,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcceptRedirectedHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.AcceptRedirectedHandoff", &server.AcceptHandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update,
		func(err error) {
			if callback != nil {
				callback(err)
			}
		}))
}

func (c *ControlClient) CancelHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.CancelHandoff", &server.CancelHandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) ForceQL(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.ForceQL", &server.ForceQLArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		Controller:      controller,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) PointOut(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.PointOut", &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		Controller:      controller,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcknowledgePointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.AcknowledgePointOut", &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RecallPointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.RecallPointOut", &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RejectPointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.RejectPointOut", &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) ReleaseDeparture(callsign av.ADSBCallsign, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.ReleaseDeparture", &server.HeldDepartureArgs{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) ChangeControlPosition(tcp string, keepTracks bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.client.callWithTimeout("Sim.ChangeControlPosition",
		&server.ChangeControlPositionArgs{
			ControllerToken: c.controllerToken,
			TCP:             tcp,
			KeepTracks:      keepTracks,
		}, nil)
	if err == nil {
		c.State.UserTCP = tcp
	}
	return err
}

func (c *ControlClient) CreateDeparture(airport, runway, category string, rules av.FlightRules, ac *sim.Aircraft,
	callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go("Sim.CreateDeparture", &server.CreateDepartureArgs{
		ControllerToken: c.controllerToken,
		Airport:         airport,
		Runway:          runway,
		Category:        category,
		Rules:           rules,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateArrival(group, airport string, ac *sim.Aircraft, callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go("Sim.CreateArrival", &server.CreateArrivalArgs{
		ControllerToken: c.controllerToken,
		Group:           group,
		Airport:         airport,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateOverflight(group string, ac *sim.Aircraft, callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go("Sim.CreateOverflight", &server.CreateOverflightArgs{
		ControllerToken: c.controllerToken,
		Group:           group,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateRestrictionArea(ra av.RestrictionArea, callback func(int, error)) {
	var result server.CreateRestrictionAreaResultArgs
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.CreateRestrictionArea", &server.RestrictionAreaArgs{
		ControllerToken: c.controllerToken,
		RestrictionArea: ra,
	}, &result, nil), &result.StateUpdate,
		func(err error) {
			if callback != nil {
				if err != nil {
					callback(result.Index, err)
				}
				callback(result.Index, err)
			}
		}))
}

func (c *ControlClient) UpdateRestrictionArea(idx int, ra av.RestrictionArea, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.UpdateRestrictionArea", &server.RestrictionAreaArgs{
		ControllerToken: c.controllerToken,
		Index:           idx,
		RestrictionArea: ra,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) DeleteRestrictionArea(idx int, callback func(error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.DeleteRestrictionArea", &server.RestrictionAreaArgs{
		ControllerToken: c.controllerToken,
		Index:           idx,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) GetVideoMapLibrary(filename string) (*sim.VideoMapLibrary, error) {
	var vmf sim.VideoMapLibrary
	err := c.client.callWithTimeout("Sim.GetVideoMapLibrary", &server.VideoMapsArgs{
		ControllerToken: c.controllerToken,
		Filename:        filename,
	}, &vmf)
	return &vmf, err
}

func (c *ControlClient) GetAircraftDisplayState(callsign av.ADSBCallsign) (sim.AircraftDisplayState, error) {
	var state sim.AircraftDisplayState
	err := c.client.callWithTimeout("Sim.GetAircraftDisplayState", &server.AircraftSpecifier{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
	}, &state)
	return state, err
}

func (c *ControlClient) GetSerializeSim() (*sim.Sim, error) {
	var s sim.Sim
	err := c.client.callWithTimeout("SimManager.GetSerializeSim", c.controllerToken, &s)
	return &s, err
}

func (c *ControlClient) ToggleSimPause() {
	c.addCall(makeRPCCall(c.client.Go("Sim.TogglePause", c.controllerToken, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.Paused = !c.State.Paused
}

func (c *ControlClient) RequestFlightFollowing() {
	c.addCall(makeRPCCall(c.client.Go("Sim.RequestFlightFollowing", c.controllerToken, nil, nil), nil))
}

func (c *ControlClient) FastForward() {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.FastForward", c.controllerToken, &update, nil), &update, nil))
}

func (c *ControlClient) SetSimRate(r float32) {
	c.addCall(makeRPCCall(c.client.Go("Sim.SetSimRate",
		&server.SetSimRateArgs{
			ControllerToken: c.controllerToken,
			Rate:            r,
		}, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.SimRate = r
}

func (c *ControlClient) SetLaunchConfig(lc sim.LaunchConfig) {
	c.addCall(makeRPCCall(c.client.Go("Sim.SetLaunchConfig",
		&server.SetLaunchConfigArgs{
			ControllerToken: c.controllerToken,
			Config:          lc,
		}, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.LaunchConfig = lc
}

func (c *ControlClient) DeleteAllAircraft(callback func(err error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.DeleteAllAircraft", &server.DeleteAircraftArgs{
		ControllerToken: c.controllerToken,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) DeleteAircraft(aircraft []sim.Aircraft, callback func(err error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.DeleteAircraft", &server.DeleteAircraftListArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        aircraft,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) GetQULines(aircraft sim.ACID, callback func(err error)) {
	var update sim.StateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go("Sim.SendCoordinateInfo", &server.QULineArgs{
		ControllerToken: c.controllerToken,
		ACID:           aircraft,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RunAircraftCommands(callsign av.ADSBCallsign, cmds string, handleResult func(message string, remainingInput string)) {

	if c.HaveTTS() && cmds != "P" && cmds != "X" {
		c.mu.Lock()

		if c.awaitReadbackCallsign != "" {
			c.lg.Warnf("Already awaiting readback for %q, just got %q!", c.awaitReadbackCallsign, callsign)
		}
		c.awaitReadbackCallsign = callsign

		c.mu.Unlock()
	}

	var result server.AircraftCommandsResult
	c.addCall(makeRPCCall(c.client.Go("Sim.RunAircraftCommands", &server.AircraftCommandsArgs{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
		Commands:        cmds,
	}, &result, nil),
		func(err error) {
			if result.RemainingInput == cmds {
				c.awaitReadbackCallsign = ""
			}
			handleResult(result.ErrorMessage, result.RemainingInput)
			if err != nil {
				c.lg.Errorf("%s: %v", callsign, err)
			}
		}))
}

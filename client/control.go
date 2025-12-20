// client/control.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

func (c *ControlClient) TakeOrReturnLaunchControl(eventStream *sim.EventStream) {
	c.addCall(makeRPCCall(c.client.Go(server.TakeOrReturnLaunchControlRPC, c.controllerToken, nil, nil),
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
	c.addCall(makeRPCCall(c.client.Go(server.LaunchAircraftRPC, &server.LaunchAircraftArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        ac,
		DepartureRunway: rwy,
	}, nil, nil), nil))
}

func (c *ControlClient) LaunchArrivalOverflight(ac sim.Aircraft) {
	c.addCall(makeRPCCall(c.client.Go(server.LaunchAircraftRPC, &server.LaunchAircraftArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        ac,
		DepartureRunway: "",
	}, nil, nil), nil))
}

func (c *ControlClient) SendGlobalMessage(global sim.GlobalMessage) {
	c.addCall(makeRPCCall(c.client.Go(server.GlobalMessageRPC, &server.GlobalMessageArgs{
		ControllerToken: c.controllerToken,
		Message:         global.Message,
	}, nil, nil), nil))
}

func (c *ControlClient) CreateFlightPlan(spec sim.FlightPlanSpecifier, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go(server.CreateFlightPlanRPC, &server.CreateFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update, callback))
}

func (c *ControlClient) ModifyFlightPlan(acid sim.ACID, spec sim.FlightPlanSpecifier, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go(server.ModifyFlightPlanRPC, &server.ModifyFlightPlanArgs{
			ControllerToken:     c.controllerToken,
			ACID:                acid,
			FlightPlanSpecifier: spec,
		}, &update, nil), &update, callback))
}

func (c *ControlClient) AssociateFlightPlan(callsign av.ADSBCallsign, spec sim.FlightPlanSpecifier, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go(server.AssociateFlightPlanRPC, &server.AssociateFlightPlanArgs{
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

func (c *ControlClient) ActivateFlightPlan(callsign av.ADSBCallsign, fpACID sim.ACID, spec *sim.FlightPlanSpecifier,
	callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(
		makeStateUpdateRPCCall(c.client.Go(server.ActivateFlightPlanRPC, &server.ActivateFlightPlanArgs{
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
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.DeleteFlightPlanRPC, &server.DeleteFlightPlanArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RepositionTrack(acid sim.ACID, callsign av.ADSBCallsign, p math.Point2LL, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.RepositionTrackRPC, &server.RepositionTrackArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		Callsign:        callsign,
		Position:        p,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) HandoffTrack(acid sim.ACID, to sim.ControllerPosition, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.HandoffTrackRPC, &server.HandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToPosition:      to,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcceptHandoff(acid sim.ACID, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.AcceptHandoffRPC, &server.AcceptHandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update,
		func(err error) {
			if callback != nil {
				callback(err)
			}
		}))
}

func (c *ControlClient) RedirectHandoff(acid sim.ACID, to sim.ControllerPosition, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.RedirectHandoffRPC, &server.HandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToPosition:      to,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcceptRedirectedHandoff(acid sim.ACID, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.AcceptRedirectedHandoffRPC, &server.AcceptHandoffArgs{
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
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.CancelHandoffRPC, &server.CancelHandoffArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) ForceQL(acid sim.ACID, to sim.ControllerPosition, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.ForceQLRPC, &server.ForceQLArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToPosition:      to,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) PointOut(acid sim.ACID, to sim.ControllerPosition, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.PointOutRPC, &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
		ToPosition:      to,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) AcknowledgePointOut(acid sim.ACID, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.AcknowledgePointOutRPC, &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RecallPointOut(acid sim.ACID, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.RecallPointOutRPC, &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RejectPointOut(acid sim.ACID, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.RejectPointOutRPC, &server.PointOutArgs{
		ControllerToken: c.controllerToken,
		ACID:            acid,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) ReleaseDeparture(callsign av.ADSBCallsign, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.ReleaseDepartureRPC, &server.HeldDepartureArgs{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) SetWaypointCommands(commands string) {
	c.addCall(makeRPCCall(c.client.Go(server.SetWaypointCommandsRPC, &server.SetWaypointCommandsArgs{
		ControllerToken: c.controllerToken,
		Commands:        commands,
	}, nil, nil), nil))
}

func (c *ControlClient) CreateDeparture(airport, runway, category string, rules av.FlightRules, ac *sim.Aircraft,
	callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go(server.CreateDepartureRPC, &server.CreateDepartureArgs{
		ControllerToken: c.controllerToken,
		Airport:         airport,
		Runway:          runway,
		Category:        category,
		Rules:           rules,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateArrival(group, airport string, ac *sim.Aircraft, callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go(server.CreateArrivalRPC, &server.CreateArrivalArgs{
		ControllerToken: c.controllerToken,
		Group:           group,
		Airport:         airport,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateOverflight(group string, ac *sim.Aircraft, callback func(error)) {
	c.addCall(makeRPCCall(c.client.Go(server.CreateOverflightRPC, &server.CreateOverflightArgs{
		ControllerToken: c.controllerToken,
		Group:           group,
	}, ac, nil), callback))
}

func (c *ControlClient) CreateRestrictionArea(ra av.RestrictionArea, callback func(int, error)) {
	var result server.CreateRestrictionAreaResultArgs
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.CreateRestrictionAreaRPC, &server.RestrictionAreaArgs{
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
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.UpdateRestrictionAreaRPC, &server.RestrictionAreaArgs{
		ControllerToken: c.controllerToken,
		Index:           idx,
		RestrictionArea: ra,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) DeleteRestrictionArea(idx int, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.DeleteRestrictionAreaRPC, &server.RestrictionAreaArgs{
		ControllerToken: c.controllerToken,
		Index:           idx,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) GetVideoMapLibrary(filename string) (*sim.VideoMapLibrary, error) {
	var vmf sim.VideoMapLibrary
	err := c.client.callWithTimeout(server.GetVideoMapLibraryRPC, &server.VideoMapsArgs{
		Filename: filename,
	}, &vmf)
	return &vmf, err
}

func (c *ControlClient) GetAircraftDisplayState(callsign av.ADSBCallsign) (sim.AircraftDisplayState, error) {
	var state sim.AircraftDisplayState
	err := c.client.callWithTimeout(server.GetAircraftDisplayStateRPC, &server.AircraftSpecifier{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
	}, &state)
	return state, err
}

func (c *ControlClient) GetSerializeSim() (*sim.Sim, error) {
	var s sim.Sim
	err := c.client.callWithTimeout(server.GetSerializeSimRPC, c.controllerToken, &s)
	return &s, err
}

func (c *ControlClient) ToggleSimPause() {
	c.addCall(makeRPCCall(c.client.Go(server.TogglePauseRPC, c.controllerToken, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.Paused = !c.State.Paused
}

func (c *ControlClient) RequestFlightFollowing() {
	c.addCall(makeRPCCall(c.client.Go(server.RequestFlightFollowingRPC, c.controllerToken, nil, nil), nil))
}

func (c *ControlClient) TriggerEmergency(emergencyName string) {
	c.addCall(makeRPCCall(c.client.Go(server.TriggerEmergencyRPC, &server.TriggerEmergencyArgs{
		ControllerToken: c.controllerToken,
		EmergencyName:   emergencyName,
	}, nil, nil), nil))
}

func (c *ControlClient) FastForward() {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.FastForwardRPC, c.controllerToken, &update, nil), &update, nil))
}

func (c *ControlClient) SetSimRate(r float32) {
	c.addCall(makeRPCCall(c.client.Go(server.SetSimRateRPC,
		&server.SetSimRateArgs{
			ControllerToken: c.controllerToken,
			Rate:            r,
		}, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.SimRate = r
}

func (c *ControlClient) SetLaunchConfig(lc sim.LaunchConfig) {
	c.addCall(makeRPCCall(c.client.Go(server.SetLaunchConfigRPC,
		&server.SetLaunchConfigArgs{
			ControllerToken: c.controllerToken,
			Config:          lc,
		}, nil, nil), nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.State.LaunchConfig = lc
}

func (c *ControlClient) DeleteAllAircraft(callback func(err error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.DeleteAllAircraftRPC, &server.DeleteAircraftArgs{
		ControllerToken: c.controllerToken,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) DeleteAircraft(aircraft []sim.Aircraft, callback func(err error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.DeleteAircraftRPC, &server.DeleteAircraftListArgs{
		ControllerToken: c.controllerToken,
		Aircraft:        aircraft,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) SendRouteCoordinates(aircraft sim.ACID, callback func(err error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.SendRouteCoordinatesRPC, &server.SendRouteCoordinatesArgs{
		ControllerToken: c.controllerToken,
		ACID:            aircraft,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) FlightPlanDirect(aircraft sim.ACID, fix string, callback func(err error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.FlightPlanDirectRPC, &server.FlightPlanDirectArgs{
		ControllerToken: c.controllerToken,
		ACID:            aircraft,
		Fix:             fix,
	}, &update, nil), &update, callback))
}

func (c *ControlClient) RunAircraftCommands(callsign av.ADSBCallsign, cmds string, multiple, clickedTrack bool, handleResult func(message string, remainingInput string)) {
	if c.HaveTTS() && cmds != "P" && cmds != "X" {
		c.mu.Lock()

		if c.awaitReadbackCallsign != "" {
			c.lg.Warnf("Already awaiting readback for %q, just got %q!", c.awaitReadbackCallsign, callsign)
		}
		c.awaitReadbackCallsign = callsign

		c.mu.Unlock()
	}

	var result server.AircraftCommandsResult
	c.addCall(makeRPCCall(c.client.Go(server.RunAircraftCommandsRPC, &server.AircraftCommandsArgs{
		ControllerToken: c.controllerToken,
		Callsign:        callsign,
		Commands:        cmds,
		Multiple:        multiple,
		ClickedTrack:    clickedTrack,
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

// ConsolidateTCP consolidates the sendingTCP to the receivingTCW's keyboard.
// sim.ConsolidationFull transfers active tracks; sim.ConsolidationBasic only inactive/future flights.
func (c *ControlClient) ConsolidateTCP(receivingTCW sim.TCW, sendingTCP sim.TCP, consType sim.ConsolidationType, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.ConsolidateTCPRPC, &server.ConsolidateTCPArgs{
		ControllerToken: c.controllerToken,
		ReceivingTCW:    receivingTCW,
		SendingTCP:      sendingTCP,
		Type:            consType,
	}, &update, nil), &update, callback))
}

// DeconsolidateTCP returns a secondary TCP to its default keyboard.  If
// tcp is empty, deconsolidates the default TCP for the TCW (i.e., the TCP
// one with the same name) back to the user's TCW.
func (c *ControlClient) DeconsolidateTCP(tcp sim.TCP, callback func(error)) {
	var update server.SimStateUpdate
	c.addCall(makeStateUpdateRPCCall(c.client.Go(server.DeconsolidateTCPRPC, &server.DeconsolidateTCPArgs{
		ControllerToken: c.controllerToken,
		TCP:             tcp,
	}, &update, nil), &update, callback))
}

// pkg/server/client.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"net/rpc"
	"slices"
	"sort"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type ControlClient struct {
	proxy *proxy

	lg *log.Logger

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *PendingCall
	remoteSim         bool

	pendingCalls []*PendingCall

	SessionStats struct {
		Departures    int
		Arrivals      int
		IntraFacility int
		Overflights   int

		SignOnTime time.Time
	}

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	State sim.State
}

func (c *ControlClient) RPCClient() *RPCClient {
	return c.proxy.Client
}

type RPCClient struct {
	*rpc.Client
}

func (c *RPCClient) CallWithTimeout(serviceMethod string, args any, reply any) error {
	pc := &PendingCall{
		Call:      c.Go(serviceMethod, args, reply, nil),
		IssueTime: time.Now(),
	}

	select {
	case <-pc.Call.Done:
		return pc.Call.Error

	case <-time.After(5 * time.Second):
		return ErrRPCTimeout
	}
}

type PendingCall struct {
	Call      *rpc.Call
	IssueTime time.Time
	Callback  func(*sim.EventStream, *sim.State, error)
}

func makeRPCCall(call *rpc.Call, callback func(error)) *PendingCall {
	return &PendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *sim.State, err error) {
			if callback != nil {
				callback(err)
			}
		},
	}
}

func makeStateUpdateRPCCall(call *rpc.Call, update *sim.StateUpdate, callback func(error)) *PendingCall {
	return &PendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *sim.State, err error) {
			if err == nil {
				update.Apply(state, es)
			}
			if callback != nil {
				callback(err)
			}
		},
	}
}

func (p *PendingCall) CheckFinished(es *sim.EventStream, state *sim.State) bool {
	select {
	case c := <-p.Call.Done:
		if p.Callback != nil {
			p.Callback(es, state, c.Error)
		}
		return true

	default:
		return false
	}
}

func NewControlClient(ss sim.State, local bool, controllerToken string, client *RPCClient, lg *log.Logger) *ControlClient {
	cc := &ControlClient{
		State:     ss,
		lg:        lg,
		remoteSim: !local,
		proxy: &proxy{
			ControllerToken: controllerToken,
			Client:          client,
		},
		lastUpdateRequest: time.Now(),
	}
	cc.SessionStats.SignOnTime = ss.SimTime
	return cc
}

func (c *ControlClient) Status() string {
	if c == nil || c.State.SimDescription == "" {
		return "[disconnected]"
	} else {
		stats := c.SessionStats
		deparr := fmt.Sprintf(" [ %d departures %d arrivals %d intrafacility %d overflights ]",
			stats.Departures, stats.Arrivals, stats.IntraFacility, stats.Overflights)
		return c.State.UserTCP + c.State.SimDescription + deparr
	}
}

func (c *ControlClient) TakeOrReturnLaunchControl(eventStream *sim.EventStream) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.TakeOrReturnLaunchControl(),
		func(err error) {
			if err != nil {
				eventStream.Post(sim.Event{
					Type:    sim.StatusMessageEvent,
					Message: err.Error(),
				})
			}
		}))
}

func (c *ControlClient) LaunchDeparture(ac sim.Aircraft, rwy string) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.LaunchAircraft(ac, rwy), nil))
}

func (c *ControlClient) LaunchArrivalOverflight(ac sim.Aircraft) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.LaunchAircraft(ac, ""), nil))
}

func (c *ControlClient) SendGlobalMessage(global sim.GlobalMessage) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.GlobalMessage(global), nil))
}

func (c *ControlClient) CreateFlightPlan(spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.CreateFlightPlan(spec, &update), &update, callback))
}

func (c *ControlClient) ModifyFlightPlan(acid sim.ACID, spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.ModifyFlightPlan(acid, spec, &update), &update, callback))
}

// Utility function that we shim around the user-supplied "success"
// callback for control operations where we increment the controller's "#
// airplanes worked" stats.
func (c *ControlClient) updateControllerStats(callsign av.ADSBCallsign) func(any) {
	return func(result any) {
		if trk, ok := c.State.GetTrackByCallsign(callsign); ok {
			if trk.IsDeparture() {
				c.SessionStats.Departures++
			} else if trk.IsArrival() {
				c.SessionStats.Arrivals++
			} else if trk.IsOverflight() {
				c.SessionStats.Overflights++
			}
		}
	}
}

func (c *ControlClient) AssociateFlightPlan(callsign av.ADSBCallsign, spec sim.STARSFlightPlanSpecifier, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.AssociateFlightPlan(callsign, spec, &update), &update,
			func(err error) {
				if err == nil {
					c.updateControllerStats(callsign)
				}
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) ActivateFlightPlan(callsign av.ADSBCallsign, fpACID sim.ACID, spec *sim.STARSFlightPlanSpecifier,
	callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.ActivateFlightPlan(callsign, fpACID, spec, &update), &update,
			func(err error) {
				if err == nil {
					c.updateControllerStats(callsign)
				}
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) DeleteFlightPlan(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.DeleteFlightPlan(acid, &update), &update, callback))
}

func (c *ControlClient) RepositionTrack(acid sim.ACID, callsign av.ADSBCallsign, p math.Point2LL, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.RepositionTrack(acid, callsign, p, &update), &update, callback))
}

func (c *ControlClient) HandoffTrack(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.HandoffTrack(acid, controller, &update), &update, callback))
}

func (c *ControlClient) AcceptHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.AcceptHandoff(acid, &update), &update,
			func(err error) {
				if err == nil {
					c.updateControllerStats(av.ADSBCallsign(acid))
				}
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) RedirectHandoff(acid sim.ACID, tcp string, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.RedirectHandoff(acid, tcp, &update), &update, callback))
}

func (c *ControlClient) AcceptRedirectedHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.AcceptRedirectedHandoff(acid, &update), &update,
			func(err error) {
				if err == nil {
					c.updateControllerStats(av.ADSBCallsign(acid))
				}
				if callback != nil {
					callback(err)
				}
			}))
}

func (c *ControlClient) CancelHandoff(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.CancelHandoff(acid, &update), &update, callback))
}

func (c *ControlClient) ForceQL(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.ForceQL(acid, controller, &update), &update, callback))
}

func (c *ControlClient) PointOut(acid sim.ACID, controller string, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.PointOut(acid, controller, &update), &update, callback))
}

func (c *ControlClient) AcknowledgePointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.AcknowledgePointOut(acid, &update), &update, callback))
}

func (c *ControlClient) RecallPointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.RecallPointOut(acid, &update), &update, callback))
}

func (c *ControlClient) RejectPointOut(acid sim.ACID, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.RejectPointOut(acid, &update), &update, callback))
}

func (c *ControlClient) ReleaseDeparture(callsign av.ADSBCallsign, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.ReleaseDeparture(callsign, &update), &update, callback))
}

func (c *ControlClient) ChangeControlPosition(tcp string, keepTracks bool) error {
	err := c.proxy.ChangeControlPosition(tcp, keepTracks)
	if err == nil {
		c.State.UserTCP = tcp
	}
	return err
}

func (c *ControlClient) CreateDeparture(airport, runway, category string, rules av.FlightRules, ac *sim.Aircraft,
	callback func(error)) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.CreateDeparture(airport, runway, category, rules, ac),
		callback))
}

func (c *ControlClient) CreateArrival(group, airport string, ac *sim.Aircraft, callback func(error)) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.CreateArrival(group, airport, ac), callback))
}

func (c *ControlClient) CreateOverflight(group string, ac *sim.Aircraft, callback func(error)) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.CreateOverflight(group, ac), callback))
}

func (c *ControlClient) Disconnect() {
	if err := c.proxy.SignOff(nil, nil); err != nil {
		c.lg.Errorf("Error signing off from sim: %v", err)
	}
	c.State.Tracks = nil
	c.State.UnassociatedFlightPlans = nil
	c.State.Controllers = nil
}

// Note that the success callback is passed an integer, giving the index of
// the newly-created restriction area.
func (c *ControlClient) CreateRestrictionArea(ra av.RestrictionArea, callback func(int, error)) {
	var result CreateRestrictionAreaResultArgs
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.CreateRestrictionArea(ra, &result), &result.StateUpdate,
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
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.UpdateRestrictionArea(idx, ra, &update), &update, callback))
}

func (c *ControlClient) DeleteRestrictionArea(idx int, callback func(error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.DeleteRestrictionArea(idx, &update), &update, callback))
}

func (c *ControlClient) GetVideoMapLibrary(filename string) (*sim.VideoMapLibrary, error) {
	var vmf sim.VideoMapLibrary
	err := c.proxy.GetVideoMapLibrary(filename, &vmf)
	return &vmf, err
}

func (c *ControlClient) ControllerAirspace(id string) []av.ControllerAirspaceVolume {
	var vols []av.ControllerAirspaceVolume
	for _, pos := range c.State.GetConsolidatedPositions(id) {
		for _, sub := range util.SortedMapKeys(c.State.Airspace[pos]) {
			vols = append(vols, c.State.Airspace[pos][sub]...)
		}
	}
	return vols
}

func (c *ControlClient) GetAircraftDisplayState(callsign av.ADSBCallsign) (sim.AircraftDisplayState, error) { // synchronous
	return c.proxy.GetAircraftDisplayState(callsign)
}

func (c *ControlClient) GetUpdates(eventStream *sim.EventStream, onErr func(error)) {
	if c.proxy == nil {
		return
	}

	if c.updateCall != nil {
		if c.updateCall.CheckFinished(eventStream, &c.State) {
			c.updateCall = nil
			return
		}
		checkTimeout(c.updateCall, eventStream, onErr)
	}

	c.checkPendingRPCs(eventStream, onErr)

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/c.State.SimRate, 0.05, 1)
	if d := time.Since(c.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if c.updateCall != nil {
			c.lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			return
		}
		c.lastUpdateRequest = time.Now()

		var update sim.StateUpdate
		c.updateCall = makeStateUpdateRPCCall(c.proxy.GetStateUpdate(&update), &update,
			func(err error) {
				d := time.Since(c.updateCall.IssueTime)
				if d > 250*time.Millisecond {
					c.lg.Warnf("Slow world update response %s", d)
				} else {
					c.lg.Debugf("World update response time %s", d)
				}
			})
	}
}

func (c *ControlClient) checkPendingRPCs(eventStream *sim.EventStream, onErr func(error)) {
	c.pendingCalls = slices.DeleteFunc(c.pendingCalls,
		func(call *PendingCall) bool { return call.CheckFinished(eventStream, &c.State) })

	for _, call := range c.pendingCalls {
		if checkTimeout(call, eventStream, onErr) {
			break
		}
	}
}

func checkTimeout(call *PendingCall, eventStream *sim.EventStream, onErr func(error)) bool {
	if time.Since(call.IssueTime) > 5*time.Second {
		eventStream.Post(sim.Event{
			Type:    sim.StatusMessageEvent,
			Message: "No response from server for over 5 seconds. Network connection may be lost.",
		})
		if onErr != nil {
			onErr(ErrRPCTimeout)
		}
		return true
	}
	return false
}

func (c *ControlClient) Connected() bool {
	return c.proxy != nil
}

func (c *ControlClient) GetSerializeSim() (*sim.Sim, error) {
	return c.proxy.GetSerializeSim()
}

func (c *ControlClient) ToggleSimPause() {
	c.State.Paused = !c.State.Paused // improve local UI responsiveness

	c.pendingCalls = append(c.pendingCalls, &PendingCall{
		Call:      c.proxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (c *ControlClient) FastForward() {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.FastForward(&update), &update, nil))
}

func (c *ControlClient) GetSimRate() float32 {
	if c.State.SimRate == 0 {
		return 1
	}
	return c.State.SimRate
}

func (c *ControlClient) SetSimRate(r float32) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.SetSimRate(r), nil))
	c.State.SimRate = r // so the UI is well-behaved...
}

func (c *ControlClient) SetLaunchConfig(lc sim.LaunchConfig) {
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.SetLaunchConfig(lc), nil))
	c.State.LaunchConfig = lc // for the UI's benefit...
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (c *ControlClient) CurrentTime() time.Time {
	t := c.State.SimTime

	if !c.State.Paused && !c.lastUpdateRequest.IsZero() {
		d := time.Since(c.lastUpdateRequest)

		// Roughly account for RPC overhead; more for a remote server (where
		// SimName will be set.)
		if !c.remoteSim {
			d -= 10 * time.Millisecond
		} else {
			d -= 50 * time.Millisecond
		}
		d = math.Max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(c.State.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(c.lastReturnedTime) {
		c.lastReturnedTime = t
	}
	return c.lastReturnedTime
}

func (c *ControlClient) DeleteAllAircraft(callback func(err error)) {
	var update sim.StateUpdate
	c.pendingCalls = append(c.pendingCalls,
		makeStateUpdateRPCCall(c.proxy.DeleteAllAircraft(&update), &update, callback))
}

func (c *ControlClient) RunAircraftCommands(callsign av.ADSBCallsign, cmds string, handleResult func(message string, remainingInput string)) {
	var result AircraftCommandsResult
	c.pendingCalls = append(c.pendingCalls, makeRPCCall(c.proxy.RunAircraftCommands(callsign, cmds, &result),
		func(err error) {
			handleResult(result.ErrorMessage, result.RemainingInput)
			if err != nil {
				c.lg.Errorf("%s: %v", callsign, err)
			}
		}))
}

func (c *ControlClient) TowerListAirports() []string {
	// Figure out airport<-->tower list assignments. Sort the airports
	// according to their TowerListIndex, putting zero (i.e., unassigned)
	// indices at the end. Break ties alphabetically by airport name. The
	// first three then are assigned to the corresponding tower list.
	ap := util.SortedMapKeys(c.State.ArrivalAirports)
	sort.Slice(ap, func(a, b int) bool {
		ai := c.State.ArrivalAirports[ap[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := c.State.ArrivalAirports[ap[b]].TowerListIndex
		if bi == 0 {
			bi = 1000
		}
		if ai == bi {
			return a < b
		}
		return ai < bi
	})
	if len(ap) > 3 {
		ap = ap[:3]
	}
	return ap
}

func (c *ControlClient) StringIsSPC(s string) bool {
	return av.StringIsSPC(s) || slices.Contains(c.State.STARSFacilityAdaptation.CustomSPCs, s)
}

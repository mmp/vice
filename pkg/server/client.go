// pkg/server/client.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
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
	updateCall        *util.PendingCall
	remoteSim         bool

	pendingCalls []*util.PendingCall

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

func (c *ControlClient) RPCClient() *util.RPCClient {
	return c.proxy.Client
}

func NewControlClient(ss sim.State, local bool, controllerToken string, client *util.RPCClient, lg *log.Logger) *ControlClient {
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
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.TakeOrReturnLaunchControl(),
			IssueTime: time.Now(),
			OnErr: func(e error) {
				eventStream.Post(sim.Event{
					Type:    sim.StatusMessageEvent,
					Message: e.Error(),
				})
			},
		})
}

func (c *ControlClient) LaunchDeparture(ac sim.Aircraft, rwy string) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.LaunchAircraft(ac, rwy),
			IssueTime: time.Now(),
		})
}

func (c *ControlClient) LaunchArrivalOverflight(ac sim.Aircraft) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.LaunchAircraft(ac, ""),
			IssueTime: time.Now(),
		})
}

func (c *ControlClient) SendGlobalMessage(global sim.GlobalMessage) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.GlobalMessage(global),
			IssueTime: time.Now(),
		})
}

func (c *ControlClient) SetGlobalLeaderLine(callsign av.ADSBCallsign, dir *math.CardinalOrdinalDirection, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.SetGlobalLeaderLine(callsign, dir),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateFlightPlan(spec av.STARSFlightPlanSpecifier, ty av.STARSFlightPlanType,
	success func(av.STARSFlightPlan), err func(error)) {
	var fpFinal av.STARSFlightPlan
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateFlightPlan(spec, ty, &fpFinal),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				if success != nil {
					success(fpFinal)
				}
			},
			OnErr: err,
		})
}

func (c *ControlClient) ModifyFlightPlan(callsign av.ADSBCallsign, spec av.STARSFlightPlanSpecifier, success func(av.STARSFlightPlan), err func(error)) {
	// Instant update locally hack
	if trk, ok := c.State.GetOurTrackByCallsign(callsign); ok {
		trk.FlightPlan.Update(spec)
	}

	var fpFinal av.STARSFlightPlan
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ModifyFlightPlan(callsign, spec, &fpFinal),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				if success != nil {
					success(fpFinal)
				}
			},
			OnErr: err,
		})
}

// Utility function that we shim around the user-supplied "success"
// callback for control operations where we increment the controller's "#
// airplanes worked" stats.
func (c *ControlClient) updateControllerStats(callsign av.ADSBCallsign, next func(any)) func(any) {
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
		if next != nil {
			next(result)
		}
	}
}

func (c *ControlClient) AssociateFlightPlan(callsign av.ADSBCallsign, spec av.STARSFlightPlanSpecifier, success func(any), err func(error)) {
	// Modifying locally is not canonical but improves perceived latency in
	// the common case; the RPC may fail, though that's fine; the next
	// world update will roll back these changes anyway.
	//
	// As in sim.go, only check for an unset TrackingController; we may already
	// have ControllingController due to a pilot checkin on a departure.
	if trk, ok := c.State.GetTrackByCallsign(callsign); ok && trk.IsUnassociated() {
		fp := spec.GetFlightPlan()
		fp.TrackingController = spec.InitialController.GetOr(c.State.UserTCP)
		if spec.CreateQuick {
			fp.ACID = av.ACID(c.State.STARSFacilityAdaptation.FlightPlan.QuickACID +
				fmt.Sprintf("%02d", c.State.QuickFlightPlanIndex%100))
		}
		trk.FlightPlan = &fp
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AssociateFlightPlan(callsign, spec),
			IssueTime: time.Now(),
			OnSuccess: c.updateControllerStats(callsign, success),
			OnErr:     err,
		})
}

func (c *ControlClient) ActivateFlightPlan(callsign av.ADSBCallsign, fpACID av.ACID, spec *av.STARSFlightPlanSpecifier,
	success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ActivateFlightPlan(callsign, fpACID, spec),
			IssueTime: time.Now(),
			OnSuccess: c.updateControllerStats(callsign, success),
			OnErr:     err,
		})
}

func (c *ControlClient) DeleteFlightPlan(acid av.ACID, success func(any), err func(error)) {
	// Local update for low latency
	if trk, ok := c.State.GetOurTrackByACID(acid); ok {
		trk.FlightPlan = nil
	}
	c.State.UnassociatedFlightPlans = slices.DeleteFunc(c.State.UnassociatedFlightPlans,
		func(fp av.STARSFlightPlan) bool { return fp.ACID == acid })

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.DeleteFlightPlan(acid),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) HandoffTrack(callsign av.ADSBCallsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcceptHandoff(callsign av.ADSBCallsign, success func(any), err func(error)) {
	trk, ok := c.State.GetTrackByCallsign(callsign)
	if ok && trk.IsAssociated() && trk.FlightPlan.HandoffTrackController == c.State.UserTCP {
		trk.FlightPlan.HandoffTrackController = ""
		trk.FlightPlan.TrackingController = c.State.UserTCP
		trk.FlightPlan.ControllingController = c.State.UserTCP
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: c.updateControllerStats(callsign, success),
			OnErr:     err,
		})
}

func (c *ControlClient) RedirectHandoff(callsign av.ADSBCallsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RedirectHandoff(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcceptRedirectedHandoff(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcceptRedirectedHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: c.updateControllerStats(callsign, success),
			OnErr:     err,
		})
}

func (c *ControlClient) CancelHandoff(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ForceQL(callsign av.ADSBCallsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) PointOut(callsign av.ADSBCallsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.PointOut(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcknowledgePointOut(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcknowledgePointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) RecallPointOut(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RecallPointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) RejectPointOut(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RejectPointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ReleaseDeparture(callsign av.ADSBCallsign, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ReleaseDeparture(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ChangeControlPosition(tcp string, keepTracks bool) error {
	err := c.proxy.ChangeControlPosition(tcp, keepTracks)
	if err == nil {
		c.State.UserTCP = tcp
	}
	return err
}

func (c *ControlClient) CreateDeparture(airport, runway, category string, rules av.FlightRules, ac *sim.Aircraft,
	success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateDeparture(airport, runway, category, rules, ac),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateArrival(group, airport string, ac *sim.Aircraft, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateArrival(group, airport, ac),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateOverflight(group string, ac *sim.Aircraft, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateOverflight(group, ac),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) Disconnect() {
	if err := c.proxy.SignOff(nil, nil); err != nil {
		c.lg.Errorf("Error signing off from sim: %v", err)
	}
	c.State.RadarTracks = nil
	c.State.Controllers = nil
}

// Note that the success callback is passed an integer, giving the index of
// the newly-created restriction area.
func (c *ControlClient) CreateRestrictionArea(ra av.RestrictionArea, success func(int), err func(error)) {
	// Speculatively make the change locally immediately to reduce perceived latency.
	if len(c.State.UserRestrictionAreas) < 100 {
		c.State.UserRestrictionAreas = append(c.State.UserRestrictionAreas, ra)
	}

	var idx int
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateRestrictionArea(ra, &idx),
			IssueTime: time.Now(),
			OnSuccess: func(any) { success(idx) },
			OnErr:     err,
		})
}

func (c *ControlClient) UpdateRestrictionArea(idx int, ra av.RestrictionArea, success func(any), err func(error)) {
	// Speculatively make the change locally immediately to reduce perceived latency.
	if idx <= 100 && idx-1 < len(c.State.UserRestrictionAreas) {
		c.State.UserRestrictionAreas[idx-1] = ra
	} else if idx >= 101 && idx-101 < len(c.State.STARSFacilityAdaptation.RestrictionAreas) {
		// Trust the caller to not try to update things they're not supposed to.
		c.State.STARSFacilityAdaptation.RestrictionAreas[idx-101] = ra
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.UpdateRestrictionArea(idx, ra),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) DeleteRestrictionArea(idx int, success func(any), err func(error)) {
	// Delete locally to reduce latency; note that only user restriction
	// areas can be deleted, not system ones from the scenario file.
	if idx-1 < len(c.State.UserRestrictionAreas) {
		c.State.UserRestrictionAreas[idx-1] = av.RestrictionArea{Deleted: true}
	}
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.DeleteRestrictionArea(idx),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) GetVideoMapLibrary(filename string) (*av.VideoMapLibrary, error) {
	var vmf av.VideoMapLibrary
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

func (c *ControlClient) GetUpdates(eventStream *sim.EventStream, onErr func(error)) {
	if c.proxy == nil {
		return
	}

	if c.updateCall != nil {
		if c.updateCall.CheckFinished() {
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

		wu := &sim.WorldUpdate{}
		c.updateCall = &util.PendingCall{
			Call:      c.proxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				d := time.Since(c.updateCall.IssueTime)
				if d > 250*time.Millisecond {
					c.lg.Warnf("Slow world update response %s", d)
				} else {
					c.lg.Debugf("World update response time %s", d)
				}
				wu.UpdateState(&c.State, eventStream)
			},
			OnErr: onErr,
		}
	}
}

func (c *ControlClient) checkPendingRPCs(eventStream *sim.EventStream, onErr func(error)) {
	c.pendingCalls = slices.DeleteFunc(c.pendingCalls,
		func(call *util.PendingCall) bool { return call.CheckFinished() })

	for _, call := range c.pendingCalls {
		if checkTimeout(call, eventStream, onErr) {
			break
		}
	}
}

func checkTimeout(call *util.PendingCall, eventStream *sim.EventStream, onErr func(error)) bool {
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
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (c *ControlClient) GetSimRate() float32 {
	if c.State.SimRate == 0 {
		return 1
	}
	return c.State.SimRate
}

func (c *ControlClient) SetSimRate(r float32) {
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	c.State.SimRate = r // so the UI is well-behaved...
}

func (c *ControlClient) SetLaunchConfig(lc sim.LaunchConfig) {
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.SetLaunchConfig(lc),
		IssueTime: time.Now(),
	})
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

func (c *ControlClient) DeleteAllAircraft(onErr func(err error)) {
	if lctrl := c.State.LaunchConfig.Controller; lctrl == "" || lctrl == c.State.UserTCP {
		c.State.RadarTracks = nil
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.DeleteAllAircraft(),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

func (c *ControlClient) RunAircraftCommands(callsign av.ADSBCallsign, cmds string, handleResult func(message string, remainingInput string)) {
	var result AircraftCommandsResult
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RunAircraftCommands(callsign, cmds, &result),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				handleResult(result.ErrorMessage, result.RemainingInput)
			},
			OnErr: func(err error) {
				c.lg.Errorf("%s: %v", callsign, err)
			},
		})
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

// pkg/sim/client.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

type ControlClient struct {
	proxy *proxy

	lg *log.Logger

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *util.PendingCall

	pendingCalls []*util.PendingCall

	scopeDraw struct {
		arrivals    map[string]map[int]bool               // group->index
		approaches  map[string]map[string]bool            // airport->approach
		departures  map[string]map[string]map[string]bool // airport->runway->exit
		overflights map[string]map[int]bool               // group->index
	}

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	State
}

func (c *ControlClient) RPCClient() *util.RPCClient {
	return c.proxy.Client
}

func NewControlClient(ss State, controllerToken string, client *util.RPCClient, lg *log.Logger) *ControlClient {
	return &ControlClient{
		State: ss,
		lg:    lg,
		proxy: &proxy{
			ControllerToken: controllerToken,
			Client:          client,
		},
		lastUpdateRequest: time.Now(),
	}
}

func (c *ControlClient) Status() string {
	if c == nil || c.SimDescription == "" {
		return "[disconnected]"
	} else {
		deparr := fmt.Sprintf(" [ %d departures %d arrivals %d overflights ]",
			c.TotalDepartures, c.TotalArrivals, c.TotalOverflights)
		if c.SimName == "" {
			return c.State.Callsign + ": " + c.SimDescription + deparr
		} else {
			return c.State.Callsign + "@" + c.SimName + ": " + c.SimDescription + deparr
		}
	}
}

func (c *ControlClient) SetSquawk(callsign string, squawk av.Squawk) error {
	return nil // UNIMPLEMENTED
}

func (c *ControlClient) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (c *ControlClient) TakeOrReturnLaunchControl(eventStream *EventStream) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.TakeOrReturnLaunchControl(),
			IssueTime: time.Now(),
			OnErr: func(e error) {
				eventStream.Post(Event{
					Type:    StatusMessageEvent,
					Message: e.Error(),
				})
			},
		})
}

func (c *ControlClient) LaunchAircraft(ac av.Aircraft) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.LaunchAircraft(ac),
			IssueTime: time.Now(),
		})
}

func (c *ControlClient) SendGlobalMessage(global GlobalMessage) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.GlobalMessage(global),
			IssueTime: time.Now(),
		})
}

func (c *ControlClient) SetScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == c.State.Callsign {
		ac.Scratchpad = scratchpad
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.SetScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) SetSecondaryScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == c.State.Callsign {
		ac.SecondaryScratchpad = scratchpad
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.SetSecondaryScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) SetTemporaryAltitude(callsign string, alt int, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == c.State.Callsign {
		ac.TempAltitude = alt
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.SetTemporaryAltitude(callsign, alt),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AmendFlightPlan(callsign string, fp av.FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (c *ControlClient) SetGlobalLeaderLine(callsign string, dir *math.CardinalOrdinalDirection, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.SetGlobalLeaderLine(callsign, dir),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateUnsupportedTrack(callsign string, ut *UnsupportedTrack,
	success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateUnsupportedTrack(callsign, ut),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AutoAssociateFP(callsign string, fp *STARSFlightPlan, success func(any),
	err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AutoAssociateFP(callsign, fp),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) UploadFlightPlan(fp *STARSFlightPlan, typ int, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.UploadFlightPlan(typ, fp),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) InitiateTrack(callsign string, fp *STARSFlightPlan, success func(any),
	err func(error)) {
	// Modifying locally is not canonical but improves perceived latency in
	// the common case; the RPC may fail, though that's fine; the next
	// world update will roll back these changes anyway.
	//
	// As in sim.go, only check for an unset TrackingController; we may already
	// have ControllingController due to a pilot checkin on a departure.
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == "" {
		ac.TrackingController = c.State.Callsign
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.InitiateTrack(callsign, fp),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) DropTrack(callsign string, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == c.State.Callsign {
		ac.TrackingController = ""
		ac.ControllingController = ""
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.DropTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) HandoffTrack(callsign string, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcceptHandoff(callsign string, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.HandoffTrackController == c.State.Callsign {
		ac.HandoffTrackController = ""
		ac.TrackingController = c.State.Callsign
		ac.ControllingController = c.State.Callsign
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) RedirectHandoff(callsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RedirectHandoff(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcceptRedirectedHandoff(callsign string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcceptRedirectedHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CancelHandoff(callsign string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ForceQL(callsign, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) PointOut(callsign string, controller string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.PointOut(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) AcknowledgePointOut(callsign string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.AcknowledgePointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) RejectPointOut(callsign string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.RejectPointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ToggleSPCOverride(callsign string, spc string, success func(any), err func(error)) {
	if ac := c.State.Aircraft[callsign]; ac != nil && ac.TrackingController == c.State.Callsign {
		ac.ToggleSPCOverride(spc)
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ToggleSPCOverride(callsign, spc),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ReleaseDeparture(callsign string, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.ReleaseDeparture(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) ChangeControlPosition(callsign string, keepTracks bool) error {
	err := c.proxy.ChangeControlPosition(callsign, keepTracks)
	if err == nil {
		c.State.Callsign = callsign
	}
	return err
}

func (c *ControlClient) CreateDeparture(airport, runway, category string, ac *av.Aircraft, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateDeparture(airport, runway, category, ac),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateArrival(group, airport string, ac *av.Aircraft, success func(any), err func(error)) {
	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.CreateArrival(group, airport, ac),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (c *ControlClient) CreateOverflight(group string, ac *av.Aircraft, success func(any), err func(error)) {
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
	c.State.Aircraft = nil
	c.State.Controllers = nil
}

// Note that the success callback is passed an integer, giving the index of
// the newly-created restriction area.
func (c *ControlClient) CreateRestrictionArea(ra RestrictionArea, success func(int), err func(error)) {
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

func (c *ControlClient) UpdateRestrictionArea(idx int, ra RestrictionArea, success func(any), err func(error)) {
	// Speculatively make the change locally immediately to reduce perceived latency.
	if idx <= 100 && idx-1 < len(c.State.UserRestrictionAreas) {
		c.State.UserRestrictionAreas[idx-1] = ra
	} else if idx >= 101 && idx-101 < len(c.STARSFacilityAdaptation.RestrictionAreas) {
		// Trust the caller to not try to update things they're not supposed to.
		c.STARSFacilityAdaptation.RestrictionAreas[idx-101] = ra
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
		c.State.UserRestrictionAreas[idx-1] = RestrictionArea{Deleted: true}
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

func (c *ControlClient) GetUpdates(eventStream *EventStream, onErr func(error)) {
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

		wu := &WorldUpdate{}
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
				c.UpdateWorld(wu, eventStream)
			},
			OnErr: onErr,
		}
	}
}

func (c *ControlClient) UpdateWorld(wu *WorldUpdate, eventStream *EventStream) {
	c.State.Aircraft = wu.Aircraft
	if wu.Controllers != nil {
		c.State.Controllers = wu.Controllers
	}
	c.State.ERAMComputers = wu.ERAMComputers

	c.State.LaunchConfig = wu.LaunchConfig

	c.State.UserRestrictionAreas = wu.UserRestrictionAreas

	c.State.SimTime = wu.Time
	c.State.SimIsPaused = wu.SimIsPaused
	c.State.SimRate = wu.SimRate
	c.State.TotalDepartures = wu.TotalDepartures
	c.State.TotalArrivals = wu.TotalArrivals
	c.State.TotalOverflights = wu.TotalOverflights

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range wu.Events {
		eventStream.Post(e)
	}
}

func (c *ControlClient) checkPendingRPCs(eventStream *EventStream, onErr func(error)) {
	c.pendingCalls = util.FilterSlice(c.pendingCalls,
		func(call *util.PendingCall) bool { return !call.CheckFinished() })

	for _, call := range c.pendingCalls {
		if checkTimeout(call, eventStream, onErr) {
			break
		}
	}
}

func checkTimeout(call *util.PendingCall, eventStream *EventStream, onErr func(error)) bool {
	if time.Since(call.IssueTime) > 5*time.Second {
		eventStream.Post(Event{
			Type:    StatusMessageEvent,
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

func (c *ControlClient) GetSerializeSim() (*Sim, error) {
	return c.proxy.GetSerializeSim()
}

func (c *ControlClient) ToggleSimPause() {
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (c *ControlClient) GetSimRate() float32 {
	if c.SimRate == 0 {
		return 1
	}
	return c.SimRate
}

func (c *ControlClient) SetSimRate(r float32) {
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	c.SimRate = r // so the UI is well-behaved...
}

func (c *ControlClient) SetLaunchConfig(lc LaunchConfig) {
	c.pendingCalls = append(c.pendingCalls, &util.PendingCall{
		Call:      c.proxy.SetLaunchConfig(lc),
		IssueTime: time.Now(),
	})
	c.LaunchConfig = lc // for the UI's benefit...
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (c *ControlClient) CurrentTime() time.Time {
	t := c.SimTime

	if !c.SimIsPaused && !c.lastUpdateRequest.IsZero() {
		d := time.Since(c.lastUpdateRequest)

		// Roughly account for RPC overhead; more for a remote server (where
		// SimName will be set.)
		if c.SimName == "" {
			d -= 10 * time.Millisecond
		} else {
			d -= 50 * time.Millisecond
		}
		d = math.Max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(c.SimRate))

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

func (c *ControlClient) ScopeDrawArrivals() map[string]map[int]bool {
	return c.scopeDraw.arrivals
}

func (c *ControlClient) ScopeDrawApproaches() map[string]map[string]bool {
	return c.scopeDraw.approaches
}

func (c *ControlClient) ScopeDrawDepartures() map[string]map[string]map[string]bool {
	return c.scopeDraw.departures
}

func (c *ControlClient) ScopeDrawOverflights() map[string]map[int]bool {
	return c.scopeDraw.overflights
}

func (c *ControlClient) DeleteAllAircraft(onErr func(err error)) {
	if lctrl := c.LaunchConfig.Controller; lctrl == "" || lctrl == c.State.Callsign {
		c.State.Aircraft = nil
	}

	c.pendingCalls = append(c.pendingCalls,
		&util.PendingCall{
			Call:      c.proxy.DeleteAllAircraft(),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

func (c *ControlClient) RunAircraftCommands(callsign string, cmds string, handleResult func(message string, remainingInput string)) {
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
	ap := util.SortedMapKeys(c.ArrivalAirports)
	sort.Slice(ap, func(a, b int) bool {
		ai := c.ArrivalAirports[ap[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := c.ArrivalAirports[ap[b]].TowerListIndex
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

func (c *ControlClient) DrawScenarioInfoWindow(lg *log.Logger) (show bool) {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription, false, 0)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show = true
	imgui.BeginV(c.State.SimDescription, &show, imgui.WindowFlagsAlwaysAutoResize)

	// Make big(ish) tables somewhat more legible
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if imgui.CollapsingHeader("Arrivals") {
		if imgui.BeginTableV("arr", 4, tableFlags, imgui.Vec2{}, 0) {
			if c.scopeDraw.arrivals == nil {
				c.scopeDraw.arrivals = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Airport(s)")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, name := range util.SortedMapKeys(c.State.InboundFlows) {
				arrivals := c.State.InboundFlows[name].Arrivals
				if len(arrivals) == 0 {
					continue
				}
				if c.scopeDraw.arrivals[name] == nil {
					c.scopeDraw.arrivals[name] = make(map[int]bool)
				}

				for i, arr := range arrivals {
					if len(c.State.LaunchConfig.InboundFlowRates[name]) == 0 {
						// Not used in the current scenario.
						continue
					}

					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := c.scopeDraw.arrivals[name][i]
					imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", name, i), &enabled)
					c.scopeDraw.arrivals[name][i] = enabled

					imgui.TableNextColumn()
					imgui.Text(name)

					imgui.TableNextColumn()
					airports := util.SortedMapKeys(arr.Airlines)
					imgui.Text(strings.Join(airports, ", "))

					imgui.TableNextColumn()
					if arr.Description != "" {
						imgui.Text(arr.Description)
					} else {
						imgui.Text("--")
					}
				}
			}

			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Approaches") {
		if imgui.BeginTableV("appr", 6, tableFlags, imgui.Vec2{}, 0) {
			if c.scopeDraw.approaches == nil {
				c.scopeDraw.approaches = make(map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Code")
			imgui.TableSetupColumn("Description")
			imgui.TableSetupColumn("FAF")
			imgui.TableHeadersRow()

			for _, rwy := range c.State.ArrivalRunways {
				if ap, ok := c.State.Airports[rwy.Airport]; !ok {
					lg.Errorf("%s: arrival airport not in world airports", rwy.Airport)
				} else {
					if c.scopeDraw.approaches[rwy.Airport] == nil {
						c.scopeDraw.approaches[rwy.Airport] = make(map[string]bool)
					}
					for _, name := range util.SortedMapKeys(ap.Approaches) {
						appr := ap.Approaches[name]
						if appr.Runway == rwy.Runway {
							imgui.TableNextRow()
							imgui.TableNextColumn()
							enabled := c.scopeDraw.approaches[rwy.Airport][name]
							imgui.Checkbox("##enable-"+rwy.Airport+"-"+rwy.Runway+"-"+name, &enabled)
							c.scopeDraw.approaches[rwy.Airport][name] = enabled

							imgui.TableNextColumn()
							imgui.Text(rwy.Airport)

							imgui.TableNextColumn()
							imgui.Text(rwy.Runway)

							imgui.TableNextColumn()
							imgui.Text(name)

							imgui.TableNextColumn()
							imgui.Text(appr.FullName)

							imgui.TableNextColumn()
							for _, wp := range appr.Waypoints[0] {
								if wp.FAF {
									imgui.Text(wp.Fix)
									break
								}
							}
						}
					}
				}
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Departures") {
		if imgui.BeginTableV("departures", 5, tableFlags, imgui.Vec2{}, 0) {
			if c.scopeDraw.departures == nil {
				c.scopeDraw.departures = make(map[string]map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, airport := range util.SortedMapKeys(c.State.LaunchConfig.DepartureRates) {
				if c.scopeDraw.departures[airport] == nil {
					c.scopeDraw.departures[airport] = make(map[string]map[string]bool)
				}
				ap := c.State.Airports[airport]

				runwayRates := c.State.LaunchConfig.DepartureRates[airport]
				for _, rwy := range util.SortedMapKeys(runwayRates) {
					if c.scopeDraw.departures[airport][rwy] == nil {
						c.scopeDraw.departures[airport][rwy] = make(map[string]bool)
					}

					exitRoutes := ap.DepartureRoutes[rwy]

					// Multiple routes may have the same waypoints, so
					// we'll reverse-engineer that here so we can present
					// them together in the UI.
					routeToExit := make(map[string][]string)
					for _, exit := range util.SortedMapKeys(exitRoutes) {
						exitRoute := ap.DepartureRoutes[rwy][exit]
						r := exitRoute.Waypoints.Encode()
						routeToExit[r] = append(routeToExit[r], exit)
					}

					for _, exit := range util.SortedMapKeys(exitRoutes) {
						// Draw the row only when we hit the first exit
						// that uses the corresponding route route.
						r := exitRoutes[exit].Waypoints.Encode()
						if routeToExit[r][0] != exit {
							continue
						}

						imgui.TableNextRow()
						imgui.TableNextColumn()
						enabled := c.scopeDraw.departures[airport][rwy][exit]
						imgui.Checkbox("##enable-"+airport+"-"+rwy+"-"+exit, &enabled)
						c.scopeDraw.departures[airport][rwy][exit] = enabled

						imgui.TableNextColumn()
						imgui.Text(airport)
						imgui.TableNextColumn()
						rwyBase, _, _ := strings.Cut(rwy, ".")
						imgui.Text(rwyBase)
						imgui.TableNextColumn()
						if len(routeToExit) == 1 {
							// If we only saw a single departure route, no
							// need to list all of the exits in the UI
							// (there are often a lot of them!)
							imgui.Text("(all)")
						} else {
							// List all of the exits that use this route.
							imgui.Text(strings.Join(routeToExit[r], ", "))
						}
						imgui.TableNextColumn()
						imgui.Text(exitRoutes[exit].Description)
					}
				}
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Overflights") {
		if imgui.BeginTableV("over", 3, tableFlags, imgui.Vec2{}, 0) {
			if c.scopeDraw.overflights == nil {
				c.scopeDraw.overflights = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Overflight")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, name := range util.SortedMapKeys(c.State.InboundFlows) {
				overflights := c.State.InboundFlows[name].Overflights
				if len(overflights) == 0 {
					continue
				}

				if c.scopeDraw.overflights[name] == nil {
					c.scopeDraw.overflights[name] = make(map[int]bool)
				}
				if _, ok := c.State.LaunchConfig.InboundFlowRates[name]["overflights"]; !ok {
					// Not used in the current scenario.
					continue
				}

				for i, of := range overflights {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := c.scopeDraw.overflights[name][i]
					imgui.Checkbox(fmt.Sprintf("##of-%s-%d", name, i), &enabled)
					c.scopeDraw.overflights[name][i] = enabled

					imgui.TableNextColumn()
					imgui.Text(name)

					imgui.TableNextColumn()
					if of.Description != "" {
						imgui.Text(of.Description)
					} else {
						imgui.Text("--")
					}
				}
			}

			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Controllers") {
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("TCP")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Frequency")
			imgui.TableSetupColumn("Name")
			imgui.TableHeadersRow()

			for _, callsign := range util.SortedMapKeys(c.Controllers) {
				ctrl := c.Controllers[callsign]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				id := ctrl.SectorId
				if ctrl.FacilityIdentifier != "" && !ctrl.ERAMFacility {
					id = ctrl.FacilityIdentifier + id
				}
				imgui.Text(id)
				imgui.TableNextColumn()
				if ctrl.IsHuman {
					sq := renderer.FontAwesomeIconCheckSquare
					// Center the square in the column
					// https://stackoverflow.com/a/66109051
					pos := imgui.CursorPosX() + float32(imgui.ColumnWidth()) - imgui.CalcTextSize(sq, false, 0).X - imgui.ScrollX() -
						2*imgui.CurrentStyle().ItemSpacing().X
					if pos > imgui.CursorPosX() {
						imgui.SetCursorPos(imgui.Vec2{X: pos, Y: imgui.CursorPos().Y})
					}
					imgui.Text(sq)
				}
				imgui.TableNextColumn()
				imgui.Text(ctrl.Frequency.String())
				imgui.TableNextColumn()
				imgui.Text(ctrl.Callsign)
			}

			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Tower/Coordination Lists") {
		if imgui.BeginTableV("tclists", 3, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Id")
			imgui.TableSetupColumn("Type")
			imgui.TableSetupColumn("Airports")
			imgui.TableHeadersRow()

			for i, ap := range c.TowerListAirports() {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(strconv.Itoa(i + 1))
				imgui.TableNextColumn()
				imgui.Text("Tower")
				imgui.TableNextColumn()
				imgui.Text(ap)
			}

			cl := util.DuplicateSlice(c.State.STARSFacilityAdaptation.CoordinationLists)
			slices.SortFunc(cl, func(a, b CoordinationList) int { return strings.Compare(a.Id, b.Id) })

			for _, list := range cl {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(list.Id)
				imgui.TableNextColumn()
				imgui.Text("Coord. (" + list.Name + ")")
				imgui.TableNextColumn()
				imgui.Text(strings.Join(list.Airports, ", "))
			}
			imgui.EndTable()
		}
	}

	if aa := c.State.STARSFacilityAdaptation.AirspaceAwareness; len(aa) > 0 {
		if imgui.CollapsingHeader("Airspace Awareness") {
			if imgui.BeginTableV("awareness", 4, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Fix")
				imgui.TableSetupColumn("Altitude")
				imgui.TableSetupColumn("A/C Type")
				imgui.TableSetupColumn("Controller")
				imgui.TableHeadersRow()

				for _, aware := range aa {
					for _, fix := range aware.Fix {
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Text(fix)
						imgui.TableNextColumn()
						alt := ""
						if aware.AltitudeRange[0] > 0 {
							if aware.AltitudeRange[1] < 60000 {
								alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + " - " +
									av.FormatAltitude(float32(aware.AltitudeRange[1]))
							} else {
								alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + "+"
							}
						} else if aware.AltitudeRange[1] < 60000 {
							alt = av.FormatAltitude(float32(aware.AltitudeRange[1])) + "-"
						}
						imgui.Text(alt)
						imgui.TableNextColumn()
						imgui.Text(strings.Join(aware.AircraftType, ", "))
						imgui.TableNextColumn()
						imgui.Text(aware.ReceivingController)
					}
				}

				imgui.EndTable()
			}
		}
	}

	imgui.End()
	return
}

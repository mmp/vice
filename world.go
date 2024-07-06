// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

///////////////////////////////////////////////////////////////////////////
// World

type World struct {
	// Used on the client side only
	simProxy *sim.SimProxy

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *util.PendingCall

	pendingCalls []*util.PendingCall

	client sim.ClientState

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	sim.State
}

func NewWorldFromSimState(ss sim.State, controllerToken string, client *util.RPCClient) *World {
	return &World{
		State: ss,
		simProxy: &sim.SimProxy{
			ControllerToken: controllerToken,
			Client:          client,
		},
		lastUpdateRequest: time.Now(),
	}
}

func (w *World) AllAirports() map[string]*av.Airport {
	all := util.DuplicateMap(w.DepartureAirports)
	for name, ap := range w.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (w *World) SetSquawk(callsign string, squawk av.Squawk) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) TakeOrReturnLaunchControl(eventStream *sim.EventStream) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.TakeOrReturnLaunchControl(),
			IssueTime: time.Now(),
			OnErr: func(e error) {
				eventStream.Post(sim.Event{
					Type:    sim.StatusMessageEvent,
					Message: e.Error(),
				})
			},
		})
}

func (w *World) LaunchAircraft(ac av.Aircraft) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.LaunchAircraft(ac),
			IssueTime: time.Now(),
		})
}

func (w *World) SendGlobalMessage(global sim.GlobalMessage) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.GlobalMessage(global),
			IssueTime: time.Now(),
		})
}

func (w *World) SetScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == w.State.Callsign {
		ac.Scratchpad = scratchpad
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetSecondaryScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == w.State.Callsign {
		ac.SecondaryScratchpad = scratchpad
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetSecondaryScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetTemporaryAltitude(callsign string, alt int, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == w.State.Callsign {
		ac.TempAltitude = alt
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetTemporaryAltitude(callsign, alt),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AmendFlightPlan(callsign string, fp av.FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetGlobalLeaderLine(callsign string, dir *math.CardinalOrdinalDirection, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetGlobalLeaderLine(callsign, dir),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) InitiateTrack(callsign string, success func(any), err func(error)) {
	// Modifying locally is not canonical but improves perceived latency in
	// the common case; the RPC may fail, though that's fine; the next
	// world update will roll back these changes anyway.
	//
	// As in sim.go, only check for an unset TrackingController; we may already
	// have ControllingController due to a pilot checkin on a departure.
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == "" {
		ac.TrackingController = w.State.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.InitiateTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) DropTrack(callsign string, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == w.State.Callsign {
		ac.TrackingController = ""
		ac.ControllingController = ""
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.DropTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffTrack(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptHandoff(callsign string, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.HandoffTrackController == w.State.Callsign {
		ac.HandoffTrackController = ""
		ac.TrackingController = w.State.Callsign
		ac.ControllingController = w.State.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RedirectHandoff(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RedirectHandoff(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptRedirectedHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcceptRedirectedHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) CancelHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ForceQL(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.ForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RemoveForceQL(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RemoveForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) PointOut(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.PointOut(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcknowledgePointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcknowledgePointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RejectPointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RejectPointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ToggleSPCOverride(callsign string, spc string, success func(any), err func(error)) {
	if ac := w.State.Aircraft[callsign]; ac != nil && ac.TrackingController == w.State.Callsign {
		ac.ToggleSPCOverride(spc)
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.ToggleSPCOverride(callsign, spc),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ChangeControlPosition(callsign string, keepTracks bool) error {
	err := w.simProxy.ChangeControlPosition(callsign, keepTracks)
	if err == nil {
		w.State.Callsign = callsign
	}
	return err
}

func (w *World) Disconnect() {
	if err := w.simProxy.SignOff(nil, nil); err != nil {
		lg.Errorf("Error signing off from sim: %v", err)
	}
	w.State.Aircraft = nil
	w.State.Controllers = nil
}

func (w *World) GetUpdates(eventStream *sim.EventStream, onErr func(error)) {
	if w.simProxy == nil {
		return
	}

	if w.updateCall != nil && w.updateCall.CheckFinished() {
		w.updateCall = nil
		return
	}

	w.checkPendingRPCs(eventStream)

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/w.SimRate, 0.05, 1)
	if d := time.Since(w.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if w.updateCall != nil {
			lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			return
		}
		w.lastUpdateRequest = time.Now()

		wu := &sim.SimWorldUpdate{}
		w.updateCall = &util.PendingCall{
			Call:      w.simProxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				d := time.Since(w.updateCall.IssueTime)
				if d > 250*time.Millisecond {
					lg.Warnf("Slow world update response %s", d)
				} else {
					lg.Debugf("World update response time %s", d)
				}
				w.UpdateWorld(wu, eventStream)
			},
			OnErr: onErr,
		}
	}
}

func (w *World) UpdateWorld(wu *sim.SimWorldUpdate, eventStream *sim.EventStream) {
	w.State.Aircraft = wu.Aircraft
	if wu.Controllers != nil {
		w.State.Controllers = wu.Controllers
	}

	w.State.LaunchConfig = wu.LaunchConfig

	w.State.SimTime = wu.Time
	w.State.SimIsPaused = wu.SimIsPaused
	w.State.SimRate = wu.SimRate
	w.State.TotalDepartures = wu.TotalDepartures
	w.State.TotalArrivals = wu.TotalArrivals

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range wu.Events {
		eventStream.Post(e)
	}
}

func (w *World) checkPendingRPCs(eventStream *sim.EventStream) {
	w.pendingCalls = util.FilterSlice(w.pendingCalls,
		func(call *util.PendingCall) bool { return !call.CheckFinished() })
}

func (w *World) Connected() bool {
	return w.simProxy != nil
}

func (w *World) GetSerializeSim() (*sim.Sim, error) {
	return w.simProxy.GetSerializeSim()
}

func (w *World) ToggleSimPause() {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (w *World) GetSimRate() float32 {
	if w.SimRate == 0 {
		return 1
	}
	return w.SimRate
}

func (w *World) SetSimRate(r float32) {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	w.SimRate = r // so the UI is well-behaved...
}

func (w *World) SetLaunchConfig(lc sim.LaunchConfig) {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.SetLaunchConfig(lc),
		IssueTime: time.Now(),
	})
	w.LaunchConfig = lc // for the UI's benefit...
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (w *World) CurrentTime() time.Time {
	t := w.SimTime

	if !w.SimIsPaused && !w.lastUpdateRequest.IsZero() {
		d := time.Since(w.lastUpdateRequest)

		// Roughly account for RPC overhead; more for a remote server (where
		// SimName will be set.)
		if w.SimName == "" {
			d -= 10 * time.Millisecond
		} else {
			d -= 50 * time.Millisecond
		}
		d = math.Max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(w.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(w.lastReturnedTime) {
		w.lastReturnedTime = t
	}
	return w.lastReturnedTime
}

func (w *World) GetWindowTitle() string {
	if w.SimDescription == "" {
		return "(disconnected)"
	} else {
		deparr := fmt.Sprintf(" [ %d departures %d arrivals ]", w.TotalDepartures, w.TotalArrivals)
		if w.SimName == "" {
			return w.State.Callsign + ": " + w.SimDescription + deparr
		} else {
			return w.State.Callsign + "@" + w.SimName + ": " + w.SimDescription + deparr
		}
	}
}

func (w *World) DeleteAllAircraft(onErr func(err error)) {
	if lctrl := w.LaunchConfig.Controller; lctrl == "" || lctrl == w.State.Callsign {
		w.State.Aircraft = nil
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.DeleteAllAircraft(),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

func (w *World) RunAircraftCommands(callsign string, cmds string, handleResult func(message string, remainingInput string)) {
	var result sim.AircraftCommandsResult
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RunAircraftCommands(callsign, cmds, &result),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				handleResult(result.ErrorMessage, result.RemainingInput)
			},
			OnErr: func(err error) {
				lg.Errorf("%s: %v", callsign, err)
			},
		})
}

///////////////////////////////////////////////////////////////////////////
// Settings

func (w *World) ToggleActivateSettingsWindow() {
	ui.showSettings = !ui.showSettings
}

func (w *World) ToggleShowScenarioInfoWindow() {
	ui.showScenarioInfo = !ui.showScenarioInfo
}

type MissingPrimaryModalClient struct {
	world *World
}

func (mp *MissingPrimaryModalClient) Title() string {
	return "Missing Primary Controller"
}

func (mp *MissingPrimaryModalClient) Opening() {}

func (mp *MissingPrimaryModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Sign in to " + mp.world.PrimaryController, action: func() bool {
		err := mp.world.ChangeControlPosition(mp.world.PrimaryController, true)
		return err == nil
	}})
	b = append(b, ModalDialogButton{text: "Disconnect", action: func() bool {
		newSimConnectionChan <- nil // This will lead to a World Disconnect() call in main.go
		uiCloseModalDialog(ui.missingPrimaryDialog)
		return true
	}})
	return b
}

func (mp *MissingPrimaryModalClient) Draw() int {
	imgui.Text("The primary controller, " + mp.world.PrimaryController + ", has disconnected from the server or is otherwise unreachable.\nThe simulation will be paused until a primary controller signs in.")
	return -1
}

func (w *World) DrawMissingPrimaryDialog(p platform.Platform) {
	if _, ok := w.State.Controllers[w.PrimaryController]; ok {
		if ui.missingPrimaryDialog != nil {
			uiCloseModalDialog(ui.missingPrimaryDialog)
			ui.missingPrimaryDialog = nil
		}
	} else {
		if ui.missingPrimaryDialog == nil {
			ui.missingPrimaryDialog = NewModalDialogBox(&MissingPrimaryModalClient{world: w}, p)
			uiShowModalDialog(ui.missingPrimaryDialog, true)
		}
	}
}

func (w *World) DrawSettingsWindow(p platform.Platform) {
	if !ui.showSettings {
		return
	}

	imgui.BeginV("Settings", &ui.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.SliderFloatV("Simulation speed", &w.SimRate, 1, 20, "%.1f", 0) {
		w.SetSimRate(w.SimRate)
	}

	update := !globalConfig.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	globalConfig.InhibitDiscordActivity.Store(!update)

	if imgui.BeginComboV("UI Font Size", strconv.Itoa(globalConfig.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := renderer.AvailableFontSizes("Roboto Regular")
		for _, size := range sizes {
			if imgui.SelectableV(strconv.Itoa(size), size == globalConfig.UIFontSize, 0, imgui.Vec2{}) {
				globalConfig.UIFontSize = size
				ui.font = renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
			}
		}
		imgui.EndCombo()
	}

	var fsp *FlightStripPane
	var messages *MessagesPane
	var stars *STARSPane
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		switch pane := p.(type) {
		case *FlightStripPane:
			fsp = pane
		case *STARSPane:
			stars = pane
		case *MessagesPane:
			messages = pane
		}
	})

	if imgui.CollapsingHeader("STARS") {
		stars.DrawUI(p)
	}

	if imgui.CollapsingHeader("Display") {
		if imgui.Checkbox("Enable anti-aliasing", &globalConfig.EnableMSAA) {
			uiShowModalDialog(NewModalDialogBox(
				&MessageModalClient{
					title: "Alert",
					message: "You must restart vice for changes to the anti-aliasing " +
						"mode to take effect.",
				}, p), true)
		}

		imgui.Checkbox("Start in full-screen", &globalConfig.StartInFullScreen)

		monitorNames := p.GetAllMonitorNames()
		if imgui.BeginComboV("Monitor", monitorNames[globalConfig.FullScreenMonitor], imgui.ComboFlagsHeightLarge) {
			for index, monitor := range monitorNames {
				if imgui.SelectableV(monitor, monitor == monitorNames[globalConfig.FullScreenMonitor], 0, imgui.Vec2{}) {
					globalConfig.FullScreenMonitor = index

					p.EnableFullScreen(p.IsFullScreen())
				}
			}

			imgui.EndCombo()
		}
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if messages != nil && imgui.CollapsingHeader("Messages") {
		messages.DrawUI()
	}

	imgui.End()
}

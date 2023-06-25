// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"math"
	"net/rpc"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

const initialSimSeconds = 45

var (
	ErrArrivalAirportUnknown        = errors.New("Arrival airport unknown")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrNoAircraftForCallsign        = errors.New("No aircraft exists with specified callsign")
	ErrNoFlightPlan                 = errors.New("No flight plan has been filed for aircraft")
	ErrOtherControllerHasTrack      = errors.New("Another controller is already tracking the aircraft")
	ErrNotBeingHandedOffToMe        = errors.New("Aircraft not being handed off to current controller")
	ErrNoController                 = errors.New("No controller with that callsign")
	ErrUnknownAircraftType          = errors.New("Unknown aircraft type")
	ErrUnableCommand                = errors.New("Unable")
	ErrInvalidAltitude              = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidHeading               = errors.New("Invalid heading")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidCommandSyntax         = errors.New("Invalid command syntax")
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
)

///////////////////////////////////////////////////////////////////////////
// World

type World struct {
	// Used on the client side only
	simProxy *SimProxy

	Aircraft    map[string]*Aircraft
	METAR       map[string]*METAR
	Controllers map[string]*Controller

	DepartureAirports map[string]*Airport
	ArrivalAirports   map[string]*Airport

	lastUpdate   time.Time
	updateCall   *PendingCall
	showSettings bool

	pendingCalls []*PendingCall

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	SimIsPaused       bool
	SimRate           float32
	SimDescription    string
	SimTime           time.Time
	MagneticVariation float32
	NmPerLongitude    float32
	Airports          map[string]*Airport
	Fixes             map[string]Point2LL
	PrimaryAirport    string
	RadarSites        map[string]*RadarSite
	Center            Point2LL
	Range             float32
	DefaultMap        string
	STARSMaps         []STARSMap
	Wind              Wind
	Callsign          string
	ApproachAirspace  []AirspaceVolume
	DepartureAirspace []AirspaceVolume
	DepartureRunways  []ScenarioGroupDepartureRunway
	Scratchpads       map[string]string
	ArrivalGroups     map[string][]Arrival
}

type PendingCall struct {
	Call      *rpc.Call
	IssueTime time.Time
	OnSuccess func()
	OnErr     func(error)
}

func (p *PendingCall) CheckFinished() bool {
	select {
	case c := <-p.Call.Done:
		lg.Printf("%s: returned in %s", c.ServiceMethod, time.Since(p.IssueTime))
		if c.Error != nil {
			if p.OnErr != nil {
				p.OnErr(c.Error)
			}
		} else if p.OnSuccess != nil {
			p.OnSuccess()
		}
		return true

	default:
		if s := time.Since(p.IssueTime); s > time.Second {
			lg.Errorf("%s: no response still... %s", p.Call.ServiceMethod, s)
		}
		return false
	}
}

func NewWorld() *World {
	return &World{
		Aircraft:    make(map[string]*Aircraft),
		METAR:       make(map[string]*METAR),
		Controllers: make(map[string]*Controller),
	}
}

func (w *World) Assign(other *World) {
	w.Aircraft = DuplicateMap(other.Aircraft)
	w.METAR = DuplicateMap(other.METAR)
	w.Controllers = DuplicateMap(other.Controllers)

	w.DepartureAirports = other.DepartureAirports
	w.ArrivalAirports = other.ArrivalAirports

	w.MagneticVariation = other.MagneticVariation
	w.NmPerLongitude = other.NmPerLongitude
	w.Airports = other.Airports
	w.Fixes = other.Fixes
	w.PrimaryAirport = other.PrimaryAirport
	w.RadarSites = other.RadarSites
	w.Center = other.Center
	w.Range = other.Range
	w.DefaultMap = other.DefaultMap
	w.STARSMaps = other.STARSMaps
	w.Wind = other.Wind
	w.Callsign = other.Callsign
	w.ApproachAirspace = other.ApproachAirspace
	w.DepartureAirspace = other.DepartureAirspace
	w.DepartureRunways = other.DepartureRunways
	w.Scratchpads = other.Scratchpads
	w.ArrivalGroups = other.ArrivalGroups
}

func (w *World) GetWindVector(p Point2LL, alt float32) Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := w.SimTime.Sub(base).Seconds()
	windSpeed := float32(w.Wind.Speed) +
		float32(w.Wind.Gust)*float32(1+math.Cos(sec/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := OppositeHeading(float32(w.Wind.Direction))
	vWind := [2]float32{sin(radians(d)), cos(radians(d))}
	vWind = scale2f(vWind, windSpeed/3600)
	return vWind
}

func (w *World) GetAirport(icao string) *Airport {
	return w.Airports[icao]
}

func (w *World) Locate(s string) (Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := w.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := w.Fixes[s]; ok {
		return p, true
	} else if n, ok := database.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := database.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := database.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (w *World) AllAirports() map[string]*Airport {
	all := DuplicateMap(w.DepartureAirports)
	for name, ap := range w.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (w *World) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetScratchpad(callsign string, scratchpad string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.SetScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetTemporaryAltitude(callsign string, alt int, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.SetTemporaryAltitude(callsign, alt),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) InitiateTrack(callsign string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.InitiateTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) DropTrack(callsign string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.DropTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffTrack(callsign string, controller string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffControl(callsign string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.HandoffControl(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptHandoff(callsign string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RejectHandoff(callsign string, success func(), err func(error)) {
	// UNIMPLEMENTED
}

func (w *World) CancelHandoff(callsign string, success func(), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) PointOut(callsign string, controller string, success func(), err func(error)) {
	// UNIMPLEMENTED
}

func (w *World) Disconnect() {
	if err := w.simProxy.SignOff(nil, nil); err != nil {
		lg.Errorf("Error signing off from sim: %v", err)
	}
	w.Aircraft = nil
	w.Controllers = nil
}

func (w *World) GetAircraft(callsign string) *Aircraft {
	if ac, ok := w.Aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (w *World) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range w.Aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (w *World) GetAllAircraft() []*Aircraft {
	return w.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (w *World) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := w.Aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (w *World) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (w *World) GetMETAR(location string) *METAR {
	return w.METAR[location]
}

func (w *World) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (w *World) GetController(callsign string) *Controller {
	if ctrl := w.Controllers[callsign]; ctrl != nil {
		return ctrl
	}

	// Look up by id
	for _, ctrl := range w.Controllers {
		if ctrl.SectorId == callsign {
			return ctrl
		}
	}

	return nil
}

func (w *World) GetAllControllers() map[string]*Controller {
	return w.Controllers
}

func (w *World) GetUpdates(eventStream *EventStream, onErr func(error)) {
	if w.simProxy == nil {
		return
	}

	if w.updateCall != nil && w.updateCall.CheckFinished() {
		w.updateCall = nil
		return
	}

	w.checkPendingRPCs()

	if time.Since(w.lastUpdate) > 1*time.Second {
		if w.updateCall != nil {
			lg.Errorf("Still waiting on last update call!")
			return
		}

		wu := &SimWorldUpdate{}
		w.updateCall = &PendingCall{
			Call:      w.simProxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func() {
				wu.UpdateWorld(w, eventStream)
				for _, ac := range w.Aircraft {
					if math.IsNaN(float64(ac.Position[0])) || math.IsNaN(float64(ac.Position[1])) {
						panic("wah")
					}
				}
				w.lastUpdate = time.Now()
			},
			OnErr: onErr,
		}
	}
}

func (w *World) checkPendingRPCs() {
	w.pendingCalls = FilterSlice(w.pendingCalls,
		func(call *PendingCall) bool { return !call.CheckFinished() })
}

func (w *World) Connected() bool {
	return w.simProxy != nil
}

func (w *World) ToggleSimPause() {
	w.pendingCalls = append(w.pendingCalls, &PendingCall{
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
	w.pendingCalls = append(w.pendingCalls, &PendingCall{
		Call:      w.simProxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	w.SimRate = r // so the UI is well-behaved...
}

func (w *World) CurrentTime() time.Time {
	d := time.Since(w.lastUpdate)
	if w.SimRate != 0 {
		d = time.Duration(float64(d) * float64(w.SimRate))
	}
	return w.SimTime.Add(d)
}

func (w *World) GetWindowTitle() string {
	if w.SimDescription == "" {
		return "(disconnected)"
	}
	return w.Callsign + ": " + w.SimDescription
}

func (w *World) PrintInfo(ac *Aircraft) {
	lg.Errorf("%s", spew.Sdump(ac))

	s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
		ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
	if ac.ApproachCleared {
		s += ", cleared approach"
	}
	lg.Errorf("%s", s)
}

func (w *World) DeleteAircraft(ac *Aircraft) {
	if w.simProxy != nil {
		w.pendingCalls = append(w.pendingCalls,
			&PendingCall{
				Call:      w.simProxy.DeleteAircraft(ac.Callsign),
				IssueTime: time.Now(),
			})
	} else {
		delete(w.Aircraft, ac.Callsign)
	}
}

func (w *World) RunAircraftCommands(ac *Aircraft, cmds string, onErr func(err error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.RunAircraftCommands(ac.Callsign, cmds),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

///////////////////////////////////////////////////////////////////////////
// Settings

func (w *World) ToggleActivateSettingsWindow() {
	w.showSettings = !w.showSettings
}

func (w *World) DrawSettingsWindow() {
	if !w.showSettings {
		return
	}

	imgui.BeginV("Settings", &w.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	max := Select(*devmode, float32(100), float32(10))
	if imgui.SliderFloatV("Simulation speed", &w.SimRate, 1, max, "%.1f", 0) {
		w.SetSimRate(w.SimRate)
	}

	if imgui.BeginComboV("UI Font Size", fmt.Sprintf("%d", globalConfig.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Roboto Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range SortedMapKeys(sizes) {
			if imgui.SelectableV(fmt.Sprintf("%d", size), size == globalConfig.UIFontSize, 0, imgui.Vec2{}) {
				globalConfig.UIFontSize = size
				ui.font = GetFont(FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
			}
		}
		imgui.EndCombo()
	}
	if imgui.BeginComboV("STARS DCB Font Size", fmt.Sprintf("%d", globalConfig.DCBFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Inconsolata Condensed Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range SortedMapKeys(sizes) {
			if imgui.SelectableV(fmt.Sprintf("%d", size), size == globalConfig.DCBFontSize, 0, imgui.Vec2{}) {
				globalConfig.DCBFontSize = size
			}
		}
		imgui.EndCombo()
	}

	var fsp *FlightStripPane
	var stars *STARSPane
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		switch pane := p.(type) {
		case *FlightStripPane:
			fsp = pane
		case *STARSPane:
			stars = pane
		}
	})

	stars.DrawUI()

	imgui.Separator()

	if imgui.CollapsingHeader("Audio") {
		globalConfig.Audio.DrawUI()
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if imgui.CollapsingHeader("Developer") {
		if imgui.BeginTableV("GlobalFiles", 4, 0, imgui.Vec2{}, 0) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Scenario:")
			imgui.TableNextColumn()
			imgui.Text(globalConfig.DevScenarioFile)
			imgui.TableNextColumn()
			if imgui.Button("New...##scenario") {
				ui.jsonSelectDialog = NewFileSelectDialogBox("Select JSON File", []string{".json"},
					globalConfig.DevScenarioFile, func(filename string) {
						globalConfig.DevScenarioFile = filename
						ui.jsonSelectDialog = nil
					})
				ui.jsonSelectDialog.Activate()
			}
			imgui.TableNextColumn()
			if globalConfig.DevScenarioFile != "" && imgui.Button("Clear##scenario") {
				globalConfig.DevScenarioFile = ""
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Video maps:")
			imgui.TableNextColumn()
			imgui.Text(globalConfig.DevVideoMapFile)
			imgui.TableNextColumn()
			if imgui.Button("New...##vid") {
				ui.jsonSelectDialog = NewFileSelectDialogBox("Select JSON File", []string{".json"},
					globalConfig.DevVideoMapFile, func(filename string) {
						globalConfig.DevVideoMapFile = filename
						ui.jsonSelectDialog = nil
					})
				ui.jsonSelectDialog.Activate()
			}
			imgui.TableNextColumn()
			if globalConfig.DevVideoMapFile != "" && imgui.Button("Clear##vid") {
				globalConfig.DevVideoMapFile = ""
			}

			imgui.EndTable()
		}

		if ui.jsonSelectDialog != nil {
			ui.jsonSelectDialog.Draw()
		}
	}

	imgui.End()
}

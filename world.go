// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"math"
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
	token string
	sim   *Sim

	Aircraft    map[string]*Aircraft
	METAR       map[string]*METAR
	Controllers map[string]*Controller

	DepartureAirports map[string]*Airport
	ArrivalAirports   map[string]*Airport

	lastUpdate   time.Time
	showSettings bool

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	UpdateSimTime                 time.Time
	MagneticVariation             float32
	NmPerLatitude, NmPerLongitude float32
	Airports                      map[string]*Airport
	Fixes                         map[string]Point2LL
	PrimaryAirport                string
	RadarSites                    map[string]*RadarSite
	Center                        Point2LL
	Range                         float32
	DefaultMap                    string
	STARSMaps                     []STARSMap
	Wind                          Wind
	Callsign                      string
	ApproachAirspace              []AirspaceVolume
	DepartureAirspace             []AirspaceVolume
	DepartureRunways              []ScenarioGroupDepartureRunway
	Scratchpads                   map[string]string
	ArrivalGroups                 map[string][]Arrival
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
	w.NmPerLatitude = other.NmPerLatitude
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

func (w *World) GetSerializeSim() *Sim {
	if w.sim != nil {
		// FIXME: should do this in sim.go
		w.sim.SerializeTime = w.sim.CurrentTime()
	}
	return w.sim
}

func (w *World) GetWindVector(p Point2LL, alt float32) Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := w.UpdateSimTime.Sub(base).Seconds()
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

func (w *World) SetScratchpad(callsign string, scratchpad string) error {
	return w.sim.SetScratchpad(&AircraftPropertiesSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil)
}

func (w *World) SetTemporaryAltitude(callsign string, alt int) error {
	return w.sim.SetTemporaryAltitude(&AltitudeAssignment{
		ControllerToken: w.token,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil)
}

func (w *World) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) InitiateTrack(callsign string) error {
	return w.sim.InitiateTrack(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) DropTrack(callsign string) error {
	return w.sim.DropTrack(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) HandoffTrack(callsign string, controller string) error {
	return w.sim.HandoffTrack(&HandoffSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
		Controller:      controller,
	}, nil)
}

func (w *World) HandoffControl(callsign string) error {
	return w.sim.HandoffControl(&HandoffSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) AcceptHandoff(callsign string) error {
	return w.sim.AcceptHandoff(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) CancelHandoff(callsign string) error {
	return w.sim.CancelHandoff(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) Disconnect() {
	if err := w.sim.SignOff(w.token, nil); err != nil {
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

func (w *World) GetUpdates(eventStream *EventStream) {
	if w.sim == nil {
		return
	}

	w.sim.Update()

	if time.Since(w.lastUpdate) > 1*time.Second {
		updates, err := w.sim.GetWorldUpdate(w.token)
		if err != nil {
			lg.Errorf("Error getting world update: %v", err)
		}

		w.Aircraft = updates.Aircraft
		w.Controllers = updates.Controllers
		w.UpdateSimTime = updates.Time

		// Important: do this after updating aircraft, controllers, etc.,
		// so that they reflect any changes the events are flagging.
		for _, e := range updates.Events {
			eventStream.Post(e)
		}

		w.lastUpdate = time.Now()
	}
}

func (w *World) Connected() bool {
	return w.sim != nil
}

func (w *World) SimIsPaused() bool {
	return w.sim != nil && w.sim.IsPaused()
}

func (w *World) ToggleSimPause() {
	if w.sim != nil {
		w.sim.TogglePause()
	}
}

func (w *World) GetSimRate() float32 {
	if w.sim == nil {
		return 1
	}
	return w.sim.GetSimRate()
}

func (w *World) SetSimRate(r float32) {
	if w.sim != nil {
		w.sim.SetSimRate(&r, nil)
	}
}

func (w *World) CurrentTime() time.Time {
	if w.sim == nil {
		return w.UpdateSimTime
	}
	return w.sim.CurrentTime()
}

func (w *World) GetWindowTitle() string {
	if w.sim == nil {
		return "(disconnected)"
	}
	return w.Callsign + ": " + w.sim.Description()
}

func (w *World) AssignAltitude(ac *Aircraft, altitude int) error {
	return w.sim.AssignAltitude(&AltitudeAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Altitude:        altitude,
	}, nil)
}

func (w *World) AssignHeading(ac *Aircraft, heading int, turn TurnMethod) error {
	return w.sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Heading:         heading,
		Turn:            turn,
	}, nil)
}

func (w *World) FlyPresentHeading(ac *Aircraft) error {
	return w.sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Present:         true,
	}, nil)
}

func (w *World) TurnLeft(ac *Aircraft, deg int) error {
	return w.sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		LeftDegrees:     deg,
	}, nil)
}

func (w *World) TurnRight(ac *Aircraft, deg int) error {
	return w.sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		RightDegrees:    deg,
	}, nil)
}

func (w *World) AssignSpeed(ac *Aircraft, speed int) error {
	return w.sim.AssignSpeed(&SpeedAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Speed:           speed,
	}, nil)
}

func (w *World) DirectFix(ac *Aircraft, fix string) error {
	return w.sim.DirectFix(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
	}, nil)
}

func (w *World) DepartFixHeading(ac *Aircraft, fix string, hdg int) error {
	return w.sim.DepartFixHeading(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Heading:         hdg,
	}, nil)
}

func (w *World) CrossFixAt(ac *Aircraft, fix string, alt int, speed int) error {
	return w.sim.CrossFixAt(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Altitude:        alt,
		Speed:           speed,
	}, nil)
}

func (w *World) ExpectApproach(ac *Aircraft, approach string) error {
	return w.sim.ExpectApproach(&ApproachAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, nil)
}

func (w *World) ClearedApproach(ac *Aircraft, approach string) error {
	return w.sim.ClearedApproach(&ApproachClearance{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, nil)
}

func (w *World) ClearedStraightInApproach(ac *Aircraft, approach string) error {
	return w.sim.ClearedApproach(&ApproachClearance{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
		StraightIn:      true,
	}, nil)
}

func (w *World) GoAround(ac *Aircraft) error {
	return w.sim.GoAround(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
	}, nil)
}

func (w *World) PrintInfo(ac *Aircraft) error {
	lg.Errorf("%s", spew.Sdump(ac))

	s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
		ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
	if ac.ApproachCleared {
		s += ", cleared approach"
	}
	lg.Errorf("%s", s)
	return nil
}

func (w *World) DeleteAircraft(ac *Aircraft) error {
	if w.sim != nil {
		return w.sim.DeleteAircraft(&AircraftSpecifier{
			ControllerToken: w.token,
			Callsign:        ac.Callsign,
		}, nil)
	} else {
		delete(w.Aircraft, ac.Callsign)
		return nil
	}
}

func (w *World) RunAircraftCommands(ac *Aircraft, cmds string) ([]string, error) {
	var result AircraftCommandsResult
	err := w.sim.RunAircraftCommands(&AircraftCommandsSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Commands:        cmds,
	}, &result)
	return result.RemainingCommands, err
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

	r := w.GetSimRate()
	max := Select(*devmode, float32(100), float32(10))
	if imgui.SliderFloatV("Simulation speed", &r, 1, max, "%.1f", 0) {
		w.SetSimRate(r)
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

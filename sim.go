// sim.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
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

type NewSimConfiguration struct {
	departureChallenge float32
	goAroundRate       float32
	scenario           *Scenario
	scenarioGroup      *ScenarioGroup
	controller         *Controller
	validControllers   map[string]*Controller

	// airport -> runway -> category -> rate
	departureRates map[string]map[string]map[string]*int32
	// arrival group -> airport -> rate
	arrivalGroupRates map[string]map[string]*int32
}

func (c *NewSimConfiguration) Initialize() {
	c.departureChallenge = 0.25
	c.goAroundRate = 0.10

	// Use the last scenario, if available.
	sg := scenarioGroups[globalConfig.LastScenarioGroup]
	if sg == nil && len(scenarioGroups) > 0 {
		// Otherwise take the first one alphabetically.
		sg = scenarioGroups[SortedMapKeys(scenarioGroups)[0]]
	}
	c.SetScenarioGroup(sg)
}

func (c *NewSimConfiguration) SetScenarioGroup(sg *ScenarioGroup) {
	c.scenarioGroup = sg

	c.validControllers = make(map[string]*Controller)
	for _, sc := range sg.Scenarios {
		c.validControllers[sc.Callsign] = sg.ControlPositions[sc.Callsign]
	}
	c.controller = sg.ControlPositions[sg.DefaultController]

	c.SetScenario(sg.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(name string) {
	var ok bool
	c.scenario, ok = c.scenarioGroup.Scenarios[name]
	if !ok {
		lg.Errorf("%s: called SetScenario with an unknown scenario name???", name)
		return
	}

	c.arrivalGroupRates = DuplicateMap(c.scenario.ArrivalGroupDefaultRates)

	c.departureRates = make(map[string]map[string]map[string]*int32)
	for _, rwy := range c.scenario.DepartureRunways {
		if _, ok := c.departureRates[rwy.Airport]; !ok {
			c.departureRates[rwy.Airport] = make(map[string]map[string]*int32)
		}
		if _, ok := c.departureRates[rwy.Airport][rwy.Runway]; !ok {
			c.departureRates[rwy.Airport][rwy.Runway] = make(map[string]*int32)
		}
		c.departureRates[rwy.Airport][rwy.Runway][rwy.Category] = new(int32)
		*c.departureRates[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate
	}
}

func (c *NewSimConfiguration) DrawUI() bool {
	if imgui.BeginComboV("Scenario Group", c.scenarioGroup.Name, imgui.ComboFlagsHeightLarge) {
		for _, name := range SortedMapKeys(scenarioGroups) {
			if imgui.SelectableV(name, name == c.scenarioGroup.Name, 0, imgui.Vec2{}) {
				c.SetScenarioGroup(scenarioGroups[name])
			}
		}
		imgui.EndCombo()
	}

	if imgui.BeginComboV("Control Position", c.controller.Callsign, imgui.ComboFlagsHeightLarge) {
		for _, controllerName := range SortedMapKeys(c.validControllers) {
			if imgui.SelectableV(controllerName, controllerName == c.controller.Callsign, 0, imgui.Vec2{}) {
				c.controller = c.validControllers[controllerName]
				// Set the current scenario to the first one alphabetically
				// with the selected controller.
				for _, scenarioName := range SortedMapKeys(c.scenarioGroup.Scenarios) {
					if c.scenarioGroup.Scenarios[scenarioName].Callsign == controllerName {
						c.SetScenario(scenarioName)
						break
					}
				}
			}
		}
		imgui.EndCombo()
	}

	scenario := c.scenario

	if imgui.BeginComboV("Config", scenario.Name(), imgui.ComboFlagsHeightLarge) {
		for _, name := range SortedMapKeys(c.scenarioGroup.Scenarios) {
			if c.scenarioGroup.Scenarios[name].Callsign != c.controller.Callsign {
				continue
			}
			if imgui.SelectableV(name, name == scenario.Name(), 0, imgui.Vec2{}) {
				c.SetScenario(name)
			}
		}
		imgui.EndCombo()
	}

	if imgui.BeginTableV("scenario", 2, 0, imgui.Vec2{500, 0}, 0.) {
		imgui.TableNextRow()
		imgui.TableNextColumn()

		if len(c.departureRates) > 0 {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Departing:")
			imgui.TableNextColumn()

			var runways []string
			for airport, runwayRates := range c.departureRates {
				for runway, categoryRates := range runwayRates {
					for _, rate := range categoryRates {
						if *rate > 0 {
							runways = append(runways, airport+"/"+runway)
							break
						}
					}
				}
			}
			sort.Strings(runways)
			imgui.Text(strings.Join(runways, ", "))
		}

		if len(scenario.ArrivalRunways) > 0 {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Landing:")
			imgui.TableNextColumn()

			var a []string
			for _, rwy := range scenario.ArrivalRunways {
				a = append(a, rwy.Airport+"/"+rwy.Runway)
			}
			sort.Strings(a)
			imgui.Text(strings.Join(a, ", "))
		}

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Wind:")
		imgui.TableNextColumn()
		if scenario.Wind.Gust > scenario.Wind.Speed {
			imgui.Text(fmt.Sprintf("%d at %d gust %d", scenario.Wind.Direction, scenario.Wind.Speed, scenario.Wind.Gust))
		} else {
			imgui.Text(fmt.Sprintf("%d at %d", scenario.Wind.Direction, scenario.Wind.Speed))
		}
		imgui.EndTable()
	}

	if len(scenario.DepartureRunways) > 0 {
		imgui.Separator()
		imgui.Text("Departures")

		sumRates := 0
		for _, runwayRates := range c.departureRates {
			for _, categoryRates := range runwayRates {
				for _, rate := range categoryRates {
					sumRates += int(*rate)
				}
			}
		}
		imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", sumRates))

		imgui.SliderFloatV("Sequencing challenge", &c.departureChallenge, 0, 1, "%.02f", 0)
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

		if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
			imgui.TableHeadersRow()

			for _, airport := range SortedMapKeys(c.departureRates) {
				imgui.PushID(airport)
				for _, runway := range SortedMapKeys(c.departureRates[airport]) {
					imgui.PushID(runway)
					for _, category := range SortedMapKeys(c.departureRates[airport][runway]) {
						rate := c.departureRates[airport][runway][category]
						imgui.PushID(category)

						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Text(airport)
						imgui.TableNextColumn()
						imgui.Text(runway)
						imgui.TableNextColumn()
						if category == "" {
							imgui.Text("(All)")
						} else {
							imgui.Text(category)
						}
						imgui.TableNextColumn()
						imgui.InputIntV("##adr", rate, 0, 120, 0)

						imgui.PopID()
					}
					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	if len(c.arrivalGroupRates) > 0 {
		// Figure out how many unique airports we've got for AAR columns in the table
		// and also sum up the overall arrival rate
		allAirports := make(map[string]interface{})
		sumRates := 0
		for _, agr := range c.arrivalGroupRates {
			for ap, rate := range agr {
				allAirports[ap] = nil
				sumRates += int(*rate)
			}
		}
		nAirports := len(allAirports)

		imgui.Separator()
		imgui.Text("Arrivals")
		imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", sumRates))
		imgui.SliderFloatV("Go around probability", &c.goAroundRate, 0, 1, "%.02f", 0)

		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("arrivalgroups", 1+nAirports, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Arrival")
			sortedAirports := SortedMapKeys(allAirports)
			for _, ap := range sortedAirports {
				imgui.TableSetupColumn(ap + " AAR")
			}
			imgui.TableHeadersRow()

			for _, group := range SortedMapKeys(c.arrivalGroupRates) {
				imgui.PushID(group)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(group)
				for _, ap := range sortedAirports {
					imgui.TableNextColumn()
					if rate, ok := c.arrivalGroupRates[group][ap]; ok {
						imgui.InputIntV("##aar-"+ap, rate, 0, 120, 0)
					}
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	return false
}

func (c *NewSimConfiguration) Start() error {
	// Send out events to remove any existing aircraft (necessary for when
	// we restart...)
	for _, ac := range sim.GetAllAircraft() {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	sim.Disconnect()

	server = NewServer(*c)
	var err error
	sim, err = server.SignOn(c.scenario.Callsign)
	if err != nil {
		return err
	}
	globalConfig.LastScenarioGroup = c.scenarioGroup.Name

	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		if stars, ok := p.(*STARSPane); ok {
			stars.ResetWorld()
		}
	})

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	token string

	ScenarioGroupName string
	ScenarioName      string

	Aircraft    map[string]*Aircraft
	METAR       map[string]*METAR
	Controllers map[string]*Controller

	DepartureAirports map[string]*Airport
	ArrivalAirports   map[string]*Airport

	eventsId EventSubscriberId

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	MagneticVariation             float32
	NmPerLatitude, NmPerLongitude float32
	Airports                      map[string]*Airport
	Fixes                         map[string]Point2LL
	PrimaryAirport                string
	RadarSites                    map[string]*RadarSite
	Center                        Point2LL
	Range                         float32
	STARSMaps                     []STARSMap
	Wind                          Wind
	Callsign                      string
	ApproachAirspace              []AirspaceVolume
	DepartureAirspace             []AirspaceVolume
	DepartureRunways              []ScenarioGroupDepartureRunway
	Scratchpads                   map[string]string
	ArrivalGroups                 map[string][]Arrival
}

func (sim *Sim) GetWindVector(p Point2LL, alt float32) Point2LL {
	return server.GetWindVector(p, alt)
}

func (sim *Sim) GetAirport(icao string) *Airport {
	return sim.Airports[icao]
}

func (sim *Sim) Locate(s string) (Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := sim.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := sim.Fixes[s]; ok {
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

func (sim *Sim) AllAirports() map[string]*Airport {
	all := DuplicateMap(sim.DepartureAirports)
	for name, ap := range sim.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (sim *Sim) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) SetScratchpad(callsign string, scratchpad string) error {
	return server.SetScratchpad(&AircraftPropertiesSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil)
}

func (sim *Sim) SetTemporaryAltitude(callsign string, alt int) error {
	return server.SetTemporaryAltitude(&AltitudeAssignment{
		ControllerToken: sim.token,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil)
}

func (sim *Sim) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) InitiateTrack(callsign string) error {
	return server.InitiateTrack(&AircraftSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
	}, nil)
}

func (sim *Sim) DropTrack(callsign string) error {
	return server.DropTrack(&AircraftSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
	}, nil)
}

func (sim *Sim) Handoff(callsign string, controller string) error {
	return server.Handoff(&HandoffSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
		Controller:      controller,
	}, nil)
}

func (sim *Sim) AcceptHandoff(callsign string) error {
	return server.AcceptHandoff(&AircraftSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
	}, nil)
}

func (sim *Sim) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) CancelHandoff(callsign string) error {
	return server.CancelHandoff(&AircraftSpecifier{
		ControllerToken: sim.token,
		Callsign:        callsign,
	}, nil)
}

func (sim *Sim) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) Disconnect() {
	for _, ac := range sim.Aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	if sim.eventsId != InvalidEventSubscriberId {
		eventStream.Unsubscribe(sim.eventsId)
		sim.eventsId = InvalidEventSubscriberId
	}
}

func (sim *Sim) GetAircraft(callsign string) *Aircraft {
	if ac, ok := sim.Aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (sim *Sim) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range sim.Aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (sim *Sim) GetAllAircraft() []*Aircraft {
	return sim.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (sim *Sim) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := sim.Aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (sim *Sim) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (sim *Sim) GetMETAR(location string) *METAR {
	return sim.METAR[location]
}

func (sim *Sim) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (sim *Sim) GetController(callsign string) *Controller {
	if ctrl := sim.Controllers[callsign]; ctrl != nil {
		return ctrl
	}

	// Look up by id
	for _, ctrl := range sim.Controllers {
		if ctrl.SectorId == callsign {
			return ctrl
		}
	}

	return nil
}

func (sim *Sim) GetAllControllers() map[string]*Controller {
	return sim.Controllers
}

func (sim *Sim) GetUpdates() {
	if server != nil {
		server.Update()
	}
}

func (sim *Sim) Connected() bool {
	return server != nil
}

func (sim *Sim) CurrentTime() time.Time {
	if server == nil {
		return time.Time{}
	}
	return server.CurrentTime()
}

func (sim *Sim) GetWindowTitle() string {
	return sim.Callsign + ": " + sim.ScenarioName
}

func pilotResponse(ac *Aircraft, fm string, args ...interface{}) {
	lg.Printf("%s: %s", ac.Callsign, fmt.Sprintf(fm, args...))
	eventStream.Post(&RadioTransmissionEvent{callsign: ac.Callsign, message: fmt.Sprintf(fm, args...)})
}

func (sim *Sim) AssignAltitude(ac *Aircraft, altitude int) error {
	var resp string
	err := server.AssignAltitude(&AltitudeAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Altitude:        altitude,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) AssignHeading(ac *Aircraft, heading int, turn TurnMethod) error {
	var resp string
	err := server.AssignHeading(&HeadingAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Heading:         heading,
		Turn:            turn,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) FlyPresentHeading(ac *Aircraft) error {
	var resp string
	err := server.AssignHeading(&HeadingAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Present:         true,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) TurnLeft(ac *Aircraft, deg int) error {
	var resp string
	err := server.AssignHeading(&HeadingAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		LeftDegrees:     deg,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) TurnRight(ac *Aircraft, deg int) error {
	var resp string
	err := server.AssignHeading(&HeadingAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		RightDegrees:    deg,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) AssignSpeed(ac *Aircraft, speed int) error {
	var resp string
	err := server.AssignSpeed(&SpeedAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Speed:           speed,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) DirectFix(ac *Aircraft, fix string) error {
	var resp string
	err := server.DirectFix(&FixSpecifier{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) DepartFixHeading(ac *Aircraft, fix string, hdg int) error {
	var resp string
	err := server.DepartFixHeading(&FixSpecifier{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Heading:         hdg,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) CrossFixAt(ac *Aircraft, fix string, alt int, speed int) error {
	var resp string
	err := server.CrossFixAt(&FixSpecifier{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Altitude:        alt,
		Speed:           speed,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) ExpectApproach(ac *Aircraft, approach string) error {
	var resp string
	err := server.ExpectApproach(&ApproachAssignment{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) ClearedApproach(ac *Aircraft, approach string) error {
	var resp string
	err := server.ClearedApproach(&ApproachClearance{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) ClearedStraightInApproach(ac *Aircraft, approach string) error {
	var resp string
	err := server.ClearedApproach(&ApproachClearance{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
		StraightIn:      true,
	}, &resp)
	if resp != "" {
		pilotResponse(ac, "%s", resp)
	}
	return err
}

func (sim *Sim) GoAround(ac *Aircraft) error {
	ac.GoAround()

	// If it was handed off to tower, hand it back to us
	if ac.TrackingController != "" && ac.TrackingController != sim.Callsign {
		ac.InboundHandoffController = sim.Callsign
		globalConfig.Audio.PlaySound(AudioEventInboundHandoff)
	}

	pilotResponse(ac, "Going around")
	return nil
}

func (sim *Sim) PrintInfo(ac *Aircraft) error {
	lg.Errorf("%s", spew.Sdump(ac))

	s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
		ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
	if ac.ApproachCleared {
		s += ", cleared approach"
	}
	lg.Errorf("%s", s)
	return nil
}

func (sim *Sim) DeleteAircraft(ac *Aircraft) error {
	return server.DeleteAircraft(&AircraftSpecifier{
		ControllerToken: sim.token,
		Callsign:        ac.Callsign,
	}, nil)
}

func (sim *Sim) RunAircraftCommands(ac *Aircraft, cmds string) ([]string, error) {
	commands := strings.Fields(cmds)
	for i, command := range commands {
		switch command[0] {
		case 'D':
			if components := strings.Split(command, "/"); len(components) > 1 {
				// Depart <fix> at heading <hdg>
				fix := components[0][1:]

				if components[1][0] != 'H' {
					return commands[i:], ErrInvalidCommandSyntax
				}
				if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
					return commands[i:], err
				} else if err := sim.DepartFixHeading(ac, fix, hdg); err != nil {
					return commands[i:], err
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := sim.AssignAltitude(ac, 100*alt); err != nil {
					return commands[i:], err
				}
			} else if _, ok := sim.Locate(string(command[1:])); ok {
				if err := sim.DirectFix(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				return commands[i:], ErrInvalidCommandSyntax
			}

		case 'H':
			if len(command) == 1 {
				if err := sim.FlyPresentHeading(ac); err != nil {
					return commands[i:], err
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				return commands[i:], err
			} else if err := sim.AssignHeading(ac, hdg, TurnClosest); err != nil {
				return commands[i:], err
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return commands[i:], err
				} else if err := sim.TurnLeft(ac, deg); err != nil {
					return commands[i:], err
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := sim.AssignHeading(ac, hdg, TurnLeft); err != nil {
					return commands[i:], err
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return commands[i:], err
				} else if err := sim.TurnRight(ac, deg); err != nil {
					return commands[i:], err
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := sim.AssignHeading(ac, hdg, TurnRight); err != nil {
					return commands[i:], err
				}
			}

		case 'C', 'A':
			if len(command) > 4 && command[:3] == "CSI" && !isAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := sim.ClearedStraightInApproach(ac, command[3:]); err != nil {
					return commands[i:], err
				}
			} else if command[0] == 'C' && len(command) > 2 && !isAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
					// Cross fix [at altitude] [at speed]
					fix := components[0][1:]
					alt, speed := 0, 0

					for _, cmd := range components[1:] {
						if len(cmd) == 0 {
							return commands[i:], ErrInvalidCommandSyntax
						}

						var err error
						if cmd[0] == 'A' {
							if alt, err = strconv.Atoi(cmd[1:]); err != nil {
								return commands[i:], err
							}
						} else if cmd[0] == 'S' {
							if speed, err = strconv.Atoi(cmd[1:]); err != nil {
								return commands[i:], err
							}
						} else {
							return commands[i:], ErrInvalidCommandSyntax
						}
					}

					if err := sim.CrossFixAt(ac, fix, 100*alt, speed); err != nil {
						return commands[i:], err
					}
				} else if err := sim.ClearedApproach(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := sim.AssignAltitude(ac, 100*alt); err != nil {
					return commands[i:], err
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := sim.AssignSpeed(ac, 0); err != nil {
					return commands[i:], err
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := sim.AssignSpeed(ac, kts); err != nil {
					return commands[i:], err
				}
			}

		case 'E':
			// Expect approach.
			if len(command) > 1 {
				if err := sim.ExpectApproach(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				return commands[i:], ErrInvalidCommandSyntax
			}

		case '?':
			if err := sim.PrintInfo(ac); err != nil {
				return commands[i:], err
			}

		case 'X':
			if err := sim.DeleteAircraft(ac); err != nil {
				return commands[i:], err
			}

		default:
			return commands[i:], ErrInvalidCommandSyntax
		}
	}
	return nil, nil
}

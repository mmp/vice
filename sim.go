// sim.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

func init() {
	gob.Register(&FlyHeading{})
	gob.Register(&FlyRoute{})
	gob.Register(&FlyRacetrackPT{})
	gob.Register(&FlyStandard45PT{})

	gob.Register(&MaintainSpeed{})
	gob.Register(&FinalApproachSpeed{})

	gob.Register(&MaintainAltitude{})
	gob.Register(&FlyRacetrackPT{})

	gob.Register(&SpeedAfterAltitude{})
	gob.Register(&AltitudeAfterSpeed{})
	gob.Register(&ApproachSpeedAt5DME{})
	gob.Register(&ClimbOnceAirborne{})
	gob.Register(&TurnToInterceptLocalizer{})
	gob.Register(&HoldLocalizerAfterIntercept{})
	gob.Register(&GoAround{})
}

var (
	ErrControllerAlreadySignedIn = errors.New("controller with that callsign already signed in")
	ErrInvalidControllerToken    = errors.New("invalid controller token")
)

type NewSimConfiguration struct {
	DepartureChallenge float32
	GoAroundRate       float32
	Scenario           string
	ScenarioGroup      string
	Callsign           string

	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]int
}

func MakeSimConfiguration() NewSimConfiguration {
	c := NewSimConfiguration{
		DepartureChallenge: 0.25,
		GoAroundRate:       0.10,
	}

	// Use the last scenario, if available.
	c.SetScenarioGroup(globalConfig.LastScenarioGroup)

	return c
}

func (c *NewSimConfiguration) SetScenarioGroup(name string) {
	sg, ok := scenarioGroups[name]
	if !ok {
		// Take the first one alphabetically if unavailable
		name = SortedMapKeys(scenarioGroups)[0]
		sg = scenarioGroups[name]
	}

	c.ScenarioGroup = name
	c.Callsign = sg.ControlPositions[sg.DefaultController].Callsign
	c.SetScenario(sg.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(name string) {
	sg, ok := scenarioGroups[c.ScenarioGroup]
	if !ok {
		lg.Errorf("%s: unknown scenario group?", c.ScenarioGroup)
		return
	}
	scenario, ok := sg.Scenarios[name]
	if !ok {
		lg.Errorf("%s: called SetScenario with an unknown scenario name???", name)
		return
	}
	c.Scenario = name

	c.ArrivalGroupRates = DuplicateMap(scenario.ArrivalGroupDefaultRates)

	c.DepartureRates = make(map[string]map[string]map[string]int)
	for _, rwy := range scenario.DepartureRunways {
		if _, ok := c.DepartureRates[rwy.Airport]; !ok {
			c.DepartureRates[rwy.Airport] = make(map[string]map[string]int)
		}
		if _, ok := c.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
			c.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]int)
		}
		c.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate
	}
}

func (c *NewSimConfiguration) DrawUI() bool {
	sg := scenarioGroups[c.ScenarioGroup]
	scenario := sg.Scenarios[c.Scenario]
	controller := sg.ControlPositions[c.Callsign]

	if imgui.BeginComboV("Scenario Group", c.ScenarioGroup, imgui.ComboFlagsHeightLarge) {
		for _, name := range SortedMapKeys(scenarioGroups) {
			if imgui.SelectableV(name, name == c.ScenarioGroup, 0, imgui.Vec2{}) {
				c.SetScenarioGroup(name)
			}
		}
		imgui.EndCombo()
	}

	if imgui.BeginComboV("Control Position", controller.Callsign, imgui.ComboFlagsHeightLarge) {
		positions := make(map[string]*Controller)
		for _, sc := range sg.Scenarios {
			positions[sc.Callsign] = sg.ControlPositions[sc.Callsign]
		}

		for _, controllerName := range SortedMapKeys(positions) {
			if imgui.SelectableV(controllerName, controllerName == c.Callsign, 0, imgui.Vec2{}) {
				c.Callsign = controllerName
				// Set the current scenario to the first one alphabetically
				// with the selected controller.
				for _, scenarioName := range SortedMapKeys(sg.Scenarios) {
					if sg.Scenarios[scenarioName].Callsign == controllerName {
						c.SetScenario(scenarioName)
						break
					}
				}
			}
		}
		imgui.EndCombo()
	}

	if imgui.BeginComboV("Config", scenario.Name(), imgui.ComboFlagsHeightLarge) {
		for _, name := range SortedMapKeys(sg.Scenarios) {
			if sg.Scenarios[name].Callsign != c.Callsign {
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

		if len(c.DepartureRates) > 0 {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Departing:")
			imgui.TableNextColumn()

			var runways []string
			for airport, runwayRates := range c.DepartureRates {
				for runway, categoryRates := range runwayRates {
					for _, rate := range categoryRates {
						if rate > 0 {
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
		for _, runwayRates := range c.DepartureRates {
			for _, categoryRates := range runwayRates {
				for _, rate := range categoryRates {
					sumRates += rate
				}
			}
		}
		imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", sumRates))

		imgui.SliderFloatV("Sequencing challenge", &c.DepartureChallenge, 0, 1, "%.02f", 0)
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

		if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
			imgui.TableHeadersRow()

			for _, airport := range SortedMapKeys(c.DepartureRates) {
				imgui.PushID(airport)
				for _, runway := range SortedMapKeys(c.DepartureRates[airport]) {
					imgui.PushID(runway)
					for _, category := range SortedMapKeys(c.DepartureRates[airport][runway]) {
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

						r := int32(c.DepartureRates[airport][runway][category])
						imgui.InputIntV("##adr", &r, 0, 120, 0)
						c.DepartureRates[airport][runway][category] = int(r)

						imgui.PopID()
					}
					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	if len(c.ArrivalGroupRates) > 0 {
		// Figure out how many unique airports we've got for AAR columns in the table
		// and also sum up the overall arrival rate
		allAirports := make(map[string]interface{})
		sumRates := 0
		for _, agr := range c.ArrivalGroupRates {
			for ap, rate := range agr {
				allAirports[ap] = nil
				sumRates += rate
			}
		}
		nAirports := len(allAirports)

		imgui.Separator()
		imgui.Text("Arrivals")
		imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", sumRates))
		imgui.SliderFloatV("Go around probability", &c.GoAroundRate, 0, 1, "%.02f", 0)

		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("arrivalgroups", 1+nAirports, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Arrival")
			sortedAirports := SortedMapKeys(allAirports)
			for _, ap := range sortedAirports {
				imgui.TableSetupColumn(ap + " AAR")
			}
			imgui.TableHeadersRow()

			for _, group := range SortedMapKeys(c.ArrivalGroupRates) {
				imgui.PushID(group)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(group)
				for _, ap := range sortedAirports {
					imgui.TableNextColumn()
					if rate, ok := c.ArrivalGroupRates[group][ap]; ok {
						r := int32(rate)
						imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0)
						c.ArrivalGroupRates[group][ap] = int(r)
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
	client, err := DialSimServer()
	if err != nil {
		return err
	}

	var result NewSimResult
	err = client.Call("SimFactory.New", c, &result)
	if err != nil {
		return err
	}

	result.World.simProxy = &SimProxy{
		Token:  result.Token,
		Client: client,
	}

	globalConfig.LastScenarioGroup = c.ScenarioGroup

	if sg := scenarioGroups[c.ScenarioGroup]; sg == nil {
		return fmt.Errorf("%s: unknown scenario group", c.ScenarioGroup)
	} else {
		err := sg.InitializeWorld(result.World)
		if err != nil {
			return err
		}
	}

	newWorldChan <- result.World

	return nil
}

///////////////////////////////////////////////////////////////////////////

type SimProxy struct {
	Token  string
	Client *rpc.Client
}

func (s *SimProxy) TogglePause() *rpc.Call {
	return s.Client.Go("Sim.TogglePause", s.Token, nil, nil)
}

func (s *SimProxy) SignOff(_, _ *struct{}) error {
	return s.Client.Call("Sim.SignOff", s.Token, nil)
}

func (s *SimProxy) GetWorldUpdate(wu *SimWorldUpdate) *rpc.Call {
	return s.Client.Go("Sim.GetWorldUpdate", s.Token, wu, nil)
}

func (s *SimProxy) SetSimRate(r float32) *rpc.Call {
	return s.Client.Go("Sim.SetSimRate",
		&SimRateSpecifier{
			ControllerToken: s.Token,
			Rate:            r,
		}, nil, nil)
}

func (s *SimProxy) SetScratchpad(callsign string, scratchpad string) *rpc.Call {
	return s.Client.Go("Sim.SetScratchpad", &AircraftPropertiesSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil, nil)
}

func (s *SimProxy) InitiateTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.InitiateTrack", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) DropTrack(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DropTrack", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) HandoffTrack(callsign string, controller string) *rpc.Call {
	return s.Client.Go("Sim.HandoffTrack", &HandoffSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
		Controller:      controller,
	}, nil, nil)
}

func (s *SimProxy) HandoffControl(callsign string) *rpc.Call {
	return s.Client.Go("Sim.HandoffControl", &HandoffSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) AcceptHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.AcceptHandoff", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) CancelHandoff(callsign string) *rpc.Call {
	return s.Client.Go("Sim.CancelHandoff", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) AssignAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetAltitude", &AltitudeAssignment{
		ControllerToken: s.Token,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) SetTemporaryAltitude(callsign string, alt int) *rpc.Call {
	return s.Client.Go("Sim.SetTemporaryAltitude", &AltitudeAssignment{
		ControllerToken: s.Token,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil, nil)
}

func (s *SimProxy) GoAround(callsign string) *rpc.Call {
	return s.Client.Go("Sim.GoAround", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

func (s *SimProxy) DeleteAircraft(callsign string) *rpc.Call {
	return s.Client.Go("Sim.DeleteAircraft", &AircraftSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
	}, nil, nil)
}

type AircraftCommandsSpecifier struct {
	ControllerToken string
	Callsign        string
	Commands        string
}

func (s *SimProxy) RunAircraftCommands(callsign string, cmds string, w *World) *rpc.Call {
	return s.Client.Go("sim.RunAircraftCommands", &AircraftCommandsSpecifier{
		ControllerToken: s.Token,
		Callsign:        callsign,
		Commands:        cmds,
	}, nil, nil)
}

///////////////////////////////////////////////////////////////////////////

type SimFactory struct{}

var activeSims map[int]*Sim
var controllerTokenToSim map[string]*Sim

type NewSimResult struct {
	World    *World
	Token    string
	SimIndex int
}

func (*SimFactory) New(config *NewSimConfiguration, result *NewSimResult) error {
	lg.Printf("New %+v", *config)

	if activeSims == nil {
		activeSims = make(map[int]*Sim)
	}
	if controllerTokenToSim == nil {
		controllerTokenToSim = make(map[string]*Sim)
	}

	sim := NewSim(*config)
	simIndex := len(activeSims)
	activeSims[simIndex] = sim

	sim.prespawn()

	world, token, err := sim.SignOn(config.Callsign)
	if err != nil {
		return err
	}
	controllerTokenToSim[token] = sim

	go func() {
		for {
			sim.Update()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	*result = NewSimResult{
		World:    world,
		Token:    token,
		SimIndex: simIndex,
	}

	return nil
}

type SimDispatcher struct{}

func (*SimDispatcher) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	if sim, ok := controllerTokenToSim[token]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.GetWorldUpdate(token, update)
	}
}

func (*SimDispatcher) SetSimRate(r *SimRateSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[r.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.SetSimRate(r, nil)
	}
}

func (*SimDispatcher) SetScratchpad(a *AircraftPropertiesSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.SetScratchpad(a, nil)
	}
}

func (*SimDispatcher) InitiateTrack(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.InitiateTrack(a, nil)
	}
}

func (*SimDispatcher) DropTrack(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.DropTrack(a, nil)
	}
}

func (*SimDispatcher) HandoffTrack(h *HandoffSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[h.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.HandoffTrack(h, nil)
	}
}

func (*SimDispatcher) HandoffControl(h *HandoffSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[h.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.HandoffControl(h, nil)
	}
}

func (*SimDispatcher) AcceptHandoff(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.AcceptHandoff(a, nil)
	}
}

func (*SimDispatcher) CancelHandoff(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.CancelHandoff(a, nil)
	}
}

func (*SimDispatcher) AssignAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[alt.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.AssignAltitude(alt, nil)
	}
}

func (*SimDispatcher) SetTemporaryAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[alt.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.SetTemporaryAltitude(alt, nil)
	}
}

func (*SimDispatcher) AssignHeading(hdg *HeadingAssignment, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[hdg.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.AssignHeading(hdg, nil)
	}
}

func (*SimDispatcher) AssignSpeed(sa *SpeedAssignment, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[sa.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.AssignSpeed(sa, nil)
	}
}

func (*SimDispatcher) DirectFix(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[f.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.DirectFix(f, nil)
	}
}

func (*SimDispatcher) DepartFixHeading(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[f.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.DepartFixHeading(f, nil)
	}
}

func (*SimDispatcher) CrossFixAt(f *FixSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[f.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.CrossFixAt(f, nil)
	}
}

func (*SimDispatcher) ExpectApproach(a *ApproachAssignment, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.ExpectApproach(a, nil)
	}
}

func (*SimDispatcher) ClearedApproach(c *ApproachClearance, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[c.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.ClearedApproach(c, nil)
	}
}

func (*SimDispatcher) GoAround(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.GoAround(a, nil)
	}
}

func (*SimDispatcher) DeleteAircraft(a *AircraftSpecifier, _ *struct{}) error {
	if sim, ok := controllerTokenToSim[a.ControllerToken]; !ok {
		return errors.New("No Sim running for this controller")
	} else {
		return sim.DeleteAircraft(a, nil)
	}
}

type AircraftCommandsError struct {
	error
	Remaining []string
}

func (e AircraftCommandsError) Error() string {
	s := e.error.Error()
	if len(e.Remaining) > 0 {
		s += " remaining: " + strings.Join(e.Remaining, " ")
	}
	return s
}

func (*SimDispatcher) RunAircraftCommands(cmds *AircraftCommandsSpecifier, _ *struct{}) error {
	sim, ok := controllerTokenToSim[cmds.ControllerToken]
	if !ok {
		return errors.New("No Sim running for this controller")
	}

	commands := strings.Fields(cmds.Commands)

	for i, command := range commands {
		wrapError := func(e error) error {
			return &AircraftCommandsError{
				error:     e,
				Remaining: commands[i:],
			}
		}

		switch command[0] {
		case 'D':
			if components := strings.Split(command, "/"); len(components) > 1 {
				// Depart <fix> at heading <hdg>
				fix := components[0][1:]

				if components[1][0] != 'H' {
					return wrapError(ErrInvalidCommandSyntax)
				}
				if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
					return wrapError(err)
				} else if err := sim.DepartFixHeading(&FixSpecifier{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Fix:             fix,
					Heading:         hdg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignAltitude(&AltitudeAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Altitude:        100 * alt,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if _, ok := sim.World.Locate(string(command[1:])); ok {
				if err := sim.DirectFix(&FixSpecifier{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Fix:             command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				return wrapError(ErrInvalidCommandSyntax)
			}

		case 'H':
			if len(command) == 1 {
				if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Present:         true,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				return wrapError(err)
			} else if err := sim.AssignHeading(&HeadingAssignment{
				ControllerToken: cmds.ControllerToken,
				Callsign:        cmds.Callsign,
				Heading:         hdg,
				Turn:            TurnClosest,
			}, nil); err != nil {
				return wrapError(err)
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					LeftDegrees:     deg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Heading:         hdg,
					Turn:            TurnLeft,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					RightDegrees:    deg,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignHeading(&HeadingAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Heading:         hdg,
					Turn:            TurnRight,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'C', 'A':
			if len(command) > 4 && command[:3] == "CSI" && !isAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := sim.ClearedApproach(&ApproachClearance{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[3:],
					StraightIn:      true,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else if command[0] == 'C' && len(command) > 2 && !isAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
					// Cross fix [at altitude] [at speed]
					fix := components[0][1:]
					alt, speed := 0, 0

					for _, cmd := range components[1:] {
						if len(cmd) == 0 {
							return wrapError(ErrInvalidCommandSyntax)
						}

						var err error
						if cmd[0] == 'A' {
							if alt, err = strconv.Atoi(cmd[1:]); err != nil {
								return wrapError(err)
							}
						} else if cmd[0] == 'S' {
							if speed, err = strconv.Atoi(cmd[1:]); err != nil {
								return wrapError(err)
							}
						} else {
							return wrapError(ErrInvalidCommandSyntax)
						}
					}

					if err := sim.CrossFixAt(&FixSpecifier{
						ControllerToken: cmds.ControllerToken,
						Callsign:        cmds.Callsign,
						Fix:             fix,
						Altitude:        100 * alt,
						Speed:           speed,
					}, nil); err != nil {
						return wrapError(err)
					}
				} else if err := sim.ClearedApproach(&ApproachClearance{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignAltitude(&AltitudeAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Altitude:        100 * alt,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := sim.AssignSpeed(&SpeedAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Speed:           0,
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					return wrapError(err)
				} else if err := sim.AssignSpeed(&SpeedAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Speed:           kts,
				}, nil); err != nil {
					return wrapError(err)
				}
			}

		case 'E':
			// Expect approach.
			if len(command) > 1 {
				if err := sim.ExpectApproach(&ApproachAssignment{
					ControllerToken: cmds.ControllerToken,
					Callsign:        cmds.Callsign,
					Approach:        command[1:],
				}, nil); err != nil {
					return wrapError(err)
				}
			} else {
				return wrapError(ErrInvalidCommandSyntax)
			}

		case '?':
			if ac, ok := sim.World.Aircraft[cmds.Callsign]; !ok {
				return wrapError(ErrNoAircraftForCallsign)
			} else if err := sim.World.PrintInfo(ac); err != nil {
				return wrapError(err)
			}

		case 'X':
			if _, ok := sim.World.Aircraft[cmds.Callsign]; !ok {
				return wrapError(ErrNoAircraftForCallsign)
			} else if err := sim.DeleteAircraft(&AircraftSpecifier{
				ControllerToken: cmds.ControllerToken,
				Callsign:        cmds.Callsign,
			}, nil); err != nil {
				return wrapError(err)
			}

		default:
			return wrapError(ErrInvalidCommandSyntax)
		}
	}
	return nil
}

func RunSimServer() {
	rpc.Register(&SimFactory{})
	rpc.RegisterName("Sim", &SimDispatcher{})

	rpc.HandleHTTP()
	l, err := net.Listen("tcp", ":6502")
	if err != nil {
		lg.Errorf("tcp listen: %v", err)
	} else {
		lg.Printf("Listening on %+v", l)
		/*go*/ http.Serve(l, nil)
	}

}

func DialSimServer() (*rpc.Client, error) {
	return rpc.DialHTTP("tcp", "localhost:6502")
}

///////////////////////////////////////////////////////////////////////////

type Sim struct {
	ScenarioGroup string
	Scenario      string

	World       *World
	controllers map[string]*ServerController // from token

	eventStream *EventStream

	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]int

	// The same runway may be present multiple times in DepartureRates,
	// with different categories. However, we want to make sure that we
	// don't spawn two aircraft on the same runway at the same time (or
	// close to it).  Therefore, here we track a per-runway "when's the
	// next time that we will spawn *something* from the runway" time.
	// When the time is up, we'll figure out which specific category to
	// use...
	// airport -> runway -> time
	NextDepartureSpawn map[string]map[string]time.Time

	// Key is arrival group name
	NextArrivalSpawn map[string]time.Time

	Handoffs map[string]time.Time

	DepartureChallenge float32
	GoAroundRate       float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	CurrentTime    time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	SimRate        float32
	Paused         bool
}

type ServerController struct {
	Callsign string
	events   *EventsSubscription
}

func NewSim(ssc NewSimConfiguration) *Sim {
	rand.Seed(time.Now().UnixNano())

	s := &Sim{
		ScenarioGroup: ssc.ScenarioGroup,
		Scenario:      ssc.Scenario,

		controllers: make(map[string]*ServerController),

		eventStream: NewEventStream(),

		DepartureRates:    DuplicateMap(ssc.DepartureRates),
		ArrivalGroupRates: DuplicateMap(ssc.ArrivalGroupRates),

		CurrentTime:    time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:            1,
		DepartureChallenge: ssc.DepartureChallenge,
		GoAroundRate:       ssc.GoAroundRate,
		Handoffs:           make(map[string]time.Time),
	}

	s.World = newWorld(ssc, s)

	s.setInitialSpawnTimes()

	return s
}

func newWorld(ssc NewSimConfiguration, s *Sim) *World {
	sg, ok := scenarioGroups[ssc.ScenarioGroup]
	if !ok {
		lg.Errorf("%s: unknown scenario group", ssc.ScenarioGroup)
		return nil
	}
	sc, ok := sg.Scenarios[ssc.Scenario]
	if !ok {
		lg.Errorf("%s: unknown scenario", ssc.Scenario)
		return nil
	}

	// Set some related globals..

	w := NewWorld()
	w.Callsign = "__SERVER__"
	w.MagneticVariation = sg.MagneticVariation
	w.NmPerLatitude = sg.NmPerLatitude
	w.NmPerLongitude = sg.NmPerLongitude
	w.Wind = sc.Wind
	w.Airports = sg.Airports
	w.Fixes = sg.Fixes
	w.PrimaryAirport = sg.PrimaryAirport
	w.RadarSites = sg.RadarSites
	w.Center = sg.Center
	w.Range = sg.Range
	w.DefaultMap = sc.DefaultMap
	w.STARSMaps = sg.STARSMaps
	w.Scratchpads = sg.Scratchpads
	w.ArrivalGroups = sg.ArrivalGroups
	w.ApproachAirspace = sc.ApproachAirspace
	w.DepartureAirspace = sc.DepartureAirspace
	w.DepartureRunways = sc.DepartureRunways

	// Extract just the active controllers
	for callsign, ctrl := range sg.ControlPositions {
		if Find(sc.Controllers, callsign) != -1 {
			w.Controllers[callsign] = ctrl
		}
	}

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)
	fakeMETAR := func(icao string) {
		spd := w.Wind.Speed - 3 + rand.Int31n(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((w.Wind.Direction + 5) / 10)
			dir += [3]int32{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := w.Wind.Gust - 3 + rand.Int31n(6)
			if gst-w.Wind.Speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		w.METAR[icao] = &METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

	w.DepartureAirports = make(map[string]*Airport)
	for name, runwayRates := range s.DepartureRates {
		for _, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				if rate > 0 {
					w.DepartureAirports[name] = w.GetAirport(name)
				}
			}
		}
	}
	w.ArrivalAirports = make(map[string]*Airport)
	for _, airportRates := range s.ArrivalGroupRates {
		for name, rate := range airportRates {
			if rate > 0 {
				w.ArrivalAirports[name] = w.GetAirport(name)
			}
		}
	}

	for ap := range w.DepartureAirports {
		fakeMETAR(ap)
	}
	for ap := range w.ArrivalAirports {
		fakeMETAR(ap)
	}

	return w
}

func (s *Sim) SignOn(callsign string) (*World, string, error) {
	for _, ctrl := range s.controllers {
		if ctrl.Callsign == callsign {
			return nil, "", ErrControllerAlreadySignedIn
		}
	}

	w := NewWorld()
	w.Assign(s.World)
	w.Callsign = callsign

	MagneticVariation = w.MagneticVariation
	NmPerLatitude = w.NmPerLatitude
	NmPerLongitude = w.NmPerLongitude

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}

	token := base64.StdEncoding.EncodeToString(buf[:])
	s.controllers[token] = &ServerController{
		Callsign: callsign,
		events:   s.eventStream.Subscribe(),
	}

	return w, token, nil
}

func (s *Sim) SignOff(token string, _ *struct{}) error {
	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		ctrl.events.Unsubscribe()
		delete(s.controllers, token)
	}
	return nil
}

func (s *Sim) TogglePause(token string, _ *struct{}) error {
	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.Paused = !s.Paused
		s.lastUpdateTime = time.Now() // ignore time passage...
		return nil
	}
}

func (s *Sim) PostEvent(e Event) {
	s.eventStream.Post(e)
}

type SimWorldUpdate struct {
	Aircraft       map[string]*Aircraft
	Controllers    map[string]*Controller
	Time           time.Time
	SimIsPaused    bool
	SimRate        float32
	SimDescription string
	Events         []Event
}

func (s *Sim) GetWorldUpdate(token string, update *SimWorldUpdate) error {
	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		*update = SimWorldUpdate{
			Aircraft:       s.World.Aircraft,
			Controllers:    s.World.Controllers,
			Time:           s.CurrentTime,
			SimIsPaused:    s.Paused,
			SimRate:        s.SimRate,
			SimDescription: s.Scenario,
			Events:         ctrl.events.Get(),
		}
		return nil
	}
}

func (s *Sim) Activate() error {
	var e ErrorLogger

	s.controllers = make(map[string]*ServerController)
	s.eventStream = NewEventStream()

	initializeWaypointLocations := func(waypoints []Waypoint, e *ErrorLogger) {
		for i, wp := range waypoints {
			if e != nil {
				e.Push("Fix " + wp.Fix)
			}
			if pos, ok := s.World.Locate(wp.Fix); !ok {
				if e != nil {
					e.ErrorString("unable to locate waypoint")
				}
			} else {
				waypoints[i].Location = pos
			}
			if e != nil {
				e.Pop()
			}
		}
	}

	// A number of time.Time values are included in the serialized World.
	// updateTime is a helper function that rewrites them to be in terms of
	// the current time, using the serializion time as a baseline.
	now := time.Now()
	serializeTime := s.CurrentTime
	updateTime := func(t time.Time) time.Time {
		return now.Add(t.Sub(serializeTime))
	}

	s.CurrentTime = now
	s.lastUpdateTime = now

	for _, ac := range s.World.Aircraft {
		e.Push(ac.Callsign)

		// Rewrite the radar track times to be w.r.t now
		for i := range ac.Tracks {
			ac.Tracks[i].Time = updateTime(ac.Tracks[i].Time)
		}

		if ap := ac.Approach; ap != nil {
			for i := range ap.Waypoints {
				initializeWaypointLocations(ap.Waypoints[i], &e)
			}
		}

		for rwy, wp := range ac.ArrivalRunwayWaypoints {
			e.Push("Arrival runway " + rwy)
			initializeWaypointLocations(wp, &e)
			e.Pop()
		}

		e.Pop()
	}

	for callsign := range s.World.Controllers {
		s.World.Controllers[callsign].Callsign = callsign
	}

	if sg := scenarioGroups[s.ScenarioGroup]; sg == nil {
		e.ErrorString(s.ScenarioGroup + ": unknown scenario group")
	} else {
		err := sg.InitializeWorld(s.World)
		if err != nil {
			e.Error(err)
		}
	}

	for i, rwy := range s.World.DepartureRunways {
		s.World.DepartureRunways[i].lastDeparture = nil
		for _, route := range rwy.ExitRoutes {
			initializeWaypointLocations(route.Waypoints, &e)
		}
	}

	for _, arrivals := range s.World.ArrivalGroups {
		for _, arr := range arrivals {
			initializeWaypointLocations(arr.Waypoints, &e)
			for _, rwp := range arr.RunwayWaypoints {
				initializeWaypointLocations(rwp, &e)
			}
		}
	}

	for ho, t := range s.Handoffs {
		s.Handoffs[ho] = updateTime(t)
	}

	for group, t := range s.NextArrivalSpawn {
		s.NextArrivalSpawn[group] = updateTime(t)
	}

	for airport, runwayTimes := range s.NextDepartureSpawn {
		for runway, t := range runwayTimes {
			s.NextDepartureSpawn[airport][runway] = updateTime(t)
		}
	}

	if e.HaveErrors() {
		e.PrintErrors()
		return errors.New("Errors during state restoration")
	}
	return nil

}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	if s.Paused {
		return
	}

	// Update the current time
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.SimRate * float32(elapsed))
	s.CurrentTime = s.CurrentTime.Add(elapsed)
	s.lastUpdateTime = time.Now()

	s.updateState()
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.CurrentTime
	for callsign, t := range s.Handoffs {
		if now.After(t) {
			if ac, ok := s.World.Aircraft[callsign]; ok {
				s.eventStream.Post(Event{
					Type:           AcceptedHandoffEvent,
					FromController: ac.TrackingController,
					ToController:   ac.OutboundHandoffController,
					Callsign:       ac.Callsign,
				})

				ac.TrackingController = ac.OutboundHandoffController
				ac.OutboundHandoffController = ""
			}
			delete(s.Handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now
		for _, ac := range s.World.Aircraft {
			ac.Update(s.World, s.World, s)

			if ac.InboundHandoffController == s.World.Callsign {
				// We hit a /ho at a fix; update to the correct controller.
				sg := scenarioGroups[s.ScenarioGroup]
				ac.InboundHandoffController = sg.ControlPositions[sg.DefaultController].Callsign
				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.ControllingController,
					ToController:   ac.InboundHandoffController,
				})
			}
		}
	}

	// Add a new radar track every 5 seconds.  While we're at it, cull
	// departures that are far from the airport.
	if now.Sub(s.lastTrackUpdate) >= 5*time.Second {
		s.lastTrackUpdate = now

		for callsign, ac := range s.World.Aircraft {
			if ap := s.World.GetAirport(ac.FlightPlan.DepartureAirport); ap != nil && ac.IsDeparture {
				if nmdistance2ll(ac.Position, ap.Location) > 200 {
					delete(s.World.Aircraft, callsign)
					continue
				}
			}

			ac.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - MagneticVariation,
				Time:        now,
			})
		}
	}

	s.spawnAircraft()
}

func (s *Sim) prespawn() {
	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		s.CurrentTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.CurrentTime = time.Now()
	s.lastUpdateTime = time.Now()
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func (s *Sim) setInitialSpawnTimes() {
	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	randomSpawn := func(rate int) time.Time {
		if rate == 0 {
			return time.Now().Add(365 * 24 * time.Hour)
		}
		avgWait := 3600 / rate
		delta := rand.Intn(avgWait) - avgWait/2 - initialSimSeconds
		return time.Now().Add(time.Duration(delta) * time.Second)
	}

	s.NextArrivalSpawn = make(map[string]time.Time)
	for group, rates := range s.ArrivalGroupRates {
		rateSum := 0
		for _, rate := range rates {
			rateSum += rate
		}
		s.NextArrivalSpawn[group] = randomSpawn(rateSum)
	}

	s.NextDepartureSpawn = make(map[string]map[string]time.Time)
	for airport, runwayRates := range s.DepartureRates {
		spawn := make(map[string]time.Time)

		for runway, categoryRates := range runwayRates {
			rateSum := 0
			for _, rate := range categoryRates {
				rateSum += rate
			}
			if rateSum > 0 {
				spawn[runway] = randomSpawn(rateSum)
			}
		}

		if len(spawn) > 0 {
			s.NextDepartureSpawn[airport] = spawn
		}
	}
}

func sampleRateMap(rates map[string]int) (string, int) {
	// Choose randomly in proportion to the rates in the map
	rateSum := 0
	var result string
	for item, rate := range rates {
		if rate == 0 {
			continue
		}
		rateSum += rate
		// Weighted reservoir sampling...
		if rand.Float32() < float32(rate)/float32(rateSum) {
			result = item
		}
	}
	return result, rateSum
}

func (s *Sim) spawnAircraft() {
	now := s.CurrentTime

	addAircraft := func(ac *Aircraft) {
		if _, ok := s.World.Aircraft[ac.Callsign]; ok {
			lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
			return
		}
		s.World.Aircraft[ac.Callsign] = ac

		ac.RunWaypointCommands(ac.Waypoints[0], s.World, s)

		ac.Position = ac.Waypoints[0].Location
		if ac.Position.IsZero() {
			lg.Errorf("%s: uninitialized initial waypoint position! %+v", ac.Callsign, ac.Waypoints[0])
			return
		}

		ac.Heading = float32(ac.Waypoints[0].Heading)
		if ac.Heading == 0 { // unassigned, so get the heading from the next fix
			ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, MagneticVariation)
		}
		ac.Waypoints = FilterSlice(ac.Waypoints[1:], func(wp Waypoint) bool { return !wp.Location.IsZero() })
	}

	randomWait := func(rate int) time.Duration {
		if rate == 0 {
			return 365 * 24 * time.Hour
		}
		avgSeconds := 3600 / float32(rate)
		seconds := lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
		return time.Duration(seconds * float32(time.Second))
	}

	for group, airportRates := range s.ArrivalGroupRates {
		if now.After(s.NextArrivalSpawn[group]) {
			arrivalAirport, rateSum := sampleRateMap(airportRates)

			if ac := s.SpawnArrival(arrivalAirport, group); ac != nil {
				ac.FlightPlan.ArrivalAirport = arrivalAirport
				addAircraft(ac)
				s.NextArrivalSpawn[group] = now.Add(randomWait(rateSum))
			}
		}
	}

	for airport, runwayTimes := range s.NextDepartureSpawn {
		for runway, spawnTime := range runwayTimes {
			if !now.After(spawnTime) {
				continue
			}

			// Figure out which category to launch
			category, rateSum := sampleRateMap(s.DepartureRates[airport][runway])
			if rateSum == 0 {
				lg.Errorf("%s/%s: couldn't find a matching runway for spawning departure?", airport, runway)
				continue
			}

			ap := s.World.GetAirport(airport)
			idx := FindIf(s.World.DepartureRunways,
				func(r ScenarioGroupDepartureRunway) bool {
					return r.Airport == airport && r.Runway == runway && r.Category == category
				})
			if idx == -1 {
				lg.Errorf("%s/%s/%s: couldn't find airport/runway/category for spawning departure. rates %s dep runways %s",
					airport, runway, category, spew.Sdump(s.DepartureRates[airport][runway]), spew.Sdump(s.World.DepartureRunways))
				continue
			}

			if ac := s.SpawnDeparture(ap, &s.World.DepartureRunways[idx]); ac != nil {
				ac.FlightPlan.DepartureAirport = airport
				var ok bool
				if ac.FlightPlan.DepartureAirportLocation, ok = s.World.Locate(ac.FlightPlan.DepartureAirport); !ok {
					lg.Errorf("%s: unable to find departure airport %s location?", ac.Callsign, ac.FlightPlan.DepartureAirport)
				} else {
					addAircraft(ac)
					s.NextDepartureSpawn[airport][runway] = now.Add(randomWait(rateSum))
				}
			}
		}
	}
}

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
}

func sampleAircraft(icao, fleet string) *Aircraft {
	al, ok := database.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fl {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := database.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from fleet %+v, airline %s",
			aircraft, fleet, icao)
		return nil
	}

	// random callsign
	callsign := strings.ToUpper(icao)
	for {
		format := "####"
		if len(al.Callsign.CallsignFormats) > 0 {
			format = Sample(al.Callsign.CallsignFormats)
		}
		for {
			id := ""
			for _, ch := range format {
				switch ch {
				case '#':
					id += fmt.Sprintf("%d", rand.Intn(10))
				case '@':
					id += string(rune('A' + rand.Intn(26)))
				}
			}
			if id != "0" {
				callsign += id
				break
			}
		}
		// Only break and accept the callsign if it's not a bad one..
		if _, found := badCallsigns[callsign]; !found {
			break
		}
	}

	squawk := Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &Aircraft{
		Callsign:       callsign,
		AssignedSquawk: squawk,
		Squawk:         squawk,
		Mode:           Charlie,
		FlightPlan: &FlightPlan{
			Rules:        IFR,
			AircraftType: acType,
		},

		Performance: perf,
	}
}

func (s *Sim) SpawnArrival(airportName string, arrivalGroup string) *Aircraft {
	arrivals := s.World.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := SampleFiltered(arrivals, func(ar Arrival) bool {
		_, ok := ar.Airlines[airportName]
		return ok
	})
	if idx == -1 {
		lg.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, airportName)
		return nil
	}
	arr := arrivals[idx]

	airline := Sample(arr.Airlines[airportName])
	ac := sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil
	}

	ac.FlightPlan.DepartureAirport = airline.Airport
	var ok bool
	if ac.FlightPlan.DepartureAirportLocation, ok = s.World.Locate(ac.FlightPlan.DepartureAirport); !ok {
		lg.Errorf("%s: unable to find departure airport %s location?", ac.Callsign, ac.FlightPlan.DepartureAirport)
		// This is fine; it's probably international and we shouldn't need the departure location for an arrival...
		//return nil
	}

	ac.FlightPlan.ArrivalAirport = airportName
	if ac.FlightPlan.ArrivalAirportLocation, ok = s.World.Locate(ac.FlightPlan.ArrivalAirport); !ok {
		lg.Errorf("%s: unable to find arrival airport %s location?", ac.Callsign, ac.FlightPlan.ArrivalAirport)
		return nil
	}

	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(s.World, ac.FlightPlan)
	}
	ac.FlightPlan.Route = arr.Route

	// Start with the default waypoints for the arrival; these may be
	// updated when an 'expect' approach is given...
	ac.Waypoints = arr.Waypoints
	// Hold onto these with the Aircraft so we have them later.
	ac.ArrivalRunwayWaypoints = arr.RunwayWaypoints

	ac.Altitude = arr.InitialAltitude
	ac.IAS = min(arr.InitialSpeed, ac.Performance.Speed.Cruise)

	ac.Scratchpad = arr.Scratchpad
	if arr.ExpectApproach != "" {
		ap := s.World.GetAirport(ac.FlightPlan.ArrivalAirport)
		if _, ok := ap.Approaches[arr.ExpectApproach]; ok {
			ac.ApproachId = arr.ExpectApproach
		} else {
			lg.Errorf("%s: unable to find expected %s approach", ac.Callsign, arr.ExpectApproach)
			return nil
		}
	}

	if rand.Float32() < s.GoAroundRate {
		ac.AddFutureNavCommand(&GoAround{AirportDistance: 0.1 + .6*rand.Float32()})
	}

	ac.Nav.L = &FlyRoute{}
	ac.Nav.S = &FlyRoute{
		SpeedRestriction: min(arr.SpeedRestriction, ac.Performance.Speed.Cruise),
	}
	ac.Nav.V = &FlyRoute{
		AltitudeRestriction: arr.ClearedAltitude,
	}

	return ac
}

func (s *Sim) SpawnDeparture(ap *Airport, rwy *ScenarioGroupDepartureRunway) *Aircraft {
	var dep *Departure
	if rand.Float32() < s.DepartureChallenge {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		if rand.Float32() < .5 {
			dep = rwy.lastDeparture
		} else if rwy.lastDeparture != nil {
			idx := SampleFiltered(ap.Departures,
				func(d Departure) bool {
					return ap.ExitCategories[d.Exit] == ap.ExitCategories[rwy.lastDeparture.Exit]
				})
			if idx == -1 {
				// This shouldn't ever happen...
				lg.Errorf("%s: unable to find a valid departure: %s", rwy.Runway, spew.Sdump(ap))
				return nil
			}
			dep = &ap.Departures[idx]
		}
	}

	if dep == nil {
		// Sample uniformly, minding the category, if specified
		idx := SampleFiltered(ap.Departures,
			func(d Departure) bool {
				return rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit]
			})
		if idx == -1 {
			// This shouldn't ever happen...
			lg.Errorf("%s: unable to find a valid departure: %s", rwy.Runway, spew.Sdump(ap))
			return nil
		}
		dep = &ap.Departures[idx]
	}

	rwy.lastDeparture = dep

	airline := Sample(dep.Airlines)
	ac := sampleAircraft(airline.ICAO, airline.Fleet)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	ac.Waypoints = DuplicateSlice(exitRoute.Waypoints)
	ac.Waypoints = append(ac.Waypoints, dep.routeWaypoints...)

	ac.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Route
	ac.FlightPlan.ArrivalAirport = dep.Destination
	var ok bool
	if ac.FlightPlan.ArrivalAirportLocation, ok = s.World.Locate(ac.FlightPlan.ArrivalAirport); !ok {
		lg.Errorf("%s: unable to find arrival airport %s location?", ac.Callsign, ac.FlightPlan.ArrivalAirport)
		return nil
	}

	ac.Scratchpad = s.World.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(s.World, ac.FlightPlan)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	ac.TrackingController = ap.DepartureController
	ac.ControllingController = ap.DepartureController
	ac.Altitude = float32(ap.Elevation)
	ac.IsDeparture = true

	ac.Nav.L = &FlyRoute{}
	ac.Nav.S = &FlyRoute{}
	ac.Nav.V = &MaintainAltitude{Altitude: float32(ap.Elevation)}

	ac.AddFutureNavCommand(&ClimbOnceAirborne{
		Altitude: float32(min(exitRoute.ClearedAltitude, ac.FlightPlan.Altitude)),
	})

	return ac
}

///////////////////////////////////////////////////////////////////////////
// Commands from the user

type SimRateSpecifier struct {
	ControllerToken string
	Rate            float32
}

func (s *Sim) SetSimRate(r *SimRateSpecifier, _ *struct{}) error {
	if _, ok := s.controllers[r.ControllerToken]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.SimRate = r.Rate
		return nil
	}
}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        string
}

type AircraftPropertiesSpecifier struct {
	ControllerToken string
	Callsign        string
	Scratchpad      string
}

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *Controller, ac *Aircraft) error,
	cmd func(*Controller, *Aircraft) (string, error)) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.World.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		ctrl := s.World.GetController(sc.Callsign)
		if ctrl == nil {
			panic("wtf")
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			response, err := cmd(ctrl, ac)
			if response != "" {
				lg.Printf("%s: %s", ac.Callsign, response)
				s.eventStream.Post(Event{
					Type:     RadioTransmissionEvent,
					Callsign: ac.Callsign,
					Message:  response,
				})
			}
			return err
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, error)) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, error)) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

func (s *Sim) SetScratchpad(a *AircraftPropertiesSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.Scratchpad = a.Scratchpad
			return "", nil
		})
}

func (s *Sim) InitiateTrack(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			s.eventStream.Post(Event{Type: InitiatedTrackEvent, Callsign: ac.Callsign})
			return "", nil
		})
}

func (s *Sim) DropTrack(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ""
			ac.ControllingController = ""
			s.eventStream.Post(Event{Type: DroppedTrackEvent, Callsign: ac.Callsign})
			return "", nil
		})
}

type HandoffSpecifier struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (s *Sim) HandoffTrack(h *HandoffSpecifier, _ *struct{}) error {
	return s.dispatchCommand(h.ControllerToken, h.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if octrl := s.World.GetController(h.Controller); octrl == nil {
				return "", ErrNoController
			} else {
				s.eventStream.Post(Event{
					Type:           OfferedHandoffEvent,
					FromController: ctrl.Callsign,
					ToController:   octrl.Callsign,
					Callsign:       ac.Callsign,
				})

				ac.OutboundHandoffController = octrl.Callsign
				acceptDelay := 4 + rand.Intn(10)
				s.Handoffs[ac.Callsign] = s.CurrentTime.Add(time.Duration(acceptDelay) * time.Second)
				return "", nil
			}
		})
}

func (s *Sim) HandoffControl(h *HandoffSpecifier, _ *struct{}) error {
	return s.dispatchCommand(h.ControllerToken, h.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.ControllingController = ac.TrackingController

			// Go ahead and climb departures the rest of the way now.
			if ac.IsDeparture {
				lg.Errorf("%s: climbing to %d", ac.Callsign, ac.FlightPlan.Altitude)
				ac.Nav.V = &MaintainAltitude{Altitude: float32(ac.FlightPlan.Altitude)}
			}

			if octrl := s.World.GetController(ac.ControllingController); octrl != nil {
				if octrl.FullName != "" {
					return fmt.Sprintf("over to %s on %s, good day", octrl.FullName, octrl.Frequency), nil
				} else {
					return fmt.Sprintf("over to %s on %s, good day", octrl.Callsign, octrl.Frequency), nil
				}
			} else {
				return "goodbye", nil
			}
		})
}

func (s *Sim) AcceptHandoff(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.InboundHandoffController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Callsign,
				Callsign:       ac.Callsign,
			})

			ac.InboundHandoffController = ""
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			return "", nil
		})
}

func (s *Sim) CancelHandoff(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			delete(s.Handoffs, ac.Callsign)
			ac.OutboundHandoffController = ""
			return "", nil
		})
}

type AltitudeAssignment struct {
	ControllerToken string
	Callsign        string
	Altitude        int
}

func (s *Sim) AssignAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	return s.dispatchControllingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignAltitude(alt.Altitude) })
}

func (s *Sim) SetTemporaryAltitude(alt *AltitudeAssignment, _ *struct{}) error {
	return s.dispatchTrackingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TempAltitude = alt.Altitude
			return "", nil
		})
}

type HeadingAssignment struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingAssignment, _ *struct{}) error {
	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if hdg.Present {
				if _, err := ac.AssignHeading(int(ac.Heading), TurnClosest); err == nil {
					return "fly present heading", nil
				} else {
					return "", err
				}
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
		})
}

type SpeedAssignment struct {
	ControllerToken string
	Callsign        string
	Speed           int
}

func (s *Sim) AssignSpeed(sa *SpeedAssignment, _ *struct{}) error {
	return s.dispatchControllingCommand(sa.ControllerToken, sa.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignSpeed(sa.Speed) })
}

type FixSpecifier struct {
	ControllerToken string
	Callsign        string
	Fix             string
	Heading         int
	Altitude        int
	Speed           int
}

func (s *Sim) DirectFix(f *FixSpecifier, _ *struct{}) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DirectFix(f.Fix) })
}

func (s *Sim) DepartFixHeading(f *FixSpecifier, _ *struct{}) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DepartFixHeading(f.Fix, f.Heading) })
}

func (s *Sim) CrossFixAt(f *FixSpecifier, _ *struct{}) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.CrossFixAt(f.Fix, f.Altitude, f.Speed) })
}

type ApproachAssignment struct {
	ControllerToken string
	Callsign        string
	Approach        string
}

func (s *Sim) ExpectApproach(a *ApproachAssignment, _ *struct{}) error {
	return s.dispatchControllingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			return ac.ExpectApproach(a.Approach, s.World)
		})
}

type ApproachClearance struct {
	ControllerToken string
	Callsign        string
	Approach        string
	StraightIn      bool
}

func (s *Sim) ClearedApproach(c *ApproachClearance, _ *struct{}) error {
	return s.dispatchControllingCommand(c.ControllerToken, c.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if c.StraightIn {
				return ac.ClearedStraightInApproach(c.Approach, s.World)
			} else {
				return ac.ClearedApproach(c.Approach, s.World)
			}
		})
}

func (s *Sim) GoAround(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchControllingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			return ac.GoAround(), nil
		})
}

func (s *Sim) DeleteAircraft(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error { return nil },
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			delete(s.World.Aircraft, ac.Callsign)
			return "", nil
		})
}

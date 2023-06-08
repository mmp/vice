// sim.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
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
	c.SetScenarioGroup(scenarioGroup)
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
	sim = NewSim(*c)
	scenarioGroup = c.scenarioGroup
	sim.Prespawn()

	globalConfig.LastScenarioGroup = c.scenarioGroup.Name

	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		if stars, ok := p.(*STARSPane); ok {
			stars.ResetScenarioGroup()
			stars.ResetScenario(c.scenario)
		}
	})

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	Scenario *Scenario

	Aircraft map[string]*Aircraft
	Handoffs map[string]time.Time
	METAR    map[string]*METAR

	SerializeTime time.Time // for updating times on deserialize

	currentTime    time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	SimRate        float32
	Paused         bool

	eventsId EventSubscriberId

	DepartureChallenge float32
	GoAroundRate       float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	showSettings bool

	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]*int32
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]*int32

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
}

func NewSim(ssc NewSimConfiguration) *Sim {
	rand.Seed(time.Now().UnixNano())

	sim := &Sim{
		Scenario: ssc.scenario,

		Aircraft: make(map[string]*Aircraft),
		Handoffs: make(map[string]time.Time),
		METAR:    make(map[string]*METAR),

		DepartureRates:    DuplicateMap(ssc.departureRates),
		ArrivalGroupRates: DuplicateMap(ssc.arrivalGroupRates),

		currentTime:        time.Now(),
		lastUpdateTime:     time.Now(),
		eventsId:           eventStream.Subscribe(),
		SimRate:            1,
		DepartureChallenge: ssc.departureChallenge,
		GoAroundRate:       ssc.goAroundRate,
	}

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)
	fakeMETAR := func(icao string) {
		spd := sim.Scenario.Wind.Speed - 3 + rand.Int31n(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((sim.Scenario.Wind.Direction + 5) / 10)
			dir += [3]int32{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := sim.Scenario.Wind.Gust - 3 + rand.Int31n(6)
			if gst-sim.Scenario.Wind.Speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		sim.METAR[icao] = &METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

	for ap := range sim.DepartureAirports() {
		fakeMETAR(ap)
	}
	for ap := range sim.ArrivalAirports() {
		fakeMETAR(ap)
	}

	sim.SetInitialSpawnTimes()

	return sim
}

func (sim *Sim) DepartureAirports() map[string]interface{} {
	airports := make(map[string]interface{})
	for ap, runwayRates := range sim.DepartureRates {
		for _, categoryRates := range runwayRates {
			for _, rate := range categoryRates {
				if *rate > 0 {
					airports[ap] = nil
				}
			}
		}
	}
	return airports
}

func (sim *Sim) ArrivalAirports() map[string]interface{} {
	airports := make(map[string]interface{})
	for _, airportRates := range sim.ArrivalGroupRates {
		for ap, rate := range airportRates {
			if *rate > 0 {
				airports[ap] = nil
			}
		}
	}
	return airports
}

func (sim *Sim) SetInitialSpawnTimes() {
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

	sim.NextArrivalSpawn = make(map[string]time.Time)
	for group, rates := range sim.ArrivalGroupRates {
		rateSum := 0
		for _, rate := range rates {
			rateSum += int(*rate)
		}
		sim.NextArrivalSpawn[group] = randomSpawn(rateSum)
	}

	sim.NextDepartureSpawn = make(map[string]map[string]time.Time)
	for airport, runwayRates := range sim.DepartureRates {
		spawn := make(map[string]time.Time)

		for runway, categoryRates := range runwayRates {
			rateSum := 0
			for _, rate := range categoryRates {
				rateSum += int(*rate)
			}
			if rateSum > 0 {
				spawn[runway] = randomSpawn(rateSum)
			}
		}

		if len(spawn) > 0 {
			sim.NextDepartureSpawn[airport] = spawn
		}
	}
}

func (sim *Sim) Activate() error {
	var e ErrorLogger
	now := time.Now()
	sim.currentTime = now
	sim.lastUpdateTime = now
	sim.eventsId = eventStream.Subscribe()

	// A number of time.Time values are included in the serialized Sim.
	// updateTime is a helper function that rewrites them to be in terms of
	// the current time, using the serializion time as a baseline.
	updateTime := func(t time.Time) time.Time {
		return now.Add(t.Sub(sim.SerializeTime))
	}

	for _, ac := range sim.Aircraft {
		e.Push(ac.Callsign)
		// Rewrite the radar track times to be w.r.t now
		for i := range ac.Tracks {
			ac.Tracks[i].Time = updateTime(ac.Tracks[i].Time)
		}

		if ac.Approach != nil {
			for i := range ac.Approach.Waypoints {
				scenarioGroup.InitializeWaypointLocations(ac.Approach.Waypoints[i], &e)
			}
			lg.Errorf("%s", spew.Sdump(ac.Approach))
		}

		for rwy, wp := range ac.ArrivalRunwayWaypoints {
			e.Push("Arrival runway " + rwy)
			scenarioGroup.InitializeWaypointLocations(wp, &e)
			e.Pop()
		}

		e.Pop()
		eventStream.Post(&AddedAircraftEvent{ac: ac})
	}

	for ho, t := range sim.Handoffs {
		sim.Handoffs[ho] = updateTime(t)
	}

	for group, t := range sim.NextArrivalSpawn {
		sim.NextArrivalSpawn[group] = updateTime(t)
	}

	for airport, runwayTimes := range sim.NextDepartureSpawn {
		for runway, t := range runwayTimes {
			sim.NextDepartureSpawn[airport][runway] = updateTime(t)
		}
	}

	if e.HaveErrors() {
		e.PrintErrors()
		return errors.New("Errors during state restoration")
	}
	return nil
}

func (sim *Sim) Prespawn() {
	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		sim.currentTime = t
		sim.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		sim.updateState()
	}
	sim.currentTime = time.Now()
	sim.lastUpdateTime = time.Now()
}

func (sim *Sim) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) SetScratchpad(callsign string, scratchpad string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != sim.Scenario.Callsign {
		// Scratchpad is tracking controller, not controlling controller
		return ErrOtherControllerHasTrack
	} else {
		ac.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return nil
	}
}

func (sim *Sim) SetTemporaryAltitude(callsign string, alt int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != sim.Scenario.Callsign {
		// Temp alt is tracking controller, not controlling controller
		return ErrOtherControllerHasTrack
	} else {
		ac.TempAltitude = alt
		return nil
	}
}

func (sim *Sim) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) InitiateTrack(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = sim.Scenario.Callsign
		ac.ControllingController = sim.Scenario.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&InitiatedTrackEvent{ac: ac})
		return nil
	}
}

func (sim *Sim) DropTrack(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != sim.Scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = ""
		ac.ControllingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&DroppedTrackEvent{ac: ac})
		return nil
	}
}

func (sim *Sim) Handoff(callsign string, controller string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != sim.Scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := sim.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		acceptDelay := 4 + rand.Intn(10)
		sim.Handoffs[callsign] = sim.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (sim *Sim) AcceptHandoff(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.InboundHandoffController != sim.Scenario.Callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.InboundHandoffController = ""
		ac.TrackingController = sim.Callsign()
		ac.ControllingController = sim.Callsign()
		eventStream.Post(&AcceptedHandoffEvent{controller: sim.Callsign(), ac: ac})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac}) // FIXME...
		return nil
	}
}

func (sim *Sim) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (sim *Sim) CancelHandoff(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != sim.Scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else {
		delete(sim.Handoffs, ac.Callsign)

		ac.OutboundHandoffController = ""
		// TODO: we are inconsistent in other control backends about events
		// when user does things like this; sometimes no event, sometimes
		// modified a/c event...
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return nil
	}
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
	if sim.Scenario == nil {
		return nil
	}

	ctrl, ok := scenarioGroup.ControlPositions[callsign]
	if ok {
		return ctrl
	}

	for _, c := range scenarioGroup.ControlPositions {
		// Make sure that the controller is active in the scenarioGroup...
		if c.SectorId == callsign && Find(sim.Scenario.Controllers, c.Callsign) != -1 {
			return c
		}
	}

	return ctrl
}

func (sim *Sim) GetAllControllers() []*Controller {
	if sim.Scenario == nil {
		return nil
	}

	_, ctrl := FlattenMap(scenarioGroup.ControlPositions)
	return FilterSlice(ctrl,
		func(ctrl *Controller) bool { return Find(sim.Scenario.Controllers, ctrl.Callsign) != -1 })
}

func (sim *Sim) GetUpdates() {
	if sim.Paused || sim.Scenario == nil {
		return
	}

	// Process events
	if sim.eventsId != InvalidEventSubscriberId {
		for _, ev := range eventStream.Get(sim.eventsId) {
			if rem, ok := ev.(*RemovedAircraftEvent); ok {
				delete(sim.Aircraft, rem.ac.Callsign)
			}
			if ack, ok := ev.(*AckedHandoffEvent); ok {
				// the user acknowledged that the other controller took the
				// handoff. This is the point where the other controller
				// takes control.  We'll just climb them to their cruise
				// altitude...
				if ack.ac.IsDeparture {
					lg.Errorf("%s: climbing to %d", ack.ac.Callsign, ack.ac.FlightPlan.Altitude)
					ack.ac.Nav.V = &MaintainAltitude{
						Altitude: float32(ack.ac.FlightPlan.Altitude),
					}
				}
			}
		}
	}

	// Update the current time
	elapsed := time.Since(sim.lastUpdateTime)
	elapsed = time.Duration(sim.SimRate * float32(elapsed))
	sim.currentTime = sim.currentTime.Add(elapsed)
	sim.lastUpdateTime = time.Now()

	sim.updateState()
}

// FIXME: this is poorly named...
func (sim *Sim) updateState() {
	// Accept any handoffs whose time has time...
	now := sim.CurrentTime()
	for callsign, t := range sim.Handoffs {
		if now.After(t) {
			if ac, ok := sim.Aircraft[callsign]; ok {
				ac.TrackingController = ac.OutboundHandoffController
				ac.OutboundHandoffController = ""
				eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
				globalConfig.Audio.PlaySound(AudioEventHandoffAccepted)
			}
			delete(sim.Handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(sim.lastSimUpdate) >= time.Second {
		sim.lastSimUpdate = now
		for _, ac := range sim.Aircraft {
			ac.Update()
		}
	}

	// Add a new radar track every 5 seconds.  While we're at it, cull
	// departures that are far from the airport.
	if now.Sub(sim.lastTrackUpdate) >= 5*time.Second {
		sim.lastTrackUpdate = now

		for callsign, ac := range sim.Aircraft {
			if ap, ok := scenarioGroup.Airports[ac.FlightPlan.DepartureAirport]; ok && ac.IsDeparture {
				if nmdistance2ll(ac.Position, ap.Location) > 200 {
					eventStream.Post(&RemovedAircraftEvent{ac: ac})
					delete(sim.Aircraft, callsign)
					continue
				}
			}

			ac.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - scenarioGroup.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		}
	}

	sim.SpawnAircraft()
}

func (sim *Sim) Connected() bool {
	return true
}

func (sim *Sim) Callsign() string {
	if sim.Scenario != nil {
		return sim.Scenario.Callsign
	} else {
		return "(disconnected)"
	}
}

func (sim *Sim) CurrentTime() time.Time {
	return sim.currentTime
}

func (sim *Sim) GetWindowTitle() string {
	if sim.Scenario == nil {
		return "(disconnected)"
	}
	return sim.Scenario.Callsign + ": " + sim.Scenario.Name()
}

func pilotResponse(callsign string, fm string, args ...interface{}) {
	lg.Printf("%s: %s", callsign, fmt.Sprintf(fm, args...))
	eventStream.Post(&RadioTransmissionEvent{callsign: callsign, message: fmt.Sprintf(fm, args...)})
}

func (sim *Sim) AssignAltitude(callsign string, altitude int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.AssignAltitude(altitude)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) AssignHeading(callsign string, heading int, turn int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.AssignHeading(heading, turn)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) TurnLeft(callsign string, deg int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.TurnLeft(deg)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) TurnRight(callsign string, deg int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.TurnRight(deg)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) AssignSpeed(callsign string, speed int) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.AssignSpeed(speed)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) DirectFix(callsign string, fix string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	} else {
		resp, err := ac.DirectFix(fix)
		if resp != "" {
			pilotResponse(callsign, "%s", resp)
		}
		return err
	}
}

func (sim *Sim) getApproach(callsign string, approach string) (*Approach, *Aircraft, error) {
	ac, ok := sim.Aircraft[callsign]
	if !ok {
		return nil, nil, ErrNoAircraftForCallsign
	}
	fp := ac.FlightPlan
	if fp == nil {
		return nil, nil, ErrNoFlightPlan
	}

	ap, ok := scenarioGroup.Airports[fp.ArrivalAirport]
	if !ok {
		lg.Errorf("Can't find TRACON airport %s for %s approach for %s", fp.ArrivalAirport, approach, callsign)
		return nil, nil, ErrArrivalAirportUnknown
	}

	for name, appr := range ap.Approaches {
		if name == approach {
			return &appr, ac, nil
		}
	}
	return nil, nil, ErrUnknownApproach
}

func (sim *Sim) ExpectApproach(callsign string, approach string) error {
	ap, ac, err := sim.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	}

	resp, err := ac.ExpectApproach(ap)
	if resp != "" {
		pilotResponse(callsign, "%s", resp)
	}
	return err
}

func (sim *Sim) ClearedApproach(callsign string, approach string) error {
	ap, ac, err := sim.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	resp, err := ac.ClearedApproach(ap)
	if resp != "" {
		pilotResponse(callsign, "%s", resp)
	}
	return err
}

func (sim *Sim) ClearedStraightInApproach(callsign string, approach string) error {
	ap, ac, err := sim.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	if ac.ControllingController != sim.Callsign() {
		return ErrOtherControllerHasTrack
	}

	resp, err := ac.ClearedStraightInApproach(ap)
	if resp != "" {
		pilotResponse(callsign, "%s", resp)
	}
	return err
}

func (sim *Sim) PrintInfo(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		lg.Errorf("%s", spew.Sdump(ac))

		s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
			ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
		if ac.ApproachCleared {
			s += ", cleared approach"
		}
		lg.Errorf("%s", s)
	}
	return nil
}

func (sim *Sim) DeleteAircraft(callsign string) error {
	if ac, ok := sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
		delete(sim.Aircraft, callsign)
		return nil
	}
}

func (sim *Sim) IsPaused() bool {
	return sim.Paused
}

func (sim *Sim) TogglePause() {
	sim.Paused = !sim.Paused
	sim.lastUpdateTime = time.Now() // ignore time passage...
}

func (sim *Sim) ToggleActivateSettingsWindow() {
	sim.showSettings = !sim.showSettings
}

func (sim *Sim) DrawSettingsWindow() {
	if !sim.showSettings {
		return
	}

	imgui.BeginV("Simulation Settings", &sim.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if *devmode {
		imgui.SliderFloatV("Simulation speed", &sim.SimRate, 1, 100, "%.1f", 0)
	} else {
		imgui.SliderFloatV("Simulation speed", &sim.SimRate, 1, 10, "%.1f", 0)
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

func (sim *Sim) GetWindVector(p Point2LL, alt float32) Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	s := sim.currentTime.Sub(base).Seconds()
	windSpeed := float32(sim.Scenario.Wind.Speed) +
		float32(sim.Scenario.Wind.Gust)*float32(1+math.Cos(s/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := float32(sim.Scenario.Wind.Direction + 180)
	vWind := [2]float32{sin(radians(d)), cos(radians(d))}
	vWind = scale2f(vWind, windSpeed/3600)
	return vWind
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func sampleRateMap(rates map[string]*int32) (string, int) {
	// Choose randomly in proportion to the rates in the map
	rateSum := 0
	var result string
	for item, rate := range rates {
		rateSum += int(*rate)
		// Weighted reservoir sampling...
		if rand.Float32() < float32(int(*rate))/float32(rateSum) {
			result = item
		}
	}
	return result, rateSum
}

func (sim *Sim) SpawnAircraft() {
	now := sim.CurrentTime()

	addAircraft := func(ac *Aircraft) {
		if _, ok := sim.Aircraft[ac.Callsign]; ok {
			lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
			return
		}
		sim.Aircraft[ac.Callsign] = ac

		ac.RunWaypointCommands(ac.Waypoints[0])

		ac.Position = ac.Waypoints[0].Location
		if ac.Position.IsZero() {
			lg.Errorf("%s: uninitialized initial waypoint position! %+v", ac.Callsign, ac.Waypoints[0])
			return
		}

		ac.Heading = float32(ac.Waypoints[0].Heading)
		if ac.Heading == 0 { // unassigned, so get the heading from the next fix
			ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, scenarioGroup.MagneticVariation)
		}
		ac.Waypoints = FilterSlice(ac.Waypoints[1:], func(wp Waypoint) bool { return !wp.Location.IsZero() })

		eventStream.Post(&AddedAircraftEvent{ac: ac})
	}

	randomWait := func(rate int) time.Duration {
		if rate == 0 {
			return 365 * 24 * time.Hour
		}
		avgSeconds := 3600 / float32(rate)
		seconds := lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
		return time.Duration(seconds * float32(time.Second))
	}

	for group, airportRates := range sim.ArrivalGroupRates {
		if now.After(sim.NextArrivalSpawn[group]) {
			arrivalAirport, rateSum := sampleRateMap(airportRates)

			if ac := sim.SpawnArrival(arrivalAirport, group); ac != nil {
				ac.FlightPlan.ArrivalAirport = arrivalAirport
				addAircraft(ac)
				sim.NextArrivalSpawn[group] = now.Add(randomWait(rateSum))
			}
		}
	}

	for airport, runwayTimes := range sim.NextDepartureSpawn {
		for runway, spawnTime := range runwayTimes {
			if !now.After(spawnTime) {
				continue
			}

			// Figure out which category to launch
			category, rateSum := sampleRateMap(sim.DepartureRates[airport][runway])
			if rateSum == 0 {
				lg.Errorf("%s/%s: couldn't find a matching runway for spawning departure?", airport, runway)
				continue
			}

			ap := scenarioGroup.Airports[airport]
			idx := FindIf(sim.Scenario.DepartureRunways,
				func(r ScenarioGroupDepartureRunway) bool {
					return r.Airport == airport && r.Runway == runway && r.Category == category
				})
			if idx == -1 {
				lg.Errorf("%s/%s/%s: couldn't find airport/runway/category for spawning departure. rates %s dep runways %s", airport, runway, category, spew.Sdump(sim.DepartureRates[airport][runway]), spew.Sdump(sim.Scenario.DepartureRunways))
				continue
			}

			if ac := sim.SpawnDeparture(ap, &sim.Scenario.DepartureRunways[idx]); ac != nil {
				ac.FlightPlan.DepartureAirport = airport
				addAircraft(ac)
				sim.NextDepartureSpawn[airport][runway] = now.Add(randomWait(rateSum))
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

func (sim *Sim) SpawnArrival(airportName string, arrivalGroup string) *Aircraft {
	arrivals := scenarioGroup.ArrivalGroups[arrivalGroup]
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
	ac.FlightPlan.ArrivalAirport = airportName
	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(ac.FlightPlan)
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
		if appr, ok := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport].Approaches[arr.ExpectApproach]; ok {
			ac.Approach = &appr
		} else {
			lg.Errorf("%s: unable to find expected %s approach", ac.Callsign, arr.ExpectApproach)
			return nil
		}
	}

	if rand.Float32() < sim.GoAroundRate {
		ac.AddFutureNavCommand(&GoAround{AirportDistance: 0.1 + .6*rand.Float32()})
	}

	ac.Nav.L = &FlyRoute{}
	if arr.SpeedRestriction != 0 {
		ac.Nav.S = &MaintainSpeed{IAS: min(arr.SpeedRestriction, ac.Performance.Speed.Cruise)}
	} else {
		ac.Nav.S = &FlyRoute{}
	}
	if arr.ClearedAltitude != 0 {
		ac.Nav.V = &MaintainAltitude{Altitude: arr.ClearedAltitude}
	} else {
		ac.Nav.V = &FlyRoute{}
	}

	return ac
}

func (sim *Sim) SpawnDeparture(ap *Airport, rwy *ScenarioGroupDepartureRunway) *Aircraft {
	var dep *Departure
	if rand.Float32() < sim.DepartureChallenge {
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

	exitRoute := rwy.exitRoutes[dep.Exit]
	ac.Waypoints = DuplicateSlice(exitRoute.Waypoints)
	ac.Waypoints = append(ac.Waypoints, dep.routeWaypoints...)

	ac.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Route
	ac.FlightPlan.ArrivalAirport = dep.Destination
	ac.Scratchpad = scenarioGroup.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(ac.FlightPlan)
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

// sim.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
)

type Scenario struct {
	Name        string   `json:"name"`
	Callsign    string   `json:"callsign"`
	Wind        Wind     `json:"wind"`
	Controllers []string `json:"controllers"`

	// Map from arrival group name to map from airport name to rate...
	ArrivalGroupRates map[string]map[string]*int32 `json:"arrivals"`

	// Key is arrival group name
	nextArrivalSpawn map[string]time.Time

	ApproachAirspace       []AirspaceVolume `json:"-"`
	DepartureAirspace      []AirspaceVolume `json:"-"`
	ApproachAirspaceNames  []string         `json:"approach_airspace"`
	DepartureAirspaceNames []string         `json:"departure_airspace"`

	DepartureRunways []ScenarioDepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []ScenarioArrivalRunway   `json:"arrival_runways,omitempty"`

	// The same runway may be present multiple times in DepartureRunways,
	// with different Category values. However, we want to make sure that
	// we don't spawn two aircraft on the same runway at the same time (or
	// close to it).  Therefore, here we track a per-runway "when's the
	// next time that we will spawn *something* from the runway" time.
	// When the time is up, we'll figure out which specific matching entry
	// in DepartureRunways to use...
	nextDepartureSpawn map[string]time.Time
}

type ScenarioDepartureRunway struct {
	Airport  string `json:"airport"`
	Runway   string `json:"runway"`
	Category string `json:"category,omitempty"`
	Rate     int32  `json:"rate"`

	lastDeparture *Departure
	exitRoutes    map[string]ExitRoute // copied from DepartureRunway
}

type ScenarioArrivalRunway struct {
	Airport string `json:"airport"`
	Runway  string `json:"runway"`
}

func (s *Scenario) AllAirports() []string {
	return append(s.DepartureAirports(), s.ArrivalAirports()...)
}

func (s *Scenario) DepartureAirports() []string {
	m := make(map[string]interface{})
	for _, rwy := range s.DepartureRunways {
		m[rwy.Airport] = nil
	}
	return SortedMapKeys(m)
}

func (s *Scenario) ArrivalAirports() []string {
	m := make(map[string]interface{})
	for _, rwy := range s.ArrivalRunways {
		m[rwy.Airport] = nil
	}
	return SortedMapKeys(m)
}

func (s *Scenario) runwayDepartureRate(ar string) int {
	r := 0
	for _, rwy := range s.DepartureRunways {
		if ar == rwy.Airport+"/"+rwy.Runway {
			r += int(rwy.Rate)
		}
	}
	return r
}

func (s *Scenario) PostDeserialize(t *TRACON) []error {
	var errors []error

	for _, as := range s.ApproachAirspaceNames {
		if vol, ok := t.AirspaceVolumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown approach airspace in scenario %s", as, s.Name))
		} else {
			s.ApproachAirspace = append(s.ApproachAirspace, vol...)
		}
	}
	for _, as := range s.DepartureAirspaceNames {
		if vol, ok := t.AirspaceVolumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown departure airspace in scenario %s", as, s.Name))
		} else {
			s.DepartureAirspace = append(s.DepartureAirspace, vol...)
		}
	}

	sort.Slice(s.DepartureRunways, func(i, j int) bool {
		if s.DepartureRunways[i].Airport != s.DepartureRunways[j].Airport {
			return s.DepartureRunways[i].Airport < s.DepartureRunways[j].Airport
		} else if s.DepartureRunways[i].Runway != s.DepartureRunways[j].Runway {
			return s.DepartureRunways[i].Runway < s.DepartureRunways[j].Runway
		} else {
			return s.DepartureRunways[i].Category < s.DepartureRunways[j].Category
		}
	})

	s.nextDepartureSpawn = make(map[string]time.Time)
	for i, rwy := range s.DepartureRunways {
		if ap, ok := t.Airports[rwy.Airport]; !ok {
			errors = append(errors, fmt.Errorf("%s: airport not found for departure runway in scenario %s", rwy.Airport, s.Name))
		} else {
			idx := FindIf(ap.DepartureRunways, func(r *DepartureRunway) bool { return r.Runway == rwy.Runway })
			if idx == -1 {
				errors = append(errors, fmt.Errorf("%s: runway not found at airport %s for departure runway in scenario %s",
					rwy.Runway, rwy.Airport, s.Name))
			} else {
				s.DepartureRunways[i].exitRoutes = ap.DepartureRunways[idx].ExitRoutes
			}
			s.nextDepartureSpawn[rwy.Airport+"/"+rwy.Runway] = time.Time{}

			if rwy.Category != "" {
				found := false
				for _, dep := range ap.Departures {
					if ap.ExitCategories[dep.Exit] == rwy.Category {
						found = true
						break
					}
				}
				if !found {
					errors = append(errors,
						fmt.Errorf("%s: no departures from %s have exit category specified for departure runway %s in scenario %s",
							rwy.Category, rwy.Airport, rwy.Runway, s.Name))
				}
			}
		}
	}

	sort.Slice(s.ArrivalRunways, func(i, j int) bool {
		if s.ArrivalRunways[i].Airport == s.ArrivalRunways[j].Airport {
			return s.ArrivalRunways[i].Runway < s.ArrivalRunways[j].Runway
		}
		return s.ArrivalRunways[i].Airport < s.ArrivalRunways[j].Airport
	})

	for _, rwy := range s.ArrivalRunways {
		if ap, ok := t.Airports[rwy.Airport]; !ok {
			errors = append(errors, fmt.Errorf("%s: airport not found for arrival runway in scenario %s", rwy.Airport, s.Name))
		} else if FindIf(ap.ArrivalRunways, func(r *ArrivalRunway) bool { return r.Runway == rwy.Runway }) == -1 {
			errors = append(errors, fmt.Errorf("%s: runway not found for arrival runway at airport %s in scenario %s",
				rwy.Runway, rwy.Airport, s.Name))
		}
	}

	s.nextArrivalSpawn = make(map[string]time.Time)

	for _, name := range SortedMapKeys(s.ArrivalGroupRates) {
		// Make sure the arrival group has been defined
		if arrivals, ok := t.ArrivalGroups[name]; !ok {
			errors = append(errors, fmt.Errorf("%s: arrival group not found in TRACON in scenario %s", name, s.Name))
		} else {
			// Check the airports in it
			for airport := range s.ArrivalGroupRates[name] {
				if _, ok := t.Airports[airport]; !ok {
					errors = append(errors, fmt.Errorf("%s: unknown arrival airport in %s arrival group in scenario %s",
						airport, name, s.Name))
				} else {
					found := false
					for _, ar := range arrivals {
						if _, ok := ar.Airlines[airport]; ok {
							found = true
							break
						}
					}
					if !found {
						errors = append(errors, fmt.Errorf("%s: airport not included in any arrivals in %s arrival group in scenario %s",
							airport, name, s.Name))
					}
				}
			}
		}
	}

	for _, ctrl := range s.Controllers {
		if _, ok := t.ControlPositions[ctrl]; !ok {
			errors = append(errors, fmt.Errorf("%s: controller unknown in scenario %s", ctrl, s.Name))
		}
	}

	return errors
}

type Wind struct {
	Direction int32 `json:"direction"`
	Speed     int32 `json:"speed"`
	Gust      int32 `json:"gust"`
}

type SimConnectionConfiguration struct {
	numAircraft      int32
	challenge        float32
	scenario         *Scenario
	controller       *Controller
	validControllers map[string]*Controller
}

func (ssc *SimConnectionConfiguration) Initialize() {
	ssc.numAircraft = 30
	ssc.challenge = 0.25
	ssc.scenario = tracon.Scenarios[tracon.DefaultScenario]
	ssc.validControllers = make(map[string]*Controller)
	for _, sc := range tracon.Scenarios {
		ssc.validControllers[sc.Callsign] = tracon.ControlPositions[sc.Callsign]
	}
	ssc.controller = tracon.ControlPositions[tracon.DefaultController]
}

func (ssc *SimConnectionConfiguration) DrawUI() bool {
	if imgui.BeginComboV("Control Position", ssc.controller.Callsign, imgui.ComboFlagsHeightLarge) {
		for _, controllerName := range SortedMapKeys(ssc.validControllers) {
			if imgui.SelectableV(controllerName, controllerName == ssc.controller.Callsign, 0, imgui.Vec2{}) {
				ssc.controller = ssc.validControllers[controllerName]
				// Set the current scenario to the first one alphabetically
				// with the selected controller.
				for _, scenarioName := range SortedMapKeys(tracon.Scenarios) {
					if tracon.Scenarios[scenarioName].Callsign == controllerName {
						ssc.scenario = tracon.Scenarios[scenarioName]
						break
					}
				}
			}
		}
		imgui.EndCombo()
	}

	scenario := ssc.scenario

	if imgui.BeginComboV("Scenario", scenario.Name, imgui.ComboFlagsHeightLarge) {
		for _, name := range SortedMapKeys(tracon.Scenarios) {
			if tracon.Scenarios[name].Callsign != ssc.controller.Callsign {
				continue
			}
			if imgui.SelectableV(name, name == scenario.Name, 0, imgui.Vec2{}) {
				ssc.scenario = tracon.Scenarios[name]
			}
		}
		imgui.EndCombo()
	}

	imgui.InputIntV("Total Aircraft", &ssc.numAircraft, 1, 100, 0)

	if imgui.BeginTableV("scenario", 2, 0, imgui.Vec2{500, 0}, 0.) {
		imgui.TableNextRow()
		imgui.TableNextColumn()

		if len(scenario.DepartureRunways) > 0 {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Departing:")
			imgui.TableNextColumn()

			d := SortedMapKeys(scenario.nextDepartureSpawn)
			// Only list ones that are actually active.
			d = FilterSlice(d, func(rwy string) bool {
				return scenario.runwayDepartureRate(rwy) > 0
			})
			imgui.Text(strings.Join(d, ", "))
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
		imgui.SliderFloatV("Sequencing challenge", &ssc.challenge, 0, 1, "%.02f", 0)
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

		if imgui.BeginTableV("departureRunways", 4, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
			imgui.TableHeadersRow()

			for i, rwy := range scenario.DepartureRunways {
				imgui.PushID(rwy.Airport + rwy.Runway + rwy.Category)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(rwy.Airport)
				imgui.TableNextColumn()
				imgui.Text(rwy.Runway)
				imgui.TableNextColumn()
				if rwy.Category == "" {
					imgui.Text("(All)")
				} else {
					imgui.Text(rwy.Category)
				}
				imgui.TableNextColumn()
				imgui.InputIntV("##adr", &scenario.DepartureRunways[i].Rate, 0, 120, 0)
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	if len(scenario.ArrivalGroupRates) > 0 {
		// Figure out how many unique airports we've got for AAR columns in the table...
		allAirports := make(map[string]interface{})
		for _, agr := range scenario.ArrivalGroupRates {
			for ap := range agr {
				allAirports[ap] = nil
			}
		}
		nAirports := len(allAirports)

		imgui.Separator()
		imgui.Text("Arrivals")
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("arrivalgroups", 1+nAirports, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Arrival")
			for _, ap := range SortedMapKeys(allAirports) {
				imgui.TableSetupColumn(ap + " AAR")
			}
			imgui.TableHeadersRow()

			for _, group := range SortedMapKeys(scenario.ArrivalGroupRates) {
				imgui.PushID(group)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(group)
				airportRates := scenario.ArrivalGroupRates[group]
				for _, ap := range SortedMapKeys(airportRates) {
					imgui.TableNextColumn()
					imgui.InputIntV("##aar-"+ap, airportRates[ap], 0, 120, 0)
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	return false
}

func (ssc *SimConnectionConfiguration) Valid() bool {
	return true
}

func (ssc *SimConnectionConfiguration) Connect() error {
	// Send out events to remove any existing aircraft (necessary for when
	// we restart...)
	for _, ac := range sim.GetAllAircraft() {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	sim.Disconnect()
	sim = NewSim(*ssc)
	sim.Prespawn()
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	scenario *Scenario

	aircraft map[string]*Aircraft
	handoffs map[string]time.Time
	metar    map[string]*METAR

	currentTime       time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime    time.Time // this is w.r.t. true wallclock time
	simRate           float32
	paused            bool
	remainingLaunches int

	eventsId EventSubscriberId

	challenge float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	showSettings bool
}

func NewSim(ssc SimConnectionConfiguration) *Sim {
	rand.Seed(time.Now().UnixNano())

	ss := &Sim{
		scenario: ssc.scenario,

		aircraft: make(map[string]*Aircraft),
		handoffs: make(map[string]time.Time),
		metar:    make(map[string]*METAR),

		currentTime:       time.Now(),
		lastUpdateTime:    time.Now(),
		remainingLaunches: int(ssc.numAircraft),
		eventsId:          eventStream.Subscribe(),
		simRate:           1,
		challenge:         ssc.challenge,
	}

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)
	for _, ap := range ss.scenario.AllAirports() {
		spd := ss.scenario.Wind.Speed - 3 + rand.Int31n(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((ss.scenario.Wind.Direction + 5) / 10)
			dir += [3]int32{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := ss.scenario.Wind.Gust - 3 + rand.Int31n(6)
			if gst-ss.scenario.Wind.Speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		ss.metar[ap] = &METAR{
			AirportICAO: ap,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

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

	for group, rates := range ss.scenario.ArrivalGroupRates {
		rateSum := 0
		for _, rate := range rates {
			rateSum += int(*rate)
		}
		ss.scenario.nextArrivalSpawn[group] = randomSpawn(rateSum)
	}
	for rwy := range ss.scenario.nextDepartureSpawn {
		ss.scenario.nextDepartureSpawn[rwy] = randomSpawn(ss.scenario.runwayDepartureRate(rwy))
	}

	return ss
}

func (s *Sim) Prespawn() {
	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		s.currentTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.currentTime = time.Now()
	s.lastUpdateTime = time.Now()
}

func (ss *Sim) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) SetScratchpad(callsign string, scratchpad string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return nil
	}
}

func (ss *Sim) SetTemporaryAltitude(callsign string, alt int) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) PushFlightStrip(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) InitiateTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = ss.scenario.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&InitiatedTrackEvent{ac: ac})
		return nil
	}
}

func (ss *Sim) DropTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&DroppedTrackEvent{ac: ac})
		return nil
	}
}

func (ss *Sim) Handoff(callsign string, controller string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := ss.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		acceptDelay := 2 + rand.Intn(10)
		ss.handoffs[callsign] = ss.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (ss *Sim) AcceptHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.InboundHandoffController != ss.scenario.Callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.InboundHandoffController = ""
		ac.TrackingController = ss.scenario.Callsign
		eventStream.Post(&AcceptedHandoffEvent{controller: ss.scenario.Callsign, ac: ac})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac}) // FIXME...
		return nil
	}
}

func (ss *Sim) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) CancelHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.scenario.Callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.OutboundHandoffController = ""
		// TODO: we are inconsistent in other control backends about events
		// when user does things like this; sometimes no event, sometimes
		// modified a/c event...
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return nil
	}
}

func (ss *Sim) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) RequestControllerATIS(controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil // UNIMPLEMENTED
}

func (ss *Sim) Disconnect() {
	for _, ac := range ss.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	if ss.eventsId != InvalidEventSubscriberId {
		eventStream.Unsubscribe(ss.eventsId)
		ss.eventsId = InvalidEventSubscriberId
	}
}

func (ss *Sim) GetAircraft(callsign string) *Aircraft {
	if ac, ok := ss.aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (ss *Sim) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range ss.aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (ss *Sim) GetAllAircraft() []*Aircraft {
	return ss.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (ss *Sim) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := ss.aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (ss *Sim) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (ss *Sim) GetMETAR(location string) *METAR {
	return ss.metar[location]
}

func (ss *Sim) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (s *Sim) GetController(callsign string) *Controller {
	if s.scenario == nil {
		return nil
	}

	ctrl, ok := tracon.ControlPositions[callsign]
	if ok {
		return ctrl
	}

	for _, c := range tracon.ControlPositions {
		// Make sure that the controller is active in the scenario...
		if c.SectorId == callsign && Find(s.scenario.Controllers, c.Callsign) != -1 {
			return c
		}
	}

	return ctrl
}

func (s *Sim) GetAllControllers() []*Controller {
	if s.scenario == nil {
		return nil
	}

	_, ctrl := FlattenMap(tracon.ControlPositions)
	return FilterSlice(ctrl,
		func(ctrl *Controller) bool { return Find(s.scenario.Controllers, ctrl.Callsign) != -1 })
}

func (ss *Sim) SetPrimaryFrequency(f Frequency) {
	// UNIMPLEMENTED
}

func (ss *Sim) GetUpdates() {
	if ss.paused || ss.scenario == nil {
		return
	}

	// Process events
	if ss.eventsId != InvalidEventSubscriberId {
		for _, ev := range eventStream.Get(ss.eventsId) {
			if rem, ok := ev.(*RemovedAircraftEvent); ok {
				delete(ss.aircraft, rem.ac.Callsign)
			}
		}
	}

	// Update the current time
	elapsed := time.Since(ss.lastUpdateTime)
	elapsed = time.Duration(ss.simRate * float32(elapsed))
	ss.currentTime = ss.currentTime.Add(elapsed)
	ss.lastUpdateTime = time.Now()

	ss.updateState()
}

// FIXME: this is poorly named...
func (ss *Sim) updateState() {
	// Accept any handoffs whose time has time...
	now := ss.CurrentTime()
	for callsign, t := range ss.handoffs {
		if now.After(t) {
			ac := ss.aircraft[callsign]
			ac.TrackingController = ac.OutboundHandoffController
			ac.OutboundHandoffController = ""
			eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
			globalConfig.Audio.PlaySound(AudioEventHandoffAccepted)
			delete(ss.handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(ss.lastSimUpdate) >= time.Second {
		ss.lastSimUpdate = now
		for _, ac := range ss.aircraft {
			ac.Update()
		}
	}

	// Add a new radar track every 5 seconds.
	if now.Sub(ss.lastTrackUpdate) >= 5*time.Second {
		ss.lastTrackUpdate = now

		for _, ac := range ss.aircraft {
			ac.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - tracon.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		}
	}

	ss.SpawnAircraft()
}

func (ss *Sim) Connected() bool {
	return true
}

func (ss *Sim) Callsign() string {
	if ss.scenario != nil {
		return ss.scenario.Callsign
	} else {
		return "(disconnected)"
	}
}

func (ss *Sim) CurrentTime() time.Time {
	return ss.currentTime
}

func (ss *Sim) GetWindowTitle() string {
	if ss.scenario == nil {
		return "(disconnected)"
	}
	remaining := fmt.Sprintf("[%d aircraft remaining]", ss.remainingLaunches)
	return ss.scenario.Callsign + ": " + ss.scenario.Name + " " + remaining
}

func pilotResponse(callsign string, fm string, args ...interface{}) {
	lg.Printf("%s: %s", callsign, fmt.Sprintf(fm, args...))
	eventStream.Post(&RadioTransmissionEvent{callsign: callsign, message: fmt.Sprintf(fm, args...)})
}

func (ss *Sim) AssignAltitude(callsign string, altitude int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if float32(altitude) > ac.Altitude {
			pilotResponse(callsign, "climb and maintain %d", altitude)
		} else if float32(altitude) == ac.Altitude {
			pilotResponse(callsign, "maintain %d", altitude)
		} else {
			pilotResponse(callsign, "descend and maintain %d", altitude)
		}

		ac.AssignedAltitude = altitude
		ac.CrossingAltitude = 0
		return nil
	}
}

func (ss *Sim) AssignHeading(callsign string, heading int, turn int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if turn > 0 {
			pilotResponse(callsign, "turn right heading %d", heading)
		} else if turn == 0 {
			pilotResponse(callsign, "fly heading %d", heading)
		} else {
			pilotResponse(callsign, "turn left heading %d", heading)
		}

		// A 0 heading shouldn't be specified, but at least cause the
		// aircraft to do what is intended, since 0 represents an
		// unassigned heading.
		if heading == 0 {
			heading = 360
		}

		ac.AssignedHeading = heading
		ac.TurnDirection = turn
		return nil
	}
}

func (ss *Sim) TurnLeft(callsign string, deg int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		pilotResponse(callsign, "turn left %d degrees", deg)

		if ac.AssignedHeading == 0 {
			ac.AssignedHeading = int(ac.Heading) - deg
		} else {
			ac.AssignedHeading -= deg
		}

		if ac.AssignedHeading <= 0 {
			ac.AssignedHeading += 360
		}
		ac.TurnDirection = 0
		return nil
	}
}

func (ss *Sim) TurnRight(callsign string, deg int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		pilotResponse(callsign, "turn right %d degrees", deg)

		if ac.AssignedHeading == 0 {
			ac.AssignedHeading = int(ac.Heading) + deg
		} else {
			ac.AssignedHeading += deg
		}

		if ac.AssignedHeading > 360 {
			ac.AssignedHeading -= 360
		}
		ac.TurnDirection = 0
		return nil
	}
}

func (ss *Sim) AssignSpeed(callsign string, speed int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if speed == 0 {
			pilotResponse(callsign, "cancel speed restrictions")
		} else if speed == ac.AssignedSpeed {
			pilotResponse(callsign, "we'll maintain %d knots", speed)
		} else {
			pilotResponse(callsign, "maintain %d knots", speed)
		}

		ac.AssignedSpeed = speed
		ac.CrossingSpeed = 0
		return nil
	}
}

func (ss *Sim) DirectFix(callsign string, fix string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		fix = strings.ToUpper(fix)

		// Look for the fix in the waypoints in the flight plan.
		for i, wp := range ac.Waypoints {
			if fix == wp.Fix {
				ac.Waypoints = ac.Waypoints[i:]
				if len(ac.Waypoints) > 0 {
					ac.WaypointUpdate(wp)
				}
				pilotResponse(callsign, "direct %s", fix)
				return nil
			}
		}

		if ac.Approach != nil {
			for _, route := range ac.Approach.Waypoints {
				for _, wp := range route {
					if wp.Fix == fix {
						ac.Waypoints = []Waypoint{wp}
						if len(ac.Waypoints) > 0 {
							ac.WaypointUpdate(wp)
						}
						pilotResponse(callsign, "direct %s", fix)
						return nil
					}
				}
			}
		}

		return fmt.Errorf("%s: fix not found in route", fix)
	}
}

func (ss *Sim) getApproach(callsign string, approach string) (*Approach, *Aircraft, error) {
	ac, ok := ss.aircraft[callsign]
	if !ok {
		return nil, nil, ErrNoAircraftForCallsign
	}
	fp := ac.FlightPlan
	if fp == nil {
		return nil, nil, ErrNoFlightPlan
	}

	ap, ok := tracon.Airports[fp.ArrivalAirport]
	if !ok {
		lg.Errorf("Can't find TRACON airport %s for %s approach for %s", fp.ArrivalAirport, approach, callsign)
		return nil, nil, ErrArrivalAirportUnknown
	}

	for i, appr := range ap.Approaches {
		if appr.ShortName == approach {
			return &ap.Approaches[i], ac, nil
		}
	}
	return nil, nil, ErrUnknownApproach
}

func (ss *Sim) ExpectApproach(callsign string, approach string) error {
	ap, ac, err := ss.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	ac.Approach = ap
	pilotResponse(callsign, "we'll expect the "+ap.FullName+" approach")

	return nil
}

func (ss *Sim) ClearedApproach(callsign string, approach string) error {
	ap, ac, err := ss.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	response := ""
	if ac.Approach == nil {
		// allow it anyway...
		response = "you never told us to expect an approach, but ok, cleared " + ap.FullName
		ac.Approach = ap
	}
	if ac.Approach.ShortName != approach {
		pilotResponse(callsign, "but you cleared us for the "+ac.Approach.FullName+" approach...")
		return ErrClearedForUnexpectedApproach
	}
	if ac.ClearedApproach {
		pilotResponse(callsign, "you already cleared us for the "+ap.FullName+" approach...")
		return nil
	}

	directApproachFix := false
	var remainingApproachWaypoints []Waypoint
	if ac.AssignedHeading == 0 && len(ac.Waypoints) > 0 {
		// Is the aircraft cleared direct to a waypoint on the approach?
		for _, approach := range ap.Waypoints {
			for i, wp := range approach {
				if wp.Fix == ac.Waypoints[0].Fix {
					directApproachFix = true
					if i+1 < len(approach) {
						remainingApproachWaypoints = approach[i+1:]
					}
					break
				}
			}
		}
	}

	if ac.Approach.Type == ILSApproach {
		if ac.AssignedHeading == 0 {
			if !directApproachFix {
				pilotResponse(callsign, "we need either direct or a heading to intercept")
				return nil
			} else {
				if remainingApproachWaypoints != nil {
					ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
				}
			}
		}
		// If the aircraft is on a heading, there's nothing more to do for
		// now; keep flying the heading and after we intercept we'll add
		// the rest of the waypoints to the aircraft's waypoints array.
	} else {
		// RNAV
		if !directApproachFix {
			pilotResponse(callsign, "we need direct to a fix on the approach...")
			return nil
		}

		if remainingApproachWaypoints != nil {
			ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
		}
	}

	// cleared approach cancels speed restrictions, but let's assume that
	// aircraft will just maintain their present speed and not immediately
	// accelerate up to 250...
	ac.AssignedSpeed = 0
	ac.CrossingSpeed = int(ac.IAS)
	ac.ClearedApproach = true

	pilotResponse(callsign, response+"cleared "+ap.FullName+" approach")
	return nil
}

func (ss *Sim) PrintInfo(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		lg.Errorf("%s", spew.Sdump(ac))
		s := fmt.Sprintf("%s: current alt %f, assigned alt %d crossing alt %d",
			ac.Callsign, ac.Altitude, ac.AssignedAltitude, ac.CrossingAltitude)
		if ac.AssignedHeading != 0 {
			s += fmt.Sprintf(" heading %d", ac.AssignedHeading)
			if ac.TurnDirection != 0 {
				s += fmt.Sprintf(" turn direction %d", ac.TurnDirection)
			}
		}
		s += fmt.Sprintf(", IAS %f GS %.1f speed %d crossing speed %d",
			ac.IAS, ac.GS, ac.AssignedSpeed, ac.CrossingSpeed)

		if ac.ClearedApproach {
			s += ", cleared approach"
		}
		if ac.OnFinal {
			s += ", on final"
		}
		lg.Errorf("%s", s)
	}
	return nil
}

func (ss *Sim) DeleteAircraft(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
		delete(ss.aircraft, callsign)
		return nil
	}
}

func (ss *Sim) Paused() bool {
	return ss.paused
}

func (ss *Sim) TogglePause() {
	ss.paused = !ss.paused
	ss.lastUpdateTime = time.Now() // ignore time passage...
}

func (ss *Sim) ActivateSettingsWindow() {
	ss.showSettings = true
}

func (ss *Sim) DrawSettingsWindow() {
	if !ss.showSettings {
		return
	}

	imgui.BeginV("Simulation Settings", &ss.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if *devmode {
		imgui.SliderFloatV("Simulation speed", &ss.simRate, 1, 100, "%.1f", 0)
	} else {
		imgui.SliderFloatV("Simulation speed", &ss.simRate, 1, 10, "%.1f", 0)
	}
	imgui.Separator()

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
	if imgui.CollapsingHeader("Audio") {
		globalConfig.Audio.DrawUI()
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if stars != nil && imgui.CollapsingHeader("STARS Radar Scope") {
		stars.DrawUI()
	}

	imgui.End()
}

func (s *Sim) GetWindVector(p Point2LL, alt float32) Point2LL {
	// TODO: have a better gust model?
	windKts := s.scenario.Wind.Speed
	if s.scenario.Wind.Gust > 0 {
		windKts += rand.Int31n(s.scenario.Wind.Gust)
	}

	// wind.dir is where it's coming from, so +180 to get the vector that
	// affects the aircraft's course.
	d := float32(s.scenario.Wind.Direction + 180)
	vWind := [2]float32{sin(radians(d)), cos(radians(d))}
	vWind = scale2f(vWind, float32(windKts)/3600)
	return nm2ll(vWind)
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func (ss *Sim) SpawnAircraft() {
	now := ss.CurrentTime()

	addAircraft := func(ac *Aircraft) {
		if _, ok := ss.aircraft[ac.Callsign]; ok {
			lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
			return
		}
		ss.aircraft[ac.Callsign] = ac

		ac.RunWaypointCommands(ac.Waypoints[0].Commands)

		ac.Position = ac.Waypoints[0].Location
		if ac.Position.IsZero() {
			lg.Errorf("%s: uninitialized initial waypoint position!", ac.Callsign)
			return
		}
		ac.Heading = float32(ac.Waypoints[0].Heading)
		if ac.Heading == 0 { // unassigned, so get the heading from the next fix
			ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, tracon.MagneticVariation)
		}
		ac.Waypoints = ac.Waypoints[1:]

		ss.remainingLaunches--
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

	for group, rates := range ss.scenario.ArrivalGroupRates {
		if ss.remainingLaunches > 0 && now.After(ss.scenario.nextArrivalSpawn[group]) {
			// Choose arrival airport randomly in proportion to the airport
			// arrival rates in the arrival group.
			rateSum := 0
			var arrivalAirport *Airport
			for airport, rate := range rates {
				rateSum += int(*rate)
				// Weighted reservoir sampling...
				if rand.Float32() < float32(int(*rate))/float32(rateSum) {
					arrivalAirport = tracon.Airports[airport]
				}
			}

			if ac := ss.SpawnArrival(arrivalAirport, group); ac != nil {
				ac.FlightPlan.ArrivalAirport = arrivalAirport.ICAO
				addAircraft(ac)
				ss.scenario.nextArrivalSpawn[group] = now.Add(randomWait(rateSum))
			}
		}
	}

	for rwy, nextSpawn := range ss.scenario.nextDepartureSpawn {
		if ss.remainingLaunches > 0 && now.After(nextSpawn) {
			// So we're going to launch from this runway but there may be
			// multiple configs with different rates on this runway. So now
			// we'll choose one with probability proportional to its
			// rate...
			idx := SampleWeighted(ss.scenario.DepartureRunways,
				func(r ScenarioDepartureRunway) int {
					if r.Airport+"/"+r.Runway != rwy {
						return 0
					}
					return int(r.Rate)
				})
			if idx == -1 {
				lg.Errorf("%s: couldn't find a matching runway for spawning departure?", rwy)
				continue
			}

			ap := tracon.Airports[ss.scenario.DepartureRunways[idx].Airport]
			if ac := ss.SpawnDeparture(ap, &ss.scenario.DepartureRunways[idx]); ac != nil {
				ac.FlightPlan.DepartureAirport = ap.ICAO
				addAircraft(ac)
				ss.scenario.nextDepartureSpawn[rwy] = now.Add(randomWait(ss.scenario.runwayDepartureRate(rwy)))
			}
		}
	}
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

func (ss *Sim) SpawnArrival(ap *Airport, arrivalGroup string) *Aircraft {
	arrivals := tracon.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := SampleFiltered(arrivals, func(ar Arrival) bool {
		_, ok := ar.Airlines[ap.ICAO]
		return ok
	})
	if idx == -1 {
		lg.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, ap.ICAO)
		return nil
	}
	arr := arrivals[idx]

	airline := Sample(arr.Airlines[ap.ICAO])
	ac := sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil
	}

	ac.FlightPlan.DepartureAirport = airline.Airport
	ac.FlightPlan.ArrivalAirport = ap.ICAO
	ac.TrackingController = arr.InitialController
	ac.FlightPlan.Altitude = 39000
	ac.FlightPlan.Route = arr.Route
	// Start with the default waypoints for the arrival
	ac.Waypoints = arr.Waypoints
	// But if there is a custom route for any of the active runways, switch
	// to that. Results are undefined if there are multiple matches.
	for _, aprwy := range ss.scenario.ArrivalRunways {
		if wp, ok := arr.RunwayWaypoints[aprwy.Runway]; ok {
			ac.Waypoints = wp
			break
		}
	}
	ac.Altitude = float32(arr.InitialAltitude)
	ac.IAS = float32(arr.InitialSpeed)
	ac.CrossingAltitude = arr.ClearedAltitude
	ac.CrossingSpeed = arr.SpeedRestriction
	ac.Scratchpad = arr.Scratchpad
	if arr.ExpectApproach != "" {
		for i, appr := range tracon.Airports[ac.FlightPlan.ArrivalAirport].Approaches {
			if appr.ShortName == arr.ExpectApproach {
				ac.Approach = &ap.Approaches[i]
			}
		}
		if ac.Approach == nil {
			lg.Errorf("%s: unable to find expected %s approach", ac.Callsign, arr.ExpectApproach)
		}
	}

	return ac
}

func (ss *Sim) SpawnDeparture(ap *Airport, rwy *ScenarioDepartureRunway) *Aircraft {
	var dep *Departure
	if rand.Float32() < ss.challenge {
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
				lg.Errorf("%s/%s: unable to find a valid departure", ap.ICAO, rwy.Runway)
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
			lg.Errorf("%s/%s: unable to find a valid departure", ap.ICAO, rwy.Runway)
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
	ac.Scratchpad = tracon.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		// If unspecified, pick something in the flight levels...
		// TODO: get altitudes right considering East/West-bound...
		ac.FlightPlan.Altitude = 28000 + 1000*rand.Intn(13)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	ac.Altitude = float32(ap.Elevation)
	ac.AssignedAltitude = exitRoute.ClearedAltitude

	return ac
}

///////////////////////////////////////////////////////////////////////////

var (
	//go:embed configs/jfk.json
	znyJSON string
	//go:embed configs/zny-maps.json.zst
	znyMapsJSON string
)

func LoadZNY(traconFilename string) *TRACON {
	var t TRACON
	var err error

	traconJSON := []byte(znyJSON)
	if traconFilename != "" {
		var f []byte
		if f, err = os.ReadFile(traconFilename); err != nil {
			lg.Errorf("%s: %v", traconFilename, err)
			os.Exit(1)
		} else {
			traconJSON = f
		}
	}

	if err := json.Unmarshal(traconJSON, &t); err != nil {
		decodeOffset := func(offset int64) (line, char int) {
			line, char = 1, 1
			for i := 0; i < int(offset) && i < len(traconJSON); i++ {
				if traconJSON[i] == '\n' {
					line++
					char = 1
				} else {
					char++
				}
			}
			return
		}

		switch jerr := err.(type) {
		case *json.SyntaxError:
			line, char := decodeOffset(jerr.Offset)
			lg.Errorf("Error at line %d, character %d of TRACON JSON: %v", line, char, jerr)

		case *json.UnmarshalTypeError:
			line, char := decodeOffset(jerr.Offset)
			lg.Errorf("Error at line %d, character %d of TRACON JSON: %s value for %s.%s invalid for type %s",
				line, char, jerr.Value, jerr.Struct, jerr.Field, jerr.Type.String())

		default:
			lg.Errorf("%s: %v", traconFilename, err)
		}
		os.Exit(1)
	}

	var maps map[string][]Point2LL
	if err := json.Unmarshal([]byte(decompressZstd(znyMapsJSON)), &maps); err != nil {
		panic(err)
	}

	t.VideoMaps = make(map[string]*VideoMap)
	for name, segs := range maps {
		vm := &VideoMap{Name: name, Segments: segs}
		vm.InitializeCommandBuffer()
		t.VideoMaps[name] = vm
	}

	return &t
}

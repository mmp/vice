// simserver.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

var (
	ErrArrivalAirportUnknown        = errors.New("Arrival airport unknown")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrNotOnApproachCourse          = errors.New("Not on approach course")
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
)

type Simulator interface {
	AssignAltitude(callsign string, altitude int) error
	AssignHeading(callsign string, heading int) error
	AssignSpeed(callsign string, kts int) error
	DirectFix(callsign string, fix string) error
	ExpectApproach(callsign string, approach string) error
	ClearedApproach(callsign string, approach string) error

	PrintInfo(callsign string) error
	DeleteAircraft(callsign string) error
	TogglePause() error
}

func mustParseLatLong(l string) Point2LL {
	ll, err := ParseLatLong(l)
	if err != nil {
		panic(l + ": " + err.Error())
	}
	return ll
}

/*
type NewScenario struct [
	Callsign string
	Controllers []*Controller
	Airports map[string]string // ICAO ->
}
*/

type Scenario struct {
	Callsign    string           `json:"callsign"`
	Controllers []*Controller    `json:"controllers"`
	Airports    []*AirportConfig `json:"airports"`
}

type AirportConfig struct {
	ICAO string `json:"ICAO"`

	NamedLocations map[string]Point2LL `json:"named_locations"`

	ArrivalGroups []ArrivalGroup `json:"arrival_groups"`
	Approaches    []Approach     `json:"approaches"`
	Departures    []Departure    `json:"departures"`

	ExitCategories map[string]string `json:"exit_categories"`

	Scratchpads map[string]string `json:"scratchpads"`

	DepartureRunways   []DepartureRunway `json:"departure_runways"`
	ArrivalRunwayNames []string          `json:"arrival_runways"`
	ArrivalRunways     []ArrivalRunway   `json:"-"`
}

func (ac *AirportConfig) PostDeserialize() []error {
	var errors []error

	for _, rwy := range ac.ArrivalRunwayNames {
		ac.ArrivalRunways = append(ac.ArrivalRunways, ArrivalRunway{Runway: rwy})
	}

	approachNames := make(map[string]interface{})
	for _, ap := range ac.Approaches {
		if _, ok := approachNames[ap.ShortName]; ok {
			errors = append(errors, fmt.Errorf("%s: multiple approaches with this short name", ap.ShortName))
		}
		approachNames[ap.ShortName] = nil

		for i := range ap.Waypoints {
			n := len(ap.Waypoints[i])
			ap.Waypoints[i][n-1].Commands = append(ap.Waypoints[i][n-1].Commands, WaypointCommandDelete)

			errors = append(errors, ac.InitializeWaypointLocations(ap.Waypoints[i])...)
		}
	}

	checkAirlines := func(airlines []AirlineConfig) {
		for i := range airlines {
			al, ok := database.Airlines[airlines[i].ICAO]
			if !ok {
				errors = append(errors, fmt.Errorf("%s: airline not in database", airlines[i].ICAO))
			}

			if airlines[i].Fleet == "" {
				airlines[i].Fleet = "default"
			}

			fleet, ok := al.Fleets[airlines[i].Fleet]
			if !ok {
				errors = append(errors,
					fmt.Errorf("%s: fleet unknown for airline \"%s\"", airlines[i].Fleet, airlines[i].ICAO))
			}

			for _, aircraft := range fleet {
				_, ok := database.AircraftPerformance[aircraft.ICAO]
				if !ok {
					errors = append(errors,
						fmt.Errorf("%s: aircraft in airline \"%s\"'s fleet \"%s\" not in perf database",
							aircraft.ICAO, airlines[i].ICAO, airlines[i].Fleet))
				}
			}
		}
	}

	for _, ag := range ac.ArrivalGroups {
		if len(ag.Arrivals) == 0 {
			errors = append(errors, fmt.Errorf("%s: no arrivals in arrival group", ag.Name))
		}

		for _, ar := range ag.Arrivals {
			errors = append(errors, ac.InitializeWaypointLocations(ar.Waypoints)...)
			for _, wp := range ar.RunwayWaypoints {
				errors = append(errors, ac.InitializeWaypointLocations(wp)...)
			}

			checkAirlines(ar.Airlines)
		}
	}

	for i, dep := range ac.Departures {
		wp := []Waypoint{Waypoint{Fix: dep.Exit}}
		errors = append(errors, ac.InitializeWaypointLocations(wp)...)
		ac.Departures[i].exitWaypoint = wp[0]

		checkAirlines(dep.Airlines)
	}

	runwayNames := make(map[string]interface{})
	for i, rwy := range ac.DepartureRunways {
		ac.DepartureRunways[i].departureCategoryEnabled = make(map[string]*bool)

		if _, ok := runwayNames[rwy.Runway]; ok {
			errors = append(errors, fmt.Errorf("%s: multiple runway definitions", rwy.Runway))
		}
		runwayNames[rwy.Runway] = nil

		for _, er := range rwy.ExitRoutes {
			errors = append(errors, ac.InitializeWaypointLocations(er.Waypoints)...)
		}

		for _, cat := range ac.ExitCategories {
			// This is sort of wasteful, but...
			ac.DepartureRunways[i].departureCategoryEnabled[cat] = new(bool)
		}
	}

	return errors
}

func (ac *AirportConfig) InitializeWaypointLocations(waypoints []Waypoint) []error {
	var prev Point2LL
	var errors []error

	for i, wp := range waypoints {
		if pos, ok := database.Locate(wp.Fix); ok {
			waypoints[i].Location = pos
		} else if pos, ok := ac.NamedLocations[wp.Fix]; ok {
			waypoints[i].Location = pos
		} else if pos, err := ParseLatLong(wp.Fix); err == nil {
			waypoints[i].Location = pos
		} else {
			errors = append(errors, fmt.Errorf("%s: unable to locate waypoint", wp.Fix))
		}

		d := nmdistance2ll(prev, waypoints[i].Location)
		if i > 1 && d > 25 {
			errors = append(errors, fmt.Errorf("%s: waypoint is suspiciously far from previous one: %f nm",
				wp.Fix, d))
		}
		prev = waypoints[i].Location
	}
	return errors
}

type DepartureRunway struct {
	Runway     string               `json:"runway"`
	Altitude   int                  `json:"altitude"`
	Enabled    bool                 `json:"-"`
	Rate       int32                `json:"rate"`
	ExitRoutes map[string]ExitRoute `json:"exit_routes"`

	departureCategoryEnabled map[string]*bool
	nextSpawn                time.Time
	lastDeparture            *Departure
}

type ExitRoute struct {
	InitialRoute    string        `json:"route"`
	ClearedAltitude int           `json:"cleared_altitude"`
	Waypoints       WaypointArray `json:"waypoints"`
}

type ArrivalRunway struct {
	Runway  string `json:"runway"`
	Enabled bool   `json:"-"`
}

type Departure struct {
	Exit         string `json:"exit"`
	exitWaypoint Waypoint

	Destination string          `json:"destination"`
	Altitude    int             `json:"altitude,omitempty"`
	Route       string          `json:"route"`
	Airlines    []AirlineConfig `json:"airlines"`
}

type ArrivalGroup struct {
	Name     string    `json:"name"`
	Rate     int32     `json:"rate"`
	Enabled  bool      `json:"-"`
	Arrivals []Arrival `json:"arrivals"`

	nextSpawn time.Time
}

type Arrival struct {
	Name            string                   `json:"name"`
	Waypoints       WaypointArray            `json:"waypoints"`
	RunwayWaypoints map[string]WaypointArray `json:"runway_waypoints"`
	Route           string                   `json:"route"`

	InitialController string `json:"initial_controller"`
	InitialAltitude   int    `json:"initial_altitude"`
	ClearedAltitude   int    `json:"cleared_altitude"`
	InitialSpeed      int    `json:"initial_speed"`
	SpeedRestriction  int    `json:"speed_restriction"`

	Airlines []AirlineConfig `json:"airlines"`
}

// for a single departure / arrival
type AirlineConfig struct {
	ICAO    string `json:"icao"`
	Airport string `json:"airport,omitempty"`
	Fleet   string `json:"fleet"`
}

type ApproachType int

const (
	ILSApproach = iota
	RNAVApproach
)

func (at ApproachType) MarshalJSON() ([]byte, error) {
	switch at {
	case ILSApproach:
		return []byte("\"ILS\""), nil
	case RNAVApproach:
		return []byte("\"RNAV\""), nil
	default:
		return nil, fmt.Errorf("unhandled approach type in MarshalJSON()")
	}
}

func (at *ApproachType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"ILS\"":
		*at = ILSApproach
		return nil

	case "\"RNAV\"":
		*at = RNAVApproach
		return nil

	default:
		return fmt.Errorf("%s: unknown approach_type", string(b))
	}
}

type Approach struct {
	ShortName string          `json:"short_name"`
	FullName  string          `json:"full_name"`
	Type      ApproachType    `json:"type"`
	Waypoints []WaypointArray `json:"waypoints"`
}

func (ap *Approach) Line() [2]Point2LL {
	// assume we have at least one set of waypoints and that it has >= 2 waypoints!
	wp := ap.Waypoints[0]

	// use the last two waypoints
	n := len(wp)
	return [2]Point2LL{wp[n-2].Location, wp[n-1].Location}
}

func (ap *Approach) Heading() int {
	p := ap.Line()
	return int(headingp2ll(p[0], p[1], database.MagneticVariation) + 0.5)
}

type WaypointCommand int

const (
	WaypointCommandHandoff = iota
	WaypointCommandDelete
)

func (wc WaypointCommand) MarshalJSON() ([]byte, error) {
	switch wc {
	case WaypointCommandHandoff:
		return []byte("\"handoff\""), nil

	case WaypointCommandDelete:
		return []byte("\"delete\""), nil

	default:
		return nil, fmt.Errorf("unhandled WaypointCommand in MarshalJSON")
	}
}

func (wc *WaypointCommand) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"handoff\"":
		*wc = WaypointCommandHandoff
		return nil

	case "\"delete\"":
		*wc = WaypointCommandDelete
		return nil

	default:
		return fmt.Errorf("%s: unknown waypoint command", string(b))
	}
}

type Waypoint struct {
	Fix      string            `json:"fix"`
	Location Point2LL          `json:"-"` // never serialized, derived from fix
	Altitude int               `json:"altitude,omitempty"`
	Speed    int               `json:"speed,omitempty"`
	Heading  int               `json:"heading,omitempty"` // outbound heading after waypoint
	Commands []WaypointCommand `json:"commands,omitempty"`
}

func (wp *Waypoint) ETA(p Point2LL, gs float32) time.Duration {
	dist := nmdistance2ll(p, wp.Location)
	eta := dist / gs
	return time.Duration(eta * float32(time.Hour))
}

type WaypointArray []Waypoint

func (wslice WaypointArray) MarshalJSON() ([]byte, error) {
	var entries []string
	for _, w := range wslice {
		s := w.Fix
		if w.Altitude != 0 {
			s += fmt.Sprintf("@a%d", w.Altitude)
		}
		if w.Speed != 0 {
			s += fmt.Sprintf("@s%d", w.Speed)
		}
		entries = append(entries, s)

		if w.Heading != 0 {
			entries = append(entries, fmt.Sprintf("#%d", w.Heading))
		}

		for _, c := range w.Commands {
			switch c {
			case WaypointCommandHandoff:
				entries = append(entries, "@")

			case WaypointCommandDelete:
				entries = append(entries, "*")
			}
		}
	}

	return []byte("\"" + strings.Join(entries, " ") + "\""), nil
}

func (w *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		*w = nil
		return nil
	}
	wp, err := parseWaypoints(string(b[1 : len(b)-1]))
	if err == nil {
		*w = wp
	}
	return err
}

var scenarios []*Scenario

type SimServerConnectionConfiguration struct {
	numAircraft      int32
	controllerActive map[string]*bool
	challenge        float32

	wind struct {
		dir   int32
		speed int32
		gust  int32
	}

	scenario *Scenario
}

func (ssc *SimServerConnectionConfiguration) Initialize() {
	if len(scenarios) == 0 {
		scenarios = append(scenarios, JFKApproachScenario())
		/*
			e := json.NewEncoder(os.Stdout)
			err := e.Encode(scenarios)
			if err != nil {
				panic(err)
			}
		*/
	}
	for _, sc := range scenarios {
		sort.Slice(sc.Controllers, func(i, j int) bool { return sc.Controllers[i].Callsign < sc.Controllers[j].Callsign })

		for _, ap := range sc.Airports {
			if errors := ap.PostDeserialize(); len(errors) > 0 {
				for _, err := range errors {
					lg.Errorf("%s: error in specification: %v", ap.ICAO, err)
				}
			}
		}
	}

	ssc.numAircraft = 30
	ssc.wind.dir = 50
	ssc.wind.speed = 10
	ssc.wind.gust = 15

	ssc.challenge = 0.25

	ssc.scenario = scenarios[0]

	ssc.controllerActive = make(map[string]*bool)
	for _, ctrl := range ssc.scenario.Controllers {
		ssc.controllerActive[ctrl.Callsign] = new(bool)
		*ssc.controllerActive[ctrl.Callsign] = true
	}
}

func (ssc *SimServerConnectionConfiguration) DrawUI() bool {
	// imgui.InputText("Callsign", &ssc.callsign)
	imgui.SliderIntV("Total aircraft", &ssc.numAircraft, 1, 100, "%d", 0)

	imgui.TableNextColumn()
	imgui.SliderFloatV("Departure sequencing challenge", &ssc.challenge, 0, 1, "%.01f", 0)

	imgui.SliderIntV("Wind heading", &ssc.wind.dir, 0, 360, "%d", 0)
	imgui.SliderIntV("Wind speed", &ssc.wind.speed, 0, 50, "%d", 0)
	imgui.SliderIntV("Wind gust", &ssc.wind.gust, 0, 50, "%d", 0)
	ssc.wind.gust = max(ssc.wind.gust, ssc.wind.speed)

	if imgui.CollapsingHeader("Controllers") {
		for _, ctrl := range ssc.scenario.Controllers {
			imgui.Checkbox(ctrl.Callsign, ssc.controllerActive[ctrl.Callsign])
		}
	}

	for i, apConfig := range ssc.scenario.Airports {
		var headerFlags imgui.TreeNodeFlags
		if i == 0 {
			headerFlags = imgui.TreeNodeFlagsDefaultOpen
		}
		if !imgui.CollapsingHeaderV(apConfig.ICAO, headerFlags) {
			continue
		}

		apConfig.DrawUI()
	}

	return false
}

func (ac *AirportConfig) DrawUI() {
	if len(ac.Departures) > 0 {
		imgui.Text("Departures")
		anyRunwaysActive := false
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

		if imgui.BeginTableV("departureRunways", 3, flags, imgui.Vec2{400, 0}, 0.) {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Enabled")
			imgui.TableSetupColumn("ADR")
			imgui.TableHeadersRow()

			for i, conf := range ac.DepartureRunways {
				imgui.PushID(conf.Runway)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(conf.Runway)
				imgui.TableNextColumn()
				if imgui.Checkbox("##enabled", &ac.DepartureRunways[i].Enabled) {
					if ac.DepartureRunways[i].Enabled {
						// enable all corresponding categories by default
						for _, enabled := range conf.departureCategoryEnabled {
							*enabled = true
						}
					} else {
						// disable all corresponding configs
						for _, enabled := range conf.departureCategoryEnabled {
							*enabled = false
						}
					}
				}
				anyRunwaysActive = anyRunwaysActive || ac.DepartureRunways[i].Enabled
				imgui.TableNextColumn()
				imgui.InputIntV("##adr", &ac.DepartureRunways[i].Rate, 1, 120, 0)
				imgui.PopID()
			}
			imgui.EndTable()
		}

		if anyRunwaysActive {
			flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("configs", 2, flags, imgui.Vec2{500, 0}, 0.) {
				imgui.TableSetupColumn("Departure Runway/Gate")
				imgui.TableSetupColumn("Enabled")
				imgui.TableHeadersRow()
				for _, conf := range ac.DepartureRunways {
					if !conf.Enabled {
						continue
					}

					imgui.PushID(conf.Runway)
					for _, category := range SortedMapKeys(conf.departureCategoryEnabled) {
						imgui.PushID(category)
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Text(conf.Runway + "/" + category)
						imgui.TableNextColumn()
						imgui.Checkbox("##check", conf.departureCategoryEnabled[category])
						imgui.PopID()
					}
					imgui.PopID()
				}
				imgui.EndTable()
			}
		}
	}

	if len(ac.ArrivalRunways) > 0 {
		imgui.Text("Arrivals")

		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		anyRunwaysActive := false
		if imgui.BeginTableV("arrivalrunways", 2, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Enabled")
			imgui.TableHeadersRow()

			for i, rwy := range ac.ArrivalRunways {
				imgui.PushID(rwy.Runway)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(rwy.Runway)
				imgui.TableNextColumn()
				imgui.Checkbox("##enabled", &ac.ArrivalRunways[i].Enabled)
				if ac.ArrivalRunways[i].Enabled {
					anyRunwaysActive = true
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}

		if anyRunwaysActive && len(ac.ArrivalGroups) > 0 {
			if imgui.BeginTableV("arrivalgroups", 3, flags, imgui.Vec2{500, 0}, 0.) {
				imgui.TableSetupColumn("Arrival")
				imgui.TableSetupColumn("Enabled")
				imgui.TableSetupColumn("AAR")
				imgui.TableHeadersRow()

				for i, ag := range ac.ArrivalGroups {
					imgui.PushID(ag.Name)
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(ag.Name)
					imgui.TableNextColumn()
					imgui.Checkbox("##enabled", &ac.ArrivalGroups[i].Enabled)
					imgui.TableNextColumn()
					imgui.InputIntV("##aar", &ac.ArrivalGroups[i].Rate, 1, 120, 0)
					imgui.PopID()
				}
				imgui.EndTable()
			}
		}
	}
}

func (ssc *SimServerConnectionConfiguration) Valid() bool {
	// Make sure that at least one scenario is selected
	for _, ap := range ssc.scenario.Airports {
		for _, rwy := range ap.DepartureRunways {
			if !rwy.Enabled {
				continue
			}
			for _, active := range rwy.departureCategoryEnabled {
				if *active {
					return true
				}
			}
		}

		for _, a := range ap.ArrivalGroups {
			if a.Enabled {
				return true
			}
		}
	}

	return false
}

func (ssc *SimServerConnectionConfiguration) Connect() error {
	// Send out events to remove any existing aircraft (necessary for when
	// we restart...)
	for _, ac := range server.GetAllAircraft() {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	server = NewSimServer(*ssc)
	return nil
}

func (ss *SimServer) RunWaypointCommands(ac *Aircraft, cmds []WaypointCommand) {
	for _, cmd := range cmds {
		switch cmd {
		case WaypointCommandHandoff:
			// Handoff to the user's position?
			ac.InboundHandoffController = ss.callsign
			eventStream.Post(&OfferedHandoffEvent{controller: ac.TrackingController, ac: ac})

		case WaypointCommandDelete:
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
			delete(ss.aircraft, ac.Callsign)
			return
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// SimServer

type SimServer struct {
	// These come from the scenario
	callsign       string
	airportConfigs []*AirportConfig
	controllers    map[string]*Controller

	aircraft map[string]*Aircraft
	handoffs map[string]time.Time
	metar    map[string]*METAR

	currentTime       time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime    time.Time // this is w.r.t. true wallclock time
	simRate           float32
	paused            bool
	remainingLaunches int

	wind struct {
		dir   int
		speed int
		gust  int
	}
	challenge float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	showSettings bool
}

func NewSimServer(ssc SimServerConnectionConfiguration) *SimServer {
	rand.Seed(time.Now().UnixNano())

	ss := &SimServer{
		callsign:       ssc.scenario.Callsign,
		airportConfigs: ssc.scenario.Airports,
		controllers:    make(map[string]*Controller),

		aircraft: make(map[string]*Aircraft),
		handoffs: make(map[string]time.Time),
		metar:    make(map[string]*METAR),

		currentTime:       time.Now(),
		lastUpdateTime:    time.Now(),
		remainingLaunches: int(ssc.numAircraft),
		simRate:           1,
		challenge:         ssc.challenge,
	}

	for _, ctrl := range ssc.scenario.Controllers {
		if *ssc.controllerActive[ctrl.Callsign] {
			ss.controllers[ctrl.Callsign] = ctrl
		}
	}

	ss.wind.dir = int(ssc.wind.dir)
	ss.wind.speed = int(ssc.wind.speed)
	ss.wind.gust = int(ssc.wind.gust)

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)
	for _, ap := range ss.airportConfigs {
		spd := ss.wind.speed - 3 + rand.Intn(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((ss.wind.dir + 5) / 10)
			dir += [3]int{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := ss.wind.gust - 3 + rand.Intn(6)
			if gst-ss.wind.speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		ss.metar[ap.ICAO] = &METAR{
			AirportICAO: ap.ICAO,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	const initialSimSeconds = 45
	randomSpawn := func(rate int) time.Time {
		delta := rand.Intn(rate) - rate/2 - initialSimSeconds
		return time.Now().Add(time.Duration(delta) * time.Second)
	}
	for _, ap := range ss.airportConfigs {
		for i := range ap.ArrivalGroups {
			ap.ArrivalGroups[i].nextSpawn = randomSpawn(int(ap.ArrivalGroups[i].Rate))
		}
		for i := range ap.DepartureRunways {
			ap.DepartureRunways[i].nextSpawn = randomSpawn(int(ap.DepartureRunways[i].Rate))
		}
	}

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	for i := 0; i < initialSimSeconds; i++ {
		ss.currentTime = t
		ss.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		ss.updateState()
	}
	ss.currentTime = time.Now()
	ss.lastUpdateTime = time.Now()

	return ss
}

func (ss *SimServer) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetScratchpad(callsign string, scratchpad string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return nil
	}
}

func (ss *SimServer) SetTemporaryAltitude(callsign string, alt int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) PushFlightStrip(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) InitiateTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = ss.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&InitiatedTrackEvent{ac: ac})
		return nil
	}
}

func (ss *SimServer) DropTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.TrackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&DroppedTrackEvent{ac: ac})
		return nil
	}
}

func (ss *SimServer) Handoff(callsign string, controller string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := ss.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		eventStream.Post(&OfferedHandoffEvent{controller: ss.callsign, ac: ac})
		acceptDelay := 2 + rand.Intn(10)
		ss.handoffs[callsign] = ss.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (ss *SimServer) AcceptHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.InboundHandoffController != ss.callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.InboundHandoffController = ""
		ac.TrackingController = ss.callsign
		eventStream.Post(&AcceptedHandoffEvent{controller: ss.callsign, ac: ac})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac}) // FIXME...
		return nil
	}
}

func (ss *SimServer) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) CancelHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController != ss.callsign {
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

func (ss *SimServer) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SendTextMessage(m TextMessage) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) RequestControllerATIS(controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) Disconnect() {
	for _, ac := range ss.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
}

func (ss *SimServer) GetAircraft(callsign string) *Aircraft {
	if ac, ok := ss.aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (ss *SimServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range ss.aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (ss *SimServer) GetAllAircraft() []*Aircraft {
	return ss.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (ss *SimServer) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := ss.aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (ss *SimServer) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetMETAR(location string) *METAR {
	return ss.metar[location]
}

func (ss *SimServer) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetController(callsign string) *Controller {
	if ctrl, ok := ss.controllers[callsign]; ok {
		return ctrl
	}
	for _, ctrl := range ss.controllers {
		if pos := ctrl.GetPosition(); pos != nil && pos.SectorId == callsign {
			return ctrl
		}
	}
	return nil
}

func (ss *SimServer) GetAllControllers() []*Controller {
	_, ctrl := FlattenMap(ss.controllers)
	return ctrl
}

func (ss *SimServer) SetPrimaryFrequency(f Frequency) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetUpdates() {
	if ss.paused {
		return
	}

	// Update the current time
	elapsed := time.Since(ss.lastUpdateTime)
	elapsed = time.Duration(ss.simRate * float32(elapsed))
	ss.currentTime = ss.currentTime.Add(elapsed)
	ss.lastUpdateTime = time.Now()

	ss.updateState()
}

// FIXME: this is poorly named...
func (ss *SimServer) updateState() {
	// Accept any handoffs whose time has time...
	now := ss.CurrentTime()
	for callsign, t := range ss.handoffs {
		if now.After(t) {
			ac := ss.aircraft[callsign]
			ac.TrackingController = ac.OutboundHandoffController
			ac.OutboundHandoffController = ""
			eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
			globalConfig.AudioSettings.HandleEvent(AudioEventHandoffAccepted)
			delete(ss.handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(ss.lastSimUpdate) >= time.Second {
		ss.lastSimUpdate = now
		ss.updateSim()
	}

	// Add a new radar track every 5 seconds.
	if now.Sub(ss.lastTrackUpdate) >= 5*time.Second {
		ss.lastTrackUpdate = now

		for _, ac := range ss.aircraft {
			ac.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - database.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		}
	}

	ss.SpawnAircraft()
}

func (ss *SimServer) updateSim() {
	for _, ac := range ss.aircraft {
		ac.UpdateAirspeed()
		ac.UpdateAltitude()
		ac.UpdateHeading()
		ac.UpdatePositionAndGS(ss)
		ac.UpdateWaypoints(ss)
	}
}

func (ss *SimServer) Connected() bool {
	return true
}

func (ss *SimServer) Callsign() string {
	return ss.callsign
}

func (ss *SimServer) CurrentTime() time.Time {
	return ss.currentTime
}

func (ss *SimServer) GetWindowTitle() string {
	return "SimServer: " + ss.callsign
}

func pilotResponse(callsign string, fm string, args ...interface{}) {
	tm := TextMessage{
		sender:      callsign,
		messageType: TextBroadcast,
		contents:    fmt.Sprintf(fm, args...),
	}
	eventStream.Post(&TextMessageEvent{message: &tm})
}

func (ss *SimServer) AssignAltitude(callsign string, altitude int) error {
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
		return nil
	}
}

func (ss *SimServer) AssignHeading(callsign string, heading int, turn int) error {
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

func (ss *SimServer) AssignSpeed(callsign string, speed int) error {
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
		return nil
	}
}

func (ss *SimServer) DirectFix(callsign string, fix string) error {
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

func (ss *SimServer) getApproach(callsign string, approach string) (*Approach, *Aircraft, error) {
	ac, ok := ss.aircraft[callsign]
	if !ok {
		return nil, nil, ErrNoAircraftForCallsign
	}
	fp := ac.FlightPlan
	if fp == nil {
		return nil, nil, ErrNoFlightPlan
	}

	for _, ap := range ss.airportConfigs {
		if ap.ICAO == fp.ArrivalAirport {
			for i, appr := range ap.Approaches {
				if appr.ShortName == approach {
					return &ap.Approaches[i], ac, nil
				}
			}
			lg.Errorf("wanted approach %s; airport %s options -> %+v", approach, ap.ICAO, ap.Approaches)
			return nil, nil, ErrUnknownApproach
		}
	}
	return nil, nil, ErrArrivalAirportUnknown
}

func (ss *SimServer) ExpectApproach(callsign string, approach string) error {
	ap, ac, err := ss.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	ac.Approach = ap
	pilotResponse(callsign, "we'll expect the "+ap.FullName+" approach")

	return nil
}

func (ss *SimServer) ClearedApproach(callsign string, approach string) error {
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
		pilotResponse(callsign, "but you cleared us for the "+ap.FullName+" approach...")
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

	ac.AssignedSpeed = 0 // cleared approach cancels speed restrictions
	ac.ClearedApproach = true

	pilotResponse(callsign, response+"cleared "+ap.FullName+" approach")
	return nil
}

func (ss *SimServer) PrintInfo(callsign string) error {
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

func (ss *SimServer) DeleteAircraft(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
		delete(ss.aircraft, callsign)
		return nil
	}
}

func (ss *SimServer) Paused() bool {
	return ss.paused
}

func (ss *SimServer) TogglePause() error {
	ss.paused = !ss.paused
	ss.lastUpdateTime = time.Now() // ignore time passage...
	return nil
}

func (ss *SimServer) ActivateSettingsWindow() {
	ss.showSettings = true
}

func (ss *SimServer) DrawSettingsWindow() {
	if !ss.showSettings {
		return
	}

	imgui.BeginV("Simulation Settings", &ss.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	imgui.SliderFloatV("Simulation speed", &ss.simRate, 1, 10, "%.1f", 0)
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
		globalConfig.AudioSettings.DrawUI()
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if stars != nil && imgui.CollapsingHeader("STARS Radar Scope") {
		stars.DrawUI()
	}

	imgui.End()
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func (ss *SimServer) SpawnAircraft() {
	now := ss.CurrentTime()

	addAircraft := func(ac *Aircraft) {
		if _, ok := ss.aircraft[ac.Callsign]; ok {
			lg.Errorf("%s: already have an aircraft with that callsign!", ac.Callsign)
			return
		}
		ss.aircraft[ac.Callsign] = ac

		ss.RunWaypointCommands(ac, ac.Waypoints[0].Commands)

		ac.Position = ac.Waypoints[0].Location
		if ac.Position.IsZero() {
			lg.Errorf("%s: uninitialized initial waypoint position!", ac.Callsign)
			return
		}
		ac.Heading = float32(ac.Waypoints[0].Heading)
		if ac.Heading == 0 { // unassigned, so get the heading from the next fix
			ac.Heading = headingp2ll(ac.Position, ac.Waypoints[1].Location, database.MagneticVariation)
		}
		ac.Waypoints = ac.Waypoints[1:]

		lg.Errorf("Added aircraft: %s", spew.Sdump(ac))

		ss.remainingLaunches--
		eventStream.Post(&AddedAircraftEvent{ac: ac})
	}

	randomWait := func(rate int32) time.Duration {
		avgSeconds := 3600 / float32(rate)
		seconds := lerp(rand.Float32(), .7*avgSeconds, 1.3*avgSeconds)
		return time.Duration(seconds * float32(time.Second))
	}

	for _, ap := range ss.airportConfigs {
		for i, arr := range ap.ArrivalGroups {
			if arr.Enabled && ss.remainingLaunches > 0 && now.After(arr.nextSpawn) {
				if ac := ss.SpawnArrival(ap, arr); ac != nil {
					ac.FlightPlan.ArrivalAirport = ap.ICAO
					addAircraft(ac)
					ap.ArrivalGroups[i].nextSpawn = now.Add(randomWait(arr.Rate))
				}
			}
		}

		for i, rwy := range ap.DepartureRunways {
			if rwy.Enabled && ss.remainingLaunches > 0 && now.After(rwy.nextSpawn) {
				if ac := ss.SpawnDeparture(ap, &ap.DepartureRunways[i]); ac != nil {
					ac.FlightPlan.DepartureAirport = ap.ICAO
					addAircraft(ac)
					ap.DepartureRunways[i].nextSpawn = now.Add(randomWait(rwy.Rate))
				}
			}
		}
	}
}

func sampleAircraft(airlines []AirlineConfig) *Aircraft {
	airline := Sample(airlines)
	al, ok := database.Airlines[airline.ICAO]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %+v, not found in database", airline)
		return nil
	}

	fleet, ok := al.Fleets[airline.Fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", airline.ICAO, airline.Fleet)
		return nil
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fleet {
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
			aircraft, fleet, airline.ICAO)
		return nil
	}

	// random callsign
	callsign := strings.ToUpper(airline.ICAO)
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
			Rules:            IFR,
			AircraftType:     acType,
			DepartureAirport: airline.Airport,
		},

		Performance: perf,
	}
}

func (ss *SimServer) SpawnArrival(ap *AirportConfig, ag ArrivalGroup) *Aircraft {
	arr := Sample(ag.Arrivals)

	ac := sampleAircraft(arr.Airlines)
	if ac == nil {
		return nil
	}

	ac.TrackingController = arr.InitialController
	ac.FlightPlan.Altitude = 39000
	ac.FlightPlan.Route = arr.Route
	// Start with the default waypoints for the arrival
	ac.Waypoints = arr.Waypoints
	// But if there is a custom route for any of the active runways, switch
	// to that. Results are undefined if there are multiple matches.
	for _, rwy := range ap.ArrivalRunways {
		if rwy.Enabled {
			if wp, ok := arr.RunwayWaypoints[rwy.Runway]; ok {
				ac.Waypoints = wp
				break
			}
		}
	}
	ac.Altitude = float32(arr.InitialAltitude)
	ac.IAS = float32(arr.InitialSpeed)
	ac.CrossingAltitude = arr.ClearedAltitude
	ac.CrossingSpeed = arr.SpeedRestriction

	return ac
}

func (ss *SimServer) SpawnDeparture(ap *AirportConfig, rwy *DepartureRunway) *Aircraft {
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
		// Sample uniformly
		idx := SampleFiltered(ap.Departures,
			func(d Departure) bool {
				category := ap.ExitCategories[d.Exit]
				return *rwy.departureCategoryEnabled[category]
			})
		if idx == -1 {
			lg.Errorf("%s/%s: unable to find a valid departure", ap.ICAO, rwy.Runway)
			return nil
		}
		dep = &ap.Departures[idx]
	}

	rwy.lastDeparture = dep

	ac := sampleAircraft(dep.Airlines)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	ac.Waypoints = DuplicateSlice(exitRoute.Waypoints)
	ac.Waypoints = append(ac.Waypoints, dep.exitWaypoint)

	ac.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Exit + " " + dep.Route
	ac.FlightPlan.ArrivalAirport = dep.Destination
	ac.Scratchpad = ap.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		// If unspecified, pick something in the flight levels...
		// TODO: get altitudes right considering East/West-bound...
		ac.FlightPlan.Altitude = 28000 + 1000*rand.Intn(13)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	ac.Altitude = float32(rwy.Altitude)
	ac.AssignedAltitude = exitRoute.ClearedAltitude

	return ac
}

///////////////////////////////////////////////////////////////////////////

func mustParseWaypoints(str string) []Waypoint {
	if wp, err := parseWaypoints(str); err != nil {
		panic(err)
	} else {
		return wp
	}
}
func parseWaypoints(str string) ([]Waypoint, error) {
	var waypoints []Waypoint
	for _, field := range strings.Fields(str) {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: \"%s\"", str)
		}

		if field == "@" {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before handoff specifier")
			}
			waypoints[len(waypoints)-1].Commands =
				append(waypoints[len(waypoints)-1].Commands, WaypointCommandHandoff)
		} else if field[0] == '#' {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before heading specifier")
			}
			if hdg, err := strconv.Atoi(field[1:]); err != nil {
				return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", field[1:], err)
			} else {
				waypoints[len(waypoints)-1].Heading = hdg
			}
		} else {
			wp := Waypoint{}
			for i, f := range strings.Split(field, "@") {
				if i == 0 {
					wp.Fix = f
				} else {
					switch f[0] {
					case 'a':
						alt, err := strconv.Atoi(f[1:])
						if err != nil {
							return nil, err
						}
						wp.Altitude = alt

					case 's':
						kts, err := strconv.Atoi(f[1:])
						if err != nil {
							return nil, err
						}
						wp.Speed = kts

					default:
						return nil, fmt.Errorf("%s: unknown @ command '%c", field, f[0])
					}
				}
			}

			waypoints = append(waypoints, wp)
		}
	}

	return waypoints, nil
}

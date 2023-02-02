// simserver.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
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

type Scenario struct {
	Callsign    string           `json:"callsign"`
	Controllers []*Controller    `json:"controllers"`
	Airports    []*AirportConfig `json:"airports"`
}

type AirportConfig struct {
	ICAO string `json:"ICAO"`

	NamedLocations map[string]Point2LL `json:"named_locations"`

	Arrivals   []Arrival   `json:"arrivals"`
	Approaches []Approach  `json:"approaches"`
	Departures []Departure `json:"departures"`

	ExitCategories map[string]string `json:"exit_categories"`

	Scratchpads map[string]string `json:"scratchpads"`

	RunwayConfigs []RunwayConfig `json:"runway_configs"`
}

func (ac *AirportConfig) PostDeserialize() error {
	for _, ap := range ac.Approaches {
		for i := range ap.Waypoints {
			n := len(ap.Waypoints[i])
			ap.Waypoints[i][n-1].Commands = append(ap.Waypoints[i][n-1].Commands, WaypointCommandDelete)

			if err := ac.InitializeWaypointLocations(ap.Waypoints[i]); err != nil {
				return err
			}
		}
	}

	for _, ar := range ac.Arrivals {
		if err := ac.InitializeWaypointLocations(ar.Waypoints); err != nil {
			return err
		}
	}

	for i, dep := range ac.Departures {
		wp := []Waypoint{Waypoint{Fix: dep.Exit}}
		if err := ac.InitializeWaypointLocations(wp); err != nil {
			return err
		}
		ac.Departures[i].exitWaypoint = wp[0]
	}

	for i, rwy := range ac.RunwayConfigs {
		ac.RunwayConfigs[i].departureCategoryEnabled = make(map[string]*bool)

		for _, er := range rwy.ExitRoutes {
			if err := ac.InitializeWaypointLocations(er.Waypoints); err != nil {
				return err
			}
		}

		for _, cat := range ac.ExitCategories {
			// This is sort of wasteful, but...
			ac.RunwayConfigs[i].departureCategoryEnabled[cat] = new(bool)
		}
	}

	return nil
}

func (ac *AirportConfig) InitializeWaypointLocations(waypoints []Waypoint) error {
	for i, wp := range waypoints {
		if pos, ok := database.Locate(wp.Fix); ok {
			waypoints[i].Location = pos
		} else if pos, ok := ac.NamedLocations[wp.Fix]; ok {
			waypoints[i].Location = pos
		} else if pos, err := ParseLatLong(wp.Fix); err == nil {
			waypoints[i].Location = pos
		} else {
			return fmt.Errorf("%s: unable to locate waypoint", wp.Fix)
		}
	}
	return nil
}

type RunwayConfig struct {
	Runway     string               `json:"runway"`
	Altitude   int                  `json:"altitude"`
	Enabled    bool                 `json:"-"`
	Rate       int32                `json:"rate"`
	ExitRoutes map[string]ExitRoute `json:"exit_routes"`

	departureCategoryEnabled map[string]*bool
	nextSpawn                time.Time
	challenge                float32
	lastDeparture            *Departure
}

type ExitRoute struct {
	InitialRoute    string        `json:"route"`
	ClearedAltitude int           `json:"cleared_altitude"`
	Waypoints       WaypointArray `json:"waypoints"`
}

type Departure struct {
	Exit         string `json:"exit"`
	exitWaypoint Waypoint

	Destination string          `json:"destination"`
	Altitude    int             `json:"altitude,omitempty"`
	Route       string          `json:"route"`
	Airlines    []AirlineConfig `json:"airlines"`
}

type Arrival struct {
	Name      string        `json:"name"`
	Rate      int32         `json:"rate"`
	Enabled   bool          `json:"-"`
	Waypoints WaypointArray `json:"waypoints"`
	Route     string        `json:"route"`

	InitialController string `json:"initial_controller"`
	InitialAltitude   int    `json:"initial_altitude"`
	ClearedAltitude   int    `json:"cleared_altitude"`
	InitialSpeed      int    `json:"initial_speed"`
	SpeedRestriction  int    `json:"speed_restriction"`

	Airlines []AirlineConfig `json:"airlines"`

	nextSpawn time.Time
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
		e := json.NewEncoder(os.Stdout)
		err := e.Encode(scenarios)
		if err != nil {
			panic(err)
		}
	}
	for _, sc := range scenarios {
		sort.Slice(sc.Controllers, func(i, j int) bool { return sc.Controllers[i].Callsign < sc.Controllers[j].Callsign })

		for _, ap := range sc.Airports {
			ap.PostDeserialize()
		}
	}

	ssc.numAircraft = 30
	ssc.wind.dir = 50
	ssc.wind.speed = 10
	ssc.wind.gust = 15

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
		anyRunwaysActive := false
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

		if imgui.BeginTableV("runways", 4, flags, imgui.Vec2{800, 0}, 0.) {
			imgui.TableSetupColumn("Enabled")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("ADR")
			imgui.TableSetupColumn("Challenge level")
			imgui.TableHeadersRow()

			for i, conf := range ac.RunwayConfigs {
				imgui.PushID(conf.Runway)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				if imgui.Checkbox("##enabled", &ac.RunwayConfigs[i].Enabled) {
					if ac.RunwayConfigs[i].Enabled {
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
				anyRunwaysActive = anyRunwaysActive || ac.RunwayConfigs[i].Enabled
				imgui.TableNextColumn()
				imgui.Text(conf.Runway)
				imgui.TableNextColumn()
				imgui.InputIntV("##adr", &ac.RunwayConfigs[i].Rate, 1, 120, 0)
				imgui.TableNextColumn()
				imgui.SliderFloatV("##challenge", &ac.RunwayConfigs[i].challenge, 0, 1, "%.01f", 0)
				imgui.PopID()
			}
			imgui.EndTable()
		}

		if anyRunwaysActive {
			imgui.Separator()
			flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("configs", 2, flags, imgui.Vec2{800, 0}, 0.) {
				imgui.TableSetupColumn("Enabled")
				imgui.TableSetupColumn("Runway/Gate")
				imgui.TableHeadersRow()
				for _, conf := range ac.RunwayConfigs {
					if !conf.Enabled {
						continue
					}

					imgui.PushID(conf.Runway)
					for _, category := range SortedMapKeys(conf.departureCategoryEnabled) {
						imgui.PushID(category)
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Checkbox("##check", conf.departureCategoryEnabled[category])
						imgui.TableNextColumn()
						imgui.Text(conf.Runway + "/" + category)
						imgui.PopID()
					}
					imgui.PopID()
				}
				imgui.EndTable()
			}
		}
	}

	if len(ac.Arrivals) > 0 {
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("arrivals", 3, flags, imgui.Vec2{600, 0}, 0.) {
			imgui.TableSetupColumn("Enabled")
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("AAR")
			imgui.TableHeadersRow()

			for i := range ac.Arrivals {
				imgui.PushID(ac.Arrivals[i].Name)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Checkbox("##enabled", &ac.Arrivals[i].Enabled)
				imgui.TableNextColumn()
				imgui.Text(ac.Arrivals[i].Name)
				imgui.TableNextColumn()
				imgui.InputIntV("##aar", &ac.Arrivals[i].Rate, 1, 120, 0)
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}
}

func (ssc *SimServerConnectionConfiguration) Valid() bool {
	// Make sure that at least one scenario is selected
	for _, ap := range ssc.scenario.Airports {
		for _, rwy := range ap.RunwayConfigs {
			if !rwy.Enabled {
				continue
			}
			for _, active := range rwy.departureCategoryEnabled {
				if *active {
					return true
				}
			}
		}

		for _, a := range ap.Arrivals {
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

///////////////////////////////////////////////////////////////////////////
// SSAircr

type SSAircraft struct {
	AC          *Aircraft
	Performance AircraftPerformance
	Strip       FlightStrip
	Waypoints   []Waypoint

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...

	// The following are for controller-assigned altitudes, speeds, and
	// headings.  Values of 0 indicate no assignment.
	AssignedAltitude int
	AssignedSpeed    int
	AssignedHeading  int
	TurnDirection    int

	// These are for altitudes/speeds to meet at the next fix; unlike
	// controller-assigned ones, where we try to get there as quickly as
	// the aircraft is capable of, these we try to get to exactly at the
	// fix.
	CrossingAltitude int
	CrossingSpeed    int

	Approach        *Approach // if assigned
	ClearedApproach bool
	OnFinal         bool
}

func (ac *SSAircraft) TAS() float32 {
	// Simple model for the increase in TAS as a function of altitude: 2%
	// additional TAS on top of IAS for each 1000 feet.
	return ac.IAS * (1 + .02*ac.Altitude/1000)
}

func (ac *SSAircraft) UpdateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance
	var targetSpeed int

	// Slow down on final approach
	if ac.OnFinal {
		if airportPos, ok := database.Locate(ac.AC.FlightPlan.ArrivalAirport); ok {
			airportDist := nmdistance2ll(ac.Position, airportPos)
			if airportDist < 1 {
				targetSpeed = perf.Speed.Landing
			} else if airportDist < 5 || (airportDist < 10 && ac.AssignedSpeed == 0) {
				// Ignore speed restrictions if the aircraft is within 5
				// miles; otherwise start slowing down if it hasn't't been
				// given a speed restriction.

				// Expected speed at 10 DME, without further direction.
				approachSpeed := min(210, perf.Speed.Cruise)
				landingSpeed := perf.Speed.Landing
				targetSpeed = int(lerp((airportDist-1)/9, float32(landingSpeed), float32(approachSpeed)))
			}

			// However, don't accelerate if the aircraft is already under
			// the target speed.
			targetSpeed = min(targetSpeed, int(ac.IAS))
			//lg.Errorf("airport dist %f -> target speed %d", airportDist, targetSpeed)
		}
	}

	if targetSpeed == 0 && ac.AssignedSpeed != 0 {
		// Use the controller-assigned speed, but only as far as the
		// aircraft's capabilities.
		targetSpeed = clamp(ac.AssignedSpeed, perf.Speed.Min, perf.Speed.Max)
	}

	if targetSpeed == 0 && ac.CrossingSpeed != 0 {
		dist, ok := ac.nextFixDistance()
		if !ok {
			lg.Errorf("unable to get crossing fix distance... %+v", ac)
			return
		}

		eta := dist / float32(ac.AC.Groundspeed()) * 3600 // in seconds
		cs := float32(ac.CrossingSpeed)
		if ac.IAS < cs {
			accel := (cs - ac.IAS) / eta * 1.25
			accel = min(accel, ac.Performance.Rate.Accelerate/2)
			ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
		} else if ac.IAS > cs {
			decel := (ac.IAS - cs) / eta * 0.75
			decel = min(decel, ac.Performance.Rate.Decelerate/2)
			ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
			//lg.Errorf("dist %f eta %f ias %f crossing %f decel %f", dist, eta, ac.IAS, cs, decel)
		}

		return
	}

	if targetSpeed == 0 {
		targetSpeed = perf.Speed.Cruise

		// But obey 250kts under 10,000'
		if ac.Altitude < 10000 {
			targetSpeed = min(targetSpeed, 250)
		}
	}

	// Finally, adjust IAS subject to the capabilities of the aircraft.
	if ac.IAS+1 < float32(targetSpeed) {
		accel := ac.Performance.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
	} else if ac.IAS-1 > float32(targetSpeed) {
		decel := ac.Performance.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
	}
}

func (ac *SSAircraft) UpdateAltitude() {
	// Climb or descend, but only if it's going fast enough to be
	// airborne.  (Assume no stalls in flight.)
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if !airborne {
		return
	}

	if ac.AssignedAltitude == 0 && ac.CrossingAltitude == 0 {
		// No altitude assignment, so... just stay where we are
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := float32(ac.Performance.Rate.Climb), float32(ac.Performance.Rate.Descent)

	// For high performing aircraft, reduce climb rate after 5,000'
	if climb >= 2500 && ac.Altitude > 5000 {
		climb -= 500
	}

	if ac.AssignedAltitude != 0 {
		// Controller-assigned altitude takes precedence over a crossing
		// altitude.

		if ac.Altitude < float32(ac.AssignedAltitude) {
			// Simple model: we just update altitude based on the rated climb
			// rate; does not account for simultaneous acceleration, etc...
			ac.Altitude = min(float32(ac.AssignedAltitude), ac.Altitude+climb/60)
		} else if ac.Altitude > float32(ac.AssignedAltitude) {
			// Similarly, descent modeling doesn't account for airspeed or
			// acceleration/deceleration...
			ac.Altitude = max(float32(ac.AssignedAltitude), ac.Altitude-descent/60)
		}
	} else {
		// We have a crossing altitude.  Estimated time to get there in minutes.
		dist, ok := ac.nextFixDistance()
		if !ok {
			lg.Errorf("unable to get crossing fix distance... %+v", ac)
			return
		}

		eta := dist / float32(ac.AC.Groundspeed()) * 60 // in minutes
		if ac.CrossingAltitude > int(ac.Altitude) {
			// Need to climb.  Figure out rate of climb that would get us
			// there when we reach the fix (ft/min).
			rate := (float32(ac.CrossingAltitude) - ac.Altitude) / eta

			// But we can't climb faster than the aircraft is capable of.
			ac.Altitude += min(rate, climb) / 60
		} else {
			// Need to descend; same logic as the climb case.
			rate := (ac.Altitude - float32(ac.CrossingAltitude)) / eta
			ac.Altitude -= min(rate, descent) / 60
			//lg.Errorf("dist %f eta %f alt %f crossing %d eta %f -> rate %f ft/min -> delta %f",
			//dist, eta, ac.Altitude, ac.CrossingAltitude, eta, rate, min(rate, descent)/60)
		}
	}
}

// Returns the distance from the aircraft to the next fix in its waypoints,
// in nautical miles.
func (ac *SSAircraft) nextFixDistance() (float32, bool) {
	if len(ac.Waypoints) == 0 {
		return 0, false
	}

	return nmdistance2ll(ac.Position, ac.Waypoints[0].Location), true
}

func (ac *SSAircraft) UpdateHeading() {
	// Figure out the heading; if the route is empty, just leave it
	// on its current heading...
	targetHeading := ac.Heading
	turn := float32(0)

	// Are we intercepting a localizer? Possibly turn to join it.
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		ap.Type == ILSApproach &&
		ac.AssignedHeading != 0 &&
		ac.AssignedHeading != ap.Heading() &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 40 /* allow quite some slop... */ {
		// Estimate time to intercept.  Do this using nm coordinates
		loc := ap.Line()
		loc[0], loc[1] = ll2nm(loc[0]), ll2nm(loc[1])

		pos := ll2nm(ac.Position)
		hdg := ac.Heading - database.MagneticVariation
		headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		pos1 := add2f(pos, headingVector)

		// Intersection of aircraft's path with the localizer
		isect, ok := LineLineIntersect(loc[0], loc[1], pos, pos1)
		if !ok {
			lg.Errorf("no intersect!")
			return // better luck next time...
		}

		// Is the intersection behind the aircraft? (This can happen if it
		// has flown through the localizer.) Ignore it if so.
		v := sub2f(isect, pos)
		if v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
			lg.Errorf("behind us...")
		} else {
			// Find eta to the intercept and the turn required to align with
			// the localizer.
			dist := distance2f(pos, isect)
			eta := dist / float32(ac.AC.Groundspeed()) * 3600 // in seconds
			turn := abs(headingDifference(hdg, float32(ap.Heading())-database.MagneticVariation))
			//lg.Errorf("dist %f, eta %f, turn %f", dist, eta, turn)

			// Assuming 3 degree/second turns, then we might start to turn to
			// intercept when the eta until intercept is 1/3 the number of
			// degrees to cover.  However... the aircraft approaches the
			// localizer more slowly as it turns, so we'll add another 1/2
			// fudge factor, which seems to account for that reasonably well.
			if eta < turn/3/2 {
				lg.Errorf("assigned approach heading! %d", ap.Heading())
				ac.AssignedHeading = ap.Heading()
				ac.TurnDirection = 0
				// Just in case.. Thus we will be ready to pick up the
				// approach waypoints once we capture.
				ac.Waypoints = nil
			}
		}
	}

	// Otherwise, if the controller has assigned a heading, then no matter
	// what, that's what we will turn to.
	if ac.AssignedHeading != 0 {
		targetHeading = float32(ac.AssignedHeading)
		if ac.TurnDirection != 0 {
			// If the controller specified a left or right turn, then
			// compute the full turn angle. We'll do no more than 3 degrees
			// of that.
			if ac.TurnDirection < 0 { // left
				angle := ac.Heading - targetHeading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = -angle
			} else if ac.TurnDirection > 0 { // right
				angle := targetHeading - ac.Heading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = angle
			}
		}
	} else if len(ac.Waypoints) > 0 {
		// Our desired heading is the heading to get to the next waypoint.
		targetHeading = headingp2ll(ac.Position, ac.Waypoints[0].Location,
			database.MagneticVariation)
	} else {
		// And otherwise we're flying off into the void...
		return
	}

	// A turn direction wasn't specified, so figure out which way is
	// closest.
	if turn == 0 {
		// First find the angle to rotate the target heading by so that
		// it's aligned with 180 degrees. This lets us not worry about the
		// complexities of the wrap around at 0/360..
		rot := 180 - targetHeading
		if rot < 0 {
			rot += 360
		}
		cur := mod(ac.Heading+rot, 360) // w.r.t. 180 target
		turn = clamp(180-cur, -3, 3)    // max 3 degrees / second
	}

	// Finally, do the turn.
	if ac.Heading != targetHeading {
		ac.Heading += turn
		if ac.Heading < 0 {
			ac.Heading += 360
		} else if ac.Heading > 360 {
			ac.Heading -= 360
		}
	}
}

func (ss *SimServer) RunWaypointCommands(ac *SSAircraft, cmds []WaypointCommand) {
	for _, cmd := range cmds {
		switch cmd {
		case WaypointCommandHandoff:
			// Handoff to the user's position?
			ac.AC.InboundHandoffController = ss.callsign
			eventStream.Post(&OfferedHandoffEvent{controller: ac.AC.TrackingController, ac: ac.AC})

		case WaypointCommandDelete:
			eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
			delete(ss.aircraft, ac.AC.Callsign)
			return
		}
	}
}

func (ac *SSAircraft) UpdatePositionAndGS(ss *SimServer) {
	// Update position given current heading
	prev := ac.Position
	hdg := ac.Heading - database.MagneticVariation
	v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
	// First use TAS to get a first whack at the new position.
	newPos := add2f(ll2nm(ac.Position), scale2f(v, ac.TAS()/3600))

	// Now add wind...
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if airborne {
		// TODO: have a better gust model?
		windKts := float32(ss.wind.speed + rand.Intn(ss.wind.gust))

		// wind.dir is where it's coming from, so +180 to get the vector
		// that affects the aircraft's course.
		d := float32(ss.wind.dir + 180)
		vWind := [2]float32{sin(radians(d)), cos(radians(d))}
		newPos = add2f(newPos, scale2f(vWind, windKts/3600))
	}

	// Finally update position and groundspeed.
	ac.Position = nm2ll(newPos)
	ac.GS = distance2f(ll2nm(prev), newPos) * 3600
}

func (ac *SSAircraft) UpdateWaypoints(ss *SimServer) {
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		len(ac.Waypoints) == 0 &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 2 &&
		ac.Approach.Type == ILSApproach {
		loc := ap.Line()
		dist := PointLineDistance(ll2nm(ac.Position), ll2nm(loc[0]), ll2nm(loc[1]))

		if dist < .2 {
			// we'll call that good enough. Now we need to figure out which
			// fixes in the approach are still ahead and then add them to
			// the aircraft's waypoints..
			pos := ll2nm(ac.Position)
			hdg := ac.Heading - database.MagneticVariation
			headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}

			ac.Waypoints = nil
			found := false
			// Note that we only use the first set of waypoints
			// here--effectively assuming an intercept in the common
			// segment if there are multiple.
			for _, wp := range ap.Waypoints[0] {
				if wp.Altitude != 0 && int(ac.Altitude) < wp.Altitude {
					continue
				}

				v := sub2f(ll2nm(wp.Location), pos)
				if !found && v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
					lg.Errorf("%s: behind us...", wp.Fix)
				} else {
					lg.Errorf("%s: ahead of us...", wp.Fix)
					ac.Waypoints = append(ac.Waypoints, wp)
				}
			}

			ac.AssignedHeading = 0
			ac.AssignedAltitude = 0
		}
	}

	if len(ac.Waypoints) == 0 {
		return
	}

	wp := ac.Waypoints[0]

	// Are we nearly at the fix?  Move on to the next one a little bit
	// early so that we don't overshoot.
	// TODO: do the turn in a more principled manner so that we smoothly
	// intercept the desired outbound direction.
	if nmdistance2ll(ac.Position, wp.Location) < .75 {
		// Execute any commands associated with the waypoint
		ss.RunWaypointCommands(ac, wp.Commands)

		if ac.Waypoints[0].Heading != 0 {
			// We have an outbound heading
			ac.AssignedHeading = wp.Heading
			ac.TurnDirection = 0
			// The aircraft won't head to the next waypoint until the
			// assigned heading is cleared, though...
			ac.Waypoints = ac.Waypoints[1:]
		} else {
			ac.Waypoints = ac.Waypoints[1:]

			if len(ac.Waypoints) > 0 {
				ac.WaypointUpdate(ac.Waypoints[0])
			}
		}
	}
}

func (ac *SSAircraft) WaypointUpdate(wp Waypoint) {
	if *devmode {
		lg.Printf("Waypoint update. wp %s ac %s", spew.Sdump(wp), spew.Sdump(ac))
	}

	// For starters, convert a previous crossing restriction to a current
	// assignment.  Clear out the previous crossing restriction.
	if ac.AssignedAltitude == 0 {
		ac.AssignedAltitude = ac.CrossingAltitude
	}
	ac.CrossingAltitude = 0

	if ac.AssignedSpeed == 0 {
		ac.AssignedSpeed = ac.CrossingSpeed
	}
	ac.CrossingSpeed = 0

	// Now handle any altitude/speed restriction at the next waypoint.
	if wp.Altitude != 0 {
		ac.CrossingAltitude = wp.Altitude
		ac.AssignedAltitude = 0
	}
	if wp.Speed != 0 {
		ac.CrossingSpeed = wp.Speed
		ac.AssignedSpeed = 0
	}

	ac.AssignedHeading = 0
	ac.TurnDirection = 0

	if ac.ClearedApproach {
		// The aircraft has made it to the approach fix they
		// were cleared to.
		lg.Errorf("%s: on final...", ac.AC.Callsign)
		ac.OnFinal = true
	}
}

///////////////////////////////////////////////////////////////////////////
// SimServer

type SimServer struct {
	// These come from the scenario
	callsign       string
	airportConfigs []*AirportConfig
	controllers    map[string]*Controller

	aircraft map[string]*SSAircraft
	handoffs map[string]time.Time

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

		aircraft:          make(map[string]*SSAircraft),
		handoffs:          make(map[string]time.Time),
		currentTime:       time.Now(),
		lastUpdateTime:    time.Now(),
		remainingLaunches: int(ssc.numAircraft),
		simRate:           1,
	}

	for _, ctrl := range ssc.scenario.Controllers {
		if *ssc.controllerActive[ctrl.Callsign] {
			ss.controllers[ctrl.Callsign] = ctrl
		}
	}

	ss.wind.dir = int(ssc.wind.dir)
	ss.wind.speed = int(ssc.wind.speed)
	ss.wind.gust = int(ssc.wind.gust)

	// Prime the pump before the user gets involved
	ss.SpawnAircraft()
	for i := 0; i < 60; i++ {
		ss.updateSim()
	}

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
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
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
	} else if ac.AC.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&InitiatedTrackEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) DropTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&DroppedTrackEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) Handoff(callsign string, controller string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := ss.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.AC.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&OfferedHandoffEvent{controller: ss.callsign, ac: ac.AC})
		acceptDelay := 2 + rand.Intn(10)
		ss.handoffs[callsign] = ss.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (ss *SimServer) AcceptHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.InboundHandoffController != ss.callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.AC.InboundHandoffController = ""
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&AcceptedHandoffEvent{controller: ss.callsign, ac: ac.AC})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC}) // FIXME...
		return nil
	}
}

func (ss *SimServer) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) CancelHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.OutboundHandoffController = ""
		// TODO: we are inconsistent in other control backends about events
		// when user does things like this; sometimes no event, sometimes
		// modified a/c event...
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
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
		eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
	}
}

func (ss *SimServer) GetAircraft(callsign string) *Aircraft {
	if ac, ok := ss.aircraft[callsign]; ok {
		return ac.AC
	}
	return nil
}

func (ss *SimServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range ss.aircraft {
		if filter(ac.AC) {
			filtered = append(filtered, ac.AC)
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
	// UNIMPLEMENTED
	return nil
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

	// Accept any handoffs whose time has time...
	now := ss.CurrentTime()
	for callsign, t := range ss.handoffs {
		if now.After(t) {
			ac := ss.aircraft[callsign].AC
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
			ac.AC.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - database.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
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

func (ss *SimServer) getApproach(callsign string, approach string) (*Approach, *SSAircraft, error) {
	ac, ok := ss.aircraft[callsign]
	if !ok {
		return nil, nil, ErrNoAircraftForCallsign
	}
	fp := ac.AC.FlightPlan
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
		response = "you never told us to expect an approach, but "
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

	if ac.Approach.Type == ILSApproach && len(ac.Waypoints) == 0 {
		if ac.AssignedHeading == 0 {
			pilotResponse(callsign, "we need either direct or a heading to intercept")
			return nil
		}
		// Otherwise nothing more to do; keep flying the heading for now.
		// After we intercept we'll get the rest of the waypoints added.
	} else {
		// For RNAV or ILS direct to a fix, the first (and only) entry in
		// Waypoints should be one of the approach fixes. Add the remaining ones..
		if len(ac.Waypoints) != 1 {
			// TODO: ERROR CHECKING BETTER, HANDLE RNAV CLEARED W/O DIRECT TO A FIX...
			lg.Errorf("WTF waypoints: %+v", ac.Waypoints)
			return errors.New("WTF waypoints")
		}
		found := false
		for _, route := range ap.Waypoints {
			for _, wp := range route {
				if wp.Fix == ac.Waypoints[0].Fix {
					found = true
				} else if found {
					ac.Waypoints = append(ac.Waypoints, wp)
				}
			}
			if found {
				break
			}
		}
		if !found {
			lg.Errorf("waypoint %s not in approach's waypoints %+v", ac.Waypoints[0].Fix, ap.Waypoints)
			return errors.New("WTF2 waypoints")
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
		s := fmt.Sprintf("%s: current alt %d, assigned alt %d crossing alt %d",
			ac.AC.Callsign, ac.AC.Altitude(), ac.AssignedAltitude, ac.CrossingAltitude)
		if ac.AssignedHeading != 0 {
			s += fmt.Sprintf(" heading %d", ac.AssignedHeading)
			if ac.TurnDirection != 0 {
				s += fmt.Sprintf(" turn direction %d", ac.TurnDirection)
			}
		}
		s += fmt.Sprintf(", IAS %f GS %d speed %d crossing speed %d",
			ac.IAS, ac.AC.Groundspeed(), ac.AssignedSpeed, ac.CrossingSpeed)

		if ac.ClearedApproach {
			s += ", cleared approach"
		}
		if ac.OnFinal {
			s += ", on final"
		}
		if ac.Approach != nil {
			s += fmt.Sprintf(", approach: %s", spew.Sdump(ac.Approach))
		}
		s += fmt.Sprintf(", route %s", spew.Sdump(ac.Waypoints))
		lg.Errorf("%s", s)
	}
	return nil
}

func (ss *SimServer) DeleteAircraft(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
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

	addAircraft := func(ssa *SSAircraft) {
		if _, ok := ss.aircraft[ssa.AC.Callsign]; ok {
			lg.Errorf("%s: already have an aircraft with that callsign!", ssa.AC.Callsign)
			return
		}
		ss.aircraft[ssa.AC.Callsign] = ssa

		ss.RunWaypointCommands(ssa, ssa.Waypoints[0].Commands)

		ssa.Position = ssa.Waypoints[0].Location
		ssa.Heading = float32(ssa.Waypoints[0].Heading)
		if ssa.Heading == 0 { // unassigned, so get the heading from the next fix
			ssa.Heading = headingp2ll(ssa.Position, ssa.Waypoints[1].Location, database.MagneticVariation)
		}
		ssa.Waypoints = ssa.Waypoints[1:]

		lg.Errorf("Added aircraft: %s", spew.Sdump(ssa))

		ss.remainingLaunches--
		eventStream.Post(&AddedAircraftEvent{ac: ssa.AC})
	}

	for _, ap := range ss.airportConfigs {
		for i, arr := range ap.Arrivals {
			if arr.Enabled && ss.remainingLaunches > 0 && now.After(arr.nextSpawn) {
				if ac := ss.SpawnArrival(arr); ac != nil {
					ac.AC.FlightPlan.ArrivalAirport = ap.ICAO
					addAircraft(ac)
					seconds := 3600/int(arr.Rate) - 10 + rand.Intn(21)
					ap.Arrivals[i].nextSpawn = now.Add(time.Duration(seconds) * time.Second)
				}
			}
		}

		for i, rwy := range ap.RunwayConfigs {
			if rwy.Enabled && ss.remainingLaunches > 0 && now.After(rwy.nextSpawn) {
				if ac := ss.SpawnDeparture(ap, &ap.RunwayConfigs[i]); ac != nil {
					ac.AC.FlightPlan.DepartureAirport = ap.ICAO
					addAircraft(ac)
					seconds := 3600/int(rwy.Rate) - 10 + rand.Intn(21)
					ap.RunwayConfigs[i].nextSpawn = now.Add(time.Duration(seconds) * time.Second)
				}
			}
		}
	}
}

func sampleAircraft(airlines []AirlineConfig) *SSAircraft {
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
	for _, ch := range al.Callsign.CallsignFormats[rand.Intn(len(al.Callsign.CallsignFormats))] {
		switch ch {
		case '#':
			callsign += fmt.Sprintf("%d", rand.Intn(10))
		case '@':
			callsign += string(rune('A' + rand.Intn(26)))
		}
	}
	// TODO: retry if we're trying to use 0 as the flight number...

	squawk := Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &SSAircraft{
		AC: &Aircraft{
			Callsign:       callsign,
			AssignedSquawk: squawk,
			Squawk:         squawk,
			Mode:           Charlie,
			FlightPlan: &FlightPlan{
				Rules:            IFR,
				AircraftType:     acType,
				DepartureAirport: airline.Airport,
			},
		},
		Performance: perf,
	}
}

func (ss *SimServer) SpawnArrival(arr Arrival) *SSAircraft {
	ac := sampleAircraft(arr.Airlines)
	if ac == nil {
		return nil
	}

	ac.AC.TrackingController = arr.InitialController
	ac.AC.FlightPlan.Altitude = 39000
	ac.AC.FlightPlan.Route = arr.Route
	ac.Waypoints = arr.Waypoints
	ac.Altitude = float32(arr.InitialAltitude)
	ac.IAS = float32(arr.InitialSpeed)
	ac.CrossingAltitude = arr.ClearedAltitude
	ac.CrossingSpeed = arr.SpeedRestriction

	return ac
}

func (ss *SimServer) SpawnDeparture(ap *AirportConfig, rwy *RunwayConfig) *SSAircraft {
	idx := SampleFiltered(ap.Departures,
		func(d Departure) bool {
			category := ap.ExitCategories[d.Exit]
			return *rwy.departureCategoryEnabled[category]
		})
	if idx == -1 {
		return nil
	}

	// TODO: support rwy.challenge

	dep := ap.Departures[idx]
	rwy.lastDeparture = &ap.Departures[idx]

	ac := sampleAircraft(dep.Airlines)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	ac.Waypoints = DuplicateSlice(exitRoute.Waypoints)
	ac.Waypoints = append(ac.Waypoints, dep.exitWaypoint)

	ac.AC.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Exit + " " + dep.Route
	ac.AC.FlightPlan.ArrivalAirport = dep.Destination
	ac.AC.Scratchpad = ap.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		// If unspecified, pick something in the flight levels...
		// TODO: get altitudes right considering East/West-bound...
		ac.AC.FlightPlan.Altitude = 28000 + 1000*rand.Intn(13)
	} else {
		ac.AC.FlightPlan.Altitude = dep.Altitude
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

///////////////////////////////////////////////////////////////////////////
// KJFK

/*
func jfk31RRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	delete(c.categoryEnabled, "Southwest")

	c.name = "31R"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkPropProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}
*/

func JFKApproachScenario() *Scenario {
	s := &Scenario{}

	s.Callsign = "JFK_APP"

	addController := func(cs string, freq float32) {
		s.Controllers = append(s.Controllers, &Controller{
			Callsign:  cs,
			Frequency: NewFrequency(freq),
		})
	}

	addController("BOS_E_CTR", 133.45) // B17
	addController("ISP_APP", 120.05)   //  3H
	addController("JFK_APP", 128.125)  //  2G
	addController("JFK_DEP", 135.9)    //  2A
	addController("JFK_TWR", 119.1)    //  2W
	addController("LGA_DEP", 120.4)    //  1L
	addController("NY_F_CTR", 128.3)   // N66
	addController("NY_CTR", 125.325)   // N56
	addController("NY_LE_DEP", 126.8)  //  5E
	addController("NY_LS_DEP", 124.75) //  5S

	jfk := JFKAirport()
	s.Airports = append(s.Airports, jfk)
	lga := LGAAirport()
	lga.Scratchpads = jfk.Scratchpads
	lga.ExitCategories = jfk.ExitCategories
	s.Airports = append(s.Airports, LGAAirport())
	isp := ISPAirport()
	isp.Scratchpads = jfk.Scratchpads
	isp.ExitCategories = jfk.ExitCategories
	s.Airports = append(s.Airports, ISPAirport())
	frg := FRGAirport()
	frg.Scratchpads = jfk.Scratchpads
	frg.ExitCategories = jfk.ExitCategories
	s.Airports = append(s.Airports, FRGAirport())

	return s
}

func JFKAirport() *AirportConfig {
	ac := &AirportConfig{ICAO: "KJFK"}
	ac.NamedLocations = map[string]Point2LL{
		"_JFK_31L": mustParseLatLong("N040.37.41.000, W073.46.20.227"),
		"_JFK_31R": mustParseLatLong("N040.38.35.986, W073.45.31.503"),
		"_JFK_22R": mustParseLatLong("N040.39.00.362, W073.45.49.053"),
		"_JFK_22L": mustParseLatLong("N040.38.41.232, W073.45.18.511"),
		"_JFK_4L":  mustParseLatLong("N040.37.19.370, W073.47.08.045"),
		"_JFK_4La": mustParseLatLong("N040.39.21.332, W073.45.32.849"),
		"_JFK_4R":  mustParseLatLong("N040.37.31.661, W073.46.12.894"),
		"_JFK_13R": mustParseLatLong("N040.38.53.537, W073.49.00.188"),
		"_JFK_13L": mustParseLatLong("N040.39.26.976, W073.47.24.277"),
	}

	ac.ExitCategories = map[string]string{
		"WAVEY": "Water",
		"SHIPP": "Water",
		"HAPIE": "Water",
		"BETTE": "Water",
		"MERIT": "East",
		"GREKI": "East",
		"BAYYS": "East",
		"BDR":   "East",
		"DIXIE": "Southwest",
		"WHITE": "Southwest",
		"RBV":   "Southwest",
		"ARD":   "Southwest",
		"COATE": "North",
		"NEION": "North",
		"HAAYS": "North",
		"GAYEL": "North",
		"DEEZZ": "North",
	}

	i4l := Approach{
		ShortName: "I4L",
		FullName:  "ILS 4 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "AROKE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i4l)

	i4r := Approach{
		ShortName: "I4R",
		FullName:  "ILS 4 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ZETAL", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i4r)

	rz4l := Approach{
		ShortName: "R4L",
		FullName:  "RNAV Zulu 4 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "REPRE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz4l)

	rz4r := Approach{
		ShortName: "R4R",
		FullName:  "RNAV Zulu 4 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "VERRU", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz4r)

	i13l := Approach{
		ShortName: "I3L",
		FullName:  "ILS 13 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "COVIR", Altitude: 3000},
			Waypoint{Fix: "KMCHI", Altitude: 2900},
			Waypoint{Fix: "BUZON", Altitude: 2900},
			Waypoint{Fix: "TELEX", Altitude: 2100},
			Waypoint{Fix: "CAXUN", Altitude: 1500},
			Waypoint{Fix: "UXHUB", Altitude: 680},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i13l)

	rz13l := Approach{
		ShortName: "R3L",
		FullName:  "RNAV Zulu 13 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "CNRSE", Altitude: 2000},
			Waypoint{Fix: "LEISA", Altitude: 1246},
			Waypoint{Fix: "SILJY", Altitude: 835},
			Waypoint{Fix: "ROBJE", Altitude: 456},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz13l)

	rz13r := Approach{
		ShortName: "R3R",
		FullName:  "RNAV Zulu 13 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "NUCRI", Altitude: 2000},
			Waypoint{Fix: "PEEBO", Altitude: 921},
			Waypoint{Fix: "MAYMA", Altitude: 520},
			Waypoint{Fix: "_JFK_13R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz13r)

	i22l := Approach{
		ShortName: "I2L",
		FullName:  "ILS 22 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			}},
	}
	ac.Approaches = append(ac.Approaches, i22l)

	i22r := Approach{
		ShortName: "I2R",
		FullName:  "ILS 22 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1800},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1900},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			}},
	}
	ac.Approaches = append(ac.Approaches, i22r)

	rz22l := Approach{
		ShortName: "R2L",
		FullName:  "RNAV Zulu 22 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "TUSTE", Altitude: 3000},
			Waypoint{Fix: "ZAKUS", Altitude: 1900},
			Waypoint{Fix: "JIRVA", Altitude: 1900},
			Waypoint{Fix: "SOGOE", Altitude: 1429},
			Waypoint{Fix: "HESOR", Altitude: 1019},
			Waypoint{Fix: "_JFK_22L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz22l)

	rz22r := Approach{
		ShortName: "R2R",
		FullName:  "RNAV Zulu 22 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "RIVRA", Altitude: 3000},
			Waypoint{Fix: "HENEB", Altitude: 1900},
			Waypoint{Fix: "_JFK_22R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz22r)

	i31l := Approach{
		ShortName: "I3L",
		FullName:  "ILS 31 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CHANT", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "DPK", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, i31l)

	i31r := Approach{
		ShortName: "I3R",
		FullName:  "ILS 31 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CATOD", Altitude: 3000},
				Waypoint{Fix: "MALDE", Altitude: 3000},
				Waypoint{Fix: "ZULAB", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, i31r)

	rz31l := Approach{
		ShortName: "R1L",
		FullName:  "RNAV Zulu 31 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "SESKE"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "RISSY"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, rz31l)

	rz31r := Approach{
		ShortName: "R1R",
		FullName:  "RNAV Zulu 31 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "PZULU"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "VIDIO"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, rz31r)

	camrn4 := Arrival{
		Name:              "CAMRN4",
		Rate:              30,
		Waypoints:         mustParseWaypoints("N039.46.43.120,W074.03.15.529 KARRS @ CAMRN #041"),
		Route:             "/. CAMRN4",
		InitialController: "NY_F_CTR",
		InitialAltitude:   15000,
		ClearedAltitude:   11000,
		InitialSpeed:      300,
		SpeedRestriction:  250,
		Airlines: []AirlineConfig{
			AirlineConfig{ICAO: "WJA", Airport: "KDCA", Fleet: "jfk"},
			AirlineConfig{ICAO: "WJA", Airport: "KORF", Fleet: "jfk"},
			AirlineConfig{ICAO: "WJA", Airport: "KJAX", Fleet: "jfk"},
			AirlineConfig{ICAO: "JBU", Airport: "KDFW", Fleet: "default"},
			AirlineConfig{ICAO: "JBU", Airport: "KMCO", Fleet: "default"},
			AirlineConfig{ICAO: "JBU", Airport: "KCLT", Fleet: "default"},
			AirlineConfig{ICAO: "BWA", Airport: "MKJP", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KTUL", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KAUS", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KDEN", Fleet: "default"},
			AirlineConfig{ICAO: "AMX", Airport: "MMMY", Fleet: "long"},
			AirlineConfig{ICAO: "AMX", Airport: "MMMX", Fleet: "long"},
		},
	}
	ac.Arrivals = append(ac.Arrivals, camrn4)

	lendy8 := Arrival{
		Name:              "LENDY8",
		Rate:              30,
		Waypoints:         mustParseWaypoints("N040.56.09.863,W074.30.33.013 N040.55.09.974,W074.25.19.628 @ LENDY #135"),
		Route:             "/. LENDY8",
		InitialController: "NY_F_CTR",
		InitialAltitude:   20000,
		ClearedAltitude:   19000,
		InitialSpeed:      300,
		SpeedRestriction:  250,
		Airlines: []AirlineConfig{
			AirlineConfig{ICAO: "ASA", Airport: "KSFO", Fleet: "default"},
			AirlineConfig{ICAO: "ASA", Airport: "KPDX", Fleet: "default"},
			AirlineConfig{ICAO: "DAL", Airport: "KMSP", Fleet: "default"},
			AirlineConfig{ICAO: "DAL", Airport: "KDTW", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KORD", Fleet: "default"},
			AirlineConfig{ICAO: "UPS", Airport: "KLAS", Fleet: "default"},
			AirlineConfig{ICAO: "DAL", Airport: "KSEA", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KLAX", Fleet: "default"},
			AirlineConfig{ICAO: "UAL", Airport: "KSFO", Fleet: "default"},
			AirlineConfig{ICAO: "AAL", Airport: "KSFO", Fleet: "default"},
			AirlineConfig{ICAO: "CPA", Airport: "VHHH", Fleet: "cargo"},
			AirlineConfig{ICAO: "ANA", Airport: "RJAA", Fleet: "long"},
			AirlineConfig{ICAO: "KAL", Airport: "RKSI", Fleet: "long"},
			AirlineConfig{ICAO: "WJA", Airport: "KIND", Fleet: "jfk"},
			AirlineConfig{ICAO: "WJA", Airport: "KCVG", Fleet: "jfk"},
			AirlineConfig{ICAO: "ACA", Airport: "CYYC", Fleet: "default"},
			AirlineConfig{ICAO: "ACA", Airport: "CYUL", Fleet: "short"},
		},
	}
	ac.Arrivals = append(ac.Arrivals, lendy8)

	debug := Arrival{
		Name:              "DEBUG",
		Rate:              30,
		Waypoints:         mustParseWaypoints("N040.20.22.874,W073.48.09.981 N040.21.34.834,W073.51.11.997 @ #360"),
		Route:             "/. DEBUG",
		InitialController: "NY_F_CTR",
		InitialAltitude:   3000,
		ClearedAltitude:   2000,
		InitialSpeed:      250,
		Airlines: []AirlineConfig{
			AirlineConfig{ICAO: "UAL", Airport: "KMSP", Fleet: "default"},
		},
	}
	ac.Arrivals = append(ac.Arrivals, debug)

	parch3 := Arrival{
		Name:              "PARCH3",
		Rate:              30,
		Waypoints:         mustParseWaypoints("N041.02.38.230,W072.23.00.102 N040.57.31.959,W072.42.21.494 @ CCC ROBER #278"),
		Route:             "/. PARCH3",
		InitialController: "NY_F_CTR",
		InitialAltitude:   13000,
		ClearedAltitude:   12000,
		InitialSpeed:      275,
		SpeedRestriction:  250,
		Airlines: []AirlineConfig{
			AirlineConfig{ICAO: "AAL", Airport: "CYYZ", Fleet: "short"},
			AirlineConfig{ICAO: "AFR", Airport: "LPFG", Fleet: "long"},
			AirlineConfig{ICAO: "BAW", Airport: "EGLL", Fleet: "long"},
			AirlineConfig{ICAO: "CLX", Airport: "EGCC", Fleet: "default"},
			AirlineConfig{ICAO: "DAL", Airport: "KBOS", Fleet: "short"},
			AirlineConfig{ICAO: "DLH", Airport: "EDDF", Fleet: "long"},
			AirlineConfig{ICAO: "GEC", Airport: "EDDF", Fleet: "default"},
			AirlineConfig{ICAO: "DLH", Airport: "EDDM", Fleet: "long"},
			AirlineConfig{ICAO: "GDC", Airport: "EDDM", Fleet: "default"},
			AirlineConfig{ICAO: "EIN", Airport: "EIDW", Fleet: "long"},
			AirlineConfig{ICAO: "ELY", Airport: "LLBG", Fleet: "jfk"},
			AirlineConfig{ICAO: "FIN", Airport: "EFHK", Fleet: "long"},
			AirlineConfig{ICAO: "GEC", Airport: "EDDF", Fleet: "default"},
			AirlineConfig{ICAO: "IBE", Airport: "LEBL", Fleet: "long "},
			AirlineConfig{ICAO: "IBE", Airport: "LEMD", Fleet: "long"},
			AirlineConfig{ICAO: "JBU", Airport: "KBOS", Fleet: "default"},
			AirlineConfig{ICAO: "KLM", Airport: "EHAM", Fleet: "long"},
			AirlineConfig{ICAO: "QXE", Airport: "KBGR", Fleet: "short"},
			AirlineConfig{ICAO: "QXE", Airport: "KPVD", Fleet: "short"},
			AirlineConfig{ICAO: "UAE", Airport: "OMDB", Fleet: "loww"},
			AirlineConfig{ICAO: "UAL", Airport: "CYYZ", Fleet: "short"},
			AirlineConfig{ICAO: "UAL", Airport: "KBOS", Fleet: "short"},
			AirlineConfig{ICAO: "UPS", Airport: "KBOS", Fleet: "default"},
			AirlineConfig{ICAO: "VIR", Airport: "EGCC", Fleet: "long"},
		},
	}
	ac.Arrivals = append(ac.Arrivals, parch3)

	// TODO? PAWLING2 (turboprop <= 250KT)

	ac.Departures = []Departure{
		// Europe
		// Charles De Gaulle
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO WHALE N251A JOOPY NATZ MALOT NATZ GISTI LESLU M142 LND N160 NAKID M25 ANNET UM25 UVSUV UM25 INGOR UM25 LUKIP LUKIP9E",
			Destination: "LPFG",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AFR", Fleet: "long"}},
		},
		// Heathrow
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT WITCH ALLEX N379A ALLRY NATU SOVED LUTOV KELLY L10 WAL UY53 NUGRA",
			Destination: "EGLL",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "BAW", Fleet: "long"},
				AirlineConfig{ICAO: "VIR", Fleet: "default"},
			},
		},
		// Manchester
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N139A PORTI 4700N/05000W 5000N/04000W 5200N/03000W 5300N/02000W MALOT GISTI PELIG BOFUM Q37 MALUD MALUD1M",
			Destination: "EGCC",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "BAW", Fleet: "long"}},
		},
		// Istanbul
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT EBONY ALLRY 5100N/05000W 5300N/04000W 5500N/03000W 5600N/02000W PIKIL SOVED NIBOG BELOX L603 LAMSO PETIK NOMKA OMELO GOLOP KOZLI ROMIS PITOK BADOR RONBU BUVAK RIXEN",
			Destination: "LTFM",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "THY", Fleet: "long"}},
		},
		// Edinburgh
		Departure{
			Exit:        "BETTE",
			Route:       "ACK TUSKY N291A IBERG NATW NEBIN NATW OLGON MOLAK BRUCE L602 CLYDE STIRA",
			Destination: "EGPH",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "VIR", Fleet: "default"},
				AirlineConfig{ICAO: "FDX", Fleet: "long"},
			},
		},
		// Warsaw
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS MARTN TOPPS N499A RIKAL 5300N/05000W 5500N/04000W 5700N/03000W 5700N/02000W GOMUP GINGA TIR REKNA PETIL BAVTA BAKLI KEKOV GOSOT NASOK N195 SORIX",
			Destination: "EPWA",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "LOT", Fleet: "long"}},
		},
		// Madrid
		Departure{
			Exit:        "BETTE",
			Route:       "ACK VITOL N189A NICSO 4800N/05000W 4900N/04000W 4900N/03000W 4700N/02000W PASAS STG DESAT UN733 ZMR",
			Destination: "LEMD",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "long"},
				AirlineConfig{ICAO: "IBE", Fleet: "long"},
			},
		},
		// Amsterdam
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N317A ELSIR NATY DOGAL NATY BEXET BOYNE DIBAL L603 LAMSO",
			Destination: "EHAM",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "KLM", Fleet: "long"}},
		},
		// Frankfurt
		Departure{
			Exit:        "BETTE",
			Route:       "ACK BRADD N255A JOOPY NATX MALOT NATX GISTI UNBEG SHA SLANY UL9 KONAN UL607 SPI T180 TOBOP T180 NIVNU T180 UNOKO UNOK3A",
			Destination: "EDDF",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "DLH", Fleet: "a359"},
				AirlineConfig{ICAO: "GEC", Fleet: "default"}},
		},
		// Barcelona (lebl)
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO DOVEY 4200N/06000W 4200N/05000W 4300N/04000W 4400N/03000W 4400N/02000W MUDOS STG SUSOS UN725 YAKXU UN725 LOBAR",
			Destination: "LEBL",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "IBE", Fleet: "long"}},
		},
		// Rome
		Departure{
			Exit:        "BETTE",
			Route:       "ACK BRADD N255A JOOPY NATX MALOT NATX GISTI SLANY UL9 BIG L15 MOTOX UL15 RANUX UL15 NEBAX LASAT DEVDI ODINA SRN EKPAL Q705 XIBIL XIBIL3A",
			Destination: "LIRF",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "long"}},
		},
		// Helsinki
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS MARTN DANOL N481A SAXAN 5300N/05000W 5600N/04000W 5900N/03000W 6100N/02000W 6200N/01000W IPTON UXADA Y349 AMROT Y362 LAKUT",
			Destination: "EFHK",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "FIN", Fleet: "long"}},
		},
		// Dublin
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT BOS TUSKY N321A ELSIR NATV RESNO NATV NETKI OLAPO OLAPO3X",
			Destination: "EIDW",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EIN", Fleet: "long"}},
		},

		// Central/South America (ish)
		// Mexico city
		Departure{
			Exit:        "RBV",
			Route:       "Q430 COPES Q75 GVE LYH COLZI AWYAT IPTAY CHOPZ MGM SJI TBD M575 KENGS M345 AXEXO UM345 PAZ UT154 ENAGA ENAGA2A",
			Destination: "MMMX",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "long"},
				AirlineConfig{ICAO: "AMX", Fleet: "long"}},
		},
		// Sao Paulo
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L459 KEEKA L329 ZPATA UL329 KORTO ETATA UP535 MOMSO UZ24 DOLVI UL452 GELVA UZ6 ISOPI UZ6 NIMKI UZ38 VUNOX VUNOX1A",
			Destination: "SBGR",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "long"}},
		},

		// Caribbean
		// San Juan
		Departure{
			Exit:        "SHIPP",
			Route:       "Y489 RESQU SKPPR L455 KINCH L455 LENNT M423 PLING RTE7 SAALR",
			Destination: "TJSJ",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"}},
		},
		// Bermuda
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC MOMOM1",
			Destination: "TXKF",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		// Kingston
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP IDOLS RROOO Y323 CARPX Y307 ENAMO NEFTU UP525 EMABU UA301 IMADI SAVEM",
			Destination: "MKJP",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "JBU", Fleet: "default"},
				AirlineConfig{ICAO: "BWA", Fleet: "b738"}},
		},
		// St. Thomas
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L456 HANCY L456 THANK JETSS",
			Destination: "TIST",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"}},
		},
		// Antigua
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC PIREX L462 ANU",
			Destination: "TAPA",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "DAL", Fleet: "default"}},
		},

		// Misc US routes
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS ESENT LUNNI1",
			Destination: "KJAX",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "ATN", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE NOSIK Q812 ZOHAN IDIOM MUSCL3",
			Destination: "KMSP",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "UPS", Fleet: "default"},
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "HYPER8",
			Destination: "KIAD",
			Altitude:    22000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 COPES Q75 GVE LYH CHSLY5",
			Destination: "KCLT",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"}},
		},
		Departure{
			Exit:        "COATE",
			Route:       "Q436 EMMMA WYNDE2",
			Destination: "KORD",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "J95 CFB TRAAD JACCI FERRL2",
			Destination: "KDTW",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO CHIEZ Y291 MAJIK CUUDA2",
			Destination: "KFLL",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE RUBKI ASP TVC KP87I FSD KP81C BFF KD60S HVE GGAPP CHOWW2",
			Destination: "KLAS",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 WARNN ZJAAY TAQLE1",
			Destination: "KRDU",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS IGARY Q85 LPERD GTOUT1",
			Destination: "KMCO",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 SAAME J6 HVQ Q68 BWG BLUZZ3",
			Destination: "KMEM",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "FDX", Fleet: "b752"},
				AirlineConfig{ICAO: "FDX", Fleet: "b763"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS J132 ULW BENEE",
			Destination: "KBUF",
			Altitude:    18000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CCV",
			Destination: "KORF",
			Altitude:    26000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 SAAME J6 HVQ Q68 YOCKY GROAT PASLY4",
			Destination: "KBNA",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "UPS", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO YLEEE ZILLS Y289 DULEE CLMNT2",
			Destination: "KPBI",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		}, // west palm beach
		Departure{
			Exit:        "BDR",
			Route:       "CARLD V188 GON PVD",
			Destination: "KPVD",
			Altitude:    9000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "COATE",
			Route:       "V116 LVZ V613 FJC",
			Destination: "KABE",
			Altitude:    12000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS CAM ENE",
			Destination: "KBGR",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE SSM YQT VBI YWG LIVBI DUKPO FAREN YDR VLN J500 YYN MEDAK ROPLA YXC GLASR1",
			Destination: "KSEA",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "UPS", Fleet: "default"},
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 BYRDD J48 MOL FLASK REAVS ODF THRSR GRGIA SJI SLIDD2",
			Destination: "KMSY",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB HAYNZ7",
			Destination: "KPIT",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB UPPRR TRYBE4",
			Destination: "KCLE",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
				AirlineConfig{ICAO: "ATN", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS CFB V29 SYR",
			Destination: "KSYR",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q812 ARRKK Q812 SYR TULEG YYB SPALD YTL YGX IKLIX YHY 6100N/13000W JAGIT NCA13 TMSON PTERS3",
			Destination: "PANC",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "CAL", Fleet: "long"},
			},
		},

		// Canadia
		// Toronto
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE LINNG3",
			Destination: "CYYZ",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "ACA", Fleet: "default"},
			},
		},
		// Montreal
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS CAM PBERG CARTR4",
			Destination: "CYUL",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "ACA", Fleet: "default"},
			},
		},
		// Calgary
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE ASP RIMBE WIEDS 4930N/10000W GUDOG PIKLA BIRKO5",
			Destination: "CYYC",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "ACA", Fleet: "default"},
			},
		},

		// Middle East
		// Doha (qatari air)
		Departure{
			Exit:        "BETTE",
			Route:       "ACK WHALE N193A NICSO NATY ELSOX MAPAG LESLU L180 MERLY M140 DVR UL9 KONAN UL607 KOK MATUG BOMBI DETEV INBED LAMSI STEIN INVED RASUB RIXEN UA17 BAG UL614 EZS UT36 ULTED UT301 BOTAS UT301 DEPSU UT301 DURSI UT301 KAVAM UT301 MIDSI R659 VEDED R659 VELAM Z225 BAYAN",
			Destination: "OTBD",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "QTR", Fleet: "jfk"}},
		},
		// Tel Aviv
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO DOVEY 4200N/06000W 4400N/05000W 4700N/04000W 4900N/03000W 5000N/02000W SOMAX ATSUR TAKAS ALUTA KORER UM616 TUPAR DIDRU BEBIX VALKU LERGA TOZOT UT183 OTROT UM728 DOKAR SUNEV LAT SIPRO PAPIZ PINDO LINRO UL52 VAXOS UN134 VANZA PIKOG L609 ZUKKO AMMOS1C",
			Destination: "LLBG",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "ELY", Fleet: "jfk"}},
		},
		// Dubai
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS BAF KB87E MIILS N563A NEEKO 5500N/05000W 6000N/04000W 6300N/03000W 6400N/02000W 6300N/01000W GUNPA RASVI KOSEB PEROM BINKA ROVEK KELEL BADOR ARGES ARTAT UP975 UNVUS UP975 EZS UG8 OTKEP UM688 RATVO UM688 SIDAD P975 SESRU M677 RABAP M677 IVIVI M677 UKNEP M677 DEGSO M677 OBNET M677 LUDAM M677 VUTEB",
			Destination: "OMDB",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "UAE", Fleet: "a388"}},
		},

		// Far East
		// Hong Kong
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS BAF KJOHN CEFOU YBC KETLA 6700N/05000W 7200N/04000W 7800N/02000W 8000N/00000E 8000N/02000E 8000N/03000E PIREL N611 DOSON R705 OKASA R705 BRT G490 SERNA Y520 POLHO G218 TMR B458 VERUX B458 DADGA W37 OMBEB R473 BEMAG R473 WYN W18 SANIP W18 NLG W23 ZUH R473 SIERA",
			Destination: "VHHH",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "CPA", Fleet: "default"}},
		},
		// Seoul
		Departure{
			Exit:        "GAYEL",
			Route:       "Q812 MSLIN Q812 SYR RAKAM 4800N/08000W 5400N/09000W 5700N/10000W 5900N/11000W HOGAR GUDEN DEEJA LAIRE KODNE CHUUK R341 HAVAM R341 NATES R220 NUBDA R220 NODAN R217 ASTER Y514 SDE Y512 GTC L512 TENAS Y437 KAE Y697 KARBU",
			Destination: "RKSI",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "KAL", Fleet: "jumbo"}},
		},
		// Tokyo Narita
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE Q917 DUTEL Q917 SSM YRL 5400N/10000W 5800N/11000W 6000N/12000W 6100N/13000W IGSOM OMSUN NCA20 ELLAM TED NODLE R220 NOSHO R220 NIKLL R220 NIPPI R220 NOGAL R220 NANAC Y810 OLDIV Y809 SUPOK",
			Destination: "RJAA",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "ANA", Fleet: "a380"}},
		},

		// India
		// New Delhi
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N195A NICSO NATY MALOT NATY GISTI EVRIN L607 NUMPO L607 KONAN UL607 MATUG BOMBI TENLO LAMSI STEIN TEGRI BULEN L742 RIXEN UA17 YAVRU UA4 ERZ UN161 YAVUZ UN161 INDUR N161 EDATA M11 RODAR A909 BABUM A477 BUPOR B198 OGNOB G555 USETU G500 BUTRA L181 POMIR G500 FIRUZ P500 PS T400 SULOM A466 IGINO IGIN5A",
			Destination: "VIDP",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AIC", Fleet: "b788"}},
		},
	}

	ac.Scratchpads = map[string]string{
		"WAVEY":  "WAV",
		"SHIPP":  "SHI",
		"HAPIE":  "HAP",
		"BETTE":  "BET",
		"MERIT":  "MER",
		"GREKI":  "GRE",
		"BAYYS":  "BAY",
		"BDR":    "BDR",
		"DIXIE":  "DIX",
		"WHITE":  "WHI",
		"RBV":    "RBV",
		"ARD":    "ARD",
		"COATE":  "COA",
		"NEION":  "NEI",
		"HAAYS":  "HAY",
		"GAYEL":  "GAY",
		"DEEZZ":  "DEZ",
		"DEEZZ5": "DEZ",
	}

	ac.RunwayConfigs = []RunwayConfig{
		RunwayConfig{
			Runway:   "31L",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "SKORR5.RNGRR #223", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "SKORR5.RNGRR #223", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "SKORR5.RNGRR #223", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "SKORR5.RNGRR #223", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "SKORR5.YNKEE #172", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
			},
		},
		RunwayConfig{
			Runway:   "22R",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
			},
		},
		RunwayConfig{
			Runway:   "13R",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #170"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #170"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #155"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #155"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #109"), ClearedAltitude: 5000},
			},
		},
		RunwayConfig{
			Runway:   "4L",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
			},
		},
	}

	return ac
}

func LGAAirport() *AirportConfig {
	lga := &AirportConfig{ICAO: "KLGA"}
	lga.NamedLocations = map[string]Point2LL{
		"_LGA_13":  mustParseLatLong("N040.46.56.029, W073.52.42.359"),
		"_LGA_13a": mustParseLatLong("N040.48.06.479, W073.55.40.914"),
		"_LGA_31":  mustParseLatLong("N040.46.19.788, W073.51.25.949"),
		"_LGA_31a": mustParseLatLong("N040.45.34.950, W073.49.52.922"),
		"_LGA_31b": mustParseLatLong("N040.48.50.809, W073.46.42.200"),
		"_LGA_22":  mustParseLatLong("N040.47.06.864, W073.52.14.811"),
		"_LGA_22a": mustParseLatLong("N040.51.18.890, W073.49.30.483"),
		"_LGA_4":   mustParseLatLong("N040.46.09.447, W073.53.02.574"),
		"_LGA_4a":  mustParseLatLong("N040.44.56.662, W073.51.53.497"),
		"_LGA_4b":  mustParseLatLong("N040.47.59.557, W073.47.11.533"),
	}

	lga.Departures = []Departure{
		// Caribbean
		// San Juan
		Departure{
			Exit:        "SHIPP",
			Route:       "Y489 RESQU SKPPR L455 KINCH L455 LENNT M423 PLING RTE7 SAALR",
			Destination: "TJSJ",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"}},
		},
		// Bermuda
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC MOMOM1",
			Destination: "TXKF",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		// Kingston
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP IDOLS RROOO Y323 CARPX Y307 ENAMO NEFTU UP525 EMABU UA301 IMADI SAVEM",
			Destination: "MKJP",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "JBU", Fleet: "default"},
				AirlineConfig{ICAO: "BWA", Fleet: "b738"}},
		},
		// St. Thomas
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L456 HANCY L456 THANK JETSS",
			Destination: "TIST",
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"}},
		},
		// Antigua
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC PIREX L462 ANU",
			Destination: "TAPA",
			Airlines: []AirlineConfig{AirlineConfig{ICAO: "AAL", Fleet: "default"},
				AirlineConfig{ICAO: "DAL", Fleet: "default"}},
		},

		// Misc US routes
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS ESENT LUNNI1",
			Destination: "KJAX",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "ATN", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO CHIEZ Y291 MAJIK CUUDA2",
			Destination: "KFLL",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "AAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 WARNN ZJAAY TAQLE1",
			Destination: "KRDU",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS IGARY Q85 LPERD GTOUT1",
			Destination: "KMCO",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CCV",
			Destination: "KORF",
			Altitude:    26000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO YLEEE ZILLS Y289 DULEE CLMNT2",
			Destination: "KPBI",
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "JBU", Fleet: "default"},
			},
		},
	}

	lga.RunwayConfigs = []RunwayConfig{
		RunwayConfig{
			Runway:   "31",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 6000},
			},
		},
		RunwayConfig{
			Runway:   "22",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 6000},
			},
		},
		RunwayConfig{
			Runway:   "13",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 6000},
			},
		},
		RunwayConfig{
			Runway:   "4",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 6000},
			},
		},
	}

	/* TODO
	if *config.categoryEnabled["Southwest Props"] {
		// WHITE Props
		rp := proto
		rp.ClearedAltitude = 7000
		rp.Fleet = "short"
		rp.Airlines = []string{"QXE", "BWA", "FDX"}
		routeTemplates = append(routeTemplates, jfkWHITE.GetRouteTemplates(rp, way, "LGA7")...)
	}
	*/

	return lga
}

func ISPAirport() *AirportConfig {
	isp := &AirportConfig{ICAO: "KISP"}
	isp.NamedLocations = map[string]Point2LL{
		"_ISP_6":    mustParseLatLong("N040.47.18.743, W073.06.44.022"),
		"_ISP_6a":   mustParseLatLong("N040.50.43.281, W073.02.11.698"),
		"_ISP_6b":   mustParseLatLong("N040.50.28.573, W073.09.10.827"),
		"_ISP_24":   mustParseLatLong("N040.48.06.643, W073.05.39.202"),
		"_ISP_24a":  mustParseLatLong("N040.45.56.414, W073.08.58.879"),
		"_ISP_24b":  mustParseLatLong("N040.47.41.032, W073.06.08.371"),
		"_ISP_24c":  mustParseLatLong("N040.48.48.350, W073.07.30.466"),
		"_ISP_15R":  mustParseLatLong("N040.48.05.462, W073.06.24.356"),
		"_ISP_15Ra": mustParseLatLong("N040.45.33.934, W073.02.36.555"),
		"_ISP_15Rb": mustParseLatLong("N040.49.18.755, W073.03.43.379"),
		"_ISP_15Rc": mustParseLatLong("N040.48.34.288, W073.09.11.211"),
		"_ISP_33L":  mustParseLatLong("N040.47.32.819, W073.05.41.702"),
		"_ISP_33La": mustParseLatLong("N040.49.52.085, W073.08.43.141"),
		"_ISP_33Lb": mustParseLatLong("N040.49.21.515, W073.06.31.250"),
		"_ISP_33Lc": mustParseLatLong("N040.48.20.019, W073.10.31.686"),
	}
	isp.Departures = []Departure{
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS J132 ULW BENEE",
			Destination: "KBUF",
			Altitude:    16000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "MERIT",
			Route:       "MERIT ORW ORW7",
			Destination: "KBOS",
			Altitude:    21000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "NEION J223 CORDS CFB V29 SYR",
			Destination: "KSYR",
			Altitude:    21000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "GREKI",
			Route:       "GREKI JUDDS CAM",
			Destination: "KBTV",
			Altitude:    21000,
			Airlines: []AirlineConfig{
				AirlineConfig{ICAO: "WJA", Fleet: "jfk"},
				AirlineConfig{ICAO: "JBU", Fleet: "jfk"},
			},
		},
	}

	isp.RunwayConfigs = []RunwayConfig{
		RunwayConfig{
			Runway: "6",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
			},
		},
		RunwayConfig{
			Runway: "24",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
			},
		},
		RunwayConfig{
			Runway: "15R",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
			},
		},
		RunwayConfig{
			Runway: "33L",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
			},
		},
	}

	return isp
}

func FRGAirport() *AirportConfig {
	frg := &AirportConfig{ICAO: "KFRG"}
	frg.NamedLocations = map[string]Point2LL{
		"_FRG_1":   mustParseLatLong("N040.43.20.230, W073.24.51.229"),
		"_FRG_1a":  mustParseLatLong("N040.46.52.637, W073.24.58.809"),
		"_FRG_19":  mustParseLatLong("N040.44.10.396, W073.24.50.982"),
		"_FRG_19a": mustParseLatLong("N040.41.03.313, W073.26.45.267"),
		"_FRG_14":  mustParseLatLong("N040.44.02.898, W073.25.17.486"),
		"_FRG_14a": mustParseLatLong("N040.38.37.868, W073.22.41.398"),
		"_FRG_32":  mustParseLatLong("N040.43.20.436, W073.24.13.848"),
		"_FRG_32a": mustParseLatLong("N040.45.28.921, W073.27.08.421"),
	}

	frg.Departures = []Departure{
		Departure{
			Exit:        "DIXIE",
			Route:       "JFK V16 DIXIE V276 RBV V249 SBJ",
			Destination: "KTEB",
			Altitude:    12000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Altitude:    39000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "MERIT",
			Route:       "ROBUC3",
			Destination: "KBOS",
			Altitude:    21000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "BDR",
			Route:       "V487 CANAN",
			Destination: "KALB",
			Altitude:    39000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB HAYNZ7",
			Destination: "KPIT",
			Altitude:    39000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "DIXIE",
			Route:       "JFK DIXIE V16 VCN VCN9",
			Destination: "KILG",
			Altitude:    39000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "COATE",
			Route:       "Q436 HERBA JHW WWSHR CBUSS2",
			Destination: "KCMH",
			Altitude:    39000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
		Departure{
			Exit:        "BDR",
			Route:       "BDR",
			Destination: "KHVN",
			Altitude:    14000,
			Airlines:    []AirlineConfig{AirlineConfig{ICAO: "EJA", Fleet: "default"}},
		},
	}

	frg.RunwayConfigs = []RunwayConfig{
		RunwayConfig{
			Runway:   "1",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
			},
		},
		RunwayConfig{
			Runway:   "19",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
			},
		},
		RunwayConfig{
			Runway:   "14",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
			},
		},
		RunwayConfig{
			Runway:   "32",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
			},
		},
	}

	return frg
}

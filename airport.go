// airport.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"time"

	"github.com/mmp/imgui-go/v4"
)

type Airport struct {
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

func (ac *Airport) PostDeserialize() []error {
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

func (ac *Airport) InitializeWaypointLocations(waypoints []Waypoint) []error {
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

func (ac *Airport) DrawUI() {
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
		oldNumActive, newNumActive := 0, 0
		if imgui.BeginTableV("arrivalrunways", 2, flags, imgui.Vec2{500, 0}, 0.) {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Enabled")
			imgui.TableHeadersRow()

			for i, rwy := range ac.ArrivalRunways {
				if rwy.Enabled {
					oldNumActive++
				}

				imgui.PushID(rwy.Runway)
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(rwy.Runway)
				imgui.TableNextColumn()
				imgui.Checkbox("##enabled", &ac.ArrivalRunways[i].Enabled)
				if ac.ArrivalRunways[i].Enabled {
					newNumActive++
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}

		if oldNumActive == 0 && newNumActive == 1 {
			for i := range ac.ArrivalGroups {
				ac.ArrivalGroups[i].Enabled = true
			}
		} else if oldNumActive == 1 && newNumActive == 0 {
			for i := range ac.ArrivalGroups {
				ac.ArrivalGroups[i].Enabled = false
			}
		}

		if newNumActive > 0 && len(ac.ArrivalGroups) > 0 {
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

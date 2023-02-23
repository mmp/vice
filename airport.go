// airport.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type Airport struct {
	ICAO      string   `json:"icao"`
	Elevation int      `json:"elevation"`
	Location  Point2LL `json:"location"`

	ArrivalGroups []ArrivalGroup `json:"arrival_groups,omitempty"`
	Approaches    []Approach     `json:"approaches,omitempty"`
	Departures    []Departure    `json:"departures,omitempty"`

	ExitCategories map[string]string `json:"exit_categories"`

	DepartureRunways   []*DepartureRunway `json:"departure_runways"`
	ArrivalRunwayNames []string           `json:"arrival_runways"`
	ArrivalRunways     []*ArrivalRunway   `json:"-"`
}

func (ac *Airport) PostDeserialize(controllers map[string]*Controller) []error {
	var errors []error

	for _, rwy := range ac.ArrivalRunwayNames {
		ac.ArrivalRunways = append(ac.ArrivalRunways, &ArrivalRunway{Runway: rwy})
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

	checkAirline := func(icao, fleet string) {
		al, ok := database.Airlines[icao]
		if !ok {
			errors = append(errors, fmt.Errorf("%s: airline not in database", icao))
		}

		if fleet == "" {
			fleet = "default"
		}

		fl, ok := al.Fleets[fleet]
		if !ok {
			errors = append(errors,
				fmt.Errorf("%s: fleet unknown for airline \"%s\"", fleet, icao))
		}

		for _, aircraft := range fl {
			if perf, ok := database.AircraftPerformance[aircraft.ICAO]; !ok {
				errors = append(errors,
					fmt.Errorf("%s: aircraft in airline \"%s\"'s fleet \"%s\" not in perf database",
						aircraft.ICAO, icao, fleet))
			} else {
				if perf.Speed.Min < 50 || perf.Speed.Landing < 50 || perf.Speed.Cruise < 50 ||
					perf.Speed.Max < 50 || perf.Speed.Min > perf.Speed.Max {
					fmt.Errorf("%s: aircraft's speed specification is questionable: %s", aircraft.ICAO,
						spew.Sdump(perf.Speed))
				}
				if perf.Rate.Climb == 0 || perf.Rate.Descent == 0 || perf.Rate.Accelerate == 0 ||
					perf.Rate.Decelerate == 0 {
					fmt.Errorf("%s: aircraft's rate specification is questionable: %s", aircraft.ICAO,
						spew.Sdump(perf.Rate))
				}
			}
		}
	}

	// Filter out the DEBUG arrivals if devmode isn't enabled
	if !*devmode {
		ac.ArrivalGroups = FilterSlice(ac.ArrivalGroups, func(ag ArrivalGroup) bool { return ag.Name != "Debug" })
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

			for _, al := range ar.Airlines {
				checkAirline(al.ICAO, al.Fleet)
			}

			if _, ok := controllers[ar.InitialController]; !ok {
				errors = append(errors, fmt.Errorf("%s: controller not found for arrival in %s group",
					ar.InitialController, ag.Name))
			}
		}
	}

	for i, dep := range ac.Departures {
		wp := []Waypoint{Waypoint{Fix: dep.Exit}}
		errors = append(errors, ac.InitializeWaypointLocations(wp)...)
		ac.Departures[i].exitWaypoint = wp[0]

		for _, al := range dep.Airlines {
			checkAirline(al.ICAO, al.Fleet)
		}
	}

	runwayNames := make(map[string]interface{})
	for _, rwy := range ac.DepartureRunways {
		if _, ok := runwayNames[rwy.Runway]; ok {
			errors = append(errors, fmt.Errorf("%s: multiple runway definitions", rwy.Runway))
		}
		runwayNames[rwy.Runway] = nil

		for _, er := range rwy.ExitRoutes {
			errors = append(errors, ac.InitializeWaypointLocations(er.Waypoints)...)
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
		} else if pos, ok := tracon.Locate(wp.Fix); ok {
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
	Enabled    bool                 `json:"-"`
	Rate       int32                `json:"rate"`
	ExitRoutes map[string]ExitRoute `json:"exit_routes"`

	nextSpawn     time.Time
	lastDeparture *Departure
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

	Destination string             `json:"destination"`
	Altitude    int                `json:"altitude,omitempty"`
	Route       string             `json:"route"`
	Airlines    []DepartureAirline `json:"airlines"`
}

type DepartureAirline struct {
	ICAO  string `json:"icao"`
	Fleet string `json:"fleet,omitempty"`
}

type ArrivalGroup struct {
	Name     string    `json:"name"`
	Rate     int32     `json:"rate"`
	Enabled  bool      `json:"-"`
	Arrivals []Arrival `json:"arrivals"`

	nextSpawn time.Time
}

type Arrival struct {
	Waypoints       WaypointArray            `json:"waypoints"`
	RunwayWaypoints map[string]WaypointArray `json:"runway_waypoints"`
	Route           string                   `json:"route"`

	InitialController string `json:"initial_controller"`
	InitialAltitude   int    `json:"initial_altitude"`
	ClearedAltitude   int    `json:"cleared_altitude"`
	InitialSpeed      int    `json:"initial_speed"`
	SpeedRestriction  int    `json:"speed_restriction"`
	ExpectApproach    string `json:"expect_approach"`
	Scratchpad        string `json:"scratchpad"`

	Airlines []ArrivalAirline `json:"airlines"`
}

type ArrivalAirline struct {
	ICAO    string `json:"icao"`
	Airport string `json:"airport"`
	Fleet   string `json:"fleet,omitempty"`
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
	return int(headingp2ll(p[0], p[1], tracon.MagneticVariation) + 0.5)
}

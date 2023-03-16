// airport.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"
)

type Airport struct {
	Elevation      int      `json:"elevation"`
	Location       Point2LL `json:"location"`
	TowerListIndex int      `json:"tower_list"`

	Approaches map[string]Approach `json:"approaches,omitempty"`
	Departures []Departure         `json:"departures,omitempty"`

	ExitCategories map[string]string `json:"exit_categories"`

	// runway -> (exit -> route)
	DepartureRoutes map[string]map[string]ExitRoute `json:"departure_routes"`
}

func (ac *Airport) PostDeserialize(t *ScenarioGroup) []error {
	var errors []error

	for _, ap := range ac.Approaches {
		for i := range ap.Waypoints {
			n := len(ap.Waypoints[i])
			ap.Waypoints[i][n-1].Commands = append(ap.Waypoints[i][n-1].Commands, WaypointCommandDelete)

			errors = append(errors, t.InitializeWaypointLocations(ap.Waypoints[i])...)
		}
	}

	// Departure routes are specified in the JSON as comma-separated lists
	// of exits. We'll split those out into individual entries in the
	// Airport's DepartureRoutes, one per exit, for convenience of future code.
	splitDepartureRoutes := make(map[string]map[string]ExitRoute)
	for rwy, rwyRoutes := range ac.DepartureRoutes {
		seenExits := make(map[string]interface{})
		splitDepartureRoutes[rwy] = make(map[string]ExitRoute)

		for exitList, exitRoute := range rwyRoutes {
			errors = append(errors, t.InitializeWaypointLocations(exitRoute.Waypoints)...)

			for _, exit := range strings.Split(exitList, ",") {
				if _, ok := seenExits[exit]; ok {
					errors = append(errors, fmt.Errorf("%s: exit repeatedly specified in routes for runway %s", exit, rwy))
				}
				seenExits[exit] = nil

				splitDepartureRoutes[rwy][exit] = exitRoute
			}
		}
	}
	ac.DepartureRoutes = splitDepartureRoutes

	for i, dep := range ac.Departures {
		if _, ok := t.Scratchpads[dep.Exit]; !ok {
			errors = append(errors, fmt.Errorf("%s: exit in departure to %s not in scratchpads", dep.Exit, dep.Destination))
		}

		// Make sure that all runways have a route to the exit
		for rwy, rwyRoutes := range ac.DepartureRoutes {
			if _, ok := rwyRoutes[dep.Exit]; !ok {
				errors = append(errors, fmt.Errorf("%s: exit not found in departure routes for runway %s",
					dep.Exit, rwy))
			}
		}

		sawExit := false
		for _, fix := range strings.Fields(dep.Route) {
			sawExit = sawExit || fix == dep.Exit
			// Best effort only to find waypoint locations; we will fail
			// for airways, international ones not in the FAA database,
			// latlongs in the flight plan, etc.  Don't issue an error
			// unless the exit wasn't present in the route in the first
			// place.
			wp := []Waypoint{Waypoint{Fix: fix}}
			if errs := t.InitializeWaypointLocations(wp); len(errs) == 0 {
				ac.Departures[i].routeWaypoints = append(ac.Departures[i].routeWaypoints, wp[0])
			}
		}
		if !sawExit {
			errors = append(errors, fmt.Errorf("%s: exit not found in departure route to %s", dep.Exit, dep.Destination))
		}

		for _, al := range dep.Airlines {
			errors = append(errors, database.CheckAirline(al.ICAO, al.Fleet)...)
		}
	}

	return errors
}

type ExitRoute struct {
	InitialRoute    string        `json:"route"`
	ClearedAltitude int           `json:"cleared_altitude"`
	Waypoints       WaypointArray `json:"waypoints"`
}

type Departure struct {
	Exit string `json:"exit"`

	Destination    string `json:"destination"`
	Altitude       int    `json:"altitude,omitempty"`
	Route          string `json:"route"`
	routeWaypoints []Waypoint
	Airlines       []DepartureAirline `json:"airlines"`
}

type DepartureAirline struct {
	ICAO  string `json:"icao"`
	Fleet string `json:"fleet,omitempty"`
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
	return int(headingp2ll(p[0], p[1], scenario.MagneticVariation) + 0.5)
}

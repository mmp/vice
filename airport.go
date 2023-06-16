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

	DepartureController string `json:"departure_controller"`

	ExitCategories map[string]string `json:"exit_categories"`

	// runway -> (exit -> route)
	DepartureRoutes map[string]map[string]ExitRoute `json:"departure_routes"`
}

func (ap *Airport) PostDeserialize(sg *ScenarioGroup, e *ErrorLogger) {
	for name, ap := range ap.Approaches {
		e.Push("Approach " + name)

		if isAllNumbers(name) {
			e.ErrorString("Approach names cannot only have numbers in them")
		}

		for i := range ap.Waypoints {
			n := len(ap.Waypoints[i])
			ap.Waypoints[i][n-1].Delete = true
			sg.InitializeWaypointLocations(ap.Waypoints[i], e)

			if ap.Waypoints[i][n-1].ProcedureTurn != nil {
				e.ErrorString("ProcedureTurn cannot be specified at the final waypoint")
			}
			for j, wp := range ap.Waypoints[i] {
				e.Push("Fix " + wp.Fix)
				if wp.NoPT {
					if FindIf(ap.Waypoints[i][j+1:],
						func(wp Waypoint) bool { return wp.ProcedureTurn != nil }) == -1 {
						e.ErrorString("No procedure turn found after fix with \"nopt\"")
					}
				}
				e.Pop()
			}
		}
		if ap.Runway == "" {
			e.ErrorString("Must specify \"runway\"")
		}
		e.Pop()
	}

	if _, ok := sg.ControlPositions[ap.DepartureController]; !ok && ap.DepartureController != "" {
		e.ErrorString("departure_controller \"%s\" unknown", ap.DepartureController)
	}

	// Departure routes are specified in the JSON as comma-separated lists
	// of exits. We'll split those out into individual entries in the
	// Airport's DepartureRoutes, one per exit, for convenience of future code.
	splitDepartureRoutes := make(map[string]map[string]ExitRoute)
	for rwy, rwyRoutes := range ap.DepartureRoutes {
		e.Push("Departure runway " + rwy)
		seenExits := make(map[string]interface{})
		splitDepartureRoutes[rwy] = make(map[string]ExitRoute)

		for exitList, route := range rwyRoutes {
			e.Push("Exit " + exitList)
			sg.InitializeWaypointLocations(route.Waypoints, e)

			for _, exit := range strings.Split(exitList, ",") {
				if _, ok := seenExits[exit]; ok {
					e.ErrorString("exit repeatedly specified in routes")
				}
				seenExits[exit] = nil

				splitDepartureRoutes[rwy][exit] = route
			}
			e.Pop()
		}
		e.Pop()
	}
	ap.DepartureRoutes = splitDepartureRoutes

	for i, dep := range ap.Departures {
		e.Push("Departure exit " + dep.Exit)
		e.Push("Destination " + dep.Destination)

		if _, ok := sg.Scratchpads[dep.Exit]; !ok {
			e.ErrorString("exit not in scenario group \"scratchpads\"")
		}

		// Make sure that all runways have a route to the exit
		for rwy, routes := range ap.DepartureRoutes {
			e.Push("Runway " + rwy)
			if _, ok := routes[dep.Exit]; !ok {
				e.ErrorString("exit \"%s\" not found in runway's \"departure_routes\"", dep.Exit)
			}
			e.Pop()
		}

		sawExit := false
		for _, fix := range strings.Fields(dep.Route) {
			sawExit = sawExit || fix == dep.Exit
			wp := []Waypoint{Waypoint{Fix: fix}}
			// Best effort only to find waypoint locations; this will fail
			// for airways, international ones not in the FAA database,
			// latlongs in the flight plan, etc.
			if fix == dep.Exit {
				sg.InitializeWaypointLocations(wp, e)
			} else {
				// nil here so errors aren't logged if it's not the actual exit.
				sg.InitializeWaypointLocations(wp, nil)
			}
			ap.Departures[i].routeWaypoints = append(ap.Departures[i].routeWaypoints, wp[0])
		}
		if !sawExit {
			e.ErrorString("exit not found in departure route")
		}

		for _, al := range dep.Airlines {
			database.CheckAirline(al.ICAO, al.Fleet, e)
		}

		e.Pop()
		e.Pop()
	}
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

func (at ApproachType) String() string {
	return []string{"ILS", "RNAV"}[at]
}

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
	Runway    string          `json:"runway"`
	Waypoints []WaypointArray `json:"waypoints"`
}

func (ap *Approach) Line() [2]Point2LL {
	// assume we have at least one set of waypoints and that it has >= 2 waypoints!
	wp := ap.Waypoints[0]

	// use the last two waypoints
	n := len(wp)
	return [2]Point2LL{wp[n-2].Location, wp[n-1].Location}
}

func (ap *Approach) Heading() float32 {
	p := ap.Line()
	return headingp2ll(p[0], p[1], sim.MagneticVariation())
}

// airport.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"
	"unicode"
)

type Airport struct {
	Location       Point2LL `json:"location"`
	TowerListIndex int      `json:"tower_list"`

	Name string `json:"name"`

	Approaches map[string]Approach `json:"approaches,omitempty"`
	Departures []Departure         `json:"departures,omitempty"`

	// Optional: initial tracking controller, for cases where a virtual
	// controller has the initial track.
	DepartureController string `json:"departure_controller"`

	ExitCategories map[string]string `json:"exit_categories"`

	// runway -> (exit -> route)
	DepartureRoutes map[string]map[string]ExitRoute `json:"departure_routes"`

	ApproachRegions   map[string]*ApproachRegion `json:"approach_regions"`
	ConvergingRunways []ConvergingRunways        `json:"converging_runways"`
}

type ConvergingRunways struct {
	Runways                [2]string                   `json:"runways"`
	TieSymbol              string                      `json:"tie_symbol"`
	StaggerSymbol          string                      `json:"stagger_symbol"`
	TieOffset              float32                     `json:"tie_offset"`
	LeaderDirectionStrings [2]string                   `json:"leader_directions"`
	LeaderDirections       [2]CardinalOrdinalDirection // not in JSON, set during deserialize
	RunwayIntersection     Point2LL                    // not in JSON, set during deserialize
}

type ApproachRegion struct {
	Runway           string  // set during deserialization
	HeadingTolerance float32 `json:"heading_tolerance"`

	ReferenceLineHeading   float32  `json:"reference_heading"`
	ReferenceLineLength    float32  `json:"reference_length"`
	ReferencePointAltitude float32  `json:"reference_altitude"`
	ReferencePoint         Point2LL `json:"reference_point"`

	// lateral qualification region
	NearDistance  float32 `json:"near_distance"`
	NearHalfWidth float32 `json:"near_half_width"`
	FarHalfWidth  float32 `json:"far_half_width"`
	RegionLength  float32 `json:"region_length"`

	// vertical qualification region
	DescentPointDistance   float32 `json:"descent_distance"`
	DescentPointAltitude   float32 `json:"descent_altitude"`
	AboveAltitudeTolerance float32 `json:"above_altitude_tolerance"`
	BelowAltitudeTolerance float32 `json:"below_altitude_tolerance"`

	ScratchpadPatterns []string `json:"scratchpad_patterns"`
}

// returns a point along the reference line with given distance from the
// reference point, in nm coordinates.
func (ar *ApproachRegion) referenceLinePoint(dist, nmPerLongitude, magneticVariation float32) [2]float32 {
	hdg := radians(ar.ReferenceLineHeading + 180 - magneticVariation)
	v := [2]float32{sin(hdg), cos(hdg)}
	pref := ll2nm(ar.ReferencePoint, nmPerLongitude)
	return add2f(pref, scale2f(v, dist))
}

func (ar *ApproachRegion) NearPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) FarPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) GetLateralGeometry(nmPerLongitude, magneticVariation float32) (line [2]Point2LL, quad [4]Point2LL) {
	// Start with the reference line
	p0 := ar.referenceLinePoint(0, nmPerLongitude, magneticVariation)
	p1 := ar.referenceLinePoint(ar.ReferenceLineLength, nmPerLongitude, magneticVariation)
	line = [2]Point2LL{nm2ll(p0, nmPerLongitude), nm2ll(p1, nmPerLongitude)}

	// Get the unit vector perpendicular to the reference line
	v := normalize2f(sub2f(p1, p0))
	vperp := [2]float32{-v[1], v[0]}

	pNear := ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
	pFar := ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
	q0 := add2f(pNear, scale2f(vperp, ar.NearHalfWidth))
	q1 := add2f(pFar, scale2f(vperp, ar.FarHalfWidth))
	q2 := add2f(pFar, scale2f(vperp, -ar.FarHalfWidth))
	q3 := add2f(pNear, scale2f(vperp, -ar.NearHalfWidth))
	quad = [4]Point2LL{nm2ll(q0, nmPerLongitude), nm2ll(q1, nmPerLongitude),
		nm2ll(q2, nmPerLongitude), nm2ll(q3, nmPerLongitude)}

	return
}

type GhostAircraft struct {
	Callsign            string
	Position            Point2LL
	Groundspeed         int
	LeaderLineDirection CardinalOrdinalDirection
	TrackId             string
}

func (ar *ApproachRegion) TryMakeGhost(callsign string, track RadarTrack, heading float32, scratchpad string,
	forceGhost bool, offset float32, leaderDirection CardinalOrdinalDirection, runwayIntersection [2]float32,
	nmPerLongitude float32, magneticVariation float32, other *ApproachRegion) *GhostAircraft {
	// Start with lateral extent since even if it's forced, the aircraft still must be inside it.
	line, quad := ar.GetLateralGeometry(nmPerLongitude, magneticVariation)
	if !PointInPolygon(track.Position, quad[:]) {
		return nil
	}

	if !forceGhost {
		// Heading must be in range
		if headingDifference(heading, ar.ReferenceLineHeading) > ar.HeadingTolerance {
			return nil
		}

		// Check vertical extent
		// Work in nm here...
		l := [2][2]float32{ll2nm(line[0], nmPerLongitude), ll2nm(line[1], nmPerLongitude)}
		pc := ClosestPointOnLine(l, ll2nm(track.Position, nmPerLongitude))
		d := distance2f(pc, l[0])
		if d > ar.DescentPointDistance {
			if float32(track.Altitude) > ar.DescentPointAltitude+ar.AboveAltitudeTolerance ||
				float32(track.Altitude) < ar.DescentPointAltitude-ar.BelowAltitudeTolerance {
				return nil
			}
		} else {
			t := (d - ar.NearDistance) / (ar.DescentPointDistance - ar.NearDistance)
			alt := lerp(t, ar.ReferencePointAltitude, ar.DescentPointAltitude)
			if float32(track.Altitude) > alt+ar.AboveAltitudeTolerance ||
				float32(track.Altitude) < alt-ar.BelowAltitudeTolerance {
				return nil
			}
		}

		if len(ar.ScratchpadPatterns) > 0 {
			if idx := FindIf(ar.ScratchpadPatterns,
				func(pat string) bool { return strings.Contains(scratchpad, pat) }); idx == -1 {
				return nil
			}
		}
	}

	isectNm := ll2nm(runwayIntersection, nmPerLongitude)
	remap := func(pll Point2LL) Point2LL {
		// Switch to nm for transformations to compute ghost position
		p := ll2nm(pll, nmPerLongitude)
		// Vector to reference point
		v := sub2f(p, isectNm)
		// Rotate it to be oriented with respect to the other runway's reference point
		v = rotator2f(other.ReferenceLineHeading - ar.ReferenceLineHeading)(v)
		// Offset as appropriate
		v = add2f(v, scale2f(normalize2f(v), offset))
		// Back to a nm point with regards to the other reference point
		p = add2f(isectNm, v)
		// And lat-long for the final result
		return nm2ll(p, nmPerLongitude)
	}

	ghost := &GhostAircraft{
		Callsign:            callsign,
		Position:            remap(track.Position),
		Groundspeed:         track.Groundspeed,
		LeaderLineDirection: leaderDirection,
	}

	return ghost
}

func (ap *Airport) PostDeserialize(sg *ScenarioGroup, e *ErrorLogger) {
	if ap.Location.IsZero() {
		e.ErrorString("Must specify \"location\" for airport")
	}

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

			ap.Waypoints[i].CheckApproach(e)
		}

		if ap.Runway == "" {
			e.ErrorString("Must specify \"runway\"")
		}

		if ap.Type == ChartedVisualApproach && len(ap.Waypoints) != 1 {
			// Note: this could be relaxed if necessary but the logic in
			// Nav prepareForChartedVisual() assumes as much.
			e.ErrorString("Only a single set of waypoints are allowed for a charted visual approach route")
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

			route.Waypoints.CheckDeparture(e)

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

		if _, ok := sg.Scratchpads[dep.Exit]; dep.Scratchpad == "" && !ok {
			e.ErrorString("exit not in scenario group \"scratchpads\"")
		}

		if _, ok := database.Airports[dep.Destination]; !ok {
			e.ErrorString("destination airport \"%s\" unknown", dep.Destination)
		}

		// Make sure that all runways have a route to the exit
		for rwy, routes := range ap.DepartureRoutes {
			e.Push("Runway " + rwy)
			if _, ok := routes[dep.Exit]; !ok {
				e.ErrorString("exit \"%s\" not found in runway's \"departure_routes\"", dep.Exit)
			}
			e.Pop()
		}

		// We may have multiple ways to reach an exit (e.g. different for
		// jets vs piston aircraft); in that case the departure exit may be
		// specified like COLIN.P, etc.  Therefore, here we remove any
		// trailing non-alphabetical characters for the departure exit name
		// used below.
		depExit := dep.Exit
		for i, ch := range depExit {
			if !unicode.IsLetter(ch) {
				depExit = depExit[:i]
				break
			}
		}

		sawExit := false
		for _, fix := range strings.Fields(dep.Route) {
			sawExit = sawExit || fix == depExit
			wp := []Waypoint{Waypoint{Fix: fix}}
			// Best effort only to find waypoint locations; this will fail
			// for airways, international ones not in the FAA database,
			// latlongs in the flight plan, etc.
			if fix == depExit {
				sg.InitializeWaypointLocations(wp, e)
			} else {
				// nil here so errors aren't logged if it's not the actual exit.
				sg.InitializeWaypointLocations(wp, nil)
			}
			if !wp[0].Location.IsZero() {
				ap.Departures[i].RouteWaypoints = append(ap.Departures[i].RouteWaypoints, wp[0])
			}
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

	for rwy, def := range ap.ApproachRegions {
		e.Push(rwy + " region")
		def.Runway = rwy

		idx := FindIf(ap.ConvergingRunways,
			func(c ConvergingRunways) bool { return c.Runways[0] == rwy || c.Runways[1] == rwy })
		if idx == -1 {
			e.ErrorString("runway not used in \"converging_runways\"")
		}

		e.Pop()
	}

	for i, pair := range ap.ConvergingRunways {
		e.Push("Converging runways " + pair.Runways[0] + "/" + pair.Runways[1])

		// Find the runway intersection point
		reg0, reg1 := ap.ApproachRegions[pair.Runways[0]], ap.ApproachRegions[pair.Runways[1]]
		if reg0 != nil && reg1 != nil {
			// If either is nil, we'll flag the error below, so it's fine to ignore that here.
			r0n := reg0.NearPoint(sg.NmPerLongitude, sg.MagneticVariation)
			r0f := reg0.FarPoint(sg.NmPerLongitude, sg.MagneticVariation)
			r1n := reg1.NearPoint(sg.NmPerLongitude, sg.MagneticVariation)
			r1f := reg1.FarPoint(sg.NmPerLongitude, sg.MagneticVariation)

			p, ok := LineLineIntersect(r0n, r0f, r1n, r1f)
			if ok && distance2f(p, r0n) < 10 && distance2f(p, r1n) < 10 {
				ap.ConvergingRunways[i].RunwayIntersection = nm2ll(p, sg.NmPerLongitude)
			} else {
				mid := scale2f(add2f(ll2nm(reg0.ReferencePoint, sg.NmPerLongitude),
					ll2nm(reg1.ReferencePoint, sg.NmPerLongitude)), 0.5)
				ap.ConvergingRunways[i].RunwayIntersection = nm2ll(mid, sg.NmPerLongitude)
			}
		}

		for j, rwy := range pair.Runways {
			e.Push(rwy)
			var err error
			ap.ConvergingRunways[i].LeaderDirections[j], err =
				ParseCardinalOrdinalDirection(pair.LeaderDirectionStrings[j])
			if err != nil {
				e.Error(err)
			}

			if _, ok := ap.ApproachRegions[rwy]; !ok {
				e.ErrorString("runway not defined in \"approach_regions\"")
			}
			e.Pop()
		}
		e.Pop()
	}
}

type ExitRoute struct {
	InitialRoute    string        `json:"route"`
	ClearedAltitude int           `json:"cleared_altitude"`
	Waypoints       WaypointArray `json:"waypoints"`
	Description     string        `json:"description"`
}

type Departure struct {
	Exit string `json:"exit"`

	Destination    string             `json:"destination"`
	Altitude       int                `json:"altitude,omitempty"`
	Route          string             `json:"route"`
	RouteWaypoints WaypointArray      // not specified in user JSON
	Airlines       []DepartureAirline `json:"airlines"`
	Scratchpad     string             `json:"scratchpad"` // optional
}

type DepartureAirline struct {
	ICAO  string `json:"icao"`
	Fleet string `json:"fleet,omitempty"`
}

type ApproachType int

const (
	ILSApproach = iota
	RNAVApproach
	ChartedVisualApproach
)

func (at ApproachType) String() string {
	return []string{"ILS", "RNAV", "Charted Visual"}[at]
}

func (at ApproachType) MarshalJSON() ([]byte, error) {
	switch at {
	case ILSApproach:
		return []byte("\"ILS\""), nil
	case RNAVApproach:
		return []byte("\"RNAV\""), nil
	case ChartedVisualApproach:
		return []byte("\"Visual\""), nil
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

	case "\"Visual\"":
		*at = ChartedVisualApproach
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

func (ap *Approach) Heading(nmPerLongitude, magneticVariation float32) float32 {
	p := ap.Line()
	return headingp2ll(p[0], p[1], nmPerLongitude, magneticVariation)
}

// airport.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

type Airport struct {
	Location       Point2LL
	TowerListIndex int `json:"tower_list"`

	Name string `json:"name"`

	Approaches map[string]*Approach `json:"approaches,omitempty"`
	Departures []Departure          `json:"departures,omitempty"`

	// Optional: initial tracking controller, for cases where a virtual
	// controller has the initial track.
	DepartureController string `json:"departure_controller"`

	ExitCategories map[string]string `json:"exit_categories"`

	// runway -> (exit -> route)
	DepartureRoutes map[string]map[string]ExitRoute `json:"departure_routes"`

	ApproachRegions   map[string]*ApproachRegion `json:"approach_regions"`
	ConvergingRunways []ConvergingRunways        `json:"converging_runways"`

	ATPAVolumes map[string]*ATPAVolume `json:"atpa_volumes"`
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

type ATPAVolume struct {
	Id                  string // Unique identifier, set after deserialization
	ThresholdString     string `json:"runway_threshold"`
	Threshold           Point2LL
	Heading             float32  `json:"heading"`
	MaxHeadingDeviation float32  `json:"max_heading_deviation"`
	Floor               float32  `json:"floor"`
	Ceiling             float32  `json:"ceiling"`
	Length              float32  `json:"length"`
	LeftWidth           float32  `json:"left_width"`
	RightWidth          float32  `json:"right_width"`
	FilteredScratchpads []string `json:"filtered_scratchpads"`
	ExcludedScratchpads []string `json:"excluded_scratchpads"`
	Enable25nmApproach  bool     `json:"enable_2.5nm"`
	Dist25nmApproach    float32  `json:"2.5nm_distance"`
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
	if !PointInPolygon2LL(track.Position, quad[:]) {
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
			if !slices.ContainsFunc(ar.ScratchpadPatterns,
				func(pat string) bool { return strings.Contains(scratchpad, pat) }) {
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

func (a *ATPAVolume) Inside(p Point2LL, alt, hdg, nmPerLongitude, magneticVariation float32) bool {
	if alt < a.Floor || alt > a.Ceiling {
		return false
	}
	if headingDifference(hdg, a.Heading) > a.MaxHeadingDeviation {
		return false
	}

	rect := a.GetRect(nmPerLongitude, magneticVariation)
	return PointInPolygon2LL(p, rect[:])
}

func (a *ATPAVolume) GetRect(nmPerLongitude, magneticVariation float32) [4]Point2LL {
	// Segment along the approach course
	p0 := ll2nm(a.Threshold, nmPerLongitude)
	hdg := a.Heading - magneticVariation + 180
	v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
	p1 := add2f(p0, scale2f(v, a.Length))

	vp := [2]float32{-v[1], v[0]} // perp
	left, right := a.LeftWidth/NauticalMilesToFeet, a.RightWidth/NauticalMilesToFeet

	quad := [4][2]float32{
		add2f(p0, scale2f(vp, -left)), add2f(p1, scale2f(vp, -left)),
		add2f(p1, scale2f(vp, right)), add2f(p0, scale2f(vp, right))}
	return [4]Point2LL{
		nm2ll(quad[0], nmPerLongitude), nm2ll(quad[1], nmPerLongitude),
		nm2ll(quad[2], nmPerLongitude), nm2ll(quad[3], nmPerLongitude)}
}

func (ap *Airport) PostDeserialize(icao string, sg *ScenarioGroup, e *ErrorLogger) {
	if info, ok := database.Airports[icao]; !ok {
		e.ErrorString("airport \"%s\" not found in airport database", icao)
	} else {
		ap.Location = info.Location
	}

	if ap.Location.IsZero() {
		e.ErrorString("Must specify \"location\" for airport")
	}

	for name, appr := range ap.Approaches {
		e.Push("Approach " + name)

		if isAllNumbers(name) {
			e.ErrorString("Approach names cannot only have numbers in them")
		}

		if appr.Id != "" {
			if wps, ok := database.Airports[icao].Approaches[appr.Id]; !ok {
				e.ErrorString("Approach \"%s\" not in database. Options: %s", appr.Id,
					strings.Join(SortedMapKeys(database.Airports[icao].Approaches), ", "))
				e.Pop()
				continue
			} else {
				if appr.Id[0] == 'H' || appr.Id[0] == 'R' {
					appr.Type = RNAVApproach
				} else {
					appr.Type = ILSApproach // close enough
				}
				// RZ22L -> 22L
				for i, ch := range appr.Id {
					if ch >= '1' && ch <= '9' {
						appr.Runway = appr.Id[i:]
						break
					}
				}
				if len(appr.Runway) == 0 {
					e.ErrorString("unable to convert approach id \"%s\" to runway", appr.Id)
				}

				appr.Waypoints = wps
			}
		}

		if appr.Runway == "" {
			e.ErrorString("Must specify \"runway\"")
		}
		rwy, ok := LookupRunway(icao, appr.Runway)
		if !ok {
			e.ErrorString("\"runway\" \"%s\" is unknown. Options: %s", appr.Runway,
				database.Airports[icao].ValidRunways())
		}

		for i := range appr.Waypoints {
			sg.InitializeWaypointLocations(appr.Waypoints[i], e)

			// Add the final fix at the runway threshold.
			appr.Waypoints[i] = append(appr.Waypoints[i], Waypoint{
				Fix:      appr.Runway,
				Location: rwy.Threshold,
				AltitudeRestriction: &AltitudeRestriction{
					Range: [2]float32{float32(rwy.Elevation), float32(rwy.Elevation)},
				},
				Delete: true,
			})
			n := len(appr.Waypoints[i])

			if appr.Waypoints[i][n-1].ProcedureTurn != nil {
				e.ErrorString("ProcedureTurn cannot be specified at the final waypoint")
			}
			for j, wp := range appr.Waypoints[i] {
				e.Push("Fix " + wp.Fix)
				if wp.NoPT {
					if !slices.ContainsFunc(appr.Waypoints[i][j+1:],
						func(wp Waypoint) bool { return wp.ProcedureTurn != nil }) {
						e.ErrorString("No procedure turn found after fix with \"nopt\"")
					}
				}
				e.Pop()
			}

			appr.Waypoints[i].CheckApproach(e)
		}

		if appr.FullName == "" {
			switch appr.Type {
			case ILSApproach:
				appr.FullName = "ILS Runway " + appr.Runway
			case RNAVApproach:
				appr.FullName = "RNAV Runway " + appr.Runway
			case ChartedVisualApproach:
				e.ErrorString("Must provide \"full_name\" for charted visual approach")
			}
		} else if !strings.Contains(appr.FullName, "runway") && !strings.Contains(appr.FullName, "Runway") {
			e.ErrorString("Must have \"runway\" in approach's \"full_name\"")
		}

		if appr.TowerController == "" {
			appr.TowerController = icao[1:] + "_TWR"
			if _, ok := sg.ControlPositions[appr.TowerController]; !ok {
				e.ErrorString("No position specified for \"tower_controller\" and \"" +
					appr.TowerController + "\" is not a valid controller")
			}
		} else if _, ok := sg.ControlPositions[appr.TowerController]; !ok {
			e.ErrorString("No control position \"" + appr.TowerController + "\" for \"tower_controller\"")
		}

		if appr.Type == ChartedVisualApproach && len(appr.Waypoints) != 1 {
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

		r, ok := LookupRunway(icao, rwy)
		if !ok {
			e.ErrorString("unknown runway for airport")
		}
		rend, ok := LookupOppositeRunway(icao, rwy)
		if !ok {
			e.ErrorString("missing opposite runway")
		}

		for exitList, route := range rwyRoutes {
			e.Push("Exit " + exitList)
			sg.InitializeWaypointLocations(route.Waypoints, e)

			route.Waypoints = append([]Waypoint{
				Waypoint{
					Fix:      rwy,
					Location: r.Threshold,
				},
				Waypoint{
					Fix:      rwy + "-mid",
					Location: lerp2f(0.75, r.Threshold, rend.Threshold),
				}}, route.Waypoints...)

			route.Waypoints.CheckDeparture(e)

			for _, exit := range strings.Split(exitList, ",") {
				exit = strings.TrimSpace(exit)
				if _, ok := seenExits[exit]; ok {
					e.ErrorString("%s: exit repeatedly specified in routes", exit)
				}
				seenExits[exit] = nil

				splitDepartureRoutes[rwy][exit] = route
			}
			e.Pop()
		}
		e.Pop()
	}
	ap.DepartureRoutes = splitDepartureRoutes

	// Make sure if departures are initially controlled by a virtual
	// controller, all routes have a valid handoff controller (and the
	// converse).
	for rwy, routes := range ap.DepartureRoutes {
		e.Push("Departure runway " + rwy)
		for exit, route := range routes {
			e.Push("Exit " + exit)

			if ap.DepartureController != "" {
				if route.HandoffController == "" {
					e.ErrorString("no \"handoff_controller\" specified even though airport has a \"departure_controller\"")
				} else if _, ok := sg.ControlPositions[route.HandoffController]; !ok {
					e.ErrorString("control position \"%s\" unknown in scenario", route.HandoffController)
				}
			} else if route.HandoffController != "" {
				e.ErrorString("\"handoff_controller\" specified but won't be used since airport has no \"departure_controller\"")
			}

			if route.AssignedAltitude == 0 && route.ClearedAltitude == 0 {
				e.ErrorString("must specify either \"assigned_altitude\" or \"cleared_altitude\"")
			} else if route.AssignedAltitude != 0 && route.ClearedAltitude != 0 {
				e.ErrorString("cannot specify both \"assigned_altitude\" and \"cleared_altitude\"")
			}

			e.Pop()
		}
		e.Pop()
	}

	for i, dep := range ap.Departures {
		e.Push("Departure exit " + dep.Exit)
		e.Push("Destination " + dep.Destination)

		if _, ok := sg.STARSFacilityAdaptation.Scratchpads[dep.Exit]; dep.Scratchpad == "" && !ok {
			e.ErrorString("exit not in scenario group \"scratchpads\"")
		}

		if dep.Altitude < 500 {
			e.ErrorString("altitude of %v is too low to be used. Is it supposed to be %v?",dep.Altitude, dep.Altitude*100)
		}

		if _, ok := database.Airports[dep.Destination]; !ok {
			e.ErrorString("destination airport \"%s\" unknown", dep.Destination)
		}

		if len(dep.Airlines) == 0 {
			e.ErrorString("No \"airlines\" specified for departure")
		}

		// Make sure that all runways have a route to the exit
		for rwy := range ap.DepartureRoutes {
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy, database.Airports[icao].ValidRunways())
			}
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

		if _, ok := LookupRunway(icao, rwy); !ok {
			e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy,
				database.Airports[icao].ValidRunways())
		}

		if !slices.ContainsFunc(ap.ConvergingRunways,
			func(c ConvergingRunways) bool { return c.Runways[0] == rwy || c.Runways[1] == rwy }) {
			e.ErrorString("runway not used in \"converging_runways\"")
		}

		e.Pop()
	}

	for i, pair := range ap.ConvergingRunways {
		e.Push("Converging runways " + pair.Runways[0] + "/" + pair.Runways[1])

		for _, rwy := range pair.Runways {
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy, database.Airports[icao].ValidRunways())
			}
		}

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

	// Generate reasonable default ATPA volumes for any runways they aren't
	// specified for.
	if ap.ATPAVolumes == nil {
		ap.ATPAVolumes = make(map[string]*ATPAVolume)
	}
	for _, rwy := range database.Airports[icao].Runways {
		if _, ok := ap.ATPAVolumes[rwy.Id]; !ok {
			// Make a default volume
			ap.ATPAVolumes[rwy.Id] = &ATPAVolume{
				Id:        rwy.Id,
				Threshold: rwy.Threshold,
				Heading:   rwy.Heading,
			}
		}
	}

	for rwy, vol := range ap.ATPAVolumes {
		e.Push("ATPA " + rwy)

		vol.Id = icao + rwy

		if _, ok := LookupRunway(icao, rwy); !ok {
			e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy, database.Airports[icao].ValidRunways())
		}

		if vol.Threshold.IsZero() { // the location is set directly for default volumes
			if vol.ThresholdString == "" {
				e.ErrorString("\"runway_threshold\" not specified.")
			} else {
				var ok bool
				if vol.Threshold, ok = sg.locate(vol.ThresholdString); !ok {
					e.ErrorString("\"%s\" unknown for \"runway_threshold\".", vol.ThresholdString)
				}
			}
		}

		// Defaults if things are not specified
		if vol.MaxHeadingDeviation == 0 {
			vol.MaxHeadingDeviation = 90
		}
		if vol.Floor == 0 {
			vol.Floor = float32(database.Airports[icao].Elevation + 100)
		}
		if vol.Ceiling == 0 {
			vol.Ceiling = float32(database.Airports[icao].Elevation + 5000)
		}
		if vol.Length == 0 {
			vol.Length = 15
		}
		if vol.LeftWidth == 0 {
			vol.LeftWidth = 2000
		}
		if vol.RightWidth == 0 {
			vol.RightWidth = 2000
		}

		e.Pop()
	}
}

type ExitRoute struct {
	SID              string        `json:"sid"`
	AssignedAltitude int           `json:"assigned_altitude"`
	ClearedAltitude  int           `json:"cleared_altitude"`
	Waypoints        WaypointArray `json:"waypoints"`
	Description      string        `json:"description"`
	// optional, control position to handoff to at a /ho
	HandoffController string `json:"handoff_controller"`
}

type Departure struct {
	Exit string `json:"exit"`

	Destination         string             `json:"destination"`
	Altitude            int                `json:"altitude,omitempty"`
	Route               string             `json:"route"`
	RouteWaypoints      WaypointArray      // not specified in user JSON
	Airlines            []DepartureAirline `json:"airlines"`
	Scratchpad          string             `json:"scratchpad"`           // optional
	SecondaryScratchpad string             `json:"secondary_scratchpad"` // optional
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
	Id              string          `json:"cifp_id"`
	FullName        string          `json:"full_name"`
	Type            ApproachType    `json:"type"`
	Runway          string          `json:"runway"`
	Waypoints       []WaypointArray `json:"waypoints"`
	TowerController string          `json:"tower_controller"`
}

func (ap *Approach) Line() [2]Point2LL {
	// assume we have at least one set of waypoints and that it has >= 2 waypoints!
	wp := ap.Waypoints[0]

	// use the last two waypoints of the approach
	n := len(wp)
	return [2]Point2LL{wp[n-2].Location, wp[n-1].Location}
}

func (ap *Approach) Heading(nmPerLongitude, magneticVariation float32) float32 {
	p := ap.Line()
	return headingp2ll(p[0], p[1], nmPerLongitude, magneticVariation)
}

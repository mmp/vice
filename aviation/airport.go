// pkg/aviation/airport.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/brunoga/deep"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type Airport struct {
	Location       math.Point2LL
	TowerListIndex int `json:"tower_list"`

	Approaches map[string]*Approach `json:"approaches,omitempty"`
	// Departures is optional: an airport that only ever sees published
	// traffic needs no more than its "departure_routes" to fly it.
	Departures []Departure `json:"departures,omitempty"`

	VFR struct {
		Randoms VFRRandomsSpec `json:"random_routes"`
		Routes  []VFRRouteSpec `json:"routes"`
	} `json:"vfr"`

	// Optional: initial tracking controller, for cases where a virtual
	// controller has the initial track.
	DepartureController ControlPosition `json:"departure_controller"`
	HoldForRelease      bool            `json:"hold_for_release"`

	ExitCategories map[ExitID]string `json:"exit_categories"`

	// runway -> (exit -> routes)
	DepartureRoutes map[RunwayID]map[ExitID]ExitRoutes `json:"departure_routes"`

	// TrafficRoutes gives the routes published traffic between this airport
	// and specific other airports flies.
	TrafficRoutes TrafficRoutes `json:"traffic_routes"`

	CRDARegions map[string]*CRDARegion `json:"crda_regions"`
	CRDAPairs   []CRDAPair             `json:"crda_pairs"`

	ATPAVolumes           map[string]*ATPAVolume `json:"atpa_volumes"`
	OmitArrivalScratchpad bool                   `json:"omit_arrival_scratchpad"`
	DepartureRunwaysAsOne []string               `json:"departure_runways_as_one"`
}

// icaoRegionPrefixes are the leading letters of the ICAO ids in the regions
// the FAA works: the contiguous US (K), Alaska, Hawaii, and the Pacific
// territories (P), and Puerto Rico and the Virgin Islands (T). Airports in
// those regions are named domestically by their ICAO id without it.
const icaoRegionPrefixes = "KPT"

// TrimICAOPrefix returns the domestic name of an airport's ICAO id, dropping
// the leading region letter. Ids from elsewhere are returned unchanged.
func TrimICAOPrefix(icao string) string {
	if len(icao) == 4 && strings.ContainsRune(icaoRegionPrefixes, rune(icao[0])) {
		return icao[1:]
	}
	return icao
}

type VFRRandomsSpec struct {
	Rate  float32 `json:"rate"`
	Fleet string  `json:"fleet"`
}

type VFRRouteSpec struct {
	Name        string        `json:"name"`
	Rate        float32       `json:"rate"`
	Fleet       string        `json:"fleet"`
	Waypoints   WaypointArray `json:"waypoints"`
	Destination string        `json:"destination"`
	Description string        `json:"description"`
}

// CRDAPair describes a one-directional ghosting relationship between two
// CRDA regions. Aircraft flying through SourceRegion's qualification volume
// have ghost data blocks plotted on GhostRegion's centerline; to ghost in
// both directions, define two pairs with the roles swapped.
type CRDAPair struct {
	SourceRegion             string  `json:"source_region"`
	GhostRegion              string  `json:"ghost_region"`
	SourceLeaderDirectionStr string  `json:"source_leader_direction"`
	GhostLeaderDirectionStr  string  `json:"ghost_leader_direction"`
	TieSymbol                string  `json:"tie_symbol"`
	StaggerSymbol            string  `json:"stagger_symbol"`
	TieOffset                float32 `json:"tie_offset"`

	// Set during deserialize.
	SourceLeaderDirection math.CardinalOrdinalDirection
	GhostLeaderDirection  math.CardinalOrdinalDirection
	ConvergencePoint      math.Point2LL
}

type GhostTrack struct {
	ADSBCallsign        ADSBCallsign
	Position            math.Point2LL
	Groundspeed         int
	LeaderLineDirection math.CardinalOrdinalDirection
	TrackId             string
}

func (ar *CRDARegion) Inside(p math.Point2LL, alt float32, nmPerLongitude float32) (lateral, vertical bool) {
	pNM := math.LL2NM(p, nmPerLongitude)
	dist, perpOffset, _ := ar.Path.Project(pNM)

	// Lateral check: must be within [NearDistance, NearDistance+RegionLength]
	// and within interpolated half-width at that distance
	if dist < ar.NearDistance || dist > ar.NearDistance+ar.RegionLength {
		return
	}
	t := (dist - ar.NearDistance) / ar.RegionLength
	halfWidth := math.Lerp(t, ar.NearHalfWidth, ar.FarHalfWidth)
	if math.Abs(perpOffset) > halfWidth {
		return
	}
	lateral = true

	// Vertical check
	if dist > ar.DescentPointDistance {
		vertical = alt <= ar.DescentPointAltitude+ar.AboveAltitudeTolerance &&
			alt >= ar.DescentPointAltitude-ar.BelowAltitudeTolerance
	} else if ar.DescentPointDistance > ar.NearDistance {
		vt := (dist - ar.NearDistance) / (ar.DescentPointDistance - ar.NearDistance)
		approachAlt := math.Lerp(vt, ar.ReferencePointAltitude, ar.DescentPointAltitude)
		vertical = alt <= approachAlt+ar.AboveAltitudeTolerance &&
			alt >= approachAlt-ar.BelowAltitudeTolerance
	}
	return
}

func (ar *CRDARegion) TryMakeGhost(trk RadarTrack, heading float32,
	scratchpad string, forceGhost bool, offset float32, leaderDirection math.CardinalOrdinalDirection,
	nmPerLongitude float32, other *CRDARegion) *GhostTrack {
	// Start with lateral extent since even if it's forced, the aircraft still must be inside it.
	lat, vert := ar.Inside(trk.Location, float32(trk.TrueAltitude), nmPerLongitude)
	if !lat {
		return nil
	}

	if !forceGhost {
		// Heading must be in range
		pNM := math.LL2NM(trk.Location, nmPerLongitude)
		_, _, pathHeading := ar.Path.Project(pNM)
		if math.HeadingDifference(heading, pathHeading) > ar.HeadingTolerance {
			return nil
		}

		// Check vertical extent
		if !vert {
			return nil
		}

		if len(ar.ScratchpadPatterns) > 0 {
			if !slices.ContainsFunc(ar.ScratchpadPatterns,
				func(pat string) bool { return strings.Contains(scratchpad, pat) }) {
				return nil
			}
		}
	}

	// Project aircraft onto source path
	pNM := math.LL2NM(trk.Location, nmPerLongitude)
	pathDist, perpOffset, _ := ar.Path.Project(pNM)

	// Compute distance from convergence point
	convDist := ar.DistToConvergence + (ar.Path.Length - pathDist)

	// Map to target path: find the point at the same convergence distance
	targetDist := other.Path.Length - convDist + other.DistToConvergence
	ghostPt, ghostHeading := other.Path.PointAtDistance(targetDist)

	// Apply perpendicular offset (preserve aircraft's offset from centerline)
	perpRad := math.Radians(ghostHeading - 90)
	perpVec := math.SinCos(perpRad)
	ghostPt = math.Add2f(ghostPt, math.Scale2f(perpVec, perpOffset))

	// Apply tie offset along forward direction
	if offset != 0 {
		fwdRad := math.Radians(ghostHeading)
		fwdVec := math.SinCos(fwdRad)
		ghostPt = math.Add2f(ghostPt, math.Scale2f(fwdVec, offset))
	}

	return &GhostTrack{
		ADSBCallsign:        trk.ADSBCallsign,
		Position:            math.NM2LL(ghostPt, nmPerLongitude),
		Groundspeed:         int(trk.Groundspeed),
		LeaderLineDirection: leaderDirection,
	}
}

func (a *ATPAVolume) Inside(p math.Point2LL, alt float32, hdg math.MagneticHeading, nmPerLongitude, magneticVariation float32) bool {
	if alt < a.Floor || alt > a.Ceiling {
		return false
	}
	if math.HeadingDifference(hdg, a.Heading) > a.MaxHeadingDeviation {
		return false
	}

	rect := a.GetRect(nmPerLongitude, magneticVariation)
	return math.PointInPolygon2LL(p, rect[:])
}

func (a *ATPAVolume) GetRect(nmPerLongitude, magneticVariation float32) [4]math.Point2LL {
	// Segment along the approach course
	p0 := math.LL2NM(a.Threshold, nmPerLongitude)
	hdg := float32(math.MagneticToTrue(math.OppositeHeading(a.Heading), magneticVariation))
	v := math.SinCos(math.Radians(hdg))
	p1 := math.Add2f(p0, math.Scale2f(v, a.Length))

	vp := [2]float32{-v[1], v[0]} // perp
	left, right := a.LeftWidth/math.NauticalMilesToFeet, a.RightWidth/math.NauticalMilesToFeet

	quad := [4][2]float32{
		math.Add2f(p0, math.Scale2f(vp, -left)), math.Add2f(p1, math.Scale2f(vp, -left)),
		math.Add2f(p1, math.Scale2f(vp, right)), math.Add2f(p0, math.Scale2f(vp, right))}
	return [4]math.Point2LL{
		math.NM2LL(quad[0], nmPerLongitude), math.NM2LL(quad[1], nmPerLongitude),
		math.NM2LL(quad[2], nmPerLongitude), math.NM2LL(quad[3], nmPerLongitude)}
}

func (ap *Airport) PostDeserialize(icao string, loc Locator, nmPerLongitude float32,
	magneticVariation float32, controlPositions map[ControlPosition]*Controller, scratchpads map[string]string,
	facilityAirports map[string]*Airport, checkScratchpad func(string) bool, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if info, ok := DB.Airports[icao]; !ok {
		e.ErrorString("airport %q not found in airport database", icao)
	} else {
		ap.Location = info.Location

		if len(info.Runways) == 0 {
			e.ErrorString("no runways found at %q", icao)
		}
	}

	if ap.Location.IsZero() {
		e.ErrorString(`Must specify "location" for airport`)
	}

	for name, appr := range ap.Approaches {
		e.Push("Approach " + name)

		if util.IsAllNumbers(name) {
			e.ErrorString("Approach names cannot only have numbers in them")
		}

		if appr.Id != "" {
			if dbAppr, ok := DB.Airports[icao].Approaches[appr.Id]; !ok {
				e.ErrorString("Approach %q not in database. Options: %s", appr.Id,
					strings.Join(util.SortedMapKeys(DB.Airports[icao].Approaches), ", "))
				e.Pop()
				continue
			} else {
				// Copy the approach from the database, doing checks to
				// make sure bogus overrides haven't been specified.
				if appr.Type != UnknownApproach {
					e.ErrorString(`"type" cannot be given with "cifp_id" approaches`)
				}
				appr.Type = dbAppr.Type

				if len(appr.Waypoints) > 0 {
					e.ErrorString(`"waypoints" cannot be given with "cifp_id" approaches`)
				}

				if dbAppr.Runway == "" {
					if appr.Runway == "" {
						e.ErrorString(`"runway" must be specified: the CIFP approach is not runway-specific`)
					}
				} else if appr.Runway != "" && appr.Runway != dbAppr.Runway {
					e.ErrorString(`specified "runway" doesn't match the one %q in the CIFP approach`, dbAppr.Runway)
				} else {
					appr.Runway = dbAppr.Runway
				}

				// Deep-copy the waypoint arrays so per-scenario mutations (appending the threshold
				// waypoint, etc.) don't race with other scenarios that reference the same CIFP
				// approach.
				for _, wps := range dbAppr.Waypoints {
					appr.Waypoints = append(appr.Waypoints, deep.MustCopy(wps))
				}
			}
		} else {
			if appr.Type == UnknownApproach {
				e.ErrorString(`Must specify "type"`)
			}
			if appr.Runway == "" {
				e.ErrorString(`Must specify "runway"`)
			}
			if len(appr.Waypoints) == 0 {
				e.ErrorString(`Must specify "waypoints"`)
			}
		}
		appr.InitializeWaypoints(icao, loc, nmPerLongitude, magneticVariation, e)

		for i := range appr.Waypoints {
			n := len(appr.Waypoints[i])

			if appr.Waypoints[i][n-1].ProcedureTurn() != nil {
				e.ErrorString("ProcedureTurn cannot be specified at the final waypoint")
			}
			for j, wp := range appr.Waypoints[i] {
				e.Push("Fix " + wp.Fix)
				if wp.ProcedureTurn() != nil &&
					(appr.Type == VisualApproach || appr.Type == ChartedVisualApproach) {
					e.ErrorString("ProcedureTurn cannot be specified on a visual approach")
				}
				if wp.NoPT() {
					if !slices.ContainsFunc(appr.Waypoints[i][j+1:],
						func(wp Waypoint) bool { return wp.ProcedureTurn() != nil }) {
						e.ErrorString(`No procedure turn found after fix with "nopt"`)
					}
				}
				e.Pop()
			}
		}
		requireFAF := appr.Type != ChartedVisualApproach && appr.Type != VisualApproach
		CheckApproaches(e, appr.Waypoints, requireFAF, controlPositions, checkScratchpad)

		if appr.FullName == "" {
			if appr.Type == ChartedVisualApproach {
				e.ErrorString(`Must provide "full_name" for charted visual approach`)
			} else {
				appr.FullName = appr.Type.String() + " "
				if len(appr.Id) >= 3 && appr.Id[1] >= 'W' && appr.Id[1] <= 'Z' {
					appr.FullName += string(appr.Id[1]) + " "
				}
				if len(appr.Id) >= 3 && appr.Id[0] == 'G' {
					appr.FullName += "GPS "
				}
				appr.FullName += "Runway " + appr.Runway
			}
		} else if !strings.Contains(appr.FullName, "runway") && !strings.Contains(appr.FullName, "Runway") {
			e.ErrorString(`Must have "runway" in approach's "full_name"`)
		}

		if appr.Type == ChartedVisualApproach && len(appr.Waypoints) != 1 {
			// Note: this could be relaxed if necessary but the logic in
			// Nav prepareForChartedVisual() assumes as much.
			e.ErrorString("Only a single set of waypoints are allowed for a charted visual approach route")
		}

		e.Pop()
	}

	if ap.DepartureController != "" {
		if _, ok := controlPositions[ap.DepartureController]; !ok {
			e.ErrorString("departure_controller %q unknown", ap.DepartureController)
		}
	}

	// Departure routes are specified in the JSON as comma-separated lists
	// of exits. We'll split those out into individual entries in the
	// Airport's DepartureRoutes, one per exit, for convenience of future code.
	splitDepartureRoutes := make(map[RunwayID]map[ExitID]ExitRoutes)
	for rwy, rwyRoutes := range ap.DepartureRoutes {
		e.Push("Departure runway " + string(rwy))
		seenExits := make(map[string]any)
		splitDepartureRoutes[rwy] = make(map[ExitID]ExitRoutes)

		r, ok := LookupRunway(icao, rwy.Base())
		if !ok {
			e.ErrorString("unknown runway for airport")
		}
		rend, ok := LookupOppositeRunway(icao, rwy.Base())
		if !ok {
			e.ErrorString("missing opposite runway")
		}

		for exitList, routes := range rwyRoutes {
			e.Push("Exit " + string(exitList))
			if len(routes) == 0 {
				e.ErrorString("no departure routes given")
			}

			var exits []ExitID
			for exit := range strings.SplitSeq(string(exitList), ",") {
				exit = strings.TrimSpace(exit)
				if exit == "" {
					// A trailing comma in the list; not an exit.
					continue
				}
				if _, ok := seenExits[exit]; ok {
					e.ErrorString("%s: exit repeatedly specified in routes", exit)
				}
				seenExits[exit] = nil
				exits = append(exits, ExitID(exit))
			}

			var taken AircraftClass // the classes the routes so far leave to no one else
			for i, route := range routes {
				if len(routes) > 1 {
					e.Push(fmt.Sprintf("Route %d", i+1))
				}

				if route.Aircraft.coveredBy(taken) {
					e.ErrorString("no aircraft fly the route; earlier routes take all of the ones it allows")
				}
				taken |= route.Aircraft.expand()

				if len(route.Waypoints) > 0 || route.SID == "" {
					if route.InitialHeading != 0 {
						e.ErrorString(`"initial_heading" applies only to a route taken from the CIFP; put the heading in "waypoints"`)
					}
					if len(route.WaypointActions) > 0 {
						e.ErrorString(`"waypoint_actions" applies only to a route taken from the CIFP; put the actions in "waypoints"`)
					}
					route.Waypoints = route.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)
					route.Waypoints.CheckDeparture(e, DB.Airports[icao].Elevation, controlPositions, checkScratchpad)
					route.initialize(icao, rwy, r, rend, nmPerLongitude, magneticVariation, controlPositions, e)
					for _, exit := range exits {
						splitDepartureRoutes[rwy][exit] = append(splitDepartureRoutes[rwy][exit], route)
					}
				} else {
					// The waypoints come from the CIFP's SID and depend on
					// the exit, so each exit gets its own copy of the route.
					// The checks made of scenario-authored waypoints don't
					// apply: a SID may legitimately have a 200+ nm oceanic
					// leg or a lower minimum altitude at a later fix.
					// A suffix on the SID name selects one of its enroute
					// transitions (e.g. "LAXX1.OCN"); the plain SID name is
					// what goes on the flight plan.
					var transition string
					route.SID, transition, _ = strings.Cut(route.SID, ".")
					for _, exit := range exits {
						if len(exits) > 1 {
							e.Push("Exit " + string(exit))
						}
						exitRoute := *route
						if wps, ok := sidWaypoints(icao, route.SID, transition, rwy, exit, route.InitialHeading != 0, e); ok {
							wps = route.amendSIDWaypoints(wps, e)
							exitRoute.Waypoints = wps.InitializeLocations(loc, nmPerLongitude, magneticVariation, true, e)
							for _, wp := range exitRoute.Waypoints {
								if wp.Location.IsZero() {
									e.ErrorString("%s: unable to locate SID waypoint", wp.Fix)
								}
							}
							exitRoute.Waypoints.checkBasics(e, controlPositions, checkScratchpad)
							exitRoute.initialize(icao, rwy, r, rend, nmPerLongitude, magneticVariation, controlPositions, e)
							splitDepartureRoutes[rwy][exit] = append(splitDepartureRoutes[rwy][exit], &exitRoute)
						}
						if len(exits) > 1 {
							e.Pop()
						}
					}
				}

				if len(routes) > 1 {
					e.Pop()
				}
			}
			e.Pop()
		}
		e.Pop()
	}
	ap.DepartureRoutes = splitDepartureRoutes

	e.Push(`"traffic_routes"`)
	checkTrafficRouteAirports := func(routes map[string]TrafficRouteSet) map[string]TrafficRouteSet {
		if len(routes) == 0 {
			return routes
		}
		checked := make(map[string]TrafficRouteSet, len(routes))
		for _, other := range util.SortedMapKeys(routes) {
			norm := strings.ToUpper(strings.TrimSpace(other))
			if norm == icao {
				e.ErrorString("%s: routes to or from the airport itself", other)
			} else if err := CheckAirport("traffic route", norm); err != nil {
				e.Error(err)
			} else if _, ok := checked[norm]; ok {
				e.ErrorString("%s: airport repeatedly specified", other)
			} else {
				checked[norm] = routes[other]
			}
		}
		return checked
	}
	ap.TrafficRoutes.Departures = checkTrafficRouteAirports(ap.TrafficRoutes.Departures)
	ap.TrafficRoutes.Arrivals = checkTrafficRouteAirports(ap.TrafficRoutes.Arrivals)

	checkTrafficRoute := func(r TrafficRoute) bool {
		if r.Route == "" {
			e.ErrorString("route may not be empty")
			return false
		}
		wps := RouteWaypoints(r.Route).InitializeLocations(loc, nmPerLongitude, magneticVariation,
			true /* allowSlop */, e)
		if !slices.ContainsFunc(wps, func(wp Waypoint) bool { return !wp.Location.IsZero() }) {
			e.ErrorString("%s: no locatable fixes in route", r.Route)
			return false
		}
		return true
	}
	for _, other := range util.SortedMapKeys(ap.TrafficRoutes.Departures) {
		e.Push("Departure " + other)
		for _, r := range ap.TrafficRoutes.Departures[other] {
			if checkTrafficRoute(r) && !ap.routeReachesExit(r.Route, icao) {
				e.ErrorString(`%s: route reaches no exit in "departure_routes"`, r.Route)
			}
		}
		e.Pop()
	}
	for _, other := range util.SortedMapKeys(ap.TrafficRoutes.Arrivals) {
		e.Push("Arrival " + other)
		for _, r := range ap.TrafficRoutes.Arrivals[other] {
			if !checkTrafficRoute(r) {
				continue
			}
			// A final token that looks like a procedure name must be one of
			// the airport's STARs; anything else is likely a typo.
			if token := routeProcedureToken(r.Route, icao); token != "" {
				if star, _ := RouteSTAR(r.Route, icao); star == "" {
					e.ErrorString("%s: %q matches no STAR at %s", r.Route, token, icao)
				}
			}
		}
		e.Pop()
	}
	e.Pop()

	for i, dep := range ap.Departures {
		e.Push("Departure exit " + string(dep.Exit))
		e.Push("Destination " + dep.Destination)

		for _, alt := range dep.Altitudes {
			if alt < 500 {
				e.ErrorString("altitude of %v is too low to be used. Is it supposed to be %v?", alt, alt*100)
			}
		}

		if err := CheckAirport("destination", dep.Destination); err != nil {
			e.Error(err)
		}

		// Make sure that all runways have a route to the exit
		for rwy := range ap.DepartureRoutes {
			if _, ok := LookupRunway(icao, rwy.Base()); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
			}
		}

		// Use Base() to get the canonical exit name (e.g., "COLIN" from "COLIN.P")
		depExit := dep.Exit.Base()

		if !checkScratchpad(dep.Scratchpad) {
			e.ErrorString("%s: invalid scratchpad", dep.Scratchpad)
		}
		if !checkScratchpad(dep.SecondaryScratchpad) {
			e.ErrorString("%s: invalid secondary scratchpad", dep.SecondaryScratchpad)
		}

		/*
			if _, ok := ap.ExitCategories[depExit]; !ok {
				e.ErrorString("exit %q isn't in \"exit_categories\"", depExit)
			}
		*/

		wp, err := parseWaypoints(dep.Route)
		if err != nil {
			e.Error(err)
		}

		_, intraFacility := facilityAirports[dep.Destination]
		allowSlop := !intraFacility // Make sure that the full route is valid for intra-facility.
		wp = wp.InitializeLocations(loc, nmPerLongitude, magneticVariation, allowSlop, e)
		ap.Departures[i].RouteWaypoints = wp

		if !slices.ContainsFunc(ap.Departures[i].RouteWaypoints,
			func(wp Waypoint) bool { return wp.Fix == depExit }) {
			e.ErrorString("exit %q not found in departure route", depExit)
		}

		for _, al := range dep.Airlines {
			al.Check(e)
		}

		e.Pop()
		e.Pop()
	}

	ga := DB.Airlines["N"]
	checkFleet := func(fleet, loc string) {
		if fleet == "" {
			return
		}
		if _, ok := ga.Fleets[fleet]; !ok {
			e.ErrorString("Fleet %q in %q is not a valid GA aircraft fleet. Options: %s",
				fleet, loc, strings.Join(slices.Collect(maps.Keys(ga.Fleets)), ", "))
		}
	}
	e.Push(`"vfr"`)
	if ap.VFR.Randoms.Fleet != "" {
		checkFleet(ap.VFR.Randoms.Fleet, "random_routes")
		if ap.VFR.Randoms.Rate == 0 {
			e.ErrorString(`"fleet" specified for "vfr" "random_routes" but "rate" is not specified or is zero.`)
		}
	}
	for i := range ap.VFR.Routes {
		ap.VFR.Routes[i].Waypoints =
			ap.VFR.Routes[i].Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

		spec := &ap.VFR.Routes[i]
		e.Push("routes " + spec.Name)
		if spec.Rate == 0 {
			e.ErrorString(`No "rate" specified`)
		}
		if spec.Fleet == "" {
			spec.Fleet = "default"
		} else {
			checkFleet(spec.Fleet, "routes")
		}
		if len(spec.Waypoints) == 0 {
			e.ErrorString(`must specify "waypoints"`)
		} else {
			// Convert any /land from route parsing to SequenceVFRLanding;
			// we know these are VFR routes so Land is never appropriate.
			for j := range spec.Waypoints {
				if spec.Waypoints[j].Land() {
					spec.Waypoints[j].SetLand(false)
					spec.Waypoints[j].SetSequenceVFRLanding(true)
				}
			}
			// Ensure the last waypoint always has it, even if /land
			// wasn't specified in the route.
			spec.Waypoints[len(spec.Waypoints)-1].SetSequenceVFRLanding(true)
		}
		if err := CheckAirport("destination", spec.Destination); err != nil {
			e.Error(err)
		}
		e.Pop()
	}
	e.Pop()

	// Check if airport has VFR departures but is in class B or C airspace
	if ap.VFR.Randoms.Rate > 0 || len(ap.VFR.Routes) > 0 {
		elevation := DB.Airports[icao].Elevation
		checkAllVolumes := func(volsIter iter.Seq[[]AirspaceVolume]) bool {
			return util.SeqContainsFunc(volsIter, func(vols []AirspaceVolume) bool {
				return slices.ContainsFunc(vols, func(vol AirspaceVolume) bool {
					return vol.Inside(ap.Location, elevation)
				})
			})
		}
		if checkAllVolumes(maps.Values(DB.BravoAirspace)) || checkAllVolumes(maps.Values(DB.CharlieAirspace)) {
			e.ErrorString("Airport has VFR departures specified but is located in class B or C airspace")
		}
	}

	// Validate DepartureRunwaysAsOne entries
	seenRunways := make(map[string]bool)
	for i, rwys := range ap.DepartureRunwaysAsOne {
		// Remove whitespace and any runway suffixes.
		ap.DepartureRunwaysAsOne[i] = strings.Join(util.MapSlice(strings.Split(rwys, ","),
			func(r string) string { return RunwayID(r).Base() }), ",")

		e.Push(fmt.Sprintf("departure_runways_as_one[%d]", i))
		runways := strings.Split(ap.DepartureRunwaysAsOne[i], ",")
		if len(runways) < 2 {
			e.ErrorString("must specify at least two runways")
		}
		for _, rwy := range runways {
			rwy = strings.TrimSpace(rwy)
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
			}
			if seenRunways[rwy] {
				e.ErrorString("runway %q appears in multiple groups", rwy)
			}
			seenRunways[rwy] = true
		}
		e.Pop()
	}

	for name, def := range ap.CRDARegions {
		e.Push(name + " CRDA region")
		def.Name = name

		hasRefLine := !def.ReferencePoint.IsZero() || def.ReferenceLineHeading != 0 || def.ReferenceLineLength != 0
		hasRefRoute := def.ReferenceRoute != ""

		if hasRefLine && hasRefRoute {
			e.ErrorString(`cannot specify both reference line fields and "reference_route"`)
		} else if !hasRefLine && !hasRefRoute {
			e.ErrorString(`must specify either reference line fields or "reference_route"`)
		} else if hasRefRoute {
			if def.RegionLength != 0 {
				e.ErrorString(`"region_length" must not be specified with "reference_route"`)
			}
			routePoints := parseCRDARoute(def.ReferenceRoute, loc, nmPerLongitude, magneticVariation, e)
			def.Path = PathFromRoutePoints(routePoints, nmPerLongitude)
			def.RegionLength = def.Path.Length - def.NearDistance
		} else {
			def.Path = PathFromReferenceLine(def.ReferencePoint, def.ReferenceLineHeading,
				def.ReferenceLineLength, nmPerLongitude, magneticVariation)
		}
		if !slices.ContainsFunc(ap.CRDAPairs,
			func(c CRDAPair) bool { return c.SourceRegion == name || c.GhostRegion == name }) {
			e.ErrorString(`region not used in "crda_pairs"`)
		}

		e.Pop()
	}

	for i, pair := range ap.CRDAPairs {
		e.Push("CRDA pair " + pair.SourceRegion + "/" + pair.GhostRegion)

		srcReg := ap.CRDARegions[pair.SourceRegion]
		ghostReg := ap.CRDARegions[pair.GhostRegion]
		if srcReg == nil {
			e.ErrorString(`region %q not defined in "crda_regions"`, pair.SourceRegion)
		}
		if ghostReg == nil {
			e.ErrorString(`region %q not defined in "crda_regions"`, pair.GhostRegion)
		}

		// Find the convergence point by extending each path's final
		// segment as a straight line and computing line-line intersection.
		if srcReg != nil && ghostReg != nil {
			// Get final points and headings for both paths
			pSrc, hSrc := srcReg.Path.PointAtDistance(srcReg.Path.Length)
			pGhost, hGhost := ghostReg.Path.PointAtDistance(ghostReg.Path.Length)

			// Extend lines along final headings
			vSrc := math.SinCos(math.Radians(hSrc))
			vGhost := math.SinCos(math.Radians(hGhost))
			pSrcFar := math.Add2f(pSrc, math.Scale2f(vSrc, 20))
			pGhostFar := math.Add2f(pGhost, math.Scale2f(vGhost, 20))

			p, ok := math.LineLineIntersect(pSrc, pSrcFar, pGhost, pGhostFar)
			if ok && math.Distance2f(p, pSrc) < 20 && math.Distance2f(p, pGhost) < 20 {
				ap.CRDAPairs[i].ConvergencePoint = math.NM2LL(p, nmPerLongitude)
			} else {
				mid := math.Scale2f(math.Add2f(pSrc, pGhost), 0.5)
				ap.CRDAPairs[i].ConvergencePoint = math.NM2LL(mid, nmPerLongitude)
			}

			// Compute distToConvergence for each region
			convNM := math.LL2NM(ap.CRDAPairs[i].ConvergencePoint, nmPerLongitude)
			srcReg.DistToConvergence = math.Distance2f(pSrc, convNM)
			ghostReg.DistToConvergence = math.Distance2f(pGhost, convNM)
		}

		parseLeader := func(name, s string, dst *math.CardinalOrdinalDirection) {
			e.Push(name)
			d, err := math.ParseCardinalOrdinalDirection(s)
			if err != nil {
				e.Error(err)
			}
			*dst = d
			e.Pop()
		}
		parseLeader(pair.SourceRegion, pair.SourceLeaderDirectionStr, &ap.CRDAPairs[i].SourceLeaderDirection)
		parseLeader(pair.GhostRegion, pair.GhostLeaderDirectionStr, &ap.CRDAPairs[i].GhostLeaderDirection)
		e.Pop()
	}

	// Generate reasonable default ATPA volumes for any runways they aren't
	// specified for.
	if ap.ATPAVolumes == nil {
		ap.ATPAVolumes = make(map[string]*ATPAVolume)
	}
	for _, rwy := range DB.Airports[icao].Runways {
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

		if vol.Id == "" {
			vol.Id = rwy
		}

		if r, ok := LookupRunway(icao, rwy); !ok {
			e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
		} else {
			if vol.Threshold.IsZero() {
				if vol.ThresholdString != "" {
					var ok bool
					if vol.Threshold, ok = loc.Locate(vol.ThresholdString); !ok {
						e.ErrorString(`%q unknown for "runway_threshold".`, vol.ThresholdString)
					}
				} else {
					vol.Threshold = r.Threshold
				}
			}
			if vol.Heading == 0 {
				vol.Heading = r.Heading
			}
		}

		// Defaults if things are not specified
		if vol.MaxHeadingDeviation == 0 {
			vol.MaxHeadingDeviation = 90
		}
		if vol.Floor == 0 {
			vol.Floor = float32(DB.Airports[icao].Elevation + 100)
		}
		if vol.Ceiling == 0 {
			vol.Ceiling = float32(DB.Airports[icao].Elevation + 5000)
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

func (ap Airport) HasIFROperations() bool {
	return len(ap.Approaches) > 0 || len(ap.DepartureRoutes) > 0
}

func (ap Airport) VFRRateSum() float32 {
	sum := ap.VFR.Randoms.Rate
	for _, spec := range ap.VFR.Routes {
		sum += spec.Rate
	}
	return sum
}

type ExitRoute struct {
	SID              string            `json:"sid"`
	AssignedAltitude int               `json:"assigned_altitude"`
	ClearedAltitude  int               `json:"cleared_altitude"`
	Waypoints        WaypointArray     `json:"waypoints"`
	Description      string            `json:"description"`
	IsRNAV           bool              `json:"is_rnav"`
	HoldForRelease   bool              `json:"hold_for_release"`
	InitialHeading   int               `json:"initial_heading"` // tower-assigned
	WaypointActions  map[string]string `json:"waypoint_actions"`
	// optional, control position to handoff to at a /ho
	HandoffController ControlPosition `json:"handoff_controller"`
	// optional, the initial tracking controller for the departure.
	DepartureController ControlPosition `json:"departure_controller"`

	// Aircraft restricts which types of flights take the route; it's used
	// when an exit routes them differently.
	Aircraft AircraftClass `json:"aircraft,omitempty"`

	WaitToContactDeparture bool // whether the aircraft waits until a /TC point to contact departure
}

// ExitRoutes gives the ways departures leave via an exit; a bare JSON object
// is shorthand for a single route open to all aircraft. A departure flies the
// first route its aircraft type is admitted to, so the routes need not cover
// every type: one with no route is not launched.
type ExitRoutes []*ExitRoute

func (er *ExitRoutes) UnmarshalJSON(b []byte) error {
	var routes util.SingleOrArray[*ExitRoute]
	if err := routes.UnmarshalJSON(b); err != nil {
		return err
	}
	*er = ExitRoutes(routes)
	return nil
}

// CheckJSONErrors checks the routes in either form themselves so that a
// misspelled member is reported as such rather than as an unexpected shape.
func (er *ExitRoutes) CheckJSONErrors(json any, e *util.ErrorLogger) {
	array, ok := json.([]any)
	if !ok {
		util.TypeCheckJSONErrors[ExitRoute](json, e)
		return
	}
	for i, route := range array {
		e.Push(fmt.Sprintf("Route %d", i+1))
		util.TypeCheckJSONErrors[ExitRoute](route, e)
		e.Pop()
	}
}

// ForAircraft returns the route the given aircraft type flies, or nil if none
// of the routes takes it.
func (er ExitRoutes) ForAircraft(acType string) *ExitRoute {
	if i := slices.IndexFunc(er, func(r *ExitRoute) bool { return r.Aircraft.Matches(acType) }); i != -1 {
		return er[i]
	}
	return nil
}

// ExitRoutesForAircraft returns the route to each exit that the given aircraft
// type flies, leaving out the exits it has no route to.
func ExitRoutesForAircraft(routes map[ExitID]ExitRoutes, acType string) map[ExitID]*ExitRoute {
	m := make(map[ExitID]*ExitRoute, len(routes))
	for exit, er := range routes {
		if r := er.ForAircraft(acType); r != nil {
			m[exit] = r
		}
	}
	return m
}

// sidWaypoints returns the waypoints of the CIFP's SID off the runway to the
// exit for an exit route that gives none of its own; transition, if
// non-empty, names the SID's enroute transition to fly. A route with an
// initial heading needs no runway transition from the CIFP: the heading is
// how the aircraft gets from the runway to the SID.
func sidWaypoints(icao, sid, transition string, rwy RunwayID, exit ExitID, initialHeading bool,
	e *util.ErrorLogger) (WaypointArray, bool) {
	s, ok := DB.Airports[icao].SIDs[sid]
	if !ok {
		e.ErrorString(`must specify "waypoints": SID %q isn't in the FAA CIFP for %s. Options: %s`,
			sid, icao, strings.Join(util.SortedMapKeys(DB.Airports[icao].SIDs), ", "))
		return nil, false
	}
	runway := rwy.Base()
	if _, ok := s.RunwayTransitions[runway]; !ok && initialHeading {
		runway = ""
	}
	wps, err := s.Waypoints(runway, transition, exit.Base())
	if err != nil {
		e.ErrorString(`must specify "waypoints": SID %s: %v`, sid, err)
		return nil, false
	}
	return wps.Clone(), true
}

// amendSIDWaypoints applies the route's "initial_heading" and
// "waypoint_actions" to the SID's waypoints from the CIFP.
func (er *ExitRoute) amendSIDWaypoints(wps WaypointArray, e *util.ErrorLogger) WaypointArray {
	if h := er.InitialHeading; h < 0 || h > 360 {
		e.ErrorString(`"initial_heading" %d: must be between 1 and 360`, h)
	}
	for _, key := range util.SortedMapKeys(er.WaypointActions) {
		if err := wps.addActions(key, er.WaypointActions[key]); err != nil {
			e.ErrorString(`"waypoint_actions" %q: %v`, key, err)
		}
	}
	return wps
}

// How close to the departure end of the runway a route's first waypoint has
// to be to be taken for one, in nm along the runway and off its centerline.
const (
	departureEndAlong   = 0.25
	departureEndLateral = 0.1
)

// atDepartureEnd reports whether the waypoint sits at the departure end of
// the runway--named as the opposite runway's threshold, or placed there as a
// lat-long--rather than being a fix the aircraft flies to after takeoff.
func atDepartureEnd(wp Waypoint, r, rend Runway, nmPerLongitude float32) bool {
	end := math.LL2NM(rend.Threshold, nmPerLongitude)
	along := math.Normalize2f(math.Sub2f(end, math.LL2NM(r.Threshold, nmPerLongitude)))
	v := math.Sub2f(math.LL2NM(wp.Location, nmPerLongitude), end)
	return math.Abs(math.Dot(v, along)) <= departureEndAlong &&
		math.Abs(math.Dot(v, [2]float32{along[1], -along[0]})) <= departureEndLateral
}

// initialize puts the runway in front of the route's located waypoints--its
// threshold and then its midpoint, from which the aircraft tracks the runway
// centerline until it is 400' above the field and only then flies the
// route--and checks the route's other members against them.
func (er *ExitRoute) initialize(icao string, rwy RunwayID, r, rend Runway, nmPerLongitude float32,
	magneticVariation float32, controlPositions map[ControlPosition]*Controller, e *util.ErrorLogger) {
	course := math.TrueToMagnetic(math.Heading2LL(r.Threshold, rend.Threshold, nmPerLongitude), magneticVariation)

	// Waypoints at the departure end of the runway are the old way of saying
	// "fly the runway to the end and then turn on course"; the centerline
	// track below does that, so they go away and what they did is done once
	// the aircraft is 400' up.
	var departureEndGroups []WaypointActionGroup
	var departureEndAltitude *AltitudeRestriction
	var departureEndSpeed *SpeedRestriction
	for len(er.Waypoints) > 0 && atDepartureEnd(er.Waypoints[0], r, rend, nmPerLongitude) {
		wp := er.Waypoints[0]
		if er.InitialHeading == 0 {
			// Otherwise the tower's heading supersedes the charted legs.
			departureEndGroups = append(departureEndGroups, wp.ActionGroups()...)
		}
		if ar := wp.AltitudeRestriction(); ar != nil {
			departureEndAltitude = ar
		}
		if sr := wp.SpeedRestriction(); sr != nil {
			departureEndSpeed = sr
		}
		er.Waypoints = er.Waypoints[1:]
	}

	// A first fix close behind where the aircraft turns on course is almost always the runway's
	// own threshold named in place of its departure end....
	if len(er.Waypoints) > 0 && er.InitialHeading == 0 && departureEndGroups == nil &&
		math.HeadingDifference(course, r.Heading) <= 45 {
		first := er.Waypoints[0]
		along := math.Normalize2f(math.Sub2f(math.LL2NM(rend.Threshold, nmPerLongitude), math.LL2NM(r.Threshold, nmPerLongitude)))
		toFix := math.Sub2f(math.LL2NM(first.Location, nmPerLongitude), math.LL2NM(rend.Threshold, nmPerLongitude))
		if math.Length2f(toFix) <= 2 && math.Dot(toFix, along) < -0.1 {
			e.ErrorString("%s: first fix is behind the aircraft as it leaves runway %s; the departure end of the runway is %s-%s",
				first.Fix, rwy.Base(), icao, rend.Id)
		}
	}

	midWp := Waypoint{Fix: rwy.Base() + "-mid", Location: math.Lerp2f(0.5, r.Threshold, rend.Threshold)}
	// Every departure holds the runway centerline as a ground track until it
	// is 400' above the field; only then does it turn on course.
	track := int16(math.Round(float32(math.NormalizeHeading(course))))
	if track == 0 { // headings are given as 1-360; 0 means unset
		track = 360
	}
	groups := []WaypointActionGroup{
		{
			Actions: WaypointActions{Heading: WaypointHeadingAction{Heading: track, Track: true}},
			Until: WaypointActionTermination{
				Type:      WaypointActionAltitude,
				Altitude:  DB.Airports[icao].Elevation + 400,
				AtOrAbove: true,
			},
		},
	}
	if h := er.InitialHeading; h >= 1 && h <= 360 {
		// The tower's assigned heading: turn to it 400' above the field and
		// fly it until the departure controller sends the aircraft direct to
		// a fix on the SID.
		groups = append(groups, WaypointActionGroup{
			Actions: WaypointActions{Heading: WaypointHeadingAction{Heading: int16(h)}}})
	} else {
		groups = append(groups, departureEndGroups...)
	}
	midWp.InitExtra().ActionGroups = groups
	// The departure end waypoints' restrictions apply from here on out.
	if departureEndAltitude != nil {
		midWp.SetAltitudeRestriction(*departureEndAltitude)
	}
	if departureEndSpeed != nil {
		midWp.SetSpeedRestriction(*departureEndSpeed)
	}

	er.Waypoints = append([]Waypoint{
		{
			Fix:      rwy.Base(),
			Location: r.Threshold,
		},
		midWp}, er.Waypoints...)

	for i := range er.Waypoints {
		er.Waypoints[i].SetOnSID(true)

		if er.Waypoints[i].HasTransferCommsAction() {
			er.WaitToContactDeparture = true
		}
	}

	if er.Waypoints.HasHumanHandoff() {
		if er.HandoffController == "" {
			e.ErrorString(`no "handoff_controller" specified even though route has "/ho"`)
		} else if _, ok := controlPositions[er.HandoffController]; !ok {
			e.ErrorString("control position %q unknown in scenario", er.HandoffController)
		}
	} else if er.HandoffController != "" {
		e.ErrorString(`"handoff_controller" specified but won't be used since route has no "/ho"`)
	}

	if er.AssignedAltitude == 0 && er.ClearedAltitude == 0 {
		e.ErrorString(`must specify either "assigned_altitude" or "cleared_altitude"`)
	} else if er.AssignedAltitude != 0 && er.ClearedAltitude != 0 {
		e.ErrorString(`cannot specify both "assigned_altitude" and "cleared_altitude"`)
	}
}

// FinalHeading returns the heading the route leaves the aircraft on, or 0
// if it ends at a fix rather than on vectors.
func (er ExitRoute) FinalHeading() int {
	for _, v := range slices.Backward(er.Waypoints) {
		if groups := v.ActionGroups(); len(groups) > 0 {
			last := groups[len(groups)-1]
			if h := last.Actions.Heading; h.Heading != 0 && last.Until.Type == WaypointActionNoTermination {
				return int(h.Heading)
			}
		}
	}
	return 0
}

type Departure struct {
	Exit ExitID `json:"exit"`

	Destination    string                  `json:"destination"`
	Altitudes      util.SingleOrArray[int] `json:"altitude,omitempty"`
	Route          string                  `json:"route"`
	RouteWaypoints WaypointArray           // not specified in user JSON
	// Airlines is optional: without it the scenario can't generate its own
	// departures here, but published traffic still flies the exit and route.
	Airlines            []DepartureAirline `json:"airlines"`
	Scratchpad          string             `json:"scratchpad"`           // optional
	SecondaryScratchpad string             `json:"secondary_scratchpad"` // optional
	Description         string             `json:"description"`
}

type DepartureAirline struct {
	AirlineSpecifier
}

// TrafficRoutes says how published traffic between an airport and specific
// other airports is routed, keyed by the other airport's ICAO code.
type TrafficRoutes struct {
	Departures map[string]TrafficRouteSet `json:"departures"`
	Arrivals   map[string]TrafficRouteSet `json:"arrivals"`
}

// TrafficRoute is one route and the aircraft classes it applies to.
type TrafficRoute struct {
	Route    string        `json:"route"`
	Aircraft AircraftClass `json:"aircraft,omitempty"`
}

// TrafficRouteSet is the routes for one city pair; a bare JSON string is
// shorthand for a single route open to all aircraft.
type TrafficRouteSet []TrafficRoute

func (ts *TrafficRouteSet) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var route string
		if err := json.Unmarshal(b, &route); err != nil {
			return err
		}
		*ts = TrafficRouteSet{TrafficRoute{Route: route}}
		return nil
	}
	var routes []TrafficRoute
	if err := json.Unmarshal(b, &routes); err != nil {
		return err
	}
	*ts = routes
	return nil
}

// Routes returns the routes the given aircraft type may fly, in listed order.
func (ts TrafficRouteSet) Routes(acType string) []string {
	var routes []string
	for _, r := range ts {
		if r.Aircraft.Matches(acType) {
			routes = append(routes, r.Route)
		}
	}
	return routes
}

// routeReachesExit reports whether a departure route out of the airport flies
// over one of its exits or files a SID that leads to one.
func (ap *Airport) routeReachesExit(route, icao string) bool {
	fields := strings.Fields(route)
	if len(fields) > 0 && TokenNamesAirport(fields[0], icao) {
		fields = fields[1:]
	}
	for _, exitRoutes := range ap.DepartureRoutes {
		for exit, routes := range exitRoutes {
			if slices.Contains(fields, exit.Base()) {
				return true
			}
			for _, er := range routes {
				if er.SID != "" && slices.ContainsFunc(fields, func(f string) bool {
					return ProcedureBase(f) == ProcedureBase(er.SID)
				}) {
					return true
				}
			}
		}
	}
	return false
}

type ApproachType int

const (
	UnknownApproach ApproachType = iota
	ILSApproach
	RNAVApproach
	ChartedVisualApproach
	VisualApproach
	LocalizerApproach
	VORApproach
)

func (at ApproachType) String() string {
	return []string{"Unknown", "ILS", "RNAV", "Charted Visual", "Visual", "Localizer", "VOR"}[at]
}

func (at ApproachType) MarshalJSON() ([]byte, error) {
	switch at {
	case UnknownApproach:
		return []byte(`"Unknown"`), nil
	case ILSApproach:
		return []byte(`"ILS"`), nil
	case RNAVApproach:
		return []byte(`"RNAV"`), nil
	case ChartedVisualApproach:
		return []byte(`"ChartedVisual"`), nil
	case VisualApproach:
		return []byte(`"Visual"`), nil
	case LocalizerApproach:
		return []byte(`"Localizer"`), nil
	case VORApproach:
		return []byte(`"VOR"`), nil
	default:
		return nil, fmt.Errorf("unhandled approach type in MarshalJSON()")
	}
}

func (at *ApproachType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"Unknown"`:
		*at = UnknownApproach
		return nil

	case `"ILS"`:
		*at = ILSApproach
		return nil

	case `"RNAV"`:
		*at = RNAVApproach
		return nil

	case `"ChartedVisual"`:
		*at = ChartedVisualApproach
		return nil

	case `"Visual"`:
		*at = VisualApproach
		return nil

	case `"Localizer"`:
		*at = LocalizerApproach
		return nil

	case `"VOR"`:
		*at = VORApproach
		return nil

	default:
		return fmt.Errorf("%s: unknown approach_type", string(b))
	}
}

type Approach struct {
	Id        string          `json:"cifp_id"`
	FullName  string          `json:"full_name"`
	Type      ApproachType    `json:"type"`
	Runway    string          `json:"runway"`
	Waypoints []WaypointArray `json:"waypoints"`

	// Set in Airport PostDeserialize()
	Threshold         math.Point2LL
	OppositeThreshold math.Point2LL
}

// InitializeWaypoints resolves waypoint locations and adds the runway
// threshold waypoint to each route. It also sets the OnApproach flag,
// Threshold, and OppositeThreshold fields.
func (ap *Approach) InitializeWaypoints(icao string, loc Locator, nmPerLongitude float32,
	magneticVariation float32, e *util.ErrorLogger) {
	rwy, ok := LookupRunway(icao, ap.Runway)
	if !ok {
		e.ErrorString(`"runway" %q is unknown. Options: %s`, ap.Runway,
			DB.Airports[icao].ValidRunways())
	}
	ap.Threshold = rwy.Threshold

	if opp, ok := LookupOppositeRunway(icao, ap.Runway); ok {
		ap.OppositeThreshold = opp.Threshold
	} else {
		e.ErrorString("no opposite runway found for %q\n", ap.Runway)
	}

	for i := range ap.Waypoints {
		ap.Waypoints[i] =
			ap.Waypoints[i].InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

		// Add the final fix at the runway threshold.
		alt := rwy.Elevation + rwy.ThresholdCrossingHeight
		threshold := math.Offset2LL(rwy.Threshold, math.MagneticToTrue(rwy.Heading, magneticVariation),
			rwy.DisplacedThresholdDistance, nmPerLongitude)

		thresholdWP := Waypoint{
			Fix:      "_" + ap.Runway + "_THRESHOLD",
			Location: threshold,
			Flags:    WaypointFlagLand | WaypointFlagFlyOver,
		}
		thresholdWP.SetAltitudeRestriction(MakeAtAltitudeRestriction(float32(alt)))
		ap.Waypoints[i] = append(ap.Waypoints[i], thresholdWP)

		for j := range ap.Waypoints[i] {
			ap.Waypoints[i][j].SetOnApproach(true)
		}
	}
}

// Find the FAF: return the corresponding waypoint array and the index of the FAF within it.
func (ap *Approach) FAFSegment(nmPerLongitude, magneticVariation float32) ([]Waypoint, int) {
	// For approaches with multiple segments, want the segment that is most
	// closely aligned with the runway.
	rwyHdg := ap.RunwayHeading(nmPerLongitude)

	bestWpsIdx, bestWpsFAFIdx := -1, -1
	minDiff := float32(360)

	for i, wps := range ap.Waypoints {
		fafIdx := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF() })
		if fafIdx == -1 {
			// no FAF on this segment(?)
			continue
		}

		if wps[fafIdx].IF() || wps[fafIdx].IAF() {
			// Likely a HILPT; don't go outbound for the approach course as
			// it may be some random feeder fix.
			fafIdx++
		}

		// Go from the previous fix to the FAF if possible.
		if fafIdx == 0 {
			fafIdx++
		}

		hdg := math.Heading2LL(wps[fafIdx-1].Location, wps[fafIdx].Location, nmPerLongitude)

		diff := math.HeadingDifference(hdg, rwyHdg)
		if diff < minDiff {
			minDiff = diff
			bestWpsIdx = i
			bestWpsFAFIdx = fafIdx
		}
	}

	if bestWpsIdx != -1 {
		return ap.Waypoints[bestWpsIdx], bestWpsFAFIdx
	} else {
		// Shouldn't ever happen since we ensure there is a FAF for each approach.
		return nil, 0
	}
}

func (ap *Approach) ExtendedCenterline(nmPerLongitude, magneticVariation float32) [2]math.Point2LL {
	return [2]math.Point2LL{ap.Threshold, ap.OppositeThreshold}
}

func (ap *Approach) RunwayHeading(nmPerLongitude float32) math.TrueHeading {
	return math.Heading2LL(ap.Threshold, ap.OppositeThreshold, nmPerLongitude)
}

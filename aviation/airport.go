// aviation/airport.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"cmp"
	"fmt"
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
	PrintDepartureStrips  *bool                  `json:"print_departure_strips"` // nil -> unspecified -> true
	PrintArrivalStrips    *bool                  `json:"print_arrival_strips"`
}

// ICAOAirportCode identifies an airport by the id the aviation database keys
// it by: its ICAO id where one exists (KJFK, PHOG, EGLL) and otherwise its FAA
// local identifier (00N, TX15). Scenarios and the aviation data name airports
// this way.
type ICAOAirportCode string

// FAAAirportCode is an airport's FAA local identifier (JFK, OGG, 00N): the
// domestic name displayed on STARS and ERAM scopes and entered by the humans
// running sims. Airports outside the FAA's regions have no FAAAirportCode.
type FAAAirportCode string

type VFRRandomsSpec struct {
	Rate  float32 `json:"rate"`
	Fleet string  `json:"fleet"`
}

type VFRRouteSpec struct {
	Name        string          `json:"name"`
	Rate        float32         `json:"rate"`
	Fleet       string          `json:"fleet"`
	Waypoints   WaypointArray   `json:"waypoints"`
	Destination ICAOAirportCode `json:"destination"`
	Description string          `json:"description"`
}

// Finalize checks the airport's definition and resolves its routes.
// historicalSTARs are the STARs a historical scenario's arrivals fly into the
// airport; its "traffic_routes" may name them though the CIFP doesn't chart
// them.
func (ap *Airport) Finalize(icao ICAOAirportCode, db Database, nmPerLongitude float32,
	magneticVariation float32, controlPositions map[ControlPosition]*Controller, scratchpads map[string]string,
	facilityAirports map[ICAOAirportCode]*Airport, checkScratchpad func(string) bool, historicalSTARs []string,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if p, ok := db.AirportLocation(icao); !ok {
		e.ErrorString("airport %q not found in airport database", icao)
	} else {
		ap.Location = p

		if len(db.AirportRunways(icao)) == 0 {
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
			if dbAppr, ok := db.AirportApproaches(icao)[appr.Id]; !ok {
				e.ErrorString("Approach %q not in database. Options: %s", appr.Id,
					strings.Join(util.SortedMapKeys(db.AirportApproaches(icao)), ", "))
				e.Pop()
				continue
			} else {
				// if dbAppr.CirclingOnly {
				// 	e.ErrorString("Approach %q has only circle-to-land minimums", appr.Id)
				// }

				// Copy the approach from the database, doing checks to
				// make sure bogus overrides haven't been specified.
				if appr.Type != UnknownApproach {
					e.ErrorString(`"type" cannot be given with "cifp_id" approaches`)
				}
				appr.Type = dbAppr.Type
				appr.LocalizerLocation = dbAppr.LocalizerLocation

				if len(appr.Waypoints) > 0 {
					e.ErrorString(`"waypoints" cannot be given with "cifp_id" approaches`)
				}
				if appr.FullName == "" {
					appr.FullName = dbAppr.FullName
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
		appr.InitializeWaypoints(icao, db, nmPerLongitude, magneticVariation, e)

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
		appr.checkCourseAlignment(nmPerLongitude, magneticVariation, e)

		appr.finalizeReportingPoints(db, ap.Location, e)

		if appr.FullName == "" {
			if appr.Type == ChartedVisualApproach {
				e.ErrorString(`Must provide "full_name" for charted visual approach`)
			} else {
				appr.FullName = appr.DefaultFullName()
			}
		} else if !strings.Contains(appr.FullName, "runway") && !strings.Contains(appr.FullName, "Runway") {
			e.ErrorString(`Must have "runway" in approach's "full_name"`)
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

		r, ok := LookupRunway(db, icao, rwy.Base())
		if !ok {
			e.ErrorString("unknown runway for airport. Options: %s", db.ValidRunways(icao))
		}
		rend, ok := LookupOppositeRunway(db, icao, rwy.Base())
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
					if route.ClimboutActions != "" {
						e.ErrorString(`"climbout_actions" applies only to a route taken from the CIFP; put the actions in "waypoints"`)
					}
					if len(route.WaypointActions) > 0 {
						e.ErrorString(`"waypoint_actions" applies only to a route taken from the CIFP; put the actions in "waypoints"`)
					}
					route.Waypoints = route.Waypoints.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)
					route.Waypoints.CheckDeparture(e, db.AirportElevation(icao), controlPositions, checkScratchpad)
					route.checkChartedSIDRoute(db, icao, rwy, exits, r, rend, db, nmPerLongitude, magneticVariation, e)
					route.initialize(db, icao, rwy, r, rend, nmPerLongitude, magneticVariation, controlPositions, Waypoint{}, e)
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

					var override Waypoint
					if route.ClimboutActions != "" {
						if route.InitialHeading != 0 {
							e.ErrorString(`cannot give both "initial_heading" and "climbout_actions"; put the heading in "climbout_actions"`)
						}
						if ovr, err := route.parseClimboutActions(); err != nil {
							e.ErrorString(`"climbout_actions": %v`, err)
						} else {
							override = ovr
							override.initializeActionLocations(db, ap.Location, magneticVariation, false, e)
							WaypointArray{override}.checkBasics(e, controlPositions, checkScratchpad)
						}
					}

					for _, exit := range exits {
						if len(exits) > 1 {
							e.Push("Exit " + string(exit))
						}
						exitRoute := *route
						if wps, err := sidWaypoints(db, icao, route.SID, transition, rwy, exit,
							route.InitialHeading != 0 || override.AssignsHeading()); err != nil {
							e.ErrorString(`must specify "waypoints": %v`, err)
						} else {
							wps = route.amendSIDWaypoints(wps, e)
							exitRoute.Waypoints = wps.InitializeLocations(db, nmPerLongitude, magneticVariation, true, e)
							exitRoute.Waypoints.checkBasics(e, controlPositions, checkScratchpad)
							exitRoute.initialize(db, icao, rwy, r, rend, nmPerLongitude, magneticVariation, controlPositions, override, e)
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

	ap.checkExits(db, e)

	e.Push(`"traffic_routes"`)
	checkTrafficRouteAirports := func(routes map[ICAOAirportCode]TrafficRouteSet) map[ICAOAirportCode]TrafficRouteSet {
		if len(routes) == 0 {
			return routes
		}
		checked := make(map[ICAOAirportCode]TrafficRouteSet, len(routes))
		for _, other := range util.SortedMapKeys(routes) {
			norm := ICAOAirportCode(strings.ToUpper(strings.TrimSpace(string(other))))
			if norm == icao {
				e.ErrorString("%s: routes to or from the airport itself", other)
			} else if err := db.CheckAirport("traffic route", norm); err != nil {
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
		wps := RouteWaypoints(db, r.Route, e).InitializeLocations(db, nmPerLongitude, magneticVariation,
			true /* allowSlop */, e)
		if !slices.ContainsFunc(wps, func(wp Waypoint) bool { return !wp.Location.IsZero() }) {
			e.ErrorString("%s: no locatable fixes in route", r.Route)
			return false
		}
		return true
	}
	for _, other := range util.SortedMapKeys(ap.TrafficRoutes.Departures) {
		e.Push("Departure " + string(other))
		for _, r := range ap.TrafficRoutes.Departures[other] {
			if checkTrafficRoute(r) && !ap.routeReachesExit(db, r.Route, icao) {
				e.ErrorString(`%s: route reaches no exit in "departure_routes"`, r.Route)
			}
		}
		e.Pop()
	}
	for _, other := range util.SortedMapKeys(ap.TrafficRoutes.Arrivals) {
		e.Push("Arrival " + string(other))
		for _, r := range ap.TrafficRoutes.Arrivals[other] {
			if !checkTrafficRoute(r) {
				continue
			}
			// A final token that looks like a procedure name must be one of
			// the airport's charted STARs or, in a historical scenario, one
			// its arrivals fly; anything else is likely a typo.
			if token, _ := RouteFiledSTAR(db, r.Route, icao); token != "" {
				star, _ := RouteSTAR(db, r.Route, icao)
				flown := slices.ContainsFunc(historicalSTARs,
					func(s string) bool { return ProcedureBase(s) == ProcedureBase(token) })
				if star == "" && !flown {
					e.ErrorString("%s: %q matches no STAR at %s", r.Route, token, icao)
				}
			}
		}
		e.Pop()
	}
	e.Pop()

	for i, dep := range ap.Departures {
		e.Push("Departure exit " + string(dep.Exit))
		e.Push("Destination " + string(dep.Destination))

		for _, alt := range dep.Altitudes {
			if alt < 500 {
				e.ErrorString("altitude of %v is too low to be used. Is it supposed to be %v?", alt, alt*100)
			}
		}

		if err := db.CheckAirport("destination", dep.Destination); err != nil {
			e.Error(err)
		}

		// Use Base() to get the canonical exit name (e.g., "COLIN" from "COLIN.P")
		depExit := dep.Exit.Base()

		if !checkScratchpad(dep.Scratchpad) {
			e.ErrorString("%s: invalid scratchpad", dep.Scratchpad)
		}
		if !checkScratchpad(dep.SecondaryScratchpad) {
			e.ErrorString("%s: invalid secondary scratchpad", dep.SecondaryScratchpad)
		}

		wp, err := parseWaypoints(dep.Route)
		if err != nil {
			e.Error(err)
		}

		_, intraFacility := facilityAirports[dep.Destination]
		allowSlop := !intraFacility // Make sure that the full route is valid for intra-facility.
		wp = wp.InitializeLocations(db, nmPerLongitude, magneticVariation, allowSlop, e)
		ap.Departures[i].RouteWaypoints = wp

		if !slices.ContainsFunc(ap.Departures[i].RouteWaypoints,
			func(wp Waypoint) bool { return wp.Fix == depExit }) {
			e.ErrorString("exit %q not found in departure route", depExit)
		}

		ap.checkDepartureRouteAlongSID(db, icao, &ap.Departures[i], e)

		// The slop above lets a route name places the database doesn't have,
		// which an enroute route legitimately does. The fixes up to the exit
		// are inside the facility, so those it has to know.
		for _, w := range wp {
			if w.Fix == depExit {
				break
			}
			if w.Airway() == "" && w.Location.IsZero() {
				e.ErrorString("%s: unable to locate waypoint before the exit", w.Fix)
			}
		}

		for _, al := range dep.Airlines {
			al.Check(db, e)
		}

		e.Pop()
		e.Pop()
	}

	checkFleet := func(fleet, where string) {
		if fleet == "" {
			return
		}
		if !db.IsGAFleet(fleet) {
			e.ErrorString("Fleet %q in %q is not a valid GA aircraft fleet. Options: %s",
				fleet, where, strings.Join(db.GAFleetNames(), ", "))
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
			ap.VFR.Routes[i].Waypoints.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)

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
				if spec.Waypoints[j].HasLandAction() {
					spec.Waypoints[j].ClearLandAction()
					spec.Waypoints[j].SetSequenceVFRLanding(true)
				}
			}
			// Ensure the last waypoint always has it, even if /land
			// wasn't specified in the route.
			spec.Waypoints[len(spec.Waypoints)-1].SetSequenceVFRLanding(true)
		}
		if err := db.CheckAirport("destination", spec.Destination); err != nil {
			e.Error(err)
		}
		e.Pop()
	}
	e.Pop()

	// Check if airport has VFR departures but is in class B or C airspace
	if ap.VFR.Randoms.Rate > 0 || len(ap.VFR.Routes) > 0 {
		if db.InClassBOrC(ap.Location, db.AirportElevation(icao)) {
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
			if _, ok := LookupRunway(db, icao, rwy); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, db.ValidRunways(icao))
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

		def.ReferencePoint.Resolve(db, "reference_point", e)

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
			routePoints := parseCRDARoute(def.ReferenceRoute, db, nmPerLongitude, magneticVariation, e)
			def.Path = PathFromRoutePoints(routePoints, nmPerLongitude)
			def.RegionLength = def.Path.Length - def.NearDistance
		} else {
			def.Path = PathFromReferenceLine(def.ReferencePoint.Point2LL, def.ReferenceLineHeading,
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
	runways := db.AirportRunways(icao)
	headings := make(map[string]math.MagneticHeading)
	for _, rwy := range runways {
		if _, ok := ap.ATPAVolumes[rwy.Id]; !ok {
			ap.ATPAVolumes[rwy.Id] = &ATPAVolume{}
		}
		headings[rwy.Id] = ap.atpaHeading(db, icao, rwy, nmPerLongitude, magneticVariation)
	}

	for rwy, vol := range ap.ATPAVolumes {
		e.Push("ATPA " + rwy)

		if vol.Id == "" {
			vol.Id = rwy
		}

		if r, ok := LookupRunway(db, icao, rwy); !ok {
			e.ErrorString("runway %q is unknown. Options: %s", rwy, db.ValidRunways(icao))
		} else {
			vol.Threshold.Resolve(db, "runway_threshold", e)
			handWritten := !vol.Threshold.IsZero() || vol.Heading != 0
			if vol.Threshold.IsZero() {
				vol.Threshold.Point2LL = r.Threshold
			}
			if vol.Heading == 0 {
				vol.Heading = headings[r.Id]
			}
			if handWritten {
				checkATPACourse(vol, r, runways, headings, e)
			}
		}

		// Defaults if things are not specified
		if vol.MaxHeadingDeviation == 0 {
			vol.MaxHeadingDeviation = 90
		}
		if vol.Floor == 0 {
			vol.Floor = float32(db.AirportElevation(icao) + 100)
		}
		if vol.Ceiling == 0 {
			vol.Ceiling = float32(db.AirportElevation(icao) + 5000)
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

		if vol.MaxHeadingDeviation < 0 || vol.MaxHeadingDeviation > 180 {
			e.ErrorString(`"max_heading_deviation" %.0f must be between 0 and 180`, vol.MaxHeadingDeviation)
		}
		if elevation := db.AirportElevation(icao); vol.Ceiling < float32(elevation+1000) {
			e.ErrorString(`"ceiling" %.0f is less than 1000 feet above the airport's elevation of %d`,
				vol.Ceiling, elevation)
		}
		if vol.Floor >= vol.Ceiling {
			e.ErrorString(`"floor" %.0f must be below "ceiling" %.0f`, vol.Floor, vol.Ceiling)
		}

		e.Pop()
	}
}

// checkATPACourse reports a hand-written ATPA volume whose threshold or heading
// can't be what the scenario meant, as when it was copied from another runway
// or airport. A volume may follow another runway's final for a circling
// approach (KEWR 29 follows 22L, KJFK 13L the 044 course of its RNAV), so its
// heading is checked against the courses of all of the airport's runways.
// headings gives each runway's course, as its default volume follows it.
func checkATPACourse(vol *ATPAVolume, rwy Runway, runways []Runway, headings map[string]math.MagneticHeading,
	e *util.ErrorLogger) {
	// Thresholds this close are the same point; some runways share a corner.
	const sameThreshold = 0.1 // nm
	atThreshold := func(r Runway) bool {
		return math.NMDistance2LL(vol.Threshold.Point2LL, r.Threshold) < sameThreshold
	}

	if d := math.NMDistance2LL(vol.Threshold.Point2LL, rwy.Threshold); d > 5 {
		e.ErrorString(`"runway_threshold" is %.1f nm from runway %s's threshold`, d, rwy.Id)
	}

	follows := func(r Runway) bool { return math.HeadingDifference(vol.Heading, headings[r.Id]) <= 10 }
	if !slices.ContainsFunc(runways, follows) {
		e.ErrorString(`"heading" %.0f follows none of the airport's runways`, vol.Heading)
	}
	if d := math.HeadingDifference(vol.Heading, headings[rwy.Id]); d > 120 {
		e.ErrorString(`"heading" %.0f is %.0f degrees from runway %s's course %.0f, so the volume `+
			`covers arrivals to the opposite end`, vol.Heading, d, rwy.Id, headings[rwy.Id])
	}

	// Starting at another runway's threshold is only sensible when following
	// that runway's final.
	if atThreshold(rwy) {
		return
	}
	for _, r := range runways {
		if atThreshold(r) && !follows(r) {
			e.ErrorString(`"runway_threshold" is runway %s's threshold, but "heading" %.0f isn't its course %.0f`,
				r.Id, vol.Heading, headings[r.Id])
		}
	}
}

// atpaHeading returns the heading of a runway's ATPA volume when the scenario
// doesn't give one. Where the runway has a localizer approach, the volume
// follows its final approach course: a localizer may be offset from its runway
// (KSLC 35's LDA is 5 degrees off). RNAV finals aren't used since they may
// curve after the FAF, and finals more than 30 degrees off the runway are
// circling approaches. Otherwise the volume follows the runway's surveyed
// alignment rather than its published magnetic bearing, which may be referenced
// to an outdated magnetic variation.
func (ap *Airport) atpaHeading(db Database, icao ICAOAirportCode, rwy Runway, nmPerLongitude,
	magneticVariation float32) math.MagneticHeading {
	rwyHeading := math.MagneticToTrue(rwy.Heading, magneticVariation)
	if opp, ok := LookupOppositeRunway(db, icao, rwy.Id); ok {
		rwyHeading = math.Heading2LL(rwy.Threshold, opp.Threshold, nmPerLongitude)
	}
	offset := func(course math.TrueHeading) float32 { return math.HeadingDifference(course, rwyHeading) }

	var finals []math.TrueHeading
	for _, appr := range ap.ApproachesToRunway(rwy.Id) {
		if appr.Type.HasLocalizer() {
			finals = append(finals, appr.FinalApproachCourse(nmPerLongitude, magneticVariation))
		}
	}
	finals = slices.DeleteFunc(finals, func(course math.TrueHeading) bool { return offset(course) > 30 })
	if len(finals) == 0 {
		return math.TrueToMagnetic(rwyHeading, magneticVariation)
	}
	final := slices.MinFunc(finals, func(a, b math.TrueHeading) int { return cmp.Compare(offset(a), offset(b)) })
	return math.TrueToMagnetic(final, magneticVariation)
}

func (ap Airport) HasIFROperations() bool {
	return len(ap.Approaches) > 0 || len(ap.DepartureRoutes) > 0
}

// ApproachesToRunway returns the approaches that land on rwy, in order of
// their identifiers.
func (ap *Airport) ApproachesToRunway(rwy string) []*Approach {
	var approaches []*Approach
	for appr := range util.SortedMapValues(ap.Approaches) {
		if appr.Runway == rwy {
			approaches = append(approaches, appr)
		}
	}
	return approaches
}

func (ap Airport) VFRRateSum() float32 {
	sum := ap.VFR.Randoms.Rate
	for _, spec := range ap.VFR.Routes {
		sum += spec.Rate
	}
	return sum
}

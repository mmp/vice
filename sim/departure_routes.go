// sim/departure_routes.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
)

func (ss *CommonState) departureConfiguration(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	category string) (*av.Airport, *DepartureRunway, map[av.ExitID]av.ExitRoutes, error) {
	ap := ss.Airports[departureAirport]
	if ap == nil {
		return nil, nil, nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(ss.DepartureRunways,
		func(r DepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, nil, av.ErrUnknownRunway
	}
	rwy := &ss.DepartureRunways[idx]
	return ap, rwy, ap.DepartureRoutes[rwy.Runway], nil
}

// errNoScenarioRoute means a runway can't plausibly work a published flight:
// no gate it launches goes anywhere near where the flight really went. Another
// runway the scenario is launching from may still fly it; a flight none of them
// can is dropped before it spawns rather than forced through an unrelated gate.
var errNoScenarioRoute = errors.New("no plausible route in this scenario")

// publishedDepartureMaxHeadingDifference bounds how far off in direction a
// substituted departure route may be. A scenario that models one gate of a
// busy airport should be handed the flights that plausibly leave through it,
// not every departure from the airport.
const publishedDepartureMaxHeadingDifference = 45 // degrees

// publishedSubstituteMaxExitHeadingDifference bounds the gate a borrowed route
// may go out. It is far wider than the limit on the substitute airport itself,
// since a gate sits twenty or thirty miles out and the turn onto course comes
// later: JFK's Florida traffic leaves over WAVEY, 60 degrees off the direct
// line. What it rules out is setting off in the other direction entirely.
const publishedSubstituteMaxExitHeadingDifference = 90 // degrees

// candidateDeparture is a scenario departure a published flight could fly,
// together with the runway category configuration it came from.
type candidateDeparture struct {
	ap         *av.Airport
	rwy        *DepartureRunway
	exitRoutes map[av.ExitID]*av.ExitRoute
	dep        *av.Departure
}

// compatibleDepartures collects the exits the given runway categories can
// launch the aircraft type out of, one candidate per exit: published traffic
// brings its own destination, and the routes say which exit it really leaves
// through. The scenario's "departures" have no say here; they belong to its
// own generator.
func (ss *CommonState) compatibleDepartures(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, aircraftType string) []candidateDeparture {
	var candidates []candidateDeparture
	for _, category := range categories {
		ap, rwy, allRoutes, err := ss.departureConfiguration(departureAirport, runway, category)
		if err != nil {
			continue
		}
		exitRoutes := av.ExitRoutesForAircraft(db.Lookups{}, allRoutes, aircraftType)

		inCategory := func(exit av.ExitID) bool {
			return rwy.Category == "" || rwy.Category == ap.ExitCategory(exit)
		}

		exits := util.FilterSlice(util.SortedMapKeys(exitRoutes), inCategory)
		// One backing array for the whole category, so the pointers stay
		// valid as the slice below grows.
		synthesized := make([]av.Departure, len(exits))
		for i, exit := range exits {
			synthesized[i] = av.Departure{Exit: exit}
			candidates = append(candidates, candidateDeparture{ap, rwy, exitRoutes, &synthesized[i]})
		}
	}
	return candidates
}

// departureFit ranks how well a runway's gates suit a published flight, best
// first: a runway that flies the flight's own route is a better place to launch
// it from than one that only has a gate pointing the same general way.
type departureFit int

const (
	fitScenarioRoute departureFit = iota // the scenario's route for the city pair
	fitRealRoute                         // a scraped filing or FAA route for the pair
	fitNeighborRoute                     // the route to the nearest routed destination
	fitNearestGate                       // nothing routed; the gate heading the right way
)

// departureChoice is the exit a published flight leaves through, the real-world
// route that found it if one did, how well the runway's gates fit the flight,
// and how the choice was made, for reporting. Finding it costs only database
// lookups, so a runway can ask whether it works a flight without paying to turn
// the route into waypoints. A choice that comes back with an error carries only
// the route the flight would have filed, so that the failure can be read.
type departureChoice struct {
	candidate candidateDeparture
	route     string
	cruise    CruiseLimits
	fit       departureFit
	how       string
}

// departurePlacement is the departure a published flight flies and how the
// choice was made, for reporting. Its departure is a resolved copy of the
// candidate's: one authored by the scenario carries its own route, while one
// synthesized for an airport that names no departures gets the route here.
type departurePlacement struct {
	ap         *av.Airport
	rwy        *DepartureRunway
	exitRoutes map[av.ExitID]*av.ExitRoute
	dep        av.Departure
	cruise     CruiseLimits
	how        string
}

// placement resolves the choice into the departure the flight flies. A choice
// with no route falls back to flying to its exit fix, since without that its
// route ends with the scenario's vector off the runway and it would head
// straight for its destination from wherever that leaves it.
func (ss *CommonState) placement(choice departureChoice, departureAirport, destination av.ICAOAirportCode) departurePlacement {
	c := choice.candidate
	p := departurePlacement{ap: c.ap, rwy: c.rwy, exitRoutes: c.exitRoutes, dep: *c.dep,
		cruise: choice.cruise, how: choice.how}
	if choice.route != "" {
		p.cruise.Floor = av.RouteAltitudeFloor(db.Lookups{}, choice.route, departureAirport, destination)
	}

	exitRoute := c.exitRoutes[c.dep.Exit]
	if choice.route != "" {
		p.dep.Route = departureRoute(choice.route, departureAirport, c.dep.Exit, exitRoute)
		p.dep.RouteWaypoints = dropFlownPrefix(ss.departureRouteWaypoints(p.dep.Route),
			exitRoute.Waypoints)
	} else {
		p.dep.Route = string(c.dep.Exit.Base())
		p.dep.RouteWaypoints = ss.departureRouteWaypoints(p.dep.Route)
	}
	return p
}

// departureRoute is the part of a filed route a departure flies and files:
// everything past the tokens naming the origin airport, in full--the fixes
// between the airport and the exit are flown, not trimmed away. A leading SID
// token drops out whichever SID it names, since it's the scenario's exit
// route that flies the fixes off the runway and its SID that goes on the
// flight plan; the exit fix is put in front only when neither the route nor
// the exit route reaches it, so that "direct on course" still goes out over
// the gate--JFK to Cleveland files "KJFK DEEZZ6 CANDR J60...", and with the
// DEEZZ6 exit route authored as plain vectors, DEEZZ has to lead the route
// itself.
func departureRoute(route string, departureAirport av.ICAOAirportCode, exit av.ExitID, exitRoute *av.ExitRoute) string {
	fields := av.TrimDepartureAirportTokens(db.Lookups{}, strings.Fields(route), departureAirport)
	if len(fields) > 0 && av.TokenNamesProcedure(db.Lookups{}, fields[0]) {
		fields = fields[1:]
	}

	fix := exit.Base()
	onExitRoute := slices.ContainsFunc(exitRoute.Waypoints,
		func(wp av.Waypoint) bool { return wp.Fix == fix })
	if !slices.Contains(fields, fix) && !onExitRoute {
		fields = append([]string{fix}, fields...)
	}
	return strings.Join(fields, " ")
}

// dropFlownPrefix removes the leading route waypoints the exit route already
// flies: the route resumes after the last fix they share, so an exit route
// that ends at the exit fix doesn't send the aircraft back to a fix behind it.
// The fix the exit route ends at is the exception: the route keeps its own
// copy, which av.SpliceRoutes merges into the exit route's, so that the airway
// the flight leaves the fix on comes along.
func dropFlownPrefix(routeWps, exitWps av.WaypointArray) av.WaypointArray {
	for i, exitWp := range slices.Backward(exitWps) {
		for j, routeWp := range slices.Backward(routeWps) {
			if routeWp.Fix == exitWp.Fix {
				return routeWps[j+util.Select(i == len(exitWps)-1, 0, 1):]
			}
		}
	}
	return routeWps
}

// departureRouteWaypoints locates the fixes of a departure's enroute route,
// stopping at the point where the sim lets the aircraft go: the fixes past
// there are never flown and every one of them is sent to the clients on every
// update. Fixes it can't place--SID and STAR names, radial/DME fixes--drop out.
func (ss *CommonState) departureRouteWaypoints(route string) av.WaypointArray {
	wps := av.RouteWaypoints(db.Lookups{}, route, nil).InitializeLocations(ss, ss.NmPerLongitude,
		ss.MagneticVariation, true /* allowSlop */, nil)

	cull := ss.cullDistance()
	if i := slices.IndexFunc(wps, func(wp av.Waypoint) bool {
		return math.NMDistance2LL(wp.Location, ss.Center) > cull
	}); i != -1 {
		wps = wps[:i+1] // keep the first one past it so the aircraft flies out on course
	}
	return wps
}

// resolvePublishedDeparture finds the departure a published flight flies off a
// runway, ready to be handed to the aircraft.
func (ss *CommonState) resolvePublishedDeparture(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, destination av.ICAOAirportCode, aircraftType string,
	routedDestinations map[av.ICAOAirportCode][]av.ICAOAirportCode) (departurePlacement, error) {
	departureAirport = traffic.NormalizeAirportCode(departureAirport)
	choice, err := ss.findPublishedDeparture(departureAirport, runway, categories, destination,
		aircraftType, routedDestinations)
	if err != nil {
		return departurePlacement{}, err
	}
	return ss.placement(choice, departureAirport, destination), nil
}

// findPublishedDeparture finds the scenario exit and route a published
// flight flies. A route the scenario gives for the city pair wins outright;
// otherwise, if the route database knows how the pair is really flown and one
// of its routes leaves through a modeled exit, the flight follows that exit
// and files the real route. Failing both, the flight flies the way its
// nearest routed neighbor is left for--minus that route's tail, which belongs
// to the neighbor--since Vero Beach has no route from JFK but Orlando 66nm
// away leaves over WAVEY. Last of all it goes out the exit lying closest to
// the direction it is going. If nothing is in the right direction at all the
// runway doesn't work this flight and errNoScenarioRoute says not to launch it
// from here.
func (ss *CommonState) findPublishedDeparture(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, destination av.ICAOAirportCode, aircraftType string,
	routedDestinations map[av.ICAOAirportCode][]av.ICAOAirportCode) (departureChoice, error) {
	departureAirport = traffic.NormalizeAirportCode(departureAirport)
	destination = traffic.NormalizeAirportCode(destination)

	candidates := ss.compatibleDepartures(departureAirport, runway, categories, aircraftType)
	if len(candidates) == 0 {
		return departureChoice{}, fmt.Errorf("no compatible departure route for runway %s and a %s",
			runway, aircraftType)
	}

	scenarioRoutes := func(to av.ICAOAirportCode) []string {
		if ap, ok := ss.Airports[departureAirport]; ok {
			return ap.TrafficRoutes.Departures[to].Routes(db.Lookups{}, aircraftType)
		}
		return nil
	}
	hour, hourKnown := ss.localHour(departureAirport)

	scenario := scenarioRoutes(destination)
	real := realDepartureRoutes(departureAirport, destination, aircraftType, hour, hourKnown)
	// The route the flight would file, kept aside so that a flight no runway
	// can work still reports the route that couldn't be fitted: that is what
	// says why it couldn't be.
	filed := ""
	if len(scenario) > 0 {
		filed = scenario[0]
	} else if len(real) > 0 {
		filed = real[0].route
	}

	for _, route := range scenario {
		if c, ok := departureExit(route, departureAirport, destination, "", candidates); ok {
			return departureChoice{candidate: c, route: route, fit: fitScenarioRoute,
				how: "scenario route via " + c.dep.Exit.Base()}, nil
		}
	}
	for _, r := range real {
		if c, ok := departureExit(r.route, departureAirport, destination, r.departureFix, candidates); ok {
			// The scraped filings say what altitudes the route is really
			// flown at; file within them when the aircraft can.
			return departureChoice{candidate: c, route: r.route,
				cruise: CruiseLimits{Low: r.minAltitude, High: r.maxAltitude},
				fit:    fitRealRoute,
				how:    r.how + " via " + c.dep.Exit.Base()}, nil
		}
	}

	// Either the pair has no route or every route it has leaves through an
	// exit this configuration doesn't model: a scenario that works one corner
	// of an airport has no reason to model the gate a filed route uses. Fly
	// the way the nearest destination that does have a workable route is left
	// for, keeping that route only as far as it is this flight's own: the
	// trailing airport and STAR belong to the neighbor. Heading and distance
	// gate plausibility--a route the scenario models in some other direction
	// entirely is no way to leave, however close its destination.
	pool := slices.Clone(routedDestinations[departureAirport])
	if ap, ok := ss.Airports[departureAirport]; ok {
		for _, to := range util.SortedMapKeys(ap.TrafficRoutes.Departures) {
			if len(scenarioRoutes(to)) > 0 {
				pool = append(pool, to)
			}
		}
	}

	origin, originOK := db.DB.Airports[departureAirport]
	trueAirport, trueOK := db.DB.Airports[destination]
	if !originOK || !trueOK {
		return departureChoice{route: filed}, fmt.Errorf(
			"no route to %s and airport coordinates are unavailable", destination)
	}
	trueHeading := math.GreatCircleHeading(origin.Location, trueAirport.Location)

	// A borrowed route is only as good as the gate it goes out: the neighbor
	// lies the right way, but nothing so far says its route leaves that way.
	// Birmingham stands in for Atlanta from Minneapolis, yet one of its routes
	// sets off up the northeast gate.
	towardDestination := func(c candidateDeparture) bool {
		difference, ok := exitHeadingDifference(c, origin.Location, trueHeading, ss.NmPerLongitude)
		return !ok || difference <= publishedSubstituteMaxExitHeadingDifference
	}
	for _, substitute := range substituteAirports(departureAirport, destination, pool,
		publishedDepartureMaxHeadingDifference) {
		for _, route := range scenarioRoutes(substitute) {
			if c, ok := departureExit(route, departureAirport, substitute, "", candidates); ok &&
				towardDestination(c) {
				return departureChoice{candidate: c, route: stripSubstituteTail(route, substitute),
					fit: fitNeighborRoute, how: "nearest route, to " + string(substitute)}, nil
			}
		}
		for _, r := range realDepartureRoutes(departureAirport, substitute, aircraftType, hour, hourKnown) {
			if c, ok := departureExit(r.route, departureAirport, substitute, r.departureFix, candidates); ok &&
				towardDestination(c) {
				return departureChoice{candidate: c, route: stripSubstituteTail(r.route, substitute),
					fit: fitNeighborRoute, how: "nearest route, to " + string(substitute)}, nil
			}
		}
	}

	// Nothing is routed anywhere near where this flight is going; the exits
	// themselves say which way each one leaves.
	if c, ok := exitTowardDestination(candidates, origin.Location, trueHeading,
		ss.NmPerLongitude); ok {
		return departureChoice{candidate: c, fit: fitNearestGate, how: "nearest gate"}, nil
	}
	return departureChoice{route: filed}, fmt.Errorf("%w: no modeled departure heads toward %s",
		errNoScenarioRoute, destination)
}

// stripSubstituteTail removes the parts of a borrowed route that belong to its
// own destination rather than the flight's: the trailing airport token and the
// STAR ahead of it.
func stripSubstituteTail(route string, substitute av.ICAOAirportCode) string {
	fields := av.TrimDestinationAirportTokens(db.Lookups{}, strings.Fields(route), substitute)
	if n := len(fields); n > 0 {
		last := fields[n-1]
		if c := last[len(last)-1]; c >= '0' && c <= '9' {
			if _, ok := db.DB.Airways[last]; !ok {
				fields = fields[:n-1]
			}
		}
	}
	return strings.Join(fields, " ")
}

// realRoute is one way a city pair is really flown, from the scraped recent
// filings or from the FAA databases.
type realRoute struct {
	route        string
	departureFix string // a coded departure route names its own
	how          string
	minAltitude  int // the scraped filed altitudes, when known
	maxAltitude  int
}

// realDepartureRoutes returns the ways the pair is really flown: recently
// scraped filings first, ordered for the aircraft and the hour of day,
// followed by the FAA databases' routes.
func realDepartureRoutes(from, to av.ICAOAirportCode, aircraftType string, hour int, hourKnown bool) []realRoute {
	var routes []realRoute
	for _, r := range orderScrapedRoutes(db.DB.ScrapedRoutesBetween(from, to),
		aircraftType, hour, hourKnown) {
		routes = append(routes, realRoute{route: r.Route, how: "scraped route",
			minAltitude: r.MinAltitude, maxAltitude: r.MaxAltitude})
	}
	for _, r := range eligibleAirportPairRoutes(db.DB.RoutesBetween(from, to),
		engineTypeFor(aircraftType)) {
		routes = append(routes, realRoute{route: r.Route, departureFix: r.DepartureFix,
			how: "faa route"})
	}
	return routes
}

// exitHeadingDifference is how far a candidate's exit fix lies from the
// direction the flight is really going. A fix the database can't place gets no
// say either way.
func exitHeadingDifference(c candidateDeparture, airport math.Point2LL,
	trueHeading math.TrueHeading, nmPerLongitude float32) (float32, bool) {
	exit, ok := db.DB.LookupWaypoint(c.dep.Exit.Base())
	if !ok {
		return 0, false
	}
	return math.HeadingDifference(trueHeading, math.Heading2LL(airport, exit, nmPerLongitude)), true
}

// exitTowardDestination picks the candidate whose exit fix lies closest in
// direction to where the flight is really going.
func exitTowardDestination(candidates []candidateDeparture, airport math.Point2LL,
	trueHeading math.TrueHeading, nmPerLongitude float32) (candidateDeparture, bool) {
	var best candidateDeparture
	bestDifference := float32(0)
	for _, c := range candidates {
		difference, ok := exitHeadingDifference(c, airport, trueHeading, nmPerLongitude)
		if !ok || difference > publishedDepartureMaxHeadingDifference {
			continue
		}
		if best.dep == nil || difference < bestDifference {
			best, bestDifference = c, difference
		}
	}
	return best, best.dep != nil
}

// eligibleAirportPairRoutes filters the FAA preferred routes for a city pair to
// the ones the aircraft can fly and orders them by preference: jets take
// high-altitude routes first, everything else low-altitude ones.
func eligibleAirportPairRoutes(routes []db.AirportPairRoute, engineType string) []db.AirportPairRoute {
	eligible := func(r db.AirportPairRoute) bool {
		switch engineType {
		case "P": // pistons fly conventional, non-jet routes
			return !r.RNAVRequired && r.Aircraft != "jet"
		case "J":
			return r.Aircraft != "prop"
		default: // turboprops and anything unknown
			return r.Aircraft != "jet"
		}
	}

	var ordered []db.AirportPairRoute
	lowFirst := engineType != "J"
	for _, low := range []bool{lowFirst, !lowFirst} {
		for _, r := range routes {
			if r.LowAltitude() == low && eligible(r) {
				ordered = append(ordered, r)
			}
		}
	}
	return ordered
}

// departureExit finds the compatible departure whose exit a filed route
// leaves through: the first of the route's fixes the scenario models, since
// that is the one the flight actually goes out over. Failing that, the
// route's first fix tells where the flight rejoins its own navigation on a
// SID the exits fly, and the exit behind that fix on the charted path is the
// gate it goes out over--JFK to Las Vegas files "KJFK DEEZZ6 CANDR J60...",
// resuming at CANDR with the DEEZZ exit behind it. When the route leaves the
// SID before reaching any exit, the exit ahead stands in. A coded departure
// route's own departure fix is the last thing to go on. The filed SID's name
// is never consulted: it may not be the SID the scenario flies for the gate.
func departureExit(route string, departureAirport, destination av.ICAOAirportCode, departureFix string,
	candidates []candidateDeparture) (candidateDeparture, bool) {
	wps := av.TrimDepartureAirportWaypoints(db.Lookups{}, av.RouteWaypoints(db.Lookups{}, route, nil), departureAirport)
	wps = av.TrimDestinationAirportWaypoints(db.Lookups{}, wps, destination)
	if departureFix != "" {
		wps = append(wps, av.Waypoint{Fix: departureFix})
	}

	for _, wp := range wps {
		if i := slices.IndexFunc(candidates, func(c candidateDeparture) bool {
			return c.dep.Exit.Base() == wp.Fix
		}); i != -1 {
			return candidates[i], true
		}
	}

	// No exit fix on the route; find where its first fix joins the SIDs the
	// exits fly.
	exitCandidates := make(map[string]candidateDeparture)
	exitBases := make(map[string]bool)
	var sids []string
	for _, c := range candidates {
		base := c.dep.Exit.Base()
		if _, ok := exitCandidates[base]; !ok {
			exitCandidates[base] = c
			exitBases[base] = true
		}
		if exitRoute, ok := c.exitRoutes[c.dep.Exit]; ok && exitRoute.SID != "" {
			name, _, _ := strings.Cut(exitRoute.SID, ".")
			if !slices.Contains(sids, name) {
				sids = append(sids, name)
			}
		}
	}

	behind, ahead := av.SIDPathExits(enroute.DBLocator{}, departureAirport, wps, exitBases, sids)
	matches := util.Select(len(behind) > 0, behind, ahead)
	if len(matches) == 0 {
		return candidateDeparture{}, false
	}
	if len(matches) == 1 {
		return exitCandidates[matches[0]], true
	}

	// Several paths' exits could stand in; the one nearest the route's
	// first locatable fix is the one the flight leaves through.
	for _, wp := range wps {
		routeFix, ok := db.DB.LookupWaypoint(wp.Fix)
		if !ok {
			continue
		}
		best, bestDistance := "", float32(0)
		for _, m := range matches {
			exit, ok := db.DB.LookupWaypoint(m)
			if !ok {
				continue
			}
			if d := math.NMDistance2LL(exit, routeFix); best == "" || d < bestDistance {
				best, bestDistance = m, d
			}
		}
		if best != "" {
			return exitCandidates[best], true
		}
		break
	}
	return exitCandidates[matches[0]], true
}

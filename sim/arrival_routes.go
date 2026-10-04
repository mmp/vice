// sim/arrival_routes.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
)

// publishedArrivalMaxHeadingDifference is how far an origin may lie from the
// direction an arrival flies in from and still plausibly come in on it. Gates
// are rarely less than this far apart, so a smaller difference doesn't say the
// traffic would come in anywhere else.
const publishedArrivalMaxHeadingDifference = 60 // degrees

var (
	// errNoSuitableArrival: no active arrival can carry the aircraft at all,
	// by class or by altitude.
	errNoSuitableArrival = errors.New("no arrival suits the aircraft")
	// errArrivalSTARInactive: the flight's real route ends with a STAR no
	// active arrival flies, so it is dropped rather than shoehorned onto a
	// flow it never flies.
	errArrivalSTARInactive = errors.New("no active arrival flies STAR")
	// errNoPlausibleArrival: no arrival comes in plausibly from the flight's
	// direction.
	errNoPlausibleArrival = errors.New("no plausible arrival to fly")
)

// arrivalPlacement is the inbound flow and arrival a published flight comes in
// on, the route it files when the route database is what found it, the airport
// standing in for its origin if the scenario has no way to fly it from where it
// really came from, and how the choice was made, for reporting.
type arrivalPlacement struct {
	group      string
	index      int
	filedRoute string
	substitute av.ICAOAirportCode
	cruise     CruiseLimits
	how        string
}

// candidateArrival is an arrival an inbound flow the scenario is running could
// bring a published flight in on. Only the flows it works count: putting an
// arrival on one it doesn't model would hand the controller traffic down a
// feeder nobody is working.
type candidateArrival struct {
	group string
	index int
	arr   *av.Arrival
}

// candidateArrivals gathers them in sorted flow order, so that a choice between
// equally good ones doesn't vary between runs. includeDisabled takes in the
// flows the scenario has switched off, too.
func (ss *CommonState) candidateArrivals(arrivalAirport av.ICAOAirportCode, includeDisabled bool) []candidateArrival {
	arrivalAirport = traffic.NormalizeAirportCode(arrivalAirport)

	var candidates []candidateArrival
	for _, group := range util.SortedMapKeys(ss.InboundFlows) {
		enabled, listed := ss.LaunchConfig.InboundFlowEnabled[group][string(arrivalAirport)]
		if !enabled && !(includeDisabled && listed) {
			continue
		}
		arrivals := ss.InboundFlows[group].Arrivals
		for i := range arrivals {
			if slices.Contains(arrivals[i].Airports, arrivalAirport) {
				candidates = append(candidates, candidateArrival{group, i, &arrivals[i]})
			}
		}
	}
	return candidates
}

// placeArrival decides how a published flight into arrivalAirport from origin
// is flown: the inbound flow and arrival that carry it, the route it files, and
// the airport standing in for its origin when neither the scenario nor the
// route database covers where it really came from. A flight only a flow the
// scenario has switched off would carry fails with errFlowDisabled.
func (ss *CommonState) placeArrival(arrivalAirport, origin av.ICAOAirportCode, aircraftType string,
	routed routedPairs) (arrivalPlacement, error) {
	arrivalAirport = traffic.NormalizeAirportCode(arrivalAirport)
	origin = traffic.NormalizeAirportCode(origin)

	p, err := ss.placeArrivalAmong(ss.candidateArrivals(arrivalAirport, false), arrivalAirport, origin,
		aircraftType, routed)
	if err != nil {
		if d, derr := ss.placeArrivalAmong(ss.candidateArrivals(arrivalAirport, true), arrivalAirport,
			origin, aircraftType, routed); derr == nil {
			return p, fmt.Errorf("%w: %s", errFlowDisabled, d.group)
		}
	}
	return p, err
}

// placeArrivalAmong places the flight on one of the given candidates.
func (ss *CommonState) placeArrivalAmong(candidates []candidateArrival, arrivalAirport, origin av.ICAOAirportCode,
	aircraftType string, routed routedPairs) (arrivalPlacement, error) {
	if len(candidates) == 0 {
		return arrivalPlacement{}, errNoPlausibleArrival
	}
	if len(suitableArrivals(candidates, aircraftType)) == 0 {
		return arrivalPlacement{}, errNoSuitableArrival
	}

	hour, hourKnown := ss.localHour(arrivalAirport)
	scenarioRoutes := func(from av.ICAOAirportCode) []string {
		if ap, ok := ss.Airports[arrivalAirport]; ok {
			return ap.TrafficRoutes.Arrivals[from].Routes(db.Lookups{}, aircraftType)
		}
		return nil
	}
	scrapedRoutes := func(from av.ICAOAirportCode) []av.ScrapedRoute {
		return orderScrapedRoutes(db.DB.ScrapedRoutesBetween(from, arrivalAirport),
			aircraftType, hour, hourKnown)
	}
	scrapedNames := func(routes []av.ScrapedRoute) []string {
		return util.MapSlice(routes, func(r av.ScrapedRoute) string { return r.Route })
	}
	faaRoutes := func(from av.ICAOAirportCode) []string {
		eligible := eligibleAirportPairRoutes(faaFallbackRoutes(from, arrivalAirport),
			engineTypeFor(aircraftType))
		return util.MapSlice(eligible, func(r db.AirportPairRoute) string { return r.Route })
	}

	// Use the scenario's routes from this origin if it has any. Otherwise use
	// the pair's scraped routes, or its FAA routes if it has no scraped ones.
	// If none of the routes fits the scenario (e.g., their STAR isn't active),
	// drop the flight instead of moving it to an arrival it wouldn't fly. A
	// scenario that only works one arrival gate shouldn't get all of the
	// airport's arrivals.
	if routes := scenarioRoutes(origin); len(routes) > 0 {
		c, route, err := matchArrivalRoutes(candidates, aircraftType, routes, arrivalAirport, origin)
		if err != nil {
			// The route comes back with the error: it is what says why the
			// scenario has no way to fly the flight.
			return arrivalPlacement{filedRoute: route}, err
		}
		return c.placement(route, "", arrivalCruiseLimits(route, origin, arrivalAirport, nil),
			"scenario route"), nil
	}
	if scraped, faa := scrapedRoutes(origin), faaRoutes(origin); len(scraped)+len(faa) > 0 {
		names := scrapedNames(scraped)
		c, route, err := matchArrivalRoutes(candidates, aircraftType,
			slices.Concat(names, faa), arrivalAirport, origin)
		if err != nil {
			return arrivalPlacement{filedRoute: route}, err
		}
		how := util.Select(slices.Contains(names, route), "scraped route", "faa route")
		return c.placement(route, "", arrivalCruiseLimits(route, origin, arrivalAirport, scraped),
			how), nil
	}

	// Neither knows this origin, so fly the flight the way the nearest airport
	// one of them does know is flown: from JFK, Norfolk stands in for Kill Devil
	// Hills. Real traffic comes from far more airports than either source
	// covers, and arriving as one's neighbors do beats not arriving at all. The
	// flight files its own route rather than the substitute's, which starts
	// somewhere it has never been.
	pool := slices.Clone(routed.originsByDestination[arrivalAirport])
	if ap, ok := ss.Airports[arrivalAirport]; ok {
		for _, from := range util.SortedMapKeys(ap.TrafficRoutes.Arrivals) {
			if len(scenarioRoutes(from)) > 0 {
				pool = append(pool, from)
			}
		}
	}
	for _, substitute := range substituteAirports(arrivalAirport, origin, pool,
		publishedArrivalMaxHeadingDifference) {
		routes := slices.Concat(scenarioRoutes(substitute), scrapedNames(scrapedRoutes(substitute)),
			faaRoutes(substitute))
		if c, _, err := matchArrivalRoutes(candidates, aircraftType, routes, arrivalAirport, substitute); err == nil {
			return c.placement("", substitute, CruiseLimits{}, "nearest route, from "+string(substitute)), nil
		}
	}

	// Nothing is routed anywhere near it; the gate nearest the great-circle arc
	// the flight actually flies is all that is left to go on.
	if c, ok := arrivalNearestArc(suitableArrivals(candidates, aircraftType),
		arrivalAirport, origin); ok {
		return c.placement("", "", CruiseLimits{}, "great-circle gate"), nil
	}
	return arrivalPlacement{}, errNoPlausibleArrival
}

func (c candidateArrival) placement(filedRoute string, substitute av.ICAOAirportCode, cruise CruiseLimits,
	how string) arrivalPlacement {
	return arrivalPlacement{group: c.group, index: c.index, filedRoute: filedRoute,
		substitute: substitute, cruise: cruise, how: how}
}

// arrivalCruiseLimits is what the route a published arrival files says about
// the altitude it cruises at: what its procedures require, and what the pair's
// recent filings of that route were seen at, when it is one of them.
func arrivalCruiseLimits(route string, origin, arrivalAirport av.ICAOAirportCode, scraped []av.ScrapedRoute) CruiseLimits {
	limits := CruiseLimits{Floor: av.RouteAltitudeFloor(db.Lookups{}, route, origin, arrivalAirport)}
	if i := slices.IndexFunc(scraped, func(r av.ScrapedRoute) bool { return r.Route == route }); i != -1 {
		limits.Low, limits.High = scraped[i].MinAltitude, scraped[i].MaxAltitude
	}
	return limits
}

// matchArrivalRoutes matches each route in turn against the candidates,
// returning the first one of them can fly. The first route's failure is the
// one reported, along with the route it failed on: it is the preferred way the
// pair is flown, and it is what says why the flight can't be flown.
func matchArrivalRoutes(candidates []candidateArrival, aircraftType string, routes []string,
	arrivalAirport, origin av.ICAOAirportCode) (candidateArrival, string, error) {
	var firstErr error
	var firstRoute string
	for _, route := range routes {
		c, err := matchArrivalRoute(candidates, aircraftType, route, arrivalAirport, origin)
		if err == nil {
			return c, route, nil
		}
		if firstErr == nil {
			firstErr, firstRoute = err, route
		}
	}
	if firstErr == nil {
		firstErr = errNoPlausibleArrival
	}
	return candidateArrival{}, firstRoute, firstErr
}

// matchArrivalRoute finds the candidate arrival a filed route into the airport
// comes in on. A route that ends with a STAR belongs to the arrivals that take
// that STAR's traffic, and the CIFP transition the route joins it at says which
// of them the flight actually reaches. A route with no STAR--GA and
// terminal-en-route traffic--comes in through the gate nearest its origin.
// Suitability is judged here rather than up front so that the errors can tell
// an inactive STAR apart from active arrivals that don't admit the aircraft.
func matchArrivalRoute(candidates []candidateArrival, aircraftType, route string, arrivalAirport,
	origin av.ICAOAirportCode) (candidateArrival, error) {
	star, entry := av.RouteSTAR(db.Lookups{}, route, traffic.NormalizeAirportCode(arrivalAirport))
	if star == "" {
		suitable := suitableArrivals(candidates, aircraftType)
		if len(suitable) == 0 {
			return candidateArrival{}, errNoSuitableArrival
		}
		if c, ok := nearestSpawnToOrigin(suitable, arrivalAirport, origin); ok {
			return c, nil
		}
		return candidateArrival{}, errNoPlausibleArrival
	}

	matching := util.FilterSlice(candidates, func(c candidateArrival) bool {
		return slices.ContainsFunc(c.arr.ServedSTARs(), func(s string) bool {
			return av.ProcedureBase(s) == av.ProcedureBase(star)
		})
	})
	if len(matching) == 0 {
		return candidateArrival{}, fmt.Errorf("%w %s into %s", errArrivalSTARInactive,
			star, arrivalAirport)
	}
	matching = suitableArrivals(matching, aircraftType)
	if len(matching) == 0 {
		return candidateArrival{}, fmt.Errorf("%w among those flying the %s",
			errNoSuitableArrival, star)
	}
	if len(matching) == 1 {
		return matching[0], nil
	}

	// Several arrivals fly the STAR; walking the CIFP transition the route
	// enters through says which of them the flight reaches, the one joined
	// soonest after the entry fix winning: that is the gate, while a later
	// join is a feeder it would only pass on the way in.
	cifp := db.DB.Airports[traffic.NormalizeAirportCode(arrivalAirport)].STARs[star]
	if entry != "" {
		best, bestJoin := -1, 0
		for _, name := range util.SortedMapKeys(cifp.Transitions) {
			wps := cifp.Transitions[name]
			entryIndex := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == entry })
			if entryIndex == -1 {
				continue
			}
			for i, c := range matching {
				fixes := arrivalWaypointFixes(c.arr)
				join := slices.IndexFunc(wps[entryIndex:],
					func(wp av.Waypoint) bool { return fixes[wp.Fix] })
				if join != -1 && (best == -1 || join < bestJoin) {
					best, bestJoin = i, join
				}
			}
		}
		if best != -1 {
			return matching[best], nil
		}
	}

	// The entry fix is unknown or on no charted transition; the route's own
	// fixes are the next best evidence, the arrival matching furthest along
	// it winning: that is where the flight enters the terminal area, while an
	// earlier fix is only somewhere it passed on the way in.
	best, bestIndex := -1, -1
	fixes := enrouteFixes(route)
	for i, c := range matching {
		for fix := range arrivalWaypointFixes(c.arr) {
			if j := slices.Index(fixes, fix); j > bestIndex {
				best, bestIndex = i, j
			}
		}
	}
	if best != -1 {
		return matching[best], nil
	}

	// Nothing on the route pins it down; the gate nearest the great circle
	// from the origin is the most plausible.
	if c, ok := arrivalNearestArc(matching, arrivalAirport, origin); ok {
		return c, nil
	}
	return matching[0], nil
}

// suitableArrivals filters the candidates to those the aircraft can fly: the
// arrival's aircraft classes and its altitudes both have to admit it.
func suitableArrivals(candidates []candidateArrival, aircraftType string) []candidateArrival {
	perf, ok := db.DB.AircraftPerformance[aircraftType]
	return util.FilterSlice(candidates, func(c candidateArrival) bool {
		if !c.arr.Aircraft.Matches(db.Lookups{}, aircraftType) {
			return false
		}
		return !ok || arrivalWithinCeiling(c.arr, perf)
	})
}

// arrivalWithinCeiling reports whether the aircraft can fly the arrival's
// altitudes: the lowest one it may spawn at has to be within its ceiling. The
// arrival's cruise altitudes have no say--they fill in the filed altitude on
// the flight strip and are never flown.
func arrivalWithinCeiling(arr *av.Arrival, perf av.AircraftPerformance) bool {
	if len(arr.InitialAltitudes) > 0 {
		return float32(slices.Min(arr.InitialAltitudes)) <= perf.Ceiling
	}
	if len(arr.Waypoints) > 0 {
		if wp := arr.Waypoints[0]; wp.HasAltitudeRestriction() && wp.AltRestriction.Range[0] > perf.Ceiling {
			return false
		}
	}
	return true
}

// arrivalWaypointFixes is the set of real fixes the arrival flies over.
// Waypoints synthesized during deserialization are prefixed with an underscore
// and are no part of any charted route.
func arrivalWaypointFixes(arr *av.Arrival) map[string]bool {
	fixes := make(map[string]bool)
	for _, wp := range arr.Waypoints {
		if !strings.HasPrefix(wp.Fix, "_") {
			fixes[wp.Fix] = true
		}
	}
	return fixes
}

// enrouteFixes returns the fixes a real route names between its endpoints. A
// route reads "ORIGIN ...fixes... DESTINATION", so its two ends are airport
// identifiers rather than points to match a scenario's exits or arrivals
// against: every route into JFK ends with "JFK".
func enrouteFixes(route string) []string {
	fixes := strings.Fields(route)
	if len(fixes) <= 2 {
		return nil
	}
	return fixes[1 : len(fixes)-1]
}

// nearestSpawnToOrigin picks the arrival whose spawn point lies nearest the
// origin, gated by heading so the flight doesn't come in through a gate
// pointing somewhere else entirely.
func nearestSpawnToOrigin(candidates []candidateArrival, arrivalAirport,
	origin av.ICAOAirportCode) (candidateArrival, bool) {
	ap, apOK := db.DB.Airports[traffic.NormalizeAirportCode(arrivalAirport)]
	from, fromOK := db.DB.Airports[traffic.NormalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return candidateArrival{}, false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	best, bestDistance := -1, float32(0)
	for i, c := range candidates {
		if len(c.arr.Waypoints) == 0 {
			continue
		}
		spawn := c.arr.Waypoints[0].Location
		if math.HeadingDifference(math.GreatCircleHeading(ap.Location, spawn),
			toOrigin) > publishedArrivalMaxHeadingDifference {
			continue
		}
		if d := math.NMDistance2LL(spawn, from.Location); best == -1 || d < bestDistance {
			best, bestDistance = i, d
		}
	}
	if best == -1 {
		return candidateArrival{}, false
	}
	return candidates[best], true
}

// arrivalNearestArc is the last resort when no route covers the pair, foreign
// origins mostly: the gate nearest the great circle the flight actually flies,
// among those pointing plausibly toward its origin at all. With one gate
// active a bare minimum-distance pick would take any flight from anywhere.
func arrivalNearestArc(candidates []candidateArrival, arrivalAirport,
	origin av.ICAOAirportCode) (candidateArrival, bool) {
	ap, apOK := db.DB.Airports[traffic.NormalizeAirportCode(arrivalAirport)]
	from, fromOK := db.DB.Airports[traffic.NormalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return candidateArrival{}, false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	best, bestDistance := -1, float32(0)
	for i, c := range candidates {
		if len(c.arr.Waypoints) == 0 {
			continue
		}
		spawn := c.arr.Waypoints[0].Location
		if math.HeadingDifference(math.GreatCircleHeading(ap.Location, spawn),
			toOrigin) > publishedArrivalMaxHeadingDifference {
			continue
		}
		d := math.NMDistanceToSegment2LL(spawn, from.Location, ap.Location)
		if best == -1 || d < bestDistance {
			best, bestDistance = i, d
		}
	}
	if best == -1 {
		return candidateArrival{}, false
	}
	return candidates[best], true
}

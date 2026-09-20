// aviation/overflight.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Overflight

type Overflight struct {
	Waypoints           WaypointArray           `json:"waypoints"`
	InitialAltitudes    util.SingleOrArray[int] `json:"initial_altitude"`
	CruiseAltitudes     util.SingleOrArray[int] `json:"cruise_altitude"`
	AssignedAltitude    float32                 `json:"assigned_altitude"`
	InitialSpeed        Airspeed                `json:"initial_speed"`
	AssignedSpeed       float32                 `json:"assigned_speed"`
	SpeedRestriction    SpeedRestriction        `json:"speed_restriction"`
	InitialController   ControlPosition         `json:"initial_controller"`
	Scratchpad          string                  `json:"scratchpad"`
	SecondaryScratchpad string                  `json:"secondary_scratchpad"`
	Description         string                  `json:"description"`
	IsRNAV              bool                    `json:"is_rnav"`
	Airlines            []OverflightAirline     `json:"airlines"`
	TypeOfFlightString  string                  `json:"flight_type"`
	TypeOfFlight        TypeOfFlight            // set via TypeOfFlightString
}

type OverflightAirline struct {
	AirlineSpecifier
	DepartureAirport ICAOAirportCode `json:"departure_airport"`
	ArrivalAirport   ICAOAirportCode `json:"arrival_airport"`
}

func (of *Overflight) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[ICAOAirportCode]*Airport, controlPositions map[ControlPosition]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())
	if len(of.Waypoints) < 2 {
		e.ErrorString(`must provide at least two "waypoints" for overflight`)
	}

	of.Waypoints = of.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

	of.Waypoints[len(of.Waypoints)-1].MergeActions(WaypointActions{Delete: true})
	of.Waypoints[len(of.Waypoints)-1].SetFlyOver(true)

	of.Waypoints.CheckOverflight(e, controlPositions, checkScratchpad)

	if len(of.Airlines) == 0 {
		e.ErrorString(`must specify at least one airline in "airlines"`)
	}
	for i := range of.Airlines {
		of.Airlines[i].Check(e)

		if of.Airlines[i].DepartureAirport == "" {
			e.ErrorString(`must specify "departure_airport"`)
		} else if _, ok := airports[of.Airlines[i].DepartureAirport]; !ok {
			if err := CheckAirport("departure", of.Airlines[i].DepartureAirport); err != nil {
				e.Error(err)
			}
		}

		if of.Airlines[i].ArrivalAirport == "" {
			e.ErrorString(`must specify "arrival_airport"`)
		} else if _, ok := airports[of.Airlines[i].ArrivalAirport]; !ok {
			if err := CheckAirport("arrival", of.Airlines[i].ArrivalAirport); err != nil {
				e.Error(err)
			}
		}
	}

	if len(of.InitialAltitudes) == 0 {
		e.ErrorString(`must specify at least one "initial_altitude"`)
	}

	if of.InitialSpeed.IsZero() {
		e.ErrorString(`must specify "initial_speed"`)
	} else {
		checkSpeed(e, `"initial_speed"`, of.InitialSpeed)
	}

	if of.AssignedSpeed != 0 {
		checkSpeed(e, `"assigned_speed"`, MakeIAS(of.AssignedSpeed))
	}

	if !of.SpeedRestriction.IsZero() {
		checkSpeedRange(e, of.SpeedRestriction)
	}

	if of.InitialController == "" {
		e.ErrorString(`Must specify "initial_controller".`)
	} else if _, ok := controlPositions[of.InitialController]; !ok {
		e.ErrorString(`controller %q not found for "initial_controller"`, of.InitialController)
	}

	if !checkScratchpad(of.Scratchpad) {
		e.ErrorString("%s: invalid scratchpad", of.Scratchpad)
	}
	if !checkScratchpad(of.SecondaryScratchpad) {
		e.ErrorString("%s: invalid secondary scratchpad", of.SecondaryScratchpad)
	}

	switch of.TypeOfFlightString {
	case "", "overflight":
		of.TypeOfFlight = FlightTypeOverflight
	case "departure":
		of.TypeOfFlight = FlightTypeDeparture
	case "arrival":
		of.TypeOfFlight = FlightTypeArrival
	default:
		e.ErrorString(`%s: unknown "flight_type" value. Options: "departure", "arrival", "overflight".`,
			of.TypeOfFlightString)
	}

}

///////////////////////////////////////////////////////////////////////////
// RouteGenerator

// RouteGenerator is a utility class for describing lateral routes with
// respect to a local coordinate system. The user provides two points
// (generally the endpoints of a runway) which are then at (-1,0) and
// (1,0) in the coordinate system. The y axis is perpendicular to the vector
// between the two points and points to the left of it. (Thus, note that
// lengths in the two dimensions are different.)
type RouteGenerator struct {
	p0, p1         [2]float32
	origin         [2]float32
	xvec, yvec     [2]float32 // basis vectors
	nmPerLongitude float32
}

func MakeRouteGenerator(p0ll, p1ll math.Point2LL, nmPerLongitude float32) RouteGenerator {
	rg := RouteGenerator{
		p0:             math.LL2NM(p0ll, nmPerLongitude),
		p1:             math.LL2NM(p1ll, nmPerLongitude),
		nmPerLongitude: nmPerLongitude,
	}
	rg.origin = math.Mid2f(rg.p0, rg.p1)
	rg.xvec = math.Scale2f(math.Sub2f(rg.p1, rg.p0), 0.5)
	rg.yvec = math.Normalize2f([2]float32{-rg.xvec[1], rg.xvec[0]})
	return rg
}

func (rg RouteGenerator) Waypoint(name string, dx, dy float32) Waypoint {
	p := math.Add2f(rg.origin, math.Add2f(math.Scale2f(rg.xvec, dx), math.Scale2f(rg.yvec, dy)))
	return Waypoint{
		Fix:      name,
		Location: math.NM2LL(p, rg.nmPerLongitude),
	}
}

// RouteRayIntersection extends math.RayRouteIntersection with the index of
// the WaypointArray the hit falls on.
type RouteRayIntersection struct {
	math.RayRouteIntersection
	RouteIndex int
}

// IntersectRayWithRoutes runs math.IntersectRayWithRoute on each entry in
// routes and returns all hits. Callers apply their own scoring (closest,
// earliest, turn-angle tiers, etc.).
func IntersectRayWithRoutes(origin math.Point2LL, heading math.TrueHeading, routes []WaypointArray) []RouteRayIntersection {
	var hits []RouteRayIntersection
	for ri, route := range routes {
		pts := make([]math.Point2LL, len(route))
		for i, wp := range route {
			pts[i] = wp.Location
		}
		for _, h := range math.IntersectRayWithRoute(origin, heading, pts) {
			hits = append(hits, RouteRayIntersection{RayRouteIntersection: h, RouteIndex: ri})
		}
	}
	return hits
}

// ClosestRayRouteIntersection returns the hit with the smallest distance
// from origin across all supplied routes.
func ClosestRayRouteIntersection(origin math.Point2LL, heading math.TrueHeading, routes []WaypointArray) (RouteRayIntersection, bool) {
	var best RouteRayIntersection
	found := false
	bestDist := float32(0)
	for _, h := range IntersectRayWithRoutes(origin, heading, routes) {
		d := math.NMDistance2LL(origin, h.Location)
		if !found || d < bestDist {
			best = h
			bestDist = d
			found = true
		}
	}
	return best, found
}

///////////////////////////////////////////////////////////////////////////

// ScrapedRoute is one way a city pair has recently been flown, taken from
// recently filed flight plans by cmd/scraperoutes.
type ScrapedRoute struct {
	Route string `json:"route"`
	// Count is how many times the route was filed over the sampled period.
	Count int `json:"count"`
	// Aircraft is the classes observed flying the route; zero means no one
	// looked.
	Aircraft AircraftClass `json:"aircraft,omitempty"`
	// MinAltitude and MaxAltitude bound the filed cruise altitudes, in feet.
	MinAltitude int `json:"min_altitude,omitempty"`
	MaxAltitude int `json:"max_altitude,omitempty"`
	// Hours is the local hours of day the route has been observed filed at:
	// noise abatement runs some routes only at night.
	Hours HourRanges `json:"hours,omitempty"`
}

// ScrapedRouteSet is everything the scraper knows about one city pair. An
// entry with no routes still records that the pair was looked up, so that it
// isn't fetched again until it goes stale.
type ScrapedRouteSet struct {
	// Updated is the YYYY-MM-DD day the pair was last fetched, which is what
	// cmd/scraperoutes judges staleness by.
	Updated string         `json:"updated"`
	Routes  []ScrapedRoute `json:"routes,omitempty"`
}

// ScrapedRoutesPath is where the scraped route database lives in the
// resources, keyed by "KSFO-KPDX"-style directed city pairs.
const ScrapedRoutesPath = "scraped-routes.json"

// ReadScrapedRoutes parses the scraped route database, or returns an empty
// map if none has been written yet.
func ReadScrapedRoutes(resources fs.StatFS) (map[string]ScrapedRouteSet, error) {
	sets := make(map[string]ScrapedRouteSet)
	if _, err := resources.Stat(ScrapedRoutesPath); err != nil {
		return sets, nil
	}
	b, err := fs.ReadFile(resources, ScrapedRoutesPath)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &sets); err != nil {
		return nil, fmt.Errorf("%s: %w", ScrapedRoutesPath, err)
	}
	return sets, nil
}

// parseScrapedRoutes loads the scraped route database for route selection,
// most-filed routes first.
func parseScrapedRoutes() map[AirportPair][]ScrapedRoute {
	sets, err := ReadScrapedRoutes(util.GetResourcesFS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	routes := make(map[AirportPair][]ScrapedRoute)
	for key, set := range sets {
		if len(set.Routes) == 0 {
			continue
		}
		from, to, ok := strings.Cut(key, "-")
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: %q isn't a FROM-TO city pair\n", ScrapedRoutesPath, key)
			os.Exit(1)
		}
		routes[AirportPair{From: ICAOAirportCode(from), To: ICAOAirportCode(to)}] = set.Routes
	}
	return routes
}

// HourRanges is a set of hours of the day, held as a bit per hour and encoded
// in JSON as ranges like "6-9,22-23".
type HourRanges uint32

// Contains reports whether the hour is in the set.
func (h HourRanges) Contains(hour int) bool {
	return h&(1<<(((hour%24)+24)%24)) != 0
}

// Add puts the hour in the set.
func (h *HourRanges) Add(hour int) {
	*h |= 1 << (((hour % 24) + 24) % 24)
}

func (h HourRanges) String() string {
	var ranges []string
	for hour := 0; hour < 24; {
		if !h.Contains(hour) {
			hour++
			continue
		}
		end := hour
		for end+1 < 24 && h.Contains(end+1) {
			end++
		}
		if end == hour {
			ranges = append(ranges, strconv.Itoa(hour))
		} else {
			ranges = append(ranges, fmt.Sprintf("%d-%d", hour, end))
		}
		hour = end + 1
	}
	return strings.Join(ranges, ",")
}

func (h HourRanges) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

func (h *HourRanges) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	*h = 0
	if s == "" {
		return nil
	}
	for r := range strings.SplitSeq(s, ",") {
		first, last, isRange := strings.Cut(r, "-")
		start, err := strconv.Atoi(first)
		if err != nil {
			return fmt.Errorf("%q: %w", r, err)
		}
		end := start
		if isRange {
			if end, err = strconv.Atoi(last); err != nil {
				return fmt.Errorf("%q: %w", r, err)
			}
		}
		if start < 0 || end > 23 || start > end {
			return fmt.Errorf("%q: hours must run from 0 to 23", r)
		}
		for hour := start; hour <= end; hour++ {
			h.Add(hour)
		}
	}
	return nil
}

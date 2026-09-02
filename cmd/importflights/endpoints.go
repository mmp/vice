// cmd/importflights/endpoints.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Placing the two ends of a track at airports. The source data gives the
// airports near where a track began and ended, the points themselves, and the
// itinerary it looked up from the flight's callsign; between them they say
// where the flight departed from and where it arrived.

package main

import (
	gomath "math"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// What the source data's altitude columns say for a track point whose aircraft
// was on the ground. It is the commonest value in them.
const groundValue = "ground"

// trackEnd is what the source data says about where a track was first or last
// seen: the airports near that point, the point itself, and how high the
// aircraft was.
type trackEnd struct {
	candidates  []string
	position    math.Point2LL
	hasPosition bool
	height      float32 // feet MSL
	hasHeight   bool
	onGround    bool
}

// endpoint is where one end of a track has been placed: the airport, and
// whether the aircraft was there rather than merely passing over it. An empty
// airport means there was nothing to place it with at all.
type endpoint struct {
	airport   string
	atAirport bool
}

func (e endpoint) known() bool { return e.airport != "" }

// parseTrackEnd gathers what the source data says about one end of a track.
func parseTrackEnd(airports, latitude, longitude, altitude string) trackEnd {
	e := trackEnd{candidates: parseAirportList(airports)}

	lat, okLat := parseFloat(latitude)
	long, okLong := parseFloat(longitude)
	if okLat && okLong {
		e.position, e.hasPosition = math.Point2LL{long, lat}, true
	}

	if altitude == groundValue {
		e.onGround = true
	} else {
		e.height, e.hasHeight = parseFloat(altitude)
	}

	return e
}

// parseFloat parses one of the source data's numeric columns. A value it
// doesn't have is written "-" or "nan", and strconv reads "nan" and "inf" as
// numbers rather than rejecting them, so anything that isn't finite is the
// source having nothing to say rather than a number to use.
func parseFloat(value string) (float32, bool) {
	v, err := strconv.ParseFloat(value, 32)
	if err != nil || gomath.IsNaN(v) || gomath.IsInf(v, 0) {
		return 0, false
	}
	return float32(v), true
}

// A track point is taken to be at an airport when it is no farther from it than
// this and, if the aircraft was airborne, no higher than this above the field.
// Measured rather than guessed. Over the 2026 Q2 data, importflights -calibrate
// scores these against the ends the itinerary settles: they accept 37% of the
// track ends it can score and pick the itinerary's airport for 99.9% of those.
// Reaching further--five miles airborne rather than two--accepts 40% but gets
// 99.4% right, which is a poor trade when the airports being told apart are a
// few miles from each other in the first place.
const (
	maxGroundDistance   = 5    // nm
	maxAirborneDistance = 2    // nm
	maxHeightAboveField = 1000 // feet
)

// at reports whether the track point is where an aircraft operating at ap would
// be rather than one that merely flew over it. An aircraft whose height the
// source data doesn't give is taken not to have been there: airports come in
// clusters, and height is what separates a departure from an overflight.
func (e trackEnd) at(ap av.FAAAirport, distance float32) bool {
	if e.onGround {
		return distance <= maxGroundDistance
	}
	return e.hasHeight && distance <= maxAirborneDistance &&
		e.height <= float32(ap.Elevation)+maxHeightAboveField
}

// nearest returns the candidate airport closest to the point the track was seen
// at and how far away it was. Candidates the airport database doesn't know are
// passed over, since there is no telling how far away they are.
func (e trackEnd) nearest(airports map[string]av.FAAAirport) (string, av.FAAAirport, float32, bool) {
	if !e.hasPosition {
		return "", av.FAAAirport{}, 0, false
	}

	var icao string
	var nearest av.FAAAirport
	distance, found := math.Infinity, false
	for _, id := range e.candidates {
		ap, ok := airports[id]
		if !ok {
			continue
		}
		if d := math.NMDistance2LL(e.position, ap.Location); d < distance {
			icao, nearest, distance, found = id, ap, d, true
		}
	}
	return icao, nearest, distance, found
}

// maxOverflightHeight is how far above a field an aircraft can be and still be
// taken to have departed from or landed at it. Higher than this it is enroute
// whatever else is true: the track began or ended in the middle of the flight,
// the airport underneath saw nothing, and the time the record would carry is
// not the time of anything that happened there. Flight level 180 is where the
// aviation world itself stops calling the airspace terminal.
const maxOverflightHeight = 18000 // feet

// overflying reports whether the aircraft was too high above the field for the
// track to be recording anything that happened at it.
func (e trackEnd) overflying(ap av.FAAAirport) bool {
	return e.hasHeight && e.height > float32(ap.Elevation)+maxOverflightHeight
}

// resolveEndpoint places one end of a track from the track alone: the candidate
// airport nearest the point the aircraft was seen at, and whether it was
// plausibly there.
func resolveEndpoint(e trackEnd, airports map[string]av.FAAAirport) endpoint {
	// A lone candidate is taken at its word about which airport it is, since
	// the source data offers nowhere else the aircraft could have been. Whether
	// the aircraft was there at all is still worth asking: an airport with no
	// neighbors is the applicable one for everything that passes overhead, and
	// that is how a flight from St. Croix to Paris comes to be recorded as
	// departing an island it was crossing at altitude.
	if len(e.candidates) == 1 {
		icao := e.candidates[0]
		ap, ok := airports[icao]
		return endpoint{airport: icao, atAirport: !ok || !e.overflying(ap)}
	}

	icao, ap, distance, ok := e.nearest(airports)
	if !ok {
		return endpoint{}
	}
	return endpoint{airport: icao, atAirport: e.at(ap, distance)}
}

// routeEndpoints places whichever ends the itinerary agrees on, considering the
// legs that fit the airports the track itself suggests. A round trip leaves the
// end it returns to undecided while still deciding the other one.
func routeEndpoints(route, origins, destinations []string) (from, to endpoint) {
	matched := false
	for i := 0; i+1 < len(route); i++ {
		f, t := route[i], route[i+1]
		if len(origins) > 0 && !slices.Contains(origins, f) {
			continue
		}
		if len(destinations) > 0 && !slices.Contains(destinations, t) {
			continue
		}
		if !matched {
			from, to = endpoint{airport: f, atAirport: true}, endpoint{airport: t, atAirport: true}
			matched = true
			continue
		}
		// More than one leg fits; whichever end they disagree about is no
		// longer decided, but the other one still is.
		if from.airport != f {
			from = endpoint{}
		}
		if to.airport != t {
			to = endpoint{}
		}
	}
	return from, to
}

// resolveEndpoints places both ends of a track. The itinerary is consulted
// first, since it says where the flight was going rather than where its track
// happened to be picked up; the track places whichever ends it leaves open.
// Each end is settled on its own, so an origin that could be any of a cluster
// of airports no longer costs us the arrival at the destination it names
// outright.
func resolveEndpoints(origin, destination trackEnd, route []string,
	airports map[string]av.FAAAirport) (from, to endpoint) {
	from, to = routeEndpoints(route, origin.candidates, destination.candidates)
	if !from.known() {
		from = resolveEndpoint(origin, airports)
	}
	if !to.known() {
		to = resolveEndpoint(destination, airports)
	}
	return from, to
}

// parseAirportList parses the airport lists in the source data, which are
// formatted as Python lists: "['KMSP']" or "['KACT', 'KAUS']". A track that
// started or ended too far from any airport has "-" instead. The source data
// predates any airport the FAA has since re-identified, so the identifiers are
// canonicalized here rather than at every place they are used.
func parseAirportList(value string) []string {
	var airports []string
	var current strings.Builder

	flush := func() {
		if current.Len() == 4 {
			airports = append(airports, av.CurrentAirportId(current.String()))
		}
		current.Reset()
	}

	for _, ch := range value {
		if ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			current.WriteRune(ch)
		} else {
			flush()
		}
	}
	flush()

	return airports
}

// parseRoute parses the itinerary the source data looked up from the flight's
// callsign, e.g. "KMSP-KSTL" or the multi-leg "KMSP-KAUS-KMSP". It is "nan"
// when the callsign wasn't found, which is most of the general aviation
// traffic.
func parseRoute(value string) []string {
	var route []string
	for airport := range strings.SplitSeq(value, "-") {
		if len(airport) == 4 {
			route = append(route, av.CurrentAirportId(airport))
		}
	}
	return route
}

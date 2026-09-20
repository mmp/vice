// aviation/runway.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
)

type Runway struct {
	Id                         string
	Heading                    math.MagneticHeading
	Threshold                  math.Point2LL
	ThresholdCrossingHeight    int // delta from elevation
	Elevation                  int
	DisplacedThresholdDistance float32 // in nm
}

// RunwayID is a runway identifier that may include a dot-separated suffix
// (e.g., "13R.All"). Use Base() for physical runway lookups/comparisons.
type RunwayID string

func (r RunwayID) Base() string {
	s, _, _ := strings.Cut(string(r), ".")
	return strings.TrimSpace(s)
}

func (r RunwayID) SameRunway(other RunwayID) bool {
	return r.Base() == other.Base()
}

// ExitID is an exit fix identifier that may include a dot-separated suffix
// (e.g., "COLIN.P"). Use Base() for navaid lookups and display.
type ExitID string

func (e ExitID) Base() string {
	s, _, _ := strings.Cut(string(e), ".")
	return s
}

// AirportHasRunway returns true if the given runway exists at the airport (from DB).
func AirportHasRunway(db Database, airport ICAOAirportCode, runway RunwayID) bool {
	if !db.IsPublishedAirport(airport) {
		return false
	}
	base := runway.Base()
	for _, rwy := range db.AirportRunways(airport) {
		if RunwayID(rwy.Id).Base() == base {
			return true
		}
	}
	return false
}

///////////////////////////////////////////////////////////////////////////

type RadarSite struct {
	Char           string        `json:"char"`
	PositionString string        `json:"position"`
	Position       math.Point2LL // not in JSON, set during deserialize

	Elevation      int32   `json:"elevation"`
	PrimaryRange   int32   `json:"primary_range"`
	SecondaryRange int32   `json:"secondary_range"`
	SlopeAngle     float32 `json:"slope_angle"`
	SilenceAngle   float32 `json:"silence_angle"`
}

func (rs *RadarSite) CheckVisibility(p math.Point2LL, altitude int) (primary, secondary bool, distance float32) {
	// Check altitude first; this is a quick first cull that
	// e.g. takes care of everyone on the ground.
	if altitude <= int(rs.Elevation) {
		return
	}

	// Time to check the angles..
	palt := float32(altitude) * math.FeetToNauticalMiles
	ralt := float32(rs.Elevation) * math.FeetToNauticalMiles
	dalt := palt - ralt
	// not quite true distance, but close enough
	distance = math.NMDistance2LL(rs.Position, p) + math.Abs(palt-ralt)

	// If we normalize the vector from the radar site to the aircraft, then
	// the z (altitude) component gives the cosine of the angle with the
	// "up" direction; in turn, we can check that against the two angles.
	cosAngle := dalt / distance
	// if angle < silence angle, we can't see it, but the test flips since
	// we're testing cosines.
	// FIXME: it's annoying to be repeatedly computing these cosines here...
	if cosAngle > math.Cos(math.Radians(rs.SilenceAngle)) {
		// inside the cone of silence
		return
	}
	// similarly, if angle > 90-slope angle, we can't see it, but again the
	// test flips.
	if cosAngle < math.Cos(math.Radians(90-rs.SlopeAngle)) {
		// below the slope angle
		return
	}

	primary = distance <= float32(rs.PrimaryRange)
	secondary = !primary && distance <= float32(rs.SecondaryRange)
	return
}

func cleanRunway(rwy string) string {
	// The runway may have extra text to distinguish different
	// configurations (e.g., "13.JFK-ILS-13" or "22L,22R"). Find the
	// prefix that is an actual runway specifier to use in the search
	// below. Identifiers are usually a number plus an optional L/R/C/W,
	// but the CIFP also has the likes of "15U" (unpaved) and "NE"/"SW"
	// (unnumbered turf strips).
	n := 0
	for n < len(rwy) && rwy[n] >= '0' && rwy[n] <= '9' {
		n++
	}
	if n == 0 {
		for n < len(rwy) && rwy[n] >= 'A' && rwy[n] <= 'Z' {
			n++
		}
		return rwy[:n]
	}
	if n < len(rwy) && (rwy[n] == 'L' || rwy[n] == 'R' || rwy[n] == 'C' || rwy[n] == 'W') {
		n++
	}
	if n < len(rwy) && rwy[n] == 'U' {
		n++
	}
	return rwy[:n]
}

func LookupRunway(db Database, icao ICAOAirportCode, rwy string) (Runway, bool) {
	if !db.IsPublishedAirport(icao) {
		return Runway{}, false
	} else {
		rwy = cleanRunway(rwy)
		idx := slices.IndexFunc(db.AirportRunways(icao), func(r Runway) bool { return r.Id == rwy })
		if idx == -1 {
			return Runway{}, false
		}
		return db.AirportRunways(icao)[idx], true
	}
}

// OppositeRunwayId returns the runway ID for the opposite end of the given runway.
// E.g., "13L" -> "31R", "22R" -> "4L", "9" -> "27".
func OppositeRunwayId(rwy string) string {
	rwy = cleanRunway(rwy)
	if rwy == "" {
		return ""
	}

	n := len(rwy)
	num, ext := "", ""
	switch rwy[n-1] {
	case 'R':
		ext = "L"
		num = rwy[:n-1]
	case 'L':
		ext = "R"
		num = rwy[:n-1]
	case 'C':
		ext = "C"
		num = rwy[:n-1]
	case 'W':
		ext = "W"
		num = rwy[:n-1]
	default:
		num = rwy
	}

	v, err := strconv.Atoi(num)
	if err != nil {
		return ""
	}

	// (v+18)%36 would give 0 for runway 36, so handle 18 specially.
	if v == 18 {
		return "36" + ext
	}
	return fmt.Sprintf("%d", (v+18)%36) + ext
}

func LookupOppositeRunway(db Database, icao ICAOAirportCode, rwy string) (Runway, bool) {
	if !db.IsPublishedAirport(icao) {
		return Runway{}, false
	}

	oppRwy := OppositeRunwayId(rwy)
	if oppRwy == "" {
		return Runway{}, false
	}

	idx := slices.IndexFunc(db.AirportRunways(icao), func(r Runway) bool { return r.Id == oppRwy })
	if idx == -1 {
		return Runway{}, false
	}
	return db.AirportRunways(icao)[idx], true
}

// runwayEndpoints returns the runway's two thresholds in nm coordinates.
func runwayEndpoints(db Database, airport ICAOAirportCode, rwy string, nmPerLongitude float32) (p1, p2 [2]float32, ok bool) {
	var runway, opp Runway
	if runway, ok = LookupRunway(db, airport, rwy); !ok {
		return
	}
	if opp, ok = LookupOppositeRunway(db, airport, rwy); !ok {
		return
	}
	p1 = math.LL2NM(runway.Threshold, nmPerLongitude)
	p2 = math.LL2NM(opp.Threshold, nmPerLongitude)
	return p1, p2, true
}

// RunwayIntersectionPoint returns the point at which the centerlines of the
// two given runways cross, if that point is within maxDistNM of both runway
// segments (threshold to threshold). It returns false for same or
// opposite-direction runway pairs and for parallel runways.
func RunwayIntersectionPoint(db Database, airport ICAOAirportCode, a, b RunwayID, nmPerLongitude, maxDistNM float32) (math.Point2LL, bool) {
	aBase, bBase := a.Base(), b.Base()
	if aBase == bBase || aBase == OppositeRunwayId(bBase) {
		return math.Point2LL{}, false
	}

	a1, a2, ok := runwayEndpoints(db, airport, aBase, nmPerLongitude)
	if !ok {
		return math.Point2LL{}, false
	}
	b1, b2, ok := runwayEndpoints(db, airport, bBase, nmPerLongitude)
	if !ok {
		return math.Point2LL{}, false
	}

	// Check if the infinite centerlines intersect
	p, ok := math.LineLineIntersect(a1, a2, b1, b2)
	if !ok {
		return math.Point2LL{}, false // Lines are parallel
	}

	// Check if the intersection point is within maxDistNM of both runway segments
	if math.PointSegmentDistance(p, a1, a2) > maxDistNM || math.PointSegmentDistance(p, b1, b2) > maxDistNM {
		return math.Point2LL{}, false
	}

	return math.NM2LL(p, nmPerLongitude), true
}

// IntersectingRunways returns all runways at airport that physically intersect
// the given runway. It checks if runway centerlines cross and the intersection
// point is within maxDistNM of both runway segments (threshold to threshold).
// Use maxDistNM=0 for strict threshold-to-threshold intersection, or a small
// value (e.g., 0.5) to account for pavement extending past thresholds.
// Returns both directions for each intersecting runway (e.g., both "13L" and "31R").
func IntersectingRunways(db Database, airport ICAOAirportCode, rwy RunwayID, nmPerLongitude, maxDistNM float32) []string {
	if !db.IsPublishedAirport(airport) {
		return nil
	}

	var intersecting []string
	seen := make(map[string]bool)
	for _, otherRwy := range db.AirportRunways(airport) {
		id := RunwayID(otherRwy.Id).Base()
		if seen[id] {
			continue
		}

		if _, ok := RunwayIntersectionPoint(db, airport, rwy, RunwayID(otherRwy.Id), nmPerLongitude, maxDistNM); ok {
			// Add both this runway and its opposite direction
			seen[id] = true
			intersecting = append(intersecting, id)

			if oppId := OppositeRunwayId(id); oppId != "" && !seen[oppId] {
				seen[oppId] = true
				intersecting = append(intersecting, oppId)
			}
		}
	}

	return intersecting
}

// returns the ratio of air density at the given altitude (in feet) to the
// air density at sea level, subject to assuming the standard atmosphere.

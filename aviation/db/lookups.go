// aviation/db/lookups.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// Lookups answers what the published data holds, so that packages below this
// one can read it through the aviation.Database interface without depending
// on this package.
type Lookups struct{}

func (Lookups) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	if n, ok := DB.Navaids[s]; ok {
		return n.Location, ok
	} else if ap, ok := DB.LookupICAOAirport(av.ICAOAirportCode(s)); ok {
		return ap.Location, ok
	} else if ap, ok := DB.LookupFAAAirport(av.FAAAirportCode(s)); ok {
		return ap.Location, ok
	} else if f, ok := DB.Fixes[s]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else if ident, rwy, found := strings.Cut(s, "-"); found && len(ident) >= 3 {
		if r, ok := av.LookupRunway(Lookups{}, av.ICAOAirportCode(ident), rwy); ok {
			return r.Threshold, true
		}
	}

	return LocateFRD(s, Lookups{}.Locate)
}

// LocateFRD returns the point a fix-radial-distance such as JFK090020 names:
// two or more letters naming a fix, which locate finds, then a magnetic
// radial from it and a distance from it in nm, three digits each. The radial
// is referenced to the fix's station declination if it is a VHF navaid and
// to the magnetic variation where it is otherwise.
func LocateFRD(s string, locate func(string) (math.Point2LL, bool)) (math.Point2LL, bool) {
	n := len(s) - 6
	if n < 2 || !util.IsAllLetters(s[:n]) || !util.IsAllNumbers(s[n:]) {
		return math.Point2LL{}, false
	}
	fix := s[:n]
	radial, _ := strconv.Atoi(s[n : n+3])
	dist, _ := strconv.Atoi(s[n+3:])
	if radial > 360 {
		return math.Point2LL{}, false
	}

	loc, ok := locate(fix)
	if !ok {
		return math.Point2LL{}, false
	}
	variation, ok := radialVariation(fix, loc)
	if !ok {
		return math.Point2LL{}, false
	}
	hdg := math.MagneticToTrue(math.MagneticHeading(radial), variation)
	return math.Offset2LL(loc, hdg, float32(dist), math.NMPerLongitudeAt(loc)), true
}

// FormatFRD returns p as a fix-radial-distance from fix, which is at loc, in
// the form LocateFRD takes. The distance is measured in the projection
// LocateFRD offsets the fix in, so that the FRD names p to within the whole
// degree and nm it gives.
func FormatFRD(fix string, loc, p math.Point2LL) (string, bool) {
	variation, ok := radialVariation(fix, loc)
	if !ok {
		return "", false
	}
	nmPerLongitude := math.NMPerLongitudeAt(loc)
	radial := int(math.Round(float32(math.TrueToMagnetic(math.Heading2LL(loc, p, nmPerLongitude), variation))))
	if radial == 0 {
		radial = 360
	}
	dist := int(math.Round(math.NMDistance2LLFast(loc, p, nmPerLongitude)))
	if dist > 999 {
		return "", false
	}
	return fmt.Sprintf("%s%03d%03d", fix, radial, dist), true
}

// radialVariation returns the variation the radials of the fix at loc are
// referenced to: a VHF navaid's station declination, which the local
// variation has usually drifted from since the station was aligned, or else
// the variation there.
func radialVariation(fix string, loc math.Point2LL) (float32, bool) {
	if d, ok := DB.Declination(fix); ok {
		return d, true
	}
	variation, err := DB.MagneticGrid.Lookup(loc)
	return variation, err == nil
}

func (Lookups) Declination(s string) (float32, bool) {
	return DB.Declination(s)
}

func (Lookups) Airways(name string) ([]av.Airway, bool) {
	aw, ok := DB.Airways[name]
	return aw, ok
}

func (Lookups) IsNavaidOrFix(fix string) bool {
	if _, ok := DB.Navaids[fix]; ok {
		return true
	}
	_, ok := DB.Fixes[fix]
	return ok
}

func (Lookups) CheckAirport(role string, id av.ICAOAirportCode) error {
	return CheckAirport(role, id)
}

func (Lookups) AirportFAACode(icao av.ICAOAirportCode) (av.FAAAirportCode, bool) {
	return ICAOAirportToFAA(icao)
}

func (Lookups) Airline(icao string) (av.Airline, bool) {
	al, ok := DB.Airlines[icao]
	return al, ok
}

func (Lookups) AircraftPerformance(acType string) (av.AircraftPerformance, bool) {
	p, ok := DB.AircraftPerformance[acType]
	return p, ok
}

func (Lookups) AirportLocation(icao av.ICAOAirportCode) (math.Point2LL, bool) {
	ap, ok := DB.Airports[icao]
	return ap.Location, ok
}

func (Lookups) IsPublishedAirport(icao av.ICAOAirportCode) bool {
	_, ok := DB.Airports[icao]
	return ok
}

func (Lookups) AirportElevation(icao av.ICAOAirportCode) int {
	return DB.Airports[icao].Elevation
}

func (Lookups) AirportRunways(icao av.ICAOAirportCode) []av.Runway {
	return DB.Airports[icao].Runways
}

func (Lookups) AirportApproaches(icao av.ICAOAirportCode) map[string]av.Approach {
	return DB.Airports[icao].Approaches
}

func (Lookups) AirportSIDs(icao av.ICAOAirportCode) map[string]av.SID {
	return DB.Airports[icao].SIDs
}

func (Lookups) AirportSTARs(icao av.ICAOAirportCode) map[string]av.STAR {
	return DB.Airports[icao].STARs
}

func (Lookups) ValidRunways(icao av.ICAOAirportCode) string {
	return DB.Airports[icao].ValidRunways()
}

func (Lookups) IsGAFleet(name string) bool {
	_, ok := DB.Airlines["N"].Fleets[name]
	return ok
}

func (Lookups) GAFleetNames() []string {
	return slices.Collect(maps.Keys(DB.Airlines["N"].Fleets))
}

func (Lookups) InClassBOrC(p math.Point2LL, alt int) bool {
	inside := func(vols iter.Seq[[]av.AirspaceVolume]) bool {
		return util.SeqContainsFunc(vols, func(vs []av.AirspaceVolume) bool {
			return slices.ContainsFunc(vs, func(v av.AirspaceVolume) bool { return v.Inside(p, alt) })
		})
	}
	return inside(maps.Values(DB.BravoAirspace)) || inside(maps.Values(DB.CharlieAirspace))
}

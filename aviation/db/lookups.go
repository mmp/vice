// aviation/db/lookups.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"iter"
	"maps"
	"slices"
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

	return math.Point2LL{}, false
}

func (Lookups) Declination(s string) (float32, bool) {
	return DB.Declination(s)
}

func (Lookups) Similar(fix string) []string {
	d1, d2 := util.SelectInTwoEdits(fix, maps.Keys(DB.Navaids), nil, nil)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(DB.Fixes), d1, d2)
	return util.Select(len(d1) > 0, d1, d2)
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

// cmd/importflights/write.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Turning what was imported into the flight data files: gathering each facility's
// flights, leaving out the months the source data barely covers, and encoding
// the rest.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// writeFlightData writes one file per facility, holding the departures and
// arrivals at every airport it works over however many years the source data
// spans. A file is replaced when there are flights for its facility and is
// otherwise left alone. An airport worked by more than one facility appears in
// each of their files, so that every file stands on its own.
func writeFlightData(dir string, imp *importer, facilities map[string]*facility,
	minCoverage float64, dryRun bool) error {
	covered := coveredMonths(imp.daysPresent, minCoverage)
	droppedMonths := make(map[string]int)
	files, flightCount, bytes := 0, 0, 0

	for _, name := range util.SortedMapKeys(facilities) {
		f := facilities[name]

		var flights []av.Flight
		for airport := range f.airports() {
			// An airport standing in for a made-up one is written out under the
			// name the scenarios fly, at both ends of the flight: the Academy
			// scenarios route traffic between their own airports, so a hop
			// between two donors has to arrive as one between their stand-ins.
			name := f.name(airport)
			for _, departure := range []bool{true, false} {
				if departure && !f.departures[airport] || !departure && !f.arrivals[airport] {
					continue
				}
				for _, r := range imp.buckets[bucket{airport: airport, departure: departure}] {
					flights = append(flights, av.Flight{
						Airport:      name,
						Callsign:     imp.symbols.string(r.callsign),
						Other:        f.name(imp.symbols.string(r.other)),
						AircraftType: imp.symbols.string(r.acType),
						Day:          r.day,
						Minute:       int(r.minute),
						Departure:    departure,
					})
				}
			}
		}

		av.SortFlights(flights)
		// Sorting puts the flights that appear in more than one source file
		// next to each other.
		flights = slices.Compact(flights)

		flights, dropped := dropSparseFlights(flights, covered)
		for month, n := range dropped {
			droppedMonths[month] += n
		}
		if len(flights) == 0 {
			continue
		}

		encoded, err := av.EncodeFlights(flights)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		if !dryRun {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, flightDataFilename(name)), encoded, 0o644); err != nil {
				return err
			}
		}

		files++
		flightCount += len(flights)
		bytes += len(encoded)
	}

	for _, month := range util.SortedMapKeys(droppedMonths) {
		year, m := parseMonthKey(month)
		fmt.Printf("%s: source data covers %d of %d days; left %s flights out\n",
			month, len(imp.daysPresent[month]), daysInMonth(year, m), commas(int64(droppedMonths[month])))
	}

	verb := "Wrote"
	if dryRun {
		verb = "Would write"
	}
	fmt.Printf("%s %s files holding %s flights, %.1f MB (%.2f bits/flight)\n", verb,
		commas(int64(files)), commas(int64(flightCount)), float64(bytes)/(1024*1024),
		8*float64(bytes)/float64(max(flightCount, 1)))

	return nil
}

// flightDataFilename returns the name of a facility's flight data file.
func flightDataFilename(facility string) string {
	return facility + av.FlightDataExtension
}

// coveredMonths returns the months the source data covers well enough to be
// worth writing out.
func coveredMonths(daysPresent map[string]map[string]bool, minCoverage float64) map[string]bool {
	covered := make(map[string]bool, len(daysPresent))
	for month := range daysPresent {
		covered[month] = monthCoverage(daysPresent, month) >= minCoverage
	}
	return covered
}

// monthCoverage returns the fraction of a month's days that the source data
// covers. The quarterly source files overlap at their boundaries, so a file can
// hold a few days of a month it otherwise knows nothing about.
func monthCoverage(daysPresent map[string]map[string]bool, month string) float64 {
	year, m := parseMonthKey(month)
	return float64(len(daysPresent[month])) / float64(daysInMonth(year, m))
}

// dropSparseFlights removes the flights that fall in months the source data
// barely covers, so that a file isn't replaced by one holding a couple of
// stray days either side of a quarter boundary.
func dropSparseFlights(flights []av.Flight, covered map[string]bool) (kept []av.Flight,
	dropped map[string]int) {
	dropped = make(map[string]int)
	kept = flights[:0]
	for _, f := range flights {
		if month := monthKey(f.Date()); covered[month] {
			kept = append(kept, f)
		} else {
			dropped[month]++
		}
	}
	return kept, dropped
}

func monthKey(t time.Time) string {
	return t.Format("2006-01")
}

func parseMonthKey(key string) (int, time.Month) {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return 0, 0
	}
	return t.Year(), t.Month()
}

// daysInMonth returns the number of days in the given month.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

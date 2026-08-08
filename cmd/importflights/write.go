// cmd/importflights/write.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Turning what was imported into the flight data files: gathering each cell's
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

// writeFlightData writes one file per grid cell, holding the departures and
// arrivals at every airport in it over however many years the source data
// spans, plus the metadata the cells share. Whatever was in the directory
// before is replaced: a run imports everything, so a file it didn't write is a
// file with no business being there.
func writeFlightData(dir string, imp *importer, minCoverage float64, dryRun bool) error {
	covered := coveredMonths(imp.daysPresent, minCoverage)
	droppedMonths := make(map[string]int)
	files, flightCount, bytes := 0, 0, 0
	var intervals []util.TimeInterval
	written := make(map[string]bool)

	if !dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	for _, cell := range cells(imp.buckets) {
		var flights []av.Flight
		for _, departure := range []bool{true, false} {
			key := bucket{cell: cell, departure: departure}
			for _, r := range imp.buckets[key] {
				flights = append(flights, av.Flight{
					Airport:      imp.symbols.string(r.airport),
					Callsign:     imp.symbols.string(r.callsign),
					Other:        imp.symbols.string(r.other),
					AircraftType: imp.symbols.string(r.acType),
					Day:          r.day,
					Minute:       int(r.minute),
					Departure:    departure,
				})
			}
			// The records are of no further use, and there are enough of them
			// that it is worth letting them go as we work through the cells.
			delete(imp.buckets, key)
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
		intervals = append(intervals, av.FlightIntervals(flights)...)

		encoded, err := av.EncodeFlights(flights)
		if err != nil {
			return fmt.Errorf("%s: %w", cell, err)
		}

		name := flightDataFilename(cell)
		if !dryRun {
			if err := os.WriteFile(filepath.Join(dir, name), encoded, 0o644); err != nil {
				return err
			}
		}
		written[name] = true

		files++
		flightCount += len(flights)
		bytes += len(encoded)
	}

	intervals = av.MergeFlightIntervals(intervals)
	if !dryRun {
		if err := writeMetadata(dir, intervals); err != nil {
			return err
		}
		if err := removeStaleFiles(dir, written); err != nil {
			return err
		}
	}

	for _, month := range util.SortedMapKeys(droppedMonths) {
		year, m := parseMonthKey(month)
		fmt.Printf("%s: source data covers %d of %d days; left %s flights out\n",
			month, len(imp.daysPresent[month]), daysInMonth(year, m), commas(int64(droppedMonths[month])))
	}
	reportFlightGaps(intervals)

	verb := "Wrote"
	if dryRun {
		verb = "Would write"
	}
	fmt.Printf("%s %s files holding %s flights, %.1f MB (%.2f bits/flight)\n", verb,
		commas(int64(files)), commas(int64(flightCount)), float64(bytes)/(1024*1024),
		8*float64(bytes)/float64(max(flightCount, 1)))

	return nil
}

// cells returns the cells that have flights, in order.
func cells(buckets map[bucket][]record) []string {
	names := make(map[string]bool)
	for key := range buckets {
		names[key.cell] = true
	}
	return util.SortedMapKeys(names)
}

// writeMetadata records what the cells have in common.
func writeMetadata(dir string, intervals []util.TimeInterval) error {
	encoded, err := av.EncodeFlightDataMetadata(intervals)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, av.FlightDataMetadataName), append(encoded, '\n'), 0o644)
}

// removeStaleFiles deletes the flight data files this run didn't write, which
// is how the files of a previous import that named them differently go away.
func removeStaleFiles(dir string, written map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != av.FlightDataExtension || written[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
		fmt.Printf("%s: removed, this import has no flights for it\n", name)
	}
	return nil
}

// reportFlightGaps names the stretches the data has no flights at all for. They
// are stretches a sim can't be started in, so they are worth seeing: they are
// the source data having gone down, and only the person running the import can
// say whether that is expected.
func reportFlightGaps(intervals []util.TimeInterval) {
	for i := 1; i < len(intervals); i++ {
		from, to := intervals[i-1].End(), intervals[i].Start()
		fmt.Printf("No flights anywhere from %s to %s (%.0f hours)\n",
			from.Format("2006-01-02 15:04Z"), to.Format("2006-01-02 15:04Z"),
			to.Sub(from).Hours())
	}
}

// flightDataFilename returns the name of a cell's flight data file.
func flightDataFilename(cell string) string {
	return cell + av.FlightDataExtension
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

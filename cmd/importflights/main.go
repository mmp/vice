// cmd/importflights/main.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Imports historical flight data into the flight data files under
// resources/traffic/flights. Input is one or more parquet files from the
// MrAirspace/aircraft-flight-schedules dataset, which is derived from ADS-B
// position reports and published quarterly:
//
//	https://github.com/MrAirspace/aircraft-flight-schedules
//
// Every flight that departs from or arrives at an airport the FAA controls is
// kept, wherever it is. What any scenario happens to fly is none of the
// importer's business, so adding an airport to a scenario, or writing a new
// facility, needs no reimport to have traffic for it.
//
// One file is written per cell of a grid of latitude and longitude, e.g.
// resources/traffic/flights/N40W074.flt, holding the flights at every airport
// in that cell, plus a metadata.json recording the stretches of time the data
// covers. Each flight records the airport it departs from or arrives at, its
// callsign, the airport at the other end, the UTC date and time, and the
// aircraft type; see aviation/flights.go for how that is stored. Pass every
// source file in one go, since each run rewrites the directory from scratch.
//
// The FAA Academy scenarios fly made-up airports that the source data knows
// nothing about; each one borrows a real airport's traffic, per
// av.FlightDataSubstitutes.
//
// Must be run from the top of a Vice checkout so that the resources are found.
//
// Usage:
//
//	importflights [-out dir] [-mincoverage f] [-dryrun] <flights.parquet>...

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	av "github.com/mmp/vice/aviation"
)

func main() {
	out := flag.String("out", "resources/traffic/flights", "`directory` to write flight data files to")
	minCoverage := flag.Float64("mincoverage", 0.9,
		"`fraction` of a month's days the input must cover for its flights to be written")
	dryRun := flag.Bool("dryrun", false, "report what would be written without writing it")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Printf("usage: importflights [options] <flights.parquet>...\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	av.InitDB()

	imp, err := makeImporter(av.DB.Airports, av.DB.AircraftPerformance, av.DB.Airlines)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	for _, path := range flag.Args() {
		if err := readFlights(path, imp); err != nil {
			fmt.Printf("%v\n", err)
			os.Exit(1)
		}
	}

	imp.report()

	if err := writeFlightData(*out, imp, *minCoverage, *dryRun); err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
}

// commas formats a number with thousands separators.
func commas(v int64) string {
	s := fmt.Sprintf("%d", v)
	var b strings.Builder
	if strings.HasPrefix(s, "-") {
		b.WriteByte('-')
		s = s[1:]
	}
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

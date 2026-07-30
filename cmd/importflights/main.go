// cmd/importflights/main.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Imports real-world flight data into the per-airport schedule files under
// resources/schedules. Input is one or more parquet files from the
// MrAirspace/aircraft-flight-schedules dataset, which is derived from ADS-B
// position reports and published quarterly:
//
//	https://github.com/MrAirspace/aircraft-flight-schedules
//
// One file is written per facility, e.g. resources/schedules/N90.flt, holding
// the departures and arrivals at every airport that facility generates IFR
// traffic at, over however many years the source files span. Facilities are
// named as the scenarios name them, so a sim running at N90 or ZBW reads the
// file of the same name. A file is replaced when there are flights for its
// facility and is otherwise left alone; pass every source file in one go, since
// each run rewrites a facility's schedule from scratch.
//
// Each flight records the airport it departs from or arrives at, its callsign,
// the airport at the other end, the UTC date and time, and the aircraft type.
// See aviation/flights.go for how that is stored.
//
// Must be run from the top of a Vice checkout so that the resources are found.
//
// Usage:
//
//	importflights [-out dir] [-airports KMSP,KORD] [-mincoverage f] [-dryrun] <flights.parquet>...

package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
)

func main() {
	out := flag.String("out", "resources/schedules", "`directory` to write schedule files to")
	airports := flag.String("airports", "", "comma-separated `airports` to import, if not all of them")
	minCoverage := flag.Float64("mincoverage", 0.9,
		"`fraction` of a month's days the input must cover for its files to be written")
	dryRun := flag.Bool("dryrun", false, "report what would be written without writing it")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Printf("usage: importflights [options] <flights.parquet>...\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	av.InitDB()

	var e util.ErrorLogger
	lg := log.New(false, "warn", "")
	facilities := gatherFacilities(&e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}

	if *airports != "" {
		restrict(facilities, strings.Split(strings.ToUpper(*airports), ","))
	}

	// A flight is worth keeping if some facility works its airport.
	departureAirports, arrivalAirports := make(map[string]bool), make(map[string]bool)
	for _, f := range facilities {
		maps.Copy(departureAirports, f.departures)
		maps.Copy(arrivalAirports, f.arrivals)
	}

	fmt.Printf("Importing %d departure and %d arrival airports across %d facilities\n",
		len(departureAirports), len(arrivalAirports), len(facilities))

	imp := makeImporter(departureAirports, arrivalAirports, av.DB.AircraftPerformance, av.DB.Airlines)
	for _, path := range flag.Args() {
		if err := readFlights(path, imp); err != nil {
			fmt.Printf("%v\n", err)
			os.Exit(1)
		}
	}

	imp.report()

	if err := writeSchedules(*out, imp, facilities, *minCoverage, *dryRun); err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
}

///////////////////////////////////////////////////////////////////////////

// facility is the set of airports one facility generates IFR traffic at, and
// gets one schedule file. A facility is a TRACON, or an ARTCC for the scenarios
// that are run out of one; it is the same name the scenarios are grouped under,
// so a sim can find the flights it needs from the facility it is running.
//
// An airport that more than one facility works appears in each of their files:
// ZBW needs KJFK arrivals just as much as N90 does, and each file standing on
// its own is what lets a sim load one file and be done.
type facility struct {
	departures map[string]bool
	arrivals   map[string]bool
}

func makeFacility() *facility {
	return &facility{departures: make(map[string]bool), arrivals: make(map[string]bool)}
}

func (f *facility) airports() map[string]bool {
	all := make(map[string]bool, len(f.departures)+len(f.arrivals))
	maps.Copy(all, f.departures)
	maps.Copy(all, f.arrivals)
	return all
}

// gatherFacilities returns the airports each facility works, taken from the
// scenarios it runs. The scenario group type isn't exported, so the scenarios
// are loaded and picked over here rather than passed around. Airports that
// aren't identified by an ICAO code can't appear in the source data, so they
// are left out.
func gatherFacilities(e *util.ErrorLogger, lg *log.Logger) map[string]*facility {
	scenarioGroups, _, _, _, _ := server.LoadScenarioGroups("", "", "", e, lg)
	if e.HaveErrors() {
		return nil
	}

	facilities := make(map[string]*facility)
	for name, groups := range scenarioGroups {
		f := makeFacility()
		for _, sg := range groups {
			for _, sc := range sg.Scenarios {
				for _, runway := range sc.DepartureRunways {
					if len(runway.Airport) == 4 {
						f.departures[runway.Airport] = true
					}
				}
				for _, rates := range sc.InboundFlowDefaultRates {
					for airport := range rates {
						if airport != "overflights" && len(airport) == 4 {
							f.arrivals[airport] = true
						}
					}
				}
			}
		}
		if len(f.departures)+len(f.arrivals) > 0 {
			facilities[name] = f
		}
	}
	return facilities
}

// restrict drops all but the given airports, and then any facility left with
// nothing to import.
func restrict(facilities map[string]*facility, keep []string) {
	keeping := make(map[string]bool)
	for _, airport := range keep {
		keeping[strings.TrimSpace(airport)] = true
	}

	for name, f := range facilities {
		for _, airports := range []map[string]bool{f.departures, f.arrivals} {
			for airport := range airports {
				if !keeping[airport] {
					delete(airports, airport)
				}
			}
		}
		if len(f.departures)+len(f.arrivals) == 0 {
			delete(facilities, name)
		}
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

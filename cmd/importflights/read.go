// cmd/importflights/read.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Reading the source parquet files: decoding the columns we need, working out
// where each flight actually flew between, and accumulating the ones our
// scenarios can use.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/parquet-go/parquet-go"
)

// flightRow is the subset of the source data's columns that we need. Declaring
// only these means the reader never decodes the position and altitude columns.
type flightRow struct {
	Callsign            string `parquet:"Callsign"`
	AircraftType        string `parquet:"AC_Type"`
	OriginTime          string `parquet:"Track_Origin_DateTime_UTC"`
	OriginAirports      string `parquet:"Track_Origin_ApplicableAirports"`
	DestinationTime     string `parquet:"Track_Destination_DateTime_UTC"`
	DestinationAirports string `parquet:"Track_Destination_ApplicableAirports"`
	Route               string `parquet:"Route_Validation_Based_on_Callsign"`
}

// noCallsign is what the source data records for a flight whose callsign was
// never received.
const noCallsign = "NO_CALLSIGN"

const (
	readBatchSize = 8192

	// progressInterval is how many rows go by between progress reports.
	progressInterval = 1_000_000
)

// readFlights reads every flight in a source file into the importer.
func readFlights(path string, imp *importer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	total := pf.NumRows()
	fmt.Printf("Reading %s (%s rows)\n", path, commas(total))

	reader := parquet.NewGenericReader[flightRow](pf)
	defer reader.Close()

	start := time.Now()
	batch := make([]flightRow, readBatchSize)
	var read int64
	for {
		n, err := reader.Read(batch)
		for i := range n {
			imp.processRow(&batch[i])
		}

		previous := read
		read += int64(n)
		if previous/progressInterval != read/progressInterval {
			fmt.Printf("  %s rows (%.0f%%), %s\n", commas(read), 100*float64(read)/float64(total),
				time.Since(start).Round(time.Second))
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}

	fmt.Printf("  %s rows in %s\n", commas(read), time.Since(start).Round(time.Second))

	return nil
}

///////////////////////////////////////////////////////////////////////////

// symbols interns the callsigns, airports, and aircraft types that appear in
// the source data so that records can refer to them by index. A quarter of
// flights holds millions of records but only a few hundred thousand distinct
// strings.
type symbols struct {
	ids     map[string]uint32
	strings []string
}

func makeSymbols() *symbols {
	return &symbols{ids: make(map[string]uint32)}
}

func (s *symbols) id(value string) uint32 {
	if id, ok := s.ids[value]; ok {
		return id
	}
	// The strings handed to us are backed by the decoder's buffers, which are
	// reused for the next batch of rows, so keep a copy.
	value = strings.Clone(value)
	id := uint32(len(s.strings))
	s.strings = append(s.strings, value)
	s.ids[value] = id
	return id
}

func (s *symbols) string(id uint32) string {
	return s.strings[id]
}

// record is one flight in one flight data file. It is kept small and comparable:
// there are millions of them and duplicates are removed by sorting.
type record struct {
	callsign uint32
	other    uint32
	acType   uint32
	minute   uint16 // minutes after UTC midnight
	day      uint16 // UTC date, in days from 1970-01-01
}

// bucket collects the flights that go into one half of one airport's flights.
// Departures and arrivals are gathered separately and merged when the file is
// written, which keeps record small.
type bucket struct {
	airport   string
	departure bool
}

// importer accumulates the flights read from the source files.
type importer struct {
	departureAirports map[string]bool
	arrivalAirports   map[string]bool
	airports          map[string]av.FAAAirport
	performance       map[string]av.AircraftPerformance
	airlines          map[string]av.Airline

	symbols *symbols
	buckets map[bucket][]record

	// daysPresent records the source days seen for each "2006-01" month, which
	// is how partially covered months are recognized.
	daysPresent map[string]map[string]bool

	rowsRead            int64
	recordsEmitted      int64
	unresolvedEndpoints int64
	sameEndpoints       int64
	notTargetAirport    int64
	badCallsign         int64
	badTimestamp        int64
	unknownOtherAirport int64
	missingAircraftType int64

	// unknownTypes counts the flights seen for each aircraft type that isn't in
	// the aircraft database. Those flights are still imported, but a sim skips
	// them until the type is added to openscope-aircraft.json.
	unknownTypes map[string]int64

	// unknownAirlines counts the flights seen for each callsign prefix that
	// isn't in the airline database. Those flights are still imported, but a
	// sim skips them until the airline is added to openscope-airlines.json.
	unknownAirlines map[string]int64
}

func makeImporter(departureAirports, arrivalAirports map[string]bool, airports map[string]av.FAAAirport,
	performance map[string]av.AircraftPerformance, airlines map[string]av.Airline) *importer {
	return &importer{
		departureAirports: departureAirports,
		arrivalAirports:   arrivalAirports,
		airports:          airports,
		performance:       performance,
		airlines:          airlines,
		symbols:           makeSymbols(),
		buckets:           make(map[bucket][]record),
		daysPresent:       make(map[string]map[string]bool),
		unknownTypes:      make(map[string]int64),
		unknownAirlines:   make(map[string]int64),
	}
}

// processRow turns one row of source data into up to two records: one for the
// airport it departed from and one for the airport it arrived at, when those
// are airports our scenarios use.
func (imp *importer) processRow(row *flightRow) {
	imp.rowsRead++
	imp.noteDay(row.OriginTime)

	origin, destination, ok := resolveEndpoints(parseAirportList(row.OriginAirports),
		parseAirportList(row.DestinationAirports), parseRoute(row.Route))
	if !ok {
		imp.unresolvedEndpoints++
		return
	}
	if origin == destination {
		imp.sameEndpoints++
		return
	}

	departure := imp.departureAirports[origin]
	arrival := imp.arrivalAirports[destination]
	if !departure && !arrival {
		imp.notTargetAirport++
		return
	}

	// Every real callsign starts with an operator's letters or a registration
	// prefix; the source data has some that are bare numbers.
	callsign := strings.ToUpper(strings.TrimSpace(row.Callsign))
	if callsign == "" || callsign == "-" || callsign == noCallsign ||
		callsign[0] < 'A' || callsign[0] > 'Z' {
		imp.badCallsign++
		return
	}

	base, _ := av.SplitCallsign(callsign)
	if _, ok := imp.airlines[base]; !ok {
		imp.unknownAirlines[base]++
	}

	// A type Vice doesn't know is imported all the same and counted so that the
	// report says what to add to openscope-aircraft.json; a row with no type at
	// all says nothing to import.
	aircraftType := strings.ToUpper(strings.TrimSpace(row.AircraftType))
	if aircraftType == "" || aircraftType == "-" {
		imp.missingAircraftType++
		return
	}
	if _, ok := imp.performance[aircraftType]; !ok {
		imp.unknownTypes[aircraftType]++
	}

	if departure {
		if _, ok := imp.airports[destination]; !ok {
			imp.unknownOtherAirport++
		} else {
			imp.add(origin, destination, callsign, aircraftType, row.OriginTime, true)
		}
	}
	if arrival {
		if _, ok := imp.airports[origin]; !ok {
			imp.unknownOtherAirport++
		} else {
			imp.add(destination, origin, callsign, aircraftType, row.DestinationTime, false)
		}
	}
}

// noteDay records that the source data covers the day a flight departed on.
func (imp *importer) noteDay(timestamp string) {
	if len(timestamp) < 10 {
		return
	}
	day := timestamp[:10] // YYYY-MM-DD
	month := day[:7]
	days, ok := imp.daysPresent[month]
	if !ok {
		days = make(map[string]bool)
		imp.daysPresent[month] = days
	}
	days[day] = true
}

// add files one flight under the airport whose flights it belongs to. Times
// are recorded in UTC, as the source data gives them; the seconds are dropped.
func (imp *importer) add(airport, other, callsign, aircraftType, timestamp string, departure bool) {
	utc, ok := parseTime(timestamp)
	if !ok {
		imp.badTimestamp++
		return
	}

	key := bucket{airport: airport, departure: departure}
	imp.buckets[key] = append(imp.buckets[key], record{
		callsign: imp.symbols.id(callsign),
		other:    imp.symbols.id(other),
		acType:   imp.symbols.id(aircraftType),
		minute:   uint16(utc.Hour()*60 + utc.Minute()),
		day:      av.FlightDataDayNumber(utc),
	})
	imp.recordsEmitted++
}

// report summarizes what was read and what had to be thrown away, plus what
// was imported that Vice doesn't yet know how to fly.
func (imp *importer) report() {
	fmt.Printf("\nRead %s flights, kept %s\n", commas(imp.rowsRead), commas(imp.recordsEmitted))
	fmt.Printf("Skipped:\n")
	for _, s := range []struct {
		what string
		n    int64
	}{
		{"not an airport we simulate", imp.notTargetAirport},
		{"couldn't tell where it flew between", imp.unresolvedEndpoints},
		{"no aircraft type", imp.missingAircraftType},
		{"departed and arrived at the same airport", imp.sameEndpoints},
		{"other airport not in the database", imp.unknownOtherAirport},
		{"unusable callsign", imp.badCallsign},
		{"unusable timestamp", imp.badTimestamp},
	} {
		if s.n > 0 {
			fmt.Printf("  %12s  %s\n", commas(s.n), s.what)
		}
	}

	if len(imp.unknownTypes) > 0 {
		fmt.Printf("\n%d aircraft types not in openscope-aircraft.json "+
			"(their flights are imported but sims skip them):\n", len(imp.unknownTypes))
		printCounts(imp.unknownTypes)
	}

	if len(imp.unknownAirlines) > 0 {
		fmt.Printf("\n%d callsign prefixes not in openscope-airlines.json "+
			"(their flights are imported but sims skip them):\n", len(imp.unknownAirlines))
		printCounts(imp.unknownAirlines)
	}

	fmt.Printf("\n")
}

// printCounts lists the keys of counts from most to least common, showing the
// most common few and summarizing the rest.
func printCounts(counts map[string]int64) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})

	const show = 100
	for _, k := range keys[:min(show, len(keys))] {
		fmt.Printf("  %-8s %s\n", k, commas(counts[k]))
	}
	if len(keys) > show {
		var rest int64
		for _, k := range keys[show:] {
			rest += counts[k]
		}
		fmt.Printf("  and %d more accounting for %s flights\n", len(keys)-show, commas(rest))
	}
}

///////////////////////////////////////////////////////////////////////////

// parseAirportList parses the airport lists in the source data, which are
// formatted as Python lists: "['KMSP']" or "['KACT', 'KAUS']". A track that
// started or ended too far from any airport has "-" instead.
func parseAirportList(value string) []string {
	var airports []string
	var current strings.Builder

	flush := func() {
		if current.Len() == 4 {
			airports = append(airports, current.String())
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

// parseRoute parses the route the source data derived from the flight's
// callsign, e.g. "KMSP-KSTL" or the multi-leg "KMSP-KAUS-KMSP". It is "-" when
// the callsign wasn't found.
func parseRoute(value string) []string {
	var route []string
	for _, airport := range strings.Split(value, "-") {
		if len(airport) == 4 {
			route = append(route, airport)
		}
	}
	return route
}

// resolveEndpoints determines where a flight departed from and arrived at.
// Tracks that were first or last seen away from an airport give a list of
// candidates rather than a single airport; in that case the route derived from
// the callsign picks out the leg that was flown. Flights that remain ambiguous
// are not usable.
func resolveEndpoints(origins, destinations, route []string) (origin, destination string, ok bool) {
	if len(origins) == 1 && len(destinations) == 1 {
		return origins[0], destinations[0], true
	}

	// Consider each leg of the route in turn, keeping those consistent with
	// the airports the track itself suggests.
	for i := 0; i+1 < len(route); i++ {
		from, to := route[i], route[i+1]
		if len(origins) > 0 && !slices.Contains(origins, from) {
			continue
		}
		if len(destinations) > 0 && !slices.Contains(destinations, to) {
			continue
		}
		if ok && (from != origin || to != destination) {
			// More than one leg of the route fits.
			return "", "", false
		}
		origin, destination, ok = from, to, true
	}

	return origin, destination, ok
}

// timeLayout is the format of the timestamps in the source data. They are UTC.
const timeLayout = "2006-01-02 15:04:05"

// parseTime parses a source data timestamp.
func parseTime(value string) (time.Time, bool) {
	t, err := time.ParseInLocation(timeLayout, value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

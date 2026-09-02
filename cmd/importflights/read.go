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

// flightRow is the subset of the source data's columns that we need. Every
// column in the file is a string, the positions and altitudes included: the
// source data has no null values and writes "-" or "nan" for what it lacks.
type flightRow struct {
	Callsign             string `parquet:"Callsign"`
	AircraftType         string `parquet:"AC_Type"`
	OriginTime           string `parquet:"Track_Origin_DateTime_UTC"`
	OriginAirports       string `parquet:"Track_Origin_ApplicableAirports"`
	OriginLatitude       string `parquet:"Track_Origin_Lat"`
	OriginLongitude      string `parquet:"Track_Origin_Lon"`
	OriginAltitude       string `parquet:"Track_Origin_FL_Ft"`
	DestinationTime      string `parquet:"Track_Destination_DateTime_UTC"`
	DestinationAirports  string `parquet:"Track_Destination_ApplicableAirports"`
	DestinationLatitude  string `parquet:"Track_Destination_Lat"`
	DestinationLongitude string `parquet:"Track_Destination_Lon"`
	DestinationAltitude  string `parquet:"Track_Destination_FL_Ft"`
	Route                string `parquet:"Route_Validation_Based_on_Callsign"`
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
// there are tens of millions of them and duplicates are removed by sorting.
type record struct {
	airport  uint32
	callsign uint32
	other    uint32
	acType   uint32
	minute   uint16 // minutes after UTC midnight
	day      uint16 // UTC date, in days from 1970-01-01
}

// bucket collects the flights that go into one half of one cell's file.
// Departures and arrivals are gathered separately and merged when the file is
// written, which keeps record small.
type bucket struct {
	cell      string
	departure bool
}

// airportCell is where an airport's flights go, and whether it has flights of
// its own at all: only the airports the FAA controls do.
type airportCell struct {
	cell string
	keep bool
}

// substitute is a made-up airport standing on a real one's traffic, and the
// cell its flights are filed under.
type substitute struct {
	airport string
	cell    string
}

// importer accumulates the flights read from the source files.
type importer struct {
	airports    map[string]av.FAAAirport
	performance map[string]av.AircraftPerformance
	airlines    map[string]av.Airline

	// cells records where each airport seen so far files its flights, since
	// working that out over and over for tens of millions of rows is waste.
	cells map[string]airportCell

	// donors maps each real airport to the made-up one that borrows its traffic.
	donors map[string]substitute

	symbols *symbols
	buckets map[bucket][]record

	// daysPresent records the source days seen for each "2006-01" month, which
	// is how partially covered months are recognized.
	daysPresent map[string]map[string]bool

	// calibration, when the -calibrate flag is given, measures how well a
	// track point picks an airport out of a list of candidates instead of
	// importing anything.
	calibration *calibration

	rowsRead            int64
	recordsEmitted      int64
	noEndpoints         int64
	notAtItsAirport     int64
	noFarEndpoint       int64
	sameEndpoints       int64
	notFAAAirport       int64
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

	// repairedTypes counts the flights repairSuspectTypes took each aircraft
	// type away from.
	repairedTypes map[string]int64
}

func makeImporter(airports map[string]av.FAAAirport, performance map[string]av.AircraftPerformance,
	airlines map[string]av.Airline) (*importer, error) {
	donors := make(map[string]substitute, len(av.FlightDataSubstitutes))
	for fictional, donor := range av.FlightDataSubstitutes {
		ap, ok := airports[fictional]
		if !ok {
			return nil, fmt.Errorf("%s: made-up airport isn't in custom_airports.json, "+
				"so there is nowhere to file the traffic it borrows from %s", fictional, donor)
		}
		donors[donor] = substitute{airport: fictional, cell: av.FlightDataCell(ap.Location)}
	}

	return &importer{
		airports:        airports,
		performance:     performance,
		airlines:        airlines,
		cells:           make(map[string]airportCell),
		donors:          donors,
		symbols:         makeSymbols(),
		buckets:         make(map[bucket][]record),
		daysPresent:     make(map[string]map[string]bool),
		unknownTypes:    make(map[string]int64),
		unknownAirlines: make(map[string]int64),
		repairedTypes:   make(map[string]int64),
	}, nil
}

// cellFor returns the cell an airport's flights are filed under, and whether it
// has flights of its own at all: an airport the FAA doesn't control is only
// ever the far end of somebody else's flight.
func (imp *importer) cellFor(icao string) (string, bool) {
	if c, ok := imp.cells[icao]; ok {
		return c.cell, c.keep
	}

	var c airportCell
	if ap, ok := imp.airports[icao]; ok && ap.FAAControlled() {
		c = airportCell{cell: av.FlightDataCell(ap.Location), keep: true}
	}
	imp.cells[strings.Clone(icao)] = c
	return c.cell, c.keep
}

// processRow turns one row of source data into up to two records: one for the
// airport it departed from and one for the airport it arrived at, for whichever
// of those the FAA controls.
func (imp *importer) processRow(row *flightRow) {
	imp.rowsRead++
	imp.noteDay(row.OriginTime)

	origin := parseTrackEnd(row.OriginAirports, row.OriginLatitude, row.OriginLongitude,
		row.OriginAltitude)
	destination := parseTrackEnd(row.DestinationAirports, row.DestinationLatitude,
		row.DestinationLongitude, row.DestinationAltitude)
	route := parseRoute(row.Route)

	if imp.calibration != nil {
		imp.calibration.observe(origin, destination, route, imp.airports)
		return
	}

	from, to := resolveEndpoints(origin, destination, route, imp.airports)
	if !from.known() && !to.known() {
		imp.noEndpoints++
		return
	}
	if from.airport == to.airport {
		imp.sameEndpoints++
		return
	}

	originCell, atOrigin := imp.cellFor(from.airport)
	destinationCell, atDestination := imp.cellFor(to.airport)
	if !atOrigin && !atDestination {
		imp.notFAAAirport++
		return
	}

	// A record is filed at an airport only if the track says the aircraft was
	// really there. The airport at the other end only has to be in the right
	// direction, since all a sim does with it is pick a departure gate or the
	// flow an arrival comes in on, so a flight whose far end is merely the
	// nearest of several candidates is still worth having.
	departure := atOrigin && from.atAirport && to.known()
	arrival := atDestination && to.atAirport && from.known()
	if atOrigin && !departure {
		imp.countUnfiled(from.atAirport)
	}
	if atDestination && !arrival {
		imp.countUnfiled(to.atAirport)
	}
	if !departure && !arrival {
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
		if _, ok := imp.airports[to.airport]; !ok {
			imp.unknownOtherAirport++
		} else {
			imp.add(originCell, from.airport, to.airport, callsign, aircraftType,
				row.OriginTime, true)
		}
	}
	if arrival {
		if _, ok := imp.airports[from.airport]; !ok {
			imp.unknownOtherAirport++
		} else {
			imp.add(destinationCell, to.airport, from.airport, callsign, aircraftType,
				row.DestinationTime, false)
		}
	}
}

// countUnfiled records why a record that would have been filed at one of our
// airports wasn't.
func (imp *importer) countUnfiled(atAirport bool) {
	if atAirport {
		imp.noFarEndpoint++
	} else {
		imp.notAtItsAirport++
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

// add files one flight under the cell whose file it belongs in. Times are
// recorded in UTC, as the source data gives them; the seconds are dropped.
func (imp *importer) add(cell, airport, other, callsign, aircraftType, timestamp string,
	departure bool) {
	utc, ok := parseTime(timestamp)
	if !ok {
		imp.badTimestamp++
		return
	}
	imp.file(cell, airport, other, callsign, aircraftType, utc, departure)

	// A made-up airport is filed alongside the real one it stands on, and the
	// far end is renamed too: the Academy scenarios route traffic between their
	// own airports, so a hop between two donors has to arrive as one between
	// their stand-ins.
	if sub, ok := imp.donors[airport]; ok {
		if far, ok := imp.donors[other]; ok {
			other = far.airport
		}
		imp.file(sub.cell, sub.airport, other, callsign, aircraftType, utc, departure)
	}
}

func (imp *importer) file(cell, airport, other, callsign, aircraftType string, utc time.Time,
	departure bool) {
	key := bucket{cell: cell, departure: departure}
	imp.buckets[key] = append(imp.buckets[key], record{
		airport:  imp.symbols.id(airport),
		callsign: imp.symbols.id(callsign),
		other:    imp.symbols.id(other),
		acType:   imp.symbols.id(aircraftType),
		minute:   uint16(utc.Hour()*60 + utc.Minute()),
		day:      av.FlightDataDayNumber(utc),
	})
	imp.recordsEmitted++
}

// repairSuspectTypes gives back the type a flight was really flown with when
// the source data's is one it can't have been. That data resolves a track's
// type from the aircraft's registration and gets it wrong for the occasional
// airframe, which is how an Air Canada A220 comes to be recorded as a Cessna
// 172 for a day: the whole of that day's rotation carries the wrong type.
//
// A callsign that flies a jet or a turboprop in the data's own telling doesn't
// also fly a piston single or a helicopter, so a record of one that is
// outnumbered by the type the callsign otherwise flies takes that type instead.
// The flight itself is real and is kept: only what it was flown with was wrong.
func (imp *importer) repairSuspectTypes() {
	counts := make(map[uint32]map[uint32]int)
	for _, records := range imp.buckets {
		for _, r := range records {
			byType, ok := counts[r.callsign]
			if !ok {
				byType = make(map[uint32]int)
				counts[r.callsign] = byType
			}
			byType[r.acType]++
		}
	}

	// Ties go to the type that sorts first so that two runs over the same
	// source data write the same files.
	predominant := make(map[uint32]uint32, len(counts))
	for callsign, byType := range counts {
		var best uint32
		bestCount := -1
		for acType, n := range byType {
			if n > bestCount || (n == bestCount && imp.symbols.string(acType) < imp.symbols.string(best)) {
				best, bestCount = acType, n
			}
		}
		if class := imp.engineClass(best); class == "J" || class == "T" {
			predominant[callsign] = best
		}
	}

	for _, records := range imp.buckets {
		for i := range records {
			r := &records[i]
			best, ok := predominant[r.callsign]
			if !ok || r.acType == best || counts[r.callsign][r.acType] >= counts[r.callsign][best] {
				continue
			}
			// A callsign whose prefix isn't an airline's is one no sim flies,
			// so there is nothing to gain by second-guessing its type.
			base, _ := av.SplitCallsign(imp.symbols.string(r.callsign))
			if _, ok := imp.airlines[base]; !ok {
				continue
			}
			// A type Vice doesn't know is as suspect as a piston or a
			// helicopter: nothing an airline flies is missing from the aircraft
			// database.
			if class := imp.engineClass(r.acType); class != "" && class != "P" && class != "H" {
				continue
			}
			imp.repairedTypes[imp.symbols.string(r.acType)]++
			r.acType = best
		}
	}
}

// engineClass is how an aircraft type is powered: "P" for piston, "T" for
// turboprop, "J" for jet, and "H" for rotorcraft. It is empty for a type that
// isn't in the aircraft database.
func (imp *importer) engineClass(acType uint32) string {
	if perf, ok := imp.performance[imp.symbols.string(acType)]; ok {
		return perf.Engine.AircraftType
	}
	return ""
}

// skipped is one line of the import report: how many of something were thrown
// away, and why.
type skipped struct {
	what string
	n    int64
}

// report summarizes what was read and what had to be thrown away, plus what
// was imported that Vice doesn't yet know how to fly. Flights and records are
// counted apart: a flight between two of our airports is two records, and
// either of them can be lost while the other is kept.
func (imp *importer) report() {
	fmt.Printf("\nRead %s flights, kept %s records\n", commas(imp.rowsRead),
		commas(imp.recordsEmitted))

	printSkipped("Flights skipped", []skipped{
		{"neither end an FAA airport", imp.notFAAAirport},
		{"couldn't place either end of the track", imp.noEndpoints},
		{"departed and arrived at the same airport", imp.sameEndpoints},
		{"no aircraft type", imp.missingAircraftType},
		{"unusable callsign", imp.badCallsign},
	})
	printSkipped("Records not filed", []skipped{
		{"the track wasn't at the airport it would be filed at", imp.notAtItsAirport},
		{"nothing said where the other end was", imp.noFarEndpoint},
		{"the airport at the other end isn't in the database", imp.unknownOtherAirport},
		{"unusable timestamp", imp.badTimestamp},
	})

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

	if len(imp.repairedTypes) > 0 {
		var repaired int64
		for _, n := range imp.repairedTypes {
			repaired += n
		}
		fmt.Printf("\nGave %s flights the type their callsign otherwise flies, "+
			"taking them away from:\n", commas(repaired))
		printCounts(imp.repairedTypes)
	}

	fmt.Printf("\n")
}

// printSkipped prints the lines of the report that have anything to say.
func printSkipped(title string, counts []skipped) {
	if !slices.ContainsFunc(counts, func(s skipped) bool { return s.n > 0 }) {
		return
	}
	fmt.Printf("%s:\n", title)
	for _, s := range counts {
		if s.n > 0 {
			fmt.Printf("  %12s  %s\n", commas(s.n), s.what)
		}
	}
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

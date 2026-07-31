// sim/timetable.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// TimetableFlight is one published flight in a timetable. It stores
// only facts from the timetable; runway, route, altitude, and spawn details are
// resolved later by the simulation.
type TimetableFlight struct {
	Callsign     string
	Origin       string
	Destination  string
	AircraftType string

	// PublishedMinute is expressed as minutes after local midnight at the
	// timetable airport. For departures it is the published pushback time; for
	// arrivals it is the published arrival/runway operation time.
	PublishedMinute int

	// Cargo flights are retained when the user reduces the traffic percentage.
	Cargo bool
}

// TimetableOperation describes how a timetable's flight relates to the
// timetable's airport.
type TimetableOperation int

const (
	TimetableOperationUnknown TimetableOperation = iota
	TimetableOperationArrival
	TimetableOperationDeparture
)

// OperationAt determines whether the flight is an arrival or departure at the
// supplied airport. A flight whose origin and destination both match (or
// neither matches) is not usable for that airport.
func (f TimetableFlight) OperationAt(airport string) TimetableOperation {
	airport = normalizeAirportCode(airport)
	originMatches := normalizeAirportCode(f.Origin) == airport
	destinationMatches := normalizeAirportCode(f.Destination) == airport

	switch {
	case originMatches && !destinationMatches:
		return TimetableOperationDeparture
	case destinationMatches && !originMatches:
		return TimetableOperationArrival
	default:
		return TimetableOperationUnknown
	}
}

// LoadTimetableCSV reads a timetable CSV. Required columns may appear
// in any order. Unknown columns are ignored so the format can grow without
// breaking older Vice versions.
func LoadTimetableCSV(r io.Reader) ([]TimetableFlight, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("timetable CSV is empty")
		}
		return nil, fmt.Errorf("read timetable CSV header: %w", err)
	}

	columns, err := timetableCSVColumns(header)
	if err != nil {
		return nil, err
	}

	var flights []TimetableFlight
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read timetable CSV line %d: %w", line, err)
		}
		if timetableCSVRecordEmpty(record) {
			continue
		}

		flight, err := parseTimetableFlight(record, columns)
		if err != nil {
			return nil, fmt.Errorf("timetable CSV line %d: %w", line, err)
		}
		flights = append(flights, flight)
	}

	if len(flights) == 0 {
		return nil, fmt.Errorf("timetable CSV contains no flights")
	}
	return flights, nil
}

type timetableCSVColumn int

const (
	timetableCSVCallsign timetableCSVColumn = iota
	timetableCSVOrigin
	timetableCSVDestination
	timetableCSVAircraftType
	timetableCSVTime
	timetableCSVCargo
)

var requiredTimetableCSVColumns = map[string]timetableCSVColumn{
	"callsign":      timetableCSVCallsign,
	"origin":        timetableCSVOrigin,
	"destination":   timetableCSVDestination,
	"aircraft_type": timetableCSVAircraftType,
	"time":          timetableCSVTime,
}

func timetableCSVColumns(header []string) (map[timetableCSVColumn]int, error) {
	columns := make(map[timetableCSVColumn]int)
	for i, name := range header {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
		name = strings.ToLower(name)

		column, required := requiredTimetableCSVColumns[name]
		if name == "cargo" {
			column = timetableCSVCargo
		} else if !required {
			continue
		}

		if _, exists := columns[column]; exists {
			return nil, fmt.Errorf("timetable CSV has duplicate %q column", name)
		}
		columns[column] = i
	}

	for name, column := range requiredTimetableCSVColumns {
		if _, ok := columns[column]; !ok {
			return nil, fmt.Errorf("timetable CSV is missing required %q column", name)
		}
	}
	return columns, nil
}

func parseTimetableFlight(record []string, columns map[timetableCSVColumn]int) (TimetableFlight, error) {
	value := func(column timetableCSVColumn) string {
		index, ok := columns[column]
		if !ok || index >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[index])
	}

	flight := TimetableFlight{
		Callsign:     strings.ToUpper(value(timetableCSVCallsign)),
		Origin:       normalizeAirportCode(value(timetableCSVOrigin)),
		Destination:  normalizeAirportCode(value(timetableCSVDestination)),
		AircraftType: strings.ToUpper(value(timetableCSVAircraftType)),
	}

	for name, field := range map[string]string{
		"callsign":      flight.Callsign,
		"origin":        flight.Origin,
		"destination":   flight.Destination,
		"aircraft_type": flight.AircraftType,
	} {
		if field == "" {
			return TimetableFlight{}, fmt.Errorf("%s is empty", name)
		}
	}

	minute, err := parseTimetableTime(value(timetableCSVTime))
	if err != nil {
		return TimetableFlight{}, err
	}
	flight.PublishedMinute = minute

	if rawCargo := value(timetableCSVCargo); rawCargo != "" {
		cargo, err := parseTimetableBool(rawCargo)
		if err != nil {
			return TimetableFlight{}, fmt.Errorf("cargo: %w", err)
		}
		flight.Cargo = cargo
	}

	return flight, nil
}

func parseTimetableTime(value string) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("time %q must use HH:MM in 24-hour local time", value)
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, fmt.Errorf("time %q has an invalid hour", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("time %q has an invalid minute", value)
	}
	return hour*60 + minute, nil
}

func parseTimetableBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1":
		return true, nil
	case "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%q must be true or false", value)
	}
}

func timetableCSVRecordEmpty(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

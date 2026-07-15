// sim/schedule.go
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

// ScheduledFlight is one published flight in a real-world schedule. It stores
// only facts from the schedule; runway, route, altitude, and spawn details are
// resolved later by the simulation.
type ScheduledFlight struct {
	Callsign     string
	Origin       string
	Destination  string
	AircraftType string

	// ScheduledMinute is the runway operation time expressed as minutes after
	// local midnight at the schedule airport.
	ScheduledMinute int

	// Cargo flights are retained when the user reduces the traffic percentage.
	Cargo bool
}

// ScheduleOperation describes how a scheduled flight relates to the schedule
// airport.
type ScheduleOperation int

const (
	ScheduleOperationUnknown ScheduleOperation = iota
	ScheduleOperationArrival
	ScheduleOperationDeparture
)

// OperationAt determines whether the flight is an arrival or departure at the
// supplied airport. A flight whose origin and destination both match (or
// neither matches) is not usable for that airport.
func (f ScheduledFlight) OperationAt(airport string) ScheduleOperation {
	airport = normalizeScheduleCode(airport)
	originMatches := normalizeScheduleCode(f.Origin) == airport
	destinationMatches := normalizeScheduleCode(f.Destination) == airport

	switch {
	case originMatches && !destinationMatches:
		return ScheduleOperationDeparture
	case destinationMatches && !originMatches:
		return ScheduleOperationArrival
	default:
		return ScheduleOperationUnknown
	}
}

func normalizeScheduledAircraftType(value string) string {
	aircraftType := strings.ToUpper(strings.TrimSpace(value))

	switch aircraftType {
	case "B717":
		return "B712"
	default:
		return aircraftType
	}
}

// LoadScheduleCSV reads a real-world schedule CSV. Required columns may appear
// in any order. Unknown columns are ignored so the format can grow without
// breaking older Vice versions.
func LoadScheduleCSV(r io.Reader) ([]ScheduledFlight, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("schedule CSV is empty")
		}
		return nil, fmt.Errorf("read schedule CSV header: %w", err)
	}

	columns, err := scheduleCSVColumns(header)
	if err != nil {
		return nil, err
	}

	var flights []ScheduledFlight
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read schedule CSV line %d: %w", line, err)
		}
		if scheduleCSVRecordEmpty(record) {
			continue
		}

		flight, err := parseScheduledFlight(record, columns)
		if err != nil {
			return nil, fmt.Errorf("schedule CSV line %d: %w", line, err)
		}
		flights = append(flights, flight)
	}

	if len(flights) == 0 {
		return nil, fmt.Errorf("schedule CSV contains no flights")
	}
	return flights, nil
}

type scheduleCSVColumn int

const (
	scheduleCSVCallsign scheduleCSVColumn = iota
	scheduleCSVOrigin
	scheduleCSVDestination
	scheduleCSVAircraftType
	scheduleCSVTime
	scheduleCSVCargo
)

var requiredScheduleCSVColumns = map[string]scheduleCSVColumn{
	"callsign":      scheduleCSVCallsign,
	"origin":        scheduleCSVOrigin,
	"destination":   scheduleCSVDestination,
	"aircraft_type": scheduleCSVAircraftType,
	"time":          scheduleCSVTime,
}

func scheduleCSVColumns(header []string) (map[scheduleCSVColumn]int, error) {
	columns := make(map[scheduleCSVColumn]int)
	for i, name := range header {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
		name = strings.ToLower(name)

		column, required := requiredScheduleCSVColumns[name]
		if name == "cargo" {
			column = scheduleCSVCargo
		} else if !required {
			continue
		}

		if _, exists := columns[column]; exists {
			return nil, fmt.Errorf("schedule CSV has duplicate %q column", name)
		}
		columns[column] = i
	}

	for name, column := range requiredScheduleCSVColumns {
		if _, ok := columns[column]; !ok {
			return nil, fmt.Errorf("schedule CSV is missing required %q column", name)
		}
	}
	return columns, nil
}

func parseScheduledFlight(record []string, columns map[scheduleCSVColumn]int) (ScheduledFlight, error) {
	value := func(column scheduleCSVColumn) string {
		index, ok := columns[column]
		if !ok || index >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[index])
	}

	flight := ScheduledFlight{
		Callsign:     strings.ToUpper(value(scheduleCSVCallsign)),
		Origin:       normalizeScheduleCode(value(scheduleCSVOrigin)),
		Destination:  normalizeScheduleCode(value(scheduleCSVDestination)),
		AircraftType: strings.ToUpper(value(scheduleCSVAircraftType)),
	}

	for name, field := range map[string]string{
		"callsign":      flight.Callsign,
		"origin":        flight.Origin,
		"destination":   flight.Destination,
		"aircraft_type": flight.AircraftType,
	} {
		if field == "" {
			return ScheduledFlight{}, fmt.Errorf("%s is empty", name)
		}
	}

	minute, err := parseScheduleTime(value(scheduleCSVTime))
	if err != nil {
		return ScheduledFlight{}, err
	}
	flight.ScheduledMinute = minute

	if rawCargo := value(scheduleCSVCargo); rawCargo != "" {
		cargo, err := parseScheduleBool(rawCargo)
		if err != nil {
			return ScheduledFlight{}, fmt.Errorf("cargo: %w", err)
		}
		flight.Cargo = cargo
	}

	return flight, nil
}

func parseScheduleTime(value string) (int, error) {
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

func parseScheduleBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1":
		return true, nil
	case "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%q must be true or false", value)
	}
}

func normalizeScheduleCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func scheduleCSVRecordEmpty(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

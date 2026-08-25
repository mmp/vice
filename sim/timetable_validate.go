// sim/timetable_validate.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"

	av "github.com/mmp/vice/aviation"
)

const (
	timetableDepartureActiveMinutes = 45
	timetableArrivalActiveMinutes   = 45
	minutesPerTimetableDay          = 24 * 60
)

type timetableCallsignUse struct {
	flight TimetableFlight
	row    int
	start  int
	end    int
}

func timetableFlightActiveWindow(
	flight TimetableFlight,
	airport string,
) (start int, end int) {
	switch flight.OperationAt(airport) {
	case TimetableOperationDeparture:
		return flight.PublishedMinute,
			flight.PublishedMinute + timetableDepartureActiveMinutes

	case TimetableOperationArrival:
		return flight.PublishedMinute - timetableArrivalActiveMinutes,
			flight.PublishedMinute

	default:
		return flight.PublishedMinute, flight.PublishedMinute
	}
}
func timetableWindowsOverlap(
	firstStart int,
	firstEnd int,
	secondStart int,
	secondEnd int,
) bool {
	return firstStart < secondEnd && secondStart < firstEnd
}

// validateTimetable checks timetable-wide rules that cannot be validated
// while parsing an individual CSV row.
func validateTimetable(timetable Timetable) error {
	seenRows := make(map[TimetableFlight]int)
	callsignUses := make(map[string][]timetableCallsignUse)

	for index, flight := range timetable.Flights {
		row := index + 2

		if flight.OperationAt(timetable.Airport) == TimetableOperationUnknown {
			return fmt.Errorf(
				"row %d callsign %s is neither an arrival nor departure at %s",
				row,
				flight.Callsign,
				timetable.Airport,
			)
		}

		if _, ok := av.DB.AircraftPerformance[flight.AircraftType]; !ok {
			return fmt.Errorf(
				"row %d callsign %s uses unknown aircraft type %s",
				row,
				flight.Callsign,
				flight.AircraftType,
			)
		}
		if err := av.CheckAirport("origin", flight.Origin); err != nil {
			return fmt.Errorf("row %d callsign %s: %w", row, flight.Callsign, err)
		}

		if err := av.CheckAirport("destination", flight.Destination); err != nil {
			return fmt.Errorf("row %d callsign %s: %w", row, flight.Callsign, err)
		}

		if previousRow, ok := seenRows[flight]; ok {
			return fmt.Errorf(
				"row %d exactly duplicates row %d for callsign %s",
				row,
				previousRow,
				flight.Callsign,
			)
		}
		seenRows[flight] = row
		start, end := timetableFlightActiveWindow(flight, timetable.Airport)
		callsignUses[flight.Callsign] = append(
			callsignUses[flight.Callsign],
			timetableCallsignUse{
				flight: flight,
				row:    row,
				start:  start,
				end:    end,
			},
		)
	}
	for callsign, uses := range callsignUses {
		for firstIndex := range uses {
			first := uses[firstIndex]

			for secondIndex := firstIndex + 1; secondIndex < len(uses); secondIndex++ {
				second := uses[secondIndex]

				overlaps := false
				for _, dayOffset := range []int{
					-minutesPerTimetableDay,
					0,
					minutesPerTimetableDay,
				} {
					if timetableWindowsOverlap(
						first.start,
						first.end,
						second.start+dayOffset,
						second.end+dayOffset,
					) {
						overlaps = true
						break
					}
				}

				if overlaps {
					return fmt.Errorf(
						"callsign %s has overlapping active windows on rows %d and %d",
						callsign,
						first.row,
						second.row,
					)
				}
			}
		}
	}
	return nil
}

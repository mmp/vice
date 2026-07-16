// sim/schedule_validate.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"

	av "github.com/mmp/vice/aviation"
)

const (
	scheduledDepartureActiveMinutes = 45
	scheduledArrivalActiveMinutes   = 45
	minutesPerScheduleDay           = 24 * 60
)

type scheduledCallsignUse struct {
	flight ScheduledFlight
	row    int
	start  int
	end    int
}

func scheduledFlightActiveWindow(
	flight ScheduledFlight,
	airport string,
) (start int, end int) {
	switch flight.OperationAt(airport) {
	case ScheduleOperationDeparture:
		return flight.ScheduledMinute,
			flight.ScheduledMinute + scheduledDepartureActiveMinutes

	case ScheduleOperationArrival:
		return flight.ScheduledMinute - scheduledArrivalActiveMinutes,
			flight.ScheduledMinute

	default:
		return flight.ScheduledMinute, flight.ScheduledMinute
	}
}
func scheduleWindowsOverlap(
	firstStart int,
	firstEnd int,
	secondStart int,
	secondEnd int,
) bool {
	return firstStart < secondEnd && secondStart < firstEnd
}

// validateBuiltInSchedule checks schedule-wide rules that cannot be validated
// while parsing an individual CSV row.
func validateBuiltInSchedule(schedule BuiltInSchedule) error {
	seenRows := make(map[ScheduledFlight]int)
	callsignUses := make(map[string][]scheduledCallsignUse)

	for index, flight := range schedule.Flights {
		row := index + 2

		if flight.OperationAt(schedule.Airport) == ScheduleOperationUnknown {
			return fmt.Errorf(
				"row %d callsign %s is neither an arrival nor departure at %s",
				row,
				flight.Callsign,
				schedule.Airport,
			)
		}

		aircraftType := normalizeScheduledAircraftType(flight.AircraftType)
		if _, ok := av.DB.AircraftPerformance[aircraftType]; !ok {
			return fmt.Errorf(
				"row %d callsign %s uses unknown aircraft type %s",
				row,
				flight.Callsign,
				aircraftType,
			)
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
		start, end := scheduledFlightActiveWindow(flight, schedule.Airport)
		callsignUses[flight.Callsign] = append(
			callsignUses[flight.Callsign],
			scheduledCallsignUse{
				flight: flight,
				row:    row,
				start:  start,
				end:    end,
			},
		)
	}
	for callsign, uses := range callsignUses {
		for firstIndex := 0; firstIndex < len(uses); firstIndex++ {
			first := uses[firstIndex]

			for secondIndex := firstIndex + 1; secondIndex < len(uses); secondIndex++ {
				second := uses[secondIndex]

				overlaps := false
				for _, dayOffset := range []int{
					-minutesPerScheduleDay,
					0,
					minutesPerScheduleDay,
				} {
					if scheduleWindowsOverlap(
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

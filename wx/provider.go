// wx/provider.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"time"

	"github.com/mmp/vice/util"
)

type Provider interface {
	// GetPrecipURL returns precipitation radar URL (GCS only).
	// Returns the item at-or-before the given time.
	GetPrecipURL(facility string, t time.Time) (string, time.Time, error)

	// GetAtmosGrid returns atmospheric grid for simulation.
	// GCS provides full spatial grid; local fallback provides single averaged sample.
	// Returns atmos, its time, the time for the next one in the series.
	// If primaryAirport is non-empty and no atmos data is available, creates
	// a fallback grid from the primary airport's METAR wind data.
	GetAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error)
}

const (
	metarIntervalTolerance  = 75 * time.Minute
	precipIntervalTolerance = 40 * time.Minute
	atmosIntervalTolerance  = 65 * time.Minute
)

// METARIntervals converts METAR timestamps to time intervals suitable for weather data.
func METARIntervals(times []time.Time) []util.TimeInterval {
	return util.FindTimeIntervals(times, metarIntervalTolerance)
}

// PrecipIntervals converts precipitation timestamps to time intervals suitable for weather data.
func PrecipIntervals(times []time.Time) []util.TimeInterval {
	return util.FindTimeIntervals(times, precipIntervalTolerance)
}

// AtmosIntervals converts atmosphere timestamps to time intervals suitable for weather data.
func AtmosIntervals(times []time.Time) []util.TimeInterval {
	return util.FindTimeIntervals(times, atmosIntervalTolerance)
}

// MergeAndAlignToMidnight merges multiple sets of time intervals and aligns them to
// full 24-hour periods starting and ending at midnight UTC (0000Z).
func MergeAndAlignToMidnight(intervals ...[]util.TimeInterval) []util.TimeInterval {
	if len(intervals) == 0 {
		return nil
	}

	iv := util.IntersectAllIntervals(intervals...)

	iv = util.MapSlice(iv, func(ti util.TimeInterval) util.TimeInterval {
		// Make sure we're in UTC.
		ti = util.TimeInterval{ti[0].UTC(), ti[1].UTC()}

		// Ensure that all intervals start and end at 0000Z by
		// advancing the start and pulling back the end as needed. Note
		// that this may give us some invalid intervals, but we will
		// cull those shortly.
		start := ti.Start().Truncate(24 * time.Hour)
		if !ti.Start().Equal(start) {
			// Interval doesn't start at midnight, so this day isn't fully covered
			start = start.Add(24 * time.Hour)
		}
		end := ti.End().Truncate(24 * time.Hour)

		return util.TimeInterval{start, end}
	})

	iv = util.FilterSliceInPlace(iv, func(in util.TimeInterval) bool {
		return in.Start().Before(in.End())
	})

	return iv
}

// FullDataDays computes time intervals where all three data sources (METAR, precip, atmos)
// have continuous coverage, aligned to full 24-hour periods at midnight UTC.
func FullDataDays(metar, precip, atmos []time.Time) []util.TimeInterval {
	var intervals [][]util.TimeInterval

	if metar != nil {
		intervals = append(intervals, METARIntervals(metar))
	}
	if precip != nil {
		intervals = append(intervals, PrecipIntervals(precip))
	}
	if atmos != nil {
		intervals = append(intervals, AtmosIntervals(atmos))
	}

	return MergeAndAlignToMidnight(intervals...)
}

// wx/provider.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"time"

	"github.com/mmp/vice/util"
)

type Provider interface {
	GetAvailableTimeIntervals() []util.TimeInterval

	// Best effort, may not have it for all airports, but no error is returned for that.
	GetMETAR(airports []string) (map[string]METARSOA, error)

	// Returns the item at-or-before the given time
	GetPrecipURL(tracon string, t time.Time) (string, time.Time, error)
	// Returns atmos, it's time, the time for the next one in the series.
	GetAtmosGrid(tracon string, t time.Time) (*AtmosByPointSOA, time.Time, time.Time, error)
}

func FullDataDays(metar, precip, atmos []time.Time) []util.TimeInterval {
	const (
		metarIntervalTolerance  = 75 * time.Minute
		precipIntervalTolerance = 40 * time.Minute
		atmosIntervalTolerance  = 65 * time.Minute
	)

	var intervals [][]util.TimeInterval

	if metar != nil {
		intervals = append(intervals, util.FindTimeIntervals(metar, metarIntervalTolerance))
	}
	if precip != nil {
		intervals = append(intervals, util.FindTimeIntervals(precip, precipIntervalTolerance))
	}
	if atmos != nil {
		intervals = append(intervals, util.FindTimeIntervals(atmos, atmosIntervalTolerance))
	}

	if len(intervals) == 0 {
		return nil
	}

	iv := util.MergeIntervals(intervals...)

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

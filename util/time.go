// util/time.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"time"
)

// TimeInterval represents a time interval with start and end times
type TimeInterval [2]time.Time

// Start returns the start time of the interval
func (ti TimeInterval) Start() time.Time {
	return ti[0]
}

// End returns the end time of the interval
func (ti TimeInterval) End() time.Time {
	return ti[1]
}

// Duration returns the duration of the interval
func (ti TimeInterval) Duration() time.Duration {
	return ti[1].Sub(ti[0])
}

// Contains checks if the interval contains the given time
func (ti TimeInterval) Contains(t time.Time) bool {
	return !t.Before(ti[0]) && !t.After(ti[1])
}

// MergeIntervals intersects multiple sets of TimeIntervals and returns the common intervals
func MergeIntervals(intervals ...[]TimeInterval) []TimeInterval {
	if len(intervals) == 0 {
		return nil
	}

	result := intervals[0]
	for i := 1; i < len(intervals); i++ {
		result = IntersectIntervals(result, intervals[i])
		if len(result) == 0 {
			return nil
		}
	}
	return result
}

// IntersectIntervals returns the intersection of two sets of TimeIntervals
func IntersectIntervals(a, b []TimeInterval) []TimeInterval {
	var result []TimeInterval
	i, j := 0, 0

	for i < len(a) && j < len(b) {
		start, end := a[i].Start(), a[i].End()
		if b[j].Start().After(start) {
			start = b[j].Start()
		}
		if b[j].End().Before(end) {
			end = b[j].End()
		}

		if start.Before(end) {
			result = append(result, TimeInterval{start, end})
		}

		if a[i].End().Before(b[j].End()) || a[i].End().Equal(b[j].End()) {
			i++
		} else {
			j++
		}
	}

	return result
}

// FindTimeIntervals creates TimeIntervals from a series of sorted times.
// Given a series of sorted times and a maximum duration, it returns intervals where
// if the duration between two successive times is greater than d, then the current
// interval ends at the first time and a new interval starts at the second time.
func FindTimeIntervals(times []time.Time, d time.Duration) []TimeInterval {
	if len(times) == 0 {
		return nil
	}

	var intervals []TimeInterval
	start := times[0]

	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) > d {
			intervals = append(intervals, TimeInterval{start, times[i-1]})
			start = times[i]
		}
	}

	// Add the final interval
	return append(intervals, TimeInterval{start, times[len(times)-1]})
}

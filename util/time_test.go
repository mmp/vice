// util/time_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"testing"
	"time"
)

func TestTimeInterval_Methods(t *testing.T) {
	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	interval := TimeInterval{start, end}

	if interval.Start() != start {
		t.Errorf("Expected start time %v, got %v", start, interval.Start())
	}

	if interval.End() != end {
		t.Errorf("Expected end time %v, got %v", end, interval.End())
	}

	expectedDuration := 2 * time.Hour
	if interval.Duration() != expectedDuration {
		t.Errorf("Expected duration %v, got %v", expectedDuration, interval.Duration())
	}
}

func TestTimeInterval_Contains(t *testing.T) {
	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	interval := TimeInterval{start, end}

	// Test time within interval
	within := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	if !interval.Contains(within) {
		t.Errorf("Expected interval to contain %v", within)
	}

	// Test start time (boundary)
	if !interval.Contains(start) {
		t.Errorf("Expected interval to contain start time %v", start)
	}

	// Test end time (boundary)
	if !interval.Contains(end) {
		t.Errorf("Expected interval to contain end time %v", end)
	}

	// Test time outside interval
	outside := time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC)
	if interval.Contains(outside) {
		t.Errorf("Expected interval to not contain %v", outside)
	}
}

func TestFindTimeIntervals(t *testing.T) {
	baseTime := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	times := []time.Time{
		baseTime,
		baseTime.Add(30 * time.Minute), // Gap: 30 minutes
		baseTime.Add(1 * time.Hour),    // Gap: 30 minutes
		baseTime.Add(3 * time.Hour),    // Gap: 2 hours (> maxGap)
		baseTime.Add(4 * time.Hour),    // Gap: 1 hour
	}

	maxGap := 1 * time.Hour

	intervals := FindTimeIntervals(times, maxGap)

	expected := []TimeInterval{
		{baseTime, baseTime.Add(1 * time.Hour)},                    // First interval
		{baseTime.Add(3 * time.Hour), baseTime.Add(4 * time.Hour)}, // Second interval after gap
	}

	if len(intervals) != len(expected) {
		t.Fatalf("Expected %d intervals, got %d", len(expected), len(intervals))
	}

	for i, interval := range intervals {
		if interval.Start() != expected[i].Start() || interval.End() != expected[i].End() {
			t.Errorf("Expected interval %d to be %v-%v, got %v-%v",
				i, expected[i].Start(), expected[i].End(), interval.Start(), interval.End())
		}
	}
}

func TestIntersectIntervals(t *testing.T) {
	baseTime := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	a := []TimeInterval{
		{baseTime, baseTime.Add(2 * time.Hour)},
		{baseTime.Add(4 * time.Hour), baseTime.Add(6 * time.Hour)},
	}

	b := []TimeInterval{
		{baseTime.Add(1 * time.Hour), baseTime.Add(3 * time.Hour)},
		{baseTime.Add(5 * time.Hour), baseTime.Add(7 * time.Hour)},
	}

	result := IntersectIntervals(a, b)

	expected := []TimeInterval{
		{baseTime.Add(1 * time.Hour), baseTime.Add(2 * time.Hour)},
		{baseTime.Add(5 * time.Hour), baseTime.Add(6 * time.Hour)},
	}

	if len(result) != len(expected) {
		t.Fatalf("Expected %d intervals, got %d", len(expected), len(result))
	}

	for i, interval := range result {
		if interval.Start() != expected[i].Start() || interval.End() != expected[i].End() {
			t.Errorf("Expected interval %d to be %v-%v, got %v-%v",
				i, expected[i].Start(), expected[i].End(), interval.Start(), interval.End())
		}
	}
}

func TestIntersectAllIntervals(t *testing.T) {
	baseTime := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	a := []TimeInterval{
		{baseTime, baseTime.Add(4 * time.Hour)},
	}

	b := []TimeInterval{
		{baseTime.Add(1 * time.Hour), baseTime.Add(3 * time.Hour)},
	}

	c := []TimeInterval{
		{baseTime.Add(2 * time.Hour), baseTime.Add(5 * time.Hour)},
	}

	result := IntersectAllIntervals(a, b, c)

	// The intersection should be from 2h to 3h
	expected := []TimeInterval{
		{baseTime.Add(2 * time.Hour), baseTime.Add(3 * time.Hour)},
	}

	if len(result) != len(expected) {
		t.Fatalf("Expected %d intervals, got %d", len(expected), len(result))
	}

	for i, interval := range result {
		if interval.Start() != expected[i].Start() || interval.End() != expected[i].End() {
			t.Errorf("Expected interval %d to be %v-%v, got %v-%v",
				i, expected[i].Start(), expected[i].End(), interval.Start(), interval.End())
		}
	}
}

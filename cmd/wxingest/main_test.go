// cmd/wxingest/main_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mmp/vice/wx"
)

func TestShardOwnsPartition(t *testing.T) {
	defer func(idx, count int) { taskIndex, taskCount = idx, count }(taskIndex, taskCount)

	// With a single task, everything is owned.
	taskIndex, taskCount = 0, 1
	if !shardOwns("scrape/WX/PHL/2026-08-25T12:00:00Z.gob") {
		t.Error("single task must own all work")
	}

	// With multiple tasks, each key is owned by exactly one task and the
	// keys are reasonably balanced across tasks.
	taskCount = 4
	const nKeys = 10000
	counts := make(map[int]int)
	for i := range nKeys {
		key := fmt.Sprintf("scrape/WX/PHL/%d.gob", i)
		owners := 0
		for taskIndex = range taskCount {
			if shardOwns(key) {
				owners++
				counts[taskIndex]++
			}
		}
		if owners != 1 {
			t.Fatalf("%s owned by %d tasks", key, owners)
		}
	}
	for idx, n := range counts {
		if n < nKeys/taskCount/2 {
			t.Errorf("task %d owns only %d of %d keys; poor balance", idx, n, nKeys)
		}
	}
}

func TestAtmosIngestFollowsEachFacilitysRadar(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, time.January, d, 0, 0, 0, 0, time.UTC) }
	every := func(start, end time.Time, step time.Duration) []time.Time {
		var times []time.Time
		for t := start; !t.After(end); t = t.Add(step) {
			times = append(times, t)
		}
		return times
	}

	// METAR for January 1-5; P31's radar covers all of it, while ZJX's
	// starts on the 3rd. LTS has no radar.
	metar := every(day(1), day(6), time.Hour)
	precip, err := wx.MakeManifestFromMap(map[string][]time.Time{
		"P31": every(day(1), day(6), 5*time.Minute),
		"ZJX": every(day(3).Add(-time.Hour), day(6), 5*time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	intervals := atmosIngestIntervals(metar, precip, []string{"LTS", "P31", "ZJX"})
	if _, ok := intervals["LTS"]; ok {
		t.Errorf("LTS has no radar but got intervals %v", intervals["LTS"])
	}

	for _, tc := range []struct {
		t        time.Time
		ingested []string
		want     []string
	}{
		{day(2).Add(12 * time.Hour), nil, []string{"P31"}},
		{day(3).Add(12 * time.Hour), nil, []string{"P31", "ZJX"}},
		{day(3).Add(12 * time.Hour), []string{"P31"}, []string{"ZJX"}},
		{day(7), nil, nil},
	} {
		if got := facilitiesMissingAtmos(tc.t, intervals, tc.ingested); !slices.Equal(got, tc.want) {
			t.Errorf("%s with %v ingested: got %v, want %v", tc.t, tc.ingested, got, tc.want)
		}
	}
}

func TestHRRRIssuedEveryThreeHoursOutsideCONUS(t *testing.T) {
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		facility string
		t        time.Time
		want     bool
	}{
		{"P31", t0.Add(4 * time.Hour), true},
		{"A11", t0.Add(3 * time.Hour), true},
		{"A11", t0.Add(4 * time.Hour), false},
		{"ZAN", t0.Add(21 * time.Hour), true},
	} {
		if got := hrrrIssued(tc.facility, tc.t); got != tc.want {
			t.Errorf("%s at %s: got %v, want %v", tc.facility, tc.t, got, tc.want)
		}
	}
}

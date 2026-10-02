// cmd/wxingest/main_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	gomath "math"
	"slices"
	"testing"
	"time"

	"github.com/mmp/vice/wx"

	"github.com/mmp/squall"
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

func TestHRRRIssuedOnEachRegionsSchedule(t *testing.T) {
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
		{"HNL", t0.Add(6 * time.Hour), true},
		{"HNL", t0.Add(3 * time.Hour), false},
		{"OGG", t0.Add(9 * time.Hour), false},
		{"ZHN", t0.Add(18 * time.Hour), true},
	} {
		if got := hrrrIssued(tc.facility, tc.t); got != tc.want {
			t.Errorf("%s at %s: got %v, want %v", tc.facility, tc.t, got, tc.want)
		}
	}
}

func TestCheckNAMRetirement(t *testing.T) {
	before := time.Date(2026, time.October, 13, 18, 0, 0, 0, time.UTC)
	after := time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		t          time.Time
		facilities []string
		wantErr    bool
	}{
		{before, []string{"P31", "HNL", "ZHN"}, false},
		{after, []string{"P31", "A11"}, false},
		{after, []string{"P31", "ZHN"}, true},
	} {
		if err := checkNAMRetirement(tc.t, tc.facilities); (err != nil) != tc.wantErr {
			t.Errorf("%s %v: got error %v, want error %v", tc.t, tc.facilities, err, tc.wantErr)
		}
	}
}

func TestDewpointFromSpecificHumidity(t *testing.T) {
	for _, tc := range []struct {
		q, mb, want float32 // kg/kg, mb, Celsius
	}{
		{0.018, 1000, 23.32},
		{0.00261, 500, -13.90}, // from the NAM Hawaii nest's SPFH and DPT
		{0, 500, minDewpoint},
	} {
		if got := dewpointFromSpecificHumidity(tc.q, tc.mb).Celsius(); got < tc.want-0.05 || got > tc.want+0.05 {
			t.Errorf("q %g at %g mb: got %.2f C, want %.2f C", tc.q, tc.mb, got, tc.want)
		}
	}
}

func TestFillLevels(t *testing.T) {
	tmp := squall.ParameterID{Discipline: 0, Category: 0, Number: 0}
	const missing = 9.999e20
	// A field that is linear in log pressure, which filling reproduces
	// exactly; the second point is missing at 1000 mb.
	field := func(mb float64) []float32 {
		v := float32(250 + 10*gomath.Log(mb))
		if mb == 1000 {
			return []float32{v, missing}
		}
		return []float32{v, v}
	}
	records := func(mbs ...float64) []*squall.GRIB2 {
		var r []*squall.GRIB2
		for _, mb := range mbs {
			r = append(r, &squall.GRIB2{Parameter: tmp, Level: fmt.Sprintf("%g mb", mb), LevelValue: float32(mb * 100), Data: field(mb)})
		}
		return r
	}
	every25 := func(lo, hi float64) []float64 {
		var mbs []float64
		for mb := lo; mb <= hi; mb += 25 {
			mbs = append(mbs, mb)
		}
		return mbs
	}

	for _, tc := range []struct {
		name string
		mbs  []float64
	}{
		{"HRRR", append(every25(50, 1000), 1013.2)},
		{"NAM", append([]float64{30}, every25(50, 1000)...)},
		{"RRFS", append([]float64{50, 70}, every25(100, 1000)...)},
	} {
		filled, err := fillLevels(records(tc.mbs...), []string{"TMP"})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(filled) != wx.NumSampleLevels {
			t.Errorf("%s: got %d records, want %d", tc.name, len(filled), wx.NumSampleLevels)
			continue
		}
		for _, r := range filled {
			level := wx.LevelIndexFromId([]byte(r.Level))
			mb := float64(wx.PressureFromLevelIndex(level))
			want := field(mb)[0]
			if got := r.Data[0]; gomath.Abs(float64(got-want)) > 1e-3 {
				t.Errorf("%s: %s: got %f, want %f", tc.name, r.Level, got, want)
			}
			// 1013.2 mb is extrapolated from 1000 mb if the source doesn't have it.
			wantMissing := mb == 1000 || (mb > 1000 && !slices.Contains(tc.mbs, 1013.2))
			if squall.IsMissing(r.Data[1]) != wantMissing {
				t.Errorf("%s: %s: got %f for the second point, want missing %v", tc.name, r.Level, r.Data[1], wantMissing)
			}
		}
	}

	// A file truncated after 500 mb is missing levels too far from any it
	// has to fill them in.
	if _, err := fillLevels(records(every25(500, 1000)...), []string{"TMP"}); err == nil {
		t.Error("truncated: expected an error")
	}
}

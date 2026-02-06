// wx/resources.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// GetMETAR returns METAR data from bundled resources for the specified airports.
func GetMETAR(airports []string) (map[string]METARSOA, error) {
	f, err := fs.ReadFile(util.GetResourcesFS(), "wx/"+METARFilename)
	if err != nil {
		return nil, err
	}

	cm, err := LoadCompressedMETAR(bytes.NewReader(f))
	if err != nil {
		return nil, err
	}

	m := make(map[string]METARSOA)
	for _, icao := range airports {
		if metarSOA, err := cm.GetAirportMETARSOA(icao); err == nil {
			m[icao] = metarSOA
		}
	}

	return m, nil
}

// GetTimeIntervals returns available time intervals from bundled resources.
// Returns a map from TRACON to available time intervals.
func GetTimeIntervals() map[string][]util.TimeInterval {
	result := make(map[string][]util.TimeInterval)

	util.WalkResources("wx/atmos", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		// Extract TRACON name from filename (e.g., "wx/atmos/PHL.msgpack.zst" -> "PHL")
		filename := filepath.Base(path)
		tracon := strings.TrimSuffix(filename, ".msgpack.zst")

		atmosByTime, err := GetAtmosByTime(tracon)
		if err != nil {
			return nil
		}

		var atmosTimes []time.Time
		for t := range atmosByTime.SampleStacks {
			atmosTimes = append(atmosTimes, t)
		}
		slices.SortFunc(atmosTimes, func(a, b time.Time) int { return a.Compare(b) })

		intervals := MergeAndAlignToMidnight(AtmosIntervals(atmosTimes))
		if len(intervals) > 0 {
			result[tracon] = intervals
		}

		return nil
	})

	return result
}

// GetAtmosByTime returns atmospheric data for a TRACON from bundled resources.
func GetAtmosByTime(tracon string) (*AtmosByTime, error) {
	path := "wx/atmos/" + tracon + ".msgpack.zst"

	f, err := fs.ReadFile(util.GetResourcesFS(), path)
	if err != nil {
		return nil, err
	}

	zr, err := zstd.NewReader(bytes.NewReader(f))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var atmosTimesSOA AtmosByTimeSOA
	if err := msgpack.NewDecoder(zr).Decode(&atmosTimesSOA); err != nil {
		return nil, err
	}

	atmosByTime := atmosTimesSOA.ToAOS()
	return &atmosByTime, nil
}

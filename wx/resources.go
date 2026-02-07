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

// Cached resource data, loaded asynchronously at startup via init().
// Each cache uses a channel that is closed when loading completes;
// callers block on the channel if the data isn't ready yet.
var (
	metarCache struct {
		done chan struct{}
		cm   CompressedMETAR
		err  error
	}
	atmosCache struct {
		done    chan struct{}
		byTime  map[string]*AtmosByTime // keyed by TRACON
		timeInt map[string][]util.TimeInterval
	}
)

func init() {
	metarCache.done = make(chan struct{})
	go func() {
		defer close(metarCache.done)
		f, err := fs.ReadFile(util.GetResourcesFS(), "wx/"+METARFilename)
		if err != nil {
			metarCache.err = err
			return
		}
		metarCache.cm, metarCache.err = LoadCompressedMETAR(bytes.NewReader(f))
	}()

	atmosCache.done = make(chan struct{})
	go func() {
		defer close(atmosCache.done)
		atmosCache.byTime = make(map[string]*AtmosByTime)
		atmosCache.timeInt = make(map[string][]util.TimeInterval)

		util.WalkResources("wx/atmos", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}

			filename := filepath.Base(path)
			tracon := strings.TrimSuffix(filename, ".msgpack.zst")

			atmosByTime, err := loadAtmosByTime(tracon)
			if err != nil {
				return nil
			}
			atmosCache.byTime[tracon] = atmosByTime

			var atmosTimes []time.Time
			for t := range atmosByTime.SampleStacks {
				atmosTimes = append(atmosTimes, t)
			}
			slices.SortFunc(atmosTimes, func(a, b time.Time) int { return a.Compare(b) })

			intervals := MergeAndAlignToMidnight(AtmosIntervals(atmosTimes))
			if len(intervals) > 0 {
				atmosCache.timeInt[tracon] = intervals
			}

			return nil
		})
	}()
}

// GetMETAR returns METAR data from bundled resources for the specified airports.
func GetMETAR(airports []string) (map[string]METARSOA, error) {
	<-metarCache.done
	if metarCache.err != nil {
		return nil, metarCache.err
	}

	m := make(map[string]METARSOA)
	for _, icao := range airports {
		if metarSOA, err := metarCache.cm.GetAirportMETARSOA(icao); err == nil {
			m[icao] = metarSOA
		}
	}

	return m, nil
}

// GetTimeIntervals returns available time intervals from bundled resources.
// Returns a map from TRACON to available time intervals.
func GetTimeIntervals() map[string][]util.TimeInterval {
	<-atmosCache.done
	return atmosCache.timeInt
}

// GetAtmosByTime returns atmospheric data for a TRACON from bundled resources.
func GetAtmosByTime(tracon string) (*AtmosByTime, error) {
	<-atmosCache.done
	if abt, ok := atmosCache.byTime[tracon]; ok {
		return abt, nil
	}
	// Not in cache (no atmos file for this TRACON); load directly.
	return loadAtmosByTime(tracon)
}

// loadAtmosByTime reads and decodes atmospheric data for a single TRACON from disk.
func loadAtmosByTime(tracon string) (*AtmosByTime, error) {
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

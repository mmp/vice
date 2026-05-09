// wx/resources.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// Cached resource data, loaded asynchronously at startup via Init().
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
		timeInt map[string][]util.TimeInterval // keyed by facility
	}
	tfrCache struct {
		done chan struct{}
		tfrs []av.TFR
		err  error
	}
)

var wxInitOnce sync.Once

func Init() {
	wxInitOnce.Do(initResources)
}

func initResources() {
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

	tfrCache.done = make(chan struct{})
	go func() {
		defer close(tfrCache.done)
		f, err := fs.ReadFile(util.GetResourcesFS(), "wx/"+TFRFilename)
		if err != nil {
			tfrCache.err = err
			return
		}
		tfrCache.tfrs, tfrCache.err = LoadCompressedTFRs(bytes.NewReader(f))
	}()

	atmosCache.done = make(chan struct{})
	go func() {
		defer close(atmosCache.done)
		atmosCache.timeInt = make(map[string][]util.TimeInterval)
		path := "wx/" + ManifestPath("atmos")
		f, err := fs.ReadFile(util.GetResourcesFS(), path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return
		}
		manifest, err := LoadManifest(bytes.NewReader(f))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return
		}

		for _, facility := range manifest.Facilities() {
			times, ok := manifest.GetTimestamps(facility)
			if !ok {
				continue
			}
			intervals := MergeAndAlignToMidnight(AtmosIntervals(times))
			if len(intervals) > 0 {
				atmosCache.timeInt[facility] = intervals
			}
		}
	}()
}

///////////////////////////////////////////////////////////////////////////
// Resource availability intervals

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

///////////////////////////////////////////////////////////////////////////
// Resource data access

// GetMETAR returns METAR data from bundled resources for the specified airports.
func GetMETAR(airports []string) (map[string]METARSOA, error) {
	Init()
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

// GetTRACONTimeIntervals returns available time intervals for TRACONs from
// bundled resources. Returns a map from TRACON id to available time
// intervals.
func GetTRACONTimeIntervals() map[string][]util.TimeInterval {
	Init()
	<-atmosCache.done

	result := make(map[string][]util.TimeInterval)
	for facility, intervals := range atmosCache.timeInt {
		if _, ok := av.DB.TRACONs[facility]; ok {
			result[facility] = intervals
		}
	}
	return result
}

// GetARTCCTimeIntervals returns available time intervals for ARTCCs from
// bundled resources. Returns a map from ARTCC id to available time
// intervals.
func GetARTCCTimeIntervals() map[string][]util.TimeInterval {
	Init()
	<-atmosCache.done

	result := make(map[string][]util.TimeInterval)
	for facility, intervals := range atmosCache.timeInt {
		if _, ok := av.DB.ARTCCs[facility]; ok {
			result[facility] = intervals
		}
	}
	return result
}

// GetAtmosByTime returns atmospheric data for a facility from bundled resources.
func GetAtmosByTime(facility string) (*AtmosByTime, error) {
	Init()
	return loadAtmosByTime(facility)
}

// loadAtmosByTime reads and decodes atmospheric data for a single facility from disk.
func loadAtmosByTime(facility string) (*AtmosByTime, error) {
	path := "wx/atmos/" + facility + ".msgpack.zst"

	f, err := fs.ReadFile(util.GetResourcesFS(), path)
	if err != nil {
		return nil, err
	}

	zr, err := zstd.NewReader(bytes.NewReader(f), zstd.WithDecoderConcurrency(1))
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

// GetCachedTFRsForARTCC returns TFRs from bundled resources matching the given
// ARTCC that are active at time t.
func GetCachedTFRsForARTCC(artcc string, t time.Time) ([]av.TFR, error) {
	Init()
	<-tfrCache.done
	if tfrCache.err != nil {
		return nil, tfrCache.err
	}
	return GetTFRsForARTCC(tfrCache.tfrs, artcc, t), nil
}

// GetCachedTFRsForTRACON returns TFRs from bundled resources matching the parent
// ARTCC that are active at time t and geographically near center.
func GetCachedTFRsForTRACON(artcc string, center math.Point2LL,
	rangeNm float32, t time.Time) ([]av.TFR, error) {
	Init()
	<-tfrCache.done
	if tfrCache.err != nil {
		return nil, tfrCache.err
	}
	return GetTFRsForTRACON(tfrCache.tfrs, artcc, center, rangeNm, t), nil
}

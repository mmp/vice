package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/mmp/squall"
	"golang.org/x/sync/errgroup"
)

func getAvailableMETARTimes(sb StorageBackend) ([]time.Time, error) {
	var metar map[string]wx.METARSOA
	if err := sb.ReadObject("METAR.msgpack.zst", &metar); err != nil {
		return nil, err
	}

	phlMETAR, ok := metar["KPHL"]
	if !ok {
		return nil, fmt.Errorf("KPHL not found in METAR data")
	}

	decoded := wx.DecodeMETARSOA(phlMETAR)
	times := make([]time.Time, 0, len(decoded))
	for _, metar := range decoded {
		times = append(times, metar.Time.UTC())
	}

	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	return times, nil
}

func getAvailablePrecipTimes(sb StorageBackend) ([]time.Time, error) {
	// List all monthly manifest files
	manifests, err := sb.List("precip/manifest-")
	if err != nil {
		return nil, err
	}

	var allPaths []string
	for manifestPath := range manifests {
		var tm []string
		if err := sb.ReadObject(manifestPath, &tm); err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", manifestPath, err)
		}

		manifest, err := util.TransposeStrings(tm)
		if err != nil {
			return nil, fmt.Errorf("failed to transpose %s: %w", manifestPath, err)
		}

		allPaths = append(allPaths, manifest...)
	}

	var times []time.Time
	for _, relativePath := range allPaths {
		// Parse paths like "PHL/2025-08-06T03:00:00Z.msgpack.zst" to extract timestamp
		parts := strings.Split(relativePath, "/")
		if len(parts) != 2 {
			LogError("%s: unexpected path in manifest", relativePath)
			continue
		}

		t, err := time.Parse(time.RFC3339, strings.TrimSuffix(parts[1], ".msgpack.zst"))
		if err != nil {
			LogError("%v", err)
			continue
		}
		times = append(times, t.UTC())
	}

	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })

	return times, nil
}

// NOAA high-resolution rapid refresh: https://rapidrefresh.noaa.gov/hrrr/
func ingestHRRR(sb StorageBackend) error {
	if err := os.Chdir(os.TempDir()); err != nil {
		return err
	}

	hrrrGCS, err := MakeGCSBackend("high-resolution-rapid-refresh")
	if err != nil {
		return err
	}
	hrrrsb := NewTrackingBackend(hrrrGCS)

	// Get available METAR and precip data times to determine valid intervals
	metarTimes, err := getAvailableMETARTimes(sb)
	if err != nil {
		return fmt.Errorf("failed to get METAR times: %w", err)
	}

	precipTimes, err := getAvailablePrecipTimes(sb)
	if err != nil {
		return fmt.Errorf("failed to get precip times: %w", err)
	}

	// Find complete day intervals where both METAR and precip data are available
	validIntervals := wx.FullDataDays(metarTimes, precipTimes, nil)
	if len(validIntervals) == 0 {
		return errors.New("no valid time intervals with complete METAR and precip data")
	}
	for _, iv := range validIntervals {
		LogInfo("Time interval with valid METAR/precip: %s - %s", iv[0], iv[1])
	}

	tfr := util.MakeTempFileRegistry(nil)
	defer tfr.RemoveAll()
	registerCleanup(tfr.RemoveAll)

	existing := listIngestedAtmos(sb)

	tCh := make(chan time.Time)
	eg, ctx := errgroup.WithContext(context.Background())
	eg.Go(func() error {
		defer close(tCh)

		// Process all hours within valid day intervals
		for _, interval := range validIntervals {
			start := interval[0].UTC()
			end := interval[1].UTC()

			// Iterate through each hour in the interval (including the
			// 0000Z at the end).
			for t := start; !t.After(end); t = t.Add(time.Hour) {
				// Stop once we get close to the current time
				if time.Since(t) <= 3*time.Hour {
					break
				}

				// Check if we already have data for all TRACONs at this time
				tracons := existing[t] // may be empty
				slices.Sort(tracons)
				var missing []string
				for _, tracon := range wx.AtmosTRACONs {
					if !slices.Contains(tracons, tracon) {
						missing = append(missing, tracon)
					}
				}
				if len(missing) > 0 {
					LogInfo(fmt.Sprintf("Time %s: missing atmos for %s\n", t, strings.Join(missing, ", ")))

					select {
					case tCh <- t:
					case <-ctx.Done():
						return ctx.Err()
					}
					if *hrrrQuick {
						return nil
					}
				}
			}
		}
		return nil
	})

	type downloadedHRRR struct {
		path string
		t    time.Time
	}
	hrrrCh := make(chan downloadedHRRR, 1) // buffer 1 to have the next one prefetched.
	eg.Go(func() error {
		// Download HRRR files in a goroutine so that we can start
		// downloading the next one after the one currently being
		// processed.
		defer close(hrrrCh)
		for t := range tCh {
			path, err := downloadHRRRForTime(t, tfr, hrrrsb)
			if err != nil {
				LogError("%v", err)
			} else {
				select {
				case hrrrCh <- downloadedHRRR{path: path, t: t}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	})

	eg.Go(func() error {
		for hrrr := range hrrrCh {
			LogInfo("Starting work on " + hrrr.t.Format(time.RFC3339))
			if err := ingestHRRRForTime(hrrr.path, hrrr.t, existing[hrrr.t], sb, hrrrsb); err != nil {
				return err
			}
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	// Merge HRRR transfer statistics into main backend
	if mainTB, ok := sb.(*TrackingBackend); ok {
		mainTB.MergeStats(hrrrsb)
	}

	return generateManifest(sb, "atmos")

}

func listIngestedAtmos(sb StorageBackend) map[time.Time][]string {
	ingested := make(map[time.Time][]string) // which TRACONs have the data for the time

	if *hrrrQuick {
		// start at the beginning
		return nil
	}

	// List all objects under atmos/ in one call
	atmosPaths, err := sb.List("atmos/")
	if err != nil {
		LogError("Failed to list atmos/ directory: %v", err)
		return ingested
	}

	// Parse all paths in a single pass
	for path := range atmosPaths {
		if path == "atmos/manifest.msgpack.zst" {
			continue
		}

		// Parse paths like atmos/BOI/2025-07-27T18:00:00Z.msgpack.zst
		parts := strings.Split(strings.TrimPrefix(path, "atmos/"), "/")
		if len(parts) != 2 {
			LogError("%s: malformed path", path)
			continue
		}

		tracon := parts[0]

		tm, err := time.Parse(time.RFC3339, strings.TrimSuffix(parts[1], ".msgpack.zst"))
		if err != nil {
			LogError("%s", err)
			continue
		}

		ingested[tm] = append(ingested[tm], tracon)
	}

	LogInfo("Found %d ingested atmos TRACON objects for %d times", len(atmosPaths), len(ingested))

	return ingested
}

func checkDiskSpace(path string, requiredGB int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("failed to check disk space for %s: %w", path, err)
	}

	// Calculate available space in bytes
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)
	requiredBytes := requiredGB * 1024 * 1024 * 1024

	if availableBytes < requiredBytes {
		return fmt.Errorf("insufficient disk space in %s: %.2f GB available, %d GB required",
			path, float64(availableBytes)/(1024*1024*1024), requiredGB)
	}

	return nil
}

func downloadHRRRForTime(t time.Time, tfr *util.TempFileRegistry, hrrrsb StorageBackend) (string, error) {
	// Check disk space before downloading
	if err := checkDiskSpace(".", 2); err != nil {
		return "", err
	}

	// Download the grib2 file from the NOAA archive
	hrrrpath := fmt.Sprintf("hrrr.%d%02d%02d/conus/hrrr.t%02dz.wrfprsf00.grib2", t.Year(), t.Month(), t.Day(), t.Hour())
	hrrrr, err := hrrrsb.OpenRead(hrrrpath)
	if err != nil {
		return "", err
	}
	defer hrrrr.Close()

	hf, err := os.Create(fmt.Sprintf("%s.grib2", t.Format(time.RFC3339)))
	if err != nil {
		return "", err
	}
	tfr.RegisterPath(hf.Name())

	LogInfo("%s: downloading", hrrrpath)

	n, err := io.Copy(hf, hrrrr)
	if err != nil {
		hf.Close()
		return "", err
	}

	if err := hf.Close(); err != nil {
		return "", err
	}

	LogInfo("%s: downloaded %s to %s", hrrrpath, util.ByteCount(n), hf.Name())

	if n < 32*1024*1024 {
		return "", fmt.Errorf("%s: grib2 file appears truncated: length %d", hrrrpath, n)
	}

	return hf.Name(), nil
}

func ingestHRRRForTime(gribPath string, t time.Time, existingTRACONs []string, sb, hrrrsb StorageBackend) error {
	defer func() { _ = os.Remove(gribPath) }()

	records, err := parseAndFilterGRIB2(gribPath)
	if err != nil {
		return err
	}

	var eg errgroup.Group
	var totalUploads, totalUploadBytes int64
	sem := make(chan struct{}, *nWorkers)
	for _, tracon := range wx.AtmosTRACONs {
		if !slices.Contains(existingTRACONs, tracon) {
			eg.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				n, err := ingestHRRRForTracon(records, tracon, t, sb)
				if err == nil {
					LogInfo("Uploaded %s for %s-%s", util.ByteCount(n), tracon, t.Format(time.RFC3339))
					atomic.AddInt64(&totalUploads, 1)
					atomic.AddInt64(&totalUploadBytes, n)
				}

				return err
			})
		}
	}

	return eg.Wait()
}

func ingestHRRRForTracon(records []*squall.GRIB2, tracon string, t time.Time, sb StorageBackend) (int64, error) {
	sf, err := sampleFieldFromGRIB2(records, tracon, t)
	if err != nil {
		return 0, fmt.Errorf("%s-%s: GRIB2 parsing failed: %w", tracon, t.Format(time.RFC3339), err)
	} else {
		return uploadWeatherAtmos(sf, tracon, t, sb)
	}
}

func parseAndFilterGRIB2(gribPath string) ([]*squall.GRIB2, error) {
	f, err := os.Open(gribPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open GRIB2 file: %w", err)
	}
	defer f.Close()

	records, err := squall.Read(f)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GRIB2 file: %w", err)
	}

	LogInfo("%s: parsed %d total records", gribPath, len(records))
	if len(records) > 0 {
		for i := 0; i < min(5, len(records)); i++ {
			r := records[i]
			LogInfo("  Sample record %d: param=%s, level=%q, levelValue=%.1f, numPoints=%d",
				i, r.Parameter.ShortName(), r.Level, r.LevelValue, r.NumPoints)
		}
	}

	// Filter to only keep records we care about:
	// - Parameters: UGRD, VGRD, TMP, DPT, HGT
	// - Levels: isobaric levels that vice recognizes
	filtered := util.FilterSlice(records, func(record *squall.GRIB2) bool {
		// Check parameter
		shortName := record.Parameter.ShortName()
		if shortName != "UGRD" && shortName != "VGRD" && shortName != "TMP" && shortName != "DPT" && shortName != "HGT" {
			return false
		}

		// Check if vice recognizes this level (squall provides levels already formatted as "50 mb", etc.)
		levelIndex := wx.LevelIndexFromId([]byte(record.Level))
		if levelIndex == -1 {
			LogInfo("  Rejected record: param=%s, level=%q, levelIndex=%d",
				shortName, record.Level, levelIndex)
		}
		return levelIndex != -1
	})

	LogInfo("%s: filtered %d records to %d records", gribPath, len(records), len(filtered))

	if len(filtered) > 0 {
		LogInfo("  Filtered records by parameter:")
		counts := make(map[string]int)
		for _, r := range filtered {
			counts[r.Parameter.ShortName()]++
		}
		for param, count := range counts {
			LogInfo("    %s: %d records", param, count)
		}
	}

	return filtered, nil
}

func sampleFieldFromGRIB2(records []*squall.GRIB2, tracon string, t time.Time) (*wx.AtmosByPoint, error) {
	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius
	nmPerLongitude := math.NMPerLongitudeAt(center)

	at := wx.MakeAtmosByPoint()

	var arena []wx.AtmosSampleStack
	allocStack := func() *wx.AtmosSampleStack {
		if len(arena) == 0 {
			arena = make([]wx.AtmosSampleStack, 1024)
		}
		s := &arena[0]
		arena = arena[1:]
		return s
	}

	var totalPoints, filteredPoints, addedPoints int

	const (
		recUnsetType = iota
		recUDirection
		recVDirection
		recTemperature
		recDewpoint
		recHeight
	)

	for _, record := range records {
		var recType int
		switch record.Parameter.ShortName() {
		case "UGRD":
			recType = recUDirection
		case "VGRD":
			recType = recVDirection
		case "TMP":
			recType = recTemperature
		case "DPT":
			recType = recDewpoint
		case "HGT":
			recType = recHeight
		default:
			return nil, errors.New("unexpected parameter: " + record.Parameter.ShortName())
		}

		// Get the level index (already validated during filtering)
		levelIndex := wx.LevelIndexFromId([]byte(record.Level))

		if levelIndex == -1 {
			return nil, fmt.Errorf("GRIB2: param=%s, level=%q -> invalid levelIndex", record.Parameter.ShortName(), record.Level)
		}

		// Process each value in the record (squall uses parallel slices)
		for i := range record.NumPoints {
			totalPoints++
			value := record.Data[i]

			// Skip missing values
			if value > 9e20 {
				continue
			}

			lon := record.Longitudes[i]
			if lon > 180 {
				lon -= 360
			}
			pt := math.Point2LL{lon, record.Latitudes[i]}

			// Check if point is within TRACON bounds
			if d := math.NMDistance2LLFast(center, pt, nmPerLongitude); d > radius {
				filteredPoints++
				continue
			}

			addedPoints++
			stack, ok := at.SampleStacks[pt]
			if !ok {
				stack = allocStack()
				at.SampleStacks[pt] = stack
			}

			switch recType {
			case recUDirection:
				stack.Levels[levelIndex].UComponent = value
			case recVDirection:
				stack.Levels[levelIndex].VComponent = value
			case recTemperature:
				stack.Levels[levelIndex].Temperature = value
			case recDewpoint:
				stack.Levels[levelIndex].Dewpoint = value
			case recHeight:
				stack.Levels[levelIndex].Height = value
			}
		}
	}

	LogInfo("GRIB2 %s-%s: processed %d records, %d total points, %d filtered by distance, %d added, %d unique locations",
		tracon, t.Format(time.RFC3339), len(records), totalPoints, filteredPoints, addedPoints, len(at.SampleStacks))

	return &at, nil
}

func uploadWeatherAtmos(at *wx.AtmosByPoint, tracon string, t time.Time, st StorageBackend) (int64, error) {
	soa, err := at.ToSOA()
	if err != nil {
		return 0, err
	}
	if err := wx.CheckAtmosConversion(*at, soa); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("atmos/%s/%s.msgpack.zst", tracon, t.Format(time.RFC3339))

	if *hrrrQuick {
		// skip upload
		var drb DryRunBackend
		return drb.StoreObject(path, soa)
	}

	return st.StoreObject(path, soa)
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"slices"
	"strconv"
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
	tmp := os.Getenv("WXINGEST_TMP")
	if tmp == "" {
		return errors.New("Must set WXINGEST_TMP environment variable for HRRR")
	}
	_ = os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0755); err != nil {
		return err
	}
	if err := os.Chdir(tmp); err != nil {
		return err
	}

	hrrrsb, err := MakeGCSBackend("high-resolution-rapid-refresh")
	if err != nil {
		return err
	}

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

	existing := listIngestedAtmos(sb)

	tfr := util.MakeTempFileRegistry(nil)
	defer tfr.RemoveAll()

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
	//nTimeWorkers := min(2, *nWorkers)
	hrrrCh := make(chan downloadedHRRR)
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

	// Do two times at once to keep the CPU busy and avoid low
	// utilization at the end when just a few TRACONs are left.
	for range 1 { // nTimeWorkers {
		eg.Go(func() error {
			for hrrr := range hrrrCh {
				LogInfo("Starting work on " + hrrr.t.Format(time.RFC3339))
				if err := ingestHRRRForTime(hrrr.path, hrrr.t, existing[hrrr.t], tfr, sb, hrrrsb); err != nil {
					return err
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
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
	tmp := os.Getenv("WXINGEST_TMP")
	if err := checkDiskSpace(tmp, 8); err != nil {
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

func ingestHRRRForTime(gribPath string, t time.Time, existingTRACONs []string, tfr *util.TempFileRegistry,
	sb, hrrrsb StorageBackend) error {
	defer tfr.RemoveAllPrefix(t.Format(time.RFC3339))

	// Parse and filter the GRIB file once for all TRACONs
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

				n, err := ingestHRRRForTracon(gribPath, records, tracon, tfr, t, sb)
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

func ingestHRRRForTracon(gribPath string, records []*squall.GRIB2, tracon string, tfr *util.TempFileRegistry, t time.Time, sb StorageBackend) (int64, error) {
	pathPrefix := tracon + "-" + t.Format(time.RFC3339)
	defer tfr.RemoveAllPrefix(pathPrefix)

	// Run CSV-based approach (wgrib2)
	f, err := gribToCSV(gribPath, tracon, pathPrefix, tfr)
	if err != nil {
		return 0, err
	}

	sfCSV, err := sampleFieldFromCSV(tracon, f)
	if err != nil {
		return 0, err
	}

	// Run GRIB2-based approach (go-grib2) with pre-parsed records
	sfGRIB2, err := sampleFieldFromGRIB2(records, tracon, t)
	if err != nil {
		LogError("%s-%s: GRIB2 parsing failed: %v", tracon, t.Format(time.RFC3339), err)
	} else {
		// Compare the two results
		if err := compareAtmosByPoint(sfCSV, sfGRIB2, tracon); err != nil {
			LogError("%s-%s: Comparison failed:\n%v", tracon, t.Format(time.RFC3339), err)
		} else {
			LogInfo("%s-%s: CSV and GRIB2 parsing results match!", tracon, t.Format(time.RFC3339))
		}
	}

	// Use CSV result for upload (maintain existing behavior)
	return uploadWeatherAtmos(sfCSV, tracon, t, sb)
}

func gribToCSV(gribPath, tracon, pathPrefix string, tfr *util.TempFileRegistry) (*os.File, error) {
	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius

	bbox := math.BoundLatLongCircle(center, radius)

	smallGribPath := pathPrefix + ".grib2"
	cmd := exec.Command("wgrib2", gribPath, "-small_grib", fmt.Sprintf("%f:%f", bbox.P0[0], bbox.P1[0]), /* longitude */
		fmt.Sprintf("%f:%f", bbox.P0[1], bbox.P1[1]) /* latitude */, smallGribPath)
	tfr.RegisterPath(smallGribPath)

	//LogInfo("Running " + cmd.String())
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	cf, err := os.Create(pathPrefix + ".csv")
	if err != nil {
		return nil, err
	}
	tfr.RegisterPath(cf.Name())

	cmd = exec.Command("wgrib2", smallGribPath, "-match", ":(UGRD|VGRD|TMP|DPT|HGT):", "-csv", cf.Name())
	//LogInfo("Running " + cmd.String())
	if err := cmd.Run(); err != nil {
		cf.Close()
		return nil, err
	}

	if err := cf.Sync(); err != nil {
		cf.Close()
		return nil, err
	}

	if _, err := cf.Seek(0, 0); err != nil {
		return nil, err
	}

	return cf, nil
}

func sampleFieldFromCSV(tracon string, f *os.File) (*wx.AtmosByPoint, error) {
	eg, ctx := errgroup.WithContext(context.Background())

	// Read chunks of the file asynchronously and with double-buffering so
	// that reads continue as processing is being performed.
	freeBufCh := make(chan []byte, 1)
	eg.Go(func() error {
		freeBufCh <- make([]byte, 16*1024*1024)
		freeBufCh <- make([]byte, 16*1024*1024)
		return nil
	})

	readBufCh := make(chan []byte, 1)
	eg.Go(func() error { return readCSV(ctx, f, freeBufCh, readBufCh) })

	var sf *wx.AtmosByPoint
	eg.Go(func() error {
		var err error
		sf, err = parseWindCSV(ctx, tracon, f.Name(), readBufCh, freeBufCh)
		return err
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return sf, nil
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

	for _, record := range records {
		// Determine the item type
		var itemType int
		switch record.Parameter.ShortName() {
		case "UGRD":
			itemType = LineItemUDirection
		case "VGRD":
			itemType = LineItemVDirection
		case "TMP":
			itemType = LineItemTemperature
		case "DPT":
			itemType = LineItemDewpoint
		case "HGT":
			itemType = LineItemHeight
		default:
			panic("unexpected parameter: " + record.Parameter.ShortName())
		}

		// Get the level index (already validated during filtering)
		levelIndex := wx.LevelIndexFromId([]byte(record.Level))

		if levelIndex == -1 {
			LogError("GRIB2: param=%s, level=%q -> invalid levelIndex",
				record.Parameter.ShortName(), record.Level)
			continue
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

			switch itemType {
			case LineItemUDirection:
				stack.Levels[levelIndex].UComponent = value
			case LineItemVDirection:
				stack.Levels[levelIndex].VComponent = value
			case LineItemTemperature:
				stack.Levels[levelIndex].Temperature = value
			case LineItemDewpoint:
				stack.Levels[levelIndex].Dewpoint = value
			case LineItemHeight:
				stack.Levels[levelIndex].Height = value
			}
		}
	}

	LogInfo("GRIB2 %s-%s: processed %d records, %d total points, %d filtered by distance, %d added, %d unique locations",
		tracon, t.Format(time.RFC3339), len(records), totalPoints, filteredPoints, addedPoints, len(at.SampleStacks))

	return &at, nil
}

func compareAtmosByPoint(a, b *wx.AtmosByPoint, tracon string) error {
	var differences []string

	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		panic(fmt.Sprintf("%s: unable to find bounds for TRACON", tracon))
	}
	center, radius := tspec.Center(), tspec.Radius
	nmPerLongitude := math.NMPerLongitudeAt(center)

	hasSample := func(stack map[math.Point2LL]*wx.AtmosSampleStack, pt math.Point2LL) (math.Point2LL, float32) {
		closestDist := float32(999999)
		var closest math.Point2LL
		for ps := range stack {
			if dist := math.NMDistance2LLFast(ps, pt, nmPerLongitude); dist < closestDist {
				closest = ps
				closestDist = dist
			}
		}
		return closest, closestDist
	}

	// Check that both have the same set of points
	const distLimit = 0.03 // the CSVs seem to be printed with 3 decimals of precision, so allow some slop...
	for pt := range a.SampleStacks {
		cdist := math.NMDistance2LLFast(center, pt, nmPerLongitude)
		if pc, d := hasSample(b.SampleStacks, pt); d > distLimit && math.Abs(cdist-radius) > 1 {
			differences = append(differences, fmt.Sprintf("point %v exists in first but not in second closest %v dist %.3f (dist %.2f rad %f)",
				pt, pc, d, math.NMDistance2LLFast(center, pt, nmPerLongitude), radius))
		}
	}
	for pt := range b.SampleStacks {
		cdist := math.NMDistance2LLFast(center, pt, nmPerLongitude)
		if pc, d := hasSample(a.SampleStacks, pt); d > distLimit && math.Abs(cdist-radius) > 0.1 {
			differences = append(differences, fmt.Sprintf("point %v exists in second but not in first closest %v dist %.3f (dist %.2f rad %f)",
				pt, pc, d, cdist, radius))
		}
	}

	// Compare values at each point and level
	const (
		windTolerance        = 0.1 // m/s
		temperatureTolerance = 0.1 // Kelvin
		heightTolerance      = 1.0 // meters
	)

	for pt, stackA := range a.SampleStacks {
		stackB, ok := b.SampleStacks[pt]
		if !ok {
			// Already warned above
			continue
		}

		for level := range wx.NumSampleLevels {
			sampleA := stackA.Levels[level]
			sampleB := stackB.Levels[level]

			if math.Abs(sampleA.UComponent-sampleB.UComponent) > windTolerance {
				differences = append(differences, fmt.Sprintf("Point %v, Level %d (%s): UComponent differs: %.4f vs %.4f",
					pt, level, wx.IdFromLevelIndex(level), sampleA.UComponent, sampleB.UComponent))
			}
			if math.Abs(sampleA.VComponent-sampleB.VComponent) > windTolerance {
				differences = append(differences, fmt.Sprintf("Point %v, Level %d (%s): VComponent differs: %.4f vs %.4f",
					pt, level, wx.IdFromLevelIndex(level), sampleA.VComponent, sampleB.VComponent))
			}
			if math.Abs(sampleA.Temperature-sampleB.Temperature) > temperatureTolerance {
				differences = append(differences, fmt.Sprintf("Point %v, Level %d (%s): Temperature differs: %.4f vs %.4f",
					pt, level, wx.IdFromLevelIndex(level), sampleA.Temperature, sampleB.Temperature))
			}
			if math.Abs(sampleA.Dewpoint-sampleB.Dewpoint) > temperatureTolerance {
				differences = append(differences, fmt.Sprintf("Point %v, Level %d (%s): Dewpoint differs: %.4f vs %.4f",
					pt, level, wx.IdFromLevelIndex(level), sampleA.Dewpoint, sampleB.Dewpoint))
			}
			if math.Abs(sampleA.Height-sampleB.Height) > heightTolerance {
				differences = append(differences, fmt.Sprintf("Point %v, Level %d (%s): Height differs: %.4f vs %.4f",
					pt, level, wx.IdFromLevelIndex(level), sampleA.Height, sampleB.Height))
			}
		}
	}

	if len(differences) > 0 {
		msg := fmt.Sprintf("Found %d differences:\n", len(differences))
		return errors.New(msg + "\n" + strings.Join(differences, "\n"))
	}

	return nil
}

func readCSV(ctx context.Context, f *os.File, freeBufCh <-chan []byte, readBufCh chan<- []byte) error {
	for {
		var buf []byte
		select {
		case buf = <-freeBufCh:
		case <-ctx.Done():
			close(readBufCh)
			return ctx.Err()
		}

		n, err := f.Read(buf)
		if n == 0 && err == io.EOF {
			close(readBufCh) // no more coming
			return nil
		} else if err != nil && err != io.EOF {
			return err
		} else {
			select {
			case readBufCh <- buf[:n]:
			case <-ctx.Done():
				close(readBufCh)
				return ctx.Err()
			}
		}
	}
}

func commaSplit(s []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for {
			if idx := bytes.IndexByte(s, ','); idx != -1 {
				if !yield(s[:idx]) { // don't include the comma
					return
				}
				s = s[idx+1:]
			} else {
				yield(s)
				return
			}
		}
	}
}

var div10 = []float64{
	1,
	10,
	100,
	1000,
	10000,
	100000,
	1000000,
	10000000,
	100000000,
	1000000000,
	10000000000,
	100000000000,
	1000000000000,
}

func atof(s []byte) float32 {
	var neg, decimal bool
	var ndec int
	var digits int64
	for _, ch := range s {
		switch {
		case ch == '-':
			neg = true
		case ch == '.':
			decimal = true
		case ch >= '0' && ch <= '9':
			digits *= 10
			digits += int64(ch - '0')
			if decimal {
				ndec++
			}
		case ch == 'e':
			// Doh--scientific notation. punt
			v, err := strconv.ParseFloat(string(s), 32)
			if err != nil {
				panic(err)
			}
			return float32(v)
		default:
			panic("bad string passed to atof: " + string(s))
		}
	}
	vf := float64(digits)
	if neg {
		vf = -vf
	}
	if ndec > 0 {
		vf /= div10[ndec]
	}

	return float32(vf)
}

const (
	LineItemUnsetType = iota
	LineItemUDirection
	LineItemVDirection
	LineItemTemperature
	LineItemDewpoint
	LineItemHeight
)

type LineItem struct {
	Lat, Long, Value float32
	Type             int
	Level            int
}

func parseWindCSV(ctx context.Context, tracon, filename string, readBufCh <-chan []byte, freeBufCh chan<- []byte) (*wx.AtmosByPoint, error) {
	bp := 0 // buf pos
	var buf []byte

	var getline func() []byte
	getline = func() []byte {
		if idx := bytes.IndexByte(buf[bp:], '\n'); idx != -1 {
			line := buf[bp : bp+idx] // no \n
			bp += idx + 1            // skip \n
			return line
		}

		// reached the end without finding a newline
		var accum []byte
		if buf != nil {
			accum = append(accum, buf[bp:]...) // copy!
			freeBufCh <- buf
		}

		var ok bool
		select {
		case buf, ok = <-readBufCh:
			break
		case <-ctx.Done():
			return nil
		}

		bp = 0
		if !ok { // EOF
			return accum
		}
		return append(accum, getline()...)
	}

	var arena []wx.AtmosSampleStack
	allocStack := func() *wx.AtmosSampleStack {
		if len(arena) == 0 {
			arena = make([]wx.AtmosSampleStack, 1024)
		}
		s := &arena[0]
		arena = arena[1:]
		return s
	}

	at := wx.MakeAtmosByPoint()

	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius
	nmPerLongitude := math.NMPerLongitudeAt(center)

	start := time.Now()
	n, nbytes := 0, 0
	for {
		line := getline()
		if len(line) == 0 {
			elapsed := time.Since(start).Seconds()
			LogInfo("%s: processed %d lines of HRRR CSV (%.2f M / sec, %.2f MB/s)", filename, n,
				float64(n)/elapsed/(1024*1024), float64(nbytes)/elapsed/(1024*1024))

			return &at, nil
		}
		if n%1000000 == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				break
			}
		}

		n++
		nbytes += len(line)
		if item, err := parseHRRRLine(line); err != nil {
			return nil, err
		} else if item.Type != LineItemUnsetType {
			pt := math.Point2LL{item.Long, item.Lat}
			if d := math.NMDistance2LLFast(center, pt, nmPerLongitude); d > radius {
				// Skip this point
				continue
			}

			stack, ok := at.SampleStacks[pt]
			if !ok {
				stack = allocStack()
				at.SampleStacks[pt] = stack
			}

			switch item.Type {
			case LineItemUDirection:
				stack.Levels[item.Level].UComponent = item.Value
			case LineItemVDirection:
				stack.Levels[item.Level].VComponent = item.Value
			case LineItemTemperature:
				stack.Levels[item.Level].Temperature = item.Value
			case LineItemDewpoint:
				stack.Levels[item.Level].Dewpoint = item.Value
			case LineItemHeight:
				stack.Levels[item.Level].Height = item.Value
			}
		}
	}
}

func parseHRRRLine(line []byte) (LineItem, error) {
	var li LineItem
	// "2025-08-06 03:00:00","2025-08-06 03:00:00","HGT","50 mb",-122.72,21.1381,20804.8
	if line[43] != ',' {
		return LineItem{}, fmt.Errorf("Found %q at 43 in %q", string(line[43]), line)
	}
	line = line[44:]

	var record [5][]byte
	i := 0
	for r := range commaSplit(line) { // strings.SplitSeq(line, ",") {
		if r[0] == '"' {
			r = r[1 : len(r)-1]
		}
		record[i] = r
		i++
	}
	if i != len(record) {
		return LineItem{}, fmt.Errorf("Didn't find 5 records in line %q. OUT OF SPACE?", string(line))
	}

	level := wx.LevelIndexFromId(record[1])
	if level == -1 {
		// not a level we care about (cloud tops, etc.)
		return LineItem{}, nil
	}
	li.Level = level

	if bytes.Equal(record[0], []byte("UGRD")) {
		li.Type = LineItemUDirection
	} else if bytes.Equal(record[0], []byte("VGRD")) {
		li.Type = LineItemVDirection
	} else if bytes.Equal(record[0], []byte("TMP")) {
		li.Type = LineItemTemperature
	} else if bytes.Equal(record[0], []byte("DPT")) {
		li.Type = LineItemDewpoint
	} else if bytes.Equal(record[0], []byte("HGT")) {
		li.Type = LineItemHeight
	}

	li.Lat = atof(record[3])
	li.Long = atof(record[2])
	li.Value = atof(record[4])

	return li, nil
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

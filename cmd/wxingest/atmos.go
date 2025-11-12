package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
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
	r, err := sb.OpenRead(wx.METARFilename)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	compressedMETAR, err := wx.LoadCompressedMETAR(r)
	if err != nil {
		return nil, err
	}

	metar, err := compressedMETAR.GetAirportMETAR("KPHL")
	if err != nil {
		return nil, err
	}

	times := util.MapSlice(metar, func(m wx.METAR) time.Time { return m.Time.UTC() })

	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })

	return times, nil
}

func getAvailablePrecipTimes(sb StorageBackend) ([]time.Time, error) {
	var rawManifest wx.RawManifest
	if err := sb.ReadObject(wx.ManifestPath("precip"), &rawManifest); err != nil {
		return nil, err
	}

	manifest := wx.MakeManifest(rawManifest)

	// All timestamps from all TRACONs, then sort and remove duplicates
	allTimes := manifest.GetAllTimestamps()
	slices.SortFunc(allTimes, func(a, b time.Time) int { return a.Compare(b) })
	allTimes = slices.CompactFunc(allTimes, func(a, b time.Time) bool { return a.Equal(b) })

	return allTimes, nil
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

	return generateAtmosManifest(sb)

}

func generateAtmosManifest(sb StorageBackend) error {
	LogInfo("Updating consolidated atmos manifest")

	paths, err := sb.List("atmos/")
	if err != nil {
		return err
	}

	manifest, err := wx.GenerateManifestWithPrefix(paths, "atmos")
	if err != nil {
		return err
	}

	manifestPath := wx.ManifestPath("atmos")
	n, err := sb.StoreObject(manifestPath, manifest.RawManifest())
	if err != nil {
		return err
	}

	LogInfo("Stored %d items in consolidated %s (%s)", manifest.TotalEntries(), manifestPath, util.ByteCount(n))

	return nil
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
		if strings.Contains(path, "manifest") {
			continue
		}

		// Parse paths like atmos/BOI/2025-07-27T18:00:00Z.msgpack.zst
		tracon, timestamp, err := wx.ParseWeatherObjectPath(strings.TrimPrefix(path, "atmos/"))
		if err != nil {
			LogError("%s: %v", path, err)
			continue
		}

		tm := time.Unix(timestamp, 0).UTC()
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

	// Build grid once for all TRACONs
	grid := buildGridFromGRIB2(records)

	var eg errgroup.Group
	var totalUploads, totalUploadBytes int64
	sem := make(chan struct{}, *nWorkers)
	for _, tracon := range wx.AtmosTRACONs {
		if !slices.Contains(existingTRACONs, tracon) {
			eg.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				if *validateGrid {
					if err := validateGridForTracon(grid, records, tracon); err != nil {
						LogError("%s: grid validation failed: %v", tracon, err)
					}
				}

				n, err := ingestHRRRForTracon(grid, records, tracon, t, sb)
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

func ingestHRRRForTracon(grid *Grid, records []*squall.GRIB2, tracon string, t time.Time, sb StorageBackend) (int64, error) {
	sf, err := sampleFieldFromGRIB2(grid, records, tracon, t)
	if err != nil {
		return 0, fmt.Errorf("%s-%s: GRIB2 parsing failed: %w", tracon, t.Format(time.RFC3339), err)
	}
	return uploadWeatherAtmos(sf, tracon, t, sb)
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
	if false && len(records) > 0 {
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
		if false && levelIndex == -1 {
			LogInfo("  Rejected record: param=%s, level=%q, levelIndex=%d",
				shortName, record.Level, levelIndex)
		}
		return levelIndex != -1
	})

	LogInfo("%s: filtered %d records to %d records", gribPath, len(records), len(filtered))

	if false && len(filtered) > 0 {
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

func sampleFieldFromGRIB2(grid *Grid, records []*squall.GRIB2, tracon string, t time.Time) (*wx.AtmosByPoint, error) {
	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius

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
	processedPoints := 0
	for ref := range grid.QueryCircle(center, radius) {
		processedPoints++

		record := records[ref.RecordIdx]
		value := record.Data[ref.PointIdx]

		// Get the level index (should already have been validated during filtering)
		levelIndex := wx.LevelIndexFromId([]byte(record.Level))
		if levelIndex == -1 {
			return nil, fmt.Errorf("GRIB2: param=%s, level=%q -> invalid levelIndex", record.Parameter.ShortName(), record.Level)
		}

		stack, ok := at.SampleStacks[ref.Location]
		if !ok {
			stack = allocStack()
			at.SampleStacks[ref.Location] = stack
		}

		switch record.Parameter.ShortName() {
		case "UGRD":
			stack.Levels[levelIndex].UComponent = value
		case "VGRD":
			stack.Levels[levelIndex].VComponent = value
		case "TMP":
			stack.Levels[levelIndex].Temperature = value
		case "DPT":
			stack.Levels[levelIndex].Dewpoint = value
		case "HGT":
			stack.Levels[levelIndex].Height = value
		default:
			return nil, errors.New("unexpected parameter: " + record.Parameter.ShortName())
		}
	}

	if false {
		LogInfo("GRIB2 %s-%s: processed %d points -> %d unique locations",
			tracon, t.Format(time.RFC3339), processedPoints, len(at.SampleStacks))
	}

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

///////////////////////////////////////////////////////////////////////////
// Grid

// GridCell represents a cell in the  grid using integer coordinates.
type GridCell struct {
	LatCell, LonCell int
}

// PointRef references a specific data point in the GRIB2 records.
type PointRef struct {
	RecordIdx int           // index in records slice
	PointIdx  int           // index in record.Data slice
	Location  math.Point2LL // cached location for quick access
}

// Grid divides the lat-lon space into uniform (w.r.t. degrees) cells.
type Grid struct {
	CellSize float32                 // cell size in degrees
	Cells    map[GridCell][]PointRef // points in each cell
}

// NewGrid creates a new  grid with the specified cell size in degrees.
// A cell size of 0.5° is approximately 30-35 nautical miles at mid-latitudes.
func NewGrid(cellSize float32) *Grid {
	return &Grid{
		CellSize: cellSize,
		Cells:    make(map[GridCell][]PointRef),
	}
}

// cellForPoint returns the grid cell containing the given point.
func (sg *Grid) cellForPoint(pt math.Point2LL) GridCell {
	return GridCell{
		LatCell: int(math.Floor(pt.Latitude() / sg.CellSize)),
		LonCell: int(math.Floor(pt.Longitude() / sg.CellSize)),
	}
}

// AddPoint adds a point reference to the  grid.
func (sg *Grid) AddPoint(p math.Point2LL, recordIdx, pointIdx int) {
	cell := sg.cellForPoint(p)
	sg.Cells[cell] = append(sg.Cells[cell], PointRef{
		RecordIdx: recordIdx,
		PointIdx:  pointIdx,
		Location:  p,
	})
}

// QueryCircle returns an iterator over all points within radiusNM of center.
// This performs a coarse grid-based filter followed by precise distance checking.
// Panics if the query region spans the longitude wrap-around at ±180°.
func (sg *Grid) QueryCircle(center math.Point2LL, radiusNM float32) iter.Seq[PointRef] {
	// Convert radius from nautical miles to degrees for grid cell calculation
	nmPerLongitude := math.NMPerLongitudeAt(center)
	radiusDegLat := radiusNM / math.NMPerLatitude
	radiusDegLon := radiusNM / nmPerLongitude

	// Check for longitude wrap-around (±180°)
	minLon := center.Longitude() - radiusDegLon
	maxLon := center.Longitude() + radiusDegLon
	if minLon < -180 || maxLon > 180 {
		panic(fmt.Sprintf("QueryCircle: query region spans longitude boundary (center=%v, radius=%fnm, lon range=[%f, %f])",
			center, radiusNM, minLon, maxLon))
	}

	// Compute bounding box in grid cells
	centerCell := sg.cellForPoint(center)
	cellRadiusLat := int(math.Ceil(radiusDegLat/sg.CellSize)) + 1 // +1 for safety margin
	cellRadiusLon := int(math.Ceil(radiusDegLon/sg.CellSize)) + 1

	return func(yield func(PointRef) bool) {
		// Iterate over all grid cells that could contain points within radius
		for latCell := centerCell.LatCell - cellRadiusLat; latCell <= centerCell.LatCell+cellRadiusLat; latCell++ {
			for lonCell := centerCell.LonCell - cellRadiusLon; lonCell <= centerCell.LonCell+cellRadiusLon; lonCell++ {
				cell := GridCell{LatCell: latCell, LonCell: lonCell}
				if points, ok := sg.Cells[cell]; ok {
					// Check each point in this cell
					for _, ref := range points {
						if d := math.NMDistance2LLFast(center, ref.Location, nmPerLongitude); d <= radiusNM {
							if !yield(ref) {
								return
							}
						}
					}
				}
			}
		}
	}
}

// PointCount returns the total number of points indexed in the grid.
func (sg *Grid) PointCount() int {
	total := 0
	for _, points := range sg.Cells {
		total += len(points)
	}
	return total
}

// Merge merges another grid into this grid by appending all points from the other grid's cells.
func (sg *Grid) Merge(other *Grid) {
	for cell, points := range other.Cells {
		sg.Cells[cell] = append(sg.Cells[cell], points...)
	}
}

// buildGridFromGRIB2 constructs a grid index from GRIB2 records.
// This index can be reused across multiple TRACON queries.
func buildGridFromGRIB2(records []*squall.GRIB2) *Grid {
	numWorkers := *nWorkers
	if numWorkers > len(records) {
		numWorkers = len(records)
	}

	// Partition records across workers
	recordsPerWorker := (len(records) + numWorkers - 1) / numWorkers

	partialGrids := make([]*Grid, numWorkers)
	var eg errgroup.Group

	for i := range numWorkers {
		eg.Go(func() error {
			grid := NewGrid(0.5) // 0.5 degrees ~ 30-35nm at mid-latitudes
			start := i * recordsPerWorker
			end := min(start+recordsPerWorker, len(records))

			for recIdx := start; recIdx < end; recIdx++ {
				record := records[recIdx]
				for ptIdx := range record.NumPoints {
					// Skip missing values
					if squall.IsMissing(record.Data[ptIdx]) {
						continue
					}

					lon := record.Longitudes[ptIdx]
					if lon > 180 {
						lon -= 360
					}
					pt := math.Point2LL{lon, record.Latitudes[ptIdx]}

					grid.AddPoint(pt, recIdx, ptIdx)
				}
			}

			partialGrids[i] = grid
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		panic(err) // shouldn't happen in grid building
	}

	// Merge all partial grids
	finalGrid := partialGrids[0]
	for i := 1; i < len(partialGrids); i++ {
		finalGrid.Merge(partialGrids[i])
	}

	LogInfo("Built grid: %d cells, %d points", len(finalGrid.Cells), finalGrid.PointCount())
	return finalGrid
}

// validateGridForTracon validates that QueryCircle returns exactly the
// same points as an exhaustive search over all points in the records.
func validateGridForTracon(grid *Grid, records []*squall.GRIB2, tracon string) error {
	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		return fmt.Errorf("%s: unable to find bounds for TRACON", tracon)
	}
	center, radius := tspec.Center(), tspec.Radius
	nmPerLongitude := math.NMPerLongitudeAt(center)

	// Exhaustive approach: collect all points within radius
	exhaustivePoints := make(map[PointRef]bool)
	for recIdx, record := range records {
		for ptIdx := range record.NumPoints {
			if squall.IsMissing(record.Data[ptIdx]) {
				continue
			}

			lon := record.Longitudes[ptIdx]
			if lon > 180 {
				lon -= 360
			}
			pt := math.Point2LL{lon, record.Latitudes[ptIdx]}

			if d := math.NMDistance2LLFast(center, pt, nmPerLongitude); d <= radius {
				exhaustivePoints[PointRef{RecordIdx: recIdx, PointIdx: ptIdx, Location: pt}] = true
			}
		}
	}

	// Grid approach: collect all points from QueryCircle
	gridPoints := make(map[PointRef]bool)
	for ref := range grid.QueryCircle(center, radius) {
		gridPoints[ref] = true
	}

	// Compare the two sets
	if len(exhaustivePoints) != len(gridPoints) {
		return fmt.Errorf("%s: point count mismatch - exhaustive: %d, grid: %d",
			tracon, len(exhaustivePoints), len(gridPoints))
	}

	// Check that all exhaustive points are in grid results
	for pt := range exhaustivePoints {
		if !gridPoints[pt] {
			return fmt.Errorf("%s: exhaustive point missing from grid: %+v", tracon, pt)
		}
	}

	// Check that all grid points are in exhaustive results
	for pt := range gridPoints {
		if !exhaustivePoints[pt] {
			return fmt.Errorf("%s: grid point not in exhaustive results: %+v", tracon, pt)
		}
	}

	LogInfo("%s: validation passed - %d points match exactly", tracon, len(exhaustivePoints))
	return nil
}

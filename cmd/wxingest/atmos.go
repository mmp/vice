package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
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

// artccAtmosDownsampleRate computes the spatial downsampling rate for ARTCC
// atmospheric data based on the coverage radius. The rate scales with radius²
// (area) to target approximately 4MB output file size regardless of ARTCC size.
const artccBaseRadius = 330.0 // radius (nm) at which rate=4 produces ~4MB output

func artccAtmosDownsampleRate(radius float32) int {
	// rate = 4 * (radius / baseRadius)²
	ratio := float64(radius) / artccBaseRadius
	rate := int(4*ratio*ratio + 0.5) // round to nearest int
	if rate < 1 {
		rate = 1
	}
	return rate
}

func facilityRegion(facilityID string) string {
	if facilityID == "A11" || facilityID == "FAI" || facilityID == "ZAN" {
		return "alaska"
	}
	if facilityID == "ZHN" {
		return "hawaii"
	}
	return "conus"
}

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

	// If --single-time is specified, skip all the time interval calculations
	// and just process that one time for all facilities
	if *singleTime != "" {
		return ingestHRRRSingleTime(sb, hrrrsb)
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

	tfr := util.MakeTempFileRegistry(nil)
	defer tfr.RemoveAll()
	registerCleanup(tfr.RemoveAll)

	existing := listIngestedAtmos(sb)

	type timeWithMissing struct {
		t       time.Time
		missing []string
	}
	tCh := make(chan timeWithMissing)
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

				// Check if we already have data for all facilities at this time
				facilities := existing[t] // may be empty
				slices.Sort(facilities)
				var missing []string
				for _, tracon := range wx.AtmosTRACONs {
					if !slices.Contains(facilities, tracon) {
						missing = append(missing, tracon)
					}
				}
				for _, artcc := range wx.AtmosARTCCs {
					if !slices.Contains(facilities, artcc) {
						missing = append(missing, artcc)
					}
				}

				// Alaska HRRR data is only available every 3 hours (00Z, 03Z, 06Z, etc.)
				// Filter out Alaska/Hawaii facilities if this time doesn't align with their schedule
				if t.Hour()%3 != 0 {
					missing = util.FilterSlice(missing, func(facility string) bool {
						return facilityRegion(facility) == "conus"
					})
				}

				if len(missing) > 0 {
					LogInfo(fmt.Sprintf("Time %s: missing atmos for %s\n", t, strings.Join(missing, ", ")))

					select {
					case tCh <- timeWithMissing{t: t, missing: missing}:
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
		path             string
		t                time.Time
		region           string
		targetFacilities []string
	}
	hrrrCh := make(chan downloadedHRRR, 1) // buffer 1 to have the next one prefetched.
	eg.Go(func() error {
		// Download HRRR files in a goroutine so that we can start
		// downloading the next one after the one currently being
		// processed.
		defer close(hrrrCh)
		for tw := range tCh {
			// Group missing facilities by region
			byRegion := make(map[string][]string)
			for _, facility := range tw.missing {
				region := facilityRegion(facility)
				byRegion[region] = append(byRegion[region], facility)
			}

			// Process each region sequentially (memory constraint: one GRIB2 at a time)
			for region, facilities := range byRegion {
				path, err := downloadHRRRForTime(tw.t, region, tfr, hrrrsb)
				if err != nil {
					LogError("%v", err)
				} else {
					select {
					case hrrrCh <- downloadedHRRR{path: path, t: tw.t, region: region, targetFacilities: facilities}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		return nil
	})

	eg.Go(func() error {
		for hrrr := range hrrrCh {
			LogInfo("Starting work on %s (%s region)", hrrr.t.Format(time.RFC3339), hrrr.region)
			if err := ingestHRRRForTime(hrrr.path, hrrr.t, hrrr.targetFacilities, sb); err != nil {
				LogError("%s %s: %v", hrrr.t.Format(time.RFC3339), hrrr.region, err)
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
	raw := manifest.RawManifest()
	var n int64
	err = retry(3, 10*time.Second, func() error {
		var err error
		n, err = sb.StoreObject(manifestPath, raw)
		return err
	})
	if err != nil {
		localFile := "atmos-manifest.msgpack.zst"
		if localErr := storeObjectLocal(localFile, raw); localErr != nil {
			LogError("MANIFEST WRITE FAILED for atmos and local save also failed: upload: %v, local: %v", err, localErr)
		} else {
			LogError("MANIFEST WRITE FAILED for atmos: %v -- saved to %s; upload to gs://vice-wx/%s", err, localFile, manifestPath)
		}
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

func downloadHRRRForTime(t time.Time, region string, tfr *util.TempFileRegistry, hrrrsb StorageBackend) (string, error) {
	// Check disk space before downloading
	if err := checkDiskSpace(".", 2); err != nil {
		return "", err
	}

	// Download the grib2 file from the NOAA archive
	var hrrrpath string
	if region == "alaska" {
		hrrrpath = fmt.Sprintf("hrrr.%d%02d%02d/alaska/hrrr.t%02dz.wrfprsf00.ak.grib2", t.Year(), t.Month(), t.Day(), t.Hour())
	} else {
		hrrrpath = fmt.Sprintf("hrrr.%d%02d%02d/conus/hrrr.t%02dz.wrfprsf00.grib2", t.Year(), t.Month(), t.Day(), t.Hour())
	}

	localPath := fmt.Sprintf("%s-%s.grib2", t.Format(time.RFC3339), region)

	hrrrr, err := hrrrsb.OpenRead(hrrrpath)
	if err != nil {
		return "", err
	}
	defer hrrrr.Close()

	hf, err := os.Create(localPath)
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

func ingestHRRRForTime(gribPath string, t time.Time, targetFacilities []string, sb StorageBackend) error {
	defer func() { _ = os.Remove(gribPath) }()

	records, err := parseAndFilterGRIB2(gribPath)
	if err != nil {
		return err
	}

	// Build grid once for all facilities
	grid := buildGridFromGRIB2(records)

	var eg errgroup.Group
	var totalUploads, totalUploadBytes int64
	sem := make(chan struct{}, *nWorkers)

	for _, facilityID := range targetFacilities {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			n, err := ingestHRRRForFacility(grid, records, facilityID, t, sb)
			if err != nil {
				LogError("%s-%s: %v", facilityID, t.Format(time.RFC3339), err)
				return nil
			}

			LogInfo("Uploaded %s for %s-%s", util.ByteCount(n), facilityID, t.Format(time.RFC3339))
			atomic.AddInt64(&totalUploads, 1)
			atomic.AddInt64(&totalUploadBytes, n)
			return nil
		})
	}

	eg.Wait()
	return nil
}

func ingestHRRRForFacility(grid *Grid, records []*squall.GRIB2, facilityID string, t time.Time, sb StorageBackend) (int64, error) {
	sf, err := sampleFieldFromGRIB2(grid, records, facilityID)
	if err != nil {
		return 0, fmt.Errorf("%s-%s: GRIB2 parsing failed: %w", facilityID, t.Format(time.RFC3339), err)
	}
	return uploadWeatherAtmos(sf, facilityID, t, sb)
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

func sampleFieldFromGRIB2(grid *Grid, records []*squall.GRIB2, facilityID string) (*wx.AtmosByPoint, error) {
	fac, ok := av.DB.LookupFacility(facilityID)
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for facility", facilityID)
	}
	center, radius := fac.Center(), fac.Radius

	_, isARTCC := av.DB.ARTCCs[facilityID]

	var pointRefIter iter.Seq[PointRef]
	if !isARTCC {
		// TRACON: all points in range
		pointRefIter = grid.QueryCircle(center, radius)
	} else {
		// ARTCC: downsample
		allPointRefs := slices.Collect(grid.QueryCircle(center, radius))

		// Dedupe locations (the same location appears once per GRIB2 parameter)
		locationSet := make(map[math.Point2LL]bool)
		for _, pr := range allPointRefs {
			locationSet[pr.Location] = true
		}
		locationList := slices.Collect(maps.Keys(locationSet))

		// Select well-distributed subset
		rate := artccAtmosDownsampleRate(radius)
		targetCount := max(1, len(locationList)/rate)
		selectedLocations := math.SelectDistributedPoints(locationList, targetCount)

		LogInfo("%s: KD-tree selected %d of %d locations (rate=%d)",
			facilityID, len(selectedLocations), len(locationList), rate)

		pointRefIter = func(yield func(PointRef) bool) {
			for _, ref := range allPointRefs {
				if _, ok := selectedLocations[ref.Location]; ok {
					if !yield(ref) {
						return
					}
				}
			}
		}
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
	processedPoints := 0

	// Pass 2 (or single pass for TRACONs): gather data for selected locations
	for ref := range pointRefIter {
		processedPoints++

		record := records[ref.RecordIdx()]
		value := record.Data[ref.PointIdx()]

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
		LogInfo("GRIB2 %s: processed %d points -> %d unique locations",
			facilityID, processedPoints, len(at.SampleStacks))
	}

	return &at, nil
}

func uploadWeatherAtmos(at *wx.AtmosByPoint, facilityID string, t time.Time, st StorageBackend) (int64, error) {
	soa, err := at.ToSOA()
	if err != nil {
		return 0, err
	}
	if err := wx.CheckAtmosConversion(*at, soa); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("atmos/%s/%s.msgpack.zst", facilityID, t.Format(time.RFC3339))

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
	RecordPointIdx uint32        // high 8 bits for the record index, low 24 for for the point index
	Location       math.Point2LL // cached location for quick access
}

func MakePointRef(p math.Point2LL, recordIdx int, pointIdx int) PointRef {
	if recordIdx > 255 {
		panic(fmt.Sprintf("recordIdx %d > 255", recordIdx))
	}
	if pointIdx >= 1<<24 {
		panic(fmt.Sprintf("pointIdx %d >= 1<<24", pointIdx))
	}
	return PointRef{
		RecordPointIdx: uint32(recordIdx)<<24 | uint32(pointIdx),
		Location:       p,
	}
}

func (p PointRef) RecordIdx() int {
	return int(p.RecordPointIdx >> 24)
}

func (p PointRef) PointIdx() int {
	return int(p.RecordPointIdx & ((1 << 24) - 1))
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
	sg.Cells[cell] = append(sg.Cells[cell], MakePointRef(p, recordIdx, pointIdx))
}

// QueryCircle returns an iterator over all points within radiusNM of center.
// This performs a coarse grid-based filter followed by precise distance checking.
// Handles the longitude wrap-around at ±180° by querying both sides of the boundary.
func (sg *Grid) QueryCircle(center math.Point2LL, radiusNM float32) iter.Seq[PointRef] {
	// Convert radius from nautical miles to degrees for grid cell calculation
	nmPerLongitude := math.NMPerLongitudeAt(center)
	radiusDegLat := radiusNM / math.NMPerLatitude
	radiusDegLon := radiusNM / nmPerLongitude

	// Check for longitude wrap-around (±180°)
	minLon := center.Longitude() - radiusDegLon
	maxLon := center.Longitude() + radiusDegLon
	wrapAround := minLon < -180 || maxLon > 180

	// Compute bounding box in grid cells
	centerCell := sg.cellForPoint(center)
	cellRadiusLat := int(math.Ceil(radiusDegLat/sg.CellSize)) + 1 // +1 for safety margin
	cellRadiusLon := int(math.Ceil(radiusDegLon/sg.CellSize)) + 1

	// Determine longitude cell ranges to query. Normally just one range,
	// but if we wrap around ±180°, we need to query two separate ranges.
	type lonRange struct{ min, max int }
	var lonRanges []lonRange

	if minLon < -180 {
		// Wraps past -180°: query from wrapped portion (near +180°) and from -180° to maxLon
		wrappedMinLon := minLon + 360 // e.g., -181 -> +179
		lonRanges = []lonRange{
			{int(math.Floor(wrappedMinLon / sg.CellSize)), int(math.Floor(180 / sg.CellSize))},
			{int(math.Floor(-180 / sg.CellSize)), centerCell.LonCell + cellRadiusLon},
		}
	} else if maxLon > 180 {
		// Wraps past +180°: query from minLon to +180° and from -180° to wrapped portion
		wrappedMaxLon := maxLon - 360 // e.g., +181 -> -179
		lonRanges = []lonRange{
			{centerCell.LonCell - cellRadiusLon, int(math.Floor(180 / sg.CellSize))},
			{int(math.Floor(-180 / sg.CellSize)), int(math.Floor(wrappedMaxLon / sg.CellSize))},
		}
	} else {
		// No wrap-around, single range
		lonRanges = []lonRange{
			{centerCell.LonCell - cellRadiusLon, centerCell.LonCell + cellRadiusLon},
		}
	}

	// Choose distance function: use accurate Haversine for wrap-around cases
	// (it handles the date line correctly), fast approximation otherwise.
	distanceFunc := func(ref PointRef) float32 {
		if wrapAround {
			return math.NMDistance2LL(center, ref.Location)
		}
		return math.NMDistance2LLFast(center, ref.Location, nmPerLongitude)
	}

	return func(yield func(PointRef) bool) {
		// Iterate over all grid cells that could contain points within radius
		for latCell := centerCell.LatCell - cellRadiusLat; latCell <= centerCell.LatCell+cellRadiusLat; latCell++ {
			for _, lr := range lonRanges {
				for lonCell := lr.min; lonCell <= lr.max; lonCell++ {
					cell := GridCell{LatCell: latCell, LonCell: lonCell}
					if points, ok := sg.Cells[cell]; ok {
						// Check each point in this cell
						for _, ref := range points {
							if distanceFunc(ref) <= radiusNM {
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
}

// PointCount returns the total number of points indexed in the grid.
func (sg *Grid) PointCount() int {
	total := 0
	for _, points := range sg.Cells {
		total += len(points)
	}
	return total
}

// MergeAll merges multiple grids into a single grid with preallocated slices.
// This is more efficient than sequential merges as it counts total points per cell
// first, then allocates exactly the right amount of space.
func MergeAll(grids []*Grid) *Grid {
	if len(grids) == 0 {
		return NewGrid(0.5)
	} else if len(grids) == 1 {
		return grids[0]
	}

	cellCounts := make(map[GridCell]int)
	for _, grid := range grids {
		for cell, points := range grid.Cells {
			cellCounts[cell] += len(points)
		}
	}

	result := NewGrid(grids[0].CellSize)
	for i, grid := range grids {
		for cell, points := range grid.Cells {
			if result.Cells[cell] == nil {
				result.Cells[cell] = make([]PointRef, 0, cellCounts[cell])
			}
			result.Cells[cell] = append(result.Cells[cell], points...)
		}
		grids[i] = nil // throw the GC a bone
	}

	return result
}

// buildGridFromGRIB2 constructs a grid index from GRIB2 records.
// This index can be reused across multiple TRACON queries.
func buildGridFromGRIB2(records []*squall.GRIB2) *Grid {
	numWorkers := min(*nWorkers, len(records))

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

	// Merge all partial grids at once with preallocation
	finalGrid := MergeAll(partialGrids)

	LogInfo("Built grid: %d cells, %d points", len(finalGrid.Cells), finalGrid.PointCount())
	return finalGrid
}

// ingestHRRRSingleTime processes HRRR data for a single specified time.
// This is useful for testing and evaluating runtime/file sizes.
func ingestHRRRSingleTime(sb StorageBackend, hrrrsb *TrackingBackend) error {
	t, err := time.Parse(time.RFC3339, *singleTime)
	if err != nil {
		return fmt.Errorf("failed to parse --single-time %q: %w", *singleTime, err)
	}
	t = t.UTC()
	LogInfo("Processing single time: %s", t.Format(time.RFC3339))

	startTime := time.Now()

	tfr := util.MakeTempFileRegistry(nil)
	defer tfr.RemoveAll()
	registerCleanup(tfr.RemoveAll)

	// Collect all facilities by region
	byRegion := make(map[string][]string)
	for _, tracon := range wx.AtmosTRACONs {
		region := facilityRegion(tracon)
		byRegion[region] = append(byRegion[region], tracon)
	}
	for _, artcc := range wx.AtmosARTCCs {
		region := facilityRegion(artcc)
		byRegion[region] = append(byRegion[region], artcc)
	}

	// Filter out Alaska/Hawaii facilities if time doesn't align with their 3-hour schedule
	if t.Hour()%3 != 0 {
		LogInfo("Time %s is not on 3-hour boundary - skipping Alaska/Hawaii facilities", t.Format(time.RFC3339))
		delete(byRegion, "alaska")
		delete(byRegion, "hawaii")
	}

	LogInfo("Facilities by region: conus=%d, alaska=%d, hawaii=%d",
		len(byRegion["conus"]), len(byRegion["alaska"]), len(byRegion["hawaii"]))

	// Process each region
	for region, facilities := range byRegion {
		LogInfo("Processing %s region with %d facilities", region, len(facilities))

		path, err := downloadHRRRForTime(t, region, tfr, hrrrsb)
		if err != nil {
			LogError("Failed to download HRRR for %s: %v", region, err)
			continue
		}

		if err := ingestHRRRForTime(path, t, facilities, sb); err != nil {
			LogError("Failed to ingest HRRR for %s: %v", region, err)
			continue
		}
	}

	elapsed := time.Since(startTime)
	LogInfo("Single-time processing completed in %s", elapsed)

	// Merge HRRR transfer statistics into main backend
	if mainTB, ok := sb.(*TrackingBackend); ok {
		mainTB.MergeStats(hrrrsb)
	}

	return nil
}

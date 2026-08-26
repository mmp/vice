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
	"github.com/mmp/squall/product"
	"golang.org/x/sync/errgroup"
)

// artccAtmosDownsampleRate computes the spatial downsampling rate for ARTCC
// atmospheric data based on the coverage radius. The rate scales with radius²
// (area) to target approximately 4MB output file size regardless of ARTCC size.
const artccBaseRadius = 330.0 // radius (nm) at which rate=4 produces ~4MB output

func artccAtmosDownsampleRate(radius float32) int {
	// rate = 4 * (radius / baseRadius)²
	ratio := float64(radius) / artccBaseRadius
	rate := max(
		// round to nearest int
		int(4*ratio*ratio+0.5), 1)
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
	var raw wx.RawManifest
	if err := sb.ReadObject(wx.ManifestPath("metar"), &raw); err != nil {
		return nil, fmt.Errorf("reading METAR manifest (create it with \"wxingest -manifests-only metar\" if missing): %w", err)
	}

	// KPHL is used as the reference for when METAR data is available.
	times, ok := wx.MakeManifest(raw).GetTimestamps("KPHL")
	if !ok {
		return nil, errors.New("KPHL missing from METAR manifest")
	}

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
	if *manifestsOnly {
		return generateAtmosManifest(sb)
	}

	// Read this before the chdir below, which would leave a relative path
	// resolving against the temporary directory.
	fac, err := wx.ReadFacilities(*facilitiesFile)
	if err != nil {
		return err
	}

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
		return ingestHRRRSingleTime(sb, hrrrsb, fac)
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

				// Hours are partitioned across Cloud Run job tasks.
				if !shardOwns(t.Format(time.RFC3339)) {
					continue
				}

				// Check if we already have data for all facilities at this time
				facilities := existing[t] // may be empty
				slices.Sort(facilities)
				var missing []string
				for _, tracon := range fac.TRACONs {
					if !slices.Contains(facilities, tracon) {
						missing = append(missing, tracon)
					}
				}
				// ARTCC precip data is only available starting Feb 1, 2026;
				// skip ARTCC atmos ingest before then.
				artccAtmosStart := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
				if !t.Before(artccAtmosStart) {
					for _, artcc := range fac.ARTCCs {
						if !slices.Contains(facilities, artcc) {
							missing = append(missing, artcc)
						}
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

	if inCloudRunJob() {
		// Manifest generation is deferred to a single -manifests-only
		// execution after all of the job's tasks complete.
		return nil
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

	return storeManifest(sb, manifest, wx.ManifestPath("atmos"), "atmos-manifest.msgpack.zst")
}

// rollupAtmosSeries gathers each facility's hourly averaged profiles into a
// single object holding all of them. The per-hour objects exist because atmos
// ingest is sharded by hour and a task can only write the hours it owns, but
// nothing downstream wants them one at a time: fetching a few hundred
// thousand ~850 byte objects from outside GCP costs about 120ms of latency
// each, while in here it is a short in-region pass.
func rollupAtmosSeries(sb StorageBackend) error {
	facilities := slices.Concat(slices.Sorted(maps.Keys(av.DB.TRACONs)), slices.Sorted(maps.Keys(av.DB.ARTCCs)))

	var eg errgroup.Group
	eg.SetLimit(4) // each facility fans out to *nWorkers reads of its own
	for _, facilityID := range facilities {
		// Facilities are partitioned across Cloud Run job tasks.
		if !shardOwns(facilityID) {
			continue
		}
		eg.Go(func() error { return storeAtmosSeries(facilityID, sb) })
	}
	return eg.Wait()
}

func storeAtmosSeries(facilityID string, sb StorageBackend) error {
	prefix := "atmos-avg/" + facilityID + "/"
	objects, err := sb.List(prefix)
	if err != nil {
		return fmt.Errorf("listing %s: %w", prefix, err)
	}
	if len(objects) == 0 {
		return nil
	}

	paths := slices.Sorted(maps.Keys(objects))
	times := make([]time.Time, len(paths))
	stacks := make([]*wx.AtmosSampleStack, len(paths))

	var eg errgroup.Group
	eg.SetLimit(*nWorkers)
	for i, path := range paths {
		eg.Go(func() error {
			_, ts, err := wx.ParseWeatherObjectPath(strings.TrimPrefix(path, "atmos-avg/"))
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}

			var stack wx.AtmosSampleStack
			if err := sb.ReadObject(path, &stack); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}

			times[i], stacks[i] = time.Unix(ts, 0).UTC(), &stack
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	at := wx.AtmosByTime{SampleStacks: make(map[time.Time]*wx.AtmosSampleStack, len(paths))}
	for i, t := range times {
		at.SampleStacks[t] = stacks[i]
	}

	soa, err := at.ToSOA()
	if err != nil {
		return err
	}

	n, err := sb.StoreObject(wx.AtmosSeriesPath(facilityID), soa)
	if err != nil {
		return err
	}

	LogInfo("%s: rolled up %d profiles into %s", facilityID, len(at.SampleStacks), util.ByteCount(n))
	return nil
}

// backfillAtmosAvg writes the averaged profile for any grid that is missing
// one. Ingest writes both together, so in steady state this finds nothing;
// it exists to repair partial uploads and to populate the averages for the
// grids that predate them.
func backfillAtmosAvg(sb StorageBackend) error {
	grids, err := sb.List("atmos/")
	if err != nil {
		return fmt.Errorf("listing atmos/: %w", err)
	}
	avgs, err := sb.List("atmos-avg/")
	if err != nil {
		return fmt.Errorf("listing atmos-avg/: %w", err)
	}

	var total int
	var missing []string
	for path := range grids {
		if strings.Contains(path, "manifest") {
			continue
		}
		total++
		if _, ok := avgs["atmos-avg/"+strings.TrimPrefix(path, "atmos/")]; ok {
			continue
		}
		// Grids are partitioned across Cloud Run job tasks.
		if shardOwns(path) {
			missing = append(missing, path)
		}
	}
	slices.Sort(missing)

	LogInfo("%d of %d atmos grids need averaging", len(missing), total)
	if len(missing) == 0 {
		return nil
	}

	var done, failed atomic.Int64
	var eg errgroup.Group
	eg.SetLimit(*nWorkers)
	for _, path := range missing {
		eg.Go(func() error {
			if err := storeAtmosAvgForGrid(path, sb); err != nil {
				LogError("%s: %v", path, err)
				failed.Add(1)
			}
			if n := done.Add(1); n%1000 == 0 {
				LogInfo("Averaged %d of %d atmos grids", n, len(missing))
			}
			return nil
		})
	}
	eg.Wait()

	if n := failed.Load(); n > 0 {
		return fmt.Errorf("failed to average %d of %d atmos grids", n, len(missing))
	}
	LogInfo("Averaged %d atmos grids", len(missing))
	return nil
}

func storeAtmosAvgForGrid(path string, sb StorageBackend) error {
	facilityID, ts, err := wx.ParseWeatherObjectPath(strings.TrimPrefix(path, "atmos/"))
	if err != nil {
		return err
	}

	var soa wx.AtmosByPointSOA
	if err := sb.ReadObject(path, &soa); err != nil {
		return err
	}

	_, err = storeAtmosAvg(soa, facilityID, time.Unix(ts, 0).UTC(), sb)
	return err
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
	grid, err := buildGridFromGRIB2(records)
	if err != nil {
		return err
	}

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

// keepHRRRMessage is a squall read filter that selects the parameters and
// isobaric levels vice uses: UGRD, VGRD, TMP, DPT, and HGT at the levels
// that wx.LevelIndexFromId recognizes. Filtering at this stage means the
// other records--the majority of the file--are never decoded.
func keepHRRRMessage(msg *squall.Message) bool {
	if msg.Section0 == nil || msg.Section4 == nil || msg.Section4.Product == nil {
		return false
	}

	p := squall.ParameterID{
		Discipline: msg.Section0.Discipline,
		Category:   msg.Section4.Product.GetParameterCategory(),
		Number:     msg.Section4.Product.GetParameterNumber(),
	}
	switch p.ShortName() {
	case "UGRD", "VGRD", "TMP", "DPT", "HGT":
	default:
		return false
	}

	t, ok := msg.Section4.Product.(*product.Template40)
	if !ok || t.FirstSurfaceType != 100 { // 100: isobaric surface, value in Pa
		return false
	}
	if t.SecondSurfaceType == 100 && t.SecondSurfaceValueScaled() > 0 {
		return false // layer between two isobaric surfaces
	}

	mb := t.FirstSurfaceValueScaled() / 100
	if mb == 1013.2 {
		return true
	}
	imb := int(mb)
	return float64(imb) == mb && imb >= 50 && imb <= 1000 && (imb-50)%25 == 0
}

func parseAndFilterGRIB2(gribPath string) ([]*squall.GRIB2, error) {
	f, err := os.Open(gribPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open GRIB2 file: %w", err)
	}
	defer f.Close()

	records, err := squall.ReadWithOptions(f, squall.WithFilter(keepHRRRMessage))
	if err != nil {
		return nil, fmt.Errorf("failed to parse GRIB2 file: %w", err)
	}

	LogInfo("%s: parsed %d records", gribPath, len(records))

	return records, nil
}

func sampleFieldFromGRIB2(grid *Grid, records []*squall.GRIB2, facilityID string) (*wx.AtmosByPoint, error) {
	fac, ok := av.DB.LookupFacility(facilityID)
	if !ok {
		return nil, fmt.Errorf("%s: unable to find bounds for facility", facilityID)
	}
	center, radius := fac.Center(), fac.Radius

	_, isARTCC := av.DB.ARTCCs[facilityID]

	// Collect matching points (now unique — one entry per grid location).
	matchingRefs := slices.Collect(grid.QueryCircle(center, radius))

	if isARTCC {
		// Downsample for ARTCCs: stratify over square cells of the regular
		// HRRR grid, keeping the first point seen in each. A cell holds
		// ~rate grid points, so ~len/rate spatially uniform points remain.
		rate := artccAtmosDownsampleRate(radius)
		cellSize := math.Sqrt(float32(rate))
		seen := make(map[[2]int]bool)
		total := len(matchingRefs)
		matchingRefs = util.FilterSlice(matchingRefs, func(ref PointRef) bool {
			i, j := int(ref.PointIdx)%grid.Ni, int(ref.PointIdx)/grid.Ni
			cell := [2]int{int(float32(i) / cellSize), int(float32(j) / cellSize)}
			if seen[cell] {
				return false
			}
			seen[cell] = true
			return true
		})

		LogInfo("%s: downsampling kept %d of %d locations (rate=%d)",
			facilityID, len(matchingRefs), total, rate)
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

	// Pre-allocate stacks for all matching locations, also keeping them in a
	// slice parallel to matchingRefs to avoid a map lookup per (record, point)
	// in the loop below.
	stacks := make([]*wx.AtmosSampleStack, len(matchingRefs))
	for i, ref := range matchingRefs {
		stacks[i] = allocStack()
		at.SampleStacks[ref.Location] = stacks[i]
	}

	// Record-major iteration: for each record, fill in matching points.
	for _, record := range records {
		levelIndex := wx.LevelIndexFromId([]byte(record.Level))
		if levelIndex == -1 {
			return nil, fmt.Errorf("GRIB2: param=%s, level=%q -> invalid levelIndex", record.Parameter.ShortName(), record.Level)
		}

		var set func(s *wx.AtmosSample, v float32)
		switch record.Parameter.ShortName() {
		case "UGRD":
			set = func(s *wx.AtmosSample, v float32) { s.UComponent = v }
		case "VGRD":
			set = func(s *wx.AtmosSample, v float32) { s.VComponent = v }
		case "TMP":
			set = func(s *wx.AtmosSample, v float32) { s.Temperature = av.MakeTemperatureFromKelvin(v) }
		case "DPT":
			set = func(s *wx.AtmosSample, v float32) { s.Dewpoint = av.MakeTemperatureFromKelvin(v) }
		case "HGT":
			set = func(s *wx.AtmosSample, v float32) { s.Height = v }
		default:
			return nil, errors.New("unexpected parameter: " + record.Parameter.ShortName())
		}

		for i, ref := range matchingRefs {
			if v := record.Data[ref.PointIdx]; !squall.IsMissing(v) {
				set(&stacks[i].Levels[levelIndex], v)
			}
		}
	}

	return &at, nil
}

func uploadWeatherAtmos(at *wx.AtmosByPoint, facilityID string, t time.Time, st StorageBackend) (int64, error) {
	if len(at.SampleStacks) == 0 {
		LogError("%s-%s: no sample stacks; skipping upload (facility likely not covered by HRRR grid)",
			facilityID, t.Format(time.RFC3339))
		return 0, nil
	}

	soa, err := at.ToSOA()
	if err != nil {
		return 0, err
	}
	clamped, err := wx.CheckAtmosConversion(*at, soa)
	if err != nil {
		return 0, err
	}
	if clamped > 0 {
		LogInfo("%s-%s: %d sub-surface height samples clamped (likely a low-pressure system)",
			facilityID, t.Format(time.RFC3339), clamped)
	}

	if *hrrrQuick {
		// skip upload
		var drb DryRunBackend
		return drb.StoreObject(wx.BuildObjectPath("atmos", facilityID, t), soa)
	}

	n, err := st.StoreObject(wx.BuildObjectPath("atmos", facilityID, t), soa)
	if err != nil {
		return n, err
	}

	// The average goes after the grid: "wxingest atmosavg" recreates an
	// average that's missing for a grid, but an average with no grid behind
	// it would be packaged for an hour that the server can't serve.
	na, err := storeAtmosAvg(soa, facilityID, t, st)
	return n + na, err
}

// storeAtmosAvg writes the facility-averaged vertical profile for a grid.
// That average is all the bundled resources/wx data needs, so storing it here
// means nothing downstream has to read the grids back to average them again.
func storeAtmosAvg(soa wx.AtmosByPointSOA, facilityID string, t time.Time, st StorageBackend) (int64, error) {
	avg := soa.Average()
	if avg == nil {
		return 0, nil
	}
	return st.StoreObject(wx.BuildObjectPath("atmos-avg", facilityID, t), avg)
}

///////////////////////////////////////////////////////////////////////////
// Grid

// GridCell represents a cell in the  grid using integer coordinates.
type GridCell struct {
	LatCell, LonCell int
}

// PointRef references a specific data point in the GRIB2 grid.
// All GRIB2 records share the same lat/lon grid, so we only need
// the point index (not a record index).
type PointRef struct {
	PointIdx uint32
	Location math.Point2LL
}

// Grid divides the lat-lon space into uniform (w.r.t. degrees) cells.
type Grid struct {
	CellSize float32                 // cell size in degrees
	Cells    map[GridCell][]PointRef // points in each cell
	Ni       int                     // width of the underlying GRIB2 grid, for recovering (i, j) from PointIdx
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

// AddPoint adds a point reference to the grid.
func (sg *Grid) AddPoint(p math.Point2LL, pointIdx int) {
	cell := sg.cellForPoint(p)
	sg.Cells[cell] = append(sg.Cells[cell], PointRef{PointIdx: uint32(pointIdx), Location: p})
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

// buildGridFromGRIB2 constructs a grid index from a single GRIB2 record.
// All HRRR records share the same lat/lon grid, so we only need to index
// one. After building the grid, Latitudes and Longitudes are nilled out on
// all records to free the coordinate arrays.
func buildGridFromGRIB2(records []*squall.GRIB2) (*Grid, error) {
	// Verify all records share the same grid. HRRR files always do, but
	// check so we don't silently produce wrong results if that changes.
	// squall shares one backing array across same-grid records, so check
	// slice identity before falling back to an element-wise comparison.
	sameGrid := func(a, b []float32) bool {
		return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0] || slices.Equal(a, b))
	}
	ref := records[0]
	for i, r := range records[1:] {
		if !sameGrid(r.Latitudes, ref.Latitudes) || !sameGrid(r.Longitudes, ref.Longitudes) {
			return nil, fmt.Errorf("GRIB2 grid mismatch: record %d has different lat/lon grid than record 0", i+1)
		}
	}

	grid := NewGrid(0.5) // 0.5 degrees ~ 30-35nm at mid-latitudes
	grid.Ni = ref.GridNi

	for ptIdx := range ref.NumPoints {
		lon := ref.Longitudes[ptIdx]
		if lon > 180 {
			lon -= 360
		}
		pt := math.Point2LL{lon, ref.Latitudes[ptIdx]}
		grid.AddPoint(pt, ptIdx)
	}

	LogInfo("Built grid: %d cells, %d points", len(grid.Cells), grid.PointCount())

	// Free lat/lon arrays on all records now that the grid holds locations.
	for _, r := range records {
		r.Latitudes = nil
		r.Longitudes = nil
	}

	return grid, nil
}

// ingestHRRRSingleTime processes HRRR data for a single specified time.
// This is useful for testing and evaluating runtime/file sizes.
func ingestHRRRSingleTime(sb StorageBackend, hrrrsb *TrackingBackend, fac wx.Facilities) error {
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
	for _, facility := range slices.Concat(fac.TRACONs, fac.ARTCCs) {
		region := facilityRegion(facility)
		byRegion[region] = append(byRegion[region], facility)
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

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/gob"
	"fmt"
	"image/png"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"golang.org/x/sync/errgroup"
)

func ingestPrecip(sb StorageBackend) error {
	if *manifestsOnly {
		months, err := precipManifestMonths()
		if err != nil {
			return err
		}
		if err := generateMonthlyManifests(sb, months); err != nil {
			return err
		}
		return generateConsolidatedManifest(sb)
	}

	// Track months encountered during processing
	months := make(map[string]bool)
	var mu sync.Mutex

	eg, ctx := errgroup.WithContext(context.Background())

	ch := make(chan string)
	eg.Go(func() error {
		defer close(ch)
		return sb.ChanList(ctx, "scrape/WX", ch)
	})

	var totalBytes, totalObjects int64
	var nErrors atomic.Int64
	for range *nWorkers {
		eg.Go(func() error {
			for path := range ch {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				if !shardOwns(path) {
					continue
				}

				n, month, err := processPrecip(sb, path)
				if err != nil {
					LogError("%s: %v", path, err)
					nErrors.Add(1)
					continue
				}

				mu.Lock()
				months[month] = true
				mu.Unlock()

				nb := atomic.AddInt64(&totalBytes, n)
				nobj := atomic.AddInt64(&totalObjects, 1)
				if nobj%10000 == 0 {
					LogInfo("Processed %d WX objects so far, %s", nobj, util.ByteCount(nb))
				}
			}
			return nil
		})
	}

	err := eg.Wait()
	LogInfo("Ingested %s of WX stored in %d objects", util.ByteCount(totalBytes), totalObjects)
	if ne := nErrors.Load(); ne > 0 {
		LogError("%d objects had errors and were skipped; they remain in scrape/ for the next run", ne)
	}
	if err != nil {
		return err
	}

	if inCloudRunJob() {
		// Manifest generation is deferred to a single -manifests-only
		// execution after all of the job's tasks complete.
		return nil
	}

	if err := generateMonthlyManifests(sb, months); err != nil {
		return err
	}

	return generateConsolidatedManifest(sb)
}

// precipManifestMonths returns the months to regenerate precip manifests for
// in -manifests-only mode: the -months flag if given, otherwise the current
// and previous month, which cover everything drained from scrape/ since any
// reasonably recent run.
func precipManifestMonths() (map[string]bool, error) {
	months := make(map[string]bool)
	if *monthsFlag != "" {
		for m := range strings.SplitSeq(*monthsFlag, ",") {
			if _, err := time.Parse("2006-01", m); err != nil {
				return nil, fmt.Errorf("-months %q: %w", m, err)
			}
			months[m] = true
		}
		return months, nil
	}

	now := time.Now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	months[now.Format("2006-01")] = true
	months[first.AddDate(0, 0, -1).Format("2006-01")] = true
	return months, nil
}

func processPrecip(sb StorageBackend, path string) (int64, string, error) {
	// Parse time
	t, err := time.Parse(time.RFC3339, strings.TrimSuffix(filepath.Base(path), ".gob"))
	if err != nil {
		return 0, "", err
	}
	t = t.UTC()

	r, err := sb.OpenRead(path)
	if err != nil {
		return 0, "", err
	}
	defer r.Close()

	scraped, err := io.ReadAll(r)
	if err != nil {
		return 0, "", err
	}

	type WXScraped struct {
		PNG        []byte
		Resolution int
		Latitude   float32
		Longitude  float32
		Source     string
		NX, NY     int
		Bounds     math.Extent2D
	}
	var wxs WXScraped
	if err := gob.NewDecoder(bytes.NewReader(scraped)).Decode(&wxs); err != nil {
		return 0, "", err
	}

	img, err := png.Decode(bytes.NewReader(wxs.PNG))
	if err != nil {
		return 0, "", err
	}

	wxp := wx.Precip{
		DBZ:        util.DeltaEncode(wx.RadarImageToDBZ(img, wx.PrecipSource(wxs.Source))),
		Resolution: wxs.Resolution,
		Latitude:   wxs.Latitude,
		Longitude:  wxs.Longitude,
		NX:         wxs.NX,
		NY:         wxs.NY,
		Bounds:     wxs.Bounds,
	}

	facilityID, _, ok := strings.Cut(strings.TrimPrefix(path, "scrape/WX/"), "/")
	if !ok {
		return 0, "", fmt.Errorf("%s: unexpected format; can't find facility ID", path)
	}

	objpath := fmt.Sprintf("precip/%s/%s.msgpack.zst", facilityID, t.Format(time.RFC3339))

	n, err := sb.StoreObject(objpath, wxp)
	if err != nil {
		return 0, "", err
	}

	// Archive only if everything's worked out; a server-side copy avoids
	// re-uploading the original bytes.
	apath := filepath.Join("archive", strings.TrimPrefix(path, "scrape/"))
	if err := sb.Copy(apath, path); err != nil {
		return n, "", err
	}

	month := t.Format("2006-01")
	return n, month, sb.Delete(path)
}

func generateMonthlyManifests(sb StorageBackend, months map[string]bool) error {
	for month := range months {
		LogInfo("Generating manifest for %s", month)

		manifest := wx.NewManifest()
		var mu sync.Mutex
		sem := make(chan struct{}, 16)
		eg := errgroup.Group{}

		// Process both TRACONs and ARTCCs
		var facilities []string
		for tracon := range av.DB.TRACONs {
			facilities = append(facilities, tracon)
		}
		for artcc := range av.DB.ARTCCs {
			facilities = append(facilities, artcc)
		}

		for _, facilityID := range facilities {
			eg.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				prefix := fmt.Sprintf("precip/%s/%s-", facilityID, month)
				files, err := sb.List(prefix)
				if err != nil {
					return fmt.Errorf("failed to list files for %s: %w", prefix, err)
				}

				var timestamps []time.Time
				for path := range files {
					relativePath := strings.TrimPrefix(path, "precip/")
					if !strings.Contains(relativePath, "manifest") {
						_, ts, err := wx.ParseWeatherObjectPath(relativePath)
						if err != nil {
							LogError("%s: %v", relativePath, err)
							continue
						}
						timestamps = append(timestamps, time.Unix(ts, 0).UTC())
					}
				}

				if len(timestamps) == 0 {
					return nil
				}

				mu.Lock()
				defer mu.Unlock()

				if err := manifest.SetFacilityTimestamps(facilityID, timestamps); err != nil {
					return err
				}

				return nil
			})
		}

		if err := eg.Wait(); err != nil {
			return err
		}

		totalEntries := manifest.TotalEntries()
		LogInfo("Found %d precip objects for %s", totalEntries, month)
		if totalEntries == 0 {
			LogInfo("No files found for %s, skipping manifest", month)
			continue
		}

		localFile := fmt.Sprintf("precip-manifest-%s.msgpack.zst", month)
		_ = storeManifest(sb, manifest, wx.MonthlyManifestPath("precip", month), localFile)
	}

	return nil
}

func generateConsolidatedManifest(sb StorageBackend) error {
	LogInfo("Generating consolidated precip manifest from monthly manifests")

	// List all monthly manifest files
	manifestCh := make(chan string)
	go func() {
		defer close(manifestCh)
		if err := sb.ChanList(context.Background(), wx.MonthlyManifestPrefix("precip"), manifestCh); err != nil {
			LogError("Failed to list monthly manifests: %v", err)
		}
	}()

	// Merge timestamps from all monthly manifests
	timestamps := make(map[string][]time.Time)
	var mu sync.Mutex
	eg := errgroup.Group{}
	sem := make(chan struct{}, 16)

	for path := range manifestCh {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			var rawManifest wx.RawManifest
			if err := sb.ReadObject(path, &rawManifest); err != nil {
				return fmt.Errorf("failed to read %s: %w", path, err)
			}

			monthlyManifest := wx.MakeManifest(rawManifest)

			// Collect timestamps for each facility
			for _, facility := range monthlyManifest.Facilities() {
				times, ok := monthlyManifest.GetTimestamps(facility)
				if !ok {
					continue
				}

				mu.Lock()
				timestamps[facility] = append(timestamps[facility], times...)
				mu.Unlock()
			}

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	// Sort and set timestamps for each TRACON
	consolidated, err := wx.MakeManifestFromMap(timestamps)
	if err != nil {
		LogError("MakeManifestFromMap: %v", err)
	}

	return storeManifest(sb, consolidated, wx.ManifestPath("precip"), "precip-manifest-consolidated.msgpack.zst")
}

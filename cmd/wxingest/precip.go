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
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"golang.org/x/sync/errgroup"
)

func ingestPrecip(sb StorageBackend) error {
	// Track months encountered during processing
	months := make(map[string]bool)
	var mu sync.Mutex

	eg, ctx := errgroup.WithContext(context.Background())

	ch := make(chan string)
	eg.Go(func() error {
		defer close(ch)
		return sb.ChanList("scrape/WX", ch)
	})

	var totalBytes, totalObjects int64
	for range *nWorkers {
		eg.Go(func() error {
			for path := range ch {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				n, month, err := processPrecip(sb, path)
				if err != nil {
					return fmt.Errorf("%s: %v", path, err)
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
	if err != nil {
		return err
	}

	if err := generateMonthlyManifests(sb, months); err != nil {
		return err
	}

	return generateConsolidatedManifest(sb)
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
		DBZ:        util.DeltaEncode(wx.RadarImageToDBZ(img)),
		Resolution: wxs.Resolution,
		Latitude:   wxs.Latitude,
		Longitude:  wxs.Longitude,
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

	// Archive only if everything's worked out.
	apath := filepath.Join("archive", strings.TrimPrefix(path, "scrape/"))
	if _, err := sb.Store(apath, bytes.NewReader(scraped)); err != nil {
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

		manifestPath := wx.MonthlyManifestPath("precip", month)
		n, err := sb.StoreObject(manifestPath, manifest.RawManifest())
		if err != nil {
			return fmt.Errorf("failed to store manifest for %s: %w", month, err)
		}

		LogInfo("Stored %d items in %s (%s)", totalEntries, manifestPath, util.ByteCount(n))
	}

	return nil
}

func generateConsolidatedManifest(sb StorageBackend) error {
	LogInfo("Generating consolidated precip manifest from monthly manifests")

	// List all monthly manifest files
	manifestCh := make(chan string)
	go func() {
		defer close(manifestCh)
		if err := sb.ChanList(wx.MonthlyManifestPrefix("precip"), manifestCh); err != nil {
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

			// Collect timestamps for each TRACON
			for _, tracon := range monthlyManifest.TRACONs() {
				times, ok := monthlyManifest.GetTimestamps(tracon)
				if !ok {
					continue
				}

				mu.Lock()
				timestamps[tracon] = append(timestamps[tracon], times...)
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

	// Store consolidated manifest
	manifestPath := wx.ManifestPath("precip")
	n, err := sb.StoreObject(manifestPath, consolidated.RawManifest())
	if err != nil {
		return fmt.Errorf("failed to store consolidated manifest: %w", err)
	}

	LogInfo("Stored %d items in %s (%s) from monthly manifests", consolidated.TotalEntries(), manifestPath, util.ByteCount(n))
	return nil
}

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/gob"
	"fmt"
	"image/png"
	"io"
	"maps"
	"path/filepath"
	"slices"
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
		return generatePrecipManifest(sb)
	}

	eg, ctx := errgroup.WithContext(context.Background())

	ch := make(chan string)
	eg.Go(func() error {
		defer close(ch)
		return sb.ChanList(ctx, "scrape/WX/", ch)
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

				n, err := processPrecip(sb, path)
				if err != nil {
					LogError("%s: %v", path, err)
					nErrors.Add(1)
					continue
				}

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

	return generatePrecipManifest(sb)
}

func processPrecip(sb StorageBackend, path string) (int64, error) {
	// Parse time
	t, err := time.Parse(time.RFC3339, strings.TrimSuffix(filepath.Base(path), ".gob"))
	if err != nil {
		return 0, err
	}
	t = t.UTC()

	r, err := sb.OpenRead(path)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	scraped, err := io.ReadAll(r)
	if err != nil {
		return 0, err
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
		return 0, err
	}

	img, err := png.Decode(bytes.NewReader(wxs.PNG))
	if err != nil {
		return 0, err
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
		return 0, fmt.Errorf("%s: unexpected format; can't find facility ID", path)
	}

	objpath := fmt.Sprintf("precip/%s/%s.msgpack.zst", facilityID, t.Format(time.RFC3339))

	n, err := sb.StoreObject(objpath, wxp)
	if err != nil {
		return 0, err
	}

	// Archive only if everything's worked out; a server-side copy avoids
	// re-uploading the original bytes.
	apath := filepath.Join("archive", strings.TrimPrefix(path, "scrape/"))
	if err := sb.Copy(apath, path); err != nil {
		return n, err
	}

	return n, sb.Delete(path)
}

// generatePrecipManifest rebuilds the precip manifest from a full listing of
// the precip objects. The manifest is what makes the data reachable--it
// bounds the hours atmos ingests and the client looks up radar images through
// it--so an incremental update that missed a facility or a month would
// silently strand objects that are sitting in the bucket. A full listing is a
// few cents of Class A operations, which is cheap enough to not have to
// reason about what changed. Listing per facility rather than under precip/
// alone both fans out and keeps any one path map small.
func generatePrecipManifest(sb StorageBackend) error {
	LogInfo("Generating precip manifest")

	facilities := slices.Concat(slices.Sorted(maps.Keys(av.DB.TRACONs)), slices.Sorted(maps.Keys(av.DB.ARTCCs)))

	timestamps := make(map[string][]time.Time)
	var mu sync.Mutex

	var eg errgroup.Group
	eg.SetLimit(16)
	for _, facilityID := range facilities {
		eg.Go(func() error {
			prefix := "precip/" + facilityID + "/"
			paths, err := sb.List(prefix)
			if err != nil {
				return fmt.Errorf("failed to list %s: %w", prefix, err)
			}

			var times []time.Time
			for path := range paths {
				_, ts, err := wx.ParseWeatherObjectPath(strings.TrimPrefix(path, "precip/"))
				if err != nil {
					LogError("%s: %v", path, err)
					continue
				}
				times = append(times, time.Unix(ts, 0).UTC())
			}

			if len(times) == 0 {
				return nil
			}

			mu.Lock()
			defer mu.Unlock()
			timestamps[facilityID] = times
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	manifest, err := wx.MakeManifestFromMap(timestamps)
	if err != nil {
		return err
	}

	LogInfo("Found %d precip objects across %d facilities", manifest.TotalEntries(), len(timestamps))

	return storeManifest(sb, manifest, wx.ManifestPath("precip"), "precip-manifest.msgpack.zst")
}

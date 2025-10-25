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
	"slices"
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

	return generateMonthlyManifests(sb, months)
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

	tracon, _, ok := strings.Cut(strings.TrimPrefix(path, "scrape/WX/"), "/")
	if !ok {
		return 0, "", fmt.Errorf("%s: unexpected format; can't find TRACON", path)
	}

	objpath := fmt.Sprintf("precip/%s/%s.msgpack.zst", tracon, t.Format(time.RFC3339))

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

		var manifest []string
		var mu sync.Mutex
		sem := make(chan struct{}, 16)
		eg := errgroup.Group{}

		for tracon := range av.DB.TRACONs {
			eg.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()

				prefix := fmt.Sprintf("precip/%s/%s-", tracon, month)
				files, err := sb.List(prefix)
				if err != nil {
					return fmt.Errorf("failed to list files for %s: %w", prefix, err)
				}

				mu.Lock()
				for path := range files {
					relativePath := strings.TrimPrefix(path, "precip/")
					if !strings.Contains(relativePath, "manifest") {
						manifest = append(manifest, relativePath)
					}
				}
				mu.Unlock()

				return nil
			})
		}

		if err := eg.Wait(); err != nil {
			return err
		}
		LogInfo("Found %d precip objects", len(manifest))

		if len(manifest) == 0 {
			LogInfo("No files found for %s, skipping manifest", month)
			continue
		}

		slices.Sort(manifest)
		tm, err := util.TransposeStrings(manifest)
		if err != nil {
			return fmt.Errorf("failed to transpose manifest for %s: %w", month, err)
		}

		manifestPath := fmt.Sprintf("precip/manifest-%s.msgpack.zst", month)
		n, err := sb.StoreObject(manifestPath, tm)
		if err != nil {
			return fmt.Errorf("failed to store manifest for %s: %w", month, err)
		}

		LogInfo("Stored %d items in %s (%s)", len(manifest), manifestPath, util.ByteCount(n))
	}

	return nil
}

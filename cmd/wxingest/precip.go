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
	"sync/atomic"
	"time"

	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"golang.org/x/sync/errgroup"
)

func ingestWX(sb StorageBackend) {
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

				n, err := processWX(sb, path)
				if err != nil {
					return fmt.Errorf("%s: %v", path, err)
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

	if err := eg.Wait(); err != nil {
		LogError("WX: %v", err)
	}

	LogInfo("Ingested %s of WX stored in %d objects", util.ByteCount(totalBytes), totalObjects)
}

func processWX(sb StorageBackend, path string) (int64, error) {
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
	}
	var wxs WXScraped
	if err := gob.NewDecoder(bytes.NewReader(scraped)).Decode(&wxs); err != nil {
		return 0, err
	}

	img, err := png.Decode(bytes.NewReader(wxs.PNG))
	if err != nil {
		return 0, err
	}

	type WXProcessed struct {
		DBZ        []byte
		Resolution int
		Latitude   float32
		Longitude  float32
	}
	wxp := WXProcessed{
		DBZ:        util.DeltaEncode(wx.RadarImageToDBZ(img)),
		Resolution: wxs.Resolution,
		Latitude:   wxs.Latitude,
		Longitude:  wxs.Longitude,
	}

	tracon, _, ok := strings.Cut(strings.TrimPrefix(path, "scrape/WX/"), "/")
	if !ok {
		return 0, fmt.Errorf("%s: unexpected format; can't find TRACON", path)
	}

	objpath := fmt.Sprintf("precip/%s/%s.msgpack.zst", tracon, t.Format(time.RFC3339))

	n, err := sb.StoreObject(objpath, wxp)
	if err != nil {
		return 0, err
	}

	// Archive only if everything's worked out.
	apath := filepath.Join("archive", strings.TrimPrefix(path, "scrape/"))
	if _, err := sb.Store(apath, bytes.NewReader(scraped)); err != nil {
		return n, err
	}

	return n, sb.Delete(path)
}

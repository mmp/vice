package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"
)

func ingestTFRs(sb StorageBackend) error {
	if *manifestsOnly {
		return nil // TFRs have no manifest
	}
	if taskCount > 1 {
		return errors.New("TFR ingest must run as a single task")
	}

	lg := log.New(false, "warn", "")

	// Load both archived TFRs and the newly-scraped XMLs into memory.
	tfrs, arch, err := loadAllTFRs(sb, lg)
	if err != nil {
		return err
	}

	// Store all TFRs as a compressed blob.
	if err := storeTFRs(sb, tfrs); err != nil {
		return err
	}

	// Archive the newly-scraped XMLs and delete the originals.
	return archiveTFRs(arch, sb)
}

func loadAllTFRs(sb StorageBackend, lg *log.Logger) ([]av.TFR, []toArchive, error) {
	var tfrs []av.TFR
	var arch []toArchive
	var mu sync.Mutex // protects both
	eg, ctx := errgroup.WithContext(context.Background())

	// Load scraped XMLs
	scrapedCh := make(chan string)

	for range *nWorkers {
		eg.Go(func() error {
			for path := range scrapedCh {
				b, err := readWithRetry(sb, path)
				if err != nil {
					LogError("scrape/tfrs: %s: read: %v", path, err)
					continue
				}

				tfr, err := decodeTFRFromXML(path, b, lg)
				if err != nil {
					LogError("scrape/tfrs: %s: %v", path, err)
				}

				mu.Lock()
				if tfr != nil {
					tfrs = append(tfrs, *tfr)
				}
				arch = append(arch, toArchive{path: path, b: b})
				mu.Unlock()
			}
			return nil
		})
	}

	eg.Go(func() error {
		defer close(scrapedCh)
		return sb.ChanList(ctx, "scrape/tfrs/", scrapedCh)
	})

	// Load archived zips
	archivedPathCh := make(chan string)

	for range *nWorkers {
		eg.Go(func() error {
			for path := range archivedPathCh {
				b, err := readWithRetry(sb, path)
				if err != nil {
					LogError("archive/tfrs: %s: read: %v", path, err)
					continue
				}

				archived, err := parseTFRZip(b, path, lg)
				if err != nil {
					LogError("archive/tfrs: %s: %v", path, err)
					continue
				}
				mu.Lock()
				tfrs = append(tfrs, archived...)
				mu.Unlock()
			}
			return nil
		})
	}

	eg.Go(func() error {
		defer close(archivedPathCh)
		return sb.ChanList(ctx, "archive/tfrs/", archivedPathCh)
	})

	err := eg.Wait()

	LogInfo("Loaded %d TFRs total", len(tfrs))

	return tfrs, arch, err
}

// decodeTFRFromXML parses a TFR XML and returns nil if the TFR has no polygon points.
func decodeTFRFromXML(path string, b []byte, lg *log.Logger) (*av.TFR, error) {
	tfr, err := av.DecodeTFRXML(path, bytes.NewReader(b), lg)
	if err != nil {
		return nil, err
	}

	// Skip TFRs with no polygon points (nationwide/template TFRs).
	if len(tfr.Points) == 0 {
		return nil, nil
	}

	return &tfr, nil
}

func parseTFRZip(b []byte, path string, lg *log.Logger) ([]av.TFR, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}

	var tfrs []av.TFR
	for _, f := range zr.File {
		if f.UncompressedSize64 == 0 {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		contents, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}

		tfr, err := decodeTFRFromXML(f.Name, contents, lg)
		if err != nil {
			LogError("archive %s: %s: %v", path, f.Name, err)
			continue // skip bad entries
		}
		if tfr != nil {
			tfrs = append(tfrs, *tfr)
		}
	}

	return tfrs, nil
}

func storeTFRs(sb StorageBackend, tfrs []av.TFR) error {
	LogInfo("Storing %d TFRs", len(tfrs))

	var buf bytes.Buffer
	if err := wx.SaveCompressedTFRs(tfrs, &buf); err != nil {
		return err
	}

	n, err := sb.Store(wx.TFRFilename, &buf)
	if err == nil {
		LogInfo("Stored %s for %d TFRs", util.ByteCount(n), len(tfrs))
	}

	return err
}

func archiveTFRs(arch []toArchive, sb StorageBackend) error {
	if len(arch) == 0 {
		LogInfo("No TFR XMLs to archive")
		return nil
	}

	LogInfo("Archiving %d TFR XMLs", len(arch))

	var b bytes.Buffer
	zw := zip.NewWriter(&b)

	for _, rec := range arch {
		if w, err := zw.Create(rec.path); err != nil {
			return err
		} else if _, err := io.Copy(w, bytes.NewReader(rec.b)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}

	path := fmt.Sprintf("archive/tfrs/%s.zip", time.Now().Format(time.RFC3339))
	n, err := sb.Store(path, &b)
	if err == nil {
		LogInfo("Archived %s of scraped TFR XMLs from %d files. Deleting scraped...", util.ByteCount(n), len(arch))

		for _, rec := range arch {
			if err := sb.Delete(rec.path); err != nil {
				LogInfo("%s: %v", rec.path, err)
			}
		}
		LogInfo("Deleted %d scraped TFR XMLs", len(arch))
	}

	return err
}

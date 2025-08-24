package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"
)

// METAR from a single file
type FileMETAR struct {
	ICAO  string
	METAR []wx.BasicMETAR
}

func ingestMETAR(sb StorageBackend) {
	// Snapshot what's available at the start; this is what we will archive
	// when we're done. (Don't archive new scraped ones that landed after
	// we started ingesting.)
	scraped, err := sb.List("scrape/metar")
	if err != nil {
		LogError("%v", err)
		return
	}
	if len(scraped) == 0 {
		LogInfo("scrape/metar: no new METAR objects found. Skipping METAR ingest.")
		return
	}
	LogInfo("scrape/metar: found %d objects", len(scraped))

	// Load both archived METAR and the newly-scraped records into memory
	// and collect them by airport.
	metar, arch, err := loadAllMETAR(scraped, sb)
	if err != nil {
		LogError("%v", err)
		return
	}

	// Store per-airport METAR objects, overwriting old ones.
	if err := storeMETAR(sb, metar); err != nil {
		LogError("%v", err)
		return
	}

	// Archive the new stuff.
	if err := archiveMETAR(arch, sb); err != nil {
		LogError("%v", err)
	}
}

type toArchive struct {
	path string
	b    []byte
}

func loadAllMETAR(scraped map[string]int64, sb StorageBackend) (map[string][]FileMETAR, []toArchive, error) {
	metar := make(map[string][]FileMETAR)
	var arch []toArchive
	var mu sync.Mutex // protects both metar and arch

	var eg errgroup.Group
	sem := make(chan struct{}, *nWorkers)

	for path := range scraped {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			if fm, b, err := loadScrapedMETAR(sb, path); err != nil {
				return err
			} else {
				mu.Lock()

				if len(fm.METAR) > 0 {
					metar[fm.ICAO] = append(metar[fm.ICAO], fm)
				}

				// Add this to the list of objects to archive (if ingest is successful).
				arch = append(arch, toArchive{path: path, b: b})
				mu.Unlock()
				return nil
			}
		})
	}

	archivedPathCh := make(chan string)

	for range *nWorkers {
		eg.Go(func() error {
			for path := range archivedPathCh {
				sem <- struct{}{}
				recs, err := readZipMETAREntries(sb, path)
				<-sem

				if err != nil {
					return err
				} else {
					mu.Lock()
					for _, fm := range recs {
						if len(fm.METAR) > 0 { // skip ones for empty files; they don't have ICAO set in any case
							metar[fm.ICAO] = append(metar[fm.ICAO], fm)
						}
					}
					mu.Unlock()
				}
			}
			return nil
		})
	}

	eg.Go(func() error {
		return EnqueueObjects(sb, "archive/metar", archivedPathCh)
	})

	err := eg.Wait()

	LogInfo("Loaded all METAR")

	return metar, arch, err
}

func loadScrapedMETAR(sb StorageBackend, path string) (FileMETAR, []byte, error) {
	r, err := sb.OpenRead(path)
	if err != nil {
		return FileMETAR{}, nil, err
	}
	defer r.Close()

	b, err := io.ReadAll(r)
	if err != nil {
		return FileMETAR{}, nil, err
	}

	m, err := decodeMETAR(bytes.NewReader(b))
	return m, b, err
}

func decodeMETAR(r io.Reader) (FileMETAR, error) {
	var fm FileMETAR
	if err := json.NewDecoder(r).Decode(&fm.METAR); err != nil {
		return FileMETAR{}, err
	}
	if len(fm.METAR) == 0 {
		return FileMETAR{}, nil
	}
	fm.ICAO = fm.METAR[0].ICAO

	return fm, nil
}

func readZipMETAREntries(sb StorageBackend, path string) ([]FileMETAR, error) {
	r, err := sb.OpenRead(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	// Read the contents into a buffer so we can provide an io.ReaderAt as
	// well as total size to the zip.Reader.
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}

	var fms []FileMETAR
	for _, f := range zr.File {
		// Skip entries for directories
		if f.UncompressedSize64 == 0 {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		fm, err := decodeMETAR(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}

		fms = append(fms, fm)
	}

	return fms, nil
}

func storeMETAR(st StorageBackend, metar map[string][]FileMETAR) error {
	LogInfo("Uploading METAR for %d airports", len(metar))

	var eg errgroup.Group
	var totalBytes int64
	sem := make(chan struct{}, *nWorkers)
	for ap := range metar {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			if n, err := storeAirportMETAR(st, ap, metar[ap]); err != nil {
				return err
			} else {
				atomic.AddInt64(&totalBytes, n)
				return nil
			}
		})
	}

	err := eg.Wait()

	LogInfo("Stored %s for %d airports' METAR", util.ByteCount(totalBytes), len(metar))

	return err
}

func storeAirportMETAR(st StorageBackend, ap string, fm []FileMETAR) (int64, error) {
	// Flatten all of the METAR
	var recs []wx.BasicMETAR
	for _, m := range fm {
		recs = append(recs, m.METAR...)
	}

	// Sort by date; since the time format used is 2006-01-02 15:04:05,
	// string compare sorts them in time order.
	slices.SortFunc(recs, func(a, b wx.BasicMETAR) int { return strings.Compare(a.ReportTime, b.ReportTime) })

	// Eliminate duplicates (may happen since the scraper grabs 24-hour chunks every 16 hours.
	recs = slices.CompactFunc(recs, func(a, b wx.BasicMETAR) bool { return a.ReportTime == b.ReportTime })

	soa, err := wx.MakeBasicMETARSOA(recs)
	if err != nil {
		return 0, err
	}

	if err := wx.CheckBasicMETARSOA(soa, recs); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("metar/%s.msgpack.zstd", ap)
	return st.StoreObject(path, soa)
}

func archiveMETAR(arch []toArchive, sb StorageBackend) error {
	LogInfo("Archiving %d METAR records", len(arch))

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

	path := fmt.Sprintf("archive/metar/%s.zip", time.Now().Format(time.RFC3339))
	n, err := sb.Store(path, &b)
	if err == nil {
		LogInfo("Archived %s of scraped METAR from %d records. Deleting scraped...", util.ByteCount(n), len(arch))

		for _, rec := range arch {
			if err := sb.Delete(rec.path); err != nil {
				LogInfo("%s: %v", rec.path, err)
			}
		}
		LogInfo("Deleted %d scraped METAR records", len(arch))
	}

	return err
}

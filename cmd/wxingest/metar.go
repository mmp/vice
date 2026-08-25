package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"
)

// METAR from a single file
type FileMETAR struct {
	ICAO  string
	METAR []wx.METAR
}

func ingestMETAR(sb StorageBackend) error {
	if taskCount > 1 {
		return errors.New("METAR ingest must run as a single task")
	}

	if *manifestsOnly {
		cm, err := loadConsolidatedMETAR(sb)
		if err != nil {
			return err
		}
		return rebuildMETARManifest(sb, cm)
	}

	// Load the newly-scraped records, grouped by airport.
	scraped, arch, err := loadScrapedMETAR(sb)
	if err != nil {
		return err
	}

	// The consolidated METAR object is the merge base; if it can't be read
	// (first run or disaster recovery), rebuild it from the archive.
	cm, err := loadConsolidatedMETAR(sb)
	rebuilding := err != nil
	if rebuilding {
		LogError("%s: %v; rebuilding from archive/metar", wx.METARFilename, err)
		cm = wx.NewCompressedMETAR()
		archived, err := loadArchivedMETAR(sb)
		if err != nil {
			return err
		}
		for ap, recs := range archived {
			scraped[ap] = append(recs, scraped[ap]...)
		}
	}

	if len(scraped) == 0 && !rebuilding {
		LogInfo("No new METAR records")
		return archiveMETAR(arch, sb)
	}

	times, err := mergeMETAR(cm, scraped)
	if err != nil {
		return err
	}

	nb, err := sb.StoreObject(wx.METARFilename, cm)
	if err != nil {
		return err
	}
	LogInfo("Stored %s for %d airports' METAR", util.ByteCount(nb), cm.Len())

	if err := updateMETARManifest(sb, cm, times); err != nil {
		return err
	}

	// Archive the scraped objects and delete them only now that everything
	// else has succeeded; an earlier failure leaves them for the next run.
	return archiveMETAR(arch, sb)
}

// loadConsolidatedMETAR reads the current consolidated METAR object.
func loadConsolidatedMETAR(sb StorageBackend) (wx.CompressedMETAR, error) {
	r, err := sb.OpenRead(wx.METARFilename)
	if err != nil {
		return wx.CompressedMETAR{}, err
	}
	defer r.Close()

	return wx.LoadCompressedMETAR(r)
}

type toArchive struct {
	path string
	b    []byte
}

// loadScrapedMETAR reads all objects under scrape/metar and returns their
// records grouped by airport, along with the raw bytes for archiving.
// Objects that fail to read or decode are skipped and left in scrape/ to be
// retried on the next run.
func loadScrapedMETAR(sb StorageBackend) (map[string][]wx.METAR, []toArchive, error) {
	metar := make(map[string][]wx.METAR)
	var arch []toArchive
	var mu sync.Mutex // protects both metar and arch
	eg, ctx := errgroup.WithContext(context.Background())

	scrapedCh := make(chan string)

	for range *nWorkers {
		eg.Go(func() error {
			for path := range scrapedCh {
				b, err := readWithRetry(sb, path)
				if err != nil {
					LogError("scrape/metar: %s: read: %v", path, err)
					continue
				}

				fm, err := decodeMETAR(bytes.NewReader(b))
				if err != nil {
					LogError("scrape/metar: %s: %v", path, err)
					continue
				}

				mu.Lock()
				if len(fm.METAR) > 0 {
					metar[fm.ICAO] = append(metar[fm.ICAO], fm.METAR...)
				}
				arch = append(arch, toArchive{path: path, b: b})
				mu.Unlock()
			}
			return nil
		})
	}

	eg.Go(func() error {
		defer close(scrapedCh)
		return sb.ChanList(ctx, "scrape/metar", scrapedCh)
	})

	err := eg.Wait()

	LogInfo("Loaded %d scraped METAR objects covering %d airports", len(arch), len(metar))

	return metar, arch, err
}

// loadArchivedMETAR reads the complete METAR history from the archive/metar
// zip files; it is only used to rebuild the consolidated object from scratch,
// so any error is fatal--an incomplete rebuild would permanently lose records.
func loadArchivedMETAR(sb StorageBackend) (map[string][]wx.METAR, error) {
	metar := make(map[string][]wx.METAR)
	var mu sync.Mutex
	eg, ctx := errgroup.WithContext(context.Background())

	archivedPathCh := make(chan string)

	for range *nWorkers {
		eg.Go(func() error {
			for path := range archivedPathCh {
				b, err := readWithRetry(sb, path)
				if err != nil {
					return fmt.Errorf("archive/metar: %s: read: %w", path, err)
				}

				recs, err := parseMETARZip(b)
				if err != nil {
					return fmt.Errorf("archive/metar: %s: %w", path, err)
				}

				mu.Lock()
				for _, fm := range recs {
					if len(fm.METAR) > 0 { // skip ones for empty files; they don't have ICAO set in any case
						metar[fm.ICAO] = append(metar[fm.ICAO], fm.METAR...)
					}
				}
				mu.Unlock()
			}
			return nil
		})
	}

	eg.Go(func() error {
		defer close(archivedPathCh)
		return sb.ChanList(ctx, "archive/metar", archivedPathCh)
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	LogInfo("Loaded archived METAR for %d airports", len(metar))

	return metar, nil
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

func parseMETARZip(b []byte) ([]FileMETAR, error) {
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

type metarUpdate struct {
	blob    []byte
	times   []time.Time
	dropped int
	err     error
}

// mergeMETAR merges the given new records into cm and returns the full,
// merged observation times of each updated airport for the manifest.
func mergeMETAR(cm wx.CompressedMETAR, scraped map[string][]wx.METAR) (map[string][]time.Time, error) {
	LogInfo("Merging METAR for %d airports", len(scraped))

	if err := foldRenamedStations(cm, scraped); err != nil {
		return nil, err
	}

	// Concurrently decode each airport's existing records, merge in the new
	// ones, and re-compress. cm is only read during this phase; the updates
	// are applied serially below.
	aps := util.SortedMapKeys(scraped)
	updates := make([]metarUpdate, len(aps))
	eg := errgroup.Group{}
	eg.SetLimit(*nWorkers)
	for i, ap := range aps {
		eg.Go(func() error {
			updates[i] = mergeAirportMETAR(cm, ap, scraped[ap])
			return nil
		})
	}
	eg.Wait()

	times := make(map[string][]time.Time)
	var nFailedAirports, nDroppedRecords int
	for i, ap := range aps {
		u := updates[i]
		if u.err != nil {
			LogError("%s: %v (skipping airport)", ap, u.err)
			nFailedAirports++
			continue
		}
		cm.SetCompressedAirportMETAR(ap, u.blob)
		times[ap] = u.times
		nDroppedRecords += u.dropped
	}

	if nDroppedRecords > 0 {
		LogError("dropped %d METAR records whose ICAO didn't match the file's airport", nDroppedRecords)
	}
	if nFailedAirports > 0 {
		LogError("%d airports failed round-trip check and were skipped", nFailedAirports)
	}

	// Make fake METAR for KAAC based on KOKC.
	if _, ok := times["KOKC"]; ok {
		recs, err := cm.GetAirportMETAR("KOKC")
		if err != nil {
			return nil, err
		}
		for i := range recs {
			recs[i].ICAO = "KAAC"
			recs[i].Raw = strings.ReplaceAll(recs[i].Raw, "KOKC", "KAAC")
		}
		if err := cm.SetAirportMETAR("KAAC", recs); err != nil {
			return nil, err
		}
		times["KAAC"] = times["KOKC"]
	}

	return times, nil
}

// foldRenamedStations hands the records of a station whose airport the FAA has
// re-identified to the current identifier, rewriting the ICAO on each so the
// station reads as one continuous series across the change. They go in with the
// scraped records so that the merge below sorts, dedups, and re-compresses them
// along with everything else.
func foldRenamedStations(cm wx.CompressedMETAR, scraped map[string][]wx.METAR) error {
	for previous, current := range av.RenamedAirports {
		if !cm.HasAirport(previous) {
			continue
		}

		recs, err := cm.GetAirportMETAR(previous)
		if err != nil {
			return fmt.Errorf("%s: decoding METAR to fold into %s: %w", previous, current, err)
		}
		for i := range recs {
			recs[i].ICAO = current
			recs[i].Raw = strings.ReplaceAll(recs[i].Raw, previous, current)
		}

		scraped[current] = append(scraped[current], recs...)
		cm.RemoveAirport(previous)
		LogInfo("%s: folded %d METAR records into %s", previous, len(recs), current)
	}
	return nil
}

// mergeAirportMETAR merges recs into the airport's existing records in cm
// and returns the airport's updated compressed encoding.
func mergeAirportMETAR(cm wx.CompressedMETAR, ap string, recs []wx.METAR) metarUpdate {
	var u metarUpdate

	if cm.HasAirport(ap) {
		existing, err := cm.GetAirportMETAR(ap)
		if err != nil {
			u.err = fmt.Errorf("decoding existing METAR: %w", err)
			return u
		}
		recs = append(existing, recs...)
	}

	// Scraped files are tagged by their first record's ICAO, but
	// occasionally contain stray records from other airports. Filter
	// those out so they don't fail the round-trip check.
	if mismatched := util.FilterSlice(recs, func(r wx.METAR) bool { return r.ICAO != ap }); len(mismatched) > 0 {
		u.dropped = len(mismatched)
		recs = util.FilterSlice(recs, func(r wx.METAR) bool { return r.ICAO == ap })
	}

	// Sort by date; since the time format used is 2006-01-02 15:04:05,
	// string compare sorts them in time order.
	slices.SortFunc(recs, func(a, b wx.METAR) int { return strings.Compare(a.ReportTime, b.ReportTime) })

	// Eliminate duplicates (expected, since the scraper's fetch windows overlap).
	recs = slices.CompactFunc(recs, func(a, b wx.METAR) bool { return a.ReportTime == b.ReportTime })

	u.blob, u.err = wx.CompressAirportMETAR(ap, recs)
	u.times = util.MapSlice(recs, func(m wx.METAR) time.Time { return m.Time.UTC() })
	return u
}

// updateMETARManifest folds the given airports' observation times into the
// METAR manifest. If there is no existing manifest to update, only rebuilding
// from the full consolidated METAR gives a complete one: times alone covers
// just the airports the scraper has gotten to since the last ingest.
func updateMETARManifest(sb StorageBackend, cm wx.CompressedMETAR, times map[string][]time.Time) error {
	var raw wx.RawManifest
	if err := sb.ReadObject(wx.ManifestPath("metar"), &raw); err != nil {
		LogInfo("%s: %v; rebuilding manifest from consolidated METAR", wx.ManifestPath("metar"), err)
		return rebuildMETARManifest(sb, cm)
	}

	manifest := wx.MakeManifest(raw)
	for ap, ts := range times {
		if err := manifest.SetFacilityTimestamps(ap, ts); err != nil {
			return err
		}
	}

	return storeManifest(sb, manifest, wx.ManifestPath("metar"), "metar-manifest.msgpack.zst")
}

// rebuildMETARManifest regenerates the METAR manifest from the consolidated
// METAR object; used by -manifests-only.
func rebuildMETARManifest(sb StorageBackend, cm wx.CompressedMETAR) error {
	times := make(map[string][]time.Time)
	for ap := range cm.Airports() {
		metar, err := cm.GetAirportMETAR(ap)
		if err != nil {
			return fmt.Errorf("%s: %w", ap, err)
		}
		times[ap] = util.MapSlice(metar, func(m wx.METAR) time.Time { return m.Time.UTC() })
	}

	manifest, err := wx.MakeManifestFromMap(times)
	if err != nil {
		return err
	}

	return storeManifest(sb, manifest, wx.ManifestPath("metar"), "metar-manifest.msgpack.zst")
}

func archiveMETAR(arch []toArchive, sb StorageBackend) error {
	if len(arch) == 0 {
		return nil
	}

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

	path := fmt.Sprintf("archive/metar/%s.zip", time.Now().UTC().Format(time.RFC3339))
	n, err := sb.Store(path, &b)
	if err != nil {
		return err
	}
	LogInfo("Archived %s of scraped METAR from %d records. Deleting scraped...", util.ByteCount(n), len(arch))

	eg := errgroup.Group{}
	eg.SetLimit(*nWorkers)
	for _, rec := range arch {
		eg.Go(func() error {
			if err := sb.Delete(rec.path); err != nil {
				LogInfo("%s: %v", rec.path, err)
			}
			return nil
		})
	}
	eg.Wait()

	LogInfo("Deleted %d scraped METAR records", len(arch))

	return nil
}

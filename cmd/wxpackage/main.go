// cmd/wxpackage/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/api/option"
)

var (
	dateRange = flag.String("dates", "", "Date range to package (format: 2025-08-01/2025-09-01). If not specified, all available data is used.")
	outputDir = flag.String("output", "resources/wx", "Output directory for packaged weather data")
)

// gcsReadTimeout is the per-operation timeout for individual GCS reads.
const gcsReadTimeout = 2 * time.Minute

func main() {
	flag.Parse()

	var startDate, endDate time.Time
	var err error

	if *dateRange != "" {
		// Parse specified date range
		dates := strings.Split(*dateRange, "/")
		if len(dates) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid date range format. Expected: 2025-08-01/2025-09-01")
			os.Exit(1)
		}

		startDate, err = time.Parse("2006-01-02", dates[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid start date: %v", err)
			os.Exit(1)
		}
		startDate = startDate.UTC()

		endDate, err = time.Parse("2006-01-02", dates[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid end date: %v", err)
			os.Exit(1)
		}
		endDate = endDate.UTC()
	}

	av.InitDB()

	// Load scenarios to find active airports and facilities (TRACONs + ARTCCs)
	var e util.ErrorLogger
	lg := log.New(false, "warn", "")
	scenarioGroups, _, _, _, _ := server.LoadScenarioGroups("", "", "", &e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}

	airports := make(map[string]bool)
	facilities := make(map[string]bool)
	artccs := make(map[string]bool)

	for facility, scenarios := range scenarioGroups {
		facilities[facility] = true
		for _, sg := range scenarios {
			for icao := range sg.Airports {
				airports[icao] = true
			}
			if sg.ARTCC != "" {
				artccs[sg.ARTCC] = true
			} else if sg.TRACON != "" {
				// TRACON scenarios don't set ARTCC explicitly;
				// look up the parent ARTCC from the database.
				if artcc := av.DB.ARTCCForFacility(sg.TRACON); artcc != "" {
					artccs[artcc] = true
				}
			}
		}
	}

	// Also add all facilities from the atmos lists (some may not have
	// scenarios yet but we still want their data bundled).
	for _, tracon := range wx.AtmosTRACONs() {
		facilities[tracon] = true
	}
	for _, artcc := range wx.AtmosARTCCs() {
		facilities[artcc] = true
	}

	fmt.Printf("Found %d active airports across %d facilities (TRACONs + ARTCCs)\n", len(airports), len(facilities))

	// Initialize GCS client. Use VICE_GCS_CREDENTIALS when set; otherwise
	// fall back to Application Default Credentials (e.g. the attached
	// service account on Cloud Run).
	ctx := context.Background()
	var opts []option.ClientOption
	if credsJSON := os.Getenv("VICE_GCS_CREDENTIALS"); credsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(credsJSON)))
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create GCS client: %v", err)
		os.Exit(1)
	}
	defer client.Close()

	bucket := client.Bucket("vice-wx")

	// If no date range specified, use default wide range
	if *dateRange == "" {
		startDate = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		endDate = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
		fmt.Printf("No date range specified, using default range: %s to %s\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	}

	fmt.Printf("Processing weather data from %s to %s\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	// Create output directories
	atmosDir := filepath.Join(*outputDir, "atmos")
	if err := os.MkdirAll(atmosDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create atmos directory: %v", err)
		os.Exit(1)
	}

	// Process METAR data
	fmt.Printf("Processing METAR data for %d airports\n", len(airports))
	if err := processMETAR(ctx, bucket, airports, startDate, endDate, *outputDir); err != nil {
		fmt.Printf("Failed to process METAR: %v\n", err)
		os.Exit(1)
	}

	// Process TFR data
	fmt.Printf("Processing TFR data for %d ARTCCs\n", len(artccs))
	if err := processTFRs(ctx, bucket, artccs, startDate, endDate, *outputDir); err != nil {
		fmt.Printf("Failed to process TFRs: %v\n", err)
		os.Exit(1)
	}

	// Process atmospheric data
	fmt.Printf("Processing atmospheric data for %d facilities\n", len(facilities))
	if err := processAtmos(ctx, bucket, facilities, startDate, endDate, atmosDir); err != nil {
		fmt.Printf("Failed to process atmospheric data: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Weather package created successfully in %s\n", *outputDir)
}

// gcsNewReader opens a GCS object for reading with a per-operation timeout
// and retries with exponential backoff on transient failures. The returned
// reader's context is kept alive until Close is called.
func gcsNewReader(ctx context.Context, bucket *storage.BucketHandle, path string) (io.ReadCloser, error) {
	var r *storage.Reader
	var cancel context.CancelFunc
	err := retry(ctx, 3, 10*time.Second, func() error {
		var readCtx context.Context
		readCtx, cancel = context.WithTimeout(ctx, gcsReadTimeout)

		var err error
		r, err = bucket.Object(path).NewReader(readCtx)
		if err != nil {
			cancel()
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return &readerWithCancel{ReadCloser: r, cancel: cancel}, nil
}

// readerWithCancel wraps a ReadCloser and calls cancel when closed,
// ensuring the context stays alive for the duration of the read.
type readerWithCancel struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *readerWithCancel) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

func processMETAR(ctx context.Context, bucket *storage.BucketHandle, airports map[string]bool, start, end time.Time, outputDir string) error {
	// Download the full METAR file
	r, err := gcsNewReader(ctx, bucket, wx.METARFilename)
	if err != nil {
		return err
	}
	defer r.Close()

	allMETAR, err := wx.LoadCompressedMETAR(r)
	if err != nil {
		return err
	}

	// Filter to only active airports and optionally filter by date range
	filteredMETAR := wx.NewCompressedMETAR()

	for icao := range allMETAR.Airports() {
		if !airports[icao] {
			continue
		}

		metar, err := allMETAR.GetAirportMETAR(icao)
		if err != nil {
			return fmt.Errorf("%s: %w", icao, err)
		}

		filtered := util.FilterSlice(metar, func(m wx.METAR) bool {
			return !m.Time.Before(start) && !m.Time.After(end)
		})

		if len(filtered) > 0 {
			if err := filteredMETAR.SetAirportMETAR(icao, filtered); err != nil {
				return err
			}
		}
	}

	// Write single METAR file for all airports
	outputPath := filepath.Join(outputDir, wx.METARFilename)

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}

	if err := filteredMETAR.Save(f); err != nil {
		f.Close()
		return fmt.Errorf("failed to save METAR file: %w", err)
	}

	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote METAR data for %d airports\n", filteredMETAR.Len())

	return nil
}

func processTFRs(ctx context.Context, bucket *storage.BucketHandle, artccs map[string]bool, start, end time.Time, outputDir string) error {
	// Download the full TFR file
	r, err := gcsNewReader(ctx, bucket, wx.TFRFilename)
	if err != nil {
		return err
	}
	defer r.Close()

	allTFRs, err := wx.LoadCompressedTFRs(r)
	if err != nil {
		return err
	}

	// Filter: keep TFRs whose [Effective, Expire] overlaps the date range
	// AND whose ARTCC matches a scenario-relevant ARTCC.
	var filtered []av.TFR
	for _, tfr := range allTFRs {
		if !artccs[tfr.ARTCC] {
			continue
		}
		// Check time overlap: TFR is relevant if Effective < end AND Expire > start
		if tfr.Effective.Before(end) && tfr.Expire.After(start) {
			filtered = append(filtered, tfr)
		}
	}

	// Write filtered TFRs
	outputPath := filepath.Join(outputDir, wx.TFRFilename)
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}

	if err := wx.SaveCompressedTFRs(filtered, f); err != nil {
		f.Close()
		return fmt.Errorf("failed to save TFR file: %w", err)
	}

	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote TFR data: %d TFRs (from %d total) for %d ARTCCs\n", len(filtered), len(allTFRs), len(artccs))
	return nil
}

func processAtmos(ctx context.Context, bucket *storage.BucketHandle, facilities map[string]bool, startDate, endDate time.Time, outputDir string) error {
	packaged := make(map[string][]time.Time)
	var mu sync.Mutex

	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(16)
	for facilityID := range facilities {
		eg.Go(func() error {
			times, err := processFacilityAtmos(ctx, bucket, facilityID, startDate, endDate, outputDir)
			if err != nil {
				return err
			}
			if len(times) > 0 {
				mu.Lock()
				packaged[facilityID] = times
				mu.Unlock()
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}
	return writePackagedAtmosManifest(packaged, outputDir)
}

// processFacilityAtmos writes the facility's bundled atmospheric data from
// the series object that "wxingest atmosseries" rolled its hourly averaged
// profiles up into, and returns the timestamps it packaged.
func processFacilityAtmos(ctx context.Context, bucket *storage.BucketHandle, facilityID string, startDate, endDate time.Time, outputDir string) ([]time.Time, error) {
	seriesPath := wx.AtmosSeriesPath(facilityID)

	r, err := gcsNewReader(ctx, bucket, seriesPath)
	if errors.Is(err, storage.ErrObjectNotExist) {
		fmt.Printf("%s: no atmospheric data, skipping\n", facilityID)
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	zr, err := zstd.NewReader(r)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("zstd decompress %s: %w", seriesPath, err)
	}

	var seriesSOA wx.AtmosByTimeSOA
	err = msgpack.NewDecoder(zr).Decode(&seriesSOA)
	zr.Close()
	r.Close()
	if err != nil {
		return nil, fmt.Errorf("msgpack decode %s: %w", seriesPath, err)
	}

	facilityAtmos := seriesSOA.ToAOS()
	maps.DeleteFunc(facilityAtmos.SampleStacks, func(t time.Time, _ *wx.AtmosSampleStack) bool {
		return t.Before(startDate) || t.After(endDate)
	})
	if len(facilityAtmos.SampleStacks) == 0 {
		fmt.Printf("%s: no atmospheric data in the requested date range, skipping\n", facilityID)
		return nil, nil
	}

	facilityAtmosSOA, err := facilityAtmos.ToSOA()
	if err != nil {
		return nil, err
	}

	outputPath := filepath.Join(outputDir, facilityID+".msgpack.zst")
	f, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return nil, err
	} else if err := msgpack.NewEncoder(zw).Encode(facilityAtmosSOA); err != nil {
		return nil, err
	} else if err := zw.Close(); err != nil {
		return nil, err
	} else if err := f.Close(); err != nil {
		return nil, err
	}

	times := slices.Collect(maps.Keys(facilityAtmos.SampleStacks))
	fmt.Printf("Wrote atmospheric data for %s: %d entries\n", facilityID, len(times))

	return times, nil
}

func writePackagedAtmosManifest(packaged map[string][]time.Time, outputDir string) error {
	manifest := wx.NewManifest()
	for facility, times := range packaged {
		if err := manifest.SetFacilityTimestamps(facility, times); err != nil {
			return fmt.Errorf("%s: set manifest timestamps: %w", facility, err)
		}
	}

	path := filepath.Join(outputDir, wx.ManifestFilename)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := manifest.Save(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote atmosphere manifest: %s\n", path)
	return nil
}

// retry calls fn up to attempts times with exponential backoff starting at sleep.
// It stops early if ctx is cancelled.
func retry(ctx context.Context, attempts int, sleep time.Duration, fn func() error) error {
	var err error
	for range attempts {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		fmt.Printf("retryable error (will retry in %s): %v\n", sleep, err)
		time.Sleep(sleep)
		sleep *= 2
	}
	return err
}

// cmd/wxpackage/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	scenarioGroups, _, _, _ := server.LoadScenarioGroups("", "", true /* skipVideoMaps */, &e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}

	airports := make(map[string]bool)
	facilities := make(map[string]bool)

	for facility, scenarios := range scenarioGroups {
		facilities[facility] = true
		for _, sg := range scenarios {
			for icao := range sg.Airports {
				airports[icao] = true
			}
		}
	}

	// Also add all facilities from the atmos lists (some may not have
	// scenarios yet but we still want their data bundled).
	for _, tracon := range wx.AtmosTRACONs {
		facilities[tracon] = true
	}
	for _, artcc := range wx.AtmosARTCCs {
		facilities[artcc] = true
	}

	fmt.Printf("Found %d active airports across %d facilities (TRACONs + ARTCCs)\n", len(airports), len(facilities))

	// Initialize GCS client
	ctx := context.Background()
	credsJSON := os.Getenv("VICE_GCS_CREDENTIALS")
	if credsJSON == "" {
		fmt.Fprintf(os.Stderr, "VICE_GCS_CREDENTIALS environment variable not set")
		os.Exit(1)
	}
	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
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

func processAtmos(ctx context.Context, bucket *storage.BucketHandle, facilities map[string]bool, startDate, endDate time.Time, outputDir string) error {
	// Download the manifest once and share it across all workers.
	r, err := gcsNewReader(ctx, bucket, wx.ManifestPath("atmos"))
	if err != nil {
		return err
	}
	manifest, err := wx.LoadManifest(r)
	r.Close()
	if err != nil {
		return fmt.Errorf("failed to load atmos manifest: %w", err)
	}

	eg, ctx := errgroup.WithContext(ctx)

	ch := make(chan string)
	eg.Go(func() error {
		defer close(ch)
		for fac := range facilities {
			select {
			case ch <- fac:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})

	for range 16 { // workers
		eg.Go(func() error {
			for facilityID := range ch {
				if err := processFacilityAtmos(ctx, bucket, manifest, facilityID, startDate, endDate, outputDir); err != nil {
					return err
				}
			}
			return nil
		})
	}

	return eg.Wait()
}

func processFacilityAtmos(ctx context.Context, bucket *storage.BucketHandle, manifest *wx.Manifest, facilityID string, startDate, endDate time.Time, outputDir string) error {
	// Load existing atmospheric data if it exists
	facilityAtmos := wx.AtmosByTime{
		SampleStacks: make(map[time.Time]*wx.AtmosSampleStack),
	}

	outputPath := filepath.Join(outputDir, facilityID+".msgpack.zst")
	facilityAtmos, err := loadExistingAtmosData(outputPath)
	if err == nil {
		fmt.Printf("Loaded existing atmospheric data for %s with %d time entries\n", facilityID, len(facilityAtmos.SampleStacks))
	} else {
		if !os.IsNotExist(err) {
			// Corrupt file from a previous interrupted run; warn and start fresh.
			fmt.Printf("%s: failed to load existing data (%v), starting fresh\n", facilityID, err)
		}
		facilityAtmos = wx.AtmosByTime{
			SampleStacks: make(map[time.Time]*wx.AtmosSampleStack),
		}
	}

	// Get timestamps for this facility from the manifest
	allTimestamps, ok := manifest.GetTimestamps(facilityID)
	if !ok {
		fmt.Printf("%s: no data in manifest, skipping\n", facilityID)
		return nil
	}

	// Filter timestamps by date range and check if we already have them
	var timestampsToDownload []time.Time
	for _, ts := range allTimestamps {
		// Skip if outside date range
		if ts.Before(startDate) || ts.After(endDate) {
			continue
		}
		// Skip if we already have data for this time
		if _, ok := facilityAtmos.SampleStacks[ts]; ok {
			continue
		}
		timestampsToDownload = append(timestampsToDownload, ts)
	}

	if len(timestampsToDownload) == 0 {
		fmt.Printf("%s: no new data to download (have %d entries already)\n", facilityID, len(facilityAtmos.SampleStacks))
		return nil
	}

	fmt.Printf("%s: downloading %d atmos objects (have %d already, manifest has %d total)\n",
		facilityID, len(timestampsToDownload), len(facilityAtmos.SampleStacks), len(allTimestamps))

	// Download and process atmos data at each timestamp
	for _, timestamp := range timestampsToDownload {
		objectPath := wx.BuildObjectPath("atmos", facilityID, timestamp)

		// Download and process this atmos file
		r, err := gcsNewReader(ctx, bucket, objectPath)
		if err != nil {
			return err
		}

		// Decompress and deserialize
		zr, err := zstd.NewReader(r)
		if err != nil {
			r.Close()
			return fmt.Errorf("%s: zstd decompress %s: %w", facilityID, objectPath, err)
		}

		var atmosSOA wx.AtmosByPointSOA
		if err := msgpack.NewDecoder(zr).Decode(&atmosSOA); err != nil {
			zr.Close()
			r.Close()
			return fmt.Errorf("%s: msgpack decode %s: %w", facilityID, objectPath, err)
		}
		zr.Close()
		r.Close()

		// Convert SOA to regular Atmos object and store the averaged stack
		atmos := atmosSOA.ToAOS()
		_, avgStack := atmos.Average()
		facilityAtmos.SampleStacks[timestamp] = avgStack
	}

	fmt.Printf("%s: processed %d entries\n", facilityID, len(facilityAtmos.SampleStacks))

	// Convert AtmosByTime to SOA format for storage
	facilityAtmosSOA, err := facilityAtmos.ToSOA()
	if err != nil {
		return err
	}

	// Write atmospheric data file
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return err
	} else if err := msgpack.NewEncoder(zw).Encode(facilityAtmosSOA); err != nil {
		return err
	} else if err := zw.Close(); err != nil {
		return err
	} else if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote atmospheric data for %s\n", facilityID)

	return nil
}

// loadExistingAtmosData loads existing atmospheric data from a file
func loadExistingAtmosData(path string) (wx.AtmosByTime, error) {
	f, err := os.Open(path)
	if err != nil {
		return wx.AtmosByTime{}, err
	}
	defer f.Close()

	// Decompress and deserialize
	zr, err := zstd.NewReader(f)
	if err != nil {
		return wx.AtmosByTime{}, err
	}
	defer zr.Close()

	var atmosSOA wx.AtmosByTimeSOA
	if err := msgpack.NewDecoder(zr).Decode(&atmosSOA); err != nil {
		return wx.AtmosByTime{}, err
	}

	// Convert SOA back to AtmosByTime
	return atmosSOA.ToAOS(), nil
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

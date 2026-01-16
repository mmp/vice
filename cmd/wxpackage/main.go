// cmd/wxpackage/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
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
)

var (
	dateRange = flag.String("dates", "", "Date range to package (format: 2025-08-01/2025-09-01). If not specified, all available data is used.")
	outputDir = flag.String("output", "resources/wx", "Output directory for packaged weather data")
)

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

	// Load scenarios to find active airports/TRACONs
	var e util.ErrorLogger
	lg := log.New(false, "warn", "")
	scenarioGroups, _, _, _ := server.LoadScenarioGroups("", "", true /* skipVideoMaps */, &e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}

	// Extract all active airports from scenarios
	airports := make(map[string]bool)
	tracons := make(map[string]bool)

	for tracon, scenarios := range scenarioGroups {
		if tracon == "" { // ERAM scenario; ignore for now
			continue
		}

		tracons[tracon] = true
		for _, sg := range scenarios {
			for icao := range sg.Airports {
				airports[icao] = true
			}
		}
	}

	fmt.Printf("Found %d active airports across %d TRACONs\n", len(airports), len(tracons))

	// Initialize GCS client
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
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
	fmt.Printf("Processing atmospheric data for %d TRACONs\n", len(tracons))
	if err := processAtmos(ctx, bucket, tracons, startDate, endDate, atmosDir); err != nil {
		fmt.Printf("Failed to process atmospheric data: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Weather package created successfully in %s\n", *outputDir)
}

func processMETAR(ctx context.Context, bucket *storage.BucketHandle, airports map[string]bool, start, end time.Time, outputDir string) error {
	// Download the full METAR file
	r, err := bucket.Object(wx.METARFilename).NewReader(ctx)
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

func processAtmos(ctx context.Context, bucket *storage.BucketHandle, tracons map[string]bool, startDate, endDate time.Time, outputDir string) error {
	var eg errgroup.Group

	ch := make(chan string)
	eg.Go(func() error {
		for t := range tracons {
			ch <- t
		}
		close(ch)
		return nil
	})

	for range 16 { // workers
		eg.Go(func() error {
			for tracon := range ch {
				if err := processTraconAtmos(ctx, bucket, tracon, startDate, endDate, outputDir); err != nil {
					return err
				}
			}
			return nil
		})
	}

	return eg.Wait()
}

func processTraconAtmos(ctx context.Context, bucket *storage.BucketHandle, tracon string, startDate, endDate time.Time, outputDir string) error {
	// Load existing atmospheric data if it exists
	traconAtmos := wx.AtmosByTime{
		SampleStacks: make(map[time.Time]*wx.AtmosSampleStack),
	}

	outputPath := filepath.Join(outputDir, tracon+".msgpack.zst")
	traconAtmos, err := loadExistingAtmosData(outputPath)
	if err == nil {
		fmt.Printf("Loaded existing atmospheric data for %s with %d time entries\n", tracon, len(traconAtmos.SampleStacks))
	} else if os.IsNotExist(err) {
		traconAtmos = wx.AtmosByTime{
			SampleStacks: make(map[time.Time]*wx.AtmosSampleStack),
		}
	} else {
		return err
	}

	// Read the atmos manifest to see what's available.
	manifestPath := wx.ManifestPath("atmos")
	manifestReader, err := bucket.Object(manifestPath).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
	}
	manifest, err := wx.LoadManifest(manifestReader)
	manifestReader.Close()
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	// Get timestamps for this TRACON from the manifest
	allTimestamps, ok := manifest.GetTimestamps(tracon)
	if !ok {
		fmt.Printf("%s: no data in manifest, skipping\n", tracon)
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
		if _, ok := traconAtmos.SampleStacks[ts]; ok {
			continue
		}
		timestampsToDownload = append(timestampsToDownload, ts)
	}

	if len(timestampsToDownload) == 0 {
		fmt.Printf("%s: no new data to download (have %d entries already)\n", tracon, len(traconAtmos.SampleStacks))
		return nil
	}

	fmt.Printf("%s: downloading %d atmos objects (have %d already, manifest has %d total)\n",
		tracon, len(timestampsToDownload), len(traconAtmos.SampleStacks), len(allTimestamps))

	// Download and process atmos data at each timestamp
	for _, timestamp := range timestampsToDownload {
		objectPath := wx.BuildObjectPath("atmos", tracon, timestamp)

		// Download and process this atmos file
		r, err := bucket.Object(objectPath).NewReader(ctx)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", objectPath, err)
		}

		// Decompress and deserialize
		zr, err := zstd.NewReader(r)
		if err != nil {
			r.Close()
			return err
		}

		var atmosSOA wx.AtmosByPointSOA
		if err := msgpack.NewDecoder(zr).Decode(&atmosSOA); err != nil {
			zr.Close()
			r.Close()
			return err
		}
		zr.Close()
		r.Close()

		// Convert SOA to regular Atmos object and store the averaged stack
		atmos := atmosSOA.ToAOS()
		_, avgStack := atmos.Average()
		traconAtmos.SampleStacks[timestamp] = avgStack
	}

	fmt.Printf("%s: processed %d entries\n", tracon, len(traconAtmos.SampleStacks))

	// Convert AtmosByTime to SOA format for storage
	traconAtmosSOA, err := traconAtmos.ToSOA()
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
	} else if err := msgpack.NewEncoder(zw).Encode(traconAtmosSOA); err != nil {
		return err
	} else if err := zw.Close(); err != nil {
		return err
	} else if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote atmospheric data for %s\n", tracon)

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

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
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/api/iterator"
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
	scenarioGroups, _, _ := server.LoadScenarioGroups(true, "", "", &e, nil)
	if e.HaveErrors() {
		e.PrintErrors(nil)
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

func processMETAR(ctx context.Context, bucket *storage.BucketHandle, airports map[string]bool, startDate, endDate time.Time, outputDir string) error {
	// Download the full METAR file
	objectName := "METAR.msgpack.zst"
	r, err := bucket.Object(objectName).NewReader(ctx)
	if err != nil {
		return err
	}
	defer r.Close()

	// Decompress and deserialize
	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()

	var allMETAR map[string]wx.METARSOA
	if err := msgpack.NewDecoder(zr).Decode(&allMETAR); err != nil {
		return err
	}

	// Filter to only active airports and optionally filter by date range
	filteredMETAR := make(map[string]wx.METARSOA)

	for icao, metarSOA := range allMETAR {
		if !airports[icao] {
			continue
		}

		// Decode the METAR data to filter by date
		var filteredMETARs []wx.METAR
		for _, metar := range wx.DecodeMETARSOA(metarSOA) {
			if !metar.Time.Before(startDate) && !metar.Time.After(endDate) {
				filteredMETARs = append(filteredMETARs, metar)
			}
		}

		if len(filteredMETARs) > 0 {
			// Re-encode the filtered METARs
			filteredSOA, err := wx.MakeMETARSOA(filteredMETARs)
			if err != nil {
				return fmt.Errorf("%s METAR: %w", icao, err)
			}

			filteredMETAR[icao] = filteredSOA
		}
	}

	// Write single METAR file for all airports
	outputPath := filepath.Join(outputDir, "METAR.msgpack.zst")

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	} else if zw, err := zstd.NewWriter(f); err != nil {
		return err
	} else if err := msgpack.NewEncoder(zw).Encode(filteredMETAR); err != nil {
		return err
	} else if err := zw.Close(); err != nil {
		return err
	} else if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("Wrote METAR data for %d airports\n", len(filteredMETAR))

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

	// List all atmos objects for this TRACON
	prefix := "atmos/" + tracon + "/"
	query := &storage.Query{
		Prefix: prefix,
	}
	it := bucket.Objects(ctx, query)
	addedAny := false
	objectCount := 0

	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		} else if err != nil {
			return fmt.Errorf("failed to list objects with prefix %s: %w", prefix, err)
		}

		objectCount++

		// Extract timestamp from filename like "atmos/N90/2025-08-01T00:00:00Z.msgpack.zst"
		filename := filepath.Base(obj.Name)
		if filename == "manifest.msgpack.zst" {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, strings.TrimSuffix(filename, ".msgpack.zst"))
		if err != nil {
			fmt.Printf("%s: %v\n", filename, err)
			continue
		}

		// Skip if outside date range
		if timestamp.Before(startDate) || timestamp.After(endDate) {
			continue
		}
		// Skip if we already have data for this time
		if _, ok := traconAtmos.SampleStacks[timestamp]; ok {
			continue
		}

		// Download and process this atmos file
		r, err := bucket.Object(obj.Name).NewReader(ctx)
		if err != nil {
			return err
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
		addedAny = true
	}

	fmt.Printf("%s: found %d objects, processed %d new entries\n", tracon, objectCount, len(traconAtmos.SampleStacks))

	if !addedAny {
		return nil
	}

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

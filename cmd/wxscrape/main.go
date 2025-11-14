package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const bucketName = "vice-wx"

func main() {
	var metar, precip, tfrs bool
	if len(os.Args) == 1 {
		metar, precip, tfrs = true, true, true
	} else {
		for _, a := range os.Args[1:] {
			switch strings.ToLower(a) {
			case "metar":
				metar = true
			case "precip":
				precip = true
			case "tfrs":
				tfrs = true
			default:
				fmt.Fprintf(os.Stderr, "usage: wxscrape [metar|precip|tfrs]...\n")
				os.Exit(1)
			}
		}
	}

	credsJSON := os.Getenv("VICE_GCS_CREDENTIALS")
	if credsJSON == "" {
		fmt.Fprintf(os.Stderr, "VICE_GCS_CREDENTIALS environment variable not set\n")
		os.Exit(1)
	}

	launchHTTPServer()

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	bucket := client.Bucket(bucketName)

	if metar {
		go fetchMETAR(ctx, bucket)
	}
	if precip {
		go fetchPRECIP(ctx, bucket)
	}
	if tfrs {
		go fetchTFRs(ctx, bucket)
	}

	select {} // wait forever
}

func LogInfo(msg string, args ...any) {
	log.Printf("INFO "+msg, args...)
}

func LogError(msg string, args ...any) {
	log.Printf("ERROR "+msg, args...)
}

func listExisting(ctx context.Context, bucket *storage.BucketHandle, base string) map[string]interface{} {
	m := make(map[string]interface{})

	// See what has been archived already
	query := storage.Query{
		Projection: storage.ProjectionNoACL,
		Prefix:     base,
	}
	LogInfo("Archiver: listing existing objects in %q", query.Prefix)

	it := bucket.Objects(ctx, &query)
	var sz int64
	for {
		if obj, err := it.Next(); err == iterator.Done {
			break
		} else if err != nil {
			LogError("%v", err)
			break
		} else {
			sz += obj.Size
			m[obj.Name] = obj.Size
		}
	}

	LogInfo("Found %d objects in %q, %s", len(m), query.Prefix, util.ByteCount(sz))

	return m
}

// https://adip.faa.gov/agis/public/#/airportSearch/advanced -> control tower Yes
// then added K/P prefixes
// and then culled out ones with no METAR
// ->
//
//go:embed metar-airports.txt
var metarAirports string

func fetchMETAR(ctx context.Context, bucket *storage.BucketHandle) {
	var airports []string
	for ap := range strings.Lines(metarAirports) {
		airports = append(airports, strings.TrimSpace(ap))
	}
	LogInfo("%d METAR airports", len(airports))

	lastReport := make(map[string]time.Time)

	// ~650 airports -> fetches for all are spread over ~4 hours
	tick := time.Tick(21 * time.Second)
	tock := time.Tick(time.Hour)
	for {
		perm := rand.Perm(len(airports))
		for _, i := range perm {
			ap := airports[i]
			if t, ok := lastReport[ap]; ok && time.Since(t) < 20*time.Hour {
				LogInfo("%s: skipping METAR fetch, last fetch %s ago", ap, time.Since(t))
				continue
			} else {
				LogInfo("%s: fetching METAR: last fetch %s ago", ap, time.Since(t))
			}

			if doWithBackoff(func() Status { return fetchAirportMETAR(ctx, bucket, ap) }) {
				lastReport[ap] = time.Now()
			} else {
				LogError("%s: unable to fetch METAR; giving up for this cycle", ap)
			}

			<-tick
		}

		<-tock
	}
}

type Status int

const (
	StatusSuccess Status = iota
	StatusTransientFailure
)

func doWithBackoff(f func() Status) bool {
	backoff := 5 * time.Second
	for range 7 {
		switch f() {
		case StatusSuccess:
			return true

		case StatusTransientFailure:
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return false // unsuccessful after multiple retries
}

func downloadToGCS(ctx context.Context, bucket *storage.BucketHandle, url, objpath string) Status {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		LogError("%s: %v", url, err)
		return StatusTransientFailure
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		LogError("%s: HTTP status code %d", url, resp.StatusCode)
		return StatusTransientFailure
	}

	objw := bucket.Object(objpath).NewWriter(ctx)

	_, err = io.Copy(objw, resp.Body)
	if err != nil {
		LogError("%s: %v", objpath, err)
		return StatusTransientFailure
	}

	if err := objw.Close(); err != nil {
		LogError("%s: %v", objpath, err)
		return StatusTransientFailure
	}

	return StatusSuccess
}

func fetchAirportMETAR(ctx context.Context, bucket *storage.BucketHandle, ap string) Status {
	const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json&hours=%d`
	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, ap, 36 /* hours */)

	path := filepath.Join("scrape", "metar", ap, time.Now().Format(time.RFC3339)+".txt")
	status := downloadToGCS(ctx, bucket, requestUrl, path)
	if status == StatusSuccess {
		LogInfo("%s: downloaded METAR data", ap)
	}
	return status
}

func fetchPRECIP(ctx context.Context, bucket *storage.BucketHandle) {
	av.InitDB()

	for tracon := range av.DB.TRACONs {
		go fetchTraconPrecip(ctx, bucket, tracon)
	}
}

// fetchTraconPrecip runs asynchronously in a goroutine and fetches radar
// images for a single TRACON and writes them to disk.
func fetchTraconPrecip(ctx context.Context, bucket *storage.BucketHandle, tracon string) {
	// Spread out the requests temporally
	time.Sleep(time.Duration(rand.Intn(200)) * time.Second)

	tick := time.Tick(5 * time.Minute)

	tspec, ok := av.DB.TRACONs[tracon]
	if !ok {
		LogError("%s: unable to find TRACON info", tracon)
	}
	center := tspec.Center()
	fetchResolution := int(4 * tspec.Radius) // -> 0.5nm per pixel
	bbox := math.BoundLatLongCircle(center, tspec.Radius)

	// The weather radar image comes via a WMS GetMap request from the NOAA.
	//
	// Relevant background:
	// https://enterprise.arcgis.com/en/server/10.3/publish-services/windows/communicating-with-a-wms-service-in-a-web-browser.htm
	// http://schemas.opengis.net/wms/1.3.0/capabilities_1_3_0.xsd
	// NOAA weather: https://opengeo.ncep.noaa.gov/geoserver/www/index.html
	// https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?service=wms&version=1.3.0&request=GetCapabilities
	params := url.Values{}
	params.Add("SERVICE", "WMS")
	params.Add("REQUEST", "GetMap")
	params.Add("FORMAT", "image/png")
	params.Add("WIDTH", fmt.Sprintf("%d", fetchResolution))
	params.Add("HEIGHT", fmt.Sprintf("%d", fetchResolution))
	params.Add("LAYERS", "conus_bref_qcd")
	params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", bbox.P0[0], bbox.P0[1], bbox.P1[0], bbox.P1[1]))

	url := "https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?" + params.Encode()

	for {
		var havePrecip bool

		tryFetch := func() Status {
			resp, err := http.Get(url)
			if err != nil {
				LogError("%s: %s: %v", tracon, url, err)
				return StatusTransientFailure
			}
			defer resp.Body.Close()

			b, err := io.ReadAll(resp.Body)
			if err != nil {
				LogError("%s: %s: %v", tracon, url, err)
				return StatusTransientFailure
			}

			var status Status
			havePrecip, status = func() (bool, Status) {
				// Decode and see if there's anything there.
				img, err := png.Decode(bytes.NewReader(b))
				if err != nil {
					LogError("%s: %s: %v", tracon, url, err)
					return false, StatusTransientFailure
				}

				if nimg, ok := img.(*image.NRGBA); !ok {
					// This path is much slower but we'll keep it
					// around for robustness in case the image format
					// somehow changes.
					LogError("%s: %s: PNG is not an *image.NRGBA", tracon, url)
					bounds := img.Bounds()
					for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
						for x := bounds.Min.X; x < bounds.Max.X; x++ {
							r, g, b, _ := img.At(x, y).RGBA()
							if r != 0xffff || g != 0xffff || b != 0xffff {
								return true, StatusSuccess
							}
						}
					}
					return false, StatusSuccess
				} else {
					bounds := nimg.Rect
					stride := nimg.Stride
					pix := nimg.Pix
					for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
						for x := bounds.Min.X; x < bounds.Max.X; x++ {
							offset := (y-bounds.Min.Y)*stride + (x-bounds.Min.X)*4
							if pix[offset] != 0xff || pix[offset+1] != 0xff || pix[offset+2] != 0xff {
								return true, StatusSuccess
							}
						}
					}
					return false, StatusSuccess
				}
			}()
			if status != StatusSuccess {
				return status
			}

			type WX struct {
				PNG        []byte
				Resolution int
				Latitude   float32
				Longitude  float32
			}
			wx := WX{
				PNG:        b,
				Resolution: fetchResolution,
				Latitude:   center[1],
				Longitude:  center[0],
			}

			path := filepath.Join("scrape", "WX", tracon, time.Now().UTC().Format(time.RFC3339)+".gob")

			objw := bucket.Object(path).NewWriter(ctx)

			if err := gob.NewEncoder(objw).Encode(wx); err != nil {
				LogError("%s: %v", path, err)
				return StatusTransientFailure
			}
			if err := objw.Close(); err != nil {
				LogError("%s: %v", path, err)
				return StatusTransientFailure
			}

			return StatusSuccess
		}

		if doWithBackoff(tryFetch) {
			LogInfo("Got precip for %s have precip %v", tracon, havePrecip)

			<-tick

			if !havePrecip {
				// No weather; sleep for 30 minutes rather than 5
				for range 5 {
					<-tick
				}
			}
		} else {
			LogError("%s: unable to fetch precip", tracon)
			<-tick
		}
	}
}

func fetchTFRs(ctx context.Context, bucket *storage.BucketHandle) {
	existing := listExisting(ctx, bucket, "scrape/tfrs")

	tick := time.Tick(time.Hour)

	for {
		doWithBackoff(func() Status {
			// Fetch the list of TFRs
			resp, err := http.Get("https://tfr.faa.gov/tfrapi/exportTfrList")
			if err != nil {
				LogError("Error fetching TFR list: %v", err)
				return StatusTransientFailure
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				LogError("Error reading TFR list response: %v", err)
				return StatusTransientFailure
			}

			// Parse JSON response
			var tfrList []struct {
				NotamID string `json:"notam_id"`
			}
			if err := json.Unmarshal(body, &tfrList); err != nil {
				LogError("Error parsing TFR list JSON: %v", err)
				return StatusTransientFailure
			}

			LogInfo("Found %d TFRs", len(tfrList))

			// Process each TFR
			for _, tfr := range tfrList {
				// Sanitize notam_id for use as filename
				safeID := strings.ReplaceAll(tfr.NotamID, "/", "_")
				safeID = strings.ReplaceAll(safeID, "\\", "_")
				safeID = strings.ReplaceAll(safeID, ":", "_")
				path := filepath.Join("scrape", "tfrs", safeID+".xml")

				// Check if already downloaded
				if _, ok := existing[path]; ok {
					LogInfo("TFR %s already downloaded", tfr.NotamID)
					continue
				}

				// Download the TFR
				url := fmt.Sprintf("https://tfr.faa.gov/download/detail_%s.xml", safeID)
				if downloadToGCS(ctx, bucket, url, path) != StatusSuccess {
					LogError("Failed to download TFR %s", tfr.NotamID)
				} else {
					LogInfo("Downloaded TFR %s", tfr.NotamID)
					existing[path] = 0
				}

				// Small delay between downloads
				time.Sleep(time.Second)
			}

			return StatusSuccess
		})

		<-tick
	}
}

func launchHTTPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if listener, err := net.Listen("tcp", ":8002"); err == nil {
		LogInfo("Launching HTTP server on port 8002")
		go http.Serve(listener, mux)
	} else {
		fmt.Fprintf(os.Stderr, "Unable to start HTTP server: %v", err)
		os.Exit(1)
	}
}

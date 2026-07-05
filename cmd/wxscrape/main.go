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
	"math/rand/v2"
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
	"github.com/mmp/vice/wx"

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

func listExisting(ctx context.Context, bucket *storage.BucketHandle, base string) map[string]any {
	m := make(map[string]any)

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
		go fetchFacilityPrecip(ctx, bucket, tracon)
	}
	for artcc := range av.DB.ARTCCs {
		go fetchFacilityPrecip(ctx, bucket, artcc)
	}
}

// calcResolution returns the image resolution to fetch for a given facility.
// TRACONs use 4*radius (0.5nm/pixel), ARTCCs use 2*radius capped at 2048
// (~1nm/pixel). Only used for facilities without an entry in
// nexradCoverage.
func calcResolution(facilityID string, radius float32) int {
	if _, isARTCC := av.DB.ARTCCs[facilityID]; isARTCC {
		return min(int(2*radius), 2048)
	}
	return int(4 * radius)
}

var nexradCoverage = map[string]struct {
	TopLat, TopLon float32 // degrees; anchor of pixel (0, 0)
	Width, Height  int     // NM (and pixels, at 1 NM/px)
}{
	"ZAB": {44, -121, 1412, 1140},
	"ZAU": {50, -101, 1191, 960},
	"ZBW": {54, -83, 1105, 1260},
	"ZDC": {46, -88, 1068, 1140},
	"ZDV": {51, -119, 1466, 1320},
	"ZFW": {42, -110, 1368, 1080},
	"ZHN": {31, -167, 1056, 1140},
	"ZHU": {38, -109, 2221, 1200},
	"ZID": {47, -96, 1141, 1020},
	"ZJX": {41, -94, 1343, 1200},
	"ZKC": {46, -110, 1466, 1020},
	"ZLA": {44, -131, 1423, 1200},
	"ZLC": {52, -123, 1388, 1080},
	"ZMA": {36, -92, 2132, 1620},
	"ZME": {43, -103, 1357, 1080},
	"ZMP": {55, -111, 1852, 1320},
	"ZNY": {48, -84, 1507, 1320},
	"ZOA": {47, -134, 1322, 1140},
	"ZOB": {50, -93, 1105, 1020},
	"ZSE": {54, -136, 1487, 1200},
	"ZTL": {43, -96, 1303, 1080},
}

// fetchGeometry returns the pixel dimensions and geographic bounds to fetch
// for a facility: the nexradCoverage rectangle at ERAM's 1 NM/pixel for
// ARTCCs, else a square around the facility center (0.5 NM/pixel for
// TRACONs).
func fetchGeometry(facilityID string, fac av.Facility) (wpx, hpx int, bbox math.Extent2D) {
	if cov, ok := nexradCoverage[facilityID]; ok {
		bottomLat := cov.TopLat - float32(cov.Height)/60
		eastLon := cov.TopLon + float32(cov.Width)/(60*math.Cos(math.Radians(bottomLat)))
		bbox = math.Extent2D{
			P0: [2]float32{cov.TopLon, bottomLat},
			P1: [2]float32{eastLon, cov.TopLat},
		}
		return cov.Width, cov.Height, bbox
	}

	res := calcResolution(facilityID, fac.Radius)
	return res, res, math.BoundLatLongCircle(fac.Center(), fac.Radius)
}

// fetchFacilityPrecip runs asynchronously in a goroutine and fetches radar
// images for a single facility (TRACON or ARTCC) and writes them to disk.
func fetchFacilityPrecip(ctx context.Context, bucket *storage.BucketHandle, facilityID string) {
	// Spread out the requests temporally
	time.Sleep(time.Duration(rand.IntN(200)) * time.Second)

	tick := time.Tick(5 * time.Minute)

	fac, ok := av.DB.LookupFacility(facilityID)
	if !ok {
		LogError("%s: unable to find facility info", facilityID)
		return
	}
	wpx, hpx, bbox := fetchGeometry(facilityID, fac)
	center := bbox.Center()

	area := "conus"
	if facilityID == "HCF" || facilityID == "OGG" || facilityID == "ZHN" {
		area = "hawaii"
	} else if facilityID == "A11" || facilityID == "FAI" || facilityID == "ZAN" {
		area = "alaska"
	}

	// The weather radar image comes via a WMS GetMap request to the Iowa
	// Environmental Mesonet's NEXRAD n0q (0.5 dBZ resolution base
	// reflectivity) composites; this is the same source that backs NOAA's
	// radar map at https://www.ncei.noaa.gov/maps/radar/. No-data pixels
	// are returned fully transparent.
	//
	// Relevant background:
	// https://mesonet.agron.iastate.edu/ogc/
	// https://mesonet.agron.iastate.edu/cgi-bin/wms/nexrad/n0q.cgi?SERVICE=WMS&VERSION=1.1.1&REQUEST=GetCapabilities
	layer := map[string]string{
		"conus":  "nexrad-n0q-900913-conus",
		"alaska": "nexrad-n0q-900913-ak",
		"hawaii": "nexrad-n0q-900913-hi",
	}[area]
	params := url.Values{}
	params.Add("SERVICE", "WMS")
	params.Add("VERSION", "1.1.1")
	params.Add("REQUEST", "GetMap")
	params.Add("FORMAT", "image/png")
	params.Add("TRANSPARENT", "true")
	params.Add("STYLES", "")
	params.Add("SRS", "EPSG:4326")
	params.Add("WIDTH", fmt.Sprintf("%d", wpx))
	params.Add("HEIGHT", fmt.Sprintf("%d", hpx))
	params.Add("LAYERS", layer)
	params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", bbox.P0[0], bbox.P0[1], bbox.P1[0], bbox.P1[1]))

	url := "https://mesonet.agron.iastate.edu/cgi-bin/wms/nexrad/n0q.cgi?" + params.Encode()

	for {
		var havePrecip bool

		tryFetch := func() Status {
			resp, err := http.Get(url)
			if err != nil {
				LogError("%s: %s: %v", facilityID, url, err)
				return StatusTransientFailure
			}
			defer resp.Body.Close()

			b, err := io.ReadAll(resp.Body)
			if err != nil {
				LogError("%s: %s: %v", facilityID, url, err)
				return StatusTransientFailure
			}

			var status Status
			havePrecip, status = func() (bool, Status) {
				// Decode and see if there's anything there: no-data
				// pixels are fully transparent, so any pixel with
				// nonzero alpha is a radar return.
				img, err := png.Decode(bytes.NewReader(b))
				if err != nil {
					LogError("%s: %s: %v", facilityID, url, err)
					return false, StatusTransientFailure
				}

				if nimg, ok := img.(*image.NRGBA); !ok {
					// This path is much slower but we'll keep it
					// around for robustness in case the image format
					// somehow changes.
					LogError("%s: %s: PNG is not an *image.NRGBA", facilityID, url)
					bounds := img.Bounds()
					for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
						for x := bounds.Min.X; x < bounds.Max.X; x++ {
							if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
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
							if pix[offset+3] != 0 {
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
				Source     string
				// Pixel dimensions of PNG and the geographic bounds of
				// the fetch; wxingest carries these through so displays
				// don't have to reconstruct the extent.
				NX, NY int
				Bounds math.Extent2D
			}
			blob := WX{
				PNG:        b,
				Resolution: wpx,
				Latitude:   center[1],
				Longitude:  center[0],
				Source:     string(wx.PrecipSourceIEMN0Q),
				NX:         wpx,
				NY:         hpx,
				Bounds:     bbox,
			}

			path := filepath.Join("scrape", "WX", facilityID, time.Now().UTC().Format(time.RFC3339)+".gob")

			objw := bucket.Object(path).NewWriter(ctx)

			if err := gob.NewEncoder(objw).Encode(blob); err != nil {
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
			LogInfo("Got precip for %s have precip %v", facilityID, havePrecip)

			<-tick

			if !havePrecip {
				// No weather; sleep for 30 minutes rather than 5
				for range 5 {
					<-tick
				}
			}
		} else {
			LogError("%s: unable to fetch precip", facilityID)
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

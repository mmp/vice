package main

import (
	"bytes"
	_ "embed"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: wxscrape <scrape-dir>\n")
		os.Exit(1)
	}

	if err := os.Chdir(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	launchHTTPServer()

	go fetchMETAR()
	go fetchWX()
	go fetchHRRR()
	go fetchTFRs()

	for {
		time.Sleep(time.Hour)
	}
}

func LogInfo(msg string, args ...any) {
	log.Printf("INFO "+msg, args...)
}

var errors []string

func LogError(msg string, args ...any) {
	log.Printf("ERROR "+msg, args...)
	errors = append(errors, fmt.Sprintf(time.Now().Format(time.DateTime)+" "+msg, args...))
}

// https://adip.faa.gov/agis/public/#/airportSearch/advanced -> control tower Yes
// then added K/P prefixes
// and then culled out ones with no METAR
// ->
//
//go:embed metar-airports.txt
var metarAirports string

func fetchMETAR() {
	os.Mkdir("metar", 0755)

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

			if doWithBackoff(func() Status { return fetchAirportMETAR(ap) }) {
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
	StatusPermanentFailure
)

func doWithBackoff(f func() Status) bool {
	backoff := 5 * time.Second
	for range 5 {
		switch f() {
		case StatusSuccess:
			return true

		case StatusTransientFailure:
			time.Sleep(backoff)
			backoff *= 2

		case StatusPermanentFailure:
			return false
		}
	}
	return false // unsuccessful after multiple retries
}

func downloadToFile(url, filename string, compress bool) Status {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		LogError("%s: %v", url, err)
		return StatusTransientFailure
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		LogError("%s: HTTP status code %d", url, resp.StatusCode)
		if resp.StatusCode >= 500 {
			return StatusTransientFailure
		}
		return StatusPermanentFailure
	}

	if compress {
		filename += ".zst"
	}

	tmpFile := filename + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		LogError("%s: %v", tmpFile, err)
		return StatusPermanentFailure
	}

	if compress {
		zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if err != nil {
			f.Close()
			os.Remove(tmpFile)
			LogError("%s: %v", tmpFile, err)
			return StatusTransientFailure
		}

		_, err = io.Copy(zw, resp.Body)
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmpFile)
			LogError("%s: %v", tmpFile, err)
			return StatusTransientFailure
		}

		if err = zw.Close(); err != nil {
			f.Close()
			os.Remove(tmpFile)
			LogError("%s: %v", tmpFile, err)
			return StatusTransientFailure
		}
	} else {
		_, err = io.Copy(f, resp.Body)
		if err != nil {
			f.Close()
			os.Remove(tmpFile)
			LogError("%s: %v", tmpFile, err)
			return StatusTransientFailure
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpFile)
		LogError("%s: %v", tmpFile, err)
		return StatusPermanentFailure
	}

	if err := os.Rename(tmpFile, filename); err != nil {
		os.Remove(tmpFile)
		LogError("%s -> %s: %v", tmpFile, filename, err)
		return StatusPermanentFailure
	}

	return StatusSuccess
}

func fetchAirportMETAR(ap string) Status {
	_ = os.Mkdir(filepath.Join("metar", ap), 0755)

	const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json&hours=%d`
	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, ap, 36 /* hours */)

	fn := filepath.Join("metar", ap, time.Now().Format(time.RFC3339)+".txt")
	status := downloadToFile(requestUrl, fn, true /* compress */)
	if status == StatusSuccess {
		LogInfo("%s: downloaded METAR data", ap)
	}
	return status
}

//go:embed tracon-airports.json
var traconAirports []byte

type TraconSpec struct {
	Airport   string  `json:"airport"`
	Latitude  float32 `json:"latitude"`
	Longitude float32 `json:"longitude"`
	Distance  float32 `json:"distance"` // 128 by default
}

func fetchWX() {
	os.Mkdir("WX", 0755)

	var tracons map[string]TraconSpec
	if err := json.Unmarshal(traconAirports, &tracons); err != nil {
		LogError("tracon-airports.json: %v", err)
	}
	for tracon, spec := range tracons {
		go fetchTraconWX(tracon, spec)
	}
}

// fetchTraconWX runs asynchronously in a goroutine and fetches radar
// images for a single TRACON and writes them to disk.
func fetchTraconWX(tracon string, spec TraconSpec) {
	os.Mkdir(filepath.Join("WX", tracon), 0755)

	// Spread out the requests temporally
	time.Sleep(time.Duration(rand.Intn(200)) * time.Second)

	tick := time.Tick(5 * time.Minute)

	fetchDistance := spec.Distance
	if fetchDistance == 0 { // unspecified
		// Fetch this many nm out from the center
		const wxFetchDistance = 128
		fetchDistance = wxFetchDistance
	}
	fetchResolution := int(4 * fetchDistance)

	// Figure out how far out in degrees latitude / longitude to fetch.
	// Latitude is easy: 60nm per degree
	dlat := float32(fetchDistance) / 60
	// Longitude: figure out nm per degree at center
	nmPerLong := 60 * math.Cos(float64(spec.Latitude*math.Pi/180))
	dlong := fetchDistance / float32(nmPerLong)

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
	params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", spec.Longitude-dlong, spec.Latitude-dlat,
		spec.Longitude+dlong, spec.Latitude+dlat))

	url := "https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?" + params.Encode()

	for {
		var haveWX bool

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

			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				LogError("%s: %s: %v", tracon, url, err)
				return StatusTransientFailure
			}

			// Is it all-white?
			haveWX = false
			bounds := img.Bounds()
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					if r != 0xffff || g != 0xffff || b != 0xffff {
						haveWX = true
						break
					}
				}
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
				Latitude:   spec.Latitude,
				Longitude:  spec.Longitude,
			}

			fn := filepath.Join("WX", tracon, time.Now().UTC().Format(time.RFC3339)+".gob.tmp")
			f, err := os.Create(fn)
			if err != nil {
				log.Print(err)
				return StatusPermanentFailure
			}

			if err := gob.NewEncoder(f).Encode(wx); err != nil {
				log.Print(err)
				f.Close()
				os.Remove(fn)
				return StatusPermanentFailure
			}

			if err := f.Close(); err != nil {
				log.Print(err)
				os.Remove(fn)
				return StatusPermanentFailure
			}

			if err := os.Rename(fn, strings.TrimSuffix(fn, ".tmp")); err != nil {
				log.Print(err)
				os.Remove(fn + ".tmp")
				return StatusPermanentFailure
			}

			return StatusSuccess
		}

		if doWithBackoff(tryFetch) {
			LogInfo("Got WX for %s centered at %s, have WX %v", tracon, spec.Airport, haveWX)

			<-tick

			if !haveWX {
				// No weather; sleep for 30 minutes rather than 5
				for range 5 {
					<-tick
				}
			}
		} else {
			LogError("%s: unable to fetch WX", tracon)
			<-tick
		}
	}
}

func fetchHRRR() {
	os.Mkdir("HRRR", 0755)

	const baseURL = "https://nomads.ncep.noaa.gov/pub/data/nccf/com/hrrr/prod/hrrr.%s/conus/hrrr.t%02dz.wrfprsf00.grib2"

	tick := time.Tick(5 * time.Minute)

	// Start 24 hours in the past
	fetchTime := time.Now().Add(-24 * time.Hour).UTC()

	for {
		// Don't try to fetch stuff that is too recent
		for time.Since(fetchTime) < 3*time.Hour {
			<-tick
		}

		date := fetchTime.Format("20060102")
		url := fmt.Sprintf(baseURL, date, fetchTime.Hour())
		fn := filepath.Join("HRRR", date+"-"+path.Base(url))

		if f, err := os.Open(fn); err == nil {
			LogInfo("%s: already exists. Skipping fetch", fn)
			fetchTime = fetchTime.Add(time.Hour)
			f.Close()
			continue
		}

		tryFetch := func() Status {
			LogInfo("Attempting to fetch %s", url)
			return downloadToFile(url, fn, false /* don't compress */)
		}

		if doWithBackoff(tryFetch) {
			LogInfo("%s: finished download", fn)
			fetchTime = fetchTime.Add(time.Hour)
		} else {
			LogError("%s: unable to download", fn)
			<-tick
		}
	}
}

func fetchTFRs() {
	os.Mkdir("tfrs", 0755)

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
				xmlFile := filepath.Join("tfrs", safeID+".xml")

				// Check if already downloaded
				if _, err := os.Stat(xmlFile); err == nil {
					LogInfo("TFR %s already downloaded", tfr.NotamID)
					continue
				}

				// Download the TFR
				url := fmt.Sprintf("https://tfr.faa.gov/download/detail_%s.xml", safeID)
				if downloadToFile(url, xmlFile, true /* compress */) != StatusSuccess {
					LogError("Failed to download TFR %s", tfr.NotamID)
				} else {
					LogInfo("Downloaded TFR %s", tfr.NotamID)
				}

				// Small delay between downloads
				time.Sleep(time.Second)
			}

			return StatusSuccess
		})

		<-tick
	}
}

///////////////////////////////////////////////////////////////////////////

var startTime time.Time

func launchHTTPServer() {
	startTime = time.Now()

	mux := http.NewServeMux()

	mux.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		statsHandler(w, r)
	})

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if listener, err := net.Listen("tcp", ":8001"); err == nil {
		LogInfo("Launching HTTP server on port 8001")
		go http.Serve(listener, mux)
	} else {
		fmt.Fprintf(os.Stderr, "Unable to start HTTP server: %v", err)
		os.Exit(1)
	}
}

type serverStats struct {
	Uptime           time.Duration
	AllocMemory      uint64
	TotalAllocMemory uint64
	SysMemory        uint64
	NumGC            uint32
	NumGoRoutines    int
	Errors           string
}

func formatBytes(v int64) string {
	if v < 1024 {
		return fmt.Sprintf("%d B", v)
	} else if v < 1024*1024 {
		return fmt.Sprintf("%d KiB", v/1024)
	} else if v < 1024*1024*1024 {
		return fmt.Sprintf("%d MiB", v/1024/1024)
	} else {
		return fmt.Sprintf("%d GiB", v/1024/1024/1024)
	}
}

var templateFuncs = template.FuncMap{"bytes": formatBytes}

var statsTemplate = template.Must(template.New("").Funcs(templateFuncs).Parse(`
<!DOCTYPE html>
<html>
<head>
<title>wxscrape status</title>
</head>
<style>
table {
  border-collapse: collapse;
  width: 100%;
}

th, td {
  border: 1px solid #dddddd;
  padding: 8px;
  text-align: left;
}

tr:nth-child(even) {
  background-color: #f2f2f2;
}

#log {
    font-family: "Courier New", monospace;  /* use a monospace font */
    width: 100%;
    height: 500px;
    font-size: 12px;
    overflow: auto;  /* add scrollbars as necessary */
    white-space: pre-wrap;  /* wrap text */
    border: 1px solid #ccc;
    padding: 10px;
}
</style>
<body>
<h1>Status</h1>
<ul>
  <li>Uptime: {{.Uptime}}</li>
  <li>Allocated memory: {{.AllocMemory}} MB</li>
  <li>Total allocated memory: {{.TotalAllocMemory}} MB</li>
  <li>System memory: {{.SysMemory}} MB</li>
  <li>Garbage collection passes: {{.NumGC}}</li>
  <li>Running goroutines: {{.NumGoRoutines}}</li>
</ul>

<h1>Errors</h1>
<pre>
{{.Errors}}
</pre>

</body>
</html>
`))

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	errs := slices.Clone(errors)
	slices.Reverse(errs)

	stats := serverStats{
		Uptime:           time.Since(startTime).Round(time.Second),
		AllocMemory:      m.Alloc / (1024 * 1024),
		TotalAllocMemory: m.TotalAlloc / (1024 * 1024),
		SysMemory:        m.Sys / (1024 * 1024),
		NumGC:            m.NumGC,
		NumGoRoutines:    runtime.NumGoroutine(),
		Errors:           strings.Join(errs, "\n"),
	}

	statsTemplate.Execute(w, stats)
}

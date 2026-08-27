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
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	bucket := client.Bucket(bucketName)

	h := &health{start: time.Now()}

	if metar {
		airports := metarAirports()
		LogInfo("%d METAR airports", len(airports))
		go fetchMETAR(ctx, bucket, airports, h.add("METAR", metarOverdue, airports))
	}
	if precip {
		// Registers a source per facility and starts a goroutine for each,
		// then returns.
		fetchPRECIP(ctx, bucket, h)
	}
	if tfrs {
		// Only newly-issued TFRs are uploaded, so there is no cadence to
		// hold the scraper to here.
		go fetchTFRs(ctx, bucket, h.add("TFR", 0, nil))
	}

	// Only once every category has been registered, so that the handler
	// never races with h.add.
	launchHTTPServer(h)

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
var metarAirportsFile string

const (
	// metarRefetch is how often each airport is refetched: well inside the
	// 36 hours of history that a fetch returns, so that consecutive fetches
	// overlap and leave no holes.
	metarRefetch = 20 * time.Hour
	// The airports that come due are worked through back-to-back at one per
	// 21 seconds, and a full pass over all of them takes about four hours,
	// so an airport can legitimately run several hours past due before the
	// scraper reaches it.
	metarOverdue = 26 * time.Hour
)

// metarAirports returns the airports to fetch METARs for.
func metarAirports() []string {
	var airports []string
	for ap := range strings.Lines(metarAirportsFile) {
		airports = append(airports, strings.TrimSpace(ap))
	}
	return airports
}

func fetchMETAR(ctx context.Context, bucket *storage.BucketHandle, airports []string, c *category) {
	tick := time.Tick(21 * time.Second)
	tock := time.Tick(time.Hour)
	for {
		perm := rand.Perm(len(airports))
		for _, i := range perm {
			ap := airports[i]
			if age, ok := c.sinceFetch(ap); !ok {
				LogInfo("%s: fetching METAR: no previous fetch", ap)
			} else if age < metarRefetch {
				LogInfo("%s: skipping METAR fetch, last fetch %s ago", ap, age)
				continue
			} else {
				LogInfo("%s: fetching METAR: last fetch %s ago", ap, age)
			}

			if doWithBackoff(func() Status { return fetchAirportMETAR(ctx, bucket, ap, c) }) {
				c.markFetched(ap)
			} else {
				c.recordFailure("%s: unable to fetch METAR; giving up for this cycle", ap)
			}

			<-tick
		}

		<-tock
	}
}

type Status int

const (
	StatusSuccess Status = iota
	StatusNoData
	StatusTransientFailure
)

// doWithBackoff retries f until it stops reporting a transient failure,
// returning whether it got that far. A source with nothing to give is not
// retried: a station that has stopped publishing--because its airport was
// re-identified, say--would otherwise burn the entire backoff every cycle.
func doWithBackoff(f func() Status) bool {
	backoff := 5 * time.Second
	for range 7 {
		switch f() {
		case StatusSuccess, StatusNoData:
			return true

		case StatusTransientFailure:
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return false // unsuccessful after multiple retries
}

// The largest ARTCC radar images take the longest to come back; METAR
// responses are small.
var httpClient = &http.Client{Timeout: 60 * time.Second}

func downloadToGCS(ctx context.Context, bucket *storage.BucketHandle, url, objpath string) Status {
	resp, err := httpClient.Get(url)
	if err != nil {
		LogError("%s: %v", url, err)
		return StatusTransientFailure
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return StatusNoData
	}
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

func fetchAirportMETAR(ctx context.Context, bucket *storage.BucketHandle, ap string, c *category) Status {
	const aviationWeatherCenterDataApi = `https://aviationweather.gov/api/data/metar?ids=%s&format=json&hours=%d`
	requestUrl := fmt.Sprintf(aviationWeatherCenterDataApi, ap, 36 /* hours */)

	path := filepath.Join("scrape", "metar", ap, time.Now().Format(time.RFC3339)+".txt")
	status := downloadToGCS(ctx, bucket, requestUrl, path)
	switch status {
	case StatusSuccess:
		c.recordUpload(path)
		LogInfo("%s: downloaded METAR data", ap)
	case StatusNoData:
		LogError("%s: publishes no METAR", ap)
	}
	return status
}

const (
	// precipRefetch is how often each facility's radar image is refetched;
	// a facility with no returns waits six of those instead of one.
	precipRefetch = 5 * time.Minute
	// precipOverdue covers that quiet cycle plus a full retry budget.
	precipOverdue = 50 * time.Minute
)

func fetchPRECIP(ctx context.Context, bucket *storage.BucketHandle, h *health) {
	av.InitDB()

	// A facility that doesn't resolve is left out entirely rather than
	// registered and never fetched, which would pin the category to STALE.
	facs := make(map[string]av.Facility)
	addFacility := func(id string) {
		if fac, ok := av.DB.LookupFacility(id); !ok {
			LogError("%s: unable to find facility info", id)
		} else {
			facs[id] = fac
		}
	}
	for tracon := range av.DB.TRACONs {
		addFacility(tracon)
	}
	for artcc := range av.DB.ARTCCs {
		addFacility(artcc)
	}

	ids := slices.Sorted(maps.Keys(facs))
	c := h.add("precip", precipOverdue, ids)
	for _, id := range ids {
		go fetchFacilityPrecip(ctx, bucket, id, facs[id], c)
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
func fetchFacilityPrecip(ctx context.Context, bucket *storage.BucketHandle, facilityID string,
	fac av.Facility, c *category) {
	// Spread out the requests temporally
	time.Sleep(time.Duration(rand.IntN(200)) * time.Second)

	tick := time.Tick(precipRefetch)

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
			resp, err := httpClient.Get(url)
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

			c.recordUpload(path)

			return StatusSuccess
		}

		if doWithBackoff(tryFetch) {
			LogInfo("Got precip for %s have precip %v", facilityID, havePrecip)
			c.markFetched(facilityID)

			<-tick

			if !havePrecip {
				// No weather; sleep for 30 minutes rather than 5
				for range 5 {
					<-tick
				}
			}
		} else {
			c.recordFailure("%s: unable to fetch precip", facilityID)
			<-tick
		}
	}
}

func fetchTFRs(ctx context.Context, bucket *storage.BucketHandle, c *category) {
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
					c.recordFailure("Failed to download TFR %s", tfr.NotamID)
				} else {
					LogInfo("Downloaded TFR %s", tfr.NotamID)
					c.recordUpload(path)
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

// Ports for the two HTTP servers. Only healthPort is reachable from outside
// GCE, via the "wxscrape-health" firewall rule; profiling stays on the
// private port and is reached through an ssh tunnel.
const (
	pprofPort  = 8002
	healthPort = 8003
)

const nRecentUploads = 8

type uploadRecord struct {
	when time.Time
	path string
}

// category records the scraper's progress for one class of scraped object.
// Freshness is measured per source--an airport, a facility--rather than from
// the last upload of any kind: METAR in particular refetches each airport
// only every 20 hours and works through the ones that come due back-to-back,
// so it is silent for most of the day by design.
type category struct {
	name string
	// overdueAfter is the longest a source may go between successful
	// fetches; zero for a category whose fetches are event-driven and have
	// no schedule to hold them to.
	overdueAfter time.Duration
	// sources in the order they were registered, so that the status text
	// names the same laggard from one request to the next.
	sources []string

	mu              sync.Mutex
	uploads         int
	recent          []uploadRecord       // most recent first
	fetched         map[string]time.Time // last successful fetch; zero if not yet fetched
	failures        int
	lastFailure     time.Time
	lastFailureText string
}

func (c *category) recordUpload(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.uploads++
	c.recent = slices.Insert(c.recent, 0, uploadRecord{when: time.Now(), path: path})
	if len(c.recent) > nRecentUploads {
		c.recent = c.recent[:nRecentUploads]
	}
}

// markFetched notes that source was fetched successfully, whether or not
// that fetch had anything to upload.
func (c *category) markFetched(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fetched[source] = time.Now()
}

// sinceFetch returns how long it has been since source was last fetched
// successfully, and whether it has ever been fetched at all.
func (c *category) sinceFetch(source string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.fetched[source]
	if !ok {
		return 0, false
	}
	return time.Since(t), true
}

// recordFailure notes that the scraper gave up on a fetch after exhausting
// its retries, logging it as well.
func (c *category) recordFailure(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	LogError("%s", msg)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.failures++
	c.lastFailure = time.Now()
	c.lastFailureText = msg
}

// oldestFetch returns the source that has gone longest without a successful
// fetch and how long that has been, measuring a source not yet fetched from
// start so that a freshly-started scraper is not immediately reported as
// stale. The source is "" for a category that tracks no sources. The caller
// must hold c.mu.
func (c *category) oldestFetch(start time.Time) (string, time.Duration) {
	var oldest string
	var since time.Time
	for _, src := range c.sources {
		t := c.fetched[src]
		if t.IsZero() {
			t = start
		}
		if oldest == "" || t.Before(since) {
			oldest, since = src, t
		}
	}
	if oldest == "" {
		return "", 0
	}
	return oldest, time.Since(since)
}

// writeStatus reports the category's activity and returns whether its
// sources are still being fetched on schedule.
func (c *category) writeStatus(w io.Writer, start time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	src, age := c.oldestFetch(start)
	ok := c.overdueAfter == 0 || age <= c.overdueAfter

	state := "ok"
	if !ok {
		state = "STALE"
	}
	fmt.Fprintf(w, "%s: %s", c.name, state)
	if len(c.recent) > 0 {
		fmt.Fprintf(w, ", last upload %s ago", time.Since(c.recent[0].when).Round(time.Second))
	} else {
		fmt.Fprintf(w, ", no uploads yet")
	}
	if src != "" {
		fmt.Fprintf(w, ", oldest fetch %s %s ago", src, age.Round(time.Second))
		if !ok {
			fmt.Fprintf(w, ", expected within %s", c.overdueAfter)
		}
	}
	fmt.Fprintf(w, " (%d uploads, %d failures)\n", c.uploads, c.failures)

	for _, u := range c.recent {
		fmt.Fprintf(w, "  %s %s\n", u.when.UTC().Format(time.RFC3339), u.path)
	}
	if c.failures > 0 {
		fmt.Fprintf(w, "  last failure %s ago: %s\n", time.Since(c.lastFailure).Round(time.Second),
			c.lastFailureText)
	}

	return ok
}

// health serves a plain-text summary of what the scraper has uploaded
// recently, for monitoring from outside.
type health struct {
	start time.Time
	cats  []*category
}

// add registers a category of scraped object. sources names the individual
// things the scraper refetches--airports, facilities--each of which is
// expected to be fetched again within overdueAfter; pass none for a category
// whose fetches are event-driven.
func (h *health) add(name string, overdueAfter time.Duration, sources []string) *category {
	c := &category{
		name:         name,
		overdueAfter: overdueAfter,
		sources:      sources,
		fetched:      make(map[string]time.Time),
	}
	h.cats = append(h.cats, c)
	return c
}

func (h *health) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body strings.Builder
	ok := true
	for _, c := range h.cats {
		ok = c.writeStatus(&body, h.start) && ok
	}

	status := "ok"
	if !ok {
		status = "stale"
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "status: %s\nuptime: %s\n", status, time.Since(h.start).Round(time.Second))
	io.WriteString(w, body.String())
}

func launchHTTPServer(h *health) {
	mux := http.NewServeMux()

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	serve(pprofPort, mux)

	healthMux := http.NewServeMux()
	healthMux.Handle("/health", h)
	serve(healthPort, healthMux)
}

func serve(port int, mux *http.ServeMux) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to start HTTP server on port %d: %v", port, err)
		os.Exit(1)
	}

	LogInfo("Launching HTTP server on port %d", port)
	go http.Serve(listener, mux)
}

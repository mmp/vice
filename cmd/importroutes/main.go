// cmd/importroutes/main.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Imports the FAA's city-pair route databases into resources/routes.csv.zst,
// which real-world scheduled traffic uses to file realistic routes. Input is
// the preferred routes and coded departure route (CDR) files published by the
// FAA's Route Management Tool; both are downloaded when not given on the
// command line:
//
//	https://www.fly.faa.gov/rmt/data_file/prefroutes_db.csv
//	https://www.fly.faa.gov/rmt/data_file/codedswap_db.csv
//
// Preferred routes are the FAA's normal-day routes and are kept as they are.
// CDRs are contingency reroutes, most of them for weather, so only the most
// direct one for each city pair is kept--plus the most direct conventional
// one when the most direct needs RNAV, so non-RNAV aircraft still have a real
// route to file. CDRs that require coordination with ATC are dropped: they
// are not flown on a normal day.
//
// Must be run from the top of a Vice checkout so that the resources are found.
//
// Usage:
//
//	importroutes [-prefroutes file] [-cdr file] [-out file] [-dryrun]

package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"

	"github.com/klauspost/compress/zstd"
)

const (
	prefroutesURL = "https://www.fly.faa.gov/rmt/data_file/prefroutes_db.csv"
	cdrURL        = "https://www.fly.faa.gov/rmt/data_file/codedswap_db.csv"
)

func main() {
	prefroutesPath := flag.String("prefroutes", "", "`path` to prefroutes_db.csv; downloaded if not given")
	cdrPath := flag.String("cdr", "", "`path` to codedswap_db.csv; downloaded if not given")
	out := flag.String("out", "resources/routes.csv.zst", "`file` to write the route database to")
	dryRun := flag.Bool("dryrun", false, "report what would be written without writing it")
	flag.Parse()

	av.InitDB()

	prefroutesData, err := readSource(*prefroutesPath, prefroutesURL)
	if err != nil {
		fmt.Printf("preferred routes: %v\n", err)
		os.Exit(1)
	}
	cdrData, err := readSource(*cdrPath, cdrURL)
	if err != nil {
		fmt.Printf("coded departure routes: %v\n", err)
		os.Exit(1)
	}

	routes, err := parsePrefroutes(prefroutesData)
	if err != nil {
		fmt.Printf("preferred routes: %v\n", err)
		os.Exit(1)
	}
	cdrs, err := parseCDRs(cdrData)
	if err != nil {
		fmt.Printf("coded departure routes: %v\n", err)
		os.Exit(1)
	}

	airportLocation := func(icao string) (math.Point2LL, bool) {
		ap, ok := av.DB.Airports[icao]
		return ap.Location, ok
	}
	routes = append(routes, cullCDRs(cdrs, airportLocation, av.DB.LookupWaypoint)...)
	routes = dedupe(routes)

	slices.SortFunc(routes, compareRoutes)

	pairs := make(map[av.AirportPair]bool)
	for _, r := range routes {
		pairs[av.AirportPair{From: r.orig, To: r.dest}] = true
	}
	fmt.Printf("Keeping %d routes between %d city pairs\n", len(routes), len(pairs))

	if *dryRun {
		return
	}
	if err := write(routes, *out); err != nil {
		fmt.Printf("%s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\n", *out)
}

// route is one row of the output database.
type route struct {
	orig, dest string // ICAO
	typ        string // TEC, H, L, NAR, SHD, HSD, SLD, or CDR
	depFix     string
	aircraft   string // "jet", "prop", or "" for no restriction
	rnav       bool
	route      string
	seq        int // preference order within a city pair and type
}

// readSource returns the file's contents, downloading the FAA's copy when no
// path is given. The FAA files start with a UTF-8 byte-order mark, which is
// stripped so that the first header field parses.
func readSource(path, url string) ([]byte, error) {
	var b []byte
	var err error
	if path != "" {
		b, err = os.ReadFile(path)
	} else {
		fmt.Printf("Downloading %s\n", url)
		var resp *http.Response
		if resp, err = http.Get(url); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("%s: %s", url, resp.Status)
			}
			b, err = io.ReadAll(resp.Body)
		}
	}
	return bytes.TrimPrefix(b, []byte("\ufeff")), err
}

// parseCSV calls the callback with the requested columns of each row.
func parseCSV(b []byte, fields []string, callback func([]string)) error {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return err
	}
	var indices []int
	for _, f := range fields {
		i := slices.IndexFunc(header, func(h string) bool { return strings.TrimSpace(h) == f })
		if i == -1 {
			return fmt.Errorf("column %q not found in header", f)
		}
		indices = append(indices, i)
	}

	var strs []string
	for {
		record, err := cr.Read()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		for _, i := range indices {
			strs = append(strs, record[i])
		}
		callback(strs)
		strs = strs[:0]
	}
}

func parsePrefroutes(b []byte) ([]route, error) {
	var routes []route
	var read, unknownAirport, emptyRoute int
	err := parseCSV(b, []string{"Orig", "Route String", "Dest", "Type", "Aircraft", "Seq"},
		func(s []string) {
			read++
			orig, origOK := av.DB.LookupAirport(strings.TrimSpace(s[0]))
			dest, destOK := av.DB.LookupAirport(strings.TrimSpace(s[2]))
			if !origOK || !destOK || orig.Id == dest.Id {
				unknownAirport++
				return
			}
			routeStr := strings.Join(strings.Fields(s[1]), " ")
			if routeStr == "" {
				emptyRoute++
				return
			}
			aircraft, rnav := classifyAircraft(s[4])
			seq, _ := strconv.Atoi(strings.TrimSpace(s[5]))
			routes = append(routes, route{
				orig:     orig.Id,
				dest:     dest.Id,
				typ:      strings.ToUpper(strings.TrimSpace(s[3])),
				aircraft: aircraft,
				rnav:     rnav,
				route:    routeStr,
				seq:      seq,
			})
		})

	fmt.Printf("Preferred routes: read %d, kept %d (%d with an unusable airport, %d with no route)\n",
		read, len(routes), unknownAirport, emptyRoute)
	return routes, err
}

func parseCDRs(b []byte) ([]route, error) {
	var routes []route
	var read, coordinationRequired, unknownAirport, emptyRoute int
	err := parseCSV(b, []string{"Orig", "Dest", "DepFix", "Route String", "CoordReq", "NavEqp"},
		func(s []string) {
			read++
			if strings.TrimSpace(s[4]) == "Y" {
				// Using the route needs coordination with ATC, so it isn't
				// flown on a normal day.
				coordinationRequired++
				return
			}
			orig, origOK := av.DB.LookupAirport(strings.TrimSpace(s[0]))
			dest, destOK := av.DB.LookupAirport(strings.TrimSpace(s[1]))
			if !origOK || !destOK || orig.Id == dest.Id {
				unknownAirport++
				return
			}
			routeStr := strings.Join(strings.Fields(s[3]), " ")
			if routeStr == "" {
				emptyRoute++
				return
			}
			routes = append(routes, route{
				orig:   orig.Id,
				dest:   dest.Id,
				typ:    "CDR",
				depFix: strings.TrimSpace(s[2]),
				// NavEqp 1 is conventional navigation; 2 and 3 need RNAV.
				rnav:  strings.TrimSpace(s[5]) != "1",
				route: routeStr,
			})
		})

	fmt.Printf("Coded departure routes: read %d, kept %d (%d requiring coordination, %d with an unusable airport, %d with no route)\n",
		read, len(routes), coordinationRequired, unknownAirport, emptyRoute)
	return routes, err
}

// classifyAircraft reduces the free-text aircraft restriction from the
// preferred routes database to what the sim can act on: jets only, props
// only, or no restriction, plus whether RNAV equipment is called for.
func classifyAircraft(s string) (aircraft string, rnav bool) {
	s = strings.ToUpper(s)
	rnav = strings.Contains(s, "RNAV") || strings.Contains(s, "GPS")
	switch {
	case strings.Contains(s, "NON-JET"), strings.Contains(s, "NON JET"),
		strings.Contains(s, "SINGLE ENGINE"),
		strings.Contains(s, "PROP") && !strings.Contains(s, "JET"):
		aircraft = "prop"
	case strings.Contains(s, "JET"):
		aircraft = "jet"
	}
	return
}

// cullCDRs keeps the most direct CDR for each city pair: CDRs exist to give
// alternatives for weather and congestion, and keeping the rare ones would
// have the sim flying esoteric routings on a clear day. When the most direct
// route needs RNAV, the most direct conventional one is kept as well.
func cullCDRs(cdrs []route, airportLocation, fixLocation func(string) (math.Point2LL, bool)) []route {
	best := make(map[av.AirportPair]route)
	bestConventional := make(map[av.AirportPair]route)
	ratios := make(map[av.AirportPair]float32)
	conventionalRatios := make(map[av.AirportPair]float32)

	for _, r := range cdrs {
		orig, origOK := airportLocation(r.orig)
		dest, destOK := airportLocation(r.dest)
		if !origOK || !destOK {
			continue
		}
		ratio := routeLengthRatio(orig, dest, r.route, fixLocation)

		pair := av.AirportPair{From: r.orig, To: r.dest}
		if prev, ok := ratios[pair]; !ok || ratio < prev {
			best[pair], ratios[pair] = r, ratio
		}
		if !r.rnav {
			if prev, ok := conventionalRatios[pair]; !ok || ratio < prev {
				bestConventional[pair], conventionalRatios[pair] = r, ratio
			}
		}
	}

	var kept int
	var routes []route
	for pair, r := range best {
		r.seq = 1
		routes = append(routes, r)
		kept++
		if r.rnav {
			if alt, ok := bestConventional[pair]; ok {
				alt.seq = 2
				routes = append(routes, alt)
				kept++
			}
		}
	}

	fmt.Printf("Culled coded departure routes to %d, the most direct for each city pair\n", kept)
	return routes
}

// routeLengthRatio returns the length of the route relative to flying direct:
// the route's resolvable fixes are joined by great circles, and airway and
// procedure names, which don't resolve, are skipped.
func routeLengthRatio(orig, dest math.Point2LL, routeStr string,
	fixLocation func(string) (math.Point2LL, bool)) float32 {
	points := []math.Point2LL{orig}
	for _, token := range strings.Fields(routeStr) {
		if p, ok := fixLocation(token); ok {
			points = append(points, p)
		}
	}
	points = append(points, dest)

	var length float32
	for i := range len(points) - 1 {
		length += math.NMDistance2LL(points[i], points[i+1])
	}
	return length / max(1, math.NMDistance2LL(orig, dest))
}

// dedupe drops routes whose route string duplicates an earlier entry for the
// same city pair; with preferred routes parsed first, a CDR that matches one
// is the one dropped.
func dedupe(routes []route) []route {
	seen := make(map[string]bool)
	var kept []route
	for _, r := range routes {
		key := r.orig + " " + r.dest + " " + r.route
		if !seen[key] {
			seen[key] = true
			kept = append(kept, r)
		}
	}
	return kept
}

// compareRoutes orders the output: by city pair, preferred routes before
// CDRs, then by type and the FAA's preference order.
func compareRoutes(a, b route) int {
	if c := strings.Compare(a.orig, b.orig); c != 0 {
		return c
	}
	if c := strings.Compare(a.dest, b.dest); c != 0 {
		return c
	}
	aCDR, bCDR := a.typ == "CDR", b.typ == "CDR"
	if aCDR != bCDR {
		if aCDR {
			return 1
		}
		return -1
	}
	if c := strings.Compare(a.typ, b.typ); c != 0 {
		return c
	}
	if a.seq != b.seq {
		return a.seq - b.seq
	}
	return strings.Compare(a.route, b.route)
}

func write(routes []route, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return err
	}

	cw := csv.NewWriter(zw)
	if err := cw.Write([]string{"orig", "dest", "type", "dep_fix", "acft", "rnav", "route"}); err != nil {
		return err
	}
	for _, r := range routes {
		rnav := ""
		if r.rnav {
			rnav = "Y"
		}
		if err := cw.Write([]string{r.orig, r.dest, r.typ, r.depFix, r.aircraft, rnav, r.route}); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

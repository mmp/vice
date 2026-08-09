// cmd/scraperoutes/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// scraperoutes maintains resources/scraped-routes.json, the database of
// recently filed routes that route selection consults for city pairs the FAA
// databases don't cover. It finds the pairs flown in the historical flight
// data that have no FAA route, looks each one up on FlightAware's IFR route
// analyzer, and records the routes actually filed along with how often, at
// what altitudes, by what kinds of aircraft, and at what hours of the day--
// noise abatement runs some routes only at night. Rarely filed routes, a
// tenth or less as often as the pair's most common, are culled. Pairs are
// fetched most-flown first, and refetched after they go stale (60 days, by
// default).
//
//	go run ./cmd/scraperoutes [-cell N40W074] [-limit 25] [-dryrun]
package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

func main() {
	cell := flag.String("cell", "", "only process pairs from this flight data `cell`, e.g. N40W074")
	limit := flag.Int("limit", 25, "maximum `number` of city pairs to fetch routes for in this run")
	minCount := flag.Int("mincount", 100, "skip pairs the flight data records fewer than `count` flights for")
	delay := flag.Duration("delay", 10*time.Second, "`wait` between web requests")
	recheck := flag.Int("recheck", 60, "refetch pairs last fetched more than `days` ago")
	dryRun := flag.Bool("dryrun", false, "report what would be recorded without updating the database")
	dbPath := flag.String("db", "resources/"+av.ScrapedRoutesPath, "scraped route database `file` to update")
	flag.Parse()

	av.InitDB()

	sets := readRouteSets(*dbPath)
	if consolidateSets(sets) {
		fmt.Printf("Consolidated previously recorded routes\n")
		if !*dryRun {
			writeRouteSets(*dbPath, sets)
		}
	}
	stale := time.Now().AddDate(0, 0, -*recheck).Format("2006-01-02")

	pairs := gatherPairs(*cell)
	pairs = util.FilterSlice(pairs, func(p pair) bool {
		if p.count < *minCount {
			return false
		}
		set, ok := sets[p.key()]
		return !ok || set.Updated <= stale
	})
	fmt.Printf("%d city pairs to fetch: no FAA route, %d or more flights, not fetched since %s\n",
		len(pairs), *minCount, stale)

	client := &http.Client{Timeout: 30 * time.Second}
	today := time.Now().Format("2006-01-02")
	fetched := 0
	for _, pr := range pairs {
		if fetched == *limit {
			break
		}
		if fetched > 0 {
			time.Sleep(*delay)
		}
		fetched++

		routes, err := fetchRoutes(client, pr.from, pr.to, domestic(pr.from), domestic(pr.to))
		if err != nil {
			fmt.Printf("%s->%s: %v\n", pr.from, pr.to, err)
			continue
		}
		routes = cullRareRoutes(routes)

		if len(routes) == 0 {
			fmt.Printf("%s->%s (%d flights): no routes found\n", pr.from, pr.to, pr.count)
		}
		for _, r := range routes {
			fmt.Printf("%s->%s (%d flights): %q %s\n", pr.from, pr.to, pr.count, r.Route, describe(r))
		}

		// An entry is recorded even with no routes, so the pair isn't asked
		// about again until it goes stale.
		sets[pr.key()] = av.ScrapedRouteSet{Updated: today, Routes: routes}
		if !*dryRun {
			writeRouteSets(*dbPath, sets)
		}
	}

	if *dryRun {
		fmt.Printf("Dry run: %s left unchanged\n", *dbPath)
	}
	if remaining := len(pairs) - fetched; remaining > 0 {
		fmt.Printf("%d pairs remain; run again to continue\n", remaining)
	}
}

// describe summarizes what was recorded with a route.
func describe(r av.ScrapedRoute) string {
	parts := []string{fmt.Sprintf("filed %d times", r.Count)}
	if r.Aircraft != 0 {
		if b, err := r.Aircraft.MarshalJSON(); err == nil {
			parts = append(parts, "by "+strings.ReplaceAll(strings.Trim(string(b), `"[]`), `"`, ""))
		}
	}
	if r.MinAltitude > 0 {
		if r.MaxAltitude > r.MinAltitude {
			parts = append(parts, fmt.Sprintf("at %d-%d", r.MinAltitude, r.MaxAltitude))
		} else {
			parts = append(parts, fmt.Sprintf("at %d", r.MinAltitude))
		}
	}
	if r.Hours != 0 {
		parts = append(parts, "hours "+r.Hours.String())
	}
	return strings.Join(parts, ", ")
}

///////////////////////////////////////////////////////////////////////////
// City pairs from the flight data

// pair is a directed city pair from the flight data and the number of
// flights recorded for it.
type pair struct {
	from, to string
	count    int
}

func (p pair) key() string { return p.from + "-" + p.to }

// gatherPairs walks the flight data and returns the directed city pairs that
// have no FAA route, most flights first. A pair is recorded in the cell of the
// airport it departs and again in the cell of the one it lands at, so its count
// is the larger of what the two give.
func gatherPairs(onlyCell string) []pair {
	resources := util.GetResourcesFS()
	files, err := fs.Glob(resources, av.FlightDataDirectory+"/*"+av.FlightDataExtension)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	counts := make(map[string]int)
	endpoints := make(map[string][2]string)
	for _, file := range files {
		cell := strings.TrimSuffix(path.Base(file), av.FlightDataExtension)
		if onlyCell != "" && !strings.EqualFold(cell, onlyCell) {
			continue
		}

		data, err := av.ReadFlightData(resources, cell)
		if err != nil || data == nil {
			continue
		}
		flights, err := av.DecodeFlights(data)
		if err != nil {
			fmt.Printf("%s: %v\n", file, err)
			continue
		}

		local := make(map[string]int)
		for _, f := range flights {
			from, to := f.Other, f.Airport
			if f.Departure {
				from, to = f.Airport, f.Other
			}
			key := from + "-" + to
			local[key]++
			endpoints[key] = [2]string{from, to}
		}
		for key, n := range local {
			counts[key] = max(counts[key], n)
		}
	}

	var pairs []pair
	for key, n := range counts {
		from, to := endpoints[key][0], endpoints[key][1]
		if madeUpAirport(from) || madeUpAirport(to) {
			continue
		}
		if _, ok := av.DB.Airports[from]; !ok {
			continue
		}
		if _, ok := av.DB.Airports[to]; !ok {
			continue
		}
		if len(av.DB.RoutesBetween(from, to)) > 0 {
			continue
		}
		pairs = append(pairs, pair{from: from, to: to, count: n})
	}

	slices.SortFunc(pairs, func(a, b pair) int {
		if a.count != b.count {
			return cmp.Compare(b.count, a.count)
		}
		return strings.Compare(a.key(), b.key())
	})
	return pairs
}

// madeUpAirport reports whether an airport is one of the fictional ones the FAA
// Academy scenarios fly. They stand on a real airport's traffic, which is why
// they turn up in the flight data at all, but no real route was ever filed to
// one and the Academy is not to be flown on real-world routes regardless.
func madeUpAirport(icao string) bool {
	_, ok := av.FlightDataSubstitutes[icao]
	return ok
}

// domestic reports whether an airport is one the FAA controls, which is what
// decides the end of an oceanic route worth keeping.
func domestic(icao string) bool { return av.DB.Airports[icao].FAAControlled() }

///////////////////////////////////////////////////////////////////////////
// FlightAware's IFR route analyzer

var (
	summaryCountRE = regexp.MustCompile(`(?s)^\s*<td class="row\d+">(\d+)</td>`)
	altitudeRE     = regexp.MustCompile(`>\s*((?:FL)?[\d,]+)(?:\s*-\s*((?:FL)?[\d,]+))?\s*</td>`)
	routeRE        = regexp.MustCompile(`(?s)skyvector\.com[^"]*"[^>]*>([^<]+)</a>`)
	itemizedTimeRE = regexp.MustCompile(`(\d{1,2}):\d{2}(AM|PM)&nbsp;<span class="tz">`)
	aircraftTypeRE = regexp.MustCompile(`/live/aircrafttype/([A-Z0-9-]+)`)
)

// fetchRoutes asks FlightAware's IFR route analyzer how the pair has
// recently been flown, most-filed routes first.
func fetchRoutes(client *http.Client, from, to string,
	fromScenario, toScenario bool) ([]av.ScrapedRoute, error) {
	u := "https://www.flightaware.com/analysis/route.rvt?origin=" + url.QueryEscape(from) +
		"&destination=" + url.QueryEscape(to)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching routes: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseAnalyzerRoutes(string(body), from, to, fromScenario, toScenario), nil
}

// parseAnalyzerRoutes reads the route analyzer's two tables. The summary rows
// lead with the number of times each route was filed and give its altitude
// range; the itemized rows below them lead with a local time of day and give
// each flight's aircraft type. Both link the route to SkyVector.
func parseAnalyzerRoutes(body, from, to string, fromScenario, toScenario bool) []av.ScrapedRoute {
	stats := make(map[string]*av.ScrapedRoute)
	statsFor := func(route string) *av.ScrapedRoute {
		if _, ok := stats[route]; !ok {
			stats[route] = &av.ScrapedRoute{Route: route}
		}
		return stats[route]
	}
	widen := func(r *av.ScrapedRoute, alt int) {
		if alt <= 0 {
			return
		}
		if r.MinAltitude == 0 || alt < r.MinAltitude {
			r.MinAltitude = alt
		}
		r.MaxAltitude = max(r.MaxAltitude, alt)
	}

	for row := range strings.SplitSeq(body, "<tr>") {
		routeMatch := routeRE.FindStringSubmatch(row)
		if routeMatch == nil {
			continue
		}
		route := consolidateRoute(cleanRoute(routeMatch[1], from, to), fromScenario, toScenario)
		if route == "" {
			continue
		}

		if count := summaryCountRE.FindStringSubmatchIndex(row); count != nil {
			n, err := strconv.Atoi(row[count[2]:count[3]])
			if err != nil {
				continue
			}
			r := statsFor(route)
			r.Count += n
			if alts := altitudeRE.FindStringSubmatch(row[count[1]:]); alts != nil {
				widen(r, parseAltitude(alts[1]))
				widen(r, parseAltitude(alts[2]))
			}
		} else if when := itemizedTimeRE.FindStringSubmatch(row); when != nil {
			r := statsFor(route)
			hour, _ := strconv.Atoi(when[1])
			hour %= 12
			if when[2] == "PM" {
				hour += 12
			}
			r.Hours.Add(hour)
			if acType := aircraftTypeRE.FindStringSubmatch(row); acType != nil {
				r.Aircraft |= av.AircraftClassOf(acType[1])
			}
			if alts := altitudeRE.FindStringSubmatch(row); alts != nil {
				widen(r, parseAltitude(alts[1]))
			}
		}
	}

	var routes []av.ScrapedRoute
	for _, r := range stats {
		if r.Count > 0 { // itemized-only rows aren't in the summary; skip them
			routes = append(routes, *r)
		}
	}
	slices.SortFunc(routes, func(a, b av.ScrapedRoute) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return strings.Compare(a.Route, b.Route)
	})
	return routes
}

// maxRoutesPerPair bounds how many routes a pair keeps. Route selection only
// ever wants a few, and anything past the first several is one-off noise.
const maxRoutesPerPair = 8

// cullRareRoutes drops the routes filed a tenth or less as often as the
// pair's most common, which are one-off reroutes rather than how the pair is
// flown, and keeps at most maxRoutesPerPair of the rest.
func cullRareRoutes(routes []av.ScrapedRoute) []av.ScrapedRoute {
	if len(routes) == 0 {
		return routes
	}
	most := routes[0].Count
	for _, r := range routes {
		most = max(most, r.Count)
	}
	routes = util.FilterSlice(routes, func(r av.ScrapedRoute) bool { return r.Count*10 > most })
	return routes[:min(len(routes), maxRoutesPerPair)]
}

// consolidateSets reapplies the route consolidation and culling to what is
// already in the database, so that entries recorded under older rules are
// cleaned up in place rather than refetched. It reports whether anything
// changed.
func consolidateSets(sets map[string]av.ScrapedRouteSet) bool {
	changed := false
	for key, set := range sets {
		from, to, ok := strings.Cut(key, "-")
		if !ok {
			continue
		}

		var order []string
		merged := make(map[string]*av.ScrapedRoute)
		for _, r := range set.Routes {
			route := consolidateRoute(cleanRoute(r.Route, from, to), domestic(from), domestic(to))
			if route == "" {
				continue
			}
			m, ok := merged[route]
			if !ok {
				c := r
				c.Route = route
				merged[route] = &c
				order = append(order, route)
				continue
			}
			m.Count += r.Count
			m.Aircraft |= r.Aircraft
			m.Hours |= r.Hours
			if r.MinAltitude > 0 && (m.MinAltitude == 0 || r.MinAltitude < m.MinAltitude) {
				m.MinAltitude = r.MinAltitude
			}
			m.MaxAltitude = max(m.MaxAltitude, r.MaxAltitude)
		}

		routes := util.MapSlice(order, func(route string) av.ScrapedRoute { return *merged[route] })
		slices.SortStableFunc(routes, func(a, b av.ScrapedRoute) int {
			if a.Count != b.Count {
				return cmp.Compare(b.Count, a.Count)
			}
			return strings.Compare(a.Route, b.Route)
		})
		routes = cullRareRoutes(routes)

		if !slices.Equal(routes, set.Routes) {
			set.Routes = routes
			sets[key] = set
			changed = true
		}
	}
	return changed
}

// parseAltitude reads the analyzer's altitude notations--"FL340", "10,000",
// or a bare flight level--returning feet, or 0 if there's nothing there.
func parseAltitude(s string) int {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if after, ok := strings.CutPrefix(s, "FL"); ok {
		s = after
	}
	alt, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	if alt < 1000 {
		alt *= 100
	}
	return alt
}

// cleanRoute strips the endpoint airport tokens some filed routes carry, so
// that "SFO TRUKN2 GRTFL MACHU TMBRS4 PDX" and "TRUKN2 GRTFL MACHU TMBRS4"
// merge, along with FlightAware's own annotations: it marks segments it
// inferred with a leading "+" and fills gaps with "TBD"--the fixes are real,
// the markers aren't. A route left with nothing--a direct filing--comes back
// empty.
func cleanRoute(route, from, to string) string {
	fields := strings.Fields(html.UnescapeString(route))
	fields = util.MapSlice(fields, func(f string) string { return strings.TrimLeft(f, "+") })
	fields = util.FilterSlice(fields, func(f string) bool { return f != "" && f != "TBD" })
	if len(fields) > 0 && av.TokenNamesAirport(fields[0], from) {
		fields = fields[1:]
	}
	if n := len(fields); n > 0 && av.TokenNamesAirport(fields[n-1], to) {
		fields = fields[:n-1]
	}
	return strings.Join(fields, " ")
}

// oceanicTokenRE matches the parts of a route that belong to the organized
// track systems: fixes given as coordinates and the NAT tracks themselves.
var oceanicTokenRE = regexp.MustCompile(`^(\d{2,4}[NS]/?\d{2,5}[EW]|NAT[A-Z])$`)

// consolidateRoute cuts an oceanic route down to the parts vice can use.
// Flights across an ocean file organized-track routings that differ flight to
// flight and day to day, so keeping them splinters a pair into hundreds of
// one-off routes; and everything beyond the ocean lies on the far side of it
// from the scenario. What's kept is the portion on each end that belongs to a
// scenario airport: the head before the first oceanic token for the origin,
// the tail after the last one for the destination.
func consolidateRoute(route string, fromScenario, toScenario bool) string {
	fields := strings.Fields(route)
	first := slices.IndexFunc(fields, oceanicTokenRE.MatchString)
	if first == -1 {
		return route
	}
	last := first
	for i := first + 1; i < len(fields); i++ {
		if oceanicTokenRE.MatchString(fields[i]) {
			last = i
		}
	}

	var kept []string
	if fromScenario {
		kept = append(kept, fields[:first]...)
	}
	if toScenario {
		kept = append(kept, fields[last+1:]...)
	}
	return strings.Join(kept, " ")
}

///////////////////////////////////////////////////////////////////////////
// The database file

func readRouteSets(path string) map[string]av.ScrapedRouteSet {
	sets := make(map[string]av.ScrapedRouteSet)
	b, err := os.ReadFile(path)
	if err != nil {
		return sets // not written yet
	}
	if err := json.Unmarshal(b, &sets); err != nil {
		fmt.Printf("%s: %v\n", path, err)
		os.Exit(1)
	}
	return sets
}

// writeRouteSets rewrites the database after every fetch, so that an
// interrupted run keeps what it paid for. Map keys marshal in sorted order,
// which keeps the diffs reviewable.
func writeRouteSets(path string, sets map[string]av.ScrapedRouteSet) {
	b, err := json.MarshalIndent(sets, "", "  ")
	if err == nil {
		err = os.WriteFile(path, b, 0o644)
	}
	if err != nil {
		fmt.Printf("%s: %v\n", path, err)
		os.Exit(1)
	}
}

// cmd/scraperoutes/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// scraperoutes maintains resources/scraped-routes.json, the database of
// recently filed routes that route selection consults ahead of the FAA
// databases. It counts the traffic in the historical flight data that would
// file a route, looks each city pair it flies up on FlightAware's IFR route
// analyzer, and records the routes actually filed along with how often, at
// what altitudes, by what kinds of aircraft, and at what hours of the
// day--noise abatement runs some routes only at night. Routes that leave and
// arrive the same way are counted as one, and rarely filed ones are culled.
// Pairs are fetched busiest first, whatever routes the FAA databases hold for
// them, and refetched after they go stale (60 days, by default).
//
// A big queue can be spread across Cloud Run tasks, each fetching its share
// from its own instance; see cloudrun/run.sh, which drives the -plan, -worker
// and -merge modes.
//
//	go run ./cmd/scraperoutes [-cell N40W074] [-limit 25] [-dryrun]
//	go run ./cmd/scraperoutes -lookup KCPS/KORD
//	cmd/scraperoutes/cloudrun/run.sh [-limit 2000]
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
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
)

func main() {
	cell := flag.String("cell", "", "only process pairs from this flight data `cell`, e.g. N40W074")
	limit := flag.Int("limit", 25, "maximum `number` of city pairs to fetch routes for in this run")
	minCount := flag.Int("mincount", 100,
		"skip pairs the flight data records fewer than `count` flights that would file a route for")
	delay := flag.Duration("delay", 10*time.Second, "`wait` between web requests")
	recheck := flag.Int("recheck", 60, "refetch pairs last fetched more than `days` ago")
	dryRun := flag.Bool("dryrun", false, "report what would be recorded without updating the database")
	lookup := flag.String("lookup", "", "look up a single airport `pair`, e.g. KCPS/KORD, and print its routes")
	dbPath := flag.String("db", "resources/"+av.ScrapedRoutesPath, "scraped route database `file` to update")
	planDir := flag.String("plan", "", "write the pairs to fetch to `dir`/"+planFile+
		" for Cloud Run tasks, rather than fetching them")
	worker := flag.String("worker", "", "fetch this Cloud Run task's share of the pairs in `location`/"+
		planFile+", a directory or gs://bucket/prefix, into location/"+resultsDir)
	mergeDir := flag.String("merge", "", "record the routes Cloud Run tasks fetched into `dir`/"+resultsDir)
	flag.Parse()

	db.InitDB()

	if *lookup != "" {
		lookupPair(*lookup)
		return
	}
	if *worker != "" {
		runWorker(*worker)
		return
	}
	if *mergeDir != "" {
		mergeResults(*mergeDir, *dbPath, *dryRun)
		return
	}

	flown := gatherFilings(*cell)

	sets := readRouteSets(*dbPath)
	changed := consolidateSets(sets)
	if changed {
		fmt.Printf("Consolidated previously recorded routes\n")
	}
	// Only a full walk knows what is still flown; with -cell every pair outside
	// the cell would look unflown.
	if *cell == "" {
		if pruned := prunePairs(sets, flown); pruned > 0 {
			fmt.Printf("Pruned %d recorded city pairs\n", pruned)
			changed = true
		}
	}
	if changed && !*dryRun {
		writeRouteSets(*dbPath, sets)
	}
	stale := time.Now().AddDate(0, 0, -*recheck).Format("2006-01-02")

	pairs := gatherPairs(flown)
	pairs = util.FilterSlice(pairs, func(p pair) bool {
		if p.flights < *minCount {
			return false
		}
		set, ok := sets[p.key()]
		return !ok || set.Updated <= stale
	})
	fmt.Printf("%d city pairs to fetch: %d or more flights that would file, not fetched since %s\n",
		len(pairs), *minCount, stale)

	// A dry run is for reading the queue rather than filling it, so print what
	// it would work through. With -limit 0 that is the whole of what the run
	// would do, and it never reaches the network.
	if *dryRun {
		for _, p := range pairs {
			fmt.Printf("  %s->%s: %d flights\n", p.from, p.to, p.flights)
		}
	}

	if *planDir != "" {
		writePlan(*planDir, pairs[:min(len(pairs), *limit)], *delay)
		if remaining := len(pairs) - *limit; remaining > 0 {
			fmt.Printf("%d pairs remain past -limit\n", remaining)
		}
		return
	}

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
			fmt.Printf("%s->%s (%d flights): no routes found\n", pr.from, pr.to, pr.flights)
		}
		for _, r := range routes {
			fmt.Printf("%s->%s (%d flights): %q %s\n", pr.from, pr.to, pr.flights,
				r.Route, describe(r))
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

// lookupPair fetches the routes for the airport pair given as "KCPS/KORD"
// and prints what would be recorded for it.
func lookupPair(spec string) {
	fromStr, toStr, ok := strings.Cut(strings.ToUpper(spec), "/")
	if !ok {
		fmt.Printf("%q: expected an airport pair like KCPS/KORD\n", spec)
		os.Exit(1)
	}
	from, to := av.ICAOAirportCode(fromStr), av.ICAOAirportCode(toStr)
	for _, icao := range []av.ICAOAirportCode{from, to} {
		if _, ok := db.DB.Airports[icao]; !ok {
			fmt.Printf("%s: airport not in the FAA database\n", icao)
			os.Exit(1)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	routes, err := fetchRoutes(client, from, to, domestic(from), domestic(to))
	if err != nil {
		fmt.Printf("%s->%s: %v\n", from, to, err)
		os.Exit(1)
	}
	routes = cullRareRoutes(routes)

	if len(routes) == 0 {
		fmt.Printf("%s->%s: no routes found\n", from, to)
	}
	for _, r := range routes {
		fmt.Printf("%s->%s: %q %s\n", from, to, r.Route, describe(r))
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

// pair is a directed city pair from the flight data and how many of the
// flights recorded for it would file a route.
type pair struct {
	from, to av.ICAOAirportCode
	flights  int
}

func (p pair) key() string { return string(p.from) + "-" + string(p.to) }

// gatherFilings walks the flight data and counts, for every directed city pair
// it holds, the flights that would file a route. A pair is recorded in the
// cell of the airport it departs and again in the cell of the one it lands
// at, so its count is the larger of what the two give.
func gatherFilings(onlyCell string) map[db.AirportPair]int {
	resources := util.GetResourcesFS()
	files, err := fs.Glob(resources, traffic.FlightDataDirectory+"/*"+traffic.FlightDataExtension)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	counts := make(map[db.AirportPair]int)
	for _, file := range files {
		cell := strings.TrimSuffix(path.Base(file), traffic.FlightDataExtension)
		if onlyCell != "" && !strings.EqualFold(cell, onlyCell) {
			continue
		}

		data, err := traffic.ReadFlightData(resources, cell)
		if err != nil || data == nil {
			continue
		}
		flights, err := traffic.DecodeFlights(data)
		if err != nil {
			fmt.Printf("%s: %v\n", file, err)
			continue
		}

		local := make(map[db.AirportPair]int)
		for _, f := range flights {
			if !filesIFR(f.Callsign, f.AircraftType) {
				continue
			}
			from, to := f.Other, f.Airport
			if f.Departure {
				from, to = f.Airport, f.Other
			}
			local[db.AirportPair{From: from, To: to}]++
		}
		for key, n := range local {
			counts[key] = max(counts[key], n)
		}
	}

	return counts
}

// gatherPairs returns the directed city pairs with traffic that would file a
// route, busiest first.
func gatherPairs(flown map[db.AirportPair]int) []pair {
	var pairs []pair
	for key, n := range flown {
		from, to := key.From, key.To
		if madeUpAirport(from) || madeUpAirport(to) {
			continue
		}
		if _, ok := db.DB.Airports[from]; !ok {
			continue
		}
		if _, ok := db.DB.Airports[to]; !ok {
			continue
		}
		pairs = append(pairs, pair{from: from, to: to, flights: n})
	}

	slices.SortFunc(pairs, func(a, b pair) int {
		if a.flights != b.flights {
			return cmp.Compare(b.flights, a.flights)
		}
		return strings.Compare(a.key(), b.key())
	})
	return pairs
}

// classAFloor is where the flight levels begin. An aircraft that cannot get
// near it is a light one flying locally, whatever its operator: the trainers,
// the tour flights, the club aircraft and the helicopters.
const classAFloor = 18000

// filesIFR reports whether a flight is one whose route the analyzer could know.
// Two things have to hold, and each is there for traffic the other lets
// through. The callsign has to be an operator's or a registration--three
// letters and a number, or one and a number--which leaves out the two-letter
// squadron callsigns that fly Navy primary training all day. And the aircraft
// has to be one that goes places rather than back where it started, which is
// what tells Cape Air's piston 402s from a flight school's 172s: both fly
// under an operator's callsign.
func filesIFR(callsign, aircraftType string) bool {
	prefix, number := av.SplitCallsign(callsign)
	if number == "" || (len(prefix) != 1 && len(prefix) != 3) {
		return false
	}
	perf, ok := db.DB.AircraftPerformance[aircraftType]
	if !ok || perf.Engine.AircraftType == "H" {
		return false // a helicopter goes where the route structure doesn't
	}
	return perf.Ceiling > classAFloor
}

// madeUpAirport reports whether an airport is one of the fictional ones the FAA
// Academy scenarios fly. They stand on a real airport's traffic, which is why
// they turn up in the flight data at all, but no real route was ever filed to
// one and the Academy is not to be flown on real-world routes regardless.
func madeUpAirport(icao av.ICAOAirportCode) bool {
	_, ok := traffic.FlightDataSubstitutes[icao]
	return ok
}

// domestic reports whether an airport is one the FAA controls, which is what
// decides the end of an oceanic route worth keeping.
func domestic(icao av.ICAOAirportCode) bool { return db.DB.Airports[icao].FAAControlled() }

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
func fetchRoutes(client *http.Client, from, to av.ICAOAirportCode,
	fromScenario, toScenario bool) ([]av.ScrapedRoute, error) {
	u := "https://www.flightaware.com/analysis/route.rvt?origin=" + url.QueryEscape(string(from)) +
		"&destination=" + url.QueryEscape(string(to))
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
	// The analyzer's title is on its page whether or not it found any routes.
	// Without it, this is something else--a block page served to a client it
	// has decided is a bot--and reading it as no routes would record the pair
	// as having none until it went stale.
	if !strings.Contains(string(body), "IFR Route Analyzer") {
		return nil, fmt.Errorf("fetching routes: not the route analyzer's page")
	}
	return parseAnalyzerRoutes(string(body), from, to, fromScenario, toScenario), nil
}

// parseAnalyzerRoutes reads the route analyzer's two tables. The summary rows
// lead with the number of times each route was filed and give its altitude
// range; the itemized rows below them lead with a local time of day and give
// each flight's aircraft type. Both link the route to SkyVector.
func parseAnalyzerRoutes(body string, from, to av.ICAOAirportCode, fromScenario, toScenario bool) []av.ScrapedRoute {
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
				r.Aircraft |= av.AircraftClassOf(db.Lookups{}, acType[1])
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
	sortRoutes(routes)
	return routes
}

// maxRoutesPerPair bounds how many routes a pair keeps. Route selection only
// ever wants a few, and anything past the first several is one-off noise.
const maxRoutesPerPair = 8

// minRouteFilings is how often the analyzer has to have seen a route, or one
// leaving and arriving the same way, to call it how the pair is flown rather
// than one flight's paperwork. Its window is about
// a week, going by what the busiest pairs come back with against how often they
// are really flown, so this asks a route to turn up most days. Without it a
// pair whose whole sample is a filing or two keeps all of it: the relative rule
// below has nothing to measure one lone route against another.
const minRouteFilings = 5

// cullRareRoutes groups the routes that share their ends, then drops the
// groups filed a tenth or less as often as the pair's most common, which are
// one-off reroutes rather than how the pair is flown, and the ones barely
// filed at all, and keeps at most maxRoutesPerPair of the rest, most-filed
// first.
func cullRareRoutes(routes []av.ScrapedRoute) []av.ScrapedRoute {
	routes = groupByEnds(routes)
	if len(routes) == 0 {
		return routes
	}
	most := routes[0].Count
	routes = util.FilterSlice(routes, func(r av.ScrapedRoute) bool {
		return r.Count >= minRouteFilings && r.Count*10 > most
	})
	return routes[:min(len(routes), maxRoutesPerPair)]
}

// groupByEnds merges the routes that leave and arrive the same way into the
// most-filed of them, which then carries what was seen of all of them.
// Long-haul jets file wind-optimal routes whose middles are rarely the same
// twice--Atlanta to Los Angeles comes back as 87 routes in 102 filings, none
// filed more than four times--but they leave by a handful of SIDs and gates
// and arrive by a handful of STARs, and the ends are what route selection
// fits to a scenario. Taken whole, nothing such a pair files recurs enough to
// keep; taken by their ends, five routes account for three-quarters of its
// filings. A pair whose routes repeat whole comes out as it went in.
func groupByEnds(routes []av.ScrapedRoute) []av.ScrapedRoute {
	// The same route can come more than once--consolidateSets reworks
	// recorded routes into each other--and it is the most-filed route that
	// stands for its group, so those merge first.
	routes = mergeRoutes(routes, func(r av.ScrapedRoute) string { return r.Route })
	return mergeRoutes(routes, func(r av.ScrapedRoute) string { return routeEnds(r.Route) })
}

// routeEnds is a route's first two tokens and its last two--typically the
// SID and the gate it leaves by, and the fix it joins the STAR at and the
// STAR--or the whole of a route too short to have a middle.
func routeEnds(route string) string {
	fields := strings.Fields(route)
	if len(fields) <= 4 {
		return route
	}
	return strings.Join(slices.Concat(fields[:2], fields[len(fields)-2:]), " ")
}

// mergeRoutes merges the routes that share a key into the first of them,
// most-filed first, and returns them most-filed first.
func mergeRoutes(routes []av.ScrapedRoute, key func(av.ScrapedRoute) string) []av.ScrapedRoute {
	sorted := slices.Clone(routes)
	sortRoutes(sorted)

	var merged []av.ScrapedRoute
	index := make(map[string]int)
	for _, r := range sorted {
		k := key(r)
		i, ok := index[k]
		if !ok {
			index[k] = len(merged)
			merged = append(merged, r)
			continue
		}
		m := &merged[i]
		m.Count += r.Count
		m.Aircraft |= r.Aircraft
		m.Hours |= r.Hours
		if r.MinAltitude > 0 && (m.MinAltitude == 0 || r.MinAltitude < m.MinAltitude) {
			m.MinAltitude = r.MinAltitude
		}
		m.MaxAltitude = max(m.MaxAltitude, r.MaxAltitude)
	}
	sortRoutes(merged)
	return merged
}

// sortRoutes orders routes most-filed first.
func sortRoutes(routes []av.ScrapedRoute) {
	slices.SortStableFunc(routes, func(a, b av.ScrapedRoute) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return strings.Compare(a.Route, b.Route)
	})
}

// prunePairs drops the entries for pairs nothing that would file a route flies
// any more. An import that stops recording a flight as having landed somewhere
// it was only crossing takes the pair it invented with it, and nothing else
// would ever clear the entry: consolidateSets reworks a pair's routes but never
// decides the pair itself is gone. It reports how many it removed.
//
// Nothing flying a pair that would file is a statement about the pair, not
// about this run, which is why the -mincount bar has no say here: keying
// deletion to a flag would let one run with a high one empty the database.
func prunePairs(sets map[string]av.ScrapedRouteSet, flown map[db.AirportPair]int) int {
	pruned := 0
	for key := range sets {
		fromStr, toStr, ok := strings.Cut(key, "-")
		if !ok {
			continue
		}
		if flown[db.AirportPair{From: av.ICAOAirportCode(fromStr), To: av.ICAOAirportCode(toStr)}] > 0 {
			continue
		}
		fmt.Printf("  %s: nothing that files flies it any more\n", key)
		delete(sets, key)
		pruned++
	}
	return pruned
}

// consolidateSets reapplies the route consolidation and culling to what is
// already in the database, so that entries recorded under older rules are
// cleaned up in place rather than refetched. It reports whether anything
// changed.
func consolidateSets(sets map[string]av.ScrapedRouteSet) bool {
	changed := false
	for key, set := range sets {
		from, to, ok := parsePairKey(key)
		if !ok {
			continue
		}

		var routes []av.ScrapedRoute
		for _, r := range set.Routes {
			r.Route = consolidateRoute(cleanRoute(r.Route, from, to), domestic(from), domestic(to))
			if r.Route != "" {
				routes = append(routes, r)
			}
		}
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
// merge--the origin's id may also sit behind the SID token, as in
// "MAUI5 OGG LNY ..."--along with FlightAware's own annotations: it marks
// segments it inferred with a leading "+" and fills gaps with "TBD"--the
// fixes are real, the markers aren't. A route left with nothing--a direct
// filing--comes back empty.
func cleanRoute(route string, from, to av.ICAOAirportCode) string {
	fields := strings.Fields(html.UnescapeString(route))
	fields = util.MapSlice(fields, func(f string) string { return strings.TrimLeft(f, "+") })
	fields = util.FilterSlice(fields, func(f string) bool { return f != "" && f != "TBD" })
	fields = av.TrimDepartureAirportTokens(db.Lookups{}, fields, from)
	fields = av.TrimDestinationAirportTokens(db.Lookups{}, fields, to)
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

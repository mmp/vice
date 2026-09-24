// aviation/db/db.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"fmt"
	av "github.com/mmp/vice/aviation"
	"os"
	"slices"
	"strings"
	"sync"

	// Embed the time zone database: Windows has no system copy, and the zone
	// names in TFR NOTAMs have to resolve everywhere Vice runs.
	_ "time/tzdata"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

var DB *StaticDatabase

///////////////////////////////////////////////////////////////////////////
// StaticDatabase

type StaticDatabase struct {
	Navaids             map[string]Navaid
	Airports            map[av.ICAOAirportCode]Airport
	faaToICAO           map[av.FAAAirportCode]av.ICAOAirportCode
	Fixes               map[string]Fix
	Airways             map[string][]av.Airway
	EnrouteHolds        map[string][]av.Hold                        // Fix -> Holds
	TerminalHolds       map[av.ICAOAirportCode]map[string][]av.Hold // Airport -> Fix -> Holds
	Callsigns           map[string]string                           // 3 letter -> callsign
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]av.AircraftPerformance
	Airlines            map[string]av.Airline
	MagneticGrid        MagneticGrid
	ARTCCs              map[string]ARTCC
	TRACONs             map[string]TRACON
	ATCTs               map[string]ATCT
	MVAs                map[string][]MVA // TRACON -> MVAs
	AirportPairRoutes   map[AirportPair][]AirportPairRoute
	ScrapedRoutes       map[AirportPair][]av.ScrapedRoute
	BravoAirspace       map[string][]av.AirspaceVolume
	CharlieAirspace     map[string][]av.AirspaceVolume
	DeltaAirspace       map[string][]av.AirspaceVolume
	Say                 Pronunciations
}

type Airport struct {
	Id         av.ICAOAirportCode
	Name       string
	Country    string
	LocalCode  av.FAAAirportCode `json:"local_code"`
	Elevation  int
	Location   math.Point2LL
	Runways    []av.Runway
	Approaches map[string]av.Approach
	SIDs       map[string]av.SID
	STARs      map[string]av.STAR
	ARTCC      string
}

// faaCountries are the ISO country codes of the places the FAA runs air traffic
// control: the fifty states and the territories.
var faaCountries = map[string]bool{
	"US": true, // the fifty states, including Alaska and Hawaii
	"PR": true, // Puerto Rico
	"VI": true, // US Virgin Islands
	"GU": true, // Guam
	"MP": true, // Northern Mariana Islands
	"AS": true, // American Samoa
	"UM": true, // Midway, Wake, and the other minor outlying islands
}

// FAAControlled reports whether the FAA runs air traffic control at the
// airport. The airport database is worldwide, so this is what tells the
// airports Vice might one day simulate from the ones that are only ever the far
// end of somebody else's flight.
func (ap Airport) FAAControlled() bool { return faaCountries[ap.Country] }

// RenamedAirports maps airport identifiers the FAA has retired to the ones that
// replaced them. The historical data vice imports goes on using the old
// identifier long after the change, so the import tools canonicalize through
// this; scenarios and timetables must use the current one.
var RenamedAirports = map[av.ICAOAirportCode]av.ICAOAirportCode{
	"KPBI": "KDJT", // renamed 2026-08-18
}

// CurrentAirportId returns the identifier now in use for an airport that has
// been re-identified; any other id is returned unchanged.
func CurrentAirportId(id av.ICAOAirportCode) av.ICAOAirportCode {
	if current, ok := RenamedAirports[id]; ok {
		return current
	}
	return id
}

// CheckAirport returns an error unless id names an airport in the database.
// role describes the airport's part in whatever is being validated, as in
// "destination" or "arrival". A retired identifier names its replacement:
// historical data keeps using the old one, so it turns up in scenario edits
// made from a stale copy.
func CheckAirport(role string, id av.ICAOAirportCode) error {
	if _, ok := DB.Airports[id]; ok {
		return nil
	}
	if current, ok := RenamedAirports[id]; ok {
		return fmt.Errorf("%s airport %q was re-identified as %q; use the current identifier", role, id, current)
	}
	return fmt.Errorf("%s airport %q unknown", role, id)
}

// Facility represents a geographic facility with a center point and radius.
// Both TRACONs and ARTCCs use this structure for weather data handling.
type Facility struct {
	Name      string
	Latitude  float32
	Longitude float32
	Radius    float32
}

func (f Facility) Center() math.Point2LL {
	return math.Point2LL{f.Longitude, f.Latitude}
}

// ARTCC is a type alias for Facility representing an Air Route Traffic Control Center.
type ARTCC = Facility

// TRACON represents a Terminal Radar Approach Control facility.
type TRACON struct {
	Facility
	ARTCC string
}

type ATCT TRACON

type Navaid struct {
	Id       string
	Type     string
	Name     string
	Location math.Point2LL

	// Declination is a VHF navaid's station declination: the angle its
	// radials are referenced to, fixed when the station was last aligned
	// rather than following the local variation as it has drifted since.
	// It is in the sim's magnetic variation convention (positive west), so
	// math.MagneticToTrue(radial, Declination) is a radial's true bearing.
	Declination    float32
	HasDeclination bool

	HasDME          bool
	DMELocation     math.Point2LL
	DMEElevation    int
	HasDMEElevation bool
}

type Fix struct {
	Id       string
	Location math.Point2LL
}

const (
	RouteBasedFix = "route"
	ZoneBasedFix  = "zone"
)

type AdaptationFix struct {
	Name         string // not in JSON
	Type         string `json:"type"`
	ToFacility   string `json:"to"`   // controller to handoff to
	FromFacility string `json:"from"` // controller to handoff from
	Altitude     [2]int `json:"altitude"`
}

type AdaptationFixes []AdaptationFix

///////////////////////////////////////////////////////////////////////////

func (ap Airport) SelectBestRunway(windDir math.TrueHeading, magneticVariation float32) (*av.Runway, *av.Runway) {
	whdg := math.TrueToMagnetic(windDir, magneticVariation)

	// Find best aligned runway
	minDelta := float32(1000)
	bestRwy := -1
	for i, rwy := range ap.Runways {
		if _, ok := av.LookupOppositeRunway(Lookups{}, ap.Id, rwy.Id); ok {
			d := math.HeadingDifference(whdg, rwy.Heading)
			if d < minDelta {
				minDelta = d
				bestRwy = i
			}
		}
	}
	if bestRwy == -1 {
		return nil, nil
	}

	rwy := ap.Runways[bestRwy]
	opp, _ := av.LookupOppositeRunway(Lookups{}, ap.Id, rwy.Id)

	return &rwy, &opp
}

///////////////////////////////////////////////////////////////////////////

func (d StaticDatabase) LookupWaypoint(f string) (math.Point2LL, bool) {
	if n, ok := d.Navaids[f]; ok {
		return n.Location, true
	} else if f, ok := d.Fixes[f]; ok {
		return f.Location, true
	} else {
		return math.Point2LL{}, false
	}
}

func (d StaticDatabase) LookupDME(f string) (math.Point2LL, int, bool) {
	if n, ok := d.Navaids[strings.ToUpper(f)]; ok && n.HasDME && n.HasDMEElevation {
		return n.DMELocation, n.DMEElevation, true
	}
	return math.Point2LL{}, 0, false
}

// Declination returns the station declination of the named VHF navaid, if
// it has one.
func (d StaticDatabase) Declination(id string) (float32, bool) {
	n, ok := d.Navaids[strings.ToUpper(id)]
	return n.Declination, ok && n.HasDeclination
}

// LookupICAOAirport returns the airport the aviation database keys by the
// given id.
func (d StaticDatabase) LookupICAOAirport(icao av.ICAOAirportCode) (Airport, bool) {
	ap, ok := d.Airports[icao]
	return ap, ok
}

// LookupFAAAirport returns the airport with the given FAA local identifier.
func (d StaticDatabase) LookupFAAAirport(faa av.FAAAirportCode) (Airport, bool) {
	if icao, ok := d.faaToICAO[faa]; ok {
		return d.Airports[icao], true
	}
	return Airport{}, false
}

// initLocalCodes fills in the FAA local identifiers that the airports
// database and the CIFP don't carry directly and builds the reverse FAA ->
// database id map. It exits fatally if an FAA airport ends up without a
// local code or two airports claim the same one: both must be resolved
// before a release.
func (d *StaticDatabase) initLocalCodes() {
	fatal := d.fillLocalCodes()
	fatal = append(fatal, d.buildFAAIndex()...)

	if len(fatal) > 0 {
		slices.Sort(fatal)
		for _, f := range fatal {
			fmt.Fprintln(os.Stderr, f)
		}
		os.Exit(1)
	}
}

// fillLocalCodes applies the local code overrides and gives the airports
// already keyed by their FAA identifier that identifier as their local code.
// It returns the FAA airports left without one.
func (d *StaticDatabase) fillLocalCodes() []string {
	var fatal []string

	for icao, lc := range airportLocalCodeOverrides {
		if ap, ok := d.Airports[icao]; ok {
			ap.LocalCode = lc
			d.Airports[icao] = ap
		}
	}

	for icao, ap := range d.Airports {
		if ap.LocalCode != "" {
			continue
		}
		if _, ok := airportLocalCodeOverrides[icao]; ok {
			continue // its claim was deliberately taken away
		}
		// An airport in the FAA's regions whose id isn't a four-letter ICAO
		// id is keyed by its FAA location identifier already. Country is
		// empty for airports that come only from the CIFP or from
		// custom_airports.json; the CIFP is FAA data, so those qualify too.
		inFAARegion := ap.Country == "" || faaCountries[ap.Country]
		fourLetters := len(icao) == 4 && !strings.ContainsFunc(string(icao),
			func(r rune) bool { return r < 'A' || r > 'Z' })
		if inFAARegion && !fourLetters {
			ap.LocalCode = av.FAAAirportCode(icao)
			d.Airports[icao] = ap
		} else if faaCountries[ap.Country] && !airportsWithoutLocalCode[icao] {
			fatal = append(fatal, fmt.Sprintf("%s: FAA airport has no local code in airports.csv.zst", icao))
		}
	}
	return fatal
}

// buildFAAIndex builds the reverse FAA local identifier -> database id map
// from the airports' local codes, returning the codes that two separate
// airports claim.
func (d *StaticDatabase) buildFAAIndex() []string {
	var fatal []string
	d.faaToICAO = make(map[av.FAAAirportCode]av.ICAOAirportCode)
	for icao, ap := range d.Airports {
		lc := ap.LocalCode
		if lc == "" {
			continue
		}
		prev, ok := d.faaToICAO[lc]
		if !ok {
			d.faaToICAO[lc] = icao
			continue
		}
		// The same airport can be in the database twice: under the gps code
		// ourairports invents for airports with no ICAO id (K00N) and under
		// its FAA identifier from the CIFP (00N). The FAA identifier is the
		// key to prefer; two separate airports claiming one code is fatal.
		self, other := icao, prev
		if string(prev) == string(lc) {
			self, other = prev, icao
		} else if string(icao) != string(lc) {
			fatal = append(fatal, fmt.Sprintf("%s: airports %s and %s both claim the local code", lc, prev, icao))
			continue
		}
		if math.NMDistance2LL(d.Airports[self].Location, d.Airports[other].Location) > 5 {
			fatal = append(fatal, fmt.Sprintf("%s: airports %s and %s both claim the local code but aren't co-located",
				lc, prev, icao))
			continue
		}
		d.faaToICAO[lc] = self
	}
	return fatal
}

// LookupFacility returns a Facility for the given id, checking
// TRACONs, ATCTs, and ARTCCs.
func (d StaticDatabase) LookupFacility(id string) (Facility, bool) {
	if tracon, ok := d.TRACONs[id]; ok {
		return tracon.Facility, true
	}
	if atct, ok := d.ATCTs[id]; ok {
		return atct.Facility, true
	}
	if artcc, ok := d.ARTCCs[id]; ok {
		return artcc, true
	}
	return Facility{}, false
}

// IsFacility returns true if id is a known ARTCC, TRACON, or ATCT.
func (d StaticDatabase) IsFacility(id string) bool {
	return d.IsARTCC(id) || d.IsTRACON(id) || d.IsATCT(id)
}

// ARTCCForFacility returns the ARTCC identifier for the given facility:
// the id itself if it is an ARTCC, the parent ARTCC for a TRACON or
// ATCT, or "" if the id is unknown.
func (d StaticDatabase) ARTCCForFacility(fac string) string {
	if _, ok := d.ARTCCs[fac]; ok {
		return fac
	}
	if tracon, ok := d.TRACONs[fac]; ok {
		return tracon.ARTCC
	}
	if atct, ok := d.ATCTs[fac]; ok {
		return atct.ARTCC
	}
	return ""
}

func (d StaticDatabase) IsARTCC(id string) bool {
	_, ok := d.ARTCCs[id]
	return ok
}

func (d StaticDatabase) IsTRACON(id string) bool {
	_, ok := d.TRACONs[id]
	return ok
}

func (d StaticDatabase) IsATCT(id string) bool {
	_, ok := d.ATCTs[id]
	return ok
}

var (
	initDBOnce   sync.Once
	initDBDoneCh = make(chan struct{})
)

// InitDB ensures the aviation database is initialized and blocks until
// it is ready. Safe to call multiple times; only the first call does
// work. Prefer StartInitDB if you want to kick off loading without
// waiting.
func InitDB() {
	StartInitDB()
	<-initDBDoneCh
}

// StartInitDB kicks off aviation database loading on a background
// goroutine without blocking. Subsequent callers of InitDB() will wait
// until that load completes.
func StartInitDB() {
	initDBOnce.Do(func() {
		go func() {
			doInitDB()
			close(initDBDoneCh)
		}()
	})
}

func ReloadDB() {
	// An initial load that is still running would otherwise finish after
	// this one and put the old contents back.
	InitDB()
	doInitDB()
}

func doInitDB() {
	db := &StaticDatabase{}

	var wg sync.WaitGroup
	var customAirports map[av.ICAOAirportCode]Airport
	wg.Go(func() { db.Airports, customAirports = parseAirports() })
	wg.Go(func() { db.AircraftTypeAliases, db.AircraftPerformance = parseAircraft() })
	wg.Go(func() { db.Airlines, db.Callsigns = parseAirlines() })
	var airports map[av.ICAOAirportCode]Airport
	wg.Go(func() {
		r := parseCIFP()
		airports = r.Airports
		db.Navaids = r.Navaids
		db.Fixes = r.Fixes
		db.Airways = r.Airways
		db.EnrouteHolds = r.EnrouteHolds
		db.TerminalHolds = r.TerminalHolds
	})
	var hpfEnroute map[string][]av.Hold
	wg.Go(func() { hpfEnroute = parseHPF() })
	wg.Go(func() { db.MagneticGrid = parseMagneticGrid() })
	wg.Go(func() { db.ARTCCs, db.TRACONs, db.ATCTs = parseFacilities() })
	wg.Go(func() { db.MVAs = parseMVAs() })
	wg.Go(func() { db.Say = parsePronunciations() })
	wg.Go(func() { db.AirportPairRoutes = parseAirportPairRoutes() })
	wg.Go(func() { db.ScrapedRoutes = parseScrapedRoutes() })
	wg.Go(func() {
		db.BravoAirspace = parseAirspace("bravo-airspace.json.zst")
		db.CharlieAirspace = parseAirspace("charlie-airspace.json.zst")
		db.DeltaAirspace = parseAirspace("delta-airspace.json.zst")
	})
	wg.Wait()

	// Merge HPF holds with CIFP holds
	for fix, holds := range hpfEnroute {
		db.EnrouteHolds[fix] = append(db.EnrouteHolds[fix], holds...)
	}

	for icao, ap := range airports {
		if _, ok := customAirports[icao]; !ok { // ignore ones defined in custom_airports.json
			// We don't get these from the CIFP but have them from the other airports
			// database, so port them over. The CIFP's ATA/IATA designator only
			// fills in when the airports database has no local code.
			ap.Name = db.Airports[icao].Name
			ap.Country = db.Airports[icao].Country
			ap.ARTCC = db.Airports[icao].ARTCC
			if lc := db.Airports[icao].LocalCode; lc != "" {
				ap.LocalCode = lc
			}
			db.Airports[icao] = ap
		}
	}

	db.initLocalCodes()

	DB = db

}

///////////////////////////////////////////////////////////////////////////
// Airport-pair Routes

// AirportPair keys the city-pair route database by ICAO airport codes.
type AirportPair struct {
	From, To av.ICAOAirportCode
}

// AirportPairRoute is one real-world route between two airports, taken from the
// FAA preferred-route and coded-departure-route databases by cmd/importroutes.
type AirportPairRoute struct {
	Route        string // e.g. "NEION J223 CORDS J132 ULW BENEE"
	DepartureFix string // explicit for coded departure routes, empty otherwise
	Type         string // TEC, H, L, NAR, SHD, HSD, SLD, or CDR
	Aircraft     string // "jet", "prop", or empty for no restriction
	RNAVRequired bool
}

// LowAltitude reports whether the route is a low-altitude one, flown by
// aircraft that stay out of the flight levels.
func (r AirportPairRoute) LowAltitude() bool {
	return r.Type == "TEC" || r.Type == "L" || r.Type == "SLD"
}

// RoutesBetween returns the real-world routes from one airport to another,
// ordered preferred-routes first, or nil if the pair isn't in the database.
func (d StaticDatabase) RoutesBetween(from, to av.ICAOAirportCode) []AirportPairRoute {
	return d.AirportPairRoutes[AirportPair{From: from, To: to}]
}

// ScrapedRoutesBetween returns the recently filed routes from one airport to
// another, or nil if the pair hasn't been scraped.
func (d StaticDatabase) ScrapedRoutesBetween(from, to av.ICAOAirportCode) []av.ScrapedRoute {
	return d.ScrapedRoutes[AirportPair{From: from, To: to}]
}

///////////////////////////////////////////////////////////////////////////

func (ap Airport) ValidRunways() string {
	return strings.Join(util.MapSlice(ap.Runways, func(r av.Runway) string { return r.Id }), ", ")
}

func PrintCIFPRoutes(airport av.ICAOAirportCode) error {
	ap, ok := DB.Airports[airport]
	if !ok {
		return fmt.Errorf("%s: airport not present in database\n", airport)
	}

	fmt.Printf("SIDs:\n")
	for name, sid := range util.SortedMap(ap.SIDs) {
		sid.Print(name)
	}
	fmt.Printf("\nSTARs:\n")
	for name, star := range util.SortedMap(ap.STARs) {
		star.Print(name)
	}
	fmt.Printf("\nApproaches:\n")
	for name, appr := range util.SortedMap(ap.Approaches) {
		fmt.Printf("%-5s: ", name)
		for i, wp := range appr.Waypoints {
			if i > 0 {
				fmt.Printf("       ")
			}
			fmt.Println(wp.Encode())
		}
	}
	return nil
}

// ICAOAirportToFAA returns the FAA local identifier of the given airport, or
// "", false if the airport is unknown or has no FAA local identifier.
func ICAOAirportToFAA(icao av.ICAOAirportCode) (av.FAAAirportCode, bool) {
	if DB == nil { // tests that run without the database
		return "", false
	}
	ap, ok := DB.Airports[icao]
	if !ok || ap.LocalCode == "" {
		return "", false
	}
	return ap.LocalCode, true
}

// FAAAirportToICAO returns the id the aviation database keys the given
// airport by, or "", false if no airport has the given FAA local identifier.
func FAAAirportToICAO(faa av.FAAAirportCode) (av.ICAOAirportCode, bool) {
	if DB == nil { // tests that run without the database
		return "", false
	}
	icao, ok := DB.faaToICAO[faa]
	return icao, ok
}

// AirportDisplayId returns the name the FAA's systems know the airport by:
// its FAA local identifier when it has one and otherwise its id unchanged,
// as for an airport outside the FAA's regions.
func AirportDisplayId(icao av.ICAOAirportCode) string {
	if faa, ok := ICAOAirportToFAA(icao); ok {
		return string(faa)
	}
	return string(icao)
}

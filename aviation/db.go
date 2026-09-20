// aviation/db.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
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
	Airports            map[ICAOAirportCode]FAAAirport
	faaToICAO           map[FAAAirportCode]ICAOAirportCode
	Fixes               map[string]Fix
	Airways             map[string][]Airway
	EnrouteHolds        map[string][]Hold                     // Fix -> Holds
	TerminalHolds       map[ICAOAirportCode]map[string][]Hold // Airport -> Fix -> Holds
	Callsigns           map[string]string                     // 3 letter -> callsign
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline
	MagneticGrid        MagneticGrid
	ARTCCs              map[string]ARTCC
	TRACONs             map[string]TRACON
	ATCTs               map[string]ATCT
	MVAs                map[string][]MVA // TRACON -> MVAs
	AirportPairRoutes   map[AirportPair][]AirportPairRoute
	ScrapedRoutes       map[AirportPair][]ScrapedRoute
	BravoAirspace       map[string][]AirspaceVolume
	CharlieAirspace     map[string][]AirspaceVolume
	DeltaAirspace       map[string][]AirspaceVolume
	Say                 Pronunciations
}

type FAAAirport struct {
	Id         ICAOAirportCode
	Name       string
	Country    string
	LocalCode  FAAAirportCode `json:"local_code"`
	Elevation  int
	Location   math.Point2LL
	Runways    []Runway
	Approaches map[string]Approach
	SIDs       map[string]SID
	STARs      map[string]STAR
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
func (ap FAAAirport) FAAControlled() bool { return faaCountries[ap.Country] }

// RenamedAirports maps airport identifiers the FAA has retired to the ones that
// replaced them. The historical data vice imports goes on using the old
// identifier long after the change, so the import tools canonicalize through
// this; scenarios and timetables must use the current one.
var RenamedAirports = map[ICAOAirportCode]ICAOAirportCode{
	"KPBI": "KDJT", // renamed 2026-08-18
}

// CurrentAirportId returns the identifier now in use for an airport that has
// been re-identified; any other id is returned unchanged.
func CurrentAirportId(id ICAOAirportCode) ICAOAirportCode {
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
func CheckAirport(role string, id ICAOAirportCode) error {
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

func (ap FAAAirport) SelectBestRunway(windDir math.TrueHeading, magneticVariation float32) (*Runway, *Runway) {
	whdg := math.TrueToMagnetic(windDir, magneticVariation)

	// Find best aligned runway
	minDelta := float32(1000)
	bestRwy := -1
	for i, rwy := range ap.Runways {
		if _, ok := LookupOppositeRunway(ap.Id, rwy.Id); ok {
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
	opp, _ := LookupOppositeRunway(ap.Id, rwy.Id)

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
func (d StaticDatabase) LookupICAOAirport(icao ICAOAirportCode) (FAAAirport, bool) {
	ap, ok := d.Airports[icao]
	return ap, ok
}

// LookupFAAAirport returns the airport with the given FAA local identifier.
func (d StaticDatabase) LookupFAAAirport(faa FAAAirportCode) (FAAAirport, bool) {
	if icao, ok := d.faaToICAO[faa]; ok {
		return d.Airports[icao], true
	}
	return FAAAirport{}, false
}

// initLocalCodes fills in the FAA local identifiers that the airports
// database and the CIFP don't carry directly and builds the reverse FAA ->
// database id map. It exits fatally if an FAA airport ends up without a
// local code or two airports claim the same one: both must be resolved
// before a release.
func (d *StaticDatabase) initLocalCodes() {
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
			ap.LocalCode = FAAAirportCode(icao)
			d.Airports[icao] = ap
		} else if faaCountries[ap.Country] && !airportsWithoutLocalCode[icao] {
			fatal = append(fatal, fmt.Sprintf("%s: FAA airport has no local code in airports.csv.zst", icao))
		}
	}

	d.faaToICAO = make(map[FAAAirportCode]ICAOAirportCode)
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

	if len(fatal) > 0 {
		slices.Sort(fatal)
		for _, f := range fatal {
			fmt.Fprintln(os.Stderr, f)
		}
		os.Exit(1)
	}
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

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string  `json:"weightClass"`
	Ceiling     float32 `json:"ceiling"`
	Engine      struct {
		// AircraftType is "P" for piston, "T" for turboprop, "J" for jet, and
		// "H" for rotorcraft.
		AircraftType string `json:"type"`
	} `json:"engines"`
	Rate struct {
		Climb      float32 `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    float32 `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Category struct {
		SRS   int    `json:"srs"`
		LAHSO int    `json:"lahso"`
		CWT   string `json:"cwt"`
	}
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min        float32 `json:"min"`
		V2         float32 `json:"v2"`
		Landing    float32 `json:"landing"`
		CruiseTAS  float32 `json:"cruise"`
		CruiseMach float32 `json:"cruiseM"`
		MaxTAS     float32 `json:"max"`
		MaxMach    float32 `json:"maxM"`
	} `json:"speed"`
	Turn struct {
		MaxBankAngle float32 `json:"maxBankAngle"`
		MaxBankRate  float32 `json:"maxBankRate"`
	}
	Capacity struct {
		Passengers int `json:"passengers"`
		FuelPounds int `json:"fuel_pounds"`
	} `json:"capacity"`
}

type Airline struct {
	ICAO     string `json:"icao"`
	Name     string `json:"name"`
	Callsign struct {
		Name            string   `json:"name"`
		CallsignFormats []string `json:"callsignFormats"`
	} `json:"callsign"`
	JSONFleets map[string][][2]any `json:"fleets"`
	Fleets     map[string][]FleetAircraft
}

type FleetAircraft struct {
	ICAO  string
	Count int
}

// baseApproachSpeed returns a reasonable final approach speed for this
// aircraft type. If landing speed is available, a small buffer above that
// speed is used. Otherwise V2 or a default is returned.
func (ap AircraftPerformance) baseApproachSpeed() float32 {
	if ap.Speed.Landing > 0 {
		return ap.Speed.Landing + 5
	} else if ap.Speed.V2 > 0 {
		return 1.25 * ap.Speed.V2
	} else {
		return 120
	}
}

// ApproachSpeed returns the final approach speed including wind
// additives. The runway heading is used to compute the headwind component
// of the provided wind. Jets and turboprops add half the headwind plus the
// full gust factor (not to exceed 20 knots). Pistons add half the gust
// factor... I suppose we should also add a max additive but most pistons
// won't be landing in very windy conditions
func (ap AircraftPerformance) ApproachSpeed(windDirection, windSpeed, windGust float32, runwayHeading float32) float32 {
	gustFactor := max(0, windGust-windSpeed)

	additive := float32(0)
	switch ap.Engine.AircraftType {
	case "J", "T":
		diff := math.HeadingDifference(windDirection, runwayHeading)
		headwind := max(0, float32(windSpeed)*math.Cos(math.Radians(diff)))
		additive = min(headwind/2+gustFactor, 20)
	case "P":
		additive = gustFactor / 2
	}

	return ap.baseApproachSpeed() + additive
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
	var customAirports map[ICAOAirportCode]FAAAirport
	wg.Go(func() { db.Airports, customAirports = parseAirports() })
	wg.Go(func() { db.AircraftTypeAliases, db.AircraftPerformance = parseAircraft() })
	wg.Go(func() { db.Airlines, db.Callsigns = parseAirlines() })
	var airports map[ICAOAirportCode]FAAAirport
	wg.Go(func() {
		r := parseCIFP()
		airports = r.Airports
		db.Navaids = r.Navaids
		db.Fixes = r.Fixes
		db.Airways = r.Airways
		db.EnrouteHolds = r.EnrouteHolds
		db.TerminalHolds = r.TerminalHolds
	})
	var hpfEnroute map[string][]Hold
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
	From, To ICAOAirportCode
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
func (d StaticDatabase) RoutesBetween(from, to ICAOAirportCode) []AirportPairRoute {
	return d.AirportPairRoutes[AirportPair{From: from, To: to}]
}

// RouteWaypoints converts a real-world route from the city-pair database into
// waypoints. An airway name attaches to the fix before it, so
// InitializeLocations fills in the fixes it passes through. The returned
// waypoints have no Location: the caller must run InitializeLocations on them,
// which is also what discards the tokens that aren't fixes at all--SID and STAR
// names, radial/DME fixes like SLI341/019.
//
// This deliberately doesn't go through the scenario route parser, which
// understands vice's "/" waypoint modifiers and so can't read the routes that
// name such fixes.
func RouteWaypoints(route string) WaypointArray {
	var waypoints WaypointArray
	for field := range strings.FieldsSeq(route) {
		if _, ok := DB.Airways[field]; ok && len(waypoints) > 0 {
			waypoints[len(waypoints)-1].InitExtra().Airway = field
		} else {
			waypoints = append(waypoints, Waypoint{Fix: field})
		}
	}
	return waypoints
}

// ScrapedRoutesBetween returns the recently filed routes from one airport to
// another, or nil if the pair hasn't been scraped.
func (d StaticDatabase) ScrapedRoutesBetween(from, to ICAOAirportCode) []ScrapedRoute {
	return d.ScrapedRoutes[AirportPair{From: from, To: to}]
}

///////////////////////////////////////////////////////////////////////////

func (ap FAAAirport) ValidRunways() string {
	return strings.Join(util.MapSlice(ap.Runways, func(r Runway) string { return r.Id }), ", ")
}

func PrintCIFPRoutes(airport ICAOAirportCode) error {
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

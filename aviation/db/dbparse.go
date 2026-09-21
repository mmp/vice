// aviation/db/dbparse.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	av "github.com/mmp/vice/aviation"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// FAA (and other) databases

// Utility function for parsing CSV files as strings; it breaks each line
// of the file into fields and calls the provided callback function for
// each one.
func mungeCSV(filename string, r io.Reader, fields []string, callback func([]string)) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Find the index of each field the caller requested
	var fieldIndices []int
	if header, err := cr.Read(); err != nil {
		panic(fmt.Sprintf("%s: error parsing CSV file: %s", filename, err))
	} else {
		for fi, f := range fields {
			for hi, h := range header {
				if f == strings.TrimSpace(h) {
					fieldIndices = append(fieldIndices, hi)
					break
				}
			}
			if len(fieldIndices) != fi+1 {
				panic(fmt.Sprintf("%s: did not find requested field header", f))
			}
		}
	}

	var strs []string
	for {
		if record, err := cr.Read(); err == io.EOF {
			return
		} else if err != nil {
			panic(fmt.Sprintf("%s: error parsing CSV file: %s", filename, err))
		} else {
			for _, i := range fieldIndices {
				strs = append(strs, record[i])
			}
			callback(strs)
			strs = strs[:0]
		}
	}
}

// airportLocalCodeOverrides patches FAA local identifiers the source data
// has wrong; an empty value takes an airport's claim to a code away.
var airportLocalCodeOverrides = map[av.ICAOAirportCode]av.FAAAirportCode{
	// The upstream row for LS45 (Entergy Waterford 3 Heliport) carries LS82's
	// local code, which belongs to a different Louisiana heliport; FAA NASR
	// lists both as distinct identifiers.
	"LS45": "LS45",
	// Northwood Municipal's identifier belongs to the made-up Academy
	// airport of the same name, defined in custom_airports.json.
	"K4V4": "",
	// Taku Lodge Seaplane Base, which the CIFP has as PFTK with the same
	// TKL designator; the airports database entry keeps the code since it
	// carries the name and country.
	"PFTK": "",
}

// airportsWithoutLocalCode lists the FAA-region airports that have no FAA
// location identifier at all: none of them appear in the FAA NASR APT data.
// The fatal missing-local-code check in initLocalCodes skips them.
var airportsWithoutLocalCode = map[av.ICAOAirportCode]bool{
	"KGWN": true, // Winn Army Community Hospital Helipad
	"KMWN": true, // Mount Washington Observatory
	"KNLW": true, // Naval Station Newport Helipad
	"KNPI": true, // Site 8 NOLF
	"KXTA": true, // Homey (Area 51) Airport
	"KZ26": true, // Camp Roberts Army Heliport
	"PAHE": true, // Healy Airport (not the NASR-listed Healy River/HRR, which is PAHV)
}

func parseAirports() (map[av.ICAOAirportCode]Airport, map[av.ICAOAirportCode]Airport) {
	airports := make(map[av.ICAOAirportCode]Airport)

	// https://ourairports.com/data/
	// Only load airports that have ICAO gps_codes so that we don't
	// pick up minor foreign airports with local_codes that conflict
	// with US fix/VOR names.
	r := util.LoadResource("airports.csv.zst")
	defer r.Close()
	mungeCSV("airports", r,
		[]string{"latitude_deg", "longitude_deg", "elevation_ft", "gps_code", "name", "iso_country", "type", "local_code"},
		func(s []string) {
			id := av.ICAOAirportCode(s[3]) // gps_code
			if id == "" || s[6] == "closed" {
				return
			}

			atof := func(s string) float64 {
				v, err := util.Atof(s)
				if err != nil {
					panic(err)
				}
				return v
			}

			elevation := float64(0)
			if s[2] != "" && s[2] != "NA" {
				elevation = atof(s[2])
			}

			loc := math.Point2LL{float32(atof(s[1])), float32(atof(s[0]))}
			ap := Airport{Id: id, Name: s[4], Country: s[5], Location: loc, Elevation: int(elevation)}
			// Local codes are only unique within the FAA's regions; elsewhere
			// in the world they freely collide with FAA identifiers.
			if ap.FAAControlled() {
				ap.LocalCode = av.FAAAirportCode(s[7])
			}
			airports[id] = ap
		})

	// Custom airports/runways
	custom := util.LoadResource("custom_airports.json")
	defer custom.Close()
	customAirports := make(map[av.ICAOAirportCode]Airport)
	if err := util.UnmarshalJSON(custom, &customAirports); err != nil {
		fmt.Fprintf(os.Stderr, "custom_airports.json: %v\n", err)
		os.Exit(1)
	}
	for icao, ap := range customAirports {
		ap.Id = icao
		airports[icao] = ap
	}

	// ARTCCs
	ar := util.LoadResource("airport_artccs.json")
	defer ar.Close()
	data := make(map[av.ICAOAirportCode]string) // Airport -> ARTCC
	if err := util.UnmarshalJSON(ar, &data); err != nil {
		fmt.Fprintf(os.Stderr, "airport_artccs.json: %v\n", err)
		os.Exit(1)
	}

	for name, artcc := range data {
		if entry, ok := airports[name]; ok {
			entry.ARTCC = artcc
			airports[name] = entry
		}
	}

	return airports, customAirports
}

func parseAircraft() (map[string]string, map[string]av.AircraftPerformance) {
	r := util.LoadResource("openscope-aircraft.json")
	defer r.Close()

	var acStruct struct {
		Aircraft []av.AircraftPerformance `json:"aircraft"`
	}
	if err := util.UnmarshalJSON(r, &acStruct); err != nil {
		fmt.Fprintf(os.Stderr, "openscope-aircraft.json: %v\n", err)
		os.Exit(1)
	}

	aliases := make(map[string]string)
	ap := make(map[string]av.AircraftPerformance)
	for _, ac := range acStruct.Aircraft {
		aliases[ac.ICAO] = ac.Name

		// If we have mach but not TAS, do the conversion; the nav code
		// works with TAS..
		if ac.Speed.CruiseMach != 0 && ac.Speed.CruiseTAS == 0 {
			ac.Speed.CruiseTAS = 666.739 * ac.Speed.CruiseMach
		}
		if ac.Speed.MaxMach != 0 && ac.Speed.MaxTAS == 0 {
			ac.Speed.MaxTAS = 666.739 * ac.Speed.MaxMach
		}

		ap[ac.ICAO] = ac

		cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
		if !slices.Contains(cwt, ac.Category.CWT) {
			fmt.Fprintf(os.Stderr, "%s: invalid CWT category provided\n", ac.Category.CWT)
		}
		if ac.Rate.Climb < 500 || ac.Rate.Climb > 5000 {
			fmt.Fprintf(os.Stderr, "%s: aircraft climb rate %f seems off\n", ac.ICAO, ac.Rate.Climb)
		}
		if ac.Rate.Descent < 500 || ac.Rate.Descent > 5000 {
			fmt.Fprintf(os.Stderr, "%s: aircraft descent rate %f seems off\n", ac.ICAO, ac.Rate.Descent)
		}
		if ac.Rate.Accelerate < 2 || ac.Rate.Accelerate > 10 {
			fmt.Fprintf(os.Stderr, "%s: aircraft accelerate rate %f seems off\n", ac.ICAO, ac.Rate.Accelerate)
		}
		if ac.Rate.Decelerate < 2 || ac.Rate.Decelerate > 8 {
			fmt.Fprintf(os.Stderr, "%s: aircraft decelerate rate %f seems off\n", ac.ICAO, ac.Rate.Decelerate)
		}
		if ac.Speed.Min < 34 || ac.Speed.Min > 200 {
			fmt.Fprintf(os.Stderr, "%s: aircraft min speed %f seems off\n", ac.ICAO, ac.Speed.Min)
		}
		if ac.Speed.Landing < 40 || ac.Speed.Landing > 200 {
			fmt.Fprintf(os.Stderr, "%s: aircraft landing speed %f seems off\n", ac.ICAO, ac.Speed.Landing)
		}
		if ac.Speed.MaxTAS < 40 || (ac.Speed.MaxTAS > 550 && ac.ICAO != "CONC") {
			fmt.Fprintf(os.Stderr, "%s: aircraft max TAS %f seems off\n", ac.ICAO, ac.Speed.MaxTAS)
		}
		if ac.Speed.V2 != 0 && ac.Speed.V2 > 1.5*ac.Speed.Min {
			fmt.Fprintf(os.Stderr, "%s: aircraft V2 %.0f seems suspiciously high (vs min %.01f)",
				ac.ICAO, ac.Speed.V2, ac.Speed.Min)
		}
		if t := ac.Engine.AircraftType; t != "P" && t != "T" && t != "J" && t != "H" {
			fmt.Fprintf(os.Stderr, `%s: aircraft type %q should be "P", "T", "J", or "H".\n`,
				ac.ICAO, t)
		}
		if ac.Turn.MaxBankAngle < 5 {
			fmt.Fprintf(os.Stderr, "%s: aircraft maximum bank angle %f is suspiciously low", ac.ICAO, ac.Turn.MaxBankAngle)
		}
		if ac.Turn.MaxBankRate < 1 {
			fmt.Fprintf(os.Stderr, "%s: aircraft maximum bank rate %f is suspiciously low", ac.ICAO, ac.Turn.MaxBankRate)
		}
	}

	return aliases, ap
}

// borderAirportTimeZones covers the airports that sit closer to a time zone
// boundary than the roughly 3km the boundaries are resolved to, so that looking
// the zone up from the airport's position puts it on the wrong side.
var borderAirportTimeZones = map[av.ICAOAirportCode]string{
	"KLSF": "America/New_York", // Fort Benning, a mile east of the Chattahoochee
}

// AirportTimeZone returns the local time zone at an airport, from where it is.
// It fails for an airport that isn't in the database or that isn't in any time
// zone.
func (d *StaticDatabase) AirportTimeZone(id av.ICAOAirportCode) (*time.Location, bool) {
	ap, ok := d.Airports[id]
	if !ok {
		return nil, false
	}
	if zone, ok := borderAirportTimeZones[id]; ok {
		loc, err := time.LoadLocation(zone)
		return loc, err == nil
	}
	return util.TimeZoneAt(ap.Location.Latitude(), ap.Location.Longitude())
}

func parseAirlines() (map[string]av.Airline, map[string]string) {
	r := util.LoadResource("openscope-airlines.json")
	defer r.Close()

	var alStruct struct {
		Airlines []av.Airline `json:"airlines"`
	}
	if err := util.UnmarshalJSON(r, &alStruct); err != nil {
		fmt.Fprintf(os.Stderr, "openscope-airlines.json: %v\n", err)
		os.Exit(1)
	}

	airlines := make(map[string]av.Airline)
	callsigns := make(map[string]string)
	for _, al := range alStruct.Airlines {
		fixedAirline := al
		fixedAirline.Fleets = make(map[string][]av.FleetAircraft)
		for name, aircraft := range fixedAirline.JSONFleets {
			for _, ac := range aircraft {
				fleetAC := av.FleetAircraft{
					ICAO:  strings.ToUpper(ac[0].(string)),
					Count: int(ac[1].(float64)),
				}
				fixedAirline.Fleets[name] = append(fixedAirline.Fleets[name], fleetAC)
			}
		}
		fixedAirline.JSONFleets = nil

		airlines[strings.ToUpper(al.ICAO)] = fixedAirline
		callsigns[strings.ToUpper(al.ICAO)] = al.Callsign.Name
	}
	return airlines, callsigns
}

// FAA Coded Instrument Flight Procedures (CIFP)
// https://www.faa.gov/air_traffic/flight_info/aeronav/digital_products/cifp/download/
func parseCIFP() ARINC424Result {
	r := util.LoadResource("FAACIFP18.zst")
	defer r.Close()
	return ParseARINC424(r)
}

// parseHPF parses the FAA Holding Pattern File (HPF) CSV files and returns holds.
// HPF provides additional holds not found in CIFP, particularly for STARs and enroute holds.
// https://www.faa.gov/air_traffic/flight_info/aeronav/aero_data/NASR_Subscription/
func parseHPF() map[string][]av.Hold {
	type hpfBase struct {
		fixID         string
		courseInbound string
		turnDirection string
		legLengthDist string
		chartType     string
		speedRange    string
		altitudeRange string
	}

	holds := make(map[string]hpfBase) // HP_NAME -> hold data

	// Parse HPF_BASE.csv for core hold data
	baseR := util.LoadResource("HPF_BASE.csv.zst")
	defer baseR.Close()
	mungeCSV("HPF_BASE", baseR,
		[]string{"HP_NAME", "FIX_ID", "COURSE_INBOUND_DEG", "TURN_DIRECTION", "LEG_LENGTH_DIST"},
		func(s []string) {
			h := hpfBase{
				fixID:         strings.TrimSpace(s[1]),
				courseInbound: strings.TrimSpace(s[2]),
				turnDirection: strings.TrimSpace(s[3]),
				legLengthDist: strings.TrimSpace(s[4]),
			}
			holds[strings.TrimSpace(s[0])] = h
		})

	// Parse HPF_CHRT.csv for charting type
	chartR := util.LoadResource("HPF_CHRT.csv.zst")
	defer chartR.Close()
	mungeCSV("HPF_CHRT", chartR,
		[]string{"HP_NAME", "CHARTING_TYPE_DESC"},
		func(s []string) {
			name := strings.TrimSpace(s[0])
			if h, ok := holds[name]; ok {
				h.chartType = strings.TrimSpace(s[1])
				holds[name] = h
			}
		})

	// Parse HPF_SPD_ALT.csv for speed and altitude restrictions
	spdAltR := util.LoadResource("HPF_SPD_ALT.csv.zst")
	defer spdAltR.Close()
	mungeCSV("HPF_SPD_ALT", spdAltR,
		[]string{"HP_NAME", "SPEED_RANGE", "ALTITUDE"},
		func(s []string) {
			name := strings.TrimSpace(s[0])
			if h, ok := holds[name]; ok {
				h.speedRange = strings.TrimSpace(s[1])
				h.altitudeRange = strings.TrimSpace(s[2])
				holds[name] = h
			}
		})

	// Convert to Hold objects
	enrouteHolds := make(map[string][]av.Hold)

	for _, h := range holds {
		if h.fixID == "" || h.courseInbound == "" || h.turnDirection == "" {
			continue
		}

		hold := av.Hold{
			Fix:           h.fixID,
			TurnDirection: av.TurnLeft,
		}

		if course, err := strconv.Atoi(h.courseInbound); err == nil {
			hold.InboundCourse = math.MagneticHeading(course)
		}
		if h.turnDirection == "R" {
			hold.TurnDirection = av.TurnRight
		}
		// Parse leg length (nautical miles) or default to time-based
		if h.legLengthDist != "" {
			if dist, err := strconv.Atoi(h.legLengthDist); err == nil {
				hold.LegLengthNM = float32(dist)
			}
		} else {
			// Default to 1 minute hold if no distance specified
			hold.LegMinutes = 1
		}

		if h.speedRange != "" {
			if speed, err := strconv.Atoi(h.speedRange); err == nil {
				hold.HoldingSpeed = speed
			}
		}

		// Parse altitude restrictions (format: "MIN/MAX" in hundreds of feet)
		if h.altitudeRange != "" {
			parts := strings.Split(h.altitudeRange, "/")
			if len(parts) == 2 {
				if minAlt, err := strconv.Atoi(parts[0]); err == nil {
					hold.MinimumAltitude = minAlt * 100
				}
				if maxAlt, err := strconv.Atoi(parts[1]); err == nil {
					hold.MaximumAltitude = maxAlt * 100
				}
			}
		}

		// HPF doesn't provide specific procedure names
		hold.Procedure = ""

		// Add to enroute holds (HPF doesn't provide airport associations for terminal holds)
		enrouteHolds[h.fixID] = append(enrouteHolds[h.fixID], hold)
	}

	return enrouteHolds
}

func parseMVAs() map[string][]MVA {
	// The MVA files are stored in a zip file to avoid the overhead of
	// opening lots of files to read them in.
	z := util.LoadResourceBytes("mva-fus3.zip")
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		panic(err)
	}

	type mvaTracon struct {
		TRACON string
		MVAs   []MVA
	}
	parsed := make([]mvaTracon, len(zr.File))

	var wg sync.WaitGroup
	for i, f := range zr.File {
		// Launch a goroutine for each one so that we load them in
		// parallel.
		wg.Go(func() {
			r, err := f.Open()
			if err != nil {
				// Errors are panics since this all happens at startup time
				// with data that's fixed at release time.
				panic(err)
			}

			zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(0))
			if err != nil {
				panic(err)
			}
			defer zr.Close()

			decoder := xml.NewDecoder(zr)

			var mvas []MVA
			tracon := ""
			for {
				// The full XML schema is fairly complex so rather than
				// declaring a ton of helper types to represent the full
				// nested complexity, we'll instead walk through until we
				// find the sections where the MVA altitudes and polygons
				// are defined.
				token, _ := decoder.Token()
				if token == nil {
					break
				}

				if se, ok := token.(xml.StartElement); ok {
					switch se.Name.Local {
					case "description":
						// The first <ns1:description> in the file will be
						// of the form ABE_MVA_FUS3_2022, which gives us
						// the name of the TRACON we've got (ABE, in that
						// case). Subsequent descriptions should all be
						// "MINIMUM VECTORING ALTITUDE (MVA)"
						var desc string
						if err := decoder.DecodeElement(&desc, &se); err != nil {
							panic(fmt.Sprintf("Error decoding element: %v", err))
						}

						if tracon == "" {
							var ok bool
							tracon, _, ok = strings.Cut(desc, "_")
							if !ok {
								panic(desc + ": unexpected description string")
							}
						} else if desc != "MINIMUM VECTORING ALTITUDE (MVA)" {
							panic(desc)
						}

					case "AirspaceVolume":
						var m MVA
						if err := decoder.DecodeElement(&m, &se); err != nil {
							panic(fmt.Sprintf("Error decoding element: %v", err))
						}

						// Parse the floats and initialize the rings
						patch := m.Proj.Surface.Patches.PolygonPatch
						m.ExteriorRing = patch.Exterior.LinearRing.Vertices()
						for _, in := range patch.Interiors {
							m.InteriorRings = append(m.InteriorRings, in.LinearRing.Vertices())
						}

						m.Proj = nil // Don't hold on to the strings

						// Initialize the bounding box
						m.Bounds = math.Extent2DFromPoints(m.ExteriorRing)

						mvas = append(mvas, m)
					}
				}
			}

			r.Close()

			parsed[i] = mvaTracon{TRACON: tracon, MVAs: mvas}
		})
	}
	wg.Wait()

	// Gathered in the zip's order rather than as the goroutines finish: a
	// TRACON with more than one MVA chart (D01 has Denver's and Grand
	// Junction's) takes the volumes of both, always in the same order.
	mvas := make(map[string][]MVA)
	for _, m := range parsed {
		mvas[m.TRACON] = append(mvas[m.TRACON], m.MVAs...)
	}

	return mvas
}

func parseAirportPairRoutes() map[AirportPair][]AirportPairRoute {
	routes := make(map[AirportPair][]AirportPairRoute)

	r := util.LoadResource("routes.csv.zst")
	defer r.Close()
	mungeCSV("routes", r, []string{"orig", "dest", "type", "dep_fix", "acft", "rnav", "route"},
		func(s []string) {
			pair := AirportPair{From: av.ICAOAirportCode(strings.TrimSpace(s[0])), To: av.ICAOAirportCode(strings.TrimSpace(s[1]))}
			routes[pair] = append(routes[pair], AirportPairRoute{
				Route:        strings.TrimSpace(s[6]),
				DepartureFix: strings.TrimSpace(s[3]),
				Type:         strings.TrimSpace(s[2]),
				Aircraft:     strings.TrimSpace(s[4]),
				RNAVRequired: strings.TrimSpace(s[5]) == "Y",
			})
		})

	return routes
}

func parseFacilities() (map[string]ARTCC, map[string]TRACON, map[string]ATCT) {
	ar := util.LoadResource("artccs.json")
	defer ar.Close()
	var artccs map[string]ARTCC
	if err := util.UnmarshalJSON(ar, &artccs); err != nil {
		fmt.Fprintf(os.Stderr, "artccs.json: %v\n", err)
		os.Exit(1)
	}

	tr := util.LoadResource("tracons.json")
	defer tr.Close()
	var tracons map[string]TRACON
	if err := util.UnmarshalJSON(tr, &tracons); err != nil {
		fmt.Fprintf(os.Stderr, "tracons.json: %v\n", err)
		os.Exit(1)
	}

	for name, artcc := range artccs {
		if artcc.Latitude == 0 || artcc.Longitude == 0 || artcc.Radius == 0 {
			fmt.Fprintf(os.Stderr, "%s: ARTCC missing latitude/longitude/radius in artccs.json\n", name)
		}
	}

	// Validate that all of the TRACON ARTCCs are known.
	for name, tracon := range tracons {
		if _, ok := artccs[tracon.ARTCC]; !ok {
			fmt.Fprintln(os.Stderr, tracon.ARTCC+": ARTCC unknown for TRACON "+name)
			os.Exit(1)
		}
		if tracon.Radius < 20 {
			fmt.Fprintf(os.Stderr, tracon.ARTCC+": unexpectedly small radius %f\n", tracon.Radius)
			os.Exit(1)
		}
		if tracon.Latitude == 0 || tracon.Longitude == 0 {
			fmt.Fprintf(os.Stderr, "%s: TRACON missing latitude/longitude in tracons.json\n", name)
		}
	}

	at := util.LoadResource("atcts.json")
	defer at.Close()
	var atcts map[string]ATCT
	if err := util.UnmarshalJSON(at, &atcts); err != nil {
		fmt.Fprintf(os.Stderr, "atcts.json: %v\n", err)
		os.Exit(1)
	}

	// Validate that all of the ATCT ARTCCs are known.
	for name, atct := range atcts {
		if _, ok := artccs[atct.ARTCC]; !ok {
			fmt.Fprintln(os.Stderr, atct.ARTCC+": ARTCC unknown for ATCT "+name)
			os.Exit(1)
		}
	}

	return artccs, tracons, atcts
}

func parseAirspace(filename string) map[string][]av.AirspaceVolume {
	aj := util.LoadResource(filename)
	defer aj.Close()

	// These should match the definition in util/airspace.go
	type AirspaceLoop [][2]float32
	type airspaceEntry struct {
		Bottom, Top int
		// First one is exterior; any additional ones are holes.
		Loops []AirspaceLoop
	}

	var airspace map[string][]airspaceEntry
	if err := util.UnmarshalJSON(aj, &airspace); err != nil {
		panic(err)
	}

	// Uplift to vice's internal AirspaceVolume representation.
	convert := func(v [][2]float32) []math.Point2LL {
		return util.MapSlice(v, func(p [2]float32) math.Point2LL { return math.Point2LL(p) })
	}
	vols := make(map[string][]av.AirspaceVolume)
	for name, as := range airspace {
		var v []av.AirspaceVolume
		for _, a := range as {
			bounds := math.Extent2DFromPoints(a.Loops[0])

			id := name
			if len(id) > 7 {
				id = id[:7]
			}
			vol := av.AirspaceVolume{
				Id:            id,
				Description:   name,
				Type:          av.AirspaceVolumePolygon,
				Floor:         a.Bottom,
				Ceiling:       a.Top,
				Vertices:      convert(a.Loops[0]),
				PolygonBounds: &bounds,
			}
			for _, l := range a.Loops[1:] {
				vol.Holes = append(vol.Holes, util.MapSlice(l, func(p [2]float32) av.ScenarioPoint2LL {
					return av.ScenarioPoint2LL{Point2LL: math.Point2LL(p)}
				}))
			}
			v = append(v, vol)
		}
		vols[name] = v
	}

	return vols
}

// Pronunciations maps written text to the phonetic spellings that work
// better with voice synthesis. Where a slice is stored, one of its items is
// chosen at random when one is needed.
type Pronunciations struct {
	Airports map[string][]string
	ACTypes  map[string][]string
	Fixes    map[string]string
	Airlines map[string]string
	SIDs     map[string]string
	STARs    map[string]string
}

// parsePronunciations reads the say*.json files; it is called as part of
// loading the aviation database, so editing one of them takes effect on a
// reload along with the rest of it.
func parsePronunciations() Pronunciations {
	var say Pronunciations
	load := func(file string, m any) {
		if err := json.Unmarshal(util.LoadResourceBytes(file), m); err != nil {
			panic(fmt.Sprintf("%s: %v", file, err))
		}
	}

	load("sayactype.json", &say.ACTypes)
	load("sayairport.json", &say.Airports)
	load("sayairline.json", &say.Airlines)
	load("sayfix.json", &say.Fixes)
	load("saysid.json", &say.SIDs)
	load("saystar.json", &say.STARs)

	return say
}

// parseScrapedRoutes loads the scraped route database for route selection,
// most-filed routes first.
func parseScrapedRoutes() map[AirportPair][]av.ScrapedRoute {
	sets, err := av.ReadScrapedRoutes(util.GetResourcesFS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	routes := make(map[AirportPair][]av.ScrapedRoute)
	for key, set := range sets {
		if len(set.Routes) == 0 {
			continue
		}
		from, to, ok := strings.Cut(key, "-")
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: %q isn't a FROM-TO city pair\n", av.ScrapedRoutesPath, key)
			os.Exit(1)
		}
		routes[AirportPair{From: av.ICAOAirportCode(from), To: av.ICAOAirportCode(to)}] = set.Routes
	}
	return routes
}

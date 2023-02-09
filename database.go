// database.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StaticDatabase is a catch-all for data about the world that doesn't
// change after it's loaded.  It includes information from FAA databases,
// the sector file, and the position file.
type StaticDatabase struct {
	// From the FAA (et al.) databases
	FAA struct {
		navaids  map[string]Navaid
		airports map[string]Airport
		fixes    map[string]Fix
		prd      map[AirportPair][]PRDEntry
	}
	callsigns           map[string]Callsign
	AircraftTypes       map[string]AircraftType
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline

	// From the sector file
	NmPerLatitude     float32
	NmPerLongitude    float32
	MagneticVariation float32
}

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string `json:"weightClass"`
	Ceiling     int    `json:"ceiling"`
	Rate        struct {
		Climb      int     `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    int     `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min     int `json:"min"`
		Landing int `json:"landing"`
		Cruise  int `json:"cruise"`
		Max     int `json:"max"`
	} `json:"speed"`
}

type Airline struct {
	ICAO     string `json:"icao"`
	Name     string `json:"name"`
	Callsign struct {
		CallsignFormats []string `json:"callsignFormats"`
	} `json:"callsign"`
	JSONFleets map[string][][2]interface{} `json:"fleets"`
	Fleets     map[string][]FleetAircraft
}

type FleetAircraft struct {
	ICAO  string
	Count int
}

type PRDEntry struct {
	Depart, Arrive          string
	Route                   string
	Hours                   [3]string
	Type                    string
	Area                    string
	Altitude                string
	Aircraft                string
	Direction               string
	Seq                     string
	DepCenter, ArriveCenter string
}

// Label represents a labeled point on a map.
type Label struct {
	name  string
	p     Point2LL
	color RGB
}

func InitializeStaticDatabase(dbChan chan *StaticDatabase) {
	start := time.Now()

	db := &StaticDatabase{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { db.FAA.navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.fixes = parseFixes(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.prd = parsePRD(); wg.Done() }()
	wg.Add(1)
	go func() { db.callsigns = parseCallsigns(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftTypes, db.AircraftTypeAliases = parseAircraftTypes(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftPerformance = parseAircraftPerformance(); wg.Done() }()
	wg.Add(1)
	go func() { db.Airlines = parseAirlines(); wg.Done() }()
	wg.Wait()

	lg.Printf("Parsed built-in databases in %v", time.Since(start))

	dbChan <- db
}

///////////////////////////////////////////////////////////////////////////
// FAA databases

var (
	// https://www.faa.gov/air_traffic/flight_info/aeronav/aero_data/NASR_Subscription_2022-07-14/
	//go:embed resources/NAV_BASE.csv.zst
	navBaseRaw string
	//go:embed resources/APT_BASE.csv.zst
	airportsRaw string
	//go:embed resources/FIX_BASE.csv.zst
	fixesRaw string
	//go:embed resources/callsigns.csv.zst
	callsignsRaw string
	//go:embed resources/virtual-callsigns.csv.zst
	virtualCallsignsRaw string
	//go:embed resources/prefroutes_db.csv.zst
	prdRaw string

	// Via Arash Partow, MIT licensed
	// https://www.partow.net/miscellaneous/airportdatabase/
	//go:embed resources/GlobalAirportDatabase.txt.zst
	globalAirportsRaw string

	//go:embed resources/aircraft.json
	aircraftTypesRaw string
)

// Utility function for parsing CSV files as strings; it breaks each line
// of the file into fields and calls the provided callback function for
// each one.
func mungeCSV(filename string, raw string, callback func([]string)) {
	r := bytes.NewReader([]byte(raw))
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Skip the first line with the legend
	if _, err := cr.Read(); err != nil {
		lg.Errorf("%s: error parsing CSV file: %s", filename, err)
		return
	}

	for {
		if record, err := cr.Read(); err == io.EOF {
			return
		} else if err != nil {
			lg.Errorf("%s: error parsing CSV file: %s", filename, err)
			return
		} else {
			callback(record)
		}
	}
}

// lat and long should be 4-long slices, e.g.: [42 7 12.68 N]
func point2LLFromComponents(lat []string, long []string) Point2LL {
	latitude := atof(lat[0]) + atof(lat[1])/60. + atof(lat[2])/3600.
	if lat[3] == "S" {
		latitude = -latitude
	}
	longitude := atof(long[0]) + atof(long[1])/60. + atof(long[2])/3600.
	if long[3] == "W" {
		longitude = -longitude
	}

	return Point2LL{float32(longitude), float32(latitude)}
}

func point2LLFromStrings(lat, long string) Point2LL {
	return Point2LL{float32(atof(long)), float32(atof(lat))}
}

func parseNavaids() map[string]Navaid {
	navaids := make(map[string]Navaid)

	mungeCSV("navaids", decompressZstd(navBaseRaw), func(s []string) {
		n := Navaid{Id: s[1], Type: s[2], Name: s[7],
			Location: point2LLFromComponents(s[22:26], s[26:30])}
		if n.Id != "" {
			navaids[n.Id] = n
		}
	})

	return navaids
}

func parseAirports() map[string]Airport {
	airports := make(map[string]Airport)

	// FAA database
	mungeCSV("airports", decompressZstd(airportsRaw), func(s []string) {
		if elevation, err := strconv.ParseFloat(s[24], 64); err != nil {
			lg.Errorf("%s: error parsing elevation: %s", s[24], err)
		} else {
			loc := point2LLFromComponents(s[15:19], s[19:23])
			ap := Airport{Id: s[98], Name: s[12], Location: loc, Elevation: int(elevation)}
			if ap.Id == "" {
				ap.Id = s[4] // No ICAO code so grab the FAA airport id
			}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		}
	})

	// Global database; this isn't in CSV, so we need to parse it manually.
	r := bytes.NewReader([]byte(decompressZstd(globalAirportsRaw)))
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		line := scan.Text()
		f := strings.Split(line, ":")
		if len(f) != 16 {
			lg.Errorf("Expected 16 fields, got %d: %s", len(f), line)
		}

		if elevation, err := strconv.ParseFloat(f[13], 64); err != nil {
			lg.Errorf("%s: error parsing elevation: %s", f[13], err)
		} else {
			elevation *= 3.28084 // meters to feet

			ap := Airport{
				Id:        f[0],
				Name:      f[2],
				Location:  point2LLFromStrings(f[14], f[15]),
				Elevation: int(elevation)}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		}
	}

	return airports
}

func parseFixes() map[string]Fix {
	fixes := make(map[string]Fix)

	mungeCSV("fixes", decompressZstd(fixesRaw), func(s []string) {
		f := Fix{
			Id:       s[1],
			Location: point2LLFromComponents(s[5:9], s[9:13])}
		if f.Id != "" {
			fixes[f.Id] = f
		}
	})

	return fixes
}

func parsePRD() map[AirportPair][]PRDEntry {
	prd := make(map[AirportPair][]PRDEntry)

	mungeCSV("prd", decompressZstd(prdRaw), func(s []string) {
		entry := PRDEntry{
			Depart:       s[0],
			Route:        s[1],
			Arrive:       s[2],
			Hours:        [3]string{s[3], s[4], s[5]},
			Type:         s[6],
			Area:         s[7],
			Altitude:     s[8],
			Aircraft:     s[9],
			Direction:    s[10],
			Seq:          s[11],
			DepCenter:    s[12],
			ArriveCenter: s[13]}
		if entry.Depart != "" && entry.Arrive != "" {
			prd[AirportPair{entry.Depart, entry.Arrive}] =
				append(prd[AirportPair{entry.Depart, entry.Arrive}], entry)
		}
	})

	return prd
}

func parseCallsigns() map[string]Callsign {
	callsigns := make(map[string]Callsign)

	addCallsign := func(s []string) {
		fix := func(s string) string { return stopShouting(strings.TrimSpace(s)) }

		cs := Callsign{
			Company:     fix(s[0]),
			Country:     fix(s[1]),
			Telephony:   fix(s[2]),
			ThreeLetter: strings.TrimSpace(s[3])}
		if cs.ThreeLetter != "" && cs.ThreeLetter != "..." {
			callsigns[cs.ThreeLetter] = cs
		}
	}

	mungeCSV("callsigns", decompressZstd(callsignsRaw), addCallsign)
	// Do virtual second since we let them take precedence
	mungeCSV("virtual callsigns", decompressZstd(virtualCallsignsRaw), addCallsign)

	return callsigns
}

func parseAircraftTypes() (map[string]AircraftType, map[string]string) {
	var ac struct {
		AircraftTypes       map[string]AircraftType `json:"Aircraft"`
		AircraftTypeAliases map[string]string       `json:"Aliases"`
	}
	ac.AircraftTypes = make(map[string]AircraftType)
	ac.AircraftTypeAliases = make(map[string]string)

	if err := json.Unmarshal([]byte(aircraftTypesRaw), &ac); err != nil {
		lg.Errorf("%v", err)
	}

	return ac.AircraftTypes, ac.AircraftTypeAliases
}

//go:embed resources/openscope-aircraft.json
var openscopeAircraft string

func parseAircraftPerformance() map[string]AircraftPerformance {
	var acStruct struct {
		Aircraft []AircraftPerformance `json:"aircraft"`
	}
	if err := json.Unmarshal([]byte(openscopeAircraft), &acStruct); err != nil {
		lg.Errorf("%v", err)
	}

	ap := make(map[string]AircraftPerformance)
	for i, ac := range acStruct.Aircraft {
		ap[ac.ICAO] = acStruct.Aircraft[i]
	}

	return ap
}

//go:embed resources/openscope-airlines.json
var openscopeAirlines string

func parseAirlines() map[string]Airline {
	var alStruct struct {
		Airlines []Airline `json:"airlines"`
	}
	if err := json.Unmarshal([]byte(openscopeAirlines), &alStruct); err != nil {
		lg.Errorf("%v", err)
	}

	airlines := make(map[string]Airline)
	for _, al := range alStruct.Airlines {
		fixedAirline := al
		fixedAirline.Fleets = make(map[string][]FleetAircraft)
		for name, aircraft := range fixedAirline.JSONFleets {
			for _, ac := range aircraft {
				fleetAC := FleetAircraft{
					ICAO:  strings.ToUpper(ac[0].(string)),
					Count: int(ac[1].(float64)),
				}
				fixedAirline.Fleets[name] = append(fixedAirline.Fleets[name], fleetAC)
			}
		}
		fixedAirline.JSONFleets = nil

		airlines[strings.ToUpper(al.ICAO)] = fixedAirline
	}
	return airlines
}

///////////////////////////////////////////////////////////////////////////
// Utility methods

// Locate returns the location of a (static) named thing, if we've heard of it.
func (db *StaticDatabase) Locate(name string) (Point2LL, bool) {
	name = strings.ToUpper(name)
	if n, ok := db.FAA.navaids[name]; ok {
		return n.Location, ok
	} else if f, ok := db.FAA.fixes[name]; ok {
		return f.Location, ok
	} else if ap, ok := db.FAA.airports[name]; ok {
		return ap.Location, ok
	} else {
		return Point2LL{}, false
	}
}

func (db *StaticDatabase) LookupAircraftType(ac string) (AircraftType, bool) {
	if t, ok := db.AircraftTypes[ac]; ok {
		return t, true
	}
	if ac, ok := db.AircraftTypeAliases[ac]; ok {
		t, ok := db.AircraftTypes[ac]
		if !ok {
			lg.Errorf("%s: alias not found in aircraft types database", ac)
		}
		return t, ok
	}
	return AircraftType{}, false
}

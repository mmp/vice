// database.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
)

// StaticDatabase is a catch-all for data about the world that doesn't
// change after it's loaded.  It includes information from FAA databases,
// the sector file, and the position file.
type StaticDatabase struct {
	// From the FAA (et al.) databases
	FAA struct {
		navaids map[string]Navaid
		fixes   map[string]Fix
	}
	callsigns           map[string]Callsign
	AircraftTypes       map[string]AircraftType
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline
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

func InitializeStaticDatabase() *StaticDatabase {
	start := time.Now()

	db := &StaticDatabase{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { db.FAA.navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.fixes = parseFixes(); wg.Done() }()
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

	return db
}

///////////////////////////////////////////////////////////////////////////
// FAA databases

var (
	// https://www.faa.gov/air_traffic/flight_info/aeronav/aero_data/NASR_Subscription_2022-07-14/
	//go:embed resources/NAV_BASE.csv.zst
	navBaseRaw string
	//go:embed resources/FIX_BASE.csv.zst
	fixesRaw string
	//go:embed resources/callsigns.csv.zst
	callsignsRaw string

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

func (db *StaticDatabase) CheckAirline(icao, fleet string) []error {
	var errors []error

	al, ok := database.Airlines[icao]
	if !ok {
		errors = append(errors, fmt.Errorf("%s: airline not in database", icao))
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		errors = append(errors,
			fmt.Errorf("%s: fleet unknown for airline \"%s\"", fleet, icao))
	}

	for _, aircraft := range fl {
		if perf, ok := database.AircraftPerformance[aircraft.ICAO]; !ok {
			errors = append(errors,
				fmt.Errorf("%s: aircraft in airline \"%s\"'s fleet \"%s\" not in perf database",
					aircraft.ICAO, icao, fleet))
		} else {
			if perf.Speed.Min < 50 || perf.Speed.Landing < 50 || perf.Speed.Cruise < 50 ||
				perf.Speed.Max < 50 || perf.Speed.Min > perf.Speed.Max {
				fmt.Errorf("%s: aircraft's speed specification is questionable: %s", aircraft.ICAO,
					spew.Sdump(perf.Speed))
			}
			if perf.Rate.Climb == 0 || perf.Rate.Descent == 0 || perf.Rate.Accelerate == 0 ||
				perf.Rate.Decelerate == 0 {
				fmt.Errorf("%s: aircraft's rate specification is questionable: %s", aircraft.ICAO,
					spew.Sdump(perf.Rate))
			}
		}
	}
	return errors
}

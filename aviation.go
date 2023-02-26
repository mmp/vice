// aviation.go
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type METAR struct {
	AirportICAO string
	Time        string
	Auto        bool
	Wind        string
	Weather     string
	Altimeter   string
	Rmk         string
}

func (m METAR) String() string {
	auto := ""
	if m.Auto {
		auto = "AUTO"
	}
	return strings.Join([]string{m.AirportICAO, m.Time, auto, m.Wind, m.Weather, m.Altimeter, m.Rmk}, " ")
}

func ParseMETAR(str string) (*METAR, error) {
	fields := strings.Fields(str)
	if len(fields) < 3 {
		return nil, fmt.Errorf("Expected >= 3 fields in METAR text")
	}

	i := 0
	next := func() string {
		if i == len(fields) {
			return ""
		}
		s := fields[i]
		i++
		return s
	}

	m := &METAR{AirportICAO: next(), Time: next(), Wind: next()}
	if m.Wind == "AUTO" {
		m.Auto = true
		m.Wind = next()
	}

	for {
		s := next()
		if s == "" {
			break
		}
		if s[0] == 'A' || s[0] == 'Q' {
			m.Altimeter = s
			break
		}
		m.Weather += s + " "
	}
	m.Weather = strings.TrimRight(m.Weather, " ")

	if s := next(); s != "RMK" {
		// TODO: improve the METAR parser...
		lg.Printf("Expecting RMK where %s is in METAR \"%s\"", s, str)
	} else {
		for s != "" {
			s = next()
			m.Rmk += s + " "
		}
		m.Rmk = strings.TrimRight(m.Rmk, " ")
	}
	return m, nil
}

type ATIS struct {
	Airport  string
	AppDep   string
	Code     string
	Contents string
}

// Frequencies are scaled by 1000 and then stored in integers.
type Frequency int

func NewFrequency(f float32) Frequency {
	// 0.5 is key for handling rounding!
	return Frequency(f*1000 + 0.5)
}

func (f Frequency) String() string {
	s := fmt.Sprintf("%03d.%03d", f/1000, f%1000)
	for len(s) < 7 {
		s += "0"
	}
	return s
}

type Controller struct {
	Callsign  string    `json:"callsign"`
	Frequency Frequency `json:"frequency"`
	SectorId  string    `json:"sector_id"`  // e.g. N56, 2J, ...
	Scope     string    `json:"scope_char"` // For tracked a/c on the scope--e.g., T
}

type FlightRules int

const (
	UNKNOWN = iota
	IFR
	VFR
	DVFR
	SVFR
)

func (f FlightRules) String() string {
	return [...]string{"Unknown", "IFR", "VFR", "DVFR", "SVFR"}[f]
}

type FlightPlan struct {
	Rules                  FlightRules
	AircraftType           string
	CruiseSpeed            int
	DepartureAirport       string
	DepartTimeEst          int
	DepartTimeActual       int
	Altitude               int
	ArrivalAirport         string
	Hours, Minutes         int
	FuelHours, FuelMinutes int
	AlternateAirport       string
	Route                  string
	Remarks                string
}

type FlightStrip struct {
	callsign    string
	annotations [9]string
}

type Squawk int

func (s Squawk) String() string { return fmt.Sprintf("%04o", s) }

func ParseSquawk(s string) (Squawk, error) {
	if s == "" {
		return Squawk(0), nil
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil {
		return Squawk(0), fmt.Errorf("%s: invalid squawk code", s)
	} else if sq < 0 || sq > 0o7777 {
		return Squawk(0), fmt.Errorf("%s: out of range squawk code", s)
	}
	return Squawk(sq), nil
}

type RadarTrack struct {
	Position    Point2LL
	Altitude    int
	Groundspeed int
	Heading     float32
	Time        time.Time
}

type TransponderMode int

const (
	Standby = iota
	Charlie
	Ident
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "C", "Ident"}[t]
}

type Runway struct {
	Number         string
	Heading        float32
	Threshold, End Point2LL
}

type Navaid struct {
	Id       string
	Type     string
	Name     string
	Location Point2LL
}

type Fix struct {
	Id       string
	Location Point2LL
}

type Callsign struct {
	Company     string
	Country     string
	Telephony   string
	ThreeLetter string
}

func ParseAltitude(s string) (int, error) {
	s = strings.ToUpper(s)
	if strings.HasPrefix(s, "FL") {
		if alt, err := strconv.Atoi(s[2:]); err != nil {
			return 0, err
		} else {
			return alt * 100, nil
		}
	} else if alt, err := strconv.Atoi(s); err != nil {
		return 0, err
	} else {
		return alt, nil
	}
}

func (fp FlightPlan) BaseType() string {
	s := strings.TrimPrefix(fp.TypeWithoutSuffix(), "H/")
	s = strings.TrimPrefix(s, "S/")
	s = strings.TrimPrefix(s, "J/")
	return s
}

func (fp FlightPlan) TypeWithoutSuffix() string {
	// try to chop off equipment suffix
	actypeFields := strings.Split(fp.AircraftType, "/")
	switch len(actypeFields) {
	case 3:
		// Heavy (presumably), with suffix
		return actypeFields[0] + "/" + actypeFields[1]
	case 2:
		if actypeFields[0] == "H" || actypeFields[0] == "S" || actypeFields[0] == "J" {
			// Heavy or super, no suffix
			return actypeFields[0] + "/" + actypeFields[1]
		} else {
			// No heavy, with suffix
			return actypeFields[0]
		}
	default:
		// Who knows, so leave it alone
		return fp.AircraftType
	}
}

type AircraftType struct {
	Name         string
	Manufacturer string

	RECAT string
	Type  string // [ALH]#[JTP] -> { L->land, H->heli, A -> water}, # engines, { Jet, Turboprop, Prop}
	WTC   string // Wake turbulence category: L -> light, M -> medium, H -> heavy, J -> super
	APC   string // Approach category: Vat: A 0-90, B 91-120 C 121-140 D 141-165 E >165

	Initial struct {
		IAS, ROC int
	}
	ClimbFL150 struct {
		IAS, ROC int
	}
	ClimbFL240 struct {
		IAS, ROC int
	}
	Cruise struct {
		Ceiling int // FL
		TAS     int
		ROC     int
		Mach    float32
	}
	Approach struct {
		IAS, MCS int
	}
}

func (ac *AircraftType) NumEngines() int {
	if len(ac.Type) != 3 {
		return 0
	}
	return int([]byte(ac.Type)[1] - '0')
}

func (ac *AircraftType) EngineType() string {
	if len(ac.Type) != 3 {
		return "unknown"
	}
	switch ac.Type[2] {
	case 'P':
		return "piston"
	case 'T':
		return "turboprop"
	case 'J':
		return "jet"
	default:
		return "unknown"
	}
}

func (ac *AircraftType) ApproachCategory() string {
	switch ac.APC {
	case "A":
		return "A: Vat <90, initial 90-150 kts final 70-110 kts"
	case "B":
		return "B: Vat 91-120, initial 120-180 kts final 85-130 kts"
	case "C":
		return "C: Vat 121-140, initial 160-240 kts final 115-160 kts"
	case "D":
		return "D: Vat 141-165, initial 185-250 kts final 180-185 kts"
	case "E":
		return "E: Vat 166-210, initial 185-250 kts final 155-230 kts"
	default:
		return "unknown"
	}
}

func (ac *AircraftType) RECATCategory() string {
	code := "unknown"
	switch ac.RECAT {
	case "F":
		code = "Light"
	case "E":
		code = "Lower Medium"
	case "D":
		code = "Upper Medium"
	case "C":
		code = "Lower Heavy"
	case "B":
		code = "Upper Heavy"
	case "A":
		code = "Super Heavy"
	}

	return ac.RECAT + ": " + code
}

func RECATDistance(leader, follower string) (int, error) {
	dist := [6][6]int{
		[6]int{3, 4, 5, 5, 6, 8},
		[6]int{0, 3, 4, 4, 5, 7},
		[6]int{0, 0, 3, 3, 4, 6},
		[6]int{0, 0, 0, 0, 0, 5},
		[6]int{0, 0, 0, 0, 0, 4},
		[6]int{0, 0, 0, 0, 0, 3},
	}

	if len(leader) != 1 {
		return 0, fmt.Errorf("invalid RECAT leader")
	}
	if len(follower) != 1 {
		return 0, fmt.Errorf("invalid RECAT follower")
	}

	l := int([]byte(leader)[0] - 'A')
	if l < 0 || l >= len(dist) {
		return 0, fmt.Errorf("%s: invalid RECAT leader", leader)
	}

	f := int([]byte(follower)[0] - 'A')
	if f < 0 || f >= len(dist) {
		return 0, fmt.Errorf("%s: invalid follower", follower)
	}

	return dist[l][f], nil
}

func RECATAircraftDistance(leader, follower *Aircraft) (int, error) {
	if leader.FlightPlan == nil || follower.FlightPlan == nil {
		return 0, ErrNoFlightPlan
	}
	if lac, ok := database.LookupAircraftType(leader.FlightPlan.BaseType()); !ok {
		return 0, ErrUnknownAircraftType
	} else {
		if fac, ok := database.LookupAircraftType(follower.FlightPlan.BaseType()); !ok {
			return 0, ErrUnknownAircraftType
		} else {
			return RECATDistance(lac.RECAT, fac.RECAT)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Waypoint

type WaypointCommand int

const (
	WaypointCommandHandoff = iota
	WaypointCommandDelete
)

func (wc WaypointCommand) MarshalJSON() ([]byte, error) {
	switch wc {
	case WaypointCommandHandoff:
		return []byte("\"handoff\""), nil

	case WaypointCommandDelete:
		return []byte("\"delete\""), nil

	default:
		return nil, fmt.Errorf("unhandled WaypointCommand in MarshalJSON")
	}
}

func (wc *WaypointCommand) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"handoff\"":
		*wc = WaypointCommandHandoff
		return nil

	case "\"delete\"":
		*wc = WaypointCommandDelete
		return nil

	default:
		return fmt.Errorf("%s: unknown waypoint command", string(b))
	}
}

type Waypoint struct {
	Fix      string            `json:"fix"`
	Location Point2LL          `json:"-"` // never serialized, derived from fix
	Altitude int               `json:"altitude,omitempty"`
	Speed    int               `json:"speed,omitempty"`
	Heading  int               `json:"heading,omitempty"` // outbound heading after waypoint
	Commands []WaypointCommand `json:"commands,omitempty"`
}

func (wp *Waypoint) ETA(p Point2LL, gs float32) time.Duration {
	dist := nmdistance2ll(p, wp.Location)
	eta := dist / gs
	return time.Duration(eta * float32(time.Hour))
}

type WaypointArray []Waypoint

func (wslice WaypointArray) MarshalJSON() ([]byte, error) {
	var entries []string
	for _, w := range wslice {
		s := w.Fix
		if w.Altitude != 0 {
			s += fmt.Sprintf("@a%d", w.Altitude)
		}
		if w.Speed != 0 {
			s += fmt.Sprintf("@s%d", w.Speed)
		}
		entries = append(entries, s)

		if w.Heading != 0 {
			entries = append(entries, fmt.Sprintf("#%d", w.Heading))
		}

		for _, c := range w.Commands {
			switch c {
			case WaypointCommandHandoff:
				entries = append(entries, "@")

			case WaypointCommandDelete:
				entries = append(entries, "*")
			}
		}
	}

	return []byte("\"" + strings.Join(entries, " ") + "\""), nil
}

func (w *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		*w = nil
		return nil
	}
	wp, err := parseWaypoints(string(b[1 : len(b)-1]))
	if err == nil {
		*w = wp
	}
	return err
}

func mustParseWaypoints(str string) []Waypoint {
	if wp, err := parseWaypoints(str); err != nil {
		panic(err)
	} else {
		return wp
	}
}

func parseWaypoints(str string) ([]Waypoint, error) {
	var waypoints []Waypoint
	for _, field := range strings.Fields(str) {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: \"%s\"", str)
		}

		if field == "@" {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before handoff specifier")
			}
			waypoints[len(waypoints)-1].Commands =
				append(waypoints[len(waypoints)-1].Commands, WaypointCommandHandoff)
		} else if field[0] == '#' {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before heading specifier")
			}
			if hdg, err := strconv.Atoi(field[1:]); err != nil {
				return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", field[1:], err)
			} else {
				waypoints[len(waypoints)-1].Heading = hdg
			}
		} else {
			wp := Waypoint{}
			for i, f := range strings.Split(field, "@") {
				if i == 0 {
					wp.Fix = f
				} else {
					switch f[0] {
					case 'a':
						alt, err := strconv.Atoi(f[1:])
						if err != nil {
							return nil, err
						}
						wp.Altitude = alt

					case 's':
						kts, err := strconv.Atoi(f[1:])
						if err != nil {
							return nil, err
						}
						wp.Speed = kts

					default:
						return nil, fmt.Errorf("%s: unknown @ command '%c", field, f[0])
					}
				}
			}

			waypoints = append(waypoints, wp)
		}
	}

	return waypoints, nil
}

type VideoMap struct {
	Name     string
	Segments []Point2LL

	cb CommandBuffer
}

func (v *VideoMap) InitializeCommandBuffer() {
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)

	for i := 0; i < len(v.Segments)/2; i++ {
		ld.AddLine(v.Segments[2*i], v.Segments[2*i+1])
	}

	ld.GenerateCommands(&v.cb)
}

type RadarSite struct {
	Char     string `json:"char"`
	Id       string `json:"id"`
	Position string `json:"position"`

	Elevation      int32   `json:"elevation"`
	PrimaryRange   int32   `json:"primary_range"`
	SecondaryRange int32   `json:"secondary_range"`
	SlopeAngle     float32 `json:"slope_angle"`
	SilenceAngle   float32 `json:"silence_angle"`
}

// StaticDatabase is a catch-all for data about the world that doesn't
// change after it's loaded.
type StaticDatabase struct {
	Navaids             map[string]Navaid
	Fixes               map[string]Fix
	Callsigns           map[string]Callsign
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
	go func() { db.Navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { db.Fixes = parseFixes(); wg.Done() }()
	wg.Add(1)
	go func() { db.Callsigns = parseCallsigns(); wg.Done() }()
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
	if n, ok := db.Navaids[name]; ok {
		return n.Location, ok
	} else if f, ok := db.Fixes[name]; ok {
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

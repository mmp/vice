// aviation.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
}

func (c *Controller) GetPosition() *Position {
	return database.LookupPosition(c.Callsign, c.Frequency)
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

type AirportPair struct {
	depart, arrive string
}

type Airport struct {
	Id        string
	Name      string
	Elevation int
	Location  Point2LL
}

type Callsign struct {
	Company     string
	Country     string
	Telephony   string
	ThreeLetter string
}

type Position struct {
	Name                  string // e.g., Kennedy Local 1
	Callsign              string // e.g., Kennedy Tower
	Frequency             Frequency
	SectorId              string // For handoffs, etc--e.g., 2W
	Scope                 string // For tracked a/c on the scope--e.g., T
	Id                    string // e.g. JFK_TWR
	LowSquawk, HighSquawk Squawk
}

// Returns nm
func EstimatedFutureDistance(a *Aircraft, b *Aircraft, seconds float32) float32 {
	a0, av := a.TrackPosition(), a.HeadingVector()
	b0, bv := b.TrackPosition(), b.HeadingVector()
	// Heading vector comes back in minutes
	afut := add2f(a0, scale2f(av, seconds/60))
	bfut := add2f(b0, scale2f(bv, seconds/60))
	return nmdistance2ll(afut, bfut)
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

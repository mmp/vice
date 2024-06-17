// aviation.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/klauspost/compress/zstd"
)

type ERAMAdaptation struct { // add more later
	CoordinationFixes map[string]AdaptationFix `json:"coordination_fixes"`
}

const (
	RouteBasedFix = "route"
	ZoneBasedFix  = "zone"
)

type AdaptationFix struct {
	Type           string `json:"type"`
	ToController   string `json:"to"`   // controller to handoff to
	FromController string `json:"from"` // controller to handoff from
}

type FAAAirport struct {
	Id         string
	Name       string
	Elevation  int
	Location   Point2LL
	Runways    []Runway
	Approaches map[string][]WaypointArray
	STARs      map[string]STAR
	ARTCC      string
}

type TRACON struct {
	Name  string
	ARTCC string
}

type ARTCC struct {
	Name string
}

type ReportingPoint struct {
	Fix      string
	Location Point2LL
}

type Arrival struct {
	Waypoints       WaypointArray                       `json:"waypoints"`
	RunwayWaypoints map[string]map[string]WaypointArray `json:"runway_waypoints"` // Airport -> runway -> waypoints
	SpawnWaypoint   string                              `json:"spawn"`            // if "waypoints" aren't specified
	CruiseAltitude  float32                             `json:"cruise_altitude"`
	Route           string                              `json:"route"`
	STAR            string                              `json:"star"`

	InitialController   string  `json:"initial_controller"`
	InitialAltitude     float32 `json:"initial_altitude"`
	AssignedAltitude    float32 `json:"assigned_altitude"`
	InitialSpeed        float32 `json:"initial_speed"`
	SpeedRestriction    float32 `json:"speed_restriction"`
	ExpectApproach      string  `json:"expect_approach"`
	Scratchpad          string  `json:"scratchpad"`
	SecondaryScratchpad string  `json:"secondary_scratchpad"`
	Description         string  `json:"description"`
	CoordinationFix     string  `json:"coordination_fix"`

	// Airport -> arrival airlines
	Airlines map[string][]ArrivalAirline `json:"airlines"`
}

type ArrivalAirline struct {
	ICAO    string `json:"icao"`
	Airport string `json:"airport"`
	Fleet   string `json:"fleet,omitempty"`
}

type STAR struct {
	Transitions     map[string]WaypointArray
	RunwayWaypoints map[string]WaypointArray
}

func (s STAR) Check(e *ErrorLogger) {
	check := func(wps WaypointArray) {
		for _, wp := range wps {
			_, okn := database.Navaids[wp.Fix]
			_, okf := database.Fixes[wp.Fix]
			if !okn && !okf {
				e.ErrorString("fix %s not found in navaid database", wp.Fix)
			}
		}
	}
	for _, wps := range s.Transitions {
		check(wps)
	}
	for _, wps := range s.RunwayWaypoints {
		check(wps)
	}
}

func (s STAR) HasWaypoint(wp string) bool {
	for _, wps := range s.Transitions {
		if slices.ContainsFunc(wps, func(w Waypoint) bool { return w.Fix == wp }) {
			return true
		}
	}
	for _, wps := range s.RunwayWaypoints {
		if slices.ContainsFunc(wps, func(w Waypoint) bool { return w.Fix == wp }) {
			return true
		}
	}
	return false
}

func (s STAR) GetWaypointsFrom(fix string) WaypointArray {
	for _, tr := range SortedMapKeys(s.Transitions) {
		wps := s.Transitions[tr]
		if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == fix }); idx != -1 {
			return wps[idx:]
		}
	}
	for _, tr := range SortedMapKeys(s.RunwayWaypoints) {
		wps := s.RunwayWaypoints[tr]
		if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == fix }); idx != -1 {
			return wps[idx:]
		}
	}
	return nil
}

func MakeSTAR() *STAR {
	return &STAR{
		Transitions:     make(map[string]WaypointArray),
		RunwayWaypoints: make(map[string]WaypointArray),
	}
}

func (s STAR) Print(name string) {
	for tr, wps := range s.Transitions {
		fmt.Printf("%-12s: %s\n", name+"."+tr, wps.Encode())
	}
	for rwy, wps := range s.RunwayWaypoints {
		fmt.Printf("%-12s: %s\n", name+".RWY"+rwy, wps.Encode())
	}
}

type Runway struct {
	Id        string
	Heading   float32
	Threshold Point2LL
	Elevation int
}

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
		lg.Warnf("Expecting RMK where %s is in METAR \"%s\"", s, str)
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
	Callsign           string    // Not provided in scenario JSON
	FullName           string    `json:"full_name"`
	Frequency          Frequency `json:"frequency"`
	SectorId           string    `json:"sector_id"`  // e.g. N56, 2J, ...
	Scope              string    `json:"scope_char"` // For tracked a/c on the scope--e.g., T
	IsHuman            bool      // Not provided in scenario JSON
	FacilityIdentifier string    `json:"facility_id"`     // For example the "N" in "N4P" showing the N90 TRACON
	ERAMFacility       bool      `json:"eram_facility"`   // To weed out N56 and N4P being the same fac
	Facility           string    `json:"facility"`        // So we can get the STARS facility from a controller
	DefaultAirport     string    `json:"default_airport"` // only required if CRDA is a thing
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
	Callsign               string
	Rules                  FlightRules
	AircraftType           string
	CruiseSpeed            int
	AssignedSquawk         Squawk // from ATC
	ECID                   string // Mainly for ERAM
	DepartureAirport       string
	DepartTimeEst          int
	DepartTimeActual       int
	Altitude               int
	ArrivalAirport         string
	Hours, Minutes         int
	FuelHours, FuelMinutes int
	AlternateAirport       string
	Exit                   string
	Route                  string
	Remarks                string
}

type FlightStrip struct {
	Callsign    string
	Annotations [9]string
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

// Special purpose code: beacon codes are squawked in various unusual situations.
type SPC struct {
	Squawk Squawk
	Code   string
}

var spcs = []SPC{
	{Squawk: Squawk(0o7400), Code: "LL"}, // lost link
	{Squawk: Squawk(0o7500), Code: "HJ"}, // hijack
	{Squawk: Squawk(0o7600), Code: "RF"}, // radio failure
	{Squawk: Squawk(0o7700), Code: "EM"}, // emergency condigion
	{Squawk: Squawk(0o7777), Code: "MI"}, // military intercept
}

// SquawkIsSPC returns true if the given beacon code is a SPC.  The second
// return value is a string giving the two-letter abbreviated SPC it
// corresponds to.
func SquawkIsSPC(squawk Squawk) (bool, string) {
	for _, spc := range spcs {
		if spc.Squawk == squawk {
			return true, spc.Code
		}
	}
	return false, ""
}

func StringIsSPC(code string) bool {
	return slices.ContainsFunc(spcs, func(spc SPC) bool { return spc.Code == code })
}

type RadarTrack struct {
	Position    Point2LL
	Altitude    int
	Groundspeed int
	Time        time.Time
}

func FormatAltitude(falt float32) string {
	alt := int(falt)
	if alt >= 18000 {
		return "FL" + strconv.Itoa(alt/100)
	} else if alt < 1000 {
		return strconv.Itoa(alt)
	} else {
		th := alt / 1000
		hu := (alt % 1000) / 100 * 100
		if th == 0 {
			return strconv.Itoa(hu)
		} else if hu == 0 {
			return strconv.Itoa(th) + ",000"
		} else {
			return fmt.Sprintf("%d,%03d", th, hu)
		}
	}
}

type TransponderMode int

const (
	Standby = iota
	Charlie
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "C"}[t]
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

func (aircraft *Aircraft) NewFlightPlan(r FlightRules, ac, dep, arr string) *FlightPlan {
	return &FlightPlan{
		Callsign:         aircraft.Callsign,
		Rules:            r,
		AircraftType:     ac,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
		AssignedSquawk:   aircraft.Squawk,
		ECID:             "XXX", // TODO. (Mainly for FDIO and ERAM so not super high priority. )
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

func PlausibleFinalAltitude(w *World, fp *FlightPlan, perf AircraftPerformance) (altitude int) {
	// try to figure out direction of flight
	dep, dok := database.Airports[fp.DepartureAirport]
	arr, aok := database.Airports[fp.ArrivalAirport]
	if !dok || !aok {
		return 34000
	}

	pDep, pArr := dep.Location, arr.Location
	if nmdistance2ll(pDep, pArr) < 100 {
		altitude = 7000
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			altitude += 1000
		}
	} else if nmdistance2ll(pDep, pArr) < 200 {
		altitude = 11000
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			altitude += 1000
		}
	} else if nmdistance2ll(pDep, pArr) < 300 {
		altitude = 21000
	} else {
		altitude = 37000
	}
	altitude = min(altitude, int(perf.Ceiling))

	if headingp2ll(pDep, pArr, w.NmPerLongitude, w.MagneticVariation) > 180 {
		altitude += 1000
	}

	return
}

///////////////////////////////////////////////////////////////////////////

type RadioTransmissionType int

const (
	RadioTransmissionContact    = iota // Messages initiated by the pilot
	RadioTransmissionReadback          // Reading back an instruction
	RadioTransmissionUnexpected        // Something urgent or unusual
)

func (r RadioTransmissionType) String() string {
	switch r {
	case RadioTransmissionContact:
		return "contact"
	case RadioTransmissionReadback:
		return "readback"
	case RadioTransmissionUnexpected:
		return "urgent"
	default:
		return "(unhandled type)"
	}
}

type RadioTransmission struct {
	Controller string
	Message    string
	Type       RadioTransmissionType
}

func PostRadioEvents(from string, transmissions []RadioTransmission, ep EventPoster) {
	for _, rt := range transmissions {
		ep.PostEvent(Event{
			Type:                  RadioTransmissionEvent,
			Callsign:              from,
			ToController:          rt.Controller,
			Message:               rt.Message,
			RadioTransmissionType: rt.Type,
		})
	}
}

///////////////////////////////////////////////////////////////////////////

type TurnMethod int

const (
	TurnClosest = iota // default
	TurnLeft
	TurnRight
)

func (t TurnMethod) String() string {
	return []string{"closest", "left", "right"}[t]
}

const StandardTurnRate = 3

func TurnAngle(from, to float32, turn TurnMethod) float32 {
	switch turn {
	case TurnLeft:
		return NormalizeHeading(from - to)

	case TurnRight:
		return NormalizeHeading(to - from)

	case TurnClosest:
		return abs(headingDifference(from, to))

	default:
		panic("unhandled TurnMethod")
	}
}

///////////////////////////////////////////////////////////////////////////
// HILPT

type PTType int

const (
	PTUndefined = iota
	PTRacetrack
	PTStandard45
)

func (pt PTType) String() string {
	return []string{"undefined", "racetrack", "standard 45"}[pt]
}

type ProcedureTurn struct {
	Type         PTType
	RightTurns   bool
	ExitAltitude int     `json:",omitempty"`
	MinuteLimit  float32 `json:",omitempty"`
	NmLimit      float32 `json:",omitempty"`
	Entry180NoPT bool    `json:",omitempty"`
}

type RacetrackPTEntry int

const (
	DirectEntryShortTurn = iota
	DirectEntryLongTurn
	ParallelEntry
	TeardropEntry
)

func (e RacetrackPTEntry) String() string {
	return []string{"direct short", "direct long", "parallel", "teardrop"}[int(e)]
}

func (e RacetrackPTEntry) MarshalJSON() ([]byte, error) {
	s := "\"" + e.String() + "\""
	return []byte(s), nil
}

func (e *RacetrackPTEntry) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		return fmt.Errorf("invalid HILPT")
	}

	switch string(b[1 : len(b)-1]) {
	case "direct short":
		*e = DirectEntryShortTurn
	case "direct long":
		*e = DirectEntryLongTurn
	case "parallel":
		*e = ParallelEntry
	case "teardrop":
		*e = TeardropEntry
	default:
		return fmt.Errorf("%s: malformed HILPT JSON", string(b))
	}
	return nil
}

func (pt *ProcedureTurn) SelectRacetrackEntry(inboundHeading float32, aircraftFixHeading float32) RacetrackPTEntry {
	// Rotate so we can treat inboundHeading as 0.
	hdg := aircraftFixHeading - inboundHeading
	if hdg < 0 {
		hdg += 360
	}

	if pt.RightTurns {
		if hdg > 290 {
			return DirectEntryLongTurn
		} else if hdg < 110 {
			return DirectEntryShortTurn
		} else if hdg > 180 {
			return ParallelEntry
		} else {
			return TeardropEntry
		}
	} else {
		if hdg > 250 {
			return DirectEntryShortTurn
		} else if hdg < 70 {
			return DirectEntryLongTurn
		} else if hdg < 180 {
			return ParallelEntry
		} else {
			return TeardropEntry
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Wind

type Wind struct {
	Direction int32 `json:"direction"`
	Speed     int32 `json:"speed"`
	Gust      int32 `json:"gust"`
}

type WindModel interface {
	GetWindVector(p Point2LL, alt float32) Point2LL
	AverageWindVector() [2]float32
}

///////////////////////////////////////////////////////////////////////////
// AltitudeRestriction

type AltitudeRestriction struct {
	// We treat 0 as "unset", which works naturally for the bottom but
	// requires occasional care at the top.
	Range [2]float32
}

func (a *AltitudeRestriction) UnmarshalJSON(b []byte) error {
	// For backwards compatibility with saved scenarios, we allow
	// unmarshaling from the single-valued altitude restrictions we had
	// before.
	if alt, err := strconv.Atoi(string(b)); err == nil {
		a.Range = [2]float32{float32(alt), float32(alt)}
		return nil
	} else {
		// Otherwise declare a temporary variable with matching structure
		// but a different type to avoid an infinite loop when
		// json.Unmarshal is called.
		ar := struct{ Range [2]float32 }{}
		if err := json.Unmarshal(b, &ar); err == nil {
			a.Range = ar.Range
			return nil
		} else {
			return err
		}
	}
}

func (a AltitudeRestriction) TargetAltitude(alt float32) float32 {
	if a.Range[1] != 0 {
		return clamp(alt, a.Range[0], a.Range[1])
	} else {
		return max(alt, a.Range[0])
	}
}

// ClampRange limits a range of altitudes to satisfy the altitude
// restriction; the returned Boolean indicates whether the ranges
// overlapped.
func (a AltitudeRestriction) ClampRange(r [2]float32) ([2]float32, bool) {
	a0, a1 := a.Range[0], a.Range[1]
	if a1 == 0 {
		a1 = 1000000
	}

	ok := r[0] <= a1 || r[1] >= a0
	return [2]float32{clamp(r[0], a0, a1), clamp(r[1], a0, a1)}, ok
}

// Summary returns a human-readable summary of the altitude
// restriction.
func (a AltitudeRestriction) Summary() string {
	if a.Range[0] != 0 {
		if a.Range[1] == a.Range[0] {
			return fmt.Sprintf("at %s", FormatAltitude(a.Range[0]))
		} else if a.Range[1] != 0 {
			return fmt.Sprintf("between %s-%s", FormatAltitude(a.Range[0]), FormatAltitude(a.Range[1]))
		} else {
			return fmt.Sprintf("at or above %s", FormatAltitude(a.Range[0]))
		}
	} else if a.Range[1] != 0 {
		return fmt.Sprintf("at or below %s", FormatAltitude(a.Range[1]))
	} else {
		return ""
	}
}

// Encoded returns the restriction in the encoded form in which it is
// specified in scenario configuration files, e.g. "5000+" for "at or above
// 5000".
func (a AltitudeRestriction) Encoded() string {
	if a.Range[0] != 0 {
		if a.Range[0] == a.Range[1] {
			return fmt.Sprintf("%.0f", a.Range[0])
		} else if a.Range[1] != 0 {
			return fmt.Sprintf("%.0f-%.0f", a.Range[0], a.Range[1])
		} else {
			return fmt.Sprintf("%.0f+", a.Range[0])
		}
	} else if a.Range[1] != 0 {
		return fmt.Sprintf("%.0f-", a.Range[1])
	} else {
		return ""
	}
}

// ParseAltitudeRestriction parses an altitude restriction in the compact
// text format used in scenario definition files.
func ParseAltitudeRestriction(s string) (*AltitudeRestriction, error) {
	n := len(s)
	if n == 0 {
		return nil, fmt.Errorf("%s: no altitude provided for crossing restriction", s)
	}

	if s[n-1] == '-' {
		// At or below
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		return &AltitudeRestriction{Range: [2]float32{0, float32(alt)}}, nil
	} else if s[n-1] == '+' {
		// At or above
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		return &AltitudeRestriction{Range: [2]float32{float32(alt), 0}}, nil
	} else if alts := strings.Split(s, "-"); len(alts) == 2 {
		// Between
		if low, err := strconv.Atoi(alts[0]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if high, err := strconv.Atoi(alts[1]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if low > high {
			return nil, fmt.Errorf("%s: low altitude %d is above high altitude %d", s, low, high)
		} else {
			return &AltitudeRestriction{Range: [2]float32{float32(low), float32(high)}}, nil
		}
	} else {
		// At
		if alt, err := strconv.Atoi(s); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else {
			return &AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}, nil
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// DMEArc

// Can either be specified with (Fix,Radius), or (Length,Clockwise); the
// remaining fields are then derived from those.
type DMEArc struct {
	Fix            string
	Center         Point2LL
	Radius         float32
	Length         float32
	InitialHeading float32
	Clockwise      bool
}

///////////////////////////////////////////////////////////////////////////
// Waypoint

type Waypoint struct {
	Fix                 string               `json:"fix"`
	Location            Point2LL             // not provided in scenario JSON; derived from fix
	AltitudeRestriction *AltitudeRestriction `json:"altitude_restriction,omitempty"`
	Speed               int                  `json:"speed,omitempty"`
	Heading             int                  `json:"heading,omitempty"` // outbound heading after waypoint
	ProcedureTurn       *ProcedureTurn       `json:"pt,omitempty"`
	NoPT                bool                 `json:"nopt,omitempty"`
	Handoff             bool                 `json:"handoff,omitempty"`
	FlyOver             bool                 `json:"flyover,omitempty"`
	Delete              bool                 `json:"delete,omitempty"`
	Arc                 *DMEArc              `json:"arc,omitempty"`
	IAF, IF, FAF        bool                 // not provided in scenario JSON; derived from fix
}

func (wp Waypoint) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("fix", wp.Fix)}
	if wp.AltitudeRestriction != nil {
		attrs = append(attrs, slog.Any("altitude_restriction", wp.AltitudeRestriction))
	}
	if wp.Speed != 0 {
		attrs = append(attrs, slog.Int("speed", wp.Speed))
	}
	if wp.Heading != 0 {
		attrs = append(attrs, slog.Int("heading", wp.Heading))
	}
	if wp.ProcedureTurn != nil {
		attrs = append(attrs, slog.Any("procedure_turn", wp.ProcedureTurn))
	}
	if wp.IAF {
		attrs = append(attrs, slog.Bool("IAF", wp.IAF))
	}
	if wp.IF {
		attrs = append(attrs, slog.Bool("IF", wp.IF))
	}
	if wp.FAF {
		attrs = append(attrs, slog.Bool("FAF", wp.FAF))
	}
	if wp.NoPT {
		attrs = append(attrs, slog.Bool("no_pt", wp.NoPT))
	}
	if wp.Handoff {
		attrs = append(attrs, slog.Bool("handoff", wp.Handoff))
	}
	if wp.FlyOver {
		attrs = append(attrs, slog.Bool("fly_over", wp.FlyOver))
	}
	if wp.Delete {
		attrs = append(attrs, slog.Bool("delete", wp.Delete))
	}
	if wp.Arc != nil {
		attrs = append(attrs, slog.Any("arc", wp.Arc))
	}

	return slog.GroupValue(attrs...)
}

func (wp *Waypoint) ETA(p Point2LL, gs float32) time.Duration {
	dist := nmdistance2ll(p, wp.Location)
	eta := dist / gs
	return time.Duration(eta * float32(time.Hour))
}

type WaypointArray []Waypoint

func (wslice WaypointArray) Encode() string {
	var entries []string
	for _, w := range wslice {
		s := w.Fix
		if w.AltitudeRestriction != nil {
			s += "/a" + w.AltitudeRestriction.Encoded()
		}
		if w.Speed != 0 {
			s += fmt.Sprintf("/s%d", w.Speed)
		}
		if pt := w.ProcedureTurn; pt != nil {
			if pt.Type == PTStandard45 {
				if !pt.RightTurns {
					s += "/lpt45"
				} else {
					s += "/pt45"
				}
			} else {
				if !pt.RightTurns {
					s += "/lhilpt"
				} else {
					s += "/hilpt"
				}
			}
			if pt.MinuteLimit != 0 {
				s += fmt.Sprintf("%.1fmin", pt.MinuteLimit)
			} else {
				s += fmt.Sprintf("%.1fnm", pt.NmLimit)
			}
			if pt.Entry180NoPT {
				s += "/nopt180"
			}
			if pt.ExitAltitude != 0 {
				s += fmt.Sprintf("/pta%d", pt.ExitAltitude)
			}
		}
		if w.IAF {
			s += "/iaf"
		}
		if w.IF {
			s += "/if"
		}
		if w.FAF {
			s += "/faf"
		}
		if w.NoPT {
			s += "/nopt"
		}
		if w.Handoff {
			s += "/ho"
		}
		if w.FlyOver {
			s += "/flyover"
		}
		if w.Delete {
			s += "/delete"
		}
		if w.Heading != 0 {
			s += fmt.Sprintf("/h%d", w.Heading)
		}
		if w.Arc != nil {
			if w.Arc.Fix != "" {
				s += fmt.Sprintf("/arc%.1f%s", w.Arc.Radius, w.Arc.Fix)
			} else {
				s += fmt.Sprintf("/arc%.1f", w.Arc.Length)
			}
		}

		entries = append(entries, s)

	}

	return strings.Join(entries, " ")
}

func (w *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		// Handle the string encoding used in scenario JSON files
		wp, err := parseWaypoints(string(b[1 : len(b)-1]))
		if err == nil {
			*w = wp
		}
		return err
	} else {
		// Otherwise unmarshal it normally
		var wp []Waypoint
		err := json.Unmarshal(b, &wp)
		if err == nil {
			*w = wp
		}
		return err
	}
}

func (w WaypointArray) CheckDeparture(e *ErrorLogger) {
	w.checkBasics(e)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in departure")
		}
		if war := wp.AltitudeRestriction; war != nil {
			// Make sure it's generally reasonable
			if war.Range[0] < 0 || war.Range[0] >= 50000 || war.Range[1] < 0 || war.Range[1] >= 50000 {
				e.ErrorString("Invalid altitude range: should be between 0 and FL500: %s-%s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}
			if war.Range[0] != 0 {
				if lastMin != 0 && war.Range[0] < lastMin {
					// our minimum must be >= the previous minimum
					e.ErrorString("Minimum altitude %s is lower than previous fix %s's minimum %s",
						FormatAltitude(war.Range[0]), minFix, FormatAltitude(lastMin))
				}
				lastMin = war.Range[0]
				minFix = wp.Fix
			}
		}

		e.Pop()
	}
}

func (w WaypointArray) checkBasics(e *ErrorLogger) {
	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.Speed < 0 || wp.Speed > 300 {
			e.ErrorString("invalid speed restriction %d", wp.Speed)
		}
		e.Pop()
	}
}

func (w WaypointArray) CheckApproach(e *ErrorLogger) {
	w.checkBasics(e)
	w.checkDescending(e)

	if len(w) < 2 {
		e.ErrorString("must have at least two waypoints in an approach")
	}

	/*
		// Disable for now...
		foundFAF := false
		for _, wp := range w {
			if wp.FAF {
				foundFAF = true
				break
			}
		}
		if !foundFAF {
			e.ErrorString("No /faf specifier found in approach")
		}
	*/
}

func (w WaypointArray) CheckArrival(e *ErrorLogger) {
	w.checkBasics(e)
	w.checkDescending(e)

	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		e.Pop()
	}
}

func (w WaypointArray) checkDescending(e *ErrorLogger) {
	// or at least, check not climbing...
	var lastMin float32
	var minFix string // last fix that established a specific minimum alt

	for _, wp := range w {
		e.Push(wp.Fix)

		if war := wp.AltitudeRestriction; war != nil {
			if war.Range[0] != 0 && war.Range[1] != 0 && war.Range[0] > war.Range[1] {
				e.ErrorString("Minimum altitude %s is higher than maximum %s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			// Make sure it's generally reasonable
			if war.Range[0] < 0 || war.Range[0] >= 50000 || war.Range[1] < 0 || war.Range[1] >= 50000 {
				e.ErrorString("Invalid altitude range: should be between 0 and FL500: %s-%s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			if war.Range[0] != 0 {
				if minFix != "" && war.Range[0] > lastMin {
					e.ErrorString("Minimum altitude %s is higher than previous fix %s's minimum %s",
						FormatAltitude(war.Range[1]), minFix, FormatAltitude(lastMin))
				}
				minFix = wp.Fix
				lastMin = war.Range[0]
			}
		}

		e.Pop()
	}

}

func parsePTExtent(pt *ProcedureTurn, extent string) error {
	if len(extent) == 0 {
		// Unspecified; we will use the default of 1min for ILS, 4nm for RNAV
		return nil
	}
	if len(extent) < 3 {
		return fmt.Errorf("%s: invalid extent specification for procedure turn", extent)
	}

	var err error
	var limit float64
	if extent[len(extent)-2:] == "nm" {
		if limit, err = strconv.ParseFloat(extent[:len(extent)-2], 32); err != nil {
			return fmt.Errorf("%s: unable to parse length in nm for procedure turn: %v", extent, err)
		}
		pt.NmLimit = float32(limit)
	} else if extent[len(extent)-3:] == "min" {
		if limit, err = strconv.ParseFloat(extent[:len(extent)-3], 32); err != nil {
			return fmt.Errorf("%s: unable to parse minutes in procedure turn: %v", extent, err)
		}
		pt.MinuteLimit = float32(limit)
	} else {
		return fmt.Errorf("%s: invalid extent units for procedure turn", extent)
	}

	return nil
}

func parseWaypoints(str string) ([]Waypoint, error) {
	var waypoints []Waypoint
	for _, field := range strings.Fields(str) {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: \"%s\"", str)
		}

		wp := Waypoint{}
		for i, f := range strings.Split(field, "/") {
			if i == 0 {
				wp.Fix = f
			} else if len(f) == 0 {
				return nil, fmt.Errorf("no command found after / in \"%s\"", field)
			} else {
				if f == "ho" {
					wp.Handoff = true
				} else if f == "flyover" {
					wp.FlyOver = true
				} else if f == "delete" {
					wp.Delete = true
				} else if f == "iaf" {
					wp.IAF = true
				} else if f == "if" {
					wp.IF = true
				} else if f == "faf" {
					wp.FAF = true
				} else if (len(f) >= 4 && f[:4] == "pt45") || len(f) >= 5 && f[:5] == "lpt45" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Type = PTStandard45
					wp.ProcedureTurn.RightTurns = f[0] == 'p'

					extent := f[4:]
					if !wp.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(wp.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if (len(f) >= 5 && f[:5] == "hilpt") || (len(f) >= 6 && f[:6] == "lhilpt") {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Type = PTRacetrack
					wp.ProcedureTurn.RightTurns = f[0] == 'h'

					extent := f[5:]
					if !wp.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(wp.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if len(f) >= 4 && f[:3] == "pta" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}

					if alt, err := strconv.Atoi(f[3:]); err == nil {
						wp.ProcedureTurn.ExitAltitude = alt
					} else {
						return nil, fmt.Errorf("%s: error parsing procedure turn exit altitude: %v", f[3:], err)
					}
				} else if f == "nopt" {
					wp.NoPT = true
				} else if f == "nopt180" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Entry180NoPT = true
				} else if len(f) >= 4 && f[:3] == "arc" {
					spec := f[3:]
					rend := 0
					for rend < len(spec) &&
						((spec[rend] >= '0' && spec[rend] <= '9') || spec[rend] == '.') {
						rend++
					}
					if rend == 0 {
						return nil, fmt.Errorf("%s: radius not found after /arc", f)
					}

					v, err := strconv.ParseFloat(spec[:rend], 32)
					if err != nil {
						return nil, fmt.Errorf("%s: invalid arc radius/length: %w", f, err)
					}

					if rend == len(spec) {
						// no fix given, so interpret it as an arc length
						wp.Arc = &DMEArc{
							Length: float32(v),
						}
					} else {
						wp.Arc = &DMEArc{
							Fix:    spec[rend:],
							Radius: float32(v),
						}
					}

					// Do these last since they only match the first character...
				} else if f[0] == 'a' {
					var err error
					wp.AltitudeRestriction, err = ParseAltitudeRestriction(f[1:])
					if err != nil {
						return nil, err
					}
				} else if f[0] == 's' {
					kts, err := strconv.Atoi(f[1:])
					if err != nil {
						return nil, fmt.Errorf("%s: error parsing number after speed restriction: %v", f[1:], err)
					}
					wp.Speed = kts
				} else if f[0] == 'h' { // after "ho" and "hilpt" check...
					if hdg, err := strconv.Atoi(f[1:]); err != nil {
						return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", f[1:], err)
					} else {
						wp.Heading = hdg
					}

				} else {
					return nil, fmt.Errorf("%s: unknown fix modifier: %s", field, f)
				}
			}
		}

		if wp.ProcedureTurn != nil && wp.ProcedureTurn.Type == PTUndefined {
			return nil, fmt.Errorf("%s: no procedure turn specified for fix (e.g., pt45/hilpt) even though PT parameters were given", wp.Fix)
		}

		waypoints = append(waypoints, wp)
	}

	return waypoints, nil
}

///////////////////////////////////////////////////////////////////////////

type RadarSite struct {
	Char           string   `json:"char"`
	PositionString string   `json:"position"`
	Position       Point2LL // not in JSON, set during deserialize

	Elevation      int32   `json:"elevation"`
	PrimaryRange   int32   `json:"primary_range"`
	SecondaryRange int32   `json:"secondary_range"`
	SlopeAngle     float32 `json:"slope_angle"`
	SilenceAngle   float32 `json:"silence_angle"`
}

func (rs *RadarSite) CheckVisibility(w *World, p Point2LL, altitude int) (primary, secondary bool, distance float32) {
	// Check altitude first; this is a quick first cull that
	// e.g. takes care of everyone on the ground.
	if altitude <= int(rs.Elevation) {
		return
	}

	// Time to check the angles..
	palt := float32(altitude) * FeetToNauticalMiles
	ralt := float32(rs.Elevation) * FeetToNauticalMiles
	dalt := palt - ralt
	// not quite true distance, but close enough
	distance = nmdistance2ll(rs.Position, p) + abs(palt-ralt)

	// If we normalize the vector from the radar site to the aircraft, then
	// the z (altitude) component gives the cosine of the angle with the
	// "up" direction; in turn, we can check that against the two angles.
	cosAngle := dalt / distance
	// if angle < silence angle, we can't see it, but the test flips since
	// we're testing cosines.
	// FIXME: it's annoying to be repeatedly computing these cosines here...
	if cosAngle > cos(radians(rs.SilenceAngle)) {
		// inside the cone of silence
		return
	}
	// similarly, if angle > 90-slope angle, we can't see it, but again the
	// test flips.
	if cosAngle < cos(radians(90-rs.SlopeAngle)) {
		// below the slope angle
		return
	}

	primary = distance <= float32(rs.PrimaryRange)
	secondary = !primary && distance <= float32(rs.SecondaryRange)
	return
}

type AirspaceVolume struct {
	Name    string             `json:"name"`
	Type    AirspaceVolumeType `json:"type"`
	Floor   int                `json:"floor"`
	Ceiling int                `json:"ceiling"`
	// Polygon
	Vertices []Point2LL `json:"vertices"`
	// Circle
	Center Point2LL `json:"center"`
	Radius float32  `json:"radius"`
}

type AirspaceVolumeType int

const (
	AirspaceVolumePolygon = iota
	AirspaceVolumeCircle
)

func (t *AirspaceVolumeType) MarshalJSON() ([]byte, error) {
	switch *t {
	case AirspaceVolumePolygon:
		return []byte("\"polygon\""), nil
	case AirspaceVolumeCircle:
		return []byte("\"circle\""), nil
	default:
		return nil, fmt.Errorf("%d: unknown airspace volume type", *t)
	}
}

func (t *AirspaceVolumeType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"polygon\"":
		*t = AirspaceVolumePolygon
		return nil
	case "\"circle\"":
		*t = AirspaceVolumeCircle
		return nil
	default:
		return fmt.Errorf("%s: unknown airspace volume type", string(b))
	}
}

func (a *AirspaceVolume) Inside(p Point2LL, alt int) bool {
	if alt <= a.Floor || alt > a.Ceiling {
		return false
	}

	switch a.Type {
	case AirspaceVolumePolygon:
		return PointInPolygon2LL(p, a.Vertices)
	case AirspaceVolumeCircle:
		return nmdistance2ll(p, a.Center) < a.Radius
	default:
		lg.Errorf("%d: unhandled airspace volume type", a.Type)
		return false
	}
}

func (a *AirspaceVolume) GenerateDrawCommands(cb *CommandBuffer, nmPerLongitude float32) {
	ld := GetLinesDrawBuilder()

	switch a.Type {
	case AirspaceVolumePolygon:
		var v [][2]float32
		for _, vtx := range a.Vertices {
			v = append(v, [2]float32(vtx))
		}
		ld.AddLineLoop(v)
	case AirspaceVolumeCircle:
		ld.AddLatLongCircle(a.Center, nmPerLongitude, a.Radius, 360)
	default:
		lg.Errorf("%d: unhandled airspace volume type", a.Type)
	}

	ld.GenerateCommands(cb)
	ReturnLinesDrawBuilder(ld)
}

///////////////////////////////////////////////////////////////////////////
// StaticDatabase

// StaticDatabase is a catch-all for data about the world that doesn't
// change after it's loaded.
type StaticDatabase struct {
	Navaids             map[string]Navaid
	Airports            map[string]FAAAirport
	Fixes               map[string]Fix
	Callsigns           map[string]string // 3 letter -> callsign
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline
	MagneticGrid        MagneticGrid
	ARTCCs              map[string]ARTCC
	ERAMAdaptations     map[string]ERAMAdaptation
	TRACONs             map[string]TRACON
	MVAs                map[string][]MVA // TRACON -> MVAs
}

func (d StaticDatabase) LookupWaypoint(f string) (Point2LL, bool) {
	if n, ok := d.Navaids[f]; ok {
		return n.Location, true
	} else if f, ok := d.Fixes[f]; ok {
		return f.Location, true
	} else {
		return Point2LL{}, false
	}
}

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string  `json:"weightClass"`
	Ceiling     float32 `json:"ceiling"`
	Engine      struct {
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
}

type Airline struct {
	ICAO     string `json:"icao"`
	Name     string `json:"name"`
	Callsign struct {
		Name            string   `json:"name"`
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
	go func() { db.Airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftPerformance = parseAircraftPerformance(); wg.Done() }()
	wg.Add(1)
	go func() { db.Airlines, db.Callsigns = parseAirlines(); wg.Done() }()
	var airports map[string]FAAAirport
	wg.Add(1)
	go func() { airports, db.Navaids, db.Fixes = parseCIFP(); wg.Done() }()
	wg.Add(1)
	go func() { db.MagneticGrid = parseMagneticGrid(); wg.Done() }()
	wg.Add(1)
	go func() { db.ARTCCs, db.TRACONs = parseARTCCsAndTRACONs(); wg.Done() }()
	wg.Add(1)
	go func() { db.MVAs = parseMVAs(); wg.Done() }()
	wg.Add(1)
	go func() { db.ERAMAdaptations = parseAdaptations(); wg.Done() }()
	wg.Wait()

	for icao, ap := range airports {
		db.Airports[icao] = ap
	}

	//fmt.Printf("Parsed built-in databases in %v\n", time.Since(start))
	lg.Infof("Parsed built-in databases in %v", time.Since(start))

	return db
}

///////////////////////////////////////////////////////////////////////////
// FAA (and other) databases

// Utility function for parsing CSV files as strings; it breaks each line
// of the file into fields and calls the provided callback function for
// each one.
func mungeCSV(filename string, raw string, fields []string, callback func([]string)) {
	r := bytes.NewReader([]byte(raw))
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Find the index of each field the caller requested
	var fieldIndices []int
	if header, err := cr.Read(); err != nil {
		lg.Errorf("%s: error parsing CSV file: %s", filename, err)
	} else {
		for fi, f := range fields {
			for hi, h := range header {
				if f == strings.TrimSpace(h) {
					fieldIndices = append(fieldIndices, hi)
					break
				}
			}
			if len(fieldIndices) != fi+1 {
				lg.Error("did not find requested field header",
					slog.String("filename", filename),
					slog.String("field", f),
					slog.Any("header", header))
			}
		}
	}

	var strs []string
	for {
		if record, err := cr.Read(); err == io.EOF {
			return
		} else if err != nil {
			lg.Errorf("%s: error parsing CSV file: %s", filename, err)
			return
		} else {
			for _, i := range fieldIndices {
				strs = append(strs, record[i])
			}
			callback(strs)
			strs = strs[:0]
		}
	}
}

func parseAirports() map[string]FAAAirport {
	airports := make(map[string]FAAAirport)

	airportsRaw := LoadResource("airports.csv.zst") // https://ourairports.com/data/

	parse := func(s string) Point2LL {
		loc, err := ParseLatLong([]byte(s))
		if err != nil {
			panic(err)
		}
		return loc
	}

	// These aren't in the FAA database but we need to have them defined
	// for the AAC scenario...
	airports["4V4"] = FAAAirport{Id: "4V4", Name: "", Elevation: 623,
		Location: parse("N36.02.19.900,W95.28.49.512")}
	airports["4Y3"] = FAAAirport{Id: "4Y3", Name: "", Elevation: 624,
		Location: parse("N36.26.30.006,W95.36.21.936")}
	airports["KAAC"] = FAAAirport{Id: "KAAC", Name: "", Elevation: 677,
		Location: parse("N036.11.08.930,W095.45.53.942"),
		Runways: []Runway{
			Runway{Id: "28L", Heading: 280, Threshold: parse("N036.10.37.069,W095.44.51.979"), Elevation: 677},
			Runway{Id: "28R", Heading: 280, Threshold: parse("N036.11.23.280,W095.44.35.912"), Elevation: 677},
			Runway{Id: "10L", Heading: 280, Threshold: parse("N036.10.32.180,W095.44.24.843"), Elevation: 677},
			Runway{Id: "10R", Heading: 280, Threshold: parse("N036.11.19.188,W095.44.10.863"), Elevation: 677},
		}}
	airports["KBRT"] = FAAAirport{Id: "KBRT", Name: "", Elevation: 689,
		Location: parse("N36.30.26.585,W96.16.28.968")}
	airports["KJKE"] = FAAAirport{Id: "KJKE", Name: "", Elevation: 608,
		Location: parse("N035.56.19.765,W095.42.49.812"),
		Runways: []Runway{
			Runway{Id: "27", Heading: 270, Threshold: parse("N035.56.14.615,W095.42.05.152"), Elevation: 689},
			Runway{Id: "9", Heading: 270, Threshold: parse("N035.56.20.355,W095.41.35.791"), Elevation: 689},
		}}
	airports["Z91"] = FAAAirport{Id: "Z91", Name: "", Elevation: 680,
		Location: parse("N36.05.06.948,W96.26.57.501")}

	// FAA database
	mungeCSV("airports", string(airportsRaw),
		[]string{"latitude_deg", "longitude_deg", "elevation_ft", "gps_code", "name"},
		func(s []string) {
			elevation := float64(0)
			if s[2] != "" {
				elevation = atof(s[2])
			}
			loc := Point2LL{float32(atof(s[1])), float32(atof(s[0]))}
			ap := FAAAirport{Id: s[3], Name: s[4], Location: loc, Elevation: int(elevation)}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		})

	artccsRaw := LoadResource("airport_artccs.json")
	data := make(map[string]string) // Airport -> ARTCC
	json.Unmarshal(artccsRaw, &data)

	for name, artcc := range data {
		if entry, ok := airports[name]; ok {
			entry.ARTCC = artcc
			airports[name] = entry
		}
	}

	return airports
}

func parseAircraftPerformance() map[string]AircraftPerformance {
	openscopeAircraft := LoadResource("openscope-aircraft.json")

	var acStruct struct {
		Aircraft []AircraftPerformance `json:"aircraft"`
	}
	if err := json.Unmarshal(openscopeAircraft, &acStruct); err != nil {
		lg.Errorf("error in JSON unmarshal of openscope-aircraft: %v", err)
	}

	ap := make(map[string]AircraftPerformance)
	for _, ac := range acStruct.Aircraft {
		// If we have mach but not TAS, do the conversion; the nav code
		// works with TAS..
		if ac.Speed.CruiseMach != 0 && ac.Speed.CruiseTAS == 0 {
			ac.Speed.CruiseTAS = 666.739 * ac.Speed.CruiseMach
		}
		if ac.Speed.MaxMach != 0 && ac.Speed.MaxTAS == 0 {
			ac.Speed.MaxTAS = 666.739 * ac.Speed.MaxMach
		}

		ap[ac.ICAO] = ac

		if ac.Speed.V2 != 0 && ac.Speed.V2 > 1.5*ac.Speed.Min {
			lg.Errorf("%s: aircraft V2 %.0f seems suspiciously high (vs min %.01f)",
				ac.ICAO, ac.Speed.V2, ac.Speed.Min)
		}
	}

	return ap
}

func parseAirlines() (map[string]Airline, map[string]string) {
	openscopeAirlines := LoadResource("openscope-airlines.json")

	var alStruct struct {
		Airlines []Airline `json:"airlines"`
	}
	if err := json.Unmarshal([]byte(openscopeAirlines), &alStruct); err != nil {
		lg.Errorf("error in JSON unmarshal of openscope-airlines: %v", err)
	}

	airlines := make(map[string]Airline)
	callsigns := make(map[string]string)
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
		callsigns[strings.ToUpper(al.ICAO)] = al.Callsign.Name
	}
	return airlines, callsigns
}

// FAA Coded Instrument Flight Procedures (CIFP)
// https://www.faa.gov/air_traffic/flight_info/aeronav/digital_products/cifp/download/
func parseCIFP() (map[string]FAAAirport, map[string]Navaid, map[string]Fix) {
	cifp, err := fs.ReadFile(resourcesFS, "FAACIFP18.zst")
	if err != nil {
		panic(err)
	}

	return ParseARINC424(cifp)
}

type MagneticGrid struct {
	MinLatitude, MaxLatitude   float32
	MinLongitude, MaxLongitude float32
	LatLongStep                float32
	Samples                    []float32
}

func parseMagneticGrid() MagneticGrid {
	/*
		1. Download software and coefficients from https://www.ncei.noaa.gov/products/world-magnetic-model
		2. Build wmm_grid, run with the parameters in the MagneticGrid initializer below, year 2024,
		   altitude 0 -> 0, select "declination" for output.
		3. awk '{print $5}' < GridResults.txt | zstd -19 -o magnetic_grid.txt.zst
	*/
	mg := MagneticGrid{
		MinLatitude:  24,
		MaxLatitude:  50,
		MinLongitude: -125,
		MaxLongitude: -66,
		LatLongStep:  0.25,
	}

	samples := LoadResource("magnetic_grid.txt.zst")
	r := bufio.NewReader(bytes.NewReader(samples))

	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		if v, err := strconv.ParseFloat(strings.TrimSpace(line), 32); err != nil {
			panic(line + ": parsing error: " + err.Error())
		} else {
			mg.Samples = append(mg.Samples, float32(v))
		}
	}

	nlat := int(1 + (mg.MaxLatitude-mg.MinLatitude)/mg.LatLongStep)
	nlong := int(1 + (mg.MaxLongitude-mg.MinLongitude)/mg.LatLongStep)
	if len(mg.Samples) != nlat*nlong {
		panic(fmt.Sprintf("found %d magnetic grid samples, expected %d x %d = %d",
			len(mg.Samples), nlat, nlong, nlat*nlong))
	}

	return mg
}

func (mg *MagneticGrid) Lookup(p Point2LL) (float32, error) {
	if p[0] < mg.MinLongitude || p[0] > mg.MaxLongitude ||
		p[1] < mg.MinLatitude || p[1] > mg.MaxLatitude {
		return 0, fmt.Errorf("lookup point outside sampled grid")
	}

	nlat := int(1 + (mg.MaxLatitude-mg.MinLatitude)/mg.LatLongStep)
	nlong := int(1 + (mg.MaxLongitude-mg.MinLongitude)/mg.LatLongStep)

	// Round to nearest
	lat := min(int((p[1]-mg.MinLatitude)/mg.LatLongStep+0.5), nlat-1)
	long := min(int((p[0]-mg.MinLongitude)/mg.LatLongStep+0.5), nlong-1)

	// Note: we flip the sign
	return -mg.Samples[long+nlong*lat], nil
}

type MVA struct {
	MinimumLimit          int                      `xml:"minimumLimit"`
	MinimumLimitReference string                   `xml:"minimumLimitReference"`
	Proj                  *MVAHorizontalProjection `xml:"horizontalProjection"`
	ExteriorRing          [][2]float32
	InteriorRings         [][][2]float32
}

func (m *MVA) Inside(p [2]float32) bool {
	if !PointInPolygon(p, m.ExteriorRing) {
		return false
	}
	for _, in := range m.InteriorRings {
		if PointInPolygon(p, in) {
			return false
		}
	}
	return true
}

type MVALinearRing struct {
	PosList string `xml:"posList"`
}

func (r MVALinearRing) Vertices() [][2]float32 {
	var v [][2]float32
	f := strings.Fields(r.PosList)
	if len(f)%2 != 0 {
		panic("odd number of floats?")
	}

	for i := 0; i < len(f); i += 2 {
		v0, err := strconv.ParseFloat(f[i], 32)
		if err != nil {
			panic(err)
		}
		v1, err := strconv.ParseFloat(f[i+1], 32)
		if err != nil {
			panic(err)
		}
		v = append(v, [2]float32{float32(v0), float32(v1)})
	}

	return v
}

type MVAExterior struct {
	LinearRing MVALinearRing `xml:"LinearRing"`
}

type MVAInterior struct {
	LinearRing MVALinearRing `xml:"LinearRing"`
}

type MVAPolygonPatch struct {
	Exterior  MVAExterior   `xml:"exterior"`
	Interiors []MVAInterior `xml:"interior"`
}

type MVAPatches struct {
	PolygonPatch MVAPolygonPatch `xml:"PolygonPatch"`
}

type MVASurface struct {
	Patches MVAPatches `xml:"patches"`
}

type MVAHorizontalProjection struct {
	Surface MVASurface `xml:"Surface"`
}

// To update the MVA data:
// % go run util/scrapemva.go # download the XML files
// % parallel zstd -19 {} ::: *xml
// % zip mva-fus3.zip *FUS3_*zst
// % mv mva*zip ~/vice/resources/
// % /bin/rm MVA_*zst MVA_*xml

func parseMVAs() map[string][]MVA {
	// The MVA files are stored in a zip file to avoid the overhead of
	// opening lots of files to read them in.
	z := LoadResource("mva-fus3.zip")
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		panic(err)
	}

	type mvaTracon struct {
		TRACON string
		MVAs   []MVA
	}
	mvaChan := make(chan mvaTracon, len(zr.File))

	for _, f := range zr.File {
		// Launch a goroutine for each one so that we load them in
		// parallel.
		go func(f *zip.File) {
			r, err := f.Open()
			if err != nil {
				// Errors are panics since this all happens at startup time
				// with data that's fixed at release time.
				panic(err)
			}

			b, err := io.ReadAll(r)
			if err != nil {
				panic(err)
			}

			contents := []byte(decompressZstd(string(b)))
			decoder := xml.NewDecoder(bytes.NewReader(contents))

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

						mvas = append(mvas, m)
					}
				}
			}

			r.Close()

			mvaChan <- mvaTracon{TRACON: tracon, MVAs: mvas}
		}(f)
	}

	mvas := make(map[string][]MVA)
	for range zr.File {
		m := <-mvaChan
		mvas[m.TRACON] = m.MVAs
	}

	return mvas
}

func parseARTCCsAndTRACONs() (map[string]ARTCC, map[string]TRACON) {
	artccJSON := LoadResource("artccs.json")
	var artccs map[string]ARTCC
	if err := json.Unmarshal(artccJSON, &artccs); err != nil {
		panic(fmt.Sprintf("error unmarshalling ARTCCs: %v", err))
	}

	traconJSON := LoadResource("tracons.json")
	var tracons map[string]TRACON
	if err := json.Unmarshal(traconJSON, &tracons); err != nil {
		panic(fmt.Sprintf("error unmarshalling TRACONs: %v", err))
	}

	// Validate that all of the TRACON ARTCCs are known.
	for name, tracon := range tracons {
		if _, ok := artccs[tracon.ARTCC]; !ok {
			panic(tracon.ARTCC + ": ARTCC unknown for TRACON " + name)
		}
	}

	return artccs, tracons
}

///////////////////////////////////////////////////////////////////////////
// Utility methods

func (db *StaticDatabase) CheckAirline(icao, fleet string, e *ErrorLogger) {
	e.Push("Airline " + icao + ", fleet " + fleet)
	defer e.Pop()

	al, ok := database.Airlines[icao]
	if !ok {
		e.ErrorString("airline not known")
		return
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		e.ErrorString("fleet unknown")
		return
	}

	for _, aircraft := range fl {
		e.Push("Aircraft " + aircraft.ICAO)
		if perf, ok := database.AircraftPerformance[aircraft.ICAO]; !ok {
			e.ErrorString("aircraft not present in performance database")
		} else {
			if perf.Speed.Min < 35 || perf.Speed.Landing < 35 || perf.Speed.CruiseTAS < 35 ||
				perf.Speed.MaxTAS < 35 || perf.Speed.Min > perf.Speed.MaxTAS {
				e.ErrorString("aircraft's speed specification is questionable: %s", spew.Sdump(perf.Speed))
			}
			if perf.Rate.Climb == 0 || perf.Rate.Descent == 0 || perf.Rate.Accelerate == 0 ||
				perf.Rate.Decelerate == 0 {
				e.ErrorString("aircraft's rate specification is questionable: %s", spew.Sdump(perf.Rate))
			}
		}
		e.Pop()
	}
}

func parseAdaptations() map[string]ERAMAdaptation {
	adaptations := make(map[string]ERAMAdaptation)

	adaptationsRaw := LoadResource("adaptations.json")
	if err := json.Unmarshal(adaptationsRaw, &adaptations); err != nil {
		panic(err)
	}

	return adaptations
}

func FixReadback(fix string) string {
	if aid, ok := database.Navaids[fix]; ok {
		return stopShouting(aid.Name)
	} else {
		return fix
	}
}

func cleanRunway(rwy string) string {
	// The runway may have extra text to distinguish different
	// configurations (e.g., "13.JFK-ILS-13"). Find the prefix that is
	// an actual runway specifier to use in the search below.
	for i, ch := range rwy {
		if ch >= '0' && ch <= '9' {
			continue
		} else if ch == 'L' || ch == 'R' || ch == 'C' {
			return rwy[:i+1]
		} else {
			return rwy[:i]
		}
	}
	return rwy
}

func LookupRunway(icao, rwy string) (Runway, bool) {
	if ap, ok := database.Airports[icao]; !ok {
		return Runway{}, false
	} else {
		rwy = cleanRunway(rwy)
		idx := slices.IndexFunc(ap.Runways, func(r Runway) bool { return r.Id == rwy })
		if idx == -1 {
			return Runway{}, false
		}
		return ap.Runways[idx], true
	}
}

func LookupOppositeRunway(icao, rwy string) (Runway, bool) {
	if ap, ok := database.Airports[icao]; !ok {
		return Runway{}, false
	} else {
		rwy = cleanRunway(rwy)

		// Break runway into number and optional extension and swap
		// left/right.
		n := len(rwy)
		num, ext := "", ""
		switch rwy[n-1] {
		case 'R':
			ext = "L"
			num = rwy[:n-1]
		case 'L':
			ext = "R"
			num = rwy[:n-1]
		case 'C':
			ext = "C"
			num = rwy[:n-1]
		default:
			num = rwy
		}

		// Extract the number so we can get the opposite heading
		v, err := strconv.Atoi(num)
		if err != nil {
			return Runway{}, false
		}

		// The (v+18)%36 below would give us 0 for runway 36, so handle 18
		// specially.
		if v == 18 {
			rwy = "36" + ext
		} else {
			rwy = fmt.Sprintf("%d", (v+18)%36) + ext
		}

		idx := slices.IndexFunc(ap.Runways, func(r Runway) bool { return r.Id == rwy })
		if idx == -1 {
			return Runway{}, false
		}
		return ap.Runways[idx], true
	}
}

func (ap FAAAirport) ValidRunways() string {
	return strings.Join(MapSlice(ap.Runways, func(r Runway) string { return r.Id }), ", ")
}

// returns the ratio of air density at the given altitude (in feet) to the
// air density at sea level, subject to assuming the standard atmosphere.
func DensityRatioAtAltitude(alt float32) float32 {
	altm := alt * 0.3048 // altitude in meters

	// https://en.wikipedia.org/wiki/Barometric_formula#Density_equations
	const g0 = 9.80665    // gravitational constant, m/s^2
	const M_air = 0.02897 // molar mass of earth's air, kg/mol
	const R = 8.314463    // universal gas constant J/(mol K)
	const T_b = 288.15    // reference temperature at sea level, degrees K

	return exp(-g0 * M_air * altm / (R * T_b))
}

func IASToTAS(ias, altitude float32) float32 {
	return ias / sqrt(DensityRatioAtAltitude(altitude))
}

func TASToIAS(tas, altitude float32) float32 {
	return tas * sqrt(DensityRatioAtAltitude(altitude))
}

///////////////////////////////////////////////////////////////////////////
// Arrival

func (ar *Arrival) PostDeserialize(sg *ScenarioGroup, e *ErrorLogger) {
	if ar.Route == "" && ar.STAR == "" {
		e.ErrorString("neither \"route\" nor \"star\" specified")
		return
	}

	if ar.Route != "" {
		e.Push("Route " + ar.Route)
	} else {
		e.Push("Route " + ar.STAR)
	}
	defer e.Pop()

	if len(ar.Waypoints) == 0 {
		// STAR details are coming from the FAA CIFP; make sure
		// everything is ok so we don't get into trouble when we
		// spawn arrivals...
		if ar.STAR == "" {
			e.ErrorString("must provide \"star\" if \"waypoints\" aren't given")
			return
		}
		if ar.SpawnWaypoint == "" {
			e.ErrorString("must specify \"spawn\" if \"waypoints\" aren't given with arrival")
			return
		}

		spawnPoint, spawnTString, ok := strings.Cut(ar.SpawnWaypoint, "@")
		spawnT := float32(0)
		if ok {
			if st, err := strconv.ParseFloat(spawnTString, 32); err != nil {
				e.ErrorString("error parsing spawn offset \"%s\": %s", spawnTString, err)
			} else {
				spawnT = float32(st)
			}
		}

		for icao := range ar.Airlines {
			airport, ok := database.Airports[icao]
			if !ok {
				e.ErrorString("airport \"%s\" not found in database", icao)
				continue
			}

			star, ok := airport.STARs[ar.STAR]
			if !ok {
				e.ErrorString("STAR \"%s\" not available for %s. Options: %s",
					ar.STAR, icao, strings.Join(SortedMapKeys(airport.STARs), ", "))
				continue
			}

			star.Check(e)

			if len(ar.Waypoints) == 0 {
				for _, tr := range SortedMapKeys(star.Transitions) {
					wps := star.Transitions[tr]
					if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == spawnPoint }); idx != -1 {
						if idx == len(wps)-1 {
							e.ErrorString("Only have one waypoint on STAR: \"%s\". 2 or more are necessary for navigation",
								wps[idx].Fix)
						}

						ar.Waypoints = DuplicateSlice(wps[idx:])
						sg.InitializeWaypointLocations(ar.Waypoints, e)

						if len(ar.Waypoints) >= 2 && spawnT != 0 {
							ar.Waypoints[0].Location = lerp2f(spawnT, ar.Waypoints[0].Location, ar.Waypoints[1].Location)
							ar.Waypoints[0].Fix = "_" + ar.Waypoints[0].Fix
						}

						break
					}
				}
			}

			if star.RunwayWaypoints != nil {
				if ar.RunwayWaypoints == nil {
					ar.RunwayWaypoints = make(map[string]map[string]WaypointArray)
				}
				if ar.RunwayWaypoints[icao] == nil {
					ar.RunwayWaypoints[icao] = make(map[string]WaypointArray)
				}

				for _, rwy := range airport.Runways {
					for starRwy, wp := range star.RunwayWaypoints {
						// Trim leading 0, if any
						if starRwy[0] == '0' {
							starRwy = starRwy[1:]
						}

						n := len(starRwy)
						if starRwy == rwy.Id ||
							(n == len(rwy.Id) && starRwy[n-1] == 'B' /* both */ && starRwy[:n-1] == rwy.Id[:n-1]) {
							ar.RunwayWaypoints[icao][rwy.Id] = DuplicateSlice(wp)
							sg.InitializeWaypointLocations(ar.RunwayWaypoints[icao][rwy.Id], e)
							break
						}
					}
				}
			}
		}
		switch len(ar.Waypoints) {
		case 0:
			e.ErrorString("Couldn't find waypoint %s in any of the STAR routes", spawnPoint)
			return

		case 1:
			ar.Waypoints[0].Handoff = true

		default:
			// add a handoff point randomly halfway between the first two waypoints.
			mid := Waypoint{
				Fix: "_handoff",
				// FIXME: it's a little sketchy to lerp Point2ll coordinates
				// but probably ok over short distances here...
				Location: lerp2f(0.5, ar.Waypoints[0].Location, ar.Waypoints[1].Location),
				Handoff:  true,
			}
			ar.Waypoints = append([]Waypoint{ar.Waypoints[0], mid}, ar.Waypoints[1:]...)
		}
	} else {
		if len(ar.Waypoints) < 2 {
			e.ErrorString("must provide at least two \"waypoints\" for arrival " +
				"(even if \"runway_waypoints\" are provided)")
		}

		sg.InitializeWaypointLocations(ar.Waypoints, e)

		for ap, rwywp := range ar.RunwayWaypoints {
			e.Push("Airport " + ap)

			if _, ok := database.Airports[ap]; !ok {
				e.ErrorString("airport is unknown")
				continue
			}

			for rwy, wp := range rwywp {
				e.Push("Runway " + rwy)

				if _, ok := LookupRunway(ap, rwy); !ok {
					e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy, database.Airports[ap].ValidRunways())
				}

				sg.InitializeWaypointLocations(wp, e)

				if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
					e.ErrorString("initial \"runway_waypoints\" fix must match " +
						"last \"waypoints\" fix")
				}

				// For the check, splice together the last common
				// waypoint and the runway waypoints.  This will give
				// us a repeated first fix, but this way we can check
				// compliance with restrictions at that fix...
				ewp := append([]Waypoint{ar.Waypoints[len(ar.Waypoints)-1]}, wp...)
				WaypointArray(ewp).CheckArrival(e)

				e.Pop()
			}
			e.Pop()
		}
	}

	ar.Waypoints.CheckArrival(e)

	for arrivalAirport, airlines := range ar.Airlines {
		e.Push("Arrival airport " + arrivalAirport)
		if len(airlines) == 0 {
			e.ErrorString("no \"airlines\" specified for arrivals to " + arrivalAirport)
		}
		for _, al := range airlines {
			database.CheckAirline(al.ICAO, al.Fleet, e)
			if _, ok := database.Airports[al.Airport]; !ok {
				e.ErrorString("departure airport \"airport\" \"%s\" unknown", al.Airport)
			}
		}

		ap, ok := sg.Airports[arrivalAirport]
		if !ok {
			e.ErrorString("arrival airport \"%s\" unknown", arrivalAirport)
		} else if ar.ExpectApproach != "" {
			if _, ok := ap.Approaches[ar.ExpectApproach]; !ok {
				e.ErrorString("arrival airport \"%s\" doesn't have a \"%s\" approach",
					arrivalAirport, ar.ExpectApproach)
			}
		}

		e.Pop()
	}

	if ar.InitialAltitude == 0 {
		e.ErrorString("must specify \"initial_altitude\"")
	} else {
		// Make sure the initial altitude isn't below any of
		// altitude restrictions.
		for _, wp := range ar.Waypoints {
			if wp.AltitudeRestriction != nil &&
				wp.AltitudeRestriction.TargetAltitude(ar.InitialAltitude) > ar.InitialAltitude {
				e.ErrorString("\"initial_altitude\" is below altitude restriction at \"%s\"", wp.Fix)
			}
		}
	}

	if ar.InitialSpeed == 0 {
		e.ErrorString("must specify \"initial_speed\"")
	}

	if ar.InitialController == "" {
		e.ErrorString("\"initial_controller\" missing")
	} else if _, ok := sg.ControlPositions[ar.InitialController]; !ok {
		e.ErrorString("controller \"%s\" not found for \"initial_controller\"", ar.InitialController)
	}

	for _, controller := range sg.ControlPositions {
		if controller.ERAMFacility && controller.FacilityIdentifier == "" {
			e.ErrorString(fmt.Sprintf("%v is an ERAM facility, but has no facility id specified", controller.Callsign))
		}
	}
}

func (a Arrival) GetRunwayWaypoints(airport, rwy string) WaypointArray {
	if ap, ok := a.RunwayWaypoints[airport]; !ok {
		return nil
	} else if wp, ok := ap[rwy]; !ok {
		return nil
	} else {
		return wp
	}
}

///////////////////////////////////////////////////////////////////////////

// VideoMapLibrary maintains a collection of video maps loaded from multiple
// files.  Video maps are loaded asynchronously.
type VideoMapLibrary struct {
	manifests map[string]map[string]interface{} // filename -> map name
	maps      map[string]map[string]*STARSMap
	ch        chan LoadedVideoMap
	loading   map[string]interface{}
}

type LoadedVideoMap struct {
	path string
	maps map[string]*STARSMap
}

func MakeVideoMapLibrary() *VideoMapLibrary {
	return &VideoMapLibrary{
		manifests: make(map[string]map[string]interface{}),
		maps:      make(map[string]map[string]*STARSMap),
		ch:        make(chan LoadedVideoMap, 64),
		loading:   make(map[string]interface{}),
	}
}

// AddFile adds a video map to the library. referenced encodes which maps
// in the file are actually used; the loading code uses this information to
// skip the work of generating CommandBuffers for unused video maps.
func (ml *VideoMapLibrary) AddFile(filesystem fs.FS, filename string, referenced map[string]interface{}, e *ErrorLogger) {
	// Load the manifest and do initial error checking
	mf, _ := strings.CutSuffix(filename, ".zst")
	mf, _ = strings.CutSuffix(mf, "-videomaps.gob")
	mf += "-manifest.gob"

	fm, err := filesystem.Open(mf)
	if err != nil {
		e.Error(err)
		return
	}
	defer fm.Close()

	var manifest map[string]interface{} // the manifest file doesn't include the lines so is fast to parse...
	dec := gob.NewDecoder(fm)
	if err := dec.Decode(&manifest); err != nil {
		e.Error(err)
		return
	}
	ml.manifests[filename] = manifest

	for name := range referenced {
		if name != "" {
			if _, ok := manifest[name]; !ok {
				e.Error(fmt.Errorf("%s: video map \"%s\" in \"stars_maps\" not found", filename, name))
			}
		}
	}

	// Kick off the work to load the actual video map.
	f, err := filesystem.Open(filename)
	if err != nil {
		e.Error(err)
	} else {
		ml.loading[filename] = nil
		go ml.loadVideoMap(f, filename, referenced, manifest)
	}
}

// loadVideoMap handles loading the given map; it runs asynchronously and
// returns the result via the ml.ch chan.
func (ml *VideoMapLibrary) loadVideoMap(f io.ReadCloser, filename string, referenced map[string]interface{},
	manifest map[string]interface{}) {
	defer f.Close()

	r := io.Reader(f)
	if strings.HasSuffix(strings.ToLower(filename), ".zst") {
		zr, _ := zstd.NewReader(r, zstd.WithDecoderConcurrency(0))
		defer zr.Close()
		r = zr
	}

	// Initial decoding of the gob file.
	var maps []STARSMap
	dec := gob.NewDecoder(r)
	if err := dec.Decode(&maps); err != nil {
		panic(fmt.Sprintf("%s: %v", filename, err))
	}

	// We'll return the maps via a map from the map name to the associated
	// *STARSMap.
	starsMaps := make(map[string]*STARSMap)
	for _, sm := range maps {
		if sm.Name == "" {
			continue
		}

		if _, ok := referenced[sm.Name]; ok {
			if _, ok := manifest[sm.Name]; !ok {
				panic(fmt.Sprintf("%s: map \"%s\" not found in manifest file", filename, sm.Name))
			}

			ld := GetLinesDrawBuilder()
			for _, lines := range sm.Lines {
				// Slightly annoying: the line vertices are stored with
				// Point2LLs but AddLineStrip() expects [2]float32s.
				fl := MapSlice(lines, func(p Point2LL) [2]float32 { return p })
				ld.AddLineStrip(fl)
			}
			ld.GenerateCommands(&sm.CommandBuffer)
		}

		// Clear out Lines so that the memory can be reclaimed since they
		// aren't needed any more.
		sm.Lines = nil
		starsMaps[sm.Name] = &sm
	}

	ml.ch <- LoadedVideoMap{path: filename, maps: starsMaps}
}

func (ml *VideoMapLibrary) GetMap(filename, mapname string) *STARSMap {
	// First harvest any video map files that have been loaded. Keep going
	// as long as there are more waiting, but don't stall if there aren't
	// any.
	stop := false
	for !stop {
		select {
		case m := <-ml.ch:
			delete(ml.loading, m.path)
			ml.maps[m.path] = m.maps

		default:
			stop = true
		}
	}

	if fmaps, ok := ml.maps[filename]; ok {
		return fmaps[mapname]
	} else {
		// The map file hasn't been loaded, so we'll need to stall and wait
		// for it.
		if _, ok := ml.loading[filename]; !ok {
			lg.Errorf("%s: video map \"%s\" requested from file that isn't being loaded",
				filename, mapname)
			return nil
		}

		lg.Infof("%s/%s: blocking waiting for video map", filename, mapname)
		for {
			// Blocking channel receive here
			m := <-ml.ch
			delete(ml.loading, m.path)
			ml.maps[m.path] = m.maps

			if m.path == filename {
				lg.Infof("%s: finished loading video map file", filename)
				return m.maps[mapname]
			}
		}
	}
}

func (ml VideoMapLibrary) HaveFile(filename string) bool {
	// This can be determined strictly from the manifests and so there's no
	// need to block.
	_, ok := ml.manifests[filename]
	return ok
}

func (ml VideoMapLibrary) AvailableFiles() []string {
	return MapSlice(SortedMapKeys(ml.manifests),
		func(s string) string {
			s = strings.TrimPrefix(s, "videomaps/")
			s, _, _ = strings.Cut(s, "-")
			return s
		})
}

func (ml VideoMapLibrary) AvailableMaps(filename string) []string {
	if mf, ok := ml.manifests[filename]; !ok {
		return nil
	} else {
		return SortedMapKeys(mf)
	}
}

func (ml VideoMapLibrary) HaveMap(filename, mapname string) bool {
	mf, ok := ml.manifests[filename]
	if ok {
		_, ok = mf[mapname]
	}
	return ok
}

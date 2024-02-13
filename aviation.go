// aviation.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"golang.org/x/exp/slog"
)

type FAAAirport struct {
	Id        string
	Name      string
	Elevation int
	Location  Point2LL
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
	Callsign  string    // Not provided in scenario JSON
	FullName  string    `json:"full_name"`
	Frequency Frequency `json:"frequency"`
	SectorId  string    `json:"sector_id"`  // e.g. N56, 2J, ...
	Scope     string    `json:"scope_char"` // For tracked a/c on the scope--e.g., T
	IsHuman   bool      // Not provided in scenario JSON
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

func NewFlightPlan(r FlightRules, ac, dep, arr string) *FlightPlan {
	return &FlightPlan{
		Rules:            r,
		AircraftType:     ac,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
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
	} else if nmdistance2ll(pDep, pArr) < 200 {
		altitude = 11000
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
	ExitAltitude float32 `json:",omitempty"`
	MinuteLimit  int     `json:",omitempty"`
	NmLimit      int     `json:",omitempty"`
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
				s += fmt.Sprintf("%dmin", pt.MinuteLimit)
			} else {
				s += fmt.Sprintf("%dnm", pt.NmLimit)
			}
			if pt.Entry180NoPT {
				s += "/nopt180"
			}
			if pt.ExitAltitude != 0 {
				s += fmt.Sprintf("/pta%0f", pt.ExitAltitude)
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
				s += fmt.Sprintf("/arc%f%s", w.Arc.Radius, w.Arc.Fix)
			} else {
				s += fmt.Sprintf("/arc%f", w.Arc.Length)
			}
		}

		entries = append(entries, s)

	}

	return strings.Join(entries, " ")
}

func (w *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) > 2 && b[0] == '"' && b[len(b)-1] == '"' {
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

	if len(w) < 3 {
		e.ErrorString("must have at least three waypoints in an approach")
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
	if extent[len(extent)-2:] == "nm" {
		if pt.NmLimit, err = strconv.Atoi(extent[:len(extent)-2]); err != nil {
			return fmt.Errorf("%s: unable to parse length in nm for procedure turn: %v", extent, err)
		}
	} else if extent[len(extent)-3:] == "min" {
		if pt.MinuteLimit, err = strconv.Atoi(extent[:len(extent)-3]); err != nil {
			return fmt.Errorf("%s: unable to parse minutes procedure turn: %v", extent, err)
		}
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
						wp.ProcedureTurn.ExitAltitude = float32(alt)
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
		return PointInPolygon(p, a.Vertices)
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
		ld.AddPolyline([2]float32{}, v)
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
}

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string  `json:"weightClass"`
	Ceiling     float32 `json:"ceiling"`
	Rate        struct {
		Climb      float32 `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    float32 `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min     float32 `json:"min"`
		V2      float32 `json:"v2"`
		Landing float32 `json:"landing"`
		Cruise  float32 `json:"cruise"`
		Max     float32 `json:"max"`
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
	go func() { db.Navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { db.Airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { db.Fixes = parseFixes(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftPerformance = parseAircraftPerformance(); wg.Done() }()
	wg.Add(1)
	go func() { db.Airlines, db.Callsigns = parseAirlines(); wg.Done() }()
	wg.Wait()

	lg.Infof("Parsed built-in databases in %v", time.Since(start))

	return db
}

///////////////////////////////////////////////////////////////////////////
// FAA databases

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

func parseNavaids() map[string]Navaid {
	navaids := make(map[string]Navaid)

	// https://www.faa.gov/air_traffic/flight_info/aeronav/aero_data/NASR_Subscription_2023-09-07/
	navBaseRaw := LoadResource("NAV_BASE.csv.zst")
	mungeCSV("navaids", string(navBaseRaw),
		[]string{"NAV_ID", "NAV_TYPE", "NAME", "LONG_DECIMAL", "LAT_DECIMAL"},
		func(s []string) {
			n := Navaid{
				Id:       s[0],
				Type:     s[1],
				Name:     s[2],
				Location: Point2LL{float32(atof(s[3])), float32(atof(s[4]))},
			}
			if n.Id != "" {
				navaids[n.Id] = n
			}
		})

	return navaids
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
		Location: parse("N036.11.08.930,W095.45.53.942")}
	airports["KBRT"] = FAAAirport{Id: "KBRT", Name: "", Elevation: 689,
		Location: parse("N36.30.26.585,W96.16.28.968")}
	airports["KJKE"] = FAAAirport{Id: "KJKE", Name: "", Elevation: 608,
		Location: parse("N035.56.19.765,W095.42.49.812")}
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

	return airports
}

func parseFixes() map[string]Fix {
	fixes := make(map[string]Fix)

	fixesRaw := LoadResource("FIX_BASE.csv.zst")

	mungeCSV("fixes", string(fixesRaw),
		[]string{"FIX_ID", "LONG_DECIMAL", "LAT_DECIMAL"},
		func(s []string) {
			f := Fix{
				Id:       s[0],
				Location: Point2LL{float32(atof(s[1])), float32(atof(s[2]))},
			}
			if f.Id != "" {
				fixes[f.Id] = f
			}
		})

	return fixes
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
			if perf.Speed.Min < 35 || perf.Speed.Landing < 35 || perf.Speed.Cruise < 35 ||
				perf.Speed.Max < 35 || perf.Speed.Min > perf.Speed.Max {
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

func FixReadback(fix string) string {
	if aid, ok := database.Navaids[fix]; ok {
		return stopShouting(aid.Name)
	} else {
		return fix
	}
}

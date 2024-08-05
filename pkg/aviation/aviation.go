// pkg/aviation/aviation.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/klauspost/compress/zstd"
)

type ReportingPoint struct {
	Fix      string
	Location math.Point2LL
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

func (s STAR) Check(e *util.ErrorLogger) {
	check := func(wps WaypointArray) {
		for _, wp := range wps {
			_, okn := DB.Navaids[wp.Fix]
			_, okf := DB.Fixes[wp.Fix]
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
	for _, tr := range util.SortedMapKeys(s.Transitions) {
		wps := s.Transitions[tr]
		if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == fix }); idx != -1 {
			return wps[idx:]
		}
	}
	for _, tr := range util.SortedMapKeys(s.RunwayWaypoints) {
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
	Threshold math.Point2LL
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
		//lg.Warnf("Expecting RMK where %s is in METAR \"%s\"", s, str)
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
	Callsign       string
	Rules          FlightRules
	AircraftType   string
	CruiseSpeed    int
	AssignedSquawk Squawk // from ATC
	// An ECID (CID) are three alpha-numeric characters (eg. 971, 43A,
	// etc.) and is what ERAM assigns to a track to act as another way to
	// identify that track. To execute commands, controllers may use the
	// ECID instead of the aircrafts callsign.
	ECID                   string
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
	Position    math.Point2LL
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
	GetWindVector(p math.Point2LL, alt float32) math.Point2LL
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
		return math.Clamp(alt, a.Range[0], a.Range[1])
	} else {
		return math.Max(alt, a.Range[0])
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
	return [2]float32{math.Clamp(r[0], a0, a1), math.Clamp(r[1], a0, a1)}, ok
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

///////////////////////////////////////////////////////////////////////////
// DMEArc

// Can either be specified with (Fix,Radius), or (Length,Clockwise); the
// remaining fields are then derived from those.
type DMEArc struct {
	Fix            string
	Center         math.Point2LL
	Radius         float32
	Length         float32
	InitialHeading float32
	Clockwise      bool
}

///////////////////////////////////////////////////////////////////////////
// Waypoint

type Waypoint struct {
	Fix                 string               `json:"fix"`
	Location            math.Point2LL        // not provided in scenario JSON; derived from fix
	AltitudeRestriction *AltitudeRestriction `json:"altitude_restriction,omitempty"`
	Speed               int                  `json:"speed,omitempty"`
	Heading             int                  `json:"heading,omitempty"` // outbound heading after waypoint
	ProcedureTurn       *ProcedureTurn       `json:"pt,omitempty"`
	NoPT                bool                 `json:"nopt,omitempty"`
	Handoff             bool                 `json:"handoff,omitempty"`
	PointOut            string               `json:"pointout,omitempty"`
	FlyOver             bool                 `json:"flyover,omitempty"`
	Delete              bool                 `json:"delete,omitempty"`
	Arc                 *DMEArc              `json:"arc,omitempty"`
	IAF, IF, FAF        bool                 // not provided in scenario JSON; derived from fix
	Airway              string               // when parsing waypoints, this is set if we're on an airway after the fix
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
	if wp.PointOut != "" {
		attrs = append(attrs, slog.String("pointout", wp.PointOut))
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
	if wp.Airway != "" {
		attrs = append(attrs, slog.String("airway", wp.Airway))
	}

	return slog.GroupValue(attrs...)
}

func (wp *Waypoint) ETA(p math.Point2LL, gs float32) time.Duration {
	dist := math.NMDistance2LL(p, wp.Location)
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
		if w.PointOut != "" {
			s += "/po" + w.PointOut
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
		if w.Airway != "" {
			s += "/airway" + w.Airway
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

func (w WaypointArray) RouteString() string {
	var r []string
	airway := ""
	for _, wp := range w {
		if airway != "" && wp.Airway == airway {
			// This fix was automatically added for an airway so don't include it here.
			continue
		}
		r = append(r, wp.Fix)

		if wp.Airway != airway {
			if wp.Airway != "" {
				r = append(r, wp.Airway)
			}
			airway = wp.Airway
		}
	}
	return strings.Join(r, " ")
}

func (w WaypointArray) CheckDeparture(e *util.ErrorLogger, controllers map[string]*Controller) {
	w.checkBasics(e, controllers)

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

func (w WaypointArray) checkBasics(e *util.ErrorLogger, controllers map[string]*Controller) {
	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.Speed < 0 || wp.Speed > 300 {
			e.ErrorString("invalid speed restriction %d", wp.Speed)
		}

		if wp.PointOut != "" {
			if !util.MapContains(controllers,
				func(callsign string, ctrl *Controller) bool { return ctrl.SectorId == wp.PointOut }) {
				e.ErrorString("No controller found with TCP id \"%s\" for point out", wp.PointOut)
			}
		}
		e.Pop()
	}
}

func (w WaypointArray) CheckApproach(e *util.ErrorLogger, controllers map[string]*Controller) {
	w.checkBasics(e, controllers)
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

func (w WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[string]*Controller) {
	w.checkBasics(e, ctrl)
	w.checkDescending(e)

	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		e.Pop()
	}
}

func (w WaypointArray) CheckOverflight(e *util.ErrorLogger, ctrl map[string]*Controller) {
	w.checkBasics(e, ctrl)
}

func (w WaypointArray) checkDescending(e *util.ErrorLogger) {
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
	entries := strings.Fields(str)
	for ei, field := range entries {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: \"%s\"", str)
		}

		components := strings.Split(field, "/")

		// Is it an airway?
		if _, ok := DB.Airways[components[0]]; ok {
			if ei == 0 {
				return nil, fmt.Errorf("%s: can't begin a route with an airway", components[0])
			} else if ei == len(entries)-1 {
				return nil, fmt.Errorf("%s: can't end a route with an airway", components[0])
			} else if len(components) > 1 {
				return nil, fmt.Errorf("%s: can't have fix modifiers with an airway", field)
			} else {
				// Just set the Airway field for now; we'll patch up the
				// waypoints to include the airway waypoints at the end of
				// this function.
				nwp := len(waypoints)
				waypoints[nwp-1].Airway = components[0]
				continue
			}
		}

		wp := Waypoint{}
		for i, f := range components {
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
				} else if len(f) > 2 && f[:2] == "po" {
					wp.PointOut = f[2:]
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
				} else if len(f) >= 7 && f[:6] == "airway" {
					wp.Airway = f[6:]

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

	// Now go through and expand out any airways into their constituent waypoints
	var wpExpanded []Waypoint
	for i, wp := range waypoints {
		wpExpanded = append(wpExpanded, wp)

		if wp.Airway != "" {
			found := false
			wp0, wp1 := wp.Fix, waypoints[i+1].Fix
			for _, airway := range DB.Airways[wp.Airway] {
				if awp, ok := airway.WaypointsBetween(wp0, wp1); ok {
					wpExpanded = append(wpExpanded, awp...)
					found = true
					break
				}
			}

			if !found {
				return nil, fmt.Errorf("%s: unable to find fix pair %s - %s in airway", wp.Airway, wp0, wp1)
			}
		}
	}

	return wpExpanded, nil
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

type AirwayLevel int

const (
	AirwayLevelAll = iota
	AirwayLevelLow
	AirwayLevelHigh
)

type AirwayDirection int

const (
	AirwayDirectionAny = iota
	AirwayDirectionForward
	AirwayDirectionBackward
)

type AirwayFix struct {
	Fix       string
	Level     AirwayLevel
	Direction AirwayDirection
}

type Airway struct {
	Name  string
	Fixes []AirwayFix
}

func (a Airway) WaypointsBetween(wp0, wp1 string) ([]Waypoint, bool) {
	start := slices.IndexFunc(a.Fixes, func(f AirwayFix) bool { return f.Fix == wp0 })
	end := slices.IndexFunc(a.Fixes, func(f AirwayFix) bool { return f.Fix == wp1 })
	if start == -1 || end == -1 {
		return nil, false
	}

	var wps []Waypoint
	delta := util.Select(start < end, 1, -1)
	// Index so that we return waypoints exclusive of wp0 and wp1
	for i := start + delta; i != end; i += delta {
		wps = append(wps, Waypoint{
			Fix:    a.Fixes[i].Fix,
			Airway: a.Name, // maintain the identity that we're on an airway
		})
	}
	return wps, true
}

///////////////////////////////////////////////////////////////////////////

type RadarSite struct {
	Char           string        `json:"char"`
	PositionString string        `json:"position"`
	Position       math.Point2LL // not in JSON, set during deserialize

	Elevation      int32   `json:"elevation"`
	PrimaryRange   int32   `json:"primary_range"`
	SecondaryRange int32   `json:"secondary_range"`
	SlopeAngle     float32 `json:"slope_angle"`
	SilenceAngle   float32 `json:"silence_angle"`
}

func (rs *RadarSite) CheckVisibility(p math.Point2LL, altitude int) (primary, secondary bool, distance float32) {
	// Check altitude first; this is a quick first cull that
	// e.g. takes care of everyone on the ground.
	if altitude <= int(rs.Elevation) {
		return
	}

	// Time to check the angles..
	palt := float32(altitude) * math.FeetToNauticalMiles
	ralt := float32(rs.Elevation) * math.FeetToNauticalMiles
	dalt := palt - ralt
	// not quite true distance, but close enough
	distance = math.NMDistance2LL(rs.Position, p) + math.Abs(palt-ralt)

	// If we normalize the vector from the radar site to the aircraft, then
	// the z (altitude) component gives the cosine of the angle with the
	// "up" direction; in turn, we can check that against the two angles.
	cosAngle := dalt / distance
	// if angle < silence angle, we can't see it, but the test flips since
	// we're testing cosines.
	// FIXME: it's annoying to be repeatedly computing these cosines here...
	if cosAngle > math.Cos(math.Radians(rs.SilenceAngle)) {
		// inside the cone of silence
		return
	}
	// similarly, if angle > 90-slope angle, we can't see it, but again the
	// test flips.
	if cosAngle < math.Cos(math.Radians(90-rs.SlopeAngle)) {
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
	Vertices []math.Point2LL `json:"vertices"`
	// Circle
	Center math.Point2LL `json:"center"`
	Radius float32       `json:"radius"`
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

func (a *AirspaceVolume) Inside(p math.Point2LL, alt int) bool {
	if alt <= a.Floor || alt > a.Ceiling {
		return false
	}

	switch a.Type {
	case AirspaceVolumePolygon:
		return math.PointInPolygon2LL(p, a.Vertices)
	case AirspaceVolumeCircle:
		return math.NMDistance2LL(p, a.Center) < a.Radius
	default:
		panic("unhandled AirspaceVolume type")
	}
}

func (a *AirspaceVolume) GenerateDrawCommands(cb *renderer.CommandBuffer, nmPerLongitude float32) {
	ld := renderer.GetLinesDrawBuilder()

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
		panic("unhandled AirspaceVolume type")
	}

	ld.GenerateCommands(cb)
	renderer.ReturnLinesDrawBuilder(ld)
}

func FixReadback(fix string) string {
	if aid, ok := DB.Navaids[fix]; ok {
		return util.StopShouting(aid.Name)
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
	if ap, ok := DB.Airports[icao]; !ok {
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
	if ap, ok := DB.Airports[icao]; !ok {
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

// returns the ratio of air density at the given altitude (in feet) to the
// air density at sea level, subject to assuming the standard atmosphere.
func DensityRatioAtAltitude(alt float32) float32 {
	altm := alt * 0.3048 // altitude in meters

	// https://en.wikipedia.org/wiki/Barometric_formula#Density_equations
	const g0 = 9.80665    // gravitational constant, m/s^2
	const M_air = 0.02897 // molar mass of earth's air, kg/mol
	const R = 8.314463    // universal gas constant J/(mol K)
	const T_b = 288.15    // reference temperature at sea level, degrees K

	return math.Exp(-g0 * M_air * altm / (R * T_b))
}

func IASToTAS(ias, altitude float32) float32 {
	return ias / math.Sqrt(DensityRatioAtAltitude(altitude))
}

func TASToIAS(tas, altitude float32) float32 {
	return tas * math.Sqrt(DensityRatioAtAltitude(altitude))
}

///////////////////////////////////////////////////////////////////////////
// Arrival

func (ar *Arrival) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[string]*Airport, controlPositions map[string]*Controller, e *util.ErrorLogger) {
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
			airport, ok := DB.Airports[icao]
			if !ok {
				e.ErrorString("airport \"%s\" not found in database", icao)
				continue
			}

			star, ok := airport.STARs[ar.STAR]
			if !ok {
				e.ErrorString("STAR \"%s\" not available for %s. Options: %s",
					ar.STAR, icao, strings.Join(util.SortedMapKeys(airport.STARs), ", "))
				continue
			}

			star.Check(e)

			if len(ar.Waypoints) == 0 {
				for _, tr := range util.SortedMapKeys(star.Transitions) {
					wps := star.Transitions[tr]
					if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == spawnPoint }); idx != -1 {
						if idx == len(wps)-1 {
							e.ErrorString("Only have one waypoint on STAR: \"%s\". 2 or more are necessary for navigation",
								wps[idx].Fix)
						}

						ar.Waypoints = util.DuplicateSlice(wps[idx:])
						initializeWaypointLocations(ar.Waypoints, loc, nmPerLongitude, magneticVariation, e)

						if len(ar.Waypoints) >= 2 && spawnT != 0 {
							ar.Waypoints[0].Location = math.Lerp2f(spawnT, ar.Waypoints[0].Location, ar.Waypoints[1].Location)
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
							ar.RunwayWaypoints[icao][rwy.Id] = util.DuplicateSlice(wp)
							initializeWaypointLocations(ar.RunwayWaypoints[icao][rwy.Id], loc, nmPerLongitude, magneticVariation, e)
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
				Location: math.Lerp2f(0.5, ar.Waypoints[0].Location, ar.Waypoints[1].Location),
				Handoff:  true,
			}
			ar.Waypoints = append([]Waypoint{ar.Waypoints[0], mid}, ar.Waypoints[1:]...)
		}
	} else {
		if len(ar.Waypoints) < 2 {
			e.ErrorString("must provide at least two \"waypoints\" for arrival " +
				"(even if \"runway_waypoints\" are provided)")
		}

		initializeWaypointLocations(ar.Waypoints, loc, nmPerLongitude, magneticVariation, e)

		for ap, rwywp := range ar.RunwayWaypoints {
			e.Push("Airport " + ap)

			if _, ok := DB.Airports[ap]; !ok {
				e.ErrorString("airport is unknown")
				continue
			}

			for rwy, wp := range rwywp {
				e.Push("Runway " + rwy)

				if _, ok := LookupRunway(ap, rwy); !ok {
					e.ErrorString("runway \"%s\" is unknown. Options: %s", rwy, DB.Airports[ap].ValidRunways())
				}

				initializeWaypointLocations(wp, loc, nmPerLongitude, magneticVariation, e)

				if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
					e.ErrorString("initial \"runway_waypoints\" fix must match " +
						"last \"waypoints\" fix")
				}

				// For the check, splice together the last common
				// waypoint and the runway waypoints.  This will give
				// us a repeated first fix, but this way we can check
				// compliance with restrictions at that fix...
				ewp := append([]Waypoint{ar.Waypoints[len(ar.Waypoints)-1]}, wp...)
				WaypointArray(ewp).CheckArrival(e, controlPositions)

				e.Pop()
			}
			e.Pop()
		}
	}

	ar.Waypoints.CheckArrival(e, controlPositions)

	for arrivalAirport, airlines := range ar.Airlines {
		e.Push("Arrival airport " + arrivalAirport)
		if len(airlines) == 0 {
			e.ErrorString("no \"airlines\" specified for arrivals to " + arrivalAirport)
		}
		for _, al := range airlines {
			DB.CheckAirline(al.ICAO, al.Fleet, e)
			if _, ok := DB.Airports[al.Airport]; !ok {
				e.ErrorString("departure airport \"airport\" \"%s\" unknown", al.Airport)
			}
		}

		ap, ok := airports[arrivalAirport]
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
	} else if _, ok := controlPositions[ar.InitialController]; !ok {
		e.ErrorString("controller \"%s\" not found for \"initial_controller\"", ar.InitialController)
	}

	for _, controller := range controlPositions {
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

// Note: this should match ViceMapSpec in crc2vice (except for the command buffer)
type VideoMap struct {
	Label         string
	Group         int // 0 -> A, 1 -> B
	Name          string
	Id            int
	Lines         [][]math.Point2LL
	CommandBuffer renderer.CommandBuffer
}

// VideoMapLibrary maintains a collection of video maps loaded from multiple
// files.  Video maps are loaded on demand.
type VideoMapLibrary struct {
	manifests map[string]map[string]interface{} // filename -> map name
	maps      map[string]map[string]*VideoMap
	toLoad    map[string]videoMapToLoad
}

type videoMapToLoad struct {
	referenced map[string]interface{}
	filesystem fs.FS
}

type LoadedVideoMaps struct {
	path string
	maps map[string]*VideoMap
}

func MakeVideoMapLibrary() *VideoMapLibrary {
	return &VideoMapLibrary{
		manifests: make(map[string]map[string]interface{}),
		maps:      make(map[string]map[string]*VideoMap),
		toLoad:    make(map[string]videoMapToLoad),
	}
}

// AddFile adds a video map to the library. referenced encodes which maps
// in the file are actually used; the loading code uses this information to
// skip the work of generating CommandBuffers for unused video maps.
func (ml *VideoMapLibrary) AddFile(filesystem fs.FS, filename string, referenced map[string]interface{}, e *util.ErrorLogger) {
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

	// Make sure the file exists but don't load it until it's needed.
	f, err := filesystem.Open(filename)
	if err != nil {
		e.Error(err)
	} else {
		f.Close()
		ml.toLoad[filename] = videoMapToLoad{
			referenced: util.DuplicateMap(referenced),
			filesystem: filesystem,
		}
	}
}

// loadVideoMap handles loading the given map; it runs asynchronously and
// returns the result via the ml.ch chan.
func (v videoMapToLoad) load(filename string, manifest map[string]interface{}) (map[string]*VideoMap, error) {
	f, err := v.filesystem.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := io.Reader(f)
	if strings.HasSuffix(strings.ToLower(filename), ".zst") {
		zr, _ := zstd.NewReader(r, zstd.WithDecoderConcurrency(0))
		defer zr.Close()
		r = zr
	}

	// Initial decoding of the gob file.
	var maps []VideoMap
	dec := gob.NewDecoder(r)
	if err := dec.Decode(&maps); err != nil {
		return nil, err
	}

	// We'll return the maps via a map from the map name to the associated
	// *VideoMap.
	starsMaps := make(map[string]*VideoMap)
	for _, sm := range maps {
		if _, ok := v.referenced[sm.Name]; ok {
			if _, ok := manifest[sm.Name]; !ok {
				panic(fmt.Sprintf("%s: map \"%s\" not found in manifest file", filename, sm.Name))
			}

			ld := renderer.GetLinesDrawBuilder()
			for _, lines := range sm.Lines {
				// Slightly annoying: the line vertices are stored with
				// Point2LLs but AddLineStrip() expects [2]float32s.
				fl := util.MapSlice(lines, func(p math.Point2LL) [2]float32 { return p })
				ld.AddLineStrip(fl)
			}
			ld.GenerateCommands(&sm.CommandBuffer)

			// Clear out Lines so that the memory can be reclaimed since they
			// aren't needed any more.
			sm.Lines = nil
			starsMaps[sm.Name] = &sm
		}
	}

	return starsMaps, nil
}

func (ml *VideoMapLibrary) GetMap(filename, mapname string) (*VideoMap, error) {
	if _, ok := ml.maps[filename]; !ok {
		if vload, ok := ml.toLoad[filename]; !ok {
			return nil, fmt.Errorf("%s: video map \"%s\" requested from file that isn't being loaded",
				filename, mapname)
		} else {
			var err error
			ml.maps[filename], err = vload.load(filename, ml.manifests[filename])
			if err != nil {
				return nil, err
			}
			delete(ml.toLoad, filename)
		}
	}
	if m, ok := ml.maps[filename][mapname]; !ok {
		return nil, fmt.Errorf("%s: no video map \"%s\"", filename, mapname)
	} else {
		return m, nil
	}
}

func (ml VideoMapLibrary) HaveFile(filename string) bool {
	// This can be determined strictly from the manifests and so there's no
	// need to block.
	_, ok := ml.manifests[filename]
	return ok
}

func (ml VideoMapLibrary) AvailableFiles() []string {
	return util.MapSlice(util.SortedMapKeys(ml.manifests),
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
		return util.SortedMapKeys(mf)
	}
}

func (ml VideoMapLibrary) HaveMap(filename, mapname string) bool {
	mf, ok := ml.manifests[filename]
	if ok {
		_, ok = mf[mapname]
	}
	return ok
}

func PrintVideoMaps(path string, e *util.ErrorLogger) {
	lib := MakeVideoMapLibrary()
	lib.AddFile(os.DirFS("."), path, make(map[string]interface{}), e)

	var videoMaps []VideoMap
	for _, name := range lib.AvailableMaps(path) {
		if m, err := lib.GetMap(path, name); err != nil {
			e.Error(err)
		} else {
			videoMaps = append(videoMaps, *m)
		}
	}

	sort.Slice(videoMaps, func(i, j int) bool {
		vi, vj := videoMaps[i], videoMaps[j]
		if vi.Id != vj.Id {
			return vi.Id < vj.Id
		}
		return vi.Name < vj.Name
	})

	fmt.Printf("%5s\t%20s\t%s\n", "Id", "Label", "Name")
	for _, m := range videoMaps {
		fmt.Printf("%5d\t%20s\t%s\n", m.Id, m.Label, m.Name)
	}
}

///////////////////////////////////////////////////////////////////////////

// split -> config
type SplitConfigurationSet map[string]SplitConfiguration

// callsign -> controller contig
type SplitConfiguration map[string]*MultiUserController

type MultiUserController struct {
	Primary          bool     `json:"primary"`
	BackupController string   `json:"backup"`
	Departures       []string `json:"departures"`
	Arrivals         []string `json:"arrivals"` // TEMPORARY for inbound flows transition
	InboundFlows     []string `json:"inbound_flows"`
}

///////////////////////////////////////////////////////////////////////////
// SplitConfigurations

func (sc SplitConfigurationSet) GetConfiguration(split string) (SplitConfiguration, error) {
	if len(sc) == 1 {
		// ignore split
		for _, config := range sc {
			return config, nil
		}
	}

	config, ok := sc[split]
	if !ok {
		return config, fmt.Errorf("%s: split not found", split)
	}
	return config, nil
}

func (sc SplitConfigurationSet) GetPrimaryController(split string) (string, error) {
	configs, err := sc.GetConfiguration(split)
	if err != nil {
		return "", err
	}

	for callsign, mc := range configs {
		if mc.Primary {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("No primary controller in split")
}

func (sc SplitConfigurationSet) Len() int {
	return len(sc)
}

func (sc SplitConfigurationSet) Splits() []string {
	return util.SortedMapKeys(sc)
}

///////////////////////////////////////////////////////////////////////////
// SplitConfiguration

// ResolveController takes a controller callsign and returns the signed-in
// controller that is responsible for that position (possibly just the
// provided callsign).
func (sc SplitConfiguration) ResolveController(callsign string, active func(callsign string) bool) (string, error) {
	i := 0
	for {
		if active(callsign) {
			return callsign, nil
		}

		if ctrl, ok := sc[callsign]; !ok {
			return "", fmt.Errorf("%s: failed to find controller in MultiControllers", callsign)
		} else {
			callsign = ctrl.BackupController
		}

		i++
		if i == 20 {
			return "", fmt.Errorf("%s: unable to find backup for arrival handoff controller", callsign)
		}
	}
}

func (sc SplitConfiguration) GetInboundController(group string) (string, error) {
	for callsign, ctrl := range sc {
		if ctrl.IsInboundController(group) {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("%s: couldn't find inbound controller", group)
}

func (sc SplitConfiguration) GetDepartureController(airport, runway, sid string) (string, error) {
	for callsign, ctrl := range sc {
		if ctrl.IsDepartureController(airport, runway, sid) {
			return callsign, nil
		}
	}

	return "", fmt.Errorf("%s/%s: couldn't find departure controller", airport, sid)
}

///////////////////////////////////////////////////////////////////////////
// MultiUserController

func (c *MultiUserController) IsDepartureController(ap, rwy, sid string) bool {
	for _, d := range c.Departures {
		depAirport, depSIDRwy, ok := strings.Cut(d, "/")
		if ok { // have a runway or SID
			if ap == depAirport && (rwy == depSIDRwy || sid == depSIDRwy) {
				return true
			}
		} else { // no runway/SID, so only match airport
			if ap == depAirport {
				return true
			}
		}
	}
	return false
}

func (c *MultiUserController) IsInboundController(group string) bool {
	return slices.Contains(c.InboundFlows, group)
}

///////////////////////////////////////////////////////////////////////////

type Overflight struct {
	Waypoints           WaypointArray       `json:"waypoints"`
	InitialAltitude     float32             `json:"initial_altitude"`
	CruiseAltitude      float32             `json:"cruise_altitude"`
	AssignedAltitude    float32             `json:"assigned_altitude"`
	InitialSpeed        float32             `json:"initial_speed"`
	SpeedRestriction    float32             `json:"speed_restriction"`
	InitialController   string              `json:"initial_controller"`
	Scratchpad          string              `json:"scratchpad"`
	SecondaryScratchpad string              `json:"secondary_scratchpad"`
	Description         string              `json:"description"`
	CoordinationFix     string              `json:"coordination_fix"`
	Airlines            []OverflightAirline `json:"airlines"`
}

type OverflightAirline struct {
	ICAO             string `json:"icao"`
	Fleet            string `json:"fleet,omitempty"`
	DepartureAirport string `json:"departure_airport"`
	ArrivalAirport   string `json:"arrival_airport"`
}

func (of *Overflight) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[string]*Airport, controlPositions map[string]*Controller, e *util.ErrorLogger) {
	if len(of.Waypoints) < 2 {
		e.ErrorString("must provide at least two \"waypoints\" for overflight")
	}

	initializeWaypointLocations(of.Waypoints, loc, nmPerLongitude, magneticVariation, e)

	of.Waypoints[len(of.Waypoints)-1].Delete = true
	of.Waypoints[len(of.Waypoints)-1].FlyOver = true

	of.Waypoints.CheckOverflight(e, controlPositions)

	if len(of.Airlines) == 0 {
		e.ErrorString("must specify at least one airline in \"airlines\"")
	}
	for _, al := range of.Airlines {
		DB.CheckAirline(al.ICAO, al.Fleet, e)
	}

	if of.InitialAltitude == 0 {
		e.ErrorString("must specify \"initial_altitude\"")
	}

	if of.InitialSpeed == 0 {
		e.ErrorString("must specify \"initial_speed\"")
	}

	if of.InitialController == "" {
		e.ErrorString("\"initial_controller\" missing")
	} else if _, ok := controlPositions[of.InitialController]; !ok {
		e.ErrorString("controller \"%s\" not found for \"initial_controller\"", of.InitialController)
	}
}

// pkg/aviation/aviation.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/gob"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
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
	SignOnTime         time.Time
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
	if err != nil || sq < 0 || sq > 0o7777 {
		return Squawk(0), ErrInvalidSquawkCode
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

				for i := range wp {
					wp[i].OnSTAR = true
				}

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

	for i := range ar.Waypoints {
		ar.Waypoints[i].OnSTAR = true
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
		if _, ok := v.referenced[sm.Name]; ok || len(v.referenced) == 0 /* empty -> load all */ {
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
		if name == "" {
			continue
		}
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
	origCallsign := callsign
	i := 0
	for {
		if ctrl, ok := sc[callsign]; !ok {
			return "", fmt.Errorf("%s: failed to find controller in MultiControllers", callsign)
		} else if ctrl.Primary || active(callsign) {
			return callsign, nil
		} else {
			callsign = ctrl.BackupController
		}

		i++
		if i == 20 {
			return "", fmt.Errorf("%s: unable to find controller backup", origCallsign)
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
// SquawkCodePool

type SquawkCodePool struct {
	First, Last Squawk // inclusive range of codes
	GetOffset   int
	// Available squawk codes are represented by a bitset
	AssignedBits []uint64
}

func makePool(first, last int) *SquawkCodePool {
	ncodes := last - first + 1
	nalloc := (ncodes + 63) / 64

	return &SquawkCodePool{
		First:        Squawk(first),
		Last:         Squawk(last),
		AssignedBits: make([]uint64, nalloc),
	}
}

func MakeCompleteSquawkCodePool() *SquawkCodePool {
	p := makePool(0o1001, 0o7777)

	// Don't issue VFR or any SPCs
	p.Claim(0o1200)
	for _, spc := range spcs {
		p.Claim(spc.Squawk)
	}

	return p
}

func MakeSquawkBankCodePool(bank int) *SquawkCodePool {
	return makePool(bank*0o100+1, bank*0o100+0o77)
}

func (p *SquawkCodePool) Get() (Squawk, error) {
	for i := range len(p.AssignedBits) {
		// Start the search at p.GetOffset, then wrap around.
		idx := (p.GetOffset + i) % len(p.AssignedBits)

		if p.AssignedBits[idx] == ^uint64(0) {
			// All are assigned in this chunk of 64.
			continue
		}

		// "available" is a bit of a misnomer since we may have bits
		// corresponding to invalid codes in the last entry.
		available := ^p.AssignedBits[idx]
		// Pick the last set bit
		bit := bits.TrailingZeros64(available)

		sq := p.First + Squawk(64*idx+bit)
		if sq <= p.Last {
			// It is in fact in our range of valid codes; take it.
			p.AssignedBits[idx] |= (1 << bit)

			// Update GetOffset so that our next search starts from where
			// we last successfully found an available code.
			p.GetOffset = idx

			return sq, nil
		}
	}

	return Squawk(0), ErrNoMoreAvailableSquawkCodes
}

func (p *SquawkCodePool) indices(code Squawk) (int, int, error) {
	if code < p.First || code > p.Last {
		return 0, 0, ErrSquawkCodeNotManagedByPool
	}
	offset := int(code - p.First)
	return offset / 64, offset % 64, nil
}

func (p *SquawkCodePool) IsAssigned(code Squawk) bool {
	if idx, bit, err := p.indices(code); err == nil {
		return p.AssignedBits[idx]&(1<<bit) != 0
	}
	return false
}

func (p *SquawkCodePool) Return(code Squawk) error {
	if !p.IsAssigned(code) {
		return ErrSquawkCodeUnassigned
	}
	if idx, bit, err := p.indices(code); err != nil {
		return err
	} else {
		// Clear the bit
		p.AssignedBits[idx] &= ^(1 << bit)
		return nil
	}
}

func (p *SquawkCodePool) Claim(code Squawk) error {
	if p.IsAssigned(code) {
		return ErrSquawkCodeAlreadyAssigned
	}
	if idx, bit, err := p.indices(code); err != nil {
		return err
	} else {
		// Set the bit
		p.AssignedBits[idx] |= (1 << bit)
		return nil
	}
}

func (p *SquawkCodePool) NumAvailable() int {
	n := int(p.Last - p.First + 1) // total possible
	for _, b := range p.AssignedBits {
		// Reduce the count based on how many are assigned.
		n -= bits.OnesCount64(b)
	}
	return n
}

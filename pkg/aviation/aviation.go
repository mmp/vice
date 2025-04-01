// pkg/aviation/aviation.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"maps"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

type ReportingPoint struct {
	Fix      string
	Location math.Point2LL
}

type InboundFlow struct {
	Arrivals    []Arrival    `json:"arrivals"`
	Overflights []Overflight `json:"overflights"`
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
	Scratchpad          string  `json:"scratchpad"`
	SecondaryScratchpad string  `json:"secondary_scratchpad"`
	Description         string  `json:"description"`
	CoordinationFix     string  `json:"coordination_fix"`

	ExpectApproach util.OneOf[string, map[string]string] `json:"expect_approach"`

	// Airport -> arrival airlines
	Airlines map[string][]ArrivalAirline `json:"airlines"`
}

type AirlineSpecifier struct {
	ICAO          string   `json:"icao"`
	Fleet         string   `json:"fleet,omitempty"`
	AircraftTypes []string `json:"types,omitempty"`
}

type ArrivalAirline struct {
	AirlineSpecifier
	Airport string `json:"airport"`
}

func (a AirlineSpecifier) Aircraft() []FleetAircraft {
	if a.Fleet == "" && len(a.AircraftTypes) == 0 {
		return DB.Airlines[strings.ToUpper(a.ICAO)].Fleets["default"]
	} else if a.Fleet != "" {
		return DB.Airlines[strings.ToUpper(a.ICAO)].Fleets[a.Fleet]
	} else {
		var f []FleetAircraft
		for _, ty := range a.AircraftTypes {
			f = append(f, FleetAircraft{ICAO: ty, Count: 1})
		}
		return f
	}
}

func (a *AirlineSpecifier) Check(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("Airline " + a.ICAO)
	defer e.Pop()

	al, ok := DB.Airlines[strings.ToUpper(a.ICAO)]
	if !ok {
		e.ErrorString("airline not known")
		return
	}

	if a.Fleet == "" && len(a.AircraftTypes) == 0 {
		a.Fleet = "default"
	}
	if a.Fleet != "" {
		if len(a.AircraftTypes) != 0 {
			e.ErrorString("cannot specify both \"fleet\" and \"types\"")
			return
		}
		if _, ok := al.Fleets[a.Fleet]; !ok {
			e.ErrorString("\"fleet\" %s unknown", a.Fleet)
			return
		}
	}

	for _, ac := range a.Aircraft() {
		e.Push("Aircraft " + ac.ICAO)
		if perf, ok := DB.AircraftPerformance[ac.ICAO]; !ok {
			e.ErrorString("aircraft not present in performance database")
		} else {
			if perf.Speed.Min < 35 || perf.Speed.Landing < 35 || perf.Speed.CruiseTAS < 35 ||
				perf.Speed.MaxTAS < 35 || perf.Speed.Min > perf.Speed.MaxTAS {
				e.ErrorString("aircraft's speed specification is questionable: %+v", perf.Speed)
			}
			if perf.Rate.Climb == 0 || perf.Rate.Descent == 0 || perf.Rate.Accelerate == 0 ||
				perf.Rate.Decelerate == 0 {
				e.ErrorString("aircraft's rate specification is questionable: %+v", perf.Rate)
			}
		}
		e.Pop()
	}
}

type Runway struct {
	Id        string
	Heading   float32
	Threshold math.Point2LL
	Elevation int
}

func TidyRunway(r string) string {
	r, _, _ = strings.Cut(r, ".")
	return strings.TrimSpace(r)
}

type ATIS struct {
	Airport  string
	AppDep   string
	Code     string
	Contents string
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

type FlightRules int

const (
	UNKNOWN FlightRules = iota
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

/////////////////////////////////////////////////////////////////////////
// SPC

// SPC (Special Purpose Code) is a unique beacon code,
// indicate an emergency or non-standard operation.
type SPC struct {
	Squawk Squawk
	Code   string
}

var spcs = map[Squawk]string{
	Squawk(0o7400): "LL", // Lost link
	Squawk(0o7500): "HJ", // Hijack/Unlawful Interference
	Squawk(0o7600): "RF", // Communication Failure
	Squawk(0o7700): "EM", // Emergency
	Squawk(0o7777): "MI", // Military interceptor operations
}

func SquawkIsSPC(squawk Squawk) (ok bool, code string) {
	return squawk.IsSPC()
}

// IsSPC returns true if the given squawk code is an SPC.
// The second return value is a string giving the two-letter abbreviated SPC it corresponds to.
func (squawk Squawk) IsSPC() (ok bool, code string) {
	code, ok = spcs[squawk]
	return
}

func StringIsSPC(code string) bool {
	for scpCode := range maps.Values(spcs) {
		if scpCode == code {
			return true
		}
	}
	return false
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
	Standby  TransponderMode = iota /* off */
	Altitude                        /* mode C */
	On                              /* mode A */
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "Altitude", "On"}[t]
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
		if rwy == "" {
			return Runway{}, false
		}

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
	defer e.CheckDepth(e.CurrentDepth())

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
				e.ErrorString("error parsing spawn offset %q: %s", spawnTString, err)
			} else {
				spawnT = float32(st)
			}
		}

		for icao := range ar.Airlines {
			airport, ok := DB.Airports[icao]
			if !ok {
				e.ErrorString("airport %q not found in database", icao)
				continue
			}

			star, ok := airport.STARs[ar.STAR]
			if !ok {
				e.ErrorString(
					"STAR %q not available for %s. Options: %s",
					ar.STAR, icao, strings.Join(util.SortedMapKeys(airport.STARs), ", "),
				)
				continue
			}

			star.Check(e)

			if len(ar.Waypoints) == 0 {
				for _, tr := range util.SortedMapKeys(star.Transitions) {
					wps := star.Transitions[tr]
					if idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == spawnPoint }); idx != -1 {
						if idx == len(wps)-1 {
							e.ErrorString(
								"Only have one waypoint on STAR: %q. 2 or more are necessary for navigation",
								wps[idx].Fix,
							)
						}

						ar.Waypoints = util.DuplicateSlice(wps[idx:])
						ar.Waypoints = ar.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

						if len(ar.Waypoints) >= 2 && spawnT != 0 {
							ar.Waypoints[0].Location = math.Lerp2f(
								spawnT, ar.Waypoints[0].Location, ar.Waypoints[1].Location,
							)
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
							ar.RunwayWaypoints[icao][rwy.Id] =
								ar.RunwayWaypoints[icao][rwy.Id].InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)
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
			ar.Waypoints[0].HumanHandoff = true // empty string -> to human

		default:
			// add a handoff point randomly halfway between the first two waypoints.
			mid := Waypoint{
				Fix: "_handoff",
				// FIXME: it's a little sketchy to lerp Point2ll coordinates
				// but probably ok over short distances here...
				Location:     math.Lerp2f(0.5, ar.Waypoints[0].Location, ar.Waypoints[1].Location),
				HumanHandoff: true,
			}
			ar.Waypoints = append([]Waypoint{ar.Waypoints[0], mid}, ar.Waypoints[1:]...)
		}
	} else {
		if len(ar.Waypoints) < 2 {
			e.ErrorString(
				"must provide at least two \"waypoints\" for arrival " +
					"(even if \"runway_waypoints\" are provided)",
			)
		}

		ar.Waypoints = ar.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

		for ap, rwywp := range ar.RunwayWaypoints {
			e.Push("Airport " + ap)

			if _, ok := DB.Airports[ap]; !ok {
				e.ErrorString("airport is unknown")
				continue
			}

			for rwy, wp := range rwywp {
				e.Push("Runway " + rwy)

				if _, ok := LookupRunway(ap, rwy); !ok {
					e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[ap].ValidRunways())
				}

				wp = wp.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

				for i := range wp {
					wp[i].OnSTAR = true
				}

				if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
					e.ErrorString(
						"initial \"runway_waypoints\" fix must match " +
							"last \"waypoints\" fix",
					)
				}

				// For the check, splice together the last common
				// waypoint and the runway waypoints.  This will give
				// us a repeated first fix, but this way we can check
				// compliance with restrictions at that fix...
				ewp := append([]Waypoint{ar.Waypoints[len(ar.Waypoints)-1]}, wp...)
				approachAssigned := ar.ExpectApproach.A != nil || ar.ExpectApproach.B != nil
				WaypointArray(ewp).CheckArrival(e, controlPositions, approachAssigned)

				e.Pop()
			}
			e.Pop()
		}
	}

	for i := range ar.Waypoints {
		ar.Waypoints[i].OnSTAR = true
	}

	approachAssigned := ar.ExpectApproach.A != nil || ar.ExpectApproach.B != nil
	ar.Waypoints.CheckArrival(e, controlPositions, approachAssigned)

	for arrivalAirport, airlines := range ar.Airlines {
		e.Push("Arrival airport " + arrivalAirport)
		if len(airlines) == 0 {
			e.ErrorString("no \"airlines\" specified for arrivals to " + arrivalAirport)
		}
		for _, al := range airlines {
			al.Check(e)
			if _, ok := DB.Airports[al.Airport]; !ok {
				e.ErrorString("departure airport \"airport\" %q unknown", al.Airport)
			}
		}

		_, ok := airports[arrivalAirport]
		if !ok {
			e.ErrorString("arrival airport %q unknown", arrivalAirport)
		}

		e.Pop()
	}

	if ar.ExpectApproach.A != nil { // Given a single string
		if len(ar.Airlines) > 1 {
			e.ErrorString("There are multiple arrival airports but only one approach in \"expect_approach\"")
		}
		// Ugly way to get the key from a one-element map
		var airport string
		for airport, _ = range ar.Airlines {
		}
		// We checked the arrival airports were valid above, no need to issue an error if not found.
		if ap, ok := airports[airport]; ok {
			if _, ok := ap.Approaches[*ar.ExpectApproach.A]; !ok {
				e.ErrorString(
					"arrival airport %q doesn't have a %q approach for \"expect_approach\"",
					airport, *ar.ExpectApproach.A,
				)
			}
		}
	} else if ar.ExpectApproach.B != nil {
		for airport, appr := range *ar.ExpectApproach.B {
			if _, ok := ar.Airlines[airport]; !ok {
				e.ErrorString(
					"airport %q is listed in \"expect_approach\" but is not in arrival airports",
					airport,
				)
			} else if ap, ok := airports[airport]; ok {
				if _, ok := ap.Approaches[appr]; !ok {
					e.ErrorString(
						"arrival airport %q doesn't have a %q approach for \"expect_approach\"",
						airport, appr,
					)
				}
			}
		}
	}

	if ar.InitialAltitude == 0 {
		e.ErrorString("must specify \"initial_altitude\"")
	} else {
		// Make sure the initial altitude isn't below any of
		// altitude restrictions.
		for _, wp := range ar.Waypoints {
			if wp.AltitudeRestriction != nil &&
				wp.AltitudeRestriction.TargetAltitude(ar.InitialAltitude) > ar.InitialAltitude {
				e.ErrorString("\"initial_altitude\" is below altitude restriction at %q", wp.Fix)
			}
		}
	}

	if ar.InitialSpeed == 0 {
		e.ErrorString("must specify \"initial_speed\"")
	}

	if ar.InitialController == "" {
		e.ErrorString("\"initial_controller\" missing")
	} else if _, ok := controlPositions[ar.InitialController]; !ok {
		e.ErrorString("controller %q not found for \"initial_controller\"", ar.InitialController)
	}

	for id, controller := range controlPositions {
		if controller.ERAMFacility && controller.FacilityIdentifier == "" {
			e.ErrorString("%q is an ERAM facility, but has no facility id specified", id)
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
// SquawkCodePool

type SquawkCodePool struct {
	First, Last Squawk // inclusive range of codes
	// Available squawk codes are represented by a bitset
	AssignedBits []uint64
}

func makePool(first, last int) *SquawkCodePool {
	ncodes := last - first + 1
	nalloc := (ncodes + 63) / 64

	p := &SquawkCodePool{
		First:        Squawk(first),
		Last:         Squawk(last),
		AssignedBits: make([]uint64, nalloc),
	}

	p.removeInvalidCodes()

	// Mark the excess invalid codes in the last entry of AssignedBits as
	// taken so that we don't try to assign them later.
	slop := ncodes % 64
	p.AssignedBits[nalloc-1] |= ^uint64(0) << slop

	return p
}

func (p *SquawkCodePool) removeInvalidCodes() {
	// Remove the non-discrete codes (i.e., ones ending in 00).
	for i := 0; i <= 0o7700; i += 0o100 {
		_ = p.Claim(Squawk(i))
	}

	claimRange := func(start, end int) {
		for i := start; i < end; i++ {
			_ = p.Claim(Squawk(i))
		}
	}
	claimBlock := func(start int) {
		claimRange(start, start+64)
	}

	// Remove various reserved squawk codes, per 7110.66G
	// https://www.faa.gov/documentLibrary/media/Order/FAA_Order_JO_7110.66G_NBCAP.pdf.
	_ = p.Claim(0o1200)
	_ = p.Claim(0o1201)
	_ = p.Claim(0o1202)
	_ = p.Claim(0o1205)
	_ = p.Claim(0o1206)
	claimRange(0o1207, 0o1233)
	claimRange(0o1235, 0o1254)
	claimRange(0o1256, 0o1272)
	_ = p.Claim(0o1234)
	_ = p.Claim(0o1255)
	claimRange(0o1273, 0o1275)
	_ = p.Claim(0o1276)
	_ = p.Claim(0o1277)
	_ = p.Claim(0o2000)
	claimRange(0o4400, 0o4433)
	claimRange(0o4434, 0o4437)
	claimRange(0o4440, 0o4452)
	_ = p.Claim(0o4453)
	claimRange(0o4454, 0o4477)
	_ = p.Claim(0o7400)
	claimRange(0o7501, 0o7577)
	_ = p.Claim(0o7500)
	_ = p.Claim(0o7600)
	claimRange(0o7601, 0o7607)
	_ = p.Claim(0o7700)
	claimRange(0o7701, 0o7707)
	_ = p.Claim(0o7777)

	// TODO? 0100, 0200, 0300, 0400 blocks?

	// FIXME: these probably shouldn't be hardcoded like this but should be available to PCT.
	claimBlock(0o5100) // PCT TRACON for DC SFRA/FRZ
	claimBlock(0o5200) // PCT TRACON for DC SFRA/FRZ

	claimBlock(0o5000)
	claimBlock(0o5400)
	claimBlock(0o6100)
	claimBlock(0o6400)

	_ = p.Claim(0o7777)
	for squawk := range spcs {
		_ = p.Claim(squawk)
	}
}

func MakeCompleteSquawkCodePool() *SquawkCodePool {
	return makePool(0o1001, 0o7777)
}

func MakeSquawkBankCodePool(bank int) *SquawkCodePool {
	return makePool(bank*0o100+1, bank*0o100+0o77)
}

func (p *SquawkCodePool) Get() (Squawk, error) {
	start := rand.Intn(len(p.AssignedBits)) // random starting point in p.AssignedBits
	rot := rand.Intn(64)                    // random rotation to randomize search start within each uint64

	for i := range len(p.AssignedBits) {
		// Start the search at start, then wrap around.
		idx := (start + i) % len(p.AssignedBits)

		if p.AssignedBits[idx] == ^uint64(0) {
			// All are assigned in this chunk of 64 squawk codes.
			continue
		}

		// Flip it around and see which ones are available.
		available := ^p.AssignedBits[idx]

		// Randomly rotate the bits so that when we start searching for a
		// set bit starting from the low bit, we effectively randomize
		// which bit index we're starting from.
		available = bits.RotateLeft64(available, rot)

		// Find the last set bit and then map that back to a bit index in
		// the unrotated bits.
		bit := (bits.TrailingZeros64(available) + 64 - rot) % 64

		// Record that we've taken it
		p.AssignedBits[idx] |= (1 << bit)

		return p.First + Squawk(64*idx+bit), nil
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

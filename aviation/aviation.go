// aviation/aviation.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

type RadarTrack struct {
	ADSBCallsign        ADSBCallsign
	Squawk              Squawk
	Mode                TransponderMode
	Ident               bool
	TrueAltitude        float32
	TransponderAltitude float32
	Location            math.Point2LL
	Heading             float32
	Groundspeed         float32
	TypeOfFlight        TypeOfFlight
}

type ADSBCallsign string

func (c ADSBCallsign) String() string { return string(c) }

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

type ReportingPoint struct {
	Fix      string
	Location math.Point2LL
}

type InboundFlow struct {
	Arrivals    []Arrival    `json:"arrivals"`
	Overflights []Overflight `json:"overflights"`
}

// HasHumanHandoff returns true if any arrival or overflight in the flow
// has a waypoint with HumanHandoff set.
func (f InboundFlow) HasHumanHandoff() bool {
	for _, ar := range f.Arrivals {
		if ar.Waypoints.HasHumanHandoff() {
			return true
		}
		for _, rwys := range ar.RunwayWaypoints {
			for _, wps := range rwys {
				if wps.HasHumanHandoff() {
					return true
				}
			}
		}
	}
	for _, of := range f.Overflights {
		if of.Waypoints.HasHumanHandoff() {
			return true
		}
	}
	return false
}

// InitialControllers returns a list of all initial controllers specified
// for arrivals and overflights in this flow.
func (f InboundFlow) InitialControllers() []ControlPosition {
	c := make(map[ControlPosition]struct{})
	for _, ar := range f.Arrivals {
		if ar.InitialController != "" {
			c[ar.InitialController] = struct{}{}
		}
	}
	for _, of := range f.Overflights {
		if of.InitialController != "" {
			c[of.InitialController] = struct{}{}
		}
	}
	return slices.Collect(maps.Keys(c))
}

type Arrival struct {
	Waypoints       WaypointArray                       `json:"waypoints"`
	RunwayWaypoints map[string]map[string]WaypointArray `json:"runway_waypoints"` // Airport -> runway -> waypoints
	SpawnWaypoint   string                              `json:"spawn"`            // if "waypoints" aren't specified
	CruiseAltitude  float32                             `json:"cruise_altitude"`
	Route           string                              `json:"route"`
	STAR            string                              `json:"star"`

	InitialController   ControlPosition `json:"initial_controller"`
	InitialAltitude     float32         `json:"initial_altitude"`
	AssignedAltitude    float32         `json:"assigned_altitude"`
	InitialSpeed        float32         `json:"initial_speed"`
	SpeedRestriction    float32         `json:"speed_restriction"`
	Scratchpad          string          `json:"scratchpad"`
	SecondaryScratchpad string          `json:"secondary_scratchpad"`
	Description         string          `json:"description"`
	CoordinationFix     string          `json:"coordination_fix"`
	IsRNAV              bool            `json:"is_rnav"`

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

type TypeOfFlight int

const (
	FlightTypeUnknown TypeOfFlight = iota
	FlightTypeDeparture
	FlightTypeArrival
	FlightTypeOverflight
)

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

var badCallsigns map[string]any = map[string]any{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
	"PSA5342": nil,
}

// cwtMaxRanges defines the maximum flight distances (in nautical miles)
// for each CWT category (semi ad-hoc, but seems to work reasonably); 0 means no limit.
var cwtMaxRanges = map[string]float32{
	"A": 0,    // A380
	"B": 0,    // Large widebody (B777, B787, A330, B747)
	"C": 6500, // Medium widebody (B767, A300, DC-10)
	"D": 5500, // Large widebody variants (A350-1000, B747SP, L-1011)
	"E": 4500, // B757
	"F": 3800, // Narrowbody jets (A320, B737)
	"G": 2000, // Regional jets/turboprops (CRJ, ATR, Dash 8)
	"H": 2000, // Light jets (Citation, Learjet)
	"I": 1200, // Small aircraft (King Air, Baron)
}

// Though category C, these can go quite far, so don't prohibit them.
var extraLongRange = []string{"A35K", "A359"}

// currentCallsigns will be empty if we don't care about unique suffixes.
func (a AirlineSpecifier) SampleAcTypeAndCallsign(r *rand.Rand, currentCallsigns []ADSBCallsign, uniqueSuffix bool, departureAirport, arrivalAirport string, lg *log.Logger) (actype, callsign string) {
	dbAirline, ok := DB.Airlines[strings.ToUpper(a.ICAO)]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Airline %q not found in database", a.ICAO)
		return "", ""
	}

	// Calculate flight distance to filter aircraft by CWT category
	dep, arr := DB.Airports[departureAirport], DB.Airports[arrivalAirport]
	flightDistance := math.NMDistance2LL(dep.Location, arr.Location)

	// Sample according to fleet count, filtering by maximum distance for CWT category
	acCount := 0
	for _, ac := range a.Aircraft() {
		// Filter based on flight distance and aircraft CWT category
		if flightDistance > 0 && !slices.Contains(extraLongRange, ac.ICAO) {
			if perf, ok := DB.AircraftPerformance[ac.ICAO]; ok {
				if maxRange, ok := cwtMaxRanges[perf.Category.CWT]; ok {
					// Check if flight distance exceeds category maximum (0 means no limit)
					if maxRange > 0 && flightDistance > maxRange {
						continue
					}
				}
			}
		}

		// Reservoir sampling...
		acCount += ac.Count
		if r.Float32() < float32(ac.Count)/float32(acCount) {
			actype = ac.ICAO
		}
	}
	if actype == "" {
		// Try again without considering range.
		for _, ac := range a.Aircraft() {
			acCount += ac.Count
			if r.Float32() < float32(ac.Count)/float32(acCount) {
				actype = ac.ICAO
			}
		}
	}

	if _, ok := DB.AircraftPerformance[actype]; !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %q not found in performance database for airline %+v",
			actype, a)
		return "", ""
	}

	callsignClashesWithExisting := func(proposed string) bool {
		if uniqueSuffix {
			// Reject if the last 2 characters of callsign match an existing callsign.
			suffixMatches := func(cs ADSBCallsign) bool {
				return strings.HasSuffix(string(cs), proposed[len(proposed)-2:])
			}
			return slices.ContainsFunc(currentCallsigns, suffixMatches)
		} else {
			// Reject only if there's an exact match
			return slices.Contains(currentCallsigns, ADSBCallsign(proposed))
		}
	}

	// random callsign
	var cs strings.Builder
	for range 100 {
		format := "####"
		if len(dbAirline.Callsign.CallsignFormats) > 0 {
			f, ok := rand.SampleWeighted(r, dbAirline.Callsign.CallsignFormats,
				func(f string) int {
					if _, wt, ok := strings.Cut(f, "x"); ok { // we have a weight
						if v, err := strconv.Atoi(wt); err == nil {
							return v
						}
					}
					return 1
				})
			if ok {
				format = f
			}
		}

		cs.WriteString(strings.ToUpper(dbAirline.ICAO))
	loop:
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					cs.WriteByte(byte('1' + r.Intn(9))) // Don't start with a 0.
				} else {
					cs.WriteByte(byte('0' + r.Intn(10)))
				}
			case '@':
				// Exclude I and O which can be confused with 1 and 0
				const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
				cs.WriteByte(letters[r.Intn(len(letters))])
			case 'x':
				break loop
			}
		}
		if _, ok := badCallsigns[cs.String()]; ok {
			cs.Reset()
			continue // nope
		} else if slices.Contains(currentCallsigns, ADSBCallsign(cs.String())) {
			cs.Reset()
			continue
		} else if callsignClashesWithExisting(cs.String()) {
			cs.Reset()
			continue
		}
		return actype, cs.String()
	}

	return "", ""
}

type Runway struct {
	Id                         string
	Heading                    float32
	Threshold                  math.Point2LL
	ThresholdCrossingHeight    int // delta from elevation
	Elevation                  int
	DisplacedThresholdDistance float32 // in nm
}

func TidyRunway(r string) string {
	r, _, _ = strings.Cut(r, ".")
	return strings.TrimSpace(r)
}

// AirportHasRunway returns true if the given runway exists at the airport (from DB).
func AirportHasRunway(airport, runway string) bool {
	ap, ok := DB.Airports[airport]
	if !ok {
		return false
	}
	tidyRunway := TidyRunway(runway)
	for _, rwy := range ap.Runways {
		if TidyRunway(rwy.Id) == tidyRunway {
			return true
		}
	}
	return false
}

///////////////////////////////////////////////////////////////////////////

type FlightRules int

const (
	FlightRulesUnknown FlightRules = iota
	FlightRulesIFR
	FlightRulesVFR
	FlightRulesDVFR
	FlightRulesSVFR
)

func (f FlightRules) String() string {
	return [...]string{"Unknown", "IFR", "VFR", "DVFR", "SVFR"}[f]
}

// FlightPlan represents the flight plan from the perspective of the
// Aircraft: who they are, what they're doing, how they're going to get
// there.
type FlightPlan struct {
	Rules            FlightRules
	AircraftType     string
	CruiseSpeed      int
	DepartureAirport string
	DepartureRunway  string
	Altitude         int
	ArrivalAirport   string
	AlternateAirport string
	Exit             string
	Route            string
	Remarks          string
}

/////////////////////////////////////////////////////////////////////////
// Squawk Codes and SPCs

type Squawk int

func (sq Squawk) String() string { return fmt.Sprintf("%04o", sq) }

func ParseSquawk(s string) (Squawk, error) {
	if len(s) != 4 {
		return Squawk(0), ErrInvalidSquawkCode
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil || sq < 0 || sq > 0o7777 {
		return Squawk(0), ErrInvalidSquawkCode
	}
	return Squawk(sq), nil
}

func ParseSquawkOrBlock(s string) (Squawk, error) {
	if len(s) != 4 && len(s) != 2 {
		return Squawk(0), ErrInvalidSquawkCode
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil || sq < 0 || sq > 0o7777 {
		return Squawk(0), ErrInvalidSquawkCode
	}
	return Squawk(sq), nil
}

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
func (sq Squawk) IsSPC() (ok bool, code string) {
	code, ok = spcs[sq]
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

func FormatAltitude(falt float32) string {
	alt := int(falt)
	if alt >= 18000 {
		return "FL" + strconv.Itoa(alt/100)
	} else if alt < 1000 {
		return strconv.Itoa(100 * (alt / 100))
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

func FormatScopeAltitude[T ~int | ~float32](alt T) string {
	return fmt.Sprintf("%03v", int(alt+50)/100)
}

type TransponderMode int

const (
	TransponderModeStandby  TransponderMode = iota /* off */
	TransponderModeAltitude                        /* mode C */
	TransponderModeOn                              /* mode A */
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "Altitude", "On"}[t]
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

func cleanRunway(rwy string) string {
	// The runway may have extra text to distinguish different
	// configurations (e.g., "13.JFK-ILS-13"). Find the prefix that is
	// an actual runway specifier to use in the search below.
	for i, ch := range rwy {
		if ch >= '0' && ch <= '9' {
			continue
		} else if ch == 'L' || ch == 'R' || ch == 'C' || ch == 'W' {
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

// OppositeRunwayId returns the runway ID for the opposite end of the given runway.
// E.g., "13L" -> "31R", "22R" -> "4L", "9" -> "27".
func OppositeRunwayId(rwy string) string {
	rwy = cleanRunway(rwy)
	if rwy == "" {
		return ""
	}

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
	case 'W':
		ext = "W"
		num = rwy[:n-1]
	default:
		num = rwy
	}

	v, err := strconv.Atoi(num)
	if err != nil {
		return ""
	}

	// (v+18)%36 would give 0 for runway 36, so handle 18 specially.
	if v == 18 {
		return "36" + ext
	}
	return fmt.Sprintf("%d", (v+18)%36) + ext
}

func LookupOppositeRunway(icao, rwy string) (Runway, bool) {
	ap, ok := DB.Airports[icao]
	if !ok {
		return Runway{}, false
	}

	oppRwy := OppositeRunwayId(rwy)
	if oppRwy == "" {
		return Runway{}, false
	}

	idx := slices.IndexFunc(ap.Runways, func(r Runway) bool { return r.Id == oppRwy })
	if idx == -1 {
		return Runway{}, false
	}
	return ap.Runways[idx], true
}

// IntersectingRunways returns all runways at airport that physically intersect
// the given runway. It checks if runway centerlines cross and the intersection
// point is within maxDistNM of both runway segments (threshold to threshold).
// Use maxDistNM=0 for strict threshold-to-threshold intersection, or a small
// value (e.g., 0.5) to account for pavement extending past thresholds.
// Returns both directions for each intersecting runway (e.g., both "13L" and "31R").
func IntersectingRunways(airport, rwy string, nmPerLongitude, maxDistNM float32) []string {
	rwy = TidyRunway(rwy)

	// Get runway threshold positions
	rwyEndpoints := func(r string) (p1, p2 [2]float32, ok bool) {
		var runway, opp Runway
		if runway, ok = LookupRunway(airport, r); !ok {
			return
		}
		if opp, ok = LookupOppositeRunway(airport, r); !ok {
			return
		}
		p1 = math.LL2NM(runway.Threshold, nmPerLongitude)
		p2 = math.LL2NM(opp.Threshold, nmPerLongitude)
		return p1, p2, true
	}

	// Distance from point p to line segment seg0-seg1
	pointToSegmentDist := func(p, seg0, seg1 [2]float32) float32 {
		v := math.Sub2f(seg1, seg0)
		w := math.Sub2f(p, seg0)
		c1 := math.Dot(w, v)
		c2 := math.Dot(v, v)
		if c2 == 0 {
			return math.Distance2f(p, seg0)
		}
		t := c1 / c2
		// Clamp to segment endpoints
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		proj := math.Add2f(seg0, math.Scale2f(v, t))
		return math.Distance2f(p, proj)
	}

	rwy1, rwy2, ok := rwyEndpoints(rwy)
	if !ok {
		return nil
	}

	oppRwy := OppositeRunwayId(rwy)

	var intersecting []string
	seen := make(map[string]bool)
	ap, ok := DB.Airports[airport]
	if !ok {
		return nil
	}

	for _, otherRwy := range ap.Runways {
		id := TidyRunway(otherRwy.Id)
		if id == rwy || id == oppRwy {
			continue
		}

		oth1, oth2, ok := rwyEndpoints(otherRwy.Id)
		if !ok {
			continue
		}

		// Check if the infinite centerlines intersect
		p, ok := math.LineLineIntersect(rwy1, rwy2, oth1, oth2)
		if !ok {
			continue // Lines are parallel
		}

		// Check if intersection point is within maxDistNM of both runway segments
		dist1 := pointToSegmentDist(p, rwy1, rwy2)
		dist2 := pointToSegmentDist(p, oth1, oth2)

		if dist1 <= maxDistNM && dist2 <= maxDistNM {
			// Add both this runway and its opposite direction
			if !seen[id] {
				seen[id] = true
				intersecting = append(intersecting, id)
			}
			oppId := OppositeRunwayId(id)
			if oppId != "" && !seen[oppId] {
				seen[oppId] = true
				intersecting = append(intersecting, oppId)
			}
		}
	}

	return intersecting
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

	return math.FastExp(-g0 * M_air * altm / (R * T_b))
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
	airports map[string]*Airport, controlPositions map[ControlPosition]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
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
		if ar.SpawnWaypoint != "" {
			e.ErrorString("\"spawn\" cannot be specified if \"waypoints\" are provided")
			return
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
				WaypointArray(ewp).CheckArrival(e, controlPositions, approachAssigned, checkScratchpad)

				e.Pop()
			}
			e.Pop()
		}
	}

	for i := range ar.Waypoints {
		ar.Waypoints[i].OnSTAR = true
	}

	approachAssigned := ar.ExpectApproach.A != nil || ar.ExpectApproach.B != nil
	ar.Waypoints.CheckArrival(e, controlPositions, approachAssigned, checkScratchpad)

	for arrivalAirport := range ar.Airlines {
		e.Push("Arrival airport " + arrivalAirport)
		if len(ar.Airlines[arrivalAirport]) == 0 {
			e.ErrorString("no \"airlines\" specified for arrivals to %q", arrivalAirport)
		}
		for i := range ar.Airlines[arrivalAirport] {
			ar.Airlines[arrivalAirport][i].Check(e)
			if _, ok := DB.Airports[ar.Airlines[arrivalAirport][i].Airport]; !ok {
				e.ErrorString("departure airport \"airport\" %q unknown", ar.Airlines[arrivalAirport][i].Airport)
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
		for airport = range ar.Airlines {
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

	if !checkScratchpad(ar.Scratchpad) {
		e.ErrorString("%s: invalid scratchpad", ar.Scratchpad)
	}
	if !checkScratchpad(ar.SecondaryScratchpad) {
		e.ErrorString("%s: invalid secondary scratchpad", ar.SecondaryScratchpad)
	}
}

func (ar Arrival) GetRunwayWaypoints(airport, rwy string) WaypointArray {
	if ap, ok := ar.RunwayWaypoints[airport]; !ok {
		return nil
	} else if wp, ok := ap[rwy]; !ok {
		return nil
	} else {
		return wp
	}
}

///////////////////////////////////////////////////////////////////////////
// EnrouteSquawkCodePool

type EnrouteSquawkCodePool struct {
	Available *util.IntRangeSet

	// Initial is maintained as a read-only snapshot of the initial set of
	// available codes; it allows us to catch cases where the caller tries
	// to return code that is inside the range we cover but was removed
	// from the pool when it was first initialized.
	Initial *util.IntRangeSet
}

func removeInvalidCodes(codes *util.IntRangeSet) {
	// Remove the non-discrete codes (i.e., ones ending in 00).
	for i := 0; i <= 0o7700; i += 0o100 {
		_ = codes.Take(i)
	}

	takeRange := func(start, end int) {
		for i := start; i <= end; i++ {
			_ = codes.Take(i)
		}
	}
	takeBlock := func(start int) {
		takeRange(start, start+64)
	}

	// Remove various reserved squawk codes, per 7110.66G
	// https://www.faa.gov/documentLibrary/media/Order/FAA_Order_JO_7110.66G_NBCAP.pdf.
	takeBlock(0o1200)
	_ = codes.Take(0o2000)
	takeRange(0o4400, 0o4433)
	takeRange(0o4434, 0o4437)
	takeRange(0o4440, 0o4452)
	_ = codes.Take(0o4453)
	takeRange(0o4454, 0o4477)
	_ = codes.Take(0o7400)
	takeRange(0o7501, 0o7577)
	_ = codes.Take(0o7500)
	_ = codes.Take(0o7600)
	takeRange(0o7601, 0o7607)
	_ = codes.Take(0o7700)
	takeRange(0o7701, 0o7707)
	_ = codes.Take(0o7777)

	// FIXME: these probably shouldn't be hardcoded like this but should be available to PCT.
	takeBlock(0o5100) // PCT TRACON for DC SFRA/FRZ
	takeBlock(0o5200) // PCT TRACON for DC SFRA/FRZ

	takeBlock(0o5000)
	takeBlock(0o5400)
	takeBlock(0o6100)
	takeBlock(0o6400)

	_ = codes.Take(0o7777)
	for squawk := range spcs {
		_ = codes.Take(int(squawk))
	}
}

func MakeEnrouteSquawkCodePool(loc *LocalSquawkCodePool) *EnrouteSquawkCodePool {
	p := &EnrouteSquawkCodePool{
		Initial: util.MakeIntRangeSet(0o1001, 0o7777),
	}

	removeInvalidCodes(p.Initial)

	// Remove codes in the local pool as well
	if loc != nil {
		for _, pool := range loc.Pools {
			for _, rng := range pool.Ranges {
				for sq := rng[0]; sq <= rng[1]; sq++ {
					_ = p.Initial.Take(int(sq))
				}
			}
		}
		for _, r := range loc.BeaconCodeTable.VFRCodes {
			for sq := r[0]; sq <= r[1]; sq++ {
				_ = p.Initial.Take(int(sq))
			}
		}
	}

	p.Available = p.Initial.Clone()

	return p
}

func (p *EnrouteSquawkCodePool) Get(r *rand.Rand) (Squawk, error) {
	code, err := p.Available.GetRandom(r)
	if err != nil {
		return Squawk(0), ErrNoMoreAvailableSquawkCodes
	} else {
		return Squawk(code), nil
	}
}

func (p *EnrouteSquawkCodePool) IsAssigned(code Squawk) bool {
	return !p.Available.IsAvailable(int(code))
}

func (p *EnrouteSquawkCodePool) InInitialPool(code Squawk) bool {
	return p.Initial.IsAvailable(int(code))
}

func (p *EnrouteSquawkCodePool) Return(code Squawk) error {
	if !p.Initial.InRange(int(code)) || !p.Initial.IsAvailable(int(code)) {
		// It's not ours; just ignore it.
		return nil
	}
	if err := p.Available.Return(int(code)); err != nil {
		return ErrSquawkCodeUnassigned
	}
	return nil
}

func (p *EnrouteSquawkCodePool) Take(code Squawk) error {
	if p.IsAssigned(code) {
		return ErrSquawkCodeAlreadyAssigned
	}
	if err := p.Available.Take(int(code)); err != nil {
		return ErrSquawkCodeNotManagedByPool
	}
	return nil
}

func (p *EnrouteSquawkCodePool) NumAvailable() int {
	return p.Available.Count()
}

///////////////////////////////////////////////////////////////////////////
// LocalSquawkCodePool

// SSR Codes Windows
type LocalSquawkCodePoolSpecifier struct {
	Pools           map[string]PoolSpecifier `json:"auto_assignable_codes"`
	BeaconCodeTable BeaconCodeTableSpecifier `json:"beacon_code_table"`
}

type PoolSpecifier struct {
	Ranges  []string `json:"ranges"`
	Rules   string   `json:"flight_rules"`
	Backups string   `json:"backup_pool_list"`
	// TODO: no_flight_plan_exclusion: bool, if true, don't show WHO in DB if it exits the departure filter region.
}

type BeaconCodeTableSpecifier struct {
	VFRCodes []string `json:"vfr_codes"` // Array of squawk code ranges
	// TODO: MSAW
}

// Doesn't return an error since errors are logged to the ErrorLogger
// (which in turn will cause validation to fail if there are any issues...)
func parseCodeRange(s string, e *util.ErrorLogger) [2]Squawk {
	if low, high, ok := strings.Cut(s, "-"); ok {
		// Code range
		slow, err := ParseSquawk(low)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", low)
		}
		shigh, err := ParseSquawk(high)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", high)
		}
		if slow > shigh {
			e.ErrorString("first squawk code %q is greater than second %q", slow, shigh)
		}
		return [2]Squawk{slow, shigh}
	} else {
		// Single code
		sq, err := ParseSquawk(s)
		if err != nil {
			e.ErrorString("Invalid squawk code %q", s)
		}
		return [2]Squawk{sq, sq}
	}
}

// Helper function to parse ranges from an array of strings
func parseCodeRanges(ranges []string, e *util.ErrorLogger) [][2]Squawk {
	var result [][2]Squawk

	// Parse ranges from the array
	for _, r := range ranges {
		result = append(result, parseCodeRange(r, e))
	}

	return result
}

func (s *LocalSquawkCodePoolSpecifier) PostDeserialize(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if len(s.Pools) == 0 {
		return
	} else {
		if vpool, ok := s.Pools["vfr"]; !ok {
			e.ErrorString("must specify \"vfr\" squawk pool")
		} else if vpool.Rules != "" && vpool.Rules != "v" {
			e.ErrorString("\"rules\" cannot be specified for the \"vfr\" pool")
		}

		if ipool, ok := s.Pools["ifr"]; !ok {
			e.ErrorString("must specify \"ifr\" squawk pool")
		} else if ipool.Rules != "" && ipool.Rules != "i" {
			e.ErrorString("\"rules\" cannot be specified for the \"ifr\" pool")
		}
		// Numbered ones optional(?)

		// Both check individual pools for valid ranges and internal overlaps and also
		// check for overlaps with previous ranges.
		var allRanges [][][2]Squawk
		for name, spec := range s.Pools {
			e.Push("Code pool " + name)
			if name != "ifr" && name != "vfr" && name != "1" && name != "2" &&
				name != "3" && name != "4" {
				e.ErrorString("Pool name %q is invalid: must be one of \"ifr\", \"vfr\", "+
					"\"1\", \"2\", \"3\", or \"4\".", name)
			}

			// Validate input: must provide Ranges
			if len(spec.Ranges) == 0 {
				e.ErrorString("must specify \"ranges\" for pool %q", name)
			}

			// Parse all the ranges for this pool
			poolRanges := parseCodeRanges(spec.Ranges, e)

			// Check for overlaps within this pool
			overlaps := func(a, b [2]Squawk) bool {
				return (a[0] >= b[0] && a[0] <= b[1]) || (a[1] >= b[0] && a[1] <= b[1])
			}
			for i := range poolRanges {
				for j := range i {
					if overlaps(poolRanges[i], poolRanges[j]) {
						e.ErrorString("Range %s-%s overlaps with range %s-%s in the same pool",
							poolRanges[i][0], poolRanges[i][1], poolRanges[j][0], poolRanges[j][1])
					}
				}

				for _, ranges := range allRanges {
					for _, rng := range ranges {
						if overlaps(poolRanges[i], rng) {
							e.ErrorString("Range %s-%s overlaps with range %s-%s in other pool",
								poolRanges[i][0], poolRanges[i][1], rng[0], rng[1])
						}
					}
				}
			}
			allRanges = append(allRanges, poolRanges)

			if spec.Rules != "" && spec.Rules != "i" && spec.Rules != "v" {
				e.ErrorString("\"rules\" must be \"i\" or \"v\"")
			}

			for i, ch := range spec.Backups {
				if string(ch) == strings.TrimPrefix(name, "/") {
					e.ErrorString("Can't specify ourself %q as a backup pool", string(ch))
				} else if strings.Contains(spec.Backups[:i], string(ch)) {
					e.ErrorString("Can't repeat the same backup pool %q", string(ch))
				} else if ch < '1' && ch > '4' {
					e.ErrorString("Backup pools can only contain \"1\", \"2\", \"3\", or \"4\".")
				}
			}

			e.Pop()
		}
	}

	e.Push("\"beacon_code_table\"")
	// Validate VFR codes using the same parser
	_ = parseCodeRanges(s.BeaconCodeTable.VFRCodes, e)
	e.Pop()
}

type LocalSquawkCodePool struct {
	Pools           map[string]LocalPool
	BeaconCodeTable BeaconCodeTable
}

type BeaconCodeTable struct {
	VFRCodes [][2]Squawk
	// TODO: MSAW
}

type LocalPool struct {
	Initial     *util.IntRangeSet
	Available   *util.IntRangeSet
	Ranges      [][2]Squawk
	Backups     string
	FlightRules FlightRules
}

func MakeLocalSquawkCodePool(spec LocalSquawkCodePoolSpecifier) *LocalSquawkCodePool {
	// Assume spec has already been validated
	p := &LocalSquawkCodePool{Pools: make(map[string]LocalPool)}
	if len(spec.Pools) == 0 {
		// Return a reasonable default
		p.Pools["vfr"] = LocalPool{
			Initial:     util.MakeIntRangeSet(0o201, 0o277),
			Available:   util.MakeIntRangeSet(0o201, 0o277),
			Ranges:      [][2]Squawk{{0o0201, 0o0277}},
			FlightRules: FlightRulesVFR,
		}
		p.Pools["ifr"] = LocalPool{
			Initial:     util.MakeIntRangeSet(0o301, 0o377),
			Available:   util.MakeIntRangeSet(0o301, 0o377),
			Ranges:      [][2]Squawk{{0o0301, 0o0377}},
			FlightRules: FlightRulesIFR,
		}
	} else {
		for name, pspec := range spec.Pools {
			poolRanges := parseCodeRanges(pspec.Ranges, nil)

			// Find the min and max values to create the IntRangeSet
			r := [2]int{int(poolRanges[0][0]), int(poolRanges[0][1])}
			for _, rng := range poolRanges {
				r[0] = min(r[0], int(rng[0]))
				r[1] = max(r[1], int(rng[1]))
			}

			// Create an IntRangeSet covering the full range
			rs := util.MakeIntRangeSet(r[0], r[1])

			// Remove values that are not in any of the specified ranges
			for sq := r[0]; sq <= r[1]; sq++ {
				if !slices.ContainsFunc(poolRanges, func(r [2]Squawk) bool { return sq >= int(r[0]) && sq <= int(r[1]) }) {
					// sq not in any of the ranges.
					_ = rs.Take(sq)
				}
			}

			p.Pools[name] = LocalPool{
				Initial:   rs.Clone(),
				Available: rs,
				Ranges:    poolRanges,
				Backups:   pspec.Backups,
				FlightRules: func() FlightRules {
					if name == "vfr" || pspec.Rules == "v" {
						return FlightRulesVFR
					}
					return FlightRulesIFR
				}(),
			}
		}
	}

	if len(spec.BeaconCodeTable.VFRCodes) == 0 {
		p.BeaconCodeTable.VFRCodes = [][2]Squawk{{0o1200, 0o1277}}
	} else {
		p.BeaconCodeTable.VFRCodes = parseCodeRanges(spec.BeaconCodeTable.VFRCodes, nil)
	}

	return p
}

func (p *LocalSquawkCodePool) IsReservedVFRCode(sq Squawk) bool {
	for _, r := range p.BeaconCodeTable.VFRCodes {
		if sq >= r[0] && sq <= r[1] {
			return true
		}
	}
	return false
}

// inbound rules are only used to choose a VFR/IFR pool if spec == ""
func (p *LocalSquawkCodePool) Get(spec string, rules FlightRules, r *rand.Rand) (Squawk, FlightRules, error) {
	if spec == "" {
		if rules == FlightRulesIFR {
			spec = "ifr"
		} else {
			spec = "vfr"
		}
	} else if spec == "+" {
		spec = "ifr"
	} else if spec == "/" {
		spec = "vfr"
	} else if len(spec) == 2 && spec[0] == '/' {
		spec = spec[1:]
	}

	if sq, err := ParseSquawk(spec); err == nil && len(spec) == 4 {
		// Remove it from the corresponding pool for auto-assignment. (But
		// it's ok to assign the same code multiple times...)
		for _, pool := range p.Pools {
			for _, rng := range pool.Ranges {
				if sq >= rng[0] && sq <= rng[1] {
					_ = pool.Available.Take(int(sq))
					return sq, pool.FlightRules, nil
				}
			}
		}
		// It's fine if it's not it any of the pools
		return sq, rules, nil
	}

	if pool, ok := p.Pools[spec]; !ok {
		return Squawk(0), FlightRulesUnknown, ErrBadPoolSpecifier
	} else {
		backups := pool.Backups
		rules := pool.FlightRules // initial pool's rules are sticky even if we go to a backup
		for {
			if sq, err := pool.Available.GetRandom(r); err == nil {
				return Squawk(sq), rules, nil
			} else if len(backups) == 0 {
				return Squawk(0), rules, ErrNoMoreAvailableSquawkCodes
			} else {
				pool = p.Pools[string(backups[0])]
				backups = backups[1:]
			}
		}
	}
}

func (p *LocalSquawkCodePool) IsAssigned(code Squawk) bool {
	for _, pool := range p.Pools {
		if pool.Initial.IsAvailable(int(code)) && !pool.Available.IsAvailable(int(code)) {
			return true
		}
	}
	return false
}

func (p *LocalSquawkCodePool) InInitialPool(code Squawk) bool {
	for _, pool := range p.Pools {
		if pool.Initial.IsAvailable(int(code)) {
			return true
		}
	}
	return false
}

func (p *LocalSquawkCodePool) Return(sq Squawk) error {
	for _, pool := range p.Pools {
		if pool.Available.InRange(int(sq)) {
			return pool.Available.Return(int(sq))
		}
	}
	return fmt.Errorf("returned code %s not in any pool's range", sq)
}

///////////////////////////////////////////////////////////////////////////

type RadioTransmissionType int

const (
	RadioTransmissionUnknown    = iota
	RadioTransmissionContact    // Messages initiated by the pilot
	RadioTransmissionReadback   // Reading back an instruction
	RadioTransmissionUnexpected // Something urgent or unusual
	RadioTransmissionMixUp      // Pilot confused about who was being addressed
	RadioTransmissionNoId       // No callsign included (e.g. to say "blocked")
)

func (r RadioTransmissionType) String() string {
	switch r {
	case RadioTransmissionContact:
		return "contact"
	case RadioTransmissionReadback:
		return "readback"
	case RadioTransmissionUnexpected:
		return "urgent"
	case RadioTransmissionMixUp:
		return "mixup"
	case RadioTransmissionNoId:
		return "noid"
	default:
		return "(unhandled type)"
	}
}

///////////////////////////////////////////////////////////////////////////
// CWT functions

// CWTApproachSeparation returns the required separation between aircraft of the two
// given CWT categories. If 0 is returned, minimum radar separation should be used.
func CWTApproachSeparation(front, back string) float32 {
	if len(front) != 1 || (front[0] < 'A' && front[0] > 'I') {
		return 10
	}
	if len(back) != 1 || (back[0] < 'A' && back[0] > 'I') {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-2
	cwtOnApproachLookUp := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 6},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 6}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 6, 6},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtOnApproachLookUp[f][b]
}

// CWT25nmReductionAllowed returns true if 2.5nm reduced approach separation is
// allowed for the given front/back CWT category pair per 7110.65 5-5-4(i):
// the leading aircraft's weight class must be the same or less than the trailing
// aircraft, and super/heavy aircraft may only participate as the trailing aircraft.
func CWT25nmReductionAllowed(frontCWT, backCWT string) bool {
	return len(frontCWT) == 1 && frontCWT[0] >= 'E' && frontCWT[0] <= 'I' &&
		len(backCWT) == 1 && backCWT[0] >= 'A' && backCWT[0] <= 'I' &&
		frontCWT[0] >= backCWT[0]
}

// CWTRequiredApproachSeparation returns the required approach separation between
// aircraft of the given CWT categories, applying the 2.5nm reduction if eligible25nm
// is true and the weight categories allow it.
func CWTRequiredApproachSeparation(frontCWT, backCWT string, eligible25nm bool) float32 {
	sep := CWTApproachSeparation(frontCWT, backCWT)
	if sep == 0 {
		sep = 3 // baseline radar separation
	}
	if eligible25nm && CWT25nmReductionAllowed(frontCWT, backCWT) {
		sep = 2.5
	}
	return sep
}

// CWTDirectlyBehindSeparation returns the required separation between
// aircraft of the two given CWT categories. If 0 is returned, minimum
// radar separation should be used.
func CWTDirectlyBehindSeparation(front, back string) float32 {
	if len(front) != 1 || (front[0] < 'A' && front[0] > 'I') {
		return 10
	}
	if len(back) != 1 || (back[0] < 'A' && back[0] > 'I') {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-1
	cwtBehindLookup := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 5}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtBehindLookup[f][b]
}

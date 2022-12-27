// aviation.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
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
		return nil, MalformedMessageError{"Expected >= 3 fields in METAR text"}
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
	Code     string
	Contents string
}

type NetworkRating int

const (
	UndefinedRating = iota
	ObserverRating
	S1Rating
	S2Rating
	S3Rating
	C1Rating
	C2Rating
	C3Rating
	I1Rating
	I2Rating
	I3Rating
	SupervisorRating
	AdministratorRating
)

func (r NetworkRating) String() string {
	return [...]string{"Undefined", "Observer", "S1", "S2", "S3", "C1", "C2", "C3",
		"I1", "I2", "I3", "Supervisor", "Administrator"}[r]
}

type Facility int

const (
	FacilityOBS = iota
	FacilityFSS
	FacilityDEL
	FacilityGND
	FacilityTWR
	FacilityAPP
	FacilityCTR
	FacilityUndefined
)

func (f Facility) String() string {
	return [...]string{"Observer", "FSS", "Delivery", "Ground", "Tower", "Approach", "Center", "Undefined"}[f]
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
	Callsign string // it's not exactly a callsign, but...
	Name     string
	CID      string
	Rating   NetworkRating

	Frequency     Frequency
	ScopeRange    int
	Facility      Facility
	Location      Point2LL
	RequestRelief bool
}

func (c *Controller) GetPosition() *Position {
	return database.LookupPosition(c.Callsign, c.Frequency)
}

type Pilot struct {
	Callsign string
	Name     string
	CID      string
	Rating   NetworkRating
}

type RadarTrack struct {
	Position    Point2LL
	Altitude    int
	Groundspeed int
	Heading     float32
	Time        time.Time
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

type Aircraft struct {
	Callsign        string
	Scratchpad      string
	AssignedSquawk  Squawk // from ATC
	Squawk          Squawk // actually squawking
	Mode            TransponderMode
	TempAltitude    int
	VoiceCapability VoiceCapability
	FlightPlan      *FlightPlan

	Tracks [10]RadarTrack

	TrackingController        string
	InboundHandoffController  string
	OutboundHandoffController string
}

type AircraftPair struct {
	a, b *Aircraft
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

type RangeLimitFlightRules int

const (
	IFR_IFR = iota
	IFR_VFR
	VFR_VFR
	NumRangeTypes
)

func (r RangeLimitFlightRules) String() string {
	return [...]string{"IFR-IFR", "IFR-VFR", "VFR-VFR"}[r]
}

type RangeLimits struct {
	WarningLateral    float32 // nm
	WarningVertical   int32   // feet
	ViolationLateral  float32
	ViolationVertical int32
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

type User struct {
	Name   string
	Note   string
	Rating NetworkRating
}

type VoiceCapability int

const (
	VoiceUnknown = iota
	VoiceFull
	VoiceReceive
	VoiceText
)

func (v VoiceCapability) String() string {
	return [...]string{"?", "v", "r", "t"}[v]
}

func (a *Aircraft) Altitude() int {
	return a.Tracks[0].Altitude
}

// Reported in feet per minute
func (a *Aircraft) AltitudeChange() int {
	if a.Tracks[0].Position.IsZero() || a.Tracks[1].Position.IsZero() {
		return 0
	}

	dt := a.Tracks[0].Time.Sub(a.Tracks[1].Time)
	return int(float64(a.Tracks[0].Altitude-a.Tracks[1].Altitude) / dt.Minutes())
}

func (a *Aircraft) HaveTrack() bool {
	return a.Position()[0] != 0 || a.Position()[1] != 0
}

func (a *Aircraft) Position() Point2LL {
	return a.Tracks[0].Position
}

func (a *Aircraft) InterpolatedPosition(t float32) Point2LL {
	// Return the first valid one; this makes things cleaner at the start when
	// we don't have a full set of track history.
	pos := func(idx int) Point2LL {
		if idx >= len(a.Tracks) {
			// Linearly extrapolate the last two. (We don't expect to be
			// doing this often...)
			steps := 1 + idx - len(a.Tracks)
			last := len(a.Tracks) - 1
			v := sub2ll(a.Tracks[last].Position, a.Tracks[last-1].Position)
			return add2ll(a.Tracks[last].Position, scale2ll(v, float32(steps)))
		}
		for idx > 0 {
			if !a.Tracks[idx].Position.IsZero() {
				break
			}
			idx--
		}
		return a.Tracks[idx].Position
	}

	if t < 0 {
		// interpolate past tracks

		t /= -5
		idx := int(t)
		dt := t - float32(idx)

		return lerp2ll(dt, pos(idx), pos(idx+1))
	} else {
		// extrapolate from last track. fit a parabola a t^2 + b t ^ c = x_i
		// to the last three tracks, with associated times assumed to be
		// 0, -5, and -10. We immediately have c=x_0 and are left with:
		// 25 a - 5 b + x0 = x1
		// 100 a - 10 b + x0 = x2
		// Solving gives a = (x0 - 2 x1 + x2) / 50, b = (3 x0 - 4x1 + x2) / 10
		fit := func(x0, x1, x2 float32) (a, b, c float32) {
			a = (x0 - 2*x1 + x2) / 50
			b = (3*x0 - 4*x1 + x2) / 10
			c = x0
			return
		}
		longa, longb, longc := fit(pos(0).Longitude(), pos(1).Longitude(), pos(2).Longitude())
		lata, latb, latc := fit(pos(0).Latitude(), pos(1).Latitude(), pos(2).Latitude())

		return Point2LL{longa*t*t + longb*t + longc, lata*t*t + latb*t + latc}
	}
}

func (a *Aircraft) Groundspeed() int {
	return a.Tracks[0].Groundspeed
}

// Note: returned value includes the magnetic correction
func (a *Aircraft) Heading() float32 {
	return a.Tracks[0].Heading + database.MagneticVariation
}

// Perhaps confusingly, the vector returned by HeadingVector() is not
// aligned with the reported heading but is instead along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (a *Aircraft) HeadingVector() Point2LL {
	var v [2]float32
	if !a.HaveHeading() {
		v = [2]float32{cos(radians(a.Heading())), sin(radians(a.Heading()))}
	} else {
		p0, p1 := a.Tracks[0].Position, a.Tracks[1].Position
		v = sub2ll(p0, p1)
	}

	nm := nmlength2ll(v)
	// v's length should be groundspeed / 60 nm.
	return scale2ll(v, float32(a.Groundspeed())/(60*nm))
}

func (a *Aircraft) HaveHeading() bool {
	return !a.Tracks[0].Position.IsZero() && !a.Tracks[1].Position.IsZero()
}

func (a *Aircraft) ExtrapolatedHeadingVector(lag float32) Point2LL {
	if !a.HaveHeading() {
		return Point2LL{}
	}
	t := float32(time.Since(a.Tracks[0].Time).Seconds()) - lag
	return sub2ll(a.InterpolatedPosition(t+.5), a.InterpolatedPosition(t-0.5))
}

func (a *Aircraft) HeadingTo(p Point2LL) float32 {
	return headingp2ll(a.Position(), p, database.MagneticVariation)
}

func (a *Aircraft) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !a.Tracks[0].Position.IsZero() && now.Sub(a.Tracks[0].Time) > 30*time.Second
}

func (a *Aircraft) AddTrack(t RadarTrack) {
	// Move everthing forward one to make space for the new one. We could
	// be clever and use a circular buffer to skip the copies, though at
	// the cost of more painful indexing elsewhere...
	copy(a.Tracks[1:], a.Tracks[:len(a.Tracks)-1])
	a.Tracks[0] = t
}

func (a *Aircraft) Telephony() string {
	cs := strings.TrimRight(a.Callsign, "0123456789")
	if sign, ok := database.callsigns[cs]; ok {
		return sign.Telephony
	} else {
		return ""
	}
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (a *Aircraft) OnGround() bool {
	if a.Groundspeed() < 40 {
		return true
	}

	if fp := a.FlightPlan; fp != nil {
		for _, airport := range [2]string{fp.DepartureAirport, fp.ArrivalAirport} {
			if ap, ok := database.FAA.airports[airport]; ok {
				heightAGL := abs(a.Altitude() - ap.Elevation)
				return heightAGL < 100
			}
		}
	}
	// Didn't know the airports. We could be more fancy and find the
	// closest airport in the sector file and then use its elevation,
	// though it's not clear that is worth the work.
	return false
}

func (a *Aircraft) GetFormattedFlightPlan(includeRemarks bool) (contents string, indent int) {
	if plan := a.FlightPlan; plan == nil {
		contents = "No flight plan"
		return
	} else {
		var sb strings.Builder
		w := tabwriter.NewWriter(&sb, 0, 1, 1, ' ', 0)
		write := func(s string) { w.Write([]byte(s)) }

		write(a.Callsign)
		if a.VoiceCapability != VoiceFull {
			write("/" + a.VoiceCapability.String())
		}
		write("\t")
		nbsp := "\u00a0" // non-breaking space; wrapText honors these
		write("rules:" + nbsp + plan.Rules.String() + "\t")
		write("a/c:" + nbsp + plan.AircraftType + "\t")
		write("dep/arr:" + nbsp + plan.DepartureAirport + "-" + plan.ArrivalAirport + nbsp + "(" + plan.AlternateAirport + ")\n")

		write("\t")
		write("alt:" + nbsp + nbsp + nbsp + fmt.Sprintf("%d", plan.Altitude))
		if a.TempAltitude != 0 {
			write(fmt.Sprintf(nbsp+"(%d)", a.TempAltitude))
		}
		write("\t")
		write("sqk:" + nbsp + a.AssignedSquawk.String() + "\t")
		write("scratch:" + nbsp + a.Scratchpad + "\n")

		w.Flush()
		contents = sb.String()

		indent = 1 + len(a.Callsign)
		if a.VoiceCapability != VoiceFull {
			indent += 1 + len(a.VoiceCapability.String())
		}
		indstr := fmt.Sprintf("%*c", indent, ' ')
		contents = contents + indstr + "route:" + nbsp + plan.Route + "\n"
		if includeRemarks {
			contents = contents + indstr + "rmks:" + nbsp + nbsp + plan.Remarks + "\n"
		}

		return contents, indent
	}
}

// Returns nm
func EstimatedFutureDistance(a *Aircraft, b *Aircraft, seconds float32) float32 {
	a0, av := a.Position(), a.HeadingVector()
	b0, bv := b.Position(), b.HeadingVector()
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
	return strings.TrimPrefix(strings.TrimPrefix(fp.TypeWithoutSuffix(), "H/"), "S/")
}

func (fp FlightPlan) TypeWithoutSuffix() string {
	// try to chop off equipment suffix
	actypeFields := strings.Split(fp.AircraftType, "/")
	switch len(actypeFields) {
	case 3:
		// Heavy (presumably), with suffix
		return actypeFields[0] + "/" + actypeFields[1]
	case 2:
		if actypeFields[0] == "H" || actypeFields[0] == "S" {
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

type Conflict struct {
	aircraft [2]*Aircraft
	limits   RangeLimits
}

func GetConflicts(aircraft []*Aircraft, rangeLimits [NumRangeTypes]RangeLimits) (warning []Conflict, violation []Conflict) {
	for i, ac1 := range aircraft {
		for j := i + 1; j < len(aircraft); j++ {
			ac2 := aircraft[j]

			var r RangeLimits
			if ac1.FlightPlan != nil && ac1.FlightPlan.Rules == IFR {
				if ac2.FlightPlan != nil && ac2.FlightPlan.Rules == IFR {
					r = rangeLimits[IFR_IFR]
				} else {
					r = rangeLimits[IFR_VFR]
				}
			} else {
				if ac2.FlightPlan != nil && ac2.FlightPlan.Rules == IFR {
					r = rangeLimits[IFR_VFR]
				} else {
					r = rangeLimits[VFR_VFR]
				}
			}

			ldist := nmdistance2ll(ac1.Position(), ac2.Position())
			vdist := int32(abs(ac1.Altitude() - ac2.Altitude()))
			if ldist < r.ViolationLateral && vdist < r.ViolationVertical {
				violation = append(violation,
					Conflict{aircraft: [2]*Aircraft{ac1, ac2}, limits: r})
			} else if ldist < r.WarningLateral && vdist < r.WarningVertical {
				warning = append(warning,
					Conflict{aircraft: [2]*Aircraft{ac1, ac2}, limits: r})
			}
		}
	}

	return
}

type AircraftType struct {
	Name         string
	Manufacturer string

	RECAT string
	Type  string // [ALH]#[JTP] -> { L->land, H->heli, A -> water}, # engines, { Jet, Turboprop, Prop}
	WTC   string // Wake turbulence category
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
	if lac, ok := database.AircraftTypes[leader.FlightPlan.BaseType()]; !ok {
		return 0, ErrUnknownAircraftType
	} else {
		if fac, ok := database.AircraftTypes[follower.FlightPlan.BaseType()]; !ok {
			return 0, ErrUnknownAircraftType
		} else {
			return RECATDistance(lac.RECAT, fac.RECAT)
		}
	}
}

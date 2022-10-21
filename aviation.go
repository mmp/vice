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
	airport   string
	time      string
	auto      bool
	wind      string
	weather   string
	altimeter string
	rmk       string
}

func (m METAR) String() string {
	auto := ""
	if m.auto {
		auto = "AUTO"
	}
	return strings.Join([]string{m.airport, m.time, auto, m.wind, m.weather, m.altimeter, m.rmk}, " ")
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

func (f Frequency) String() string {
	s := fmt.Sprintf("%03d.%03d", f/1000, f%1000)
	for len(s) < 7 {
		s += "0"
	}
	return s
}

type Controller struct {
	callsign string // it's not exactly a callsign, but...
	name     string
	cid      string
	rating   NetworkRating

	frequency     Frequency
	scopeRange    int
	facility      Facility
	location      Point2LL
	requestRelief bool
}

func (c *Controller) GetPosition() *Position {
	// compute the basic callsign: e.g. NY_1_CTR -> NY_CTR, PHL_ND_APP -> PHL_APP
	callsign := c.callsign
	cf := strings.Split(callsign, "_")
	if len(cf) > 2 {
		callsign = cf[0] + "_" + cf[len(cf)-1]
	}

	for i, pos := range database.positions[callsign] {
		if pos.frequency == c.frequency {
			return &database.positions[callsign][i]
		}
	}
	return nil
}

type Pilot struct {
	callsign string
	name     string
	cid      string
	rating   NetworkRating
}

type RadarTrack struct {
	position    Point2LL
	altitude    int
	groundspeed int
	heading     float32
	time        time.Time
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
	rules                  FlightRules
	actype                 string
	cruiseSpeed            int
	depart                 string
	departTimeEst          int
	departTimeActual       int
	altitude               int
	arrive                 string
	hours, minutes         int
	fuelHours, fuelMinutes int
	alternate              string
	route                  string
	remarks                string
}

type FlightStrip struct {
	callsign    string
	formatId    string // ???
	annotations []string
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
	callsign        string
	scratchpad      string
	assignedSquawk  Squawk // from ATC
	squawk          Squawk // actually squawking
	mode            TransponderMode
	tempAltitude    int
	voiceCapability VoiceCapability
	flightPlan      *FlightPlan

	tracks [10]RadarTrack
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
	number         string
	heading        float32
	threshold, end Point2LL
}

type Navaid struct {
	id       string
	navtype  string
	name     string
	location Point2LL
}

type Fix struct {
	id       string
	location Point2LL
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
	id        string
	name      string
	elevation int
	location  Point2LL
}

type Callsign struct {
	company   string
	country   string
	telephony string
	threeltr  string
}

type Position struct {
	name                  string // e.g., Kennedy Local 1
	callsign              string // e.g., Kennedy Tower
	frequency             Frequency
	sectorId              string // For handoffs, etc--e.g., 2W
	scope                 string // For tracked a/c on the scope--e.g., T
	id                    string // e.g. JFK_TWR
	lowSquawk, highSquawk Squawk
}

type User struct {
	name   string
	note   string
	rating NetworkRating
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

type TextMessageType int

const (
	TextBroadcast = iota
	TextWallop
	TextATC
	TextFrequency
	TextPrivate
)

func (t TextMessageType) String() string {
	return [...]string{"Broadcast", "Wallop", "ATC", "Frequency", "Private"}[t]
}

type TextMessage struct {
	sender      string
	messageType TextMessageType
	contents    string
	frequencies []Frequency // only used for messageType == TextFrequency
	recipient   string      // only used for TextPrivate
}

func (a *Aircraft) Altitude() int {
	return a.tracks[0].altitude
}

// Reported in feet per minute
func (a *Aircraft) AltitudeChange() int {
	if a.tracks[0].position.IsZero() || a.tracks[1].position.IsZero() {
		return 0
	}

	dt := a.tracks[0].time.Sub(a.tracks[1].time)
	return int(float64(a.tracks[0].altitude-a.tracks[1].altitude) / dt.Minutes())
}

func (a *Aircraft) HaveTrack() bool {
	return a.Position()[0] != 0 || a.Position()[1] != 0
}

func (a *Aircraft) Position() Point2LL {
	return a.tracks[0].position
}

func (a *Aircraft) InterpolatedPosition(t float32) Point2LL {
	// Return the first valid one; this makes things cleaner at the start when
	// we don't have a full set of track history.
	pos := func(idx int) Point2LL {
		if idx >= len(a.tracks) {
			// Linearly extrapolate the last two. (We don't expect to be
			// doing this often...)
			steps := 1 + idx - len(a.tracks)
			last := len(a.tracks) - 1
			v := sub2ll(a.tracks[last].position, a.tracks[last-1].position)
			return add2ll(a.tracks[last].position, scale2ll(v, float32(steps)))
		}
		for idx > 0 {
			if !a.tracks[idx].position.IsZero() {
				break
			}
			idx--
		}
		return a.tracks[idx].position
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

func (a *Aircraft) GroundSpeed() int {
	return a.tracks[0].groundspeed
}

// Note: returned value includes the magnetic correction
func (a *Aircraft) Heading() float32 {
	// The heading reported by vatsim seems systemically off for some (but
	// not all!) aircraft and not just magnetic variation. So take the
	// heading vector, which is more reliable, and work from there...
	return headingv2ll(a.HeadingVector(), database.MagneticVariation)
}

// Scale it so that it represents where it is expected to be one minute in
// the future.
func (a *Aircraft) HeadingVector() Point2LL {
	if !a.HaveHeading() {
		return Point2LL{}
	}

	p0, p1 := a.tracks[0].position, a.tracks[1].position
	v := sub2ll(p0, p1)
	nm := nmlength2ll(v)
	// v's length should be groundspeed / 60 nm.
	return scale2ll(v, float32(a.GroundSpeed())/(60*nm))
}

func (a *Aircraft) HaveHeading() bool {
	return !a.tracks[0].position.IsZero() && !a.tracks[1].position.IsZero()
}

func (a *Aircraft) ExtrapolatedHeadingVector(lag float32) Point2LL {
	if !a.HaveHeading() {
		return Point2LL{}
	}
	t := float32(time.Since(a.tracks[0].time).Seconds()) - lag
	return sub2ll(a.InterpolatedPosition(t+.5), a.InterpolatedPosition(t-0.5))
}

func (a *Aircraft) HeadingTo(p Point2LL) float32 {
	return headingp2ll(a.Position(), p, database.MagneticVariation)
}

func (a *Aircraft) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !a.tracks[0].position.IsZero() && now.Sub(a.tracks[0].time) > 30*time.Second
}

func (a *Aircraft) AddTrack(t RadarTrack) {
	// Move everthing forward one to make space for the new one. We could
	// be clever and use a circular buffer to skip the copies, though at
	// the cost of more painful indexing elsewhere...
	copy(a.tracks[1:], a.tracks[:len(a.tracks)-1])
	a.tracks[0] = t
}

func (a *Aircraft) Callsign() string {
	return a.callsign
}

func (a *Aircraft) Telephony() string {
	cs := strings.TrimRight(a.callsign, "0123456789")
	if sign, ok := database.callsigns[cs]; ok {
		return sign.telephony
	} else {
		return ""
	}
}

func (a *Aircraft) OnGround() bool {
	if a.GroundSpeed() < 40 {
		return true
	}

	if a.flightPlan != nil {
		for _, airport := range [2]string{a.flightPlan.depart, a.flightPlan.arrive} {
			if ap, ok := database.FAA.airports[airport]; ok {
				heightAGL := abs(a.Altitude() - ap.elevation)
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
	if a.flightPlan == nil {
		contents = "No flight plan"
		return
	} else {
		plan := a.flightPlan

		var sb strings.Builder
		w := tabwriter.NewWriter(&sb, 0, 1, 1, ' ', 0)
		write := func(s string) { w.Write([]byte(s)) }

		write(a.Callsign())
		if a.voiceCapability != VoiceFull {
			write("/" + a.voiceCapability.String())
		}
		write("\t")
		nbsp := "\u00a0" // non-breaking space; wrapText honors these
		write("rules:" + nbsp + plan.rules.String() + "\t")
		write("a/c:" + nbsp + plan.actype + "\t")
		write("dep/arr:" + nbsp + plan.depart + "-" + plan.arrive + nbsp + "(" + plan.alternate + ")\n")

		write("\t")
		write("alt:" + nbsp + nbsp + nbsp + fmt.Sprintf("%d", plan.altitude))
		if a.tempAltitude != 0 {
			write(fmt.Sprintf(nbsp+"(%d)", a.tempAltitude))
		}
		write("\t")
		write("sqk:" + nbsp + a.assignedSquawk.String() + "\t")
		write("scratch:" + nbsp + a.scratchpad + "\n")

		w.Flush()
		contents = sb.String()

		indent = 1 + len(a.Callsign())
		if a.voiceCapability != VoiceFull {
			indent += 1 + len(a.voiceCapability.String())
		}
		indstr := fmt.Sprintf("%*c", indent, ' ')
		contents = contents + indstr + "route:" + nbsp + plan.route + "\n"
		if includeRemarks {
			contents = contents + indstr + "rmks:" + nbsp + nbsp + plan.remarks + "\n"
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

func (fp FlightPlan) TypeWithoutSuffix() string {
	// try to chop off equipment suffix
	actypeFields := strings.Split(fp.actype, "/")
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
		return fp.actype
	}
}

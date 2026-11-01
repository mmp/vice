// nav/airwork.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// Airwork is a VFR aircraft practicing maneuvers within Radius nm of Center
// and within AltRange, the way pilots do in a practice area: stalls, steep
// turns, ground reference maneuvers and the like, one after another until
// RemainingSteps seconds have passed. Terrain is the highest terrain in the
// area in feet MSL; each maneuver keeps the height above it that the FAA's
// Airplane Flying Handbook and Airman Certification Standards give for it.
//
// The maneuvers are flown directly rather than through the targets the rest
// of nav flies to: a stall, a steep spiral or a chandelle trades speed and
// altitude in ways that the climb, descent and turn rates of normal flight
// don't allow.
type Airwork struct {
	Radius   float32
	Center   math.Point2LL
	AltRange [2]float32
	Terrain  float32

	RemainingSteps int

	// Maneuver is the maneuver being flown and Segments what is left of
	// it; the first of them is in progress, and Progress says how far.
	Maneuver AirworkManeuver
	Segments []AirworkSegment
	Progress AirworkProgress
}

// AirworkManeuver is a maneuver practiced during airwork.
type AirworkManeuver int8

const (
	AirworkTurns AirworkManeuver = iota
	AirworkSlowFlight
	AirworkPowerOffStall
	AirworkPowerOnStall
	AirworkAcceleratedStall
	AirworkSteepTurns
	AirworkSteepSpiral
	AirworkChandelle
	AirworkLazyEights
	AirworkRectangularCourse
	AirworkSTurns
	AirworkTurnsAroundPoint
	AirworkEmergencyDescent
	AirworkEngineFailure
	NumAirworkManeuvers
)

func (m AirworkManeuver) String() string {
	return [...]string{"climbing, descending and level turns", "slow flight", "power-off stalls",
		"power-on stalls", "accelerated stalls", "steep turns", "a steep spiral", "chandelles", "lazy eights",
		"a rectangular course", "S-turns", "turns around a point", "an emergency descent",
		"a simulated engine failure"}[m]
}

// AirworkSegmentKind is how an AirworkSegment is flown laterally.
type AirworkSegmentKind int8

const (
	// AirworkHeading turns steadily through Turn degrees over Seconds, or
	// holds the heading if Turn is zero, drifting with the wind.
	AirworkHeading AirworkSegmentKind = iota
	// AirworkCircle flies Turn degrees around a point on the ground Radius
	// nm off the side the turn is toward, correcting for the wind so that
	// the radius over the ground stays the same.
	AirworkCircle
	// AirworkTrack turns onto the ground track Track and flies it for
	// Distance nm, correcting for the wind.
	AirworkTrack
	// AirworkStayNear flies for Seconds, holding its heading near the
	// middle of the area and turning back toward it once a quarter of
	// the area's radius away.
	AirworkStayNear
)

// AirworkProfile is how the altitude and speed change over an
// AirworkSegment: steadily, or starting or finishing gradually.
type AirworkProfile int8

const (
	AirworkLinear AirworkProfile = iota
	AirworkEaseIn
	AirworkEaseOut
	AirworkEaseInOut
)

// apply returns how much of the change is made once the share f of the
// segment has been flown.
func (p AirworkProfile) apply(f float32) float32 {
	switch p {
	case AirworkEaseIn:
		return f * f
	case AirworkEaseOut:
		return 1 - (1-f)*(1-f)
	case AirworkEaseInOut:
		return f * f * (3 - 2*f)
	default:
		return f
	}
}

// AirworkSegment is one stretch of a maneuver. How it is flown laterally
// sets how long it lasts; it climbs Climb feet (descending if negative)
// and reaches IAS by its end. An IAS of zero keeps the speed it starts at.
type AirworkSegment struct {
	Kind     AirworkSegmentKind
	Turn     float32 // degrees, positive to the right
	Seconds  float32
	Radius   float32 // nm
	Track    math.MagneticHeading
	Distance float32 // nm

	Climb   float32
	IAS     float32
	Profile AirworkProfile
}

// AirworkProgress is how far the aircraft has flown the current
// AirworkSegment and how it was flying when it started it. Done is in
// seconds, nm or degrees around the circle, as the segment measures it.
// For a circle, Center is the point circled and Bearing the direction from
// it to the aircraft at the last tick.
type AirworkProgress struct {
	Started  bool
	Done     float32
	Heading  math.MagneticHeading
	Altitude float32
	IAS      float32
	Center   math.Point2LL
	Bearing  math.TrueHeading
}

// StartAirwork starts the airwork a route's waypoint calls for.
func StartAirwork(wp av.Waypoint) *Airwork {
	return &Airwork{
		Radius:         float32(wp.AirworkRadius()),
		Center:         wp.Location,
		AltRange:       wp.AltitudeRestriction().Range,
		Terrain:        float32(wp.AirworkTerrain()),
		RemainingSteps: wp.AirworkMinutes() * 60, // sim ticks are 1 second.
	}
}

// TargetAltitude returns the altitude the aircraft is climbing or
// descending to in its maneuver.
func (aw *Airwork) TargetAltitude(alt float32) float32 {
	if len(aw.Segments) == 0 || !aw.Progress.Started {
		return alt
	}
	return aw.Progress.Altitude + aw.Segments[0].Climb
}

// fly flies the aircraft for one second of airwork, starting the next
// maneuver once the last is done. It returns false when the time for
// airwork has run out and the last maneuver is finished.
func (aw *Airwork) fly(nav *Nav, wxs wx.Sample) bool {
	aw.RemainingSteps--
	if len(aw.Segments) == 0 {
		if aw.RemainingSteps <= 0 {
			return false
		}
		aw.startManeuver(nav, wxs)
	}

	fs := &nav.FlightState
	seg, pr := aw.Segments[0], &aw.Progress
	if !pr.Started {
		*pr = AirworkProgress{Started: true, Heading: fs.Heading, Altitude: fs.Altitude, IAS: fs.IAS}
		if seg.Kind == AirworkCircle {
			side := math.MagneticToTrue(fs.Heading, fs.MagneticVariation) + math.TrueHeading(90*math.Sign(seg.Turn))
			pr.Center = math.Offset2LL(fs.Position, side, seg.Radius, fs.NmPerLongitude)
			pr.Bearing = math.Heading2LL(pr.Center, fs.Position, fs.NmPerLongitude)
		}
	}

	var f float32 // the share of the segment flown by the end of this tick
	switch seg.Kind {
	case AirworkHeading:
		pr.Done++
		f = min(pr.Done/seg.Seconds, 1)
		fs.Heading = math.OffsetHeading(pr.Heading, seg.Turn*f)
	case AirworkCircle:
		f = min(aw.flyCircle(nav, wxs), 1)
	case AirworkTrack:
		hdg := nav.headingForTrack(seg.Track, wxs)
		nav.turnToward(hdg, airworkTrackBank, wxs)
		if math.HeadingDifference(fs.Heading, hdg) < 5 {
			pr.Done += fs.GS / 3600
		}
		f = min(pr.Done/seg.Distance, 1)
	case AirworkStayNear:
		if math.NMDistance2LL(fs.Position, aw.Center) > aw.Radius/4 {
			hdg := math.Heading2LL(fs.Position, aw.Center, fs.NmPerLongitude)
			nav.turnToward(math.TrueToMagnetic(hdg, fs.MagneticVariation), airworkStayNearBank, wxs)
		}
		pr.Done++
		f = min(pr.Done/seg.Seconds, 1)
	}

	s := seg.Profile.apply(f)
	fs.PrevAltitude = fs.Altitude
	fs.Altitude = pr.Altitude + seg.Climb*s
	fs.AltitudeRate = (fs.Altitude - fs.PrevAltitude) * 60
	if seg.IAS != 0 {
		fs.IAS = math.Lerp(s, pr.IAS, seg.IAS)
	}
	// Turns are flown at their rate without modeling the roll into them,
	// as holds are.
	fs.BankAngle = 0

	if f == 1 {
		aw.Segments = aw.Segments[1:]
		aw.Progress = AirworkProgress{}
	}
	return true
}

// airworkTrackBank and airworkCircleBank are the steepest banks flown to
// turn onto a ground track and to hold a circle over the ground; the latter
// is the 60° limit for a steep spiral. airworkStayNearBank is the gentler
// bank of a turn back toward the middle of the area.
const (
	airworkTrackBank    = 30
	airworkCircleBank   = 60
	airworkStayNearBank = 20
)

// flyCircle turns the aircraft to stay on the circle the current segment
// flies and returns the share of the segment's turn flown so far.
func (aw *Airwork) flyCircle(nav *Nav, wxs wx.Sample) float32 {
	fs := &nav.FlightState
	seg, pr := aw.Segments[0], &aw.Progress
	dir := math.Sign(seg.Turn)

	b := math.Heading2LL(pr.Center, fs.Position, fs.NmPerLongitude)
	pr.Done += dir * math.HeadingSignedTurn(pr.Bearing, b)
	pr.Bearing = b

	// Fly the tangent, angled in or out to get back onto the circle if the
	// aircraft has drifted off it.
	d := math.NMDistance2LL(pr.Center, fs.Position)
	correction := math.Clamp(60*(d-seg.Radius)/seg.Radius, -45, 45)
	track := math.OffsetHeading(b, dir*(90+correction))
	nav.turnToward(nav.headingForTrack(math.TrueToMagnetic(track, fs.MagneticVariation), wxs),
		airworkCircleBank, wxs)

	return pr.Done / math.Abs(seg.Turn)
}

// turnToward turns the aircraft the shorter way toward hdg, no faster
// than a turn at the given bank angle.
func (nav *Nav) turnToward(hdg math.MagneticHeading, bank float32, wxs wx.Sample) {
	rate := bankTurnRate(bank, nav.TAS(wxs.Temperature()))
	turn := math.Clamp(math.HeadingSignedTurn(nav.FlightState.Heading, hdg), -rate, rate)
	nav.FlightState.Heading = math.OffsetHeading(nav.FlightState.Heading, turn)
}

// bankTurnRate returns the rate of a coordinated turn at the given bank
// angle and true airspeed in knots, in degrees per second.
func bankTurnRate(bank, tas float32) float32 {
	return math.Degrees(9.81 * math.Tan(math.Radians(bank)) / (tas * 0.514444))
}

// bankTurnRadius returns the radius in nm of a coordinated turn at the
// given bank angle and speed in knots.
func bankTurnRadius(bank, speed float32) float32 {
	v := speed * 0.514444
	return v * v / (9.81 * math.Tan(math.Radians(bank))) / 1852
}

///////////////////////////////////////////////////////////////////////////
// Choosing maneuvers

// airworkLimits is the band of heights above the terrain a maneuver goes
// through and how far below and above where it starts it goes.
type airworkLimits struct {
	minAGL, maxAGL float32
	below, above   float32
}

// limits returns the airworkLimits of the maneuver for the given aircraft.
// The handbook has slow flight and stalls finished no lower than 1,500'
// above the ground in a single-engine airplane and 3,000' in a twin;
// accelerated stalls are 3,000' for all, and the ground reference
// maneuvers are flown between 600' and 1,000'.
func (m AirworkManeuver) limits(perf av.AircraftPerformance) airworkLimits {
	stalls := float32(util.Select(isSingleEnginePiston(perf), 1500, 3000))
	high := float32(av.MaxAltitude)
	switch m {
	case AirworkSlowFlight:
		return airworkLimits{minAGL: stalls, maxAGL: high, below: 300, above: 300}
	case AirworkPowerOffStall:
		return airworkLimits{minAGL: stalls, maxAGL: high, below: 550}
	case AirworkPowerOnStall:
		return airworkLimits{minAGL: stalls, maxAGL: high, below: 100, above: 300}
	case AirworkAcceleratedStall:
		return airworkLimits{minAGL: 3000, maxAGL: high, below: 250}
	case AirworkSteepTurns:
		return airworkLimits{minAGL: 1500, maxAGL: high, below: 100, above: 100}
	case AirworkSteepSpiral:
		// Three turns lose 1,200-2,000'.
		return airworkLimits{minAGL: 1500, maxAGL: high, below: 2000}
	case AirworkChandelle:
		return airworkLimits{minAGL: 1500, maxAGL: high, above: 900}
	case AirworkLazyEights:
		return airworkLimits{minAGL: 1500, maxAGL: high, below: 50, above: 450}
	case AirworkRectangularCourse, AirworkSTurns, AirworkTurnsAroundPoint:
		return airworkLimits{minAGL: 600, maxAGL: 1000, below: 50, above: 50}
	case AirworkEmergencyDescent:
		return airworkLimits{minAGL: 2000, maxAGL: high, below: 1500}
	case AirworkEngineFailure:
		// It glides down to 500' over a field before going around.
		return airworkLimits{minAGL: 500, maxAGL: 4000, below: 1500, above: 100}
	default:
		return airworkLimits{minAGL: 1000, maxAGL: high}
	}
}

// weight is how often the maneuver is practiced, relative to the others.
func (m AirworkManeuver) weight() float32 {
	switch m {
	case AirworkPowerOffStall, AirworkPowerOnStall, AirworkSteepTurns:
		return 3
	case AirworkSlowFlight, AirworkChandelle, AirworkSTurns, AirworkTurnsAroundPoint, AirworkEngineFailure:
		return 2
	default:
		return 1
	}
}

// practicedIn reports whether the maneuver is practiced in the given
// aircraft. Steep spirals, chandelles, lazy eights and the ground reference
// maneuvers are single-engine tasks, as are simulated engine failures to a
// field; training for those is in light single-engine pistons.
func (m AirworkManeuver) practicedIn(perf av.AircraftPerformance) bool {
	switch m {
	case AirworkTurns, AirworkSlowFlight, AirworkPowerOffStall, AirworkPowerOnStall, AirworkAcceleratedStall,
		AirworkSteepTurns, AirworkEmergencyDescent:
		return true
	default:
		return isSingleEnginePiston(perf)
	}
}

// AirworkAreaScale returns how much larger than a light single's an area
// an aircraft needs for airwork, since faster aircraft cover more ground
// and turn wider.
func AirworkAreaScale(perf av.AircraftPerformance) float32 {
	return max(1, perf.Speed.CruiseTAS/150)
}

func isSingleEnginePiston(perf av.AircraftPerformance) bool {
	return perf.Engine.AircraftType == "P" && perf.Engine.Number <= 1
}

// entryWindow returns the lowest and highest altitudes at which the
// maneuver can start, given the airwork's altitude range and the terrain;
// it returns false if there are none.
func (aw *Airwork) entryWindow(l airworkLimits) (float32, float32, bool) {
	lo := max(aw.Terrain+l.minAGL, aw.AltRange[0]) + l.below
	hi := min(aw.Terrain+l.maxAGL, aw.AltRange[1]) - l.above
	return lo, hi, lo <= hi
}

// startManeuver picks the next maneuver and lays out its segments.
func (aw *Airwork) startManeuver(nav *Nav, wxs wx.Sample) {
	aw.planManeuver(aw.chooseManeuver(nav), nav, wxs)
}

// chooseManeuver picks a maneuver that fits in the area's altitudes. The
// choice favors the ones that need less of a climb or descent to get to
// and the one just flown, since students repeat maneuvers. If nothing
// fits, it returns AirworkTurns.
func (aw *Airwork) chooseManeuver(nav *Nav) AirworkManeuver {
	alt := nav.FlightState.Altitude
	var candidates []AirworkManeuver
	for m := range NumAirworkManeuvers {
		if _, _, ok := aw.entryWindow(m.limits(nav.Perf)); ok && m.practicedIn(nav.Perf) {
			candidates = append(candidates, m)
		}
	}
	m, ok := rand.SampleWeighted(nav.Rand, candidates, func(m AirworkManeuver) float32 {
		lo, hi, _ := aw.entryWindow(m.limits(nav.Perf))
		w := m.weight() / (1 + max(lo-alt, alt-hi, 0)/1000)
		return util.Select(m == aw.Maneuver, 3*w, w)
	})
	return util.Select(ok, m, AirworkTurns)
}

// planManeuver lays out the segments that fly a maneuver: first back
// toward the middle of the area if the aircraft has wandered, then up or
// down to where the maneuver starts, then the maneuver.
func (aw *Airwork) planManeuver(m AirworkManeuver, nav *Nav, wxs wx.Sample) {
	fs := &nav.FlightState
	l := m.limits(nav.Perf)
	floor := max(aw.Terrain+l.minAGL, aw.AltRange[0])
	ceiling := min(aw.Terrain+l.maxAGL, aw.AltRange[1])
	lo, hi, ok := aw.entryWindow(l)
	if !ok {
		// There's no room for it in the area's altitudes. It still keeps
		// its height above the terrain, flying above them if it must.
		ceiling = max(floor, aw.AltRange[1])
		lo, hi = floor, ceiling
	}

	// Start where it is if it can, otherwise a little inside the window.
	entry := fs.Altitude
	if entry < lo {
		entry = min(hi, lo+nav.Rand.Float32Range(0, 300))
	} else if entry > hi {
		entry = max(lo, hi-nav.Rand.Float32Range(0, 300))
	}

	p := airworkPlanner{
		aw:      aw,
		nav:     nav,
		r:       nav.Rand,
		temp:    wxs.Temperature(),
		wind:    wxs.WindSample,
		alt:     fs.Altitude,
		ias:     fs.IAS,
		pos:     fs.Position,
		floor:   floor,
		ceiling: ceiling,
	}
	p.reposition()
	p.climbOrDescend(entry)
	p.build(m)
	p.pause()

	aw.Maneuver = m
	aw.Segments = p.segs
	aw.Progress = AirworkProgress{}
}

///////////////////////////////////////////////////////////////////////////
// Laying out maneuvers

// airworkPlanner lays out the segments of a maneuver. It tracks the
// altitude and speed the aircraft will have at the end of the segments so
// far, which the later ones are worked out from, and roughly where it will
// be when the maneuver starts. floor and ceiling are the lowest and highest
// altitudes the maneuver may reach.
type airworkPlanner struct {
	aw             *Airwork
	nav            *Nav
	r              *rand.Rand
	temp           av.Temperature
	wind           wx.WindSample
	alt, ias       float32
	pos            math.Point2LL
	floor, ceiling float32
	segs           []AirworkSegment
}

func (p *airworkPlanner) add(s AirworkSegment) {
	p.alt += s.Climb
	if s.IAS != 0 {
		p.ias = s.IAS
	}
	p.segs = append(p.segs, s)
}

// fly adds a segment that turns steadily through turn degrees, or flies
// straight, for the given number of seconds.
func (p *airworkPlanner) fly(seconds, turn, climb, ias float32, profile AirworkProfile) {
	p.add(AirworkSegment{Kind: AirworkHeading, Seconds: max(1, seconds), Turn: turn, Climb: climb, IAS: ias,
		Profile: profile})
}

// turn adds a segment that turns through turn degrees at the given bank.
func (p *airworkPlanner) turn(turn, bank, climb, ias float32, profile AirworkProfile) {
	end := util.Select(ias == 0, p.ias, ias)
	rate := bankTurnRate(bank, av.IASToTAS((p.ias+end)/2, p.alt+climb/2, p.temp))
	p.fly(math.Abs(turn)/rate, turn, climb, ias, profile)
}

// circle adds a segment that circles a point on the ground through turn
// degrees, with the radius that has the bank reach the given angle where
// it is steepest, heading downwind.
func (p *airworkPlanner) circle(turn, bank, climb float32) {
	radius := bankTurnRadius(bank, p.tas(p.ias)+p.wind.WindSpeed())
	p.add(AirworkSegment{Kind: AirworkCircle, Turn: turn, Radius: radius, Climb: climb})
}

// stayNear adds a segment that flies for the given number of seconds
// without going far from the middle of the area.
func (p *airworkPlanner) stayNear(seconds, climb, ias float32, profile AirworkProfile) {
	p.add(AirworkSegment{Kind: AirworkStayNear, Seconds: max(1, seconds), Climb: climb, IAS: ias,
		Profile: profile})
}

// track adds a segment that flies a ground track for dist nm.
func (p *airworkPlanner) track(track math.MagneticHeading, dist float32) {
	p.add(AirworkSegment{Kind: AirworkTrack, Track: track, Distance: dist})
}

func (p *airworkPlanner) tas(ias float32) float32 {
	return av.IASToTAS(ias, p.alt, p.temp)
}

// direction returns 1 for a turn to the right or -1 for one to the left.
func (p *airworkPlanner) direction() float32 {
	return util.Select(p.r.Bool(), float32(1), -1)
}

// downwind returns the magnetic course downwind. Ground reference
// maneuvers are entered downwind; in a calm, any direction does.
func (p *airworkPlanner) downwind() math.MagneticHeading {
	fs := &p.nav.FlightState
	if p.wind.WindSpeed() < 3 {
		return math.MagneticHeading(p.r.Float32Range(0, 360))
	}
	return math.TrueToMagnetic(math.OppositeHeading(p.wind.WindDirection()), fs.MagneticVariation)
}

// The speeds the maneuvers are flown at, from the stall speed in the
// ratios that a light airplane's handbook speeds have to it. A Cessna 172
// stalls at 48 knots clean and approaches at 65, climbs at 62 for the best
// angle and 74 for the best rate, glides best at 68, and lifts off and
// flies slow flight at about 55.
func (p *airworkPlanner) vs() float32 { return p.nav.Perf.Speed.Min }

func (p *airworkPlanner) cruise() float32 {
	ias := av.TASToIAS(p.nav.Perf.Speed.CruiseTAS, p.alt, p.temp)
	return util.Select(p.alt < 10000, min(ias, 250), ias)
}

// va returns the maneuvering speed: the stall speed times the square root
// of the normal category's load limit of 3.8.
func (p *airworkPlanner) va() float32 { return min(p.cruise(), 1.95*p.vs()) }

// climbOrDescend changes altitude to alt in gentle turns, as pilots do to
// stay in the practice area: climbing at the best rate of climb speed or
// descending at cruise speed.
func (p *airworkPlanner) climbOrDescend(alt float32) {
	climb := alt - p.alt
	if math.Abs(climb) < 50 {
		return
	}
	rate, ias := 0.8*p.nav.Perf.Rate.Climb, 1.5*p.vs()
	if climb < 0 {
		rate, ias = 0.7*p.nav.Perf.Rate.Descent, p.cruise()
	}
	seconds := math.Abs(climb) / rate * 60
	p.fly(seconds, p.direction()*bankTurnRate(15, p.tas(ias))*seconds, climb, ias, AirworkLinear)
}

// changeSpeed accelerates or slows to ias in level flight.
func (p *airworkPlanner) changeSpeed(ias float32) {
	d := ias - p.ias
	rate := util.Select(d > 0, p.nav.Perf.Rate.Accelerate/4, p.nav.Perf.Rate.Decelerate/2)
	if math.Abs(d) >= 1 {
		p.stayNear(math.Abs(d)/rate, 0, ias, AirworkLinear)
	}
}

// recover returns to alt, climbing at the best rate of climb speed if it
// is below it, and then to cruise.
func (p *airworkPlanner) recover(alt float32) {
	if alt > p.alt {
		p.stayNear((alt-p.alt)/(0.8*p.nav.Perf.Rate.Climb)*60, alt-p.alt, 1.5*p.vs(), AirworkLinear)
	} else {
		p.climbOrDescend(alt)
	}
	p.changeSpeed(p.cruise())
}

// reposition flies back toward the middle of the area if the aircraft has
// wandered from it, so that the next maneuver stays in it.
func (p *airworkPlanner) reposition() {
	fs := &p.nav.FlightState
	d := math.NMDistance2LL(p.pos, p.aw.Center)
	if d < p.aw.Radius/2 {
		return
	}
	p.changeSpeed(p.cruise())
	hdg := math.TrueToMagnetic(math.Heading2LL(p.pos, p.aw.Center, fs.NmPerLongitude), fs.MagneticVariation)
	p.track(hdg, d-p.r.Float32Range(0, p.aw.Radius/4))
	p.pos = p.aw.Center
}

// towardCenter returns 1 if the middle of the area is to the right of the
// given course from where the maneuver starts and -1 if it is to the left,
// for maneuvers that work their way to one side.
func (p *airworkPlanner) towardCenter(course math.MagneticHeading) float32 {
	fs := &p.nav.FlightState
	hdg := math.TrueToMagnetic(math.Heading2LL(p.pos, p.aw.Center, fs.NmPerLongitude), fs.MagneticVariation)
	return util.Select(math.HeadingSignedTurn(course, hdg) > 0, float32(1), -1)
}

// pause flies level for a bit after a maneuver, as an instructor and
// student talk it over.
func (p *airworkPlanner) pause() {
	p.stayNear(p.r.Float32Range(10, 45), 0, 0, AirworkLinear)
}

// clearingTurns are the two 90° turns, one each way, that a pilot makes to
// look for traffic before a maneuver.
func (p *airworkPlanner) clearingTurns() {
	dir := p.direction()
	p.turn(dir*90, 30, 0, 0, AirworkLinear)
	p.turn(-dir*90, 30, 0, 0, AirworkLinear)
}

func (p *airworkPlanner) build(m AirworkManeuver) {
	switch m {
	case AirworkSlowFlight:
		p.slowFlight()
	case AirworkPowerOffStall:
		p.powerOffStall()
	case AirworkPowerOnStall:
		p.powerOnStall()
	case AirworkAcceleratedStall:
		p.acceleratedStall()
	case AirworkSteepTurns:
		p.steepTurns()
	case AirworkSteepSpiral:
		p.steepSpiral()
	case AirworkChandelle:
		p.chandelle()
	case AirworkLazyEights:
		p.lazyEights()
	case AirworkRectangularCourse:
		p.rectangularCourse()
	case AirworkSTurns:
		p.sTurns()
	case AirworkTurnsAroundPoint:
		p.turnsAroundPoint()
	case AirworkEmergencyDescent:
		p.emergencyDescent()
	case AirworkEngineFailure:
		p.engineFailure()
	default:
		p.turns()
	}
}

// turns climbs or descends 500-1,000' in a turn and then makes a level
// 360.
func (p *airworkPlanner) turns() {
	climb := p.r.Float32Range(500, 1000)
	if p.alt+climb > p.ceiling || (p.alt-climb >= p.floor && p.r.Bool()) {
		climb = -climb
	}
	p.climbOrDescend(math.Clamp(p.alt+climb, p.floor, p.ceiling))
	p.turn(p.direction()*360, 30, 0, p.cruise(), AirworkLinear)
}

// slowFlight slows to just above the stall and then flies straight and
// level, turns, climbs and descends there before speeding back up.
func (p *airworkPlanner) slowFlight() {
	entry := p.alt
	p.clearingTurns()
	p.changeSpeed(1.1 * p.vs())
	for range p.r.IntRange(3, 5) {
		switch p.r.Intn(3) {
		case 0:
			p.fly(p.r.Float32Range(20, 40), 0, 0, 0, AirworkLinear)
		case 1:
			p.turn(p.direction()*p.r.Float32Range(90, 270), 20, 0, 0, AirworkLinear)
		default:
			// A climb or descent of a few hundred feet, back toward where
			// it started; there is little power to spare for climbing.
			climb, rate := p.r.Float32Range(150, 300), float32(300)
			if p.alt > entry || (p.alt == entry && p.r.Bool()) {
				climb, rate = -climb, 500
			}
			seconds := math.Abs(climb) / rate * 60
			turn := util.Select(p.r.Bool(), p.direction()*bankTurnRate(15, p.tas(p.ias))*seconds, 0)
			p.fly(seconds, turn, climb, 0, AirworkEaseInOut)
		}
	}
	p.recover(entry)
}

// powerOffStall slows to the approach speed, sets up a descent like one on
// final, and raises the nose until it stalls, straight ahead or in a turn
// of up to 20° bank. The recovery is a go-around back up to where it
// started.
func (p *airworkPlanner) powerOffStall() {
	entry, vs := p.alt, p.vs()
	p.clearingTurns()
	p.changeSpeed(1.3 * vs)
	seconds := p.r.Float32Range(15, 30)
	p.fly(seconds, 0, -500*seconds/60, 0, AirworkLinear)
	p.stall(p.r.Float32Range(6, 10), -40, AirworkEaseOut)
	p.recover(entry)
}

// powerOnStall slows to lift-off speed and then, at takeoff power, pitches
// up as on a departure until it stalls, straight ahead or in a turn of up
// to 20° bank.
func (p *airworkPlanner) powerOnStall() {
	entry, vs := p.alt, p.vs()
	p.clearingTurns()
	p.changeSpeed(1.1 * vs)
	seconds := p.r.Float32Range(12, 18)
	p.stall(seconds, 0.9*p.nav.Perf.Rate.Climb*seconds/60, AirworkLinear)
	p.recover(entry)
}

// stall slows to the stall over the given time while climbing or
// descending, and then recovers, losing up to the 230' the 172's handbook
// allows while the nose is lowered and the wings leveled.
func (p *airworkPlanner) stall(seconds, climb float32, profile AirworkProfile) {
	vs := p.vs()
	turn := float32(0)
	if p.r.Bool() {
		turn = p.direction() * bankTurnRate(20, p.tas(vs)) * seconds
	}
	p.fly(seconds, turn, climb, vs, profile)
	p.stallBreak(p.r.Float32Range(100, 230), 1.25*vs)
}

// stallBreak is the recovery from a stall: lowering the nose, adding
// power and leveling the wings, losing the given altitude at an average
// of 1,500 feet per minute while picking up speed to ias.
func (p *airworkPlanner) stallBreak(loss, ias float32) {
	p.fly(max(5, loss/25), 0, -loss, ias, AirworkEaseInOut)
}

// acceleratedStall rolls into a 45° bank at a speed well below
// maneuvering speed and pulls until it stalls.
func (p *airworkPlanner) acceleratedStall() {
	entry, vs := p.alt, p.vs()
	p.clearingTurns()
	p.changeSpeed(1.4 * vs)
	// The stall speed in a 45° bank is 1.19 times that wings-level.
	p.turn(p.direction()*p.r.Float32Range(60, 120), 45, 0, 1.2*vs, AirworkLinear)
	p.stallBreak(p.r.Float32Range(100, 200), 1.3*vs)
	p.recover(entry)
}

// steepTurns makes a 360 at a 45° bank, or the commercial standard's 50°,
// at maneuvering speed, and usually another the other way. Altitude has to
// stay within 100', which leaves room to wander some.
func (p *airworkPlanner) steepTurns() {
	entry := p.alt
	bank := util.Select(p.r.Bool(), float32(45), 50)
	p.clearingTurns()
	p.changeSpeed(p.va())
	dir := p.direction()
	for i := range util.Select(p.r.Float32() < .8, 2, 1) {
		if i > 0 {
			p.fly(p.r.Float32Range(2, 10), 0, entry-p.alt, 0, AirworkEaseInOut)
		}
		p.turn(dir*180, bank, entry+p.r.Float32Range(-80, 80)-p.alt, 0, AirworkEaseInOut)
		p.turn(dir*180, bank, entry+p.r.Float32Range(-30, 30)-p.alt, 0, AirworkEaseInOut)
		dir = -dir
	}
	p.changeSpeed(p.cruise())
}

// steepSpiral glides with the power at idle through three or four turns
// around a point on the ground, banked up to 50° where it is steepest. The
// descent in the turns is about 1,200 feet per minute for a light single,
// 400-700' a turn.
func (p *airworkPlanner) steepSpiral() {
	p.clearingTurns()
	p.changeSpeed(1.4 * p.vs())

	turns := float32(util.Select(p.r.Float32() < .7, 3, 4))
	radius := bankTurnRadius(50, p.tas(p.ias)+p.wind.WindSpeed())
	seconds := turns * 2 * math.Pi * radius / p.tas(p.ias) * 3600
	loss := 1.2 * p.nav.Perf.Rate.Descent * seconds / 60
	p.circle(p.direction()*360*turns, 50, -min(loss, p.alt-p.floor))
	p.changeSpeed(p.cruise())
}

// chandelle is a maximum performance climbing 180: a 30° bank and a
// steadily rising nose through the first 90°, then rolling out through the
// second, finishing just above the stall. A 172 gains about 500'.
func (p *airworkPlanner) chandelle() {
	vs, va := p.vs(), p.va()
	p.clearingTurns()
	p.changeSpeed(va)

	mid, end := math.Lerp(0.4, va, 1.1*vs), 1.1*vs
	t1 := 90 / bankTurnRate(30, p.tas((va+mid)/2))
	t2 := 90 / bankTurnRate(15, p.tas((mid+end)/2))
	gain := min(climbFromSpeed(va, end)+0.5*p.nav.Perf.Rate.Climb*(t1+t2)/60, p.ceiling-p.alt)
	// The climb rate builds through the first half and tapers off through
	// the second; splitting the gain by time has them meet at the middle.
	dir := p.direction()
	p.fly(t1, dir*90, gain*t1/(t1+t2), mid, AirworkEaseIn)
	p.fly(t2, dir*90, gain*t2/(t1+t2), end, AirworkEaseOut)
	p.fly(2, 0, 0, 0, AirworkLinear)
	p.changeSpeed(p.cruise())
}

// lazyEights alternates 180° turns, each climbing to the slowest speed and
// a 30° bank at its 90° point and descending back to where it started by
// the 180° point.
func (p *airworkPlanner) lazyEights() {
	vs, va := p.vs(), p.va()
	p.clearingTurns()
	p.changeSpeed(va)

	slow := 1.15 * vs
	gain := min(climbFromSpeed(va, slow), p.ceiling-p.alt)
	dir := p.direction()
	for range util.Select(p.r.Bool(), 2, 4) {
		p.turn(dir*90, 20, gain, slow, AirworkEaseInOut)
		p.turn(dir*90, 20, -gain, va, AirworkEaseInOut)
		dir = -dir
	}
	p.changeSpeed(p.cruise())
}

// climbFromSpeed returns the altitude in feet that slowing from one
// speed to another in knots gains.
func climbFromSpeed(from, to float32) float32 {
	const k = 0.514444
	return (from*k*from*k - to*k*to*k) / (2 * 9.81) / 0.3048
}

// groundReferenceSpeed is the speed for the ground reference maneuvers:
// no faster than maneuvering speed, and the 90-100 knots schools use.
func (p *airworkPlanner) groundReferenceSpeed() float32 {
	return min(p.va(), p.cruise(), 100)
}

// rectangularCourse flies around a field of about a mile by half a mile,
// half a mile or so outside its edges, correcting for the wind so that each
// leg stays parallel to the edge. It enters at 45° to the downwind leg.
func (p *airworkPlanner) rectangularCourse() {
	p.changeSpeed(p.groundReferenceSpeed())
	downwind := p.downwind()
	dir := p.towardCenter(downwind)
	long, short := p.r.Float32Range(1.5, 2), p.r.Float32Range(1, 1.5)
	p.track(math.OffsetHeading(downwind, -dir*45), 0.5)
	for range p.r.IntRange(1, 2) {
		for i, d := range []float32{long, short, long, short} {
			p.track(math.OffsetHeading(downwind, dir*90*float32(i)), d)
		}
	}
	p.changeSpeed(p.cruise())
}

// sTurns crosses a road at right angles to the wind, heading downwind, and
// flies half circles of the same radius over the ground on alternate sides
// of it. The bank is steepest, 30-40°, at the first crossing.
func (p *airworkPlanner) sTurns() {
	p.changeSpeed(p.groundReferenceSpeed())
	downwind := p.downwind()
	p.track(downwind, p.r.Float32Range(0.5, 1))
	bank, dir := p.r.Float32Range(30, 40), p.towardCenter(downwind)
	for range p.r.IntRange(4, 6) {
		p.circle(dir*180, bank, 0)
		dir = -dir
	}
	p.changeSpeed(p.cruise())
}

// turnsAroundPoint circles a point on the ground two or three times at the
// same radius, entering downwind where the bank is steepest.
func (p *airworkPlanner) turnsAroundPoint() {
	p.changeSpeed(p.groundReferenceSpeed())
	p.track(p.downwind(), p.r.Float32Range(0.5, 1))
	p.circle(p.direction()*360*float32(p.r.IntRange(2, 3)), p.r.Float32Range(30, 45), 0)
	p.changeSpeed(p.cruise())
}

// emergencyDescent descends 1,500-2,500' as fast as it can, at idle in a
// 30-45° bank, and levels off.
func (p *airworkPlanner) emergencyDescent() {
	p.clearingTurns()
	ias := p.cruise()
	loss := min(p.r.Float32Range(1500, 2500), p.alt-p.floor)
	seconds := loss / (2 * p.nav.Perf.Rate.Descent) * 60
	turn := p.direction() * bankTurnRate(p.r.Float32Range(30, 45), p.tas(ias)) * seconds
	p.fly(seconds, turn, -loss, ias, AirworkLinear)
	p.changeSpeed(p.cruise())
}

// engineFailure is the instructor pulling the power: the pilot slows to
// the best glide speed and glides toward a field, circles down over it
// and turns final, and goes around 500' over it once it is clear that it
// would have made it.
func (p *airworkPlanner) engineFailure() {
	entry, vg := p.alt, 1.4*p.vs()
	p.fly(max(0, p.ias-vg)/(p.nav.Perf.Rate.Decelerate/2), 0, p.r.Float32Range(0, 100), vg, AirworkEaseOut)

	// Gliding about 800 feet per minute in a light single.
	sink := 0.8 * p.nav.Perf.Rate.Descent
	glide := func(loss, turn float32) {
		p.fly(loss/sink*60, turn, -loss, 0, AirworkLinear)
	}
	loss := p.alt - p.floor
	toField, final := 0.3*loss, min(0.2*loss, 500)
	p.stayNear(toField/sink*60, -toField, 0, AirworkLinear)
	glide(loss-toField-final, p.direction()*p.r.Float32Range(270, 450))
	glide(final, 0)
	p.recover(min(entry, p.floor+p.r.Float32Range(1000, 1500)))
}

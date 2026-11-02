// nav/nav.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"errors"
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

// Errors used by the nav package
var (
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrFixIsTooFarAway              = errors.New("Fix is too far away")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidFix                   = errors.New("Invalid fix")
	ErrNotClearedForApproach        = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute               = errors.New("Aircraft is not currently flying its assigned route")
	ErrUnableCommand                = errors.New("Unable")
	ErrUnknownApproach              = errors.New("Unknown approach")
)

// Nav flies one aircraft: its Instructions say what it has been told to do and FlightState where
// that has gotten it. Pointers are used for optional values; nil -> unset/unspecified.
type Nav struct {
	Instructions             // Controller instructions / filed route
	FlightState  FlightState // Physical aircraft state (position, heading, etc.)
	Perf         av.AircraftPerformance
	Airwork      *Airwork
	Prespawn     bool

	ExpectedDirectFix string

	FinalAltitude float32

	// RouteAltitudeActions is set at creation time if the route includes
	// /c, /d, /cvs, or /dvs altitude actions at any of its waypoints. If it
	// does, those actions govern the aircraft's altitude and DepartOnCourse
	// never climbs it to its cruise altitude. (Waypoints are dropped from the
	// route as they are passed, so this must be recorded up front.)
	RouteAltitudeActions bool

	PendingWaypointActionEvents []av.WaypointActionEvent

	Delays PilotDelays
	Rand   *rand.Rand

	// Session recording
	recorder      Recorder
	recordStarted bool
	recordDepth   int     // in progress count
	lookups       Lookups // av.db information source (or proxy during reply)
}

// Instructions is what the aircraft has been told to do, by its controllers and by its route, and
// how far it has gotten with it. This is what's changed by controller instructions and what is
// rolled back on a correction.
type Instructions struct {
	Altitude Altitude
	Speed    Speed
	Heading  Heading
	Approach Approach
	// Requests is what the pilot has decided to ask the controller for and
	// the sim hasn't yet taken; see TakePilotRequests.
	Requests PilotRequests

	FixAssignments map[string]FixAssignment

	// DeferredNavHeading stores a heading/direct fix assignment from the
	// controller that the pilot has not yet started to follow.  Note that
	// only a single such assignment is stored; for example, if the
	// controller issues a first heading and then a second shortly
	// afterward, before the first has been followed, it's fine for the
	// second to override it.
	DeferredNavHeading *DeferredNavHeading

	Waypoints av.WaypointArray

	// PassedWaypoint is the route fix behind the aircraft on the leg it is
	// flying: the last one it passed, or the start of a leg it joined
	// partway along.
	PassedWaypoint av.Waypoint
}

// PilotRequests are what the pilot has decided to ask the controller for, or
// to do on their own, since the sim last took them.
type PilotRequests struct {
	ApproachClearance bool // close to the approach with no clearance
	GoAround          bool // reached the FAF with no clearance
	Vectors           bool // overshot the localizer
	Altitude          bool // vectored off a STAR or approach with no altitude
}

// TakePilotRequests returns what the pilot wants to ask for or do and clears
// it, so that each request is acted on once.
func (nav *Nav) TakePilotRequests() PilotRequests {
	defer nav.record("TakePilotRequests")()
	r := nav.Requests
	nav.Requests = PilotRequests{}
	return r
}

type UpdateResult struct {
	PassedWaypoint *av.Waypoint
	ActionEvents   []av.WaypointActionEvent
}

// TakeSnapshot returns a copy of the aircraft's instructions for
// RestoreSnapshot to roll back to.
func (nav *Nav) TakeSnapshot() Instructions {
	return deep.MustCopy(nav.Instructions)
}

// RestoreSnapshot puts back the instructions TakeSnapshot returned, undoing what the aircraft has
// been told since.
func (nav *Nav) RestoreSnapshot(snap Instructions) {
	defer nav.record("RestoreSnapshot", snap)()
	nav.Instructions = snap
}

// DelayRange is the range of times from which a pilot's delay in starting
// to follow an instruction is drawn.
type DelayRange struct {
	Min, Max time.Duration
}

// PilotDelays holds the ranges of pilot delays for each kind of
// instruction and for each situation the pilot may be in when it is issued.
type PilotDelays struct {
	Altitude               DelayRange
	AltitudeAfterSpeed     DelayRange
	HeadingApproachCleared DelayRange
	HeadingFromHeading     DelayRange
	HeadingFromLNAV        DelayRange
	DirectExpected         DelayRange
	DirectFromLNAV         DelayRange
	DirectFromHeading      DelayRange
	OnCourse               DelayRange
	// PendingApproachCleared caps the delay of a heading or direct that
	// is still pending when the aircraft is cleared for an approach.
	PendingApproachCleared DelayRange
}

// BriefPilotDelays and RealisticPilotDelays are the two sets of pilot delays
// offered when a sim is created: short ones that keep traffic moving, and
// longer ones meant to match how long real pilots take to respond.
var (
	BriefPilotDelays = PilotDelays{
		Altitude:               DelayRange{3 * time.Second, 4 * time.Second},
		AltitudeAfterSpeed:     DelayRange{3 * time.Second, 4 * time.Second},
		HeadingApproachCleared: DelayRange{2 * time.Second, 3 * time.Second},
		HeadingFromHeading:     DelayRange{2 * time.Second, 3 * time.Second},
		HeadingFromLNAV:        DelayRange{3 * time.Second, 4 * time.Second},
		DirectExpected:         DelayRange{3 * time.Second, 4 * time.Second},
		DirectFromLNAV:         DelayRange{4 * time.Second, 7 * time.Second},
		DirectFromHeading:      DelayRange{8 * time.Second, 13 * time.Second},
		OnCourse:               DelayRange{8 * time.Second, 13 * time.Second},
		PendingApproachCleared: DelayRange{1 * time.Second, 3 * time.Second},
	}

	RealisticPilotDelays = PilotDelays{
		Altitude:               DelayRange{9500 * time.Millisecond, 11500 * time.Millisecond},
		AltitudeAfterSpeed:     DelayRange{3 * time.Second, 6 * time.Second},
		HeadingApproachCleared: DelayRange{10 * time.Second, 12 * time.Second},
		HeadingFromHeading:     DelayRange{9 * time.Second, 11 * time.Second},
		HeadingFromLNAV:        DelayRange{10 * time.Second, 12 * time.Second},
		DirectExpected:         DelayRange{9 * time.Second, 12 * time.Second},
		DirectFromLNAV:         DelayRange{9 * time.Second, 12 * time.Second},
		DirectFromHeading:      DelayRange{10 * time.Second, 12 * time.Second},
		OnCourse:               DelayRange{9 * time.Second, 11 * time.Second},
		PendingApproachCleared: DelayRange{8500 * time.Millisecond, 10500 * time.Millisecond},
	}
)

func (nav *Nav) drawDelay(r DelayRange) time.Duration {
	return nav.Rand.DurationRange(r.Min, r.Max)
}

func MakeArrivalNav(callsign av.ADSBCallsign, arr *av.Arrival, depAirport, arrAirport av.ICAOAirportCode,
	cruiseAlt int, perf av.AircraftPerformance, delays PilotDelays, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time, r *rand.Rand,
	lg *log.Logger) *Nav {
	if nav := makeNav(callsign, depAirport, arrAirport, cruiseAlt, perf, delays, arr.Waypoints,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, r, lg); nav != nil {
		if !arr.SpeedRestriction.IsZero() {
			sr := arr.SpeedRestriction
			nav.Speed.Restriction = &sr
		}
		if arr.AssignedAltitude > 0 {
			// Descend to the assigned altitude but then hold that until
			// either DVS or further descents are given.
			nav.setAssignedAltitude(arr.AssignedAltitude)
		}
		if arr.ClearedAltitude > 0 {
			nav.Altitude.Cleared = &ClearedAltitude{Altitude: arr.ClearedAltitude, IsFloor: true}
		}

		alt := nav.startAirborne(arr.InitialAltitudes, arr.InitialSpeed, model, simTime)
		nav.FinalAltitude = max(nav.FinalAltitude, alt)
		return nav
	}
	return nil
}

func MakeDepartureNav(callsign av.ADSBCallsign, depAirport, arrAirport av.ICAOAirportCode, cruiseAlt int,
	perf av.AircraftPerformance, delays PilotDelays, assignedAlt, clearedAlt int, wp av.WaypointArray,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, r *rand.Rand,
	lg *log.Logger) *Nav {
	if nav := makeNav(callsign, depAirport, arrAirport, cruiseAlt, perf, delays, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, r, lg); nav != nil {
		if assignedAlt != 0 {
			nav.setAssignedAltitude(float32(min(assignedAlt, cruiseAlt)))
		} else {
			nav.Altitude.Cleared = &ClearedAltitude{Altitude: float32(min(clearedAlt, cruiseAlt))}
		}
		nav.FlightState.InitialDepartureClimb = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func MakeOverflightNav(callsign av.ADSBCallsign, of *av.Overflight, depAirport, arrAirport av.ICAOAirportCode,
	cruiseAlt int, perf av.AircraftPerformance, delays PilotDelays, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time, r *rand.Rand,
	lg *log.Logger) *Nav {
	if nav := makeNav(callsign, depAirport, arrAirport, cruiseAlt, perf, delays, of.Waypoints,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, r, lg); nav != nil {
		if !of.SpeedRestriction.IsZero() {
			sr := of.SpeedRestriction
			nav.Speed.Restriction = &sr
		}
		if of.AssignedAltitude > 0 {
			nav.setAssignedAltitude(of.AssignedAltitude)
		}
		if !of.AssignedSpeed.IsZero() {
			sr := av.MakeAtAirspeedRestriction(of.AssignedSpeed)
			nav.Speed.Assigned = &sr
		}

		nav.startAirborne(of.InitialAltitudes, of.InitialSpeed, model, simTime)
		return nav
	}
	return nil
}

// startAirborne puts an aircraft that spawns in flight at one of its initial
// altitudes, flying its initial speed, and returns that altitude.
func (nav *Nav) startAirborne(initialAltitudes []int, initialSpeed av.Airspeed, model *wx.Model, simTime Time) float32 {
	alt := float32(rand.SampleSlice(nav.Rand, initialAltitudes))
	nav.FlightState.Altitude = alt
	wxs := model.Lookup(nav.FlightState.Position, alt, simTime.Time())
	nav.FlightState.IAS = min(initialSpeed.IAS(alt), nav.maxIAS(wxs.Temperature()))
	// This won't be quite right but it's better than leaving GS to be 0 for
	// the first nav update tick, which leads to various Inf and NaN cases.
	nav.FlightState.GS = nav.FlightState.IAS
	return alt
}

// makeNav seeds the aircraft's random number generator from r, so that a
// sim's aircraft draw repeatable numbers when its own generator is seeded.
func makeNav(callsign av.ADSBCallsign, depAirport, arrAirport av.ICAOAirportCode, cruiseAlt int,
	perf av.AircraftPerformance, delays PilotDelays, wp av.WaypointArray, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, r *rand.Rand, lg *log.Logger) *Nav {
	nav := &Nav{
		Instructions: Instructions{
			FixAssignments: make(map[string]FixAssignment),
		},
		Perf:          perf,
		FinalAltitude: float32(cruiseAlt),
		Delays:        delays,
		Rand:          rand.New(r.Uint64()),
	}

	// Clone the provided waypoints so that any local modifications we make don't pollute the
	// waypoints stored for the scenario. Add a small buffer for the destination airport waypoint.
	nav.Waypoints = append(wp.Clone(), av.Waypoint{})

	av.RandomizeRoute(nav.Waypoints, nav.Rand, randomizeAltitudeRange, nav.Perf, nmPerLongitude,
		magneticVariation, arrAirport, lg)

	nav.RouteAltitudeActions = slices.ContainsFunc(nav.Waypoints,
		func(wp av.Waypoint) bool { return wp.HasAltitudeActions() })

	// VFR routes end at their SequenceVFRLanding waypoint; truncate any
	// trailing slots (including the +1 buffer allocated above).
	seqIdx := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.SequenceVFRLanding() })
	if seqIdx != -1 {
		nav.Waypoints = nav.Waypoints[:seqIdx+1]
	}

	nav.FlightState = FlightState{
		MagneticVariation: magneticVariation,
		NmPerLongitude:    nmPerLongitude,
		Position:          nav.Waypoints[0].Location,
	}
	if h, ok := nav.Waypoints[0].HeadingAction(); ok {
		nav.FlightState.Heading = math.MagneticHeading(h.Heading)
	}

	if nav.FlightState.Position.IsZero() {
		lg.Errorf("uninitialized initial waypoint position! %+v", nav.Waypoints[0])
		return nil
	}

	if nav.FlightState.Heading == 0 { // unassigned, so get the heading using the next fix
		nav.FlightState.Heading = math.TrueToMagnetic(
			math.Heading2LL(nav.FlightState.Position, nav.Waypoints[1].Location, nav.FlightState.NmPerLongitude),
			nav.FlightState.MagneticVariation)
	}

	// Filter out airways...
	nav.Waypoints = util.FilterSliceInPlace(nav.Waypoints,
		func(wp av.Waypoint) bool { return !wp.Location.IsZero() })

	if ap, ok := db.DB.Airports[depAirport]; !ok {
		lg.Errorf("%s: departure airport unknown", depAirport)
		return nil
	} else {
		nav.FlightState.DepartureAirportLocation = ap.Location
		nav.FlightState.DepartureAirportElevation = float32(ap.Elevation)
	}
	if ap, ok := db.DB.Airports[arrAirport]; !ok {
		lg.Errorf("%s: arrival airport unknown", arrAirport)
		return nil
	} else {
		nav.FlightState.ArrivalAirportLocation = ap.Location
		nav.FlightState.ArrivalAirportElevation = float32(ap.Elevation)

		nav.FlightState.ArrivalAirport = av.Waypoint{
			Fix:      string(arrAirport),
			Location: ap.Location,
		}
		// VFR routes with SequenceVFRLanding get dynamic landing
		// waypoints when they reach the sequencing point; don't
		// append the arrival airport after it.
		if seqIdx == -1 {
			nav.Waypoints = append(nav.Waypoints, nav.FlightState.ArrivalAirport)
		}
	}

	return nav
}

func (nav *Nav) Check(lg *log.Logger) {
	check := func(waypoints []av.Waypoint, what string) {
		for _, wp := range waypoints {
			if wp.Location.IsZero() {
				lg.Errorf("zero waypoint location for %s in %s", wp.Fix, what)
			}
		}
	}

	check(nav.Waypoints, "waypoints")
	if nav.Approach.Assigned != nil {
		for i, waypoints := range nav.Approach.Assigned.Waypoints {
			check(waypoints, fmt.Sprintf("approach %d waypoints", i))
		}
	}
}

// Update flies the aircraft for one second in the weather the model gives
// for where it is. bravo is the Class B airspace, if any, whose shelves a
// VFR aircraft slows under, and arrivalMETAR, if non-nil, sets the approach
// speed's wind additive.
func (nav *Nav) Update(callsign string, model *wx.Model, rules av.FlightRules, arrivalMETAR *wx.METAR, simTime Time,
	bravo *db.AirspaceGrid) UpdateResult {
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime.Time())
	underBravo := rules == av.FlightRulesVFR &&
		db.UnderBravoShelf(bravo, nav.FlightState.Position, int(nav.FlightState.Altitude))
	return nav.UpdateWithWeather(callsign, wxs, arrivalMETAR, rules, simTime, underBravo)
}

// UpdateWithWeather is Update given its surroundings rather than finding
// them: the weather at the aircraft and whether it is under a Class B shelf.
func (nav *Nav) UpdateWithWeather(callsign string, wxs wx.Sample, arrivalMETAR *wx.METAR, rules av.FlightRules, simTime Time,
	underBravo bool) UpdateResult {
	// The recorder gets the Nav as it is before the update; commands the
	// update gives the aircraft, like an altitude to depart a fix at, are
	// its own doing and aren't recorded.
	rec := nav.recording()
	nav.recordDepth++
	defer func() { nav.recordDepth-- }()

	nav.PendingWaypointActionEvents = nil
	nav.activatePendingAltitude(simTime)

	// Log current state every tick
	NavLog(callsign, simTime, NavLogState, "pos=%.4f,%.4f alt=%.0f hdg=%.0f ias=%.0f gs=%.0f bank=%.1f rate=%.0f",
		nav.FlightState.Position[0], nav.FlightState.Position[1],
		nav.FlightState.Altitude, nav.FlightState.Heading,
		nav.FlightState.IAS, nav.FlightState.GS,
		nav.FlightState.BankAngle, nav.FlightState.AltitudeRate)

	if nav.Airwork != nil && !nav.Airwork.fly(nav, wxs) {
		// Done; on to the next waypoint.
		nav.Airwork = nil
		nav.Heading = Heading{}
	}
	if nav.Airwork == nil {
		nav.cancelSpeedOnFinal()
		targetAltitude, altitudeRate, geometricDescent := nav.TargetAltitude()
		deltaKts, slowingTo250 := nav.updateAirspeed(callsign, targetAltitude, geometricDescent, rules, wxs, arrivalMETAR, simTime, underBravo)
		nav.updateAltitude(callsign, targetAltitude, altitudeRate, geometricDescent, deltaKts, slowingTo250, wxs, simTime)
		nav.updateHeading(callsign, wxs, simTime)
	}
	nav.FlightState.advance(nav.TAS(wxs.Temperature()), nav.groundWind(wxs))

	result := UpdateResult{ActionEvents: nav.PendingWaypointActionEvents}
	if nav.Airwork == nil && nav.Heading.Assigned == nil &&
		nav.Heading.Hold == nil && len(nav.Heading.Maneuvers) == 0 {
		result = nav.updateWaypoints(callsign, wxs, simTime)
		result.ActionEvents = append(nav.PendingWaypointActionEvents, result.ActionEvents...)
	}

	if rec != nil {
		rec.Update(UpdateRecord{Time: simTime, Weather: wxs, METAR: arrivalMETAR, Rules: rules,
			UnderBravo: underBravo, Prespawn: nav.Prespawn, Result: result, State: nav.FlightState})
	}
	return result
}

func (nav *Nav) IsAirborne() bool {
	// Slow flight and stalls during airwork go well below V2.
	if nav.Airwork != nil {
		return true
	}
	return nav.FlightState.airborne(nav.Perf)
}

// groundWind returns the wind the aircraft drifts with: none until it is
// airborne, since it neither crabs nor drifts on the ground.
func (nav *Nav) groundWind(wxs wx.Sample) [2]float32 {
	if nav.IsAirborne() {
		return wxs.WindVec()
	}
	return [2]float32{}
}

///////////////////////////////////////////////////////////////////////////
// Recording

// A Recorder is told what happens to a Nav so that its flight can be replayed
// and compared later: every command it is given, what each update was given
// and what came of it, and every answer it gets from its Lookups. A Nav
// without one records nothing.
type Recorder interface {
	// Start is called once, before anything else is recorded, with the Nav
	// as it is then: the state the recorded flight starts from.
	Start(nav *Nav)
	// Command records a command method being called, with its arguments in
	// the order the method declares them.
	Command(name string, args ...any)
	// Lookup records the answer to a question asked of the Lookups: kind
	// names the question, like "waypoint", and key what it was asked about.
	Lookup(kind, key string, result any)
	// Update records one update of the aircraft.
	Update(u UpdateRecord)
}

// UpdateRecord is what one update of a Nav was given and what came of it.
type UpdateRecord struct {
	Time       Time
	Weather    wx.Sample
	METAR      *wx.METAR
	Rules      av.FlightRules
	UnderBravo bool
	Prespawn   bool
	Result     UpdateResult
	State      FlightState
}

// Lookups answers what nav asks about the world beyond the aircraft and its
// route. The sim answers from the database; a replayed trace answers from
// what the recording was told.
type Lookups interface {
	// Waypoint returns the location of a named fix or navaid.
	Waypoint(fix string) (math.Point2LL, bool)
	// Airport returns the location and elevation of an airport named by
	// its ICAO or FAA code.
	Airport(code string) (math.Point2LL, float32, bool)
	// Declination returns a VHF navaid's station declination.
	Declination(fix string) (float32, bool)
	Airways(name string) ([]av.Airway, bool)
	// Holds returns the holds published at a fix.
	Holds(fix string) ([]av.Hold, bool)
	Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool)
	OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool)
}

// LookupResult is how a recorded answer from the Lookups is encoded: what
// the method returned and whether it found anything.
type LookupResult[T any] struct {
	Value T
	OK    bool
}

// AirportLookup is the value of a recorded Airport answer.
type AirportLookup struct {
	Location  math.Point2LL
	Elevation float32
}

// SetRecorder sets the recorder the Nav reports to; nil stops it reporting.
func (nav *Nav) SetRecorder(r Recorder) {
	nav.recorder = r
	nav.recordStarted = false
}

// recording returns the recorder to report to, starting it with the Nav as
// it is now if this is the first report, or nil if the Nav isn't recording.
func (nav *Nav) recording() Recorder {
	if nav.recorder != nil && !nav.recordStarted {
		nav.recordStarted = true
		nav.recorder.Start(nav)
	}
	return nav.recorder
}

// SetLookups sets where the Nav's questions about the world are answered;
// nil restores the database.
func (nav *Nav) SetLookups(l Lookups) {
	nav.lookups = l
}

// record reports a command to the recorder, if there is one, and returns the
// function that marks the command's end. A command that another command or
// an update gives isn't recorded: replaying the outer one gives it again.
// Every command method starts with "defer nav.record(...)()", naming itself
// and all of its parameters, which TestCommandsRecorded checks.
func (nav *Nav) record(name string, args ...any) func() {
	rec := nav.recording()
	if rec == nil {
		return func() {}
	}
	if nav.recordDepth == 0 {
		rec.Command(name, args...)
	}
	nav.recordDepth++
	return func() { nav.recordDepth-- }
}

// lookup returns where the Nav's questions are answered: its Lookups, or the
// database if it has none, with every answer recorded if it is recording.
func (nav *Nav) lookup() Lookups {
	l := nav.lookups
	if l == nil {
		l = databaseLookups{}
	}
	if rec := nav.recording(); rec != nil {
		return recordedLookups{l, rec}
	}
	return l
}

// databaseLookups answers from the static database.
type databaseLookups struct{}

func (databaseLookups) Waypoint(fix string) (math.Point2LL, bool) {
	return db.DB.LookupWaypoint(fix)
}

func (databaseLookups) Airport(code string) (math.Point2LL, float32, bool) {
	if ap, ok := db.DB.LookupICAOAirport(av.ICAOAirportCode(code)); ok {
		return ap.Location, float32(ap.Elevation), true
	} else if ap, ok := db.DB.LookupFAAAirport(av.FAAAirportCode(code)); ok {
		return ap.Location, float32(ap.Elevation), true
	}
	return math.Point2LL{}, 0, false
}

func (databaseLookups) Declination(fix string) (float32, bool) {
	return db.DB.Declination(fix)
}

func (databaseLookups) Airways(name string) ([]av.Airway, bool) {
	a, ok := db.DB.Airways[name]
	return a, ok
}

func (databaseLookups) Holds(fix string) ([]av.Hold, bool) {
	h, ok := db.DB.EnrouteHolds[fix]
	return h, ok
}

func (databaseLookups) Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	return av.LookupRunway(db.Lookups{}, airport, runway)
}

func (databaseLookups) OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	return av.LookupOppositeRunway(db.Lookups{}, airport, runway)
}

// recordedLookups answers from another Lookups and records each answer.
type recordedLookups struct {
	l   Lookups
	rec Recorder
}

func (r recordedLookups) Waypoint(fix string) (math.Point2LL, bool) {
	p, ok := r.l.Waypoint(fix)
	r.rec.Lookup("waypoint", fix, LookupResult[math.Point2LL]{p, ok})
	return p, ok
}

func (r recordedLookups) Airport(code string) (math.Point2LL, float32, bool) {
	p, elev, ok := r.l.Airport(code)
	r.rec.Lookup("airport", code, LookupResult[AirportLookup]{AirportLookup{p, elev}, ok})
	return p, elev, ok
}

func (r recordedLookups) Declination(fix string) (float32, bool) {
	d, ok := r.l.Declination(fix)
	r.rec.Lookup("declination", fix, LookupResult[float32]{d, ok})
	return d, ok
}

func (r recordedLookups) Airways(name string) ([]av.Airway, bool) {
	a, ok := r.l.Airways(name)
	r.rec.Lookup("airways", name, LookupResult[[]av.Airway]{a, ok})
	return a, ok
}

func (r recordedLookups) Holds(fix string) ([]av.Hold, bool) {
	h, ok := r.l.Holds(fix)
	r.rec.Lookup("holds", fix, LookupResult[[]av.Hold]{h, ok})
	return h, ok
}

func (r recordedLookups) Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	rwy, ok := r.l.Runway(airport, runway)
	r.rec.Lookup("runway", string(airport)+"/"+runway, LookupResult[av.Runway]{rwy, ok})
	return rwy, ok
}

func (r recordedLookups) OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	rwy, ok := r.l.OppositeRunway(airport, runway)
	r.rec.Lookup("opposite_runway", string(airport)+"/"+runway, LookupResult[av.Runway]{rwy, ok})
	return rwy, ok
}

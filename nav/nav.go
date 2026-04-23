// nav/nav.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
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
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidFix                   = errors.New("Invalid fix")
	ErrNotClearedForApproach        = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute               = errors.New("Aircraft is not currently flying its assigned route")
	ErrUnableCommand                = errors.New("Unable")
	ErrUnknownApproach              = errors.New("Unknown approach")
)

// State related to navigation. Pointers are used for optional values; nil
// -> unset/unspecified.
type Nav struct {
	FlightState FlightState
	Perf        av.AircraftPerformance
	Altitude    NavAltitude
	Speed       NavSpeed
	Heading     NavHeading
	Approach    NavApproach
	Airwork     *NavAirwork
	Prespawn    bool

	FixAssignments map[string]NavFixAssignment

	// DeferredNavHeading stores a heading/direct fix assignment from the
	// controller that the pilot has not yet started to follow.  Note that
	// only a single such assignment is stored; for example, if the
	// controller issues a first heading and then a second shortly
	// afterward, before the first has been followed, it's fine for the
	// second to override it.
	DeferredNavHeading *DeferredNavHeading

	// ExpectedDirectFix records a fix the pilot has been told to expect
	// direct to; when the actual direct instruction comes, there is less
	// delay since the pilot is prepared.
	ExpectedDirectFix string

	FinalAltitude float32
	Waypoints     av.WaypointArray

	PendingWaypointActionEvents []av.WaypointActionEvent

	Rand *rand.Rand

	// PendingConditionalCommand stores a single deferred LV/RC action
	// (e.g., "leaving 3,000, fly heading 010"). Cleared when the trigger
	// fires or when a new LV/RC command is installed. Not cleared on
	// new altitude/heading/speed assignments or on handoff.
	PendingConditionalCommand *PendingConditionalCommand
}

type UpdateResult struct {
	PassedWaypoint *av.Waypoint
	ActionEvents   []av.WaypointActionEvent
}

type contactCrossingRestriction struct {
	Fix            string
	AltRestriction *av.AltitudeRestriction
	Speed          *av.SpeedRestriction
}

// DeferredNavHeading stores a heading assignment from the controller and the
// time at which to start executing it; this time is set to be a few
// seconds after the controller issues it in order to model the delay
// before pilots start to follow assignments.
type DeferredNavHeading struct {
	Time    Time
	Heading *math.MagneticHeading
	Turn    *av.TurnDirection
	Hold    *FlyHold
	// For direct fix, this will be the updated set of waypoints.
	Waypoints []av.Waypoint
}

// NavSnapshot captures all controller-modifiable state in Nav for rollback purposes.
// It does NOT include FlightState (aircraft physical position/heading/altitude) -
// only control assignments that can be rolled back.
type NavSnapshot struct {
	Altitude           NavAltitude
	Speed              NavSpeed
	Heading            NavHeading
	Approach           NavApproach
	Waypoints          av.WaypointArray
	DeferredNavHeading *DeferredNavHeading
	FixAssignments     map[string]NavFixAssignment
}

// TakeSnapshot captures the current controller-modifiable nav state for later rollback.
func (nav *Nav) TakeSnapshot() NavSnapshot {
	return deep.MustCopy(NavSnapshot{
		Altitude:           nav.Altitude,
		Speed:              nav.Speed,
		Heading:            nav.Heading,
		Approach:           nav.Approach,
		Waypoints:          nav.Waypoints,
		DeferredNavHeading: nav.DeferredNavHeading,
		FixAssignments:     nav.FixAssignments,
	})
}

// RestoreSnapshot restores nav state from a previously captured snapshot.
// The aircraft's physical state (FlightState) is NOT restored - only control assignments.
func (nav *Nav) RestoreSnapshot(snap NavSnapshot) {
	nav.Altitude = snap.Altitude
	nav.Speed = snap.Speed
	nav.Heading = snap.Heading
	nav.Approach = snap.Approach
	nav.Waypoints = snap.Waypoints
	nav.DeferredNavHeading = snap.DeferredNavHeading
	nav.FixAssignments = snap.FixAssignments
}

type FlightState struct {
	InitialDepartureClimb     bool
	DepartureAirportLocation  math.Point2LL
	DepartureAirportElevation float32
	ArrivalAirport            av.Waypoint
	ArrivalAirportLocation    math.Point2LL
	ArrivalAirportElevation   float32

	MagneticVariation float32
	NmPerLongitude    float32

	Position     math.Point2LL
	Heading      math.MagneticHeading
	Altitude     float32
	PrevAltitude float32
	IAS, GS      float32 // speeds...
	BankAngle    float32 // degrees
	AltitudeRate float32 // + -> climb, - -> descent
}

func (fs *FlightState) Summary() string {
	return fmt.Sprintf("heading %03d altitude %.0f ias %.1f gs %.1f",
		int(fs.Heading), fs.Altitude, fs.IAS, fs.GS)
}

func (fs FlightState) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("initial_departure_climb", fs.InitialDepartureClimb),
		slog.Any("position", fs.Position),
		slog.Float64("heading", float64(fs.Heading)),
		slog.Float64("altitude", float64(fs.Altitude)),
		slog.Float64("ias", float64(fs.IAS)),
		slog.Float64("gs", float64(fs.GS)),
	)
}

type RateQualifier int

const (
	RateNormal   RateQualifier = iota
	RateGood                   // faster than normal, not maximum
	RateExpedite               // maximum rate
)

type NavAltitude struct {
	Assigned        *float32 // controller-assigned altitude (not yet in autopilot)
	ActiveAssigned  *float32 // assigned altitude currently used for vertical guidance
	ActivateAt      Time     // non-zero while Assigned is pending activation
	Cleared         *float32 // from initial clearance
	AfterSpeed      *float32
	AfterSpeedSpeed *float32
	Rate            RateQualifier
	RateThrough     *float32      // revert to RateNormal after passing this altitude; nil = all the way
	RateAfterSpeed  RateQualifier // preserved across speed transitions

	// Carried after passing a waypoint if we were unable to meet the
	// restriction at the way point; we keep trying until we get there (or
	// are given another instruction..)
	Restriction *av.AltitudeRestriction
}

type NavSpeed struct {
	Assigned                 *av.SpeedRestriction // controller-assigned (exact or range)
	AfterAltitude            *av.SpeedRestriction // speed to apply after reaching assigned altitude
	AfterAltitudeAltitude    *float32
	MaintainSlowestPractical bool
	MaintainMaximumForward   bool
	// Carried after passing a waypoint
	Restriction *av.SpeedRestriction
}

const MaxIAS = 290

type NavHeading struct {
	Assigned   *math.MagneticHeading
	Turn       *av.TurnDirection
	Arc        *av.DMEArc
	JoiningArc bool
	Maneuvers  []LateralManeuver
	Hold       *FlyHold
}

type NavApproach struct {
	Assigned                    *av.Approach
	ATPAVolume                  *av.ATPAVolume
	Cleared                     bool
	StandbyApproach             bool // suppress repeated approach clearance requests
	RequestApproachClearance    bool // pilot should radio for approach clearance
	GoAroundNoApproachClearance bool // pilot should go around (reached FAF without clearance)
	RequestVectors              bool // pilot should request vectors (overshot localizer)
	InterceptState              InterceptState
	PassedApproachFix           bool // have we passed a fix on the approach yet?
	PassedFAF                   bool
	NoPT                        bool
	AtFixClearedRoute           []av.Waypoint
	AtFixInterceptFix           string           // fix where aircraft should intercept the localizer
	InterceptCourseLine         [2]math.Point2LL // cached course line for non-ILS intercepts
	InterceptWaypoints          []av.Waypoint    // cached remaining waypoints for non-ILS intercepts
}

type NavFixAssignment struct {
	Arrive struct {
		Altitude *av.AltitudeRestriction
		Speed    *av.SpeedRestriction
	}
	Depart struct {
		Fix         *av.Waypoint
		Heading     *math.MagneticHeading
		Turn        *av.TurnDirection
		Speed       *av.SpeedRestriction
		CancelSpeed bool // cancel speed restrictions when passing this fix
		Altitude    *float32
	}
	Hold *av.Hold
}

type NavAirwork struct {
	Radius   float32
	Center   math.Point2LL
	AltRange [2]float32

	RemainingSteps  int
	NextMoveCounter int
	Heading         math.MagneticHeading
	TurnRate        float32
	TurnDirection   av.TurnDirection
	IAS             float32
	Altitude        float32
	Dive            bool
	ToCenter        bool
}

type InterceptState int

const (
	NotIntercepting InterceptState = iota
	InitialHeading
	TurningToJoin
	OnApproachCourse
)

func MakeArrivalNav(callsign av.ADSBCallsign, arr *av.Arrival, fp av.FlightPlan, perf av.AircraftPerformance,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == av.FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, arr.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, lg); nav != nil {
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
			alt := arr.ClearedAltitude
			nav.Altitude.Cleared = &alt
		}

		nav.FinalAltitude = max(nav.FinalAltitude, arr.InitialAltitude)
		nav.FlightState.Altitude = arr.InitialAltitude
		nav.FlightState.IAS = arr.InitialSpeed
		// This won't be quite right but it's better than leaving GS to be
		// 0 for the first nav update tick which leads to various Inf and
		// NaN cases...
		nav.FlightState.GS = nav.FlightState.IAS

		return nav
	}
	return nil
}

func MakeDepartureNav(callsign av.ADSBCallsign, fp av.FlightPlan, perf av.AircraftPerformance,
	assignedAlt, clearedAlt int, wp []av.Waypoint, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time, lg *log.Logger) *Nav {
	if nav := makeNav(callsign, fp, perf, wp, randomizeAltitudeRange, nmPerLongitude, magneticVariation,
		lg); nav != nil {
		if assignedAlt != 0 {
			nav.setAssignedAltitude(float32(min(assignedAlt, fp.Altitude)))
		} else {
			alt := float32(min(clearedAlt, fp.Altitude))
			nav.Altitude.Cleared = &alt
		}
		nav.FlightState.InitialDepartureClimb = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func MakeOverflightNav(callsign av.ADSBCallsign, of *av.Overflight, fp av.FlightPlan, perf av.AircraftPerformance,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == av.FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, of.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, lg); nav != nil {
		if !of.SpeedRestriction.IsZero() {
			sr := of.SpeedRestriction
			nav.Speed.Restriction = &sr
		}
		if of.AssignedAltitude > 0 {
			nav.setAssignedAltitude(of.AssignedAltitude)
		}
		if of.AssignedSpeed > 0 {
			sr := av.MakeAtSpeedRestriction(of.AssignedSpeed)
			nav.Speed.Assigned = &sr
		}

		nav.FlightState.Altitude = float32(rand.SampleSlice(nav.Rand, of.InitialAltitudes))
		nav.FlightState.IAS = of.InitialSpeed
		// This won't be quite right but it's better than leaving GS to be
		// 0 for the first nav update tick which leads to various Inf and
		// NaN cases...
		nav.FlightState.GS = nav.FlightState.IAS

		return nav
	}
	return nil
}

func makeNav(callsign av.ADSBCallsign, fp av.FlightPlan, perf av.AircraftPerformance, wp []av.Waypoint,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32,
	lg *log.Logger) *Nav {
	nav := &Nav{
		Perf:           perf,
		FinalAltitude:  float32(fp.Altitude),
		FixAssignments: make(map[string]NavFixAssignment),
		Rand:           rand.Make(),
	}

	// Copy the provided waypoints so that any local modifications we make don't pollute the
	// waypoints stored for the scenario. Add a small buffer for the destination airport waypoint.
	nav.Waypoints = make([]av.Waypoint, len(wp)+1)
	copy(nav.Waypoints, wp)

	av.RandomizeRoute(nav.Waypoints, nav.Rand, randomizeAltitudeRange, nav.Perf, nmPerLongitude,
		magneticVariation, fp.ArrivalAirport, lg)

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
		Heading:           nav.Waypoints[0].MagneticHeading(),
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

	if ap, ok := av.DB.Airports[fp.DepartureAirport]; !ok {
		lg.Errorf("%s: departure airport unknown", fp.DepartureAirport)
		return nil
	} else {
		nav.FlightState.DepartureAirportLocation = ap.Location
		nav.FlightState.DepartureAirportElevation = float32(ap.Elevation)
	}
	if ap, ok := av.DB.Airports[fp.ArrivalAirport]; !ok {
		lg.Errorf("%s: arrival airport unknown", fp.ArrivalAirport)
		return nil
	} else {
		nav.FlightState.ArrivalAirportLocation = ap.Location
		nav.FlightState.ArrivalAirportElevation = float32(ap.Elevation)

		nav.FlightState.ArrivalAirport = av.Waypoint{
			Fix:      fp.ArrivalAirport,
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

func (nav *Nav) TAS(temp av.Temperature) float32 {
	tas := av.IASToTAS(nav.FlightState.IAS, nav.FlightState.Altitude)
	if nav.machTransition() {
		tas = min(tas, av.MachToTAS(nav.Perf.Speed.MaxMach, temp))
	} else {
		tas = min(tas, nav.Perf.Speed.CruiseTAS)
	}
	return tas
}

func (nav *Nav) Mach(temp av.Temperature) float32 {
	tas := nav.TAS(temp)
	return av.TASToMach(tas, temp)
}

func (nav *Nav) v2() float32 {
	if nav.Perf.Speed.V2 == 0 {
		// Unfortunately we don't always have V2 in the performance database, so approximate...
		return 0.95 * nav.Perf.Speed.Landing
	}
	return nav.Perf.Speed.V2
}

func (nav *Nav) IsAirborne() bool {
	v2 := nav.v2()

	// FIXME: this only considers speed, which is probably ok but is somewhat unsatisfying.
	// More explicitly model "on the ground" vs "airborne" states?
	return nav.FlightState.IAS >= v2
}

// AssignedHeading returns the aircraft's current heading assignment, if
// any, regardless of whether the pilot has yet started following it.
func (nav *Nav) AssignedHeading() (math.MagneticHeading, bool) {
	if dh := nav.DeferredNavHeading; dh != nil {
		if dh.Heading != nil {
			return *dh.Heading, true
		}
	} else if nav.Heading.Assigned != nil {
		return *nav.Heading.Assigned, true
	}
	return 0, false
}

// DepartureHeadingState describes the state of a departure's heading assignment.
type DepartureHeadingState int

const (
	// NoHeading indicates no heading is assigned or upcoming
	NoHeading DepartureHeadingState = iota
	// OnHeading indicates the aircraft is currently flying an assigned heading
	OnHeading
	// TurningToHeading indicates the aircraft is about to turn to a heading
	TurningToHeading
)

// DepartureHeading returns the heading a departure will fly and its state.
// Returns the heading and state based on:
// - OnHeading: aircraft has an assigned heading
// - TurningToHeading: first waypoint has a heading (about to turn)
// - NoHeading: no assigned heading and first waypoint has no heading
func (nav *Nav) DepartureHeading() (int, DepartureHeadingState) {
	// If we have an assigned heading, we're flying it
	if nav.Heading.Assigned != nil {
		return int(*nav.Heading.Assigned), OnHeading
	}
	// If the first waypoint has a heading, we're about to turn to it
	if len(nav.Waypoints) > 0 && nav.Waypoints[0].Heading != 0 {
		return int(nav.Waypoints[0].Heading), TurningToHeading
	}
	return 0, NoHeading
}

// EnqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment. delayReduction is subtracted
// from the pilot-reaction delay (floored at zero) to offset latency already
// spent receiving the voice transmission.
func (nav *Nav) EnqueueHeading(hdg math.MagneticHeading, turn av.TurnDirection, approachCleared bool, simTime Time, delayReduction time.Duration) {
	var d time.Duration
	if approachCleared {
		// Minimal delay if the aircraft has been cleared for an approach.
		d = nav.Rand.DurationRange(1*time.Second, 2*time.Second)
	} else if nav.Heading.Assigned != nil && nav.DeferredNavHeading == nil {
		// Already flying a heading; minimal delay.
		d = nav.Rand.DurationRange(1*time.Second, 2*time.Second)
	} else {
		// LNAV -> heading mode
		d = nav.Rand.DurationRange(2*time.Second, 4*time.Second)
	}

	if d > delayReduction {
		d -= delayReduction
	} else {
		d = 0
	}
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(d),
		Heading: &hdg,
		Turn:    &turn,
	}
}

// AssignedWaypoints returns the route that should be flown following a
// controller instruction. If an instruction has been issued but the delay
// hasn't passed, these are different than the waypoints currently being
// used for navigation.
func (nav *Nav) AssignedWaypoints() []av.Waypoint {
	if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
		return dh.Waypoints
	}
	return nav.Waypoints
}

func (nav *Nav) EnqueueDirectFix(wps []av.Waypoint, turn av.TurnDirection, simTime Time, delayReduction time.Duration) {
	var d time.Duration
	if len(wps) > 0 && nav.ExpectedDirectFix == wps[0].Fix {
		// Pilot was told to expect this fix; shorter delay
		d = nav.Rand.DurationRange(2*time.Second, 4*time.Second)
		nav.ExpectedDirectFix = ""
	} else if nav.Heading.Assigned == nil && nav.DeferredNavHeading == nil {
		// Already in LNAV mode; have less of a delay
		d = nav.Rand.DurationRange(4*time.Second, 7*time.Second)
	} else {
		// heading->LNAV--longer delay
		d = nav.Rand.DurationRange(8*time.Second, 13*time.Second)
	}

	if d > delayReduction {
		d -= delayReduction
	} else {
		d = 0
	}
	dh := &DeferredNavHeading{
		Time:      simTime.Add(d),
		Waypoints: wps,
	}
	if turn != av.TurnClosest {
		dh.Turn = &turn
	}
	nav.DeferredNavHeading = dh
}

func (nav *Nav) EnqueueOnCourse(simTime Time) {
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time: simTime.Add(nav.Rand.DurationRange(8*time.Second, 13*time.Second)),
	}
}

func (nav *Nav) OnApproach(checkAltitude bool) bool {
	if !nav.Approach.Cleared {
		return false
	}

	if _, assigned := nav.AssignedHeading(); assigned {
		return false
	}

	// The aircraft either must have passed a fix on the approach or be on
	// the localizer and also be above any upcoming altitude restrictions.
	if !nav.Approach.PassedApproachFix && nav.Approach.InterceptState != OnApproachCourse {
		return false
	}

	if !checkAltitude {
		return true
	}

	for _, wp := range nav.Waypoints {
		// Ignore controller-assigned "cross FIX at ALT" for this
		if r := wp.AltitudeRestriction(); r != nil {
			return nav.FlightState.Altitude >= r.TargetAltitude(nav.FlightState.Altitude)
		}
	}
	return false
}

// onCourseLine checks if the flight position is less than maxNmDeviation
// from the infinite line defined by the two given points.
func (nav *Nav) onCourseLine(line [2]math.Point2LL, maxNmDeviation float32) bool {
	distance := math.PointLineDistance(
		math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.LL2NM(line[0], nav.FlightState.NmPerLongitude),
		math.LL2NM(line[1], nav.FlightState.NmPerLongitude))
	return distance < maxNmDeviation
}

// OnExtendedCenterline checks if the flight position is less than maxNmDeviation
// from the infinite line defined by the assigned approach localizer
func (nav *Nav) OnExtendedCenterline(maxNmDeviation float32) bool {
	approach := nav.Approach.Assigned
	if approach == nil {
		return false
	}

	cl := approach.ExtendedCenterline(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	return nav.onCourseLine(cl, maxNmDeviation)
}

///////////////////////////////////////////////////////////////////////////
// Communication

// Full human-readable summary of nav state for use when paused and mouse
// hover on the scope
func (nav *Nav) Summary(fp av.FlightPlan, model *wx.Model, simTime Time, lg *log.Logger) string {
	var lines []string
	lines = append(lines, "Departure from "+fp.DepartureAirport+" to "+fp.ArrivalAirport)

	if nav.Altitude.Assigned != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Assigned) < 100 {
			lines = append(lines, "At assigned altitude "+
				av.FormatAltitude(*nav.Altitude.Assigned))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(*nav.Altitude.Assigned)
			line += nav.rateSummary()
			lines = append(lines, line)
		}
	} else if nav.Altitude.AfterSpeed != nil {
		dir := util.Select(*nav.Altitude.AfterSpeed > nav.FlightState.Altitude, "climb", "descend")
		rateSuffix := ""
		switch nav.Altitude.RateAfterSpeed {
		case RateGood:
			rateSuffix = ", good rate"
		case RateExpedite:
			rateSuffix = ", expediting"
		}
		lines = append(lines, fmt.Sprintf("At %.0f kts, %s to %s"+rateSuffix,
			*nav.Altitude.AfterSpeedSpeed, dir, av.FormatAltitude(*nav.Altitude.AfterSpeed)))
	} else if target, ok := nav.findAltitudeTarget(); ok {
		dir := util.Select(target.altitude > nav.FlightState.Altitude, "Climbing", "Descending")
		alt := target.altitude
		if nav.Altitude.Cleared != nil {
			alt = min(alt, *nav.Altitude.Cleared)
		}
		lines = append(lines, dir+" to "+av.FormatAltitude(alt)+" for alt. restriction at "+target.fix)
	} else if nav.Altitude.Cleared != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Cleared) < 100 {
			lines = append(lines, "At cleared altitude "+
				av.FormatAltitude(*nav.Altitude.Cleared))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(*nav.Altitude.Cleared)
			line += nav.rateSummary()
			lines = append(lines, line)
		}
	} else if nav.Altitude.Restriction != nil {
		tgt := nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
		tgt = min(tgt, nav.FinalAltitude)

		if tgt < nav.FlightState.Altitude {
			lines = append(lines, "Descending "+av.FormatAltitude(nav.FlightState.Altitude)+
				" to "+av.FormatAltitude(tgt)+" from previous crossing restriction")
		} else {
			lines = append(lines, "Climbing "+av.FormatAltitude(nav.FlightState.Altitude)+
				" to "+av.FormatAltitude(tgt)+" from previous crossing restriction")
		}
	}
	if nav.FlightState.Altitude < nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Descent rate %.0f ft/minute", 60*(nav.FlightState.PrevAltitude-nav.FlightState.Altitude)))
	} else if nav.FlightState.Altitude > nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Climb rate %.0f ft/minute", 60*(nav.FlightState.Altitude-nav.FlightState.PrevAltitude)))
	}

	// Heading
	if nav.Heading.Assigned != nil {
		if *nav.Heading.Assigned == nav.FlightState.Heading {
			lines = append(lines, fmt.Sprintf("On assigned %03d heading",
				int(*nav.Heading.Assigned)))
		} else {
			lines = append(lines, fmt.Sprintf("Turning from %03d to assigned %03d heading bank angle %d",
				int(nav.FlightState.Heading), int(*nav.Heading.Assigned), int(nav.FlightState.BankAngle)))
		}
	}
	if hold := nav.Heading.Hold; hold != nil {
		lines = append(lines, fmt.Sprintf("Flying hold at %s, %s entry, state: %s",
			hold.Hold.DisplayName(), hold.Entry.String(), hold.State.String()))
	}
	if dh := nav.DeferredNavHeading; dh != nil {
		if len(dh.Waypoints) > 0 {
			lines = append(lines, fmt.Sprintf("Will shortly go direct %s", dh.Waypoints[0].Fix))
		} else if dh.Heading != nil {
			lines = append(lines, fmt.Sprintf("Will shortly start flying heading %03d", int(*dh.Heading)))
		} else {
			lines = append(lines, "Will shortly proceed on course/fly the current SID/STAR")
		}
		if dh.Hold != nil {
			lines = append(lines, fmt.Sprintf("Will shortly enter hold at %s", dh.Hold.Hold.DisplayName()))
		}
	}

	// weather
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime.Time())
	lines = append(lines, wxs.String())
	if nav.FlightState.Altitude > nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Weather-based climb rate factor %.2fx", nav.atmosClimbFactor(wxs)))
	}

	// Speed; don't be as exhaustive as we are for altitude
	targetAltitude, _, _ := nav.TargetAltitude()
	lines = append(lines, fmt.Sprintf("IAS %d GS %d TAS %d", int(nav.FlightState.IAS),
		int(nav.FlightState.GS), int(nav.TAS(wxs.Temperature()))))
	ias, _ := nav.TargetSpeed(targetAltitude, &fp, wxs, nil)
	if nav.Speed.MaintainSlowestPractical {
		lines = append(lines, fmt.Sprintf("Maintain slowest practical speed: %.0f kts", ias))
	} else if nav.Speed.MaintainMaximumForward {
		lines = append(lines, fmt.Sprintf("Maintain maximum forward speed: %.0f kts", ias))
	} else if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	} else if sr := nav.Speed.Assigned; sr != nil {
		if spd, exact := sr.ExactValue(); exact {
			lines = append(lines, fmt.Sprintf("Maintaining %.0f kts assignment", spd))
		} else {
			lines = append(lines, fmt.Sprintf("Maintaining %s kts assignment", sr.Encoded()))
		}
	} else if nav.Speed.AfterAltitude != nil && nav.Speed.AfterAltitudeAltitude != nil {
		lines = append(lines, fmt.Sprintf("At %s, maintain %s kts", av.FormatAltitude(*nav.Speed.AfterAltitudeAltitude),
			nav.Speed.AfterAltitude.Encoded()))
	}

	for _, fix := range util.SortedMapKeys(nav.FixAssignments) {
		nfa := nav.FixAssignments[fix]
		if nfa.Arrive.Altitude != nil || nfa.Arrive.Speed != nil {
			line := "Cross " + fix + " "
			if nfa.Arrive.Altitude != nil {
				ar := av.MakeReadbackTransmission("{altrest}", nfa.Arrive.Altitude)
				line += ar.Written(nav.Rand) + " "
			}
			if nfa.Arrive.Speed != nil {
				if nfa.Arrive.Speed.IsMach {
					line += "at " + nfa.Arrive.Speed.Encoded()
				} else {
					line += "at " + nfa.Arrive.Speed.Encoded() + " kts"
				}
			}
			lines = append(lines, line)
		}
		if nfa.Depart.Heading != nil && nav.Heading.Assigned == nil {
			lines = append(lines, fmt.Sprintf("Depart "+fix+" heading %03d",
				int(*nfa.Depart.Heading)))
		}
		if nfa.Depart.Speed != nil {
			sr := nfa.Depart.Speed
			spd, exact := sr.ExactValue()
			if exact {
				lines = append(lines, fmt.Sprintf("After %s maintain %.0f kts", fix, spd))
			} else if sr.Range[0] > 0 {
				lines = append(lines, fmt.Sprintf("After %s maintain %.0f kts or greater", fix, sr.Range[0]))
			} else {
				lines = append(lines, fmt.Sprintf("After %s maintain %.0f kts or less", fix, sr.Range[1]))
			}
		}
		if nfa.Depart.Altitude != nil {
			alt := *nfa.Depart.Altitude
			verb := "climb"
			if alt < nav.FlightState.Altitude {
				verb = "descend"
			}
			lines = append(lines, fmt.Sprintf("After %s %s and maintain %s", fix, verb, av.FormatAltitude(alt)))
		}
	}

	// Approach
	if nav.Approach.Assigned != nil {
		verb := util.Select(nav.Approach.Cleared, "Cleared", "Assigned")
		if nav.Approach.Cleared && nav.Approach.NoPT {
			verb += " straight-in"
		}
		line := verb + " " + nav.Approach.Assigned.FullName
		switch nav.Approach.InterceptState {
		case NotIntercepting:
			// nada
		case InitialHeading:
			line += ", will join the approach"
		case TurningToJoin:
			line += ", turning to join the approach"
		case OnApproachCourse:
			line += ", established on the approach"
		}
		lines = append(lines, line)

		if len(nav.Heading.Maneuvers) > 0 {
			lines = append(lines, fmt.Sprintf("Flying procedure turn (step %d/%d: %s)",
				1, len(nav.Heading.Maneuvers), nav.Heading.Maneuvers[0].String()))
		}
	}

	lines = append(lines, "Route flying: "+av.WaypointArray(nav.Waypoints).Encode())
	if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
		lines = append(lines, "Route assigned: "+av.WaypointArray(dh.Waypoints).Encode())
	}

	return strings.Join(lines, "\n")
}

// procedureHasAltRestrictions returns whether any remaining waypoints on the
// SID (if checkSID) or STAR (otherwise) have altitude restrictions.
func (nav *Nav) procedureHasAltRestrictions(checkSID bool) bool {
	for i := range nav.Waypoints {
		onProc := util.Select(checkSID, nav.Waypoints[i].OnSID(), nav.Waypoints[i].OnSTAR())
		if onProc && nav.Waypoints[i].AltitudeRestriction() != nil {
			return true
		}
	}
	return false
}

// addAltitudePhrasing appends realistic altitude reporting to the
// transmission based on the aircraft's current flight state.
func (nav *Nav) addAltitudePhrasing(rt *av.RadioTransmission, targetAlt float32) {
	cur := nav.FlightState.Altitude
	diff := targetAlt - cur

	if diff > 200 {
		// Climbing, not near target
		rt.Add("[leaving|out of] {alt} [climbing|for] {alt}", cur, targetAlt)
	} else if diff < -200 {
		// Descending, not near target
		rt.Add("[leaving|out of] {alt} [descending to|for] {alt}", cur, targetAlt)
	} else if math.Abs(diff) > 10 {
		// Leveling near target
		rt.Add("[leveling|at] {alt}", targetAlt)
	} else {
		// At altitude
		rt.Add("[at|] {alt}", cur)
	}
}

func (nav *Nav) DepartureMessage(sid string, reportHeading bool) *av.RadioTransmission {
	rt := &av.RadioTransmission{Type: av.RadioTransmissionContact}

	target := util.Select(nav.Altitude.Assigned != nil, nav.Altitude.Assigned, nav.Altitude.Cleared)
	climbing := target != nil && *target-nav.FlightState.Altitude > 200

	if sid != "" && climbing {
		if nav.Altitude.Assigned != nil && nav.procedureHasAltRestrictions(true) {
			// SID with climbing via + controller override: "except maintaining {alt}"
			rt.Add("[leaving|out of] {alt} climbing via the {sid} [departure|] except maintaining {alt}",
				nav.FlightState.Altitude, sid, *nav.Altitude.Assigned)
		} else if nav.procedureHasAltRestrictions(true) {
			// SID with altitude restrictions, climbing via
			rt.Add("[leaving|out of] {alt} climbing via the {sid} [departure|]",
				nav.FlightState.Altitude, sid)
		} else {
			// SID without altitude restrictions
			rt.Add("[leaving|out of] {alt} [for|climbing] {alt} [on the {sid} departure|]",
				nav.FlightState.Altitude, *target, sid)
		}
	} else if climbing {
		// No SID, just climbing
		rt.Add("[leaving|out of] {alt} [for|climbing] {alt}",
			nav.FlightState.Altitude, *target)
	} else {
		// At altitude or leveling
		if target != nil {
			nav.addAltitudePhrasing(rt, *target)
		} else {
			rt.Add("[at|] {alt}", nav.FlightState.Altitude)
		}
	}

	// Heading information for departures with varied exit headings
	if reportHeading {
		hdg, state := nav.DepartureHeading()
		switch state {
		case OnHeading:
			rt.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		case TurningToHeading:
			rt.Add("[turning to|about to turn to] [heading|] {hdg}", hdg)
		}
	}

	return rt
}

func (nav *Nav) ContactMessage(reportingPoints []av.ReportingPoint, star string, runway string,
	reportHeading bool, isDeparture bool) *av.RadioTransmission {
	var resp av.RadioTransmission

	// Find the first applicable fix assignment for reporting
	crossing := nav.firstCrossingRestriction()

	if isDeparture && reportHeading {
		// Departure with varied headings
		hdg, state := nav.DepartureHeading()
		switch state {
		case OnHeading:
			resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		case TurningToHeading:
			resp.Add("[turning to|about to turn to] [heading|] {hdg}", hdg)
		}
		// Add altitude after heading for departures
		nav.addContactAltitude(&resp, star, crossing)
	} else if hdg, ok := nav.AssignedHeading(); ok && reportHeading {
		// Being vectored - heading + altitude
		resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		nav.addContactAltitude(&resp, "", crossing)
	} else if star != "" {
		// On a STAR
		nav.addStarAltitude(&resp, star, crossing)
	} else {
		// Simple altitude
		nav.addContactAltitude(&resp, "", crossing)
	}

	// Runway assignment
	if runway != "" {
		resp.Add("[runway {rwy}|for runway {rwy}]", runway)
	}

	// Speed assignment
	if sr := nav.Speed.Assigned; sr != nil {
		if spd, exact := sr.ExactValue(); exact {
			resp.Add("[assigned {spd}|{spd} knots]", spd)
		} else if sr.Range[0] > 0 && sr.Range[1] == av.MaxSpeed {
			resp.Add("[{spd} or greater|at or above {spd}]", sr.Range[0])
		} else if sr.Range[0] == 0 && sr.Range[1] < av.MaxSpeed {
			resp.Add("[not exceeding {spd}|at or below {spd}]", sr.Range[1])
		}
	}

	return &resp
}

// firstCrossingRestriction finds the first controller-assigned crossing
// restriction in the remaining waypoints.
func (nav *Nav) firstCrossingRestriction() *contactCrossingRestriction {
	for _, wp := range nav.AssignedWaypoints() {
		if wp.SyntheticCrossing() {
			if ar, sr := wp.AltitudeRestriction(), wp.SpeedRestriction(); ar != nil || sr != nil {
				return &contactCrossingRestriction{
					Fix:            wp.Fix,
					AltRestriction: ar,
					Speed:          sr,
				}
			}
		}

		fa, ok := nav.FixAssignments[wp.Fix]
		if !ok || (fa.Arrive.Altitude == nil && fa.Arrive.Speed == nil) {
			continue
		}

		return &contactCrossingRestriction{
			Fix:            wp.Fix,
			AltRestriction: fa.Arrive.Altitude,
			Speed:          fa.Arrive.Speed,
		}
	}
	return nil
}

// addStarAltitude adds combined STAR + altitude phraseology.
func (nav *Nav) addStarAltitude(rt *av.RadioTransmission, star string, crossing *contactCrossingRestriction) {
	hasAltRestrictions := nav.procedureHasAltRestrictions(false)
	cur := nav.FlightState.Altitude
	descending := nav.Altitude.Assigned == nil && hasAltRestrictions

	if descending && crossing != nil && crossing.AltRestriction != nil {
		// Descending via STAR with a controller fix assignment exception
		format, args := crossingInstructionFormat(crossing, crossingAltitude(crossing), true)
		rt.Add("[leaving|out of] {alt} descending via [the|] {star} [arrival|] except "+format,
			append([]any{cur, star}, args...)...)
	} else if descending {
		// Descending via STAR, no override
		rt.Add("[leaving|out of] {alt} descending via the {star} [arrival|]", cur, star)
	} else if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != cur {
		// On STAR with controller-assigned altitude
		rt.Add("on the {star} [at|] {alt} for {alt} [assigned|]", star, cur, *nav.Altitude.Assigned)
	} else {
		// On STAR at altitude
		rt.Add("on the {star} [at|] {alt}", star, cur)
	}

	if crossing != nil && crossing.AltRestriction == nil {
		format, args := crossingInstructionFormat(crossing, 0, false)
		rt.Add(format, args...)
	}
}

// addContactAltitude adds altitude phraseology for non-STAR contexts (vectored, departures, etc.).
func (nav *Nav) addContactAltitude(rt *av.RadioTransmission, star string, crossing *contactCrossingRestriction) {
	cur := nav.FlightState.Altitude

	if crossing != nil && crossing.AltRestriction != nil && star == "" {
		// Fix crossing restriction without a STAR
		format, args := crossingInstructionFormat(crossing, crossingAltitude(crossing), true)
		rt.Add("[leaving|out of] {alt} "+format, append([]any{cur}, args...)...)
	} else if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != cur {
		nav.addAltitudePhrasing(rt, *nav.Altitude.Assigned)
	} else if target, ok := nav.findAltitudeTarget(); ok {
		alt := target.altitude
		if nav.Altitude.Cleared != nil {
			alt = min(alt, *nav.Altitude.Cleared)
		}
		if cur != alt {
			nav.addAltitudePhrasing(rt, alt)
		} else {
			rt.Add("[at|] {alt}", cur)
		}
	} else {
		rt.Add("[at|] {alt}", cur)
	}

	if crossing != nil && crossing.AltRestriction == nil {
		format, args := crossingInstructionFormat(crossing, 0, false)
		rt.Add(format, args...)
	}
}

func crossingAltitude(crossing *contactCrossingRestriction) float32 {
	alt := crossing.AltRestriction.Range[0]
	if crossing.AltRestriction.Range[1] != av.MaxAltitude {
		alt = crossing.AltRestriction.Range[1]
	}
	return alt
}

func crossingInstructionFormat(crossing *contactCrossingRestriction, altitude float32, includeAltitude bool) (string, []any) {
	targetFmt := "{fix}"
	speedFmt, speedArgs := crossingSpeedFormat(crossing)

	var format string
	args := []any{crossing.Fix}
	if includeAltitude {
		format = "[to cross|crossing] " + targetFmt + " at {alt}" + speedFmt
		args = append(args, altitude)
	} else {
		format = "[to cross|crossing] " + targetFmt + speedFmt
	}
	args = append(args, speedArgs...)
	return format, args
}

func crossingSpeedFormat(crossing *contactCrossingRestriction) (string, []any) {
	if crossing.Speed != nil {
		if crossing.Speed.IsMach {
			mach, _ := crossing.Speed.ExactValue()
			return " and {mach}", []any{mach}
		}
		if spd, exact := crossing.Speed.ExactValue(); exact {
			return " and {spd}", []any{spd}
		}
		if crossing.Speed.Range[0] > 0 && crossing.Speed.Range[1] == av.MaxSpeed {
			return " at {spd} or greater", []any{crossing.Speed.Range[0]}
		}
		if crossing.Speed.Range[0] == 0 && crossing.Speed.Range[1] < av.MaxSpeed {
			return " at {spd} or less", []any{crossing.Speed.Range[1]}
		}
		return " between {spd} and {spd}", []any{crossing.Speed.Range[0], crossing.Speed.Range[1]}
	}
	return "", nil
}

func (nav *Nav) rateSummary() string {
	var s string
	switch nav.Altitude.Rate {
	case RateGood:
		s = ", good rate"
	case RateExpedite:
		s = ", expediting"
	}
	if s != "" && nav.Altitude.RateThrough != nil {
		s += " through " + av.FormatAltitude(*nav.Altitude.RateThrough)
	}
	return s
}

func (nav *Nav) DivertToAirport(airport string) {
	ap := av.DB.Airports[airport]

	wp := av.Waypoint{
		Fix:      airport,
		Location: ap.Location,
	}
	nav.Waypoints = []av.Waypoint{wp}

	nav.FlightState.ArrivalAirport = wp
	nav.FlightState.ArrivalAirportLocation = ap.Location
	nav.FlightState.ArrivalAirportElevation = float32(ap.Elevation)
}

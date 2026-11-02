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
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
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

type UpdateResult struct {
	PassedWaypoint *av.Waypoint
	ActionEvents   []av.WaypointActionEvent
}

// DeferredNavHeading stores a heading assignment from the controller and the
// time at which to start executing it; this time is set to be a few
// seconds after the controller issues it in order to model the delay
// before pilots start to follow assignments.
type DeferredNavHeading struct {
	Time      Time
	Heading   *math.MagneticHeading
	Turn      *av.TurnDirection
	Hold      *FlyHold
	Maneuvers []LateralManeuver
	Join      *RouteJoin
	Waypoints []av.Waypoint

	// SnapshotAltitudeOnEffect, when true, causes the current altitude to be captured into
	// Altitude.Cleared, as a floor, when this deferred heading takes effect. Used when vectoring an
	// arrival off a STAR/approach with no issued altitude so the aircraft holds the current alt.
	SnapshotAltitudeOnEffect bool
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

type RateQualifier int

const (
	RateNormal   RateQualifier = iota
	RateGood                   // faster than normal, not maximum
	RateExpedite               // maximum rate
)

type Altitude struct {
	Assigned        *float32 // controller-assigned altitude (not yet in autopilot)
	ActiveAssigned  *float32 // assigned altitude currently used for vertical guidance
	ActivateAt      Time     // non-zero while Assigned is pending activation
	Cleared         *ClearedAltitude
	AfterSpeed      *float32
	AfterSpeedSpeed *av.Airspeed
	Rate            RateQualifier
	RateThrough     *float32                // revert to RateNormal after passing this altitude; nil = all the way
	RateAfterSpeed  RateQualifier           // preserved across speed transitions
	Restriction     *av.AltitudeRestriction // The restriction at the last waypoint passed.

}

// ClearedAltitude is the altitude an aircraft flying its route's altitude
// restrictions is cleared to: the restrictions never take it above that
// altitude, and if IsFloor, never below it either. Descend via clearances and
// an arrival's initial clearance are floors; an approach clearance ends a
// floor.
type ClearedAltitude struct {
	Altitude float32
	IsFloor  bool
}

type Speed struct {
	Assigned                 *av.SpeedRestriction // controller-assigned (exact or range)
	AfterAltitude            *av.SpeedRestriction // speed to apply after reaching assigned altitude
	AfterAltitudeAltitude    *float32
	MaintainSlowestPractical bool
	MaintainMaximumForward   bool
	Restriction              *av.SpeedRestriction // Carried after passing a waypoint
}

type Heading struct {
	Assigned   *math.MagneticHeading
	Turn       *av.TurnDirection
	Arc        *av.DMEArc
	JoiningArc bool
	Maneuvers  []LateralManeuver
	Hold       *FlyHold
	Join       *RouteJoin // Maneuvers we're flying the heading to join, if set

}

type Approach struct {
	Assigned                    *av.Approach
	AssignedId                  string
	ATPAVolume                  *av.ATPAVolume
	Cleared                     bool
	StandbyApproach             bool // suppress repeated approach clearance requests
	RequestApproachClearance    bool // pilot should radio for approach clearance
	GoAroundNoApproachClearance bool // pilot should go around (reached FAF without clearance)
	RequestVectors              bool // pilot should request vectors (overshot localizer)
	RequestAltitude             bool // pilot should request altitude after being vectored off STAR/approach
	MissedApproachIntercept     bool // bad vectors -> pilot was unable to intercept
	ApproachClearanceCancelled  bool // controller canceled approach clearance
	InterceptState              InterceptState
	PassedApproachFix           bool // have we passed a fix on the approach yet?
	PassedFAF                   bool
	NoPT                        bool
	AtFixClearedRoute           []av.Waypoint
	AtFixInterceptFix           string           // fix where aircraft should intercept the localizer
	InterceptCourseLine         [2]math.Point2LL // cached course line for non-ILS intercepts
	InterceptWaypoints          []av.Waypoint    // cached remaining waypoints for non-ILS intercepts

	VisualReferences     []*av.Approach // Reference approaches for non-charted visual approach
	InterceptedReference *av.Approach   // Approach giving the committed-to route for a non-charted visual approach
}

// EffectivelyCleared reports whether the aircraft has been cleared for the
// approach, either immediately or via an "at fix" clearance that hasn't yet
// triggered at the fix.
func (na *Approach) EffectivelyCleared() bool {
	return na.Cleared || na.AtFixClearedRoute != nil
}

// HasLocalizer reports whether the aircraft is currently flying localizer-style
// course geometry.
func (na *Approach) HasLocalizer() bool {
	return (na.Assigned != nil && na.Assigned.Type.HasLocalizer()) ||
		(na.InterceptedReference != nil && na.InterceptedReference.Type.HasLocalizer())
}

// clearedForVisualApproach reports whether the aircraft has been cleared for
// a visual approach, either one charted as a procedure or one synthesized for
// the runway.
func (nav *Nav) clearedForVisualApproach() bool {
	if !nav.Approach.Cleared || nav.Approach.Assigned == nil {
		return false
	}
	t := nav.Approach.Assigned.Type
	return t == av.VisualApproach || t == av.ChartedVisualApproach
}

// descendsOnGlidepath reports whether the aircraft descends on the exact
// geometric path to its next altitude restriction rather than holding
// level until it has to start down: past the FAF, cleared for a visual
// approach, or on a VFR landing's base, final, or straight-in legs.
func (nav *Nav) descendsOnGlidepath() bool {
	if nav.Approach.PassedFAF || nav.clearedForVisualApproach() {
		return true
	}
	if len(nav.Waypoints) == 0 {
		return false
	}
	switch nav.Waypoints[0].VFRPhase {
	case av.VFRPhaseBase, av.VFRPhaseFinal, av.VFRPhaseStraightIn:
		return true
	default:
		return false
	}
}

// hasDeferredRoute reports whether a controller-issued route assignment is
// pending (queued in DeferredNavHeading) but not yet active.
func (nav *Nav) hasDeferredRoute() bool {
	return nav.DeferredNavHeading != nil && len(nav.DeferredNavHeading.Waypoints) > 0
}

type FixAssignment struct {
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

type InterceptState int

const (
	NotIntercepting InterceptState = iota
	InitialHeading
	TurningToJoin
	OnApproachCourse
)

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

		alt := float32(rand.SampleSlice(nav.Rand, arr.InitialAltitudes))
		nav.FinalAltitude = max(nav.FinalAltitude, alt)
		nav.FlightState.Altitude = alt
		wxs := model.Lookup(nav.FlightState.Position, alt, simTime.Time())
		nav.FlightState.IAS = min(arr.InitialSpeed.IAS(alt), nav.maxIAS(wxs.Temperature()))
		// This won't be quite right but it's better than leaving GS to be
		// 0 for the first nav update tick which leads to various Inf and
		// NaN cases...
		nav.FlightState.GS = nav.FlightState.IAS

		return nav
	}
	return nil
}

func MakeDepartureNav(callsign av.ADSBCallsign, depAirport, arrAirport av.ICAOAirportCode, cruiseAlt int,
	perf av.AircraftPerformance, delays PilotDelays, assignedAlt, clearedAlt int, wp av.WaypointArray,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime Time,
	r *rand.Rand, lg *log.Logger) *Nav {
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

		alt := float32(rand.SampleSlice(nav.Rand, of.InitialAltitudes))
		nav.FlightState.Altitude = alt
		wxs := model.Lookup(nav.FlightState.Position, alt, simTime.Time())
		nav.FlightState.IAS = min(of.InitialSpeed.IAS(alt), nav.maxIAS(wxs.Temperature()))
		// This won't be quite right but it's better than leaving GS to be
		// 0 for the first nav update tick which leads to various Inf and
		// NaN cases...
		nav.FlightState.GS = nav.FlightState.IAS

		return nav
	}
	return nil
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

// TAS returns the aircraft's true airspeed in knots.
func (nav *Nav) TAS(temp av.Temperature) float32 {
	return nav.FlightState.TAS(temp)
}

func (nav *Nav) Mach() float32 {
	return av.IASToMach(nav.FlightState.IAS, nav.FlightState.Altitude)
}

// vmoCrossoverAltitude is a typical altitude at which a jet's maximum
// operating speed (V_MO) and its maximum Mach number give the same
// indicated airspeed. The performance database doesn't provide V_MO, so it
// is approximated as the maximum Mach number's IAS there.
const vmoCrossoverAltitude = 28000

// maxIAS returns the fastest indicated airspeed the aircraft can fly at
// its current altitude.
func (nav *Nav) maxIAS(temp av.Temperature) float32 {
	alt := nav.FlightState.Altitude
	ias := av.TASToIAS(nav.Perf.Speed.MaxTAS, alt, temp)
	if mach := nav.Perf.Speed.MaxMach; mach != 0 {
		if nav.Perf.Engine.AircraftType == "J" {
			alt = max(alt, vmoCrossoverAltitude)
		}
		ias = min(ias, av.MachToIAS(mach, alt))
	}
	return ias
}

// minIAS returns the slowest indicated airspeed the aircraft will fly.
// The performance database sometimes has a minimum speed above the landing
// speed; the landing speed wins so the aircraft can slow to it on final.
func (nav *Nav) minIAS() float32 {
	return min(nav.Perf.Speed.Min, nav.Perf.Speed.Landing)
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

// intendedHeading returns the true heading of the direction the aircraft's
// instructions are taking it: an assigned heading if it has one (including
// one the pilot hasn't started flying yet), otherwise the bearing to the next
// waypoint of its assigned route (which may also still be pending pilot
// response). The present heading answers the same question only once the
// pilot has finished turning, which is not yet the case in the seconds after
// a "direct {fix}" or mid-turn thereafter.
func (nav *Nav) intendedHeading() math.TrueHeading {
	magVar := nav.FlightState.MagneticVariation
	if hdg, ok := nav.AssignedHeading(); ok {
		return math.MagneticToTrue(hdg, magVar)
	}
	// In a hold, on a DME arc, or flying maneuvers, the route says nothing
	// about the current direction of flight.
	if nav.Heading.Hold == nil && nav.Heading.Arc == nil && len(nav.Heading.Maneuvers) == 0 {
		// Take the bearing to the first waypoint far enough away to give a
		// meaningful one: the aircraft sequences straight past any it is
		// sitting on top of.
		wps := nav.AssignedWaypoints()
		far := func(wp av.Waypoint) bool {
			return math.NMDistance2LLFast(nav.FlightState.Position, wp.Location,
				nav.FlightState.NmPerLongitude) > 0.5
		}
		if i := slices.IndexFunc(wps, far); i != -1 {
			return math.Heading2LL(nav.FlightState.Position, wps[i].Location, nav.FlightState.NmPerLongitude)
		}
	}
	return math.MagneticToTrue(nav.FlightState.Heading, magVar)
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
	if len(nav.Waypoints) > 0 {
		if h, ok := nav.Waypoints[0].HeadingAction(); ok && h.Heading != 0 {
			return int(h.Heading), TurningToHeading
		}
	}
	return 0, NoHeading
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

// onHeading reports whether the aircraft is flying a heading: one it was
// assigned, or a heading leg of a procedure, like a SID's after the climb
// off the runway.
func (nav *Nav) onHeading() bool {
	if nav.Heading.Assigned != nil {
		return true
	}
	if len(nav.Heading.Maneuvers) == 0 {
		return false
	}
	m := nav.Heading.Maneuvers[0]
	return m.FlyToward.IsZero() && m.TrackFrom.IsZero() && m.Track == 0
}

// EnqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment. delayReduction, the time
// since the pilot heard the instruction, is subtracted from the
// pilot-reaction delay (floored at zero).
func (nav *Nav) EnqueueHeading(hdg math.MagneticHeading, turn av.TurnDirection, approachCleared bool, simTime Time, delayReduction time.Duration) {
	defer nav.record("EnqueueHeading", hdg, turn, approachCleared, simTime, delayReduction)()
	var d time.Duration
	if approachCleared {
		// Minimal delay if the aircraft has been cleared for an approach.
		d = nav.drawDelay(nav.Delays.HeadingApproachCleared)
	} else if nav.onHeading() && nav.DeferredNavHeading == nil {
		// Already flying a heading; minimal delay.
		d = nav.drawDelay(nav.Delays.HeadingFromHeading)
	} else {
		// LNAV -> heading mode
		d = nav.drawDelay(nav.Delays.HeadingFromLNAV)
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
	if nav.hasDeferredRoute() {
		return nav.DeferredNavHeading.Waypoints
	}
	return nav.Waypoints
}

// setAssignedWaypoints replaces the route returned by AssignedWaypoints.
func (nav *Nav) setAssignedWaypoints(wps []av.Waypoint) {
	if nav.hasDeferredRoute() {
		nav.DeferredNavHeading.Waypoints = wps
	} else {
		nav.Waypoints = wps
	}
}

// editAssignedWaypoints returns the assigned-route slice and a commit function
// that writes a (possibly mutated) slice back to the underlying field. Callers
// should reassign their local slice variable when using slices.Delete/Insert
// and pass the current value to commit.
func (nav *Nav) editAssignedWaypoints() ([]av.Waypoint, func([]av.Waypoint)) {
	return nav.AssignedWaypoints(), nav.setAssignedWaypoints
}

// AmendRoute replaces the waypoints of the assigned route before the one at
// fix with wps, as a flight plan amendment does. It reports false, leaving
// the route alone, if fix isn't on it.
// FlyRoute replaces the aircraft's route with wps and has it fly the route
// from where it is, dropping any heading it was flying.
func (nav *Nav) FlyRoute(wps []av.Waypoint) {
	defer nav.record("FlyRoute", wps)()
	nav.Waypoints = wps
	nav.Heading = Heading{}
}

// FlyFromRunway has an aircraft that is back on the runway after a
// touch-and-go take off again along wps, with its speed and altitude managed
// as a departure's are.
func (nav *Nav) FlyFromRunway(wps []av.Waypoint) {
	defer nav.record("FlyFromRunway", wps)()
	nav.Waypoints = wps
	nav.Heading = Heading{}
	nav.FlightState.InitialDepartureClimb = true
}

func (nav *Nav) AmendRoute(wps []av.Waypoint, fix string) bool {
	defer nav.record("AmendRoute", wps, fix)()
	route := nav.AssignedWaypoints()
	idx := slices.IndexFunc(route, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if idx == -1 {
		return false
	}
	nav.setAssignedWaypoints(slices.Concat(wps, route[idx:]))
	return true
}

func (nav *Nav) EnqueueDirectFix(wps []av.Waypoint, turn av.TurnDirection, simTime Time, delayReduction time.Duration) {
	defer nav.record("EnqueueDirectFix", wps, turn, simTime, delayReduction)()
	var d time.Duration
	if len(wps) > 0 && nav.ExpectedDirectFix == wps[0].Fix {
		// Pilot was told to expect this fix; shorter delay
		d = nav.drawDelay(nav.Delays.DirectExpected)
		nav.ExpectedDirectFix = ""
	} else if nav.Heading.Assigned == nil && nav.DeferredNavHeading == nil {
		// Already in LNAV mode; have less of a delay
		d = nav.drawDelay(nav.Delays.DirectFromLNAV)
	} else {
		// heading->LNAV--longer delay
		d = nav.drawDelay(nav.Delays.DirectFromHeading)
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

func (nav *Nav) EnqueueOnCourse(simTime Time, delayReduction time.Duration) {
	defer nav.record("EnqueueOnCourse", simTime, delayReduction)()
	d := max(nav.drawDelay(nav.Delays.OnCourse)-delayReduction, 0)
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time: simTime.Add(d),
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

// courseCaptureTolerance is how close to a course an aircraft turning to
// join it has to get to be considered established on it, in nm.
const courseCaptureTolerance = 0.2

// minInterceptAngle is the shallowest angle at which a track counts as
// converging on a course; anything less only creeps toward it.
const minInterceptAngle = 10 // degrees

// onCourseLine checks if the flight position is less than maxNmDeviation
// from the infinite line defined by the two given points.
func (nav *Nav) onCourseLine(line [2]math.Point2LL, maxNmDeviation float32) bool {
	return math.Abs(nav.signedCourseLineDistance(line)) < maxNmDeviation
}

// signedCourseLineDistance returns the flight position's distance in nm from
// the infinite line defined by the two given points; it is positive when the
// aircraft is to the right of the line's direction.
func (nav *Nav) signedCourseLineDistance(line [2]math.Point2LL) float32 {
	return math.SignedPointLineDistance(
		math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.LL2NM(line[0], nav.FlightState.NmPerLongitude),
		math.LL2NM(line[1], nav.FlightState.NmPerLongitude))
}

// closingOnCourseLine reports whether the aircraft's ground track is
// converging on the infinite line defined by the two given points at more
// than a glancing angle.
func (nav *Nav) closingOnCourseLine(line [2]math.Point2LL, wxs wx.Sample) bool {
	fs := &nav.FlightState

	// The ground velocity, as the flight model integrates it: the heading
	// flown at TAS, displaced by the wind.
	v := math.Scale2f(math.HeadingVector(math.MagneticToTrue(fs.Heading, fs.MagneticVariation)),
		nav.TAS(wxs.Temperature())/3600)
	v = math.Add2f(v, nav.groundWind(wxs))

	// The track has to be angled back toward the side of the course the
	// aircraft is on, and by enough to actually get there.
	course := math.Heading2LL(line[0], line[1], fs.NmPerLongitude)
	angle := math.HeadingSignedTurn(course, math.VectorHeading(v))
	return angle*nav.signedCourseLineDistance(line) < 0 && math.Abs(angle) >= minInterceptAngle
}

// OnExtendedCenterline checks if the flight position is less than
// maxNmDeviation from the assigned approach's final approach course.
func (nav *Nav) OnExtendedCenterline(maxNmDeviation float32) bool {
	approach := nav.Approach.Assigned
	if approach == nil {
		return false
	}

	cl := approach.FinalApproachCourseLine(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	return nav.onCourseLine(cl, maxNmDeviation)
}

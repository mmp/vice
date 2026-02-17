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

	FixAssignments map[string]NavFixAssignment

	// DeferredNavHeading stores a heading/direct fix assignment from the
	// controller that the pilot has not yet started to follow.  Note that
	// only a single such assignment is stored; for example, if the
	// controller issues a first heading and then a second shortly
	// afterward, before the first has been followed, it's fine for the
	// second to override it.
	DeferredNavHeading *DeferredNavHeading

	FinalAltitude float32
	Waypoints     av.WaypointArray

	Rand *rand.Rand
}

// DeferredNavHeading stores a heading assignment from the controller and the
// time at which to start executing it; this time is set to be a few
// seconds after the controller issues it in order to model the delay
// before pilots start to follow assignments.
type DeferredNavHeading struct {
	Time    time.Time
	Heading *float32
	Turn    *TurnMethod
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
	Heading      float32
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

type NavAltitude struct {
	Assigned           *float32 // controller assigned
	Cleared            *float32 // from initial clearance
	AfterSpeed         *float32
	AfterSpeedSpeed    *float32
	Expedite           bool
	ExpediteAfterSpeed bool

	// Carried after passing a waypoint if we were unable to meet the
	// restriction at the way point; we keep trying until we get there (or
	// are given another instruction..)
	Restriction *av.AltitudeRestriction
}

type NavSpeed struct {
	Assigned                 *float32
	AfterAltitude            *float32
	AfterAltitudeAltitude    *float32
	MaintainSlowestPractical bool
	MaintainMaximumForward   bool
	// Carried after passing a waypoint
	Restriction *float32
}

const MaxIAS = 290

type NavHeading struct {
	Assigned     *float32
	Turn         *TurnMethod
	Arc          *av.DMEArc
	JoiningArc   bool
	RacetrackPT  *FlyRacetrackPT
	Standard45PT *FlyStandard45PT
	Hold         *FlyHold
}

type NavApproach struct {
	Assigned          *av.Approach
	AssignedId        string
	ATPAVolume        *av.ATPAVolume
	Cleared           bool
	InterceptState    InterceptState
	PassedApproachFix bool // have we passed a fix on the approach yet?
	PassedFAF         bool
	NoPT              bool
	AtFixClearedRoute []av.Waypoint
	AtFixInterceptFix string // fix where aircraft should intercept the localizer
}

type NavFixAssignment struct {
	Arrive struct {
		Altitude *av.AltitudeRestriction
		Speed    *float32
	}
	Depart struct {
		Fix     *av.Waypoint
		Heading *float32
	}
	Hold *av.Hold
}

type NavAirwork struct {
	Radius   float32
	Center   math.Point2LL
	AltRange [2]float32

	RemainingSteps  int
	NextMoveCounter int
	Heading         float32
	TurnRate        float32
	TurnDirection   TurnMethod
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
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime time.Time, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == av.FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, arr.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, model, simTime, lg); nav != nil {
		spd := arr.SpeedRestriction
		nav.Speed.Restriction = util.Select(spd != 0, &spd, nil)
		if arr.AssignedAltitude > 0 {
			// Descend to the assigned altitude but then hold that until
			// either DVS or further descents are given.
			alt := arr.AssignedAltitude
			nav.Altitude.Assigned = &alt
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
	assignedAlt, clearedAlt, speedRestriction int, wp []av.Waypoint, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime time.Time, lg *log.Logger) *Nav {
	if nav := makeNav(callsign, fp, perf, wp, randomizeAltitudeRange, nmPerLongitude, magneticVariation,
		model, simTime, lg); nav != nil {
		if assignedAlt != 0 {
			alt := float32(min(assignedAlt, fp.Altitude))
			nav.Altitude.Assigned = &alt
		} else {
			alt := float32(min(clearedAlt, fp.Altitude))
			nav.Altitude.Cleared = &alt
		}
		if speedRestriction != 0 {
			speed := float32(max(speedRestriction, int(perf.Speed.Min)))
			nav.Speed.Restriction = &speed
		}
		nav.FlightState.InitialDepartureClimb = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func MakeOverflightNav(callsign av.ADSBCallsign, of *av.Overflight, fp av.FlightPlan, perf av.AircraftPerformance,
	nmPerLongitude float32, magneticVariation float32, model *wx.Model, simTime time.Time, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == av.FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, of.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, model, simTime, lg); nav != nil {
		spd := of.SpeedRestriction
		nav.Speed.Restriction = util.Select(spd != 0, &spd, nil)
		if of.AssignedAltitude > 0 {
			alt := of.AssignedAltitude
			nav.Altitude.Assigned = &alt
		}
		if of.AssignedSpeed > 0 {
			spd := of.AssignedSpeed
			nav.Speed.Assigned = &spd
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
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, model *wx.Model,
	simTime time.Time, lg *log.Logger) *Nav {
	nav := &Nav{
		Perf:           perf,
		FinalAltitude:  float32(fp.Altitude),
		FixAssignments: make(map[string]NavFixAssignment),
		Rand:           rand.Make(),
	}

	// Copy the provided waypoints so that any local modifications we make don't pollute the
	// waypoints stored for the scenario. Try to size the allocation so that reallocation
	// isn't necessary: for IFR we just have the destination airport to add but for VFR we may
	// need a number of extra ones to join the pattern and land.
	nav.Waypoints = make([]av.Waypoint, len(wp)+util.Select(fp.Rules == av.FlightRulesIFR, 1, 12))
	copy(nav.Waypoints, wp)

	av.RandomizeRoute(nav.Waypoints, nav.Rand, randomizeAltitudeRange, nav.Perf, nmPerLongitude,
		magneticVariation, fp.ArrivalAirport, lg)

	landIdx := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Land })
	if landIdx != -1 {
		if fp.Rules == av.FlightRulesIFR {
			lg.Warn("IFR aircraft has /land in route", slog.Any("waypoints", nav.Waypoints),
				slog.Any("flightplan", fp))
		} else {
			ap := av.DB.Airports[fp.ArrivalAirport]
			as := model.Lookup(ap.Location, float32(ap.Elevation), simTime)
			nav.Waypoints = av.AppendVFRLanding(nav.Waypoints[:landIdx+1], nav.Perf, fp.ArrivalAirport,
				as.WindDirection(), nmPerLongitude, magneticVariation, lg)
		}
	}

	nav.FlightState = FlightState{
		MagneticVariation: magneticVariation,
		NmPerLongitude:    nmPerLongitude,
		Position:          nav.Waypoints[0].Location,
		Heading:           float32(nav.Waypoints[0].Heading),
	}

	if nav.FlightState.Position.IsZero() {
		lg.Errorf("uninitialized initial waypoint position! %+v", nav.Waypoints[0])
		return nil
	}

	if nav.FlightState.Heading == 0 { // unassigned, so get the heading using the next fix
		nav.FlightState.Heading = math.Heading2LL(nav.FlightState.Position,
			nav.Waypoints[1].Location, nav.FlightState.NmPerLongitude,
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

		// Squirrel away the arrival airport as a fix and add it to the end
		// of the waypoints.
		nav.FlightState.ArrivalAirport = av.Waypoint{
			Fix:      fp.ArrivalAirport,
			Location: ap.Location,
		}
		nav.Waypoints = append(nav.Waypoints, nav.FlightState.ArrivalAirport)
	}

	return nav
}

func (nav *Nav) TAS() float32 {
	tas := av.IASToTAS(nav.FlightState.IAS, nav.FlightState.Altitude)
	tas = min(tas, nav.Perf.Speed.CruiseTAS)
	return tas
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
func (nav *Nav) AssignedHeading() (float32, bool) {
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
		return nav.Waypoints[0].Heading, TurningToHeading
	}
	return 0, NoHeading
}

// EnqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment.
func (nav *Nav) EnqueueHeading(hdg float32, turn TurnMethod, simTime time.Time) {
	var delay float32
	if nav.Heading.Assigned != nil && nav.DeferredNavHeading == nil {
		// Already in heading mode; have less of a delay.
		delay = 4 + 3*nav.Rand.Float32()
	} else {
		// LNAV -> heading mode--longer delay but not as long as heading->LNAV
		delay = 5 + 4*nav.Rand.Float32()
	}

	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:    simTime.Add(time.Duration(delay * float32(time.Second))),
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

func (nav *Nav) EnqueueDirectFix(wps []av.Waypoint, simTime time.Time) {
	var delay float32
	if nav.Heading.Assigned == nil && nav.DeferredNavHeading == nil {
		// Already in LNAV mode; have less of a delay
		delay = 4 + 3*nav.Rand.Float32()
	} else {
		// heading->LNAV--longer delay
		delay = 8 + 5*nav.Rand.Float32()
	}

	nav.DeferredNavHeading = &DeferredNavHeading{
		Time:      simTime.Add(time.Duration(delay * float32(time.Second))),
		Waypoints: wps,
	}
}

func (nav *Nav) EnqueueOnCourse(simTime time.Time) {
	delay := 8 + 5*nav.Rand.Float32()
	nav.DeferredNavHeading = &DeferredNavHeading{
		Time: simTime.Add(time.Duration(delay * float32(time.Second))),
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
		if r := wp.AltitudeRestriction; r != nil {
			return nav.FlightState.Altitude >= r.TargetAltitude(nav.FlightState.Altitude)
		}
	}
	return false
}

// OnExtendedCenterline checks if the flight position is less than maxNmDeviation
// from the infinite line defined by the assigned approach localizer
func (nav *Nav) OnExtendedCenterline(maxNmDeviation float32) bool {
	approach := nav.Approach.Assigned
	if approach == nil {
		return false
	}

	cl := approach.ExtendedCenterline(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	distance := math.PointLineDistance(
		math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.LL2NM(cl[0], nav.FlightState.NmPerLongitude),
		math.LL2NM(cl[1], nav.FlightState.NmPerLongitude))

	return distance < maxNmDeviation
}

///////////////////////////////////////////////////////////////////////////
// Communication

// Full human-readable summary of nav state for use when paused and mouse
// hover on the scope
func (nav *Nav) Summary(fp av.FlightPlan, model *wx.Model, simTime time.Time, lg *log.Logger) string {
	var lines []string
	lines = append(lines, "Departure from "+fp.DepartureAirport+" to "+fp.ArrivalAirport)

	if nav.Altitude.Assigned != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Assigned) < 100 {
			lines = append(lines, "At assigned altitude "+
				av.FormatAltitude(*nav.Altitude.Assigned))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(*nav.Altitude.Assigned)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
			lines = append(lines, line)
		}
	} else if nav.Altitude.AfterSpeed != nil {
		dir := util.Select(*nav.Altitude.AfterSpeed > nav.FlightState.Altitude, "climb", "descend")
		exped := util.Select(nav.Altitude.ExpediteAfterSpeed, ", expediting", "")
		lines = append(lines, fmt.Sprintf("At %.0f kts, %s to %s"+exped,
			*nav.Altitude.AfterSpeedSpeed, dir, av.FormatAltitude(*nav.Altitude.AfterSpeed)))
	} else if c, ok := nav.getWaypointAltitudeConstraint(); ok && !nav.flyingPT() {
		dir := util.Select(c.Altitude > nav.FlightState.Altitude, "Climbing", "Descending")
		alt := c.Altitude
		if nav.Altitude.Cleared != nil {
			alt = min(alt, *nav.Altitude.Cleared)
		}
		lines = append(lines, dir+" to "+av.FormatAltitude(alt)+" for alt. restriction at "+c.Fix)
	} else if nav.Altitude.Cleared != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Cleared) < 100 {
			lines = append(lines, "At cleared altitude "+
				av.FormatAltitude(*nav.Altitude.Cleared))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(*nav.Altitude.Cleared)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
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
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime)
	lines = append(lines, wxs.String())
	if nav.FlightState.Altitude > nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Weather-based climb rate factor %.2fx", nav.atmosClimbFactor(wxs)))
	}

	// Speed; don't be as exhaustive as we are for altitude
	targetAltitude, _ := nav.TargetAltitude()
	lines = append(lines, fmt.Sprintf("IAS %d GS %d TAS %d", int(nav.FlightState.IAS),
		int(nav.FlightState.GS), int(nav.TAS())))
	ias, _ := nav.TargetSpeed(targetAltitude, &fp, wxs, nil)
	if nav.Speed.MaintainSlowestPractical {
		lines = append(lines, fmt.Sprintf("Maintain slowest practical speed: %.0f kts", ias))
	} else if nav.Speed.MaintainMaximumForward {
		lines = append(lines, fmt.Sprintf("Maintain maximum forward speed: %.0f kts", ias))
	} else if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	} else if nav.Speed.Assigned != nil {
		lines = append(lines, fmt.Sprintf("Maintaining %.0f kts assignment", *nav.Speed.Assigned))
	} else if nav.Speed.AfterAltitude != nil && nav.Speed.AfterAltitudeAltitude != nil {
		lines = append(lines, fmt.Sprintf("At %s, maintain %0.f kts", av.FormatAltitude(*nav.Speed.AfterAltitudeAltitude),
			*nav.Speed.AfterAltitude))
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
				line += "at " + fmt.Sprintf("%.0f kts", *nfa.Arrive.Speed)
			}
			lines = append(lines, line)
		}
		if nfa.Depart.Heading != nil && nav.Heading.Assigned == nil {
			lines = append(lines, fmt.Sprintf("Depart "+fix+" heading %03d",
				int(*nfa.Depart.Heading)))
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

		if pt := nav.Heading.RacetrackPT; pt != nil {
			lines = append(lines,
				fmt.Sprintf("Fly the %s procedure turn at %s, %s entry", pt.ProcedureTurn.Type,
					pt.Fix, pt.Entry.String()))
			if pt.ProcedureTurn.ExitAltitude != 0 &&
				nav.FlightState.Altitude > float32(pt.ProcedureTurn.ExitAltitude) {
				lines = append(lines, fmt.Sprintf("Descend to %d in the procedure turn",
					int(pt.ProcedureTurn.ExitAltitude)))
			}
		}
		if pt := nav.Heading.Standard45PT; pt != nil {
			lines = append(lines, fmt.Sprintf("Fly the standard 45/180 procedure turn at %s", pt.Fix))
		}
	}

	lines = append(lines, "Route flying: "+av.WaypointArray(nav.Waypoints).Encode())
	if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
		lines = append(lines, "Route assigned: "+av.WaypointArray(dh.Waypoints).Encode())
	}

	return strings.Join(lines, "\n")
}

func (nav *Nav) DepartureMessage(reportHeading bool) *av.RadioTransmission {
	rt := &av.RadioTransmission{Type: av.RadioTransmissionContact}

	// Altitude information
	target := util.Select(nav.Altitude.Assigned != nil, nav.Altitude.Assigned, nav.Altitude.Cleared)
	if target != nil && *target-nav.FlightState.Altitude > 100 {
		// one of the two should be set, but just in case...
		rt.Add("[at|] {alt} climbing {alt}", nav.FlightState.Altitude, *target)
	} else {
		rt.Add("[at|] {alt}", nav.FlightState.Altitude)
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

func (nav *Nav) ContactMessage(reportingPoints []av.ReportingPoint, star string, reportHeading bool, isDeparture bool) *av.RadioTransmission {
	var resp av.RadioTransmission

	if isDeparture && reportHeading {
		// Departure with varied headings
		hdg, state := nav.DepartureHeading()
		switch state {
		case OnHeading:
			resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		case TurningToHeading:
			resp.Add("[turning to|about to turn to] [heading|] {hdg}", hdg)
		}
	} else if hdg, ok := nav.AssignedHeading(); ok && reportHeading {
		// Arrival being vectored
		resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
	} else if star != "" {
		if nav.Altitude.Assigned == nil {
			resp.Add("descending on the {star}", star)
		} else {
			resp.Add("on the {star}", star)
		}
	}

	if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude {
		resp.Add("[at|] {alt} for {alt} [assigned|]", nav.FlightState.Altitude, *nav.Altitude.Assigned)
	} else if c, ok := nav.getWaypointAltitudeConstraint(); ok && !nav.flyingPT() {
		alt := c.Altitude
		if nav.Altitude.Cleared != nil {
			alt = min(alt, *nav.Altitude.Cleared)
		}
		if nav.FlightState.Altitude != alt {
			resp.Add("[at|] {alt} for {alt}", nav.FlightState.Altitude, alt)
		} else {
			resp.Add("[at|] {alt}", nav.FlightState.Altitude)
		}
	} else {
		resp.Add("[at|] {alt}", nav.FlightState.Altitude)
	}

	if nav.Speed.Assigned != nil {
		resp.Add("assigned {spd}", *nav.Speed.Assigned)
	}

	return &resp
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

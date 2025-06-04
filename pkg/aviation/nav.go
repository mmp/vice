// pkg/aviation/nav.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

// State related to navigation. Pointers are used for optional values; nil
// -> unset/unspecified.
type Nav struct {
	FlightState FlightState
	Perf        AircraftPerformance
	Altitude    NavAltitude
	Speed       NavSpeed
	Heading     NavHeading
	Approach    NavApproach
	Airwork     *NavAirwork

	FixAssignments map[string]NavFixAssignment

	// DeferredHeading stores a heading assignment from the controller that
	// the pilot has not yet started to follow.  Note that only a single
	// such assignment is stored; if the controller issues a first heading
	// and then a second shortly afterward, before the first has been
	// followed, it's fine for the second to override it.
	DeferredHeading *DeferredHeading

	FinalAltitude float32
	Waypoints     WaypointArray

	Rand *rand.Rand
}

// DeferredHeading stores a heading assignment from the controller and the
// time at which to start executing it; this time is set to be a few
// seconds after the controller issues it in order to model the delay
// before pilots start to follow assignments.
type DeferredHeading struct {
	// Time is just plain old wallclock time; it should be sim time, but a
	// lot of replumbing would be required to have that available where
	// needed. The downsides are minor: 1. On quit and resume, any pending
	// assignments will generally be followed immediately, and 2. if the
	// sim rate is increased, the delay will end up being longer than
	// intended.
	Time    time.Time
	Heading NavHeading
}

type FlightState struct {
	InitialDepartureClimb     bool
	DepartureAirportLocation  math.Point2LL
	DepartureAirportElevation float32
	ArrivalAirport            Waypoint
	ArrivalAirportLocation    math.Point2LL
	ArrivalAirportElevation   float32

	MagneticVariation float32
	NmPerLongitude    float32

	Position  math.Point2LL
	Heading   float32
	Altitude  float32
	IAS, GS   float32 // speeds...
	BankAngle float32 // degrees
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
	Restriction *AltitudeRestriction
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
	Arc          *DMEArc
	JoiningArc   bool
	RacetrackPT  *FlyRacetrackPT
	Standard45PT *FlyStandard45PT
}

type NavApproach struct {
	Assigned          *Approach
	AssignedId        string
	ATPAVolume        *ATPAVolume
	Cleared           bool
	InterceptState    InterceptState
	PassedApproachFix bool // have we passed a fix on the approach yet?
	PassedFAF         bool
	NoPT              bool
	AtFixClearedRoute []Waypoint
}

type NavFixAssignment struct {
	Arrive struct {
		Altitude *AltitudeRestriction
		Speed    *float32
	}
	Depart struct {
		Fix     *Waypoint
		Heading *float32
	}
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

func MakeArrivalNav(callsign ADSBCallsign, arr *Arrival, fp FlightPlan, perf AircraftPerformance,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, arr.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, wind, lg); nav != nil {
		spd := arr.SpeedRestriction
		nav.Speed.Restriction = util.Select(spd != 0, &spd, nil)
		if arr.AssignedAltitude > 0 {
			// Descend to the assigned altitude but then hold that until
			// either DVS or further descents are given.
			alt := arr.AssignedAltitude
			nav.Altitude.Assigned = &alt
		}

		nav.FinalAltitude = math.Max(nav.FinalAltitude, arr.InitialAltitude)
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

func MakeDepartureNav(callsign ADSBCallsign, fp FlightPlan, perf AircraftPerformance,
	assignedAlt, clearedAlt, speedRestriction int, wp []Waypoint, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, lg *log.Logger) *Nav {
	if nav := makeNav(callsign, fp, perf, wp, randomizeAltitudeRange, nmPerLongitude, magneticVariation,
		wind, lg); nav != nil {
		if assignedAlt != 0 {
			alt := float32(math.Min(assignedAlt, fp.Altitude))
			nav.Altitude.Assigned = &alt
		} else {
			alt := float32(math.Min(clearedAlt, fp.Altitude))
			nav.Altitude.Cleared = &alt
		}
		if speedRestriction != 0 {
			speed := float32(math.Max(speedRestriction, int(perf.Speed.Min)))
			nav.Speed.Restriction = &speed
		}
		nav.FlightState.InitialDepartureClimb = true
		nav.FlightState.Altitude = nav.FlightState.DepartureAirportElevation
		return nav
	}
	return nil
}

func MakeOverflightNav(callsign ADSBCallsign, of *Overflight, fp FlightPlan, perf AircraftPerformance,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, lg *log.Logger) *Nav {
	randomizeAltitudeRange := fp.Rules == FlightRulesVFR
	if nav := makeNav(callsign, fp, perf, of.Waypoints, randomizeAltitudeRange, nmPerLongitude,
		magneticVariation, wind, lg); nav != nil {
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

func makeNav(callsign ADSBCallsign, fp FlightPlan, perf AircraftPerformance, wp []Waypoint, randomizeAltitudeRange bool,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, lg *log.Logger) *Nav {
	nav := &Nav{
		Perf:           perf,
		FinalAltitude:  float32(fp.Altitude),
		Waypoints:      util.DuplicateSlice(wp),
		FixAssignments: make(map[string]NavFixAssignment),
		Rand:           rand.Make(),
	}

	nav.Waypoints = RandomizeRoute(nav.Waypoints, nav.Rand, randomizeAltitudeRange, nav.Perf, nmPerLongitude,
		magneticVariation, fp.ArrivalAirport, wind, lg)

	if fp.Rules == FlightRulesIFR && slices.ContainsFunc(nav.Waypoints, func(wp Waypoint) bool { return wp.Land }) {
		lg.Warn("IFR aircraft has /land in route", slog.Any("waypoints", nav.Waypoints),
			slog.Any("flightplan", fp))
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
		func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if ap, ok := DB.Airports[fp.DepartureAirport]; !ok {
		lg.Errorf("%s: departure airport unknown", fp.DepartureAirport)
		return nil
	} else {
		nav.FlightState.DepartureAirportLocation = ap.Location
		nav.FlightState.DepartureAirportElevation = float32(ap.Elevation)
	}
	if ap, ok := DB.Airports[fp.ArrivalAirport]; !ok {
		lg.Errorf("%s: arrival airport unknown", fp.ArrivalAirport)
		return nil
	} else {
		nav.FlightState.ArrivalAirportLocation = ap.Location
		nav.FlightState.ArrivalAirportElevation = float32(ap.Elevation)

		// Squirrel away the arrival airport as a fix and add it to the end
		// of the waypoints.
		nav.FlightState.ArrivalAirport = Waypoint{
			Fix:      fp.ArrivalAirport,
			Location: ap.Location,
		}
		nav.Waypoints = append(nav.Waypoints, nav.FlightState.ArrivalAirport)
	}

	return nav
}

func (nav *Nav) TAS() float32 {
	tas := IASToTAS(nav.FlightState.IAS, nav.FlightState.Altitude)
	tas = math.Min(tas, nav.Perf.Speed.CruiseTAS)
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
	if dh := nav.DeferredHeading; dh != nil {
		if dh.Heading.Assigned != nil {
			return *dh.Heading.Assigned, true
		}
	} else if nav.Heading.Assigned != nil {
		return *nav.Heading.Assigned, true
	}
	return 0, false
}

// EnqueueHeading enqueues the given heading assignment to be followed a
// few seconds in the future. It should only be called for heading changes
// due to controller instructions to the pilot and never in cases where the
// autopilot is changing the heading assignment.
func (nav *Nav) EnqueueHeading(h NavHeading) {
	delay := 3 + 3*nav.Rand.Float32()
	now := time.Now()
	nav.DeferredHeading = &DeferredHeading{
		Time:    now.Add(time.Duration(delay * float32(time.Second))),
		Heading: h,
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
	localizer, ok := approach.Line(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	if !ok {
		// e.g. charted visual where we don't have a single approach orientation..
		return false
	}
	distance := math.PointLineDistance(
		math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.LL2NM(localizer[0], nav.FlightState.NmPerLongitude),
		math.LL2NM(localizer[1], nav.FlightState.NmPerLongitude))

	return distance < maxNmDeviation
}

///////////////////////////////////////////////////////////////////////////
// Communication

// Full human-readable summary of nav state for use when paused and mouse
// hover on the scope
func (nav *Nav) Summary(fp FlightPlan, lg *log.Logger) string {
	var lines []string
	lines = append(lines, "Departure from "+fp.DepartureAirport+" to "+fp.ArrivalAirport)

	if nav.Altitude.Assigned != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Assigned) < 100 {
			lines = append(lines, "At assigned altitude "+
				FormatAltitude(*nav.Altitude.Assigned))
		} else {
			line := "At " + FormatAltitude(nav.FlightState.Altitude) + " for " +
				FormatAltitude(*nav.Altitude.Assigned)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
			lines = append(lines, line)
		}
	} else if nav.Altitude.AfterSpeed != nil {
		dir := util.Select(*nav.Altitude.AfterSpeed > nav.FlightState.Altitude, "climb", "descend")
		exped := util.Select(nav.Altitude.ExpediteAfterSpeed, ", expediting", "")
		lines = append(lines, fmt.Sprintf("At %.0f kts, %s to %s"+exped,
			*nav.Altitude.AfterSpeedSpeed, dir, FormatAltitude(*nav.Altitude.AfterSpeed)))
	} else if c := nav.getWaypointAltitudeConstraint(); c != nil && !nav.flyingPT() {
		dir := util.Select(c.Altitude > nav.FlightState.Altitude, "Climbing", "Descending")
		alt := c.Altitude
		if nav.Altitude.Cleared != nil {
			alt = math.Min(alt, *nav.Altitude.Cleared)
		}
		lines = append(lines, dir+" to "+FormatAltitude(alt)+" for alt. restriction at "+c.Fix)
	} else if nav.Altitude.Cleared != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Cleared) < 100 {
			lines = append(lines, "At cleared altitude "+
				FormatAltitude(*nav.Altitude.Cleared))
		} else {
			line := "At " + FormatAltitude(nav.FlightState.Altitude) + " for " +
				FormatAltitude(*nav.Altitude.Cleared)
			if nav.Altitude.Expedite {
				line += ", expediting"
			}
			lines = append(lines, line)
		}
	} else if nav.Altitude.Restriction != nil {
		tgt := nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
		tgt = math.Min(tgt, nav.FinalAltitude)

		if tgt < nav.FlightState.Altitude {
			lines = append(lines, "Descending "+FormatAltitude(nav.FlightState.Altitude)+
				" to "+FormatAltitude(tgt)+" from previous crossing restriction")
		} else {
			lines = append(lines, "Climbing "+FormatAltitude(nav.FlightState.Altitude)+
				" to "+FormatAltitude(tgt)+" from previous crossing restriction")
		}
	}

	// Heading
	if nav.Heading.Assigned != nil {
		if *nav.Heading.Assigned == nav.FlightState.Heading {
			lines = append(lines, fmt.Sprintf("On assigned %03d heading",
				int(*nav.Heading.Assigned)))
		} else {
			lines = append(lines, fmt.Sprintf("Turning from %03d to assigned %03d heading",
				int(nav.FlightState.Heading), int(*nav.Heading.Assigned)))
		}
	}
	if dh := nav.DeferredHeading; dh != nil {
		if dh.Heading.Assigned == nil && len(nav.Waypoints) > 0 {
			lines = append(lines, fmt.Sprintf("Will shortly go direct %s", nav.Waypoints[0].Fix))
		} else if dh.Heading.Assigned != nil {
			lines = append(lines, fmt.Sprintf("Will shortly start flying heading %03d", int(*dh.Heading.Assigned)))
		}
	}

	// Speed; don't be as exhaustive as we are for altitude
	targetAltitude, _ := nav.TargetAltitude(lg)
	ias, _ := nav.TargetSpeed(targetAltitude, lg)
	if nav.Speed.MaintainSlowestPractical {
		lines = append(lines, fmt.Sprintf("Maintain slowest practical speed: %.0f kts", ias))
	} else if nav.Speed.MaintainMaximumForward {
		lines = append(lines, fmt.Sprintf("Maintain maximum forward speed: %.0f kts", ias))
	} else if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	} else if nav.Speed.Assigned != nil {
		lines = append(lines, fmt.Sprintf("Maintaining %.0f kts assignment", *nav.Speed.Assigned))
	} else if nav.Speed.AfterAltitude != nil && nav.Speed.AfterAltitudeAltitude != nil {
		lines = append(lines, fmt.Sprintf("At %s, maintain %0.f kts", FormatAltitude(*nav.Speed.AfterAltitudeAltitude),
			*nav.Speed.AfterAltitude))
	}

	for _, fix := range util.SortedMapKeys(nav.FixAssignments) {
		nfa := nav.FixAssignments[fix]
		if nfa.Arrive.Altitude != nil || nfa.Arrive.Speed != nil {
			line := "Cross " + fix + " "
			if nfa.Arrive.Altitude != nil {
				line += nfa.Arrive.Altitude.PilotTransmission().Written(nav.Rand) + " "
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

	lines = append(lines, "Route: "+WaypointArray(nav.Waypoints).Encode())

	return strings.Join(lines, "\n")
}

func (nav *Nav) DepartureMessage() PilotTransmission {
	target := util.Select(nav.Altitude.Assigned != nil, nav.Altitude.Assigned, nav.Altitude.Cleared)
	if target != nil && *target-nav.FlightState.Altitude > 100 {
		// one of the two should be set, but just in case...
		return MakePilotTransmission("at {alt} climbing {alt}", nav.FlightState.Altitude, *target)
	} else {
		return MakePilotTransmission("at {alt}", nav.FlightState.Altitude)
	}
}

func (nav *Nav) ContactMessage(reportingPoints []ReportingPoint, star string) PilotTransmission {
	// We'll just handle a few cases here; this isn't supposed to be exhaustive..
	var resp PilotTransmission

	var rp *ReportingPoint
	rpDistance := float32(1000)
	for _, wp := range nav.Waypoints {
		if len(wp.Fix) <= 5 {
			rp = &ReportingPoint{Fix: wp.Fix, Location: wp.Location}
			rpDistance = math.NMDistance2LL(nav.FlightState.Position, wp.Location)
			break
		}
	}
	if rp == nil {
		for i, pt := range reportingPoints {
			if d := math.NMDistance2LL(nav.FlightState.Position, pt.Location); d < rpDistance {
				rp = &reportingPoints[i]
				rpDistance = d
			}
		}
	}
	if rp != nil {
		direction := math.Compass(math.Heading2LL(rp.Location, nav.FlightState.Position,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation))

		if dist := int(rpDistance + 0.5); dist <= 1 {
			resp.Add("overhead {fix}", rp.Fix)
		} else {
			resp.Add("{gf} miles "+direction+" of {fix}", dist, rp.Fix)
		}
	}

	if hdg, ok := nav.AssignedHeading(); ok {
		resp.Add("on a {hdg} heading", hdg)
	} else {
		if star != "" {
			if nav.Altitude.Assigned == nil {
				resp.Add("descending on the {star}", star)
			} else {
				resp.Add("on the {star}", star)
			}
		} else if len(nav.Waypoints) > 0 {
			wp := nav.Waypoints[0]
			if len(wp.Fix) > 0 && len(wp.Fix) <= 5 && !strings.ContainsAny(wp.Fix, "-_0123456789") {
				resp.Add("inbound {fix}", wp.Fix)
			}
		}
	}

	if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned != nav.FlightState.Altitude {
		resp.Add("at {alt} for {alt} assigned", nav.FlightState.Altitude, *nav.Altitude.Assigned)
	} else {
		resp.Add("at {alt}", nav.FlightState.Altitude)
	}

	if nav.Speed.Assigned != nil {
		resp.Add("assigned {spd}", *nav.Speed.Assigned)
	}

	return resp
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (nav *Nav) updateAirspeed(alt float32, lg *log.Logger) (float32, bool) {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.
	targetSpeed, targetRate := nav.TargetSpeed(alt, lg)

	// Stay within the aircraft's capabilities
	targetSpeed = math.Clamp(targetSpeed, nav.Perf.Speed.Min, MaxIAS)

	setSpeed := func(next float32) (float32, bool) {
		if nav.Altitude.AfterSpeed != nil &&
			(nav.Altitude.Assigned == nil || *nav.Altitude.Assigned == nav.FlightState.Altitude) {
			cur := nav.FlightState.IAS
			at := *nav.Altitude.AfterSpeedSpeed
			// Check if we've reached or are passing a speed assignment
			// after which an altitude assignment should be followed.
			if (cur > at && next <= at) || (cur < at && next >= at) {
				nav.Altitude.Assigned = nav.Altitude.AfterSpeed
				nav.Altitude.Expedite = nav.Altitude.ExpediteAfterSpeed
				nav.Altitude.AfterSpeed = nil
				nav.Altitude.AfterSpeedSpeed = nil
				nav.Altitude.Restriction = nil
				lg.Debugf("alt: reached target speed %.0f; now going for altitude %.0f", at, *nav.Altitude.Assigned)
			}
		}
		delta := next - nav.FlightState.IAS
		nav.FlightState.IAS = next

		slowingTo250 := targetSpeed == 250 && nav.FlightState.Altitude >= 10000
		return delta, slowingTo250
	}

	if !nav.FlightState.InitialDepartureClimb && alt > nav.FlightState.Altitude &&
		nav.Perf.Engine.AircraftType == "P" {
		// Climbing prop; bleed off speed.
		cruiseIAS := TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
		limit := (nav.v2() + cruiseIAS) * 0.5
		if nav.FlightState.IAS > limit {
			spd := math.Max(nav.FlightState.IAS*.99, limit)
			return setSpeed(spd)
		}
	}

	if nav.Altitude.Expedite {
		// Don't accelerate or decelerate if we're expediting
		//lg.Debug("expediting altitude, so speed unchanged")
		return 0, false
	}

	if nav.FlightState.IAS < targetSpeed {
		accel := nav.Perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = math.Min(accel, targetRate/60)
		if !nav.IsAirborne() {
			// Rough approximation of it being easier to accelerate on the
			// ground and when going slow than when going fast (and
			// airborne).
			if nav.FlightState.IAS < 40 {
				accel *= 3
			} else {
				accel *= 2
			}
		} else if nav.Altitude.Assigned != nil && nav.FlightState.Altitude < *nav.Altitude.Assigned {
			// Reduce acceleration since also climbing
			if nav.FlightState.InitialDepartureClimb {
				// But less so in the initial climb, assuming full power.
				accel *= 0.8
			} else {
				accel *= 0.6
			}
		}
		return setSpeed(math.Min(targetSpeed, nav.FlightState.IAS+accel))
	} else if nav.FlightState.IAS > targetSpeed {
		decel := nav.Perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = math.Min(decel, targetRate/60)
		if nav.Altitude.Assigned != nil && nav.FlightState.Altitude > *nav.Altitude.Assigned {
			// Reduce deceleration since also descending
			decel *= 0.6
		}
		return setSpeed(math.Max(targetSpeed, nav.FlightState.IAS-decel))
	} else {
		return 0, false
	}
}

func (nav *Nav) updateAltitude(targetAltitude, targetRate float32, lg *log.Logger, deltaKts float32, slowingTo250 bool) {
	if targetAltitude == nav.FlightState.Altitude {
		if nav.IsAirborne() && nav.FlightState.InitialDepartureClimb {
			nav.FlightState.InitialDepartureClimb = false
		}
		nav.Altitude.Expedite = false
		return
	}

	// Wrap altitude setting in a lambda so we can detect when we pass
	// through an altitude for "at alt, reduce speed" sort of assignments.
	setAltitude := func(next float32) {
		if nav.Speed.AfterAltitude != nil &&
			(nav.Speed.Assigned == nil || *nav.Speed.Assigned == nav.FlightState.IAS) {
			cur := nav.FlightState.Altitude
			at := *nav.Speed.AfterAltitudeAltitude
			if (cur > at && next <= at) || (cur < at && next >= at) {
				// Reached or passed the altitude, now go for speed
				lg.Debugf("speed: reached altitude %.0f; now going for speed %.0f", at, *nav.Speed.AfterAltitude)
				nav.Speed.Assigned = nav.Speed.AfterAltitude
				nav.Speed.AfterAltitude = nil
				nav.Speed.AfterAltitudeAltitude = nil
			}
		}

		if nav.FlightState.Altitude >= 10000 && next < 10000 {
			// Descending through 10,000'
			if nav.Speed.Assigned != nil && *nav.Speed.Assigned > 250 {
				// Cancel any speed assignments >250kts when we are ready
				// to descend below 10,000'
				nav.Speed.Assigned = nil
				next = 10000
			}
			if nav.Speed.Restriction != nil && *nav.Speed.Restriction > 250 {
				// clear any speed restrictions >250kts we are carrying
				// from a previous waypoint.
				nav.Speed.Restriction = nil
				next = 10000
			}

			if slowingTo250 {
				// Keep it at 10k until we're done slowing
				next = 10000
			}
		}

		nav.FlightState.Altitude = next
	}

	if math.Abs(targetAltitude-nav.FlightState.Altitude) < 3 {
		setAltitude(targetAltitude)
		lg.Debug("reached target altitude")
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := nav.Perf.Rate.Climb, nav.Perf.Rate.Descent

	// Reduce rates from highest possible to be more realistic.
	if !nav.Altitude.Expedite {
		// For high performing aircraft, reduce climb rate after 5,000'
		if climb >= 2500 && nav.FlightState.Altitude > 5000 {
			climb -= 500
		}
		climb = math.Min(climb, targetRate)
		descent = math.Min(descent, targetRate)
	}

	if nav.FlightState.Altitude < targetAltitude {
		if deltaKts > 0 {
			// accelerating in the climb, so reduce climb rate; the scale
			// factor is w.r.t. the maximum acceleration possible.
			max := nav.Perf.Rate.Accelerate / 2
			s := math.Clamp(max-deltaKts, .25, 1)
			climb *= s
		}
		setAltitude(math.Min(targetAltitude, nav.FlightState.Altitude+climb/60))
	} else if nav.FlightState.Altitude > targetAltitude {
		if deltaKts < 0 {
			// Reduce rate due to concurrent deceleration
			max := nav.Perf.Rate.Decelerate / 2
			s := math.Clamp(max - -deltaKts, .25, 1)
			descent *= s
		}
		setAltitude(math.Max(targetAltitude, nav.FlightState.Altitude-descent/60))
	}
}

func (nav *Nav) updateHeading(wind WindModel, lg *log.Logger) {
	targetHeading, turnDirection, turnRate := nav.TargetHeading(wind, lg)

	if nav.FlightState.Heading == targetHeading {
		// BankAngle should be zero(ish) at this point but just to be sure.
		nav.FlightState.BankAngle = 0
		return
	}
	if math.HeadingDifference(nav.FlightState.Heading, targetHeading) < 1 {
		if nav.FlightState.BankAngle > 10 {
			lg.Warnf("reached target but bank angle %f\n", nav.FlightState.BankAngle)
		}
		nav.FlightState.Heading = targetHeading
		nav.FlightState.BankAngle = 0
		return
	}
	//lg.Debugf("turning for heading %.0f", targetHeading)

	var turn float32
	switch turnDirection {
	case TurnLeft:
		angle := math.NormalizeHeading(nav.FlightState.Heading - targetHeading)
		angle = math.Min(angle, turnRate)
		turn = -angle
	case TurnRight:
		angle := math.NormalizeHeading(targetHeading - nav.FlightState.Heading)
		angle = math.Min(angle, turnRate)
		turn = angle
	case TurnClosest:
		turn = math.HeadingSignedTurn(nav.FlightState.Heading, targetHeading)
		turn = math.Clamp(turn, -turnRate, turnRate)
	}

	// Finally, do the turn.
	nav.FlightState.Heading = math.NormalizeHeading(nav.FlightState.Heading + turn)
}

func (nav *Nav) updatePositionAndGS(wind WindModel, lg *log.Logger) {
	// Calculate offset vector based on heading and current TAS.
	hdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	TAS := nav.TAS() / 3600
	flightVector := math.Scale2f([2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}, TAS)

	// Further offset based on the wind
	var windVector [2]float32
	if nav.IsAirborne() && wind != nil {
		windVector = wind.GetWindVector(nav.FlightState.Position, nav.FlightState.Altitude)
	}

	// Update the aircraft's state
	p := math.Add2f(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.Add2f(flightVector, windVector))

	nav.FlightState.Position = math.NM2LL(p, nav.FlightState.NmPerLongitude)
	nav.FlightState.GS = math.Length2f(math.Add2f(flightVector, windVector)) * 3600
}

func (nav *Nav) DepartOnCourse(alt float32, exit string) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Don't do anything if they are not on a heading; let them fly the
		// regular route and don't (potentially) skip waypoints and go
		// straight to the exit; however, the altitude should be changed
		nav.Altitude = NavAltitude{Assigned: &alt}
		nav.Speed = NavSpeed{}
		return
	}

	// Make sure we are going direct to the exit.
	if idx := slices.IndexFunc(nav.Waypoints, func(wp Waypoint) bool { return wp.Fix == exit }); idx != -1 {
		nav.Waypoints = nav.Waypoints[idx:]
	}
	nav.Altitude = NavAltitude{Assigned: &alt}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
}

func (nav *Nav) Check(lg *log.Logger) {
	check := func(waypoints []Waypoint, what string) {
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

// returns passed waypoint if any
func (nav *Nav) Update(wind WindModel, fp *FlightPlan, lg *log.Logger) *Waypoint {
	targetAltitude, altitudeRate := nav.TargetAltitude(lg)
	deltaKts, slowingTo250 := nav.updateAirspeed(targetAltitude, lg)
	nav.updateAltitude(targetAltitude, altitudeRate, lg, deltaKts, slowingTo250)
	nav.updateHeading(wind, lg)
	nav.updatePositionAndGS(wind, lg)
	if nav.Airwork != nil && !nav.Airwork.Update(nav) {
		nav.Airwork = nil // Done.
	}

	//lg.Debug("nav_update", slog.Any("flight_state", nav.FlightState))

	// Don't refer to DeferredHeading here; assume that if the pilot hasn't
	// punched in a new heading assignment, we should update waypoints or
	// not as per the old assignment.
	if nav.Airwork == nil && nav.Heading.Assigned == nil {
		return nav.updateWaypoints(wind, fp, lg)
	}

	return nil
}

func (nav *Nav) TargetHeading(wind WindModel, lg *log.Logger) (heading float32, turn TurnMethod, rate float32) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetHeading()
	}

	// Is it time to start following a heading given by the controller a
	// few seconds ago?
	if dh := nav.DeferredHeading; dh != nil && time.Now().After(dh.Time) {
		lg.Debug("initiating deferred heading assignment", slog.Any("heading", dh.Heading))
		nav.Heading = dh.Heading
		nav.DeferredHeading = nil
	}

	heading, turn = nav.FlightState.Heading, TurnClosest

	// nav.Heading.Assigned may still be nil pending a deferred turn
	if (nav.Approach.InterceptState == InitialHeading ||
		nav.Approach.InterceptState == TurningToJoin) && nav.Heading.Assigned != nil {
		heading, turn = nav.ApproachHeading(wind, lg)
	} else if nav.Heading.RacetrackPT != nil {
		nav.FlightState.BankAngle = 0
		return nav.Heading.RacetrackPT.GetHeading(nav, wind, lg)
	} else if nav.Heading.Standard45PT != nil {
		nav.FlightState.BankAngle = 0
		return nav.Heading.Standard45PT.GetHeading(nav, wind, lg)
	} else if nav.Heading.Assigned != nil {
		heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
	} else if arc := nav.Heading.Arc; arc != nil && nav.Heading.JoiningArc {
		heading = nav.Heading.Arc.InitialHeading
		if math.HeadingDifference(nav.FlightState.Heading, heading) < 1 {
			nav.Heading.JoiningArc = false
		}
	} else {
		// Either on an arc or to a waypoint. Figure out the point we're
		// heading to and then common code will handle wind correction,
		// etc...
		var pTarget math.Point2LL

		if arc := nav.Heading.Arc; arc != nil {
			// Work in nm coordinates
			pc := math.LL2NM(arc.Center, nav.FlightState.NmPerLongitude)
			pac := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
			v := math.Sub2f(pac, pc)
			// Heading from center to aircraft, which we assume to be more
			// or less on the arc already.
			angle := math.Degrees(math.Atan2(v[0], v[1])) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(util.Select(arc.Clockwise, 10, -10))
			p := math.Add2f(pc, math.Scale2f([2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}, arc.Radius))
			pTarget = math.NM2LL(p, nav.FlightState.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				//lg.Debug("heading: route empty, no heading assigned", heading)
				return // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
		}

		// No magnetic correction yet, just the raw geometric heading vector
		hdg := math.Heading2LL(nav.FlightState.Position, pTarget, nav.FlightState.NmPerLongitude, 0)
		v := [2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}
		v = math.Scale2f(v, nav.FlightState.GS)

		if nav.IsAirborne() {
			// model where we'll actually end up, given the wind
			vp := math.Add2f(v, wind.AverageWindVector())

			// Find the deflection angle of how much the wind pushes us off course.
			vn, vpn := math.Normalize2f(v), math.Normalize2f(vp)
			deflection := math.Degrees(math.AngleBetween(vn, vpn))
			// Get a signed angle: take the cross product and then (effectively)
			// dot with (0,0,1) to figure out which way it goes
			if vn[0]*vpn[1]-vn[1]*vpn[0] > 0 {
				deflection = -deflection
			}

			// Turn into the wind; this is a bit of an approximation, since
			// turning changes how much the wind affects the aircraft, but this
			// should be minor since the aircraft's speed should be much
			// greater than the wind speed...
			hdg -= deflection
		}

		// Incorporate magnetic variation in the final heading
		hdg += nav.FlightState.MagneticVariation

		heading = math.NormalizeHeading(hdg)
		if nav.Heading.Arc != nil {
			lg.Debugf("heading: flying %.0f for %.1fnm radius arc", heading, nav.Heading.Arc.Radius)
		} else {
			lg.Debugf("heading: flying %.0f to %s", heading, nav.Waypoints[0].Fix)
		}
	}

	// We have a heading and a direction; now figure out if we need to
	// adjust the bank and then how far we turn this tick.

	// maximum turn rate: 3 degrees/sec at 180kts and below, down to 2 degrees/sec at >=250kts.
	maxTurnRate := 3 - math.Clamp((nav.TAS()-180)/70, 0, 1)

	// signed difference, negative is turn left
	headingDelta := func() float32 {
		switch turn {
		case TurnLeft:
			diff := heading - nav.FlightState.Heading
			if diff > 0 {
				return diff - 360 // force left turn
			}
			return diff // already left
		case TurnRight:
			diff := heading - nav.FlightState.Heading
			if diff < 0 {
				return diff + 360 // force right turn
			}
			return diff // already right
		default:
			diff := heading - nav.FlightState.Heading
			if diff > 180 {
				diff -= 360
			} else if diff < -180 {
				diff += 360
			}
			return diff
		}
	}()

	// In theory, turn rate is proportional to tan(bankAngle) but to make
	// the turn in/turn out math easier, we model it linearly, which is not
	// unreasonable since tan(theta) is linear-ish around 0.
	// Note that this is signed.
	maxBankAngle := nav.Perf.Turn.MaxBankAngle
	maxRollRate := nav.Perf.Turn.MaxBankRate
	turnRate := func(bankAngle float32) float32 { return bankAngle / maxBankAngle * maxTurnRate }

	// If we started leveling out now, how many more degrees would we turn through?
	var levelOutDelta float32
	if nav.FlightState.BankAngle < 0 {
		for a := nav.FlightState.BankAngle; a < 0; a += maxRollRate {
			levelOutDelta += turnRate(a)
		}
	} else {
		for a := nav.FlightState.BankAngle; a > 0; a -= maxRollRate {
			levelOutDelta += turnRate(a)
		}
	}

	//fmt.Printf("hdg %.1f for %.1f max rate %.1f cur bank %.1f levelout delta %.1f, heading delta %.1f\n",
	//nav.FlightState.Heading, heading, maxTurnRate, nav.FlightState.BankAngle, levelOutDelta, headingDelta)

	if headingDelta < 0 {
		// Turning left
		if levelOutDelta < headingDelta {
			//fmt.Printf("  leveling\n")
			nav.FlightState.BankAngle += maxRollRate
		} else if nav.FlightState.BankAngle > -maxBankAngle &&
			levelOutDelta+turnRate(nav.FlightState.BankAngle-maxRollRate) > headingDelta {
			//fmt.Printf("  increasing left bank\n")
			nav.FlightState.BankAngle -= maxRollRate
		}
	} else {
		// Turning right
		if levelOutDelta > headingDelta {
			//fmt.Printf("  leveling\n")
			nav.FlightState.BankAngle -= maxRollRate
		} else if nav.FlightState.BankAngle < maxBankAngle &&
			levelOutDelta+turnRate(nav.FlightState.BankAngle+maxRollRate) < headingDelta {
			//fmt.Printf("  increasing right bank\n")
			nav.FlightState.BankAngle += maxRollRate
		}
	}

	turn = util.Select(nav.FlightState.BankAngle < 0, TurnLeft, TurnRight)

	rate = math.Abs(turnRate(nav.FlightState.BankAngle))

	return
}

func (nav *Nav) ApproachHeading(wind WindModel, lg *log.Logger) (heading float32, turn TurnMethod) {
	// Baseline
	heading, turn = *nav.Heading.Assigned, TurnClosest

	ap := nav.Approach.Assigned

	switch nav.Approach.InterceptState {
	case InitialHeading:
		// On a heading. Is it time to turn?  Allow a lot of slop, but just
		// fly through the localizer if it's too sharp an intercept
		hdg, _ := ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		if d := math.HeadingDifference(hdg, nav.FlightState.Heading); d > 45 {
			lg.Infof("heading: difference %.0f too much to intercept", d)
			return
		}

		loc, _ := ap.Line(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if nav.shouldTurnToIntercept(loc[0], hdg, TurnClosest, wind, lg) {
			lg.Debugf("heading: time to turn for approach heading %.1f", hdg)

			nav.Approach.InterceptState = TurningToJoin
			// The autopilot is doing this, so start the turn immediately;
			// don't use EnqueueHeading. However, leave any deferred
			// heading in place, as it represents a controller command that
			// should be followed.
			nav.Heading = NavHeading{Assigned: &hdg}
			// Just in case.. Thus we will be ready to pick up the
			// approach waypoints once we capture.
			nav.Waypoints = []Waypoint{nav.FlightState.ArrivalAirport}
		}
		return

	case TurningToJoin:
		// we've turned to intercept. have we intercepted?
		if !nav.OnExtendedCenterline(.2) {
			return
		}
		lg.Debug("heading: approach intercepted")

		// we'll call that good enough. Now we need to figure out which
		// fixes in the approach are still ahead and then add them to
		// the aircraft's waypoints.
		lg.Debugf("heading: intercepted the approach!")
		apHeading, _ := ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		wps, idx := ap.FAFSegment(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		for idx > 0 {
			prev := wps[idx-1]
			hdg := math.Heading2LL(prev.Location, wps[idx].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

			if math.HeadingDifference(hdg, apHeading) > 1 { // not on the final approach course
				break
			}

			acToWpHeading := math.Heading2LL(nav.FlightState.Position, wps[idx].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
			acToPrevHeading := math.Heading2LL(nav.FlightState.Position, wps[idx-1].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

			da := math.Mod(acToWpHeading-nav.FlightState.Heading+360, 360)
			db := math.Mod(acToPrevHeading-nav.FlightState.Heading+360, 360)
			if (da < 180 && db > 180) || (da > 180 && db < 180) {
				// prev and current are on different sides of the current
				// heading, so don't take the prev so we don't turn away
				// from where we should be going.
				break
			}
			idx--
		}
		nav.Waypoints = append(util.DuplicateSlice(wps[idx:]), nav.FlightState.ArrivalAirport)
		lg.Debug("heading: fix added future waypoints", slog.Any("waypoints", nav.Waypoints))

		// Ignore the approach altitude constraints if the aircraft is only
		// intercepting but isn't cleared.
		if nav.Approach.Cleared {
			nav.Altitude = NavAltitude{}
		}
		// As with the heading assignment above under the InitialHeading
		// case, do this immediately.
		nav.Heading = NavHeading{}
		nav.Approach.InterceptState = OnApproachCourse

		// If we have intercepted the approach course, we don't do procedure turns.
		nav.Approach.NoPT = true

		return
	}

	return
}

const MaximumRate = 100000

func (nav *Nav) TargetAltitude(lg *log.Logger) (float32, float32) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetAltitude()
	}

	// Stay on the ground if we're still on the takeoff roll.
	rate := float32(MaximumRate)
	if nav.FlightState.InitialDepartureClimb && !nav.IsAirborne() {
		//lg.Debug("alt: continuing takeoff roll")
		rate = 0 // still return the desired altitude, just no oomph to get there.
	}

	// Ugly to be digging into heading here, but anyway...
	if nav.Heading.RacetrackPT != nil {
		if alt, ok := nav.Heading.RacetrackPT.GetAltitude(nav); ok {
			//lg.Debugf("alt: descending to %d for procedure turn", int(alt))
			return alt, rate
		}
	}

	// Controller-assigned altitude overrides everything else
	if nav.Altitude.Assigned != nil {
		return *nav.Altitude.Assigned, rate
	}

	if c := nav.getWaypointAltitudeConstraint(); c != nil && !nav.flyingPT() {
		//lg.Debugf("alt: altitude %.0f for waypoint %s in %.0f seconds", c.Altitude, c.Fix, c.ETA)
		if c.ETA < 5 || nav.FlightState.Altitude < c.Altitude {
			// Always climb as soon as we can
			return c.Altitude, rate
		} else {
			// Descending
			rate = (nav.FlightState.Altitude - c.Altitude) / c.ETA
			rate *= 60 // feet per minute

			descent := nav.Perf.Rate.Descent
			if nav.FlightState.Altitude < 10000 && !nav.Altitude.Expedite {
				// And reduce it based on airspeed as well
				descent *= math.Min(nav.FlightState.IAS/250, 1)
				if descent > 2000 {
					// Reduce descent rate on approach
					descent = 2000
				}
			}

			if nav.Approach.PassedFAF {
				// After the FAF, try to go down linearly
				return c.Altitude, rate
			} else if rate > descent/2 {
				// Don't start the descent until (more or less) it's
				// necessary. (But then go a little faster than we think we
				// need to, to be safe.)
				return c.Altitude, rate * 1.25
			} else {
				// Stay where we are for now.
				return nav.FlightState.Altitude, 0
			}
		}
	}

	if nav.Altitude.Cleared != nil {
		return math.Min(*nav.Altitude.Cleared, nav.FinalAltitude), rate
	}

	if ar := nav.Altitude.Restriction; ar != nil {
		return ar.TargetAltitude(nav.FlightState.Altitude), rate
	}

	// Baseline: stay where we are
	return nav.FlightState.Altitude, 0
}

func (nav *Nav) flyingPT() bool {
	return (nav.Heading.RacetrackPT != nil && nav.Heading.RacetrackPT.State != PTStateApproaching) ||
		(nav.Heading.Standard45PT != nil && nav.Heading.Standard45PT.State != PT45StateApproaching)
}

type WaypointCrossingConstraint struct {
	Altitude float32
	Fix      string  // where we're trying to readh Altitude
	ETA      float32 // seconds
}

// getWaypointAltitudeConstraint looks at the waypoint altitude
// restrictions in the aircraft's upcoming route and determines the
// altitude at which it will cross the next waypoint with a crossing
// restriction. It balances the general principle of preferring to be at
// higher altitudes (speed, efficiency) with the aircraft's performance and
// subsequent altitude restrictions--e.g., sometimes it needs to be lower
// than it would otherwise at one waypoint in order to make a restriction
// at a subsequent waypoint.
func (nav *Nav) getWaypointAltitudeConstraint() *WaypointCrossingConstraint {
	if nav.Heading.Assigned != nil {
		// ignore what's going on with the fixes
		return nil
	}

	if nav.InterceptedButNotCleared() {
		// Assuming this must be an altitude constraint on the approach,
		// we'll ignore it until the aircraft has been cleared for the
		// approach.
		return nil
	}

	getRestriction := func(i int) *AltitudeRestriction {
		wp := nav.Waypoints[i]
		// Return any controller-assigned constraint in preference to a
		// charted one.
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
			return nfa.Arrive.Altitude
		} else if ar := nav.Waypoints[i].AltitudeRestriction; ar != nil {
			// If the controller has given 'cross [wp] at [alt]' for a
			// future waypoint, however, ignore the charted altitude
			// restriction.
			if len(nav.FixAssignments) > 0 {
				// This is surprisingly expensive e.g. during VFR prespawn
				// airspace violation checks and so we'll skip it entirely
				// when possible.
				if slices.ContainsFunc(nav.Waypoints[i+1:], func(wp Waypoint) bool {
					fa, ok := nav.FixAssignments[wp.Fix]
					return ok && fa.Arrive.Altitude != nil
				}) {
					return nil
				}
			}
			return ar
		}
		return nil
	}

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft.
	lastWp := -1
	for i := len(nav.Waypoints) - 1; i >= 0; i-- {
		// Skip restrictions that don't apply (e.g. "at or above" if we're
		// already above.) I think(?) we would actually bail out and return
		// nil if we find one that doesn't apply, under the principle that
		// we should also already be meeting any restrictions that are
		// before it, but this seems less risky.
		if r := getRestriction(i); r != nil &&
			r.TargetAltitude(nav.FlightState.Altitude) != nav.FlightState.Altitude {
			lastWp = i
			break
		}
	}
	if lastWp == -1 {
		// No applicable altitude restrictions found, so nothing to do here.
		return nil
	}

	// Figure out what climb/descent rate we will use for modeling the
	// flight path.
	var altRate float32
	descending := nav.FlightState.Altitude > getRestriction(lastWp).TargetAltitude(nav.FlightState.Altitude)
	if descending {
		altRate = nav.Perf.Rate.Descent
		// This unfortunately mirrors logic in the updateAltitude() method.
		// It would be nice to unify the nav modeling and the aircraft's
		// flight modeling to eliminate this...
		if nav.FlightState.Altitude < 10000 {
			altRate = math.Min(altRate, 2000)
			altRate *= math.Min(nav.FlightState.IAS/250, 1)
		}
		// Reduce the expected rate by a fudge factor to try to account for
		// slowing down at lower altitudes, speed reductions on approach,
		// and the fact that aircraft cut corners at turns rather than
		// going the longer way and overflying fixes.
		altRate *= 0.7
	} else {
		// This also mirrors logic in updateAltitude() and has its own
		// fudge factor, though a smaller one. Note that it doesn't include
		// a model for pausing the climb at 10k feet to accelerate, though
		// at that point we're likely leaving the TRACON airspace anyway...
		altRate = 0.9 * util.Select(nav.Perf.Rate.Climb > 2500, nav.Perf.Rate.Climb-500, nav.Perf.Rate.Climb)
	}

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := getRestriction(lastWp).Range

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	fix := nav.Waypoints[lastWp].Fix // first one with an alt restriction
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += math.NMDistance2LLFast(nav.Waypoints[i+1].Location, nav.Waypoints[i].Location,
			nav.FlightState.NmPerLongitude)

		// Does this one have a relevant altitude restriction?
		restr := getRestriction(i)
		if restr == nil {
			continue
		}

		// Ignore it if the aircraft is cleared for the approach and is below it.
		// TODO: I think this can be 'break' rather than continue...
		if nav.Approach.Cleared && nav.FlightState.Altitude < restr.Range[0] {
			continue
		}

		fix = nav.Waypoints[i].Fix

		// TODO: account for decreasing GS with altitude?
		// TODO: incorporate a simple wind model in GS?
		eta := sumDist / nav.FlightState.GS * 3600 // seconds

		// Maximum change in altitude possible before reaching this
		// waypoint.
		dalt := altRate * eta / 60

		// possibleRange is altitude range the aircraft could have at this
		// waypoint, given its performance characteristics and assuming it
		// will meet the constraint at the subsequent waypoint with an
		// altitude restriction.
		//
		// Note that dalt only applies to one limit, since the aircraft can
		// always maintain its current altitude between waypoints; which
		// limit depends on whether it is climbing or descending (but then
		// which one and the addition/subtraction are confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := altRange
		if !descending {
			possibleRange[0] -= dalt
		} else {
			possibleRange[1] += dalt
		}

		// Limit the possible range according to the restriction at the
		// current waypoint.
		var ok bool
		altRange, ok = restr.ClampRange(possibleRange)
		if !ok {
			//lg.Infof("unable to fulfill altitude restriction at %s: possible %v required %v",
			// nav.Waypoints[i].Fix, possibleRange, restr.Range)
			// Keep using altRange, FWIW; it will be clamped to whichever of the
			// low and high of the restriction's range it is closest to.
		}

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Add the distance to the first waypoint to get the total distance
	// (and then the ETA) between the aircraft and the first waypoint with
	// an altitude restriction.
	d := sumDist + math.NMDistance2LLFast(nav.FlightState.Position, nav.Waypoints[0].Location,
		nav.FlightState.NmPerLongitude)
	eta := d / nav.FlightState.GS * 3600 // seconds

	// Prefer to be higher rather than low; deal with "at or above" here as well.
	alt := util.Select(altRange[1] != 0, altRange[1], nav.FinalAltitude)

	// But leave arrivals at their current altitude if it's acceptable;
	// don't climb just because we can.
	if descending {
		ar := AltitudeRestriction{Range: altRange}
		if ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
			alt = nav.FlightState.Altitude
		}
	}

	return &WaypointCrossingConstraint{
		Altitude: alt,
		ETA:      eta,
		Fix:      fix,
	}
}

func (nav *Nav) TargetSpeed(targetAltitude float32, lg *log.Logger) (float32, float32) {
	if nav.Airwork != nil {
		if spd, rate, ok := nav.Airwork.TargetSpeed(); ok {
			return spd, rate
		}
	}

	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	fd, err := nav.distanceToEndOfApproach()
	if err == nil && fd < 5 {
		// Cancel speed restrictions inside 5 mile final
		lg.Debug("speed: cancel speed restrictions at 5 mile final")
		nav.Speed = NavSpeed{}
	}

	// Controller assignments: these override anything else.
	if nav.Speed.MaintainSlowestPractical {
		//lg.Debug("speed: slowest practical")
		return nav.Perf.Speed.Landing + 5, MaximumRate
	}
	if nav.Speed.MaintainMaximumForward {
		//lg.Debug("speed: maximum forward")
		if nav.Approach.Cleared {
			// (We expect this to usually be the case.) Ad-hoc speed based
			// on V2, also assuming some flaps are out, so we don't just
			// want to return 250 knots here...
			cruiseIAS := TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
			return math.Min(nav.v2()*1.6, math.Min(250, cruiseIAS)), MaximumRate
		}
		return nav.targetAltitudeIAS()
	}
	if nav.Speed.Assigned != nil {
		//lg.Debugf("speed: %.0f assigned", *nav.Speed.Assigned)
		return *nav.Speed.Assigned, MaximumRate
	}

	// Manage the speed profile in the initial climb
	if nav.FlightState.InitialDepartureClimb {
		agl := nav.FlightState.Altitude - nav.FlightState.DepartureAirportElevation
		isJet := nav.Perf.Engine.AircraftType == "J"

		if (isJet && agl >= 5000) || (!isJet && agl >= 1500) {
			nav.FlightState.InitialDepartureClimb = false
		}

		var targetSpeed float32
		if nav.Perf.Engine.AircraftType == "J" { // jet
			if agl < 1500 {
				targetSpeed = 180
			} else {
				targetSpeed = 210
			}
		} else { // prop/turboprop
			if agl < 500 {
				targetSpeed = 1.1 * nav.v2()
			} else if agl < 1000 {
				targetSpeed = 1.2 * nav.v2()
			} else {
				targetSpeed = 1.3 * nav.v2()
			}
		}

		// Make sure we're not trying to go faster than we're able to
		cruiseIAS := TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
		targetSpeed = math.Min(targetSpeed, cruiseIAS)

		// And don't accelerate past any upcoming speed restrictions
		if nav.Speed.Restriction != nil {
			targetSpeed = math.Min(targetSpeed, *nav.Speed.Restriction)
		}
		if _, speed, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
			targetSpeed = math.Min(targetSpeed, speed)
		}

		// However, don't let anything prevent us from taking off!
		targetSpeed = math.Max(targetSpeed, nav.v2())

		return targetSpeed, 0.8 * maxAccel
	}

	if wp, speed, eta, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		//lg.Debugf("speed: %.0f to cross %s in %.0fs", speed, wp.Fix, eta)
		if eta < 5 { // includes unknown ETA case
			return speed, MaximumRate
		}

		if speed > nav.FlightState.IAS {
			// accelerate immediately
			return speed, MaximumRate
		} else if wp.OnSID {
			// don't accelerate past speed constraints on SIDs
			return speed, MaximumRate
		} else {
			// go slow on deceleration
			rate := (nav.FlightState.IAS - speed) / eta
			decel := nav.Perf.Rate.Decelerate / 2 // it's specified in per 2 seconds...
			if rate > decel/2 {
				// Start to decelerate.
				return speed, MaximumRate
			}
			// Otherwise fall through in case anything else applies.
		}
	}

	// Something from a previous waypoint; ignore it if we're cleared for the approach.
	if nav.Speed.Restriction != nil && !nav.Approach.Cleared {
		//lg.Debugf("speed: previous restriction %.0f", *nav.Speed.Restriction)
		return *nav.Speed.Restriction, MaximumRate
	}

	// Absent controller speed restrictions, slow down arrivals starting 15 miles out.
	if nav.Speed.Assigned == nil && fd != 0 && fd < 15 {
		spd := nav.Perf.Speed
		// Expected speed at 10 DME, without further direction.
		approachSpeed := 1.25 * spd.Landing

		x := math.Clamp((fd-1)/9, float32(0), float32(1))
		ias := math.Lerp(x, spd.Landing, approachSpeed)
		// Don't speed up after being been cleared to land.
		ias = math.Min(ias, nav.FlightState.IAS)

		//lg.Debugf("speed: approach cleared, %.1f nm out, ias %.0f", fd, ias)
		return ias, MaximumRate
	}

	if nav.Approach.Cleared {
		// Don't speed up if we're cleared and farther away
		//lg.Debugf("speed: cleared approach but far away")
		return nav.FlightState.IAS, MaximumRate
	}

	if nav.FlightState.Altitude >= 10000 && targetAltitude < 10000 && nav.FlightState.IAS > 250 {
		// Consider slowing to 250; estimate how long until we'll reach 10k
		dalt := nav.FlightState.Altitude - 10000
		salt := dalt / (nav.Perf.Rate.Descent / 60) // seconds until we reach 10k

		dspeed := nav.FlightState.IAS - 250
		sspeed := dspeed / (nav.Perf.Rate.Decelerate / 2) // seconds to decelerate to 250

		if salt <= sspeed {
			// Time to slow down
			return 250, MaximumRate
		} else {
			// Otherwise reduce in general but in any case don't speed up
			// again.
			ias, rate := nav.targetAltitudeIAS()
			return math.Min(ias, nav.FlightState.IAS), rate
		}
	}

	// Nothing assigned by the controller or the route, so set a target
	// based on the aircraft's altitude.
	ias, rate := nav.targetAltitudeIAS()
	//lg.Debugf("speed: %.0f based on altitude", ias)
	return ias, rate
}

// Compute target airspeed for higher altitudes speed by lerping from 250
// to cruise speed based on altitude.
func (nav *Nav) targetAltitudeIAS() (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute
	cruiseIAS := TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)

	if nav.FlightState.Altitude <= 10000 {
		// 250kts under 10k.  We can assume a high acceleration rate for
		// departures when this kicks in at 1500' AGL given that VNav will
		// slow the rate of climb at that point until we reach the target
		// speed.
		return math.Min(cruiseIAS, 250), 0.9 * maxAccel
	}

	x := math.Clamp((nav.FlightState.Altitude-10000)/(nav.Perf.Ceiling-10000), 0, 1)
	return math.Lerp(x, math.Min(cruiseIAS, 280), cruiseIAS), 0.8 * maxAccel
}

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (Waypoint, float32, float32, bool) {
	var eta float32
	for i, wp := range nav.Waypoints {
		if i == 0 {
			eta = float32(wp.ETA(nav.FlightState.Position, nav.FlightState.GS,
				nav.FlightState.NmPerLongitude).Seconds())
		} else {
			d := math.NMDistance2LLFast(wp.Location, nav.Waypoints[i-1].Location,
				nav.FlightState.NmPerLongitude)
			etaHours := d / nav.FlightState.GS
			eta += etaHours * 3600
		}

		spd := float32(wp.Speed)
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
			spd = *nfa.Arrive.Speed
		}

		if spd != 0 {
			return wp, spd, eta, true
		}
	}
	return Waypoint{}, 0, 0, false
}

// distanceToEndOfApproach returns the remaining distance to the last
// waypoint (usually runway threshold) of the currently assigned approach.
func (nav *Nav) distanceToEndOfApproach() (float32, error) {
	if nav.Approach.Assigned == nil || !nav.Approach.Cleared {
		return 0, ErrNotClearedForApproach
	}

	if nav.Heading.Assigned != nil {
		// We're not currently on the route, so it's a little unclear. Rather than
		// guessing, we'll just error out and let callers decide how to handle this.
		return 0, ErrNotFlyingRoute
	}

	// Calculate flying distance to the airport
	if wp := nav.Waypoints; len(wp) == 0 {
		// This shouldn't ever happen; we should always have the
		// destination airport, but just in case...
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, nav.FlightState.ArrivalAirportLocation)
		return remainingDistance, nil
	} else {
		// Distance to the next fix plus sum of the distances between
		// remaining fixes.
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
		// Don't include the final waypoint, which should be the
		// destination airport.
		for i := 0; i < len(wp)-2; i++ {
			remainingDistance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
		}

		return remainingDistance, nil
	}
}

func (nav *Nav) updateWaypoints(wind WindModel, fp *FlightPlan, lg *log.Logger) *Waypoint {
	if len(nav.Waypoints) == 0 {
		return nil
	}

	wp := nav.Waypoints[0]

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if len(nav.Approach.AtFixClearedRoute) > 1 &&
		nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix {
		hdg = math.Heading2LL(wp.Location, nav.Approach.AtFixClearedRoute[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
		// depart fix direct
		hdg = math.Heading2LL(wp.Location, nfa.Depart.Fix.Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if wp.PresentHeading {
		hdg = nav.FlightState.Heading
	} else if wp.Arc != nil {
		// Joining a DME arc after the heading
		hdg = wp.Arc.InitialHeading
	} else if len(nav.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = math.Heading2LL(wp.Location, nav.Waypoints[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = nav.FlightState.Heading
	}

	passedWaypoint := false
	if wp.FlyOver {
		dist := math.NMDistance2LL(nav.FlightState.Position, wp.Location)
		eta := dist / nav.FlightState.GS * 3600 // in seconds
		passedWaypoint = eta < 2
	} else {
		passedWaypoint = nav.shouldTurnForOutbound(wp.Location, hdg, TurnClosest, wind, lg)
	}

	if passedWaypoint {
		//lg.Debugf("turning outbound from %.1f to %.1f for %s", nav.FlightState.Heading,	hdg, wp.Fix)

		clearedAtFix := nav.Approach.AtFixClearedRoute != nil && nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix
		if clearedAtFix {
			nav.Approach.Cleared = true
			nav.Speed = NavSpeed{}
			if wp.NoPT || nav.Approach.AtFixClearedRoute[0].NoPT {
				nav.Approach.NoPT = true
			}
			nav.Waypoints = append(nav.Approach.AtFixClearedRoute, nav.FlightState.ArrivalAirport)
		}
		if nav.Heading.Arc != nil {
			nav.Heading = NavHeading{}
		}

		if wp.ClearApproach {
			if fp == nil {
				lg.Warnf("nil *FlightPlan at waypoint /clearapp")
			} else {
				_, err := nav.clearedApproach(fp.ArrivalAirport, nav.Approach.AssignedId, false, lg)
				if err != nil {
					lg.Errorf("/clearapp: %s", err)
				}
			}
		}

		if nav.Approach.Cleared {
			// The aircraft has made it to the approach fix they
			// were cleared to, so they can start to descend.
			nav.Altitude = NavAltitude{}
			nav.Approach.PassedApproachFix = true
			if wp.FAF {
				nav.Approach.PassedFAF = true
			}
		} else if wp.OnApproach {
			// Overflew an approach fix but haven't been cleared yet.
			nav.Approach.PassedApproachFix = true
		}

		if wp.AltitudeRestriction != nil && !nav.InterceptedButNotCleared() &&
			(!nav.Approach.Cleared || wp.AltitudeRestriction.Range[0] < nav.FlightState.Altitude) {
			// Don't climb if we're cleared approach and below the next
			// fix's altitude.
			nav.Altitude.Restriction = wp.AltitudeRestriction
		}
		if wp.Speed != 0 && !wp.OnSID {
			// Carry on the speed restriction unless it's a SID
			spd := float32(wp.Speed)
			nav.Speed.Restriction = &spd
		}

		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
			// Controller-assigned heading
			hdg := *nfa.Depart.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
			if nav.directFix(nfa.Depart.Fix.Fix) != nil {
				lg.Errorf("unable direct %s after %s???", nfa.Depart.Fix.Fix, wp.Fix)
			} else {
				// Hacky: directFix updates the route but below we peel off
				// the current waypoint, so re-add it here so everything
				// works out.
				nav.Waypoints = append([]Waypoint{wp}, nav.Waypoints...)
			}
		} else if wp.Heading != 0 && !clearedAtFix {
			// We have an outbound heading
			hdg := float32(wp.Heading)
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if wp.PresentHeading && !clearedAtFix {
			// Round to nearest 5 degrees
			hdg := float32(5 * int((nav.FlightState.Heading+2.5)/5))
			hdg = math.NormalizeHeading(hdg)
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if wp.Arc != nil {
			// Fly the DME arc
			nav.Heading = NavHeading{Arc: wp.Arc, JoiningArc: true}
		}

		if wp.NoPT {
			nav.Approach.NoPT = true
		}

		if wp.AirworkMinutes > 0 {
			nav.Airwork = StartAirwork(wp, *nav)
		}

		// Remove the waypoint from the route unless it's the destination
		// airport, which we leave in any case.
		if len(nav.Waypoints) == 1 {
			// Passing the airport; leave it in the route but make sure
			// we're on a heading.
			hdg := nav.FlightState.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else {
			nav.Waypoints = nav.Waypoints[1:]
		}

		if nav.Heading.Assigned == nil {
			nav.flyProcedureTurnIfNecessary()
		}

		nav.Check(lg)

		return &wp
	}
	return nil
}

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial.
func (nav *Nav) shouldTurnForOutbound(p math.Point2LL, hdg float32, turn TurnMethod, wind WindModel, lg *log.Logger) bool {
	dist := math.NMDistance2LL(nav.FlightState.Position, p)
	eta := dist / nav.FlightState.GS * 3600 // in seconds

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Alternatively, if we're far away w.r.t. the needed turn, don't even
	// consider it. This is both for performance but also so that we don't
	// make tiny turns miles away from fixes in some cases.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	hm := hdg - nav.FlightState.MagneticVariation
	p1 := math.Add2f(p0, [2]float32{math.Sin(math.Radians(hm)), math.Cos(math.Radians(hm))})

	// Make a ghost aircraft to use to simulate the turn.
	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.DeferredHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position,
		nav2.FlightState.NmPerLongitude),
		p0, p1)

	// Don't simulate the turn longer than it will take to do it.
	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind, nil, nil)
		curDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position,
			nav2.FlightState.NmPerLongitude),
			p0, p1)

		if math.Sign(initialDist) != math.Sign(curDist) {
			// Aircraft is on the other side of the line than it started on.
			lg.Debugf("turning now to intercept outbound in %d seconds", i)
			return true
		}
	}
	return false
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept the radial.
func (nav *Nav) shouldTurnToIntercept(p0 math.Point2LL, hdg float32, turn TurnMethod, wind WindModel, lg *log.Logger) bool {
	p0 = math.LL2NM(p0, nav.FlightState.NmPerLongitude)
	p1 := math.Add2f(p0, [2]float32{math.Sin(math.Radians(hdg - nav.FlightState.MagneticVariation)),
		math.Cos(math.Radians(hdg - nav.FlightState.MagneticVariation))})

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude), p0, p1)
	eta := math.Abs(initialDist) / nav.FlightState.GS * 3600 // in seconds
	if eta < 2 {
		// Just in case, start the turn
		return true
	}

	// As above, don't consider starting the turn if we're far away.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.DeferredHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	n := int(1 + turnAngle/3)
	for i := 0; i < n; i++ {
		nav2.Update(wind, nil, nil)
		curDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position, nav2.FlightState.NmPerLongitude), p0, p1)
		if math.Sign(initialDist) != math.Sign(curDist) && math.Abs(curDist) < .25 && math.HeadingDifference(hdg, nav2.FlightState.Heading) < 3.5 {
			lg.Debugf("turning now to intercept radial in %d seconds", i)
			return true
		}
	}
	return false
}

///////////////////////////////////////////////////////////////////////////

type TurnMethod int

const (
	TurnClosest TurnMethod = iota // default
	TurnLeft
	TurnRight
)

func (t TurnMethod) String() string {
	return []string{"closest", "left", "right"}[t]
}

const StandardTurnRate = 3

func TurnAngle(from, to float32, turn TurnMethod) float32 {
	switch turn {
	case TurnLeft:
		return math.NormalizeHeading(from - to)

	case TurnRight:
		return math.NormalizeHeading(to - from)

	case TurnClosest:
		return math.Abs(math.HeadingDifference(from, to))

	default:
		panic("unhandled TurnMethod")
	}
}

func (nav *Nav) GoAround() PilotTransmission {
	hdg := nav.FlightState.Heading
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.DeferredHeading = nil

	nav.Speed = NavSpeed{}

	alt := float32(1000 * int((nav.FlightState.ArrivalAirportElevation+2500)/1000))
	nav.Altitude = NavAltitude{Assigned: &alt}

	nav.Approach = NavApproach{}
	// Keep the destination airport at the end of the route.
	nav.Waypoints = []Waypoint{nav.FlightState.ArrivalAirport}

	return MakePilotTransmission("[going around|on the go]")
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool) PilotTransmission {
	if alt > nav.Perf.Ceiling {
		return MakeUnexpectedPilotTransmission("unable. That altitude is above our ceiling.")
	}

	var response PilotTransmission
	if alt > nav.FlightState.Altitude {
		response = MakePilotTransmission("[climb-and-maintain|up to|] {alt}", alt)
	} else if alt == nav.FlightState.Altitude {
		response = MakePilotTransmission("[maintain|we'll keep it at|] {alt}", alt)
	} else {
		response = MakePilotTransmission("[descend-and-maintain|down to|] {alt}", alt)
	}

	if afterSpeed && nav.Speed.Assigned != nil && *nav.Speed.Assigned != nav.FlightState.IAS {
		nav.Altitude.AfterSpeed = &alt
		spd := *nav.Speed.Assigned
		nav.Altitude.AfterSpeedSpeed = &spd

		rspeed := MakePilotTransmission("at {spd}", *nav.Speed.Assigned)
		rspeed.Merge(response)
		response = rspeed
	} else {
		nav.Altitude = NavAltitude{Assigned: &alt}
	}
	return response
}

func (nav *Nav) AssignSpeed(speed float32, afterAltitude bool) PilotTransmission {
	maxIAS := TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if speed == 0 {
		nav.Speed = NavSpeed{}
		return MakePilotTransmission("cancel speed restrictions")
	} else if float32(speed) < nav.Perf.Speed.Landing {
		return MakePilotTransmission("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if float32(speed) > maxIAS {
		return MakePilotTransmission("unable. Our maximum speed is {spd}", maxIAS)
	} else if nav.Approach.Cleared {
		// TODO: make sure we're not within 5 miles...
		nav.Speed = NavSpeed{Assigned: &speed}
		return MakePilotTransmission("{spd} until 5 mile final", speed)
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		nav.Speed.AfterAltitude = &speed
		alt := *nav.Altitude.Assigned
		nav.Speed.AfterAltitudeAltitude = &alt

		return MakePilotTransmission("[at {alt} maintain {spd}|at {alt} {spd}|{alt} then {spd}]", alt, speed)
	} else {
		nav.Speed = NavSpeed{Assigned: &speed}
		if speed < nav.FlightState.IAS {
			return MakePilotTransmission("[reduce to {spd}|speed {spd}|slow to {spd}|{spd}]",
				speed)
		} else if speed > nav.FlightState.IAS {
			return MakePilotTransmission("[increase to {spd}|speed {spd}|maintain {spd}|{spd}]", speed)
		} else {
			return MakePilotTransmission("[maintain {spd}|keep it at {spd}|we'll stay at {spd}|{spd}]", speed)
		}
	}
}

func (nav *Nav) MaintainSlowestPractical() PilotTransmission {
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}
	return MakePilotTransmission("[slowest practical speed|slowing as much as we can]")
}

func (nav *Nav) MaintainMaximumForward() PilotTransmission {
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	return MakePilotTransmission("[maximum forward speed|maintaining maximum forward speed]")
}

func (nav *Nav) SaySpeed() PilotTransmission {
	currentSpeed := nav.FlightState.IAS

	if nav.Speed.Assigned != nil {
		assignedSpeed := *nav.Speed.Assigned
		if assignedSpeed < currentSpeed {
			return MakePilotTransmission("[at {spd} slowing to {spd}|at {spd} down to {spd}]", currentSpeed, assignedSpeed)
		} else if assignedSpeed > currentSpeed {
			return MakePilotTransmission("at {spd} speeding up to {spd}", currentSpeed, assignedSpeed)
		} else {
			return MakePilotTransmission("[maintaining {spd}|at {spd}]", currentSpeed)
		}
	} else {
		return MakePilotTransmission("[maintaining {spd}|at {spd}]", currentSpeed)
	}
}

func (nav *Nav) SayHeading() PilotTransmission {
	currentHeading := nav.FlightState.Heading

	if nav.Heading.Assigned != nil {
		assignedHeading := *nav.Heading.Assigned
		if assignedHeading != currentHeading {
			return MakePilotTransmission("[heading {hdg}|{hdg}]", currentHeading, assignedHeading)
		} else {
			return MakePilotTransmission("heading {hdg}", currentHeading)
		}
	} else {
		return MakePilotTransmission("heading {hdg}", currentHeading)
	}
}

func (nav *Nav) SayAltitude() PilotTransmission {
	currentAltitude := nav.FlightState.Altitude

	if nav.Altitude.Assigned != nil {
		assignedAltitude := *nav.Altitude.Assigned
		if assignedAltitude < currentAltitude {
			return MakePilotTransmission("[at {alt} descending to {alt}|at {alt} and descending]",
				currentAltitude, assignedAltitude)
		} else if assignedAltitude > currentAltitude {
			return MakePilotTransmission("at {alt} climbing to {alt}", currentAltitude, assignedAltitude)
		} else {
			return MakePilotTransmission("[maintaining {alt}|at {alt}]", currentAltitude)
		}
	} else {
		return MakePilotTransmission("maintaining {alt}", currentAltitude)
	}
}

func (nav *Nav) ExpediteDescent() PilotTransmission {
	alt, _ := nav.TargetAltitude(nil)
	if alt >= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return MakePilotTransmission("[expediting down to|expedite to] {alt} once we're at {spd}",
				*nav.Altitude.AfterSpeed, *nav.Altitude.AfterSpeedSpeed)
		} else {
			return MakeUnexpectedPilotTransmission("unable. We're not descending")
		}
	} else if nav.Altitude.Expedite {
		return MakePilotTransmission("[we're already expediting|that's our best rate]")
	} else {
		nav.Altitude.Expedite = true
		return MakePilotTransmission("[expediting down to|expedite] {alt}", alt)
	}
}

func (nav *Nav) ExpediteClimb() PilotTransmission {
	alt, _ := nav.TargetAltitude(nil)
	if alt <= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return MakePilotTransmission("[expediting up to|expedite to] {alt} once we're at {spd}",
				*nav.Altitude.AfterSpeed, *nav.Altitude.AfterSpeedSpeed)
		} else {
			return MakeUnexpectedPilotTransmission("unable. We're not climbing")
		}
	} else if nav.Altitude.Expedite {
		return MakePilotTransmission("[we're already expediting|that's our best rate]")
	} else {
		nav.Altitude.Expedite = true
		return MakePilotTransmission("[expediting up to|expedite] {alt}", alt)
	}
}

func (nav *Nav) AssignHeading(hdg float32, turn TurnMethod) PilotTransmission {
	if hdg <= 0 || hdg > 360 {
		return MakeUnexpectedPilotTransmission("unable. {hdg} isn't a valid heading", hdg)
	}

	nav.assignHeading(hdg, turn)

	switch turn {
	case TurnClosest:
		return MakePilotTransmission("[heading|fly heading] {hdg}", hdg)
	case TurnRight:
		return MakePilotTransmission("[right heading|right] {hdg}", hdg)
	case TurnLeft:
		return MakePilotTransmission("[left heading|left] {hdg}", hdg)
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}
}

func (nav *Nav) assignHeading(hdg float32, turn TurnMethod) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false

		// If an arrival is given a heading off of a route with altitude
		// constraints, set its cleared altitude to its current altitude
		// for now.
		if len(nav.Waypoints) > 0 && (nav.Waypoints[0].OnSTAR || nav.Waypoints[0].OnApproach) && nav.Altitude.Assigned == nil {
			if c := nav.getWaypointAltitudeConstraint(); c != nil {
				// Don't take a direct pointer to nav.FlightState.Altitude!
				alt := nav.FlightState.Altitude
				nav.Altitude.Cleared = &alt
			}
		}
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(NavHeading{Assigned: &hdg, Turn: &turn})
}

func (nav *Nav) FlyPresentHeading() PilotTransmission {
	nav.assignHeading(nav.FlightState.Heading, TurnClosest)
	return MakePilotTransmission("[fly present heading|present heading]")
}

func (nav *Nav) fixInRoute(fix string) bool {
	for i := range nav.Waypoints {
		if fix == nav.Waypoints[i].Fix {
			return true
		}
	}

	if ap := nav.Approach.Assigned; ap != nil {
		for _, route := range ap.Waypoints {
			for i := range route {
				if fix == route[i].Fix {
					return true
				}
			}
		}
	}
	return false
}

func (nav *Nav) fixPairInRoute(fixa, fixb string) (fa *Waypoint, fb *Waypoint) {
	find := func(f string, wp []Waypoint) int {
		return slices.IndexFunc(wp, func(wp Waypoint) bool { return wp.Fix == f })
	}

	var apWaypoints []WaypointArray
	if nav.Approach.Assigned != nil {
		apWaypoints = nav.Approach.Assigned.Waypoints
	}

	if ia := find(fixa, nav.Waypoints); ia != -1 {
		// First fix is in the current route
		fa = &nav.Waypoints[ia]
		if ib := find(fixb, nav.Waypoints[ia:]); ib != -1 {
			// As is the second, and after the first
			fb = &nav.Waypoints[ia+ib]
			return
		}
		for _, wp := range apWaypoints {
			if idx := find(fixb, wp); idx != -1 {
				fb = &wp[idx]
				return
			}
		}
	} else {
		// Check the approaches
		for _, wp := range apWaypoints {
			if ia := find(fixa, wp); ia != -1 {
				fa = &wp[ia]
				if ib := find(fixb, wp[ia:]); ib != -1 {
					fb = &wp[ia+ib]
					return
				}
			}
		}
	}
	return
}

func (nav *Nav) directFix(fix string) error {
	// Check the approach (if any) first; this way if the current route
	// ends with a fix that happens to be on the approach, we pick up the
	// rest of the approach fixes rather than forgetting about them.
	if ap := nav.Approach.Assigned; ap != nil {
		// This is a little hacky, but... Because of the way we currently
		// interpret ARINC424 files, fixes with procedure turns have no
		// procedure turn for routes with /nopt from the previous fix.
		// Therefore, if we are going direct to a fix that has a procedure
		// turn, we can't take the first matching route but have to keep
		// looking for it in case another route has it with a PT...
		found := false
		for _, route := range ap.Waypoints {
			for i, wp := range route {
				if wp.Fix == fix {
					nav.Waypoints = append(route[i:], nav.FlightState.ArrivalAirport)
					found = true
					if wp.ProcedureTurn != nil {
						break
					}
				}
			}
		}
		if found {
			return nil
		}
	}

	// Look for the fix in the waypoints in the flight plan.
	for i, wp := range nav.Waypoints {
		if fix == wp.Fix {
			nav.Waypoints = nav.Waypoints[i:]
			return nil
		}
	}

	// See if it's a random fix not in the flight plan.
	p, ok := func() (math.Point2LL, bool) {
		if p, ok := DB.LookupWaypoint(fix); ok {
			return p, true
		} else if ap, ok := DB.Airports[fix]; ok {
			return ap.Location, true
		} else if ap, ok := DB.Airports["K"+fix]; len(fix) == 3 && ok {
			return ap.Location, true
		}
		return math.Point2LL{}, false
	}()
	if ok {
		// Ignore ones that are >150nm away under the assumption that it's
		// a typo in that case.
		if math.NMDistance2LL(p, nav.FlightState.Position) > 150 {
			return ErrFixIsTooFarAway
		}

		nav.Waypoints = []Waypoint{
			Waypoint{
				Fix:      fix,
				Location: p,
			},
			nav.FlightState.ArrivalAirport,
		}
		return nil
	}

	return ErrInvalidFix
}

func (nav *Nav) DirectFix(fix string) PilotTransmission {
	if err := nav.directFix(fix); err == nil {
		nav.EnqueueHeading(NavHeading{})
		nav.Approach.NoPT = false
		nav.Approach.InterceptState = NotIntercepting
		return MakePilotTransmission("direct {fix}", fix)
	} else if err == ErrFixIsTooFarAway {
		return MakeUnexpectedPilotTransmission("unable. {fix} is too far away to go direct", fix)
	} else {
		return MakeUnexpectedPilotTransmission("unable. {fix} isn't a valid fix", fix)
	}
}

func (nav *Nav) DepartFixDirect(fixa string, fixb string) PilotTransmission {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return MakeUnexpectedPilotTransmission("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return MakeUnexpectedPilotTransmission("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return MakePilotTransmission("depart {fix} direct {fix}", fixa, fixb)
}

func (nav *Nav) DepartFixHeading(fix string, hdg float32) PilotTransmission {
	if hdg <= 0 || hdg > 360 {
		return MakeUnexpectedPilotTransmission("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return MakeUnexpectedPilotTransmission("unable. {fix} isn't in our route")
	}

	nfa := nav.FixAssignments[fix]
	h := float32(hdg)
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return MakePilotTransmission("depart {fix} heading {hdg}", fix, hdg)
}

func (nav *Nav) CrossFixAt(fix string, ar *AltitudeRestriction, speed int) PilotTransmission {
	if !nav.fixInRoute(fix) {
		return MakeUnexpectedPilotTransmission("unable. " + fix + " isn't in our route")
	}

	pt := MakePilotTransmission("cross {fix}", fix)

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		pt.Merge(ar.PilotTransmission())
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if speed != 0 {
		s := float32(speed)
		nfa.Arrive.Speed = &s
		pt.Add("at {spd}", s)
		// Delete other speed restrictions
		nav.Speed = NavSpeed{}
	}
	nav.FixAssignments[fix] = nfa

	return pt
}

func (nav *Nav) getApproach(airport *Airport, id string, lg *log.Logger) (*Approach, error) {
	if id == "" {
		return nil, ErrInvalidApproach
	}

	for name, appr := range airport.Approaches {
		if name == id {
			return appr, nil
		}
	}
	return nil, ErrUnknownApproach
}

func (nav *Nav) ExpectApproach(airport *Airport, id string, runwayWaypoints map[string]WaypointArray,
	lg *log.Logger) PilotTransmission {
	ap, err := nav.getApproach(airport, id, lg)
	if err != nil {
		return MakeUnexpectedPilotTransmission("unable. We don't know the {appr} approach.", id)
	}

	if id == nav.Approach.AssignedId && nav.Approach.Assigned != nil {
		return MakePilotTransmission("you already told us to expect the {appr} approach.", ap.FullName)
	}

	nav.Approach.Assigned = ap
	nav.Approach.AssignedId = id
	nav.Approach.ATPAVolume = airport.ATPAVolumes[ap.Runway]

	if waypoints := runwayWaypoints[ap.Runway]; len(waypoints) > 0 {
		if len(nav.Waypoints) == 0 {
			// Nothing left on our route; this shouldn't ever happen but
			// just in case patch the runway waypoints in there and hope it
			// works out.
			nav.Waypoints = append(util.DuplicateSlice(waypoints[1:]), nav.FlightState.ArrivalAirport)
		} else {
			// Try to splice the runway-specific waypoints in with the
			// aircraft's current waypoints...
			found := false
			for i, wp := range waypoints {
				if idx := slices.IndexFunc(nav.Waypoints, func(w Waypoint) bool { return w.Fix == wp.Fix }); idx != -1 {
					// This is a little messy: there are a handful of
					// modifiers we would like to carry over if they are
					// set though in general the waypoint from the approach
					// takes priority for things like altitude, speed, etc.
					nopt := nav.Waypoints[idx].NoPT
					humanHandoff := nav.Waypoints[idx].HumanHandoff
					tcpHandoff := nav.Waypoints[idx].TCPHandoff
					clearapp := nav.Waypoints[idx].ClearApproach

					// Keep the waypoints up to but not including the match.
					nav.Waypoints = nav.Waypoints[:idx]
					// Add the approach waypoints; take the matching one from there.
					nav.Waypoints = append(nav.Waypoints, waypoints[i:]...)
					// And add the destination airport again at the end.
					nav.Waypoints = append(nav.Waypoints, nav.FlightState.ArrivalAirport)

					nav.Waypoints[idx].NoPT = nopt
					nav.Waypoints[idx].HumanHandoff = humanHandoff
					nav.Waypoints[idx].TCPHandoff = tcpHandoff
					nav.Waypoints[idx].ClearApproach = clearapp

					found = true
					break
				}
			}

			if !found {
				// Most likely they were told to expect one runway, then
				// given a different one, but after they passed the common
				// set of waypoints on the arrival.  We'll replace the
				// waypoints but leave them on their current heading; then
				// it's over to the controller to either vector them or
				// send them direct somewhere reasonable...
				lg.Warn("aircraft waypoints don't match up with arrival runway waypoints. splicing...",
					slog.Any("aircraft", nav.Waypoints),
					slog.Any("runway", waypoints))
				nav.Waypoints = append(util.DuplicateSlice(waypoints), nav.FlightState.ArrivalAirport)

				hdg := nav.FlightState.Heading
				nav.Heading = NavHeading{Assigned: &hdg}
				nav.DeferredHeading = nil
			}
		}
	}

	return MakePilotTransmission("[we'll expect the|expecting the|we'll plan for the] {appr} approach", ap.FullName)
}

func (nav *Nav) InterceptApproach(airport string, lg *log.Logger) PilotTransmission {
	if nav.Approach.AssignedId == "" {
		return MakeUnexpectedPilotTransmission("you never told us to expect an approach")
	}

	_, onHeading := nav.AssignedHeading()

	if !(onHeading || (len(nav.Waypoints) > 0 && nav.Waypoints[0].OnApproach)) {
		return MakeUnexpectedPilotTransmission("we have to be on a heading or direct to an approach fix to intercept")
	}

	resp, err := nav.prepareForApproach(false, lg)
	if err != nil {
		return resp
	} else {
		ap := nav.Approach.Assigned
		if ap.Type == ILSApproach || ap.Type == LocalizerApproach {
			return MakePilotTransmission("[intercepting the {appr} approach|intercepting {appr}]", ap.FullName)
		} else {
			return MakePilotTransmission("[joining the {appr} approach course|joining {appr}]", ap.FullName)
		}
	}
}

func (nav *Nav) AtFixCleared(fix, id string) PilotTransmission {
	if nav.Approach.AssignedId == "" {
		return MakeUnexpectedPilotTransmission("you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned
	if ap == nil {
		return MakeUnexpectedPilotTransmission("unable. We were never told to expect an approach")
	}
	if nav.Approach.AssignedId != id {
		return MakeUnexpectedPilotTransmission("unable. We were told to expect the {appr} approach.", ap.FullName)
	}

	if !slices.ContainsFunc(nav.Waypoints, func(wp Waypoint) bool { return wp.Fix == fix }) {
		return MakeUnexpectedPilotTransmission("unable. {fix} is not in our route", fix)
	}
	nav.Approach.AtFixClearedRoute = nil
	for _, route := range ap.Waypoints {
		for i, wp := range route {
			if wp.Fix == fix {
				nav.Approach.AtFixClearedRoute = util.DuplicateSlice(route[i:])
			}
		}
	}

	return MakePilotTransmission("at {fix} cleared {appr}", fix, ap.FullName)
}

func (nav *Nav) prepareForApproach(straightIn bool, lg *log.Logger) (PilotTransmission, error) {
	if nav.Approach.AssignedId == "" {
		return MakeUnexpectedPilotTransmission("you never told us to expect an approach"),
			ErrClearedForUnexpectedApproach
	}

	ap := nav.Approach.Assigned

	// Charted visual is special in all sorts of ways
	if ap.Type == ChartedVisualApproach {
		return nav.prepareForChartedVisual()
	} else {
		// Otherwise we expect it to be able to return a final approach heading (and also ap.Line);
		// just check that once here so we don't spam the logs if this is an issue
		if _, ok := ap.Heading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation); !ok {
			lg.Warnf("%s (%s): no heading for approach?", ap.Id, ap.FullName)
		}
	}

	directApproachFix := false
	_, assignedHeading := nav.AssignedHeading()
	if !assignedHeading {
		// See if any of the waypoints in our route connect to the approach
	outer:
		for i, wp := range nav.Waypoints {
			for _, app := range ap.Waypoints {
				if idx := slices.IndexFunc(app, func(awp Waypoint) bool { return wp.Fix == awp.Fix }); idx != -1 {
					// Splice the routes
					directApproachFix = true
					nav.Waypoints = append(nav.Waypoints[:i], app[idx:]...)
					nav.Waypoints = append(nav.Waypoints, nav.FlightState.ArrivalAirport)
					break outer
				}
			}
		}
	}

	if directApproachFix {
		// all good
	} else if assignedHeading {
		nav.Approach.InterceptState = InitialHeading
	} else {
		return MakeUnexpectedPilotTransmission("unable. We need either direct or a heading to intercept"),
			ErrUnableCommand
	}
	// If the aircraft is on a heading, there's nothing more to do for
	// now; keep flying the heading and after we intercept we'll add
	// the rest of the waypoints to the aircraft's waypoints array.

	// No procedure turn if it intercepts via a heading
	nav.Approach.NoPT = straightIn || assignedHeading

	return PilotTransmission{}, nil
}

func (nav *Nav) prepareForChartedVisual() (PilotTransmission, error) {
	// Airport PostDeserialize() checks that there is just a single set of
	// waypoints for charted visual approaches.
	wp := nav.Approach.Assigned.Waypoints[0]

	// First try to find the first (if any) waypoint along the approach
	// that is within 15 degrees of the aircraft's current heading.
	intercept := -1
	for i := range wp {
		h := math.Heading2LL(nav.FlightState.Position, wp[i].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if math.HeadingDifference(h, nav.FlightState.Heading) < 30 {
			intercept = i
			break
		}
	}

	// Also check for intercepting a segment of the approach. There are two
	// cases:
	// 1. If we found a waypoint intercept above, then we are only
	//    interested in the segment from that waypoint to the subsequent
	//    one; we will take that if we find it (so the aircraft can stay
	//    on its present heading) but will not take a later one (so that it
	//    gets on the approach sooner rather than later.)
	// 2. If no waypoint intercept is found, we will take the first
	//    intercept with an approach segment. This case should be unusual
	//    but may come into play when an aircraft is very close to the
	//    approach route and no waypoints are close to its current course.

	// Work in nm coordinates
	pac0 := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
	// Find a second point along its current course (note: ignoring wind)
	hdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	dir := [2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}
	pac1 := math.Add2f(pac0, dir)

	checkSegment := func(i int) *Waypoint {
		if i+1 == len(wp) {
			return nil
		}
		pl0 := math.LL2NM(wp[i].Location, nav.FlightState.NmPerLongitude)
		pl1 := math.LL2NM(wp[i+1].Location, nav.FlightState.NmPerLongitude)

		if pi, ok := math.LineLineIntersect(pac0, pac1, pl0, pl1); ok {
			// We only want intersections along the segment from pl0 to pl1
			// and not along the infinite line they define, so this is a
			// hacky check to limit to that.
			if math.Extent2DFromPoints([][2]float32{pl0, pl1}).Inside(pi) {
				return &Waypoint{
					Fix:      "intercept",
					Location: math.NM2LL(pi, nav.FlightState.NmPerLongitude),
				}
			}
		}
		return nil
	}

	// wi will store the route the aircraft will fly if it is going to join
	// the approach.
	var wi []Waypoint

	if intercept == -1 { // check all of the segments
		for i := range wp {
			if w := checkSegment(i); w != nil {
				// Take the first one that works
				wi = append([]Waypoint{*w}, wp[i+1:]...)
				break
			}
		}
	} else {
		// Just check the segment after the waypoint we're considering
		if w := checkSegment(intercept); w != nil {
			wi = append([]Waypoint{*w}, wp[intercept+1:]...)
		} else {
			// No problem if it doesn't intersect that segment; just start
			// the route from that waypoint.
			wi = wp[intercept:]
		}
	}

	if wi != nil {
		// Update the route and go direct to the intercept point.
		nav.Waypoints = append(wi, nav.FlightState.ArrivalAirport)
		nav.Heading = NavHeading{}
		nav.DeferredHeading = nil
		return PilotTransmission{}, nil
	}

	return MakeUnexpectedPilotTransmission("unable. We are not on course to intercept the approach"),
		ErrUnableCommand
}

func (nav *Nav) clearedApproach(airport string, id string, straightIn bool, lg *log.Logger) (PilotTransmission, error) {
	ap := nav.Approach.Assigned
	if ap == nil {
		return MakeUnexpectedPilotTransmission("unable. We haven't been told to expect an approach"),
			ErrClearedForUnexpectedApproach
	}
	if nav.Approach.AssignedId != id {
		return MakeUnexpectedPilotTransmission("unable. We were told to expect the {appr} approach.", ap.FullName),
			ErrClearedForUnexpectedApproach
	}

	if resp, err := nav.prepareForApproach(straightIn, lg); err != nil {
		return resp, err
	} else {
		nav.Approach.Cleared = true
		if nav.Approach.PassedApproachFix {
			// We've already passed an approach fix, so allow it to start descending.
			nav.Altitude = NavAltitude{}
		} else if nav.Approach.InterceptState == OnApproachCourse || nav.Approach.PassedApproachFix {
			// First intercepted then cleared or otherwise passed an
			// approach fix, so allow it to start descending.
			nav.Altitude = NavAltitude{}
			// No procedure turn needed if we were vectored to intercept.
			nav.Approach.NoPT = true
		}
		// Cleared approach also cancels speed restrictions.
		nav.Speed = NavSpeed{}

		nav.flyProcedureTurnIfNecessary()

		if straightIn {
			return MakePilotTransmission("cleared straight in {appr} [approach|]", ap.FullName), nil
		} else {
			return MakePilotTransmission("cleared {appr} [approach|]", ap.FullName), nil
		}
	}
}

func (nav *Nav) CancelApproachClearance() PilotTransmission {
	if !nav.Approach.Cleared {
		return MakeUnexpectedPilotTransmission("we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false

	return MakePilotTransmission("cancel approach clearance.")
}

func (nav *Nav) ClimbViaSID() PilotTransmission {
	if len(nav.Waypoints) == 0 || !nav.Waypoints[0].OnSID {
		return MakeUnexpectedPilotTransmission("unable. We're not flying a departure procedure")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
	return MakePilotTransmission("climb via the SID")
}

func (nav *Nav) DescendViaSTAR() PilotTransmission {
	if len(nav.Waypoints) == 0 || !nav.Waypoints[0].OnSTAR {
		return MakeUnexpectedPilotTransmission("unable. We're not on a STAR")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueHeading(NavHeading{})
	return MakePilotTransmission("descend via the STAR")
}

func (nav *Nav) DistanceAlongRoute(fix string) (float32, error) {
	if nav.Heading.Assigned != nil {
		return 0, ErrNotFlyingRoute
	}
	if len(nav.Waypoints) == 0 {
		return 0, nil
	} else {
		index := slices.IndexFunc(nav.Waypoints, func(wp Waypoint) bool { return wp.Fix == fix })
		if index == -1 {
			return 0, ErrFixNotInRoute
		}
		wp := nav.Waypoints[:index+1]
		distance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
		for i := 0; i < len(wp)-1; i++ {
			distance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
		}
		return distance, nil
	}
}

func (nav *Nav) ResumeOwnNavigation() PilotTransmission {
	if nav.Heading.Assigned == nil {
		return MakePilotTransmission("I don't think you ever put us on a heading...")
	}

	nav.Heading = NavHeading{}
	nav.DeferredHeading = nil

	if len(nav.Waypoints) > 1 {
		// Find the route segment we're closest to then go direct to the
		// end of it.  In some cases for the first segment maybe it's
		// preferable to go to the first fix but it's a little unclear what
		// the criteria should be.
		minDist := float32(1000000)
		startIdx := 0
		pac := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
		for i := 0; i < len(nav.Waypoints)-1; i++ {
			wp0, wp1 := nav.Waypoints[i], nav.Waypoints[i+1]
			p0 := math.LL2NM(wp0.Location, nav.FlightState.NmPerLongitude)
			p1 := math.LL2NM(wp1.Location, nav.FlightState.NmPerLongitude)
			if d := math.PointSegmentDistance(pac, p0, p1); d < minDist {
				minDist = d
				startIdx = i + 1
			}
		}
		nav.Waypoints = nav.Waypoints[startIdx:]
	}
	return MakePilotTransmission("[own navigation|resuming own navigation]")
}

func (nav *Nav) AltitudeOurDiscretion() PilotTransmission {
	if nav.Altitude.Assigned == nil {
		return MakePilotTransmission("You never assigned us an altitude...")
	}

	nav.Altitude = NavAltitude{}
	alt := nav.FinalAltitude
	nav.Altitude.Cleared = &alt

	return MakePilotTransmission("[altitude our discretion|altitude our discretion, maintain VFR]")
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}

///////////////////////////////////////////////////////////////////////////
// Procedure turns

type FlyRacetrackPT struct {
	ProcedureTurn      *ProcedureTurn
	Fix                string
	FixLocation        math.Point2LL
	Entry              RacetrackPTEntry
	InboundHeading     float32
	OutboundHeading    float32
	OutboundTurnRate   float32
	OutboundTurnMethod TurnMethod
	OutboundLegLength  float32
	State              int
}

const (
	PTStateApproaching = iota
	PTStateTurningOutbound
	PTStateFlyingOutbound
	PTStateTurningInbound
	PTStateFlyingInbound // parallel entry only
)

type FlyStandard45PT struct {
	ProcedureTurn    *ProcedureTurn
	Fix              string
	FixLocation      math.Point2LL
	InboundHeading   float32 // fix->airport
	AwayHeading      float32 // outbound + 45 offset
	State            int
	SecondsRemaining int
}

const (
	PT45StateApproaching = iota
	PT45StateTurningOutbound
	PT45StateFlyingOutbound
	PT45StateTurningAway
	PT45StateFlyingAway
	PT45StateTurningIn
	PT45StateFlyingIn
	PT45StateTurningToIntercept
)

func (nav *Nav) flyProcedureTurnIfNecessary() {
	wp := nav.Waypoints
	if !nav.Approach.Cleared || len(wp) < 2 || wp[0].ProcedureTurn == nil || nav.Approach.NoPT {
		return
	}

	if wp[0].ProcedureTurn.Entry180NoPT {
		inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)

		acFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if math.HeadingDifference(acFixHeading, inboundHeading) < 90 {
			return
		}
	}

	switch wp[0].ProcedureTurn.Type {
	case PTRacetrack:
		// Immediate heading update here (and below) since it's the
		// autopilot doing this at the appropriate time (vs. a controller
		// instruction.)
		nav.Heading = NavHeading{RacetrackPT: MakeFlyRacetrackPT(nav, wp)}
		nav.DeferredHeading = nil
	case PTStandard45:
		nav.Heading = NavHeading{Standard45PT: MakeFlyStandard45PT(nav, wp)}
		nav.DeferredHeading = nil

	default:
		panic("Unhandled procedure turn type")
	}
}

func MakeFlyStandard45PT(nav *Nav, wp []Waypoint) *FlyStandard45PT {
	inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	awayHeading := math.OppositeHeading(inboundHeading)
	awayHeading += float32(util.Select(wp[0].ProcedureTurn.RightTurns, -45, 45))

	return &FlyStandard45PT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		AwayHeading:    math.NormalizeHeading(awayHeading),
		State:          PTStateApproaching,
	}
}

func MakeFlyRacetrackPT(nav *Nav, wp []Waypoint) *FlyRacetrackPT {
	inboundHeading := math.Heading2LL(wp[0].Location, wp[1].Location, nav.FlightState.NmPerLongitude,
		nav.FlightState.MagneticVariation)

	aircraftFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
		nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

	pt := wp[0].ProcedureTurn

	fp := &FlyRacetrackPT{
		ProcedureTurn:  wp[0].ProcedureTurn,
		Entry:          pt.SelectRacetrackEntry(inboundHeading, aircraftFixHeading),
		Fix:            wp[0].Fix,
		FixLocation:    wp[0].Location,
		InboundHeading: inboundHeading,
		State:          PTStateApproaching,
	}

	// Set the outbound heading. For everything but teardrop, it's the
	// opposite of the inbound heading.
	fp.OutboundHeading = math.OppositeHeading(fp.InboundHeading)
	if fp.Entry == TeardropEntry {
		// For teardrop, it's offset by 30 degrees, toward the outbound
		// track.
		if pt.RightTurns {
			fp.OutboundHeading = math.NormalizeHeading(fp.OutboundHeading - 30)
		} else {
			fp.OutboundHeading = math.NormalizeHeading(fp.OutboundHeading + 30)
		}
	}

	// Set the outbound turn rate
	fp.OutboundTurnRate = float32(StandardTurnRate)
	if fp.Entry == DirectEntryShortTurn {
		// Since we have less than 180 degrees in our turn, turn more
		// slowly so that we more or less end up the right offset distance
		// from the inbound path.
		acFixHeading := math.Heading2LL(nav.FlightState.Position, wp[0].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		diff := math.HeadingDifference(fp.OutboundHeading, acFixHeading)
		fp.OutboundTurnRate = 3 * diff / 180

		//lg.Debugf("hdg %.0f outbound hdg %.0f diff %.0f -> rate %.1f",
		//acFixHeading, fp.OutboundHeading, math.HeadingDifference(fp.OutboundHeading, acFixHeading),
		//fp.OutboundTurnRate)
	}

	// Set the outbound turn method.
	fp.OutboundTurnMethod = TurnMethod(util.Select(pt.RightTurns, TurnRight, TurnLeft))
	if fp.Entry == ParallelEntry {
		// Swapped turn direction
		fp.OutboundTurnMethod = TurnMethod(util.Select(pt.RightTurns, TurnLeft, TurnRight))
	} else if fp.Entry == TeardropEntry {
		fp.OutboundTurnMethod = TurnClosest
	}

	// Figure out the outbound leg length.
	// Specified by the user?
	fp.OutboundLegLength = float32(pt.NmLimit) / 2
	if fp.OutboundLegLength == 0 {
		fp.OutboundLegLength = float32(pt.MinuteLimit) * nav.FlightState.GS / 60
	}
	if fp.OutboundLegLength == 0 {
		// Select a default based on the approach type.
		switch nav.Approach.Assigned.Type {
		case ILSApproach, LocalizerApproach, VORApproach:
			// 1 minute by default on these
			fp.OutboundLegLength = nav.FlightState.GS / 60
		case RNAVApproach:
			// 4nm by default for RNAV, though that's the distance from the
			// fix, so turn earlier...
			fp.OutboundLegLength = 2

		default:
			panic(fmt.Sprintf("unhandled approach type: %s", nav.Approach.Assigned.Type))
			//fp.OutboundLegLength = nav.FlightState.GS / 60
		}
	}
	// Lengthen it a bit for teardrop since we're flying along the
	// diagonal.
	if fp.Entry == TeardropEntry {
		fp.OutboundLegLength *= 1.5
	}

	return fp
}

func (fp *FlyRacetrackPT) GetHeading(nav *Nav, wind WindModel, lg *log.Logger) (float32, TurnMethod, float32) {
	pt := fp.ProcedureTurn

	switch fp.State {
	case PTStateApproaching:
		dist := math.NMDistance2LL(nav.FlightState.Position, fp.FixLocation)
		eta := dist / nav.FlightState.GS * 3600 // in seconds
		startTurn := false

		switch fp.Entry {
		case DirectEntryShortTurn:
			startTurn = eta < 2

		case DirectEntryLongTurn:
			// Turn start is based on lining up for the inbound heading,
			// even though the actual turn will be that plus 180.
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.InboundHeading,
				fp.OutboundTurnMethod, wind, lg)
		case ParallelEntry, TeardropEntry:
			startTurn = nav.shouldTurnForOutbound(fp.FixLocation, fp.OutboundHeading,
				fp.OutboundTurnMethod, wind, lg)
		}

		if startTurn {
			fp.State = PTStateTurningOutbound
			lg.Debugf("starting outbound turn-heading %.1f rate %.2f method %s",
				fp.OutboundHeading, fp.OutboundTurnRate, fp.OutboundTurnMethod.String())
		}

		// Even if we're turning, this last time we'll keep the heading to
		// the fix.
		fixHeading := math.Heading2LL(nav.FlightState.Position, fp.FixLocation, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)

		return fixHeading, TurnClosest, StandardTurnRate
	case PTStateTurningOutbound:
		if math.HeadingDifference(nav.FlightState.Heading, fp.OutboundHeading) < 1 {
			// Finished the turn; now we'll fly the leg.
			lg.Debugf("finished the turn; ac heading %.1f outbound %.1f; flying outbound leg",
				nav.FlightState.Heading, fp.OutboundHeading)
			fp.State = PTStateFlyingOutbound
		}

		return fp.OutboundHeading, fp.OutboundTurnMethod, fp.OutboundTurnRate

	case PTStateFlyingOutbound:
		d := math.NMDistance2LL(nav.FlightState.Position, fp.FixLocation)

		if fp.Entry == TeardropEntry {
			// start the turn when we will intercept the inbound radial
			turn := TurnMethod(util.Select(pt.RightTurns, TurnRight, TurnLeft))
			if d > 0.5 && nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
				lg.Debug("teardrop Turning inbound!")
				fp.State = PTStateTurningInbound
			}
		} else if d > fp.OutboundLegLength {
			lg.Debug("Turning inbound!")
			fp.State = PTStateTurningInbound
		}
		return fp.OutboundHeading, TurnClosest, fp.OutboundTurnRate

	case PTStateTurningInbound:
		if fp.Entry == ParallelEntry {
			// Parallel is special: we fly at the 30 degree
			// offset-from-true-inbound heading until it is time to turn to
			// intercept.
			hdg := math.NormalizeHeading(fp.InboundHeading + float32(util.Select(pt.RightTurns, -30, 30)))
			lg.Debugf("parallel inbound turning to %.1f", hdg)
			if math.HeadingDifference(nav.FlightState.Heading, hdg) < 1 {
				fp.State = PTStateFlyingInbound
			}
			// This turn is in the opposite direction than usual
			turn := util.Select(!pt.RightTurns, TurnRight, TurnLeft)
			return hdg, TurnMethod(turn), StandardTurnRate
		} else {
			if math.HeadingDifference(nav.FlightState.Heading, fp.InboundHeading) < 1 {
				// otherwise go direct to the fix
				lg.Debug("direct fix--done with the HILPT!")
				nav.Heading = NavHeading{}
				nav.Altitude = NavAltitude{}
			}

			turn := util.Select(pt.RightTurns, TurnRight, TurnLeft)
			return fp.InboundHeading, TurnMethod(turn), StandardTurnRate
		}

	case PTStateFlyingInbound:
		// This state is only used for ParallelEntry
		turn := TurnMethod(util.Select(pt.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
			lg.Debug("parallel inbound direct fix")
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
		}
		hdg := math.NormalizeHeading(fp.InboundHeading + float32(util.Select(pt.RightTurns, -30, 30)))
		return hdg, TurnClosest, StandardTurnRate
	default:
		panic("unhandled state")
	}
}

func (fp *FlyRacetrackPT) GetAltitude(nav *Nav) (float32, bool) {
	descend := fp.ProcedureTurn.ExitAltitude != 0 &&
		nav.FlightState.Altitude > float32(fp.ProcedureTurn.ExitAltitude) &&
		fp.State != PTStateApproaching
	return float32(fp.ProcedureTurn.ExitAltitude), descend
}

func (fp *FlyStandard45PT) GetHeading(nav *Nav, wind WindModel, lg *log.Logger) (float32, TurnMethod, float32) {
	outboundHeading := math.OppositeHeading(fp.InboundHeading)

	switch fp.State {
	case PT45StateApproaching:
		if nav.shouldTurnForOutbound(fp.FixLocation, outboundHeading, TurnClosest, wind, lg) {
			lg.Debugf("turning outbound to %.0f", outboundHeading)
			fp.State = PT45StateTurningOutbound
		}

		// Fly toward the fix until it's time to turn outbound
		fixHeading := math.Heading2LL(nav.FlightState.Position, fp.FixLocation,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		return fixHeading, TurnClosest, StandardTurnRate
	case PT45StateTurningOutbound:
		if nav.FlightState.Heading == outboundHeading {
			fp.State = PTStateFlyingOutbound
			fp.SecondsRemaining = 60
			lg.Debugf("flying outbound for %ds", fp.SecondsRemaining)
		}
		return outboundHeading, TurnClosest, StandardTurnRate
	case PT45StateFlyingOutbound:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningAway
			lg.Debugf("turning away from outbound to %.0f", fp.AwayHeading)

		}
		return outboundHeading, TurnClosest, StandardTurnRate
	case PT45StateTurningAway:
		if nav.FlightState.Heading == fp.AwayHeading {
			fp.State = PT45StateFlyingAway
			fp.SecondsRemaining = 60
			lg.Debugf("flying away for %ds", fp.SecondsRemaining)
		}

		return fp.AwayHeading, TurnClosest, StandardTurnRate
	case PT45StateFlyingAway:
		fp.SecondsRemaining--
		if fp.SecondsRemaining == 0 {
			fp.State = PT45StateTurningIn
			lg.Debugf("turning in to %.0f", math.OppositeHeading(fp.AwayHeading))
		}
		return fp.AwayHeading, TurnClosest, StandardTurnRate
	case PT45StateTurningIn:
		hdg := math.OppositeHeading(fp.AwayHeading)
		if nav.FlightState.Heading == hdg {
			fp.State = PT45StateFlyingIn
			lg.Debug("flying in")
		}

		turn := util.Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft)
		return hdg, TurnMethod(turn), StandardTurnRate
	case PT45StateFlyingIn:
		turn := TurnMethod(util.Select(fp.ProcedureTurn.RightTurns, TurnRight, TurnLeft))
		if nav.shouldTurnToIntercept(fp.FixLocation, fp.InboundHeading, turn, wind, lg) {
			fp.State = PT45StateTurningToIntercept
			lg.Debugf("starting turn to intercept %.0f", fp.InboundHeading)
		}
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate
	case PT45StateTurningToIntercept:
		if nav.FlightState.Heading == fp.InboundHeading {
			nav.Heading = NavHeading{}
			nav.Altitude = NavAltitude{}
			lg.Debugf("done! direct to the fix now")
		}

		return fp.InboundHeading, TurnClosest, StandardTurnRate
	default:
		lg.Errorf("unhandled PT state: %d", fp.State)
		return nav.FlightState.Heading, TurnClosest, StandardTurnRate
	}
}

func StartAirwork(wp Waypoint, nav Nav) *NavAirwork {
	a := &NavAirwork{
		Radius:         float32(wp.AirworkRadius),
		Center:         wp.Location,
		AltRange:       wp.AltitudeRestriction.Range,
		RemainingSteps: wp.AirworkMinutes * 60, // sim ticks are 1 second.
		Altitude:       nav.FlightState.Altitude,
	}

	a.Start360(nav)

	return a
}

func (aw *NavAirwork) Update(nav *Nav) bool {
	// Tick down the number of seconds we're doing this.
	aw.RemainingSteps--
	if aw.RemainingSteps == 0 {
		// Direct to the next waypoint in the route
		nav.Heading = NavHeading{}
		return false
	}

	// If we're getting close to the maximum distance from the center
	// point, turn back toward it.
	d := math.NMDistance2LL(nav.FlightState.Position, aw.Center)
	if aw.ToCenter && d < 1 {
		// Close enough
		aw.ToCenter = false
	} else if float32(aw.Radius)-d < 2.5 || aw.ToCenter {
		aw.Heading = math.Heading2LL(nav.FlightState.Position, aw.Center, nav.FlightState.NmPerLongitude,
			nav.FlightState.MagneticVariation)
		aw.TurnRate = StandardTurnRate
		aw.TurnDirection = TurnClosest
		aw.ToCenter = true
		return true
	}

	// Don't check IAS; we only care that we reach the heading and altitude
	// we wanted to do next.
	if nav.FlightState.Heading == aw.Heading && nav.FlightState.Altitude == aw.Altitude {
		if aw.NextMoveCounter == 0 {
			// We just finished. Clean up and Continue straight and level for a bit.
			aw.Dive = false
			aw.NextMoveCounter = 5 + nav.Rand.Intn(25)
		} else if aw.NextMoveCounter == 1 {
			// Pick a new thing.
			aw.ToCenter = false
			if nav.Rand.Float32() < .2 {
				// Do a 360
				aw.Start360(*nav)
			} else if nav.FlightState.Altitude > aw.AltRange[0]+2000 && nav.Rand.Float32() < .2 {
				// Dive.
				aw.Dive = true
				aw.Altitude = aw.AltRange[0] + 200*nav.Rand.Float32()
			} else if nav.FlightState.Altitude+1000 < aw.AltRange[1] && nav.Rand.Float32() < .2 {
				// Climbing turn
				aw.Altitude = aw.AltRange[1] - 500*nav.Rand.Float32()
				aw.Heading = 360 * nav.Rand.Float32()
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, TurnLeft, TurnRight)
			} else if nav.FlightState.Altitude < aw.AltRange[0]+1000 && nav.Rand.Float32() < .2 {
				// Descending turn
				aw.Altitude = aw.AltRange[0] + 500*nav.Rand.Float32()
				aw.Heading = 360 * nav.Rand.Float32()
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, TurnLeft, TurnRight)
			} else if nav.Rand.Float32() < .2 {
				// Slow turn
				aw.Heading = 360 * nav.Rand.Float32()
				aw.IAS = math.Lerp(.1, nav.Perf.Speed.Min, TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude))
				aw.TurnDirection = util.Select(nav.Rand.Float32() < .5, TurnLeft, TurnRight)
			} else if nav.Rand.Float32() < .2 {
				// Slow, straight and level
				aw.IAS = math.Lerp(.1, nav.Perf.Speed.Min, TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude))
				aw.NextMoveCounter = 20
			} else {
				// Straight and level and then we'll reconsider.
				aw.NextMoveCounter = 10
			}
		}
		// Tick
		aw.NextMoveCounter--
	}

	return true
}

func (aw *NavAirwork) Start360(nav Nav) {
	if nav.Rand.Intn(2) == 0 {
		aw.TurnDirection = TurnLeft
		aw.Heading = math.NormalizeHeading(nav.FlightState.Heading + 1)
	} else {
		aw.TurnDirection = TurnRight
		aw.Heading = math.NormalizeHeading(nav.FlightState.Heading - 1)
	}
	aw.TurnRate = StandardTurnRate
}

func (aw *NavAirwork) TargetHeading() (heading float32, turn TurnMethod, rate float32) {
	return aw.Heading, aw.TurnDirection, aw.TurnRate
}

func (aw *NavAirwork) TargetAltitude() (float32, float32) {
	return aw.Altitude, float32(util.Select(aw.Dive, 3000, 500))
}

func (aw *NavAirwork) TargetSpeed() (float32, float32, bool) {
	if aw.IAS == 0 {
		return 0, 0, false
	}
	return aw.IAS, 10, true
}

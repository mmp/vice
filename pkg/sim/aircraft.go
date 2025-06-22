// pkg/sim/aircraft.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"
)

type Aircraft struct {
	// This is ADS-B callsign of the aircraft. Just because different the
	// callsign in the flight plan can be different across multiple STARS
	// facilities, so two different facilities can show different
	// callsigns; however, the ADS-B callsign is transmitted from the
	// aircraft and would be the same to all facilities.
	ADSBCallsign av.ADSBCallsign

	Squawk av.Squawk
	Mode   av.TransponderMode

	IdentStartTime, IdentEndTime time.Time

	FlightPlan   av.FlightPlan
	TypeOfFlight av.TypeOfFlight

	Strip av.FlightStrip

	// State related to navigation.
	Nav Nav

	// Arrival-related state
	STAR                string
	STARRunwayWaypoints map[string]av.WaypointArray
	GotContactTower     bool

	STARSFlightPlan *STARSFlightPlan

	HoldForRelease    bool
	Released          bool // only used for hold for release
	ReleaseTime       time.Time
	WaitingForLaunch  bool // for departures
	MissingFlightPlan bool

	GoAroundDistance *float32

	// Departure related state
	DepartureContactAltitude float32

	// The controller who gave approach clearance
	ApproachController string

	// Who had control when the fp disassociated due to an arrival filter.
	PreArrivalDropController string

	InDepartureFilter bool

	FirstSeen time.Time

	RequestedFlightFollowing bool

	Voice speech.Voice
}

func (ac *Aircraft) GetRadarTrack(now time.Time) av.RadarTrack {
	return av.RadarTrack{
		ADSBCallsign:        ac.ADSBCallsign,
		Squawk:              util.Select(ac.Mode != av.TransponderModeStandby, ac.Squawk, av.Squawk(0)),
		Mode:                ac.Mode,
		Ident:               ac.Mode != av.TransponderModeStandby && now.After(ac.IdentStartTime) && now.Before(ac.IdentEndTime),
		TrueAltitude:        ac.Altitude(),
		TransponderAltitude: util.Select(ac.Mode == av.TransponderModeAltitude, ac.Altitude(), 0),
		Location:            ac.Position(),
		Heading:             ac.Heading(),
		Groundspeed:         ac.GS(),
		TypeOfFlight:        ac.TypeOfFlight,
	}
}

func (ac *Aircraft) InitializeFlightPlan(r av.FlightRules, acType, dep, arr string) {
	ac.FlightPlan = av.FlightPlan{
		Rules:            r,
		AircraftType:     acType,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
		CruiseSpeed:      int(ac.AircraftPerformance().Speed.CruiseTAS),
	}
}

func (ac *Aircraft) TAS() float32 {
	return ac.Nav.TAS()
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

func (ac *Aircraft) Update(wind av.WindModel, bravo *av.AirspaceGrid, lg *log.Logger) *av.Waypoint {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	passedWaypoint := ac.Nav.Update(wind, &ac.FlightPlan, bravo, lg)
	if passedWaypoint != nil {
		lg.Info("passed", slog.Any("waypoint", passedWaypoint))
	}

	return passedWaypoint
}

func (ac *Aircraft) GoAround() *speech.RadioTransmission {
	ac.GotContactTower = false
	return ac.Nav.GoAround()
}

func (ac *Aircraft) Ident(now time.Time) *speech.RadioTransmission {
	ac.IdentStartTime = now.Add(time.Duration(2+ac.Nav.Rand.Intn(3)) * time.Second) // delay the start a bit
	ac.IdentEndTime = ac.IdentStartTime.Add(10 * time.Second)
	return speech.MakeReadbackTransmission("ident")
}

func (ac *Aircraft) AssignAltitude(altitude int, afterSpeed bool) *speech.RadioTransmission {
	return ac.Nav.AssignAltitude(float32(altitude), afterSpeed)
}

func (ac *Aircraft) AssignSpeed(speed int, afterAltitude bool) *speech.RadioTransmission {
	return ac.Nav.AssignSpeed(float32(speed), afterAltitude)
}

func (ac *Aircraft) MaintainSlowestPractical() *speech.RadioTransmission {
	return ac.Nav.MaintainSlowestPractical()
}

func (ac *Aircraft) MaintainMaximumForward() *speech.RadioTransmission {
	return ac.Nav.MaintainMaximumForward()
}

func (ac *Aircraft) SaySpeed() *speech.RadioTransmission {
	return ac.Nav.SaySpeed()
}

func (ac *Aircraft) SayHeading() *speech.RadioTransmission {
	return ac.Nav.SayHeading()
}

func (ac *Aircraft) SayAltitude() *speech.RadioTransmission {
	return ac.Nav.SayAltitude()
}

func (ac *Aircraft) ExpediteDescent() *speech.RadioTransmission {
	return ac.Nav.ExpediteDescent()
}

func (ac *Aircraft) ExpediteClimb() *speech.RadioTransmission {
	return ac.Nav.ExpediteClimb()
}

func (ac *Aircraft) AssignHeading(heading int, turn TurnMethod) *speech.RadioTransmission {
	return ac.Nav.AssignHeading(float32(heading), turn)
}

func (ac *Aircraft) TurnLeft(deg int) *speech.RadioTransmission {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading - float32(deg))
	ac.Nav.AssignHeading(hdg, TurnLeft)
	return speech.MakeReadbackTransmission("[turn {num} degrees left|{num} to the left|{num} left]", deg)
}

func (ac *Aircraft) TurnRight(deg int) *speech.RadioTransmission {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading + float32(deg))
	ac.Nav.AssignHeading(hdg, TurnRight)
	return speech.MakeReadbackTransmission("[turn {num} degrees right|{num} to the right|{num} right]", deg)
}

func (ac *Aircraft) FlyPresentHeading() *speech.RadioTransmission {
	return ac.Nav.FlyPresentHeading()
}

func (ac *Aircraft) DirectFix(fix string) *speech.RadioTransmission {
	return ac.Nav.DirectFix(strings.ToUpper(fix))
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) *speech.RadioTransmission {
	return ac.Nav.DepartFixHeading(strings.ToUpper(fix), float32(hdg))
}

func (ac *Aircraft) DepartFixDirect(fixa, fixb string) *speech.RadioTransmission {
	return ac.Nav.DepartFixDirect(strings.ToUpper(fixa), strings.ToUpper(fixb))
}

func (ac *Aircraft) CrossFixAt(fix string, ar *av.AltitudeRestriction, speed int) *speech.RadioTransmission {
	return ac.Nav.CrossFixAt(strings.ToUpper(fix), ar, speed)
}

func (ac *Aircraft) ExpectApproach(id string, ap *av.Airport, lg *log.Logger) *speech.RadioTransmission {
	return ac.Nav.ExpectApproach(ap, id, ac.STARRunwayWaypoints, lg)
}

func (ac *Aircraft) AssignedApproach() string {
	return ac.Nav.Approach.AssignedId
}

func (ac *Aircraft) AtFixCleared(fix, approach string) *speech.RadioTransmission {
	return ac.Nav.AtFixCleared(fix, approach)
}

func (ac *Aircraft) ClearedApproach(id string, lg *log.Logger) (*speech.RadioTransmission, error) {
	return ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, false, lg)
}

func (ac *Aircraft) ClearedStraightInApproach(id string, lg *log.Logger) (*speech.RadioTransmission, error) {
	return ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true, lg)
}

func (ac *Aircraft) CancelApproachClearance() *speech.RadioTransmission {
	return ac.Nav.CancelApproachClearance()
}

func (ac *Aircraft) ClimbViaSID() *speech.RadioTransmission {
	return ac.Nav.ClimbViaSID()
}

func (ac *Aircraft) DescendViaSTAR() *speech.RadioTransmission {
	return ac.Nav.DescendViaSTAR()
}

func (ac *Aircraft) ResumeOwnNavigation() *speech.RadioTransmission {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return speech.MakeUnexpectedTransmission("unable. We're IFR")
	} else {
		return ac.Nav.ResumeOwnNavigation()
	}
}

func (ac *Aircraft) AltitudeOurDiscretion() *speech.RadioTransmission {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return speech.MakeUnexpectedTransmission("unable. We're IFR")
	} else {
		return ac.Nav.AltitudeOurDiscretion()
	}
}

func (ac *Aircraft) ContactTower(lg *log.Logger) *speech.RadioTransmission {
	if ac.GotContactTower {
		// No response; they're not on our frequency any more.
		return nil
	} else if ac.Nav.Approach.Assigned == nil {
		return speech.MakeUnexpectedTransmission("unable. We haven't been given an approach.")
	} else if !ac.Nav.Approach.Cleared {
		return speech.MakeUnexpectedTransmission("unable. We haven't been cleared for the approach.")
	} else {
		ac.GotContactTower = true
		return speech.MakeReadbackTransmission("contact tower")
	}
}

func (ac *Aircraft) InterceptApproach(lg *log.Logger) *speech.RadioTransmission {
	return ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport, lg)
}

func (ac *Aircraft) InitializeArrival(ap *av.Airport, arr *av.Arrival, nmPerLongitude float32, magneticVariation float32,
	wind av.WindModel, now time.Time, lg *log.Logger) error {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, rand.Make())
	}
	if arr.Route != "" {
		ac.FlightPlan.Route = arr.Route
	} else {
		ac.FlightPlan.Route = "/. " + arr.STAR
	}
	ac.TypeOfFlight = av.FlightTypeArrival

	nav := MakeArrivalNav(ac.ADSBCallsign, arr, ac.FlightPlan, perf, nmPerLongitude, magneticVariation,
		wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if arr.ExpectApproach.A != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
		ac.ExpectApproach(*arr.ExpectApproach.A, ap, lg)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
			ac.ExpectApproach(app, ap, lg)
		}
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(ap *av.Airport, departureAirport string, dep *av.Departure,
	runway string, exitRoute av.ExitRoute, nmPerLongitude float32, magneticVariation float32,
	wind av.WindModel, now time.Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = util.FilterSliceInPlace(wp, func(wp av.Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.FlightPlan.Route = exitRoute.SID + " " + dep.Route
	} else {
		ac.FlightPlan.Route = dep.Route
	}

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Exit = dep.Exit

	r := rand.Make()
	idx := rand.SampleFiltered(r, dep.Altitudes, func(alt int) bool { return alt <= int(perf.Ceiling) })
	if idx == -1 {
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, r)
	} else {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	}

	ac.TypeOfFlight = av.FlightTypeDeparture

	randomizeAltitudeRange := ac.FlightPlan.Rules == av.FlightRulesVFR
	nav := MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, exitRoute.SpeedRestriction, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeVFRDeparture(ap *av.Airport, wps av.WaypointArray,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, wind av.WindModel,
	lg *log.Logger) error {
	wp := util.DuplicateSlice(wps)

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.TypeOfFlight = av.FlightTypeDeparture

	nav := MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, 0, /* assigned alt */
		ac.FlightPlan.Altitude /* cleared alt */, 0 /* speed restriction */, wp,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav
	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeOverflight(of *av.Overflight, nmPerLongitude float32,
	magneticVariation float32, wind av.WindModel, now time.Time, lg *log.Logger) error {
	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(of.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, rand.Make())
	}
	ac.FlightPlan.Route = of.Waypoints.RouteString()
	ac.TypeOfFlight = av.FlightTypeOverflight

	nav := MakeOverflightNav(ac.ADSBCallsign, of, ac.FlightPlan, perf, nmPerLongitude,
		magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	return nil
}

func (ac *Aircraft) NavSummary(lg *log.Logger) string {
	return ac.Nav.Summary(ac.FlightPlan, lg)
}

func (ac *Aircraft) ContactMessage(reportingPoints []av.ReportingPoint) *speech.RadioTransmission {
	return ac.Nav.ContactMessage(reportingPoints, ac.STAR)
}

func (ac *Aircraft) DepartOnCourse(lg *log.Logger) {
	if ac.FlightPlan.Exit == "" {
		lg.Warn("unset \"exit\" for departure", slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}
	ac.Nav.DepartOnCourse(float32(ac.FlightPlan.Altitude), ac.FlightPlan.Exit)
}

func (ac *Aircraft) Check(lg *log.Logger) {
	ac.Nav.Check(lg)
}

func (ac *Aircraft) Position() math.Point2LL {
	return ac.Nav.FlightState.Position
}

func (ac *Aircraft) Altitude() float32 {
	return ac.Nav.FlightState.Altitude
}

func (ac *Aircraft) Heading() float32 {
	return ac.Nav.FlightState.Heading
}

func (ac *Aircraft) NmPerLongitude() float32 {
	return ac.Nav.FlightState.NmPerLongitude
}

func (ac *Aircraft) MagneticVariation() float32 {
	return ac.Nav.FlightState.MagneticVariation
}

func (ac *Aircraft) IsAirborne() bool {
	return ac.Nav.IsAirborne()
}

func (ac *Aircraft) IAS() float32 {
	return ac.Nav.FlightState.IAS
}

func (ac *Aircraft) GS() float32 {
	return ac.Nav.FlightState.GS
}

func (ac *Aircraft) OnApproach(checkAltitude bool) bool {
	return ac.Nav.OnApproach(checkAltitude)
}

func (ac *Aircraft) OnExtendedCenterline(maxNmDeviation float32) bool {
	return ac.Nav.OnExtendedCenterline(maxNmDeviation)
}

func (ac *Aircraft) DepartureAirportElevation() float32 {
	return ac.Nav.FlightState.DepartureAirportElevation
}

func (ac *Aircraft) ArrivalAirportElevation() float32 {
	return ac.Nav.FlightState.ArrivalAirportElevation
}

func (ac *Aircraft) DepartureAirportLocation() math.Point2LL {
	return ac.Nav.FlightState.DepartureAirportLocation
}

func (ac *Aircraft) ArrivalAirportLocation() math.Point2LL {
	return ac.Nav.FlightState.ArrivalAirportLocation
}

func (ac *Aircraft) ATPAVolume() *av.ATPAVolume {
	return ac.Nav.Approach.ATPAVolume
}

func (ac *Aircraft) MVAsApply() bool {
	return !ac.OnApproach(true)
}

func (ac *Aircraft) AircraftPerformance() av.AircraftPerformance {
	return ac.Nav.Perf
}

func (ac *Aircraft) RouteIncludesFix(fix string) bool {
	return slices.ContainsFunc(ac.Nav.Waypoints, func(w av.Waypoint) bool { return w.Fix == fix })
}

func (ac *Aircraft) DistanceToEndOfApproach() (float32, error) {
	return ac.Nav.distanceToEndOfApproach()
}

func (ac *Aircraft) Waypoints() []av.Waypoint {
	return ac.Nav.Waypoints
}

func (ac *Aircraft) DistanceAlongRoute(fix string) (float32, error) {
	return ac.Nav.DistanceAlongRoute(fix)
}

func (ac *Aircraft) CWT() string {
	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return "NOWGT"
	}
	cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
	if !slices.Contains(cwt, perf.Category.CWT) {
		return "NOWGT"
	}
	return perf.Category.CWT
}

func PlausibleFinalAltitude(fp av.FlightPlan, perf av.AircraftPerformance, nmPerLongitude float32, magneticVariation float32,
	r *rand.Rand) int {
	// try to figure out direction of flight
	dep, dok := av.DB.Airports[fp.DepartureAirport]
	arr, aok := av.DB.Airports[fp.ArrivalAirport]
	if !dok || !aok {
		if fp.Rules == av.FlightRulesIFR {
			return 34000
		} else {
			return 12500
		}
	}

	// Pick a base altitude in thousands and a sampling delta. Odd altitude
	// for now; we deal with direction of flight later.
	pDep, pArr := dep.Location, arr.Location
	alt, delta := 0, 2
	if math.NMDistance2LL(pDep, pArr) < 50 {
		alt = 5
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 100 {
		alt = 9
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 200 {
		alt = 15
		delta = 2
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 300 {
		alt = 21
		delta = 2
	} else {
		alt = 35
		delta = 3

	}

	// Randomize the altitude a bit
	alt = (alt - delta + r.Intn(2*delta+1))

	// Round ceiling down to odd 1000s.
	ceiling := int(perf.Ceiling) / 1000
	if ceiling%2 == 0 {
		ceiling--
	}

	// Enforce ceiling
	alt = min(alt, ceiling)

	if math.Heading2LL(pDep, pArr, nmPerLongitude, magneticVariation) > 180 {
		// Decrease rather than increasing so that we don't potentially go
		// above the aircraft's ceiling.
		alt--
	}

	altitude := alt * 1000

	if fp.Rules == av.FlightRulesVFR {
		altitude += 500
	}

	return altitude
}

func (ac *Aircraft) IsDeparture() bool {
	return ac.TypeOfFlight == av.FlightTypeDeparture
}

func (ac *Aircraft) IsArrival() bool {
	return ac.TypeOfFlight == av.FlightTypeArrival
}

func (ac *Aircraft) IsOverflight() bool {
	return ac.TypeOfFlight == av.FlightTypeOverflight
}

func (ac *Aircraft) WillDoAirwork() bool {
	return ac.Nav.Airwork != nil ||
		slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.AirworkRadius > 0 })
}

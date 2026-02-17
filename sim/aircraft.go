// sim/aircraft.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// CallsignAddressingForm indicates how a controller addressed an aircraft's callsign.
type CallsignAddressingForm int

const (
	// AddressingFormFull is the full callsign form (e.g., "november 1 2 3 alpha bravo")
	AddressingFormFull CallsignAddressingForm = iota
	// AddressingFormTypeTrailing3 is the aircraft type + trailing 3 form (e.g., "skyhawk 3 alpha bravo")
	AddressingFormTypeTrailing3
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
	// For departures, after we first see them in the departure acquisition
	// volume, we set a time a bit in the future for the flight plan to
	// actually acquire to simulate the delay in that.
	DepartureFPAcquisitionTime time.Time

	// State related to navigation.
	Nav nav.Nav

	// Departure-related state
	SID string

	// Arrival-related state
	STAR                string
	STARRunwayWaypoints map[string]av.WaypointArray
	GotContactTower     bool

	NASFlightPlan *NASFlightPlan

	// ControllerFrequency is the controller position whose radio frequency
	// this aircraft is tuned to. Only this controller can issue ATC commands
	// to the aircraft. Empty means the aircraft is not on any controller's
	// frequency.
	ControllerFrequency ControlPosition

	HoldForRelease    bool
	Released          bool // only used for hold for release
	ReleaseTime       time.Time
	WaitingForLaunch  bool // for departures
	MissingFlightPlan bool

	GoAroundDistance *float32

	// Set when tower sends aircraft around for spacing; affects the contact message.
	SentAroundForSpacing bool
	// Set when a spacing check rolled "no go-around"; prevents re-rolling every tick.
	SpacingGoAroundDeclined bool
	// Set when going around on runway heading (vs a specific assigned heading).
	GoAroundOnRunwayHeading bool
	// Set when the aircraft has gone around; prevents the arrival drop
	// filter from dropping its flight plan.
	WentAround bool

	// Departure related state
	DepartureContactAltitude float32 // 0 = waiting for /tc point, -1 = already contacted departure
	ReportDepartureHeading   bool    // true if runway has multiple exit heading

	// The controller who gave approach clearance
	ApproachTCP TCP

	FirstSeen time.Time

	RequestedFlightFollowing bool
	// WaitingForGoAhead is set when a VFR aircraft has made an abbreviated
	// flight following request ("approach, N123AB, VFR request") and is
	// waiting for the controller to say "go ahead".
	WaitingForGoAhead bool

	EmergencyState *EmergencyState

	LastRadioTransmission time.Time

	// LastAddressingForm tracks how the controller last addressed this aircraft.
	// Used for readbacks to match the controller's style.
	LastAddressingForm CallsignAddressingForm

	// ATIS letter the aircraft reported during initial contact (e.g., "B").
	// Empty if the pilot did not report having ATIS.
	ReportedATIS string

	// Traffic advisory state
	TrafficInSight      bool      // True if aircraft has reported traffic in sight
	TrafficInSightTime  time.Time // When traffic was reported in sight
	TrafficLookingUntil time.Time // If non-zero, aircraft may report traffic in sight before this time

	// RequestedVisual is set when the pilot has spontaneously requested
	// the visual approach (field in sight). Prevents repeated requests.
	RequestedVisual bool
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

// GetSTTFixes returns the raw fix names relevant for STT context.
// This includes assigned waypoints within 75nm and approach waypoints if applicable.
func (ac *Aircraft) GetSTTFixes() []string {
	var fixes []string
	p := ac.Nav.FlightState.Position

	isValidFix := func(fix string) bool {
		return len(fix) >= 3 && len(fix) <= 5 && fix[0] != '_'
	}

	for _, wp := range ac.Nav.AssignedWaypoints() {
		if math.NMDistance2LL(p, wp.Location) > 75 && len(fixes) > 0 {
			break
		}
		if isValidFix(wp.Fix) {
			fixes = append(fixes, wp.Fix)
		}
	}

	if ac.Nav.Approach.Assigned != nil {
		// Check if approach waypoints are already in the route
		hasApproachWaypoints := slices.ContainsFunc(ac.Nav.AssignedWaypoints(),
			func(wp av.Waypoint) bool { return wp.OnApproach() })

		// If not, add all approach waypoints (aircraft is being vectored to intercept)
		if !hasApproachWaypoints {
			for _, wps := range ac.Nav.Approach.Assigned.Waypoints {
				for _, wp := range wps {
					if isValidFix(wp.Fix) {
						fixes = append(fixes, wp.Fix)
					}
				}
			}
		}
	}

	return fixes
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

func (ac *Aircraft) TAS(temp float32) float32 {
	return ac.Nav.TAS(temp)
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

func (ac *Aircraft) Update(model *wx.Model, simTime time.Time, bravo *av.AirspaceGrid, lg *log.Logger) *av.Waypoint {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	passedWaypoint := ac.Nav.Update(string(ac.ADSBCallsign), model, &ac.FlightPlan, simTime, bravo)
	if passedWaypoint != nil {
		lg.Debug("passed", slog.Any("waypoint", passedWaypoint))
	}

	return passedWaypoint
}

func (ac *Aircraft) PilotMixUp() av.CommandIntent {
	return av.MixUpIntent{
		Callsign:    ac.ADSBCallsign,
		IsEmergency: ac.EmergencyState != nil,
	}
}

func (ac *Aircraft) Ident(now time.Time) av.CommandIntent {
	ac.IdentStartTime = now.Add(time.Duration(2+ac.Nav.Rand.Intn(3)) * time.Second) // delay the start a bit
	ac.IdentEndTime = ac.IdentStartTime.Add(10 * time.Second)
	return av.TransponderIntent{Ident: true}
}

func (ac *Aircraft) AssignAltitude(altitude int, afterSpeed bool) av.CommandIntent {
	return ac.Nav.AssignAltitude(float32(altitude), afterSpeed)
}

func (ac *Aircraft) AssignMach(mach float32, afterAltitude bool, temp float32) av.CommandIntent {
	return ac.Nav.AssignMach(mach, afterAltitude, temp)
}

func (ac *Aircraft) AssignSpeed(speed int, afterAltitude bool) av.CommandIntent {
	return ac.Nav.AssignSpeed(float32(speed), afterAltitude)
}

func (ac *Aircraft) AssignSpeedUntil(speed int, until *av.SpeedUntil) av.CommandIntent {
	return ac.Nav.AssignSpeedUntil(float32(speed), until)
}

func (ac *Aircraft) MaintainSlowestPractical() av.CommandIntent {
	return ac.Nav.MaintainSlowestPractical()
}

func (ac *Aircraft) MaintainMaximumForward() av.CommandIntent {
	return ac.Nav.MaintainMaximumForward()
}

func (ac *Aircraft) MaintainPresentSpeed() av.CommandIntent {
	return ac.Nav.MaintainPresentSpeed()
}

func (ac *Aircraft) SaySpeed(tempKelvin float32) av.CommandIntent {
	return ac.Nav.SaySpeed(tempKelvin)
}

func (ac *Aircraft) SayIndicatedSpeed() av.CommandIntent {
	return ac.Nav.SayIndicatedSpeed()
}

func (ac *Aircraft) SayMach(tempKelvin float32) av.CommandIntent {
	return ac.Nav.SayMach(tempKelvin)
}

func (ac *Aircraft) SayHeading() av.CommandIntent {
	return ac.Nav.SayHeading()
}

func (ac *Aircraft) SayAltitude() av.CommandIntent {
	return ac.Nav.SayAltitude()
}

func (ac *Aircraft) ExpediteDescent() av.CommandIntent {
	return ac.Nav.ExpediteDescent()
}

func (ac *Aircraft) ExpediteClimb() av.CommandIntent {
	return ac.Nav.ExpediteClimb()
}

func (ac *Aircraft) AssignHeading(heading int, turn av.TurnDirection, simTime time.Time) av.CommandIntent {
	return ac.Nav.AssignHeading(float32(heading), turn, simTime)
}

func (ac *Aircraft) TurnLeft(deg int, simTime time.Time) av.CommandIntent {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading - float32(deg))
	ac.Nav.AssignHeading(hdg, av.TurnLeft, simTime)
	return av.HeadingIntent{
		Type:    av.HeadingTurnLeft,
		Heading: hdg,
		Degrees: deg,
	}
}

func (ac *Aircraft) TurnRight(deg int, simTime time.Time) av.CommandIntent {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading + float32(deg))
	ac.Nav.AssignHeading(hdg, av.TurnRight, simTime)
	return av.HeadingIntent{
		Type:    av.HeadingTurnRight,
		Heading: hdg,
		Degrees: deg,
	}
}

func (ac *Aircraft) FlyPresentHeading(simTime time.Time) av.CommandIntent {
	return ac.Nav.FlyPresentHeading(simTime)
}

func (ac *Aircraft) DirectFix(fix string, simTime time.Time) av.CommandIntent {
	return ac.Nav.DirectFix(strings.ToUpper(fix), simTime)
}

func (ac *Aircraft) HoldAtFix(fix string, hold *av.Hold) av.CommandIntent {
	return ac.Nav.HoldAtFix(string(ac.ADSBCallsign), strings.ToUpper(fix), hold)
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) av.CommandIntent {
	return ac.Nav.DepartFixHeading(strings.ToUpper(fix), float32(hdg))
}

func (ac *Aircraft) DepartFixDirect(fixa, fixb string) av.CommandIntent {
	return ac.Nav.DepartFixDirect(strings.ToUpper(fixa), strings.ToUpper(fixb))
}

func (ac *Aircraft) CrossFixAt(fix string, ar *av.AltitudeRestriction, speed int, mach float32) av.CommandIntent {
	return ac.Nav.CrossFixAt(strings.ToUpper(fix), ar, speed, mach)
}

func (ac *Aircraft) ExpectApproach(id string, ap *av.Airport, lahsoRunway string, lg *log.Logger) av.CommandIntent {
	return ac.Nav.ExpectApproach(ap, id, ac.STARRunwayWaypoints, lahsoRunway, lg)
}

func (ac *Aircraft) AtFixCleared(fix, approach string) av.CommandIntent {
	return ac.Nav.AtFixCleared(fix, approach)
}

func (ac *Aircraft) AtFixIntercept(fix string, lg *log.Logger) av.CommandIntent {
	return ac.Nav.AtFixIntercept(fix, ac.FlightPlan.ArrivalAirport, lg)
}

func (ac *Aircraft) ClearedApproach(id string, simTime time.Time, lg *log.Logger) (av.CommandIntent, bool) {
	return ac.Nav.ClearedApproach(ac.FlightPlan.ArrivalAirport, id, false, simTime)
}

func (ac *Aircraft) ClearedDirectVisual(runway string, simTime time.Time) (av.CommandIntent, bool) {
	return ac.Nav.ClearedDirectVisual(runway, simTime)
}

func (ac *Aircraft) ClearedStraightInApproach(id string, simTime time.Time, lg *log.Logger) (av.CommandIntent, bool) {
	return ac.Nav.ClearedApproach(ac.FlightPlan.ArrivalAirport, id, true, simTime)
}

func (ac *Aircraft) CancelApproachClearance() av.CommandIntent {
	return ac.Nav.CancelApproachClearance()
}

func (ac *Aircraft) ClimbViaSID(simTime time.Time) av.CommandIntent {
	return ac.Nav.ClimbViaSID(simTime)
}

func (ac *Aircraft) DescendViaSTAR(simTime time.Time) av.CommandIntent {
	return ac.Nav.DescendViaSTAR(simTime)
}

func (ac *Aircraft) ResumeOwnNavigation() av.CommandIntent {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return av.MakeUnableIntent("unable. We're IFR")
	} else {
		return ac.Nav.ResumeOwnNavigation()
	}
}

func (ac *Aircraft) AltitudeOurDiscretion() av.CommandIntent {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return av.MakeUnableIntent("unable. We're IFR")
	} else {
		return ac.Nav.AltitudeOurDiscretion()
	}
}

func (ac *Aircraft) ContactTower(lg *log.Logger) (av.CommandIntent, bool) {
	if ac.GotContactTower {
		// No response; they're not on our frequency any more.
		return nil, false
	} else if ac.Nav.Approach.Assigned == nil {
		return av.MakeUnableIntent("unable. We haven't been given an approach."), false
	} else if !ac.Nav.Approach.Cleared {
		return av.MakeUnableIntent("unable. We haven't been cleared for the approach."), false
	} else {
		ac.GotContactTower = true
		return av.ContactTowerIntent{}, true
	}
}

func (ac *Aircraft) InterceptApproach(lg *log.Logger) av.CommandIntent {
	return ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport, lg)
}

func (ac *Aircraft) InitializeArrival(ap *av.Airport, arr *av.Arrival, nmPerLongitude float32, magneticVariation float32,
	model *wx.Model, simTime time.Time, lg *log.Logger) error {
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

	nav := nav.MakeArrivalNav(ac.ADSBCallsign, arr, ac.FlightPlan, perf, nmPerLongitude, magneticVariation, model,
		simTime, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if arr.ExpectApproach.A != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
		ac.ExpectApproach(*arr.ExpectApproach.A, ap, "", lg)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
			ac.ExpectApproach(app, ap, "", lg)
		}
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(ap *av.Airport, departureAirport string, dep *av.Departure,
	runway string, exitRoute av.ExitRoute, nmPerLongitude float32, magneticVariation float32,
	model *wx.Model, simTime time.Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = util.FilterSliceInPlace(wp, func(wp av.Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.SID = exitRoute.SID
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
	ac.FlightPlan.DepartureRunway = runway

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
	nav := nav.MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, exitRoute.SpeedRestriction, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, model, simTime, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeVFRDeparture(ap *av.Airport, wps av.WaypointArray,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, model *wx.Model,
	simTime time.Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(wps)

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.TypeOfFlight = av.FlightTypeDeparture

	nav := nav.MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, 0, /* assigned alt */
		ac.FlightPlan.Altitude /* cleared alt */, 0 /* speed restriction */, wp,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, model, simTime, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav
	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeOverflight(of *av.Overflight, nmPerLongitude float32,
	magneticVariation float32, model *wx.Model, simTime time.Time, lg *log.Logger) error {
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

	nav := nav.MakeOverflightNav(ac.ADSBCallsign, of, ac.FlightPlan, perf, nmPerLongitude,
		magneticVariation, model, simTime, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	return nil
}

func (ac *Aircraft) NavSummary(model *wx.Model, simTime time.Time, lg *log.Logger) string {
	return ac.Nav.Summary(ac.FlightPlan, model, simTime, lg)
}

func (ac *Aircraft) ContactMessage(reportingPoints []av.ReportingPoint) *av.RadioTransmission {
	// For departures, only report heading if the runway has varied exit headings.
	// For arrivals (and others), always report heading if assigned.
	reportHeading := !ac.IsDeparture() || ac.ReportDepartureHeading
	return ac.Nav.ContactMessage(reportingPoints, ac.STAR, reportHeading, ac.IsDeparture())
}

func (ac *Aircraft) DepartOnCourse(simTime time.Time, lg *log.Logger) {
	if ac.FlightPlan.Exit == "" {
		lg.Warn(`unset "exit" for departure`, slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}
	ac.Nav.DepartOnCourse(float32(ac.FlightPlan.Altitude), string(ac.FlightPlan.Exit), simTime)
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
	return ac.Nav.DistanceToEndOfApproach()
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

	if fp.Rules == av.FlightRulesVFR {
		alt = min(alt, 17) // VFRs stay out of class A airspace
	}

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
		slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.AirworkRadius() > 0 })
}

func (ac *Aircraft) IsUnassociated() bool {
	return ac.NASFlightPlan == nil
}

func (ac *Aircraft) IsAssociated() bool {
	return ac.NASFlightPlan != nil
}

func (ac *Aircraft) AssociateFlightPlan(fp *NASFlightPlan) {
	fp.Location = math.Point2LL{} // clear location in case it was an unsupported DB
	ac.NASFlightPlan = fp
}

func (ac *Aircraft) DisassociateFlightPlan() *NASFlightPlan {
	fp := ac.NASFlightPlan
	ac.NASFlightPlan = nil
	return fp
}

func (ac *Aircraft) DivertToAirport(ap string) {
	ac.FlightPlan.ArrivalAirport = ap
	ac.TypeOfFlight = av.FlightTypeArrival

	ac.Nav.DivertToAirport(ap)
}

///////////////////////////////////////////////////////////////////////////
// VoiceAssigner

// AirlineVoices maps airline ICAO codes (or comma-separated codes) to voice names.
// "default" is used for callsigns that don't match any specific airline.
// Edit this map to customize which voices are used for each airline.
var AirlineVoices = map[string][]string{
	"default": {
		"af_alloy", "af_aoede", "af_bella", "af_heart", "af_nova", "af_kore",
		"af_river", "af_sarah", "af_sky", "am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
		"am_michael", "am_onyx", "am_puck",
	},
	"BAW,VIR,DLH,KQA": {
		"bf_alice", "bf_emma", "bf_isabella", "bf_lily",
		"bm_daniel", "bm_fable", "bm_george", "bm_lewis",
	},
	"AMX,IBE": {
		"ef_dora", "em_alex",
	},
	"AFR": {
		"ff_siwis",
	},
	"ITY,LOT": {
		"if_sara", "im_nicola",
	},
	"TAP,TAM,ELY": {
		"pf_dora", "pm_alex", "pm_santa",
	},
	"AIC": {
		"hf_alpha", "hf_beta", "hm_omega", "hm_psi",
	},
	"JAL,ANA,KAL": {
		"jf_alpha", "jf_gongitsune", "jf_nezumi", "jf_tebukuro", "jm_kumo",
	},
	"CAL,CCA,CES,CSN,CXA,SIA": {
		"zf_xiaobei", "zf_xiaoni", "zf_xiaoxiao", "zm_yunjian", "zm_yunxi", "zm_yunxia", "zm_yunyang",
	},
}

// VoiceAssigner manages the pool of available TTS voice names and assigns them
// to aircraft callsigns. Each aircraft gets a consistent voice throughout
// the session.
type VoiceAssigner struct {
	// Same keys as AirlineVoices, shuffled and consumed FIFO.
	VoicePools map[string][]string
	// Callsign -> voice name mapping
	AircraftVoices map[av.ADSBCallsign]string
}

// NewVoiceAssigner creates a new VoiceAssigner with airline-based voice pools.
func NewVoiceAssigner(r *rand.Rand) *VoiceAssigner {
	va := &VoiceAssigner{
		VoicePools:     maps.Clone(AirlineVoices),
		AircraftVoices: make(map[av.ADSBCallsign]string),
	}

	for voices := range maps.Values(va.VoicePools) {
		rand.ShuffleSlice(voices, r)
	}

	return va
}

// GetVoice returns the voice name assigned to an aircraft, assigning one if needed.
func (va *VoiceAssigner) GetVoice(callsign av.ADSBCallsign, r *rand.Rand) string {
	// Check if already assigned
	if voiceName, ok := va.AircraftVoices[callsign]; ok {
		return voiceName
	}

	getVoice := func(callsigns string) string {
		voices := va.VoicePools[callsigns]
		if len(voices) == 0 {
			voices = slices.Clone(AirlineVoices[callsigns])
			rand.ShuffleSlice(voices, r)
		}

		voice := voices[0]
		va.VoicePools[callsigns] = voices[1:]
		va.AircraftVoices[callsign] = voice
		return voice
	}

	if len(callsign) > 3 {
		icao := string(callsign[:3])
		for callsigns := range va.VoicePools {
			if util.SeqContains(strings.SplitSeq(callsigns, ","), icao) {
				return getVoice(callsigns)
			}
		}
	}

	return getVoice("default")
}

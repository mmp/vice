// pkg/aviation/aircraft.go
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

type RadarTrack struct {
	ADSBCallsign ADSBCallsign
	Squawk       Squawk
	Mode         TransponderMode
	Ident        bool
	Altitude     float32
	Location     math.Point2LL
	Heading      float32
	Groundspeed  float32
}

func (ac *Aircraft) GetRadarTrack(now time.Time) RadarTrack {
	return RadarTrack{
		ADSBCallsign: ac.ADSBCallsign,
		Squawk:       util.Select(ac.Mode != TransponderModeStandby, ac.Squawk, Squawk(0)),
		Mode:         ac.Mode,
		Ident:        ac.Mode != TransponderModeStandby && now.After(ac.IdentStartTime) && now.Before(ac.IdentEndTime),
		Altitude:     util.Select(ac.Mode == TransponderModeAltitude, ac.Altitude(), 0),
		Location:     ac.Position(),
		Heading:      ac.Heading(),
		Groundspeed:  ac.GS(),
	}
}

type Aircraft struct {
	// This is ADS-B callsign of the aircraft. Just because different the
	// callsign in the flight plan can be different across multiple STARS
	// facilities, so two different facilities can show different
	// callsigns; however, the ADS-B callsign is transmitted from the
	// aircraft and would be the same to all facilities.
	ADSBCallsign ADSBCallsign

	Squawk Squawk
	Mode   TransponderMode

	IdentStartTime, IdentEndTime time.Time

	FlightPlan   FlightPlan
	TypeOfFlight TypeOfFlight

	Strip FlightStrip

	// State related to navigation.
	Nav Nav

	// Arrival-related state
	STAR                string
	STARRunwayWaypoints map[string]WaypointArray
	GotContactTower     bool
}

type ADSBCallsign string

func (c ADSBCallsign) String() string { return string(c) }

type PilotResponse struct {
	Message    string
	Unexpected bool // should it be highlighted in the UI
}

///////////////////////////////////////////////////////////////////////////
// Aircraft

func (ac *Aircraft) InitializeFlightPlan(r FlightRules, acType, dep, arr string) {
	ac.FlightPlan = FlightPlan{
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

// Helper function to make the code for the common case of a readback
// response more compact.
func (ac *Aircraft) readback(f string, args ...interface{}) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Message: fmt.Sprintf(f, args...),
		Type:    RadioTransmissionReadback,
	}}
}

func (ac *Aircraft) readbackUnexpected(f string, args ...interface{}) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Message: fmt.Sprintf(f, args...),
		Type:    RadioTransmissionUnexpected,
	}}
}

func (ac *Aircraft) transmitResponse(r PilotResponse) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Message: r.Message,
		Type:    RadioTransmissionType(util.Select(r.Unexpected, RadioTransmissionUnexpected, RadioTransmissionReadback)),
	}}
}

func (ac *Aircraft) Update(wind WindModel, lg *log.Logger) *Waypoint {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	passedWaypoint := ac.Nav.Update(wind, &ac.FlightPlan, lg)
	if passedWaypoint != nil {
		lg.Info("passed", slog.Any("waypoint", passedWaypoint))
	}

	return passedWaypoint
}

func (ac *Aircraft) GoAround() []RadioTransmission {
	resp := ac.Nav.GoAround()
	ac.GotContactTower = false

	return []RadioTransmission{RadioTransmission{
		Message: resp.Message,
		Type:    RadioTransmissionType(util.Select(resp.Unexpected, RadioTransmissionUnexpected, RadioTransmissionContact)),
	}}
}

func (ac *Aircraft) Ident(now time.Time) []RadioTransmission {
	ac.IdentStartTime = now.Add(time.Duration(2+rand.Intn(3)) * time.Second) // delay the start a bit
	ac.IdentEndTime = ac.IdentStartTime.Add(10 * time.Second)

	return []RadioTransmission{RadioTransmission{
		Message: "ident",
		Type:    RadioTransmissionReadback,
	}}
}

func (ac *Aircraft) AssignAltitude(altitude int, afterSpeed bool) []RadioTransmission {
	response := ac.Nav.AssignAltitude(float32(altitude), afterSpeed)
	return ac.transmitResponse(response)
}

func (ac *Aircraft) AssignSpeed(speed int, afterAltitude bool) []RadioTransmission {
	resp := ac.Nav.AssignSpeed(float32(speed), afterAltitude)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) MaintainSlowestPractical() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.MaintainSlowestPractical())
}

func (ac *Aircraft) MaintainMaximumForward() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.MaintainMaximumForward())
}

func (ac *Aircraft) SaySpeed() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.SaySpeed())
}

func (ac *Aircraft) SayHeading() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.SayHeading())
}

func (ac *Aircraft) SayAltitude() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.SayAltitude())
}

func (ac *Aircraft) ExpediteDescent() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.ExpediteDescent())
}

func (ac *Aircraft) ExpediteClimb() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.ExpediteClimb())
}

func (ac *Aircraft) AssignHeading(heading int, turn TurnMethod) []RadioTransmission {
	resp := ac.Nav.AssignHeading(float32(heading), turn)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) TurnLeft(deg int) []RadioTransmission {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading - float32(deg))
	ac.Nav.AssignHeading(hdg, TurnLeft)
	return ac.readback(rand.Sample("turn %d degrees left", "%d to the left"), deg)
}

func (ac *Aircraft) TurnRight(deg int) []RadioTransmission {
	hdg := math.NormalizeHeading(ac.Nav.FlightState.Heading + float32(deg))
	ac.Nav.AssignHeading(hdg, TurnRight)
	return ac.readback(rand.Sample("turn %d degrees right", "%d to the right"), deg)
}

func (ac *Aircraft) FlyPresentHeading() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.FlyPresentHeading())
}

func (ac *Aircraft) DirectFix(fix string) []RadioTransmission {
	return ac.transmitResponse(ac.Nav.DirectFix(strings.ToUpper(fix)))
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) []RadioTransmission {
	resp := ac.Nav.DepartFixHeading(strings.ToUpper(fix), float32(hdg))
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) DepartFixDirect(fixa, fixb string) []RadioTransmission {
	resp := ac.Nav.DepartFixDirect(strings.ToUpper(fixa), strings.ToUpper(fixb))
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) CrossFixAt(fix string, ar *AltitudeRestriction, speed int) []RadioTransmission {
	resp := ac.Nav.CrossFixAt(strings.ToUpper(fix), ar, speed)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) ExpectApproach(id string, ap *Airport, lg *log.Logger) []RadioTransmission {
	resp := ac.Nav.ExpectApproach(ap, id, ac.STARRunwayWaypoints, lg)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) AssignedApproach() string {
	return ac.Nav.Approach.AssignedId
}

func (ac *Aircraft) AtFixCleared(fix, approach string) []RadioTransmission {
	return ac.transmitResponse(ac.Nav.AtFixCleared(fix, approach))
}

func (ac *Aircraft) ClearedApproach(id string, lg *log.Logger) ([]RadioTransmission, error) {
	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, false)
	return ac.transmitResponse(resp), err
}

func (ac *Aircraft) ClearedStraightInApproach(id string) ([]RadioTransmission, error) {
	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true)
	return ac.transmitResponse(resp), err
}

func (ac *Aircraft) CancelApproachClearance() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.CancelApproachClearance())
}

func (ac *Aircraft) ClimbViaSID() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.ClimbViaSID())
}

func (ac *Aircraft) DescendViaSTAR() []RadioTransmission {
	return ac.transmitResponse(ac.Nav.DescendViaSTAR())
}

func (ac *Aircraft) ContactTower(lg *log.Logger) []RadioTransmission {
	if ac.GotContactTower {
		// No response; they're not on our frequency any more.
		return nil
	} else if ac.Nav.Approach.Assigned == nil {
		return ac.readbackUnexpected("unable. We haven't been given an approach.")
	} else if !ac.Nav.Approach.Cleared {
		return ac.readbackUnexpected("unable. We haven't been cleared for the approach.")
	} else {
		ac.GotContactTower = true
		return []RadioTransmission{RadioTransmission{
			Message: "contact tower",
			Type:    RadioTransmissionReadback,
		}}
	}
}

func (ac *Aircraft) InterceptApproach() []RadioTransmission {
	resp := ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) InitializeArrival(ap *Airport, arr *Arrival, nmPerLongitude float32, magneticVariation float32,
	wind WindModel, now time.Time, lg *log.Logger) error {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	}
	if arr.Route != "" {
		ac.FlightPlan.Route = arr.Route
	} else {
		ac.FlightPlan.Route = "/. " + arr.STAR
	}
	ac.TypeOfFlight = FlightTypeArrival

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

func (ac *Aircraft) InitializeDeparture(ap *Airport, departureAirport string, dep *Departure,
	runway string, exitRoute ExitRoute, nmPerLongitude float32, magneticVariation float32,
	wind WindModel, now time.Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = util.FilterSliceInPlace(wp, func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.FlightPlan.Route = exitRoute.SID + " " + dep.Route
	} else {
		ac.FlightPlan.Route = dep.Route
	}

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Exit = dep.Exit

	idx := rand.SampleFiltered(dep.Altitudes, func(alt int) bool { return alt <= int(perf.Ceiling) })
	if idx == -1 {
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	} else {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	}

	ac.TypeOfFlight = FlightTypeDeparture

	randomizeAltitudeRange := ac.FlightPlan.Rules == FlightRulesVFR
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

func (ac *Aircraft) InitializeVFRDeparture(ap *Airport, wps WaypointArray, alt int,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, wind WindModel,
	lg *log.Logger) error {
	wp := util.DuplicateSlice(wps)

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = math.Min(alt, int(perf.Ceiling))
	ac.TypeOfFlight = FlightTypeDeparture

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

func (ac *Aircraft) InitializeOverflight(of *Overflight, nmPerLongitude float32,
	magneticVariation float32, wind WindModel, now time.Time, lg *log.Logger) error {
	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(of.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	}
	ac.FlightPlan.Route = of.Waypoints.RouteString()
	ac.TypeOfFlight = FlightTypeOverflight

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

func (ac *Aircraft) ContactMessage(reportingPoints []ReportingPoint) string {
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

func (ac *Aircraft) ATPAVolume() *ATPAVolume {
	return ac.Nav.Approach.ATPAVolume
}

func (ac *Aircraft) MVAsApply() bool {
	// Start issuing MVAs 5 miles from the departure airport but not if
	// they're established on an approach.
	// TODO: are there better criteria?
	return math.NMDistance2LL(ac.Position(), ac.Nav.FlightState.DepartureAirportLocation) > 5 &&
		!ac.OnApproach(true)
}

func (ac *Aircraft) AircraftPerformance() AircraftPerformance {
	return ac.Nav.Perf
}

func (ac *Aircraft) RouteIncludesFix(fix string) bool {
	return slices.ContainsFunc(ac.Nav.Waypoints, func(w Waypoint) bool { return w.Fix == fix })
}

func (ac *Aircraft) DistanceToEndOfApproach() (float32, error) {
	return ac.Nav.distanceToEndOfApproach()
}

func (ac *Aircraft) Waypoints() []Waypoint {
	return ac.Nav.Waypoints
}

func (ac *Aircraft) DistanceAlongRoute(fix string) (float32, error) {
	return ac.Nav.DistanceAlongRoute(fix)
}

func (ac *Aircraft) CWT() string {
	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return "NOWGT"
	}
	cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
	if !slices.Contains(cwt, perf.Category.CWT) {
		return "NOWGT"
	}
	return perf.Category.CWT
}

func PlausibleFinalAltitude(fp FlightPlan, perf AircraftPerformance, nmPerLongitude float32,
	magneticVariation float32) (altitude int) {
	// try to figure out direction of flight
	dep, dok := DB.Airports[fp.DepartureAirport]
	arr, aok := DB.Airports[fp.ArrivalAirport]
	if !dok || !aok {
		return 34000
	}

	pDep, pArr := dep.Location, arr.Location
	if math.NMDistance2LL(pDep, pArr) < 100 {
		altitude = 7000
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			altitude += 1000
		}
	} else if math.NMDistance2LL(pDep, pArr) < 200 {
		altitude = 11000
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			altitude += 1000
		}
	} else if math.NMDistance2LL(pDep, pArr) < 300 {
		altitude = 21000
	} else {
		altitude = 37000
	}
	altitude = math.Min(altitude, int(perf.Ceiling))

	if math.Heading2LL(pDep, pArr, nmPerLongitude, magneticVariation) > 180 {
		// Decrease rather than increasing so that we don't potentially go
		// above the aircraft's ceiling.
		altitude -= 1000
	}

	return
}

func (ac *Aircraft) IsDeparture() bool {
	return ac.TypeOfFlight == FlightTypeDeparture
}

func (ac *Aircraft) IsArrival() bool {
	return ac.TypeOfFlight == FlightTypeArrival
}

func (ac *Aircraft) IsOverflight() bool {
	return ac.TypeOfFlight == FlightTypeOverflight
}

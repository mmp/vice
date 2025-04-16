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

type Aircraft struct {
	// This is ADS-B callsign of the aircraft. Just because different the
	// callsign in the flight plan can be different across multiple STARS
	// facilities, so two different facilities can show different
	// callsigns; however, the ADS-B callsign is transmitted from the
	// aircraft and would be the same to all facilities.
	Callsign string

	Scratchpad          string
	SecondaryScratchpad string
	Squawk              Squawk // actually squawking
	Mode                TransponderMode
	TempAltitude        int
	FlightPlan          *FlightPlan
	PointOutHistory     []string

	// STARS-related state that is globally visible
	TrackingController          string // Who has the radar track
	ControllingController       string // Who has control; not necessarily the same as TrackingController
	HandoffTrackController      string // Handoff offered but not yet accepted
	GlobalLeaderLineDirection   *math.CardinalOrdinalDirection
	RedirectedHandoff           RedirectedHandoff
	SPCOverride                 string
	PilotReportedAltitude       int
	InhibitModeCAltitudeDisplay bool

	HoldForRelease   bool
	Released         bool // only used for hold for release
	ReleaseTime      time.Time
	WaitingForLaunch bool // for departures

	// The controller who gave approach clearance
	ApproachController string

	Strip FlightStrip

	// State related to navigation. Pointers are used for optional values;
	// nil -> unset/unspecified.
	Nav Nav

	// Departure related state
	DepartureContactAltitude   float32
	DepartureContactController string

	// Arrival-related state
	GoAroundDistance    *float32
	STAR                string
	STARRunwayWaypoints map[string]WaypointArray
	GotContactTower     bool

	// Who to try to hand off to at a waypoint with /ho
	WaypointHandoffController string
}

type PilotResponse struct {
	Message    string
	Unexpected bool // should it be highlighted in the UI
}

///////////////////////////////////////////////////////////////////////////
// Aircraft

func (ac *Aircraft) NewFlightPlan(r FlightRules, acType, dep, arr string) *FlightPlan {
	return &FlightPlan{
		Callsign:         ac.Callsign,
		Rules:            r,
		AircraftType:     acType,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
		CruiseSpeed:      int(ac.AircraftPerformance().Speed.CruiseTAS),
		AssignedSquawk:   ac.Squawk,
		ECID:             "XXX", // TODO. (Mainly for FDIO and ERAM so not super high priority. )
	}
}

func (ac *Aircraft) TAS() float32 {
	return ac.Nav.TAS()
}

func (ac *Aircraft) HandleControllerDisconnect(callsign string, primaryController string) {
	if callsign == primaryController {
		// Don't change anything; the sim will pause without the primary
		// controller, so we might as well have all of the tracks and
		// inbound handoffs waiting for them when they return.
		return
	}

	if ac.HandoffTrackController == callsign {
		// Otherwise redirect handoffs to the primary controller. This is
		// not a perfect solution; for an arrival, for example, we should
		// re-resolve it based on the signed-in controllers, as is done in
		// Sim updateState() for arrivals when they are first handed
		// off. We don't have all of that information here, though...
		ac.HandoffTrackController = primaryController
	}

	if ac.ControllingController == callsign {
		if ac.TrackingController == callsign {
			// Drop track of aircraft that we control
			ac.TrackingController = ""
			ac.ControllingController = ""
		} else {
			// Another controller has the track but not yet control;
			// just give them control
			ac.ControllingController = ac.TrackingController
		}
	}
}

func (ac *Aircraft) TransferTracks(from, to string) {
	if ac.HandoffTrackController == from {
		ac.HandoffTrackController = to
	}
	if ac.TrackingController == from {
		ac.TrackingController = to
	}
	if ac.ControllingController == from {
		ac.ControllingController = to
	}
	if ac.ApproachController == from {
		ac.ApproachController = to
	}
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

// Helper function to make the code for the common case of a readback
// response more compact.
func (ac *Aircraft) readback(f string, args ...interface{}) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    fmt.Sprintf(f, args...),
		Type:       RadioTransmissionReadback,
	}}
}

func (ac *Aircraft) readbackUnexpected(f string, args ...interface{}) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    fmt.Sprintf(f, args...),
		Type:       RadioTransmissionUnexpected,
	}}
}

func (ac *Aircraft) transmitResponse(r PilotResponse) []RadioTransmission {
	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    r.Message,
		Type:       RadioTransmissionType(util.Select(r.Unexpected, RadioTransmissionUnexpected, RadioTransmissionReadback)),
	}}
}

func (ac *Aircraft) Update(wind WindModel, lg *log.Logger) *Waypoint {
	if lg != nil {
		lg = lg.With(slog.String("callsign", ac.Callsign))
	}

	passedWaypoint := ac.Nav.Update(wind, ac.FlightPlan, lg)
	if passedWaypoint != nil {
		lg.Info("passed", slog.Any("waypoint", passedWaypoint))

		if passedWaypoint.ClearApproach {
			ac.ApproachController = ac.ControllingController
		}
	}

	return passedWaypoint
}

func (ac *Aircraft) GoAround() []RadioTransmission {
	resp := ac.Nav.GoAround()
	ac.GotContactTower = false
	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    resp.Message,
		Type:       RadioTransmissionType(util.Select(resp.Unexpected, RadioTransmissionUnexpected, RadioTransmissionContact)),
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

func (ac *Aircraft) ClearedApproach(id string, lg *log.Logger) []RadioTransmission {
	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, false)
	if err == nil {
		ac.ApproachController = ac.ControllingController
	}
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) ClearedStraightInApproach(id string) []RadioTransmission {
	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true)
	if err == nil {
		ac.ApproachController = ac.ControllingController
	}
	return ac.transmitResponse(resp)
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
		prevController := ac.ControllingController
		ac.ControllingController = "_TOWER"

		return []RadioTransmission{RadioTransmission{
			Controller: prevController,
			Message:    "contact tower",
			Type:       RadioTransmissionReadback,
		}}
	}
}

func (ac *Aircraft) InterceptApproach() []RadioTransmission {
	resp := ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) InitializeArrival(ap *Airport, arr *Arrival, arrivalHandoffController string, goAround bool,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, lg *log.Logger) error {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]
	ac.Scratchpad = arr.Scratchpad
	ac.SecondaryScratchpad = arr.SecondaryScratchpad
	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	ac.WaypointHandoffController = arrivalHandoffController

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
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

	nav := MakeArrivalNav(ac.Callsign, arr, *ac.FlightPlan, perf, nmPerLongitude, magneticVariation,
		wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	// VFRs don't go around since they aren't talking to us.
	goAround = goAround && ac.FlightPlan.Rules == IFR
	// If it's only controlled by virtual controllers, then don't let it go
	// around.  Note that this test misses the case where a human has
	// control from the start, though that shouldn't be happening...
	goAround = goAround && slices.ContainsFunc(ac.Nav.Waypoints, func(wp Waypoint) bool { return wp.HumanHandoff })
	if goAround {
		// Don't go around
		d := 0.1 + .6*rand.Float32()
		ac.GoAroundDistance = &d
	}

	if arr.ExpectApproach.A != nil {
		lg = lg.With(slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
		ac.ExpectApproach(*arr.ExpectApproach.A, ap, lg)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			lg = lg.With(slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
			ac.ExpectApproach(app, ap, lg)
		}
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(ap *Airport, departureAirport string, dep *Departure,
	runway string, exitRoute ExitRoute, nmPerLongitude float32,
	magneticVariation float32, scratchpads map[string]string,
	primaryController string, multiControllers SplitConfiguration,
	wind WindModel, lg *log.Logger) error {
	wp := util.DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = util.FilterSliceInPlace(wp, func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.FlightPlan.Route = exitRoute.SID + " " + dep.Route
	} else {
		ac.FlightPlan.Route = dep.Route
	}

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.Scratchpad = dep.Scratchpad
	if ac.Scratchpad == "" {
		ac.Scratchpad = scratchpads[dep.Exit]
	}
	ac.SecondaryScratchpad = dep.SecondaryScratchpad
	ac.FlightPlan.Exit = dep.Exit

	idx := rand.SampleFiltered(dep.Altitudes, func(alt int) bool { return alt <= int(perf.Ceiling) })
	if idx == -1 {
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	} else {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	}

	ac.HoldForRelease = ap.HoldForRelease && ac.FlightPlan.Rules == IFR // VFRs aren't held

	randomizeAltitudeRange := ac.FlightPlan.Rules == VFR
	nav := MakeDepartureNav(ac.Callsign, *ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, exitRoute.SpeedRestriction, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if ap.DepartureController != "" && ap.DepartureController != primaryController {
		// starting out with a virtual controller
		ac.TrackingController = ap.DepartureController
		ac.ControllingController = ap.DepartureController
		ac.WaypointHandoffController = exitRoute.HandoffController
	} else {
		// human controller will be first
		ctrl := primaryController
		if len(multiControllers) > 0 {
			var err error
			ctrl, err = multiControllers.GetDepartureController(departureAirport, runway, exitRoute.SID)
			if err != nil {
				lg.Error("unable to get departure controller", slog.Any("error", err),
					slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
			}
		}
		if ctrl == "" {
			ctrl = primaryController
		}

		ac.DepartureContactAltitude =
			ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(rand.Intn(500))
		ac.DepartureContactAltitude = math.Min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))
		ac.DepartureContactController = ctrl
	}

	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeVFRDeparture(ap *Airport, wps WaypointArray, alt int,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, wind WindModel,
	lg *log.Logger) error {
	wp := util.DuplicateSlice(wps)

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = math.Min(alt, int(perf.Ceiling))

	nav := MakeDepartureNav(ac.Callsign, *ac.FlightPlan, perf, 0, /* assigned alt */
		ac.FlightPlan.Altitude /* cleared alt */, 0 /* speed restriction */, wp,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav
	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeOverflight(of *Overflight, controller string, nmPerLongitude float32,
	magneticVariation float32, wind WindModel, lg *log.Logger) error {
	ac.Scratchpad = of.Scratchpad
	ac.SecondaryScratchpad = of.SecondaryScratchpad
	ac.TrackingController = of.InitialController
	ac.ControllingController = of.InitialController
	ac.WaypointHandoffController = controller

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(of.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	}
	ac.FlightPlan.Route = of.Waypoints.RouteString()

	nav := MakeOverflightNav(ac.Callsign, of, *ac.FlightPlan, perf, nmPerLongitude,
		magneticVariation, wind, lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	return nil
}

func (ac *Aircraft) NavSummary(lg *log.Logger) string {
	return ac.Nav.Summary(*ac.FlightPlan, lg)
}

func (ac *Aircraft) ContactMessage(reportingPoints []ReportingPoint) string {
	return ac.Nav.ContactMessage(reportingPoints, ac.STAR)
}

func (ac *Aircraft) DepartOnCourse(lg *log.Logger) {
	if ac.FlightPlan.Exit == "" {
		lg.Warn("unset \"exit\" for departure", slog.String("callsign", ac.Callsign))
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

func (ac *Aircraft) ToggleSPCOverride(spc string) {
	if ac.SPCOverride == spc {
		ac.SPCOverride = ""
	} else {
		ac.SPCOverride = spc
	}
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
	perf, ok := DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		return "NOWGT"
	}
	cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
	if !slices.Contains(cwt, perf.Category.CWT) {
		return "NOWGT"
	}
	return perf.Category.CWT
}

func (ac *Aircraft) HandingOffTo(tcp string) bool {
	return (ac.HandoffTrackController == tcp &&
		!slices.Contains(ac.RedirectedHandoff.Redirector, tcp)) || // not a redirector
		ac.RedirectedHandoff.RedirectedTo == tcp // redirected to
}

func PlausibleFinalAltitude(fp *FlightPlan, perf AircraftPerformance, nmPerLongitude float32,
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

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
	Altitude     float32
	Location     math.Point2LL
	Heading      float32
	Groundspeed  float32
}

func (ac *Aircraft) GetRadarTrack() RadarTrack {
	return RadarTrack{
		ADSBCallsign: ac.ADSBCallsign,
		Squawk:       ac.Squawk,
		Mode:         ac.Mode,
		Altitude:     ac.Altitude(),
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

	FlightPlan   FlightPlan
	TypeOfFlight TypeOfFlight

	STARSFlightPlan *STARSFlightPlan // if this is nil, it's unassociated.

	HoldForRelease   bool
	Released         bool // only used for hold for release
	ReleaseTime      time.Time
	WaitingForLaunch bool // for departures

	// The controller who gave approach clearance
	ApproachController string

	Strip FlightStrip

	// State related to navigation.
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

func (ac *Aircraft) HandleControllerDisconnect(callsign string, primaryController string) {
	if callsign == primaryController {
		// Don't change anything; the sim will pause without the primary
		// controller, so we might as well have all of the tracks and
		// inbound handoffs waiting for them when they return.
		return
	}
	if ac.IsUnassociated() {
		return
	}

	sfp := ac.STARSFlightPlan
	if sfp.HandoffTrackController == callsign {
		// Otherwise redirect handoffs to the primary controller. This is
		// not a perfect solution; for an arrival, for example, we should
		// re-resolve it based on the signed-in controllers, as is done in
		// Sim updateState() for arrivals when they are first handed
		// off. We don't have all of that information here, though...
		sfp.HandoffTrackController = primaryController
	}

	if sfp.ControllingController == callsign {
		if sfp.TrackingController == callsign {
			// Drop track of aircraft that we control
			sfp.TrackingController = ""
			sfp.ControllingController = ""
		} else {
			// Another controller has the track but not yet control;
			// just give them control
			sfp.ControllingController = sfp.TrackingController
		}
	}
}

func (ac *Aircraft) TransferTracks(from, to string) {
	if ac.ApproachController == from {
		ac.ApproachController = to
	}

	if ac.IsUnassociated() {
		return
	}

	sfp := ac.STARSFlightPlan
	if sfp.HandoffTrackController == from {
		sfp.HandoffTrackController = to
	}
	if sfp.TrackingController == from {
		sfp.TrackingController = to
	}
	if sfp.ControllingController == from {
		sfp.ControllingController = to
	}
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

// Helper function to make the code for the common case of a readback
// response more compact.
func (ac *Aircraft) readback(f string, args ...interface{}) []RadioTransmission {
	if ac.IsAssociated() {
		return []RadioTransmission{RadioTransmission{
			Controller: ac.STARSFlightPlan.ControllingController,
			Message:    fmt.Sprintf(f, args...),
			Type:       RadioTransmissionReadback,
		}}
	} else {
		return nil
	}
}

func (ac *Aircraft) readbackUnexpected(f string, args ...interface{}) []RadioTransmission {
	if ac.IsAssociated() {
		return []RadioTransmission{RadioTransmission{
			Controller: ac.STARSFlightPlan.ControllingController,
			Message:    fmt.Sprintf(f, args...),
			Type:       RadioTransmissionUnexpected,
		}}
	} else {
		return nil
	}
}

func (ac *Aircraft) transmitResponse(r PilotResponse) []RadioTransmission {
	if ac.IsAssociated() {
		return []RadioTransmission{RadioTransmission{
			Controller: ac.STARSFlightPlan.ControllingController,
			Message:    r.Message,
			Type:       RadioTransmissionType(util.Select(r.Unexpected, RadioTransmissionUnexpected, RadioTransmissionReadback)),
		}}
	} else {
		return nil
	}
}

func (ac *Aircraft) Update(wind WindModel, lg *log.Logger) *Waypoint {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	passedWaypoint := ac.Nav.Update(wind, &ac.FlightPlan, lg)
	if passedWaypoint != nil {
		lg.Info("passed", slog.Any("waypoint", passedWaypoint))

		if passedWaypoint.ClearApproach && ac.IsAssociated() {
			ac.ApproachController = ac.STARSFlightPlan.ControllingController
		}
	}

	return passedWaypoint
}

func (ac *Aircraft) GoAround() []RadioTransmission {
	resp := ac.Nav.GoAround()
	ac.GotContactTower = false
	if ac.IsAssociated() {
		return []RadioTransmission{RadioTransmission{
			Controller: ac.STARSFlightPlan.ControllingController,
			Message:    resp.Message,
			Type:       RadioTransmissionType(util.Select(resp.Unexpected, RadioTransmissionUnexpected, RadioTransmissionContact)),
		}}
	} else {
		return nil
	}
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
	if err == nil && ac.IsAssociated() {
		ac.ApproachController = ac.STARSFlightPlan.ControllingController
	}
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) ClearedStraightInApproach(id string) []RadioTransmission {
	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true)
	if err == nil && ac.IsAssociated() {
		ac.ApproachController = ac.STARSFlightPlan.ControllingController
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
		if ac.IsAssociated() {
			prevController := ac.STARSFlightPlan.ControllingController
			ac.STARSFlightPlan.ControllingController = "_TOWER"

			return []RadioTransmission{RadioTransmission{
				Controller: prevController,
				Message:    "contact tower",
				Type:       RadioTransmissionReadback,
			}}
		} else {
			return nil
		}
	}
}

func (ac *Aircraft) InterceptApproach() []RadioTransmission {
	resp := ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport)
	return ac.transmitResponse(resp)
}

func getAircraftTime(now time.Time) time.Time {
	// Hallucinate a random time around the present for the aircraft.
	delta := time.Duration(-20 + rand.Intn(40))
	t := now.Add(delta * time.Minute)

	// 9 times out of 10, make it a multiple of 5 minutes
	if rand.Intn(10) != 9 {
		dm := t.Minute() % 5
		t = t.Add(time.Duration(5-dm) * time.Minute)
	}

	return t
}

func (ac *Aircraft) InitializeArrival(ap *Airport, arr *Arrival, arrivalHandoffController string, goAround bool,
	nmPerLongitude float32, magneticVariation float32, wind WindModel, now time.Time, lg *log.Logger) (STARSFlightPlan, error) {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]
	ac.WaypointHandoffController = arrivalHandoffController

	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return STARSFlightPlan{}, ErrUnknownAircraftType
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
		return STARSFlightPlan{}, fmt.Errorf("error initializing Nav")
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
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
		ac.ExpectApproach(*arr.ExpectApproach.A, ap, lg)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
			ac.ExpectApproach(app, ap, lg)
		}
	}

	return STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: "", // TODO
		ExitFix:  util.Select(len(ac.FlightPlan.ArrivalAirport) == 4, ac.FlightPlan.ArrivalAirport[1:], ac.FlightPlan.ArrivalAirport),
		ETAOrPTD: getAircraftTime(now),

		TrackingController:    arr.InitialController,
		ControllingController: arr.InitialController,

		Rules:        IFR,
		TypeOfFlight: FlightTypeArrival,

		Scratchpad:          arr.Scratchpad,
		SecondaryScratchpad: arr.SecondaryScratchpad,
		RequestedAltitude:   ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     perf.Category.CWT,
	}, nil
}

func (ac *Aircraft) InitializeDeparture(ap *Airport, departureAirport string, dep *Departure,
	runway string, exitRoute ExitRoute, nmPerLongitude float32,
	magneticVariation float32, scratchpads map[string]string,
	primaryController string, multiControllers SplitConfiguration,
	wind WindModel, now time.Time, lg *log.Logger) (STARSFlightPlan, error) {
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
		return STARSFlightPlan{}, ErrUnknownAircraftType
	}

	ac.FlightPlan.Exit = dep.Exit

	idx := rand.SampleFiltered(dep.Altitudes, func(alt int) bool { return alt <= int(perf.Ceiling) })
	if idx == -1 {
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	} else {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	}

	ac.HoldForRelease = ap.HoldForRelease && ac.FlightPlan.Rules == IFR // VFRs aren't held
	ac.TypeOfFlight = FlightTypeDeparture

	randomizeAltitudeRange := ac.FlightPlan.Rules == VFR
	nav := MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, exitRoute.SpeedRestriction, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, wind, lg)
	if nav == nil {
		return STARSFlightPlan{}, fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	ac.Nav.Check(lg)

	shortExit, _, _ := strings.Cut(dep.Exit, ".") // chop any excess
	sfp := STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: util.Select(len(ac.FlightPlan.DepartureAirport) == 4, ac.FlightPlan.DepartureAirport[1:], ac.FlightPlan.DepartureAirport),
		ExitFix:  shortExit,
		ETAOrPTD: getAircraftTime(now),

		InitialController: util.Select(ac.DepartureContactController != "", ac.DepartureContactController, /* human */
			ap.DepartureController /* virtual */),

		Rules:        IFR,
		TypeOfFlight: FlightTypeDeparture,

		Scratchpad:          util.Select(dep.Scratchpad != "", dep.Scratchpad, scratchpads[dep.Exit]),
		SecondaryScratchpad: dep.SecondaryScratchpad,
		RequestedAltitude:   ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     perf.Category.CWT,
	}

	if ap.DepartureController != "" && ap.DepartureController != primaryController {
		// starting out with a virtual controller
		sfp.TrackingController = ap.DepartureController
		sfp.ControllingController = ap.DepartureController
		ac.WaypointHandoffController = exitRoute.HandoffController
	} else {
		// human controller will be first
		ctrl := primaryController
		if len(multiControllers) > 0 {
			var err error
			ctrl, err = multiControllers.GetDepartureController(departureAirport, runway, exitRoute.SID)
			if err != nil {
				lg.Error("unable to get departure controller", slog.Any("error", err),
					slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
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

	return sfp, nil
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

func (ac *Aircraft) InitializeOverflight(of *Overflight, controller string, nmPerLongitude float32,
	magneticVariation float32, wind WindModel, now time.Time, lg *log.Logger) (STARSFlightPlan, error) {
	perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return STARSFlightPlan{}, ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(of.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation)
	}
	ac.FlightPlan.Route = of.Waypoints.RouteString()
	ac.TypeOfFlight = FlightTypeOverflight
	ac.WaypointHandoffController = controller

	nav := MakeOverflightNav(ac.ADSBCallsign, of, ac.FlightPlan, perf, nmPerLongitude,
		magneticVariation, wind, lg)
	if nav == nil {
		return STARSFlightPlan{}, fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	return STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: "", // TODO
		ExitFix:  "", // TODO
		ETAOrPTD: getAircraftTime(now),

		TrackingController:    of.InitialController,
		ControllingController: of.InitialController,

		Rules:               IFR,
		TypeOfFlight:        FlightTypeOverflight,
		Scratchpad:          of.Scratchpad,
		SecondaryScratchpad: of.SecondaryScratchpad,

		RequestedAltitude: ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     perf.Category.CWT,
	}, nil
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

func (ac *Aircraft) IsUnassociated() bool {
	return ac.STARSFlightPlan == nil
}

func (ac *Aircraft) IsAssociated() bool {
	return ac.STARSFlightPlan != nil
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

func (ac *Aircraft) AssociateFlightPlan(fp *STARSFlightPlan) {
	ac.STARSFlightPlan = fp
}

func (ac *Aircraft) UpdateFlightPlan(spec STARSFlightPlanSpecifier) STARSFlightPlan {
	if ac.STARSFlightPlan != nil {
		if spec.InitialController.IsSet {
			ac.STARSFlightPlan.TrackingController = spec.InitialController.Get()
		}

		ac.STARSFlightPlan.Update(spec)
		return *ac.STARSFlightPlan
	}
	return spec.GetFlightPlan()
}

// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"log/slog"
	"strings"
)

type Aircraft struct {
	Callsign            string
	Scratchpad          string
	SecondaryScratchpad string
	AssignedSquawk      Squawk // from ATC
	Squawk              Squawk // actually squawking
	Mode                TransponderMode
	TempAltitude        int
	FlightPlan          *FlightPlan
	ForceQLControllers  []string
	PointOutHistory     []string

	// Who has the radar track
	TrackingController string
	// Who has control of the aircraft; may not be the same as
	// TrackingController, e.g. after an aircraft has been flashed but
	// before they have been instructed to contact the new tracking
	// controller.
	ControllingController string

	// Handoff offered but not yet accepted
	HandoffTrackController string

	GlobalLinePosition *CardinalOrdinalDirection
	RedirectedHandoff  RedirectedHandoff

	// The controller who gave approach clearance
	ApproachController string

	Strip FlightStrip

	// State related to navigation. Pointers are used for optional values;
	// nil -> unset/unspecified.
	Nav Nav

	// Departure related state
	Exit                       string
	DepartureContactAltitude   float32
	DepartureContactController string

	// Arrival-related state
	STAR              string
	GoAroundDistance  *float32
	ArrivalGroup      string
	ArrivalGroupIndex int
	GotContactTower   bool

	// Who to try to hand off to at a waypoint with /ho
	WaypointHandoffController string
}

type RedirectedHandoff struct {
	OrigionalOwner string   // Controller callsign
	Redirector     []string // Sector id
	RedirectedTo   string   // Sector id
	RDIndicator    bool
}

type PilotResponse struct {
	Message    string
	Unexpected bool // should it be highlighted in the UI
}

///////////////////////////////////////////////////////////////////////////
// Aircraft

func (ac *Aircraft) TAS() float32 {
	return ac.Nav.TAS()
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (ac *Aircraft) HandleControllerDisconnect(callsign string, w *World) {
	if callsign == w.PrimaryController {
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
		ac.HandoffTrackController = w.PrimaryController
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
		Type:       RadioTransmissionType(Select(r.Unexpected, RadioTransmissionUnexpected, RadioTransmissionReadback)),
	}}
}

func (ac *Aircraft) Update(w *World, ep EventPoster, simlg *Logger) *Waypoint {
	lg := simlg.With(slog.String("callsign", ac.Callsign))

	passedWaypoint := ac.Nav.Update(w, lg)
	if passedWaypoint != nil {
		lg.Info("passed", slog.Any("waypoint", passedWaypoint))

		if passedWaypoint.Delete && ac.Nav.Approach.Cleared {
			lg.Info("deleting aircraft after landing")
			w.DeleteAircraft(ac, nil)
		}
	}

	if ac.GoAroundDistance != nil {
		if d, err := ac.Nav.finalApproachDistance(); err == nil && d < *ac.GoAroundDistance {
			lg.Info("randomly going around")
			ac.GoAroundDistance = nil // only go around once
			rt := ac.GoAround()
			ac.ControllingController = w.DepartureController(ac)
			PostRadioEvents(ac.Callsign, rt, ep)

			// If it was handed off to tower, hand it back to us
			if ac.TrackingController != "" && ac.TrackingController != ac.ApproachController {
				ac.HandoffTrackController = w.DepartureController(ac)
				if ac.HandoffTrackController == "" {
					ac.HandoffTrackController = ac.ApproachController
				}
				ep.PostEvent(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.TrackingController,
					ToController:   ac.ApproachController,
				})
			}
		}
	}

	return passedWaypoint
}

func (ac *Aircraft) GoAround() []RadioTransmission {
	resp := ac.Nav.GoAround()
	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    resp.Message,
		Type:       RadioTransmissionType(Select(resp.Unexpected, RadioTransmissionUnexpected, RadioTransmissionContact)),
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
	hdg := NormalizeHeading(ac.Nav.FlightState.Heading - float32(deg))
	ac.Nav.AssignHeading(hdg, TurnLeft)
	return ac.readback(Sample("turn %d degrees left", "%d to the left"), deg)
}

func (ac *Aircraft) TurnRight(deg int) []RadioTransmission {
	hdg := NormalizeHeading(ac.Nav.FlightState.Heading + float32(deg))
	ac.Nav.AssignHeading(hdg, TurnRight)
	return ac.readback(Sample("turn %d degrees right", "%d to the right"), deg)
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

func (ac *Aircraft) getArrival(w *World) (*Arrival, error) {
	if arrivals, ok := w.ArrivalGroups[ac.ArrivalGroup]; !ok || ac.ArrivalGroupIndex >= len(arrivals) {
		lg.Error("invalid arrival group or index",
			slog.String("callsign", ac.Callsign),
			slog.String("arrival_group", ac.ArrivalGroup),
			slog.Int("index", ac.ArrivalGroupIndex))
		return nil, ErrNoValidArrivalFound
	} else {
		return &arrivals[ac.ArrivalGroupIndex], nil
	}
}

func (ac *Aircraft) ExpectApproach(id string, w *World, lg *Logger) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readbackUnexpected("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readbackUnexpected("unable.")
	}

	lg = lg.With(slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	resp := ac.Nav.ExpectApproach(ac.FlightPlan.ArrivalAirport, id, arr, w, lg)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) AtFixCleared(fix, approach string) []RadioTransmission {
	return ac.transmitResponse(ac.Nav.AtFixCleared(fix, approach))
}

func (ac *Aircraft) ClearedApproach(id string, w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readbackUnexpected("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readbackUnexpected("unable.")
	}

	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, false, arr, w)
	if err == nil {
		ac.ApproachController = ac.ControllingController
	}
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) ClearedStraightInApproach(id string, w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readbackUnexpected("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readbackUnexpected("unable.")
	}

	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true, arr, w)
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

func (ac *Aircraft) ContactTower(w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readbackUnexpected("unable. This aircraft is a departure.")
	} else if ac.GotContactTower {
		// No response; they're not on our frequency any more.
		return nil
	} else if ac.Nav.Approach.Assigned == nil {
		return ac.readbackUnexpected("unable. We haven't been given an approach.")
	} else if !ac.Nav.Approach.Cleared {
		return ac.readbackUnexpected("unable. We haven't been cleared for the approach.")
	} else {
		ac.GotContactTower = true
		twr := ac.Nav.Approach.Assigned.TowerController
		prevController := ac.ControllingController
		ac.ControllingController = twr

		msg := "contact tower"
		if ctrl, ok := w.Controllers[twr]; !ok {
			lg.Error("unknown tower controller", slog.String("tower_callsign", twr), slog.Any("aircraft", ac))
		} else {
			msg += ", " + ctrl.Frequency.String()
		}

		return []RadioTransmission{RadioTransmission{
			Controller: prevController,
			Message:    msg,
			Type:       RadioTransmissionReadback,
		}}
	}
}

func (ac *Aircraft) InterceptLocalizer(w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readbackUnexpected("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readbackUnexpected("unable.")
	}

	resp := ac.Nav.InterceptLocalizer(ac.FlightPlan.ArrivalAirport, arr, w)
	return ac.transmitResponse(resp)
}

func (ac *Aircraft) InitializeArrival(w *World, arrivalGroup string,
	arrivalGroupIndex int, arrivalHandoffController string, goAround bool) error {
	arr := &w.ArrivalGroups[arrivalGroup][arrivalGroupIndex]
	ac.STAR = arr.STAR
	ac.ArrivalGroup = arrivalGroup
	ac.ArrivalGroupIndex = arrivalGroupIndex
	ac.Scratchpad = arr.Scratchpad
	ac.SecondaryScratchpad = arr.SecondaryScratchpad

	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	ac.WaypointHandoffController = arrivalHandoffController

	perf, ok := database.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan, perf)
	}
	if arr.Route != "" {
		ac.FlightPlan.Route = arr.Route
	} else {
		ac.FlightPlan.Route = "/. " + arr.STAR
	}

	if goAround {
		d := 0.1 + .6*rand.Float32()
		ac.GoAroundDistance = &d
	}

	nav := MakeArrivalNav(w, arr, *ac.FlightPlan, perf)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if arr.ExpectApproach != "" {
		lg = lg.With(slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
		ac.ExpectApproach(arr.ExpectApproach, w, lg)
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(w *World, ap *Airport, departureAirport string, dep *Departure, runway string,
	exitRoute ExitRoute) error {
	wp := DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = FilterSlice(wp, func(wp Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.FlightPlan.Route = exitRoute.SID + " " + dep.Route
	} else {
		ac.FlightPlan.Route = dep.Route
	}

	perf, ok := database.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.Scratchpad = dep.Scratchpad
	if ac.Scratchpad == "" {
		ac.Scratchpad = w.Scratchpads[dep.Exit]
	}
	ac.SecondaryScratchpad = dep.SecondaryScratchpad
	ac.Exit = dep.Exit

	if dep.Altitude == 0 {
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan, perf)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	nav := MakeDepartureNav(w, *ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, wp)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if ap.DepartureController != "" {
		// starting out with a virtual controller
		ac.TrackingController = ap.DepartureController
		ac.ControllingController = ap.DepartureController
		ac.WaypointHandoffController = exitRoute.HandoffController
	} else {
		// human controller will be first
		ctrl := w.PrimaryController
		if w.MultiControllers != nil {
			ctrl = w.MultiControllers.GetDepartureController(departureAirport, runway, exitRoute.SID)
			if ctrl == "" {
				ctrl = w.PrimaryController
			}
		}

		ac.DepartureContactAltitude =
			ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(rand.Intn(500))
		ac.DepartureContactAltitude = min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))
		ac.DepartureContactController = ctrl
	}

	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) NavSummary() string {
	return ac.Nav.Summary(*ac.FlightPlan)
}

func (ac *Aircraft) ContactMessage(reportingPoints []ReportingPoint) string {
	return ac.Nav.ContactMessage(reportingPoints, ac.STAR)
}

func (ac *Aircraft) DepartOnCourse() {
	if ac.Exit == "" {
		lg.Warn("unset \"exit\" for departure", slog.String("callsign", ac.Callsign))
	}
	ac.Nav.DepartOnCourse(float32(ac.FlightPlan.Altitude), ac.Exit)
}

func (ac *Aircraft) IsDeparture() bool {
	return ac.Nav.FlightState.IsDeparture
}

func (ac *Aircraft) Check(lg *Logger) {
	ac.Nav.Check(lg)
}

func (ac *Aircraft) Position() Point2LL {
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

func (ac *Aircraft) DepartureAirportElevation() float32 {
	return ac.Nav.FlightState.DepartureAirportElevation
}

func (ac *Aircraft) ArrivalAirportElevation() float32 {
	return ac.Nav.FlightState.ArrivalAirportElevation
}

func (ac *Aircraft) ATPAVolume() *ATPAVolume {
	return ac.Nav.Approach.ATPAVolume
}

func (ac *Aircraft) MVAsApply() bool {
	if ac.IsDeparture() {
		// Start issuing MVAs 5 miles from the field
		// TODO: are there better criteria?
		return nmdistance2ll(ac.Position(), ac.Nav.FlightState.DepartureAirportLocation) > 5
	} else {
		// If they're established on the approach, they're good.
		return !ac.OnApproach(true)
	}
}

// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strings"
)

type Aircraft struct {
	Callsign       string
	Scratchpad     string
	AssignedSquawk Squawk // from ATC
	Squawk         Squawk // actually squawking
	Mode           TransponderMode
	TempAltitude   int
	FlightPlan     *FlightPlan

	// Who has the radar track
	TrackingController string
	// Who has control of the aircraft; may not be the same as
	// TrackingController, e.g. after an aircraft has been flashed but
	// before they have been instructed to contact the new tracking
	// controller.
	ControllingController string

	// Handoff offered but not yet accepted
	HandoffTrackController string

	// The controller who gave approach clearance
	ApproachController string

	Strip FlightStrip

	// State related to navigation. Pointers are used for optional values;
	// nil -> unset/unspecified.
	Nav Nav

	GoAroundDistance         *float32
	DepartureContactAltitude float32

	// Arrival-related state
	ArrivalGroup             string
	ArrivalGroupIndex        int
	ArrivalHandoffController string
}

///////////////////////////////////////////////////////////////////////////
// Aircraft

func (ac *Aircraft) TAS() float32 {
	return ac.Nav.TAS()
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (ac *Aircraft) DropControllerTrack(callsign string) {
	if ac.HandoffTrackController == callsign {
		ac.HandoffTrackController = ""
	}
	if ac.ControllingController == callsign {
		if ac.TrackingController == callsign {
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

func (ac *Aircraft) Update(w *World, ep EventPoster) {
	if passedWaypoint := ac.Nav.Update(w); passedWaypoint != nil {
		if passedWaypoint.Handoff {
			ac.HandoffTrackController = w.Callsign
			if ep != nil {
				ep.PostEvent(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.ControllingController,
					ToController:   ac.HandoffTrackController,
				})
			}
		}
		if passedWaypoint.Delete && ac.Nav.Approach.Cleared {
			w.DeleteAircraft(ac, nil)
		}
	}

	if ac.IsDeparture() && ac.DepartureContactAltitude != 0 &&
		ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude {
		// We're above the contact altitude, so time to check in.
		dep := w.GetDepartureController(ac)
		PostRadioEvents(ac.Callsign, []RadioTransmission{RadioTransmission{
			Controller: dep,
			Message:    ac.Nav.DepartureMessage(),
			Type:       RadioTransmissionContact,
		}}, ep)

		// Clear this out so we only send one contact message
		ac.DepartureContactAltitude = 0

		// Only after we're on frequency can the controller start
		// issuing control commands..
		ac.ControllingController = dep
	}

	if ac.GoAroundDistance != nil {
		if d, err := ac.Nav.finalApproachDistance(); err == nil && d < *ac.GoAroundDistance {
			rt := ac.GoAround()
			PostRadioEvents(ac.Callsign, rt, ep)

			// If it was handed off to tower, hand it back to us
			if ac.TrackingController != "" && ac.TrackingController != ac.ApproachController {
				ac.HandoffTrackController = ac.ApproachController
				ep.PostEvent(Event{
					Type:           OfferedHandoffEvent,
					Callsign:       ac.Callsign,
					FromController: ac.TrackingController,
					ToController:   ac.ApproachController,
				})
			}
		}
	}
}

func (ac *Aircraft) GoAround() []RadioTransmission {
	resp := ac.Nav.GoAround()

	return []RadioTransmission{RadioTransmission{
		Controller: ac.ControllingController,
		Message:    resp,
		Type:       RadioTransmissionContact,
	}}
}

func (ac *Aircraft) AssignAltitude(altitude int) []RadioTransmission {
	response := ac.Nav.AssignAltitude(float32(altitude))
	return ac.readback(response)
}

func (ac *Aircraft) AssignSpeed(speed int) []RadioTransmission {
	resp := ac.Nav.AssignSpeed(float32(speed))
	return ac.readback(resp)
}

func (ac *Aircraft) MaintainSlowestPractical() []RadioTransmission {
	return ac.readback(ac.Nav.MaintainSlowestPractical())
}

func (ac *Aircraft) MaintainMaximumForward() []RadioTransmission {
	return ac.readback(ac.Nav.MaintainMaximumForward())
}

func (ac *Aircraft) ExpediteDescent() []RadioTransmission {
	resp := ac.Nav.ExpediteDescent()
	return ac.readback(resp)
}

func (ac *Aircraft) ExpediteClimb() []RadioTransmission {
	resp := ac.Nav.ExpediteClimb()
	return ac.readback(resp)
}

func (ac *Aircraft) AssignHeading(heading int, turn TurnMethod) []RadioTransmission {
	resp := ac.Nav.AssignHeading(float32(heading), turn)
	return ac.readback(resp)
}

func (ac *Aircraft) TurnLeft(deg int) []RadioTransmission {
	hdg := NormalizeHeading(ac.Nav.FlightState.Heading - float32(deg))
	ac.Nav.AssignHeading(hdg, TurnLeft)
	return ac.readback(Sample([]string{"turn %d degrees left", "%d to the left"}), deg)
}

func (ac *Aircraft) TurnRight(deg int) []RadioTransmission {
	hdg := NormalizeHeading(ac.Nav.FlightState.Heading + float32(deg))
	ac.Nav.AssignHeading(hdg, TurnRight)
	return ac.readback(Sample([]string{"turn %d degrees right", "%d to the right"}), deg)
}

func (ac *Aircraft) FlyPresentHeading() []RadioTransmission {
	resp := ac.Nav.FlyPresentHeading()
	return ac.readback(resp)
}

func (ac *Aircraft) DirectFix(fix string) []RadioTransmission {
	resp := ac.Nav.DirectFix(strings.ToUpper(fix))
	return ac.readback(resp)
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) []RadioTransmission {
	resp := ac.Nav.DepartFixHeading(strings.ToUpper(fix), float32(hdg))
	return ac.readback(resp)
}

func (ac *Aircraft) DepartFixDirect(fixa, fixb string) []RadioTransmission {
	resp := ac.Nav.DepartFixDirect(strings.ToUpper(fixa), strings.ToUpper(fixb))
	return ac.readback(resp)
}

func (ac *Aircraft) CrossFixAt(fix string, ar *AltitudeRestriction, speed int) []RadioTransmission {
	resp := ac.Nav.CrossFixAt(strings.ToUpper(fix), ar, speed)
	return ac.readback(resp)
}

func (ac *Aircraft) getArrival(w *World) (*Arrival, error) {
	if arrivals, ok := w.ArrivalGroups[ac.ArrivalGroup]; !ok || ac.ArrivalGroupIndex >= len(arrivals) {
		lg.Errorf("%s: invalid arrival group %s or index %d", ac.Callsign, ac.ArrivalGroup,
			ac.ArrivalGroupIndex)
		return nil, ErrNoValidArrivalFound
	} else {
		return &arrivals[ac.ArrivalGroupIndex], nil
	}
}

func (ac *Aircraft) ExpectApproach(id string, w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readback("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readback("unable.")
	}

	resp, _ := ac.Nav.ExpectApproach(ac.FlightPlan.ArrivalAirport, id, arr, w)
	return ac.readback(resp)
}

func (ac *Aircraft) AtFixCleared(fix, approach string) []RadioTransmission {
	return ac.readback(ac.Nav.AtFixCleared(fix, approach))
}

func (ac *Aircraft) ClearedApproach(id string, w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readback("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readback("unable.")
	}

	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, false, arr, w)
	if err == nil {
		ac.ApproachController = ac.ControllingController
	}
	return ac.readback(resp)
}

func (ac *Aircraft) ClearedStraightInApproach(id string, w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readback("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readback("unable.")
	}

	resp, err := ac.Nav.clearedApproach(ac.FlightPlan.ArrivalAirport, id, true, arr, w)
	if err == nil {
		ac.ApproachController = ac.ControllingController
	}
	return ac.readback(resp)
}

func (ac *Aircraft) CancelApproachClearance() []RadioTransmission {
	resp := ac.Nav.CancelApproachClearance()
	return ac.readback(resp)
}

func (ac *Aircraft) ClimbViaSID() []RadioTransmission {
	return ac.readback(ac.Nav.ClimbViaSID())
}

func (ac *Aircraft) DescendViaSTAR() []RadioTransmission {
	return ac.readback(ac.Nav.DescendViaSTAR())
}

func (ac *Aircraft) InterceptLocalizer(w *World) []RadioTransmission {
	if ac.IsDeparture() {
		return ac.readback("unable. This aircraft is a departure.")
	}

	arr, err := ac.getArrival(w)
	if err != nil {
		return ac.readback("unable.")
	}

	resp := ac.Nav.InterceptLocalizer(ac.FlightPlan.ArrivalAirport, arr, w)
	return ac.readback(resp)
}

func (ac *Aircraft) InitializeArrival(w *World, arrivalGroup string,
	arrivalGroupIndex int, goAround bool) error {
	arr := &w.ArrivalGroups[arrivalGroup][arrivalGroupIndex]
	ac.ArrivalGroup = arrivalGroup
	ac.ArrivalGroupIndex = arrivalGroupIndex
	ac.Scratchpad = arr.Scratchpad

	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	if len(w.MultiControllers) > 0 {
		for callsign, mc := range w.MultiControllers {
			if idx := Find(mc.Arrivals, arrivalGroup); idx != -1 {
				ac.ArrivalHandoffController = callsign
			}
		}
		if ac.ArrivalHandoffController == "" {
			return fmt.Errorf("%s: couldn't find arrival controller", ac.Callsign)
		}
	}

	perf, ok := database.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan, perf)
	}
	ac.FlightPlan.Route = arr.Route

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
		ac.ExpectApproach(arr.ExpectApproach, w)
	}

	ac.Nav.Check(ac.Callsign)

	return nil
}

func (ac *Aircraft) InitializeDeparture(w *World, ap *Airport, dep *Departure,
	exitRoute ExitRoute) error {
	wp := DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = FilterSlice(wp, func(wp Waypoint) bool { return !wp.Location.IsZero() })

	ac.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Route

	perf, ok := database.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.BaseType())
		return ErrUnknownAircraftType
	}

	ac.Scratchpad = w.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan, perf)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	alt := float32(min(exitRoute.ClearedAltitude, ac.FlightPlan.Altitude))
	nav := MakeDepartureNav(w, *ac.FlightPlan, perf, alt, wp)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	ac.TrackingController = ap.DepartureController
	ac.ControllingController = ap.DepartureController
	ac.DepartureContactAltitude =
		ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(rand.Intn(500))
	ac.DepartureContactAltitude = min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))

	ac.Nav.Check(ac.Callsign)

	return nil
}

func (ac *Aircraft) NavSummary() string {
	return ac.Nav.Summary(*ac.FlightPlan)
}

func (ac *Aircraft) ContactMessage(reportingPoints []ReportingPoint) string {
	return ac.Nav.ContactMessage(reportingPoints)
}

func (ac *Aircraft) DepartOnCourse() {
	ac.Nav.DepartOnCourse(float32(ac.FlightPlan.Altitude))
}

func (ac *Aircraft) IsDeparture() bool {
	return ac.Nav.FlightState.IsDeparture
}

func (ac *Aircraft) Check() {
	ac.Nav.Check(ac.Callsign)
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

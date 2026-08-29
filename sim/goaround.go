// sim/goaround.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func (s *Sim) contactDeparture(ac *Aircraft, fp *NASFlightPlan) {
	tcp := fp.InboundHandoffController
	s.lg.Debug("contacting departure controller", slog.String("tcp", string(tcp)))

	// Mark as already contacted so we only send one contact message
	ac.DepartureContactAltitude = -1

	// Queue the contact (may be delayed due to radio activity)
	s.enqueueDepartureContact(ac, tcp)
}

func (s *Sim) isRadarVisible(ac *Aircraft) bool {
	filters := s.State.FacilityAdaptation.Filters
	return !filters.SurfaceTracking.Inside(ac.Position(), int(ac.Altitude()))
}

func (s *Sim) goAround(ac *Aircraft) {
	// Capture approach info before anything clears it.
	approach := ac.Nav.Approach.Assigned
	if approach == nil {
		s.lg.Warn("goAround called without assigned approach",
			slog.String("callsign", string(ac.ADSBCallsign)))
		return
	}
	airport := ac.FlightPlan.ArrivalAirport
	runway := approach.Runway

	proc := s.getGoAroundProcedureForAircraft(ac)
	if proc.HandoffController == "" {
		proc.HandoffController = s.getGoAroundController(ac)
	}

	ac.WentAround = true
	ac.GotContactTower = false
	ac.AskedAboutTowerSwitch = false
	ac.SpacingGoAroundDeclined = false
	ac.GoAroundOnRunwayHeading = proc.IsRunwayHeading

	altitude := float32(proc.Altitude)

	// Waypoint at the opposite threshold recording who to contact when it's reached.
	wp := av.Waypoint{
		Location:       approach.OppositeThreshold,
		Flags:          av.WaypointFlagFlyOver | av.WaypointFlagHasAltRestriction,
		AltRestriction: av.MakeAtAltitudeRestriction(altitude),
		Extra: &av.WaypointExtra{
			ActionGroups: []av.WaypointActionGroup{{Actions: av.WaypointActions{
				Heading:                   av.WaypointHeadingAction{Heading: int16(proc.Heading)},
				GoAroundContactController: proc.HandoffController,
			}}},
		},
	}

	ac.Nav.GoAroundWithProcedure(altitude, wp)

	holdRunways := append([]string{runway}, proc.HoldDepartures...)
	s.holdDeparturesForGoAround(airport, holdRunways, proc.HandoffController)
}

// getGoAroundController returns the TCP that should handle a go-around for the given aircraft.
// Lookup priority: go_around_assignments for airport/runway, airport, then departure_assignments for airport.
func (s *Sim) getGoAroundController(ac *Aircraft) TCP {
	airport := ac.FlightPlan.ArrivalAirport
	runway := ""
	if ac.Nav.Approach.Assigned != nil {
		runway = ac.Nav.Approach.Assigned.Runway
	}

	// Check go_around_assignments for specific runway
	if runway != "" {
		if tcp, ok := s.GoAroundAssignments[airport+"/"+runway]; ok {
			return tcp
		}
	}

	// Check go_around_assignments for airport
	if tcp, ok := s.GoAroundAssignments[airport]; ok {
		return tcp
	}

	// Fall back to departure_assignments for airport
	if tcp, ok := s.DepartureAssignments[airport]; ok {
		return tcp
	}

	// We shouldn't get here but just in case--current controller
	return TCP(ac.ControllerFrequency)
}

// holdDeparturesForGoAround sets GoAroundHoldUntil on the specified runways and
// posts a status message to the go-around controller.
func (s *Sim) holdDeparturesForGoAround(airport string, holdRunways []string, goAroundTCP TCP) {
	if len(holdRunways) == 0 {
		return
	}

	depState, ok := s.DepartureState[airport]
	if !ok {
		return
	}

	holdUntil := s.State.SimTime.Add(time.Minute)

	// Set the hold state on matching runways
	for rwy, state := range depState {
		rwyBase := rwy.Base()
		for _, holdRwy := range holdRunways {
			if rwyBase == av.RunwayID(holdRwy).Base() {
				state.GoAroundHoldUntil = holdUntil
				s.lg.Info("holding departures on runway due to go-around",
					slog.String("airport", airport), slog.String("runway", rwyBase))
			}
		}
	}

	s.eventStream.Post(Event{
		Type:         StatusMessageEvent,
		ToController: ControlPosition(goAroundTCP),
		WrittenText:  fmt.Sprintf("%s DEPARTURES HELD FOR 1 MINUTE", airport),
	})
}

// getGoAroundProcedureForAircraft returns the go-around procedure defined for the
// aircraft's arrival airport/runway, if one exists in the scenario's arrival_runways.
func (s *Sim) getGoAroundProcedureForAircraft(ac *Aircraft) *GoAroundProcedure {
	airport := ac.FlightPlan.ArrivalAirport
	runway := ac.Nav.Approach.Assigned.Runway

	// Find matching arrival runway with a go-around procedure
	for _, ar := range s.State.ArrivalRunways {
		if ar.Airport == airport && ar.Runway.Base() == runway && ar.GoAround != nil {
			return ar.GoAround
		}
	}

	approach := ac.Nav.Approach.Assigned
	return &GoAroundProcedure{
		Heading:           int(math.TrueToMagnetic(approach.RunwayHeading(s.State.NmPerLongitude), s.State.MagneticVariation) + 0.5),
		IsRunwayHeading:   true,
		Altitude:          1000 * int((ac.Nav.FlightState.ArrivalAirportElevation+2500)/1000),
		HandoffController: s.getGoAroundController(ac),
	}
}

// checkFinalApproachSpacing checks for spacing violations between IFR aircraft
// on the same final approach and triggers go-arounds when separation is insufficient.
func (s *Sim) checkFinalApproachSpacing() {
	if !s.State.LaunchConfig.EnableTowerGoArounds {
		return
	}

	type runwayKey struct{ airport, runway string }
	aircraftByRunway := make(map[runwayKey][]*Aircraft)

	// Group IFR aircraft with assigned approaches by airport+runway
	for _, ac := range s.Aircraft {
		// Only tower sends aircraft around; don't include ones that have already been sent around
		// since presumably we'll have vertical separation soon if not already.
		if ac.Nav.Approach.Assigned != nil && ac.GotContactTower && !ac.SentAroundForSpacing {
			key := runwayKey{ac.FlightPlan.ArrivalAirport, ac.Nav.Approach.Assigned.Runway}
			aircraftByRunway[key] = append(aircraftByRunway[key], ac)
		}
	}

	for _, aircraft := range aircraftByRunway {
		// Sort by distance to threshold (closest first)
		threshold := aircraft[0].Nav.Approach.Assigned.Threshold
		slices.SortFunc(aircraft, func(a, b *Aircraft) int {
			return cmp.Compare(math.NMDistance2LL(a.Position(), threshold),
				math.NMDistance2LL(b.Position(), threshold))
		})

		// Check each adjacent pair
		for i := 1; i < len(aircraft); i++ {
			front, trailing := aircraft[i-1], aircraft[i]

			// Get required separation
			vol := trailing.ATPAVolume()
			eligible25nm := vol != nil && vol.Enable25nmApproach &&
				s.State.IsATPAVolume25nmEnabled(vol.Id) &&
				trailing.OnExtendedCenterline(0.2) && front.OnExtendedCenterline(0.2)
			reqSep := av.CWTApproachSeparation(front.CWT(), trailing.CWT(), eligible25nm)

			actualSep := math.NMDistance2LL(front.Position(), trailing.Position())

			majorBust := actualSep < reqSep*0.8
			minorBust := actualSep < reqSep*0.9

			// >20% violation: always go around
			// >10% but <=20% violation: 50% chance (one-time roll); skip check if already declined
			issueGoAround := majorBust || (minorBust && !trailing.SpacingGoAroundDeclined && s.Rand.Float32() < 0.5)
			if issueGoAround {
				s.goAroundForSpacing(trailing)
			} else if minorBust {
				trailing.SpacingGoAroundDeclined = true
			}
		}
	}
}

// goAroundForSpacing initiates a tower-commanded go-around for spacing violations.
func (s *Sim) goAroundForSpacing(ac *Aircraft) {
	ac.SentAroundForSpacing = true
	s.goAround(ac)
}

// GoAroundProcedure defines go-around parameters for a specific arrival runway.
type GoAroundProcedure struct {
	Heading           int      `json:"heading"` // degrees 1-360; 0 (or unset) means runway heading
	IsRunwayHeading   bool     // true when heading was 0 (runway heading) before resolution
	Altitude          int      `json:"altitude"`           // feet, e.g., 2000, 3000
	HandoffController TCP      `json:"handoff_controller"` // TCP (e.g., "1D")
	HoldDepartures    []string `json:"hold_departures"`    // runways to hold, empty = no holds
}

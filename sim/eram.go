// sim/eram.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
)

// This file is the glue between the sim and the enroute package's pseudo-ERAM
// coordination: it extracts what the engine needs from sim types and writes
// the derived entry/exit fix back to the NAS flight plan.

// coordAttrsFor derives the coordination match attributes for a flight. The
// engine tokens come from the aircraft-performance database's engine type
// (J=jet, T=turboprop, anything else=prop); nav is "conventional" for
// non-RNAV flights.
func coordAttrsFor(nasFp *NASFlightPlan, ac *Aircraft, destAirport string) enroute.Attrs {
	nav := "conventional"
	if nasFp.RNAV {
		nav = "rnav"
	}
	return enroute.Attrs{
		Engine:        engineClass(nasFp.AircraftType),
		Nav:           nav,
		ACType:        nasFp.AircraftType,
		DestAirport:   destAirport,
		AssignedLevel: assignedLevelForCoord(nasFp, ac),
	}
}

// assignedLevelForCoord returns the altitude (hundreds of feet) ARTS
// coordination compares against a fix's altitude range for the default
// "Assigned" altitude_kind: the flight's operational level. For departures
// climbing to their filed altitude, that's fixPairLevel's requested-altitude
// case. For arrivals/overflights it's NASFlightPlan.AssignedAltitude when
// already known (set for ERAM-facility spawns), else the altitude implied by
// the aircraft's route/waypoint restrictions — the actual nav/route assigned
// altitude, still meaningful for a TRACON-facility spawn even though STARS
// hasn't set AssignedAltitude there — else fixPairLevel's cruise fallback.
func assignedLevelForCoord(nasFp *NASFlightPlan, ac *Aircraft) int {
	if nasFp.TypeOfFlight == av.FlightTypeDeparture {
		return fixPairLevel(nasFp)
	}
	if nasFp.AssignedAltitude != 0 {
		return nasFp.AssignedAltitude / 100
	}
	if alt, ok := findLowestWaypointAltitude(ac.Nav.Waypoints, ac.Nav.FlightState.Altitude); ok {
		return alt / 100
	}
	return fixPairLevel(nasFp)
}

// makeTrajectory builds the trajectory model for a spawning aircraft, with
// its vertical envelope capped by any matching adapted restrictions.
func (s *Sim) makeTrajectory(ac *Aircraft, nasFp *NASFlightPlan) *enroute.Trajectory {
	cruiseAlt := float32(ac.FlightPlan.Altitude)
	fieldElev := cruiseAlt // overflight: level at the filed altitude
	switch nasFp.TypeOfFlight {
	case av.FlightTypeDeparture:
		fieldElev = ac.Nav.FlightState.DepartureAirportElevation
	case av.FlightTypeArrival:
		fieldElev = ac.Nav.FlightState.ArrivalAirportElevation
	}
	traj := enroute.MakeTrajectory(ac.Nav.Waypoints, nasFp.TypeOfFlight, nasFp.AircraftType,
		cruiseAlt, fieldElev, s.State.NmPerLongitude)
	if ec := s.State.ERAMCoordination; ec != nil && len(ec.Restrictions) > 0 {
		arrivalAirport := ac.FlightPlan.ArrivalAirport
		traj.ApplyRestrictions(ec.Restrictions, nasFp.Route, arrivalAirport,
			coordAttrsFor(nasFp, ac, arrivalAirport))
	}
	return traj
}

// deriveERAMFixPair runs the pseudo-ERAM coordination for a spawning flight and
// writes the derived entry/exit onto the NAS flight plan. It is a no-op unless
// the sim has a resolved ERAM coordination entry (loaded from the ARTCC host).
//
// The coordination fix is the boundary-crossing fix, so it lands on the
// boundary side of the pair (the other side is the local airport, already
// filled at spawn):
// - arrival: coordination fix -> EntryFix (ExitFix stays the arrival airport)
// - departure: coordination fix -> ExitFix (EntryFix stays the departure airport)
// - overflight: coordination fix -> EntryFix (ExitFix left for the assignment "*")
func (s *Sim) deriveERAMFixPair(nasFp *NASFlightPlan, ac *Aircraft) enroute.Result {
	ec := s.State.ERAMCoordination
	if ec == nil || ec.Coord == nil {
		return enroute.Result{}
	}
	destAirport := ac.FlightPlan.ArrivalAirport
	// Fully-contained (internal) flight: a departure whose destination is a
	// local facility airport never leaves the ARTS airspace, so its
	// coordination (exit) fix is the destination airport itself, not a
	// boundary-crossing route/zone partition. Skip route/zone selection
	// entirely; there is no interfacility handoff.
	if nasFp.TypeOfFlight == av.FlightTypeDeparture && s.State.Airports[destAirport] != nil {
		exit := av.TrimICAOPrefix(destAirport) // KCPP -> CPP, matching how EntryFix strips it
		nasFp.ExitFix = exit
		nasFp.CoordinationFix = exit
		// The exit fix is a local-arrival airport; the caller reclassifies the
		// flight plan to an arrival after ownership is assigned. TypeOfFlight
		// stays Departure here so the departure fix-pair owner is used.
		nasFp.LocalArrival = true
		return enroute.Result{Fix: exit, OK: true}
	}
	traj := s.makeTrajectory(ac, nasFp)
	attrs := coordAttrsFor(nasFp, ac, destAirport)
	res := enroute.DeriveCoordinationFix(ec.Coord, traj, attrs, nasFp.TypeOfFlight, nasFp.Route)
	if !res.OK {
		return res
	}
	// Coordination rules name a fix in full, while a flight plan carries
	// 3-character fix ids; store the id so the fix pairs, adapted fix criteria,
	// and airspace awareness rules that match against it can.
	res.Fix = s.State.FacilityAdaptation.FixPairFixID(res.Fix)
	switch nasFp.TypeOfFlight {
	case av.FlightTypeArrival, av.FlightTypeOverflight:
		nasFp.EntryFix = res.Fix
	case av.FlightTypeDeparture:
		nasFp.ExitFix = res.Fix
	}
	nasFp.CoordinationFix = res.Fix
	return res
}

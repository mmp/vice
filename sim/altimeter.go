// sim/altimeter.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// initPilotAltim sets ac.PilotAltim and ac.PilotAltimSetAt according to the
// hybrid spawn rule. No-op if SimulatePilotAltimeter is off.
//
// Categories:
//   - Departure: local field altimeter (or nearest METAR if airport has no METAR)
//   - Arrival / IFR overflight / VFR with flight-following: nearest METAR at spawn
//   - VFR overflight without flight-following: 70% nearest, 30% random within 100 NM
func (s *Sim) initPilotAltim(ac *Aircraft) {
	if !s.State.FacilityAdaptation.SimulatePilotAltimeter {
		return
	}

	pos := ac.Nav.FlightState.Position

	wrongEligible := ac.TypeOfFlight == av.FlightTypeOverflight &&
		ac.FlightPlan.Rules == av.FlightRulesVFR &&
		!ac.RequestedFlightFollowing

	if ac.TypeOfFlight == av.FlightTypeDeparture {
		// Departure: use the departure airport's METAR if available.
		if dep := ac.FlightPlan.DepartureAirport; dep != "" {
			if m, ok := s.State.METAR[dep]; ok {
				ac.PilotAltim = m.Altimeter_inHg()
				ac.PilotAltimSetAt = s.State.SimTime
				return
			}
		}
		// Fallback to nearest METAR.
		ac.PilotAltim = s.nearestActualAltim(pos)
		ac.PilotAltimSetAt = s.State.SimTime
		return
	}

	if wrongEligible && s.Rand.Float32() < 0.30 {
		// 30% chance: pick a random METAR within 100 NM.
		if alt, ok := s.randomMETARWithin(pos, 100); ok {
			ac.PilotAltim = alt
			ac.PilotAltimSetAt = s.State.SimTime
			return
		}
	}

	// Default: nearest METAR.
	ac.PilotAltim = s.nearestActualAltim(pos)
	ac.PilotAltimSetAt = s.State.SimTime
}

// randomMETARWithin returns the altimeter from a uniformly random METAR
// station within rangeNM of pos, excluding the closest one (so the result
// represents a "stale from a different airport" setting). Returns ok=false
// if there are fewer than two stations in range.
func (s *Sim) randomMETARWithin(pos math.Point2LL, rangeNM float32) (float32, bool) {
	type station struct {
		alt  float32
		dist float32
	}
	var inRange []station
	for icao, m := range s.State.METAR {
		ap, ok := av.DB.Airports[icao]
		if !ok {
			continue
		}
		d := math.NMDistance2LL(pos, ap.Location)
		if d <= rangeNM {
			inRange = append(inRange, station{m.Altimeter_inHg(), d})
		}
	}
	if len(inRange) < 2 {
		return 0, false
	}
	// Drop the nearest (we want a *different* station).
	nearestIdx := 0
	for i := 1; i < len(inRange); i++ {
		if inRange[i].dist < inRange[nearestIdx].dist {
			nearestIdx = i
		}
	}
	inRange = append(inRange[:nearestIdx], inRange[nearestIdx+1:]...)
	pick := inRange[s.Rand.Intn(len(inRange))]
	return pick.alt, true
}

// altimBiasFeet returns the altitude error caused by the pilot's altimeter
// setting differing from the local actual. Positive bias means the aircraft
// flies *higher* than assigned (pilot set too low). Negative means lower.
func altimBiasFeet(nearestActualInHg, pilotInHg float32) float32 {
	if pilotInHg == 0 {
		return 0
	}
	return (nearestActualInHg - pilotInHg) * 1000
}

// altimBiasFor returns the current altimeter bias for ac, applying the same
// gating as the per-tick update loop (feature on, airborne, below FL180).
// Returns 0 when the bias should not apply.
func (s *Sim) altimBiasFor(ac *Aircraft) float32 {
	if !s.State.FacilityAdaptation.SimulatePilotAltimeter {
		return 0
	}
	if !ac.Nav.IsAirborne() || ac.Altitude() >= 18000 {
		return 0
	}
	actual := s.nearestActualAltim(ac.Position())
	return altimBiasFeet(actual, ac.PilotAltim)
}

// nearestActualAltim returns the altimeter (inHg) at the METAR-reporting
// station geographically closest to pos. Returns 0 if no usable METAR is
// available; callers treat 0 as "skip bias entirely".
func (s *Sim) nearestActualAltim(pos math.Point2LL) float32 {
	var best float32
	bestDist := float32(1e30)
	for icao, m := range s.State.METAR {
		ap, ok := av.DB.Airports[icao]
		if !ok {
			continue
		}
		d := math.NMDistance2LL(pos, ap.Location)
		if d < bestDist {
			bestDist = d
			best = m.Altimeter_inHg()
		}
	}
	return best
}

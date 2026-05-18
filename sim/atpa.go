// sim/atpa.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"sort"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// ATPAStatus is the STARS ATPA per-aircraft display state. It lives in sim
// because the server computes it (once per tick) and publishes it on
// sim.Track; stars/ re-exports it as an alias for convenience.
type ATPAStatus int

const (
	ATPAStatusUnset ATPAStatus = iota
	ATPAStatusMonitor
	ATPAStatusWarning
	ATPAStatusAlert
)

// ATPADerived holds the server-computed ATPA state for a single aircraft.
// It is stored on Aircraft.ATPADerived and copied through to sim.Track so
// clients do not have to re-walk the sim each frame.
type ATPADerived struct {
	IntrailDistance          float32
	MinimumMIT               float32
	ATPAStatus               ATPAStatus
	ATPALeadAircraftCallsign av.ADSBCallsign
	DrawATPAGraphics         bool
}

// updateATPA walks all ATPA volumes, pairs adjacent aircraft by distance to
// the runway threshold, and populates Aircraft.ATPADerived. This mirrors the
// client-side walk that lived in stars.updateInTrailDistance but runs once
// per sim tick instead of once per client per frame.
//
// Caller must hold s.mu.
func (s *Sim) updateATPA() {
	// Zero out previous results on every aircraft (whether or not ATPA
	// is enabled / the aircraft is on an approach).
	for _, ac := range s.Aircraft {
		ac.ATPADerived = ATPADerived{}
	}

	if !s.State.ATPAEnabled {
		return
	}

	nmPerLongitude := s.State.NmPerLongitude
	magneticVariation := s.State.MagneticVariation

	// Loop over each ATPA volume at arrival airports and process all
	// aircraft inside it.
	for icao, apVolState := range s.State.ATPAVolumeState {
		airport := s.State.Airports[icao]
		if airport == nil {
			continue
		}
		for id, volState := range apVolState {
			if volState.Disabled {
				continue
			}
			vol := airport.ATPAVolumes[id]
			if vol == nil {
				continue
			}

			// Collect aircraft on approach to this runway that are
			// inside the volume. We iterate in sorted order for
			// deterministic behavior in tests.
			var runwayAircraft []*Aircraft
			for _, ac := range util.SortedMap(s.Aircraft) {
				acVol := ac.ATPAVolume()
				if acVol == nil || acVol.Id != vol.Id || acVol.Threshold != vol.Threshold {
					continue
				}

				// Excluded scratchpad -> aircraft doesn't participate at all.
				if ac.NASFlightPlan != nil && ac.NASFlightPlan.Scratchpad != "" &&
					slices.Contains(vol.ExcludedScratchpads, ac.NASFlightPlan.Scratchpad) {
					continue
				}

				// Determine the altitude the volume sees (transponder
				// altitude; zero when transponder isn't reporting mode C).
				alt := float32(0)
				if ac.Mode == av.TransponderModeAltitude {
					alt = ac.Altitude()
				}
				if !vol.Inside(ac.Position(), alt, ac.Heading(),
					nmPerLongitude, magneticVariation) {
					continue
				}
				runwayAircraft = append(runwayAircraft, ac)
			}

			// Sort by distance to threshold (closest first).
			sort.SliceStable(runwayAircraft, func(i, j int) bool {
				return math.NMDistance2LL(runwayAircraft[i].Position(), vol.Threshold) <
					math.NMDistance2LL(runwayAircraft[j].Position(), vol.Threshold)
			})

			for i := range runwayAircraft {
				if i == 0 {
					// The first one doesn't have anyone in front.
					continue
				}
				leading, trailing := runwayAircraft[i-1], runwayAircraft[i]
				trailing.ATPADerived.IntrailDistance =
					math.NMDistance2LL(leading.Position(), trailing.Position())
				trailing.ATPADerived.DrawATPAGraphics = true
				s.checkInTrailCwtSeparation(trailing, leading, vol, nmPerLongitude, magneticVariation)
			}
		}
	}
}

// checkInTrailCwtSeparation sets MinimumMIT, ATPALeadAircraftCallsign, and
// ATPAStatus on the trailing aircraft based on a short lookahead simulation
// against the leading aircraft. Mirrors stars.checkInTrailCwtSeparation but
// runs server-side against authoritative aircraft state.
func (s *Sim) checkInTrailCwtSeparation(back, front *Aircraft, vol *av.ATPAVolume,
	nmPerLongitude, magneticVariation float32) {
	// ATPA is only meaningful for associated (tracked) aircraft.
	if back.NASFlightPlan == nil || front.NASFlightPlan == nil {
		return
	}

	eligible25nm := vol.Enable25nmApproach &&
		s.State.IsATPAVolume25nmEnabled(vol.Id) &&
		math.NMDistance2LL(vol.Threshold, back.Position()) < vol.Dist25nmApproach &&
		back.OnExtendedCenterline(0.2) && front.OnExtendedCenterline(0.2)
	cwtSeparation := av.CWTApproachSeparation(
		front.NASFlightPlan.CWTCategory, back.NASFlightPlan.CWTCategory, eligible25nm)

	back.ATPADerived.MinimumMIT = cwtSeparation
	back.ATPADerived.ATPALeadAircraftCallsign = front.ADSBCallsign
	back.ATPADerived.ATPAStatus = ATPAStatusMonitor // baseline

	// If the aircraft's scratchpad is filtered, then it doesn't get
	// warnings or alerts but is still here for the aircraft behind it.
	if back.NASFlightPlan.Scratchpad != "" &&
		slices.Contains(vol.FilteredScratchpads, back.NASFlightPlan.Scratchpad) {
		return
	}

	// Short extrapolation: will there be a MIT violation within the next 45s?
	// On the server we have authoritative heading / GS straight out of Nav,
	// so we do not need the finite-difference track math the client used.
	frontModel := makeServerModeledAircraft(front, vol.Threshold, nmPerLongitude, magneticVariation)
	backModel := makeServerModeledAircraft(back, vol.Threshold, nmPerLongitude, magneticVariation)

	frontPosition, backPosition := frontModel.p, backModel.p
	for step := range 45 {
		frontPosition = frontModel.nextPosition(frontPosition)
		backPosition = backModel.nextPosition(backPosition)
		distance := math.Distance2f(frontPosition, backPosition)
		if distance < cwtSeparation {
			if step <= 24 {
				// Conflict expected within 24s (6-159): alert.
				back.ATPADerived.ATPAStatus = ATPAStatusAlert
				return
			}
			// Otherwise, within 45s: warning.
			back.ATPADerived.ATPAStatus = ATPAStatusWarning
			return
		}
	}
}

// serverModeledAircraft is the server-side counterpart of stars.ModeledAircraft.
// It uses authoritative heading / groundspeed rather than finite-difference
// track math.
type serverModeledAircraft struct {
	p            [2]float32 // nm coords
	v            [2]float32 // nm, normalized
	gs           float32
	threshold    [2]float32 // nm
	landingSpeed float32
}

func makeServerModeledAircraft(ac *Aircraft, threshold math.Point2LL,
	nmPerLongitude, magneticVariation float32) serverModeledAircraft {
	ma := serverModeledAircraft{
		p:         math.LL2NM(ac.Position(), nmPerLongitude),
		gs:        ac.GS(),
		threshold: math.LL2NM(threshold, nmPerLongitude),
	}

	if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok {
		ma.landingSpeed = perf.Speed.Landing
	} else {
		ma.landingSpeed = 120
	}

	// Heading vector: true heading -> unit vector in NM space.
	// Aviation convention: heading 0 = north (+y), 90 = east (+x).
	trueHdg := math.MagneticToTrue(ac.Heading(), magneticVariation)
	ma.v = math.HeadingVector(trueHdg)
	return ma
}

// nextPosition returns the extrapolated position 1s in the future, tapering
// speed down to the landing speed near the threshold (same model as the
// client-side ModeledAircraft).
func (ma *serverModeledAircraft) nextPosition(p [2]float32) [2]float32 {
	gs := ma.gs
	td := math.Distance2f(p, ma.threshold)
	if td < 2 {
		gs = min(gs, ma.landingSpeed)
	} else if td < 5 {
		t := (td - 2) / 3 // [0, 1]
		gs = math.Lerp(t, ma.landingSpeed, gs)
	}

	gs /= 3600 // nm/second
	return math.Add2f(p, math.Scale2f(ma.v, gs))
}

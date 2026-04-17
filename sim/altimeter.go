// sim/altimeter.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// altimBiasFeet returns the altitude error caused by the pilot's altimeter
// setting differing from the local actual. Positive bias means the aircraft
// flies *higher* than assigned (pilot set too low). Negative means lower.
func altimBiasFeet(nearestActualInHg, pilotInHg float32) float32 {
	if pilotInHg == 0 {
		return 0
	}
	return (nearestActualInHg - pilotInHg) * 1000
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

// sim/modec.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// FPMThreshold is the altitude-change-rate cutoff (feet per minute) above
// which a Mode-C reading is deemed unreasonable. Mirrors the stars-side
// constant that previously lived in stars/track.go.
const FPMThreshold = 8400

// updateModeC runs once per sim tick. For each aircraft with Mode-C
// altitude available it compares the current reading against the previous
// one stored on the Aircraft and flags UnreasonableModeC when the implied
// climb/descent rate exceeds FPMThreshold. Once flagged, the flag clears
// only after five consecutive normal readings. Results are exposed on
// sim.Track via (*Sim).GetStateUpdate.
//
// This mirrors what stars.checkUnreasonableModeC used to do per client
// per tick; the logic is ported verbatim but keyed on server-owned state.
//
// Caller must hold s.mu.
func (s *Sim) updateModeC() {
	now := s.State.SimTime
	for _, ac := range s.Aircraft {
		cur := ac.GetRadarTrack(now)

		// Capture previous reading before advancing; the stars-side
		// updateRadarTracks advances state.previousTrack/state.track
		// unconditionally every tick before running the check.
		prevAlt := ac.PreviousTransponderAlt
		prevTime := ac.PreviousTransponderTime
		ac.PreviousTransponderAlt = cur.TransponderAltitude
		ac.PreviousTransponderTime = now

		// Mode-C unavailable on current or previous reading: reset.
		if cur.Mode != av.TransponderModeAltitude || cur.TransponderAltitude == 0 ||
			prevAlt == 0 {
			ac.UnreasonableModeC = false
			ac.ConsecutiveNormalTracks = 0
			continue
		}

		deltaAlt := prevAlt - cur.TransponderAltitude
		deltaMinutes := prevTime.Sub(now).Minutes()

		if deltaMinutes == 0 {
			// No elapsed time: can't compute a rate; leave flag as-is
			// (matches the stars-side early return). Note that the
			// previous-reading writes above (PreviousTransponderAlt /
			// PreviousTransponderTime) have already been advanced to
			// the current tick's values, so a subsequent tick at a
			// later sim time will have a non-zero delta and can
			// compute the rate normally.
			continue
		}

		rate := math.Abs(deltaAlt / float32(deltaMinutes))
		if rate > FPMThreshold {
			ac.UnreasonableModeC = true
			ac.ConsecutiveNormalTracks = 0
		} else if ac.UnreasonableModeC {
			ac.ConsecutiveNormalTracks++
			if ac.ConsecutiveNormalTracks >= 5 {
				ac.UnreasonableModeC = false
				ac.ConsecutiveNormalTracks = 0
			}
		}
	}
}

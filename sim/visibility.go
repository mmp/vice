// sim/visibility.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// updateVisibility runs once per sim tick and maintains two server-owned
// per-aircraft flags that used to live on the stars-side TrackState:
//
//   - FirstRadarTrackTime: stamped the first tick the aircraft is
//     radar-visible. This mirrors what stars.updateVisibleTracks did
//     per-client/per-frame. Server-side we stamp on the first live tick
//     since isRadarVisible-culled aircraft are excluded from Tracks.
//   - EnteredAirspace: a per-TCW bool map. For each TCW that currently
//     has at least one controller position hosted at the TCW, flips
//     the bit true the first tick the aircraft is inside any airspace
//     volume owned by that TCW. Each TCW has its own monotonic bit so
//     a handoff target does not inherit the previous owner's
//     latched-true value.
//
// Both are exposed on sim.Track via (*Sim).GetStateUpdate.
//
// Caller must hold s.mu.
func (s *Sim) updateVisibility() {
	now := s.State.SimTime
	for _, ac := range s.Aircraft {
		if !s.isRadarVisible(ac) {
			continue
		}

		if ac.FirstRadarTrackTime.IsZero() {
			ac.FirstRadarTrackTime = now
		}

		pos := ac.Position()
		alt := ac.Altitude()

		// Iterate every TCW that currently has a display (human presence
		// at that TCW) and maintain a per-TCW monotonic entered bit.
		// Skip TCWs whose bit is already latched true.
		for tcw := range s.TCWDisplay {
			if ac.EnteredAirspace[tcw] {
				continue
			}
			var vols []av.ControllerAirspaceVolume
			for _, p := range s.State.GetPositionsForTCW(tcw) {
				for _, avol := range util.SortedMap(s.State.Airspace[p]) {
					vols = append(vols, avol...)
				}
			}
			if len(vols) == 0 {
				continue
			}
			inside, _ := av.InAirspace(pos, alt, vols)
			if inside {
				if ac.EnteredAirspace == nil {
					ac.EnteredAirspace = make(map[TCW]bool)
				}
				ac.EnteredAirspace[tcw] = true
			}
		}
	}
}

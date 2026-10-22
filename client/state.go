// client/state.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"maps"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// SimState is the client's view of simulation state.
// It embeds server.SimState, providing access to all its fields and methods.
type SimState struct {
	server.SimState
}

func (ss *SimState) GetRegularReleaseDepartures() []sim.ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep sim.ReleaseDeparture) bool {
			if dep.Released {
				return false
			}

			for _, cl := range ss.FacilityAdaptation.Lists.Coordination {
				if slices.Contains(cl.Airports, dep.DepartureAirport) {
					// It'll be in a STARS coordination list
					return false
				}
			}
			return true
		})
}

func (ss *SimState) GetSTARSReleaseDepartures() []sim.ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep sim.ReleaseDeparture) bool {
			for _, cl := range ss.FacilityAdaptation.Lists.Coordination {
				if slices.Contains(cl.Airports, dep.DepartureAirport) {
					return true
				}
			}
			return false
		})
}

func (ss *SimState) BeaconCodeInUse(sq av.Squawk) bool {
	if util.SeqContainsFunc(maps.Values(ss.Tracks),
		func(tr *sim.Track) bool {
			return tr.IsAssociated() && tr.Squawk == sq
		}) {
		return true
	}

	if slices.ContainsFunc(ss.UnassociatedFlightPlans,
		func(fp *sim.FlightPlan) bool { return fp.AssignedSquawk == sq }) {
		return true
	}

	return false
}

func (ss *SimState) GetTrackByCallsign(callsign av.ADSBCallsign) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.ADSBCallsign == callsign {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetTrackByACID(acid sim.ACID) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetTrackByCID(flid string) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.CID == flid {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetTrackBySquawk(code av.Squawk) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.AssignedSquawk == code {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetFlightPlanForACID(acid sim.ACID) *sim.FlightPlan {
	for _, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			return trk.FlightPlan
		}
	}
	for i, fp := range ss.UnassociatedFlightPlans {
		if fp.ACID == acid {
			return ss.UnassociatedFlightPlans[i]
		}
	}
	return nil
}

func (ss *SimState) GetInitialRange() float32 {
	tcp := ss.PrimaryPositionForTCW(ss.UserTCW)
	fa := &ss.FacilityAdaptation

	// Controller-specific config takes priority.
	if config, ok := fa.Positions[tcp]; ok && config.Range != 0 {
		return config.Range
	}
	// Then scenario, then area, then facility.
	if ss.ScenarioRange != 0 {
		return ss.ScenarioRange
	}
	if ctrl, ok := ss.Controllers[tcp]; ok && ctrl.Area != "" {
		if ac, ok := fa.Areas[ctrl.Area]; ok && ac.Range != 0 {
			return ac.Range
		}
	}
	return ss.Range
}

func (ss *SimState) GetInitialCenter() math.Point2LL {
	tcp := ss.PrimaryPositionForTCW(ss.UserTCW)
	fa := &ss.FacilityAdaptation

	// Controller-specific config takes priority.
	if config, ok := fa.Positions[tcp]; ok && !config.Center.IsZero() {
		return config.Center.Point2LL
	}
	// Then scenario, then area, then facility.
	if !ss.ScenarioCenter.IsZero() {
		return ss.ScenarioCenter
	}
	if ctrl, ok := ss.Controllers[tcp]; ok && ctrl.Area != "" {
		if ac, ok := fa.Areas[ctrl.Area]; ok && !ac.Center.IsZero() {
			return ac.Center.Point2LL
		}
	}
	return ss.Center
}

// GetAltitudeRange returns the altitude range, in feet, of the user's
// airspace: the union of the ranges of the positions the user's TCW owns.
// Each position's range is resolved in the same order of precedence as the
// scope's center: its "controllers" entry, then the scenario, then its area,
// then the facility, then sim.DefaultAltitudeRange.
func (ss *SimState) GetAltitudeRange() [2]int {
	fa := &ss.FacilityAdaptation
	positionRange := func(pos sim.ControlPosition) [2]int {
		if config, ok := fa.Positions[pos]; ok && config.AltitudeRange != [2]int{} {
			return config.AltitudeRange
		}
		if ss.ScenarioAltitudeRange != [2]int{} {
			return ss.ScenarioAltitudeRange
		}
		if ctrl, ok := ss.Controllers[pos]; ok && ctrl.Area != "" {
			if ac, ok := fa.Areas[ctrl.Area]; ok && ac.AltitudeRange != [2]int{} {
				return ac.AltitudeRange
			}
		}
		if fa.AltitudeRange != [2]int{} {
			return fa.AltitudeRange
		}
		return sim.DefaultAltitudeRange
	}

	positions := ss.GetPositionsForTCW(ss.UserTCW)
	if len(positions) == 0 {
		positions = []sim.ControlPosition{ss.PrimaryPositionForTCW(ss.UserTCW)}
	}
	r := positionRange(positions[0])
	for _, pos := range positions[1:] {
		pr := positionRange(pos)
		r = [2]int{min(r[0], pr[0]), max(r[1], pr[1])}
	}
	return r
}

// GetInitialAltitudeLimits returns the adapted ERAM target and LDB altitude
// limits filters for the user's position, in hundreds of feet. A position that
// adapts any of its limits takes both of them from its own adaptation rather
// than mixing in the scenario's.
func (ss *SimState) GetInitialAltitudeLimits() (targets, ldbs [2]int) {
	tcp := ss.PrimaryPositionForTCW(ss.UserTCW)

	if config, ok := ss.FacilityAdaptation.Positions[tcp]; ok && config.AltitudeLimits.Adapted() {
		return config.AltitudeLimits.Filters()
	}
	return ss.ScenarioAltitudeLimits.Filters()
}

// GetUserConsolidation returns the consolidation state for the current user's TCW.
// Returns nil if no consolidation state exists.
func (ss *SimState) GetUserConsolidation() *sim.TCPConsolidation {
	return ss.CurrentConsolidation[ss.UserTCW]
}

// UserControlsTrack returns true if the current user controls the given track.
func (ss *SimState) UserControlsTrack(track *sim.Track) bool {
	return track.ControlledBy(ss.UserTCW)
}

// UserControlsPosition returns true if the current user controls the given position.
func (ss *SimState) UserControlsPosition(pos sim.ControlPosition) bool {
	return ss.TCWControlsPosition(ss.UserTCW, pos)
}

func (ss *SimState) GetOurTrackByCallsign(callsign av.ADSBCallsign) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.ADSBCallsign == callsign && trk.IsAssociated() && ss.UserControlsTrack(ss.Tracks[i]) {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetOurTrackByACID(acid sim.ACID) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid && ss.UserControlsTrack(ss.Tracks[i]) {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetAllReleaseDepartures() []sim.ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep sim.ReleaseDeparture) bool {
			return ss.UserControlsPosition(dep.DepartureController)
		})
}

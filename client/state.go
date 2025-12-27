// client/state.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
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

			for _, cl := range ss.FacilityAdaptation.CoordinationLists {
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
			for _, cl := range ss.FacilityAdaptation.CoordinationLists {
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
		func(fp *sim.NASFlightPlan) bool { return fp.AssignedSquawk == sq }) {
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

func (ss *SimState) GetTrackByFLID(flid string) (*sim.Track, bool) {
	for i, trk := range ss.Tracks {
		if !trk.IsAssociated() {
			continue
		}
		if trk.FlightPlan.CID == flid {
			return ss.Tracks[i], true
		}
		if trk.ADSBCallsign == av.ADSBCallsign(flid) {
			return ss.Tracks[i], true
		}
		if sq, err := av.ParseSquawk(flid); err == nil && trk.FlightPlan.AssignedSquawk == sq {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *SimState) GetFlightPlanForACID(acid sim.ACID) *sim.NASFlightPlan {
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
	if config, ok := ss.FacilityAdaptation.ControllerConfigs[ss.PrimaryPositionForTCW(ss.UserTCW)]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *SimState) GetInitialCenter() math.Point2LL {
	if config, ok := ss.FacilityAdaptation.ControllerConfigs[ss.PrimaryPositionForTCW(ss.UserTCW)]; ok && !config.Center.IsZero() {
		return config.Center
	}
	return ss.Center
}

// GetUserConsolidation returns the consolidation state for the current user's TCW.
// Returns nil if no consolidation state exists.
func (ss *SimState) GetUserConsolidation() *sim.TCPConsolidation {
	return ss.CurrentConsolidation[ss.UserTCW]
}

// UserControlsTrack returns true if the current user controls the given track.
func (ss *SimState) UserControlsTrack(track *sim.Track) bool {
	return ss.TCWControlsTrack(ss.UserTCW, track)
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

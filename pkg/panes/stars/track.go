// pkg/panes/stars/track.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"sort"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type TrackState struct {
	// Independently of the track history, we store the most recent track
	// from the sensor as well as the previous one. This gives us the
	// freshest possible information for things like calculating headings,
	// rates of altitude change, etc.
	track             av.RadarTrack
	trackTime         time.Time
	previousTrack     av.RadarTrack
	previousTrackTime time.Time

	// Radar track history is maintained with a ring buffer where
	// historyTracksIndex is the index of the next track to be written.
	// (Thus, historyTracksIndex==0 implies that there are no tracks.)
	// Changing to/from FUSED mode causes tracksIndex to be reset, thus
	// discarding previous tracks.
	historyTracks      [10]av.RadarTrack
	historyTracksIndex int

	FullLDBEndTime           time.Time // If the LDB displays the groundspeed. When to stop
	DisplayRequestedAltitude *bool     // nil if unspecified

	IsSelected bool // middle click

	// We handed it off, the other controller accepted it, we haven't yet
	// slewed to make it a PDB.
	DisplayFDB bool

	// Hold for release aircraft released and deleted from the coordination
	// list by the controller.
	ReleaseDeleted bool

	// Only drawn if non-zero
	JRingRadius    float32
	ConeLength     float32
	DisplayTPASize *bool // unspecified->system default if nil

	DisplayATPAMonitor       *bool // unspecified->system default if nil
	DisplayATPAWarnAlert     *bool // unspecified->system default if nil
	IntrailDistance          float32
	ATPAStatus               ATPAStatus
	MinimumMIT               float32
	ATPALeadAircraftCallsign av.ADSBCallsign

	POFlashingEndTime time.Time
	UNFlashingEndTime time.Time
	IFFlashing        bool // Will continue to flash unless slewed or a successful handoff

	SuspendedShowAltitudeEndTime time.Time

	AcceptedHandoffSector     string
	AcceptedHandoffDisplayEnd time.Time

	// These are only set if a leader line direction was specified for this
	// aircraft individually:
	LeaderLineDirection *math.CardinalOrdinalDirection
	UseGlobalLeaderLine bool

	Ghost struct {
		PartialDatablock bool
		State            GhostState
	}

	DisplayLDBBeaconCode bool
	DisplayPTL           bool

	MSAW             bool // minimum safe altitude warning
	MSAWStart        time.Time
	InhibitMSAW      bool // only applies if in an alert. clear when alert is over?
	MSAWAcknowledged bool
	MSAWSoundEnd     time.Time

	SPCAlert        bool
	SPCAcknowledged bool
	SPCSoundEnd     time.Time

	// record the code when it was ack'ed so that if it happens again with
	// a different code, we get a flashing DB in the datablock.
	DBAcknowledged av.Squawk

	FirstRadarTrackTime time.Time
	EnteredOurAirspace  bool

	OutboundHandoffAccepted bool
	OutboundHandoffFlashEnd time.Time

	RDIndicatorEnd time.Time

	// Set when the user enters a command to clear the primary scratchpad,
	// but it is already empty. (In turn, this causes the exit
	// fix/destination airport and the like to no longer be displayed, when
	// it is adapted to be shown in the FDB.)
	ClearedScratchpadAlternate bool

	// This is a little messy: we maintain maps from callsign->sector id
	// for pointouts that track the global state of them. Here we track
	// just inbound pointouts to the current controller so that the first
	// click acks a point out but leaves it yellow and a second clears it
	// entirely.
	PointOutAcknowledged bool
	ForceQL              bool

	// Unreasonable Mode-C
	UnreasonableModeC       bool
	ConsecutiveNormalTracks int

	// This is for [FLT DATA][SLEW] of an unowned FDB in which case it only
	// applies locally; for owned tracks, the flight plan is modified so it
	// applies globally.
	InhibitACTypeDisplay      *bool
	ForceACTypeDisplayEndTime time.Time

	// Draw the datablock in yellow (until cleared); currently only used for
	// [MF]Y[SLEW] quick flight plans
	DatablockAlert bool
}

type ATPAStatus int

const (
	ATPAStatusUnset = iota
	ATPAStatusMonitor
	ATPAStatusWarning
	ATPAStatusAlert
)

type GhostState int

const (
	GhostStateRegular = iota
	GhostStateSuppressed
	GhostStateForced
)

const (
	FPMThreshold = 8400 / 100
)

func (ts *TrackState) TrackDeltaAltitude() int {
	if ts.previousTrack.Location.IsZero() {
		// No previous track
		return 0
	}
	return int(ts.track.Altitude - ts.previousTrack.Altitude)
}

func (ts *TrackState) HaveHeading() bool {
	return !ts.previousTrack.Location.IsZero()
}

// Note that the vector returned by HeadingVector() is along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (ts *TrackState) HeadingVector(nmPerLongitude, magneticVariation float32) math.Point2LL {
	if !ts.HaveHeading() {
		return math.Point2LL{}
	}

	p0 := math.LL2NM(ts.track.Location, nmPerLongitude)
	p1 := math.LL2NM(ts.previousTrack.Location, nmPerLongitude)
	v := math.Sub2LL(p0, p1)
	v = math.Normalize2f(v)
	// v's length should be groundspeed / 60 nm.
	v = math.Scale2f(v, float32(ts.track.Groundspeed)/60) // hours to minutes
	return math.NM2LL(v, nmPerLongitude)
}

func (ts *TrackState) TrackHeading(nmPerLongitude float32) float32 {
	if !ts.HaveHeading() {
		return 0
	}
	return math.Heading2LL(ts.previousTrack.Location, ts.track.Location, nmPerLongitude, 0)
}

func (ts *TrackState) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !ts.track.Location.IsZero() && now.Sub(ts.trackTime) > 30*time.Second
}

func (sp *STARSPane) trackStateForACID(ctx *panes.Context, acid sim.ACID) (*TrackState, bool) {
	// Figure out the ADSB callsign for this ACID.
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			s, ok := sp.TrackState[trk.ADSBCallsign]
			return s, ok
		}
	}
	return nil, false
}

func (sp *STARSPane) processEvents(ctx *panes.Context) {
	// First handle changes in sim.State.Tracks
	for _, trk := range ctx.Client.State.Tracks {
		if _, ok := sp.TrackState[trk.ADSBCallsign]; !ok {
			// First we've seen it; create the *AircraftState for it
			sa := &TrackState{}
			if trk.IsAssociated() {
				sa.UseGlobalLeaderLine = trk.FlightPlan.GlobalLeaderLineDirection != nil
			}

			sp.TrackState[trk.ADSBCallsign] = sa
		}

		if ok, _ := trk.Squawk.IsSPC(); ok && !sp.TrackState[trk.ADSBCallsign].SPCAlert {
			// First we've seen it squawking the SPC
			state := sp.TrackState[trk.ADSBCallsign]
			state.SPCAlert = true
			state.SPCAcknowledged = false
			state.SPCSoundEnd = ctx.Now.Add(AlertAudioDuration)
		}
	}

	// Unsupported DBs also get state, but there's less to it
	for _, fp := range ctx.Client.State.UnassociatedFlightPlans {
		if fp.Location.IsZero() {
			continue
		}
		callsign := av.ADSBCallsign("__" + string(fp.ACID)) // fake callsign to identify for state
		if _, ok := sp.TrackState[callsign]; !ok {
			sp.TrackState[callsign] = &TrackState{}
		}
	}

	// See if any aircraft we have state for have been removed
	for callsign := range sp.TrackState {
		if strings.HasPrefix(string(callsign), "__") { // unsupported fp
			acid := sim.ACID(strings.TrimPrefix(string(callsign), "__"))
			if !slices.ContainsFunc(ctx.Client.State.UnassociatedFlightPlans,
				func(fp *sim.STARSFlightPlan) bool { return fp.ACID == acid }) {
				delete(sp.TrackState, callsign)
			}
		} else if _, ok := ctx.GetTrackByCallsign(callsign); !ok {
			delete(sp.TrackState, callsign)
		}
	}

	// Look for duplicate beacon codes
	sp.DuplicateBeacons = make(map[av.Squawk]interface{})
	beaconCount := make(map[av.Squawk]int)
	for _, trk := range ctx.Client.State.Tracks {
		// Don't count SPC or VFR as duplicates.
		if trk.Squawk == 0o1200 {
			continue
		}
		if ok, _ := av.SquawkIsSPC(trk.Squawk); ok {
			continue
		}

		beaconCount[trk.Squawk] = beaconCount[trk.Squawk] + 1
		if beaconCount[trk.Squawk] > 1 {
			sp.DuplicateBeacons[trk.Squawk] = nil
		}
	}

	// Filter out any removed aircraft from the CA and MCI lists
	sp.CAAircraft = util.FilterSliceInPlace(sp.CAAircraft, func(ca CAAircraft) bool {
		_, a := ctx.GetTrackByCallsign(ca.ADSBCallsigns[0])
		_, b := ctx.GetTrackByCallsign(ca.ADSBCallsigns[1])
		return a && b
	})
	sp.MCIAircraft = util.FilterSliceInPlace(sp.MCIAircraft, func(ca CAAircraft) bool {
		_, a := ctx.GetTrackByCallsign(ca.ADSBCallsigns[0])
		_, b := ctx.GetTrackByCallsign(ca.ADSBCallsigns[1])
		return a && b
	})

	// In the following, note that we may see events that refer to aircraft
	// that no longer exist (e.g., due to deletion). Thus, this is a case
	// where we have to check our accesses to the sp.Aircraft map and not
	// crash if we don't find an entry for an aircraft we have an event
	// for.
	for _, event := range sp.events.Get() {
		switch event.Type {
		case sim.PointOutEvent:
			sp.PointOuts[event.ACID] = PointOutControllers{
				From: event.FromController,
				To:   event.ToController,
			}

		case sim.AcknowledgedPointOutEvent:
			if tcps, ok := sp.PointOuts[event.ACID]; ok {
				if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
					if tcps.From == ctx.UserTCP {
						state.POFlashingEndTime = time.Now().Add(5 * time.Second)
					} else if tcps.To == ctx.UserTCP {
						state.PointOutAcknowledged = true
					}
				}
				delete(sp.PointOuts, event.ACID)
			}

		case sim.RecalledPointOutEvent:
			delete(sp.PointOuts, event.ACID)

		case sim.RejectedPointOutEvent:
			if tcps, ok := sp.PointOuts[event.ACID]; ok && tcps.From == ctx.UserTCP {
				sp.RejectedPointOuts[event.ACID] = nil
				if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
					state.UNFlashingEndTime = time.Now().Add(5 * time.Second)
				}
			}
			delete(sp.PointOuts, event.ACID)

		case sim.FlightPlanAssociatedEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil {
				if fp.TrackingController == ctx.UserTCP {
					if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
						state.DisplayFDB = true

						if fp.QuickFlightPlan {
							state.DatablockAlert = true // display in yellow until slewed
						}
					}
				}
			}

		case sim.OfferedHandoffEvent:
			if event.ToController == ctx.UserTCP {
				sp.playOnce(ctx.Platform, AudioInboundHandoff)
			}

		case sim.AcceptedHandoffEvent, sim.AcceptedRedirectedHandoffEvent:
			if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
				outbound := event.FromController == ctx.UserTCP && event.ToController != ctx.UserTCP
				inbound := event.FromController != ctx.UserTCP && event.ToController == ctx.UserTCP
				if outbound {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					state.OutboundHandoffAccepted = true
					dur := time.Duration(ctx.FacilityAdaptation.HandoffAcceptFlashDuration) * time.Second
					state.OutboundHandoffFlashEnd = time.Now().Add(dur)
					state.DisplayFDB = true

					if event.Type == sim.AcceptedRedirectedHandoffEvent {
						state.RDIndicatorEnd = time.Now().Add(30 * time.Second)
					}
				}
				if outbound || inbound {
					state.AcceptedHandoffSector = util.Select(outbound, event.ToController, event.FromController)
					dur := time.Duration(ctx.FacilityAdaptation.HOSectorDisplayDuration) * time.Second
					state.AcceptedHandoffDisplayEnd = time.Now().Add(dur)
				}
			}
			// Clean up if a point out was instead taken as a handoff.
			delete(sp.PointOuts, event.ACID)

		case sim.SetGlobalLeaderLineEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil {
				if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
					state.UseGlobalLeaderLine = fp.GlobalLeaderLineDirection != nil
				}
			}

		case sim.ForceQLEvent:
			if sp.ForceQLACIDs == nil {
				sp.ForceQLACIDs = make(map[sim.ACID]interface{})
			}
			sp.ForceQLACIDs[event.ACID] = nil

		case sim.TransferRejectedEvent:
			if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
				state.IFFlashing = true
				sp.cancelHandoff(ctx, event.ACID)
			}

		case sim.TransferAcceptedEvent:
			if state, ok := sp.trackStateForACID(ctx, event.ACID); ok {
				state.IFFlashing = false
			}
		}
	}
}

func (sp *STARSPane) isQuicklooked(ctx *panes.Context, trk sim.Track) bool {
	if trk.IsUnassociated() {
		return false
	}

	if sp.currentPrefs().QuickLookAll {
		return true
	}
	if _, ok := sp.ForceQLACIDs[trk.FlightPlan.ACID]; ok {
		return true
	}

	// Quick Look Positions.
	for _, quickLookPositions := range sp.currentPrefs().QuickLookPositions {
		if trk.FlightPlan.TrackingController == quickLookPositions.Id {
			return true
		}
	}

	return false
}

func (sp *STARSPane) updateMSAWs(ctx *panes.Context) {
	// See if there are any MVA issues
	mvas := av.DB.MVAs[ctx.Client.State.TRACON]
	for _, trk := range ctx.Client.State.Tracks {
		state := sp.TrackState[trk.ADSBCallsign]
		if !trk.MVAsApply {
			state.MSAW = false
			continue
		}

		if trk.IsUnassociated() {
			// No MSAW for unassociated tracks.
			state.MSAW = false
			continue
		}

		pilotAlt := trk.FlightPlan.PilotReportedAltitude
		if (trk.FlightPlan.InhibitModeCAltitudeDisplay || trk.Mode != av.TransponderModeAltitude) && pilotAlt == 0 {
			// We can use pilot reported for low altitude alerts: 5-167.
			state.MSAW = false
			continue
		}

		alt := util.Select(pilotAlt != 0, pilotAlt, int(trk.Altitude))
		warn := slices.ContainsFunc(mvas, func(mva av.MVA) bool {
			return alt < mva.MinimumLimit && mva.Inside(trk.Location)
		})

		if !warn && state.InhibitMSAW {
			// The warning has cleared, so the inhibit is disabled (p.7-25)
			state.InhibitMSAW = false
		}
		if warn && !state.MSAW {
			// It's a new alert
			state.MSAWAcknowledged = false
			state.MSAWSoundEnd = time.Now().Add(AlertAudioDuration)
			state.MSAWStart = time.Now()
		}
		state.MSAW = warn
	}
}

func (sp *STARSPane) updateRadarTracks(ctx *panes.Context, tracks []sim.Track) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	now := ctx.Client.State.SimTime
	if sp.radarMode(ctx.FacilityAdaptation.RadarSites) == RadarModeFused {
		if now.Sub(sp.lastTrackUpdate) < 1*time.Second {
			return
		}
	} else {
		if now.Sub(sp.lastTrackUpdate) < 5*time.Second {
			return
		}
	}
	sp.lastTrackUpdate = now

	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]

		state.previousTrack = state.track
		state.previousTrackTime = state.trackTime
		state.track = trk.RadarTrack
		state.trackTime = now

		sp.checkUnreasonableModeC(state)
	}

	// Update low altitude alerts now that we have updated tracks
	sp.updateMSAWs(ctx)

	// History tracks are updated after a radar track update, only if
	// H_RATE seconds have elapsed (4-94).
	ps := sp.currentPrefs()
	if now.Sub(sp.lastHistoryTrackUpdate).Seconds() >= float64(ps.RadarTrackHistoryRate) {
		sp.lastHistoryTrackUpdate = now
		for _, trk := range tracks { // We only get radar tracks for visible aircraft
			state := sp.TrackState[trk.ADSBCallsign]
			idx := state.historyTracksIndex % len(state.historyTracks)
			state.historyTracks[idx] = state.track
			state.historyTracksIndex++
		}
	}

	sp.updateCAAircraft(ctx, tracks)
	sp.updateInTrailDistance(ctx, tracks)
}

func (sp *STARSPane) checkUnreasonableModeC(state *TrackState) {
	changeInAltitude := float64(state.previousTrack.Altitude - state.track.Altitude)
	changeInTime := state.previousTrackTime.Sub(state.trackTime)
	changeInTimeSeconds := changeInTime.Seconds()

	var change float64

	if changeInTimeSeconds != 0 {
		change = changeInAltitude / changeInTimeSeconds
	}

	if change > FPMThreshold || change < -FPMThreshold {
		state.UnreasonableModeC = true
		state.ConsecutiveNormalTracks = 0
	} else if state.UnreasonableModeC {
		state.ConsecutiveNormalTracks++
		if state.ConsecutiveNormalTracks >= 5 {
			state.UnreasonableModeC = false
			state.ConsecutiveNormalTracks = 0
		}
	}
}

func (sp *STARSPane) drawTracks(ctx *panes.Context, tracks []sim.Track, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trackBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trackBuilder)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	// TODO: square icon if it's squawking a beacon code we're monitoring

	// Update cached command buffers for tracks
	sp.fusedTrackVertices = getTrackVertices(ctx, sp.getTrackSize(ctx, transforms))

	now := ctx.Client.State.SimTime
	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]

		if state.LostTrack(now) {
			continue
		}

		positionSymbol := "*"

		if trk.IsUnassociated() {
			switch trk.Mode {
			case av.TransponderModeStandby:
				ps := sp.currentPrefs()
				positionSymbol = util.Select(ps.InhibitPositionSymOnUnassociatedPrimary,
					" ", string(rune(140))) // diamond
			case av.TransponderModeAltitude:
				if sp.beaconCodeSelected(trk.Squawk) {
					positionSymbol = string(rune(129)) // square
				} else {
					positionSymbol = "*"
				}
			case av.TransponderModeOn:
				if sp.beaconCodeSelected(trk.Squawk) {
					positionSymbol = string(rune(128)) // triangle
				} else {
					positionSymbol = "+"
				}
			}
		} else {
			positionSymbol = "?"
			if ctrl, ok := ctx.Client.State.Controllers[trk.FlightPlan.TrackingController]; ok && ctrl != nil {
				if ctrl.Scope != "" {
					// Explicitly specified scope_char overrides everything.
					positionSymbol = ctrl.Scope
				} else if ctrl.FacilityIdentifier != "" {
					// For external facilities we use the facility id
					positionSymbol = ctrl.FacilityIdentifier
				} else {
					positionSymbol = ctrl.TCP[len(ctrl.TCP)-1:]
				}
			}
		}

		sp.drawTrack(trk, state, ctx, transforms, positionSymbol, trackBuilder, ld, trid, td)
	}

	transforms.LoadWindowViewingMatrices(cb)
	trackBuilder.GenerateCommands(cb)

	transforms.LoadLatLongViewingMatrices(cb)
	trid.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) beaconCodeSelected(code av.Squawk) bool {
	ps := sp.currentPrefs()
	for _, c := range ps.SelectedBeacons {
		if c <= 0o77 && c == code/0o100 {
			// check the entire code bank
			return true
		} else if c == code {
			return true
		}
	}
	return false
}

func (sp *STARSPane) getTrackSize(ctx *panes.Context, transforms ScopeTransformations) float32 {
	var size float32 = 13 // base track size
	e := transforms.PixelDistanceNM(ctx.NmPerLongitude)
	var distance float32 = 0.3623 // Around 2200 feet in nm
	if distance/e > 13 {
		size = distance / e
	}
	return size
}

func (sp *STARSPane) getGhostTracks(ctx *panes.Context, tracks []sim.Track) []*av.GhostTrack {
	var ghosts []*av.GhostTrack
	ps := sp.currentPrefs()
	now := ctx.Client.State.SimTime

	for i, pairState := range ps.CRDA.RunwayPairState {
		if !pairState.Enabled {
			continue
		}
		for j, rwyState := range pairState.RunwayState {
			if !rwyState.Enabled {
				continue
			}

			// Leader line direction comes from the scenario configuration, unless it
			// has been overridden for the runway via <multifunc>NL.
			leaderDirection := sp.ConvergingRunways[i].LeaderDirections[j]
			if rwyState.LeaderLineDirection != nil {
				leaderDirection = *rwyState.LeaderLineDirection
			}

			runwayIntersection := sp.ConvergingRunways[i].RunwayIntersection
			region := sp.ConvergingRunways[i].ApproachRegions[j]
			otherRegion := sp.ConvergingRunways[i].ApproachRegions[(j+1)%2]

			trackId := util.Select(pairState.Mode == CRDAModeStagger, sp.ConvergingRunways[i].StaggerSymbol,
				sp.ConvergingRunways[i].TieSymbol)

			offset := util.Select(pairState.Mode == CRDAModeTie, sp.ConvergingRunways[i].TieOffset, float32(0))

			nmPerLongitude := ctx.NmPerLongitude
			magneticVariation := ctx.MagneticVariation
			for _, trk := range tracks {
				state := sp.TrackState[trk.ADSBCallsign]
				if state.LostTrack(now) {
					continue
				}

				// Create a ghost track if appropriate, add it to the
				// ghosts slice, and draw its radar track.
				force := state.Ghost.State == GhostStateForced || ps.CRDA.ForceAllGhosts
				heading := util.Select(state.HaveHeading(), state.TrackHeading(nmPerLongitude),
					trk.Heading)

				sp := ""
				if trk.IsAssociated() {
					sp = trk.FlightPlan.Scratchpad
				}

				ghost := region.TryMakeGhost(trk.RadarTrack, heading, sp, force, offset, leaderDirection,
					runwayIntersection, nmPerLongitude, magneticVariation, otherRegion)
				if ghost != nil {
					ghost.TrackId = trackId
					ghosts = append(ghosts, ghost)
				}
			}
		}
	}

	return ghosts
}

func (sp *STARSPane) drawGhosts(ctx *panes.Context, ghosts []*av.GhostTrack, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.currentPrefs()
	brightness := ps.Brightness.OtherTracks
	color := brightness.ScaleRGB(STARSGhostColor)
	trackFont := sp.systemFont(ctx, ps.CharSize.PositionSymbols)
	trackStyle := renderer.TextStyle{Font: trackFont, Color: color, LineSpacing: 0}
	datablockFont := sp.systemFont(ctx, ps.CharSize.Datablocks)

	var strBuilder strings.Builder
	for _, ghost := range ghosts {
		state := sp.TrackState[ghost.ADSBCallsign]

		if state.Ghost.State == GhostStateSuppressed {
			continue
		}

		// The track is just the single character..
		pw := transforms.WindowFromLatLongP(ghost.Position)
		td.AddTextCentered(ghost.TrackId, pw, trackStyle)

		// Draw datablock
		db := sp.getGhostDatablock(ctx, ghost, color)
		pac := transforms.WindowFromLatLongP(ghost.Position)
		vll := sp.getLeaderLineVector(ctx, ghost.LeaderLineDirection)
		pll := math.Add2f(pac, vll)

		db.draw(td, pll, datablockFont, &strBuilder, brightness, ghost.LeaderLineDirection, ctx.Now.Unix())

		// Leader line
		ld.AddLine(pac, math.Add2f(pac, vll), color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawTrack(trk sim.Track, state *TrackState, ctx *panes.Context,
	transforms ScopeTransformations, positionSymbol string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	// TODO: orient based on radar center if just one radar

	pos := trk.Location
	isUnsupported := trk.Altitude == 0 && trk.FlightPlan != nil // FIXME: there's surely a better way to do this
	pw := transforms.WindowFromLatLongP(pos)
	// On high DPI windows displays we need to scale up the tracks

	primaryTargetBrightness := ps.Brightness.PrimarySymbols
	if primaryTargetBrightness > 0 && !isUnsupported {
		switch mode := sp.radarMode(ctx.FacilityAdaptation.RadarSites); mode {
		case RadarModeSingle:
			site := ctx.FacilityAdaptation.RadarSites[ps.RadarSiteSelected]
			primary, secondary, dist := site.CheckVisibility(pos, int(trk.Altitude))

			// Orient the box toward the radar
			h := math.Heading2LL(site.Position, pos, ctx.NmPerLongitude, ctx.MagneticVariation)
			rot := math.Rotator2f(h)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}

			// Scale box based on distance from the radar; TODO: what exactly should this be?
			scale := ctx.DrawPixelScale * float32(math.Clamp(dist/40, .5, 1.5))
			for i := range box {
				box[i] = math.Scale2f(box[i], scale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
			if primary {
				// Draw a filled box
				trid.AddQuad(box[0], box[1], box[2], box[3], color)
			} else if secondary {
				// If it's just a secondary return, only draw the box outline.
				// TODO: is this 40nm, or secondary?
				ld.AddLineLoop(color, box[:])
			}

			// green line
			line := [2][2]float32{[2]float32{-16, 3}, [2]float32{16, 3}}
			for i := range line {
				line[i] = math.Add2f(rot(math.Scale2f(line[i], scale)), pw)
				line[i] = transforms.LatLongFromWindowP(line[i])
			}
			ld.AddLine(line[0], line[1], primaryTargetBrightness.ScaleRGB(renderer.RGB{R: .1, G: .8, B: .1}))

		case RadarModeMulti:
			primary, secondary, _ := sp.radarVisibility(ctx.FacilityAdaptation.RadarSites,
				pos, int(trk.Altitude))
			// "cheat" by using trk.Heading if we don't yet have two radar tracks to compute the
			// heading with; this makes things look better when we first see a track or when
			// restarting a simulation...
			heading := util.Select(state.HaveHeading(),
				state.TrackHeading(ctx.NmPerLongitude)+ctx.MagneticVariation, trk.Heading)

			rot := math.Rotator2f(heading)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}
			for i := range box {
				box[i] = math.Scale2f(box[i], ctx.DrawPixelScale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
			if primary {
				// Draw a filled box
				trid.AddQuad(box[0], box[1], box[2], box[3], color)
			} else if secondary {
				// If it's just a secondary return, only draw the box outline.
				// TODO: is this 40nm, or secondary?
				ld.AddLineLoop(color, box[:])
			}

		case RadarModeFused:
			if ps.Brightness.PrimarySymbols > 0 {
				color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
				drawTrack(trackBuilder, pw, sp.fusedTrackVertices, color)
			}
		}
	}

	// Draw main track position symbol
	color, _, posBrightness := sp.trackDatablockColorBrightness(ctx, trk)
	if posBrightness > 0 {
		if positionSymbol != "" {
			font := sp.systemFont(ctx, ps.CharSize.PositionSymbols)
			outlineFont := sp.systemOutlineFont(ctx, ps.CharSize.PositionSymbols)
			pt := math.Add2f(pw, [2]float32{0.5, -0.5})
			td.AddTextCentered(positionSymbol, pt, renderer.TextStyle{Font: outlineFont, Color: renderer.RGB{}})

			posColor := posBrightness.ScaleRGB(color)
			td.AddTextCentered(positionSymbol, pt, renderer.TextStyle{Font: font, Color: posColor})
		} else {
			// TODO: draw box if in range of squawks we have selected

			// diagonals
			dx := transforms.LatLongFromWindowV([2]float32{1, 0})
			dy := transforms.LatLongFromWindowV([2]float32{0, 1})
			// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
			delta := func(p math.Point2LL, x, y float32) math.Point2LL {
				return math.Add2LL(p, math.Add2LL(math.Scale2f(dx, x), math.Scale2f(dy, y)))
			}

			px := 3 * ctx.DrawPixelScale
			// diagonals
			diagPx := px * 0.707107                                                 /* 1/sqrt(2) */
			trackColor := posBrightness.ScaleRGB(renderer.RGB{R: .1, G: .7, B: .1}) // TODO make a STARS... constant
			ld.AddLine(delta(pos, -diagPx, -diagPx), delta(pos, diagPx, diagPx), trackColor)
			ld.AddLine(delta(pos, diagPx, -diagPx), delta(pos, -diagPx, diagPx), trackColor)
			// horizontal line
			ld.AddLine(delta(pos, -px, 0), delta(pos, px, 0), trackColor)
			// vertical line
			ld.AddLine(delta(pos, 0, -px), delta(pos, 0, px), trackColor)
		}
	}
}

func drawTrack(ctd *renderer.ColoredTrianglesDrawBuilder, p [2]float32, vertices [][2]float32, color renderer.RGB) {
	for i := range vertices {
		v0, v1 := vertices[i], vertices[(i+1)%len(vertices)]
		ctd.AddTriangle(p, math.Add2f(p, v0), math.Add2f(p, v1), color)
	}
}

func getTrackVertices(ctx *panes.Context, diameter float32) [][2]float32 {
	// Figure out how many points to use to approximate the circle; use
	// more the bigger it is on the screen, but, sadly, not enough to get a
	// nice clean circle (matching real-world..)
	np := 8
	if diameter > 20 {
		np = util.Select(diameter <= 40, 16, 32)
	}

	// Prepare the points around the unit circle; rotate them by 1/2 their
	// angular spacing so that we have vertical and horizontal edges at the
	// sides (e.g., a octagon like a stop-sign with 8 points, rather than
	// having a vertex at the top of the circle.)
	rot := math.Rotator2f(360 / (2 * float32(np)))
	pts := util.MapSlice(math.CirclePoints(np), func(p [2]float32) [2]float32 { return rot(p) })

	// Scale the points based on the circle radius (and deal with the usual
	// Windows high-DPI borkage...)
	radius := ctx.DrawPixelScale * float32(int(diameter/2+0.5)) // round to integer
	pts = util.MapSlice(pts, func(p [2]float32) [2]float32 { return math.Scale2f(p, radius) })

	return pts
}

func (sp *STARSPane) drawHistoryTrails(ctx *panes.Context, tracks []sim.Track, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.History == 0 {
		// Don't draw if brightness == 0.
		return
	}

	historyBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(historyBuilder)

	const historyTrackDiameter = 8
	historyTrackVertices := getTrackVertices(ctx, historyTrackDiameter)

	now := ctx.Client.CurrentTime()
	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]

		if state.LostTrack(now) {
			continue
		}

		// Draw history from new to old
		for i := range ps.RadarTrackHistory {
			trackColorNum := math.Min(i, len(STARSTrackHistoryColors)-1)
			trackColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColors[trackColorNum])

			if idx := (state.historyTracksIndex - 1 - i) % len(state.historyTracks); idx >= 0 {
				if p := state.historyTracks[idx].Location; !p.IsZero() {
					drawTrack(historyBuilder, transforms.WindowFromLatLongP(p), historyTrackVertices,
						trackColor)
				}
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	historyBuilder.GenerateCommands(cb)
}

func (sp *STARSPane) WarnOutsideAirspace(ctx *panes.Context, trk sim.Track) ([][2]int, bool) {
	// Only report on ones that are tracked by us
	if trk.IsAssociated() && trk.FlightPlan.TrackingController != ctx.UserTCP {
		return nil, false
	}

	if trk.OnApproach {
		// No warnings once they're flying the approach
		return nil, false
	}

	state := sp.TrackState[trk.ADSBCallsign]
	vols := ctx.Client.ControllerAirspace(ctx.UserTCP)

	inside, alts := av.InAirspace(trk.Location, trk.Altitude, vols)
	if state.EnteredOurAirspace && !inside {
		return alts, true
	} else if inside {
		state.EnteredOurAirspace = true
	}
	return nil, false
}

func (sp *STARSPane) updateCAAircraft(ctx *panes.Context, tracks []sim.Track) {
	inCAInhibitVolumes := func(trk *sim.Track) bool {
		for _, vol := range ctx.Client.State.InhibitCAVolumes() {
			if vol.Inside(trk.Location, int(trk.Altitude)) {
				return true
			}
		}
		return false
	}

	tracked, untracked := make(map[av.ADSBCallsign]sim.Track), make(map[av.ADSBCallsign]sim.Track)
	for _, trk := range tracks {
		if trk.IsAirborne {
			continue
		}
		if trk.IsAssociated() {
			tracked[trk.ADSBCallsign] = trk
		} else {
			untracked[trk.ADSBCallsign] = trk
		}
	}

	nmPerLongitude := ctx.NmPerLongitude
	caConflict := func(callsigna, callsignb av.ADSBCallsign) bool {
		// No CA if we don't have proper mode-C altitude for both.
		trka, oka := ctx.GetTrackByCallsign(callsigna)
		trkb, okb := ctx.GetTrackByCallsign(callsignb)
		if !oka || !okb {
			return false
		}

		// Both must be associated
		if trka.IsUnassociated() || trkb.IsUnassociated() {
			return false
		}
		if trka.FlightPlan.InhibitModeCAltitudeDisplay || trkb.FlightPlan.InhibitModeCAltitudeDisplay {
			return false
		}
		if trka.Mode != av.TransponderModeAltitude || trkb.Mode != av.TransponderModeAltitude {
			return false
		}
		if trka.FlightPlan.DisableCA || trkb.FlightPlan.DisableCA {
			return false
		}

		// Quick outs before more expensive checks: using approximate
		// distance; don't bother if they're >10nm apart or have >5000'
		// vertical separation.
		if math.Abs(trka.Altitude-trkb.Altitude) > 5000 ||
			math.NMLength2LL(math.Sub2f(trka.Location, trkb.Location), nmPerLongitude) > 10 {
			return false
		}

		// No CA if they're in the same ATPA volume; let the ATPA monitor take it
		va, vb := trka.ATPAVolume, trkb.ATPAVolume
		if va != nil && vb != nil && va.Id == vb.Id {
			return false
		}

		if inCAInhibitVolumes(trka) || inCAInhibitVolumes(trkb) {
			return false
		}

		return math.NMDistance2LL(trka.Location, trkb.Location) <= LateralMinimum &&
			math.Abs(trka.Altitude-trkb.Altitude) <= VerticalMinimum-5 && /*small slop for fp error*/
			!sp.diverging(ctx, trka, trkb)
	}

	// Assume that the second one is the untracked one.
	mciConflict := func(callsigna, callsignb av.ADSBCallsign) bool {
		trka, oka := ctx.GetTrackByCallsign(callsigna)
		trkb, okb := ctx.GetTrackByCallsign(callsignb)
		if !oka || !okb {
			return false
		}
		if trka.IsAssociated() && trka.FlightPlan.DisableCA {
			return false
		}
		// No CA if we don't have proper mode-C altitude for both.
		if trka.IsAssociated() && trka.FlightPlan.InhibitModeCAltitudeDisplay {
			return false
		}
		if trka.Mode != av.TransponderModeAltitude || trkb.Mode != av.TransponderModeAltitude {
			return false
		}

		// Is this beacon code suppressed for this aircraft?
		if trka.IsAssociated() && trka.FlightPlan.MCISuppressedCode == trkb.Squawk {
			return false
		}

		// Quick outs before more expensive checks: using approximate
		// distance; don't bother if they're >10nm apart or have >5000'
		// vertical separation.
		if math.Abs(trka.Altitude-trkb.Altitude) > 5000 ||
			math.NMLength2LL(math.Sub2f(trka.Location, trkb.Location), nmPerLongitude) > 10 {
			return false
		}

		if inCAInhibitVolumes(trka) || inCAInhibitVolumes(trkb) {
			return false
		}

		return math.NMDistance2LL(trka.Location, trkb.Location) <= 1.5 &&
			math.Abs(trka.Altitude-trkb.Altitude) <= 500-5 && /*small slop for fp error*/
			!sp.diverging(ctx, trka, trkb)
	}

	// Remove ones that no longer exist
	sp.CAAircraft = util.FilterSliceInPlace(sp.CAAircraft, func(ca CAAircraft) bool {
		_, ok0 := tracked[ca.ADSBCallsigns[0]]
		_, ok1 := tracked[ca.ADSBCallsigns[1]]
		return ok0 && ok1
	})
	sp.MCIAircraft = util.FilterSliceInPlace(sp.MCIAircraft, func(ca CAAircraft) bool {
		_, ok0 := tracked[ca.ADSBCallsigns[0]]
		_, ok1 := untracked[ca.ADSBCallsigns[1]]
		return ok0 && ok1
	})

	// Remove ones that are no longer conflicting
	sp.CAAircraft = util.FilterSliceInPlace(sp.CAAircraft, func(ca CAAircraft) bool {
		return caConflict(ca.ADSBCallsigns[0], ca.ADSBCallsigns[1])
	})
	sp.MCIAircraft = util.FilterSliceInPlace(sp.MCIAircraft, func(ca CAAircraft) bool {
		return mciConflict(ca.ADSBCallsigns[0], ca.ADSBCallsigns[1])
	})

	// Add new conflicts; by appending we keep them sorted by when they
	// were first detected...
	for cs0 := range tracked {
		for cs1 := range tracked {
			if cs0 >= cs1 { // alphabetically-ordered callsign pair
				continue
			}
			if slices.ContainsFunc(sp.CAAircraft, func(ca CAAircraft) bool {
				return cs0 == ca.ADSBCallsigns[0] && cs1 == ca.ADSBCallsigns[1]
			}) {
				continue
			}
			if caConflict(cs0, cs1) {
				sp.CAAircraft = append(sp.CAAircraft, CAAircraft{
					ADSBCallsigns: [2]av.ADSBCallsign{cs0, cs1},
					SoundEnd:      ctx.Now.Add(AlertAudioDuration),
					Start:         time.Now(), // this rather than ctx.Now so they are unique and sort consistently for the list.
				})
			}
		}

		for cs1 := range untracked {
			if slices.ContainsFunc(sp.MCIAircraft, func(ca CAAircraft) bool {
				return cs0 == ca.ADSBCallsigns[0] && cs1 == ca.ADSBCallsigns[1]
			}) {
				continue
			}
			if mciConflict(cs0, cs1) {
				sp.MCIAircraft = append(sp.MCIAircraft, CAAircraft{
					ADSBCallsigns: [2]av.ADSBCallsign{cs0, cs1},
					SoundEnd:      ctx.Now.Add(AlertAudioDuration),
					Start:         time.Now(), // this rather than ctx.Now so they are unique and sort consistently for the list.
				})
			}
		}
	}
}

func (sp *STARSPane) updateInTrailDistance(ctx *panes.Context, tracks []sim.Track) {
	nmPerLongitude := ctx.NmPerLongitude
	magneticVariation := ctx.MagneticVariation

	// Zero out the previous distance
	for _, trk := range tracks {
		state := sp.TrackState[trk.ADSBCallsign]
		state.IntrailDistance = 0
		state.MinimumMIT = 0
		state.ATPAStatus = ATPAStatusUnset
		state.ATPALeadAircraftCallsign = ""
	}

	// For simplicity, we always compute all of the necessary distances
	// here, regardless of things like both ps.DisplayATPAWarningAlertCones
	// and ps.DisplayATPAMonitorCones being disabled. Later, when it's time
	// to display things (or not), we account for both that as well as all
	// of the potential per-aircraft overrides. This does mean that
	// sometimes the work here is fully wasted.

	// We basically want to loop over each active volume and process all of
	// the aircraft inside it together. There's no direct way to iterate
	// over them, so we'll instead loop over aircraft and when we find one
	// that's inside a volume that hasn't been processed, process all
	// aircraft inside it and then mark the volume as completed.
	handledVolumes := make(map[string]interface{})

	for _, trk := range tracks {
		vol := trk.ATPAVolume
		if vol == nil {
			continue
		}
		if _, ok := handledVolumes[vol.Id]; ok {
			continue
		}

		// Get all aircraft on approach to this runway
		runwayAircraft := util.FilterSlice(tracks, func(trk sim.Track) bool {
			if v := trk.ATPAVolume; v == nil || v.Id != vol.Id {
				return false
			}

			// Excluded scratchpad -> aircraft doesn't participate in the
			// party whatsoever.
			if trk.IsAssociated() && trk.FlightPlan.Scratchpad != "" &&
				slices.Contains(vol.ExcludedScratchpads, trk.FlightPlan.Scratchpad) {
				return false
			}

			state := sp.TrackState[trk.ADSBCallsign]
			return vol.Inside(trk.Location, trk.Altitude,
				state.TrackHeading(nmPerLongitude)+magneticVariation,
				nmPerLongitude, magneticVariation)
		})

		// Sort by distance to threshold (there will be some redundant
		// lookups of STARSAircraft state et al. here, but it's
		// straightforward to implement it like this.)
		sort.Slice(runwayAircraft, func(i, j int) bool {
			pi := sp.TrackState[runwayAircraft[i].ADSBCallsign].track.Location
			pj := sp.TrackState[runwayAircraft[j].ADSBCallsign].track.Location
			return math.NMDistance2LL(pi, vol.Threshold) < math.NMDistance2LL(pj, vol.Threshold)
		})

		for i := range runwayAircraft {
			if i == 0 {
				// The first one doesn't have anyone in front...
				continue
			}
			leading, trailing := runwayAircraft[i-1], runwayAircraft[i]
			leadingState, trailingState := sp.TrackState[leading.ADSBCallsign], sp.TrackState[trailing.ADSBCallsign]
			trailingState.IntrailDistance =
				math.NMDistance2LL(leadingState.track.Location, trailingState.track.Location)
			sp.checkInTrailCwtSeparation(ctx, trailing, leading)
		}
		handledVolumes[vol.Id] = nil
	}
}

type ModeledAircraft struct {
	callsign     av.ADSBCallsign
	p            [2]float32 // nm coords
	v            [2]float32 // nm, normalized
	gs           float32
	alt          float32
	dalt         float32    // per second
	threshold    [2]float32 // nm
	landingSpeed float32
}

func MakeModeledAircraft(ctx *panes.Context, trk sim.Track, state *TrackState, threshold math.Point2LL) ModeledAircraft {
	nmPerLongitude := ctx.NmPerLongitude
	magneticVariation := ctx.MagneticVariation

	ma := ModeledAircraft{
		callsign:  trk.ADSBCallsign,
		p:         math.LL2NM(trk.Location, nmPerLongitude),
		gs:        trk.Groundspeed,
		alt:       trk.Altitude,
		dalt:      float32(state.TrackDeltaAltitude()),
		threshold: math.LL2NM(threshold, nmPerLongitude),
	}
	// Note: assuming it's associated...
	if perf, ok := av.DB.AircraftPerformance[trk.FlightPlan.AircraftType]; ok {
		ma.landingSpeed = perf.Speed.Landing
	} else {
		ma.landingSpeed = 120 // ....
	}
	ma.v = state.HeadingVector(nmPerLongitude, magneticVariation)
	ma.v = math.LL2NM(ma.v, nmPerLongitude)
	ma.v = math.Normalize2f(ma.v)
	return ma
}

// estimated altitude s seconds in the future
func (ma *ModeledAircraft) EstimatedAltitude(s float32) float32 {
	// simple linear model
	return ma.alt + s*ma.dalt
}

// Return estimated position 1s in the future
func (ma *ModeledAircraft) NextPosition(p [2]float32) [2]float32 {
	gs := ma.gs // current speed
	td := math.Distance2f(p, ma.threshold)
	if td < 2 {
		gs = math.Min(gs, ma.landingSpeed)
	} else if td < 5 {
		t := (td - 2) / 3 // [0,1]
		// lerp from current speed down to landing speed
		gs = math.Lerp(t, ma.landingSpeed, gs)
	}

	gs /= 3600 // nm / second
	return math.Add2f(p, math.Scale2f(ma.v, gs))
}

func (sp *STARSPane) checkInTrailCwtSeparation(ctx *panes.Context, back, front sim.Track) {
	if front.IsUnassociated() && back.IsUnassociated() {
		return
	}
	cwtSeparation := av.CWTApproachSeparation(front.FlightPlan.CWTCategory, back.FlightPlan.CWTCategory)

	state := sp.TrackState[back.ADSBCallsign]
	vol := back.ATPAVolume
	if cwtSeparation == 0 {
		cwtSeparation = float32(LateralMinimum)

		// 7110.126B replaces 7110.65Z 5-5-4(j), which is now 7110.65AA 5-5-4(i)
		// Reduced separation allowed 10 NM out (also enabled for the ATPA volume)
		if vol.Enable25nmApproach &&
			math.NMDistance2LL(vol.Threshold, back.Location) < vol.Dist25nmApproach {

			// between aircraft established on the final approach course
			// Note 1: checked with OnExtendedCenterline since reduced separation probably
			// doesn't apply to approaches with curved final approach segment
			// Note 2: 0.2 NM is slightly less than full-scale deflection at 5 NM out
			if back.OnExtendedCenterline && front.OnExtendedCenterline {
				// Not-implemented: Required separation must exist prior to applying 2.5 NM separation (TBL 5-5-2)
				cwtSeparation = 2.5
			}
		}
	}

	state.MinimumMIT = cwtSeparation
	state.ATPALeadAircraftCallsign = front.ADSBCallsign
	state.ATPAStatus = ATPAStatusMonitor // baseline

	// If the aircraft's scratchpad is filtered, then it doesn't get
	// warnings or alerts but is still here for the aircraft behind it.
	if back.IsAssociated() && back.FlightPlan.Scratchpad != "" &&
		slices.Contains(vol.FilteredScratchpads, back.FlightPlan.Scratchpad) {
		return
	}

	// front, back aircraft
	frontModel := MakeModeledAircraft(ctx, front, sp.TrackState[front.ADSBCallsign], vol.Threshold)
	backModel := MakeModeledAircraft(ctx, back, state, vol.Threshold)

	// Will there be a MIT violation s seconds in the future?  (Note that
	// we don't include altitude separation here since what we need is
	// distance separation by the threshold...)
	frontPosition, backPosition := frontModel.p, backModel.p
	for s := 0; s < 45; s++ {
		frontPosition, backPosition = frontModel.NextPosition(frontPosition), backModel.NextPosition(backPosition)
		distance := math.Distance2f(frontPosition, backPosition)
		if distance < cwtSeparation { // no bueno
			if s <= 24 {
				// Error if conflict expected within 24 seconds (6-159).
				state.ATPAStatus = ATPAStatusAlert
				return
			} else {
				// Warning if conflict expected within 45 seconds (6-159).
				state.ATPAStatus = ATPAStatusWarning
				return
			}
		}
	}
}

func (sp *STARSPane) diverging(ctx *panes.Context, a, b *sim.Track) bool {
	nmPerLongitude := ctx.NmPerLongitude
	magneticVariation := ctx.MagneticVariation

	sa, sb := sp.TrackState[a.ADSBCallsign], sp.TrackState[b.ADSBCallsign]

	pa := math.LL2NM(a.Location, nmPerLongitude)
	da := math.LL2NM(sa.HeadingVector(nmPerLongitude, magneticVariation), nmPerLongitude)
	pb := math.LL2NM(b.Location, nmPerLongitude)
	db := math.LL2NM(sb.HeadingVector(nmPerLongitude, magneticVariation), nmPerLongitude)

	pint, ok := math.LineLineIntersect(pa, math.Add2f(pa, da), pb, math.Add2f(pb, db))
	if !ok {
		// This generally happens at the start when we don't have a valid
		// track heading vector yet.
		return false
	}

	if math.Dot(da, math.Sub2f(pint, pa)) > 0 && math.Dot(db, math.Sub2f(pint, pb)) > 0 {
		// intersection is in front of one of them
		return false
	}

	// Intersection behind both; make sure headings are at least 15 degrees apart.
	return math.HeadingDifference(sa.TrackHeading(nmPerLongitude), sb.TrackHeading(nmPerLongitude)) >= 15
}

func (sp *STARSPane) drawLeaderLines(ctx *panes.Context, tracks []sim.Track, dbs map[av.ADSBCallsign]datablock,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	tsz := sp.getTrackSize(ctx, transforms)
	draw := func(tracks []sim.Track) {
		for _, trk := range tracks {
			if trk.IsAssociated() && trk.FlightPlan.Suspended {
				// no leader line for suspended
				continue
			}

			if db := dbs[trk.ADSBCallsign]; db != nil {
				baseColor, brightness, _ := sp.trackDatablockColorBrightness(ctx, trk)
				pac := transforms.WindowFromLatLongP(trk.Location)
				v := sp.getLeaderLineVector(ctx, sp.getLeaderLineDirection(ctx, trk))
				// Offset the starting point to the edge of the track circle;
				// this doesn't matter when we're drawing the circle but is
				// helpful for unsupported DBs.
				p0 := math.Add2f(pac, math.Scale2f(math.Normalize2f(v), tsz/2))
				v = math.Scale2f(v, ctx.DrawPixelScale)
				ld.AddLine(p0, math.Add2f(pac, v), brightness.ScaleRGB(baseColor))
			}
		}
	}

	draw(tracks)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) getLeaderLineDirection(ctx *panes.Context, trk sim.Track) math.CardinalOrdinalDirection {
	ps := sp.currentPrefs()
	state := sp.TrackState[trk.ADSBCallsign]

	if trk.IsAssociated() {
		sfp := trk.FlightPlan
		if sfp.Suspended {
			// Suspended are always north, evidently.
			return math.North
		} else if state.UseGlobalLeaderLine {
			return *sfp.GlobalLeaderLineDirection
		} else if state.LeaderLineDirection != nil {
			// The direction was specified for the aircraft specifically
			return *state.LeaderLineDirection
		} else if sfp.TrackingController == ctx.UserTCP {
			// Tracked by us
			return ps.LeaderLineDirection
		} else if sfp.HandoffTrackController == ctx.UserTCP {
			// Being handed off to us
			return ps.LeaderLineDirection
		} else if dir, ok := ps.ControllerLeaderLineDirections[sfp.TrackingController]; ok {
			// Tracked by another controller for whom a direction was specified
			return dir
		} else if ps.OtherControllerLeaderLineDirection != nil {
			// Tracked by another controller without a per-controller direction specified
			return *ps.OtherControllerLeaderLineDirection
		}
	} else { // unassociated
		if state.LeaderLineDirection != nil {
			// The direction was specified for the aircraft specifically
			return *state.LeaderLineDirection
		} else if ps.UnassociatedLeaderLineDirection != nil {
			return *ps.UnassociatedLeaderLineDirection
		}
	}

	// TODO: should this case have a user-specifiable default?
	return math.CardinalOrdinalDirection(math.North)
}

func (sp *STARSPane) getLeaderLineVector(ctx *panes.Context, dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	ps := sp.currentPrefs()
	pxLengths := []float32{0, 17, 32, 47, 62, 77, 114, 152}
	idx := min(ps.LeaderLineLength, len(pxLengths)-1)
	return math.Scale2f(v, pxLengths[idx])
}

func (sp *STARSPane) radarVisibility(radarSites map[string]*av.RadarSite, pos math.Point2LL, alt int) (primary, secondary bool, distance float32) {
	prefs := sp.currentPrefs()
	distance = 1e30
	single := sp.radarMode(radarSites) == RadarModeSingle
	for id, site := range radarSites {
		if single && prefs.RadarSiteSelected != id {
			continue
		}

		if p, s, dist := site.CheckVisibility(pos, alt); p || s {
			primary = primary || p
			secondary = secondary || s
			distance = math.Min(distance, dist)
		}
	}

	return
}

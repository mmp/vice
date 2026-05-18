// stars/track.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type TrackState struct {
	// Independently of the track history, we store the most recent track
	// from the sensor as well as the previous one. This gives us the
	// freshest possible information for things like calculating headings,
	// rates of altitude change, etc.
	track             av.RadarTrack
	trackTime         sim.Time
	previousTrack     av.RadarTrack
	previousTrackTime sim.Time

	// Radar track history is maintained with a ring buffer where
	// historyTracksIndex is the index of the next track to be written.
	// (Thus, historyTracksIndex==0 implies that there are no tracks.)
	// Changing to/from FUSED mode causes tracksIndex to be reset, thus
	// discarding previous tracks.
	historyTracks      [10]av.RadarTrack
	historyTracksIndex int
}

// ATPAStatus now lives in sim (see sim/atpa.go) because it is computed
// server-side and published via sim.Track. The alias + const re-exports
// keep existing stars.ATPAStatus callers working.
type ATPAStatus = sim.ATPAStatus

const (
	ATPAStatusUnset   = sim.ATPAStatusUnset
	ATPAStatusMonitor = sim.ATPAStatusMonitor
	ATPAStatusWarning = sim.ATPAStatusWarning
	ATPAStatusAlert   = sim.ATPAStatusAlert
)

// GhostState now lives in sim (see sim/track_ghost.go) because it is
// part of per-ACID annotations that are synchronized server-side. The
// alias + const re-exports keep existing stars.GhostState callers working.
type GhostState = sim.GhostState

const (
	GhostStateRegular    = sim.GhostStateRegular
	GhostStateSuppressed = sim.GhostStateSuppressed
	GhostStateForced     = sim.GhostStateForced
)

func (ts *TrackState) TrackDeltaAltitude() int {
	if ts.previousTrack.Location.IsZero() {
		// No previous track
		return 0
	}
	return int(ts.track.TransponderAltitude - ts.previousTrack.TransponderAltitude)
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

func (ts *TrackState) TrackHeading(nmPerLongitude float32) math.TrueHeading {
	if !ts.HaveHeading() {
		return 0
	}
	return math.Heading2LL(ts.previousTrack.Location, ts.track.Location, nmPerLongitude)
}

func (sp *STARSPane) trackStateForACID(ctx *panes.Context, acid sim.ACID) (*TrackState, bool) {
	// Figure out the ADSB callsign for this ACID.
	for _, trk := range sp.visibleTracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			s, ok := sp.TrackState[trk.ADSBCallsign]
			return s, ok
		}
	}
	return nil, false
}

// annotations returns the shared TCW annotations for the given
// callsign, or a zero-value TrackAnnotations if no entry exists.
// Callers read fields unconditionally; the zero value is the semantic
// default for every synced field.
func (sp *STARSPane) annotations(ctx *panes.Context, callsign av.ADSBCallsign) sim.TrackAnnotations {
	d := ctx.Client.State.TCWDisplay
	if d == nil || d.Annotations == nil {
		return sim.TrackAnnotations{}
	}
	return d.Annotations[callsign]
}

// callsignForACID resolves an ACID to its ADSBCallsign via State.GetTrackByACID.
// Returns ("", false) if no associated track is found.
func (sp *STARSPane) callsignForACID(ctx *panes.Context, acid sim.ACID) (av.ADSBCallsign, bool) {
	if trk, ok := ctx.Client.State.GetTrackByACID(acid); ok {
		return trk.ADSBCallsign, true
	}
	return "", false
}

// annotationsForTrack returns the shared TCW annotations for a track.
// ADSBCallsign is always present on a track (associated or not) so no
// IsAssociated guard is needed.
func (sp *STARSPane) annotationsForTrack(ctx *panes.Context, trk sim.Track) sim.TrackAnnotations {
	return sp.annotations(ctx, trk.ADSBCallsign)
}

func (sp *STARSPane) processEvents(ctx *panes.Context) {
	for i := range ctx.Client.State.ATIS {
		if sp.LastATIS[i] != ctx.Client.State.ATIS[i] || sp.LastGIText[i] != ctx.Client.State.GIText[i] {
			// Don't flash the controller's own edit when its RPC response or the
			// next world update brings the shared state back to this pane.
			if pending := sp.pendingATISGITextUpdate[i]; pending.Valid &&
				pending.ExpectedATIS == ctx.Client.State.ATIS[i] &&
				pending.ExpectedGIText == ctx.Client.State.GIText[i] {
				sp.FlashATIS[i] = false
				sp.clearPendingATISGITextUpdate(i)
			} else {
				sp.FlashATIS[i] = ctx.FacilityAdaptation.Lists.SSA.FlashOnATISUpdate
			}
			sp.LastATIS[i] = ctx.Client.State.ATIS[i]
			sp.LastGIText[i] = ctx.Client.State.GIText[i]
		}
	}

	// First handle changes in sim.State.Tracks
	for _, trk := range ctx.Client.State.Tracks {
		if _, ok := sp.TrackState[trk.ADSBCallsign]; !ok {
			// First we've seen it; create the *AircraftState for it
			sp.TrackState[trk.ADSBCallsign] = &TrackState{}
			if trk.IsAssociated() && trk.FlightPlan.GlobalLeaderLineDirection != nil {
				anno := sp.annotations(ctx, trk.ADSBCallsign)
				anno.UseGlobalLeaderLine = true
				ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
					func(err error) { sp.displayError(err, ctx, "") })
			}
		}

		if ok, _ := trk.Squawk.IsSPC(); ok && !sp.annotations(ctx, trk.ADSBCallsign).SPCAlert {
			// First we've seen it squawking the SPC
			// TODO(shared-tcw-display): move SPC detection server-side so the annotation can be shared.
			anno := sp.annotations(ctx, trk.ADSBCallsign)
			anno.SPCAlert = true
			anno.SPCAcknowledged = false
			anno.SPCSoundEnd = ctx.SimTime.Add(AlertAudioDuration)
			ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
				func(err error) { sp.displayError(err, ctx, "") })
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
		// For all tracks (including "__" prefixed fake tracks for unsupported DBs),
		// delete TrackState if the track no longer exists in State.Tracks
		if _, ok := ctx.GetTrackByCallsign(callsign); !ok {
			delete(sp.TrackState, callsign)
		}
	}

	// Look for duplicate beacon codes
	sp.DuplicateBeacons = make(map[av.Squawk]any)
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
				// Note: PointOutAcknowledged is written server-side in
				// Sim.AcknowledgePointOut (see commit 75cecb9a); the client
				// only handles the flash timer on the sender.
				if ctx.UserControlsPosition(tcps.From) {
					if cs, ok := sp.callsignForACID(ctx, event.ACID); ok {
						anno := sp.annotations(ctx, cs)
						anno.POFlashingEndTime = ctx.SimTime.Add(5 * time.Second)
						ctx.Client.SetTrackAnnotations(cs, anno,
							func(err error) { sp.displayError(err, ctx, "") })
					}
				}
				delete(sp.PointOuts, event.ACID)
			}

		case sim.RecalledPointOutEvent:
			delete(sp.PointOuts, event.ACID)

		case sim.RejectedPointOutEvent:
			if tcps, ok := sp.PointOuts[event.ACID]; ok && ctx.UserControlsPosition(tcps.From) {
				sp.RejectedPointOuts[event.ACID] = nil
				if cs, ok := sp.callsignForACID(ctx, event.ACID); ok {
					anno := sp.annotations(ctx, cs)
					anno.UNFlashingEndTime = ctx.SimTime.Add(5 * time.Second)
					ctx.Client.SetTrackAnnotations(cs, anno,
						func(err error) { sp.displayError(err, ctx, "") })
				}
			}
			delete(sp.PointOuts, event.ACID)

		case sim.FlightPlanAssociatedEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil {
				if ctx.UserOwnsFlightPlan(fp) {
					if trk, ok := ctx.Client.State.GetTrackByACID(event.ACID); ok {
						anno := sp.annotationsForTrack(ctx, *trk)
						anno.DisplayFDB = true
						if fp.QuickFlightPlan {
							anno.DatablockAlert = true // display in yellow until slewed
						}
						ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
							func(err error) { sp.displayError(err, ctx, "") })
					}
				}
			}

		case sim.OfferedHandoffEvent:
			if ctx.UserControlsPosition(event.ToController) {
				sp.playOnce(ctx.Platform, AudioInboundHandoff)
			}

		case sim.AcceptedHandoffEvent, sim.AcceptedRedirectedHandoffEvent:
			if cs, ok := sp.callsignForACID(ctx, event.ACID); ok {
				outbound := ctx.UserControlsPosition(event.FromController) && !ctx.UserControlsPosition(event.ToController)
				inbound := !ctx.UserControlsPosition(event.FromController) && ctx.UserControlsPosition(event.ToController)

				// Collect annotation changes so we issue a single
				// read-modify-write per logical event.
				anno := sp.annotations(ctx, cs)
				dirty := false

				if outbound {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					// Note: OutboundHandoffAccepted + OutboundHandoffFlashEnd are
					// written server-side in Sim.AcceptHandoff (see commit 75cecb9a).
					anno.DisplayFDB = true
					dirty = true

					if event.Type == sim.AcceptedRedirectedHandoffEvent {
						anno.RDIndicatorEnd = ctx.SimTime.Add(30 * time.Second)
					}
				}
				if outbound || inbound {
					otherPos := util.Select(outbound, event.ToController, event.FromController)
					if otherCtrl := ctx.GetResolvedController(otherPos); otherCtrl != nil && otherCtrl.IsExternal() {
						anno.AcceptedHandoffSector = string(otherPos)
						dur := time.Duration(ctx.FacilityAdaptation.Datablocks.FDB.SectorDisplayDuration) * time.Second
						anno.AcceptedHandoffDisplayEnd = ctx.SimTime.Add(dur)
						dirty = true
					}
				}

				if dirty {
					ctx.Client.SetTrackAnnotations(cs, anno,
						func(err error) { sp.displayError(err, ctx, "") })
				}
			}
			// Clean up if a point out was instead taken as a handoff.
			delete(sp.PointOuts, event.ACID)

		case sim.SetGlobalLeaderLineEvent:
			if fp := ctx.Client.State.GetFlightPlanForACID(event.ACID); fp != nil {
				if trk, ok := ctx.Client.State.GetTrackByACID(event.ACID); ok {
					anno := sp.annotationsForTrack(ctx, *trk)
					anno.UseGlobalLeaderLine = fp.GlobalLeaderLineDirection != nil
					ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
						func(err error) { sp.displayError(err, ctx, "") })
				}
			}

		case sim.FDAMLeaderLineEvent:
			if ctx.UserControlsPosition(event.ToController) {
				if trk, ok := ctx.Client.State.GetTrackByACID(event.ACID); ok {
					anno := sp.annotationsForTrack(ctx, *trk)
					anno.FDAMLeaderLineDirection = event.LeaderLineDirection
					ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
						func(err error) { sp.displayError(err, ctx, "") })
				}
			}

		case sim.ForceQLEvent:
			if ctx.UserControlsPosition(event.ToController) {
				if sp.ForceQLACIDs == nil {
					sp.ForceQLACIDs = make(map[sim.ACID]any)
				}
				sp.ForceQLACIDs[event.ACID] = nil
			}

		case sim.TransferRejectedEvent:
			if cs, ok := sp.callsignForACID(ctx, event.ACID); ok {
				anno := sp.annotations(ctx, cs)
				anno.IFFlashing = true
				ctx.Client.SetTrackAnnotations(cs, anno,
					func(err error) { sp.displayError(err, ctx, "") })
				ctx.Client.CancelHandoff(event.ACID, func(err error) { sp.displayError(err, ctx, "") })
			}

		case sim.TransferAcceptedEvent:
			if cs, ok := sp.callsignForACID(ctx, event.ACID); ok {
				anno := sp.annotations(ctx, cs)
				anno.IFFlashing = false
				ctx.Client.SetTrackAnnotations(cs, anno,
					func(err error) { sp.displayError(err, ctx, "") })
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

	// Quick Look Positions - use OwningTCW to determine which tracks to show.
	_, ok := sp.currentPrefs().QuickLookTCPs[string(ctx.PrimaryTCPForTCW(trk.FlightPlan.OwningTCW))]
	return ok
}

// TODO(shared-tcw-display): move MSAW detection server-side so the annotation can be shared.
func (sp *STARSPane) updateMSAWs(ctx *panes.Context) {
	for _, trk := range sp.visibleTracks {
		state := sp.TrackState[trk.ADSBCallsign]
		cur := sp.annotationsForTrack(ctx, trk)

		// Compute whether the track is in an MSAW warning state. Any
		// early-out clears MSAW; the suppression filters / altitude
		// checks use the same shared anno snapshot.
		warn := false
		if trk.MVAsApply && trk.IsAssociated() {
			pilotAlt := trk.FlightPlan.PilotReportedAltitude
			modeCUsable := !(trk.FlightPlan.InhibitModeCAltitudeDisplay || trk.Mode != av.TransponderModeAltitude)
			if modeCUsable || pilotAlt != 0 {
				alt := util.Select(pilotAlt != 0, pilotAlt, int(trk.TransponderAltitude))

				msawFilter := ctx.Client.State.FacilityAdaptation.Filters.InhibitMSAW
				if !msawFilter.Inside(state.track.Location, alt) {
					mva := sp.mvaGrid.GetMVA(state.track.Location)
					warn = mva > 0 && alt < mva
				}
			}
		}

		cb := func(err error) { sp.displayError(err, ctx, "") }

		// Compare against the current snapshot and issue per-field
		// writes only for fields that actually changed this tick.
		// Routing through per-field setters (rather than a single
		// SetTrackAnnotations whole-struct push) avoids clobbering
		// server-driven transitions (handoff accept, pointout ack,
		// etc.) that may have mutated neighboring annotation fields
		// between 1 Hz state-update polls.
		if !warn && cur.InhibitMSAW {
			// The warning has cleared, so the inhibit is disabled (p.7-25)
			ctx.Client.SetTrackInhibitMSAW(trk.ADSBCallsign, false, cb)
		}
		if warn && !cur.MSAW {
			// It's a new alert
			ctx.Client.SetTrackMSAWAcknowledged(trk.ADSBCallsign, false, cb)
			ctx.Client.SetTrackMSAWSoundEnd(trk.ADSBCallsign, ctx.SimTime.Add(AlertAudioDuration), cb)
			ctx.Client.SetTrackMSAWStart(trk.ADSBCallsign, ctx.SimTime, cb)
		}
		if warn != cur.MSAW {
			ctx.Client.SetTrackMSAW(trk.ADSBCallsign, warn, cb)
		}
	}
}

func (sp *STARSPane) updateRadarTracks(ctx *panes.Context) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	fa := ctx.Client.State.FacilityAdaptation
	if sp.radarMode(fa.RadarSites) == RadarModeFused {
		if ctx.SimTime.Sub(sp.lastTrackUpdate) < 1*time.Second {
			return
		}
	} else {
		if ctx.SimTime.Sub(sp.lastTrackUpdate) < 5*time.Second {
			return
		}
	}
	sp.lastTrackUpdate = ctx.SimTime

	for _, trk := range sp.visibleTracks {
		state := sp.TrackState[trk.ADSBCallsign]

		if trk.TypeOfFlight == av.FlightTypeDeparture && trk.IsTentative && !state.trackTime.IsZero() {
			// Get the first track for tentative tracks but then don't
			// update any further until it's no longer tentative.
			continue
		}

		state.previousTrack = state.track
		state.previousTrackTime = state.trackTime
		state.track = trk.RadarTrack
		state.trackTime = ctx.SimTime

		// UnreasonableModeC is computed server-side by (*sim.Sim).updateModeC
		// and read from trk.UnreasonableModeC where needed.
	}

	// Check quicklook regions
	sp.updateQuicklookRegionTracks(ctx)

	// Update low altitude alerts now that we have updated tracks
	sp.updateMSAWs(ctx)

	// History tracks are updated after a radar track update, only if
	// H_RATE seconds have elapsed (4-94).
	ps := sp.currentPrefs()
	if ctx.SimTime.Sub(sp.lastHistoryTrackUpdate).Seconds() >= float64(ps.RadarTrackHistoryRate) {
		sp.lastHistoryTrackUpdate = ctx.SimTime
		for _, trk := range sp.visibleTracks { // We only get radar tracks for visible aircraft
			state := sp.TrackState[trk.ADSBCallsign]
			if trk.IsTentative {
				// No history tracks for tentative
				continue
			}
			if trk.TypeOfFlight == av.FlightTypeDeparture && !trk.IsTentative && state.previousTrackTime.IsZero() {
				// First tick after it's no longer tentative. Don't update history with the tentative track.
				continue
			}

			idx := state.historyTracksIndex % len(state.historyTracks)
			state.historyTracks[idx] = state.track
			state.historyTracksIndex++
		}
	}

	sp.updateCAAircraft(ctx)
}

func (sp *STARSPane) updateQuicklookRegionTracks(ctx *panes.Context) {
	ps := sp.currentPrefs()
	fa := ctx.Client.State.FacilityAdaptation

	qlfilt := util.FilterSlice(fa.Filters.Quicklook,
		func(f sim.QuicklookRegion) bool {
			_, disabled := ps.DisabledQLRegions[f.Id]
			return !disabled
		})
	userPositions := ctx.Client.State.GetPositionsForTCW(ctx.UserTCW)
	for _, trk := range sp.visibleTracks {
		state := sp.TrackState[trk.ADSBCallsign]

		if trk.IsUnassociated() {
			continue
		}

		fp := trk.FlightPlan
		acType := ""
		if fp != nil {
			acType = fp.AircraftType
		}
		inRegion := slices.ContainsFunc(qlfilt,
			func(f sim.QuicklookRegion) bool {
				return f.Match(state.track.Location, int(state.track.TransponderAltitude),
					fp, userPositions, acType, fa.SignificantPoints)
			})

		anno := sp.annotationsForTrack(ctx, trk)
		cb := func(err error) { sp.displayError(err, ctx, "") }
		// Route the InQLRegion bit through its per-field setter to
		// avoid clobbering server-driven neighboring annotation
		// fields between 1 Hz state-update polls.
		if inRegion && !anno.InQLRegion {
			// Entry: track just entered a quicklook region.
			anno.DisplayFDB = true
			ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno, cb)
			ctx.Client.SetTrackInQLRegion(trk.ADSBCallsign, true, cb)
		} else if !inRegion && anno.InQLRegion {
			// Exit: track just left a quicklook region.
			// Don't clear DisplayFDB if it's being maintained by the
			// outbound handoff acceptance logic.
			if !anno.OutboundHandoffAccepted {
				anno.DisplayFDB = false
				ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno, cb)
			}
			ctx.Client.SetTrackInQLRegion(trk.ADSBCallsign, false, cb)
		}
	}
}

func (sp *STARSPane) drawTracks(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trackBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trackBuilder)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)

	// Update cached command buffers for tracks
	sp.fusedTrackVertices = getTrackVertices(ctx, sp.getTrackSize(ctx, transforms))

	processTrack := func(trk sim.Track) {
		state := sp.TrackState[trk.ADSBCallsign]

		positionSymbol := ""

		if trk.IsUnassociated() {
			// See if a position symbol override applies. Note that this may be overridden in code following shortly.
			fa := ctx.Client.State.FacilityAdaptation
			for _, r := range fa.UntrackedPositionSymbolOverrides.CodeRanges {
				if trk.Squawk >= r[0] && trk.Squawk <= r[1] { // ranges are inclusive
					positionSymbol = fa.UntrackedPositionSymbolOverrides.Symbol
					break
				}
			}

			switch trk.Mode {
			case av.TransponderModeStandby:
				ps := sp.currentPrefs()
				positionSymbol = util.Select(ps.InhibitPositionSymOnUnassociatedPrimary,
					" ", string(rune(140))) // diamond
			case av.TransponderModeAltitude:
				if sp.beaconCodeSelected(trk.Squawk) {
					positionSymbol = string(rune(129)) // square
				} else if positionSymbol == "" {
					positionSymbol = "*"
				}
			case av.TransponderModeOn:
				if sp.beaconCodeSelected(trk.Squawk) {
					positionSymbol = string(rune(128)) // triangle
				} else if positionSymbol == "" {
					positionSymbol = "+"
				}
			}
		} else {
			positionSymbol = "?"
			// Use the owning TCW's primary TCP to determine the position symbol.
			tcp := ctx.PrimaryTCPForTCW(trk.FlightPlan.OwningTCW)
			if tcp == "" && trk.FlightPlan.OwningTCW != "" {
				// Owning TCW has no positions. Use its character id
				positionSymbol = string(trk.FlightPlan.OwningTCW[1:])
			} else if ctrl := ctx.Client.State.Controllers[tcp]; ctrl != nil {
				if ctrl.Scope != "" {
					// Explicitly specified scope_char overrides everything.
					positionSymbol = ctrl.Scope
				} else if ctrl.FacilityIdentifier != "" {
					// For external facilities we use the shortest facility id
					positionSymbol = ctrl.FacilityIdentifier
				} else if len(ctrl.Position) > 0 {
					positionSymbol = ctrl.Position[len(ctrl.Position)-1:]
				}
			}
		}

		sp.drawTrack(trk, state, ctx, transforms, positionSymbol, trackBuilder, ld, trid, td)
	}

	// Draw in three passes so that the user's own tracks end up on top:
	// unassociated first, then other controllers' tracks, then tracks owned
	// by the user's TCW last.
	for _, trk := range sp.visibleTracks {
		if trk.IsUnassociated() {
			processTrack(trk)
		}
	}
	for _, trk := range sp.visibleTracks {
		if trk.IsAssociated() && !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			processTrack(trk)
		}
	}
	for _, trk := range sp.visibleTracks {
		if trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			processTrack(trk)
		}
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

func (sp *STARSPane) getTrackSize(ctx *panes.Context, transforms radar.ScopeTransformations) float32 {
	var size float32 = 13 // base track size
	e := transforms.PixelDistanceNM(ctx.NmPerLongitude)
	var distance float32 = 0.3623 // Around 2200 feet in nm
	if distance/e > 13 {
		size = distance / e
	}
	return size
}

func (sp *STARSPane) getGhostTracks(ctx *panes.Context) []*av.GhostTrack {
	var ghosts []*av.GhostTrack
	ps := sp.currentPrefs()

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
			leaderDirection := sp.CRDAPairs[i].LeaderDirections[j]
			if rwyState.LeaderLineDirection != nil {
				leaderDirection = *rwyState.LeaderLineDirection
			}

			region := sp.CRDAPairs[i].CRDARegions[j]
			otherRegion := sp.CRDAPairs[i].CRDARegions[(j+1)%2]

			trackId := util.Select(pairState.Mode == CRDAModeStagger, sp.CRDAPairs[i].StaggerSymbol,
				sp.CRDAPairs[i].TieSymbol)

			offset := util.Select(pairState.Mode == CRDAModeTie, sp.CRDAPairs[i].TieOffset, float32(0))

			nmPerLongitude := ctx.NmPerLongitude
			for _, trk := range sp.visibleTracks {
				if trk.IsUnassociated() {
					continue
				}
				state := sp.TrackState[trk.ADSBCallsign]
				anno := sp.annotationsForTrack(ctx, trk)

				// Create a ghost track if appropriate, add it to the
				// ghosts slice, and draw its radar track.
				force := anno.Ghost.State == GhostStateForced || ps.CRDA.ForceAllGhosts
				heading := util.Select(state.HaveHeading(),
					float32(math.TrueToMagnetic(state.TrackHeading(nmPerLongitude), ctx.MagneticVariation)),
					float32(trk.Heading))

				ghost := region.TryMakeGhost(trk.RadarTrack, heading, trk.FlightPlan.Scratchpad, force, offset,
					leaderDirection, nmPerLongitude, otherRegion)
				if ghost != nil {
					ghost.TrackId = trackId
					ghosts = append(ghosts, ghost)
				}
			}
		}
	}

	return ghosts
}

func (sp *STARSPane) drawGhosts(ctx *panes.Context, ghosts []*av.GhostTrack, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.currentPrefs()
	brightness := ps.Brightness.OtherTracks
	color := brightness.ScaleRGB(sp.Colors.GhostDatablock)
	trackFont := sp.systemFont(ctx, ps.CharSize.PositionSymbols)
	trackStyle := renderer.TextStyle{Font: trackFont, Color: color, LineSpacing: 0}
	datablockFont := sp.systemFont(ctx, ps.CharSize.Datablocks)

	var strBuilder strings.Builder
	for _, ghost := range ghosts {
		anno := sp.annotations(ctx, ghost.ADSBCallsign)

		if anno.Ghost.State == GhostStateSuppressed {
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

		halfSeconds := time.Now().UnixMilli() / 500
		clockPhase := ctx.FacilityAdaptation.CurrentDatablockClockPhase(time.Now())
		db.draw(td, pll, datablockFont, &strBuilder, brightness, ghost.LeaderLineDirection, clockPhase, halfSeconds)

		// Leader line
		ld.AddLine(pac, math.Add2f(pac, vll), color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawTrack(trk sim.Track, state *TrackState, ctx *panes.Context,
	transforms radar.ScopeTransformations, positionSymbol string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()

	pos := state.track.Location
	isUnsupported := state.track.TrueAltitude == 0 && trk.FlightPlan != nil // FIXME: there's surely a better way to do this
	pw := transforms.WindowFromLatLongP(pos)
	primaryTargetBrightness := ps.Brightness.PrimarySymbols

	drawPrimarySymbol := !isUnsupported && primaryTargetBrightness > 0 && !trk.IsTentative
	if drawPrimarySymbol {
		switch mode := sp.radarMode(ctx.FacilityAdaptation.RadarSites); mode {
		case RadarModeSingle:
			site := ctx.FacilityAdaptation.RadarSites[ps.RadarSiteSelected]
			primary, secondary, dist := site.CheckVisibility(pos, int(trk.TrueAltitude))

			// Orient the box toward the radar
			h := float32(math.TrueToMagnetic(math.Heading2LL(site.Position, pos, ctx.NmPerLongitude), ctx.MagneticVariation))
			rot := math.Rotator2f(h)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{{-9, -3}, {9, -3}, {9, 3}, {-9, 3}}

			// Scale box based on distance from the radar; TODO: what exactly should this be?
			scale := ctx.DrawPixelScale * float32(math.Clamp(dist/40, .5, 1.5))
			for i := range box {
				box[i] = math.Scale2f(box[i], scale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(sp.Colors.TrackGeometry)
			if primary {
				// Draw a filled box
				trid.AddQuad(box[0], box[1], box[2], box[3], color)
			} else if secondary {
				// If it's just a secondary return, only draw the box outline.
				// TODO: is this 40nm, or secondary?
				ld.AddLineLoop(color, box[:])
			}

			// green line
			line := [2][2]float32{{-16, 3}, {16, 3}}
			for i := range line {
				line[i] = math.Add2f(rot(math.Scale2f(line[i], scale)), pw)
				line[i] = transforms.LatLongFromWindowP(line[i])
			}
			ld.AddLine(line[0], line[1], primaryTargetBrightness.ScaleRGB(renderer.RGB{R: .1, G: .8, B: .1}))

		case RadarModeMulti:
			primary, secondary, _ := sp.radarVisibility(ctx.FacilityAdaptation.RadarSites,
				pos, int(trk.TrueAltitude))
			// "cheat" by using trk.Heading if we don't yet have two radar tracks to compute the
			// heading with; this makes things look better when we first see a track or when
			// restarting a simulation...
			heading := util.Select(state.HaveHeading(),
				float32(math.TrueToMagnetic(state.TrackHeading(ctx.NmPerLongitude), ctx.MagneticVariation)), float32(trk.Heading))

			rot := math.Rotator2f(heading)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{{-9, -3}, {9, -3}, {9, 3}, {-9, 3}}
			for i := range box {
				box[i] = math.Scale2f(box[i], ctx.DrawPixelScale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(sp.Colors.TrackGeometry)
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
				color := primaryTargetBrightness.ScaleRGB(sp.Colors.TrackGeometry)
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
			// diagonals
			dx := transforms.LatLongFromWindowV([2]float32{1, 0})
			dy := transforms.LatLongFromWindowV([2]float32{0, 1})
			// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
			delta := func(p math.Point2LL, x, y float32) math.Point2LL {
				return math.Add2LL(p, math.Add2LL(math.Scale2f(dx, x), math.Scale2f(dy, y)))
			}

			px := 3 * ctx.DrawPixelScale
			// diagonals
			diagPx := px * 0.707107 /* 1/sqrt(2) */
			trackColor := posBrightness.ScaleRGB(sp.Colors.UnownedDatablock)
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

func (sp *STARSPane) drawHistoryTrails(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.History == 0 {
		// Don't draw if brightness == 0.
		return
	}

	historyBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(historyBuilder)

	const historyTrackDiameter = 8
	historyTrackVertices := getTrackVertices(ctx, historyTrackDiameter)

	for _, trk := range sp.visibleTracks {
		state := sp.TrackState[trk.ADSBCallsign]

		// In general, if the datablock isn't being drawn (e.g. due to
		// altitude filters), don't draw history. The one exception is
		// unassociated tracks squawking standby (I think!).
		if !sp.datablockVisible(ctx, trk) && !(trk.IsUnassociated() && trk.Mode == av.TransponderModeStandby) {
			continue
		}

		// Skip history updates for unsupported datablocks (fake tracks with "__" prefix)
		if strings.HasPrefix(string(trk.ADSBCallsign), "__") {
			continue
		}

		// Draw history from new to old
		for i := range ps.RadarTrackHistory {
			trackColorNum := min(i, len(sp.Colors.TrackHistory)-1)
			trackColor := ps.Brightness.History.ScaleRGB(sp.Colors.TrackHistory[trackColorNum])

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
	if trk.IsAssociated() && !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
		return nil, false
	}

	if trk.OnApproach {
		// No warnings once they're flying the approach
		return nil, false
	}

	vols := ctx.Client.AirspaceForTCW(ctx.UserTCW)

	inside, alts := av.InAirspace(trk.Location, trk.TrueAltitude, vols)
	if trk.EnteredOurAirspace && !inside {
		return alts, true
	}
	return nil, false
}

func (sp *STARSPane) updateCAAircraft(ctx *panes.Context) {
	tracked, untracked := make(map[av.ADSBCallsign]sim.Track), make(map[av.ADSBCallsign]sim.Track)
	for _, trk := range sp.visibleTracks {
		if trk.IsAssociated() {
			tracked[trk.ADSBCallsign] = trk
		} else {
			untracked[trk.ADSBCallsign] = trk
		}
	}

	inCAInhibitFilter := func(trk *sim.Track) bool {
		nocaFilter := ctx.Client.State.FacilityAdaptation.Filters.InhibitCA
		state := sp.TrackState[trk.ADSBCallsign]
		return nocaFilter.Inside(state.track.Location, int(state.track.TransponderAltitude))
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
		if math.Abs(trka.TransponderAltitude-trkb.TransponderAltitude) > 5000 ||
			math.NMLength2LL(math.Sub2f(trka.Location, trkb.Location), nmPerLongitude) > 10 {
			return false
		}

		// No CA if they're in the same ATPA volume; let the ATPA monitor take it
		va, vb := trka.ATPAVolume, trkb.ATPAVolume
		if va != nil && vb != nil && va.Id == vb.Id {
			return false
		}

		if inCAInhibitFilter(trka) || inCAInhibitFilter(trkb) {
			return false
		}

		return math.NMDistance2LL(trka.Location, trkb.Location) <= LateralMinimum &&
			math.Abs(trka.TransponderAltitude-trkb.TransponderAltitude) <= VerticalMinimum-5 && /*small slop for fp error*/
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
		if math.Abs(trka.TransponderAltitude-trkb.TransponderAltitude) > 5000 ||
			math.NMLength2LL(math.Sub2f(trka.Location, trkb.Location), nmPerLongitude) > 10 {
			return false
		}

		if inCAInhibitFilter(trka) || inCAInhibitFilter(trkb) {
			return false
		}

		return math.NMDistance2LL(trka.Location, trkb.Location) <= 1.5 &&
			math.Abs(trka.TransponderAltitude-trkb.TransponderAltitude) <= 500-5 && /*small slop for fp error*/
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
					SoundEnd:      ctx.SimTime.Add(AlertAudioDuration),
					Start:         ctx.SimTime,
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
					SoundEnd:      ctx.SimTime.Add(AlertAudioDuration),
					Start:         ctx.SimTime,
				})
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

func (sp *STARSPane) drawLeaderLines(ctx *panes.Context, dbs map[av.ADSBCallsign]datablock, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {

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
				state := sp.TrackState[trk.ADSBCallsign]
				pac := transforms.WindowFromLatLongP(state.track.Location)

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

	draw(sp.visibleTracks)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) getLeaderLineDirection(ctx *panes.Context, trk sim.Track) math.CardinalOrdinalDirection {
	ps := sp.currentPrefs()

	if trk.IsAssociated() {
		sfp := trk.FlightPlan
		anno := sp.annotations(ctx, trk.ADSBCallsign)
		if sfp.Suspended {
			// Suspended are always north, evidently.
			return math.North
		} else if anno.UseGlobalLeaderLine && sfp.GlobalLeaderLineDirection != nil {
			return *sfp.GlobalLeaderLineDirection
		} else if anno.LeaderLineDirection != nil {
			// The direction was specified for the aircraft specifically
			return *anno.LeaderLineDirection
		} else if anno.FDAMLeaderLineDirection != nil {
			return *anno.FDAMLeaderLineDirection
		}

		// Check if the active configuration specifies a leader line
		// direction for this scratchpad. This comes after per-aircraft and
		// FDAM overrides but before controller defaults, since it is an
		// adaptation-defined default keyed on flight plan data.
		if sfp.Scratchpad != "" {
			if config, ok := ctx.FacilityAdaptation.Configurations[ctx.Client.State.ConfigurationId]; ok &&
				config.ScratchpadLeaderLineDirections != nil {
				if dir, ok := config.ScratchpadLeaderLineDirections[sfp.Scratchpad]; ok {
					return dir
				}
			}
		}

		if ctx.UserOwnsFlightPlan(sfp) {
			// Tracked by us
			return ps.LeaderLineDirection
		} else if ctx.UserControlsPosition(sfp.HandoffController) {
			// Being handed off to us
			return ps.LeaderLineDirection
		} else if dir, ok := ps.ControllerLeaderLineDirections[ctx.PrimaryTCPForTCW(sfp.OwningTCW)]; ok {
			// Owned by another controller for whom a direction was specified
			return dir
		} else if ps.OtherControllerLeaderLineDirection != nil {
			// Tracked by another controller without a per-controller direction specified
			return *ps.OtherControllerLeaderLineDirection
		}
	} else { // unassociated
		// Per-track leader line direction is not available for
		// unassociated tracks (no ACID to key shared annotations on).
		if ps.UnassociatedLeaderLineDirection != nil {
			return *ps.UnassociatedLeaderLineDirection
		}
	}

	// TODO: should this case have a user-specifiable default?
	return math.CardinalOrdinalDirection(math.North)
}

func (sp *STARSPane) getLeaderLineVector(ctx *panes.Context, dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := math.SinCos(math.Radians(angle))
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
			distance = min(distance, dist)
		}
	}

	return
}

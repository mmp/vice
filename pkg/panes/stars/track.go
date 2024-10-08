// pkg/panes/stars/track.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"sort"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

// This is a stopgap for the ERAM/STARS switchover; it should eventually be
// replaced with something like
// ctx.ControlClient.STARSComputer().TrackInformation[ac.Callsign].  Until
// everything is wired up, some of the information needed is still being
// maintained in Aircraft, so we'll make an ad-hoc TrackInformation here.
func (sp *STARSPane) getTrack(ctx *panes.Context, ac *av.Aircraft) *sim.TrackInformation {
	trk := ctx.ControlClient.STARSComputer().TrackInformation[ac.Callsign]
	if trk == nil {
		trk = &sim.TrackInformation{}
	}

	trk.Identifier = ac.Callsign
	trk.TrackOwner = ac.TrackingController
	trk.HandoffController = ac.HandoffTrackController
	trk.SP1 = ac.Scratchpad
	trk.SP2 = ac.SecondaryScratchpad
	trk.RedirectedHandoff = ac.RedirectedHandoff
	trk.PointOutHistory = ac.PointOutHistory
	if trk.FlightPlan == nil {
		trk.FlightPlan = sim.MakeSTARSFlightPlan(ac.FlightPlan)
	}

	return trk
}

type AircraftState struct {
	// Independently of the track history, we store the most recent track
	// from the sensor as well as the previous one. This gives us the
	// freshest possible information for things like calculating headings,
	// rates of altitude change, etc.
	track         av.RadarTrack
	previousTrack av.RadarTrack

	// Radar track history is maintained with a ring buffer where
	// historyTracksIndex is the index of the next track to be written.
	// (Thus, historyTracksIndex==0 implies that there are no tracks.)
	// Changing to/from FUSED mode causes tracksIndex to be reset, thus
	// discarding previous tracks.
	historyTracks      [10]av.RadarTrack
	historyTracksIndex int

	DatablockType            DatablockType
	FullLDBEndTime           time.Time // If the LDB displays the groundspeed. When to stop
	DisplayRequestedAltitude *bool     // nil if unspecified

	IsSelected bool // middle click

	TabListIndex int // 0-99. If -1, we ran out and haven't assigned one.

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
	ATPALeadAircraftCallsign string

	POFlashingEndTime time.Time
	UNFlashingEndTime time.Time
	IFFlashing        bool // Will continue to flash unless slewed or a successful handoff
	NextController    string

	// These are only set if a leader line direction was specified for this
	// aircraft individually:
	LeaderLineDirection       *math.CardinalOrdinalDirection
	GlobalLeaderLineDirection *math.CardinalOrdinalDirection
	UseGlobalLeaderLine       bool

	Ghost struct {
		PartialDatablock bool
		State            GhostState
	}

	displayPilotAltitude bool
	pilotAltitude        int

	DisplayLDBBeaconCode bool
	DisplayPTL           bool
	DisableCAWarnings    bool

	MSAW             bool // minimum safe altitude warning
	DisableMSAW      bool
	InhibitMSAW      bool // only applies if in an alert. clear when alert is over?
	MSAWAcknowledged bool
	MSAWSoundEnd     time.Time

	SPCAlert        bool
	SPCAcknowledged bool
	SPCSoundEnd     time.Time

	FirstSeen           time.Time
	FirstRadarTrack     time.Time
	HaveEnteredAirspace bool

	CWTCategory string // cache this for performance

	IdentStart, IdentEnd    time.Time
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
	PointedOut bool
	ForceQL    bool
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

func (s *AircraftState) TrackAltitude() int {
	return s.track.Altitude
}

func (s *AircraftState) TrackDeltaAltitude() int {
	if s.previousTrack.Position.IsZero() {
		// No previous track
		return 0
	}
	return s.track.Altitude - s.previousTrack.Altitude
}

func (s *AircraftState) TrackPosition() math.Point2LL {
	return s.track.Position
}

func (s *AircraftState) TrackGroundspeed() int {
	return s.track.Groundspeed
}

func (s *AircraftState) HaveHeading() bool {
	return !s.previousTrack.Position.IsZero()
}

// Note that the vector returned by HeadingVector() is along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (s *AircraftState) HeadingVector(nmPerLongitude, magneticVariation float32) math.Point2LL {
	if !s.HaveHeading() {
		return math.Point2LL{}
	}

	p0 := math.LL2NM(s.track.Position, nmPerLongitude)
	p1 := math.LL2NM(s.previousTrack.Position, nmPerLongitude)
	v := math.Sub2LL(p0, p1)
	v = math.Normalize2f(v)
	// v's length should be groundspeed / 60 nm.
	v = math.Scale2f(v, float32(s.TrackGroundspeed())/60) // hours to minutes
	return math.NM2LL(v, nmPerLongitude)
}

func (s *AircraftState) TrackHeading(nmPerLongitude float32) float32 {
	if !s.HaveHeading() {
		return 0
	}
	return math.Heading2LL(s.previousTrack.Position, s.track.Position, nmPerLongitude, 0)
}

func (s *AircraftState) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !s.track.Position.IsZero() && now.Sub(s.track.Time) > 30*time.Second
}

func (s *AircraftState) Ident(now time.Time) bool {
	return !s.IdentStart.IsZero() && s.IdentStart.Before(now) && s.IdentEnd.After(now)
}

func (sp *STARSPane) processEvents(ctx *panes.Context) {
	// First handle changes in world.Aircraft
	for callsign, ac := range ctx.ControlClient.Aircraft {
		if _, ok := sp.Aircraft[callsign]; !ok {
			// First we've seen it; create the *AircraftState for it
			sa := &AircraftState{}
			if ac.TrackingController == ctx.ControlClient.Callsign || ac.ControllingController == ctx.ControlClient.Callsign {
				sa.DatablockType = FullDatablock
			}
			sa.GlobalLeaderLineDirection = ac.GlobalLeaderLineDirection
			sa.UseGlobalLeaderLine = sa.GlobalLeaderLineDirection != nil
			sa.FirstSeen = ctx.ControlClient.SimTime
			sa.CWTCategory = ac.CWT()
			sa.TabListIndex = TabListUnassignedIndex

			sp.Aircraft[callsign] = sa
		}

		if ok, _ := av.SquawkIsSPC(ac.Squawk); ok && !sp.Aircraft[callsign].SPCAlert {
			// First we've seen it
			state := sp.Aircraft[callsign]
			state.SPCAlert = true
			state.SPCAcknowledged = false
			state.SPCSoundEnd = ctx.Now.Add(AlertAudioDuration)
		}
	}

	// See if any aircraft we have state for have been removed
	for callsign, state := range sp.Aircraft {
		if _, ok := ctx.ControlClient.Aircraft[callsign]; !ok {
			// Free up the Tab list entry
			if state.TabListIndex != TabListUnassignedIndex {
				sp.TabListAircraft[state.TabListIndex] = ""
			}
			delete(sp.Aircraft, callsign)
		}
	}

	// Filter out any removed aircraft from the CA list
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		_, a := ctx.ControlClient.Aircraft[ca.Callsigns[0]]
		_, b := ctx.ControlClient.Aircraft[ca.Callsigns[1]]
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
			if event.ToController == ctx.ControlClient.Callsign {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil {
					sp.InboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.InboundPointOuts[event.Callsign] = ""
				}
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}
			if event.FromController == ctx.ControlClient.Callsign {
				if ctrl := ctx.ControlClient.Controllers[event.ToController]; ctrl != nil {
					sp.OutboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.OutboundPointOuts[event.Callsign] = ""
				}
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}

		case sim.AcknowledgedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil && ctrl.SectorId == id {
					sp.Aircraft[event.Callsign].POFlashingEndTime = time.Now().Add(5 * time.Second)
					delete(sp.OutboundPointOuts, event.Callsign)
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.ToController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
					if state, ok := sp.Aircraft[event.Callsign]; ok {
						state.PointedOut = true
						state.POFlashingEndTime = time.Now().Add(5 * time.Second)
					}
				}
			}

		case sim.RejectedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.OutboundPointOuts, event.Callsign)
					sp.RejectedPointOuts[event.Callsign] = nil
					sp.Aircraft[event.Callsign].UNFlashingEndTime = time.Now().Add(5 * time.Second)
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.ToController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
				}
			}

		case sim.InitiatedTrackEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}

		case sim.OfferedHandoffEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				sp.playOnce(ctx.Platform, AudioInboundHandoff)
			}

		case sim.AcceptedHandoffEvent:
			if event.FromController == ctx.ControlClient.Callsign && event.ToController != ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					state.OutboundHandoffAccepted = true
					state.OutboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
				}
			}

		case sim.AcceptedRedirectedHandoffEvent:
			if event.FromController == ctx.ControlClient.Callsign && event.ToController != ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					state.OutboundHandoffAccepted = true
					state.OutboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
					state.RDIndicatorEnd = time.Now().Add(30 * time.Second)
					state.DatablockType = FullDatablock
				}
			}

		case sim.IdentEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.IdentStart = time.Now().Add(time.Duration(2+rand.Intn(3)) * time.Second)
				state.IdentEnd = state.IdentStart.Add(10 * time.Second)
			}

		case sim.SetGlobalLeaderLineEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.GlobalLeaderLineDirection = event.LeaderLineDirection
				state.UseGlobalLeaderLine = state.GlobalLeaderLineDirection != nil
			}

		case sim.ForceQLEvent:
			if sp.ForceQLCallsigns == nil {
				sp.ForceQLCallsigns = make(map[string]interface{})
			}
			sp.ForceQLCallsigns[event.Callsign] = nil

		case sim.TransferRejectedEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.IFFlashing = true
				sp.cancelHandoff(ctx, event.Callsign)
			}

		case sim.TransferAcceptedEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.IFFlashing = false
			}
		}
	}
}

func (sp *STARSPane) isQuicklooked(ctx *panes.Context, ac *av.Aircraft) bool {
	if sp.currentPrefs().QuickLookAll {
		return true
	}
	if _, ok := sp.ForceQLCallsigns[ac.Callsign]; ok {
		return true
	}

	// Quick Look Positions.
	if trk := sp.getTrack(ctx, ac); trk != nil {
		for _, quickLookPositions := range sp.currentPrefs().QuickLookPositions {
			if trk.TrackOwner == quickLookPositions.Callsign {
				return true
			}
		}
	}

	return false
}

func (sp *STARSPane) updateMSAWs(ctx *panes.Context) {
	// See if there are any MVA issues
	mvas := av.DB.MVAs[ctx.ControlClient.TRACON]
	for callsign, ac := range ctx.ControlClient.Aircraft {
		state := sp.Aircraft[callsign]
		if !ac.MVAsApply() {
			state.MSAW = false
			continue
		}

		if trk := sp.getTrack(ctx, ac); trk == nil || trk.TrackOwner == "" {
			// No MSAW for unassociated tracks.
			state.MSAW = false
			continue
		}

		warn := slices.ContainsFunc(mvas, func(mva av.MVA) bool {
			return state.track.Altitude < mva.MinimumLimit && mva.Inside(state.track.Position)
		})

		if !warn && state.InhibitMSAW {
			// The warning has cleared, so the inhibit is disabled (p.7-25)
			state.InhibitMSAW = false
		}
		if warn && !state.MSAW {
			// It's a new alert
			state.MSAWAcknowledged = false
			state.MSAWSoundEnd = time.Now().Add(AlertAudioDuration)
		}
		state.MSAW = warn
	}
}

func (sp *STARSPane) updateRadarTracks(ctx *panes.Context) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	now := ctx.ControlClient.SimTime
	if sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused {
		if now.Sub(sp.lastTrackUpdate) < 1*time.Second {
			return
		}
	} else {
		if now.Sub(sp.lastTrackUpdate) < 5*time.Second {
			return
		}
	}
	sp.lastTrackUpdate = now

	for callsign, state := range sp.Aircraft {
		ac, ok := ctx.ControlClient.Aircraft[callsign]
		if !ok {
			ctx.Lg.Errorf("%s: not found in Aircraft?", callsign)
			continue
		}

		state.previousTrack = state.track
		state.track = av.RadarTrack{
			Position:    ac.Position(),
			Altitude:    int(ac.Altitude()),
			Groundspeed: int(ac.Nav.FlightState.GS),
			Time:        now,
		}
	}

	// Update low altitude alerts now that we have updated tracks
	sp.updateMSAWs(ctx)

	aircraft := sp.visibleAircraft(ctx)
	sort.Slice(aircraft, func(i, j int) bool {
		return aircraft[i].Callsign < aircraft[j].Callsign
	})

	// History tracks are updated after a radar track update, only if
	// H_RATE seconds have elapsed (4-94).
	ps := sp.currentPrefs()
	if now.Sub(sp.lastHistoryTrackUpdate).Seconds() >= float64(ps.RadarTrackHistoryRate) {
		sp.lastHistoryTrackUpdate = now
		for _, ac := range aircraft { // We only get radar tracks for visible aircraft
			state := sp.Aircraft[ac.Callsign]
			idx := state.historyTracksIndex % len(state.historyTracks)
			state.historyTracks[idx] = state.track
			state.historyTracksIndex++
		}
	}

	sp.updateCAAircraft(ctx, aircraft)
	sp.updateInTrailDistance(ctx, aircraft)

	// FIXME(mtrokel): should this be happening in the STARSComputer Update method?
	if !ctx.ControlClient.STARSFacilityAdaptation.KeepLDB {
		ctx.ControlClient.STARSComputer().UpdateAssociatedFlightPlans(aircraft)
	}
}

func (sp *STARSPane) drawTracks(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations,
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

	now := ctx.ControlClient.SimTime
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		positionSymbol := "*"
		if trk := sp.getTrack(ctx, ac); trk != nil && trk.TrackOwner != "" {
			positionSymbol = "?"
			if ctrl, ok := ctx.ControlClient.Controllers[trk.TrackOwner]; ok && ctrl != nil {
				if ctrl.FacilityIdentifier != "" {
					// For external facilities we use the facility id
					positionSymbol = ctrl.FacilityIdentifier
				} else if ctrl.Scope != "" {
					positionSymbol = ctrl.Scope
				} else {
					positionSymbol = ctrl.SectorId[len(ctrl.SectorId)-1:]
				}
			}
		}

		// "cheat" by using ac.Heading() if we don't yet have two radar tracks to compute the
		// heading with; this makes things look better when we first see a track or when
		// restarting a simulation...
		heading := util.Select(state.HaveHeading(),
			state.TrackHeading(ac.NmPerLongitude())+ac.MagneticVariation(), ac.Heading())

		sp.drawRadarTrack(ac, state, heading, ctx, transforms, positionSymbol, trackBuilder,
			ld, trid, td)
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

func (sp *STARSPane) getTrackSize(ctx *panes.Context, transforms ScopeTransformations) float32 {
	var size float32 = 13 // base track size
	e := transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)
	var distance float32 = 0.3623 // Around 2200 feet in nm
	if distance/e > 13 {
		size = distance / e
	}
	return size
}

func (sp *STARSPane) getGhostAircraft(aircraft []*av.Aircraft, ctx *panes.Context) []*av.GhostAircraft {
	var ghosts []*av.GhostAircraft
	ps := sp.currentPrefs()
	now := ctx.ControlClient.SimTime

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

			for _, ac := range aircraft {
				state := sp.Aircraft[ac.Callsign]
				if state.LostTrack(now) {
					continue
				}

				// Create a ghost track if appropriate, add it to the
				// ghosts slice, and draw its radar track.
				force := state.Ghost.State == GhostStateForced || ps.CRDA.ForceAllGhosts
				heading := util.Select(state.HaveHeading(), state.TrackHeading(ac.NmPerLongitude()),
					ac.Heading())

				ghost := region.TryMakeGhost(ac.Callsign, state.track, heading, ac.Scratchpad, force,
					offset, leaderDirection, runwayIntersection, ac.NmPerLongitude(), ac.MagneticVariation(),
					otherRegion)
				if ghost != nil {
					ghost.TrackId = trackId
					ghosts = append(ghosts, ghost)
				}
			}
		}
	}

	return ghosts
}

func (sp *STARSPane) drawGhosts(ghosts []*av.GhostAircraft, ctx *panes.Context, transforms ScopeTransformations,
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

	for _, ghost := range ghosts {
		state := sp.Aircraft[ghost.Callsign]

		if state.Ghost.State == GhostStateSuppressed {
			continue
		}

		// The track is just the single character..
		pw := transforms.WindowFromLatLongP(ghost.Position)
		td.AddTextCentered(ghost.TrackId, pw, trackStyle)

		// Draw datablock
		db := sp.getGhostDatablock(ghost, color)
		pac := transforms.WindowFromLatLongP(ghost.Position)
		vll := sp.getLeaderLineVector(ctx, ghost.LeaderLineDirection)
		pll := math.Add2f(pac, vll)

		db.draw(td, pll, datablockFont, brightness, ghost.LeaderLineDirection, ctx.Now.Unix())

		// Leader line
		ld.AddLine(pac, math.Add2f(pac, vll), color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRadarTrack(ac *av.Aircraft, state *AircraftState, heading float32, ctx *panes.Context,
	transforms ScopeTransformations, positionSymbol string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	// TODO: orient based on radar center if just one radar

	pos := state.TrackPosition()
	pw := transforms.WindowFromLatLongP(pos)
	// On high DPI windows displays we need to scale up the tracks

	primaryTargetBrightness := ps.Brightness.PrimarySymbols
	if primaryTargetBrightness > 0 {
		switch mode := sp.radarMode(ctx.ControlClient.RadarSites); mode {
		case RadarModeSingle:
			site := ctx.ControlClient.RadarSites[ps.RadarSiteSelected]
			primary, secondary, dist := site.CheckVisibility(pos, state.TrackAltitude())

			// Orient the box toward the radar
			h := math.Heading2LL(site.Position, pos, ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
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
			primary, secondary, _ := sp.radarVisibility(ctx.ControlClient.RadarSites, pos, state.TrackAltitude())
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
	color, _, posBrightness := sp.trackDatablockColorBrightness(ctx, ac)
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

func (sp *STARSPane) drawHistoryTrails(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations,
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

	now := ctx.ControlClient.CurrentTime()
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		// Draw history from new to old
		for i := range ps.RadarTrackHistory {
			trackColorNum := math.Min(i, len(STARSTrackHistoryColors)-1)
			trackColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColors[trackColorNum])

			if idx := (state.historyTracksIndex - 1 - i) % len(state.historyTracks); idx >= 0 {
				if p := state.historyTracks[idx].Position; !p.IsZero() {
					drawTrack(historyBuilder, transforms.WindowFromLatLongP(p), historyTrackVertices,
						trackColor)
				}
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	historyBuilder.GenerateCommands(cb)
}

func (sp *STARSPane) WarnOutsideAirspace(ctx *panes.Context, ac *av.Aircraft) (alts [][2]int, outside bool) {
	// Only report on ones that are tracked by us
	if trk := sp.getTrack(ctx, ac); trk == nil || trk.TrackOwner != ctx.ControlClient.Callsign {
		return
	}

	if ac.OnApproach(false) {
		// No warnings once they're flying the approach
		return
	}

	state := sp.Aircraft[ac.Callsign]
	if ctx.ControlClient.IsDeparture(ac) {
		if len(ctx.ControlClient.DepartureAirspace) > 0 {
			inDepartureAirspace, depAlts := sim.InAirspace(ac.Position(), ac.Altitude(), ctx.ControlClient.DepartureAirspace)
			if !state.HaveEnteredAirspace {
				state.HaveEnteredAirspace = inDepartureAirspace
			} else {
				alts = depAlts
				outside = !inDepartureAirspace
			}
		}
	} else {
		if len(ctx.ControlClient.ApproachAirspace) > 0 {
			inApproachAirspace, depAlts := sim.InAirspace(ac.Position(), ac.Altitude(), ctx.ControlClient.ApproachAirspace)
			if !state.HaveEnteredAirspace {
				state.HaveEnteredAirspace = inApproachAirspace
			} else {
				alts = depAlts
				outside = !inApproachAirspace
			}
		}
	}
	return
}

func (sp *STARSPane) updateCAAircraft(ctx *panes.Context, aircraft []*av.Aircraft) {
	inCAVolumes := func(state *AircraftState) bool {
		for _, vol := range ctx.ControlClient.InhibitCAVolumes() {
			if vol.Inside(state.TrackPosition(), state.TrackAltitude()) {
				return true
			}
		}
		return false
	}

	conflicting := func(callsigna, callsignb string) bool {
		sa, sb := sp.Aircraft[callsigna], sp.Aircraft[callsignb]
		if sa.DisableCAWarnings || sb.DisableCAWarnings {
			return false
		}

		// No CA for unassociated tracks
		aca, acb := ctx.ControlClient.Aircraft[callsigna], ctx.ControlClient.Aircraft[callsignb]
		if aca != nil && acb != nil {
			trka, trkb := sp.getTrack(ctx, aca), sp.getTrack(ctx, acb)
			if trka == nil || trka.TrackOwner == "" || trkb == nil || trkb.TrackOwner == "" {
				return false
			}
		}

		// No CA if they're in the same ATPA volume; let the ATPA monitor take it
		va, vb := aca.ATPAVolume(), acb.ATPAVolume()
		if va != nil && vb != nil && va.Id == vb.Id {
			return false
		}

		if inCAVolumes(sa) || inCAVolumes(sb) {
			return false
		}

		return math.NMDistance2LL(sa.TrackPosition(), sb.TrackPosition()) <= LateralMinimum &&
			/*small slop for fp error*/
			math.Abs(sa.TrackAltitude()-sb.TrackAltitude()) <= VerticalMinimum-5 &&
			!sp.diverging(ctx.ControlClient.Aircraft[callsigna], ctx.ControlClient.Aircraft[callsignb])
	}

	// Remove ones that are no longer conflicting
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return conflicting(ca.Callsigns[0], ca.Callsigns[1])
	})

	// Remove ones that are no longer visible
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return slices.ContainsFunc(aircraft, func(ac *av.Aircraft) bool { return ac.Callsign == ca.Callsigns[0] }) &&
			slices.ContainsFunc(aircraft, func(ac *av.Aircraft) bool { return ac.Callsign == ca.Callsigns[1] })
	})

	// Add new conflicts; by appending we keep them sorted by when they
	// were first detected...
	callsigns := util.MapSlice(aircraft, func(ac *av.Aircraft) string { return ac.Callsign })
	for i, callsign := range callsigns {
		for _, ocs := range callsigns[i+1:] {
			if conflicting(callsign, ocs) {
				if !slices.ContainsFunc(sp.CAAircraft, func(ca CAAircraft) bool {
					return callsign == ca.Callsigns[0] && ocs == ca.Callsigns[1]
				}) {
					sp.CAAircraft = append(sp.CAAircraft, CAAircraft{
						Callsigns: [2]string{callsign, ocs},
						SoundEnd:  ctx.Now.Add(AlertAudioDuration),
					})
				}
			}
		}
	}
}

func (sp *STARSPane) updateInTrailDistance(ctx *panes.Context, aircraft []*av.Aircraft) {
	// Zero out the previous distance
	for _, ac := range aircraft {
		sp.Aircraft[ac.Callsign].IntrailDistance = 0
		sp.Aircraft[ac.Callsign].MinimumMIT = 0
		sp.Aircraft[ac.Callsign].ATPAStatus = ATPAStatusUnset
		sp.Aircraft[ac.Callsign].ATPALeadAircraftCallsign = ""
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

	for _, ac := range aircraft {
		vol := ac.ATPAVolume()
		if vol == nil {
			continue
		}
		if _, ok := handledVolumes[vol.Id]; ok {
			continue
		}

		// Get all aircraft on approach to this runway
		runwayAircraft := util.FilterSlice(aircraft, func(ac *av.Aircraft) bool {
			if v := ac.ATPAVolume(); v == nil || v.Id != vol.Id {
				return false
			}

			// Excluded scratchpad -> aircraft doesn't participate in the
			// party whatsoever.
			if ac.Scratchpad != "" && slices.Contains(vol.ExcludedScratchpads, ac.Scratchpad) {
				return false
			}

			state := sp.Aircraft[ac.Callsign]
			return vol.Inside(state.TrackPosition(), float32(state.TrackAltitude()),
				state.TrackHeading(ac.NmPerLongitude())+ac.MagneticVariation(),
				ac.NmPerLongitude(), ac.MagneticVariation())
		})

		// Sort by distance to threshold (there will be some redundant
		// lookups of STARSAircraft state et al. here, but it's
		// straightforward to implement it like this.)
		sort.Slice(runwayAircraft, func(i, j int) bool {
			pi := sp.Aircraft[runwayAircraft[i].Callsign].TrackPosition()
			pj := sp.Aircraft[runwayAircraft[j].Callsign].TrackPosition()
			return math.NMDistance2LL(pi, vol.Threshold) < math.NMDistance2LL(pj, vol.Threshold)
		})

		for i := range runwayAircraft {
			if i == 0 {
				// The first one doesn't have anyone in front...
				continue
			}
			leading, trailing := runwayAircraft[i-1], runwayAircraft[i]
			leadingState, trailingState := sp.Aircraft[leading.Callsign], sp.Aircraft[trailing.Callsign]
			trailingState.IntrailDistance =
				math.NMDistance2LL(leadingState.TrackPosition(), trailingState.TrackPosition())
			sp.checkInTrailCwtSeparation(ctx, trailing, leading)
		}
		handledVolumes[vol.Id] = nil
	}
}

type ModeledAircraft struct {
	callsign     string
	p            [2]float32 // nm coords
	v            [2]float32 // nm, normalized
	gs           float32
	alt          float32
	dalt         float32    // per second
	threshold    [2]float32 // nm
	landingSpeed float32
}

func MakeModeledAircraft(ac *av.Aircraft, state *AircraftState, threshold math.Point2LL) ModeledAircraft {
	ma := ModeledAircraft{
		callsign:  ac.Callsign,
		p:         math.LL2NM(state.TrackPosition(), ac.NmPerLongitude()),
		gs:        float32(state.TrackGroundspeed()),
		alt:       float32(state.TrackAltitude()),
		dalt:      float32(state.TrackDeltaAltitude()),
		threshold: math.LL2NM(threshold, ac.NmPerLongitude()),
	}
	if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.BaseType()]; ok {
		ma.landingSpeed = perf.Speed.Landing
	} else {
		ma.landingSpeed = 120 // ....
	}
	ma.v = state.HeadingVector(ac.NmPerLongitude(), ac.MagneticVariation())
	ma.v = math.LL2NM(ma.v, ac.NmPerLongitude())
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

func (sp *STARSPane) checkInTrailCwtSeparation(ctx *panes.Context, back, front *av.Aircraft) {
	cwtSeparation := av.CWTApproachSeparation(front.CWT(), back.CWT())

	state := sp.Aircraft[back.Callsign]
	vol := back.ATPAVolume()
	if cwtSeparation == 0 {
		cwtSeparation = float32(LateralMinimum)

		// 7110.126B replaces 7110.65Z 5-5-4(j), which is now 7110.65AA 5-5-4(i)
		// Reduced separation allowed 10 NM out (also enabled for the ATPA volume)
		if vol.Enable25nmApproach &&
			math.NMDistance2LL(vol.Threshold, state.TrackPosition()) < vol.Dist25nmApproach {

			// between aircraft established on the final approach course
			// Note 1: checked with OnExtendedCenterline since reduced separation probably
			// doesn't apply to approaches with curved final approach segment
			// Note 2: 0.2 NM is slightly less than full-scale deflection at 5 NM out
			if back.OnExtendedCenterline(.2) && front.OnExtendedCenterline(.2) {
				// Not-implemented: Required separation must exist prior to applying 2.5 NM separation (TBL 5-5-2)
				cwtSeparation = 2.5
			}
		}
	}

	state.MinimumMIT = cwtSeparation
	state.ATPALeadAircraftCallsign = front.Callsign
	state.ATPAStatus = ATPAStatusMonitor // baseline

	// If the aircraft's scratchpad is filtered, then it doesn't get
	// warnings or alerts but is still here for the aircraft behind it.
	if back.Scratchpad != "" && slices.Contains(vol.FilteredScratchpads, back.Scratchpad) {
		return
	}

	// front, back aircraft
	frontModel := MakeModeledAircraft(front, sp.Aircraft[front.Callsign], vol.Threshold)
	backModel := MakeModeledAircraft(back, state, vol.Threshold)

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

func (sp *STARSPane) diverging(a, b *av.Aircraft) bool {
	sa, sb := sp.Aircraft[a.Callsign], sp.Aircraft[b.Callsign]

	pa := math.LL2NM(sa.TrackPosition(), a.NmPerLongitude())
	da := math.LL2NM(sa.HeadingVector(a.NmPerLongitude(), a.MagneticVariation()), a.NmPerLongitude())
	pb := math.LL2NM(sb.TrackPosition(), b.NmPerLongitude())
	db := math.LL2NM(sb.HeadingVector(b.NmPerLongitude(), b.MagneticVariation()), b.NmPerLongitude())

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
	if math.HeadingDifference(sa.TrackHeading(a.NmPerLongitude()), sb.TrackHeading(b.NmPerLongitude())) < 15 {
		return false
	}

	return true
}

func (sp *STARSPane) drawLeaderLines(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	now := ctx.ControlClient.SimTime

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		if sp.getDatablock(ctx, ac) != nil {
			baseColor, brightness, _ := sp.trackDatablockColorBrightness(ctx, ac)
			pac := transforms.WindowFromLatLongP(state.TrackPosition())
			v := sp.getLeaderLineVector(ctx, sp.getLeaderLineDirection(ac, ctx))
			ld.AddLine(pac, math.Add2f(pac, v), brightness.ScaleRGB(baseColor))
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) getLeaderLineDirection(ac *av.Aircraft, ctx *panes.Context) math.CardinalOrdinalDirection {
	ps := sp.currentPrefs()
	state := sp.Aircraft[ac.Callsign]
	trk := sp.getTrack(ctx, ac)

	if trk == nil {
		if state.LeaderLineDirection != nil {
			return *state.LeaderLineDirection
		} else {
			return math.CardinalOrdinalDirection(math.North)
		}
	}

	if state.UseGlobalLeaderLine {
		return *state.GlobalLeaderLineDirection
	} else if state.LeaderLineDirection != nil {
		// The direction was specified for the aircraft specifically
		return *state.LeaderLineDirection
	} else if trk.TrackOwner == ctx.ControlClient.Callsign {
		// Tracked by us
		return ps.LeaderLineDirection
	} else if dir, ok := ps.ControllerLeaderLineDirections[trk.TrackOwner]; ok {
		// Tracked by another controller for whom a direction was specified
		return dir
	} else if ps.OtherControllerLeaderLineDirection != nil {
		// Tracked by another controller without a per-controller direction specified
		return *ps.OtherControllerLeaderLineDirection
	} else {
		// TODO: should this case have a user-specifiable default?
		return math.CardinalOrdinalDirection(math.North)
	}
}

func (sp *STARSPane) getLeaderLineVector(ctx *panes.Context, dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	ps := sp.currentPrefs()
	pxLengths := []float32{0, 17, 32, 47, 62, 77, 114, 152}
	idx := min(ps.LeaderLineLength, len(pxLengths)-1)
	return math.Scale2f(v, pxLengths[idx])
}

func (sp *STARSPane) isOverflight(ctx *panes.Context, trk *sim.TrackInformation) bool {
	return trk != nil && trk.FlightPlan != nil &&
		ctx.ControlClient.Airports[trk.FlightPlan.DepartureAirport] == nil &&
		ctx.ControlClient.Airports[trk.FlightPlan.ArrivalAirport] == nil
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

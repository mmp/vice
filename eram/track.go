// eram/track.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"maps"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// eramUpdateInterval is how often all radar samples are refreshed: the
// 12 second scan of the long-range radars that ERAM uses for most of its
// coverage.
const eramUpdateInterval = 12 * time.Second

type TrackState struct {
	// Track is the most recent radar sample of the aircraft; everything
	// the scope displays about its position and transponder comes from it
	// rather than from the continuously-updated sim state.
	Track             av.RadarTrack
	PreviousTrack     av.RadarTrack
	TrackTime         sim.Time
	PreviousTrackTime sim.Time

	// The current sample plus the 5 past positions that the HISTORY
	// setting can display at most.
	HistoryTracks     [6]historyTrack
	HistoryTrackIndex int

	DatablockType DatablockType

	LeaderLineDirection *math.CardinalOrdinalDirection
	// LeaderLineLength is set by the /<0-3> command; nil follows FDB LDR.
	LeaderLineLength *int // 0=no line (W/E only), 1=normal (default), 2=2x, 3=3x

	ELDB bool
	EFDB bool

	DisplayJRing        bool
	DisplayReducedJRing bool

	DisplayVCI bool

	OSectorEndTime sim.Time

	HoverVCI bool // if the user is hovering over the VCI field

	HSFHide bool
	// LastScratchpads holds the flight plan's scratchpads, which hold its
	// HSF data, as of the last check, so that a new entry clears HSFHide.
	LastScratchpads [2]string

	// p/o receiver keeps a FDB until suppressed by QP <FLID>.
	PointOutFDBLocked bool

	// add more as we figure out what to do...
}

type aircraftFixCoordinates struct {
	coords     []math.Point2LL
	deleteTime sim.Time
}

func (ts *TrackState) HaveHeading() bool {
	return !ts.PreviousTrack.Location.IsZero()
}

func (ts *TrackState) TrackHeading(nmPerLongitude float32) math.TrueHeading {
	if !ts.HaveHeading() {
		return -1
	}
	return math.Heading2LL(ts.PreviousTrack.Location, ts.Track.Location, nmPerLongitude)
}

func (ep *Scope) trackStateForACID(ctx *scope.Context, acid sim.ACID) (*TrackState, bool) {
	// Figure out the ADSB callsign for this ACID.
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			s, ok := ep.TrackState[trk.ADSBCallsign]
			return s, ok
		}
	}
	return nil, false
}

func (ep *Scope) processEvents(ctx *scope.Context) {
	for _, trk := range ctx.Client.State.Tracks {
		state, ok := ep.TrackState[trk.ADSBCallsign]
		if !ok {
			state = &TrackState{}
			ep.TrackState[trk.ADSBCallsign] = state
		}
		// The point out may have been made before the user signed on, so
		// the lock follows the flight plan rather than an event.
		if trk.IsAssociated() && len(ctx.InboundPointOuts(trk.FlightPlan)) > 0 {
			state.PointOutFDBLocked = true
		}
	}

	// Forget everything about aircraft that are gone.
	for callsign := range ep.TrackState {
		if _, ok := ctx.GetTrackByCallsign(callsign); !ok {
			delete(ep.TrackState, callsign)
		}
	}
	trackExists := func(acid sim.ACID) bool {
		_, ok := ctx.Client.State.GetTrackByACID(acid)
		return ok
	}
	maps.DeleteFunc(ep.AckedPointOuts, func(acid sim.ACID, _ []sim.ControlPosition) bool {
		return !trackExists(acid)
	})
	for _, g := range ep.CRRGroups {
		if g != nil {
			maps.DeleteFunc(g.Aircraft, func(callsign av.ADSBCallsign, _ struct{}) bool {
				_, ok := ctx.GetTrackByCallsign(callsign)
				return !ok
			})
		}
	}

	// Events may refer to aircraft that no longer exist, so track state
	// lookups must be checked.
	for _, event := range ctx.Events {
		switch event.Type {
		case sim.AcceptedHandoffEvent:
			// The transferring sector keeps its FDB, where Field E shows the
			// accepting sector for a while (Table 5-4, O-dd).
			outbound := ctx.UserControlsPosition(event.FromController) && !ctx.UserControlsPosition(event.ToController)
			if !outbound {
				continue
			}
			if state, ok := ep.trackStateForACID(ctx, event.ACID); ok {
				state.EFDB = true
				state.OSectorEndTime = ctx.InterpolatedSimTime.Add(30 * time.Second)
			}

		case sim.OfferedHandoffEvent:
			// Being offered a handoff redisplays suppressed HSF data
			// (5.1.5.20.3).
			if ctx.UserControlsPosition(event.ToController) {
				if state, ok := ep.trackStateForACID(ctx, event.ACID); ok {
					state.HSFHide = false
				}
			}

		case sim.AcknowledgedPointOutEvent:
			// Per sim/handoff.go, From/To are swapped in this event relative to the original p/o.
			if ctx.UserControlsPosition(event.ToController) {
				// We were the originator.
				acked := ep.AckedPointOuts[event.ACID]
				if !slices.Contains(acked, event.FromController) {
					ep.AckedPointOuts[event.ACID] = append(acked, event.FromController)
				}
			}

		case sim.FixCoordinatesEvent:
			ep.aircraftFixCoordinates[event.ACID] = aircraftFixCoordinates{
				coords:     event.WaypointInfo,
				deleteTime: ctx.InterpolatedSimTime.Add(15 * time.Second),
			}

		case sim.FlightPlanDirectEvent:
			// Draw the waypoints like QU /M line
			var coords []math.Point2LL
			for _, wp := range event.Route {
				coords = append(coords, wp.Location)
			}
			ep.aircraftFixCoordinates[event.ACID] = aircraftFixCoordinates{
				coords:     coords,
				deleteTime: ctx.InterpolatedSimTime.Add(15 * time.Second),
			}
		}
	}
}

// updateRadarTracks samples tracks on a shared scan. New tracks wait for the
// next scan before appearing on the scope.
func (ep *Scope) updateRadarTracks(ctx *scope.Context) {
	nowInterp := ctx.InterpolatedSimTime.Time()
	if nowInterp.Sub(ep.dbLastAlternateTime) > 6*time.Second {
		ep.dbAlternate = !ep.dbAlternate
		ep.dbLastAlternateTime = nowInterp
	}

	now := ctx.Client.State.SimTime
	updateAll := ep.lastRadarUpdate.IsZero() || now.Sub(ep.lastRadarUpdate) >= eramUpdateInterval
	if updateAll {
		ep.lastRadarUpdate = now
	}
	for _, trk := range ctx.Client.State.Tracks {
		state := ep.TrackState[trk.ADSBCallsign]

		if trk.IsAssociated() {
			// Adding or changing a heading, speed, or free text redisplays
			// suppressed HSF data; deleting one doesn't (5.1.5.20.3).
			sps := [2]string{trk.FlightPlan.Scratchpad, trk.FlightPlan.SecondaryScratchpad}
			for i, sp := range sps {
				if sp != "" && sp != state.LastScratchpads[i] {
					state.HSFHide = false
				}
			}
			state.LastScratchpads = sps
		}

		if !updateAll {
			continue
		}
		if trk.TypeOfFlight == av.FlightTypeDeparture && trk.IsTentative && !state.TrackTime.IsZero() {
			// Get the first track for tentative tracks but then don't
			// update any further until it's no longer tentative.
			continue
		}

		state.PreviousTrack = state.Track
		state.PreviousTrackTime = state.TrackTime
		state.Track = trk.RadarTrack
		state.TrackTime = now

		// Update history tracks
		idx := state.HistoryTrackIndex % len(state.HistoryTracks)
		state.HistoryTracks[idx] = historyTrack{state.Track, ep.getTarget(*trk, state)}
		state.HistoryTrackIndex++

		// A reduced halo reverts to the standard one when the track leaves
		// reduced separation airspace (2.9.3).
		if state.DisplayReducedJRing && !inReducedSeparationAirspace(state.Track.TransponderAltitude) {
			state.DisplayReducedJRing = false
			state.DisplayJRing = true
		}

		// TODO: check unreasonable C
		// CA processing
		// etc
	}
	// check QU lines; see if they need to be cleared. TODO: add QU to delete all lines
	for ac, coords := range ep.aircraftFixCoordinates {
		if coords.deleteTime.Before(ctx.InterpolatedSimTime) {
			delete(ep.aircraftFixCoordinates, ac)
		}
	}
}

func (ep *Scope) drawTargets(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trackBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trackBuilder)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)

	for _, trk := range tracks {
		if !ep.targetVisible(ctx, trk) {
			continue
		}
		state := ep.TrackState[trk.ADSBCallsign]
		targetSymbol := ep.getTarget(trk, state)
		ep.drawTarget(trk, state, ctx, transforms, targetSymbol, trackBuilder, ld, trid, td, cb)
	}
	transforms.LoadWindowViewingMatrices(cb)
	trackBuilder.GenerateCommands(cb)

	transforms.LoadLatLongViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (ep *Scope) drawTarget(track sim.Track, state *TrackState, ctx *scope.Context,
	transforms scope.Transformations, position string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder,
	cb *renderer.CommandBuffer) {
	pos := state.Track.Location
	pw := transforms.WindowFromLatLongP(pos)
	pt := math.Add2f(pw, [2]float32{0.5, -.5}) // Text this out

	color := ep.targetColor(track)
	td.AddTextCentered(position, pt, renderer.TextStyle{Font: ep.systemFont[5], Color: color})

	// trackBuilder.GenerateCommands(cb)
	ld.GenerateCommands(cb) // why does this need to be here?
}

func (ep *Scope) drawTracks(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	for _, trk := range tracks {
		if ep.datablockType(ctx, trk) != FullDatablock {
			continue // Only draw tracks for full datablocks
		}
		state := ep.TrackState[trk.ADSBCallsign]
		ep.drawTrack(trk, state, ctx, td, transforms, cb)
	}
	td.GenerateCommands(cb)
}

// TODO: Store tracks in ERAMComputer and have them associate to targets
func (ep *Scope) drawTrack(trk sim.Track, state *TrackState, ctx *scope.Context,
	td *renderer.TextDrawBuilder, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	pos := state.Track.Location
	// TODO: free tracks, frozen tracks, and coast tracks
	// drawDiamond(ctx, transforms, ep.trackColor(state, trk), pos, ld, cb)
	font := ep.systemFont[9]
	color := ep.targetColor(trk)
	if ep.caDimmed(trk.ADSBCallsign) {
		color = color.Scale(caDimFactor)
	}
	td.AddTextCentered("\u0000", transforms.WindowFromLatLongP(pos),
		renderer.TextStyle{Font: font, Color: color})
}

// Target symbols, at their code points in the EramTargets font (Table 2-3).
const (
	uncorrelatedPrimaryTarget = "\u0000" // +
	correlatedPrimaryTarget   = "\u0002" // X
	uncorrelatedBeaconTarget  = "\u0003" // /
	correlatedBeaconTarget    = "\u0004" // \
	reducedSeparationTarget   = "\u0005" // filled circle
	identingBeaconTarget      = "\u0006" // three bars
	code1200Target            = "\u0008" // V
)

// getTarget returns the target symbol for the track's radar sample (Table
// 2-3); only whether it is associated with a flight plan comes from the live
// track.
func (ep *Scope) getTarget(trk sim.Track, state *TrackState) string {
	rt := state.Track
	switch {
	case rt.Mode == av.TransponderModeStandby:
		return util.Select(trk.IsAssociated(), correlatedPrimaryTarget, uncorrelatedPrimaryTarget)
	case rt.Ident:
		return identingBeaconTarget
	case trk.IsAssociated():
		return util.Select(inReducedSeparationAirspace(rt.TransponderAltitude), reducedSeparationTarget,
			correlatedBeaconTarget)
	case rt.Squawk == 0o1200:
		return code1200Target
	default:
		return uncorrelatedBeaconTarget
	}
}

// inReducedSeparationAirspace reports whether a track at the given altitude
// gets the reduced separation target symbol, may have a 3 NM halo, and is
// probed for conflicts at 3 NM. The manual leaves reduced separation airspace
// to adaptation; vice takes it to be everything at or below FL230.
func inReducedSeparationAirspace(alt float32) bool {
	return alt <= 23000
}

// targetColor returns the color of a track's target and position symbols,
// which the PR TGT and UNP TGT brightness controls set for paired and
// unpaired targets (Table 3-3).
func (ep *Scope) targetColor(trk sim.Track) renderer.RGB {
	ps := ep.currentPrefs()
	bright := util.Select(trk.IsAssociated(), ps.Brightness.PRTGT, ps.Brightness.UNPTGT)
	return bright.ScaleRGB(colors.yellow)
}

func (ep *Scope) updateVisibleTracks(ctx *scope.Context) { // When radar holes are added
	// Get the visible tracks based on the current range and center.
	ep.visibleTracks = ep.visibleTracks[:0]
	for _, trk := range ctx.Client.State.Tracks {
		// Radar wholes neeeded for this. For now, return true
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil || state.TrackTime.IsZero() || state.Track.TransponderAltitude <= 49 {
			continue
		}
		ep.visibleTracks = append(ep.visibleTracks, *trk)
	}
}

// datablockBrightness returns the configured brightness for the given track's
// datablock type.
func (ep *Scope) datablockBrightness(state *TrackState) scope.Brightness {
	ps := ep.currentPrefs()
	if state.DatablockType == FullDatablock {
		return ps.Brightness.FDB
	}
	return ps.Brightness.LDB
}

// datablockDirection returns the position of a track's datablock relative to
// its target. LDBs and FDBs without a leader line may only be positioned to
// the left or right of the target.
func (ep *Scope) datablockDirection(state *TrackState, dbType DatablockType) math.CardinalOrdinalDirection {
	dir := math.NorthEast
	if state.LeaderLineDirection != nil {
		dir = *state.LeaderLineDirection
	}
	if dbType == FullDatablock && ep.leaderLineLength(state) != 0 {
		return dir
	}
	switch dir {
	case math.West, math.NorthWest, math.SouthWest:
		return math.West
	default:
		return math.East
	}
}

// leaderLineLength returns the length mode of a track's FDB leader line.
func (ep *Scope) leaderLineLength(state *TrackState) int {
	if state.LeaderLineLength != nil {
		return *state.LeaderLineLength
	}
	return ep.currentPrefs().FDBLdrLength
}

// leaderLineVectorWithLength returns a vector in window coordinates representing a leader
// line with the length determined by the lengthMode parameter.
// lengthMode: 0 = no line, 1 = normal (60), 2 = 2x (120), 3 = 3x (180)
func (ep *Scope) leaderLineVectorWithLength(dir math.CardinalOrdinalDirection, lengthMode int) [2]float32 {
	scale := float32(60)
	switch lengthMode {
	case 0:
		scale = 0 // No line
	case 1:
		scale = 60 // Normal
	case 2:
		scale = 120 // 2x
	case 3:
		scale = 180 // 3x
	default:
		scale = 60 // Default to normal
	}

	return math.Scale2f(dir.UnitVector(), scale)
}

// For LDBs
func (ep *Scope) leaderLineVectorNoLength(dir math.CardinalOrdinalDirection) [2]float32 {
	return math.Scale2f(dir.UnitVector(), 8)
}

// altitudeInLimits reports whether a reported altitude in feet falls within
// an altitude limits filter, which is expressed in hundreds of feet. The
// altitude is rounded the way the datablock's altitude field rounds it, so
// that a track reading 234 there is inside 234B400.
func altitudeInLimits(alt float32, limits [2]int) bool {
	hundreds := int(alt+50) / 100
	return hundreds >= limits[0] && hundreds <= limits[1]
}

// passesAltitudeLimits reports whether a track is displayed under the given
// altitude limits filter. Full datablocks and their targets are always
// displayed, as are tracks that aren't reporting an altitude to filter on.
func (ep *Scope) passesAltitudeLimits(ctx *scope.Context, trk sim.Track, limits [2]int) bool {
	state := ep.TrackState[trk.ADSBCallsign]
	if state == nil || state.Track.Mode != av.TransponderModeAltitude ||
		ep.datablockType(ctx, trk) == FullDatablock {
		return true
	}
	// Filter on the radar sample the scope is displaying rather than the
	// newer altitude the simulation has: the sample only refreshes every few
	// seconds, and a climbing track crossing a filter boundary would
	// otherwise come and go out of step with the altitude in its datablock.
	return altitudeInLimits(state.Track.TransponderAltitude, limits)
}

// targetVisible reports whether a track's target symbol, and with it its
// history trail, is drawn.
func (ep *Scope) targetVisible(ctx *scope.Context, trk sim.Track) bool {
	return ep.passesAltitudeLimits(ctx, trk, ep.currentPrefs().AltitudeLimits.Targets)
}

// datablockVisible reports whether a datablock should be drawn. The LDB
// altitude limits filter is independent of the target one, so a datablock may
// be drawn for a track whose target is filtered out.
func (ep *Scope) datablockVisible(ctx *scope.Context, trk sim.Track) bool {
	return ep.passesAltitudeLimits(ctx, trk, ep.currentPrefs().AltitudeLimits.LDBs)
}

// datablockType chooses which datablock format to display. Design.
func (ep *Scope) datablockType(ctx *scope.Context, trk sim.Track) DatablockType {
	if trk.IsUnassociated() {
		return LimitedDatablock
	} else {
		state := ep.TrackState[trk.ADSBCallsign]
		fp := trk.FlightPlan
		if ctx.UserOwnsFlightPlan(fp) {
			return FullDatablock
		}
		// Conflict alert forces a full datablock for both participants.
		if ep.inConflictAlert(trk.ADSBCallsign) {
			return FullDatablock
		}
		if ctx.IsHandoffToUser(&trk) {
			return FullDatablock
		}
		if len(ctx.InboundPointOuts(fp)) > 0 {
			return FullDatablock
		}
		if _, ok := ep.QuickLookSectors[string(ctx.PrimaryTCPForTCW(fp.OwningTCW))]; ok {
			return FullDatablock
		}
		if state == nil {
			// No radar track has been taken of it yet.
			return LimitedDatablock
		}
		if state.PointOutFDBLocked {
			return FullDatablock
		}
		if state.EFDB {
			return FullDatablock
		}
		if state.ELDB {
			return LimitedDatablock
		}
	}
	return LimitedDatablock
}

// trackDatablockColorBrightness returns the track color and datablock brightness. Design.
func (ep *Scope) trackDatablockColor(ctx *scope.Context, trk sim.Track) renderer.RGB {
	dType := ep.datablockType(ctx, trk)
	ps := ep.currentPrefs()
	brite := util.Select(dType == FullDatablock, ps.Brightness.FDB, ps.Brightness.LDB)
	return brite.ScaleRGB(colors.yellow)
}

// drawLeaderLines draws leader lines for visible datablocks.
func (ep *Scope) drawLeaderLines(ctx *scope.Context, tracks []sim.Track, dbs map[av.ADSBCallsign]datablock,
	transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	cb.LineWidth(2, ctx.DPIScale)
	font := ep.ERAMFont(ep.currentPrefs().FDBSize)
	for _, trk := range tracks {
		db := dbs[trk.ADSBCallsign]
		if db == nil || !ep.datablockVisible(ctx, trk) {
			continue
		}
		dbType := ep.datablockType(ctx, trk)
		color := ep.trackDatablockColor(ctx, trk)
		if ep.caDimmed(trk.ADSBCallsign) {
			color = color.Scale(caDimFactor)
		}
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}
		p0 := transforms.WindowFromLatLongP(state.Track.Location)
		dir := ep.datablockDirection(state, dbType)

		v := util.Select(dbType == FullDatablock, ep.leaderLineVectorWithLength(dir, ep.leaderLineLength(state)), ep.leaderLineVectorNoLength(dir))
		pv := math.Scale2f(v, ctx.DrawPixelScale)

		if fdb, ok := db.(*fullDatablock); ok && dbType == FullDatablock {
			if over, l := fdb.leadOverhang(font), math.Length2f(pv); over > 0 {
				var pull float32
				switch dir {
				case math.East, math.NorthEast:
					if pv[0] > 0 {
						pull = over * l / pv[0]
					}
				case math.North:
					pull = dbInkHeight*float32(font.Size) + dbLeaderClearance*font.LookupGlyph(' ').AdvanceX
				}
				if pull > 0 && l > pull {
					pv = math.Scale2f(pv, (l-pull)/l)
				}
			}
		}

		p0[1] += 0.5
		p1 := math.Add2f(p0, pv)
		if dbType == FullDatablock {
			ld.AddLine(p0, p1, color)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
}

func (ep *Scope) drawPTLs(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	cb.LineWidth(2, ctx.DPIScale) // tweak this
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	for _, trk := range tracks {
		dbType := ep.datablockType(ctx, trk)
		if dbType != FullDatablock {
			continue // Only draw PTLs for full datablocks
		}
		state := ep.TrackState[trk.ADSBCallsign]
		speed := state.Track.Groundspeed
		dist := speed / 60 * float32(ep.VelocityTime)
		pos := state.Track.Location
		heading := state.TrackHeading(ctx.NmPerLongitude)
		if heading == -1 {
			continue // dont draw PTLs for tracks that don't have a calculated heading
		}
		ptlEnd := math.Offset2LL(pos, heading, dist, ctx.NmPerLongitude)
		p0 := transforms.WindowFromLatLongP(pos)
		p1 := transforms.WindowFromLatLongP(ptlEnd)
		color := ep.trackDatablockColor(ctx, trk)
		if ep.caDimmed(trk.ADSBCallsign) {
			color = color.Scale(caDimFactor)
		}
		ld.AddLine(p0, p1, color)
	}
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)

}

type historyTrack struct {
	av.RadarTrack
	PositionSymbol string
}

// drawHistoryTracks draws small position symbols representing the last few
// positions of each track.
func (ep *Scope) drawHistoryTracks(ctx *scope.Context, tracks []sim.Track,
	transforms scope.Transformations, cb *renderer.CommandBuffer) {

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ctd := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(ctd)

	ps := ep.currentPrefs()

	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil || !ep.targetVisible(ctx, trk) {
			continue
		}

		// Determine brightness based on association
		var bright scope.Brightness
		// TODO: Eventually when coasting tracks, etc, (non associated with aircraft tracks) are added, this will need to be updated to include all tracks that are associated with an aircraft (not just a flight plan)
		if trk.IsAssociated() {
			bright = ps.Brightness.PRHST
		} else {
			bright = ps.Brightness.UNPHST
		}

		color := bright.ScaleRGB(colors.yellow)

		// Draw newest first (circular buffer handling), respecting selected
		// history length. The newest entry is the current sample, which the
		// target symbol already marks, so the trail starts with the one
		// before it.
		for i := range min(ps.HistoryLength, len(state.HistoryTracks)-1) {
			idx := (state.HistoryTrackIndex - 2 - i + len(state.HistoryTracks)) % len(state.HistoryTracks)
			hist := state.HistoryTracks[idx]

			loc := hist.Location
			if loc.IsZero() {
				continue
			}

			pw := transforms.WindowFromLatLongP(loc)
			pt := math.Add2f(pw, [2]float32{0.5, -.5})

			td.AddTextCentered(
				hist.PositionSymbol,
				pt,
				renderer.TextStyle{
					Font:  ep.systemFont[5],
					Color: color,
				},
			)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ctd.GenerateCommands(cb)
}

func (ep *Scope) drawJRings(ctx *scope.Context, tracks []sim.Track,
	transforms scope.Transformations, cb *renderer.CommandBuffer) {
	// Draw J-Rings for tracks that have them enabled.
	jr := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(jr)
	cb.LineWidth(2, ctx.DPIScale)

	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		pos := state.Track.Location
		if pos.IsZero() {
			continue
		}
		if state.DisplayJRing {
			pw := transforms.WindowFromLatLongP(pos)
			radius := 5 / transforms.PixelDistanceNM(ctx.NmPerLongitude)
			jr.AddCircle(pw, radius, 50, colors.yellow)
		}
		if state.DisplayReducedJRing {
			pw := transforms.WindowFromLatLongP(pos)
			reducedRadius := 3 / transforms.PixelDistanceNM(ctx.NmPerLongitude)
			// 15 degree gaps centered on the cardinal headings (glossary:
			// Distance Reference Indicator). With 48 segments of 7.5
			// degrees, each gap falls exactly on segment boundaries.
			reducedJRingGaps := [][2]float32{
				{352.5, 7.5},
				{82.5, 97.5},
				{172.5, 187.5},
				{262.5, 277.5},
			}

			jr.AddGappedCircle(pw, reducedRadius, 48, reducedJRingGaps, colors.yellow)
		}
	}
	jr.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
}

func (ep *Scope) drawQULines(ctx *scope.Context, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	for acid, info := range ep.aircraftFixCoordinates {
		trk, ok := ctx.Client.State.GetTrackByACID(acid)
		if !ok {
			continue
		}
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}
		color := ep.trackDatablockColor(ctx, *trk)

		// Convert aircraft position to window coordinates
		acWindowPos := transforms.WindowFromLatLongP(state.Track.Location)
		if len(info.coords) == 0 {
			continue
		}
		firstFixWindowPos := transforms.WindowFromLatLongP(info.coords[0])
		ld.AddLine(acWindowPos, firstFixWindowPos, color) // draw a line from the AC to the first fix

		for i, coordinate := range info.coords {
			if i+1 >= len(info.coords) {
				window := transforms.WindowFromLatLongP(coordinate)
				// get coordinates of an X shape
				p1 := math.Add2f(window, [2]float32{5, 8})
				p2 := math.Add2f(window, [2]float32{-5, -8})
				p3 := math.Add2f(window, [2]float32{5, -8})
				p4 := math.Add2f(window, [2]float32{-5, 8})
				ld.AddLine(p1, p2, color)
				ld.AddLine(p3, p4, color)
			} else {
				windowCoords := transforms.WindowFromLatLongP(coordinate)
				ld.AddLine(windowCoords, transforms.WindowFromLatLongP(info.coords[i+1]), color)
			}
		}
	}
	ld.GenerateCommands(cb)
}

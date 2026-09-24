// eram/track.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Gap ranges (in degrees) where reduced separation J rings should not be drawn.

type TrackState struct {
	Track             av.RadarTrack
	PreviousTrack     av.RadarTrack
	PreviousAltitude  float32 // for seeing if the track is climbing or descending. This may need to be moved someplace else later
	PreviousTrackTime time.Time
	TrackTime         time.Time
	CID               int

	HistoryTracks     [6]historyTrack // I think it's six?
	HistoryTrackIndex int

	DatablockType DatablockType

	LeaderLineDirection *math.CardinalOrdinalDirection
	LeaderLineLength    int // 0=no line (W/E only), 1=normal (default), 2=2x, 3=3x

	ELDB bool
	EFDB bool

	DisplayJRing        bool
	DisplayReducedJRing bool

	DisplayVCI bool

	OSectorEndTime sim.Time

	ReachedAltitude bool
	// LastDataBlockAltitude is the altitude the data block showed when it was
	// last checked, so that an amended altitude clears ReachedAltitude.
	LastDataBlockAltitude int

	HoverVCI bool // if the user is hovering over the VCI field

	HSFHide bool

	// p/o receiver keeps a FDB until cleared by QP <FLID>.
	PointOutFDBLocked bool

	// add more as we figure out what to do...
}

type aircraftFixCoordinates struct {
	coords     []math.Point2LL
	deleteTime sim.Time
}

func (ts *TrackState) TrackDeltaAltitude() int {
	if ts.PreviousTrack.Location.IsZero() {
		// No previous track
		return 0
	}
	return int(ts.Track.TransponderAltitude - ts.PreviousTrack.TransponderAltitude)
}

func (ts *TrackState) Descending() bool {
	return ts.Track.TransponderAltitude < ts.PreviousTrack.TransponderAltitude
}

func (ts *TrackState) Climbing() bool {
	return ts.Track.TransponderAltitude > ts.PreviousTrack.TransponderAltitude
}

func (ts *TrackState) IsLevel() bool {
	return ts.Track.TransponderAltitude == ts.PreviousTrack.TransponderAltitude
}

func (ts *TrackState) HaveHeading() bool {
	return !ts.PreviousTrack.Location.IsZero()
}

func (ts *TrackState) HeadingVector(nmPerLongitude, magneticVariation float32) math.Point2LL {
	if !ts.HaveHeading() {
		return math.Point2LL{}
	}

	p0 := math.LL2NM(ts.Track.Location, nmPerLongitude)
	p1 := math.LL2NM(ts.PreviousTrack.Location, nmPerLongitude)
	v := math.Sub2LL(p0, p1)
	v = math.Normalize2f(v)
	// v's length should be groundspeed / 60 nm.
	v = math.Scale2f(v, float32(ts.Track.Groundspeed)/60) // hours to minutes
	return math.NM2LL(v, nmPerLongitude)
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
		if _, ok := ep.TrackState[trk.ADSBCallsign]; !ok {
			sa := &TrackState{
				LeaderLineLength: ep.currentPrefs().FDBLdrLength, // Use current preference
			}
			ep.TrackState[trk.ADSBCallsign] = sa
		}
	}
	for _, event := range ctx.Events {
		switch event.Type {
		case sim.AcceptedHandoffEvent:
			thisCtrl := !ctx.UserControlsPosition(event.FromController) && ctx.UserControlsPosition(event.ToController)
			if !thisCtrl {
				continue
			}
			state := ep.TrackState[av.ADSBCallsign(event.ACID)]
			state.EFDB = true
			state.OSectorEndTime = ctx.InterpolatedSimTime.Add(30 * time.Second)

		case sim.PointOutEvent:
			if ctx.UserControlsPosition(event.ToController) {
				// The receiver's FDB stays forced until cleared by QP <FLID>.
				if state, ok := ep.trackStateForACID(ctx, event.ACID); ok && state != nil {
					state.PointOutFDBLocked = true
				}
				senders := ep.InboundPointOuts[event.ACID]
				if !slices.Contains(senders, event.FromController) {
					ep.InboundPointOuts[event.ACID] = append(senders, event.FromController)
				}
			}
			if ctx.UserControlsPosition(event.FromController) {
				entries := ep.OutboundPointOuts[event.ACID]
				dupe := slices.ContainsFunc(entries, func(po outboundPointOut) bool {
					return po.Receiver == event.ToController
				})
				if !dupe {
					ep.OutboundPointOuts[event.ACID] = append(entries,
						outboundPointOut{Receiver: event.ToController})
				}
			}

		case sim.AcknowledgedPointOutEvent:
			// Per sim/handoff.go, From/To are swapped in this event relative to the original p/o.
			if ctx.UserControlsPosition(event.FromController) {
				// We were the recipient
				delete(ep.InboundPointOuts, event.ACID)
			}
			if ctx.UserControlsPosition(event.ToController) {
				// We were the originator; mark the specific receiver as acked.
				ep.markOutboundPointOutAcked(event.ACID, event.FromController)
			}

		case sim.RecalledPointOutEvent:
			// Receiver clears the originator from its inbound list; the originator clears the
			// specific receiver from its outbound list.
			if ctx.UserControlsPosition(event.ToController) {
				ep.removeInboundPointOut(event.ACID, event.FromController)
			}
			if ctx.UserControlsPosition(event.FromController) {
				ep.removeOutboundPointOutByReceiver(event.ACID, event.ToController)
			}

		case sim.RejectedPointOutEvent:
			// From/To are swapped here too (event.FromController is the
			// recipient who rejected; event.ToController is the originator).
			if ctx.UserControlsPosition(event.FromController) {
				delete(ep.InboundPointOuts, event.ACID)
			}
			if ctx.UserControlsPosition(event.ToController) {
				ep.removeOutboundPointOutByReceiver(event.ACID, event.FromController)
			}

		case sim.FixCoordinatesEvent:
			ac := event.ACID
			coords := event.WaypointInfo
			ep.aircraftFixCoordinates[string(ac)] = aircraftFixCoordinates{
				coords:     coords,
				deleteTime: ctx.InterpolatedSimTime.Add(15 * time.Second),
			}

		case sim.FlightPlanDirectEvent:
			ac := event.ACID
			// Draw the waypoints like QU /M line

			var coords []math.Point2LL
			for _, wp := range event.Route {
				coords = append(coords, wp.Location)
			}
			ep.aircraftFixCoordinates[string(ac)] = aircraftFixCoordinates{
				coords:     coords,
				deleteTime: ctx.InterpolatedSimTime.Add(15 * time.Second),
			}
		}
	}
}

func (ep *Scope) updateRadarTracks(ctx *scope.Context, tracks []sim.Track) {
	// Update the track states based on the current radar tracks.
	nowInterp := ctx.InterpolatedSimTime.Time()
	nowApplied := ctx.Client.State.SimTime.Time()
	if nowInterp.Sub(ep.dbLastAlternateTime) > 6*time.Second {
		ep.dbAlternate = !ep.dbAlternate
		ep.dbLastAlternateTime = nowInterp
	}
	// The data block shows an amended altitude as soon as it is entered,
	// whether by a controller here or by the virtual controller working the
	// aircraft, so the climb/descent arrow must follow it just as promptly.
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil || !trk.IsAssociated() {
			continue
		}
		if alt := trk.FlightPlan.DataBlockAltitude(); alt != state.LastDataBlockAltitude {
			state.LastDataBlockAltitude = alt
			state.ReachedAltitude = false
		}
	}

	if nowApplied.Sub(ep.lastTrackUpdate) < 12*time.Second {
		return
	}
	ep.lastTrackUpdate = nowApplied
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			state = &TrackState{
				LeaderLineLength: ep.currentPrefs().FDBLdrLength, // Use current preference
			}
			ep.TrackState[trk.ADSBCallsign] = state
		}

		if trk.TypeOfFlight == av.FlightTypeDeparture && trk.IsTentative && !state.TrackTime.IsZero() {
			// Get the first track for tentative tracks but then don't
			// update any further until it's no longer tentative.
			continue
		}

		// check if tracks with a reduced DRI are above FL230
		if state.DisplayReducedJRing && state.Track.TransponderAltitude > 23000 {
			state.DisplayReducedJRing = false
		}

		state.PreviousTrack = state.Track
		state.PreviousAltitude = state.Track.TransponderAltitude
		state.PreviousTrackTime = state.TrackTime
		state.Track = trk.RadarTrack
		state.TrackTime = nowApplied

		// Update history tracks
		idx := state.HistoryTrackIndex % len(state.HistoryTracks)
		state.HistoryTracks[idx] = historyTrack{state.Track, ep.getTarget(trk, state)}
		state.HistoryTrackIndex++

		// check to see if the a/c has reached the altitude
		if trk.IsAssociated() {
			qalt := func(alt float32) int { return int(alt+50) / 100 }
			if qalt(state.Track.TransponderAltitude) == qalt(float32(trk.FlightPlan.AssignedAltitude)) {
				state.ReachedAltitude = true
			}
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

	color := ep.trackColor()
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
	td.AddTextCentered("\u0000", transforms.WindowFromLatLongP(pos),
		renderer.TextStyle{Font: font, Color: ep.trackColor()})
}

func (ep *Scope) getTarget(trk sim.Track, state *TrackState) string {
	symbol := "\u0001"
	if trk.IsUnassociated() {
		switch trk.Mode {
		case av.TransponderModeStandby:
			symbol = "\u0000"
		case av.TransponderModeAltitude:
			switch {
			case trk.Ident:
				symbol = "\u0006"
			case trk.Squawk == 0o1200 && trk.TransponderAltitude < 1000:
				symbol = "\u0008"
			case trk.Squawk != 0o1200 && trk.TransponderAltitude < 1000:
				symbol = "\u0003"
			case trk.TransponderAltitude >= 1000:
				symbol = "\u0007"
			}
		}
	} else {
		if trk.Mode == av.TransponderModeStandby {
			symbol = "\u0002"
		} else if state.Track.TransponderAltitude < 23000 {
			symbol = "\u0005"
		} else {
			symbol = "\u0004"
		}
	}
	return symbol
}

func drawDiamond(ctx *scope.Context, transforms scope.Transformations, color renderer.RGB,
	pos [2]float32, ld *renderer.ColoredLinesDrawBuilder, cb *renderer.CommandBuffer) {
	cb.LineWidth(2, ctx.DPIScale)
	pt := transforms.WindowFromLatLongP(pos)
	scale := float32(5) * ctx.DPIScale
	p0 := math.Add2f(pt, [2]float32{0, scale})
	p1 := math.Add2f(pt, [2]float32{scale, 0})
	p2 := math.Add2f(pt, [2]float32{0, -scale})
	p3 := math.Add2f(pt, [2]float32{-scale, 0})
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
}

func (ep *Scope) trackColor() renderer.RGB {
	ps := ep.currentPrefs()
	bright := ps.Brightness.PRTGT
	return bright.ScaleRGB(colors.yellow)
}

func (ep *Scope) updateVisibleTracks(ctx *scope.Context) { // When radar holes are added
	// Get the visible tracks based on the current range and center.
	ep.visibleTracks = ep.visibleTracks[:0]
	for _, trk := range ctx.Client.State.Tracks {
		// Radar wholes neeeded for this. For now, return true
		if trk.TransponderAltitude <= 49 {
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

// leaderLineDirection returns the direction in which a datablock's leader line
// should be drawn. The initial implementation always points northeast.
func (ep *Scope) leaderLineDirection(ctx *scope.Context, trk sim.Track) *math.CardinalOrdinalDirection {
	state := ep.TrackState[trk.ADSBCallsign]
	dir := state.LeaderLineDirection
	if dir == nil {
		direction := math.NorthEast
		dir = &direction
		state.LeaderLineDirection = dir
	}
	return state.LeaderLineDirection
}

// leaderLineVector returns a vector in window coordinates representing a leader
// line of a fixed length in the given direction.
func (ep *Scope) leaderLineVector(dir math.CardinalOrdinalDirection) [2]float32 {
	return math.Scale2f(dir.UnitVector(), 60)
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
	if state == nil || trk.Mode != av.TransponderModeAltitude ||
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
		if len(ep.InboundPointOuts[fp.ACID]) > 0 {
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
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}
		p0 := transforms.WindowFromLatLongP(state.Track.Location)
		dir := ep.leaderLineDirection(ctx, trk)
		if dbType == LimitedDatablock || dbType == EnhancedLimitedDatablock {
			*dir = math.East
		}

		// For /0 mode (LeaderLineLength == 0), restrict to W/E only
		if dbType == FullDatablock && state.LeaderLineLength == 0 {
			if *dir != math.East && *dir != math.West {
				*dir = math.East // Default to East if in /0 mode
			}
		}

		v := util.Select(dbType == FullDatablock, ep.leaderLineVectorWithLength(*dir, state.LeaderLineLength), ep.leaderLineVectorNoLength(*dir))
		pv := math.Scale2f(v, ctx.DrawPixelScale)

		if fdb, ok := db.(*fullDatablock); ok && dbType == FullDatablock {
			if over, l := fdb.leadOverhang(font), math.Length2f(pv); over > 0 {
				var pull float32
				switch *dir {
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

		// Draw newest first (circular buffer handling), respecting selected history length.
		for i := range min(ps.HistoryLength, len(state.HistoryTracks)) {
			idx := (state.HistoryTrackIndex - 1 - i + len(state.HistoryTracks)) % len(state.HistoryTracks)
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
			reducedJRingGaps := [][2]float32{
				{350, 10},
				{80, 100},
				{170, 190},
				{260, 280},
			}

			jr.AddGappedCircle(pw, reducedRadius, 50, reducedJRingGaps, colors.yellow)
		}
	}
	jr.GenerateCommands(cb)
}

func (ep *Scope) drawQULines(ctx *scope.Context, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	for acid, info := range ep.aircraftFixCoordinates {
		trk, ok := ctx.GetTrackByCallsign(av.ADSBCallsign(acid))
		if !ok {
			continue
		}
		state := ep.TrackState[trk.ADSBCallsign]
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

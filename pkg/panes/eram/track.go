package eram

import (
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type TrackState struct {
	track             av.RadarTrack
	previousTrack     av.RadarTrack
	previousAltitude  float32 // for seeing if the track is climbing or descending. This may need to be moved someplace else later
	previousTrackTime time.Time
	trackTime         time.Time
	CID               int

	historyTracks     [6]av.RadarTrack // I think it's six?
	historyTrackIndex int

	DatablockType DatablockType

	JRingRadius         float32
	leaderLineDirection math.CardinalOrdinalDirection

	eLDB bool
	eFDB bool

	// add more as we figure out what to do...

}

func (ts *TrackState) TrackDeltaAltitude() int {
	if ts.previousTrack.Location.IsZero() {
		// No previous track
		return 0
	}
	return int(ts.track.TransponderAltitude - ts.previousTrack.TransponderAltitude)
}

func (ts *TrackState) Descending() bool {
	return ts.track.TransponderAltitude < ts.previousTrack.TransponderAltitude
}

func (ts *TrackState) Climbing() bool {
	return ts.track.TransponderAltitude > ts.previousTrack.TransponderAltitude
}

func (ts *TrackState) IsLevel() bool {
	return ts.track.TransponderAltitude == ts.previousTrack.TransponderAltitude
}

func (ts *TrackState) HaveHeading() bool {
	return !ts.previousTrack.Location.IsZero()
}

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

func (ep *ERAMPane) trackStateForACID(ctx *panes.Context, acid sim.ACID) (*TrackState, bool) {
	// Figure out the ADSB callsign for this ACID.
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			s, ok := ep.TrackState[trk.ADSBCallsign]
			return s, ok
		}
	}
	return nil, false
}

func (ep *ERAMPane) processEvents(ctx *panes.Context) {
	for _, trk := range ctx.Client.State.Tracks {
		if _, ok := ep.TrackState[trk.ADSBCallsign]; !ok {
			sa := &TrackState{}
			ep.TrackState[trk.ADSBCallsign] = sa
		}
	}
}

func (ep *ERAMPane) updateRadarTracks(ctx *panes.Context, tracks []sim.Track) {
	// Update the track states based on the current radar tracks.
	now := ctx.Client.CurrentTime()
	if now.Sub(ep.lastTrackUpdate) < 12*time.Second {
		return
	}
	ep.lastTrackUpdate = now
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]

		if trk.TypeOfFlight == av.FlightTypeDeparture && trk.IsTentative && !state.trackTime.IsZero() {
			// Get the first track for tentative tracks but then don't
			// update any further until it's no longer tentative.
			continue
		}

		state.previousTrack = state.track
		state.previousAltitude = state.track.TransponderAltitude
		state.previousTrackTime = state.trackTime
		state.track = trk.RadarTrack
		state.trackTime = now

		// Update history tracks
		idx := state.historyTrackIndex % len(state.historyTracks)
		state.historyTracks[idx] = state.track
		state.historyTrackIndex++

		// TODO: check unreasonable C
		// CA processing
		// etc
	}
}

func (ep *ERAMPane) drawTracks(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations,
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
		state := ep.TrackState[trk.ADSBCallsign]
		positionSymbol := ep.positionSymbol(trk, state)
		ep.drawTrack(trk, state, ctx, transforms, positionSymbol, trackBuilder, ld, trid, td, cb)
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

func (ep *ERAMPane) drawTrack(track sim.Track, state *TrackState, ctx *panes.Context,
	transforms radar.ScopeTransformations, position string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder,
	cb *renderer.CommandBuffer) {
	pos := state.track.Location
	pw := transforms.WindowFromLatLongP(pos)
	pt := math.Add2f(pw, [2]float32{0.5, -.5}) // Text this out
	// Draw the position symbol
	color := ep.trackColor(state, track)
	font := renderer.GetDefaultFont() // Change this to the actual font
	if ep.datablockType(ctx, track) == FullDatablock {
		// draw a diamond
		drawDiamond(ctx, transforms, color, pos, ld, cb)
	}
	td.AddTextCentered(position, pt, renderer.TextStyle{Font: font, Color: color})

	ld.GenerateCommands(cb) // why does this need to be here?
	cb.LineWidth(1, ctx.DPIScale)

}

func (ep *ERAMPane) positionSymbol(trk sim.Track, state *TrackState) string {
	symbol := "?"
	if trk.IsUnassociated() {
		switch trk.Mode {
		case av.TransponderModeStandby:
			symbol = "+"
		case av.TransponderModeAltitude:
			switch {
			case trk.Ident:
				symbol = string(0x2630)
			case trk.Squawk == 0o1200 && trk.TransponderAltitude < 100:
				symbol = "V"
			case trk.Squawk != 0o1200 && trk.TransponderAltitude < 100:
				symbol = "/"
			case trk.TransponderAltitude > 100:
				symbol = "I"
			}
		}
	} else {
		if trk.Mode == av.TransponderModeStandby {
			symbol = "X"
		} else if state.track.TransponderAltitude < 23000 {
			symbol = "\u00b7"
		} else {
			symbol = "\\"
		}
	}
	return symbol
}

func drawDiamond(ctx *panes.Context, transforms radar.ScopeTransformations, color renderer.RGB,
	pos [2]float32, ld *renderer.ColoredLinesDrawBuilder, cb *renderer.CommandBuffer) {
	cb.LineWidth(2, ctx.DPIScale)
	pt := transforms.WindowFromLatLongP(pos)
	p0 := math.Add2f(pt, [2]float32{0, 5})
	p1 := math.Add2f(pt, [2]float32{5, 0})
	p2 := math.Add2f(pt, [2]float32{0, -5})
	p3 := math.Add2f(pt, [2]float32{-5, 0})
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
}

func (ep *ERAMPane) trackColor(state *TrackState, track sim.Track) renderer.RGB {
	ps := ep.currentPrefs()
	bright := util.Select(state.DatablockType == FullDatablock, ps.Brightness.FDB, ps.Brightness.LDB)
	color := bright.ScaleRGB(renderer.RGB{.855, .855, 0})

	// Scale this color based on the type of tag it is.
	// DB and Track brights/ color are the same, so call the DB color function TODO
	return color
}

func (ep *ERAMPane) visibleTracks(ctx *panes.Context) []sim.Track { // When radar holes are added
	// Get the visible tracks based on the current range and center.
	var tracks []sim.Track
	for _, trk := range ctx.Client.State.Tracks {
		// Radar wholes neeeded for this. For now, return true
		tracks = append(tracks, *trk)
	}
	return tracks
}

// datablockBrightness returns the configured brightness for the given track's
// datablock type.
func (ep *ERAMPane) datablockBrightness(state *TrackState) radar.ScopeBrightness {
	ps := ep.currentPrefs()
	if state.DatablockType == FullDatablock {
		return ps.Brightness.FDB
	}
	return ps.Brightness.LDB
}

// leaderLineDirection returns the direction in which a datablock's leader line
// should be drawn. The initial implementation always points northeast.
func (ep *ERAMPane) leaderLineDirection(ctx *panes.Context, trk sim.Track) math.CardinalOrdinalDirection {
	// state := ep.TrackState[trk.ADSBCallsign]
	return math.North // change to state
}

// leaderLineVector returns a vector in window coordinates representing a leader
// line of a fixed length in the given direction.
func (ep *ERAMPane) leaderLineVector(dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	return math.Scale2f(v, 32)
}

// For LDBs
func (ep *ERAMPane) leaderLineVectorNoLength(dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	return math.Scale2f(v, 1)
}

// datablockVisible reports whether a datablock should be drawn. Design.
func (ep *ERAMPane) datablockVisible(ctx *panes.Context, trk sim.Track) bool {
	// design
	return true
}

// datablockType chooses which datablock format to display. Design.
func (ep *ERAMPane) datablockType(ctx *panes.Context, trk sim.Track) DatablockType {
	if trk.IsUnassociated() {
		return LimitedDatablock
	} else {
		state := ep.TrackState[trk.ADSBCallsign]
		fp := trk.FlightPlan
		if fp.TrackingController == ctx.UserTCP {
			return FullDatablock
		}
		if trk.HandingOffTo(ctx.UserTCP) {
			return FullDatablock
		}
		if state.eFDB {
			return FullDatablock
		}
		if state.eLDB {
			return LimitedDatablock
		}
	}
	return LimitedDatablock
}

// trackDatablockColorBrightness returns the track color and datablock brightness. Design.
func (ep *ERAMPane) trackDatablockColor(ctx *panes.Context, trk sim.Track) renderer.RGB {
	dType := ep.datablockType(ctx, trk)
	ps := ep.currentPrefs()
	brite := util.Select(dType == FullDatablock, ps.Brightness.FDB, ps.Brightness.LDB)
	return brite.ScaleRGB(renderer.RGB{.855, .855, 0})
}

// drawLeaderLines draws leader lines for visible datablocks.
func (ep *ERAMPane) drawLeaderLines(ctx *panes.Context, tracks []sim.Track, dbs map[av.ADSBCallsign]datablock,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	cb.LineWidth(10, ctx.DPIScale)
	for _, trk := range tracks {
		db := dbs[trk.ADSBCallsign]
		if db == nil {
			continue
		}
		dbType := ep.datablockType(ctx, trk)
		color := ep.trackDatablockColor(ctx, trk)
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}
		p0 := transforms.WindowFromLatLongP(state.track.Location)
		dir := ep.leaderLineDirection(ctx, trk)
		if dbType == LimitedDatablock || dbType == EnhancedLimitedDatablock {
			dir = math.East
		}
		v := util.Select(dbType == FullDatablock, ep.leaderLineVector(dir), ep.leaderLineVectorNoLength(dir))
		p1 := math.Add2f(p0, math.Scale2f(v, ctx.DrawPixelScale))
		if dbType == FullDatablock {
			ld.AddLine(p0, p1, color)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
}

func (ep *ERAMPane) drawPTLs(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	cb.LineWidth(10, ctx.DPIScale) // tweak this
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	for _, trk := range tracks {
		dbType := ep.datablockType(ctx, trk)
		if dbType != FullDatablock {
			continue // Only draw PTLs for full datablocks
		}
		speed := trk.Groundspeed
		state := ep.TrackState[trk.ADSBCallsign]
		dist := speed / 60 * float32(ep.velocityTime)
		pos := state.track.Location
		heading := state.TrackHeading(ctx.NmPerLongitude)
		ptlEnd := math.Offset2LL(pos, heading, dist, ctx.NmPerLongitude, ctx.MagneticVariation)
		p0 := transforms.WindowFromLatLongP(pos)
		p1 := transforms.WindowFromLatLongP(ptlEnd)
		color := ep.trackDatablockColor(ctx, trk)
		ld.AddLine(p0, p1, color)
	}
	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)

}

// drawHistoryTracks draws small position symbols representing the last few
// positions of each track.
func (ep *ERAMPane) drawHistoryTracks(ctx *panes.Context, tracks []sim.Track,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		symbol := ep.positionSymbol(trk, state)

		var bright radar.ScopeBrightness
		if trk.IsAssociated() {
			bright = ps.Brightness.PRHST
		} else {
			bright = ps.Brightness.UNPHST
		}
		color := bright.ScaleRGB(renderer.RGB{.855, .855, 0})

		for i := 0; i < len(state.historyTracks); i++ {
			idx := (state.historyTrackIndex - 1 - i) % len(state.historyTracks)
			if idx < 0 {
				idx += len(state.historyTracks)
			}
			loc := state.historyTracks[idx].Location
			if loc.IsZero() {
				continue
			}
			pw := transforms.WindowFromLatLongP(loc)
			pt := math.Add2f(pw, [2]float32{0.5, -.5})
			td.AddTextCentered(symbol, pt, renderer.TextStyle{Font: renderer.GetDefaultFont(), Color: color})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

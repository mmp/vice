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

// Gap ranges (in degrees) where reduced separation J rings should not be drawn.

type TrackState struct {
	track             av.RadarTrack
	previousTrack     av.RadarTrack
	previousAltitude  float32 // for seeing if the track is climbing or descending. This may need to be moved someplace else later
	previousTrackTime time.Time
	trackTime         time.Time
	CID               int

	historyTracks     [6]historyTrack // I think it's six?
	historyTrackIndex int

	DatablockType DatablockType

	leaderLineDirection *math.CardinalOrdinalDirection

	eLDB bool
	eFDB bool

	DisplayJRing        bool
	DisplayReducedJRing bool

	DisplayVCI bool 

	// add more as we figure out what to do...

}

type aircraftFixCoordinates struct {
	coords     [][2]float32
	deleteTime time.Time
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
		return -1
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
	for _, event := range ep.events.Get() {
		switch event.Type {
		case sim.FixCoordinatesEvent:
			ac := event.ACID
			coords := event.WaypointInfo
			ep.aircraftFixCoordinates[string(ac)] = aircraftFixCoordinates{
				coords:     coords,
				deleteTime: ctx.Client.CurrentTime().Add(15 * time.Second),
			}
		}
	}
}

func (ep *ERAMPane) updateRadarTracks(ctx *panes.Context, tracks []sim.Track) {
	// Update the track states based on the current radar tracks.
	now := ctx.Client.CurrentTime()
	if now.Sub(ep.dbLastAlternateTime) > 6*time.Second {
		ep.dbAlternate = !ep.dbAlternate
		ep.dbLastAlternateTime = now
	}
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

		// check if tracks with a reduced DRI are above FL230
		if state.DisplayReducedJRing && state.track.TransponderAltitude > 23000 {
			state.DisplayReducedJRing = false
		}

		state.previousTrack = state.track
		state.previousAltitude = state.track.TransponderAltitude
		state.previousTrackTime = state.trackTime
		state.track = trk.RadarTrack
		state.trackTime = now

		// Update history tracks
		idx := state.historyTrackIndex % len(state.historyTracks)
		state.historyTracks[idx] = historyTrack{state.track, ep.positionSymbol(trk, state)}
		state.historyTrackIndex++

		// TODO: check unreasonable C
		// CA processing
		// etc
	}
	// check QU lines; see if they need to be cleared. TODO: add QU to delete all lines
	for ac, coords := range ep.aircraftFixCoordinates {
		if coords.deleteTime.Before(ctx.Client.CurrentTime()) {
			delete(ep.aircraftFixCoordinates, ac)
		}
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
	font := ep.ERAMFont() // Change this to the actual font
	if ep.datablockType(ctx, track) == FullDatablock {
		// draw a diamond
		drawDiamond(ctx, transforms, color, pos, ld, cb)
	}
	if position == "p" {
		trackBuilder.AddCircle(pt, 2, 100, color)
	} else {
		td.AddTextCentered(position, pt, renderer.TextStyle{Font: font, Color: color})
	}

	// trackBuilder.GenerateCommands(cb)
	ld.GenerateCommands(cb) // why does this need to be here?
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
			case trk.Squawk == 0o1200 && trk.TransponderAltitude < 1000:
				symbol = "V"
			case trk.Squawk != 0o1200 && trk.TransponderAltitude < 1000:
				symbol = "/"
			case trk.TransponderAltitude > 1000:
				symbol = "I"
			}
		}
	} else {
		if trk.Mode == av.TransponderModeStandby {
			symbol = "X"
		} else if state.track.TransponderAltitude < 23000 {
			symbol = "p"
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
	color := bright.ScaleRGB(ERAMYellow)

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
func (ep *ERAMPane) leaderLineDirection(ctx *panes.Context, trk sim.Track) *math.CardinalOrdinalDirection {
	state := ep.TrackState[trk.ADSBCallsign]
	dir := state.leaderLineDirection
	if dir == nil {
		dbType := ep.datablockType(ctx, trk)
		if dbType == FullDatablock {
			direction := math.CardinalOrdinalDirection(math.NorthEast)
			dir = &direction
		} else {
			direction := math.CardinalOrdinalDirection(math.East)
			dir = &direction
		}
		state.leaderLineDirection = dir
	}
	// fmt.Println("leaderLineDirection:", *dir, "for track", trk.ADSBCallsign)
	return state.leaderLineDirection
}

// leaderLineVector returns a vector in window coordinates representing a leader
// line of a fixed length in the given direction.
func (ep *ERAMPane) leaderLineVector(dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	return math.Scale2f(v, 48)
}

// For LDBs
func (ep *ERAMPane) leaderLineVectorNoLength(dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	return math.Scale2f(v, 8)
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
	return brite.ScaleRGB(ERAMYellow)
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
			*dir = math.East
		}
		v := util.Select(dbType == FullDatablock, ep.leaderLineVector(*dir), ep.leaderLineVectorNoLength(*dir))
		if dbType == FullDatablock {
			if trk.FlightPlan.TrackingController != ctx.UserTCP && (*dir == math.NorthEast || *dir == math.East) {
				v = math.Scale2f(v, 0.7) // shorten the leader line for FDBs that are not tracked by the user
			}
		}

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
		state := ep.TrackState[trk.ADSBCallsign]
		speed := state.track.Groundspeed
		dist := speed / 60 * float32(ep.velocityTime)
		pos := state.track.Location
		heading := state.TrackHeading(ctx.NmPerLongitude)
		if heading == -1 {
			continue // dont draw PTLs for tracks that don't have a calculated heading
		}
		ptlEnd := math.Offset2LL(pos, heading, dist, ctx.NmPerLongitude, 0)
		p0 := transforms.WindowFromLatLongP(pos)
		p1 := transforms.WindowFromLatLongP(ptlEnd)
		color := ep.trackDatablockColor(ctx, trk)
		ld.AddLine(p0, p1, color)
	}
	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)

}

type historyTrack struct {
	av.RadarTrack
	PositionSymbol string
}

// drawHistoryTracks draws small position symbols representing the last few
// positions of each track.
func (ep *ERAMPane) drawHistoryTracks(ctx *panes.Context, tracks []sim.Track,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ctd := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(ctd)

	ps := ep.currentPrefs()
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]

		var bright radar.ScopeBrightness
		if trk.IsAssociated() {
			bright = ps.Brightness.PRHST
		} else {
			bright = ps.Brightness.UNPHST
		}
		color := bright.ScaleRGB(ERAMYellow)

		for _, trk := range state.historyTracks {
			loc := trk.Location
			if loc.IsZero() {
				continue
			}
			symbol := trk.PositionSymbol
			pw := transforms.WindowFromLatLongP(loc)
			pt := math.Add2f(pw, [2]float32{0.5, -.5})
			if symbol == "p" {
				vertices := radar.GetTrackVertices(ctx, 5)
				for i := range vertices {
					v0, v1 := vertices[i], vertices[(i+1)%len(vertices)]
					ctd.AddTriangle(pt, math.Add2f(pt, v0), math.Add2f(pt, v1), color)
				}

			} else {
				td.AddTextCentered(symbol, pt, renderer.TextStyle{Font: ep.ERAMFont(), Color: color})
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ctd.GenerateCommands(cb)
}

func (ep *ERAMPane) drawJRings(ctx *panes.Context, tracks []sim.Track,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	// Draw J-Rings for tracks that have them enabled.
	jr := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(jr)

	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		pos := state.track.Location
		if pos.IsZero() {
			continue
		}
		if state.DisplayJRing {
			pw := transforms.WindowFromLatLongP(pos)
			radius := 5 / transforms.PixelDistanceNM(ctx.NmPerLongitude)
			jr.AddCircle(pw, radius, 50, ERAMYellow)
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

			jr.AddGappedCircle(pw, reducedRadius, 50, reducedJRingGaps, ERAMYellow)
		}
	}
	jr.GenerateCommands(cb)
}

func (ep *ERAMPane) drawQULines(ctx *panes.Context, transforms radar.ScopeTransformations,
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
		acWindowPos := transforms.WindowFromLatLongP(state.track.Location)
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

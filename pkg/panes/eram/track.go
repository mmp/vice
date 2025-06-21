package eram

import (
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
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
		var positionSymbol string = "?"
		if trk.IsUnassociated() {
			switch trk.Mode {
			case av.TransponderModeStandby:
				positionSymbol = "+" // Find the actual character for this
			case av.TransponderModeAltitude:
				switch {
				case trk.Ident:
					positionSymbol = string(0x2630) // Hopefully the correct font will make this a normal character
				case trk.Squawk == 0o1200 && trk.TransponderAltitude < 100: // Below CA floor TODO: Find real CA floor
					positionSymbol = "V"
				case trk.Squawk != 0o1200 && trk.TransponderAltitude < 100: // Below CA floor
					positionSymbol = "/" // Hopefully the correct font will make this
					// case trk.Squawk : // Above CA floor
					// 	positionSymbol = "I"
				case trk.TransponderAltitude > 100:
					positionSymbol = "I"
				}
			}
		} else {
			if trk.Mode == av.TransponderModeStandby {
				positionSymbol = "X"
			} else if trk.TransponderAltitude > 23000 {
				positionSymbol = "\\"
			} else {
				positionSymbol = "\u00b7" // Find a bigger symbol
			}
		}
		ep.drawTrack(trk, state, ctx, transforms, positionSymbol, trackBuilder, ld, trid, td)
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
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder) {
	pos := state.track.Location
	pw := transforms.WindowFromLatLongP(pos)
	pt := math.Add2f(pw, [2]float32{0.5, -.5}) // Text this out
	// Draw the position symbol
	color := ep.trackColor(state, track)
	font := renderer.GetDefaultFont() // Change this to the actual font
	td.AddTextCentered(position, pt, renderer.TextStyle{Font: font, Color: color})
}

func (ep *ERAMPane) trackColor(state *TrackState, track sim.Track) renderer.RGB {
	color := renderer.RGB{.855, .855, 0} // standard color for all

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
	return math.East // change to state
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
func (ep *ERAMPane) trackDatablockColorBrightness(ctx *panes.Context, trk sim.Track) (renderer.RGB, radar.ScopeBrightness, radar.ScopeBrightness) {
	// design
	return renderer.RGB{}, 0, 0
}

// drawLeaderLines draws leader lines for visible datablocks.
func (ep *ERAMPane) drawLeaderLines(ctx *panes.Context, tracks []sim.Track, dbs map[av.ADSBCallsign]datablock,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	for _, trk := range tracks {
		if dbs[trk.ADSBCallsign] == nil {
			continue
		}

		_, brightness, _ := ep.trackDatablockColorBrightness(ctx, trk)
		if brightness == 0 {
			continue
		}

		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}
		p0 := transforms.WindowFromLatLongP(state.track.Location)
		dir := ep.leaderLineDirection(ctx, trk)
		v := ep.leaderLineVector(dir)
		p1 := math.Add2f(p0, math.Scale2f(v, ctx.DrawPixelScale))
		ld.AddLine(p0, p1, brightness.ScaleRGB(renderer.RGB{R: 1, G: 1, B: 1}))
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
}

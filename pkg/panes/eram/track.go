package eram

import (
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/sim"
)

type TrackState struct {
	track             av.RadarTrack
	previousTrack     av.RadarTrack
	previousTrackTime time.Time
	lastTrackUpdate   time.Time
	trackTime         time.Time
	CID               int

	historyTracks     [6]av.RadarTrack // I think it's six?
	historyTrackIndex int

	DatablockType DatablockType

	JRingRadius float32
	// add more as we figure out what to do...

}

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
	// ...
}

func (ep *ERAMPane) updateRadarTracks(ctx *panes.Context, tracks []sim.Track) {
	// Update the track states based on the current radar tracks.
	now := ctx.Client.CurrentTime()

	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if state.track.TransponderAltitude > 230000 && now.Sub(state.lastTrackUpdate) < 15*time.Second {
			return
		} else if state.track.TransponderAltitude < 230000 && now.Sub(state.lastTrackUpdate) < 5*time.Second {
			return
		}
		state.lastTrackUpdate = now

		if trk.TypeOfFlight == av.FlightTypeDeparture && trk.IsTentative && !state.trackTime.IsZero() {
			// Get the first track for tentative tracks but then don't
			// update any further until it's no longer tentative.
			continue
		}

		state.previousTrack = state.track
		state.previousTrackTime = state.trackTime
		state.track = trk.RadarTrack
		state.trackTime = now

		// TODO: check unreasonable C
		// CA processing
		// etc
	}
}


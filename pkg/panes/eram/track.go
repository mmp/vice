package eram

import av "github.com/mmp/vice/pkg/aviation"

type AircraftState struct {
	track av.RadarTrack
	previousTrack av.RadarTrack

	historyTracks [10]av.RadarTrack // keep it at 10 for now; I'll change it when I find out what the max truly is (if there even is one at all)
	historyTrackIndex int 

	DatablockType DatablockType

	// add more as we figure out what to do...

}
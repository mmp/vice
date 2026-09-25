// eram/datablock_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

func TestAltitudeFormatFieldB(t *testing.T) {
	for _, c := range []struct {
		name    string
		fp      sim.NASFlightPlan
		alt     float32
		reached bool
		want    string
	}{
		{"at assigned", sim.NASFlightPlan{AssignedAltitude: 30000}, 30000, true, "300C"},
		{"climbing", sim.NASFlightPlan{AssignedAltitude: 30000}, 25300, false, "300" + upArrow + "253"},
		{"descending", sim.NASFlightPlan{AssignedAltitude: 23000}, 25300, false, "230" + downArrow + "253"},
		{"climbed through", sim.NASFlightPlan{AssignedAltitude: 23000}, 25300, true, "230+253"},
		{"descended below", sim.NASFlightPlan{AssignedAltitude: 23000}, 21200, true, "230-212"},
		{"interim", sim.NASFlightPlan{AssignedAltitude: 30000, InterimAlt: 10000}, 25300, false, "100T253"},
		{"above block", sim.NASFlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 35300, false, "200B353"},
		{"within block", sim.NASFlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 22100, false, "200B250"},
		{"block ceiling", sim.NASFlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 25040, false, "200B250"},
		{"below block", sim.NASFlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 19300, false, "200B193"},
	} {
		trk := sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1"}, FlightPlan: &c.fp}
		ep := &Scope{TrackState: map[av.ADSBCallsign]*TrackState{
			"AAL1": {Track: av.RadarTrack{TransponderAltitude: c.alt}, ReachedAltitude: c.reached},
		}}
		if got := ep.getAltitudeFormat(trk); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

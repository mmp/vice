// stars/datablock_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

func TestFormatAltitudeUsesRadarSample(t *testing.T) {
	// The live track is always at 21000; the datablock must show the
	// radar sample instead.
	live := sim.Track{RadarTrack: av.RadarTrack{Mode: av.TransponderModeAltitude, TransponderAltitude: 21000}}
	sample := func(mode av.TransponderMode, alt float32) av.RadarTrack {
		return av.RadarTrack{Mode: mode, TransponderAltitude: alt}
	}

	for _, c := range []struct {
		name         string
		rt           av.RadarTrack
		sfp          *sim.NASFlightPlan
		unreasonable bool
		want         string
		wantPilot    bool
	}{
		{"sampled altitude", sample(av.TransponderModeAltitude, 20000), nil, false, "200", false},
		{"below sea level", sample(av.TransponderModeAltitude, -500), nil, false, "N05", false},
		{"sample in standby", sample(av.TransponderModeStandby, 0), nil, false, "RDR", false},
		{"unreasonable mode C", sample(av.TransponderModeAltitude, 20000), nil, true, "XXX", false},
		{"pilot reported", sample(av.TransponderModeOn, 0), &sim.NASFlightPlan{PilotReportedAltitude: 5000}, false, "050", true},
	} {
		got, pilot := formatAltitude(live, c.rt, c.sfp, c.unreasonable)
		if got != c.want || pilot != c.wantPilot {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, pilot, c.want, c.wantPilot)
		}
	}
}
